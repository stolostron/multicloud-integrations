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

package exec

import (
	"context"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureOpenShiftGitOpsNamespace(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create fake client
	fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

	// Test case 1: Namespace doesn't exist, should be created
	t.Run("CreateNamespace", func(t *testing.T) {
		err := ensureOpenShiftGitOpsNamespace(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify namespace was created
		namespace := &corev1.Namespace{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{Name: GitopsNS}, namespace)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(namespace.Name).To(gomega.Equal(GitopsNS))
		// Verify no labels are added (we don't own system namespaces)
		g.Expect(namespace.Labels).To(gomega.BeNil())
	})

	// Test case 2: Namespace already exists
	t.Run("NamespaceAlreadyExists", func(t *testing.T) {
		err := ensureOpenShiftGitOpsNamespace(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})
}

func TestEnsureAddonManagerRole(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Add RBAC types to scheme
	rbacv1.AddToScheme(k8sscheme.Scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

	// Ensure namespace exists first
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: GitopsNS,
		},
	}
	g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

	// Test case 1: Role doesn't exist, should be created
	t.Run("CreateRole", func(t *testing.T) {
		err := ensureAddonManagerRole(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify role was created
		role := &rbacv1.Role{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-role",
			Namespace: GitopsNS,
		}, role)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(role.Name).To(gomega.Equal("addon-manager-controller-role"))
		g.Expect(role.Namespace).To(gomega.Equal(GitopsNS))
		g.Expect(role.Labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))

		// Verify rules
		g.Expect(len(role.Rules)).To(gomega.Equal(1))
		g.Expect(role.Rules[0].APIGroups).To(gomega.Equal([]string{""}))
		g.Expect(role.Rules[0].Resources).To(gomega.Equal([]string{"secrets"}))
		g.Expect(role.Rules[0].Verbs).To(gomega.Equal([]string{"get", "list", "watch"}))
	})

	// Test case 2: Role already exists
	t.Run("RoleAlreadyExists", func(t *testing.T) {
		err := ensureAddonManagerRole(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})
}

func TestEnsureAddonManagerRoleBinding(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Add RBAC types to scheme
	rbacv1.AddToScheme(k8sscheme.Scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

	// Ensure namespace exists first
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: GitopsNS,
		},
	}
	g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

	// Test case 1: RoleBinding doesn't exist, should be created
	t.Run("CreateRoleBinding", func(t *testing.T) {
		err := ensureAddonManagerRoleBinding(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify rolebinding was created
		roleBinding := &rbacv1.RoleBinding{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-rolebinding",
			Namespace: GitopsNS,
		}, roleBinding)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(roleBinding.Name).To(gomega.Equal("addon-manager-controller-rolebinding"))
		g.Expect(roleBinding.Namespace).To(gomega.Equal(GitopsNS))
		g.Expect(roleBinding.Labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))

		// Verify subjects - namespace should be computed dynamically
		g.Expect(len(roleBinding.Subjects)).To(gomega.Equal(1))
		g.Expect(roleBinding.Subjects[0].Kind).To(gomega.Equal("ServiceAccount"))
		g.Expect(roleBinding.Subjects[0].Name).To(gomega.Equal("addon-manager-controller-sa"))
		expectedNamespace := utils.GetComponentNamespace("open-cluster-management") + "-hub"
		g.Expect(roleBinding.Subjects[0].Namespace).To(gomega.Equal(expectedNamespace))

		// Verify role ref
		g.Expect(roleBinding.RoleRef.Kind).To(gomega.Equal("Role"))
		g.Expect(roleBinding.RoleRef.Name).To(gomega.Equal("addon-manager-controller-role"))
		g.Expect(roleBinding.RoleRef.APIGroup).To(gomega.Equal("rbac.authorization.k8s.io"))
	})

	// Test case 2: RoleBinding already exists
	t.Run("RoleBindingAlreadyExists", func(t *testing.T) {
		err := ensureAddonManagerRoleBinding(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})
}

func TestEnsureAddonManagerRBAC(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Add RBAC types to scheme
	rbacv1.AddToScheme(k8sscheme.Scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

	// Ensure namespace exists first
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: GitopsNS,
		},
	}
	g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

	// Test creating both role and rolebinding
	t.Run("CreateRBACResources", func(t *testing.T) {
		err := ensureAddonManagerRBAC(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify both resources exist
		role := &rbacv1.Role{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-role",
			Namespace: GitopsNS,
		}, role)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		roleBinding := &rbacv1.RoleBinding{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-rolebinding",
			Namespace: GitopsNS,
		}, roleBinding)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})
}

