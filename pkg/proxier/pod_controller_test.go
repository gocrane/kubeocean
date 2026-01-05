// Copyright 2025 The Kubeocean Authors
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

package proxier

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
)

// TestNewPodController tests the creation of a new pod controller
func TestNewPodController(t *testing.T) {
	tests := []struct {
		name   string
		client client.Client
		scheme *runtime.Scheme
		log    logr.Logger
	}{
		{
			name:   "valid pod controller creation",
			client: fake.NewClientBuilder().Build(),
			scheme: runtime.NewScheme(),
			log:    logr.Discard(),
		},
		{
			name:   "pod controller with nil scheme",
			client: fake.NewClientBuilder().Build(),
			scheme: nil,
			log:    logr.Discard(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := NewPodController(tt.client, tt.scheme, tt.log)
			require.NotNil(t, controller)
			assert.Equal(t, tt.client, controller.Client)
			assert.Equal(t, tt.scheme, controller.Scheme)
		})
	}
}

// TestPodController_Reconcile tests the reconcile method
func TestPodController_Reconcile(t *testing.T) {
	// Initialize global pod mapper
	InitGlobalPodMapper()

	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name            string
		pod             *corev1.Pod
		podExists       bool
		expectError     bool
		expectInMapping bool
		setupFunc       func()
		validateFunc    func(t *testing.T)
	}{
		{
			name: "reconcile pod with all annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						AnnotationVirtualNodeName:     "vnode-1",
						AnnotationVirtualPodName:      "virtual-pod-1",
						AnnotationVirtualPodNamespace: "virtual-ns",
					},
				},
			},
			podExists:       true,
			expectError:     false,
			expectInMapping: true,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T) {
				info, exists := GetVirtualPodInfo("test-pod")
				assert.True(t, exists)
				assert.Equal(t, "vnode-1", info.VirtualNodeName)
				assert.Equal(t, "virtual-pod-1", info.VirtualPodName)
				assert.Equal(t, "virtual-ns", info.VirtualPodNamespace)
			},
		},
		{
			name: "reconcile pod without managed-by label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-no-label",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationVirtualNodeName:     "vnode-1",
						AnnotationVirtualPodName:      "virtual-pod-1",
						AnnotationVirtualPodNamespace: "virtual-ns",
					},
				},
			},
			podExists:       true,
			expectError:     false,
			expectInMapping: false,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T) {
				_, exists := GetVirtualPodInfo("test-pod-no-label")
				assert.False(t, exists)
			},
		},
		{
			name: "reconcile pod missing annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-no-annotations",
					Namespace: "default",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
			},
			podExists:       true,
			expectError:     false,
			expectInMapping: false,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T) {
				_, exists := GetVirtualPodInfo("test-pod-no-annotations")
				assert.False(t, exists)
			},
		},
		{
			name: "reconcile pod missing virtual node annotation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-missing-vnode",
					Namespace: "default",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						AnnotationVirtualPodName:      "virtual-pod-1",
						AnnotationVirtualPodNamespace: "virtual-ns",
					},
				},
			},
			podExists:       true,
			expectError:     false,
			expectInMapping: false,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T) {
				_, exists := GetVirtualPodInfo("test-pod-missing-vnode")
				assert.False(t, exists)
			},
		},
		{
			name: "reconcile deleted pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-deleted",
					Namespace: "default",
				},
			},
			podExists:       false,
			expectError:     false,
			expectInMapping: false,
			setupFunc: func() {
				InitGlobalPodMapper()
				// Pre-add pod to mapping
				SetVirtualPodInfo("test-pod-deleted", &VirtualPodInfo{
					VirtualNodeName:     "vnode-1",
					VirtualPodName:      "virtual-pod-1",
					VirtualPodNamespace: "virtual-ns",
				})
			},
			validateFunc: func(t *testing.T) {
				_, exists := GetVirtualPodInfo("test-pod-deleted")
				assert.False(t, exists)
			},
		},
		{
			name: "reconcile pod with empty annotation values",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-empty-annotations",
					Namespace: "default",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						AnnotationVirtualNodeName:     "",
						AnnotationVirtualPodName:      "virtual-pod-1",
						AnnotationVirtualPodNamespace: "virtual-ns",
					},
				},
			},
			podExists:       true,
			expectError:     false,
			expectInMapping: false,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T) {
				_, exists := GetVirtualPodInfo("test-pod-empty-annotations")
				assert.False(t, exists)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			if tt.setupFunc != nil {
				tt.setupFunc()
			}

			// Create fake client with or without pod
			clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.podExists {
				clientBuilder = clientBuilder.WithObjects(tt.pod)
			}
			fakeClient := clientBuilder.Build()

			// Create controller
			controller := &PodController{
				Client: fakeClient,
				Scheme: scheme,
				Log:    logr.Discard(),
			}

			// Execute reconcile
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.pod.Name,
					Namespace: tt.pod.Namespace,
				},
			}

			result, err := controller.Reconcile(context.Background(), req)

			// Validate
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, ctrl.Result{}, result)
			}

			if tt.validateFunc != nil {
				tt.validateFunc(t)
			}
		})
	}
}

