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
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// uninstallGitopsAgentInternal performs the actual uninstall logic
// Step 0: Create pause marker to stop the gitops-addon controller from reconciling
// Step 1: Delete GitOpsService CR (for OLM mode, if exists)
// Step 2: Delete ArgoCD CR and wait for it to be fully removed (operator handles agent cleanup)
// Step 3: Delete OLM Subscription and CSV (for OLM mode)
// Step 4: Delete openshift-gitops-operator resources from openshift-gitops-operator namespace
// Step 5: Wait and re-verify that resources stay deleted (if configured)
// All steps attempt to continue even if one fails to ensure maximum cleanup
// Note: argocd-agent resources are handled by the argocd-operator when the ArgoCD CR is deleted
func uninstallGitopsAgentInternal(ctx context.Context, c client.Client, gitopsOperatorNS string) error {
	klog.Infof("Starting Gitops agent addon uninstall - gitopsOperatorNS: %s, gitopsNS: %s", gitopsOperatorNS, GitOpsNamespace)
	var errors []error

	// Step 0: Create pause marker to prevent the gitops-addon controller from reconciling resources
	klog.Infof("Step 0: Creating pause marker in namespace: %s", AddonNamespace)
	if err := createPauseMarker(ctx, c); err != nil {
		klog.Errorf("Error creating pause marker (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to create pause marker: %w", err))
	}

	// Step 1: Delete GitOpsService CR (for OLM mode, if exists)
	klog.Info("Step 1: Deleting GitOpsService CR (if exists)")
	if err := deleteGitOpsServiceCR(ctx, c); err != nil {
		klog.Errorf("Error deleting GitOpsService CR (continuing with cleanup): %v", err)
		// Don't add to errors - GitOpsService might not exist if not in OLM mode
	}

	// Step 2: Delete ArgoCD CR and wait for it to be fully removed
	// The argocd-operator will handle cleaning up the agent resources when the ArgoCD CR is deleted
	klog.Info("Step 2: Deleting ArgoCD CR from namespace: " + GitOpsNamespace)
	if err := deleteArgoCDCR(ctx, c); err != nil {
		klog.Errorf("Error deleting ArgoCD CR (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete ArgoCD CR: %w", err))
	}

	// Step 3: Delete OLM Subscription and CSV (for OLM mode)
	klog.Info("Step 3: Deleting OLM Subscription and CSV (if exists)")
	if err := deleteOLMResources(ctx, c); err != nil {
		klog.Errorf("Error deleting OLM resources (continuing with cleanup): %v", err)
		// Don't add to errors - OLM resources might not exist if not in OLM mode
	}

	// Step 4: Delete openshift-gitops-operator resources from openshift-gitops-operator namespace
	klog.Info("Step 4: Deleting openshift-gitops-operator resources from namespace: " + gitopsOperatorNS)
	if err := deleteOperatorResources(ctx, c, gitopsOperatorNS); err != nil {
		klog.Errorf("Error deleting openshift-gitops-operator resources (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete openshift-gitops-operator resources: %w", err))
	}

	// Step 5: Wait and re-verify that resources stay deleted
	// This is critical to handle timing issues where the controller might try to reconcile
	// Can be disabled for tests by setting CLEANUP_VERIFICATION_WAIT_SECONDS=0
	waitDuration := getCleanupVerificationWaitDuration()
	if waitDuration > 0 {
		klog.Infof("Step 5: Waiting and re-verifying that resources stay deleted (%v)...", waitDuration)
		if err := waitAndVerifyCleanup(ctx, c, gitopsOperatorNS, waitDuration); err != nil {
			klog.Errorf("Error during cleanup verification (resources may have been recreated): %v", err)
			errors = append(errors, fmt.Errorf("cleanup verification failed: %w", err))
		}
	} else {
		klog.Info("Step 5: Skipping cleanup verification (wait duration is 0)")
	}

	// Step 6: Delete the pause marker now that cleanup is done.
	// This prevents a stale marker from blocking the addon if it is re-deployed.
	klog.Info("Step 6: Deleting pause marker after successful cleanup")
	ClearStalePauseMarker(ctx, c)

	// Final Step: Delete the gitops-addon ClusterRoleBinding (self-referencing RBAC)
	// This MUST be the very last action because it removes the cleanup Job's permissions.
	// It was intentionally skipped during Step 4 to preserve permissions for resource cleanup.
	klog.Info("Final Step: Deleting gitops-addon ClusterRoleBinding (self-referencing RBAC)")
	addonCRB := &unstructured.Unstructured{}
	addonCRB.SetAPIVersion("rbac.authorization.k8s.io/v1")
	addonCRB.SetKind("ClusterRoleBinding")
	addonCRB.SetName(AddonDeploymentName)
	if delErr := c.Delete(ctx, addonCRB); delErr != nil {
		if !apierrors.IsNotFound(delErr) {
			klog.Warningf("Failed to delete gitops-addon ClusterRoleBinding: %v", delErr)
		}
	} else {
		klog.Info("Successfully deleted gitops-addon ClusterRoleBinding")
	}

	if len(errors) > 0 {
		klog.Errorf("Gitops agent addon uninstall completed with %d error(s)", len(errors))
		// Return the first error but log all of them
		return errors[0]
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

// deleteOLMResources deletes OLM Subscription and CSV resources with gitopsaddon label
func deleteOLMResources(ctx context.Context, c client.Client) error {
	klog.Info("Deleting OLM resources (Subscription and CSV)")

	// Delete Subscriptions with gitopsaddon label in openshift-operators namespace
	subscriptionNamespaces := []string{"openshift-operators", "operators"}
	for _, ns := range subscriptionNamespaces {
		if err := deleteSubscriptionsInNamespace(ctx, c, ns); err != nil {
			klog.Warningf("Error deleting subscriptions in %s: %v", ns, err)
		}
	}

	// Delete associated CSVs
	for _, ns := range subscriptionNamespaces {
		if err := deleteCSVsInNamespace(ctx, c, ns); err != nil {
			klog.Warningf("Error deleting CSVs in %s: %v", ns, err)
		}
	}

	return nil
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
		if apierrors.IsNotFound(err) {
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

// deleteCSVsInNamespace deletes ClusterServiceVersions related to argocd-operator
func deleteCSVsInNamespace(ctx context.Context, c client.Client, namespace string) error {
	csvs := &unstructured.UnstructuredList{}
	csvs.SetAPIVersion("operators.coreos.com/v1alpha1")
	csvs.SetKind("ClusterServiceVersionList")

	err := c.List(ctx, csvs, client.InNamespace(namespace))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	for _, csv := range csvs.Items {
		name := csv.GetName()
		// Delete argocd-operator or openshift-gitops-operator CSVs
		if containsArgoCD(name) {
			klog.Infof("Deleting CSV: %s/%s", namespace, name)
			if err := c.Delete(ctx, &csv); err != nil && !apierrors.IsNotFound(err) {
				klog.Warningf("Failed to delete CSV %s: %v", name, err)
			}
		}
	}

	return nil
}

// containsArgoCD checks if a string contains argocd-related identifiers
func containsArgoCD(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "argocd") || strings.Contains(lower, "openshift-gitops")
}

// deleteArgoCDCR deletes ArgoCD CRs with the gitopsaddon label and retries until fully removed
// Only deletes ArgoCD CRs created by the gitops-addon (with apps.open-cluster-management.io/gitopsaddon label)
func deleteArgoCDCR(ctx context.Context, c client.Client) error {
	klog.Info("Deleting ArgoCD CRs with gitopsaddon label in namespace: " + GitOpsNamespace)

	// Retry loop: 2 minutes total (24 iterations * 5 seconds)
	maxWait := 2 * time.Minute
	checkInterval := 5 * time.Second
	elapsed := time.Duration(0)
	deleted := false

	// Label selector for gitopsaddon-created resources
	labelSelector := client.MatchingLabels{
		"apps.open-cluster-management.io/gitopsaddon": "true",
	}

	for elapsed < maxWait {
		// List ArgoCD CRs with gitopsaddon label in the namespace
		argoCDList := &unstructured.UnstructuredList{}
		argoCDList.SetAPIVersion("argoproj.io/v1beta1")
		argoCDList.SetKind("ArgoCDList")

		err := c.List(ctx, argoCDList, client.InNamespace(GitOpsNamespace), labelSelector)
		if err != nil {
			klog.Warningf("Error listing ArgoCD CRs: %v", err)
			// Continue trying despite errors
		} else if len(argoCDList.Items) == 0 {
			// No ArgoCD CRs found with the label
			if deleted {
				klog.Info("All gitopsaddon-labeled ArgoCD CRs have been fully deleted")
			} else {
				klog.Info("No gitopsaddon-labeled ArgoCD CRs found (already deleted or never existed)")
			}
			return nil
		} else {
			// Delete each ArgoCD CR found with the label
			for _, argoCD := range argoCDList.Items {
				name := argoCD.GetName()
				klog.Infof("Deleting ArgoCD CR: %s/%s (elapsed: %v)", GitOpsNamespace, name, elapsed)
				if err := c.Delete(ctx, &argoCD); err != nil && !apierrors.IsNotFound(err) {
					klog.Warningf("Failed to delete ArgoCD CR %s: %v", name, err)
				} else {
					deleted = true
				}
			}
		}

		// Wait before next retry
		time.Sleep(checkInterval)
		elapsed += checkInterval
	}

	// Timeout reached - check final status
	argoCDList := &unstructured.UnstructuredList{}
	argoCDList.SetAPIVersion("argoproj.io/v1beta1")
	argoCDList.SetKind("ArgoCDList")
	err := c.List(ctx, argoCDList, client.InNamespace(GitOpsNamespace), labelSelector)
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

// waitAndVerifyCleanup waits for a specified duration and periodically verifies that
// resources stay deleted. If resources are recreated, it deletes them again.
// This handles timing issues where the controller might be mid-reconciliation when cleanup starts.
// Note: argocd-agent resources are handled by the argocd-operator when the ArgoCD CR is deleted.
func waitAndVerifyCleanup(ctx context.Context, c client.Client, gitopsOperatorNS string, waitDuration time.Duration) error {
	klog.Infof("Starting cleanup verification for %v", waitDuration)

	checkInterval := 20 * time.Second
	elapsed := time.Duration(0)
	checkCount := 0

	for elapsed < waitDuration {
		time.Sleep(checkInterval)
		elapsed += checkInterval
		checkCount++

		klog.Infof("Cleanup verification check %d/%d (elapsed: %v)", checkCount, int(waitDuration/checkInterval), elapsed)

		// Check if any resources have been recreated in gitopsOperatorNS
		operatorResourcesExist, err := verifyNamespaceCleanup(ctx, c, gitopsOperatorNS)
		if err != nil {
			klog.Warningf("Error checking operator namespace: %v", err)
		}

		if operatorResourcesExist {
			klog.Warning("Resources detected in operator namespace, attempting to delete again...")
			if err := deleteOperatorResources(ctx, c, gitopsOperatorNS); err != nil {
				klog.Errorf("Failed to re-delete operator resources: %v", err)
			}
		}
	}

	klog.Infof("Cleanup verification completed after %v", waitDuration)

	// Final check - verify operator resources are clean
	operatorClean, _ := verifyNamespaceCleanup(ctx, c, gitopsOperatorNS)

	if operatorClean {
		return fmt.Errorf("resources still exist after cleanup verification period")
	}

	return nil
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

// getCleanupVerificationWaitDuration returns the wait duration for cleanup verification
// Defaults to 0 seconds, can be overridden with CLEANUP_VERIFICATION_WAIT_SECONDS env var
// Set to 0 to skip verification
func getCleanupVerificationWaitDuration() time.Duration {
	if waitSecondsStr := os.Getenv("CLEANUP_VERIFICATION_WAIT_SECONDS"); waitSecondsStr != "" {
		if waitSeconds, err := strconv.Atoi(waitSecondsStr); err == nil {
			return time.Duration(waitSeconds) * time.Second
		}
	}
	return 0
}
