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
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
)

const (
	// Default values for OLM Subscription
	DefaultOLMSubscriptionName            = "openshift-gitops-operator"
	DefaultOLMSubscriptionNamespace       = "openshift-operators"
	DefaultOLMSubscriptionChannel         = "latest"
	DefaultOLMSubscriptionSource          = "redhat-operators"
	DefaultOLMSubscriptionSourceNamespace = "openshift-marketplace"
	DefaultOLMInstallPlanApproval         = "Automatic"
)

// IsOLMSubscriptionEnabled checks if custom OLM subscription overrides are requested.
// Requires both gitopsAddon.enabled and olmSubscription.enabled to be true.
// Note: OLM_SUBSCRIPTION_* env vars are always populated in the AddOnDeploymentConfig
// (with defaults) so that AddOnTemplate placeholders can be resolved. This function
// only controls whether OLM_SUBSCRIPTION_ENABLED is set to "true" (custom overrides active)
// vs "false" (auto-detect mode with defaults).
func IsOLMSubscriptionEnabled(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) bool {
	if gitOpsCluster.Spec.GitOpsAddon == nil {
		return false
	}
	if gitOpsCluster.Spec.GitOpsAddon.Enabled == nil || !*gitOpsCluster.Spec.GitOpsAddon.Enabled {
		return false
	}
	if gitOpsCluster.Spec.GitOpsAddon.OLMSubscription == nil {
		return false
	}
	if gitOpsCluster.Spec.GitOpsAddon.OLMSubscription.Enabled == nil {
		return false
	}
	return *gitOpsCluster.Spec.GitOpsAddon.OLMSubscription.Enabled
}

// HasCustomOLMSubscriptionValues checks if any custom OLM subscription values are specified
func HasCustomOLMSubscriptionValues(olmSpec *gitopsclusterV1beta1.OLMSubscriptionSpec) bool {
	if olmSpec == nil {
		return false
	}
	return olmSpec.Name != "" || olmSpec.Namespace != "" || olmSpec.Channel != "" ||
		olmSpec.Source != "" || olmSpec.SourceNamespace != "" || olmSpec.InstallPlanApproval != ""
}

// GetOLMSubscriptionValues returns the OLM subscription values with defaults applied
func GetOLMSubscriptionValues(olmSpec *gitopsclusterV1beta1.OLMSubscriptionSpec) (name, namespace, channel, source, sourceNamespace, installPlanApproval string) {
	name = DefaultOLMSubscriptionName
	namespace = DefaultOLMSubscriptionNamespace
	channel = DefaultOLMSubscriptionChannel
	source = DefaultOLMSubscriptionSource
	sourceNamespace = DefaultOLMSubscriptionSourceNamespace
	installPlanApproval = DefaultOLMInstallPlanApproval

	if olmSpec == nil {
		return
	}

	if olmSpec.Name != "" {
		name = olmSpec.Name
	}
	if olmSpec.Namespace != "" {
		namespace = olmSpec.Namespace
	}
	if olmSpec.Channel != "" {
		channel = olmSpec.Channel
	}
	if olmSpec.Source != "" {
		source = olmSpec.Source
	}
	if olmSpec.SourceNamespace != "" {
		sourceNamespace = olmSpec.SourceNamespace
	}
	if olmSpec.InstallPlanApproval != "" {
		installPlanApproval = olmSpec.InstallPlanApproval
	}

	return
}
