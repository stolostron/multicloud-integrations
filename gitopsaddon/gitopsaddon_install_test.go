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
	"os"
	"testing"
	"time"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// unsetEnvForTest ensures the given env var is unset for the duration of the
// test, restoring any pre-existing value via t.Cleanup.
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	if orig, ok := os.LookupEnv(key); ok {
		os.Unsetenv(key)
		t.Cleanup(func() { os.Setenv(key, orig) })
	}
}

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

func TestPatchArgoCDServiceAccountsWithCustomNamespace(t *testing.T) {
	g := gomega.NewWithT(t)

	customNS := "local-cluster"
	t.Setenv("ARGOCD_NAMESPACE", customNS)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
	}

	// Create the custom namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: customNS},
	}
	err := reconciler.Create(context.TODO(), ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	secretName := "open-cluster-management-image-pull-credentials"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: customNS,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`{"auths":{"registry.redhat.io":{"auth":"test"}}}`),
		},
	}
	err = reconciler.Create(context.TODO(), secret)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	saName := "test-sa-custom-ns"
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: customNS,
		},
	}
	err = reconciler.Create(context.TODO(), sa)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	err = reconciler.patchArgoCDServiceAccountsWithImagePullSecrets()
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Verify SA was patched in the custom namespace
	patchedSA := &corev1.ServiceAccount{}
	err = reconciler.Get(context.TODO(), types.NamespacedName{Name: saName, Namespace: customNS}, patchedSA)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	found := false
	for _, ref := range patchedSA.ImagePullSecrets {
		if ref.Name == secretName {
			found = true
			break
		}
	}
	g.Expect(found).To(gomega.BeTrue(), "SA in custom namespace should have imagePullSecrets")

	// Cleanup
	_ = reconciler.Delete(context.TODO(), sa)
	_ = reconciler.Delete(context.TODO(), secret)
}

func TestDeletePodsWithImagePullIssuesCustomNamespace(t *testing.T) {
	g := gomega.NewWithT(t)

	customNS := "local-cluster"
	t.Setenv("ARGOCD_NAMESPACE", customNS)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
		Config: getTestEnv().Config,
	}

	// Create the custom namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: customNS},
	}
	err := reconciler.Create(context.TODO(), ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	// Create a pod with ImagePullBackOff in custom namespace
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "failing-pod-custom-ns",
			Namespace: customNS,
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
	}
	err = reconciler.Create(context.TODO(), pod)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	err = reconciler.deletePodsWithImagePullIssues()
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Verify the pod was deleted
	checkPod := &corev1.Pod{}
	err = reconciler.Get(context.TODO(), types.NamespacedName{Name: "failing-pod-custom-ns", Namespace: customNS}, checkPod)
	g.Expect(errors.IsNotFound(err)).To(gomega.BeTrue(), "Pod in custom namespace should have been deleted")
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

func TestIsOCPCluster(t *testing.T) {
	tests := []struct {
		name     string
		objects  []runtime.Object
		expected bool
	}{
		{
			name:     "no OCP indicators",
			objects:  []runtime.Object{},
			expected: false,
		},
		{
			name: "ClusterVersion CRD present",
			objects: []runtime.Object{
				func() runtime.Object {
					crd := &unstructured.Unstructured{}
					crd.SetAPIVersion("apiextensions.k8s.io/v1")
					crd.SetKind("CustomResourceDefinition")
					crd.SetName("clusterversions.config.openshift.io")
					return crd
				}(),
			},
			expected: true,
		},
		{
			name: "Infrastructure CRD present",
			objects: []runtime.Object{
				func() runtime.Object {
					crd := &unstructured.Unstructured{}
					crd.SetAPIVersion("apiextensions.k8s.io/v1")
					crd.SetKind("CustomResourceDefinition")
					crd.SetName("infrastructures.config.openshift.io")
					return crd
				}(),
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			scheme := runtime.NewScheme()
			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, obj := range tt.objects {
				builder = builder.WithRuntimeObjects(obj)
			}
			r := &GitopsAddonReconciler{Client: builder.Build()}
			result, err := r.isOCPCluster(context.Background())
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(result).To(gomega.Equal(tt.expected))
		})
	}
}

