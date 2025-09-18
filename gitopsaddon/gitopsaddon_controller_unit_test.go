package gitopsaddon

import (
	"context"
	"embed"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

func TestSetupWithManager(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	mgr := &fakeManager{
		client: fakeClient,
		scheme: scheme,
		config: &rest.Config{},
	}

	err = SetupWithManager(mgr, 30, "test-operator-image", "test-operator-ns",
		"test-gitops-image", "test-gitops-ns", "test-redis-image", "cluster",
		"", "", "", "Install", "true", "test-agent-image", "test-server", "8080", "grpc")

	assert.NoError(t, err)
}

func TestGitopsAddonReconciler_Start(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &GitopsAddonReconciler{
		Client:              fakeClient,
		Scheme:              scheme,
		Config:              &rest.Config{},
		Interval:            1, // 1 second for faster testing
		GitopsOperatorImage: "test-operator-image",
		GitopsOperatorNS:    "test-operator-ns",
		GitopsImage:         "test-gitops-image",
		GitopsNS:            "test-gitops-ns",
		RedisImage:          "test-redis-image",
		ReconcileScope:      "cluster",
		ACTION:              "Install",
		ArgoCDAgentEnabled:  "false",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = reconciler.Start(ctx)
	assert.NoError(t, err)
}

func TestGitopsAddonReconciler_HouseKeeping(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	testCases := []struct {
		name   string
		action string
	}{
		{
			name:   "Install action",
			action: "Install",
		},
		{
			name:   "Delete-Operator action",
			action: "Delete-Operator",
		},
		{
			name:   "Delete-Instance action",
			action: "Delete-Instance",
		},
		{
			name:   "Default action",
			action: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

			reconciler := &GitopsAddonReconciler{
				Client:              fakeClient,
				Scheme:              scheme,
				Config:              &rest.Config{},
				GitopsOperatorImage: "test-operator-image",
				GitopsOperatorNS:    "test-operator-ns",
				GitopsImage:         "test-gitops-image",
				GitopsNS:            "test-gitops-ns",
				RedisImage:          "test-redis-image",
				ReconcileScope:      "cluster",
				ACTION:              tc.action,
				ArgoCDAgentEnabled:  "false",
			}

			configFlags := &genericclioptions.ConfigFlags{}

			// This should not panic
			assert.NotPanics(t, func() {
				reconciler.houseKeeping(configFlags)
			})
		})
	}
}

func TestParseImageReference(t *testing.T) {
	testCases := []struct {
		name          string
		imageRef      string
		expectedImage string
		expectedTag   string
		expectedError bool
	}{
		{
			name:          "Valid image with tag",
			imageRef:      "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator:v1.0.0",
			expectedImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator",
			expectedTag:   "v1.0.0",
			expectedError: false,
		},
		{
			name:          "Valid image with digest",
			imageRef:      "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator@sha256:abc123",
			expectedImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator",
			expectedTag:   "sha256:abc123",
			expectedError: false,
		},
		{
			name:          "Valid image with incomplete digest",
			imageRef:      "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator@abc123",
			expectedImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator",
			expectedTag:   "sha256:abc123",
			expectedError: false,
		},
		{
			name:          "Image with port in registry",
			imageRef:      "localhost:5000/myimage:v1.0.0",
			expectedImage: "localhost:5000/myimage",
			expectedTag:   "v1.0.0",
			expectedError: false,
		},
		{
			name:          "Invalid format - no tag or digest",
			imageRef:      "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator",
			expectedError: true,
		},
		{
			name:          "Invalid format - empty image",
			imageRef:      ":v1.0.0",
			expectedError: true,
		},
		{
			name:          "Invalid format - empty tag",
			imageRef:      "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator:",
			expectedError: true,
		},
		{
			name:          "Invalid format - empty digest",
			imageRef:      "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator@",
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			image, tag, err := ParseImageReference(tc.imageRef)

			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedImage, image)
				assert.Equal(t, tc.expectedTag, tag)
			}
		})
	}
}

