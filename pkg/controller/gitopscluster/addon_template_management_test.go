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

package gitopscluster

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetControllerImage(t *testing.T) {
	tests := []struct {
		name        string
		envValue    string
		expectValue string
		expectError bool
	}{
		{
			name:        "uses environment variable when set",
			envValue:    "test-image:v1.0.0",
			expectValue: "test-image:v1.0.0",
			expectError: false,
		},
		{
			name:        "errors when env var not set",
			envValue:    "",
			expectValue: "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment
			if tt.envValue != "" {
				os.Setenv(ControllerImageEnvVar, tt.envValue)
				defer os.Unsetenv(ControllerImageEnvVar)
			} else {
				os.Unsetenv(ControllerImageEnvVar)
			}

			scheme := runtime.NewScheme()
			_ = addonv1alpha1.AddToScheme(scheme)
			_ = gitopsclusterV1beta1.AddToScheme(scheme)

			r := &ReconcileGitOpsCluster{}
			image, err := r.getControllerImage()

			if tt.expectError {
				if err == nil {
					t.Errorf("getControllerImage() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("getControllerImage() error = %v", err)
				return
			}

			if image != tt.expectValue {
				t.Errorf("getControllerImage() = %v, want %v", image, tt.expectValue)
			}
		})
	}
}

func TestGetAddOnTemplateName(t *testing.T) {
	gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
	}

	expected := "gitops-addon-test-namespace-test-cluster"
	result := getAddOnTemplateName(gitOpsCluster)

	if result != expected {
		t.Errorf("getAddOnTemplateName() = %v, want %v", result, expected)
	}
}

func TestEnsureAddOnTemplate(t *testing.T) {
	// Set controller image for this test
	os.Setenv(ControllerImageEnvVar, "test-controller:v1")
	defer os.Unsetenv(ControllerImageEnvVar)

	scheme := runtime.NewScheme()
	_ = addonv1alpha1.AddToScheme(scheme)
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				GitOpsOperatorImage: "test-operator:v1",
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					ServerAddress: "test-server:443",
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileGitOpsCluster{
		Client: fakeClient,
		scheme: scheme,
	}

	// Test creating AddOnTemplate
	err := r.EnsureAddOnTemplate(gitOpsCluster)
	if err != nil {
		t.Errorf("EnsureAddOnTemplate() error = %v", err)
		return
	}

	// Verify AddOnTemplate was created
	templateName := getAddOnTemplateName(gitOpsCluster)
	template := &addonv1alpha1.AddOnTemplate{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: templateName}, template)
	if err != nil {
		t.Errorf("Failed to get created AddOnTemplate: %v", err)
		return
	}

	if template.Spec.AddonName != "gitops-addon" {
		t.Errorf("AddOnTemplate AddonName = %v, want %v", template.Spec.AddonName, "gitops-addon")
	}

	// Test updating existing AddOnTemplate
	err = r.EnsureAddOnTemplate(gitOpsCluster)
	if err != nil {
		t.Errorf("EnsureAddOnTemplate() update error = %v", err)
	}
}

func TestEnsureAddOnTemplateErrorWithoutEnv(t *testing.T) {
	// Ensure env var is not set
	os.Unsetenv(ControllerImageEnvVar)

	scheme := runtime.NewScheme()
	_ = addonv1alpha1.AddToScheme(scheme)
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileGitOpsCluster{
		Client: fakeClient,
		scheme: scheme,
	}

	// Test should error when env var not set
	err := r.EnsureAddOnTemplate(gitOpsCluster)
	if err == nil {
		t.Error("EnsureAddOnTemplate() expected error when CONTROLLER_IMAGE not set, but got none")
	}
}

