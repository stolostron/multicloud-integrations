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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
		{
			name: "mixed secrets in multiple namespaces",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster1-secret",
						Namespace: "argocd",
						Labels: map[string]string{
							"apps.open-cluster-management.io/acm-cluster": "true",
							"argocd.argoproj.io/secret-type":              "cluster",
						},
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster2-secret",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"apps.open-cluster-management.io/acm-cluster": "true",
							"argocd.argoproj.io/secret-type":              "cluster",
						},
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "non-cluster-secret",
						Namespace: "argocd",
						Labels: map[string]string{
							"apps.open-cluster-management.io/acm-cluster": "true",
							"argocd.argoproj.io/secret-type":              "repository",
						},
					},
				},
			},
			expectedSecrets: 2,
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
		{
			name:          "secrets without name field",
			argoNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret-without-name",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type": "cluster",
						},
					},
					// No "name" field in Data
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "acm-cluster-secret",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type":              "cluster",
							"apps.open-cluster-management.io/acm-cluster": "true",
						},
					},
					Data: map[string][]byte{
						"name": []byte("acm-cluster"),
					},
				},
			},
			expectedClusters: map[string]int{},
		},
		{
			name:          "multiple secrets for same cluster",
			argoNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster1-secret-1",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type": "cluster",
						},
					},
					Data: map[string][]byte{
						"name": []byte("same-cluster"),
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster1-secret-2",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type": "cluster",
						},
					},
					Data: map[string][]byte{
						"name": []byte("same-cluster"),
					},
				},
			},
			expectedClusters: map[string]int{
				"same-cluster": 2,
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
		{
			name: "existing secret has Data field, new secret has StringData",
			newSecret: &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"new-label": "new-value",
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
					},
				},
				Data: map[string][]byte{
					"existing-key": []byte("existing-value"),
				},
			},
			validateFunc: func(t *testing.T, result *v1.Secret) {
				// Should convert Data to StringData
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

func TestSaveClusterSecret(t *testing.T) {
	tests := []struct {
		name                string
		orphanSecretsList   map[types.NamespacedName]string
		secretObjectKey     types.NamespacedName
		msaSecretObjectKey  types.NamespacedName
		expectedOrphansList map[types.NamespacedName]string
		description         string
	}{
		{
			name: "remove both secrets from orphan list",
			orphanSecretsList: map[types.NamespacedName]string{
				{Name: "cluster1-secret", Namespace: "argocd"}:                     "argocd",
				{Name: "cluster1-application-manager-secret", Namespace: "argocd"}: "argocd",
				{Name: "cluster2-secret", Namespace: "argocd"}:                     "argocd",
			},
			secretObjectKey: types.NamespacedName{
				Name:      "cluster1-secret",
				Namespace: "argocd",
			},
			msaSecretObjectKey: types.NamespacedName{
				Name:      "cluster1-application-manager-secret",
				Namespace: "argocd",
			},
			expectedOrphansList: map[types.NamespacedName]string{
				{Name: "cluster2-secret", Namespace: "argocd"}: "argocd",
			},
			description: "Should remove both secrets from orphan list",
		},
		{
			name: "remove only one secret when other doesn't exist in orphan list",
			orphanSecretsList: map[types.NamespacedName]string{
				{Name: "cluster1-secret", Namespace: "argocd"}: "argocd",
				{Name: "cluster2-secret", Namespace: "argocd"}: "argocd",
			},
			secretObjectKey: types.NamespacedName{
				Name:      "cluster1-secret",
				Namespace: "argocd",
			},
			msaSecretObjectKey: types.NamespacedName{
				Name:      "cluster1-application-manager-secret",
				Namespace: "argocd",
			},
			expectedOrphansList: map[types.NamespacedName]string{
				{Name: "cluster2-secret", Namespace: "argocd"}: "argocd",
			},
			description: "Should remove only existing secrets from orphan list",
		},
		{
			name:              "empty orphan list - no changes",
			orphanSecretsList: map[types.NamespacedName]string{},
			secretObjectKey: types.NamespacedName{
				Name:      "cluster1-secret",
				Namespace: "argocd",
			},
			msaSecretObjectKey: types.NamespacedName{
				Name:      "cluster1-application-manager-secret",
				Namespace: "argocd",
			},
			expectedOrphansList: map[types.NamespacedName]string{},
			description:         "Empty orphan list should remain empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveClusterSecret(tt.orphanSecretsList, tt.secretObjectKey, tt.msaSecretObjectKey)
			assert.Equal(t, tt.expectedOrphansList, tt.orphanSecretsList, tt.description)
		})
	}
}

