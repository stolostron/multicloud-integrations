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

	klog.Info("Final Step: Deleting gitops-addon ClusterRoleBinding")
	addonCRB := &unstructured.Unstructured{}
	addonCRB.SetAPIVersion("rbac.authorization.k8s.io/v1")
	addonCRB.SetKind("ClusterRoleBinding")
	addonCRB.SetName(AddonDeploymentName)
	if delErr := c.Delete(ctx, addonCRB); delErr != nil && !apierrors.IsNotFound(delErr) {
		klog.Warningf("Failed to delete gitops-addon ClusterRoleBinding: %v", delErr)
		errors = append(errors, fmt.Errorf("failed to delete gitops-addon ClusterRoleBinding: %w", delErr))
	}

	if len(errors) > 0 {
		return stderrors.Join(errors...)
	}
	klog.Info("Successfully completed hub cluster cleanup")
	return nil
}

// uninstallOnManagedCluster performs a full cleanup on remote managed clusters.
func uninstallOnManagedCluster(ctx context.Context, c client.Client, gitopsOperatorNS string) error {
	klog.Info("Managed cluster cleanup: full uninstall")
	var errors []error

	klog.Infof("Step 0: Creating pause marker in namespace: %s", AddonNamespace)
	if err := createPauseMarker(ctx, c); err != nil {
		klog.Errorf("Error creating pause marker (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to create pause marker: %w", err))
	}

	// Wait for the addon agent's informer cache to pick up the pause marker.
	// The addon agent runs in a separate process; its informer receives watch events
	// within 1-3 seconds. Without this delay, the agent may be mid-reconciliation
	// (not yet paused) and recreate the operator deployment after we delete it.
	// The operator deployment is NOT in the ManifestWork, so ManifestWork deletion
	// can't remove it — this Job is the only mechanism.
	pauseSettleDuration := getPauseSettleDuration()
	klog.Infof("Waiting %v for addon agent to observe pause marker...", pauseSettleDuration)
	time.Sleep(pauseSettleDuration)

	klog.Info("Step 1: Deleting GitOpsService CR (if exists)")
	if err := deleteGitOpsServiceCR(ctx, c); err != nil {
		klog.Errorf("Error deleting GitOpsService CR (continuing with cleanup): %v", err)
	}

	klog.Info("Step 2: Deleting ArgoCD CRs with gitopsaddon label (all namespaces)")
	if err := deleteArgoCDCR(ctx, c); err != nil {
		klog.Errorf("Error deleting ArgoCD CR (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete ArgoCD CR: %w", err))
	}

	klog.Info("Step 3: Deleting OLM Subscription and CSV (if exists)")
	if err := deleteOLMResources(ctx, c); err != nil {
		klog.Errorf("Error deleting OLM resources (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete OLM resources: %w", err))
	}

	klog.Info("Step 4: Deleting openshift-gitops-operator resources from namespace: " + gitopsOperatorNS)
	if err := deleteOperatorResources(ctx, c, gitopsOperatorNS); err != nil {
		klog.Errorf("Error deleting openshift-gitops-operator resources (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete openshift-gitops-operator resources: %w", err))
	}

	waitDuration := getCleanupVerificationWaitDuration()
	if waitDuration > 0 {
		klog.Infof("Step 5: Best-effort re-sweep for resources recreated by in-flight reconciliation (%v)...", waitDuration)
		waitAndVerifyCleanup(ctx, c, gitopsOperatorNS, waitDuration)
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

	klog.Info("Final Step: Deleting gitops-addon ClusterRoleBinding (self-referencing RBAC)")
	addonCRB := &unstructured.Unstructured{}
	addonCRB.SetAPIVersion("rbac.authorization.k8s.io/v1")
	addonCRB.SetKind("ClusterRoleBinding")
	addonCRB.SetName(AddonDeploymentName)
	if delErr := c.Delete(ctx, addonCRB); delErr != nil && !apierrors.IsNotFound(delErr) {
		klog.Warningf("Failed to delete gitops-addon ClusterRoleBinding: %v", delErr)
		errors = append(errors, fmt.Errorf("failed to delete gitops-addon ClusterRoleBinding: %w", delErr))
	}

	if len(errors) > 0 {
		klog.Errorf("Gitops agent addon uninstall completed with %d error(s)", len(errors))
		return stderrors.Join(errors...)
	}

	klog.Info("Successfully completed Gitops agent addon uninstall")
	return nil
}

