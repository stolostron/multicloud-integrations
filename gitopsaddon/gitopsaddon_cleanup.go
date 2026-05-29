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
	"context"
	stderrors "errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PauseMarkerName is the name of the ConfigMap used to pause the gitops-addon controller
	PauseMarkerName = "gitops-addon-pause"
	// AddonNamespace is the namespace where the gitops-addon controller runs
	AddonNamespace = "open-cluster-management-agent-addon"
	// AddonDeploymentName is the name of the gitops-addon deployment
	AddonDeploymentName = "gitops-addon"
	// GitOpsAddonLabelKey is the label key used to mark all resources created by gitopsaddon
	GitOpsAddonLabelKey = "apps.open-cluster-management.io/gitopsaddon"
	// GitOpsAddonLabelValue is the label value for resources owned by gitopsaddon
	GitOpsAddonLabelValue = "true"
)

// uninstallGitopsAgent uninstalls the Gitops agent addon for cleanup reconciler
func (r *GitopsAddonCleanupReconciler) uninstallGitopsAgent(ctx context.Context) error {
	return uninstallGitopsAgentInternal(ctx, r.Client, GitOpsOperatorNamespace)
}

// uninstallGitopsAgentInternal performs the actual uninstall logic.
// On hub clusters, only the addon-created ArgoCD CR is deleted (operator/OLM resources are shared).
// On remote clusters, a full cleanup is performed.
func uninstallGitopsAgentInternal(ctx context.Context, c client.Client, gitopsOperatorNS string) error {
	klog.Infof("Starting Gitops agent addon uninstall - gitopsOperatorNS: %s, gitopsNS: %s", gitopsOperatorNS, GitOpsNamespace)

	isHub, err := isHubClusterForCleanup(ctx, c)
	if err != nil {
		return fmt.Errorf("failed to detect cluster type during cleanup, aborting: %w", err)
	}
	if isHub {
		return uninstallOnHub(ctx, c)
	}
	return uninstallOnManagedCluster(ctx, c, gitopsOperatorNS)
}

