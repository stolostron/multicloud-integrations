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
	"encoding/json"
	"errors"
	"fmt"

	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateAddOnDeploymentConfig creates or updates an AddOnDeploymentConfig for the managed cluster namespace
// Behavior depends on the overrideExistingConfigs flag:
// - When false (default): preserves all existing variables and only adds new ones from GitOpsCluster spec
// - When true: preserves user variables but overrides managed variables with values from GitOpsCluster spec
func (r *ReconcileGitOpsCluster) CreateAddOnDeploymentConfig(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Check if OLM subscription mode is enabled
	olmSubscriptionEnabled := IsOLMSubscriptionEnabled(gitOpsCluster)

	// Define variables managed by GitOpsCluster controller - only ArgoCD agent related variables
	managedVariables := map[string]string{
		"ARGOCD_AGENT_ENABLED": "false", // Only default we set
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

		// For OLM subscription mode (template-type addon), set AgentInstallNamespace to ""
		// to use the namespace defined in the AddOnTemplate manifest.
		// We must use JSON merge patch after creation because the kubebuilder default
		// would override empty string during creation.
		// See: https://github.com/open-cluster-management-io/api/blob/main/addon/v1alpha1/types_addondeploymentconfig.go#L56-L63
		if olmSubscriptionEnabled {
			klog.Infof("Patching AgentInstallNamespace to empty string for OLM subscription mode in namespace %s", namespace)
			err = r.patchAgentInstallNamespaceToEmpty(namespace)
			if err != nil {
				klog.Errorf("Failed to patch AgentInstallNamespace: %v", err)
				return err
			}
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

		// Determine behavior based on overrideExistingConfigs setting
		shouldOverrideExisting := false
		if gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.OverrideExistingConfigs != nil {
			shouldOverrideExisting = *gitOpsCluster.Spec.GitOpsAddon.OverrideExistingConfigs
		}

		updatedVariables := make([]addonv1alpha1.CustomizedVariable, 0)

		if shouldOverrideExisting {
			// Override mode: preserve user variables, update/add managed variables
			for _, variable := range existing.Spec.CustomizedVariables {
				if _, isManaged := managedVariables[variable.Name]; !isManaged {
					// This is a user-added variable, preserve it
					updatedVariables = append(updatedVariables, variable)
				}
			}

			// Add/update all managed variables with current values
			for name, value := range managedVariables {
				updatedVariables = append(updatedVariables, addonv1alpha1.CustomizedVariable{
					Name:  name,
					Value: value,
				})
			}
		} else {
			// Preserve mode (default): preserve ALL existing variables and only add new ones
			// EXCEPT for ARGOCD_AGENT_ENABLED which should always reflect the current state
			for _, variable := range existing.Spec.CustomizedVariables {
				if variable.Name == "ARGOCD_AGENT_ENABLED" {
					// Always update ARGOCD_AGENT_ENABLED to match current argoCDAgent.enabled state
					if newValue, exists := managedVariables["ARGOCD_AGENT_ENABLED"]; exists {
						updatedVariables = append(updatedVariables, addonv1alpha1.CustomizedVariable{
							Name:  "ARGOCD_AGENT_ENABLED",
							Value: newValue,
						})
					}
				} else {
					// Preserve other existing variables
					updatedVariables = append(updatedVariables, variable)
				}
			}

			// Add only NEW managed variables that don't already exist
			for name, value := range managedVariables {
				if _, exists := existingVars[name]; !exists {
					// Only add if the variable doesn't already exist
					updatedVariables = append(updatedVariables, addonv1alpha1.CustomizedVariable{
						Name:  name,
						Value: value,
					})
				}
			}
		}

		existing.Spec.CustomizedVariables = updatedVariables

		err = r.Update(context.Background(), existing)
		if err != nil {
			klog.Errorf("Failed to update AddOnDeploymentConfig: %v", err)
			return err
		}

		// For OLM subscription mode (template-type addon), always patch AgentInstallNamespace to ""
		// to use the namespace defined in the AddOnTemplate manifest.
		// The Update() call above re-applies the kubebuilder default, so we must always patch.
		// See: https://github.com/open-cluster-management-io/api/blob/main/addon/v1alpha1/types_addondeploymentconfig.go#L56-L63
		if olmSubscriptionEnabled {
			klog.Infof("Patching AgentInstallNamespace to empty string for OLM subscription mode in namespace %s", namespace)
			err = r.patchAgentInstallNamespaceToEmpty(namespace)
			if err != nil {
				klog.Errorf("Failed to patch AgentInstallNamespace: %v", err)
				return err
			}
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
func (r *ReconcileGitOpsCluster) EnsureManagedClusterAddon(namespace string, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Get the AddOnTemplate name for this GitOpsCluster
	templateName := fmt.Sprintf("gitops-addon-%s-%s", gitOpsCluster.Namespace, gitOpsCluster.Name)

	// Check if OLM subscription mode is enabled
	olmSubscriptionEnabled := IsOLMSubscriptionEnabled(gitOpsCluster)

	// Get OLM AddOnTemplate name if OLM mode is enabled
	olmTemplateName := getOLMAddOnTemplateName(gitOpsCluster)

	// Check if ManagedClusterAddOn already exists
	existing := &addonv1alpha1.ManagedClusterAddOn{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon",
		Namespace: namespace,
	}, existing)

	// Check if ArgoCD agent is enabled
	_, argoCDAgentEnabled := r.GetGitOpsAddonStatus(gitOpsCluster)

	// Build expected configs based on mode:
	// 1. OLM subscription mode: use OLM AddOnTemplate (takes precedence)
	// 2. ArgoCD agent mode: use dynamic AddOnTemplate
	// 3. Default mode: use default template from ClusterManagementAddOn
	expectedConfigs := []addonv1alpha1.AddOnConfig{}

	if olmSubscriptionEnabled {
		// Add OLM AddOnTemplate config when OLM subscription mode is enabled
		klog.Infof("Adding OLM AddOnTemplate %s to ManagedClusterAddOn config for namespace %s", olmTemplateName, namespace)
		expectedConfigs = append(expectedConfigs, addonv1alpha1.AddOnConfig{
			ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
				Group:    "addon.open-cluster-management.io",
				Resource: "addontemplates",
			},
			ConfigReferent: addonv1alpha1.ConfigReferent{
				Name: olmTemplateName,
			},
		})
	} else if argoCDAgentEnabled {
		// Add dynamic AddOnTemplate config only when ArgoCD agent is enabled (and OLM is not)
		expectedConfigs = append(expectedConfigs, addonv1alpha1.AddOnConfig{
			ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
				Group:    "addon.open-cluster-management.io",
				Resource: "addontemplates",
			},
			ConfigReferent: addonv1alpha1.ConfigReferent{
				Name: templateName,
			},
		})
	}

	// Always add AddOnDeploymentConfig
	expectedConfigs = append(expectedConfigs, addonv1alpha1.AddOnConfig{
		ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
			Group:    "addon.open-cluster-management.io",
			Resource: "addondeploymentconfigs",
		},
		ConfigReferent: addonv1alpha1.ConfigReferent{
			Name:      "gitops-addon-config",
			Namespace: namespace,
		},
	})

	if k8errors.IsNotFound(err) {
		// Create new ManagedClusterAddOn with both config references
		klog.Infof("Creating ManagedClusterAddOn gitops-addon in namespace %s with AddOnTemplate %s", namespace, templateName)

		managedClusterAddOn := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "multicloud-integrations",
					"app.kubernetes.io/component":  "addon",
				},
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				Configs: expectedConfigs,
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

	// ManagedClusterAddOn exists, ensure it has all correct config references
	needsUpdate := false

	// Add missing configs
	for _, expectedConfig := range expectedConfigs {
		configExists := false
		for _, config := range existing.Spec.Configs {
			if config.Group == expectedConfig.Group &&
				config.Resource == expectedConfig.Resource &&
				config.Name == expectedConfig.Name {
				// Check namespace only if it's set in expected config (AddOnTemplate doesn't have namespace)
				if expectedConfig.Namespace == "" || config.Namespace == expectedConfig.Namespace {
					configExists = true
					break
				}
			}
		}

		if !configExists {
			// Add the config reference if it doesn't exist
			klog.Infof("Adding %s config reference to ManagedClusterAddOn gitops-addon in namespace %s", expectedConfig.Resource, namespace)
			existing.Spec.Configs = append(existing.Spec.Configs, expectedConfig)
			needsUpdate = true
		}
	}

	// Remove configs that are no longer expected (e.g., AddOnTemplate when ArgoCD agent is disabled)
	newConfigs := []addonv1alpha1.AddOnConfig{}
	for _, config := range existing.Spec.Configs {
		configExpected := false
		for _, expectedConfig := range expectedConfigs {
			if config.Group == expectedConfig.Group &&
				config.Resource == expectedConfig.Resource &&
				config.Name == expectedConfig.Name {
				// Check namespace only if it's set in expected config (AddOnTemplate doesn't have namespace)
				if expectedConfig.Namespace == "" || config.Namespace == expectedConfig.Namespace {
					configExpected = true
					break
				}
			}
		}

		if configExpected {
			newConfigs = append(newConfigs, config)
		} else {
			// This config is no longer expected, remove it
			klog.Infof("Removing %s config reference from ManagedClusterAddOn gitops-addon in namespace %s", config.Resource, namespace)
			needsUpdate = true
		}
	}

	if needsUpdate {
		existing.Spec.Configs = newConfigs
		err = r.Update(context.Background(), existing)
		if err != nil {
			klog.Errorf("Failed to update ManagedClusterAddOn gitops-addon: %v", err)
			return err
		}

		klog.Infof("Updated ManagedClusterAddOn gitops-addon config references in namespace %s", namespace)
	} else {
		klog.V(2).Infof("ManagedClusterAddOn gitops-addon already has correct config references in namespace %s", namespace)
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

		// GITOPS_OPERATOR_NAMESPACE is always openshift-gitops-operator (no longer configurable)
		managedVariables["GITOPS_OPERATOR_NAMESPACE"] = utils.GitOpsOperatorNamespace

		// GITOPS_NAMESPACE is always openshift-gitops (no longer configurable)
		managedVariables["GITOPS_NAMESPACE"] = utils.GitOpsNamespace

		if gitOpsCluster.Spec.GitOpsAddon.ReconcileScope != "" {
			managedVariables["RECONCILE_SCOPE"] = gitOpsCluster.Spec.GitOpsAddon.ReconcileScope
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

	if argoCDAgent.Enabled != nil && *argoCDAgent.Enabled {
		managedVariables["ARGOCD_AGENT_ENABLED"] = "true"
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

// patchAgentInstallNamespaceToEmpty patches the AgentInstallNamespace field to empty string
// using JSON merge patch. This is required because the kubebuilder default on the field
// prevents us from setting it to empty string via normal Create/Update operations.
func (r *ReconcileGitOpsCluster) patchAgentInstallNamespaceToEmpty(namespace string) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"agentInstallNamespace": "",
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	existing := &addonv1alpha1.AddOnDeploymentConfig{}
	err = r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon-config",
		Namespace: namespace,
	}, existing)
	if err != nil {
		return fmt.Errorf("failed to get AddOnDeploymentConfig: %w", err)
	}

	err = r.Patch(context.Background(), existing, client.RawPatch(types.MergePatchType, patchBytes))
	if err != nil {
		return fmt.Errorf("failed to patch AddOnDeploymentConfig: %w", err)
	}

	klog.Infof("Successfully patched AgentInstallNamespace to empty string in namespace %s", namespace)
	return nil
}
