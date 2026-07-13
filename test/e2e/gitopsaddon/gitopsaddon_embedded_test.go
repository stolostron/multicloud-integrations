//go:build e2e

package gitopsaddon_e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Mirrors test-scenarios.sh Scenario 1: Kind cluster + local-cluster, no agent, no OLM
var _ = Describe("GitOps Addon - Embedded Operator (Kind, No Agent)", Label("embedded"), Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	var opts gitOpsClusterOpts

	BeforeAll(func() {
		opts = gitOpsClusterOpts{
			name:          gitopsClusterName,
			namespace:     argoCDNamespace,
			placementName: placementName,
			agentEnabled:  false,
		}
		createBaseResources()
		createGitOpsCluster(opts)
	})

	AfterAll(func() {
		safeCleanup(opts)
	})

	Context("Spoke Deployment", func() {
		It("should create ManagedClusterAddOn and deploy addon on spoke", func() {
			verifyAddonDeployed(8 * time.Minute)
		})

		It("should deploy embedded operator on spoke (non-OCP auto-detected)", func() {
			verifyEmbeddedOperator(5 * time.Minute)
		})

		It("should NOT create OLM subscription on non-OCP spoke", func() {
			verifyNoOLMSubscription("openshift-gitops-operator", "openshift-operators")
		})

		It("should deploy ArgoCD CR and application-controller on spoke", func() {
			verifyArgoCDOnSpoke(8 * time.Minute)
		})

		It("should have all GitOpsCluster conditions True", func() {
			verifyGitOpsClusterConditions([]string{
				"Ready",
				"PlacementResolved",
				"ArgoServerVerified",
				"ClustersRegistered",
				"AddOnDeploymentConfigsReady",
				"ManagedClusterAddOnsReady",
				"ArgoCDPolicyReady",
			}, 3*time.Minute)
		})
	})

	Context("Spoke Application Sync", func() {
		It("should deploy guestbook via ArgoCD on spoke and verify it is synced", func() {
			deployGuestbookApp(5 * time.Minute)
		})
	})

	Context("Spoke Environment Health", func() {
		It("should have no cross-namespace application controller conflicts", func() {
			verifyEnvironmentHealth(spokeContext)
		})
	})

	Context("Skip ArgoCD Policy Annotation", func() {
		It("should have ArgoCD Policy on hub before annotation", func() {
			policyName := gitopsClusterName + "-argocd-policy"
			waitForResourceExists(hubContext, "policy.policy.open-cluster-management.io",
				policyName, argoCDNamespace, 2*time.Minute)
		})

		It("should not recreate Policy after annotation + deletion", func() {
			verifySkipArgoCDPolicyAnnotation(gitopsClusterName, argoCDNamespace, 3*time.Minute)
		})

		It("should recreate Policy after removing annotation", func() {
			verifyPolicyRecreatedAfterAnnotationRemoval(gitopsClusterName, argoCDNamespace, 3*time.Minute)
		})
	})

	// This scenario runs with agentEnabled=false, so the controller never calls
	// ensureLocalClusterSecret (only CreateArgoCDAgentClusters does, gated on
	// ArgoCDAgent.Enabled) - there is no cluster-local-cluster secret to verify here.
	// See gitopsaddon_embedded_agent_test.go / gitopsaddon_embedded_autonomous_test.go for that.
	Context("Local-Cluster Verification (Hybrid Mode)", func() {
		It("should NOT have a duplicate acm-openshift-gitops ArgoCD instance anywhere on hub", func() {
			verifyNoDuplicateArgoCDOnHub()
		})

		It("should deploy and sync guestbook on local-cluster via the hub's own application controller", func() {
			verifyLocalClusterGuestbook(false, 10*time.Minute)
		})

		It("should have correct controller namespace for local-cluster app", func() {
			verifyLocalClusterControllerNamespace(false)
		})

		It("should have no addon-installed application controller anywhere on hub", func() {
			verifyLocalClusterEnvironmentHealth()
		})
	})

	Context("Cleanup", func() {
		It("should clean up guestbook resources", func() {
			cleanupGuestbookResources(false)
		})

		It("should delete all scenario resources in proper order", func() {
			scenarioCleanup(opts)
		})
	})

	Context("Cleanup Verification", func() {
		It("should have removed GitOpsCluster and MCA (spoke) from hub", func() {
			verifyHubCleanup(opts)
		})

		It("should have removed ArgoCD CR and operator from spoke", func() {
			verifySpokeCleanup()
		})
	})
})
