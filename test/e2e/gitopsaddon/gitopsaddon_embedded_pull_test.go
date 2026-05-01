//go:build e2e

package gitopsaddon_e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Tests the old (non-argocd-agent) pull model with gitops-addon on local-cluster.
// The propagation controller creates ManifestWork for local-cluster Applications
// (previously skipped), and the ArgoCD instance in the local-cluster namespace
// reconciles the app.
var _ = Describe("GitOps Addon - Pull Model Propagation on Local-Cluster", Label("embedded-pull"), Ordered, func() {
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

	Context("Spoke and Local-Cluster Setup", func() {
		It("should create ManagedClusterAddOn and deploy addon on spoke", func() {
			verifyAddonDeployed(8 * time.Minute)
		})

		It("should deploy embedded operator on spoke", func() {
			verifyEmbeddedOperator(5 * time.Minute)
		})

		It("should deploy ArgoCD CR and application-controller on spoke", func() {
			verifyArgoCDOnSpoke(8 * time.Minute)
		})

		It("should create ManagedClusterAddOn for local-cluster", func() {
			verifyLocalClusterAddon(5 * time.Minute)
		})

		It("should deploy ArgoCD CR in local-cluster namespace on hub", func() {
			verifyLocalClusterArgoCDDeployed(8 * time.Minute)
		})
	})

	Context("Pull Model Propagation to Local-Cluster", func() {
		It("should wait for propagation controller to be running", func() {
			By("waiting for multicloud-integrations deployment (contains propagation controller)")
			waitForDeploymentReady(hubContext, controllerNamespace, "multicloud-integrations", 3*time.Minute)
		})

		It("should set up pull model prerequisites on hub", func() {
			setupPullModelPrerequisites()
		})

		It("should create ApplicationSet with pull annotations targeting local-cluster", func() {
			createPullModelApplicationSet()
		})

		It("should generate Application from ApplicationSet for local-cluster", func() {
			appName := localClusterName + "-guestbook-pull"
			By(fmt.Sprintf("waiting for ApplicationSet to generate %s", appName))
			waitForResourceExists(hubContext, "application", appName, argoCDNamespace, 5*time.Minute)
		})

		It("should create ManifestWork in local-cluster namespace", func() {
			verifyPullModelManifestWork(5 * time.Minute)
		})

		It("should propagate Application to local-cluster namespace and sync guestbook", func() {
			verifyPullModelGuestbookOnLocalCluster(10 * time.Minute)
		})
	})

	Context("Cleanup", func() {
		It("should clean up pull model resources", func() {
			cleanupPullModelResources()
		})

		It("should delete all scenario resources in proper order", func() {
			scenarioCleanup(opts)
		})
	})

	Context("Cleanup Verification", func() {
		It("should have removed GitOpsCluster and MCAs from hub", func() {
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
