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

package gitopscluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	workv1 "open-cluster-management.io/api/work/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
)

const (
	// DefaultAddonImage is the default image to use if controller image cannot be determined
	DefaultAddonImage = "quay.io/stolostron/multicloud-integrations"

	// ControllerImageEnvVar is the environment variable name for the controller image
	ControllerImageEnvVar = "CONTROLLER_IMAGE"

	// GitOpsAddonLabel is the label used to identify gitops-addon resources for cleanup
	GitOpsAddonLabel = "apps.open-cluster-management.io/gitopsaddon"
)

// imagePullPolicyForImage returns PullAlways for :latest tags (which change
// frequently during dev/test and must never be cached), PullIfNotPresent for
// digest-pinned or versioned images.
func imagePullPolicyForImage(image string) corev1.PullPolicy {
	if strings.HasSuffix(image, ":latest") {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}

// getControllerImage retrieves the controller's image from environment variable or auto-detects it
// Auto-detection: queries the Kubernetes API to find its own pod and reads the image
// Returns an error if the image cannot be determined
func (r *ReconcileGitOpsCluster) getControllerImage() (string, error) {
	// Try to get image from environment variable first (explicit configuration takes precedence)
	if image := os.Getenv(ControllerImageEnvVar); image != "" {
		klog.V(2).Infof("Found controller image from environment: %s", image)
		return image, nil
	}

	// Auto-detect: get pod name and namespace from environment (set via downward API)
	podName := os.Getenv("POD_NAME")
	podNamespace := os.Getenv("POD_NAMESPACE")

	if podName == "" || podNamespace == "" {
		return "", fmt.Errorf("controller image configuration missing: %s environment variable not set and auto-detection failed (POD_NAME or POD_NAMESPACE not set)", ControllerImageEnvVar)
	}

	// Query the pod to get its image
	pod, err := r.authClient.CoreV1().Pods(podNamespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to auto-detect controller image: could not get pod %s/%s: %w", podNamespace, podName, err)
	}

	// Find the gitopscluster container by checking if command contains "gitopscluster"
	for _, container := range pod.Spec.Containers {
		for _, cmd := range container.Command {
			if strings.Contains(cmd, "gitopscluster") {
				klog.V(2).Infof("Auto-detected controller image from pod spec: %s", container.Image)
				return container.Image, nil
			}
		}
	}

	return "", fmt.Errorf("failed to auto-detect controller image: no container with 'gitopscluster' command found in pod %s/%s", podNamespace, podName)
}

// getAddOnTemplateName generates a unique AddOnTemplate name for the GitOpsCluster with argocd-agent
func getAddOnTemplateName(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) string {
	// Use namespace-name format for uniqueness
	return fmt.Sprintf("gitops-addon-%s-%s", gitOpsCluster.Namespace, gitOpsCluster.Name)
}

// LegacyStaticOLMAddOnTemplateName is the name of the static AddOnTemplate that was shipped
// in the ACM 2.16 installer manifests. It embedded the OLM Subscription directly in the
// ManifestWork and placed addon resources in open-cluster-management-agent-addon. The 2.17
// installer no longer ships this template, but it is never deleted by the installer upgrade
// process — the hub controller must remove it to stop OCM from continuing to apply the
// stale ManifestWork (which creates a duplicate OLM subscription).
const LegacyStaticOLMAddOnTemplateName = "gitops-addon-olm"

// getLegacyOLMAddOnTemplateName returns the name of the per-GitOpsCluster dynamic
// AddOnTemplate created by older hub controller versions when both gitopsAddon and
// olmSubscription were enabled. These templates embedded the OLM Subscription directly
// and are superseded by the runtime OLM path introduced in 2.17.
func getLegacyOLMAddOnTemplateName(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) string {
	return fmt.Sprintf("gitops-addon-olm-%s-%s", gitOpsCluster.Namespace, gitOpsCluster.Name)
}

// cleanupLegacyOLMAddOnTemplates removes both varieties of legacy OLM AddOnTemplate that
// may be left over after a 2.16→2.17 upgrade:
//
//   - LegacyStaticOLMAddOnTemplateName ("gitops-addon-olm") — shipped by the 2.16 installer
//     as a cluster-scoped manifest that embedded the OLM Subscription in the ManifestWork.
//
//   - getLegacyOLMAddOnTemplateName(…) ("gitops-addon-olm-{ns}-{name}") — created
//     dynamically by the old hub controller when argoCDAgent + olmSubscription were both
//     enabled.
//
// Callers must ensure the ManagedClusterAddOn no longer references these templates before
// calling this function (EnsureManagedClusterAddon handles that). Deletion is best-effort:
// errors are logged as warnings so that a transient API failure does not block reconciliation.
func (r *ReconcileGitOpsCluster) cleanupLegacyOLMAddOnTemplates(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) {
	if err := r.deleteAddOnTemplateByName(LegacyStaticOLMAddOnTemplateName); err != nil {
		klog.Warningf("Failed to delete static legacy OLM AddOnTemplate %s: %v", LegacyStaticOLMAddOnTemplateName, err)
	}
	dynamicLegacyName := getLegacyOLMAddOnTemplateName(gitOpsCluster)
	if err := r.deleteAddOnTemplateByName(dynamicLegacyName); err != nil {
		klog.Warningf("Failed to delete dynamic legacy OLM AddOnTemplate %s: %v", dynamicLegacyName, err)
	}
}

// EnsureAddOnTemplate creates or updates the AddOnTemplate for a GitOpsCluster with ArgoCD agent enabled
// This is the dynamic template for argocd-agent WITHOUT OLM subscription
func (r *ReconcileGitOpsCluster) EnsureAddOnTemplate(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	templateName := getAddOnTemplateName(gitOpsCluster)

	// Delete any legacy OLM-style AddOnTemplates that may have been left by an older version
	// of the controller. Both the static installer template ("gitops-addon-olm") and the
	// per-GitOpsCluster dynamic template ("gitops-addon-olm-{ns}-{name}") must be removed so
	// OCM stops applying the stale ManifestWork that embedded the OLM Subscription directly.
	r.cleanupLegacyOLMAddOnTemplates(gitOpsCluster)

	// Get the controller image - error out if not available
	addonImage, err := r.getControllerImage()
	if err != nil {
		klog.Errorf("Failed to get controller image: %v", err)
		return fmt.Errorf("cannot create addon template: %w", err)
	}

	// Build the AddOnTemplate for argocd-agent mode
	addonTemplate := &addonv1alpha1.AddOnTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: templateName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":                            "multicloud-integrations",
				"app.kubernetes.io/component":                             "addon-template",
				"gitopscluster.apps.open-cluster-management.io/name":      gitOpsCluster.Name,
				"gitopscluster.apps.open-cluster-management.io/namespace": gitOpsCluster.Namespace,
			},
		},
		Spec: addonv1alpha1.AddOnTemplateSpec{
			AddonName: "gitops-addon",
			AgentSpec: workv1.ManifestWorkSpec{
				Workload: workv1.ManifestsTemplate{
					Manifests: buildAddonManifests(gitOpsCluster.Namespace, addonImage),
				},
			},
			Registration: []addonv1alpha1.RegistrationSpec{
				{
					Type: addonv1alpha1.RegistrationTypeCustomSigner,
					CustomSigner: &addonv1alpha1.CustomSignerRegistrationConfig{
						SignerName: "open-cluster-management.io/argocd-agent-addon",
						// Note: The certificate CN will be the full OCM path like:
						// system:open-cluster-management:cluster:<cluster-name>:addon:gitops-addon:agent:gitops-addon-agent
						// The ArgoCD principal is configured with a custom auth regex to extract
						// just the cluster name from this full path.
					SigningCA: addonv1alpha1.SigningCARef{
						Name:      "argocd-agent-ca",
						Namespace: GetEffectiveArgoNamespace(gitOpsCluster),
					},
					},
				},
			},
		},
	}

	// Check if AddOnTemplate already exists
	existing := &addonv1alpha1.AddOnTemplate{}
	err = r.Get(context.Background(), types.NamespacedName{Name: templateName}, existing)
	if err == nil {
		// AddOnTemplate exists, update it
		klog.V(2).Infof("Updating AddOnTemplate %s", templateName)
		existing.Spec = addonTemplate.Spec
		existing.Labels = addonTemplate.Labels
		return r.Update(context.Background(), existing)
	}

	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to get AddOnTemplate: %w", err)
	}

	// Create new AddOnTemplate
	klog.Infof("Creating AddOnTemplate %s", templateName)
	return r.Create(context.Background(), addonTemplate)
}

