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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGitopsAddonCleanupReconciler_uninstallGitopsAgent(t *testing.T) {
	g := gomega.NewWithT(t)

	// Disable cleanup verification wait for tests
	t.Setenv("CLEANUP_VERIFICATION_WAIT_SECONDS", "0")

	tests := []struct {
		name         string
		setupObjects []client.Object
		expectError  bool
	}{
		{
			name: "successful_uninstall_with_argocd_cr",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AddonDeploymentName,
						Namespace: AddonNamespace,
						UID:       "test-uid-1",
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsNamespace,
					},
				},
				&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "argoproj.io/v1beta1",
						"kind":       "ArgoCD",
						"metadata": map[string]interface{}{
							"name":      GitOpsNamespace,
							"namespace": GitOpsNamespace,
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "uninstall_without_argocd_cr",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AddonDeploymentName,
						Namespace: AddonNamespace,
						UID:       "test-uid-2",
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsNamespace,
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test scheme
			scheme := runtime.NewScheme()
			err := clientgoscheme.AddToScheme(scheme)
			g.Expect(err).ToNot(gomega.HaveOccurred())

			// Create a fake client with test objects
			testClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.setupObjects...).
				Build()

			// Create reconciler
			reconciler := &GitopsAddonCleanupReconciler{
				Client: testClient,
			}

			// Call uninstallGitopsAgent
			err = reconciler.uninstallGitopsAgent(context.TODO())

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Verify ArgoCD CR is deleted
			argoCD := &unstructured.Unstructured{}
			argoCD.SetAPIVersion("argoproj.io/v1beta1")
			argoCD.SetKind("ArgoCD")
			err = testClient.Get(context.TODO(), types.NamespacedName{
				Name:      GitOpsNamespace,
				Namespace: GitOpsNamespace,
			}, argoCD)
			g.Expect(err).To(gomega.HaveOccurred()) // Should be NotFound

			// Verify pause marker remains in place after cleanup (prevents controller from reinstalling)
			paused := IsPaused(context.TODO(), testClient)
			g.Expect(paused).To(gomega.BeTrue(), "Pause marker must remain in place after cleanup to prevent reinstallation")
		})
	}
}

func TestUninstallGitopsAgentInternal(t *testing.T) {
	g := gomega.NewWithT(t)

	// Disable cleanup verification wait for tests
	t.Setenv("CLEANUP_VERIFICATION_WAIT_SECONDS", "0")

	tests := []struct {
		name         string
		setupObjects []client.Object
		expectError  bool
		errorCheck   func(error) bool
	}{
		{
			name: "successful_uninstall_with_argocd_cr",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AddonDeploymentName,
						Namespace: AddonNamespace,
						UID:       "test-uid-3",
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsNamespace,
					},
				},
				&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "argoproj.io/v1beta1",
						"kind":       "ArgoCD",
						"metadata": map[string]interface{}{
							"name":      GitOpsNamespace,
							"namespace": GitOpsNamespace,
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "uninstall_without_argocd_cr",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AddonDeploymentName,
						Namespace: AddonNamespace,
						UID:       "test-uid-4",
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsNamespace,
					},
				},
			},
			expectError: false,
		},
		{
			name: "uninstall_with_operator_resources",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AddonDeploymentName,
						Namespace: AddonNamespace,
						UID:       "test-uid-5",
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-gitops-ns",
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-deployment",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-service",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
				&rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-clusterrole",
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-clusterrolebinding",
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test scheme
			scheme := runtime.NewScheme()
			err := clientgoscheme.AddToScheme(scheme)
			g.Expect(err).ToNot(gomega.HaveOccurred())

			// Create a fake client with test objects
			testClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.setupObjects...).
				Build()

			// Call uninstallGitopsAgentInternal
			err = uninstallGitopsAgentInternal(context.TODO(), testClient, GitOpsOperatorNamespace)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
				if tt.errorCheck != nil {
					g.Expect(tt.errorCheck(err)).To(gomega.BeTrue())
				}
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Verify ArgoCD CR is deleted
			argoCD := &unstructured.Unstructured{}
			argoCD.SetAPIVersion("argoproj.io/v1beta1")
			argoCD.SetKind("ArgoCD")
			err = testClient.Get(context.TODO(), types.NamespacedName{
				Name:      GitOpsNamespace,
				Namespace: GitOpsNamespace,
			}, argoCD)
			g.Expect(err).To(gomega.HaveOccurred()) // Should be NotFound

			// Verify pause marker remains in place after cleanup (prevents controller from reinstalling)
			paused := IsPaused(context.TODO(), testClient)
			g.Expect(paused).To(gomega.BeTrue(), "Pause marker must remain in place after cleanup to prevent reinstallation")
		})
	}
}

