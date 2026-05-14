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
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
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
				Namespace: DefaultSourceNamespace,
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
				Namespace: DefaultSourceNamespace,
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
				Namespace: DefaultSourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(requeueResult()))
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
				Namespace: DefaultSourceNamespace,
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
				Namespace: DefaultSourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(requeueResult()))

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
				Namespace: DefaultSourceNamespace,
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
				Namespace: DefaultSourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(requeueResult()))

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
				Namespace: DefaultSourceNamespace,
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
				Namespace: DefaultSourceNamespace,
			},
		}

		result, err := reconciler.Reconcile(context.TODO(), req)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).To(Equal(requeueResult()))

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
				Namespace: DefaultSourceNamespace,
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
		g.Expect(result).To(Equal(requeueResult()))

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
				Namespace: DefaultSourceNamespace,
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
				Namespace: DefaultSourceNamespace,
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
				Namespace: DefaultSourceNamespace,
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
			Namespace: DefaultSourceNamespace,
		},
	}
	g.Expect(isTargetSecret(targetSecret)).To(BeTrue())

	// Test wrong name
	wrongNameSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wrong-name",
			Namespace: DefaultSourceNamespace,
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
	g.Expect(requests[0].Namespace).To(Equal(DefaultSourceNamespace))

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
				Namespace: DefaultSourceNamespace,
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
				Namespace: DefaultSourceNamespace,
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

func TestTargetSecretPredicate(t *testing.T) {
	g := NewWithT(t)

	// Pin env vars so getTargetNamespace() resolves deterministically
	t.Setenv("ARGOCD_NAMESPACE", GitOpsNamespace)

	// Target secret (argocd-agent-client-tls in openshift-gitops)
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		},
	}

	// DeleteFunc should return true for the target secret
	deleteEvent := event.TypedDeleteEvent[*corev1.Secret]{Object: targetSecret}
	g.Expect(targetSecretPredicateFunc.DeleteFunc(deleteEvent)).To(BeTrue())

	// CreateFunc should return false (we only care about deletions)
	createEvent := event.TypedCreateEvent[*corev1.Secret]{Object: targetSecret}
	g.Expect(targetSecretPredicateFunc.CreateFunc(createEvent)).To(BeFalse())

	// UpdateFunc should return false
	updateEvent := event.TypedUpdateEvent[*corev1.Secret]{ObjectNew: targetSecret}
	g.Expect(targetSecretPredicateFunc.UpdateFunc(updateEvent)).To(BeFalse())

	// Wrong secret name should not match
	wrongSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wrong-name",
			Namespace: GitOpsNamespace,
		},
	}
	deleteEventWrong := event.TypedDeleteEvent[*corev1.Secret]{Object: wrongSecret}
	g.Expect(targetSecretPredicateFunc.DeleteFunc(deleteEventWrong)).To(BeFalse())

	// Wrong namespace should not match
	wrongNs := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetSecretName,
			Namespace: "wrong-namespace",
		},
	}
	deleteEventWrongNs := event.TypedDeleteEvent[*corev1.Secret]{Object: wrongNs}
	g.Expect(targetSecretPredicateFunc.DeleteFunc(deleteEventWrongNs)).To(BeFalse())
}

func TestTargetSecretToSourceMapper(t *testing.T) {
	g := NewWithT(t)

	// Pin env vars so getTargetNamespace() and getSourceNamespace() resolve deterministically
	t.Setenv("ARGOCD_NAMESPACE", GitOpsNamespace)
	t.Setenv("POD_NAMESPACE", DefaultSourceNamespace)

	// Target secret deletion should map back to source secret
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		},
	}
	requests := targetSecretToSourceMapper(context.TODO(), targetSecret)
	g.Expect(requests).To(HaveLen(1))
	g.Expect(requests[0].Name).To(Equal(getSourceSecretName()))
	g.Expect(requests[0].Namespace).To(Equal(DefaultSourceNamespace))

	// Wrong secret should not generate any requests
	wrongSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wrong-name",
			Namespace: GitOpsNamespace,
		},
	}
	requests = targetSecretToSourceMapper(context.TODO(), wrongSecret)
	g.Expect(requests).To(HaveLen(0))
}

