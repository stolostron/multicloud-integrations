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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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

// TestIsPausedFailsClosed verifies that API errors are treated as "paused"
// to prevent race conditions during cleanup.
func TestIsPausedFailsClosed(t *testing.T) {
	g := gomega.NewWithT(t)

	// Create a scheme that does NOT include ConfigMap, so Get will fail
	// with a "no kind is registered" error (not NotFound).
	scheme := runtime.NewScheme()

	testClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	paused := IsPaused(context.TODO(), testClient)
	g.Expect(paused).To(gomega.BeTrue(), "IsPaused should return true on API errors (fail closed)")
}

// TestDeleteRemainingGitOpsCSVs verifies cleanup of leftover CSVs
func TestDeleteRemainingGitOpsCSVs(t *testing.T) {
	g := gomega.NewWithT(t)

	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	csv1 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "ClusterServiceVersion",
			"metadata": map[string]interface{}{
				"name":      "openshift-gitops-operator.v1.14.0",
				"namespace": "openshift-operators",
			},
		},
	}
	csv2 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "ClusterServiceVersion",
			"metadata": map[string]interface{}{
				"name":      "openshift-gitops-operator.v1.15.0",
				"namespace": "openshift-operators",
			},
		},
	}
	unrelatedCSV := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "ClusterServiceVersion",
			"metadata": map[string]interface{}{
				"name":      "other-operator.v1.0.0",
				"namespace": "openshift-operators",
			},
		},
	}

	testClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(csv1, csv2, unrelatedCSV).
		Build()

	err = deleteRemainingGitOpsCSVs(context.TODO(), testClient, "openshift-operators")
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// gitops CSVs should be deleted
	remaining := &unstructured.Unstructured{}
	remaining.SetAPIVersion("operators.coreos.com/v1alpha1")
	remaining.SetKind("ClusterServiceVersion")

	err = testClient.Get(context.TODO(), types.NamespacedName{
		Name: "openshift-gitops-operator.v1.14.0", Namespace: "openshift-operators",
	}, remaining)
	g.Expect(err).To(gomega.HaveOccurred(), "gitops CSV v1.14 should be deleted")

	err = testClient.Get(context.TODO(), types.NamespacedName{
		Name: "openshift-gitops-operator.v1.15.0", Namespace: "openshift-operators",
	}, remaining)
	g.Expect(err).To(gomega.HaveOccurred(), "gitops CSV v1.15 should be deleted")

	// Unrelated CSV should remain
	err = testClient.Get(context.TODO(), types.NamespacedName{
		Name: "other-operator.v1.0.0", Namespace: "openshift-operators",
	}, remaining)
	g.Expect(err).ToNot(gomega.HaveOccurred(), "unrelated CSV should not be deleted")
}

