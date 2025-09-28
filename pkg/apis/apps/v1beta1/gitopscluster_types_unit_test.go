package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGitOpsCluster_SetCondition(t *testing.T) {
	testCases := []struct {
		name               string
		existingConditions []metav1.Condition
		conditionType      string
		status             metav1.ConditionStatus
		reason             string
		message            string
		expectedConditions int
		expectedStatus     metav1.ConditionStatus
		expectedReason     string
		expectedMessage    string
	}{
		{
			name:               "Add new condition",
			existingConditions: []metav1.Condition{},
			conditionType:      GitOpsClusterReady,
			status:             metav1.ConditionTrue,
			reason:             ReasonSuccess,
			message:            "GitOps cluster is ready",
			expectedConditions: 1,
			expectedStatus:     metav1.ConditionTrue,
			expectedReason:     ReasonSuccess,
			expectedMessage:    "GitOps cluster is ready",
		},
		{
			name: "Update existing condition with different status",
			existingConditions: []metav1.Condition{
				{
					Type:    GitOpsClusterReady,
					Status:  metav1.ConditionFalse,
					Reason:  ReasonInvalidConfiguration,
					Message: "Configuration is invalid",
				},
			},
			conditionType:      GitOpsClusterReady,
			status:             metav1.ConditionTrue,
			reason:             ReasonSuccess,
			message:            "GitOps cluster is now ready",
			expectedConditions: 1,
			expectedStatus:     metav1.ConditionTrue,
			expectedReason:     ReasonSuccess,
			expectedMessage:    "GitOps cluster is now ready",
		},
		{
			name: "Update existing condition with same status",
			existingConditions: []metav1.Condition{
				{
					Type:               GitOpsClusterReady,
					Status:             metav1.ConditionTrue,
					Reason:             ReasonSuccess,
					Message:            "Original message",
					LastTransitionTime: metav1.Now(),
				},
			},
			conditionType:      GitOpsClusterReady,
			status:             metav1.ConditionTrue,
			reason:             ReasonSuccess,
			message:            "Updated message",
			expectedConditions: 1,
			expectedStatus:     metav1.ConditionTrue,
			expectedReason:     ReasonSuccess,
			expectedMessage:    "Updated message",
		},
		{
			name: "Add condition to existing list",
			existingConditions: []metav1.Condition{
				{
					Type:    GitOpsClusterPlacementResolved,
					Status:  metav1.ConditionTrue,
					Reason:  ReasonSuccess,
					Message: "Placement resolved",
				},
			},
			conditionType:      GitOpsClusterClustersRegistered,
			status:             metav1.ConditionFalse,
			reason:             ReasonClusterRegistrationFailed,
			message:            "Failed to register clusters",
			expectedConditions: 2,
			expectedStatus:     metav1.ConditionFalse,
			expectedReason:     ReasonClusterRegistrationFailed,
			expectedMessage:    "Failed to register clusters",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gitopsCluster := &GitOpsCluster{
				Status: GitOpsClusterStatus{
					Conditions: tc.existingConditions,
				},
			}

			gitopsCluster.SetCondition(tc.conditionType, tc.status, tc.reason, tc.message)

			assert.Len(t, gitopsCluster.Status.Conditions, tc.expectedConditions)

			// Find the condition we just set/updated
			var foundCondition *metav1.Condition
			for i := range gitopsCluster.Status.Conditions {
				if gitopsCluster.Status.Conditions[i].Type == tc.conditionType {
					foundCondition = &gitopsCluster.Status.Conditions[i]
					break
				}
			}

			require.NotNil(t, foundCondition)
			assert.Equal(t, tc.expectedStatus, foundCondition.Status)
			assert.Equal(t, tc.expectedReason, foundCondition.Reason)
			assert.Equal(t, tc.expectedMessage, foundCondition.Message)
			assert.NotZero(t, foundCondition.LastTransitionTime)
		})
	}
}