// buildAddonManifests builds the manifest list for the AddOnTemplate (argocd-agent without OLM)
func buildAddonManifests(gitOpsNamespace, addonImage string) []workv1.Manifest {
	if gitOpsNamespace == "" {
		gitOpsNamespace = utils.GitOpsNamespace
	}

	manifests := []workv1.Manifest{
		// Pre-delete RBAC — included in the pre-delete ManifestWork so the cleanup Job
		// retains permissions even after the regular ManifestWork (which deploys the main
		// ClusterRole) is deleted during MCA teardown.
		buildCleanupClusterRoleManifest(),
		buildCleanupClusterRoleBindingManifest("open-cluster-management-agent-addon"),
		// Pre-delete cleanup job
		buildCleanupJobManifest(addonImage, "open-cluster-management-agent-addon"),
		// ServiceAccount
		buildServiceAccountManifest("open-cluster-management-agent-addon"),
		// ClusterRole (fine-grained permissions for the addon agent)
		buildClusterRoleManifest(),
		// ClusterRoleBinding
		buildClusterRoleBindingManifest("open-cluster-management-agent-addon"),
		// Deployment
		buildDeploymentManifest(addonImage, "open-cluster-management-agent-addon"),
	}

	return manifests
}

// buildCleanupClusterRoleManifest creates a ClusterRole for the pre-delete cleanup Job.
// This is annotated with addon-pre-delete so it's included in the pre-delete ManifestWork,
// ensuring the cleanup Job retains RBAC even after the regular ManifestWork (which deploys
// the main gitops-addon ClusterRole) is deleted during MCA teardown.
func buildCleanupClusterRoleManifest() workv1.Manifest {
	return newManifestWithoutStatus(&rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "gitops-addon-cleanup",
			Annotations: map[string]string{
				"addon.open-cluster-management.io/addon-pre-delete": "",
			},
			Labels: map[string]string{
				GitOpsAddonLabel: "true",
			},
		},
		Rules: cleanupClusterRoleRules(),
	})
}