func TestGitopsNSConstant(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test the GitopsNS constant is set correctly
	t.Run("GitopsNSValue", func(t *testing.T) {
		g.Expect(GitopsNS).To(gomega.Equal("openshift-gitops"))
	})
}

func TestRBACResourceLabels(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Add RBAC types to scheme
	rbacv1.AddToScheme(k8sscheme.Scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

	// Ensure namespace exists first
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: GitopsNS,
		},
	}
	g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

	t.Run("VerifyResourceLabels", func(t *testing.T) {
		// Create the resources
		err := ensureAddonManagerRBAC(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify Role has correct labels
		role := &rbacv1.Role{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-role",
			Namespace: GitopsNS,
		}, role)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(role.Labels).To(gomega.HaveKey("apps.open-cluster-management.io/gitopsaddon"))
		g.Expect(role.Labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))

		// Verify RoleBinding has correct labels
		roleBinding := &rbacv1.RoleBinding{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-rolebinding",
			Namespace: GitopsNS,
		}, roleBinding)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(roleBinding.Labels).To(gomega.HaveKey("apps.open-cluster-management.io/gitopsaddon"))
		g.Expect(roleBinding.Labels["apps.open-cluster-management.io/gitopsaddon"]).To(gomega.Equal("true"))
	})
}

func TestRBACResourceSubjectsAndRoleRef(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Add RBAC types to scheme
	rbacv1.AddToScheme(k8sscheme.Scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

	// Ensure namespace exists first
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: GitopsNS,
		},
	}
	g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

	t.Run("VerifyDetailedRBACConfiguration", func(t *testing.T) {
		// Create the resources
		err := ensureAddonManagerRBAC(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify Role rules in detail
		role := &rbacv1.Role{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-role",
			Namespace: GitopsNS,
		}, role)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(len(role.Rules)).To(gomega.Equal(1))
		g.Expect(role.Rules[0].APIGroups).To(gomega.Equal([]string{""}))
		g.Expect(role.Rules[0].Resources).To(gomega.Equal([]string{"secrets"}))
		g.Expect(role.Rules[0].Verbs).To(gomega.Equal([]string{"get", "list", "watch"}))

		// Verify RoleBinding subjects and role reference in detail
		roleBinding := &rbacv1.RoleBinding{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-rolebinding",
			Namespace: GitopsNS,
		}, roleBinding)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify subjects with dynamic namespace computation
		g.Expect(len(roleBinding.Subjects)).To(gomega.Equal(1))
		g.Expect(roleBinding.Subjects[0].Kind).To(gomega.Equal("ServiceAccount"))
		g.Expect(roleBinding.Subjects[0].Name).To(gomega.Equal("addon-manager-controller-sa"))
		expectedNamespace := utils.GetComponentNamespace("open-cluster-management") + "-hub"
		g.Expect(roleBinding.Subjects[0].Namespace).To(gomega.Equal(expectedNamespace))

		// Verify role reference
		g.Expect(roleBinding.RoleRef.Kind).To(gomega.Equal("Role"))
		g.Expect(roleBinding.RoleRef.Name).To(gomega.Equal("addon-manager-controller-role"))
		g.Expect(roleBinding.RoleRef.APIGroup).To(gomega.Equal("rbac.authorization.k8s.io"))
	})
}

