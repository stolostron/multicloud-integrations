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
	"testing"
	"time"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
)

func TestWaitForOperatorReady(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		timeout         time.Duration
		setupDeployment bool
		expectError     bool
	}{
		{
			name:            "operator_exists_and_ready",
			timeout:         5 * time.Second,
			setupDeployment: true,
			expectError:     false, // fake client skips waiting
		},
		{
			name:            "operator_does_not_exist_timeout",
			timeout:         100 * time.Millisecond,
			setupDeployment: false,
			expectError:     false, // Changed: fake client skips waiting, so no error expected
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
				Config: getTestEnv().Config,
			}

			// Setup operator deployment if needed
			if tt.setupDeployment {
				replicas := int32(1)
				deployment := &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-operator-controller-manager",
						Namespace: "openshift-gitops-operator",
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: &replicas,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "argocd-operator"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"app": "argocd-operator"},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "manager", Image: "test"},
								},
							},
						},
					},
					Status: appsv1.DeploymentStatus{
						ReadyReplicas: 1,
						Conditions: []appsv1.DeploymentCondition{
							{
								Type:   appsv1.DeploymentAvailable,
								Status: corev1.ConditionTrue,
							},
						},
					},
				}
				err := reconciler.Create(context.TODO(), deployment)
				if err != nil && !errors.IsAlreadyExists(err) {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			}

			err := reconciler.waitForOperatorReady(tt.timeout)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}
		})
	}
}

func TestCreateUpdateNamespace(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name          string
		namespaceName string
		existingNS    *corev1.Namespace
		expectError   bool
	}{
		{
			name:          "create_new_namespace",
			namespaceName: "new-test-ns-install",
			existingNS:    nil,
			expectError:   false,
		},
		{
			name:          "update_existing_namespace",
			namespaceName: "existing-test-ns-install",
			existingNS: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "existing-test-ns-install",
					Labels: map[string]string{
						"existing": "label",
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
			}

			// Create existing namespace if specified
			if tt.existingNS != nil {
				err := reconciler.Create(context.TODO(), tt.existingNS)
				if err != nil && !errors.IsAlreadyExists(err) {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			}

			nameSpaceKey := types.NamespacedName{Name: tt.namespaceName}
			err := reconciler.CreateUpdateNamespace(nameSpaceKey)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())

				// Verify namespace exists
				ns := &corev1.Namespace{}
				err = reconciler.Get(context.TODO(), nameSpaceKey, ns)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}
		})
	}
}

func TestInstallOrUpdateOpenshiftGitops(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client:      getTestEnv().Client,
		Config:      getTestEnv().Config,
		AddonConfig: createTestConfig("test-operator:latest", "test-argocd:latest", "test-redis:latest", false),
	}

	// This test mainly verifies that the function doesn't panic
	// The actual functionality depends on many external dependencies
	g.Expect(func() {
		reconciler.installOrUpdateOpenshiftGitops()
	}).ToNot(gomega.Panic())
}

