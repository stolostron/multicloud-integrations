/*
Copyright 2022.

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

package maestroapplication

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fakeworkclient "open-cluster-management.io/api/client/work/clientset/versioned/fake"
	workv1client "open-cluster-management.io/api/client/work/clientset/versioned/typed/work/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
)

var _ = Describe("ApplicationReconciler", func() {
	var (
		reconciler        *ApplicationReconciler
		fakeClient        client.Client
		fakeMaestroClient workv1client.WorkV1Interface
		ctx               context.Context
		application       *unstructured.Unstructured
		managedCluster    *clusterv1.ManagedCluster
		localCluster      *clusterv1.ManagedCluster
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme := runtime.NewScheme()
		_ = clusterv1.AddToScheme(scheme)
		_ = workv1.AddToScheme(scheme)

		// Create fake Kubernetes client
		fakeClient = fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		// Create fake Maestro work client
		fakeWorkClientset := fakeworkclient.NewSimpleClientset()
		fakeMaestroClient = fakeWorkClientset.WorkV1()

		reconciler = &ApplicationReconciler{
			Client:            fakeClient,
			Scheme:            scheme,
			MaestroWorkClient: fakeMaestroClient,
		}

		// Create test application
		application = &unstructured.Unstructured{}
		application.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "argoproj.io",
			Version: "v1alpha1",
			Kind:    "Application",
		})
		application.SetNamespace("test-namespace")
		application.SetName("test-app")
		application.SetAnnotations(map[string]string{
			AnnotationKeyOCMManagedCluster: "test-cluster",
		})
		application.SetLabels(map[string]string{
			LabelKeyPull: "true",
		})

		// Create test managed cluster
		managedCluster = &clusterv1.ManagedCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cluster",
			},
		}

		// Create local cluster for testing
		localCluster = &clusterv1.ManagedCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "local-cluster",
				Labels: map[string]string{
					"local-cluster": "true",
				},
			},
		}
	})

	Context("when reconciling an Application", func() {
		It("should successfully create ManifestWork for valid application", func() {
			// Setup: Create managed cluster in fake client
			Expect(fakeClient.Create(ctx, managedCluster)).To(Succeed())
			Expect(fakeClient.Create(ctx, application)).To(Succeed())

			// Test
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: application.GetNamespace(),
					Name:      application.GetName(),
				},
			}

			result, err := reconciler.Reconcile(ctx, req)

			// Assert
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Verify ManifestWork was created in Maestro
			mwName := generateMaestroManifestWorkName(application.GetNamespace(), application.GetName())
			mw, err := fakeMaestroClient.ManifestWorks("test-cluster").Get(ctx, mwName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(mw.Name).To(Equal(mwName))
			Expect(mw.Namespace).To(Equal("test-cluster"))
		})

		It("should skip reconciliation for local cluster", func() {
			// Setup: Create local cluster and application pointing to it
			Expect(fakeClient.Create(ctx, localCluster)).To(Succeed())

			app := application.DeepCopy()
			app.SetAnnotations(map[string]string{
				AnnotationKeyOCMManagedCluster: "local-cluster",
			})
			Expect(fakeClient.Create(ctx, app)).To(Succeed())

			// Test
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: app.GetNamespace(),
					Name:      app.GetName(),
				},
			}

			result, err := reconciler.Reconcile(ctx, req)

			// Assert
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Verify no ManifestWork was created
			mwName := generateMaestroManifestWorkName(app.GetNamespace(), app.GetName())
			_, err = fakeMaestroClient.ManifestWorks("local-cluster").Get(ctx, mwName, metav1.GetOptions{})
			Expect(err).To(HaveOccurred()) // Should not exist
		})

		It("should handle application with deletion timestamp", func() {
			// Setup: Create managed cluster first
			Expect(fakeClient.Create(ctx, managedCluster)).To(Succeed())

			// Create application with deletion timestamp and finalizers
			app := application.DeepCopy()
			now := metav1.Now()
			app.SetDeletionTimestamp(&now)
			app.SetFinalizers([]string{ResourcesFinalizerName})
			Expect(fakeClient.Create(ctx, app)).To(Succeed())

			// Test
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: app.GetNamespace(),
					Name:      app.GetName(),
				},
			}

			result, err := reconciler.Reconcile(ctx, req)

			// Assert - Should complete without error
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("should return error when managed cluster does not exist", func() {
			// Setup: Create application without creating the managed cluster
			Expect(fakeClient.Create(ctx, application)).To(Succeed())

			// Test
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: application.GetNamespace(),
					Name:      application.GetName(),
				},
			}

			result, err := reconciler.Reconcile(ctx, req)

			// Assert
			Expect(err).To(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("should update existing ManifestWork", func() {
			// Setup: Create managed cluster and application
			Expect(fakeClient.Create(ctx, managedCluster)).To(Succeed())
			Expect(fakeClient.Create(ctx, application)).To(Succeed())

			// Create existing ManifestWork with proper resource version
			mwName := generateMaestroManifestWorkName(application.GetNamespace(), application.GetName())
			existingMW := &workv1.ManifestWork{
				ObjectMeta: metav1.ObjectMeta{
					Name:            mwName,
					Namespace:       "test-cluster",
					ResourceVersion: "1",
				},
				Spec: workv1.ManifestWorkSpec{
					Workload: workv1.ManifestsTemplate{
						Manifests: []workv1.Manifest{},
					},
				},
			}
			_, err := fakeMaestroClient.ManifestWorks("test-cluster").Create(ctx, existingMW, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			// Test
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: application.GetNamespace(),
					Name:      application.GetName(),
				},
			}

			result, err := reconciler.Reconcile(ctx, req)

			// Assert
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Verify ManifestWork exists and has been processed
			mw, err := fakeMaestroClient.ManifestWorks("test-cluster").Get(ctx, mwName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(mw.Name).To(Equal(mwName))
			Expect(mw.ResourceVersion).NotTo(BeEmpty())

			// Verify the ManifestWork has the expected labels and annotations
			Expect(mw.Labels).To(HaveKey(LabelKeyPull))
			Expect(mw.Annotations).To(HaveKey(AnnotationKeyHubApplicationNamespace))
			Expect(mw.Annotations).To(HaveKey(AnnotationKeyHubApplicationName))
		})
	})

	Context("when testing isLocalCluster method", func() {
		It("should return true for cluster with local-cluster label set to true", func() {
			// Setup
			Expect(fakeClient.Create(ctx, localCluster)).To(Succeed())

			// Test
			result := reconciler.isLocalCluster("local-cluster")

			// Assert
			Expect(result).To(BeTrue())
		})

		It("should return false for cluster without local-cluster label", func() {
			// Setup
			Expect(fakeClient.Create(ctx, managedCluster)).To(Succeed())

			// Test
			result := reconciler.isLocalCluster("test-cluster")

			// Assert
			Expect(result).To(BeFalse())
		})

		It("should return false for cluster with local-cluster label set to false", func() {
			// Setup
			cluster := managedCluster.DeepCopy()
			cluster.SetLabels(map[string]string{
				"local-cluster": "false",
			})
			Expect(fakeClient.Create(ctx, cluster)).To(Succeed())

			// Test
			result := reconciler.isLocalCluster("test-cluster")

			// Assert
			Expect(result).To(BeFalse())
		})

		It("should return false for non-existent cluster", func() {
			// Test
			result := reconciler.isLocalCluster("non-existent-cluster")

			// Assert
			Expect(result).To(BeFalse())
		})
	})

	Context("when testing deleteManifestworkFromMaestro method", func() {
		It("should successfully delete existing ManifestWork", func() {
			// Setup: Create ManifestWork
			mwName := "test-mw"
			clusterName := "test-cluster"
			mw := &workv1.ManifestWork{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mwName,
					Namespace: clusterName,
				},
			}
			_, err := fakeMaestroClient.ManifestWorks(clusterName).Create(ctx, mw, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			// Test
			err = reconciler.deleteManifestworkFromMaestro(ctx, clusterName, mwName)

			// Assert
			Expect(err).NotTo(HaveOccurred())

			// Verify ManifestWork was deleted
			_, err = fakeMaestroClient.ManifestWorks(clusterName).Get(ctx, mwName, metav1.GetOptions{})
			Expect(err).To(HaveOccurred()) // Should not exist anymore
		})

		It("should handle deletion of non-existent ManifestWork gracefully", func() {
			// Test
			err := reconciler.deleteManifestworkFromMaestro(ctx, "test-cluster", "non-existent-mw")

			// Assert
			Expect(err).NotTo(HaveOccurred()) // Should not return error
		})
	})

	Context("when testing SetupWithManager", func() {
		It("should setup controller successfully", func() {
			// Create a fake manager
			mgr, err := ctrl.NewManager(&rest.Config{}, ctrl.Options{
				Scheme: reconciler.Scheme,
			})
			Expect(err).NotTo(HaveOccurred())

			// Test
			err = reconciler.SetupWithManager(mgr, 5)

			// Assert
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("ApplicationPredicateFunctions", func() {
	var (
		validApp      *unstructured.Unstructured
		invalidApp    *unstructured.Unstructured
		validLabels   map[string]string
		validAnnos    map[string]string
		invalidLabels map[string]string
		invalidAnnos  map[string]string
	)

	BeforeEach(func() {
		validLabels = map[string]string{
			LabelKeyPull: "true",
		}
		validAnnos = map[string]string{
			AnnotationKeyOCMManagedCluster: "test-cluster",
		}
		invalidLabels = map[string]string{
			LabelKeyPull: "false",
		}
		invalidAnnos = map[string]string{
			"other-annotation": "value",
		}

		validApp = &unstructured.Unstructured{}
		validApp.SetLabels(validLabels)
		validApp.SetAnnotations(validAnnos)

		invalidApp = &unstructured.Unstructured{}
		invalidApp.SetLabels(invalidLabels)
		invalidApp.SetAnnotations(invalidAnnos)
	})

	Context("CreateFunc", func() {
		It("should return true for application with valid labels and annotations", func() {
			e := event.CreateEvent{
				Object: validApp,
			}

			result := ApplicationPredicateFunctions.CreateFunc(e)
			Expect(result).To(BeTrue())
		})

		It("should return false for application with invalid labels", func() {
			e := event.CreateEvent{
				Object: invalidApp,
			}

			result := ApplicationPredicateFunctions.CreateFunc(e)
			Expect(result).To(BeFalse())
		})

		It("should return false for application without pull label", func() {
			app := &unstructured.Unstructured{}
			app.SetAnnotations(validAnnos)
			e := event.CreateEvent{
				Object: app,
			}

			result := ApplicationPredicateFunctions.CreateFunc(e)
			Expect(result).To(BeFalse())
		})

		It("should return false for application without managed cluster annotation", func() {
			app := &unstructured.Unstructured{}
			app.SetLabels(validLabels)
			e := event.CreateEvent{
				Object: app,
			}

			result := ApplicationPredicateFunctions.CreateFunc(e)
			Expect(result).To(BeFalse())
		})
	})

	Context("DeleteFunc", func() {
		It("should return true for application with valid labels and annotations", func() {
			e := event.DeleteEvent{
				Object: validApp,
			}

			result := ApplicationPredicateFunctions.DeleteFunc(e)
			Expect(result).To(BeTrue())
		})

		It("should return false for application with invalid labels", func() {
			e := event.DeleteEvent{
				Object: invalidApp,
			}

			result := ApplicationPredicateFunctions.DeleteFunc(e)
			Expect(result).To(BeFalse())
		})
	})

	Context("UpdateFunc", func() {
		It("should return true when valid application spec changes", func() {
			oldApp := validApp.DeepCopy()
			newApp := validApp.DeepCopy()

			// Change spec (not status)
			newApp.Object["spec"] = map[string]interface{}{
				"source": map[string]interface{}{
					"repoURL": "https://github.com/example/new-repo",
				},
			}

			e := event.UpdateEvent{
				ObjectOld: oldApp,
				ObjectNew: newApp,
			}

			result := ApplicationPredicateFunctions.UpdateFunc(e)
			Expect(result).To(BeTrue())
		})

		It("should return false when only status changes", func() {
			oldApp := validApp.DeepCopy()
			newApp := validApp.DeepCopy()

			// Change only status
			newApp.Object["status"] = map[string]interface{}{
				"health": map[string]interface{}{
					"status": "Healthy",
				},
			}

			e := event.UpdateEvent{
				ObjectOld: oldApp,
				ObjectNew: newApp,
			}

			result := ApplicationPredicateFunctions.UpdateFunc(e)
			Expect(result).To(BeFalse())
		})

		It("should return false when application has invalid labels", func() {
			oldApp := invalidApp.DeepCopy()
			newApp := invalidApp.DeepCopy()

			// Change spec
			newApp.Object["spec"] = map[string]interface{}{
				"source": map[string]interface{}{
					"repoURL": "https://github.com/example/new-repo",
				},
			}

			e := event.UpdateEvent{
				ObjectOld: oldApp,
				ObjectNew: newApp,
			}

			result := ApplicationPredicateFunctions.UpdateFunc(e)
			Expect(result).To(BeFalse())
		})

		It("should return false when no changes are detected", func() {
			oldApp := validApp.DeepCopy()
			newApp := validApp.DeepCopy()

			e := event.UpdateEvent{
				ObjectOld: oldApp,
				ObjectNew: newApp,
			}

			result := ApplicationPredicateFunctions.UpdateFunc(e)
			Expect(result).To(BeFalse())
		})
	})
})

var _ = Describe("Helper Functions", func() {
	Context("containsValidPullLabel", func() {
		It("should return true for valid pull label", func() {
			labels := map[string]string{
				LabelKeyPull: "true",
			}
			result := containsValidPullLabel(labels)
			Expect(result).To(BeTrue())
		})

		It("should return false for invalid pull label value", func() {
			labels := map[string]string{
				LabelKeyPull: "false",
			}
			result := containsValidPullLabel(labels)
			Expect(result).To(BeFalse())
		})

		It("should return false for missing pull label", func() {
			labels := map[string]string{
				"other-label": "value",
			}
			result := containsValidPullLabel(labels)
			Expect(result).To(BeFalse())
		})

		It("should return false for empty labels", func() {
			result := containsValidPullLabel(nil)
			Expect(result).To(BeFalse())

			result = containsValidPullLabel(map[string]string{})
			Expect(result).To(BeFalse())
		})

		It("should return false for invalid boolean value", func() {
			labels := map[string]string{
				LabelKeyPull: "invalid",
			}
			result := containsValidPullLabel(labels)
			Expect(result).To(BeFalse())
		})
	})

	Context("containsValidPullAnnotation", func() {
		It("should return true for valid managed cluster annotation", func() {
			annos := map[string]string{
				AnnotationKeyOCMManagedCluster: "test-cluster",
			}
			result := containsValidPullAnnotation(annos)
			Expect(result).To(BeTrue())
		})

		It("should return false for empty managed cluster annotation", func() {
			annos := map[string]string{
				AnnotationKeyOCMManagedCluster: "",
			}
			result := containsValidPullAnnotation(annos)
			Expect(result).To(BeFalse())
		})

		It("should return false for missing managed cluster annotation", func() {
			annos := map[string]string{
				"other-annotation": "value",
			}
			result := containsValidPullAnnotation(annos)
			Expect(result).To(BeFalse())
		})

		It("should return false for empty annotations", func() {
			result := containsValidPullAnnotation(nil)
			Expect(result).To(BeFalse())

			result = containsValidPullAnnotation(map[string]string{})
			Expect(result).To(BeFalse())
		})
	})

	Context("generateMaestroManifestWorkName", func() {
		It("should generate correct ManifestWork name", func() {
			appNs := "test-namespace"
			appName := "test-app"
			expected := "test-namespace-test-app"

			result := generateMaestroManifestWorkName(appNs, appName)
			Expect(result).To(Equal(expected))
		})

		It("should handle empty values", func() {
			result := generateMaestroManifestWorkName("", "")
			Expect(result).To(Equal("-"))

			result = generateMaestroManifestWorkName("ns", "")
			Expect(result).To(Equal("ns-"))

			result = generateMaestroManifestWorkName("", "app")
			Expect(result).To(Equal("-app"))
		})
	})
})