// TestPatchArgoCDCRDConversionWebhook tests the CRD conversion webhook patching
func TestPatchArgoCDCRDConversionWebhook(t *testing.T) {
	g := gomega.NewWithT(t)

	t.Run("no_crd_exists_noop", func(t *testing.T) {
		scheme := runtime.NewScheme()
		err := clientgoscheme.AddToScheme(scheme)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		testClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		err = patchArgoCDCRDConversionWebhook(context.TODO(), testClient)
		g.Expect(err).ToNot(gomega.HaveOccurred())
	})

	t.Run("crd_no_conversion_noop", func(t *testing.T) {
		scheme := runtime.NewScheme()
		err := clientgoscheme.AddToScheme(scheme)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		crd := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apiextensions.k8s.io/v1",
				"kind":       "CustomResourceDefinition",
				"metadata": map[string]interface{}{
					"name": "argocds.argoproj.io",
				},
				"spec": map[string]interface{}{
					"group": "argoproj.io",
				},
			},
		}

		testClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(crd).
			Build()

		err = patchArgoCDCRDConversionWebhook(context.TODO(), testClient)
		g.Expect(err).ToNot(gomega.HaveOccurred())
	})

	t.Run("crd_strategy_none_noop", func(t *testing.T) {
		scheme := runtime.NewScheme()
		err := clientgoscheme.AddToScheme(scheme)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		crd := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apiextensions.k8s.io/v1",
				"kind":       "CustomResourceDefinition",
				"metadata": map[string]interface{}{
					"name": "argocds.argoproj.io",
				},
				"spec": map[string]interface{}{
					"group": "argoproj.io",
					"conversion": map[string]interface{}{
						"strategy": "None",
					},
				},
			},
		}

		testClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(crd).
			Build()

		err = patchArgoCDCRDConversionWebhook(context.TODO(), testClient)
		g.Expect(err).ToNot(gomega.HaveOccurred())
	})

	t.Run("crd_webhook_strategy_patched_to_none", func(t *testing.T) {
		scheme := runtime.NewScheme()
		err := clientgoscheme.AddToScheme(scheme)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		crd := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apiextensions.k8s.io/v1",
				"kind":       "CustomResourceDefinition",
				"metadata": map[string]interface{}{
					"name": "argocds.argoproj.io",
				},
				"spec": map[string]interface{}{
					"group": "argoproj.io",
					"conversion": map[string]interface{}{
						"strategy": "Webhook",
						"webhook": map[string]interface{}{
							"clientConfig": map[string]interface{}{
								"service": map[string]interface{}{
									"name":      "gitops-operator-webhook",
									"namespace": "openshift-gitops-operator",
								},
							},
						},
					},
				},
			},
		}

		testClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(crd).
			Build()

		err = patchArgoCDCRDConversionWebhook(context.TODO(), testClient)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		// Verify the CRD conversion strategy was changed to None
		result := &unstructured.Unstructured{}
		result.SetAPIVersion("apiextensions.k8s.io/v1")
		result.SetKind("CustomResourceDefinition")
		err = testClient.Get(context.TODO(), types.NamespacedName{Name: "argocds.argoproj.io"}, result)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		conversion, found, _ := unstructured.NestedMap(result.Object, "spec", "conversion")
		g.Expect(found).To(gomega.BeTrue(), "spec.conversion should still exist")
		g.Expect(conversion["strategy"]).To(gomega.Equal("None"), "conversion strategy should be None")
		_, webhookExists, _ := unstructured.NestedMap(conversion, "webhook")
		g.Expect(webhookExists).To(gomega.BeFalse(), "webhook config should be removed")
	})
}

// TestClearStalePauseMarker tests the ClearStalePauseMarker function
func TestClearStalePauseMarker(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectDeleted   bool
	}{
		{
			name: "no_marker_exists_noop",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
			},
			expectDeleted: false,
		},
		{
			name: "stale_marker_deleted",
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
						"paused":    "true",
						"reason":    "cleanup",
						"timestamp": "2025-01-01T00:00:00Z",
					},
				},
			},
			expectDeleted: true,
		},
		{
			name: "marker_with_owner_ref_deleted",
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
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
								Name:       AddonDeploymentName,
								UID:        "old-uid",
							},
						},
					},
					Data: map[string]string{
						"paused":    "true",
						"reason":    "cleanup",
						"timestamp": "2025-01-01T00:00:00Z",
					},
				},
			},
			expectDeleted: true,
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

			// Should not panic regardless of state
			ClearStalePauseMarker(context.TODO(), testClient)

			// Verify the marker is gone
			paused := IsPaused(context.TODO(), testClient)
			if tt.expectDeleted {
				g.Expect(paused).To(gomega.BeFalse(), "Expected pause marker to be deleted")
			} else {
				g.Expect(paused).To(gomega.BeFalse(), "Expected no pause marker")
			}

			// Double-check: ConfigMap should not exist
			cm := &corev1.ConfigMap{}
			err = testClient.Get(context.TODO(), types.NamespacedName{
				Name:      PauseMarkerName,
				Namespace: AddonNamespace,
			}, cm)
			if tt.expectDeleted {
				g.Expect(err).To(gomega.HaveOccurred(), "Expected ConfigMap to be deleted")
			}
		})
	}
}

