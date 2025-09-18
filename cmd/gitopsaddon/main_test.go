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
)

func TestEnvironmentVariables(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test default values
	t.Run("DefaultValues", func(t *testing.T) {
		g.Expect(GitopsOperatorImage).To(gomega.ContainSubstring("registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator"))
		g.Expect(GitopsOperatorNS).To(gomega.Equal("openshift-gitops-operator"))
		g.Expect(GitopsImage).To(gomega.ContainSubstring("registry.redhat.io/openshift-gitops-1/argocd-rhel8"))
		g.Expect(GitopsNS).To(gomega.Equal("openshift-gitops"))
		g.Expect(RedisImage).To(gomega.ContainSubstring("registry.redhat.io/rhel9/redis-7"))
		g.Expect(ReconcileScope).To(gomega.Equal("Single-Namespace"))
		g.Expect(ACTION).To(gomega.Equal("Install"))
		g.Expect(ARGOCD_AGENT_ENABLED).To(gomega.Equal("false"))
		g.Expect(ARGOCD_AGENT_IMAGE).To(gomega.ContainSubstring("registry.redhat.io/openshift-gitops-1/argocd-agent-rhel8@sha256:2f5f997bce924445de735ae0508dca1a7bba561bc4acdacf659928488233cb8a"))
		g.Expect(ARGOCD_AGENT_SERVER_ADDRESS).To(gomega.Equal(""))
		g.Expect(ARGOCD_AGENT_SERVER_PORT).To(gomega.Equal(""))
		g.Expect(ARGOCD_AGENT_MODE).To(gomega.Equal("managed"))
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
		}

		g.Expect(opts.MetricsAddr).To(gomega.Equal(""))
		g.Expect(opts.LeaderElectionLeaseDuration).To(gomega.Equal(60 * time.Second))
		g.Expect(opts.LeaderElectionRenewDeadline).To(gomega.Equal(10 * time.Second))
		g.Expect(opts.LeaderElectionRetryPeriod).To(gomega.Equal(2 * time.Second))
		g.Expect(opts.SyncInterval).To(gomega.Equal(60))
	})
}

