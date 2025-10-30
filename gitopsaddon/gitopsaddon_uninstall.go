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
	"os"
	"strings"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	k8syaml "sigs.k8s.io/yaml"
)

// performUninstallOperations performs uninstall of all resources created by the controller
func (r *GitopsAddonReconciler) performUninstallOperations() {
	klog.Info("Starting aggressive uninstall - fire and forget...")

	// 1. Delete argocd-agent resources (if enabled) - FASTEST
	if r.ArgoCDAgentEnabled == "true" {
		r.uninstallArgoCDAgent()
	}

	// 2. Delete argocd-redis secret
	r.uninstallArgoCDRedisSecret()

	// 3. Delete ArgoCD CR explicitly
	r.uninstallArgoCDCR()

	// 4. Delete openshift-gitops-dependency resources
	r.uninstallGitOpsDependency()

	// 5. Delete openshift-gitops-operator resources
	r.uninstallGitOpsOperator()

	// 6. Delete CRDs we applied
	r.uninstallCRDs()

	// Keep namespaces

	klog.Info("Uninstall requests sent - exiting")
}

// performAgentUninstallOperations performs uninstall of only ArgoCD agent resources
func (r *GitopsAddonReconciler) performAgentUninstallOperations() {
	klog.Info("Starting ArgoCD agent uninstall (agent-only)...")

	// Only delete argocd-agent resources (if enabled)
	if r.ArgoCDAgentEnabled == "true" {
		r.uninstallArgoCDAgent()
	} else {
		klog.Info("ArgoCD agent is not enabled, nothing to uninstall")
	}

	klog.Info("ArgoCD agent uninstall requests sent - exiting")
}

// uninstallArgoCDAgent deletes all resources created by argocd-agent chart
func (r *GitopsAddonReconciler) uninstallArgoCDAgent() {
	klog.Info("Uninstall: Deleting argocd-agent resources...")
	r.deleteChartResources("charts/argocd-agent", r.GitopsNS, "argocd-agent")
}

// uninstallArgoCDRedisSecret deletes the argocd-redis secret if it has our label
func (r *GitopsAddonReconciler) uninstallArgoCDRedisSecret() {
	klog.Info("Uninstall: Deleting argocd-redis secret...")

	argoCDRedisSecret := &corev1.Secret{}
	argoCDRedisSecretKey := types.NamespacedName{
		Name:      "argocd-redis",
		Namespace: r.GitopsNS,
	}

	err := r.Get(context.TODO(), argoCDRedisSecretKey, argoCDRedisSecret)
	if err != nil {
		if !errors.IsNotFound(err) {
			klog.Errorf("Failed to get argocd-redis secret: %v", err)
		}
		return
	}

	// Only delete the secret if it was created by us (has our label)
	if label, exists := argoCDRedisSecret.Labels["apps.open-cluster-management.io/gitopsaddon"]; !exists || label != "true" {
		klog.Info("argocd-redis secret not managed by gitopsaddon, skipping deletion")
		return
	}

	err = r.Delete(context.TODO(), argoCDRedisSecret)
	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("Failed to delete argocd-redis secret: %v", err)
	} else {
		klog.Info("Successfully deleted argocd-redis secret")
	}
}

// uninstallArgoCDCR deletes the ArgoCD CR "openshift-gitops"
func (r *GitopsAddonReconciler) uninstallArgoCDCR() {
	klog.Info("Uninstall: Deleting ArgoCD CR...")

	argoCD := &unstructured.Unstructured{}
	argoCD.SetAPIVersion("argoproj.io/v1beta1")
	argoCD.SetKind("ArgoCD")

	argoCDKey := types.NamespacedName{
		Name:      "openshift-gitops",
		Namespace: r.GitopsNS,
	}

	err := r.Get(context.TODO(), argoCDKey, argoCD)
	if err != nil {
		if !errors.IsNotFound(err) {
			klog.Errorf("Failed to get ArgoCD CR: %v", err)
		}
		return
	}

	err = r.Delete(context.TODO(), argoCD)
	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("Failed to delete ArgoCD CR: %v", err)
	} else {
		klog.Info("Successfully deleted ArgoCD CR")
	}
}

// uninstallGitOpsDependency deletes all resources created by gitops dependency manifests
func (r *GitopsAddonReconciler) uninstallGitOpsDependency() {
	klog.Info("Uninstall: Deleting gitops dependency resources...")
	r.deleteChartResources("charts/openshift-gitops-dependency", r.GitopsNS, "openshift-gitops-dependency")
}

// uninstallGitOpsOperator deletes all resources created by gitops operator chart
func (r *GitopsAddonReconciler) uninstallGitOpsOperator() {
	klog.Info("Uninstall: Deleting gitops operator resources...")
	r.deleteChartResources("charts/openshift-gitops-operator", r.GitopsOperatorNS, "openshift-gitops-operator")
}

