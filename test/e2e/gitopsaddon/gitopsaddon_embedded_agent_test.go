//go:build e2e

package gitopsaddon_e2e

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Mirrors test-scenarios.sh Scenario 2: Kind cluster + local-cluster, agent mode
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
				"ArgoCDPolicyReady",
			}, 3*time.Minute)
		})

		It("should propagate ARGOCD_AGENT_ENABLED=true to AddOnDeploymentConfig", func() {
			verifyAddOnDeploymentConfigEnvVar(spokeName, "ARGOCD_AGENT_ENABLED", "true", 3*time.Minute)
		})

		It("should propagate OLM_SUBSCRIPTION_ENABLED=false to AddOnDeploymentConfig", func() {
			verifyAddOnDeploymentConfigEnvVar(spokeName, "OLM_SUBSCRIPTION_ENABLED", "false", 3*time.Minute)
		})
	})

	Context("Agent ApplicationSet Sync", func() {
		It("should deploy guestbook via ApplicationSet and agent, and verify sync status on hub", func() {
			deployGuestbookAgentMode(15 * time.Minute)
		})
	})

	Context("Spoke Environment Health", func() {
		It("should have no cross-namespace application controller conflicts", func() {
			verifyEnvironmentHealth(spokeContext)
		})
	})

	Context("Agent Version Drift Auto-Heal", func() {
		It("should patch ArgoCD Policy with principal image for agent drift heal", func() {
			verifyAgentVersionDriftHeal(gitopsClusterName, argoCDNamespace, 5*time.Minute)
		})
	})

	Context("Local-Cluster Verification (Hybrid Mode)", func() {
		It("should register local-cluster as a plain in-cluster ArgoCD secret (no agent routing)", func() {
			verifyLocalClusterSecret(5 * time.Minute)
		})

		It("should NOT have a duplicate acm-openshift-gitops ArgoCD instance anywhere on hub", func() {
			verifyNoDuplicateArgoCDOnHub()
		})

		It("should deploy and sync guestbook on local-cluster via the ApplicationSet + hub application controller", func() {
			verifyLocalClusterGuestbook(true, 10*time.Minute)
		})

		It("should have correct controller namespace for local-cluster app", func() {
			verifyLocalClusterControllerNamespace(true)
		})

		It("should have no addon-installed application controller anywhere on hub", func() {
			verifyLocalClusterEnvironmentHealth()
		})
	})

	Context("Cert Rotation Resilience", func() {
		It("should recreate argocd-agent-client-tls on spoke when deleted", func() {
			verifyCertRotationOnSpoke(argoCDNamespace, 2*time.Minute)
		})

		It("should have CA cert in the correct ArgoCD namespace on hub", func() {
			verifyCASecretInNamespace(argoCDNamespace, 1*time.Minute)
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
		It("should have removed GitOpsCluster and MCA (spoke) from hub", func() {
			verifyHubCleanup(opts)
		})

		It("should have removed ArgoCD CR and operator from spoke", func() {
			verifySpokeCleanup()
		})
	})
})
