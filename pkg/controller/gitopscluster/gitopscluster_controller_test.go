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

//go:build integration

package gitopscluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	c client.Client

	// Test1 resources
	test1Ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test1",
		},
	}

	test1Pl = &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-1",
			Namespace: test1Ns.Name,
		},
		Spec: clusterv1beta1.PlacementSpec{},
	}

	test1PlDc = &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-decision-1",
			Namespace: test1Ns.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "test-placement-1",
			},
		},
	}

	placementDecisionStatus = &clusterv1beta1.PlacementDecisionStatus{
		Decisions: []clusterv1beta1.ClusterDecision{
			*clusterDecision1,
		},
	}

	clusterDecision1 = &clusterv1beta1.ClusterDecision{
		ClusterName: "cluster1",
		Reason:      "OK",
	}

	// Test2 resources
	test2Ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test2",
		},
	}

	test2Pl = &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-2",
			Namespace: test2Ns.Name,
		},
		Spec: clusterv1beta1.PlacementSpec{},
	}

	test2PlDc = &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-decision-2",
			Namespace: test2Ns.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "test-placement-2",
			},
		},
	}

	// Test3 resources
	test3Ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test3",
		},
	}

	test3Pl = &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-3",
			Namespace: test3Ns.Name,
		},
		Spec: clusterv1beta1.PlacementSpec{},
	}

	test3PlDc = &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-decision-3",
			Namespace: test3Ns.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "test-placement-3",
			},
		},
	}

	// Test4 resources
	test4Ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test4",
		},
	}

	test4Pl = &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-4",
			Namespace: test4Ns.Name,
		},
		Spec: clusterv1beta1.PlacementSpec{},
	}

	test4PlDc = &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-decision-4",
			Namespace: test4Ns.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "test-placement-4",
			},
		},
	}

	// Test5 resources
	test5Ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test5",
		},
	}

	test5Pl = &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-5",
			Namespace: test5Ns.Name,
		},
		Spec: clusterv1beta1.PlacementSpec{},
	}

	test5PlDc = &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-decision-5",
			Namespace: test5Ns.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "test-placement-5",
			},
		},
	}

	// Test6 resources
	test6GitopsServerNamespace6 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-gitops6",
		},
	}

	test6ArgoService = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argo-server6",
			Namespace: test6GitopsServerNamespace6.Name,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":   "argocd",
				"app.kubernetes.io/component": "server",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:       "10.0.0.16",
			SessionAffinity: corev1.ServiceAffinityNone,
			Type:            corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       int32(443),
					TargetPort: intstr.FromInt(443),
				},
			},
		},
	}

	test6GitopsPlacement = &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-ops-placement-6",
			Namespace: test6GitopsServerNamespace6.Name,
		},
		Spec: clusterv1beta1.PlacementSpec{},
	}

	test6GitopsCluster = &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-ops-cluster-6",
			Namespace: test6GitopsServerNamespace6.Name,
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				Cluster:       "local-cluster",
				ArgoNamespace: test6GitopsServerNamespace6.Name,
			},
			PlacementRef: &corev1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Namespace:  test6GitopsServerNamespace6.Name,
				Name:       "git-ops-placement-6",
			},
		},
	}

	test6ManagedClusterNamespace6 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster6",
		},
	}

	test6ManagedCluster6 = &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster6",
			Labels: map[string]string{
				"test-label": "test-value",
			},
		},
		Spec: clusterv1.ManagedClusterSpec{
			HubAcceptsClient: true,
		},
	}

	test6GitopsPlacementDecision = &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-ops-placement-decision-6",
			Namespace: test6GitopsServerNamespace6.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "git-ops-placement-6",
			},
		},
		Status: clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{
				{
					ClusterName: test6ManagedCluster6.Name,
				},
			},
		},
	}

	// Test7 resources
	test7GitopsServerNamespace7 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-gitops7",
		},
	}

	test7ArgoService = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argo-server7",
			Namespace: test7GitopsServerNamespace7.Name,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":   "argocd",
				"app.kubernetes.io/component": "server",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:       "10.0.0.17",
			SessionAffinity: corev1.ServiceAffinityNone,
			Type:            corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       int32(443),
					TargetPort: intstr.FromInt(443),
				},
			},
		},
	}

	test7GitopsPlacement = &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-ops-placement-7",
			Namespace: test7GitopsServerNamespace7.Name,
		},
		Spec: clusterv1beta1.PlacementSpec{},
	}

	test7GitopsCluster = &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-ops-cluster-7",
			Namespace: test7GitopsServerNamespace7.Name,
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				Cluster:       "local-cluster",
				ArgoNamespace: test7GitopsServerNamespace7.Name,
			},
			PlacementRef: &corev1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Namespace:  test7GitopsServerNamespace7.Name,
				Name:       "git-ops-placement-7",
			},
		},
	}

	test7ManagedClusterNamespace7 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster7",
		},
	}

	test7ManagedCluster7 = &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster7",
			Labels: map[string]string{
				"test-label": "test-value",
			},
		},
		Spec: clusterv1.ManagedClusterSpec{
			HubAcceptsClient: true,
			ManagedClusterClientConfigs: []clusterv1.ClientConfig{
				{
					URL:      "https://local-cluster:6443",
					CABundle: []byte("abc"),
				},
			},
		},
	}

	test7GitopsPlacementDecision = &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-ops-placement-decision-7",
			Namespace: test7GitopsServerNamespace7.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "git-ops-placement-7",
			},
		},
		Status: clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{
				{
					ClusterName: test7ManagedCluster7.Name,
				},
			},
		},
	}

	test7ManagedClusterSecret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster7-cluster-secret",
			Namespace: test7ManagedCluster7.Name,
			Labels: map[string]string{
				"apps.open-cluster-management.io/secret-type": "acm-cluster",
			},
		},
		StringData: map[string]string{
			"name":   test7ManagedCluster7.Name,
			"server": "https://api.cluster7.com:6443",
			"config": "{\"bearerToken\": \"fakeToken1\", \"tlsClientConfig\": {\"insecure\": true}}",
		},
	}

	test7ManagedServiceAccount = &authv1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "application-manager",
			Namespace: test7ManagedCluster7.Name,
		},
		Spec: authv1beta1.ManagedServiceAccountSpec{
			Rotation: authv1beta1.ManagedServiceAccountRotation{
				Enabled: true,
				Validity: metav1.Duration{
					Duration: time.Hour * 168,
				},
			},
		},
	}

	test7MSASecret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "application-manager",
			Namespace: test7ManagedCluster7.Name,
		},
		StringData: map[string]string{
			"token":  "token1",
			"ca.crt": "caCrt1",
		},
	}

	test7GitopsNSSecret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster7-application-manager-cluster-secret",
			Namespace: test7ManagedCluster7.Name,
			Labels: map[string]string{
				"apps.open-cluster-management.io/secret-type": "acm-cluster",
			},
		},
		StringData: map[string]string{
			"name":   test7ManagedCluster7.Name,
			"server": "https://api.cluster7.com:6443",
			"config": "{\"bearerToken\": \"fakeToken1\", \"tlsClientConfig\": {\"insecure\": true}}",
		},
	}

	// Namespace where GitOpsCluster1 CR is
	testNamespace1 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test1",
		},
	}

	managedClusterNamespace1 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster1",
		},
	}

	managedClusterNamespace3 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster3",
		},
	}

	managedClusterNamespace10 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster10",
		},
	}

	managedClusterNamespace20 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster20",
		},
	}

	managedClusterNamespace30 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster30",
		},
	}

	managedClusterNamespace40 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster40",
		},
	}

	argocdServerNamespace1 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "argocd1",
		},
	}

	argocdServerNamespace2 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "argocd2",
		},
	}

	argocdServerNamespace3 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "argocd3",
		},
	}

	gitopsServerNamespace1 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-gitops1",
		},
	}

	managedCluster1 = &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster1",
			Labels: map[string]string{
				"test-label": "test-value",
			},
		},
		Spec: clusterv1.ManagedClusterSpec{
			HubAcceptsClient: true,
		},
	}

	managedClusterSecret1 = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1-cluster-secret",
			Namespace: "cluster1",
			Labels: map[string]string{
				"apps.open-cluster-management.io/secret-type": "acm-cluster",
			},
		},
		StringData: map[string]string{
			"name":   "cluster1",
			"server": "https://api.cluster1.com:6443",
			"config": "test-bearer-token-1",
		},
	}

	gitopsServerNamespace5 = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "openshift-gitops5",
		},
	}

	managedClusterSecret5 = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1-cluster-secret",
			Namespace: "cluster1",
			Labels: map[string]string{
				"apps.open-cluster-management.io/secret-type": "acm-cluster",
			},
		},
		StringData: map[string]string{
			"name":   "cluster1",
			"server": "",
			"config": "{\"bearerToken\": \"fakeToken1\", \"tlsClientConfig\": {\"insecure\": true}}",
		},
	}

	gitOpsClusterSecret5Key = types.NamespacedName{
		Name:      "cluster1-cluster-secret",
		Namespace: gitopsServerNamespace5.Name,
	}

	managedClusterSecret10 = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster10-cluster-secret",
			Namespace: "cluster10",
			Labels: map[string]string{
				"apps.open-cluster-management.io/secret-type": "acm-cluster",
				"dummy-label": "true",
			},
			Annotations: map[string]string{
				"dummy-annotation": "true",
			},
		},
		StringData: map[string]string{
			"name":   "cluster10",
			"server": "https://api.cluster10.com:6443",
			"config": "test-bearer-token-10",
		},
	}

	gitOpsClusterSecret1Key = types.NamespacedName{
		Name:      "cluster1-cluster-secret",
		Namespace: "argocd1",
	}

	gitOpsClusterSecret2Key = types.NamespacedName{
		Name:      "cluster1-cluster-secret",
		Namespace: "argocd2",
	}

	applicationSetConfigMapNew = types.NamespacedName{
		Name:      configMapNameNew,
		Namespace: "argocd1",
	}

	applicationSetConfigMapOld = types.NamespacedName{
		Name:      configMapNameOld,
		Namespace: "argocd1",
	}

	applicationsetRole = types.NamespacedName{
		Name:      "argocd1" + RoleSuffix,
		Namespace: "argocd1",
	}

	gitOpsClusterSecret2 = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1-cluster-secret",
			Namespace: "argocd2",
			Labels: map[string]string{
				"apps.open-cluster-management.io/acm-cluster": "true",
				"argocd.argoproj.io/secret-type":              "cluster",
			},
		},
		StringData: map[string]string{
			"name":   "cluster1",
			"server": "https://api.cluster1.com:6443",
			"config": "test-bearer-token-1",
		},
	}

	gitOpsCluster = &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-ops-cluster-1",
			Namespace: testNamespace1.Name,
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				Cluster:       "local-cluster",
				ArgoNamespace: "argocd1",
			},
			PlacementRef: &corev1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Namespace:  test1Ns.Name,
				Name:       test1Pl.Name,
			},
		},
	}

	gitOpsCluster2 = &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "git-ops-cluster-2",
			Namespace: argocdServerNamespace3.Name,
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				Cluster:       "local-cluster",
				ArgoNamespace: "argocd3",
			},
			PlacementRef: &corev1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Namespace:  argocdServerNamespace3.Name,
				Name:       test1Pl.Name,
			},
		},
	}

	gitOpsClusterSecret3Key = types.NamespacedName{
		Name:      "cluster1-cluster-secret",
		Namespace: gitopsServerNamespace1.Name,
	}

	argoService = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argo-server",
			Namespace: "argocd1",
			Labels: map[string]string{
				"app.kubernetes.io/part-of":   "argocd",
				"app.kubernetes.io/component": "server",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:       "10.0.0.10",
			SessionAffinity: corev1.ServiceAffinityNone,
			Type:            corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       int32(443),
					TargetPort: intstr.FromInt(443),
				},
			},
		},
	}

	argoService3 = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argo-server",
			Namespace: "argocd3",
			Labels: map[string]string{
				"app.kubernetes.io/part-of":   "argocd",
				"app.kubernetes.io/component": "server",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:       "10.0.0.11",
			SessionAffinity: corev1.ServiceAffinityNone,
			Type:            corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       int32(443),
					TargetPort: intstr.FromInt(443),
				},
			},
		},
	}
)

