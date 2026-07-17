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
		// e2e-Kind-only: see disableHubAppController's doc comment (ArgoCD version gap,
		// argo-cd#26425/#26442) - not needed/used by gitopsaddon/test-cycle.sh against a real hub.
		// The Local-Cluster Verification context below only checks secret/health shape, not app
		// reconciliation, so it doesn't need the hub app-controller on.
		disableHubAppController()
	})

	AfterAll(func() {
		enableHubAppController()
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

		It("should deploy ArgoCD agent pod on spoke without crash-looping", func() {
			verifyAgentPodRunning(10 * time.Minute)
		})

		It("should have destinationBasedMapping disabled on both hub Policy and spoke ArgoCD CR", func() {
			verifyDestinationBasedMappingDisabledForAutonomous(gitopsClusterName, argoCDNamespace, 3*time.Minute)
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

	// Deployed by connecting DIRECTLY to the managed cluster's own API server - never through
	// the hub. That's the actual autonomous-mode contract: the spoke is the source of truth,
	// and the hub only ever sees a read-only mirror. Delivering the same spec via a hub-authored
	// Policy would exercise a hub-push path indistinguishable from managed mode and wouldn't
	// prove autonomous mode's distinguishing behavior.
	Context("Autonomous Agent Application Sync (direct spoke connection)", func() {
		It("should sync a guestbook Application created directly on the spoke, and mirror it to the hub", func() {
			deployGuestbookDirectlyOnSpoke(10 * time.Minute)
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
			cleanupGuestbookDirectSpoke()
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
