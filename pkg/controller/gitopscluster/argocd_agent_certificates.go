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
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	v1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"

	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
)

// ServiceEndpoints represents discovered service endpoints
type ServiceEndpoints struct {
	IPs      []net.IP
	DNSNames []string
}

// getArgoCDAgentPrincipalTLSSecretName returns the principal TLS secret name, allowing override via environment variable
func getArgoCDAgentPrincipalTLSSecretName() string {
	if name := os.Getenv("ARGOCD_AGENT_PRINCIPAL_TLS_SECRET_NAME"); name != "" {
		return name
	}

	return "argocd-agent-principal-tls"
}

// getArgoCDAgentResourceProxyTLSSecretName returns the resource proxy TLS secret name, allowing override via environment variable
func getArgoCDAgentResourceProxyTLSSecretName() string {
	if name := os.Getenv("ARGOCD_AGENT_RESOURCE_PROXY_TLS_SECRET_NAME"); name != "" {
		return name
	}

	return "argocd-agent-resource-proxy-tls"
}

// EnsureArgoCDAgentCertificates ensures that both principal and resource-proxy TLS certificates are signed
func (r *ReconcileGitOpsCluster) EnsureArgoCDAgentCertificates(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	argoNamespace := gitOpsCluster.Spec.ArgoServer.ArgoNamespace
	if argoNamespace == "" {
		argoNamespace = "openshift-gitops"
	}

	// Ensure principal TLS certificate
	if err := r.ensureArgoCDAgentPrincipalTLSCert(argoNamespace); err != nil {
		return fmt.Errorf("failed to ensure principal TLS certificate: %w", err)
	}

	// Ensure resource proxy TLS certificate
	if err := r.ensureArgoCDAgentResourceProxyTLSCert(argoNamespace); err != nil {
		return fmt.Errorf("failed to ensure resource proxy TLS certificate: %w", err)
	}

	return nil
}

// ensureArgoCDAgentPrincipalTLSCert ensures the argocd-agent-principal-tls certificate exists
func (r *ReconcileGitOpsCluster) ensureArgoCDAgentPrincipalTLSCert(argoNamespace string) error {
	secretName := getArgoCDAgentPrincipalTLSSecretName()

	// Check if certificate already exists
	if exists, err := r.checkCertificateExists(secretName, argoNamespace); err != nil {
		return err
	} else if exists {
		klog.Infof("%s certificate already exists", secretName)
		return nil
	}

	klog.Infof("Creating %s certificate...", secretName)

	// Discover principal endpoints (external ArgoCD service)
	endpoints, err := r.discoverPrincipalEndpoints(argoNamespace)
	if err != nil {
		return fmt.Errorf("failed to discover principal endpoints: %w", err)
	}

	// Get CA certificate and key
	caCert, caKey, err := r.getArgoCDAgentCA(argoNamespace)
	if err != nil {
		return fmt.Errorf("failed to get CA certificate: %w", err)
	}

	// Generate certificate
	cert, key, err := r.generateTLSCertificate(caCert, caKey, "argocd-agent-principal", endpoints.IPs, endpoints.DNSNames)
	if err != nil {
		return fmt.Errorf("failed to generate principal certificate: %w", err)
	}

	// Create secret
	return r.createTLSSecret(secretName, argoNamespace, cert, key)
}

// ensureArgoCDAgentResourceProxyTLSCert ensures the argocd-agent-resource-proxy-tls certificate exists
func (r *ReconcileGitOpsCluster) ensureArgoCDAgentResourceProxyTLSCert(argoNamespace string) error {
	secretName := getArgoCDAgentResourceProxyTLSSecretName()

	// Check if certificate already exists
	if exists, err := r.checkCertificateExists(secretName, argoNamespace); err != nil {
		return err
	} else if exists {
		klog.Infof("%s certificate already exists", secretName)
		return nil
	}

	klog.Infof("Creating %s certificate...", secretName)

	// Discover resource proxy endpoints (internal service)
	endpoints, err := r.discoverResourceProxyEndpoints(argoNamespace)
	if err != nil {
		return fmt.Errorf("failed to discover resource proxy endpoints: %w", err)
	}

	// Get CA certificate and key
	caCert, caKey, err := r.getArgoCDAgentCA(argoNamespace)
	if err != nil {
		return fmt.Errorf("failed to get CA certificate: %w", err)
	}

	// Generate certificate
	cert, key, err := r.generateTLSCertificate(caCert, caKey, "argocd-agent-resource-proxy", endpoints.IPs, endpoints.DNSNames)
	if err != nil {
		return fmt.Errorf("failed to generate resource proxy certificate: %w", err)
	}

	// Create secret
	return r.createTLSSecret(secretName, argoNamespace, cert, key)
}