func TestReconcileCreateSecretInArgo(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})

	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	reconciler, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	recFn := SetupTestReconcile(reconciler)
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	// Set up test environment
	c.Create(context.TODO(), test1Ns)

	// Create placement
	g.Expect(c.Create(context.TODO(), test1Pl.DeepCopy())).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), test1Pl)

	// Create placement decision
	g.Expect(c.Create(context.TODO(), test1PlDc.DeepCopy())).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), test1PlDc)

	time.Sleep(time.Second * 5)

	// Update placement decision status
	placementDecision1 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: test1PlDc.Namespace, Name: test1PlDc.Name},
		placementDecision1)).NotTo(gomega.HaveOccurred())

	newPlacementDecision1 := placementDecision1.DeepCopy()
	newPlacementDecision1.Status = *placementDecisionStatus

	g.Expect(c.Status().Update(context.TODO(), newPlacementDecision1)).NotTo(gomega.HaveOccurred())

	time.Sleep(time.Second * 5)

	placementDecisionAfterupdate := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: placementDecision1.Namespace, Name: placementDecision1.Name},
		placementDecisionAfterupdate)).NotTo(gomega.HaveOccurred())

	g.Expect(placementDecisionAfterupdate.Status.Decisions[0].ClusterName).To(gomega.Equal("cluster1"))

	managedCluster1Copy := managedCluster1.DeepCopy()
	g.Expect(c.Create(context.TODO(), managedCluster1Copy)).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), managedCluster1Copy)

	// Managed cluster namespace
	c.Create(context.TODO(), managedClusterNamespace1)
	g.Expect(c.Create(context.TODO(), managedClusterSecret1.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), managedClusterSecret1)

	// Create Argo namespace and fake argo server pod
	c.Create(context.TODO(), argocdServerNamespace1)
	g.Expect(c.Create(context.TODO(), argoService.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), argoService)

	// Create GitOpsCluster CR
	goc := gitOpsCluster.DeepCopy()
	goc.Namespace = test1Ns.Name
	goc.Spec.PlacementRef = &corev1.ObjectReference{
		Kind:       "Placement",
		APIVersion: "cluster.open-cluster-management.io/v1beta1",
		Name:       test1Pl.Name,
	}
	g.Expect(c.Create(context.TODO(), goc)).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), goc)

	// Test that the managed cluster's secret is created in the Argo namespace
	secret := expectedSecretCreated(c, gitOpsClusterSecret1Key)
	g.Expect(secret).ToNot(gomega.BeNil())
	g.Expect(secret.Labels).To(gomega.HaveKeyWithValue("test-label", "test-value"))

	// Test that the ConfigMaps for ApplicationSets were created
	g.Expect(expectedConfigMapCreated(c, applicationSetConfigMapNew)).To(gomega.BeTrue())
	g.Expect(expectedConfigMapCreated(c, applicationSetConfigMapOld)).To(gomega.BeTrue())
	g.Expect(expectedRbacCreated(c, applicationsetRole)).To(gomega.BeTrue())

	// Test that updates to the managed cluster's labels are propagated
	managedCluster1Copy.Labels["test-label-2"] = "test-value-2"
	g.Expect(c.Update(context.TODO(), managedCluster1Copy)).NotTo(gomega.HaveOccurred())
	g.Eventually(func(g2 gomega.Gomega) {
		updatedSecret := expectedSecretCreated(c, gitOpsClusterSecret1Key)
		g2.Expect(updatedSecret).ToNot(gomega.BeNil())
		g2.Expect(updatedSecret.Labels).To(gomega.HaveKeyWithValue("test-label", "test-value"))
		g2.Expect(updatedSecret.Labels).To(gomega.HaveKeyWithValue("test-label-2", "test-value-2"))
	}, 60*time.Second, 1*time.Second).Should(gomega.Succeed())

	// Testcase #2 Create ArgoCD cluster secret using ManagedServiceAccount
	mcNS := managedClusterNamespace3

	msa := &authv1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "application-manager",
			Namespace: mcNS.Name,
		},
		Spec: authv1beta1.ManagedServiceAccountSpec{
			Rotation: authv1beta1.ManagedServiceAccountRotation{
				Enabled: true,
				Validity: metav1.Duration{
					Duration: time.Hour * 168,
				},
			},
		},
		Status: authv1beta1.ManagedServiceAccountStatus{
			TokenSecretRef: &authv1beta1.SecretRef{
				Name:                 "application-manager",
				LastRefreshTimestamp: metav1.NewTime(time.Time{}),
			},
		},
	}

	msaSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "application-manager",
			Namespace: mcNS.Name,
		},
		StringData: map[string]string{
			"token":  "token1",
			"ca.crt": "caCrt1",
		},
	}

	// Create namespaces
	c.Create(context.TODO(), argocdServerNamespace3)
	c.Create(context.TODO(), mcNS)

	// Create placement
	pm := test1Pl.DeepCopy()
	pm.Namespace = argocdServerNamespace3.Name
	pmDC := test1PlDc.DeepCopy()
	pmDC.Namespace = argocdServerNamespace3.Name

	c.Create(context.TODO(), pm)
	c.Create(context.TODO(), pmDC)
	c.Create(context.TODO(), argoService3)

	// Update PlacementDecision status
	c.Get(context.TODO(), client.ObjectKeyFromObject(pmDC), pmDC)
	cd1 := &clusterv1beta1.ClusterDecision{
		ClusterName: mcNS.Name,
		Reason:      "OK",
	}
	pmDC.Status = clusterv1beta1.PlacementDecisionStatus{
		Decisions: []clusterv1beta1.ClusterDecision{
			*cd1,
		},
	}
	c.Status().Update(context.TODO(), pmDC)

	// Create managed cluster
	mc1 := managedCluster1.DeepCopy()
	mc1.Name = mcNS.Name
	mc1.Spec.ManagedClusterClientConfigs = []clusterv1.ClientConfig{{URL: "https://local-cluster:6443", CABundle: []byte("abc")}}
	c.Create(context.TODO(), mc1)
	c.Create(context.TODO(), msa)
	c.Get(context.TODO(), client.ObjectKeyFromObject(msa), msa)

	// Update ManagedServiceAccount status
	tokenRef := authv1beta1.SecretRef{Name: msaSecret.Name, LastRefreshTimestamp: metav1.Now()}
	etime := metav1.NewTime(time.Now().Add(168 * time.Hour))
	msa.Status = authv1beta1.ManagedServiceAccountStatus{
		TokenSecretRef:      &tokenRef,
		ExpirationTimestamp: &etime,
	}
	c.Status().Update(context.TODO(), msa)

	// Create GitopsCluster
	gc := gitOpsCluster2.DeepCopy()
	c.Create(context.TODO(), gc)

	c.Create(context.TODO(), msaSecret)

	gitOpsMsaClusterSecretKey := types.NamespacedName{
		Name:      fmt.Sprintf("%v-%v-cluster-secret", mc1.Name, msa.Name),
		Namespace: argocdServerNamespace3.Name,
	}
	clusterSecret := &corev1.Secret{}

	// Wait for controller to create the ArgoCD cluster secret
	g.Eventually(func(g2 gomega.Gomega) {
		err = c.Get(context.TODO(), gitOpsMsaClusterSecretKey, clusterSecret)

		g2.Expect(err).To(gomega.BeNil())
	}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())

	c.Create(context.TODO(), managedClusterNamespace20)
	c.Create(context.TODO(), managedClusterNamespace30)
	c.Create(context.TODO(), managedClusterNamespace40)

	mc20 := managedCluster1.DeepCopy()
	mc20.Name = managedClusterNamespace20.Name
	mc20.Spec.ManagedClusterClientConfigs = []clusterv1.ClientConfig{{URL: "https://local-cluster:6443", CABundle: []byte("abc")}}
	c.Create(context.TODO(), mc20)

	mc30 := managedCluster1.DeepCopy()
	mc30.Name = managedClusterNamespace30.Name
	mc30.Spec.ManagedClusterClientConfigs = []clusterv1.ClientConfig{{URL: "https://local-cluster:6443", CABundle: []byte("abc")}}
	c.Create(context.TODO(), mc30)

	mc40 := managedCluster1.DeepCopy()
	mc40.Name = managedClusterNamespace40.Name
	mc40.Spec.ManagedClusterClientConfigs = []clusterv1.ClientConfig{{URL: "https://local-cluster:6443", CABundle: []byte("abc")}}
	c.Create(context.TODO(), mc40)

	c.Get(context.TODO(), client.ObjectKeyFromObject(pmDC), pmDC)
	cd20 := &clusterv1beta1.ClusterDecision{
		ClusterName: managedClusterNamespace20.Name,
		Reason:      "OK",
	}
	cd30 := &clusterv1beta1.ClusterDecision{
		ClusterName: managedClusterNamespace30.Name,
		Reason:      "OK",
	}
	cd40 := &clusterv1beta1.ClusterDecision{
		ClusterName: managedClusterNamespace40.Name,
		Reason:      "OK",
	}
	pmDC.Status = clusterv1beta1.PlacementDecisionStatus{
		Decisions: []clusterv1beta1.ClusterDecision{
			*cd20, *cd30, *cd40,
		},
	}
	c.Status().Update(context.TODO(), pmDC)
	gc3 := gitOpsCluster2.DeepCopy()
	c.Create(context.TODO(), gc3)

	g.Eventually(func(g2 gomega.Gomega) {
		updated := &gitopsclusterV1beta1.GitOpsCluster{}
		err := c.Get(context.TODO(), client.ObjectKeyFromObject(gc3), updated)
		g2.Expect(err).ToNot(gomega.HaveOccurred())

		// Test legacy fields (backward compatibility)
		g2.Expect(updated.Status.Phase).To(gomega.Equal("failed"))
		g2.Expect(updated.Status.Message).To(gomega.ContainSubstring("cluster20"))
		g2.Expect(updated.Status.Message).To(gomega.ContainSubstring("cluster30"))
		g2.Expect(updated.Status.Message).To(gomega.ContainSubstring("cluster40"))
		g2.Expect(updated.Status.Message).To(gomega.ContainSubstring("Secret"))
		g2.Expect(updated.Status.Message).To(gomega.ContainSubstring("not found"))

		// Test new conditions (should also be set)
		g2.Expect(len(updated.Status.Conditions)).To(gomega.BeNumerically(">", 0))
		g2.Expect(updated.IsConditionFalse(gitopsclusterV1beta1.GitOpsClusterClustersRegistered)).To(gomega.BeTrue())

		// Overall Ready condition should be False when cluster registration fails
		readyCondition := updated.GetCondition(gitopsclusterV1beta1.GitOpsClusterReady)
		if readyCondition != nil {
			g2.Expect(readyCondition.Status).To(gomega.Equal(metav1.ConditionFalse))
		}
	}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())
}

