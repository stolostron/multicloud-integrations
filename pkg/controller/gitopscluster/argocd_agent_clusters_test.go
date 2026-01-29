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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateArgoCDAgentClusters(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = spokeclusterv1.AddToScheme(scheme)
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	// Create test CA certificate
	caCert, caKey, caPEM := generateTestCACertificate(t)

	tests := []struct {
		name                 string
		gitOpsCluster        *gitopsclusterV1beta1.GitOpsCluster
		managedClusters      []*spokeclusterv1.ManagedCluster
		orphanSecretsList    map[types.NamespacedName]string
		existingObjects      []client.Object
		expectedError        bool
		expectedSecretsCount int
		validateFunc         func(t *testing.T, c client.Client, orphanList map[types.NamespacedName]string)
	}{
		{
			name: "create agent cluster secrets for multiple clusters",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "test-server.example.com",
							ServerPort:    "443",
						},
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster2",
					},
				},
			},
			orphanSecretsList: map[types.NamespacedName]string{
				{Name: "cluster-cluster1", Namespace: "argocd"}: "argocd/cluster-cluster1",
				{Name: "cluster-cluster2", Namespace: "argocd"}: "argocd/cluster-cluster2",
				{Name: "other-secret", Namespace: "argocd"}:     "argocd/other-secret",
			},
			existingObjects: []client.Object{
				createTestPrincipalCASecret("argocd", caCert, caKey, caPEM),
			},
			expectedError:        false,
			expectedSecretsCount: 2,
			validateFunc: func(t *testing.T, c client.Client, orphanList map[types.NamespacedName]string) {
				// Check that agent cluster secrets were removed from orphan list
				_, exists := orphanList[types.NamespacedName{Name: "cluster-cluster1", Namespace: "argocd"}]
				assert.False(t, exists, "Agent cluster secret should be removed from orphan list")

				_, exists = orphanList[types.NamespacedName{Name: "cluster-cluster2", Namespace: "argocd"}]
				assert.False(t, exists, "Agent cluster secret should be removed from orphan list")

				// Other secrets should remain in orphan list
				_, exists = orphanList[types.NamespacedName{Name: "other-secret", Namespace: "argocd"}]
				assert.True(t, exists, "Other secrets should remain in orphan list")

				// Verify created secrets
				secret1 := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{Name: "cluster-cluster1", Namespace: "argocd"}, secret1)
				assert.NoError(t, err)
				assert.Equal(t, "cluster", secret1.Labels[argoCDTypeLabel])
				assert.Equal(t, "cluster1", secret1.Labels[labelKeyClusterAgentMapping])
				assert.Equal(t, labelValueManagerName, secret1.Annotations[argoCDManagedByAnnotation])

				secret2 := &v1.Secret{}
				err = c.Get(context.TODO(), types.NamespacedName{Name: "cluster-cluster2", Namespace: "argocd"}, secret2)
				assert.NoError(t, err)
				assert.Equal(t, "cluster", secret2.Labels[argoCDTypeLabel])
				assert.Equal(t, "cluster2", secret2.Labels[labelKeyClusterAgentMapping])
			},
		},
		{
			name: "override existing traditional cluster secret",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "test-server.example.com",
							ServerPort:    "443",
						},
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
			},
			orphanSecretsList: map[types.NamespacedName]string{},
			existingObjects: []client.Object{
				createTestPrincipalCASecret("argocd", caCert, caKey, caPEM),
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster-cluster1",
						Namespace: "argocd",
						Labels: map[string]string{
							argoCDTypeLabel: argoCDSecretTypeClusterValue,
							// No agent mapping label - this is a traditional secret
						},
					},
					Data: map[string][]byte{
						"server": []byte("https://traditional-server"),
						"name":   []byte("cluster1"),
					},
				},
			},
			expectedError:        false,
			expectedSecretsCount: 1,
			validateFunc: func(t *testing.T, c client.Client, orphanList map[types.NamespacedName]string) {
				// Verify secret was overridden with agent configuration
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{Name: "cluster-cluster1", Namespace: "argocd"}, secret)
				assert.NoError(t, err)
				assert.Equal(t, "cluster1", secret.Labels[labelKeyClusterAgentMapping])
				assert.Equal(t, labelValueManagerName, secret.Annotations[argoCDManagedByAnnotation])

				// Verify it's an agent URL now
				serverURL := string(secret.Data["server"])
				assert.Contains(t, serverURL, "agentName=cluster1")
			},
		},
		{
			name: "missing server configuration should fail",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							// Missing ServerAddress and ServerPort
						},
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
			},
			orphanSecretsList:    map[types.NamespacedName]string{},
			existingObjects:      []client.Object{},
			expectedError:        true,
			expectedSecretsCount: 0,
		},
		{
			name: "missing CA secret should fail",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "test-server.example.com",
							ServerPort:    "443",
						},
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
			},
			orphanSecretsList:    map[types.NamespacedName]string{},
			existingObjects:      []client.Object{}, // No CA secret
			expectedError:        true,
			expectedSecretsCount: 0,
		},
		{
			name: "skip local-cluster by name",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "test-server.example.com",
							ServerPort:    "443",
						},
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "local-cluster",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "remote-cluster",
					},
				},
			},
			orphanSecretsList: map[types.NamespacedName]string{},
			existingObjects: []client.Object{
				createTestPrincipalCASecret("argocd", caCert, caKey, caPEM),
			},
			expectedError:        false,
			expectedSecretsCount: 1,
			validateFunc: func(t *testing.T, c client.Client, orphanList map[types.NamespacedName]string) {
				// local-cluster should not have cluster secret
				localSecret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{Name: "cluster-local-cluster", Namespace: "argocd"}, localSecret)
				assert.Error(t, err, "Cluster secret should not be created for local-cluster")

				// remote-cluster should have cluster secret
				remoteSecret := &v1.Secret{}
				err = c.Get(context.TODO(), types.NamespacedName{Name: "cluster-remote-cluster", Namespace: "argocd"}, remoteSecret)
				assert.NoError(t, err, "Cluster secret should be created for remote-cluster")
				assert.Equal(t, "cluster", remoteSecret.Labels[argoCDTypeLabel])
				assert.Equal(t, "remote-cluster", remoteSecret.Labels[labelKeyClusterAgentMapping])
			},
		},
		{
			name: "skip cluster with local-cluster label",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gitops",
					Namespace: "test-ns",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "argocd",
					},
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "test-server.example.com",
							ServerPort:    "443",
						},
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "hub-cluster",
						Labels: map[string]string{
							"local-cluster": "true",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "managed-cluster",
					},
				},
			},
			orphanSecretsList: map[types.NamespacedName]string{},
			existingObjects: []client.Object{
				createTestPrincipalCASecret("argocd", caCert, caKey, caPEM),
			},
			expectedError:        false,
			expectedSecretsCount: 1,
			validateFunc: func(t *testing.T, c client.Client, orphanList map[types.NamespacedName]string) {
				// hub-cluster (with local-cluster label) should not have cluster secret
				hubSecret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{Name: "cluster-hub-cluster", Namespace: "argocd"}, hubSecret)
				assert.Error(t, err, "Cluster secret should not be created for cluster with local-cluster=true label")

				// managed-cluster should have cluster secret
				managedSecret := &v1.Secret{}
				err = c.Get(context.TODO(), types.NamespacedName{Name: "cluster-managed-cluster", Namespace: "argocd"}, managedSecret)
				assert.NoError(t, err, "Cluster secret should be created for managed-cluster")
				assert.Equal(t, "cluster", managedSecret.Labels[argoCDTypeLabel])
				assert.Equal(t, "managed-cluster", managedSecret.Labels[labelKeyClusterAgentMapping])
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

			err := reconciler.CreateArgoCDAgentClusters(tt.gitOpsCluster, tt.managedClusters, tt.orphanSecretsList)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient, tt.orphanSecretsList)
				}
			}
		})
	}
}

