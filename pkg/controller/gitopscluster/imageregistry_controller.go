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
	"fmt"
	"strings"
	"time"

	imageregistryv1alpha1 "github.com/stolostron/cluster-lifecycle-api/imageregistry/v1alpha1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"

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
	// imageRegistryFinalizer keeps a ManagedClusterImageRegistry around long enough for the
	// controller to revert every AddOnDeploymentConfig it mirrored images into, so the
	// gitopsaddon agent falls back to the source (non-mirrored) images again.
	imageRegistryFinalizer = "imageregistry.open-cluster-management.io/gitops-addon-cleanup"

	// adcManagedByAnnotation is stamped on a managed cluster's "gitops-addon-config"
	// AddOnDeploymentConfig to record which ManagedClusterImageRegistry (namespace/name) is
	// currently driving its image mirroring. Prevents two different ManagedClusterImageRegistry
	// objects from clobbering each other's bookkeeping if they ever target the same cluster.
	adcManagedByAnnotation = "imageregistry.open-cluster-management.io/managed-by"

	// adcOriginalValuesAnnotation stores a JSON-encoded map of customizedVariable name -> its
	// value BEFORE mirroring was applied (i.e. the source-registry value). This makes mirroring
	// reversible: deleting the ManagedClusterImageRegistry restores exactly these values.
	adcOriginalValuesAnnotation = "imageregistry.open-cluster-management.io/original-values"

	// adcLastMirroredValuesAnnotation stores a JSON-encoded map of customizedVariable name -> the
	// mirrored value this controller last wrote for it. Comparing this against the variable's
	// live value is what makes mirroring idempotent (re-applying doesn't double-replace the
	// registry prefix) while still distinguishing genuine external drift: if the live value no
	// longer matches what we last wrote, something else replaced it with a fresh source value
	// (most notably CreateAddOnDeploymentConfig writing a new hub default after an image
	// upgrade), which must be picked up as the new original rather than blindly re-mirroring
	// the stale recorded original. CreateAddOnDeploymentConfig itself preserves already-mirrored
	// values when the source hasn't changed, so the two controllers no longer fight on the
	// steady-state path.
	adcLastMirroredValuesAnnotation = "imageregistry.open-cluster-management.io/last-mirrored-values"

	// gitopsAddonImageMirroringAnnotation is the opt-in annotation that must be set to "true"
	// on a ManagedClusterImageRegistry before this controller will watch or reconcile it.
	// ManagedClusterImageRegistry is a general ACM API (cluster-image-registry-controller uses
	// it to mirror klusterlet images); without this gate every existing CR would have its
	// selected clusters' gitops-addon AddOnDeploymentConfigs rewritten, which is a behavior
	// change users did not ask for. Value must be the literal "true", matching the other
	// apps.open-cluster-management.io annotations in this package.
	gitopsAddonImageMirroringAnnotation = "apps.open-cluster-management.io/gitops-addon-image-mirroring"

	gitopsAddonName               = "gitops-addon"
	addonDeploymentConfigGroup    = "addon.open-cluster-management.io"
	addonDeploymentConfigResource = "addondeploymentconfigs"

	placementDecisionClusterLabel = "cluster.open-cluster-management.io/placement"
)

// ReconcileImageRegistry reconciles a ManagedClusterImageRegistry object that has opted
// into gitops-addon image mirroring via the gitopsAddonImageMirroringAnnotation.
//
// For every managed cluster selected by the CR's placementRef, it finds the gitops-addon
// ManagedClusterAddOn in that cluster's namespace, resolves the AddOnDeploymentConfig it
// references, and rewrites any image name/value pair whose registry prefix matches one of the
// CR's configured source registries so it points at the mirror instead -- so the gitopsaddon
// agent on the spoke installs the GitOps operator/ArgoCD images from the mirror registry. When
// the ManagedClusterImageRegistry is deleted (or the opt-in annotation is removed), the same
// AddOnDeploymentConfigs are reverted back to their original (source) image values. Unannotated
// CRs are ignored so existing ManagedClusterImageRegistry usage (klusterlet image mirroring)
// is unchanged.
type ReconcileImageRegistry struct {
	client.Client
	scheme *runtime.Scheme
}

var _ reconcile.Reconciler = &ReconcileImageRegistry{}