func TestReconcileNoSecretInInvalidArgoNamespace(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	reconciler, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	recFn := SetupTestReconcile(reconciler)
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	// Set up test environment
	c.Create(context.TODO(), test2Ns)

	// Create placement
	g.Expect(c.Create(context.TODO(), test2Pl.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), test2Pl)

	// Create placement decision
	g.Expect(c.Create(context.TODO(), test2PlDc.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), test2PlDc)

	time.Sleep(time.Second * 3)

	// Update placement decision status
	placementDecision2 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: test2PlDc.Namespace, Name: test2PlDc.Name},
		placementDecision2)).NotTo(gomega.HaveOccurred())

	newPlacementDecision2 := placementDecision2.DeepCopy()
	newPlacementDecision2.Status = *placementDecisionStatus

	g.Expect(c.Status().Update(context.TODO(), newPlacementDecision2)).NotTo(gomega.HaveOccurred())

	time.Sleep(time.Second * 3)

	placementDecisionAfterupdate2 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: placementDecision2.Namespace, Name: placementDecision2.Name},
		placementDecisionAfterupdate2)).NotTo(gomega.HaveOccurred())

	g.Expect(placementDecisionAfterupdate2.Status.Decisions[0].ClusterName).To(gomega.Equal("cluster1"))

	// Create managed cluster namespaces
	c.Create(context.TODO(), managedClusterNamespace1)
	g.Expect(c.Create(context.TODO(), managedClusterSecret1.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), managedClusterSecret1)

	// Create invalid Argo namespaces where there is no argo server pod
	c.Create(context.TODO(), argocdServerNamespace2)

	// Create GitOpsCluster CR
	goc := gitOpsCluster.DeepCopy()
	goc.Namespace = test2Ns.Name
	goc.Spec.PlacementRef = &corev1.ObjectReference{
		Kind:       "Placement",
		APIVersion: "cluster.open-cluster-management.io/v1beta1",
		Name:       test2Pl.Name,
	}
	g.Expect(c.Create(context.TODO(), goc)).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), goc)

	// Test that the managed cluster's secret is not created in argocd2
	// namespace because there is no valid argocd server pod in argocd2 namespace
	g.Expect(expectedSecretCreated(c, gitOpsClusterSecret2Key)).To(gomega.BeNil())
}

