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
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"

	yaml "gopkg.in/yaml.v3"
	rbacv1 "k8s.io/api/rbac/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	workv1 "open-cluster-management.io/api/work/v1"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileGitOpsCluster reconciles a GitOpsCluster object.
type ReconcileGitOpsCluster struct {
	client.Client
	dynamic.DynamicClient
	authClient kubernetes.Interface
	scheme     *runtime.Scheme
	lock       sync.Mutex
}

// TokenConfig defines a token configuration used in ArgoCD cluster secret
type TokenConfig struct {
	BearerToken     string `json:"bearerToken"`
	TLSClientConfig struct {
		Insecure bool `json:"insecure"`
	} `json:"tlsClientConfig"`
}

// Add creates a new argocd cluster Controller and adds it to the Manager with default RBAC.
// The Manager will set fields on the Controller and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	reconciler, err := newReconciler(mgr)
	if err != nil {
		return err
	}

	return add(mgr, reconciler)
}

var _ reconcile.Reconciler = &ReconcileGitOpsCluster{}

var errInvalidPlacementRef = errors.New("invalid placement reference")

const (
	clusterSecretSuffix = "-cluster-secret"
	maxStatusMsgLen     = 128 * 1024

	// Annotation keys for tracking ManifestWork state
	ArgoCDAgentOutdatedAnnotation    = "apps.open-cluster-management.io/argocd-agent-outdated"
	ArgoCDAgentPropagateCAAnnotation = "apps.open-cluster-management.io/argocd-agent-propagate-ca"
)

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) (reconcile.Reconciler, error) {
	authCfg := mgr.GetConfig()
	kubeClient := kubernetes.NewForConfigOrDie(authCfg)

	dynamicClient, err := dynamic.NewForConfig(authCfg)
	if err != nil {
		klog.Error("failed to create dynamic client, error: ", err)

		return nil, err
	}

	dsRS := &ReconcileGitOpsCluster{
		Client:        mgr.GetClient(),
		DynamicClient: *dynamicClient,
		scheme:        mgr.GetScheme(),
		authClient:    kubeClient,
		lock:          sync.Mutex{},
	}

	return dsRS, nil
}

type placementDecisionMapper struct {
	client.Client
}

func (mapper *placementDecisionMapper) Map(ctx context.Context, obj *clusterv1beta1.PlacementDecision) []reconcile.Request {
	var requests []reconcile.Request

	gitOpsClusterList := &gitopsclusterV1beta1.GitOpsClusterList{}
	listopts := &client.ListOptions{Namespace: obj.GetNamespace()}
	err := mapper.List(context.TODO(), gitOpsClusterList, listopts)

	if err != nil {
		klog.Error("failed to list GitOpsClusters, error:", err)
	}

	labels := obj.GetLabels()

	// if placementDecision is created/updated/deleted, its relative GitOpsCluster should be reconciled.
	for _, gitOpsCluster := range gitOpsClusterList.Items {
		if strings.EqualFold(gitOpsCluster.Spec.PlacementRef.Name, labels["cluster.open-cluster-management.io/placement"]) &&
			strings.EqualFold(gitOpsCluster.Namespace, obj.GetNamespace()) {
			klog.Infof("Placement decision %s/%s affects GitOpsCluster %s/%s",
				obj.GetNamespace(),
				obj.GetName(),
				gitOpsCluster.Namespace,
				gitOpsCluster.Name)

			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: gitOpsCluster.Namespace, Name: gitOpsCluster.Name}})

			// just one GitOpsCluster is enough. The reconcile will process all GitOpsClusters
			break
		}
	}

	klog.Info("Out placement decision mapper with requests:", requests)

	return requests
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	skipValidation := true
	c, err := controller.New("gitopscluster-controller", mgr, controller.Options{
		Reconciler:         r,
		SkipNameValidation: &skipValidation,
	})

	if err != nil {
		return err
	}

	if utils.IsReadyACMClusterRegistry(mgr.GetAPIReader()) {
		// Watch gitopscluster changes
		err = c.Watch(
			source.Kind(
				mgr.GetCache(),
				&gitopsclusterV1beta1.GitOpsCluster{},
				&handler.TypedEnqueueRequestForObject[*gitopsclusterV1beta1.GitOpsCluster]{},
				utils.GitOpsClusterPredicateFunc,
			),
		)

		if err != nil {
			return err
		}

		// Watch for managed cluster secret changes in argo or managed cluster namespaces
		// The manager started with cache that filters all other secrets so no predicate needed
		err = c.Watch(
			source.Kind(
				mgr.GetCache(),
				&v1.Secret{},
				&handler.TypedEnqueueRequestForObject[*v1.Secret]{},
				utils.ManagedClusterSecretPredicateFunc,
			),
		)

		if err != nil {
			return err
		}

		// Watch cluster list changes in placement decision
		pdMapper := &placementDecisionMapper{mgr.GetClient()}
		err = c.Watch(
			source.Kind(
				mgr.GetCache(),
				&clusterv1beta1.PlacementDecision{},
				handler.TypedEnqueueRequestsFromMapFunc[*clusterv1beta1.PlacementDecision](pdMapper.Map),
				utils.PlacementDecisionPredicateFunc,
			),
		)

		if err != nil {
			return err
		}

		// Watch cluster changes to update cluster labels
		err = c.Watch(
			source.Kind(
				mgr.GetCache(),
				&spokeclusterv1.ManagedCluster{},
				&handler.TypedEnqueueRequestForObject[*spokeclusterv1.ManagedCluster]{},
				utils.ClusterPredicateFunc,
			),
		)
		if err != nil {
			return err
		}

		// Watch changes to Managed service account's tokenSecretRef
		if utils.IsReadyManagedServiceAccount(mgr.GetAPIReader()) {
			err = c.Watch(
				source.Kind(
					mgr.GetCache(),
					&authv1beta1.ManagedServiceAccount{},
					&handler.TypedEnqueueRequestForObject[*authv1beta1.ManagedServiceAccount]{},
					utils.ManagedServiceAccountPredicateFunc,
				),
			)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *ReconcileGitOpsCluster) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Info("Reconciling GitOpsClusters for watched resource change: ", request.NamespacedName)

	// Get all existing GitOps managed cluster secrets, not the ones from the managed cluster namespaces
	managedClusterSecretsInArgo, err := r.GetAllManagedClusterSecretsInArgo()

	if err != nil {
		klog.Error("failed to get all existing managed cluster secrets for ArgoCD, ", err)
		return reconcile.Result{Requeue: false}, nil
	}

	// Then save it in a map. As we create/update GitOps managed cluster secrets while
	// reconciling each GitOpsCluster resource, remove the secret from this list.
	// After reconciling all GitOpsCluster resources, the secrets left in this list are
	// orphan secrets to be removed.
	orphanGitOpsClusterSecretList := map[types.NamespacedName]string{}

	for _, secret := range managedClusterSecretsInArgo.Items {
		orphanGitOpsClusterSecretList[types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}] = secret.Namespace + "/" + secret.Name
	}

	// Get all GitOpsCluster resources
	gitOpsClusters, err := r.GetAllGitOpsClusters()

	if err != nil {
		return reconcile.Result{Requeue: false}, nil
	}

	var returnErr error

	var returnRequeueInterval int

	// For any watched resource change, process all GitOpsCluster CRs to create new secrets or update existing secrets.
	for _, gitOpsCluster := range gitOpsClusters.Items {
		klog.Info("Process GitOpsCluster: " + gitOpsCluster.Namespace + "/" + gitOpsCluster.Name)

		instance := &gitopsclusterV1beta1.GitOpsCluster{}

		err := r.Get(context.TODO(), types.NamespacedName{Name: gitOpsCluster.Name, Namespace: gitOpsCluster.Namespace}, instance)

		if err != nil && k8errors.IsNotFound(err) {
			klog.Infof("GitOpsCluster %s/%s deleted", gitOpsCluster.Namespace, gitOpsCluster.Name)
			// deleted? just skip to the next GitOpsCluster resource
			continue
		}

		// reconcile one GitOpsCluster resource
		requeueInterval, err := r.reconcileGitOpsCluster(*instance, orphanGitOpsClusterSecretList)

		if err != nil {
			klog.Error(err.Error())

			returnErr = err
			returnRequeueInterval = requeueInterval
		}
	}

	// Remove all invalid/orphan GitOps cluster secrets
	if !r.cleanupOrphanSecrets(orphanGitOpsClusterSecretList) {
		// If it failed to delete orphan GitOps managed cluster secrets, reconile again in 10 minutes.
		if returnErr == nil {
			return reconcile.Result{Requeue: true, RequeueAfter: time.Duration(10) * time.Minute}, err
		}
	}

	if returnErr != nil {
		klog.Info("reconcile failed, requeue")

		return reconcile.Result{Requeue: true, RequeueAfter: time.Duration(returnRequeueInterval) * time.Minute}, returnErr
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileGitOpsCluster) cleanupOrphanSecrets(orphanGitOpsClusterSecretList map[types.NamespacedName]string) bool {
	cleanupSuccessful := true

	// 4. Delete all orphan GitOps managed cluster secrets
	for key, secretName := range orphanGitOpsClusterSecretList {
		secretToDelete := &v1.Secret{}

		err := r.Get(context.TODO(), key, secretToDelete)

		if err == nil {
			klog.Infof("Deleting orphan GitOps managed cluster secret %s", secretName)

			err = r.Delete(context.TODO(), secretToDelete)

			if err != nil {
				klog.Errorf("failed to delete orphan managed cluster secret %s, err: %s", key, err.Error())

				cleanupSuccessful = false

				continue
			}
		}
	}

	return cleanupSuccessful
}

