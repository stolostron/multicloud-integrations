//go:build e2e

package gitopsaddon_e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// placementYAML selects every cluster in the ManagedClusterSet, INCLUDING local-cluster. Only
// use this for placements that are allowed to target local-cluster (e.g. the ApplicationSet
// placement) - never for the GitOpsCluster's own placementRef (see
// placementExcludingLocalClusterYAML).
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

// placementExcludingLocalClusterYAML selects every cluster in the ManagedClusterSet EXCEPT
// local-cluster. This must be used for the GitOpsCluster's own placementRef: local-cluster
// already has its own ArgoCD instance (hosting the argocd-agent principal) and is never an
// addon-install target - the hub controller now rejects (hard failure) any GitOpsCluster whose
// resolved Placement includes local-cluster (see gitopscluster_controller.go).
func placementExcludingLocalClusterYAML(name, ns string) string {
	return fmt.Sprintf(`apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: %s
  namespace: %s
spec:
  predicates:
  - requiredClusterSelector:
      labelSelector:
        matchExpressions:
        - key: local-cluster
          operator: NotIn
          values:
          - "true"
  tolerations:
  - key: cluster.open-cluster-management.io/unreachable
    operator: Exists
  - key: cluster.open-cluster-management.io/unavailable
    operator: Exists`, name, ns)
}

func placementWithClusterYAML(name, ns, clusterName string) string {
	return fmt.Sprintf(`apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: %s
  namespace: %s
spec:
  predicates:
  - requiredClusterSelector:
      labelSelector:
        matchLabels:
          name: %s
  tolerations:
  - key: cluster.open-cluster-management.io/unreachable
    operator: Exists
  - key: cluster.open-cluster-management.io/unavailable
    operator: Exists`, name, ns, clusterName)
}

type gitOpsClusterOpts struct {
	name          string
	namespace     string
	argoNamespace string // ArgoCD instance namespace (defaults to argoCDNamespace constant)
	placementName string
	agentEnabled  bool
	agentMode     string // "managed" or "autonomous" (empty defaults to managed)
	olmEnabled    bool
	olmSource     string
	olmSourceNS   string
	olmChannel    string
	olmSubName    string
	olmSubNS      string
}

func gitOpsClusterYAML(opts gitOpsClusterOpts) string {
	argoNs := opts.argoNamespace
	if argoNs == "" {
		argoNs = opts.namespace
	}
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
    name: %s`, opts.name, opts.namespace, argoNs, opts.placementName)

	spec += fmt.Sprintf(`
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: %t`, opts.agentEnabled)

	if opts.agentMode != "" {
		spec += fmt.Sprintf(`
      mode: %s`, opts.agentMode)
	}

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
	// IMPORTANT: destinations must include name: '*' in addition to server: '*'.
	// The argocd-agent principal only propagates AppProjects to managed agents
	// when the destinations include a name wildcard.  Without name: '*', the
	// principal skips propagation and agents can't find the AppProject for
	// Applications that use destination.name (e.g. ApplicationSet-generated
	// apps with destination.name: '{{name}}').  This matches the settings used
	// by test-scenarios.sh's ensure_default_appproject_for_agents helper.
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
  - name: '*'
    namespace: '*'
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

func deleteMCAWithFallback(ctx, addonName, ns string) {
	_, err := kubectlCtx(ctx, "delete", "managedclusteraddon", addonName, "-n", ns,
		"--ignore-not-found", "--timeout=180s")
	if err != nil {
		By(fmt.Sprintf("MCA delete timed out for %s in %s — stripping finalizers", addonName, ns))
		kubectlCtx(ctx, "patch", "managedclusteraddon", addonName, "-n", ns,
			"--type=merge", "-p", `{"metadata":{"finalizers":[]}}`)
		kubectlCtx(ctx, "delete", "managedclusteraddon", addonName, "-n", ns,
			"--ignore-not-found", "--timeout=60s")
	}
}

// waitForPodPhase waits for a pod matching labelSelector to reach phase and STAY there across two
// consecutive samples (same phase, same restart count) 5 seconds apart, not just to be observed
// there once. A crash-looping container (start -> fatal error -> exit -> restart) can genuinely
// report status.phase=Running for the brief window between container start and the fatal exit -
// long enough that a single-sample poll has a real chance of sampling mid-flicker and reporting a
// false pass while the pod is actually stuck restarting forever. Requiring the phase AND restart
// count to be identical across two samples closes that gap: any restart in between changes the
// restart count and forces another round.
func waitForPodPhase(ctx, ns, labelSelector, phase string, timeout time.Duration) {
	By(fmt.Sprintf("waiting for pod (%s) in %s to be stably %s (two consecutive stable samples)", labelSelector, ns, phase))
	type sample struct {
		phase    string
		restarts string
	}
	var last *sample
	deadline := time.Now().Add(timeout)
	for {
		out, err := kubectlCtx(ctx, "get", "pods", "-n", ns, "-l", labelSelector,
			"-o", "jsonpath={.items[0].status.phase},{.items[0].status.containerStatuses[0].restartCount}")
		var cur *sample
		if err == nil {
			parts := strings.SplitN(out, ",", 2)
			s := sample{phase: parts[0]}
			if len(parts) > 1 {
				s.restarts = parts[1]
			}
			cur = &s
		}
		if cur != nil && cur.phase == phase && last != nil && *last == *cur {
			return
		}
		last = cur
		if time.Now().After(deadline) {
			curPhase := "<error fetching pod>"
			if cur != nil {
				curPhase = cur.phase
			}
			Fail(fmt.Sprintf("pod (%s) in %s did not stably reach phase %s within %s (last observed phase: %s, err: %v)",
				labelSelector, ns, phase, timeout, curPhase, err))
		}
		time.Sleep(5 * time.Second)
	}
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

// appsetPlacementName is a Placement used only by ApplicationSets - it includes local-cluster
// alongside the real managed cluster(s), unlike placementName (the GitOpsCluster's own
// placementRef) which must exclude local-cluster.
const appsetPlacementName = placementName + "-appset"

func createBaseResources() {
	By("creating ManagedClusterSetBinding")
	Expect(applyLiteral(hubContext, managedClusterSetBindingYAML(argoCDNamespace))).To(Succeed())

	By("creating Placement (addon-install target, excludes local-cluster)")
	Expect(applyLiteral(hubContext, placementExcludingLocalClusterYAML(placementName, argoCDNamespace))).To(Succeed())

	By("creating Placement (ApplicationSet target, includes local-cluster - hybrid mode)")
	Expect(applyLiteral(hubContext, placementYAML(appsetPlacementName, argoCDNamespace))).To(Succeed())
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

	// Applications always live in argoCDNamespace now - local-cluster (hybrid mode) has no
	// separate namespace to reconcile in, same as any other in-cluster ArgoCD destination.
	appNS := argoCDNamespace

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

	patchOperatorClusterRoleForUpstream(spokeContext)
	ensureOperatorInspectedCluster()
}

// patchOperatorClusterRoleForUpstream adds argocdexports RBAC to the operator ClusterRole.
// The embedded chart's ClusterRole is extracted from the Red Hat CSV which removed argocdexports.
// The upstream argocd-operator (used in e2e) still watches ArgoCDExport resources and needs
// this permission. Without it, the operator's informer cache fails to sync and it never
// reconciles ArgoCD CRs. This is e2e-only — the Red Hat operator does not need this.
func patchOperatorClusterRoleForUpstream(ctx string) {
	By("patching operator ClusterRole with argocdexports for upstream argocd-operator")
	_, _ = kubectlCtx(ctx, "patch", "clusterrole", "openshift-gitops-operator-manager-role",
		"--type=json",
		`-p=[{"op":"add","path":"/rules/-","value":{"apiGroups":["argoproj.io"],"resources":["argocdexports","argocdexports/finalizers"],"verbs":["create","delete","get","list","patch","update","watch"]}}]`)
	By("restarting operator pod to pick up patched ClusterRole")
	kubectlCtx(ctx, "delete", "pod", "-n", operatorNamespace,
		"-l", "control-plane=gitops-operator", "--grace-period=0", "--force")
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(ctx, "get", "deployment",
			"openshift-gitops-operator-controller-manager",
			"-n", operatorNamespace,
			"-o", "jsonpath={.status.availableReplicas}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(Equal("1"))
	}, 2*time.Minute, 5*time.Second).Should(Succeed())
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
	By("ensuring hub principal pod is running and Ready")
	// Check pod Ready condition (not just phase). A pod in CrashLoopBackOff has
	// phase=Running but Ready=False. If the principal is crashing because
	// argocd-agent-resource-proxy-tls is missing, this check catches it early.
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(hubContext, "get", "pods", "-n", argoCDNamespace,
			"-l", "app.kubernetes.io/name=openshift-gitops-agent-principal",
			"-o", `jsonpath={.items[0].status.conditions[?(@.type=="Ready")].status}`)
		g.Expect(err).NotTo(HaveOccurred())
		if out != "True" {
			// Log why the pod is not ready to aid in debugging
			logs, _ := kubectlCtx(hubContext, "logs", "-n", argoCDNamespace,
				"-l", "app.kubernetes.io/name=openshift-gitops-agent-principal", "--tail=10")
			fmt.Printf("[principal-ready-check] not yet Ready (status=%q); last logs:\n%s\n", out, logs)
		}
		return out
	}, 5*time.Minute, 10*time.Second).Should(Equal("True"),
		"hub principal pod should be Ready — if it keeps crashing, check for missing argocd-agent-resource-proxy-tls secret")
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
	// The resource proxy service is created by setup_env.sh to mirror the Red Hat
	// OpenShift GitOps operator behaviour.  Verify it exists before checking the
	// cluster secret URL so that any setup failure is surfaced here rather than
	// causing a confusing "sync=Unknown" failure later in deployGuestbookAgentMode.
	By("verifying resource proxy service exists in hub ArgoCD namespace")
	Eventually(func(g Gomega) {
		_, err := kubectlCtx(hubContext, "get", "service",
			"openshift-gitops-agent-principal-resource-proxy",
			"-n", argoCDNamespace)
		g.Expect(err).NotTo(HaveOccurred(),
			"resource proxy service should exist (created by setup_env.sh)")
	}, 2*time.Minute, 5*time.Second).Should(Succeed())

	By("verifying cluster secret exists on hub with resource proxy server URL")
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
		// With the resource proxy service present, the hub controller should choose
		// the in-cluster resource-proxy URL over the external NodePort fallback.
		g.Expect(serverURL).To(ContainSubstring("resource-proxy"),
			"server URL should use the resource proxy service (not NodePort fallback)")
	}, timeout, 5*time.Second).Should(Succeed())

	By("verifying cluster secret has skip-reconcile annotation")
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(hubContext, "get", "secret",
			fmt.Sprintf("cluster-%s", spokeName),
			"-n", argoCDNamespace,
			"-o", "jsonpath={.metadata.annotations.argocd\\.argoproj\\.io/skip-reconcile}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(Equal("true"),
			"agent cluster secret must have argocd.argoproj.io/skip-reconcile=true for hybrid mode")
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

// guestbookApplicationSetYAML builds a guestbook-generating ApplicationSet named
// "<namePrefix>-guestbook-appset", whose clusterDecisionResource generator reads
// PlacementDecisions from generatorPlacementName. namePrefix and generatorPlacementName are
// deliberately separate: the generator must target appsetPlacementName (includes local-cluster),
// while the AppSet's own name keeps using the addon placementName for continuity with existing
// cleanup references.
func guestbookApplicationSetYAML(namePrefix, generatorPlacementName, ns string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: %s-guestbook-appset
  namespace: %s
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: acm-placement
        labelSelector:
          matchLabels:
            cluster.open-cluster-management.io/placement: %s
        requeueAfterSeconds: 30
  template:
    metadata:
      name: '{{name}}-guestbook'
    spec:
      project: default
      source:
        repoURL: https://github.com/argoproj/argocd-example-apps
        targetRevision: HEAD
        path: guestbook
      destination:
        name: '{{name}}'
        namespace: guestbook
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
        syncOptions:
          - CreateNamespace=true`, namePrefix, ns, generatorPlacementName)
}