// isHubClusterForCleanup detects if this is the hub cluster during cleanup.
// Uses the same signals as isHubCluster but works with a raw client (no reconciler).
// Returns (bool, error) so callers can fail closed on unexpected API errors
// instead of defaulting to the destructive managed-cluster cleanup path.
func isHubClusterForCleanup(ctx context.Context, c client.Client) (bool, error) {
	cmList := &unstructured.UnstructuredList{}
	cmList.SetAPIVersion("operator.open-cluster-management.io/v1")
	cmList.SetKind("ClusterManagerList")
	if err := c.List(ctx, cmList); err != nil {
		if !apierrors.IsNotFound(err) && !isNoKindMatchError(err) {
			return false, fmt.Errorf("failed to list ClusterManager resources: %w", err)
		}
	} else if len(cmList.Items) > 0 {
		klog.Info("Hub cluster detected during cleanup: ClusterManager resource found")
		return true, nil
	}

	mcList := &unstructured.UnstructuredList{}
	mcList.SetAPIVersion("cluster.open-cluster-management.io/v1")
	mcList.SetKind("ManagedClusterList")
	if err := c.List(ctx, mcList); err != nil {
		if !apierrors.IsNotFound(err) && !isNoKindMatchError(err) {
			return false, fmt.Errorf("failed to list ManagedCluster resources: %w", err)
		}
	} else {
		for i := range mcList.Items {
			mc := &mcList.Items[i]
			if mc.GetName() == "local-cluster" {
				klog.Info("Hub cluster detected during cleanup: ManagedCluster 'local-cluster' found")
				return true, nil
			}
			labels := mc.GetLabels()
			if labels != nil {
				if v, ok := labels["local-cluster"]; ok && v == "true" {
					klog.Infof("Hub cluster detected during cleanup: ManagedCluster '%s' has local-cluster=true label", mc.GetName())
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// isNoKindMatchError returns true if the error indicates the CRD is not installed.
func isNoKindMatchError(err error) bool {
	if err == nil {
		return false
	}
	return meta.IsNoMatchError(err)
}

// uninstallOnHub performs a conservative cleanup on the hub cluster.
// Only deletes the addon-created ArgoCD CR — operator, OLM, and shared resources are preserved.
func uninstallOnHub(ctx context.Context, c client.Client) error {
	klog.Info("Hub cluster cleanup: only deleting addon-created ArgoCD CRs")
	var errors []error

	klog.Info("Step 0: Creating pause marker")
	if err := createPauseMarker(ctx, c); err != nil {
		klog.Errorf("Error creating pause marker (continuing): %v", err)
		errors = append(errors, fmt.Errorf("failed to create pause marker: %w", err))
	}

	klog.Info("Step 1: Deleting ArgoCD CRs with gitopsaddon label (all namespaces)")
	if err := deleteArgoCDCR(ctx, c); err != nil {
		klog.Errorf("Error deleting ArgoCD CRs (continuing): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete ArgoCD CRs: %w", err))
	}

	klog.Info("Step 2: Deleting pause marker")
	if err := deletePauseMarker(ctx, c); err != nil {
		klog.Warningf("Failed to delete pause marker (continuing): %v", err)
	}

	klog.Info("Final Step: Deleting gitops-addon RBAC (self-referencing, best-effort)")
	for _, name := range []string{AddonDeploymentName, AddonDeploymentName + "-cleanup"} {
		crb := &unstructured.Unstructured{}
		crb.SetAPIVersion("rbac.authorization.k8s.io/v1")
		crb.SetKind("ClusterRoleBinding")
		crb.SetName(name)
		if delErr := c.Delete(ctx, crb); delErr != nil && !apierrors.IsNotFound(delErr) {
			klog.Warningf("Best-effort: failed to delete %s ClusterRoleBinding: %v", name, delErr)
		}
		cr := &unstructured.Unstructured{}
		cr.SetAPIVersion("rbac.authorization.k8s.io/v1")
		cr.SetKind("ClusterRole")
		cr.SetName(name)
		if delErr := c.Delete(ctx, cr); delErr != nil && !apierrors.IsNotFound(delErr) {
			klog.Warningf("Best-effort: failed to delete %s ClusterRole: %v", name, delErr)
		}
	}

	if len(errors) > 0 {
		return stderrors.Join(errors...)
	}
	klog.Info("Successfully completed hub cluster cleanup")
	return nil
}

// uninstallOnManagedCluster performs a full cleanup on remote managed clusters.
// The cleanup strategy depends on how the operator was installed:
//   - OLM mode (OCP): delete Subscription + CSV and let OLM handle operator teardown.
//     Manual resource deletion is skipped because OLM cleans up its own resources,
//     and OpenShift auto-recreates system resources (kube-root-ca.crt, default SA, etc.)
//     that would cause the retry loop to fight pointlessly for minutes.
//   - Embedded mode (Kind/EKS): manually delete all operator resources since there is
//     no OLM to handle cleanup.
func uninstallOnManagedCluster(ctx context.Context, c client.Client, gitopsOperatorNS string) error {
	klog.Info("Managed cluster cleanup: full uninstall")
	var errors []error

	klog.Infof("Step 0a: Creating pause marker in namespace: %s", AddonNamespace)
	if err := createPauseMarker(ctx, c); err != nil {
		klog.Errorf("Error creating pause marker (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to create pause marker: %w", err))
	}

	klog.Info("Step 0b: Scaling down addon agent to prevent reconciliation during cleanup")
	scaleDownAddonAgent(ctx, c)

	pauseSettleDuration := getPauseSettleDuration()
	klog.Infof("Waiting %v for addon agent pod termination...", pauseSettleDuration)
	time.Sleep(pauseSettleDuration)

	klog.Info("Step 1: Deleting GitOpsService CR (if exists)")
	if err := deleteGitOpsServiceCR(ctx, c); err != nil {
		klog.Errorf("Error deleting GitOpsService CR (continuing with cleanup): %v", err)
	}

	// Step 2: Delete ArgoCD CRs with gitopsaddon label. The operator processes the
	// ArgoCD CR finalizer and cleans up all workloads it created (redis, repo-server,
	// app-controller, server, etc.). We wait as long as needed — the operator MUST
	// finish processing the finalizer before we remove the operator in step 3.
	// NEVER force-strip the finalizer. NEVER blindly delete ArgoCD workloads.
	klog.Info("Step 2: Deleting ArgoCD CRs with gitopsaddon label (all namespaces) — waiting for operator to process finalizer")
	if err := deleteArgoCDCR(ctx, c); err != nil {
		klog.Errorf("Error deleting ArgoCD CR (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete ArgoCD CR: %w", err))
	}

	// Step 3: Remove the operator. Only runs AFTER ArgoCD CR is fully gone (step 2
	// waited for the finalizer). This ordering is critical — if we remove the operator
	// before the ArgoCD CR finalizer is processed, the operator can't clean up its
	// own workloads and they become orphaned.
	klog.Info("Step 3: Removing operator installation")
	olmFound, olmErr := deleteOLMResourcesAndReport(ctx, c)
	if olmErr != nil {
		klog.Errorf("Error deleting OLM resources (continuing with cleanup): %v", olmErr)
		errors = append(errors, fmt.Errorf("failed to delete OLM resources: %w", olmErr))
	}

	if olmFound {
		// OLM mode (OCP): OLM handles operator resource cleanup when the CSV is deleted.
		// The ArgoCD workloads were already cleaned up by the operator in step 2 (finalizer).
		// Skip manual operator namespace deletion — it would fight OpenShift auto-recreated
		// system resources (kube-root-ca.crt, default/builder/deployer SAs, system RoleBindings).
		klog.Info("Step 4: SKIPPED operator namespace cleanup — OLM mode detected")
	} else {
		// Embedded mode (Kind/EKS): no OLM to clean up, must delete labeled resources manually.
		// Only deletes resources with the gitopsaddon label — never touches system resources.
		klog.Info("Step 4: Deleting gitopsaddon-labeled operator resources from namespace: " + gitopsOperatorNS)
		if err := deleteOperatorResources(ctx, c, gitopsOperatorNS); err != nil {
			klog.Errorf("Error deleting openshift-gitops-operator resources (continuing with cleanup): %v", err)
			errors = append(errors, fmt.Errorf("failed to delete openshift-gitops-operator resources: %w", err))
		}

		// The OCM work-agent may temporarily restore the addon agent Deployment (from the
		// regular ManifestWork) before the ManifestWork deletion takes effect. The restored
		// agent can reconcile and recreate operator resources. Re-scale down the addon agent
		// and run a final cleanup pass to catch any late recreations.
		klog.Info("Step 5: Re-scaling down addon agent + final operator cleanup pass")
		scaleDownAddonAgent(ctx, c)
		time.Sleep(15 * time.Second)
		if err := deleteOperatorResources(ctx, c, gitopsOperatorNS); err != nil {
			klog.Warningf("Final operator cleanup pass: %v", err)
		}
	}

	// If the pause marker has an ownerReference to the addon Deployment, leave it for
	// Kubernetes garbage collection — the ManifestWork will delete the Deployment, which
	// triggers GC of the marker. Deleting it early would unpause the addon agent, which
	// could recreate operator resources before the ManifestWork kills the agent.
	//
	// However, createPauseMarker() creates the marker WITHOUT an ownerReference when
	// the Deployment is not found. In that case, GC won't clean it up and it must be
	// deleted explicitly to avoid leaving the addon permanently paused.
	if err := deleteOrphanedPauseMarker(ctx, c); err != nil {
		klog.Warningf("Failed to check/delete orphaned pause marker: %v", err)
	}

	klog.Info("Final Step: Deleting gitops-addon RBAC (self-referencing, best-effort)")
	// The gitops-addon-cleanup ClusterRole/CRB (deployed by the pre-delete ManifestWork)
	// ensures we retain permissions even after the gitops-addon CR/CRB (deployed by the
	// regular ManifestWork) are already gone. Delete the main RBAC first, then cleanup RBAC.
	for _, name := range []string{AddonDeploymentName, AddonDeploymentName + "-cleanup"} {
		crb := &unstructured.Unstructured{}
		crb.SetAPIVersion("rbac.authorization.k8s.io/v1")
		crb.SetKind("ClusterRoleBinding")
		crb.SetName(name)
		if delErr := c.Delete(ctx, crb); delErr != nil && !apierrors.IsNotFound(delErr) {
			klog.Warningf("Best-effort: failed to delete %s ClusterRoleBinding: %v", name, delErr)
		}
		cr := &unstructured.Unstructured{}
		cr.SetAPIVersion("rbac.authorization.k8s.io/v1")
		cr.SetKind("ClusterRole")
		cr.SetName(name)
		if delErr := c.Delete(ctx, cr); delErr != nil && !apierrors.IsNotFound(delErr) {
			klog.Warningf("Best-effort: failed to delete %s ClusterRole: %v", name, delErr)
		}
	}

	if len(errors) > 0 {
		klog.Errorf("Gitops agent addon uninstall completed with %d error(s)", len(errors))
		return stderrors.Join(errors...)
	}

	klog.Info("Successfully completed Gitops agent addon uninstall")
	return nil
}

// deleteGitOpsServiceCR deletes the GitOpsService CR if it exists (used in OLM mode).
// GitOpsService is cluster-scoped, so no namespace is set.
func deleteGitOpsServiceCR(ctx context.Context, c client.Client) error {
	klog.Info("Deleting GitOpsService CR (if exists)")

	gitOpsService := &unstructured.Unstructured{}
	gitOpsService.SetAPIVersion("pipelines.openshift.io/v1alpha1")
	gitOpsService.SetKind("GitopsService")
	gitOpsService.SetName("cluster")

	err := c.Delete(ctx, gitOpsService)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info("GitOpsService CR not found (expected if not using OLM mode)")
			return nil
		}
		return fmt.Errorf("failed to delete GitOpsService CR: %w", err)
	}

	klog.Info("GitOpsService CR deleted successfully")
	return nil
}