func TestGetArgoCDAgentServerConfig(t *testing.T) {
	agentEnabled := true
	tests := []struct {
		name            string
		gitOpsCluster   *gitopsclusterV1beta1.GitOpsCluster
		expectedAddress string
		expectedPort    string
		expectedError   bool
	}{
		{
			name: "returns configured server address and port",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled:       &agentEnabled,
							ServerAddress: "principal.example.com",
							ServerPort:    "443",
						},
					},
				},
			},
			expectedAddress: "principal.example.com",
			expectedPort:    "443",
			expectedError:   false,
		},
		{
			name: "returns default port 443 when not specified",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled:       &agentEnabled,
							ServerAddress: "172.18.255.201",
						},
					},
				},
			},
			expectedAddress: "172.18.255.201",
			expectedPort:    "443",
			expectedError:   false,
		},
		{
			name: "error when server address not configured",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							Enabled: &agentEnabled,
						},
					},
				},
			},
			expectedAddress: "",
			expectedPort:    "",
			expectedError:   true,
		},
		{
			name: "error when GitOpsAddon not configured",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{},
			},
			expectedAddress: "",
			expectedPort:    "",
			expectedError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &ReconcileGitOpsCluster{}

			address, port, err := reconciler.getArgoCDAgentServerConfig(tt.gitOpsCluster)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedAddress, address)
				assert.Equal(t, tt.expectedPort, port)
			}
		})
	}
}

