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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetRoleDuck(t *testing.T) {
	namespace := "test-namespace"
	role := getRoleDuck(namespace)

	assert.Equal(t, namespace+RoleSuffix, role.Name)
	assert.Equal(t, namespace, role.Namespace)

	// Verify role rules
	assert.Len(t, role.Rules, 2)

	// Check placementrules rule
	placementRuleRule := role.Rules[0]
	assert.Equal(t, []string{"apps.open-cluster-management.io"}, placementRuleRule.APIGroups)
	assert.Equal(t, []string{"placementrules"}, placementRuleRule.Resources)
	assert.Equal(t, []string{"list"}, placementRuleRule.Verbs)

	// Check placementdecisions rule
	placementDecisionRule := role.Rules[1]
	assert.Equal(t, []string{"cluster.open-cluster-management.io"}, placementDecisionRule.APIGroups)
	assert.Equal(t, []string{"placementdecisions"}, placementDecisionRule.Resources)
	assert.Equal(t, []string{"list"}, placementDecisionRule.Verbs)
}

func TestGetAppSetServiceAccountName(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		namespace       string
		existingObjects []client.Object
		expectedName    string
	}{
		{
			name:      "find service account with argocd-applicationset label",
			namespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-applicationset-controller",
						Namespace: utils.GitOpsNamespace,
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd-applicationset",
						},
					},
				},
			},
			expectedName: "openshift-gitops-applicationset-controller",
		},
		{
			name:      "find service account with argocd label",
			namespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "custom-applicationset-controller",
						Namespace: utils.GitOpsNamespace,
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd",
						},
					},
				},
			},
			expectedName: "custom-applicationset-controller",
		},
		{
			name:      "no matching service account - return default",
			namespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-service-account",
						Namespace: utils.GitOpsNamespace,
					},
				},
			},
			expectedName: "openshift-gitops-applicationset-controller",
		},
		{
			name:         "empty namespace - return default",
			namespace:    utils.GitOpsNamespace,
			expectedName: "openshift-gitops-applicationset-controller",
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

			saName := reconciler.getAppSetServiceAccountName(tt.namespace)
			assert.Equal(t, tt.expectedName, saName)
		})
	}
}

func TestGetRoleBindingDuck(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	namespace := "test-namespace"

	// Mock service account lookup
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-applicationset-controller",
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/part-of": "argocd-applicationset",
				},
			},
		}).
		Build()

	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	roleBinding := reconciler.getRoleBindingDuck(namespace)

	assert.Equal(t, namespace+RoleSuffix, roleBinding.Name)
	assert.Equal(t, namespace, roleBinding.Namespace)

	// Verify RoleRef
	assert.Equal(t, "rbac.authorization.k8s.io", roleBinding.RoleRef.APIGroup)
	assert.Equal(t, "Role", roleBinding.RoleRef.Kind)
	assert.Equal(t, namespace+RoleSuffix, roleBinding.RoleRef.Name)

	// Verify Subjects
	assert.Len(t, roleBinding.Subjects, 1)
	subject := roleBinding.Subjects[0]
	assert.Equal(t, "ServiceAccount", subject.Kind)
	assert.Equal(t, "test-applicationset-controller", subject.Name)
	assert.Equal(t, namespace, subject.Namespace)
}

func TestGetConfigMapDuck(t *testing.T) {
	tests := []struct {
		name          string
		configMapName string
		namespace     string
		apiVersion    string
		kind          string
		expectedData  map[string]string
	}{
		{
			name:          "create placementrules config map",
			configMapName: configMapNameOld,
			namespace:     "test-ns",
			apiVersion:    "apps.open-cluster-management.io/v1",
			kind:          "placementrules",
			expectedData: map[string]string{
				"apiVersion":    "apps.open-cluster-management.io/v1",
				"kind":          "placementrules",
				"statusListKey": "decisions",
				"matchKey":      "clusterName",
			},
		},
		{
			name:          "create placementdecisions config map",
			configMapName: configMapNameNew,
			namespace:     "test-ns",
			apiVersion:    "cluster.open-cluster-management.io/v1beta1",
			kind:          "placementdecisions",
			expectedData: map[string]string{
				"apiVersion":    "cluster.open-cluster-management.io/v1beta1",
				"kind":          "placementdecisions",
				"statusListKey": "decisions",
				"matchKey":      "clusterName",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configMap := getConfigMapDuck(tt.configMapName, tt.namespace, tt.apiVersion, tt.kind)

			assert.Equal(t, tt.configMapName, configMap.Name)
			assert.Equal(t, tt.namespace, configMap.Namespace)
			assert.Equal(t, tt.expectedData, configMap.Data)
		})
	}
}