// uninstallCRDs deletes the CRDs we applied
func (r *GitopsAddonReconciler) uninstallCRDs() {
	klog.Info("Uninstall: Deleting CRDs...")
	r.deleteCRD("gitopsservices.pipelines.openshift.io")
	r.deleteCRD("routes.route.openshift.io")
	r.deleteCRD("clusterversions.config.openshift.io")
}

// deleteChartResources templates a chart and deletes all rendered manifests
func (r *GitopsAddonReconciler) deleteChartResources(chartPath, namespace, releaseName string) {
	klog.Infof("Deleting chart resources: %s in namespace %s", releaseName, namespace)

	// Create temp directory for chart files
	tempDir, err := os.MkdirTemp("", "helm-chart-uninstall-*")
	if err != nil {
		klog.Errorf("Failed to create temp dir for uninstall: %v", err)
		return
	}
	defer os.RemoveAll(tempDir)

	// Copy embedded chart files to temp directory
	err = r.copyEmbeddedToTemp(ChartFS, chartPath, tempDir, releaseName)
	if err != nil {
		klog.Errorf("Failed to copy files for uninstall: %v", err)
		return
	}

	// Load the chart
	chart, err := loader.Load(tempDir)
	if err != nil {
		klog.Errorf("Failed to load chart for uninstall: %v", err)
		return
	}

	// Template the chart (same as apply process) - populate values based on chart path
	values := map[string]interface{}{}

	// Populate values based on chart path (same as in utils)
	switch chartPath {
	case "charts/openshift-gitops-operator":
		// Parse the operator image
		image, tag, err := ParseImageReference(r.GitopsOperatorImage)
		if err != nil {
			klog.Errorf("Failed to parse GitopsOperatorImage for uninstall: %v", err)
			return
		}

		// Parse the gitops service image
		gitOpsServiceImage, gitOpsServiceTag, err := ParseImageReference(r.GitOpsServiceImage)
		if err != nil {
			klog.Errorf("Failed to parse GitOpsServiceImage for uninstall: %v", err)
			return
		}

		// Parse the gitops console plugin image
		gitOpsConsolePluginImage, gitOpsConsolePluginTag, err := ParseImageReference(r.GitOpsConsolePluginImage)
		if err != nil {
			klog.Errorf("Failed to parse GitOpsConsolePluginImage for uninstall: %v", err)
			return
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

	case "charts/openshift-gitops-dependency":
		// Parse the gitops and redis images
		gitopsImage, gitopsTag, err := ParseImageReference(r.GitopsImage)
		if err != nil {
			klog.Errorf("Failed to parse GitopsImage for uninstall: %v", err)
			return
		}

		redisImage, redisTag, err := ParseImageReference(r.RedisImage)
		if err != nil {
			klog.Errorf("Failed to parse RedisImage for uninstall: %v", err)
			return
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

	case "charts/argocd-agent":
		// Parse the agent image
		agentImage, agentTag, err := ParseImageReference(r.ArgoCDAgentImage)
		if err != nil {
			klog.Errorf("Failed to parse ArgoCDAgentImage for uninstall: %v", err)
			return
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

	options := chartutil.ReleaseOptions{
		Name:      releaseName,
		Namespace: namespace,
	}

	valuesToRender, err := chartutil.ToRenderValues(chart, values, options, nil)
	if err != nil {
		klog.Errorf("Failed to prepare chart values for uninstall: %v", err)
		return
	}

	files, err := engine.Engine{}.Render(chart, valuesToRender)
	if err != nil {
		klog.Errorf("Failed to render chart templates for uninstall: %v", err)
		return
	}

	// Delete each rendered manifest (fire and forget)
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
				klog.Warningf("Failed to parse YAML document in %s for uninstall: %v", name, err)
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

			// Delete the manifest (fire and forget)
			r.deleteManifest(&obj)
		}
	}

	klog.Infof("Deletion requests sent for chart %s in namespace %s", releaseName, namespace)
}

// deleteManifest deletes a manifest (fire and forget - no waiting)
func (r *GitopsAddonReconciler) deleteManifest(obj *unstructured.Unstructured) {
	err := r.Delete(context.TODO(), obj)
	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("Failed to delete manifest %s/%s %s: %v (continuing anyway)",
			obj.GetKind(), obj.GetName(), obj.GetNamespace(), err)
	} else {
		klog.Infof("Delete request sent for %s/%s %s",
			obj.GetKind(), obj.GetName(), obj.GetNamespace())
	}
}

// deleteCRD deletes a CRD by name (fire and forget)
func (r *GitopsAddonReconciler) deleteCRD(crdName string) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	crd.SetName(crdName)

	err := r.Delete(context.TODO(), crd)
	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("Failed to delete CRD %s: %v (continuing anyway)", crdName, err)
	} else {
		klog.Infof("Delete request sent for CRD %s", crdName)
	}
}
