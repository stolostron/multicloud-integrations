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

import (
	"os"
	"sort"
)

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
// installed on the hub.
var DefaultOperatorImages = map[string]string{
	// GitOps Operator image
	// CSV name: manager
	EnvGitOpsOperatorImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel9-operator@sha256:ec145c44990edf0ee098ea6552a4c6c120c66e725efe6fdd4933d6051602dbf6",

	// ArgoCD core images
	// CSV name: argocd_image
	EnvArgoCDImage:           "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:deda478bd4d845e08dd25fb839e1c15d7d4507a397c7184c90b4f78d19410e05",
	EnvArgoCDRepoServerImage: "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:deda478bd4d845e08dd25fb839e1c15d7d4507a397c7184c90b4f78d19410e05",

	// Redis images
	// CSV name: argocd_redis_image
	EnvArgoCDRedisImage:   "registry.redhat.io/rhel9/redis-7@sha256:d4865f7560b555f366db86b6097568e02163fed131f765352d46d9478e59f5d6",
	EnvArgoCDRedisHAImage: "registry.redhat.io/rhel9/redis-7@sha256:d4865f7560b555f366db86b6097568e02163fed131f765352d46d9478e59f5d6",
	// CSV name: argocd_redis_ha_proxy_image
	EnvArgoCDRedisHAProxyImage: "registry.redhat.io/openshift4/ose-haproxy-router-rhel9@sha256:5a0aa5b4d20601b1632a315fd25aed7f94fe2b1e5ff097fcc42be245b3c72e9b",

	// SSO / Dex image
	// CSV name: argocd_dex_image
	EnvArgoCDDexImage: "registry.redhat.io/openshift-gitops-1/dex-rhel9@sha256:51454e5c706fd913503980d29a11ab233e3d0b82d319b23d46394612dd81d3ff",

	// Backend / GitOps Service image
	// CSV name: backend_image
	EnvBackendImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel9@sha256:36a54bd16da5c4ddd09edcce72942e92d4d18533453bd1e93aee3261ace6949c",

	// Console plugin image
	// CSV name: gitops_console_plugin_image
	EnvGitOpsConsolePlugin: "registry.redhat.io/openshift-gitops-1/console-plugin-rhel9@sha256:a87a5e273a4b47e0d4ac8a6fdad7e74abef7ce93e8250623927004d4d1fc09a0",

	// Extension image
	// CSV name: argocd_extension_image
	EnvArgoCDExtensionImage: "registry.redhat.io/openshift-gitops-1/argocd-extensions-rhel9@sha256:34c704f9fb45355b434146fc5097db0b612643e7f91856155ac5765b3da5045d",

	// Argo Rollouts image
	// CSV name: argo_rollouts_image
	EnvArgoRolloutsImage: "registry.redhat.io/openshift-gitops-1/argo-rollouts-rhel9@sha256:bd7a1f2743eee67efbea5cab042ff4a1656f4f6821b925159d516c8f581bc38c",

	// ArgoCD Agent Principal image - used on hub for argocd-agent principal
	// CSV name: argocd_principal_image
	EnvArgoCDPrincipalImage: "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:b7d3afe3a5f6cdc41377e3ed522ee7c7db74f126dfefee9d62ede8ddfbf6b1cc",

	// ArgoCD Agent image - used on spoke for argocd-agent component
	// CSV name: argocd_agent_image (same image for both principal and agent)
	EnvArgoCDAgentImage: "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel9@sha256:b7d3afe3a5f6cdc41377e3ed522ee7c7db74f126dfefee9d62ede8ddfbf6b1cc",

	// ArgoCD Image Updater image
	// CSV name: argocd_image_updater_image
	EnvArgoCDImageUpdaterImage: "registry.redhat.io/openshift-gitops-1/argocd-image-updater-rhel9@sha256:bd8604a3da6f4350962fb6e17e4550b8d666271f830bcb799a474feda403395d",
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

	// Add image env vars (excluding hub-only vars), sorted: ranging over a map directly
	// randomizes iteration order on every call (Go intentionally randomizes map iteration), so
	// callers that build ordered/positional output from this list would otherwise see a
	// different order on every call despite identical content.
	imageEnvKeys := make([]string, 0, len(DefaultOperatorImages))
	for envKey := range DefaultOperatorImages {
		if !hubOnlyEnvVars[envKey] {
			imageEnvKeys = append(imageEnvKeys, envKey)
		}
	}
	sort.Strings(imageEnvKeys)
	vars = append(vars, imageEnvKeys...)

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

	// Add all image env vars, sorted (see SpokeConfigEnvVars for why).
	imageEnvKeys := make([]string, 0, len(DefaultOperatorImages))
	for envKey := range DefaultOperatorImages {
		imageEnvKeys = append(imageEnvKeys, envKey)
	}
	sort.Strings(imageEnvKeys)
	vars = append(vars, imageEnvKeys...)

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
