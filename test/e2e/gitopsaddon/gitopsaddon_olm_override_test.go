//go:build e2e

package gitopsaddon_e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Mirrors test-scenarios.sh Scenario 4: Kind cluster with olmSubscription.enabled=true.
// On Kind (no OLM), the addon will fail to install the operator via OLM, but
// the hub-side behavior is fully validated: OLM_SUBSCRIPTION_ENABLED=true is
// propagated to the AddOnDeploymentConfig, and the GitOpsCluster conditions
// reflect the correct state.
var _ = Describe("GitOps Addon - OLM Override on Kind", Label("olm-override"), Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	var opts gitOpsClusterOpts

	BeforeAll(func() {
		opts = gitOpsClusterOpts{
			name:          "olm-override-gitops",
			namespace:     argoCDNamespace,
			placementName: "olm-override-placement",
			agentEnabled:  false,
			olmEnabled:    true,
		}
		By("creating ManagedClusterSetBinding")
		Expect(applyLiteral(hubContext, managedClusterSetBindingYAML(argoCDNamespace))).To(Succeed())

		By("creating Placement targeting spoke only")
		Expect(applyLiteral(hubContext, placementWithClusterYAML(opts.placementName, argoCDNamespace, spokeName))).To(Succeed())

		createGitOpsCluster(opts)
	})

	AfterAll(func() {
		safeCleanupOLMOverride(opts)
	})

	Context("Hub-Side OLM Override Propagation", func() {
		It("should create ManagedClusterAddOn for spoke", func() {
			waitForResourceExists(hubContext, "managedclusteraddon", addonName, spokeName, 5*time.Minute)
		})

		It("should propagate OLM_SUBSCRIPTION_ENABLED=true to AddOnDeploymentConfig", func() {
			verifyOLMOverrideEnvVars(spokeName, 3*time.Minute)
		})

		It("should propagate default OLM subscription values", func() {
			verifyAddOnDeploymentConfigEnvVar(spokeName, "OLM_SUBSCRIPTION_NAME", "openshift-gitops-operator", 2*time.Minute)
			verifyAddOnDeploymentConfigEnvVar(spokeName, "OLM_SUBSCRIPTION_CHANNEL", "latest", 2*time.Minute)
			verifyAddOnDeploymentConfigEnvVar(spokeName, "OLM_SUBSCRIPTION_SOURCE", "redhat-operators", 2*time.Minute)
		})

		It("should have AddOnDeploymentConfigsReady condition True", func() {
			waitForConditionTrue(hubContext, "gitopscluster", opts.name, argoCDNamespace,
				"AddOnDeploymentConfigsReady", 3*time.Minute)
		})

		It("should have ManagedClusterAddOnsReady condition True", func() {
			waitForConditionTrue(hubContext, "gitopscluster", opts.name, argoCDNamespace,
				"ManagedClusterAddOnsReady", 3*time.Minute)
		})

		It("should have ArgoCDPolicyReady condition True", func() {
			waitForConditionTrue(hubContext, "gitopscluster", opts.name, argoCDNamespace,
				"ArgoCDPolicyReady", 3*time.Minute)
		})
	})

	Context("Cleanup", func() {
		It("should delete all OLM override test resources", func() {
			safeCleanupOLMOverride(opts)
		})
	})

	Context("Cleanup Verification", func() {
		It("should have removed GitOpsCluster and MCA from hub", func() {
			waitForResourceGone(hubContext, "gitopscluster", opts.name, argoCDNamespace, 2*time.Minute)
			waitForResourceGone(hubContext, "managedclusteraddon", addonName, spokeName, 3*time.Minute)
		})
	})
})
