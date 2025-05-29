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

package gitopsaddon

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
)

var (
	SyncInterval        = 10
	GitopsOperatorImage = "registry.redhat.io/openshift-gitops-1/gitops-rhel8-operator@sha256:2a932c0397dcd29a75216a7d0467a640decf8651d41afe74379860035a93a6bd"
	GitopsOperatorNS    = "openshift-gitops-operator"
	GitopsImage         = "registry.redhat.io/openshift-gitops-1/argocd-rhel8@sha256:94e19aca2c330ec15a7de3c2d9309bb2e956320ef29dae2df3dfe6b9cad4ed39"
	GitopsNS            = "openshift-gitops"
	RedisImage          = "registry.redhat.io/rhel9/redis-7@sha256:848f4298a9465dafb7ce9790e991bd8a11de2558e3a6685e1d7c4a6e0fc5f371"
	ReconcileScope      = "Single-Namespace"
	HTTP_PROXY          = ""
	HTTPS_PROXY         = ""
	NO_PROXY            = ""
	ACTION              = "" //options: "Install", "Delete-Operator", "Delete-Instance"
)

func setupHelmWithEnvTestConfig(cfg *rest.Config) (*genericclioptions.ConfigFlags, error) {
	configFlags := genericclioptions.NewConfigFlags(false)

	// Set the basic connection info
	configFlags.APIServer = &cfg.Host

	// Handle TLS settings
	if cfg.TLSClientConfig.CAData != nil {
		tmpFile, err := os.CreateTemp("", "ca-")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp CA file: %w", err)
		}

		if err := os.WriteFile(tmpFile.Name(), cfg.TLSClientConfig.CAData, 0600); err != nil {
			return nil, fmt.Errorf("failed to write CA data: %w", err)
		}
		caFile := tmpFile.Name()
		configFlags.CAFile = &caFile
	}

	// Set bearer token if it exists
	if len(cfg.BearerToken) > 0 {
		configFlags.BearerToken = &cfg.BearerToken
	}

	// Handle client certificate authentication
	if len(cfg.TLSClientConfig.CertData) > 0 {
		tmpCert, err := os.CreateTemp("", "cert-")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp cert file: %w", err)
		}

		if err := os.WriteFile(tmpCert.Name(), cfg.TLSClientConfig.CertData, 0600); err != nil {
			return nil, fmt.Errorf("failed to write cert data: %w", err)
		}
		certFile := tmpCert.Name()
		configFlags.CertFile = &certFile
	}

	if len(cfg.TLSClientConfig.KeyData) > 0 {
		tmpKey, err := os.CreateTemp("", "key-")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp key file: %w", err)
		}

		if err := os.WriteFile(tmpKey.Name(), cfg.TLSClientConfig.KeyData, 0600); err != nil {
			return nil, fmt.Errorf("failed to write key data: %w", err)
		}
		keyFile := tmpKey.Name()
		configFlags.KeyFile = &keyFile
	}

	return configFlags, nil
}

func GetPodNamespace() string {
	addonNameSpace := ""
	nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")

	if err != nil || len(nsBytes) == 0 {
		klog.Infof("failed to get current pod namespace. error: %v", err)
	} else {
		addonNameSpace = string(nsBytes)
	}

	klog.Infof("current Pod NS = %v", addonNameSpace)

	return addonNameSpace
}