// TestPodController_InitializeExistingPods tests initialization of existing pods
func TestPodController_InitializeExistingPods(t *testing.T) {
	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name          string
		pods          []*corev1.Pod
		expectedCount int
		setupFunc     func()
		validateFunc  func(t *testing.T, pods []*corev1.Pod)
	}{
		{
			name: "initialize with multiple valid pods",
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: "default",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						},
						Annotations: map[string]string{
							AnnotationVirtualNodeName:     "vnode-1",
							AnnotationVirtualPodName:      "virtual-pod-1",
							AnnotationVirtualPodNamespace: "virtual-ns-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-2",
						Namespace: "default",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						},
						Annotations: map[string]string{
							AnnotationVirtualNodeName:     "vnode-2",
							AnnotationVirtualPodName:      "virtual-pod-2",
							AnnotationVirtualPodNamespace: "virtual-ns-2",
						},
					},
				},
			},
			expectedCount: 2,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T, pods []*corev1.Pod) {
				info1, exists := GetVirtualPodInfo("pod-1")
				assert.True(t, exists)
				assert.Equal(t, "vnode-1", info1.VirtualNodeName)

				info2, exists := GetVirtualPodInfo("pod-2")
				assert.True(t, exists)
				assert.Equal(t, "vnode-2", info2.VirtualNodeName)
			},
		},
		{
			name: "initialize with mixed valid and invalid pods",
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod",
						Namespace: "default",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						},
						Annotations: map[string]string{
							AnnotationVirtualNodeName:     "vnode-1",
							AnnotationVirtualPodName:      "virtual-pod-1",
							AnnotationVirtualPodNamespace: "virtual-ns-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "invalid-pod",
						Namespace: "default",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						},
						// Missing annotations
					},
				},
			},
			expectedCount: 1,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T, pods []*corev1.Pod) {
				info, exists := GetVirtualPodInfo("valid-pod")
				assert.True(t, exists)
				assert.Equal(t, "vnode-1", info.VirtualNodeName)

				_, exists = GetVirtualPodInfo("invalid-pod")
				assert.False(t, exists)
			},
		},
		{
			name:          "initialize with no pods",
			pods:          []*corev1.Pod{},
			expectedCount: 0,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T, pods []*corev1.Pod) {
				assert.Equal(t, 0, GetPodMappingCount())
			},
		},
		{
			name: "initialize with pods without managed-by label",
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unlabeled-pod",
						Namespace: "default",
						Annotations: map[string]string{
							AnnotationVirtualNodeName:     "vnode-1",
							AnnotationVirtualPodName:      "virtual-pod-1",
							AnnotationVirtualPodNamespace: "virtual-ns-1",
						},
					},
				},
			},
			expectedCount: 0,
			setupFunc: func() {
				InitGlobalPodMapper()
			},
			validateFunc: func(t *testing.T, pods []*corev1.Pod) {
				_, exists := GetVirtualPodInfo("unlabeled-pod")
				assert.False(t, exists)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			if tt.setupFunc != nil {
				tt.setupFunc()
			}

			// Create fake client with pods
			objects := make([]client.Object, len(tt.pods))
			for i, pod := range tt.pods {
				objects[i] = pod
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			// Create controller
			controller := &PodController{
				Client: fakeClient,
				Scheme: scheme,
				Log:    logr.Discard(),
			}

			// Execute initialization
			err := controller.InitializeExistingPods(context.Background())

			// Validate
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedCount, GetPodMappingCount())

			if tt.validateFunc != nil {
				tt.validateFunc(t, tt.pods)
			}
		})
	}
}

