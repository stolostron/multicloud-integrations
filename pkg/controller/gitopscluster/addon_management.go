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
	"errors"

	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
)

// CreateAddOnDeploymentConfig creates or updates an AddOnDeploymentConfig for the managed cluster namespace
// It only updates GitOpsCluster-managed variables and preserves user-added custom variables
func (r *ReconcileGitOpsCluster) CreateAddOnDeploymentConfig(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Define variables managed by GitOpsCluster controller - only ArgoCD agent related variables
	managedVariables := map[string]string{
		"ARGOCD_AGENT_ENABLED": "true", // Only default we set
	}

	// Extract variables from GitOpsAddon and ArgoCDAgent specs with proper precedence
	r.ExtractVariablesFromGitOpsCluster(gitOpsCluster, managedVariables)

	// Check if AddOnDeploymentConfig already exists
	existing := &addonv1alpha1.AddOnDeploymentConfig{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon-config",
		Namespace: namespace,
	}, existing)

	if k8errors.IsNotFound(err) {
		// Create new AddOnDeploymentConfig with default managed variables
		klog.Infof("Creating AddOnDeploymentConfig gitops-addon-config in namespace %s", namespace)

		customizedVariables := make([]addonv1alpha1.CustomizedVariable, 0, len(managedVariables))
		for name, value := range managedVariables {
			customizedVariables = append(customizedVariables, addonv1alpha1.CustomizedVariable{
				Name:  name,
				Value: value,
			})
		}

		addonDeploymentConfig := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: namespace,
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: customizedVariables,
			},
		}

		err = r.Create(context.Background(), addonDeploymentConfig)
		if err != nil {
			klog.Errorf("Failed to create AddOnDeploymentConfig: %v", err)
			return err
		}
	} else if err != nil {
		klog.Errorf("Failed to get AddOnDeploymentConfig: %v", err)
		return err
	} else {
		// Update existing AddOnDeploymentConfig - merge managed variables with existing ones
		klog.Infof("Updating AddOnDeploymentConfig gitops-addon-config in namespace %s", namespace)

		// Create a map of existing variables for easy lookup
		existingVars := make(map[string]addonv1alpha1.CustomizedVariable)
		for _, variable := range existing.Spec.CustomizedVariables {
			existingVars[variable.Name] = variable
		}

		// Update or add managed variables, preserve user-added variables
		updatedVariables := make([]addonv1alpha1.CustomizedVariable, 0)

		// First, add all existing user variables (non-managed ones will be preserved)
		for _, variable := range existing.Spec.CustomizedVariables {
			if _, isManaged := managedVariables[variable.Name]; !isManaged {
				// This is a user-added variable, preserve it
				updatedVariables = append(updatedVariables, variable)
			}
		}

		// Then add/update all managed variables with current values
		for name, value := range managedVariables {
			updatedVariables = append(updatedVariables, addonv1alpha1.CustomizedVariable{
				Name:  name,
				Value: value,
			})
		}

		existing.Spec.CustomizedVariables = updatedVariables
		err = r.Update(context.Background(), existing)

		if err != nil {
			klog.Errorf("Failed to update AddOnDeploymentConfig: %v", err)
			return err
		}
	}

	// Check and update existing ManagedClusterAddOn if it exists
	err = r.UpdateManagedClusterAddonConfig(namespace)
	if err != nil {
		klog.Errorf("Failed to update ManagedClusterAddOn config: %v", err)
	}

	return nil
}

