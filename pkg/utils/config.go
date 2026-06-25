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

package utils

import "os"

// Environment variable names for GitOps addon configuration.
// These are the canonical names used throughout the system:
// - Set via AddOnDeploymentConfig on the hub cluster
// - Passed as template placeholders in AddOnTemplate
// - Read by the gitopsaddon agent on the spoke/managed cluster
const (
	// Image environment variables - these match what the GitOps operator expects
	EnvGitOpsOperatorImage     = "GITOPS_OPERATOR_IMAGE"
	EnvArgoCDImage             = "ARGOCD_IMAGE"
	EnvArgoCDRepoServerImage   = "ARGOCD_REPOSERVER_IMAGE"
	EnvArgoCDRedisImage        = "ARGOCD_REDIS_IMAGE"
	EnvArgoCDRedisHAImage      = "ARGOCD_REDIS_HA_IMAGE"
	EnvArgoCDRedisHAProxyImage = "ARGOCD_REDIS_HA_PROXY_IMAGE"
	EnvArgoCDDexImage          = "ARGOCD_DEX_IMAGE"
	EnvBackendImage            = "BACKEND_IMAGE"
	EnvGitOpsConsolePlugin     = "GITOPS_CONSOLE_PLUGIN_IMAGE"
	EnvArgoCDExtensionImage    = "ARGOCD_EXTENSION_IMAGE"
	EnvArgoRolloutsImage       = "ARGO_ROLLOUTS_IMAGE"
	EnvArgoCDPrincipalImage    = "ARGOCD_PRINCIPAL_IMAGE"
	EnvArgoCDAgentImage        = "ARGOCD_AGENT_IMAGE"
	EnvArgoCDImageUpdaterImage = "ARGOCD_IMAGE_UPDATER_IMAGE"

	// Proxy environment variables
	EnvHTTPProxy  = "HTTP_PROXY"
	EnvHTTPSProxy = "HTTPS_PROXY"
	EnvNoProxy    = "NO_PROXY"

	// ArgoCD Agent configuration environment variables
	EnvArgoCDAgentEnabled       = "ARGOCD_AGENT_ENABLED"
	EnvArgoCDAgentServerAddress = "ARGOCD_AGENT_SERVER_ADDRESS"
	EnvArgoCDAgentServerPort    = "ARGOCD_AGENT_SERVER_PORT"
	EnvArgoCDAgentMode          = "ARGOCD_AGENT_MODE"
)

// Default image values - these should match the latest Red Hat OpenShift GitOps operator bundle
// Image SHAs sourced from: openshift-gitops-operator.v1.21.0 ClusterServiceVersion
var DefaultOperatorImages = map[string]string{
	// GitOps Operator image
	// CSV name: manager
	EnvGitOpsOperatorImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel9-operator@sha256:52db2c50a554a80b3560ece381cffcad41ceff7f34cd9c590b4f95a60f227c68",

	// ArgoCD core images
	// CSV name: argocd_image
	EnvArgoCDImage:           "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:1df65c944adf38f5b22f86f633a36d7e6d81f9bc744691ec4fc181bb076f96a8",
	EnvArgoCDRepoServerImage: "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:1df65c944adf38f5b22f86f633a36d7e6d81f9bc744691ec4fc181bb076f96a8",

	// Redis images
	// CSV name: argocd_redis_image
	EnvArgoCDRedisImage:   "registry.redhat.io/rhel9/redis-7@sha256:3d31c0cfaf4219f5bd1c52882b603215d1cb4aaef5b8d1a128d0174e090f96f3",
	EnvArgoCDRedisHAImage: "registry.redhat.io/rhel9/redis-7@sha256:3d31c0cfaf4219f5bd1c52882b603215d1cb4aaef5b8d1a128d0174e090f96f3",
	// CSV name: argocd_redis_ha_proxy_image
	EnvArgoCDRedisHAProxyImage: "registry.redhat.io/openshift4/ose-haproxy-router-rhel9@sha256:37d7152baebc92d886e1ffb48f86561a96e94e8f4bb0a2a7546bd0aee90f447d",

	// SSO / Dex image
	// CSV name: argocd_dex_image
	EnvArgoCDDexImage: "registry.redhat.io/openshift-gitops-1/dex-rhel9@sha256:a5ba068c793188f2300c3ffa40f53b9915f19a4cf517f5243be8918e61cf37ce",

	// Backend / GitOps Service image
	// CSV name: backend_image
	EnvBackendImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel9@sha256:36c5430f08f788b425c2b30dd6e7ceef3ae8162a4cb854b7dd459f4d91286295",

	// Console plugin image
	// CSV name: gitops_console_plugin_image
	EnvGitOpsConsolePlugin: "registry.redhat.io/openshift-gitops-1/console-plugin-rhel9@sha256:c1b7cb1c31f7f8987768e155df210ff89be20f5230355ec744aa75253dd73a2a",

	// Extension image
	// CSV name: argocd_extension_image
	EnvArgoCDExtensionImage: "registry.redhat.io/openshift-gitops-1/argocd-extensions-rhel9@sha256:812ebcdda9380161c64be9182b42fd9167f63da220f2e2f1adc4f6c00696a2eb",

	// Argo Rollouts image
	// CSV name: argo_rollouts_image
	EnvArgoRolloutsImage: "registry.redhat.io/openshift-gitops-1/argo-rollouts-rhel9@sha256:887cc49617f87decc0dc6ba595739194fce6e95d849657723fcf227893a8a842",

	// ArgoCD Agent Principal image - used on hub for argocd-agent principal
	// CSV name: argocd_principal_image
	EnvArgoCDPrincipalImage: "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:aa21554e99267be91fb17f093afc277615098060cd4cab6fd5d473a4ee46ef39",

	// ArgoCD Agent image - used on spoke for argocd-agent component
	// CSV name: argocd_agent_image (same image for both principal and agent)
	EnvArgoCDAgentImage: "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:aa21554e99267be91fb17f093afc277615098060cd4cab6fd5d473a4ee46ef39",

	// ArgoCD Image Updater image
	// CSV name: argocd_image_updater_image
	EnvArgoCDImageUpdaterImage: "registry.redhat.io/openshift-gitops-1/argocd-image-updater-rhel9@sha256:0d23ad519706b18f28100a309dd3b23fefbba10625905fb0b9f638add2c02cb9",
}

