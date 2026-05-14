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
	"fmt"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	TargetSecretName       = "argocd-agent-client-tls" // #nosec G101
	DefaultSourceNamespace = "open-cluster-management-agent-addon"

	// SecretResyncInterval is the periodic interval at which the reconciler re-checks
	// that the target secret is in sync with the source. This catches missed watch events
	// (e.g., from network blips or pod restarts) and ensures the target cert stays fresh.
	// The OCM ClientCertController rotates at ~80% of cert lifetime (e.g., ~19.2h for a 24h cert),
	// so a 5-minute resync ensures rapid propagation of rotated certs.
	SecretResyncInterval = 5 * time.Minute
)

// getTargetNamespace returns the namespace where the ArgoCD CR lives.
// For local-cluster (hub), this is the cluster's own namespace (e.g. "local-cluster").
// For remote clusters, this defaults to "openshift-gitops".
func getTargetNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("ARGOCD_NAMESPACE")); ns != "" {
		return ns
	}
	return GitOpsNamespace
}

// getSourceNamespace returns the source namespace for the agent client cert secret.
// The gitopsaddon agent always runs in open-cluster-management-agent-addon (the standard
// ACM addon namespace, set via ManagedClusterAddOn.spec.installNamespace). The POD_NAMESPACE
// env var is injected by OCM and reflects the actual running namespace. It can be overridden
// via GITOPS_ADDON_NAMESPACE for testing or special deployments.
func getSourceNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("GITOPS_ADDON_NAMESPACE")); ns != "" {
		return ns
	}
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	return DefaultSourceNamespace
}

// getSourceSecretName returns the source secret name, allowing override via environment variable
func getSourceSecretName() string {
	if name := strings.TrimSpace(os.Getenv("GITOPS_ADDON_ARGOCD_AGENT_SECRET_NAME")); name != "" {
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

	// Watch for target secret deletion so we can re-copy from source
	err = c.Watch(
		source.Kind(
			mgr.GetCache(),
			&corev1.Secret{},
			handler.TypedEnqueueRequestsFromMapFunc[*corev1.Secret](targetSecretToSourceMapper),
			targetSecretPredicateFunc,
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

// targetSecretPredicateFunc filters events for the target secret (argocd-agent-client-tls)
// so we can re-copy it from the source if it gets deleted
var targetSecretPredicateFunc = predicate.TypedFuncs[*corev1.Secret]{
	CreateFunc: func(e event.TypedCreateEvent[*corev1.Secret]) bool {
		return false // We don't need to handle create events for the target
	},
	UpdateFunc: func(e event.TypedUpdateEvent[*corev1.Secret]) bool {
		return false // We don't need to handle update events for the target
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*corev1.Secret]) bool {
		return isTargetSecretInTargetNamespace(e.Object)
	},
}

// isTargetSecretInTargetNamespace checks if the deleted secret is our target secret
func isTargetSecretInTargetNamespace(secret *corev1.Secret) bool {
	return secret.Name == TargetSecretName && secret.Namespace == getTargetNamespace()
}

// targetSecretToSourceMapper maps target secret deletion events back to the source secret
// so the reconciler re-copies the cert from source to target
func targetSecretToSourceMapper(ctx context.Context, obj *corev1.Secret) []reconcile.Request {
	if isTargetSecretInTargetNamespace(obj) {
		return []reconcile.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      getSourceSecretName(),
					Namespace: getSourceNamespace(),
				},
			},
		}
	}
	return []reconcile.Request{}
}

// targetNamespacePredicateFunc filters events for the target namespace
var targetNamespacePredicateFunc = predicate.TypedFuncs[*corev1.Namespace]{
	CreateFunc: func(e event.TypedCreateEvent[*corev1.Namespace]) bool {
		return e.Object.Name == getTargetNamespace()
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*corev1.Namespace]) bool {
		return e.Object.Name == getTargetNamespace()
	},
}

// isTargetSecret checks if the secret is the one we want to watch
func isTargetSecret(secret *corev1.Secret) bool {
	return secret.Name == getSourceSecretName() && secret.Namespace == getSourceNamespace()
}

// namespaceToSecretMapper maps namespace events to secret reconcile requests
func namespaceToSecretMapper(ctx context.Context, obj *corev1.Namespace) []reconcile.Request {
	if obj.Name == getTargetNamespace() {
		// When the target namespace is created or deleted, trigger reconciliation of the source secret
		return []reconcile.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      getSourceSecretName(),
					Namespace: getSourceNamespace(),
				},
			},
		}
	}

	return []reconcile.Request{}
}

// requeueResult returns a reconcile result that schedules a periodic re-check.
// This ensures the target stays in sync even if watch events are missed.
func requeueResult() reconcile.Result {
	return reconcile.Result{RequeueAfter: SecretResyncInterval}
}

