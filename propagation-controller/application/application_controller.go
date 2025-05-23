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

package application

import (
	"context"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	clusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
)

const (
	// Application annotation that dictates which managed cluster this Application should be pulled to
	AnnotationKeyOCMManagedCluster = "apps.open-cluster-management.io/ocm-managed-cluster"
	// Application annotation that dictates which managed cluster namespace this Application should be pulled to
	AnnotationKeyOCMManagedClusterAppNamespace = "apps.open-cluster-management.io/ocm-managed-cluster-app-namespace"
	// Application and ManifestWork annotation that shows which ApplicationSet is the grand parent of this work
	AnnotationKeyAppSet = "apps.open-cluster-management.io/hosting-applicationset"
	// Application annotation that enables the skip reconciliation of an application
	AnnotationKeyAppSkipReconcile = "argocd.argoproj.io/skip-reconcile"
	// ManifestWork annotation that shows the namespace of the hub Application.
	AnnotationKeyHubApplicationNamespace = "apps.open-cluster-management.io/hub-application-namespace"
	// ManifestWork annotation that shows the name of the hub Application.
	AnnotationKeyHubApplicationName = "apps.open-cluster-management.io/hub-application-name"
	// Application and ManifestWork label that shows that ApplicationSet is the grand parent of this work
	LabelKeyAppSet = "apps.open-cluster-management.io/application-set"
	// ManifestWork label with the ApplicationSet namespace and name in sha1 hash value
	LabelKeyAppSetHash = "apps.open-cluster-management.io/application-set-hash"
	// Application label that enables the pull controller to wrap the Application in ManifestWork payload
	LabelKeyPull = "apps.open-cluster-management.io/pull-to-ocm-managed-cluster"
	// ResourcesFinalizerName is the finalizer value which we inject to finalize deletion of an application
	ResourcesFinalizerName string = "resources-finalizer.argocd.argoproj.io"
)

// ApplicationReconciler reconciles a Application object
type ApplicationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=work.open-cluster-management.io,resources=manifestworks,verbs=get;list;watch;create;update;patch;delete

// ApplicationPredicateFunctions defines which Application this controller should wrap inside ManifestWork's payload
var ApplicationPredicateFunctions = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		newApp := e.ObjectNew.(*unstructured.Unstructured)
		oldApp := e.ObjectOld.(*unstructured.Unstructured)
		oldAppCopy := oldApp.DeepCopy()
		newAppCopy := newApp.DeepCopy()
		unstructured.RemoveNestedField(oldAppCopy.Object, "status")
		unstructured.RemoveNestedField(newAppCopy.Object, "status")
		isChanged := !reflect.DeepEqual(oldAppCopy.Object, newAppCopy.Object)
		return containsValidPullLabel(newApp.GetLabels()) && containsValidPullAnnotation(newApp.GetAnnotations()) && isChanged
	},
	CreateFunc: func(e event.CreateEvent) bool {
		app := e.Object.(*unstructured.Unstructured)
		return containsValidPullLabel(app.GetLabels()) && containsValidPullAnnotation(app.GetAnnotations())
	},

	DeleteFunc: func(e event.DeleteEvent) bool {
		app := e.Object.(*unstructured.Unstructured)
		return containsValidPullLabel(app.GetLabels()) && containsValidPullAnnotation(app.GetAnnotations())
	},
}

// SetupWithManager sets up the controller with the Manager.
func (r *ApplicationReconciler) SetupWithManager(mgr ctrl.Manager, maxConcurrentReconciles int) error {
	applicationGVK := &unstructured.Unstructured{}
	applicationGVK.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		For(applicationGVK).
		WithEventFilter(ApplicationPredicateFunctions).
		Complete(r)
}

func (r *ApplicationReconciler) isLocalCluster(clusterName string) bool {
	managedCluster := &clusterv1.ManagedCluster{}
	managedClusterKey := types.NamespacedName{
		Name: clusterName,
	}
	err := r.Get(context.TODO(), managedClusterKey, managedCluster)

	if err != nil {
		klog.Errorf("Failed to find managed cluster: %v, error: %v ", clusterName, err)
		return false
	}

	labels := managedCluster.GetLabels()

	if labels == nil {
		labels = make(map[string]string)
	}

	if strings.EqualFold(labels["local-cluster"], "true") {
		klog.Infof("This is local-cluster: %v", clusterName)
		return true
	}

	return false
}

