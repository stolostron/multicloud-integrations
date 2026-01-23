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
	"testing"
)

func TestNewGitOpsAddonConfig_Defaults(t *testing.T) {
	// Clear any environment variables that might interfere
	envVarsToClean := AllConfigEnvVars()
	for _, envVar := range envVarsToClean {
		os.Unsetenv(envVar)
	}

	config := NewGitOpsAddonConfig()

	// Test that all default images are set
	for envKey, expectedValue := range DefaultOperatorImages {
		if got := config.OperatorImages[envKey]; got != expectedValue {
			t.Errorf("Expected default image for %s to be %s, got %s", envKey, expectedValue, got)
		}
	}

	// Test default proxy settings (should be empty)
	if config.HTTPProxy != "" {
		t.Errorf("Expected HTTPProxy to be empty, got %s", config.HTTPProxy)
	}
	if config.HTTPSProxy != "" {
		t.Errorf("Expected HTTPSProxy to be empty, got %s", config.HTTPSProxy)
	}
	if config.NoProxy != "" {
		t.Errorf("Expected NoProxy to be empty, got %s", config.NoProxy)
	}

	// Test default ArgoCD agent settings
	if config.ArgoCDAgentEnabled != false {
		t.Errorf("Expected ArgoCDAgentEnabled to be false, got %v", config.ArgoCDAgentEnabled)
	}
	if config.ArgoCDAgentMode != "managed" {
		t.Errorf("Expected ArgoCDAgentMode to be 'managed', got %s", config.ArgoCDAgentMode)
	}
	if config.ArgoCDAgentServerAddress != "" {
		t.Errorf("Expected ArgoCDAgentServerAddress to be empty, got %s", config.ArgoCDAgentServerAddress)
	}
	if config.ArgoCDAgentServerPort != "" {
		t.Errorf("Expected ArgoCDAgentServerPort to be empty, got %s", config.ArgoCDAgentServerPort)
	}
}

func TestNewGitOpsAddonConfig_EnvironmentOverrides(t *testing.T) {
	// Clear environment first
	envVarsToClean := AllConfigEnvVars()
	for _, envVar := range envVarsToClean {
		os.Unsetenv(envVar)
	}

	// Set test environment variables
	testImage := "test-registry.io/test-image:latest"
	os.Setenv(EnvGitOpsOperatorImage, testImage)
	os.Setenv(EnvHTTPProxy, "http://proxy.example.com:8080")
	os.Setenv(EnvHTTPSProxy, "https://proxy.example.com:8443")
	os.Setenv(EnvNoProxy, "localhost,127.0.0.1")
	os.Setenv(EnvArgoCDAgentEnabled, "true")
	os.Setenv(EnvArgoCDAgentServerAddress, "argocd-server.example.com")
	os.Setenv(EnvArgoCDAgentServerPort, "443")
	os.Setenv(EnvArgoCDAgentMode, "autonomous")

	defer func() {
		for _, envVar := range envVarsToClean {
			os.Unsetenv(envVar)
		}
	}()

	config := NewGitOpsAddonConfig()

	// Test image override
	if got := config.OperatorImages[EnvGitOpsOperatorImage]; got != testImage {
		t.Errorf("Expected image override for %s to be %s, got %s", EnvGitOpsOperatorImage, testImage, got)
	}

	// Test that non-overridden images still have defaults
	expectedDefault := DefaultOperatorImages[EnvArgoCDImage]
	if got := config.OperatorImages[EnvArgoCDImage]; got != expectedDefault {
		t.Errorf("Expected default image for %s to be %s, got %s", EnvArgoCDImage, expectedDefault, got)
	}

	// Test proxy overrides
	if config.HTTPProxy != "http://proxy.example.com:8080" {
		t.Errorf("Expected HTTPProxy to be 'http://proxy.example.com:8080', got %s", config.HTTPProxy)
	}
	if config.HTTPSProxy != "https://proxy.example.com:8443" {
		t.Errorf("Expected HTTPSProxy to be 'https://proxy.example.com:8443', got %s", config.HTTPSProxy)
	}
	if config.NoProxy != "localhost,127.0.0.1" {
		t.Errorf("Expected NoProxy to be 'localhost,127.0.0.1', got %s", config.NoProxy)
	}

	// Test ArgoCD agent overrides
	if config.ArgoCDAgentEnabled != true {
		t.Errorf("Expected ArgoCDAgentEnabled to be true, got %v", config.ArgoCDAgentEnabled)
	}
	if config.ArgoCDAgentServerAddress != "argocd-server.example.com" {
		t.Errorf("Expected ArgoCDAgentServerAddress to be 'argocd-server.example.com', got %s", config.ArgoCDAgentServerAddress)
	}
	if config.ArgoCDAgentServerPort != "443" {
		t.Errorf("Expected ArgoCDAgentServerPort to be '443', got %s", config.ArgoCDAgentServerPort)
	}
	if config.ArgoCDAgentMode != "autonomous" {
		t.Errorf("Expected ArgoCDAgentMode to be 'autonomous', got %s", config.ArgoCDAgentMode)
	}
}

