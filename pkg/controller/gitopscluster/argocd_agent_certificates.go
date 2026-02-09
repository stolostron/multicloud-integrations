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
	"fmt"
	"strings"
	"time"

	"crypto/x509"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/library-go/pkg/crypto"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog"
	"open-cluster-management.io/sdk-go/pkg/certrotation"

	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ArgoCDAgentCASecretName is the name of the CA secret
	ArgoCDAgentCASecretName = "argocd-agent-ca" // #nosec G101

	// ArgoCDAgentPrincipalTLSSecretName is the name of the principal TLS certificate
	ArgoCDAgentPrincipalTLSSecretName = "argocd-agent-principal-tls" // #nosec G101

	// ArgoCDAgentResourceProxyTLSSecretName is the name of the resource proxy TLS certificate
	ArgoCDAgentResourceProxyTLSSecretName = "argocd-agent-resource-proxy-tls" // #nosec G101

	// CASignerNamePrefix is the prefix for the CA signer name
	CASignerNamePrefix = "argocd-agent-ca"
)

// Certificate validity periods
var (
	// SigningCertValidity is the validity for the CA certificate (1 year)
	SigningCertValidity = time.Hour * 24 * 365

	// TargetCertValidity is the validity for service certificates (30 days)
	TargetCertValidity = time.Hour * 24 * 30

	// ResyncInterval is how often to check for rotation (10 minutes)
	ResyncInterval = time.Minute * 10
)

// EnsureArgoCDAgentCASecret ensures the ArgoCD agent CA secret exists
// This creates only the CA certificate and CA bundle ConfigMap
func (r *ReconcileGitOpsCluster) EnsureArgoCDAgentCASecret(ctx context.Context, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	namespace := gitOpsCluster.Namespace

	klog.V(2).Infof("Ensuring ArgoCD agent CA certificate in namespace %s", namespace)

	// Get Kubernetes clientset
	kubeClient, err := r.getKubernetesClientset()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes clientset: %w", err)
	}

	// Setup informers
	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		ResyncInterval,
		informers.WithNamespace(namespace),
	)

	secretLister := informerFactory.Core().V1().Secrets().Lister()
	configMapLister := informerFactory.Core().V1().ConfigMaps().Lister()

	// Start the informer and wait for cache sync
	stopCh := make(chan struct{})
	defer close(stopCh)
	informerFactory.Start(stopCh)

	// Wait for cache to sync
	cacheSyncs := informerFactory.WaitForCacheSync(stopCh)
	for informerType, synced := range cacheSyncs {
		if !synced {
			return fmt.Errorf("failed to sync informer cache for type %v", informerType)
		}
	}

	// Create SigningRotation for the CA certificate
	signingRotation := &certrotation.SigningRotation{
		Namespace:        namespace,
		Name:             ArgoCDAgentCASecretName,
		SignerNamePrefix: CASignerNamePrefix,
		Validity:         SigningCertValidity,
		Lister:           secretLister,
		Client:           kubeClient.CoreV1(),
	}

	// Ensure the CA signing certificate key pair
	signingCertKeyPair, err := signingRotation.EnsureSigningCertKeyPair()
	if err != nil {
		return fmt.Errorf("failed to ensure signing cert key pair: %w", err)
	}

	// Create CABundleRotation for the CA bundle ConfigMap
	caBundleRotation := &certrotation.CABundleRotation{
		Namespace: namespace,
		Name:      "argocd-agent-ca-bundle",
		Lister:    configMapLister,
		Client:    kubeClient.CoreV1(),
	}

	// Ensure the CA bundle ConfigMap
	_, err = caBundleRotation.EnsureConfigMapCABundle(signingCertKeyPair)
	if err != nil {
		return fmt.Errorf("failed to ensure CA bundle: %w", err)
	}

	klog.Infof("Successfully ensured ArgoCD agent CA certificate in namespace %s, secret %s", namespace, ArgoCDAgentCASecretName)

	return nil
}