func TestGitOpsCluster_GetCondition(t *testing.T) {
	testCases := []struct {
		name              string
		conditions        []metav1.Condition
		conditionType     string
		expectedCondition *metav1.Condition
	}{
		{
			name:              "Condition not found",
			conditions:        []metav1.Condition{},
			conditionType:     GitOpsClusterReady,
			expectedCondition: nil,
		},
		{
			name: "Condition found",
			conditions: []metav1.Condition{
				{
					Type:    GitOpsClusterReady,
					Status:  metav1.ConditionTrue,
					Reason:  ReasonSuccess,
					Message: "GitOps cluster is ready",
				},
				{
					Type:    GitOpsClusterPlacementResolved,
					Status:  metav1.ConditionFalse,
					Reason:  ReasonPlacementNotFound,
					Message: "Placement not found",
				},
			},
			conditionType: GitOpsClusterPlacementResolved,
			expectedCondition: &metav1.Condition{
				Type:    GitOpsClusterPlacementResolved,
				Status:  metav1.ConditionFalse,
				Reason:  ReasonPlacementNotFound,
				Message: "Placement not found",
			},
		},
		{
			name: "Multiple conditions, find specific one",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   GitOpsClusterArgoCDAgentPrereqsReady,
					Status: metav1.ConditionFalse,
				},
				{
					Type:   GitOpsClusterClustersRegistered,
					Status: metav1.ConditionTrue,
				},
			},
			conditionType: GitOpsClusterArgoCDAgentPrereqsReady,
			expectedCondition: &metav1.Condition{
				Type:   GitOpsClusterArgoCDAgentPrereqsReady,
				Status: metav1.ConditionFalse,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gitopsCluster := &GitOpsCluster{
				Status: GitOpsClusterStatus{
					Conditions: tc.conditions,
				},
			}

			condition := gitopsCluster.GetCondition(tc.conditionType)

			if tc.expectedCondition == nil {
				assert.Nil(t, condition)
			} else {
				require.NotNil(t, condition)
				assert.Equal(t, tc.expectedCondition.Type, condition.Type)
				assert.Equal(t, tc.expectedCondition.Status, condition.Status)
				assert.Equal(t, tc.expectedCondition.Reason, condition.Reason)
				assert.Equal(t, tc.expectedCondition.Message, condition.Message)
			}
		})
	}
}

