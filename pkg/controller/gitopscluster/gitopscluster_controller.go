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
		requeueInterval, err := r.reconcileGitOpsCluster(ctx, *instance, orphanGitOpsClusterSecretList)

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

// initializeConditions sets all applicable conditions to Unknown/Reconciling state at the start
func (r *ReconcileGitOpsCluster) initializeConditions(instance *gitopsclusterV1beta1.GitOpsCluster) {
	// Check if GitOps addon is enabled
	gitopsAddonEnabled := false
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.Enabled != nil {
		gitopsAddonEnabled = *instance.Spec.GitOpsAddon.Enabled
	}

	// Check if ArgoCD agent is enabled
	argoCDAgentEnabled := false
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.ArgoCDAgent != nil && instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled
	}

	// Always initialize these conditions
	r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterReady)
	r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterPlacementResolved)
	r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterArgoServerVerified)
	r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterApplicationSetResourcesReady)
	r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterPolicyTemplateReady)
	r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterClustersRegistered)

	// Initialize GitOps addon related conditions
	if gitopsAddonEnabled {
		r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterAddOnDeploymentConfigsReady)
		r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterManagedClusterAddOnsReady)
	}

	// Initialize ArgoCD agent related conditions
	if argoCDAgentEnabled {
		r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterAddOnTemplateReady)
		r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady)
		r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterCertificatesReady)

		// Check if CA propagation is enabled (default true)
		propagateHubCA := true
		if instance.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA != nil {
			propagateHubCA = *instance.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA
		}
		if propagateHubCA {
			r.initializeCondition(instance, gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied)
		}
	}
}

// initializeCondition sets a condition to Unknown if it doesn't exist yet
func (r *ReconcileGitOpsCluster) initializeCondition(instance *gitopsclusterV1beta1.GitOpsCluster, conditionType string) {
	condition := instance.GetCondition(conditionType)
	if condition == nil {
		instance.SetCondition(conditionType, metav1.ConditionUnknown, gitopsclusterV1beta1.ReasonReconciling, "Reconciliation in progress")
	}
}