// olmCSVRef identifies a specific ClusterServiceVersion to delete
type olmCSVRef struct {
	name      string
	namespace string
}

// deleteOLMResources deletes OLM Subscription and CSV resources, discarding the
// "found" signal. Retained for backward compatibility with tests.
func deleteOLMResources(ctx context.Context, c client.Client) error {
	_, err := deleteOLMResourcesAndReport(ctx, c)
	return err
}

// deleteOLMResourcesAndReport deletes OLM resources and also reports whether any
// OLM resources (subscriptions or CSVs) were found and deleted. This signal tells the
// caller whether the operator was installed via OLM (so OLM handles operator teardown)
// or via the embedded chart (so manual resource deletion is needed).
func deleteOLMResourcesAndReport(ctx context.Context, c client.Client) (bool, error) {
	return deleteOLMResourcesInternal(ctx, c)
}

// deleteOLMResourcesInternal deletes OLM Subscription and CSV resources owned by gitopsaddon.
// CSVs are identified via the subscription's status.installedCSV field BEFORE deleting
// the subscription, so we only delete CSVs that we installed — never pre-existing ones.
// Returns true if any OLM subscriptions or CSVs were found (indicating OLM mode).
func deleteOLMResourcesInternal(ctx context.Context, c client.Client) (bool, error) {
	klog.Info("Deleting OLM resources (Subscription and CSV)")

	// AddonNamespace is included because some 2.16 installer builds embedded the
	// subscription in open-cluster-management-agent-addon via the static AddOnTemplate
	// (ManifestWork) before the namespace was moved to openshift-gitops-operator in 2.17.
	subscriptionNamespaces := []string{"openshift-operators", "operators", GitOpsOperatorNamespace, AddonNamespace}

	// Include the namespace from env var if set and not already in the list
	if envNs := os.Getenv("OLM_SUBSCRIPTION_NAMESPACE"); envNs != "" {
		found := false
		for _, ns := range subscriptionNamespaces {
			if ns == envNs {
				found = true
				break
			}
		}
		if !found {
			subscriptionNamespaces = append(subscriptionNamespaces, envNs)
		}
	}

	var errs []error
	olmFound := false

	// Step 1: Collect CSV names from our labeled subscriptions BEFORE deleting them.
	var csvsToDelete []olmCSVRef

	for _, ns := range subscriptionNamespaces {
		refs, err := getInstalledCSVsFromLabeledSubscriptions(ctx, c, ns)
		if err != nil {
			klog.Warningf("Error collecting CSVs from subscriptions in %s: %v", ns, err)
			errs = append(errs, fmt.Errorf("failed to collect CSVs in %s: %w", ns, err))
		}
		if len(refs) > 0 {
			olmFound = true
		}
		csvsToDelete = append(csvsToDelete, refs...)
	}

	// Step 2: Delete our labeled subscriptions
	for _, ns := range subscriptionNamespaces {
		deleted, err := deleteSubscriptionsInNamespaceAndReport(ctx, c, ns)
		if err != nil {
			klog.Warningf("Error deleting subscriptions in %s: %v", ns, err)
			errs = append(errs, fmt.Errorf("failed to delete subscriptions in %s: %w", ns, err))
		}
		if deleted {
			olmFound = true
		}
	}

	// Step 3: Delete only the CSVs that were installed by our subscriptions
	for _, ref := range csvsToDelete {
		csv := &unstructured.Unstructured{}
		csv.SetAPIVersion("operators.coreos.com/v1alpha1")
		csv.SetKind("ClusterServiceVersion")
		csv.SetName(ref.name)
		csv.SetNamespace(ref.namespace)
		klog.Infof("Deleting CSV installed by gitopsaddon subscription: %s/%s", ref.namespace, ref.name)
		if err := c.Delete(ctx, csv); err != nil && !apierrors.IsNotFound(err) && !isNoKindMatchError(err) {
			klog.Warningf("Failed to delete CSV %s/%s: %v", ref.namespace, ref.name, err)
			errs = append(errs, fmt.Errorf("failed to delete CSV %s/%s: %w", ref.namespace, ref.name, err))
		}
	}

	// Step 4: Clean up any remaining openshift-gitops-operator CSVs that were not
	// referenced by status.installedCSV (e.g. replacement CSVs from an OLM upgrade
	// cycle that are stuck in Pending after the installed CSV was deleted).
	subName := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAME", "openshift-gitops-operator")
	for _, ns := range subscriptionNamespaces {
		if err := deleteRemainingGitOpsCSVs(ctx, c, ns, subName); err != nil {
			klog.Warningf("Error cleaning remaining CSVs in %s: %v", ns, err)
			errs = append(errs, fmt.Errorf("failed to clean remaining CSVs in %s: %w", ns, err))
		}
	}

	// Step 5: Fix CRD conversion webhook. After deleting the operator, the ArgoCD CRD
	// still references the operator's webhook service for v1alpha1<->v1beta1 conversion.
	// When OLM reinstalls, its InstallPlan validates existing CRs via this webhook,
	// which fails ("service not found"). Patch to None so the next install can proceed.
	if err := patchArgoCDCRDConversionWebhook(ctx, c); err != nil {
		klog.Warningf("Error patching ArgoCD CRD conversion webhook: %v", err)
		errs = append(errs, fmt.Errorf("failed to patch ArgoCD CRD conversion webhook: %w", err))
	}

	if olmFound {
		klog.Info("OLM resources were found and deleted — operator cleanup will be handled by OLM")
	} else {
		klog.Info("No OLM resources found — operator was installed via embedded chart")
	}

	if len(errs) > 0 {
		return olmFound, fmt.Errorf("OLM resource cleanup had %d error(s): %w", len(errs), errs[0])
	}
	return olmFound, nil
}

