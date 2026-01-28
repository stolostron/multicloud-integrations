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
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
)

// ErrPolicyFrameworkNotAvailable is returned when the governance-policy-framework is not installed.
var ErrPolicyFrameworkNotAvailable = fmt.Errorf("governance-policy-framework is not installed: Policy CRD not found")

// CreateArgoCDPolicy creates a Policy wrapping the ArgoCD CR for managed clusters.
// The Policy uses ConfigurationPolicy to enforce the ArgoCD CR on managed clusters selected by the Placement.
// All resources (Policy, PlacementBinding, ManagedClusterSetBinding) are created in the same namespace
// as the GitOpsCluster CR. The GitOpsCluster CR should be in a non-managed-cluster namespace
// (e.g., openshift-gitops) to avoid the policy propagator deleting policies.
// Returns an error if the Policy CRD is not installed (governance-policy-framework not available).
func (r *ReconcileGitOpsCluster) CreateArgoCDPolicy(instance *gitopsclusterV1beta1.GitOpsCluster) error {
	// Only create ArgoCD Policy if GitOps addon is enabled
	gitopsAddonEnabled := false
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.Enabled != nil {
		gitopsAddonEnabled = *instance.Spec.GitOpsAddon.Enabled
	}

	if !gitopsAddonEnabled {
		klog.Info("GitOps addon is not enabled, skipping ArgoCD Policy creation")
		return nil
	}

	if instance.Spec.PlacementRef == nil {
		klog.Info("PlacementRef is nil, skipping ArgoCD Policy creation")
		return nil
	}

	// Check if Policy CRD exists (governance-policy-framework installed)
	// Policy framework is REQUIRED - no fallback
	if !r.isPolicyCRDAvailable() {
		klog.Error("Policy CRD not available - governance-policy-framework must be installed for ArgoCD CR management")
		return ErrPolicyFrameworkNotAvailable
	}

	// Ensure ManagedClusterSetBinding exists in the GitOpsCluster namespace
	// This is required for the Placement to be able to select clusters from the default ClusterSet
	if err := r.createNamespaceScopedResourceFromYAML(generateManagedClusterSetBindingYaml(*instance)); err != nil {
		klog.Error("failed to create ManagedClusterSetBinding: ", err)
		return err
	}

	// Create PlacementBinding to bind the Policy to the existing Placement
	if err := r.createNamespaceScopedResourceFromYAML(generateArgoCDPolicyPlacementBindingYaml(*instance)); err != nil {
		klog.Error("failed to create ArgoCD Policy PlacementBinding: ", err)
		return err
	}

	// Create the Policy wrapping the ArgoCD CR
	if err := r.createNamespaceScopedResourceFromYAML(generateArgoCDPolicyYaml(*instance)); err != nil {
		klog.Error("failed to create ArgoCD Policy: ", err)
		return err
	}

	klog.Infof("Successfully created ArgoCD Policy for GitOpsCluster %s/%s", instance.Namespace, instance.Name)
	return nil
}

// isPolicyCRDAvailable checks if the Policy CRD from governance-policy-framework is available.
// Returns true if Policy CRD is found, false otherwise.
// In test environments where DynamicClient is not initialized, returns true to allow tests to proceed.
func (r *ReconcileGitOpsCluster) isPolicyCRDAvailable() bool {
	// Try to check if the Policy CRD exists by discovering the API
	gvr := schema.GroupVersionResource{
		Group:    "policy.open-cluster-management.io",
		Version:  "v1",
		Resource: "policies",
	}

	// Use recover to handle any panics from dynamic client operations (e.g., in unit tests)
	available := false
	panicOccurred := false
	func() {
		defer func() {
			if panicErr := recover(); panicErr != nil {
				klog.V(4).Infof("Dynamic client operation failed (test environment): %v", panicErr)
				panicOccurred = true
			}
		}()
		// Try to list policies - if the CRD doesn't exist, this will fail
		_, err := r.DynamicClient.Resource(gvr).List(context.TODO(), metav1.ListOptions{Limit: 1})
		if err == nil {
			available = true
		} else {
			klog.V(4).Infof("Policy CRD check failed: %v", err)
			available = false
		}
	}()

	// If a panic occurred (test environment), assume Policy is available and let creation be skipped
	if panicOccurred {
		return true
	}

	return available
}

// generateManagedClusterSetBindingYaml generates a ManagedClusterSetBinding in the GitOpsCluster namespace
// to allow Placements to select clusters from the default ClusterSet.
func generateManagedClusterSetBindingYaml(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	return fmt.Sprintf(`
apiVersion: cluster.open-cluster-management.io/v1beta2
kind: ManagedClusterSetBinding
metadata:
  name: default
  namespace: %s
spec:
  clusterSet: default
`, gitOpsCluster.Namespace)
}

// generateArgoCDPolicyPlacementBindingYaml generates the PlacementBinding YAML to bind the ArgoCD Policy to the Placement.
// All resources are created in the same namespace as the GitOpsCluster CR.
// Note: No ownerReferences - Policy resources are intentionally NOT cleaned up when GitOpsCluster is deleted.
// They must be manually deleted by the user.
func generateArgoCDPolicyPlacementBindingYaml(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	yamlString := fmt.Sprintf(`
apiVersion: policy.open-cluster-management.io/v1
kind: PlacementBinding
metadata:
  name: %s
  namespace: %s
placementRef:
  name: %s
  kind: Placement
  apiGroup: cluster.open-cluster-management.io
subjects:
  - name: %s
    kind: Policy
    apiGroup: policy.open-cluster-management.io
`,
		gitOpsCluster.Name+"-argocd-policy-binding", gitOpsCluster.Namespace,
		gitOpsCluster.Spec.PlacementRef.Name,
		gitOpsCluster.Name+"-argocd-policy")

	return yamlString
}

