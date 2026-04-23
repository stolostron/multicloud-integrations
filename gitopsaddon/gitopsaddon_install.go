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

// installViaOLMSubscription creates an OLM subscription on OCP clusters
func (r *GitopsAddonReconciler) installViaOLMSubscription(ctx context.Context) error {
	if err := r.createOrUpdateOLMSubscription(ctx); err != nil {
		return fmt.Errorf("failed to create OLM subscription: %w", err)
	}

	// Also check the OLM subscription namespace for the deployment, since OLM
	// creates the operator deployment in the same namespace as the CSV/Subscription.
	subNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAMESPACE", "openshift-operators")
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

	gitopsOperatorNsKey := types.NamespacedName{Name: GitOpsOperatorNamespace}
	if err := r.CreateUpdateNamespace(gitopsOperatorNsKey); err != nil {
		return fmt.Errorf("failed to create/update %s namespace: %w", GitOpsOperatorNamespace, err)
	}

	if err := r.templateAndApplyChart("charts/openshift-gitops-operator", GitOpsOperatorNamespace, "openshift-gitops-operator"); err != nil {
		klog.Errorf("Failed to template and apply openshift-gitops-operator: %v", err)
		return err
	}
	klog.Info("Successfully templated and applied openshift-gitops-operator")

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

// createOrUpdateOLMSubscription creates or updates the OLM subscription for openshift-gitops-operator.
// Subscription values are read from env vars (set by hub when olmSubscription.enabled=true) with
// hardcoded defaults as fallback.
func (r *GitopsAddonReconciler) createOrUpdateOLMSubscription(ctx context.Context) error {
	subName := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAME", "openshift-gitops-operator")
	subNamespace := getOLMEnvOrDefault("OLM_SUBSCRIPTION_NAMESPACE", "openshift-operators")
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
					"apps.open-cluster-management.io/gitopsaddon": "true",
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
	if labels != nil && labels["apps.open-cluster-management.io/gitopsaddon"] == "true" {
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

		klog.Infof("OLM subscription %s exists with gitopsaddon label, updating spec", subName)
		sub.SetResourceVersion(existing.GetResourceVersion())
		return r.Update(ctx, sub)
	}

	klog.Infof("OLM subscription %s already exists in namespace %s without gitopsaddon label (pre-existing), skipping", subName, subNamespace)
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
	targetNS := getArgoCDNamespace()

	// First check if the secret exists in the namespace
	secret := &corev1.Secret{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: targetNS}, secret)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(2).Infof("Image pull secret %s not found in namespace %s, skipping SA patching", secretName, targetNS)
			return nil // Not an error - secret might not exist in OCP environments where node-level secrets work
		}
		return fmt.Errorf("failed to check for image pull secret: %v", err)
	}

	// List all ServiceAccounts in the namespace
	saList := &corev1.ServiceAccountList{}
	if err := r.List(context.TODO(), saList, &client.ListOptions{Namespace: targetNS}); err != nil {
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
				klog.Warningf("Failed to patch ServiceAccount %s/%s: %v", targetNS, sa.Name, err)
				continue
			}
			klog.Infof("Patched ServiceAccount %s/%s with imagePullSecrets", targetNS, sa.Name)
			patchedCount++
		}
	}

	if patchedCount > 0 {
		klog.Infof("Patched %d ServiceAccounts with imagePullSecrets", patchedCount)
	}

	return nil
}

// deletePodsWithImagePullIssues deletes pods in the ArgoCD namespace that have ImagePullBackOff or ErrImagePull status.
// This forces Kubernetes to recreate them, and the new pods will use the patched ServiceAccounts with imagePullSecrets.
func (r *GitopsAddonReconciler) deletePodsWithImagePullIssues() error {
	targetNS := getArgoCDNamespace()
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