func TestReconcileCreateSecretInOpenshiftGitops(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})

	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	reconciler, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	recFn := SetupTestReconcile(reconciler)
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	// Set up test environment
	c.Create(context.TODO(), test3Ns)

	// Create placement
	g.Expect(c.Create(context.TODO(), test3Pl.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), test3Pl)

	// Create placement decision
	g.Expect(c.Create(context.TODO(), test3PlDc.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), test3PlDc)

	time.Sleep(time.Second * 3)

	// Update placement decision status
	placementDecision3 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: test3PlDc.Namespace, Name: test3PlDc.Name},
		placementDecision3)).NotTo(gomega.HaveOccurred())

	newPlacementDecision3 := placementDecision3.DeepCopy()
	newPlacementDecision3.Status = *placementDecisionStatus

	g.Expect(c.Status().Update(context.TODO(), newPlacementDecision3)).NotTo(gomega.HaveOccurred())

	time.Sleep(time.Second * 3)

	placementDecisionAfterupdate3 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: placementDecision3.Namespace, Name: placementDecision3.Name},
		placementDecisionAfterupdate3)).NotTo(gomega.HaveOccurred())

	g.Expect(placementDecisionAfterupdate3.Status.Decisions[0].ClusterName).To(gomega.Equal("cluster1"))

	// Managed cluster namespace
	c.Create(context.TODO(), managedClusterNamespace1)
	g.Expect(c.Create(context.TODO(), managedClusterSecret1.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), managedClusterSecret1)

	mc1 := managedCluster1.DeepCopy()
	mc1.Spec.ManagedClusterClientConfigs = []clusterv1.ClientConfig{{URL: "https://local-cluster:6443", CABundle: []byte("abc")}}

	g.Expect(c.Create(context.TODO(), mc1)).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), mc1)

	// Create Openshift-gitops namespace
	c.Create(context.TODO(), gitopsServerNamespace1)

	argoServiceInGitOps := argoService.DeepCopy()
	argoServiceInGitOps.Namespace = gitopsServerNamespace1.Name

	g.Expect(c.Create(context.TODO(), argoServiceInGitOps)).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), argoServiceInGitOps)

	// Create GitOpsCluster CR
	goc := gitOpsCluster.DeepCopy()
	goc.Namespace = test3Ns.Name
	goc.Spec.ArgoServer.ArgoNamespace = gitopsServerNamespace1.Name
	goc.Spec.PlacementRef = &corev1.ObjectReference{
		Kind:       "Placement",
		APIVersion: "cluster.open-cluster-management.io/v1beta1",
		Name:       test3Pl.Name,
	}

	g.Expect(c.Create(context.TODO(), goc)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), goc)

	// Test that the managed cluster's secret is created in the Argo namespace
	secret := expectedSecretCreated(c, gitOpsClusterSecret3Key)
	g.Expect(secret).ToNot(gomega.BeNil())
	g.Expect(secret.Labels).To(gomega.HaveKeyWithValue("test-label", "test-value"))

	// Test that updates to the managed cluster's labels are propagated
	mc1.Labels["test-label-2"] = "test-value-2"
	g.Expect(c.Update(context.TODO(), mc1)).NotTo(gomega.HaveOccurred())
	g.Eventually(func(g2 gomega.Gomega) {
		updatedSecret := expectedSecretCreated(c, gitOpsClusterSecret3Key)
		g2.Expect(updatedSecret).ToNot(gomega.BeNil())
		g2.Expect(updatedSecret.Labels).To(gomega.HaveKeyWithValue("test-label", "test-value"))
		g2.Expect(updatedSecret.Labels).To(gomega.HaveKeyWithValue("test-label-2", "test-value-2"))
	}, 60*time.Second, 1*time.Second).Should(gomega.Succeed())

	// Update GitOpsCluster CR with managedServiceAccountRef
	msaSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managedserviceaccountsecret",
			Namespace: managedClusterNamespace1.Name,
		},
		StringData: map[string]string{
			"token":  "token1",
			"ca.crt": "caCrt1",
		},
	}

	g.Expect(c.Create(context.TODO(), msaSecret)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), msaSecret)

	msa := &authv1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managedserviceaccount1",
			Namespace: managedClusterNamespace1.Name,
		},
		Spec: authv1beta1.ManagedServiceAccountSpec{Rotation: authv1beta1.ManagedServiceAccountRotation{Enabled: false}},
	}

	g.Expect(c.Create(context.TODO(), msa)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), msa)

	time.Sleep(1 * time.Second)

	g.Expect(c.Get(context.TODO(), client.ObjectKeyFromObject(msa), msa)).NotTo(gomega.HaveOccurred())

	tokenRef := authv1beta1.SecretRef{Name: msaSecret.Name, LastRefreshTimestamp: metav1.Now()}
	etime := metav1.NewTime(time.Now().Add(30 * time.Second))
	msa.Status = authv1beta1.ManagedServiceAccountStatus{
		TokenSecretRef:      &tokenRef,
		ExpirationTimestamp: &etime,
	}
	g.Expect(c.Status().Update(context.TODO(), msa)).NotTo(gomega.HaveOccurred())

	time.Sleep(1 * time.Second)

	g.Expect(c.Get(context.TODO(), client.ObjectKeyFromObject(goc), goc))
	goc.Spec.ManagedServiceAccountRef = msa.Name
	g.Expect(c.Update(context.TODO(), goc)).NotTo(gomega.HaveOccurred())

	time.Sleep(1 * time.Second)

	// Check the secret created from the managed service account
	gitOpsMsaClusterSecretKey := types.NamespacedName{
		Name:      fmt.Sprintf("%v-%v-cluster-secret", managedClusterNamespace1.Name, msa.Name),
		Namespace: gitopsServerNamespace1.Name,
	}
	g.Expect(expectedSecretCreated(c, gitOpsMsaClusterSecretKey)).ToNot(gomega.BeNil())

	// Update gitops cluster to use a non-existent managed service account,
	// expects to fail to create the new cluster secret but old cluster secret should be removed
	g.Expect(c.Get(context.TODO(), client.ObjectKeyFromObject(goc), goc))
	goc.Spec.ManagedServiceAccountRef = "dummy-msa"
	g.Expect(c.Update(context.TODO(), goc)).NotTo(gomega.HaveOccurred())

	time.Sleep(1 * time.Second)

	oldClusterSecret := &corev1.Secret{}

	g.Eventually(func(g2 gomega.Gomega) {
		err = c.Get(context.TODO(), gitOpsMsaClusterSecretKey, oldClusterSecret)

		g2.Expect(err).ToNot(gomega.BeNil())
	}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())

	// Test6 *-cluster-secret and *-application-manager-cluster-secret are not found

	// Create managed cluster namespace
	c.Create(context.TODO(), test6ManagedClusterNamespace6)

	// Create managed cluster
	c.Create(context.TODO(), test6ManagedCluster6)

	// Create Gitops namespace
	c.Create(context.TODO(), test6GitopsServerNamespace6)

	// Create Argo service
	c.Create(context.TODO(), test6ArgoService)

	// Create Gitops placement
	c.Create(context.TODO(), test6GitopsPlacement)

	// Creat Gitops placement decision
	c.Create(context.TODO(), test6GitopsPlacementDecision)

	g.Eventually(func(g2 gomega.Gomega) {
		tempPlacementDecisionObject := expectedPlacementDecisionCreated(c,
			types.NamespacedName{
				Name:      test6GitopsPlacementDecision.Name,
				Namespace: test6GitopsPlacementDecision.Namespace,
			},
		)

		tempPlacementDecisionObject.Status = clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{
				{
					ClusterName: test6ManagedCluster6.Name,
					Reason:      "",
				},
			},
		}

		g2.Expect(c.Status().Update(context.TODO(), tempPlacementDecisionObject)).Should(gomega.Succeed())
	}, 60*time.Second, 1*time.Second).Should(gomega.Succeed())

	// Create GitopsCluster
	c.Create(context.TODO(), test6GitopsCluster)

	// Validate cluster secret is not created in Gitops namespace
	test6ClusterSecret := expectedSecretCreated(c, types.NamespacedName{
		Name:      test6ManagedCluster6.Name + "-cluster-secret",
		Namespace: test6GitopsServerNamespace6.Name,
	})

	g.Expect(test6ClusterSecret).To(gomega.BeNil())

	// Test 7 Long lived secret clean up

	// Create managed cluster namespace
	c.Create(context.TODO(), test7ManagedClusterNamespace7)

	// Create managed cluster
	c.Create(context.TODO(), test7ManagedCluster7)

	// Create Gitops namespace
	c.Create(context.TODO(), test7GitopsServerNamespace7)

	// Create Argo service
	err = c.Create(context.TODO(), test7ArgoService)

	// Create Gitops placement
	c.Create(context.TODO(), test7GitopsPlacement)

	// Creat Gitops placement decision
	c.Create(context.TODO(), test7GitopsPlacementDecision)

	g.Eventually(func(g2 gomega.Gomega) {
		tempPlacementDecisionObject := expectedPlacementDecisionCreated(c,
			types.NamespacedName{
				Name:      test7GitopsPlacementDecision.Name,
				Namespace: test7GitopsPlacementDecision.Namespace,
			},
		)

		tempPlacementDecisionObject.Status = clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{
				{
					ClusterName: test7ManagedCluster7.Name,
					Reason:      "",
				},
			},
		}

		g2.Expect(c.Status().Update(context.TODO(), tempPlacementDecisionObject)).Should(gomega.Succeed())
	}, 60*time.Second, 1*time.Second).Should(gomega.Succeed())

	// Create MSA
	c.Create(context.TODO(), test7ManagedServiceAccount)

	g.Eventually(func(g2 gomega.Gomega) {
		updateManagedServiceAccount := expectedMSACreated(c, types.NamespacedName{
			Name:      "application-manager",
			Namespace: test7ManagedCluster7.Name,
		})
		test7etime := metav1.NewTime(time.Now().Add(1 * time.Hour))
		updateManagedServiceAccount.Status = authv1beta1.ManagedServiceAccountStatus{
			TokenSecretRef: &authv1beta1.SecretRef{
				Name:                 "application-manager",
				LastRefreshTimestamp: metav1.Now(),
			},
			ExpirationTimestamp: &test7etime,
		}

		g2.Expect(c.Status().Update(context.TODO(), updateManagedServiceAccount)).Should(gomega.Succeed())
	}, 60*time.Second, 1*time.Second).Should(gomega.Succeed())

	// Create managed cluster secret
	c.Create(context.TODO(), test7ManagedClusterSecret)

	// Create MSA secret
	c.Create(context.TODO(), test7MSASecret)

	// Create Gitops namespace cluster secret
	c.Create(context.TODO(), test7GitopsNSSecret)

	// Create GitopsCluster
	c.Create(context.TODO(), test7GitopsCluster)

	// Validate cluster secret is created in Gitops namespace
	test7ClusterSecret := &corev1.Secret{}

	g.Eventually(func(g2 gomega.Gomega) {
		err = c.Get(context.TODO(), types.NamespacedName{
			Name:      test7ManagedCluster7.Name + "-application-manager-cluster-secret",
			Namespace: test7GitopsServerNamespace7.Name,
		}, test7ClusterSecret)

		g2.Expect(err).ToNot(gomega.BeNil())
	}, 60*time.Second, 1*time.Second).Should(gomega.Succeed())
}

// test managed cluster secret creation for non OCP clusters
func TestReconcileNonOCPCreateSecretInOpenshiftGitops(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})

	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	reconciler, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	recFn := SetupTestReconcile(reconciler)
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	// Set up test environment
	c.Create(context.TODO(), test5Ns)

	// Create placement
	g.Expect(c.Create(context.TODO(), test5Pl.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), test5Pl)

	// Create placement decision
	g.Expect(c.Create(context.TODO(), test5PlDc.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), test5PlDc)

	time.Sleep(time.Second * 3)

	// Update placement decision status
	placementDecision5 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: test5PlDc.Namespace, Name: test5PlDc.Name},
		placementDecision5)).NotTo(gomega.HaveOccurred())

	newPlacementDecision5 := placementDecision5.DeepCopy()
	newPlacementDecision5.Status = *placementDecisionStatus

	g.Expect(c.Status().Update(context.TODO(), newPlacementDecision5)).NotTo(gomega.HaveOccurred())

	time.Sleep(time.Second * 3)

	placementDecisionAfterupdate5 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: placementDecision5.Namespace, Name: placementDecision5.Name},
		placementDecisionAfterupdate5)).NotTo(gomega.HaveOccurred())

	g.Expect(placementDecisionAfterupdate5.Status.Decisions[0].ClusterName).To(gomega.Equal("cluster1"))

	// Create Managed cluster secret with empty api server url
	c.Create(context.TODO(), managedClusterNamespace1)

	managedClusterSecret5 := managedClusterSecret5.DeepCopy()
	managedClusterSecret5.StringData["server"] = ""

	g.Expect(c.Create(context.TODO(), managedClusterSecret5.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), managedClusterSecret5)

	// Create Managed cluster with empty api server url data
	mc1 := managedCluster1.DeepCopy()

	g.Expect(c.Create(context.TODO(), mc1)).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), mc1)

	// Create Openshift-gitops5 namespace
	c.Create(context.TODO(), gitopsServerNamespace5)

	argoServiceInGitOps := argoService.DeepCopy()
	argoServiceInGitOps.Namespace = gitopsServerNamespace5.Name

	g.Expect(c.Create(context.TODO(), argoServiceInGitOps)).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), argoServiceInGitOps)

	// Create GitOpsCluster CR
	goc := gitOpsCluster.DeepCopy()
	goc.Namespace = test5Ns.Name
	goc.Spec.ArgoServer.ArgoNamespace = gitopsServerNamespace5.Name
	goc.Spec.PlacementRef = &corev1.ObjectReference{
		Kind:       "Placement",
		APIVersion: "cluster.open-cluster-management.io/v1beta1",
		Name:       test5Pl.Name,
	}

	g.Expect(c.Create(context.TODO(), goc)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), goc)

	// expect the gitopscluster CR fails to generate the cluster secret as both the managed cluster and the managed cluster secret don't have api server url
	time.Sleep(3 * time.Second)

	g.Expect(c.Get(context.TODO(), client.ObjectKeyFromObject(goc), goc)).NotTo(gomega.HaveOccurred())

	// Test legacy phase field (backward compatibility)
	g.Expect(goc.Status.Phase).To(gomega.Equal("failed"))

	// Test new conditions are also set correctly
	g.Expect(len(goc.Status.Conditions)).To(gomega.BeNumerically(">", 0))

	// Verify that ArgoCD server failure condition is set
	agentCondition := goc.GetCondition(gitopsclusterV1beta1.GitOpsClusterArgoCDAgentReady)
	if agentCondition != nil {
		g.Expect(agentCondition.Status).To(gomega.Equal(metav1.ConditionFalse))
		g.Expect(agentCondition.Reason).To(gomega.Equal(gitopsclusterV1beta1.ReasonArgoServerNotFound))
	}

	// append the api server url to the managed cluster, expect the managed cluster's secret is created in the Argo namespace
	mc1.Spec.ManagedClusterClientConfigs = []clusterv1.ClientConfig{{URL: "https://local-cluster-5:6443", CABundle: []byte("abc")}}
	g.Expect(c.Update(context.TODO(), mc1)).NotTo(gomega.HaveOccurred())

	time.Sleep(3 * time.Second)

	updatedSecret := expectedSecretCreated(c, gitOpsClusterSecret5Key)

	g.Expect(updatedSecret).ToNot(gomega.BeNil())
	g.Expect(string(updatedSecret.Data["server"])).To(gomega.Equal("https://local-cluster-5:6443"))
}

