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
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/klog"
	k8syaml "sigs.k8s.io/yaml"
)

// templateAndApplyChart templates a Helm chart and applies the rendered manifests
func (r *GitopsAddonReconciler) templateAndApplyChart(chartPath, namespace, releaseName string) error {
	klog.Infof("Templating and applying chart %s in namespace %s", releaseName, namespace)

	// Create temp directory for chart files
	tempDir, err := os.MkdirTemp("", "helm-chart-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Copy embedded chart files to temp directory
	err = r.copyEmbeddedToTemp(ChartFS, chartPath, tempDir, releaseName)
	if err != nil {
		return fmt.Errorf("failed to copy files: %v", err)
	}

	// Load the chart
	chart, err := loader.Load(tempDir)
	if err != nil {
		return fmt.Errorf("failed to load chart: %v", err)
	}

	// Prepare values for templating
	values := map[string]interface{}{}

	// Populate values based on chart path
	switch chartPath {
	case "charts/openshift-gitops-operator":
		// Parse the operator image
		image, tag, err := ParseImageReference(r.GitopsOperatorImage)
		if err != nil {
			return fmt.Errorf("failed to parse GitopsOperatorImage: %w", err)
		}

		// Parse the gitops service image
		gitOpsServiceImage, gitOpsServiceTag, err := ParseImageReference(r.GitOpsServiceImage)
		if err != nil {
			return fmt.Errorf("failed to parse GitOpsServiceImage: %w", err)
		}

		// Parse the gitops console plugin image
		gitOpsConsolePluginImage, gitOpsConsolePluginTag, err := ParseImageReference(r.GitOpsConsolePluginImage)
		if err != nil {
			return fmt.Errorf("failed to parse GitOpsConsolePluginImage: %w", err)
		}

		// Set up global values for openshift-gitops-operator chart
		global := map[string]interface{}{
			"openshift_gitops_operator": map[string]interface{}{
				"image": image,
				"tag":   tag,
			},
			"gitops_service": map[string]interface{}{
				"image": gitOpsServiceImage,
				"tag":   gitOpsServiceTag,
			},
			"gitops_console_plugin": map[string]interface{}{
				"image": gitOpsConsolePluginImage,
				"tag":   gitOpsConsolePluginTag,
			},
			"proxyConfig": map[string]interface{}{
				"HTTP_PROXY":  r.HTTP_PROXY,
				"HTTPS_PROXY": r.HTTPS_PROXY,
				"NO_PROXY":    r.NO_PROXY,
			},
		}
		values["global"] = global
	case "charts/argocd-agent":
		// Parse the agent image
		agentImage, agentTag, err := ParseImageReference(r.ArgoCDAgentImage)
		if err != nil {
			return fmt.Errorf("failed to parse ArgoCDAgentImage: %w", err)
		}

		// Set up argocdAgent values
		argocdAgent := map[string]interface{}{
			"image": agentImage,
			"tag":   agentTag,
		}

		// Set server connection details
		if r.ArgoCDAgentServerAddress != "" {
			argocdAgent["serverAddress"] = r.ArgoCDAgentServerAddress
		}
		if r.ArgoCDAgentServerPort != "" {
			argocdAgent["serverPort"] = r.ArgoCDAgentServerPort
		}
		if r.ArgoCDAgentMode != "" {
			argocdAgent["mode"] = r.ArgoCDAgentMode
		}

		// Set up global values for argocd-agent chart
		global := map[string]interface{}{
			"argocdAgent": argocdAgent,
			"proxyConfig": map[string]interface{}{
				"HTTP_PROXY":  r.HTTP_PROXY,
				"HTTPS_PROXY": r.HTTPS_PROXY,
				"NO_PROXY":    r.NO_PROXY,
			},
		}
		values["global"] = global
	}

	// Set up release options
	options := chartutil.ReleaseOptions{
		Name:      releaseName,
		Namespace: namespace,
	}

	// Prepare values to render
	valuesToRender, err := chartutil.ToRenderValues(chart, values, options, nil)
	if err != nil {
		return fmt.Errorf("failed to prepare chart values: %v", err)
	}

	// Render the chart templates
	files, err := engine.Engine{}.Render(chart, valuesToRender)
	if err != nil {
		return fmt.Errorf("failed to render chart templates: %v", err)
	}

	// Apply each rendered manifest
	for name, content := range files {
		// Skip empty files and notes
		if len(strings.TrimSpace(content)) == 0 || strings.HasSuffix(name, "NOTES.txt") {
			continue
		}

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

			// Apply the manifest
			if err := r.applyManifest(&obj); err != nil {
				klog.Errorf("Failed to apply manifest %s/%s %s: %v",
					obj.GetKind(), obj.GetName(), obj.GetNamespace(), err)
				// Continue with other manifests even if one fails
			}
		}
	}

	klog.Infof("Successfully templated and applied chart %s in namespace %s", releaseName, namespace)
	return nil
}

