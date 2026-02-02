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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestIsOLMSubscriptionEnabled(t *testing.T) {
	tests := []struct {
		name           string
		gitOpsCluster  *gitopsclusterV1beta1.GitOpsCluster
		expectedResult bool
	}{
		{
			name: "OLM subscription enabled with gitopsAddon enabled",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolPtr(true),
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled: boolPtr(true),
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "OLM subscription enabled but gitopsAddon disabled",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolPtr(false),
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled: boolPtr(true),
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "OLM subscription disabled",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolPtr(true),
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled: boolPtr(false),
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "OLM subscription nil",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolPtr(true),
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "GitOpsAddon nil",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{},
			},
			expectedResult: false,
		},
		{
			name: "GitOpsAddon enabled nil",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled: boolPtr(true),
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "OLM subscription enabled nil",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled:         boolPtr(true),
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{},
					},
				},
			},
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsOLMSubscriptionEnabled(tt.gitOpsCluster)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestGetOLMSubscriptionValues(t *testing.T) {
	tests := []struct {
		name                        string
		olmSpec                     *gitopsclusterV1beta1.OLMSubscriptionSpec
		expectedName                string
		expectedNamespace           string
		expectedChannel             string
		expectedSource              string
		expectedSourceNamespace     string
		expectedInstallPlanApproval string
	}{
		{
			name:                        "nil spec returns defaults",
			olmSpec:                     nil,
			expectedName:                DefaultOLMSubscriptionName,
			expectedNamespace:           DefaultOLMSubscriptionNamespace,
			expectedChannel:             DefaultOLMSubscriptionChannel,
			expectedSource:              DefaultOLMSubscriptionSource,
			expectedSourceNamespace:     DefaultOLMSubscriptionSourceNamespace,
			expectedInstallPlanApproval: DefaultOLMInstallPlanApproval,
		},
		{
			name:                        "empty spec returns defaults",
			olmSpec:                     &gitopsclusterV1beta1.OLMSubscriptionSpec{},
			expectedName:                DefaultOLMSubscriptionName,
			expectedNamespace:           DefaultOLMSubscriptionNamespace,
			expectedChannel:             DefaultOLMSubscriptionChannel,
			expectedSource:              DefaultOLMSubscriptionSource,
			expectedSourceNamespace:     DefaultOLMSubscriptionSourceNamespace,
			expectedInstallPlanApproval: DefaultOLMInstallPlanApproval,
		},
		{
			name: "custom values override defaults",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				Name:                "custom-gitops-operator",
				Namespace:           "custom-operators",
				Channel:             "latest",
				Source:              "custom-catalog",
				SourceNamespace:     "custom-marketplace",
				InstallPlanApproval: "Manual",
			},
			expectedName:                "custom-gitops-operator",
			expectedNamespace:           "custom-operators",
			expectedChannel:             "latest",
			expectedSource:              "custom-catalog",
			expectedSourceNamespace:     "custom-marketplace",
			expectedInstallPlanApproval: "Manual",
		},
		{
			name: "partial custom values",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				Channel: "latest",
			},
			expectedName:                DefaultOLMSubscriptionName,
			expectedNamespace:           DefaultOLMSubscriptionNamespace,
			expectedChannel:             "latest",
			expectedSource:              DefaultOLMSubscriptionSource,
			expectedSourceNamespace:     DefaultOLMSubscriptionSourceNamespace,
			expectedInstallPlanApproval: DefaultOLMInstallPlanApproval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, namespace, channel, source, sourceNamespace, installPlanApproval := GetOLMSubscriptionValues(tt.olmSpec)
			assert.Equal(t, tt.expectedName, name)
			assert.Equal(t, tt.expectedNamespace, namespace)
			assert.Equal(t, tt.expectedChannel, channel)
			assert.Equal(t, tt.expectedSource, source)
			assert.Equal(t, tt.expectedSourceNamespace, sourceNamespace)
			assert.Equal(t, tt.expectedInstallPlanApproval, installPlanApproval)
		})
	}
}

