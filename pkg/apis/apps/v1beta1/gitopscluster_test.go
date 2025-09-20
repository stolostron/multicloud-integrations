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

package v1beta1

import (
	"testing"
	"time"

	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGitOpsClusterConditionMethods(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("SetCondition creates and updates conditions correctly", func(t *testing.T) {
		gitopsCluster := &GitOpsCluster{}

		// Test adding a new condition
		gitopsCluster.SetCondition(GitOpsClusterReady, metav1.ConditionTrue, ReasonSuccess, "All components ready")

		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(1))
		condition := gitopsCluster.Status.Conditions[0]
		g.Expect(condition.Type).To(gomega.Equal(GitOpsClusterReady))
		g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionTrue))
		g.Expect(condition.Reason).To(gomega.Equal(ReasonSuccess))
		g.Expect(condition.Message).To(gomega.Equal("All components ready"))
		firstTime := condition.LastTransitionTime

		// Test updating the same condition with different status (should update LastTransitionTime)
		time.Sleep(10 * time.Millisecond)
		gitopsCluster.SetCondition(GitOpsClusterReady, metav1.ConditionFalse, ReasonClusterRegistrationFailed, "Failed")

		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(1))
		condition = gitopsCluster.Status.Conditions[0]
		g.Expect(condition.Type).To(gomega.Equal(GitOpsClusterReady))
		g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionFalse))
		g.Expect(condition.Reason).To(gomega.Equal(ReasonClusterRegistrationFailed))
		g.Expect(condition.Message).To(gomega.Equal("Failed"))
		g.Expect(condition.LastTransitionTime.After(firstTime.Time)).To(gomega.BeTrue())

		// Test updating the same condition with same status (should not update LastTransitionTime)
		secondTime := condition.LastTransitionTime
		time.Sleep(10 * time.Millisecond)
		gitopsCluster.SetCondition(GitOpsClusterReady, metav1.ConditionFalse, ReasonClusterRegistrationFailed, "Still failed")

		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(1))
		condition = gitopsCluster.Status.Conditions[0]
		g.Expect(condition.Message).To(gomega.Equal("Still failed"))
		g.Expect(condition.LastTransitionTime.Equal(&secondTime)).To(gomega.BeTrue())
	})

	t.Run("GetCondition returns correct condition or nil", func(t *testing.T) {
		gitopsCluster := &GitOpsCluster{}

		// Test getting non-existent condition
		condition := gitopsCluster.GetCondition(GitOpsClusterReady)
		g.Expect(condition).To(gomega.BeNil())

		// Add a condition
		gitopsCluster.SetCondition(GitOpsClusterReady, metav1.ConditionTrue, ReasonSuccess, "Ready")

		// Test getting existing condition
		condition = gitopsCluster.GetCondition(GitOpsClusterReady)
		g.Expect(condition).NotTo(gomega.BeNil())
		g.Expect(condition.Type).To(gomega.Equal(GitOpsClusterReady))
		g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionTrue))

		// Test getting different non-existent condition
		condition = gitopsCluster.GetCondition(GitOpsClusterPlacementResolved)
		g.Expect(condition).To(gomega.BeNil())
	})

	t.Run("IsConditionTrue returns correct boolean values", func(t *testing.T) {
		gitopsCluster := &GitOpsCluster{}

		// Test non-existent condition
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterReady)).To(gomega.BeFalse())

		// Add True condition
		gitopsCluster.SetCondition(GitOpsClusterReady, metav1.ConditionTrue, ReasonSuccess, "Ready")
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterReady)).To(gomega.BeTrue())

		// Add False condition
		gitopsCluster.SetCondition(GitOpsClusterPlacementResolved, metav1.ConditionFalse, ReasonPlacementNotFound, "Not found")
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterPlacementResolved)).To(gomega.BeFalse())

		// Add Unknown condition
		gitopsCluster.SetCondition(GitOpsClusterClustersRegistered, metav1.ConditionUnknown, "InProgress", "Processing")
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterClustersRegistered)).To(gomega.BeFalse())
	})

	t.Run("IsConditionFalse returns correct boolean values", func(t *testing.T) {
		gitopsCluster := &GitOpsCluster{}

		// Test non-existent condition
		g.Expect(gitopsCluster.IsConditionFalse(GitOpsClusterReady)).To(gomega.BeFalse())

		// Add False condition
		gitopsCluster.SetCondition(GitOpsClusterReady, metav1.ConditionFalse, ReasonClusterRegistrationFailed, "Failed")
		g.Expect(gitopsCluster.IsConditionFalse(GitOpsClusterReady)).To(gomega.BeTrue())

		// Add True condition
		gitopsCluster.SetCondition(GitOpsClusterPlacementResolved, metav1.ConditionTrue, ReasonSuccess, "Resolved")
		g.Expect(gitopsCluster.IsConditionFalse(GitOpsClusterPlacementResolved)).To(gomega.BeFalse())

		// Add Unknown condition
		gitopsCluster.SetCondition(GitOpsClusterClustersRegistered, metav1.ConditionUnknown, "InProgress", "Processing")
		g.Expect(gitopsCluster.IsConditionFalse(GitOpsClusterClustersRegistered)).To(gomega.BeFalse())
	})

	t.Run("Multiple conditions work independently", func(t *testing.T) {
		gitopsCluster := &GitOpsCluster{}

		// Add multiple conditions
		gitopsCluster.SetCondition(GitOpsClusterReady, metav1.ConditionTrue, ReasonSuccess, "Ready")
		gitopsCluster.SetCondition(GitOpsClusterPlacementResolved, metav1.ConditionTrue, ReasonSuccess, "Resolved")
		gitopsCluster.SetCondition(GitOpsClusterClustersRegistered, metav1.ConditionFalse, ReasonClusterRegistrationFailed, "Failed")
		gitopsCluster.SetCondition(GitOpsClusterArgoCDAgentPrereqsReady, metav1.ConditionTrue, ReasonSuccess, "Agent ready")
		gitopsCluster.SetCondition(GitOpsClusterCertificatesReady, metav1.ConditionUnknown, "InProgress", "Processing")
		gitopsCluster.SetCondition(GitOpsClusterManifestWorksApplied, metav1.ConditionTrue, ReasonNotRequired, "Not required")

		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(6))

		// Test each condition independently
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterReady)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterPlacementResolved)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionFalse(GitOpsClusterClustersRegistered)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterArgoCDAgentPrereqsReady)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterCertificatesReady)).To(gomega.BeFalse())
		g.Expect(gitopsCluster.IsConditionFalse(GitOpsClusterCertificatesReady)).To(gomega.BeFalse())
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterManifestWorksApplied)).To(gomega.BeTrue())

		// Update one condition and verify others are unchanged
		gitopsCluster.SetCondition(GitOpsClusterClustersRegistered, metav1.ConditionTrue, ReasonSuccess, "Now registered")
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterClustersRegistered)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterReady)).To(gomega.BeTrue()) // Should still be true
	})

	t.Run("Conditions coexist with legacy fields", func(t *testing.T) {
		gitopsCluster := &GitOpsCluster{
			Status: GitOpsClusterStatus{
				Phase:          "successful",
				Message:        "Legacy message",
				LastUpdateTime: metav1.Now(),
			},
		}

		// Add conditions
		gitopsCluster.SetCondition(GitOpsClusterReady, metav1.ConditionTrue, ReasonSuccess, "Ready")
		gitopsCluster.SetCondition(GitOpsClusterPlacementResolved, metav1.ConditionTrue, ReasonSuccess, "Resolved")

		// Verify legacy fields are preserved
		g.Expect(gitopsCluster.Status.Phase).To(gomega.Equal("successful"))
		g.Expect(gitopsCluster.Status.Message).To(gomega.Equal("Legacy message"))
		g.Expect(gitopsCluster.Status.LastUpdateTime).NotTo(gomega.BeZero())

		// Verify conditions work
		g.Expect(len(gitopsCluster.Status.Conditions)).To(gomega.Equal(2))
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterReady)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(GitOpsClusterPlacementResolved)).To(gomega.BeTrue())
	})

	t.Run("Condition constants have correct values", func(t *testing.T) {
		// Test condition type constants
		g.Expect(GitOpsClusterReady).To(gomega.Equal("Ready"))
		g.Expect(GitOpsClusterPlacementResolved).To(gomega.Equal("PlacementResolved"))
		g.Expect(GitOpsClusterClustersRegistered).To(gomega.Equal("ClustersRegistered"))
		g.Expect(GitOpsClusterArgoCDAgentPrereqsReady).To(gomega.Equal("ArgoCDAgentPrereqsReady"))
		g.Expect(GitOpsClusterCertificatesReady).To(gomega.Equal("CertificatesReady"))
		g.Expect(GitOpsClusterManifestWorksApplied).To(gomega.Equal("ManifestWorksApplied"))

		// Test reason constants
		g.Expect(ReasonSuccess).To(gomega.Equal("Success"))
		g.Expect(ReasonDisabled).To(gomega.Equal("Disabled"))
		g.Expect(ReasonNotRequired).To(gomega.Equal("NotRequired"))
		g.Expect(ReasonInvalidConfiguration).To(gomega.Equal("InvalidConfiguration"))
		g.Expect(ReasonPlacementNotFound).To(gomega.Equal("PlacementNotFound"))
		g.Expect(ReasonManagedClustersNotFound).To(gomega.Equal("ManagedClustersNotFound"))
		g.Expect(ReasonArgoServerNotFound).To(gomega.Equal("ArgoServerNotFound"))
		g.Expect(ReasonClusterRegistrationFailed).To(gomega.Equal("ClusterRegistrationFailed"))
		g.Expect(ReasonCertificateSigningFailed).To(gomega.Equal("CertificateSigningFailed"))
		g.Expect(ReasonManifestWorkFailed).To(gomega.Equal("ManifestWorkFailed"))
		g.Expect(ReasonArgoCDAgentFailed).To(gomega.Equal("ArgoCDAgentFailed"))
	})

	t.Run("DeepCopy works correctly with conditions", func(t *testing.T) {
		original := &GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Status: GitOpsClusterStatus{
				Phase:          "successful",
				Message:        "Original message",
				LastUpdateTime: metav1.Now(),
			},
		}

		// Add conditions to original
		original.SetCondition(GitOpsClusterReady, metav1.ConditionTrue, ReasonSuccess, "Ready")
		original.SetCondition(GitOpsClusterPlacementResolved, metav1.ConditionFalse, ReasonPlacementNotFound, "Not found")

		// Create deep copy
		copied := original.DeepCopy()

		// Verify they are separate objects
		g.Expect(copied).NotTo(gomega.BeIdenticalTo(original))
		g.Expect(&copied.Status).NotTo(gomega.BeIdenticalTo(&original.Status))
		g.Expect(&copied.Status.Conditions).NotTo(gomega.BeIdenticalTo(&original.Status.Conditions))

		// Verify contents are the same
		g.Expect(copied.Name).To(gomega.Equal(original.Name))
		g.Expect(copied.Namespace).To(gomega.Equal(original.Namespace))
		g.Expect(copied.Status.Phase).To(gomega.Equal(original.Status.Phase))
		g.Expect(copied.Status.Message).To(gomega.Equal(original.Status.Message))
		g.Expect(len(copied.Status.Conditions)).To(gomega.Equal(len(original.Status.Conditions)))

		// Verify condition contents are copied correctly
		g.Expect(copied.IsConditionTrue(GitOpsClusterReady)).To(gomega.BeTrue())
		g.Expect(copied.IsConditionFalse(GitOpsClusterPlacementResolved)).To(gomega.BeTrue())

		// Verify modifying the copy doesn't affect the original
		copied.SetCondition(GitOpsClusterReady, metav1.ConditionFalse, ReasonClusterRegistrationFailed, "Failed")
		g.Expect(original.IsConditionTrue(GitOpsClusterReady)).To(gomega.BeTrue()) // Original unchanged
		g.Expect(copied.IsConditionFalse(GitOpsClusterReady)).To(gomega.BeTrue())  // Copy changed
	})
}
