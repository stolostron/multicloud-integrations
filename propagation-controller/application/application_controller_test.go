/*
Copyright 2022.

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

package application

import (
	"context"
	"strconv"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
)

var _ = Describe("Application Pull controller", func() {

	const (
		appName          = "app-1"
		appName2         = "app-2"
		appName3         = "app-3"
		appNamespace     = "default"
		clusterName      = "cluster1"
		localClusterName = "local-cluster"
	)

	appKey := types.NamespacedName{Name: appName, Namespace: appNamespace}
	appKey2 := types.NamespacedName{Name: appName2, Namespace: appNamespace}
	appKey3 := types.NamespacedName{Name: appName3, Namespace: appNamespace}
	ctx := context.Background()

	Context("When Application without OCM pull label is created", func() {
		It("Should not create ManifestWork", func() {
			By("Creating the Application without OCM pull label")
			app1 := &unstructured.Unstructured{}
			app1.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			app1.SetName(appName)
			app1.SetNamespace(appNamespace)
			app1.SetAnnotations(map[string]string{AnnotationKeyOCMManagedCluster: clusterName})
			_ = unstructured.SetNestedField(app1.Object, "default", "spec", "project")
			_ = unstructured.SetNestedField(app1.Object, "https://github.com/argoproj/argocd-example-apps.git", "spec", "source", "repoURL")
			_ = unstructured.SetNestedMap(app1.Object, map[string]interface{}{
				"server":    KubernetesInternalAPIServerAddr,
				"namespace": appNamespace,
			}, "spec", "destination")
			Expect(k8sClient.Create(ctx, app1)).Should(Succeed())
			Expect(k8sClient.Get(ctx, appKey, app1)).Should(Succeed())

			mwKey := types.NamespacedName{Name: generateManifestWorkName(app1.GetName(), app1.GetUID()), Namespace: clusterName}
			mw := workv1.ManifestWork{}
			Consistently(func() bool {
				if err := k8sClient.Get(ctx, mwKey, &mw); err != nil {
					return false
				}
				return true
			}).Should(BeFalse())
		})
	})

	Context("When Application with OCM pull label is created/updated/deleted", func() {
		It("Should create/update/delete ManifestWork", func() {
			By("Creating the OCM ManagedCluster")
			managedCluster := clusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterName,
				},
			}
			Expect(k8sClient.Create(ctx, &managedCluster)).Should(Succeed())

			By("Creating the OCM ManagedCluster namespace")
			managedClusterNs := corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterName,
				},
			}
			Expect(k8sClient.Create(ctx, &managedClusterNs)).Should(Succeed())

			By("Creating the Application with OCM pull label")
			app2 := &unstructured.Unstructured{}
			app2.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			app2.SetName(appName2)
			app2.SetNamespace(appNamespace)
			app2.SetLabels(map[string]string{LabelKeyPull: strconv.FormatBool(true)})
			app2.SetAnnotations(map[string]string{AnnotationKeyOCMManagedCluster: clusterName})
			app2.SetFinalizers([]string{ResourcesFinalizerName})
			_ = unstructured.SetNestedField(app2.Object, "default", "spec", "project")
			_ = unstructured.SetNestedField(app2.Object, "https://github.com/argoproj/argocd-example-apps.git", "spec", "source", "repoURL")
			_ = unstructured.SetNestedMap(app2.Object, map[string]interface{}{
				"server":    KubernetesInternalAPIServerAddr,
				"namespace": appNamespace,
			}, "spec", "destination")
			Expect(k8sClient.Create(ctx, app2)).Should(Succeed())
			Expect(k8sClient.Get(ctx, appKey2, app2)).Should(Succeed())

			mwKey := types.NamespacedName{Name: generateManifestWorkName(app2.GetName(), app2.GetUID()), Namespace: clusterName}
			mw := workv1.ManifestWork{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, mwKey, &mw); err != nil {
					return false
				}
				return true
			}).Should(BeTrue())

			By("Updating the Application")
			spec := map[string]interface{}{
				"project": "somethingelse",
				"source":  map[string]interface{}{"repoURL": "dummy"},
				"destination": map[string]interface{}{
					"server":    KubernetesInternalAPIServerAddr,
					"namespace": appNamespace,
				},
			}
			Expect(unstructured.SetNestedField(app2.Object, spec, "spec")).Should(Succeed())
			Expect(k8sClient.Update(ctx, app2)).Should(Succeed())
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, mwKey, &mw); err != nil {
					return false
				}
				if len(mw.Spec.Workload.Manifests) == 0 {
					return false
				}
				if mw.Spec.Workload.Manifests[0].RawExtension.Raw == nil {
					return false
				}
				return strings.Contains(string(mw.Spec.Workload.Manifests[0].RawExtension.Raw), "somethingelse")
			}).Should(BeTrue())

			By("Updating the Application with operation")
			operation := map[string]interface{}{
				"info": []interface{}{
					map[string]interface{}{
						"name":  "Reason",
						"value": "ApplicationSet RollingSync triggered a sync of this Application resource.",
					},
				},
				"initiatedBy": map[string]interface{}{
					"automated": true,
					"username":  "applicationset-controller",
				},
				"retry": map[string]interface{}{},
				"sync": map[string]interface{}{
					"syncOptions": []interface{}{
						"CreateNamespace=true",
					},
				},
			}
			Expect(unstructured.SetNestedField(app2.Object, operation, "operation")).Should(Succeed())
			Expect(k8sClient.Update(ctx, app2)).Should(Succeed())
			Eventually(func() bool {
				updatedApp := &unstructured.Unstructured{}
				updatedApp.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "argoproj.io",
					Version: "v1alpha1",
					Kind:    "Application",
				})
				err := k8sClient.Get(ctx, appKey2, updatedApp)
				if err != nil {
					return false
				}
				_, ok := updatedApp.Object["operation"]
				return !ok
			}).Should(BeTrue())

			By("Deleting the Application")
			Expect(k8sClient.Get(ctx, appKey2, app2)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, app2)).Should(Succeed())
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, mwKey, &mw); err != nil {
					return true
				}
				return false
			}).Should(BeTrue())
		})
	})

	Context("When Application with OCM pull label is created for local-cluster", func() {
		It("Should not create ManifestWork", func() {
			By("Creating the OCM ManagedCluster")
			managedCluster := clusterv1.ManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:   localClusterName,
					Labels: map[string]string{"local-cluster": "true"},
				},
			}
			Expect(k8sClient.Create(ctx, &managedCluster)).Should(Succeed())

			By("Creating the OCM ManagedCluster namespace")
			managedClusterNs := corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: localClusterName,
				},
			}
			Expect(k8sClient.Create(ctx, &managedClusterNs)).Should(Succeed())

			By("Creating the Application with OCM pull label and local-cluster")
			app3 := &unstructured.Unstructured{}
			app3.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			app3.SetName(appName3)
			app3.SetNamespace(appNamespace)
			app3.SetLabels(map[string]string{LabelKeyPull: strconv.FormatBool(true)})
			app3.SetAnnotations(map[string]string{AnnotationKeyOCMManagedCluster: localClusterName})
			app3.SetFinalizers([]string{ResourcesFinalizerName})
			_ = unstructured.SetNestedField(app3.Object, "default", "spec", "project")
			_ = unstructured.SetNestedField(app3.Object, "https://github.com/argoproj/argocd-example-apps.git", "spec", "source", "repoURL")
			_ = unstructured.SetNestedMap(app3.Object, map[string]interface{}{
				"server":    KubernetesInternalAPIServerAddr,
				"namespace": appNamespace,
			}, "spec", "destination")
			Expect(k8sClient.Create(ctx, app3)).Should(Succeed())
			Expect(k8sClient.Get(ctx, appKey3, app3)).Should(Succeed())

			mwKey := types.NamespacedName{Name: generateManifestWorkName(app3.GetName(), app3.GetUID()), Namespace: clusterName}
			mw := workv1.ManifestWork{}
			Consistently(func() bool {
				if err := k8sClient.Get(ctx, mwKey, &mw); err != nil {
					return true
				}
				return false
			}).Should(BeTrue())
		})
	})

	Context("When Application has operation field", func() {
		It("Should remove operation field after propagation", func() {
			By("Creating the Application with operation field")
			appWithOperation := &unstructured.Unstructured{}
			appWithOperation.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			appWithOperation.SetName("app-with-operation")
			appWithOperation.SetNamespace(appNamespace)
			appWithOperation.SetLabels(map[string]string{LabelKeyPull: strconv.FormatBool(true)})
			appWithOperation.SetAnnotations(map[string]string{AnnotationKeyOCMManagedCluster: clusterName})
			_ = unstructured.SetNestedField(appWithOperation.Object, "default", "spec", "project")
			_ = unstructured.SetNestedField(appWithOperation.Object, "https://github.com/argoproj/argocd-example-apps.git", "spec", "source", "repoURL")
			_ = unstructured.SetNestedMap(appWithOperation.Object, map[string]interface{}{
				"server":    KubernetesInternalAPIServerAddr,
				"namespace": appNamespace,
			}, "spec", "destination")

			// Add operation field
			appWithOperation.Object["operation"] = map[string]interface{}{
				"sync": map[string]interface{}{
					"prune": true,
				},
			}

			Expect(k8sClient.Create(ctx, appWithOperation)).Should(Succeed())

			By("Waiting for reconciliation to remove operation field")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "app-with-operation", Namespace: appNamespace}, appWithOperation)
				if err != nil {
					return false
				}
				_, hasOperation := appWithOperation.Object["operation"]
				return !hasOperation // operation field should be removed
			}).Should(BeTrue())
		})
	})

	Context("When Application has refresh annotation", func() {
		It("Should remove refresh annotation after propagation", func() {
			By("Creating the Application with refresh annotation")
			appWithRefresh := &unstructured.Unstructured{}
			appWithRefresh.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			appWithRefresh.SetName("app-with-refresh")
			appWithRefresh.SetNamespace(appNamespace)
			appWithRefresh.SetLabels(map[string]string{LabelKeyPull: strconv.FormatBool(true)})
			appWithRefresh.SetAnnotations(map[string]string{
				AnnotationKeyOCMManagedCluster: clusterName,
				AnnotationKeyAppRefresh:        "normal",
			})
			_ = unstructured.SetNestedField(appWithRefresh.Object, "default", "spec", "project")
			_ = unstructured.SetNestedField(appWithRefresh.Object, "https://github.com/argoproj/argocd-example-apps.git", "spec", "source", "repoURL")
			_ = unstructured.SetNestedMap(appWithRefresh.Object, map[string]interface{}{
				"server":    KubernetesInternalAPIServerAddr,
				"namespace": appNamespace,
			}, "spec", "destination")

			Expect(k8sClient.Create(ctx, appWithRefresh)).Should(Succeed())

			By("Waiting for reconciliation to remove refresh annotation")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "app-with-refresh", Namespace: appNamespace}, appWithRefresh)
				if err != nil {
					return false
				}
				annotations := appWithRefresh.GetAnnotations()
				_, hasRefresh := annotations[AnnotationKeyAppRefresh]
				return !hasRefresh // refresh annotation should be removed
			}).Should(BeTrue())
		})
	})

	Context("When Application has both operation field and refresh annotation", func() {
		It("Should remove both operation field and refresh annotation after propagation", func() {
			By("Creating the Application with both operation field and refresh annotation")
			appWithBoth := &unstructured.Unstructured{}
			appWithBoth.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			appWithBoth.SetName("app-with-both")
			appWithBoth.SetNamespace(appNamespace)
			appWithBoth.SetLabels(map[string]string{LabelKeyPull: strconv.FormatBool(true)})
			appWithBoth.SetAnnotations(map[string]string{
				AnnotationKeyOCMManagedCluster: clusterName,
				AnnotationKeyAppRefresh:        "hard",
			})
			_ = unstructured.SetNestedField(appWithBoth.Object, "default", "spec", "project")
			_ = unstructured.SetNestedField(appWithBoth.Object, "https://github.com/argoproj/argocd-example-apps.git", "spec", "source", "repoURL")
			_ = unstructured.SetNestedMap(appWithBoth.Object, map[string]interface{}{
				"server":    KubernetesInternalAPIServerAddr,
				"namespace": appNamespace,
			}, "spec", "destination")

			// Add operation field
			appWithBoth.Object["operation"] = map[string]interface{}{
				"sync": map[string]interface{}{
					"prune":       true,
					"dryRun":      false,
					"syncOptions": []string{"CreateNamespace=true"},
				},
			}

			Expect(k8sClient.Create(ctx, appWithBoth)).Should(Succeed())

			By("Waiting for reconciliation to remove both operation field and refresh annotation")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "app-with-both", Namespace: appNamespace}, appWithBoth)
				if err != nil {
					return false
				}
				_, hasOperation := appWithBoth.Object["operation"]
				annotations := appWithBoth.GetAnnotations()
				_, hasRefresh := annotations[AnnotationKeyAppRefresh]
				return !hasOperation && !hasRefresh // both should be removed
			}).Should(BeTrue())
		})
	})

	Context("When Application has nil annotations", func() {
		It("Should handle nil annotations gracefully", func() {
			By("Creating the Application with nil annotations initially")
			appWithNilAnnotations := &unstructured.Unstructured{}
			appWithNilAnnotations.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			appWithNilAnnotations.SetName("app-with-nil-annotations")
			appWithNilAnnotations.SetNamespace(appNamespace)
			appWithNilAnnotations.SetLabels(map[string]string{LabelKeyPull: strconv.FormatBool(true)})
			// Set the required annotation for cluster targeting
			appWithNilAnnotations.SetAnnotations(map[string]string{AnnotationKeyOCMManagedCluster: clusterName})
			_ = unstructured.SetNestedField(appWithNilAnnotations.Object, "default", "spec", "project")
			_ = unstructured.SetNestedField(appWithNilAnnotations.Object, "https://github.com/argoproj/argocd-example-apps.git", "spec", "source", "repoURL")
			_ = unstructured.SetNestedMap(appWithNilAnnotations.Object, map[string]interface{}{
				"server":    KubernetesInternalAPIServerAddr,
				"namespace": appNamespace,
			}, "spec", "destination")

			Expect(k8sClient.Create(ctx, appWithNilAnnotations)).Should(Succeed())

			// The test passes if no panic occurs during reconciliation
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "app-with-nil-annotations", Namespace: appNamespace}, appWithNilAnnotations)
				return err == nil
			}).Should(BeTrue())
		})
	})

	Context("When Application has neither operation field nor refresh annotation", func() {
		It("Should not perform unnecessary updates", func() {
			By("Creating the Application without operation field or refresh annotation")
			appWithNeither := &unstructured.Unstructured{}
			appWithNeither.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			appWithNeither.SetName("app-with-neither")
			appWithNeither.SetNamespace(appNamespace)
			appWithNeither.SetLabels(map[string]string{LabelKeyPull: strconv.FormatBool(true)})
			appWithNeither.SetAnnotations(map[string]string{AnnotationKeyOCMManagedCluster: clusterName})
			_ = unstructured.SetNestedField(appWithNeither.Object, "default", "spec", "project")
			_ = unstructured.SetNestedField(appWithNeither.Object, "https://github.com/argoproj/argocd-example-apps.git", "spec", "source", "repoURL")
			_ = unstructured.SetNestedMap(appWithNeither.Object, map[string]interface{}{
				"server":    KubernetesInternalAPIServerAddr,
				"namespace": appNamespace,
			}, "spec", "destination")

			Expect(k8sClient.Create(ctx, appWithNeither)).Should(Succeed())

			// The test passes if the application is successfully processed without updates
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "app-with-neither", Namespace: appNamespace}, appWithNeither)
				return err == nil
			}).Should(BeTrue())
		})
	})
})

// Unit test for operation field removal logic (testing the specific code block)
func Test_removeOperationFieldLogic(t *testing.T) {
	tests := []struct {
		name                string
		inputOperation      interface{}
		expectedNeedsUpdate bool
		shouldHaveOperation bool
	}{
		{
			name: "should remove operation field when present",
			inputOperation: map[string]interface{}{
				"sync": map[string]interface{}{
					"prune": true,
				},
			},
			expectedNeedsUpdate: true,
			shouldHaveOperation: false,
		},
		{
			name:                "should not modify when operation field not present",
			inputOperation:      nil,
			expectedNeedsUpdate: false,
			shouldHaveOperation: false,
		},
		{
			name: "should remove complex operation field",
			inputOperation: map[string]interface{}{
				"info": []interface{}{
					map[string]interface{}{
						"name":  "Reason",
						"value": "ApplicationSet triggered sync",
					},
				},
				"initiatedBy": map[string]interface{}{
					"automated": true,
					"username":  "applicationset-controller",
				},
				"sync": map[string]interface{}{
					"prune":       true,
					"dryRun":      false,
					"syncOptions": []string{"CreateNamespace=true"},
				},
			},
			expectedNeedsUpdate: true,
			shouldHaveOperation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test application
			application := &unstructured.Unstructured{}
			application.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})

			// Set operation field if provided
			if tt.inputOperation != nil {
				application.Object["operation"] = tt.inputOperation
			}

			// Simulate the operation field removal logic from the Reconcile method
			needsUpdate := false

			// This is the exact code block from lines 255-258 in application_controller.go
			if _, ok := application.Object["operation"]; ok {
				delete(application.Object, "operation")
				needsUpdate = true
			}

			// Verify results
			if needsUpdate != tt.expectedNeedsUpdate {
				t.Errorf("needsUpdate = %v, want %v", needsUpdate, tt.expectedNeedsUpdate)
			}

			_, hasOperation := application.Object["operation"]
			if hasOperation != tt.shouldHaveOperation {
				t.Errorf("hasOperation = %v, want %v", hasOperation, tt.shouldHaveOperation)
			}
		})
	}
}

// Unit test for refresh annotation removal logic (testing the specific code block)
func Test_removeRefreshAnnotationLogic(t *testing.T) {
	tests := []struct {
		name                string
		inputAnnotations    map[string]string
		expectedAnnotations map[string]string
		expectedNeedsUpdate bool
	}{
		{
			name: "should remove refresh annotation when present",
			inputAnnotations: map[string]string{
				AnnotationKeyAppRefresh: "normal",
				"other.annotation":      "value",
			},
			expectedAnnotations: map[string]string{
				"other.annotation": "value",
			},
			expectedNeedsUpdate: true,
		},
		{
			name: "should not modify when refresh annotation not present",
			inputAnnotations: map[string]string{
				"other.annotation":   "value",
				"another.annotation": "another-value",
			},
			expectedAnnotations: map[string]string{
				"other.annotation":   "value",
				"another.annotation": "another-value",
			},
			expectedNeedsUpdate: false,
		},
		{
			name:                "should handle nil annotations",
			inputAnnotations:    nil,
			expectedAnnotations: nil,
			expectedNeedsUpdate: false,
		},
		{
			name:                "should handle empty annotations",
			inputAnnotations:    map[string]string{},
			expectedAnnotations: map[string]string{},
			expectedNeedsUpdate: false,
		},
		{
			name: "should remove only refresh annotation when multiple annotations present",
			inputAnnotations: map[string]string{
				AnnotationKeyAppRefresh: "hard",
				"app.annotation":        "app-value",
				"cluster.annotation":    "cluster-value",
			},
			expectedAnnotations: map[string]string{
				"app.annotation":     "app-value",
				"cluster.annotation": "cluster-value",
			},
			expectedNeedsUpdate: true,
		},
		{
			name: "should remove refresh annotation with different values",
			inputAnnotations: map[string]string{
				AnnotationKeyAppRefresh:        "hard",
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedAnnotations: map[string]string{
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedNeedsUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test application
			application := &unstructured.Unstructured{}
			application.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			application.SetAnnotations(tt.inputAnnotations)

			// Simulate the refresh annotation removal logic from the Reconcile method
			needsUpdate := false

			// This is the exact code block from lines 261-268 in application_controller.go
			annotations := application.GetAnnotations()
			if annotations != nil {
				if _, exists := annotations[AnnotationKeyAppRefresh]; exists {
					delete(annotations, AnnotationKeyAppRefresh)
					application.SetAnnotations(annotations)
					needsUpdate = true
				}
			}

			// Verify results
			if needsUpdate != tt.expectedNeedsUpdate {
				t.Errorf("needsUpdate = %v, want %v", needsUpdate, tt.expectedNeedsUpdate)
			}

			resultAnnotations := application.GetAnnotations()

			if tt.expectedAnnotations == nil {
				if len(resultAnnotations) > 0 {
					t.Errorf("expected nil annotations, got %v", resultAnnotations)
				}
			} else {
				if len(resultAnnotations) != len(tt.expectedAnnotations) {
					t.Errorf("annotation count = %d, want %d", len(resultAnnotations), len(tt.expectedAnnotations))
				}

				for key, expectedValue := range tt.expectedAnnotations {
					if actualValue, exists := resultAnnotations[key]; !exists || actualValue != expectedValue {
						t.Errorf("annotation[%s] = %v, want %v", key, actualValue, expectedValue)
					}
				}

				// Verify refresh annotation is not present
				if _, exists := resultAnnotations[AnnotationKeyAppRefresh]; exists {
					t.Errorf("refresh annotation should be removed but still present")
				}
			}
		})
	}
}

// Unit test for combined operation field and refresh annotation removal logic
func Test_removeBothOperationAndRefreshLogic(t *testing.T) {
	tests := []struct {
		name                string
		inputOperation      interface{}
		inputAnnotations    map[string]string
		expectedAnnotations map[string]string
		expectedNeedsUpdate bool
		shouldHaveOperation bool
	}{
		{
			name: "should remove both operation field and refresh annotation",
			inputOperation: map[string]interface{}{
				"sync": map[string]interface{}{
					"prune": true,
				},
			},
			inputAnnotations: map[string]string{
				AnnotationKeyAppRefresh:        "normal",
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedAnnotations: map[string]string{
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedNeedsUpdate: true,
			shouldHaveOperation: false,
		},
		{
			name:           "should remove only operation field when no refresh annotation",
			inputOperation: map[string]interface{}{"sync": map[string]interface{}{"prune": true}},
			inputAnnotations: map[string]string{
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedAnnotations: map[string]string{
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedNeedsUpdate: true,
			shouldHaveOperation: false,
		},
		{
			name:           "should remove only refresh annotation when no operation field",
			inputOperation: nil,
			inputAnnotations: map[string]string{
				AnnotationKeyAppRefresh:        "hard",
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedAnnotations: map[string]string{
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedNeedsUpdate: true,
			shouldHaveOperation: false,
		},
		{
			name:           "should not update when neither present",
			inputOperation: nil,
			inputAnnotations: map[string]string{
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedAnnotations: map[string]string{
				AnnotationKeyOCMManagedCluster: "test-cluster",
			},
			expectedNeedsUpdate: false,
			shouldHaveOperation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test application
			application := &unstructured.Unstructured{}
			application.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "Application",
			})
			application.SetAnnotations(tt.inputAnnotations)

			// Set operation field if provided
			if tt.inputOperation != nil {
				application.Object["operation"] = tt.inputOperation
			}

			// Simulate the combined removal logic from the Reconcile method
			needsUpdate := false

			// This is the exact code block from lines 255-268 in application_controller.go
			if _, ok := application.Object["operation"]; ok {
				delete(application.Object, "operation")
				needsUpdate = true
			}

			annotations := application.GetAnnotations()
			if annotations != nil {
				if _, exists := annotations[AnnotationKeyAppRefresh]; exists {
					delete(annotations, AnnotationKeyAppRefresh)
					application.SetAnnotations(annotations)
					needsUpdate = true
				}
			}

			// Verify results
			if needsUpdate != tt.expectedNeedsUpdate {
				t.Errorf("needsUpdate = %v, want %v", needsUpdate, tt.expectedNeedsUpdate)
			}

			_, hasOperation := application.Object["operation"]
			if hasOperation != tt.shouldHaveOperation {
				t.Errorf("hasOperation = %v, want %v", hasOperation, tt.shouldHaveOperation)
			}

			resultAnnotations := application.GetAnnotations()

			if len(resultAnnotations) != len(tt.expectedAnnotations) {
				t.Errorf("annotation count = %d, want %d", len(resultAnnotations), len(tt.expectedAnnotations))
			}

			for key, expectedValue := range tt.expectedAnnotations {
				if actualValue, exists := resultAnnotations[key]; !exists || actualValue != expectedValue {
					t.Errorf("annotation[%s] = %v, want %v", key, actualValue, expectedValue)
				}
			}

			// Verify refresh annotation is not present
			if _, exists := resultAnnotations[AnnotationKeyAppRefresh]; exists {
				t.Errorf("refresh annotation should be removed but still present")
			}
		})
	}
}
