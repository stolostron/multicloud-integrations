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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	boolTrue    = true
	boolTruePtr = &boolTrue
)

func TestCreateAddOnDeploymentConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	err := addonv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)
	err = gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name               string
		gitOpsCluster      *gitopsclusterV1beta1.GitOpsCluster
		namespace          string
		existingObjects    []client.Object
		expectedError      bool
		expectedConfigName string
		validateFunc       func(t *testing.T, c client.Client, namespace string)
	}{
		{
			name:      "empty namespace should return error",
			namespace: "",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
			},
			expectedError: true,
		},
		{
			name:      "create new AddOnDeploymentConfig successfully",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						GitOpsOperatorImage: "test-operator-image:latest",
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "test-server.com",
							ServerPort:    "443",
							Mode:          "managed",
						},
					},
				},
			},
			expectedConfigName: "gitops-addon-config",
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				// Verify expected variables are set
				// Note: Only GitOpsOperatorImage is configurable via GitOpsCluster spec
				// Other images are handled by the operator via environment variables
				expectedVars := map[string]string{
					"ARGOCD_AGENT_ENABLED":        "false",
					"GITOPS_OPERATOR_IMAGE":       "test-operator-image:latest",
					"ARGOCD_AGENT_SERVER_ADDRESS": "test-server.com",
					"ARGOCD_AGENT_SERVER_PORT":    "443",
					"ARGOCD_AGENT_MODE":           "managed",
					"ARGOCD_NAMESPACE":            utils.GitOpsNamespace,
				}

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				for expectedKey, expectedValue := range expectedVars {
					assert.Equal(t, expectedValue, varMap[expectedKey], "Variable %s should have value %s", expectedKey, expectedValue)
				}
			},
		},
		{
			name:      "local-cluster namespace gets ARGOCD_NAMESPACE set to local-cluster",
			namespace: "local-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						GitOpsOperatorImage: "test-operator-image:latest",
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				assert.Equal(t, "local-cluster", varMap["ARGOCD_NAMESPACE"], "ARGOCD_NAMESPACE should be set to local-cluster for local-cluster namespace")
			},
		},
		{
			name:      "update existing AddOnDeploymentConfig: managed vars updated, user vars preserved",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						GitOpsOperatorImage: "updated-operator-image:latest",
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled:       boolTruePtr,
							ServerAddress: "updated-server.com",
						},
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon-config",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						CustomizedVariables: []addonv1alpha1.CustomizedVariable{
							{Name: "ARGOCD_AGENT_ENABLED", Value: "true"},
							{Name: "USER_CUSTOM_VAR", Value: "user-value"},
							{Name: "GITOPS_OPERATOR_IMAGE", Value: "old-operator-image:latest"},
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				// User variable should be preserved
				assert.Equal(t, "user-value", varMap["USER_CUSTOM_VAR"], "User custom variable should be preserved")
				// Managed variables should be updated to current spec values
				assert.Equal(t, "updated-operator-image:latest", varMap["GITOPS_OPERATOR_IMAGE"], "Managed variable should be updated")
				// New managed variables should be added
				assert.Equal(t, "updated-server.com", varMap["ARGOCD_AGENT_SERVER_ADDRESS"], "New managed variable should be added")
			},
		},
		{
			name:      "override existing configs when overrideExistingConfigs is true",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						OverrideExistingConfigs: func(b bool) *bool { return &b }(true),
						GitOpsOperatorImage:     "new-operator-image:latest",
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "new-server.com",
						},
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon-config",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						CustomizedVariables: []addonv1alpha1.CustomizedVariable{
							{Name: "ARGOCD_AGENT_ENABLED", Value: "true"},
							{Name: "USER_CUSTOM_VAR", Value: "user-value"},
							{Name: "GITOPS_OPERATOR_IMAGE", Value: "old-operator-image:latest"},
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				// User variable should be preserved
				assert.Equal(t, "user-value", varMap["USER_CUSTOM_VAR"], "User custom variable should be preserved")
				// Managed variables should be overridden
				assert.Equal(t, "new-operator-image:latest", varMap["GITOPS_OPERATOR_IMAGE"], "Managed variable should be overridden")
				assert.Equal(t, "new-server.com", varMap["ARGOCD_AGENT_SERVER_ADDRESS"], "New managed variable should be added")
				assert.Equal(t, "false", varMap["ARGOCD_AGENT_ENABLED"], "Default managed variable should be be false")
			},
		},
		{
			name:      "preserve mode (overrideExistingConfigs=false): managed vars updated, user vars preserved",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						OverrideExistingConfigs: func(b bool) *bool { return &b }(false),
						GitOpsOperatorImage:     "new-operator-image:latest",
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled:       boolTruePtr,
							ServerAddress: "new-server.com",
						},
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon-config",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						CustomizedVariables: []addonv1alpha1.CustomizedVariable{
							{Name: "ARGOCD_AGENT_ENABLED", Value: "true"},
							{Name: "USER_CUSTOM_VAR", Value: "user-value"},
							{Name: "GITOPS_OPERATOR_IMAGE", Value: "old-operator-image:latest"},
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				// User variable should be preserved
				assert.Equal(t, "user-value", varMap["USER_CUSTOM_VAR"], "User custom variable should be preserved")
				// Managed variables should be updated to current spec values
				assert.Equal(t, "new-operator-image:latest", varMap["GITOPS_OPERATOR_IMAGE"], "Managed variable should be updated")
				// New managed variables should be added
				assert.Equal(t, "new-server.com", varMap["ARGOCD_AGENT_SERVER_ADDRESS"], "New managed variable should be added")
				assert.Equal(t, "true", varMap["ARGOCD_AGENT_ENABLED"], "ARGOCD_AGENT_ENABLED should be updated to match current state")
			},
		},
		{
			name:      "OLM subscription env vars passed when olmSubscription.enabled=true",
			namespace: "test-cluster-olm",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops-olm",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolTruePtr,
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled:         boolTruePtr,
							Channel:         "stable",
							Source:          "custom-catalog",
							SourceNamespace: "custom-marketplace",
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				assert.Equal(t, "true", varMap["OLM_SUBSCRIPTION_ENABLED"])
				assert.Equal(t, "stable", varMap["OLM_SUBSCRIPTION_CHANNEL"])
				assert.Equal(t, "custom-catalog", varMap["OLM_SUBSCRIPTION_SOURCE"])
				assert.Equal(t, "custom-marketplace", varMap["OLM_SUBSCRIPTION_SOURCE_NAMESPACE"])
				// Defaults should be filled in for unspecified fields
				assert.Equal(t, "openshift-gitops-operator", varMap["OLM_SUBSCRIPTION_NAME"])
				assert.Equal(t, "openshift-operators", varMap["OLM_SUBSCRIPTION_NAMESPACE"])
				assert.Equal(t, "Automatic", varMap["OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL"])
			},
		},
		{
			name:      "OLM vars present with defaults when olmSubscription.enabled=false",
			namespace: "test-cluster-no-olm",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops-no-olm",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolTruePtr,
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled: func(b bool) *bool { return &b }(false),
							Channel: "stable",
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				assert.Equal(t, "false", varMap["OLM_SUBSCRIPTION_ENABLED"], "OLM_SUBSCRIPTION_ENABLED should be false")
				assert.Equal(t, "latest", varMap["OLM_SUBSCRIPTION_CHANNEL"], "Default channel should be used when disabled")
				assert.Equal(t, "Automatic", varMap["OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL"], "Default approval should be set")
			},
		},
		{
			name:      "OLM vars present with defaults when olmSubscription is absent",
			namespace: "test-cluster-no-olm-spec",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops-no-olm-spec",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolTruePtr,
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				assert.Equal(t, "false", varMap["OLM_SUBSCRIPTION_ENABLED"], "OLM_SUBSCRIPTION_ENABLED should be false when absent")
				assert.Equal(t, "Automatic", varMap["OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL"], "Default approval should be set")
				assert.Equal(t, "openshift-gitops-operator", varMap["OLM_SUBSCRIPTION_NAME"], "Default name should be set")
			},
		},
		{
			name:      "all managed vars update in preserve mode, user vars preserved",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						OverrideExistingConfigs: func(b bool) *bool { return &b }(false),
						GitOpsOperatorImage:     "operator-image:latest",
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: func(b bool) *bool { return &b }(false),
						},
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon-config",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						CustomizedVariables: []addonv1alpha1.CustomizedVariable{
							{Name: "ARGOCD_AGENT_ENABLED", Value: "true"},
							{Name: "USER_CUSTOM_VAR", Value: "user-value"},
							{Name: "GITOPS_OPERATOR_IMAGE", Value: "old-operator-image:latest"},
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				config := &addonv1alpha1.AddOnDeploymentConfig{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon-config",
					Namespace: namespace,
				}, config)
				require.NoError(t, err)

				varMap := make(map[string]string)
				for _, variable := range config.Spec.CustomizedVariables {
					varMap[variable.Name] = variable.Value
				}

				assert.Equal(t, "false", varMap["ARGOCD_AGENT_ENABLED"], "ARGOCD_AGENT_ENABLED should be updated")
				assert.Equal(t, "user-value", varMap["USER_CUSTOM_VAR"], "User custom variable should be preserved")
				assert.Equal(t, "operator-image:latest", varMap["GITOPS_OPERATOR_IMAGE"], "Managed variable should be updated")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			err := reconciler.CreateAddOnDeploymentConfig(tt.gitOpsCluster, tt.namespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient, tt.namespace)
				}
			}
		})
	}
}

func TestUpdateManagedClusterAddonConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	err := addonv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		namespace       string
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client, namespace string)
	}{
		{
			name:          "empty namespace should return error",
			namespace:     "",
			expectedError: true,
		},
		{
			name:      "no ManagedClusterAddOn exists - should not error",
			namespace: "test-cluster",
		},
		{
			name:      "update existing ManagedClusterAddOn with config",
			namespace: "test-cluster",
			existingObjects: []client.Object{
				&addonv1alpha1.ManagedClusterAddOn{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.ManagedClusterAddOnSpec{},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon",
					Namespace: namespace,
				}, addon)
				require.NoError(t, err)

				// Verify config was added
				assert.Len(t, addon.Spec.Configs, 1)
				config := addon.Spec.Configs[0]
				assert.Equal(t, "addon.open-cluster-management.io", config.Group)
				assert.Equal(t, "addondeploymentconfigs", config.Resource)
				assert.Equal(t, "gitops-addon-config", config.Name)
				assert.Equal(t, namespace, config.Namespace)
			},
		},
		{
			name:      "addon already has correct config - no update needed",
			namespace: "test-cluster",
			existingObjects: []client.Object{
				&addonv1alpha1.ManagedClusterAddOn{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.ManagedClusterAddOnSpec{
						Configs: []addonv1alpha1.AddOnConfig{
							{
								ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
									Group:    "addon.open-cluster-management.io",
									Resource: "addondeploymentconfigs",
								},
								ConfigReferent: addonv1alpha1.ConfigReferent{
									Name:      "gitops-addon-config",
									Namespace: "test-cluster",
								},
							},
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon",
					Namespace: namespace,
				}, addon)
				require.NoError(t, err)

				// Should still have only one config
				assert.Len(t, addon.Spec.Configs, 1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			err := reconciler.UpdateManagedClusterAddonConfig(tt.namespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient, tt.namespace)
				}
			}
		})
	}
}