func TestEnsureArgoCDAgentCASecret(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test case 1: Target secret already exists, should not create
	t.Run("SecretAlreadyExists", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create target namespace
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

		// Create existing target secret
		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-agent-ca",
				Namespace: GitopsNS,
			},
			Data: map[string][]byte{
				"existing": []byte("data"),
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), existingSecret)).NotTo(gomega.HaveOccurred())

		// Run the function
		err := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify the existing secret is unchanged
		secret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca",
			Namespace: GitopsNS,
		}, secret)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(secret.Data["existing"]).To(gomega.Equal([]byte("data")))
	})

	// Test case 2: Source secret doesn't exist, should fail
	t.Run("SourceSecretNotFound", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create target namespace
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

		// Run the function (source secret doesn't exist)
		err := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("source secret multicluster-operators-application-svc-ca not found"))
		g.Expect(err.Error()).To(gomega.ContainSubstring("ensure OCM is properly installed"))
	})

	// Test case 3: Successful copy from source to target
	t.Run("SuccessfulCopy", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create source namespace with dynamic namespace computation
		sourceNamespaceName := utils.GetComponentNamespace("open-cluster-management")
		sourceNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sourceNamespaceName,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceNamespace)).NotTo(gomega.HaveOccurred())

		// Create target namespace
		targetNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), targetNamespace)).NotTo(gomega.HaveOccurred())

		// Create source secret
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multicluster-operators-application-svc-ca",
				Namespace: sourceNamespaceName,
				Labels: map[string]string{
					"test-label": "test-value",
				},
				Annotations: map[string]string{
					"test-annotation": "test-annotation-value",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"ca.crt":  []byte("test-ca-certificate-data"),
				"tls.crt": []byte("test-tls-certificate-data"),
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceSecret)).NotTo(gomega.HaveOccurred())

		// Run the function
		err := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify target secret was created
		targetSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca",
			Namespace: GitopsNS,
		}, targetSecret)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify secret properties
		g.Expect(targetSecret.Name).To(gomega.Equal("argocd-agent-ca"))
		g.Expect(targetSecret.Namespace).To(gomega.Equal(GitopsNS))
		g.Expect(targetSecret.Type).To(gomega.Equal(corev1.SecretTypeOpaque))

		// Verify data was copied correctly
		g.Expect(targetSecret.Data["ca.crt"]).To(gomega.Equal([]byte("test-ca-certificate-data")))
		g.Expect(targetSecret.Data["tls.crt"]).To(gomega.Equal([]byte("test-tls-certificate-data")))

		// Verify labels were copied
		g.Expect(targetSecret.Labels["test-label"]).To(gomega.Equal("test-value"))

		// Verify annotations were copied
		g.Expect(targetSecret.Annotations["test-annotation"]).To(gomega.Equal("test-annotation-value"))
	})

	// Test case 4: Concurrent creation (another process creates secret while we're running)
	t.Run("ConcurrentCreation", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create source namespace with dynamic namespace computation
		sourceNamespaceName := utils.GetComponentNamespace("open-cluster-management")
		sourceNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sourceNamespaceName,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceNamespace)).NotTo(gomega.HaveOccurred())

		// Create target namespace
		targetNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), targetNamespace)).NotTo(gomega.HaveOccurred())

		// Create source secret
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multicluster-operators-application-svc-ca",
				Namespace: sourceNamespaceName,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"ca.crt": []byte("test-ca-certificate-data"),
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceSecret)).NotTo(gomega.HaveOccurred())

		// Create the target secret to simulate concurrent creation
		concurrentSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-agent-ca",
				Namespace: GitopsNS,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"concurrent": []byte("creation"),
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), concurrentSecret)).NotTo(gomega.HaveOccurred())

		// Run the function - should not fail even though secret exists
		err := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify the concurrent secret is still there (unchanged)
		targetSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca",
			Namespace: GitopsNS,
		}, targetSecret)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(targetSecret.Data["concurrent"]).To(gomega.Equal([]byte("creation")))
	})

	// Test case 5: Source secret with nil labels and annotations
	t.Run("SourceSecretWithNilLabelsAndAnnotations", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create source namespace with dynamic namespace computation
		sourceNamespaceName := utils.GetComponentNamespace("open-cluster-management")
		sourceNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sourceNamespaceName,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceNamespace)).NotTo(gomega.HaveOccurred())

		// Create target namespace
		targetNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), targetNamespace)).NotTo(gomega.HaveOccurred())

		// Create source secret with nil labels and annotations
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multicluster-operators-application-svc-ca",
				Namespace: sourceNamespaceName,
				// Labels and Annotations are nil
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"ca.crt": []byte("test-ca-certificate-data"),
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceSecret)).NotTo(gomega.HaveOccurred())

		// Run the function
		err := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify target secret was created with empty but non-nil labels and annotations
		targetSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca",
			Namespace: GitopsNS,
		}, targetSecret)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify labels and annotations are initialized (may be nil or empty map)
		// The fake client may return nil instead of empty map, which is acceptable
		if targetSecret.Labels != nil {
			g.Expect(len(targetSecret.Labels)).To(gomega.Equal(0))
		}

		if targetSecret.Annotations != nil {
			g.Expect(len(targetSecret.Annotations)).To(gomega.Equal(0))
		}

		// Verify data was copied correctly
		g.Expect(targetSecret.Data["ca.crt"]).To(gomega.Equal([]byte("test-ca-certificate-data")))
	})
}

