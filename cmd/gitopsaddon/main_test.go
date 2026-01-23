// Copyright 2021 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"os"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
)

func TestGitOpsAddonConfig(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test default values
	t.Run("DefaultValues", func(t *testing.T) {
		config := utils.NewGitOpsAddonConfig()

		g.Expect(config.OperatorImages[utils.EnvGitOpsOperatorImage]).To(gomega.ContainSubstring("registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDImage]).To(gomega.ContainSubstring("registry.redhat.io/openshift-gitops-1/argocd-rhel8"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDRedisImage]).To(gomega.ContainSubstring("registry.redhat.io/rhel9/redis-7"))
		g.Expect(config.OperatorImages[utils.EnvBackendImage]).To(gomega.ContainSubstring("registry.redhat.io/openshift-gitops-1/gitops-rhel8"))
		g.Expect(config.OperatorImages[utils.EnvGitOpsConsolePlugin]).To(gomega.ContainSubstring("registry.redhat.io/openshift-gitops-1/console-plugin-rhel8"))
		g.Expect(config.ArgoCDAgentEnabled).To(gomega.BeFalse())
		g.Expect(config.OperatorImages[utils.EnvArgoCDPrincipalImage]).To(gomega.ContainSubstring("registry.redhat.io/openshift-gitops-1/argocd-agent-rhel8"))
		g.Expect(config.ArgoCDAgentServerAddress).To(gomega.Equal(""))
		g.Expect(config.ArgoCDAgentServerPort).To(gomega.Equal(""))
		g.Expect(config.ArgoCDAgentMode).To(gomega.Equal("managed"))
	})
}

func TestGitopsAddonAgentOptions(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("DefaultOptions", func(t *testing.T) {
		opts := GitopsAddonAgentOptions{
			MetricsAddr:                 "",
			LeaderElectionLeaseDuration: 60 * time.Second,
			LeaderElectionRenewDeadline: 10 * time.Second,
			LeaderElectionRetryPeriod:   2 * time.Second,
			SyncInterval:                60,
			Cleanup:                     false,
		}

		g.Expect(opts.MetricsAddr).To(gomega.Equal(""))
		g.Expect(opts.LeaderElectionLeaseDuration).To(gomega.Equal(60 * time.Second))
		g.Expect(opts.LeaderElectionRenewDeadline).To(gomega.Equal(10 * time.Second))
		g.Expect(opts.LeaderElectionRetryPeriod).To(gomega.Equal(2 * time.Second))
		g.Expect(opts.SyncInterval).To(gomega.Equal(60))
		g.Expect(opts.Cleanup).To(gomega.BeFalse())
	})

	t.Run("CleanupModeEnabled", func(t *testing.T) {
		opts := GitopsAddonAgentOptions{
			MetricsAddr:                 "",
			LeaderElectionLeaseDuration: 15 * time.Second,
			LeaderElectionRenewDeadline: 10 * time.Second,
			LeaderElectionRetryPeriod:   2 * time.Second,
			SyncInterval:                60,
			Cleanup:                     true,
		}

		g.Expect(opts.Cleanup).To(gomega.BeTrue())
	})
}

func TestEnvironmentVariableOverrides(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test overriding ArgoCD Agent environment variables
	t.Run("ArgoCDAgentEnvironmentOverrides", func(t *testing.T) {
		// Set environment variables
		t.Setenv("ARGOCD_AGENT_ENABLED", "true")
		t.Setenv("ARGOCD_AGENT_SERVER_ADDRESS", "custom.argocd.example.com")
		t.Setenv("ARGOCD_AGENT_SERVER_PORT", "8443")
		t.Setenv("ARGOCD_AGENT_MODE", "autonomous")

		// Create config with environment overrides
		config := utils.NewGitOpsAddonConfig()

		// Verify the values were updated
		g.Expect(config.ArgoCDAgentEnabled).To(gomega.BeTrue())
		g.Expect(config.ArgoCDAgentServerAddress).To(gomega.Equal("custom.argocd.example.com"))
		g.Expect(config.ArgoCDAgentServerPort).To(gomega.Equal("8443"))
		g.Expect(config.ArgoCDAgentMode).To(gomega.Equal("autonomous"))
	})
}

func TestGlobalVariables(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("SchemeInitialization", func(t *testing.T) {
		g.Expect(scheme).ToNot(gomega.BeNil())
	})

	t.Run("SetupLogInitialization", func(t *testing.T) {
		g.Expect(setupLog).ToNot(gomega.BeNil())
	})

	t.Run("MetricsConfiguration", func(t *testing.T) {
		g.Expect(metricsHost).To(gomega.Equal("0.0.0.0"))
		g.Expect(metricsPort).To(gomega.Equal(8387))
	})
}

func TestOperatorImageOverrides(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("GitOpsOperatorImageOverride", func(t *testing.T) {
		// Set environment variable
		t.Setenv("GITOPS_OPERATOR_IMAGE", "custom.registry.io/gitops-operator:v1.0.0")

		// Create config with environment overrides
		config := utils.NewGitOpsAddonConfig()

		// Verify the value was updated
		g.Expect(config.OperatorImages[utils.EnvGitOpsOperatorImage]).To(gomega.Equal("custom.registry.io/gitops-operator:v1.0.0"))
	})

	t.Run("AllOperatorImagesOverride", func(t *testing.T) {
		// Set all operator image environment variables
		t.Setenv("GITOPS_OPERATOR_IMAGE", "custom.registry.io/gitops-operator:test")
		t.Setenv("ARGOCD_IMAGE", "custom.registry.io/argocd:test")
		t.Setenv("ARGOCD_REDIS_IMAGE", "custom.registry.io/redis:test")
		t.Setenv("BACKEND_IMAGE", "custom.registry.io/backend:test")
		t.Setenv("GITOPS_CONSOLE_PLUGIN_IMAGE", "custom.registry.io/console-plugin:test")

		// Create config with environment overrides
		config := utils.NewGitOpsAddonConfig()

		// Verify all values were updated
		g.Expect(config.OperatorImages[utils.EnvGitOpsOperatorImage]).To(gomega.Equal("custom.registry.io/gitops-operator:test"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDImage]).To(gomega.Equal("custom.registry.io/argocd:test"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDRedisImage]).To(gomega.Equal("custom.registry.io/redis:test"))
		g.Expect(config.OperatorImages[utils.EnvBackendImage]).To(gomega.Equal("custom.registry.io/backend:test"))
		g.Expect(config.OperatorImages[utils.EnvGitOpsConsolePlugin]).To(gomega.Equal("custom.registry.io/console-plugin:test"))
	})
}

func TestProxyEnvironmentVariables(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("ProxyEnvironmentOverrides", func(t *testing.T) {
		// Set environment variables
		t.Setenv("HTTP_PROXY", "http://proxy.example.com:8080")
		t.Setenv("HTTPS_PROXY", "https://proxy.example.com:8443")
		t.Setenv("NO_PROXY", "localhost,127.0.0.1,.example.com")

		// Create config with environment overrides
		config := utils.NewGitOpsAddonConfig()

		// Verify the values were updated
		g.Expect(config.HTTPProxy).To(gomega.Equal("http://proxy.example.com:8080"))
		g.Expect(config.HTTPSProxy).To(gomega.Equal("https://proxy.example.com:8443"))
		g.Expect(config.NoProxy).To(gomega.Equal("localhost,127.0.0.1,.example.com"))
	})
}

func TestInit(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("InitScheme", func(t *testing.T) {
		// Verify that the scheme is properly initialized
		g.Expect(scheme).ToNot(gomega.BeNil())
		// Test that the init function has run by verifying scheme contains expected types
		gvkList := scheme.AllKnownTypes()
		g.Expect(len(gvkList)).To(gomega.BeNumerically(">", 0))
	})
}

func TestConstantsAndVariables(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("ConstantValues", func(t *testing.T) {
		// Test that critical constants have expected values
		g.Expect(metricsHost).To(gomega.Equal("0.0.0.0"))
		g.Expect(metricsPort).To(gomega.Equal(8387))

		// Test that all default image references contain registry.redhat.io
		config := utils.NewGitOpsAddonConfig()
		g.Expect(config.OperatorImages[utils.EnvGitOpsOperatorImage]).To(gomega.ContainSubstring("registry.redhat.io"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDImage]).To(gomega.ContainSubstring("registry.redhat.io"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDRedisImage]).To(gomega.ContainSubstring("registry.redhat.io"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDPrincipalImage]).To(gomega.ContainSubstring("registry.redhat.io"))
	})
}

func TestOptionsValidation(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("DefaultOptionsValidation", func(t *testing.T) {
		// Test that default option values are reasonable
		g.Expect(options.LeaderElectionLeaseDuration).To(gomega.BeNumerically(">", 0))
		g.Expect(options.LeaderElectionRenewDeadline).To(gomega.BeNumerically(">", 0))
		g.Expect(options.LeaderElectionRetryPeriod).To(gomega.BeNumerically(">", 0))
		g.Expect(options.SyncInterval).To(gomega.BeNumerically(">", 0))
		g.Expect(options.Cleanup).To(gomega.BeFalse())

		// Test leader election timing relationships
		g.Expect(options.LeaderElectionRenewDeadline).To(gomega.BeNumerically("<", options.LeaderElectionLeaseDuration))
		g.Expect(options.LeaderElectionRetryPeriod).To(gomega.BeNumerically("<", options.LeaderElectionRenewDeadline))
	})

	t.Run("ModifyOptions", func(t *testing.T) {
		// Test that options can be modified
		originalSyncInterval := options.SyncInterval
		options.SyncInterval = 120
		g.Expect(options.SyncInterval).To(gomega.Equal(120))
		// Restore
		options.SyncInterval = originalSyncInterval
	})
}

func TestEnvironmentVariableHandling(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("EmptyStringHandling", func(t *testing.T) {
		// Set empty environment variable (should not override default)
		os.Setenv("GITOPS_OPERATOR_IMAGE", "")

		// Create config
		config := utils.NewGitOpsAddonConfig()

		// Should use default value since empty string
		g.Expect(config.OperatorImages[utils.EnvGitOpsOperatorImage]).To(gomega.ContainSubstring("registry.redhat.io"))

		os.Unsetenv("GITOPS_OPERATOR_IMAGE")
	})

	t.Run("CompleteEnvironmentOverride", func(t *testing.T) {
		// Set all environment variables
		t.Setenv("GITOPS_OPERATOR_IMAGE", "test.registry.io/gitops-operator:test")
		t.Setenv("ARGOCD_IMAGE", "test.registry.io/argocd:test")
		t.Setenv("ARGOCD_REDIS_IMAGE", "test.registry.io/redis:test")
		t.Setenv("HTTP_PROXY", "http://test-proxy:8080")
		t.Setenv("HTTPS_PROXY", "https://test-proxy:8443")
		t.Setenv("NO_PROXY", "test.local")
		t.Setenv("ARGOCD_AGENT_ENABLED", "true")
		t.Setenv("ARGOCD_AGENT_SERVER_ADDRESS", "test.argocd.local")
		t.Setenv("ARGOCD_AGENT_SERVER_PORT", "9443")
		t.Setenv("ARGOCD_AGENT_MODE", "test-mode")

		// Create config
		config := utils.NewGitOpsAddonConfig()

		// Verify all values were updated
		g.Expect(config.OperatorImages[utils.EnvGitOpsOperatorImage]).To(gomega.Equal("test.registry.io/gitops-operator:test"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDImage]).To(gomega.Equal("test.registry.io/argocd:test"))
		g.Expect(config.OperatorImages[utils.EnvArgoCDRedisImage]).To(gomega.Equal("test.registry.io/redis:test"))
		g.Expect(config.HTTPProxy).To(gomega.Equal("http://test-proxy:8080"))
		g.Expect(config.HTTPSProxy).To(gomega.Equal("https://test-proxy:8443"))
		g.Expect(config.NoProxy).To(gomega.Equal("test.local"))
		g.Expect(config.ArgoCDAgentEnabled).To(gomega.BeTrue())
		g.Expect(config.ArgoCDAgentServerAddress).To(gomega.Equal("test.argocd.local"))
		g.Expect(config.ArgoCDAgentServerPort).To(gomega.Equal("9443"))
		g.Expect(config.ArgoCDAgentMode).To(gomega.Equal("test-mode"))
	})
}