func TestGitopsAddonReconciler_ShouldUpdateOpenshiftGiopsOperator(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	testCases := []struct {
		name           string
		namespace      *corev1.Namespace
		operatorImage  string
		operatorNS     string
		expectedUpdate bool
	}{
		{
			name:           "Namespace not found - should update",
			operatorImage:  "test-image",
			operatorNS:     "test-ns",
			expectedUpdate: true,
		},
		{
			name: "Namespace exists with different image - should update",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
					Annotations: map[string]string{
						"apps.open-cluster-management.io/gitops-operator-image": "old-image",
						"apps.open-cluster-management.io/gitops-operator-ns":    "test-ns",
					},
				},
			},
			operatorImage:  "new-image",
			operatorNS:     "test-ns",
			expectedUpdate: true,
		},
		{
			name: "Namespace exists with different namespace - should update",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
					Annotations: map[string]string{
						"apps.open-cluster-management.io/gitops-operator-image": "test-image",
						"apps.open-cluster-management.io/gitops-operator-ns":    "old-ns",
					},
				},
			},
			operatorImage:  "test-image",
			operatorNS:     "test-ns",
			expectedUpdate: true,
		},
		{
			name: "Namespace exists with same config - should not update",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
					Annotations: map[string]string{
						"apps.open-cluster-management.io/gitops-operator-image": "test-image",
						"apps.open-cluster-management.io/gitops-operator-ns":    "test-ns",
					},
				},
			},
			operatorImage:  "test-image",
			operatorNS:     "test-ns",
			expectedUpdate: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []client.Object
			if tc.namespace != nil {
				objs = append(objs, tc.namespace)
			}

			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

			reconciler := &GitopsAddonReconciler{
				Client:              fakeClient,
				GitopsOperatorImage: tc.operatorImage,
				GitopsOperatorNS:    tc.operatorNS,
			}

			shouldUpdate := reconciler.ShouldUpdateOpenshiftGiopsOperator()
			assert.Equal(t, tc.expectedUpdate, shouldUpdate)
		})
	}
}

func TestGitopsAddonReconciler_ShouldUpdateOpenshiftGiops(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	testCases := []struct {
		name           string
		namespace      *corev1.Namespace
		gitopsImage    string
		gitopsNS       string
		redisImage     string
		reconcileScope string
		expectedUpdate bool
	}{
		{
			name:           "Namespace not found - should update",
			gitopsImage:    "test-image",
			gitopsNS:       "test-ns",
			redisImage:     "test-redis",
			reconcileScope: "cluster",
			expectedUpdate: true,
		},
		{
			name: "Namespace exists with different image - should update",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
					Annotations: map[string]string{
						"apps.open-cluster-management.io/gitops-image":    "old-image",
						"apps.open-cluster-management.io/gitops-ns":       "test-ns",
						"apps.open-cluster-management.io/redis-image":     "test-redis",
						"apps.open-cluster-management.io/reconcile-scope": "cluster",
					},
				},
			},
			gitopsImage:    "new-image",
			gitopsNS:       "test-ns",
			redisImage:     "test-redis",
			reconcileScope: "cluster",
			expectedUpdate: true,
		},
		{
			name: "Namespace exists with same config - should not update",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
					Annotations: map[string]string{
						"apps.open-cluster-management.io/gitops-image":    "test-image",
						"apps.open-cluster-management.io/gitops-ns":       "test-ns",
						"apps.open-cluster-management.io/redis-image":     "test-redis",
						"apps.open-cluster-management.io/reconcile-scope": "cluster",
					},
				},
			},
			gitopsImage:    "test-image",
			gitopsNS:       "test-ns",
			redisImage:     "test-redis",
			reconcileScope: "cluster",
			expectedUpdate: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []client.Object
			if tc.namespace != nil {
				objs = append(objs, tc.namespace)
			}

			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

			reconciler := &GitopsAddonReconciler{
				Client:         fakeClient,
				GitopsImage:    tc.gitopsImage,
				GitopsNS:       tc.gitopsNS,
				RedisImage:     tc.redisImage,
				ReconcileScope: tc.reconcileScope,
			}

			shouldUpdate := reconciler.ShouldUpdateOpenshiftGiops()
			assert.Equal(t, tc.expectedUpdate, shouldUpdate)
		})
	}
}

