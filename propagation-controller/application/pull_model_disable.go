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
	"fmt"

	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workv1 "open-cluster-management.io/api/work/v1"
)

// SweepManifestWorksToReadOnly patches every ManifestWork this propagation controller has
// ever generated to updateStrategy=ReadOnly, so the spoke's klusterlet work-agent stops
// re-asserting/enforcing their payload. It is meant to be called once, at startup, only
// when the basic pull model is disabled (see pkg/pullmodelconfig) -- with Reconcile already
// a no-op in that state (see application_controller.go and application_status_controller.go),
// nothing will ever call generateManifestWork again to undo this, so a single pass here is
// permanently sufficient. Safe to call on every startup regardless: matching ManifestWorks
// that are already ReadOnly are left untouched, so repeat calls (e.g. across pod restarts)
// are fast no-ops after the first one actually does the work.
//
// Matching is content-based, not name- or position-based, specifically so this cannot ever
// touch a ManifestWork it doesn't recognize as one of its own:
//  1. Must carry the hub-application-name annotation -- set only by generateManifestWork,
//     on nothing else in the system.
//  2. Must carry one of the two mutually-exclusive ownership labels generateManifestWork
//     sets depending on whether the source Application came from an ApplicationSet or was
//     hand-created.
//  3. Within a matching ManifestWork, only the specific manifestConfigs entry whose
//     resourceIdentifier is {group: argoproj.io, resource: applications} is touched -- never
//     by positional index, so any other manifest config entry a ManifestWork might carry is
//     left completely alone.
//
// Deliberately never deletes anything -- see CLAUDE.md for why deleting a pull-model
// ManifestWork is destructive to the real workload it delivered.
func SweepManifestWorksToReadOnly(ctx context.Context, c client.Client) error {
	var mwList workv1.ManifestWorkList
	if err := c.List(ctx, &mwList); err != nil {
		return fmt.Errorf("failed to list ManifestWorks for pull-model disable sweep: %w", err)
	}

	var swept, alreadyDone, skipped int

	for i := range mwList.Items {
		mw := &mwList.Items[i]

		if !isPullModelManifestWork(mw) {
			skipped++
			continue
		}

		changed := false

		for j := range mw.Spec.ManifestConfigs {
			mc := &mw.Spec.ManifestConfigs[j]
			if mc.ResourceIdentifier.Group != "argoproj.io" || mc.ResourceIdentifier.Resource != "applications" {
				continue
			}

			if mc.UpdateStrategy != nil && mc.UpdateStrategy.Type == workv1.UpdateStrategyTypeReadOnly {
				continue
			}

			mc.UpdateStrategy = &workv1.UpdateStrategy{Type: workv1.UpdateStrategyTypeReadOnly}
			changed = true
		}

		if !changed {
			alreadyDone++
			continue
		}

		if err := c.Update(ctx, mw); err != nil {
			klog.Errorf("pull-model disable sweep: failed to patch ManifestWork %s/%s to ReadOnly: %v", mw.Namespace, mw.Name, err)
			continue
		}

		klog.Infof("pull-model disable sweep: patched ManifestWork %s/%s to ReadOnly", mw.Namespace, mw.Name)
		swept++
	}

	klog.Infof("pull-model disable sweep complete: %d patched to ReadOnly, %d already ReadOnly, %d not pull-model-owned (skipped)",
		swept, alreadyDone, skipped)

	return nil
}

// isPullModelManifestWork reports whether mw is one this propagation controller generated,
// by content -- never by name or namespace guessing.
func isPullModelManifestWork(mw *workv1.ManifestWork) bool {
	if _, ok := mw.Annotations[AnnotationKeyHubApplicationName]; !ok {
		return false
	}

	if v, ok := mw.Labels[LabelKeyAppSet]; ok && v == "true" {
		return true
	}

	if v, ok := mw.Labels[LabelKeyPull]; ok && v == "true" {
		return true
	}

	return false
}
