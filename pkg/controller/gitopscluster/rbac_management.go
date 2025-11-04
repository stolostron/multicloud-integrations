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

package gitopscluster

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	RoleSuffix       = "-applicationset-controller-placement"
	configMapNameOld = "acm-placementrule"
	configMapNameNew = "acm-placement"
)

// getRoleDuck creates a Role for ApplicationSet controller to access placement resources
func getRoleDuck(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: namespace + RoleSuffix, Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"apps.open-cluster-management.io"},
				Resources: []string{"placementrules"},
				Verbs:     []string{"list"},
			},
			{
				APIGroups: []string{"cluster.open-cluster-management.io"},
				Resources: []string{"placementdecisions"},
				Verbs:     []string{"list"},
			},
		},
	}
}

// getAppSetServiceAccountName finds the ApplicationSet controller service account name
func (r *ReconcileGitOpsCluster) getAppSetServiceAccountName(namespace string) string {
	saName := "openshift-gitops-applicationset-controller" // if every attempt fails, use this name

	var labels = [2]map[string]string{
		{"app.kubernetes.io/part-of": "argocd-applicationset"},
		{"app.kubernetes.io/part-of": "argocd"},
	}

	// First, try to get the applicationSet controller service account by label
	saList := &v1.ServiceAccountList{}
	listopts := &client.ListOptions{Namespace: namespace}

	for _, label := range labels {
		saSelector := &metav1.LabelSelector{
			MatchLabels: label,
		}

		saSelectionLabel, err := utils.ConvertLabels(saSelector)

		if err != nil {
			klog.Error("Failed to convert managed cluster secret selector, err:", err)
		} else {
			listopts.LabelSelector = saSelectionLabel
		}

		err = r.List(context.TODO(), saList, listopts)

		if err != nil {
			klog.Error("Failed to get service account list, err:", err) // Just return the default SA name

			return saName
		}

		// find the SA name that ends with -applicationset-controller
		for _, sa := range saList.Items {
			if strings.HasSuffix(sa.Name, "-applicationset-controller") {
				klog.Info("found the application set controller service account name from list: " + sa.Name)

				return sa.Name
			}
		}
	}

	klog.Warning("could not find application set controller service account name")

	return saName
}

// getRoleBindingDuck creates a RoleBinding for ApplicationSet controller
func (r *ReconcileGitOpsCluster) getRoleBindingDuck(namespace string) *rbacv1.RoleBinding {
	saName := r.getAppSetServiceAccountName(namespace)

	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: namespace + RoleSuffix, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     namespace + RoleSuffix,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: namespace,
			},
		},
	}
}

// getConfigMapDuck creates a ConfigMap for ApplicationSet controller to identify placement resources
func getConfigMapDuck(configMapName string, namespace string, apiVersion string, kind string) v1.ConfigMap {
	return v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"apiVersion":    apiVersion,
			"kind":          kind,
			"statusListKey": "decisions",
			"matchKey":      "clusterName",
		},
	}
}

// CreateApplicationSetConfigMaps creates the required configMap to allow ArgoCD ApplicationSet
// to identify our two forms of placement
func (r *ReconcileGitOpsCluster) CreateApplicationSetConfigMaps(namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Create two configMaps, one for placementrules.apps and placementdecisions.cluster
	maps := []v1.ConfigMap{
		getConfigMapDuck(configMapNameOld, namespace, "apps.open-cluster-management.io/v1", "placementrules"),
		getConfigMapDuck(configMapNameNew, namespace, "cluster.open-cluster-management.io/v1beta1", "placementdecisions"),
	}

	for i := range maps {
		configMap := v1.ConfigMap{}

		err := r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: maps[i].Name}, &configMap)

		if err != nil && strings.Contains(err.Error(), " not found") {
			err = r.Create(context.Background(), &maps[i])
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}

	return nil
}

