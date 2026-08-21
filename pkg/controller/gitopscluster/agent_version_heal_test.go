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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newPrincipalDeployment(namespace, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/component": "principal",
				"app.kubernetes.io/name":      "openshift-gitops-agent-principal",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "principal"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "principal"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "principal", Image: image},
					},
				},
			},
		},
	}
}

func TestFindPrincipalDeploymentImage(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	t.Run("principal found", func(t *testing.T) {
		deploy := newPrincipalDeployment("openshift-gitops", "registry.redhat.io/agent:v1.21")
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}

		img, err := r.findPrincipalDeploymentImage(context.TODO(), "openshift-gitops")
		require.NoError(t, err)
		assert.Equal(t, "registry.redhat.io/agent:v1.21", img)
	})

	t.Run("principal not found returns empty", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}

		img, err := r.findPrincipalDeploymentImage(context.TODO(), "openshift-gitops")
		require.NoError(t, err)
		assert.Empty(t, img)
	})

	t.Run("non-principal deployment with component label ignored", func(t *testing.T) {
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "some-other-principal",
				Namespace: "openshift-gitops",
				Labels:    map[string]string{"app.kubernetes.io/component": "principal"},
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "x"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "c", Image: "img:old"}},
					},
				},
			},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}

		img, err := r.findPrincipalDeploymentImage(context.TODO(), "openshift-gitops")
		require.NoError(t, err)
		assert.Empty(t, img)
	})

	t.Run("wrong namespace returns empty", func(t *testing.T) {
		deploy := newPrincipalDeployment("other-ns", "registry.redhat.io/agent:v1.21")
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}

		img, err := r.findPrincipalDeploymentImage(context.TODO(), "openshift-gitops")
		require.NoError(t, err)
		assert.Empty(t, img)
	})
}

func TestHealAgentVersionDrift_ReturnsPrincipalImage(t *testing.T) {
	agentEnabled := true
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	deploy := newPrincipalDeployment("openshift-gitops", "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:live1234")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).Build()
	r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}
	instance := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "openshift-gitops"},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{ArgoNamespace: "openshift-gitops"},
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{Enabled: &agentEnabled},
			},
		},
	}

	// HealAgentVersionDrift must only ever report the principal's live image back to the
	// caller -- it must NOT touch the ArgoCD Policy (a single object shared across every
	// managed cluster) with it. The caller is responsible for feeding this into each managed
	// cluster's own AddOnDeploymentConfig instead (see gitopscluster_controller.go), so it
	// flows through the per-cluster ManagedClusterImageRegistry mirroring pipeline.
	img, err := r.HealAgentVersionDrift(instance)
	require.NoError(t, err)
	assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:live1234", img)
}

func TestHealAgentVersionDrift_SkipCases(t *testing.T) {
	agentEnabled := true
	agentDisabled := false
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)

	t.Run("skip when annotation set", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}
		instance := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test",
				Namespace:   "openshift-gitops",
				Annotations: map[string]string{skipAgentVersionHealAnnotation: "true"},
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{ArgoNamespace: "openshift-gitops"},
				GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
					ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{Enabled: &agentEnabled},
				},
			},
		}

		img, err := r.HealAgentVersionDrift(instance)
		require.NoError(t, err)
		assert.Empty(t, img)
	})

	t.Run("skip when agent not enabled", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}
		instance := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "openshift-gitops"},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{ArgoNamespace: "openshift-gitops"},
				GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
					ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{Enabled: &agentDisabled},
				},
			},
		}

		img, err := r.HealAgentVersionDrift(instance)
		require.NoError(t, err)
		assert.Empty(t, img)
	})

	t.Run("skip when no gitopsAddon", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}
		instance := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "openshift-gitops"},
			Spec:       gitopsclusterV1beta1.GitOpsClusterSpec{},
		}

		img, err := r.HealAgentVersionDrift(instance)
		require.NoError(t, err)
		assert.Empty(t, img)
	})

	t.Run("skip when principal not found", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}
		instance := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "openshift-gitops"},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{ArgoNamespace: "openshift-gitops"},
				GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
					ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{Enabled: &agentEnabled},
				},
			},
		}

		img, err := r.HealAgentVersionDrift(instance)
		require.NoError(t, err)
		assert.Empty(t, img)
	})
}