// AddImageRegistryController creates a new ManagedClusterImageRegistry controller and adds it to
// the Manager. The Manager will set fields on the Controller and start it when the Manager
// starts.
func AddImageRegistryController(mgr manager.Manager) error {
	r := &ReconcileImageRegistry{
		Client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
	}

	return addImageRegistryController(mgr, r)
}

// addImageRegistryController adds a new Controller to mgr with r as the reconcile.Reconciler.
func addImageRegistryController(mgr manager.Manager, r reconcile.Reconciler) error {
	skipValidation := true
	c, err := controller.New("imageregistry-controller", mgr, controller.Options{
		Reconciler:         r,
		SkipNameValidation: &skipValidation,
	})

	if err != nil {
		return err
	}

	if !utils.IsReadyACMClusterRegistry(mgr.GetAPIReader()) {
		// Placement/PlacementDecision/ManagedClusterAddOn all live under the same ACM cluster
		// registry as ManagedCluster. If it isn't ready yet, utils.DetectClusterRegistry (called
		// from the binary's main setup) will exit the process once it becomes ready, and the
		// manager restart re-runs this setup with working watches.
		klog.Warning("ACM cluster registry not ready, skipping ImageRegistry controller watch setup")
		return nil
	}

	if !utils.IsReadyManagedClusterImageRegistry(mgr.GetAPIReader()) {
		klog.Warning("ManagedClusterImageRegistry API not ready, skipping ImageRegistry controller watch setup")
		return nil
	}

	// Watch ManagedClusterImageRegistry changes, but only for CRs that have opted into
	// gitops-addon image mirroring (or that already carry our finalizer, so removing the
	// annotation still triggers cleanup). Unannotated CRs are left entirely to ACM's own
	// cluster-image-registry-controller.
	if err := c.Watch(
		source.Kind(
			mgr.GetCache(),
			&imageregistryv1alpha1.ManagedClusterImageRegistry{},
			&handler.TypedEnqueueRequestForObject[*imageregistryv1alpha1.ManagedClusterImageRegistry]{},
			gitopsAddonImageRegistryPredicate,
		),
	); err != nil {
		return err
	}

	// Watch PlacementDecision changes so a cluster joining/leaving a referenced Placement
	// re-applies (or stops applying) image mirroring for it.
	pdMapper := &imageRegistryPlacementDecisionMapper{Client: mgr.GetClient()}
	if err := c.Watch(
		source.Kind(
			mgr.GetCache(),
			&clusterv1beta1.PlacementDecision{},
			handler.TypedEnqueueRequestsFromMapFunc[*clusterv1beta1.PlacementDecision](pdMapper.Map),
		),
	); err != nil {
		return err
	}

	// Watch AddOnDeploymentConfig changes so mirroring is re-applied if GitOpsCluster writes a
	// new source-registry value (image upgrade / principal drift heal). Steady-state reconciles
	// no longer fight: CreateAddOnDeploymentConfig preserves already-mirrored values when the
	// recorded original still matches the desired source.
	adcMapper := &addOnDeploymentConfigMapper{Client: mgr.GetClient()}
	if err := c.Watch(
		source.Kind(
			mgr.GetCache(),
			&addonv1alpha1.AddOnDeploymentConfig{},
			handler.TypedEnqueueRequestsFromMapFunc[*addonv1alpha1.AddOnDeploymentConfig](adcMapper.Map),
		),
	); err != nil {
		return err
	}

	return nil
}

// gitopsAddonImageRegistryPredicate drops ManagedClusterImageRegistry events for CRs that have
// not opted into gitops-addon image mirroring. Update still fires when the annotation is
// removed from a previously opted-in CR (or when our finalizer is present) so we can revert
// mirroring and drop the finalizer.
var gitopsAddonImageRegistryPredicate = predicate.TypedFuncs[*imageregistryv1alpha1.ManagedClusterImageRegistry]{
	CreateFunc: func(e event.TypedCreateEvent[*imageregistryv1alpha1.ManagedClusterImageRegistry]) bool {
		return isGitOpsAddonImageRegistryTarget(e.Object)
	},
	UpdateFunc: func(e event.TypedUpdateEvent[*imageregistryv1alpha1.ManagedClusterImageRegistry]) bool {
		return isGitOpsAddonImageRegistryTarget(e.ObjectOld) || isGitOpsAddonImageRegistryTarget(e.ObjectNew)
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*imageregistryv1alpha1.ManagedClusterImageRegistry]) bool {
		return isGitOpsAddonImageRegistryTarget(e.Object)
	},
}

