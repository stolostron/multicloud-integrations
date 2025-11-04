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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestResourceManagement(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name        string
		obj         *unstructured.Unstructured
		existingObj *unstructured.Unstructured
		expectError bool
		expectSkip  bool
	}{
		{
			name: "resource_gets_management_label",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "test-cm",
						"namespace": "test-ns",
					},
					"data": map[string]interface{}{
						"key": "value",
					},
				},
			},
			expectError: false,
			expectSkip:  false,
		},
		{
			name: "resource_skipped_with_annotation",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "skip-cm",
						"namespace": "test-ns",
					},
					"data": map[string]interface{}{
						"key": "value",
					},
				},
			},
			existingObj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "skip-cm",
						"namespace": "test-ns",
						"annotations": map[string]interface{}{
							"gitops-addon.open-cluster-management.io/skip": "true",
						},
					},
					"data": map[string]interface{}{
						"key": "existing-value",
					},
				},
			},
			expectError: false,
			expectSkip:  true,
		},
		{
			name: "argocd_cr_skipped_with_annotation",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "argoproj.io/v1beta1",
					"kind":       "ArgoCD",
					"metadata": map[string]interface{}{
						"name":      "test-argocd-skip",
						"namespace": "test-gitops-ns",
					},
					"spec": map[string]interface{}{
						"server": map[string]interface{}{
							"route": map[string]interface{}{
								"enabled": true,
							},
						},
					},
				},
			},
			existingObj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "argoproj.io/v1beta1",
					"kind":       "ArgoCD",
					"metadata": map[string]interface{}{
						"name":      "test-argocd-skip",
						"namespace": "test-gitops-ns",
						"annotations": map[string]interface{}{
							"gitops-addon.open-cluster-management.io/skip": "true",
						},
					},
					"spec": map[string]interface{}{
						"server": map[string]interface{}{
							"route": map[string]interface{}{
								"enabled": false,
							},
						},
					},
				},
			},
			expectError: false,
			expectSkip:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client:   getTestEnv().Client,
				GitopsNS: "test-gitops-ns",
			}

			if tt.existingObj != nil {
				err := reconciler.Client.Create(context.TODO(), tt.existingObj)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Test applyManifest
			err := reconciler.applyManifest(tt.obj)
			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())

				if !tt.expectSkip {
					// Verify the resource was created/updated with management label
					result := &unstructured.Unstructured{}
					result.SetAPIVersion(tt.obj.GetAPIVersion())
					result.SetKind(tt.obj.GetKind())
					key := types.NamespacedName{
						Name:      tt.obj.GetName(),
						Namespace: tt.obj.GetNamespace(),
					}
					err := reconciler.Client.Get(context.TODO(), key, result)
					g.Expect(err).ToNot(gomega.HaveOccurred())

				// Check that management label was added
				labels := result.GetLabels()
				g.Expect(labels).ToNot(gomega.BeNil())
				g.Expect(labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))
				}
			}

			// Clean up for next test
			if tt.existingObj != nil {
				_ = reconciler.Client.Delete(context.TODO(), tt.existingObj)
			}
		})
	}
}

func TestApplyManifestSelectivelyWithManagement(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name        string
		obj         *unstructured.Unstructured
		existingObj *unstructured.Unstructured
		expectError bool
		expectSkip  bool
	}{
		{
			name: "service_account_gets_management_label",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ServiceAccount",
					"metadata": map[string]interface{}{
						"name":      "test-sa",
						"namespace": "test-ns",
					},
				},
			},
			expectError: false,
			expectSkip:  false,
		},
		{
			name: "service_account_skipped_with_annotation",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ServiceAccount",
					"metadata": map[string]interface{}{
						"name":      "skip-sa",
						"namespace": "test-ns",
					},
				},
			},
			existingObj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ServiceAccount",
					"metadata": map[string]interface{}{
						"name":      "skip-sa",
						"namespace": "test-ns",
						"annotations": map[string]interface{}{
							"gitops-addon.open-cluster-management.io/skip": "true",
						},
					},
				},
			},
			expectError: false,
			expectSkip:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client:   getTestEnv().Client,
				GitopsNS: "test-gitops-ns",
			}

			if tt.existingObj != nil {
				err := reconciler.Client.Create(context.TODO(), tt.existingObj)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Test applyManifestSelectively
			err := reconciler.applyManifestSelectively(tt.obj)
			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())

				if !tt.expectSkip {
					// Verify the resource was created/updated with management label
					result := &unstructured.Unstructured{}
					result.SetAPIVersion(tt.obj.GetAPIVersion())
					result.SetKind(tt.obj.GetKind())
					key := types.NamespacedName{
						Name:      tt.obj.GetName(),
						Namespace: tt.obj.GetNamespace(),
					}
					err := reconciler.Client.Get(context.TODO(), key, result)
					g.Expect(err).ToNot(gomega.HaveOccurred())

				// Check that management label was added
				labels := result.GetLabels()
				g.Expect(labels).ToNot(gomega.BeNil())
				g.Expect(labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))
				}
			}

			// Clean up for next test
			if tt.existingObj != nil {
				_ = reconciler.Client.Delete(context.TODO(), tt.existingObj)
			}
		})
	}
}

func TestNamespaceManagement(t *testing.T) {
	t.Skip("Skipping namespace test due to image pull secret dependencies in test environment")
}
