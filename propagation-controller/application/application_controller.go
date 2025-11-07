/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package application

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	clusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
)

const (
	// Application annotation that dictates which managed cluster this Application should be pulled to
	AnnotationKeyOCMManagedCluster = "apps.open-cluster-management.io/ocm-managed-cluster"
	// Application annotation that dictates which managed cluster namespace this Application should be pulled to
	AnnotationKeyOCMManagedClusterAppNamespace = "apps.open-cluster-management.io/ocm-managed-cluster-app-namespace"
	// Application and ManifestWork annotation that shows which ApplicationSet is the grand parent of this work
	AnnotationKeyAppSet = "apps.open-cluster-management.io/hosting-applicationset"
	// Application annotation that enables the skip reconciliation of an application
	AnnotationKeyAppSkipReconcile = "argocd.argoproj.io/skip-reconcile"
	// Application annotation that triggers ArgoCD to refresh the application (propagated like operation field)
	AnnotationKeyAppRefresh = "argocd.argoproj.io/refresh"
	// ManifestWork annotation that shows the namespace of the hub Application.
	AnnotationKeyHubApplicationNamespace = "apps.open-cluster-management.io/hub-application-namespace"
	// ManifestWork annotation that shows the name of the hub Application.
	AnnotationKeyHubApplicationName = "apps.open-cluster-management.io/hub-application-name"
	// Application and ManifestWork label that shows that ApplicationSet is the grand parent of this work
	LabelKeyAppSet = "apps.open-cluster-management.io/application-set"
	// ManifestWork label with the ApplicationSet namespace and name in sha1 hash value
	LabelKeyAppSetHash = "apps.open-cluster-management.io/application-set-hash"
	// Application label that enables the pull controller to wrap the Application in ManifestWork payload
	LabelKeyPull = "apps.open-cluster-management.io/pull-to-ocm-managed-cluster"
	// ResourcesFinalizerName is the finalizer value which we inject to finalize deletion of an application
	ResourcesFinalizerName string = "resources-finalizer.argocd.argoproj.io"
)

// ApplicationReconciler reconciles a Application object
type ApplicationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=argoproj.io,resources=appprojects,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
//+kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=work.open-cluster-management.io,resources=manifestworks,verbs=get;list;watch;create;update;patch;delete

// ApplicationPredicateFunctions defines which Application this controller should wrap inside ManifestWork's payload
var ApplicationPredicateFunctions = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		newApp := e.ObjectNew.(*unstructured.Unstructured)
		oldApp := e.ObjectOld.(*unstructured.Unstructured)
		oldAppCopy := oldApp.DeepCopy()
		newAppCopy := newApp.DeepCopy()
		unstructured.RemoveNestedField(oldAppCopy.Object, "status")
		unstructured.RemoveNestedField(newAppCopy.Object, "status")
		isChanged := !reflect.DeepEqual(oldAppCopy.Object, newAppCopy.Object)
		return containsValidPullLabel(newApp.GetLabels()) && containsValidPullAnnotation(newApp.GetAnnotations()) && isChanged
	},
	CreateFunc: func(e event.CreateEvent) bool {
		app := e.Object.(*unstructured.Unstructured)
		return containsValidPullLabel(app.GetLabels()) && containsValidPullAnnotation(app.GetAnnotations())
	},

	DeleteFunc: func(e event.DeleteEvent) bool {
		app := e.Object.(*unstructured.Unstructured)
		return containsValidPullLabel(app.GetLabels()) && containsValidPullAnnotation(app.GetAnnotations())
	},
}

// SetupWithManager sets up the controller with the Manager.
func (r *ApplicationReconciler) SetupWithManager(mgr ctrl.Manager, maxConcurrentReconciles int) error {
	applicationGVK := &unstructured.Unstructured{}
	applicationGVK.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		For(applicationGVK).
		WithEventFilter(ApplicationPredicateFunctions).
		Complete(r)
}