// discoverPrincipalEndpoints discovers external endpoints for the principal service
func (r *ReconcileGitOpsCluster) discoverPrincipalEndpoints(argoNamespace string) (*ServiceEndpoints, error) {
	endpoints := &ServiceEndpoints{
		IPs:      []net.IP{},
		DNSNames: []string{},
	}

	// Look for openshift-gitops-agent-principal service (the actual principal service)
	service, err := r.findArgoCDAgentPrincipalService(argoNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to find ArgoCD agent principal service (required for principal certificate): %w", err)
	}

	// For principal certificate, we need external endpoints
	// Add load balancer IPs and hostnames
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.IP != "" {
			// Add the LoadBalancer IP directly (fallback if DNS resolution fails)
			if ip := net.ParseIP(ingress.IP); ip != nil {
				endpoints.IPs = append(endpoints.IPs, ip)

				klog.Infof("Added LoadBalancer IP: %s", ingress.IP)
			}
			// Try to resolve the IP via DNS to get additional IPs (best effort)
			resolvedIPs, err := r.resolveHostname(ingress.IP)
			if err != nil {
				klog.Infof("DNS resolution failed for IP %s (using direct IP as fallback): %v", ingress.IP, err)
			} else {
				// Add resolved IPs only if they're different from the original
				for _, resolvedIP := range resolvedIPs {
					if !r.containsIP(endpoints.IPs, resolvedIP) {
						endpoints.IPs = append(endpoints.IPs, resolvedIP)
						klog.Infof("Added resolved IP: %s", resolvedIP.String())
					}
				}
			}
		}

		if ingress.Hostname != "" {
			// Add the external hostname for DNS names (required)
			endpoints.DNSNames = append(endpoints.DNSNames, ingress.Hostname)
			klog.Infof("Added LoadBalancer hostname: %s", ingress.Hostname)
			// Try to resolve hostname to get IPs (best effort, not required)
			resolvedIPs, err := r.resolveHostname(ingress.Hostname)
			if err != nil {
				klog.Infof("DNS resolution failed for hostname %s (certificate will use hostname only): %v", ingress.Hostname, err)
			} else {
				// Add resolved IPs
				for _, resolvedIP := range resolvedIPs {
					if !r.containsIP(endpoints.IPs, resolvedIP) {
						endpoints.IPs = append(endpoints.IPs, resolvedIP)
						klog.Infof("Added resolved IP from hostname: %s", resolvedIP.String())
					}
				}
			}
		}
	}

	// Check if we have external endpoints before adding localhost
	hasExternalEndpoints := false
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.IP != "" || ingress.Hostname != "" {
			hasExternalEndpoints = true
			break
		}
	}

	// Validate that we have at least some external endpoints (localhost doesn't count as external)
	if !hasExternalEndpoints {
		klog.Warningf("No external endpoints found for principal service %s - service needs LoadBalancer with external IP or hostname. Adding localhost for local access only.", service.Name)
	}

	// Always add localhost IPs and DNS names for local access
	localhostIPv4 := net.ParseIP("127.0.0.1")
	if !r.containsIP(endpoints.IPs, localhostIPv4) {
		endpoints.IPs = append(endpoints.IPs, localhostIPv4)
		klog.Infof("Added localhost IPv4 for principal certificate: 127.0.0.1")
	}

	localhostIPv6 := net.ParseIP("::1")
	if !r.containsIP(endpoints.IPs, localhostIPv6) {
		endpoints.IPs = append(endpoints.IPs, localhostIPv6)
		klog.Infof("Added localhost IPv6 for principal certificate: ::1")
	}

	if !contains(endpoints.DNSNames, "localhost") {
		endpoints.DNSNames = append(endpoints.DNSNames, "localhost")
		klog.Infof("Added localhost DNS name for principal certificate: localhost")
	}

	if !contains(endpoints.DNSNames, "localhost.localdomain") {
		endpoints.DNSNames = append(endpoints.DNSNames, "localhost.localdomain")
		klog.Infof("Added localhost FQDN for principal certificate: localhost.localdomain")
	}

	// Log what we found
	if len(endpoints.IPs) == 0 {
		klog.Infof("Principal certificate will be generated with DNS names only (no IPs found): %v", endpoints.DNSNames)
	} else {
		klog.Infof("Principal certificate will be generated with IPs: %v and DNS names: %v", endpoints.IPs, endpoints.DNSNames)
	}

	klog.Infof("Discovered principal endpoints - IPs: %v, DNS: %v", endpoints.IPs, endpoints.DNSNames)

	return endpoints, nil
}

