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
	"embed"
	"os"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// Test embedded file system for tests
//
//go:embed charts/openshift-gitops-operator/**
var testFS embed.FS

//go:embed routes-openshift-crd/**
var testRouteCRDFS embed.FS

func TestParseImageReference(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name         string
		imageRef     string
		expectedRepo string
		expectedTag  string
		expectError  bool
	}{
		{
			name:         "image_with_tag",
			imageRef:     "registry.io/repo/image:v1.0.0",
			expectedRepo: "registry.io/repo/image",
			expectedTag:  "v1.0.0",
			expectError:  false,
		},
		{
			name:         "image_with_digest",
			imageRef:     "registry.io/repo/image@sha256:abcdef123456",
			expectedRepo: "registry.io/repo/image",
			expectedTag:  "sha256:abcdef123456",
			expectError:  false,
		},
		{
			name:         "image_with_port_and_tag",
			imageRef:     "localhost:5000/image:latest",
			expectedRepo: "localhost:5000/image",
			expectedTag:  "latest",
			expectError:  false,
		},
		{
			name:         "image_without_tag",
			imageRef:     "registry.io/repo/image",
			expectedRepo: "",
			expectedTag:  "",
			expectError:  true,
		},
		{
			name:         "simple_image_name",
			imageRef:     "nginx",
			expectedRepo: "",
			expectedTag:  "",
			expectError:  true,
		},
		{
			name:         "complex_registry_with_tag",
			imageRef:     "my-registry.example.com:443/namespace/repo:v2.1.0",
			expectedRepo: "my-registry.example.com:443/namespace/repo",
			expectedTag:  "v2.1.0",
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, tag, err := ParseImageReference(tt.imageRef)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(repo).To(gomega.Equal(tt.expectedRepo))
				g.Expect(tag).To(gomega.Equal(tt.expectedTag))
			}
		})
	}
}

func TestParseImageComponents(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name         string
		imageRef     string
		expectedRepo string
		expectedTag  string
	}{
		{
			name:         "basic_image_with_tag",
			imageRef:     "nginx:1.20",
			expectedRepo: "nginx",
			expectedTag:  "1.20",
		},
		{
			name:         "registry_image_with_digest",
			imageRef:     "registry.io/app@sha256:123456",
			expectedRepo: "registry.io/app",
			expectedTag:  "sha256:123456",
		},
		{
			name:         "image_with_registry_and_port",
			imageRef:     "localhost:8080/myapp:v1.0",
			expectedRepo: "localhost:8080/myapp",
			expectedTag:  "v1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, tag := parseImageComponents(tt.imageRef)
			g.Expect(repo).To(gomega.Equal(tt.expectedRepo))
			g.Expect(tag).To(gomega.Equal(tt.expectedTag))
		})
	}
}

func TestApplyCRDIfNotExists(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name        string
		resource    string
		apiVersion  string
		yamlPath    string
		setupCRD    bool
		expectError bool
	}{
		{
			name:        "crd_does_not_exist",
			resource:    "appprojects",
			apiVersion:  "argoproj.io/v1alpha1",
			yamlPath:    "charts/openshift-gitops-operator/templates/crds/appprojects.argoproj.io.crd.yaml",
			setupCRD:    false,
			expectError: false, // Should succeed with real CRD
		},
		{
			name:        "crd_already_exists",
			resource:    "applications",
			apiVersion:  "argoproj.io/v1alpha1",
			yamlPath:    "charts/openshift-gitops-operator/templates/crds/applications.argoproj.io.crd.yaml",
			setupCRD:    true,
			expectError: false, // Should handle existing CRD gracefully
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
				Config: getTestEnv().Config,
			}

			// Setup existing CRD if needed
			if tt.setupCRD {
				crd := &apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{
						Name: "applications.argoproj.io",
					},
					Spec: apiextensionsv1.CustomResourceDefinitionSpec{
						Group: "argoproj.io",
						Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
							Name:    "v1alpha1",
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
							Plural: "applications",
							Kind:   "Application",
						},
					},
				}
				err := reconciler.Create(context.TODO(), crd)
				if err != nil && !errors.IsAlreadyExists(err) {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			}

			err := reconciler.applyCRDIfNotExists(testFS, tt.resource, tt.apiVersion, tt.yamlPath)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}
		})
	}
}