func (r *ReconcileGitOpsCluster) reconcileGitOpsCluster(
	gitOpsCluster gitopsclusterV1beta1.GitOpsCluster,
	orphanSecretsList map[types.NamespacedName]string) (int, error) {
	instance := gitOpsCluster.DeepCopy()

	// Validate ArgoCDAgent spec fields
	if err := r.ValidateArgoCDAgentSpec(instance.Spec.ArgoCDAgent); err != nil {
		klog.Errorf("ArgoCDAgent spec validation failed: %v", err)

		r.updateGitOpsClusterConditions(instance, "failed",
			fmt.Sprintf("ArgoCDAgent spec validation failed: %v", err),
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady: {
					Status:  metav1.ConditionFalse,
					Reason:  gitopsclusterV1beta1.ReasonInvalidConfiguration,
					Message: fmt.Sprintf("ArgoCDAgent spec validation failed: %v", err),
				},
			})

		err2 := r.Client.Status().Update(context.TODO(), instance)
		if err2 != nil {
			klog.Errorf("failed to update GitOpsCluster %s status after validation failure: %s", instance.Namespace+"/"+instance.Name, err2)
			return 1, err2
		}

		return 1, err
	}

	annotations := instance.GetAnnotations()

	// Create default policy template
	createPolicyTemplate := false
	if instance.Spec.CreatePolicyTemplate != nil {
		createPolicyTemplate = *instance.Spec.CreatePolicyTemplate
	}

	if createPolicyTemplate && instance.Spec.PlacementRef != nil &&
		instance.Spec.PlacementRef.Kind == "Placement" &&
		instance.Spec.ManagedServiceAccountRef != "" {
		if err := r.createNamespaceScopedResourceFromYAML(generatePlacementYamlString(*instance)); err != nil {
			klog.Error("failed to create default policy template local placement: ", err)
		}

		if err := r.createNamespaceScopedResourceFromYAML(generatePlacementBindingYamlString(*instance)); err != nil {
			klog.Error("failed to create default policy template local placement binding, ", err)
		}

		if err := r.createNamespaceScopedResourceFromYAML(generatePolicyTemplateYamlString(*instance)); err != nil {
			klog.Error("failed to create default policy template, ", err)
		}
	}

	// 1. Verify that spec.argoServer.argoNamespace is a valid ArgoCD namespace
	// skipArgoNamespaceVerify annotation just in case the service labels we use for verification change in future
	if !r.VerifyArgocdNamespace(gitOpsCluster.Spec.ArgoServer.ArgoNamespace) &&
		annotations["skipArgoNamespaceVerify"] != "true" {
		klog.Info("invalid argocd namespace because argo server pod was not found")

		r.updateGitOpsClusterConditions(instance, "failed",
			"invalid gitops namespace because argo server pod was not found",
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady: {
					Status:  metav1.ConditionFalse,
					Reason:  gitopsclusterV1beta1.ReasonArgoServerNotFound,
					Message: "ArgoCD server pod was not found in the specified namespace",
				},
			})

		err := r.Client.Status().Update(context.TODO(), instance)

		if err != nil {
			klog.Errorf("failed to update GitOpsCluster %s status, will try again: %s", instance.Namespace+"/"+instance.Name, err)
			return 1, err
		}

		return 1, errors.New("invalid gitops namespace because argo server pod was not found, will try again")
	}

	// 1a. Add configMaps to be used by ArgoCD ApplicationSets
	err := r.CreateApplicationSetConfigMaps(gitOpsCluster.Spec.ArgoServer.ArgoNamespace)
	if err != nil {
		klog.Warningf("there was a problem creating the configMaps: %v", err.Error())
	}

	// 1b. Add roles so applicationset-controller can read placementRules and placementDecisions
	err = r.CreateApplicationSetRbac(gitOpsCluster.Spec.ArgoServer.ArgoNamespace)
	if err != nil {
		klog.Warningf("there was a problem creating the role or binding: %v", err.Error())
	}

	// 2. Get the list of managed clusters
	// The placement must be in the same namespace as GitOpsCluster
	managedClusters, err := r.GetManagedClusters(instance.Namespace, *instance.Spec.PlacementRef)
	// 2a. Get the placement decision
	// 2b. Get the managed cluster names from the placement decision
	if err != nil {
		klog.Info("failed to get managed cluster list")

		r.updateGitOpsClusterConditions(instance, "failed", err.Error(),
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterPlacementResolved: {
					Status:  metav1.ConditionFalse,
					Reason:  gitopsclusterV1beta1.ReasonPlacementNotFound,
					Message: err.Error(),
				},
			})

		err2 := r.Client.Status().Update(context.TODO(), instance)

		if err2 != nil {
			klog.Errorf("failed to update GitOpsCluster %s status, will try again in 3 minutes: %s", instance.Namespace+"/"+instance.Name, err2)
			return 3, err2
		}
	}

	managedClusterNames := []string{}
	for _, managedCluster := range managedClusters {
		managedClusterNames = append(managedClusterNames, managedCluster.Name)
	}

	klog.V(1).Infof("adding managed clusters %v into argo namespace %s", managedClusterNames, instance.Spec.ArgoServer.ArgoNamespace)

	// 3. Check if ArgoCD agent is enabled
	argoCDAgentEnabled := false
	if instance.Spec.ArgoCDAgent != nil && instance.Spec.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *instance.Spec.ArgoCDAgent.Enabled
	}

	// 3a. Ensure argocd-redis secret exists if ArgoCD agent is enabled
	if argoCDAgentEnabled {
		err = r.ensureArgoCDRedisSecret(instance.Spec.ArgoServer.ArgoNamespace)
		if err != nil {
			klog.Errorf("failed to ensure argocd-redis secret: %v", err)

			msg := err.Error()
			if len(msg) > maxStatusMsgLen {
				msg = msg[:maxStatusMsgLen]
			}

			r.updateGitOpsClusterConditions(instance, "failed", msg,
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonInvalidConfiguration,
						Message: msg,
					},
				})

			err2 := r.Client.Status().Update(context.TODO(), instance)
			if err2 != nil {
				klog.Errorf("failed to update GitOpsCluster %s status after redis secret failure: %s", instance.Namespace+"/"+instance.Name, err2)
				return 3, err2
			}

			return 3, err
		}
	}

	// 3b. Auto-discover server address and port if ArgoCD agent is enabled and values are empty
	if argoCDAgentEnabled {
		updated, err := r.EnsureServerAddressAndPort(instance, managedClusters)
		if err != nil {
			klog.Warningf("failed to auto-discover server address/port: %v", err)
		} else if updated {
			// Update the GitOpsCluster spec with discovered values
			err = r.Update(context.TODO(), instance)
			if err != nil {
				klog.Errorf("failed to update GitOpsCluster with discovered server address/port: %v", err)
			} else {
				klog.Infof("updated GitOpsCluster %s/%s with auto-discovered server address/port", instance.Namespace, instance.Name)
			}
		}

		// 3b. Ensure addon-manager-controller RBAC resources exist in GitOps namespace
		if err := r.ensureAddonManagerRBAC(instance.Namespace); err != nil {
			klog.Errorf("failed to ensure addon-manager-controller RBAC resources in namespace %s: %v", instance.Namespace, err)
			r.updateGitOpsClusterConditions(instance, "failed",
				fmt.Sprintf("Failed to ensure addon-manager RBAC resources: %v", err),
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonArgoCDAgentFailed,
						Message: fmt.Sprintf("Failed to setup addon-manager RBAC resources: %v", err),
					},
				})
			err2 := r.Client.Status().Update(context.TODO(), instance)
			if err2 != nil {
				klog.Errorf("failed to update GitOpsCluster %s status after RBAC setup failure: %s", instance.Namespace+"/"+instance.Name, err2)
				return 3, err2
			}
			return 3, err
		}

		// 3c. Ensure argocd-agent-ca secret exists in GitOps namespace
		if err := r.ensureArgoCDAgentCASecret(instance.Namespace); err != nil {
			klog.Errorf("failed to ensure argocd-agent-ca secret in namespace %s: %v", instance.Namespace, err)
			r.updateGitOpsClusterConditions(instance, "failed",
				fmt.Sprintf("Failed to ensure ArgoCD agent CA secret: %v", err),
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonArgoCDAgentFailed,
						Message: fmt.Sprintf("Failed to setup ArgoCD agent CA secret: %v", err),
					},
				})
			err2 := r.Client.Status().Update(context.TODO(), instance)
			if err2 != nil {
				klog.Errorf("failed to update GitOpsCluster %s status after CA secret setup failure: %s", instance.Namespace+"/"+instance.Name, err2)
				return 3, err2
			}
			return 3, err
		}

		klog.Infof("Successfully ensured ArgoCD agent prerequisites (RBAC and CA secret) for GitOpsCluster %s/%s", instance.Namespace, instance.Name)
	}

	// Check if Hub CA propagation is enabled (default true)
	propagateHubCA := true
	if instance.Spec.ArgoCDAgent != nil && instance.Spec.ArgoCDAgent.PropagateHubCA != nil {
		propagateHubCA = *instance.Spec.ArgoCDAgent.PropagateHubCA
	}

	// 4. Copy secret contents from the managed cluster namespaces and create the secret in spec.argoServer.argoNamespace
	// if spec.createBlankClusterSecrets is true then do err on missing secret from the managed cluster namespace
	// if argoCDAgent is enabled, createBlankClusterSecrets should also be true
	createBlankClusterSecrets := false
	if instance.Spec.CreateBlankClusterSecrets != nil {
		createBlankClusterSecrets = *instance.Spec.CreateBlankClusterSecrets
	}
	// Enable createBlankClusterSecrets when ArgoCD agent is enabled
	if argoCDAgentEnabled {
		createBlankClusterSecrets = true
	}

	// Create AddOnDeploymentConfig for each managed cluster namespace
	for _, managedCluster := range managedClusters {
		err = r.CreateAddOnDeploymentConfig(instance, managedCluster.Name)
		if err != nil {
			klog.Errorf("failed to create AddOnDeploymentConfig for managed cluster %s: %v", managedCluster.Name, err)
		}
	}

	// Create ManagedClusterAddon for each managed cluster namespace if ArgoCD agent is enabled
	if argoCDAgentEnabled {
		for _, managedCluster := range managedClusters {
			err = r.EnsureManagedClusterAddon(managedCluster.Name)
			if err != nil {
				klog.Errorf("failed to ensure ManagedClusterAddon for managed cluster %s: %v", managedCluster.Name, err)
			}
		}
	}

	err = r.AddManagedClustersToArgo(instance, managedClusters, orphanSecretsList, createBlankClusterSecrets)

	if err != nil {
		klog.Info("failed to add managed clusters to argo")

		msg := err.Error()
		if len(msg) > maxStatusMsgLen {
			msg = msg[:maxStatusMsgLen]
		}

		r.updateGitOpsClusterConditions(instance, "failed", msg,
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterClustersRegistered: {
					Status:  metav1.ConditionFalse,
					Reason:  gitopsclusterV1beta1.ReasonClusterRegistrationFailed,
					Message: msg,
				},
			})

		err2 := r.Client.Status().Update(context.TODO(), instance)

		if err2 != nil {
			klog.Errorf("failed to update GitOpsCluster %s status, will try again in 3 minutes: %s", instance.Namespace+"/"+instance.Name, err2)
			return 3, err2
		}

		return 3, err
	}

	// 5. Handle ArgoCD agent CA secret ManifestWork based on propagateHubCA setting
	if argoCDAgentEnabled {
		if propagateHubCA {
			// Create/update ManifestWork for ArgoCD agent CA secret
			err = r.CreateArgoCDAgentManifestWorks(instance, managedClusters)
			if err != nil {
				klog.Errorf("failed to create ArgoCD agent ManifestWorks: %v", err)

				msg := err.Error()
				if len(msg) > maxStatusMsgLen {
					msg = msg[:maxStatusMsgLen]
				}

				r.updateGitOpsClusterConditions(instance, "failed", msg,
					map[string]ConditionUpdate{
						gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied: {
							Status:  metav1.ConditionFalse,
							Reason:  gitopsclusterV1beta1.ReasonManifestWorkFailed,
							Message: msg,
						},
					})

				err2 := r.Client.Status().Update(context.TODO(), instance)

				if err2 != nil {
					klog.Errorf("failed to update GitOpsCluster %s status, will try again in 3 minutes: %s", instance.Namespace+"/"+instance.Name, err2)
					return 3, err2
				}

				return 3, err
			}
		} else {
			// Mark existing ManifestWorks as outdated when propagateHubCA is false
			err = r.MarkArgoCDAgentManifestWorksAsOutdated(instance, managedClusters)
			if err != nil {
				klog.Errorf("failed to mark ArgoCD agent ManifestWorks as outdated: %v", err)
			}
		}
	}

	// 6. Ensure ArgoCD agent TLS certificates are signed if argoCDAgent is enabled
	if argoCDAgentEnabled {
		err = r.EnsureArgoCDAgentCertificates(instance)
		if err != nil {
			klog.Errorf("failed to ensure ArgoCD agent certificates: %v", err)

			msg := err.Error()
			if len(msg) > maxStatusMsgLen {
				msg = msg[:maxStatusMsgLen]
			}

			r.updateGitOpsClusterConditions(instance, "failed", msg,
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterCertificatesReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonCertificateSigningFailed,
						Message: msg,
					},
				})

			err2 := r.Client.Status().Update(context.TODO(), instance)

			if err2 != nil {
				klog.Errorf("failed to update GitOpsCluster %s status, will try again in 3 minutes: %s", instance.Namespace+"/"+instance.Name, err2)
				return 3, err2
			}

			return 3, err
		}
	}

	managedClustersStr := strings.Join(managedClusterNames, " ")
	if len(managedClustersStr) > 4096 {
		managedClustersStr = fmt.Sprintf("%.4096v", managedClustersStr) + "..."
	}

	successMessage := fmt.Sprintf("Added managed clusters [%v] to gitops namespace %s", managedClustersStr, instance.Spec.ArgoServer.ArgoNamespace)

	// Build condition updates for successful case
	conditionUpdates := map[string]ConditionUpdate{
		gitopsclusterV1beta1.GitOpsClusterPlacementResolved: {
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonSuccess,
			Message: fmt.Sprintf("Successfully resolved %d managed clusters from placement", len(managedClusterNames)),
		},
		gitopsclusterV1beta1.GitOpsClusterClustersRegistered: {
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonSuccess,
			Message: fmt.Sprintf("Successfully registered %d managed clusters to ArgoCD", len(managedClusterNames)),
		},
	}

	// Add ArgoCD agent specific conditions if enabled
	if argoCDAgentEnabled {
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonSuccess,
			Message: "ArgoCD agent is properly configured and enabled",
		}
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterCertificatesReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonSuccess,
			Message: "ArgoCD agent certificates are properly signed and ready",
		}

		// Only set ManifestWorks condition if CA propagation is enabled
		if propagateHubCA {
			conditionUpdates[gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied] = ConditionUpdate{
				Status:  metav1.ConditionTrue,
				Reason:  gitopsclusterV1beta1.ReasonSuccess,
				Message: "CA propagation ManifestWorks successfully applied to managed clusters",
			}
		} else {
			conditionUpdates[gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied] = ConditionUpdate{
				Status:  metav1.ConditionTrue,
				Reason:  gitopsclusterV1beta1.ReasonNotRequired,
				Message: "CA propagation is disabled, ManifestWorks not required",
			}
		}
	} else {
		// ArgoCD agent is disabled, set conditions accordingly
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonDisabled,
			Message: "ArgoCD agent is disabled",
		}
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterCertificatesReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonNotRequired,
			Message: "ArgoCD agent certificates not required (agent disabled)",
		}
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonNotRequired,
			Message: "ManifestWorks not required (agent disabled)",
		}
	}

	r.updateGitOpsClusterConditions(instance, "successful", successMessage, conditionUpdates)

	err = r.Client.Status().Update(context.TODO(), instance)

	if err != nil {
		klog.Errorf("failed to update GitOpsCluster %s status, will try again in 3 minutes: %s", instance.Namespace+"/"+instance.Name, err)
		return 3, err
	}

	return 0, nil
}

