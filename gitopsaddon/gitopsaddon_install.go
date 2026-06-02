/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gitopsaddon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// installOrUpdateOpenshiftGitops orchestrates the complete GitOps installation process.
// The addon agent selects the installation method using the following priority:
//  1. Hub cluster: skip operator installation (operator already exists from OLM)
//  2. OLM_SUBSCRIPTION_ENABLED=true (set via GitOpsCluster olmSubscription.enabled):
//     force OLM subscription mode regardless of cluster type
//  3. OCP auto-detect: create OLM subscription for openshift-gitops-operator
//  4. Fallback: deploy embedded operator manifests (Kind, EKS, etc.)
//
// ArgoCD CR is created by Policy (argocd_policy.go on the hub), not by this addon.
func (r *GitopsAddonReconciler) installOrUpdateOpenshiftGitops() error {
	ctx := context.Background()

	// Detect cluster type
	isHub, err := r.isHubCluster(ctx)
	if err != nil {
		return fmt.Errorf("failed to detect hub cluster: %w", err)
	}

	if isHub {
		klog.Info("Hub cluster detected - skipping operator installation (operator already present)")
		// On the hub, the ArgoCD namespace is the managed cluster's own namespace (e.g. local-cluster).
		// Ensure the namespace has the right labels for image pull secret sync.
		argoCDNs := getArgoCDNamespace()
		if argoCDNs != "" && argoCDNs != GitOpsNamespace {
			nsKey := types.NamespacedName{Name: argoCDNs}
			if err := r.CreateUpdateNamespace(nsKey); err != nil {
				return fmt.Errorf("failed to ensure namespace %s on hub: %w", argoCDNs, err)
			}
		}
		return nil
	}

	// On managed clusters: clean up the stale default ArgoCD instance that was created by
	// the ACM 2.16 OLM operator (installed via the embedded subscription in the old
	// gitops-addon-olm ManifestWork). That subscription lacked DISABLE_DEFAULT_ARGOCD_INSTANCE=true,
	// so the operator created a default GitopsService CR and thus the openshift-gitops ArgoCD CR.
	// The 2.17 addon agent runs with DISABLE_DEFAULT_ARGOCD_INSTANCE=true in its subscription,
	// so the default instance should not exist. Deleting the GitopsService CR causes the operator
	// to garbage-collect the openshift-gitops ArgoCD CR via ownerReferences.
	if err := r.deleteDefaultGitopsServiceIfStale(ctx); err != nil {
		klog.Warningf("Failed to clean up stale default GitopsService CR: %v", err)
	}

	// OLM override: when olmSubscription.enabled=true in GitOpsCluster spec, the hub
	// sets OLM_SUBSCRIPTION_ENABLED=true on the agent. This forces OLM subscription
	// mode regardless of OCP auto-detection, allowing operators to explicitly choose
	// OLM even if the cluster detection fails or the cluster is non-OCP.
	if strings.EqualFold(os.Getenv("OLM_SUBSCRIPTION_ENABLED"), "true") {
		klog.Info("OLM subscription mode forced by GitOpsCluster spec (OLM_SUBSCRIPTION_ENABLED=true)")
		return r.installViaOLMSubscription(ctx)
	}

	isOCP, err := r.isOCPCluster(ctx)
	if err != nil {
		return fmt.Errorf("failed to detect OCP cluster: %w", err)
	}

	if isOCP {
		klog.Info("OCP cluster detected - creating OLM subscription for openshift-gitops-operator")
		return r.installViaOLMSubscription(ctx)
	}

	klog.Info("Non-OCP cluster detected - deploying embedded openshift-gitops operator")
	return r.installViaEmbeddedManifests()
}

// installViaOLMSubscription creates an OLM subscription on OCP clusters.
//
// The subscription is placed in the openshift-gitops-operator namespace (matching
// the embedded-chart install path). This ensures OLM deploys the operator
// controller-manager — and its conversion webhook service — in the same namespace
// the Red Hat OLM bundle hardcodes in the ArgoCD CRD's conversion webhook config
// (openshift-gitops-operator-controller-manager-service.openshift-gitops-operator).
// Placing the subscription in openshift-operators (AllNamespaces mode) would put
// the service in openshift-operators instead, causing an OLM InstallPlan failure:
// "conversion webhook service not found".
func (r *GitopsAddonReconciler) installViaOLMSubscription(ctx context.Context) error {
	subNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAMESPACE", GitOpsOperatorNamespace)

	// Ensure the subscription namespace exists with proper labels.
	if err := r.CreateUpdateNamespace(types.NamespacedName{Name: subNamespace}); err != nil {
		return fmt.Errorf("failed to ensure OLM subscription namespace %s: %w", subNamespace, err)
	}

	// Create an AllNamespaces OperatorGroup so OLM knows to deploy the operator.
	// The openshift-gitops-operator does not support OwnNamespace install mode;
	// it requires AllNamespaces. Without an OperatorGroup in the subscription
	// namespace OLM will refuse to install.
	if err := r.createOrUpdateOperatorGroup(ctx, subNamespace); err != nil {
		return fmt.Errorf("failed to ensure OperatorGroup in %s: %w", subNamespace, err)
	}

	// Migration: if a previous install left a subscription in a stale namespace,
	// delete it now so OLM doesn't run two competing operator instances.
	// Only migrates our own labeled subscriptions.
	//
	// Namespaces checked:
	//   openshift-operators — the pre-2.17 default for OLM mode
	//   open-cluster-management-agent-addon — the addon agent namespace; some 2.16
	//     installer builds embedded the subscription there in the static AddOnTemplate
	//     (ManifestWork) before it was moved to openshift-gitops-operator in 2.17.
	for _, oldNS := range []string{"openshift-operators", AddonNamespace} {
		if subNamespace == oldNS {
			continue
		}
		if err := r.migrateSubscriptionFromNamespace(ctx, oldNS); err != nil {
			klog.Warningf("Migration: failed to remove old subscription from %s: %v", oldNS, err)
		}
	}

	// Recover from a previously-failed InstallPlan caused by the ArgoCD conversion
	// webhook not being ready. When OLM applies the CRD bundle it sets strategy=Webhook
	// and immediately tries to validate existing CRs; if the operator pod wasn't ready yet
	// the webhook call fails and OLM marks the InstallPlan as failed — permanently (OLM
	// never retries a failed InstallPlan automatically). Deleting the stale InstallPlan
	// causes OLM to set InstallPlanMissing on the subscription; our existing
	// InstallPlanMissing handler then deletes and recreates the subscription, OLM creates
	// a new InstallPlan, and because the operator IS already running (installedCSV is set)
	// the webhook validation succeeds on the second attempt.
	if err := r.recoverFromFailedWebhookInstallPlan(ctx); err != nil {
		klog.Warningf("Pre-install: failed to recover from failed webhook InstallPlan: %v", err)
	}

	// Recover from a "ghost Complete" InstallPlan — the plan phase is Complete but the
	// operator CSV was never actually created in the target namespace. This happens during
	// namespace migration: when the subscription is first created in openshift-gitops-operator
	// while the CSV still exists in openshift-operators, OLM finds it "Present" globally and
	// marks the plan Complete without creating a local copy. After the old CSV is later deleted
	// (by the migration sweep above), the CSV is absent but the InstallPlan is already done
	// and OLM won't re-run it. Deleting the stale Complete InstallPlan forces OLM to issue a
	// fresh one, which creates the CSV in the correct namespace.
	if err := r.recoverFromGhostCompleteInstallPlan(ctx); err != nil {
		klog.Warningf("Pre-install: failed to recover from ghost-complete InstallPlan: %v", err)
	}

	// Only patch the ArgoCD CRD conversion webhook when the subscription is actually
	// being created or its spec is genuinely changing. On steady-state reconciles the
	// subscription spec is unchanged, so we skip the patch to avoid a fight-loop where
	// the addon sets strategy=None every 60 s and the operator immediately restores it
	// to Webhook, causing continuous OLM churn and rolling addon pod restarts.
	if r.olmSubscriptionNeedsUpdate(ctx) {
		if err := patchArgoCDCRDConversionWebhookIfNotReady(ctx, r.Client); err != nil {
			klog.Warningf("Pre-install: failed to patch ArgoCD CRD conversion webhook: %v", err)
		}
	}

	if err := r.createOrUpdateOLMSubscription(ctx); err != nil {
		return fmt.Errorf("failed to create OLM subscription: %w", err)
	}

	klog.Info("Waiting for ArgoCD operator deployment to be ready (OLM mode)...")
	if err := r.waitForOperatorReady(5*time.Minute, subNamespace); err != nil {
		klog.Errorf("Failed to wait for ArgoCD operator: %v", err)
		return err
	}
	klog.Info("ArgoCD operator is ready (OLM mode)")

	gitopsNsKey := types.NamespacedName{Name: GitOpsNamespace}
	if err := r.CreateUpdateNamespace(gitopsNsKey); err != nil {
		return fmt.Errorf("failed to create/update %s namespace: %w", GitOpsNamespace, err)
	}

	return nil
}

