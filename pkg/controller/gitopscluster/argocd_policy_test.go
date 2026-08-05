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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGenerateArgoCDPolicyPlacementBindingYaml(t *testing.T) {
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
			UID:       "test-uid",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			PlacementRef: &v1.ObjectReference{
				Name: "test-placement",
				Kind: "Placement",
			},
		},
	}

	yamlString := generateArgoCDPolicyPlacementBindingYaml(gitOpsCluster)

	assert.Contains(t, yamlString, "name: test-gitops-argocd-policy-binding")
	assert.Contains(t, yamlString, "namespace: openshift-gitops")
	assert.Contains(t, yamlString, "kind: PlacementBinding")
	assert.Contains(t, yamlString, "name: test-placement")
	assert.Contains(t, yamlString, "name: test-gitops-argocd-policy")
	// No ownerReferences - Policy resources are not cleaned up with GitOpsCluster
	assert.NotContains(t, yamlString, "ownerReferences")
}

func TestGenerateArgoCDPolicyYaml(t *testing.T) {
	enabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
			UID:       "test-uid",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			PlacementRef: &v1.ObjectReference{
				Name: "test-placement",
				Kind: "Placement",
			},
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
			},
		},
	}

	yamlString := generateArgoCDPolicyYaml(gitOpsCluster)

	assert.Contains(t, yamlString, "name: test-gitops-argocd-policy")
	assert.Contains(t, yamlString, "namespace: openshift-gitops")
	assert.Contains(t, yamlString, "kind: Policy")
	assert.Contains(t, yamlString, "kind: ConfigurationPolicy")
	assert.Contains(t, yamlString, "name: test-gitops-argocd-config-policy")
	assert.Contains(t, yamlString, "kind: ArgoCD")
	assert.Contains(t, yamlString, "name: acm-openshift-gitops")
	assert.Contains(t, yamlString, "remediationAction: enforce")
	// ArgoCD CR should be orphaned when policy is deleted (cleanup job handles deletion)
	assert.Contains(t, yamlString, "pruneObjectBehavior: None")
	// The ArgoCD CR the Policy enforces on the MANAGED cluster must always target the fixed
	// utils.GitOpsNamespace ("openshift-gitops"), never the hub's own (possibly different)
	// effective ArgoCD namespace -- see TestGenerateArgoCDPolicyYaml_SpokeNamespaceIsAlwaysFixed
	// below for the case where those two actually differ.
	assert.Contains(t, yamlString, "namespace: 'openshift-gitops'",
		"the managed-cluster ArgoCD CR must always land in the standard openshift-gitops namespace")
	assert.NotContains(t, yamlString, "local-cluster",
		"the Policy must never target local-cluster - it already has its own ArgoCD instance and is not an addon-install target")
}

// TestGenerateArgoCDPolicyYaml_SpokeNamespaceIsAlwaysFixed reproduces the bug where a
// GitOpsCluster living in a custom hub namespace (e.g. "argocd-principal") caused the Policy to
// enforce the ArgoCD CR and default AppProject in that SAME namespace on the MANAGED cluster,
// where it doesn't exist -- the Policy went permanently NonCompliant with "namespaces
// '<hub-namespace>' not found". The managed-cluster namespace must always be the fixed
// utils.GitOpsNamespace regardless of where the hub's own GitOpsCluster/ArgoCD instance lives.
func TestGenerateArgoCDPolicyYaml_SpokeNamespaceIsAlwaysFixed(t *testing.T) {
	enabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "argocd-principal",
			UID:       "test-uid",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			PlacementRef: &v1.ObjectReference{
				Name: "test-placement",
				Kind: "Placement",
			},
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
			},
		},
	}

	yamlString := generateArgoCDPolicyYaml(gitOpsCluster)

	// The Policy/PlacementBinding/ConfigurationPolicy metadata correctly lives alongside the
	// GitOpsCluster CR on the hub, in its own custom namespace.
	assert.Contains(t, yamlString, "namespace: argocd-principal",
		"the Policy's own hub-side metadata must stay in the GitOpsCluster's namespace")

	// But the object-template contents -- the ArgoCD CR AND the default AppProject (agent is not
	// enabled in this fixture, so both are included) that get enforced ON THE MANAGED CLUSTER --
	// must always target the fixed spoke namespace, never the hub's.
	assert.Contains(t, yamlString, "kind: AppProject")
	assert.Equal(t, 2, strings.Count(yamlString, "namespace: 'openshift-gitops'"),
		"both the managed-cluster ArgoCD CR and AppProject must land in openshift-gitops even when the hub's own GitOpsCluster lives elsewhere")
	assert.NotContains(t, yamlString, "namespace: 'argocd-principal'",
		"the managed-cluster object-templates must never be targeted at the hub's own custom ArgoCD namespace")
}