func expectedSecretCreated(c client.Client, expectedSecretKey types.NamespacedName) *corev1.Secret {
	timeout := 0

	for {
		secret := &corev1.Secret{}
		err := c.Get(context.TODO(), expectedSecretKey, secret)

		if err == nil {
			return secret
		}

		if timeout > 30 {
			return nil
		}

		time.Sleep(time.Second * 3)

		timeout += 3
	}
}

func expectedMSACreated(c client.Client, expectedMSAKey types.NamespacedName) *authv1beta1.ManagedServiceAccount {
	timeout := 0

	for {
		msa := &authv1beta1.ManagedServiceAccount{}
		err := c.Get(context.TODO(), expectedMSAKey, msa)

		if err == nil {
			return msa
		}

		if timeout > 30 {
			return nil
		}

		time.Sleep(time.Second * 3)

		timeout += 3
	}
}

func expectedPlacementDecisionCreated(c client.Client, expectedPlacementDecisionKey types.NamespacedName) *clusterv1beta1.PlacementDecision {
	timeout := 0

	for {
		pd := &clusterv1beta1.PlacementDecision{}
		err := c.Get(context.TODO(), expectedPlacementDecisionKey, pd)

		if err == nil {
			return pd
		}

		if timeout > 30 {
			return nil
		}

		time.Sleep(time.Second * 3)

		timeout += 3
	}
}

func expectedConfigMapCreated(c client.Client, expectedConfigMap types.NamespacedName) bool {
	timeout := 0

	for {
		configMap := &corev1.ConfigMap{}
		err := c.Get(context.TODO(), expectedConfigMap, configMap)

		if err == nil {
			return true
		}

		if timeout > 30 {
			return false
		}

		time.Sleep(time.Second * 3)

		timeout += 3
	}
}

func expectedRbacCreated(c client.Client, expectedDetails types.NamespacedName) bool {
	timeout := 0

	for {
		role := &rbacv1.Role{}
		err := c.Get(context.TODO(), expectedDetails, role)
		fmt.Printf("role: %v", role)

		if err == nil {
			roleBinding := &rbacv1.RoleBinding{}
			err = c.Get(context.TODO(), expectedDetails, roleBinding)

			if err == nil {
				return true
			}
		}

		if timeout > 30 {
			return false
		}

		time.Sleep(time.Second * 3)

		timeout += 3
	}
}

func TestReconcileDeleteOrphanSecret(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})

	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	reconciler, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	recFn := SetupTestReconcile(reconciler)
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	// Set up test environment
	c.Create(context.TODO(), test4Ns)

	// Create placement
	g.Expect(c.Create(context.TODO(), test4Pl.DeepCopy())).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), test4Pl)

	// Create placement decision
	g.Expect(c.Create(context.TODO(), test4PlDc.DeepCopy())).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), test4PlDc)

	time.Sleep(time.Second * 3)

	// Update placement decision status
	placementDecision4 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: test4PlDc.Namespace, Name: test4PlDc.Name},
		placementDecision4)).NotTo(gomega.HaveOccurred())

	newPlacementDecision4 := placementDecision4.DeepCopy()
	newPlacementDecision4.Status = *placementDecisionStatus

	g.Expect(c.Status().Update(context.TODO(), newPlacementDecision4)).NotTo(gomega.HaveOccurred())

	time.Sleep(time.Second * 3)

	placementDecisionAfterupdate4 := &clusterv1beta1.PlacementDecision{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: placementDecision4.Namespace,
			Name: placementDecision4.Name}, placementDecisionAfterupdate4)).NotTo(gomega.HaveOccurred())

	g.Expect(placementDecisionAfterupdate4.Status.Decisions[0].ClusterName).To(gomega.Equal("cluster1"))

	// Managed cluster namespace
	c.Create(context.TODO(), managedClusterNamespace1)
	g.Expect(c.Create(context.TODO(), managedClusterSecret1.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), managedClusterSecret1)

	c.Create(context.TODO(), managedClusterNamespace10)
	g.Expect(c.Create(context.TODO(), managedClusterSecret10.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), managedClusterSecret10)

	// Create Argo namespace
	c.Create(context.TODO(), argocdServerNamespace1)
	g.Expect(c.Create(context.TODO(), argoService.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), argoService)

	// Create invalid Argo namespaces where there is no argo server pod
	// And create a cluster secret to simulate an orphan cluster secret
	c.Create(context.TODO(), argocdServerNamespace2)
	g.Expect(c.Create(context.TODO(), gitOpsClusterSecret2.DeepCopy())).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), gitOpsClusterSecret2)

	// Create GitOpsCluster CR
	goc := gitOpsCluster.DeepCopy()
	goc.Namespace = test4Ns.Name
	goc.Spec.PlacementRef = &corev1.ObjectReference{
		Kind:       "Placement",
		APIVersion: "cluster.open-cluster-management.io/v1beta1",
		Name:       test4Pl.Name,
	}
	g.Expect(c.Create(context.TODO(), goc)).NotTo(gomega.HaveOccurred())

	defer c.Delete(context.TODO(), goc)

	// Test that the orphan managed cluster's secret is deleted from the Argo namespace
	g.Expect(checkOrphanSecretDeleted(c, gitOpsClusterSecret2Key)).To(gomega.BeTrue())

	mySecret := &corev1.Secret{}
	c.Get(context.TODO(), types.NamespacedName{Name: "cluster10-cluster-secret", Namespace: "cluster10"}, mySecret)
	g.Expect(mySecret.Annotations).To(gomega.Equal(map[string]string{
		"dummy-annotation": "true",
	}))
	g.Expect(mySecret.Labels).To(gomega.Equal(map[string]string{
		"apps.open-cluster-management.io/secret-type": "acm-cluster",
		"dummy-label": "true",
	}))
	g.Expect(mySecret.Data).To(gomega.Equal(map[string][]byte{
		"name":   []byte("cluster10"),
		"server": []byte("https://api.cluster10.com:6443"),
		"config": []byte("test-bearer-token-10"),
	}))
}

func checkOrphanSecretDeleted(c client.Client, expectedSecretKey types.NamespacedName) bool {
	timeout := 0

	for {
		secret := &corev1.Secret{}
		err := c.Get(context.TODO(), expectedSecretKey, secret)

		if err != nil {
			return true
		}

		if timeout > 30 {
			return false
		}

		time.Sleep(time.Second * 3)

		timeout += 3
	}
}

