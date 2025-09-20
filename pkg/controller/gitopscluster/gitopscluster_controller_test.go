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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestValidateArgoCDAgentSpec(t *testing.T) {
	reconciler := &ReconcileGitOpsCluster{}

	tests := []struct {
		name          string
		argoCDAgent   *gitopsclusterV1beta1.ArgoCDAgentSpec
		expectedError bool
		errorContains string
	}{
		{
			name:        "nil spec is valid",
			argoCDAgent: nil,
		},
		{
			name: "valid managed mode",
			argoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Mode: "managed",
			},
		},
		{
			name: "valid autonomous mode",
			argoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Mode: "autonomous",
			},
		},
		{
			name: "empty mode is valid",
			argoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Mode: "",
			},
		},
		{
			name: "invalid mode",
			argoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Mode: "invalid",
			},
			expectedError: true,
			errorContains: "invalid Mode 'invalid'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.ValidateArgoCDAgentSpec(tt.argoCDAgent)

			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateGitOpsAddonSpec(t *testing.T) {
	reconciler := &ReconcileGitOpsCluster{}

	tests := []struct {
		name          string
		gitOpsAddon   *gitopsclusterV1beta1.GitOpsAddonSpec
		expectedError bool
		errorContains string
	}{
		{
			name:        "nil spec is valid",
			gitOpsAddon: nil,
		},
		{
			name: "valid All-Namespaces scope",
			gitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				ReconcileScope: "All-Namespaces",
			},
		},
		{
			name: "valid Single-Namespace scope",
			gitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				ReconcileScope: "Single-Namespace",
			},
		},
		{
			name: "valid Install action",
			gitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Action: "Install",
			},
		},
		{
			name: "valid Delete-Operator action",
			gitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Action: "Delete-Operator",
			},
		},
		{
			name: "invalid reconcile scope",
			gitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				ReconcileScope: "Invalid-Scope",
			},
			expectedError: true,
			errorContains: "invalid ReconcileScope 'Invalid-Scope'",
		},
		{
			name: "invalid action",
			gitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Action: "Invalid-Action",
			},
			expectedError: true,
			errorContains: "invalid Action 'Invalid-Action'",
		},
		{
			name: "invalid nested ArgoCDAgent",
			gitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Mode: "invalid-mode",
				},
			},
			expectedError: true,
			errorContains: "ArgoCDAgent validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.ValidateGitOpsAddonSpec(tt.gitOpsAddon)

			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetAllManagedClusterSecretsInArgo(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectedSecrets int
	}{
		{
			name: "find ACM managed cluster secrets",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster1-cluster-secret",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"apps.open-cluster-management.io/acm-cluster": "true",
							"argocd.argoproj.io/secret-type":              "cluster",
						},
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster2-cluster-secret",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"apps.open-cluster-management.io/acm-cluster": "true",
							"argocd.argoproj.io/secret-type":              "cluster",
						},
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "non-acm-secret",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type": "cluster",
						},
					},
				},
			},
			expectedSecrets: 2,
		},
		{
			name:            "no ACM managed cluster secrets",
			existingObjects: []client.Object{},
			expectedSecrets: 0,
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

			secretList, err := reconciler.GetAllManagedClusterSecretsInArgo()

			assert.NoError(t, err)
			assert.Len(t, secretList.Items, tt.expectedSecrets)
		})
	}
}

func TestGetAllNonAcmManagedClusterSecretsInArgo(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name             string
		argoNamespace    string
		existingObjects  []client.Object
		expectedClusters map[string]int
	}{
		{
			name:          "find non-ACM cluster secrets",
			argoNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "manual-cluster-secret",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type": "cluster",
						},
					},
					Data: map[string][]byte{
						"name": []byte("manual-cluster"),
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "acm-cluster-secret",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"apps.open-cluster-management.io/acm-cluster": "true",
							"argocd.argoproj.io/secret-type":              "cluster",
						},
					},
					Data: map[string][]byte{
						"name": []byte("acm-cluster"),
					},
				},
			},
			expectedClusters: map[string]int{
				"manual-cluster": 1,
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

			secretMap, err := reconciler.GetAllNonAcmManagedClusterSecretsInArgo(tt.argoNamespace)

			assert.NoError(t, err)

			for clusterName, expectedCount := range tt.expectedClusters {
				secrets, exists := secretMap[clusterName]
				assert.True(t, exists, "Expected cluster %s to exist in secret map", clusterName)
				assert.Len(t, secrets, expectedCount, "Expected %d secrets for cluster %s", expectedCount, clusterName)
			}
		})
	}
}

