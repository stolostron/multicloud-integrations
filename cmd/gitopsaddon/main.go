/*
Copyright 2022.

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

package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/klog"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"open-cluster-management.io/multicloud-integrations/gitopsaddon"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// GitopsAddonAgentOptions for command line flag parsing
type GitopsAddonAgentOptions struct {
	MetricsAddr                 string
	LeaderElectionLeaseDuration time.Duration
	LeaderElectionRenewDeadline time.Duration
	LeaderElectionRetryPeriod   time.Duration
	SyncInterval                int
	Cleanup                     bool
}

var options = GitopsAddonAgentOptions{
	MetricsAddr:                 "",
	LeaderElectionLeaseDuration: 15 * time.Second,
	LeaderElectionRenewDeadline: 10 * time.Second,
	LeaderElectionRetryPeriod:   2 * time.Second,
	SyncInterval:                60,
	Cleanup:                     false,
}

var (
	scheme      = runtime.NewScheme()
	setupLog    = ctrl.Log.WithName("setup")
	metricsHost = "0.0.0.0"
	metricsPort = 8387
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
}

func main() {
	enableLeaderElection := false

	if _, err := rest.InClusterConfig(); err == nil {
		klog.Info("LeaderElection enabled as running in a cluster")

		enableLeaderElection = true
	} else {
		klog.Info("LeaderElection disabled as not running in a cluster")
	}

	flag.StringVar(
		&options.MetricsAddr,
		"metrics-addr",
		options.MetricsAddr,
		"The address the metric endpoint binds to.",
	)

	flag.DurationVar(
		&options.LeaderElectionLeaseDuration,
		"leader-election-lease-duration",
		options.LeaderElectionLeaseDuration,
		"The duration that non-leader candidates will wait after observing a leadership "+
			"renewal until attempting to acquire leadership of a led but unrenewed leader "+
			"slot. This is effectively the maximum duration that a leader can be stopped "+
			"before it is replaced by another candidate. This is only applicable if leader "+
			"election is enabled.",
	)

	flag.DurationVar(
		&options.LeaderElectionRenewDeadline,
		"leader-election-renew-deadline",
		options.LeaderElectionRenewDeadline,
		"The interval between attempts by the acting master to renew a leadership slot "+
			"before it stops leading. This must be less than or equal to the lease duration. "+
			"This is only applicable if leader election is enabled.",
	)

	flag.DurationVar(
		&options.LeaderElectionRetryPeriod,
		"leader-election-retry-period",
		options.LeaderElectionRetryPeriod,
		"The duration the clients should wait between attempting acquisition and renewal "+
			"of a leadership. This is only applicable if leader election is enabled.",
	)

	flag.IntVar(
		&options.SyncInterval,
		"sync-interval",
		options.SyncInterval,
		"The interval of housekeeping in seconds.",
	)

	flag.BoolVar(
		&options.Cleanup,
		"cleanup",
		options.Cleanup,
		"Run in cleanup mode (pre-delete hook).",
	)

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Create config from environment variables with defaults
	config := utils.NewGitOpsAddonConfig()

	// If cleanup mode is enabled, run cleanup and exit
	if options.Cleanup {
		setupLog.Info("Starting in cleanup mode")
		runCleanupMode(config)
		return
	}

	setupLog.Info("Leader election settings",
		"leaseDuration", options.LeaderElectionLeaseDuration,
		"renewDeadline", options.LeaderElectionRenewDeadline,
		"retryPeriod", options.LeaderElectionRetryPeriod,
		"syncInterval", options.SyncInterval,
		"HTTP_PROXY", config.HTTPProxy,
		"HTTPS_PROXY", config.HTTPSProxy,
		"NO_PROXY", config.NoProxy,
		"ArgoCDAgentEnabled", config.ArgoCDAgentEnabled,
		"ArgoCDAgentServerAddress", config.ArgoCDAgentServerAddress,
		"ArgoCDAgentServerPort", config.ArgoCDAgentServerPort,
		"ArgoCDAgentMode", config.ArgoCDAgentMode,
	)

	// Log all operator images
	setupLog.Info("Operator images configured:")
	for key, value := range config.OperatorImages {
		setupLog.Info("  Image", key, value)
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: fmt.Sprintf("%s:%d", metricsHost, metricsPort),
		},
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "gitops-addon-agent-leader.open-cluster-management.io",
		LeaseDuration:    &options.LeaderElectionLeaseDuration,
		RenewDeadline:    &options.LeaderElectionRenewDeadline,
		RetryPeriod:      &options.LeaderElectionRetryPeriod,
	})
	if err != nil {
		setupLog.Error(err, "unable to start gitops addon agent")
		os.Exit(1)
	}

	if err = gitopsaddon.SetupWithManager(mgr, options.SyncInterval, config); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "gitopsaddon")
		os.Exit(1)
	}

	setupLog.Info("starting gitops addon agent")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// runCleanupMode runs the application in cleanup mode for pre-delete hook
func runCleanupMode(config *utils.GitOpsAddonConfig) {
	setupLog.Info("Running cleanup mode")

	// Create a manager for cleanup mode (no leader election for cleanup job)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics in cleanup mode
		},
		LeaderElection: false, // Cleanup job doesn't need leader election
	})
	if err != nil {
		setupLog.Error(err, "unable to start cleanup manager")
		os.Exit(1)
	}

	// Setup the cleanup with the manager
	if err = gitopsaddon.SetupCleanupWithManager(mgr, config); err != nil {
		setupLog.Error(err, "unable to create cleanup controller")
		os.Exit(1)
	}

	setupLog.Info("starting cleanup manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running cleanup manager")
		os.Exit(1)
	}
}
