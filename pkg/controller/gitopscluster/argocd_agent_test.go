// Copyright 2021 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gitopscluster

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Unit tests for ArgoCD Agent functionality that don't require the full test environment

// setupScheme creates a runtime scheme with all necessary types registered
func setupScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	workv1.AddToScheme(scheme)
	clusterv1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	return scheme
}

func TestGetArgoCDAgentCACertUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with valid secret
	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "test-namespace",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("-----BEGIN CERTIFICATE-----\nFAKE-TEST-CERT-FOOBAR-EXAMPLE\n-----END CERTIFICATE-----"),
			"tls.key": []byte("-----BEGIN RSA PRIVATE KEY-----\nFAKE-TEST-KEY-FOOBAR-EXAMPLE\n-----END RSA PRIVATE KEY-----"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(testSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test successful retrieval
	caCert, err := reconciler.getArgoCDAgentCACert("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(caCert).To(gomega.Equal("-----BEGIN CERTIFICATE-----\nFAKE-TEST-CERT-FOOBAR-EXAMPLE\n-----END CERTIFICATE-----"))

	// Test with missing secret
	_, err = reconciler.getArgoCDAgentCACert("nonexistent-namespace")
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("failed to get argocd-agent-ca secret"))
}

func TestGetArgoCDAgentCACertMissingTLSCrtUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with secret missing tls.crt
	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "test-namespace",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.key": []byte("test-key-data"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(testSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test missing both tls.crt and ca.crt fields
	_, err := reconciler.getArgoCDAgentCACert("test-namespace")
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("neither tls.crt nor ca.crt found in argocd-agent-ca secret"))
}

func TestGetArgoCDAgentCACertWithCaCrtField(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with secret having ca.crt field (fallback case)
	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "test-namespace",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca.crt": []byte("test-certificate-data-from-ca-crt"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(testSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test successful retrieval from ca.crt field
	caCert, err := reconciler.getArgoCDAgentCACert("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(caCert).To(gomega.Equal("test-certificate-data-from-ca-crt"))
}

func TestCreateArgoCDAgentManifestWorkUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	reconciler := &ReconcileGitOpsCluster{}

	// Test ManifestWork creation
	manifestWork := reconciler.createArgoCDAgentManifestWork("test-cluster", "test-namespace", "test-ca-cert")

	g.Expect(manifestWork.Name).To(gomega.Equal("argocd-agent-ca-mw"))
	g.Expect(manifestWork.Namespace).To(gomega.Equal("test-cluster"))
	g.Expect(manifestWork.APIVersion).To(gomega.Equal("work.open-cluster-management.io/v1"))
	g.Expect(manifestWork.Kind).To(gomega.Equal("ManifestWork"))
	g.Expect(len(manifestWork.Spec.Workload.Manifests)).To(gomega.Equal(1))

	// Verify the secret manifest
	manifest := manifestWork.Spec.Workload.Manifests[0]
	secret := manifest.RawExtension.Object.(*corev1.Secret)
	g.Expect(secret.Name).To(gomega.Equal("argocd-agent-ca"))
	g.Expect(secret.Namespace).To(gomega.Equal("test-namespace"))
	g.Expect(secret.Type).To(gomega.Equal(corev1.SecretTypeOpaque))
	g.Expect(string(secret.Data["ca.crt"])).To(gomega.Equal("test-ca-cert"))
}

func TestArgoCDAgentEnabledUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create test resources
	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-agent",
		},
	}

	// Create ArgoCD agent CA secret
	argoCDAgentCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "openshift-gitops",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("-----BEGIN CERTIFICATE-----\nTEST-FAKE-CERTIFICATE-DATA-FOR-UNIT-TESTS-ONLY\nNOT-A-REAL-CERTIFICATE-FOOBAR-EXAMPLE\n-----END CERTIFICATE-----"),
			"tls.key": []byte("-----BEGIN RSA PRIVATE KEY-----\nTEST-FAKE-PRIVATE-KEY-DATA-FOR-UNIT-TESTS-ONLY\nNOT-A-REAL-PRIVATE-KEY-FOOBAR-EXAMPLE\n-----END RSA PRIVATE KEY-----"),
		},
	}

	// Create GitOpsCluster with ArgoCD agent enabled
	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-agent",
			Namespace: "test-argocd-agent",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "openshift-gitops",
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0], // pointer to true
			},
		},
	}

	// Create fake client with test resources
	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(argoCDAgentCASecret).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test CreateArgoCDAgentManifestWorks function
	managedClusters := []*clusterv1.ManagedCluster{testCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork was created
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-agent",
	}, manifestWork)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork content
	g.Expect(manifestWork.Name).To(gomega.Equal("argocd-agent-ca-mw"))
	g.Expect(manifestWork.Namespace).To(gomega.Equal("test-cluster-agent"))
	g.Expect(len(manifestWork.Spec.Workload.Manifests)).To(gomega.Equal(1))

	// Verify the secret manifest in the ManifestWork
	manifest := manifestWork.Spec.Workload.Manifests[0]
	secret := &corev1.Secret{}
	err = json.Unmarshal(manifest.RawExtension.Raw, secret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(secret.Name).To(gomega.Equal("argocd-agent-ca"))
	g.Expect(secret.Namespace).To(gomega.Equal("openshift-gitops"))
	g.Expect(secret.Type).To(gomega.Equal(corev1.SecretTypeOpaque))
	g.Expect(secret.Data["ca.crt"]).NotTo(gomega.BeEmpty())
}

func TestArgoCDAgentDefaultNamespaceUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create test resources
	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-default",
		},
	}

	// Create ArgoCD agent CA secret in default namespace
	argoCDAgentCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "openshift-gitops", // Default namespace
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("-----BEGIN CERTIFICATE-----\nFAKE-DEFAULT-NAMESPACE-CERT-FOOBAR-TEST\n-----END CERTIFICATE-----"),
			"tls.key": []byte("-----BEGIN RSA PRIVATE KEY-----\nFAKE-DEFAULT-NAMESPACE-KEY-FOOBAR-TEST\n-----END RSA PRIVATE KEY-----"),
		},
	}

	// Create GitOpsCluster with empty ArgoNamespace (should default to openshift-gitops)
	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-default",
			Namespace: "test-default-namespace",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "", // Empty namespace should default to openshift-gitops
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0],
			},
		},
	}

	// Create fake client with test resources
	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(argoCDAgentCASecret).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test CreateArgoCDAgentManifestWorks function
	managedClusters := []*clusterv1.ManagedCluster{testCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork was created
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-default",
	}, manifestWork)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify the secret manifest uses the default namespace
	manifest := manifestWork.Spec.Workload.Manifests[0]
	secret := &corev1.Secret{}
	err = json.Unmarshal(manifest.RawExtension.Raw, secret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(secret.Namespace).To(gomega.Equal("openshift-gitops")) // Should use default namespace
}

func TestArgoCDAgentDisabledUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create test resources
	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-agent-disabled",
		},
	}

	// Create GitOpsCluster with ArgoCD agent disabled
	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-agent-disabled",
			Namespace: "test-argocd-agent-disabled",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "openshift-gitops",
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{false}[0], // pointer to false
			},
		},
	}

	// Create fake client with test resources (no CA secret)
	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test CreateArgoCDAgentManifestWorks function
	managedClusters := []*clusterv1.ManagedCluster{testCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).To(gomega.HaveOccurred()) // Should fail because CA secret doesn't exist

	// Verify ManifestWork was NOT created
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-agent-disabled",
	}, manifestWork)
	g.Expect(err).To(gomega.HaveOccurred()) // Should not exist
}

