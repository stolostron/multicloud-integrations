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
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetArgoCDAgentPrincipalTLSSecretName(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{
			name:     "default name when env var not set",
			envValue: "",
			expected: "argocd-agent-principal-tls",
		},
		{
			name:     "custom name from env var",
			envValue: "custom-principal-tls",
			expected: "custom-principal-tls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original env var
			originalValue := os.Getenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME")
			defer os.Setenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME", originalValue)

			if tt.envValue != "" {
				os.Setenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME", tt.envValue)
			} else {
				os.Unsetenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME")
			}

			result := getArgoCDAgentPrincipalTLSSecretName()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetArgoCDAgentResourceProxyTLSSecretName(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{
			name:     "default name when env var not set",
			envValue: "",
			expected: "argocd-agent-resource-proxy-tls",
		},
		{
			name:     "custom name from env var",
			envValue: "custom-proxy-tls",
			expected: "custom-proxy-tls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original env var
			originalValue := os.Getenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME")
			defer os.Setenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME", originalValue)

			if tt.envValue != "" {
				os.Setenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME", tt.envValue)
			} else {
				os.Unsetenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME")
			}

			result := getArgoCDAgentResourceProxyTLSSecretName()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEnsureArgoCDAgentCertificates(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)
	err = gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	// Create test CA certificate and key
	caCert, caKey := createTestCA(t)
	caCertPEM := pemEncodeCertificate(caCert)
	caKeyPEM := pemEncodePrivateKey(caKey)

	// Create test ArgoCD agent principal service
	principalService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "openshift-gitops",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "10.0.0.100",
			Ports: []v1.ServicePort{
				{Name: "https", Port: 443},
			},
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{Hostname: "test-server.example.com"},
				},
			},
		},
	}

	// Create CA secret
	caSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "openshift-gitops",
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": caCertPEM,
			"tls.key": caKeyPEM,
		},
	}

	tests := []struct {
		name            string
		gitOpsCluster   *gitopsclusterV1beta1.GitOpsCluster
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client)
	}{
		{
			name: "create both principal and resource proxy certificates",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "openshift-gitops",
					},
				},
			},
			existingObjects: []client.Object{principalService, caSecret},
			validateFunc: func(t *testing.T, c client.Client) {
				// Check principal TLS secret
				principalSecret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-principal-tls",
					Namespace: "openshift-gitops",
				}, principalSecret)
				assert.NoError(t, err)
				assert.Equal(t, v1.SecretTypeTLS, principalSecret.Type)
				assert.NotEmpty(t, principalSecret.Data["tls.crt"])
				assert.NotEmpty(t, principalSecret.Data["tls.key"])

				// Check resource proxy TLS secret
				proxySecret := &v1.Secret{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-resource-proxy-tls",
					Namespace: "openshift-gitops",
				}, proxySecret)
				assert.NoError(t, err)
				assert.Equal(t, v1.SecretTypeTLS, proxySecret.Type)
				assert.NotEmpty(t, proxySecret.Data["tls.crt"])
				assert.NotEmpty(t, proxySecret.Data["tls.key"])
			},
		},
		{
			name: "certificates already exist - no error",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "openshift-gitops",
					},
				},
			},
			existingObjects: []client.Object{
				principalService,
				caSecret,
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-principal-tls",
						Namespace: "openshift-gitops",
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": []byte("existing-cert"),
						"tls.key": []byte("existing-key"),
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-resource-proxy-tls",
						Namespace: "openshift-gitops",
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": []byte("existing-cert"),
						"tls.key": []byte("existing-key"),
					},
				},
			},
		},
		{
			name: "use default namespace when not specified",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{},
				},
			},
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Spec: v1.ServiceSpec{
						Type:      v1.ServiceTypeLoadBalancer,
						ClusterIP: "10.0.0.100",
					},
					Status: v1.ServiceStatus{
						LoadBalancer: v1.LoadBalancerStatus{
							Ingress: []v1.LoadBalancerIngress{
								{
									IP: "192.168.1.100",
								},
							},
						},
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-ca",
						Namespace: "openshift-gitops",
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": caCertPEM,
						"tls.key": caKeyPEM,
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

			err := reconciler.EnsureArgoCDAgentCertificates(tt.gitOpsCluster)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient)
				}
			}
		})
	}
}

func TestDiscoverPrincipalEndpoints(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, endpoints *ServiceEndpoints)
	}{
		{
			name: "discover endpoints from LoadBalancer service",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Status: v1.ServiceStatus{
						LoadBalancer: v1.LoadBalancerStatus{
							Ingress: []v1.LoadBalancerIngress{
								{Hostname: "test-server.example.com"},
								{IP: "192.168.1.100"},
							},
						},
					},
				},
			},
			validateFunc: func(t *testing.T, endpoints *ServiceEndpoints) {
				assert.Contains(t, endpoints.DNSNames, "test-server.example.com")
				// IP addresses depend on DNS resolution which may not work in tests
				assert.NotNil(t, endpoints.IPs)
			},
		},
		{
			name:          "service not found should return error",
			expectedError: true,
		},
		{
			name: "service with no external endpoints should return error",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Status: v1.ServiceStatus{
						LoadBalancer: v1.LoadBalancerStatus{
							Ingress: []v1.LoadBalancerIngress{},
						},
					},
				},
			},
			expectedError: true,
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

			endpoints, err := reconciler.discoverPrincipalEndpoints("openshift-gitops")

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, endpoints)
				}
			}
		})
	}
}

