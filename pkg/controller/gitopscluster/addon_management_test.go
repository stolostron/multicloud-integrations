// Copyright 2021 The Kubernetes Authors.
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

package gitopscluster_test

import (
	"context"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/controller/gitopscluster"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Pure unit tests that don't require envtest/control plane
func TestCreateAddOnDeploymentConfigUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create scheme for fake client
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	addonv1alpha1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	t.Run("CreateWithEmptyArgoCDAgentSpec", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// GitOpsCluster with no ArgoCDAgent spec
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				// No ArgoCDAgent spec - should only set ENABLED=true
			},
		}

		// Call CreateAddOnDeploymentConfig
		err := reconciler.CreateAddOnDeploymentConfig(gitOpsCluster, "test-cluster")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify AddOnDeploymentConfig was created
		addonConfig := &addonv1alpha1.AddOnDeploymentConfig{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: "test-cluster",
		}, addonConfig)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Should only have ARGOCD_AGENT_ENABLED=true (no other variables)
		g.Expect(addonConfig.Spec.CustomizedVariables).To(gomega.HaveLen(1))
		g.Expect(addonConfig.Spec.CustomizedVariables[0].Name).To(gomega.Equal("ARGOCD_AGENT_ENABLED"))
		g.Expect(addonConfig.Spec.CustomizedVariables[0].Value).To(gomega.Equal("true"))
	})

	t.Run("CreateWithFullArgoCDAgentSpec", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-full"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// GitOpsCluster with full ArgoCDAgent spec
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops-full",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Image:                   "custom-agent:v1.0.0",
					ServerAddress:           "argocd.example.com",
					ServerPort:              "8080",
					Mode:                    "autonomous",
					GitOpsOperatorImage:     "custom-operator:v2.0.0",
					GitOpsOperatorNamespace: "custom-gitops-operator",
					GitOpsImage:             "custom-argocd:v3.0.0",
					GitOpsNamespace:         "custom-gitops",
					RedisImage:              "custom-redis:v4.0.0",
					ReconcileScope:          "Multi-Namespace",
					Action:                  "Update",
				},
			},
		}

		// Call CreateAddOnDeploymentConfig
		err := reconciler.CreateAddOnDeploymentConfig(gitOpsCluster, "test-cluster-full")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify AddOnDeploymentConfig was created with all variables
		addonConfig := &addonv1alpha1.AddOnDeploymentConfig{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: "test-cluster-full",
		}, addonConfig)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Should have all 12 managed variables (1 default + 11 from spec)
		g.Expect(addonConfig.Spec.CustomizedVariables).To(gomega.HaveLen(12))

		// Convert to map for easier verification
		varMap := make(map[string]string)
		for _, variable := range addonConfig.Spec.CustomizedVariables {
			varMap[variable.Name] = variable.Value
		}

		// Verify all variables are set correctly
		g.Expect(varMap["ARGOCD_AGENT_ENABLED"]).To(gomega.Equal("true"))
		g.Expect(varMap["ARGOCD_AGENT_IMAGE"]).To(gomega.Equal("custom-agent:v1.0.0"))
		g.Expect(varMap["ARGOCD_AGENT_SERVER_ADDRESS"]).To(gomega.Equal("argocd.example.com"))
		g.Expect(varMap["ARGOCD_AGENT_SERVER_PORT"]).To(gomega.Equal("8080"))
		g.Expect(varMap["ARGOCD_AGENT_MODE"]).To(gomega.Equal("autonomous"))
		g.Expect(varMap["GITOPS_OPERATOR_IMAGE"]).To(gomega.Equal("custom-operator:v2.0.0"))
		g.Expect(varMap["GITOPS_OPERATOR_NAMESPACE"]).To(gomega.Equal("custom-gitops-operator"))
		g.Expect(varMap["GITOPS_IMAGE"]).To(gomega.Equal("custom-argocd:v3.0.0"))
		g.Expect(varMap["GITOPS_NAMESPACE"]).To(gomega.Equal("custom-gitops"))
		g.Expect(varMap["REDIS_IMAGE"]).To(gomega.Equal("custom-redis:v4.0.0"))
		g.Expect(varMap["RECONCILE_SCOPE"]).To(gomega.Equal("Multi-Namespace"))
		g.Expect(varMap["ACTION"]).To(gomega.Equal("Update"))
	})

	t.Run("PreserveUserVariablesOnUpdate", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-update"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create existing AddOnDeploymentConfig with user-added variables
		existingConfig := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: "test-cluster-update",
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: []addonv1alpha1.CustomizedVariable{
					{Name: "ARGOCD_AGENT_ENABLED", Value: "true"},
					{Name: "ARGOCD_AGENT_IMAGE", Value: "old-image:v0.9"},
					{Name: "USER_CUSTOM_VAR1", Value: "user-value-1"},
					{Name: "USER_CUSTOM_VAR2", Value: "user-value-2"},
				},
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingConfig)).NotTo(gomega.HaveOccurred())

		// GitOpsCluster with updated spec
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops-update",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Image:         "new-image:v2.0",
					ServerAddress: "new-argocd.example.com",
					ServerPort:    "9090",
				},
			},
		}

		// Call CreateAddOnDeploymentConfig (should update existing)
		err := reconciler.CreateAddOnDeploymentConfig(gitOpsCluster, "test-cluster-update")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify AddOnDeploymentConfig was updated
		updatedConfig := &addonv1alpha1.AddOnDeploymentConfig{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: "test-cluster-update",
		}, updatedConfig)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Convert to map for easier verification
		varMap := make(map[string]string)
		for _, variable := range updatedConfig.Spec.CustomizedVariables {
			varMap[variable.Name] = variable.Value
		}

		// User variables should be preserved
		g.Expect(varMap["USER_CUSTOM_VAR1"]).To(gomega.Equal("user-value-1"))
		g.Expect(varMap["USER_CUSTOM_VAR2"]).To(gomega.Equal("user-value-2"))

		// Managed variables should be updated
		g.Expect(varMap["ARGOCD_AGENT_ENABLED"]).To(gomega.Equal("true"))
		g.Expect(varMap["ARGOCD_AGENT_IMAGE"]).To(gomega.Equal("new-image:v2.0"))
		g.Expect(varMap["ARGOCD_AGENT_SERVER_ADDRESS"]).To(gomega.Equal("new-argocd.example.com"))
		g.Expect(varMap["ARGOCD_AGENT_SERVER_PORT"]).To(gomega.Equal("9090"))

		// Mode should not be present since it wasn't specified in the GitOpsCluster
		_, modeExists := varMap["ARGOCD_AGENT_MODE"]
		g.Expect(modeExists).To(gomega.BeFalse())

		// Should have 6 total variables (4 managed + 2 user)
		g.Expect(updatedConfig.Spec.CustomizedVariables).To(gomega.HaveLen(6))
	})

	t.Run("OnlyUpdateSpecifiedFields", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-partial"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// GitOpsCluster with only some fields specified
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops-partial",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Image:               "new-image:v3.0",
					GitOpsOperatorImage: "partial-operator:v1.0",
					ReconcileScope:      "Single-Namespace",
					// Other fields are empty - should not be added
				},
			},
		}

		// Call CreateAddOnDeploymentConfig
		err := reconciler.CreateAddOnDeploymentConfig(gitOpsCluster, "test-cluster-partial")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify only specified variables are managed
		addonConfig := &addonv1alpha1.AddOnDeploymentConfig{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: "test-cluster-partial",
		}, addonConfig)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Convert to map for easier verification
		varMap := make(map[string]string)
		for _, variable := range addonConfig.Spec.CustomizedVariables {
			varMap[variable.Name] = variable.Value
		}

		// Should have ENABLED (always set) and specified fields
		g.Expect(varMap["ARGOCD_AGENT_ENABLED"]).To(gomega.Equal("true"))
		g.Expect(varMap["ARGOCD_AGENT_IMAGE"]).To(gomega.Equal("new-image:v3.0"))
		g.Expect(varMap["GITOPS_OPERATOR_IMAGE"]).To(gomega.Equal("partial-operator:v1.0"))
		g.Expect(varMap["RECONCILE_SCOPE"]).To(gomega.Equal("Single-Namespace"))

		// Other variables should not be present
		fieldsToCheck := []string{
			"ARGOCD_AGENT_SERVER_ADDRESS", "ARGOCD_AGENT_SERVER_PORT", "ARGOCD_AGENT_MODE",
			"GITOPS_OPERATOR_NAMESPACE", "GITOPS_IMAGE", "GITOPS_NAMESPACE",
			"REDIS_IMAGE", "ACTION",
		}
		for _, field := range fieldsToCheck {
			_, exists := varMap[field]
			g.Expect(exists).To(gomega.BeFalse(), "field %s should not exist", field)
		}

		// Should have only 4 variables (ENABLED + 3 specified)
		g.Expect(addonConfig.Spec.CustomizedVariables).To(gomega.HaveLen(4))
	})

	t.Run("ErrorWithEmptyNamespace", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		// Call with empty namespace - should error
		err := reconciler.CreateAddOnDeploymentConfig(gitOpsCluster, "")
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("no namespace provided"))
	})
}