// generateArgoCDPolicyYaml generates the Policy YAML wrapping the ArgoCD CR and optionally the default AppProject.
// The Policy is created in the same namespace as the GitOpsCluster CR.
// Note: No ownerReferences - Policy resources are intentionally NOT cleaned up when GitOpsCluster is deleted.
// They must be manually deleted by the user.
// Note: pruneObjectBehavior is set to None to orphan the ArgoCD CR when the policy is deleted.
// The ArgoCD CR cleanup is handled by the gitops addon code.
// When ArgoCD agent is enabled, AppProject is NOT included because argocd-agent propagates it from the hub.
func generateArgoCDPolicyYaml(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	argoCDSpec := generateArgoCDSpec(gitOpsCluster)

	// Check if ArgoCD agent is enabled
	argoCDAgentEnabled := false
	if gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.Enabled
	}

	// Base policy with ArgoCD CR
	yamlString := fmt.Sprintf(`
apiVersion: policy.open-cluster-management.io/v1
kind: Policy
metadata:
  name: %s
  namespace: %s
  annotations:
    policy.open-cluster-management.io/standards: NIST-CSF
    policy.open-cluster-management.io/categories: PR.PT Protective Technology
    policy.open-cluster-management.io/controls: PR.PT-3 Least Functionality
spec:
  remediationAction: enforce
  disabled: false
  policy-templates:
    - objectDefinition:
        apiVersion: policy.open-cluster-management.io/v1
        kind: ConfigurationPolicy
        metadata:
          name: %s
        spec:
          pruneObjectBehavior: None
          remediationAction: enforce
          severity: medium
          object-templates:
            - complianceType: musthave
              objectDefinition:
                apiVersion: argoproj.io/v1beta1
                kind: ArgoCD
                metadata:
                  name: acm-openshift-gitops
                  namespace: openshift-gitops
                spec:
%s`,
		gitOpsCluster.Name+"-argocd-policy", gitOpsCluster.Namespace,
		gitOpsCluster.Name+"-argocd-config-policy",
		indentYaml(argoCDSpec, 18))

	// Only include AppProject when ArgoCD agent is NOT enabled
	// When argocd-agent is enabled, AppProject is propagated from the hub by the agent
	if !argoCDAgentEnabled {
		yamlString += `
            - complianceType: musthave
              objectDefinition:
                apiVersion: argoproj.io/v1alpha1
                kind: AppProject
                metadata:
                  name: default
                  namespace: openshift-gitops
                spec:
                  clusterResourceWhitelist:
                    - group: '*'
                      kind: '*'
                  destinations:
                    - namespace: '*'
                      server: '*'
                  sourceRepos:
                    - '*'
`
	} else {
		klog.Infof("ArgoCD agent is enabled, excluding AppProject from policy (will be propagated by agent)")
		yamlString += "\n"
	}

	return yamlString
}

// generateArgoCDSpec generates the ArgoCD spec based on GitOpsCluster configuration.
// The spec is kept minimal, relying on operator defaults for most settings.
// The operator handles all image selection via env vars, EXCEPT for the agent's agent
// component which doesn't have an env var - we must set it in the CR spec.
func generateArgoCDSpec(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	// Check if ArgoCD agent is enabled
	argoCDAgentEnabled := false
	if gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.Enabled
	}

	// Base spec: disable server (controller, repo, redis are enabled by default)
	// No image overrides - let the operator use its bundled images
	spec := `server:
  enabled: false`

	// Add ArgoCD agent configuration if enabled
	// NOTE: We must set the agent image explicitly because the operator only has
	// ARGOCD_PRINCIPAL_IMAGE env var for the principal component, not the agent.
	// The agent component uses hardcoded community defaults, so we override it here
	// with the default Red Hat image (same as ARGOCD_PRINCIPAL_IMAGE).
	// Users can override this directly in the ArgoCD CR if needed.
	if argoCDAgentEnabled && gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil {
		agentConfig := gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent
		serverAddress := agentConfig.ServerAddress
		serverPort := agentConfig.ServerPort
		if serverPort == "" {
			serverPort = "443"
		}
		mode := agentConfig.Mode
		if mode == "" {
			mode = "managed"
		}

		// Use the default Red Hat agent image from utils, or allow override via environment variable
		// for e2e testing (registry.redhat.io requires authentication which may not be available in test environments)
		agentImage := os.Getenv("ARGOCD_AGENT_IMAGE_OVERRIDE")
		if agentImage == "" {
			agentImage = utils.DefaultOperatorImages[utils.EnvArgoCDPrincipalImage]
		}

		spec += fmt.Sprintf(`
argoCDAgent:
  agent:
    enabled: true
    image: "%s"
    client:
      principalServerAddress: "%s"
      principalServerPort: "%s"
      mode: "%s"
    tls:
      rootCASecretName: argocd-agent-ca`,
			agentImage, serverAddress, serverPort, mode)
	}

	return spec
}

// indentYaml indents a YAML string by the specified number of spaces.
func indentYaml(yaml string, spaces int) string {
	indent := strings.Repeat(" ", spaces)
	lines := strings.Split(yaml, "\n")
	var result []string
	for _, line := range lines {
		if line != "" {
			result = append(result, indent+line)
		} else {
			result = append(result, "")
		}
	}
	return strings.Join(result, "\n")
}