func TestEnsureManagedClusterAddon(t *testing.T) {
	scheme := runtime.NewScheme()
	err := addonv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)
	err = gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		namespace       string
		gitOpsCluster   *gitopsclusterV1beta1.GitOpsCluster
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client, namespace string)
	}{
		{
			name:          "empty namespace should return error",
			namespace:     "",
			expectedError: true,
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitopscluster",
					Namespace: "test-namespace",
				},
			},
		},
		{
			name:      "create new ManagedClusterAddOn with ArgoCD agent disabled - only AddonDeploymentConfig",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitopscluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: func(b bool) *bool { return &b }(false),
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon",
					Namespace: namespace,
				}, addon)
				require.NoError(t, err)

				// Only AddonDeploymentConfig when ArgoCD agent is disabled
				assert.Len(t, addon.Spec.Configs, 1)

				// Check that we only have AddonDeploymentConfig
				foundTemplate := false
				foundDeploymentConfig := false
				for _, config := range addon.Spec.Configs {
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addontemplates" {
						foundTemplate = true
					}
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
						foundDeploymentConfig = true
					}
				}
				assert.False(t, foundTemplate, "Should NOT have AddOnTemplate config when ArgoCD agent is disabled")
				assert.True(t, foundDeploymentConfig, "Should have AddonDeploymentConfig")
			},
		},
		{
			name:      "create new ManagedClusterAddOn with ArgoCD agent enabled - both configs",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitopscluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: func(b bool) *bool { return &b }(true),
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon",
					Namespace: namespace,
				}, addon)
				require.NoError(t, err)

				// Both AddOnTemplate and AddonDeploymentConfig when ArgoCD agent is enabled
				assert.Len(t, addon.Spec.Configs, 2)

				// Check that we have both configs
				foundTemplate := false
				foundDeploymentConfig := false
				for _, config := range addon.Spec.Configs {
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addontemplates" {
						assert.Equal(t, "gitops-addon-test-namespace-test-gitopscluster", config.Name)
						foundTemplate = true
					}
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
						foundDeploymentConfig = true
					}
				}
				assert.True(t, foundTemplate, "Should have AddOnTemplate config when ArgoCD agent is enabled")
				assert.True(t, foundDeploymentConfig, "Should have AddonDeploymentConfig")
			},
		},
		{
			name:      "update existing ManagedClusterAddOn with missing config - ArgoCD agent disabled",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitopscluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: func(b bool) *bool { return &b }(false),
						},
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.ManagedClusterAddOn{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.ManagedClusterAddOnSpec{},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon",
					Namespace: namespace,
				}, addon)
				require.NoError(t, err)

				// Only AddonDeploymentConfig when ArgoCD agent is disabled
				assert.Len(t, addon.Spec.Configs, 1)

				// Check that we only have AddonDeploymentConfig
				foundTemplate := false
				foundDeploymentConfig := false
				for _, config := range addon.Spec.Configs {
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addontemplates" {
						foundTemplate = true
					}
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
						foundDeploymentConfig = true
					}
				}
				assert.False(t, foundTemplate, "Should NOT have AddOnTemplate config when ArgoCD agent is disabled")
				assert.True(t, foundDeploymentConfig, "Should have AddonDeploymentConfig")
			},
		},
		{
			name:      "update existing ManagedClusterAddOn with missing config - ArgoCD agent enabled",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitopscluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: func(b bool) *bool { return &b }(true),
						},
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.ManagedClusterAddOn{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.ManagedClusterAddOnSpec{},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon",
					Namespace: namespace,
				}, addon)
				require.NoError(t, err)

				// Both AddOnTemplate and AddonDeploymentConfig when ArgoCD agent is enabled
				assert.Len(t, addon.Spec.Configs, 2)

				// Check that we have both configs
				foundTemplate := false
				foundDeploymentConfig := false
				for _, config := range addon.Spec.Configs {
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addontemplates" {
						assert.Equal(t, "gitops-addon-test-namespace-test-gitopscluster", config.Name)
						foundTemplate = true
					}
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
						foundDeploymentConfig = true
					}
				}
				assert.True(t, foundTemplate, "Should have AddOnTemplate config when ArgoCD agent is enabled")
				assert.True(t, foundDeploymentConfig, "Should have AddonDeploymentConfig")
			},
		},
		{
			name:      "remove AddOnTemplate from existing ManagedClusterAddOn when ArgoCD agent is disabled",
			namespace: "test-cluster",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitopscluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: func(b bool) *bool { return &b }(false),
						},
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.ManagedClusterAddOn{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.ManagedClusterAddOnSpec{
						Configs: []addonv1alpha1.AddOnConfig{
							{
								ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
									Group:    "addon.open-cluster-management.io",
									Resource: "addontemplates",
								},
								ConfigReferent: addonv1alpha1.ConfigReferent{
									Name: "gitops-addon-test-namespace-test-gitopscluster",
								},
							},
							{
								ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
									Group:    "addon.open-cluster-management.io",
									Resource: "addondeploymentconfigs",
								},
								ConfigReferent: addonv1alpha1.ConfigReferent{
									Name:      "gitops-addon-config",
									Namespace: "test-cluster",
								},
							},
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon",
					Namespace: namespace,
				}, addon)
				require.NoError(t, err)

				// Only AddonDeploymentConfig when ArgoCD agent is disabled
				assert.Len(t, addon.Spec.Configs, 1)

				// Check that we only have AddonDeploymentConfig
				foundTemplate := false
				foundDeploymentConfig := false
				for _, config := range addon.Spec.Configs {
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addontemplates" {
						foundTemplate = true
					}
					if config.Group == "addon.open-cluster-management.io" && config.Resource == "addondeploymentconfigs" {
						foundDeploymentConfig = true
					}
				}
				assert.False(t, foundTemplate, "Should NOT have AddOnTemplate config when ArgoCD agent is disabled")
				assert.True(t, foundDeploymentConfig, "Should have AddonDeploymentConfig")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			err := reconciler.EnsureManagedClusterAddon(tt.namespace, tt.gitOpsCluster)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient, tt.namespace)
				}
			}
		})
	}
}

