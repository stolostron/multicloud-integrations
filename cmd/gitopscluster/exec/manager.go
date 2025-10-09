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

package exec

import (
	"context"
	"fmt"
	"os"

	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
	"open-cluster-management.io/multicloud-integrations/pkg/apis"
	"open-cluster-management.io/multicloud-integrations/pkg/controller"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Change below variables to serve metrics on different host or port.
var (
	metricsHost = "0.0.0.0"
	metricsPort = 8388
	GitopsNS    = "openshift-gitops"
)

// RunManager starts the actual manager
func RunManager() {
	enableLeaderElection := false

	if _, err := rest.InClusterConfig(); err == nil {
		klog.Info("LeaderElection enabled as running in a cluster")

		enableLeaderElection = true
	} else {
		klog.Info("LeaderElection disabled as not running in a cluster")
	}

	klog.Info("Leader election settings",
		"leaseDuration", options.LeaderElectionLeaseDuration,
		"renewDeadline", options.LeaderElectionRenewDeadline,
		"retryPeriod", options.LeaderElectionRetryPeriod)

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Metrics: metricsserver.Options{
			BindAddress: fmt.Sprintf("%s:%d", metricsHost, metricsPort),
		},
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "multicloud-operators-gitopscluster-leader.open-cluster-management.io",
		LeaderElectionNamespace: "kube-system",
		NewCache: func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
			opts.ByObject = map[client.Object]cache.ByObject{
				&v1.Secret{}: {
					Label: labels.SelectorFromSet(labels.Set{"apps.open-cluster-management.io/cluster-name,argocd.argoproj.io/secret-type": "cluster"}),
				}}
			return cache.New(config, opts)
		},
		LeaseDuration: &options.LeaderElectionLeaseDuration,
		RenewDeadline: &options.LeaderElectionRenewDeadline,
		RetryPeriod:   &options.LeaderElectionRetryPeriod,
		NewClient:     NewNonCachingClient,
	})

	if err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	klog.Info("Registering GitOpsCluster components.")

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	if err := v1.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	if err := clusterv1beta1.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	if err := clusterv1.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	if err := authv1beta1.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	if err := rbacv1.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	// Setup Addon Scheme for manager
	if err := addonv1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	// Setup ManifestWork Scheme for manager
	if err := workv1.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	// Ensure openshift-gitops namespace exists
	kubeClient := mgr.GetClient()
	if err := ensureOpenShiftGitOpsNamespace(kubeClient); err != nil {
		klog.Error(err, "failed to ensure openshift-gitops namespace")
		os.Exit(1)
	}

	// Ensure addon-manager-controller RBAC resources exist
	if err := ensureAddonManagerRBAC(kubeClient); err != nil {
		klog.Error(err, "failed to ensure addon-manager-controller RBAC resources")
		os.Exit(1)
	}

	// Ensure argocd-agent-ca secret exists in openshift-gitops namespace
	if err := ensureArgoCDAgentCASecret(kubeClient); err != nil {
		klog.Error(err, "failed to ensure argocd-agent-ca secret")
		os.Exit(1)
	}

	// Setup all Controllers
	if err := controller.AddGitOpsClusterToManager(mgr); err != nil {
		klog.Error(err, "")
		os.Exit(1)
	}

	sig := signals.SetupSignalHandler()

	klog.Info("Detecting ACM cluster API service...")
	utils.DetectClusterRegistry(sig, mgr.GetAPIReader())

	klog.Info("Detecting ACM managedServiceAccount API...")
	utils.DetectManagedServiceAccount(sig, mgr.GetAPIReader())

	klog.Info("Starting the Cmd.")

	// Start the Cmd
	if err := mgr.Start(sig); err != nil {
		klog.Error(err, "Manager exited non-zero")
		os.Exit(1)
	}
}

func NewNonCachingClient(config *rest.Config, options client.Options) (client.Client, error) {
	return client.New(config, client.Options{Scheme: scheme.Scheme})
}

// ensureOpenShiftGitOpsNamespace creates the openshift-gitops namespace if it doesn't exist
func ensureOpenShiftGitOpsNamespace(kubeClient client.Client) error {
	namespace := &v1.Namespace{}
	namespaceName := types.NamespacedName{Name: GitopsNS}

	err := kubeClient.Get(context.TODO(), namespaceName, namespace)
	if err == nil {
		klog.Info("openshift-gitops namespace already exists")
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check openshift-gitops namespace: %w", err)
	}

	klog.Info("Creating openshift-gitops namespace...")

	newNamespace := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: GitopsNS,
		},
	}

	err = kubeClient.Create(context.TODO(), newNamespace)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			klog.Info("openshift-gitops namespace was created by another process")
			return nil
		}

		return fmt.Errorf("failed to create openshift-gitops namespace: %w", err)
	}

	klog.Info("Successfully created openshift-gitops namespace")

	return nil
}