// deleteGitOpsServiceCR deletes the GitOpsService CR if it exists (used in OLM mode)
func deleteGitOpsServiceCR(ctx context.Context, c client.Client) error {
	klog.Info("Deleting GitOpsService CR (if exists)")

	// GitOpsService is typically in the openshift-gitops namespace
	gitOpsService := &unstructured.Unstructured{}
	gitOpsService.SetAPIVersion("pipelines.openshift.io/v1alpha1")
	gitOpsService.SetKind("GitopsService")
	gitOpsService.SetName("cluster")
	gitOpsService.SetNamespace(GitOpsNamespace)

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

// deleteOLMResources deletes OLM Subscription and CSV resources owned by gitopsaddon.
// CSVs are identified via the subscription's status.installedCSV field BEFORE deleting
// the subscription, so we only delete CSVs that we installed — never pre-existing ones.
func deleteOLMResources(ctx context.Context, c client.Client) error {
	klog.Info("Deleting OLM resources (Subscription and CSV)")

	subscriptionNamespaces := []string{"openshift-operators", "operators"}

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

	// Step 1: Collect CSV names from our labeled subscriptions BEFORE deleting them.
	var csvsToDelete []olmCSVRef

	for _, ns := range subscriptionNamespaces {
		refs, err := getInstalledCSVsFromLabeledSubscriptions(ctx, c, ns)
		if err != nil {
			klog.Warningf("Error collecting CSVs from subscriptions in %s: %v", ns, err)
			errs = append(errs, fmt.Errorf("failed to collect CSVs in %s: %w", ns, err))
		}
		csvsToDelete = append(csvsToDelete, refs...)
	}

	// Step 2: Delete our labeled subscriptions
	for _, ns := range subscriptionNamespaces {
		if err := deleteSubscriptionsInNamespace(ctx, c, ns); err != nil {
			klog.Warningf("Error deleting subscriptions in %s: %v", ns, err)
			errs = append(errs, fmt.Errorf("failed to delete subscriptions in %s: %w", ns, err))
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

	if len(errs) > 0 {
		return fmt.Errorf("OLM resource cleanup had %d error(s): %w", len(errs), errs[0])
	}
	return nil
}

// getInstalledCSVsFromLabeledSubscriptions reads status.installedCSV from gitopsaddon-labeled
// subscriptions in the given namespace. Returns the CSV refs before the subscriptions are deleted.
func getInstalledCSVsFromLabeledSubscriptions(ctx context.Context, c client.Client, namespace string) ([]olmCSVRef, error) {
	subscriptions := &unstructured.UnstructuredList{}
	subscriptions.SetAPIVersion("operators.coreos.com/v1alpha1")
	subscriptions.SetKind("SubscriptionList")

	err := c.List(ctx, subscriptions,
		client.InNamespace(namespace),
		client.MatchingLabels{"apps.open-cluster-management.io/gitopsaddon": "true"})
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

// deleteSubscriptionsInNamespace deletes OLM Subscriptions with gitopsaddon label
func deleteSubscriptionsInNamespace(ctx context.Context, c client.Client, namespace string) error {
	subscriptions := &unstructured.UnstructuredList{}
	subscriptions.SetAPIVersion("operators.coreos.com/v1alpha1")
	subscriptions.SetKind("SubscriptionList")

	err := c.List(ctx, subscriptions,
		client.InNamespace(namespace),
		client.MatchingLabels{"apps.open-cluster-management.io/gitopsaddon": "true"})
	if err != nil {
		if apierrors.IsNotFound(err) || isNoKindMatchError(err) {
			return nil
		}
		return err
	}

	for _, sub := range subscriptions.Items {
		klog.Infof("Deleting Subscription: %s/%s", namespace, sub.GetName())
		if err := c.Delete(ctx, &sub); err != nil && !apierrors.IsNotFound(err) {
			klog.Warningf("Failed to delete Subscription %s: %v", sub.GetName(), err)
		}
	}

	return nil
}

// deleteArgoCDCR deletes ArgoCD CRs with the gitopsaddon label across ALL namespaces
// and retries until fully removed. Only deletes CRs created by the gitops-addon.
func deleteArgoCDCR(ctx context.Context, c client.Client) error {
	klog.Info("Deleting ArgoCD CRs with gitopsaddon label (all namespaces)")

	maxWait := 2 * time.Minute
	checkInterval := 5 * time.Second
	elapsed := time.Duration(0)
	deleted := false

	labelSelector := client.MatchingLabels{
		"apps.open-cluster-management.io/gitopsaddon": "true",
	}

	for elapsed < maxWait {
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
		} else if len(argoCDList.Items) == 0 {
			if deleted {
				klog.Info("All gitopsaddon-labeled ArgoCD CRs have been fully deleted")
			} else {
				klog.Info("No gitopsaddon-labeled ArgoCD CRs found (already deleted or never existed)")
			}
			return nil
		} else {
			for _, argoCD := range argoCDList.Items {
				name := argoCD.GetName()
				ns := argoCD.GetNamespace()
				klog.Infof("Deleting ArgoCD CR: %s/%s (elapsed: %v)", ns, name, elapsed)
				if err := c.Delete(ctx, &argoCD); err != nil && !apierrors.IsNotFound(err) {
					klog.Warningf("Failed to delete ArgoCD CR %s/%s: %v", ns, name, err)
				} else {
					deleted = true
				}
			}
		}

		time.Sleep(checkInterval)
		elapsed += checkInterval
	}

	argoCDList := &unstructured.UnstructuredList{}
	argoCDList.SetAPIVersion("argoproj.io/v1beta1")
	argoCDList.SetKind("ArgoCDList")
	err := c.List(ctx, argoCDList, labelSelector)
	if err != nil && isNoKindMatchError(err) {
		klog.Info("ArgoCD CRD not installed — nothing to clean up")
		return nil
	}
	if err == nil && len(argoCDList.Items) == 0 {
		klog.Warning("ArgoCD CRs deleted after timeout")
		return nil
	}

	return fmt.Errorf("timeout: gitopsaddon-labeled ArgoCD CRs still exist after %v", maxWait)
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

	// Try with label first, then fallback to namespace-based deletion
	err := deleteResourcesWithRetry(ctx, c, namespacedResourceTypes, clusterResourceTypes, gitopsOperatorNS, 4*time.Minute, true)
	if err != nil {
		klog.Warningf("Label-based deletion failed or incomplete, trying namespace-based deletion: %v", err)
		// Fallback: delete all resources in namespace (without label filter)
		err = deleteResourcesWithRetry(ctx, c, namespacedResourceTypes, clusterResourceTypes, gitopsOperatorNS, 2*time.Minute, false)
	}
	return err
}

// deleteResourcesWithRetry deletes resources and retries until fully removed or timeout
func deleteResourcesWithRetry(ctx context.Context, c client.Client, namespacedTypes, clusterTypes []resourceType, namespace string, maxWait time.Duration, requireLabel bool) error {
	checkInterval := 5 * time.Second
	elapsed := time.Duration(0)

	for elapsed < maxWait {
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
			listOpts = append(listOpts, client.MatchingLabels{"apps.open-cluster-management.io/gitopsaddon": "true"})
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

				// IMPORTANT: Skip the gitops-addon ClusterRoleBinding during operator resource cleanup.
				// This is the service account's own RBAC - deleting it would remove the cleanup Job's
				// permissions and prevent it from cleaning up remaining resources.
				// The gitops-addon ClusterRoleBinding is deleted as the very last step in
				// uninstallGitopsAgentInternal after all other resources are cleaned up.
				if rt.kind == "ClusterRoleBinding" && item.GetName() == AddonDeploymentName {
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
			listOpts = append(listOpts, client.MatchingLabels{"apps.open-cluster-management.io/gitopsaddon": "true"})
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
			listOpts = append(listOpts, client.MatchingLabels{"apps.open-cluster-management.io/gitopsaddon": "true"})
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
		klog.Warningf("Error checking pause marker: %v", err)
		return false
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
		client.MatchingLabels{"apps.open-cluster-management.io/gitopsaddon": "true"})

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
