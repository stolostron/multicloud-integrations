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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestSetupWithManager(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name                     string
		interval                 int
		gitopsOperatorImage      string
		gitopsOperatorNS         string
		gitopsImage              string
		gitopsNS                 string
		redisImage               string
		gitOpsServiceImage       string
		gitOpsConsolePluginImage string
		reconcileScope           string
		httpProxy                string
		httpsProxy               string
		noProxy                  string
		uninstall                string
		argoCDAgentEnabled       string
		argoCDAgentImage         string
		argoCDAgentServerAddr    string
		argoCDAgentServerPort    string
		argoCDAgentMode          string
		expectError              bool
	}{
		{
			name:                     "successful_setup_with_defaults",
			interval:                 30,
			gitopsOperatorImage:      "test-operator:latest",
			gitopsOperatorNS:         "openshift-gitops-operator",
			gitopsImage:              "test-gitops:latest",
			gitopsNS:                 "openshift-gitops",
			redisImage:               "test-redis:latest",
			gitOpsServiceImage:       "test-gitops-service:latest",
			gitOpsConsolePluginImage: "test-gitops-console-plugin:latest",
			reconcileScope:           "Single-Namespace",
			httpProxy:                "",
			httpsProxy:               "",
			noProxy:                  "",
			uninstall:                "false",
			argoCDAgentEnabled:       "false",
			argoCDAgentImage:         "test-agent:latest",
			argoCDAgentServerAddr:    "",
			argoCDAgentServerPort:    "",
			argoCDAgentMode:          "managed",
			expectError:              false,
		},
		{
			name:                     "setup_with_proxy_settings",
			interval:                 60,
			gitopsOperatorImage:      "test-operator:v1.0.0",
			gitopsOperatorNS:         "custom-operator-ns",
			gitopsImage:              "test-gitops:v1.0.0",
			gitopsNS:                 "custom-gitops-ns",
			redisImage:               "test-redis:v1.0.0",
			gitOpsServiceImage:       "test-gitops-service:v1.0.0",
			gitOpsConsolePluginImage: "test-gitops-console-plugin:v1.0.0",
			reconcileScope:           "All-Namespaces",
			httpProxy:                "http://proxy:8080",
			httpsProxy:               "https://proxy:8080",
			noProxy:                  "localhost,127.0.0.1",
			uninstall:                "true",
			argoCDAgentEnabled:       "true",
			argoCDAgentImage:         "test-agent:v1.0.0",
			argoCDAgentServerAddr:    "argocd.example.com",
			argoCDAgentServerPort:    "443",
			argoCDAgentMode:          "autonomous",
			expectError:              false,
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
			err = SetupWithManager(mgr, tt.interval, tt.gitopsOperatorImage, tt.gitopsOperatorNS,
				tt.gitopsImage, tt.gitopsNS, tt.redisImage, tt.gitOpsServiceImage, tt.gitOpsConsolePluginImage, tt.reconcileScope,
				tt.httpProxy, tt.httpsProxy, tt.noProxy, tt.uninstall,
				tt.argoCDAgentEnabled, tt.argoCDAgentImage, tt.argoCDAgentServerAddr,
				tt.argoCDAgentServerPort, tt.argoCDAgentMode, "false")

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
				Client:              getTestEnv().Client,
				Config:              getTestEnv().Config,
				Scheme:              getTestEnv().Scheme,
				Interval:            tt.interval,
				GitopsOperatorImage: "test-operator:latest",
				GitopsOperatorNS:    "openshift-gitops-operator",
				GitopsImage:         "test-gitops:latest",
				GitopsNS:            "openshift-gitops",
				RedisImage:          "test-redis:latest",
				ReconcileScope:      "Single-Namespace",
				Uninstall:           "false",
				ArgoCDAgentEnabled:  "false",
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
		name                 string
		gitopsOperatorNS     string
		gitopsNS             string
		argoCDAgentEnabled   string
		uninstall            string
		argoCDAgentUninstall string
		description          string
	}{
		{
			name:                 "housekeeping_install_with_agent_disabled",
			gitopsOperatorNS:     "openshift-gitops-operator",
			gitopsNS:             "openshift-gitops",
			argoCDAgentEnabled:   "false",
			uninstall:            "false",
			argoCDAgentUninstall: "false",
			description:          "Normal install flow",
		},
		{
			name:                 "housekeeping_install_with_agent_enabled",
			gitopsOperatorNS:     "custom-operator-ns",
			gitopsNS:             "custom-gitops-ns",
			argoCDAgentEnabled:   "true",
			uninstall:            "false",
			argoCDAgentUninstall: "false",
			description:          "Install flow with agent enabled",
		},
		{
			name:                 "housekeeping_full_uninstall",
			gitopsOperatorNS:     "openshift-gitops-operator",
			gitopsNS:             "openshift-gitops",
			argoCDAgentEnabled:   "true",
			uninstall:            "true",
			argoCDAgentUninstall: "false",
			description:          "Full uninstall including agent",
		},
		{
			name:                 "housekeeping_agent_only_uninstall",
			gitopsOperatorNS:     "openshift-gitops-operator",
			gitopsNS:             "openshift-gitops",
			argoCDAgentEnabled:   "true",
			uninstall:            "false",
			argoCDAgentUninstall: "true",
			description:          "Agent-only uninstall",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test reconciler with mock client
			reconciler := &GitopsAddonReconciler{
				Client:                   getTestEnv().Client,
				Scheme:                   getTestEnv().Scheme,
				Config:                   getTestEnv().Config,
				Interval:                 30,
				GitopsOperatorImage:      "test-operator:latest",
				GitopsOperatorNS:         tt.gitopsOperatorNS,
				GitopsImage:              "test-gitops:latest",
				GitopsNS:                 tt.gitopsNS,
				RedisImage:               "test-redis:latest",
				GitOpsServiceImage:       "test-gitops-service:latest",
				GitOpsConsolePluginImage: "test-gitops-console-plugin:latest",
				ReconcileScope:           "Single-Namespace",
				Uninstall:                tt.uninstall,
				ArgoCDAgentEnabled:       tt.argoCDAgentEnabled,
				ArgoCDAgentUninstall:     tt.argoCDAgentUninstall,
			}

			// This test verifies that houseKeeping doesn't panic
			// The actual functionality is tested in install/uninstall tests
			g.Expect(func() {
				reconciler.houseKeeping()
			}).ToNot(gomega.Panic())
		})
	}
}

