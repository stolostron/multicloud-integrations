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

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestPerformCleanupOperations(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name               string
		argoCDAgentEnabled string
		expectPanic        bool
	}{
		{
			name:               "cleanup_with_agent_disabled",
			argoCDAgentEnabled: "false",
			expectPanic:        false,
		},
		{
			name:               "cleanup_with_agent_enabled",
			argoCDAgentEnabled: "true",
			expectPanic:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client:             getTestEnv().Client,
				GitopsOperatorNS:   "test-operator-ns",
				GitopsNS:           "test-gitops-ns",
				ArgoCDAgentEnabled: tt.argoCDAgentEnabled,
			}

			if tt.expectPanic {
				g.Expect(func() {
					reconciler.PerformCleanupOperations()
				}).To(gomega.Panic())
			} else {
				g.Expect(func() {
					reconciler.PerformCleanupOperations()
				}).ToNot(gomega.Panic())
			}
		})
	}
}

func TestCleanupArgoCDAgent(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client:   getTestEnv().Client,
		GitopsNS: "test-gitops-ns",
	}

	// This test mainly verifies the function doesn't panic
	// The actual chart deletion functionality is tested in deleteChartResources
	g.Expect(func() {
		reconciler.cleanupArgoCDAgent()
	}).ToNot(gomega.Panic())
}

func TestCleanupArgoCDRedisSecret(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name           string
		existingSecret *corev1.Secret
		expectDelete   bool
	}{
		{
			name: "delete_managed_secret",
			existingSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "argocd-redis",
					Namespace: "test-gitops-ns",
					Labels: map[string]string{
						"apps.open-cluster-management.io/gitopsaddon": "true",
					},
				},
			},
			expectDelete: true,
		},
		{
			name: "skip_unmanaged_secret",
			existingSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "argocd-redis",
					Namespace: "test-gitops-ns",
					Labels: map[string]string{
						"other-label": "value",
					},
				},
			},
			expectDelete: false,
		},
		{
			name:           "secret_does_not_exist",
			existingSecret: nil,
			expectDelete:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client:   getTestEnv().Client,
				GitopsNS: "test-gitops-ns",
			}

			// Create existing secret if specified
			if tt.existingSecret != nil {
				err := reconciler.Create(context.TODO(), tt.existingSecret)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Run cleanup
			reconciler.cleanupArgoCDRedisSecret()

			// Verify result
			secret := &corev1.Secret{}
			key := types.NamespacedName{
				Name:      "argocd-redis",
				Namespace: "test-gitops-ns",
			}
			err := reconciler.Get(context.TODO(), key, secret)

			if tt.expectDelete {
				g.Expect(err).To(gomega.HaveOccurred()) // Should be NotFound
			} else if tt.existingSecret != nil {
				g.Expect(err).ToNot(gomega.HaveOccurred()) // Should still exist
			}
		})
	}
}

func TestCleanupArgoCDCR(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name         string
		setupArgoCD  bool
		expectDelete bool
	}{
		{
			name:         "delete_existing_argocd_cr",
			setupArgoCD:  true,
			expectDelete: true,
		},
		{
			name:         "argocd_cr_does_not_exist",
			setupArgoCD:  false,
			expectDelete: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client:   getTestEnv().Client,
				GitopsNS: "test-gitops-ns",
			}

			// Setup ArgoCD CR if needed
			if tt.setupArgoCD {
				argoCD := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "argoproj.io/v1beta1",
						"kind":       "ArgoCD",
						"metadata": map[string]interface{}{
							"name":      "openshift-gitops",
							"namespace": "test-gitops-ns",
						},
					},
				}
				err := reconciler.Create(context.TODO(), argoCD)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Run cleanup
			reconciler.cleanupArgoCDCR()

			// Verify result
			argoCD := &unstructured.Unstructured{}
			argoCD.SetAPIVersion("argoproj.io/v1beta1")
			argoCD.SetKind("ArgoCD")

			key := types.NamespacedName{
				Name:      "openshift-gitops",
				Namespace: "test-gitops-ns",
			}
			err := reconciler.Get(context.TODO(), key, argoCD)

			if tt.expectDelete {
				g.Expect(err).To(gomega.HaveOccurred()) // Should be NotFound
			} else {
				// Should be NotFound anyway since ArgoCD CRD doesn't exist in test
				g.Expect(err).To(gomega.HaveOccurred())
			}
		})
	}
}

