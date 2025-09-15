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
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// setupSchemeWithRBAC creates a runtime scheme with all necessary types registered including RBAC
func setupSchemeWithRBAC() *runtime.Scheme {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	rbacv1.AddToScheme(scheme)
	return scheme
}

func TestEnsureAddonManagerRBAC(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create test namespace
	testNamespace := "test-gitops-namespace"

	// Create fake client
	fakeClient := fake.NewClientBuilder().WithScheme(setupSchemeWithRBAC()).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test ensuring RBAC resources
	err := reconciler.ensureAddonManagerRBAC(testNamespace)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify Role was created
	role := &rbacv1.Role{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "addon-manager-controller-role",
		Namespace: testNamespace,
	}, role)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(role.Name).To(gomega.Equal("addon-manager-controller-role"))
	g.Expect(role.Namespace).To(gomega.Equal(testNamespace))
	g.Expect(role.Labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))
	g.Expect(len(role.Rules)).To(gomega.Equal(1))
	g.Expect(role.Rules[0].Resources).To(gomega.ContainElement("secrets"))
	g.Expect(role.Rules[0].Verbs).To(gomega.ContainElements("get", "list", "watch"))

	// Verify RoleBinding was created
	roleBinding := &rbacv1.RoleBinding{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "addon-manager-controller-rolebinding",
		Namespace: testNamespace,
	}, roleBinding)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(roleBinding.Name).To(gomega.Equal("addon-manager-controller-rolebinding"))
	g.Expect(roleBinding.Namespace).To(gomega.Equal(testNamespace))
	g.Expect(roleBinding.Labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))
	g.Expect(roleBinding.RoleRef.Name).To(gomega.Equal("addon-manager-controller-role"))
	g.Expect(len(roleBinding.Subjects)).To(gomega.Equal(1))
	g.Expect(roleBinding.Subjects[0].Name).To(gomega.Equal("addon-manager-controller-sa"))
	// Verify namespace uses utils.GetComponentNamespace + "-hub"
	expectedNamespace := utils.GetComponentNamespace("open-cluster-management") + "-hub"
	g.Expect(roleBinding.Subjects[0].Namespace).To(gomega.Equal(expectedNamespace))
}

func TestEnsureAddonManagerRBACIdempotent(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	testNamespace := "test-gitops-idempotent"

	// Create existing RBAC resources
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "addon-manager-controller-role",
			Namespace: testNamespace,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopsaddon": "true",
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}

	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "addon-manager-controller-rolebinding",
			Namespace: testNamespace,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopsaddon": "true",
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "addon-manager-controller-sa",
				Namespace: utils.GetComponentNamespace("open-cluster-management") + "-hub",
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     "addon-manager-controller-role",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	// Create fake client with existing resources
	fakeClient := fake.NewClientBuilder().WithScheme(setupSchemeWithRBAC()).WithObjects(role, roleBinding).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test ensuring RBAC resources again (should be idempotent)
	err := reconciler.ensureAddonManagerRBAC(testNamespace)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify resources still exist and unchanged
	retrievedRole := &rbacv1.Role{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "addon-manager-controller-role",
		Namespace: testNamespace,
	}, retrievedRole)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(retrievedRole.Name).To(gomega.Equal("addon-manager-controller-role"))

	retrievedRoleBinding := &rbacv1.RoleBinding{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "addon-manager-controller-rolebinding",
		Namespace: testNamespace,
	}, retrievedRoleBinding)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(retrievedRoleBinding.Name).To(gomega.Equal("addon-manager-controller-rolebinding"))
}

func TestEnsureArgoCDAgentCASecret(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	testNamespace := "test-gitops-ca-secret"
	ocmNamespace := utils.GetComponentNamespace("open-cluster-management")

	// Create source secret in OCM namespace
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "multicluster-operators-application-svc-ca",
			Namespace: ocmNamespace,
			Labels: map[string]string{
				"app": "multicluster-operators-application",
			},
			Annotations: map[string]string{
				"test-annotation": "test-value",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("test-ca-cert-data"),
			"tls.key": []byte("test-ca-key-data"),
		},
	}

	// Create fake client with source secret
	fakeClient := fake.NewClientBuilder().WithScheme(setupSchemeWithRBAC()).WithObjects(sourceSecret).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test ensuring CA secret
	err := reconciler.ensureArgoCDAgentCASecret(testNamespace)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify target secret was created
	targetSecret := &corev1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: testNamespace,
	}, targetSecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(targetSecret.Name).To(gomega.Equal("argocd-agent-ca"))
	g.Expect(targetSecret.Namespace).To(gomega.Equal(testNamespace))
	g.Expect(targetSecret.Type).To(gomega.Equal(corev1.SecretTypeTLS))
	g.Expect(string(targetSecret.Data["tls.crt"])).To(gomega.Equal("test-ca-cert-data"))
	g.Expect(string(targetSecret.Data["tls.key"])).To(gomega.Equal("test-ca-key-data"))
	g.Expect(targetSecret.Labels["app"]).To(gomega.Equal("multicluster-operators-application"))
	g.Expect(targetSecret.Annotations["test-annotation"]).To(gomega.Equal("test-value"))
}

func TestEnsureArgoCDAgentCASecretIdempotent(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	testNamespace := "test-gitops-ca-idempotent"

	// Create existing target secret
	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: testNamespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("existing-ca-cert-data"),
		},
	}

	// Create fake client with existing secret
	fakeClient := fake.NewClientBuilder().WithScheme(setupSchemeWithRBAC()).WithObjects(existingSecret).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test ensuring CA secret again (should be idempotent)
	err := reconciler.ensureArgoCDAgentCASecret(testNamespace)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify secret still exists and unchanged
	retrievedSecret := &corev1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: testNamespace,
	}, retrievedSecret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(string(retrievedSecret.Data["tls.crt"])).To(gomega.Equal("existing-ca-cert-data"))
}

func TestEnsureArgoCDAgentCASecretSourceMissing(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	testNamespace := "test-gitops-ca-missing"

	// Create fake client without source secret
	fakeClient := fake.NewClientBuilder().WithScheme(setupSchemeWithRBAC()).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test ensuring CA secret when source is missing
	err := reconciler.ensureArgoCDAgentCASecret(testNamespace)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("source secret multicluster-operators-application-svc-ca not found"))

	// Verify target secret was not created
	targetSecret := &corev1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: testNamespace,
	}, targetSecret)
	g.Expect(k8errors.IsNotFound(err)).To(gomega.BeTrue())
}
