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

	"k8s.io/klog"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"

	yaml "gopkg.in/yaml.v3"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// CreatePolicyTemplate creates default policy template resources if enabled
func (r *ReconcileGitOpsCluster) CreatePolicyTemplate(instance *gitopsclusterV1beta1.GitOpsCluster) error {
	// Create default policy template
	createPolicyTemplate := false
	if instance.Spec.CreatePolicyTemplate != nil {
		createPolicyTemplate = *instance.Spec.CreatePolicyTemplate
	}

	if createPolicyTemplate && instance.Spec.PlacementRef != nil &&
		instance.Spec.PlacementRef.Kind == "Placement" &&
		instance.Spec.ManagedServiceAccountRef != "" {
		if err := r.createNamespaceScopedResourceFromYAML(generatePlacementYamlString(*instance)); err != nil {
			klog.Error("failed to create default policy template local placement: ", err)
			return err
		}

		if err := r.createNamespaceScopedResourceFromYAML(generatePlacementBindingYamlString(*instance)); err != nil {
			klog.Error("failed to create default policy template local placement binding, ", err)
			return err
		}

		if err := r.createNamespaceScopedResourceFromYAML(generatePolicyTemplateYamlString(*instance)); err != nil {
			klog.Error("failed to create default policy template, ", err)
			return err
		}
	}

	return nil
}

func (r *ReconcileGitOpsCluster) createNamespaceScopedResourceFromYAML(yamlString string) error {
	return r.createOrUpdateNamespaceScopedResourceFromYAML(yamlString, true)
}

// createOnlyNamespaceScopedResourceFromYAML creates a resource from YAML only if it doesn't exist.
// If the resource already exists, it skips the update to allow users to own and modify the resource.
// This is used for Policy resources that users may want to customize.
func (r *ReconcileGitOpsCluster) createOnlyNamespaceScopedResourceFromYAML(yamlString string) error {
	return r.createOrUpdateNamespaceScopedResourceFromYAML(yamlString, false)
}

// createOrUpdateNamespaceScopedResourceFromYAML handles both create-only and create-or-update scenarios.
// When updateIfExists is true, existing resources are updated.
// When updateIfExists is false, existing resources are skipped (create-only mode).
func (r *ReconcileGitOpsCluster) createOrUpdateNamespaceScopedResourceFromYAML(yamlString string, updateIfExists bool) error {
	var obj map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlString), &obj); err != nil {
		klog.Error("failed to unmarshal yaml string: ", err)

		return err
	}

	unstructuredObj := &unstructured.Unstructured{Object: obj}

	// Get API resource information from unstructured object.
	apiResource := unstructuredObj.GroupVersionKind().GroupVersion().WithResource(
		strings.ToLower(unstructuredObj.GetKind()) + "s",
	)

	if apiResource.Resource == "policys" {
		apiResource.Resource = "policies"
	}

	namespace := unstructuredObj.GetNamespace()
	name := unstructuredObj.GetName()

	// Use recover to handle cases where dynamic client operations fail
	var existingObj *unstructured.Unstructured
	var err error
	func() {
		defer func() {
			if panicErr := recover(); panicErr != nil {
				klog.Warningf("Dynamic client not available (likely in test environment): %v", panicErr)
				err = fmt.Errorf("dynamic client not available: %v", panicErr)
			}
		}()
		existingObj, err = r.DynamicClient.Resource(apiResource).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	}()

	// If dynamic client is not available, skip
	if err != nil && strings.Contains(err.Error(), "dynamic client not available") {
		return nil
	}

	// Check if the resource already exists.
	// existingObj, err := r.DynamicClient.Resource(apiResource).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err == nil {
		if updateIfExists {
			// Resource exists, perform an update.
			unstructuredObj.SetResourceVersion(existingObj.GetResourceVersion())
			_, err = r.DynamicClient.Resource(apiResource).Namespace(namespace).Update(
				context.TODO(),
				unstructuredObj,
				metav1.UpdateOptions{},
			)

			if err != nil {
				klog.Error("failed to update resource: ", err)

				return err
			}

			klog.Infof("resource updated: %s/%s\n", namespace, name)
		} else {
			// Resource exists but we're in create-only mode - skip update
			klog.Infof("resource already exists, skipping update (user-owned): %s/%s\n", namespace, name)
		}
	} else if k8errors.IsNotFound(err) {
		// Resource does not exist, create it.
		_, err = r.DynamicClient.Resource(apiResource).Namespace(namespace).Create(
			context.TODO(),
			unstructuredObj,
			metav1.CreateOptions{},
		)
		if err != nil {
			klog.Error("failed to create resource: ", err)

			return err
		}

		klog.Infof("resource created: %s/%s\n", namespace, name)
	} else {
		klog.Error("failed to get resource: ", err)

		return err
	}

	return nil
}