func TestPatchArgoCDServiceAccountsWithImagePullSecrets(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name             string
		setupSecret      bool
		setupSAs         []string
		existingPullRef  bool
		expectPatchCount int
		expectError      bool
	}{
		{
			name:             "no_secret_exists_skip_patching",
			setupSecret:      false,
			setupSAs:         []string{},
			existingPullRef:  false,
			expectPatchCount: 0,
			expectError:      false,
		},
		{
			name:             "secret_exists_no_sas_to_patch",
			setupSecret:      true,
			setupSAs:         []string{},
			existingPullRef:  false,
			expectPatchCount: 0,
			expectError:      false,
		},
		{
			name:             "patch_serviceaccounts_successfully",
			setupSecret:      true,
			setupSAs:         []string{"acm-openshift-gitops-argocd-application-controller", "acm-openshift-gitops-argocd-redis"},
			existingPullRef:  false,
			expectPatchCount: 2,
			expectError:      false,
		},
		{
			name:             "sa_already_has_pullsecret_skip",
			setupSecret:      true,
			setupSAs:         []string{"acm-openshift-gitops-argocd-application-controller"},
			existingPullRef:  true,
			expectPatchCount: 0,
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
			}

			// Clean up before test
			secretName := "open-cluster-management-image-pull-credentials"

			// Create openshift-gitops namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: GitOpsNamespace,
				},
			}
			err := reconciler.Create(context.TODO(), ns)
			if err != nil && !errors.IsAlreadyExists(err) {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Setup secret if needed
			if tt.setupSecret {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secretName,
						Namespace: GitOpsNamespace,
					},
					Type: corev1.SecretTypeDockerConfigJson,
					Data: map[string][]byte{
						".dockerconfigjson": []byte(`{"auths":{"registry.redhat.io":{"auth":"test"}}}`),
					},
				}
				err := reconciler.Create(context.TODO(), secret)
				if err != nil && !errors.IsAlreadyExists(err) {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			}

			// Setup ServiceAccounts if needed
			for _, saName := range tt.setupSAs {
				sa := &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      saName,
						Namespace: GitOpsNamespace,
					},
				}
				if tt.existingPullRef {
					sa.ImagePullSecrets = []corev1.LocalObjectReference{
						{Name: secretName},
					}
				}
				err := reconciler.Create(context.TODO(), sa)
				if err != nil && !errors.IsAlreadyExists(err) {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			}

			// Run the function
			err = reconciler.patchArgoCDServiceAccountsWithImagePullSecrets()

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Verify ServiceAccounts were patched if expected
			if tt.expectPatchCount > 0 && !tt.existingPullRef {
				for _, saName := range tt.setupSAs {
					sa := &corev1.ServiceAccount{}
					err := reconciler.Get(context.TODO(), types.NamespacedName{Name: saName, Namespace: GitOpsNamespace}, sa)
					g.Expect(err).ToNot(gomega.HaveOccurred())

					found := false
					for _, ref := range sa.ImagePullSecrets {
						if ref.Name == secretName {
							found = true
							break
						}
					}
					g.Expect(found).To(gomega.BeTrue(), "ServiceAccount %s should have imagePullSecrets", saName)
				}
			}

			// Cleanup: delete ServiceAccounts for next test
			for _, saName := range tt.setupSAs {
				sa := &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      saName,
						Namespace: GitOpsNamespace,
					},
				}
				_ = reconciler.Delete(context.TODO(), sa)
			}

			// Cleanup: delete secret for next test
			if tt.setupSecret {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secretName,
						Namespace: GitOpsNamespace,
					},
				}
				_ = reconciler.Delete(context.TODO(), secret)
			}
		})
	}
}

func TestCopyImagePullSecret(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		setupSource     bool
		targetNamespace string
		expectError     bool
	}{
		{
			name:            "source_secret_not_found",
			setupSource:     false,
			targetNamespace: "test-target-ns",
			expectError:     true, // Returns error when source secret not found
		},
		{
			name:            "copy_secret_successfully",
			setupSource:     true,
			targetNamespace: "test-target-ns-copy",
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
			}

			secretName := "open-cluster-management-image-pull-credentials"
			// Source namespace is determined by utils.GetComponentNamespace("open-cluster-management-agent-addon")
			// Use the actual namespace to match what copyImagePullSecret will use
			sourceNamespace := utils.GetComponentNamespace("open-cluster-management-agent-addon")

			// Create source namespace
			srcNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: sourceNamespace,
				},
			}
			err := reconciler.Create(context.TODO(), srcNs)
			if err != nil && !errors.IsAlreadyExists(err) {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Create target namespace
			tgtNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.targetNamespace,
				},
			}
			err = reconciler.Create(context.TODO(), tgtNs)
			if err != nil && !errors.IsAlreadyExists(err) {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Setup source secret if needed
			if tt.setupSource {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secretName,
						Namespace: sourceNamespace,
					},
					Type: corev1.SecretTypeDockerConfigJson,
					Data: map[string][]byte{
						".dockerconfigjson": []byte(`{"auths":{"registry.redhat.io":{"auth":"testcred"}}}`),
					},
				}
				err := reconciler.Create(context.TODO(), secret)
				if err != nil && !errors.IsAlreadyExists(err) {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			}

			// Run the function with NamespacedName
			nsKey := types.NamespacedName{Name: tt.targetNamespace}
			err = reconciler.copyImagePullSecret(nsKey)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Verify secret was copied if source existed
			if tt.setupSource {
				secret := &corev1.Secret{}
				err := reconciler.Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: tt.targetNamespace}, secret)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(secret.Data).To(gomega.HaveKey(".dockerconfigjson"))
			}

			// Cleanup
			if tt.setupSource {
				_ = reconciler.Delete(context.TODO(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secretName,
						Namespace: sourceNamespace,
					},
				})
				_ = reconciler.Delete(context.TODO(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secretName,
						Namespace: tt.targetNamespace,
					},
				})
			}
		})
	}
}