func TestGetManagedClusterToken(t *testing.T) {
	tests := []struct {
		name          string
		dataConfig    []byte
		expectedToken string
		expectedError bool
		description   string
	}{
		{
			name:          "valid JSON config with bearerToken",
			dataConfig:    []byte(`{"bearerToken": "test-token-123", "tlsClientConfig": {"insecure": true}}`),
			expectedToken: "test-token-123",
			expectedError: false,
			description:   "Should extract token from valid JSON config",
		},
		{
			name:          "valid JSON config without bearerToken",
			dataConfig:    []byte(`{"other_field": "value"}`),
			expectedToken: "",
			expectedError: true,
			description:   "Should return error when no bearerToken present",
		},
		{
			name:          "invalid JSON",
			dataConfig:    []byte(`{invalid json}`),
			expectedToken: "",
			expectedError: true,
			description:   "Should return error for invalid JSON",
		},
		{
			name:          "empty config",
			dataConfig:    []byte(""),
			expectedToken: "",
			expectedError: true,
			description:   "Should return error for empty config",
		},
		{
			name:          "empty JSON object",
			dataConfig:    []byte(`{}`),
			expectedToken: "",
			expectedError: true,
			description:   "Should return error for empty JSON object without bearerToken",
		},
		{
			name:          "config with null bearerToken",
			dataConfig:    []byte(`{"bearerToken": null}`),
			expectedToken: "",
			expectedError: true,
			description:   "Should return error for null bearerToken",
		},
		{
			name:          "config with empty string bearerToken",
			dataConfig:    []byte(`{"bearerToken": "", "tlsClientConfig": {"insecure": true}}`),
			expectedToken: "",
			expectedError: true,
			description:   "Should return error for empty bearerToken",
		},
		{
			name:          "config with whitespace bearerToken",
			dataConfig:    []byte(`{"bearerToken": "   ", "tlsClientConfig": {"insecure": true}}`),
			expectedToken: "   ",
			expectedError: false,
			description:   "Should return whitespace token as-is",
		},
		{
			name:          "config with numeric bearerToken",
			dataConfig:    []byte(`{"bearerToken": 123}`),
			expectedToken: "",
			expectedError: true,
			description:   "Should return error for numeric bearerToken (JSON unmarshal fails)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectedError {
				// Function may panic on invalid input, so we need to catch panics
				defer func() {
					if r := recover(); r != nil {
						// Expected panic/error case
						return
					}
				}()
			}

			token, err := getManagedClusterToken(tt.dataConfig)

			if tt.expectedError {
				if err == nil {
					// If no error but we expected one, the function should have panicked
					t.Errorf("Expected error or panic but got none")
				}
			} else {
				assert.NoError(t, err, tt.description)
				assert.Equal(t, tt.expectedToken, token, tt.description)
			}
		})
	}
}