// cleanupClusterRoleRules returns the minimal RBAC rules the cleanup Job needs.
func cleanupClusterRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"serviceaccounts"}, Verbs: []string{"get", "list", "watch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list", "watch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "list", "watch", "delete"}},
		{APIGroups: []string{"apps"}, Resources: []string{"statefulsets"}, Verbs: []string{"get", "list", "watch", "delete"}},
		{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles", "rolebindings"}, Verbs: []string{"get", "list", "delete"}},
		{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"clusterroles", "clusterrolebindings"}, Verbs: []string{"get", "list", "delete"}},
		{APIGroups: []string{"argoproj.io"}, Resources: []string{"argocds"}, Verbs: []string{"get", "list", "watch", "delete", "patch"}},
		{APIGroups: []string{"operators.coreos.com"}, Resources: []string{"subscriptions"}, Verbs: []string{"get", "list", "delete"}},
		{APIGroups: []string{"operators.coreos.com"}, Resources: []string{"clusterserviceversions"}, Verbs: []string{"get", "list", "delete"}},
		{APIGroups: []string{"operators.coreos.com"}, Resources: []string{"installplans"}, Verbs: []string{"get", "list", "delete"}},
		{APIGroups: []string{"pipelines.openshift.io"}, Resources: []string{"gitopsservices"}, Verbs: []string{"get", "delete"}},
	}
}

// buildCleanupClusterRoleBindingManifest creates a ClusterRoleBinding for the pre-delete
// cleanup Job, binding the gitops-addon SA to the gitops-addon-cleanup ClusterRole.
func buildCleanupClusterRoleBindingManifest(namespace string) workv1.Manifest {
	return newManifestWithoutStatus(&rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "gitops-addon-cleanup",
			Annotations: map[string]string{
				"addon.open-cluster-management.io/addon-pre-delete": "",
			},
			Labels: map[string]string{
				GitOpsAddonLabel: "true",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "gitops-addon-cleanup",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "gitops-addon",
				Namespace: namespace,
			},
		},
	})
}

