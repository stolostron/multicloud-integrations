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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// installOrUpdateOpenshiftGitops orchestrates the complete GitOps installation process
// Note: ArgoCD CR is now created by Policy (argocd_policy.go on the hub), not by this addon.
// The addon's job is to install the operator and prepare the namespace.
func (r *GitopsAddonReconciler) installOrUpdateOpenshiftGitops() error {
	klog.Info("Start templating and applying openshift gitops operator and its instance...")

	// 1. Create operator namespace with proper labels for pull secret sync
	gitopsOperatorNsKey := types.NamespacedName{
		Name: GitOpsOperatorNamespace,
	}

	// Install the Route CRD if it doesn't already exist (e.g. on Kind/EKS clusters without OCP)
	// The Route CRD is maintained separately from the operator chart to avoid overwriting OCP-provided Routes
	err := r.applyCRDIfNotExists(RouteCRDFS, "routes", "route.openshift.io/v1", "routes-openshift-crd/routes.route.openshift.io.crd.yaml")
	if err != nil {
		return err
	}

	if err := r.CreateUpdateNamespace(gitopsOperatorNsKey); err == nil {
		// 2. Template and apply the openshift gitops operator manifest
		err := r.templateAndApplyChart("charts/openshift-gitops-operator", GitOpsOperatorNamespace, "openshift-gitops-operator")
		if err != nil {
			klog.Errorf("Failed to template and apply openshift-gitops-operator: %v", err)
			return err
		}
		klog.Info("Successfully templated and applied openshift-gitops-operator")
	}

	// 3. Wait for the operator deployment to be ready before creating the ArgoCD CR
	klog.Info("Waiting for ArgoCD operator deployment to be ready...")
	timeout := 2 * time.Minute
	err = r.waitForOperatorReady(timeout)
	if err != nil {
		klog.Errorf("Failed to wait for ArgoCD operator: %v", err)
		return err
	}
	klog.Info("ArgoCD operator is ready")

	// 4. Create the openshift-gitops namespace (ArgoCD CR is created by Policy from the hub)
	gitopsNsKey := types.NamespacedName{
		Name: GitOpsNamespace,
	}
	if err := r.CreateUpdateNamespace(gitopsNsKey); err == nil {
		klog.Info("Successfully created/updated openshift-gitops namespace")
	}

	// 5. Patch ArgoCD ServiceAccounts with imagePullSecrets
	// The ArgoCD operator creates ServiceAccounts without imagePullSecrets, so we need to patch them
	// This is needed for non-OCP clusters (like Kind, EKS) where node-level pull secrets don't exist
	if err := r.patchArgoCDServiceAccountsWithImagePullSecrets(); err != nil {
		klog.Warningf("Failed to patch ArgoCD ServiceAccounts: %v (continuing anyway)", err)
	}

	// 6. Check and delete pods with image pull issues on every reconcile cycle
	// This handles the case where pods were already in ImagePullBackOff from a previous cycle
	if err := r.deletePodsWithImagePullIssues(); err != nil {
		klog.Warningf("Failed to delete pods with image pull issues: %v", err)
	}

	// 7. Log ArgoCD Agent status (operator handles deployment via ArgoCD CR)
	if r.AddonConfig.ArgoCDAgentEnabled {
		klog.Info("ArgoCD Agent is enabled - deployment is handled by the argocd-operator via the ArgoCD CR spec.argoCDAgent.agent")
	} else {
		klog.Info("ArgoCD Agent not enabled")
	}

	return nil
}