func TestTargetSecretDeletionTriggersRecreation(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	// Create source secret and target namespace
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("test-cert-data"),
			"tls.key": []byte("test-key-data"),
		},
	}
	targetNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: GitOpsNamespace,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sourceSecret, targetNs).Build()
	reconciler := &SecretReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
	}

	// First reconcile creates the target secret
	result, err := reconciler.Reconcile(context.TODO(), req)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(requeueResult()))

	// Verify target secret was created
	createdSecret := &corev1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      TargetSecretName,
		Namespace: GitOpsNamespace,
	}, createdSecret)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(createdSecret.Data["tls.crt"]).To(Equal([]byte("test-cert-data")))

	// Delete the target secret (simulating external deletion)
	err = fakeClient.Delete(context.TODO(), createdSecret)
	g.Expect(err).ToNot(HaveOccurred())

	// Re-reconcile (as if triggered by the target secret deletion watch)
	result, err = reconciler.Reconcile(context.TODO(), req)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(requeueResult()))

	// Verify target secret was recreated
	recreatedSecret := &corev1.Secret{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      TargetSecretName,
		Namespace: GitOpsNamespace,
	}, recreatedSecret)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(recreatedSecret.Data["tls.crt"]).To(Equal([]byte("test-cert-data")))
}

func TestSecretDataEqual(t *testing.T) {
	g := NewWithT(t)

	g.Expect(secretDataEqual(nil, nil)).To(BeTrue())
	g.Expect(secretDataEqual(map[string][]byte{}, map[string][]byte{})).To(BeTrue())
	g.Expect(secretDataEqual(
		map[string][]byte{"a": []byte("1")},
		map[string][]byte{"a": []byte("1")},
	)).To(BeTrue())
	g.Expect(secretDataEqual(
		map[string][]byte{"a": []byte("1")},
		map[string][]byte{"a": []byte("2")},
	)).To(BeFalse())
	g.Expect(secretDataEqual(
		map[string][]byte{"a": []byte("1")},
		map[string][]byte{"b": []byte("1")},
	)).To(BeFalse())
	g.Expect(secretDataEqual(
		map[string][]byte{"a": []byte("1")},
		map[string][]byte{"a": []byte("1"), "b": []byte("2")},
	)).To(BeFalse())
}

func TestResyncSkipsWhenUpToDate(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	err := clientgoscheme.AddToScheme(scheme)
	g.Expect(err).ToNot(HaveOccurred())

	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
		Data: map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("key")},
	}
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		},
		Data: map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("key")},
	}
	targetNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: GitOpsNamespace},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sourceSecret, targetSecret, targetNs).Build()
	reconciler := &SecretReconciler{Client: fakeClient, Scheme: scheme}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
	}

	result, err := reconciler.Reconcile(context.TODO(), req)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(requeueResult()))
}

func TestCertRotationRestartsAgentDeployments(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	replicas := int32(1)
	agentDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acm-openshift-gitops-agent",
			Namespace: GitOpsNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "argocd-agent",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "agent"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "agent"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "agent", Image: "test:latest"}},
				},
			},
		},
	}

	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
		Data: map[string][]byte{"tls.crt": []byte("new-cert"), "tls.key": []byte("new-key")},
	}
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		},
		Data: map[string][]byte{"tls.crt": []byte("old-cert"), "tls.key": []byte("old-key")},
	}
	targetNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: GitOpsNamespace},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sourceSecret, targetSecret, targetNs, agentDeploy).Build()
	reconciler := &SecretReconciler{Client: fakeClient, Scheme: scheme}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
	}

	result, err := reconciler.Reconcile(context.TODO(), req)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(requeueResult()))

	// Verify the agent deployment was annotated with cert-rotated-at
	updatedDeploy := &appsv1.Deployment{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "acm-openshift-gitops-agent",
		Namespace: GitOpsNamespace,
	}, updatedDeploy)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updatedDeploy.Spec.Template.Annotations).To(HaveKey("apps.open-cluster-management.io/cert-rotated-at"))
}

