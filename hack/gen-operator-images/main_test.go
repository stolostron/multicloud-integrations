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

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func generatedValue(t *testing.T, generated []byte, key string) string {
	t.Helper()
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `":\s+"([^"]+)"`)
	match := re.FindSubmatch(generated)
	if match == nil {
		t.Fatalf("generated output has no entry for %s", key)
	}
	return string(match[1])
}

func TestRenderRealChartValues(t *testing.T) {
	generated, err := render(filepath.Join("..", "..", chartValuesRelPath))
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	checks := map[string]string{
		"GITOPS_OPERATOR_IMAGE":  "registry.redhat.io/openshift-gitops-1/gitops-rhel9-operator@sha256:",
		"ARGOCD_EXTENSION_IMAGE": "registry.redhat.io/openshift-gitops-1/argocd-extensions-rhel9@sha256:",
		"ARGOCD_PRINCIPAL_IMAGE": "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:",
	}
	for key, prefix := range checks {
		if got := generatedValue(t, generated, key); !strings.HasPrefix(got, prefix) {
			t.Errorf("%s = %q, want prefix %q", key, got, prefix)
		}
	}
}

func TestRenderUsesColonSeparatorForTaggedImages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.yaml")

	var b strings.Builder
	b.WriteString("image:\n  manager:\n    registry: example.com\n    repository: team/operator\n    tag: v1.2.3\n")
	b.WriteString("operatorImageEnv:\n")
	for _, chartKey := range []string{
		"ARGOCD_IMAGE",
		"ARGOCD_REPOSERVER_IMAGE",
		"ARGOCD_REDIS_IMAGE",
		"ARGOCD_REDIS_HA_IMAGE",
		"ARGOCD_REDIS_HA_PROXY_IMAGE",
		"ARGOCD_DEX_IMAGE",
		"BACKEND_IMAGE",
		"GITOPS_CONSOLE_PLUGIN_IMAGE",
		"GITOPS_CONSOLE_PLUGIN_IMAGE_PF5",
		"ARGOCD_ROLLOUTS_IMAGE",
		"ROLLOUTS_EXTENSION_IMAGE",
		"ARGO_ROLLOUTS_IMAGE",
		"MUST_GATHER_IMAGE",
		"ARGOCD_PRINCIPAL_IMAGE",
		"ARGOCD_AGENT_IMAGE",
		"ARGOCD_IMAGE_UPDATER_IMAGE",
		"PROMOTER_EXTENSION_IMAGE",
		"GITOPS_PROMOTER_IMAGE",
	} {
		b.WriteString("  " + chartKey + ": example.com/team/" + strings.ToLower(chartKey) + ":v1\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write values: %v", err)
	}

	generated, err := render(path)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := generatedValue(t, generated, "GITOPS_OPERATOR_IMAGE"); got != "example.com/team/operator:v1.2.3" {
		t.Errorf("GITOPS_OPERATOR_IMAGE = %q, want example.com/team/operator:v1.2.3", got)
	}
}

func TestRenderFailsOnMissingImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(path, []byte("image: {}\n"), 0o600); err != nil {
		t.Fatalf("write values: %v", err)
	}

	if _, err := render(path); err == nil {
		t.Fatal("expected an error for values without image.manager.repository")
	}
}

func TestRunCheckAndGenerateInTemporaryRoot(t *testing.T) {
	root := t.TempDir()
	valuesDir := filepath.Join(root, filepath.Dir(chartValuesRelPath))
	if err := os.MkdirAll(valuesDir, 0o750); err != nil {
		t.Fatalf("create values dir: %v", err)
	}
	chartValues, err := os.ReadFile(filepath.Join("..", "..", chartValuesRelPath))
	if err != nil {
		t.Fatalf("read chart values: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, chartValuesRelPath), chartValues, 0o600); err != nil {
		t.Fatalf("seed chart values: %v", err)
	}

	outputPath := filepath.Join(root, outputRelPath)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		t.Fatalf("create output dir: %v", err)
	}

	if err := runAt(root, true); err == nil {
		t.Fatal("check must fail while the generated file does not exist yet")
	}
	if err := runAt(root, false); err != nil {
		t.Fatalf("generate: %v", err)
	}

	first, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if err := runAt(root, true); err != nil {
		t.Fatalf("check should pass once generated: %v", err)
	}
	if err := runAt(root, false); err != nil {
		t.Fatalf("second generate: %v", err)
	}
	second, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated file again: %v", err)
	}
	if string(first) != string(second) {
		t.Error("regeneration changed the file content; it must be idempotent")
	}

	if err := os.WriteFile(outputPath, []byte("// stale\n"), 0o600); err != nil {
		t.Fatalf("overwrite generated file: %v", err)
	}
	if err := runAt(root, true); err == nil {
		t.Fatal("check must fail when the generated file is stale")
	}
}

func TestRepoRootFindsModuleRoot(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Errorf("repoRoot %q does not contain go.mod: %v", root, err)
	}
}
