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
var DefaultOperatorImages = map[string]string{
	// GitOps Operator image
	EnvGitOpsOperatorImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator@sha256:c3f236b77221bfb0ffd9f44fbf231ecf44764905226f9eaa876c9335c27acb46",

	// ArgoCD core images
	EnvArgoCDImage:           "registry.redhat.io/openshift-gitops-1/argocd-rhel8@sha256:a5d9ecefafd75b1c8ba0fd067f6336d7df1cd8775e3c250032048c9745875e4f",
	EnvArgoCDRepoServerImage: "registry.redhat.io/openshift-gitops-1/argocd-rhel8@sha256:a5d9ecefafd75b1c8ba0fd067f6336d7df1cd8775e3c250032048c9745875e4f",

	// Redis images
	EnvArgoCDRedisImage:        "registry.redhat.io/rhel9/redis-7@sha256:3d31c0cfaf4219f5bd1c52882b603215d1cb4aaef5b8d1a128d0174e090f96f3",
	EnvArgoCDRedisHAImage:      "registry.redhat.io/rhel9/redis-7@sha256:3d31c0cfaf4219f5bd1c52882b603215d1cb4aaef5b8d1a128d0174e090f96f3",
	EnvArgoCDRedisHAProxyImage: "registry.redhat.io/openshift4/ose-haproxy-router@sha256:21f3a0248a09051849317576680f80820c75996d99d703a9b3206a88569e6581",

	// SSO / Dex image
	EnvArgoCDDexImage: "registry.redhat.io/openshift-gitops-1/dex-rhel8@sha256:442508af831dd69f0bdfde9065c8bf2776abf5d37786467528f696e372f5d996",

	// Backend / GitOps Service image
	EnvBackendImage: "registry.redhat.io/openshift-gitops-1/gitops-rhel8@sha256:fa0f52974e14c22af290c896f1f0b9ff62148a27317f48b3b5b2ea8768c9549b",

	// Console plugin image
	EnvGitOpsConsolePlugin: "registry.redhat.io/openshift-gitops-1/console-plugin-rhel8@sha256:74112a7f92c432baef25f9af458d90bfae308aa23f0b188d46c15b9917090be1",

	// Extension image
	EnvArgoCDExtensionImage: "registry.redhat.io/openshift-gitops-1/argocd-extensions-rhel8@sha256:8afea9d7876656c931f8050979d6d92c85b01069c7a5f2de46c0f65002663458",

	// Argo Rollouts image
	EnvArgoRolloutsImage: "registry.redhat.io/openshift-gitops-1/argo-rollouts-rhel8@sha256:5365dfcdcedde372b9d7cc787107b05f997c7526fd1d473e26f843718a5be92d",

	// ArgoCD Agent (Principal) image - also used for agent component
	EnvArgoCDPrincipalImage: "registry.redhat.io/openshift-gitops-1/argocd-agent-rhel8@sha256:c650147ab5940d7095eaee89ba75956726838595057ab6b216d41d0f09265f30",

	// ArgoCD Image Updater image
	EnvArgoCDImageUpdaterImage: "registry.redhat.io/openshift-gitops-1/argocd-image-updater-rhel8@sha256:3c8df368671ee9aa4fbbddf46b2e0e7730d040b617986d799f0807fb14f866d5",
}

// hubOnlyEnvVars are environment variables that are only used on the hub side
// and should NOT be passed to the spoke/managed cluster via AddOnDeploymentConfig.
var hubOnlyEnvVars = map[string]bool{
	EnvArgoCDPrincipalImage: true, // Used on hub for argocd-agent principal, not needed on spoke
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