// TestClearStalePauseMarkerWithExplicitReader tests the ClearStalePauseMarker function
// with an explicit reader parameter, simulating the real startup scenario where
// the controller-runtime cache (client.Client) may not have synced yet but
// the uncached APIReader can see the marker.
func TestClearStalePauseMarkerWithExplicitReader(t *testing.T) {
	g := gomega.NewWithT(t)

	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	tests := []struct {
		name                string
		readerObjects       []client.Object // objects visible to the reader
		clientObjects       []client.Object // objects visible to the client
		expectDeleted       bool
		description         string
	}{
		{
			name: "reader_sees_marker_client_does_not",
			// Simulate: uncached APIReader sees the marker, but cached client doesn't yet
			readerObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: AddonNamespace},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
						Labels:    map[string]string{"app": "gitops-addon"},
					},
					Data: map[string]string{
						"paused":    "true",
						"reason":    "cleanup",
						"timestamp": "2025-01-01T00:00:00Z",
					},
				},
			},
			// Client also has it (fake client shares state, so delete works)
			clientObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: AddonNamespace},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
						Labels:    map[string]string{"app": "gitops-addon"},
					},
					Data: map[string]string{
						"paused":    "true",
						"reason":    "cleanup",
						"timestamp": "2025-01-01T00:00:00Z",
					},
				},
			},
			expectDeleted: true,
			description:   "Reader finds marker and client deletes it",
		},
		{
			name: "reader_sees_no_marker",
			readerObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: AddonNamespace},
				},
			},
			clientObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: AddonNamespace},
				},
			},
			expectDeleted: false,
			description:   "No marker exists, no-op",
		},
		{
			name: "nil_reader_falls_back_to_client",
			readerObjects: nil, // nil reader - should fall back to client
			clientObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: AddonNamespace},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
						Labels:    map[string]string{"app": "gitops-addon"},
					},
					Data: map[string]string{
						"paused":    "true",
						"reason":    "cleanup",
						"timestamp": "2025-01-01T00:00:00Z",
					},
				},
			},
			expectDeleted: true,
			description:   "Nil reader falls back to client which finds and deletes marker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build the main client (used for Delete)
			clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
			if len(tt.clientObjects) > 0 {
				clientBuilder = clientBuilder.WithObjects(tt.clientObjects...)
			}
			testClient := clientBuilder.Build()

			if tt.readerObjects == nil {
				// Pass nil reader - should fall back to client
				ClearStalePauseMarker(context.TODO(), testClient, nil)
			} else {
				// Build a separate reader (simulates uncached APIReader)
				readerBuilder := fake.NewClientBuilder().WithScheme(scheme)
				if len(tt.readerObjects) > 0 {
					readerBuilder = readerBuilder.WithObjects(tt.readerObjects...)
				}
				testReader := readerBuilder.Build()

				ClearStalePauseMarker(context.TODO(), testClient, testReader)
			}

			// Verify result
			cm := &corev1.ConfigMap{}
			getErr := testClient.Get(context.TODO(), types.NamespacedName{
				Name:      PauseMarkerName,
				Namespace: AddonNamespace,
			}, cm)

			if tt.expectDeleted {
				g.Expect(getErr).To(gomega.HaveOccurred(), tt.description+": marker should be deleted")
			} else {
				// Marker didn't exist to begin with
				paused := IsPaused(context.TODO(), testClient)
				g.Expect(paused).To(gomega.BeFalse(), tt.description+": should not be paused")
			}
		})
	}
}

// TestGetCleanupVerificationWaitDuration tests the getCleanupVerificationWaitDuration function
func TestGetCleanupVerificationWaitDuration(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected time.Duration
	}{
		{
			name:     "default_value_no_env",
			envValue: "",
			expected: 20 * time.Second,
		},
		{
			name:     "set_to_60_seconds",
			envValue: "60",
			expected: 60 * time.Second,
		},
		{
			name:     "set_to_0_seconds",
			envValue: "0",
			expected: 0,
		},
		{
			name:     "invalid_value_returns_default",
			envValue: "invalid",
			expected: 20 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear any existing env var first
			os.Unsetenv("CLEANUP_VERIFICATION_WAIT_SECONDS")

			if tt.envValue != "" {
				os.Setenv("CLEANUP_VERIFICATION_WAIT_SECONDS", tt.envValue)
				defer os.Unsetenv("CLEANUP_VERIFICATION_WAIT_SECONDS")
			}

			result := getCleanupVerificationWaitDuration()
			if result != tt.expected {
				t.Errorf("getCleanupVerificationWaitDuration() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestDeleteGitOpsServiceCR tests the deleteGitOpsServiceCR function
func TestDeleteGitOpsServiceCR(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectError     bool
	}{
		{
			name:            "no_gitopsservice_exists",
			existingObjects: []client.Object{},
			expectError:     false,
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

			err = deleteGitOpsServiceCR(context.TODO(), testClient)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}
		})
	}
}

func TestVerifyNamespaceCleanup(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		namespace       string
		existingObjects []client.Object
		expectedExists  bool
		expectedError   bool
	}{
		{
			name:            "no_deployments_in_namespace",
			namespace:       "openshift-gitops-operator",
			existingObjects: []client.Object{},
			expectedExists:  false,
			expectedError:   false,
		},
		{
			name:      "labeled_deployment_exists",
			namespace: "openshift-gitops-operator",
			existingObjects: []client.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-deploy",
						Namespace: "openshift-gitops-operator",
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectedExists: true,
			expectedError:  false,
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

			exists, err := verifyNamespaceCleanup(context.TODO(), testClient, tt.namespace)

			if tt.expectedError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}
			g.Expect(exists).To(gomega.Equal(tt.expectedExists))
		})
	}
}