func TestCreateApplicationSetConfigMaps(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
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
			name:      "create both config maps successfully",
			namespace: utils.GitOpsNamespace,
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				// Check old config map
				oldConfigMap := &v1.ConfigMap{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      configMapNameOld,
					Namespace: namespace,
				}, oldConfigMap)
				assert.NoError(t, err)
				assert.Equal(t, "apps.open-cluster-management.io/v1", oldConfigMap.Data["apiVersion"])
				assert.Equal(t, "placementrules", oldConfigMap.Data["kind"])

				// Check new config map
				newConfigMap := &v1.ConfigMap{}
				err = c.Get(context.Background(), types.NamespacedName{
					Name:      configMapNameNew,
					Namespace: namespace,
				}, newConfigMap)
				assert.NoError(t, err)
				assert.Equal(t, "cluster.open-cluster-management.io/v1beta1", newConfigMap.Data["apiVersion"])
				assert.Equal(t, "placementdecisions", newConfigMap.Data["kind"])
			},
		},
		{
			name:      "config maps already exist - no error",
			namespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      configMapNameOld,
						Namespace: utils.GitOpsNamespace,
					},
					Data: map[string]string{
						"apiVersion": "apps.open-cluster-management.io/v1",
						"kind":       "placementrules",
					},
				},
				&v1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      configMapNameNew,
						Namespace: utils.GitOpsNamespace,
					},
					Data: map[string]string{
						"apiVersion": "cluster.open-cluster-management.io/v1beta1",
						"kind":       "placementdecisions",
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				// Both config maps should still exist
				oldConfigMap := &v1.ConfigMap{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      configMapNameOld,
					Namespace: namespace,
				}, oldConfigMap)
				assert.NoError(t, err)

				newConfigMap := &v1.ConfigMap{}
				err = c.Get(context.Background(), types.NamespacedName{
					Name:      configMapNameNew,
					Namespace: namespace,
				}, newConfigMap)
				assert.NoError(t, err)
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

			err := reconciler.CreateApplicationSetConfigMaps(tt.namespace)

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

func TestCreateApplicationSetRbac(t *testing.T) {
	scheme := runtime.NewScheme()
	err := rbacv1.AddToScheme(scheme)
	require.NoError(t, err)
	err = v1.AddToScheme(scheme)
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
			name:      "create role and rolebinding successfully",
			namespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-applicationset-controller",
						Namespace: utils.GitOpsNamespace,
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd-applicationset",
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				// Check role
				role := &rbacv1.Role{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      namespace + RoleSuffix,
					Namespace: namespace,
				}, role)
				assert.NoError(t, err)
				assert.Len(t, role.Rules, 2)

				// Check rolebinding
				roleBinding := &rbacv1.RoleBinding{}
				err = c.Get(context.Background(), types.NamespacedName{
					Name:      namespace + RoleSuffix,
					Namespace: namespace,
				}, roleBinding)
				assert.NoError(t, err)
				assert.Equal(t, namespace+RoleSuffix, roleBinding.RoleRef.Name)
				assert.Len(t, roleBinding.Subjects, 1)
				assert.Equal(t, "openshift-gitops-applicationset-controller", roleBinding.Subjects[0].Name)
			},
		},
		{
			name:      "role and rolebinding already exist - no error",
			namespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-applicationset-controller",
						Namespace: utils.GitOpsNamespace,
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd-applicationset",
						},
					},
				},
				&rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name:      utils.GitOpsNamespace + RoleSuffix,
						Namespace: utils.GitOpsNamespace,
					},
				},
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      utils.GitOpsNamespace + RoleSuffix,
						Namespace: utils.GitOpsNamespace,
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				// Both should still exist
				role := &rbacv1.Role{}
				err := c.Get(context.Background(), types.NamespacedName{
					Name:      namespace + RoleSuffix,
					Namespace: namespace,
				}, role)
				assert.NoError(t, err)

				roleBinding := &rbacv1.RoleBinding{}
				err = c.Get(context.Background(), types.NamespacedName{
					Name:      namespace + RoleSuffix,
					Namespace: namespace,
				}, roleBinding)
				assert.NoError(t, err)
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

			err := reconciler.CreateApplicationSetRbac(tt.namespace)

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

