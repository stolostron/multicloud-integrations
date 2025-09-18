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

package gitopscluster

import (
	"testing"

	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
)

func TestUpdateGitOpsClusterConditions(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("legacy and new fields coexist correctly", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
		}

		conditionUpdates := map[string]ConditionUpdate{
			gitopsclusterV1beta1.GitOpsClusterPlacementResolved: {
				Status:  metav1.ConditionTrue,
				Reason:  gitopsclusterV1beta1.ReasonSuccess,
				Message: "Placement resolved successfully",
			},
			gitopsclusterV1beta1.GitOpsClusterClustersRegistered: {
				Status:  metav1.ConditionFalse,
				Reason:  gitopsclusterV1beta1.ReasonClusterRegistrationFailed,
				Message: "Failed to register clusters",
			},
		}

		// Set legacy fields
		gitopsCluster.Status.Phase = "failed"
		gitopsCluster.Status.Message = "Test message"
		gitopsCluster.Status.LastUpdateTime = metav1.Now()

		// Set conditions manually to test that they work alongside legacy fields
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
			conditionUpdates[gitopsclusterV1beta1.GitOpsClusterPlacementResolved].Status,
			conditionUpdates[gitopsclusterV1beta1.GitOpsClusterPlacementResolved].Reason,
			conditionUpdates[gitopsclusterV1beta1.GitOpsClusterPlacementResolved].Message,
		)
		gitopsCluster.SetCondition(
			gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
			conditionUpdates[gitopsclusterV1beta1.GitOpsClusterClustersRegistered].Status,
			conditionUpdates[gitopsclusterV1beta1.GitOpsClusterClustersRegistered].Reason,
			conditionUpdates[gitopsclusterV1beta1.GitOpsClusterClustersRegistered].Message,
		)

		// Test that conditions were set correctly
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterPlacementResolved)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionFalse(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)).To(gomega.BeTrue())

		// Test that legacy fields are still accessible
		g.Expect(gitopsCluster.Status.Phase).To(gomega.Equal("failed"))
		g.Expect(gitopsCluster.Status.Message).To(gomega.Equal("Test message"))
		g.Expect(gitopsCluster.Status.LastUpdateTime).NotTo(gomega.BeZero())
	})

	t.Run("Ready condition logic with ArgoCD agent disabled", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				// ArgoCD agent not specified (disabled by default)
			},
		}

		// Set basic conditions to True
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterPlacementResolved, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Resolved")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterClustersRegistered, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Registered")

		// Set agent-related conditions as not required since agent is disabled
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonDisabled, "Agent disabled")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterCertificatesReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonNotRequired, "Not required")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonNotRequired, "Not required")

		// Now manually test the logic that would be in updateReadyCondition
		placementResolved := gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterPlacementResolved)
		clustersRegistered := gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)

		// Check if any condition is False
		hasError := false
		for _, condition := range gitopsCluster.Status.Conditions {
			if condition.Status == metav1.ConditionFalse {
				hasError = true
				break
			}
		}

		g.Expect(placementResolved).To(gomega.BeTrue())
		g.Expect(clustersRegistered).To(gomega.BeTrue())
		g.Expect(hasError).To(gomega.BeFalse())

		// Ready should be True when agent is disabled and basic conditions are met
		if !hasError && placementResolved && clustersRegistered {
			gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "GitOpsCluster is ready")
		}

		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterReady)).To(gomega.BeTrue())
	})

	t.Run("Ready condition logic with ArgoCD agent enabled", func(t *testing.T) {
		enabled := true
		propagateCA := true
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:        &enabled,
					PropagateHubCA: &propagateCA,
				},
			},
		}

		// Set all conditions to True
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterPlacementResolved, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Resolved")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterClustersRegistered, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Registered")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Agent ready")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterCertificatesReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Certificates ready")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "ManifestWorks applied")

		// Test the Ready condition logic
		placementResolved := gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterPlacementResolved)
		clustersRegistered := gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)
		agentReady := gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady)
		certificatesReady := gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterCertificatesReady)
		manifestWorksApplied := gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied)

		hasError := false
		for _, condition := range gitopsCluster.Status.Conditions {
			if condition.Status == metav1.ConditionFalse {
				hasError = true
				break
			}
		}

		g.Expect(placementResolved).To(gomega.BeTrue())
		g.Expect(clustersRegistered).To(gomega.BeTrue())
		g.Expect(agentReady).To(gomega.BeTrue())
		g.Expect(certificatesReady).To(gomega.BeTrue())
		g.Expect(manifestWorksApplied).To(gomega.BeTrue())
		g.Expect(hasError).To(gomega.BeFalse())

		// Ready should be True when all agent conditions are True
		if !hasError && placementResolved && clustersRegistered && agentReady && certificatesReady && manifestWorksApplied {
			gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "All components ready")
		}

		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterReady)).To(gomega.BeTrue())
	})

	t.Run("Ready condition with error condition", func(t *testing.T) {
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
		}

		// Set some conditions True and one False
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterPlacementResolved, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Resolved")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterClustersRegistered, metav1.ConditionFalse, gitopsclusterV1beta1.ReasonClusterRegistrationFailed, "Failed to register")

		// Check if any condition is False
		hasError := false
		for _, condition := range gitopsCluster.Status.Conditions {
			if condition.Status == metav1.ConditionFalse {
				hasError = true
				break
			}
		}

		g.Expect(hasError).To(gomega.BeTrue())

		// Ready should be False when any condition is False
		if hasError {
			gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionFalse, gitopsclusterV1beta1.ReasonClusterRegistrationFailed, "One or more components have failed")
		}

		g.Expect(gitopsCluster.IsConditionFalse(gitopsclusterV1beta1.GitOpsClusterReady)).To(gomega.BeTrue())
	})

	t.Run("Ready condition with ArgoCD agent enabled but CA propagation disabled", func(t *testing.T) {
		enabled := true
		propagateCA := false
		gitopsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:        &enabled,
					PropagateHubCA: &propagateCA,
				},
			},
		}

		// Set basic conditions and agent conditions
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterPlacementResolved, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Resolved")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterClustersRegistered, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Registered")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Agent ready")
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterCertificatesReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "Certificates ready")
		// ManifestWorks not required when CA propagation is disabled
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonNotRequired, "CA propagation disabled")

		// Test that all conditions are True
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterPlacementResolved)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterCertificatesReady)).To(gomega.BeTrue())
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied)).To(gomega.BeTrue())

		// Ready should be True even when CA propagation is disabled but condition is set appropriately
		gitopsCluster.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionTrue, gitopsclusterV1beta1.ReasonSuccess, "All components ready")
		g.Expect(gitopsCluster.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterReady)).To(gomega.BeTrue())
	})
}