func deployGuestbookAgentMode(timeout time.Duration) {
	ensureArgoCDClusterAdmin(spokeContext, argoCDNamespace)

	ensureHubPrincipalRunning()

	// In agent mode, the controller creates agent-URL cluster secrets (cluster-<name>)
	// alongside legacy import secrets (<name>-application-manager-cluster-secret).
	// The ApplicationSet controller rejects apps when two secrets share the same cluster
	// name but different server URLs. Remove the legacy secrets so only the agent
	// secrets remain.
	By("removing legacy cluster secrets to avoid duplicate cluster name conflicts")
	kubectlCtx(hubContext, "delete", "secret",
		spokeName+"-application-manager-cluster-secret", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "secret",
		localClusterName+"-application-manager-cluster-secret", "-n", argoCDNamespace, "--ignore-not-found")

	By("patching default AppProject in ArgoCD namespace on hub to allow source namespaces")
	Expect(applyLiteral(hubContext, appProjectYAML(argoCDNamespace))).To(Succeed())

	By("ensuring default AppProject exists on spoke (agent should propagate; create as fallback)")
	Eventually(func() error {
		_, err := kubectlCtx(spokeContext, "get", "appproject", "default",
			"-n", argoCDNamespace)
		if err != nil {
			applyLiteral(spokeContext, appProjectYAML(argoCDNamespace))
		}
		return err
	}, 2*time.Minute, 10*time.Second).Should(Succeed())

	By("ensuring acm-placement ConfigMap exists for clusterDecisionResource generator")
	Expect(applyLiteral(hubContext, fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: acm-placement
  namespace: %s
data:
  apiVersion: cluster.open-cluster-management.io/v1beta1
  kind: placementdecisions
  statusListKey: decisions
  matchKey: clusterName`, argoCDNamespace))).To(Succeed())

	By("ensuring ApplicationSet controller can read PlacementDecisions")
	Expect(applyLiteral(hubContext, `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: appset-placement-reader
rules:
- apiGroups: ["cluster.open-cluster-management.io"]
  resources: ["placementdecisions"]
  verbs: ["get", "list", "watch"]`)).To(Succeed())
	Expect(applyLiteral(hubContext, fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: appset-placement-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: appset-placement-reader
subjects:
- kind: ServiceAccount
  name: openshift-gitops-argocd-application-controller
  namespace: %s
- kind: ServiceAccount
  name: openshift-gitops-applicationset-controller
  namespace: %s`, argoCDNamespace, argoCDNamespace))).To(Succeed())

	appsetName := placementName + "-guestbook-appset"
	appName := spokeName + "-guestbook"
	localClusterAppName := localClusterName + "-guestbook"

	By("waiting for application-controller (includes ApplicationSet) to be ready on hub")
	Eventually(func(g Gomega) int {
		out, err := kubectlCtx(hubContext, "get", "statefulset",
			"openshift-gitops-application-controller", "-n", argoCDNamespace,
			"-o", "jsonpath={.status.readyReplicas}")
		g.Expect(err).NotTo(HaveOccurred())
		if out == "" {
			return 0
		}
		var n int
		fmt.Sscanf(out, "%d", &n)
		return n
	}, 5*time.Minute, 10*time.Second).Should(BeNumerically(">=", 1))

	By("checking PlacementDecision for placement")
	pdOut, _ := kubectlCtx(hubContext, "get", "placementdecision", "-n", argoCDNamespace,
		"-l", "cluster.open-cluster-management.io/placement="+appsetPlacementName,
		"-o", "jsonpath={range .items[*]}{.metadata.name}: {.status.decisions}{','}{end}")
	fmt.Fprintf(GinkgoWriter, "  [diag] PlacementDecisions: %s\n", pdOut)

	By("creating guestbook ApplicationSet on hub")
	Expect(applyLiteral(hubContext, guestbookApplicationSetYAML(placementName, appsetPlacementName, argoCDNamespace))).To(Succeed())

	By(fmt.Sprintf("waiting for ApplicationSet to generate %s (spoke)", appName))
	waitForResourceExists(hubContext, "application", appName, argoCDNamespace, 8*time.Minute)

	By(fmt.Sprintf("waiting for ApplicationSet to generate %s (local-cluster)", localClusterAppName))
	waitForResourceExists(hubContext, "application", localClusterAppName, argoCDNamespace, 3*time.Minute)

	By("waiting for principal to dispatch cluster1-guestbook to spoke agent")
	// In agent mode the principal creates the Application in openshift-gitops on the spoke.
	// If this never appears, the principal has not dispatched to the cluster1 agent — likely
	// because the gRPC connection is not established.  Fail fast with diagnostic info rather
	// than waiting the full 15-min guestbook timeout with no signal.
	Eventually(func(g Gomega) {
		_, err := kubectlCtx(spokeContext, "get", "application", appName, "-n", argoCDNamespace)
		if err != nil {
			// Gather diagnostics to understand the dispatch failure
			hubApp, _ := kubectlCtx(hubContext, "get", "application", appName,
				"-n", argoCDNamespace,
				"-o", "jsonpath=sync={.status.sync.status} health={.status.health.status}")
			agentPhase, _ := kubectlCtx(spokeContext, "get", "pods", "-n", argoCDNamespace,
				"-l", "app.kubernetes.io/name=acm-openshift-gitops-agent-agent",
				"-o", "jsonpath={.items[0].status.phase}")
			// Last 20 lines of agent pod logs (shows gRPC connect/disconnect events)
			agentLogs, _ := kubectlCtx(spokeContext, "logs", "-n", argoCDNamespace,
				"-l", "app.kubernetes.io/name=acm-openshift-gitops-agent-agent",
				"--tail=20")
			// Principal logs for connection events
			principalLogs, _ := kubectlCtx(hubContext, "logs", "-n", argoCDNamespace,
				"-l", "app.kubernetes.io/name=openshift-gitops-agent-principal",
				"--tail=20")
			fmt.Fprintf(GinkgoWriter,
				"  [dispatch-diag] hub app: %s; agent phase: %s\n  [agent-logs]\n%s\n  [principal-logs]\n%s\n",
				hubApp, agentPhase, agentLogs, principalLogs)
		}
		g.Expect(err).NotTo(HaveOccurred(),
			"Application %s should appear in openshift-gitops on spoke once principal dispatches it", appName)
	}, 3*time.Minute, 10*time.Second).Should(Succeed(),
		"Principal did not dispatch %s to spoke agent within 3 minutes — check [dispatch-diag] above", appName)

	By("waiting for agent to propagate guestbook to spoke")
	Eventually(func(g Gomega) int {
		out, err := kubectlCtx(spokeContext, "get", "deployment", "guestbook-ui",
			"-n", "guestbook",
			"-o", "jsonpath={.status.availableReplicas}")
		if err != nil {
			appHubInfo, _ := kubectlCtx(hubContext, "get", "application", appName,
				"-n", argoCDNamespace,
				"-o", "jsonpath=sync={.status.sync.status} health={.status.health.status}")
			appSpokeInfo, _ := kubectlCtx(spokeContext, "get", "application",
				appName, "-n", argoCDNamespace,
				"-o", "jsonpath=sync={.status.sync.status} health={.status.health.status} message={.status.conditions[0].message}")
			fmt.Fprintf(GinkgoWriter,
				"  [diag] guestbook-ui not found; hub app: %s; spoke app: %s\n",
				appHubInfo, appSpokeInfo)
		}
		g.Expect(err).NotTo(HaveOccurred())
		if out == "" {
			return 0
		}
		replicas, convErr := strconv.Atoi(strings.TrimSpace(out))
		g.Expect(convErr).NotTo(HaveOccurred())
		return replicas
	}, timeout, 10*time.Second).Should(BeNumerically(">", 0))

	// The spoke-side guestbook-ui check above only proves the agent successfully deployed the
	// app - it says nothing about whether the principal actually dispatched status back to the
	// hub. A broken agent<->principal connection would still let the spoke half of this test
	// pass. This is the real functional proof for managed mode: the hub's own copy of the
	// dispatched Application must receive a REAL (non-empty, non-Unknown) status update -
	// proving the full dispatch+report round trip actually works. Deliberately does not require
	// the hub's value to equal the spoke's value at this instant: both progress independently
	// (observed live: the spoke can advance to a newer resourceVersion while the hub is still
	// catching up on an older one), so racing them for exact equality chases a moving target.
	// What matters is that the hub got SOME real update, not that they're byte-identical right
	// now - and this also does not require a hardcoded "Synced": the app's actual GitOps outcome
	// is a property of the demo repo/content (or environment quirks like restricted-SCC on OCP),
	// not of whether this reporting pipeline works.
	By("waiting for the spoke Application's sync status to settle to a real (non-Unknown) value")
	var spokeSync string
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(spokeContext, "get", "application", appName,
			"-n", argoCDNamespace, "-o", "jsonpath={.status.sync.status}")
		g.Expect(err).NotTo(HaveOccurred())
		spokeSync = out
		return out
	}, 5*time.Minute, 10*time.Second).ShouldNot(SatisfyAny(BeEmpty(), Equal("Unknown")))

	// The principal's event-processor can get stuck retrying one stale (superseded) status-update
	// event against a resourceVersion the hub has already moved past, logging "the server
	// rejected our request due to an error in our request" repeatedly - self-heals once the
	// spoke's next periodic resync (ArgoCD default 180s) sends a fresh event, but nudging another
	// refresh on the spoke gets there faster than passively waiting out that timer.
	By("verifying the hub Application received a real status update - proves the principal is relaying status from the agent")
	hubCheckAttempt := 0
	Eventually(func(g Gomega) string {
		hubCheckAttempt++
		out, err := kubectlCtx(hubContext, "get", "application", appName,
			"-n", argoCDNamespace,
			"-o", "jsonpath={.status.sync.status}")
		if err != nil || out == "" || out == "Unknown" {
			appSpokeInfo, _ := kubectlCtx(spokeContext, "get", "application",
				appName, "-n", argoCDNamespace,
				"-o", "jsonpath=sync={.status.sync.status} health={.status.health.status}")
			fmt.Fprintf(GinkgoWriter, "  [diag] hub Application %s sync=%q (spoke's own status: %q; spoke: %s)\n", appName, out, spokeSync, appSpokeInfo)
			if hubCheckAttempt == 6 {
				kubectlCtx(spokeContext, "annotate", "application", appName, "-n", argoCDNamespace,
					"argocd.argoproj.io/refresh=normal", "--overwrite")
			}
		}
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, 8*time.Minute, 10*time.Second).ShouldNot(SatisfyAny(BeEmpty(), Equal("Unknown")),
		"hub Application %s never received a real status update - the principal must actually receive and reflect real status from the agent (spoke's own status: %q)", appName, spokeSync)

	_ = appsetName
}

