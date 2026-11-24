// Copyright 2022 The Kubernetes Authors.
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

package utils

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestDefaultOperatorImagesMatchChart fails when the generated image list drifts from the
// operator chart's values.yaml. It enumerates the chart's own keys, so a chart bump that adds a
// component image can never silently leave it out of the disconnected-mirroring pipeline.
// Regenerate with `go generate ./pkg/utils`.
func TestDefaultOperatorImagesMatchChart(t *testing.T) {
	data, err := os.ReadFile("../../gitopsaddon/charts/openshift-gitops-operator/values.yaml")
	if err != nil {
		t.Fatalf("read chart values: %v", err)
	}

	var values struct {
		Image struct {
			Manager struct {
				Registry   string `json:"registry"`
				Repository string `json:"repository"`
				Tag        string `json:"tag"`
			} `json:"manager"`
		} `json:"image"`
		OperatorImageEnv map[string]string `json:"operatorImageEnv"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("parse chart values: %v", err)
	}

	if len(values.OperatorImageEnv) == 0 {
		t.Fatal("chart values has no operatorImageEnv entries")
	}

	manager := values.Image.Manager
	operatorImage := manager.Registry + "/" + manager.Repository
	switch {
	case manager.Tag == "":
	case strings.HasPrefix(manager.Tag, "sha256:"):
		operatorImage += "@" + manager.Tag
	default:
		operatorImage += ":" + manager.Tag
	}

	if got := DefaultOperatorImages[EnvGitOpsOperatorImage]; got != operatorImage {
		t.Errorf("%s: generated %q, chart has %q (run go generate ./pkg/utils)",
			EnvGitOpsOperatorImage, got, operatorImage)
	}

	// ROLLOUTS_EXTENSION_IMAGE duplicates ARGOCD_ROLLOUTS_IMAGE and is synthesized by
	// templateAndApplyChart from ARGOCD_EXTENSION_IMAGE; every other chart key must appear in
	// the generated map under the same name (ARGOCD_ROLLOUTS_IMAGE under its legacy name).
	expectedEntries := 1 // the operator image
	for chartKey, chartValue := range values.OperatorImageEnv {
		switch chartKey {
		case "ROLLOUTS_EXTENSION_IMAGE":
			if got := DefaultOperatorImages[EnvArgoCDExtensionImage]; got != chartValue {
				t.Errorf("%s alias: generated %q, chart has %q", chartKey, got, chartValue)
			}
			continue
		case "ARGOCD_ROLLOUTS_IMAGE":
			if got := DefaultOperatorImages[EnvArgoCDExtensionImage]; got != chartValue {
				t.Errorf("%s: generated %q, chart has %q", EnvArgoCDExtensionImage, got, chartValue)
			}
			expectedEntries++ // exposed as the legacy ARGOCD_EXTENSION_IMAGE name
			continue
		}
		expectedEntries++
		if got := DefaultOperatorImages[chartKey]; got != chartValue {
			t.Errorf("%s: generated %q, chart has %q (run go generate ./pkg/utils)",
				chartKey, got, chartValue)
		}
	}

	if len(DefaultOperatorImages) != expectedEntries {
		t.Errorf("DefaultOperatorImages has %d entries, chart requires %d", len(DefaultOperatorImages), expectedEntries)
	}

	// No entry may exist without a corresponding chart key, so a removed chart image is caught too.
	chartEntryFor := func(envKey string) bool {
		if envKey == EnvArgoCDExtensionImage {
			_, ok := values.OperatorImageEnv["ARGOCD_ROLLOUTS_IMAGE"]
			return ok
		}
		_, ok := values.OperatorImageEnv[envKey]
		return ok
	}
	for envKey := range DefaultOperatorImages {
		if envKey == EnvGitOpsOperatorImage {
			continue
		}
		if !chartEntryFor(envKey) {
			t.Errorf("%s has no matching chart operatorImageEnv key", envKey)
		}
	}
}
