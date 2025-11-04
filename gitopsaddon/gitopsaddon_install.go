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
	"strings"
	"time"

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

// installOrUpdateOpenshiftGitops orchestrates the complete GitOps installation process
func (r *GitopsAddonReconciler) installOrUpdateOpenshiftGitops() error {
	klog.Info("Start templating and applying openshift gitops operator and its instance...")

	// 1. Always template and apply the openshift gitops operator manifest
	gitopsOperatorNsKey := types.NamespacedName{
		Name: r.GitopsOperatorNS,
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

	err = r.applyCRDIfNotExists("clusterversions", "config.openshift.io/v1", "charts/dep-crds/clusterversions.config.openshift.io.crd.yaml")
	if err != nil {
		return err
	}

	if err := r.CreateUpdateNamespace(gitopsOperatorNsKey); err == nil {
		err := r.templateAndApplyChart("charts/openshift-gitops-operator", r.GitopsOperatorNS, "openshift-gitops-operator")
		if err != nil {
			klog.Errorf("Failed to template and apply openshift-gitops-operator: %v", err)
			return err
		} else {
			klog.Info("Successfully templated and applied openshift-gitops-operator")
		}
	}

	// 2. Wait for the ArgoCD CR to be created by the openshift-gitops-operator
	klog.Info("Waiting for ArgoCD CR to be created by openshift-gitops-operator...")
	timeout := 1 * time.Minute
	err = r.waitForArgoCDCR(timeout)
	if err != nil {
		klog.Errorf("Failed to find ArgoCD CR within %v: %v", timeout, err)
		return err
	}

	// 3. Always render and apply openshift-gitops-dependency helm chart manifests
	gitopsNsKey := types.NamespacedName{
		Name: r.GitopsNS,
	}

	if err := r.CreateUpdateNamespace(gitopsNsKey); err == nil {
		err := r.renderAndApplyDependencyManifests("charts/openshift-gitops-dependency", r.GitopsNS)
		if err != nil {
			klog.Errorf("Failed to process openshift-gitops-dependency manifests: %v", err)
			return err
		} else {
			klog.Info("Successfully templated and applied openshift-gitops-dependency")
		}
	}

	// 4. Always template and apply the argocd-agent if enabled
	if r.ArgoCDAgentEnabled == "true" {
		klog.Info("Templating and applying argocd-agent...")

		// Template and apply argocd-agent in the same namespace as openshift-gitops
		if err := r.CreateUpdateNamespace(gitopsNsKey); err == nil {
			// Ensure argocd-redis secret exists before templating argocd-agent
			err := r.ensureArgoCDRedisSecret()
			if err != nil {
				klog.Errorf("Failed to ensure argocd-redis secret: %v", err)
				return err
			}

			err = r.templateAndApplyChart("charts/argocd-agent", r.GitopsNS, "argocd-agent")
			if err != nil {
				klog.Errorf("Failed to template and apply argocd-agent: %v", err)
				return err
			} else {
				klog.Info("Successfully templated and applied argocd-agent")
			}
		}
	} else {
		klog.Info("ArgoCD Agent not enabled, skipping installation")
	}

	return nil
}

// waitForArgoCDCR waits for the ArgoCD CR to be created by the operator
func (r *GitopsAddonReconciler) waitForArgoCDCR(timeout time.Duration) error {
	// Skip waiting in test environments with fake clients
	if r.Config != nil && r.Config.Host == "fake://test" {
		klog.Info("Using fake client, skipping ArgoCD CR wait")
		return nil
	}

	argoCDName := "openshift-gitops"
	argoCDKey := types.NamespacedName{
		Name:      argoCDName,
		Namespace: r.GitopsNS,
	}

	klog.Infof("Waiting for ArgoCD CR %s to be created in namespace %s...", argoCDName, r.GitopsNS)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		argoCD := &unstructured.Unstructured{}
		argoCD.SetAPIVersion("argoproj.io/v1beta1")
		argoCD.SetKind("ArgoCD")

		err := r.Get(ctx, argoCDKey, argoCD)
		if err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("ArgoCD CR %s not found yet, continuing to wait...", argoCDName)
				return false, nil // Continue waiting
			}
			klog.Errorf("Error checking for ArgoCD CR %s, continuing to wait..., err: %v", argoCDName, err)
			return false, nil // Continue waiting
		}

		klog.Infof("ArgoCD CR %s found successfully", argoCDName)
		return true, nil // ArgoCD CR found, stop waiting
	})
}

