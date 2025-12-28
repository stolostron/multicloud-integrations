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
			Namespace: "test-ns",
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
	assert.Contains(t, yamlString, "namespace: test-ns")
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
			Namespace: "test-ns",
			UID:       "test-uid",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			PlacementRef: &v1.ObjectReference{
				Name: "test-placement",
				Kind: "Placement",
			},
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled:     &enabled,
				GitOpsImage: "quay.io/argoproj/argocd:v2.10.0",
				RedisImage:  "redis:7.0",
			},
		},
	}

	yamlString := generateArgoCDPolicyYaml(gitOpsCluster)

	assert.Contains(t, yamlString, "name: test-gitops-argocd-policy")
	assert.Contains(t, yamlString, "namespace: test-ns")
	assert.Contains(t, yamlString, "kind: Policy")
	assert.Contains(t, yamlString, "kind: ConfigurationPolicy")
	assert.Contains(t, yamlString, "name: test-gitops-argocd-config-policy")
	assert.Contains(t, yamlString, "kind: ArgoCD")
	assert.Contains(t, yamlString, "name: openshift-gitops")
	assert.Contains(t, yamlString, "remediationAction: enforce")
	assert.Contains(t, yamlString, "pruneObjectBehavior: DeleteIfCreated")
}

func TestGenerateArgoCDSpec_BasicConfig(t *testing.T) {
	enabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-ns",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	assert.Contains(t, spec, "controller:")
	assert.Contains(t, spec, "enabled: true")
	assert.Contains(t, spec, "redis:")
	assert.Contains(t, spec, "repo:")
	assert.Contains(t, spec, "rbac:")
	assert.Contains(t, spec, "defaultPolicy: \"role:admin\"")
	assert.Contains(t, spec, "applicationSet:")
	assert.Contains(t, spec, "enabled: false")
	// Should have all-namespaces env by default
	assert.Contains(t, spec, "ARGOCD_APPLICATION_NAMESPACES")
}

func TestGenerateArgoCDSpec_WithImages(t *testing.T) {
	enabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-ns",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled:     &enabled,
				GitOpsImage: "quay.io/argoproj/argocd:v2.10.0",
				RedisImage:  "redis:7.0",
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	assert.Contains(t, spec, "image: quay.io/argoproj/argocd")
	assert.Contains(t, spec, "version: v2.10.0")
}

func TestGenerateArgoCDSpec_WithDigestImage(t *testing.T) {
	enabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-ns",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled:     &enabled,
				GitOpsImage: "quay.io/argoproj/argocd@sha256:abc123def456",
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	assert.Contains(t, spec, "image: quay.io/argoproj/argocd")
	assert.Contains(t, spec, "version: sha256:abc123def456")
}

func TestGenerateArgoCDSpec_WithArgoCDAgent(t *testing.T) {
	enabled := true
	agentEnabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-ns",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled: &enabled,
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					Image:         "ghcr.io/argoproj-labs/argocd-agent:latest",
					ServerAddress: "192.168.1.100",
					ServerPort:    "443",
					Mode:          "managed",
				},
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	assert.Contains(t, spec, "argoCDAgent:")
	assert.Contains(t, spec, "agent:")
	assert.Contains(t, spec, "image: ghcr.io/argoproj-labs/argocd-agent:latest")
	assert.Contains(t, spec, "principalServerAddress: \"192.168.1.100\"")
	assert.Contains(t, spec, "principalServerPort: \"443\"")
	assert.Contains(t, spec, "mode: \"managed\"")
	assert.Contains(t, spec, "argocd-agent-client-tls")
	assert.Contains(t, spec, "argocd-agent-ca")
}

func TestGenerateArgoCDSpec_SingleNamespace(t *testing.T) {
	enabled := true
	gitOpsCluster := gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-ns",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				Enabled:        &enabled,
				ReconcileScope: "Single-Namespace",
			},
		},
	}

	spec := generateArgoCDSpec(gitOpsCluster)

	// Should NOT have ARGOCD_APPLICATION_NAMESPACES for single-namespace
	assert.NotContains(t, spec, "ARGOCD_APPLICATION_NAMESPACES")
}

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		name        string
		imageRef    string
		expectedImg string
		expectedTag string
	}{
		{
			name:        "image with tag",
			imageRef:    "quay.io/argoproj/argocd:v2.10.0",
			expectedImg: "quay.io/argoproj/argocd",
			expectedTag: "v2.10.0",
		},
		{
			name:        "image with digest",
			imageRef:    "quay.io/argoproj/argocd@sha256:abc123",
			expectedImg: "quay.io/argoproj/argocd",
			expectedTag: "sha256:abc123",
		},
		{
			name:        "image without tag or digest",
			imageRef:    "quay.io/argoproj/argocd",
			expectedImg: "quay.io/argoproj/argocd",
			expectedTag: "latest",
		},
		{
			name:        "image with port and tag",
			imageRef:    "localhost:5000/argocd:v1.0",
			expectedImg: "localhost",
			expectedTag: "5000/argocd:v1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, tag := parseImageRef(tt.imageRef)
			assert.Equal(t, tt.expectedImg, img)
			assert.Equal(t, tt.expectedTag, tag)
		})
	}
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
			Namespace: "test-ns",
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
			Namespace: "test-ns",
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
