// Copyright 2019 The Kubernetes Authors.
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

package utils

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io/ioutil"
	"path/filepath"
	"reflect"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	spokeClusterV1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// #nosec G101
	ACMClusterSecretLabel = "apps.open-cluster-management.io/secret-type"
	// #nosec G101
	ArgocdClusterSecretLabel = "apps.open-cluster-management.io/acm-cluster"
	// #nosec G101
	ACMClusterNameLabel = "apps.open-cluster-management.io/cluster-name"
)

// ClusterPredicateFunc defines predicate function for cluster related watch, main purpose is to ignore heartbeat without change
var ClusterPredicateFunc = predicate.TypedFuncs[*spokeClusterV1.ManagedCluster]{
	UpdateFunc: func(e event.TypedUpdateEvent[*spokeClusterV1.ManagedCluster]) bool {
		oldcl := e.ObjectOld
		newcl := e.ObjectNew

		//if managed cluster is being deleted
		if !reflect.DeepEqual(oldcl.DeletionTimestamp, newcl.DeletionTimestamp) {
			return true
		}

		if !reflect.DeepEqual(oldcl.Labels, newcl.Labels) {
			return true
		}

		if !reflect.DeepEqual(oldcl.Spec.ManagedClusterClientConfigs, newcl.Spec.ManagedClusterClientConfigs) {
			return true
		}

		oldcondMap := make(map[string]metav1.ConditionStatus)
		for _, cond := range oldcl.Status.Conditions {
			oldcondMap[cond.Type] = cond.Status
		}
		for _, cond := range newcl.Status.Conditions {
			oldcondst, ok := oldcondMap[cond.Type]
			if !ok || oldcondst != cond.Status {
				return true
			}
			delete(oldcondMap, cond.Type)
		}

		if len(oldcondMap) > 0 {
			return true
		}

		klog.V(1).Info("Out Cluster Predicate Func ", oldcl.Name, " with false possitive")
		return false
	},
}

var GitOpsClusterPredicateFunc = predicate.TypedFuncs[*gitopsclusterV1beta1.GitOpsCluster]{
	UpdateFunc: func(e event.TypedUpdateEvent[*gitopsclusterV1beta1.GitOpsCluster]) bool {
		oldGitOpsCluster := e.ObjectOld
		newGitOpsCluster := e.ObjectNew

		return !reflect.DeepEqual(oldGitOpsCluster.Spec, newGitOpsCluster.Spec)
	},
}

var PlacementDecisionPredicateFunc = predicate.TypedFuncs[*clusterv1beta1.PlacementDecision]{
	CreateFunc: func(e event.TypedCreateEvent[*clusterv1beta1.PlacementDecision]) bool {
		klog.Infof("placement decision created, %v/%v", e.Object.Namespace, e.Object.Name)
		return true
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*clusterv1beta1.PlacementDecision]) bool {
		klog.Infof("placement decision deleted, %v/%v", e.Object.Namespace, e.Object.Name)
		return true
	},
	UpdateFunc: func(e event.TypedUpdateEvent[*clusterv1beta1.PlacementDecision]) bool {
		oldDecision := e.ObjectOld
		newDecision := e.ObjectNew

		klog.Infof("placement decision updated, %v/%v", newDecision.Namespace, newDecision.Name)

		return !reflect.DeepEqual(oldDecision.Status, newDecision.Status)
	},
}

var ManagedClusterSecretPredicateFunc = predicate.TypedFuncs[*v1.Secret]{
	UpdateFunc: func(e event.TypedUpdateEvent[*v1.Secret]) bool {
		_, isSecretInArgo := e.ObjectNew.GetLabels()[ArgocdClusterSecretLabel]

		if isSecretInArgo {
			klog.Infof("Managed cluster secret in ArgoCD namespace updated: %v/%v", e.ObjectNew.GetNamespace(), e.ObjectNew.GetName())

			// No reconcile if the secret is in argo server namespae
			return false
		}

		return true
	},
	CreateFunc: func(e event.TypedCreateEvent[*v1.Secret]) bool {
		_, isSecretInArgo := e.Object.GetLabels()[ArgocdClusterSecretLabel]

		if isSecretInArgo {
			klog.Infof("Managed cluster secret in ArgoCD namespace created: %v/%v", e.Object.GetNamespace(), e.Object.GetName())

			// No reconcile if the secret is in argo server namespae
			return false
		}

		return true
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*v1.Secret]) bool {
		_, isSecretInArgo := e.Object.GetLabels()[ArgocdClusterSecretLabel]

		if isSecretInArgo {
			klog.Infof("Managed cluster secret in ArgoCD namespace deleted: %v/%v", e.Object.GetNamespace(), e.Object.GetName())

			return true
		}

		// No reconcile if the secret is deleted from managed cluster namespae. Let placement decision update
		// trigger reconcile
		return false
	},
}