func TestCopyEmbeddedToTemp(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name        string
		srcPath     string
		releaseName string
		expectError bool
	}{
		{
			name:        "copy_existing_chart",
			srcPath:     "charts/openshift-gitops-operator",
			releaseName: "test-release",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{}

			// Create temp directory
			tempDir, err := os.MkdirTemp("", "copy-test-*")
			g.Expect(err).ToNot(gomega.HaveOccurred())
			defer os.RemoveAll(tempDir)

			err = reconciler.copyEmbeddedToTemp(testFS, tt.srcPath, tempDir)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}
		})
	}
}

func TestTemplateAndApplyChart(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name        string
		chartPath   string
		namespace   string
		releaseName string
		expectError bool
	}{
		{
			name:        "existing_chart",
			chartPath:   "charts/dep-crds",
			namespace:   "test-ns",
			releaseName: "test-release",
			expectError: true, // Will fail due to missing Chart.yaml but won't panic
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
			}

			err := reconciler.templateAndApplyChart(tt.chartPath, tt.namespace, tt.releaseName)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}
		})
	}
}

func TestApplyCRDIfNotExists_DirectCRDCheck(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name        string
		crdName     string
		resource    string
		apiVersion  string
		setupCRD    bool
		expectError bool
	}{
		{
			name:        "skip_when_crd_exists_directly",
			crdName:     "testresources.test.example.com",
			resource:    "testresources",
			apiVersion:  "test.example.com/v1",
			setupCRD:    true,
			expectError: false, // Should skip installation without error
		},
		{
			name:        "install_when_crd_not_exists",
			crdName:     "nonexistent.test.example.com",
			resource:    "nonexistent",
			apiVersion:  "test.example.com/v1",
			setupCRD:    false,
			expectError: true, // Error because yaml file doesn't exist
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
				Config: getTestEnv().Config,
			}

			// Cleanup before test
			existingCRD := &apiextensionsv1.CustomResourceDefinition{}
			err := reconciler.Get(context.TODO(), types.NamespacedName{Name: tt.crdName}, existingCRD)
			if err == nil {
				_ = reconciler.Delete(context.TODO(), existingCRD)
			}

			// Setup CRD if specified
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
							Plural: tt.resource,
							Kind:   "TestResource",
						},
					},
				}
				err := reconciler.Create(context.TODO(), crd)
				if err != nil && !errors.IsAlreadyExists(err) {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			}

			// Run the function with a non-existent yaml path
			// When CRD exists, it should skip without needing to read the yaml
			err = reconciler.applyCRDIfNotExists(testFS, tt.resource, tt.apiVersion, "charts/nonexistent.yaml")

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			// Cleanup
			if tt.setupCRD {
				_ = reconciler.Delete(context.TODO(), &apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{Name: tt.crdName},
				})
			}
		})
	}
}