func TestBuildAddonManifests(t *testing.T) {
	namespace := "test-namespace"
	addonImage := "addon:v1"
	// Note: operatorImage and agentImage are now configured via AddOnDeploymentConfig template variables

	manifests := buildAddonManifests(namespace, addonImage)

	if len(manifests) == 0 {
		t.Error("buildAddonManifests() returned empty manifests")
	}

	// 7 manifests: pre-delete ClusterRole, pre-delete ClusterRoleBinding, Job,
	// ServiceAccount, ClusterRole, ClusterRoleBinding, Deployment
	expectedCount := 7
	if len(manifests) != expectedCount {
		t.Errorf("buildAddonManifests() manifest count = %v, want %v", len(manifests), expectedCount)
	}

	allowedKinds := map[string]bool{
		"Job": true, "ServiceAccount": true, "ClusterRole": true,
		"ClusterRoleBinding": true, "Deployment": true,
	}
	kindCounts := map[string]int{}

	for i, manifest := range manifests {
		if len(manifest.Raw) == 0 {
			t.Errorf("Manifest %d has empty Raw field, expected populated Raw bytes", i)
		}

		var objMap map[string]interface{}
		if err := json.Unmarshal(manifest.Raw, &objMap); err != nil {
			t.Errorf("Manifest %d failed to unmarshal: %v", i, err)
			continue
		}

		if _, hasStatus := objMap["status"]; hasStatus {
			t.Errorf("Manifest %d contains unwanted 'status' field", i)
		}

		kind, hasKind := objMap["kind"].(string)
		if !hasKind {
			t.Errorf("Manifest %d missing 'kind' field", i)
			continue
		}
		if _, hasApiVersion := objMap["apiVersion"]; !hasApiVersion {
			t.Errorf("Manifest %d missing 'apiVersion' field", i)
		}

		if !allowedKinds[kind] {
			t.Errorf("Manifest %d has unexpected kind %q", i, kind)
		}
		kindCounts[kind]++
	}

	// 2 ClusterRoles (main + cleanup), 2 ClusterRoleBindings (main + cleanup)
	if kindCounts["ClusterRole"] != 2 {
		t.Errorf("expected 2 ClusterRole manifests, got %d", kindCounts["ClusterRole"])
	}
	if kindCounts["ClusterRoleBinding"] != 2 {
		t.Errorf("expected 2 ClusterRoleBinding manifests, got %d", kindCounts["ClusterRoleBinding"])
	}
	for _, required := range []string{"Job", "ServiceAccount", "Deployment"} {
		if kindCounts[required] != 1 {
			t.Errorf("expected 1 %s manifest, got %d", required, kindCounts[required])
		}
	}
}

func TestBuildCleanupJobManifestHasManualSelector(t *testing.T) {
	manifest := buildCleanupJobManifest("addon:v1", "test-ns")

	var objMap map[string]interface{}
	if err := json.Unmarshal(manifest.Raw, &objMap); err != nil {
		t.Fatalf("Failed to unmarshal Job manifest: %v", err)
	}

	spec, ok := objMap["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("Job manifest missing spec")
	}

	if ms, ok := spec["manualSelector"].(bool); !ok || !ms {
		t.Errorf("Job spec.manualSelector = %v, want true", spec["manualSelector"])
	}

	selector, ok := spec["selector"].(map[string]interface{})
	if !ok {
		t.Fatal("Job manifest missing spec.selector")
	}
	matchLabels, ok := selector["matchLabels"].(map[string]interface{})
	if !ok {
		t.Fatal("Job manifest missing spec.selector.matchLabels")
	}
	if matchLabels["job-name"] != "gitops-addon-cleanup" {
		t.Errorf("selector.matchLabels[job-name] = %q, want %q", matchLabels["job-name"], "gitops-addon-cleanup")
	}

	template, ok := spec["template"].(map[string]interface{})
	if !ok {
		t.Fatal("Job manifest missing spec.template")
	}
	tmplMeta, ok := template["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("Job manifest missing spec.template.metadata")
	}
	tmplLabels, ok := tmplMeta["labels"].(map[string]interface{})
	if !ok {
		t.Fatal("Job manifest missing spec.template.metadata.labels")
	}
	if tmplLabels["job-name"] != "gitops-addon-cleanup" {
		t.Errorf("template.metadata.labels[job-name] = %q, want %q", tmplLabels["job-name"], "gitops-addon-cleanup")
	}
}

