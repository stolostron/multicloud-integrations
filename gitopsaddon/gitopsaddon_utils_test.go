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
	"strings"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	utils "open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Test embedded file system for tests
//
//go:embed all:charts/openshift-gitops-operator/**
var testFS embed.FS

// newChartTestClient returns an isolated fake client for the chart tests, so cluster-scoped
// resources they create (CRDs, ClusterRoles) cannot leak into other tests.
func newChartTestClient(t *testing.T) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}

	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

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

func TestTemplateAndApplyChartLabelsAllResources(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: newChartTestClient(t),
		Config: getTestEnv().Config,
		AddonConfig: &utils.GitOpsAddonConfig{
			OperatorImages: map[string]string{
				utils.EnvGitOpsOperatorImage: "quay.io/test/operator:latest",
			},
		},
	}

	testNS := "label-inject-test"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}
	err := reconciler.Create(context.TODO(), ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("failed to create test namespace: %v", err)
	}
	defer func() {
		_ = reconciler.Delete(context.TODO(), ns)
	}()

	err = reconciler.templateAndApplyChart("charts/openshift-gitops-operator", testNS, "openshift-gitops-operator")
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify Deployment has the gitopsaddon label
	deploy := &unstructured.Unstructured{}
	deploy.SetAPIVersion("apps/v1")
	deploy.SetKind("Deployment")
	err = reconciler.Get(context.TODO(), types.NamespacedName{
		Name: "openshift-gitops-operator-controller-manager", Namespace: testNS,
	}, deploy)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "Deployment should exist")
	g.Expect(deploy.GetLabels()).To(gomega.HaveKeyWithValue(
		"apps.open-cluster-management.io/gitopsaddon", "true",
	), "Deployment must have gitopsaddon label")

	// Verify ServiceAccount has the gitopsaddon label
	sa := &unstructured.Unstructured{}
	sa.SetAPIVersion("v1")
	sa.SetKind("ServiceAccount")
	err = reconciler.Get(context.TODO(), types.NamespacedName{
		Name: "openshift-gitops-operator-controller-manager", Namespace: testNS,
	}, sa)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "ServiceAccount should exist")
	g.Expect(sa.GetLabels()).To(gomega.HaveKeyWithValue(
		"apps.open-cluster-management.io/gitopsaddon", "true",
	), "ServiceAccount must have gitopsaddon label")

	// Verify a ConfigMap deployed by the chart also has the label
	cm := &unstructured.Unstructured{}
	cm.SetAPIVersion("v1")
	cm.SetKind("ConfigMap")
	err = reconciler.Get(context.TODO(), types.NamespacedName{
		Name: "openshift-gitops-operator-manager-config", Namespace: testNS,
	}, cm)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "ConfigMap should exist")
	g.Expect(cm.GetLabels()).To(gomega.HaveKeyWithValue(
		"apps.open-cluster-management.io/gitopsaddon", "true",
	), "ConfigMap must have gitopsaddon label")
}

func TestCRDShapeMatches(t *testing.T) {
	g := gomega.NewWithT(t)

	newCRD := func(scope string, versions ...string) *unstructured.Unstructured {
		versionList := make([]interface{}, 0, len(versions))
		for _, name := range versions {
			versionList = append(versionList, map[string]interface{}{
				"name":    name,
				"served":  true,
				"storage": name == versions[0],
			})
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"spec": map[string]interface{}{
				"group":    "argoproj.io",
				"scope":    scope,
				"names":    map[string]interface{}{"plural": "argocds", "kind": "ArgoCD"},
				"versions": versionList,
			},
		}}
	}

	g.Expect(crdShapeMatches(newCRD("Namespaced", "v1alpha1", "v1beta1"), newCRD("Namespaced", "v1alpha1", "v1beta1"))).
		To(gomega.BeTrue())
	g.Expect(crdShapeMatches(newCRD("Namespaced", "v1alpha1", "v1beta1"), newCRD("Namespaced", "v1beta1", "v1alpha1"))).
		To(gomega.BeTrue(), "version order must not matter")
	g.Expect(crdShapeMatches(newCRD("Namespaced", "v1alpha1"), newCRD("Namespaced", "v1alpha1", "v1beta1"))).
		To(gomega.BeFalse(), "a version added by someone else must trigger a re-apply")
	g.Expect(crdShapeMatches(newCRD("Namespaced", "v1beta1"), newCRD("Cluster", "v1beta1"))).
		To(gomega.BeFalse(), "a scope change must trigger a re-apply")
}

