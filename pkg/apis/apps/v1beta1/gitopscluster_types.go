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

package v1beta1

import (
	"crypto/tls"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// TLS minimum version as an integer
	TLSMinVersionInt = tls.VersionTLS12
)

// GitOpsCluster condition types
const (
	// GitOpsClusterReady indicates whether the GitOpsCluster is ready and functioning correctly.
	GitOpsClusterReady = "Ready"

	// GitOpsClusterPlacementResolved indicates whether the placement reference was resolved
	// and managed clusters were successfully retrieved.
	GitOpsClusterPlacementResolved = "PlacementResolved"

	// GitOpsClusterClustersRegistered indicates whether managed clusters were successfully
	// registered with the ArgoCD server.
	GitOpsClusterClustersRegistered = "ClustersRegistered"

	// GitOpsClusterArgoCDAgentPrereqsReady indicates whether the ArgoCD agent prerequisites
	// (like JWT secrets and configuration) are properly set up.
	GitOpsClusterArgoCDAgentPrereqsReady = "ArgoCDAgentPrereqsReady"

	// GitOpsClusterCertificatesReady indicates whether ArgoCD agent certificates are properly
	// signed and ready for use.
	GitOpsClusterCertificatesReady = "CertificatesReady"

	// GitOpsClusterManifestWorksApplied indicates whether CA propagation ManifestWorks were
	// successfully applied to managed clusters.
	GitOpsClusterManifestWorksApplied = "ManifestWorksApplied"
)

// GitOpsCluster condition reasons
const (
	// Success reasons
	ReasonSuccess     = "Success"
	ReasonDisabled    = "Disabled"
	ReasonNotRequired = "NotRequired"

	// Error reasons
	ReasonInvalidConfiguration      = "InvalidConfiguration"
	ReasonPlacementNotFound         = "PlacementNotFound"
	ReasonManagedClustersNotFound   = "ManagedClustersNotFound"
	ReasonArgoServerNotFound        = "ArgoServerNotFound"
	ReasonClusterRegistrationFailed = "ClusterRegistrationFailed"
	ReasonCertificateSigningFailed  = "CertificateSigningFailed"
	ReasonManifestWorkFailed        = "ManifestWorkFailed"
	ReasonArgoCDAgentFailed         = "ArgoCDAgentFailed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope="Namespaced"

// The GitOpsCluster uses placement to import selected managed clusters into the Argo CD.
type GitOpsCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec   GitOpsClusterSpec   `json:"spec"`
	Status GitOpsClusterStatus `json:"status,omitempty"`
}

// GitOpsClusterSpec defines the desired state of GitOpsCluster.
type GitOpsClusterSpec struct {
	ArgoServer ArgoServerSpec `json:"argoServer"`

	PlacementRef *corev1.ObjectReference `json:"placementRef"`

	// ManagedServiceAccountRef defines managed service account in the managed cluster namespace used to create the ArgoCD cluster secret.
	ManagedServiceAccountRef string `json:"managedServiceAccountRef,omitempty"`

	// GitOpsAddon defines the configuration for the GitOps addon.
	GitOpsAddon *GitOpsAddonSpec `json:"gitopsAddon,omitempty"`

	// Internally used.
	CreateBlankClusterSecrets *bool `json:"createBlankClusterSecrets,omitempty"`

	// Create default policy template if it is true.
	CreatePolicyTemplate *bool `json:"createPolicyTemplate,omitempty"`
}

// ArgoServerSpec specifies the location of the Argo CD server.
type ArgoServerSpec struct {
	// Not used and reserved for defining a managed cluster name.
	Cluster string `json:"cluster,omitempty"`

	// ArgoNamespace is the namespace in which the Argo CD server is installed.
	ArgoNamespace string `json:"argoNamespace"`
}