func TestGitOpsAddonConfig_GetImage(t *testing.T) {
	config := &GitOpsAddonConfig{
		OperatorImages: map[string]string{
			EnvGitOpsOperatorImage: "custom-image:latest",
		},
	}

	// Test getting a set image
	if got := config.GetImage(EnvGitOpsOperatorImage); got != "custom-image:latest" {
		t.Errorf("Expected custom image, got %s", got)
	}

	// Test getting a non-set image (should return default)
	expectedDefault := DefaultOperatorImages[EnvArgoCDImage]
	if got := config.GetImage(EnvArgoCDImage); got != expectedDefault {
		t.Errorf("Expected default image for %s, got %s", EnvArgoCDImage, got)
	}

	// Test getting a completely unknown image (should return empty)
	if got := config.GetImage("UNKNOWN_IMAGE"); got != "" {
		t.Errorf("Expected empty string for unknown image, got %s", got)
	}
}

func TestAllConfigEnvVars(t *testing.T) {
	vars := AllConfigEnvVars()

	// Should have all image vars + 3 proxy vars + 4 ArgoCD agent vars
	expectedMinCount := len(DefaultOperatorImages) + 7

	if len(vars) < expectedMinCount {
		t.Errorf("Expected at least %d env vars, got %d", expectedMinCount, len(vars))
	}

	// Check that key variables are present
	requiredVars := []string{
		EnvGitOpsOperatorImage,
		EnvArgoCDImage,
		EnvHTTPProxy,
		EnvHTTPSProxy,
		EnvNoProxy,
		EnvArgoCDAgentEnabled,
		EnvArgoCDAgentServerAddress,
		EnvArgoCDAgentServerPort,
		EnvArgoCDAgentMode,
	}

	varSet := make(map[string]bool)
	for _, v := range vars {
		varSet[v] = true
	}

	for _, required := range requiredVars {
		if !varSet[required] {
			t.Errorf("Expected %s to be in AllConfigEnvVars()", required)
		}
	}
}

func TestSpokeConfigEnvVars(t *testing.T) {
	vars := SpokeConfigEnvVars()

	// Should have all image vars except hub-only + 3 proxy vars + 4 ArgoCD agent vars
	// Hub-only vars (like ARGOCD_PRINCIPAL_IMAGE) should not be included
	expectedMinCount := len(DefaultOperatorImages) - 1 + 7 // -1 for hub-only vars

	if len(vars) < expectedMinCount {
		t.Errorf("Expected at least %d spoke env vars, got %d", expectedMinCount, len(vars))
	}

	varSet := make(map[string]bool)
	for _, v := range vars {
		varSet[v] = true
	}

	// ARGOCD_PRINCIPAL_IMAGE should NOT be in spoke vars
	if varSet[EnvArgoCDPrincipalImage] {
		t.Errorf("ARGOCD_PRINCIPAL_IMAGE should not be in SpokeConfigEnvVars()")
	}

	// But other image vars should be present
	requiredVars := []string{
		EnvGitOpsOperatorImage,
		EnvArgoCDImage,
		EnvHTTPProxy,
		EnvArgoCDAgentEnabled,
	}
	for _, required := range requiredVars {
		if !varSet[required] {
			t.Errorf("Expected %s to be in SpokeConfigEnvVars()", required)
		}
	}
}

func TestIsHubOnlyEnvVar(t *testing.T) {
	// ARGOCD_PRINCIPAL_IMAGE should be hub-only
	if !IsHubOnlyEnvVar(EnvArgoCDPrincipalImage) {
		t.Error("Expected ARGOCD_PRINCIPAL_IMAGE to be hub-only")
	}

	// Other vars should not be hub-only
	if IsHubOnlyEnvVar(EnvGitOpsOperatorImage) {
		t.Error("Expected GITOPS_OPERATOR_IMAGE to NOT be hub-only")
	}

	if IsHubOnlyEnvVar(EnvArgoCDImage) {
		t.Error("Expected ARGOCD_IMAGE to NOT be hub-only")
	}
}

func TestDefaultOperatorImages(t *testing.T) {
	// Ensure all image constants have defaults
	requiredImages := []string{
		EnvGitOpsOperatorImage,
		EnvArgoCDImage,
		EnvArgoCDRepoServerImage,
		EnvArgoCDRedisImage,
		EnvArgoCDRedisHAImage,
		EnvArgoCDRedisHAProxyImage,
		EnvArgoCDDexImage,
		EnvBackendImage,
		EnvGitOpsConsolePlugin,
		EnvArgoCDExtensionImage,
		EnvArgoRolloutsImage,
		EnvArgoCDPrincipalImage,
		EnvArgoCDImageUpdaterImage,
	}

	for _, img := range requiredImages {
		if _, ok := DefaultOperatorImages[img]; !ok {
			t.Errorf("Expected default image for %s to exist", img)
		}
	}

	// Ensure defaults are not empty
	for envKey, value := range DefaultOperatorImages {
		if value == "" {
			t.Errorf("Default image for %s should not be empty", envKey)
		}
	}
}

