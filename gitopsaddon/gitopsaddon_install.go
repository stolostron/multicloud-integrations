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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
)

// installOrUpdateOpenshiftGitops orchestrates the complete GitOps installation process
func (r *GitopsAddonReconciler) installOrUpdateOpenshiftGitops() error {
	klog.Info("Start templating and applying openshift gitops operator and its instance...")

	// 1. Always template and apply the openshift gitops operator manifest
	gitopsOperatorNsKey := types.NamespacedName{
		Name: GitOpsOperatorNamespace,
	}

	// install dependency CRDs if it doesn't exist
	err := r.applyCRDIfNotExists("gitopsservices", "pipelines.openshift.io/v1alpha1", "charts/dep-crds/gitopsservices.pipelines.openshift.io.crd.yaml")
	if err != nil {
		return err
	}

	err = r.applyCRDIfNotExists("routes", "route.openshift.io/v1", "charts/dep-crds/routes.route.openshift.io.crd.yaml")
	if err != nil {
		return err
	}

	// Only install clusterversions CRD if we're on OpenShift (i.e., it already exists).
	// On non-OpenShift clusters (like kind), installing this CRD would cause the argocd-operator
	// to incorrectly detect the cluster as OpenShift and apply incorrect security contexts.
	// We check if the CRD already exists first - if it does, it's a real OpenShift cluster.
	if r.crdExists("clusterversions", "config.openshift.io/v1") {
		klog.Info("ClusterVersion CRD already exists (OpenShift cluster detected), skipping installation")
	} else {
		klog.Info("ClusterVersion CRD does not exist (non-OpenShift cluster), skipping installation to avoid operator misconfiguration")
	}

	if err := r.CreateUpdateNamespace(gitopsOperatorNsKey); err == nil {
		err := r.templateAndApplyChart("charts/openshift-gitops-operator", GitOpsOperatorNamespace, "openshift-gitops-operator")
		if err != nil {
			klog.Errorf("Failed to template and apply openshift-gitops-operator: %v", err)
			return err
		} else {
			klog.Info("Successfully templated and applied openshift-gitops-operator")
		}
	}

	// 2. Wait for the operator deployment to be ready before creating the ArgoCD CR
	klog.Info("Waiting for ArgoCD operator deployment to be ready...")
	timeout := 2 * time.Minute
	err = r.waitForOperatorReady(timeout)
	if err != nil {
		klog.Errorf("Failed to wait for ArgoCD operator: %v", err)
		return err
	}
	klog.Info("ArgoCD operator is ready")

	// 3. Create or update the ArgoCD CR via openshift-gitops-dependency helm chart
	gitopsNsKey := types.NamespacedName{
		Name: GitOpsNamespace,
	}

	if err := r.CreateUpdateNamespace(gitopsNsKey); err == nil {
		err := r.renderAndApplyDependencyManifests("charts/openshift-gitops-dependency", GitOpsNamespace)
		if err != nil {
			klog.Errorf("Failed to process openshift-gitops-dependency manifests: %v", err)
			return err
		} else {
			klog.Info("Successfully templated and applied openshift-gitops-dependency")
		}
	}

	// 4. Log ArgoCD Agent status (operator handles deployment via ArgoCD CR)
	if r.ArgoCDAgentEnabled == "true" {
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

	deploymentName := "argocd-operator-controller-manager"
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
				klog.Errorf("Failed to create the openshift-gitops-operator namespace, err: %v", err)
				return err
			}
		} else {
			klog.Errorf("Failed to get the openshift-gitops-operator namespace, err: %v", err)
			return err
		}
	}

	// Check if namespace should be skipped due to annotation
	annotations := namespace.GetAnnotations()
	if annotations != nil && annotations["gitops-addon.open-cluster-management.io/skip"] == "true" {
		klog.Infof("Skipping namespace %s due to skip annotation", namespace.Name)
		return nil
	}

	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
	}
	namespace.Labels["addon.open-cluster-management.io/namespace"] = "true"
	namespace.Labels["apps.open-cluster-management.io/gitopsaddon"] = "true"

	if err := r.Update(context.TODO(), namespace); err != nil {
		klog.Errorf("Failed to update the labels to the openshift-gitops-operator namespace, err: %v", err)
		return err
	}

	// If on the hcp hosted cluster, there is no klusterlet operator
	// As a result, the open-cluster-management-image-pull-credentials secret is not automatically synced up
	// to the new namespace even though the addon.open-cluster-management.io/namespace: true label is specified
	// To support all kinds of clusters, we proactively copy the original git addon
	// open-cluster-management-image-pull-credentials secret to the new namespace
	err = r.copyImagePullSecret(nameSpaceKey)
	if err != nil {
		return err
	}

	// Wait 1 min for the image pull secret `open-cluster-management-image-pull-credentials`
	// to be generated
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

		klog.Infof("the image pull credentials secret is NOT found, wait for the next check, err: %v", err)

		time.Sleep(interval)
	}

	// Timeout reached
	klog.Errorf("Timeout waiting for secret %s in namespace %s", secretName, nameSpaceKey.Name)

	return fmt.Errorf("timeout waiting for secret %s in namespace %s", secretName, nameSpaceKey.Name)
}

