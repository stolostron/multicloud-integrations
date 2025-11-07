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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PauseMarkerName is the name of the ConfigMap used to pause the gitops-addon controller
	PauseMarkerName = "gitops-addon-pause"
)

// uninstallGitopsAgent uninstalls the Gitops agent addon for cleanup reconciler
func (r *GitopsAddonCleanupReconciler) uninstallGitopsAgent(ctx context.Context) error {
	return uninstallGitopsAgentInternal(ctx, r.Client, r.GitopsOperatorNS, r.GitopsNS)
}

// uninstallGitopsAgentInternal performs the actual uninstall logic
// Step 0: Create pause marker to stop the gitops-addon controller from reconciling
// Step 1: Delete ArgoCD CR and wait for it to be fully removed
// Step 2: Delete argocd-agent resources from openshift-gitops namespace
// Step 3: Delete openshift-gitops-operator resources from openshift-gitops-operator namespace
// All steps attempt to continue even if one fails to ensure maximum cleanup
func uninstallGitopsAgentInternal(ctx context.Context, c client.Client, gitopsOperatorNS, gitopsNS string) error {
	klog.Infof("Starting Gitops agent addon uninstall - gitopsOperatorNS: %s, gitopsNS: %s", gitopsOperatorNS, gitopsNS)
	var errors []error

	// Step 0: Create pause marker to prevent the gitops-addon controller from reconciling resources
	klog.Infof("Step 0: Creating pause marker in namespace: %s", gitopsOperatorNS)
	if err := createPauseMarker(ctx, c, gitopsOperatorNS); err != nil {
		klog.Errorf("Error creating pause marker (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to create pause marker: %w", err))
	}

	// Step 1: Delete ArgoCD CR and wait for it to be fully removed
	klog.Info("Step 1: Deleting ArgoCD CR from namespace: " + gitopsNS)
	if err := deleteArgoCDCR(ctx, c, gitopsNS); err != nil {
		klog.Errorf("Error deleting ArgoCD CR (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete ArgoCD CR: %w", err))
	}

	// Step 2: Delete argocd-agent resources from openshift-gitops namespace
	klog.Info("Step 2: Deleting argocd-agent resources from namespace: " + gitopsNS)
	if err := deleteArgoCDAgentResources(ctx, c, gitopsNS); err != nil {
		klog.Errorf("Error deleting argocd-agent resources (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete argocd-agent resources: %w", err))
	}

	// Step 3: Delete openshift-gitops-operator resources from openshift-gitops-operator namespace
	klog.Info("Step 3: Deleting openshift-gitops-operator resources from namespace: " + gitopsOperatorNS)
	if err := deleteOperatorResources(ctx, c, gitopsOperatorNS); err != nil {
		klog.Errorf("Error deleting openshift-gitops-operator resources (continuing with cleanup): %v", err)
		errors = append(errors, fmt.Errorf("failed to delete openshift-gitops-operator resources: %w", err))
	}

	// Step 4: Wait and re-verify that resources stay deleted
	// This is critical to handle timing issues where the controller might try to reconcile
	// Wait for at least 3 minutes (longer than typical reconciliation interval)
	// Can be disabled for tests by setting CLEANUP_VERIFICATION_WAIT_SECONDS=0
	waitDuration := getCleanupVerificationWaitDuration()
	if waitDuration > 0 {
		klog.Infof("Step 4: Waiting and re-verifying that resources stay deleted (%v)...", waitDuration)
		if err := waitAndVerifyCleanup(ctx, c, gitopsOperatorNS, gitopsNS, waitDuration); err != nil {
			klog.Errorf("Error during cleanup verification (resources may have been recreated): %v", err)
			errors = append(errors, fmt.Errorf("cleanup verification failed: %w", err))
		}
	} else {
		klog.Info("Step 4: Skipping cleanup verification (wait duration is 0)")
	}

	if len(errors) > 0 {
		klog.Errorf("Gitops agent addon uninstall completed with %d error(s)", len(errors))
		// Return the first error but log all of them
		return errors[0]
	}

	klog.Info("Successfully completed Gitops agent addon uninstall")
	return nil
}

