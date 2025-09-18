package gitopscluster

import (
	"context"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestUpdateGitOpsClusterConditionsEdgeCases(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

	t.Run("UpdateConditionsWithNilConditionUpdates", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Status: gitopsclusterV1beta1.GitOpsClusterStatus{
				Conditions: []metav1.Condition{},
			},
		}

		// Test with nil condition updates map
		reconciler.updateGitOpsClusterConditions(gitopsCluster, "test-phase", "test-message", nil)

		g.Expect(gitopsCluster.Status.Phase).To(gomega.Equal("test-phase"))
		g.Expect(gitopsCluster.Status.Message).To(gomega.Equal("test-message"))
		g.Expect(gitopsCluster.Status.LastUpdateTime).NotTo(gomega.BeZero())
	})

	t.Run("UpdateConditionsWithEmptyConditionUpdates", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Status: gitopsclusterV1beta1.GitOpsClusterStatus{
				Conditions: []metav1.Condition{},
			},
		}

		// Test with empty condition updates map
		conditionUpdates := make(map[string]ConditionUpdate)
		reconciler.updateGitOpsClusterConditions(gitopsCluster, "test-phase", "test-message", conditionUpdates)

		g.Expect(gitopsCluster.Status.Phase).To(gomega.Equal("test-phase"))
		g.Expect(gitopsCluster.Status.Message).To(gomega.Equal("test-message"))
		g.Expect(gitopsCluster.Status.LastUpdateTime).NotTo(gomega.BeZero())
	})

	t.Run("UpdateReadyConditionWithArgoCDAgentDisabled", func(t *testing.T) {
		enabled := false
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled: &enabled,
				},
			},
			Status: gitopsclusterV1beta1.GitOpsClusterStatus{
				Conditions: []metav1.Condition{
					{
						Type:   gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
						Status: metav1.ConditionTrue,
					},
					{
						Type:   gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
						Status: metav1.ConditionTrue,
					},
				},
			},
		}

		reconciler.updateReadyCondition(gitopsCluster)

		readyCondition := gitopsCluster.GetCondition(gitopsclusterV1beta1.GitOpsClusterReady)
		g.Expect(readyCondition).NotTo(gomega.BeNil())
		g.Expect(readyCondition.Status).To(gomega.Equal(metav1.ConditionTrue))
		g.Expect(readyCondition.Reason).To(gomega.Equal(gitopsclusterV1beta1.ReasonSuccess))
	})

	t.Run("UpdateReadyConditionWithArgoCDAgentEnabledButNotReady", func(t *testing.T) {
		enabled := true
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled: &enabled,
				},
			},
			Status: gitopsclusterV1beta1.GitOpsClusterStatus{
				Conditions: []metav1.Condition{
					{
						Type:   gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
						Status: metav1.ConditionTrue,
					},
					{
						Type:   gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
						Status: metav1.ConditionTrue,
					},
					{
						Type:   gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady,
						Status: metav1.ConditionFalse,
					},
				},
			},
		}

		reconciler.updateReadyCondition(gitopsCluster)

		readyCondition := gitopsCluster.GetCondition(gitopsclusterV1beta1.GitOpsClusterReady)
		g.Expect(readyCondition).NotTo(gomega.BeNil())
		g.Expect(readyCondition.Status).To(gomega.Equal(metav1.ConditionFalse))
	})

	t.Run("UpdateReadyConditionWithMixedConditionStates", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Status: gitopsclusterV1beta1.GitOpsClusterStatus{
				Conditions: []metav1.Condition{
					{
						Type:   gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
						Status: metav1.ConditionTrue,
					},
					{
						Type:   gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
						Status: metav1.ConditionFalse,
					},
				},
			},
		}

		reconciler.updateReadyCondition(gitopsCluster)

		readyCondition := gitopsCluster.GetCondition(gitopsclusterV1beta1.GitOpsClusterReady)
		g.Expect(readyCondition).NotTo(gomega.BeNil())
		g.Expect(readyCondition.Status).To(gomega.Equal(metav1.ConditionFalse))
	})

	t.Run("UpdateReadyConditionWithMissingCriticalConditions", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Status: gitopsclusterV1beta1.GitOpsClusterStatus{
				Conditions: []metav1.Condition{
					// Missing PlacementResolved and ClustersRegistered
				},
			},
		}

		reconciler.updateReadyCondition(gitopsCluster)

		readyCondition := gitopsCluster.GetCondition(gitopsclusterV1beta1.GitOpsClusterReady)
		g.Expect(readyCondition).NotTo(gomega.BeNil())
		// When critical conditions are missing, the status becomes Unknown rather than False
		g.Expect(readyCondition.Status).To(gomega.Equal(metav1.ConditionUnknown))
	})
}

