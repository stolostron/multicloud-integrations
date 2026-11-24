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
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	k8syaml "sigs.k8s.io/yaml"
)

// TestStaticAddonTemplateCoversSpokeEnvVars pins the hand-maintained static template to the
// generated spoke variable list, so a chart image bump cannot leave the static path behind.
func TestStaticAddonTemplateCoversSpokeEnvVars(t *testing.T) {
	data, err := os.ReadFile("addonTemplates/addonTemplates.yaml")
	if err != nil {
		t.Fatalf("read static addon template: %v", err)
	}

	placeholders := map[string]string{}

	for _, doc := range strings.Split(string(data), "\n---") {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}

		var obj unstructured.Unstructured
		if err := k8syaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("parse static addon template document: %v", err)
		}

		collectDeploymentPlaceholders(obj.Object, placeholders)

		manifests, _, _ := unstructured.NestedSlice(obj.Object, "spec", "agentSpec", "workload", "manifests")
		for _, manifest := range manifests {
			manifestMap, ok := manifest.(map[string]interface{})
			if !ok {
				continue
			}
			collectDeploymentPlaceholders(manifestMap, placeholders)
		}
	}

	for _, name := range utils.SpokeConfigEnvVars() {
		want := "{{" + name + "}}"
		got, found := placeholders[name]
		if !found {
			t.Errorf("static addon template is missing placeholder env var %s", name)

			continue
		}
		if got != want {
			t.Errorf("static addon template env var %s = %q, want %q", name, got, want)
		}
	}
}

// collectDeploymentPlaceholders records the placeholder env vars of a Deployment manifest.
func collectDeploymentPlaceholders(manifest map[string]interface{}, placeholders map[string]string) {
	if manifest["kind"] != "Deployment" {
		return
	}

	containers, found, err := unstructured.NestedSlice(manifest, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return
	}

	for _, container := range containers {
		containerMap, ok := container.(map[string]interface{})
		if !ok {
			continue
		}

		envs, _, _ := unstructured.NestedSlice(containerMap, "env")
		for _, env := range envs {
			envMap, ok := env.(map[string]interface{})
			if !ok {
				continue
			}

			name, _ := envMap["name"].(string)
			value, _ := envMap["value"].(string)
			if strings.HasPrefix(value, "{{") && strings.HasSuffix(value, "}}") {
				placeholders[name] = value
			}
		}
	}
}
