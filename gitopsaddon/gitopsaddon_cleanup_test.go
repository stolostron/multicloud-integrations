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

package gitopsaddon

import (
	"context"
	"testing"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestCreatePauseMarker tests the pause marker creation
func TestCreatePauseMarker(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name             string
		existingObjects  []client.Object
		expectError      bool
		verifyPauseState bool
		verifyOwnerRef   bool
	}{
		{
			name: "create_pause_marker_successfully_with_deployment",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      AddonDeploymentName,
						Namespace: AddonNamespace,
						UID:       "test-deployment-uid",
					},
				},
			},
			expectError:      false,
			verifyPauseState: true,
			verifyOwnerRef:   true,
		},
		{
			name: "create_pause_marker_already_exists",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
						Labels: map[string]string{
							"app": "gitops-addon",
						},
					},
					Data: map[string]string{
						"paused": "true",
						"reason": "cleanup",
					},
				},
			},
			expectError:      false,
			verifyPauseState: true,
			verifyOwnerRef:   false,
		},
		{
			name: "create_pause_marker_without_deployment",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
			},
			expectError:      false,
			verifyPauseState: true,
			verifyOwnerRef:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := clientgoscheme.AddToScheme(scheme)
			g.Expect(err).ToNot(gomega.HaveOccurred())

			testClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			err = createPauseMarker(context.TODO(), testClient)

			if tt.expectError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}

			if tt.verifyPauseState {
				// Verify the pause marker exists and has correct state
				paused := IsPaused(context.TODO(), testClient)
				g.Expect(paused).To(gomega.BeTrue(), "Expected addon to be paused")

				// Verify the ConfigMap exists with correct data
				cm := &corev1.ConfigMap{}
				err = testClient.Get(context.TODO(), types.NamespacedName{
					Name:      PauseMarkerName,
					Namespace: AddonNamespace,
				}, cm)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(cm.Data["paused"]).To(gomega.Equal("true"))
				g.Expect(cm.Data["reason"]).To(gomega.Equal("cleanup"))

				// Verify owner reference if expected
				if tt.verifyOwnerRef {
					g.Expect(cm.OwnerReferences).To(gomega.HaveLen(1))
					g.Expect(cm.OwnerReferences[0].Kind).To(gomega.Equal("Deployment"))
					g.Expect(cm.OwnerReferences[0].Name).To(gomega.Equal(AddonDeploymentName))
				}
			}
		})
	}
}

// TestIsPaused tests the IsPaused function
func TestIsPaused(t *testing.T) {
	g := gomega.NewWithT(t)

	tests := []struct {
		name            string
		existingObjects []client.Object
		expectedPaused  bool
	}{
		{
			name: "not_paused_no_marker",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
			},
			expectedPaused: false,
		},
		{
			name: "paused_with_marker",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
					},
					Data: map[string]string{
						"paused": "true",
					},
				},
			},
			expectedPaused: true,
		},
		{
			name: "not_paused_marker_false",
			existingObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: AddonNamespace,
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      PauseMarkerName,
						Namespace: AddonNamespace,
					},
					Data: map[string]string{
						"paused": "false",
					},
				},
			},
			expectedPaused: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := clientgoscheme.AddToScheme(scheme)
			g.Expect(err).ToNot(gomega.HaveOccurred())

			testClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingObjects...).
				Build()

			paused := IsPaused(context.TODO(), testClient)
			g.Expect(paused).To(gomega.Equal(tt.expectedPaused))
		})
	}
}