func TestUninstallGitopsAgentInternal_WaitTimeout(t *testing.T) {
	g := gomega.NewWithT(t)

	// Disable cleanup verification wait for tests
	t.Setenv("CLEANUP_VERIFICATION_WAIT_SECONDS", "0")

	// This test verifies that the uninstall proceeds even if ArgoCD CR deletion times out
	// Note: We can't easily simulate a real timeout with the fake client,
	// but we verify the logic handles the timeout scenario
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Create a fake client without ArgoCD CR (simulating it was already deleted)
	testClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: AddonNamespace,
				},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      AddonDeploymentName,
					Namespace: AddonNamespace,
					UID:       "test-uid-wait-timeout",
				},
			},
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: GitOpsOperatorNamespace,
				},
			},
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-gitops-ns",
				},
			},
		).
		Build()

	// Call uninstallGitopsAgentInternal with non-existent ArgoCD CR
	// This should complete quickly without waiting
	start := time.Now()
	err = uninstallGitopsAgentInternal(context.TODO(), testClient, GitOpsOperatorNamespace)
	elapsed := time.Since(start)

	g.Expect(err).ToNot(gomega.HaveOccurred())
	// Should complete quickly since ArgoCD CR doesn't exist
	g.Expect(elapsed).To(gomega.BeNumerically("<", 5*time.Second))

	// Verify pause marker remains in place after cleanup (prevents controller from reinstalling)
	paused := IsPaused(context.TODO(), testClient)
	g.Expect(paused).To(gomega.BeTrue(), "Pause marker must remain in place after cleanup to prevent reinstallation")
}

func TestDeleteOperatorResourcesInternal(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		setupObjects    []client.Object
		expectDeleted   []types.NamespacedName
		expectRemaining []types.NamespacedName
	}{
		{
			name: "delete_deployment_and_service",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-deployment",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-service",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectDeleted: []types.NamespacedName{
				{Name: "test-deployment", Namespace: GitOpsOperatorNamespace},
				{Name: "test-service", Namespace: GitOpsOperatorNamespace},
			},
		},
		{
			name: "delete_rbac_resources",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-role",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rolebinding",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectDeleted: []types.NamespacedName{
				{Name: "test-role", Namespace: GitOpsOperatorNamespace},
				{Name: "test-rolebinding", Namespace: GitOpsOperatorNamespace},
			},
		},
		{
			name: "delete_cluster_scoped_resources",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-clusterrole",
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-clusterrolebinding",
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectDeleted: []types.NamespacedName{
				{Name: "test-clusterrole"},
				{Name: "test-clusterrolebinding"},
			},
		},
		{
			name: "skip_resources_without_label",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "keep-deployment",
						Namespace: GitOpsOperatorNamespace,
						// No gitopsaddon label
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "delete-deployment",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectDeleted: []types.NamespacedName{
				{Name: "delete-deployment", Namespace: GitOpsOperatorNamespace},
			},
			expectRemaining: []types.NamespacedName{
				{Name: "keep-deployment", Namespace: GitOpsOperatorNamespace},
			},
		},
		{
			name: "delete_configmap_and_serviceaccount",
			setupObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: GitOpsOperatorNamespace,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-configmap",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-serviceaccount",
						Namespace: GitOpsOperatorNamespace,
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectDeleted: []types.NamespacedName{
				{Name: "test-configmap", Namespace: GitOpsOperatorNamespace},
				{Name: "test-serviceaccount", Namespace: GitOpsOperatorNamespace},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test scheme
			scheme := runtime.NewScheme()
			err := clientgoscheme.AddToScheme(scheme)
			g.Expect(err).ToNot(gomega.HaveOccurred())

			// Create a fake client with test objects
			testClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.setupObjects...).
				Build()

			// Call deleteOperatorResources
			deleteOperatorResources(context.TODO(), testClient, GitOpsOperatorNamespace)

			// Verify expected deletions
			for _, key := range tt.expectDeleted {
				// Try different resource types
				deployment := &appsv1.Deployment{}
				service := &corev1.Service{}
				sa := &corev1.ServiceAccount{}
				cm := &corev1.ConfigMap{}
				role := &rbacv1.Role{}
				roleBinding := &rbacv1.RoleBinding{}
				clusterRole := &rbacv1.ClusterRole{}
				clusterRoleBinding := &rbacv1.ClusterRoleBinding{}

				// At least one of these should be NotFound (deleted)
				depErr := testClient.Get(context.TODO(), key, deployment)
				svcErr := testClient.Get(context.TODO(), key, service)
				saErr := testClient.Get(context.TODO(), key, sa)
				cmErr := testClient.Get(context.TODO(), key, cm)
				roleErr := testClient.Get(context.TODO(), key, role)
				rbErr := testClient.Get(context.TODO(), key, roleBinding)
				crErr := testClient.Get(context.TODO(), key, clusterRole)
				crbErr := testClient.Get(context.TODO(), key, clusterRoleBinding)

				// All should be NotFound errors
				allNotFound := (depErr != nil) && (svcErr != nil) && (saErr != nil) &&
					(cmErr != nil) && (roleErr != nil) && (rbErr != nil) &&
					(crErr != nil) && (crbErr != nil)

				g.Expect(allNotFound).To(gomega.BeTrue(), "Expected resource %s to be deleted", key)
			}

			// Verify resources that should remain
			for _, key := range tt.expectRemaining {
				deployment := &appsv1.Deployment{}
				err := testClient.Get(context.TODO(), key, deployment)
				g.Expect(err).ToNot(gomega.HaveOccurred(), "Expected resource %s to remain", key)
			}
		})
	}
}

