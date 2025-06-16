// Copyright 2019 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package maestroapplication

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openshift-online/maestro/pkg/api/openapi"
	"github.com/openshift-online/maestro/pkg/client/cloudevents/grpcsource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog"
	workv1client "open-cluster-management.io/api/client/work/clientset/versioned/typed/work/v1"
	workv1 "open-cluster-management.io/api/work/v1"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/options/grpc"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// KubernetesInternalAPIServerAddr is address of the k8s API server when accessing internal to the cluster
	KubernetesInternalAPIServerAddr = "https://kubernetes.default.svc"
)

func containsValidPullLabel(labels map[string]string) bool {
	if len(labels) == 0 {
		return false
	}

	if pullLabelStr, ok := labels[LabelKeyPull]; ok {
		isPull, err := strconv.ParseBool(pullLabelStr)
		if err != nil {
			return false
		}

		return isPull
	}

	return false
}

func containsValidPullAnnotation(annos map[string]string) bool {
	if len(annos) == 0 {
		return false
	}

	managedClusterName, ok := annos[AnnotationKeyOCMManagedCluster]

	return ok && len(managedClusterName) > 0
}

func GetServiceURL(c client.Client, serviceName, serviceNS, protocol string) (string, error) {
	serviceKey := client.ObjectKey{
		Namespace: serviceNS,
		Name:      serviceName,
	}

	service := &corev1.Service{}
	if err := c.Get(context.TODO(), serviceKey, service); err != nil {
		return "", fmt.Errorf("failed to get service %s/%s: %w", serviceNS, serviceName, err)
	}

	if service.Spec.ClusterIP == "" {
		return "", fmt.Errorf("service %s/%s has no clusterIP", serviceNS, serviceName)
	}

	if len(service.Spec.Ports) == 0 {
		return "", fmt.Errorf("service %s/%s has no ports defined", serviceNS, serviceName)
	}

	serviceDNS := serviceName + "." + serviceNS + ".svc.cluster.local"

	// serviceURL := net.JoinHostPort(service.Spec.ClusterIP, fmt.Sprint(service.Spec.Ports[0].Port))

	serviceURL := net.JoinHostPort(serviceDNS, strconv.Itoa(int(service.Spec.Ports[0].Port)))
	if protocol > "" {
		serviceURL = protocol + "://" + net.JoinHostPort(serviceDNS, strconv.Itoa(int(service.Spec.Ports[0].Port)))
	}

	klog.Infof("service URL: %v", serviceURL)

	return serviceURL, nil
}

func BuildWorkClient(ctx context.Context, maestroServerAddr, grpcServerAddr string) (workv1client.WorkV1Interface, error) {
	maestroAPIClient := openapi.NewAPIClient(&openapi.Configuration{
		DefaultHeader: make(map[string]string),
		UserAgent:     "OpenAPI-Generator/1.0.0/go",
		Debug:         false,
		Servers: openapi.ServerConfigurations{
			{
				URL:         maestroServerAddr,
				Description: "current domain",
			},
		},
		OperationServers: map[string]openapi.ServerConfigurations{},
		HTTPClient: &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			}},
			Timeout: 10 * time.Second,
		},
	})

	grpcOptions := grpc.NewGRPCOptions()
	grpcOptions.Dialer = &grpc.GRPCDialer{}
	grpcOptions.Dialer.URL = grpcServerAddr

	workClient, err := grpcsource.NewMaestroGRPCSourceWorkClient(
		ctx,
		maestroAPIClient,
		grpcOptions,
		"app-work-client",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create the Maestro GRPC work client, err: %w", err)
	}

	return workClient, nil
}

// generateAppNamespace returns the intended namespace for the Application in the following priority
// 1) Annotation specified custom namespace
// 2) Application's namespace value
// 3) Fallsback to the default 'openshift-gitops' namespace
func generateAppNamespace(namespace string, annos map[string]string) string {
	appNamespace := annos[AnnotationKeyOCMManagedClusterAppNamespace]
	if len(appNamespace) > 0 {
		return appNamespace
	}
	appNamespace = namespace

	if len(appNamespace) > 0 {
		return appNamespace
	}

	return "openshift-gitops"
}

// generateMaestroManifestWorkName returns the ManifestWork name for a given application.
func generateMaestroManifestWorkName(appNs, appName string) string {
	return appNs + "-" + appName
}