// ensureAddonManagerRBAC creates the addon-manager-controller RBAC resources if they don't exist
func ensureAddonManagerRBAC(kubeClient client.Client) error {
	// Create Role
	err := ensureAddonManagerRole(kubeClient)
	if err != nil {
		return fmt.Errorf("failed to ensure addon-manager-controller role: %w", err)
	}

	// Create RoleBinding
	err = ensureAddonManagerRoleBinding(kubeClient)
	if err != nil {
		return fmt.Errorf("failed to ensure addon-manager-controller rolebinding: %w", err)
	}

	return nil
}

// ensureAddonManagerRole creates the addon-manager-controller role if it doesn't exist
func ensureAddonManagerRole(kubeClient client.Client) error {
	role := &rbacv1.Role{}
	roleName := types.NamespacedName{
		Name:      "addon-manager-controller-role",
		Namespace: GitopsNS,
	}

	err := kubeClient.Get(context.TODO(), roleName, role)
	if err == nil {
		klog.Info("addon-manager-controller-role already exists")
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check addon-manager-controller-role: %w", err)
	}

	klog.Info("Creating addon-manager-controller-role...")

	newRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "addon-manager-controller-role",
			Namespace: GitopsNS,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopsaddon": "true",
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}

	err = kubeClient.Create(context.TODO(), newRole)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			klog.Info("addon-manager-controller-role was created by another process")
			return nil
		}

		return fmt.Errorf("failed to create addon-manager-controller-role: %w", err)
	}

	klog.Info("Successfully created addon-manager-controller-role")

	return nil
}

// ensureAddonManagerRoleBinding creates the addon-manager-controller rolebinding if it doesn't exist
func ensureAddonManagerRoleBinding(kubeClient client.Client) error {
	roleBinding := &rbacv1.RoleBinding{}
	roleBindingName := types.NamespacedName{
		Name:      "addon-manager-controller-rolebinding",
		Namespace: GitopsNS,
	}

	err := kubeClient.Get(context.TODO(), roleBindingName, roleBinding)
	if err == nil {
		klog.Info("addon-manager-controller-rolebinding already exists")
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check addon-manager-controller-rolebinding: %w", err)
	}

	klog.Info("Creating addon-manager-controller-rolebinding...")

	newRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "addon-manager-controller-rolebinding",
			Namespace: GitopsNS,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopsaddon": "true",
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "addon-manager-controller-sa",
				Namespace: utils.GetComponentNamespace("open-cluster-management") + "-hub",
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     "addon-manager-controller-role",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	err = kubeClient.Create(context.TODO(), newRoleBinding)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			klog.Info("addon-manager-controller-rolebinding was created by another process")
			return nil
		}

		return fmt.Errorf("failed to create addon-manager-controller-rolebinding: %w", err)
	}

	klog.Info("Successfully created addon-manager-controller-rolebinding")

	return nil
}

// ensureArgoCDAgentCASecret ensures the argocd-agent-ca secret exists in openshift-gitops namespace
// by copying it from the multicluster-operators-application-svc-ca secret in open-cluster-management namespace
func ensureArgoCDAgentCASecret(kubeClient client.Client) error {
	// Check if argocd-agent-ca secret already exists
	targetSecret := &v1.Secret{}
	targetSecretName := types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: GitopsNS,
	}

	err := kubeClient.Get(context.TODO(), targetSecretName, targetSecret)
	if err == nil {
		klog.Info("argocd-agent-ca secret already exists in openshift-gitops namespace")
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check argocd-agent-ca secret: %w", err)
	}

	klog.Info("argocd-agent-ca secret not found, copying from source secret...")

	// Get the source secret from open-cluster-management namespace
	sourceSecret := &v1.Secret{}
	sourceSecretName := types.NamespacedName{
		Name:      "multicluster-operators-application-svc-ca",
		Namespace: utils.GetComponentNamespace("open-cluster-management"),
	}

	err = kubeClient.Get(context.TODO(), sourceSecretName, sourceSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("source secret multicluster-operators-application-svc-ca not found in open-cluster-management namespace - ensure OCM is properly installed")
		}

		return fmt.Errorf("failed to get source secret: %w", err)
	}

	// Create the new secret with modified name and namespace
	newSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: GitopsNS,
		},
		Type: sourceSecret.Type,
		Data: sourceSecret.Data,
	}

	// Copy labels and annotations from source secret
	if sourceSecret.Labels != nil {
		newSecret.Labels = make(map[string]string)
		for k, v := range sourceSecret.Labels {
			newSecret.Labels[k] = v
		}
	} else {
		newSecret.Labels = make(map[string]string)
	}

	if sourceSecret.Annotations != nil {
		newSecret.Annotations = make(map[string]string)
		for k, v := range sourceSecret.Annotations {
			newSecret.Annotations[k] = v
		}
	} else {
		newSecret.Annotations = make(map[string]string)
	}

	err = kubeClient.Create(context.TODO(), newSecret)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			klog.Info("argocd-agent-ca secret was created by another process")
			return nil
		}

		return fmt.Errorf("failed to create argocd-agent-ca secret: %w", err)
	}

	klog.Info("Successfully created argocd-agent-ca secret in openshift-gitops namespace")

	return nil
}