// buildCleanupJobManifest creates the cleanup job manifest - consistent across all templates
func buildCleanupJobManifest(addonImage, namespace string) workv1.Manifest {
	return newManifestWithoutStatus(&batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitops-addon-cleanup",
			Namespace: namespace,
			Annotations: map[string]string{
				"addon.open-cluster-management.io/addon-pre-delete": "",
			},
			Labels: map[string]string{
				GitOpsAddonLabel: "true",
			},
		},
		Spec: batchv1.JobSpec{
			ManualSelector:        boolPtr(true),
			BackoffLimit:          int32Ptr(3),   // Retry up to 3 times on failure
			ActiveDeadlineSeconds: int64Ptr(600), // 10 minutes timeout
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"job-name": "gitops-addon-cleanup",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"job-name": "gitops-addon-cleanup",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "gitops-addon",
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "cleanup",
							Image:           addonImage,
							ImagePullPolicy: imagePullPolicyForImage(addonImage),
							Command:         []string{"/usr/local/bin/gitopsaddon"},
							Args:            []string{"-cleanup"},
							Env:             buildCleanupEnvVars(),
						},
					},
				},
			},
		},
	})
}

// buildCleanupEnvVars creates the environment variables for the cleanup Job container.
// The cleanup code reads OLM_SUBSCRIPTION_NAMESPACE (and other OLM vars) to locate
// subscriptions in custom namespaces. Without these, cleanup only checks the defaults.
func buildCleanupEnvVars() []corev1.EnvVar {
	vars := []string{
		"ARGOCD_NAMESPACE",
		"OLM_SUBSCRIPTION_ENABLED",
		"OLM_SUBSCRIPTION_NAME",
		"OLM_SUBSCRIPTION_NAMESPACE",
		"OLM_SUBSCRIPTION_CHANNEL",
		"OLM_SUBSCRIPTION_SOURCE",
		"OLM_SUBSCRIPTION_SOURCE_NAMESPACE",
		"OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL",
	}
	var envVars []corev1.EnvVar
	for _, v := range vars {
		envVars = append(envVars, corev1.EnvVar{
			Name:  v,
			Value: fmt.Sprintf("{{%s}}", v),
		})
	}
	return envVars
}

// buildServiceAccountManifest creates the service account manifest
func buildServiceAccountManifest(namespace string) workv1.Manifest {
	return newManifestWithoutStatus(&corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitops-addon",
			Namespace: namespace,
			Labels: map[string]string{
				GitOpsAddonLabel: "true",
			},
		},
		ImagePullSecrets: []corev1.LocalObjectReference{
			{Name: "open-cluster-management-image-pull-credentials"},
		},
	})
}

// buildClusterRoleManifest creates the fine-grained ClusterRole for the addon agent.
// This replaces the previous cluster-admin binding with least-privilege permissions.
func buildClusterRoleManifest() workv1.Manifest {
	return newManifestWithoutStatus(&rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "gitops-addon",
			Labels: map[string]string{
				GitOpsAddonLabel: "true",
			},
		},
		Rules: gitopsAddonClusterRoleRules(),
	})
}