func TestGetAllGitOpsClusters(t *testing.T) {
	scheme := runtime.NewScheme()
	err := gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectedCount   int
	}{
		{
			name: "find multiple GitOpsClusters",
			existingObjects: []client.Object{
				&gitopsclusterV1beta1.GitOpsCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops1",
						Namespace: "ns1",
					},
				},
				&gitopsclusterV1beta1.GitOpsCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops2",
						Namespace: "ns2",
					},
				},
			},
			expectedCount: 2,
		},
		{
			name:          "no GitOpsClusters",
			expectedCount: 0,
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

			gitOpsList, err := reconciler.GetAllGitOpsClusters()

			assert.NoError(t, err)
			assert.Len(t, gitOpsList.Items, tt.expectedCount)
		})
	}
}

func TestCleanupOrphanSecrets(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		existingObjects []client.Object
		orphanSecrets   map[types.NamespacedName]string
		expectedSuccess bool
		validateFunc    func(t *testing.T, c client.Client)
	}{
		{
			name: "successfully delete orphan secrets",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphan-secret",
						Namespace: "test-ns",
					},
				},
			},
			orphanSecrets: map[types.NamespacedName]string{
				{Name: "orphan-secret", Namespace: "test-ns"}: "test-ns/orphan-secret",
			},
			expectedSuccess: true,
			validateFunc: func(t *testing.T, c client.Client) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "orphan-secret",
					Namespace: "test-ns",
				}, secret)
				assert.Error(t, err, "Secret should be deleted")
			},
		},
		{
			name: "handle non-existent orphan secrets gracefully",
			orphanSecrets: map[types.NamespacedName]string{
				{Name: "non-existent", Namespace: "test-ns"}: "test-ns/non-existent",
			},
			expectedSuccess: true,
		},
		{
			name:            "empty orphan list",
			orphanSecrets:   map[types.NamespacedName]string{},
			expectedSuccess: true,
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

			success := reconciler.cleanupOrphanSecrets(tt.orphanSecrets)

			assert.Equal(t, tt.expectedSuccess, success)
			if tt.validateFunc != nil {
				tt.validateFunc(t, fakeClient)
			}
		})
	}
}

func TestCreateManagedClusterSecretInArgo(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)
	err = spokeclusterv1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name                      string
		argoNamespace             string
		managedClusterSecret      *v1.Secret
		managedCluster            *spokeclusterv1.ManagedCluster
		createBlankClusterSecrets bool
		expectedError             bool
		validateFunc              func(t *testing.T, secret *v1.Secret)
	}{
		{
			name:          "create blank cluster secret",
			argoNamespace: "openshift-gitops",
			managedClusterSecret: &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-cluster-secret",
					Namespace: "test-cluster",
				},
			},
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Labels: map[string]string{
						"environment": "test",
					},
				},
			},
			createBlankClusterSecrets: true,
			validateFunc: func(t *testing.T, secret *v1.Secret) {
				assert.Equal(t, "test-cluster-application-manager-cluster-secret", secret.Name)
				assert.Equal(t, "openshift-gitops", secret.Namespace)
				assert.Equal(t, "test-cluster", secret.StringData["name"])
				assert.Equal(t, "https://test-cluster-control-plane", secret.StringData["server"])

				// Check that cluster labels are copied
				assert.Equal(t, "test", secret.Labels["environment"])
				assert.Equal(t, "true", secret.Labels["apps.open-cluster-management.io/acm-cluster"])
			},
		},
		{
			name:          "create secret from existing cluster secret",
			argoNamespace: "openshift-gitops",
			managedClusterSecret: &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-cluster-secret",
					Namespace: "test-cluster",
					Labels: map[string]string{
						"apps.open-cluster-management.io/cluster-name":   "test-cluster",
						"apps.open-cluster-management.io/cluster-server": "api.test-cluster.com",
					},
				},
				Data: map[string][]byte{
					"config": []byte(`{"bearerToken":"test-token","tlsClientConfig":{"insecure":true}}`),
					"name":   []byte("test-cluster"),
					"server": []byte("https://api.test-cluster.com:6443"),
				},
			},
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
			},
			createBlankClusterSecrets: false,
			validateFunc: func(t *testing.T, secret *v1.Secret) {
				assert.Equal(t, "test-cluster-cluster-secret", secret.Name)
				assert.Equal(t, "openshift-gitops", secret.Namespace)
				assert.NotEmpty(t, secret.StringData["config"])
				assert.Equal(t, "test-cluster", secret.StringData["name"])
				assert.Equal(t, "https://api.test-cluster.com:6443", secret.StringData["server"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			secret, err := reconciler.CreateManagedClusterSecretInArgo(
				tt.argoNamespace, tt.managedClusterSecret, tt.managedCluster, tt.createBlankClusterSecrets)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, secret)
				}
			}
		})
	}
}

