// Copyright 2021 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package multiclusterstatusaggregation

import (
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	appsetreportV1alpha1 "open-cluster-management.io/multicloud-integrations/pkg/apis/appsetreport/v1alpha1"
)

func TestAppNameFromClusterConditionApp(t *testing.T) {
	tests := []struct {
		name string
		app  string
		want string
	}{
		{"manifestwork-sourced (4 segments)", "openshift-gitops/platform-projects-archigrp-osf-gvz-evz02/osf-gvz-evz02/platform-projects-archigrp-osf-gvz-evz02-c18f6", "platform-projects-archigrp-osf-gvz-evz02"},
		{"yaml-sourced (2 segments)", "openshift-gitops/platform-projects-archigrp-osf-gvz-evz02", "platform-projects-archigrp-osf-gvz-evz02"},
		{"no separator", "platform-projects-archigrp-osf-gvz-evz02", "platform-projects-archigrp-osf-gvz-evz02"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appNameFromClusterConditionApp(tt.app); got != tt.want {
				t.Errorf("appNameFromClusterConditionApp(%q) = %q, want %q", tt.app, got, tt.want)
			}
		})
	}
}

// TestGenerateSummary_MultipleAppsOnSameCluster reproduces the scenario reported by the customer:
// a single ApplicationSet (via a git-directory generator) creates more than one Application
// targeting the same managed cluster. Before the fix, appSetClusterStatusMap was keyed only by
// cluster name, so the second ManifestWork for the same cluster silently overwrote the first in
// the map and only one of the two Applications ever showed up in the resulting
// MulticlusterApplicationSetReport (and therefore only one ever got its hub-side Application
// status updated).
func TestGenerateSummary_MultipleAppsOnSameCluster(t *testing.T) {
	r := &ReconcilePullModelAggregation{}

	appset := AppSet{types.NamespacedName{Namespace: "openshift-gitops", Name: "platform-projects-all-placement"}}

	appSetClusterStatusMap := map[AppSet]map[Cluster]OverallStatus{
		appset: {
			{clusterName: "osf-gvz-evz02", appName: "platform-projects-archigrp-osf-gvz-evz02"}: {
				HealthStatus: "Healthy",
				SyncStatus:   "Synced",
				App:          "openshift-gitops/platform-projects-archigrp-osf-gvz-evz02/osf-gvz-evz02/platform-projects-archigrp-osf-gvz-evz02-c18f6",
			},
			{clusterName: "osf-gvz-evz02", appName: "platform-projects-unblu-osf-gvz-evz02"}: {
				HealthStatus: "Healthy",
				SyncStatus:   "Synced",
				App:          "openshift-gitops/platform-projects-unblu-osf-gvz-evz02/osf-gvz-evz02/platform-projects-unblu-osf-gvz-evz02-abcde",
			},
		},
	}

	summary, conditions := r.generateSummary(appSetClusterStatusMap, appset, []appsetreportV1alpha1.ClusterCondition{})

	if summary.Clusters != "2" {
		t.Errorf("summary.Clusters = %v, want 2 (one entry per Application, not collapsed per cluster)", summary.Clusters)
	}

	if len(conditions) != 2 {
		t.Fatalf("got %d ClusterConditions, want 2 (one per Application on the shared cluster): %+v", len(conditions), conditions)
	}

	gotApps := []string{
		appNameFromClusterConditionApp(conditions[0].App),
		appNameFromClusterConditionApp(conditions[1].App),
	}
	sort.Strings(gotApps)

	wantApps := []string{"platform-projects-archigrp-osf-gvz-evz02", "platform-projects-unblu-osf-gvz-evz02"}

	if gotApps[0] != wantApps[0] || gotApps[1] != wantApps[1] {
		t.Errorf("got apps %v, want %v", gotApps, wantApps)
	}
}
