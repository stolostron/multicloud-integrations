//go:build e2e

package gitopsaddon_e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const kubectlTimeout = 2 * time.Minute

func run(cmd *exec.Cmd) (string, error) {
	command := strings.Join(cmd.Args, " ")
	fmt.Fprintf(GinkgoWriter, "  > %s\n", command)
	output, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(output))
	if err != nil {
		return out, fmt.Errorf("%s failed: %s: %w", command, out, err)
	}
	return out, nil
}

func hasTimeoutFlag(args []string) bool {
	for _, a := range args {
		if a == "--timeout" || strings.HasPrefix(a, "--timeout=") {
			return true
		}
	}
	return false
}

func runWithTimeout(name string, args ...string) (string, error) {
	if hasTimeoutFlag(args) {
		cmd := exec.Command(name, args...)
		return run(cmd)
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	return run(cmd)
}

func kubectl(args ...string) (string, error) {
	return runWithTimeout("kubectl", args...)
}

func kubectlCtx(kctx string, args ...string) (string, error) {
	full := append([]string{"--context", kctx}, args...)
	return kubectl(full...)
}

func applyLiteral(kctx, yaml string) error {
	ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kctx, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	_, err := run(cmd)
	return err
}

func deleteLiteral(kctx, yaml string) error {
	ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kctx, "delete", "--ignore-not-found", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	_, err := run(cmd)
	return err
}

// ---- YAML generators ----

func managedClusterSetBindingYAML(ns string) string {
	return fmt.Sprintf(`apiVersion: cluster.open-cluster-management.io/v1beta2
kind: ManagedClusterSetBinding
metadata:
  name: %s
  namespace: %s
spec:
  clusterSet: %s`, managedClusterSetName, ns, managedClusterSetName)
}

func placementYAML(name, ns string) string {
	return fmt.Sprintf(`apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: %s
  namespace: %s
spec:
  tolerations:
  - key: cluster.open-cluster-management.io/unreachable
    operator: Exists
  - key: cluster.open-cluster-management.io/unavailable
    operator: Exists`, name, ns)
}

type gitOpsClusterOpts struct {
	name          string
	namespace     string
	placementName string
	agentEnabled  bool
	olmEnabled    bool
	olmSource     string
	olmSourceNS   string
	olmChannel    string
	olmSubName    string
	olmSubNS      string
}

func gitOpsClusterYAML(opts gitOpsClusterOpts) string {
	spec := fmt.Sprintf(`apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: %s
  namespace: %s
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: %s
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: %s`, opts.name, opts.namespace, argoCDNamespace, opts.placementName)

	spec += fmt.Sprintf(`
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: %t`, opts.agentEnabled)

	if opts.olmEnabled {
		spec += `
    olmSubscription:
      enabled: true`
		if opts.olmSubName != "" {
			spec += fmt.Sprintf(`
      name: %s`, opts.olmSubName)
		}
		if opts.olmSubNS != "" {
			spec += fmt.Sprintf(`
      namespace: %s`, opts.olmSubNS)
		}
		if opts.olmChannel != "" {
			spec += fmt.Sprintf(`
      channel: %s`, opts.olmChannel)
		}
		if opts.olmSource != "" {
			spec += fmt.Sprintf(`
      source: %s`, opts.olmSource)
		}
		if opts.olmSourceNS != "" {
			spec += fmt.Sprintf(`
      sourceNamespace: %s`, opts.olmSourceNS)
		}
	}

	return spec
}

func guestbookAppYAML(ns, destServer string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook
  namespace: %s
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps
    targetRevision: HEAD
    path: guestbook
  destination:
    server: %s
    namespace: guestbook
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    retry:
      limit: 5
      backoff:
        duration: 5s
        factor: 2
        maxDuration: 1m
    syncOptions:
    - CreateNamespace=true`, ns, destServer)
}

func appProjectYAML(ns string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: %s
spec:
  clusterResourceWhitelist:
  - group: '*'
    kind: '*'
  destinations:
  - namespace: '*'
    server: '*'
  sourceRepos:
  - '*'
  sourceNamespaces:
  - '*'`, ns)
}

