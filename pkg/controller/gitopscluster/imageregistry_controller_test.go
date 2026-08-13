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
	"testing"

	imageregistryv1alpha1 "github.com/stolostron/cluster-lifecycle-api/imageregistry/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func imageRegistryTestScheme(t *testing.T) *runtime.Scheme {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, addonv1alpha1.AddToScheme(scheme))
	require.NoError(t, clusterv1beta1.AddToScheme(scheme))
	require.NoError(t, imageregistryv1alpha1.AddToScheme(scheme))

	return scheme
}

func TestComputeMirroredValue(t *testing.T) {
	registries := []imageregistryv1alpha1.Registries{
		{Source: "registry.redhat.io", Mirror: "999569342541.dkr.ecr.us-east-1.amazonaws.com"},
		{Source: "quay.io", Mirror: "999569342541.dkr.ecr.us-east-1.amazonaws.com/quay"},
	}

	tests := []struct {
		name          string
		value         string
		registries    []imageregistryv1alpha1.Registries
		registry      string
		expectedValue string
		expectedOK    bool
	}{
		{
			name:          "matching source is replaced",
			value:         "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd",
			registries:    registries,
			expectedValue: "999569342541.dkr.ecr.us-east-1.amazonaws.com/openshift-gitops-1/argocd-rhel9@sha256:abcd",
			expectedOK:    true,
		},
		{
			name:          "no matching source is left as-is",
			value:         "some.other.registry/foo/bar:latest",
			registries:    registries,
			expectedValue: "some.other.registry/foo/bar:latest",
			expectedOK:    false,
		},
		{
			name:          "value without a registry host component is left as-is",
			value:         "managed",
			registries:    registries,
			expectedValue: "managed",
			expectedOK:    false,
		},
		{
			name:          "empty value is left as-is",
			value:         "",
			registries:    registries,
			expectedValue: "",
			expectedOK:    false,
		},
		{
			name:          "empty registries list falls back to single Registry catch-all",
			value:         "registry.redhat.io/rhel9/redis-7@sha256:1234",
			registries:    nil,
			registry:      "registry.mist11-0.qe.red-chesterfield.com:5000",
			expectedValue: "registry.mist11-0.qe.red-chesterfield.com:5000/rhel9/redis-7@sha256:1234",
			expectedOK:    true,
		},
		{
			name:          "no registries and no registry means no change",
			value:         "registry.redhat.io/rhel9/redis-7@sha256:1234",
			registries:    nil,
			registry:      "",
			expectedValue: "registry.redhat.io/rhel9/redis-7@sha256:1234",
			expectedOK:    false,
		},
		{
			name:  "later entry wins when sources collide",
			value: "registry.redhat.io/foo/bar:latest",
			registries: []imageregistryv1alpha1.Registries{
				{Source: "registry.redhat.io", Mirror: "mirror-one.example.com"},
				{Source: "registry.redhat.io", Mirror: "mirror-two.example.com"},
			},
			expectedValue: "mirror-two.example.com/foo/bar:latest",
			expectedOK:    true,
		},
		{
			name:  "entry with empty source acts as catch-all within the list",
			value: "anything.example.com/foo/bar:latest",
			registries: []imageregistryv1alpha1.Registries{
				{Source: "", Mirror: "catch-all.example.com"},
			},
			expectedValue: "catch-all.example.com/foo/bar:latest",
			expectedOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := computeMirroredValue(tt.value, tt.registries, tt.registry)
			assert.Equal(t, tt.expectedOK, ok)
			assert.Equal(t, tt.expectedValue, got)
		})
	}
}

