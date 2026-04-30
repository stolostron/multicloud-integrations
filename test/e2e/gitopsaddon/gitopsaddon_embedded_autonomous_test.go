//go:build e2e

package gitopsaddon_e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Mirrors test-scenarios.sh Scenario 6: Kind cluster + local-cluster, autonomous agent mode
var _ = Describe("GitOps Addon - Embedded Operator + Autonomous Agent (Kind)", Label("embedded-autonomous"), Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	var opts gitOpsClusterOpts

	BeforeAll(func() {
		opts = gitOpsClusterOpts{
			name:          gitopsClusterName,
			namespace:     argoCDNamespace,
			placementName: placementName,
			agentEnabled:  true,
			agentMode:     "autonomous",
		}
		createBaseResources()
		createGitOpsCluster(opts)
	})

	AfterAll(func() {
		safeCleanup(opts)
	})

	Context("Spoke + Autonomous Agent Deployment", func() {
		It("should create ManagedClusterAddOn and deploy addon on spoke", func() {
			verifyAddonDeployed(8 * time.Minute)
		})

		It("should deploy embedded operator on spoke", func() {
			verifyEmbeddedOperator(5 * time.Minute)
		})

		It("should deploy ArgoCD CR and application-controller on spoke", func() {
			verifyArgoCDOnSpoke(8 * time.Minute)
		})

		It("should deploy ArgoCD agent pod on spoke", func() {
			verifyAgentPodRunning(10 * time.Minute)
		})

		It("should auto-discover principal server address from hub ArgoCD", func() {
			verifyPrincipalServerAddress(3 * time.Minute)
		})

		It("should create cluster secret with agent URL on hub", func() {
			verifyClusterSecret(3 * time.Minute)
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

		It("should propagate ARGOCD_AGENT_ENABLED=true to AddOnDeploymentConfig", func() {
			verifyAddOnDeploymentConfigEnvVar(spokeName, "ARGOCD_AGENT_ENABLED", "true", 3*time.Minute)
		})

		It("should propagate ARGOCD_AGENT_MODE=autonomous to AddOnDeploymentConfig", func() {
			verifyAddOnDeploymentConfigEnvVar(spokeName, "ARGOCD_AGENT_MODE", "autonomous", 3*time.Minute)
		})

		It("should propagate OLM_SUBSCRIPTION_ENABLED=false to AddOnDeploymentConfig", func() {
			verifyAddOnDeploymentConfigEnvVar(spokeName, "OLM_SUBSCRIPTION_ENABLED", "false", 3*time.Minute)
		})
	})

	Context("Autonomous Agent Application Sync via Policy", func() {
		It("should deploy guestbook via Policy on spoke and verify sync", func() {
			deployGuestbookAutonomousMode(15 * time.Minute)
		})
	})

	Context("Spoke Environment Health", func() {
		It("should have no cross-namespace application controller conflicts", func() {
			verifyEnvironmentHealth(spokeContext)
		})
	})

	Context("Local-Cluster Verification", func() {
		It("should create ManagedClusterAddOn for local-cluster", func() {
			verifyLocalClusterAddon(5 * time.Minute)
		})

		It("should verify autonomous agent infrastructure on local-cluster (hub)", func() {
			verifyLocalClusterAutonomousInfrastructure(8 * time.Minute)
		})

		It("should have no cross-namespace conflicts on local-cluster", func() {
			verifyLocalClusterEnvironmentHealth()
		})
	})

	Context("Cleanup", func() {
		It("should clean up guestbook resources", func() {
			cleanupGuestbookAutonomous()
		})

		It("should delete all scenario resources in proper order", func() {
			scenarioCleanup(opts)
		})
	})

	Context("Cleanup Verification", func() {
		It("should have removed GitOpsCluster, MCA (spoke), and MCA (local-cluster) from hub", func() {
			verifyHubCleanup(opts)
		})

		It("should have removed ArgoCD CR and operator from spoke", func() {
			verifySpokeCleanup()
		})

		It("should have removed ArgoCD CR from local-cluster namespace on hub", func() {
			verifyLocalClusterCleanup()
		})
	})
})
