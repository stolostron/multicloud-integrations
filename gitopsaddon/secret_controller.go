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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	SourceNamespace  = "open-cluster-management-agent-addon"
	TargetSecretName = "argocd-agent-client-tls" // #nosec G101
)

// getSourceSecretName returns the source secret name, allowing override via environment variable
func getSourceSecretName() string {
	if name := os.Getenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME"); name != "" {
		return name
	}

	return "gitops-addon-open-cluster-management.io-argocd-agent-addon-client-cert"
}

// SecretReconciler reconciles ArgoCD agent client cert secrets
type SecretReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// SetupSecretControllerWithManager sets up the secret controller with the Manager
func SetupSecretControllerWithManager(mgr manager.Manager) error {
	r := &SecretReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}

	// Create a new controller
	skipValidation := true
	c, err := controller.New("gitopsaddon-secret-controller", mgr, controller.Options{
		Reconciler:         r,
		SkipNameValidation: &skipValidation,
	})

	if err != nil {
		return err
	}

	// Watch for the specific ArgoCD agent client cert secret in the source namespace
	err = c.Watch(
		source.Kind(
			mgr.GetCache(),
			&corev1.Secret{},
			&handler.TypedEnqueueRequestForObject[*corev1.Secret]{},
			argoCDAgentSecretPredicateFunc,
		),
	)
	if err != nil {
		return err
	}

	// Watch for namespace creation/deletion to handle target namespace availability
	err = c.Watch(
		source.Kind(
			mgr.GetCache(),
			&corev1.Namespace{},
			handler.TypedEnqueueRequestsFromMapFunc[*corev1.Namespace](namespaceToSecretMapper),
			targetNamespacePredicateFunc,
		),
	)
	if err != nil {
		return err
	}

	return nil
}

// argoCDAgentSecretPredicateFunc filters events for the specific ArgoCD agent client cert secret
var argoCDAgentSecretPredicateFunc = predicate.TypedFuncs[*corev1.Secret]{
	CreateFunc: func(e event.TypedCreateEvent[*corev1.Secret]) bool {
		return isTargetSecret(e.Object)
	},
	UpdateFunc: func(e event.TypedUpdateEvent[*corev1.Secret]) bool {
		return isTargetSecret(e.ObjectNew)
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*corev1.Secret]) bool {
		return isTargetSecret(e.Object)
	},
}

// targetNamespacePredicateFunc filters events for the target namespace
var targetNamespacePredicateFunc = predicate.TypedFuncs[*corev1.Namespace]{
	CreateFunc: func(e event.TypedCreateEvent[*corev1.Namespace]) bool {
		return e.Object.Name == GitOpsNamespace
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*corev1.Namespace]) bool {
		return e.Object.Name == GitOpsNamespace
	},
}

// isTargetSecret checks if the secret is the one we want to watch
func isTargetSecret(secret *corev1.Secret) bool {
	return secret.Name == getSourceSecretName() && secret.Namespace == SourceNamespace
}

// namespaceToSecretMapper maps namespace events to secret reconcile requests
func namespaceToSecretMapper(ctx context.Context, obj *corev1.Namespace) []reconcile.Request {
	if obj.Name == GitOpsNamespace {
		// When the target namespace is created or deleted, trigger reconciliation of the source secret
		return []reconcile.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      getSourceSecretName(),
					Namespace: SourceNamespace,
				},
			},
		}
	}

	return []reconcile.Request{}
}