func TestComputeUpdatedVariables(t *testing.T) {
	registries := []imageregistryv1alpha1.Registries{
		{Source: "registry.redhat.io", Mirror: "mirror.example.com"},
	}

	t.Run("first-time mirroring records the original value", func(t *testing.T) {
		current := []addonv1alpha1.CustomizedVariable{
			{Name: "ARGOCD_IMAGE", Value: "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd"},
			{Name: "ARGOCD_AGENT_MODE", Value: "managed"},
		}

		updated, origMap, lastMirroredMap, changed := computeUpdatedVariables(current, map[string]string{}, map[string]string{}, registries, "")

		assert.True(t, changed)
		assert.Equal(t, "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(updated, "ARGOCD_IMAGE"))
		assert.Equal(t, "managed", findVar(updated, "ARGOCD_AGENT_MODE"))
		assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd", origMap["ARGOCD_IMAGE"])
		assert.Equal(t, "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:abcd", lastMirroredMap["ARGOCD_IMAGE"])
		_, tracked := origMap["ARGOCD_AGENT_MODE"]
		assert.False(t, tracked, "non-image variables should never be tracked as originals")
	})

	t.Run("re-applying with an already-mirrored value is idempotent", func(t *testing.T) {
		current := []addonv1alpha1.CustomizedVariable{
			{Name: "ARGOCD_IMAGE", Value: "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:abcd"},
		}
		origMap := map[string]string{"ARGOCD_IMAGE": "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd"}
		lastMirroredMap := map[string]string{"ARGOCD_IMAGE": "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:abcd"}

		updated, newOrigMap, newLastMirroredMap, changed := computeUpdatedVariables(current, origMap, lastMirroredMap, registries, "")

		assert.False(t, changed)
		assert.Equal(t, "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(updated, "ARGOCD_IMAGE"))
		assert.Equal(t, origMap, newOrigMap)
		assert.Equal(t, lastMirroredMap, newLastMirroredMap)
	})

	t.Run("source value drifting (e.g. hub upgraded the digest) is re-mirrored from the new source", func(t *testing.T) {
		current := []addonv1alpha1.CustomizedVariable{
			// gitopscluster controller reset this back to a NEW source value (new digest),
			// undoing the previous mirror -- the live value no longer matches lastMirroredMap.
			{Name: "ARGOCD_IMAGE", Value: "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:NEW"},
		}
		origMap := map[string]string{"ARGOCD_IMAGE": "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd"}
		lastMirroredMap := map[string]string{"ARGOCD_IMAGE": "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:abcd"}

		updated, newOrigMap, newLastMirroredMap, changed := computeUpdatedVariables(current, origMap, lastMirroredMap, registries, "")

		assert.True(t, changed)
		assert.Equal(t, "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:NEW", findVar(updated, "ARGOCD_IMAGE"))
		assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:NEW", newOrigMap["ARGOCD_IMAGE"])
		assert.Equal(t, "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:NEW", newLastMirroredMap["ARGOCD_IMAGE"])
	})

	t.Run("registries no longer matching reverts to the original and drops tracking", func(t *testing.T) {
		current := []addonv1alpha1.CustomizedVariable{
			// Live value still matches our own last write -- nothing external touched it.
			{Name: "ARGOCD_IMAGE", Value: "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:abcd"},
		}
		origMap := map[string]string{"ARGOCD_IMAGE": "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd"}
		lastMirroredMap := map[string]string{"ARGOCD_IMAGE": "mirror.example.com/openshift-gitops-1/argocd-rhel9@sha256:abcd"}

		// No registries configured anymore.
		updated, newOrigMap, newLastMirroredMap, changed := computeUpdatedVariables(current, origMap, lastMirroredMap, nil, "")

		assert.True(t, changed)
		assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(updated, "ARGOCD_IMAGE"))
		_, tracked := newOrigMap["ARGOCD_IMAGE"]
		assert.False(t, tracked)
		_, trackedMirrored := newLastMirroredMap["ARGOCD_IMAGE"]
		assert.False(t, trackedMirrored)
	})
}

func findVar(vars []addonv1alpha1.CustomizedVariable, name string) string {
	for _, v := range vars {
		if v.Name == name {
			return v.Value
		}
	}

	return ""
}

func TestResolvePlacementClusterNames(t *testing.T) {
	scheme := imageRegistryTestScheme(t)

	mcir := &imageregistryv1alpha1.ManagedClusterImageRegistry{
		ObjectMeta: metav1.ObjectMeta{Name: "eks-imageregistry", Namespace: "eks-xili-1"},
		Spec: imageregistryv1alpha1.ImageRegistrySpec{
			PlacementRef: imageregistryv1alpha1.PlacementRef{
				Group:    "cluster.open-cluster-management.io",
				Resource: "placements",
				Name:     "eks-placement",
			},
		},
	}

	placement := &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{Name: "eks-placement", Namespace: "eks-xili-1"},
	}

	placementDecision := &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "eks-placement-decision-1",
			Namespace: "eks-xili-1",
			Labels:    map[string]string{placementDecisionClusterLabel: "eks-placement"},
		},
		Status: clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{
				{ClusterName: "eks-cluster1"},
				{ClusterName: "eks-cluster2"},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(placement, placementDecision).Build()

	names, err := resolvePlacementClusterNames(context.TODO(), c, mcir)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"eks-cluster1", "eks-cluster2"}, names)

	t.Run("invalid placementRef group/resource", func(t *testing.T) {
		bad := mcir.DeepCopy()
		bad.Spec.PlacementRef.Group = "wrong.group"

		_, err := resolvePlacementClusterNames(context.TODO(), c, bad)
		assert.Error(t, err)
	})

	t.Run("missing placement", func(t *testing.T) {
		bad := mcir.DeepCopy()
		bad.Spec.PlacementRef.Name = "does-not-exist"

		_, err := resolvePlacementClusterNames(context.TODO(), c, bad)
		assert.Error(t, err)
	})
}