func TestDiscoverResourceProxyEndpoints(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, endpoints *ServiceEndpoints)
	}{
		{
			name: "discover endpoints from ClusterIP service",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Spec: v1.ServiceSpec{
						ClusterIP: "10.0.0.100",
					},
				},
			},
			validateFunc: func(t *testing.T, endpoints *ServiceEndpoints) {
				assert.Contains(t, endpoints.DNSNames, "openshift-gitops-agent-principal.openshift-gitops.svc.cluster.local")
				assert.Len(t, endpoints.IPs, 1)
				assert.Equal(t, "10.0.0.100", endpoints.IPs[0].String())
			},
		},
		{
			name: "headless service - DNS only",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Spec: v1.ServiceSpec{
						ClusterIP: "None",
					},
				},
			},
			validateFunc: func(t *testing.T, endpoints *ServiceEndpoints) {
				assert.Contains(t, endpoints.DNSNames, "openshift-gitops-agent-principal.openshift-gitops.svc.cluster.local")
				assert.Empty(t, endpoints.IPs)
			},
		},
		{
			name:          "service not found should return error",
			expectedError: true,
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

			endpoints, err := reconciler.discoverResourceProxyEndpoints("openshift-gitops")

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, endpoints)
				}
			}
		})
	}
}

func TestFindArgoCDAgentPrincipalService(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectedError   bool
		expectedName    string
	}{
		{
			name: "find service by primary name",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
				},
			},
			expectedName: "openshift-gitops-agent-principal",
		},
		{
			name: "find service by fallback name",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-principal",
						Namespace: "openshift-gitops",
					},
				},
			},
			expectedName: "argocd-agent-principal",
		},
		{
			name:          "no service found should return error",
			expectedError: true,
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

			service, err := reconciler.findArgoCDAgentPrincipalService("openshift-gitops")

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedName, service.Name)
			}
		})
	}
}

func TestResolveHostname(t *testing.T) {
	reconciler := &ReconcileGitOpsCluster{}

	tests := []struct {
		name     string
		hostname string
		validate func(t *testing.T, ips []net.IP, err error)
	}{
		{
			name:     "resolve IP address",
			hostname: "192.168.1.1",
			validate: func(t *testing.T, ips []net.IP, err error) {
				assert.NoError(t, err)
				assert.Len(t, ips, 1)
				assert.Equal(t, "192.168.1.1", ips[0].String())
			},
		},
		{
			name:     "resolve localhost",
			hostname: "localhost",
			validate: func(t *testing.T, ips []net.IP, err error) {
				// This may or may not work depending on the test environment
				// Just ensure it doesn't panic
				if err != nil {
					t.Logf("Expected hostname resolution failure for localhost: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ips, err := reconciler.resolveHostname(tt.hostname)
			tt.validate(t, ips, err)
		})
	}
}

func TestContainsIP(t *testing.T) {
	reconciler := &ReconcileGitOpsCluster{}

	ip1 := net.ParseIP("192.168.1.1")
	ip2 := net.ParseIP("192.168.1.2")
	ip3 := net.ParseIP("192.168.1.3")

	tests := []struct {
		name     string
		ips      []net.IP
		target   net.IP
		expected bool
	}{
		{
			name:     "IP found in list",
			ips:      []net.IP{ip1, ip2},
			target:   ip1,
			expected: true,
		},
		{
			name:     "IP not found in list",
			ips:      []net.IP{ip1, ip2},
			target:   ip3,
			expected: false,
		},
		{
			name:     "empty list",
			ips:      []net.IP{},
			target:   ip1,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.containsIP(tt.ips, tt.target)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCheckCertificateExists(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		secretName      string
		namespace       string
		existingObjects []client.Object
		expectedExists  bool
		expectedError   bool
	}{
		{
			name:       "valid TLS secret exists",
			secretName: "test-tls",
			namespace:  "test-ns",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-tls",
						Namespace: "test-ns",
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": []byte("cert-data"),
						"tls.key": []byte("key-data"),
					},
				},
			},
			expectedExists: true,
		},
		{
			name:       "secret exists but missing tls.key",
			secretName: "test-tls",
			namespace:  "test-ns",
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-tls",
						Namespace: "test-ns",
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": []byte("cert-data"),
					},
				},
			},
			expectedExists: false,
		},
		{
			name:           "secret does not exist",
			secretName:     "test-tls",
			namespace:      "test-ns",
			expectedExists: false,
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

			exists, err := reconciler.checkCertificateExists(tt.secretName, tt.namespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedExists, exists)
			}
		})
	}
}