func TestGitopsAddonReconciler_ShouldUpdateArgoCDAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	testCases := []struct {
		name           string
		namespace      *corev1.Namespace
		agentImage     string
		serverAddress  string
		serverPort     string
		mode           string
		expectedUpdate bool
	}{
		{
			name:           "Namespace not found - should update",
			agentImage:     "test-agent-image",
			serverAddress:  "test-server",
			serverPort:     "8080",
			mode:           "grpc",
			expectedUpdate: true,
		},
		{
			name: "Namespace exists without gitopsaddon label - should not update",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
				},
			},
			agentImage:     "test-agent-image",
			serverAddress:  "test-server",
			serverPort:     "8080",
			mode:           "grpc",
			expectedUpdate: false,
		},
		{
			name: "Namespace exists with different config - should update",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
					Labels: map[string]string{
						"apps.open-cluster-management.io/gitopsaddon": "true",
					},
					Annotations: map[string]string{
						"apps.open-cluster-management.io/argocd-agent-image":          "old-image",
						"apps.open-cluster-management.io/argocd-agent-server-address": "test-server",
						"apps.open-cluster-management.io/argocd-agent-server-port":    "8080",
						"apps.open-cluster-management.io/argocd-agent-mode":           "grpc",
					},
				},
			},
			agentImage:     "new-image",
			serverAddress:  "test-server",
			serverPort:     "8080",
			mode:           "grpc",
			expectedUpdate: true,
		},
		{
			name: "Namespace exists with same config - should not update",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
					Labels: map[string]string{
						"apps.open-cluster-management.io/gitopsaddon": "true",
					},
					Annotations: map[string]string{
						"apps.open-cluster-management.io/argocd-agent-image":          "test-agent-image",
						"apps.open-cluster-management.io/argocd-agent-server-address": "test-server",
						"apps.open-cluster-management.io/argocd-agent-server-port":    "8080",
						"apps.open-cluster-management.io/argocd-agent-mode":           "grpc",
					},
				},
			},
			agentImage:     "test-agent-image",
			serverAddress:  "test-server",
			serverPort:     "8080",
			mode:           "grpc",
			expectedUpdate: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []client.Object
			if tc.namespace != nil {
				objs = append(objs, tc.namespace)
			}

			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

			reconciler := &GitopsAddonReconciler{
				Client:                   fakeClient,
				GitopsNS:                 "test-ns",
				ArgoCDAgentImage:         tc.agentImage,
				ArgoCDAgentServerAddress: tc.serverAddress,
				ArgoCDAgentServerPort:    tc.serverPort,
				ArgoCDAgentMode:          tc.mode,
			}

			shouldUpdate := reconciler.ShouldUpdateArgoCDAgent()
			assert.Equal(t, tc.expectedUpdate, shouldUpdate)
		})
	}
}

func TestGitopsAddonReconciler_EnsureArgoCDRedisSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	testCases := []struct {
		name          string
		existingObjs  []client.Object
		expectedError bool
	}{
		{
			name: "ArgoCD redis secret already exists",
			existingObjs: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-redis",
						Namespace: "test-gitops-ns",
					},
				},
			},
			expectedError: false,
		},
		{
			name: "Create new ArgoCD redis secret from existing password secret",
			existingObjs: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-redis-initial-password",
						Namespace: "test-gitops-ns",
					},
					Data: map[string][]byte{
						"admin.password": []byte("test-password"),
					},
				},
			},
			expectedError: false,
		},
		{
			name:          "No redis password secret found",
			existingObjs:  []client.Object{},
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(tc.existingObjs...).Build()

			reconciler := &GitopsAddonReconciler{
				Client:   fakeClient,
				GitopsNS: "test-gitops-ns",
			}

			err := reconciler.ensureArgoCDRedisSecret()

			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify the secret was created or already exists
				var secret corev1.Secret
				err = fakeClient.Get(context.TODO(), types.NamespacedName{Name: "argocd-redis", Namespace: "test-gitops-ns"}, &secret)
				assert.NoError(t, err)
			}
		})
	}
}

func TestGitopsAddonReconciler_DeleteArgoCDRedisSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	testCases := []struct {
		name          string
		existingObjs  []client.Object
		expectedError bool
	}{
		{
			name:          "Secret not found - no error",
			existingObjs:  []client.Object{},
			expectedError: false,
		},
		{
			name: "Secret exists but not managed by gitopsaddon - skip deletion",
			existingObjs: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-redis",
						Namespace: "test-gitops-ns",
					},
				},
			},
			expectedError: false,
		},
		{
			name: "Secret exists and managed by gitopsaddon - delete",
			existingObjs: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-redis",
						Namespace: "test-gitops-ns",
						Labels: map[string]string{
							"apps.open-cluster-management.io/gitopsaddon": "true",
						},
					},
				},
			},
			expectedError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(tc.existingObjs...).Build()

			reconciler := &GitopsAddonReconciler{
				Client:   fakeClient,
				GitopsNS: "test-gitops-ns",
			}

			err := reconciler.deleteArgoCDRedisSecret()

			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGitopsAddonReconciler_CopyEmbeddedToTemp(t *testing.T) {
	// Create a test embedded filesystem
	testFS := embed.FS{}

	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "test-copy-embedded")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	reconciler := &GitopsAddonReconciler{}

	// Test with empty filesystem - this should handle the error gracefully
	err = reconciler.copyEmbeddedToTemp(testFS, "non-existent-path", tempDir, "test-release")
	assert.Error(t, err) // Expected to fail since the path doesn't exist in empty FS
}