// installViaEmbeddedManifests deploys the operator from embedded chart on non-OCP clusters
func (r *GitopsAddonReconciler) installViaEmbeddedManifests() error {
	err := r.applyCRDIfNotExists(RouteCRDFS, "routes", "route.openshift.io/v1", "routes-openshift-crd/routes.route.openshift.io.crd.yaml")
	if err != nil {
		return err
	}

	monitoringCRDs := []struct {
		resource, apiVersion, path string
	}{
		{"servicemonitors", "monitoring.coreos.com/v1", "monitoring-crds/servicemonitors.monitoring.coreos.com.crd.yaml"},
		{"prometheuses", "monitoring.coreos.com/v1", "monitoring-crds/prometheuses.monitoring.coreos.com.crd.yaml"},
		{"prometheusrules", "monitoring.coreos.com/v1", "monitoring-crds/prometheusrules.monitoring.coreos.com.crd.yaml"},
	}
	for _, crd := range monitoringCRDs {
		if err := r.applyCRDIfNotExists(MonitoringCRDFS, crd.resource, crd.apiVersion, crd.path); err != nil {
			return err
		}
	}

	gitopsOperatorNsKey := types.NamespacedName{Name: GitOpsOperatorNamespace}
	if err := r.CreateUpdateNamespace(gitopsOperatorNsKey); err != nil {
		return fmt.Errorf("failed to create/update %s namespace: %w", GitOpsOperatorNamespace, err)
	}

	if err := r.templateAndApplyChart("charts/openshift-gitops-operator", GitOpsOperatorNamespace, "openshift-gitops-operator"); err != nil {
		klog.Errorf("Failed to template and apply openshift-gitops-operator: %v", err)
		return err
	}
	klog.Info("Successfully templated and applied openshift-gitops-operator")

	if err := r.patchServiceAccountsWithImagePullSecrets(GitOpsOperatorNamespace); err != nil {
		klog.Warningf("Failed to patch operator ServiceAccounts with imagePullSecrets: %v", err)
	}
	if err := r.deletePodsWithImagePullIssuesInNamespace(GitOpsOperatorNamespace); err != nil {
		klog.Warningf("Failed to delete pods with image pull issues in namespace %s: %v", GitOpsOperatorNamespace, err)
	}

	klog.Info("Waiting for ArgoCD operator deployment to be ready...")
	if err := r.waitForOperatorReady(2 * time.Minute); err != nil {
		klog.Errorf("Failed to wait for ArgoCD operator: %v", err)
		return err
	}
	klog.Info("ArgoCD operator is ready")

	gitopsNsKey := types.NamespacedName{Name: GitOpsNamespace}
	if err := r.CreateUpdateNamespace(gitopsNsKey); err != nil {
		return fmt.Errorf("failed to create/update %s namespace: %w", GitOpsNamespace, err)
	}

	return nil
}

// isOCPCluster detects if the cluster is an OpenShift Container Platform cluster.
// Checks multiple signals: CRDs specific to OCP, and OCM ClusterClaims.
func (r *GitopsAddonReconciler) isOCPCluster(ctx context.Context) (bool, error) {
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")

	// Check 1: ClusterVersion CRD (core OCP CRD)
	if err := r.Get(ctx, types.NamespacedName{Name: "clusterversions.config.openshift.io"}, crd); err == nil {
		klog.Info("OCP detected: clusterversions.config.openshift.io CRD exists")
		return true, nil
	} else if !errors.IsNotFound(err) && !isNoKindMatchError(err) {
		return false, fmt.Errorf("failed to check clusterversions CRD: %w", err)
	}

	// Check 2: Infrastructure CRD (core OCP CRD)
	if err := r.Get(ctx, types.NamespacedName{Name: "infrastructures.config.openshift.io"}, crd); err == nil {
		klog.Info("OCP detected: infrastructures.config.openshift.io CRD exists")
		return true, nil
	} else if !errors.IsNotFound(err) && !isNoKindMatchError(err) {
		return false, fmt.Errorf("failed to check infrastructures CRD: %w", err)
	}

	claim := &unstructured.Unstructured{}
	claim.SetAPIVersion("cluster.open-cluster-management.io/v1alpha1")
	claim.SetKind("ClusterClaim")

	// Check 3: ClusterClaim version.openshift.io
	if err := r.Get(ctx, types.NamespacedName{Name: "version.openshift.io"}, claim); err == nil {
		klog.Info("OCP detected: version.openshift.io ClusterClaim exists")
		return true, nil
	} else if !errors.IsNotFound(err) && !isNoKindMatchError(err) {
		return false, fmt.Errorf("failed to check version.openshift.io ClusterClaim: %w", err)
	}

	// Check 4: ClusterClaim product.open-cluster-management.io == "OpenShift"
	if err := r.Get(ctx, types.NamespacedName{Name: "product.open-cluster-management.io"}, claim); err == nil {
		val, _, _ := unstructured.NestedString(claim.Object, "spec", "value")
		if val == "OpenShift" {
			klog.Info("OCP detected: product.open-cluster-management.io ClusterClaim is OpenShift")
			return true, nil
		}
	} else if !errors.IsNotFound(err) && !isNoKindMatchError(err) {
		return false, fmt.Errorf("failed to check product ClusterClaim: %w", err)
	}

	klog.Info("Cluster is not OCP (no OpenShift indicators found)")
	return false, nil
}

