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
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	gitopsclusterV1beta1 "open-cluster-management.io/multicloud-integrations/pkg/apis/apps/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestVerifyCACertificateExists(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	tests := []struct {
		name         string
		secretName   string
		namespace    string
		createSecret bool
		expectError  bool
	}{
		{
			name:         "CA secret exists",
			secretName:   ArgoCDAgentCASecretName,
			namespace:    "test-namespace",
			createSecret: true,
			expectError:  false,
		},
		{
			name:         "CA secret does not exist",
			secretName:   ArgoCDAgentCASecretName,
			namespace:    "test-namespace",
			createSecret: false,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			if tt.createSecret {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tt.secretName,
						Namespace: tt.namespace,
					},
				}
				objs = append(objs, secret)
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
			r := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			err := r.verifyCACertificateExists(context.Background(), tt.namespace)

			if (err != nil) != tt.expectError {
				t.Errorf("verifyCACertificateExists() error = %v, expectError %v", err, tt.expectError)
			}
		})
	}
}

func TestFindArgoCDAgentPrincipalService(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	tests := []struct {
		name          string
		serviceName   string
		serviceLabels map[string]string
		namespace     string
		expectFound   bool
	}{
		{
			name:        "service found by hardcoded name - openshift-gitops-agent-principal",
			serviceName: "openshift-gitops-agent-principal",
			serviceLabels: map[string]string{
				"app": "gitops",
			},
			namespace:   "test-namespace",
			expectFound: true,
		},
		{
			name:        "service found by hardcoded name - argocd-agent-principal",
			serviceName: "argocd-agent-principal",
			serviceLabels: map[string]string{
				"app": "other",
			},
			namespace:   "test-namespace",
			expectFound: true,
		},
		{
			name:        "service found by suffix match",
			serviceName: "custom-agent-principal",
			serviceLabels: map[string]string{
				"app": "custom",
			},
			namespace:   "test-namespace",
			expectFound: true,
		},
		{
			name:          "service not found - no matching suffix",
			serviceName:   "other-service",
			serviceLabels: map[string]string{},
			namespace:     "test-namespace",
			expectFound:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.serviceName,
					Namespace: tt.namespace,
					Labels:    tt.serviceLabels,
				},
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(service).Build()
			r := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			result, err := r.FindArgoCDAgentPrincipalService(context.Background(), tt.namespace)

			if tt.expectFound {
				if err != nil {
					t.Errorf("FindArgoCDAgentPrincipalService() unexpected error = %v", err)
				}
				if result == nil {
					t.Error("FindArgoCDAgentPrincipalService() returned nil service")
				}
			} else {
				if err == nil {
					t.Error("FindArgoCDAgentPrincipalService() expected error but got nil")
				}
			}
		})
	}
}

func TestGetPrincipalHostNames(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = gitopsclusterV1beta1.AddToScheme(scheme)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-principal",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/name": "argocd-agent-principal",
			},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{
						IP:       "10.0.0.1",
						Hostname: "test.example.com",
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(service).Build()
	r := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Create a GitOpsCluster with agent config including serverAddress
	gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster",
			Namespace: "test-namespace",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					ServerAddress: "test-server.example.com",
				},
			},
		},
	}

	hostnames := r.getPrincipalHostNames(context.Background(), "test-namespace", gitOpsCluster)

	if len(hostnames) == 0 {
		t.Error("getPrincipalHostNames() returned empty hostnames")
	}

	// Verify basic hostnames are included
	hasLocalhost := false
	hasInternalDNS := false
	for _, h := range hostnames {
		if h == "localhost" {
			hasLocalhost = true
		}
		if h == "argocd-agent-principal.test-namespace.svc" {
			hasInternalDNS = true
		}
	}

	if !hasLocalhost {
		t.Error("getPrincipalHostNames() should include localhost")
	}
	if !hasInternalDNS {
		t.Error("getPrincipalHostNames() should include internal DNS name")
	}
}

func TestGetResourceProxyHostNames(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	hostnames := r.getResourceProxyHostNames(context.Background(), "test-namespace")

	if len(hostnames) == 0 {
		t.Error("getResourceProxyHostNames() returned empty hostnames")
	}

	// Verify basic hostnames are included
	hasLocalhost := false
	for _, h := range hostnames {
		if h == "localhost" {
			hasLocalhost = true
		}
	}

	if !hasLocalhost {
		t.Error("getResourceProxyHostNames() should include localhost")
	}
}

func TestCertificateConstants(t *testing.T) {
	// Verify important constants are set
	if ArgoCDAgentCASecretName == "" {
		t.Error("ArgoCDAgentCASecretName should not be empty")
	}
	if ArgoCDAgentPrincipalTLSSecretName == "" {
		t.Error("ArgoCDAgentPrincipalTLSSecretName should not be empty")
	}
	if ArgoCDAgentResourceProxyTLSSecretName == "" {
		t.Error("ArgoCDAgentResourceProxyTLSSecretName should not be empty")
	}
	if CASignerNamePrefix == "" {
		t.Error("CASignerNamePrefix should not be empty")
	}
}