func TestNewNonCachingClient(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("NewNonCachingClientFunction", func(t *testing.T) {
		// Test with nil config should not panic but may return error
		c, err := NewNonCachingClient(nil, client.Options{})

		// We expect an error with nil config
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(c).To(gomega.BeNil())
	})
}

func TestGlobalVariables(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("GlobalVariableDefaults", func(t *testing.T) {
		g.Expect(metricsHost).To(gomega.Equal("0.0.0.0"))
		g.Expect(metricsPort).To(gomega.Equal(8388))
		g.Expect(GitopsNS).To(gomega.Equal("openshift-gitops"))
	})
}

func TestEnsureRBACErrorHandling(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("EnsureRBACWithoutNamespace", func(t *testing.T) {
		// Test RBAC creation without the namespace existing first
		rbacv1.AddToScheme(k8sscheme.Scheme)
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Try to create RBAC without creating namespace first
		err := ensureAddonManagerRBAC(fakeClient)
		// Should still work as Kubernetes will create the resources
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})
}

func TestSecretOperationsEdgeCases(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("SecretWithEmptyData", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create source namespace
		sourceNamespaceName := utils.GetComponentNamespace("open-cluster-management")
		sourceNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sourceNamespaceName,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceNamespace)).NotTo(gomega.HaveOccurred())

		// Create target namespace
		targetNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), targetNamespace)).NotTo(gomega.HaveOccurred())

		// Create source secret with empty data
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multicluster-operators-application-svc-ca",
				Namespace: sourceNamespaceName,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{}, // Empty data
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceSecret)).NotTo(gomega.HaveOccurred())

		// Run the function
		err := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify target secret was created with empty data
		targetSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca",
			Namespace: GitopsNS,
		}, targetSecret)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(len(targetSecret.Data)).To(gomega.Equal(0))
	})

	t.Run("SecretWithDifferentType", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create source namespace
		sourceNamespaceName := utils.GetComponentNamespace("open-cluster-management")
		sourceNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sourceNamespaceName,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceNamespace)).NotTo(gomega.HaveOccurred())

		// Create target namespace
		targetNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), targetNamespace)).NotTo(gomega.HaveOccurred())

		// Create source secret with TLS type
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multicluster-operators-application-svc-ca",
				Namespace: sourceNamespaceName,
			},
			Type: corev1.SecretTypeTLS, // Different type
			Data: map[string][]byte{
				"tls.crt": []byte("test-certificate"),
				"tls.key": []byte("test-private-key"),
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceSecret)).NotTo(gomega.HaveOccurred())

		// Run the function
		err := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify target secret was created with correct type
		targetSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca",
			Namespace: GitopsNS,
		}, targetSecret)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(targetSecret.Type).To(gomega.Equal(corev1.SecretTypeTLS))
		g.Expect(targetSecret.Data["tls.crt"]).To(gomega.Equal([]byte("test-certificate")))
		g.Expect(targetSecret.Data["tls.key"]).To(gomega.Equal([]byte("test-private-key")))
	})
}