// TestDeleteResourcesByType tests the deleteResourcesByType function
func TestDeleteResourcesByType(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		namespace       string
		resourceTypes   []resourceType
		requireLabel    bool
		existingObjects []client.Object
		expectedAllDel  bool
	}{
		{
			name:      "no_resources_to_delete",
			namespace: "test-namespace",
			resourceTypes: []resourceType{
				{"ServiceAccount", "v1"},
			},
			requireLabel:    true,
			existingObjects: []client.Object{},
			expectedAllDel:  true,
		},
		{
			name:      "delete_serviceaccount_with_label",
			namespace: "test-namespace",
			resourceTypes: []resourceType{
				{"ServiceAccount", "v1"},
			},
			requireLabel: true,
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-namespace",
					},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-sa",
						Namespace: "test-namespace",
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectedAllDel: false, // Will return false because we just deleted
		},
		{
			name:      "skip_serviceaccount_without_label",
			namespace: "test-namespace",
			resourceTypes: []resourceType{
				{"ServiceAccount", "v1"},
			},
			requireLabel: true,
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-namespace",
					},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-sa",
						Namespace: "test-namespace",
						// No gitopsaddon label
					},
				},
			},
			expectedAllDel: true, // Returns true because no labeled resources found
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

			allDeleted := deleteResourcesByType(context.TODO(), testClient, tt.resourceTypes, tt.namespace, tt.requireLabel)
			g.Expect(allDeleted).To(gomega.Equal(tt.expectedAllDel))
		})
	}
}

// TestCollectRemainingResources tests the collectRemainingResources function
func TestCollectRemainingResources(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name             string
		namespace        string
		namespacedTypes  []resourceType
		clusterTypes     []resourceType
		requireLabel     bool
		existingObjects  []client.Object
		expectedCount    int
	}{
		{
			name:      "no_remaining_resources",
			namespace: "test-namespace",
			namespacedTypes: []resourceType{
				{"ServiceAccount", "v1"},
			},
			clusterTypes: []resourceType{
				{"ClusterRole", "rbac.authorization.k8s.io/v1"},
			},
			requireLabel:    true,
			existingObjects: []client.Object{},
			expectedCount:   0,
		},
		{
			name:      "one_remaining_cluster_role",
			namespace: "test-namespace",
			namespacedTypes: []resourceType{
				{"ServiceAccount", "v1"},
			},
			clusterTypes: []resourceType{
				{"ClusterRole", "rbac.authorization.k8s.io/v1"},
			},
			requireLabel: true,
			existingObjects: []client.Object{
				&rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-clusterrole",
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectedCount: 1,
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

			remaining := collectRemainingResources(context.TODO(), testClient, tt.namespacedTypes, tt.clusterTypes, tt.namespace, tt.requireLabel)
			g.Expect(remaining).To(gomega.HaveLen(tt.expectedCount))
		})
	}
}


func TestIsHubClusterForCleanup(t *testing.T) {
	g := gomega.NewWithT(t)

	t.Run("no hub indicators returns false", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		testClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		result, err := isHubClusterForCleanup(context.TODO(), testClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(result).To(gomega.BeFalse())
	})

	t.Run("ClusterManager exists returns true", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		cm := &unstructured.Unstructured{}
		cm.SetAPIVersion("operator.open-cluster-management.io/v1")
		cm.SetKind("ClusterManager")
		cm.SetName("cluster-manager")
		testClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cm).Build()
		result, err := isHubClusterForCleanup(context.TODO(), testClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(result).To(gomega.BeTrue())
	})

	t.Run("ManagedCluster local-cluster by name returns true", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		mc := &unstructured.Unstructured{}
		mc.SetAPIVersion("cluster.open-cluster-management.io/v1")
		mc.SetKind("ManagedCluster")
		mc.SetName("local-cluster")
		testClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(mc).Build()
		result, err := isHubClusterForCleanup(context.TODO(), testClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(result).To(gomega.BeTrue())
	})

	t.Run("ManagedCluster with local-cluster label returns true", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		mc := &unstructured.Unstructured{}
		mc.SetAPIVersion("cluster.open-cluster-management.io/v1")
		mc.SetKind("ManagedCluster")
		mc.SetName("my-hub-cluster")
		mc.SetLabels(map[string]string{"local-cluster": "true"})
		testClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(mc).Build()
		result, err := isHubClusterForCleanup(context.TODO(), testClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(result).To(gomega.BeTrue())
	})

	t.Run("non-hub ManagedCluster returns false", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		mc := &unstructured.Unstructured{}
		mc.SetAPIVersion("cluster.open-cluster-management.io/v1")
		mc.SetKind("ManagedCluster")
		mc.SetName("spoke-cluster-1")
		testClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(mc).Build()
		result, err := isHubClusterForCleanup(context.TODO(), testClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(result).To(gomega.BeFalse())
	})
}

