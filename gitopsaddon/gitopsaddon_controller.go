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
var ChartFS embed.FS

//go:embed routes-openshift-crd/**
var RouteCRDFS embed.FS

// GitOpsNamespace is exported from pkg/utils for backward compatibility
const GitOpsNamespace = utils.GitOpsNamespace

// GitOpsOperatorNamespace is exported from pkg/utils for backward compatibility
const GitOpsOperatorNamespace = utils.GitOpsOperatorNamespace

// GitopsAddonReconciler reconciles a openshift gitops operator
type GitopsAddonReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Config    *rest.Config
	Interval  int
	APIReader client.Reader // uncached reader for startup operations

	// Config holds all image and configuration settings
	AddonConfig *utils.GitOpsAddonConfig
}

// GitopsAddonCleanupReconciler handles cleanup/uninstall of Gitops agent addon
type GitopsAddonCleanupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Config *rest.Config

	// Config holds all image and configuration settings
	AddonConfig *utils.GitOpsAddonConfig
}

// SetupWithManager sets up the addon with the Manager
func SetupWithManager(mgr manager.Manager, interval int, config *utils.GitOpsAddonConfig) error {
	reconciler := &GitopsAddonReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Config:      mgr.GetConfig(),
		Interval:    interval,
		APIReader:   mgr.GetAPIReader(),
		AddonConfig: config,
	}

	// Setup the secret controller to watch and copy ArgoCD agent client cert secrets
	if err := SetupSecretControllerWithManager(mgr); err != nil {
		return err
	}

	return mgr.Add(reconciler)
}

// SetupCleanupWithManager sets up the cleanup reconciler with the Manager
func SetupCleanupWithManager(mgr manager.Manager, config *utils.GitOpsAddonConfig) error {
	reconciler := &GitopsAddonCleanupReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Config:      mgr.GetConfig(),
		AddonConfig: config,
	}

	return mgr.Add(reconciler)
}

// Start implements manager.Runnable and blocks until the context is cancelled
func (r *GitopsAddonReconciler) Start(ctx context.Context) error {
	klog.Info("Starting Gitops Addon controller")

	// Clear any stale pause marker from a previous cleanup cycle.
	// The cleanup Job creates a pause marker ConfigMap to prevent reconciliation during cleanup.
	// On OCP (OLM mode), the Deployment runs in openshift-operators but the marker is in
	// open-cluster-management-agent-addon, so the owner reference can't be set and
	// the marker is never garbage collected. Since the controller is starting fresh,
	// any existing marker is stale and should be removed.
	// We use the uncached APIReader here because the controller-runtime informer cache
	// may not have synced ConfigMaps yet at startup, causing the cached Get to miss
	// markers that actually exist.
	ClearStalePauseMarker(ctx, r.Client, r.APIReader)

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
