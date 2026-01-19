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
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	workv1 "open-cluster-management.io/api/work/v1"
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

// getOLMAddOnTemplateName generates a unique AddOnTemplate name for OLM subscription mode
func getOLMAddOnTemplateName(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) string {
	return fmt.Sprintf("gitops-addon-olm-%s-%s", gitOpsCluster.Namespace, gitOpsCluster.Name)
}

// IsOLMSubscriptionEnabled checks if OLM subscription mode is enabled
// OLM subscription requires both gitopsAddon.enabled and olmSubscription.enabled to be true
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

// EnsureOLMAddOnTemplate creates or updates the AddOnTemplate for OLM subscription mode
func (r *ReconcileGitOpsCluster) EnsureOLMAddOnTemplate(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	templateName := getOLMAddOnTemplateName(gitOpsCluster)

	// Get OLM subscription values with defaults
	subName, subNamespace, channel, source, sourceNamespace, installPlanApproval := GetOLMSubscriptionValues(gitOpsCluster.Spec.GitOpsAddon.OLMSubscription)

	// Build the AddOnTemplate with OLM Subscription
	addonTemplate := &addonv1alpha1.AddOnTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: templateName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":                            "multicloud-integrations",
				"app.kubernetes.io/component":                             "addon-template-olm",
				"gitopscluster.apps.open-cluster-management.io/name":      gitOpsCluster.Name,
				"gitopscluster.apps.open-cluster-management.io/namespace": gitOpsCluster.Namespace,
			},
		},
		Spec: addonv1alpha1.AddOnTemplateSpec{
			AddonName: "gitops-addon",
			AgentSpec: workv1.ManifestWorkSpec{
				Workload: workv1.ManifestsTemplate{
					Manifests: buildOLMSubscriptionManifests(subName, subNamespace, channel, source, sourceNamespace, installPlanApproval),
				},
			},
		},
	}

	// Check if AddOnTemplate already exists
	existing := &addonv1alpha1.AddOnTemplate{}
	err := r.Get(context.Background(), types.NamespacedName{Name: templateName}, existing)
	if err == nil {
		// AddOnTemplate exists, update it
		klog.V(2).Infof("Updating OLM AddOnTemplate %s", templateName)
		existing.Spec = addonTemplate.Spec
		existing.Labels = addonTemplate.Labels
		return r.Update(context.Background(), existing)
	}

	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to get OLM AddOnTemplate: %w", err)
	}

	// Create new AddOnTemplate
	klog.Infof("Creating OLM AddOnTemplate %s", templateName)
	return r.Create(context.Background(), addonTemplate)
}

// buildOLMSubscriptionManifests builds the manifest list for the OLM AddOnTemplate
func buildOLMSubscriptionManifests(name, namespace, channel, source, sourceNamespace, installPlanApproval string) []workv1.Manifest {
	// Create the Subscription resource as unstructured
	subscription := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by":                            "multicloud-integrations",
					"gitopscluster.apps.open-cluster-management.io/olm-addon": "true",
				},
			},
			"spec": map[string]interface{}{
				"channel":             channel,
				"name":                name,
				"source":              source,
				"sourceNamespace":     sourceNamespace,
				"installPlanApproval": installPlanApproval,
				"config": map[string]interface{}{
					"env": []map[string]interface{}{
						{
							"name":  "DISABLE_DEFAULT_ARGOCD_INSTANCE",
							"value": "true",
						},
					},
				},
			},
		},
	}

	manifests := []workv1.Manifest{
		newManifestFromUnstructured(subscription),
	}

	return manifests
}

// newManifestFromUnstructured creates a workv1.Manifest from an unstructured object
func newManifestFromUnstructured(obj *unstructured.Unstructured) workv1.Manifest {
	jsonBytes, err := json.Marshal(obj.Object)
	if err != nil {
		klog.Errorf("Failed to marshal unstructured object: %v", err)
		return workv1.Manifest{
			RawExtension: runtime.RawExtension{
				Object: obj,
			},
		}
	}

	return workv1.Manifest{
		RawExtension: runtime.RawExtension{
			Raw: jsonBytes,
		},
	}
}