func TestUnionSecretData(t *testing.T) {
	type args struct {
		newSecret      *corev1.Secret
		existingSecret *corev1.Secret
	}

	tests := []struct {
		name string
		args args
		want *corev1.Secret
	}{
		{
			name: "empty secrets",
			args: args{newSecret: &corev1.Secret{}, existingSecret: &corev1.Secret{}},
			want: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{},
				Annotations: map[string]string{},
			},
				StringData: map[string]string{}},
		},
		{
			name: "no changes in secret",
			args: args{
				newSecret: &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster1-cluster-secret",
						Namespace: "cluster1",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type":              "cluster",
							"apps.open-cluster-management.io/acm-cluster": "true"},
					},
					StringData: map[string]string{
						"name":   "cluster1",
						"server": "https://api.cluster1.com:6443",
						"config": "test-bearer-token-1",
					},
				},
				existingSecret: &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster1-cluster-secret",
						Namespace: "cluster1",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type":              "cluster",
							"apps.open-cluster-management.io/acm-cluster": "true",
						},
					},
					Data: map[string][]byte{
						"name":   []byte("cluster1"),
						"server": []byte("https://api.cluster1.com:6443"),
						"config": []byte("test-bearer-token-1"),
					},
				},
			},
			want: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1-cluster-secret",
					Namespace: "cluster1",
					Labels: map[string]string{
						"argocd.argoproj.io/secret-type":              "cluster",
						"apps.open-cluster-management.io/acm-cluster": "true"},
					Annotations: map[string]string{},
				},
				StringData: map[string]string{
					"name":   "cluster1",
					"server": "https://api.cluster1.com:6443",
					"config": "test-bearer-token-1",
				},
			},
		},
		{
			name: "union labels, annotations, and data",
			args: args{
				newSecret: &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster1-cluster-secret",
						Namespace: "cluster1",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type":              "cluster",
							"apps.open-cluster-management.io/acm-cluster": "true",
						},
					},
					StringData: map[string]string{
						"name":   "cluster1",
						"server": "https://api.cluster1.com:6443",
						"config": "test-bearer-token-1",
					},
				},
				existingSecret: &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster1-cluster-secret",
						Namespace: "cluster1",
						Labels: map[string]string{
							"argocd.argoproj.io/secret-type": "cluster",
							"test-label-copy-over":           "true",
						},
						Annotations: map[string]string{
							"test-annotation-copy":                             "true",
							"kubectl.kubernetes.io/last-applied-configuration": "{\"apiVersion\":\"cluster.open-cluster-management.io/v1beta1\"",
						},
					},
					Data: map[string][]byte{
						"name":       []byte("cluster1"),
						"server":     []byte("https://api.cluster1.com:6443"),
						"config":     []byte("test-bearer-token-1"),
						"dummy-data": []byte("test-dummy-data"),
					},
				},
			},
			want: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1-cluster-secret",
					Namespace: "cluster1",
					Labels: map[string]string{
						"argocd.argoproj.io/secret-type":              "cluster",
						"apps.open-cluster-management.io/acm-cluster": "true",
						"test-label-copy-over":                        "true",
					},
					Annotations: map[string]string{
						"test-annotation-copy": "true",
					},
				},
				StringData: map[string]string{
					"name":       "cluster1",
					"server":     "https://api.cluster1.com:6443",
					"config":     "test-bearer-token-1",
					"dummy-data": "test-dummy-data",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unionSecretData(tt.args.newSecret, tt.args.existingSecret); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unionSecretData() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateMangedClusterSecretFromManagedServiceAccount(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})

	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()
	gitopsc, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	msa := &authv1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managedserviceaccount1",
			Namespace: managedClusterNamespace1.Name,
		},
		Spec: authv1beta1.ManagedServiceAccountSpec{Rotation: authv1beta1.ManagedServiceAccountRotation{Enabled: false}},
	}

	msaSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managedserviceaccountsecret",
			Namespace: managedClusterNamespace1.Name,
		},
		StringData: map[string]string{
			"token":  "token1",
			"ca.crt": "caCrt1",
		},
	}

	msaSecret2 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managedserviceaccountsecret2",
			Namespace: managedClusterNamespace1.Name,
		},
		StringData: map[string]string{
			"token":  "token2",
			"ca.crt": "caCrt1",
		},
	}

	// Create namespaces
	c.Create(context.TODO(), argocdServerNamespace1)
	c.Create(context.TODO(), managedClusterNamespace1)

	// Create managed cluster
	mc1 := managedCluster1.DeepCopy()
	mc1.Spec.ManagedClusterClientConfigs = []clusterv1.ClientConfig{{URL: "https://local-cluster:6443", CABundle: []byte("abc")}}

	g.Expect(c.Create(context.TODO(), mc1)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), mc1)

	time.Sleep(1 * time.Second)

	// No managed service account
	_, err = gitopsc.(*ReconcileGitOpsCluster).CreateMangedClusterSecretFromManagedServiceAccount(
		argocdServerNamespace1.Name, managedCluster1, msa.Name, true)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).Should(gomega.MatchRegexp("ManagedServiceAccount.authentication.open-cluster-management.io.*not found"))

	g.Expect(c.Create(context.TODO(), msaSecret)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), msaSecret)

	g.Expect(c.Create(context.TODO(), msa)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), msa)

	time.Sleep(1 * time.Second)

	// No tokenSecretRef
	_, err = gitopsc.(*ReconcileGitOpsCluster).CreateMangedClusterSecretFromManagedServiceAccount(
		argocdServerNamespace1.Name, mc1, msa.Name, true)
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.Equal("no token reference secret found in the managed service account: cluster1/managedserviceaccount1"))

	// Has tokenSecretRef
	g.Expect(c.Get(context.TODO(), client.ObjectKeyFromObject(msa), msa)).NotTo(gomega.HaveOccurred())

	tokenRef := authv1beta1.SecretRef{Name: msaSecret.Name, LastRefreshTimestamp: metav1.Now()}
	etime := metav1.NewTime(time.Now().Add(30 * time.Second))
	msa.Status = authv1beta1.ManagedServiceAccountStatus{
		TokenSecretRef:      &tokenRef,
		ExpirationTimestamp: &etime,
	}
	g.Expect(c.Status().Update(context.TODO(), msa)).NotTo(gomega.HaveOccurred())

	time.Sleep(1 * time.Second)

	// Cluster secret created from the managed service account
	clusterSecret, err := gitopsc.(*ReconcileGitOpsCluster).CreateMangedClusterSecretFromManagedServiceAccount(
		argocdServerNamespace1.Name, mc1, msa.Name, true)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	data := make(map[string]interface{})
	g.Expect(json.Unmarshal([]byte(clusterSecret.StringData["config"]), &data)).NotTo(gomega.HaveOccurred())
	g.Expect(data["bearerToken"].(string)).To(gomega.Equal("token1"))

	// Update MSA to a different secret token
	g.Expect(c.Create(context.TODO(), msaSecret2)).NotTo(gomega.HaveOccurred())

	tokenRef = authv1beta1.SecretRef{Name: msaSecret2.Name, LastRefreshTimestamp: metav1.Now()}
	msa.Status = authv1beta1.ManagedServiceAccountStatus{
		TokenSecretRef:      &tokenRef,
		ExpirationTimestamp: &etime,
	}
	g.Expect(c.Status().Update(context.TODO(), msa)).NotTo(gomega.HaveOccurred())

	time.Sleep(1 * time.Second)

	// Cluster secret update from the managed service account
	clusterSecret, err = gitopsc.(*ReconcileGitOpsCluster).CreateMangedClusterSecretFromManagedServiceAccount(
		argocdServerNamespace1.Name, mc1, msa.Name, true)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(json.Unmarshal([]byte(clusterSecret.StringData["config"]), &data)).NotTo(gomega.HaveOccurred())
	g.Expect(data["bearerToken"].(string)).To(gomega.Equal("token2"))
}

func TestGetAllNonAcmManagedClusterSecretsInArgo(t *testing.T) {
	argoNs := "argons"

	acmCluster1Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1-cluster-secret",
			Namespace: argoNs,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type":              "cluster",
				"apps.open-cluster-management.io/acm-cluster": "true",
			},
		},
		StringData: map[string]string{
			"name":       "cluster1",
			"server":     "https://api.cluster1.com:6443",
			"config":     "test-bearer-token-1",
			"dummy-data": "test-dummy-data",
		},
	}

	acmCluster2Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster2-cluster-secret",
			Namespace: argoNs,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type":              "cluster",
				"apps.open-cluster-management.io/acm-cluster": "true",
			},
		},
		StringData: map[string]string{
			"name":       "cluster2",
			"server":     "https://api.cluster2.com:6443",
			"config":     "test-bearer-token-1",
			"dummy-data": "test-dummy-data",
		},
	}

	cusCluster1Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1-sa1-cluster-secret",
			Namespace: argoNs,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type": "cluster",
			},
		},
		StringData: map[string]string{
			"name":       "cluster1",
			"server":     "https://api.cluster1.com:6443",
			"config":     "test-bearer-token-1",
			"dummy-data": "test-dummy-data",
		},
	}

	cusCluster2Secret1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster2-sa1-cluster-secret",
			Namespace: argoNs,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type": "cluster",
			},
		},
		StringData: map[string]string{
			"name":       "cluster2",
			"server":     "https://api.cluster2.com:6443",
			"config":     "test-bearer-token-1",
			"dummy-data": "test-dummy-data",
		},
	}

	cusCluster2Secret2 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster2-sa2-cluster-secret",
			Namespace: argoNs,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type": "cluster",
			},
		},
		StringData: map[string]string{
			"name":       "cluster2",
			"server":     "https://api.cluster2.com:6443",
			"config":     "test-bearer-token-1",
			"dummy-data": "test-dummy-data",
		},
	}

	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})

	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	gitopsc, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	time.Sleep(time.Second * 3)

	argoNsNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: argoNs,
		},
	}

	g.Expect(c.Create(context.TODO(), argoNsNs)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), argoNsNs)

	// No cluster secrets
	clustersecretsMap, err := gitopsc.(*ReconcileGitOpsCluster).GetAllNonAcmManagedClusterSecretsInArgo(argoNs)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(len(clustersecretsMap)).To(gomega.Equal(0))

	// ACM cluster secrets
	g.Expect(c.Create(context.TODO(), acmCluster1Secret)).NotTo(gomega.HaveOccurred())
	g.Expect(c.Create(context.TODO(), acmCluster2Secret)).NotTo(gomega.HaveOccurred())

	clustersecretsMap, err = gitopsc.(*ReconcileGitOpsCluster).GetAllNonAcmManagedClusterSecretsInArgo(argoNs)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(len(clustersecretsMap)).To(gomega.Equal(0))

	// Non-ACM cluster secrets
	g.Expect(c.Create(context.TODO(), cusCluster1Secret)).NotTo(gomega.HaveOccurred())
	g.Expect(c.Create(context.TODO(), cusCluster2Secret1)).NotTo(gomega.HaveOccurred())
	g.Expect(c.Create(context.TODO(), cusCluster2Secret2)).NotTo(gomega.HaveOccurred())

	time.Sleep(1 * time.Second)

	clustersecretsMap, err = gitopsc.(*ReconcileGitOpsCluster).GetAllNonAcmManagedClusterSecretsInArgo(argoNs)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(len(clustersecretsMap["cluster1"])).To(gomega.Equal(1))
	g.Expect(len(clustersecretsMap["cluster2"])).To(gomega.Equal(2))
}