// patchArgoCDCRDConversionWebhook patches the argocds.argoproj.io CRD to remove
// the conversion webhook. After the operator is deleted, the CRD still references
// the operator's webhook service. OLM's InstallPlan validation then fails because
// it can't reach the webhook to convert existing CRs to the new schema.
func patchArgoCDCRDConversionWebhook(ctx context.Context, c client.Client) error {
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")

	err := c.Get(ctx, types.NamespacedName{Name: "argocds.argoproj.io"}, crd)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return fmt.Errorf("failed to get ArgoCD CRD: %w", err)
	}

	conversion, found, _ := unstructured.NestedMap(crd.Object, "spec", "conversion")
	if !found {
		return nil
	}

	strategy, _, _ := unstructured.NestedString(conversion, "strategy")
	if strategy != "Webhook" {
		return nil
	}

	klog.Info("Patching ArgoCD CRD conversion webhook from Webhook to None")
	patch := client.RawPatch(types.JSONPatchType,
		[]byte(`[{"op": "replace", "path": "/spec/conversion", "value": {"strategy": "None"}}]`))
	if err := c.Patch(ctx, crd, patch); err != nil {
		return fmt.Errorf("failed to patch ArgoCD CRD conversion: %w", err)
	}
	klog.Info("Successfully patched ArgoCD CRD conversion to None")
	return nil
}

// patchArgoCDCRDConversionWebhookIfNotReady unconditionally patches the ArgoCD CRD
// conversion strategy to "None" when the CRD has a service-based webhook configured.
//
// This must be called BEFORE creating the OLM Subscription so that OLM's InstallPlan
// can validate existing ArgoCD CRs without hitting the webhook. Two failure modes are
// prevented:
//
//  1. "service not found" — the webhook service does not exist yet.
//
//  2. "connection refused" — the service and its endpoints appear ready to k8s (the
//     readiness probe passed), but the webhook server on port 9443 has not yet opened
//     the socket. Endpoint readiness is not a reliable proxy for port-level readiness,
//     so checking endpoints is insufficient to prevent this race.
//
// Patching to "None" unconditionally is safe: OLM restores the "Webhook" strategy when
// it processes the new InstallPlan, and the running operator re-establishes the full
// webhook config once its pod is serving.
//
// URL-based webhooks (clientConfig.url rather than clientConfig.service) are left
// untouched since they are not managed by OLM and are not our concern.
func patchArgoCDCRDConversionWebhookIfNotReady(ctx context.Context, c client.Client) error {
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")

	err := c.Get(ctx, types.NamespacedName{Name: "argocds.argoproj.io"}, crd)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return fmt.Errorf("failed to get ArgoCD CRD: %w", err)
	}

	conversion, found, _ := unstructured.NestedMap(crd.Object, "spec", "conversion")
	if !found {
		return nil
	}

	strategy, _, _ := unstructured.NestedString(conversion, "strategy")
	if strategy != "Webhook" {
		return nil
	}

	// Only patch when the webhook is service-based (OLM-managed). URL-based webhooks
	// (clientConfig.url) are not managed by OLM and should not be touched.
	svcName, _, _ := unstructured.NestedString(conversion, "webhook", "clientConfig", "service", "name")
	svcNamespace, _, _ := unstructured.NestedString(conversion, "webhook", "clientConfig", "service", "namespace")
	if svcName == "" || svcNamespace == "" {
		return nil
	}

	klog.Infof("ArgoCD CRD conversion webhook references service %s/%s; patching strategy to None before OLM subscription",
		svcNamespace, svcName)
	return patchCRDConversionToNone(ctx, c, crd, "pre-emptive patch before OLM subscription")
}

// patchCRDConversionToNone patches the given CRD's conversion strategy to "None".
func patchCRDConversionToNone(ctx context.Context, c client.Client, crd *unstructured.Unstructured, reason string) error {
	patch := client.RawPatch(types.JSONPatchType,
		[]byte(`[{"op": "replace", "path": "/spec/conversion", "value": {"strategy": "None"}}]`))
	if err := c.Patch(ctx, crd, patch); err != nil {
		return fmt.Errorf("failed to patch ArgoCD CRD conversion: %w", err)
	}
	klog.Infof("Successfully patched ArgoCD CRD conversion webhook to None (%s)", reason)
	return nil
}

// deleteRemainingGitOpsCSVs deletes any ClusterServiceVersion whose name starts
// with "openshift-gitops-operator." in the given namespace. This catches CSVs
// that were created by OLM's upgrade mechanism (status "Replacing") and are not
// deleteRemainingGitOpsCSVs sweeps namespace for CSVs whose name starts with
// "<subName>." (e.g. "openshift-gitops-operator.v1.20.0") that are not referenced
// by any surviving OLM Subscription. This covers the "Replacing" CSV left behind
// during an in-progress OLM upgrade where status.installedCSV pointed only to the
// newer version.
//
// OLM provenance labels (operators.coreos.com/<pkg>.<ns>) are intentionally NOT
// used as the ownership check: the identical label is also present on CSVs installed
// via OperatorHub in the same namespace, making it an unreliable discriminator.
// Checking for surviving subscription references is the reliable guard: a CSV still
// claimed by any subscription must not be deleted.
func deleteRemainingGitOpsCSVs(ctx context.Context, c client.Client, namespace, subName string) error {
	csvList := &unstructured.UnstructuredList{}
	csvList.SetAPIVersion("operators.coreos.com/v1alpha1")
	csvList.SetKind("ClusterServiceVersionList")

	if err := c.List(ctx, csvList, client.InNamespace(namespace)); err != nil {
		if apierrors.IsNotFound(err) || isNoKindMatchError(err) {
			return nil
		}
		return err
	}

	for i := range csvList.Items {
		csv := &csvList.Items[i]
		name := csv.GetName()
		if len(name) == 0 || !strings.HasPrefix(name, subName+".") {
			continue
		}
		// Only delete CSVs not claimed by any surviving subscription.
		// A CSV still referenced by a subscription (ours or the user's) must not be touched.
		referenced, err := isCsvReferencedByAnySubscription(ctx, c, namespace, name)
		if err != nil {
			klog.Warningf("Migration sweep: could not check subscriptions for CSV %s/%s, skipping: %v", namespace, name, err)
			continue
		}
		if referenced {
			klog.Infof("Migration sweep: CSV %s/%s is still referenced by a subscription, skipping", namespace, name)
			continue
		}
		klog.Infof("Deleting remaining gitops-operator CSV: %s/%s", namespace, name)
		if err := c.Delete(ctx, csv); err != nil && !apierrors.IsNotFound(err) {
			klog.Warningf("Failed to delete remaining CSV %s/%s: %v", namespace, name, err)
		}
	}
	return nil
}