func clusterAdminCRBYAML(saNamespace string) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: acm-openshift-gitops-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: acm-openshift-gitops-argocd-application-controller
  namespace: %s`, saNamespace)
}

func ensureArgoCDClusterAdmin(ctx, saNamespace string) {
	By(fmt.Sprintf("creating cluster-admin ClusterRoleBinding for ArgoCD (SA ns=%s) on %s", saNamespace, ctx))
	Expect(applyLiteral(ctx, clusterAdminCRBYAML(saNamespace))).To(Succeed())

	By("pre-creating guestbook namespace")
	Expect(applyLiteral(ctx, `apiVersion: v1
kind: Namespace
metadata:
  name: guestbook`)).To(Succeed())
}

// ---- Wait / assertion helpers ----

func eventuallyKubectl(ctx string, timeout, interval time.Duration, args ...string) AsyncAssertion {
	return Eventually(func(g Gomega) string {
		out, err := kubectlCtx(ctx, args...)
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, timeout, interval)
}

func waitForConditionTrue(ctx, resource, name, ns, condType string, timeout time.Duration) {
	By(fmt.Sprintf("waiting for %s/%s condition %s=True", resource, name, condType))
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(ctx, "get", resource, name, "-n", ns,
			"-o", fmt.Sprintf("jsonpath={.status.conditions[?(@.type=='%s')].status}", condType))
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, timeout, 5*time.Second).Should(Equal("True"))
}

func waitForResourceExists(ctx, resource, name, ns string, timeout time.Duration) {
	By(fmt.Sprintf("waiting for %s/%s in %s", resource, name, ns))
	Eventually(func(g Gomega) {
		_, err := kubectlCtx(ctx, "get", resource, name, "-n", ns)
		g.Expect(err).NotTo(HaveOccurred())
	}, timeout, 5*time.Second).Should(Succeed())
}

func waitForResourceGone(ctx, resource, name, ns string, timeout time.Duration) {
	By(fmt.Sprintf("waiting for %s/%s to be deleted from %s", resource, name, ns))
	Eventually(func(g Gomega) {
		_, err := kubectlCtx(ctx, "get", resource, name, "-n", ns)
		g.Expect(err).To(HaveOccurred())
		errMsg := err.Error()
		g.Expect(strings.Contains(errMsg, "NotFound") || strings.Contains(errMsg, "not found")).
			To(BeTrue(), "expected NotFound error, got: %s", errMsg)
	}, timeout, 5*time.Second).Should(Succeed())
}

func waitForPodPhase(ctx, ns, labelSelector, phase string, timeout time.Duration) {
	By(fmt.Sprintf("waiting for pod (%s) in %s to be %s", labelSelector, ns, phase))
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(ctx, "get", "pods", "-n", ns,
			"-l", labelSelector,
			"-o", "jsonpath={.items[0].status.phase}")
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, timeout, 5*time.Second).Should(Equal(phase))
}

func waitForDeploymentReady(ctx, ns, name string, timeout time.Duration) {
	By(fmt.Sprintf("waiting for deployment %s/%s to be ready", ns, name))
	Eventually(func(g Gomega) int {
		out, err := kubectlCtx(ctx, "get", "deployment", name, "-n", ns,
			"-o", "jsonpath={.status.availableReplicas}")
		g.Expect(err).NotTo(HaveOccurred())
		if out == "" {
			return 0
		}
		replicas, convErr := strconv.Atoi(strings.TrimSpace(out))
		g.Expect(convErr).NotTo(HaveOccurred(), "failed to parse availableReplicas: %s", out)
		return replicas
	}, timeout, 5*time.Second).Should(BeNumerically(">", 0))
}

func getJSONPath(ctx, resource, name, ns, jsonpath string) (string, error) {
	return kubectlCtx(ctx, "get", resource, name, "-n", ns, "-o", fmt.Sprintf("jsonpath=%s", jsonpath))
}

// ---- Scenario setup helpers ----

func createBaseResources() {
	By("creating ManagedClusterSetBinding")
	Expect(applyLiteral(hubContext, managedClusterSetBindingYAML(argoCDNamespace))).To(Succeed())

	By("creating Placement")
	Expect(applyLiteral(hubContext, placementYAML(placementName, argoCDNamespace))).To(Succeed())
}

func createGitOpsCluster(opts gitOpsClusterOpts) {
	By(fmt.Sprintf("creating GitOpsCluster %s/%s (agent=%t, olm=%t)", opts.namespace, opts.name, opts.agentEnabled, opts.olmEnabled))
	Expect(applyLiteral(hubContext, gitOpsClusterYAML(opts))).To(Succeed())
}

// ---- Spoke deployment verification helpers ----

func verifyAddonDeployed(timeout time.Duration) {
	By("verifying ManagedClusterAddOn exists for spoke")
	waitForResourceExists(hubContext, "managedclusteraddon", addonName, spokeName, timeout)

	By("verifying addon pod is running on spoke")
	waitForPodPhase(spokeContext, addonAgentNamespace, "app=gitops-addon", "Running", timeout)
}

func verifyArgoCDOnSpoke(timeout time.Duration) {
	By("verifying ArgoCD CR exists on spoke")
	waitForResourceExists(spokeContext, "argocd", "acm-openshift-gitops", argoCDNamespace, timeout)

	By("verifying ArgoCD application-controller pod is running on spoke")
	waitForPodPhase(spokeContext, argoCDNamespace,
		"app.kubernetes.io/name=acm-openshift-gitops-application-controller", "Running", timeout)
}

func verifyGitOpsClusterConditions(conditions []string, timeout time.Duration) {
	for _, cond := range conditions {
		waitForConditionTrue(hubContext, "gitopscluster", gitopsClusterName, argoCDNamespace, cond, timeout)
	}
}

func verifyGuestbookDeployed(ctx string, timeout time.Duration) {
	By(fmt.Sprintf("verifying guestbook deployment exists (%s)", ctx))

	appNS := argoCDNamespace
	if ctx == hubContext {
		appNS = localClusterName
	}

	Eventually(func(g Gomega) int {
		out, err := kubectlCtx(ctx, "get", "deployment", "guestbook-ui",
			"-n", "guestbook",
			"-o", "jsonpath={.status.availableReplicas}")
		if err != nil {
			appInfo, _ := kubectlCtx(ctx, "get", "application", "guestbook",
				"-n", appNS,
				"-o", "jsonpath=sync={.status.sync.status} health={.status.health.status} dest={.spec.destination.server}")
			fmt.Fprintf(GinkgoWriter, "  [diag] guestbook-ui not found; app(%s/%s): %s\n", ctx, appNS, appInfo)
		}
		g.Expect(err).NotTo(HaveOccurred())
		if out == "" {
			return 0
		}
		replicas, convErr := strconv.Atoi(strings.TrimSpace(out))
		g.Expect(convErr).NotTo(HaveOccurred(), "failed to parse availableReplicas: %s", out)
		return replicas
	}, timeout, 10*time.Second).Should(BeNumerically(">", 0))
}

func verifyNoOLMSubscription(subName, subNS string) {
	By("verifying no OLM subscription on spoke")
	_, err := kubectlCtx(spokeContext, "get", "subscription.operators.coreos.com", subName, "-n", subNS)
	Expect(err).To(HaveOccurred(), "OLM subscription should not exist on non-OCP cluster")
	errMsg := err.Error()
	Expect(errMsg).To(SatisfyAny(
		ContainSubstring("not found"),
		ContainSubstring("no matches for"),
		ContainSubstring("the server doesn't have a resource type"),
	), "expected 'not found' or missing CRD error but got: %v", err)
}

func verifyEmbeddedOperator(timeout time.Duration) {
	By("verifying embedded operator deployment on spoke")
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(spokeContext, "get", "deployment",
			"openshift-gitops-operator-controller-manager",
			"-n", operatorNamespace,
			"-o", "jsonpath={.status.availableReplicas}")
		g.Expect(err).NotTo(HaveOccurred())
		replicas, convErr := strconv.Atoi(out)
		g.Expect(convErr).NotTo(HaveOccurred(), "availableReplicas should be a number, got: %q", out)
		g.Expect(replicas).To(BeNumerically(">", 0), "expected at least 1 available replica")
	}, timeout, 5*time.Second).Should(Succeed())

	ensureOperatorInspectedCluster()
}

// ensureOperatorInspectedCluster restarts the spoke operator pod if InspectCluster
// failed at startup due to an RBAC race condition. The addon creates the operator's
// ClusterRoleBinding and Deployment simultaneously, so the operator may start before
// RBAC is propagated, causing InspectCluster to fail and preventing agent/route
// resource reconciliation.
func ensureOperatorInspectedCluster() {
	By("checking if spoke operator successfully inspected the cluster")
	logs, err := kubectlCtx(spokeContext, "logs", "deployment/openshift-gitops-operator-controller-manager",
		"-n", operatorNamespace, "--tail=50")
	if err != nil {
		return
	}
	if !strings.Contains(logs, "unable to inspect cluster") {
		return
	}

	By("restarting spoke operator to re-run InspectCluster after RBAC propagation")
	kubectlCtx(spokeContext, "delete", "pod",
		"-n", operatorNamespace,
		"-l", "control-plane=controller-manager",
		"--grace-period=0", "--force")

	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(spokeContext, "get", "deployment",
			"openshift-gitops-operator-controller-manager",
			"-n", operatorNamespace,
			"-o", "jsonpath={.status.availableReplicas}")
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, 3*time.Minute, 5*time.Second).Should(Equal("1"))

	By("verifying operator InspectCluster succeeded after restart")
	Eventually(func() bool {
		newLogs, err := kubectlCtx(spokeContext, "logs", "deployment/openshift-gitops-operator-controller-manager",
			"-n", operatorNamespace, "--tail=30")
		if err != nil {
			return false
		}
		return !strings.Contains(newLogs, "unable to inspect cluster")
	}, 2*time.Minute, 5*time.Second).Should(BeTrue())
}

func ensureHubPrincipalRunning() {
	By("ensuring hub principal pod is running")
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(hubContext, "get", "pods", "-n", argoCDNamespace,
			"-l", "app.kubernetes.io/name=openshift-gitops-agent-principal",
			"-o", "jsonpath={.items[0].status.phase}")
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, 3*time.Minute, 10*time.Second).Should(Equal("Running"))
}

func verifyAddOnDeploymentConfigEnvVar(clusterName, envKey, expectedValue string, timeout time.Duration) {
	By(fmt.Sprintf("verifying AddOnDeploymentConfig has %s=%s for %s", envKey, expectedValue, clusterName))
	jsonpath := fmt.Sprintf(`{.spec.customizedVariables[?(@.name=="%s")].value}`, envKey)
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(hubContext, "get", "addondeploymentconfig",
			"gitops-addon-config", "-n", clusterName,
			"-o", fmt.Sprintf("jsonpath=%s", jsonpath))
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, timeout, 5*time.Second).Should(Equal(expectedValue))
}

func verifyEnvironmentHealth(ctx string) {
	By("verifying no cross-namespace application controller conflicts")
	out, err := kubectlCtx(ctx, "get", "pods", "-A",
		"-l", "app.kubernetes.io/name=acm-openshift-gitops-application-controller",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}{' '}{end}")
	Expect(err).NotTo(HaveOccurred(), "kubectl failed during environment health check: %v", err)
	if out != "" {
		namespaces := strings.Fields(out)
		for _, ns := range namespaces {
			Expect(ns).To(Equal(argoCDNamespace),
				"ArgoCD application controller should only run in its designated namespace")
		}
	}
}

func verifyAgentPodRunning(timeout time.Duration) {
	By("verifying ArgoCD agent pod is running on spoke")
	waitForPodPhase(spokeContext, argoCDNamespace,
		"app.kubernetes.io/name=acm-openshift-gitops-agent-agent", "Running", timeout)
}

func verifyPrincipalServerAddress(timeout time.Duration) string {
	By("discovering principal server address")
	var serverAddr, serverPort string
	Eventually(func(g Gomega) {
		addr, err := getJSONPath(hubContext, "gitopscluster", gitopsClusterName, argoCDNamespace,
			"{.spec.gitopsAddon.argoCDAgent.serverAddress}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(addr).NotTo(BeEmpty())
		serverAddr = addr

		port, err := getJSONPath(hubContext, "gitopscluster", gitopsClusterName, argoCDNamespace,
			"{.spec.gitopsAddon.argoCDAgent.serverPort}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(port).NotTo(BeEmpty())
		serverPort = port
	}, timeout, 5*time.Second).Should(Succeed())

	destServer := fmt.Sprintf("https://%s:%s?agentName=%s", serverAddr, serverPort, spokeName)
	fmt.Fprintf(GinkgoWriter, "Agent destination server: %s\n", destServer)
	return destServer
}

func verifyClusterSecret(timeout time.Duration) {
	By("verifying cluster secret exists on hub with valid agent server URL")
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(hubContext, "get", "secret",
			fmt.Sprintf("cluster-%s", spokeName),
			"-n", argoCDNamespace,
			"-o", "jsonpath={.data.server}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).NotTo(BeEmpty())

		decoded, err := base64.StdEncoding.DecodeString(out)
		g.Expect(err).NotTo(HaveOccurred(), "server value should be valid base64")
		serverURL := string(decoded)
		g.Expect(serverURL).To(HavePrefix("https://"), "server URL should use https")
		g.Expect(serverURL).To(ContainSubstring("agentName="), "server URL should contain agentName parameter")
	}, timeout, 5*time.Second).Should(Succeed())
}

// ---- Spoke application deployment helpers ----

func deployGuestbookApp(timeout time.Duration) {
	ensureArgoCDClusterAdmin(spokeContext, argoCDNamespace)

	By("creating AppProject default on spoke")
	Expect(applyLiteral(spokeContext, appProjectYAML(argoCDNamespace))).To(Succeed())

	By("creating guestbook Application on spoke")
	Expect(applyLiteral(spokeContext, guestbookAppYAML(argoCDNamespace, "https://kubernetes.default.svc"))).To(Succeed())

	verifyGuestbookDeployed(spokeContext, timeout)
}

func deployGuestbookAgentMode(timeout time.Duration) {
	ensureArgoCDClusterAdmin(spokeContext, argoCDNamespace)

	destServer := verifyPrincipalServerAddress(3 * time.Minute)

	ensureHubPrincipalRunning()

	By("patching default AppProject in ArgoCD namespace on hub to allow source namespaces")
	Expect(applyLiteral(hubContext, appProjectYAML(argoCDNamespace))).To(Succeed())

	By("ensuring spoke namespace on hub")
	Expect(applyLiteral(hubContext, fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s`, spokeName))).To(Succeed())

	By("creating AppProject on hub in spoke namespace")
	Expect(applyLiteral(hubContext, appProjectYAML(spokeName))).To(Succeed())

	By("ensuring default AppProject exists on spoke (agent should propagate; create as fallback)")
	Eventually(func() error {
		_, err := kubectlCtx(spokeContext, "get", "appproject", "default",
			"-n", argoCDNamespace)
		if err != nil {
			applyLiteral(spokeContext, appProjectYAML(argoCDNamespace))
		}
		return err
	}, 2*time.Minute, 10*time.Second).Should(Succeed())

	By("creating guestbook Application on hub in spoke namespace")
	Expect(applyLiteral(hubContext, guestbookAppYAML(spokeName, destServer))).To(Succeed())

	By("waiting for agent to propagate guestbook Application to spoke")
	Eventually(func() error {
		_, err := kubectlCtx(spokeContext, "get", "application", "guestbook",
			"-n", argoCDNamespace)
		return err
	}, 5*time.Minute, 15*time.Second).Should(Succeed())

	By("verifying guestbook deployed on spoke")
	verifyGuestbookDeployed(spokeContext, timeout)

	By("verifying Application sync status reflected back to hub")
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(hubContext, "get", "application", "guestbook",
			"-n", spokeName,
			"-o", "jsonpath={.status.sync.status}")
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, 3*time.Minute, 10*time.Second).Should(Equal("Synced"))
}

