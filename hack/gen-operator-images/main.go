// Copyright Contributors to the Open Cluster Management project
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

// Generates pkg/utils/operator_images_generated.go from the operator chart's
// values.yaml. Runs as part of `make build`; also available via
// `go generate ./pkg/utils` and `go run ./hack/gen-operator-images -check`.
package main

import (
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	chartValuesRelPath = "gitopsaddon/charts/openshift-gitops-operator/values.yaml"
	outputRelPath      = "pkg/utils/operator_images_generated.go"

	operatorImageEnvVar = "GITOPS_OPERATOR_IMAGE"
	// The chart names the extensions image ARGOCD_ROLLOUTS_IMAGE; the addon's
	// env var for it is ARGOCD_EXTENSION_IMAGE.
	extensionImageEnvVar = "ARGOCD_EXTENSION_IMAGE"
)

// envVarAliases maps chart operatorImageEnv keys whose env-var name differs in the addon.
// ARGOCD_ROLLOUTS_IMAGE is exposed to the addon as the legacy ARGOCD_EXTENSION_IMAGE name;
// templateAndApplyChart feeds both chart keys (ARGOCD_ROLLOUTS_IMAGE and
// ROLLOUTS_EXTENSION_IMAGE) from it.
var envVarAliases = map[string]string{
	"ARGOCD_ROLLOUTS_IMAGE": extensionImageEnvVar,
}

// skippedChartKeys are chart keys deliberately not generated on their own:
// ROLLOUTS_EXTENSION_IMAGE carries the same value as ARGOCD_ROLLOUTS_IMAGE and is synthesized
// by templateAndApplyChart, so generating it separately would only add a duplicate entry.
var skippedChartKeys = map[string]bool{
	"ROLLOUTS_EXTENSION_IMAGE": true,
}

type chartImageValues struct {
	Image struct {
		Manager struct {
			Registry   string `json:"registry"`
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"manager"`
	} `json:"image"`
	OperatorImageEnv map[string]string `json:"operatorImageEnv"`
}

const boilerplate = `// Copyright Contributors to the Open Cluster Management project
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

`

func main() {
	check := flag.Bool("check", false, "fail if the generated file is out of date")
	flag.Parse()

	if err := run(*check); err != nil {
		fail(err)
	}
}

// run generates (or verifies) pkg/utils/operator_images_generated.go in the module root.
func run(check bool) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	return runAt(root, check)
}

// runAt is run() against an explicit module root, so tests never touch the working tree.
func runAt(root string, check bool) error {
	generated, err := render(filepath.Join(root, chartValuesRelPath))
	if err != nil {
		return err
	}

	outputPath := filepath.Join(root, outputRelPath)

	if check {
		// #nosec G304 -- the path is derived from the module root, not user input.
		existing, readErr := os.ReadFile(outputPath)
		if readErr != nil || string(existing) != string(generated) {
			return fmt.Errorf("operator image list is out of date with the chart values; run `go generate ./pkg/utils`")
		}
		return nil
	}

	// The file is generated source, not a secret; 0600 keeps gosec's file-permission check happy
	// and git does not record non-executable modes anyway.
	return os.WriteFile(outputPath, generated, 0o600)
}

// render builds the generated Go source from a chart values.yaml file.
func render(chartValuesPath string) ([]byte, error) {
	// #nosec G304 -- the path is derived from the module root, not user input.
	data, err := os.ReadFile(chartValuesPath)
	if err != nil {
		return nil, err
	}

	var values chartImageValues
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, err
	}

	manager := values.Image.Manager
	if manager.Repository == "" {
		return nil, fmt.Errorf("chart values has no image.manager.repository")
	}
	operatorImage := manager.Repository
	if manager.Registry != "" {
		operatorImage = manager.Registry + "/" + manager.Repository
	}
	switch {
	case manager.Tag == "":
	case strings.HasPrefix(manager.Tag, "sha256:"):
		operatorImage += "@" + manager.Tag
	default:
		operatorImage += ":" + manager.Tag
	}
	images := map[string]string{operatorImageEnvVar: operatorImage}

	// Every image the chart knows about is propagated, so a chart bump that adds a component
	// image cannot leave it unmirrored in a disconnected environment.
	chartKeys := make([]string, 0, len(values.OperatorImageEnv))
	for chartKey := range values.OperatorImageEnv {
		chartKeys = append(chartKeys, chartKey)
	}
	sort.Strings(chartKeys)

	for _, chartKey := range chartKeys {
		if skippedChartKeys[chartKey] {
			continue
		}
		value := values.OperatorImageEnv[chartKey]
		if value == "" {
			return nil, fmt.Errorf("chart values has an empty operatorImageEnv.%s", chartKey)
		}
		envKey := chartKey
		if alias, ok := envVarAliases[chartKey]; ok {
			envKey = alias
		}
		images[envKey] = value
	}

	if len(values.OperatorImageEnv) == 0 {
		return nil, fmt.Errorf("chart values has no operatorImageEnv entries")
	}

	keys := make([]string, 0, len(images))
	for k := range images {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out strings.Builder
	out.WriteString(boilerplate)
	out.WriteString("// Code generated by hack/gen-operator-images from the operator chart values; DO NOT EDIT.\n\n")
	out.WriteString("package utils\n\n")
	out.WriteString("// DefaultOperatorImages are the operator images the hub propagates to managed clusters.\n")
	out.WriteString("var DefaultOperatorImages = map[string]string{\n")
	for _, k := range keys {
		fmt.Fprintf(&out, "\t%q: %q,\n", k, images[k])
	}
	out.WriteString("}\n")

	return format.Source([]byte(out.String()))
}

// repoRoot walks up from the working directory to the module root (the directory holding go.mod),
// so the generator works both from the repository root and from pkg/utils.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found in %s or any parent directory", dir)
		}
		dir = parent
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen-operator-images:", err)
	os.Exit(1)
}