// CreateApplicationSetRbac sets up required role and roleBinding so that the applicationset-controller
// can work with placementRules and placementDecisions
func (r *ReconcileGitOpsCluster) CreateApplicationSetRbac(namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	err := r.Get(context.Background(), types.NamespacedName{Name: namespace + RoleSuffix, Namespace: namespace}, &rbacv1.Role{})
	if k8errors.IsNotFound(err) {
		klog.Infof("creating role %s, in namespace %s", namespace+RoleSuffix, namespace)

		err = r.Create(context.Background(), getRoleDuck(namespace))
		if err != nil {
			return err
		}
	}

	err = r.Get(context.Background(), types.NamespacedName{Name: namespace + RoleSuffix, Namespace: namespace}, &rbacv1.RoleBinding{})
	if k8errors.IsNotFound(err) {
		klog.Infof("creating roleBinding %s, in namespace %s", namespace+RoleSuffix, namespace)

		err = r.Create(context.Background(), r.getRoleBindingDuck(namespace))
		if err != nil {
			return err
		}
	}

	return nil
}

// ensureAddonManagerRBAC creates the addon-manager-controller RBAC resources if they don't exist
func (r *ReconcileGitOpsCluster) ensureAddonManagerRBAC(gitopsNamespace string) error {
	// Create Role
	err := r.ensureAddonManagerRole(gitopsNamespace)
	if err != nil {
		return fmt.Errorf("failed to ensure addon-manager-controller role: %w", err)
	}

	// Create RoleBinding
	err = r.ensureAddonManagerRoleBinding(gitopsNamespace)
	if err != nil {
		return fmt.Errorf("failed to ensure addon-manager-controller rolebinding: %w", err)
	}

	return nil
}

// ensureAddonManagerRole creates the addon-manager-controller role if it doesn't exist
func (r *ReconcileGitOpsCluster) ensureAddonManagerRole(gitopsNamespace string) error {
	role := &rbacv1.Role{}
	roleName := types.NamespacedName{
		Name:      "addon-manager-controller-role",
		Namespace: gitopsNamespace,
	}

	err := r.Get(context.TODO(), roleName, role)
	if err == nil {
		klog.Info("addon-manager-controller-role already exists in namespace", gitopsNamespace)
		return nil
	}

	if !k8errors.IsNotFound(err) {
		return fmt.Errorf("failed to check addon-manager-controller-role: %w", err)
	}

	klog.Info("Creating addon-manager-controller-role in namespace", gitopsNamespace)

	newRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "addon-manager-controller-role",
			Namespace: gitopsNamespace,
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

	err = r.Create(context.TODO(), newRole)
	if err != nil {
		if k8errors.IsAlreadyExists(err) {
			klog.Info("addon-manager-controller-role was created by another process in namespace", gitopsNamespace)
			return nil
		}

		return fmt.Errorf("failed to create addon-manager-controller-role: %w", err)
	}

	klog.Info("Successfully created addon-manager-controller-role in namespace", gitopsNamespace)

	return nil
}

// ensureAddonManagerRoleBinding creates the addon-manager-controller rolebinding if it doesn't exist
func (r *ReconcileGitOpsCluster) ensureAddonManagerRoleBinding(gitopsNamespace string) error {
	roleBinding := &rbacv1.RoleBinding{}
	roleBindingName := types.NamespacedName{
		Name:      "addon-manager-controller-rolebinding",
		Namespace: gitopsNamespace,
	}

	err := r.Get(context.TODO(), roleBindingName, roleBinding)
	if err == nil {
		klog.Info("addon-manager-controller-rolebinding already exists in namespace", gitopsNamespace)
		return nil
	}

	if !k8errors.IsNotFound(err) {
		return fmt.Errorf("failed to check addon-manager-controller-rolebinding: %w", err)
	}

	klog.Info("Creating addon-manager-controller-rolebinding in namespace", gitopsNamespace)

	// The addon-manager-controller-sa is always in the open-cluster-management-hub namespace
	// regardless of where the GitOpsCluster controller is running
	addonManagerNamespace := "open-cluster-management-hub"

	newRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "addon-manager-controller-rolebinding",
			Namespace: gitopsNamespace,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopsaddon": "true",
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "addon-manager-controller-sa",
				Namespace: addonManagerNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     "addon-manager-controller-role",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	err = r.Create(context.TODO(), newRoleBinding)
	if err != nil {
		if k8errors.IsAlreadyExists(err) {
			klog.Info("addon-manager-controller-rolebinding was created by another process in namespace", gitopsNamespace)
			return nil
		}

		return fmt.Errorf("failed to create addon-manager-controller-rolebinding: %w", err)
	}

	klog.Info("Successfully created addon-manager-controller-rolebinding in namespace", gitopsNamespace)

	return nil
}