func TestGetInstalledCSVsFromLabeledSubscriptions(t *testing.T) {
	g := gomega.NewWithT(t)
	ctx := context.TODO()

	t.Run("returns CSV from gitopsaddon-labeled subscription", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)

		sub := &unstructured.Unstructured{}
		sub.SetAPIVersion("operators.coreos.com/v1alpha1")
		sub.SetKind("Subscription")
		sub.SetName("openshift-gitops-operator")
		sub.SetNamespace("openshift-operators")
		sub.SetLabels(map[string]string{
			"apps.open-cluster-management.io/gitopsaddon": "true",
		})
		_ = unstructured.SetNestedField(sub.Object, "openshift-gitops-operator.v1.14.0", "status", "installedCSV")

		testClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(sub).Build()

		refs, err := getInstalledCSVsFromLabeledSubscriptions(ctx, testClient, "openshift-operators")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(refs).To(gomega.HaveLen(1))
		g.Expect(refs[0].name).To(gomega.Equal("openshift-gitops-operator.v1.14.0"))
		g.Expect(refs[0].namespace).To(gomega.Equal("openshift-operators"))
	})

	t.Run("ignores subscription without gitopsaddon label", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)

		sub := &unstructured.Unstructured{}
		sub.SetAPIVersion("operators.coreos.com/v1alpha1")
		sub.SetKind("Subscription")
		sub.SetName("openshift-gitops-operator")
		sub.SetNamespace("openshift-operators")
		_ = unstructured.SetNestedField(sub.Object, "openshift-gitops-operator.v1.14.0", "status", "installedCSV")

		testClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(sub).Build()

		refs, err := getInstalledCSVsFromLabeledSubscriptions(ctx, testClient, "openshift-operators")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(refs).To(gomega.BeEmpty())
	})

	t.Run("skips subscription without installedCSV status", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)

		sub := &unstructured.Unstructured{}
		sub.SetAPIVersion("operators.coreos.com/v1alpha1")
		sub.SetKind("Subscription")
		sub.SetName("openshift-gitops-operator")
		sub.SetNamespace("openshift-operators")
		sub.SetLabels(map[string]string{
			"apps.open-cluster-management.io/gitopsaddon": "true",
		})

		testClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(sub).Build()

		refs, err := getInstalledCSVsFromLabeledSubscriptions(ctx, testClient, "openshift-operators")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(refs).To(gomega.BeEmpty())
	})

	t.Run("returns empty when no subscriptions exist", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		testClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		refs, err := getInstalledCSVsFromLabeledSubscriptions(ctx, testClient, "openshift-operators")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(refs).To(gomega.BeEmpty())
	})

	t.Run("ignores labeled subscription in different namespace", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)

		subTarget := &unstructured.Unstructured{}
		subTarget.SetAPIVersion("operators.coreos.com/v1alpha1")
		subTarget.SetKind("Subscription")
		subTarget.SetName("openshift-gitops-operator")
		subTarget.SetNamespace("openshift-operators")
		subTarget.SetLabels(map[string]string{
			"apps.open-cluster-management.io/gitopsaddon": "true",
		})
		_ = unstructured.SetNestedField(subTarget.Object, "openshift-gitops-operator.v1.14.0", "status", "installedCSV")

		subOther := &unstructured.Unstructured{}
		subOther.SetAPIVersion("operators.coreos.com/v1alpha1")
		subOther.SetKind("Subscription")
		subOther.SetName("openshift-gitops-operator")
		subOther.SetNamespace("other-namespace")
		subOther.SetLabels(map[string]string{
			"apps.open-cluster-management.io/gitopsaddon": "true",
		})
		_ = unstructured.SetNestedField(subOther.Object, "openshift-gitops-operator.v1.15.0", "status", "installedCSV")

		testClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(subTarget, subOther).Build()

		refs, err := getInstalledCSVsFromLabeledSubscriptions(ctx, testClient, "openshift-operators")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(refs).To(gomega.HaveLen(1))
		g.Expect(refs[0].name).To(gomega.Equal("openshift-gitops-operator.v1.14.0"))
		g.Expect(refs[0].namespace).To(gomega.Equal("openshift-operators"))
	})
}