// ValidateArgoCDAgentSpec validates the ArgoCDAgent spec fields and returns an error if validation fails
func (r *ReconcileGitOpsCluster) ValidateArgoCDAgentSpec(argoCDAgent *gitopsclusterV1beta1.ArgoCDAgentSpec) error {
	if argoCDAgent == nil {
		return nil // nil spec is valid
	}

	// Validate Mode field
	if argoCDAgent.Mode != "" {
		validModes := []string{"managed", "autonomous"}
		isValidMode := false
		for _, validMode := range validModes {
			if argoCDAgent.Mode == validMode {
				isValidMode = true
				break
			}
		}
		if !isValidMode {
			return fmt.Errorf("invalid Mode '%s': must be one of %v", argoCDAgent.Mode, validModes)
		}
	}

	// Validate ReconcileScope field
	if argoCDAgent.ReconcileScope != "" {
		validScopes := []string{"All-Namespaces", "Single-Namespace"}
		isValidScope := false
		for _, validScope := range validScopes {
			if argoCDAgent.ReconcileScope == validScope {
				isValidScope = true
				break
			}
		}
		if !isValidScope {
			return fmt.Errorf("invalid ReconcileScope '%s': must be one of %v", argoCDAgent.ReconcileScope, validScopes)
		}
	}

	// Validate Action field
	if argoCDAgent.Action != "" {
		validActions := []string{"Install", "Delete-Operator"}
		isValidAction := false
		for _, validAction := range validActions {
			if argoCDAgent.Action == validAction {
				isValidAction = true
				break
			}
		}
		if !isValidAction {
			return fmt.Errorf("invalid Action '%s': must be one of %v", argoCDAgent.Action, validActions)
		}
	}

	return nil
}

// GetAllManagedClusterSecretsInArgo returns list of secrets from all GitOps managed cluster
func (r *ReconcileGitOpsCluster) GetAllManagedClusterSecretsInArgo() (v1.SecretList, error) {
	klog.Info("Getting all managed cluster secrets from argo namespaces")

	secretList := &v1.SecretList{}
	listopts := &client.ListOptions{}

	secretSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"apps.open-cluster-management.io/acm-cluster": "true",
			"argocd.argoproj.io/secret-type":              "cluster",
		},
	}

	secretSelectionLabel, err := utils.ConvertLabels(secretSelector)
	if err != nil {
		klog.Error("Failed to convert managed cluster secret selector, err:", err)
		return *secretList, err
	}

	listopts.LabelSelector = secretSelectionLabel
	err = r.List(context.TODO(), secretList, listopts)

	if err != nil {
		klog.Error("Failed to list managed cluster secrets in argo, err:", err)
		return *secretList, err
	}

	return *secretList, nil
}

// GetAllManagedClusterSecretsInArgo returns list of secrets from all GitOps managed cluster.
// these secrets are not gnerated by ACM ArgoCD push model, they are created by end users themselves
func (r *ReconcileGitOpsCluster) GetAllNonAcmManagedClusterSecretsInArgo(argoNs string) (map[string][]*v1.Secret, error) {
	klog.Info("Getting all non-acm managed cluster secrets from argo namespaces")

	secretMap := make(map[string][]*v1.Secret, 0)

	secretList := &v1.SecretList{}
	listopts := &client.ListOptions{}

	secretSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"argocd.argoproj.io/secret-type": "cluster",
		},
	}

	secretSelectionLabel, err := utils.ConvertLabels(secretSelector)
	if err != nil {
		klog.Error("Failed to convert managed cluster secret selector, err:", err)
		return secretMap, err
	}

	listopts.Namespace = argoNs

	listopts.LabelSelector = secretSelectionLabel
	err = r.List(context.TODO(), secretList, listopts)

	if err != nil {
		klog.Error("Failed to list managed cluster secrets in argo, err:", err)
		return secretMap, err
	}

	// Add non-ACM secrets to map by cluster name
	for i := range secretList.Items {
		s := secretList.Items[i]

		_, acmcluster := s.Labels["apps.open-cluster-management.io/acm-cluster"]
		if !acmcluster {
			cluster := s.Data["name"]

			if cluster != nil {
				secrets := secretMap[string(cluster)]
				if secrets == nil {
					secrets = []*v1.Secret{}
				}

				secrets = append(secrets, &s)
				secretMap[string(cluster)] = secrets
			}
		}
	}

	return secretMap, nil
}

// GetAllGitOpsClusters returns all GitOpsCluster CRs
func (r *ReconcileGitOpsCluster) GetAllGitOpsClusters() (gitopsclusterV1beta1.GitOpsClusterList, error) {
	klog.Info("Getting all GitOpsCluster resources")

	gitOpsClusterList := &gitopsclusterV1beta1.GitOpsClusterList{}

	err := r.List(context.TODO(), gitOpsClusterList)

	if err != nil {
		klog.Error("Failed to list GitOpsCluster resources, err:", err)
		return *gitOpsClusterList, err
	}

	return *gitOpsClusterList, nil
}

// VerifyArgocdNamespace verifies that the given argoNamespace is a valid namspace by verifying that ArgoCD is actually
// installed in that namespace
func (r *ReconcileGitOpsCluster) VerifyArgocdNamespace(argoNamespace string) bool {
	return r.FindServiceWithLabelsAndNamespace(argoNamespace,
		map[string]string{"app.kubernetes.io/component": "server", "app.kubernetes.io/part-of": "argocd"})
}

// FindServiceWithLabelsAndNamespace finds a list of services with provided labels from the specified namespace
func (r *ReconcileGitOpsCluster) FindServiceWithLabelsAndNamespace(namespace string, labels map[string]string) bool {
	serviceList := &v1.ServiceList{}
	listopts := &client.ListOptions{}

	serviceSelector := &metav1.LabelSelector{
		MatchLabels: labels,
	}

	serviceLabels, err := utils.ConvertLabels(serviceSelector)
	if err != nil {
		klog.Error("Failed to convert label selector, err:", err)
		return false
	}

	listopts.LabelSelector = serviceLabels
	listopts.Namespace = namespace
	err = r.List(context.TODO(), serviceList, listopts)

	if err != nil {
		klog.Error("Failed to list services, err:", err)
		return false
	}

	if len(serviceList.Items) == 0 {
		klog.Infof("No service with labels %v found", labels)
		return false
	}

	for _, service := range serviceList.Items {
		klog.Info("Found service ", service.GetName(), " in namespace ", service.GetNamespace())
	}

	return true
}

const configMapNameOld = "acm-placementrule"
const configMapNameNew = "acm-placement"

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

const RoleSuffix = "-applicationset-controller-placement"

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

// ApplyApplicationSetConfigMaps creates the required configMap to allow ArgoCD ApplicationSet
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