// discoverResourceProxyEndpoints discovers internal endpoints for the resource proxy service
func (r *ReconcileGitOpsCluster) discoverResourceProxyEndpoints(argoNamespace string) (*ServiceEndpoints, error) {
	endpoints := &ServiceEndpoints{
		IPs:      []net.IP{},
		DNSNames: []string{},
	}

	// Look for the openshift-gitops-agent-principal service (internal access)
	service, err := r.findArgoCDAgentPrincipalService(argoNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to find ArgoCD agent principal service (required for resource proxy certificate): %w", err)
	}

	// For resource proxy, we need internal cluster IP
	if service.Spec.ClusterIP != "" && service.Spec.ClusterIP != "None" {
		if ip := net.ParseIP(service.Spec.ClusterIP); ip != nil {
			endpoints.IPs = append(endpoints.IPs, ip)

			klog.Infof("Added service ClusterIP for resource proxy: %s", service.Spec.ClusterIP)
		}
	} else {
		klog.Infof("Service %s has no ClusterIP (headless service), will use DNS name only for resource proxy certificate", service.Name)
	}

	// Add the internal DNS name for resource proxy (always required)
	internalDNS := fmt.Sprintf("openshift-gitops-agent-principal.%s.svc.cluster.local", argoNamespace)
	endpoints.DNSNames = append(endpoints.DNSNames, internalDNS)
	klog.Infof("Added internal DNS name for resource proxy: %s", internalDNS)

	// Always add localhost IPs and DNS names for local access
	localhostIPv4 := net.ParseIP("127.0.0.1")
	if !r.containsIP(endpoints.IPs, localhostIPv4) {
		endpoints.IPs = append(endpoints.IPs, localhostIPv4)
		klog.Infof("Added localhost IPv4 for resource proxy certificate: 127.0.0.1")
	}

	localhostIPv6 := net.ParseIP("::1")
	if !r.containsIP(endpoints.IPs, localhostIPv6) {
		endpoints.IPs = append(endpoints.IPs, localhostIPv6)
		klog.Infof("Added localhost IPv6 for resource proxy certificate: ::1")
	}

	if !contains(endpoints.DNSNames, "localhost") {
		endpoints.DNSNames = append(endpoints.DNSNames, "localhost")
		klog.Infof("Added localhost DNS name for resource proxy certificate: localhost")
	}

	if !contains(endpoints.DNSNames, "localhost.localdomain") {
		endpoints.DNSNames = append(endpoints.DNSNames, "localhost.localdomain")
		klog.Infof("Added localhost FQDN for resource proxy certificate: localhost.localdomain")
	}

	// For resource proxy, we always have at least the internal DNS name, but validate anyway
	if len(endpoints.IPs) == 0 && len(endpoints.DNSNames) == 0 {
		return nil, fmt.Errorf("no internal endpoints found for resource proxy - this should not happen")
	}

	// Log what we found
	if len(endpoints.IPs) == 0 {
		klog.Infof("Resource proxy certificate will be generated with DNS names only (no ClusterIP): %v", endpoints.DNSNames)
	} else {
		klog.Infof("Resource proxy certificate will be generated with IPs: %v and DNS names: %v", endpoints.IPs, endpoints.DNSNames)
	}

	klog.Infof("Discovered resource proxy endpoints - IPs: %v, DNS: %v", endpoints.IPs, endpoints.DNSNames)

	return endpoints, nil
}

// findArgoCDAgentPrincipalService finds the ArgoCD agent principal service
func (r *ReconcileGitOpsCluster) findArgoCDAgentPrincipalService(argoNamespace string) (*v1.Service, error) {
	// First try to find by the specific name
	service := &v1.Service{}
	err := r.Get(context.TODO(), types.NamespacedName{
		Name:      "openshift-gitops-agent-principal",
		Namespace: argoNamespace,
	}, service)

	if err == nil {
		return service, nil
	}

	// Fallback: try alternative names
	fallbackNames := []string{"argocd-agent-principal"}
	for _, name := range fallbackNames {
		service := &v1.Service{}
		err := r.Get(context.TODO(), types.NamespacedName{
			Name:      name,
			Namespace: argoNamespace,
		}, service)

		if err == nil {
			return service, nil
		}
	}

	return nil, fmt.Errorf("ArgoCD agent principal service not found")
}

// resolveHostname resolves a hostname or IP address to IP addresses using DNS lookup
func (r *ReconcileGitOpsCluster) resolveHostname(hostname string) ([]net.IP, error) {
	// If it's already an IP, just return it
	if ip := net.ParseIP(hostname); ip != nil {
		return []net.IP{ip}, nil
	}

	// Perform DNS lookup
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup failed for %s: %w", hostname, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no IP addresses found for %s", hostname)
	}

	return ips, nil
}

// containsIP checks if an IP address is already in the slice
func (r *ReconcileGitOpsCluster) containsIP(ips []net.IP, target net.IP) bool {
	for _, ip := range ips {
		if ip.Equal(target) {
			return true
		}
	}

	return false
}

// contains checks if a string is already in the slice
func contains(slice []string, target string) bool {
	for _, item := range slice {
		if item == target {
			return true
		}
	}

	return false
}