// ---- Local-cluster verification helpers ----
// Mirrors verify_local_cluster_working() from test-scenarios.sh.
// Every scenario targets BOTH the spoke AND local-cluster via Placement.

func verifyLocalClusterAddon(timeout time.Duration) {
	By("verifying ManagedClusterAddOn exists for local-cluster")
	waitForResourceExists(hubContext, "managedclusteraddon", addonName, localClusterName, timeout)
}

func verifyLocalClusterArgoCDDeployed(timeout time.Duration) {
	By("verifying ArgoCD CR acm-openshift-gitops in local-cluster namespace on hub")
	waitForResourceExists(hubContext, "argocd", "acm-openshift-gitops", localClusterName, timeout)
}

func verifyNoDuplicateArgoCDOnHub() {
	By("verifying no duplicate acm-openshift-gitops in openshift-gitops on hub")
	out, err := kubectlCtx(hubContext, "get", "argocd", "-n", argoCDNamespace,
		"-o", "jsonpath={range .items[*]}{.metadata.name}{' '}{end}")
	Expect(err).NotTo(HaveOccurred(), "kubectl failed checking ArgoCD on hub: %v", err)
	names := strings.Fields(out)
	for _, name := range names {
		Expect(name).NotTo(Equal("acm-openshift-gitops"),
			"acm-openshift-gitops should NOT exist in openshift-gitops namespace on hub")
	}
}