// Tests for validateArgoCDAgentSpec function
func TestValidateArgoCDAgentSpecUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create scheme for fake client
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

	t.Run("NilSpecIsValid", func(t *testing.T) {
		// nil ArgoCDAgent spec should be valid
		err := reconciler.ValidateArgoCDAgentSpec(nil)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})

	t.Run("EmptyFieldsAreValid", func(t *testing.T) {
		// Empty fields should be valid
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode:           "",
			ReconcileScope: "",
			Action:         "",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})

	t.Run("ValidModeValues", func(t *testing.T) {
		validModes := []string{"managed", "autonomous"}
		for _, mode := range validModes {
			spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Mode: mode,
			}
			err := reconciler.ValidateArgoCDAgentSpec(spec)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "Mode '%s' should be valid", mode)
		}
	})

	t.Run("InvalidModeValues", func(t *testing.T) {
		invalidModes := []string{"invalid", "auto", "manual", "MANAGED", "Managed"}
		for _, mode := range invalidModes {
			spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Mode: mode,
			}
			err := reconciler.ValidateArgoCDAgentSpec(spec)
			g.Expect(err).To(gomega.HaveOccurred(), "Mode '%s' should be invalid", mode)
			g.Expect(err.Error()).To(gomega.ContainSubstring("invalid Mode"))
			g.Expect(err.Error()).To(gomega.ContainSubstring(mode))
		}
	})

	t.Run("ValidReconcileScopeValues", func(t *testing.T) {
		validScopes := []string{"All-Namespaces", "Single-Namespace"}
		for _, scope := range validScopes {
			spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
				ReconcileScope: scope,
			}
			err := reconciler.ValidateArgoCDAgentSpec(spec)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "ReconcileScope '%s' should be valid", scope)
		}
	})

	t.Run("InvalidReconcileScopeValues", func(t *testing.T) {
		invalidScopes := []string{"invalid", "all-namespaces", "single-namespace", "Global", "Cluster"}
		for _, scope := range invalidScopes {
			spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
				ReconcileScope: scope,
			}
			err := reconciler.ValidateArgoCDAgentSpec(spec)
			g.Expect(err).To(gomega.HaveOccurred(), "ReconcileScope '%s' should be invalid", scope)
			g.Expect(err.Error()).To(gomega.ContainSubstring("invalid ReconcileScope"))
			g.Expect(err.Error()).To(gomega.ContainSubstring(scope))
		}
	})

	t.Run("ValidActionValues", func(t *testing.T) {
		validActions := []string{"Install", "Delete-Operator"}
		for _, action := range validActions {
			spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Action: action,
			}
			err := reconciler.ValidateArgoCDAgentSpec(spec)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "Action '%s' should be valid", action)
		}
	})

	t.Run("InvalidActionValues", func(t *testing.T) {
		invalidActions := []string{"invalid", "install", "delete", "Uninstall", "Update", "Delete-Operator-Forcefully"}
		for _, action := range invalidActions {
			spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Action: action,
			}
			err := reconciler.ValidateArgoCDAgentSpec(spec)
			g.Expect(err).To(gomega.HaveOccurred(), "Action '%s' should be invalid", action)
			g.Expect(err.Error()).To(gomega.ContainSubstring("invalid Action"))
			g.Expect(err.Error()).To(gomega.ContainSubstring(action))
		}
	})

	t.Run("CombinedValidFieldsAreValid", func(t *testing.T) {
		// Test combination of all valid fields
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode:           "managed",
			ReconcileScope: "All-Namespaces",
			Action:         "Install",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})

	t.Run("CombinedInvalidFields", func(t *testing.T) {
		// Test combination with at least one invalid field
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode:           "invalid",
			ReconcileScope: "All-Namespaces",
			Action:         "Install",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("invalid Mode"))
	})

	t.Run("CaseInsensitiveValidation", func(t *testing.T) {
		// Test that validation is case-sensitive
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode: "MANAGED",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).To(gomega.HaveOccurred(), "Validation should be case-sensitive")
	})

	t.Run("WhitespaceHandling", func(t *testing.T) {
		// Test that whitespace is not trimmed
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode: " managed ",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).To(gomega.HaveOccurred(), "Whitespace should not be trimmed")
	})

	t.Run("MultipleValidFields", func(t *testing.T) {
		// All valid fields together should pass
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode:           "managed",
			ReconcileScope: "Single-Namespace",
			Action:         "Install",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})

	t.Run("MixedValidAndInvalidFields", func(t *testing.T) {
		// If any field is invalid, the whole spec should fail validation
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode:           "managed", // valid
			ReconcileScope: "invalid", // invalid
			Action:         "Install", // valid
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("invalid ReconcileScope"))
	})

	t.Run("ValidationDoesNotAffectOtherFields", func(t *testing.T) {
		// Other fields should not be affected by validation
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode:           "managed",
			ReconcileScope: "Single-Namespace",
			Action:         "Install",
			Image:          "custom-image:v1.0",
			ServerAddress:  "custom.server.com",
			ServerPort:     "9090",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})
}