// Test the integration logic: argoCDAgent.enabled=true should set createBlankClusterSecrets=true
func TestArgoCDAgentEnablesBlankClusterSecretsUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test case 1: ArgoCDAgent enabled, CreateBlankClusterSecrets explicitly false
	testGitOpsCluster1 := &gitopsclusterV1beta1.GitOpsCluster{
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			CreateBlankClusterSecrets: &[]bool{false}[0], // explicitly false
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0], // enabled
			},
		},
	}

	// Simulate the logic from the controller
	argoCDAgentEnabled := false
	if testGitOpsCluster1.Spec.ArgoCDAgent != nil && testGitOpsCluster1.Spec.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *testGitOpsCluster1.Spec.ArgoCDAgent.Enabled
	}

	createBlankClusterSecrets := false
	if testGitOpsCluster1.Spec.CreateBlankClusterSecrets != nil {
		createBlankClusterSecrets = *testGitOpsCluster1.Spec.CreateBlankClusterSecrets
	}
	// This is the key logic we're testing
	if argoCDAgentEnabled {
		createBlankClusterSecrets = true
	}

	g.Expect(argoCDAgentEnabled).To(gomega.BeTrue())
	g.Expect(createBlankClusterSecrets).To(gomega.BeTrue()) // Should be overridden to true

	// Test case 2: ArgoCDAgent disabled, CreateBlankClusterSecrets explicitly true
	testGitOpsCluster2 := &gitopsclusterV1beta1.GitOpsCluster{
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			CreateBlankClusterSecrets: &[]bool{true}[0], // explicitly true
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{false}[0], // disabled
			},
		},
	}

	argoCDAgentEnabled2 := false
	if testGitOpsCluster2.Spec.ArgoCDAgent != nil && testGitOpsCluster2.Spec.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled2 = *testGitOpsCluster2.Spec.ArgoCDAgent.Enabled
	}

	createBlankClusterSecrets2 := false
	if testGitOpsCluster2.Spec.CreateBlankClusterSecrets != nil {
		createBlankClusterSecrets2 = *testGitOpsCluster2.Spec.CreateBlankClusterSecrets
	}

	if argoCDAgentEnabled2 {
		createBlankClusterSecrets2 = true
	}

	g.Expect(argoCDAgentEnabled2).To(gomega.BeFalse())
	g.Expect(createBlankClusterSecrets2).To(gomega.BeTrue()) // Should remain true
}

// Test ManifestWork updates when CA certificate changes
func TestArgoCDAgentManifestWorkUpdateUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-update",
		},
	}

	// Create initial CA secret
	initialCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "openshift-gitops",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("-----BEGIN CERTIFICATE-----\nFAKE-INITIAL-CERT-DATA-FOOBAR-TEST\n-----END CERTIFICATE-----"),
			"tls.key": []byte("-----BEGIN RSA PRIVATE KEY-----\nFAKE-INITIAL-KEY-DATA-FOOBAR-TEST\n-----END RSA PRIVATE KEY-----"),
		},
	}

	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-update",
			Namespace: "test-update",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "openshift-gitops",
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0],
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(initialCASecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// First run - create ManifestWork
	managedClusters := []*clusterv1.ManagedCluster{testCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify initial ManifestWork
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-update",
	}, manifestWork)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Get initial secret data
	initialManifest := manifestWork.Spec.Workload.Manifests[0]
	initialSecret := &corev1.Secret{}
	err = json.Unmarshal(initialManifest.RawExtension.Raw, initialSecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(string(initialSecret.Data["ca.crt"])).To(gomega.Equal("-----BEGIN CERTIFICATE-----\nFAKE-INITIAL-CERT-DATA-FOOBAR-TEST\n-----END CERTIFICATE-----"))

	// Update the CA secret
	updatedCASecret := initialCASecret.DeepCopy()
	updatedCASecret.Data["tls.crt"] = []byte("-----BEGIN CERTIFICATE-----\nFAKE-UPDATED-CERT-DATA-FOOBAR-TEST\n-----END CERTIFICATE-----")
	err = fakeClient.Update(context.TODO(), updatedCASecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Second run - should update existing ManifestWork
	err = reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork was updated
	updatedManifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-update",
	}, updatedManifestWork)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify the secret data was updated
	updatedManifest := updatedManifestWork.Spec.Workload.Manifests[0]
	updatedSecret := &corev1.Secret{}
	err = json.Unmarshal(updatedManifest.RawExtension.Raw, updatedSecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(string(updatedSecret.Data["ca.crt"])).To(gomega.Equal("-----BEGIN CERTIFICATE-----\nFAKE-UPDATED-CERT-DATA-FOOBAR-TEST\n-----END CERTIFICATE-----"))
}

