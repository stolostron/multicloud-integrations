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
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	k8syaml "sigs.k8s.io/yaml"
)

// templateAndApplyChart templates a Helm chart and applies the rendered manifests.
func (r *GitopsAddonReconciler) templateAndApplyChart(chartPath, namespace, releaseName string) error {
	klog.Infof("Templating and applying chart %s in namespace %s", releaseName, namespace)

	tempDir, err := os.MkdirTemp("", "helm-chart-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	if err := r.copyEmbeddedToTemp(ChartFS, chartPath, tempDir); err != nil {
		return fmt.Errorf("failed to copy files: %v", err)
	}

	chart, err := loader.Load(tempDir)
	if err != nil {
		return fmt.Errorf("failed to load chart: %v", err)
	}

	// Apply the chart's crds/ directory: Helm does not render it.
	for _, crd := range chart.CRDObjects() {
		r.applyCRDYAML(crd.Name, crd.File.Data)
	}

	options := chartutil.ReleaseOptions{
		Name:      releaseName,
		Namespace: namespace,
	}

	valuesToRender, err := chartutil.ToRenderValues(chart, r.chartValues(chartPath), options, nil)
	if err != nil {
		return fmt.Errorf("failed to prepare chart values: %v", err)
	}

	files, err := engine.Engine{}.Render(chart, valuesToRender)
	if err != nil {
		return fmt.Errorf("failed to render chart templates: %v", err)
	}

	r.applyRenderedManifests(files, namespace)

	klog.Infof("Successfully templated and applied chart %s in namespace %s", releaseName, namespace)

	return nil
}

// chartValues builds the Helm values for the embedded operator chart.
func (r *GitopsAddonReconciler) chartValues(chartPath string) map[string]interface{} {
	values := map[string]interface{}{}
	if chartPath != "charts/openshift-gitops-operator" {
		return values
	}

	operatorImage := r.AddonConfig.OperatorImages[utils.EnvGitOpsOperatorImage]

	if operatorImage != "" {
		repository, tag, parseErr := ParseImageReference(operatorImage)
		if parseErr != nil {
			// No explicit tag: use the whole reference as the repository.
			klog.Warningf("Operator image %q has no explicit tag, defaulting to latest: %v", operatorImage, parseErr)
			repository = operatorImage
			tag = "latest"
		}
		registry := ""
		repo := repository
		if idx := strings.Index(repository, "/"); idx >= 0 {
			registry = repository[:idx]
			repo = repository[idx+1:]
		}
		values["image"] = map[string]interface{}{
			"manager": map[string]interface{}{
				"registry":   registry,
				"repository": repo,
				"tag":        tag,
				"pullPolicy": "IfNotPresent",
			},
		}
	}

	operatorImages := map[string]interface{}{}
	for key, value := range r.AddonConfig.OperatorImages {
		if key == utils.EnvGitOpsOperatorImage || value == "" {
			continue
		}
		operatorImages[key] = value
	}
	// The chart renamed the extension image env vars; honour the legacy name.
	if extensionImage, ok := operatorImages[utils.EnvArgoCDExtensionImage]; ok {
		operatorImages["ARGOCD_ROLLOUTS_IMAGE"] = extensionImage
		operatorImages["ROLLOUTS_EXTENSION_IMAGE"] = extensionImage
	}
	values["operatorImageEnv"] = operatorImages

	envValues := map[string]interface{}{
		"ARGOCD_CLUSTER_CONFIG_NAMESPACES":        "*",
		"CLUSTER_SCOPED_ARGO_ROLLOUTS_NAMESPACES": "openshift-gitops",
		"DISABLE_DEFAULT_ARGOCD_INSTANCE":         "true",
		"ENABLE_CONVERSION_WEBHOOK":               "false",
		"OPERATOR_NAME":                           "gitops-operator",
	}
	if r.AddonConfig.HTTPProxy != "" {
		envValues[utils.EnvHTTPProxy] = r.AddonConfig.HTTPProxy
	}
	if r.AddonConfig.HTTPSProxy != "" {
		envValues[utils.EnvHTTPSProxy] = r.AddonConfig.HTTPSProxy
	}
	if r.AddonConfig.NoProxy != "" {
		envValues[utils.EnvNoProxy] = r.AddonConfig.NoProxy
	}
	values["env"] = envValues

	values["args"] = []interface{}{
		"--health-probe-bind-address=:8081",
		"--metrics-bind-address=127.0.0.1:8080",
		"--leader-elect",
	}
	values["livenessProbe"] = map[string]interface{}{
		"httpGet":             map[string]interface{}{"path": "/healthz", "port": 8081},
		"initialDelaySeconds": 15,
		"periodSeconds":       20,
	}
	values["readinessProbe"] = map[string]interface{}{
		"httpGet":             map[string]interface{}{"path": "/readyz", "port": 8081},
		"initialDelaySeconds": 5,
		"periodSeconds":       10,
	}
	values["securityContext"] = map[string]interface{}{
		"allowPrivilegeEscalation": false,
		"capabilities":             map[string]interface{}{"drop": []interface{}{"ALL"}},
		"readOnlyRootFilesystem":   true,
		"runAsNonRoot":             true,
		"seccompProfile":           map[string]interface{}{"type": "RuntimeDefault"},
	}
	values["podSecurityContext"] = map[string]interface{}{"runAsNonRoot": true}

	// The chart's service-account-token secret hardcodes this name.
	values["serviceAccount"] = map[string]interface{}{
		"create": true,
		"name":   "openshift-gitops-operator-controller-manager",
	}
	// The chart creates the Secret the operator propagates into ArgoCD
	// ServiceAccounts on non-OCP clusters.
	pullSecretValues := map[string]interface{}{"create": false}
	if pullSecretData, getErr := r.getAddonImagePullSecretData(); getErr != nil {
		klog.V(2).Infof("Image pull credentials not available for chart propagation yet: %v", getErr)
	} else if len(pullSecretData) > 0 {
		pullSecretValues = map[string]interface{}{
			"create":           true,
			"name":             "redhat-registry-pull-secret",
			"dockerConfigJson": string(pullSecretData),
			"labels": map[string]interface{}{
				"operator.argoproj.io/propagate-image-pull-secret": "true",
			},
		}
	}
	values["pullSecret"] = pullSecretValues
	values["imagePullSecrets"] = []interface{}{
		map[string]interface{}{"name": addonImagePullSecretName},
	}
	// No Prometheus operator on non-OCP clusters.
	values["monitoring"] = map[string]interface{}{"enabled": false}
	values["installMode"] = "AllNamespaces"

	return values
}