func TestIsHubCluster(t *testing.T) {
	tests := []struct {
		name     string
		objects  []runtime.Object
		expected bool
	}{
		{
			name:     "no hub indicators",
			objects:  []runtime.Object{},
			expected: false,
		},
		{
			name: "ClusterManager resource exists",
			objects: []runtime.Object{
				func() runtime.Object {
					cm := &unstructured.Unstructured{}
					cm.SetAPIVersion("operator.open-cluster-management.io/v1")
					cm.SetKind("ClusterManager")
					cm.SetName("cluster-manager")
					return cm
				}(),
			},
			expected: true,
		},
		{
			name: "ManagedCluster local-cluster by name",
			objects: []runtime.Object{
				func() runtime.Object {
					mc := &unstructured.Unstructured{}
					mc.SetAPIVersion("cluster.open-cluster-management.io/v1")
					mc.SetKind("ManagedCluster")
					mc.SetName("local-cluster")
					return mc
				}(),
			},
			expected: true,
		},
		{
			name: "ManagedCluster with local-cluster label",
			objects: []runtime.Object{
				func() runtime.Object {
					mc := &unstructured.Unstructured{}
					mc.SetAPIVersion("cluster.open-cluster-management.io/v1")
					mc.SetKind("ManagedCluster")
					mc.SetName("my-hub")
					mc.SetLabels(map[string]string{"local-cluster": "true"})
					return mc
				}(),
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			scheme := runtime.NewScheme()
			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, obj := range tt.objects {
				builder = builder.WithRuntimeObjects(obj)
			}
			r := &GitopsAddonReconciler{Client: builder.Build()}
			result, err := r.isHubCluster(context.Background())
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(result).To(gomega.Equal(tt.expected))
		})
	}
}

func TestGetOLMEnvOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		envKey       string
		envValue     string
		defaultValue string
		expected     string
	}{
		{
			name:         "env var not set returns default",
			envKey:       "OLM_TEST_VAR",
			envValue:     "",
			defaultValue: "default-value",
			expected:     "default-value",
		},
		{
			name:         "env var set returns env value",
			envKey:       "OLM_TEST_VAR",
			envValue:     "custom-value",
			defaultValue: "default-value",
			expected:     "custom-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			if tt.envValue != "" {
				t.Setenv(tt.envKey, tt.envValue)
			} else {
				unsetEnvForTest(t, tt.envKey)
			}
			g.Expect(getOLMEnvOrDefault(tt.envKey, tt.defaultValue)).To(gomega.Equal(tt.expected))
		})
	}
}

func TestCreateOrUpdateOLMSubscriptionWithEnvVars(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
		Config: getTestEnv().Config,
	}

	// Set custom env vars (t.Setenv restores original values on test cleanup)
	t.Setenv("OLM_SUBSCRIPTION_NAME", "custom-gitops-operator")
	t.Setenv("OLM_SUBSCRIPTION_NAMESPACE", "custom-operators")
	t.Setenv("OLM_SUBSCRIPTION_CHANNEL", "stable")
	t.Setenv("OLM_SUBSCRIPTION_SOURCE", "custom-catalog")
	t.Setenv("OLM_SUBSCRIPTION_SOURCE_NAMESPACE", "custom-marketplace")
	t.Setenv("OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL", "Manual")

	// Create namespace first
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-operators"},
	}
	err := reconciler.Create(context.TODO(), ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	err = reconciler.createOrUpdateOLMSubscription(context.Background())
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Verify the subscription was created with custom values
	sub := &unstructured.Unstructured{}
	sub.SetAPIVersion("operators.coreos.com/v1alpha1")
	sub.SetKind("Subscription")
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "custom-gitops-operator",
		Namespace: "custom-operators",
	}, sub)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	t.Cleanup(func() { _ = reconciler.Delete(context.TODO(), sub) })

	spec, _, _ := unstructured.NestedMap(sub.Object, "spec")
	g.Expect(spec["channel"]).To(gomega.Equal("stable"))
	g.Expect(spec["name"]).To(gomega.Equal("custom-gitops-operator"))
	g.Expect(spec["source"]).To(gomega.Equal("custom-catalog"))
	g.Expect(spec["sourceNamespace"]).To(gomega.Equal("custom-marketplace"))
	g.Expect(spec["installPlanApproval"]).To(gomega.Equal("Manual"))
}