func TestGitopsAddon(t *testing.T) {
	g := NewGomegaWithT(t)

	configFlags, err := setupHelmWithEnvTestConfig(cfg)
	g.Expect(err).NotTo(HaveOccurred())

	// verify to install gitops operator helm chart
	gitopsAddonReconciler := &GitopsAddonReconciler{
		Client:              c,
		Scheme:              c.Scheme(),
		Config:              cfg,
		Interval:            SyncInterval,
		GitopsOperatorImage: GitopsOperatorImage,
		GitopsOperatorNS:    GitopsOperatorNS,
		GitopsImage:         GitopsImage,
		GitopsNS:            GitopsNS,
		RedisImage:          RedisImage,
		ReconcileScope:      ReconcileScope,
		HTTP_PROXY:          HTTP_PROXY,
		HTTPS_PROXY:         HTTPS_PROXY,
		NO_PROXY:            NO_PROXY,
		ACTION:              ACTION,
	}

	podNS := GetPodNamespace()
	g.Expect(gitopsAddonReconciler.createDepResources(podNS)).NotTo(HaveOccurred())

	g.Expect(gitopsAddonReconciler.createNamespace(GitopsOperatorNS)).NotTo(HaveOccurred())
	g.Expect(gitopsAddonReconciler.createNamespace(GitopsNS)).NotTo(HaveOccurred())

	time.Sleep(5 * time.Second)

	gitopsAddonReconciler.houseKeeping(configFlags)

	// verify the gitops meta data are saved to namspace successfully
	namespace := &corev1.Namespace{}
	g.Expect(gitopsAddonReconciler.Get(context.TODO(), types.NamespacedName{Name: GitopsOperatorNS}, namespace)).NotTo(HaveOccurred())
	g.Expect(namespace.Annotations["apps.open-cluster-management.io/gitops-operator-image"]).To(Equal(GitopsOperatorImage))
	g.Expect(namespace.Annotations["apps.open-cluster-management.io/gitops-operator-ns"]).To(Equal(GitopsOperatorNS))

	namespace = &corev1.Namespace{}
	g.Expect(gitopsAddonReconciler.Get(context.TODO(), types.NamespacedName{Name: GitopsNS}, namespace)).NotTo(HaveOccurred())
	g.Expect(namespace.Annotations["apps.open-cluster-management.io/gitops-image"]).To(Equal(GitopsImage))
	g.Expect(namespace.Annotations["apps.open-cluster-management.io/gitops-ns"]).To(Equal(GitopsNS))
	g.Expect(namespace.Annotations["apps.open-cluster-management.io/redis-image"]).To(Equal(RedisImage))
	g.Expect(namespace.Annotations["apps.open-cluster-management.io/reconcile-scope"]).To(Equal(ReconcileScope))

	// verify to delete gitops dependency helm chart
	gitopsAddonReconciler.ACTION = "Delete-Instance"

	gitopsAddonReconciler.houseKeeping(configFlags)

	err = gitopsAddonReconciler.getServiceAccount("openshift-gitops", "openshift-gitops-argocd-application-controller")
	g.Expect(errors.IsNotFound(err)).To(Equal(true))

	err = gitopsAddonReconciler.getServiceAccount("openshift-gitops", "openshift-gitops-argocd-redis")
	g.Expect(errors.IsNotFound(err)).To(Equal(true))

	// clean up temp files
	if configFlags.CAFile != nil {
		os.Remove(*configFlags.CAFile)
	}

	if configFlags.CertFile != nil {
		os.Remove(*configFlags.CertFile)
	}

	if configFlags.KeyFile != nil {
		os.Remove(*configFlags.KeyFile)
	}
}

func (r *GitopsAddonReconciler) getServiceAccount(namespace, name string) error {
	sa := &corev1.ServiceAccount{}
	return r.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, sa)
}

func (r *GitopsAddonReconciler) createDepResources(podNamespace string) error {
	// create app namespace
	appNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "app",
		},
	}

	if err := r.Create(context.TODO(), appNS); err != nil {
		return err
	}

	// create gitops addon default namespace
	// podNamespace == "", the unit test is not running inside of k8s cluster pod
	// podNamespace > "", the unit test is running inside of k8s cluster pod (cpaas prow)
	if podNamespace == "" {
		podNamespace = "open-cluster-management-agent-addon"
	}
	gitopsNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: podNamespace,
		},
	}

	if err := r.Create(context.TODO(), gitopsNS); err != nil {
		return err
	}

	// create gitops addon image pull secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: podNamespace,
			Name:      "open-cluster-management-image-pull-credentials",
		},
	}

	if err := r.Create(context.TODO(), secret); err != nil {
		return err
	}

	return nil
}

func (r *GitopsAddonReconciler) createNamespace(nameSpaceName string) error {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nameSpaceName,
			Labels: map[string]string{
				"addon.open-cluster-management.io/namespace":  "true",
				"apps.open-cluster-management.io/gitopsaddon": "true",
			},
		},
	}

	if err := r.Create(context.TODO(), namespace); err != nil {
		return err
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: nameSpaceName,
			Name:      "default",
		},
	}

	if err := r.Create(context.TODO(), sa); err != nil {
		return err
	}

	saKey := types.NamespacedName{
		Name:      "default",
		Namespace: nameSpaceName,
	}

	if err := r.patchDefaultSA(saKey); err != nil {
		return err
	}

	return nil
}
