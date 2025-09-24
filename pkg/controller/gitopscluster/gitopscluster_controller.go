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
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"open-cluster-management.io/multicloud-integrations/pkg/utils"

	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
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

	// Validate GitOpsAddon and ArgoCDAgent spec fields
	if instance.Spec.GitOpsAddon != nil {
		if err := r.ValidateGitOpsAddonSpec(instance.Spec.GitOpsAddon); err != nil {
			klog.Errorf("GitOpsAddon spec validation failed: %v", err)

			r.updateGitOpsClusterConditions(instance, "failed",
				fmt.Sprintf("GitOpsAddon spec validation failed: %v", err),
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonInvalidConfiguration,
						Message: fmt.Sprintf("GitOpsAddon spec validation failed: %v", err),
					},
				})

			err2 := r.Client.Status().Update(context.TODO(), instance)
			if err2 != nil {
				klog.Errorf("failed to update GitOpsCluster %s status after validation failure: %s", instance.Namespace+"/"+instance.Name, err2)
				return 1, err2
			}

			return 1, err
		}
	}

	annotations := instance.GetAnnotations()

	// Create default policy template
	if err := r.CreatePolicyTemplate(instance); err != nil {
		klog.Error("failed to create policy template: ", err)
	}

	// 1. Verify that spec.argoServer.argoNamespace is a valid ArgoCD namespace
	// skipArgoNamespaceVerify annotation just in case the service labels we use for verification change in future
	if !r.VerifyArgocdNamespace(gitOpsCluster.Spec.ArgoServer.ArgoNamespace) &&
		annotations["skipArgoNamespaceVerify"] != "true" {
		klog.Info("invalid argocd namespace because argo server pod was not found")

		r.updateGitOpsClusterConditions(instance, "failed",
			"invalid gitops namespace because argo server pod was not found",
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady: {
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

	// 3. Check if GitOps addon and ArgoCD agent are enabled
	gitopsAddonEnabled, argoCDAgentEnabled := r.GetGitOpsAddonStatus(instance)

	// 3a. Ensure secrets exists if ArgoCD agent is enabled
	if argoCDAgentEnabled {
		// Ensure argocd-redis secret exists if ArgoCD agent is enabled
		err = r.ensureArgoCDRedisSecret(instance.Spec.ArgoServer.ArgoNamespace)
		if err != nil {
			klog.Errorf("failed to ensure argocd-redis secret: %v", err)

			msg := err.Error()
			if len(msg) > maxStatusMsgLen {
				msg = msg[:maxStatusMsgLen]
			}

			r.updateGitOpsClusterConditions(instance, "failed", msg,
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady: {
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

		// Ensure argocd-agent-jwt secret exists if ArgoCD agent is enabled
		err = r.ensureArgoCDAgentJWTSecret(instance.Spec.ArgoServer.ArgoNamespace)
		if err != nil {
			klog.Errorf("failed to ensure argocd-agent-jwt secret: %v", err)

			msg := err.Error()
			if len(msg) > maxStatusMsgLen {
				msg = msg[:maxStatusMsgLen]
			}

			r.updateGitOpsClusterConditions(instance, "failed", msg,
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonInvalidConfiguration,
						Message: msg,
					},
				})

			err2 := r.Client.Status().Update(context.TODO(), instance)
			if err2 != nil {
				klog.Errorf("failed to update GitOpsCluster %s status after JWT secret failure: %s", instance.Namespace+"/"+instance.Name, err2)
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
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady: {
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
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady: {
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

		klog.Infof("Successfully ensured ArgoCD agent prerequisites (RBAC, CA secret, Redis secret, and JWT secret) for GitOpsCluster %s/%s", instance.Namespace, instance.Name)
	}

	// Check if Hub CA propagation is enabled (default true)
	propagateHubCA := true
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.ArgoCDAgent != nil && instance.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA != nil {
		propagateHubCA = *instance.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA
	}

	// 4. Copy secret contents from the managed cluster namespaces and create the secret in spec.argoServer.argoNamespace
	// if spec.createBlankClusterSecrets is true then do err on missing secret from the managed cluster namespace
	// When gitopsAddon is enabled, always create blank cluster secrets regardless of createBlankClusterSecrets field value
	createBlankClusterSecrets := false
	if gitopsAddonEnabled {
		// When gitopsAddon is enabled, always create blank cluster secrets regardless of the createBlankClusterSecrets field value
		createBlankClusterSecrets = true
	} else {
		// When gitopsAddon is not enabled, respect the createBlankClusterSecrets field value
		if instance.Spec.CreateBlankClusterSecrets != nil {
			createBlankClusterSecrets = *instance.Spec.CreateBlankClusterSecrets
		}
	}

	// Create AddOnDeploymentConfig and ManagedClusterAddon for each managed cluster namespace if GitOps addon is enabled
	if gitopsAddonEnabled {
		for _, managedCluster := range managedClusters {
			err = r.CreateAddOnDeploymentConfig(instance, managedCluster.Name)
			if err != nil {
				klog.Errorf("failed to create AddOnDeploymentConfig for managed cluster %s: %v", managedCluster.Name, err)
			}

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
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonSuccess,
			Message: "ArgoCD agent prerequisites (RBAC, CA secret, Redis secret, and JWT secret) are ready",
		}
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady] = ConditionUpdate{
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
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonNotRequired,
			Message: "ArgoCD agent prerequisites not required (agent disabled)",
		}
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady] = ConditionUpdate{
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

	return nil
}

// ValidateGitOpsAddonSpec validates the GitOpsAddon specification
func (r *ReconcileGitOpsCluster) ValidateGitOpsAddonSpec(gitOpsAddon *gitopsclusterV1beta1.GitOpsAddonSpec) error {
	if gitOpsAddon == nil {
		return nil
	}

	// Validate ReconcileScope field
	if gitOpsAddon.ReconcileScope != "" {
		validScopes := []string{"All-Namespaces", "Single-Namespace"}
		isValidScope := false
		for _, validScope := range validScopes {
			if gitOpsAddon.ReconcileScope == validScope {
				isValidScope = true
				break
			}
		}
		if !isValidScope {
			return fmt.Errorf("invalid ReconcileScope '%s': must be one of %v", gitOpsAddon.ReconcileScope, validScopes)
		}
	}

	// Validate Action field
	if gitOpsAddon.Action != "" {
		validActions := []string{"Install", "Delete-Operator"}
		isValidAction := false
		for _, validAction := range validActions {
			if gitOpsAddon.Action == validAction {
				isValidAction = true
				break
			}
		}
		if !isValidAction {
			return fmt.Errorf("invalid Action '%s': must be one of %v", gitOpsAddon.Action, validActions)
		}
	}

	// Validate nested ArgoCDAgent spec
	if err := r.ValidateArgoCDAgentSpec(gitOpsAddon.ArgoCDAgent); err != nil {
		return fmt.Errorf("ArgoCDAgent validation failed: %w", err)
	}

	return nil
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
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.ArgoCDAgent != nil && instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled
	}

	// If ArgoCD agent is enabled, also check its conditions
	agentPrereqsReady := true
	certificatesReady := true
	manifestWorksApplied := true

	if argoCDAgentEnabled {
		agentPrereqsReady = instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady)
		certificatesReady = instance.IsConditionTrue(gitopsclusterV1beta1.GitOpsClusterCertificatesReady)

		// Check if CA propagation is enabled
		propagateHubCA := true
		if instance.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA != nil {
			propagateHubCA = *instance.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA
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
	} else if placementResolved && clustersRegistered && agentPrereqsReady && certificatesReady && manifestWorksApplied {
		instance.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess, "GitOpsCluster is ready and all components are functioning correctly")
	} else {
		instance.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionUnknown,
			"InProgress", "GitOpsCluster components are still being processed")
	}
}
