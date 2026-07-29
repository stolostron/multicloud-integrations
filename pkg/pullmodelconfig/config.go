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

// Package pullmodelconfig loads the multicluster-integrations-config ConfigMap read by the
// propagation, gitopssyncresc, and multiclusterstatusaggregation binaries at startup. It is
// deliberately independent of pkg/controller/gitopscluster (the argocd-agent controller) --
// nothing here is imported by, or imports, that package.
package pullmodelconfig

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	// ConfigMapName is the fixed name of the shared config ConfigMap. It always lives in
	// whatever namespace the reading pod itself is running in -- see ResolveNamespace --
	// never a hardcoded namespace, since that can differ across installs.
	ConfigMapName = "multicluster-integrations-config"

	// ConfigMapDataKey is the single data key holding the YAML-serialized ControllerConfig.
	// A single structured blob (rather than flat ConfigMap data entries) so future settings
	// that need nesting or lists don't require a schema rework.
	ConfigMapDataKey = "config.yaml"

	podNamespaceEnvVar          = "POD_NAMESPACE"
	serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// ControllerConfig is the top-level shape of the multicluster-integrations-config ConfigMap.
// Unknown fields are ignored on read (forward compatible with future additions); fields
// absent from an older or hand-edited ConfigMap default to their Go zero value (backward
// compatible -- an empty or partial ConfigMap is never an error).
type ControllerConfig struct {
	PullModel PullModelConfig `json:"pullModel,omitempty"`
	// Future sibling sections go here, e.g.:
	// SomeOtherFeature SomeOtherFeatureConfig `json:"someOtherFeature,omitempty"`
}

// PullModelConfig groups settings for the different pull-model delivery mechanisms this
// repo supports. Named as a section (not a bare top-level bool) specifically so it reads
// unambiguously next to argocd-agent, which is also pull-based in its own agent-to-principal
// communication but is a completely different mechanism from what's configured here.
type PullModelConfig struct {
	// Basic controls the classic ManifestWork-based pull model (propagation-controller,
	// gitopssyncresc, multiclusterstatusaggregation).
	Basic BasicPullModelConfig `json:"basic,omitempty"`
	// Future sibling, if the opt-in Maestro-backed variant ever needs the same switch:
	// Maestro MaestroPullModelConfig `json:"maestro,omitempty"`
}

// BasicPullModelConfig controls the classic ManifestWork-based pull model.
type BasicPullModelConfig struct {
	// Disabled stops all three basic-pull-model components from doing their normal work:
	// propagation stops generating/updating ManifestWork (and performs a one-time sweep of
	// existing ManifestWork to ReadOnly on startup while this is true), gitopssyncresc stops
	// polling the search API, and multiclusterstatusaggregation stops building
	// MulticlusterApplicationSetReports. The processes keep running either way -- this does
	// not scale anything down, it just makes their reconcile/sync loops no-op.
	Disabled bool `json:"disabled,omitempty"`
}

// ResolveNamespace returns the namespace the current process is running in, without ever
// assuming a hardcoded value (the multicluster-integrations deployment's namespace is not
// guaranteed to be the same across every install). It checks the POD_NAMESPACE env var
// first -- already set on these containers today via the Downward API -- and falls back to
// the namespace file Kubernetes automatically mounts into every pod that has a service
// account, which requires no Deployment change at all.
func ResolveNamespace() (string, error) {
	if ns := os.Getenv(podNamespaceEnvVar); ns != "" {
		return ns, nil
	}

	data, err := os.ReadFile(serviceAccountNamespaceFile)
	if err != nil {
		return "", fmt.Errorf("unable to resolve current namespace: %s is not set and %s could not be read: %w",
			podNamespaceEnvVar, serviceAccountNamespaceFile, err)
	}

	return string(data), nil
}

// LoadOrCreate reads the multicluster-integrations-config ConfigMap from the current pod's
// own namespace (see ResolveNamespace). If it does not exist, it is created with all
// defaults (pullModel.basic.disabled: false) so a fresh install never needs to "find" a
// config that was never provisioned -- whichever of the three sibling binaries starts first
// plants it. Safe to call concurrently from multiple sibling containers in the same pod: if
// two processes race to create it, the loser's Create fails with AlreadyExists and it simply
// re-Gets whatever the winner wrote, rather than erroring out.
func LoadOrCreate(ctx context.Context, c client.Client) (*ControllerConfig, error) {
	ns, err := ResolveNamespace()
	if err != nil {
		return nil, err
	}

	cm := &corev1.ConfigMap{}
	err = c.Get(ctx, types.NamespacedName{Name: ConfigMapName, Namespace: ns}, cm)

	switch {
	case err == nil:
		return parse(cm)
	case apierrors.IsNotFound(err):
		return createDefault(ctx, c, ns)
	default:
		return nil, err
	}
}

func createDefault(ctx context.Context, c client.Client, ns string) (*ControllerConfig, error) {
	defaultCfg := &ControllerConfig{}

	raw, err := yaml.Marshal(defaultCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal default pull model config: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: ns,
		},
		Data: map[string]string{
			ConfigMapDataKey: string(raw),
		},
	}

	if err := c.Create(ctx, cm); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing := &corev1.ConfigMap{}
			if getErr := c.Get(ctx, types.NamespacedName{Name: ConfigMapName, Namespace: ns}, existing); getErr != nil {
				return nil, fmt.Errorf("lost create race for %s/%s but could not re-read it: %w", ns, ConfigMapName, getErr)
			}

			return parse(existing)
		}

		return nil, fmt.Errorf("failed to create %s/%s: %w", ns, ConfigMapName, err)
	}

	return defaultCfg, nil
}

func parse(cm *corev1.ConfigMap) (*ControllerConfig, error) {
	cfg := &ControllerConfig{}

	raw, ok := cm.Data[ConfigMapDataKey]
	if !ok {
		// Key missing from an otherwise-present ConfigMap -- treat as all-defaults
		// rather than an error, same backward-compatible spirit as the ConfigMap
		// being absent entirely.
		return cfg, nil
	}

	if err := yaml.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s/%s data[%s]: %w", cm.Namespace, cm.Name, ConfigMapDataKey, err)
	}

	return cfg, nil
}
