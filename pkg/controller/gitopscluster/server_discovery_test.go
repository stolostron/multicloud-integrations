package gitopscluster

import (
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	spokeclusterv1 "open-cluster-management.io/api/cluster/v1"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDiscoverServerAddressAndPort(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create scheme for fake client
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	spokeclusterv1.AddToScheme(scheme)

	t.Run("DiscoverFromLoadBalancerHostname", func(t *testing.T) {
		// Create a service with LoadBalancer hostname (AWS ELB example)
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "openshift-gitops",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							Hostname: "a268620f39c894f3c89ab43684752d42-1827832642.us-west-1.elb.amazonaws.com",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		address, port, err := reconciler.DiscoverServerAddressAndPort("openshift-gitops")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(address).To(gomega.Equal("a268620f39c894f3c89ab43684752d42-1827832642.us-west-1.elb.amazonaws.com"))
		g.Expect(port).To(gomega.Equal("443"))
	})

	t.Run("DiscoverFromLoadBalancerIP", func(t *testing.T) {
		// Create a service with LoadBalancer IP
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "openshift-gitops",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							IP: "192.168.1.100",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		address, port, err := reconciler.DiscoverServerAddressAndPort("openshift-gitops")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(address).To(gomega.Equal("192.168.1.100"))
		g.Expect(port).To(gomega.Equal("443"))
	})

	t.Run("DiscoverWithCustomPort", func(t *testing.T) {
		// Create a service with custom port
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "test-namespace",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 8443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							Hostname: "test.example.com",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		address, port, err := reconciler.DiscoverServerAddressAndPort("test-namespace")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(address).To(gomega.Equal("test.example.com"))
		g.Expect(port).To(gomega.Equal("8443"))
	})

	t.Run("PreferHostnameOverIP", func(t *testing.T) {
		// Create a service with both hostname and IP (hostname should be preferred)
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "test-namespace",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							IP:       "192.168.1.100",
							Hostname: "preferred.example.com",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		address, port, err := reconciler.DiscoverServerAddressAndPort("test-namespace")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(address).To(gomega.Equal("preferred.example.com")) // Hostname preferred over IP
		g.Expect(port).To(gomega.Equal("443"))
	})

	t.Run("FallbackToNonHTTPSPort", func(t *testing.T) {
		// Create a service without "https" named port - should fallback to port 443 or use first port
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "test-namespace",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "http",
						Port: 80,
					},
					{
						Name: "secure",
						Port: 443, // Should find this 443 port
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							Hostname: "test.example.com",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		address, port, err := reconciler.DiscoverServerAddressAndPort("test-namespace")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(address).To(gomega.Equal("test.example.com"))
		g.Expect(port).To(gomega.Equal("443")) // Should find the 443 port even without "https" name
	})

	t.Run("ServiceNotFound", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		_, _, err := reconciler.DiscoverServerAddressAndPort("nonexistent-namespace")
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("ArgoCD agent principal service not found"))
	})

	t.Run("NoLoadBalancerEndpoints", func(t *testing.T) {
		// Create a service without LoadBalancer endpoints
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "test-namespace",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{}, // Empty ingress
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		_, _, err := reconciler.DiscoverServerAddressAndPort("test-namespace")
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(err.Error()).To(gomega.ContainSubstring("no external LoadBalancer IP or hostname found"))
	})

	t.Run("FallbackServiceName", func(t *testing.T) {
		// Test with fallback service name (argocd-agent-principal instead of openshift-gitops-agent-principal)
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "argocd-agent-principal", // Fallback name
				Namespace: "argocd",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							Hostname: "fallback.example.com",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		address, port, err := reconciler.DiscoverServerAddressAndPort("argocd")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(address).To(gomega.Equal("fallback.example.com"))
		g.Expect(port).To(gomega.Equal("443"))
	})
}