func TestAutoSetCreateBlankClusterSecretsWhenGitOpsAddonEnabled(t *testing.T) {
	scheme := runtime.NewScheme()
	err := gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name                              string
		gitOpsCluster                     *gitopsclusterV1beta1.GitOpsCluster
		expectedCreateBlankClusterSecrets *bool
		expectedUpdate                    bool
	}{
		{
			name: "gitopsAddon enabled, createBlankClusterSecrets nil - should set to true",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
					},
					CreateBlankClusterSecrets: nil,
				},
			},
			expectedCreateBlankClusterSecrets: func(b bool) *bool { return &b }(true),
			expectedUpdate:                    true,
		},
		{
			name: "gitopsAddon enabled, createBlankClusterSecrets false - should set to true",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
					},
					CreateBlankClusterSecrets: func(b bool) *bool { return &b }(false),
				},
			},
			expectedCreateBlankClusterSecrets: func(b bool) *bool { return &b }(true),
			expectedUpdate:                    true,
		},
		{
			name: "gitopsAddon enabled, createBlankClusterSecrets already true - no update needed",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(true),
					},
					CreateBlankClusterSecrets: func(b bool) *bool { return &b }(true),
				},
			},
			expectedCreateBlankClusterSecrets: func(b bool) *bool { return &b }(true),
			expectedUpdate:                    false,
		},
		{
			name: "gitopsAddon disabled - no changes",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: func(b bool) *bool { return &b }(false),
					},
					CreateBlankClusterSecrets: func(b bool) *bool { return &b }(false),
				},
			},
			expectedCreateBlankClusterSecrets: func(b bool) *bool { return &b }(false),
			expectedUpdate:                    false,
		},
		{
			name: "no gitopsAddon spec - no changes",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					CreateBlankClusterSecrets: func(b bool) *bool { return &b }(false),
				},
			},
			expectedCreateBlankClusterSecrets: func(b bool) *bool { return &b }(false),
			expectedUpdate:                    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a copy to simulate what the reconciler does
			instance := tt.gitOpsCluster.DeepCopy()

			// Apply the auto-set logic
			needsUpdate := false
			if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.Enabled != nil && *instance.Spec.GitOpsAddon.Enabled {
				if instance.Spec.CreateBlankClusterSecrets == nil || !*instance.Spec.CreateBlankClusterSecrets {
					trueValue := true
					instance.Spec.CreateBlankClusterSecrets = &trueValue
					needsUpdate = true
				}
			}

			// Validate results
			assert.Equal(t, tt.expectedUpdate, needsUpdate, "update flag should match expected")

			if tt.expectedCreateBlankClusterSecrets != nil {
				require.NotNil(t, instance.Spec.CreateBlankClusterSecrets, "createBlankClusterSecrets should not be nil")
				assert.Equal(t, *tt.expectedCreateBlankClusterSecrets, *instance.Spec.CreateBlankClusterSecrets, "createBlankClusterSecrets value should match expected")
			} else {
				assert.Nil(t, instance.Spec.CreateBlankClusterSecrets, "createBlankClusterSecrets should be nil")
			}
		})
	}
}

