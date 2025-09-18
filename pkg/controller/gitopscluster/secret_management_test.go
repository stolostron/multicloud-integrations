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
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureArgoCDAgentCASecret(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	// Get the actual namespace that GetComponentNamespace will return
	sourceNamespace := utils.GetComponentNamespace("open-cluster-management")

	tests := []struct {
		name            string
		gitopsNamespace string
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client, namespace string)
	}{
		{
			name:            "create CA secret by copying from source",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multicluster-operators-application-svc-ca",
						Namespace: sourceNamespace,
						Labels: map[string]string{
							"source-label": "source-value",
						},
						Annotations: map[string]string{
							"source-annotation": "source-value",
						},
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": []byte("test-ca-certificate"),
						"tls.key": []byte("test-ca-key"),
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-ca",
					Namespace: namespace,
				}, secret)
				assert.NoError(t, err)

				assert.Equal(t, "argocd-agent-ca", secret.Name)
				assert.Equal(t, namespace, secret.Namespace)
				assert.Equal(t, v1.SecretTypeTLS, secret.Type)
				assert.Equal(t, []byte("test-ca-certificate"), secret.Data["tls.crt"])
				assert.Equal(t, []byte("test-ca-key"), secret.Data["tls.key"])

				// Verify labels and annotations are copied
				assert.Equal(t, "source-value", secret.Labels["source-label"])
				assert.Equal(t, "source-value", secret.Annotations["source-annotation"])
			},
		},
		{
			name:            "secret already exists - no error",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-ca",
						Namespace: "openshift-gitops",
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": []byte("existing-ca-certificate"),
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-ca",
					Namespace: namespace,
				}, secret)
				assert.NoError(t, err)
				// Should not be modified
				assert.Equal(t, []byte("existing-ca-certificate"), secret.Data["tls.crt"])
			},
		},
		{
			name:            "source secret not found - should return error",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{},
			expectedError:   true,
		},
		{
			name:            "copy from source with nil labels and annotations",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multicluster-operators-application-svc-ca",
						Namespace: sourceNamespace,
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": []byte("test-ca-certificate"),
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-ca",
					Namespace: namespace,
				}, secret)
				assert.NoError(t, err)

				// The fake client may return nil for empty maps, which is acceptable
				// What we care about is that no entries exist, not that the map itself is non-nil
				assert.Equal(t, 0, len(secret.Labels))
				assert.Equal(t, 0, len(secret.Annotations))
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

			err := reconciler.ensureArgoCDAgentCASecret(tt.gitopsNamespace)

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

func TestEnsureArgoCDRedisSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		gitopsNamespace string
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client, namespace string)
	}{
		{
			name:            "create Redis secret from existing redis-initial-password secret",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-redis-initial-password",
						Namespace: "openshift-gitops",
					},
					Data: map[string][]byte{
						"admin.password": []byte("test-redis-password"),
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-redis",
					Namespace: namespace,
				}, secret)
				assert.NoError(t, err)

				assert.Equal(t, "argocd-redis", secret.Name)
				assert.Equal(t, namespace, secret.Namespace)
				assert.Equal(t, v1.SecretTypeOpaque, secret.Type)
				assert.Equal(t, []byte("test-redis-password"), secret.Data["auth"])
				assert.Equal(t, "true", secret.Labels["apps.open-cluster-management.io/gitopscluster"])
			},
		},
		{
			name:            "Redis secret already exists - no error",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-redis",
						Namespace: "openshift-gitops",
					},
					Data: map[string][]byte{
						"auth": []byte("existing-password"),
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-redis",
					Namespace: namespace,
				}, secret)
				assert.NoError(t, err)
				// Should not be modified
				assert.Equal(t, []byte("existing-password"), secret.Data["auth"])
			},
		},
		{
			name:            "use default namespace when empty",
			gitopsNamespace: "",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-redis-initial-password",
						Namespace: "openshift-gitops",
					},
					Data: map[string][]byte{
						"admin.password": []byte("test-redis-password"),
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-redis",
					Namespace: "openshift-gitops", // Should use default namespace
				}, secret)
				assert.NoError(t, err)
				assert.Equal(t, []byte("test-redis-password"), secret.Data["auth"])
			},
		},
		{
			name:            "no redis-initial-password secret found - should return error",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{},
			expectedError:   true,
		},
		{
			name:            "redis-initial-password secret exists but missing admin.password",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-redis-initial-password",
						Namespace: "openshift-gitops",
					},
					Data: map[string][]byte{
						"other-field": []byte("other-value"),
					},
				},
			},
			expectedError: true,
		},
		{
			name:            "find secret with different prefix but ending with redis-initial-password",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "custom-redis-initial-password",
						Namespace: "openshift-gitops",
					},
					Data: map[string][]byte{
						"admin.password": []byte("custom-redis-password"),
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-redis",
					Namespace: namespace,
				}, secret)
				assert.NoError(t, err)
				assert.Equal(t, []byte("custom-redis-password"), secret.Data["auth"])
			},
		},
		{
			name:            "multiple redis-initial-password secrets - should use first found",
			gitopsNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "first-redis-initial-password",
						Namespace: "openshift-gitops",
					},
					Data: map[string][]byte{
						"admin.password": []byte("first-password"),
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "second-redis-initial-password",
						Namespace: "openshift-gitops",
					},
					Data: map[string][]byte{
						"admin.password": []byte("second-password"),
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, namespace string) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-redis",
					Namespace: namespace,
				}, secret)
				assert.NoError(t, err)
				// Should use the first one found (order may not be deterministic in fake client)
				assert.NotEmpty(t, secret.Data["auth"])
				assert.True(t,
					string(secret.Data["auth"]) == "first-password" ||
						string(secret.Data["auth"]) == "second-password",
					"Should use one of the passwords from redis-initial-password secrets")
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

			err := reconciler.ensureArgoCDRedisSecret(tt.gitopsNamespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					// Use the actual namespace or default
					actualNamespace := tt.gitopsNamespace
					if actualNamespace == "" {
						actualNamespace = "openshift-gitops"
					}
					tt.validateFunc(t, fakeClient, actualNamespace)
				}
			}
		})
	}
}