func (r *ApplicationReconciler) isLocalCluster(clusterName string) bool {
	managedCluster := &clusterv1.ManagedCluster{}
	managedClusterKey := types.NamespacedName{
		Name: clusterName,
	}
	err := r.Get(context.TODO(), managedClusterKey, managedCluster)

	if err != nil {
		klog.Errorf("Failed to find managed cluster: %v, error: %v ", clusterName, err)
		return false
	}

	labels := managedCluster.GetLabels()

	if labels == nil {
		labels = make(map[string]string)
	}

	if strings.EqualFold(labels["local-cluster"], "true") {
		klog.Infof("This is local-cluster: %v", clusterName)
		return true
	}

	return false
}

// discoverAndFetchAppProject discovers the ArgoCD instance namespace and fetches the AppProject by:
// 1. Searching for deployments with label app.kubernetes.io/part-of=argocd
// 2. Falling back to common namespaces
// Returns the AppProject if found, or an error if not found in any namespace
func (r *ApplicationReconciler) discoverAndFetchAppProject(application *unstructured.Unstructured, projectName string) (*unstructured.Unstructured, error) {
	// Helper function to fetch an AppProject
	fetchAppProject := func(namespace string) (*unstructured.Unstructured, error) {
		appProject := &unstructured.Unstructured{}
		appProject.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "argoproj.io",
			Version: "v1alpha1",
			Kind:    "AppProject",
		})

		err := r.Get(context.TODO(), types.NamespacedName{
			Name:      projectName,
			Namespace: namespace,
		}, appProject)

		return appProject, err
	}

	// 1. Search for ArgoCD deployments with the label app.kubernetes.io/part-of=argocd
	deploymentList := &unstructured.UnstructuredList{}
	deploymentList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "DeploymentList",
	})

	listOptions := []client.ListOption{
		client.MatchingLabels{"app.kubernetes.io/part-of": "argocd"},
	}

	err := r.List(context.TODO(), deploymentList, listOptions...)
	if err != nil {
		klog.Warningf("Failed to list ArgoCD deployments: %v", err)
	} else if len(deploymentList.Items) > 0 {
		// Found ArgoCD deployments, extract the namespace
		argocdNamespace := deploymentList.Items[0].GetNamespace()
		klog.Infof("Discovered ArgoCD namespace from deployment: %s", argocdNamespace)

		appProject, err := fetchAppProject(argocdNamespace)
		if err == nil {
			klog.Infof("Found AppProject %s in discovered namespace %s", projectName, argocdNamespace)
			return appProject, nil
		}

		klog.Warningf("Found ArgoCD deployment in namespace %s but AppProject %s not found there: %v", argocdNamespace, projectName, err)
	}

	// 2. Fall back to trying common ArgoCD namespaces
	candidateNamespaces := []string{
		application.GetNamespace(), // Try application's namespace first
		"openshift-gitops",         // Default OpenShift GitOps namespace
		"argocd",                   // Common ArgoCD namespace
	}

	// Try each candidate namespace
	for _, ns := range candidateNamespaces {
		appProject, err := fetchAppProject(ns)
		if err == nil {
			klog.Infof("Found AppProject %s in fallback namespace %s", projectName, ns)
			return appProject, nil
		}

		if !errors.IsNotFound(err) {
			klog.Infof("Error checking AppProject in namespace %s: %v", ns, err)
		}
	}

	return nil, fmt.Errorf("failed to discover AppProject %s: not found in any candidate namespace", projectName)
}

// prepareAppproject fetches the AppProject referenced by the Application and prepares it for ManifestWork payload
// - discovers the ArgoCD instance namespace dynamically and fetches the AppProject
// - keeps only name, namespace, labels, and spec
func (r *ApplicationReconciler) prepareAppproject(application *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	// Get the project name from application.spec.project
	projectName, found, err := unstructured.NestedString(application.Object, "spec", "project")
	if err != nil {
		return nil, err
	}
	if !found || projectName == "" {
		return nil, fmt.Errorf("Application %s/%s does not have spec.project field", application.GetNamespace(), application.GetName())
	}

	// Discover and fetch the AppProject from the ArgoCD instance namespace
	appProject, err := r.discoverAndFetchAppProject(application, projectName)
	if err != nil {
		return nil, err
	}

	// Build a new AppProject with only name, namespace, labels, and spec
	newAppProject := &unstructured.Unstructured{}
	newAppProject.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "AppProject",
	})
	newAppProject.SetName(appProject.GetName())
	newAppProject.SetNamespace(appProject.GetNamespace())

	newAppProject.SetLabels(appProject.GetLabels())

	// copy the annos except for the last-applied-configuration annotation
	annotations := make(map[string]string)

	for key, value := range appProject.GetAnnotations() {
		if key != "kubectl.kubernetes.io/last-applied-configuration" {
			annotations[key] = value
		}
	}

	newAppProject.SetAnnotations(annotations)

	// Copy the spec field
	if spec, ok := appProject.Object["spec"].(map[string]interface{}); ok {
		newAppProject.Object["spec"] = spec
	}

	return newAppProject, nil
}

