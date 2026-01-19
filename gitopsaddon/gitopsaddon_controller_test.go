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

package gitopsaddon

import (
	"context"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// createTestConfig creates a GitOpsAddonConfig for testing
func createTestConfig(operatorImage, argocdImage, redisImage string, agentEnabled bool) *utils.GitOpsAddonConfig {
	return &utils.GitOpsAddonConfig{
		OperatorImages: map[string]string{
			utils.EnvGitOpsOperatorImage:     operatorImage,
			utils.EnvArgoCDImage:             argocdImage,
			utils.EnvArgoCDRepoServerImage:   argocdImage,
			utils.EnvArgoCDRedisImage:        redisImage,
			utils.EnvArgoCDRedisHAImage:      redisImage,
			utils.EnvArgoCDRedisHAProxyImage: "test-haproxy:latest",
			utils.EnvArgoCDDexImage:          "test-dex:latest",
			utils.EnvBackendImage:            "test-backend:latest",
			utils.EnvGitOpsConsolePlugin:     "test-console-plugin:latest",
			utils.EnvArgoCDExtensionImage:    "test-extension:latest",
			utils.EnvArgoRolloutsImage:       "test-rollouts:latest",
			utils.EnvArgoCDPrincipalImage:    "test-agent:latest",
			utils.EnvArgoCDImageUpdaterImage: "test-image-updater:latest",
		},
		ArgoCDAgentEnabled: agentEnabled,
		ArgoCDAgentMode:    "managed",
	}
}

func TestSetupWithManager(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name        string
		interval    int
		config      *utils.GitOpsAddonConfig
		expectError bool
	}{
		{
			name:        "successful_setup_with_defaults",
			interval:    30,
			config:      createTestConfig("test-operator:latest", "test-argocd:latest", "test-redis:latest", false),
			expectError: false,
		},
		{
			name:     "setup_with_proxy_settings",
			interval: 60,
			config: func() *utils.GitOpsAddonConfig {
				c := createTestConfig("test-operator:v1.0.0", "test-argocd:v1.0.0", "test-redis:v1.0.0", true)
				c.HTTPProxy = "http://proxy:8080"
				c.HTTPSProxy = "https://proxy:8080"
				c.NoProxy = "localhost,127.0.0.1"
				c.ArgoCDAgentServerAddress = "argocd.example.com"
				c.ArgoCDAgentServerPort = "443"
				c.ArgoCDAgentMode = "autonomous"
				return c
			}(),
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test scheme
			scheme := runtime.NewScheme()
			err := clientgoscheme.AddToScheme(scheme)
			g.Expect(err).ToNot(gomega.HaveOccurred())

			// Create a test manager
			mgr, err := manager.New(getTestEnv().Config, manager.Options{
				Scheme: scheme,
				Metrics: metricsserver.Options{
					BindAddress: "0", // Disable metrics server for tests
				},
				LeaderElection: false,
			})
			g.Expect(err).ToNot(gomega.HaveOccurred())

			// Test SetupWithManager
			err = SetupWithManager(mgr, tt.interval, tt.config)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}
		})
	}
}

func TestGitopsAddonReconciler_Start(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name         string
		interval     int
		expectError  bool
		contextDelay time.Duration
	}{
		{
			name:         "start_successful",
			interval:     1, // 1 second for faster test
			expectError:  false,
			contextDelay: 100 * time.Millisecond,
		},
		{
			name:         "start_with_cancellation",
			interval:     1,
			expectError:  false,
			contextDelay: 50 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test reconciler
			reconciler := &GitopsAddonReconciler{
				Client:      getTestEnv().Client,
				Config:      getTestEnv().Config,
				Scheme:      getTestEnv().Scheme,
				Interval:    tt.interval,
				AddonConfig: createTestConfig("test-operator:latest", "test-argocd:latest", "test-redis:latest", false),
			}

			// Create a context that will be cancelled
			ctx, cancel := context.WithCancel(context.Background())

			// Start the reconciler
			errCh := make(chan error, 1)
			go func() {
				errCh <- reconciler.Start(ctx)
			}()

			// Wait for the specified delay, then cancel
			time.Sleep(tt.contextDelay)
			cancel()

			// Wait for Start to return
			select {
			case err := <-errCh:
				if tt.expectError {
					g.Expect(err).To(gomega.HaveOccurred())
				} else {
					g.Expect(err).ToNot(gomega.HaveOccurred())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Start method did not return within timeout")
			}
		})
	}
}

func TestGitopsAddonReconciler_houseKeeping(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name               string
		argoCDAgentEnabled bool
		description        string
	}{
		{
			name:               "housekeeping_install_with_agent_disabled",
			argoCDAgentEnabled: false,
			description:        "Normal install flow",
		},
		{
			name:               "housekeeping_install_with_agent_enabled",
			argoCDAgentEnabled: true,
			description:        "Install flow with agent enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test reconciler with mock client
			reconciler := &GitopsAddonReconciler{
				Client:      getTestEnv().Client,
				Scheme:      getTestEnv().Scheme,
				Config:      getTestEnv().Config,
				Interval:    30,
				AddonConfig: createTestConfig("test-operator:latest", "test-argocd:latest", "test-redis:latest", tt.argoCDAgentEnabled),
			}

			// This test verifies that reconcile doesn't panic
			// The actual functionality is tested in install/uninstall tests
			g.Expect(func() {
				reconciler.reconcile(context.TODO())
			}).ToNot(gomega.Panic())
		})
	}
}