// isHubCluster detects if this cluster is the ACM/OCM hub cluster.
// Checks for ClusterManager resources (hub operator) and ManagedCluster with local-cluster identity.
func (r *GitopsAddonReconciler) isHubCluster(ctx context.Context) (bool, error) {
	// Check 1: ClusterManager resources exist (hub-only OCM resource)
	cmList := &unstructured.UnstructuredList{}
	cmList.SetAPIVersion("operator.open-cluster-management.io/v1")
	cmList.SetKind("ClusterManagerList")
	if err := r.List(ctx, cmList); err != nil {
		if !errors.IsNotFound(err) && !isNoKindMatchError(err) {
			return false, fmt.Errorf("failed to list ClusterManager resources: %w", err)
		}
	} else if len(cmList.Items) > 0 {
		klog.Info("Hub cluster detected: ClusterManager resource found")
		return true, nil
	}

	// Check 2: ManagedCluster with name "local-cluster" or label "local-cluster=true"
	mcList := &unstructured.UnstructuredList{}
	mcList.SetAPIVersion("cluster.open-cluster-management.io/v1")
	mcList.SetKind("ManagedClusterList")
	if err := r.List(ctx, mcList); err != nil {
		if !errors.IsNotFound(err) && !isNoKindMatchError(err) {
			return false, fmt.Errorf("failed to list ManagedCluster resources: %w", err)
		}
	} else {
		for i := range mcList.Items {
			mc := &mcList.Items[i]
			if mc.GetName() == "local-cluster" {
				klog.Info("Hub cluster detected: ManagedCluster 'local-cluster' found")
				return true, nil
			}
			labels := mc.GetLabels()
			if labels != nil {
				if v, ok := labels["local-cluster"]; ok && v == "true" {
					klog.Infof("Hub cluster detected: ManagedCluster '%s' has local-cluster=true label", mc.GetName())
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// getArgoCDNamespace returns the namespace where the ArgoCD CR lives for this cluster.
func getArgoCDNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("ARGOCD_NAMESPACE")); ns != "" {
		return ns
	}
	return GitOpsNamespace
}

// getOLMEnvOrDefault reads an OLM subscription value from env var, falling back to a default.
// When olmSubscription.enabled is true on the GitOpsCluster, the hub controller passes custom
// values through AddOnDeploymentConfig env vars. The addon agent reads them here.
func getOLMEnvOrDefault(envKey, defaultValue string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return defaultValue
}

// olmSubscriptionSpecChanged returns true when any of the four user-visible spec
// fields differ between what is installed and what the env vars require.
func olmSubscriptionSpecChanged(existing *unstructured.Unstructured, channel, source, sourceNamespace, installPlanApproval string) bool {
	existingChannel, _, _ := unstructured.NestedString(existing.Object, "spec", "channel")
	existingSource, _, _ := unstructured.NestedString(existing.Object, "spec", "source")
	existingSourceNS, _, _ := unstructured.NestedString(existing.Object, "spec", "sourceNamespace")
	existingApproval, _, _ := unstructured.NestedString(existing.Object, "spec", "installPlanApproval")
	klog.Infof("Existing spec: channel=%s, source=%s, sourceNamespace=%s, installPlanApproval=%s", existingChannel, existingSource, existingSourceNS, existingApproval)
	klog.Infof("New spec: channel=%s, source=%s, sourceNamespace=%s, installPlanApproval=%s", channel, source, sourceNamespace, installPlanApproval)
	klog.Infof("OLM Subscription SpecChanged: %v", existingChannel != channel || existingSource != source || existingSourceNS != sourceNamespace || existingApproval != installPlanApproval)
	return existingChannel != channel ||
		existingSource != source ||
		existingSourceNS != sourceNamespace ||
		existingApproval != installPlanApproval
}

// recoverFromFailedWebhookInstallPlan detects an OLM subscription with an InstallPlanFailed
// condition caused by the ArgoCD conversion webhook and deletes the referenced InstallPlan.
// Once the InstallPlan is deleted, OLM transitions the subscription to InstallPlanMissing;
// the existing InstallPlanMissing handler then delete+recreates the subscription so OLM
// issues a fresh InstallPlan — which succeeds because by this point the operator IS running.
// This is a no-op if the subscription does not exist, is not ours, or the failure is not
// webhook-related.
func (r *GitopsAddonReconciler) recoverFromFailedWebhookInstallPlan(ctx context.Context) error {
	subName := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAME", "openshift-gitops-operator")
	subNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAMESPACE", GitOpsOperatorNamespace)

	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("operators.coreos.com/v1alpha1")
	existing.SetKind("Subscription")
	if err := r.Get(ctx, types.NamespacedName{Name: subName, Namespace: subNamespace}, existing); err != nil {
		return nil // subscription not found yet — nothing to recover
	}

	labels := existing.GetLabels()
	if labels == nil || labels[GitOpsAddonLabelKey] != GitOpsAddonLabelValue {
		return nil // pre-existing subscription not managed by us — leave it alone
	}

	// Look for an InstallPlanFailed condition whose message references the conversion webhook.
	conditions, _, _ := unstructured.NestedSlice(existing.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cm["type"] != "InstallPlanFailed" || cm["status"] != "True" {
			continue
		}
		msg, _ := cm["message"].(string)
		if !strings.Contains(msg, "conversion webhook") {
			continue
		}

		// Retrieve the referenced InstallPlan name from status.installPlanRef.
		ipRef, _, _ := unstructured.NestedMap(existing.Object, "status", "installPlanRef")
		ipName, _ := ipRef["name"].(string)
		if ipName == "" {
			klog.Warningf("OLM subscription %s has InstallPlanFailed (webhook) but no installPlanRef", subName)
			return nil
		}

		klog.Infof("OLM subscription %s has InstallPlanFailed due to conversion webhook — deleting InstallPlan %s to trigger OLM retry",
			subName, ipName)
		ip := &unstructured.Unstructured{}
		ip.SetAPIVersion("operators.coreos.com/v1alpha1")
		ip.SetKind("InstallPlan")
		ip.SetName(ipName)
		ip.SetNamespace(subNamespace)
		if err := r.Delete(ctx, ip); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete failed InstallPlan %s/%s: %w", subNamespace, ipName, err)
		}
		klog.Infof("Deleted failed InstallPlan %s/%s — OLM will set InstallPlanMissing on next reconcile", subNamespace, ipName)
		return nil
	}

	return nil
}

// recoverFromGhostCompleteInstallPlan detects two related "ghost install" scenarios where
// OLM believes the operator is installed but the CSV is actually absent from the target
// namespace. In both cases the fix is to delete the subscription so the addon agent
// recreates it fresh (no installedCSV, no installPlanRef); OLM then issues a new
// InstallPlan that creates the CSV in the correct namespace.
//
// Scenario A — ghost-Present (InstallPlan Complete, CSV absent):
//
//  1. The subscription was first created in openshift-gitops-operator while the CSV
//     existed in openshift-operators (from the pre-migration install).
//  2. OLM resolved the InstallPlan, found the CSV "Present" on the cluster (in
//     openshift-operators), and marked the plan Complete without creating a local copy
//     in openshift-gitops-operator.
//  3. After the old CSV in openshift-operators is later deleted by the migration sweep,
//     the CSV is now absent everywhere — but the InstallPlan is already Complete and
//     OLM will never re-run it.
//
// Scenario B — InstallPlan already deleted, installedCSV still set:
//
//  Same root cause as Scenario A, but the stale Complete InstallPlan has already been
//  deleted (e.g. manually by the operator or by a previous recovery attempt). OLM still
//  sees installedCSV set in the subscription status and considers the operator installed,
//  so it does NOT issue a new InstallPlan. The addon is stuck waiting for the operator
//  deployment which will never appear.
func (r *GitopsAddonReconciler) recoverFromGhostCompleteInstallPlan(ctx context.Context) error {
	subName := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAME", "openshift-gitops-operator")
	subNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAMESPACE", GitOpsOperatorNamespace)

	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("operators.coreos.com/v1alpha1")
	existing.SetKind("Subscription")
	if err := r.Get(ctx, types.NamespacedName{Name: subName, Namespace: subNamespace}, existing); err != nil {
		return nil // subscription not found yet — nothing to recover
	}

	labels := existing.GetLabels()
	if labels == nil || labels[GitOpsAddonLabelKey] != GitOpsAddonLabelValue {
		return nil // pre-existing subscription not managed by us — leave it alone
	}

	// Determine the CSV name the subscription expects to be installed.
	csvName, _, _ := unstructured.NestedString(existing.Object, "status", "currentCSV")
	if csvName == "" {
		csvName, _, _ = unstructured.NestedString(existing.Object, "status", "installedCSV")
	}
	if csvName == "" {
		return nil // subscription has no CSV name yet — nothing to check
	}

	// Check whether that CSV actually exists in the target namespace.
	csv := &unstructured.Unstructured{}
	csv.SetAPIVersion("operators.coreos.com/v1alpha1")
	csv.SetKind("ClusterServiceVersion")
	csvErr := r.Get(ctx, types.NamespacedName{Name: csvName, Namespace: subNamespace}, csv)
	if csvErr == nil {
		return nil // CSV is present — subscription is healthy
	}
	if !errors.IsNotFound(csvErr) && !isNoKindMatchError(csvErr) {
		return fmt.Errorf("failed to check for CSV %s/%s: %w", subNamespace, csvName, csvErr)
	}

	// CSV is absent. Only act if the subscription's installedCSV is set (meaning OLM
	// thinks it's installed) or if the referenced InstallPlan is Complete (ghost-Present).
	// Otherwise this is just a normal pending install — leave it alone.
	installedCSV, _, _ := unstructured.NestedString(existing.Object, "status", "installedCSV")

	// Check whether there's a referenced InstallPlan and whether it's Complete.
	ipRef, _, _ := unstructured.NestedMap(existing.Object, "status", "installPlanRef")
	ipName, _ := ipRef["name"].(string)
	var ip *unstructured.Unstructured
	ipComplete := false
	if ipName != "" {
		ip = &unstructured.Unstructured{}
		ip.SetAPIVersion("operators.coreos.com/v1alpha1")
		ip.SetKind("InstallPlan")
		if err := r.Get(ctx, types.NamespacedName{Name: ipName, Namespace: subNamespace}, ip); err == nil {
			phase, _, _ := unstructured.NestedString(ip.Object, "status", "phase")
			ipComplete = (phase == "Complete")
		}
		// If the InstallPlan is not found (already deleted), ip stays nil — handled below.
	}

	// Only delete the subscription if we're in a stuck state: either the subscription
	// believes the CSV is installed (installedCSV is set) but it's absent, OR the
	// InstallPlan is Complete but didn't actually create the CSV.
	if installedCSV == "" && !ipComplete {
		return nil // normal pending install — not stuck yet
	}

	// Stuck: CSV is absent but OLM thinks the operator is installed (installedCSV set)
	// or the InstallPlan is Complete without having created the CSV. Deleting the
	// InstallPlan alone is NOT enough — OLM sees installedCSV and won't issue a new
	// plan. We must delete the subscription so the addon agent recreates it fresh
	// (no installedCSV, no installPlanRef). OLM then issues a new InstallPlan that
	// actually creates the CSV in the target namespace.
	klog.Infof("Ghost install detected: CSV %s is absent in %s but subscription installedCSV=%q / InstallPlan %q Complete=%v — deleting subscription to force fresh install",
		csvName, subNamespace, installedCSV, ipName, ipComplete)
	if ip != nil {
		if err := r.Delete(ctx, ip); err != nil && !errors.IsNotFound(err) {
			klog.Warningf("Failed to delete ghost InstallPlan %s/%s (continuing with subscription delete): %v", subNamespace, ipName, err)
		}
	}
	if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete subscription %s/%s to recover from ghost install: %w", subNamespace, subName, err)
	}
	klog.Infof("Deleted subscription %s/%s — addon agent will recreate it and OLM will issue a fresh InstallPlan",
		subNamespace, subName)
	return nil
}


// It also returns true when the subscription has the InstallPlanMissing condition,
// which forces a delete-and-recreate cycle.
// Returns false for pre-existing subscriptions not owned by the gitopsaddon label.
func (r *GitopsAddonReconciler) olmSubscriptionNeedsUpdate(ctx context.Context) bool {
	subName := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAME", "openshift-gitops-operator")
	subNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAMESPACE", GitOpsOperatorNamespace)
	channel := getOLMEnvOrDefault("OLM_SUBSCRIPTION_CHANNEL", "latest")
	source := getOLMEnvOrDefault("OLM_SUBSCRIPTION_SOURCE", "redhat-operators")
	sourceNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_SOURCE_NAMESPACE", "openshift-marketplace")
	installPlanApproval := getOLMEnvOrDefault("OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL", "Automatic")

	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("operators.coreos.com/v1alpha1")
	existing.SetKind("Subscription")
	if err := r.Get(ctx, types.NamespacedName{Name: subName, Namespace: subNamespace}, existing); err != nil {
		// Not found (or transient error): treat as "needs create".
		klog.Infof("OLM subscription %s not found in namespace %s, treating as needs create", subName, subNamespace)
		return true
	}

	labels := existing.GetLabels()
	if labels == nil || labels[GitOpsAddonLabelKey] != GitOpsAddonLabelValue {
		// Pre-existing subscription not managed by gitopsaddon — leave it alone.
		klog.Infof("OLM subscription %s not managed by gitopsaddon, skipping", subName)
		return false
	}

	// InstallPlanMissing forces a delete-recreate.
	conditions, _, _ := unstructured.NestedSlice(existing.Object, "status", "conditions")
	for _, c := range conditions {
		if cm, ok := c.(map[string]interface{}); ok {
			if cm["type"] == "InstallPlanMissing" && cm["status"] == "True" {
				klog.Infof("OLM subscription %s has InstallPlanMissing condition, treating as needs create", subName)
				return true
			}
		}
	}

	return olmSubscriptionSpecChanged(existing, channel, source, sourceNamespace, installPlanApproval)
}

// createOrUpdateOLMSubscription creates or updates the OLM subscription for openshift-gitops-operator.
// Subscription values are read from env vars (set by hub when olmSubscription.enabled=true) with
// hardcoded defaults as fallback.
func (r *GitopsAddonReconciler) createOrUpdateOLMSubscription(ctx context.Context) error {
	subName := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAME", "openshift-gitops-operator")
	subNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAMESPACE", GitOpsOperatorNamespace)
	channel := getOLMEnvOrDefault("OLM_SUBSCRIPTION_CHANNEL", "latest")
	source := getOLMEnvOrDefault("OLM_SUBSCRIPTION_SOURCE", "redhat-operators")
	sourceNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_SOURCE_NAMESPACE", "openshift-marketplace")
	installPlanApproval := getOLMEnvOrDefault("OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL", "Automatic")

	klog.Infof("OLM subscription config: name=%s, namespace=%s, channel=%s, source=%s, sourceNamespace=%s, approval=%s",
		subName, subNamespace, channel, source, sourceNamespace, installPlanApproval)

	sub := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      subName,
				"namespace": subNamespace,
				"labels": map[string]interface{}{
					GitOpsAddonLabelKey: GitOpsAddonLabelValue,
				},
			},
			"spec": map[string]interface{}{
				"channel":             channel,
				"name":                subName,
				"source":              source,
				"sourceNamespace":     sourceNamespace,
				"installPlanApproval": installPlanApproval,
				"config": map[string]interface{}{
					"env": []interface{}{
						map[string]interface{}{
							"name":  "DISABLE_DEFAULT_ARGOCD_INSTANCE",
							"value": "true",
						},
					},
				},
			},
		},
	}

	// Ensure the subscription namespace exists (custom namespace may not be pre-created)
	nsObj := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: subNamespace}, nsObj); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Creating namespace %s for OLM subscription", subNamespace)
			nsObj = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: subNamespace},
			}
			if createErr := r.Create(ctx, nsObj); createErr != nil && !errors.IsAlreadyExists(createErr) {
				return fmt.Errorf("failed to create namespace %s for OLM subscription: %w", subNamespace, createErr)
			}
		} else {
			return fmt.Errorf("failed to check namespace %s: %w", subNamespace, err)
		}
	}

	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("operators.coreos.com/v1alpha1")
	existing.SetKind("Subscription")
	err := r.Get(ctx, types.NamespacedName{Name: subName, Namespace: subNamespace}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Creating OLM subscription %s in namespace %s", subName, subNamespace)
			return r.Create(ctx, sub)
		}
		return fmt.Errorf("failed to check for existing OLM subscription: %w", err)
	}

	labels := existing.GetLabels()
	if labels != nil && labels[GitOpsAddonLabelKey] == GitOpsAddonLabelValue {
		// Check for InstallPlanMissing condition — if present, delete and recreate
		// to force OLM to generate a new install plan
		conditions, _, _ := unstructured.NestedSlice(existing.Object, "status", "conditions")
		for _, c := range conditions {
			if cm, ok := c.(map[string]interface{}); ok {
				if cm["type"] == "InstallPlanMissing" && cm["status"] == "True" {
					klog.Infof("OLM subscription %s has InstallPlanMissing condition, deleting and recreating", subName)
					if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
						return fmt.Errorf("failed to delete stale OLM subscription: %w", err)
					}
					nsName := types.NamespacedName{Name: subName, Namespace: subNamespace}
					pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					if err := wait.PollUntilContextCancel(pollCtx, time.Second, true, func(ctx context.Context) (bool, error) {
						check := &unstructured.Unstructured{}
						check.SetAPIVersion("operators.coreos.com/v1alpha1")
						check.SetKind("Subscription")
						if err := r.Get(ctx, nsName, check); errors.IsNotFound(err) {
							return true, nil
						}
						return false, nil
					}); err != nil {
						return fmt.Errorf("timed out waiting for stale OLM subscription %s to be deleted: %w", subName, err)
					}
					return r.Create(ctx, sub)
				}
			}
		}

		// Skip the Update call when nothing meaningful has changed.  An unconditional
		// Update bumps resourceVersion on the server, which triggers OLM watches and
		// can cause unnecessary InstallPlan creation and rolling addon pod restarts.
		if !olmSubscriptionSpecChanged(existing, channel, source, sourceNamespace, installPlanApproval) {
			klog.V(4).Infof("OLM subscription %s spec unchanged, skipping update", subName)
			return nil
		}
		klog.Infof("OLM subscription %s spec changed, updating", subName)
		sub.SetResourceVersion(existing.GetResourceVersion())
		return r.Update(ctx, sub)
	}

	klog.Infof("OLM subscription %s already exists in namespace %s without gitopsaddon label (pre-existing), skipping", subName, subNamespace)
	return nil
}