func TestDeleteOLMResourcesOnlyDeletesOwnedCSVs(t *testing.T) {
	g := gomega.NewWithT(t)
	ctx := context.TODO()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	// Our subscription with gitopsaddon label and an installedCSV
	ourSub := &unstructured.Unstructured{}
	ourSub.SetAPIVersion("operators.coreos.com/v1alpha1")
	ourSub.SetKind("Subscription")
	ourSub.SetName("openshift-gitops-operator")
	ourSub.SetNamespace("openshift-operators")
	ourSub.SetLabels(map[string]string{
		"apps.open-cluster-management.io/gitopsaddon": "true",
	})
	_ = unstructured.SetNestedField(ourSub.Object, "openshift-gitops-operator.v1.14.0", "status", "installedCSV")

	// Pre-existing subscription WITHOUT gitopsaddon label
	adminSub := &unstructured.Unstructured{}
	adminSub.SetAPIVersion("operators.coreos.com/v1alpha1")
	adminSub.SetKind("Subscription")
	adminSub.SetName("admin-argocd")
	adminSub.SetNamespace("openshift-operators")
	_ = unstructured.SetNestedField(adminSub.Object, "admin-argocd.v2.0.0", "status", "installedCSV")

	// Our CSV (should be deleted)
	ourCSV := &unstructured.Unstructured{}
	ourCSV.SetAPIVersion("operators.coreos.com/v1alpha1")
	ourCSV.SetKind("ClusterServiceVersion")
	ourCSV.SetName("openshift-gitops-operator.v1.14.0")
	ourCSV.SetNamespace("openshift-operators")

	// Admin CSV (should NOT be deleted)
	adminCSV := &unstructured.Unstructured{}
	adminCSV.SetAPIVersion("operators.coreos.com/v1alpha1")
	adminCSV.SetKind("ClusterServiceVersion")
	adminCSV.SetName("admin-argocd.v2.0.0")
	adminCSV.SetNamespace("openshift-operators")

	testClient := fake.NewClientBuilder().WithScheme(scheme).
		WithRuntimeObjects(ourSub, adminSub, ourCSV, adminCSV).Build()

	err := deleteOLMResources(ctx, testClient)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify our subscription was deleted
	checkSub := &unstructured.Unstructured{}
	checkSub.SetAPIVersion("operators.coreos.com/v1alpha1")
	checkSub.SetKind("Subscription")
	err = testClient.Get(ctx, types.NamespacedName{Name: "openshift-gitops-operator", Namespace: "openshift-operators"}, checkSub)
	g.Expect(err).To(gomega.HaveOccurred(), "Our subscription should have been deleted")

	// Verify admin subscription was NOT deleted (no label)
	checkAdminSub := &unstructured.Unstructured{}
	checkAdminSub.SetAPIVersion("operators.coreos.com/v1alpha1")
	checkAdminSub.SetKind("Subscription")
	err = testClient.Get(ctx, types.NamespacedName{Name: "admin-argocd", Namespace: "openshift-operators"}, checkAdminSub)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "Admin subscription should still exist")

	// Verify our CSV was deleted
	checkCSV := &unstructured.Unstructured{}
	checkCSV.SetAPIVersion("operators.coreos.com/v1alpha1")
	checkCSV.SetKind("ClusterServiceVersion")
	err = testClient.Get(ctx, types.NamespacedName{Name: "openshift-gitops-operator.v1.14.0", Namespace: "openshift-operators"}, checkCSV)
	g.Expect(err).To(gomega.HaveOccurred(), "Our CSV should have been deleted")

	// Verify admin CSV was NOT deleted
	checkAdminCSV := &unstructured.Unstructured{}
	checkAdminCSV.SetAPIVersion("operators.coreos.com/v1alpha1")
	checkAdminCSV.SetKind("ClusterServiceVersion")
	err = testClient.Get(ctx, types.NamespacedName{Name: "admin-argocd.v2.0.0", Namespace: "openshift-operators"}, checkAdminCSV)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "Admin CSV should still exist")
}