func TestGetOLMAddOnTemplateName(t *testing.T) {
	tests := []struct {
		name          string
		gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster
		expected      string
	}{
		{
			name: "standard naming",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-cluster",
					Namespace: utils.GitOpsNamespace,
				},
			},
			expected: "gitops-addon-olm-openshift-gitops-my-cluster",
		},
		{
			name: "different namespace",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "argocd",
				},
			},
			expected: "gitops-addon-olm-argocd-test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getOLMAddOnTemplateName(tt.gitOpsCluster)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEnsureOLMAddOnTemplate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = addonv1alpha1.AddToScheme(scheme)
	_ = gitopsclusterV1beta1.SchemeBuilder.AddToScheme(scheme)

	tests := []struct {
		name             string
		gitOpsCluster    *gitopsclusterV1beta1.GitOpsCluster
		existingTemplate *addonv1alpha1.AddOnTemplate
		expectCreate     bool
		expectUpdate     bool
		expectError      bool
	}{
		{
			name: "create new OLM AddOnTemplate with defaults",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gitopscluster",
					Namespace: utils.GitOpsNamespace,
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolPtr(true),
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled: boolPtr(true),
						},
					},
				},
			},
			existingTemplate: nil,
			expectCreate:     true,
			expectUpdate:     false,
			expectError:      false,
		},
		{
			name: "create new OLM AddOnTemplate with custom values",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-cluster",
					Namespace: "argocd",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolPtr(true),
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled:   boolPtr(true),
							Name:      "custom-gitops",
							Namespace: "custom-ns",
							Channel:   "latest",
						},
					},
				},
			},
			existingTemplate: nil,
			expectCreate:     true,
			expectUpdate:     false,
			expectError:      false,
		},
		{
			name: "update existing OLM AddOnTemplate",
			gitOpsCluster: &gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gitopscluster",
					Namespace: utils.GitOpsNamespace,
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
						Enabled: boolPtr(true),
						OLMSubscription: &gitopsclusterV1beta1.OLMSubscriptionSpec{
							Enabled: boolPtr(true),
							Channel: "stable",
						},
					},
				},
			},
			existingTemplate: &addonv1alpha1.AddOnTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gitops-addon-olm-openshift-gitops-gitopscluster",
				},
				Spec: addonv1alpha1.AddOnTemplateSpec{
					AddonName: "gitops-addon",
				},
			},
			expectCreate: false,
			expectUpdate: true,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []runtime.Object{}
			if tt.existingTemplate != nil {
				objs = append(objs, tt.existingTemplate)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			r := &ReconcileGitOpsCluster{
				Client: fakeClient,
				scheme: scheme,
			}

			err := r.EnsureOLMAddOnTemplate(tt.gitOpsCluster)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify the template was created/updated
				templateName := getOLMAddOnTemplateName(tt.gitOpsCluster)
				template := &addonv1alpha1.AddOnTemplate{}
				err = fakeClient.Get(context.Background(), types.NamespacedName{Name: templateName}, template)
				assert.NoError(t, err)

				// Verify template has correct labels
				assert.Equal(t, "multicloud-integrations", template.Labels["app.kubernetes.io/managed-by"])
				assert.Equal(t, "addon-template-olm", template.Labels["app.kubernetes.io/component"])
				assert.Equal(t, tt.gitOpsCluster.Name, template.Labels["gitopscluster.apps.open-cluster-management.io/name"])
				assert.Equal(t, tt.gitOpsCluster.Namespace, template.Labels["gitopscluster.apps.open-cluster-management.io/namespace"])

				// Verify addon name
				assert.Equal(t, "gitops-addon", template.Spec.AddonName)

				// Verify manifests contain the subscription
				assert.Len(t, template.Spec.AgentSpec.Workload.Manifests, 1)
			}
		})
	}
}

func TestBuildOLMSubscriptionManifests(t *testing.T) {
	manifests := buildOLMSubscriptionManifests(
		"openshift-gitops-operator",
		"openshift-operators",
		"stable",
		"redhat-operators",
		"openshift-marketplace",
		"Automatic",
	)

	assert.Len(t, manifests, 1)
	assert.NotNil(t, manifests[0].Raw)
}

func TestHasCustomOLMSubscriptionValues(t *testing.T) {
	tests := []struct {
		name     string
		olmSpec  *gitopsclusterV1beta1.OLMSubscriptionSpec
		expected bool
	}{
		{
			name:     "nil spec returns false",
			olmSpec:  nil,
			expected: false,
		},
		{
			name:     "empty spec returns false",
			olmSpec:  &gitopsclusterV1beta1.OLMSubscriptionSpec{},
			expected: false,
		},
		{
			name: "only enabled set returns false (enabled is not a custom value)",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				Enabled: boolPtr(true),
			},
			expected: false,
		},
		{
			name: "name set returns true",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				Name: "argocd-operator",
			},
			expected: true,
		},
		{
			name: "namespace set returns true",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				Namespace: "operators",
			},
			expected: true,
		},
		{
			name: "channel set returns true",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				Channel: "alpha",
			},
			expected: true,
		},
		{
			name: "source set returns true",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				Source: "operatorhubio-catalog",
			},
			expected: true,
		},
		{
			name: "sourceNamespace set returns true",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				SourceNamespace: "olm",
			},
			expected: true,
		},
		{
			name: "installPlanApproval set returns true",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				InstallPlanApproval: "Manual",
			},
			expected: true,
		},
		{
			name: "all custom values set returns true",
			olmSpec: &gitopsclusterV1beta1.OLMSubscriptionSpec{
				Name:                "argocd-operator",
				Namespace:           "operators",
				Channel:             "alpha",
				Source:              "operatorhubio-catalog",
				SourceNamespace:     "olm",
				InstallPlanApproval: "Manual",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasCustomOLMSubscriptionValues(tt.olmSpec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