func TestGenerateSerialNumber(t *testing.T) {
	// Test that we can generate serial numbers successfully
	serial1, err := generateSerialNumber()
	assert.NoError(t, err)
	assert.NotNil(t, serial1)
	assert.True(t, serial1.Cmp(big.NewInt(0)) > 0, "Serial number should be positive")

	// Generate another and ensure they're different
	serial2, err := generateSerialNumber()
	assert.NoError(t, err)
	assert.NotNil(t, serial2)
	assert.NotEqual(t, serial1, serial2, "Serial numbers should be unique")
}

func TestGenerateTLSCertificate(t *testing.T) {
	// Create test CA
	caCert, caKey := createTestCA(t)

	reconciler := &ReconcileGitOpsCluster{}

	tests := []struct {
		name         string
		commonName   string
		ips          []net.IP
		dnsNames     []string
		validateFunc func(t *testing.T, certPEM, keyPEM []byte)
	}{
		{
			name:       "generate certificate with IPs and DNS names",
			commonName: "test-cert",
			ips:        []net.IP{net.ParseIP("192.168.1.1")},
			dnsNames:   []string{"test.example.com"},
			validateFunc: func(t *testing.T, certPEM, keyPEM []byte) {
				// Parse and validate certificate
				block, _ := pem.Decode(certPEM)
				require.NotNil(t, block)

				cert, err := x509.ParseCertificate(block.Bytes)
				require.NoError(t, err)

				assert.Equal(t, "test-cert", cert.Subject.CommonName)

				// Check if the expected IP is in the certificate's IP addresses
				expectedIP := net.ParseIP("192.168.1.1")
				found := false
				for _, ip := range cert.IPAddresses {
					if ip.Equal(expectedIP) {
						found = true
						break
					}
				}
				assert.True(t, found, "Expected IP %s not found in certificate IP addresses %v", expectedIP, cert.IPAddresses)

				assert.Contains(t, cert.DNSNames, "test.example.com")

				// Validate that certificate is signed by CA
				roots := x509.NewCertPool()
				roots.AddCert(caCert)
				opts := x509.VerifyOptions{Roots: roots}
				_, err = cert.Verify(opts)
				assert.NoError(t, err)
			},
		},
		{
			name:       "generate certificate with DNS names only",
			commonName: "dns-only-cert",
			dnsNames:   []string{"dns-only.example.com", "alt.example.com"},
			validateFunc: func(t *testing.T, certPEM, keyPEM []byte) {
				block, _ := pem.Decode(certPEM)
				cert, err := x509.ParseCertificate(block.Bytes)
				require.NoError(t, err)

				assert.Equal(t, "dns-only-cert", cert.Subject.CommonName)
				assert.Empty(t, cert.IPAddresses)
				assert.Len(t, cert.DNSNames, 2)
				assert.Contains(t, cert.DNSNames, "dns-only.example.com")
				assert.Contains(t, cert.DNSNames, "alt.example.com")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			certPEM, keyPEM, err := reconciler.generateTLSCertificate(caCert, caKey, tt.commonName, tt.ips, tt.dnsNames)

			assert.NoError(t, err)
			assert.NotEmpty(t, certPEM)
			assert.NotEmpty(t, keyPEM)

			if tt.validateFunc != nil {
				tt.validateFunc(t, certPEM, keyPEM)
			}
		})
	}
}

func TestCreateTLSSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		secretName      string
		namespace       string
		cert            []byte
		key             []byte
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client)
	}{
		{
			name:       "create new TLS secret",
			secretName: "test-tls",
			namespace:  "test-ns",
			cert:       []byte("test-cert"),
			key:        []byte("test-key"),
			validateFunc: func(t *testing.T, c client.Client) {
				secret := &v1.Secret{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "test-tls",
					Namespace: "test-ns",
				}, secret)
				assert.NoError(t, err)
				assert.Equal(t, v1.SecretTypeTLS, secret.Type)
				assert.Equal(t, []byte("test-cert"), secret.Data["tls.crt"])
				assert.Equal(t, []byte("test-key"), secret.Data["tls.key"])
			},
		},
		{
			name:       "secret already exists - no error",
			secretName: "existing-tls",
			namespace:  "test-ns",
			cert:       []byte("test-cert"),
			key:        []byte("test-key"),
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "existing-tls",
						Namespace: "test-ns",
					},
					Type: v1.SecretTypeTLS,
					Data: map[string][]byte{
						"tls.crt": []byte("existing-cert"),
						"tls.key": []byte("existing-key"),
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

			err := reconciler.createTLSSecret(tt.secretName, tt.namespace, tt.cert, tt.key)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient)
				}
			}
		})
	}
}

// Helper functions for testing

func createTestCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	// Generate CA private key
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Create CA certificate template
	caTemplate := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "Test CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	// Create CA certificate
	caCertDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)

	caCert, err := x509.ParseCertificate(caCertDER)
	require.NoError(t, err)

	return caCert, caKey
}

func pemEncodeCertificate(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
}

func pemEncodePrivateKey(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}