func TestGitOpsCluster_IsConditionTrue(t *testing.T) {
	testCases := []struct {
		name           string
		conditions     []metav1.Condition
		conditionType  string
		expectedResult bool
	}{
		{
			name:           "Condition not found",
			conditions:     []metav1.Condition{},
			conditionType:  GitOpsClusterReady,
			expectedResult: false,
		},
		{
			name: "Condition is True",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionTrue,
				},
			},
			conditionType:  GitOpsClusterReady,
			expectedResult: true,
		},
		{
			name: "Condition is False",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionFalse,
				},
			},
			conditionType:  GitOpsClusterReady,
			expectedResult: false,
		},
		{
			name: "Condition is Unknown",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionUnknown,
				},
			},
			conditionType:  GitOpsClusterReady,
			expectedResult: false,
		},
		{
			name: "Multiple conditions, check specific one",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionFalse,
				},
				{
					Type:   GitOpsClusterPlacementResolved,
					Status: metav1.ConditionTrue,
				},
			},
			conditionType:  GitOpsClusterPlacementResolved,
			expectedResult: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gitopsCluster := &GitOpsCluster{
				Status: GitOpsClusterStatus{
					Conditions: tc.conditions,
				},
			}

			result := gitopsCluster.IsConditionTrue(tc.conditionType)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestGitOpsCluster_IsConditionFalse(t *testing.T) {
	testCases := []struct {
		name           string
		conditions     []metav1.Condition
		conditionType  string
		expectedResult bool
	}{
		{
			name:           "Condition not found",
			conditions:     []metav1.Condition{},
			conditionType:  GitOpsClusterReady,
			expectedResult: false,
		},
		{
			name: "Condition is True",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionTrue,
				},
			},
			conditionType:  GitOpsClusterReady,
			expectedResult: false,
		},
		{
			name: "Condition is False",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionFalse,
				},
			},
			conditionType:  GitOpsClusterReady,
			expectedResult: true,
		},
		{
			name: "Condition is Unknown",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionUnknown,
				},
			},
			conditionType:  GitOpsClusterReady,
			expectedResult: false,
		},
		{
			name: "Multiple conditions, check specific one",
			conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   GitOpsClusterArgoCDAgentPrereqsReady,
					Status: metav1.ConditionFalse,
				},
			},
			conditionType:  GitOpsClusterArgoCDAgentPrereqsReady,
			expectedResult: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gitopsCluster := &GitOpsCluster{
				Status: GitOpsClusterStatus{
					Conditions: tc.conditions,
				},
			}

			result := gitopsCluster.IsConditionFalse(tc.conditionType)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestGitOpsClusterConstants(t *testing.T) {
	// Test that constants are properly defined
	assert.Equal(t, "Ready", GitOpsClusterReady)
	assert.Equal(t, "PlacementResolved", GitOpsClusterPlacementResolved)
	assert.Equal(t, "ClustersRegistered", GitOpsClusterClustersRegistered)
	assert.Equal(t, "ArgoCDAgentPrereqsReady", GitOpsClusterArgoCDAgentPrereqsReady)
	assert.Equal(t, "CertificatesReady", GitOpsClusterCertificatesReady)
	assert.Equal(t, "ManifestWorksApplied", GitOpsClusterManifestWorksApplied)

	// Test reason constants
	assert.Equal(t, "Success", ReasonSuccess)
	assert.Equal(t, "Disabled", ReasonDisabled)
	assert.Equal(t, "NotRequired", ReasonNotRequired)
	assert.Equal(t, "InvalidConfiguration", ReasonInvalidConfiguration)
	assert.Equal(t, "PlacementNotFound", ReasonPlacementNotFound)
	assert.Equal(t, "ManagedClustersNotFound", ReasonManagedClustersNotFound)
	assert.Equal(t, "ArgoServerNotFound", ReasonArgoServerNotFound)
	assert.Equal(t, "ClusterRegistrationFailed", ReasonClusterRegistrationFailed)
	assert.Equal(t, "CertificateSigningFailed", ReasonCertificateSigningFailed)
	assert.Equal(t, "ManifestWorkFailed", ReasonManifestWorkFailed)
	assert.Equal(t, "ArgoCDAgentFailed", ReasonArgoCDAgentFailed)
}

func TestGitOpsClusterSpec_DefaultValues(t *testing.T) {
	// Test that we can create a GitOpsCluster with minimal required fields
	gitopsCluster := &GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops",
			Namespace: "test-namespace",
		},
		Spec: GitOpsClusterSpec{
			ArgoServer: ArgoServerSpec{
				ArgoNamespace: "argocd",
			},
		},
	}

	assert.Equal(t, "test-gitops", gitopsCluster.Name)
	assert.Equal(t, "test-namespace", gitopsCluster.Namespace)
	assert.Equal(t, "argocd", gitopsCluster.Spec.ArgoServer.ArgoNamespace)
	assert.Nil(t, gitopsCluster.Spec.PlacementRef)
	assert.Empty(t, gitopsCluster.Spec.ManagedServiceAccountRef)
	assert.Nil(t, gitopsCluster.Spec.GitOpsAddon)
	assert.Nil(t, gitopsCluster.Spec.CreateBlankClusterSecrets)
	assert.Nil(t, gitopsCluster.Spec.CreatePolicyTemplate)
}

func TestArgoCDAgentSpec_DefaultValues(t *testing.T) {
	// Test ArgoCDAgentSpec with default values
	agentSpec := &ArgoCDAgentSpec{
		Image:         "test-agent-image",
		ServerAddress: "test-server",
		ServerPort:    "8080",
		Mode:          "grpc",
	}

	assert.Equal(t, "test-agent-image", agentSpec.Image)
	assert.Equal(t, "test-server", agentSpec.ServerAddress)
	assert.Equal(t, "8080", agentSpec.ServerPort)
	assert.Equal(t, "grpc", agentSpec.Mode)
	assert.Nil(t, agentSpec.Enabled)
	assert.Nil(t, agentSpec.PropagateHubCA)
}