func verifyRedHatImages(ctx, ns string) {
	By(fmt.Sprintf("verifying all pods in %s use Red Hat images", ns))
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(ctx, "get", "pods", "-n", ns,
			"-o", "jsonpath={range .items[*]}{range .spec.containers[*]}{.image}{','}{end}{end}")
		g.Expect(err).NotTo(HaveOccurred())
		images := strings.Split(strings.TrimRight(out, ","), ",")
		for _, img := range images {
			img = strings.TrimSpace(img)
			if img == "" {
				continue
			}
			g.Expect(img).To(HavePrefix("registry.redhat.io/"),
				"expected Red Hat image, got: %s", img)
		}
	}, 2*time.Minute, 10*time.Second).Should(Succeed())
}

// ---- Local-cluster (hybrid mode) verification helpers ----
// local-cluster is never an addon-install target (no ManagedClusterAddOn, no dedicated ArgoCD
// instance/namespace): it already has its own ArgoCD instance (the one hosting the
// argocd-agent principal in openshift-gitops), and is registered as a plain, non-agent-routed
// ArgoCD cluster secret (see ensureLocalClusterSecret in pkg/controller/gitopscluster). Its
// Applications live in openshift-gitops and are reconciled by the hub's own application
// controller - there is no separate namespace, Redis, or agent for local-cluster.