// isCsvReferencedByAnySubscription returns true if any surviving OLM Subscription in
// csvNamespace lists csvName as its status.installedCSV or status.currentCSV.
// Scoping to the same namespace is correct by OLM invariant: a Subscription's
// status.installedCSV always points to a CSV in the same namespace, never across
// namespaces. This avoids a cluster-wide list and is safe to use as a deletion guard.
func isCsvReferencedByAnySubscription(ctx context.Context, c client.Client, csvNamespace, csvName string) (bool, error) {
	subList := &unstructured.UnstructuredList{}
	subList.SetAPIVersion("operators.coreos.com/v1alpha1")
	subList.SetKind("SubscriptionList")
	if err := c.List(ctx, subList, client.InNamespace(csvNamespace)); err != nil {
		if apierrors.IsNotFound(err) || isNoKindMatchError(err) {
			return false, nil
		}
		return false, err
	}
	for i := range subList.Items {
		sub := &subList.Items[i]
		if sub.GetDeletionTimestamp() != nil {
			continue
		}
		installedCSV, _, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
		currentCSV, _, _ := unstructured.NestedString(sub.Object, "status", "currentCSV")
		if installedCSV == csvName || currentCSV == csvName {
			return true, nil
		}
	}
	return false, nil
}

// getInstalledCSVsFromLabeledSubscriptions reads status.installedCSV from gitopsaddon-labeled
// subscriptions in the given namespace. Returns the CSV refs before the subscriptions are deleted.
func getInstalledCSVsFromLabeledSubscriptions(ctx context.Context, c client.Client, namespace string) ([]olmCSVRef, error) {
	subscriptions := &unstructured.UnstructuredList{}
	subscriptions.SetAPIVersion("operators.coreos.com/v1alpha1")
	subscriptions.SetKind("SubscriptionList")

	err := c.List(ctx, subscriptions,
		client.InNamespace(namespace),
		client.MatchingLabels{GitOpsAddonLabelKey: GitOpsAddonLabelValue})
	if err != nil {
		if apierrors.IsNotFound(err) || isNoKindMatchError(err) {
			return nil, nil
		}
		return nil, err
	}

	var refs []olmCSVRef
	for _, sub := range subscriptions.Items {
		csvName, _, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
		if csvName != "" {
			klog.Infof("Found installedCSV %s from subscription %s/%s", csvName, namespace, sub.GetName())
			refs = append(refs, olmCSVRef{name: csvName, namespace: namespace})
		}
	}

	return refs, nil
}

// deleteSubscriptionsInNamespaceAndReport deletes gitopsaddon-labeled subscriptions
// and returns true if any were found (regardless of whether deletion succeeded).
func deleteSubscriptionsInNamespaceAndReport(ctx context.Context, c client.Client, namespace string) (bool, error) {
	subscriptions := &unstructured.UnstructuredList{}
	subscriptions.SetAPIVersion("operators.coreos.com/v1alpha1")
	subscriptions.SetKind("SubscriptionList")

	err := c.List(ctx, subscriptions,
		client.InNamespace(namespace),
		client.MatchingLabels{GitOpsAddonLabelKey: GitOpsAddonLabelValue})
	if err != nil {
		if apierrors.IsNotFound(err) || isNoKindMatchError(err) {
			return false, nil
		}
		return false, err
	}

	found := len(subscriptions.Items) > 0

	for _, sub := range subscriptions.Items {
		klog.Infof("Deleting Subscription: %s/%s", namespace, sub.GetName())
		if err := c.Delete(ctx, &sub); err != nil && !apierrors.IsNotFound(err) {
			klog.Warningf("Failed to delete Subscription %s: %v", sub.GetName(), err)
		}
	}

	return found, nil
}

// deleteArgoCDCR deletes ArgoCD CRs with the gitopsaddon label across ALL namespaces
// and retries until fully removed. Only deletes CRs created by the gitops-addon.
func deleteArgoCDCR(ctx context.Context, c client.Client) error {
	klog.Info("Deleting ArgoCD CRs with gitopsaddon label (all namespaces)")

	checkInterval := 5 * time.Second
	// After seeing 0 CRs, require stability for this duration before returning.
	// This guards against the ConfigurationPolicy race: the config-policy-controller
	// may have a queued reconciliation that re-creates the ArgoCD CR after the
	// ConfigurationPolicy is deleted. Waiting ensures any stale enforcement has settled.
	// Keep this short (15s) to avoid approaching the Job's activeDeadlineSeconds (600s).
	stabilityDuration := 15 * time.Second
	deleted := false
	startTime := time.Now()
	var zeroSince time.Time

	labelSelector := client.MatchingLabels{
		GitOpsAddonLabelKey: GitOpsAddonLabelValue,
	}

	for {
		argoCDList := &unstructured.UnstructuredList{}
		argoCDList.SetAPIVersion("argoproj.io/v1beta1")
		argoCDList.SetKind("ArgoCDList")

		err := c.List(ctx, argoCDList, labelSelector)
		if err != nil {
			if isNoKindMatchError(err) {
				klog.Info("ArgoCD CRD not installed — nothing to clean up")
				return nil
			}
			klog.Warningf("Error listing ArgoCD CRs: %v", err)
			zeroSince = time.Time{}
		} else if len(argoCDList.Items) == 0 {
			if zeroSince.IsZero() {
				zeroSince = time.Now()
				if !deleted {
					klog.Info("No gitopsaddon-labeled ArgoCD CRs found, verifying stability...")
				} else {
					klog.Info("ArgoCD CRs deleted, verifying stability (checking for re-creation)...")
				}
			}
			if time.Since(zeroSince) >= stabilityDuration {
				klog.Infof("ArgoCD CR deletion confirmed stable for %v (total elapsed: %v)", stabilityDuration, time.Since(startTime))
				return nil
			}
		} else {
			if !zeroSince.IsZero() {
				klog.Warning("ArgoCD CR re-appeared after deletion — likely stale ConfigurationPolicy enforcement, re-deleting")
				zeroSince = time.Time{}
			}
			for _, argoCD := range argoCDList.Items {
				name := argoCD.GetName()
				ns := argoCD.GetNamespace()
				elapsed := time.Since(startTime).Truncate(time.Second)
				klog.Infof("Deleting ArgoCD CR: %s/%s (waiting for operator finalizer, elapsed: %v)", ns, name, elapsed)
				if err := c.Delete(ctx, &argoCD); err != nil && !apierrors.IsNotFound(err) {
					klog.Warningf("Failed to delete ArgoCD CR %s/%s: %v", ns, name, err)
				} else {
					deleted = true
				}
			}
		}

		time.Sleep(checkInterval)
	}
}