func TestUnionSecretData(t *testing.T) {
	tests := []struct {
		name           string
		newSecret      *v1.Secret
		existingSecret *v1.Secret
		validateFunc   func(t *testing.T, result *v1.Secret)
	}{
		{
			name: "merge labels and annotations",
			newSecret: &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"new-label": "new-value",
						"shared":    "new-shared-value",
					},
					Annotations: map[string]string{
						"new-annotation": "new-value",
					},
				},
				StringData: map[string]string{
					"new-key": "new-value",
				},
			},
			existingSecret: &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"existing-label": "existing-value",
						"shared":         "existing-shared-value",
					},
					Annotations: map[string]string{
						"existing-annotation":                              "existing-value",
						"kubectl.kubernetes.io/last-applied-configuration": "should-be-ignored",
					},
				},
				Data: map[string][]byte{
					"existing-key": []byte("existing-value"),
				},
			},
			validateFunc: func(t *testing.T, result *v1.Secret) {
				// Labels should be merged with new values taking precedence
				assert.Equal(t, "new-value", result.Labels["new-label"])
				assert.Equal(t, "existing-value", result.Labels["existing-label"])
				assert.Equal(t, "new-shared-value", result.Labels["shared"])

				// Annotations should be merged, excluding kubectl annotation
				assert.Equal(t, "new-value", result.Annotations["new-annotation"])
				assert.Equal(t, "existing-value", result.Annotations["existing-annotation"])
				assert.NotContains(t, result.Annotations, "kubectl.kubernetes.io/last-applied-configuration")

				// Data should be merged
				assert.Equal(t, "new-value", result.StringData["new-key"])
				assert.Equal(t, "existing-value", result.StringData["existing-key"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := unionSecretData(tt.newSecret, tt.existingSecret)

			if tt.validateFunc != nil {
				tt.validateFunc(t, result)
			}
		})
	}
}

func TestUpdateGitOpsClusterConditions(t *testing.T) {
	tests := []struct {
		name             string
		instance         *gitopsclusterV1beta1.GitOpsCluster
		phase            string
		message          string
		conditionUpdates map[string]ConditionUpdate
		validateFunc     func(t *testing.T, instance *gitopsclusterV1beta1.GitOpsCluster)
	}{
		{
			name: "update multiple conditions",
			instance: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
			},
			phase:   "successful",
			message: "All components are ready",
			conditionUpdates: map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterPlacementResolved: {
					Status:  metav1.ConditionTrue,
					Reason:  gitopsclusterV1beta1.ReasonSuccess,
					Message: "Placement resolved successfully",
				},
				gitopsclusterV1beta1.GitOpsClusterClustersRegistered: {
					Status:  metav1.ConditionTrue,
					Reason:  gitopsclusterV1beta1.ReasonSuccess,
					Message: "Clusters registered to ArgoCD",
				},
			},
			validateFunc: func(t *testing.T, instance *gitopsclusterV1beta1.GitOpsCluster) {
				assert.Equal(t, "successful", instance.Status.Phase)
				assert.Equal(t, "All components are ready", instance.Status.Message)
				assert.NotNil(t, instance.Status.LastUpdateTime)

				// Check that conditions were set
				assert.True(t, instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterPlacementResolved))
				assert.True(t, instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterClustersRegistered))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &ReconcileGitOpsCluster{}

			reconciler.updateGitOpsClusterConditions(tt.instance, tt.phase, tt.message, tt.conditionUpdates)

			if tt.validateFunc != nil {
				tt.validateFunc(t, tt.instance)
			}
		})
	}
}