// Tests for UpdateManagedClusterAddonConfig function
func TestUpdateManagedClusterAddonConfigUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create scheme for fake client
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	addonv1alpha1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	t.Run("ManagedClusterAddOnNotExists", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace but no ManagedClusterAddOn
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-no-addon"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Call UpdateManagedClusterAddonConfig - should not error
		err := reconciler.UpdateManagedClusterAddonConfig("test-cluster-no-addon")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify no ManagedClusterAddOn was created
		addon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-no-addon",
		}, addon)
		g.Expect(err).To(gomega.HaveOccurred()) // Should not be found
	})

	t.Run("ManagedClusterAddOnExistsWithCorrectConfig", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-correct-config"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create ManagedClusterAddOn with correct config already
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-correct-config",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				Configs: []addonv1alpha1.AddOnConfig{
					{
						ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
							Group:    "addon.open-cluster-management.io",
							Resource: "addondeploymentconfigs",
						},
						ConfigReferent: addonv1alpha1.ConfigReferent{
							Name:      "gitops-addon-config",
							Namespace: "test-cluster-correct-config",
						},
					},
				},
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// Call UpdateManagedClusterAddonConfig - should not error and not modify
		err := reconciler.UpdateManagedClusterAddonConfig("test-cluster-correct-config")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify ManagedClusterAddOn still has only one config
		updatedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-correct-config",
		}, updatedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updatedAddon.Spec.Configs).To(gomega.HaveLen(1))

		// Verify the config is still correct
		config := updatedAddon.Spec.Configs[0]
		g.Expect(config.Group).To(gomega.Equal("addon.open-cluster-management.io"))
		g.Expect(config.Resource).To(gomega.Equal("addondeploymentconfigs"))
		g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
		g.Expect(config.Namespace).To(gomega.Equal("test-cluster-correct-config"))
	})

	t.Run("ManagedClusterAddOnExistsWithoutConfig", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-no-config"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create ManagedClusterAddOn without AddOnDeploymentConfig reference
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-no-config",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				// No configs
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// Call UpdateManagedClusterAddonConfig - should add config
		err := reconciler.UpdateManagedClusterAddonConfig("test-cluster-no-config")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify ManagedClusterAddOn was updated with config
		updatedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-no-config",
		}, updatedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updatedAddon.Spec.Configs).To(gomega.HaveLen(1))

		// Verify the config is correct
		config := updatedAddon.Spec.Configs[0]
		g.Expect(config.Group).To(gomega.Equal("addon.open-cluster-management.io"))
		g.Expect(config.Resource).To(gomega.Equal("addondeploymentconfigs"))
		g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
		g.Expect(config.Namespace).To(gomega.Equal("test-cluster-no-config"))
	})

	t.Run("ManagedClusterAddOnExistsWithOtherConfigs", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-other-configs"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create ManagedClusterAddOn with other configs but not the AddOnDeploymentConfig
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-other-configs",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				Configs: []addonv1alpha1.AddOnConfig{
					{
						ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
							Group:    "addon.open-cluster-management.io",
							Resource: "addontemplates",
						},
						ConfigReferent: addonv1alpha1.ConfigReferent{
							Name: "gitops-addon-template",
						},
					},
					{
						ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
							Group:    "config.custom.io",
							Resource: "customconfigs",
						},
						ConfigReferent: addonv1alpha1.ConfigReferent{
							Name:      "custom-config",
							Namespace: "test-cluster-other-configs",
						},
					},
				},
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// Call UpdateManagedClusterAddonConfig - should append new config
		err := reconciler.UpdateManagedClusterAddonConfig("test-cluster-other-configs")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify ManagedClusterAddOn was updated with new config appended
		updatedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-other-configs",
		}, updatedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updatedAddon.Spec.Configs).To(gomega.HaveLen(3)) // Original 2 + new 1

		// Verify original configs are preserved
		var foundTemplate, foundCustom, foundDeploymentConfig bool
		for _, config := range updatedAddon.Spec.Configs {
			if config.Group == "addon.open-cluster-management.io" && config.Resource == "addontemplates" {
				foundTemplate = true
				g.Expect(config.Name).To(gomega.Equal("gitops-addon-template"))
			} else if config.Group == "config.custom.io" && config.Resource == "customconfigs" {
				foundCustom = true
				g.Expect(config.Name).To(gomega.Equal("custom-config"))
				g.Expect(config.Namespace).To(gomega.Equal("test-cluster-other-configs"))
			} else if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
				foundDeploymentConfig = true
				g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
				g.Expect(config.Namespace).To(gomega.Equal("test-cluster-other-configs"))
			}
		}

		g.Expect(foundTemplate).To(gomega.BeTrue())
		g.Expect(foundCustom).To(gomega.BeTrue())
		g.Expect(foundDeploymentConfig).To(gomega.BeTrue())
	})

	t.Run("ErrorWithEmptyNamespace", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Call with empty namespace - should error
		err := reconciler.UpdateManagedClusterAddonConfig("")
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("no namespace provided"))
	})
}

