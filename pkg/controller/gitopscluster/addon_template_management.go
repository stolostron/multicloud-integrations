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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// getAddOnTemplateOLMName generates a unique AddOnTemplate name for argocd-agent + OLM mode
func getAddOnTemplateOLMName(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) string {
	return fmt.Sprintf("gitops-addon-olm-%s-%s", gitOpsCluster.Namespace, gitOpsCluster.Name)
}

// EnsureAddOnTemplate creates or updates the AddOnTemplate for a GitOpsCluster with ArgoCD agent enabled
// This is the dynamic template for argocd-agent WITHOUT OLM subscription
func (r *ReconcileGitOpsCluster) EnsureAddOnTemplate(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	templateName := getAddOnTemplateName(gitOpsCluster)

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
							Namespace: gitOpsCluster.Namespace,
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

// EnsureAddOnTemplateOLM creates or updates the AddOnTemplate for argocd-agent + OLM subscription mode
func (r *ReconcileGitOpsCluster) EnsureAddOnTemplateOLM(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	templateName := getAddOnTemplateOLMName(gitOpsCluster)

	// Get the controller image - error out if not available
	addonImage, err := r.getControllerImage()
	if err != nil {
		klog.Errorf("Failed to get controller image: %v", err)
		return fmt.Errorf("cannot create addon template: %w", err)
	}

	// Get OLM subscription values with defaults
	subName, subNamespace, channel, source, sourceNamespace, installPlanApproval := GetOLMSubscriptionValues(gitOpsCluster.Spec.GitOpsAddon.OLMSubscription)

	// Build the AddOnTemplate for argocd-agent + OLM mode
	addonTemplate := &addonv1alpha1.AddOnTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: templateName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":                            "multicloud-integrations",
				"app.kubernetes.io/component":                             "addon-template-olm-agent",
				"gitopscluster.apps.open-cluster-management.io/name":      gitOpsCluster.Name,
				"gitopscluster.apps.open-cluster-management.io/namespace": gitOpsCluster.Namespace,
			},
		},
		Spec: addonv1alpha1.AddOnTemplateSpec{
			AddonName: "gitops-addon",
			AgentSpec: workv1.ManifestWorkSpec{
				Workload: workv1.ManifestsTemplate{
					Manifests: buildOLMAgentManifests(gitOpsCluster.Namespace, addonImage, subName, subNamespace, channel, source, sourceNamespace, installPlanApproval),
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
							Namespace: gitOpsCluster.Namespace,
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
		klog.V(2).Infof("Updating OLM Agent AddOnTemplate %s", templateName)
		existing.Spec = addonTemplate.Spec
		existing.Labels = addonTemplate.Labels
		return r.Update(context.Background(), existing)
	}

	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to get OLM Agent AddOnTemplate: %w", err)
	}

	// Create new AddOnTemplate
	klog.Infof("Creating OLM Agent AddOnTemplate %s", templateName)
	return r.Create(context.Background(), addonTemplate)
}

// buildAddonManifests builds the manifest list for the AddOnTemplate (argocd-agent without OLM)
func buildAddonManifests(gitOpsNamespace, addonImage string) []workv1.Manifest {
	if gitOpsNamespace == "" {
		gitOpsNamespace = utils.GitOpsNamespace
	}

	manifests := []workv1.Manifest{
		// Pre-delete cleanup job - consistent across all templates, no env vars needed
		buildCleanupJobManifest(addonImage, "open-cluster-management-agent-addon"),
		// ServiceAccount
		buildServiceAccountManifest("open-cluster-management-agent-addon"),
		// ClusterRoleBinding
		buildClusterRoleBindingManifest("open-cluster-management-agent-addon"),
		// Deployment
		buildDeploymentManifest(addonImage, "open-cluster-management-agent-addon"),
	}

	return manifests
}