// generateManifestWork creates the ManifestWork that wraps the Application as payload
// With the status sync feedback of Application's health status and sync status.
// The Application payload Spec Destination values are modified so that the Application is always performing in-cluster resource deployments.
// If the Application is generated from an ApplicationSet, custom label and annotation are inserted.
func (r *ApplicationReconciler) generateManifestWork(name, namespace string, app *unstructured.Unstructured) (*workv1.ManifestWork, error) {
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

	//generate appproject by fetching the appproject referenced by the application
	appProject, err := r.prepareAppproject(app)
	if err != nil {
		return nil, err
	}

	// prepare namespace manifest
	manifestNSString, err := prepareManifestWorkNS(application.GetNamespace())
	if err != nil {
		return nil, err
	}

	// Build manifests list - exclude the default appProject "openshift-gitops/default"
	manifests := []workv1.Manifest{
		{
			RawExtension: runtime.RawExtension{
				Raw: []byte(manifestNSString),
			},
		},
	}

	includeAppProject := !(appProject.GetName() == "default" && appProject.GetNamespace() == "openshift-gitops")

	if includeAppProject {
		manifests = append(manifests, workv1.Manifest{
			RawExtension: runtime.RawExtension{
				Object: appProject,
			},
		})
	}

	manifests = append(manifests, workv1.Manifest{
		RawExtension: runtime.RawExtension{
			Object: &application,
		},
	})

	// Build orphaning rules - exclude the default appProject "openshift-gitops/default"
	orphaningRules := []workv1.OrphaningRule{
		{
			Group:     "",
			Namespace: "",
			Resource:  "namespaces",
			Name:      application.GetNamespace(),
		},
	}

	if includeAppProject {
		orphaningRules = append(orphaningRules, workv1.OrphaningRule{
			Group:     "argoproj.io",
			Namespace: appProject.GetNamespace(),
			Resource:  "appprojects",
			Name:      appProject.GetName(),
		})
	}

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
				Manifests: manifests,
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
						{Type: workv1.JSONPathsType, JsonPaths: []workv1.JsonPath{{Name: "healthStatus", Path: ".status.health.status"}}},
						{Type: workv1.JSONPathsType, JsonPaths: []workv1.JsonPath{{Name: "syncStatus", Path: ".status.sync.status"}}},
						{Type: workv1.JSONPathsType, JsonPaths: []workv1.JsonPath{{Name: "operationStateStartedAt", Path: ".status.operationState.startedAt"}}},
						{Type: workv1.JSONPathsType, JsonPaths: []workv1.JsonPath{{Name: "operationStatePhase", Path: ".status.operationState.phase"}}},
						{Type: workv1.JSONPathsType, JsonPaths: []workv1.JsonPath{{Name: "syncRevision", Path: ".status.sync.revision"}}},
					},
					UpdateStrategy: &workv1.UpdateStrategy{
						Type: workv1.UpdateStrategyTypeServerSideApply,
						ServerSideApply: &workv1.ServerSideApplyConfig{
							Force: true,
							IgnoreFields: []workv1.IgnoreField{
								{
									Condition: workv1.IgnoreFieldsConditionOnSpokeChange,
									// Ignore changes to operation field and refresh annotation (handles all values: normal, hard, etc.)
									JSONPaths: []string{".operation", ".metadata.annotations[\"argocd.argoproj.io/refresh\"]"},
								},
							},
						},
					},
				},
			},
			DeleteOption: &workv1.DeleteOption{
				PropagationPolicy: workv1.DeletePropagationPolicyTypeSelectivelyOrphan,
				SelectivelyOrphan: &workv1.SelectivelyOrphan{
					OrphaningRules: orphaningRules,
				},
			},
		},
	}, nil
}