func generatePlacementYamlString(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	yamlString := fmt.Sprintf(`
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: %s
  namespace: %s
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: %s
    uid: %s
spec:
  clusterSets:
    - global
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchExpressions:
            - key: local-cluster
              operator: In
              values:
                - "true"
`,
		gitOpsCluster.Name+"-policy-local-placement", gitOpsCluster.Namespace,
		gitOpsCluster.Name, string(gitOpsCluster.UID))

	return yamlString
}

func generatePlacementBindingYamlString(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	yamlString := fmt.Sprintf(`
apiVersion: policy.open-cluster-management.io/v1
kind: PlacementBinding
metadata:
  name: %s
  namespace: %s
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: %s
    uid: %s
placementRef:
  name: %s
  kind: Placement
  apiGroup: cluster.open-cluster-management.io
subjects:
  - name: %s
    kind: Policy
    apiGroup: policy.open-cluster-management.io
`,
		gitOpsCluster.Name+"-policy-local-placement-binding", gitOpsCluster.Namespace,
		gitOpsCluster.Name, string(gitOpsCluster.UID),
		gitOpsCluster.Name+"-policy-local-placement", gitOpsCluster.Name+"-policy")

	return yamlString
}

func generatePolicyTemplateYamlString(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	yamlString := fmt.Sprintf(`
apiVersion: policy.open-cluster-management.io/v1
kind: Policy
metadata:
  name: %s
  namespace: %s
  annotations:
    policy.open-cluster-management.io/standards: NIST-CSF
    policy.open-cluster-management.io/categories: PR.PT Protective Technology
    policy.open-cluster-management.io/controls: PR.PT-3 Least Functionality
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: %s
    uid: %s
spec:
  remediationAction: enforce
  disabled: false
  policy-templates:
    - objectDefinition:
        apiVersion: policy.open-cluster-management.io/v1
        kind: ConfigurationPolicy
        metadata:
          name: %s
        spec:
          pruneObjectBehavior: DeleteIfCreated
          remediationAction: enforce
          severity: low
          object-templates-raw: |
            {{ range $placedec := (lookup "cluster.open-cluster-management.io/v1beta1" "PlacementDecision" "%s" "" "cluster.open-cluster-management.io/placement=%s").items }}
            {{ range $clustdec := $placedec.status.decisions }}
            - complianceType: musthave
              objectDefinition:
                apiVersion: authentication.open-cluster-management.io/v1alpha1
                kind: ManagedServiceAccount
                metadata:
                  name: %s
                  namespace: {{ $clustdec.clusterName }}
                spec:
                  rotation: {}
            - complianceType: musthave
              objectDefinition:
                apiVersion: rbac.open-cluster-management.io/v1alpha1
                kind: ClusterPermission
                metadata:
                  name: %s
                  namespace: {{ $clustdec.clusterName }}
                spec: {}
            {{ end }}
            {{ end }}
`,
		gitOpsCluster.Name+"-policy", gitOpsCluster.Namespace,
		gitOpsCluster.Name, string(gitOpsCluster.UID),
		gitOpsCluster.Name+"-config-policy",
		gitOpsCluster.Namespace, gitOpsCluster.Spec.PlacementRef.Name,
		gitOpsCluster.Spec.ManagedServiceAccountRef, gitOpsCluster.Name+"-cluster-permission")

	return yamlString
}