// CreateAddOnDeploymentConfig creates or updates an AddOnDeploymentConfig for the managed cluster namespace
// It only updates GitOpsCluster-managed variables and preserves user-added custom variables
func (r *ReconcileGitOpsCluster) CreateAddOnDeploymentConfig(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Define variables managed by GitOpsCluster controller - only ArgoCD agent related variables
	managedVariables := map[string]string{
		"ARGOCD_AGENT_ENABLED": "true", // Only default we set
	}

	// Add ArgoCD agent values from GitOpsCluster spec only if they are provided (no defaults)
	if gitOpsCluster.Spec.ArgoCDAgent != nil {
		if gitOpsCluster.Spec.ArgoCDAgent.Image != "" {
			managedVariables["ARGOCD_AGENT_IMAGE"] = gitOpsCluster.Spec.ArgoCDAgent.Image
		}

		if gitOpsCluster.Spec.ArgoCDAgent.ServerAddress != "" {
			managedVariables["ARGOCD_AGENT_SERVER_ADDRESS"] = gitOpsCluster.Spec.ArgoCDAgent.ServerAddress
		}

		if gitOpsCluster.Spec.ArgoCDAgent.ServerPort != "" {
			managedVariables["ARGOCD_AGENT_SERVER_PORT"] = gitOpsCluster.Spec.ArgoCDAgent.ServerPort
		}

		if gitOpsCluster.Spec.ArgoCDAgent.Mode != "" {
			managedVariables["ARGOCD_AGENT_MODE"] = gitOpsCluster.Spec.ArgoCDAgent.Mode
		}

		if gitOpsCluster.Spec.ArgoCDAgent.GitOpsOperatorImage != "" {
			managedVariables["GITOPS_OPERATOR_IMAGE"] = gitOpsCluster.Spec.ArgoCDAgent.GitOpsOperatorImage
		}

		if gitOpsCluster.Spec.ArgoCDAgent.GitOpsOperatorNamespace != "" {
			managedVariables["GITOPS_OPERATOR_NAMESPACE"] = gitOpsCluster.Spec.ArgoCDAgent.GitOpsOperatorNamespace
		}

		if gitOpsCluster.Spec.ArgoCDAgent.GitOpsImage != "" {
			managedVariables["GITOPS_IMAGE"] = gitOpsCluster.Spec.ArgoCDAgent.GitOpsImage
		}

		if gitOpsCluster.Spec.ArgoCDAgent.GitOpsNamespace != "" {
			managedVariables["GITOPS_NAMESPACE"] = gitOpsCluster.Spec.ArgoCDAgent.GitOpsNamespace
		}

		if gitOpsCluster.Spec.ArgoCDAgent.RedisImage != "" {
			managedVariables["REDIS_IMAGE"] = gitOpsCluster.Spec.ArgoCDAgent.RedisImage
		}

		if gitOpsCluster.Spec.ArgoCDAgent.ReconcileScope != "" {
			managedVariables["RECONCILE_SCOPE"] = gitOpsCluster.Spec.ArgoCDAgent.ReconcileScope
		}

		if gitOpsCluster.Spec.ArgoCDAgent.Action != "" {
			managedVariables["ACTION"] = gitOpsCluster.Spec.ArgoCDAgent.Action
		}
	}

	// Check if AddOnDeploymentConfig already exists
	existing := &addonv1alpha1.AddOnDeploymentConfig{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon-config",
		Namespace: namespace,
	}, existing)

	if k8errors.IsNotFound(err) {
		// Create new AddOnDeploymentConfig with default managed variables
		klog.Infof("Creating AddOnDeploymentConfig gitops-addon-config in namespace %s", namespace)

		customizedVariables := make([]addonv1alpha1.CustomizedVariable, 0, len(managedVariables))
		for name, value := range managedVariables {
			customizedVariables = append(customizedVariables, addonv1alpha1.CustomizedVariable{
				Name:  name,
				Value: value,
			})
		}

		addonDeploymentConfig := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: namespace,
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: customizedVariables,
			},
		}

		err = r.Create(context.Background(), addonDeploymentConfig)
		if err != nil {
			klog.Errorf("Failed to create AddOnDeploymentConfig: %v", err)
			return err
		}
	} else if err != nil {
		klog.Errorf("Failed to get AddOnDeploymentConfig: %v", err)
		return err
	} else {
		// Update existing AddOnDeploymentConfig - merge managed variables with existing ones
		klog.Infof("Updating AddOnDeploymentConfig gitops-addon-config in namespace %s", namespace)

		// Create a map of existing variables for easy lookup
		existingVars := make(map[string]addonv1alpha1.CustomizedVariable)
		for _, variable := range existing.Spec.CustomizedVariables {
			existingVars[variable.Name] = variable
		}

		// Update or add managed variables, preserve user-added variables
		updatedVariables := make([]addonv1alpha1.CustomizedVariable, 0)

		// First, add all existing user variables (non-managed ones will be preserved)
		for _, variable := range existing.Spec.CustomizedVariables {
			if _, isManaged := managedVariables[variable.Name]; !isManaged {
				// This is a user-added variable, preserve it
				updatedVariables = append(updatedVariables, variable)
			}
		}

		// Then add/update all managed variables with current values
		for name, value := range managedVariables {
			updatedVariables = append(updatedVariables, addonv1alpha1.CustomizedVariable{
				Name:  name,
				Value: value,
			})
		}

		existing.Spec.CustomizedVariables = updatedVariables
		err = r.Update(context.Background(), existing)

		if err != nil {
			klog.Errorf("Failed to update AddOnDeploymentConfig: %v", err)
			return err
		}
	}

	// Check and update existing ManagedClusterAddOn if it exists
	err = r.UpdateManagedClusterAddonConfig(namespace)
	if err != nil {
		klog.Errorf("Failed to update ManagedClusterAddOn config: %v", err)
	}

	return nil
}

// UpdateManagedClusterAddonConfig updates the ManagedClusterAddOn configs to reference the AddOnDeploymentConfig
func (r *ReconcileGitOpsCluster) UpdateManagedClusterAddonConfig(namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Check if ManagedClusterAddOn exists
	existing := &addonv1alpha1.ManagedClusterAddOn{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon",
		Namespace: namespace,
	}, existing)

	if k8errors.IsNotFound(err) {
		// ManagedClusterAddOn doesn't exist, nothing to update
		klog.V(2).Infof("ManagedClusterAddOn gitops-addon not found in namespace %s, skipping config update", namespace)
		return nil
	} else if err != nil {
		klog.Errorf("Failed to get ManagedClusterAddOn gitops-addon: %v", err)
		return err
	}

	// Check if the config reference already exists and points to the correct AddOnDeploymentConfig
	expectedConfig := addonv1alpha1.AddOnConfig{
		ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
			Group:    "addon.open-cluster-management.io",
			Resource: "addondeploymentconfigs",
		},
		ConfigReferent: addonv1alpha1.ConfigReferent{
			Name:      "gitops-addon-config",
			Namespace: namespace,
		},
	}

	// Check if the expected config already exists in the configs list
	configExists := false

	for _, config := range existing.Spec.Configs {
		if config.Group == expectedConfig.Group &&
			config.Resource == expectedConfig.Resource &&
			config.Name == expectedConfig.Name &&
			config.Namespace == expectedConfig.Namespace {
			configExists = true
			break
		}
	}

	if configExists {
		klog.V(2).Infof("ManagedClusterAddOn gitops-addon already has correct config reference in namespace %s", namespace)
		return nil
	}

	// Add the config reference if it doesn't exist
	existing.Spec.Configs = append(existing.Spec.Configs, expectedConfig)

	err = r.Update(context.Background(), existing)
	if err != nil {
		klog.Errorf("Failed to update ManagedClusterAddOn gitops-addon: %v", err)
		return err
	}

	klog.Infof("Updated ManagedClusterAddOn gitops-addon config reference in namespace %s", namespace)

	return nil
}

// EnsureManagedClusterAddon creates the ManagedClusterAddon if it doesn't exist, or updates its config if it does
func (r *ReconcileGitOpsCluster) EnsureManagedClusterAddon(namespace string) error {
	if namespace == "" {
		return errors.New("no namespace provided")
	}

	// Check if ManagedClusterAddOn already exists
	existing := &addonv1alpha1.ManagedClusterAddOn{}
	err := r.Get(context.Background(), types.NamespacedName{
		Name:      "gitops-addon",
		Namespace: namespace,
	}, existing)

	expectedConfig := addonv1alpha1.AddOnConfig{
		ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
			Group:    "addon.open-cluster-management.io",
			Resource: "addondeploymentconfigs",
		},
		ConfigReferent: addonv1alpha1.ConfigReferent{
			Name:      "gitops-addon-config",
			Namespace: namespace,
		},
	}

	if k8errors.IsNotFound(err) {
		// Create new ManagedClusterAddOn with config reference
		klog.Infof("Creating ManagedClusterAddOn gitops-addon in namespace %s", namespace)

		managedClusterAddOn := &addonv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon",
				Namespace: namespace,
			},
			Spec: addonv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: "open-cluster-management-agent-addon",
				Configs:          []addonv1alpha1.AddOnConfig{expectedConfig},
			},
		}

		err = r.Create(context.Background(), managedClusterAddOn)
		if err != nil {
			klog.Errorf("Failed to create ManagedClusterAddOn gitops-addon: %v", err)
			return err
		}

		klog.Infof("Successfully created ManagedClusterAddOn gitops-addon in namespace %s", namespace)
		return nil
	} else if err != nil {
		klog.Errorf("Failed to get ManagedClusterAddOn gitops-addon: %v", err)
		return err
	}

	// ManagedClusterAddOn exists, ensure it has the correct config reference
	configExists := false
	for _, config := range existing.Spec.Configs {
		if config.Group == expectedConfig.Group &&
			config.Resource == expectedConfig.Resource &&
			config.Name == expectedConfig.Name &&
			config.Namespace == expectedConfig.Namespace {
			configExists = true
			break
		}
	}

	if !configExists {
		// Add the config reference if it doesn't exist
		klog.Infof("Adding config reference to existing ManagedClusterAddOn gitops-addon in namespace %s", namespace)
		existing.Spec.Configs = append(existing.Spec.Configs, expectedConfig)

		err = r.Update(context.Background(), existing)
		if err != nil {
			klog.Errorf("Failed to update ManagedClusterAddOn gitops-addon: %v", err)
			return err
		}

		klog.Infof("Updated ManagedClusterAddOn gitops-addon config reference in namespace %s", namespace)
	} else {
		klog.V(2).Infof("ManagedClusterAddOn gitops-addon already has correct config reference in namespace %s", namespace)
	}

	return nil
}

// EnsureServerAddressAndPort auto-discovers and populates server address and port if they are empty
func (r *ReconcileGitOpsCluster) EnsureServerAddressAndPort(gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster) (bool, error) {
	// Check if serverAddress and serverPort are already set in the GitOpsCluster spec
	if gitOpsCluster.Spec.ArgoCDAgent != nil &&
		(gitOpsCluster.Spec.ArgoCDAgent.ServerAddress != "" || gitOpsCluster.Spec.ArgoCDAgent.ServerPort != "") {
		klog.V(2).Infof("serverAddress or serverPort already set in GitOpsCluster spec, skipping auto-discovery")
		return false, nil
	}

	// Check if any existing AddonDeploymentConfig already has these values set
	hasExistingAddonConfig, err := r.HasExistingServerConfig(managedClusters)
	if err != nil {
		return false, fmt.Errorf("failed to check existing addon configs: %w", err)
	}
	if hasExistingAddonConfig {
		klog.V(2).Infof("serverAddress or serverPort already set in existing AddonDeploymentConfig, skipping auto-discovery")
		return false, nil
	}

	// Try to discover server address and port from the ArgoCD agent principal service
	argoNamespace := gitOpsCluster.Spec.ArgoServer.ArgoNamespace
	if argoNamespace == "" {
		argoNamespace = "openshift-gitops"
	}

	serverAddress, serverPort, err := r.DiscoverServerAddressAndPort(argoNamespace)
	if err != nil {
		return false, fmt.Errorf("failed to discover server address and port: %w", err)
	}

	// Initialize ArgoCDAgent spec if it doesn't exist
	if gitOpsCluster.Spec.ArgoCDAgent == nil {
		gitOpsCluster.Spec.ArgoCDAgent = &gitopsclusterV1beta1.ArgoCDAgentSpec{}
	}

	// Set the discovered values
	gitOpsCluster.Spec.ArgoCDAgent.ServerAddress = serverAddress
	gitOpsCluster.Spec.ArgoCDAgent.ServerPort = serverPort

	klog.Infof("auto-discovered server address: %s, port: %s for GitOpsCluster %s/%s",
		serverAddress, serverPort, gitOpsCluster.Namespace, gitOpsCluster.Name)

	return true, nil
}