// Integration tests for CreateAddOnDeploymentConfig with ManagedClusterAddOn updates
func TestCreateAddOnDeploymentConfigWithManagedClusterAddOnIntegration(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create scheme for fake client
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	addonv1alpha1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	t.Run("CreateConfigAndUpdateExistingManagedClusterAddOn", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-integration"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create existing ManagedClusterAddOn without config reference
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-integration",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				// No configs initially
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// GitOpsCluster spec
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops-integration",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Image: "test-image:v1.0",
				},
			},
		}

		// Call CreateAddOnDeploymentConfig - should create config AND update ManagedClusterAddOn
		err := reconciler.CreateAddOnDeploymentConfig(gitOpsCluster, "test-cluster-integration")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify AddOnDeploymentConfig was created
		addonConfig := &addonv1alpha1.AddOnDeploymentConfig{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: "test-cluster-integration",
		}, addonConfig)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(addonConfig.Spec.CustomizedVariables).To(gomega.HaveLen(2)) // ENABLED + IMAGE

		// Verify ManagedClusterAddOn was updated with config reference
		updatedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-integration",
		}, updatedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updatedAddon.Spec.Configs).To(gomega.HaveLen(1))

		// Verify the config reference is correct
		config := updatedAddon.Spec.Configs[0]
		g.Expect(config.Group).To(gomega.Equal("addon.open-cluster-management.io"))
		g.Expect(config.Resource).To(gomega.Equal("addondeploymentconfigs"))
		g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
		g.Expect(config.Namespace).To(gomega.Equal("test-cluster-integration"))
	})

	t.Run("CreateConfigWithNoManagedClusterAddOn", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace but no ManagedClusterAddOn
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-no-addon-integration"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// GitOpsCluster spec
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops-no-addon",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Image: "test-image:v1.0",
				},
			},
		}

		// Call CreateAddOnDeploymentConfig - should create config and not error about missing ManagedClusterAddOn
		err := reconciler.CreateAddOnDeploymentConfig(gitOpsCluster, "test-cluster-no-addon-integration")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify AddOnDeploymentConfig was created
		addonConfig := &addonv1alpha1.AddOnDeploymentConfig{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: "test-cluster-no-addon-integration",
		}, addonConfig)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify no ManagedClusterAddOn was created (should remain non-existent)
		addon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-no-addon-integration",
		}, addon)
		g.Expect(err).To(gomega.HaveOccurred()) // Should not be found
	})

	t.Run("UpdateConfigAndPreserveManagedClusterAddOnConfigs", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-preserve-configs"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create existing AddOnDeploymentConfig
		existingConfig := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: "test-cluster-preserve-configs",
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: []addonv1alpha1.CustomizedVariable{
					{Name: "ARGOCD_AGENT_ENABLED", Value: "true"},
					{Name: "USER_VAR", Value: "user-value"},
				},
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingConfig)).NotTo(gomega.HaveOccurred())

		// Create existing ManagedClusterAddOn with other configs
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-preserve-configs",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				Configs: []addonv1alpha1.AddOnConfig{
					{
						ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
							Group:    "addon.open-cluster-management.io",
							Resource: "addontemplates",
						},
						ConfigReferent: addonv1alpha1.ConfigReferent{
							Name: "gitops-addon-template",
						},
					},
				},
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// GitOpsCluster spec with new values
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops-preserve",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Image:         "new-image:v2.0",
					ServerAddress: "new-server.com",
				},
			},
		}

		// Call CreateAddOnDeploymentConfig - should update both config and addon
		err := reconciler.CreateAddOnDeploymentConfig(gitOpsCluster, "test-cluster-preserve-configs")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify AddOnDeploymentConfig was updated
		updatedConfig := &addonv1alpha1.AddOnDeploymentConfig{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: "test-cluster-preserve-configs",
		}, updatedConfig)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Convert to map for verification
		varMap := make(map[string]string)
		for _, variable := range updatedConfig.Spec.CustomizedVariables {
			varMap[variable.Name] = variable.Value
		}

		// Verify user variable preserved and managed variables updated
		g.Expect(varMap["USER_VAR"]).To(gomega.Equal("user-value"))
		g.Expect(varMap["ARGOCD_AGENT_ENABLED"]).To(gomega.Equal("true"))
		g.Expect(varMap["ARGOCD_AGENT_IMAGE"]).To(gomega.Equal("new-image:v2.0"))
		g.Expect(varMap["ARGOCD_AGENT_SERVER_ADDRESS"]).To(gomega.Equal("new-server.com"))

		// Verify ManagedClusterAddOn was updated with config reference while preserving existing configs
		updatedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-preserve-configs",
		}, updatedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updatedAddon.Spec.Configs).To(gomega.HaveLen(2)) // Original template + new deployment config

		// Verify both configs exist
		var foundTemplate, foundDeploymentConfig bool
		for _, config := range updatedAddon.Spec.Configs {
			if config.Group == "addon.open-cluster-management.io" && config.Resource == "addontemplates" {
				foundTemplate = true
				g.Expect(config.Name).To(gomega.Equal("gitops-addon-template"))
			} else if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
				foundDeploymentConfig = true
				g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
				g.Expect(config.Namespace).To(gomega.Equal("test-cluster-preserve-configs"))
			}
		}

		g.Expect(foundTemplate).To(gomega.BeTrue())
		g.Expect(foundDeploymentConfig).To(gomega.BeTrue())
	})
}