// UpdateManagedClusterAddonConfig updates the ManagedClusterAddOn configs to reference the AddOnDeploymentConfig
func (r *ReconcileGitOpsCluster) UpdateManagedClusterAddonConfig(namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Check if ManagedClusterAddOn exists
	existing := &addonv1alpha1.ManagedClusterAddOn{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon",
		Namespace: namespace,
	}, existing)

	if k8errors.IsNotFound(err) {
		// ManagedClusterAddOn doesn't exist, nothing to update
		klog.V(2).Infof("ManagedClusterAddOn gitops-addon not found in namespace %s, skipping config update", namespace)
		return nil
	} else if err != nil {
		klog.Errorf("Failed to get ManagedClusterAddOn gitops-addon: %v", err)
		return err
	}

	// Check if the config reference already exists and points to the correct AddOnDeploymentConfig
	expectedConfig := addonv1alpha1.AddOnConfig{
		ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
			Group:    "addon.open-cluster-management.io",
			Resource: "addondeploymentconfigs",
		},
		ConfigReferent: addonv1alpha1.ConfigReferent{
			Name:      "gitops-addon-config",
			Namespace: namespace,
		},
	}

	// Check if the expected config already exists in the configs list
	configExists := false

	for _, config := range existing.Spec.Configs {
		if config.Group == expectedConfig.Group &&
			config.Resource == expectedConfig.Resource &&
			config.Name == expectedConfig.Name &&
			config.Namespace == expectedConfig.Namespace {
			configExists = true
			break
		}
	}

	if configExists {
		klog.V(2).Infof("ManagedClusterAddOn gitops-addon already has correct config reference in namespace %s", namespace)
		return nil
	}

	// Add the config reference if it doesn't exist
	existing.Spec.Configs = append(existing.Spec.Configs, expectedConfig)

	err = r.Update(context.Background(), existing)
	if err != nil {
		klog.Errorf("Failed to update ManagedClusterAddOn gitops-addon: %v", err)
		return err
	}

	klog.Infof("Updated ManagedClusterAddOn gitops-addon config reference in namespace %s", namespace)

	return nil
}

// EnsureManagedClusterAddon creates the ManagedClusterAddon if it doesn't exist, or updates its config if it does
func (r *ReconcileGitOpsCluster) EnsureManagedClusterAddon(namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Check if ManagedClusterAddOn already exists
	existing := &addonv1alpha1.ManagedClusterAddOn{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon",
		Namespace: namespace,
	}, existing)

	expectedConfig := addonv1alpha1.AddOnConfig{
		ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
			Group:    "addon.open-cluster-management.io",
			Resource: "addondeploymentconfigs",
		},
		ConfigReferent: addonv1alpha1.ConfigReferent{
			Name:      "gitops-addon-config",
			Namespace: namespace,
		},
	}

	if k8errors.IsNotFound(err) {
		// Create new ManagedClusterAddOn with config reference
		klog.Infof("Creating ManagedClusterAddOn gitops-addon in namespace %s", namespace)

		managedClusterAddOn := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: namespace,
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				Configs:          []addonv1alpha1.AddOnConfig{expectedConfig},
			},
		}

		err = r.Create(context.Background(), managedClusterAddOn)
		if err != nil {
			klog.Errorf("Failed to create ManagedClusterAddOn gitops-addon: %v", err)
			return err
		}

		klog.Infof("Successfully created ManagedClusterAddOn gitops-addon in namespace %s", namespace)
		return nil
	} else if err != nil {
		klog.Errorf("Failed to get ManagedClusterAddOn gitops-addon: %v", err)
		return err
	}

	// ManagedClusterAddOn exists, ensure it has the correct config reference
	configExists := false
	for _, config := range existing.Spec.Configs {
		if config.Group == expectedConfig.Group &&
			config.Resource == expectedConfig.Resource &&
			config.Name == expectedConfig.Name &&
			config.Namespace == expectedConfig.Namespace {
			configExists = true
			break
		}
	}

	if !configExists {
		// Add the config reference if it doesn't exist
		klog.Infof("Adding config reference to existing ManagedClusterAddOn gitops-addon in namespace %s", namespace)
		existing.Spec.Configs = append(existing.Spec.Configs, expectedConfig)

		err = r.Update(context.Background(), existing)
		if err != nil {
			klog.Errorf("Failed to update ManagedClusterAddOn gitops-addon: %v", err)
			return err
		}

		klog.Infof("Updated ManagedClusterAddOn gitops-addon config reference in namespace %s", namespace)
	} else {
		klog.V(2).Infof("ManagedClusterAddOn gitops-addon already has correct config reference in namespace %s", namespace)
	}

	return nil
}