// HasExistingServerConfig checks if any existing AddonDeploymentConfig has server address/port configured
func (r *ReconcileGitOpsCluster) HasExistingServerConfig(managedClusters []*spokeclusterv1.ManagedCluster) (bool, error) {
	for _, managedCluster := range managedClusters {
		existing := &addonv1alpha1.AddOnDeploymentConfig{}
		err := r.Get(context.Background(), types.NamespacedName{
			Name:      "gitops-addon-config",
			Namespace: managedCluster.Name,
		}, existing)

		if err != nil {
			if k8errors.IsNotFound(err) {
				// Config doesn't exist, continue checking other clusters
				continue
			}
			return false, fmt.Errorf("failed to get AddOnDeploymentConfig in namespace %s: %w", managedCluster.Name, err)
		}

		// Check if any of the existing variables contain server address or port
		for _, variable := range existing.Spec.CustomizedVariables {
			if variable.Name == "ARGOCD_AGENT_SERVER_ADDRESS" && variable.Value != "" {
				klog.V(2).Infof("found existing ARGOCD_AGENT_SERVER_ADDRESS in namespace %s", managedCluster.Name)
				return true, nil
			}
			if variable.Name == "ARGOCD_AGENT_SERVER_PORT" && variable.Value != "" {
				klog.V(2).Infof("found existing ARGOCD_AGENT_SERVER_PORT in namespace %s", managedCluster.Name)
				return true, nil
			}
		}
	}

	return false, nil
}

// DiscoverServerAddressAndPort discovers the external server address and port from the ArgoCD agent principal service
func (r *ReconcileGitOpsCluster) DiscoverServerAddressAndPort(argoNamespace string) (string, string, error) {
	// Use the existing logic from argocd_agent_certificates.go to find the principal service
	service, err := r.findArgoCDAgentPrincipalService(argoNamespace)
	if err != nil {
		return "", "", fmt.Errorf("failed to find ArgoCD agent principal service: %w", err)
	}

	// Look for LoadBalancer external endpoints
	var serverAddress string
	var serverPort string = "443" // Default HTTPS port

	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.Hostname != "" {
			serverAddress = ingress.Hostname
			klog.V(2).Infof("discovered server address from LoadBalancer hostname: %s", serverAddress)
			break
		}
		if ingress.IP != "" {
			serverAddress = ingress.IP
			klog.V(2).Infof("discovered server address from LoadBalancer IP: %s", serverAddress)
			break
		}
	}

	if serverAddress == "" {
		return "", "", fmt.Errorf("no external LoadBalancer IP or hostname found for service %s in namespace %s", service.Name, argoNamespace)
	}

	// Try to get the actual port from the service spec
	for _, port := range service.Spec.Ports {
		if port.Name == "https" || port.Port == 443 {
			serverPort = fmt.Sprintf("%d", port.Port)
			klog.V(2).Infof("discovered server port from service spec: %s", serverPort)
			break
		}
	}

	klog.Infof("discovered ArgoCD agent server endpoint: %s:%s", serverAddress, serverPort)
	return serverAddress, serverPort, nil
}

// GetManagedClusters retrieves managed cluster names from placement decision
func (r *ReconcileGitOpsCluster) GetManagedClusters(namespace string, placementref v1.ObjectReference) ([]*spokeclusterv1.ManagedCluster, error) {
	if !(placementref.Kind == "Placement" &&
		(strings.EqualFold(placementref.APIVersion, "cluster.open-cluster-management.io/v1alpha1") ||
			strings.EqualFold(placementref.APIVersion, "cluster.open-cluster-management.io/v1beta1"))) {
		klog.Error("Invalid Kind or APIVersion, kind: " + placementref.Kind + " apiVerion: " + placementref.APIVersion)
		return nil, errInvalidPlacementRef
	}

	placement := &clusterv1beta1.Placement{}
	err := r.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: placementref.Name}, placement)

	if err != nil {
		klog.Error("failed to get placement. err: ", err.Error())
		return nil, err
	}

	klog.Infof("looking for placement decisions for placement %s", placementref.Name)

	placementDecisions := &clusterv1beta1.PlacementDecisionList{}

	listopts := &client.ListOptions{Namespace: namespace}

	secretSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"cluster.open-cluster-management.io/placement": placementref.Name,
		},
	}

	placementDecisionSelectionLabel, err := utils.ConvertLabels(secretSelector)
	if err != nil {
		klog.Error("Failed to convert placement decision selector, err:", err)
		return nil, err
	}

	listopts.LabelSelector = placementDecisionSelectionLabel
	err = r.List(context.TODO(), placementDecisions, listopts)

	if err != nil {
		klog.Error("Failed to list placement decisions, err:", err)
		return nil, err
	}

	if len(placementDecisions.Items) < 1 {
		klog.Info("no placement decision found for placement: " + placementref.Name)
		return nil, errors.New("no placement decision found for placement: " + placementref.Name)
	}

	clusterList := &spokeclusterv1.ManagedClusterList{}

	err = r.List(context.TODO(), clusterList, &client.ListOptions{})
	if err != nil {
		klog.Error("Failed to list managed clusters, err:", err)
		return nil, err
	}

	clusterMap := make(map[string]*spokeclusterv1.ManagedCluster)
	for i, cluster := range clusterList.Items {
		clusterMap[cluster.Name] = &clusterList.Items[i]
	}

	clusters := make([]*spokeclusterv1.ManagedCluster, 0)

	for _, placementdecision := range placementDecisions.Items {
		klog.Info("getting cluster names from placement decision " + placementdecision.Name)

		for _, clusterDecision := range placementdecision.Status.Decisions {
			klog.Info("cluster name: " + clusterDecision.ClusterName)

			if cluster, ok := clusterMap[clusterDecision.ClusterName]; ok {
				clusters = append(clusters, cluster)
			} else {
				klog.Info("could not find managed cluster: " + clusterDecision.ClusterName)
			}
		}
	}

	return clusters, nil
}

const componentName = "application-manager"

// AddManagedClustersToArgo copies a managed cluster secret from the managed cluster namespace to ArgoCD namespace
func (r *ReconcileGitOpsCluster) AddManagedClustersToArgo(
	gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster,
	orphanSecretsList map[types.NamespacedName]string, createBlankClusterSecrets bool) error {
	var returnErrs error
	errorOccurred := false
	argoNamespace := gitOpsCluster.Spec.ArgoServer.ArgoNamespace

	nonAcmClusterSecrets, err := r.GetAllNonAcmManagedClusterSecretsInArgo(argoNamespace)
	if err != nil {
		klog.Error("failed to get all non-acm managed cluster secrets. err: ", err.Error())

		return err
	}

	for _, managedCluster := range managedClusters {
		klog.Infof("adding managed cluster %s to gitops namespace %s", managedCluster.Name, argoNamespace)

		var newSecret *v1.Secret
		msaExists := false
		managedClusterSecret := &v1.Secret{}
		secretObjectKey := types.NamespacedName{
			Name:      managedCluster.Name + clusterSecretSuffix,
			Namespace: argoNamespace,
		}
		msaSecretObjectKey := types.NamespacedName{
			Name:      managedCluster.Name + "-" + componentName + clusterSecretSuffix,
			Namespace: argoNamespace,
		}

		// Check if there are existing non-acm created cluster secrets
		if len(nonAcmClusterSecrets[managedCluster.Name]) > 0 {
			returnErr := fmt.Errorf("founding existing non-ACM ArgoCD clusters secrets for cluster: %v", managedCluster)
			klog.Error(returnErr.Error())

			returnErrs = errors.Join(returnErrs, returnErr)
			errorOccurred = true

			saveClusterSecret(orphanSecretsList, secretObjectKey, msaSecretObjectKey)

			continue
		}

		if createBlankClusterSecrets || gitOpsCluster.Spec.ManagedServiceAccountRef == "" {
			// check for a ManagedServiceAccount to see if we need to create the secret
			ManagedServiceAccount := &authv1beta1.ManagedServiceAccount{}
			ManagedServiceAccountName := types.NamespacedName{Namespace: managedCluster.Name, Name: componentName}
			err = r.Get(context.TODO(), ManagedServiceAccountName, ManagedServiceAccount)

			if err == nil {
				// get ManagedServiceAccount secret
				managedClusterSecretKey := types.NamespacedName{Name: componentName, Namespace: managedCluster.Name}
				err = r.Get(context.TODO(), managedClusterSecretKey, managedClusterSecret)

				if err == nil {
					klog.Infof("Found ManagedServiceAccount %s created by managed cluster %s", componentName, managedCluster.Name)
					msaExists = true
				} else {
					if !createBlankClusterSecrets {
						klog.Error("failed to find ManagedServiceAccount created secret application-manager")
						saveClusterSecret(orphanSecretsList, secretObjectKey, msaSecretObjectKey)
						continue
					}
				}
			} else {
				// fallback to old code
				if !createBlankClusterSecrets {
					klog.Infof("Failed to find ManagedServiceAccount CR in namespace %s", managedCluster.Name)
				}
				secretName := managedCluster.Name + clusterSecretSuffix
				managedClusterSecretKey := types.NamespacedName{Name: secretName, Namespace: managedCluster.Name}

				err = r.Get(context.TODO(), managedClusterSecretKey, managedClusterSecret)

				if err != nil {
					// try with CreateMangedClusterSecretFromManagedServiceAccount generated name
					secretName = managedCluster.Name + "-" + componentName + clusterSecretSuffix
					managedClusterSecretKey = types.NamespacedName{Name: secretName, Namespace: managedCluster.Name}
					err = r.Get(context.TODO(), managedClusterSecretKey, managedClusterSecret)
				}
			}

			// managed cluster secret doesn't need to exist for pull model
			if err != nil && !createBlankClusterSecrets {
				klog.Error("failed to get managed cluster secret. err: ", err.Error())

				errorOccurred = true
				returnErrs = errors.Join(returnErrs, err)

				saveClusterSecret(orphanSecretsList, secretObjectKey, msaSecretObjectKey)

				continue
			}

			if msaExists {
				newSecret, err = r.CreateMangedClusterSecretFromManagedServiceAccount(
					argoNamespace, managedCluster, componentName, false)
			} else {
				newSecret, err = r.CreateManagedClusterSecretInArgo(
					argoNamespace, managedClusterSecret, managedCluster, createBlankClusterSecrets)
			}

			if err != nil {
				klog.Error("failed to create managed cluster secret. err: ", err.Error())

				errorOccurred = true
				returnErrs = errors.Join(returnErrs, err)

				saveClusterSecret(orphanSecretsList, secretObjectKey, msaSecretObjectKey)

				continue
			}
		} else {
			klog.Infof("create cluster secret using managed service account: %s/%s", managedCluster.Name, gitOpsCluster.Spec.ManagedServiceAccountRef)

			newSecret, err = r.CreateMangedClusterSecretFromManagedServiceAccount(argoNamespace, managedCluster, gitOpsCluster.Spec.ManagedServiceAccountRef, true)
			if err != nil {
				klog.Error("failed to create managed cluster secret. err: ", err.Error())

				errorOccurred = true
				returnErrs = errors.Join(returnErrs, err)

				saveClusterSecret(orphanSecretsList, secretObjectKey, msaSecretObjectKey)

				continue
			}
		}

		existingManagedClusterSecret := &v1.Secret{}

		err = r.Get(context.TODO(), types.NamespacedName{Name: newSecret.Name, Namespace: newSecret.Namespace}, existingManagedClusterSecret)
		if err == nil {
			klog.Infof("updating managed cluster secret in argo namespace: %v/%v", newSecret.Namespace, newSecret.Name)

			newSecret = unionSecretData(newSecret, existingManagedClusterSecret)

			err := r.Update(context.TODO(), newSecret)

			if err != nil {
				klog.Errorf("failed to update managed cluster secret. name: %v/%v, error: %v", newSecret.Namespace, newSecret.Name, err)

				errorOccurred = true
				returnErrs = errors.Join(returnErrs, err)

				saveClusterSecret(orphanSecretsList, secretObjectKey, msaSecretObjectKey)

				continue
			}
		} else if k8errors.IsNotFound(err) {
			klog.Infof("creating managed cluster secret in argo namespace: %v/%v", newSecret.Namespace, newSecret.Name)

			err := r.Create(context.TODO(), newSecret)

			if err != nil {
				klog.Errorf("failed to create managed cluster secret. name: %v/%v, error: %v", newSecret.Namespace, newSecret.Name, err)

				errorOccurred = true
				returnErrs = errors.Join(returnErrs, err)

				saveClusterSecret(orphanSecretsList, secretObjectKey, msaSecretObjectKey)

				continue
			}
		} else {
			klog.Errorf("failed to get managed cluster secret. name: %v/%v, error: %v", newSecret.Namespace, newSecret.Name, err)

			errorOccurred = true
			returnErrs = errors.Join(returnErrs, err)

			saveClusterSecret(orphanSecretsList, secretObjectKey, msaSecretObjectKey)

			continue
		}

		// Cleanup managed cluster secret from managed cluster namespace
		if msaExists {
			longLivedSecretKey := types.NamespacedName{
				Name:      managedCluster.Name + clusterSecretSuffix,
				Namespace: managedCluster.Name,
			}
			err := r.Get(context.TODO(), longLivedSecretKey, managedClusterSecret)

			if err != nil && k8errors.IsNotFound(err) {
				klog.Infof("Long lived token secret cleaned up already")
			} else if err != nil && !k8errors.IsNotFound(err) {
				klog.Infof("Failed to get long lived token secret to cleaned up. Error: %v", err)
			} else {
				err = r.Delete(context.TODO(), managedClusterSecret)
				if err != nil {
					klog.Infof("Failed to clean up long lived token secret. Error %v", err)
				} else {
					klog.Infof("Cleaned up long lived token secret succefully")
				}
			}
		}

		// Managed cluster secret successfully created/updated - remove from orphan list
		delete(orphanSecretsList, client.ObjectKeyFromObject(newSecret))
	}

	if !errorOccurred {
		return nil
	}

	return returnErrs
}

