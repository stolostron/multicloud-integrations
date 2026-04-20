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

		err := r.HealAgentVersionDrift(instance)
		require.NoError(t, err)
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

		err := r.HealAgentVersionDrift(instance)
		require.NoError(t, err)
	})

	t.Run("skip when no gitopsAddon", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &ReconcileGitOpsCluster{Client: cl, apiReader: cl}
		instance := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "openshift-gitops"},
			Spec:       gitopsclusterV1beta1.GitOpsClusterSpec{},
		}

		err := r.HealAgentVersionDrift(instance)
		require.NoError(t, err)
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

		err := r.HealAgentVersionDrift(instance)
		require.NoError(t, err)
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