func TestChartValuesImageHandling(t *testing.T) {
	g := gomega.NewWithT(t)

	cases := []struct {
		name    string
		image   string
		wantTag string
	}{
		{name: "digest", image: "registry.example.com/team/operator@sha256:abc123", wantTag: "sha256:abc123"},
		{name: "tag", image: "registry.example.com/team/operator:v1.2.3", wantTag: "v1.2.3"},
		{name: "no_tag", image: "registry.example.com/team/operator", wantTag: "latest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reconciler := &GitopsAddonReconciler{
				Client: getTestEnv().Client,
				AddonConfig: &utils.GitOpsAddonConfig{
					OperatorImages: map[string]string{utils.EnvGitOpsOperatorImage: tc.image},
				},
			}

			values := reconciler.chartValues("charts/openshift-gitops-operator")
			image, ok := values["image"].(map[string]interface{})
			g.Expect(ok).To(gomega.BeTrue())
			manager, ok := image["manager"].(map[string]interface{})
			g.Expect(ok).To(gomega.BeTrue())
			g.Expect(manager["tag"]).To(gomega.Equal(tc.wantTag))
		})
	}

	t.Run("extension_image_maps_to_chart_keys", func(t *testing.T) {
		reconciler := &GitopsAddonReconciler{
			Client: getTestEnv().Client,
			AddonConfig: &utils.GitOpsAddonConfig{
				OperatorImages: map[string]string{
					utils.EnvGitOpsOperatorImage:  "registry.example.com/team/operator:v1",
					utils.EnvArgoCDExtensionImage: "registry.example.com/team/extensions:v1",
				},
			},
		}

		values := reconciler.chartValues("charts/openshift-gitops-operator")
		envValues, ok := values["operatorImageEnv"].(map[string]interface{})
		g.Expect(ok).To(gomega.BeTrue())
		g.Expect(envValues["ARGOCD_ROLLOUTS_IMAGE"]).To(gomega.Equal("registry.example.com/team/extensions:v1"))
		g.Expect(envValues["ROLLOUTS_EXTENSION_IMAGE"]).To(gomega.Equal("registry.example.com/team/extensions:v1"))
	})
}

func TestChartValuesUnknownChartPath(t *testing.T) {
	g := gomega.NewWithT(t)
	reconciler := &GitopsAddonReconciler{Client: getTestEnv().Client, AddonConfig: &utils.GitOpsAddonConfig{}}
	g.Expect(reconciler.chartValues("charts/other")).To(gomega.BeEmpty())
}

func TestTemplateAndApplyChartCreatesLabeledPullSecret(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: newChartTestClient(t),
		Config: getTestEnv().Config,
		AddonConfig: &utils.GitOpsAddonConfig{
			OperatorImages: map[string]string{utils.EnvGitOpsOperatorImage: "quay.io/test/operator:latest"},
		},
	}

	// getAddonImagePullSecretData reads from the addon pod's own namespace, which is the pod's
	// service-account namespace when running inside a cluster (e.g. the Prow test pod) and the
	// default otherwise.
	addonNS := utils.GetComponentNamespace("open-cluster-management-agent-addon")
	addonNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: addonNS}}
	if err := reconciler.Create(context.TODO(), addonNamespace); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("create addon namespace: %v", err)
	}

	pullSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: addonImagePullSecretName, Namespace: addonNS},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.redhat.io":{"auth":"dGVzdA=="}}}`),
		},
	}
	if err := reconciler.Create(context.TODO(), pullSecret); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("create addon pull secret: %v", err)
	}
	defer func() { _ = reconciler.Delete(context.TODO(), pullSecret) }()

	testNS := "pull-secret-chart-test"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}
	if err := reconciler.Create(context.TODO(), ns); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("create test namespace: %v", err)
	}
	defer func() { _ = reconciler.Delete(context.TODO(), ns) }()

	g.Expect(reconciler.templateAndApplyChart("charts/openshift-gitops-operator", testNS, "openshift-gitops-operator")).
		ToNot(gomega.HaveOccurred())

	created := &corev1.Secret{}
	g.Expect(reconciler.Get(context.TODO(), types.NamespacedName{
		Name: "redhat-registry-pull-secret", Namespace: testNS,
	}, created)).ToNot(gomega.HaveOccurred(), "the chart must create the operator-propagated pull secret")
	g.Expect(created.Labels).To(gomega.HaveKeyWithValue("operator.argoproj.io/propagate-image-pull-secret", "true"))
}