func saveClusterSecret(orphanSecretsList map[types.NamespacedName]string, secretObjectKey, msaSecretObjectKey types.NamespacedName) {
	delete(orphanSecretsList, secretObjectKey)
	delete(orphanSecretsList, msaSecretObjectKey)
}

// CreateManagedClusterSecretInArgo creates a managed cluster secret with specific metadata in Argo namespace
func (r *ReconcileGitOpsCluster) CreateManagedClusterSecretInArgo(argoNamespace string, managedClusterSecret *v1.Secret,
	managedCluster *spokeclusterv1.ManagedCluster, createBlankClusterSecrets bool) (*v1.Secret, error) {
	// create the new cluster secret in the argocd server namespace
	var newSecret *v1.Secret

	clusterURL := ""

	if createBlankClusterSecrets {
		newSecret = &v1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      managedCluster.Name + "-" + componentName + clusterSecretSuffix,
				Namespace: argoNamespace,
				Labels: map[string]string{
					"argocd.argoproj.io/secret-type":                 "cluster",
					"apps.open-cluster-management.io/acm-cluster":    "true",
					"apps.open-cluster-management.io/cluster-name":   managedCluster.Name,
					"apps.open-cluster-management.io/cluster-server": managedCluster.Name + "-control-plane", // dummy value for pull model
				},
			},
			Type: "Opaque",
			StringData: map[string]string{
				"name":   managedCluster.Name,
				"server": "https://" + managedCluster.Name + "-control-plane", // dummy value for pull model
			},
		}
	} else {
		if string(managedClusterSecret.Data["server"]) == "" {
			clusterToken, err := getManagedClusterToken(managedClusterSecret.Data["config"])
			if err != nil {
				klog.Error(err)

				return nil, err
			}

			clusterURL, err = getManagedClusterURL(managedCluster, clusterToken)
			if err != nil {
				klog.Error(err)

				return nil, err
			}
		} else {
			clusterURL = string(managedClusterSecret.Data["server"])
		}

		labels := managedClusterSecret.GetLabels()

		newSecret = &v1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      managedClusterSecret.Name,
				Namespace: argoNamespace,
				Labels: map[string]string{
					"argocd.argoproj.io/secret-type":                 "cluster",
					"apps.open-cluster-management.io/acm-cluster":    "true",
					"apps.open-cluster-management.io/cluster-name":   labels["apps.open-cluster-management.io/cluster-name"],
					"apps.open-cluster-management.io/cluster-server": labels["apps.open-cluster-management.io/cluster-server"],
				},
			},
			Type: "Opaque",
			StringData: map[string]string{
				"config": string(managedClusterSecret.Data["config"]),
				"name":   string(managedClusterSecret.Data["name"]),
				"server": clusterURL,
			},
		}
	}

	// Collect labels to add to the secret
	// Labels created above have precedence
	for key, val := range managedCluster.Labels {
		if _, ok := newSecret.Labels[key]; !ok {
			newSecret.Labels[key] = val
		}
	}

	return newSecret, nil
}

func (r *ReconcileGitOpsCluster) CreateMangedClusterSecretFromManagedServiceAccount(argoNamespace string,
	managedCluster *spokeclusterv1.ManagedCluster, managedServiceAccountRef string, enableTLS bool) (*v1.Secret, error) {
	// Find managedserviceaccount in the managed cluster namespace
	account := &authv1beta1.ManagedServiceAccount{}
	if err := r.Get(context.TODO(), types.NamespacedName{Name: managedServiceAccountRef, Namespace: managedCluster.Name}, account); err != nil {
		klog.Errorf("failed to get managed service account: %v/%v", managedCluster.Name, managedServiceAccountRef)

		return nil, err
	}

	// Get secret from managedserviceaccount
	tokenSecretRef := account.Status.TokenSecretRef
	if tokenSecretRef == nil {
		err := fmt.Errorf("no token reference secret found in the managed service account: %v/%v", managedCluster.Name, managedServiceAccountRef)
		klog.Error(err)

		return nil, err
	}

	tokenSecret := &v1.Secret{}
	if err := r.Get(context.TODO(), types.NamespacedName{Name: tokenSecretRef.Name, Namespace: managedCluster.Name}, tokenSecret); err != nil {
		klog.Errorf("failed to get token secret: %v/%v", managedCluster.Name, tokenSecretRef.Name)

		return nil, err
	}

	clusterSecretName := fmt.Sprintf("%v-%v-cluster-secret", managedCluster.Name, managedServiceAccountRef)

	tlsClientConfig := map[string]interface{}{
		"insecure": true,
	}
	caCrt := base64.StdEncoding.EncodeToString(tokenSecret.Data["ca.crt"])

	if enableTLS {
		tlsClientConfig = map[string]interface{}{
			"insecure": false,
			"caData":   caCrt,
		}
	}

	config := map[string]interface{}{
		"bearerToken":     string(tokenSecret.Data["token"]),
		"tlsClientConfig": tlsClientConfig,
	}

	encodedConfig, err := json.Marshal(config)
	if err != nil {
		klog.Error(err, "failed to encode data for the cluster secret")

		return nil, err
	}

	clusterURL, err := getManagedClusterURL(managedCluster, string(tokenSecret.Data["token"]))
	if err != nil {
		klog.Error(err)

		return nil, err
	}

	klog.Infof("managed cluster %v, URL: %v", managedCluster.Name, clusterURL)

	// For use in label - remove the protocol and port (contains invalid characters for label)
	strippedClusterURL := clusterURL

	index := strings.Index(strippedClusterURL, "://")
	if index > 0 {
		strippedClusterURL = strippedClusterURL[index+3:]
	}

	index = strings.Index(strippedClusterURL, ":")
	if index > 0 {
		strippedClusterURL = strippedClusterURL[:index]
	}

	newSecret := &v1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterSecretName,
			Namespace: argoNamespace,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type":                 "cluster",
				"apps.open-cluster-management.io/acm-cluster":    "true",
				"apps.open-cluster-management.io/cluster-name":   managedCluster.Name,
				"apps.open-cluster-management.io/cluster-server": fmt.Sprintf("%.63s", strippedClusterURL),
			},
		},
		Type: "Opaque",
		StringData: map[string]string{
			"config": string(encodedConfig),
			"name":   managedCluster.Name,
			"server": clusterURL,
		},
	}

	// Collect labels to add to the secret
	// Labels created above have precedence
	for key, val := range managedCluster.Labels {
		if _, ok := newSecret.Labels[key]; !ok {
			newSecret.Labels[key] = val
		}
	}

	return newSecret, nil
}

func unionSecretData(newSecret, existingSecret *v1.Secret) *v1.Secret {
	// union of labels
	newLabels := newSecret.GetLabels()
	existingLabels := existingSecret.GetLabels()

	if newLabels == nil {
		newLabels = make(map[string]string)
	}

	if existingLabels == nil {
		existingLabels = make(map[string]string)
	}

	for key, val := range existingLabels {
		if _, ok := newLabels[key]; !ok {
			newLabels[key] = val
		}
	}

	newSecret.SetLabels(newLabels)

	// union of annotations (except for kubectl.kubernetes.io/last-applied-configuration)
	newAnnotations := newSecret.GetAnnotations()
	existingAnnotations := existingSecret.GetAnnotations()

	if newAnnotations == nil {
		newAnnotations = make(map[string]string)
	}

	if existingAnnotations == nil {
		existingAnnotations = make(map[string]string)
	}

	for key, val := range existingAnnotations {
		if _, ok := newAnnotations[key]; !ok {
			if key != "kubectl.kubernetes.io/last-applied-configuration" {
				newAnnotations[key] = val
			}
		}
	}

	newSecret.SetAnnotations(newAnnotations)

	// union of data
	newData := newSecret.StringData
	existingData := existingSecret.Data // api never returns stringData as the field is write-only

	if newData == nil {
		newData = make(map[string]string)
	}

	if existingData == nil {
		existingData = make(map[string][]byte)
	}

	for key, val := range existingData {
		if _, ok := newData[key]; !ok {
			newData[key] = string(val[:])
		}
	}

	newSecret.StringData = newData

	return newSecret
}