// copyEmbeddedToTemp copies embedded chart files to a temporary directory
func (r *GitopsAddonReconciler) copyEmbeddedToTemp(fs embed.FS, srcPath, destPath, releaseName string) error {
	entries, err := fs.ReadDir(srcPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcEntryPath := filepath.Join(srcPath, entry.Name())
		destEntryPath := filepath.Join(destPath, entry.Name())

		if entry.IsDir() {
			// Create directory and recurse
			if err := os.MkdirAll(destEntryPath, 0750); err != nil {
				return err
			}
			if err := r.copyEmbeddedToTemp(fs, srcEntryPath, destEntryPath, releaseName); err != nil {
				return err
			}
		} else {
			// Read embedded file
			data, err := fs.ReadFile(srcEntryPath)
			if err != nil {
				return err
			}

			err = os.WriteFile(destEntryPath, data, 0600)

			if err != nil {
				return err
			}
		}
	}

	return nil
}

// renderAndApplyDependencyManifests renders and applies dependency YAML manifests
func (r *GitopsAddonReconciler) renderAndApplyDependencyManifests(chartPath, namespace string) error {
	klog.Info("Rendering openshift-gitops-dependency helm chart manifests...")

	tempDir, err := os.MkdirTemp("", "gitops-dependency-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Copy chart files to temp directory
	if err := r.copyEmbeddedToTemp(ChartFS, chartPath, tempDir, "openshift-gitops-dependency"); err != nil {
		return fmt.Errorf("failed to copy chart files: %v", err)
	}

	// Load and render the chart
	chart, err := loader.Load(tempDir)
	if err != nil {
		return fmt.Errorf("failed to load chart: %v", err)
	}

	// Prepare values for templating
	values := map[string]interface{}{}

	// Parse the gitops and redis images
	gitopsImage, gitopsTag, err := ParseImageReference(r.GitopsImage)
	if err != nil {
		return fmt.Errorf("failed to parse GitopsImage: %w", err)
	}

	redisImage, redisTag, err := ParseImageReference(r.RedisImage)
	if err != nil {
		return fmt.Errorf("failed to parse RedisImage: %w", err)
	}

	// Set up global values for openshift-gitops-dependency chart
	global := map[string]interface{}{
		"application_controller": map[string]interface{}{
			"image": gitopsImage,
			"tag":   gitopsTag,
		},
		"redis": map[string]interface{}{
			"image": redisImage,
			"tag":   redisTag,
		},
		"reconcile_scope": r.ReconcileScope,
		"proxyConfig": map[string]interface{}{
			"HTTP_PROXY":  r.HTTP_PROXY,
			"HTTPS_PROXY": r.HTTPS_PROXY,
			"NO_PROXY":    r.NO_PROXY,
		},
	}
	values["global"] = global

	options := chartutil.ReleaseOptions{
		Name:      "openshift-gitops-dependency",
		Namespace: namespace,
	}

	valuesToRender, err := chartutil.ToRenderValues(chart, values, options, nil)
	if err != nil {
		return fmt.Errorf("failed to prepare chart values: %v", err)
	}

	files, err := engine.Engine{}.Render(chart, valuesToRender)
	if err != nil {
		return fmt.Errorf("failed to render chart templates: %v", err)
	}

	// Apply rendered manifests selectively
	for name, content := range files {
		if len(strings.TrimSpace(content)) == 0 || strings.HasSuffix(name, "NOTES.txt") {
			continue
		}

		yamlDocs := strings.Split(content, "\n---\n")
		for _, doc := range yamlDocs {
			doc = strings.TrimSpace(doc)
			if len(doc) == 0 {
				continue
			}

			var obj unstructured.Unstructured
			if err := k8syaml.Unmarshal([]byte(doc), &obj); err != nil {
				klog.Warningf("Failed to parse YAML document in %s: %v", name, err)
				continue
			}

			if obj.GetKind() == "" || obj.GetName() == "" {
				continue
			}

			if obj.GetNamespace() == "" {
				obj.SetNamespace(namespace)
			}

			if err := r.applyManifestSelectively(&obj); err != nil {
				klog.Errorf("Failed to apply manifest %s/%s: %v", obj.GetKind(), obj.GetName(), err)
			}
		}
	}

	klog.Info("Successfully rendered and applied openshift-gitops-dependency manifests")
	return nil
}

// applyManifestSelectively applies manifests with special handling for certain resource types
func (r *GitopsAddonReconciler) applyManifestSelectively(obj *unstructured.Unstructured) error {
	kind := obj.GetKind()
	name := obj.GetName()

	klog.Infof("Applying %s/%s selectively", kind, name)

	// Check if existing resource has skip annotation (for all resource types)
	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion(obj.GetAPIVersion())
	existing.SetKind(obj.GetKind())
	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	err := r.Get(context.TODO(), key, existing)
	if err == nil {
		annotations := existing.GetAnnotations()
		if annotations != nil && annotations["gitops-addon.open-cluster-management.io/skip"] == "true" {
			klog.Infof("Skipping %s/%s %s due to skip annotation", kind, name, obj.GetNamespace())
			return nil
		}
	}

	// Add management label to indicate this resource is managed by gitops-addon
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["app.kubernetes.io/managed-by"] = "gitops-addon"
	obj.SetLabels(labels)

	switch kind {
	case "ArgoCD":
		if name == "openshift-gitops" {
			return r.handleArgoCDManifest(obj)
		}
	case "ServiceAccount":
		switch name {
		case "default":
			return r.handleDefaultServiceAccount(obj)
		case "openshift-gitops-argocd-redis":
			return r.handleRedisServiceAccount(obj)
		case "openshift-gitops-argocd-application-controller":
			return r.handleApplicationControllerServiceAccount(obj)
		}
	case "AppProject":
		if name == "default" {
			return r.handleDefaultAppProject(obj)
		}
	case "ClusterRoleBinding":
		if name == "openshift-gitops-argocd-application-controller" {
			return r.handleApplicationControllerClusterRoleBinding(obj)
		}
	}

	// For all other resources, apply directly
	return r.applyManifest(obj)
}

// applyManifest applies a Kubernetes manifest
func (r *GitopsAddonReconciler) applyManifest(obj *unstructured.Unstructured) error {
	// Check if the resource already exists
	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion(obj.GetAPIVersion())
	existing.SetKind(obj.GetKind())

	key := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	err := r.Get(context.TODO(), key, existing)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	// Check if existing resource has skip annotation
	if err == nil {
		annotations := existing.GetAnnotations()
		if annotations != nil && annotations["gitops-addon.open-cluster-management.io/skip"] == "true" {
			klog.Infof("Skipping %s/%s %s due to skip annotation", obj.GetKind(), obj.GetName(), obj.GetNamespace())
			return nil
		}
	}

	// Add management label to indicate this resource is managed by gitops-addon
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["app.kubernetes.io/managed-by"] = "gitops-addon"
	obj.SetLabels(labels)

	if err != nil && errors.IsNotFound(err) {
		// Resource doesn't exist, create it
		klog.Infof("Creating %s/%s %s", obj.GetKind(), obj.GetName(), obj.GetNamespace())
		return r.Create(context.TODO(), obj)
	}

	// Resource exists, update it
	obj.SetResourceVersion(existing.GetResourceVersion())
	klog.Infof("Updating %s/%s %s", obj.GetKind(), obj.GetName(), obj.GetNamespace())
	return r.Update(context.TODO(), obj)
}

// applyCRDIfNotExists applies a CRD only if it doesn't already exist
func (r *GitopsAddonReconciler) applyCRDIfNotExists(resource, apiVersion, yamlFilePath string) error {
	// Check if API resource exists
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(r.Config)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %v", err)
	}

	apiResourceList, err := discoveryClient.ServerResourcesForGroupVersion(apiVersion)
	if err == nil {
		// Check if the resource exists in the API resource list
		for _, apiResource := range apiResourceList.APIResources {
			if apiResource.Name == resource {
				klog.Infof("CRD %s already exists, skipping installation", yamlFilePath)
				return nil
			}
		}
	}

	// CRD doesn't exist, install it
	klog.Infof("Installing CRD %s", yamlFilePath)

	crdData, err := ChartFS.ReadFile(yamlFilePath)
	if err != nil {
		return fmt.Errorf("failed to read CRD file %s: %v", yamlFilePath, err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := k8syaml.Unmarshal(crdData, &crd); err != nil {
		return fmt.Errorf("failed to unmarshal CRD %s: %v", yamlFilePath, err)
	}

	// Check if CRD should be skipped due to annotation
	annotations := crd.GetAnnotations()
	if annotations != nil && annotations["gitops-addon.open-cluster-management.io/skip"] == "true" {
		klog.Infof("Skipping CRD %s due to skip annotation", yamlFilePath)
		return nil
	}

	// Add management label to indicate this CRD is managed by gitops-addon
	labels := crd.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["app.kubernetes.io/managed-by"] = "gitops-addon"
	crd.SetLabels(labels)

	err = r.Create(context.TODO(), &crd)
	if err != nil && !errors.IsAlreadyExists(err) {
		klog.Errorf("failed to create CRD %s, err: %v", yamlFilePath, err)
		return err
	}

	klog.Infof("Successfully installed CRD %s", yamlFilePath)
	return nil
}

// ParseImageReference parses an image reference into repository and tag
func ParseImageReference(imageRef string) (string, string, error) {
	// First try to parse as image@digest format
	if strings.Contains(imageRef, "@") {
		parts := strings.Split(imageRef, "@")
		if len(parts) == 2 {
			return parts[0], parts[1], nil
		}
	}

	// Try to parse as image:tag format
	if strings.Contains(imageRef, ":") {
		// Find the last colon to handle registry URLs with ports
		lastColonIndex := strings.LastIndex(imageRef, ":")
		if lastColonIndex != -1 {
			repository := imageRef[:lastColonIndex]
			tag := imageRef[lastColonIndex+1:]

			// Check if this might be a port number instead of a tag
			// Port numbers are typically numeric and shorter
			if !strings.Contains(tag, "/") && len(tag) < 10 {
				// This looks like it could be a tag
				return repository, tag, nil
			}
		}
	}

	// If no tag or digest found, assume "latest"
	return imageRef, "latest", nil
}

// parseImageComponents is a helper function that parses image components
func parseImageComponents(imageRef string) (string, string) {
	repository, tag, _ := ParseImageReference(imageRef)
	return repository, tag
}