// checkCertificateExists checks if a TLS certificate secret exists
func (r *ReconcileGitOpsCluster) checkCertificateExists(secretName, namespace string) (bool, error) {
	secret := &v1.Secret{}
	err := r.Get(context.TODO(), types.NamespacedName{
		Name:      secretName,
		Namespace: namespace,
	}, secret)

	if err != nil {
		if k8errors.IsNotFound(err) {
			return false, nil
		}

		return false, fmt.Errorf("failed to check secret %s: %w", secretName, err)
	}

	// Check if it has the required TLS fields
	if _, hasKey := secret.Data["tls.key"]; !hasKey {
		return false, nil
	}

	if _, hasCert := secret.Data["tls.crt"]; !hasCert {
		return false, nil
	}

	return true, nil
}

// getArgoCDAgentCA retrieves the CA certificate and key from the argocd-agent-ca secret
func (r *ReconcileGitOpsCluster) getArgoCDAgentCA(argoNamespace string) (*x509.Certificate, *rsa.PrivateKey, error) {
	secret := &v1.Secret{}
	err := r.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: argoNamespace,
	}, secret)

	if err != nil {
		return nil, nil, fmt.Errorf("failed to get argocd-agent-ca secret: %w", err)
	}

	// Parse CA certificate
	caCertPEM, exists := secret.Data["tls.crt"]
	if !exists {
		return nil, nil, fmt.Errorf("tls.crt not found in argocd-agent-ca secret")
	}

	caCertBlock, _ := pem.Decode(caCertPEM)
	if caCertBlock == nil {
		return nil, nil, fmt.Errorf("failed to decode CA certificate PEM")
	}

	caCert, err := x509.ParseCertificate(caCertBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	// Parse CA private key
	caKeyPEM, exists := secret.Data["tls.key"]
	if !exists {
		return nil, nil, fmt.Errorf("tls.key not found in argocd-agent-ca secret")
	}

	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		return nil, nil, fmt.Errorf("failed to decode CA private key PEM")
	}

	caKey, err := x509.ParsePKCS1PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		// Try parsing as PKCS8
		keyInterface, err := x509.ParsePKCS8PrivateKey(caKeyBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse CA private key: %w", err)
		}

		var ok bool
		caKey, ok = keyInterface.(*rsa.PrivateKey)

		if !ok {
			return nil, nil, fmt.Errorf("CA private key is not an RSA key")
		}
	}

	return caCert, caKey, nil
}

// generateTLSCertificate generates a TLS certificate signed by the provided CA
func (r *ReconcileGitOpsCluster) generateTLSCertificate(caCert *x509.Certificate, caKey *rsa.PrivateKey, commonName string, ips []net.IP, dnsNames []string) ([]byte, []byte, error) {
	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Generate cryptographically random serial number
	serialNumber, err := generateSerialNumber()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	// Normalize IP addresses to ensure IPv4 addresses are stored as 4-byte addresses
	normalizedIPs := make([]net.IP, len(ips))
	for i, ip := range ips {
		if ipv4 := ip.To4(); ipv4 != nil {
			normalizedIPs[i] = ipv4
		} else {
			normalizedIPs[i] = ip
		}
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1 year
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           normalizedIPs,
		DNSNames:              dnsNames,
	}

	// Generate certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Encode certificate to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	// Encode private key to PEM
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	return certPEM, keyPEM, nil
}

// createTLSSecret creates a TLS secret with the provided certificate and key
func (r *ReconcileGitOpsCluster) createTLSSecret(secretName, namespace string, cert, key []byte) error {
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": cert,
			"tls.key": key,
		},
	}

	err := r.Create(context.TODO(), secret)
	if err != nil {
		if k8errors.IsAlreadyExists(err) {
			klog.Infof("Secret %s already exists", secretName)
			return nil
		}

		return fmt.Errorf("failed to create secret %s: %w", secretName, err)
	}

	klog.Infof("Successfully created secret %s", secretName)

	return nil
}

// generateSerialNumber generates a cryptographically random serial number for certificates
func generateSerialNumber() (*big.Int, error) {
	// Generate 16 random bytes (128-bit serial number)
	// This provides 2^128 possible values, ensuring uniqueness
	serialBytes := make([]byte, 16)
	_, err := rand.Read(serialBytes)

	if err != nil {
		return nil, fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Convert bytes to big.Int
	// Ensure the serial number is positive by setting the high bit to 0
	serialBytes[0] &= 0x7F

	serialNumber := new(big.Int).SetBytes(serialBytes)

	// Ensure serial number is not zero (RFC 5280 requirement)
	if serialNumber.Cmp(big.NewInt(0)) == 0 {
		serialNumber.SetInt64(1)
	}

	return serialNumber, nil
}
