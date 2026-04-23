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
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	skipAgentVersionHealAnnotation = "apps.open-cluster-management.io/skip-agent-version-heal"
	skipArgoCDPolicyAnnotation     = "apps.open-cluster-management.io/skip-argocd-policy"
	principalComponentLabel        = "app.kubernetes.io/component"
	principalComponentValue        = "principal"
	principalContainerName         = "principal"
	addonManagedArgoCDName         = "acm-openshift-gitops"
)

// HealAgentVersionDrift detects version drift between the hub's ArgoCD principal
// and the spoke's agent, and patches the existing ArgoCD Policy to enforce the
// correct agent image on managed clusters.
func (r *ReconcileGitOpsCluster) HealAgentVersionDrift(instance *gitopsclusterV1beta1.GitOpsCluster) error {
	klog.Infof("HealAgentVersionDrift called for %s/%s", instance.Namespace, instance.Name)

	annotations := instance.GetAnnotations()
	if annotations[skipAgentVersionHealAnnotation] == "true" {
		klog.Infof("skip-agent-version-heal annotation set, skipping drift heal for %s/%s", instance.Namespace, instance.Name)
		return nil
	}

	if instance.Spec.GitOpsAddon == nil ||
		instance.Spec.GitOpsAddon.ArgoCDAgent == nil ||
		instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled == nil ||
		!*instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled {
		klog.Infof("HealAgentVersionDrift: agent not enabled for %s/%s, skipping", instance.Namespace, instance.Name)
		return nil
	}

	argoCDNamespace := instance.Spec.ArgoServer.ArgoNamespace
	if argoCDNamespace == "" {
		argoCDNamespace = "openshift-gitops"
	}

	ctx := context.TODO()

	principalImage, err := r.findPrincipalDeploymentImage(ctx, argoCDNamespace)
	if err != nil {
		return fmt.Errorf("failed to find principal deployment: %w", err)
	}
	if principalImage == "" {
		klog.Infof("Principal deployment not found yet in %s, skipping drift heal", argoCDNamespace)
		return nil
	}

	klog.Infof("Agent version drift heal: principal image is %s for %s/%s", principalImage, instance.Namespace, instance.Name)
	return r.patchArgoCDPolicyAgentImage(instance, principalImage)
}

// findPrincipalDeploymentImage finds the ArgoCD agent principal deployment and
// returns its container image. Returns empty string if not found.
// Uses apiReader (uncached) since Deployments are not in the controller's cache.
func (r *ReconcileGitOpsCluster) findPrincipalDeploymentImage(ctx context.Context, namespace string) (string, error) {
	deployList := &appsv1.DeploymentList{}
	err := r.apiReader.List(ctx, deployList,
		client.InNamespace(namespace),
		client.MatchingLabels{principalComponentLabel: principalComponentValue},
	)
	if err != nil {
		return "", fmt.Errorf("failed to list deployments in %s: %w", namespace, err)
	}

	for _, deploy := range deployList.Items {
		if !strings.HasSuffix(deploy.Name, "-agent-principal") {
			continue
		}
		image := findContainerImage(deploy.Spec.Template.Spec.Containers, deploy.Name)
		if image == "" {
			klog.Warningf("Deployment %s/%s has no containers, skipping", namespace, deploy.Name)
			continue
		}
		klog.V(2).Infof("Found principal deployment %s/%s with image %s", namespace, deploy.Name, image)
		return image, nil
	}

	return "", nil
}

// patchArgoCDPolicyAgentImage reads the existing ArgoCD Policy, navigates the
// nested structure to find the ArgoCD object-template, and sets/updates
// spec.argoCDAgent.agent.image to match the principal's image.
// Uses RetryOnConflict to handle concurrent modifications.
func (r *ReconcileGitOpsCluster) patchArgoCDPolicyAgentImage(instance *gitopsclusterV1beta1.GitOpsCluster, principalImage string) error {
	policyName := instance.Name + "-argocd-policy"
	policyGVR := schema.GroupVersionResource{
		Group:    "policy.open-cluster-management.io",
		Version:  "v1",
		Resource: "policies",
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := r.DynamicClient.Resource(policyGVR).Namespace(instance.Namespace).Get(
			context.TODO(), policyName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				klog.V(2).Infof("ArgoCD Policy %s/%s not found, skipping agent image patch",
					instance.Namespace, policyName)
				return nil
			}
			return fmt.Errorf("failed to get ArgoCD Policy %s/%s: %w", instance.Namespace, policyName, err)
		}

		// Navigate: spec.policy-templates[*].objectDefinition.spec.object-templates[*]
		spec, ok := existing.Object["spec"].(map[string]interface{})
		if !ok {
			return fmt.Errorf("policy %s has no spec", policyName)
		}

		policyTemplates, ok := spec["policy-templates"].([]interface{})
		if !ok || len(policyTemplates) == 0 {
			return fmt.Errorf("policy %s has no policy-templates", policyName)
		}

		needsUpdate := false
		for ptIdx, pt := range policyTemplates {
			ptMap, ok := pt.(map[string]interface{})
			if !ok {
				continue
			}

			configPolicyDef, ok := ptMap["objectDefinition"].(map[string]interface{})
			if !ok {
				continue
			}

			configPolicySpec, ok := configPolicyDef["spec"].(map[string]interface{})
			if !ok {
				continue
			}

			objectTemplates, ok := configPolicySpec["object-templates"].([]interface{})
			if !ok || len(objectTemplates) == 0 {
				continue
			}

			for _, tmpl := range objectTemplates {
				tmplMap, ok := tmpl.(map[string]interface{})
				if !ok {
					continue
				}
				objDef, ok := tmplMap["objectDefinition"].(map[string]interface{})
				if !ok {
					continue
				}
				if objDef["kind"] != "ArgoCD" {
					continue
				}

				objMeta, _ := objDef["metadata"].(map[string]interface{})
				objName, _ := objMeta["name"].(string)
				if objName != addonManagedArgoCDName {
					klog.V(2).Infof("Skipping ArgoCD template %q in Policy %s policy-templates[%d] (only patching %s)",
						objName, policyName, ptIdx, addonManagedArgoCDName)
					continue
				}

				objSpec := ensureNestedMap(objDef, "spec")
				argoCDAgent := ensureNestedMap(objSpec, "argoCDAgent")
				agent := ensureNestedMap(argoCDAgent, "agent")

				currentImage, _ := agent["image"].(string)
				if currentImage == principalImage {
					klog.V(2).Infof("Agent image in Policy %s policy-templates[%d] already matches principal (%s), no patch needed",
						policyName, ptIdx, principalImage)
					continue
				}

				agent["image"] = principalImage
				klog.Infof("Patching Policy %s/%s: setting argoCDAgent.agent.image in policy-templates[%d] to %s (was: %s)",
					instance.Namespace, policyName, ptIdx, principalImage, currentImage)
				needsUpdate = true
			}
		}

		if !needsUpdate {
			klog.V(2).Infof("No ArgoCD object-template needs agent image patch in Policy %s/%s",
				instance.Namespace, policyName)
			return nil
		}

		_, err = r.DynamicClient.Resource(policyGVR).Namespace(instance.Namespace).Update(
			context.TODO(), existing, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update Policy %s/%s: %w", instance.Namespace, policyName, err)
		}

		klog.Infof("Successfully patched agent image in Policy %s/%s", instance.Namespace, policyName)
		return nil
	})
}

