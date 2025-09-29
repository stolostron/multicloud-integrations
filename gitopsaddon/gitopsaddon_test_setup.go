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

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	testClient client.Client
	testConfig *rest.Config
	testScheme *runtime.Scheme
	testEnv    *TestEnvironment
)

// setupTestEnv initializes the test environment using fake client for portability
func setupTestEnv() {
	if testEnv != nil {
		return
	}

	// Setup scheme
	testScheme = runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(testScheme))

	// Create initial test resources that the controller expects
	initialObjects := createInitialTestResources()

	// Use fake client instead of envtest for portability
	testClient = fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(initialObjects...).
		Build()

	// Create a fake config (not used by fake client but required by interface)
	testConfig = &rest.Config{
		Host: "fake://test",
	}

	testEnv = &TestEnvironment{
		Client: testClient,
		Config: testConfig,
		Scheme: testScheme,
	}
}

// teardownTestEnv cleans up the test environment
func teardownTestEnv() {
	// No cleanup needed for fake client
}

// TestMain sets up and tears down the test environment
func TestMain(m *testing.M) {
	setupTestEnv()

	code := m.Run()

	teardownTestEnv()
	os.Exit(code)
}

// TestEnvironment provides access to test environment components
type TestEnvironment struct {
	Client client.Client
	Config *rest.Config
	Scheme *runtime.Scheme
}

// getTestEnv returns a configured test environment
func getTestEnv() *TestEnvironment {
	setupTestEnv()
	return testEnv
}

// createInitialTestResources creates the resources that the controller expects to find
func createInitialTestResources() []client.Object {
	// Create ArgoCD CRs in multiple namespaces that tests expect
	argoCDDefault := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1beta1",
			"kind":       "ArgoCD",
			"metadata": map[string]interface{}{
				"name":      "openshift-gitops",
				"namespace": "openshift-gitops",
			},
			"spec": map[string]interface{}{
				"server": map[string]interface{}{
					"route": map[string]interface{}{
						"enabled": true,
					},
				},
			},
		},
	}

	// Create ArgoCD CR in test namespace as well
	argoCDTest := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1beta1",
			"kind":       "ArgoCD",
			"metadata": map[string]interface{}{
				"name":      "openshift-gitops",
				"namespace": "test-ns",
			},
			"spec": map[string]interface{}{
				"server": map[string]interface{}{
					"route": map[string]interface{}{
						"enabled": true,
					},
				},
			},
		},
	}

	// Create ArgoCD CR for test-gitops-ns
	argoCDTestGitops := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1beta1",
			"kind":       "ArgoCD",
			"metadata": map[string]interface{}{
				"name":      "openshift-gitops",
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
	}

	// Create test namespaces
	testGitopsNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-gitops",
		},
	}

	testOperatorNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-gitops-operator",
		},
	}

	testNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
		},
	}

	testGitopsTestNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-gitops-ns",
		},
	}

	// Create initial password secrets that ensureArgoCDRedisSecret looks for
	initialPassword := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-redis-initial-password",
			Namespace: "openshift-gitops",
		},
		Data: map[string][]byte{
			"admin.password": []byte("testpassword"),
		},
	}

	initialPasswordTest := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-redis-initial-password",
			Namespace: "test-gitops-ns",
		},
		Data: map[string][]byte{
			"admin.password": []byte("testpassword"),
		},
	}

	// Create default ServiceAccounts that tests expect
	defaultSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "openshift-gitops",
		},
	}

	defaultSATest := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-ns",
		},
	}

	defaultSATestGitops := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-gitops-ns",
		},
	}

	// Create ServiceAccounts in ALL namespaces to ensure no hanging
	serviceAccountNames := []string{
		"openshift-gitops-argocd-redis",
		"openshift-gitops-argocd-application-controller",
		"argocd-agent-agent",
	}

	namespaces := []string{"openshift-gitops", "test-ns", "test-gitops-ns", "openshift-gitops-operator"}

	var serviceAccounts []client.Object
	for _, name := range serviceAccountNames {
		for _, ns := range namespaces {
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
			}
			serviceAccounts = append(serviceAccounts, sa)
		}
	}

	allObjects := []client.Object{
		argoCDDefault, argoCDTest, argoCDTestGitops,
		testGitopsNS, testOperatorNS, testNS, testGitopsTestNS,
		initialPassword, initialPasswordTest,
		defaultSA, defaultSATest, defaultSATestGitops,
	}

	// Add all ServiceAccounts
	allObjects = append(allObjects, serviceAccounts...)

	return allObjects
}

// cleanupTestResources cleans up resources created during tests
func cleanupTestResources(ctx context.Context, c client.Client) error {
	// This function can be used to clean up resources between tests
	// Implementation depends on specific cleanup needs
	return nil
}