// wantsGitOpsAddonImageMirroring reports whether the user has opted this
// ManagedClusterImageRegistry into gitops-addon image mirroring.
func wantsGitOpsAddonImageMirroring(mcir *imageregistryv1alpha1.ManagedClusterImageRegistry) bool {
	if mcir == nil {
		return false
	}

	return mcir.GetAnnotations()[gitopsAddonImageMirroringAnnotation] == "true"
}

// isGitOpsAddonImageRegistryTarget reports whether this controller should enqueue the CR:
// either the user has opted in, or we previously opted in and still have our finalizer to
// clean up.
func isGitOpsAddonImageRegistryTarget(mcir *imageregistryv1alpha1.ManagedClusterImageRegistry) bool {
	if mcir == nil {
		return false
	}

	return wantsGitOpsAddonImageMirroring(mcir) || containsString(mcir.Finalizers, imageRegistryFinalizer)
}

// imageRegistryPlacementDecisionMapper maps a changed PlacementDecision to every
// ManagedClusterImageRegistry in the same namespace that references the placement it belongs to.
type imageRegistryPlacementDecisionMapper struct {
	client.Client
}

func (m *imageRegistryPlacementDecisionMapper) Map(ctx context.Context, obj *clusterv1beta1.PlacementDecision) []reconcile.Request {
	mcirList := &imageregistryv1alpha1.ManagedClusterImageRegistryList{}
	if err := m.List(ctx, mcirList, &client.ListOptions{Namespace: obj.GetNamespace()}); err != nil {
		klog.Errorf("failed to list ManagedClusterImageRegistry in namespace %s: %v", obj.GetNamespace(), err)
		return nil
	}

	placementName := obj.GetLabels()[placementDecisionClusterLabel]

	var requests []reconcile.Request

	for i := range mcirList.Items {
		mcir := &mcirList.Items[i]
		if !wantsGitOpsAddonImageMirroring(mcir) {
			continue
		}

		if strings.EqualFold(mcir.Spec.PlacementRef.Name, placementName) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: mcir.Namespace, Name: mcir.Name},
			})
		}
	}

	return requests
}

// addOnDeploymentConfigMapper maps a changed AddOnDeploymentConfig back to the
// ManagedClusterImageRegistry that owns its image mirroring (fast path: the
// adcManagedByAnnotation stamped on it), falling back to a full scan for the case where the
// AddOnDeploymentConfig is new or drifted and doesn't carry that annotation (yet).
type addOnDeploymentConfigMapper struct {
	client.Client
}

func (m *addOnDeploymentConfigMapper) Map(ctx context.Context, obj *addonv1alpha1.AddOnDeploymentConfig) []reconcile.Request {
	if owner, ok := obj.GetAnnotations()[adcManagedByAnnotation]; ok {
		if namespace, name, ok2 := splitOwnerKey(owner); ok2 {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}}
		}
	}

	mcirList := &imageregistryv1alpha1.ManagedClusterImageRegistryList{}
	if err := m.List(ctx, mcirList); err != nil {
		klog.Errorf("failed to list ManagedClusterImageRegistry for AddOnDeploymentConfig mapper: %v", err)
		return nil
	}

	var requests []reconcile.Request

	for i := range mcirList.Items {
		mcir := &mcirList.Items[i]
		if !wantsGitOpsAddonImageMirroring(mcir) {
			continue
		}

		clusterNames, err := resolvePlacementClusterNames(ctx, m.Client, mcir)
		if err != nil {
			continue
		}

		for _, name := range clusterNames {
			if name == obj.GetNamespace() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: mcir.Namespace, Name: mcir.Name},
				})

				break
			}
		}
	}

	return requests
}