func TestGetGitOpsAddonStatus(t *testing.T) {
	tests := []struct {
		name                       string
		gitOpsCluster              *gitopsclusterV1beta1.GitOpsCluster
		expectedGitOpsAddon        bool
		expectedArgoCDAgentEnabled bool
	}{
		{
			name: "no GitOpsAddon spec",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{},
			},
			expectedGitOpsAddon:        false,
			expectedArgoCDAgentEnabled: false,
		},
		{
			name: "GitOpsAddon enabled, ArgoCD agent disabled",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: func(b bool) *bool { return &b }(false),
						},
					},
				},
			},
			expectedGitOpsAddon:        true,
			expectedArgoCDAgentEnabled: false,
		},
		{
			name: "both GitOpsAddon and ArgoCD agent enabled",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: func(b bool) *bool { return &b }(true),
						},
					},
				},
			},
			expectedGitOpsAddon:        true,
			expectedArgoCDAgentEnabled: true,
		},
		{
			name: "nil enabled fields default to false",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{},
					},
				},
			},
			expectedGitOpsAddon:        false,
			expectedArgoCDAgentEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &ReconcileGitOpsCluster{}

			gitopsEnabled, argoCDEnabled := reconciler.GetGitOpsAddonStatus(tt.gitOpsCluster)

			assert.Equal(t, tt.expectedGitOpsAddon, gitopsEnabled)
			assert.Equal(t, tt.expectedArgoCDAgentEnabled, argoCDEnabled)
		})
	}
}

