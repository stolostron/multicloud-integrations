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

	// Autonomous mode only affects how Applications are dispatched to the SPOKE (via Policy,
	// not the ApplicationSet/principal pipeline) - it has no bearing on local-cluster, which is
	// never addon-installed or agent-routed regardless of mode. This just confirms hybrid mode's
	// local-cluster registration is unaffected by the spoke's agent mode setting.
	Context("Local-Cluster Verification (Hybrid Mode)", func() {
		It("should register local-cluster as a plain in-cluster ArgoCD secret (no agent routing)", func() {
			verifyLocalClusterSecret(5 * time.Minute)
		})

		It("should NOT have a duplicate acm-openshift-gitops ArgoCD instance anywhere on hub", func() {
			verifyNoDuplicateArgoCDOnHub()
		})

		It("should have no addon-installed application controller anywhere on hub", func() {
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
		It("should have removed GitOpsCluster and MCA (spoke) from hub", func() {
			verifyHubCleanup(opts)
		})

		It("should have removed ArgoCD CR and operator from spoke", func() {
			verifySpokeCleanup()
		})
	})
})