// Reconcile reads the state of a ManagedClusterImageRegistry and applies (or, on deletion,
// reverts) image mirroring to the gitops-addon AddOnDeploymentConfig of every managed cluster it
// selects via its placementRef.
func (r *ReconcileImageRegistry) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("Reconciling ManagedClusterImageRegistry %s", request.NamespacedName)

	instance := &imageregistryv1alpha1.ManagedClusterImageRegistry{}
	if err := r.Get(ctx, request.NamespacedName, instance); err != nil {
		if k8errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, err
	}

	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance)
	}

	// Opt-out / never-opted-in: do not add a finalizer or touch any AddOnDeploymentConfig.
	// If the annotation was removed from a CR we previously managed, revert mirroring and
	// drop the finalizer so the object goes back to being ignored.
	if !wantsGitOpsAddonImageMirroring(instance) {
		if containsString(instance.Finalizers, imageRegistryFinalizer) {
			klog.Infof("gitops-addon-image-mirroring annotation removed from ManagedClusterImageRegistry %s, reverting image mirroring", request.NamespacedName)
			return r.reconcileDelete(ctx, instance)
		}

		klog.V(2).Infof("ManagedClusterImageRegistry %s has no %s=true annotation, skipping",
			request.NamespacedName, gitopsAddonImageMirroringAnnotation)

		return reconcile.Result{}, nil
	}

	if !containsString(instance.Finalizers, imageRegistryFinalizer) {
		instance.Finalizers = append(instance.Finalizers, imageRegistryFinalizer)
		if err := r.Update(ctx, instance); err != nil {
			return reconcile.Result{}, err
		}
		// The Update above generates a fresh watch event; the actual work happens next pass.
		return reconcile.Result{}, nil
	}

	clusterNames, err := resolvePlacementClusterNames(ctx, r.Client, instance)
	if err != nil {
		klog.Errorf("Failed to resolve placement for ManagedClusterImageRegistry %s/%s: %v", instance.Namespace, instance.Name, err)
		return reconcile.Result{}, err
	}

	var applyErrs []error

	for _, clusterName := range clusterNames {
		if err := r.applyImageMirroring(ctx, instance, clusterName); err != nil {
			klog.Errorf("Failed to apply image mirroring for cluster %s from ManagedClusterImageRegistry %s/%s: %v",
				clusterName, instance.Namespace, instance.Name, err)
			applyErrs = append(applyErrs, fmt.Errorf("cluster %s: %w", clusterName, err))
		}
	}

	if len(applyErrs) > 0 {
		return reconcile.Result{RequeueAfter: time.Minute}, utilerrors.NewAggregate(applyErrs)
	}

	// Periodic safety-net resync: self-heals against out-of-band drift, e.g. GitOpsCluster
	// writing a new source-registry value after a hub image upgrade (the only remaining
	// CreateAddOnDeploymentConfig overwrite of a mirrored variable — it preserves the live
	// mirrored value when the recorded original still matches the desired source).
	return reconcile.Result{RequeueAfter: 5 * time.Minute}, nil
}

// reconcileDelete reverts image mirroring on every AddOnDeploymentConfig this
// ManagedClusterImageRegistry touched, then removes the finalizer so the object can finish
// deleting.
func (r *ReconcileImageRegistry) reconcileDelete(ctx context.Context, instance *imageregistryv1alpha1.ManagedClusterImageRegistry) (reconcile.Result, error) {
	if !containsString(instance.Finalizers, imageRegistryFinalizer) {
		return reconcile.Result{}, nil
	}

	if err := r.revertImageMirroring(ctx, instance); err != nil {
		klog.Errorf("Failed to revert image mirroring for ManagedClusterImageRegistry %s/%s: %v", instance.Namespace, instance.Name, err)
		return reconcile.Result{}, err
	}

	instance.Finalizers = removeString(instance.Finalizers, imageRegistryFinalizer)
	if err := r.Update(ctx, instance); err != nil {
		return reconcile.Result{}, err
	}

	klog.Infof("Removed finalizer from ManagedClusterImageRegistry %s/%s after reverting image mirroring", instance.Namespace, instance.Name)

	return reconcile.Result{}, nil
}

