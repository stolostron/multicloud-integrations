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

	routev1 "github.com/openshift/api/route/v1"
	v1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VerifyArgocdNamespace verifies that the specified namespace contains an ArgoCD server
func (r *ReconcileGitOpsCluster) VerifyArgocdNamespace(argoNamespace string) bool {
	return r.FindServiceWithLabelsAndNamespace(argoNamespace,
		map[string]string{"app.kubernetes.io/component": "server", "app.kubernetes.io/part-of": "argocd"})
}

// FindServiceWithLabelsAndNamespace finds a list of services with provided labels from the specified namespace
func (r *ReconcileGitOpsCluster) FindServiceWithLabelsAndNamespace(namespace string, labels map[string]string) bool {
	serviceList := &v1.ServiceList{}
	listopts := &client.ListOptions{Namespace: namespace}

	serviceSelector := &metav1.LabelSelector{
		MatchLabels: labels,
	}

	serviceSelectionLabel, err := utils.ConvertLabels(serviceSelector)

	if err != nil {
		klog.Error("Failed to convert managed cluster secret selector, err:", err)
		return false
	}

	listopts.LabelSelector = serviceSelectionLabel

	err = r.List(context.TODO(), serviceList, listopts)

	if err != nil {
		klog.Error("Failed to get service list, err:", err)
		return false
	}

	if len(serviceList.Items) == 0 {
		klog.Info("No services found in namespace", "namespace", namespace, "labels", labels)
		return false
	}

	return true
}

// EnsureServerAddressAndPort auto-discovers and populates server address and port if they are empty
// EnsureServerAddressAndPort is deprecated - use DiscoverServerAddressAndPort instead
// This function is kept for backwards compatibility but delegates to the new implementation
func (r *ReconcileGitOpsCluster) EnsureServerAddressAndPort(ctx context.Context, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster) (bool, error) {
	// Track if values were already set
	alreadySet := gitOpsCluster.Spec.GitOpsAddon != nil &&
		gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil &&
		gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress != "" &&
		gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort != ""

	// Use the new DiscoverServerAddressAndPort function
	err := r.DiscoverServerAddressAndPort(ctx, gitOpsCluster)
	if err != nil {
		return false, err
	}

	// Return true if values were updated (not already set)
	return !alreadySet, nil
}

// HasExistingServerConfig checks if any existing AddonDeploymentConfig has server address/port configured
func (r *ReconcileGitOpsCluster) HasExistingServerConfig(managedClusters []*spokeclusterv1.ManagedCluster) (bool, error) {
	for _, managedCluster := range managedClusters {
		existing := &addonv1alpha1.AddOnDeploymentConfig{}
		err := r.Get(context.Background(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: managedCluster.Name,
		}, existing)

		if err != nil {
			if k8errors.IsNotFound(err) {
				// Config doesn't exist, continue checking other clusters
				continue
			}
			return false, fmt.Errorf("failed to get AddOnDeploymentConfig in namespace %s: %w", managedCluster.Name, err)
		}

		// Check if any of the existing variables contain server address or port
		for _, variable := range existing.Spec.CustomizedVariables {
			if variable.Name == "ARGOCD_AGENT_SERVER_ADDRESS" && variable.Value != "" {
				klog.V(2).Infof("found existing ARGOCD_AGENT_SERVER_ADDRESS in namespace %s", managedCluster.Name)
				return true, nil
			}
			if variable.Name == "ARGOCD_AGENT_SERVER_PORT" && variable.Value != "" {
				klog.V(2).Infof("found existing ARGOCD_AGENT_SERVER_PORT in namespace %s", managedCluster.Name)
				return true, nil
			}
		}
	}

	return false, nil
}

// FindArgoCDAgentPrincipalService finds the ArgoCD agent principal service
// It tries hardcoded names first, then lists all services and filters by suffix
func (r *ReconcileGitOpsCluster) FindArgoCDAgentPrincipalService(ctx context.Context, namespace string) (*v1.Service, error) {
	// Try hardcoded common names first (faster)
	commonNames := []string{"openshift-gitops-agent-principal", "argocd-agent-principal"}

	for _, name := range commonNames {
		service := &v1.Service{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, service)
		if err == nil {
			klog.V(2).Infof("Found ArgoCD agent principal service by name: %s in namespace %s", name, namespace)
			return service, nil
		}
		if !k8errors.IsNotFound(err) {
			klog.Warningf("Error checking for service %s: %v", name, err)
		}
	}

	// Fallback: list all services and find one ending with "-agent-principal"
	serviceList := &v1.ServiceList{}
	err := r.List(ctx, serviceList, client.InNamespace(namespace))
	if err != nil {
		return nil, fmt.Errorf("failed to list services in namespace '%s': %w", namespace, err)
	}

	for _, svc := range serviceList.Items {
		if strings.HasSuffix(svc.Name, "-agent-principal") {
			klog.V(2).Infof("Found ArgoCD agent principal service by suffix match: %s in namespace %s", svc.Name, namespace)
			return &svc, nil
		}
	}

	return nil, fmt.Errorf("ArgoCD agent principal service not found in namespace '%s'. Please ensure the ArgoCD instance has been deployed with agent mode enabled and the principal service is running", namespace)
}