// createOrUpdateOperatorGroup ensures an AllNamespaces OperatorGroup exists in the
// given namespace so that OLM can install a Subscription there. If an OperatorGroup
// already exists in the namespace (pre-existing or from a previous install), it is
// left unchanged to avoid conflicts.
func (r *GitopsAddonReconciler) createOrUpdateOperatorGroup(ctx context.Context, namespace string) error {
	// List all OperatorGroups in the namespace; if any exist, leave them alone.
	ogList := &unstructured.UnstructuredList{}
	ogList.SetAPIVersion("operators.coreos.com/v1")
	ogList.SetKind("OperatorGroupList")
	if err := r.List(ctx, ogList, client.InNamespace(namespace)); err != nil {
		if isNoKindMatchError(err) {
			// OLM not present on this cluster, skip silently.
			klog.Infof("OperatorGroup API not available, skipping OperatorGroup creation in %s", namespace)
			return nil
		}
		return fmt.Errorf("failed to list OperatorGroups in %s: %w", namespace, err)
	}
	if len(ogList.Items) > 0 {
		klog.Infof("OperatorGroup already present in namespace %s (%s), skipping creation", namespace, ogList.Items[0].GetName())
		return nil
	}

	og := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1",
			"kind":       "OperatorGroup",
			"metadata": map[string]interface{}{
				"name":      "gitops-addon-operator-group",
				"namespace": namespace,
				"labels": map[string]interface{}{
					GitOpsAddonLabelKey: GitOpsAddonLabelValue,
				},
			},
			"spec": map[string]interface{}{
				// AllNamespaces mode: the openshift-gitops-operator does not
				// support OwnNamespace; omitting targetNamespaces tells OLM
				// to use AllNamespaces install mode.
			},
		},
	}
	klog.Infof("Creating AllNamespaces OperatorGroup in %s", namespace)
	if err := r.Create(ctx, og); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create OperatorGroup in %s: %w", namespace, err)
	}
	return nil
}

