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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Note: The argocd-agent-ca secret is now managed by EnsureArgoCDAgentCASecret
// in argocd_agent_certificates.go using certrotation from ocm-io/sdk-go

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
		return fmt.Errorf("ArgoCD Redis password secret not found in namespace '%s'. Please ensure ArgoCD has been installed and the Redis initial password secret exists (should end with 'redis-initial-password')", gitopsNamespace)
	}

	// Extract the admin.password value
	adminPasswordBytes, exists := initialPasswordSecret.Data["admin.password"]
	if !exists {
		return fmt.Errorf("admin.password field is missing in Redis password secret '%s'. The secret may be corrupted or in an unexpected format", initialPasswordSecret.Name)
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