// resolvePlacementClusterNames resolves a ManagedClusterImageRegistry's placementRef to the list
// of managed cluster names it currently selects, via the Placement's PlacementDecisions. The
// Placement is assumed to live in the same namespace as the ManagedClusterImageRegistry, matching
// the convention used elsewhere in this repo (see GetManagedClusters in server_discovery.go) and
// upstream's cluster-image-registry-controller.
func resolvePlacementClusterNames(ctx context.Context, c client.Client, mcir *imageregistryv1alpha1.ManagedClusterImageRegistry) ([]string, error) {
	placementRef := mcir.Spec.PlacementRef

	if !strings.EqualFold(placementRef.Group, "cluster.open-cluster-management.io") ||
		!(strings.EqualFold(placementRef.Resource, "placement") || strings.EqualFold(placementRef.Resource, "placements")) {
		return nil, fmt.Errorf("invalid placementRef group/resource: %s/%s", placementRef.Group, placementRef.Resource)
	}

	namespace := mcir.Namespace

	placement := &clusterv1beta1.Placement{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: placementRef.Name}, placement); err != nil {
		return nil, err
	}

	placementDecisions := &clusterv1beta1.PlacementDecisionList{}

	selector, err := utils.ConvertLabels(&metav1.LabelSelector{
		MatchLabels: map[string]string{placementDecisionClusterLabel: placementRef.Name},
	})
	if err != nil {
		return nil, err
	}

	if err := c.List(ctx, placementDecisions, &client.ListOptions{Namespace: namespace, LabelSelector: selector}); err != nil {
		return nil, err
	}

	seen := map[string]bool{}

	var clusterNames []string

	for _, pd := range placementDecisions.Items {
		for _, decision := range pd.Status.Decisions {
			if !seen[decision.ClusterName] {
				seen[decision.ClusterName] = true
				clusterNames = append(clusterNames, decision.ClusterName)
			}
		}
	}

	return clusterNames, nil
}

// applyImageMirroring rewrites the image name/value pairs on the given managed cluster's
// gitops-addon AddOnDeploymentConfig so any value whose registry prefix matches one of mcir's
// configured source registries is replaced by the corresponding mirror. Values with no matching
// source prefix are left untouched. Idempotent: re-running it after it already applied mirroring
// (or after mcir's registries list changed) converges to the correct state without ever
// double-mirroring a value, using the adcOriginalValuesAnnotation as the source of truth for each
// variable's pre-mirror value.
func (r *ReconcileImageRegistry) applyImageMirroring(ctx context.Context, mcir *imageregistryv1alpha1.ManagedClusterImageRegistry, clusterName string) error {
	ownerKey := ownerKeyFor(mcir)

	adc, err := r.findGitOpsAddonDeploymentConfig(ctx, clusterName)
	if err != nil {
		if k8errors.IsNotFound(err) {
			klog.V(2).Infof("gitops-addon AddOnDeploymentConfig not found yet for cluster %s, skipping image mirroring for %s", clusterName, ownerKey)
			return nil
		}

		return err
	}

	if existingOwner, ok := adc.Annotations[adcManagedByAnnotation]; ok && existingOwner != ownerKey {
		return fmt.Errorf("addOnDeploymentConfig %s/%s is already managed by ManagedClusterImageRegistry %s, refusing to take over from %s",
			adc.Namespace, adc.Name, existingOwner, ownerKey)
	}

	origMap := parseOriginalValues(adc.Annotations[adcOriginalValuesAnnotation])
	lastMirroredMap := parseOriginalValues(adc.Annotations[adcLastMirroredValuesAnnotation])

	updatedVars, newOrigMap, newLastMirroredMap, changed := computeUpdatedVariables(
		adc.Spec.CustomizedVariables, origMap, lastMirroredMap, mcir.Spec.Registries, mcir.Spec.Registry)

	newOrigAnnotation, err := marshalOriginalValues(newOrigMap)
	if err != nil {
		return err
	}

	newLastMirroredAnnotation, err := marshalOriginalValues(newLastMirroredMap)
	if err != nil {
		return err
	}

	annotationsChanged := adc.Annotations[adcManagedByAnnotation] != ownerKey && len(newOrigMap) > 0
	annotationsChanged = annotationsChanged || adc.Annotations[adcOriginalValuesAnnotation] != newOrigAnnotation
	annotationsChanged = annotationsChanged || adc.Annotations[adcLastMirroredValuesAnnotation] != newLastMirroredAnnotation

	if !changed && !annotationsChanged {
		klog.Infof("AddOnDeploymentConfig %s/%s image values already up to date for %s", adc.Namespace, adc.Name, ownerKey)
		return nil
	}

	if adc.Annotations == nil {
		adc.Annotations = map[string]string{}
	}

	if len(newOrigMap) == 0 {
		delete(adc.Annotations, adcManagedByAnnotation)
		delete(adc.Annotations, adcOriginalValuesAnnotation)
		delete(adc.Annotations, adcLastMirroredValuesAnnotation)
	} else {
		adc.Annotations[adcManagedByAnnotation] = ownerKey
		adc.Annotations[adcOriginalValuesAnnotation] = newOrigAnnotation
		adc.Annotations[adcLastMirroredValuesAnnotation] = newLastMirroredAnnotation
	}

	adc.Spec.CustomizedVariables = updatedVars

	if err := r.Update(ctx, adc); err != nil {
		return err
	}

	klog.Infof("Updated AddOnDeploymentConfig %s/%s image values for ManagedClusterImageRegistry %s", adc.Namespace, adc.Name, ownerKey)

	return nil
}