// verifyLocalClusterSecret checks the plain in-cluster cluster secret ensureLocalClusterSecret
// registers for local-cluster: no agent-name label, no skip-reconcile annotation, server points
// at the in-cluster API.
func verifyLocalClusterSecret(timeout time.Duration) {
	secretName := "cluster-" + localClusterName

	By(fmt.Sprintf("verifying %s secret exists with in-cluster server and no agent routing", secretName))
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(hubContext, "get", "secret", secretName, "-n", argoCDNamespace,
			"-o", "jsonpath={.data.server}")
		g.Expect(err).NotTo(HaveOccurred())
		decoded, decErr := base64.StdEncoding.DecodeString(out)
		g.Expect(decErr).NotTo(HaveOccurred())
		g.Expect(string(decoded)).To(Equal("https://kubernetes.default.svc"))
	}, timeout, 5*time.Second).Should(Succeed())

	By(fmt.Sprintf("verifying %s secret has no agent-name label", secretName))
	agentLabel, err := kubectlCtx(hubContext, "get", "secret", secretName, "-n", argoCDNamespace,
		"-o", "jsonpath={.metadata.labels.argocd-agent\\.argoproj-labs\\.io/agent-name}")
	Expect(err).NotTo(HaveOccurred())
	Expect(agentLabel).To(BeEmpty(), "%s must not carry the agent-name label - it is not agent-routed", secretName)

	By(fmt.Sprintf("verifying %s secret has no skip-reconcile annotation", secretName))
	skipReconcile, err := kubectlCtx(hubContext, "get", "secret", secretName, "-n", argoCDNamespace,
		"-o", "jsonpath={.metadata.annotations.argocd\\.argoproj\\.io/skip-reconcile}")
	Expect(err).NotTo(HaveOccurred())
	Expect(skipReconcile).To(BeEmpty(), "%s must not carry skip-reconcile - the hub application controller owns it", secretName)
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

// verifyLocalClusterGuestbook verifies local-cluster's guestbook Application reconciles via the
// hub's own (single) ArgoCD instance in openshift-gitops - no dedicated namespace, Redis,
// app-controller, or agent involved.
//
// Agent mode: the ApplicationSet (generator targeting appsetPlacementName, which includes
// local-cluster) already created "local-cluster-guestbook" in openshift-gitops - this just
// verifies it reconciled successfully. Non-agent mode: no ApplicationSet is in play, so this
// creates a direct "guestbook" Application in openshift-gitops targeting the in-cluster API
// server, matching how any other in-cluster ArgoCD destination is used.
func verifyLocalClusterGuestbook(isAgentMode bool, timeout time.Duration) {
	appName := "guestbook"
	if isAgentMode {
		appName = localClusterName + "-guestbook"
	} else {
		By("ensuring default AppProject in openshift-gitops permits the in-cluster destination")
		Expect(applyLiteral(hubContext, appProjectYAML(argoCDNamespace))).To(Succeed())

		By("creating guestbook Application in openshift-gitops targeting local-cluster (in-cluster)")
		Expect(applyLiteral(hubContext, guestbookAppYAML(argoCDNamespace, "https://kubernetes.default.svc"))).To(Succeed())
	}

	By(fmt.Sprintf("waiting for %s to exist in %s", appName, argoCDNamespace))
	waitForResourceExists(hubContext, "applications.argoproj.io", appName, argoCDNamespace, timeout)

	By(fmt.Sprintf("verifying guestbook-ui deployment on local-cluster (hub) via %s", appName))
	verifyGuestbookDeployed(hubContext, timeout)

	By(fmt.Sprintf("verifying %s sync status", appName))
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(hubContext, "get", "applications.argoproj.io", appName,
			"-n", argoCDNamespace,
			"-o", "jsonpath={.status.sync.status}")
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, timeout, 10*time.Second).Should(Equal("Synced"))
}

// verifyLocalClusterControllerNamespace confirms local-cluster's guestbook Application is
// reconciled by the hub's single application controller in openshift-gitops - never a separate
// namespace, regardless of agent mode.
func verifyLocalClusterControllerNamespace(isAgentMode bool) {
	appName := "guestbook"
	if isAgentMode {
		appName = localClusterName + "-guestbook"
	}

	By(fmt.Sprintf("verifying %s is managed by the hub application controller (openshift-gitops)", appName))
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(hubContext, "get", "applications.argoproj.io", appName,
			"-n", argoCDNamespace,
			"-o", "jsonpath={.status.controllerNamespace}")
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}, 2*time.Minute, 5*time.Second).Should(Equal(argoCDNamespace))
}