func TestGenerateArgoCDPolicyYaml_IncludesDefaultAppProject(t *testing.T) {
	enabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
			UID:       "test-uid",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			PlacementRef: &v1.ObjectReference{
				Name: "test-placement",
				Kind: "Placement",
			},
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
			},
		},
	}

	yamlString := generateArgoCDPolicyYaml(gitOpsCluster)

	// Policy should include the default AppProject
	assert.Contains(t, yamlString, "kind: AppProject")
	assert.Contains(t, yamlString, "name: default")

	// AppProject namespace must always be the fixed spoke namespace too (same as the ArgoCD CR)
	assert.Contains(t, yamlString, "namespace: 'openshift-gitops'",
		"the managed-cluster AppProject must land in the standard openshift-gitops namespace")

	// AppProject should have permissive spec for default project
	assert.Contains(t, yamlString, "clusterResourceWhitelist:")
	assert.Contains(t, yamlString, "group: '*'")
	assert.Contains(t, yamlString, "kind: '*'")
	assert.Contains(t, yamlString, "destinations:")
	assert.Contains(t, yamlString, "namespace: '*'")
	assert.Contains(t, yamlString, "server: '*'")
	assert.Contains(t, yamlString, "sourceRepos:")
}

func TestGenerateArgoCDPolicyYaml_ExcludesAppProjectWhenAgentEnabled(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
			UID:       "test-uid",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			PlacementRef: &v1.ObjectReference{
				Name: "test-placement",
				Kind: "Placement",
			},
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "principal.example.com",
					ServerPort:    "443",
				},
			},
		},
	}

	yamlString := generateArgoCDPolicyYaml(gitOpsCluster)

	// AppProject should NOT be included when agent is enabled
	// (argocd-agent propagates it from the hub)
	assert.NotContains(t, yamlString, "kind: AppProject")

	// Should still contain the ArgoCD CR
	assert.Contains(t, yamlString, "kind: ArgoCD")
	assert.Contains(t, yamlString, "name: acm-openshift-gitops")

	// Should contain argocd-agent configuration
	assert.Contains(t, yamlString, "argoCDAgent:")
	assert.Contains(t, yamlString, "principalServerAddress")
}

func TestGenerateArgoCDPolicyYaml_IncludesAppProjectWhenAgentAutonomous(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
			UID:       "test-uid",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			PlacementRef: &v1.ObjectReference{
				Name: "test-placement",
				Kind: "Placement",
			},
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "principal.example.com",
					ServerPort:    "443",
					Mode:          "autonomous",
				},
			},
		},
	}

	yamlString := generateArgoCDPolicyYaml(gitOpsCluster)

	// AppProject SHOULD be included when agent is in autonomous mode
	// (autonomous agents reconcile Applications locally and need AppProject)
	assert.Contains(t, yamlString, "kind: AppProject")
	assert.Contains(t, yamlString, "name: default")

	// Should still contain the ArgoCD CR with agent config
	assert.Contains(t, yamlString, "kind: ArgoCD")
	assert.Contains(t, yamlString, "mode: \"autonomous\"")
	assert.Contains(t, yamlString, "principalServerAddress")
}

func TestGenerateArgoCDSpec_BasicConfig(t *testing.T) {
	enabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// Server should be disabled
	assert.Contains(t, spec, "server:")
	assert.Contains(t, spec, "enabled: false")

	// Should NOT contain any image overrides - operator handles images via env vars
	assert.NotContains(t, spec, "image:")
	assert.NotContains(t, spec, "version:")
	assert.NotContains(t, spec, "redis:")
	assert.NotContains(t, spec, "repo:")

	// Should NOT contain argoCDAgent when not enabled
	assert.NotContains(t, spec, "argoCDAgent:")
}