// EnsureArgoCDAgentPrincipalTLSCert ensures the principal TLS certificate is generated from the CA
func (r *ReconcileGitOpsCluster) EnsureArgoCDAgentPrincipalTLSCert(ctx context.Context, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	namespace := gitOpsCluster.Namespace

	klog.V(2).Infof("Ensuring principal TLS certificate in namespace %s", namespace)

	// Verify CA secret exists
	if err := r.verifyCACertificateExists(ctx, namespace); err != nil {
		return fmt.Errorf("CA certificate not found: %w", err)
	}

	// Get Kubernetes clientset
	kubeClient, err := r.getKubernetesClientset()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes clientset: %w", err)
	}

	// Setup informers
	informerFactory, secretLister, err := r.setupInformers(kubeClient, namespace)
	if err != nil {
		return err
	}

	// Load the CA cert
	signingCertKeyPair, caBundleCerts, err := r.loadCACertificate(kubeClient, secretLister, namespace)
	if err != nil {
		return fmt.Errorf("failed to load CA certificate: %w", err)
	}

	// Create TargetRotation for the principal TLS certificate
	principalRotation := &certrotation.TargetRotation{
		Namespace: namespace,
		Name:      ArgoCDAgentPrincipalTLSSecretName,
		Validity:  TargetCertValidity,
		HostNames: r.getPrincipalHostNames(ctx, namespace, gitOpsCluster),
		Lister:    secretLister,
		Client:    kubeClient.CoreV1(),
	}

	// Ensure the principal TLS certificate
	if err := principalRotation.EnsureTargetCertKeyPair(signingCertKeyPair, caBundleCerts); err != nil {
		return fmt.Errorf("failed to ensure principal TLS certificate: %w", err)
	}

	// Stop informer
	defer informerFactory.Shutdown()

	klog.Infof("Successfully ensured principal TLS certificate in namespace %s, secret %s", namespace, ArgoCDAgentPrincipalTLSSecretName)

	return nil
}

// EnsureArgoCDAgentResourceProxyTLSCert ensures the resource proxy TLS certificate is generated from the CA
func (r *ReconcileGitOpsCluster) EnsureArgoCDAgentResourceProxyTLSCert(ctx context.Context, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	namespace := gitOpsCluster.Namespace

	klog.V(2).Infof("Ensuring resource proxy TLS certificate in namespace %s", namespace)

	// Verify CA secret exists
	if err := r.verifyCACertificateExists(ctx, namespace); err != nil {
		return fmt.Errorf("CA certificate not found: %w", err)
	}

	// Get Kubernetes clientset
	kubeClient, err := r.getKubernetesClientset()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes clientset: %w", err)
	}

	// Setup informers
	informerFactory, secretLister, err := r.setupInformers(kubeClient, namespace)
	if err != nil {
		return err
	}

	// Load the CA cert
	signingCertKeyPair, caBundleCerts, err := r.loadCACertificate(kubeClient, secretLister, namespace)
	if err != nil {
		return fmt.Errorf("failed to load CA certificate: %w", err)
	}

	// Create TargetRotation for the resource proxy TLS certificate
	resourceProxyRotation := &certrotation.TargetRotation{
		Namespace: namespace,
		Name:      ArgoCDAgentResourceProxyTLSSecretName,
		Validity:  TargetCertValidity,
		HostNames: r.getResourceProxyHostNames(ctx, namespace),
		Lister:    secretLister,
		Client:    kubeClient.CoreV1(),
	}

	// Ensure the resource proxy TLS certificate
	if err := resourceProxyRotation.EnsureTargetCertKeyPair(signingCertKeyPair, caBundleCerts); err != nil {
		return fmt.Errorf("failed to ensure resource proxy TLS certificate: %w", err)
	}

	// Stop informer
	defer informerFactory.Shutdown()

	klog.Infof("Successfully ensured resource proxy TLS certificate in namespace %s, secret %s", namespace, ArgoCDAgentResourceProxyTLSSecretName)

	return nil
}

// verifyCACertificateExists checks if the CA secret exists
func (r *ReconcileGitOpsCluster) verifyCACertificateExists(ctx context.Context, namespace string) error {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      ArgoCDAgentCASecretName,
	}, secret)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("ArgoCD agent CA certificate secret '%s' not found in namespace '%s'. The certificate may not have been generated yet, please check the controller logs for certificate generation errors", ArgoCDAgentCASecretName, namespace)
		}
		return err
	}
	return nil
}

