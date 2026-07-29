/*
Copyright 2026.

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

package pullmodelconfig

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("failed to build test scheme: %v", err)
	}

	return s
}

func objMeta(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace}
}

func TestResolveNamespace_EnvVar(t *testing.T) {
	t.Setenv(podNamespaceEnvVar, "some-namespace")

	ns, err := ResolveNamespace()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ns != "some-namespace" {
		t.Fatalf("expected 'some-namespace', got %q", ns)
	}
}

func TestResolveNamespace_FallbackFile(t *testing.T) {
	t.Setenv(podNamespaceEnvVar, "")

	// Can't easily override the hardcoded service account file path without touching
	// production code, so just confirm the error path is sane when neither is available
	// (this test environment has no POD_NAMESPACE and, almost certainly, no mounted
	// service account namespace file).
	if _, err := os.Stat(serviceAccountNamespaceFile); err == nil {
		t.Skip("service account namespace file exists in this environment, skipping negative-path check")
	}

	_, err := ResolveNamespace()
	if err == nil {
		t.Fatal("expected an error when neither POD_NAMESPACE nor the service account file are available")
	}
}

func TestLoadOrCreate_CreatesDefaultWhenMissing(t *testing.T) {
	t.Setenv(podNamespaceEnvVar, "test-ns")

	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	cfg, err := LoadOrCreate(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.PullModel.Basic.Disabled {
		t.Fatal("expected default config to have basic pull model NOT disabled")
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: ConfigMapName, Namespace: "test-ns"}, cm); err != nil {
		t.Fatalf("expected ConfigMap to have been created: %v", err)
	}

	if _, ok := cm.Data[ConfigMapDataKey]; !ok {
		t.Fatalf("expected created ConfigMap to have data key %q", ConfigMapDataKey)
	}
}

func TestLoadOrCreate_ReadsExisting(t *testing.T) {
	t.Setenv(podNamespaceEnvVar, "test-ns")

	scheme := newTestScheme(t)
	existing := &corev1.ConfigMap{
		ObjectMeta: objMeta(ConfigMapName, "test-ns"),
		Data: map[string]string{
			ConfigMapDataKey: "pullModel:\n  basic:\n    disabled: true\n",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	cfg, err := LoadOrCreate(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.PullModel.Basic.Disabled {
		t.Fatal("expected existing config's disabled:true to be honored")
	}
}

func TestLoadOrCreate_MissingDataKeyDefaultsGracefully(t *testing.T) {
	t.Setenv(podNamespaceEnvVar, "test-ns")

	scheme := newTestScheme(t)
	existing := &corev1.ConfigMap{
		ObjectMeta: objMeta(ConfigMapName, "test-ns"),
		Data:       map[string]string{},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	cfg, err := LoadOrCreate(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.PullModel.Basic.Disabled {
		t.Fatal("expected missing data key to default to not-disabled")
	}
}

func TestLoadOrCreate_MalformedYAMLErrors(t *testing.T) {
	t.Setenv(podNamespaceEnvVar, "test-ns")

	scheme := newTestScheme(t)
	existing := &corev1.ConfigMap{
		ObjectMeta: objMeta(ConfigMapName, "test-ns"),
		Data: map[string]string{
			ConfigMapDataKey: "not: [valid: yaml",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	if _, err := LoadOrCreate(context.Background(), c); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}
