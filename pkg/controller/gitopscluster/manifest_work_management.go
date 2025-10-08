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
	"encoding/json"
	"fmt"

	v1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	workv1 "open-cluster-management.io/api/work/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
)

// CreateArgoCDAgentManifestWorks creates ManifestWorks for ArgoCD agent CA certificates on managed clusters
func (r *ReconcileGitOpsCluster) CreateArgoCDAgentManifestWorks(
	gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster) error {
	// Hub namespace - where we read the CA cert from
	hubNamespace := gitOpsCluster.Spec.ArgoServer.ArgoNamespace
	if hubNamespace == "" {
		hubNamespace = "openshift-gitops"
	}

	// Managed cluster namespace - where the secret will be created on managed clusters
	managedNamespace := ""
	if gitOpsCluster.Spec.GitOpsAddon != nil && gitOpsCluster.Spec.GitOpsAddon.GitOpsNamespace != "" {
		managedNamespace = gitOpsCluster.Spec.GitOpsAddon.GitOpsNamespace
	}
	if managedNamespace == "" {
		managedNamespace = "openshift-gitops"
	}

	// Get the CA certificate from the argocd-agent-ca secret on the hub
	caCert, err := r.getArgoCDAgentCACert(hubNamespace)
	if err != nil {
		return fmt.Errorf("failed to get ArgoCD agent CA certificate: %w", err)
	}

	for _, managedCluster := range managedClusters {
		// Skip local-cluster - don't create ManifestWork for local cluster
		if IsLocalCluster(managedCluster) {
			klog.Infof("skipping ManifestWork creation for local-cluster: %s", managedCluster.Name)
			continue
		}

		manifestWork := r.createArgoCDAgentManifestWork(managedCluster.Name, managedNamespace, caCert)

		// Check if ManifestWork already exists
		existingMW := &workv1.ManifestWork{}
		err := r.Get(context.TODO(), types.NamespacedName{
			Name:      manifestWork.Name,
			Namespace: manifestWork.Namespace,
		}, existingMW)

		if err != nil {
			if k8errors.IsNotFound(err) {
				// Create new ManifestWork with proper annotations
				if manifestWork.Annotations == nil {
					manifestWork.Annotations = make(map[string]string)
				}
				manifestWork.Annotations[ArgoCDAgentPropagateCAAnnotation] = "true"

				err = r.Create(context.TODO(), manifestWork)
				if err != nil {
					klog.Errorf("failed to create ManifestWork %s/%s: %v", manifestWork.Namespace, manifestWork.Name, err)
					return err
				}

				klog.Infof("created ManifestWork %s/%s for ArgoCD agent CA", manifestWork.Namespace, manifestWork.Name)
			} else {
				return fmt.Errorf("failed to get ManifestWork %s/%s: %w", manifestWork.Namespace, manifestWork.Name, err)
			}
		} else {
			// ManifestWork exists - check if it needs updating
			needsUpdate := false

			// Check if ManifestWork was previously marked as outdated
			isOutdated := existingMW.Annotations != nil && existingMW.Annotations[ArgoCDAgentOutdatedAnnotation] == "true"
			previousPropagateCA := existingMW.Annotations != nil && existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] == "true"

			// Check if certificate data has changed
			certificateChanged := false

			if len(existingMW.Spec.Workload.Manifests) > 0 {
				// Extract the existing certificate data from the ManifestWork
				existingManifest := existingMW.Spec.Workload.Manifests[0]

				if existingManifest.RawExtension.Raw != nil {
					existingSecret := &v1.Secret{}
					err := json.Unmarshal(existingManifest.RawExtension.Raw, existingSecret)

					if err == nil {
						existingCert := string(existingSecret.Data["ca.crt"])

						if existingCert != caCert {
							certificateChanged = true

							klog.Infof("Certificate data changed for ManifestWork %s/%s", existingMW.Namespace, existingMW.Name)
						}
					}
				}
			}

			// Update if:
			// 1. ManifestWork was marked as outdated (false->true transition)
			// 2. Spec has changed
			// 3. Certificate data has changed
			if isOutdated || !previousPropagateCA || certificateChanged {
				needsUpdate = true

				klog.Infof("ManifestWork %s/%s needs update (outdated: %v, previousPropagateCA: %v, certificateChanged: %v)",
					existingMW.Namespace, existingMW.Name, isOutdated, previousPropagateCA, certificateChanged)
			}

			if needsUpdate {
				// Update the ManifestWork spec
				existingMW.Spec = manifestWork.Spec

				// Update annotations to reflect current state
				if existingMW.Annotations == nil {
					existingMW.Annotations = make(map[string]string)
				}

				delete(existingMW.Annotations, ArgoCDAgentOutdatedAnnotation)     // Remove outdated marker
				existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] = "true" // Mark as propagating

				err = r.Update(context.TODO(), existingMW)
				if err != nil {
					klog.Errorf("failed to update ManifestWork %s/%s: %v", existingMW.Namespace, existingMW.Name, err)
					return err
				}

				klog.Infof("updated ManifestWork %s/%s for ArgoCD agent CA", existingMW.Namespace, existingMW.Name)
			} else {
				// Ensure annotations are set correctly even if no update is needed
				if existingMW.Annotations == nil {
					existingMW.Annotations = make(map[string]string)
				}

				if existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] != "true" {
					existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] = "true"

					err = r.Update(context.TODO(), existingMW)
					if err != nil {
						klog.Errorf("failed to update ManifestWork annotations %s/%s: %v", existingMW.Namespace, existingMW.Name, err)
						return err
					}
				}

				klog.V(2).Infof("ManifestWork %s/%s already up to date", existingMW.Namespace, existingMW.Name)
			}
		}
	}

	return nil
}