// ensureNestedMap ensures a key exists as a map in the parent map.
// If the key doesn't exist or is not a map, creates an empty map.
func ensureNestedMap(parent map[string]interface{}, key string) map[string]interface{} {
	if existing, ok := parent[key].(map[string]interface{}); ok {
		return existing
	}
	m := make(map[string]interface{})
	parent[key] = m
	return m
}

// principalDeploymentMapper maps principal Deployment changes to GitOpsCluster
// reconcile requests. When the principal's container image changes, all
// agent-enabled GitOpsClusters whose argoServer.argoNamespace matches the
// Deployment's namespace are enqueued for reconciliation.
type principalDeploymentMapper struct {
	client client.Client
}

func (m *principalDeploymentMapper) Map(ctx context.Context, deploy *appsv1.Deployment) []reconcile.Request {
	if !strings.HasSuffix(deploy.Name, "-agent-principal") {
		return nil
	}

	gitopsClusterList := &gitopsclusterV1beta1.GitOpsClusterList{}
	if err := m.client.List(ctx, gitopsClusterList); err != nil {
		klog.Errorf("failed to list GitOpsClusters for principal deployment mapper: %v", err)
		return nil
	}

	var requests []reconcile.Request
	for i := range gitopsClusterList.Items {
		gc := &gitopsClusterList.Items[i]
		argoCDNs := "openshift-gitops"
		if gc.Spec.ArgoServer.ArgoNamespace != "" {
			argoCDNs = gc.Spec.ArgoServer.ArgoNamespace
		}
		if argoCDNs != deploy.Namespace {
			continue
		}
		if gc.Spec.GitOpsAddon == nil || gc.Spec.GitOpsAddon.ArgoCDAgent == nil ||
			gc.Spec.GitOpsAddon.ArgoCDAgent.Enabled == nil || !*gc.Spec.GitOpsAddon.ArgoCDAgent.Enabled {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      gc.Name,
				Namespace: gc.Namespace,
			},
		})
	}

	if len(requests) > 0 {
		klog.Infof("Principal deployment %s/%s image changed, triggering reconciliation for %d GitOpsCluster(s)",
			deploy.Namespace, deploy.Name, len(requests))
	}
	return requests
}

// findContainerImage returns the image of the main application container.
// Checks in order: container named "principal" (Red Hat operator convention),
// container matching the deployment name (upstream operator convention),
// then falls back to containers[0].
func findContainerImage(containers []v1.Container, deploymentName string) string {
	for _, c := range containers {
		if c.Name == principalContainerName {
			return c.Image
		}
	}
	for _, c := range containers {
		if c.Name == deploymentName {
			return c.Image
		}
	}
	if len(containers) > 0 {
		return containers[0].Image
	}
	return ""
}

// PrincipalDeploymentPredicateFunc filters Deployment watch events to only
// trigger when a principal deployment's container image changes.
var PrincipalDeploymentPredicateFunc = predicate.TypedFuncs[*appsv1.Deployment]{
	CreateFunc: func(e event.TypedCreateEvent[*appsv1.Deployment]) bool {
		return strings.HasSuffix(e.Object.GetName(), "-agent-principal")
	},
	UpdateFunc: func(e event.TypedUpdateEvent[*appsv1.Deployment]) bool {
		if !strings.HasSuffix(e.ObjectNew.GetName(), "-agent-principal") {
			return false
		}
		name := e.ObjectNew.GetName()
		return findContainerImage(e.ObjectOld.Spec.Template.Spec.Containers, name) !=
			findContainerImage(e.ObjectNew.Spec.Template.Spec.Containers, name)
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*appsv1.Deployment]) bool {
		return false
	},
}