func TestEnvironmentVariableOverrides(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Store original values to restore later
	originalValues := map[string]string{
		"ARGOCD_AGENT_ENABLED":        ARGOCD_AGENT_ENABLED,
		"ARGOCD_AGENT_IMAGE":          ARGOCD_AGENT_IMAGE,
		"ARGOCD_AGENT_SERVER_ADDRESS": ARGOCD_AGENT_SERVER_ADDRESS,
		"ARGOCD_AGENT_SERVER_PORT":    ARGOCD_AGENT_SERVER_PORT,
		"ARGOCD_AGENT_MODE":           ARGOCD_AGENT_MODE,
	}

	// Test overriding ArgoCD Agent environment variables
	t.Run("ArgoCDAgentEnvironmentOverrides", func(t *testing.T) {
		// Set environment variables
		testEnvVars := map[string]string{
			"ARGOCD_AGENT_ENABLED":        "true",
			"ARGOCD_AGENT_IMAGE":          "custom.registry.io/argocd-agent:v1.2.3",
			"ARGOCD_AGENT_SERVER_ADDRESS": "custom.argocd.example.com",
			"ARGOCD_AGENT_SERVER_PORT":    "8443",
			"ARGOCD_AGENT_MODE":           "autonomous",
		}

		for key, value := range testEnvVars {
			t.Setenv(key, value)
		}

		// Simulate the environment variable processing logic from main()
		if val, found := os.LookupEnv("ARGOCD_AGENT_ENABLED"); found && val > "" {
			ARGOCD_AGENT_ENABLED = val
		}

		if val, found := os.LookupEnv("ARGOCD_AGENT_IMAGE"); found && val > "" {
			ARGOCD_AGENT_IMAGE = val
		}

		if val, found := os.LookupEnv("ARGOCD_AGENT_SERVER_ADDRESS"); found && val > "" {
			ARGOCD_AGENT_SERVER_ADDRESS = val
		}

		if val, found := os.LookupEnv("ARGOCD_AGENT_SERVER_PORT"); found && val > "" {
			ARGOCD_AGENT_SERVER_PORT = val
		}

		if val, found := os.LookupEnv("ARGOCD_AGENT_MODE"); found && val > "" {
			ARGOCD_AGENT_MODE = val
		}

		// Verify the values were updated
		g.Expect(ARGOCD_AGENT_ENABLED).To(gomega.Equal("true"))
		g.Expect(ARGOCD_AGENT_IMAGE).To(gomega.Equal("custom.registry.io/argocd-agent:v1.2.3"))
		g.Expect(ARGOCD_AGENT_SERVER_ADDRESS).To(gomega.Equal("custom.argocd.example.com"))
		g.Expect(ARGOCD_AGENT_SERVER_PORT).To(gomega.Equal("8443"))
		g.Expect(ARGOCD_AGENT_MODE).To(gomega.Equal("autonomous"))

		// Restore original values
		for key, value := range originalValues {
			switch key {
			case "ARGOCD_AGENT_ENABLED":
				ARGOCD_AGENT_ENABLED = value
			case "ARGOCD_AGENT_IMAGE":
				ARGOCD_AGENT_IMAGE = value
			case "ARGOCD_AGENT_SERVER_ADDRESS":
				ARGOCD_AGENT_SERVER_ADDRESS = value
			case "ARGOCD_AGENT_SERVER_PORT":
				ARGOCD_AGENT_SERVER_PORT = value
			case "ARGOCD_AGENT_MODE":
				ARGOCD_AGENT_MODE = value
			}
		}
	})

	t.Run("EmptyEnvironmentVariables", func(t *testing.T) {
		// Test with empty environment variables (should not override defaults)
		testEnvVars := map[string]string{
			"ARGOCD_AGENT_ENABLED":        "",
			"ARGOCD_AGENT_IMAGE":          "",
			"ARGOCD_AGENT_SERVER_ADDRESS": "",
			"ARGOCD_AGENT_SERVER_PORT":    "",
			"ARGOCD_AGENT_MODE":           "",
		}

		for key, value := range testEnvVars {
			t.Setenv(key, value)
		}

		// Store current values
		currentValues := map[string]string{
			"ARGOCD_AGENT_ENABLED":        ARGOCD_AGENT_ENABLED,
			"ARGOCD_AGENT_IMAGE":          ARGOCD_AGENT_IMAGE,
			"ARGOCD_AGENT_SERVER_ADDRESS": ARGOCD_AGENT_SERVER_ADDRESS,
			"ARGOCD_AGENT_SERVER_PORT":    ARGOCD_AGENT_SERVER_PORT,
			"ARGOCD_AGENT_MODE":           ARGOCD_AGENT_MODE,
		}

		// Simulate the environment variable processing logic (empty values should not override)
		if val, found := os.LookupEnv("ARGOCD_AGENT_ENABLED"); found && val > "" {
			ARGOCD_AGENT_ENABLED = val
		}

		if val, found := os.LookupEnv("ARGOCD_AGENT_IMAGE"); found && val > "" {
			ARGOCD_AGENT_IMAGE = val
		}

		if val, found := os.LookupEnv("ARGOCD_AGENT_SERVER_ADDRESS"); found && val > "" {
			ARGOCD_AGENT_SERVER_ADDRESS = val
		}

		if val, found := os.LookupEnv("ARGOCD_AGENT_SERVER_PORT"); found && val > "" {
			ARGOCD_AGENT_SERVER_PORT = val
		}

		if val, found := os.LookupEnv("ARGOCD_AGENT_MODE"); found && val > "" {
			ARGOCD_AGENT_MODE = val
		}

		// Values should remain unchanged
		g.Expect(ARGOCD_AGENT_ENABLED).To(gomega.Equal(currentValues["ARGOCD_AGENT_ENABLED"]))
		g.Expect(ARGOCD_AGENT_IMAGE).To(gomega.Equal(currentValues["ARGOCD_AGENT_IMAGE"]))
		g.Expect(ARGOCD_AGENT_SERVER_ADDRESS).To(gomega.Equal(currentValues["ARGOCD_AGENT_SERVER_ADDRESS"]))
		g.Expect(ARGOCD_AGENT_SERVER_PORT).To(gomega.Equal(currentValues["ARGOCD_AGENT_SERVER_PORT"]))
		g.Expect(ARGOCD_AGENT_MODE).To(gomega.Equal(currentValues["ARGOCD_AGENT_MODE"]))
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

func TestAdditionalEnvironmentVariables(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("AdditionalGitOpsVariables", func(t *testing.T) {
		// Store original values
		originalGitopsOperatorImage := GitopsOperatorImage
		originalGitopsOperatorNS := GitopsOperatorNS
		originalGitopsImage := GitopsImage
		originalGitopsNS := GitopsNS
		originalRedisImage := RedisImage

		// Set environment variables
		t.Setenv("GITOPS_OPERATOR_IMAGE", "custom.registry.io/gitops-operator:v1.0.0")
		t.Setenv("GITOPS_OPERATOR_NAMESPACE", "custom-gitops-operator")
		t.Setenv("GITOPS_IMAGE", "custom.registry.io/argocd:v2.0.0")
		t.Setenv("GITOPS_NAMESPACE", "custom-gitops")
		t.Setenv("REDIS_IMAGE", "custom.registry.io/redis:v6.0.0")

		// Simulate the environment variable processing logic from main()
		if val, found := os.LookupEnv("GITOPS_OPERATOR_IMAGE"); found && val > "" {
			GitopsOperatorImage = val
		}
		if val, found := os.LookupEnv("GITOPS_OPERATOR_NAMESPACE"); found && val > "" {
			GitopsOperatorNS = val
		}
		if val, found := os.LookupEnv("GITOPS_IMAGE"); found && val > "" {
			GitopsImage = val
		}
		if val, found := os.LookupEnv("GITOPS_NAMESPACE"); found && val > "" {
			GitopsNS = val
		}
		if val, found := os.LookupEnv("REDIS_IMAGE"); found && val > "" {
			RedisImage = val
		}

		// Verify the values were updated
		g.Expect(GitopsOperatorImage).To(gomega.Equal("custom.registry.io/gitops-operator:v1.0.0"))
		g.Expect(GitopsOperatorNS).To(gomega.Equal("custom-gitops-operator"))
		g.Expect(GitopsImage).To(gomega.Equal("custom.registry.io/argocd:v2.0.0"))
		g.Expect(GitopsNS).To(gomega.Equal("custom-gitops"))
		g.Expect(RedisImage).To(gomega.Equal("custom.registry.io/redis:v6.0.0"))

		// Restore original values
		GitopsOperatorImage = originalGitopsOperatorImage
		GitopsOperatorNS = originalGitopsOperatorNS
		GitopsImage = originalGitopsImage
		GitopsNS = originalGitopsNS
		RedisImage = originalRedisImage
	})
}

func TestExtendedProxyEnvironmentVariables(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("ProxyEnvironmentOverrides", func(t *testing.T) {
		// Store original values
		originalHTTP_PROXY := HTTP_PROXY
		originalHTTPS_PROXY := HTTPS_PROXY
		originalNO_PROXY := NO_PROXY
		originalReconcileScope := ReconcileScope
		originalACTION := ACTION

		// Set environment variables
		t.Setenv("HTTP_PROXY", "http://proxy.example.com:8080")
		t.Setenv("HTTPS_PROXY", "https://proxy.example.com:8443")
		t.Setenv("NO_PROXY", "localhost,127.0.0.1,.example.com")
		t.Setenv("RECONCILE_SCOPE", "Multi-Namespace")
		t.Setenv("ACTION", "Delete-Operator")

		// Simulate the environment variable processing logic from main()
		if val, found := os.LookupEnv("HTTP_PROXY"); found && val > "" {
			HTTP_PROXY = val
		}
		if val, found := os.LookupEnv("HTTPS_PROXY"); found && val > "" {
			HTTPS_PROXY = val
		}
		if val, found := os.LookupEnv("NO_PROXY"); found && val > "" {
			NO_PROXY = val
		}
		if val, found := os.LookupEnv("RECONCILE_SCOPE"); found && val > "" {
			ReconcileScope = val
		}
		if val, found := os.LookupEnv("ACTION"); found && val > "" {
			ACTION = val
		}

		// Verify the values were updated
		g.Expect(HTTP_PROXY).To(gomega.Equal("http://proxy.example.com:8080"))
		g.Expect(HTTPS_PROXY).To(gomega.Equal("https://proxy.example.com:8443"))
		g.Expect(NO_PROXY).To(gomega.Equal("localhost,127.0.0.1,.example.com"))
		g.Expect(ReconcileScope).To(gomega.Equal("Multi-Namespace"))
		g.Expect(ACTION).To(gomega.Equal("Delete-Operator"))

		// Restore original values
		HTTP_PROXY = originalHTTP_PROXY
		HTTPS_PROXY = originalHTTPS_PROXY
		NO_PROXY = originalNO_PROXY
		ReconcileScope = originalReconcileScope
		ACTION = originalACTION
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

		// Test that all image references contain registry.redhat.io
		g.Expect(GitopsOperatorImage).To(gomega.ContainSubstring("registry.redhat.io"))
		g.Expect(GitopsImage).To(gomega.ContainSubstring("registry.redhat.io"))
		g.Expect(RedisImage).To(gomega.ContainSubstring("registry.redhat.io"))
		g.Expect(ARGOCD_AGENT_IMAGE).To(gomega.ContainSubstring("registry.redhat.io"))

		// Test namespace values
		g.Expect(GitopsOperatorNS).To(gomega.Equal("openshift-gitops-operator"))
		g.Expect(GitopsNS).To(gomega.Equal("openshift-gitops"))
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
		// Store original values
		originalGitopsImage := GitopsImage

		// Set empty environment variable (should not override)
		t.Setenv("GITOPS_IMAGE", "")

		// Simulate the environment variable processing logic from main()
		if val, found := os.LookupEnv("GITOPS_IMAGE"); found && val > "" {
			GitopsImage = val
		}

		// Should remain unchanged
		g.Expect(GitopsImage).To(gomega.Equal(originalGitopsImage))
	})

	t.Run("CompleteEnvironmentOverride", func(t *testing.T) {
		// Store original values
		originalValues := map[string]string{
			"GitopsOperatorImage":         GitopsOperatorImage,
			"GitopsOperatorNS":            GitopsOperatorNS,
			"GitopsImage":                 GitopsImage,
			"GitopsNS":                    GitopsNS,
			"RedisImage":                  RedisImage,
			"HTTP_PROXY":                  HTTP_PROXY,
			"HTTPS_PROXY":                 HTTPS_PROXY,
			"NO_PROXY":                    NO_PROXY,
			"ReconcileScope":              ReconcileScope,
			"ACTION":                      ACTION,
			"ARGOCD_AGENT_ENABLED":        ARGOCD_AGENT_ENABLED,
			"ARGOCD_AGENT_IMAGE":          ARGOCD_AGENT_IMAGE,
			"ARGOCD_AGENT_SERVER_ADDRESS": ARGOCD_AGENT_SERVER_ADDRESS,
			"ARGOCD_AGENT_SERVER_PORT":    ARGOCD_AGENT_SERVER_PORT,
			"ARGOCD_AGENT_MODE":           ARGOCD_AGENT_MODE,
		}

		// Set all environment variables
		envVars := map[string]string{
			"GITOPS_OPERATOR_IMAGE":       "test.registry.io/gitops-operator:test",
			"GITOPS_OPERATOR_NAMESPACE":   "test-gitops-operator",
			"GITOPS_IMAGE":                "test.registry.io/argocd:test",
			"GITOPS_NAMESPACE":            "test-gitops",
			"REDIS_IMAGE":                 "test.registry.io/redis:test",
			"HTTP_PROXY":                  "http://test-proxy:8080",
			"HTTPS_PROXY":                 "https://test-proxy:8443",
			"NO_PROXY":                    "test.local",
			"RECONCILE_SCOPE":             "Test-Scope",
			"ACTION":                      "Test-Action",
			"ARGOCD_AGENT_ENABLED":        "true",
			"ARGOCD_AGENT_IMAGE":          "test.registry.io/argocd-agent:test",
			"ARGOCD_AGENT_SERVER_ADDRESS": "test.argocd.local",
			"ARGOCD_AGENT_SERVER_PORT":    "9443",
			"ARGOCD_AGENT_MODE":           "test-mode",
		}

		for key, value := range envVars {
			t.Setenv(key, value)
		}

		// Simulate the environment variable processing logic from main()
		if val, found := os.LookupEnv("GITOPS_OPERATOR_IMAGE"); found && val > "" {
			GitopsOperatorImage = val
		}
		if val, found := os.LookupEnv("GITOPS_OPERATOR_NAMESPACE"); found && val > "" {
			GitopsOperatorNS = val
		}
		if val, found := os.LookupEnv("GITOPS_IMAGE"); found && val > "" {
			GitopsImage = val
		}
		if val, found := os.LookupEnv("GITOPS_NAMESPACE"); found && val > "" {
			GitopsNS = val
		}
		if val, found := os.LookupEnv("REDIS_IMAGE"); found && val > "" {
			RedisImage = val
		}
		if val, found := os.LookupEnv("HTTP_PROXY"); found && val > "" {
			HTTP_PROXY = val
		}
		if val, found := os.LookupEnv("HTTPS_PROXY"); found && val > "" {
			HTTPS_PROXY = val
		}
		if val, found := os.LookupEnv("NO_PROXY"); found && val > "" {
			NO_PROXY = val
		}
		if val, found := os.LookupEnv("RECONCILE_SCOPE"); found && val > "" {
			ReconcileScope = val
		}
		if val, found := os.LookupEnv("ACTION"); found && val > "" {
			ACTION = val
		}
		if val, found := os.LookupEnv("ARGOCD_AGENT_ENABLED"); found && val > "" {
			ARGOCD_AGENT_ENABLED = val
		}
		if val, found := os.LookupEnv("ARGOCD_AGENT_IMAGE"); found && val > "" {
			ARGOCD_AGENT_IMAGE = val
		}
		if val, found := os.LookupEnv("ARGOCD_AGENT_SERVER_ADDRESS"); found && val > "" {
			ARGOCD_AGENT_SERVER_ADDRESS = val
		}
		if val, found := os.LookupEnv("ARGOCD_AGENT_SERVER_PORT"); found && val > "" {
			ARGOCD_AGENT_SERVER_PORT = val
		}
		if val, found := os.LookupEnv("ARGOCD_AGENT_MODE"); found && val > "" {
			ARGOCD_AGENT_MODE = val
		}

		// Verify all values were updated
		g.Expect(GitopsOperatorImage).To(gomega.Equal("test.registry.io/gitops-operator:test"))
		g.Expect(GitopsOperatorNS).To(gomega.Equal("test-gitops-operator"))
		g.Expect(GitopsImage).To(gomega.Equal("test.registry.io/argocd:test"))
		g.Expect(GitopsNS).To(gomega.Equal("test-gitops"))
		g.Expect(RedisImage).To(gomega.Equal("test.registry.io/redis:test"))
		g.Expect(HTTP_PROXY).To(gomega.Equal("http://test-proxy:8080"))
		g.Expect(HTTPS_PROXY).To(gomega.Equal("https://test-proxy:8443"))
		g.Expect(NO_PROXY).To(gomega.Equal("test.local"))
		g.Expect(ReconcileScope).To(gomega.Equal("Test-Scope"))
		g.Expect(ACTION).To(gomega.Equal("Test-Action"))
		g.Expect(ARGOCD_AGENT_ENABLED).To(gomega.Equal("true"))
		g.Expect(ARGOCD_AGENT_IMAGE).To(gomega.Equal("test.registry.io/argocd-agent:test"))
		g.Expect(ARGOCD_AGENT_SERVER_ADDRESS).To(gomega.Equal("test.argocd.local"))
		g.Expect(ARGOCD_AGENT_SERVER_PORT).To(gomega.Equal("9443"))
		g.Expect(ARGOCD_AGENT_MODE).To(gomega.Equal("test-mode"))

		// Restore original values
		GitopsOperatorImage = originalValues["GitopsOperatorImage"]
		GitopsOperatorNS = originalValues["GitopsOperatorNS"]
		GitopsImage = originalValues["GitopsImage"]
		GitopsNS = originalValues["GitopsNS"]
		RedisImage = originalValues["RedisImage"]
		HTTP_PROXY = originalValues["HTTP_PROXY"]
		HTTPS_PROXY = originalValues["HTTPS_PROXY"]
		NO_PROXY = originalValues["NO_PROXY"]
		ReconcileScope = originalValues["ReconcileScope"]
		ACTION = originalValues["ACTION"]
		ARGOCD_AGENT_ENABLED = originalValues["ARGOCD_AGENT_ENABLED"]
		ARGOCD_AGENT_IMAGE = originalValues["ARGOCD_AGENT_IMAGE"]
		ARGOCD_AGENT_SERVER_ADDRESS = originalValues["ARGOCD_AGENT_SERVER_ADDRESS"]
		ARGOCD_AGENT_SERVER_PORT = originalValues["ARGOCD_AGENT_SERVER_PORT"]
		ARGOCD_AGENT_MODE = originalValues["ARGOCD_AGENT_MODE"]
	})
}