func TestGitopsAddonReconciler_Fields(t *testing.T) {
	g := gomega.NewWithT(t)

	reconciler := &GitopsAddonReconciler{
		Interval:                 30,
		GitopsOperatorImage:      "test-operator:latest",
		GitopsOperatorNS:         "openshift-gitops-operator",
		GitopsImage:              "test-gitops:latest",
		GitopsNS:                 "openshift-gitops",
		RedisImage:               "test-redis:latest",
		GitOpsServiceImage:       "test-gitops-service:latest",
		GitOpsConsolePluginImage: "test-gitops-console-plugin:latest",
		ReconcileScope:           "Single-Namespace",
		HTTP_PROXY:               "http://proxy:8080",
		HTTPS_PROXY:              "https://proxy:8080",
		NO_PROXY:                 "localhost,127.0.0.1",
		Uninstall:                "false",
		ArgoCDAgentEnabled:       "true",
		ArgoCDAgentImage:         "test-agent:latest",
		ArgoCDAgentServerAddress: "argocd.example.com",
		ArgoCDAgentServerPort:    "443",
		ArgoCDAgentMode:          "managed",
	}

	// Verify all fields are set correctly
	g.Expect(reconciler.Interval).To(gomega.Equal(30))
	g.Expect(reconciler.GitopsOperatorImage).To(gomega.Equal("test-operator:latest"))
	g.Expect(reconciler.GitopsOperatorNS).To(gomega.Equal("openshift-gitops-operator"))
	g.Expect(reconciler.GitopsImage).To(gomega.Equal("test-gitops:latest"))
	g.Expect(reconciler.GitopsNS).To(gomega.Equal("openshift-gitops"))
	g.Expect(reconciler.RedisImage).To(gomega.Equal("test-redis:latest"))
	g.Expect(reconciler.GitOpsServiceImage).To(gomega.Equal("test-gitops-service:latest"))
	g.Expect(reconciler.GitOpsConsolePluginImage).To(gomega.Equal("test-gitops-console-plugin:latest"))
	g.Expect(reconciler.ReconcileScope).To(gomega.Equal("Single-Namespace"))
	g.Expect(reconciler.HTTP_PROXY).To(gomega.Equal("http://proxy:8080"))
	g.Expect(reconciler.HTTPS_PROXY).To(gomega.Equal("https://proxy:8080"))
	g.Expect(reconciler.NO_PROXY).To(gomega.Equal("localhost,127.0.0.1"))
	g.Expect(reconciler.Uninstall).To(gomega.Equal("false"))
	g.Expect(reconciler.ArgoCDAgentEnabled).To(gomega.Equal("true"))
	g.Expect(reconciler.ArgoCDAgentImage).To(gomega.Equal("test-agent:latest"))
	g.Expect(reconciler.ArgoCDAgentServerAddress).To(gomega.Equal("argocd.example.com"))
	g.Expect(reconciler.ArgoCDAgentServerPort).To(gomega.Equal("443"))
	g.Expect(reconciler.ArgoCDAgentMode).To(gomega.Equal("managed"))
}