// setupInformers creates and starts the informer factory
func (r *ReconcileGitOpsCluster) setupInformers(
	kubeClient *kubernetes.Clientset,
	namespace string) (informers.SharedInformerFactory, v1.SecretLister, error) {

	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		ResyncInterval,
		informers.WithNamespace(namespace),
	)

	secretLister := informerFactory.Core().V1().Secrets().Lister()

	// Start the informer and wait for cache sync
	stopCh := make(chan struct{})
	go func() {
		<-time.After(30 * time.Second)
		close(stopCh)
	}()
	informerFactory.Start(stopCh)

	// Wait for cache to sync
	cacheSyncs := informerFactory.WaitForCacheSync(stopCh)
	for informerType, synced := range cacheSyncs {
		if !synced {
			return nil, nil, fmt.Errorf("failed to sync informer cache for type %v", informerType)
		}
	}

	return informerFactory, secretLister, nil
}

// loadCACertificate loads the CA certificate from the secret
func (r *ReconcileGitOpsCluster) loadCACertificate(
	kubeClient *kubernetes.Clientset,
	secretLister v1.SecretLister,
	namespace string) (*crypto.CA, []*x509.Certificate, error) {

	// Create SigningRotation to load the existing CA
	signingRotation := &certrotation.SigningRotation{
		Namespace:        namespace,
		Name:             ArgoCDAgentCASecretName,
		SignerNamePrefix: CASignerNamePrefix,
		Validity:         SigningCertValidity,
		Lister:           secretLister,
		Client:           kubeClient.CoreV1(),
	}

	// Load the existing CA certificate
	signingCertKeyPair, err := signingRotation.EnsureSigningCertKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load signing cert key pair: %w", err)
	}

	// Load CA bundle
	configMapLister := informers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		ResyncInterval,
		informers.WithNamespace(namespace),
	).Core().V1().ConfigMaps().Lister()

	caBundleRotation := &certrotation.CABundleRotation{
		Namespace: namespace,
		Name:      "argocd-agent-ca-bundle",
		Lister:    configMapLister,
		Client:    kubeClient.CoreV1(),
	}

	caBundleCerts, err := caBundleRotation.EnsureConfigMapCABundle(signingCertKeyPair)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load CA bundle: %w", err)
	}

	return signingCertKeyPair, caBundleCerts, nil
}