// ensureArgoCDRedisSecret creates the argocd-redis secret if it doesn't exist
func (r *GitopsAddonReconciler) ensureArgoCDRedisSecret() error {
	// Check if argocd-redis secret already exists
	argoCDRedisSecret := &corev1.Secret{}
	argoCDRedisSecretKey := types.NamespacedName{
		Name:      "argocd-redis",
		Namespace: r.GitopsNS,
	}

	err := r.Get(context.TODO(), argoCDRedisSecretKey, argoCDRedisSecret)
	if err == nil {
		klog.Info("argocd-redis secret already exists, skipping creation")
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check argocd-redis secret: %w", err)
	}

	klog.Info("argocd-redis secret not found, creating it...")

	// Find the secret ending with "redis-initial-password"
	secretList := &corev1.SecretList{}
	err = r.List(context.TODO(), secretList, client.InNamespace(r.GitopsNS))
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %s: %w", r.GitopsNS, err)
	}

	var initialPasswordSecret *corev1.Secret
	for i := range secretList.Items {
		if strings.HasSuffix(secretList.Items[i].Name, "redis-initial-password") {
			initialPasswordSecret = &secretList.Items[i]
			break
		}
	}

	if initialPasswordSecret == nil {
		return fmt.Errorf("no secret found ending with 'redis-initial-password' in namespace %s", r.GitopsNS)
	}

	// Extract the admin.password value
	adminPasswordBytes, exists := initialPasswordSecret.Data["admin.password"]
	if !exists {
		return fmt.Errorf("admin.password not found in secret %s", initialPasswordSecret.Name)
	}

	// Create the argocd-redis secret
	argoCDRedisSecretNew := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-redis",
			Namespace: r.GitopsNS,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopsaddon": "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"auth": adminPasswordBytes,
		},
	}

	err = r.Create(context.TODO(), argoCDRedisSecretNew)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			klog.Info("argocd-redis secret was created by another process, continuing...")
			return nil
		}

		return fmt.Errorf("failed to create argocd-redis secret: %w", err)
	}

	klog.Info("Successfully created argocd-redis secret")
	return nil
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

// handleArgoCDManifest waits for the default ArgoCD CR and overrides its spec
func (r *GitopsAddonReconciler) handleArgoCDManifest(obj *unstructured.Unstructured) error {
	klog.Info("Handling ArgoCD manifest - waiting for default ArgoCD CR and overriding spec")

	argoCDKey := types.NamespacedName{
		Name:      "openshift-gitops",
		Namespace: obj.GetNamespace(),
	}

	// Skip waiting in test environments with fake clients
	if r.Config != nil && r.Config.Host == "fake://test" {
		klog.Info("Using fake client, applying ArgoCD manifest directly")
		return r.applyManifest(obj)
	}

	// Wait up to 1 minute for the ArgoCD CR to exist
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var existingArgoCD *unstructured.Unstructured
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		argoCD := &unstructured.Unstructured{}
		argoCD.SetAPIVersion("argoproj.io/v1beta1")
		argoCD.SetKind("ArgoCD")

		err := r.Get(ctx, argoCDKey, argoCD)
		if err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("ArgoCD CR %s not found yet, continuing to wait...", argoCDKey.Name)
				return false, nil
			}
			return false, err
		}

		existingArgoCD = argoCD
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to wait for ArgoCD CR: %w", err)
	}

	// Override the spec with the rendered spec
	renderedSpec, found, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil {
		return fmt.Errorf("failed to get rendered spec: %w", err)
	}
	if !found {
		return fmt.Errorf("no spec found in rendered ArgoCD manifest")
	}

	// Update the existing ArgoCD CR with the new spec
	err = unstructured.SetNestedMap(existingArgoCD.Object, renderedSpec, "spec")
	if err != nil {
		return fmt.Errorf("failed to set spec: %w", err)
	}

	err = r.Update(context.TODO(), existingArgoCD)
	if err != nil {
		return fmt.Errorf("failed to update ArgoCD CR: %w", err)
	}

	klog.Info("Successfully updated ArgoCD CR spec")
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
	// Wait for the service account to exist (up to 1 minute)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