// deleteOperatorResources deletes openshift-gitops-operator resources
// Resources from gitopsaddon/charts/openshift-gitops-operator/templates/ (excluding CRDs)
func deleteOperatorResources(ctx context.Context, c client.Client, gitopsOperatorNS string) error {
	klog.Infof("Deleting openshift-gitops-operator resources from namespace: %s", gitopsOperatorNS)

	// Resource types from openshift-gitops-operator chart
	namespacedResourceTypes := []resourceType{
		{"Deployment", "apps/v1"},
		{"Service", "v1"},
		{"ConfigMap", "v1"},
		{"ServiceAccount", "v1"},
		{"Role", "rbac.authorization.k8s.io/v1"},
		{"RoleBinding", "rbac.authorization.k8s.io/v1"},
	}

	clusterResourceTypes := []resourceType{
		{"ClusterRole", "rbac.authorization.k8s.io/v1"},
		{"ClusterRoleBinding", "rbac.authorization.k8s.io/v1"},
	}

	var errs []error

	// All resources deployed by the embedded Helm chart are stamped with the
	// gitopsaddon label at apply time (in templateAndApplyChart). Use label-based
	// deletion for everything — this only removes resources we created, leaving
	// Kubernetes system-managed resources (kube-root-ca.crt, default SA, etc.) untouched.
	klog.Infof("Deleting gitopsaddon-labeled namespaced resources in operator namespace %s", gitopsOperatorNS)
	if err := deleteResourcesWithRetry(ctx, c, namespacedResourceTypes, nil, gitopsOperatorNS, 4*time.Minute, true); err != nil {
		klog.Warningf("Label-based deletion incomplete for %s: %v", gitopsOperatorNS, err)
		errs = append(errs, err)
	}

	klog.Info("Deleting gitopsaddon-labeled cluster-scoped resources (ClusterRole/ClusterRoleBinding)")
	if err := deleteResourcesWithRetry(ctx, c, nil, clusterResourceTypes, "", 2*time.Minute, true); err != nil {
		klog.Warningf("Label-based deletion incomplete for cluster-scoped resources: %v", err)
		errs = append(errs, err)
	}

	return stderrors.Join(errs...)
}

// deleteResourcesWithRetry deletes resources and retries until fully removed or timeout
func deleteResourcesWithRetry(ctx context.Context, c client.Client, namespacedTypes, clusterTypes []resourceType, namespace string, maxWait time.Duration, requireLabel bool) error {
	checkInterval := 5 * time.Second
	elapsed := time.Duration(0)

	for elapsed < maxWait {
		if ctx.Err() != nil {
			klog.Warningf("Context cancelled during resource deletion retry loop: %v", ctx.Err())
			break
		}

		allDeleted := true

		// Delete and check namespaced resources
		if !deleteResourcesByType(ctx, c, namespacedTypes, namespace, requireLabel) {
			allDeleted = false
		}

		// Delete and check cluster-scoped resources
		if !deleteResourcesByType(ctx, c, clusterTypes, "", requireLabel) {
			allDeleted = false
		}

		// If all resources are deleted, we're done
		if allDeleted {
			klog.Infof("All resources successfully deleted from namespace: %s", namespace)
			return nil
		}

		// Wait before next retry
		klog.V(1).Infof("Some resources still exist, waiting... (elapsed: %v)", elapsed)
		time.Sleep(checkInterval)
		elapsed += checkInterval
	}

	// Timeout reached - report what's left
	remainingResources := collectRemainingResources(ctx, c, namespacedTypes, clusterTypes, namespace, requireLabel)

	if len(remainingResources) > 0 {
		return fmt.Errorf("timeout: failed to delete resources after %v, remaining: %v", maxWait, remainingResources)
	}

	klog.Infof("Resources deletion completed for namespace: %s", namespace)
	return nil
}

// resourceType represents a Kubernetes resource type
type resourceType struct {
	kind       string
	apiVersion string
}

// deleteResourcesByType attempts to delete all resources of given types
// If requireLabel is true, only deletes resources with gitopsaddon label
// If requireLabel is false, deletes all resources of that type in the namespace (more aggressive cleanup)
// Returns true if no resources remain, false if some resources still exist
func deleteResourcesByType(ctx context.Context, c client.Client, resourceTypes []resourceType, namespace string, requireLabel bool) bool {
	allDeleted := true

	for _, rt := range resourceTypes {
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion(rt.apiVersion)
		list.SetKind(rt.kind + "List")

		listOpts := []client.ListOption{}
		if requireLabel {
			listOpts = append(listOpts, client.MatchingLabels{GitOpsAddonLabelKey: GitOpsAddonLabelValue})
		}
		if namespace != "" {
			listOpts = append(listOpts, client.InNamespace(namespace))
		}

		err := c.List(ctx, list, listOpts...)
		if err != nil {
			klog.Warningf("Failed to list %s: %v", rt.kind, err)
			allDeleted = false
			continue
		}

		if len(list.Items) > 0 {
			klog.Infof("Found %d %s resource(s) in namespace %s (requireLabel=%v)", len(list.Items), rt.kind, namespace, requireLabel)
			for _, item := range list.Items {
				// Skip resources that are already being deleted
				if item.GetDeletionTimestamp() != nil {
					klog.Infof("%s %s is already being deleted", rt.kind, item.GetName())
					allDeleted = false
					continue
				}

				// IMPORTANT: Skip the gitops-addon ClusterRole and ClusterRoleBinding during
				// operator resource cleanup. These are the addon's own RBAC — deleting them
				// would remove the cleanup Job's permissions. They are deleted as the very
				// last step in uninstallGitopsAgentInternal after all other resources are gone.
				if (rt.kind == "ClusterRoleBinding" || rt.kind == "ClusterRole") &&
					(item.GetName() == AddonDeploymentName || item.GetName() == AddonDeploymentName+"-cleanup") {
					klog.Infof("Skipping %s %s (self-referencing RBAC, will be deleted last)", rt.kind, item.GetName())
					continue
				}

				location := item.GetName()
				if namespace != "" {
					location = fmt.Sprintf("%s/%s", namespace, item.GetName())
				}
				klog.Infof("Deleting %s: %s", rt.kind, location)
				if err := c.Delete(ctx, &item); err != nil && !apierrors.IsNotFound(err) {
					klog.Warningf("Failed to delete %s %s: %v", rt.kind, location, err)
				}
				allDeleted = false
			}
		}
	}

	return allDeleted
}