// Reconcile handles secret events
func (r *SecretReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("Reconciling ArgoCD agent client cert secret: %s", request.NamespacedName)

	// Only process the specific secret we care about
	if request.Name != getSourceSecretName() || request.Namespace != getSourceNamespace() {
		klog.V(4).Infof("Ignoring secret %s, not the target secret", request.NamespacedName)
		return reconcile.Result{}, nil
	}

	targetNamespace := getTargetNamespace()

	// Check if target namespace exists
	targetNs := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: targetNamespace}, targetNs)

	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Target namespace %s does not exist, skipping secret copy", targetNamespace)
			return requeueResult(), nil
		}

		klog.Errorf("Failed to check target namespace %s: %v", targetNamespace, err)

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

	// Check if target already exists and is up-to-date
	targetSecretKey := types.NamespacedName{
		Name:      TargetSecretName,
		Namespace: targetNamespace,
	}

	existingTarget := &corev1.Secret{}
	err = r.Get(ctx, targetSecretKey, existingTarget)

	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("Failed to get target secret %s: %v", targetSecretKey, err)
		return reconcile.Result{}, err
	}

	if err == nil && secretDataEqual(sourceSecret.Data, existingTarget.Data) {
		klog.V(4).Infof("Target secret %s is already up-to-date, requeueing periodic check", targetSecretKey)
		return requeueResult(), nil
	}

	klog.Infof("Creating/updating target secret %s (cert data changed)", targetSecretKey)

	result, err := r.createOrUpdateSecretCopy(ctx, sourceSecret)
	if err != nil {
		return result, err
	}

	// Cert data changed — restart agent pods so they pick up the new certificate.
	// The argocd-agent process reads TLS certs at startup and does not hot-reload,
	// so a rolling restart is required for the renewed cert to take effect.
	if err := r.restartAgentDeployments(ctx, targetNamespace); err != nil {
		return reconcile.Result{}, err
	}

	return requeueResult(), nil
}

// secretDataEqual is defined in gitopsaddon_install.go (shared within the package)

// restartAgentDeployments performs a rolling restart of ArgoCD agent Deployments
// in the target namespace so they pick up the renewed TLS certificate.
// This is equivalent to `kubectl rollout restart` — it patches the pod template
// annotation to trigger a new rollout. Only Deployments labeled
// app.kubernetes.io/part-of=argocd-agent are restarted. If no agent deployments
// exist (e.g., non-agent mode), this is a no-op.
func (r *SecretReconciler) restartAgentDeployments(ctx context.Context, namespace string) error {
	agentSelector := labels.SelectorFromSet(labels.Set{
		"app.kubernetes.io/part-of": "argocd-agent",
	})

	deployList := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployList, &client.ListOptions{
		Namespace:     namespace,
		LabelSelector: agentSelector,
	}); err != nil {
		return fmt.Errorf("failed to list agent deployments in %s: %w", namespace, err)
	}

	if len(deployList.Items) == 0 {
		klog.V(4).Infof("No argocd-agent deployments found in %s, skipping restart", namespace)
		return nil
	}

	restartTimestamp := fmt.Sprintf("%d", time.Now().Unix())

	for i := range deployList.Items {
		deploy := &deployList.Items[i]
		klog.Infof("Restarting agent deployment %s/%s after cert rotation", deploy.Namespace, deploy.Name)

		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = make(map[string]string)
		}

		deploy.Spec.Template.Annotations["apps.open-cluster-management.io/cert-rotated-at"] = restartTimestamp

		if err := r.Update(ctx, deploy); err != nil {
			return fmt.Errorf("failed to restart agent deployment %s/%s: %w", deploy.Namespace, deploy.Name, err)
		}

		klog.Infof("Successfully triggered rolling restart of %s/%s", deploy.Namespace, deploy.Name)
	}

	return nil
}

// handleSourceSecretDeletion handles the case when the source secret is deleted
func (r *SecretReconciler) handleSourceSecretDeletion(ctx context.Context) (reconcile.Result, error) {
	targetNamespace := getTargetNamespace()
	// Delete the target secret if it exists
	targetSecret := &corev1.Secret{}
	targetSecretKey := types.NamespacedName{
		Name:      TargetSecretName,
		Namespace: targetNamespace,
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
	targetNamespace := getTargetNamespace()

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
			Namespace: targetNamespace,
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
			err = r.Get(ctx, types.NamespacedName{Name: TargetSecretName, Namespace: targetNamespace}, existingSecret)

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

			klog.Infof("Successfully updated secret %s in namespace %s", TargetSecretName, targetNamespace)
		} else {
			klog.Errorf("Failed to create target secret: %v", err)
			return reconcile.Result{}, err
		}
	} else {
		klog.Infof("Successfully created secret %s in namespace %s", TargetSecretName, targetNamespace)
	}

	return reconcile.Result{}, nil
}