// migrateSubscriptionFromNamespace deletes any gitopsaddon-labeled OLM Subscription
// found in fromNamespace, then immediately deletes its installed CSV so OLM tears down
// the operator Deployment without waiting for garbage-collection (which can take minutes).
// This handles the transition from the old default (openshift-operators) to the new default
// (openshift-gitops-operator) so that OLM does not run two competing AllNamespaces
// operator instances during the migration window.
func (r *GitopsAddonReconciler) migrateSubscriptionFromNamespace(ctx context.Context, fromNamespace string) error {
	subName := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAME", "openshift-gitops-operator")

	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("operators.coreos.com/v1alpha1")
	existing.SetKind("Subscription")
	err := r.Get(ctx, types.NamespacedName{Name: subName, Namespace: fromNamespace}, existing)
	if err != nil && !errors.IsNotFound(err) && !isNoKindMatchError(err) {
		return fmt.Errorf("failed to check for old subscription in %s: %w", fromNamespace, err)
	}

	if err == nil {
		// Subscription still exists in the old namespace — delete it and its CSV.
		labels := existing.GetLabels()
		if labels == nil || labels[GitOpsAddonLabelKey] != GitOpsAddonLabelValue {
			// Pre-existing subscription not owned by us — leave it alone.
			klog.Infof("Subscription %s in %s not owned by gitopsaddon, skipping migration", subName, fromNamespace)
			return nil
		}

		// Read status.installedCSV BEFORE deleting the subscription. Once the
		// subscription is gone OLM clears the status field and we can't recover it.
		csvName, _, _ := unstructured.NestedString(existing.Object, "status", "installedCSV")

		klog.Infof("Migrating: deleting old gitopsaddon subscription %s from %s", subName, fromNamespace)
		if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete old subscription %s/%s: %w", fromNamespace, subName, err)
		}

		// Delete the CSV that was installed by the old subscription so OLM removes
		// the operator Deployment immediately instead of waiting for async GC.
		// Without this, two AllNamespaces operator instances can compete over the
		// same ArgoCD CRDs for several minutes while OLM slowly GCs the old CSV.
		//
		// Ownership is already established by the subscription chain above: we confirmed
		// the subscription carries our gitopsaddon label, and csvName came directly from
		// that subscription's status.installedCSV. No additional label check on the CSV
		// is needed — OLM provenance labels are unreliable because they are also present
		// on OperatorHub-installed CSVs in the same namespace.
		if csvName != "" {
			klog.Infof("Migrating: deleting old CSV %s/%s", fromNamespace, csvName)
			csv := &unstructured.Unstructured{}
			csv.SetAPIVersion("operators.coreos.com/v1alpha1")
			csv.SetKind("ClusterServiceVersion")
			csv.SetName(csvName)
			csv.SetNamespace(fromNamespace)
			if err := r.Delete(ctx, csv); err != nil && !errors.IsNotFound(err) && !isNoKindMatchError(err) {
				klog.Warningf("Migration: failed to delete old CSV %s/%s: %v", fromNamespace, csvName, err)
			}
		}
	}

	// Always sweep for any remaining gitops-operator CSVs in the old namespace,
	// even if the subscription was already deleted before this reconcile ran.
	// This handles the upgrade scenario where: the old subscription in openshift-operators
	// was deleted (by a prior run or manually) but OLM left the CSV behind as an orphan,
	// causing OLM to keep the operator pod running there and refuse to deploy a new one
	// in openshift-gitops-operator (the correct target namespace).
	// Only CSVs not claimed by any surviving subscription are deleted.
	if err := deleteRemainingGitOpsCSVs(ctx, r.Client, fromNamespace, subName); err != nil {
		klog.Warningf("Migration: failed to clean remaining CSVs in %s: %v", fromNamespace, err)
	}

	return nil
}