// collectRemainingResources collects a list of resources that still exist
func collectRemainingResources(ctx context.Context, c client.Client, namespacedTypes, clusterTypes []resourceType, namespace string, requireLabel bool) []string {
	remaining := []string{}

	for _, rt := range namespacedTypes {
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion(rt.apiVersion)
		list.SetKind(rt.kind + "List")

		listOpts := []client.ListOption{client.InNamespace(namespace)}
		if requireLabel {
			listOpts = append(listOpts, client.MatchingLabels{GitOpsAddonLabelKey: GitOpsAddonLabelValue})
		}

		err := c.List(ctx, list, listOpts...)

		if err == nil && len(list.Items) > 0 {
			for _, item := range list.Items {
				remaining = append(remaining, fmt.Sprintf("%s/%s", rt.kind, item.GetName()))
			}
		}
	}

	for _, rt := range clusterTypes {
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion(rt.apiVersion)
		list.SetKind(rt.kind + "List")

		listOpts := []client.ListOption{}
		if requireLabel {
			listOpts = append(listOpts, client.MatchingLabels{GitOpsAddonLabelKey: GitOpsAddonLabelValue})
		}

		err := c.List(ctx, list, listOpts...)

		if err == nil && len(list.Items) > 0 {
			for _, item := range list.Items {
				remaining = append(remaining, fmt.Sprintf("%s/%s", rt.kind, item.GetName()))
			}
		}
	}

	return remaining
}

// createPauseMarker creates a ConfigMap marker to pause the gitops-addon controller
// The pause marker is owned by the gitops-addon Deployment, so it will be automatically
// garbage collected when the Deployment is deleted
func createPauseMarker(ctx context.Context, c client.Client) error {
	klog.Infof("Creating pause marker ConfigMap '%s' in namespace: %s", PauseMarkerName, AddonNamespace)

	// Get the gitops-addon Deployment to set as owner
	deployment := &appsv1.Deployment{}
	deploymentKey := types.NamespacedName{
		Name:      AddonDeploymentName,
		Namespace: AddonNamespace,
	}
	if err := c.Get(ctx, deploymentKey, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			klog.Warningf("Deployment %s not found in namespace %s, creating pause marker without owner reference", AddonDeploymentName, AddonNamespace)
			// Continue without owner reference if deployment doesn't exist
		} else {
			return fmt.Errorf("failed to get deployment for owner reference: %w", err)
		}
	}

	pauseMarker := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PauseMarkerName,
			Namespace: AddonNamespace,
			Labels: map[string]string{
				"app": "gitops-addon",
			},
		},
		Data: map[string]string{
			"paused":    "true",
			"reason":    "cleanup",
			"timestamp": time.Now().Format(time.RFC3339),
		},
	}

	// Set owner reference if deployment was found
	if deployment.UID != "" {
		blockOwnerDeletion := true
		controller := true
		pauseMarker.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion:         "apps/v1",
				Kind:               "Deployment",
				Name:               deployment.Name,
				UID:                deployment.UID,
				BlockOwnerDeletion: &blockOwnerDeletion,
				Controller:         &controller,
			},
		}
		klog.Infof("Pause marker will be owned by Deployment %s/%s", AddonNamespace, AddonDeploymentName)
	}

	err := c.Create(ctx, pauseMarker)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create pause marker: %w", err)
	}

	klog.Infof("Pause marker created successfully in namespace: %s", AddonNamespace)
	return nil
}

// deletePauseMarker directly deletes the pause marker ConfigMap without a read-before-delete.
// Used during cleanup where we know the marker was just created, avoiding the cached-read issue.
func deletePauseMarker(ctx context.Context, c client.Client) error {
	pauseMarker := &corev1.ConfigMap{}
	pauseMarker.Name = PauseMarkerName
	pauseMarker.Namespace = AddonNamespace

	if err := c.Delete(ctx, pauseMarker); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	klog.Info("Successfully deleted pause marker")
	return nil
}

// deleteOrphanedPauseMarker deletes the pause marker only if it has no ownerReferences.
// When createPauseMarker can't find the addon Deployment, the marker is created without
// an ownerReference and won't be garbage collected. This function handles that case while
// leaving owner-referenced markers for safe GC via the Deployment lifecycle.
func deleteOrphanedPauseMarker(ctx context.Context, c client.Client) error {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: PauseMarkerName, Namespace: AddonNamespace}
	if err := c.Get(ctx, key, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get pause marker: %w", err)
	}

	if len(cm.OwnerReferences) > 0 {
		klog.Info("Pause marker has ownerReferences — leaving for garbage collection")
		return nil
	}

	klog.Info("Pause marker has no ownerReferences — deleting explicitly to prevent stale pause")
	if err := c.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete orphaned pause marker: %w", err)
	}
	klog.Info("Successfully deleted orphaned pause marker")
	return nil
}