// getPrincipalHostNames returns the hostnames for the principal certificate
// For principal, we need external endpoints (LoadBalancer IPs/hostnames) plus internal DNS
func (r *ReconcileGitOpsCluster) getPrincipalHostNames(ctx context.Context, namespace string, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) []string {
	hostnames := []string{}

	// First, add the serverAddress from GitOpsCluster if it's configured for ArgoCD agent
	// This is the most reliable source as it's explicitly configured by the user
	if gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil {
		serverAddress := gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress
		if serverAddress != "" {
			hostnames = append(hostnames, serverAddress)
			klog.Infof("Added serverAddress from GitOpsCluster to principal certificate SANs: %s", serverAddress)
		}
	}

	// Also try to discover Route hostname (for cases where serverAddress isn't set yet)
	routeHostname, err := r.discoverRouteHostname(ctx, namespace)
	if err == nil && routeHostname != "" {
		// Only add if not already present
		alreadyAdded := false
		for _, h := range hostnames {
			if h == routeHostname {
				alreadyAdded = true
				break
			}
		}
		if !alreadyAdded {
			hostnames = append(hostnames, routeHostname)
			klog.V(2).Infof("Added Route hostname to principal certificate: %s", routeHostname)
		}
	} else {
		klog.V(2).Infof("Could not discover Route hostname: %v", err)
	}

	// Try to get the service to find LoadBalancer endpoints
	service, err := r.FindArgoCDAgentPrincipalService(ctx, namespace)
	if err != nil {
		klog.V(2).Infof("Could not find principal service for hostname discovery, using defaults: %v", err)
		// Return default internal hostnames plus any Route hostname
		serviceName := "argocd-agent-principal"
		hostnames = append(hostnames,
			fmt.Sprintf("%s.%s.svc", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
		)
		hostnames = append(hostnames, "localhost", "127.0.0.1", "::1")
		return hostnames
	}

	// Add LoadBalancer external hostnames/IPs
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.Hostname != "" {
			hostnames = append(hostnames, ingress.Hostname)
			klog.V(2).Infof("Added LoadBalancer hostname to principal certificate: %s", ingress.Hostname)
		}
		if ingress.IP != "" {
			// IPs should be added but cert libraries typically use IPs field, not DNS names
			// Still add as hostname for compatibility
			hostnames = append(hostnames, ingress.IP)
			klog.V(2).Infof("Added LoadBalancer IP to principal certificate: %s", ingress.IP)
		}
	}

	// Always add internal DNS names
	hostnames = append(hostnames,
		fmt.Sprintf("%s.%s.svc", service.Name, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", service.Name, namespace),
	)

	// Add localhost for local access
	hostnames = append(hostnames, "localhost", "127.0.0.1", "::1")

	return hostnames
}

// discoverRouteHostname discovers the Route hostname for the ArgoCD agent principal
func (r *ReconcileGitOpsCluster) discoverRouteHostname(ctx context.Context, namespace string) (string, error) {
	// List routes with argocd-agent labels
	routeList := &routev1.RouteList{}
	listopts := &client.ListOptions{Namespace: namespace}

	routeSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app.kubernetes.io/part-of": "argocd-agent",
		},
	}

	routeSelectionLabel, err := utils.ConvertLabels(routeSelector)
	if err != nil {
		return "", fmt.Errorf("failed to convert route labels: %w", err)
	}

	listopts.LabelSelector = routeSelectionLabel

	err = r.List(ctx, routeList, listopts)
	if err != nil {
		return "", fmt.Errorf("failed to list routes: %w", err)
	}

	if len(routeList.Items) == 0 {
		return "", fmt.Errorf("no ArgoCD agent principal route found in namespace %s", namespace)
	}

	// Find the principal route (look for "principal" in the name)
	for i := range routeList.Items {
		route := &routeList.Items[i]
		if strings.Contains(route.Name, "principal") && route.Spec.Host != "" {
			return route.Spec.Host, nil
		}
	}

	// Fallback to first route with a host
	if routeList.Items[0].Spec.Host != "" {
		return routeList.Items[0].Spec.Host, nil
	}

	return "", fmt.Errorf("no route with host found")
}

// getResourceProxyHostNames returns the hostnames for the resource proxy certificate
// For resource proxy, we need internal cluster DNS names for the resource-proxy service
func (r *ReconcileGitOpsCluster) getResourceProxyHostNames(ctx context.Context, namespace string) []string {
	hostnames := []string{}

	// Try to get the principal service to derive the resource-proxy service name
	service, err := r.FindArgoCDAgentPrincipalService(ctx, namespace)
	principalServiceName := "argocd-agent-principal"
	if err == nil {
		principalServiceName = service.Name
	}

	// The resource-proxy service is named <principal-service-name>-resource-proxy
	resourceProxyServiceName := fmt.Sprintf("%s-resource-proxy", principalServiceName)

	// Add internal DNS names for resource proxy service
	hostnames = append(hostnames,
		fmt.Sprintf("%s.%s.svc", resourceProxyServiceName, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", resourceProxyServiceName, namespace),
	)

	// Also add the principal service names for compatibility
	hostnames = append(hostnames,
		fmt.Sprintf("%s.%s.svc", principalServiceName, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", principalServiceName, namespace),
	)

	// Add localhost for local access
	hostnames = append(hostnames, "localhost", "127.0.0.1", "::1")

	return hostnames
}

// findArgoCDAgentPrincipalService moved to server_discovery.go as FindArgoCDAgentPrincipalService

// getKubernetesClientset returns the Kubernetes clientset
func (r *ReconcileGitOpsCluster) getKubernetesClientset() (*kubernetes.Clientset, error) {
	// Return the existing authClient as a Clientset
	if cs, ok := r.authClient.(*kubernetes.Clientset); ok {
		return cs, nil
	}

	return nil, fmt.Errorf("authClient is not a *kubernetes.Clientset")
}