func TestBuildCleanupClusterRoleManifest(t *testing.T) {
	manifest := buildCleanupClusterRoleManifest()

	var objMap map[string]interface{}
	if err := json.Unmarshal(manifest.Raw, &objMap); err != nil {
		t.Fatalf("Failed to unmarshal ClusterRole manifest: %v", err)
	}

	if objMap["kind"] != "ClusterRole" {
		t.Errorf("kind = %q, want ClusterRole", objMap["kind"])
	}

	metadata := objMap["metadata"].(map[string]interface{})
	if metadata["name"] != "gitops-addon-cleanup" {
		t.Errorf("name = %q, want gitops-addon-cleanup", metadata["name"])
	}

	annotations := metadata["annotations"].(map[string]interface{})
	if _, ok := annotations["addon.open-cluster-management.io/addon-pre-delete"]; !ok {
		t.Error("ClusterRole missing addon-pre-delete annotation")
	}

	rules, ok := objMap["rules"].([]interface{})
	if !ok || len(rules) == 0 {
		t.Fatal("ClusterRole has no rules")
	}

	// Verify argocds has delete+patch (needed for finalizer stripping)
	foundArgoCDDelete := false
	for _, r := range rules {
		rule := r.(map[string]interface{})
		resources, _ := rule["resources"].([]interface{})
		verbs, _ := rule["verbs"].([]interface{})
		for _, res := range resources {
			if res == "argocds" {
				for _, v := range verbs {
					if v == "delete" {
						foundArgoCDDelete = true
					}
				}
			}
		}
	}
	if !foundArgoCDDelete {
		t.Error("ClusterRole rules missing 'delete' for 'argocds'")
	}
}

func TestBuildCleanupClusterRoleBindingManifest(t *testing.T) {
	manifest := buildCleanupClusterRoleBindingManifest("test-addon-ns")

	var objMap map[string]interface{}
	if err := json.Unmarshal(manifest.Raw, &objMap); err != nil {
		t.Fatalf("Failed to unmarshal ClusterRoleBinding manifest: %v", err)
	}

	if objMap["kind"] != "ClusterRoleBinding" {
		t.Errorf("kind = %q, want ClusterRoleBinding", objMap["kind"])
	}

	metadata := objMap["metadata"].(map[string]interface{})
	if metadata["name"] != "gitops-addon-cleanup" {
		t.Errorf("name = %q, want gitops-addon-cleanup", metadata["name"])
	}

	annotations := metadata["annotations"].(map[string]interface{})
	if _, ok := annotations["addon.open-cluster-management.io/addon-pre-delete"]; !ok {
		t.Error("ClusterRoleBinding missing addon-pre-delete annotation")
	}

	roleRef := objMap["roleRef"].(map[string]interface{})
	if roleRef["name"] != "gitops-addon-cleanup" {
		t.Errorf("roleRef.name = %q, want gitops-addon-cleanup", roleRef["name"])
	}

	subjects := objMap["subjects"].([]interface{})
	if len(subjects) != 1 {
		t.Fatalf("expected 1 subject, got %d", len(subjects))
	}
	subject := subjects[0].(map[string]interface{})
	if subject["name"] != "gitops-addon" {
		t.Errorf("subject.name = %q, want gitops-addon", subject["name"])
	}
	if subject["namespace"] != "test-addon-ns" {
		t.Errorf("subject.namespace = %q, want test-addon-ns", subject["namespace"])
	}
}

func TestBuildAddonManifestsIncludesPreDeleteRBAC(t *testing.T) {
	manifests := buildAddonManifests("openshift-gitops", "addon:v1")

	preDeleteCount := 0
	var preDeleteKinds []string
	for _, m := range manifests {
		var objMap map[string]interface{}
		if err := json.Unmarshal(m.Raw, &objMap); err != nil {
			continue
		}
		metadata, _ := objMap["metadata"].(map[string]interface{})
		annotations, _ := metadata["annotations"].(map[string]interface{})
		if _, ok := annotations["addon.open-cluster-management.io/addon-pre-delete"]; ok {
			preDeleteCount++
			preDeleteKinds = append(preDeleteKinds, objMap["kind"].(string))
		}
	}

	if preDeleteCount != 3 {
		t.Errorf("expected 3 pre-delete manifests (ClusterRole, ClusterRoleBinding, Job), got %d: %v", preDeleteCount, preDeleteKinds)
	}
}

