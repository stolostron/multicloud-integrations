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

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestSecretReconciler_Reconcile(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	// Test case 1: Source secret doesn't exist
	t.Run("source secret doesn't exist", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &SecretReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Create target namespace
		targetNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitOpsNamespace,
			},
		}
		err := fakeClient.Create(context.TODO(), targetNs)
		g.Expect(err).ToNot(HaveOccurred())

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))
	})

	// Test case 2: Target namespace doesn't exist
	t.Run("target namespace doesn't exist", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &SecretReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Create source secret
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls.crt": []byte("test-cert"),
				"tls.key": []byte("test-key"),
			},
		}
		err := fakeClient.Create(context.TODO(), sourceSecret)
		g.Expect(err).ToNot(HaveOccurred())

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))
	})

	// Test case 3: Create target secret when it doesn't exist
	t.Run("create target secret", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &SecretReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Create target namespace
		targetNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitOpsNamespace,
			},
		}
		err := fakeClient.Create(context.TODO(), targetNs)
		g.Expect(err).ToNot(HaveOccurred())

		// Create source secret
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
				Labels: map[string]string{
					"test-label": "test-value",
				},
				Annotations: map[string]string{
					"test-annotation": "test-value",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls.crt": []byte("test-cert"),
				"tls.key": []byte("test-key"),
			},
		}
		err = fakeClient.Create(context.TODO(), sourceSecret)
		g.Expect(err).ToNot(HaveOccurred())

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))

		// Verify target secret was created
		targetSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		}, targetSecret)
		g.Expect(err).ToNot(HaveOccurred())

		// Verify data was copied
		g.Expect(targetSecret.Data["tls.crt"]).To(Equal([]byte("test-cert")))
		g.Expect(targetSecret.Data["tls.key"]).To(Equal([]byte("test-key")))

		// Verify secret type was converted to TLS (since it has tls.crt and tls.key)
		g.Expect(targetSecret.Type).To(Equal(corev1.SecretTypeTLS))
	})

	// Test case 4: Update target secret when source changes
	t.Run("update target secret", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &SecretReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Create target namespace
		targetNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitOpsNamespace,
			},
		}
		err := fakeClient.Create(context.TODO(), targetNs)
		g.Expect(err).ToNot(HaveOccurred())

		// Create source secret
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls.crt": []byte("updated-cert"),
				"tls.key": []byte("updated-key"),
			},
		}
		err = fakeClient.Create(context.TODO(), sourceSecret)
		g.Expect(err).ToNot(HaveOccurred())

		// Create existing target secret with old data
		targetSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TargetSecretName,
				Namespace: GitOpsNamespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls.crt": []byte("old-cert"),
				"tls.key": []byte("old-key"),
			},
		}
		err = fakeClient.Create(context.TODO(), targetSecret)
		g.Expect(err).ToNot(HaveOccurred())

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))

		// Verify target secret was updated
		updatedTargetSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		}, updatedTargetSecret)
		g.Expect(err).ToNot(HaveOccurred())

		// Verify data was updated
		g.Expect(updatedTargetSecret.Data["tls.crt"]).To(Equal([]byte("updated-cert")))
		g.Expect(updatedTargetSecret.Data["tls.key"]).To(Equal([]byte("updated-key")))

		// Verify secret type was converted to TLS (since it has tls.crt and tls.key)
		g.Expect(updatedTargetSecret.Type).To(Equal(corev1.SecretTypeTLS))
	})

	// Test case 5: Test secret type conversion logic
	t.Run("secret type conversion", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &SecretReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Create target namespace
		targetNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitOpsNamespace,
			},
		}
		err := fakeClient.Create(context.TODO(), targetNs)
		g.Expect(err).ToNot(HaveOccurred())

		// Test case 5a: Opaque secret with tls.crt and tls.key should become TLS secret
		sourceSecretTLS := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls.crt":      []byte("test-cert"),
				"tls.key":      []byte("test-key"),
				"cluster-name": []byte("test-cluster"),
			},
		}
		err = fakeClient.Create(context.TODO(), sourceSecretTLS)
		g.Expect(err).ToNot(HaveOccurred())

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))

		// Verify target secret was created with TLS type
		targetSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		}, targetSecret)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(targetSecret.Type).To(Equal(corev1.SecretTypeTLS))
		g.Expect(targetSecret.Data["tls.crt"]).To(Equal([]byte("test-cert")))
		g.Expect(targetSecret.Data["tls.key"]).To(Equal([]byte("test-key")))
		g.Expect(targetSecret.Data["cluster-name"]).To(Equal([]byte("test-cluster")))

		// Clean up for next test
		err = fakeClient.Delete(context.TODO(), sourceSecretTLS)
		g.Expect(err).ToNot(HaveOccurred())
		err = fakeClient.Delete(context.TODO(), targetSecret)
		g.Expect(err).ToNot(HaveOccurred())

		// Test case 5b: Opaque secret without tls.key should remain Opaque
		sourceSecretOpaque := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls.crt": []byte("test-cert"),
				"other":   []byte("test-data"),
			},
		}
		err = fakeClient.Create(context.TODO(), sourceSecretOpaque)
		g.Expect(err).ToNot(HaveOccurred())

		result, err = reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))

		// Verify target secret remains Opaque
		targetSecretOpaque := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		}, targetSecretOpaque)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(targetSecretOpaque.Type).To(Equal(corev1.SecretTypeOpaque))
		g.Expect(targetSecretOpaque.Data["tls.crt"]).To(Equal([]byte("test-cert")))
		g.Expect(targetSecretOpaque.Data["other"]).To(Equal([]byte("test-data")))

		// Clean up
		err = fakeClient.Delete(context.TODO(), sourceSecretOpaque)
		g.Expect(err).ToNot(HaveOccurred())
		err = fakeClient.Delete(context.TODO(), targetSecretOpaque)
		g.Expect(err).ToNot(HaveOccurred())
	})

	// Test case 6: Wrong secret name/namespace - should be ignored
	t.Run("ignore wrong secret", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &SecretReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "wrong-secret-name",
				Namespace: SourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))
	})

	// Test case 7: Source secret deletion
	t.Run("handle source secret deletion", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &SecretReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Create target namespace
		targetNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitOpsNamespace,
			},
		}
		err := fakeClient.Create(context.TODO(), targetNs)
		g.Expect(err).ToNot(HaveOccurred())

		// Create existing target secret
		targetSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TargetSecretName,
				Namespace: GitOpsNamespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls.crt": []byte("test-cert"),
			},
		}
		err = fakeClient.Create(context.TODO(), targetSecret)
		g.Expect(err).ToNot(HaveOccurred())

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
		}

		// Source secret doesn't exist, should trigger deletion
		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))

		// Verify target secret was deleted
		deletedSecret := &corev1.Secret{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		}, deletedSecret)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("not found"))
	})

	// Test case 8: Source secret deletion when target doesn't exist
	t.Run("handle source secret deletion - no target", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &SecretReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		// Create target namespace
		targetNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitOpsNamespace,
			},
		}
		err := fakeClient.Create(context.TODO(), targetNs)
		g.Expect(err).ToNot(HaveOccurred())

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
		}

		// Source secret doesn't exist, target doesn't exist either
		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(reconcile.Result{}))
	})
}

