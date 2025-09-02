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

	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
)

// setupScheme initializes the scheme for testing
func setupCertScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	v1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	return scheme
}

// generateTestCA creates a test CA certificate and key for testing
func generateTestCA() ([]byte, []byte, *x509.Certificate, *rsa.PrivateKey, error) {
	// Generate CA private key
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Generate random serial number for test CA
	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Create CA certificate template
	caTemplate := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "test-ca",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	// Generate CA certificate
	caCertDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// Encode to PEM
	caCertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCertDER,
	})

	caKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caKey),
	})

	return caCertPEM, caKeyPEM, caCert, caKey, nil
}

func TestEnsureArgoCDAgentCertificates(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Generate test CA
	caCertPEM, caKeyPEM, _, _, err := generateTestCA()
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Create CA secret
	caSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "test-namespace",
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": caCertPEM,
			"tls.key": caKeyPEM,
		},
	}

	// Create ArgoCD agent principal service
	argoCDService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/component": "agent-principal",
				"app.kubernetes.io/part-of":   "argocd",
			},
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "10.0.0.1",
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{IP: "192.168.1.100", Hostname: "test.example.com"},
				},
			},
		},
	}

	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "test-namespace",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(caSecret, argoCDService).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test successful certificate creation
	err = reconciler.EnsureArgoCDAgentCertificates(testGitOpsCluster)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify both certificates were created
	principalSecret := &v1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-principal-tls",
		Namespace: "test-namespace",
	}, principalSecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(principalSecret.Type).To(gomega.Equal(v1.SecretTypeTLS))

	resourceProxySecret := &v1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-resource-proxy-tls",
		Namespace: "test-namespace",
	}, resourceProxySecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(resourceProxySecret.Type).To(gomega.Equal(v1.SecretTypeTLS))
}

func TestEnsureArgoCDAgentCertificatesWithDefaults(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Generate test CA
	caCertPEM, caKeyPEM, _, _, err := generateTestCA()
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Create CA secret in default namespace
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

	// Create ArgoCD agent principal service in default namespace
	argoCDService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "openshift-gitops",
			Labels: map[string]string{
				"app.kubernetes.io/component": "agent-principal",
				"app.kubernetes.io/part-of":   "argocd",
			},
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "10.0.0.1",
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{IP: "192.168.1.100", Hostname: "test.example.com"},
				},
			},
		},
	}

	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "", // Empty should default to openshift-gitops
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(caSecret, argoCDService).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test with default namespace
	err = reconciler.EnsureArgoCDAgentCertificates(testGitOpsCluster)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify certificates were created in default namespace
	principalSecret := &v1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-principal-tls",
		Namespace: "openshift-gitops",
	}, principalSecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
}

func TestEnsureArgoCDAgentCertificatesSkipExisting(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Generate test CA
	caCertPEM, caKeyPEM, _, _, err := generateTestCA()
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Create CA secret
	caSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "test-namespace",
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": caCertPEM,
			"tls.key": caKeyPEM,
		},
	}

	// Create ArgoCD agent principal service
	argoCDService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/component": "agent-principal",
				"app.kubernetes.io/part-of":   "argocd",
			},
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "10.0.0.1",
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{IP: "192.168.1.100", Hostname: "test.example.com"},
				},
			},
		},
	}

	// Create existing principal certificate
	existingPrincipalSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-principal-tls",
			Namespace: "test-namespace",
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("existing-cert"),
			"tls.key": []byte("existing-key"),
		},
	}

	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "test-namespace",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(caSecret, argoCDService, existingPrincipalSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test that existing certificates are not overwritten
	err = reconciler.EnsureArgoCDAgentCertificates(testGitOpsCluster)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify existing certificate was not modified
	principalSecret := &v1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-principal-tls",
		Namespace: "test-namespace",
	}, principalSecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(string(principalSecret.Data["tls.crt"])).To(gomega.Equal("existing-cert"))
	g.Expect(string(principalSecret.Data["tls.key"])).To(gomega.Equal("existing-key"))
}

