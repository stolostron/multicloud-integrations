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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ensureArgoCDAgentCASecret ensures the argocd-agent-ca secret exists in GitOps namespace
// by copying it from the multicluster-operators-application-svc-ca secret in open-cluster-management namespace
func (r *ReconcileGitOpsCluster) ensureArgoCDAgentCASecret(gitopsNamespace string) error {
	// Check if argocd-agent-ca secret already exists
	targetSecret := &v1.Secret{}
	targetSecretName := types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: gitopsNamespace,
	}

	err := r.Get(context.TODO(), targetSecretName, targetSecret)
	if err == nil {
		klog.Info("argocd-agent-ca secret already exists in namespace", gitopsNamespace)
		return nil
	}

	if !k8errors.IsNotFound(err) {
		return fmt.Errorf("failed to check argocd-agent-ca secret: %w", err)
	}

	klog.Info("argocd-agent-ca secret not found, copying from source secret in namespace", gitopsNamespace)

	// Get the source secret from open-cluster-management namespace
	sourceSecret := &v1.Secret{}
	sourceSecretName := types.NamespacedName{
		Name:      "multicluster-operators-application-svc-ca",
		Namespace: utils.GetComponentNamespace("open-cluster-management"),
	}

	err = r.Get(context.TODO(), sourceSecretName, sourceSecret)
	if err != nil {
		if k8errors.IsNotFound(err) {
			return fmt.Errorf("source secret multicluster-operators-application-svc-ca not found in %s namespace - ensure OCM is properly installed", sourceSecretName.Namespace)
		}

		return fmt.Errorf("failed to get source secret: %w", err)
	}

	// Prepare labels and annotations maps
	labels := make(map[string]string)
	if sourceSecret.Labels != nil {
		for k, v := range sourceSecret.Labels {
			labels[k] = v
		}
	}

	annotations := make(map[string]string)
	if sourceSecret.Annotations != nil {
		for k, v := range sourceSecret.Annotations {
			annotations[k] = v
		}
	}

	// Create the new secret with modified name and namespace
	newSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "argocd-agent-ca",
			Namespace:   gitopsNamespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Type: sourceSecret.Type,
		Data: sourceSecret.Data,
	}

	err = r.Create(context.TODO(), newSecret)
	if err != nil {
		if k8errors.IsAlreadyExists(err) {
			klog.Info("argocd-agent-ca secret was created by another process in namespace", gitopsNamespace)
			return nil
		}

		return fmt.Errorf("failed to create argocd-agent-ca secret: %w", err)
	}

	klog.Info("Successfully created argocd-agent-ca secret in namespace", gitopsNamespace)

	return nil
}

// ensureArgoCDRedisSecret ensures the argocd-redis secret exists in GitOps namespace
func (r *ReconcileGitOpsCluster) ensureArgoCDRedisSecret(gitopsNamespace string) error {
	// Default to openshift-gitops if namespace is empty
	if gitopsNamespace == "" {
		gitopsNamespace = "openshift-gitops"
	}

	// Check if argocd-redis secret already exists
	argoCDRedisSecret := &v1.Secret{}
	argoCDRedisSecretKey := types.NamespacedName{
		Name:      "argocd-redis",
		Namespace: gitopsNamespace,
	}

	err := r.Get(context.TODO(), argoCDRedisSecretKey, argoCDRedisSecret)
	if err == nil {
		klog.Info("argocd-redis secret already exists, skipping creation")
		return nil
	}

	if !k8errors.IsNotFound(err) {
		return fmt.Errorf("failed to check argocd-redis secret: %w", err)
	}

	klog.Info("argocd-redis secret not found, creating it...")

	// Find the secret ending with "redis-initial-password"
	secretList := &v1.SecretList{}
	err = r.List(context.TODO(), secretList, client.InNamespace(gitopsNamespace))
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %s: %w", gitopsNamespace, err)
	}

	var initialPasswordSecret *v1.Secret
	for i := range secretList.Items {
		if strings.HasSuffix(secretList.Items[i].Name, "redis-initial-password") {
			initialPasswordSecret = &secretList.Items[i]
			break
		}
	}

	if initialPasswordSecret == nil {
		return fmt.Errorf("no secret found ending with 'redis-initial-password' in namespace %s", gitopsNamespace)
	}

	// Extract the admin.password value
	adminPasswordBytes, exists := initialPasswordSecret.Data["admin.password"]
	if !exists {
		return fmt.Errorf("admin.password not found in secret %s", initialPasswordSecret.Name)
	}

	// Create the argocd-redis secret
	argoCDRedisSecretNew := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-redis",
			Namespace: gitopsNamespace,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopscluster": "true",
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"auth": adminPasswordBytes,
		},
	}

	err = r.Create(context.TODO(), argoCDRedisSecretNew)
	if err != nil {
		if k8errors.IsAlreadyExists(err) {
			klog.Info("argocd-redis secret was created by another process, continuing...")
			return nil
		}

		return fmt.Errorf("failed to create argocd-redis secret: %w", err)
	}

	klog.Info("Successfully created argocd-redis secret")

	return nil
}

// ensureArgoCDAgentJWTSecret ensures the argocd-agent-jwt secret exists in GitOps namespace
// This function creates a JWT signing key similar to the argocd-agentctl jwt create-key command
func (r *ReconcileGitOpsCluster) ensureArgoCDAgentJWTSecret(gitopsNamespace string) error {
	// Default to openshift-gitops if namespace is empty
	if gitopsNamespace == "" {
		gitopsNamespace = "openshift-gitops"
	}

	// Check if argocd-agent-jwt secret already exists
	argoCDAgentJWTSecret := &v1.Secret{}
	argoCDAgentJWTSecretKey := types.NamespacedName{
		Name:      "argocd-agent-jwt",
		Namespace: gitopsNamespace,
	}

	err := r.Get(context.TODO(), argoCDAgentJWTSecretKey, argoCDAgentJWTSecret)
	if err == nil {
		klog.Info("argocd-agent-jwt secret already exists, skipping creation")
		return nil
	}

	if !k8errors.IsNotFound(err) {
		return fmt.Errorf("failed to check argocd-agent-jwt secret: %w", err)
	}

	klog.Info("argocd-agent-jwt secret not found, creating JWT signing key...")

	// Generate 4096-bit RSA private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return fmt.Errorf("could not generate RSA private key: %w", err)
	}

	// Convert to PKCS#8 PEM format
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("could not marshal private key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	})

	// Create the argocd-agent-jwt secret
	argoCDAgentJWTSecretNew := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-jwt",
			Namespace: gitopsNamespace,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopscluster": "true",
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"jwt.key": keyPEM,
		},
	}

	err = r.Create(context.TODO(), argoCDAgentJWTSecretNew)
	if err != nil {
		if k8errors.IsAlreadyExists(err) {
			klog.Info("argocd-agent-jwt secret was created by another process, continuing...")
			return nil
		}

		return fmt.Errorf("failed to create argocd-agent-jwt secret: %w", err)
	}

	klog.Info("Successfully created argocd-agent-jwt secret with RSA-4096 signing key")

	return nil
}