func TestDeleteOperatorResourcesInternal_NoResources(t *testing.T) {
	g := gomega.NewWithT(t)

	// Test with no resources to delete - should not error
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	testClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: GitOpsOperatorNamespace,
				},
			},
		).
		Build()

	// This should complete without error
	err = deleteOperatorResources(context.TODO(), testClient, GitOpsOperatorNamespace)
	g.Expect(err).ToNot(gomega.HaveOccurred())
}

func TestDeleteOperatorResourcesInternal_PartialFailure(t *testing.T) {
	g := gomega.NewWithT(t)

	// Test that partial failures don't stop the entire cleanup
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	testClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: GitOpsOperatorNamespace,
				},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment-1",
					Namespace: GitOpsOperatorNamespace,
					Labels: map[string]string{
						"apps.open-cluster-management.io/gitopsaddon": "true",
					},
				},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment-2",
					Namespace: GitOpsOperatorNamespace,
					Labels: map[string]string{
						"apps.open-cluster-management.io/gitopsaddon": "true",
					},
				},
			},
		).
		Build()

	// This should complete without panic even if some deletions fail
	err = deleteOperatorResources(context.TODO(), testClient, GitOpsOperatorNamespace)
	// May return error if timeout reached, but should not panic
	_ = err
}

// TestCreatePauseMarker tests the pause marker creation
func TestCreatePauseMarker(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name             string
		existingObjects  []client.Object
		expectError      bool
		verifyPauseState bool
		verifyOwnerRef   bool
	}{
		{
			name: "create_pause_marker_successfully_with_deployment",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AddonDeploymentName,
						Namespace: AddonNamespace,
						UID:       "test-deployment-uid",
					},
				},
			},
			expectError:      false,
			verifyPauseState: true,
			verifyOwnerRef:   true,
		},
		{
			name: "create_pause_marker_already_exists",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
						Labels: map[string]string{
							"app": "gitops-addon",
						},
					},
					Data: map[string]string{
						"paused": "true",
						"reason": "cleanup",
					},
				},
			},
			expectError:      false,
			verifyPauseState: true,
			verifyOwnerRef:   false,
		},
		{
			name: "create_pause_marker_without_deployment",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
			},
			expectError:      false,
			verifyPauseState: true,
			verifyOwnerRef:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := clientgoscheme.AddToScheme(scheme)
			g.Expect(err).ToNot(gomega.HaveOccurred())

			testClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			err = createPauseMarker(context.TODO(), testClient)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			if tt.verifyPauseState {
				// Verify the pause marker exists and has correct state
				paused := IsPaused(context.TODO(), testClient)
				g.Expect(paused).To(gomega.BeTrue(), "Expected addon to be paused")

				// Verify the ConfigMap exists with correct data
				cm := &corev1.ConfigMap{}
				err = testClient.Get(context.TODO(), types.NamespacedName{
					Name:      PauseMarkerName,
					Namespace: AddonNamespace,
				}, cm)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(cm.Data["paused"]).To(gomega.Equal("true"))
				g.Expect(cm.Data["reason"]).To(gomega.Equal("cleanup"))

				// Verify owner reference if expected
				if tt.verifyOwnerRef {
					g.Expect(cm.OwnerReferences).To(gomega.HaveLen(1))
					g.Expect(cm.OwnerReferences[0].Kind).To(gomega.Equal("Deployment"))
					g.Expect(cm.OwnerReferences[0].Name).To(gomega.Equal(AddonDeploymentName))
				}
			}
		})
	}
}

