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
func (r *ReconcileGitOpsCluster) EnsureServerAddressAndPort(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster) (bool, error) {
	// Check if serverAddress and serverPort are already set in the GitOpsCluster spec
	if gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil &&
		(gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress != "" || gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort != "") {
		klog.V(2).Infof("serverAddress or serverPort already set in GitOpsCluster spec, skipping auto-discovery")
		return false, nil
	}

	// Check if any existing AddonDeploymentConfig already has these values set
	hasExistingAddonConfig, err := r.HasExistingServerConfig(managedClusters)
	if err != nil {
		return false, fmt.Errorf("failed to check existing addon configs: %w", err)
	}
	if hasExistingAddonConfig {
		klog.V(2).Infof("serverAddress or serverPort already set in existing AddonDeploymentConfig, skipping auto-discovery")
		return false, nil
	}

	// Try to discover server address and port from the ArgoCD agent principal service
	argoNamespace := gitOpsCluster.Spec.ArgoServer.ArgoNamespace
	if argoNamespace == "" {
		argoNamespace = "openshift-gitops"
	}

	serverAddress, serverPort, err := r.DiscoverServerAddressAndPort(argoNamespace)
	if err != nil {
		return false, fmt.Errorf("failed to discover server address and port: %w", err)
	}

	// Initialize GitOpsAddon and ArgoCDAgent specs if they don't exist
	if gitOpsCluster.Spec.GitOpsAddon == nil {
		gitOpsCluster.Spec.GitOpsAddon = &gitopsclusterV1beta1.GitOpsAddonSpec{}
	}
	if gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent == nil {
		gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent = &gitopsclusterV1beta1.ArgoCDAgentSpec{}
	}

	// Set the discovered values
	gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress = serverAddress
	gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort = serverPort

	klog.Infof("auto-discovered server address: %s, port: %s for GitOpsCluster %s/%s",
		serverAddress, serverPort, gitOpsCluster.Namespace, gitOpsCluster.Name)

	return true, nil
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

// DiscoverServerAddressAndPort discovers the external server address and port from the ArgoCD agent principal service
func (r *ReconcileGitOpsCluster) DiscoverServerAddressAndPort(argoNamespace string) (string, string, error) {
	// Use the existing logic from argocd_agent_certificates.go to find the principal service
	service, err := r.findArgoCDAgentPrincipalService(argoNamespace)
	if err != nil {
		return "", "", fmt.Errorf("failed to find ArgoCD agent principal service: %w", err)
	}

	// Look for LoadBalancer external endpoints
	var serverAddress string
	var serverPort string = "443" // Default HTTPS port

	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.Hostname != "" {
			serverAddress = ingress.Hostname
			klog.V(2).Infof("discovered server address from LoadBalancer hostname: %s", serverAddress)
			break
		}
		if ingress.IP != "" {
			serverAddress = ingress.IP
			klog.V(2).Infof("discovered server address from LoadBalancer IP: %s", serverAddress)
			break
		}
	}

	if serverAddress == "" {
		return "", "", fmt.Errorf("no external LoadBalancer IP or hostname found for service %s in namespace %s", service.Name, argoNamespace)
	}

	// Try to get the actual port from the service spec
	for _, port := range service.Spec.Ports {
		if port.Name == "https" || port.Port == 443 {
			serverPort = fmt.Sprintf("%d", port.Port)
			klog.V(2).Infof("discovered server port from service spec: %s", serverPort)
			break
		}
	}

	klog.Infof("discovered ArgoCD agent server endpoint: %s:%s", serverAddress, serverPort)
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