// copyImagePullSecret copies the image pull secret to the target namespace
func (r *GitopsAddonReconciler) copyImagePullSecret(nameSpaceKey types.NamespacedName) error {
	secretName := "open-cluster-management-image-pull-credentials"
	secret := &corev1.Secret{}
	gitopsAddonNs := utils.GetComponentNamespace("open-cluster-management-agent-addon")

	// Get the original gitops addon image pull secret
	err := r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: gitopsAddonNs}, secret)
	if err != nil {
		klog.Errorf("gitops addon image pull secret no found. secret: %v/%v, err: %v", gitopsAddonNs, secretName, err)
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
	newSecret.ObjectMeta.DeletionGracePeriodSeconds = nil
	newSecret.ObjectMeta.OwnerReferences = nil
	newSecret.ObjectMeta.ManagedFields = nil

	// Create the new Secret in the target namespace
	err = r.Create(context.TODO(), newSecret)
	if err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create target secret %s/%s: %w", nameSpaceKey.Name, secretName, err)
		}
	}

	klog.Infof("Successfully copied secret %s/%s to %s/%s", gitopsAddonNs, secretName, nameSpaceKey.Name, secretName)

	return nil
}

// handleDefaultServiceAccount waits for the default service account and appends imagePullSecrets
func (r *GitopsAddonReconciler) handleDefaultServiceAccount(obj *unstructured.Unstructured) error {
	klog.Info("Handling default ServiceAccount - waiting for existence and appending imagePullSecrets")

	saKey := types.NamespacedName{
		Name:      "default",
		Namespace: obj.GetNamespace(),
	}

	return r.waitAndAppendImagePullSecrets(saKey, obj)
}

// handleRedisServiceAccount waits for the redis service account and appends imagePullSecrets
func (r *GitopsAddonReconciler) handleRedisServiceAccount(obj *unstructured.Unstructured) error {
	klog.Info("Handling Redis ServiceAccount - waiting for ArgoCD deployment and appending imagePullSecrets")

	saKey := types.NamespacedName{
		Name:      "openshift-gitops-argocd-redis",
		Namespace: obj.GetNamespace(),
	}

	return r.waitAndAppendImagePullSecrets(saKey, obj)
}

// handleApplicationControllerServiceAccount waits for the application controller service account and appends imagePullSecrets
func (r *GitopsAddonReconciler) handleApplicationControllerServiceAccount(obj *unstructured.Unstructured) error {
	klog.Info("Handling Application Controller ServiceAccount - waiting for ArgoCD deployment and appending imagePullSecrets")

	saKey := types.NamespacedName{
		Name:      "openshift-gitops-argocd-application-controller",
		Namespace: obj.GetNamespace(),
	}

	return r.waitAndAppendImagePullSecrets(saKey, obj)
}

// handleDefaultAppProject applies the default AppProject directly
func (r *GitopsAddonReconciler) handleDefaultAppProject(obj *unstructured.Unstructured) error {
	klog.Info("Handling default AppProject - applying directly")

	return r.applyManifest(obj)
}

// handleApplicationControllerClusterRoleBinding applies the ClusterRoleBinding directly
func (r *GitopsAddonReconciler) handleApplicationControllerClusterRoleBinding(obj *unstructured.Unstructured) error {
	klog.Info("Handling Application Controller ClusterRoleBinding - applying directly")

	return r.applyManifest(obj)
}

// waitAndAppendImagePullSecrets waits for a service account to exist and appends imagePullSecrets
func (r *GitopsAddonReconciler) waitAndAppendImagePullSecrets(saKey types.NamespacedName, renderedObj *unstructured.Unstructured) error {
	// Wait for the service account to exist (up to 3 minutes - ArgoCD operator needs time to create resources)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var existingSA *corev1.ServiceAccount
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		sa := &corev1.ServiceAccount{}
		err := r.Get(ctx, saKey, sa)
		if err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("ServiceAccount %s not found yet, continuing to wait...", saKey.Name)
				return false, nil
			}
			return false, err
		}

		existingSA = sa
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to wait for ServiceAccount %s: %w", saKey.Name, err)
	}

	// Get imagePullSecrets from the rendered manifest
	renderedImagePullSecrets, found, err := unstructured.NestedSlice(renderedObj.Object, "imagePullSecrets")
	if err != nil {
		return fmt.Errorf("failed to get rendered imagePullSecrets: %w", err)
	}
	if !found || len(renderedImagePullSecrets) == 0 {
		klog.Infof("No imagePullSecrets to append for ServiceAccount %s", saKey.Name)
		return nil
	}

	// Convert rendered imagePullSecrets to the correct format
	var secretRefs []corev1.LocalObjectReference
	for _, secret := range renderedImagePullSecrets {
		secretMap, ok := secret.(map[string]interface{})
		if !ok {
			continue
		}
		if name, exists := secretMap["name"]; exists {
			if nameStr, ok := name.(string); ok {
				secretRefs = append(secretRefs, corev1.LocalObjectReference{Name: nameStr})
			}
		}
	}

	if len(secretRefs) == 0 {
		klog.Infof("No valid imagePullSecrets found to append for ServiceAccount %s", saKey.Name)
		return nil
	}

	// Check if the secrets are already present
	secretsToAdd := []corev1.LocalObjectReference{}
	for _, newSecret := range secretRefs {
		found := false
		for _, existingSecret := range existingSA.ImagePullSecrets {
			if existingSecret.Name == newSecret.Name {
				found = true
				break
			}
		}
		if !found {
			secretsToAdd = append(secretsToAdd, newSecret)
		}
	}

	if len(secretsToAdd) == 0 {
		klog.Infof("All imagePullSecrets already exist for ServiceAccount %s", saKey.Name)
		return nil
	}

	// Append new secrets to existing ones
	existingSA.ImagePullSecrets = append(existingSA.ImagePullSecrets, secretsToAdd...)

	// Update the service account
	err = r.Update(context.TODO(), existingSA)
	if err != nil {
		return fmt.Errorf("failed to update ServiceAccount %s with imagePullSecrets: %w", saKey.Name, err)
	}

	klog.Infof("Successfully appended %d imagePullSecrets to ServiceAccount %s", len(secretsToAdd), saKey.Name)
	return nil
}