// gitopsAddonClusterRoleRules returns the RBAC rules for the gitops-addon ClusterRole.
func gitopsAddonClusterRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		// Namespace management (create ArgoCD/operator namespaces, label them)
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
		// Secret management (image pull secrets, TLS certs, ArgoCD secrets)
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		// ServiceAccount management (patch imagePullSecrets on ArgoCD SAs; delete needed for cleanup; watch needed by controller-runtime cache)
		{APIGroups: []string{""}, Resources: []string{"serviceaccounts"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		// Pod management (delete pods with image pull errors; watch needed by controller-runtime cache)
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch", "delete"}},
		// ConfigMap management (operator config, pause marker; watch needed by controller-runtime cache)
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
		// Service management (operator metrics/webhook services via Helm chart; delete needed for cleanup)
		{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		// Event recording (leader election events)
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
		// Deployment management (operator deployment, addon deployment status)
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		// Deployment finalizers (needed for blockOwnerDeletion in pause marker ownerReference)
		{APIGroups: []string{"apps"}, Resources: []string{"deployments/finalizers"}, Verbs: []string{"update"}},
		// RBAC management (operator roles/bindings created by Helm chart, cleanup)
		{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles", "rolebindings"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		// "escalate" and "bind" allow the addon to create ClusterRoles that grant
		// permissions the addon itself does not hold (needed for the ArgoCD operator's RBAC)
		{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"clusterroles", "clusterrolebindings"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete", "escalate", "bind"}},
		// CRD management (install ArgoCD CRDs, Route CRD stub, patch conversion webhook; watch needed by controller-runtime cache)
		{APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}},
		// ArgoCD CR management (cleanup deletes addon-created ArgoCD CRs)
		{APIGroups: []string{"argoproj.io"}, Resources: []string{"argocds"}, Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
		// OLM subscription management (OCP clusters use OLM for operator install)
		{APIGroups: []string{"operators.coreos.com"}, Resources: []string{"subscriptions"}, Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
		{APIGroups: []string{"operators.coreos.com"}, Resources: []string{"clusterserviceversions"}, Verbs: []string{"get", "list", "delete"}},
		{APIGroups: []string{"operators.coreos.com"}, Resources: []string{"operatorgroups"}, Verbs: []string{"get", "list", "create"}},
		// GitOpsService CR cleanup (OLM mode)
		{APIGroups: []string{"pipelines.openshift.io"}, Resources: []string{"gitopsservices"}, Verbs: []string{"get", "delete"}},
		// OCM cluster detection (is this OCP? is this a hub?)
		{APIGroups: []string{"cluster.open-cluster-management.io"}, Resources: []string{"managedclusters"}, Verbs: []string{"list"}},
		{APIGroups: []string{"cluster.open-cluster-management.io"}, Resources: []string{"clusterclaims"}, Verbs: []string{"get"}},
		{APIGroups: []string{"operator.open-cluster-management.io"}, Resources: []string{"clustermanagers"}, Verbs: []string{"list"}},
		// Leader election
		{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "list", "watch", "create", "update"}},
	}
}

// buildClusterRoleBindingManifest creates the cluster role binding manifest
func buildClusterRoleBindingManifest(namespace string) workv1.Manifest {
	return newManifestWithoutStatus(&rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "gitops-addon",
			Labels: map[string]string{
				GitOpsAddonLabel: "true",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "gitops-addon",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "gitops-addon",
				Namespace: namespace,
			},
		},
	})
}

// buildDeploymentManifest creates the deployment manifest for the gitops-addon
func buildDeploymentManifest(addonImage, namespace string) workv1.Manifest {
	return newManifestWithoutStatus(&appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitops-addon",
			Namespace: namespace,
			Labels: map[string]string{
				"app":            "gitops-addon",
				GitOpsAddonLabel: "true",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "gitops-addon",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "gitops-addon",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "gitops-addon",
					Containers: []corev1.Container{
						{
							Name:            "gitops-addon",
							Image:           addonImage,
							ImagePullPolicy: imagePullPolicyForImage(addonImage),
							Command:         []string{"/usr/local/bin/gitopsaddon"},
							Env:             buildAddonEnvVars(),
							SecurityContext: &corev1.SecurityContext{
								ReadOnlyRootFilesystem:   boolPtr(true),
								AllowPrivilegeEscalation: boolPtr(false),
								RunAsNonRoot:             boolPtr(true),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/tmp",
									Name:      "tmp-volume",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "tmp-volume",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	})
}

// buildAddonEnvVars creates the environment variables for the gitops-addon container.
// Each variable uses a template placeholder (e.g., {{GITOPS_OPERATOR_IMAGE}}) that gets
// substituted by the addon framework using values from AddOnDeploymentConfig.
func buildAddonEnvVars() []corev1.EnvVar {
	envVars := []corev1.EnvVar{}

	// Add POD_NAMESPACE using downward API - needed by secret controller to discover source namespace
	envVars = append(envVars, corev1.EnvVar{
		Name: "POD_NAMESPACE",
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: "metadata.namespace",
			},
		},
	})

	// Add all image environment variables (excluding hub-only vars like ARGOCD_PRINCIPAL_IMAGE)
	for envKey := range utils.DefaultOperatorImages {
		if utils.IsHubOnlyEnvVar(envKey) {
			continue
		}
		envVars = append(envVars, corev1.EnvVar{
			Name:  envKey,
			Value: fmt.Sprintf("{{%s}}", envKey),
		})
	}

	// Add proxy environment variables
	envVars = append(envVars,
		corev1.EnvVar{Name: utils.EnvHTTPProxy, Value: fmt.Sprintf("{{%s}}", utils.EnvHTTPProxy)},
		corev1.EnvVar{Name: utils.EnvHTTPSProxy, Value: fmt.Sprintf("{{%s}}", utils.EnvHTTPSProxy)},
		corev1.EnvVar{Name: utils.EnvNoProxy, Value: fmt.Sprintf("{{%s}}", utils.EnvNoProxy)},
	)

	// Add ArgoCD agent environment variables
	envVars = append(envVars,
		corev1.EnvVar{Name: utils.EnvArgoCDAgentEnabled, Value: fmt.Sprintf("{{%s}}", utils.EnvArgoCDAgentEnabled)},
		corev1.EnvVar{Name: utils.EnvArgoCDAgentServerAddress, Value: fmt.Sprintf("{{%s}}", utils.EnvArgoCDAgentServerAddress)},
		corev1.EnvVar{Name: utils.EnvArgoCDAgentServerPort, Value: fmt.Sprintf("{{%s}}", utils.EnvArgoCDAgentServerPort)},
		corev1.EnvVar{Name: utils.EnvArgoCDAgentMode, Value: fmt.Sprintf("{{%s}}", utils.EnvArgoCDAgentMode)},
	)

	// Add ARGOCD_NAMESPACE - tells the addon agent which namespace the ArgoCD CR lives in
	envVars = append(envVars, corev1.EnvVar{
		Name:  "ARGOCD_NAMESPACE",
		Value: "{{ARGOCD_NAMESPACE}}",
	})

	// Add OLM subscription environment variables — these pass custom subscription
	// configuration from the hub to the addon agent for OCP clusters.
	olmVars := []string{
		"OLM_SUBSCRIPTION_ENABLED",
		"OLM_SUBSCRIPTION_NAME",
		"OLM_SUBSCRIPTION_NAMESPACE",
		"OLM_SUBSCRIPTION_CHANNEL",
		"OLM_SUBSCRIPTION_SOURCE",
		"OLM_SUBSCRIPTION_SOURCE_NAMESPACE",
		"OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL",
	}
	for _, v := range olmVars {
		envVars = append(envVars, corev1.EnvVar{
			Name:  v,
			Value: fmt.Sprintf("{{%s}}", v),
		})
	}

	return envVars
}

// CleanupDynamicAddOnTemplates deletes all dynamic AddOnTemplates created for a GitOpsCluster.
// This should be called when a GitOpsCluster is deleted.
func (r *ReconcileGitOpsCluster) CleanupDynamicAddOnTemplates(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) {
	// Delete the current argocd-agent template
	templateName := getAddOnTemplateName(gitOpsCluster)
	if err := r.deleteAddOnTemplateByName(templateName); err != nil {
		klog.Warningf("Failed to delete AddOnTemplate %s: %v", templateName, err)
	}

	// Delete legacy OLM templates (static installer template + per-GitOpsCluster dynamic template)
	r.cleanupLegacyOLMAddOnTemplates(gitOpsCluster)
}

// deleteAddOnTemplateByName deletes an AddOnTemplate by name (best effort, doesn't wait)
func (r *ReconcileGitOpsCluster) deleteAddOnTemplateByName(name string) error {
	template := &addonv1alpha1.AddOnTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	err := r.Delete(context.Background(), template)
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	if err == nil {
		klog.Infof("Deleted AddOnTemplate %s", name)
	}

	return nil
}

// Helper functions
func int32Ptr(i int32) *int32 {
	return &i
}

func int64Ptr(i int64) *int64 {
	return &i
}

func boolPtr(b bool) *bool {
	return &b
}

// newManifestWithoutStatus creates a workv1.Manifest from a runtime.Object while stripping out the status field
func newManifestWithoutStatus(obj runtime.Object) workv1.Manifest {
	// Marshal the object to JSON
	jsonBytes, err := json.Marshal(obj)
	if err != nil {
		klog.Errorf("Failed to marshal object: %v", err)
		return workv1.Manifest{
			RawExtension: runtime.RawExtension{
				Object: obj,
			},
		}
	}

	// Unmarshal to a map to manipulate the JSON
	var objMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &objMap); err != nil {
		klog.Errorf("Failed to unmarshal object: %v", err)
		return workv1.Manifest{
			RawExtension: runtime.RawExtension{
				Object: obj,
			},
		}
	}

	// Remove the status field if it exists
	delete(objMap, "status")

	// Marshal back to JSON
	cleanedBytes, err := json.Marshal(objMap)
	if err != nil {
		klog.Errorf("Failed to marshal cleaned object: %v", err)
		return workv1.Manifest{
			RawExtension: runtime.RawExtension{
				Object: obj,
			},
		}
	}

	// Return manifest with Raw bytes instead of Object
	return workv1.Manifest{
		RawExtension: runtime.RawExtension{
			Raw: cleanedBytes,
		},
	}
}