func TestDiscoverRouteHostname(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = routev1.AddToScheme(scheme)

	tests := []struct {
		name             string
		namespace        string
		routes           []routev1.Route
		expectedHostname string
		expectError      bool
	}{
		{
			name:      "discover from principal route",
			namespace: "openshift-gitops",
			routes: []routev1.Route{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd-agent",
						},
					},
					Spec: routev1.RouteSpec{
						Host: "principal.apps.example.com",
					},
				},
			},
			expectedHostname: "principal.apps.example.com",
			expectError:      false,
		},
		{
			name:      "prefer principal route over others",
			namespace: "openshift-gitops",
			routes: []routev1.Route{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-other",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd-agent",
						},
					},
					Spec: routev1.RouteSpec{
						Host: "other.apps.example.com",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd-agent",
						},
					},
					Spec: routev1.RouteSpec{
						Host: "principal.apps.example.com",
					},
				},
			},
			expectedHostname: "principal.apps.example.com",
			expectError:      false,
		},
		{
			name:      "fallback to first route when no principal",
			namespace: "openshift-gitops",
			routes: []routev1.Route{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-server",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd-agent",
						},
					},
					Spec: routev1.RouteSpec{
						Host: "server.apps.example.com",
					},
				},
			},
			expectedHostname: "server.apps.example.com",
			expectError:      false,
		},
		{
			name:        "no routes found",
			namespace:   "openshift-gitops",
			routes:      []routev1.Route{},
			expectError: true,
		},
		{
			name:      "route with wrong labels",
			namespace: "openshift-gitops",
			routes: []routev1.Route{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "argocd-server",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd", // Not argocd-agent
						},
					},
					Spec: routev1.RouteSpec{
						Host: "server.apps.example.com",
					},
				},
			},
			expectError: true,
		},
		{
			name:      "route without host",
			namespace: "openshift-gitops",
			routes: []routev1.Route{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "openshift-gitops-agent-principal",
						Namespace: "openshift-gitops",
						Labels: map[string]string{
							"app.kubernetes.io/part-of": "argocd-agent",
						},
					},
					Spec: routev1.RouteSpec{
						// No host configured
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			for i := range tt.routes {
				objs = append(objs, &tt.routes[i])
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
			r := &ReconcileGitOpsCluster{
				Client: fakeClient,
			}

			hostname, err := r.discoverRouteHostname(context.Background(), tt.namespace)

			if tt.expectError {
				if err == nil {
					t.Error("discoverRouteHostname() expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("discoverRouteHostname() unexpected error: %v", err)
				}
				if hostname != tt.expectedHostname {
					t.Errorf("discoverRouteHostname() = %v, want %v", hostname, tt.expectedHostname)
				}
			}
		})
	}
}

func TestGetPrincipalHostNames_WithRoute(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = routev1.AddToScheme(scheme)

	// Create a route with argocd-agent labels
	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openshift-gitops-agent-principal",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "argocd-agent",
			},
		},
		Spec: routev1.RouteSpec{
			Host: "principal.apps.example.com",
		},
	}

	// Create a service for LoadBalancer fallback
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-agent-principal",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/name": "argocd-agent-principal",
			},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{
						Hostname: "lb.example.com",
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(route, service).Build()
	r := &ReconcileGitOpsCluster{
		Client: fakeClient,
	}

	// Create a GitOpsCluster with agent config
	gitOpsCluster := &gitopsclusterV1beta1.GitOpsCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gitopscluster",
			Namespace: "test-namespace",
		},
		Spec: gitopsclusterV1beta1.GitOpsClusterSpec{
			GitOpsAddon: &gitopsclusterV1beta1.GitOpsAddonSpec{
				ArgoCDAgent: &gitopsclusterV1beta1.ArgoCDAgentSpec{
					ServerAddress: "agent-server.example.com",
				},
			},
		},
	}

	hostnames := r.getPrincipalHostNames(context.Background(), "test-namespace", gitOpsCluster)

	if len(hostnames) == 0 {
		t.Error("getPrincipalHostNames() returned empty hostnames")
	}

	// Verify Route hostname is included
	hasRouteHostname := false
	for _, h := range hostnames {
		if h == "principal.apps.example.com" {
			hasRouteHostname = true
			break
		}
	}

	if !hasRouteHostname {
		t.Error("getPrincipalHostNames() should include Route hostname")
	}

	// Verify basic hostnames are included
	hasLocalhost := false
	for _, h := range hostnames {
		if h == "localhost" {
			hasLocalhost = true
			break
		}
	}

	if !hasLocalhost {
		t.Error("getPrincipalHostNames() should include localhost")
	}
}
