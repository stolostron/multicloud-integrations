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
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPropagateHubCA(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)
	err = workv1.AddToScheme(scheme)
	require.NoError(t, err)
	err = spokeclusterv1.AddToScheme(scheme)
	require.NoError(t, err)
	err = gitopsclusterV1beta1.AddToScheme(scheme)
	require.NoError(t, err)

	// Create test CA secret
	caSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: utils.GitOpsNamespace,
		},
		Type: v1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("test-ca-certificate"),
		},
	}

	tests := []struct {
		name            string
		gitOpsCluster   *gitopsclusterV1beta1.GitOpsCluster
		managedClusters []*spokeclusterv1.ManagedCluster
		existingObjects []client.Object
		expectedError   bool
		validateFunc    func(t *testing.T, c client.Client, managedClusters []*spokeclusterv1.ManagedCluster)
	}{
		{
			name: "create ManifestWorks for managed clusters",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: utils.GitOpsNamespace,
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: utils.GitOpsNamespace,
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster2",
					},
				},
			},
			existingObjects: []client.Object{caSecret},
			validateFunc: func(t *testing.T, c client.Client, managedClusters []*spokeclusterv1.ManagedCluster) {
				for _, cluster := range managedClusters {
					mw := &workv1.ManifestWork{}
					err := c.Get(context.TODO(), types.NamespacedName{
						Name:      "argocd-agent-ca-mw",
						Namespace: cluster.Name,
					}, mw)
					assert.NoError(t, err, "ManifestWork should be created for cluster %s", cluster.Name)

					// Verify secret in manifest
					assert.Len(t, mw.Spec.Workload.Manifests, 1)
					manifest := mw.Spec.Workload.Manifests[0]

					secret := &v1.Secret{}
					err = json.Unmarshal(manifest.RawExtension.Raw, secret)
					assert.NoError(t, err)
					assert.Equal(t, "argocd-agent-ca", secret.Name)
					assert.Equal(t, utils.GitOpsNamespace, secret.Namespace)
					assert.Equal(t, []byte("test-ca-certificate"), secret.Data["ca.crt"])
				}
			},
		},
		{
			name: "skip local-cluster by name",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: utils.GitOpsNamespace,
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: utils.GitOpsNamespace,
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "local-cluster",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "remote-cluster",
					},
				},
			},
			existingObjects: []client.Object{caSecret},
			validateFunc: func(t *testing.T, c client.Client, managedClusters []*spokeclusterv1.ManagedCluster) {
				// local-cluster should not have ManifestWork
				mw := &workv1.ManifestWork{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-ca-mw",
					Namespace: "local-cluster",
				}, mw)
				assert.Error(t, err, "ManifestWork should not be created for local-cluster")

				// remote-cluster should have ManifestWork
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-ca-mw",
					Namespace: "remote-cluster",
				}, mw)
				assert.NoError(t, err, "ManifestWork should be created for remote-cluster")
			},
		},
		{
			name: "skip cluster with local-cluster label",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: utils.GitOpsNamespace,
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: utils.GitOpsNamespace,
					},
				},
			},
			managedClusters: []*spokeclusterv1.ManagedCluster{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster-with-label",
						Labels: map[string]string{
							"local-cluster": "true",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "normal-cluster",
					},
				},
			},
			existingObjects: []client.Object{caSecret},
			validateFunc: func(t *testing.T, c client.Client, managedClusters []*spokeclusterv1.ManagedCluster) {
				// cluster with local-cluster=true label should not have ManifestWork
				mw := &workv1.ManifestWork{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-ca-mw",
					Namespace: "cluster-with-label",
				}, mw)
				assert.Error(t, err, "ManifestWork should not be created for cluster with local-cluster=true label")

				// normal cluster should have ManifestWork
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-ca-mw",
					Namespace: "normal-cluster",
				}, mw)
				assert.NoError(t, err, "ManifestWork should be created for normal cluster")
			},
		},
		{
			name: "update existing ManifestWork when certificate changes",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: utils.GitOpsNamespace,
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: utils.GitOpsNamespace,
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
				caSecret,
				&workv1.ManifestWork{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-ca-mw",
						Namespace: "test-cluster",
					},
					Spec: workv1.ManifestWorkSpec{
						Workload: workv1.ManifestsTemplate{
							Manifests: []workv1.Manifest{
								{
									RawExtension: runtime.RawExtension{
										Raw: []byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"argocd-agent-ca","namespace":"openshift-gitops"},"type":"Opaque","data":{"ca.crt":"b2xkLWNlcnRpZmljYXRl"}}`),
									},
								},
							},
						},
					},
				},
			},
			validateFunc: func(t *testing.T, c client.Client, managedClusters []*spokeclusterv1.ManagedCluster) {
				mw := &workv1.ManifestWork{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      "argocd-agent-ca-mw",
					Namespace: "test-cluster",
				}, mw)
				assert.NoError(t, err)

				// Verify the secret was updated with new certificate
				manifest := mw.Spec.Workload.Manifests[0]
				secret := &v1.Secret{}
				err = json.Unmarshal(manifest.RawExtension.Raw, secret)
				assert.NoError(t, err)
				assert.Equal(t, []byte("test-ca-certificate"), secret.Data["ca.crt"])
			},
		},
		{
			name: "CA secret not found should return error",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: utils.GitOpsNamespace,
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
						ArgoNamespace: utils.GitOpsNamespace,
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

			err := reconciler.PropagateHubCA(tt.gitOpsCluster, tt.managedClusters)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, fakeClient, tt.managedClusters)
				}
			}
		})
	}
}

func TestGetArgoCDAgentCACert(t *testing.T) {
	scheme := runtime.NewScheme()
	err := v1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := []struct {
		name            string
		argoNamespace   string
		existingObjects []client.Object
		expectedError   bool
		expectedCert    string
	}{
		{
			name:          "get CA cert from tls.crt field",
			argoNamespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-ca",
						Namespace: utils.GitOpsNamespace,
					},
					Data: map[string][]byte{
						"tls.crt": []byte("test-ca-from-tls-crt"),
					},
				},
			},
			expectedCert: "test-ca-from-tls-crt",
		},
		{
			name:          "get CA cert from ca.crt field when tls.crt not available",
			argoNamespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-ca",
						Namespace: utils.GitOpsNamespace,
					},
					Data: map[string][]byte{
						"ca.crt": []byte("test-ca-from-ca-crt"),
					},
				},
			},
			expectedCert: "test-ca-from-ca-crt",
		},
		{
			name:          "secret not found should return error",
			argoNamespace: utils.GitOpsNamespace,
			expectedError: true,
		},
		{
			name:          "secret exists but no certificate data",
			argoNamespace: utils.GitOpsNamespace,
			existingObjects: []client.Object{
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-agent-ca",
						Namespace: utils.GitOpsNamespace,
					},
					Data: map[string][]byte{
						"other-field": []byte("other-data"),
					},
				},
			},
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

			cert, err := reconciler.getArgoCDAgentCACert(tt.argoNamespace)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedCert, cert)
			}
		})
	}
}

func TestCreateArgoCDAgentManifestWork(t *testing.T) {
	tests := []struct {
		name          string
		clusterName   string
		argoNamespace string
		caCert        string
		validateFunc  func(t *testing.T, mw *workv1.ManifestWork)
	}{
		{
			name:          "create ManifestWork with correct structure",
			clusterName:   "test-cluster",
			argoNamespace: utils.GitOpsNamespace,
			caCert:        "test-ca-certificate",
			validateFunc: func(t *testing.T, mw *workv1.ManifestWork) {
				assert.Equal(t, "argocd-agent-ca-mw", mw.Name)
				assert.Equal(t, "test-cluster", mw.Namespace)
				assert.Equal(t, "work.open-cluster-management.io/v1", mw.APIVersion)
				assert.Equal(t, "ManifestWork", mw.Kind)

				// Verify manifest structure
				assert.Len(t, mw.Spec.Workload.Manifests, 1)
				manifest := mw.Spec.Workload.Manifests[0]

				// Extract and verify the secret
				secret := &v1.Secret{}
				err := json.Unmarshal(manifest.RawExtension.Raw, secret)
				assert.NoError(t, err)

				assert.Equal(t, "argocd-agent-ca", secret.Name)
				assert.Equal(t, utils.GitOpsNamespace, secret.Namespace)
				assert.Equal(t, v1.SecretTypeOpaque, secret.Type)
				assert.Equal(t, []byte("test-ca-certificate"), secret.Data["ca.crt"])
			},
		},
		{
			name:          "create ManifestWork with different namespace",
			clusterName:   "cluster2",
			argoNamespace: "custom-gitops",
			caCert:        "custom-ca-cert",
			validateFunc: func(t *testing.T, mw *workv1.ManifestWork) {
				assert.Equal(t, "cluster2", mw.Namespace)

				manifest := mw.Spec.Workload.Manifests[0]
				secret := &v1.Secret{}
				err := json.Unmarshal(manifest.RawExtension.Raw, secret)
				assert.NoError(t, err)

				assert.Equal(t, "custom-gitops", secret.Namespace)
				assert.Equal(t, []byte("custom-ca-cert"), secret.Data["ca.crt"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &ReconcileGitOpsCluster{}

			mw := reconciler.createArgoCDAgentManifestWork(tt.clusterName, tt.argoNamespace, tt.caCert)

			if tt.validateFunc != nil {
				tt.validateFunc(t, mw)
			}
		})
	}
}