// applyRenderedManifests applies every rendered manifest in a stable order, so a binding is
// never applied before the role it references.
func (r *GitopsAddonReconciler) applyRenderedManifests(files map[string]string, namespace string) {
	fileNames := make([]string, 0, len(files))
	for name := range files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)

	for _, name := range fileNames {
		content := files[name]
		if len(strings.TrimSpace(content)) == 0 || strings.HasSuffix(name, "NOTES.txt") {
			continue
		}

		for _, doc := range strings.Split(content, "\n---\n") {
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

			labels := obj.GetLabels()
			if labels == nil {
				labels = make(map[string]string)
			}
			labels[GitOpsAddonLabelKey] = GitOpsAddonLabelValue
			obj.SetLabels(labels)

			if err := r.applyManifest(&obj); err != nil {
				klog.Errorf("Failed to apply manifest %s/%s %s: %v",
					obj.GetKind(), obj.GetName(), obj.GetNamespace(), err)
			}
		}
	}
}

// crdHashAnnotation stores the hash of the CRD content last applied.
const crdHashAnnotation = "gitops-addon.open-cluster-management.io/crd-hash"

// serviceAccountTokenSecretType is the Kubernetes Secret type populated by the token controller.
const serviceAccountTokenSecretType = "kubernetes.io/service-account-token" // #nosec G101 -- Secret type

const addonImagePullSecretName = "open-cluster-management-image-pull-credentials"

// getAddonImagePullSecretData returns the pull credentials for the addon namespace.
func (r *GitopsAddonReconciler) getAddonImagePullSecretData() ([]byte, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{
		Name:      addonImagePullSecretName,
		Namespace: utils.GetComponentNamespace("open-cluster-management-agent-addon"),
	}
	if err := r.Get(context.TODO(), key, secret); err != nil {
		return nil, err
	}

	return secret.Data[corev1.DockerConfigJsonKey], nil
}

// applyCRDYAML applies a CRD file, skipping unchanged content.
func (r *GitopsAddonReconciler) applyCRDYAML(name string, data []byte) {
	content := strings.TrimSpace(string(data))
	if content == "" {
		return
	}

	for _, doc := range strings.Split(content, "\n---\n") {
		doc = strings.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}

		var obj unstructured.Unstructured
		if err := k8syaml.Unmarshal([]byte(doc), &obj); err != nil {
			klog.Warningf("Failed to parse CRD document in %s: %v", name, err)
			continue
		}
		if obj.GetKind() == "" || obj.GetName() == "" {
			continue
		}

		contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(doc)))

		existing := &unstructured.Unstructured{}
		existing.SetAPIVersion(obj.GetAPIVersion())
		existing.SetKind(obj.GetKind())
		if getErr := r.Get(context.TODO(), types.NamespacedName{Name: obj.GetName()}, existing); getErr == nil {
			if existing.GetAnnotations()[crdHashAnnotation] == contentHash && crdShapeMatches(existing, &obj) {
				continue
			}
		}

		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[crdHashAnnotation] = contentHash
		obj.SetAnnotations(annotations)

		if err := r.applyManifest(&obj); err != nil {
			klog.Errorf("Failed to apply CRD %s: %v", obj.GetName(), err)
		}
	}
}