// GetGitOpsAddonStatus returns the status of GitOps addon and ArgoCD agent
func (r *ReconcileGitOpsCluster) GetGitOpsAddonStatus(instance *gitopsclusterV1beta1.GitOpsCluster) (bool, bool) {
	// Check if GitOps addon is enabled
	gitopsAddonEnabled := false
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.Enabled != nil {
		gitopsAddonEnabled = *instance.Spec.GitOpsAddon.Enabled
	}

	// Check if ArgoCD agent is enabled
	argoCDAgentEnabled := false
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.ArgoCDAgent != nil && instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled
	}

	return gitopsAddonEnabled, argoCDAgentEnabled
}

// ExtractVariablesFromGitOpsCluster extracts configuration variables from GitOpsCluster spec for AddOnDeploymentConfig
func (r *ReconcileGitOpsCluster) ExtractVariablesFromGitOpsCluster(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedVariables map[string]string) {
	// Extract values from GitOpsAddon spec
	if gitOpsCluster.Spec.GitOpsAddon != nil {
		if gitOpsCluster.Spec.GitOpsAddon.GitOpsOperatorImage != "" {
			managedVariables["GITOPS_OPERATOR_IMAGE"] = gitOpsCluster.Spec.GitOpsAddon.GitOpsOperatorImage
		}

		if gitOpsCluster.Spec.GitOpsAddon.GitOpsImage != "" {
			managedVariables["GITOPS_IMAGE"] = gitOpsCluster.Spec.GitOpsAddon.GitOpsImage
		}

		if gitOpsCluster.Spec.GitOpsAddon.RedisImage != "" {
			managedVariables["REDIS_IMAGE"] = gitOpsCluster.Spec.GitOpsAddon.RedisImage
		}

		if gitOpsCluster.Spec.GitOpsAddon.GitOpsOperatorNamespace != "" {
			managedVariables["GITOPS_OPERATOR_NAMESPACE"] = gitOpsCluster.Spec.GitOpsAddon.GitOpsOperatorNamespace
		}

		if gitOpsCluster.Spec.GitOpsAddon.GitOpsNamespace != "" {
			managedVariables["GITOPS_NAMESPACE"] = gitOpsCluster.Spec.GitOpsAddon.GitOpsNamespace
		}

		if gitOpsCluster.Spec.GitOpsAddon.ReconcileScope != "" {
			managedVariables["RECONCILE_SCOPE"] = gitOpsCluster.Spec.GitOpsAddon.ReconcileScope
		}

		if gitOpsCluster.Spec.GitOpsAddon.Action != "" {
			managedVariables["ACTION"] = gitOpsCluster.Spec.GitOpsAddon.Action
		}

		// Extract ArgoCD agent values from the nested structure
		if gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil {
			r.extractArgoCDAgentVariables(gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent, managedVariables)
		}
	}
}

// extractArgoCDAgentVariables extracts ArgoCD agent specific variables from ArgoCDAgentSpec
func (r *ReconcileGitOpsCluster) extractArgoCDAgentVariables(argoCDAgent *gitopsclusterV1beta1.ArgoCDAgentSpec, managedVariables map[string]string) {
	if argoCDAgent == nil {
		return
	}

	if argoCDAgent.Image != "" {
		managedVariables["ARGOCD_AGENT_IMAGE"] = argoCDAgent.Image
	}

	if argoCDAgent.ServerAddress != "" {
		managedVariables["ARGOCD_AGENT_SERVER_ADDRESS"] = argoCDAgent.ServerAddress
	}

	if argoCDAgent.ServerPort != "" {
		managedVariables["ARGOCD_AGENT_SERVER_PORT"] = argoCDAgent.ServerPort
	}

	if argoCDAgent.Mode != "" {
		managedVariables["ARGOCD_AGENT_MODE"] = argoCDAgent.Mode
	}
}