func getManagedClusterToken(dataConfig []byte) (string, error) {
	if dataConfig == nil {
		return "", fmt.Errorf("empty secrect data config")
	}

	// Unmarshal the decoded JSON into the Config struct
	var config TokenConfig
	err := json.Unmarshal(dataConfig, &config)

	if err != nil {
		return "", fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return config.BearerToken, nil
}

func getManagedClusterURL(managedCluster *spokeclusterv1.ManagedCluster, token string) (string, error) {
	clientConfigs := managedCluster.Spec.ManagedClusterClientConfigs
	if len(clientConfigs) == 0 {
		err := fmt.Errorf("no client configs found for managed cluster: %v", managedCluster.Name)

		return "", err
	}

	// If only one clientconfig, always return the first
	if len(clientConfigs) == 1 {
		return clientConfigs[0].URL, nil
	}

	for _, config := range clientConfigs {
		req, err := http.NewRequest(http.MethodGet, config.URL, nil)
		if err != nil {
			klog.Infof("error building new http request to %v", config.URL)

			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Add("Authorization", "Bearer "+token)

		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(config.CABundle)

		httpClient := http.DefaultClient

		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    caCertPool,
				MinVersion: gitopsclusterV1beta1.TLSMinVersionInt, //#nosec G402
			},
		}

		resp, err := httpClient.Do(req)
		if err == nil {
			return config.URL, nil
		}

		defer func() {
			if resp != nil {
				if err := resp.Body.Close(); err != nil {
					klog.Error("Error closing response: ", err)
				}
			}
		}()

		klog.Infof("error sending http request to %v, error: %v", config.URL, err.Error())
	}

	err := fmt.Errorf("failed to find an accessible URL for the managed cluster: %v", managedCluster.Name)

	return "", err
}

func (r *ReconcileGitOpsCluster) createNamespaceScopedResourceFromYAML(yamlString string) error {
	var obj map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlString), &obj); err != nil {
		klog.Error("failed to unmarshal yaml string: ", err)

		return err
	}

	unstructuredObj := &unstructured.Unstructured{Object: obj}

	// Get API resource information from unstructured object.
	apiResource := unstructuredObj.GroupVersionKind().GroupVersion().WithResource(
		strings.ToLower(unstructuredObj.GetKind()) + "s",
	)

	if apiResource.Resource == "policys" {
		apiResource.Resource = "policies"
	}

	namespace := unstructuredObj.GetNamespace()
	name := unstructuredObj.GetName()

	// Check if the resource already exists.
	existingObj, err := r.DynamicClient.Resource(apiResource).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err == nil {
		// Resource exists, perform an update.
		unstructuredObj.SetResourceVersion(existingObj.GetResourceVersion())
		_, err = r.DynamicClient.Resource(apiResource).Namespace(namespace).Update(
			context.TODO(),
			unstructuredObj,
			metav1.UpdateOptions{},
		)

		if err != nil {
			klog.Error("failed to update resource: ", err)

			return err
		}

		klog.Infof("resource updated: %s/%s\n", namespace, name)
	} else if k8errors.IsNotFound(err) {
		// Resource does not exist, create it.
		_, err = r.DynamicClient.Resource(apiResource).Namespace(namespace).Create(
			context.TODO(),
			unstructuredObj,
			metav1.CreateOptions{},
		)
		if err != nil {
			klog.Error("failed to create resource: ", err)

			return err
		}

		klog.Infof("resource created: %s/%s\n", namespace, name)
	} else {
		klog.Error("failed to get resource: ", err)

		return err
	}

	return nil
}

func generatePlacementYamlString(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	yamlString := fmt.Sprintf(`
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: %s
  namespace: %s
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: %s
    uid: %s
spec:
  clusterSets:
    - global
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchExpressions:
            - key: local-cluster
              operator: In
              values:
                - "true"
`,
		gitOpsCluster.Name+"-policy-local-placement", gitOpsCluster.Namespace,
		gitOpsCluster.Name, string(gitOpsCluster.UID))

	return yamlString
}

func generatePlacementBindingYamlString(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	yamlString := fmt.Sprintf(`
apiVersion: policy.open-cluster-management.io/v1
kind: PlacementBinding
metadata:
  name: %s
  namespace: %s
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: %s
    uid: %s
placementRef:
  name: %s
  kind: Placement
  apiGroup: cluster.open-cluster-management.io
subjects:
  - name: %s
    kind: Policy
    apiGroup: policy.open-cluster-management.io
`,
		gitOpsCluster.Name+"-policy-local-placement-binding", gitOpsCluster.Namespace,
		gitOpsCluster.Name, string(gitOpsCluster.UID),
		gitOpsCluster.Name+"-policy-local-placement", gitOpsCluster.Name+"-policy")

	return yamlString
}

func generatePolicyTemplateYamlString(gitOpsCluster gitopsclusterV1beta1.GitOpsCluster) string {
	yamlString := fmt.Sprintf(`
apiVersion: policy.open-cluster-management.io/v1
kind: Policy
metadata:
  name: %s
  namespace: %s
  annotations:
    policy.open-cluster-management.io/standards: NIST-CSF
    policy.open-cluster-management.io/categories: PR.PT Protective Technology
    policy.open-cluster-management.io/controls: PR.PT-3 Least Functionality
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: %s
    uid: %s
spec:
  remediationAction: enforce
  disabled: false
  policy-templates:
    - objectDefinition:
        apiVersion: policy.open-cluster-management.io/v1
        kind: ConfigurationPolicy
        metadata:
          name: %s
        spec:
          pruneObjectBehavior: DeleteIfCreated
          remediationAction: enforce
          severity: low
          object-templates-raw: |
            {{ range $placedec := (lookup "cluster.open-cluster-management.io/v1beta1" "PlacementDecision" "%s" "" "cluster.open-cluster-management.io/placement=%s").items }}
            {{ range $clustdec := $placedec.status.decisions }}
            - complianceType: musthave
              objectDefinition:
                apiVersion: authentication.open-cluster-management.io/v1alpha1
                kind: ManagedServiceAccount
                metadata:
                  name: %s
                  namespace: {{ $clustdec.clusterName }}
                spec:
                  rotation: {}
            - complianceType: musthave
              objectDefinition:
                apiVersion: rbac.open-cluster-management.io/v1alpha1
                kind: ClusterPermission
                metadata:
                  name: %s
                  namespace: {{ $clustdec.clusterName }}
                spec: {}
            {{ end }}
            {{ end }}
`,
		gitOpsCluster.Name+"-policy", gitOpsCluster.Namespace,
		gitOpsCluster.Name, string(gitOpsCluster.UID),
		gitOpsCluster.Name+"-config-policy",
		gitOpsCluster.Namespace, gitOpsCluster.Spec.PlacementRef.Name,
		gitOpsCluster.Spec.ManagedServiceAccountRef, gitOpsCluster.Name+"-cluster-permission")

	return yamlString
}

// CreateArgoCDAgentManifestWorks creates ManifestWork resources for ArgoCD agent CA secret in each managed cluster
func (r *ReconcileGitOpsCluster) CreateArgoCDAgentManifestWorks(
	gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster) error {
	argoNamespace := gitOpsCluster.Spec.ArgoServer.ArgoNamespace
	if argoNamespace == "" {
		argoNamespace = "openshift-gitops"
	}

	// Get the CA certificate from the argocd-agent-ca secret
	caCert, err := r.getArgoCDAgentCACert(argoNamespace)
	if err != nil {
		return fmt.Errorf("failed to get ArgoCD agent CA certificate: %w", err)
	}

	for _, managedCluster := range managedClusters {
		// Skip local-cluster - don't create ManifestWork for local cluster
		if managedCluster.Name == "local-cluster" {
			klog.Infof("skipping ManifestWork creation for local-cluster: %s", managedCluster.Name)
			continue
		}

		// Skip clusters with local-cluster: "true" label
		if managedCluster.Labels != nil {
			if localClusterLabel, exists := managedCluster.Labels["local-cluster"]; exists && localClusterLabel == "true" {
				klog.Infof("skipping ManifestWork creation for cluster with local-cluster=true label: %s", managedCluster.Name)
				continue
			}
		}

		manifestWork := r.createArgoCDAgentManifestWork(managedCluster.Name, argoNamespace, caCert)

		// Check if ManifestWork already exists
		existingMW := &workv1.ManifestWork{}
		err := r.Get(context.TODO(), types.NamespacedName{
			Name:      manifestWork.Name,
			Namespace: manifestWork.Namespace,
		}, existingMW)

		if err != nil {
			if k8errors.IsNotFound(err) {
				// Create new ManifestWork with proper annotations
				if manifestWork.Annotations == nil {
					manifestWork.Annotations = make(map[string]string)
				}
				manifestWork.Annotations[ArgoCDAgentPropagateCAAnnotation] = "true"

				err = r.Create(context.TODO(), manifestWork)
				if err != nil {
					klog.Errorf("failed to create ManifestWork %s/%s: %v", manifestWork.Namespace, manifestWork.Name, err)
					return err
				}

				klog.Infof("created ManifestWork %s/%s for ArgoCD agent CA", manifestWork.Namespace, manifestWork.Name)
			} else {
				return fmt.Errorf("failed to get ManifestWork %s/%s: %w", manifestWork.Namespace, manifestWork.Name, err)
			}
		} else {
			// ManifestWork exists - check if it needs updating
			needsUpdate := false

			// Check if ManifestWork was previously marked as outdated
			isOutdated := existingMW.Annotations != nil && existingMW.Annotations[ArgoCDAgentOutdatedAnnotation] == "true"
			previousPropagateCA := existingMW.Annotations != nil && existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] == "true"

			// Check if certificate data has changed
			certificateChanged := false

			if len(existingMW.Spec.Workload.Manifests) > 0 {
				// Extract the existing certificate data from the ManifestWork
				existingManifest := existingMW.Spec.Workload.Manifests[0]

				if existingManifest.RawExtension.Raw != nil {
					existingSecret := &v1.Secret{}
					err := json.Unmarshal(existingManifest.RawExtension.Raw, existingSecret)

					if err == nil {
						existingCert := string(existingSecret.Data["ca.crt"])

						if existingCert != caCert {
							certificateChanged = true

							klog.Infof("Certificate data changed for ManifestWork %s/%s", existingMW.Namespace, existingMW.Name)
						}
					}
				}
			}

			// Update if:
			// 1. ManifestWork was marked as outdated (false->true transition)
			// 2. Spec has changed
			// 3. Certificate data has changed
			if isOutdated || !previousPropagateCA || certificateChanged {
				needsUpdate = true

				klog.Infof("ManifestWork %s/%s needs update (outdated: %v, previousPropagateCA: %v, certificateChanged: %v)",
					existingMW.Namespace, existingMW.Name, isOutdated, previousPropagateCA, certificateChanged)
			}

			if needsUpdate {
				// Update the ManifestWork spec
				existingMW.Spec = manifestWork.Spec

				// Update annotations to reflect current state
				if existingMW.Annotations == nil {
					existingMW.Annotations = make(map[string]string)
				}

				delete(existingMW.Annotations, ArgoCDAgentOutdatedAnnotation)     // Remove outdated marker
				existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] = "true" // Mark as propagating

				err = r.Update(context.TODO(), existingMW)
				if err != nil {
					klog.Errorf("failed to update ManifestWork %s/%s: %v", existingMW.Namespace, existingMW.Name, err)
					return err
				}

				klog.Infof("updated ManifestWork %s/%s for ArgoCD agent CA", existingMW.Namespace, existingMW.Name)
			} else {
				// Ensure annotations are set correctly even if no update is needed
				if existingMW.Annotations == nil {
					existingMW.Annotations = make(map[string]string)
				}

				if existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] != "true" {
					existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] = "true"

					err = r.Update(context.TODO(), existingMW)
					if err != nil {
						klog.Errorf("failed to update ManifestWork annotations %s/%s: %v", existingMW.Namespace, existingMW.Name, err)
						return err
					}
				}

				klog.V(2).Infof("ManifestWork %s/%s already up to date", existingMW.Namespace, existingMW.Name)
			}
		}
	}

	return nil
}