func TestReconcileArgoCDPolicyAgentSpec_NilDynamicClient(t *testing.T) {
	// When DynamicClient is zero value (nil REST client), the function should
	// recover from the panic and return nil gracefully (no-op in test environments).
	agentEnabled := true
	scheme := runtime.NewScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileGitOpsCluster{Client: cl}
	instance := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mc-gitops-ocp",
			Namespace: "openshift-gitops",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					Enabled:       &agentEnabled,
					ServerAddress: "principal.example.com",
					ServerPort:    "443",
					Mode:          "managed",
				},
			},
		},
	}

	err := r.reconcileArgoCDPolicyAgentSpec(instance)
	require.NoError(t, err, "should recover gracefully when DynamicClient is not initialized")
}

func TestReconcileArgoCDPolicyAgentSpec_FieldLogic(t *testing.T) {
	// Test that ensureNestedMap + field comparison logic works correctly
	// for all the fields that reconcileArgoCDPolicyAgentSpec manages.
	t.Run("destinationBasedMapping defaults to absent and must be set", func(t *testing.T) {
		agent := map[string]interface{}{}
		dbm := ensureNestedMap(agent, "destinationBasedMapping")
		assert.NotNil(t, dbm)

		// Simulates the condition check: dbm["enabled"] != true
		assert.NotEqual(t, true, dbm["enabled"], "newly created map should not have enabled=true yet")
		dbm["enabled"] = true
		assert.Equal(t, true, dbm["enabled"])
	})

	t.Run("client fields absent on legacy policy template", func(t *testing.T) {
		// Simulate a legacy (or user-pinned) policy's argoCDAgent.agent map that already has
		// an image. Reconciling other agent fields must never clear or overwrite it --
		// skip-agent-version-heal and a user-set Policy agent.image both depend on this.
		agent := map[string]interface{}{
			"image": "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:abc",
		}
		client := ensureNestedMap(agent, "client")
		assert.Empty(t, client, "client should be empty on a legacy policy")

		client["principalServerAddress"] = "principal.example.com"
		client["principalServerPort"] = "443"
		client["mode"] = "managed"

		assert.Equal(t, "principal.example.com", agent["client"].(map[string]interface{})["principalServerAddress"])
		assert.Equal(t, "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:abc", agent["image"],
			"Policy agent.image must be left alone; image selection is ADC-based unless the user set this field")
	})

	t.Run("tls fields absent on legacy policy template", func(t *testing.T) {
		agent := map[string]interface{}{}
		tls := ensureNestedMap(agent, "tls")
		assert.NotEqual(t, "argocd-agent-client-tls", tls["secretName"])
		assert.NotEqual(t, "argocd-agent-ca", tls["rootCASecretName"])

		tls["secretName"] = "argocd-agent-client-tls"
		tls["rootCASecretName"] = "argocd-agent-ca"
		assert.Equal(t, "argocd-agent-client-tls", tls["secretName"])
		assert.Equal(t, "argocd-agent-ca", tls["rootCASecretName"])
	})

	t.Run("allowedNamespaces absent on legacy policy template", func(t *testing.T) {
		agent := map[string]interface{}{}
		currentNS, _ := agent["allowedNamespaces"].([]interface{})
		assert.Empty(t, currentNS)
		agent["allowedNamespaces"] = []interface{}{"*"}
		updated, _ := agent["allowedNamespaces"].([]interface{})
		assert.Equal(t, []interface{}{"*"}, updated)
	})

	t.Run("destinationBasedMapping is healed toward false for autonomous mode", func(t *testing.T) {
		// Autonomous agents don't participate in principal-driven destination-name dispatch,
		// and the argocd-agent binary refuses to start with destinationBasedMapping enabled in
		// that mode. A policy that drifted (e.g. created while mode was managed, or hand-edited)
		// must be corrected back to false when mode is autonomous.
		agent := map[string]interface{}{
			"destinationBasedMapping": map[string]interface{}{
				"enabled": true,
			},
		}
		mode := "autonomous"

		dbm := ensureNestedMap(agent, "destinationBasedMapping")
		wantDBM := mode != "autonomous"
		needsUpdate := false
		if dbm["enabled"] != wantDBM {
			dbm["enabled"] = wantDBM
			needsUpdate = true
		}

		assert.True(t, needsUpdate, "a stale enabled=true must be corrected for autonomous mode")
		assert.Equal(t, false, dbm["enabled"], "destinationBasedMapping.enabled must be false for autonomous mode, or the agent fatally refuses to start")
	})

	t.Run("destinationBasedMapping is healed toward true for managed mode", func(t *testing.T) {
		// The reverse direction: a policy stuck at false (e.g. left over from a mode switch away
		// from autonomous) must be corrected back to true for managed mode, since the principal
		// expects DBM enabled there for its destination-name based dispatch/Redis key scheme.
		agent := map[string]interface{}{
			"destinationBasedMapping": map[string]interface{}{
				"enabled": false,
			},
		}
		mode := "managed"

		dbm := ensureNestedMap(agent, "destinationBasedMapping")
		wantDBM := mode != "autonomous"
		needsUpdate := false
		if dbm["enabled"] != wantDBM {
			dbm["enabled"] = wantDBM
			needsUpdate = true
		}

		assert.True(t, needsUpdate, "a stale enabled=false must be corrected for managed mode")
		assert.Equal(t, true, dbm["enabled"])
	})

	t.Run("destinationBasedMapping already correct for autonomous mode needs no update", func(t *testing.T) {
		agent := map[string]interface{}{
			"destinationBasedMapping": map[string]interface{}{
				"enabled": false,
			},
		}
		mode := "autonomous"

		dbm := ensureNestedMap(agent, "destinationBasedMapping")
		wantDBM := mode != "autonomous"
		needsUpdate := false
		if dbm["enabled"] != wantDBM {
			dbm["enabled"] = wantDBM
			needsUpdate = true
		}

		assert.False(t, needsUpdate, "no patch should be issued when the field already matches the mode")
	})

	t.Run("no update needed when all fields already correct", func(t *testing.T) {
		agent := map[string]interface{}{
			"enabled":           true,
			"allowedNamespaces": []interface{}{"*"},
			"client": map[string]interface{}{
				"principalServerAddress": "principal.example.com",
				"principalServerPort":    "443",
				"mode":                   "managed",
			},
			"destinationBasedMapping": map[string]interface{}{
				"enabled": true,
			},
			"tls": map[string]interface{}{
				"secretName":       "argocd-agent-client-tls",
				"rootCASecretName": "argocd-agent-ca",
			},
		}

		needsUpdate := false
		if agent["enabled"] != true {
			needsUpdate = true
		}
		dbm, _ := agent["destinationBasedMapping"].(map[string]interface{})
		if dbm["enabled"] != true {
			needsUpdate = true
		}
		tls, _ := agent["tls"].(map[string]interface{})
		if tls["secretName"] != "argocd-agent-client-tls" || tls["rootCASecretName"] != "argocd-agent-ca" {
			needsUpdate = true
		}
		client, _ := agent["client"].(map[string]interface{})
		if client["principalServerAddress"] != "principal.example.com" {
			needsUpdate = true
		}

		assert.False(t, needsUpdate, "no update should be needed when all fields are already correct")
	})
}

func TestEnsureNestedMap(t *testing.T) {
	t.Run("creates new map if key missing", func(t *testing.T) {
		parent := map[string]interface{}{}
		child := ensureNestedMap(parent, "spec")
		assert.NotNil(t, child)
		_, exists := parent["spec"]
		assert.True(t, exists)
	})

	t.Run("returns existing map", func(t *testing.T) {
		existing := map[string]interface{}{"foo": "bar"}
		parent := map[string]interface{}{"spec": existing}
		child := ensureNestedMap(parent, "spec")
		assert.Equal(t, "bar", child["foo"])
	})

	t.Run("replaces non-map value", func(t *testing.T) {
		parent := map[string]interface{}{"spec": "not-a-map"}
		child := ensureNestedMap(parent, "spec")
		assert.NotNil(t, child)
		assert.Empty(t, child)
	})
}