func TestUpdateReadyCondition(t *testing.T) {
	tests := []struct {
		name           string
		instance       *gitopsclusterV1beta1.GitOpsCluster
		expectedStatus metav1.ConditionStatus
		expectedReason string
	}{
		{
			name: "all conditions true without ArgoCD agent - ready",
			instance: &gitopsclusterV1beta1.GitOpsCluster{
				Status: gitopsclusterV1beta1.GitOpsClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
							Status: metav1.ConditionTrue,
						},
					},
				},
			},
			expectedStatus: metav1.ConditionTrue,
			expectedReason: gitopsclusterV1beta1.ReasonSuccess,
		},
		{
			name: "one condition false - not ready",
			instance: &gitopsclusterV1beta1.GitOpsCluster{
				Status: gitopsclusterV1beta1.GitOpsClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
							Status: metav1.ConditionFalse,
						},
					},
				},
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: gitopsclusterV1beta1.ReasonClusterRegistrationFailed,
		},
		{
			name: "all conditions true with ArgoCD agent enabled - ready",
			instance: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled:        &[]bool{true}[0],
							PropagateHubCA: &[]bool{true}[0],
						},
					},
				},
				Status: gitopsclusterV1beta1.GitOpsClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterCertificatesReady,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied,
							Status: metav1.ConditionTrue,
						},
					},
				},
			},
			expectedStatus: metav1.ConditionTrue,
			expectedReason: gitopsclusterV1beta1.ReasonSuccess,
		},
		{
			name: "ArgoCD agent prerequisites not ready - not ready",
			instance: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: &[]bool{true}[0],
						},
					},
				},
				Status: gitopsclusterV1beta1.GitOpsClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady,
							Status: metav1.ConditionFalse,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterCertificatesReady,
							Status: metav1.ConditionTrue,
						},
					},
				},
			},
			expectedStatus: metav1.ConditionFalse,
			expectedReason: gitopsclusterV1beta1.ReasonClusterRegistrationFailed,
		},
		{
			name: "ArgoCD agent disabled with prerequisites condition not required - ready",
			instance: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: &[]bool{false}[0],
						},
					},
				},
				Status: gitopsclusterV1beta1.GitOpsClusterStatus{
					Conditions: []metav1.Condition{
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
							Status: metav1.ConditionTrue,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady,
							Status: metav1.ConditionTrue,
							Reason: gitopsclusterV1beta1.ReasonNotRequired,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady,
							Status: metav1.ConditionTrue,
							Reason: gitopsclusterV1beta1.ReasonDisabled,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterCertificatesReady,
							Status: metav1.ConditionTrue,
							Reason: gitopsclusterV1beta1.ReasonNotRequired,
						},
						{
							Type:   gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied,
							Status: metav1.ConditionTrue,
							Reason: gitopsclusterV1beta1.ReasonNotRequired,
						},
					},
				},
			},
			expectedStatus: metav1.ConditionTrue,
			expectedReason: gitopsclusterV1beta1.ReasonSuccess,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &ReconcileGitOpsCluster{}

			reconciler.updateReadyCondition(tt.instance)

			readyCondition := tt.instance.GetCondition(gitopsclusterV1beta1.GitOpsClusterReady)
			assert.NotNil(t, readyCondition)
			assert.Equal(t, tt.expectedStatus, readyCondition.Status)
			assert.Equal(t, tt.expectedReason, readyCondition.Reason)
		})
	}
}