func TestGenerateArgoCDSpec_WithArgoCDAgent(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "192.168.1.100",
					ServerPort:    "443",
					Mode:          "managed",
				},
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// Server should be disabled
	assert.Contains(t, spec, "server:")
	assert.Contains(t, spec, "enabled: false")

	// ArgoCD agent should be configured (no image override - operator handles it via env var)
	assert.Contains(t, spec, "argoCDAgent:")
	assert.Contains(t, spec, "agent:")
	assert.Contains(t, spec, "enabled: true")
	assert.NotContains(t, spec, "image:")
	assert.Contains(t, spec, "principalServerAddress: \"192.168.1.100\"")
	assert.Contains(t, spec, "principalServerPort: \"443\"")
	assert.Contains(t, spec, "mode: \"managed\"")
	assert.Contains(t, spec, "allowedNamespaces:")
	assert.Contains(t, spec, "destinationBasedMapping:")
	assert.Contains(t, spec, "enabled: true")
	assert.NotContains(t, spec, "sourceNamespaces:")
}

func TestGenerateArgoCDSpec_WithArgoCDAgentNoImageOverride(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "192.168.1.100",
				},
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// No image override - operator handles agent image via ARGOCD_AGENT_IMAGE env var
	assert.Contains(t, spec, "argoCDAgent:")
	assert.NotContains(t, spec, "image:")
	assert.Contains(t, spec, "allowedNamespaces:")
	assert.Contains(t, spec, "destinationBasedMapping:")
	assert.Contains(t, spec, "enabled: true")
}

func TestGenerateArgoCDSpec_WithArgoCDAgentDefaults(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "192.168.1.100",
					// ServerPort and Mode not specified - should use defaults
				},
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// Should use default port 443 and mode managed
	assert.Contains(t, spec, "principalServerPort: \"443\"")
	assert.Contains(t, spec, "mode: \"managed\"")
}

func TestGenerateArgoCDSpec_NoAgentImageForAnyOperator(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "192.168.1.100",
					ServerPort:    "443",
				},
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// Agent image is no longer set in the CR - operator handles it via ARGOCD_AGENT_IMAGE env var
	assert.Contains(t, spec, "argoCDAgent:")
	assert.Contains(t, spec, "enabled: true")
	assert.NotContains(t, spec, "image:")
	assert.Contains(t, spec, "principalServerAddress: \"192.168.1.100\"")
	assert.Contains(t, spec, "allowedNamespaces:")
	assert.Contains(t, spec, "destinationBasedMapping:")
}

func TestGenerateArgoCDSpec_AutonomousModeDisablesDestinationBasedMapping(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "192.168.1.100",
					ServerPort:    "443",
					Mode:          "autonomous",
				},
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// destinationBasedMapping must be disabled from the very first generation for autonomous
	// mode - the argocd-agent binary fatally refuses to start with it enabled in that mode, and
	// autonomous agents never participate in the principal-driven destination-name dispatch this
	// setting exists for.
	require.Contains(t, spec, "destinationBasedMapping:")
	dbmIdx := strings.Index(spec, "destinationBasedMapping:")
	afterDBM := spec[dbmIdx:]
	assert.Contains(t, afterDBM, "enabled: false")
	assert.Contains(t, spec, "mode: \"autonomous\"")
}

func TestGenerateArgoCDSpec_ManagedModeEnablesDestinationBasedMapping(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "192.168.1.100",
					ServerPort:    "443",
					Mode:          "managed",
				},
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// Managed-mode agents must match the principal's destinationBasedMapping setting (enabled),
	// which the principal relies on for destination-name based dispatch/Redis key scheme.
	require.Contains(t, spec, "destinationBasedMapping:")
	dbmIdx := strings.Index(spec, "destinationBasedMapping:")
	afterDBM := spec[dbmIdx:]
	assert.Contains(t, afterDBM, "enabled: true")
}

func TestIndentYaml(t *testing.T) {
	yaml := `key: value
nested:
  key: nestedValue`

	indented := indentYaml(yaml, 4)

	assert.Contains(t, indented, "    key: value")
	assert.Contains(t, indented, "    nested:")
	assert.Contains(t, indented, "      key: nestedValue")
}

func TestCreateArgoCDPolicy_AddonDisabled(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	disabled := false
	gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &disabled,
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: client,
		scheme: scheme,
	}

	err := reconciler.CreateArgoCDPolicy(gitOpsCluster)
	assert.NoError(t, err, "Should return nil when addon is disabled")
}

func TestCreateArgoCDPolicy_NilPlacementRef(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	enabled := true
	gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
			},
			PlacementRef: nil,
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: client,
		scheme: scheme,
	}

	err := reconciler.CreateArgoCDPolicy(gitOpsCluster)
	assert.NoError(t, err, "Should return nil when PlacementRef is nil")
}
