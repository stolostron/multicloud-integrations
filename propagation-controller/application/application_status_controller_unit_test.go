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

// This file deliberately carries no "integration" build tag (unlike
// application_status_controller_test.go, which hosts the Ginkgo/envtest-based spec for the same
// reconciler): it uses only a fake client, needs no real control plane, and must run under the
// plain "make -f Makefile.prow test" / "make test" ("test-unit") targets, not just
// "make test-integration".
package application

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsetreportV1alpha1 "open-cluster-management.io/multicloud-integrations/pkg/apis/appsetreport/v1alpha1"
)

// Test_ApplicationStatusReconciler_HealthyNotStuckOnProgressing is a regression test for a bug
// in Reconcile's "simulate Progressing" handling: whenever a MulticlusterApplicationSetReport's
// ClusterCondition reports HealthStatus "Healthy", the reconciler force-writes "Progressing"
// directly to the live Application object (so the ApplicationSet controller sees a real
// transition), then is only supposed to restore "Healthy" afterward.
//
// That restore used to be gated behind a diff against a status snapshot captured *before* the
// forced "Progressing" write. If the current reconcile ends up computing the exact same final
// status as that pre-mutation snapshot (e.g. a later reconcile of the same report, triggered by
// an unrelated sibling app/cluster condition changing elsewhere in the same report, where this
// particular app's condition is unchanged since the last time it was written), the diff saw no
// difference and skipped the restore -- permanently leaving the Application stuck reporting
// "Progressing" even though it was already, and still should be, "Healthy".
//
// Unlike the Ginkgo spec in application_status_controller_test.go, this is a plain fake-client
// unit test (no envtest manager/watch loop needed) that calls Reconcile directly, twice, to
// deterministically exercise both the first-time-Healthy path and the
// already-Healthy-reconciled-again path.
func Test_ApplicationStatusReconciler_HealthyNotStuckOnProgressing(t *testing.T) {
	const (
		appName      = "app-regression"
		appNamespace = "argocd"
		reportName   = "report-regression"
	)

	scheme := runtime.NewScheme()
	if err := appsetreportV1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add appsetreportV1alpha1 to scheme: %v", err)
	}

	appGVK := schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}

	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(appGVK)
	app.SetName(appName)
	app.SetNamespace(appNamespace)

	report := &appsetreportV1alpha1.MulticlusterApplicationSetReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reportName,
			Namespace: appNamespace,
		},
		Statuses: appsetreportV1alpha1.AppConditions{
			ClusterConditions: []appsetreportV1alpha1.ClusterCondition{
				{
					SyncStatus:              "Synced",
					HealthStatus:            "Healthy",
					OperationStateStartedAt: "2025-04-24T19:53:38Z",
					OperationStatePhase:     "Succeeded",
					SyncRevision:            "4773b9f1f8fd425f84174c338012771c4e9a989c",
					App:                     appNamespace + "/" + appName,
					Cluster:                 "cluster1",
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(report, app).Build()

	r := &ApplicationStatusReconciler{Client: fakeClient, Scheme: scheme}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: reportName, Namespace: appNamespace}}
	ctx := context.Background()

	getAppHealth := func() string {
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(appGVK)

		if err := fakeClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: appNamespace}, got); err != nil {
			t.Fatalf("Get(application) error = %v", err)
		}

		health, _, _ := unstructured.NestedString(got.Object, "status", "health", "status")

		return health
	}

	// First reconcile: establishes Healthy from a blank status -- mirrors the scenario already
	// covered by the envtest-based "Should update Application status" test in
	// application_status_controller_test.go.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}

	if got := getAppHealth(); got != "Healthy" {
		t.Fatalf("after first Reconcile(), health status = %q, want %q", got, "Healthy")
	}

	// Second reconcile: the same report is reconciled again (e.g. triggered by an unrelated
	// sibling app/cluster condition changing elsewhere in the same report), but THIS app's
	// cluster condition is byte-for-byte identical to what was already persisted last time.
	// This must not leave the Application stuck on the intermediate "Progressing" value.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}

	if got := getAppHealth(); got != "Healthy" {
		t.Fatalf("after second Reconcile() of an unchanged already-Healthy condition, "+
			"health status = %q, want %q (regression: stuck on intermediate \"Progressing\" value)", got, "Healthy")
	}
}