func TestNoRestartWhenCertUnchanged(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	replicas := int32(1)
	agentDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acm-openshift-gitops-agent",
			Namespace: GitOpsNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "argocd-agent",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "agent"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "agent"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "agent", Image: "test:latest"}},
				},
			},
		},
	}

	// Source and target have SAME data — no rotation happened
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
		Data: map[string][]byte{"tls.crt": []byte("same-cert"), "tls.key": []byte("same-key")},
	}
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		},
		Data: map[string][]byte{"tls.crt": []byte("same-cert"), "tls.key": []byte("same-key")},
	}
	targetNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: GitOpsNamespace},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sourceSecret, targetSecret, targetNs, agentDeploy).Build()
	reconciler := &SecretReconciler{Client: fakeClient, Scheme: scheme}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
	}

	result, err := reconciler.Reconcile(context.TODO(), req)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(requeueResult()))

	// Verify the agent deployment was NOT annotated (no restart)
	updatedDeploy := &appsv1.Deployment{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "acm-openshift-gitops-agent",
		Namespace: GitOpsNamespace,
	}, updatedDeploy)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updatedDeploy.Spec.Template.Annotations).ToNot(HaveKey("apps.open-cluster-management.io/cert-rotated-at"))
}

func TestRestartNoAgentDeployments(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	// No agent deployments exist (non-agent mode)
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
		Data: map[string][]byte{"tls.crt": []byte("new-cert"), "tls.key": []byte("new-key")},
	}
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		},
		Data: map[string][]byte{"tls.crt": []byte("old-cert"), "tls.key": []byte("old-key")},
	}
	targetNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: GitOpsNamespace},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sourceSecret, targetSecret, targetNs).Build()
	reconciler := &SecretReconciler{Client: fakeClient, Scheme: scheme}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      getSourceSecretName(),
			Namespace: DefaultSourceNamespace,
		},
	}

	// Should succeed without error even with no agent deployments
	result, err := reconciler.Reconcile(context.TODO(), req)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(requeueResult()))
}

func TestGetSourceSecretName(t *testing.T) {
	t.Run("empty or unset environment variable falls back to default", func(t *testing.T) {
		t.Setenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME", "")
		g := NewWithT(t)
		expected := "gitops-addon-open-cluster-management.io-argocd-agent-addon-client-cert"
		g.Expect(getSourceSecretName()).To(Equal(expected))
	})

	t.Run("environment variable override", func(t *testing.T) {
		t.Setenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME", "my-custom-secret-name")
		g := NewWithT(t)
		g.Expect(getSourceSecretName()).To(Equal("my-custom-secret-name"))
	})

	t.Run("whitespace-only environment variable falls back to default", func(t *testing.T) {
		t.Setenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME", "   ")
		g := NewWithT(t)
		expected := "gitops-addon-open-cluster-management.io-argocd-agent-addon-client-cert"
		g.Expect(getSourceSecretName()).To(Equal(expected))
	})
}

func TestGetTargetNamespace(t *testing.T) {
	t.Run("default returns GitOpsNamespace", func(t *testing.T) {
		t.Setenv("ARGOCD_NAMESPACE", "")
		g := NewWithT(t)
		g.Expect(getTargetNamespace()).To(Equal(GitOpsNamespace))
	})

	t.Run("env var overrides to local-cluster", func(t *testing.T) {
		t.Setenv("ARGOCD_NAMESPACE", "local-cluster")
		g := NewWithT(t)
		g.Expect(getTargetNamespace()).To(Equal("local-cluster"))
	})

	t.Run("env var overrides to custom namespace", func(t *testing.T) {
		t.Setenv("ARGOCD_NAMESPACE", "my-custom-ns")
		g := NewWithT(t)
		g.Expect(getTargetNamespace()).To(Equal("my-custom-ns"))
	})

	t.Run("whitespace-only ARGOCD_NAMESPACE falls back to default", func(t *testing.T) {
		t.Setenv("ARGOCD_NAMESPACE", "   ")
		g := NewWithT(t)
		g.Expect(getTargetNamespace()).To(Equal(GitOpsNamespace))
	})
}