func TestFindGitOpsAddonDeploymentConfig(t *testing.T) {
	scheme := imageRegistryTestScheme(t)

	mca := &addonv1alpha1.ManagedClusterAddOn{
		ObjectMeta: metav1.ObjectMeta{Name: gitopsAddonName, Namespace: "eks-xili-1"},
		Spec: addonv1alpha1.ManagedClusterAddOnSpec{
			Configs: []addonv1alpha1.AddOnConfig{
				{
					ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
						Group:    addonDeploymentConfigGroup,
						Resource: addonDeploymentConfigResource,
					},
					ConfigReferent: addonv1alpha1.ConfigReferent{
						Name:      "gitops-addon-config",
						Namespace: "eks-xili-1",
					},
				},
			},
		},
	}

	adc := &addonv1alpha1.AddOnDeploymentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "gitops-addon-config", Namespace: "eks-xili-1"},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mca, adc).Build()
	r := &ReconcileImageRegistry{Client: c, scheme: scheme}

	got, err := r.findGitOpsAddonDeploymentConfig(context.TODO(), "eks-xili-1")
	require.NoError(t, err)
	assert.Equal(t, "gitops-addon-config", got.Name)

	t.Run("no ManagedClusterAddOn", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &ReconcileImageRegistry{Client: c, scheme: scheme}

		_, err := r.findGitOpsAddonDeploymentConfig(context.TODO(), "no-such-cluster")
		assert.True(t, k8errors.IsNotFound(err))
	})

	t.Run("ManagedClusterAddOn has no addondeploymentconfigs config", func(t *testing.T) {
		mcaNoConfig := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{Name: gitopsAddonName, Namespace: "cluster-no-config"},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcaNoConfig).Build()
		r := &ReconcileImageRegistry{Client: c, scheme: scheme}

		_, err := r.findGitOpsAddonDeploymentConfig(context.TODO(), "cluster-no-config")
		assert.True(t, k8errors.IsNotFound(err))
	})
}

// buildImageRegistryFixture creates a ManagedClusterImageRegistry targeting a single managed
// cluster (via a Placement/PlacementDecision) whose gitops-addon AddOnDeploymentConfig carries a
// couple of image variables sourced from registry.redhat.io, plus one unrelated variable.
func buildImageRegistryFixture() (*imageregistryv1alpha1.ManagedClusterImageRegistry, []client.Object) {
	const ns = "eks-xili-1"

	mcir := &imageregistryv1alpha1.ManagedClusterImageRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "eks-imageregistry",
			Namespace: ns,
			Annotations: map[string]string{
				gitopsAddonImageMirroringAnnotation: "true",
			},
		},
		Spec: imageregistryv1alpha1.ImageRegistrySpec{
			PlacementRef: imageregistryv1alpha1.PlacementRef{
				Group:    "cluster.open-cluster-management.io",
				Resource: "placements",
				Name:     "eks-placement",
			},
			PullSecret: corev1.LocalObjectReference{Name: "ecr-pullsecret"},
			Registries: []imageregistryv1alpha1.Registries{
				{Source: "registry.redhat.io", Mirror: "999569342541.dkr.ecr.us-east-1.amazonaws.com"},
			},
		},
	}

	placement := &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{Name: "eks-placement", Namespace: ns},
	}

	placementDecision := &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "eks-placement-decision-1",
			Namespace: ns,
			Labels:    map[string]string{placementDecisionClusterLabel: "eks-placement"},
		},
		Status: clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{{ClusterName: "eks-cluster1"}},
		},
	}

	mca := &addonv1alpha1.ManagedClusterAddOn{
		ObjectMeta: metav1.ObjectMeta{Name: gitopsAddonName, Namespace: "eks-cluster1"},
		Spec: addonv1alpha1.ManagedClusterAddOnSpec{
			Configs: []addonv1alpha1.AddOnConfig{
				{
					ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
						Group:    addonDeploymentConfigGroup,
						Resource: addonDeploymentConfigResource,
					},
					ConfigReferent: addonv1alpha1.ConfigReferent{
						Name:      "gitops-addon-config",
						Namespace: "eks-cluster1",
					},
				},
			},
		},
	}

	adc := &addonv1alpha1.AddOnDeploymentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "gitops-addon-config", Namespace: "eks-cluster1"},
		Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
			CustomizedVariables: []addonv1alpha1.CustomizedVariable{
				{Name: "ARGOCD_IMAGE", Value: "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd"},
				{Name: "ARGOCD_REDIS_IMAGE", Value: "registry.redhat.io/rhel9/redis-7@sha256:1234"},
				{Name: "ARGOCD_AGENT_MODE", Value: "managed"},
			},
		},
	}

	return mcir, []client.Object{mcir, placement, placementDecision, mca, adc}
}

