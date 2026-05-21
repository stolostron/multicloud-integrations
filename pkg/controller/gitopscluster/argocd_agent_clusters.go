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
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/netip"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ArgoCD agent constants
	labelKeyClusterAgentMapping  = "argocd-agent.argoproj-labs.io/agent-name"
	labelValueManagerName        = "argocd-agent"
	principalCAName              = "argocd-agent-ca"
	argoCDTypeLabel              = "argocd.argoproj.io/secret-type"
	argoCDSecretTypeClusterValue = "cluster"
	argoCDManagedByAnnotation    = "managed-by"
)

// TLSClientConfig represents TLS client configuration for ArgoCD cluster secrets
type TLSClientConfig struct {
	Insecure bool   `json:"insecure,omitempty"`
	CAData   []byte `json:"caData,omitempty"`
	CertData []byte `json:"certData,omitempty"`
	KeyData  []byte `json:"keyData,omitempty"`
}

// ClusterConfig represents the configuration for an ArgoCD cluster
type ClusterConfig struct {
	Username        string          `json:"username,omitempty"`
	Password        string          `json:"password,omitempty"`
	TLSClientConfig TLSClientConfig `json:"tlsClientConfig,omitempty"`
}

// Cluster represents an ArgoCD cluster
type Cluster struct {
	Server      string            `json:"server"`
	Name        string            `json:"name"`
	Config      ClusterConfig     `json:"config"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// deleteBlankClusterSecretsForCluster deletes blank pull-model ACM cluster secrets for the given
// cluster after the agent cluster secret has been successfully created. Blank secrets
// (label apps.open-cluster-management.io/data-source=blank) are dummy entries with no real
// credentials that are created for the basic GitOps pull model. When a GitOpsCluster transitions
// from non-agent mode to agent mode, both the old blank secret (e.g.
// "<cluster>-application-manager-cluster-secret") and the new agent secret ("cluster-<cluster>")
// carry data["name"] = <cluster>. ArgoCD rejects this with:
//
//	"there are 2 clusters with the same name: [<agent-url> <traditional-url>]"
//
// The orphan cleanup in cleanupOrphanSecrets will NOT remove the old secret while the ManagedCluster
// still exists. This function is called only after the agent secret is confirmed created/updated, so
// deleting the blank secret is safe — the agent secret is now the authoritative entry for the cluster.
// Non-blank traditional secrets (e.g. MSA-backed secrets with real credentials) are intentionally
// left untouched.
func (r *ReconcileGitOpsCluster) deleteBlankClusterSecretsForCluster(
	ctx context.Context,
	clusterName string,
	argoNamespace string,
) error {
	secretList := &v1.SecretList{}

	err := r.List(ctx, secretList,
		client.InNamespace(argoNamespace),
		client.MatchingLabels{
			"argocd.argoproj.io/secret-type":               "cluster",
			"apps.open-cluster-management.io/acm-cluster":  "true",
			"apps.open-cluster-management.io/cluster-name": clusterName,
			"apps.open-cluster-management.io/data-source":  "blank",
		},
	)
	if err != nil {
		return fmt.Errorf("failed to list blank cluster secrets for cluster %s: %w", clusterName, err)
	}

	for i := range secretList.Items {
		secret := &secretList.Items[i]

		klog.Infof("Deleting blank cluster secret %s/%s for cluster %s: agent secret created, pull-model placeholder no longer needed",
			secret.Namespace, secret.Name, clusterName)

		if delErr := r.Delete(ctx, secret); delErr != nil && !k8errors.IsNotFound(delErr) {
			return fmt.Errorf("failed to delete blank cluster secret %s: %w", secret.Name, delErr)
		}
	}

	return nil
}

// CreateArgoCDAgentClusters creates ArgoCD cluster secrets for each managed cluster using ArgoCD agent configuration
func (r *ReconcileGitOpsCluster) CreateArgoCDAgentClusters(
	gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster,
	managedClusters []*spokeclusterv1.ManagedCluster,
	orphanSecretsList map[types.NamespacedName]string,
) error {
	klog.Infof("Creating ArgoCD agent cluster secrets for %d managed clusters", len(managedClusters))

	argoNamespace := gitOpsCluster.Spec.ArgoServer.ArgoNamespace

	if argoNamespace == "" {
		return fmt.Errorf("ArgoCDnamespace must not be empty")
	}

	// Determine the server URL strategy for cluster secrets.
	//
	// Preferred (Red Hat operator): use the in-cluster resource proxy service so the hub
	// ArgoCD UI can forward live-manifest / resource-tree requests to the managed agent:
	//   https://<resource-proxy-svc>.<ns>.svc:<port>?agentName=<cluster>
	//
	// Fallback (upstream / embedded argocd-operator): the resource proxy sidecar is not
	// created by the upstream operator. In that case fall back to the external principal
	// NodePort / LoadBalancer address that was already discovered during reconciliation:
	//   https://<node-ip>:<nodeport>?agentName=<cluster>
	// This keeps backward compatibility with the embedded e2e test environment.
	var (
		useResourceProxy     bool
		resourceProxySvcName string
		resourceProxyPort    int32
		fallbackServerAddr   string
		fallbackServerPort   string
	)

	if svc, port, svcErr := r.FindArgoCDAgentResourceProxyService(context.TODO(), argoNamespace); svcErr == nil {
		resourceProxySvcName = svc.Name
		resourceProxyPort = port
		useResourceProxy = true
		klog.Infof("Using resource proxy service %s (port %d) for cluster secret server URLs in namespace %s",
			resourceProxySvcName, resourceProxyPort, argoNamespace)
	} else {
		klog.Infof("Resource proxy service not found in namespace %s, falling back to external principal address for cluster secrets: %v",
			argoNamespace, svcErr)
		var addrErr error
		fallbackServerAddr, fallbackServerPort, addrErr = r.getArgoCDAgentServerConfig(gitOpsCluster)
		if addrErr != nil {
			return fmt.Errorf("resource proxy unavailable and external server address not configured: %w", addrErr)
		}
	}

	// Load the principal CA certificate for signing client certificates
	caCert, caKey, caData, err := r.loadPrincipalCACertificate(argoNamespace)
	if err != nil {
		return fmt.Errorf("failed to load principal CA certificate: %w", err)
	}

	for _, managedCluster := range managedClusters {
		clusterName := managedCluster.Name

		// Validate cluster name
		if !isValidAgentName(clusterName) {
			klog.Errorf("Invalid cluster name for ArgoCD agent: %s", clusterName)
			continue
		}

		secretName := fmt.Sprintf("cluster-%s", clusterName)

		// Remove the secrets from orphan list to prevent them from being deleted
		// (similar to what traditional import does)
		secretObjectKey := types.NamespacedName{
			Name:      secretName,
			Namespace: argoNamespace,
		}
		msaSecretObjectKey := types.NamespacedName{
			Name:      clusterName + "-gitops-cluster", // MSA-style secret name
			Namespace: argoNamespace,
		}
		delete(orphanSecretsList, secretObjectKey)
		delete(orphanSecretsList, msaSecretObjectKey)

		// Check if cluster secret already exists
		existingSecret := &v1.Secret{}
		secretExists := false
		err := r.Get(context.TODO(), types.NamespacedName{
			Name:      secretName,
			Namespace: argoNamespace,
		}, existingSecret)

		if err == nil {
			secretExists = true
			// Secret already exists, check if it's already an ArgoCD agent cluster with current config
			if labels := existingSecret.Labels; labels != nil {
				if agentName, ok := labels[labelKeyClusterAgentMapping]; ok && agentName == clusterName {
					// This is already an agent cluster, check if we need to update it
					// For now, we'll always regenerate to ensure it's up to date with current config
					klog.V(2).Infof("ArgoCD agent cluster secret %s exists for cluster %s, will update", secretName, clusterName)
				} else {
					// This is a traditional cluster secret, we'll override it for agent mode
					klog.Infof("Overriding existing traditional cluster secret %s with ArgoCD agent configuration for cluster %s", secretName, clusterName)
				}
			} else {
				// Secret exists but no labels, we'll override it for agent mode
				klog.Infof("Overriding existing cluster secret %s with ArgoCD agent configuration for cluster %s", secretName, clusterName)
			}
		} else if !k8errors.IsNotFound(err) {
			klog.Errorf("Failed to check existing cluster secret %s: %v", secretName, err)
			continue
		}

		// Generate client certificate for this cluster
		clientCert, clientKey, err := r.generateAgentClientCertificate(clusterName, caCert, caKey)
		if err != nil {
			klog.Errorf("Failed to generate client certificate for cluster %s: %v", clusterName, err)
			continue
		}

		// Generate random password
		password, err := generateRandomPassword()
		if err != nil {
			klog.Errorf("Failed to generate password for cluster %s: %v", clusterName, err)
			continue
		}

		// Construct the cluster secret server URL using whichever strategy is available.
		var serverURL string
		if useResourceProxy {
			serverURL, err = constructResourceProxyURL(resourceProxySvcName, argoNamespace, resourceProxyPort, clusterName)
		} else {
			serverURL, err = constructServerURL(fallbackServerAddr, fallbackServerPort, clusterName)
		}
		if err != nil {
			klog.Errorf("Failed to construct server URL for cluster %s: %v", clusterName, err)
			continue
		}

		// Create ArgoCD cluster configuration
		cluster := &Cluster{
			Server: serverURL,
			Name:   clusterName,
			Labels: map[string]string{
				labelKeyClusterAgentMapping: clusterName,
			},
			Config: ClusterConfig{
				Username: clusterName,
				Password: password,
				TLSClientConfig: TLSClientConfig{
					Insecure: false,
					CAData:   []byte(caData),
					CertData: []byte(clientCert),
					KeyData:  []byte(clientKey),
				},
			},
		}

		// Create the secret
		secret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: argoNamespace,
			},
		}

		// Convert cluster to secret
		err = r.clusterToSecret(cluster, secret)
		if err != nil {
			klog.Errorf("Failed to convert cluster to secret for cluster %s: %v", clusterName, err)
			continue
		}

		// Create or update the secret
		if !secretExists {
			err = r.Create(context.TODO(), secret)
			if err != nil {
				klog.Errorf("Failed to create cluster secret %s: %v", secretName, err)
				continue
			}
			klog.Infof("Created ArgoCD agent cluster secret %s for cluster %s", secretName, clusterName)
		} else {
			// Update existing secret with new agent configuration
			secret.ResourceVersion = existingSecret.ResourceVersion
			err = r.Update(context.TODO(), secret)
			if err != nil {
				klog.Errorf("Failed to update cluster secret %s: %v", secretName, err)
				continue
			}
			klog.Infof("Updated ArgoCD agent cluster secret %s for cluster %s", secretName, clusterName)
		}

		// Delete blank pull-model cluster secrets for this cluster now that the agent secret exists.
		// Blank secrets (data-source=blank) are pull-model placeholders that share the same
		// data["name"] value as the new agent secret. ArgoCD raises an ApplicationSet error when
		// two cluster secrets have the same name. The orphan cleanup will not remove blank secrets
		// while the ManagedCluster still exists, so we delete them proactively here.
		// Non-blank traditional secrets (e.g. MSA-backed) are left untouched.
		if cleanErr := r.deleteBlankClusterSecretsForCluster(context.TODO(), clusterName, argoNamespace); cleanErr != nil {
			klog.Warningf("Failed to delete blank cluster secrets for cluster %s (agent secret was created successfully): %v", clusterName, cleanErr)
		}
	}

	return nil
}

// getArgoCDAgentServerConfig retrieves the server address and port configuration for the cluster secret
// The cluster secret URL uses the external Route/LoadBalancer address discovered during GitOpsCluster reconciliation.
// The ArgoCD application controller on the hub connects to the principal via this external address,
// and the principal proxies requests to the agent on the managed cluster.
func (r *ReconcileGitOpsCluster) getArgoCDAgentServerConfig(
	gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster,
) (string, string, error) {
	// Get the server address and port from the GitOpsCluster spec
	// These were discovered during reconciliation from Route or LoadBalancer
	serverAddress := ""
	serverPort := "443"

	if gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil {
		serverAddress = gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress
		if gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort != "" {
			serverPort = gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort
		}
	}

	if serverAddress == "" {
		return "", "", fmt.Errorf("ArgoCD agent server address not configured in GitOpsCluster spec")
	}

	return serverAddress, serverPort, nil
}

// loadPrincipalCACertificate loads the principal CA certificate from the secret
func (r *ReconcileGitOpsCluster) loadPrincipalCACertificate(
	namespace string,
) (*x509.Certificate, interface{}, string, error) {
	secret := &v1.Secret{}
	err := r.Get(context.TODO(), types.NamespacedName{
		Name:      principalCAName,
		Namespace: namespace,
	}, secret)

	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to get principal CA secret: %w", err)
	}

	// Extract certificate and key data
	certData, ok := secret.Data["tls.crt"]
	if !ok {
		return nil, nil, "", fmt.Errorf("CA certificate not found in secret")
	}

	keyData, ok := secret.Data["tls.key"]
	if !ok {
		return nil, nil, "", fmt.Errorf("CA private key not found in secret")
	}

	// Parse the certificate and key
	tlsCert, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	if len(tlsCert.Certificate) == 0 {
		return nil, nil, "", fmt.Errorf("no certificates found in CA")
	}

	caCert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	// Re-encode the CA certificate to PEM format for the cluster secret
	caDataPEM, err := certDataToPEM(tlsCert.Certificate[0])
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to encode CA certificate to PEM: %w", err)
	}

	return caCert, tlsCert.PrivateKey, caDataPEM, nil
}

// generateAgentClientCertificate generates a client certificate for the agent
func (r *ReconcileGitOpsCluster) generateAgentClientCertificate(
	agentName string,
	signerCert *x509.Certificate,
	signerKey interface{},
) (string, string, error) {
	// Create the certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   agentName,
			Organization: []string{"ArgoCD Agent"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(0, 6, 0), // 6 months
		IsCA:                  false,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	// Generate a private key for the client certificate
	clientKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate client key: %w", err)
	}

	// Create the client certificate
	certBytes, err := x509.CreateCertificate(rand.Reader, template, signerCert, &clientKey.PublicKey, signerKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to create client certificate: %w", err)
	}

	// Encode certificate to PEM
	certPEM, err := certDataToPEM(certBytes)
	if err != nil {
		return "", "", fmt.Errorf("failed to encode client certificate: %w", err)
	}

	// Encode private key to PEM
	keyBytes := x509.MarshalPKCS1PrivateKey(clientKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: keyBytes,
	})
	if keyPEM == nil {
		return "", "", fmt.Errorf("failed to encode client private key")
	}

	return certPEM, string(keyPEM), nil
}

// generateRandomPassword generates a random base64-encoded password
func generateRandomPassword() (string, error) {
	bytes := make([]byte, 32)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(bytes), nil
}

// constructServerURL constructs the server URL for the ArgoCD agent
func constructServerURL(serverAddress, serverPort, agentName string) (string, error) {
	// Validate the address format
	address := fmt.Sprintf("%s:%s", serverAddress, serverPort)
	_, err := netip.ParseAddrPort(address)
	if err != nil {
		// Try parsing as host:port
		parts := strings.SplitN(address, ":", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid address format: %s", address)
		}

		host := parts[0]
		port := parts[1]

		// Validate hostname
		if len(validation.NameIsDNSSubdomain(host, false)) > 0 {
			return "", fmt.Errorf("invalid hostname: %s", host)
		}

		// Validate port
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return "", fmt.Errorf("invalid port: %s", port)
		}
	}

	// Validate agent name
	if !isValidAgentName(agentName) {
		return "", fmt.Errorf("invalid agent name: %s", agentName)
	}

	return fmt.Sprintf("https://%s?agentName=%s", address, agentName), nil
}

// constructResourceProxyURL constructs the in-cluster resource proxy URL for an ArgoCD agent cluster secret.
// The hub ArgoCD UI uses this URL to serve live manifest and resource tree requests by routing them
// through the resource proxy sidecar in the principal pod to the appropriate managed cluster agent.
// Format: https://<resource-proxy-svc>.<namespace>.svc:<port>?agentName=<cluster>
func constructResourceProxyURL(resourceProxySvcName, namespace string, port int32, agentName string) (string, error) {
	if !isValidAgentName(agentName) {
		return "", fmt.Errorf("invalid agent name: %s", agentName)
	}
	return fmt.Sprintf("https://%s.%s.svc:%d?agentName=%s", resourceProxySvcName, namespace, port, agentName), nil
}

// isValidAgentName checks if the agent name is valid (DNS subdomain)
func isValidAgentName(name string) bool {
	return len(validation.NameIsDNSSubdomain(name, false)) == 0
}

// certDataToPEM encodes certificate data to PEM format
func certDataToPEM(certBytes []byte) (string, error) {
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})
	if certPEM == nil {
		return "", fmt.Errorf("failed to encode certificate to PEM")
	}
	return string(certPEM), nil
}

// clusterToSecret converts a cluster object to string data for serialization to a secret
// This is based on the ArgoCD agent's ClusterToSecret function
// It also cleans up labels/annotations from traditional mode when switching to agent mode
func (r *ReconcileGitOpsCluster) clusterToSecret(cluster *Cluster, secret *v1.Secret) error {
	data := make(map[string][]byte)
	data["server"] = []byte(strings.TrimRight(cluster.Server, "/"))

	if cluster.Name == "" {
		data["name"] = []byte(cluster.Server)
	} else {
		data["name"] = []byte(cluster.Name)
	}

	configBytes, err := json.Marshal(cluster.Config)
	if err != nil {
		return fmt.Errorf("failed to marshal cluster config: %w", err)
	}
	data["config"] = configBytes

	secret.Data = data

	// Start with fresh labels to avoid keeping stale labels from traditional mode
	newLabels := make(map[string]string)

	// Copy cluster labels
	if cluster.Labels != nil {
		for k, v := range cluster.Labels {
			newLabels[k] = v
		}
	}

	// Set required ArgoCD labels
	newLabels[argoCDTypeLabel] = argoCDSecretTypeClusterValue

	// Preserve some existing labels that are needed
	if secret.Labels != nil {
		// Keep these labels from traditional mode as they're still useful
		preserveLabels := []string{
			"apps.open-cluster-management.io/cluster-name",
		}
		for _, label := range preserveLabels {
			if val, ok := secret.Labels[label]; ok {
				newLabels[label] = val
			}
		}
	}

	secret.Labels = newLabels

	// Start with fresh annotations
	newAnnotations := make(map[string]string)

	// Copy cluster annotations
	if cluster.Annotations != nil {
		for k, v := range cluster.Annotations {
			if k == v1.LastAppliedConfigAnnotation {
				return fmt.Errorf("annotation %s cannot be set", v1.LastAppliedConfigAnnotation)
			}
			newAnnotations[k] = v
		}
	}

	// Set managed-by annotation for agent mode
	newAnnotations[argoCDManagedByAnnotation] = labelValueManagerName

	secret.Annotations = newAnnotations

	return nil
}