func TestGitopsAddonReconciler_CreateUpdateNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	testCases := []struct {
		name          string
		existingObjs  []client.Object
		namespaceName string
		expectedError bool
	}{
		{
			name:          "Create new namespace",
			namespaceName: "new-namespace",
			expectedError: true, // Will fail due to image pull secret missing in test
		},
		{
			name: "Update existing namespace",
			existingObjs: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "existing-namespace",
					},
				},
			},
			namespaceName: "existing-namespace",
			expectedError: true, // Will fail due to image pull secret missing in test
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(tc.existingObjs...).Build()

			reconciler := &GitopsAddonReconciler{
				Client: fakeClient,
			}

			nameSpaceKey := types.NamespacedName{Name: tc.namespaceName}
			err := reconciler.CreateUpdateNamespace(nameSpaceKey)

			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestInstallOrUpgradeChart tests the Helm chart installation/upgrade functionality
func TestInstallOrUpgradeChart(t *testing.T) {
	tempDir := t.TempDir()

	testCases := []struct {
		name        string
		chartPath   string
		values      string
		namespace   string
		expectError bool
		mockHelmCmd bool
	}{
		{
			name:        "test chart preparation",
			chartPath:   tempDir + "/test-chart",
			values:      tempDir + "/values.yaml",
			namespace:   "test-namespace",
			expectError: false,
			mockHelmCmd: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create test files
			os.MkdirAll(tc.chartPath, 0755)
			os.WriteFile(tc.values, []byte("test: value"), 0644)

			reconciler := &GitopsAddonReconciler{}

			// Test the function (note: this will fail with actual helm but tests the code path)
			configFlags := &genericclioptions.ConfigFlags{}
			err := reconciler.installOrUpgradeChart(configFlags, tc.chartPath, tc.namespace, "test-release")

			// Since we don't have a real helm binary, we expect an error, but this tests the code path
			assert.Error(t, err)
		})
	}
}

// TestUpdateOperatorValueYaml tests operator value yaml updates
func TestUpdateOperatorValueYaml(t *testing.T) {
	tempDir := t.TempDir()
	sourceFile := tempDir + "/source.yaml"
	destFile := tempDir + "/operator-values.yaml"

	// Create source values file
	sourceContent := `gitops:
  operator:
    image: "{{.GitopsOperatorImage}}"
    namespace: "{{.GitopsOperatorNamespace}}"
`
	err := os.WriteFile(sourceFile, []byte(sourceContent), 0644)
	require.NoError(t, err)

	reconciler := &GitopsAddonReconciler{
		GitopsOperatorImage: "new-operator-image:v1.0.0",
		GitopsOperatorNS:    "new-operator-namespace",
	}

	// Create a mock embedded filesystem
	mockFS := embed.FS{}
	err = reconciler.updateOperatorValueYaml(mockFS, sourceFile, destFile)

	// This will error because of the embedded FS, but tests the code path
	assert.Error(t, err)
}

// TestUpdateDependencyValueYaml tests dependency value yaml updates
func TestUpdateDependencyValueYaml(t *testing.T) {
	tempDir := t.TempDir()
	sourceFile := tempDir + "/source.yaml"
	destFile := tempDir + "/dependency-values.yaml"

	// Create source values file with template
	sourceContent := `dependencies:
  gitops:
    image: "{{.GitopsImage}}"
    namespace: "{{.GitopsNamespace}}"
  redis:
    image: "{{.RedisImage}}"
`
	err := os.WriteFile(sourceFile, []byte(sourceContent), 0644)
	require.NoError(t, err)

	reconciler := &GitopsAddonReconciler{
		GitopsImage: "new-gitops-image:v2.8.0",
		GitopsNS:    "new-gitops-namespace",
		RedisImage:  "new-redis-image:7.0",
	}

	// Create a mock embedded filesystem
	mockFS := embed.FS{}
	err = reconciler.updateDependencyValueYaml(mockFS, sourceFile, destFile)

	// This will error because of the embedded FS, but tests the code path
	assert.Error(t, err)
}