func reconcileNTimes(t *testing.T, r *ReconcileImageRegistry, req reconcile.Request, n int) {
	t.Helper()

	for i := 0; i < n; i++ {
		_, err := r.Reconcile(context.TODO(), req)
		require.NoError(t, err)
	}
}

func TestReconcile_AppliesImageMirroring(t *testing.T) {
	scheme := imageRegistryTestScheme(t)
	mcir, objs := buildImageRegistryFixture()

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&imageregistryv1alpha1.ManagedClusterImageRegistry{}).Build()
	r := &ReconcileImageRegistry{Client: c, scheme: scheme}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mcir.Namespace, Name: mcir.Name}}

	// First pass only adds the finalizer; second pass does the actual mirroring work.
	reconcileNTimes(t, r, req, 2)

	adc := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, adc))

	assert.Equal(t, "999569342541.dkr.ecr.us-east-1.amazonaws.com/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(adc.Spec.CustomizedVariables, "ARGOCD_IMAGE"))
	assert.Equal(t, "999569342541.dkr.ecr.us-east-1.amazonaws.com/rhel9/redis-7@sha256:1234", findVar(adc.Spec.CustomizedVariables, "ARGOCD_REDIS_IMAGE"))
	assert.Equal(t, "managed", findVar(adc.Spec.CustomizedVariables, "ARGOCD_AGENT_MODE"), "non-image variables must be left untouched")

	assert.Equal(t, "eks-xili-1/eks-imageregistry", adc.Annotations[adcManagedByAnnotation])
	assert.NotEmpty(t, adc.Annotations[adcOriginalValuesAnnotation])

	updated := &imageregistryv1alpha1.ManagedClusterImageRegistry{}
	require.NoError(t, c.Get(context.TODO(), req.NamespacedName, updated))
	assert.Contains(t, updated.Finalizers, imageRegistryFinalizer)

	// Reconciling again must be a no-op (idempotent) -- no additional changes expected.
	reconcileNTimes(t, r, req, 1)

	adcAfter := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, adcAfter))
	assert.Equal(t, adc.Spec.CustomizedVariables, adcAfter.Spec.CustomizedVariables)
}

func TestReconcile_SelfHealsAfterExternalDrift(t *testing.T) {
	scheme := imageRegistryTestScheme(t)
	mcir, objs := buildImageRegistryFixture()

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&imageregistryv1alpha1.ManagedClusterImageRegistry{}).Build()
	r := &ReconcileImageRegistry{Client: c, scheme: scheme}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mcir.Namespace, Name: mcir.Name}}
	reconcileNTimes(t, r, req, 2)

	// Simulate the gitopscluster controller resetting the image back to a newer source value on
	// its own, unrelated reconcile (e.g. the hub operator's default image changed).
	adc := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, adc))

	for i, v := range adc.Spec.CustomizedVariables {
		if v.Name == "ARGOCD_IMAGE" {
			adc.Spec.CustomizedVariables[i].Value = "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:NEWDIGEST"
		}
	}

	require.NoError(t, c.Update(context.TODO(), adc))

	_, err := r.Reconcile(context.TODO(), req)
	require.NoError(t, err)

	healed := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, healed))
	assert.Equal(t, "999569342541.dkr.ecr.us-east-1.amazonaws.com/openshift-gitops-1/argocd-rhel9@sha256:NEWDIGEST", findVar(healed.Spec.CustomizedVariables, "ARGOCD_IMAGE"))
}