func TestGitOpsClusterWithGitOpsAddon(t *testing.T) {
	// Test GitOpsCluster with GitOpsAddon configuration
	enabled := true
	propagateCA := true

	gitopsCluster := &GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitops-with-addon",
			Namespace: "test-namespace",
		},
		Spec: GitOpsClusterSpec{
			ArgoServer: ArgoServerSpec{
				ArgoNamespace: "argocd",
			},
			GitOpsAddon: &GitOpsAddonSpec{
				Enabled:                 &enabled,
				GitOpsOperatorImage:     "operator:v1.0.0",
				GitOpsImage:             "argocd:v2.0.0",
				RedisImage:              "redis:v3.0.0",
				GitOpsOperatorNamespace: "gitops-operator",
				GitOpsNamespace:         "gitops",
				ReconcileScope:          "All-Namespaces",
				OverrideExistingConfigs: &enabled,
				ArgoCDAgent: &ArgoCDAgentSpec{
					Enabled:        &enabled,
					PropagateHubCA: &propagateCA,
					Image:          "test-agent-image",
					ServerAddress:  "test-server.example.com",
					ServerPort:     "443",
					Mode:           "grpc",
				},
			},
		},
	}

	assert.NotNil(t, gitopsCluster.Spec.GitOpsAddon)
	assert.True(t, *gitopsCluster.Spec.GitOpsAddon.Enabled)
	assert.Equal(t, "operator:v1.0.0", gitopsCluster.Spec.GitOpsAddon.GitOpsOperatorImage)
	assert.Equal(t, "argocd:v2.0.0", gitopsCluster.Spec.GitOpsAddon.GitOpsImage)
	assert.Equal(t, "redis:v3.0.0", gitopsCluster.Spec.GitOpsAddon.RedisImage)
	assert.Equal(t, "gitops-operator", gitopsCluster.Spec.GitOpsAddon.GitOpsOperatorNamespace)
	assert.Equal(t, "gitops", gitopsCluster.Spec.GitOpsAddon.GitOpsNamespace)
	assert.Equal(t, "All-Namespaces", gitopsCluster.Spec.GitOpsAddon.ReconcileScope)
	assert.True(t, *gitopsCluster.Spec.GitOpsAddon.OverrideExistingConfigs)

	assert.NotNil(t, gitopsCluster.Spec.GitOpsAddon.ArgoCDAgent)
	assert.True(t, *gitopsCluster.Spec.GitOpsAddon.ArgoCDAgent.Enabled)
	assert.True(t, *gitopsCluster.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA)
	assert.Equal(t, "test-agent-image", gitopsCluster.Spec.GitOpsAddon.ArgoCDAgent.Image)
	assert.Equal(t, "test-server.example.com", gitopsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress)
	assert.Equal(t, "443", gitopsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort)
	assert.Equal(t, "grpc", gitopsCluster.Spec.GitOpsAddon.ArgoCDAgent.Mode)
}