func TestApplyManifestSkipsPreExistingResources(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: getTestEnv().Client,
		Config: getTestEnv().Config,
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "apply-manifest-test-"}}
	err := reconciler.Create(context.TODO(), ns)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "failed to create test namespace")
	testNS := ns.Name
	defer func() {
		if delErr := reconciler.Delete(context.TODO(), ns); delErr != nil {
			t.Logf("warning: failed to delete test namespace %s: %v", testNS, delErr)
		}
	}()

	t.Run("creates new resource with gitopsaddon label", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("v1")
		obj.SetKind("ConfigMap")
		obj.SetName("test-new-cm")
		obj.SetNamespace(testNS)
		if err := unstructured.SetNestedStringMap(obj.Object, map[string]string{"key": "val"}, "data"); err != nil {
			t.Fatalf("failed to set nested string map: %v", err)
		}

		err := reconciler.applyManifest(obj)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		created := &unstructured.Unstructured{}
		created.SetAPIVersion("v1")
		created.SetKind("ConfigMap")
		t.Cleanup(func() {
			if err := reconciler.Delete(context.TODO(), created); err != nil {
				t.Errorf("failed to clean up ConfigMap test-new-cm: %v", err)
			}
		})

		err = reconciler.Get(context.TODO(), types.NamespacedName{Name: "test-new-cm", Namespace: testNS}, created)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(created.GetLabels()["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))
	})

	t.Run("skips pre-existing resource without gitopsaddon label", func(t *testing.T) {
		preExisting := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pre-existing-cm",
				Namespace: testNS,
			},
			Data: map[string]string{"original": "data"},
		}
		err := reconciler.Create(context.TODO(), preExisting)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		t.Cleanup(func() {
			if err := reconciler.Delete(context.TODO(), preExisting); err != nil {
				t.Errorf("failed to clean up ConfigMap pre-existing-cm: %v", err)
			}
		})

		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("v1")
		obj.SetKind("ConfigMap")
		obj.SetName("pre-existing-cm")
		obj.SetNamespace(testNS)
		if err := unstructured.SetNestedStringMap(obj.Object, map[string]string{"new": "data"}, "data"); err != nil {
			t.Fatalf("failed to set nested string map: %v", err)
		}

		err = reconciler.applyManifest(obj)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		existing := &corev1.ConfigMap{}
		err = reconciler.Get(context.TODO(), types.NamespacedName{Name: "pre-existing-cm", Namespace: testNS}, existing)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(existing.Data["original"]).To(gomega.Equal("data"))
		g.Expect(existing.Data).NotTo(gomega.HaveKey("new"))
		g.Expect(existing.Labels).NotTo(gomega.HaveKey("apps.open-cluster-management.io/gitopsaddon"))
	})

	t.Run("updates owned resource with gitopsaddon label", func(t *testing.T) {
		owned := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-cm",
				Namespace: testNS,
				Labels: map[string]string{
					"apps.open-cluster-management.io/gitopsaddon": "true",
				},
			},
			Data: map[string]string{"original": "data"},
		}
		err := reconciler.Create(context.TODO(), owned)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		t.Cleanup(func() {
			if err := reconciler.Delete(context.TODO(), owned); err != nil {
				t.Errorf("failed to clean up ConfigMap owned-cm: %v", err)
			}
		})

		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("v1")
		obj.SetKind("ConfigMap")
		obj.SetName("owned-cm")
		obj.SetNamespace(testNS)
		if err := unstructured.SetNestedStringMap(obj.Object, map[string]string{"updated": "data"}, "data"); err != nil {
			t.Fatalf("failed to set nested string map: %v", err)
		}

		err = reconciler.applyManifest(obj)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		existing := &corev1.ConfigMap{}
		err = reconciler.Get(context.TODO(), types.NamespacedName{Name: "owned-cm", Namespace: testNS}, existing)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(existing.Data["updated"]).To(gomega.Equal("data"))
		g.Expect(existing.Labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))
	})

	t.Run("skips resource with skip annotation", func(t *testing.T) {
		skipped := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "skip-cm",
				Namespace: testNS,
				Labels: map[string]string{
					"apps.open-cluster-management.io/gitopsaddon": "true",
				},
				Annotations: map[string]string{
					"gitops-addon.open-cluster-management.io/skip": "true",
				},
			},
			Data: map[string]string{"original": "data"},
		}
		err := reconciler.Create(context.TODO(), skipped)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		t.Cleanup(func() {
			if err := reconciler.Delete(context.TODO(), skipped); err != nil {
				t.Errorf("failed to clean up ConfigMap skip-cm: %v", err)
			}
		})

		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("v1")
		obj.SetKind("ConfigMap")
		obj.SetName("skip-cm")
		obj.SetNamespace(testNS)
		if err := unstructured.SetNestedStringMap(obj.Object, map[string]string{"updated": "data"}, "data"); err != nil {
			t.Fatalf("failed to set nested string map: %v", err)
		}

		err = reconciler.applyManifest(obj)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		existing := &corev1.ConfigMap{}
		err = reconciler.Get(context.TODO(), types.NamespacedName{Name: "skip-cm", Namespace: testNS}, existing)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(existing.Data["original"]).To(gomega.Equal("data"))
		g.Expect(existing.Data).NotTo(gomega.HaveKey("updated"))
	})
}