// Reconcile create/update/delete ManifestWork with the Application as its payload
func (r *ApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling Application...")

	application := &unstructured.Unstructured{}
	application.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	if err := r.Get(ctx, req.NamespacedName, application); err != nil {
		log.Error(err, "unable to fetch Application")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	managedClusterName := application.GetAnnotations()[AnnotationKeyOCMManagedCluster]

	if r.isLocalCluster(managedClusterName) {
		log.Info("skipping Application with the local-cluster as Managed Cluster")

		return ctrl.Result{}, nil
	}

	mwName := generateManifestWorkName(application.GetName(), application.GetUID())

	// the Application is being deleted, find the ManifestWork and delete that as well
	if application.GetDeletionTimestamp() != nil {
		// remove finalizer from Application but do not 'commit' yet
		if len(application.GetFinalizers()) != 0 {
			f := application.GetFinalizers()
			for i := 0; i < len(f); i++ {
				if f[i] == ResourcesFinalizerName {
					f = append(f[:i], f[i+1:]...)
					i--
				}
			}

			application.SetFinalizers(f)
		}

		// delete the ManifestWork associated with this Application
		var work workv1.ManifestWork
		err := r.Get(ctx, types.NamespacedName{Name: mwName, Namespace: managedClusterName}, &work)

		if errors.IsNotFound(err) {
			// already deleted ManifestWork, commit the Application finalizer removal
			if err = r.Update(ctx, application); err != nil {
				log.Error(err, "unable to update Application")
				return ctrl.Result{}, err
			}
		} else if err != nil {
			log.Error(err, "unable to fetch ManifestWork")
			return ctrl.Result{}, err
		}

		if err := r.Delete(ctx, &work); err != nil {
			log.Error(err, "unable to delete ManifestWork")
			return ctrl.Result{}, err
		}

		// deleted ManifestWork, commit the Application finalizer removal
		if err := r.Update(ctx, application); err != nil {
			log.Error(err, "unable to update Application")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// verify the ManagedCluster actually exists
	var managedCluster clusterv1.ManagedCluster
	if err := r.Get(ctx, types.NamespacedName{Name: managedClusterName}, &managedCluster); err != nil {
		log.Error(err, "unable to fetch ManagedCluster")
		return ctrl.Result{}, err
	}

	log.Info("generating ManifestWork for Application")
	w, err := r.generateManifestWork(mwName, managedClusterName, application)

	if err != nil {
		log.Error(err, "unable to generating ManifestWork")
		return ctrl.Result{}, err
	}

	// create or update the ManifestWork depends if it already exists or not
	var mw workv1.ManifestWork
	err = r.Get(ctx, types.NamespacedName{Name: mwName, Namespace: managedClusterName}, &mw)

	if errors.IsNotFound(err) {
		err = r.Client.Create(ctx, w)
		if err != nil {
			log.Error(err, "unable to create ManifestWork")
			return ctrl.Result{}, err
		}
	} else if err == nil {
		mw.Annotations = w.Annotations
		mw.Labels = w.Labels
		mw.Spec = w.Spec
		err = r.Client.Update(ctx, &mw)

		if err != nil {
			log.Error(err, "unable to update ManifestWork")
			return ctrl.Result{}, err
		}
	} else {
		log.Error(err, "unable to fetch ManifestWork")
		return ctrl.Result{}, err
	}

	// remove the operation field and refresh annotation from application after propagation
	needsUpdate := false

	if _, ok := application.Object["operation"]; ok {
		delete(application.Object, "operation")
		needsUpdate = true
	}

	// remove the refresh annotation from application after propagation (treat same as operation field)
	annotations := application.GetAnnotations()
	if annotations != nil {
		if _, exists := annotations[AnnotationKeyAppRefresh]; exists {
			delete(annotations, AnnotationKeyAppRefresh)
			application.SetAnnotations(annotations)
			needsUpdate = true
		}
	}

	if needsUpdate {
		if err := r.Update(ctx, application); err != nil {
			log.Error(err, "unable to remove operation and/or refresh annotation from Application")
			return ctrl.Result{}, err
		}
	}

	log.Info("done reconciling Application")

	return ctrl.Result{}, nil
}