func TestExtractVariablesFromGitOpsCluster(t *testing.T) {
	// The new behavior is that ExtractVariablesFromGitOpsCluster always populates:
	// 1. All default operator images from utils.DefaultOperatorImages
	// 2. GitOpsCluster spec overrides (e.g., GitOpsOperatorImage)
	// 3. ArgoCD agent variables from the spec

	t.Run("populates all default images plus GitOpsCluster spec overrides", func(t *testing.T) {
		reconciler := &ReconcileGitOpsCluster{}
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
					GitOpsOperatorImage: "custom-operator:v1.0",
					ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
						Enabled:       boolTruePtr,
						ServerAddress: "server.example.com",
						ServerPort:    "443",
						Mode:          "managed",
					},
				},
			},
		}

		managedVariables := make(map[string]string)
		reconciler.ExtractVariablesFromGitOpsCluster(gitOpsCluster, managedVariables)

		// Should have all default images EXCEPT hub-only vars (like ARGOCD_PRINCIPAL_IMAGE)
		for envKey := range utils.DefaultOperatorImages {
			if utils.IsHubOnlyEnvVar(envKey) {
				_, exists := managedVariables[envKey]
				assert.False(t, exists, "Hub-only var %s should NOT be present", envKey)
				continue
			}
			_, exists := managedVariables[envKey]
			assert.True(t, exists, "Expected %s to be present", envKey)
		}

		// GitOpsOperatorImage should be overridden by spec
		assert.Equal(t, "custom-operator:v1.0", managedVariables[utils.EnvGitOpsOperatorImage])

		// ArgoCD agent variables should be set
		assert.Equal(t, "true", managedVariables[utils.EnvArgoCDAgentEnabled])
		assert.Equal(t, "server.example.com", managedVariables[utils.EnvArgoCDAgentServerAddress])
		assert.Equal(t, "443", managedVariables[utils.EnvArgoCDAgentServerPort])
		assert.Equal(t, "managed", managedVariables[utils.EnvArgoCDAgentMode])
	})

	t.Run("uses defaults when no GitOpsAddon spec", func(t *testing.T) {
		reconciler := &ReconcileGitOpsCluster{}
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{},
		}

		managedVariables := make(map[string]string)
		reconciler.ExtractVariablesFromGitOpsCluster(gitOpsCluster, managedVariables)

		// Should have all default images EXCEPT hub-only vars
		for envKey, defaultValue := range utils.DefaultOperatorImages {
			if utils.IsHubOnlyEnvVar(envKey) {
				_, exists := managedVariables[envKey]
				assert.False(t, exists, "Hub-only var %s should NOT be present", envKey)
				continue
			}
			value, exists := managedVariables[envKey]
			assert.True(t, exists, "Expected %s to be present", envKey)
			assert.Equal(t, defaultValue, value, "Expected %s to have default value", envKey)
		}
	})

	t.Run("spec overrides take precedence over defaults", func(t *testing.T) {
		reconciler := &ReconcileGitOpsCluster{}
		customImage := "my-custom-registry/gitops-operator:custom"
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
					GitOpsOperatorImage: customImage,
				},
			},
		}

		managedVariables := make(map[string]string)
		reconciler.ExtractVariablesFromGitOpsCluster(gitOpsCluster, managedVariables)

		// Custom image should override the default
		assert.Equal(t, customImage, managedVariables[utils.EnvGitOpsOperatorImage])

		// Other images should still have defaults
		assert.Equal(t, utils.DefaultOperatorImages[utils.EnvArgoCDImage], managedVariables[utils.EnvArgoCDImage])
	})

	t.Run("proxy environment variables are extracted from hub environment", func(t *testing.T) {
		// Set proxy environment variables
		os.Setenv(utils.EnvHTTPProxy, "http://proxy.example.com:8080")
		os.Setenv(utils.EnvHTTPSProxy, "https://proxy.example.com:8443")
		os.Setenv(utils.EnvNoProxy, "localhost,127.0.0.1,.example.com")
		defer func() {
			os.Unsetenv(utils.EnvHTTPProxy)
			os.Unsetenv(utils.EnvHTTPSProxy)
			os.Unsetenv(utils.EnvNoProxy)
		}()

		reconciler := &ReconcileGitOpsCluster{}
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{},
		}

		managedVariables := make(map[string]string)
		reconciler.ExtractVariablesFromGitOpsCluster(gitOpsCluster, managedVariables)

		// Proxy variables should be extracted from environment
		assert.Equal(t, "http://proxy.example.com:8080", managedVariables[utils.EnvHTTPProxy])
		assert.Equal(t, "https://proxy.example.com:8443", managedVariables[utils.EnvHTTPSProxy])
		assert.Equal(t, "localhost,127.0.0.1,.example.com", managedVariables[utils.EnvNoProxy])
	})

	t.Run("image environment variables override defaults", func(t *testing.T) {
		// Set an image environment variable
		customImage := "custom.registry.io/gitops-operator:env-override"
		os.Setenv(utils.EnvGitOpsOperatorImage, customImage)
		defer os.Unsetenv(utils.EnvGitOpsOperatorImage)

		reconciler := &ReconcileGitOpsCluster{}
		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{},
		}

		managedVariables := make(map[string]string)
		reconciler.ExtractVariablesFromGitOpsCluster(gitOpsCluster, managedVariables)

		// Environment variable should override default
		assert.Equal(t, customImage, managedVariables[utils.EnvGitOpsOperatorImage])
	})
}