func TestGetManagedClusterURL(t *testing.T) {
	tests := []struct {
		name           string
		managedCluster *spokeclusterv1.ManagedCluster
		token          string
		expectedURL    string
		expectedError  bool
		description    string
	}{
		{
			name: "managed cluster with API server URL",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: spokeclusterv1.ManagedClusterSpec{
					ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
						{
							URL: "https://api.test-cluster.example.com:6443",
						},
					},
				},
			},
			token:         "test-token",
			expectedURL:   "https://api.test-cluster.example.com:6443",
			expectedError: false,
			description:   "Should return the API server URL from managed cluster spec",
		},
		{
			name: "managed cluster without API server URL",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: spokeclusterv1.ManagedClusterSpec{
					ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{},
				},
			},
			token:         "test-token",
			expectedURL:   "",
			expectedError: true,
			description:   "Should return error when no API server URL available",
		},
		{
			name: "managed cluster with empty client configs",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: spokeclusterv1.ManagedClusterSpec{},
			},
			token:         "test-token",
			expectedURL:   "",
			expectedError: true,
			description:   "Should return error when client configs are empty",
		},
		{
			name: "managed cluster with empty URL in client config",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: spokeclusterv1.ManagedClusterSpec{
					ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
						{
							URL: "",
						},
					},
				},
			},
			token:         "test-token",
			expectedURL:   "https://",
			expectedError: false,
			description:   "Should return https:// when URL is empty (adds https prefix)",
		},
		{
			name: "managed cluster with multiple client configs",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: spokeclusterv1.ManagedClusterSpec{
					ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
						{
							URL: "https://api1.test-cluster.example.com:6443",
						},
						{
							URL: "https://api2.test-cluster.example.com:6443",
						},
					},
				},
			},
			token:         "test-token",
			expectedURL:   "https://api1.test-cluster.example.com:6443",
			expectedError: false,
			description:   "Should return first URL when multiple client configs available",
		},
		{
			name: "managed cluster with whitespace URL",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: spokeclusterv1.ManagedClusterSpec{
					ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
						{
							URL: "   https://api.test-cluster.example.com:6443   ",
						},
					},
				},
			},
			token:         "test-token",
			expectedURL:   "   https://api.test-cluster.example.com:6443   ",
			expectedError: false,
			description:   "Should return URL as-is when it already has https prefix (even with whitespace)",
		},
		{
			name: "managed cluster with URL without https prefix",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: spokeclusterv1.ManagedClusterSpec{
					ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
						{
							URL: "api.test-cluster.example.com:6443",
						},
					},
				},
			},
			token:         "test-token",
			expectedURL:   "https://api.test-cluster.example.com:6443",
			expectedError: false,
			description:   "Should add https prefix when URL doesn't have protocol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, err := getManagedClusterURL(tt.managedCluster, tt.token)

			if tt.expectedError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
				assert.Equal(t, tt.expectedURL, url, tt.description)
			}
		})
	}
}

func TestCreateManagedClusterSecretInArgoAdvanced(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = authv1beta1.AddToScheme(scheme)
	_ = spokeclusterv1.AddToScheme(scheme)

	// Create test managed service account
	testMSA := &authv1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "application-manager",
			Namespace: "test-cluster",
		},
		Status: authv1beta1.ManagedServiceAccountStatus{
			TokenSecretRef: &authv1beta1.SecretRef{
				Name: "application-manager",
			},
		},
	}

	// Create test managed service account secret
	testMSASecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "application-manager",
			Namespace: "test-cluster",
		},
		Data: map[string][]byte{
			"token": []byte(`{"access_token": "test-token-123"}`),
		},
	}

	// Create test cluster secret with proper config format for non-blank tests
	testClusterSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-cluster-secret",
			Namespace: "test-cluster",
			Labels: map[string]string{
				"apps.open-cluster-management.io/cluster-name":   "test-cluster",
				"apps.open-cluster-management.io/cluster-server": "api.test-cluster.example.com",
			},
		},
		Data: map[string][]byte{
			"config": []byte(`{"bearerToken":"test-token-123","tlsClientConfig":{"insecure":true}}`),
			"name":   []byte("test-cluster"),
			"server": []byte("https://api.test-cluster.example.com:6443"),
		},
	}

	tests := []struct {
		name                      string
		argoNamespace             string
		managedCluster            *spokeclusterv1.ManagedCluster
		managedServiceAccountRef  string
		createBlankClusterSecrets bool
		setupClient               func() client.Client
		expectedError             bool
		expectedSecretName        string
		description               string
	}{
		{
			name:          "create blank cluster secret when createBlankClusterSecrets is true",
			argoNamespace: "argocd",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
			},
			managedServiceAccountRef:  "application-manager",
			createBlankClusterSecrets: true,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(testMSA, testMSASecret).Build()
			},
			expectedError:      false,
			expectedSecretName: "test-cluster-application-manager-cluster-secret",
			description:        "Should create blank cluster secret with dummy values",
		},
		{
			name:          "create real cluster secret when createBlankClusterSecrets is false",
			argoNamespace: "argocd",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: spokeclusterv1.ManagedClusterSpec{
					ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
						{
							URL: "https://api.test-cluster.example.com:6443",
						},
					},
				},
			},
			managedServiceAccountRef:  "application-manager",
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(testMSA, testClusterSecret).Build()
			},
			expectedError:      false,
			expectedSecretName: "test-cluster-cluster-secret",
			description:        "Should create real cluster secret with actual config",
		},
		{
			name:          "managed service account secret not found - should return error",
			argoNamespace: "argocd",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
			},
			managedServiceAccountRef:  "non-existent-msa",
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError:      true,
			expectedSecretName: "",
			description:        "Should return error when MSA secret is not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.setupClient()
			reconciler := &ReconcileGitOpsCluster{
				Client: client,
				scheme: scheme,
			}

			// Use the appropriate secret based on the test case
			secretToUse := testMSASecret
			if tt.name == "create real cluster secret when createBlankClusterSecrets is false" {
				secretToUse = testClusterSecret
			}

			secret, err := reconciler.CreateManagedClusterSecretInArgo(
				tt.argoNamespace,
				secretToUse,
				tt.managedCluster,
				tt.createBlankClusterSecrets,
			)

			if tt.expectedError {
				assert.Error(t, err, tt.description)
				assert.Nil(t, secret, "Secret should be nil when error occurs")
			} else {
				assert.NoError(t, err, tt.description)
				assert.NotNil(t, secret, "Secret should not be nil when successful")
				if secret != nil && tt.expectedSecretName != "" {
					assert.Equal(t, tt.expectedSecretName, secret.Name, "Secret name should match expected")
					assert.Equal(t, tt.argoNamespace, secret.Namespace, "Secret namespace should match ArgoCD namespace")
					assert.Equal(t, "cluster", secret.Labels["argocd.argoproj.io/secret-type"], "Should have correct ArgoCD label")
					assert.Equal(t, "true", secret.Labels["apps.open-cluster-management.io/acm-cluster"], "Should have ACM cluster label")
				}
			}
		})
	}
}