// DiscoverServerAddressAndPort discovers the external server address and port from the ArgoCD agent principal Route or Service
// and updates the GitOpsCluster CR spec fields if they are empty
func (r *ReconcileGitOpsCluster) DiscoverServerAddressAndPort(ctx context.Context, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	// Only discover if fields are not already set
	if gitOpsCluster.Spec.GitOpsAddon != nil &&
		gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil &&
		gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress != "" &&
		gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort != "" {
		klog.V(2).Infof("Server address and port already configured: %s:%s",
			gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress,
			gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort)
		return nil
	}

	argoNamespace := gitOpsCluster.Spec.ArgoServer.ArgoNamespace
	if argoNamespace == "" {
		argoNamespace = utils.GitOpsNamespace
	}

	var serverAddress string
	var serverPort string

	// First, try to discover from Route (preferred for OpenShift)
	serverAddress, serverPort, err := r.discoverFromRoute(ctx, argoNamespace)
	if err != nil {
		klog.V(2).Infof("Route discovery failed, trying LoadBalancer: %v", err)

		// Fallback to LoadBalancer discovery
		serverAddress, serverPort, err = r.discoverFromLoadBalancer(ctx, argoNamespace)
		if err != nil {
			return fmt.Errorf("failed to discover ArgoCD agent principal endpoint (tried Route and LoadBalancer): %w", err)
		}
	}

	// Update the GitOpsCluster CR with discovered values
	if gitOpsCluster.Spec.GitOpsAddon == nil {
		gitOpsCluster.Spec.GitOpsAddon = &gitopsclusterV1beta1.GitOpsAddonSpec{}
	}
	if gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent == nil {
		gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent = &gitopsclusterV1beta1.ArgoCDAgentSpec{}
	}

	gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress = serverAddress
	gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort = serverPort

	// Update the CR
	err = r.Update(ctx, gitOpsCluster)
	if err != nil {
		return fmt.Errorf("failed to update GitOpsCluster with discovered server address and port: %w", err)
	}

	klog.Infof("Updated GitOpsCluster %s/%s with discovered ArgoCD agent server endpoint: %s:%s",
		gitOpsCluster.Namespace, gitOpsCluster.Name, serverAddress, serverPort)
	return nil
}

// discoverFromRoute discovers the server address from an OpenShift Route
func (r *ReconcileGitOpsCluster) discoverFromRoute(ctx context.Context, argoNamespace string) (string, string, error) {
	// List routes with principal labels
	// The ArgoCD operator creates routes with app.kubernetes.io/part-of: argocd-agent
	// and app.kubernetes.io/name containing "agent-principal"
	routeList := &routev1.RouteList{}
	listopts := &client.ListOptions{Namespace: argoNamespace}

	routeSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"app.kubernetes.io/part-of": "argocd-agent",
		},
	}

	routeSelectionLabel, err := utils.ConvertLabels(routeSelector)
	if err != nil {
		return "", "", fmt.Errorf("failed to convert route labels: %w", err)
	}

	listopts.LabelSelector = routeSelectionLabel

	err = r.List(ctx, routeList, listopts)
	if err != nil {
		return "", "", fmt.Errorf("failed to list routes: %w", err)
	}

	if len(routeList.Items) == 0 {
		return "", "", fmt.Errorf("no ArgoCD agent principal route found in namespace %s", argoNamespace)
	}

	// Find the principal route (look for "principal" in the name)
	var route *routev1.Route
	for i := range routeList.Items {
		r := &routeList.Items[i]
		if strings.Contains(r.Name, "principal") {
			route = r
			break
		}
	}

	if route == nil {
		// Fallback to first route if no principal route found
		route = &routeList.Items[0]
	}

	// Get the hostname from the Route
	if route.Spec.Host == "" {
		return "", "", fmt.Errorf("route %s/%s has no host configured", argoNamespace, route.Name)
	}

	serverAddress := route.Spec.Host

	// Determine port based on TLS configuration
	// OpenShift Routes use the router which exposes 443 for HTTPS (TLS) and 80 for HTTP
	serverPort := "443" // Default to HTTPS
	if route.Spec.TLS == nil {
		// No TLS configured, use HTTP port
		serverPort = "80"
		klog.V(2).Infof("Route %s has no TLS configuration, using port 80", route.Name)
	} else {
		klog.V(2).Infof("Route %s has TLS termination type: %s", route.Name, route.Spec.TLS.Termination)
	}

	klog.Infof("Discovered server address from Route %s: %s:%s", route.Name, serverAddress, serverPort)
	return serverAddress, serverPort, nil
}

