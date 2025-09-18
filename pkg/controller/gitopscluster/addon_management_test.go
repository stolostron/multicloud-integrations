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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
						GitOpsImage:         "test-gitops-image:latest",
						RedisImage:          "test-redis-image:latest",
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Image:         "test-agent-image:latest",
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
				expectedVars := map[string]string{
					"ARGOCD_AGENT_ENABLED":        "true",
					"GITOPS_OPERATOR_IMAGE":       "test-operator-image:latest",
					"GITOPS_IMAGE":                "test-gitops-image:latest",
					"REDIS_IMAGE":                 "test-redis-image:latest",
					"ARGOCD_AGENT_IMAGE":          "test-agent-image:latest",
					"ARGOCD_AGENT_SERVER_ADDRESS": "test-server.com",
					"ARGOCD_AGENT_SERVER_PORT":    "443",
					"ARGOCD_AGENT_MODE":           "managed",
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
			name:      "update existing AddOnDeploymentConfig preserving user variables",
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
							Image:         "updated-agent-image:latest",
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
				// Managed variables should be updated
				assert.Equal(t, "updated-operator-image:latest", varMap["GITOPS_OPERATOR_IMAGE"], "Managed variable should be updated")
				assert.Equal(t, "updated-agent-image:latest", varMap["ARGOCD_AGENT_IMAGE"], "Managed variable should be updated")
				assert.Equal(t, "updated-server.com", varMap["ARGOCD_AGENT_SERVER_ADDRESS"], "Managed variable should be updated")
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
					Spec: addonv1alpha1.ManagedClusterAddOnSpec{
						InstallNamespace: "open-cluster-management-agent-addon",
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
						InstallNamespace: "open-cluster-management-agent-addon",
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
			name:      "create new ManagedClusterAddOn",
			namespace: "test-cluster",
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      "gitops-addon",
					Namespace: namespace,
				}, addon)
				require.NoError(t, err)

				assert.Equal(t, "open-cluster-management-agent-addon", addon.Spec.InstallNamespace)
				assert.Len(t, addon.Spec.Configs, 1)

				config := addon.Spec.Configs[0]
				assert.Equal(t, "addon.open-cluster-management.io", config.Group)
				assert.Equal(t, "addondeploymentconfigs", config.Resource)
				assert.Equal(t, "gitops-addon-config", config.Name)
				assert.Equal(t, namespace, config.Namespace)
			},
		},
		{
			name:      "update existing ManagedClusterAddOn with missing config",
			namespace: "test-cluster",
			existingObjects: []client.Object{
				&addonv1alpha1.ManagedClusterAddOn{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.ManagedClusterAddOnSpec{
						InstallNamespace: "open-cluster-management-agent-addon",
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

				assert.Len(t, addon.Spec.Configs, 1)
				config := addon.Spec.Configs[0]
				assert.Equal(t, "gitops-addon-config", config.Name)
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

			err := reconciler.EnsureManagedClusterAddon(tt.namespace)

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
	tests := []struct {
		name             string
		gitOpsCluster    *gitopsclusterV1beta1.GitOpsCluster
		initialVariables map[string]string
		expectedVars     map[string]string
	}{
		{
			name: "extract all GitOpsAddon variables",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						GitOpsOperatorImage:     "operator-image:v1.0",
						GitOpsImage:             "gitops-image:v1.0",
						RedisImage:              "redis-image:v1.0",
						GitOpsOperatorNamespace: "gitops-operator-ns",
						GitOpsNamespace:         "gitops-ns",
						ReconcileScope:          "All-Namespaces",
						Action:                  "Install",
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Image:         "agent-image:v1.0",
							ServerAddress: "server.example.com",
							ServerPort:    "443",
							Mode:          "managed",
						},
					},
				},
			},
			initialVariables: map[string]string{
				"ARGOCD_AGENT_ENABLED": "true",
			},
			expectedVars: map[string]string{
				"ARGOCD_AGENT_ENABLED":        "true",
				"GITOPS_OPERATOR_IMAGE":       "operator-image:v1.0",
				"GITOPS_IMAGE":                "gitops-image:v1.0",
				"REDIS_IMAGE":                 "redis-image:v1.0",
				"GITOPS_OPERATOR_NAMESPACE":   "gitops-operator-ns",
				"GITOPS_NAMESPACE":            "gitops-ns",
				"RECONCILE_SCOPE":             "All-Namespaces",
				"ACTION":                      "Install",
				"ARGOCD_AGENT_IMAGE":          "agent-image:v1.0",
				"ARGOCD_AGENT_SERVER_ADDRESS": "server.example.com",
				"ARGOCD_AGENT_SERVER_PORT":    "443",
				"ARGOCD_AGENT_MODE":           "managed",
			},
		},
		{
			name: "extract partial variables only if non-empty",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						GitOpsOperatorImage: "operator-image:v1.0",
						// Other fields are empty/nil
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "server.example.com",
							// Other fields are empty
						},
					},
				},
			},
			initialVariables: map[string]string{
				"ARGOCD_AGENT_ENABLED": "true",
			},
			expectedVars: map[string]string{
				"ARGOCD_AGENT_ENABLED":        "true",
				"GITOPS_OPERATOR_IMAGE":       "operator-image:v1.0",
				"ARGOCD_AGENT_SERVER_ADDRESS": "server.example.com",
			},
		},
		{
			name: "no GitOpsAddon spec - no additional variables",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{},
			},
			initialVariables: map[string]string{
				"ARGOCD_AGENT_ENABLED": "true",
			},
			expectedVars: map[string]string{
				"ARGOCD_AGENT_ENABLED": "true",
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

			reconciler.ExtractVariablesFromGitOpsCluster(tt.gitOpsCluster, managedVariables)

			assert.Equal(t, tt.expectedVars, managedVariables)
		})
	}
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
				Image:         "agent-image:v2.0",
				ServerAddress: "new-server.example.com",
				ServerPort:    "8443",
				Mode:          "autonomous",
			},
			initialVariables: map[string]string{
				"EXISTING_VAR": "existing-value",
			},
			expectedVars: map[string]string{
				"EXISTING_VAR":                "existing-value",
				"ARGOCD_AGENT_IMAGE":          "agent-image:v2.0",
				"ARGOCD_AGENT_SERVER_ADDRESS": "new-server.example.com",
				"ARGOCD_AGENT_SERVER_PORT":    "8443",
				"ARGOCD_AGENT_MODE":           "autonomous",
			},
		},
		{
			name: "extract only non-empty variables",
			argoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Image: "agent-image:v2.0",
				// Other fields are empty
			},
			initialVariables: map[string]string{
				"EXISTING_VAR": "existing-value",
			},
			expectedVars: map[string]string{
				"EXISTING_VAR":       "existing-value",
				"ARGOCD_AGENT_IMAGE": "agent-image:v2.0",
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