func TestReconcile_DeleteRevertsImageMirroring(t *testing.T) {
	scheme := imageRegistryTestScheme(t)
	mcir, objs := buildImageRegistryFixture()

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&imageregistryv1alpha1.ManagedClusterImageRegistry{}).Build()
	r := &ReconcileImageRegistry{Client: c, scheme: scheme}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mcir.Namespace, Name: mcir.Name}}
	reconcileNTimes(t, r, req, 2)

	// Confirm mirroring actually applied before testing the revert.
	mirrored := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, mirrored))
	require.Equal(t, "999569342541.dkr.ecr.us-east-1.amazonaws.com/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(mirrored.Spec.CustomizedVariables, "ARGOCD_IMAGE"))

	require.NoError(t, c.Delete(context.TODO(), mcir))

	_, err := r.Reconcile(context.TODO(), req)
	require.NoError(t, err)

	reverted := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, reverted))
	assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(reverted.Spec.CustomizedVariables, "ARGOCD_IMAGE"))
	assert.Equal(t, "registry.redhat.io/rhel9/redis-7@sha256:1234", findVar(reverted.Spec.CustomizedVariables, "ARGOCD_REDIS_IMAGE"))
	assert.NotContains(t, reverted.Annotations, adcManagedByAnnotation)
	assert.NotContains(t, reverted.Annotations, adcOriginalValuesAnnotation)

	// The ManagedClusterImageRegistry itself should be fully gone now that its finalizer was
	// removed.
	gone := &imageregistryv1alpha1.ManagedClusterImageRegistry{}
	err = c.Get(context.TODO(), req.NamespacedName, gone)
	assert.True(t, k8errors.IsNotFound(err))
}

// TestReconcile_DeleteRevertsViaClusterWideScanWhenPlacementGone confirms that deletion cleanup
// does not depend on caching the resolved cluster list anywhere (see the comment on
// revertImageMirroring for why that would be an unbounded-annotation-size hazard at hub scale):
// even with the Placement gone, the unconditional cluster-wide AddOnDeploymentConfig scan alone
// must still find and revert everything this ManagedClusterImageRegistry mirrored.
func TestReconcile_DeleteRevertsViaClusterWideScanWhenPlacementGone(t *testing.T) {
	scheme := imageRegistryTestScheme(t)
	mcir, objs := buildImageRegistryFixture()

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&imageregistryv1alpha1.ManagedClusterImageRegistry{}).Build()
	r := &ReconcileImageRegistry{Client: c, scheme: scheme}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mcir.Namespace, Name: mcir.Name}}
	reconcileNTimes(t, r, req, 2)

	// Remove the Placement out from under the ManagedClusterImageRegistry before deleting it, to
	// simulate the Placement having already been cleaned up.
	placement := &clusterv1beta1.Placement{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-xili-1", Name: "eks-placement"}, placement))
	require.NoError(t, c.Delete(context.TODO(), placement))

	require.NoError(t, c.Delete(context.TODO(), mcir))

	_, err := r.Reconcile(context.TODO(), req)
	require.NoError(t, err)

	reverted := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, reverted))
	assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(reverted.Spec.CustomizedVariables, "ARGOCD_IMAGE"))
}

func TestWantsGitOpsAddonImageMirroring(t *testing.T) {
	assert.False(t, wantsGitOpsAddonImageMirroring(nil))
	assert.False(t, wantsGitOpsAddonImageMirroring(&imageregistryv1alpha1.ManagedClusterImageRegistry{}))
	assert.False(t, wantsGitOpsAddonImageMirroring(&imageregistryv1alpha1.ManagedClusterImageRegistry{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{gitopsAddonImageMirroringAnnotation: "false"}},
	}))
	assert.True(t, wantsGitOpsAddonImageMirroring(&imageregistryv1alpha1.ManagedClusterImageRegistry{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{gitopsAddonImageMirroringAnnotation: "true"}},
	}))
}