func TestDiscoverPrincipalEndpoints(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with ArgoCD agent principal service with LoadBalancer
	agentPrincipalService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "10.0.0.1",
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{IP: "192.168.1.100"},
					{Hostname: "test.example.com"},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(agentPrincipalService).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	endpoints, err := reconciler.discoverPrincipalEndpoints("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify discovered endpoints - principal should only have external endpoints
	g.Expect(len(endpoints.IPs)).To(gomega.BeNumerically(">=", 1))      // LoadBalancer IP (DNS resolution will be mocked/tested separately)
	g.Expect(len(endpoints.DNSNames)).To(gomega.BeNumerically(">=", 1)) // LoadBalancer hostname

	// Check specific values - should find LoadBalancer IP
	foundLBIP := false

	for _, ip := range endpoints.IPs {
		if ip.String() == "192.168.1.100" {
			foundLBIP = true
		}
	}

	g.Expect(foundLBIP).To(gomega.BeTrue())

	// Should find LoadBalancer hostname in DNS names
	foundLBHostname := false

	for _, dns := range endpoints.DNSNames {
		if dns == "test.example.com" {
			foundLBHostname = true
		}
	}

	g.Expect(foundLBHostname).To(gomega.BeTrue())
}

func TestDiscoverPrincipalEndpointsNoService(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	_, err := reconciler.discoverPrincipalEndpoints("test-namespace")
	// Should now return an error when service is not found (principal service is essential)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("failed to find ArgoCD agent principal service"))
	g.Expect(err.Error()).To(gomega.ContainSubstring("required for principal certificate"))
}

func TestDiscoverResourceProxyEndpoints(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with ArgoCD agent principal service (internal access)
	agentPrincipalService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "10.0.0.1",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(agentPrincipalService).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	endpoints, err := reconciler.discoverResourceProxyEndpoints("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify internal endpoints
	g.Expect(len(endpoints.IPs)).To(gomega.BeNumerically(">=", 1))
	g.Expect(len(endpoints.DNSNames)).To(gomega.BeNumerically(">=", 1))

	foundClusterIP := false

	for _, ip := range endpoints.IPs {
		if ip.String() == "10.0.0.1" {
			foundClusterIP = true
		}
	}

	g.Expect(foundClusterIP).To(gomega.BeTrue())

	foundInternalDNS := false

	for _, dns := range endpoints.DNSNames {
		if dns == "openshift-gitops-agent-principal.test-namespace.svc.cluster.local" {
			foundInternalDNS = true
		}
	}
	g.Expect(foundInternalDNS).To(gomega.BeTrue())
}

func TestDiscoverResourceProxyEndpointsNoService(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	_, err := reconciler.discoverResourceProxyEndpoints("test-namespace")
	// Should now return an error when service is not found (principal service is essential for resource proxy too)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("failed to find ArgoCD agent principal service"))
	g.Expect(err.Error()).To(gomega.ContainSubstring("required for resource proxy certificate"))
}

func TestDiscoverResourceProxyEndpointsHeadlessService(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with headless service (no ClusterIP)
	agentPrincipalService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "None", // Headless service
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(agentPrincipalService).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	endpoints, err := reconciler.discoverResourceProxyEndpoints("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Should have no IPs (headless service) but should have DNS name
	g.Expect(len(endpoints.IPs)).To(gomega.Equal(0))
	g.Expect(len(endpoints.DNSNames)).To(gomega.BeNumerically(">=", 1))

	foundInternalDNS := false
	for _, dns := range endpoints.DNSNames {
		if dns == "openshift-gitops-agent-principal.test-namespace.svc.cluster.local" {
			foundInternalDNS = true
		}
	}
	g.Expect(foundInternalDNS).To(gomega.BeTrue())
}

func TestFindArgoCDAgentPrincipalService(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with correct service name
	agentPrincipalService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "10.0.0.1",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(agentPrincipalService).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	service, err := reconciler.findArgoCDAgentPrincipalService("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(service).NotTo(gomega.BeNil())
	g.Expect(service.Name).To(gomega.Equal("openshift-gitops-agent-principal"))
}

func TestFindArgoCDAgentPrincipalServiceNotFound(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	_, err := reconciler.findArgoCDAgentPrincipalService("test-namespace")
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("ArgoCD agent principal service not found"))
}

func TestContainsIP(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	reconciler := &ReconcileGitOpsCluster{}

	ips := []net.IP{
		net.ParseIP("192.168.1.1"),
		net.ParseIP("10.0.0.1"),
		net.ParseIP("172.16.0.1"),
	}

	// Test IP that exists
	g.Expect(reconciler.containsIP(ips, net.ParseIP("192.168.1.1"))).To(gomega.BeTrue())
	g.Expect(reconciler.containsIP(ips, net.ParseIP("10.0.0.1"))).To(gomega.BeTrue())

	// Test IP that doesn't exist
	g.Expect(reconciler.containsIP(ips, net.ParseIP("1.1.1.1"))).To(gomega.BeFalse())

	// Test with empty slice
	g.Expect(reconciler.containsIP([]net.IP{}, net.ParseIP("192.168.1.1"))).To(gomega.BeFalse())
}

func TestDiscoverPrincipalEndpointsOnlyHostname(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with service that has only hostname (no IP)
	agentPrincipalService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{Hostname: "test.example.com"}, // Only hostname, no IP
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(agentPrincipalService).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	endpoints, err := reconciler.discoverPrincipalEndpoints("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Should have DNS name but may not have IPs (depending on DNS resolution)
	g.Expect(len(endpoints.DNSNames)).To(gomega.BeNumerically(">=", 1))

	foundHostname := false
	for _, dns := range endpoints.DNSNames {
		if dns == "test.example.com" {
			foundHostname = true
		}
	}
	g.Expect(foundHostname).To(gomega.BeTrue())
}

func TestDiscoverPrincipalEndpointsNoLoadBalancer(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with service that has no LoadBalancer ingress
	agentPrincipalService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP, // Not LoadBalancer
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(agentPrincipalService).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	_, err := reconciler.discoverPrincipalEndpoints("test-namespace")
	// Should error because no external endpoints found
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("no external endpoints found"))
}

func TestCheckCertificateExists(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with valid TLS secret
	tlsSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cert",
			Namespace: "test-namespace",
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(tlsSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	exists, err := reconciler.checkCertificateExists("test-cert", "test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(exists).To(gomega.BeTrue())

	// Test with non-existent secret
	exists, err = reconciler.checkCertificateExists("non-existent", "test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(exists).To(gomega.BeFalse())
}

func TestCheckCertificateExistsIncomplete(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with incomplete TLS secret (missing key)
	incompleteSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "incomplete-cert",
			Namespace: "test-namespace",
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			// Missing tls.key
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(incompleteSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	exists, err := reconciler.checkCertificateExists("incomplete-cert", "test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(exists).To(gomega.BeFalse())
}

func TestGetArgoCDAgentCA(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Generate test CA
	caCertPEM, caKeyPEM, expectedCert, expectedKey, err := generateTestCA()
	g.Expect(err).NotTo(gomega.HaveOccurred())

	caSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "test-namespace",
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": caCertPEM,
			"tls.key": caKeyPEM,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(caSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	caCert, caKey, err := reconciler.getArgoCDAgentCA("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(caCert).NotTo(gomega.BeNil())
	g.Expect(caKey).NotTo(gomega.BeNil())

	// Verify the parsed certificate matches
	g.Expect(caCert.Subject.CommonName).To(gomega.Equal(expectedCert.Subject.CommonName))
	g.Expect(caKey.N).To(gomega.Equal(expectedKey.N))
}

func TestGetArgoCDAgentCAMissing(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	_, _, err := reconciler.getArgoCDAgentCA("test-namespace")
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("failed to get argocd-agent-ca secret"))
}

func TestGenerateTLSCertificate(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Generate test CA
	_, _, caCert, caKey, err := generateTestCA()
	g.Expect(err).NotTo(gomega.HaveOccurred())

	reconciler := &ReconcileGitOpsCluster{}

	ips := []net.IP{net.ParseIP("192.168.1.1"), net.ParseIP("10.0.0.1")}
	dnsNames := []string{"test.example.com", "service.namespace.svc.cluster.local"}

	certPEM, keyPEM, err := reconciler.generateTLSCertificate(caCert, caKey, "test-cert", ips, dnsNames)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(certPEM).NotTo(gomega.BeEmpty())
	g.Expect(keyPEM).NotTo(gomega.BeEmpty())

	// Parse and verify the generated certificate
	certBlock, _ := pem.Decode(certPEM)
	g.Expect(certBlock).NotTo(gomega.BeNil())

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(cert.Subject.CommonName).To(gomega.Equal("test-cert"))

	// Check IP addresses more robustly - convert to strings for comparison
	// since net.IP representation can vary (IPv4 vs IPv6-mapped IPv4)
	expectedIPStrings := []string{"192.168.1.1", "10.0.0.1"}
	actualIPStrings := make([]string, len(cert.IPAddresses))
	for i, ip := range cert.IPAddresses {
		actualIPStrings[i] = ip.String()
	}
	g.Expect(actualIPStrings).To(gomega.Equal(expectedIPStrings))

	g.Expect(cert.DNSNames).To(gomega.Equal(dnsNames))
}

func TestCreateTLSSecret(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	certData := []byte("test-cert-data")
	keyData := []byte("test-key-data")

	err := reconciler.createTLSSecret("test-secret", "test-namespace", certData, keyData)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify secret was created
	secret := &v1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "test-secret",
		Namespace: "test-namespace",
	}, secret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(secret.Type).To(gomega.Equal(v1.SecretTypeTLS))
	g.Expect(secret.Data["tls.crt"]).To(gomega.Equal(certData))
	g.Expect(secret.Data["tls.key"]).To(gomega.Equal(keyData))
}

func TestCreateTLSSecretAlreadyExists(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create existing secret
	existingSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-secret",
			Namespace: "test-namespace",
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("existing-cert"),
			"tls.key": []byte("existing-key"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupCertScheme()).WithObjects(existingSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Try to create secret with same name - should succeed without error
	err := reconciler.createTLSSecret("existing-secret", "test-namespace", []byte("new-cert"), []byte("new-key"))
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify existing secret was not modified
	secret := &v1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "existing-secret",
		Namespace: "test-namespace",
	}, secret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(string(secret.Data["tls.crt"])).To(gomega.Equal("existing-cert"))
	g.Expect(string(secret.Data["tls.key"])).To(gomega.Equal("existing-key"))
}

func TestGenerateSerialNumber(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Generate multiple serial numbers and verify they're unique
	serialNumbers := make(map[string]bool)

	for range 100 {
		serialNumber, err := generateSerialNumber()
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(serialNumber).NotTo(gomega.BeNil())

		// Verify serial number is not zero
		g.Expect(serialNumber.Cmp(big.NewInt(0))).To(gomega.BeNumerically(">", 0))

		// Verify serial number is positive (should be less than 2^127)
		// This is more reliable than checking the high bit of the first byte
		// since big.Int.Bytes() doesn't include leading zeros
		maxPositive := new(big.Int).Lsh(big.NewInt(1), 127) // 2^127
		g.Expect(serialNumber.Cmp(maxPositive)).To(gomega.BeNumerically("<", 0))

		// Verify uniqueness (very high probability with 128-bit random numbers)
		serialStr := serialNumber.String()
		g.Expect(serialNumbers[serialStr]).To(gomega.BeFalse(), "Serial number collision detected: %s", serialStr)
		serialNumbers[serialStr] = true
	}
}

func TestGenerateTLSCertificateUniqueSerials(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Generate test CA
	_, _, caCert, caKey, err := generateTestCA()
	g.Expect(err).NotTo(gomega.HaveOccurred())

	reconciler := &ReconcileGitOpsCluster{}

	// Generate multiple certificates and verify they have unique serial numbers
	serialNumbers := make(map[string]bool)

	for range 10 {
		certPEM, keyPEM, err := reconciler.generateTLSCertificate(caCert, caKey, "test-cert", nil, nil)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(certPEM).NotTo(gomega.BeEmpty())
		g.Expect(keyPEM).NotTo(gomega.BeEmpty())

		// Parse and verify the generated certificate has unique serial
		certBlock, _ := pem.Decode(certPEM)
		g.Expect(certBlock).NotTo(gomega.BeNil())

		cert, err := x509.ParseCertificate(certBlock.Bytes)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		serialStr := cert.SerialNumber.String()
		g.Expect(serialNumbers[serialStr]).To(gomega.BeFalse(), "Certificate serial number collision: %s", serialStr)
		serialNumbers[serialStr] = true

		// Verify serial number is positive
		g.Expect(cert.SerialNumber.Cmp(big.NewInt(0))).To(gomega.BeNumerically(">", 0))
	}
}

func TestGetArgoCDAgentPrincipalTLSSecretName(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Save original environment variable value if it exists
	originalValue := os.Getenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME")
	defer func() {
		if originalValue != "" {
			os.Setenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME", originalValue)
		} else {
			os.Unsetenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME")
		}
	}()

	t.Run("default behavior - no environment variable", func(t *testing.T) {
		// Ensure environment variable is not set
		os.Unsetenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME")

		result := getArgoCDAgentPrincipalTLSSecretName()
		expected := "argocd-agent-principal-tls"
		g.Expect(result).To(gomega.Equal(expected))
	})

	t.Run("environment variable override", func(t *testing.T) {
		customSecretName := "my-custom-principal-tls-secret"
		os.Setenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME", customSecretName)

		result := getArgoCDAgentPrincipalTLSSecretName()
		g.Expect(result).To(gomega.Equal(customSecretName))
	})

	t.Run("empty environment variable falls back to default", func(t *testing.T) {
		// Set environment variable to empty string
		os.Setenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME", "")

		result := getArgoCDAgentPrincipalTLSSecretName()
		expected := "argocd-agent-principal-tls"
		g.Expect(result).To(gomega.Equal(expected))
	})
}

func TestGetArgoCDAgentResourceProxyTLSSecretName(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Save original environment variable value if it exists
	originalValue := os.Getenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME")
	defer func() {
		if originalValue != "" {
			os.Setenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME", originalValue)
		} else {
			os.Unsetenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME")
		}
	}()

	t.Run("default behavior - no environment variable", func(t *testing.T) {
		// Ensure environment variable is not set
		os.Unsetenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME")

		result := getArgoCDAgentResourceProxyTLSSecretName()
		expected := "argocd-agent-resource-proxy-tls"
		g.Expect(result).To(gomega.Equal(expected))
	})

	t.Run("environment variable override", func(t *testing.T) {
		customSecretName := "my-custom-resource-proxy-tls-secret"
		os.Setenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME", customSecretName)

		result := getArgoCDAgentResourceProxyTLSSecretName()
		g.Expect(result).To(gomega.Equal(customSecretName))
	})

	t.Run("empty environment variable falls back to default", func(t *testing.T) {
		// Set environment variable to empty string
		os.Setenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME", "")

		result := getArgoCDAgentResourceProxyTLSSecretName()
		expected := "argocd-agent-resource-proxy-tls"
		g.Expect(result).To(gomega.Equal(expected))
	})
}