// waitForOperatorReady waits for the ArgoCD operator deployment to be ready
func (r *GitopsAddonReconciler) waitForOperatorReady(timeout time.Duration) error {
	// Skip waiting in test environments with fake clients
	if r.Config != nil && r.Config.Host == "fake://test" {
		klog.Info("Using fake client, skipping operator ready wait")
		return nil
	}

	deploymentName := "openshift-gitops-operator-controller-manager"
	deploymentKey := types.NamespacedName{
		Name:      deploymentName,
		Namespace: GitOpsOperatorNamespace,
	}

	klog.Infof("Waiting for ArgoCD operator deployment %s to be ready in namespace %s...", deploymentName, GitOpsOperatorNamespace)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deployment := &appsv1.Deployment{}
		err := r.Get(ctx, deploymentKey, deployment)
		if err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("Operator deployment %s not found yet, continuing to wait...", deploymentName)
				return false, nil // Continue waiting
			}
			klog.Errorf("Error checking for operator deployment %s: %v", deploymentName, err)
			return false, nil // Continue waiting on transient errors
		}

		// Check if deployment is available
		for _, cond := range deployment.Status.Conditions {
			if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
				klog.Infof("Operator deployment %s is available", deploymentName)
				return true, nil
			}
		}

		klog.Infof("Operator deployment %s not yet available (replicas: %d/%d)", deploymentName, deployment.Status.ReadyReplicas, *deployment.Spec.Replicas)
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
						"addon.open-cluster-management.io/namespace":  "true", //enable copying the image pull secret to the NS
						"apps.open-cluster-management.io/gitopsaddon": "true",
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

	// Ensure the labels are set (in case namespace already existed without them)
	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
	}
	namespace.Labels["addon.open-cluster-management.io/namespace"] = "true"
	namespace.Labels["apps.open-cluster-management.io/gitopsaddon"] = "true"

	if err := r.Update(context.TODO(), namespace); err != nil {
		klog.Errorf("Failed to update the labels to the %s namespace, err: %v", nameSpaceKey.Name, err)
		return err
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

	// Update existing secret
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

// patchArgoCDServiceAccountsWithImagePullSecrets patches ArgoCD ServiceAccounts with imagePullSecrets
// This is needed for non-OCP clusters (like Kind, EKS) where node-level pull secrets don't exist.
// The ArgoCD operator creates ServiceAccounts without imagePullSecrets, so pods can't authenticate
// to registry.redhat.io. This function adds the pull secret reference to allow image pulls.
//
// Note: The ArgoCD CRD does NOT have an imagePullSecrets field, so SA patching is the only option.
// We dynamically list all SAs in the namespace to catch all ArgoCD-managed SAs including:
// - application-controller, server, repo-server, redis, redis-ha, redis-ha-haproxy
// - dex-server (if SSO enabled), applicationset-controller, notifications-controller
func (r *GitopsAddonReconciler) patchArgoCDServiceAccountsWithImagePullSecrets() error {
	secretName := "open-cluster-management-image-pull-credentials"

	// First check if the secret exists in the namespace
	secret := &corev1.Secret{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: GitOpsNamespace}, secret)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(2).Infof("Image pull secret %s not found in namespace %s, skipping SA patching", secretName, GitOpsNamespace)
			return nil // Not an error - secret might not exist in OCP environments where node-level secrets work
		}
		return fmt.Errorf("failed to check for image pull secret: %v", err)
	}

	// List all ServiceAccounts in the namespace
	saList := &corev1.ServiceAccountList{}
	if err := r.List(context.TODO(), saList, &client.ListOptions{Namespace: GitOpsNamespace}); err != nil {
		return fmt.Errorf("failed to list ServiceAccounts: %v", err)
	}

	secretRef := corev1.LocalObjectReference{Name: secretName}
	patchedCount := 0

	for i := range saList.Items {
		sa := &saList.Items[i]

		// Check if the secret is already in imagePullSecrets
		found := false
		for _, ref := range sa.ImagePullSecrets {
			if ref.Name == secretName {
				found = true
				break
			}
		}

		if !found {
			// Add the secret reference
			sa.ImagePullSecrets = append(sa.ImagePullSecrets, secretRef)
			if err := r.Update(context.TODO(), sa); err != nil {
				klog.Warningf("Failed to patch ServiceAccount %s/%s: %v", GitOpsNamespace, sa.Name, err)
				continue
			}
			klog.Infof("Patched ServiceAccount %s/%s with imagePullSecrets", GitOpsNamespace, sa.Name)
			patchedCount++
		}
	}

	if patchedCount > 0 {
		klog.Infof("Patched %d ServiceAccounts with imagePullSecrets", patchedCount)
	}

	return nil
}

// deletePodsWithImagePullIssues deletes pods in the GitOps namespace that have ImagePullBackOff or ErrImagePull status
// This forces Kubernetes to recreate them, and the new pods will use the patched ServiceAccounts with imagePullSecrets
func (r *GitopsAddonReconciler) deletePodsWithImagePullIssues() error {
	podList := &corev1.PodList{}
	if err := r.List(context.TODO(), podList, &client.ListOptions{Namespace: GitOpsNamespace}); err != nil {
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