// verifyLocalClusterEnvironmentHealth confirms no addon-installed ("acm-openshift-gitops")
// application controller exists anywhere on the hub - local-cluster is never an addon-install
// target, so there should be zero such pods, not just none outside a "local-cluster" namespace.
func verifyLocalClusterEnvironmentHealth() {
	verifyEnvironmentHealth(hubContext)
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

	// local-cluster's guestbook always lives in argoCDNamespace (hybrid mode: no dedicated
	// namespace). Non-agent mode created "guestbook" directly; agent mode's
	// "local-cluster-guestbook" is cleaned up below via the ApplicationSet cascade / explicit
	// delete alongside the spoke's agent-generated app.
	By("cleaning up local-cluster (hub) guestbook resources in openshift-gitops")
	kubectlCtx(hubContext, "delete", "application", "guestbook", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "namespace", "guestbook", "--ignore-not-found", "--wait=false")

	if isAgentMode {
		By("cleaning up ApplicationSet and agent-generated apps on hub")
		appsetName := placementName + "-guestbook-appset"
		kubectlCtx(hubContext, "delete", "applicationset", appsetName, "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "application", spokeName+"-guestbook", "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "application", localClusterName+"-guestbook", "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "application", "guestbook", "-n", spokeName, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "appproject", "default", "-n", spokeName, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "configmap", "acm-placement", "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "clusterrolebinding", "appset-placement-reader", "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "clusterrole", "appset-placement-reader", "--ignore-not-found")
	}
}

func scenarioCleanup(opts gitOpsClusterOpts) {
	By("--- Starting scenario cleanup (proper order per test-scenarios.sh) ---")

	By("1. Deleting Placements (addon-install + appset) - prevents controller from recreating addon")
	kubectlCtx(hubContext, "delete", "placement", opts.placementName, "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "placement", appsetPlacementName, "-n", argoCDNamespace, "--ignore-not-found")

	By("2. Deleting hub-side agent resources if applicable")
	if opts.agentEnabled {
		appsetName := opts.placementName + "-guestbook-appset"
		kubectlCtx(hubContext, "delete", "applicationset", appsetName, "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "applications.argoproj.io", spokeName+"-guestbook", "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "applications.argoproj.io", localClusterName+"-guestbook", "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "applications.argoproj.io", "--all", "-n", spokeName, "--ignore-not-found", "--wait=false")
		kubectlCtx(hubContext, "delete", "appproject", "--all", "-n", spokeName, "--ignore-not-found", "--wait=false")

		By("2a. Deleting agent-mode cluster secret for the spoke")
		kubectlCtx(hubContext, "delete", "secret", "cluster-"+spokeName, "-n", argoCDNamespace, "--ignore-not-found")
		// cluster-local-cluster is intentionally NOT deleted here: it's a permanent, non-agent
		// registration (see ensureLocalClusterSecret) that the hub controller keeps up to date
		// on every reconcile, not a per-scenario agent artifact.
	}

	By("3. Deleting Policy and PlacementBinding (stops enforcement on managed cluster)")
	policyName := opts.name + "-argocd-policy"
	bindingName := opts.name + "-argocd-policy-binding"
	kubectlCtx(hubContext, "delete", "policy.policy.open-cluster-management.io", policyName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")
	kubectlCtx(hubContext, "delete", "placementbinding.policy.open-cluster-management.io", bindingName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")

	By("3a. Waiting for replicated policy to be removed from spoke namespace")
	waitForResourceGone(hubContext, "policy.policy.open-cluster-management.io", policyName, spokeName, 2*time.Minute)

	By("4. Deleting ManagedClusterAddOn for spoke (triggers pre-delete cleanup Job)")
	deleteMCAWithFallback(hubContext, addonName, spokeName)

	By("5. Deleting GitOpsCluster")
	kubectlCtx(hubContext, "delete", "gitopscluster", opts.name, "-n", argoCDNamespace, "--ignore-not-found")

	By("6. Deleting ManagedClusterSetBinding")
	deleteLiteral(hubContext, managedClusterSetBindingYAML(argoCDNamespace))

	By("--- Scenario cleanup commands complete ---")
}

func verifyHubCleanup(opts gitOpsClusterOpts) {
	By("verifying GitOpsCluster is gone from hub")
	waitForResourceGone(hubContext, "gitopscluster", opts.name, argoCDNamespace, 2*time.Minute)

	By("verifying ManagedClusterAddOn for spoke is gone from hub")
	waitForResourceGone(hubContext, "managedclusteraddon", addonName, spokeName, 4*time.Minute)
}

func verifySpokeCleanup() {
	By("verifying ArgoCD CR is removed from spoke")
	waitForResourceGone(spokeContext, "argocd", "acm-openshift-gitops", argoCDNamespace, 5*time.Minute)

	By("verifying operator deployment is removed from spoke")
	waitForResourceGone(spokeContext, "deployment", "openshift-gitops-operator-controller-manager", operatorNamespace, 5*time.Minute)
}

// ---- Skip ArgoCD Policy annotation helpers ----

func verifySkipArgoCDPolicyAnnotation(gitopsClusterName, ns string, timeout time.Duration) {
	policyName := gitopsClusterName + "-argocd-policy"

	By("annotating GitOpsCluster with skip-argocd-policy=true")
	_, err := kubectlCtx(hubContext, "annotate", "gitopscluster", gitopsClusterName, "-n", ns,
		"apps.open-cluster-management.io/skip-argocd-policy=true", "--overwrite")
	Expect(err).NotTo(HaveOccurred())

	By("deleting the ArgoCD Policy")
	_, err = kubectlCtx(hubContext, "delete", "policy.policy.open-cluster-management.io",
		policyName, "-n", ns, "--wait=false")
	Expect(err).NotTo(HaveOccurred())

	By("waiting for Policy to be deleted")
	waitForResourceGone(hubContext, "policy.policy.open-cluster-management.io", policyName, ns, 2*time.Minute)

	// The controller's predicate only triggers on spec changes. Toggle a spec field
	// to force reconciliation while the skip annotation is active.
	By("triggering reconciliation via spec change and verifying Policy is NOT recreated")
	_, err = kubectlCtx(hubContext, "patch", "gitopscluster", gitopsClusterName, "-n", ns,
		"--type=merge", "-p", `{"spec":{"gitopsAddon":{"overrideExistingConfigs":true}}}`)
	Expect(err).NotTo(HaveOccurred(), "failed to patch gitopscluster overrideExistingConfigs=true for skip test")

	Consistently(func(g Gomega) {
		_, err := kubectlCtx(hubContext, "get", "policy.policy.open-cluster-management.io",
			policyName, "-n", ns)
		g.Expect(err).To(HaveOccurred(), "Policy should NOT be recreated while skip annotation is set")
	}, 30*time.Second, 5*time.Second).Should(Succeed())

	By("restoring overrideExistingConfigs after skip test")
	_, err = kubectlCtx(hubContext, "patch", "gitopscluster", gitopsClusterName, "-n", ns,
		"--type=merge", "-p", `{"spec":{"gitopsAddon":{"overrideExistingConfigs":false}}}`)
	Expect(err).NotTo(HaveOccurred(), "failed to restore gitopscluster overrideExistingConfigs=false after skip test")
}

func verifyPolicyRecreatedAfterAnnotationRemoval(gitopsClusterName, ns string, timeout time.Duration) {
	policyName := gitopsClusterName + "-argocd-policy"

	By("removing skip-argocd-policy annotation")
	_, err := kubectlCtx(hubContext, "annotate", "gitopscluster", gitopsClusterName, "-n", ns,
		"apps.open-cluster-management.io/skip-argocd-policy-", "--overwrite")
	Expect(err).NotTo(HaveOccurred())

	// The controller's predicate only triggers on spec changes (not annotation/metadata changes).
	// Toggle overrideExistingConfigs to force a reconciliation.
	By("toggling spec.gitopsAddon.overrideExistingConfigs to trigger reconciliation")
	_, err = kubectlCtx(hubContext, "patch", "gitopscluster", gitopsClusterName, "-n", ns,
		"--type=merge", "-p", `{"spec":{"gitopsAddon":{"overrideExistingConfigs":true}}}`)
	Expect(err).NotTo(HaveOccurred())

	By("waiting for Policy to be recreated")
	waitForResourceExists(hubContext, "policy.policy.open-cluster-management.io", policyName, ns, timeout)

	By("restoring overrideExistingConfigs to false")
	_, err = kubectlCtx(hubContext, "patch", "gitopscluster", gitopsClusterName, "-n", ns,
		"--type=merge", "-p", `{"spec":{"gitopsAddon":{"overrideExistingConfigs":false}}}`)
	Expect(err).NotTo(HaveOccurred(), "failed to restore gitopscluster overrideExistingConfigs=false after recreation test")
}

// ---- Agent Version Drift Auto-Heal helpers ----

func verifyAgentVersionDriftHeal(gitopsClusterName, ns string, timeout time.Duration) {
	policyName := gitopsClusterName + "-argocd-policy"

	By("getting principal deployment image from hub")
	var principalImage string
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(hubContext, "get", "deployment", "-n", ns,
			"-l", "app.kubernetes.io/component=principal",
			"-o", "jsonpath={.items[0].spec.template.spec.containers[0].image}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).NotTo(BeEmpty(), "principal deployment image should not be empty")
		principalImage = out
	}, 3*time.Minute, 5*time.Second).Should(Succeed())
	fmt.Fprintf(GinkgoWriter, "Principal image: %s\n", principalImage)

	By("injecting a mismatched agent image into the Policy to create drift")
	fakeImage := "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9:drift-test-fake-e2e"

	out, err := kubectlCtx(hubContext, "get", "policy.policy.open-cluster-management.io",
		policyName, "-n", ns, "-o", "json")
	Expect(err).NotTo(HaveOccurred(), "failed to get Policy for drift injection")

	var policyForPatch map[string]interface{}
	Expect(json.Unmarshal([]byte(out), &policyForPatch)).To(Succeed(), "failed to parse Policy JSON")

	patched := false
	if spec, ok := policyForPatch["spec"].(map[string]interface{}); ok {
		if pts, ok := spec["policy-templates"].([]interface{}); ok {
			for _, pt := range pts {
				ptMap, _ := pt.(map[string]interface{})
				od, _ := ptMap["objectDefinition"].(map[string]interface{})
				cpSpec, _ := od["spec"].(map[string]interface{})
				ots, _ := cpSpec["object-templates"].([]interface{})
				for _, ot := range ots {
					otMap, _ := ot.(map[string]interface{})
					objDef, _ := otMap["objectDefinition"].(map[string]interface{})
					if objDef["kind"] == "ArgoCD" {
						objSpec, _ := objDef["spec"].(map[string]interface{})
						if objSpec == nil {
							objSpec = map[string]interface{}{}
							objDef["spec"] = objSpec
						}
						agentSection, _ := objSpec["argoCDAgent"].(map[string]interface{})
						if agentSection == nil {
							agentSection = map[string]interface{}{}
							objSpec["argoCDAgent"] = agentSection
						}
						agentInner, _ := agentSection["agent"].(map[string]interface{})
						if agentInner == nil {
							agentInner = map[string]interface{}{}
							agentSection["agent"] = agentInner
						}
						agentInner["image"] = fakeImage
						patched = true
					}
				}
			}
		}
	}
	Expect(patched).To(BeTrue(), "could not find ArgoCD object-template in Policy to inject fake image")

	delete(policyForPatch["metadata"].(map[string]interface{}), "managedFields")
	patchedJSON, jsonErr := json.Marshal(policyForPatch)
	Expect(jsonErr).NotTo(HaveOccurred(), "failed to marshal patched Policy")
	Expect(applyLiteral(hubContext, string(patchedJSON))).To(Succeed(), "failed to apply patched Policy with fake image")
	fmt.Fprintf(GinkgoWriter, "Injected fake image %s into Policy\n", fakeImage)

	By("verifying Policy now has the fake image (pre-condition)")
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(hubContext, "get", "policy.policy.open-cluster-management.io",
			policyName, "-n", ns, "-o", "json")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(ContainSubstring(fakeImage), "Policy should contain fake image before heal")
	}, 30*time.Second, 5*time.Second).Should(Succeed())

	By("triggering reconciliation via spec change to run drift heal")
	_, err = kubectlCtx(hubContext, "patch", "gitopscluster", gitopsClusterName, "-n", ns,
		"--type=merge", "-p", `{"spec":{"gitopsAddon":{"overrideExistingConfigs":true}}}`)
	Expect(err).NotTo(HaveOccurred(), "failed to patch gitopscluster overrideExistingConfigs=true for drift heal")

	By("verifying controller healed: ArgoCD Policy agent image now matches principal")
	Eventually(func(g Gomega) {
		out, err := kubectlCtx(hubContext, "get", "policy.policy.open-cluster-management.io",
			policyName, "-n", ns, "-o", "json")
		g.Expect(err).NotTo(HaveOccurred())
		// Parse the Policy JSON and find the ArgoCD template's agent image
		var policyObj map[string]interface{}
		g.Expect(json.Unmarshal([]byte(out), &policyObj)).To(Succeed())
		spec, _ := policyObj["spec"].(map[string]interface{})
		templates, _ := spec["policy-templates"].([]interface{})
		found := false
		for _, pt := range templates {
			ptMap, _ := pt.(map[string]interface{})
			od, _ := ptMap["objectDefinition"].(map[string]interface{})
			cpSpec, _ := od["spec"].(map[string]interface{})
			ots, _ := cpSpec["object-templates"].([]interface{})
			for _, ot := range ots {
				otMap, _ := ot.(map[string]interface{})
				objDef, _ := otMap["objectDefinition"].(map[string]interface{})
				if objDef["kind"] == "ArgoCD" {
					agentImg, _ := objDef["spec"].(map[string]interface{})["argoCDAgent"].(map[string]interface{})["agent"].(map[string]interface{})["image"].(string)
					g.Expect(agentImg).To(Equal(principalImage),
						fmt.Sprintf("expected agent image %s but got %s", principalImage, agentImg))
					found = true
				}
			}
		}
		g.Expect(found).To(BeTrue(), "ArgoCD object-template not found in Policy")
	}, timeout, 5*time.Second).Should(Succeed())

	By("restoring overrideExistingConfigs after drift heal test")
	_, err = kubectlCtx(hubContext, "patch", "gitopscluster", gitopsClusterName, "-n", ns,
		"--type=merge", "-p", `{"spec":{"gitopsAddon":{"overrideExistingConfigs":false}}}`)
	Expect(err).NotTo(HaveOccurred(), "failed to restore gitopscluster overrideExistingConfigs=false after drift heal test")
}

// ---- Autonomous mode helpers ----

// verifyDestinationBasedMappingDisabledForAutonomous confirms destinationBasedMapping is
// disabled on both the hub's enforced Policy and the spoke's live ArgoCD CR - the two places
// this field is set (initial generation and the drift-heal loop) must agree.
func verifyDestinationBasedMappingDisabledForAutonomous(gitopsClusterName, ns string, timeout time.Duration) {
	policyName := gitopsClusterName + "-argocd-policy"

	By("verifying hub Policy has destinationBasedMapping.enabled=false and mode=autonomous")
	Eventually(func(g Gomega) {
		dbm, mode := getPolicyDestinationBasedMappingAndMode(g, policyName, ns)
		g.Expect(mode).To(Equal("autonomous"))
		g.Expect(dbm).To(Equal(false))
	}, timeout, 5*time.Second).Should(Succeed())

	By("verifying spoke ArgoCD CR has destinationBasedMapping.enabled=false and mode=autonomous")
	Eventually(func(g Gomega) {
		dbm, err := getJSONPath(spokeContext, "argocd", "acm-openshift-gitops", argoCDNamespace,
			"{.spec.argoCDAgent.agent.destinationBasedMapping.enabled}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(dbm).To(Equal("false"))

		mode, err := getJSONPath(spokeContext, "argocd", "acm-openshift-gitops", argoCDNamespace,
			"{.spec.argoCDAgent.agent.client.mode}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(mode).To(Equal("autonomous"))
	}, timeout, 5*time.Second).Should(Succeed())
}

// getPolicyDestinationBasedMappingAndMode extracts destinationBasedMapping.enabled and
// client.mode from the ArgoCD object-template inside the given Policy.
func getPolicyDestinationBasedMappingAndMode(g Gomega, policyName, ns string) (bool, string) {
	out, err := kubectlCtx(hubContext, "get", "policy.policy.open-cluster-management.io",
		policyName, "-n", ns, "-o", "json")
	g.Expect(err).NotTo(HaveOccurred())

	var policyObj map[string]interface{}
	g.Expect(json.Unmarshal([]byte(out), &policyObj)).To(Succeed())
	spec, _ := policyObj["spec"].(map[string]interface{})
	templates, _ := spec["policy-templates"].([]interface{})
	for _, pt := range templates {
		ptMap, _ := pt.(map[string]interface{})
		od, _ := ptMap["objectDefinition"].(map[string]interface{})
		cpSpec, _ := od["spec"].(map[string]interface{})
		ots, _ := cpSpec["object-templates"].([]interface{})
		for _, ot := range ots {
			otMap, _ := ot.(map[string]interface{})
			objDef, _ := otMap["objectDefinition"].(map[string]interface{})
			if objDef["kind"] != "ArgoCD" {
				continue
			}
			agent := objDef["spec"].(map[string]interface{})["argoCDAgent"].(map[string]interface{})["agent"].(map[string]interface{})
			dbm, _ := agent["destinationBasedMapping"].(map[string]interface{})["enabled"].(bool)
			mode, _ := agent["client"].(map[string]interface{})["mode"].(string)
			return dbm, mode
		}
	}
	g.Expect(false).To(BeTrue(), "ArgoCD object-template not found in Policy %s", policyName)
	return false, ""
}

// deployGuestbookDirectlyOnSpoke deploys the guestbook Application by connecting directly to the
// managed cluster's own API server (kubectlCtx(spokeContext, ...)) and applying it there - never
// through the hub. This is the actual autonomous-mode contract: "all configuration is first
// created on the workload cluster" (see
// https://argocd-agent.readthedocs.io/latest/concepts/agent-modes/autonomous/). Delivering the
// same Application spec via a hub-authored Policy (as governance-policy-framework enforcement)
// would exercise a hub-push delivery path indistinguishable from managed mode and wouldn't prove
// autonomous mode's distinguishing property: the hub never originates or owns the config, it only
// mirrors a read-only reflection of whatever the spoke's agent reports.
func deployGuestbookDirectlyOnSpoke(timeout time.Duration) {
	By("connecting directly to the spoke and creating the guestbook Application there (not via the hub)")
	ensureArgoCDClusterAdmin(spokeContext, argoCDNamespace)
	Expect(applyLiteral(spokeContext, guestbookAppYAML(argoCDNamespace, "https://kubernetes.default.svc"))).To(Succeed())

	By("waiting for the Application's sync status to settle to a real (non-Unknown) value on the spoke itself (source of truth)")
	var spokeSync string
	Eventually(func(g Gomega) string {
		out, err := kubectlCtx(spokeContext, "get", "applications.argoproj.io", "guestbook",
			"-n", argoCDNamespace, "-o", "jsonpath={.status.sync.status}")
		g.Expect(err).NotTo(HaveOccurred())
		spokeSync = out
		return out
	}, timeout, 5*time.Second).ShouldNot(SatisfyAny(BeEmpty(), Equal("Unknown")))

	By("verifying guestbook-ui deployment exists on the spoke")
	verifyGuestbookDeployed(spokeContext, timeout)

	// Deliberately does not require the hub's value to equal the spoke's value at this instant -
	// both progress independently, so racing them for exact equality chases a moving target.
	// What's under test is whether the hub's read-only mirror received a REAL (non-empty,
	// non-Unknown) status update at all, not a hardcoded "Synced" and not byte-identical timing
	// with the spoke.
	By("verifying the Application is mirrored to the hub as a read-only reflection with a real status update (namespace named after the agent)")
	mirrorCheckAttempt := 0
	Eventually(func(g Gomega) string {
		mirrorCheckAttempt++
		out, err := kubectlCtx(hubContext, "get", "applications.argoproj.io", "guestbook",
			"-n", spokeName, "-o", "jsonpath={.status.sync.status}")
		g.Expect(err).NotTo(HaveOccurred())
		if (out == "" || out == "Unknown") && mirrorCheckAttempt == 6 {
			// See the nudge comment in deployGuestbookAgentMode - the principal's queue can get
			// stuck retrying a stale status-update event; nudging the spoke to re-emit is faster
			// than waiting out ArgoCD's own periodic resync.
			kubectlCtx(spokeContext, "annotate", "applications.argoproj.io", "guestbook", "-n", argoCDNamespace,
				"argocd.argoproj.io/refresh=normal", "--overwrite")
		}
		return out
	}, timeout, 5*time.Second).ShouldNot(SatisfyAny(BeEmpty(), Equal("Unknown")),
		"hub mirror never received a real status update (spoke's own status: %q)", spokeSync)
}

// cleanupGuestbookDirectSpoke deletes the guestbook Application by connecting directly to the
// spoke, mirroring how deployGuestbookDirectlyOnSpoke created it.
func cleanupGuestbookDirectSpoke() {
	By("cleaning up guestbook app directly on the spoke")
	kubectlCtx(spokeContext, "delete", "applications.argoproj.io", "guestbook",
		"-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(spokeContext, "delete", "namespace", "guestbook", "--ignore-not-found", "--wait=false")
	kubectlCtx(spokeContext, "delete", "clusterrolebinding",
		"acm-openshift-gitops-cluster-admin", "--ignore-not-found")
}

// disableHubAppController / enableHubAppController: e2e-Kind-environment-only toggle (no product
// code involved) around the hub's own ArgoCD app-controller, needed ONLY here and NOT by
// gitopsaddon/test-cycle.sh (which runs the exact same autonomous-mode + hybrid-mode combination
// against a real hub and passes without this toggle).
//
// Both halves of hybrid mode's isolation mechanism ARE correctly wired for autonomous mode: the
// argocd-agent principal rewrites a mirrored autonomous Application's spec.destination to
// {name: <agent>} (principal/event.go in argoproj-labs/argocd-agent), and this repo's
// CreateArgoCDAgentClusters unconditionally stamps that agent's cluster secret with
// argocd.argoproj.io/skip-reconcile: "true" regardless of mode - confirmed by direct inspection
// (kubectl get application -o jsonpath spec.destination, kubectl get secret cluster-<agent>
// -o jsonpath annotations) against a live run of this exact suite.
//
// The remaining gap is an ArgoCD version limitation, not a bug in this repo: prior to
// https://github.com/argoproj/argo-cd/pull/26442 (merged into argo-cd upstream master
// 2026-02-19), skip-reconcile on a cluster secret only gated the sync/reconcile-action trigger,
// not the periodic status-refresh/comparison path - so the app-controller still attempts to
// resolve the mirrored Application's spec.project (rewritten by the principal to
// "<agent>-<project>", which by design only ever exists on the spoke for autonomous mode), fails
// with "AppProject not found", and flips status.sync.status to Unknown on every refresh cycle
// (see https://github.com/argoproj/argo-cd/issues/26425, filed specifically about this
// argocd-agent/app-controller conflict). PR #26442 centralizes the check into IsManagedCluster so
// skip-reconcile now also gates canProcessApp/canHandleCluster/the cluster-info updater, closing
// this exact gap - but it's a v3.4+ feature. The e2e Kind environment's upstream community
// argocd-operator image resolves to ArgoCD v3.3.10 (confirmed via the application-controller
// pod's own startup log), which predates the fix. The real-hub environment gitopsaddon/test-cycle.sh
// runs against does not hit this, so its autonomous-mode phase intentionally leaves the hub
// app-controller enabled throughout and asserts on it directly - do not add this toggle there.
func disableHubAppController() {
	By("disabling hub ArgoCD app-controller (e2e-only: this Kind environment's ArgoCD v3.3.10 predates argo-cd#26442, so skip-reconcile alone doesn't stop the app-controller's status-refresh path from racing the principal over the autonomous mirror's status)")
	// A merge patch (not applyLiteral/kubectl apply) - the hub ArgoCD CR carries a full spec
	// (applicationSet, argoCDAgent, sourceNamespaces) applied by setup_env.sh; a 3-way apply of a
	// manifest containing only spec.controller would delete every other field via apply's
	// last-applied-configuration diff. A merge patch touches only the field named.
	_, err := kubectlCtx(hubContext, "patch", "argocd", "openshift-gitops", "-n", argoCDNamespace,
		"--type=merge", "-p", `{"spec":{"controller":{"enabled":false}}}`)
	Expect(err).NotTo(HaveOccurred())
	Eventually(func(g Gomega) string {
		// "-o name" (not --no-headers) avoids kubectl's "No resources found in ... namespace."
		// notice, which --no-headers still prints to stdout on a zero-match list and is not "".
		out, _ := kubectlCtx(hubContext, "get", "pods", "-n", argoCDNamespace,
			"-l", "app.kubernetes.io/name=openshift-gitops-application-controller",
			"-o", "name")
		return out
	}, 2*time.Minute, 5*time.Second).Should(BeEmpty())
}

func enableHubAppController() {
	By("re-enabling hub ArgoCD app-controller")
	_, err := kubectlCtx(hubContext, "patch", "argocd", "openshift-gitops", "-n", argoCDNamespace,
		"--type=merge", "-p", `{"spec":{"controller":{"enabled":true}}}`)
	Expect(err).NotTo(HaveOccurred())
	waitForPodPhase(hubContext, argoCDNamespace,
		"app.kubernetes.io/name=openshift-gitops-application-controller", "Running", 3*time.Minute)
}

// ---- OLM Override helpers ----

func verifyOLMOverrideEnvVars(clusterName string, timeout time.Duration) {
	verifyAddOnDeploymentConfigEnvVar(clusterName, "OLM_SUBSCRIPTION_ENABLED", "true", timeout)
}

func safeCleanupOLMOverride(opts gitOpsClusterOpts) {
	policyName := opts.name + "-argocd-policy"
	bindingName := opts.name + "-argocd-policy-binding"
	kubectlCtx(hubContext, "delete", "placement", opts.placementName, "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "policy.policy.open-cluster-management.io", policyName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")
	kubectlCtx(hubContext, "delete", "placementbinding.policy.open-cluster-management.io", bindingName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")
	deleteMCAWithFallback(hubContext, addonName, spokeName)
	kubectlCtx(hubContext, "delete", "gitopscluster", opts.name, "-n", argoCDNamespace, "--ignore-not-found")
}

// safeCleanup is a best-effort AfterAll safety net (in addition to the explicit Cleanup context
// in each scenario) - it must not fail the suite if resources are already gone.
func safeCleanup(opts gitOpsClusterOpts) {
	kubectlCtx(hubContext, "delete", "placement", opts.placementName, "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "placement", appsetPlacementName, "-n", argoCDNamespace, "--ignore-not-found")
	policyName := opts.name + "-argocd-policy"
	bindingName := opts.name + "-argocd-policy-binding"
	kubectlCtx(hubContext, "delete", "policy.policy.open-cluster-management.io", policyName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")
	kubectlCtx(hubContext, "delete", "placementbinding.policy.open-cluster-management.io", bindingName, "-n", argoCDNamespace, "--ignore-not-found", "--wait=false")
	// local-cluster's guestbook always lives in argoCDNamespace under hybrid mode.
	kubectlCtx(hubContext, "delete", "application", "guestbook", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(hubContext, "delete", "namespace", "guestbook", "--ignore-not-found", "--wait=false")
	if opts.agentEnabled {
		appsetName := opts.placementName + "-guestbook-appset"
		kubectlCtx(hubContext, "delete", "applicationset", appsetName, "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "applications.argoproj.io", spokeName+"-guestbook", "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "applications.argoproj.io", localClusterName+"-guestbook", "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "application", "guestbook", "-n", spokeName, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "appproject", "--all", "-n", spokeName, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "configmap", "acm-placement", "-n", argoCDNamespace, "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "clusterrolebinding", "appset-placement-reader", "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "clusterrole", "appset-placement-reader", "--ignore-not-found")
		kubectlCtx(hubContext, "delete", "secret", "cluster-"+spokeName, "-n", argoCDNamespace, "--ignore-not-found")
		// cluster-local-cluster is a permanent, non-agent registration - not cleaned up here.
	}
	kubectlCtx(spokeContext, "delete", "application", "guestbook", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(spokeContext, "delete", "appproject", "default", "-n", argoCDNamespace, "--ignore-not-found")
	kubectlCtx(spokeContext, "delete", "namespace", "guestbook", "--ignore-not-found", "--wait=false")
	kubectlCtx(spokeContext, "delete", "clusterrolebinding", "acm-openshift-gitops-cluster-admin", "--ignore-not-found")
	deleteMCAWithFallback(hubContext, addonName, spokeName)
	kubectlCtx(hubContext, "delete", "gitopscluster", opts.name, "-n", argoCDNamespace, "--ignore-not-found")
	deleteLiteral(hubContext, managedClusterSetBindingYAML(argoCDNamespace))
}

// ---- Cert rotation helpers ----

func verifyCertRotationOnSpoke(argoNs string, timeout time.Duration) {
	By("verifying argocd-agent-client-tls exists on spoke")
	waitForResourceExists(spokeContext, "secret", "argocd-agent-client-tls", argoNs, timeout)

	By("recording cert fingerprint before deletion")
	beforeMd5, err := kubectlCtx(spokeContext, "get", "secret", "argocd-agent-client-tls",
		"-n", argoNs, "-o", "jsonpath={.data.tls\\.crt}")
	Expect(err).ToNot(HaveOccurred())
	Expect(beforeMd5).ToNot(BeEmpty())

	By("deleting argocd-agent-client-tls on spoke")
	_, err = kubectlCtx(spokeContext, "delete", "secret", "argocd-agent-client-tls", "-n", argoNs)
	Expect(err).ToNot(HaveOccurred())

	By("waiting for argocd-agent-client-tls to be recreated on spoke")
	waitForResourceExists(spokeContext, "secret", "argocd-agent-client-tls", argoNs, timeout)

	By("verifying cert data was restored")
	afterMd5, err := kubectlCtx(spokeContext, "get", "secret", "argocd-agent-client-tls",
		"-n", argoNs, "-o", "jsonpath={.data.tls\\.crt}")
	Expect(err).ToNot(HaveOccurred())
	Expect(afterMd5).ToNot(BeEmpty())

	fmt.Fprintf(GinkgoWriter, "  cert rotation: secret recreated after deletion\n")
}

func verifyCASecretInNamespace(expectedNs string, timeout time.Duration) {
	By(fmt.Sprintf("verifying argocd-agent-ca secret exists in %s on hub", expectedNs))
	waitForResourceExists(hubContext, "secret", "argocd-agent-ca", expectedNs, timeout)
}