// TestPodController_hasManagedByLabel tests the hasManagedByLabel method
func TestPodController_hasManagedByLabel(t *testing.T) {
	controller := &PodController{
		Log: logr.Discard(),
	}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod with correct label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
			},
			expected: true,
		},
		{
			name: "pod with incorrect label value",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: "other-value",
					},
				},
			},
			expected: false,
		},
		{
			name: "pod with no labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: false,
		},
		{
			name: "pod with nil labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: nil,
				},
			},
			expected: false,
		},
		{
			name: "pod with other labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "test",
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.hasManagedByLabel(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestPodController_extractVirtualPodInfo tests the extractVirtualPodInfo method
func TestPodController_extractVirtualPodInfo(t *testing.T) {
	controller := &PodController{
		Log: logr.Discard(),
	}

	tests := []struct {
		name        string
		pod         *corev1.Pod
		expectError bool
		expected    *VirtualPodInfo
	}{
		{
			name: "all annotations present",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualNodeName:     "vnode-1",
						AnnotationVirtualPodName:      "virtual-pod-1",
						AnnotationVirtualPodNamespace: "virtual-ns",
					},
				},
			},
			expectError: false,
			expected: &VirtualPodInfo{
				VirtualNodeName:     "vnode-1",
				VirtualPodName:      "virtual-pod-1",
				VirtualPodNamespace: "virtual-ns",
			},
		},
		{
			name: "missing virtual node name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualPodName:      "virtual-pod-1",
						AnnotationVirtualPodNamespace: "virtual-ns",
					},
				},
			},
			expectError: true,
			expected:    nil,
		},
		{
			name: "missing virtual pod name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualNodeName:     "vnode-1",
						AnnotationVirtualPodNamespace: "virtual-ns",
					},
				},
			},
			expectError: true,
			expected:    nil,
		},
		{
			name: "missing virtual pod namespace",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualNodeName: "vnode-1",
						AnnotationVirtualPodName:  "virtual-pod-1",
					},
				},
			},
			expectError: true,
			expected:    nil,
		},
		{
			name: "no annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expectError: true,
			expected:    nil,
		},
		{
			name: "nil annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: nil,
				},
			},
			expectError: true,
			expected:    nil,
		},
		{
			name: "empty annotation values",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualNodeName:     "",
						AnnotationVirtualPodName:      "virtual-pod-1",
						AnnotationVirtualPodNamespace: "virtual-ns",
					},
				},
			},
			expectError: true,
			expected:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := controller.extractVirtualPodInfo(tt.pod)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, tt.expected.VirtualNodeName, result.VirtualNodeName)
				assert.Equal(t, tt.expected.VirtualPodName, result.VirtualPodName)
				assert.Equal(t, tt.expected.VirtualPodNamespace, result.VirtualPodNamespace)
			}
		})
	}
}

// TestPodController_logCurrentMappingCount tests the logCurrentMappingCount method
func TestPodController_logCurrentMappingCount(t *testing.T) {
	InitGlobalPodMapper()

	controller := &PodController{
		Log: logr.Discard(),
	}

	// Add some pods to the mapping
	SetVirtualPodInfo("pod-1", &VirtualPodInfo{VirtualNodeName: "vnode-1"})
	SetVirtualPodInfo("pod-2", &VirtualPodInfo{VirtualNodeName: "vnode-2"})

	// Should not panic
	assert.NotPanics(t, func() {
		controller.logCurrentMappingCount()
	})

	// Verify count is correct
	assert.Equal(t, 2, GetPodMappingCount())
}

// TestPodController_ReconcileWithUpdates tests reconciling with pod updates
func TestPodController_ReconcileWithUpdates(t *testing.T) {
	InitGlobalPodMapper()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Create initial pod
	initialPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "update-pod",
			Namespace: "default",
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
			Annotations: map[string]string{
				AnnotationVirtualNodeName:     "vnode-1",
				AnnotationVirtualPodName:      "virtual-pod-1",
				AnnotationVirtualPodNamespace: "virtual-ns-1",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(initialPod).
		Build()

	controller := &PodController{
		Client: fakeClient,
		Scheme: scheme,
		Log:    logr.Discard(),
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "update-pod",
			Namespace: "default",
		},
	}

	// First reconcile
	_, err := controller.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	info, exists := GetVirtualPodInfo("update-pod")
	assert.True(t, exists)
	assert.Equal(t, "vnode-1", info.VirtualNodeName)

	// Update pod annotations
	updatedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "update-pod",
			Namespace: "default",
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
			Annotations: map[string]string{
				AnnotationVirtualNodeName:     "vnode-2",
				AnnotationVirtualPodName:      "virtual-pod-2",
				AnnotationVirtualPodNamespace: "virtual-ns-2",
			},
		},
	}

	err = fakeClient.Update(context.Background(), updatedPod)
	assert.NoError(t, err)

	// Second reconcile
	_, err = controller.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	info, exists = GetVirtualPodInfo("update-pod")
	assert.True(t, exists)
	assert.Equal(t, "vnode-2", info.VirtualNodeName)
	assert.Equal(t, "virtual-pod-2", info.VirtualPodName)
}

// TestPodController_ConcurrentReconcile tests concurrent reconciliation
func TestPodController_ConcurrentReconcile(t *testing.T) {
	InitGlobalPodMapper()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Create multiple pods
	pods := make([]client.Object, 10)
	for i := 0; i < 10; i++ {
		pods[i] = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "concurrent-pod-" + string(rune('a'+i)),
				Namespace: "default",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				},
				Annotations: map[string]string{
					AnnotationVirtualNodeName:     "vnode-1",
					AnnotationVirtualPodName:      "virtual-pod-" + string(rune('a'+i)),
					AnnotationVirtualPodNamespace: "virtual-ns",
				},
			},
		}
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pods...).
		Build()

	controller := &PodController{
		Client: fakeClient,
		Scheme: scheme,
		Log:    logr.Discard(),
	}

	// Reconcile all pods concurrently
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(index int) {
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      "concurrent-pod-" + string(rune('a'+index)),
					Namespace: "default",
				},
			}
			_, err := controller.Reconcile(context.Background(), req)
			assert.NoError(t, err)
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all pods are in mapping
	assert.Equal(t, 10, GetPodMappingCount())
}