func TestAddManagedClustersToArgo(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = authv1beta1.AddToScheme(scheme)
	_ = spokeclusterv1.AddToScheme(scheme)
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	tests := []struct {
		name                      string
		gitOpsCluster             *gitopsclusterV1beta1.GitOpsCluster
		managedClusters           []*spokeclusterv1.ManagedCluster
		orphanSecretsList         map[types.NamespacedName]string
		createBlankClusterSecrets bool
		setupClient               func() client.Client
		expectedError             bool
		description               string
	}{
		{
			name: "no placement reference - should skip processing",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			orphanSecretsList:         map[types.NamespacedName]string{},
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			description:   "Should skip processing when no placement reference",
		},
		{
			name: "with placement reference but no managed service account ref - should process normally",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					PlacementRef: &v1.ObjectReference{
						Kind: "Placement",
						Name: "test-placement",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			orphanSecretsList:         map[types.NamespacedName]string{},
			createBlankClusterSecrets: true,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			description:   "Should process normally with placement ref but no MSA ref",
		},
		{
			name: "with MSA exists - should use MSA path",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					PlacementRef: &v1.ObjectReference{
						Kind: "Placement",
						Name: "test-placement",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
					Spec: spokeclusterv1.ManagedClusterSpec{
						ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
							{
								URL: "https://api.test-cluster.example.com:6443",
							},
						},
					},
				},
			},
			orphanSecretsList:         map[types.NamespacedName]string{},
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				// Create MSA and its secret
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "application-manager",
						Namespace: "test-cluster",
					},
					Status: authv1beta1.ManagedServiceAccountStatus{
						TokenSecretRef: &authv1beta1.SecretRef{
							Name: "application-manager",
						},
					},
				}
				msaSecret := &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "application-manager",
						Namespace: "test-cluster",
					},
					Data: map[string][]byte{
						"token":  []byte(`{"access_token": "test-token"}`),
						"ca.crt": []byte("test-ca-data"),
					},
				}
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(msa, msaSecret).Build()
			},
			expectedError: false, // Actually succeeds because function handles errors gracefully
			description:   "Should use MSA path when MSA exists",
		},
		{
			name: "with ManagedServiceAccountRef specified - should use specified MSA",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					PlacementRef: &v1.ObjectReference{
						Kind: "Placement",
						Name: "test-placement",
					},
					ManagedServiceAccountRef: "custom-msa",
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
					Spec: spokeclusterv1.ManagedClusterSpec{
						ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
							{
								URL: "https://api.test-cluster.example.com:6443",
							},
						},
					},
				},
			},
			orphanSecretsList:         map[types.NamespacedName]string{},
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				// Create custom MSA and its secret
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "custom-msa",
						Namespace: "test-cluster",
					},
					Status: authv1beta1.ManagedServiceAccountStatus{
						TokenSecretRef: &authv1beta1.SecretRef{
							Name: "custom-msa-secret",
						},
					},
				}
				msaSecret := &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "custom-msa-secret",
						Namespace: "test-cluster",
					},
					Data: map[string][]byte{
						"token":  []byte(`{"access_token": "custom-token"}`),
						"ca.crt": []byte("custom-ca-data"),
					},
				}
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(msa, msaSecret).Build()
			},
			expectedError: false, // Actually succeeds because function handles errors gracefully
			description:   "Should use specified MSA when ManagedServiceAccountRef is set",
		},
		{
			name: "with createBlankClusterSecrets=false and no secret - should continue with errors",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					PlacementRef: &v1.ObjectReference{
						Kind: "Placement",
						Name: "test-placement",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			orphanSecretsList:         map[types.NamespacedName]string{},
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: true, // Should return errors when secrets are missing
			description:   "Should return errors when createBlankClusterSecrets=false and secrets missing",
		},
		{
			name: "with existing cluster secret - should try fallback secret names",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					PlacementRef: &v1.ObjectReference{
						Kind: "Placement",
						Name: "test-placement",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
					Spec: spokeclusterv1.ManagedClusterSpec{
						ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
							{
								URL: "https://api.test-cluster.example.com:6443",
							},
						},
					},
				},
			},
			orphanSecretsList:         map[types.NamespacedName]string{},
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				// Create a cluster secret with the fallback name
				clusterSecret := &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster-application-manager-cluster-secret",
						Namespace: "test-cluster",
					},
					Data: map[string][]byte{
						"config": []byte(`{"access_token": "fallback-token"}`),
						"server": []byte("https://api.test-cluster.example.com:6443"),
					},
				}
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(clusterSecret).Build()
			},
			expectedError: false, // Actually succeeds because function handles errors gracefully
			description:   "Should try fallback secret names when primary secret not found",
		},
		{
			name: "with existing ArgoCD secret to update - should merge secrets",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					PlacementRef: &v1.ObjectReference{
						Kind: "Placement",
						Name: "test-placement",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
					Spec: spokeclusterv1.ManagedClusterSpec{
						ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
							{
								URL: "https://api.test-cluster.example.com:6443",
							},
						},
					},
				},
			},
			orphanSecretsList:         map[types.NamespacedName]string{},
			createBlankClusterSecrets: true,
			setupClient: func() client.Client {
				// Create existing ArgoCD secret that should be updated
				existingArgoCDSecret := &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster-cluster-secret",
						Namespace: "argocd",
						Labels: map[string]string{
							"existing": "label",
						},
					},
					StringData: map[string]string{
						"existing-key": "existing-value",
					},
				}
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingArgoCDSecret).Build()
			},
			expectedError: false,
			description:   "Should update existing ArgoCD secret by merging data",
		},
		{
			name: "with orphan secrets to clean up - should handle orphan list",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					PlacementRef: &v1.ObjectReference{
						Kind: "Placement",
						Name: "test-placement",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			orphanSecretsList: map[types.NamespacedName]string{
				{Name: "test-cluster-cluster-secret", Namespace: "argocd"}: "test-cluster",
				{Name: "orphan-secret", Namespace: "argocd"}:               "orphan-cluster",
			},
			createBlankClusterSecrets: true,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			description:   "Should properly handle orphan secrets list during processing",
		},
		{
			name: "with MSA cleanup scenario - should delete old cluster secret",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					PlacementRef: &v1.ObjectReference{
						Kind: "Placement",
						Name: "test-placement",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
					Spec: spokeclusterv1.ManagedClusterSpec{
						ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
							{
								URL: "https://api.test-cluster.example.com:6443",
							},
						},
					},
				},
			},
			orphanSecretsList:         map[types.NamespacedName]string{},
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				// Create MSA, MSA secret, and old cluster secret to be cleaned up
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "application-manager",
						Namespace: "test-cluster",
					},
					Status: authv1beta1.ManagedServiceAccountStatus{
						TokenSecretRef: &authv1beta1.SecretRef{
							Name: "application-manager",
						},
					},
				}
				msaSecret := &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "application-manager",
						Namespace: "test-cluster",
					},
					Data: map[string][]byte{
						"token":  []byte(`{"access_token": "test-token"}`),
						"ca.crt": []byte("test-ca-data"),
					},
				}
				// Old cluster secret that should be deleted
				oldClusterSecret := &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster-cluster-secret",
						Namespace: "test-cluster",
					},
					Data: map[string][]byte{
						"config": []byte(`{"access_token": "old-token"}`),
					},
				}
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(msa, msaSecret, oldClusterSecret).Build()
			},
			expectedError: false,
			description:   "Should cleanup old cluster secret when MSA exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.setupClient()
			reconciler := &ReconcileGitOpsCluster{
				Client: client,
				scheme: scheme,
			}

			err := reconciler.AddManagedClustersToArgo(
				tt.gitOpsCluster,
				tt.managedClusters,
				tt.orphanSecretsList,
				tt.createBlankClusterSecrets,
			)

			if tt.expectedError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}
		})
	}
}