// computeUpdatedVariables walks current customizedVariables and decides, for each one, whether it
// needs to be (re-)mirrored, left alone, or reverted. It uses two pieces of recorded state per
// tracked variable:
//   - origMap: the source-registry value seen the first time this variable was mirrored.
//   - lastMirroredMap: the mirrored value this controller last wrote for it.
//
// Comparing the variable's live value against lastMirroredMap is what distinguishes "nothing
// changed since our last write" (live value still matches what we wrote -- possibly just needs
// recomputing if the registries config itself changed) from genuine external drift (something
// else, e.g. CreateAddOnDeploymentConfig writing a new hub default after an image upgrade,
// overwrote it with a different value that must become the new tracked original).
// CreateAddOnDeploymentConfig preserves already-mirrored values when the recorded original
// still matches the desired source, so this path is only taken when the source actually
// changed.
//
// Returns the updated variable list, the updated origMap and lastMirroredMap (pruned/extended to
// match reality), and whether anything in the variable list actually changed.
func computeUpdatedVariables(
	current []addonv1alpha1.CustomizedVariable,
	origMap map[string]string,
	lastMirroredMap map[string]string,
	registries []imageregistryv1alpha1.Registries,
	registry string,
) ([]addonv1alpha1.CustomizedVariable, map[string]string, map[string]string, bool) {
	changed := false
	newOrigMap := map[string]string{}
	newLastMirroredMap := map[string]string{}
	updated := make([]addonv1alpha1.CustomizedVariable, 0, len(current))

	for _, v := range current {
		orig, tracked := origMap[v.Name]
		lastMirrored, hadLastMirrored := lastMirroredMap[v.Name]

		if tracked && hadLastMirrored && v.Value == lastMirrored {
			// Our own last write is still intact. Recompute from the recorded original in case
			// the registries configuration itself changed since then (e.g. a different mirror).
			if mirrored, ok := computeMirroredValue(orig, registries, registry); ok {
				newOrigMap[v.Name] = orig
				newLastMirroredMap[v.Name] = mirrored

				if v.Value != mirrored {
					changed = true
				}

				updated = append(updated, addonv1alpha1.CustomizedVariable{Name: v.Name, Value: mirrored})

				continue
			}

			// The registries configuration no longer produces a mirror for this variable's
			// source value (e.g. the matching entry was removed) -- revert it to the recorded
			// original value and stop tracking it.
			if v.Value != orig {
				changed = true
			}

			updated = append(updated, addonv1alpha1.CustomizedVariable{Name: v.Name, Value: orig})

			continue
		}

		// Either untracked, or the live value no longer matches what we last wrote -- treat the
		// live value itself as the (possibly new) source-of-truth and try to mirror it.
		if mirrored, ok := computeMirroredValue(v.Value, registries, registry); ok {
			newOrigMap[v.Name] = v.Value
			newLastMirroredMap[v.Name] = mirrored
			changed = true

			updated = append(updated, addonv1alpha1.CustomizedVariable{Name: v.Name, Value: mirrored})

			continue
		}

		// Not mirrorable (no matching source registry, or not an image reference at all) --
		// leave it untouched and stop tracking it.
		updated = append(updated, v)
	}

	return updated, newOrigMap, newLastMirroredMap, changed
}

