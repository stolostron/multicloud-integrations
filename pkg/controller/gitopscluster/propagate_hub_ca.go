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

// PropagateHubCA propagates the ArgoCD agent CA certificate from hub to managed clusters via ManifestWork
// This function reads the argocd-agent-ca secret from the hub and creates/updates ManifestWorks
// to deploy this secret to all managed clusters (except local-cluster).
// The ManifestWorks are never deleted - they persist even if the secret changes or is removed.
func (r *ReconcileGitOpsCluster) PropagateHubCA(
	gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster) error {
	// Hub namespace - where we read the CA cert from (use GitOpsCluster's namespace)
	hubNamespace := gitOpsCluster.Namespace

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
				// Create new ManifestWork
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
			// ManifestWork exists - check if certificate data has changed
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

			// Update ManifestWork if certificate changed
			if certificateChanged {
				existingMW.Spec = manifestWork.Spec

				err = r.Update(context.TODO(), existingMW)
				if err != nil {
					klog.Errorf("failed to update ManifestWork %s/%s: %v", existingMW.Namespace, existingMW.Name, err)
					return err
				}

				klog.Infof("updated ManifestWork %s/%s with new ArgoCD agent CA", existingMW.Namespace, existingMW.Name)
			} else {
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
			return "", fmt.Errorf("CA certificate data not found in argocd-agent-ca secret. Expected either 'tls.crt' or 'ca.crt' field to be present")
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