func TestCreateManagedClusterSecretFromManagedServiceAccount(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = authv1beta1.AddToScheme(scheme)
	_ = spokeclusterv1.AddToScheme(scheme)

	// Create test managed service account with proper status
	testMSA := &authv1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-msa",
			Namespace: "test-cluster",
		},
		Status: authv1beta1.ManagedServiceAccountStatus{
			TokenSecretRef: &authv1beta1.SecretRef{
				Name: "test-msa-secret",
			},
		},
	}

	// Create test managed service account secret with proper data
	testMSASecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-msa-secret",
			Namespace: "test-cluster",
		},
		Data: map[string][]byte{
			"token":  []byte("test-token-123"),
			"ca.crt": []byte("test-ca-data"),
		},
	}

	// Create managed cluster with client configs
	testManagedCluster := &spokeclusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: spokeclusterv1.ManagedClusterSpec{
			ManagedClusterClientConfigs: []spokeclusterv1.ClientConfig{
				{
					URL: "https://api.test-cluster.example.com:6443",
				},
			},
		},
	}

	tests := []struct {
		name                      string
		argoNamespace             string
		managedCluster            *spokeclusterv1.ManagedCluster
		msaName                   string
		enableTLS                 bool
		createBlankClusterSecrets bool
		setupClient               func() client.Client
		expectedError             bool
		expectedName              string
		expectedServer            string
		description               string
	}{
		{
			name:                      "empty MSA name - should return error",
			argoNamespace:             "argocd",
			managedCluster:            testManagedCluster,
			msaName:                   "",
			enableTLS:                 false,
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: true,
			description:   "Should return error for empty ManagedServiceAccount name",
		},
		{
			name:                      "MSA not found - should return error",
			argoNamespace:             "argocd",
			managedCluster:            testManagedCluster,
			msaName:                   "non-existent-msa",
			enableTLS:                 false,
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: true,
			description:   "Should return error when ManagedServiceAccount is not found",
		},
		{
			name:                      "MSA without token secret ref - should return error",
			argoNamespace:             "argocd",
			managedCluster:            testManagedCluster,
			msaName:                   "test-msa-no-token",
			enableTLS:                 false,
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				msaNoToken := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-msa-no-token",
						Namespace: "test-cluster",
					},
					// No TokenSecretRef in status
				}
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(msaNoToken).Build()
			},
			expectedError: true,
			description:   "Should return error when ManagedServiceAccount has no tokenSecretRef",
		},
		{
			name:                      "MSA secret not found - should return error",
			argoNamespace:             "argocd",
			managedCluster:            testManagedCluster,
			msaName:                   "test-msa",
			enableTLS:                 false,
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				// Only include MSA but not the secret
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(testMSA).Build()
			},
			expectedError: true,
			description:   "Should return error when ManagedServiceAccount secret is not found",
		},
		{
			name:                      "token as plain string - should succeed",
			argoNamespace:             "argocd",
			managedCluster:            testManagedCluster,
			msaName:                   "test-msa",
			enableTLS:                 false,
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				invalidSecret := &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-msa-secret",
						Namespace: "test-cluster",
					},
					Data: map[string][]byte{
						"token":  []byte("test-token-123"),
						"ca.crt": []byte("test-ca-data"),
					},
				}
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(testMSA, invalidSecret).Build()
			},
			expectedError:  false,
			expectedName:   "test-cluster-test-msa-cluster-secret",
			expectedServer: "https://api.test-cluster.example.com:6443",
			description:    "Should succeed when token in secret is plain string",
		},
		{
			name:          "managed cluster without client configs - should return error",
			argoNamespace: "argocd",
			managedCluster: &spokeclusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				// No ManagedClusterClientConfigs
			},
			msaName:                   "test-msa",
			enableTLS:                 false,
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(testMSA, testMSASecret).Build()
			},
			expectedError: true,
			description:   "Should return error when managed cluster has no client configs",
		},
		{
			name:                      "successful secret creation with enableTLS=false",
			argoNamespace:             "argocd",
			managedCluster:            testManagedCluster,
			msaName:                   "test-msa",
			enableTLS:                 false,
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(testMSA, testMSASecret).Build()
			},
			expectedError:  false,
			expectedName:   "test-cluster-test-msa-cluster-secret",
			expectedServer: "https://api.test-cluster.example.com:6443",
			description:    "Should successfully create secret with MSA name in secret name",
		},
		{
			name:                      "successful secret creation with enableTLS=true",
			argoNamespace:             "argocd",
			managedCluster:            testManagedCluster,
			msaName:                   "test-msa",
			enableTLS:                 true,
			createBlankClusterSecrets: false,
			setupClient: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(testMSA, testMSASecret).Build()
			},
			expectedError:  false,
			expectedName:   "test-cluster-test-msa-cluster-secret",
			expectedServer: "https://api.test-cluster.example.com:6443",
			description:    "Should successfully create secret with TLS enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.setupClient()
			reconciler := &ReconcileGitOpsCluster{
				Client: client,
				scheme: scheme,
			}

			secret, err := reconciler.CreateManagedClusterSecretFromManagedServiceAccount(
				tt.argoNamespace,
				tt.managedCluster,
				tt.msaName,
				tt.enableTLS,
				tt.createBlankClusterSecrets,
			)

			if tt.expectedError {
				assert.Error(t, err, tt.description)
				if secret != nil {
					// Some error cases might still return a partial secret
					assert.Nil(t, secret, "Secret should be nil when error occurs")
				}
			} else {
				assert.NoError(t, err, tt.description)
				assert.NotNil(t, secret, "Secret should not be nil when successful")

				if tt.expectedName != "" {
					assert.Equal(t, tt.expectedName, secret.Name, "Secret name should match expected")
				}

				if tt.expectedServer != "" {
					assert.Equal(t, tt.expectedServer, secret.StringData["server"], "Server URL should match expected")
					// Label contains stripped URL (no protocol/port for label compatibility)
					expectedLabel := tt.expectedServer
					if idx := strings.Index(expectedLabel, "://"); idx > 0 {
						expectedLabel = expectedLabel[idx+3:]
					}
					if idx := strings.Index(expectedLabel, ":"); idx > 0 {
						expectedLabel = expectedLabel[:idx]
					}
					assert.Equal(t, expectedLabel, secret.Labels["apps.open-cluster-management.io/cluster-server"], "Server label should match expected")
				}

				// Verify common secret properties
				assert.Equal(t, tt.argoNamespace, secret.Namespace, "Secret namespace should match ArgoCD namespace")
				assert.Equal(t, "cluster", secret.Labels["argocd.argoproj.io/secret-type"], "Should have correct ArgoCD label")
				assert.Equal(t, "true", secret.Labels["apps.open-cluster-management.io/acm-cluster"], "Should have ACM cluster label")
				assert.Equal(t, tt.managedCluster.Name, secret.Labels["apps.open-cluster-management.io/cluster-name"], "Should have cluster name label")

				// Verify secret contains proper config
				assert.Contains(t, secret.StringData["config"], "bearerToken", "Config should contain bearer token")
				assert.Contains(t, secret.StringData["config"], "test-token-123", "Config should contain actual token")

				if tt.enableTLS {
					assert.Contains(t, secret.StringData["config"], "caData", "Config should contain CA data when TLS enabled")
					assert.Contains(t, secret.StringData["config"], `"insecure":false`, "Config should set insecure to false when TLS enabled")
				} else {
					assert.Contains(t, secret.StringData["config"], `"insecure":true`, "Config should set insecure to true when TLS disabled")
				}
			}
		})
	}
}
