/*
Copyright 2026.

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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	workv1 "open-cluster-management.io/api/work/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newSweepTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	if err := workv1.AddToScheme(s); err != nil {
		t.Fatalf("failed to build test scheme: %v", err)
	}

	return s
}

func appSetOwnedManifestWork(name, ns, strategy string) *workv1.ManifestWork {
	mw := &workv1.ManifestWork{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{LabelKeyAppSet: "true"},
			Annotations: map[string]string{
				AnnotationKeyHubApplicationName:      "some-app",
				AnnotationKeyHubApplicationNamespace: "openshift-gitops",
			},
		},
		Spec: workv1.ManifestWorkSpec{
			ManifestConfigs: []workv1.ManifestConfigOption{
				{
					ResourceIdentifier: workv1.ResourceIdentifier{
						Group:     "argoproj.io",
						Resource:  "applications",
						Namespace: "openshift-gitops",
						Name:      "some-app",
					},
				},
			},
		},
	}

	if strategy != "" {
		mw.Spec.ManifestConfigs[0].UpdateStrategy = &workv1.UpdateStrategy{Type: workv1.UpdateStrategyType(strategy)}
	}

	return mw
}

func TestSweepManifestWorksToReadOnly_PatchesMatchingManifestWork(t *testing.T) {
	scheme := newSweepTestScheme(t)
	mw := appSetOwnedManifestWork("app-abcde", "cluster1", "ServerSideApply")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mw).Build()

	if err := SweepManifestWorksToReadOnly(context.Background(), c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &workv1.ManifestWork{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app-abcde", Namespace: "cluster1"}, got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Spec.ManifestConfigs[0].UpdateStrategy == nil || got.Spec.ManifestConfigs[0].UpdateStrategy.Type != workv1.UpdateStrategyTypeReadOnly {
		t.Fatalf("expected updateStrategy to be patched to ReadOnly, got %+v", got.Spec.ManifestConfigs[0].UpdateStrategy)
	}
}

func TestSweepManifestWorksToReadOnly_StandaloneAppPullLabelAlsoMatches(t *testing.T) {
	scheme := newSweepTestScheme(t)
	mw := &workv1.ManifestWork{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-only",
			Namespace: "cluster1",
			Labels:    map[string]string{LabelKeyPull: "true"},
			Annotations: map[string]string{
				AnnotationKeyHubApplicationName: "some-app",
			},
		},
		Spec: workv1.ManifestWorkSpec{
			ManifestConfigs: []workv1.ManifestConfigOption{
				{
					ResourceIdentifier: workv1.ResourceIdentifier{Group: "argoproj.io", Resource: "applications", Namespace: "argocd", Name: "some-app"},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mw).Build()

	if err := SweepManifestWorksToReadOnly(context.Background(), c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &workv1.ManifestWork{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "app-only", Namespace: "cluster1"}, got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Spec.ManifestConfigs[0].UpdateStrategy == nil || got.Spec.ManifestConfigs[0].UpdateStrategy.Type != workv1.UpdateStrategyTypeReadOnly {
		t.Fatalf("expected standalone pull-labeled ManifestWork to also be patched to ReadOnly")
	}
}

func TestSweepManifestWorksToReadOnly_AlreadyReadOnlyIsNoOp(t *testing.T) {
	scheme := newSweepTestScheme(t)
	mw := appSetOwnedManifestWork("already-ro", "cluster1", "ReadOnly")
	originalGen := mw.Generation
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mw).Build()

	if err := SweepManifestWorksToReadOnly(context.Background(), c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &workv1.ManifestWork{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "already-ro", Namespace: "cluster1"}, got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Generation != originalGen {
		t.Fatalf("expected no update (generation unchanged) for an already-ReadOnly ManifestWork, got generation %d -> %d", originalGen, got.Generation)
	}
}

func TestSweepManifestWorksToReadOnly_UnrelatedManifestWorkUntouched(t *testing.T) {
	scheme := newSweepTestScheme(t)
	// No hub-application-name annotation, no pull-model ownership label -- e.g. an
	// addon-deploy ManifestWork. Must never be touched by the sweep.
	unrelated := &workv1.ManifestWork{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "addon-gitops-addon-deploy-0",
			Namespace: "cluster1",
		},
		Spec: workv1.ManifestWorkSpec{
			ManifestConfigs: []workv1.ManifestConfigOption{
				{
					ResourceIdentifier: workv1.ResourceIdentifier{Group: "apps", Resource: "deployments", Namespace: "open-cluster-management-agent-addon", Name: "gitops-addon"},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unrelated).Build()

	if err := SweepManifestWorksToReadOnly(context.Background(), c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &workv1.ManifestWork{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "addon-gitops-addon-deploy-0", Namespace: "cluster1"}, got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Spec.ManifestConfigs[0].UpdateStrategy != nil {
		t.Fatalf("expected an unrelated ManifestWork to be left completely untouched, got updateStrategy=%+v", got.Spec.ManifestConfigs[0].UpdateStrategy)
	}
}

func TestSweepManifestWorksToReadOnly_OnlyTouchesApplicationsManifestConfig(t *testing.T) {
	scheme := newSweepTestScheme(t)
	mw := &workv1.ManifestWork{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "multi-config",
			Namespace: "cluster1",
			Labels:    map[string]string{LabelKeyAppSet: "true"},
			Annotations: map[string]string{
				AnnotationKeyHubApplicationName: "some-app",
			},
		},
		Spec: workv1.ManifestWorkSpec{
			ManifestConfigs: []workv1.ManifestConfigOption{
				{
					// A hypothetical second entry for a different resource -- must be
					// left alone even though this ManifestWork matches overall.
					ResourceIdentifier: workv1.ResourceIdentifier{Group: "", Resource: "namespaces", Name: "guestbook"},
				},
				{
					ResourceIdentifier: workv1.ResourceIdentifier{Group: "argoproj.io", Resource: "applications", Namespace: "openshift-gitops", Name: "some-app"},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mw).Build()

	if err := SweepManifestWorksToReadOnly(context.Background(), c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &workv1.ManifestWork{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "multi-config", Namespace: "cluster1"}, got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Spec.ManifestConfigs[0].UpdateStrategy != nil {
		t.Fatalf("expected the namespaces manifestConfig entry to be untouched, got %+v", got.Spec.ManifestConfigs[0].UpdateStrategy)
	}

	if got.Spec.ManifestConfigs[1].UpdateStrategy == nil || got.Spec.ManifestConfigs[1].UpdateStrategy.Type != workv1.UpdateStrategyTypeReadOnly {
		t.Fatalf("expected the applications manifestConfig entry to be patched to ReadOnly, got %+v", got.Spec.ManifestConfigs[1].UpdateStrategy)
	}
}