// computeMirroredValue replaces value's registry-host prefix with the configured mirror, if a
// matching source registry is found. It returns the original value and false if value doesn't
// look like an image reference (no "/"), or no configured registry matches its host.
//
// Matches the ManagedClusterImageRegistry API semantics: registries is tried first (in order,
// last match wins so that a later, more specific entry can override an earlier one with the same
// source), and only if it's empty does the single registry field act as a catch-all mirror for
// every image host.
func computeMirroredValue(value string, registries []imageregistryv1alpha1.Registries, registry string) (string, bool) {
	idx := strings.Index(value, "/")
	if idx <= 0 {
		return value, false
	}

	host := value[:idx]
	rest := value[idx:]

	effectiveRegistries := registries
	if len(effectiveRegistries) == 0 {
		if registry == "" {
			return value, false
		}

		effectiveRegistries = []imageregistryv1alpha1.Registries{{Mirror: registry}}
	}

	newHost := ""
	matched := false

	for _, reg := range effectiveRegistries {
		if reg.Mirror == "" {
			continue
		}

		if reg.Source == "" || reg.Source == host {
			newHost = reg.Mirror
			matched = true
		}
	}

	if !matched || newHost == host {
		return value, false
	}

	return newHost + rest, true
}

// findGitOpsAddonDeploymentConfig looks up the gitops-addon ManagedClusterAddOn in the given
// managed cluster's namespace, then resolves the AddOnDeploymentConfig it references from
// spec.configs.
func (r *ReconcileImageRegistry) findGitOpsAddonDeploymentConfig(ctx context.Context, clusterName string) (*addonv1alpha1.AddOnDeploymentConfig, error) {
	mca := &addonv1alpha1.ManagedClusterAddOn{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: clusterName, Name: gitopsAddonName}, mca); err != nil {
		return nil, err
	}

	for _, cfg := range mca.Spec.Configs {
		if cfg.Group != addonDeploymentConfigGroup || cfg.Resource != addonDeploymentConfigResource {
			continue
		}

		namespace := cfg.Namespace
		if namespace == "" {
			namespace = clusterName
		}

		adc := &addonv1alpha1.AddOnDeploymentConfig{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cfg.Name}, adc); err != nil {
			return nil, err
		}

		return adc, nil
	}

	return nil, k8errors.NewNotFound(addonv1alpha1.Resource(addonDeploymentConfigResource),
		fmt.Sprintf("no %s config referenced by ManagedClusterAddOn %s/%s", addonDeploymentConfigResource, clusterName, gitopsAddonName))
}

// revertImageMirroring restores the original (source) image values on every AddOnDeploymentConfig
// this ManagedClusterImageRegistry mirrored images into.
//
// Deliberately does NOT cache the resolved cluster list anywhere (e.g. an annotation on the
// ManagedClusterImageRegistry itself) to survive the Placement/PlacementDecisions being gone by
// delete time: at hub scale (ACM supports up to ~3k managed clusters) a comma-joined cluster-name
// list can approach or exceed the apiserver's hard 256KiB total-annotations-size limit
// (k8s.io/apimachinery/pkg/api/validation.TotalAnnotationSizeLimitB), which would make every
// reconcile of a large-fleet ManagedClusterImageRegistry fail outright. Instead, the placement
// resolution below is only ever a fast-path optimization when it still succeeds; the unconditional
// cluster-wide AddOnDeploymentConfig scan further down is the sole, authoritative correctness
// backstop, and by itself is already sufficient to find and revert every AddOnDeploymentConfig
// this instance ever mirrored into, with no cluster list of any kind needed up front.
func (r *ReconcileImageRegistry) revertImageMirroring(ctx context.Context, mcir *imageregistryv1alpha1.ManagedClusterImageRegistry) error {
	ownerKey := ownerKeyFor(mcir)

	clusterNames, err := resolvePlacementClusterNames(ctx, r.Client, mcir)
	if err != nil {
		klog.Warningf("failed to resolve placement for ManagedClusterImageRegistry %s during cleanup, relying on the cluster-wide AddOnDeploymentConfig scan below instead: %v", ownerKey, err)
	}

	var errs []error

	handled := map[string]bool{}

	for _, clusterName := range clusterNames {
		handled[clusterName] = true

		if err := r.revertClusterImageMirroring(ctx, ownerKey, clusterName); err != nil {
			klog.Errorf("failed to revert image mirroring for cluster %s (%s): %v", clusterName, ownerKey, err)
			errs = append(errs, err)
		}
	}

	// Authoritative correctness backstop: scan every AddOnDeploymentConfig cluster-wide for any
	// that are still stamped as owned by this ManagedClusterImageRegistry but weren't covered
	// above (e.g. a cluster left the placement in between the last successful apply and
	// deletion, or the placement/placementDecisions above couldn't be resolved at all).
	adcList := &addonv1alpha1.AddOnDeploymentConfigList{}
	if err := r.List(ctx, adcList); err != nil {
		errs = append(errs, err)
	} else {
		for i := range adcList.Items {
			adc := &adcList.Items[i]
			if handled[adc.Namespace] {
				continue
			}

			if adc.Annotations[adcManagedByAnnotation] != ownerKey {
				continue
			}

			if err := r.revertAddOnDeploymentConfig(ctx, adc); err != nil {
				klog.Errorf("failed to revert AddOnDeploymentConfig %s/%s (%s): %v", adc.Namespace, adc.Name, ownerKey, err)
				errs = append(errs, err)
			}
		}
	}

	return utilerrors.NewAggregate(errs)
}

