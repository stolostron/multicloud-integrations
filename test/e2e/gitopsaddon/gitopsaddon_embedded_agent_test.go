//go:build e2e

package gitopsaddon_e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Mirrors test-scenarios.sh Scenario 5: Kind cluster + local-cluster, agent mode
var _ = Describe("GitOps Addon - Embedded Operator + Agent (Kind)", Label("embedded-agent"), Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	var opts gitOpsClusterOpts

	BeforeAll(func() {
		opts = gitOpsClusterOpts{
			name:          gitopsClusterName,
			namespace:     argoCDNamespace,
			placementName: placementName,
			agentEnabled:  true,
		}
		createBaseResources()
		createGitOpsCluster(opts)
	})

	AfterAll(func() {
		safeCleanup(opts)
	})

	Context("Spoke + Agent Deployment", func() {
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
			}, 3*time.Minute)
		})
	})

	Context("Agent Application Sync", func() {
		It("should sync guestbook from hub to spoke via agent and reflect status back", func() {
			deployGuestbookAgentMode(15 * time.Minute)
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

		It("should deploy ArgoCD CR in local-cluster namespace on hub", func() {
			verifyLocalClusterArgoCDDeployed(8 * time.Minute)
		})

		It("should NOT have duplicate acm-openshift-gitops in openshift-gitops on hub", func() {
			verifyNoDuplicateArgoCDOnHub()
		})

		It("should deploy and sync guestbook on local-cluster", func() {
			verifyLocalClusterGuestbook(true, 10*time.Minute)
		})

		It("should have correct controller namespace for local-cluster app", func() {
			verifyLocalClusterControllerNamespace()
		})

		It("should have no cross-namespace conflicts on local-cluster", func() {
			verifyLocalClusterEnvironmentHealth()
		})
	})

	Context("Cleanup", func() {
		It("should clean up guestbook resources", func() {
			cleanupGuestbookResources(true)
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