// getArgoCDAgentCACert retrieves the CA certificate from the argocd-agent-ca secret
func (r *ReconcileGitOpsCluster) getArgoCDAgentCACert(argoNamespace string) (string, error) {
	secret := &v1.Secret{}
	err := r.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: argoNamespace,
	}, secret)

	if err != nil {
		return "", fmt.Errorf("failed to get argocd-agent-ca secret: %w", err)
	}

	// Try to get the certificate data from either tls.crt or ca.crt field
	var caCertBytes []byte

	var exists bool

	// First try tls.crt (expected from ensureArgoCDAgentCASecret)
	caCertBytes, exists = secret.Data["tls.crt"]
	if !exists {
		// Fallback to ca.crt (in case secret was created by ManifestWork)
		caCertBytes, exists = secret.Data["ca.crt"]
		if !exists {
			return "", fmt.Errorf("neither tls.crt nor ca.crt found in argocd-agent-ca secret")
		}
	}

	// Return the certificate data as-is (it's already in the correct format)
	return string(caCertBytes), nil
}

// createArgoCDAgentManifestWork creates a ManifestWork for deploying the ArgoCD agent CA secret
func (r *ReconcileGitOpsCluster) createArgoCDAgentManifestWork(clusterName, argoNamespace, caCert string) *workv1.ManifestWork {
	// Create the secret manifest
	secretManifest := &v1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: argoNamespace,
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca.crt": []byte(caCert),
		},
	}

	// Marshal the secret to JSON for the RawExtension
	secretJSON, err := json.Marshal(secretManifest)
	if err != nil {
		// This should not happen in normal operation
		panic(fmt.Sprintf("failed to marshal secret manifest: %v", err))
	}

	// Create the ManifestWork
	manifestWork := &workv1.ManifestWork{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "work.open-cluster-management.io/v1",
			Kind:       "ManifestWork",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca-mw",
			Namespace: clusterName,
		},
		Spec: workv1.ManifestWorkSpec{
			Workload: workv1.ManifestsTemplate{
				Manifests: []workv1.Manifest{
					{
						RawExtension: runtime.RawExtension{
							Raw:    secretJSON,
							Object: secretManifest,
						},
					},
				},
			},
		},
	}

	return manifestWork
}

// MarkArgoCDAgentManifestWorksAsOutdated marks existing ArgoCD agent ManifestWorks as outdated
// This is called when propagateHubCA is set to false
func (r *ReconcileGitOpsCluster) MarkArgoCDAgentManifestWorksAsOutdated(
	gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster) error {
	for _, managedCluster := range managedClusters {
		// Skip local-cluster - same logic as in CreateArgoCDAgentManifestWorks
		if IsLocalCluster(managedCluster) {
			continue
		}

		// Check if ManifestWork exists
		existingMW := &workv1.ManifestWork{}
		err := r.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca-mw",
			Namespace: managedCluster.Name,
		}, existingMW)

		if err != nil {
			if k8errors.IsNotFound(err) {
				// ManifestWork doesn't exist, nothing to mark
				continue
			}

			return fmt.Errorf("failed to get ManifestWork argocd-agent-ca-mw/%s: %w", managedCluster.Name, err)
		}

		// Mark the ManifestWork as outdated
		if existingMW.Annotations == nil {
			existingMW.Annotations = make(map[string]string)
		}
		existingMW.Annotations[ArgoCDAgentOutdatedAnnotation] = "true"
		existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] = "false"

		err = r.Update(context.TODO(), existingMW)
		if err != nil {
			klog.Errorf("failed to mark ManifestWork %s/%s as outdated: %v", existingMW.Namespace, existingMW.Name, err)
			return err
		}

		klog.Infof("marked ManifestWork %s/%s as outdated", existingMW.Namespace, existingMW.Name)
	}

	return nil
}