// waitForOperatorReady waits for the ArgoCD operator deployment to be ready
func (r *GitopsAddonReconciler) waitForOperatorReady(timeout time.Duration, extraNamespaces ...string) error {
	// Skip waiting in test environments with fake clients
	if r.Config != nil && r.Config.Host == "fake://test" {
		klog.Info("Using fake client, skipping operator ready wait")
		return nil
	}

	deploymentName := "openshift-gitops-operator-controller-manager"
	// Check the primary namespace plus any additional namespaces (e.g., the OLM subscription namespace)
	namespacesToCheck := []string{GitOpsOperatorNamespace}
	for _, ns := range extraNamespaces {
		if ns != "" && ns != GitOpsOperatorNamespace {
			namespacesToCheck = append(namespacesToCheck, ns)
		}
	}

	klog.Infof("Waiting for ArgoCD operator deployment %s to be ready (checking namespaces: %v)...", deploymentName, namespacesToCheck)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		foundAny := false
		for _, ns := range namespacesToCheck {
			deployment := &appsv1.Deployment{}
			key := types.NamespacedName{Name: deploymentName, Namespace: ns}
			err := r.Get(ctx, key, deployment)
			if err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				klog.Errorf("Error checking for operator deployment %s/%s: %v", ns, deploymentName, err)
				continue
			}

			foundAny = true
			for _, cond := range deployment.Status.Conditions {
				if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
					klog.Infof("Operator deployment %s/%s is available", ns, deploymentName)
					return true, nil
				}
			}

			desiredReplicas := int32(1)
			if deployment.Spec.Replicas != nil {
				desiredReplicas = *deployment.Spec.Replicas
			}
			klog.Infof("Operator deployment %s/%s not yet available (replicas: %d/%d)", ns, deploymentName, deployment.Status.ReadyReplicas, desiredReplicas)
		}

		if !foundAny {
			klog.Infof("Operator deployment %s not found in any namespace yet, continuing to wait...", deploymentName)
		}
		return false, nil
	})
}