// Tests for EnsureManagedClusterAddon function
func TestEnsureManagedClusterAddon(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create scheme for fake client
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	addonv1alpha1.AddToScheme(scheme)
	spokeclusterv1.AddToScheme(scheme)

	t.Run("CreateNewManagedClusterAddon", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-new"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Call EnsureManagedClusterAddon - should create new ManagedClusterAddon
		err := reconciler.EnsureManagedClusterAddon("test-cluster-new")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify ManagedClusterAddon was created
		addon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-new",
		}, addon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(addon.Spec.InstallNamespace).To(gomega.Equal("open-cluster-management-agent-addon"))
		g.Expect(addon.Spec.Configs).To(gomega.HaveLen(1))

		// Verify the config reference is correct
		config := addon.Spec.Configs[0]
		g.Expect(config.Group).To(gomega.Equal("addon.open-cluster-management.io"))
		g.Expect(config.Resource).To(gomega.Equal("addondeploymentconfigs"))
		g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
		g.Expect(config.Namespace).To(gomega.Equal("test-cluster-new"))
	})

	t.Run("UpdateExistingManagedClusterAddonWithoutConfig", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-existing"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create existing ManagedClusterAddOn without config reference
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-existing",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				// No configs initially
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// Call EnsureManagedClusterAddon - should add config reference
		err := reconciler.EnsureManagedClusterAddon("test-cluster-existing")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify ManagedClusterAddon was updated with config reference
		updatedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-existing",
		}, updatedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updatedAddon.Spec.Configs).To(gomega.HaveLen(1))

		// Verify the config reference is correct
		config := updatedAddon.Spec.Configs[0]
		g.Expect(config.Group).To(gomega.Equal("addon.open-cluster-management.io"))
		g.Expect(config.Resource).To(gomega.Equal("addondeploymentconfigs"))
		g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
		g.Expect(config.Namespace).To(gomega.Equal("test-cluster-existing"))
	})

	t.Run("PreserveExistingManagedClusterAddonWithCorrectConfig", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-preserve"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create existing ManagedClusterAddOn with correct config reference
		correctConfig := addonv1alpha1.AddOnConfig{
			ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
				Group:    "addon.open-cluster-management.io",
				Resource: "addondeploymentconfigs",
			},
			ConfigReferent: addonv1alpha1.ConfigReferent{
				Name:      "gitops-addon-config",
				Namespace: "test-cluster-preserve",
			},
		}
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-preserve",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				Configs:          []addonv1alpha1.AddOnConfig{correctConfig},
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// Call EnsureManagedClusterAddon - should preserve existing config
		err := reconciler.EnsureManagedClusterAddon("test-cluster-preserve")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify ManagedClusterAddon was not changed
		preservedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-preserve",
		}, preservedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(preservedAddon.Spec.Configs).To(gomega.HaveLen(1))

		// Verify the config reference is still correct
		config := preservedAddon.Spec.Configs[0]
		g.Expect(config.Group).To(gomega.Equal("addon.open-cluster-management.io"))
		g.Expect(config.Resource).To(gomega.Equal("addondeploymentconfigs"))
		g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
		g.Expect(config.Namespace).To(gomega.Equal("test-cluster-preserve"))
	})

	t.Run("PreserveExistingManagedClusterAddonWithMultipleConfigs", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-multi-configs"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create existing ManagedClusterAddOn with multiple configs including ours
		gitopsConfig := addonv1alpha1.AddOnConfig{
			ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
				Group:    "addon.open-cluster-management.io",
				Resource: "addondeploymentconfigs",
			},
			ConfigReferent: addonv1alpha1.ConfigReferent{
				Name:      "gitops-addon-config",
				Namespace: "test-cluster-multi-configs",
			},
		}
		otherConfig := addonv1alpha1.AddOnConfig{
			ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
				Group:    "example.com",
				Resource: "customconfigs",
			},
			ConfigReferent: addonv1alpha1.ConfigReferent{
				Name:      "other-config",
				Namespace: "test-cluster-multi-configs",
			},
		}
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-multi-configs",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				Configs:          []addonv1alpha1.AddOnConfig{otherConfig, gitopsConfig}, // Our config already exists
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// Call EnsureManagedClusterAddon - should preserve all existing configs
		err := reconciler.EnsureManagedClusterAddon("test-cluster-multi-configs")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify ManagedClusterAddon preserved both configs
		preservedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-multi-configs",
		}, preservedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(preservedAddon.Spec.Configs).To(gomega.HaveLen(2)) // Both configs should remain

		// Verify our gitops config exists
		foundGitopsConfig := false
		foundOtherConfig := false
		for _, config := range preservedAddon.Spec.Configs {
			if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
				foundGitopsConfig = true
				g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
				g.Expect(config.Namespace).To(gomega.Equal("test-cluster-multi-configs"))
			}
			if config.Group == "example.com" && config.Resource == "customconfigs" {
				foundOtherConfig = true
			}
		}
		g.Expect(foundGitopsConfig).To(gomega.BeTrue())
		g.Expect(foundOtherConfig).To(gomega.BeTrue())
	})

	t.Run("AddConfigToExistingManagedClusterAddonWithOtherConfigs", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Create namespace
		testNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-add-to-existing"},
		}
		g.Expect(fakeClient.Create(context.TODO(), testNs)).NotTo(gomega.HaveOccurred())

		// Create existing ManagedClusterAddOn with other configs but missing ours
		otherConfig := addonv1alpha1.AddOnConfig{
			ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
				Group:    "example.com",
				Resource: "customconfigs",
			},
			ConfigReferent: addonv1alpha1.ConfigReferent{
				Name:      "other-config",
				Namespace: "test-cluster-add-to-existing",
			},
		}
		existingAddon := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "test-cluster-add-to-existing",
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				Configs:          []addonv1alpha1.AddOnConfig{otherConfig}, // Only other config, missing ours
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingAddon)).NotTo(gomega.HaveOccurred())

		// Call EnsureManagedClusterAddon - should add our config while preserving others
		err := reconciler.EnsureManagedClusterAddon("test-cluster-add-to-existing")
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify ManagedClusterAddon now has both configs
		updatedAddon := &addonv1alpha1.ManagedClusterAddOn{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "gitops-addon",
			Namespace: "test-cluster-add-to-existing",
		}, updatedAddon)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updatedAddon.Spec.Configs).To(gomega.HaveLen(2)) // Should now have both configs

		// Verify both configs exist
		foundGitopsConfig := false
		foundOtherConfig := false
		for _, config := range updatedAddon.Spec.Configs {
			if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
				foundGitopsConfig = true
				g.Expect(config.Name).To(gomega.Equal("gitops-addon-config"))
				g.Expect(config.Namespace).To(gomega.Equal("test-cluster-add-to-existing"))
			}
			if config.Group == "example.com" && config.Resource == "customconfigs" {
				foundOtherConfig = true
			}
		}
		g.Expect(foundGitopsConfig).To(gomega.BeTrue())
		g.Expect(foundOtherConfig).To(gomega.BeTrue())
	})

	t.Run("ErrorWithEmptyNamespace", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &gitopscluster.ReconcileGitOpsCluster{Client: fakeClient}

		// Call EnsureManagedClusterAddon with empty namespace - should return error
		err := reconciler.EnsureManagedClusterAddon("")
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.Equal("no namespace provided"))
	})
}