// getArgoCDAgentCACert retrieves the CA certificate from the argocd-agent-ca secret
func (r *ReconcileGitOpsCluster) getArgoCDAgentCACert(argoNamespace string) (string, error) {
	secret := &v1.Secret{}
	err := r.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: argoNamespace,
	}, secret)

	if err != nil {
		return "", fmt.Errorf("failed to get argocd-agent-ca secret: %w", err)
	}

	// Try to get the certificate data from either tls.crt or ca.crt field
	var caCertBytes []byte

	var exists bool

	// First try tls.crt (expected from ensureArgoCDAgentCASecret)
	caCertBytes, exists = secret.Data["tls.crt"]
	if !exists {
		// Fallback to ca.crt (in case secret was created by ManifestWork)
		caCertBytes, exists = secret.Data["ca.crt"]
		if !exists {
			return "", fmt.Errorf("neither tls.crt nor ca.crt found in argocd-agent-ca secret")
		}
	}

	// Return the certificate data as-is (it's already in the correct format)
	return string(caCertBytes), nil
}

// createArgoCDAgentManifestWork creates a ManifestWork for deploying the ArgoCD agent CA secret
func (r *ReconcileGitOpsCluster) createArgoCDAgentManifestWork(clusterName, argoNamespace, caCert string) *workv1.ManifestWork {
	// Create the secret manifest
	secretManifest := &v1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: argoNamespace,
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca.crt": []byte(caCert),
		},
	}

	// Create the ManifestWork
	manifestWork := &workv1.ManifestWork{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "work.open-cluster-management.io/v1",
			Kind:       "ManifestWork",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca-mw",
			Namespace: clusterName,
		},
		Spec: workv1.ManifestWorkSpec{
			Workload: workv1.ManifestsTemplate{
				Manifests: []workv1.Manifest{
					{
						RawExtension: runtime.RawExtension{
							Object: secretManifest,
						},
					},
				},
			},
		},
	}

	return manifestWork
}

// MarkArgoCDAgentManifestWorksAsOutdated marks existing ArgoCD agent ManifestWorks as outdated
// This is called when propagateHubCA is set to false
func (r *ReconcileGitOpsCluster) MarkArgoCDAgentManifestWorksAsOutdated(
	gitOpsCluster *gitopsclusterV1beta1.GitOpsCluster, managedClusters []*spokeclusterv1.ManagedCluster) error {
	for _, managedCluster := range managedClusters {
		// Skip local-cluster - same logic as in CreateArgoCDAgentManifestWorks
		if managedCluster.Name == "local-cluster" {
			continue
		}

		// Skip clusters with local-cluster: "true" label
		if managedCluster.Labels != nil {
			if localClusterLabel, exists := managedCluster.Labels["local-cluster"]; exists && localClusterLabel == "true" {
				continue
			}
		}

		// Check if ManifestWork exists
		existingMW := &workv1.ManifestWork{}
		err := r.Get(context.TODO(), types.NamespacedName{
			Name:      "argocd-agent-ca-mw",
			Namespace: managedCluster.Name,
		}, existingMW)

		if err != nil {
			if k8errors.IsNotFound(err) {
				// ManifestWork doesn't exist, nothing to mark
				continue
			}

			return fmt.Errorf("failed to get ManifestWork argocd-agent-ca-mw/%s: %w", managedCluster.Name, err)
		}

		// Mark the ManifestWork as outdated
		if existingMW.Annotations == nil {
			existingMW.Annotations = make(map[string]string)
		}
		existingMW.Annotations[ArgoCDAgentOutdatedAnnotation] = "true"
		existingMW.Annotations[ArgoCDAgentPropagateCAAnnotation] = "false"

		err = r.Update(context.TODO(), existingMW)
		if err != nil {
			klog.Errorf("failed to mark ManifestWork %s/%s as outdated: %v", existingMW.Namespace, existingMW.Name, err)
			return err
		}

		klog.Infof("marked ManifestWork %s/%s as outdated", existingMW.Namespace, existingMW.Name)
	}

	return nil
}

// updateGitOpsClusterConditions updates conditions based on the current state while maintaining
// backward compatibility with the phase field
func (r *ReconcileGitOpsCluster) updateGitOpsClusterConditions(
	instance *gitopsclusterV1beta1.GitOpsCluster,
	phase string,
	message string,
	conditionUpdates map[string]ConditionUpdate) {

	// Always update the legacy fields for backward compatibility
	instance.Status.LastUpdateTime = metav1.Now()
	instance.Status.Phase = phase
	instance.Status.Message = message

	// Update individual conditions
	for conditionType, update := range conditionUpdates {
		instance.SetCondition(conditionType, update.Status, update.Reason, update.Message)
	}

	// Set overall Ready condition based on all other conditions
	r.updateReadyCondition(instance)
}

// ConditionUpdate represents a condition update
type ConditionUpdate struct {
	Status  metav1.ConditionStatus
	Reason  string
	Message string
}

// updateReadyCondition sets the Ready condition based on other conditions
func (r *ReconcileGitOpsCluster) updateReadyCondition(instance *gitopsclusterV1beta1.GitOpsCluster) {
	// Ready is True if all critical conditions are True and no conditions are False
	placementResolved := instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterPlacementResolved)
	clustersRegistered := instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)

	// Check if ArgoCD agent is enabled to determine if agent conditions matter
	argoCDAgentEnabled := false
	if instance.Spec.ArgoCDAgent != nil && instance.Spec.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *instance.Spec.ArgoCDAgent.Enabled
	}

	// If ArgoCD agent is enabled, also check its conditions
	agentReady := true
	certificatesReady := true
	manifestWorksApplied := true

	if argoCDAgentEnabled {
		agentReady = instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady)
		certificatesReady = instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterCertificatesReady)

		// Check if CA propagation is enabled
		propagateHubCA := true
		if instance.Spec.ArgoCDAgent.PropagateHubCA != nil {
			propagateHubCA = *instance.Spec.ArgoCDAgent.PropagateHubCA
		}

		// Only require ManifestWorks if CA propagation is enabled
		if propagateHubCA {
			manifestWorksApplied = instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied)
		}
	}

	// Check if any condition is False (indicating an error)
	hasError := false
	for _, condition := range instance.Status.Conditions {
		if condition.Status == metav1.ConditionFalse {
			hasError = true
			break
		}
	}

	if hasError {
		instance.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionFalse,
			gitopsclusterV1beta1.ReasonClusterRegistrationFailed, "One or more components have failed")
	} else if placementResolved && clustersRegistered && agentReady && certificatesReady && manifestWorksApplied {
		instance.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess, "GitOpsCluster is ready and all components are functioning correctly")
	} else {
		instance.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionUnknown,
			"InProgress", "GitOpsCluster components are still being processed")
	}
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

	// Use the correct addon manager namespace
	addonManagerNamespace := utils.GetComponentNamespace("open-cluster-management") + "-hub"

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

// ensureArgoCDAgentCASecret ensures the argocd-agent-ca secret exists in GitOps namespace
// by copying it from the multicluster-operators-application-svc-ca secret in open-cluster-management namespace
func (r *ReconcileGitOpsCluster) ensureArgoCDAgentCASecret(gitopsNamespace string) error {
	// Check if argocd-agent-ca secret already exists
	targetSecret := &v1.Secret{}
	targetSecretName := types.NamespacedName{
		Name:      "argocd-agent-ca",
		Namespace: gitopsNamespace,
	}

	err := r.Get(context.TODO(), targetSecretName, targetSecret)
	if err == nil {
		klog.Info("argocd-agent-ca secret already exists in namespace", gitopsNamespace)
		return nil
	}

	if !k8errors.IsNotFound(err) {
		return fmt.Errorf("failed to check argocd-agent-ca secret: %w", err)
	}

	klog.Info("argocd-agent-ca secret not found, copying from source secret in namespace", gitopsNamespace)

	// Get the source secret from open-cluster-management namespace
	sourceSecret := &v1.Secret{}
	sourceSecretName := types.NamespacedName{
		Name:      "multicluster-operators-application-svc-ca",
		Namespace: utils.GetComponentNamespace("open-cluster-management"),
	}

	err = r.Get(context.TODO(), sourceSecretName, sourceSecret)
	if err != nil {
		if k8errors.IsNotFound(err) {
			return fmt.Errorf("source secret multicluster-operators-application-svc-ca not found in %s namespace - ensure OCM is properly installed", sourceSecretName.Namespace)
		}

		return fmt.Errorf("failed to get source secret: %w", err)
	}

	// Create the new secret with modified name and namespace
	newSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: gitopsNamespace,
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

	err = r.Create(context.TODO(), newSecret)
	if err != nil {
		if k8errors.IsAlreadyExists(err) {
			klog.Info("argocd-agent-ca secret was created by another process in namespace", gitopsNamespace)
			return nil
		}

		return fmt.Errorf("failed to create argocd-agent-ca secret: %w", err)
	}

	klog.Info("Successfully created argocd-agent-ca secret in namespace", gitopsNamespace)

	return nil
}

func (r *ReconcileGitOpsCluster) ensureArgoCDRedisSecret(gitopsNamespace string) error {
	// Default to openshift-gitops if namespace is empty
	if gitopsNamespace == "" {
		gitopsNamespace = "openshift-gitops"
	}

	// Check if argocd-redis secret already exists
	argoCDRedisSecret := &v1.Secret{}
	argoCDRedisSecretKey := types.NamespacedName{
		Name:      "argocd-redis",
		Namespace: gitopsNamespace,
	}

	err := r.Get(context.TODO(), argoCDRedisSecretKey, argoCDRedisSecret)
	if err == nil {
		klog.Info("argocd-redis secret already exists, skipping creation")
		return nil
	}

	if !k8errors.IsNotFound(err) {
		return fmt.Errorf("failed to check argocd-redis secret: %w", err)
	}

	klog.Info("argocd-redis secret not found, creating it...")

	// Find the secret ending with "redis-initial-password"
	secretList := &v1.SecretList{}
	err = r.List(context.TODO(), secretList, client.InNamespace(gitopsNamespace))
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %s: %w", gitopsNamespace, err)
	}

	var initialPasswordSecret *v1.Secret
	for i := range secretList.Items {
		if strings.HasSuffix(secretList.Items[i].Name, "redis-initial-password") {
			initialPasswordSecret = &secretList.Items[i]
			break
		}
	}

	if initialPasswordSecret == nil {
		return fmt.Errorf("no secret found ending with 'redis-initial-password' in namespace %s", gitopsNamespace)
	}

	// Extract the admin.password value
	adminPasswordBytes, exists := initialPasswordSecret.Data["admin.password"]
	if !exists {
		return fmt.Errorf("admin.password not found in secret %s", initialPasswordSecret.Name)
	}

	// Create the argocd-redis secret
	argoCDRedisSecretNew := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-redis",
			Namespace: gitopsNamespace,
			Labels: map[string]string{
				"apps.open-cluster-management.io/gitopscluster": "true",
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"auth": adminPasswordBytes,
		},
	}

	err = r.Create(context.TODO(), argoCDRedisSecretNew)
	if err != nil {
		if k8errors.IsAlreadyExists(err) {
			klog.Info("argocd-redis secret was created by another process, continuing...")
			return nil
		}

		return fmt.Errorf("failed to create argocd-redis secret: %w", err)
	}

	klog.Info("Successfully created argocd-redis secret")

	return nil
}
