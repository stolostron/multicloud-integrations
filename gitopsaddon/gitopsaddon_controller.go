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
	"embed"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

//nolint:all
//go:embed charts/openshift-gitops-operator/**
//go:embed charts/openshift-gitops-dependency/**
//go:embed charts/dep-crds/**
var ChartFS embed.FS

// GitOpsNamespace is exported from pkg/utils for backward compatibility
const GitOpsNamespace = utils.GitOpsNamespace

// GitOpsOperatorNamespace is exported from pkg/utils for backward compatibility
const GitOpsOperatorNamespace = utils.GitOpsOperatorNamespace

// GitopsAddonReconciler reconciles a openshift gitops operator
type GitopsAddonReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	Config                   *rest.Config
	Interval                 int
	GitopsOperatorImage      string
	GitopsImage              string
	RedisImage               string
	GitOpsServiceImage       string
	GitOpsConsolePluginImage string
	ReconcileScope           string
	HTTP_PROXY               string
	HTTPS_PROXY              string
	NO_PROXY                 string
	ArgoCDAgentEnabled       string
	ArgoCDAgentImage         string
	ArgoCDAgentServerAddress string
	ArgoCDAgentServerPort    string
	ArgoCDAgentMode          string
}

// GitopsAddonCleanupReconciler handles cleanup/uninstall of Gitops agent addon
type GitopsAddonCleanupReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	Config              *rest.Config
	GitopsOperatorImage string
	GitopsImage         string
	ArgoCDAgentImage    string
}

// SetupWithManager sets up the addon with the Manager
func SetupWithManager(mgr manager.Manager, interval int, gitopsOperatorImage,
	gitopsImage, redisImage, gitOpsServiceImage, gitOpsConsolePluginImage, reconcileScope,
	HTTP_PROXY, HTTPS_PROXY, NO_PROXY, argoCDAgentEnabled, argoCDAgentImage, argoCDAgentServerAddress, argoCDAgentServerPort, argoCDAgentMode string) error {
	reconciler := &GitopsAddonReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		Config:                   mgr.GetConfig(),
		Interval:                 interval,
		GitopsOperatorImage:      gitopsOperatorImage,
		GitopsImage:              gitopsImage,
		RedisImage:               redisImage,
		GitOpsServiceImage:       gitOpsServiceImage,
		GitOpsConsolePluginImage: gitOpsConsolePluginImage,
		ReconcileScope:           reconcileScope,
		HTTP_PROXY:               HTTP_PROXY,
		HTTPS_PROXY:              HTTPS_PROXY,
		NO_PROXY:                 NO_PROXY,
		ArgoCDAgentEnabled:       argoCDAgentEnabled,
		ArgoCDAgentImage:         argoCDAgentImage,
		ArgoCDAgentServerAddress: argoCDAgentServerAddress,
		ArgoCDAgentServerPort:    argoCDAgentServerPort,
		ArgoCDAgentMode:          argoCDAgentMode,
	}

	// Setup the secret controller to watch and copy ArgoCD agent client cert secrets
	if err := SetupSecretControllerWithManager(mgr); err != nil {
		return err
	}

	return mgr.Add(reconciler)
}

// SetupCleanupWithManager sets up the cleanup reconciler with the Manager
func SetupCleanupWithManager(mgr manager.Manager, gitopsOperatorImage, gitopsImage, argoCDAgentImage string) error {
	reconciler := &GitopsAddonCleanupReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		Config:              mgr.GetConfig(),
		GitopsOperatorImage: gitopsOperatorImage,
		GitopsImage:         gitopsImage,
		ArgoCDAgentImage:    argoCDAgentImage,
	}

	return mgr.Add(reconciler)
}

// Start implements manager.Runnable and blocks until the context is cancelled
func (r *GitopsAddonReconciler) Start(ctx context.Context) error {
	klog.Info("Starting Gitops Addon controller")

	// Perform initial reconciliation
	r.reconcile(ctx)

	// Run periodic reconciliation until context is cancelled
	wait.UntilWithContext(ctx, r.reconcile, time.Duration(r.Interval)*time.Second)

	klog.Info("Gitops Addon controller stopped")
	return nil
}

// reconcile performs the addon reconciliation logic
func (r *GitopsAddonReconciler) reconcile(ctx context.Context) {
	klog.V(2).Info("Reconciling Gitops Addon")

	// Check if the addon is paused (e.g., during cleanup)
	if IsPaused(ctx, r.Client) {
		klog.Info("GitOps addon is paused, skipping reconciliation")
		return
	}

	// Perform install/update
	if err := r.installOrUpdateOpenshiftGitops(); err != nil {
		klog.Errorf("Failed to reconcile Gitops Addon: %v", err)
		// Continue running - will retry on next interval
		return
	}
	klog.V(2).Info("Successfully reconciled Gitops Addon")
}

// Start implements manager.Runnable for cleanup and runs once then exits
func (r *GitopsAddonCleanupReconciler) Start(ctx context.Context) error {
	klog.Info("Starting Gitops Addon cleanup")

	// Perform cleanup (uninstall)
	if err := r.uninstallGitopsAgent(ctx); err != nil {
		klog.Errorf("Failed to cleanup Gitops Addon: %v", err)
		// Exit with error code 1
		os.Exit(1)
	}

	klog.Info("Successfully completed Gitops Addon cleanup")

	// Exit successfully after cleanup is done
	// This is needed for the cleanup job to complete properly
	os.Exit(0)

	return nil
}