func TestCleanupGitOpsDependency(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client:   getTestEnv().Client,
		GitopsNS: "test-gitops-ns",
	}

	// This test mainly verifies the function doesn't panic
	g.Expect(func() {
		reconciler.cleanupGitOpsDependency()
	}).ToNot(gomega.Panic())
}

func TestCleanupGitOpsOperator(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client:           getTestEnv().Client,
		GitopsOperatorNS: "test-operator-ns",
	}

	// This test mainly verifies the function doesn't panic
	g.Expect(func() {
		reconciler.cleanupGitOpsOperator()
	}).ToNot(gomega.Panic())
}

func TestCleanupCRDs(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
	}

	// This test mainly verifies the function doesn't panic
	g.Expect(func() {
		reconciler.cleanupCRDs()
	}).ToNot(gomega.Panic())
}

func TestDeleteCRD(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name         string
		crdName      string
		setupCRD     bool
		expectDelete bool
	}{
		{
			name:         "delete_existing_crd",
			crdName:      "testresources.test.example.com",
			setupCRD:     true,
			expectDelete: true,
		},
		{
			name:         "crd_does_not_exist",
			crdName:      "nonexistent.example.com",
			setupCRD:     false,
			expectDelete: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
			}

			// Setup CRD if needed
			if tt.setupCRD {
				crd := &apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: tt.crdName,
					},
					Spec: apiextensionsv1.CustomResourceDefinitionSpec{
						Group: "test.example.com",
						Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
							Name:    "v1",
							Served:  true,
							Storage: true,
							Schema: &apiextensionsv1.CustomResourceValidation{
								OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
									Type: "object",
								},
							},
						}},
						Scope: apiextensionsv1.NamespaceScoped,
						Names: apiextensionsv1.CustomResourceDefinitionNames{
							Plural: "testresources",
							Kind:   "TestResource",
						},
					},
				}
				err := reconciler.Create(context.TODO(), crd)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Run cleanup
			reconciler.deleteCRD(tt.crdName)

			// Verify result
			crd := &apiextensionsv1.CustomResourceDefinition{}
			key := types.NamespacedName{Name: tt.crdName}
			err := reconciler.Get(context.TODO(), key, crd)

			if tt.expectDelete {
				g.Expect(err).To(gomega.HaveOccurred()) // Should be NotFound
			} else if tt.setupCRD {
				g.Expect(err).ToNot(gomega.HaveOccurred()) // Should still exist
			}
		})
	}
}

func TestDeleteManifest(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name         string
		obj          *unstructured.Unstructured
		setupObject  bool
		expectDelete bool
	}{
		{
			name: "delete_existing_configmap",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "test-cm",
						"namespace": "test-ns",
					},
				},
			},
			setupObject:  true,
			expectDelete: true,
		},
		{
			name: "object_does_not_exist",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "nonexistent-cm",
						"namespace": "test-ns",
					},
				},
			},
			setupObject:  false,
			expectDelete: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
			}

			// Setup object if needed
			if tt.setupObject {
				err := reconciler.Create(context.TODO(), tt.obj)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Run cleanup - this doesn't return an error by design (fire-and-forget)
			reconciler.deleteManifest(tt.obj)

			// Verify result
			result := &unstructured.Unstructured{}
			result.SetAPIVersion(tt.obj.GetAPIVersion())
			result.SetKind(tt.obj.GetKind())

			key := types.NamespacedName{
				Name:      tt.obj.GetName(),
				Namespace: tt.obj.GetNamespace(),
			}
			err := reconciler.Get(context.TODO(), key, result)

			if tt.expectDelete {
				g.Expect(err).To(gomega.HaveOccurred()) // Should be NotFound
			} else if tt.setupObject {
				g.Expect(err).ToNot(gomega.HaveOccurred()) // Should still exist
			}
		})
	}
}

func TestDeleteChartResources(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name        string
		chartPath   string
		namespace   string
		releaseName string
		expectPanic bool
	}{
		{
			name:        "delete_nonexistent_chart",
			chartPath:   "charts/nonexistent",
			namespace:   "test-ns",
			releaseName: "test-release",
			expectPanic: false, // Should handle errors gracefully
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
			}

			if tt.expectPanic {
				g.Expect(func() {
					reconciler.deleteChartResources(tt.chartPath, tt.namespace, tt.releaseName)
				}).To(gomega.Panic())
			} else {
				g.Expect(func() {
					reconciler.deleteChartResources(tt.chartPath, tt.namespace, tt.releaseName)
				}).ToNot(gomega.Panic())
			}
		})
	}
}