// GitOpsAddonSpec defines the configuration for the GitOps addon.
type GitOpsAddonSpec struct {
	// Enabled indicates whether the GitOps addon is enabled. Default is false.
	// When enabled, creates AddonDeploymentConfigs/ManagedClusterAddon resources.
	// +kubebuilder:default=false
	Enabled *bool `json:"enabled,omitempty"`

	// GitOpsOperatorImage specifies the GitOps operator container image. Default is empty.
	GitOpsOperatorImage string `json:"gitOpsOperatorImage,omitempty"`

	// GitOpsImage specifies the GitOps (ArgoCD) container image. Default is empty.
	GitOpsImage string `json:"gitOpsImage,omitempty"`

	// RedisImage specifies the Redis container image. Default is empty.
	RedisImage string `json:"redisImage,omitempty"`

	// GitOpsOperatorNamespace specifies the GitOps operator namespace. Default is empty.
	GitOpsOperatorNamespace string `json:"gitOpsOperatorNamespace,omitempty"`

	// GitOpsNamespace specifies the GitOps namespace. Default is empty.
	GitOpsNamespace string `json:"gitOpsNamespace,omitempty"`

	// ReconcileScope specifies the reconcile scope for the GitOps operator. Default is empty.
	ReconcileScope string `json:"reconcileScope,omitempty"`

	// Cleanup indicates whether to perform cleanup when the addon is deleted. Default is false.
	// +kubebuilder:default=false
	Cleanup *bool `json:"cleanup,omitempty"`

	// ArgoCDAgent defines the configuration for the ArgoCD agent.
	ArgoCDAgent *ArgoCDAgentSpec `json:"argoCDAgent,omitempty"`

	// OverrideExistingConfigs indicates whether to override existing configuration values in AddOnDeploymentConfig.
	// When false (default), existing config values are preserved and only new ones are added.
	// When true, config values from GitOpsCluster spec will override existing values.
	// +kubebuilder:default=false
	OverrideExistingConfigs *bool `json:"overrideExistingConfigs,omitempty"`
}

// ArgoCDAgentSpec defines the configuration for the ArgoCD agent.
type ArgoCDAgentSpec struct {
	// Enabled indicates whether the ArgoCD agent is enabled. Default is false.
	// +kubebuilder:default=false
	Enabled *bool `json:"enabled,omitempty"`

	// PropagateHubCA indicates whether to propagate the hub CA certificate to managed clusters via ManifestWork. Default is true.
	// +kubebuilder:default=true
	PropagateHubCA *bool `json:"propagateHubCA,omitempty"`

	// Image specifies the ArgoCD agent container image. Default is empty.
	Image string `json:"image,omitempty"`

	// ServerAddress specifies the ArgoCD server address for the agent. Default is empty.
	ServerAddress string `json:"serverAddress,omitempty"`

	// ServerPort specifies the ArgoCD server port for the agent. Default is empty.
	ServerPort string `json:"serverPort,omitempty"`

	// Mode specifies the ArgoCD agent mode. Default is empty.
	Mode string `json:"mode,omitempty"`
}

// GitOpsClusterStatus defines the observed state of GitOpsCluster.
type GitOpsClusterStatus struct {
	// LastUpdateTime provides the last updated timestamp of the gitOpsCluster status
	LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty"`

	// Message provides the detailed message of the GitOpsCluster status.
	Message string `json:"message,omitempty"`

	// Phase provides the overall phase of the GitOpsCluster status. Valid values include failed or successful.
	// This field is kept for backward compatibility. For detailed status information, use the Conditions field.
	Phase string `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the GitOpsCluster's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true

// GitOpsClusterList providess a list of GitOpsClusters.
type GitOpsClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitOpsCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitOpsCluster{}, &GitOpsClusterList{})
}

// SetCondition adds or updates a condition in the GitOpsCluster status.
// If a condition of the same type already exists, it will be updated.
func (g *GitOpsCluster) SetCondition(conditionType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	// Find existing condition of the same type
	for i, existingCondition := range g.Status.Conditions {
		if existingCondition.Type == conditionType {
			// Only update LastTransitionTime if status changed
			if existingCondition.Status != status {
				g.Status.Conditions[i] = condition
			} else {
				// Status didn't change, keep the original LastTransitionTime but update reason/message
				g.Status.Conditions[i].Reason = reason
				g.Status.Conditions[i].Message = message
			}
			return
		}
	}

	// Condition not found, add new one
	g.Status.Conditions = append(g.Status.Conditions, condition)
}

// GetCondition returns the condition of the specified type.
func (g *GitOpsCluster) GetCondition(conditionType string) *metav1.Condition {
	for _, condition := range g.Status.Conditions {
		if condition.Type == conditionType {
			return &condition
		}
	}
	return nil
}

// IsConditionTrue returns true if the condition is set to True.
func (g *GitOpsCluster) IsConditionTrue(conditionType string) bool {
	condition := g.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

// IsConditionFalse returns true if the condition is set to False.
func (g *GitOpsCluster) IsConditionFalse(conditionType string) bool {
	condition := g.GetCondition(conditionType)
	return condition != nil && condition.Status == metav1.ConditionFalse
}
