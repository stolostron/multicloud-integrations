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

package gitopscluster

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestVerifyArgocdNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		argoNamespace   string
		existingObjects []client.Object
		expectedResult  bool
	}{
		{
			name:          "valid ArgoCD namespace with server service",
			argoNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-server",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"app.kubernetes.io/component": "server",
							"app.kubernetes.io/part-of":   "argocd",
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name:          "namespace without ArgoCD server service",
			argoNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-service",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"app": "other",
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name:           "empty namespace",
			argoNamespace:  "empty-namespace",
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			result := reconciler.VerifyArgocdNamespace(tt.argoNamespace)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestFindServiceWithLabelsAndNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		namespace       string
		labels          map[string]string
		existingObjects []client.Object
		expectedResult  bool
	}{
		{
			name:      "find service with matching labels",
			namespace: "test-namespace",
			labels: map[string]string{
				"app.kubernetes.io/component": "server",
				"app.kubernetes.io/part-of":   "argocd",
			},
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-server",
						Namespace: "test-namespace",
						Labels: map[string]string{
							"app.kubernetes.io/component": "server",
							"app.kubernetes.io/part-of":   "argocd",
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name:      "service with partial matching labels",
			namespace: "test-namespace",
			labels: map[string]string{
				"app.kubernetes.io/component": "server",
				"app.kubernetes.io/part-of":   "argocd",
			},
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-server",
						Namespace: "test-namespace",
						Labels: map[string]string{
							"app.kubernetes.io/component": "server",
							// Missing the part-of label
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name:      "no services in namespace",
			namespace: "empty-namespace",
			labels: map[string]string{
				"app": "test",
			},
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			result := reconciler.FindServiceWithLabelsAndNamespace(tt.namespace, tt.labels)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestEnsureServerAddressAndPort(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)
	err = spokeclusterv1.AddToScheme(scheme)
	require.NoError(t, err)
	err = gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)
	err = addonv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		gitOpsCluster   *gitopsclusterV1beta1.GitOpsCluster
		managedClusters []*spokeclusterv1.ManagedCluster
		existingObjects []client.Object
		expectedUpdated bool
		expectedError   bool
		validateFunc    func(t *testing.T, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster)
	}{
		{
			name: "server address and port already set - no update",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "openshift-gitops",
					},
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
							ServerAddress: "existing-server.com",
							ServerPort:    "443",
						},
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{},
			expectedUpdated: false,
		},
		{
			name: "discover and set server address and port",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "openshift-gitops",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Spec: v1.ServiceSpec{
						Ports: []v1.ServicePort{
							{Name: "https", Port: 443},
						},
					},
					Status: v1.ServiceStatus{
						LoadBalancer: v1.LoadBalancerStatus{
							Ingress: []v1.LoadBalancerIngress{
								{Hostname: "discovered-server.com"},
							},
						},
					},
				},
			},
			expectedUpdated: true,
			validateFunc: func(t *testing.T, gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster) {
				assert.Equal(t, "discovered-server.com", gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress)
				assert.Equal(t, "443", gitOpsCluster.Spec.GitOpsAddon.ArgoCDAgent.ServerPort)
			},
		},
		{
			name: "existing addon config has server settings - no update",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "openshift-gitops",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon-config",
						Namespace: "test-cluster",
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						CustomizedVariables: []addonv1alpha1.CustomizedVariable{
							{Name: "ARGOCD_AGENT_SERVER_ADDRESS", Value: "existing-server.com"},
						},
					},
				},
			},
			expectedUpdated: false,
		},
		{
			name: "principal service not found - should return error",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: "openshift-gitops",
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			existingObjects: []client.Object{},
			expectedError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			updated, err := reconciler.EnsureServerAddressAndPort(tt.gitOpsCluster, tt.managedClusters)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedUpdated, updated)
				if tt.validateFunc != nil {
					tt.validateFunc(t, tt.gitOpsCluster)
				}
			}
		})
	}
}

func TestHasExistingServerConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	err := addonv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)
	err = spokeclusterv1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		managedClusters []*spokeclusterv1.ManagedCluster
		existingObjects []client.Object
		expectedResult  bool
		expectedError   bool
	}{
		{
			name: "addon config with server address exists",
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon-config",
						Namespace: "cluster1",
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						CustomizedVariables: []addonv1alpha1.CustomizedVariable{
							{Name: "ARGOCD_AGENT_SERVER_ADDRESS", Value: "server.com"},
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "addon config with server port exists",
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon-config",
						Namespace: "cluster1",
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						CustomizedVariables: []addonv1alpha1.CustomizedVariable{
							{Name: "ARGOCD_AGENT_SERVER_PORT", Value: "443"},
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "addon config exists but no server settings",
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
			},
			existingObjects: []client.Object{
				&addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gitops-addon-config",
						Namespace: "cluster1",
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						CustomizedVariables: []addonv1alpha1.CustomizedVariable{
							{Name: "OTHER_VAR", Value: "other-value"},
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "no addon config exists",
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
			},
			existingObjects: []client.Object{},
			expectedResult:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			result, err := reconciler.HasExistingServerConfig(tt.managedClusters)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

func TestDiscoverServerAddressAndPort(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		argoNamespace   string
		existingObjects []client.Object
		expectedAddress string
		expectedPort    string
		expectedError   bool
	}{
		{
			name:          "discover from LoadBalancer hostname",
			argoNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Spec: v1.ServiceSpec{
						Ports: []v1.ServicePort{
							{Name: "https", Port: 443},
						},
					},
					Status: v1.ServiceStatus{
						LoadBalancer: v1.LoadBalancerStatus{
							Ingress: []v1.LoadBalancerIngress{
								{Hostname: "test-server.example.com"},
							},
						},
					},
				},
			},
			expectedAddress: "test-server.example.com",
			expectedPort:    "443",
		},
		{
			name:          "discover from LoadBalancer IP",
			argoNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Spec: v1.ServiceSpec{
						Ports: []v1.ServicePort{
							{Name: "https", Port: 8443},
						},
					},
					Status: v1.ServiceStatus{
						LoadBalancer: v1.LoadBalancerStatus{
							Ingress: []v1.LoadBalancerIngress{
								{IP: "192.168.1.100"},
							},
						},
					},
				},
			},
			expectedAddress: "192.168.1.100",
			expectedPort:    "8443",
		},
		{
			name:          "service without external LoadBalancer - should error",
			argoNamespace: "openshift-gitops",
			existingObjects: []client.Object{
				&v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
					},
					Spec: v1.ServiceSpec{
						ClusterIP: "10.0.0.100",
					},
					Status: v1.ServiceStatus{
						LoadBalancer: v1.LoadBalancerStatus{
							Ingress: []v1.LoadBalancerIngress{},
						},
					},
				},
			},
			expectedError: true,
		},
		{
			name:          "service not found - should error",
			argoNamespace: "openshift-gitops",
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			address, port, err := reconciler.DiscoverServerAddressAndPort(tt.argoNamespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedAddress, address)
				assert.Equal(t, tt.expectedPort, port)
			}
		})
	}
}

func TestGetManagedClusters(t *testing.T) {
	scheme := runtime.NewScheme()
	err := clusterv1beta1.AddToScheme(scheme)
	require.NoError(t, err)
	err = spokeclusterv1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		namespace       string
		placementRef    v1.ObjectReference
		existingObjects []client.Object
		expectedCount   int
		expectedError   bool
	}{
		{
			name:      "get managed clusters from placement decision",
			namespace: "test-namespace",
			placementRef: v1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Name:       "test-placement",
			},
			existingObjects: []client.Object{
				&clusterv1beta1.Placement{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-placement",
						Namespace: "test-namespace",
					},
				},
				&clusterv1beta1.PlacementDecision{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-placement-decision",
						Namespace: "test-namespace",
						Labels: map[string]string{
							"cluster.open-cluster-management.io/placement": "test-placement",
						},
					},
					Status: clusterv1beta1.PlacementDecisionStatus{
						Decisions: []clusterv1beta1.ClusterDecision{
							{ClusterName: "cluster1"},
							{ClusterName: "cluster2"},
						},
					},
				},
				&spokeclusterv1.ManagedCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
				&spokeclusterv1.ManagedCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster2",
					},
				},
			},
			expectedCount: 2,
		},
		{
			name:      "invalid placement kind should return error",
			namespace: "test-namespace",
			placementRef: v1.ObjectReference{
				Kind:       "InvalidKind",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Name:       "test-placement",
			},
			expectedError: true,
		},
		{
			name:      "invalid placement APIVersion should return error",
			namespace: "test-namespace",
			placementRef: v1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "invalid/v1",
				Name:       "test-placement",
			},
			expectedError: true,
		},
		{
			name:      "placement not found should return error",
			namespace: "test-namespace",
			placementRef: v1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Name:       "non-existent-placement",
			},
			expectedError: true,
		},
		{
			name:      "no placement decisions found",
			namespace: "test-namespace",
			placementRef: v1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Name:       "test-placement",
			},
			existingObjects: []client.Object{
				&clusterv1beta1.Placement{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-placement",
						Namespace: "test-namespace",
					},
				},
			},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			reconciler := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			managedClusters, err := reconciler.GetManagedClusters(tt.namespace, tt.placementRef)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, managedClusters, tt.expectedCount)
			}
		})
	}
}