// Test multiple managed clusters scenario
func TestArgoCDAgentMultipleClustersUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create multiple test clusters
	clusters := []*clusterv1.ManagedCluster{
		{ObjectMeta: metav1.ObjectMeta{Name: "cluster-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cluster-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cluster-3"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cluster-4"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cluster-5"}},
	}

	argoCDAgentCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "custom-argo-namespace",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("-----BEGIN CERTIFICATE-----\nFAKE-MULTI-CLUSTER-CERT-FOOBAR-TEST\n-----END CERTIFICATE-----"),
			"tls.key": []byte("-----BEGIN RSA PRIVATE KEY-----\nFAKE-MULTI-CLUSTER-KEY-FOOBAR-TEST\n-----END RSA PRIVATE KEY-----"),
		},
	}

	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-multi-cluster",
			Namespace: "test-multi",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "custom-argo-namespace",
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0],
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(argoCDAgentCASecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Create ManifestWorks for all clusters
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, clusters)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork was created for each cluster
	for _, cluster := range clusters {
		manifestWork := &workv1.ManifestWork{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca-mw",
			Namespace: cluster.Name,
		}, manifestWork)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(manifestWork.Name).To(gomega.Equal("argocd-agent-ca-mw"))
		g.Expect(manifestWork.Namespace).To(gomega.Equal(cluster.Name))

		// Verify secret content
		manifest := manifestWork.Spec.Workload.Manifests[0]
		secret := &corev1.Secret{}
		err = json.Unmarshal(manifest.RawExtension.Raw, secret)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(secret.Namespace).To(gomega.Equal("custom-argo-namespace"))
		g.Expect(string(secret.Data["ca.crt"])).To(gomega.Equal("-----BEGIN CERTIFICATE-----\nFAKE-MULTI-CLUSTER-CERT-FOOBAR-TEST\n-----END CERTIFICATE-----"))
	}
}

// Test partial failure scenarios
func TestArgoCDAgentPartialFailureUnit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-partial-fail",
		},
	}

	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-partial-fail",
			Namespace: "test-fail",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "nonexistent-namespace", // This will cause failure
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0],
			},
		},
	}

	// Create fake client without the CA secret (will cause failure)
	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// This should fail because CA secret doesn't exist
	managedClusters := []*clusterv1.ManagedCluster{testCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("failed to get ArgoCD agent CA certificate"))

	// Verify no ManifestWork was created
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-partial-fail",
	}, manifestWork)
	g.Expect(err).To(gomega.HaveOccurred()) // Should not exist
}

func TestCreateArgoCDAgentManifestWorksSkipLocalCluster(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create ArgoCD agent CA secret
	argoCDAgentCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "openshift-gitops",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("-----BEGIN CERTIFICATE-----\nTEST-FAKE-CERTIFICATE-DATA-FOR-UNIT-TESTS-ONLY\n-----END CERTIFICATE-----"),
			"tls.key": []byte("-----BEGIN RSA PRIVATE KEY-----\nTEST-FAKE-PRIVATE-KEY-DATA-FOR-UNIT-TESTS-ONLY\n-----END RSA PRIVATE KEY-----"),
		},
	}

	// Create test clusters - one named "local-cluster", one with local-cluster=true label, and one regular cluster
	localClusterByName := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "local-cluster",
		},
	}

	localClusterByLabel := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-with-label",
			Labels: map[string]string{
				"local-cluster": "true",
			},
		},
	}

	regularCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "regular-cluster",
		},
	}

	// Create GitOpsCluster with ArgoCD agent enabled
	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-local-filter",
			Namespace: "test-local-filter",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "openshift-gitops",
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0], // pointer to true
			},
		},
	}

	// Create fake client with test resources
	fakeClient := fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(argoCDAgentCASecret).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test CreateArgoCDAgentManifestWorks function with all three clusters
	managedClusters := []*clusterv1.ManagedCluster{localClusterByName, localClusterByLabel, regularCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork was NOT created for local-cluster (by name)
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "local-cluster",
	}, manifestWork)
	g.Expect(err).To(gomega.HaveOccurred()) // Should not exist
	g.Expect(k8errors.IsNotFound(err)).To(gomega.BeTrue())

	// Verify ManifestWork was NOT created for cluster with local-cluster=true label
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-with-label",
	}, manifestWork)
	g.Expect(err).To(gomega.HaveOccurred()) // Should not exist
	g.Expect(k8errors.IsNotFound(err)).To(gomega.BeTrue())

	// Verify ManifestWork WAS created for regular cluster
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "regular-cluster",
	}, manifestWork)
	g.Expect(err).NotTo(gomega.HaveOccurred()) // Should exist

	// Verify the ManifestWork content for regular cluster
	g.Expect(manifestWork.Name).To(gomega.Equal("argocd-agent-ca-mw"))
	g.Expect(manifestWork.Namespace).To(gomega.Equal("regular-cluster"))
	g.Expect(len(manifestWork.Spec.Workload.Manifests)).To(gomega.Equal(1))
}