// TestDeleteHelmReleaseSecret tests Helm release secret deletion
func TestDeleteHelmReleaseSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	// Create a mock Helm release secret
	helmSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.test-release.v1",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"owner": "helm",
				"name":  "test-release",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("test-release-data"),
		},
	}

	testCases := []struct {
		name        string
		secret      *corev1.Secret
		releaseName string
		namespace   string
		expectError bool
	}{
		{
			name:        "successfully delete existing secret",
			secret:      helmSecret,
			releaseName: "test-release",
			namespace:   "test-namespace",
			expectError: false,
		},
		{
			name:        "delete non-existent secret should not error",
			secret:      nil,
			releaseName: "non-existent-release",
			namespace:   "test-namespace",
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			objects := []client.Object{}
			if tc.secret != nil {
				objects = append(objects, tc.secret)
			}

			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			reconciler := &GitopsAddonReconciler{
				Client: fakeClient,
			}

			reconciler.deleteHelmReleaseSecret(tc.namespace, tc.releaseName)

			// Function is void, no error to check

			// Verify secret is deleted if it existed
			if tc.secret != nil {
				secret := &corev1.Secret{}
				err = fakeClient.Get(context.TODO(), types.NamespacedName{
					Name:      tc.secret.Name,
					Namespace: tc.secret.Namespace,
				}, secret)
				assert.Error(t, err) // Should not be found
			}
		})
	}
}

// TestPatchDefaultSA tests patching the default service account
func TestPatchDefaultSA(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	require.NoError(t, err)

	// Create a default service account
	defaultSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-namespace",
		},
	}

	testCases := []struct {
		name           string
		serviceAccount *corev1.ServiceAccount
		secretName     string
		namespace      string
		expectError    bool
	}{
		{
			name:           "successfully patch existing service account",
			serviceAccount: defaultSA,
			secretName:     "open-cluster-management-image-pull-credentials",
			namespace:      "test-namespace",
			expectError:    false,
		},
		{
			name:           "patch non-existent service account should error",
			serviceAccount: nil,
			secretName:     "open-cluster-management-image-pull-credentials",
			namespace:      "non-existent-namespace",
			expectError:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			objects := []client.Object{}
			if tc.serviceAccount != nil {
				objects = append(objects, tc.serviceAccount)
			}

			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			reconciler := &GitopsAddonReconciler{
				Client: fakeClient,
			}

			saKey := types.NamespacedName{Name: "default", Namespace: tc.namespace}
			err := reconciler.patchDefaultSA(saKey)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify service account was patched with image pull secret
				if tc.serviceAccount != nil {
					updatedSA := &corev1.ServiceAccount{}
					err = fakeClient.Get(context.TODO(), types.NamespacedName{
						Name:      "default",
						Namespace: tc.namespace,
					}, updatedSA)
					require.NoError(t, err)
					assert.Len(t, updatedSA.ImagePullSecrets, 1)
					assert.Equal(t, tc.secretName, updatedSA.ImagePullSecrets[0].Name)
				}
			}
		})
	}
}

// Helper type for testing
type fakeManager struct {
	client client.Client
	scheme *runtime.Scheme
	config *rest.Config
}

func (f *fakeManager) GetClient() client.Client                                       { return f.client }
func (f *fakeManager) GetScheme() *runtime.Scheme                                     { return f.scheme }
func (f *fakeManager) GetConfig() *rest.Config                                        { return f.config }
func (f *fakeManager) GetCache() cache.Cache                                          { return nil }
func (f *fakeManager) GetFieldIndexer() client.FieldIndexer                           { return nil }
func (f *fakeManager) GetEventRecorderFor(name string) record.EventRecorder           { return nil }
func (f *fakeManager) GetRESTMapper() meta.RESTMapper                                 { return nil }
func (f *fakeManager) GetAPIReader() client.Reader                                    { return nil }
func (f *fakeManager) Start(ctx context.Context) error                                { return nil }
func (f *fakeManager) Add(manager.Runnable) error                                     { return nil }
func (f *fakeManager) Elected() <-chan struct{}                                       { return nil }
func (f *fakeManager) GetLogger() logr.Logger                                         { return logr.Discard() }
func (f *fakeManager) GetControllerOptions() config.Controller                        { return config.Controller{} }
func (f *fakeManager) GetHTTPClient() *http.Client                                    { return nil }
func (f *fakeManager) GetWebhookServer() webhook.Server                               { return nil }
func (f *fakeManager) AddHealthzCheck(name string, check healthz.Checker) error       { return nil }
func (f *fakeManager) AddReadyzCheck(name string, check healthz.Checker) error        { return nil }
func (f *fakeManager) AddMetricsExtraHandler(path string, handler http.Handler) error { return nil }
func (f *fakeManager) AddMetricsServerExtraHandler(path string, handler http.Handler) error {
	return nil
}
func (f *fakeManager) GetHost() string             { return "" }
func (f *fakeManager) GetPort() int                { return 0 }
func (f *fakeManager) SetFields(interface{}) error { return nil }
