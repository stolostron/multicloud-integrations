/*


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

package gitopsaddon

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	k8syaml "sigs.k8s.io/yaml"
)

//nolint:all
//go:embed charts/openshift-gitops-operator/**
//go:embed charts/openshift-gitops-dependency/**
//go:embed charts/dep-crds/**
//go:embed charts/argocd-agent/**
var ChartFS embed.FS

// GitopsAddonReconciler reconciles a openshift gitops operator
type GitopsAddonReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	Config                   *rest.Config
	Interval                 int
	GitopsOperatorImage      string
	GitopsOperatorNS         string
	GitopsImage              string
	GitopsNS                 string
	RedisImage               string
	ReconcileScope           string
	HTTP_PROXY               string
	HTTPS_PROXY              string
	NO_PROXY                 string
	ACTION                   string
	ArgoCDAgentEnabled       string
	ArgoCDAgentImage         string
	ArgoCDAgentServerAddress string
	ArgoCDAgentServerPort    string
	ArgoCDAgentMode          string
}

func SetupWithManager(mgr manager.Manager, interval int, gitopsOperatorImage, gitopsOperatorNS,
	gitopsImage, gitopsNS, redisImage, reconcileScope,
	HTTP_PROXY, HTTPS_PROXY, NO_PROXY, ACTION, argoCDAgentEnabled, argoCDAgentImage, argoCDAgentServerAddress, argoCDAgentServerPort, argoCDAgentMode string) error {
	dsRS := &GitopsAddonReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		Config:                   mgr.GetConfig(),
		Interval:                 interval,
		GitopsOperatorImage:      gitopsOperatorImage,
		GitopsOperatorNS:         gitopsOperatorNS,
		GitopsImage:              gitopsImage,
		GitopsNS:                 gitopsNS,
		RedisImage:               redisImage,
		ReconcileScope:           reconcileScope,
		HTTP_PROXY:               HTTP_PROXY,
		HTTPS_PROXY:              HTTPS_PROXY,
		NO_PROXY:                 NO_PROXY,
		ACTION:                   ACTION,
		ArgoCDAgentEnabled:       argoCDAgentEnabled,
		ArgoCDAgentImage:         argoCDAgentImage,
		ArgoCDAgentServerAddress: argoCDAgentServerAddress,
		ArgoCDAgentServerPort:    argoCDAgentServerPort,
		ArgoCDAgentMode:          argoCDAgentMode,
	}

	// Setup the secret controller to watch and copy ArgoCD agent client cert secrets
	if err := SetupSecretControllerWithManager(mgr); err != nil {
		return err
	}

	return mgr.Add(dsRS)
}

func (r *GitopsAddonReconciler) Start(ctx context.Context) error {
	go wait.Until(func() {
		configFlags := genericclioptions.NewConfigFlags(false)
		configFlags.APIServer = &r.Config.Host
		configFlags.BearerToken = &r.Config.BearerToken
		configFlags.CAFile = &r.Config.CAFile
		configFlags.CertFile = &r.Config.CertFile
		configFlags.KeyFile = &r.Config.KeyFile

		r.houseKeeping(configFlags)
	}, time.Duration(r.Interval)*time.Second, ctx.Done())

	return nil
}

func (r *GitopsAddonReconciler) houseKeeping(configFlags *genericclioptions.ConfigFlags) {
	switch r.ACTION {
	case "Install":
		r.installOrUpdateOpenshiftGitops(configFlags)
	case "Delete-Operator":
		r.deleteOpenshiftGitopsOperator(configFlags)
	case "Delete-Instance":
		r.deleteOpenshiftGitopsInstance(configFlags)
	default:
		r.installOrUpdateOpenshiftGitops(configFlags)
	}
}

func (r *GitopsAddonReconciler) deleteOpenshiftGitopsOperator(configFlags *genericclioptions.ConfigFlags) {
	klog.Info("Start deleting openshift gitops operator...")

	if err := r.deleteChart(configFlags, r.GitopsOperatorNS, "openshift-gitops-operator"); err == nil {
		gitopsOperatorNsKey := types.NamespacedName{
			Name: r.GitopsOperatorNS,
		}
		r.unsetNamespace(gitopsOperatorNsKey, "openshift-gitops-operator")
	}
}

func (r *GitopsAddonReconciler) deleteOpenshiftGitopsInstance(configFlags *genericclioptions.ConfigFlags) {
	klog.Info("Start deleting openshift gitops instance...")

	if r.ArgoCDAgentEnabled == "true" {
		if err := r.deleteChart(configFlags, r.GitopsNS, "argocd-agent"); err != nil {
			klog.Errorf("Failed to delete argocd-agent: %v", err)
		}

		// Also delete the argocd-redis secret we created
		if err := r.deleteArgoCDRedisSecret(); err != nil {
			klog.Errorf("Failed to delete argocd-redis secret: %v", err)
		}
	}

	if err := r.deleteChart(configFlags, r.GitopsNS, "openshift-gitops-dependency"); err == nil {
		gitopsNsKey := types.NamespacedName{
			Name: r.GitopsNS,
		}
		r.unsetNamespace(gitopsNsKey, "openshift-gitops")
	}
}

// waitForArgoCDCR waits for the ArgoCD custom resource to be created by the openshift-gitops-operator
func (r *GitopsAddonReconciler) waitForArgoCDCR(timeout time.Duration) error {
	argoCDName := "openshift-gitops"
	argoCDKey := types.NamespacedName{
		Name:      argoCDName,
		Namespace: r.GitopsNS,
	}

	klog.Infof("Waiting for ArgoCD CR %s to be created in namespace %s...", argoCDName, r.GitopsNS)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		argoCD := &unstructured.Unstructured{}
		argoCD.SetAPIVersion("argoproj.io/v1beta1")
		argoCD.SetKind("ArgoCD")

		err := r.Get(ctx, argoCDKey, argoCD)
		if err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("ArgoCD CR %s not found yet, continuing to wait...", argoCDName)
				return false, nil // Continue waiting
			}
			klog.Errorf("Error checking for ArgoCD CR %s, continuing to wait..., err: %v", argoCDName, err)
			return false, nil // Continue waiting
		}

		klog.Infof("ArgoCD CR %s found successfully", argoCDName)
		return true, nil // ArgoCD CR found, stop waiting
	})
}

// install or update openshift gitops operator and its gitops instance
func (r *GitopsAddonReconciler) installOrUpdateOpenshiftGitops(configFlags *genericclioptions.ConfigFlags) {
	klog.Info("Start installing/updating openshift gitops operator and its instance...")

	// 1. install/update the openshift gitops operator manifest
	if !r.ShouldUpdateOpenshiftGiopsOperator() {
		klog.Info("Don't update openshift gitops operator")
	} else {
		// Install/upgrade the openshift-gitops-operator helm chart
		gitopsOperatorNsKey := types.NamespacedName{
			Name: r.GitopsOperatorNS,
		}

		// install dependency CRDs if it doesn't exsit
		err := r.applyCRDIfNotExists("gitopsservices", "pipelines.openshift.io/v1alpha1", "charts/dep-crds/gitopsservices.pipelines.openshift.io.crd.yaml")
		if err != nil {
			return
		}

		err = r.applyCRDIfNotExists("routes", "route.openshift.io/v1", "charts/dep-crds/routes.route.openshift.io.crd.yaml")
		if err != nil {
			return
		}

		err = r.applyCRDIfNotExists("clusterversions", "config.openshift.io/v1", "charts/dep-crds/clusterversions.config.openshift.io.crd.yaml")
		if err != nil {
			return
		}

		if err := r.CreateUpdateNamespace(gitopsOperatorNsKey); err == nil {
			err := r.installOrUpgradeChart(configFlags, "charts/openshift-gitops-operator", r.GitopsOperatorNS, "openshift-gitops-operator")
			if err != nil {
				klog.Errorf("Failed to process openshift-gitops-operator: %v", err)
			} else {
				r.postUpdate(gitopsOperatorNsKey, "openshift-gitops-operator")
			}
		}
	}

	// 2. Wait for the ArgoCD CR to be created by the openshift-gitops-operator
	timeout := 1 * time.Minute
	err := r.waitForArgoCDCR(timeout)
	if err != nil {
		klog.Errorf("Failed to find ArgoCD CR within %v: %v", timeout, err)
		return
	}

	// 3. Render and apply openshift-gitops-dependency helm chart manifests selectively
	if !r.ShouldUpdateOpenshiftGiops() {
		klog.Info("Don't update openshift gitops")
	} else {
		gitopsNsKey := types.NamespacedName{
			Name: r.GitopsNS,
		}

		if err := r.CreateUpdateNamespace(gitopsNsKey); err == nil {
			err := r.renderAndApplyDependencyManifests("charts/openshift-gitops-dependency", r.GitopsNS)
			if err != nil {
				klog.Errorf("Failed to process openshift-gitops-dependency manifests: %v", err)
			} else {
				r.postUpdate(gitopsNsKey, "openshift-gitops")
			}
		}
	}

	// 4. install/update the argocd-agent if enabled
	if r.ArgoCDAgentEnabled == "true" {
		if !r.ShouldUpdateArgoCDAgent() {
			klog.Info("Don't update argocd-agent")
		} else {
			klog.Info("Installing/updating argocd-agent...")

			// Install argocd-agent in the same namespace as openshift-gitops
			gitopsNsKey := types.NamespacedName{
				Name: r.GitopsNS,
			}

			if err := r.CreateUpdateNamespace(gitopsNsKey); err == nil {
				// Ensure argocd-redis secret exists before installing argocd-agent
				err := r.ensureArgoCDRedisSecret()
				if err != nil {
					klog.Errorf("Failed to ensure argocd-redis secret: %v", err)
					return
				}

				err = r.installOrUpgradeChart(configFlags, "charts/argocd-agent", r.GitopsNS, "argocd-agent")
				if err != nil {
					klog.Errorf("Failed to process argocd-agent: %v", err)
				} else {
					r.postUpdate(gitopsNsKey, "argocd-agent")
					klog.Info("Successfully installed/updated argocd-agent")
				}
			}
		}
	} else {
		klog.Info("ArgoCD Agent not enabled, skipping installation")
	}
}

func (r *GitopsAddonReconciler) deleteChart(configFlags *genericclioptions.ConfigFlags, namespace, releaseName string) error {
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(configFlags, namespace, "secret", log.Printf); err != nil {
		return fmt.Errorf("failed to init helm action config: %w", err)
	}

	helmDelete := action.NewUninstall(actionConfig)

	// Check if the release exists first
	listAction := action.NewList(actionConfig)
	releases, err := listAction.Run()

	if err != nil {
		klog.Infof("failed to list releases: %v", err)
		return nil
	}

	// Check if the release exists
	releaseExists := false

	for _, rel := range releases {
		if rel.Name == releaseName && rel.Namespace == namespace {
			releaseExists = true
			break
		}
	}

	if !releaseExists {
		klog.Infof("release %s not found in namespace %s", releaseName, namespace)
		return nil
	}

	// Perform the uninstall
	_, err = helmDelete.Run(releaseName)
	if err != nil {
		return fmt.Errorf("failed to uninstall release: %w", err)
	}

	fmt.Printf("Successfully deleted Helm chart %s from namespace %s\n", releaseName, namespace)

	return nil
}

// renderAndApplyDependencyManifests renders the openshift-gitops-dependency helm chart and applies manifests selectively
func (r *GitopsAddonReconciler) renderAndApplyDependencyManifests(chartPath, namespace string) error {
	klog.Info("Rendering openshift-gitops-dependency helm chart manifests...")

	// Create temp directory for chart files
	tempDir, err := os.MkdirTemp("", "helm-chart-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Copy embedded chart files to temp directory
	err = r.copyEmbeddedToTemp(ChartFS, chartPath, tempDir, "openshift-gitops-dependency")
	if err != nil {
		return fmt.Errorf("failed to copy files: %w", err)
	}

	// Load the chart
	chart, err := loader.Load(tempDir)
	if err != nil {
		return fmt.Errorf("failed to load chart: %w", err)
	}

	// Render the chart templates
	values := map[string]interface{}{}
	options := chartutil.ReleaseOptions{
		Name:      "openshift-gitops-dependency",
		Namespace: namespace,
	}

	valuesToRender, err := chartutil.ToRenderValues(chart, values, options, nil)
	if err != nil {
		return fmt.Errorf("failed to prepare chart values: %w", err)
	}

	files, err := engine.Engine{}.Render(chart, valuesToRender)
	if err != nil {
		return fmt.Errorf("failed to render chart templates: %w", err)
	}

	// Process each rendered manifest
	for name, content := range files {
		// Skip empty files and notes
		if len(strings.TrimSpace(content)) == 0 || strings.HasSuffix(name, "NOTES.txt") {
			continue
		}

		klog.Infof("Processing manifest: %s", name)

		// Parse YAML documents in the file
		yamlDocs := strings.Split(content, "\n---\n")
		for _, doc := range yamlDocs {
			doc = strings.TrimSpace(doc)
			if len(doc) == 0 {
				continue
			}

			// Parse the YAML into an unstructured object
			var obj unstructured.Unstructured
			if err := k8syaml.Unmarshal([]byte(doc), &obj); err != nil {
				klog.Warningf("Failed to parse YAML document in %s: %v", name, err)
				continue
			}

			// Skip if no kind or metadata
			if obj.GetKind() == "" || obj.GetName() == "" {
				continue
			}

			// Set the namespace if it's a namespaced resource and doesn't have one
			if obj.GetNamespace() == "" {
				obj.SetNamespace(namespace)
			}

			// Apply selective logic based on resource type
			if err := r.applyManifestSelectively(&obj); err != nil {
				klog.Errorf("Failed to apply manifest %s/%s %s: %v",
					obj.GetKind(), obj.GetName(), obj.GetNamespace(), err)
				return err
			}
		}
	}

	klog.Info("Successfully processed all openshift-gitops-dependency manifests")
	return nil
}

// applyManifestSelectively applies manifests based on the specific logic for each resource type
func (r *GitopsAddonReconciler) applyManifestSelectively(obj *unstructured.Unstructured) error {
	kind := obj.GetKind()
	name := obj.GetName()
	namespace := obj.GetNamespace()

	klog.Infof("Applying manifest selectively: %s/%s in namespace %s", kind, name, namespace)

	switch {
	case kind == "ArgoCD" && name == "openshift-gitops":
		return r.handleArgoCDManifest(obj)
	case kind == "ServiceAccount" && name == "default":
		return r.handleDefaultServiceAccount(obj)
	case kind == "AppProject" && name == "default":
		return r.handleDefaultAppProject(obj)
	case kind == "ServiceAccount" && name == "openshift-gitops-argocd-redis":
		return r.handleRedisServiceAccount(obj)
	case kind == "ServiceAccount" && name == "openshift-gitops-argocd-application-controller":
		return r.handleApplicationControllerServiceAccount(obj)
	case kind == "ClusterRoleBinding" && name == "openshift-gitops-argocd-application-controller":
		return r.handleApplicationControllerClusterRoleBinding(obj)
	default:
		klog.Infof("Skipping unknown manifest: %s/%s", kind, name)
		return nil
	}
}

// handleArgoCDManifest waits for the default ArgoCD CR and overrides its spec
func (r *GitopsAddonReconciler) handleArgoCDManifest(obj *unstructured.Unstructured) error {
	klog.Info("Handling ArgoCD manifest - waiting for default ArgoCD CR and overriding spec")

	argoCDKey := types.NamespacedName{
		Name:      "openshift-gitops",
		Namespace: obj.GetNamespace(),
	}

	// Wait up to 1 minute for the ArgoCD CR to exist
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var existingArgoCD *unstructured.Unstructured
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		argoCD := &unstructured.Unstructured{}
		argoCD.SetAPIVersion("argoproj.io/v1beta1")
		argoCD.SetKind("ArgoCD")

		err := r.Get(ctx, argoCDKey, argoCD)
		if err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("ArgoCD CR %s not found yet, continuing to wait...", argoCDKey.Name)
				return false, nil
			}
			return false, err
		}

		existingArgoCD = argoCD
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to wait for ArgoCD CR: %w", err)
	}

	// Override the spec with the rendered spec
	renderedSpec, found, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil {
		return fmt.Errorf("failed to get rendered spec: %w", err)
	}
	if !found {
		return fmt.Errorf("no spec found in rendered ArgoCD manifest")
	}

	// Update the existing ArgoCD CR with the new spec
	err = unstructured.SetNestedMap(existingArgoCD.Object, renderedSpec, "spec")
	if err != nil {
		return fmt.Errorf("failed to set spec: %w", err)
	}

	err = r.Update(context.TODO(), existingArgoCD)
	if err != nil {
		return fmt.Errorf("failed to update ArgoCD CR: %w", err)
	}

	klog.Info("Successfully updated ArgoCD CR spec")
	return nil
}

// handleDefaultServiceAccount waits for the default service account and appends imagePullSecrets
func (r *GitopsAddonReconciler) handleDefaultServiceAccount(obj *unstructured.Unstructured) error {
	klog.Info("Handling default ServiceAccount - waiting for existence and appending imagePullSecrets")

	saKey := types.NamespacedName{
		Name:      "default",
		Namespace: obj.GetNamespace(),
	}

	return r.waitAndAppendImagePullSecrets(saKey, obj)
}

// handleRedisServiceAccount waits for the redis service account and appends imagePullSecrets
func (r *GitopsAddonReconciler) handleRedisServiceAccount(obj *unstructured.Unstructured) error {
	klog.Info("Handling Redis ServiceAccount - waiting for ArgoCD deployment and appending imagePullSecrets")

	saKey := types.NamespacedName{
		Name:      "openshift-gitops-argocd-redis",
		Namespace: obj.GetNamespace(),
	}

	return r.waitAndAppendImagePullSecrets(saKey, obj)
}

// handleApplicationControllerServiceAccount waits for the application controller service account and appends imagePullSecrets
func (r *GitopsAddonReconciler) handleApplicationControllerServiceAccount(obj *unstructured.Unstructured) error {
	klog.Info("Handling Application Controller ServiceAccount - waiting for ArgoCD deployment and appending imagePullSecrets")

	saKey := types.NamespacedName{
		Name:      "openshift-gitops-argocd-application-controller",
		Namespace: obj.GetNamespace(),
	}

	return r.waitAndAppendImagePullSecrets(saKey, obj)
}

// handleDefaultAppProject applies the default AppProject directly
func (r *GitopsAddonReconciler) handleDefaultAppProject(obj *unstructured.Unstructured) error {
	klog.Info("Handling default AppProject - applying directly")

	return r.applyManifest(obj)
}

// handleApplicationControllerClusterRoleBinding applies the ClusterRoleBinding directly
func (r *GitopsAddonReconciler) handleApplicationControllerClusterRoleBinding(obj *unstructured.Unstructured) error {
	klog.Info("Handling Application Controller ClusterRoleBinding - applying directly")

	return r.applyManifest(obj)
}

// waitAndAppendImagePullSecrets waits for a service account to exist and appends imagePullSecrets
func (r *GitopsAddonReconciler) waitAndAppendImagePullSecrets(saKey types.NamespacedName, renderedObj *unstructured.Unstructured) error {
	// Wait for the service account to exist (up to 1 minute)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var existingSA *corev1.ServiceAccount
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		sa := &corev1.ServiceAccount{}
		err := r.Get(ctx, saKey, sa)
		if err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("ServiceAccount %s not found yet, continuing to wait...", saKey.Name)
				return false, nil
			}
			return false, err
		}

		existingSA = sa
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to wait for ServiceAccount %s: %w", saKey.Name, err)
	}

	// Get imagePullSecrets from the rendered manifest
	renderedImagePullSecrets, found, err := unstructured.NestedSlice(renderedObj.Object, "imagePullSecrets")
	if err != nil {
		return fmt.Errorf("failed to get rendered imagePullSecrets: %w", err)
	}
	if !found || len(renderedImagePullSecrets) == 0 {
		klog.Infof("No imagePullSecrets found in rendered ServiceAccount %s", saKey.Name)
		return nil
	}

	// Convert rendered imagePullSecrets to structured format and append them
	for _, renderedSecret := range renderedImagePullSecrets {
		secretMap, ok := renderedSecret.(map[string]interface{})
		if !ok {
			continue
		}
		secretName, ok := secretMap["name"].(string)
		if !ok {
			continue
		}

		// Check if this secret already exists in the service account
		secretExists := false
		for _, existingSecret := range existingSA.ImagePullSecrets {
			if existingSecret.Name == secretName {
				secretExists = true
				break
			}
		}

		// Append the secret if it doesn't already exist
		if !secretExists {
			existingSA.ImagePullSecrets = append(existingSA.ImagePullSecrets, corev1.LocalObjectReference{
				Name: secretName,
			})
		}
	}

	// Update the service account
	err = r.Update(context.TODO(), existingSA)
	if err != nil {
		return fmt.Errorf("failed to update ServiceAccount %s: %w", saKey.Name, err)
	}

	klog.Infof("Successfully updated ServiceAccount %s with imagePullSecrets", saKey.Name)
	return nil
}

// applyManifest applies a manifest directly
func (r *GitopsAddonReconciler) applyManifest(obj *unstructured.Unstructured) error {
	// Check if the resource already exists
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())

	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	err := r.Get(context.TODO(), key, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			// Resource doesn't exist, create it
			err = r.Create(context.TODO(), obj)
			if err != nil {
				return fmt.Errorf("failed to create resource %s/%s: %w", obj.GetKind(), obj.GetName(), err)
			}
			klog.Infof("Successfully created resource %s/%s", obj.GetKind(), obj.GetName())
			return nil
		}
		return fmt.Errorf("failed to get existing resource: %w", err)
	}

	// Resource exists, update it
	obj.SetResourceVersion(existing.GetResourceVersion())
	err = r.Update(context.TODO(), obj)
	if err != nil {
		return fmt.Errorf("failed to update resource %s/%s: %w", obj.GetKind(), obj.GetName(), err)
	}

	klog.Infof("Successfully updated resource %s/%s", obj.GetKind(), obj.GetName())
	return nil
}

func (r *GitopsAddonReconciler) installOrUpgradeChart(configFlags *genericclioptions.ConfigFlags, chartPath, namespace, releaseName string) error {
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(configFlags, namespace, "secret", log.Printf); err != nil {
		return fmt.Errorf("failed to init helm action config: %w", err)
	}

	// Create temp directory for chart files
	tempDir, err := os.MkdirTemp("", "helm-chart-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	klog.Infof("temp dir: %v", tempDir)

	// Copy embedded chart files to temp directory
	err = r.copyEmbeddedToTemp(ChartFS, chartPath, tempDir, releaseName)
	if err != nil {
		return fmt.Errorf("failed to copy files: %w", err)
	}

	// Load the chart
	chart, err := loader.Load(tempDir)
	if err != nil {
		return fmt.Errorf("failed to load chart: %w", err)
	}

	// Check if the release exists
	listAction := action.NewList(actionConfig)
	releases, err := listAction.Run()

	if err != nil {
		return fmt.Errorf("failed to list releases: %w", err)
	}

	// Check if the release exists
	releaseExists := false

	for _, rel := range releases {
		if rel.Name == releaseName && rel.Namespace == namespace {
			releaseExists = true
			break
		}
	}

	if releaseExists {
		// Release exists, do upgrade
		helmUpgrade := action.NewUpgrade(actionConfig)
		helmUpgrade.Namespace = namespace
		helmUpgrade.Force = true // Enable force option for upgrades

		_, err = helmUpgrade.Run(releaseName, chart, nil)

		if err != nil {
			return fmt.Errorf("failed to upgrade helm chart: %v/%v, err: %w", namespace, releaseName, err)
		}

		klog.Infof("Successfully upgraded helm chart: %v/%v", namespace, releaseName)
	} else {
		// delete stuck helm release secret if it exists
		r.deleteHelmReleaseSecret(namespace, releaseName)

		// install the helm chart
		helmInstall := action.NewInstall(actionConfig)
		helmInstall.Namespace = namespace
		helmInstall.ReleaseName = releaseName
		helmInstall.Replace = true
		helmInstall.Force = true // Enable force option for installs

		_, err = helmInstall.Run(chart, nil)

		if err != nil {
			return fmt.Errorf("failed to install helm chart: %v/%v, err: %w", namespace, releaseName, err)
		}

		klog.Infof("Successfully installed helm chart: %v/%v", namespace, releaseName)
	}

	return nil
}

func (r *GitopsAddonReconciler) deleteHelmReleaseSecret(namespace, releaseName string) {
	secretName := fmt.Sprintf("sh.helm.release.v1.%s.v1", releaseName)

	secret := &corev1.Secret{}

	err := r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: namespace}, secret)

	if err == nil && secret.Type == "helm.sh/release.v1" {
		err := r.Delete(context.TODO(), secret)
		if err != nil {
			klog.Infof("failed to delete Helm release secret: %v, err: %v", secret.String(), err)
		}
	}
}

func (r *GitopsAddonReconciler) copyEmbeddedToTemp(fs embed.FS, srcPath, destPath, releaseName string) error {
	entries, err := fs.ReadDir(srcPath)
	if err != nil {
		return err
	}

	// Create destination directory
	err = os.MkdirAll(destPath, 0750)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		sourcePath := srcPath + "/" + entry.Name()
		destFilePath := destPath + "/" + entry.Name()

		if entry.IsDir() {
			err = r.copyEmbeddedToTemp(fs, sourcePath, destFilePath, releaseName)
			if err != nil {
				return err
			}

			continue
		}

		if entry.Name() == "values.yaml" {
			// overrirde the value.yaml, save it to the temp dir
			if releaseName == "openshift-gitops-operator" {
				err = r.updateOperatorValueYaml(fs, sourcePath, destFilePath)
				if err != nil {
					return err
				}
			} else if releaseName == "openshift-gitops-dependency" {
				err = r.updateDependencyValueYaml(fs, sourcePath, destFilePath)
				if err != nil {
					return err
				}
			} else if releaseName == "argocd-agent" {
				err = r.updateArgoCDAgentValueYaml(fs, sourcePath, destFilePath)
				if err != nil {
					return err
				}
			}
		} else {
			// save other yaml files to the temp dir
			content, err := fs.ReadFile(sourcePath)
			if err != nil {
				return err
			}

			err = os.WriteFile(destFilePath, content, 0600)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *GitopsAddonReconciler) updateOperatorValueYaml(fs embed.FS, sourcePath, destFilePath string) error {
	file, err := fs.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Parse existing values into an unstructured map
	var values map[string]interface{}

	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&values); err != nil {
		return fmt.Errorf("failed to decode YAML: %w", err)
	}

	// Navigate to the nested keys and update the values
	global, ok := values["global"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to find 'global' key in YAML")
	}

	gitopsOperator, ok := global["openshift_gitops_operator"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to find 'openshift_gitops_operator' key in YAML")
	}

	image, tag, err := ParseImageReference(r.GitopsOperatorImage)
	if err != nil {
		return fmt.Errorf("failed to parse images: %w", err)
	}

	// Update the image and tag
	gitopsOperator["image"] = image
	gitopsOperator["tag"] = tag

	// override http proxy configuration
	proxyConfig, ok := global["proxyConfig"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("proxyConfig field not found or invalid type")
	}

	if r.HTTP_PROXY > "" {
		proxyConfig["HTTP_PROXY"] = r.HTTP_PROXY
	}

	if r.HTTPS_PROXY > "" {
		proxyConfig["HTTPS_PROXY"] = r.HTTPS_PROXY
	}

	if r.NO_PROXY > "" {
		proxyConfig["NO_PROXY"] = r.NO_PROXY
	}

	// Write the updated data back to the file
	outputFile, err := os.Create(filepath.Clean(destFilePath))
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %w", err)
	}
	defer outputFile.Close()

	encoder := yaml.NewEncoder(outputFile)
	encoder.SetIndent(2)

	if err := encoder.Encode(values); err != nil {
		return fmt.Errorf("failed to encode YAML: %w", err)
	}

	return nil
}

func (r *GitopsAddonReconciler) updateDependencyValueYaml(fs embed.FS, sourcePath, destFilePath string) error {
	file, err := fs.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Parse existing values into an unstructured map
	var values map[string]interface{}

	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&values); err != nil {
		return fmt.Errorf("failed to decode YAML: %w", err)
	}

	// Navigate to the nested keys and update the values
	global, ok := values["global"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to find 'global' key in YAML")
	}

	// override the gitops image and tag
	appController, ok := global["application_controller"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("application_controller field not found or invalid type")
	}

	gitOpsImage, gitOpsImagetag, err := ParseImageReference(r.GitopsImage)
	if err != nil {
		return fmt.Errorf("failed to parse images: %w", err)
	}

	appController["image"] = gitOpsImage
	appController["tag"] = gitOpsImagetag

	// override the redis image and tag
	redisServer, ok := global["redis"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("redis field not found or invalid type")
	}

	redisImage, reidsImagetag, err := ParseImageReference(r.RedisImage)
	if err != nil {
		return fmt.Errorf("failed to parse images: %w", err)
	}

	// Update the image and tag
	redisServer["image"] = redisImage
	redisServer["tag"] = reidsImagetag

	// override the reconcile_scope
	global["reconcile_scope"] = r.ReconcileScope

	// override http proxy configuration
	proxyConfig, ok := global["proxyConfig"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("proxyConfig field not found or invalid type")
	}

	if r.HTTP_PROXY > "" {
		proxyConfig["HTTP_PROXY"] = r.HTTP_PROXY
	}

	if r.HTTPS_PROXY > "" {
		proxyConfig["HTTPS_PROXY"] = r.HTTPS_PROXY
	}

	if r.NO_PROXY > "" {
		proxyConfig["NO_PROXY"] = r.NO_PROXY
	}

	// Write the updated data back to the file
	outputFile, err := os.Create(filepath.Clean(destFilePath))
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %w", err)
	}
	defer outputFile.Close()

	encoder := yaml.NewEncoder(outputFile)
	encoder.SetIndent(2)

	if err := encoder.Encode(values); err != nil {
		return fmt.Errorf("failed to encode YAML: %w", err)
	}

	return nil
}

func (r *GitopsAddonReconciler) updateArgoCDAgentValueYaml(fs embed.FS, sourcePath, destFilePath string) error {
	file, err := fs.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Parse existing values into an unstructured map
	var values map[string]interface{}

	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&values); err != nil {
		return fmt.Errorf("failed to decode YAML: %w", err)
	}

	// Navigate to the nested keys and update the values
	global, ok := values["global"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("failed to find 'global' key in YAML")
	}

	// override the argocd-agent image and tag
	argocdAgent, ok := global["argocdAgent"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("argocdAgent field not found or invalid type")
	}

	// Parse the argocd-agent image reference
	agentImage, agentTag, err := ParseImageReference(r.ArgoCDAgentImage)
	if err != nil {
		return fmt.Errorf("failed to parse ArgoCDAgentImage: %w", err)
	}

	argocdAgent["image"] = agentImage
	argocdAgent["tag"] = agentTag
	argocdAgent["serverAddress"] = r.ArgoCDAgentServerAddress
	argocdAgent["serverPort"] = r.ArgoCDAgentServerPort
	argocdAgent["mode"] = r.ArgoCDAgentMode

	// override http proxy configuration
	proxyConfig, ok := global["proxyConfig"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("proxyConfig field not found or invalid type")
	}

	if r.HTTP_PROXY > "" {
		proxyConfig["HTTP_PROXY"] = r.HTTP_PROXY
	}

	if r.HTTPS_PROXY > "" {
		proxyConfig["HTTPS_PROXY"] = r.HTTPS_PROXY
	}

	if r.NO_PROXY > "" {
		proxyConfig["NO_PROXY"] = r.NO_PROXY
	}

	// Write the updated data back to the file
	outputFile, err := os.Create(filepath.Clean(destFilePath))
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %w", err)
	}
	defer outputFile.Close()

	encoder := yaml.NewEncoder(outputFile)
	encoder.SetIndent(2)

	if err := encoder.Encode(values); err != nil {
		return fmt.Errorf("failed to encode YAML: %w", err)
	}

	return nil
}

func (r *GitopsAddonReconciler) ensureArgoCDRedisSecret() error {
	// Check if argocd-redis secret already exists
	argoCDRedisSecret := &corev1.Secret{}
	argoCDRedisSecretKey := types.NamespacedName{
		Name:      "argocd-redis",
		Namespace: r.GitopsNS,
	}

	err := r.Get(context.TODO(), argoCDRedisSecretKey, argoCDRedisSecret)
	if err == nil {
		klog.Info("argocd-redis secret already exists, skipping creation")
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check argocd-redis secret: %w", err)
	}

	klog.Info("argocd-redis secret not found, creating it...")

	// Find the secret ending with "redis-initial-password"
	secretList := &corev1.SecretList{}
	err = r.List(context.TODO(), secretList, client.InNamespace(r.GitopsNS))
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %s: %w", r.GitopsNS, err)
	}

	var initialPasswordSecret *corev1.Secret
	for i := range secretList.Items {
		if strings.HasSuffix(secretList.Items[i].Name, "redis-initial-password") {
			initialPasswordSecret = &secretList.Items[i]
			break
		}
	}

	if initialPasswordSecret == nil {
		return fmt.Errorf("no secret found ending with 'redis-initial-password' in namespace %s", r.GitopsNS)
	}

	// Extract the admin.password value
	adminPasswordBytes, exists := initialPasswordSecret.Data["admin.password"]
	if !exists {
		return fmt.Errorf("admin.password not found in secret %s", initialPasswordSecret.Name)
	}

	// Create the argocd-redis secret
	argoCDRedisSecretNew := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-redis",
			Namespace: r.GitopsNS,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopsaddon": "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"auth": adminPasswordBytes,
		},
	}

	err = r.Create(context.TODO(), argoCDRedisSecretNew)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			klog.Info("argocd-redis secret was created by another process, continuing...")
			return nil
		}

		return fmt.Errorf("failed to create argocd-redis secret: %w", err)
	}

	klog.Info("Successfully created argocd-redis secret")

	return nil
}

func (r *GitopsAddonReconciler) deleteArgoCDRedisSecret() error {
	argoCDRedisSecret := &corev1.Secret{}
	argoCDRedisSecretKey := types.NamespacedName{
		Name:      "argocd-redis",
		Namespace: r.GitopsNS,
	}

	err := r.Get(context.TODO(), argoCDRedisSecretKey, argoCDRedisSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Info("argocd-redis secret not found, nothing to delete")
			return nil
		}

		return fmt.Errorf("failed to get argocd-redis secret: %w", err)
	}

	// Only delete the secret if it was created by us (has our label)
	if label, exists := argoCDRedisSecret.Labels["apps.open-cluster-management.io/gitopsaddon"]; !exists || label != "true" {
		klog.Info("argocd-redis secret not managed by gitopsaddon, skipping deletion")
		return nil
	}

	err = r.Delete(context.TODO(), argoCDRedisSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Info("argocd-redis secret was already deleted")
			return nil
		}

		return fmt.Errorf("failed to delete argocd-redis secret: %w", err)
	}

	klog.Info("Successfully deleted argocd-redis secret")

	return nil
}

func ParseImageReference(imageRef string) (string, string, error) {
	// First try to parse as image@digest format
	if strings.Contains(imageRef, "@") {
		parts := strings.SplitN(imageRef, "@", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid image reference format with @: %s", imageRef)
		}

		image := parts[0]
		digest := parts[1]

		// Validate image and digest are not empty
		if image == "" {
			return "", "", fmt.Errorf("image part is empty")
		}

		if digest == "" {
			return "", "", fmt.Errorf("tag/digest part is empty")
		}

		// If the digest doesn't start with "sha256:", add it
		if !strings.HasPrefix(digest, "sha256:") {
			digest = "sha256:" + digest
		}

		return image, digest, nil
	}

	// Try to parse as image:tag format
	if strings.Contains(imageRef, ":") {
		// Find the last colon to handle registry URLs with ports (e.g., localhost:5000/image:tag)
		lastColonIndex := strings.LastIndex(imageRef, ":")
		if lastColonIndex == -1 {
			return "", "", fmt.Errorf("invalid image reference format: %s", imageRef)
		}

		image := imageRef[:lastColonIndex]
		tag := imageRef[lastColonIndex+1:]

		// Validate image and tag are not empty
		if image == "" {
			return "", "", fmt.Errorf("image part is empty")
		}

		if tag == "" {
			return "", "", fmt.Errorf("tag/digest part is empty")
		}

		// For tag format, we return the tag as-is (it will be used as a tag, not a digest)
		return image, tag, nil
	}

	// If neither @ nor : is found, it's an invalid format
	return "", "", fmt.Errorf("invalid image reference format, expected image:tag or image@digest: %s", imageRef)
}

func (r *GitopsAddonReconciler) ShouldUpdateOpenshiftGiopsOperator() bool {
	nameSpaceKey := types.NamespacedName{
		Name: r.GitopsOperatorNS,
	}
	namespace := &corev1.Namespace{}
	err := r.Get(context.TODO(), nameSpaceKey, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("no gitops operator namespace found")
			return true
		} else {
			klog.Errorf("Failed to get the %v namespace, err: %v", r.GitopsOperatorNS, err)
			return false
		}
	}

	// the OpenShift GitOps operator is installed by operator hub, gitops addon should not update it
	if r.isGitOpsOperatorInstalledByOLM() {
		klog.Infof("The OpenShift GitOps operator is already installed on the namespace %v by OLM", r.GitopsOperatorNS)
		return false
	}

	// the OpenShift GitOps operator is installed by gitops addon
	if _, ok := namespace.Labels["apps.open-cluster-management.io/gitopsaddon"]; ok {
		if namespace.Annotations["apps.open-cluster-management.io/gitops-operator-image"] == r.GitopsOperatorImage &&
			namespace.Annotations["apps.open-cluster-management.io/gitops-operator-ns"] == r.GitopsOperatorNS {
			klog.Infof("No new gitops operator manifest found on the namespace %v", r.GitopsOperatorNS)
			return false
		}
	}

	return true
}

// isGitOpsOperatorInstalledByOLM checks if the OpenShift GitOps operator is installed by OLM
func (r *GitopsAddonReconciler) isGitOpsOperatorInstalledByOLM() bool {
	deploymentList := &appsv1.DeploymentList{}
	err := r.List(context.TODO(), deploymentList, &client.ListOptions{
		Namespace: r.GitopsOperatorNS,
	})

	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("No deployments found in namespace %v", r.GitopsOperatorNS)
			return false
		}
		klog.Errorf("Failed to list deployments in namespace %v, err: %v", r.GitopsOperatorNS, err)
		return false
	}

	for _, deployment := range deploymentList.Items {
		// Check if the deployment has the label control-plane=gitops-operator
		if controlPlane, ok := deployment.Labels["control-plane"]; ok && controlPlane == "gitops-operator" {
			// Check if the deployment has the annotation olm.operatorGroup
			if _, hasOLMAnnotation := deployment.Annotations["olm.operatorGroup"]; hasOLMAnnotation {
				klog.Infof("Found deployment %v with control-plane=gitops-operator label and olm.operatorGroup annotation, indicating OLM installation", deployment.Name)
				return true
			}
		}
	}

	return false
}

func (r *GitopsAddonReconciler) ShouldUpdateOpenshiftGiops() bool {
	nameSpaceKey := types.NamespacedName{
		Name: r.GitopsNS,
	}
	namespace := &corev1.Namespace{}
	err := r.Get(context.TODO(), nameSpaceKey, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("no gitops namespace found")
			return true
		} else {
			klog.Errorf("Failed to get the %v namespace, err: %v", r.GitopsNS, err)
			return false
		}
	} else {
		if namespace.Annotations["apps.open-cluster-management.io/gitops-image"] != r.GitopsImage ||
			namespace.Annotations["apps.open-cluster-management.io/gitops-ns"] != r.GitopsNS ||
			namespace.Annotations["apps.open-cluster-management.io/redis-image"] != r.RedisImage ||
			namespace.Annotations["apps.open-cluster-management.io/reconcile-scope"] != r.ReconcileScope {
			klog.Infof("new gitops manifest found")
			return true
		}
	}

	return false
}

func (r *GitopsAddonReconciler) ShouldUpdateArgoCDAgent() bool {
	nameSpaceKey := types.NamespacedName{
		Name: r.GitopsNS,
	}
	namespace := &corev1.Namespace{}
	err := r.Get(context.TODO(), nameSpaceKey, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("no gitops namespace found for argocd-agent")
			return true
		} else {
			klog.Errorf("Failed to get the %v namespace for argocd-agent, err: %v", r.GitopsNS, err)
			return false
		}
	} else {
		if _, ok := namespace.Labels["apps.open-cluster-management.io/gitopsaddon"]; !ok {
			klog.Errorf("The %v namespace is not owned by gitops addon", r.GitopsNS)
			return false
		}

		if namespace.Annotations["apps.open-cluster-management.io/argocd-agent-image"] != r.ArgoCDAgentImage ||
			namespace.Annotations["apps.open-cluster-management.io/argocd-agent-server-address"] != r.ArgoCDAgentServerAddress ||
			namespace.Annotations["apps.open-cluster-management.io/argocd-agent-server-port"] != r.ArgoCDAgentServerPort ||
			namespace.Annotations["apps.open-cluster-management.io/argocd-agent-mode"] != r.ArgoCDAgentMode {
			klog.Infof("new argocd-agent configuration found")
			return true
		}
	}

	return false
}

func (r *GitopsAddonReconciler) CreateUpdateNamespace(nameSpaceKey types.NamespacedName) error {
	namespace := &corev1.Namespace{}
	err := r.Get(context.TODO(), nameSpaceKey, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nameSpaceKey.Name,
					Labels: map[string]string{
						"addon.open-cluster-management.io/namespace":  "true", //enable copying the image pull secret to the NS
						"apps.open-cluster-management.io/gitopsaddon": "true",
					},
				},
			}

			if err := r.Create(context.TODO(), namespace); err != nil {
				klog.Errorf("Failed to create the openshift-gitops-operator namespace, err: %v", err)
				return err
			}
		} else {
			klog.Errorf("Failed to get the openshift-gitops-operator namespace, err: %v", err)
			return err
		}
	}

	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
	}
	namespace.Labels["addon.open-cluster-management.io/namespace"] = "true"
	namespace.Labels["apps.open-cluster-management.io/gitopsaddon"] = "true"

	if err := r.Update(context.TODO(), namespace); err != nil {
		klog.Errorf("Failed to update the labels to the openshift-gitops-operator namespace, err: %v", err)
		return err
	}

	// If on the hcp hosted cluster, there is no klusterlet operator
	// As a result, the open-cluster-management-image-pull-credentials secret is not automatically synced up
	// to the new namespace even though the addon.open-cluster-management.io/namespace: true label is specified
	// To support all kinds of clusters, we proactively copy the original git addon
	// open-cluster-management-image-pull-credentials secret to the new namespace
	err = r.copyImagePullSecret(nameSpaceKey)
	if err != nil {
		return err
	}

	// Wait 1 min for the image pull secret `open-cluster-management-image-pull-credentials`
	// to be generated
	secretName := "open-cluster-management-image-pull-credentials"
	secret := &corev1.Secret{}
	timeout := time.Minute
	interval := time.Second * 2
	start := time.Now()

	for time.Since(start) < timeout {
		err = r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: nameSpaceKey.Name}, secret)
		if err == nil {
			// Secret found
			klog.Infof("Secret %s found in namespace %s", secretName, nameSpaceKey.Name)
			return nil
		}

		if !errors.IsNotFound(err) {
			klog.Errorf("Error while waiting for secret %s in namespace %s: %v", secretName, nameSpaceKey.Name, err)
			return err
		}

		klog.Infof("the image pull credentials secret is NOT found, wait for the next check, err: %v", err)

		time.Sleep(interval)
	}

	// Timeout reached
	klog.Errorf("Timeout waiting for secret %s in namespace %s", secretName, nameSpaceKey.Name)

	return fmt.Errorf("timeout waiting for secret %s in namespace %s", secretName, nameSpaceKey.Name)
}

func (r *GitopsAddonReconciler) copyImagePullSecret(nameSpaceKey types.NamespacedName) error {
	secretName := "open-cluster-management-image-pull-credentials"
	secret := &corev1.Secret{}
	gitopsAddonNs := utils.GetComponentNamespace("open-cluster-management-agent-addon")

	// Get the original gitops addon image pull secret
	err := r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: gitopsAddonNs}, secret)
	if err != nil {
		klog.Errorf("gitops addon image pull secret no found. secret: %v/%v, err: %v", gitopsAddonNs, secretName, err)
		return err
	}

	// Prepare the new Secret
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        secretName,
			Namespace:   nameSpaceKey.Name,
			Labels:      secret.Labels,
			Annotations: secret.Annotations,
		},
		Data:       secret.Data,
		StringData: secret.StringData,
		Type:       secret.Type,
	}

	newSecret.ObjectMeta.UID = ""
	newSecret.ObjectMeta.ResourceVersion = ""
	newSecret.ObjectMeta.CreationTimestamp = metav1.Time{}
	newSecret.ObjectMeta.DeletionTimestamp = nil
	newSecret.ObjectMeta.DeletionGracePeriodSeconds = nil
	newSecret.ObjectMeta.OwnerReferences = nil
	newSecret.ObjectMeta.ManagedFields = nil

	// Create the new Secret in the target namespace
	err = r.Create(context.TODO(), newSecret)
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create target secret %s/%s: %w", nameSpaceKey.Name, secretName, err)
		}
	}

	fmt.Printf("Successfully copied secret %s/%s to %s/%s\n", gitopsAddonNs, secretName, nameSpaceKey.Name, secretName)

	return nil
}

func (r *GitopsAddonReconciler) postUpdate(nameSpaceKey types.NamespacedName, nsType string) {
	//1. save Latest Gitops metat To Namespace annotations
	namespace := &corev1.Namespace{}
	err := r.Get(context.TODO(), nameSpaceKey, namespace)

	if err != nil {
		klog.Errorf("Failed to get the namespace: %v, err: %v", nameSpaceKey.Name, err)
		return
	}

	namespacePatch := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        namespace.Name,
			Annotations: map[string]string{},
		},
	}

	if nsType == "openshift-gitops-operator" {
		namespacePatch.Annotations["apps.open-cluster-management.io/gitops-operator-image"] = r.GitopsOperatorImage
		namespacePatch.Annotations["apps.open-cluster-management.io/gitops-operator-ns"] = r.GitopsOperatorNS
	} else if nsType == "argocd-agent" {
		namespacePatch.Annotations["apps.open-cluster-management.io/argocd-agent-image"] = r.ArgoCDAgentImage
		namespacePatch.Annotations["apps.open-cluster-management.io/argocd-agent-server-address"] = r.ArgoCDAgentServerAddress
		namespacePatch.Annotations["apps.open-cluster-management.io/argocd-agent-server-port"] = r.ArgoCDAgentServerPort
		namespacePatch.Annotations["apps.open-cluster-management.io/argocd-agent-mode"] = r.ArgoCDAgentMode
	} else {
		namespacePatch.Annotations["apps.open-cluster-management.io/gitops-image"] = r.GitopsImage
		namespacePatch.Annotations["apps.open-cluster-management.io/gitops-ns"] = r.GitopsNS
		namespacePatch.Annotations["apps.open-cluster-management.io/redis-image"] = r.RedisImage
		namespacePatch.Annotations["apps.open-cluster-management.io/reconcile-scope"] = r.ReconcileScope
	}

	// Use server-side apply with force to handle concurrent modifications
	namespacePatch.ObjectMeta.ManagedFields = nil

	if err := r.Patch(context.TODO(), namespacePatch, client.Apply,
		client.FieldOwner("gitops-addon-reconciler"), client.ForceOwnership); err != nil {
		klog.Errorf("Failed to save the lastest gitops meta data to namespace: %v, err: %v", nameSpaceKey.Name, err)
		return
	}

	klog.Infof("Successfully saved the lastest gitops meta data to namespace: %v", nameSpaceKey.Name)
}

func (r *GitopsAddonReconciler) unsetNamespace(nameSpaceKey types.NamespacedName, nsType string) {
	namespace := &corev1.Namespace{}
	err := r.Get(context.TODO(), nameSpaceKey, namespace)

	if err != nil {
		klog.Errorf("Failed to get the namespace: %v, err: %v", nameSpaceKey.Name, err)
		return
	}

	namespacePatch := namespace.DeepCopy()
	namespacePatch.Kind = "Namespace"
	namespacePatch.APIVersion = "v1"

	if nsType == "openshift-gitops-operator" {
		if namespacePatch.Annotations != nil {
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/gitops-operator-image")
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/gitops-operator-ns")
		}
	} else if nsType == "argocd-agent" {
		if namespacePatch.Annotations != nil {
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/argocd-agent-image")
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/argocd-agent-server-address")
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/argocd-agent-server-port")
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/argocd-agent-mode")
		}
	} else {
		if namespacePatch.Annotations != nil {
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/gitops-image")
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/gitops-ns")
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/redis-image")
			delete(namespacePatch.Annotations, "apps.open-cluster-management.io/reconcile-scope")
		}
	}

	// Use server-side apply with force to handle concurrent modifications
	namespacePatch.ObjectMeta.ManagedFields = nil

	if err := r.Patch(context.TODO(), namespacePatch, client.Apply,
		client.FieldOwner("gitops-addon-reconciler"), client.ForceOwnership); err != nil {
		klog.Errorf("Failed to unset annotations from namespace: %v, err: %v", nameSpaceKey.Name, err)
		return
	}
}

// applyCRDIfNotExists checks if a CRD exists, and if not, apply it to the local cluster
// crdName is the name of the CRD (e.g., "gitopsservices.pipelines.openshift.io").
// yamlFilePath is the path to the CRD YAML file.
func (r *GitopsAddonReconciler) applyCRDIfNotExists(resource, apiVersion, yamlFilePath string) error {
	// Check if API resource exists
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(r.Config)
	if err != nil {
		return err
	}

	apiResourceList, err := discoveryClient.ServerPreferredResources()
	if err != nil {
		return err
	}

	// Check if the api resource exists
	for _, apiResource := range apiResourceList {
		if apiResource.GroupVersion == apiVersion {
			for _, r := range apiResource.APIResources {
				if r.Name == resource {
					klog.Infof("API resource %s.%s exists", resource, apiVersion)
					return nil
				}
			}
		}
	}

	klog.Infof("API resource %s.%s not found, installing from %s", resource, apiVersion, yamlFilePath)

	yamlFile, err := ChartFS.ReadFile(yamlFilePath)
	if err != nil {
		klog.Errorf("failed to read crd file %s, err: %v", yamlFilePath, err)
		return err
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}

	// the sigs.k8s.io/yaml has to be used in order to unmarhal the CRD yaml correctly.
	// metav1.ObjectMeta uses embedded json struct tags but not yaml struct tags.
	// The yaml.v3 package does not automatically recognize json struct tags
	err = k8syaml.Unmarshal(yamlFile, crd)

	if err != nil {
		klog.Errorf("failed to unmarshal CRD %s, err: %v", yamlFilePath, err)
		return err
	}

	klog.Infof("crd: %v", crd.ObjectMeta.Name)

	err = r.Create(context.TODO(), crd)
	if err != nil {
		klog.Errorf("failed to create CRD %s, err: %v", yamlFilePath, err)
		return err
	}

	klog.Infof("Successfully installed CRD %s", yamlFilePath)

	return nil
}