// CreateUpdateNamespace creates or updates a namespace with proper labels
// The addon.open-cluster-management.io/namespace: true label enables the klusterlet addon
// to automatically copy the image pull secret to this namespace
func (r *GitopsAddonReconciler) CreateUpdateNamespace(nameSpaceKey types.NamespacedName) error {
	namespace := &corev1.Namespace{}
	err := r.Get(context.TODO(), nameSpaceKey, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			namespace = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nameSpaceKey.Name,
					Labels: map[string]string{
						"addon.open-cluster-management.io/namespace": "true",
						GitOpsAddonLabelKey:                          GitOpsAddonLabelValue,
					},
				},
			}

			if err := r.Create(context.TODO(), namespace); err != nil {
				klog.Errorf("Failed to create the %s namespace, err: %v", nameSpaceKey.Name, err)
				return err
			}
			klog.Infof("Successfully created namespace %s", nameSpaceKey.Name)
		} else {
			klog.Errorf("Failed to get the %s namespace, err: %v", nameSpaceKey.Name, err)
			return err
		}
	}

	// Check if namespace should be skipped due to annotation
	annotations := namespace.GetAnnotations()
	if annotations != nil && annotations["gitops-addon.open-cluster-management.io/skip"] == "true" {
		klog.Infof("Skipping namespace %s due to skip annotation", namespace.Name)
		return nil
	}

	// Ensure the labels are set (in case namespace already existed without them).
	// Only update when a label is actually missing to avoid bumping resourceVersion
	// on every reconcile (which triggers unnecessary watch events).
	needsLabelUpdate := namespace.Labels == nil ||
		namespace.Labels["addon.open-cluster-management.io/namespace"] != "true" ||
		namespace.Labels[GitOpsAddonLabelKey] != GitOpsAddonLabelValue

	if needsLabelUpdate {
		if namespace.Labels == nil {
			namespace.Labels = make(map[string]string)
		}
		namespace.Labels["addon.open-cluster-management.io/namespace"] = "true"
		namespace.Labels[GitOpsAddonLabelKey] = GitOpsAddonLabelValue

		if err := r.Update(context.TODO(), namespace); err != nil {
			klog.Errorf("Failed to update the labels to the %s namespace, err: %v", nameSpaceKey.Name, err)
			return err
		}
		klog.Infof("Updated labels on namespace %s", nameSpaceKey.Name)
	}

	// If on the hcp hosted cluster, there is no klusterlet operator
	// As a result, the open-cluster-management-image-pull-credentials secret is not automatically synced up
	// to the new namespace even though the addon.open-cluster-management.io/namespace: true label is specified
	// To support all kinds of clusters, we proactively copy the original git addon
	// open-cluster-management-image-pull-credentials secret to the new namespace
	err = r.copyImagePullSecret(nameSpaceKey)
	if err != nil {
		// If we couldn't copy the secret (e.g., source not found), don't wait for it
		// The klusterlet addon label should trigger automatic copy if available
		return nil
	}

	// Wait for the image pull secret to be generated (either by klusterlet label or our copy)
	secretName := "open-cluster-management-image-pull-credentials"
	secret := &corev1.Secret{}
	timeout := time.Minute
	interval := time.Second * 2
	start := time.Now()

	for time.Since(start) < timeout {
		err = r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: nameSpaceKey.Name}, secret)
		if err == nil {
			// Secret found
			klog.Infof("Secret %s found in namespace %s", secretName, nameSpaceKey.Name)
			return nil
		}

		if !errors.IsNotFound(err) {
			klog.Errorf("Error while waiting for secret %s in namespace %s: %v", secretName, nameSpaceKey.Name, err)
			return err
		}

		klog.Infof("the image pull credentials secret is NOT found in %s, wait for the next check, err: %v", nameSpaceKey.Name, err)

		time.Sleep(interval)
	}

	// Timeout reached
	klog.Errorf("Timeout waiting for secret %s in namespace %s", secretName, nameSpaceKey.Name)

	return fmt.Errorf("timeout waiting for secret %s in namespace %s", secretName, nameSpaceKey.Name)
}

// copyImagePullSecret copies the image pull secret to the target namespace
// This is a fallback for HCP hosted clusters where klusterlet doesn't automatically sync secrets
func (r *GitopsAddonReconciler) copyImagePullSecret(nameSpaceKey types.NamespacedName) error {
	secretName := "open-cluster-management-image-pull-credentials"
	secret := &corev1.Secret{}
	gitopsAddonNs := utils.GetComponentNamespace("open-cluster-management-agent-addon")

	// Get the original gitops addon image pull secret
	err := r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: gitopsAddonNs}, secret)
	if err != nil {
		klog.Errorf("gitops addon image pull secret not found. secret: %v/%v, err: %v", gitopsAddonNs, secretName, err)
		return err
	}

	// Prepare the new Secret
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        secretName,
			Namespace:   nameSpaceKey.Name,
			Labels:      secret.Labels,
			Annotations: secret.Annotations,
		},
		Data:       secret.Data,
		StringData: secret.StringData,
		Type:       secret.Type,
	}

	newSecret.ObjectMeta.UID = ""
	newSecret.ObjectMeta.ResourceVersion = ""
	newSecret.ObjectMeta.CreationTimestamp = metav1.Time{}
	newSecret.ObjectMeta.DeletionTimestamp = nil

	// Create/update the secret in target namespace
	existingSecret := &corev1.Secret{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: nameSpaceKey.Name}, existingSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create the secret
			if err := r.Create(context.TODO(), newSecret); err != nil {
				if !errors.IsAlreadyExists(err) {
					klog.Errorf("Failed to create image pull secret in namespace %s: %v", nameSpaceKey.Name, err)
					return err
				}
			}
			klog.Infof("Created image pull secret in namespace %s", nameSpaceKey.Name)
			return nil
		}
		return err
	}

	// Only update when the secret content actually changed, to avoid bumping
	// resourceVersion on every reconcile.
	if existingSecret.Type == newSecret.Type && secretDataEqual(existingSecret.Data, newSecret.Data) {
		klog.Infof("Image pull secret in namespace %s is up to date, skipping update", nameSpaceKey.Name)
		return nil
	}

	existingSecret.Data = newSecret.Data
	existingSecret.Type = newSecret.Type
	if err := r.Update(context.TODO(), existingSecret); err != nil {
		klog.Errorf("Failed to update image pull secret in namespace %s: %v", nameSpaceKey.Name, err)
		return err
	}
	klog.Infof("Updated image pull secret in namespace %s", nameSpaceKey.Name)
	return nil
}

// WaitForImagePullSecret waits for the image pull credentials secret to exist in the addon namespace
// This secret is propagated by ACM from the hub's multiclusterhub-operator-pull-secret
func (r *GitopsAddonReconciler) WaitForImagePullSecret(nameSpaceKey types.NamespacedName) error {
	secretName := "open-cluster-management-image-pull-credentials"
	timeout := 2 * time.Minute
	interval := 5 * time.Second

	secret := &corev1.Secret{}
	start := time.Now()

	for time.Since(start) < timeout {
		err := r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: nameSpaceKey.Name}, secret)
		if err == nil {
			// Secret found
			klog.Infof("Secret %s found in namespace %s", secretName, nameSpaceKey.Name)
			return nil
		}

		if !errors.IsNotFound(err) {
			klog.Errorf("Error while waiting for secret %s in namespace %s: %v", secretName, nameSpaceKey.Name, err)
			return err
		}

		klog.Infof("the image pull credentials secret is NOT found, wait for the next check, err: %v", err)

		time.Sleep(interval)
	}

	// Timeout reached
	klog.Errorf("Timeout waiting for secret %s in namespace %s", secretName, nameSpaceKey.Name)

	return fmt.Errorf("timeout waiting for secret %s in namespace %s", secretName, nameSpaceKey.Name)
}

// patchServiceAccountsWithImagePullSecrets patches all ServiceAccounts in the given namespace
// with imagePullSecrets. This is needed for non-OCP clusters (like Kind, EKS) where node-level
// pull secrets don't exist. Without this, pods can't authenticate to registry.redhat.io.
func (r *GitopsAddonReconciler) patchServiceAccountsWithImagePullSecrets(targetNS string) error {
	secretName := "open-cluster-management-image-pull-credentials"

	secret := &corev1.Secret{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: targetNS}, secret)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(2).Infof("Image pull secret %s not found in namespace %s, skipping SA patching", secretName, targetNS)
			return nil
		}
		return fmt.Errorf("failed to check for image pull secret: %v", err)
	}

	saList := &corev1.ServiceAccountList{}
	if err := r.List(context.TODO(), saList, &client.ListOptions{Namespace: targetNS}); err != nil {
		return fmt.Errorf("failed to list ServiceAccounts: %v", err)
	}

	secretRef := corev1.LocalObjectReference{Name: secretName}
	patchedCount := 0

	for i := range saList.Items {
		sa := &saList.Items[i]

		found := false
		for _, ref := range sa.ImagePullSecrets {
			if ref.Name == secretName {
				found = true
				break
			}
		}

		if !found {
			sa.ImagePullSecrets = append(sa.ImagePullSecrets, secretRef)
			if err := r.Update(context.TODO(), sa); err != nil {
				klog.Warningf("Failed to patch ServiceAccount %s/%s: %v", targetNS, sa.Name, err)
				continue
			}
			klog.Infof("Patched ServiceAccount %s/%s with imagePullSecrets", targetNS, sa.Name)
			patchedCount++
		}
	}

	if patchedCount > 0 {
		klog.Infof("Patched %d ServiceAccounts with imagePullSecrets in %s", patchedCount, targetNS)
	}

	return nil
}

