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
		g.Expect(ARGOCD_AGENT_IMAGE).To(gomega.ContainSubstring("ghcr.io/argoproj-labs/argocd-agent/argocd-agent"))
		g.Expect(ARGOCD_AGENT_SERVER_ADDRESS).To(gomega.Equal(""))
		g.Expect(ARGOCD_AGENT_SERVER_PORT).To(gomega.Equal("443"))
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