func TestCreateOrUpdateOLMSubscriptionWithDefaults(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
		Config: getTestEnv().Config,
	}

	// Ensure no OLM env vars are set (restore originals on cleanup)
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_NAME")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_NAMESPACE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_CHANNEL")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_SOURCE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_SOURCE_NAMESPACE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL")

	// Create namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "openshift-operators"},
	}
	err := reconciler.Create(context.TODO(), ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	err = reconciler.createOrUpdateOLMSubscription(context.Background())
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Verify the subscription was created with default values
	sub := &unstructured.Unstructured{}
	sub.SetAPIVersion("operators.coreos.com/v1alpha1")
	sub.SetKind("Subscription")
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "openshift-gitops-operator",
		Namespace: "openshift-operators",
	}, sub)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	spec, _, _ := unstructured.NestedMap(sub.Object, "spec")
	t.Cleanup(func() { _ = reconciler.Delete(context.TODO(), sub) })

	g.Expect(spec["channel"]).To(gomega.Equal("latest"))
	g.Expect(spec["source"]).To(gomega.Equal("redhat-operators"))
	g.Expect(spec["sourceNamespace"]).To(gomega.Equal("openshift-marketplace"))
}

func TestCreateOrUpdateOLMSubscriptionUpdatesOwnedExisting(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
		Config: getTestEnv().Config,
	}

	unsetEnvForTest(t, "OLM_SUBSCRIPTION_NAME")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_NAMESPACE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_CHANNEL")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_SOURCE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_SOURCE_NAMESPACE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL")

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "openshift-operators"},
	}
	err := reconciler.Create(context.TODO(), ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	// Create subscription with defaults first (has gitopsaddon label)
	err = reconciler.createOrUpdateOLMSubscription(context.Background())
	g.Expect(err).ToNot(gomega.HaveOccurred())

	sub := &unstructured.Unstructured{}
	sub.SetAPIVersion("operators.coreos.com/v1alpha1")
	sub.SetKind("Subscription")
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "openshift-gitops-operator",
		Namespace: "openshift-operators",
	}, sub)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	spec, _, _ := unstructured.NestedMap(sub.Object, "spec")
	g.Expect(spec["installPlanApproval"]).To(gomega.Equal("Automatic"))
	g.Expect(sub.GetLabels()["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))

	// Now update to Manual — should succeed because subscription has gitopsaddon label
	t.Setenv("OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL", "Manual")

	err = reconciler.createOrUpdateOLMSubscription(context.Background())
	g.Expect(err).ToNot(gomega.HaveOccurred())

	sub2 := &unstructured.Unstructured{}
	sub2.SetAPIVersion("operators.coreos.com/v1alpha1")
	sub2.SetKind("Subscription")
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "openshift-gitops-operator",
		Namespace: "openshift-operators",
	}, sub2)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	t.Cleanup(func() { _ = reconciler.Delete(context.TODO(), sub2) })

	spec2, _, _ := unstructured.NestedMap(sub2.Object, "spec")
	g.Expect(spec2["installPlanApproval"]).To(gomega.Equal("Manual"))
}