func (r *ReconcileGitOpsCluster) reconcileGitOpsCluster(
	ctx context.Context,
	gitOpsCluster gitopsclusterV1beta1.GitOpsCluster,
	orphanSecretsList map[types.NamespacedName]string) (int, error) {
	instance := gitOpsCluster.DeepCopy()

	// Initialize all applicable conditions at the start
	r.initializeConditions(instance)

	// Update status with initialized conditions
	if err := r.Client.Status().Update(context.TODO(), instance); err != nil {
		klog.Errorf("failed to initialize GitOpsCluster %s conditions: %s", instance.Namespace+"/"+instance.Name, err)
		// Don't return error, continue with reconciliation
	}

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
		r.updateGitOpsClusterConditions(instance, "",
			"",
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterPolicyTemplateReady: {
					Status:  metav1.ConditionFalse,
					Reason:  gitopsclusterV1beta1.ReasonPolicyTemplateCreationFailed,
					Message: fmt.Sprintf("Failed to create policy template: %v", err),
				},
			})
		// Don't fail reconciliation for policy template, it's optional
		// Just update the condition and continue
		err2 := r.Client.Status().Update(context.TODO(), instance)
		if err2 != nil {
			klog.Warningf("failed to update GitOpsCluster %s status after policy template failure: %s", instance.Namespace+"/"+instance.Name, err2)
		}
	} else {
		r.updateGitOpsClusterConditions(instance, "",
			"",
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterPolicyTemplateReady: {
					Status:  metav1.ConditionTrue,
					Reason:  gitopsclusterV1beta1.ReasonSuccess,
					Message: "Policy template created successfully",
				},
			})
		err2 := r.Client.Status().Update(context.TODO(), instance)
		if err2 != nil {
			klog.Warningf("failed to update GitOpsCluster %s status after policy template success: %s", instance.Namespace+"/"+instance.Name, err2)
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
				gitopsclusterV1beta1.GitOpsClusterArgoServerVerified: {
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

	// ArgoCD server verified successfully
	r.updateGitOpsClusterConditions(instance, "",
		"",
		map[string]ConditionUpdate{
			gitopsclusterV1beta1.GitOpsClusterArgoServerVerified: {
				Status:  metav1.ConditionTrue,
				Reason:  gitopsclusterV1beta1.ReasonSuccess,
				Message: "ArgoCD server pod found in the specified namespace",
			},
		})
	err := r.Client.Status().Update(context.TODO(), instance)
	if err != nil {
		klog.Warningf("failed to update GitOpsCluster %s status after argo server verification: %s", instance.Namespace+"/"+instance.Name, err)
	}

	// 1a. Add configMaps to be used by ArgoCD ApplicationSets
	configMapErr := r.CreateApplicationSetConfigMaps(gitOpsCluster.Spec.ArgoServer.ArgoNamespace)
	if configMapErr != nil {
		klog.Warningf("there was a problem creating the configMaps: %v", configMapErr.Error())
	}

	// 1b. Add roles so applicationset-controller can read placementRules and placementDecisions
	rbacErr := r.CreateApplicationSetRbac(gitOpsCluster.Spec.ArgoServer.ArgoNamespace)
	if rbacErr != nil {
		klog.Warningf("there was a problem creating the role or binding: %v", rbacErr.Error())
	}

	// Update ApplicationSetResourcesReady condition
	if configMapErr != nil || rbacErr != nil {
		var errMsg string
		if configMapErr != nil && rbacErr != nil {
			errMsg = fmt.Sprintf("ConfigMap creation failed: %v; RBAC creation failed: %v", configMapErr, rbacErr)
		} else if configMapErr != nil {
			errMsg = fmt.Sprintf("ConfigMap creation failed: %v", configMapErr)
		} else {
			errMsg = fmt.Sprintf("RBAC creation failed: %v", rbacErr)
		}

		r.updateGitOpsClusterConditions(instance, "",
			"",
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterApplicationSetResourcesReady: {
					Status:  metav1.ConditionFalse,
					Reason:  gitopsclusterV1beta1.ReasonConfigMapCreationFailed,
					Message: errMsg,
				},
			})
		err = r.Client.Status().Update(context.TODO(), instance)
		if err != nil {
			klog.Warningf("failed to update GitOpsCluster %s status after ApplicationSet resources failure: %s", instance.Namespace+"/"+instance.Name, err)
		}
		// Don't fail reconciliation for ApplicationSet resources, they're optional
	} else {
		r.updateGitOpsClusterConditions(instance, "",
			"",
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterApplicationSetResourcesReady: {
					Status:  metav1.ConditionTrue,
					Reason:  gitopsclusterV1beta1.ReasonSuccess,
					Message: "ApplicationSet ConfigMaps and RBAC resources created successfully",
				},
			})
		err = r.Client.Status().Update(context.TODO(), instance)
		if err != nil {
			klog.Warningf("failed to update GitOpsCluster %s status after ApplicationSet resources success: %s", instance.Namespace+"/"+instance.Name, err)
		}
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

	// 3b. Discover and validate server address and port if ArgoCD agent is enabled
	if argoCDAgentEnabled {
		// Discover server address and port from the ArgoCD agent principal service
		err := r.DiscoverServerAddressAndPort(ctx, instance)
		if err != nil {
			klog.Errorf("failed to discover server address and port: %v", err)

			msg := fmt.Sprintf("Failed to discover ArgoCD agent server address and port: %v", err)
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
				klog.Errorf("failed to update GitOpsCluster %s status after server discovery failure: %s", instance.Namespace+"/"+instance.Name, err2)
				return 3, err2
			}

			return 3, err
		}

		// Validate that serverAddress and serverPort are now populated
		if instance.Spec.GitOpsAddon == nil || instance.Spec.GitOpsAddon.ArgoCDAgent == nil ||
			instance.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress == "" ||
			instance.Spec.GitOpsAddon.ArgoCDAgent.ServerPort == "" {

			errMsg := "ArgoCD agent requires serverAddress and serverPort fields to be populated. Server discovery failed to populate these fields. Please ensure the ArgoCD agent principal service is properly configured with a LoadBalancer"
			klog.Error(errMsg)

			r.updateGitOpsClusterConditions(instance, "failed", errMsg,
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonInvalidConfiguration,
						Message: errMsg,
					},
				})

			err2 := r.Client.Status().Update(context.TODO(), instance)
			if err2 != nil {
				klog.Errorf("failed to update GitOpsCluster %s status after validation failure: %s", instance.Namespace+"/"+instance.Name, err2)
				return 3, err2
			}

			return 3, fmt.Errorf("%s", errMsg)
		}

		klog.Infof("ArgoCD agent server endpoint configured: %s:%s",
			instance.Spec.GitOpsAddon.ArgoCDAgent.ServerAddress,
			instance.Spec.GitOpsAddon.ArgoCDAgent.ServerPort)
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
		// Ensure addon-manager-controller RBAC resources exist in ArgoCD namespace (only needed for ArgoCD agent mode)
		if argoCDAgentEnabled {
			argoNamespace := instance.Namespace
			if argoNamespace == "" {
				argoNamespace = "openshift-gitops"
			}

			if err := r.ensureAddonManagerRBAC(argoNamespace); err != nil {
				klog.Errorf("failed to ensure addon-manager-controller RBAC resources in namespace %s: %v", argoNamespace, err)
				r.updateGitOpsClusterConditions(instance, "failed",
					fmt.Sprintf("Failed to configure required RBAC permissions: %v", err),
					map[string]ConditionUpdate{
						gitopsclusterV1beta1.GitOpsClusterGitOpsAddonPrereqsReady: {
							Status:  metav1.ConditionFalse,
							Reason:  gitopsclusterV1beta1.ReasonRBACSetupFailed,
							Message: fmt.Sprintf("Failed to create necessary Role and RoleBinding resources in ArgoCD namespace '%s'. Please verify the controller has sufficient permissions: %v", argoNamespace, err),
						},
					})
				err2 := r.Client.Status().Update(context.TODO(), instance)
				if err2 != nil {
					klog.Errorf("failed to update GitOpsCluster %s status after RBAC setup failure: %s", instance.Namespace+"/"+instance.Name, err2)
					return 3, err2
				}
				return 3, err
			}
			klog.Infof("Successfully ensured addon-manager-controller RBAC resources in ArgoCD namespace %s for ArgoCD agent mode", argoNamespace)

			// Note: ArgoCD agent CA secret will be created later via certrotation
			// when EnsureArgoCDAgentCASecret is called during certificate management
		}

		// Ensure AddOnTemplate for this GitOpsCluster
		err = r.EnsureAddOnTemplate(instance)
		if err != nil {
			klog.Errorf("failed to ensure AddOnTemplate for GitOpsCluster %s/%s: %v", instance.Namespace, instance.Name, err)
			r.updateGitOpsClusterConditions(instance, "failed",
				fmt.Sprintf("Failed to create addon template: %v", err),
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterAddOnTemplateReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonInvalidConfiguration,
						Message: fmt.Sprintf("Failed to create the addon template required for deploying GitOps components to managed clusters: %v", err),
					},
				})
			err2 := r.Client.Status().Update(context.TODO(), instance)
			if err2 != nil {
				klog.Errorf("failed to update GitOpsCluster %s status after AddOnTemplate failure: %s", instance.Namespace+"/"+instance.Name, err2)
				return 3, err2
			}
			return 3, err
		}

		r.updateGitOpsClusterConditions(instance, "", "",
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterAddOnTemplateReady: {
					Status:  metav1.ConditionTrue,
					Reason:  gitopsclusterV1beta1.ReasonSuccess,
					Message: "AddOnTemplate created/updated successfully",
				},
			})
		err2 := r.Client.Status().Update(context.TODO(), instance)
		if err2 != nil {
			klog.Warningf("failed to update GitOpsCluster %s status after AddOnTemplate success: %s", instance.Namespace+"/"+instance.Name, err2)
		}

		// Track addon creation results
		var configFailures []string
		var addonFailures []string
		successCount := 0

		for _, managedCluster := range managedClusters {
			// Skip local-cluster - addon not needed for hub cluster
			if IsLocalCluster(managedCluster) {
				klog.Infof("skipping addon creation for local-cluster: %s", managedCluster.Name)
				continue
			}

			clusterFailed := false

			err = r.CreateAddOnDeploymentConfig(instance, managedCluster.Name)
			if err != nil {
				klog.Errorf("failed to create AddOnDeploymentConfig for managed cluster %s: %v", managedCluster.Name, err)
				configFailures = append(configFailures, managedCluster.Name)
				clusterFailed = true
			}

			err = r.EnsureManagedClusterAddon(managedCluster.Name, instance)
			if err != nil {
				klog.Errorf("failed to ensure ManagedClusterAddon for managed cluster %s: %v", managedCluster.Name, err)
				addonFailures = append(addonFailures, managedCluster.Name)
				clusterFailed = true
			}

			if !clusterFailed {
				successCount++
			}
		}

		// Update AddOnDeploymentConfigsReady condition
		if len(configFailures) > 0 {
			r.updateGitOpsClusterConditions(instance, "",
				"",
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterAddOnDeploymentConfigsReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonConfigCreationFailed,
						Message: fmt.Sprintf("Failed to create AddOnDeploymentConfig for clusters: %v", strings.Join(configFailures, ", ")),
					},
				})
		} else {
			r.updateGitOpsClusterConditions(instance, "",
				"",
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterAddOnDeploymentConfigsReady: {
						Status:  metav1.ConditionTrue,
						Reason:  gitopsclusterV1beta1.ReasonSuccess,
						Message: fmt.Sprintf("Successfully created AddOnDeploymentConfigs for %d managed clusters", successCount),
					},
				})
		}

		// Update ManagedClusterAddOnsReady condition
		if len(addonFailures) > 0 {
			r.updateGitOpsClusterConditions(instance, "",
				"",
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterManagedClusterAddOnsReady: {
						Status:  metav1.ConditionFalse,
						Reason:  gitopsclusterV1beta1.ReasonAddonCreationFailed,
						Message: fmt.Sprintf("Failed to create/update ManagedClusterAddon for clusters: %v", strings.Join(addonFailures, ", ")),
					},
				})
		} else {
			r.updateGitOpsClusterConditions(instance, "",
				"",
				map[string]ConditionUpdate{
					gitopsclusterV1beta1.GitOpsClusterManagedClusterAddOnsReady: {
						Status:  metav1.ConditionTrue,
						Reason:  gitopsclusterV1beta1.ReasonSuccess,
						Message: fmt.Sprintf("Successfully created/updated ManagedClusterAddons for %d managed clusters", successCount),
					},
				})
		}

		// Update status
		err3 := r.Client.Status().Update(context.TODO(), instance)
		if err3 != nil {
			klog.Warningf("failed to update GitOpsCluster %s status after addon creation: %s", instance.Namespace+"/"+instance.Name, err3)
		}

		// If there were failures, return error to requeue
		if len(configFailures) > 0 || len(addonFailures) > 0 {
			return 3, fmt.Errorf("failed to create addons for some clusters - configs: %v, addons: %v", configFailures, addonFailures)
		}
	}

	// Ensure ArgoCD agent certificates BEFORE creating clusters (if argoCDAgent is enabled)
	if argoCDAgentEnabled {
		// Ensure CA certificate first - needed for cluster creation
		err = r.EnsureArgoCDAgentCASecret(context.TODO(), instance)
		if err != nil {
			klog.Errorf("failed to ensure ArgoCD agent CA certificate: %v", err)

			msg := fmt.Sprintf("Failed to generate or rotate the CA certificate for ArgoCD agent communication: %v", err)
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
		klog.Infof("Successfully ensured ArgoCD agent CA certificate")
	}

	// Handle cluster addition based on mode
	if argoCDAgentEnabled {
		// For ArgoCD agent mode, create agent cluster secrets - agent mode ALWAYS wins over all other options
		klog.Infof("ArgoCD agent mode enabled - agent cluster secrets will override any existing cluster secrets")

		err = r.CreateArgoCDAgentClusters(instance, managedClusters, orphanSecretsList)
		if err != nil {
			klog.Errorf("failed to create ArgoCD agent clusters: %v", err)

			msg := fmt.Sprintf("Failed to register managed clusters with ArgoCD agent. This is required for the agent to connect to managed clusters: %v", err)
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

		// Agent cluster creation succeeded - update the ClustersRegistered condition
		klog.Infof("Successfully created ArgoCD agent cluster secrets for %d managed clusters", len(managedClusters))

		r.updateGitOpsClusterConditions(instance, "successful", "ArgoCD agent clusters registered successfully",
			map[string]ConditionUpdate{
				gitopsclusterV1beta1.GitOpsClusterClustersRegistered: {
					Status:  metav1.ConditionTrue,
					Reason:  gitopsclusterV1beta1.ReasonSuccess,
					Message: fmt.Sprintf("Successfully registered %d managed clusters with ArgoCD agent configuration", len(managedClusters)),
				},
			})

		err2 := r.Client.Status().Update(context.TODO(), instance)
		if err2 != nil {
			klog.Errorf("failed to update GitOpsCluster %s status after successful agent cluster registration: %s", instance.Namespace+"/"+instance.Name, err2)
			// Don't return error here as the cluster registration actually succeeded
		}
	} else {
		// For traditional mode, use the existing logic
		err = r.AddManagedClustersToArgo(instance, managedClusters, orphanSecretsList, createBlankClusterSecrets)

		if err != nil {
			klog.Info("failed to add managed clusters to argo")

			msg := fmt.Sprintf("Failed to register managed clusters with ArgoCD. Please verify the managed cluster credentials and network connectivity: %v", err)
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
	}

	// 5. Propagate ArgoCD agent CA secret to managed clusters via ManifestWork
	if argoCDAgentEnabled {
		// Always propagate the CA secret when it exists - ManifestWorks are never deleted
		err = r.PropagateHubCA(instance, managedClusters)
		if err != nil {
			klog.Errorf("failed to propagate ArgoCD agent CA to managed clusters: %v", err)

			msg := fmt.Sprintf("Failed to distribute ArgoCD agent CA certificate to managed clusters. Please verify the argocd-agent-ca secret exists and managed clusters are accessible: %v", err)
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
	}

	// 6. Ensure additional ArgoCD agent certificates if argoCDAgent is enabled
	if argoCDAgentEnabled {
		// CA certificate was already ensured above before cluster creation
		// Now ensure Principal TLS certificate
		err = r.EnsureArgoCDAgentPrincipalTLSCert(context.TODO(), instance)
		if err != nil {
			klog.Errorf("failed to ensure ArgoCD agent principal TLS certificate: %v", err)

			msg := fmt.Sprintf("Failed to generate or rotate the TLS certificate for ArgoCD agent principal service: %v", err)
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

		// 6c. Ensure Resource Proxy TLS certificate
		err = r.EnsureArgoCDAgentResourceProxyTLSCert(context.TODO(), instance)
		if err != nil {
			klog.Errorf("failed to ensure ArgoCD agent resource proxy TLS certificate: %v", err)

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

	// Add GitOps addon specific conditions if enabled
	if gitopsAddonEnabled {
		// These were already set in the addon creation loop above
		// Just ensure they're marked as not required if addon is disabled below
	} else {
		// GitOps addon is disabled, mark addon conditions as not required
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterAddOnDeploymentConfigsReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonNotRequired,
			Message: "AddOnDeploymentConfigs not required (GitOps addon disabled)",
		}
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterManagedClusterAddOnsReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonNotRequired,
			Message: "ManagedClusterAddons not required (GitOps addon disabled)",
		}
	}

	// Add ArgoCD agent specific conditions if enabled
	if argoCDAgentEnabled {
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonSuccess,
			Message: "ArgoCD agent prerequisites (RBAC, CA secret, Redis secret, and JWT secret) are ready",
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

		// AddOnTemplateReady was set earlier in the addon template creation
	} else {
		// ArgoCD agent is disabled, set conditions accordingly
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterAddOnTemplateReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonNotRequired,
			Message: "AddOnTemplate not required (ArgoCD agent disabled)",
		}
		conditionUpdates[gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady] = ConditionUpdate{
			Status:  metav1.ConditionTrue,
			Reason:  gitopsclusterV1beta1.ReasonNotRequired,
			Message: "ArgoCD agent prerequisites not required (agent disabled)",
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

// IsLocalCluster checks if a managed cluster is the local cluster (hub cluster)
// Returns true if the cluster name is "local-cluster" or has the label "local-cluster=true"
func IsLocalCluster(managedCluster *spokeclusterv1.ManagedCluster) bool {
	if managedCluster == nil {
		return false
	}

	// Check by name
	if managedCluster.Name == "local-cluster" {
		return true
	}

	// Check by label
	if managedCluster.Labels != nil {
		if localClusterLabel, exists := managedCluster.Labels["local-cluster"]; exists && localClusterLabel == "true" {
			return true
		}
	}

	return false
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
	// Check configuration flags
	gitopsAddonEnabled := false
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.Enabled != nil {
		gitopsAddonEnabled = *instance.Spec.GitOpsAddon.Enabled
	}

	argoCDAgentEnabled := false
	if instance.Spec.GitOpsAddon != nil && instance.Spec.GitOpsAddon.ArgoCDAgent != nil && instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled != nil {
		argoCDAgentEnabled = *instance.Spec.GitOpsAddon.ArgoCDAgent.Enabled
	}

	// Check if any condition is False (indicating an error)
	hasError := false
	failedConditions := []string{}
	for _, condition := range instance.Status.Conditions {
		if condition.Type != gitopsclusterV1beta1.GitOpsClusterReady && condition.Status == metav1.ConditionFalse {
			hasError = true
			failedConditions = append(failedConditions, condition.Type)
		}
	}

	if hasError {
		instance.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionFalse,
			gitopsclusterV1beta1.ReasonClusterRegistrationFailed,
			fmt.Sprintf("One or more components have failed: %v", strings.Join(failedConditions, ", ")))
		return
	}

	// Check all critical conditions are True (or NotRequired)
	criticalConditions := []string{
		gitopsclusterV1beta1.GitOpsClusterPlacementResolved,
		gitopsclusterV1beta1.GitOpsClusterArgoServerVerified,
		gitopsclusterV1beta1.GitOpsClusterClustersRegistered,
	}

	// Add optional conditions that should be checked
	// ApplicationSetResourcesReady and PolicyTemplateReady are optional, only check if they exist and are not Unknown
	for _, condType := range []string{
		gitopsclusterV1beta1.GitOpsClusterApplicationSetResourcesReady,
		gitopsclusterV1beta1.GitOpsClusterPolicyTemplateReady,
	} {
		cond := instance.GetCondition(condType)
		if cond != nil && cond.Status != metav1.ConditionUnknown {
			criticalConditions = append(criticalConditions, condType)
		}
	}

	// Add GitOps addon conditions if enabled
	if gitopsAddonEnabled {
		criticalConditions = append(criticalConditions,
			gitopsclusterV1beta1.GitOpsClusterAddOnDeploymentConfigsReady,
			gitopsclusterV1beta1.GitOpsClusterManagedClusterAddOnsReady,
		)
	}

	// Add ArgoCD agent conditions if enabled
	if argoCDAgentEnabled {
		criticalConditions = append(criticalConditions,
			gitopsclusterV1beta1.GitOpsClusterAddOnTemplateReady,
			gitopsclusterV1beta1.GitOpsClusterArgoCDAgentPrereqsReady,
			gitopsclusterV1beta1.GitOpsClusterCertificatesReady,
		)

		// Check if CA propagation is enabled
		propagateHubCA := true
		if instance.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA != nil {
			propagateHubCA = *instance.Spec.GitOpsAddon.ArgoCDAgent.PropagateHubCA
		}

		// Only require ManifestWorks if CA propagation is enabled
		if propagateHubCA {
			criticalConditions = append(criticalConditions, gitopsclusterV1beta1.GitOpsClusterManifestWorksApplied)
		}
	}

	// Check all critical conditions
	allReady := true
	for _, condType := range criticalConditions {
		cond := instance.GetCondition(condType)
		if cond == nil || cond.Status == metav1.ConditionUnknown {
			allReady = false
			break
		}
		// True or NotRequired (both are acceptable)
		if cond.Status != metav1.ConditionTrue {
			allReady = false
			break
		}
	}

	if allReady {
		instance.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionTrue,
			gitopsclusterV1beta1.ReasonSuccess, "GitOpsCluster is ready and all components are functioning correctly")
	} else {
		instance.SetCondition(gitopsclusterV1beta1.GitOpsClusterReady, metav1.ConditionUnknown,
			gitopsclusterV1beta1.ReasonReconciling, "GitOpsCluster components are still being processed")
	}
}