// Reconcile create/update/delete ManifestWork with the Application as its payload
func (r *ApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling Application...")

	application := &unstructured.Unstructured{}
	application.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	if err := r.Get(ctx, req.NamespacedName, application); err != nil {
		log.Error(err, "unable to fetch Application")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	managedClusterName := application.GetAnnotations()[AnnotationKeyOCMManagedCluster]

	if r.isLocalCluster(managedClusterName) {
		log.Info("skipping Application with the local-cluster as Managed Cluster")

		return ctrl.Result{}, nil
	}

	mwName := generateManifestWorkName(application.GetName(), application.GetUID())

	// the Application is being deleted, find the ManifestWork and delete that as well
	if application.GetDeletionTimestamp() != nil {
		// remove finalizer from Application but do not 'commit' yet
		if len(application.GetFinalizers()) != 0 {
			f := application.GetFinalizers()
			for i := 0; i < len(f); i++ {
				if f[i] == ResourcesFinalizerName {
					f = append(f[:i], f[i+1:]...)
					i--
				}
			}

			application.SetFinalizers(f)
		}

		// delete the ManifestWork associated with this Application
		var work workv1.ManifestWork
		err := r.Get(ctx, types.NamespacedName{Name: mwName, Namespace: managedClusterName}, &work)

		if errors.IsNotFound(err) {
			// already deleted ManifestWork, commit the Application finalizer removal
			if err = r.Update(ctx, application); err != nil {
				log.Error(err, "unable to update Application")
				return ctrl.Result{}, err
			}
		} else if err != nil {
			log.Error(err, "unable to fetch ManifestWork")
			return ctrl.Result{}, err
		}

		if err := r.Delete(ctx, &work); err != nil {
			log.Error(err, "unable to delete ManifestWork")
			return ctrl.Result{}, err
		}

		// deleted ManifestWork, commit the Application finalizer removal
		if err := r.Update(ctx, application); err != nil {
			log.Error(err, "unable to update Application")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// verify the ManagedCluster actually exists
	var managedCluster clusterv1.ManagedCluster
	if err := r.Get(ctx, types.NamespacedName{Name: managedClusterName}, &managedCluster); err != nil {
		log.Error(err, "unable to fetch ManagedCluster")
		return ctrl.Result{}, err
	}

	log.Info("generating ManifestWork for Application")
	w, err := generateManifestWork(mwName, managedClusterName, application)

	if err != nil {
		log.Error(err, "unable to generating ManifestWork")
		return ctrl.Result{}, err
	}

	// create or update the ManifestWork depends if it already exists or not
	var mw workv1.ManifestWork
	err = r.Get(ctx, types.NamespacedName{Name: mwName, Namespace: managedClusterName}, &mw)

	if errors.IsNotFound(err) {
		err = r.Client.Create(ctx, w)
		if err != nil {
			log.Error(err, "unable to create ManifestWork")
			return ctrl.Result{}, err
		}
	} else if err == nil {
		mw.Annotations = w.Annotations
		mw.Labels = w.Labels
		mw.Spec = w.Spec
		err = r.Client.Update(ctx, &mw)

		if err != nil {
			log.Error(err, "unable to update ManifestWork")
			return ctrl.Result{}, err
		}
	} else {
		log.Error(err, "unable to fetch ManifestWork")
		return ctrl.Result{}, err
	}

	// remove the operation field from application if it exists
	if _, ok := application.Object["operation"]; ok {
		delete(application.Object, "operation")

		if err := r.Update(ctx, application); err != nil {
			log.Error(err, "unable to remove operation from Application")
			return ctrl.Result{}, err
		}
	}

	log.Info("done reconciling Application")

	return ctrl.Result{}, nil
}
