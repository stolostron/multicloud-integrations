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
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
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
					Image: "test-agent:v1",
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

	// Verify the number of manifests (should include Job, ServiceAccount, ClusterRoleBinding, Deployment)
	expectedCount := 4
	if len(manifests) != expectedCount {
		t.Errorf("buildAddonManifests() manifest count = %v, want %v", len(manifests), expectedCount)
	}
}

func TestDefaultArgoCDAgentImage(t *testing.T) {
	// Verify the constant is set
	if DefaultArgoCDAgentImage == "" {
		t.Error("DefaultArgoCDAgentImage should not be empty")
	}
}