func TestPlacementDecisionMapper(t *testing.T) {
	scheme := runtime.NewScheme()
	err := gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)
	err = clusterv1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name              string
		placementDecision *clusterv1beta1.PlacementDecision
		existingObjects   []client.Object
		expectedRequests  int
	}{
		{
			name: "placement decision maps to GitOpsCluster",
			placementDecision: &clusterv1beta1.PlacementDecision{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-placement-decision",
					Namespace: "test-ns",
					Labels: map[string]string{
						"cluster.open-cluster-management.io/placement": "test-placement",
					},
				},
			},
			existingObjects: []client.Object{
				&gitopsclusterV1beta1.GitOpsCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-gitops",
						Namespace: "test-ns",
					},
					Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
						PlacementRef: &v1.ObjectReference{
							Name: "test-placement",
						},
					},
				},
			},
			expectedRequests: 1,
		},
		{
			name: "no matching GitOpsCluster",
			placementDecision: &clusterv1beta1.PlacementDecision{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-placement-decision",
					Namespace: "test-ns",
					Labels: map[string]string{
						"cluster.open-cluster-management.io/placement": "different-placement",
					},
				},
			},
			existingObjects: []client.Object{
				&gitopsclusterV1beta1.GitOpsCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-gitops",
						Namespace: "test-ns",
					},
					Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
						PlacementRef: &v1.ObjectReference{
							Name: "test-placement",
						},
					},
				},
			},
			expectedRequests: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			mapper := &placementDecisionMapper{Client: fakeClient}

			requests := mapper.Map(context.TODO(), tt.placementDecision)

			assert.Len(t, requests, tt.expectedRequests)
		})
	}
}

func TestGeneratePlacementYamlString(t *testing.T) {
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-ns",
			UID:       "test-uid",
		},
	}

	yamlString := generatePlacementYamlString(gitOpsCluster)

	assert.Contains(t, yamlString, "name: test-gitops-policy-local-placement")
	assert.Contains(t, yamlString, "namespace: test-ns")
	assert.Contains(t, yamlString, "name: test-gitops")
	assert.Contains(t, yamlString, "uid: test-uid")
	assert.Contains(t, yamlString, "kind: Placement")
	assert.Contains(t, yamlString, "local-cluster")
}

func TestGeneratePlacementBindingYamlString(t *testing.T) {
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-ns",
			UID:       "test-uid",
		},
	}

	yamlString := generatePlacementBindingYamlString(gitOpsCluster)

	assert.Contains(t, yamlString, "name: test-gitops-policy-local-placement-binding")
	assert.Contains(t, yamlString, "namespace: test-ns")
	assert.Contains(t, yamlString, "name: test-gitops")
	assert.Contains(t, yamlString, "uid: test-uid")
	assert.Contains(t, yamlString, "kind: PlacementBinding")
	assert.Contains(t, yamlString, "name: test-gitops-policy-local-placement")
	assert.Contains(t, yamlString, "name: test-gitops-policy")
}

func TestGeneratePolicyTemplateYamlString(t *testing.T) {
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-ns",
			UID:       "test-uid",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			PlacementRef: &v1.ObjectReference{
				Name: "test-placement",
			},
			ManagedServiceAccountRef: "test-msa",
		},
	}

	yamlString := generatePolicyTemplateYamlString(gitOpsCluster)

	assert.Contains(t, yamlString, "name: test-gitops-policy")
	assert.Contains(t, yamlString, "namespace: test-ns")
	assert.Contains(t, yamlString, "name: test-gitops")
	assert.Contains(t, yamlString, "uid: test-uid")
	assert.Contains(t, yamlString, "kind: Policy")
	assert.Contains(t, yamlString, "test-placement")
	assert.Contains(t, yamlString, "test-msa")
	assert.Contains(t, yamlString, "ManagedServiceAccount")
	assert.Contains(t, yamlString, "ClusterPermission")
}

// Mock implementations for testing

type mockDynamicClient struct {
	client.Client
}

type mockKubernetesClient struct {
	kubernetes.Interface
}

func TestNewReconciler(t *testing.T) {
	// This test would require a more complex setup with a real manager
	// For now, we'll test the basic structure
	reconciler := &ReconcileGitOpsCluster{}
	assert.NotNil(t, reconciler)
}

func TestReconcileRequest(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)
	err = gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)
	err = spokeclusterv1.AddToScheme(scheme)
	require.NoError(t, err)
	err = clusterv1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		request         reconcile.Request
		existingObjects []client.Object
		expectedResult  reconcile.Result
		expectedError   bool
	}{
		{
			name: "reconcile with empty cluster list",
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
			},
			existingObjects: []client.Object{},
			expectedResult:  reconcile.Result{},
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

			result, err := reconciler.Reconcile(context.TODO(), tt.request)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}