func TestEnsureAddonManagerRBAC(t *testing.T) {
	scheme := runtime.NewScheme()
	err := rbacv1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		gitopsNamespace string
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client, namespace string)
	}{
		{
			name:            "create both role and rolebinding successfully",
			gitopsNamespace: utils.GitOpsNamespace,
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				// Check role
				role := &rbacv1.Role{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "addon-manager-controller-role",
					Namespace: namespace,
				}, role)
				assert.NoError(t, err)
				assert.Equal(t, "addon-manager-controller-role", role.Name)
				assert.Equal(t, "true", role.Labels["apps.open-cluster-management.io/gitopsaddon"])
				assert.Len(t, role.Rules, 1)
				assert.Equal(t, []string{""}, role.Rules[0].APIGroups)
				assert.Equal(t, []string{"secrets"}, role.Rules[0].Resources)
				assert.Equal(t, []string{"get", "list", "watch"}, role.Rules[0].Verbs)

				// Check rolebinding
				roleBinding := &rbacv1.RoleBinding{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      "addon-manager-controller-rolebinding",
					Namespace: namespace,
				}, roleBinding)
				assert.NoError(t, err)
				assert.Equal(t, "addon-manager-controller-rolebinding", roleBinding.Name)
				assert.Equal(t, "true", roleBinding.Labels["apps.open-cluster-management.io/gitopsaddon"])
				assert.Equal(t, "addon-manager-controller-role", roleBinding.RoleRef.Name)
				assert.Len(t, roleBinding.Subjects, 1)
				assert.Equal(t, "ServiceAccount", roleBinding.Subjects[0].Kind)
				assert.Equal(t, "addon-manager-controller-sa", roleBinding.Subjects[0].Name)
			},
		},
		{
			name:            "role and rolebinding already exist - no error",
			gitopsNamespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "addon-manager-controller-role",
						Namespace: utils.GitOpsNamespace,
					},
				},
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "addon-manager-controller-rolebinding",
						Namespace: utils.GitOpsNamespace,
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				// Both should still exist
				role := &rbacv1.Role{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "addon-manager-controller-role",
					Namespace: namespace,
				}, role)
				assert.NoError(t, err)

				roleBinding := &rbacv1.RoleBinding{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      "addon-manager-controller-rolebinding",
					Namespace: namespace,
				}, roleBinding)
				assert.NoError(t, err)
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

			err := reconciler.ensureAddonManagerRBAC(tt.gitopsNamespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient, tt.gitopsNamespace)
				}
			}
		})
	}
}

func TestEnsureAddonManagerRole(t *testing.T) {
	scheme := runtime.NewScheme()
	err := rbacv1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		gitopsNamespace string
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client, namespace string)
	}{
		{
			name:            "create role successfully",
			gitopsNamespace: utils.GitOpsNamespace,
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				role := &rbacv1.Role{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "addon-manager-controller-role",
					Namespace: namespace,
				}, role)
				assert.NoError(t, err)
				assert.Equal(t, "addon-manager-controller-role", role.Name)
				assert.Equal(t, namespace, role.Namespace)
			},
		},
		{
			name:            "role already exists - no error",
			gitopsNamespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "addon-manager-controller-role",
						Namespace: utils.GitOpsNamespace,
					},
				},
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

			err := reconciler.ensureAddonManagerRole(tt.gitopsNamespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient, tt.gitopsNamespace)
				}
			}
		})
	}
}

func TestEnsureAddonManagerRoleBinding(t *testing.T) {
	scheme := runtime.NewScheme()
	err := rbacv1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		gitopsNamespace string
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client, namespace string)
	}{
		{
			name:            "create rolebinding successfully",
			gitopsNamespace: utils.GitOpsNamespace,
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				roleBinding := &rbacv1.RoleBinding{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "addon-manager-controller-rolebinding",
					Namespace: namespace,
				}, roleBinding)
				assert.NoError(t, err)
				assert.Equal(t, "addon-manager-controller-rolebinding", roleBinding.Name)
				assert.Equal(t, namespace, roleBinding.Namespace)
			},
		},
		{
			name:            "rolebinding already exists - no error",
			gitopsNamespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "addon-manager-controller-rolebinding",
						Namespace: utils.GitOpsNamespace,
					},
				},
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

			err := reconciler.ensureAddonManagerRoleBinding(tt.gitopsNamespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient, tt.gitopsNamespace)
				}
			}
		})
	}
}
