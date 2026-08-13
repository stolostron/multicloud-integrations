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
	"os"

	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
)

// CreateAddOnDeploymentConfig creates or updates an AddOnDeploymentConfig for the managed cluster namespace.
// Behavior depends on the overrideExistingConfigs flag:
// - When false (default): preserves all existing variables and only adds new ones from GitOpsCluster spec
// - When true: preserves user variables but overrides managed variables with values from GitOpsCluster spec
//
// agentImageOverride, when non-empty, is the hub's live argocd-agent principal image as detected
// by HealAgentVersionDrift. It takes precedence over the static ARGOCD_AGENT_IMAGE default so the
// spoke's agent always matches the principal's actual running version, while still flowing through
// this same per-cluster AddOnDeploymentConfig -- which ManagedClusterImageRegistry mirrors
// correctly for clusters that can't reach the source registry directly. Pass "" when the caller has
// no agent version drift info (e.g. agent mode disabled, or the principal isn't found yet).
func (r *ReconcileGitOpsCluster) CreateAddOnDeploymentConfig(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedCluster *spokeclusterv1.ManagedCluster, agentImageOverride string) error {
	if managedCluster == nil {
		return errors.New("no managed cluster provided")
	}

	namespace := managedCluster.Name

	// Define variables managed by GitOpsCluster controller
	// Start with ARGOCD_AGENT_ENABLED default, then ExtractVariablesFromGitOpsCluster
	// will populate all other variables from hub environment and GitOpsCluster spec
	managedVariables := map[string]string{
		utils.EnvArgoCDAgentEnabled: "false", // Default value
	}

	// ARGOCD_NAMESPACE tells the gitopsaddon agent which namespace to install ArgoCD into ON
	// THE SPOKE. This is always the fixed utils.GitOpsNamespace -- NOT
	// GetEffectiveArgoNamespace(gitOpsCluster) (the HUB's own, possibly-custom ArgoCD
	// namespace, e.g. "argocd-principal"). The spoke has no reason to have a namespace named
	// after wherever the hub's GitOpsCluster CR happens to live; see the matching comment in
	// argocd_policy.go's generateArgoCDPolicyYaml for the full explanation of this bug.
	managedVariables["ARGOCD_NAMESPACE"] = utils.GitOpsNamespace

	// Extract variables from GitOpsAddon and ArgoCDAgent specs with proper precedence
	r.ExtractVariablesFromGitOpsCluster(gitOpsCluster, managedVariables, agentImageOverride)

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
				CustomizedVariables:   customizedVariables,
				AgentInstallNamespace: utils.AddonAgentNamespace,
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
			// Preserve mode (default): update managed variables to match
			// current spec, preserve user-added variables unchanged.
			for _, variable := range existing.Spec.CustomizedVariables {
				if newValue, ok := managedVariables[variable.Name]; ok {
					updatedVariables = append(updatedVariables, addonv1alpha1.CustomizedVariable{
						Name:  variable.Name,
						Value: newValue,
					})
				} else {
					updatedVariables = append(updatedVariables, variable)
				}
			}

			// Add NEW managed variables that don't already exist
			for name, value := range managedVariables {
				if _, exists := existingVars[name]; !exists {
					updatedVariables = append(updatedVariables, addonv1alpha1.CustomizedVariable{
						Name:  name,
						Value: value,
					})
				}
			}
		}

		// Detect whether agentInstallNamespace needs correction.
		if existing.Spec.AgentInstallNamespace != utils.AddonAgentNamespace {
			klog.Infof("Correcting AddOnDeploymentConfig agentInstallNamespace from %q to %q in namespace %s",
				existing.Spec.AgentInstallNamespace, utils.AddonAgentNamespace, namespace)
		}

		// Only call Update when something actually changed. An unconditional Update
		// bumps resourceVersion on every hub reconcile; the OCM addon framework
		// detects the change and triggers a rolling restart of the addon Deployment
		// on the managed cluster, causing continuous SIGTERM/restart cycles.
		updatedVariables = preserveMirroredImageValues(updatedVariables, existing.Spec.CustomizedVariables, existing.GetAnnotations())

		if !customizedVariablesEqual(updatedVariables, existing.Spec.CustomizedVariables) ||
			existing.Spec.AgentInstallNamespace != utils.AddonAgentNamespace {
			klog.Infof("Updating AddOnDeploymentConfig gitops-addon-config in namespace %s", namespace)
			existing.Spec.CustomizedVariables = updatedVariables
			existing.Spec.AgentInstallNamespace = utils.AddonAgentNamespace

			err = r.Update(context.Background(), existing)
			if err != nil {
				klog.Errorf("Failed to update AddOnDeploymentConfig: %v", err)
				return err
			}
		} else {
			klog.Infof("AddOnDeploymentConfig gitops-addon-config in namespace %s is up to date, skipping update", namespace)
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
// This handles 2 addon template modes:
// 1. gitops-addon (static) - gitopsAddon.enabled=true, argoCDAgent.enabled=false
// 2. gitops-addon-{ns}-{name} (dynamic) - gitopsAddon.enabled=true, argoCDAgent.enabled=true (adds RegistrationSpec for client cert)
// OLM vs embedded operator installation is handled at runtime by the addon agent based on cluster detection.
func (r *ReconcileGitOpsCluster) EnsureManagedClusterAddon(namespace string, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Check if ArgoCD agent is enabled
	_, argoCDAgentEnabled := r.GetGitOpsAddonStatus(gitOpsCluster)

	// Check if ManagedClusterAddOn already exists
	existing := &addonv1alpha1.ManagedClusterAddOn{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon",
		Namespace: namespace,
	}, existing)

	// Determine which AddOnTemplate to use
	expectedConfigs := []addonv1alpha1.AddOnConfig{}
	var templateName string

	if argoCDAgentEnabled {
		// Mode 2: ArgoCD agent enabled (dynamic template with RegistrationSpec for client cert)
		templateName = getAddOnTemplateName(gitOpsCluster)
		klog.Infof("Using dynamic ArgoCD agent AddOnTemplate %s for namespace %s", templateName, namespace)
	} else {
		// Mode 1: Default - no ArgoCD agent (static template from ClusterManagementAddOn default)
		templateName = ""
		klog.Infof("Using default static AddOnTemplate from ClusterManagementAddOn for namespace %s", namespace)
	}

	// Add AddOnTemplate config if not using default
	if templateName != "" {
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
				// Explicitly set the install namespace so the OCM addon framework always
				// places the gitopsaddon agent in the standard ACM addon namespace,
				// regardless of OLM or non-OLM operator installation mode.
				InstallNamespace: utils.AddonAgentNamespace,
				Configs:          expectedConfigs,
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

	// Ensure the install namespace is always the standard ACM addon namespace.
	// This corrects any existing resource that has a wrong or missing InstallNamespace
	// (e.g., set to openshift-operators from an older version).
	if existing.Spec.InstallNamespace != utils.AddonAgentNamespace {
		klog.Infof("Correcting ManagedClusterAddOn gitops-addon installNamespace from %q to %q in namespace %s",
			existing.Spec.InstallNamespace, utils.AddonAgentNamespace, namespace)
		existing.Spec.InstallNamespace = utils.AddonAgentNamespace
		needsUpdate = true
	}

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

// ExtractVariablesFromGitOpsCluster extracts configuration variables from GitOpsCluster spec for AddOnDeploymentConfig.
// This populates managedVariables with all the configuration that should flow from hub to spoke:
// 1. Operator images - from hub operator environment or defaults (excluding hub-only vars)
// 2. Proxy settings - from hub operator environment
// 3. ArgoCD agent settings - from GitOpsCluster spec
// 4. GitOpsCluster spec overrides - takes precedence over environment
//
// agentImageOverride, when non-empty, replaces the ARGOCD_AGENT_IMAGE value computed from the
// static default/hub-env-var above with the hub's live argocd-agent principal image (see
// CreateAddOnDeploymentConfig's doc comment). This must win over both the static default and any
// hub env var, since neither is guaranteed to match the principal's actual running version.
// skip-agent-version-heal on the GitOpsCluster drops ARGOCD_AGENT_IMAGE from the managed set
// entirely (even if agentImageOverride is set), so a user-set Policy agent.image or ADC value
// is not overwritten.
func (r *ReconcileGitOpsCluster) ExtractVariablesFromGitOpsCluster(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedVariables map[string]string, agentImageOverride string) {
	// First, populate with operator images from hub operator environment or defaults
	// This ensures the spoke uses the same images as the hub operator
	// Skip hub-only vars like ARGOCD_PRINCIPAL_IMAGE which are not needed on spoke
	for envKey, defaultValue := range utils.DefaultOperatorImages {
		if utils.IsHubOnlyEnvVar(envKey) {
			continue
		}
		if envValue := os.Getenv(envKey); envValue != "" {
			managedVariables[envKey] = envValue
		} else {
			managedVariables[envKey] = defaultValue
		}
	}

	// Agent version drift heal takes precedence: the hub's live principal image is a more
	// accurate source of truth than the static default/hub-env-var above, particularly for OCP
	// managed clusters whose OLM catalog can independently resolve ARGOCD_AGENT_IMAGE to a
	// different version than the hub's principal at any given moment.
	//
	// skip-agent-version-heal opts the GitOpsCluster out of this entirely: drop ARGOCD_AGENT_IMAGE
	// from the managed set so CreateAddOnDeploymentConfig will not overwrite a user-set ADC
	// value, and will not compete with a user-set Policy spec.argoCDAgent.agent.image (the
	// operator prefers the CR field over the env var when both are present). The Policy image
	// field is never written or cleared by this controller regardless of the annotation.
	if gitOpsCluster.GetAnnotations()[skipAgentVersionHealAnnotation] == "true" {
		delete(managedVariables, utils.EnvArgoCDAgentImage)
	} else if agentImageOverride != "" {
		managedVariables[utils.EnvArgoCDAgentImage] = agentImageOverride
	}

	// Always propagate proxy settings (even as empty strings) so the AddOnTemplate
	// {{HTTP_PROXY}} / {{HTTPS_PROXY}} / {{NO_PROXY}} placeholders are always resolved
	// by the OCM addon framework. If these keys are absent from the ADC, OCM leaves the
	// raw template literal in the pod env var, which Go's http client interprets as a
	// broken proxy URL and routes all connections (including in-cluster API server calls)
	// through it, causing leader election and every API call to fail.
	managedVariables[utils.EnvHTTPProxy] = os.Getenv(utils.EnvHTTPProxy)
	managedVariables[utils.EnvHTTPSProxy] = os.Getenv(utils.EnvHTTPSProxy)
	managedVariables[utils.EnvNoProxy] = os.Getenv(utils.EnvNoProxy)

	// Extract values from GitOpsAddon spec - these override environment settings
	if gitOpsCluster.Spec.GitOpsAddon != nil {
		// GitOpsOperatorImage from spec takes precedence over environment
		if gitOpsCluster.Spec.GitOpsAddon.GitOpsOperatorImage != "" {
			managedVariables[utils.EnvGitOpsOperatorImage] = gitOpsCluster.Spec.GitOpsAddon.GitOpsOperatorImage
		}

		// Extract ArgoCD agent values from the nested structure
		if gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil {
			r.extractArgoCDAgentVariables(gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent, managedVariables)
		}

		// Always populate OLM subscription variables so AddOnTemplate {{…}}
		// placeholders can be resolved. When olmSubscription.enabled is true,
		// OLM_SUBSCRIPTION_ENABLED=true forces the agent to use OLM mode
		// (bypassing OCP auto-detection); otherwise defaults are used and the
		// agent falls back to auto-detection.
		if IsOLMSubscriptionEnabled(gitOpsCluster) {
			managedVariables["OLM_SUBSCRIPTION_ENABLED"] = "true"
			name, ns, channel, source, sourceNs, approval := GetOLMSubscriptionValues(
				gitOpsCluster.Spec.GitOpsAddon.OLMSubscription)
			managedVariables["OLM_SUBSCRIPTION_NAME"] = name
			managedVariables["OLM_SUBSCRIPTION_NAMESPACE"] = ns
			managedVariables["OLM_SUBSCRIPTION_CHANNEL"] = channel
			managedVariables["OLM_SUBSCRIPTION_SOURCE"] = source
			managedVariables["OLM_SUBSCRIPTION_SOURCE_NAMESPACE"] = sourceNs
			managedVariables["OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL"] = approval
		} else {
			managedVariables["OLM_SUBSCRIPTION_ENABLED"] = "false"
			name, ns, channel, source, sourceNs, approval := GetOLMSubscriptionValues(nil)
			managedVariables["OLM_SUBSCRIPTION_NAME"] = name
			managedVariables["OLM_SUBSCRIPTION_NAMESPACE"] = ns
			managedVariables["OLM_SUBSCRIPTION_CHANNEL"] = channel
			managedVariables["OLM_SUBSCRIPTION_SOURCE"] = source
			managedVariables["OLM_SUBSCRIPTION_SOURCE_NAMESPACE"] = sourceNs
			managedVariables["OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL"] = approval
		}
	}
}

// extractArgoCDAgentVariables extracts ArgoCD agent specific variables from ArgoCDAgentSpec
func (r *ReconcileGitOpsCluster) extractArgoCDAgentVariables(argoCDAgent *gitopsclusterV1beta1.ArgoCDAgentSpec, managedVariables map[string]string) {
	if argoCDAgent == nil {
		return
	}

	if argoCDAgent.Enabled != nil && *argoCDAgent.Enabled {
		managedVariables[utils.EnvArgoCDAgentEnabled] = "true"
	}

	if argoCDAgent.ServerAddress != "" {
		managedVariables[utils.EnvArgoCDAgentServerAddress] = argoCDAgent.ServerAddress
	}

	if argoCDAgent.ServerPort != "" {
		managedVariables[utils.EnvArgoCDAgentServerPort] = argoCDAgent.ServerPort
	}

	if argoCDAgent.Mode != "" {
		managedVariables[utils.EnvArgoCDAgentMode] = argoCDAgent.Mode
	}
}

// customizedVariablesEqual returns true when both slices contain the same name/value
// pairs regardless of order.
func customizedVariablesEqual(a, b []addonv1alpha1.CustomizedVariable) bool {
	if len(a) != len(b) {
		return false
	}
	ma := make(map[string]string, len(a))
	for _, v := range a {
		ma[v.Name] = v.Value
	}
	for _, v := range b {
		if val, ok := ma[v.Name]; !ok || val != v.Value {
			return false
		}
	}
	return true
}

// preserveMirroredImageValues keeps live (mirrored) values for any customizedVariable that a
// ManagedClusterImageRegistry is already mirroring, as long as GitOpsCluster's desired
// source-registry value still matches the recorded original. This stops
// CreateAddOnDeploymentConfig from fighting the image-registry controller by resetting
// mirrored values back to the source registry on every reconcile.
//
// When the desired source HAS changed (hub image upgrade, spec override, principal drift
// heal), the new source is written through so the image-registry controller can re-mirror it.
func preserveMirroredImageValues(
	desired, existing []addonv1alpha1.CustomizedVariable,
	annotations map[string]string,
) []addonv1alpha1.CustomizedVariable {
	if len(annotations) == 0 {
		return desired
	}

	origMap := parseOriginalValues(annotations[adcOriginalValuesAnnotation])
	if len(origMap) == 0 {
		return desired
	}

	existingByName := make(map[string]addonv1alpha1.CustomizedVariable, len(existing))
	for _, v := range existing {
		existingByName[v.Name] = v
	}

	out := make([]addonv1alpha1.CustomizedVariable, 0, len(desired))
	for _, v := range desired {
		if orig, tracked := origMap[v.Name]; tracked && orig == v.Value {
			if live, ok := existingByName[v.Name]; ok {
				out = append(out, live)
				continue
			}
		}

		out = append(out, v)
	}

	return out
}