// discoverFromLoadBalancer discovers the server address from a LoadBalancer Service
func (r *ReconcileGitOpsCluster) discoverFromLoadBalancer(ctx context.Context, argoNamespace string) (string, string, error) {
	// Find the principal service
	service, err := r.FindArgoCDAgentPrincipalService(ctx, argoNamespace)
	if err != nil {
		return "", "", fmt.Errorf("failed to find ArgoCD agent principal service: %w", err)
	}

	// Look for LoadBalancer external endpoints
	var serverAddress string
	var serverPort string

	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.Hostname != "" {
			serverAddress = ingress.Hostname
			klog.V(2).Infof("Discovered server address from LoadBalancer hostname: %s", serverAddress)
			break
		}
		if ingress.IP != "" {
			serverAddress = ingress.IP
			klog.V(2).Infof("Discovered server address from LoadBalancer IP: %s", serverAddress)
			break
		}
	}

	if serverAddress == "" {
		return "", "", fmt.Errorf("ArgoCD agent principal service '%s' in namespace '%s' does not have an external LoadBalancer IP or hostname", service.Name, argoNamespace)
	}

	// Get the port from the service spec
	for _, port := range service.Spec.Ports {
		if port.Name == "https" || port.Port == 443 {
			serverPort = fmt.Sprintf("%d", port.Port)
			klog.V(2).Infof("Discovered server port from service spec: %s", serverPort)
			break
		}
	}

	if serverPort == "" {
		// Try first port if no https port found
		if len(service.Spec.Ports) > 0 {
			serverPort = fmt.Sprintf("%d", service.Spec.Ports[0].Port)
			klog.V(2).Infof("Using first available port from service spec: %s", serverPort)
		} else {
			return "", "", fmt.Errorf("ArgoCD agent principal service '%s' has no ports defined", service.Name)
		}
	}

	return serverAddress, serverPort, nil
}

// GetManagedClusters retrieves managed cluster names from placement decision
func (r *ReconcileGitOpsCluster) GetManagedClusters(namespace string, placementref v1.ObjectReference) ([]*spokeclusterv1.ManagedCluster, error) {
	if !(placementref.Kind == "Placement" &&
		(strings.EqualFold(placementref.APIVersion, "cluster.open-cluster-management.io/v1alpha1") ||
			strings.EqualFold(placementref.APIVersion, "cluster.open-cluster-management.io/v1beta1"))) {
		klog.Error("Invalid Kind or APIVersion, kind: " + placementref.Kind + " apiVerion: " + placementref.APIVersion)
		return nil, errInvalidPlacementRef
	}

	placement := &clusterv1beta1.Placement{}
	err := r.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: placementref.Name}, placement)

	if err != nil {
		klog.Error("failed to get placement. err: ", err.Error())
		return nil, err
	}

	klog.Infof("looking for placement decisions for placement %s", placementref.Name)

	placementDecisions := &clusterv1beta1.PlacementDecisionList{}

	listopts := &client.ListOptions{Namespace: namespace}

	secretSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"cluster.open-cluster-management.io/placement": placementref.Name,
		},
	}

	secretSelectionLabel, err := utils.ConvertLabels(secretSelector)

	if err != nil {
		klog.Error("Failed to convert managed cluster secret selector, err:", err)
		return nil, err
	}

	listopts.LabelSelector = secretSelectionLabel

	err = r.List(context.TODO(), placementDecisions, listopts)

	if err != nil {
		klog.Error("failed to get placement decisions. err: ", err.Error())
		return nil, err
	}

	managedClusters := []*spokeclusterv1.ManagedCluster{}
	clusterNames := []string{}

	for _, placementDecision := range placementDecisions.Items {
		klog.Infof("decisions: %+v for placement: %s", placementDecision.Status.Decisions, placementref.Name)

		for _, clusterDecision := range placementDecision.Status.Decisions {
			managedCluster := &spokeclusterv1.ManagedCluster{}
			err = r.Get(context.TODO(), types.NamespacedName{Name: clusterDecision.ClusterName}, managedCluster)

			if err != nil {
				klog.Error("failed to get managedcluster. err: ", err.Error())
				return nil, err
			}

			managedClusters = append(managedClusters, managedCluster)
			clusterNames = append(clusterNames, clusterDecision.ClusterName)
		}
	}

	klog.Infof("managed cluster names from placement %s: %+v", placementref.Name, clusterNames)

	return managedClusters, nil
}