func verifyLocalClusterGuestbook(isAgentMode bool, timeout time.Duration) {
	if isAgentMode {
		By("verifying argocd-agent-client-tls secret for local-cluster")
		Eventually(func(g Gomega) {
			_, err := kubectlCtx(hubContext, "get", "secret", "argocd-agent-client-tls",
				"-n", localClusterName)
			g.Expect(err).NotTo(HaveOccurred())
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("verifying ArgoCD app controller is running in local-cluster namespace")
		waitForPodPhase(hubContext, localClusterName,
			"app.kubernetes.io/name=acm-openshift-gitops-application-controller", "Running", 5*time.Minute)
	}

	ensureArgoCDClusterAdmin(hubContext, localClusterName)

	By("creating AppProject in local-cluster namespace")
	Expect(applyLiteral(hubContext, appProjectYAML(localClusterName))).To(Succeed())

	By("creating guestbook Application in local-cluster namespace")
	Expect(applyLiteral(hubContext, guestbookAppYAML(localClusterName, "https://kubernetes.default.svc"))).To(Succeed())

	By("verifying guestbook-ui deployment on local-cluster (hub)")
	verifyGuestbookDeployed(hubContext, timeout)

	By("verifying guestbook Application sync status on local-cluster")
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(hubContext, "get", "application", "guestbook",
			"-n", localClusterName,
			"-o", "jsonpath={.status.sync.status}")
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, 3*time.Minute, 10*time.Second).Should(Equal("Synced"))
}

func verifyLocalClusterControllerNamespace() {
	By("verifying guestbook on local-cluster managed by local-cluster controller")
	Eventually(func(g Gomega) string {
		out, _ := kubectlCtx(hubContext, "get", "application", "guestbook",
			"-n", localClusterName,
			"-o", "jsonpath={.status.controllerNamespace}")
		return out
	}, 1*time.Minute, 5*time.Second).Should(Equal(localClusterName))
}

func verifyLocalClusterEnvironmentHealth() {
	By("verifying no cross-namespace application controller conflicts on hub (local-cluster)")
	out, err := kubectlCtx(hubContext, "get", "pods", "-A",
		"-l", "app.kubernetes.io/name=acm-openshift-gitops-application-controller",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}{' '}{end}")
	Expect(err).NotTo(HaveOccurred(), "kubectl failed during local-cluster environment health check: %v", err)
	if out != "" {
		namespaces := strings.Fields(out)
		for _, ns := range namespaces {
			Expect(ns).To(Equal(localClusterName),
				"local-cluster ArgoCD application controller should only run in local-cluster namespace")
		}
	}
}

// ---- Cleanup helpers ----
// Mirrors cleanup_scenario() from test-scenarios.sh.
// Order: Placement → agent resources → Policy propagation wait → MCA (spoke) → MCA (local-cluster) → GitOpsCluster → ManagedClusterSetBinding

func cleanupGuestbookResources(isAgentMode bool) {
	By("cleaning up guestbook resources on spoke")
	kubectlCtx(spokeContext, "delete", "application", "guestbook", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(spokeContext, "delete", "appproject", "default", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(spokeContext, "delete", "namespace", "guestbook", "--ignore-not-found", "--wait=false")
	kubectlCtx(spokeContext, "delete", "clusterrolebinding", "acm-openshift-gitops-cluster-admin", "--ignore-not-found")
	if isAgentMode {
		kubectlCtx(hubContext, "delete", "application", "guestbook", "-n", spokeName, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "appproject", "default", "-n", spokeName, "--ignore-not-found")
	}

	By("cleaning up guestbook resources on local-cluster (hub)")
	kubectlCtx(hubContext, "delete", "application", "guestbook", "-n", localClusterName, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "appproject", "default", "-n", localClusterName, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "namespace", "guestbook", "--ignore-not-found", "--wait=false")
	kubectlCtx(hubContext, "delete", "clusterrolebinding", "acm-openshift-gitops-cluster-admin", "--ignore-not-found")
}

func scenarioCleanup(opts gitOpsClusterOpts) {
	By("--- Starting scenario cleanup (proper order per test-scenarios.sh) ---")

	By("1. Deleting Placement (prevents controller from recreating addon)")
	kubectlCtx(hubContext, "delete", "placement", opts.placementName, "-n", argoCDNamespace, "--ignore-not-found")

	By("2. Deleting hub-side agent resources if applicable")
	if opts.agentEnabled {
		kubectlCtx(hubContext, "delete", "applications.argoproj.io", "--all", "-n", spokeName, "--ignore-not-found", "--wait=false")
		kubectlCtx(hubContext, "delete", "appproject", "--all", "-n", spokeName, "--ignore-not-found", "--wait=false")
	}

	By("3. Deleting Policy and PlacementBinding (stops enforcement on managed cluster)")
	policyName := opts.name + "-argocd-policy"
	bindingName := opts.name + "-argocd-policy-binding"
	kubectlCtx(hubContext, "delete", "policy.policy.open-cluster-management.io", policyName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")
	kubectlCtx(hubContext, "delete", "placementbinding.policy.open-cluster-management.io", bindingName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")

	By("3a. Waiting for replicated policy to be removed from spoke namespace")
	waitForResourceGone(hubContext, "policy.policy.open-cluster-management.io", policyName, spokeName, 2*time.Minute)

	By("3b. Waiting for replicated policy to be removed from local-cluster namespace")
	waitForResourceGone(hubContext, "policy.policy.open-cluster-management.io", policyName, localClusterName, 2*time.Minute)

	By("4. Deleting ManagedClusterAddOn for spoke (triggers pre-delete cleanup Job)")
	kubectlCtx(hubContext, "delete", "managedclusteraddon", addonName, "-n", spokeName,
		"--ignore-not-found", "--timeout=300s")

	By("5. Deleting ManagedClusterAddOn for local-cluster")
	kubectlCtx(hubContext, "delete", "managedclusteraddon", addonName, "-n", localClusterName,
		"--ignore-not-found", "--timeout=300s")

	By("6. Deleting GitOpsCluster")
	kubectlCtx(hubContext, "delete", "gitopscluster", opts.name, "-n", argoCDNamespace, "--ignore-not-found")

	By("7. Deleting ManagedClusterSetBinding")
	deleteLiteral(hubContext, managedClusterSetBindingYAML(argoCDNamespace))

	By("8. Cleaning up orphaned local-cluster ArgoCD resources")
	kubectlCtx(hubContext, "delete", "argocd", "acm-openshift-gitops", "-n", localClusterName, "--ignore-not-found", "--wait=false")

	By("--- Scenario cleanup commands complete ---")
}

func verifyHubCleanup(opts gitOpsClusterOpts) {
	By("verifying GitOpsCluster is gone from hub")
	waitForResourceGone(hubContext, "gitopscluster", opts.name, argoCDNamespace, 2*time.Minute)

	By("verifying ManagedClusterAddOn for spoke is gone from hub")
	waitForResourceGone(hubContext, "managedclusteraddon", addonName, spokeName, 2*time.Minute)

	By("verifying ManagedClusterAddOn for local-cluster is gone from hub")
	waitForResourceGone(hubContext, "managedclusteraddon", addonName, localClusterName, 2*time.Minute)
}

func verifySpokeCleanup() {
	By("verifying ArgoCD CR is removed from spoke")
	waitForResourceGone(spokeContext, "argocd", "acm-openshift-gitops", argoCDNamespace, 5*time.Minute)

	By("verifying operator deployment is removed from spoke")
	waitForResourceGone(spokeContext, "deployment", "openshift-gitops-operator-controller-manager", operatorNamespace, 5*time.Minute)
}

func verifyLocalClusterCleanup() {
	By("verifying ArgoCD CR is removed from local-cluster namespace on hub")
	waitForResourceGone(hubContext, "argocd", "acm-openshift-gitops", localClusterName, 5*time.Minute)
}

func safeCleanup(opts gitOpsClusterOpts) {
	kubectlCtx(hubContext, "delete", "placement", opts.placementName, "-n", argoCDNamespace, "--ignore-not-found")
	policyName := opts.name + "-argocd-policy"
	bindingName := opts.name + "-argocd-policy-binding"
	kubectlCtx(hubContext, "delete", "policy.policy.open-cluster-management.io", policyName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")
	kubectlCtx(hubContext, "delete", "placementbinding.policy.open-cluster-management.io", bindingName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")
	kubectlCtx(hubContext, "delete", "application", "guestbook", "-n", localClusterName, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "appproject", "default", "-n", localClusterName, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "namespace", "guestbook", "--ignore-not-found", "--wait=false")
	if opts.agentEnabled {
		kubectlCtx(hubContext, "delete", "application", "guestbook", "-n", spokeName, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "appproject", "--all", "-n", spokeName, "--ignore-not-found")
	}
	kubectlCtx(spokeContext, "delete", "application", "guestbook", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(spokeContext, "delete", "appproject", "default", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(spokeContext, "delete", "namespace", "guestbook", "--ignore-not-found", "--wait=false")
	kubectlCtx(spokeContext, "delete", "clusterrolebinding", "acm-openshift-gitops-cluster-admin", "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "managedclusteraddon", addonName, "-n", spokeName, "--ignore-not-found", "--timeout=120s")
	kubectlCtx(hubContext, "delete", "managedclusteraddon", addonName, "-n", localClusterName, "--ignore-not-found", "--timeout=120s")
	kubectlCtx(hubContext, "delete", "argocd", "acm-openshift-gitops", "-n", localClusterName, "--ignore-not-found", "--wait=false")
	kubectlCtx(hubContext, "delete", "gitopscluster", opts.name, "-n", argoCDNamespace, "--ignore-not-found")
	deleteLiteral(hubContext, managedClusterSetBindingYAML(argoCDNamespace))
}