func TestGetManagedClusterURL(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	c := initClient()

	g.Expect(c.Create(context.TODO(), managedCluster1.DeepCopy())).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), managedCluster1)

	tests := []struct {
		name          string
		clientconfigs []clusterv1.ClientConfig
		want          string
		wantErr       string
	}{
		{
			name:          "No client configs",
			clientconfigs: []clusterv1.ClientConfig{},
			want:          "",
			wantErr:       "no client configs found for managed cluster: cluster1",
		},
		{
			name:          "One client configs",
			clientconfigs: []clusterv1.ClientConfig{{URL: "https://local-cluster:6443", CABundle: []byte("abc")}},
			want:          "https://local-cluster:6443",
			wantErr:       "",
		},
		{
			name: "Two failed client configs",
			clientconfigs: []clusterv1.ClientConfig{{URL: "https://local-cluster:6443", CABundle: []byte("abc")},
				{URL: "https://local-cluster:9443", CABundle: []byte("abc")}},
			want:    "",
			wantErr: "failed to find an accessible URL for the managed cluster: cluster1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g.Expect(c.Get(context.TODO(), client.ObjectKeyFromObject(managedCluster1), managedCluster1)).NotTo(gomega.HaveOccurred())

			managedCluster1.Spec.ManagedClusterClientConfigs = tt.clientconfigs
			g.Expect(c.Update(context.TODO(), managedCluster1)).NotTo(gomega.HaveOccurred())

			got, gotErr := getManagedClusterURL(managedCluster1, "")
			if tt.wantErr != "" && (gotErr == nil || tt.wantErr != gotErr.Error()) {
				t.Errorf("getManagedClusterURL() err = %v, want %v", gotErr, tt.wantErr)
			}

			g.Expect(got).To(gomega.Equal(tt.want))
		})
	}
}

func initClient() client.Client {
	ncb := fake.NewClientBuilder()
	return ncb.Build()
}

func Test_generatePlacementYamlString(t *testing.T) {
	tests := []struct {
		name          string
		gitOpsCluster gitopsclusterV1beta1.GitOpsCluster
		want          string
	}{
		{
			name: "normal",
			gitOpsCluster: gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gitopscluster",
					Namespace: "argocd",
					UID:       "551ce4eb-48dd-459b-b95f-27c70097ccec",
				},
			},
			want: `
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: gitopscluster-policy-local-placement
  namespace: argocd
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: gitopscluster
    uid: 551ce4eb-48dd-459b-b95f-27c70097ccec
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
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := generatePlacementYamlString(tt.gitOpsCluster); got != tt.want {
				t.Errorf("generatePlacementYamlString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_generatePlacementBindingYamlString(t *testing.T) {
	tests := []struct {
		name          string
		gitOpsCluster gitopsclusterV1beta1.GitOpsCluster
		want          string
	}{
		{
			name: "normal",
			gitOpsCluster: gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gitopscluster",
					Namespace: "argocd",
					UID:       "551ce4eb-48dd-459b-b95f-27c70097ccec",
				},
			},
			want: `
apiVersion: policy.open-cluster-management.io/v1
kind: PlacementBinding
metadata:
  name: gitopscluster-policy-local-placement-binding
  namespace: argocd
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: gitopscluster
    uid: 551ce4eb-48dd-459b-b95f-27c70097ccec
placementRef:
  name: gitopscluster-policy-local-placement
  kind: Placement
  apiGroup: cluster.open-cluster-management.io
subjects:
  - name: gitopscluster-policy
    kind: Policy
    apiGroup: policy.open-cluster-management.io
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := generatePlacementBindingYamlString(tt.gitOpsCluster); got != tt.want {
				t.Errorf("generatePlacementBindingYamlString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_generatePolicyTemplateYamlString(t *testing.T) {
	tests := []struct {
		name          string
		gitOpsCluster gitopsclusterV1beta1.GitOpsCluster
		want          string
	}{
		{
			name: "normal",
			gitOpsCluster: gitopsclusterV1beta1.GitOpsCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gitopscluster",
					Namespace: "argocd",
					UID:       "551ce4eb-48dd-459b-b95f-27c70097ccec",
				},
				Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
					ManagedServiceAccountRef: "msa",
					PlacementRef: &corev1.ObjectReference{
						Name: "placement",
					},
				},
			},
			want: `
apiVersion: policy.open-cluster-management.io/v1
kind: Policy
metadata:
  name: gitopscluster-policy
  namespace: argocd
  annotations:
    policy.open-cluster-management.io/standards: NIST-CSF
    policy.open-cluster-management.io/categories: PR.PT Protective Technology
    policy.open-cluster-management.io/controls: PR.PT-3 Least Functionality
  ownerReferences:
  - apiVersion: apps.open-cluster-management.io/v1beta1
    kind: GitOpsCluster
    name: gitopscluster
    uid: 551ce4eb-48dd-459b-b95f-27c70097ccec
spec:
  remediationAction: enforce
  disabled: false
  policy-templates:
    - objectDefinition:
        apiVersion: policy.open-cluster-management.io/v1
        kind: ConfigurationPolicy
        metadata:
          name: gitopscluster-config-policy
        spec:
          pruneObjectBehavior: DeleteIfCreated
          remediationAction: enforce
          severity: low
          object-templates-raw: |
            {{ range $placedec := (lookup "cluster.open-cluster-management.io/v1beta1" "PlacementDecision" "argocd" "" "cluster.open-cluster-management.io/placement=placement").items }}
            {{ range $clustdec := $placedec.status.decisions }}
            - complianceType: musthave
              objectDefinition:
                apiVersion: authentication.open-cluster-management.io/v1alpha1
                kind: ManagedServiceAccount
                metadata:
                  name: msa
                  namespace: {{ $clustdec.clusterName }}
                spec:
                  rotation: {}
            - complianceType: musthave
              objectDefinition:
                apiVersion: rbac.open-cluster-management.io/v1alpha1
                kind: ClusterPermission
                metadata:
                  name: gitopscluster-cluster-permission
                  namespace: {{ $clustdec.clusterName }}
                spec: {}
            {{ end }}
            {{ end }}
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := generatePolicyTemplateYamlString(tt.gitOpsCluster); got != tt.want {
				t.Errorf("generatePolicyTemplateYamlString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_createNamespaceScopedResourceFromYAML(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})

	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	gitopsc, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	time.Sleep(time.Second * 3)

	configMapYaml := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-configmap
  namespace: default
data:
  foo: bar
`

	err = gitopsc.(*ReconcileGitOpsCluster).createNamespaceScopedResourceFromYAML(configMapYaml)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: "default", Name: "test-configmap"},
		cm)).NotTo(gomega.HaveOccurred())

	configMapYaml = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-configmap
  namespace: default
data:
  bar: foo