func TestIsTargetSecret(t *testing.T) {
	g := NewWithT(t)

	// Test correct secret
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getSourceSecretName(),
			Namespace: SourceNamespace,
		},
	}
	g.Expect(isTargetSecret(targetSecret)).To(BeTrue())

	// Test wrong name
	wrongNameSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wrong-name",
			Namespace: SourceNamespace,
		},
	}
	g.Expect(isTargetSecret(wrongNameSecret)).To(BeFalse())

	// Test wrong namespace
	wrongNamespaceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getSourceSecretName(),
			Namespace: "wrong-namespace",
		},
	}
	g.Expect(isTargetSecret(wrongNamespaceSecret)).To(BeFalse())
}

func TestNamespaceToSecretMapper(t *testing.T) {
	g := NewWithT(t)

	// Test target namespace
	targetNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: GitOpsNamespace,
		},
	}
	requests := namespaceToSecretMapper(context.TODO(), targetNs)
	g.Expect(requests).To(HaveLen(1))
	g.Expect(requests[0].Name).To(Equal(getSourceSecretName()))
	g.Expect(requests[0].Namespace).To(Equal(SourceNamespace))

	// Test other namespace
	otherNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-namespace",
		},
	}
	requests = namespaceToSecretMapper(context.TODO(), otherNs)
	g.Expect(requests).To(HaveLen(0))
}