// ClearStalePauseMarker deletes the pause marker ConfigMap if it exists.
// Called at controller startup to clear markers left over from a previous cleanup cycle.
// On OCP (OLM mode), the pause marker can't use owner references because the Deployment
// and the marker are in different namespaces, so the marker is never garbage collected.
//
// The reader parameter is used for the Get operation - at controller startup this should
// be an uncached API reader (mgr.GetAPIReader()) because the controller-runtime cache
// may not have synced ConfigMaps yet when Start() is called.
// The client parameter is used for the Delete operation.
func ClearStalePauseMarker(ctx context.Context, c client.Client, readers ...client.Reader) {
	// Use provided reader for Get, or fall back to the client
	var reader client.Reader = c
	if len(readers) > 0 && readers[0] != nil {
		reader = readers[0]
	}

	pauseMarker := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Name:      PauseMarkerName,
		Namespace: AddonNamespace,
	}

	err := reader.Get(ctx, key, pauseMarker)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Info("No stale pause marker found at startup")
			return // No marker, nothing to do
		}
		klog.Warningf("Error checking for stale pause marker: %v", err)
		return
	}

	klog.Infof("Found stale pause marker from previous cleanup (timestamp: %s), deleting it",
		pauseMarker.Data["timestamp"])
	if err := c.Delete(ctx, pauseMarker); err != nil && !apierrors.IsNotFound(err) {
		klog.Warningf("Failed to delete stale pause marker: %v", err)
	} else {
		klog.Info("Successfully deleted stale pause marker")
	}
}

// scaleDownAddonAgent scales the addon agent Deployment to 0 replicas so the
// agent's reconciler cannot recreate operator resources during cleanup. The
// regular ManifestWork deletion also removes the Deployment, but there's a race
// window where the agent pod is still running. Scaling to 0 triggers immediate
// pod termination without waiting for the ManifestWork controller.
func scaleDownAddonAgent(ctx context.Context, c client.Client) {
	deploy := &appsv1.Deployment{}
	key := types.NamespacedName{Name: AddonDeploymentName, Namespace: AddonNamespace}

	if err := c.Get(ctx, key, deploy); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("Failed to get addon agent Deployment: %v", err)
		}
		return
	}

	if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas == 0 {
		klog.Info("Addon agent Deployment already scaled to 0")
		return
	}

	zero := int32(0)
	deploy.Spec.Replicas = &zero
	if err := c.Update(ctx, deploy); err != nil {
		klog.Warningf("Failed to scale down addon agent Deployment: %v", err)
	} else {
		klog.Info("Addon agent Deployment scaled to 0 — agent pod terminating")
	}
}

// IsPaused checks if the gitops-addon controller should be paused
// This is exported so the controller can use it
// The pause marker is checked in the addon namespace
func IsPaused(ctx context.Context, c client.Client) bool {
	pauseMarker := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Name:      PauseMarkerName,
		Namespace: AddonNamespace,
	}

	err := c.Get(ctx, key, pauseMarker)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		// Fail closed: treat API errors as paused to prevent race conditions
		// where a transient error could unpause the agent during cleanup.
		klog.Warningf("Error checking pause marker (treating as paused): %v", err)
		return true
	}

	// Check if the pause marker indicates paused state
	if pauseMarker.Data["paused"] == "true" {
		klog.Infof("GitOps addon is paused (reason: %s, timestamp: %s)",
			pauseMarker.Data["reason"], pauseMarker.Data["timestamp"])
		return true
	}

	return false
}

// waitAndVerifyCleanup is a best-effort re-sweep that periodically checks whether
// the addon agent (which may still be running) has recreated operator resources
// after the initial deletion, and re-deletes them. It never returns an error —
// the cleanup Job must always exit 0 so OCM can proceed with ManifestWork deletion
// (which authoritatively removes the addon agent itself).
func waitAndVerifyCleanup(ctx context.Context, c client.Client, gitopsOperatorNS string, waitDuration time.Duration) {
	klog.Infof("Starting best-effort cleanup re-sweep for %v", waitDuration)

	checkInterval := 5 * time.Second
	elapsed := time.Duration(0)
	checkCount := 0

	for elapsed < waitDuration {
		time.Sleep(checkInterval)
		elapsed += checkInterval
		checkCount++

		klog.Infof("Cleanup re-sweep check %d (elapsed: %v/%v)", checkCount, elapsed, waitDuration)

		operatorResourcesExist, err := verifyNamespaceCleanup(ctx, c, gitopsOperatorNS)
		if err != nil {
			klog.Warningf("Error checking operator namespace: %v", err)
			continue
		}

		if operatorResourcesExist {
			klog.Warning("Resources recreated by in-flight reconciliation, re-deleting...")
			if err := deleteOperatorResources(ctx, c, gitopsOperatorNS); err != nil {
				klog.Warningf("Best-effort re-delete of operator resources: %v", err)
			}
		} else {
			klog.Info("Operator namespace is clean, no recreated resources found")
		}
	}

	klog.Infof("Cleanup re-sweep completed after %v", waitDuration)
}

// verifyNamespaceCleanup checks if there are any resources with the gitops-addon label in the namespace
// Returns true if resources exist, false if clean
func verifyNamespaceCleanup(ctx context.Context, c client.Client, namespace string) (bool, error) {
	// Check for deployments with the label
	deployments := &unstructured.UnstructuredList{}
	deployments.SetAPIVersion("apps/v1")
	deployments.SetKind("DeploymentList")

	err := c.List(ctx, deployments,
		client.InNamespace(namespace),
		client.MatchingLabels{GitOpsAddonLabelKey: GitOpsAddonLabelValue})

	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}

	if len(deployments.Items) > 0 {
		klog.Infof("Found %d deployment(s) in namespace %s", len(deployments.Items), namespace)
		return true, nil
	}

	return false, nil
}

// getCleanupVerificationWaitDuration returns how long the best-effort re-sweep runs
// after the initial deletion pass. Defaults to 20 seconds. During this window the
// cleanup Job periodically checks whether the addon agent (still alive, mid-reconciliation)
// has recreated operator resources, and re-deletes them. The re-sweep is best-effort and
// never fails the Job — OCM's ManifestWork deletion authoritatively removes the addon agent
// after the Job exits. Override via CLEANUP_VERIFICATION_WAIT_SECONDS env var; set to 0
// to skip the re-sweep entirely.
func getCleanupVerificationWaitDuration() time.Duration {
	if waitSecondsStr := os.Getenv("CLEANUP_VERIFICATION_WAIT_SECONDS"); waitSecondsStr != "" {
		if waitSeconds, err := strconv.Atoi(waitSecondsStr); err == nil {
			return time.Duration(waitSeconds) * time.Second
		}
	}
	return 20 * time.Second
}

// getPauseSettleDuration returns how long to wait after creating the pause marker
// before starting deletions. Override via PAUSE_SETTLE_SECONDS env var.
func getPauseSettleDuration() time.Duration {
	if s := os.Getenv("PAUSE_SETTLE_SECONDS"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return time.Duration(v) * time.Second
		}
	}
	return 10 * time.Second
}