func TestEnsureArgoCDRedisSecretEdgeCases(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	t.Run("EnsureArgoCDRedisSecretWithEmptyNamespace", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		// Test with empty namespace - this should fail when trying to find redis password secret
		err := reconciler.ensureArgoCDRedisSecret("")
		g.Expect(err).To(gomega.HaveOccurred())
		// The actual error is about not finding the redis initial password secret
		g.Expect(err.Error()).To(gomega.ContainSubstring("no secret found ending with 'redis-initial-password'"))
	})

	t.Run("EnsureArgoCDRedisSecretCreationError", func(t *testing.T) {
		// Test when secret creation fails - fake client will fail to find initial password secret
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		// This should fail since there's no initial password secret
		err := reconciler.ensureArgoCDRedisSecret("test-namespace")
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("no secret found ending with 'redis-initial-password'"))
	})
}

func TestValidateArgoCDAgentSpecExtendedEdgeCases(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

	t.Run("ValidateSpecWithMaxLengthValues", func(t *testing.T) {
		// Test with very long string values
		longString := string(make([]byte, 1000))
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode:           "managed",
			ReconcileScope: "All-Namespaces",
			Action:         "Install",
			Image:          longString,
			ServerAddress:  longString,
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).NotTo(gomega.HaveOccurred()) // Long strings should be valid
	})

	t.Run("ValidateSpecWithSpecialCharacters", func(t *testing.T) {
		// Test with special characters in Mode (should fail)
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode: "managed@#$%",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).To(gomega.HaveOccurred())
	})

	t.Run("ValidateSpecWithUnicodeCharacters", func(t *testing.T) {
		// Test with unicode characters in fields
		spec := &gitopsclusterV1beta1.ArgoCDAgentSpec{
			Mode: "managed🚀",
		}
		err := reconciler.ValidateArgoCDAgentSpec(spec)
		g.Expect(err).To(gomega.HaveOccurred())
	})
}

func TestReconcileGitOpsClusterErrorConditions(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

	t.Run("ReconcileWithNonExistentGitOpsCluster", func(t *testing.T) {
		// Test reconciling a non-existent GitOpsCluster
		request := types.NamespacedName{
			Namespace: "test-namespace",
			Name:      "non-existent",
		}

		result, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: request})
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(result.Requeue).To(gomega.BeFalse())
	})

	t.Run("ReconcileWithInvalidGitOpsClusterSpec", func(t *testing.T) {
		// Create a GitOpsCluster with invalid ArgoCDAgent spec
		invalidGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "invalid-cluster",
				Namespace: "test-namespace",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
					ArgoNamespace: "test-argo",
				},
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Mode: "invalid-mode",
				},
			},
		}

		err := fakeClient.Create(context.TODO(), invalidGitOpsCluster)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		request := types.NamespacedName{
			Namespace: "test-namespace",
			Name:      "invalid-cluster",
		}

		// This should trigger validation failure and update conditions
		result, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: request})
		g.Expect(err).To(gomega.HaveOccurred()) // Should fail due to validation
		g.Expect(result.Requeue).To(gomega.BeTrue())
	})
}
