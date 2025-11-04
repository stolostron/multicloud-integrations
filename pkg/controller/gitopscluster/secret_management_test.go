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
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestEnsureArgoCDAgentCASecret removed - CA certificates are now self-generated
// via certrotation in argocd_agent_certificates.go, not copied from source secret

func TestJWTSecretEdgeCases(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		gitopsNamespace string
		existingObjects []client.Object
		expectedError   bool
	}{
		{
			name:            "empty namespace should use default",
			gitopsNamespace: "",
			existingObjects: []client.Object{},
			expectedError:   false,
		},
		{
			name:            "very long namespace name",
			gitopsNamespace: "this-is-a-very-long-namespace-name-that-should-still-work-fine-in-kubernetes",
			existingObjects: []client.Object{},
			expectedError:   false,
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

			err := reconciler.ensureArgoCDAgentJWTSecret(tt.gitopsNamespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify secret was created in correct namespace
				expectedNamespace := tt.gitopsNamespace
				if expectedNamespace == "" {
					expectedNamespace = "openshift-gitops"
				}

				secret := &v1.Secret{}
				err := fakeClient.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-jwt",
					Namespace: expectedNamespace,
				}, secret)
				assert.NoError(t, err)

				// Validate JWT key format and strength
				jwtKeyData := secret.Data["jwt.key"]
				assert.NotEmpty(t, jwtKeyData)

				// Parse and validate the key
				block, _ := pem.Decode(jwtKeyData)
				assert.NotNil(t, block, "Should be valid PEM format")
				assert.Equal(t, "PRIVATE KEY", block.Type)

				privateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
				assert.NoError(t, err)

				rsaKey, ok := privateKey.(*rsa.PrivateKey)
				assert.True(t, ok, "Should be RSA key")
				assert.Equal(t, 4096, rsaKey.Size()*8, "Should be 4096-bit key")

				// Verify secret labels
				assert.Equal(t, "true", secret.Labels["apps.open-cluster-management.io/gitopscluster"])
			}
		})
	}
}

func TestJWTKeyUniqueness(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	generatedKeys := make(map[string]bool)
	iterations := 3

	for i := 0; i < iterations; i++ {
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		reconciler := &ReconcileGitOpsCluster{
			Client: fakeClient,
		}

		namespace := fmt.Sprintf("test-namespace-%d", i)
		err := reconciler.ensureArgoCDAgentJWTSecret(namespace)
		assert.NoError(t, err)

		secret := &v1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-jwt",
			Namespace: namespace,
		}, secret)
		assert.NoError(t, err)

		keyData := string(secret.Data["jwt.key"])
		assert.NotEmpty(t, keyData)

		// Ensure key is unique
		assert.False(t, generatedKeys[keyData], "Generated key should be unique")
		generatedKeys[keyData] = true

		// Validate key structure
		block, _ := pem.Decode(secret.Data["jwt.key"])
		assert.NotNil(t, block)
		assert.Equal(t, "PRIVATE KEY", block.Type)

		privateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		assert.NoError(t, err)

		rsaKey, ok := privateKey.(*rsa.PrivateKey)
		assert.True(t, ok)
		assert.Equal(t, 4096, rsaKey.Size()*8)

		// Validate that the key can be used for JWT signing
		assert.NotNil(t, rsaKey.PublicKey)
		assert.True(t, rsaKey.Validate() == nil, "RSA key should be valid")
	}

	assert.Equal(t, iterations, len(generatedKeys), "Should have generated unique keys")
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