func TestGitopsAddonReconciler_Config(t *testing.T) {
	g := gomega.NewWithT(t)

	config := &utils.GitOpsAddonConfig{
		OperatorImages: map[string]string{
			utils.EnvGitOpsOperatorImage:     "test-operator:latest",
			utils.EnvArgoCDImage:             "test-argocd:latest",
			utils.EnvArgoCDRepoServerImage:   "test-argocd:latest",
			utils.EnvArgoCDRedisImage:        "test-redis:latest",
			utils.EnvArgoCDRedisHAImage:      "test-redis:latest",
			utils.EnvArgoCDRedisHAProxyImage: "test-haproxy:latest",
			utils.EnvArgoCDDexImage:          "test-dex:latest",
			utils.EnvBackendImage:            "test-backend:latest",
			utils.EnvGitOpsConsolePlugin:     "test-console-plugin:latest",
			utils.EnvArgoCDExtensionImage:    "test-extension:latest",
			utils.EnvArgoRolloutsImage:       "test-rollouts:latest",
			utils.EnvArgoCDPrincipalImage:    "test-agent:latest",
			utils.EnvArgoCDImageUpdaterImage: "test-image-updater:latest",
		},
		HTTPProxy:                "http://proxy:8080",
		HTTPSProxy:               "https://proxy:8080",
		NoProxy:                  "localhost,127.0.0.1",
		ArgoCDAgentEnabled:       true,
		ArgoCDAgentServerAddress: "argocd.example.com",
		ArgoCDAgentServerPort:    "443",
		ArgoCDAgentMode:          "managed",
	}

	reconciler := &GitopsAddonReconciler{
		Interval:    30,
		AddonConfig: config,
	}

	// Verify config is accessible through reconciler
	g.Expect(reconciler.Interval).To(gomega.Equal(30))
	g.Expect(reconciler.AddonConfig.OperatorImages[utils.EnvGitOpsOperatorImage]).To(gomega.Equal("test-operator:latest"))
	g.Expect(reconciler.AddonConfig.OperatorImages[utils.EnvArgoCDImage]).To(gomega.Equal("test-argocd:latest"))
	g.Expect(reconciler.AddonConfig.OperatorImages[utils.EnvArgoCDRedisImage]).To(gomega.Equal("test-redis:latest"))
	g.Expect(reconciler.AddonConfig.OperatorImages[utils.EnvBackendImage]).To(gomega.Equal("test-backend:latest"))
	g.Expect(reconciler.AddonConfig.OperatorImages[utils.EnvGitOpsConsolePlugin]).To(gomega.Equal("test-console-plugin:latest"))
	g.Expect(reconciler.AddonConfig.HTTPProxy).To(gomega.Equal("http://proxy:8080"))
	g.Expect(reconciler.AddonConfig.HTTPSProxy).To(gomega.Equal("https://proxy:8080"))
	g.Expect(reconciler.AddonConfig.NoProxy).To(gomega.Equal("localhost,127.0.0.1"))
	g.Expect(reconciler.AddonConfig.ArgoCDAgentEnabled).To(gomega.BeTrue())
	g.Expect(reconciler.AddonConfig.OperatorImages[utils.EnvArgoCDPrincipalImage]).To(gomega.Equal("test-agent:latest"))
	g.Expect(reconciler.AddonConfig.ArgoCDAgentServerAddress).To(gomega.Equal("argocd.example.com"))
	g.Expect(reconciler.AddonConfig.ArgoCDAgentServerPort).To(gomega.Equal("443"))
	g.Expect(reconciler.AddonConfig.ArgoCDAgentMode).To(gomega.Equal("managed"))
}

func TestGitOpsAddonConfig_OperatorImages(t *testing.T) {
	g := gomega.NewWithT(t)

	config := utils.NewGitOpsAddonConfig()

	// Verify default images are set
	g.Expect(config.OperatorImages[utils.EnvGitOpsOperatorImage]).ToNot(gomega.BeEmpty())
	g.Expect(config.OperatorImages[utils.EnvArgoCDImage]).ToNot(gomega.BeEmpty())
	g.Expect(config.OperatorImages[utils.EnvArgoCDRedisImage]).ToNot(gomega.BeEmpty())
	g.Expect(config.OperatorImages[utils.EnvArgoCDPrincipalImage]).ToNot(gomega.BeEmpty())

	// Verify unknown key returns empty
	g.Expect(config.OperatorImages["UNKNOWN_KEY"]).To(gomega.BeEmpty())
}