// GenerateManifestWorkAppSetHashLabelValue returns the sha1 hash value of appSet namespace and name
func GenerateManifestWorkAppSetHashLabelValue(appSetNamespace, appSetName string) (string, error) {
	preHash := appSetNamespace + "/" + appSetName
	h := sha1.New() // #nosec G401 not used for encryption
	_, err := h.Write([]byte(preHash))

	if err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// getAppSetOwnerName returns the applicationSet resource name if the given application contains an applicationSet owner
func getAppSetOwnerName(ownerRefs []metav1.OwnerReference) string {
	if len(ownerRefs) > 0 {
		for _, ownerRef := range ownerRefs {
			if strings.EqualFold(ownerRef.APIVersion, "argoproj.io/v1alpha1") &&
				strings.EqualFold(ownerRef.Kind, "ApplicationSet") {
				return ownerRef.Name
			}
		}
	}

	return ""
}

func prepareApplicationForWorkPayload(application *unstructured.Unstructured) unstructured.Unstructured {
	newApp := &unstructured.Unstructured{}
	newApp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})
	newApp.SetNamespace(generateAppNamespace(application.GetNamespace(), application.GetAnnotations()))
	newApp.SetName(application.GetName())
	newApp.SetFinalizers(application.GetFinalizers())

	// set the operation field
	if operation, ok := application.Object["operation"].(map[string]interface{}); ok {
		newApp.Object["operation"] = operation
	}

	// set the spec field
	if newSpec, ok := application.Object["spec"].(map[string]interface{}); ok {
		if destination, ok := newSpec["destination"].(map[string]interface{}); ok {
			// empty the name
			destination["name"] = ""
			// always set for in-cluster destination
			destination["server"] = KubernetesInternalAPIServerAddr
		}
		newApp.Object["spec"] = newSpec
	}

	// copy the labels except for the ocm specific labels
	labels := make(map[string]string)

	for key, value := range application.GetLabels() {
		if key != LabelKeyPull {
			labels[key] = value
		}
	}

	newApp.SetLabels(labels)

	// copy the annos except for the ocm specific annos
	annotations := make(map[string]string)

	for key, value := range application.GetAnnotations() {
		if key != AnnotationKeyOCMManagedCluster &&
			key != AnnotationKeyOCMManagedClusterAppNamespace &&
			key != AnnotationKeyAppSkipReconcile {
			annotations[key] = value
		}
	}

	newApp.SetAnnotations(annotations)

	appSetOwnerName := getAppSetOwnerName(application.GetOwnerReferences())

	if appSetOwnerName != "" {
		labels[LabelKeyAppSet] = strconv.FormatBool(true)
		annotations[AnnotationKeyAppSet] = application.GetNamespace() + "/" + appSetOwnerName

		newApp.SetLabels(labels)
		newApp.SetAnnotations(annotations)
	}

	return *newApp
}

// generateManifestWork creates the ManifestWork that wraps the Application as payload
// With the status sync feedback of Application's health status and sync status.
// The Application payload Spec Destination values are modified so that the Application is always performing in-cluster resource deployments.
// If the Application is generated from an ApplicationSet, custom label and annotation are inserted.
func generateManifestWork(name, namespace string, app *unstructured.Unstructured) (*workv1.ManifestWork, error) {
	workLabels := map[string]string{
		LabelKeyPull: strconv.FormatBool(true),
	}

	workAnnos := map[string]string{
		AnnotationKeyHubApplicationNamespace: app.GetNamespace(),
		AnnotationKeyHubApplicationName:      app.GetName(),
	}

	appSetOwnerName := getAppSetOwnerName(app.GetOwnerReferences())
	if appSetOwnerName != "" {
		appSetHash, err := GenerateManifestWorkAppSetHashLabelValue(app.GetNamespace(), appSetOwnerName)
		if err != nil {
			return nil, err
		}

		workLabels = map[string]string{
			LabelKeyAppSet:     strconv.FormatBool(true),
			LabelKeyAppSetHash: appSetHash}
		workAnnos[AnnotationKeyAppSet] = app.GetNamespace() + "/" + appSetOwnerName
	}

	application := prepareApplicationForWorkPayload(app)

	return &workv1.ManifestWork{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      workLabels,
			Annotations: workAnnos,
		},
		Spec: workv1.ManifestWorkSpec{
			Workload: workv1.ManifestsTemplate{
				Manifests: []workv1.Manifest{{RawExtension: runtime.RawExtension{Object: &application}}},
			},
			ManifestConfigs: []workv1.ManifestConfigOption{
				{
					ResourceIdentifier: workv1.ResourceIdentifier{
						Group:     "argoproj.io",
						Resource:  "applications",
						Namespace: application.GetNamespace(),
						Name:      application.GetName(),
					},
					FeedbackRules: []workv1.FeedbackRule{
						{
							Type: workv1.JSONPathsType,
							JsonPaths: []workv1.JsonPath{
								{
									Name: "status",
									Path: ".status",
								},
							},
						},
					},
					UpdateStrategy: &workv1.UpdateStrategy{
						Type: workv1.UpdateStrategyTypeServerSideApply,
					},
				},
			},
		},
	}, nil
}