// buildOLMAgentManifests builds the manifest list for argocd-agent + OLM AddOnTemplate
func buildOLMAgentManifests(gitOpsNamespace, addonImage, subName, subNamespace, channel, source, sourceNamespace, installPlanApproval string) []workv1.Manifest {
	if gitOpsNamespace == "" {
		gitOpsNamespace = utils.GitOpsNamespace
	}

	manifests := []workv1.Manifest{
		// Pre-delete cleanup job - consistent across all templates, no env vars needed
		buildCleanupJobManifest(addonImage, subNamespace),
		// OLM Subscription
		buildOLMSubscriptionManifest(subName, subNamespace, channel, source, sourceNamespace, installPlanApproval),
		// ServiceAccount for cleanup job and gitops-addon deployment
		buildServiceAccountManifest(subNamespace),
		// ClusterRoleBinding
		buildClusterRoleBindingManifest(subNamespace),
		// Deployment - needed to run the secret controller that copies argocd-agent-client-tls cert
		// from open-cluster-management-agent-addon namespace to openshift-gitops namespace
		buildDeploymentManifest(addonImage, subNamespace),
	}

	return manifests
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
			BackoffLimit:          int32Ptr(3),   // Retry up to 3 times on failure
			ActiveDeadlineSeconds: int64Ptr(600), // 10 minutes timeout
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "gitops-addon",
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "cleanup",
							Image:           addonImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"/usr/local/bin/gitopsaddon"},
							Args:            []string{"-cleanup"},
							// No env vars needed - cleanup uses label-based lookup
						},
					},
				},
			},
		},
	})
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
			Name:     "cluster-admin",
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
							ImagePullPolicy: corev1.PullIfNotPresent,
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

// buildOLMSubscriptionManifest creates the OLM Subscription manifest
func buildOLMSubscriptionManifest(name, namespace, channel, source, sourceNamespace, installPlanApproval string) workv1.Manifest {
	subscription := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "multicloud-integrations",
					GitOpsAddonLabel:               "true",
				},
			},
			"spec": map[string]interface{}{
				"channel":             channel,
				"name":                name,
				"source":              source,
				"sourceNamespace":     sourceNamespace,
				"installPlanApproval": installPlanApproval,
				"config": map[string]interface{}{
					"env": []map[string]interface{}{
						{
							"name":  "DISABLE_DEFAULT_ARGOCD_INSTANCE",
							"value": "true",
						},
					},
				},
			},
		},
	}

	jsonBytes, err := json.Marshal(subscription.Object)
	if err != nil {
		klog.Errorf("Failed to marshal subscription: %v", err)
		return workv1.Manifest{
			RawExtension: runtime.RawExtension{
				Object: subscription,
			},
		}
	}

	return workv1.Manifest{
		RawExtension: runtime.RawExtension{
			Raw: jsonBytes,
		},
	}
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

	return envVars
}

// CleanupDynamicAddOnTemplates deletes all dynamic AddOnTemplates created for a GitOpsCluster
// This should be called when a GitOpsCluster is deleted
func (r *ReconcileGitOpsCluster) CleanupDynamicAddOnTemplates(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) {
	// Try to delete the argocd-agent template
	templateName := getAddOnTemplateName(gitOpsCluster)
	if err := r.deleteAddOnTemplateByName(templateName); err != nil {
		klog.Warningf("Failed to delete AddOnTemplate %s: %v", templateName, err)
	}

	// Try to delete the argocd-agent + OLM template
	olmTemplateName := getAddOnTemplateOLMName(gitOpsCluster)
	if err := r.deleteAddOnTemplateByName(olmTemplateName); err != nil {
		klog.Warningf("Failed to delete OLM AddOnTemplate %s: %v", olmTemplateName, err)
	}

	// Also try to delete any OLM-only templates (non-agent)
	olmOnlyTemplateName := getOLMAddOnTemplateName(gitOpsCluster)
	if err := r.deleteAddOnTemplateByName(olmOnlyTemplateName); err != nil {
		klog.Warningf("Failed to delete OLM-only AddOnTemplate %s: %v", olmOnlyTemplateName, err)
	}
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