// Reconcile handles secret events
func (r *SecretReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("Reconciling ArgoCD agent client cert secret: %s", request.NamespacedName)

	// Only process the specific secret we care about
	if request.Name != getSourceSecretName() || request.Namespace != SourceNamespace {
		klog.V(4).Infof("Ignoring secret %s, not the target secret", request.NamespacedName)
		return reconcile.Result{}, nil
	}

	// Check if target namespace exists
	targetNs := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: GitOpsNamespace}, targetNs)

	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Target namespace %s does not exist, skipping secret copy", GitOpsNamespace)
			return reconcile.Result{}, nil
		}

		klog.Errorf("Failed to check target namespace %s: %v", GitOpsNamespace, err)

		return reconcile.Result{}, err
	}

	// Get the source secret
	sourceSecret := &corev1.Secret{}
	err = r.Get(ctx, request.NamespacedName, sourceSecret)

	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Source secret %s not found, checking if target secret should be deleted", request.NamespacedName)
			return r.handleSourceSecretDeletion(ctx)
		}

		klog.Errorf("Failed to get source secret %s: %v", request.NamespacedName, err)

		return reconcile.Result{}, err
	}

	// Always create or update the target secret since reconciliation was triggered
	targetSecretKey := types.NamespacedName{
		Name:      TargetSecretName,
		Namespace: GitOpsNamespace,
	}

	klog.Infof("Creating/updating target secret %s", targetSecretKey)

	return r.createOrUpdateSecretCopy(ctx, sourceSecret)
}

// handleSourceSecretDeletion handles the case when the source secret is deleted
func (r *SecretReconciler) handleSourceSecretDeletion(ctx context.Context) (reconcile.Result, error) {
	// Delete the target secret if it exists
	targetSecret := &corev1.Secret{}
	targetSecretKey := types.NamespacedName{
		Name:      TargetSecretName,
		Namespace: GitOpsNamespace,
	}

	err := r.Get(ctx, targetSecretKey, targetSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(4).Infof("Target secret %s does not exist, nothing to clean up", targetSecretKey)
			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, err
	}

	klog.Infof("Source secret deleted, removing target secret %s", targetSecretKey)
	err = r.Delete(ctx, targetSecret)

	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("Failed to delete target secret %s: %v", targetSecretKey, err)
		return reconcile.Result{}, err
	}

	klog.Infof("Successfully deleted target secret %s", targetSecretKey)

	return reconcile.Result{}, nil
}

// createOrUpdateSecretCopy creates or updates the target secret with data from the source secret
func (r *SecretReconciler) createOrUpdateSecretCopy(ctx context.Context, sourceSecret *corev1.Secret) (reconcile.Result, error) {
	// Determine the correct secret type - if it has tls.crt and tls.key, it should be a TLS secret
	secretType := sourceSecret.Type

	if _, hasCert := sourceSecret.Data["tls.crt"]; hasCert {
		if _, hasKey := sourceSecret.Data["tls.key"]; hasKey {
			secretType = corev1.SecretTypeTLS
		}
	}

	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetSecretName,
			Namespace: GitOpsNamespace,
		},
		Type: secretType,
		Data: make(map[string][]byte),
	}

	// Copy data
	for key, value := range sourceSecret.Data {
		targetSecret.Data[key] = make([]byte, len(value))
		copy(targetSecret.Data[key], value)
	}

	// Try to create the secret first
	err := r.Create(ctx, targetSecret)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			// Secret exists, update it instead
			existingSecret := &corev1.Secret{}
			err = r.Get(ctx, types.NamespacedName{Name: TargetSecretName, Namespace: GitOpsNamespace}, existingSecret)

			if err != nil {
				klog.Errorf("Failed to get existing target secret: %v", err)
				return reconcile.Result{}, err
			}

			// Update the existing secret's data and type
			existingSecret.Data = targetSecret.Data
			existingSecret.Type = secretType

			err = r.Update(ctx, existingSecret)
			if err != nil {
				klog.Errorf("Failed to update target secret: %v", err)
				return reconcile.Result{}, err
			}

			klog.Infof("Successfully updated secret %s in namespace %s", TargetSecretName, GitOpsNamespace)
		} else {
			klog.Errorf("Failed to create target secret: %v", err)
			return reconcile.Result{}, err
		}
	} else {
		klog.Infof("Successfully created secret %s in namespace %s", TargetSecretName, GitOpsNamespace)
	}

	return reconcile.Result{}, nil
}
