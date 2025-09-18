// Copyright 2021 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gitopscluster_test

import (
	"testing"
	"time"

	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
)

func TestGitOpsClusterConditions(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("SetCondition adds new condition", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess,
			"All components are ready",
		)

		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(1))
		condition := gitopsCluster.Status.Conditions[0]
		g.Expect(condition.Type).To(gomega.Equal(gitopsclusterV1beta1.GitOpsClusterReady))
		g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionTrue))
		g.Expect(condition.Reason).To(gomega.Equal(gitopsclusterV1beta1.ReasonSuccess))
		g.Expect(condition.Message).To(gomega.Equal("All components are ready"))
		g.Expect(condition.LastTransitionTime).NotTo(gomega.BeZero())
	})

	t.Run("SetCondition updates existing condition with status change", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		// Set initial condition
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess,
			"All components are ready",
		)

		firstTransitionTime := gitopsCluster.Status.Conditions[0].LastTransitionTime

		// Wait a bit to ensure time difference
		time.Sleep(10 * time.Millisecond)

		// Update condition with different status
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionFalse,
			gitopsclusterV1beta1.ReasonClusterRegistrationFailed,
			"Cluster registration failed",
		)

		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(1))
		condition := gitopsCluster.Status.Conditions[0]
		g.Expect(condition.Type).To(gomega.Equal(gitopsclusterV1beta1.GitOpsClusterReady))
		g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionFalse))
		g.Expect(condition.Reason).To(gomega.Equal(gitopsclusterV1beta1.ReasonClusterRegistrationFailed))
		g.Expect(condition.Message).To(gomega.Equal("Cluster registration failed"))
		g.Expect(condition.LastTransitionTime.After(firstTransitionTime.Time)).To(gomega.BeTrue())
	})

	t.Run("SetCondition updates existing condition without status change", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		// Set initial condition
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess,
			"All components are ready",
		)

		firstTransitionTime := gitopsCluster.Status.Conditions[0].LastTransitionTime

		// Wait a bit
		time.Sleep(10 * time.Millisecond)

		// Update condition with same status but different reason/message
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess,
			"All components are still ready",
		)

		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(1))
		condition := gitopsCluster.Status.Conditions[0]
		g.Expect(condition.Type).To(gomega.Equal(gitopsclusterV1beta1.GitOpsClusterReady))
		g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionTrue))
		g.Expect(condition.Reason).To(gomega.Equal(gitopsclusterV1beta1.ReasonSuccess))
		g.Expect(condition.Message).To(gomega.Equal("All components are still ready"))
		// LastTransitionTime should not change when status doesn't change
		g.Expect(condition.LastTransitionTime.Equal(&firstTransitionTime)).To(gomega.BeTrue())
	})

	t.Run("GetCondition returns correct condition", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		// Set multiple conditions
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess,
			"Ready",
		)
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
			metav1.ConditionFalse,
			gitopsclusterV1beta1.ReasonPlacementNotFound,
			"Placement not found",
		)

		// Get existing condition
		condition := gitopsCluster.GetCondition(gitopsclusterV1beta1.GitOpsClusterReady)
		g.Expect(condition).NotTo(gomega.BeNil())
		g.Expect(condition.Type).To(gomega.Equal(gitopsclusterV1beta1.GitOpsClusterReady))
		g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionTrue))

		// Get non-existing condition
		condition = gitopsCluster.GetCondition(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)
		g.Expect(condition).To(gomega.BeNil())
	})

	t.Run("IsConditionTrue returns correct values", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess,
			"Ready",
		)
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
			metav1.ConditionFalse,
			gitopsclusterV1beta1.ReasonPlacementNotFound,
			"Not resolved",
		)

		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterReady)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterPlacementResolved)).To(gomega.BeFalse())
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)).To(gomega.BeFalse())
	})

	t.Run("IsConditionFalse returns correct values", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess,
			"Ready",
		)
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
			metav1.ConditionFalse,
			gitopsclusterV1beta1.ReasonPlacementNotFound,
			"Not resolved",
		)

		g.Expect(gitopsCluster.IsConditionFalse(gitopsclusterV1beta1.GitOpsClusterReady)).To(gomega.BeFalse())
		g.Expect(gitopsCluster.IsConditionFalse(gitopsclusterV1beta1.GitOpsClusterPlacementResolved)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionFalse(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)).To(gomega.BeFalse())
	})

	t.Run("Multiple conditions coexist", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		// Set all condition types
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Ready")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterPlacementResolved, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Resolved")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterClustersRegistered, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Registered")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Agent ready")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterCertificatesReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Certificates ready")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "ManifestWorks applied")

		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(6))

		// Verify all conditions exist and are True
		for _, conditionType := range []string{
			gitopsclusterV1beta1.GitOpsClusterReady,
			gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
			gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
			gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady,
			gitopsclusterV1beta1.GitOpsClusterCertificatesReady,
			gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied,
		} {
			g.Expect(gitopsCluster.IsConditionTrue(conditionType)).To(gomega.BeTrue(), "Condition %s should be True", conditionType)
		}
	})

	t.Run("Condition constants are properly defined", func(t *testing.T) {
		// Test condition types
		g.Expect(gitopsclusterV1beta1.GitOpsClusterReady).To(gomega.Equal("Ready"))
		g.Expect(gitopsclusterV1beta1.GitOpsClusterPlacementResolved).To(gomega.Equal("PlacementResolved"))
		g.Expect(gitopsclusterV1beta1.GitOpsClusterClustersRegistered).To(gomega.Equal("ClustersRegistered"))
		g.Expect(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady).To(gomega.Equal("ArgoCDAgentReady"))
		g.Expect(gitopsclusterV1beta1.GitOpsClusterCertificatesReady).To(gomega.Equal("CertificatesReady"))
		g.Expect(gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied).To(gomega.Equal("ManifestWorksApplied"))

		// Test reason constants
		g.Expect(gitopsclusterV1beta1.ReasonSuccess).To(gomega.Equal("Success"))
		g.Expect(gitopsclusterV1beta1.ReasonDisabled).To(gomega.Equal("Disabled"))
		g.Expect(gitopsclusterV1beta1.ReasonNotRequired).To(gomega.Equal("NotRequired"))
		g.Expect(gitopsclusterV1beta1.ReasonInvalidConfiguration).To(gomega.Equal("InvalidConfiguration"))
		g.Expect(gitopsclusterV1beta1.ReasonPlacementNotFound).To(gomega.Equal("PlacementNotFound"))
		g.Expect(gitopsclusterV1beta1.ReasonManagedClustersNotFound).To(gomega.Equal("ManagedClustersNotFound"))
		g.Expect(gitopsclusterV1beta1.ReasonArgoServerNotFound).To(gomega.Equal("ArgoServerNotFound"))
		g.Expect(gitopsclusterV1beta1.ReasonClusterRegistrationFailed).To(gomega.Equal("ClusterRegistrationFailed"))
		g.Expect(gitopsclusterV1beta1.ReasonCertificateSigningFailed).To(gomega.Equal("CertificateSigningFailed"))
		g.Expect(gitopsclusterV1beta1.ReasonManifestWorkFailed).To(gomega.Equal("ManifestWorkFailed"))
		g.Expect(gitopsclusterV1beta1.ReasonArgoCDAgentFailed).To(gomega.Equal("ArgoCDAgentFailed"))
	})

	t.Run("Legacy and new fields coexist", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{}

		// Set legacy fields
		gitopsCluster.Status.Phase = "successful"
		gitopsCluster.Status.Message = "Legacy message"
		gitopsCluster.Status.LastUpdateTime = metav1.Now()

		// Set new conditions
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterReady,
			metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess,
			"All components are ready",
		)

		// Both should coexist
		g.Expect(gitopsCluster.Status.Phase).To(gomega.Equal("successful"))
		g.Expect(gitopsCluster.Status.Message).To(gomega.Equal("Legacy message"))
		g.Expect(gitopsCluster.Status.LastUpdateTime).NotTo(gomega.BeZero())
		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(1))
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterReady)).To(gomega.BeTrue())
	})
}