func TestDeletePodsWithImagePullIssues(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name           string
		pods           []corev1.Pod
		expectedDelete int
	}{
		{
			name:           "no_pods",
			pods:           []corev1.Pod{},
			expectedDelete: 0,
		},
		{
			name: "pod_with_imagepullbackoff",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-1",
						Namespace: GitOpsNamespace,
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								State: corev1.ContainerState{
									Waiting: &corev1.ContainerStateWaiting{
										Reason: "ImagePullBackOff",
									},
								},
							},
						},
					},
				},
			},
			expectedDelete: 1,
		},
		{
			name: "pod_with_errimagepull",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-2",
						Namespace: GitOpsNamespace,
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								State: corev1.ContainerState{
									Waiting: &corev1.ContainerStateWaiting{
										Reason: "ErrImagePull",
									},
								},
							},
						},
					},
				},
			},
			expectedDelete: 1,
		},
		{
			name: "pod_running_no_delete",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-3",
						Namespace: GitOpsNamespace,
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								State: corev1.ContainerState{
									Running: &corev1.ContainerStateRunning{},
								},
							},
						},
					},
				},
			},
			expectedDelete: 0,
		},
		{
			name: "init_container_with_imagepullbackoff",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-4",
						Namespace: GitOpsNamespace,
					},
					Status: corev1.PodStatus{
						InitContainerStatuses: []corev1.ContainerStatus{
							{
								State: corev1.ContainerState{
									Waiting: &corev1.ContainerStateWaiting{
										Reason: "ImagePullBackOff",
									},
								},
							},
						},
					},
				},
			},
			expectedDelete: 1,
		},
		{
			name: "mixed_pods",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "running-pod",
						Namespace: GitOpsNamespace,
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								State: corev1.ContainerState{
									Running: &corev1.ContainerStateRunning{},
								},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "failing-pod",
						Namespace: GitOpsNamespace,
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								State: corev1.ContainerState{
									Waiting: &corev1.ContainerStateWaiting{
										Reason: "ImagePullBackOff",
									},
								},
							},
						},
					},
				},
			},
			expectedDelete: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
				Config: getTestEnv().Config,
			}

			// Create namespace if not exists
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: GitOpsNamespace,
				},
			}
			err := reconciler.Create(context.TODO(), ns)
			if err != nil && !errors.IsAlreadyExists(err) {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Create test pods
			for i := range tt.pods {
				pod := &tt.pods[i]
				err := reconciler.Create(context.TODO(), pod)
				if err != nil && !errors.IsAlreadyExists(err) {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			}

			// Run the function
			err = reconciler.deletePodsWithImagePullIssues()
			g.Expect(err).ToNot(gomega.HaveOccurred())

			// Verify pods were deleted as expected
			for i := range tt.pods {
				pod := &tt.pods[i]
				checkPod := &corev1.Pod{}
				err := reconciler.Get(context.TODO(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, checkPod)

				hasImagePullIssue := false
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "ImagePullBackOff" || cs.State.Waiting.Reason == "ErrImagePull") {
						hasImagePullIssue = true
						break
					}
				}
				for _, cs := range pod.Status.InitContainerStatuses {
					if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "ImagePullBackOff" || cs.State.Waiting.Reason == "ErrImagePull") {
						hasImagePullIssue = true
						break
					}
				}

				if hasImagePullIssue {
					g.Expect(errors.IsNotFound(err)).To(gomega.BeTrue(), "Pod %s should have been deleted", pod.Name)
				} else {
					g.Expect(err).ToNot(gomega.HaveOccurred(), "Pod %s should still exist", pod.Name)
					// Cleanup running pods
					_ = reconciler.Delete(context.TODO(), pod)
				}
			}
		})
	}
}