func TestGenerateRandomPassword(t *testing.T) {
	password1, err := generateRandomPassword()
	require.NoError(t, err)
	assert.NotEmpty(t, password1)

	password2, err := generateRandomPassword()
	require.NoError(t, err)
	assert.NotEmpty(t, password2)

	// Passwords should be different
	assert.NotEqual(t, password1, password2)

	// Should be base64 encoded
	assert.Regexp(t, "^[A-Za-z0-9+/=]+$", password1)
	assert.Regexp(t, "^[A-Za-z0-9+/=]+$", password2)
}

func TestConstructServerURL(t *testing.T) {
	tests := []struct {
		name          string
		serverAddress string
		serverPort    string
		agentName     string
		expectedURL   string
		expectedError bool
	}{
		{
			name:          "valid IP address and port",
			serverAddress: "192.168.1.100",
			serverPort:    "443",
			agentName:     "cluster1",
			expectedURL:   "https://192.168.1.100:443?agentName=cluster1",
			expectedError: false,
		},
		{
			name:          "valid hostname and port",
			serverAddress: "test-server.example.com",
			serverPort:    "8443",
			agentName:     "cluster2",
			expectedURL:   "https://test-server.example.com:8443?agentName=cluster2",
			expectedError: false,
		},
		{
			name:          "invalid hostname",
			serverAddress: "invalid_hostname!",
			serverPort:    "443",
			agentName:     "cluster1",
			expectedError: true,
		},
		{
			name:          "invalid port",
			serverAddress: "test-server.example.com",
			serverPort:    "99999", // Port too high
			agentName:     "cluster1",
			expectedError: true,
		},
		{
			name:          "invalid agent name",
			serverAddress: "test-server.example.com",
			serverPort:    "443",
			agentName:     "INVALID_NAME!", // Invalid DNS subdomain
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, err := constructServerURL(tt.serverAddress, tt.serverPort, tt.agentName)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedURL, url)
			}
		})
	}
}

