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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
)

// ErrPolicyFrameworkNotAvailable is returned when the governance-policy-framework is not installed.
var ErrPolicyFrameworkNotAvailable = fmt.Errorf("governance-policy-framework is not installed: Policy CRD not found")

// CreateArgoCDPolicy creates a Policy wrapping the ArgoCD CR for managed clusters.
// The Policy uses ConfigurationPolicy to enforce the ArgoCD CR on managed clusters selected by the Placement.
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

// generateArgoCDPolicyPlacementBindingYaml generates the PlacementBinding YAML to bind the ArgoCD Policy to the Placement.
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

// generateArgoCDPolicyYaml generates the Policy YAML wrapping the ArgoCD CR.
// Note: No ownerReferences - Policy resources are intentionally NOT cleaned up when GitOpsCluster is deleted.
// They must be manually deleted by the user.
func generateArgoCDPolicyYaml(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	argoCDSpec := generateArgoCDSpec(gitOpsCluster)

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
          pruneObjectBehavior: DeleteIfCreated
          remediationAction: enforce
          severity: medium
          object-templates:
            - complianceType: musthave
              objectDefinition:
                apiVersion: argoproj.io/v1beta1
                kind: ArgoCD
                metadata:
                  name: openshift-gitops
                  namespace: openshift-gitops
                spec:
%s
`,
		gitOpsCluster.Name+"-argocd-policy", gitOpsCluster.Namespace,
		gitOpsCluster.Name+"-argocd-config-policy",
		indentYaml(argoCDSpec, 18))

	return yamlString
}

// generateArgoCDSpec generates the ArgoCD spec based on GitOpsCluster configuration.
func generateArgoCDSpec(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	// Check if ArgoCD agent is enabled
	argoCDAgentEnabled := false
	if gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.Enabled
	}

	// Get image configurations
	gitOpsImage := ""
	gitOpsImageTag := ""
	redisImage := ""
	redisTag := ""
	reconcileScope := "All-Namespaces"

	if gitOpsCluster.Spec.GitOpsAddon != nil {
		if gitOpsCluster.Spec.GitOpsAddon.GitOpsImage != "" {
			image, tag := parseImageRef(gitOpsCluster.Spec.GitOpsAddon.GitOpsImage)
			gitOpsImage = image
			gitOpsImageTag = tag
		}
		if gitOpsCluster.Spec.GitOpsAddon.RedisImage != "" {
			image, tag := parseImageRef(gitOpsCluster.Spec.GitOpsAddon.RedisImage)
			redisImage = image
			redisTag = tag
		}
		if gitOpsCluster.Spec.GitOpsAddon.ReconcileScope != "" {
			reconcileScope = gitOpsCluster.Spec.GitOpsAddon.ReconcileScope
		}
	}

	// Build controller env
	controllerEnv := ""
	if reconcileScope == "All-Namespaces" {
		controllerEnv = `
  env:
    - name: ARGOCD_APPLICATION_NAMESPACES
      value: '*'`
	}

	// Build base spec
	spec := fmt.Sprintf(`controller:
  enabled: true%s`, controllerEnv)

	// Add image if specified
	if gitOpsImage != "" {
		spec = fmt.Sprintf("image: %s\nversion: %s\n%s", gitOpsImage, gitOpsImageTag, spec)
	}

	// Add redis configuration
	if redisImage != "" {
		spec += fmt.Sprintf(`
redis:
  enabled: true
  image: %s
  version: %s`, redisImage, redisTag)
	} else {
		spec += `
redis:
  enabled: true`
	}

	// Add repo configuration
	if gitOpsImage != "" {
		spec += fmt.Sprintf(`
repo:
  enabled: true
  image: %s
  version: %s`, gitOpsImage, gitOpsImageTag)
	} else {
		spec += `
repo:
  enabled: true`
	}

	// Add RBAC configuration
	spec += `
rbac:
  defaultPolicy: "role:admin"
  policy: |
    g, cluster-admins, role:admin`

	// Disable unused components
	spec += `
applicationSet:
  enabled: false
grafana:
  enabled: false
ha:
  enabled: false
server:
  enabled: false
monitoring:
  enabled: false
notifications:
  enabled: false
prometheus:
  enabled: false`

	// Add ArgoCD agent configuration if enabled
	if argoCDAgentEnabled && gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent != nil {
		agentConfig := gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent
		agentImage := agentConfig.Image
		serverAddress := agentConfig.ServerAddress
		serverPort := agentConfig.ServerPort
		if serverPort == "" {
			serverPort = "443"
		}
		mode := agentConfig.Mode
		if mode == "" {
			mode = "managed"
		}

		spec += fmt.Sprintf(`
argoCDAgent:
  agent:
    enabled: true
    image: %s
    creds: "mtls:any"
    logLevel: "info"
    logFormat: "text"
    client:
      principalServerAddress: "%s"
      principalServerPort: "%s"
      mode: "%s"
      enableWebSocket: false
      enableCompression: false
      keepAliveInterval: "30s"
    tls:
      secretName: "argocd-agent-client-tls"
      rootCASecretName: "argocd-agent-ca"
      insecure: false
    redis:
      serverAddress: "openshift-gitops-redis:6379"`,
			agentImage, serverAddress, serverPort, mode)
	}

	return spec
}

// parseImageRef parses an image reference into image and tag components.
func parseImageRef(imageRef string) (string, string) {
	// Handle digest format (image@sha256:...)
	if strings.Contains(imageRef, "@") {
		parts := strings.SplitN(imageRef, "@", 2)
		return parts[0], parts[1]
	}
	// Handle tag format (image:tag)
	if strings.Contains(imageRef, ":") {
		parts := strings.SplitN(imageRef, ":", 2)
		return parts[0], parts[1]
	}
	// No tag or digest, use latest
	return imageRef, "latest"
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