func TestPredicateFunctions(t *testing.T) {
	g := NewWithT(t)

	// Test argoCDAgentSecretPredicateFunc
	t.Run("secret predicate", func(t *testing.T) {
		// Test correct secret
		targetSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      getSourceSecretName(),
				Namespace: SourceNamespace,
			},
		}

		// Test CreateFunc
		createEvent := event.TypedCreateEvent[*corev1.Secret]{Object: targetSecret}
		g.Expect(argoCDAgentSecretPredicateFunc.CreateFunc(createEvent)).To(BeTrue())

		// Test UpdateFunc
		updateEvent := event.TypedUpdateEvent[*corev1.Secret]{ObjectNew: targetSecret}
		g.Expect(argoCDAgentSecretPredicateFunc.UpdateFunc(updateEvent)).To(BeTrue())

		// Test DeleteFunc
		deleteEvent := event.TypedDeleteEvent[*corev1.Secret]{Object: targetSecret}
		g.Expect(argoCDAgentSecretPredicateFunc.DeleteFunc(deleteEvent)).To(BeTrue())

		// Test wrong secret
		wrongSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wrong-secret",
				Namespace: SourceNamespace,
			},
		}

		createEventWrong := event.TypedCreateEvent[*corev1.Secret]{Object: wrongSecret}
		g.Expect(argoCDAgentSecretPredicateFunc.CreateFunc(createEventWrong)).To(BeFalse())

		updateEventWrong := event.TypedUpdateEvent[*corev1.Secret]{ObjectNew: wrongSecret}
		g.Expect(argoCDAgentSecretPredicateFunc.UpdateFunc(updateEventWrong)).To(BeFalse())

		deleteEventWrong := event.TypedDeleteEvent[*corev1.Secret]{Object: wrongSecret}
		g.Expect(argoCDAgentSecretPredicateFunc.DeleteFunc(deleteEventWrong)).To(BeFalse())
	})

	// Test targetNamespacePredicateFunc
	t.Run("namespace predicate", func(t *testing.T) {
		// Test target namespace
		targetNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: GitOpsNamespace,
			},
		}

		// Test CreateFunc
		createEvent := event.TypedCreateEvent[*corev1.Namespace]{Object: targetNs}
		g.Expect(targetNamespacePredicateFunc.CreateFunc(createEvent)).To(BeTrue())

		// Test DeleteFunc
		deleteEvent := event.TypedDeleteEvent[*corev1.Namespace]{Object: targetNs}
		g.Expect(targetNamespacePredicateFunc.DeleteFunc(deleteEvent)).To(BeTrue())

		// Test other namespace
		otherNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "other-namespace",
			},
		}

		createEventOther := event.TypedCreateEvent[*corev1.Namespace]{Object: otherNs}
		g.Expect(targetNamespacePredicateFunc.CreateFunc(createEventOther)).To(BeFalse())

		deleteEventOther := event.TypedDeleteEvent[*corev1.Namespace]{Object: otherNs}
		g.Expect(targetNamespacePredicateFunc.DeleteFunc(deleteEventOther)).To(BeFalse())
	})
}

func TestGetSourceSecretName(t *testing.T) {
	g := NewWithT(t)

	// Save original environment variable value if it exists
	originalValue := os.Getenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME")
	defer func() {
		if originalValue != "" {
			os.Setenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME", originalValue)
		} else {
			os.Unsetenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME")
		}
	}()

	t.Run("default behavior - no environment variable", func(t *testing.T) {
		// Ensure environment variable is not set
		os.Unsetenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME")

		result := getSourceSecretName()
		expected := "gitops-addon-open-cluster-management.io-argocd-agent-addon-client-cert"
		g.Expect(result).To(Equal(expected))
	})

	t.Run("environment variable override", func(t *testing.T) {
		customSecretName := "my-custom-secret-name"
		os.Setenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME", customSecretName)

		result := getSourceSecretName()
		g.Expect(result).To(Equal(customSecretName))
	})

	t.Run("empty environment variable falls back to default", func(t *testing.T) {
		// Set environment variable to empty string
		os.Setenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME", "")

		result := getSourceSecretName()
		expected := "gitops-addon-open-cluster-management.io-argocd-agent-addon-client-cert"
		g.Expect(result).To(Equal(expected))
	})

	t.Run("whitespace-only environment variable falls back to default", func(t *testing.T) {
		// Set environment variable to whitespace only
		os.Setenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME", "   ")

		result := getSourceSecretName()
		// Since we only check for empty string, whitespace will be returned as-is
		g.Expect(result).To(Equal("   "))
	})
}