func TestExtractArgoCDAgentVariables(t *testing.T) {
	tests := []struct {
		name             string
		argoCDAgent      *gitopsclusterV1beta1.ArgoCDAgentSpec
		initialVariables map[string]string
		expectedVars     map[string]string
	}{
		{
			name: "extract all ArgoCD agent variables",
			argoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				ServerAddress: "new-server.example.com",
				ServerPort:    "8443",
				Mode:          "autonomous",
			},
			initialVariables: map[string]string{
				"EXISTING_VAR": "existing-value",
			},
			expectedVars: map[string]string{
				"EXISTING_VAR":                "existing-value",
				"ARGOCD_AGENT_SERVER_ADDRESS": "new-server.example.com",
				"ARGOCD_AGENT_SERVER_PORT":    "8443",
				"ARGOCD_AGENT_MODE":           "autonomous",
			},
		},
		{
			name: "extract only server address variable",
			argoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				ServerAddress: "server.example.com",
				// Other fields are empty
			},
			initialVariables: map[string]string{
				"EXISTING_VAR": "existing-value",
			},
			expectedVars: map[string]string{
				"EXISTING_VAR":                "existing-value",
				"ARGOCD_AGENT_SERVER_ADDRESS": "server.example.com",
			},
		},
		{
			name:        "nil spec - no changes",
			argoCDAgent: nil,
			initialVariables: map[string]string{
				"EXISTING_VAR": "existing-value",
			},
			expectedVars: map[string]string{
				"EXISTING_VAR": "existing-value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &ReconcileGitOpsCluster{}

			managedVariables := make(map[string]string)
			for k, v := range tt.initialVariables {
				managedVariables[k] = v
			}

			reconciler.extractArgoCDAgentVariables(tt.argoCDAgent, managedVariables)

			assert.Equal(t, tt.expectedVars, managedVariables)
		})
	}
}