func TestEnvVarConstants(t *testing.T) {
	// Test that the env var constants have expected values
	tests := []struct {
		constant string
		expected string
	}{
		{EnvGitOpsOperatorImage, "GITOPS_OPERATOR_IMAGE"},
		{EnvArgoCDImage, "ARGOCD_IMAGE"},
		{EnvHTTPProxy, "HTTP_PROXY"},
		{EnvHTTPSProxy, "HTTPS_PROXY"},
		{EnvNoProxy, "NO_PROXY"},
		{EnvArgoCDAgentEnabled, "ARGOCD_AGENT_ENABLED"},
		{EnvArgoCDAgentServerAddress, "ARGOCD_AGENT_SERVER_ADDRESS"},
		{EnvArgoCDAgentServerPort, "ARGOCD_AGENT_SERVER_PORT"},
		{EnvArgoCDAgentMode, "ARGOCD_AGENT_MODE"},
	}

	for _, tt := range tests {
		if tt.constant != tt.expected {
			t.Errorf("Expected constant value %s, got %s", tt.expected, tt.constant)
		}
	}
}

func TestNewGitOpsAddonConfig_AgentEnabledFalse(t *testing.T) {
	// Clear environment first
	envVarsToClean := AllConfigEnvVars()
	for _, envVar := range envVarsToClean {
		os.Unsetenv(envVar)
	}

	// Set ARGOCD_AGENT_ENABLED to "false" explicitly
	os.Setenv(EnvArgoCDAgentEnabled, "false")
	defer os.Unsetenv(EnvArgoCDAgentEnabled)

	config := NewGitOpsAddonConfig()

	// Agent should still be disabled (only "true" enables it)
	if config.ArgoCDAgentEnabled != false {
		t.Errorf("Expected ArgoCDAgentEnabled to be false when set to 'false', got %v", config.ArgoCDAgentEnabled)
	}
}

func TestNewGitOpsAddonConfig_EmptyEnvVars(t *testing.T) {
	// Clear environment first
	envVarsToClean := AllConfigEnvVars()
	for _, envVar := range envVarsToClean {
		os.Unsetenv(envVar)
	}

	// Set empty string values (should use defaults)
	os.Setenv(EnvGitOpsOperatorImage, "")
	os.Setenv(EnvArgoCDAgentServerAddress, "")
	defer func() {
		os.Unsetenv(EnvGitOpsOperatorImage)
		os.Unsetenv(EnvArgoCDAgentServerAddress)
	}()

	config := NewGitOpsAddonConfig()

	// Empty env var should still use defaults for images
	expectedDefault := DefaultOperatorImages[EnvGitOpsOperatorImage]
	if config.OperatorImages[EnvGitOpsOperatorImage] != expectedDefault {
		t.Errorf("Expected default image when env is empty, got %s", config.OperatorImages[EnvGitOpsOperatorImage])
	}

	// Empty string for non-image vars should remain empty
	if config.ArgoCDAgentServerAddress != "" {
		t.Errorf("Expected empty server address, got %s", config.ArgoCDAgentServerAddress)
	}
}

func TestAllConfigEnvVars_ContainsAllImages(t *testing.T) {
	vars := AllConfigEnvVars()
	varSet := make(map[string]bool)
	for _, v := range vars {
		varSet[v] = true
	}

	// All default operator images should be in the list
	for envKey := range DefaultOperatorImages {
		if !varSet[envKey] {
			t.Errorf("Expected AllConfigEnvVars to contain %s", envKey)
		}
	}
}

func TestSpokeConfigEnvVars_CountCorrect(t *testing.T) {
	spokeVars := SpokeConfigEnvVars()
	allVars := AllConfigEnvVars()

	// Spoke vars should have fewer entries than all vars (missing hub-only)
	if len(spokeVars) >= len(allVars) {
		t.Errorf("Expected SpokeConfigEnvVars (%d) to be less than AllConfigEnvVars (%d)", len(spokeVars), len(allVars))
	}

	// The difference should be exactly the number of hub-only vars
	hubOnlyCount := 0
	for range hubOnlyEnvVars {
		hubOnlyCount++
	}
	expectedDiff := hubOnlyCount
	actualDiff := len(allVars) - len(spokeVars)
	if actualDiff != expectedDiff {
		t.Errorf("Expected difference of %d hub-only vars, got %d", expectedDiff, actualDiff)
	}
}
