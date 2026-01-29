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
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestGenerateManagedClusterSetBindingYaml(t *testing.T) {
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "openshift-gitops",
		},
	}

	yamlString := generateManagedClusterSetBindingYaml(gitOpsCluster)

	assert.Contains(t, yamlString, "name: default")
	assert.Contains(t, yamlString, "namespace: openshift-gitops")
	assert.Contains(t, yamlString, "kind: ManagedClusterSetBinding")
	assert.Contains(t, yamlString, "clusterSet: default")
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
	// ArgoCD CR should be orphaned when policy is deleted
	assert.Contains(t, yamlString, "pruneObjectBehavior: None")
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
	assert.Contains(t, yamlString, "namespace: openshift-gitops")

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

	// Policy should NOT include AppProject when argocd-agent is enabled
	// because the agent propagates AppProject from the hub
	assert.NotContains(t, yamlString, "kind: AppProject")

	// Should still contain the ArgoCD CR
	assert.Contains(t, yamlString, "kind: ArgoCD")
	assert.Contains(t, yamlString, "name: acm-openshift-gitops")

	// Should contain argocd-agent configuration
	assert.Contains(t, yamlString, "argoCDAgent:")
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

	// ArgoCD agent should be configured with default Red Hat image
	assert.Contains(t, spec, "argoCDAgent:")
	assert.Contains(t, spec, "agent:")
	assert.Contains(t, spec, "enabled: true")
	assert.Contains(t, spec, "image: \"registry.redhat.io")
	assert.Contains(t, spec, "principalServerAddress: \"192.168.1.100\"")
	assert.Contains(t, spec, "principalServerPort: \"443\"")
	// This test explicitly sets Mode: "managed", so it should output managed
	assert.Contains(t, spec, "mode: \"managed\"")
}

func TestGenerateArgoCDSpec_WithArgoCDAgentDefaultImage(t *testing.T) {
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
					// No custom image - should use default Red Hat image
				},
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// Should use default Red Hat agent image
	assert.Contains(t, spec, "argoCDAgent:")
	assert.Contains(t, spec, "image: \"registry.redhat.io/openshift-gitops-1/argocd-agent-rhel8")
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

func TestGenerateArgoCDSpec_WithImageOverrideEnv(t *testing.T) {
	// Set the environment variable to override the agent image
	customImage := "custom.registry.io/argocd-agent:test"
	os.Setenv("ARGOCD_AGENT_IMAGE_OVERRIDE", customImage)
	defer os.Unsetenv("ARGOCD_AGENT_IMAGE_OVERRIDE")

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

	// Should use the overridden image from environment variable
	assert.Contains(t, spec, "argoCDAgent:")
	assert.Contains(t, spec, fmt.Sprintf("image: \"%s\"", customImage))
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