// TestGitOpsClusterDeepCopy tests the DeepCopy functionality
func TestGitOpsClusterDeepCopy(t *testing.T) {
	original := &GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
			Labels: map[string]string{
				"app": "test",
			},
		},
		Spec: GitOpsClusterSpec{
			ArgoServer: ArgoServerSpec{
				ArgoNamespace: "argo-ns",
			},
			PlacementRef: &corev1.ObjectReference{
				Name:      "test-placement",
				Namespace: "test-ns",
			},
			GitOpsAddon: &GitOpsAddonSpec{
				Enabled:                 &[]bool{true}[0],
				GitOpsOperatorImage:     "quay.io/test/gitops-operator:latest",
				GitOpsOperatorNamespace: "gitops-system",
				GitOpsImage:             "quay.io/test/argocd:latest",
				GitOpsNamespace:         "argocd",
				RedisImage:              "redis:6.2",
				ReconcileScope:          "All-Namespaces",
				ArgoCDAgent: &ArgoCDAgentSpec{
					Enabled:        &[]bool{true}[0],
					PropagateHubCA: &[]bool{true}[0],
					Image:          "quay.io/test/argocd-agent:latest",
					ServerAddress:  "https://argocd.example.com",
					ServerPort:     "443",
					Mode:           "pull",
				},
			},
		},
		Status: GitOpsClusterStatus{
			Phase:   "succeeded",
			Message: "All components ready",
			Conditions: []metav1.Condition{
				{
					Type:   GitOpsClusterReady,
					Status: metav1.ConditionTrue,
					Reason: ReasonSuccess,
				},
			},
		},
	}

	// Test DeepCopy
	copied := original.DeepCopy()
	assert.NotNil(t, copied)
	assert.NotSame(t, original, copied) // Different objects
	assert.Equal(t, original.Name, copied.Name)
	assert.Equal(t, original.Namespace, copied.Namespace)
	assert.Equal(t, original.Spec.ArgoServer.ArgoNamespace, copied.Spec.ArgoServer.ArgoNamespace)
	assert.Equal(t, *original.Spec.GitOpsAddon.Enabled, *copied.Spec.GitOpsAddon.Enabled)
	assert.Equal(t, *original.Spec.GitOpsAddon.ArgoCDAgent.Enabled, *copied.Spec.GitOpsAddon.ArgoCDAgent.Enabled)
	assert.Equal(t, original.Status.Phase, copied.Status.Phase)

	// Modify original to ensure deep copy worked
	original.Name = "modified"
	original.Spec.ArgoServer.ArgoNamespace = "modified-ns"
	*original.Spec.GitOpsAddon.Enabled = false
	*original.Spec.GitOpsAddon.ArgoCDAgent.Enabled = false

	assert.Equal(t, "test-cluster", copied.Name)
	assert.Equal(t, "argo-ns", copied.Spec.ArgoServer.ArgoNamespace)
	assert.True(t, *copied.Spec.GitOpsAddon.Enabled)
	assert.True(t, *copied.Spec.GitOpsAddon.ArgoCDAgent.Enabled)
}

// TestGitOpsClusterListDeepCopy tests the GitOpsClusterList DeepCopy functionality
func TestGitOpsClusterListDeepCopy(t *testing.T) {
	original := &GitOpsClusterList{
		Items: []GitOpsCluster{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1",
					Namespace: "ns1",
				},
				Spec: GitOpsClusterSpec{
					ArgoServer: ArgoServerSpec{
						ArgoNamespace: "argo1",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster2",
					Namespace: "ns2",
				},
				Spec: GitOpsClusterSpec{
					ArgoServer: ArgoServerSpec{
						ArgoNamespace: "argo2",
					},
				},
			},
		},
	}

	// Test DeepCopy
	copied := original.DeepCopy()
	assert.NotNil(t, copied)
	assert.NotSame(t, original, copied)
	assert.Len(t, copied.Items, 2)
	assert.Equal(t, "cluster1", copied.Items[0].Name)
	assert.Equal(t, "cluster2", copied.Items[1].Name)

	// Modify original to ensure deep copy worked
	original.Items[0].Name = "modified"
	assert.Equal(t, "cluster1", copied.Items[0].Name)
}