func TestChartValuesMirroredImagesLeaveNoExternalRegistryReferences(t *testing.T) {
	g := gomega.NewWithT(t)

	// Mirror every known image at the registry-host prefix, exactly like the disconnected
	// image mirroring controller rewrites AddOnDeploymentConfig values. If any chart image key
	// is missing from the configuration, the chart falls back to its own registry.redhat.io
	// default and the operator Deployment still references the external registry.
	mirrored := map[string]string{}
	for key, value := range utils.DefaultOperatorImages {
		mirrored[key] = strings.Replace(value, "registry.redhat.io", "mirror.example.com", 1)
	}

	reconciler := &GitopsAddonReconciler{
		Client: newChartTestClient(t),
		Config: getTestEnv().Config,
		AddonConfig: &utils.GitOpsAddonConfig{
			OperatorImages: mirrored,
		},
	}

	testNS := "mirrored-images-test"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}
	if err := reconciler.Create(context.TODO(), ns); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("create test namespace: %v", err)
	}
	defer func() { _ = reconciler.Delete(context.TODO(), ns) }()

	g.Expect(reconciler.templateAndApplyChart("charts/openshift-gitops-operator", testNS, "openshift-gitops-operator")).
		ToNot(gomega.HaveOccurred())

	deploy := &unstructured.Unstructured{}
	deploy.SetAPIVersion("apps/v1")
	deploy.SetKind("Deployment")
	g.Expect(reconciler.Get(context.TODO(), types.NamespacedName{
		Name: "openshift-gitops-operator-controller-manager", Namespace: testNS,
	}, deploy)).ToNot(gomega.HaveOccurred())

	containers, found, err := unstructured.NestedSlice(deploy.Object, "spec", "template", "spec", "containers")
	g.Expect(err).ToNot(gomega.HaveOccurred())
	g.Expect(found).To(gomega.BeTrue())

	for _, rawContainer := range containers {
		container, ok := rawContainer.(map[string]interface{})
		g.Expect(ok).To(gomega.BeTrue())

		image, _, _ := unstructured.NestedString(container, "image")
		g.Expect(image).NotTo(gomega.ContainSubstring("registry.redhat.io"),
			"mirrored operator image must not reference the external registry")

		env, _, err := unstructured.NestedSlice(container, "env")
		g.Expect(err).ToNot(gomega.HaveOccurred())
		for _, rawEnv := range env {
			entry, ok := rawEnv.(map[string]interface{})
			g.Expect(ok).To(gomega.BeTrue())
			name, _, _ := unstructured.NestedString(entry, "name")
			value, _, _ := unstructured.NestedString(entry, "value")
			g.Expect(value).NotTo(gomega.ContainSubstring("registry.redhat.io"),
				"env var %s still references the external registry; every chart image key must be configurable", name)
		}
	}
}

func TestTemplateAndApplyChartIsIdempotent(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Client: newChartTestClient(t),
		Config: getTestEnv().Config,
		AddonConfig: &utils.GitOpsAddonConfig{
			OperatorImages: map[string]string{utils.EnvGitOpsOperatorImage: "quay.io/test/operator:latest"},
		},
	}

	testNS := "chart-idempotent-test"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}
	if err := reconciler.Create(context.TODO(), ns); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("create test namespace: %v", err)
	}
	defer func() { _ = reconciler.Delete(context.TODO(), ns) }()

	g.Expect(reconciler.templateAndApplyChart("charts/openshift-gitops-operator", testNS, "openshift-gitops-operator")).
		ToNot(gomega.HaveOccurred())
	g.Expect(reconciler.templateAndApplyChart("charts/openshift-gitops-operator", testNS, "openshift-gitops-operator")).
		ToNot(gomega.HaveOccurred(), "a second apply must skip unchanged CRDs and succeed")
}

func TestApplyRenderedManifestsSkipsInvalidEntries(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{Client: getTestEnv().Client, Config: getTestEnv().Config}
	testNS := "rendered-manifests-test"
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}
	if err := reconciler.Create(context.TODO(), ns); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
	defer func() { _ = reconciler.Delete(context.TODO(), ns) }()

	reconciler.applyRenderedManifests(map[string]string{
		"empty.yaml":  "",
		"notes.txt":   "NOTES.txt",
		"bad.yaml":    "metadata: [unclosed",
		"nokind.yaml": "apiVersion: v1\nmetadata:\n  name: no-kind\n",
		"mixed.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: rendered-cm-a\n" +
			"\n---\n" +
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: rendered-cm-b\n",
	}, testNS)

	for _, name := range []string{"rendered-cm-a", "rendered-cm-b"} {
		cm := &corev1.ConfigMap{}
		g.Expect(reconciler.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: testNS}, cm)).
			ToNot(gomega.HaveOccurred(), "valid documents must be applied")
		g.Expect(cm.Labels).To(gomega.HaveKeyWithValue("apps.open-cluster-management.io/gitopsaddon", "true"))
	}
}

func TestApplyCRDYAMLHandlesInvalidInput(t *testing.T) {
	reconciler := &GitopsAddonReconciler{Client: getTestEnv().Client, Config: getTestEnv().Config}

	// Invalid YAML, empty content and a document without a kind must all be skipped quietly.
	reconciler.applyCRDYAML("bad", []byte("goodness: [not-closed"))
	reconciler.applyCRDYAML("empty", []byte("   \n"))
	reconciler.applyCRDYAML("nokind", []byte("apiVersion: apiextensions.k8s.io/v1\n"))
}