func TestNewManifestWithoutStatus(t *testing.T) {
	// Create a test deployment with a status field
	deployment := &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "test-namespace",
		},
	}

	// Call newManifestWithoutStatus
	manifest := newManifestWithoutStatus(deployment)

	// Verify Raw field is populated
	if len(manifest.Raw) == 0 {
		t.Error("newManifestWithoutStatus() returned empty Raw field")
	}

	// Unmarshal and verify no status field
	var objMap map[string]interface{}
	if err := json.Unmarshal(manifest.Raw, &objMap); err != nil {
		t.Fatalf("Failed to unmarshal manifest: %v", err)
	}

	// Verify status field was removed
	if _, hasStatus := objMap["status"]; hasStatus {
		t.Error("newManifestWithoutStatus() did not remove status field")
	}

	// Verify required fields are present
	if kind, ok := objMap["kind"].(string); !ok || kind != "Deployment" {
		t.Errorf("newManifestWithoutStatus() kind = %v, want Deployment", objMap["kind"])
	}

	if apiVersion, ok := objMap["apiVersion"].(string); !ok || apiVersion != "apps/v1" {
		t.Errorf("newManifestWithoutStatus() apiVersion = %v, want apps/v1", objMap["apiVersion"])
	}

	// Verify metadata is preserved
	metadata, ok := objMap["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("newManifestWithoutStatus() metadata is missing or invalid")
	}

	if name, ok := metadata["name"].(string); !ok || name != "test-deployment" {
		t.Errorf("newManifestWithoutStatus() metadata.name = %v, want test-deployment", metadata["name"])
	}
}

func TestDefaultOperatorImages(t *testing.T) {
	// Verify the default images are set (defined in pkg/utils/config.go)
	if utils.DefaultOperatorImages[utils.EnvArgoCDPrincipalImage] == "" {
		t.Error("Default ArgoCD Agent/Principal image should not be empty")
	}
	if utils.DefaultOperatorImages[utils.EnvGitOpsOperatorImage] == "" {
		t.Error("Default GitOps Operator image should not be empty")
	}
}

func TestBuildAddonEnvVars(t *testing.T) {
	envVars := buildAddonEnvVars()

	// Should have all image env vars (excluding hub-only) + proxy + ArgoCD agent vars + POD_NAMESPACE
	expectedMinCount := len(utils.DefaultOperatorImages) - 1 + 3 + 4 + 1 // -1 for hub-only, +3 proxy, +4 agent, +1 POD_NAMESPACE

	if len(envVars) < expectedMinCount {
		t.Errorf("Expected at least %d env vars, got %d", expectedMinCount, len(envVars))
	}

	// Check that all env vars have names and either Value or ValueFrom
	for _, env := range envVars {
		if env.Name == "" {
			t.Error("Env var name should not be empty")
		}
		// POD_NAMESPACE uses ValueFrom (downward API), others use Value
		if env.Name == "POD_NAMESPACE" {
			if env.ValueFrom == nil || env.ValueFrom.FieldRef == nil {
				t.Errorf("POD_NAMESPACE should use ValueFrom with FieldRef")
			}
			if env.ValueFrom.FieldRef.FieldPath != "metadata.namespace" {
				t.Errorf("POD_NAMESPACE FieldPath should be metadata.namespace, got %s", env.ValueFrom.FieldRef.FieldPath)
			}
		} else {
			if env.Value == "" {
				t.Errorf("Env var %s value should not be empty", env.Name)
			}
			// Value should be a placeholder like {{VAR_NAME}}
			if len(env.Value) < 4 || env.Value[:2] != "{{" || env.Value[len(env.Value)-2:] != "}}" {
				t.Errorf("Env var %s value should be a placeholder like {{VAR_NAME}}, got %s", env.Name, env.Value)
			}
		}
	}

	// Check that hub-only vars are NOT included
	for _, env := range envVars {
		if env.Name == utils.EnvArgoCDPrincipalImage {
			t.Errorf("Hub-only var %s should NOT be in buildAddonEnvVars()", utils.EnvArgoCDPrincipalImage)
		}
	}

	// Check that key spoke vars ARE included
	requiredVars := map[string]bool{
		"POD_NAMESPACE":              false,
		utils.EnvGitOpsOperatorImage: false,
		utils.EnvArgoCDImage:         false,
		utils.EnvHTTPProxy:           false,
		utils.EnvArgoCDAgentEnabled:  false,
		utils.EnvArgoCDAgentMode:     false,
	}

	for _, env := range envVars {
		if _, ok := requiredVars[env.Name]; ok {
			requiredVars[env.Name] = true
		}
	}

	for varName, found := range requiredVars {
		if !found {
			t.Errorf("Expected %s to be in buildAddonEnvVars()", varName)
		}
	}
}