var ManagedServiceAccountPredicateFunc = predicate.TypedFuncs[*authv1beta1.ManagedServiceAccount]{
	UpdateFunc: func(e event.TypedUpdateEvent[*authv1beta1.ManagedServiceAccount]) bool {
		oldmsa := e.ObjectOld
		newmsa := e.ObjectNew

		// Fire on TokenSecretRef changes (new secret name or LastRefreshTimestamp bump).
		// LastRefreshTimestamp is updated by the addon agent whenever the token is refreshed
		// in-place (same secret name, new token payload), so this catches the common rotation
		// case where only the token data changes.
		secretRefUpdated := !reflect.DeepEqual(oldmsa.Status.TokenSecretRef, newmsa.Status.TokenSecretRef)

		// Also fire when ExpirationTimestamp changes, which signals that the managed-serviceaccount
		// controller has issued a new token and updated the expiry window.
		expirationUpdated := !reflect.DeepEqual(oldmsa.Status.ExpirationTimestamp, newmsa.Status.ExpirationTimestamp)

		triggered := secretRefUpdated || expirationUpdated

		klog.Infof("Managed service account (%v/%v) status changed: tokenSecretRef=%v expirationTimestamp=%v -> reconcile=%v",
			newmsa.GetNamespace(), newmsa.GetName(), secretRefUpdated, expirationUpdated, triggered)

		return triggered
	},
	CreateFunc: func(e event.TypedCreateEvent[*authv1beta1.ManagedServiceAccount]) bool {
		msa := e.Object

		if msa.Status.TokenSecretRef != nil {
			return true
		}

		klog.Infof("Managed service account doesn't have tokenSecrefRef: %v/%v", e.Object.GetNamespace(), e.Object.GetName())

		return false
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*authv1beta1.ManagedServiceAccount]) bool {
		msa := e.Object

		if msa.Status.TokenSecretRef != nil {
			return true
		}

		klog.Infof("Managed service account doesn't have tokenSecrefRef: %v/%v", e.Object.GetNamespace(), e.Object.GetName())

		return false
	},
}

// MSATokenSecretPredicateFunc fires when the token payload of an MSA token secret changes.
// MSA token secrets carry the label
// "authentication.open-cluster-management.io/is-managed-serviceaccount=true" and live in
// managed-cluster namespaces on the hub. The managed-serviceaccount addon agent refreshes
// these secrets in-place (same name, new token bytes), so a TokenSecretRef name change is
// NOT triggered in that scenario. This predicate closes that gap by watching the raw token
// bytes directly. It is used together with a secondary cache scoped to the MSA label so
// that the controller does not watch all secrets cluster-wide.
var MSATokenSecretPredicateFunc = predicate.TypedFuncs[*v1.Secret]{
	UpdateFunc: func(e event.TypedUpdateEvent[*v1.Secret]) bool {
		oldToken := e.ObjectOld.Data["token"]
		newToken := e.ObjectNew.Data["token"]
		if len(oldToken) == len(newToken) && subtle.ConstantTimeCompare(oldToken, newToken) == 1 {
			return false
		}
		klog.Infof("MSA token secret %v/%v: token data changed, triggering reconcile",
			e.ObjectNew.GetNamespace(), e.ObjectNew.GetName())
		return true
	},
	CreateFunc: func(e event.TypedCreateEvent[*v1.Secret]) bool {
		return len(e.Object.Data["token"]) > 0
	},
	DeleteFunc: func(e event.TypedDeleteEvent[*v1.Secret]) bool {
		return true
	},
}

// Base64StringDecode decode a base64 string
func Base64StringDecode(encodedStr string) (string, error) {
	decodedBytes, err := base64.StdEncoding.DecodeString(encodedStr)
	if err != nil {
		klog.Errorf("Failed to base64 decode, err: %v", err)
		return "", err
	}

	return string(decodedBytes), nil
}

// GetManagedClusterNamespace return ACM secret namespace accoding to its secret name
func GetManagedClusterNamespace(secretName string) string {
	if secretName == "" {
		return ""
	}

	if strings.HasSuffix(secretName, "-cluster-secret") {
		return strings.TrimSuffix(secretName, "-cluster-secret")
	}

	klog.Errorf("invalid managed cluster secret name, secretName: %v", secretName)

	return ""
}

func GetClientConfigFromKubeConfig(kubeconfigFile string) (*rest.Config, error) {
	if len(kubeconfigFile) > 0 {
		return getClientConfig(kubeconfigFile)
	}

	return nil, errors.New("no kubeconfig file found")
}

func getClientConfig(kubeConfigFile string) (*rest.Config, error) {
	kubeConfigBytes, err := ioutil.ReadFile(filepath.Clean(kubeConfigFile))
	if err != nil {
		return nil, err
	}

	kubeConfig, err := clientcmd.NewClientConfigFromBytes(kubeConfigBytes)
	if err != nil {
		return nil, err
	}

	clientConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, err
	}

	return clientConfig, nil
}