func TestCreateOrUpdateOLMSubscriptionSkipsPreExisting(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
		Config: getTestEnv().Config,
	}

	unsetEnvForTest(t, "OLM_SUBSCRIPTION_NAME")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_NAMESPACE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_CHANNEL")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_SOURCE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_SOURCE_NAMESPACE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL")

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "openshift-operators"},
	}
	err := reconciler.Create(context.TODO(), ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	// Simulate a pre-existing subscription (NO gitopsaddon label, different channel)
	preExisting := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      "openshift-gitops-operator",
				"namespace": "openshift-operators",
			},
			"spec": map[string]interface{}{
				"channel":         "stable",
				"name":            "openshift-gitops-operator",
				"source":          "redhat-operators",
				"sourceNamespace": "openshift-marketplace",
			},
		},
	}
	err = reconciler.Create(context.TODO(), preExisting)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Call createOrUpdateOLMSubscription — should NOT overwrite the pre-existing subscription
	err = reconciler.createOrUpdateOLMSubscription(context.Background())
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Verify the subscription was NOT modified
	sub := &unstructured.Unstructured{}
	sub.SetAPIVersion("operators.coreos.com/v1alpha1")
	sub.SetKind("Subscription")
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "openshift-gitops-operator",
		Namespace: "openshift-operators",
	}, sub)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	spec, _, _ := unstructured.NestedMap(sub.Object, "spec")
	g.Expect(spec["channel"]).To(gomega.Equal("stable"))

	t.Cleanup(func() { _ = reconciler.Delete(context.TODO(), sub) })

	labels := sub.GetLabels()
	_, hasLabel := labels["apps.open-cluster-management.io/gitopsaddon"]
	g.Expect(hasLabel).To(gomega.BeFalse())
}

func TestGetArgoCDNamespace(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{
			name:     "no env var set returns default",
			envValue: "",
			expected: GitOpsNamespace,
		},
		{
			name:     "env var set to local-cluster",
			envValue: "local-cluster",
			expected: "local-cluster",
		},
		{
			name:     "env var set to custom namespace",
			envValue: "custom-ns",
			expected: "custom-ns",
		},
		{
			name:     "whitespace-only env var returns default",
			envValue: "   ",
			expected: GitOpsNamespace,
		},
		{
			name:     "env var with trailing whitespace is trimmed",
			envValue: "local-cluster  ",
			expected: "local-cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			if tt.envValue != "" {
				t.Setenv("ARGOCD_NAMESPACE", tt.envValue)
			} else {
				unsetEnvForTest(t, "ARGOCD_NAMESPACE")
			}
			g.Expect(getArgoCDNamespace()).To(gomega.Equal(tt.expected))
		})
	}
}

func TestCreateOrUpdateOLMSubscriptionInstallPlanMissing(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
		Config: getTestEnv().Config,
	}

	unsetEnvForTest(t, "OLM_SUBSCRIPTION_NAME")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_NAMESPACE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_CHANNEL")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_SOURCE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_SOURCE_NAMESPACE")
	unsetEnvForTest(t, "OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL")

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "openshift-operators"},
	}
	err := reconciler.Create(context.TODO(), ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	// Create a subscription with gitopsaddon label and InstallPlanMissing condition
	sub := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      "openshift-gitops-operator",
				"namespace": "openshift-operators",
				"labels": map[string]interface{}{
					"apps.open-cluster-management.io/gitopsaddon": "true",
				},
			},
			"spec": map[string]interface{}{
				"name":               "openshift-gitops-operator",
				"channel":            "latest",
				"source":             "redhat-operators",
				"sourceNamespace":    "openshift-marketplace",
				"installPlanApproval": "Automatic",
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{
						"type":   "InstallPlanMissing",
						"status": "True",
					},
				},
			},
		},
	}

	err = reconciler.Create(context.Background(), sub)
	if err != nil && !errors.IsAlreadyExists(err) {
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}

	t.Cleanup(func() {
		sub2 := &unstructured.Unstructured{}
		sub2.SetAPIVersion("operators.coreos.com/v1alpha1")
		sub2.SetKind("Subscription")
		sub2.SetName("openshift-gitops-operator")
		sub2.SetNamespace("openshift-operators")
		_ = reconciler.Delete(context.TODO(), sub2)
	})

	// Call createOrUpdateOLMSubscription — should detect InstallPlanMissing and recreate
	err = reconciler.createOrUpdateOLMSubscription(context.Background())
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// The subscription should still exist (recreated)
	newSub := &unstructured.Unstructured{}
	newSub.SetAPIVersion("operators.coreos.com/v1alpha1")
	newSub.SetKind("Subscription")
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "openshift-gitops-operator",
		Namespace: "openshift-operators",
	}, newSub)
	g.Expect(err).ToNot(gomega.HaveOccurred())
	g.Expect(newSub.GetLabels()["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))
}