func TestIsValidAgentName(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		expected  bool
	}{
		{"valid lowercase", "cluster1", true},
		{"valid with hyphens", "cluster-1", true},
		{"valid subdomain", "my-cluster.example", true},
		{"invalid uppercase", "CLUSTER1", false},
		{"invalid underscore", "cluster_1", false},
		{"invalid special chars", "cluster@1", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidAgentName(tt.agentName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateAgentClientCertificate(t *testing.T) {
	// Create test CA certificate
	caCert, caKey, _ := generateTestCACertificate(t)

	reconciler := &ReconcileGitOpsCluster{}

	certPEM, keyPEM, err := reconciler.generateAgentClientCertificate("test-agent", caCert, caKey)
	require.NoError(t, err)
	assert.NotEmpty(t, certPEM)
	assert.NotEmpty(t, keyPEM)

	// Verify certificate is valid
	certBlock, _ := pem.Decode([]byte(certPEM))
	require.NotNil(t, certBlock)

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	require.NoError(t, err)

	assert.Equal(t, "test-agent", cert.Subject.CommonName)
	assert.Contains(t, cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	assert.False(t, cert.IsCA)

	// Verify key is valid
	keyBlock, _ := pem.Decode([]byte(keyPEM))
	require.NotNil(t, keyBlock)

	_, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	require.NoError(t, err)
}

func TestClusterToSecret(t *testing.T) {
	reconciler := &ReconcileGitOpsCluster{}

	cluster := &Cluster{
		Server: "https://test-server.example.com:443?agentName=cluster1",
		Name:   "cluster1",
		Labels: map[string]string{
			labelKeyClusterAgentMapping: "cluster1",
		},
		Config: ClusterConfig{
			Username: "cluster1",
			Password: "test-password",
			TLSClientConfig: TLSClientConfig{
				Insecure: false,
				CAData:   []byte("test-ca"),
				CertData: []byte("test-cert"),
				KeyData:  []byte("test-key"),
			},
		},
	}

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-cluster1",
			Namespace: "argocd",
		},
	}

	err := reconciler.clusterToSecret(cluster, secret)
	require.NoError(t, err)

	// Verify secret data
	assert.Equal(t, "https://test-server.example.com:443?agentName=cluster1", string(secret.Data["server"]))
	assert.Equal(t, "cluster1", string(secret.Data["name"]))

	// Verify labels
	assert.Equal(t, argoCDSecretTypeClusterValue, secret.Labels[argoCDTypeLabel])
	assert.Equal(t, "cluster1", secret.Labels[labelKeyClusterAgentMapping])

	// Verify annotations
	assert.Equal(t, labelValueManagerName, secret.Annotations[argoCDManagedByAnnotation])

	// Verify config is JSON marshaled
	var config ClusterConfig
	err = json.Unmarshal(secret.Data["config"], &config)
	require.NoError(t, err)
	assert.Equal(t, "cluster1", config.Username)
	assert.Equal(t, "test-password", config.Password)
}

func TestTLSClientConfig_Structure(t *testing.T) {
	// Test that TLSClientConfig structure is correctly nested in ClusterConfig
	config := ClusterConfig{
		Username: "test-user",
		Password: "test-pass",
		TLSClientConfig: TLSClientConfig{
			Insecure: false,
			CAData:   []byte("ca-cert-data"),
			CertData: []byte("client-cert-data"),
			KeyData:  []byte("client-key-data"),
		},
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(config)
	require.NoError(t, err)

	// Unmarshal back
	var parsedConfig ClusterConfig
	err = json.Unmarshal(jsonData, &parsedConfig)
	require.NoError(t, err)

	// Verify structure is preserved
	assert.Equal(t, "test-user", parsedConfig.Username)
	assert.Equal(t, "test-pass", parsedConfig.Password)
	assert.False(t, parsedConfig.TLSClientConfig.Insecure)
	assert.Equal(t, []byte("ca-cert-data"), parsedConfig.TLSClientConfig.CAData)
	assert.Equal(t, []byte("client-cert-data"), parsedConfig.TLSClientConfig.CertData)
	assert.Equal(t, []byte("client-key-data"), parsedConfig.TLSClientConfig.KeyData)

	// Verify JSON structure has nested tlsClientConfig
	jsonStr := string(jsonData)
	assert.Contains(t, jsonStr, `"tlsClientConfig"`)
	// Note: "insecure" is omitempty, so when false it won't be included in JSON
	assert.Contains(t, jsonStr, `"caData"`)
	assert.Contains(t, jsonStr, `"certData"`)
	assert.Contains(t, jsonStr, `"keyData"`)
}

func TestTLSClientConfig_WithInsecureTrue(t *testing.T) {
	// Test that Insecure:true is correctly serialized
	config := ClusterConfig{
		Username: "test-user",
		Password: "test-pass",
		TLSClientConfig: TLSClientConfig{
			Insecure: true, // When true, should be included in JSON
			CAData:   []byte("ca-cert-data"),
		},
	}

	jsonData, err := json.Marshal(config)
	require.NoError(t, err)

	jsonStr := string(jsonData)
	assert.Contains(t, jsonStr, `"insecure":true`)

	// Verify it can be unmarshaled correctly
	var parsedConfig ClusterConfig
	err = json.Unmarshal(jsonData, &parsedConfig)
	require.NoError(t, err)
	assert.True(t, parsedConfig.TLSClientConfig.Insecure)
}

func TestClusterConfig_EmptyTLS(t *testing.T) {
	// Test that empty TLSClientConfig is handled correctly
	config := ClusterConfig{
		Username: "test-user",
		Password: "test-pass",
	}

	jsonData, err := json.Marshal(config)
	require.NoError(t, err)

	var parsedConfig ClusterConfig
	err = json.Unmarshal(jsonData, &parsedConfig)
	require.NoError(t, err)

	assert.False(t, parsedConfig.TLSClientConfig.Insecure)
	assert.Nil(t, parsedConfig.TLSClientConfig.CAData)
	assert.Nil(t, parsedConfig.TLSClientConfig.CertData)
	assert.Nil(t, parsedConfig.TLSClientConfig.KeyData)
}

// Helper functions for testing

func generateTestCACertificate(t *testing.T) (*x509.Certificate, *rsa.PrivateKey, string) {
	// Generate CA private key
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Create CA certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "Test CA",
			Organization: []string{"Test Organization"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	// Create CA certificate
	certBytes, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	caCert, err := x509.ParseCertificate(certBytes)
	require.NoError(t, err)

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})

	return caCert, caKey, string(certPEM)
}

func createTestPrincipalCASecret(namespace string, caCert *x509.Certificate, caKey *rsa.PrivateKey, caPEM string) *v1.Secret {
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caKey),
	})

	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      principalCAName,
			Namespace: namespace,
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte(caPEM),
			"tls.key": keyPEM,
		},
	}
}