// patchArgoCDServiceAccountsWithImagePullSecrets patches SAs in both the ArgoCD namespace and
// the operator namespace. On non-OCP clusters neither namespace has node-level pull secrets,
// so all SAs need the explicit imagePullSecrets reference.
func (r *GitopsAddonReconciler) patchArgoCDServiceAccountsWithImagePullSecrets() error {
	if err := r.patchServiceAccountsWithImagePullSecrets(getArgoCDNamespace()); err != nil {
		return err
	}
	return r.patchServiceAccountsWithImagePullSecrets(GitOpsOperatorNamespace)
}

// deletePodsWithImagePullIssues deletes pods in the ArgoCD namespace that have ImagePullBackOff or ErrImagePull status.
func (r *GitopsAddonReconciler) deletePodsWithImagePullIssues() error {
	return r.deletePodsWithImagePullIssuesInNamespace(getArgoCDNamespace())
}

// deletePodsWithImagePullIssuesInNamespace deletes pods with ImagePullBackOff or ErrImagePull status
// in the given namespace, forcing Kubernetes to recreate them with the patched ServiceAccounts.
func (r *GitopsAddonReconciler) deletePodsWithImagePullIssuesInNamespace(targetNS string) error {
	podList := &corev1.PodList{}
	if err := r.List(context.TODO(), podList, &client.ListOptions{Namespace: targetNS}); err != nil {
		return fmt.Errorf("failed to list pods: %v", err)
	}

	deletedCount := 0
	for i := range podList.Items {
		pod := &podList.Items[i]
		// Check if any container has ImagePullBackOff or ErrImagePull status
		hasImagePullIssue := false
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if containerStatus.State.Waiting != nil {
				reason := containerStatus.State.Waiting.Reason
				if reason == "ImagePullBackOff" || reason == "ErrImagePull" {
					hasImagePullIssue = true
					break
				}
			}
		}
		// Also check init containers
		for _, containerStatus := range pod.Status.InitContainerStatuses {
			if containerStatus.State.Waiting != nil {
				reason := containerStatus.State.Waiting.Reason
				if reason == "ImagePullBackOff" || reason == "ErrImagePull" {
					hasImagePullIssue = true
					break
				}
			}
		}

		if hasImagePullIssue {
			klog.Infof("Deleting pod %s/%s with image pull issues to force restart", pod.Namespace, pod.Name)
			if err := r.Delete(context.TODO(), pod); err != nil {
				if !errors.IsNotFound(err) {
					klog.Warningf("Failed to delete pod %s/%s: %v", pod.Namespace, pod.Name, err)
				}
			} else {
				deletedCount++
			}
		}
	}

	if deletedCount > 0 {
		klog.Infof("Deleted %d pods with image pull issues", deletedCount)
	}

	return nil
}

// secretDataEqual returns true when two secret Data maps have identical keys and values.
func secretDataEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !bytes.Equal(av, bv) {
			return false
		}
	}
	return true
}

// deleteDefaultGitopsServiceIfStale removes the cluster-scoped GitopsService CR named
// "cluster" when it should not exist on this managed cluster.
//
// Background: In ACM 2.16, the OLM Subscription embedded in the gitops-addon-olm
// ManifestWork did NOT set DISABLE_DEFAULT_ARGOCD_INSTANCE=true. As a result, the
// openshift-gitops-operator created a default GitopsService CR ("cluster"), which in
// turn created the openshift-gitops ArgoCD instance. After upgrading to ACM 2.17, the
// new agent subscription carries DISABLE_DEFAULT_ARGOCD_INSTANCE=true, so the default
// instance is no longer wanted. However, neither OLM nor the operator automatically
// removes the existing GitopsService CR when DISABLE_DEFAULT_ARGOCD_INSTANCE is first
// enabled.
//
// This function detects that condition and deletes the GitopsService CR so that the
// operator's ownerReference-based garbage collection removes the openshift-gitops ArgoCD
// CR, leaving only the Policy-managed acm-openshift-gitops instance.
//
// It is safe to call on every reconcile: the function is a no-op when the GitopsService
// CR does not exist, when the addon-created ArgoCD CR (acm-openshift-gitops) is absent
// (meaning the operator setup is still incomplete), or when the ArgoCD CRD itself is not
// installed (non-OCP clusters using the embedded operator path).
func (r *GitopsAddonReconciler) deleteDefaultGitopsServiceIfStale(ctx context.Context) error {
	// Only act when the addon-created ArgoCD instance exists, which confirms the operator
	// is running and the Policy has been applied. Without this guard we might delete the
	// GitopsService CR prematurely, before the intended ArgoCD instance is up.
	argoCDNamespace := getArgoCDNamespace()
	acmArgoCDName := "acm-openshift-gitops"

	acmArgoCD := &unstructured.Unstructured{}
	acmArgoCD.SetAPIVersion("argoproj.io/v1beta1")
	acmArgoCD.SetKind("ArgoCD")
	if err := r.Get(ctx, types.NamespacedName{Name: acmArgoCDName, Namespace: argoCDNamespace}, acmArgoCD); err != nil {
		if errors.IsNotFound(err) || isNoKindMatchError(err) {
			// Addon-managed ArgoCD not present yet — skip cleanup to avoid racing with setup.
			return nil
		}
		return fmt.Errorf("failed to check for addon-created ArgoCD CR: %w", err)
	}

	// Check whether the default GitopsService CR exists.
	gitOpsService := &unstructured.Unstructured{}
	gitOpsService.SetAPIVersion("pipelines.openshift.io/v1alpha1")
	gitOpsService.SetKind("GitopsService")
	gitOpsService.SetName("cluster")

	if err := r.Get(ctx, types.NamespacedName{Name: "cluster"}, gitOpsService); err != nil {
		if errors.IsNotFound(err) || isNoKindMatchError(err) {
			return nil // nothing to do
		}
		return fmt.Errorf("failed to check for default GitopsService CR: %w", err)
	}

	// GitopsService exists — delete it so the operator garbage-collects the default
	// openshift-gitops ArgoCD CR via ownerReferences.
	klog.Infof("Deleting stale default GitopsService CR 'cluster' — it was created by the 2.16 OLM operator (without DISABLE_DEFAULT_ARGOCD_INSTANCE=true) and is no longer needed")
	if err := r.Delete(ctx, gitOpsService); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete default GitopsService CR: %w", err)
	}
	klog.Info("Deleted stale default GitopsService CR 'cluster'; the openshift-gitops ArgoCD CR will be garbage-collected")
	return nil
}