func (r *ReconcileImageRegistry) revertClusterImageMirroring(ctx context.Context, ownerKey, clusterName string) error {
	adc, err := r.findGitOpsAddonDeploymentConfig(ctx, clusterName)
	if err != nil {
		if k8errors.IsNotFound(err) {
			return nil
		}

		return err
	}

	if adc.Annotations[adcManagedByAnnotation] != ownerKey {
		// Not (or no longer) owned by us -- nothing to revert.
		return nil
	}

	return r.revertAddOnDeploymentConfig(ctx, adc)
}

func (r *ReconcileImageRegistry) revertAddOnDeploymentConfig(ctx context.Context, adc *addonv1alpha1.AddOnDeploymentConfig) error {
	origMap := parseOriginalValues(adc.Annotations[adcOriginalValuesAnnotation])

	changed := false
	updated := make([]addonv1alpha1.CustomizedVariable, 0, len(adc.Spec.CustomizedVariables))

	for _, v := range adc.Spec.CustomizedVariables {
		if orig, ok := origMap[v.Name]; ok {
			if v.Value != orig {
				changed = true
			}

			updated = append(updated, addonv1alpha1.CustomizedVariable{Name: v.Name, Value: orig})

			continue
		}

		updated = append(updated, v)
	}

	_, hadManagedBy := adc.Annotations[adcManagedByAnnotation]
	_, hadOriginal := adc.Annotations[adcOriginalValuesAnnotation]
	_, hadLastMirrored := adc.Annotations[adcLastMirroredValuesAnnotation]

	if !changed && !hadManagedBy && !hadOriginal && !hadLastMirrored {
		return nil
	}

	adc.Spec.CustomizedVariables = updated

	if adc.Annotations != nil {
		delete(adc.Annotations, adcManagedByAnnotation)
		delete(adc.Annotations, adcOriginalValuesAnnotation)
		delete(adc.Annotations, adcLastMirroredValuesAnnotation)
	}

	klog.Infof("Reverted AddOnDeploymentConfig %s/%s image values to their source registry values", adc.Namespace, adc.Name)

	return r.Update(ctx, adc)
}

func parseOriginalValues(raw string) map[string]string {
	m := map[string]string{}
	if raw == "" {
		return m
	}

	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		klog.Warningf("failed to parse %s annotation value %q: %v", adcOriginalValuesAnnotation, raw, err)
		return map[string]string{}
	}

	return m
}

func marshalOriginalValues(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "", nil
	}

	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

func ownerKeyFor(mcir *imageregistryv1alpha1.ManagedClusterImageRegistry) string {
	return mcir.Namespace + "/" + mcir.Name
}

func splitOwnerKey(key string) (namespace, name string, ok bool) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}

	return parts[0], parts[1], true
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}

	return false
}

func removeString(list []string, s string) []string {
	result := make([]string, 0, len(list))

	for _, item := range list {
		if item != s {
			result = append(result, item)
		}
	}

	return result
}