// hubOnlyEnvVars are environment variables that are only used on the hub side
// and should NOT be passed to the spoke/managed cluster via AddOnDeploymentConfig.
var hubOnlyEnvVars = map[string]bool{
	EnvArgoCDPrincipalImage: true, // Used on hub for argocd-agent principal service, not needed on spoke
	// Note: EnvArgoCDAgentImage IS passed to spoke - it's used for the agent component on managed clusters
}

// SpokeConfigEnvVars returns a list of environment variable names that should be configured
// via AddOnDeploymentConfig and available as template placeholders for spoke/managed clusters.
// This excludes hub-only variables like ARGOCD_PRINCIPAL_IMAGE.
func SpokeConfigEnvVars() []string {
	vars := make([]string, 0, len(DefaultOperatorImages)+7)

	// Add image env vars (excluding hub-only vars)
	for envKey := range DefaultOperatorImages {
		if !hubOnlyEnvVars[envKey] {
			vars = append(vars, envKey)
		}
	}

	// Add proxy env vars
	vars = append(vars, EnvHTTPProxy, EnvHTTPSProxy, EnvNoProxy)

	// Add ArgoCD Agent env vars
	vars = append(vars, EnvArgoCDAgentEnabled, EnvArgoCDAgentServerAddress, EnvArgoCDAgentServerPort, EnvArgoCDAgentMode)

	return vars
}

// AllConfigEnvVars returns a list of all environment variable names including hub-only vars.
// Use SpokeConfigEnvVars() for variables that should flow to spoke/managed clusters.
func AllConfigEnvVars() []string {
	vars := make([]string, 0, len(DefaultOperatorImages)+7)

	// Add all image env vars
	for envKey := range DefaultOperatorImages {
		vars = append(vars, envKey)
	}

	// Add proxy env vars
	vars = append(vars, EnvHTTPProxy, EnvHTTPSProxy, EnvNoProxy)

	// Add ArgoCD Agent env vars
	vars = append(vars, EnvArgoCDAgentEnabled, EnvArgoCDAgentServerAddress, EnvArgoCDAgentServerPort, EnvArgoCDAgentMode)

	return vars
}

// IsHubOnlyEnvVar returns true if the environment variable is only used on the hub side
// and should not be passed to spoke/managed clusters.
func IsHubOnlyEnvVar(envKey string) bool {
	return hubOnlyEnvVars[envKey]
}

// GitOpsAddonConfig holds all configuration for the GitOps addon agent.
// This is populated on the spoke/managed cluster by reading environment variables.
type GitOpsAddonConfig struct {
	// OperatorImages is a map of env var name to image value
	// This matches the environment variables expected by the GitOps operator
	OperatorImages map[string]string

	// Proxy configuration
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string

	// ArgoCD Agent configuration
	ArgoCDAgentEnabled       bool
	ArgoCDAgentServerAddress string
	ArgoCDAgentServerPort    string
	ArgoCDAgentMode          string
}

// NewGitOpsAddonConfig creates a new GitOpsAddonConfig with default values
// and then overrides with environment variables if set.
// This is called on the spoke/managed cluster to read configuration that was
// passed via AddOnDeploymentConfig template substitution.
func NewGitOpsAddonConfig() *GitOpsAddonConfig {
	config := &GitOpsAddonConfig{
		OperatorImages:  make(map[string]string),
		ArgoCDAgentMode: "managed",
	}

	// Copy defaults and override with environment variables
	for envKey, defaultValue := range DefaultOperatorImages {
		if envValue := os.Getenv(envKey); envValue != "" {
			config.OperatorImages[envKey] = envValue
		} else {
			config.OperatorImages[envKey] = defaultValue
		}
	}

	// Proxy configuration
	if v := os.Getenv(EnvHTTPProxy); v != "" {
		config.HTTPProxy = v
	}
	if v := os.Getenv(EnvHTTPSProxy); v != "" {
		config.HTTPSProxy = v
	}
	if v := os.Getenv(EnvNoProxy); v != "" {
		config.NoProxy = v
	}

	// ArgoCD Agent configuration
	if v := os.Getenv(EnvArgoCDAgentEnabled); v == "true" {
		config.ArgoCDAgentEnabled = true
	}
	if v := os.Getenv(EnvArgoCDAgentServerAddress); v != "" {
		config.ArgoCDAgentServerAddress = v
	}
	if v := os.Getenv(EnvArgoCDAgentServerPort); v != "" {
		config.ArgoCDAgentServerPort = v
	}
	if v := os.Getenv(EnvArgoCDAgentMode); v != "" {
		config.ArgoCDAgentMode = v
	}

	return config
}

// GetImage returns the image for the given environment variable name.
// If the image is not set, it returns the default value.
func (c *GitOpsAddonConfig) GetImage(envName string) string {
	if img, ok := c.OperatorImages[envName]; ok {
		return img
	}
	if defaultImg, ok := DefaultOperatorImages[envName]; ok {
		return defaultImg
	}
	return ""
}
