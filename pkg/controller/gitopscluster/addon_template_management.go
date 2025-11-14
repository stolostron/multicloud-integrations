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
)

const (
	// DefaultAddonImage is the default image to use if controller image cannot be determined
	DefaultAddonImage = "quay.io/stolostron/multicloud-integrations"

	// DefaultArgoCDAgentImage is the default ArgoCD agent image
	// This should match the default in cmd/gitopsaddon/main.go
	DefaultArgoCDAgentImage = "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel8@sha256:d17069d475959a5fca31dc4cd2c2dce4f3d895f2c2b97906261791674a889079"

	// ControllerImageEnvVar is the environment variable name for the controller image
	ControllerImageEnvVar = "CONTROLLER_IMAGE"
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

// getAddOnTemplateName generates a unique AddOnTemplate name for the GitOpsCluster
func getAddOnTemplateName(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) string {
	// Use namespace-name format for uniqueness
	return fmt.Sprintf("gitops-addon-%s-%s", gitOpsCluster.Namespace, gitOpsCluster.Name)
}

// EnsureAddOnTemplate creates or updates the AddOnTemplate for a GitOpsCluster
func (r *ReconcileGitOpsCluster) EnsureAddOnTemplate(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) error {
	templateName := getAddOnTemplateName(gitOpsCluster)

	// Get the controller image - error out if not available
	addonImage, err := r.getControllerImage()
	if err != nil {
		klog.Errorf("Failed to get controller image: %v", err)
		return fmt.Errorf("cannot create addon template: %w", err)
	}

	// Note: operatorImage and agentImage are configured via AddOnDeploymentConfig environment variables,
	// not hardcoded in the template. The addon framework substitutes template placeholders at deployment time.

	// Build the AddOnTemplate
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

// buildAddonManifests builds the manifest list for the AddOnTemplate
// Note: Environment variables in the deployment use template placeholders (e.g., {{GITOPS_OPERATOR_IMAGE}})
// which are substituted by the addon framework using values from AddOnDeploymentConfig
func buildAddonManifests(gitOpsNamespace, addonImage string) []workv1.Manifest {
	if gitOpsNamespace == "" {
		gitOpsNamespace = "openshift-gitops"
	}

	manifests := []workv1.Manifest{
		// Pre-delete cleanup job
		newManifestWithoutStatus(&batchv1.Job{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "batch/v1",
				Kind:       "Job",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-cleanup",
				Namespace: "open-cluster-management-agent-addon",
				Annotations: map[string]string{
					"addon.open-cluster-management.io/addon-pre-delete": "",
				},
			},
			Spec: batchv1.JobSpec{
				BackoffLimit:          int32Ptr(3),   // Retry up to 3 times on failure
				ActiveDeadlineSeconds: int64Ptr(600), // 10 minutes timeout (increased for cleanup operations)
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
								Env: []corev1.EnvVar{
									{
										Name:  "GITOPS_OPERATOR_NAMESPACE",
										Value: "{{GITOPS_OPERATOR_NAMESPACE}}",
									},
									{
										Name:  "GITOPS_NAMESPACE",
										Value: "{{GITOPS_NAMESPACE}}",
									},
								},
							},
						},
					},
				},
			},
		}),
		// ServiceAccount
		newManifestWithoutStatus(&corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "open-cluster-management-agent-addon",
			},
		}),
		// ClusterRoleBinding
		newManifestWithoutStatus(&rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "gitops-addon",
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
					Namespace: "open-cluster-management-agent-addon",
				},
			},
		}),
		// Deployment
		newManifestWithoutStatus(&appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: "open-cluster-management-agent-addon",
				Labels: map[string]string{
					"app": "gitops-addon",
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
								Env: []corev1.EnvVar{
									{
										Name:  "GITOPS_OPERATOR_IMAGE",
										Value: "{{GITOPS_OPERATOR_IMAGE}}",
									},
									{
										Name:  "GITOPS_OPERATOR_NAMESPACE",
										Value: "{{GITOPS_OPERATOR_NAMESPACE}}",
									},
									{
										Name:  "GITOPS_IMAGE",
										Value: "{{GITOPS_IMAGE}}",
									},
									{
										Name:  "GITOPS_NAMESPACE",
										Value: "{{GITOPS_NAMESPACE}}",
									},
									{
										Name:  "REDIS_IMAGE",
										Value: "{{REDIS_IMAGE}}",
									},
									{
										Name:  "RECONCILE_SCOPE",
										Value: "{{RECONCILE_SCOPE}}",
									},
									{
										Name:  "ARGOCD_AGENT_ENABLED",
										Value: "{{ARGOCD_AGENT_ENABLED}}",
									},
									{
										Name:  "ARGOCD_AGENT_IMAGE",
										Value: "{{ARGOCD_AGENT_IMAGE}}",
									},
									{
										Name:  "ARGOCD_AGENT_SERVER_ADDRESS",
										Value: "{{ARGOCD_AGENT_SERVER_ADDRESS}}",
									},
									{
										Name:  "ARGOCD_AGENT_SERVER_PORT",
										Value: "{{ARGOCD_AGENT_SERVER_PORT}}",
									},
									{
										Name:  "ARGOCD_AGENT_MODE",
										Value: "{{ARGOCD_AGENT_MODE}}",
									},
								},
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
		}),
	}

	return manifests
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