func TestHasExistingServerConfig(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create scheme for fake client
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	addonv1alpha1.AddToScheme(scheme)
	spokeclusterv1.AddToScheme(scheme)

	t.Run("NoExistingConfigs", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		managedClusters := []*spokeclusterv1.ManagedCluster{
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster2"}},
		}

		hasConfig, err := reconciler.HasExistingServerConfig(managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(hasConfig).To(gomega.BeFalse())
	})

	t.Run("HasExistingServerAddress", func(t *testing.T) {
		// Create an AddOnDeploymentConfig with server address
		existingConfig := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: "cluster1",
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: []addonv1alpha1.CustomizedVariable{
					{
						Name:  "ARGOCD_AGENT_ENABLED",
						Value: "true",
					},
					{
						Name:  "ARGOCD_AGENT_SERVER_ADDRESS",
						Value: "existing.example.com",
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingConfig).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		managedClusters := []*spokeclusterv1.ManagedCluster{
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster1"}},
		}

		hasConfig, err := reconciler.HasExistingServerConfig(managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(hasConfig).To(gomega.BeTrue())
	})

	t.Run("HasExistingServerPort", func(t *testing.T) {
		// Create an AddOnDeploymentConfig with server port
		existingConfig := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: "cluster2",
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: []addonv1alpha1.CustomizedVariable{
					{
						Name:  "ARGOCD_AGENT_ENABLED",
						Value: "true",
					},
					{
						Name:  "ARGOCD_AGENT_SERVER_PORT",
						Value: "8443",
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingConfig).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		managedClusters := []*spokeclusterv1.ManagedCluster{
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster2"}},
		}

		hasConfig, err := reconciler.HasExistingServerConfig(managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(hasConfig).To(gomega.BeTrue())
	})

	t.Run("IgnoreEmptyValues", func(t *testing.T) {
		// Create an AddOnDeploymentConfig with empty server address/port values
		existingConfig := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: "cluster3",
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: []addonv1alpha1.CustomizedVariable{
					{
						Name:  "ARGOCD_AGENT_ENABLED",
						Value: "true",
					},
					{
						Name:  "ARGOCD_AGENT_SERVER_ADDRESS",
						Value: "", // Empty value should be ignored
					},
					{
						Name:  "ARGOCD_AGENT_SERVER_PORT",
						Value: "", // Empty value should be ignored
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingConfig).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		managedClusters := []*spokeclusterv1.ManagedCluster{
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster3"}},
		}

		hasConfig, err := reconciler.HasExistingServerConfig(managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(hasConfig).To(gomega.BeFalse()) // Empty values should be ignored
	})

	t.Run("CheckMultipleClusters", func(t *testing.T) {
		// Create configs for multiple clusters, one with server config
		existingConfig1 := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: "cluster1",
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: []addonv1alpha1.CustomizedVariable{
					{
						Name:  "ARGOCD_AGENT_ENABLED",
						Value: "true",
					},
				},
			},
		}

		existingConfig2 := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: "cluster2",
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: []addonv1alpha1.CustomizedVariable{
					{
						Name:  "ARGOCD_AGENT_ENABLED",
						Value: "true",
					},
					{
						Name:  "ARGOCD_AGENT_SERVER_ADDRESS",
						Value: "cluster2.example.com", // This cluster has server config
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingConfig1, existingConfig2).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		managedClusters := []*spokeclusterv1.ManagedCluster{
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster2"}},
		}

		hasConfig, err := reconciler.HasExistingServerConfig(managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(hasConfig).To(gomega.BeTrue()) // Should find config in cluster2
	})
}

func TestEnsureServerAddressAndPort(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create scheme for fake client
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	addonv1alpha1.AddToScheme(scheme)
	spokeclusterv1.AddToScheme(scheme)
	gitopsclusterV1beta1.AddToScheme(scheme)

	t.Run("SkipWhenServerAddressAlreadySet", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					ServerAddress: "existing.example.com",
				},
			},
		}

		managedClusters := []*spokeclusterv1.ManagedCluster{}

		updated, err := reconciler.EnsureServerAddressAndPort(gitOpsCluster, managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updated).To(gomega.BeFalse())
	})

	t.Run("SkipWhenServerPortAlreadySet", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					ServerPort: "8443",
				},
			},
		}

		managedClusters := []*spokeclusterv1.ManagedCluster{}

		updated, err := reconciler.EnsureServerAddressAndPort(gitOpsCluster, managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updated).To(gomega.BeFalse())
	})

	t.Run("SkipWhenExistingAddonConfigHasValues", func(t *testing.T) {
		// Create an existing addon config with server address
		existingConfig := &addonv1alpha1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-addon-config",
				Namespace: "cluster1",
			},
			Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
				CustomizedVariables: []addonv1alpha1.CustomizedVariable{
					{
						Name:  "ARGOCD_AGENT_SERVER_ADDRESS",
						Value: "existing-addon.example.com",
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingConfig).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
					ArgoNamespace: "openshift-gitops",
				},
				// ArgoCDAgent is nil - would normally trigger discovery
			},
		}

		managedClusters := []*spokeclusterv1.ManagedCluster{
			{ObjectMeta: metav1.ObjectMeta{Name: "cluster1"}},
		}

		updated, err := reconciler.EnsureServerAddressAndPort(gitOpsCluster, managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updated).To(gomega.BeFalse()) // Should skip because addon config already has values
	})

	t.Run("AutoDiscoverAndPopulate", func(t *testing.T) {
		// Create the ArgoCD agent principal service
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "openshift-gitops",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							Hostname: "discovered.example.com",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
					ArgoNamespace: "openshift-gitops",
				},
				// ArgoCDAgent is nil - should be initialized
			},
		}

		managedClusters := []*spokeclusterv1.ManagedCluster{}

		updated, err := reconciler.EnsureServerAddressAndPort(gitOpsCluster, managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updated).To(gomega.BeTrue())

		// Verify the GitOpsCluster was updated
		g.Expect(gitOpsCluster.Spec.ArgoCDAgent).NotTo(gomega.BeNil())
		g.Expect(gitOpsCluster.Spec.ArgoCDAgent.ServerAddress).To(gomega.Equal("discovered.example.com"))
		g.Expect(gitOpsCluster.Spec.ArgoCDAgent.ServerPort).To(gomega.Equal("443"))
	})

	t.Run("AutoDiscoverWithExistingArgoCDAgentSpec", func(t *testing.T) {
		// Create the ArgoCD agent principal service
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "custom-gitops",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 8443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							IP: "10.20.30.40",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-gitops",
				Namespace: "test-ns",
			},
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
					ArgoNamespace: "custom-gitops",
				},
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					// Has existing ArgoCDAgent spec but no server address/port
					Image: "custom-image:v1.0",
				},
			},
		}

		managedClusters := []*spokeclusterv1.ManagedCluster{}

		updated, err := reconciler.EnsureServerAddressAndPort(gitOpsCluster, managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updated).To(gomega.BeTrue())

		// Verify the GitOpsCluster was updated and existing fields preserved
		g.Expect(gitOpsCluster.Spec.ArgoCDAgent).NotTo(gomega.BeNil())
		g.Expect(gitOpsCluster.Spec.ArgoCDAgent.ServerAddress).To(gomega.Equal("10.20.30.40"))
		g.Expect(gitOpsCluster.Spec.ArgoCDAgent.ServerPort).To(gomega.Equal("8443"))
		g.Expect(gitOpsCluster.Spec.ArgoCDAgent.Image).To(gomega.Equal("custom-image:v1.0")) // Existing field preserved
	})

	t.Run("DefaultArgoNamespace", func(t *testing.T) {
		// Test with default argo namespace when ArgoNamespace is empty
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-gitops-agent-principal",
				Namespace: "openshift-gitops", // Default namespace
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name: "https",
						Port: 443,
					},
				},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{
						{
							Hostname: "default-namespace.example.com",
						},
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
					ArgoNamespace: "", // Empty - should default to "openshift-gitops"
				},
			},
		}

		managedClusters := []*spokeclusterv1.ManagedCluster{}

		updated, err := reconciler.EnsureServerAddressAndPort(gitOpsCluster, managedClusters)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(updated).To(gomega.BeTrue())

		g.Expect(gitOpsCluster.Spec.ArgoCDAgent.ServerAddress).To(gomega.Equal("default-namespace.example.com"))
	})

	t.Run("FailWhenServiceNotFound", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		reconciler := &ReconcileGitOpsCluster{Client: fakeClient}

		gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
			Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
				ArgoServer: gitopsclusterV1beta1.ArgoServerSpec{
					ArgoNamespace: "nonexistent-namespace",
				},
			},
		}

		managedClusters := []*spokeclusterv1.ManagedCluster{}

		updated, err := reconciler.EnsureServerAddressAndPort(gitOpsCluster, managedClusters)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(updated).To(gomega.BeFalse())
		g.Expect(err.Error()).To(gomega.ContainSubstring("failed to discover server address and port"))
	})
}
