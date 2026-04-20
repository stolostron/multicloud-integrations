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
// Image SHAs sourced from: openshift-gitops-operator.v1.20.1 ClusterServiceVersion
var DefaultOperatorImages = map[string]string{
	// GitOps Operator image
	// CSV name: manager
	EnvGitOpsOperatorImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel9-operator@sha256:fd980bdf8227af7685025b9153005945252cd27798fae276e2775d895bc90ba6",

	// ArgoCD core images
	// CSV name: argocd_image
	EnvArgoCDImage:           "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:90d01a4812e54dcb7920a115dfa623e2ee16c6100c113f06ac9b84c9d4780e8a",
	EnvArgoCDRepoServerImage: "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:90d01a4812e54dcb7920a115dfa623e2ee16c6100c113f06ac9b84c9d4780e8a",

	// Redis images
	// CSV name: argocd_redis_image
	EnvArgoCDRedisImage:   "registry.redhat.io/rhel9/redis-7@sha256:3d31c0cfaf4219f5bd1c52882b603215d1cb4aaef5b8d1a128d0174e090f96f3",
	EnvArgoCDRedisHAImage: "registry.redhat.io/rhel9/redis-7@sha256:3d31c0cfaf4219f5bd1c52882b603215d1cb4aaef5b8d1a128d0174e090f96f3",
	// CSV name: argocd_redis_ha_proxy_image
	EnvArgoCDRedisHAProxyImage: "registry.redhat.io/openshift4/ose-haproxy-router@sha256:31d16a1533730e682b55b2b870f4175f3eef8030667f1713a593733b9da8cd53",

	// SSO / Dex image
	// CSV name: argocd_dex_image
	EnvArgoCDDexImage: "registry.redhat.io/openshift-gitops-1/dex-rhel9@sha256:d61b160166501af7d7efced50d5dcf3766c1f68365b8432dae6b81a4d7fe097f",

	// Backend / GitOps Service image
	// CSV name: backend_image
	EnvBackendImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel9@sha256:159416d9e28cf394a8bde9ba92634750c4ae6e46486f7714d055af468f8ad217",

	// Console plugin image
	// CSV name: gitops_console_plugin_image
	EnvGitOpsConsolePlugin: "registry.redhat.io/openshift-gitops-1/console-plugin-rhel9@sha256:ed35dbc85e4ffbded7c5bb9c85e7e82030b6fa047fb11bd345bece8cd07431b1",

	// Extension image
	// CSV name: argocd_extension_image
	EnvArgoCDExtensionImage: "registry.redhat.io/openshift-gitops-1/argocd-extensions-rhel9@sha256:659f31f3763a0358e6c0792edfef4b89c8ef5f73d0d233363de5235a866d9dd8",

	// Argo Rollouts image
	// CSV name: argo_rollouts_image
	EnvArgoRolloutsImage: "registry.redhat.io/openshift-gitops-1/argo-rollouts-rhel9@sha256:8f0f7c8caebaffa4b2cd6f6d0f3e94fed93b2e34c35a229178dc4da6e6357d93",

	// ArgoCD Agent Principal image - used on hub for argocd-agent principal
	// CSV name: argocd_principal_image
	EnvArgoCDPrincipalImage: "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:cdc6d6f86e08bded208cb772897e97455f11483c0a99a45d6548f19f3048a096",

	// ArgoCD Agent image - used on spoke for argocd-agent component
	// CSV name: argocd_agent_image (same image for both principal and agent)
	EnvArgoCDAgentImage: "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:cdc6d6f86e08bded208cb772897e97455f11483c0a99a45d6548f19f3048a096",

	// ArgoCD Image Updater image
	// CSV name: argocd_image_updater_image
	EnvArgoCDImageUpdaterImage: "registry.redhat.io/openshift-gitops-1/argocd-image-updater-rhel9@sha256:27e70552bc7d1c87dfe1bcb8cd358dad2dcacfcda258eaa5f74dae500d099160",
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