func TestReconcile_SkipsWithoutOptInAnnotation(t *testing.T) {
	scheme := imageRegistryTestScheme(t)
	mcir, objs := buildImageRegistryFixture()
	mcir.Annotations = nil

	// The fixture's MCIR is also in objs; replace it with the unannotated copy.
	for i, obj := range objs {
		if existing, ok := obj.(*imageregistryv1alpha1.ManagedClusterImageRegistry); ok && existing.Name == mcir.Name {
			objs[i] = mcir
		}
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&imageregistryv1alpha1.ManagedClusterImageRegistry{}).Build()
	r := &ReconcileImageRegistry{Client: c, scheme: scheme}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mcir.Namespace, Name: mcir.Name}}
	reconcileNTimes(t, r, req, 2)

	adc := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, adc))
	assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(adc.Spec.CustomizedVariables, "ARGOCD_IMAGE"),
		"unannotated ManagedClusterImageRegistry must not rewrite gitops-addon images")
	assert.NotContains(t, adc.Annotations, adcManagedByAnnotation)

	updated := &imageregistryv1alpha1.ManagedClusterImageRegistry{}
	require.NoError(t, c.Get(context.TODO(), req.NamespacedName, updated))
	assert.NotContains(t, updated.Finalizers, imageRegistryFinalizer)
}

func TestReconcile_AnnotationRemovedRevertsMirroring(t *testing.T) {
	scheme := imageRegistryTestScheme(t)
	mcir, objs := buildImageRegistryFixture()

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&imageregistryv1alpha1.ManagedClusterImageRegistry{}).Build()
	r := &ReconcileImageRegistry{Client: c, scheme: scheme}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mcir.Namespace, Name: mcir.Name}}
	reconcileNTimes(t, r, req, 2)

	mirrored := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, mirrored))
	require.Equal(t, "999569342541.dkr.ecr.us-east-1.amazonaws.com/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(mirrored.Spec.CustomizedVariables, "ARGOCD_IMAGE"))

	live := &imageregistryv1alpha1.ManagedClusterImageRegistry{}
	require.NoError(t, c.Get(context.TODO(), req.NamespacedName, live))
	live.Annotations = map[string]string{}
	require.NoError(t, c.Update(context.TODO(), live))

	_, err := r.Reconcile(context.TODO(), req)
	require.NoError(t, err)

	reverted := &addonv1alpha1.AddOnDeploymentConfig{}
	require.NoError(t, c.Get(context.TODO(), types.NamespacedName{Namespace: "eks-cluster1", Name: "gitops-addon-config"}, reverted))
	assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abcd", findVar(reverted.Spec.CustomizedVariables, "ARGOCD_IMAGE"))
	assert.NotContains(t, reverted.Annotations, adcManagedByAnnotation)

	cleaned := &imageregistryv1alpha1.ManagedClusterImageRegistry{}
	require.NoError(t, c.Get(context.TODO(), req.NamespacedName, cleaned))
	assert.NotContains(t, cleaned.Finalizers, imageRegistryFinalizer)
}

func TestImageRegistryPlacementDecisionMapper_SkipsUnannotated(t *testing.T) {
	scheme := imageRegistryTestScheme(t)

	annotated := &imageregistryv1alpha1.ManagedClusterImageRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "opted-in",
			Namespace:   "ns",
			Annotations: map[string]string{gitopsAddonImageMirroringAnnotation: "true"},
		},
		Spec: imageregistryv1alpha1.ImageRegistrySpec{
			PlacementRef: imageregistryv1alpha1.PlacementRef{Name: "the-placement"},
		},
	}
	unannotated := &imageregistryv1alpha1.ManagedClusterImageRegistry{
		ObjectMeta: metav1.ObjectMeta{Name: "ignored", Namespace: "ns"},
		Spec: imageregistryv1alpha1.ImageRegistrySpec{
			PlacementRef: imageregistryv1alpha1.PlacementRef{Name: "the-placement"},
		},
	}
	pd := &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pd",
			Namespace: "ns",
			Labels:    map[string]string{placementDecisionClusterLabel: "the-placement"},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(annotated, unannotated, pd).Build()
	mapper := &imageRegistryPlacementDecisionMapper{Client: c}

	requests := mapper.Map(context.TODO(), pd)
	require.Len(t, requests, 1)
	assert.Equal(t, "opted-in", requests[0].Name)
}