// TestArgoCDAgentSpecDeepCopy tests the ArgoCDAgentSpec DeepCopy functionality
func TestArgoCDAgentSpecDeepCopy(t *testing.T) {
	original := &ArgoCDAgentSpec{
		Enabled:        &[]bool{true}[0],
		PropagateHubCA: &[]bool{false}[0],
		Image:          "quay.io/test/argocd-agent:v1.0.0",
		ServerAddress:  "https://argocd.example.com",
		ServerPort:     "8080",
		Mode:           "push",
	}

	// Test DeepCopy
	copied := original.DeepCopy()
	assert.NotNil(t, copied)
	assert.NotSame(t, original, copied)
	assert.NotSame(t, original.Enabled, copied.Enabled)
	assert.NotSame(t, original.PropagateHubCA, copied.PropagateHubCA)
	assert.Equal(t, *original.Enabled, *copied.Enabled)
	assert.Equal(t, *original.PropagateHubCA, *copied.PropagateHubCA)
	assert.Equal(t, original.Image, copied.Image)
	assert.Equal(t, original.ServerAddress, copied.ServerAddress)

	// Modify original to ensure deep copy worked
	*original.Enabled = false
	*original.PropagateHubCA = true
	original.Image = "modified"

	assert.True(t, *copied.Enabled)
	assert.False(t, *copied.PropagateHubCA)
	assert.Equal(t, "quay.io/test/argocd-agent:v1.0.0", copied.Image)
}

// TestGitOpsClusterSpecDeepCopy tests the GitOpsClusterSpec DeepCopy functionality
func TestGitOpsClusterSpecDeepCopy(t *testing.T) {
	original := &GitOpsClusterSpec{
		ArgoServer: ArgoServerSpec{
			ArgoNamespace: "test-argo",
			Cluster:       "test-cluster",
		},
		PlacementRef: &corev1.ObjectReference{
			Name:      "test-placement",
			Namespace: "test-ns",
			Kind:      "Placement",
		},
		GitOpsAddon: &GitOpsAddonSpec{
			Enabled: &[]bool{true}[0],
			ArgoCDAgent: &ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0],
				Image:   "test-image",
			},
		},
	}

	// Test DeepCopy
	copied := original.DeepCopy()
	assert.NotNil(t, copied)
	assert.NotSame(t, original, copied)
	assert.NotSame(t, original.PlacementRef, copied.PlacementRef)
	assert.NotSame(t, original.GitOpsAddon, copied.GitOpsAddon)
	assert.Equal(t, original.ArgoServer.ArgoNamespace, copied.ArgoServer.ArgoNamespace)
	assert.Equal(t, original.PlacementRef.Name, copied.PlacementRef.Name)
	assert.Equal(t, *original.GitOpsAddon.Enabled, *copied.GitOpsAddon.Enabled)
	assert.Equal(t, *original.GitOpsAddon.ArgoCDAgent.Enabled, *copied.GitOpsAddon.ArgoCDAgent.Enabled)

	// Test with nil GitOpsAddon
	original.GitOpsAddon = nil
	copied = original.DeepCopy()
	assert.Nil(t, copied.GitOpsAddon)
}

// TestGitOpsClusterStatusDeepCopy tests the GitOpsClusterStatus DeepCopy functionality
func TestGitOpsClusterStatusDeepCopy(t *testing.T) {
	original := &GitOpsClusterStatus{
		Phase:   "succeeded",
		Message: "Test message",
		Conditions: []metav1.Condition{
			{
				Type:   GitOpsClusterReady,
				Status: metav1.ConditionTrue,
				Reason: ReasonSuccess,
			},
			{
				Type:   GitOpsClusterArgoCDAgentPrereqsReady,
				Status: metav1.ConditionFalse,
				Reason: ReasonArgoCDAgentFailed,
			},
		},
	}

	// Test DeepCopy
	copied := original.DeepCopy()
	assert.NotNil(t, copied)
	assert.NotSame(t, original, copied)
	assert.NotSame(t, &original.Conditions, &copied.Conditions)
	assert.Equal(t, original.Phase, copied.Phase)
	assert.Equal(t, original.Message, copied.Message)
	assert.Len(t, copied.Conditions, 2)
	assert.Equal(t, original.Conditions[0].Type, copied.Conditions[0].Type)
	assert.Equal(t, original.Conditions[1].Status, copied.Conditions[1].Status)

	// Modify original to ensure deep copy worked
	original.Phase = "modified"
	original.Conditions[0].Type = "modified"
	assert.Equal(t, "succeeded", copied.Phase)
	assert.Equal(t, GitOpsClusterReady, copied.Conditions[0].Type)
}