// crdShapeMatches compares the identity-relevant parts of a CRD with the desired one. The
// content hash alone only proves which content was last written by this addon; if someone else
// changed the group, kind names, scope or served versions, the CRD is re-applied.
func crdShapeMatches(live, desired *unstructured.Unstructured) bool {
	stringFields := [][]string{
		{"spec", "group"},
		{"spec", "names", "plural"},
		{"spec", "names", "kind"},
		{"spec", "scope"},
	}
	for _, field := range stringFields {
		liveValue, _, _ := unstructured.NestedString(live.Object, field...)
		desiredValue, _, _ := unstructured.NestedString(desired.Object, field...)
		if liveValue != desiredValue {
			return false
		}
	}

	liveVersions, _, _ := unstructured.NestedSlice(live.Object, "spec", "versions")
	desiredVersions, _, _ := unstructured.NestedSlice(desired.Object, "spec", "versions")
	if len(liveVersions) != len(desiredVersions) {
		return false
	}
	liveNames := map[string]bool{}
	for _, version := range liveVersions {
		if entry, ok := version.(map[string]interface{}); ok {
			if name, ok := entry["name"].(string); ok {
				liveNames[name] = true
			}
		}
	}
	for _, version := range desiredVersions {
		entry, ok := version.(map[string]interface{})
		if !ok {
			return false
		}
		name, ok := entry["name"].(string)
		if !ok || !liveNames[name] {
			return false
		}
	}

	return true
}

// copyEmbeddedToTemp copies embedded chart files to a temporary directory
func (r *GitopsAddonReconciler) copyEmbeddedToTemp(fs embed.FS, srcPath, destPath string) error {
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
			if err := r.copyEmbeddedToTemp(fs, srcEntryPath, destEntryPath); err != nil {
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

// applyManifestSelectively applies manifests with special handling for certain resource types
func (r *GitopsAddonReconciler) applyManifestSelectively(obj *unstructured.Unstructured) error {
	// Check if the addon is paused before applying anything
	if IsPaused(context.TODO(), r.Client) {
		klog.Infof("GitOps addon is paused, skipping selective apply of %s/%s", obj.GetKind(), obj.GetName())
		return nil
	}

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
	labels[GitOpsAddonLabelKey] = GitOpsAddonLabelValue
	obj.SetLabels(labels)

	// Apply the manifest directly
	return r.applyManifest(obj)
}

// applyManifest applies a Kubernetes manifest
func (r *GitopsAddonReconciler) applyManifest(obj *unstructured.Unstructured) error {
	// Check if the addon is paused before applying anything
	if IsPaused(context.TODO(), r.Client) {
		klog.Infof("GitOps addon is paused, skipping apply of %s/%s", obj.GetKind(), obj.GetName())
		return nil
	}

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

	if err == nil {
		// Resource exists — check if it's ours or pre-existing
		annotations := existing.GetAnnotations()
		if annotations != nil && annotations["gitops-addon.open-cluster-management.io/skip"] == "true" {
			klog.Infof("Skipping %s/%s %s due to skip annotation", obj.GetKind(), obj.GetName(), obj.GetNamespace())
			return nil
		}

		existingLabels := existing.GetLabels()
		if existingLabels == nil || existingLabels[GitOpsAddonLabelKey] != GitOpsAddonLabelValue {
			klog.Infof("Skipping %s/%s %s: pre-existing resource without gitopsaddon label", obj.GetKind(), obj.GetName(), obj.GetNamespace())
			return nil
		}

		// Leave the populated token Secret alone.
		if obj.GetKind() == "Secret" {
			if secretType, _, _ := unstructured.NestedString(obj.Object, "type"); secretType == serviceAccountTokenSecretType {
				klog.V(2).Infof("Skipping existing service-account-token Secret %s/%s",
					obj.GetNamespace(), obj.GetName())
				return nil
			}
		}
	}

	// Add management label to indicate this resource is managed by gitops-addon
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[GitOpsAddonLabelKey] = GitOpsAddonLabelValue
	obj.SetLabels(labels)

	if err != nil && errors.IsNotFound(err) {
		// Resource doesn't exist, create it
		klog.Infof("Creating %s/%s %s", obj.GetKind(), obj.GetName(), obj.GetNamespace())
		return r.Create(context.TODO(), obj)
	}

	// Resource exists and is ours (has gitopsaddon label), update it
	obj.SetResourceVersion(existing.GetResourceVersion())
	klog.Infof("Updating %s/%s %s", obj.GetKind(), obj.GetName(), obj.GetNamespace())
	return r.Update(context.TODO(), obj)
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

			// Check if this is a tag or a port number
			// If there's a "/" after the colon, it's a port (e.g., registry:5000/image)
			// If there's no "/", it's a tag (e.g., image:v1.0.0)
			if !strings.Contains(tag, "/") {
				// This is a tag
				return repository, tag, nil
			}
		}
	}

	// If no tag or digest found, return error
	return "", "", fmt.Errorf("image reference %q has no tag or digest specified", imageRef)
}

// parseImageComponents is a helper function that parses image components
func parseImageComponents(imageRef string) (string, string) {
	repository, tag, _ := ParseImageReference(imageRef)
	return repository, tag
}