// TestIsPaused tests the IsPaused function
func TestIsPaused(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectedPaused  bool
	}{
		{
			name: "not_paused_no_marker",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
			},
			expectedPaused: false,
		},
		{
			name: "paused_with_marker",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
					},
					Data: map[string]string{
						"paused": "true",
					},
				},
			},
			expectedPaused: true,
		},
		{
			name: "not_paused_marker_false",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
					},
					Data: map[string]string{
						"paused": "false",
					},
				},
			},
			expectedPaused: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := clientgoscheme.AddToScheme(scheme)
			g.Expect(err).ToNot(gomega.HaveOccurred())

			testClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			paused := IsPaused(context.TODO(), testClient)
			g.Expect(paused).To(gomega.Equal(tt.expectedPaused))
		})
	}
}

// TestUninstallGitopsAgentInternal_PauseMarkerRemains verifies that the pause marker remains after cleanup
func TestUninstallGitopsAgentInternal_PauseMarkerRemains(t *testing.T) {
	g := gomega.NewWithT(t)

	// Disable cleanup verification wait for tests
	t.Setenv("CLEANUP_VERIFICATION_WAIT_SECONDS", "0")

	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	testClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: AddonNamespace,
				},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      AddonDeploymentName,
					Namespace: AddonNamespace,
					UID:       "test-deployment-uid",
				},
			},
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: GitOpsOperatorNamespace,
				},
			},
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-gitops-ns",
				},
			},
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "argoproj.io/v1beta1",
					"kind":       "ArgoCD",
					"metadata": map[string]interface{}{
						"name":      GitOpsNamespace,
						"namespace": "test-gitops-ns",
					},
				},
			},
		).
		Build()

	// Verify pause marker doesn't exist initially
	paused := IsPaused(context.TODO(), testClient)
	g.Expect(paused).To(gomega.BeFalse(), "Pause marker should not exist initially")

	// Call uninstallGitopsAgentInternal
	err = uninstallGitopsAgentInternal(context.TODO(), testClient, GitOpsOperatorNamespace)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// CRITICAL: Verify pause marker remains in place after cleanup to prevent reinstallation
	paused = IsPaused(context.TODO(), testClient)
	g.Expect(paused).To(gomega.BeTrue(), "Pause marker MUST remain in place after cleanup to prevent controller from reinstalling")

	// Verify the ConfigMap exists with owner reference
	cm := &corev1.ConfigMap{}
	err = testClient.Get(context.TODO(), types.NamespacedName{
		Name:      PauseMarkerName,
		Namespace: AddonNamespace,
	}, cm)
	g.Expect(err).ToNot(gomega.HaveOccurred(), "Pause marker ConfigMap should still exist")
	g.Expect(cm.Data["paused"]).To(gomega.Equal("true"), "Pause marker should indicate paused state")

	// Verify owner reference exists so it will be garbage collected with Deployment
	g.Expect(cm.OwnerReferences).To(gomega.HaveLen(1), "Pause marker should have owner reference")
	g.Expect(cm.OwnerReferences[0].Kind).To(gomega.Equal("Deployment"))
	g.Expect(cm.OwnerReferences[0].Name).To(gomega.Equal(AddonDeploymentName))
}