// deleteArgoCDCR deletes the ArgoCD CR and retries until it's fully removed
func deleteArgoCDCR(ctx context.Context, c client.Client, gitopsNS string) error {
	klog.Info("Deleting ArgoCD CR with retry until fully removed")

	argoCDKey := types.NamespacedName{
		Name:      "openshift-gitops",
		Namespace: gitopsNS,
	}

	// Retry loop: 2 minutes total (24 iterations * 5 seconds)
	maxWait := 2 * time.Minute
	checkInterval := 5 * time.Second
	elapsed := time.Duration(0)
	deleted := false

	for elapsed < maxWait {
		argoCD := &unstructured.Unstructured{}
		argoCD.SetAPIVersion("argoproj.io/v1beta1")
		argoCD.SetKind("ArgoCD")

		err := c.Get(ctx, argoCDKey, argoCD)
		if errors.IsNotFound(err) {
			// ArgoCD CR is fully deleted
			if deleted {
				klog.Info("ArgoCD CR has been fully deleted")
			} else {
				klog.Info("ArgoCD CR not found (already deleted or never existed)")
			}
			return nil
		}

		if err != nil {
			klog.Warningf("Error checking ArgoCD CR status: %v", err)
			// Continue trying despite errors
		} else {
			// ArgoCD CR exists, delete it
			klog.V(1).Infof("ArgoCD CR exists, attempting deletion (elapsed: %v)", elapsed)
			if err := c.Delete(ctx, argoCD); err != nil && !errors.IsNotFound(err) {
				klog.Warningf("Failed to delete ArgoCD CR: %v", err)
			} else {
				deleted = true
			}
		}

		// Wait before next retry
		time.Sleep(checkInterval)
		elapsed += checkInterval
	}

	// Timeout reached - check final status
	argoCD := &unstructured.Unstructured{}
	argoCD.SetAPIVersion("argoproj.io/v1beta1")
	argoCD.SetKind("ArgoCD")
	err := c.Get(ctx, argoCDKey, argoCD)
	if errors.IsNotFound(err) {
		klog.Warning("ArgoCD CR deleted after timeout")
		return nil
	}

	return fmt.Errorf("timeout: ArgoCD CR still exists after %v", maxWait)
}

// deleteArgoCDAgentResources deletes argocd-agent resources from openshift-gitops namespace
// Resources from gitopsaddon/charts/argocd-agent/templates/
func deleteArgoCDAgentResources(ctx context.Context, c client.Client, gitopsNS string) error {
	klog.Info("Deleting argocd-agent resources with retry until fully removed")

	// Resource types from argocd-agent chart
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

	return deleteResourcesWithRetry(ctx, c, namespacedResourceTypes, clusterResourceTypes, gitopsNS, 4*time.Minute, true)
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

				location := item.GetName()
				if namespace != "" {
					location = fmt.Sprintf("%s/%s", namespace, item.GetName())
				}
				klog.Infof("Deleting %s: %s", rt.kind, location)
				if err := c.Delete(ctx, &item); err != nil && !errors.IsNotFound(err) {
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
func createPauseMarker(ctx context.Context, c client.Client, namespace string) error {
	klog.Infof("Creating pause marker ConfigMap '%s' in namespace: %s", PauseMarkerName, namespace)

	pauseMarker := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PauseMarkerName,
			Namespace: namespace,
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

	err := c.Create(ctx, pauseMarker)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create pause marker: %w", err)
	}

	klog.Infof("Pause marker created successfully in namespace: %s", namespace)
	return nil
}

// IsPaused checks if the gitops-addon controller should be paused
// This is exported so the controller can use it
func IsPaused(ctx context.Context, c client.Client, namespace string) bool {
	pauseMarker := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Name:      PauseMarkerName,
		Namespace: namespace,
	}

	err := c.Get(ctx, key, pauseMarker)
	if err != nil {
		if errors.IsNotFound(err) {
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
func waitAndVerifyCleanup(ctx context.Context, c client.Client, gitopsOperatorNS, gitopsNS string, waitDuration time.Duration) error {
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

		// Check if any resources have been recreated in gitopsNS
		gitopsResourcesExist, err := verifyNamespaceCleanup(ctx, c, gitopsNS)
		if err != nil {
			klog.Warningf("Error checking gitops namespace: %v", err)
		}

		if gitopsResourcesExist {
			klog.Warning("Resources detected in gitops namespace, attempting to delete again...")
			if err := deleteArgoCDAgentResources(ctx, c, gitopsNS); err != nil {
				klog.Errorf("Failed to re-delete gitops agent resources: %v", err)
			}
		}
	}

	klog.Infof("Cleanup verification completed after %v", waitDuration)

	// Final check - verify everything is still clean
	operatorClean, _ := verifyNamespaceCleanup(ctx, c, gitopsOperatorNS)
	gitopsClean, _ := verifyNamespaceCleanup(ctx, c, gitopsNS)

	if operatorClean || gitopsClean {
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

	if err != nil && !errors.IsNotFound(err) {
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