func TestNamespaceOperationsEdgeCases(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("NamespaceWithExistingLabels", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create namespace with existing labels
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
				Labels: map[string]string{
					"existing-label": "existing-value",
				},
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

		// Run the function
		err := ensureOpenShiftGitOpsNamespace(fakeClient)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify namespace still exists with original labels
		retrievedNamespace := &corev1.Namespace{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{Name: GitopsNS}, retrievedNamespace)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(retrievedNamespace.Labels["existing-label"]).To(gomega.Equal("existing-value"))
	})
}

func TestRBACResourceCreationErrors(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("RoleCreationIdempotency", func(t *testing.T) {
		rbacv1.AddToScheme(k8sscheme.Scheme)
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create namespace
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

		// Create role multiple times - should be idempotent
		err1 := ensureAddonManagerRole(fakeClient)
		g.Expect(err1).NotTo(gomega.HaveOccurred())

		err2 := ensureAddonManagerRole(fakeClient)
		g.Expect(err2).NotTo(gomega.HaveOccurred())

		err3 := ensureAddonManagerRole(fakeClient)
		g.Expect(err3).NotTo(gomega.HaveOccurred())

		// Verify only one role exists
		role := &rbacv1.Role{}
		err := fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-role",
			Namespace: GitopsNS,
		}, role)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})

	t.Run("RoleBindingCreationIdempotency", func(t *testing.T) {
		rbacv1.AddToScheme(k8sscheme.Scheme)
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create namespace
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), namespace)).NotTo(gomega.HaveOccurred())

		// Create rolebinding multiple times - should be idempotent
		err1 := ensureAddonManagerRoleBinding(fakeClient)
		g.Expect(err1).NotTo(gomega.HaveOccurred())

		err2 := ensureAddonManagerRoleBinding(fakeClient)
		g.Expect(err2).NotTo(gomega.HaveOccurred())

		err3 := ensureAddonManagerRoleBinding(fakeClient)
		g.Expect(err3).NotTo(gomega.HaveOccurred())

		// Verify only one rolebinding exists
		roleBinding := &rbacv1.RoleBinding{}
		err := fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "addon-manager-controller-rolebinding",
			Namespace: GitopsNS,
		}, roleBinding)
		g.Expect(err).NotTo(gomega.HaveOccurred())
	})
}

func TestResourceCleanupBehavior(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("MultipleSecretCopiesIdempotency", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(k8sscheme.Scheme).Build()

		// Create source namespace
		sourceNamespaceName := utils.GetComponentNamespace("open-cluster-management")
		sourceNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sourceNamespaceName,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceNamespace)).NotTo(gomega.HaveOccurred())

		// Create target namespace
		targetNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitopsNS,
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), targetNamespace)).NotTo(gomega.HaveOccurred())

		// Create source secret
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multicluster-operators-application-svc-ca",
				Namespace: sourceNamespaceName,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"ca.crt": []byte("original-data"),
			},
		}
		g.Expect(fakeClient.Create(context.TODO(), sourceSecret)).NotTo(gomega.HaveOccurred())

		// Run the function multiple times
		err1 := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err1).NotTo(gomega.HaveOccurred())

		err2 := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err2).NotTo(gomega.HaveOccurred())

		err3 := ensureArgoCDAgentCASecret(fakeClient)
		g.Expect(err3).NotTo(gomega.HaveOccurred())

		// Verify target secret exists and has expected data
		targetSecret := &corev1.Secret{}
		err := fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca",
			Namespace: GitopsNS,
		}, targetSecret)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(targetSecret.Data["ca.crt"]).To(gomega.Equal([]byte("original-data")))
	})
}