`

	err = gitopsc.(*ReconcileGitOpsCluster).createNamespaceScopedResourceFromYAML(configMapYaml)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	time.Sleep(time.Second * 3)

	g.Expect(c.Get(context.TODO(),
		types.NamespacedName{Namespace: "default", Name: "test-configmap"},
		cm)).NotTo(gomega.HaveOccurred())

	g.Expect(cm.Data).To(gomega.Equal(map[string]string{"bar": "foo"}))
}

// Test ArgoCD Agent functionality
func TestArgoCDAgentEnabled(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create test resources
	testNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-argocd-agent",
		},
	}

	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-agent",
		},
	}

	testPlacement := &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-agent",
			Namespace: testNs.Name,
		},
	}

	testPlacementDecision := &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-decision-agent",
			Namespace: testNs.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "test-placement-agent",
			},
		},
		Status: clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{
				{
					ClusterName: "test-cluster-agent",
					Reason:      "OK",
				},
			},
		},
	}

	// Create ArgoCD agent CA secret
	argoCDAgentCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "openshift-gitops",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("-----BEGIN CERTIFICATE-----\nMIIDOjCCAiKgAwIBAgIRAP6XPGEj/+8Ict6b2y015IwDQYJKoZIhvcNAQELBQAw\n-----END CERTIFICATE-----"),
			"tls.key": []byte("-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEAzbMjs2eyusPY2xNwVJQMhhi8wKK3sl8CgLhkhKPnkGmtIFmR\n-----END RSA PRIVATE KEY-----"),
		},
	}

	// Create GitOpsCluster with ArgoCD agent enabled
	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-agent",
			Namespace: testNs.Name,
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "openshift-gitops",
			},
			PlacementRef: &corev1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Name:       "test-placement-agent",
				Namespace:  testNs.Name,
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0], // pointer to true
			},
		},
	}

	// Create fake client with test resources
	objs := []client.Object{
		testNs,
		testCluster,
		testPlacement,
		testPlacementDecision,
		argoCDAgentCASecret,
		testGitOpsCluster,
	}

	fakeClient := fake.NewClientBuilder().WithObjects(objs...).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test CreateArgoCDAgentManifestWorks function
	managedClusters := []*clusterv1.ManagedCluster{testCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork was created
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-agent",
	}, manifestWork)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork content
	g.Expect(manifestWork.Name).To(gomega.Equal("argocd-agent-ca-mw"))
	g.Expect(manifestWork.Namespace).To(gomega.Equal("test-cluster-agent"))
	g.Expect(len(manifestWork.Spec.Workload.Manifests)).To(gomega.Equal(1))

	// Verify the secret manifest in the ManifestWork
	manifest := manifestWork.Spec.Workload.Manifests[0]
	secret := &corev1.Secret{}
	err = json.Unmarshal(manifest.RawExtension.Raw, secret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(secret.Name).To(gomega.Equal("argocd-agent-ca"))
	g.Expect(secret.Namespace).To(gomega.Equal("openshift-gitops"))
	g.Expect(secret.Type).To(gomega.Equal(corev1.SecretTypeOpaque))
	g.Expect(secret.Data["ca.crt"]).NotTo(gomega.BeEmpty())
}

func TestArgoCDAgentDisabled(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create test resources
	testNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-argocd-agent-disabled",
		},
	}

	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-agent-disabled",
		},
	}

	// Create GitOpsCluster with ArgoCD agent disabled
	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-agent-disabled",
			Namespace: testNs.Name,
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "openshift-gitops",
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{false}[0], // pointer to false
			},
		},
	}

	// Create fake client with test resources
	objs := []client.Object{
		testNs,
		testCluster,
		testGitOpsCluster,
	}

	fakeClient := fake.NewClientBuilder().WithObjects(objs...).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test CreateArgoCDAgentManifestWorks function
	managedClusters := []*clusterv1.ManagedCluster{testCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).To(gomega.HaveOccurred()) // Should fail because CA secret doesn't exist

	// Verify ManifestWork was NOT created
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-agent-disabled",
	}, manifestWork)
	g.Expect(err).To(gomega.HaveOccurred()) // Should not exist
}

func TestGetArgoCDAgentCACert(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with valid secret
	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "test-namespace",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("test-certificate-data"),
			"tls.key": []byte("test-key-data"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithObjects(testSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test successful retrieval
	caCert, err := reconciler.getArgoCDAgentCACert("test-namespace")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(caCert).To(gomega.Equal("test-certificate-data"))

	// Test with missing secret
	_, err = reconciler.getArgoCDAgentCACert("nonexistent-namespace")
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("failed to get argocd-agent-ca secret"))
}

func TestGetArgoCDAgentCACertMissingTLSCrt(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Test with secret missing tls.crt
	testSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "test-namespace",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.key": []byte("test-key-data"),
		},
	}

	fakeClient := fake.NewClientBuilder().WithObjects(testSecret).Build()
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test missing tls.crt field
	_, err := reconciler.getArgoCDAgentCACert("test-namespace")
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(err.Error()).To(gomega.ContainSubstring("neither tls.crt nor ca.crt found in argocd-agent-ca secret"))
}

func TestCreateArgoCDAgentManifestWork(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	reconciler := &ReconcileGitOpsCluster{}

	// Test ManifestWork creation
	manifestWork := reconciler.createArgoCDAgentManifestWork("test-cluster", "test-namespace", "test-ca-cert")

	g.Expect(manifestWork.Name).To(gomega.Equal("argocd-agent-ca-mw"))
	g.Expect(manifestWork.Namespace).To(gomega.Equal("test-cluster"))
	g.Expect(manifestWork.APIVersion).To(gomega.Equal("work.open-cluster-management.io/v1"))
	g.Expect(manifestWork.Kind).To(gomega.Equal("ManifestWork"))
	g.Expect(len(manifestWork.Spec.Workload.Manifests)).To(gomega.Equal(1))

	// Verify the secret manifest
	manifest := manifestWork.Spec.Workload.Manifests[0]
	secret := manifest.RawExtension.Object.(*corev1.Secret)
	g.Expect(secret.Name).To(gomega.Equal("argocd-agent-ca"))
	g.Expect(secret.Namespace).To(gomega.Equal("test-namespace"))
	g.Expect(secret.Type).To(gomega.Equal(corev1.SecretTypeOpaque))
	g.Expect(string(secret.Data["ca.crt"])).To(gomega.Equal("test-ca-cert"))
}

func TestArgoCDAgentDefaultNamespace(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create test resources
	testNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-default-namespace",
		},
	}

	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-default",
		},
	}

	// Create ArgoCD agent CA secret in default namespace
	argoCDAgentCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-ca",
			Namespace: "openshift-gitops", // Default namespace
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": []byte("test-certificate-data"),
			"tls.key": []byte("test-key-data"),
		},
	}

	// Create GitOpsCluster with empty ArgoNamespace (should default to openshift-gitops)
	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-default",
			Namespace: testNs.Name,
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				ArgoNamespace: "", // Empty namespace should default to openshift-gitops
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0],
			},
		},
	}

	// Create fake client with test resources
	objs := []client.Object{
		testNs,
		testCluster,
		argoCDAgentCASecret,
		testGitOpsCluster,
	}

	fakeClient := fake.NewClientBuilder().WithObjects(objs...).Build()

	// Create reconciler
	reconciler := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Test CreateArgoCDAgentManifestWorks function
	managedClusters := []*clusterv1.ManagedCluster{testCluster}
	err := reconciler.CreateArgoCDAgentManifestWorks(testGitOpsCluster, managedClusters)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify ManifestWork was created
	manifestWork := &workv1.ManifestWork{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "argocd-agent-ca-mw",
		Namespace: "test-cluster-default",
	}, manifestWork)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	// Verify the secret manifest uses the default namespace
	manifest := manifestWork.Spec.Workload.Manifests[0]
	secret := &corev1.Secret{}
	err = json.Unmarshal(manifest.RawExtension.Raw, secret)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(secret.Namespace).To(gomega.Equal("openshift-gitops")) // Should use default namespace
}

func TestEnsureArgoCDAgentCertificatesFailure(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	mgr, err := manager.New(cfg, manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})

	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	reconciler, err := newReconciler(mgr)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	recFn := SetupTestReconcile(reconciler)
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Minute)
	mgrStopped := StartTestManager(ctx, mgr, g)

	defer func() {
		cancel()
		mgrStopped.Wait()
	}()

	// Create test namespace
	testNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-argocd-cert-failure",
		},
	}
	c.Create(context.TODO(), testNs)

	// Create managed cluster
	testCluster := &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-cert-failure",
		},
		Spec: clusterv1.ManagedClusterSpec{
			HubAcceptsClient: true,
		},
	}
	g.Expect(c.Create(context.TODO(), testCluster)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), testCluster)

	// Create managed cluster namespace
	testClusterNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testCluster.Name,
		},
	}
	c.Create(context.TODO(), testClusterNs)

	// Create placement
	testPlacement := &clusterv1beta1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-cert-failure",
			Namespace: testNs.Name,
		},
		Spec: clusterv1beta1.PlacementSpec{},
	}
	g.Expect(c.Create(context.TODO(), testPlacement)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), testPlacement)

	// Create placement decision
	testPlacementDecision := &clusterv1beta1.PlacementDecision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-placement-decision-cert-failure",
			Namespace: testNs.Name,
			Labels: map[string]string{
				"cluster.open-cluster-management.io/placement": "test-placement-cert-failure",
			},
		},
		Status: clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{
				{
					ClusterName: testCluster.Name,
					Reason:      "OK",
				},
			},
		},
	}
	g.Expect(c.Create(context.TODO(), testPlacementDecision)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), testPlacementDecision)

	time.Sleep(time.Second * 5)

	// Create ArgoCD namespace but WITHOUT the required CA secret (this will cause EnsureArgoCDAgentCertificates to fail)
	argoNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-argocd-cert-failure-ns",
		},
	}
	c.Create(context.TODO(), argoNs)

	// Create ArgoCD server service (needed for validation)
	argoService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-server",
			Namespace: argoNs.Name,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":   "argocd",
				"app.kubernetes.io/component": "server",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:       "10.0.0.99",
			SessionAffinity: corev1.ServiceAffinityNone,
			Type:            corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       int32(443),
					TargetPort: intstr.FromInt(443),
				},
			},
		},
	}
	g.Expect(c.Create(context.TODO(), argoService)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), argoService)

	// Create GitOpsCluster with ArgoCD agent enabled
	testGitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster-cert-failure",
			Namespace: testNs.Name,
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
				Cluster:       "local-cluster",
				ArgoNamespace: argoNs.Name, // This namespace has no CA secret
			},
			PlacementRef: &corev1.ObjectReference{
				Kind:       "Placement",
				APIVersion: "cluster.open-cluster-management.io/v1beta1",
				Namespace:  testNs.Name,
				Name:       testPlacement.Name,
			},
			ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
				Enabled: &[]bool{true}[0], // ArgoCD agent enabled - this will trigger EnsureArgoCDAgentCertificates
			},
		},
	}

	g.Expect(c.Create(context.TODO(), testGitOpsCluster)).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), testGitOpsCluster)

	// Wait for reconciler to process and verify the status shows failure
	g.Eventually(func(g2 gomega.Gomega) {
		updated := &gitopsclusterV1beta1.GitOpsCluster{}
		err := c.Get(context.TODO(), client.ObjectKeyFromObject(testGitOpsCluster), updated)
		g2.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify that the reconcile loop detected the certificate failure and set status to failed
		// Test legacy fields (backward compatibility)
		g2.Expect(updated.Status.Phase).To(gomega.Equal("failed"))
		g2.Expect(updated.Status.Message).To(gomega.ContainSubstring("failed to get argocd-agent-ca secret"))
		g2.Expect(updated.Status.LastUpdateTime).NotTo(gomega.BeNil())

		// Test new conditions are also set correctly
		g2.Expect(len(updated.Status.Conditions)).To(gomega.BeNumerically(">", 0))

		// Verify that certificates condition is set to False
		certCondition := updated.GetCondition(gitopsclusterV1beta1.GitOpsClusterCertificatesReady)
		if certCondition != nil {
			g2.Expect(certCondition.Status).To(gomega.Equal(metav1.ConditionFalse))
			g2.Expect(certCondition.Reason).To(gomega.Equal(gitopsclusterV1beta1.ReasonCertificateSigningFailed))
		}
	}, 60*time.Second, 2*time.Second).Should(gomega.Succeed())
}
