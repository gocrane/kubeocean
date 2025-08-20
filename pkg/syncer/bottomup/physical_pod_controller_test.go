package bottomup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestPhysicalPodReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	tests := []struct {
		name         string
		physicalPod  *corev1.Pod
		virtualPod   *corev1.Pod
		expectSync   bool
		expectError  bool
		expectDelete bool
	}{
		{
			name: "tapestry managed pod with valid annotations and virtual pod",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod-1",
					Namespace: "physical-ns",
					UID:       "physical-uid-123",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "virtual-pod-1",
						AnnotationVirtualPodUID:       "virtual-uid-456",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod-1",
					Namespace: "virtual-ns",
					UID:       "virtual-uid-456",
					Annotations: map[string]string{
						AnnotationPhysicalPodNamespace: "physical-ns",
						AnnotationPhysicalPodName:      "physical-pod-1",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending, // Different from physical pod
				},
			},
			expectSync:   true,
			expectError:  false,
			expectDelete: false,
		},
		{
			name: "non-tapestry managed pod",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-pod",
					Namespace: "regular-ns",
					// No Tapestry labels
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			virtualPod:   nil,
			expectSync:   false,
			expectError:  false,
			expectDelete: false,
		},
		{
			name: "tapestry managed pod missing required annotations",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod-2",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					// Missing required annotations
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			virtualPod:   nil,
			expectSync:   false,
			expectError:  false,
			expectDelete: true,
		},
		{
			name: "tapestry managed pod with non-existent virtual pod",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod-3",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "non-existent-pod",
						AnnotationVirtualPodUID:       "virtual-uid-789",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			virtualPod:   nil,
			expectSync:   false,
			expectError:  false,
			expectDelete: true,
		},
		{
			name: "tapestry managed pod with UID mismatch",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod-4",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "virtual-pod-4",
						AnnotationVirtualPodUID:       "wrong-uid",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod-4",
					Namespace: "virtual-ns",
					UID:       "correct-uid",
					Annotations: map[string]string{
						AnnotationPhysicalPodNamespace: "physical-ns",
						AnnotationPhysicalPodName:      "physical-pod-4",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			},
			expectSync:   false,
			expectError:  false,
			expectDelete: true,
		},
		{
			name: "tapestry managed pod with virtual pod pointing to different physical pod",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod-5",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "virtual-pod-5",
						AnnotationVirtualPodUID:       "virtual-uid-555",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod-5",
					Namespace: "virtual-ns",
					UID:       "virtual-uid-555",
					Annotations: map[string]string{
						AnnotationPhysicalPodNamespace: "other-ns",
						AnnotationPhysicalPodName:      "other-pod",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			},
			expectSync:   false,
			expectError:  false,
			expectDelete: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake clients
			physicalObjs := []client.Object{}
			if tt.physicalPod != nil {
				physicalObjs = append(physicalObjs, tt.physicalPod)
			}
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjs...).Build()

			virtualObjs := []client.Object{}
			if tt.virtualPod != nil {
				virtualObjs = append(virtualObjs, tt.virtualPod)
			}
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjs...).Build()

			// Create cluster binding
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			}

			// Create reconciler
			reconciler := &PhysicalPodReconciler{
				PhysicalClient: physicalClient,
				VirtualClient:  virtualClient,
				Scheme:         scheme,
				ClusterBinding: clusterBinding,
				Log:            ctrl.Log.WithName("test-physical-pod-reconciler"),
			}

			// Test reconcile
			podName := "physical-pod-1"
			if tt.physicalPod != nil {
				podName = tt.physicalPod.Name
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      podName,
					Namespace: "physical-ns",
				},
			}

			ctx := context.Background()
			result, err := reconciler.Reconcile(ctx, req)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Should not requeue for most cases
			assert.Equal(t, time.Duration(0), result.RequeueAfter)

			// Check if physical pod was deleted when expected
			if tt.expectDelete {
				// Try to get the physical pod - it should be deleted
				updatedPhysicalPod := &corev1.Pod{}
				err = physicalClient.Get(ctx, types.NamespacedName{
					Name:      tt.physicalPod.Name,
					Namespace: tt.physicalPod.Namespace,
				}, updatedPhysicalPod)
				assert.True(t, err != nil) // Should be deleted or not found
			}

			// If we expect sync and have a virtual pod, check if it was updated
			if tt.expectSync && tt.virtualPod != nil {
				updatedVirtualPod := &corev1.Pod{}
				err = virtualClient.Get(ctx, types.NamespacedName{
					Name:      tt.virtualPod.Name,
					Namespace: tt.virtualPod.Namespace,
				}, updatedVirtualPod)
				assert.NoError(t, err)

				// Check if sync timestamp annotation was added (this indicates sync happened)
				// Check if status was updated (since we only update status now)
				assert.Equal(t, corev1.PodRunning, updatedVirtualPod.Status.Phase)
			}
		})
	}
}

func TestPhysicalPodReconciler_IsTapestryManagedPod(t *testing.T) {
	reconciler := &PhysicalPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod with tapestry managed-by label",
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
			name: "pod without labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: false,
		},
		{
			name: "pod with wrong managed-by label value",
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
			name: "pod with other labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"other.label": "value",
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.isTapestryManagedPod(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalPodReconciler_HasRequiredAnnotations(t *testing.T) {
	reconciler := &PhysicalPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod with all required annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "virtual-pod",
						AnnotationVirtualPodUID:       "virtual-uid",
					},
				},
			},
			expected: true,
		},
		{
			name: "pod without annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: false,
		},
		{
			name: "pod with partial annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						// Missing other required annotations
					},
				},
			},
			expected: false,
		},
		{
			name: "pod with empty annotation values",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "",
						AnnotationVirtualPodName:      "virtual-pod",
						AnnotationVirtualPodUID:       "virtual-uid",
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.hasRequiredAnnotations(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestPhysicalPodReconciler_IsNetworkError test removed as isNetworkError method was removed

func TestPhysicalPodReconciler_ValidateVirtualPodAnnotations(t *testing.T) {
	reconciler := &PhysicalPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	physicalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "physical-pod",
			Namespace: "physical-ns",
		},
	}

	tests := []struct {
		name       string
		virtualPod *corev1.Pod
		expected   bool
	}{
		{
			name: "virtual pod with correct annotations",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationPhysicalPodNamespace: "physical-ns",
						AnnotationPhysicalPodName:      "physical-pod",
					},
				},
			},
			expected: true,
		},
		{
			name: "virtual pod without annotations",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: false,
		},
		{
			name: "virtual pod with wrong physical pod name",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationPhysicalPodNamespace: "physical-ns",
						AnnotationPhysicalPodName:      "wrong-pod",
					},
				},
			},
			expected: false,
		},
		{
			name: "virtual pod with wrong physical pod namespace",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationPhysicalPodNamespace: "wrong-ns",
						AnnotationPhysicalPodName:      "physical-pod",
					},
				},
			},
			expected: false,
		},
		{
			name: "virtual pod with missing annotations",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationPhysicalPodNamespace: "physical-ns",
						// Missing AnnotationPhysicalPodName
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.validateVirtualPodAnnotations(tt.virtualPod, physicalPod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalPodReconciler_BuildSyncPod(t *testing.T) {
	reconciler := &PhysicalPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	physicalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "physical-pod",
			Namespace: "physical-ns",
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				"app":                       "test-app",
				"version":                   "v1.0",
			},
			Annotations: map[string]string{
				AnnotationVirtualPodNamespace:       "virtual-ns",
				AnnotationVirtualPodName:            "virtual-pod",
				AnnotationVirtualPodUID:             "virtual-uid-123",
				"deployment.kubernetes.io/revision": "1",
				"app.kubernetes.io/version":         "1.0.0",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				},
			},
			PodIP:     "10.0.0.1",
			HostIP:    "192.168.1.100",
			Message:   "Pod is running",
			Reason:    "Started",
			StartTime: &metav1.Time{Time: time.Now()},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "container1",
					Ready: true,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{
							StartedAt: metav1.Time{Time: time.Now()},
						},
					},
				},
			},
		},
	}

	virtualPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "virtual-pod",
			Namespace: "virtual-ns",
			Labels: map[string]string{
				"old-label": "old-value",
			},
			Annotations: map[string]string{
				AnnotationPhysicalPodNamespace: "physical-ns",
				AnnotationPhysicalPodName:      "physical-pod",
				AnnotationLastSyncTime:         "2023-01-01T00:00:00Z",
				"old-annotation":               "old-value",
			},
		},
		Status: corev1.PodStatus{
			Phase:   corev1.PodPending,
			Message: "Old message",
		},
	}

	syncPod := reconciler.buildSyncPod(physicalPod, virtualPod)

	// Check labels - should have all physical pod labels (including managed-by)
	assert.Equal(t, "test-app", syncPod.Labels["app"])
	assert.Equal(t, "v1.0", syncPod.Labels["version"])
	assert.Equal(t, cloudv1beta1.LabelManagedByValue, syncPod.Labels[cloudv1beta1.LabelManagedBy])
	assert.NotContains(t, syncPod.Labels, "old-label")

	// Check annotations - should have physical pod annotations but not virtual ones
	assert.Equal(t, "1", syncPod.Annotations["deployment.kubernetes.io/revision"])
	assert.Equal(t, "1.0.0", syncPod.Annotations["app.kubernetes.io/version"])
	assert.NotContains(t, syncPod.Annotations, AnnotationVirtualPodNamespace)
	assert.NotContains(t, syncPod.Annotations, AnnotationVirtualPodName)
	assert.NotContains(t, syncPod.Annotations, AnnotationVirtualPodUID)
	assert.NotContains(t, syncPod.Annotations, "old-annotation")

	// Check preserved virtual pod annotations
	assert.Equal(t, "physical-ns", syncPod.Annotations[AnnotationPhysicalPodNamespace])
	assert.Equal(t, "physical-pod", syncPod.Annotations[AnnotationPhysicalPodName])
	assert.Contains(t, syncPod.Annotations, AnnotationLastSyncTime)

	// Check status - should match physical pod
	assert.Equal(t, corev1.PodRunning, syncPod.Status.Phase)
	assert.Equal(t, "Pod is running", syncPod.Status.Message)
	assert.Equal(t, "Started", syncPod.Status.Reason)
	assert.Equal(t, "10.0.0.1", syncPod.Status.PodIP)
	// HostIP should be set to PodIP according to the new logic
	assert.Equal(t, "10.0.0.1", syncPod.Status.HostIP)
	assert.Len(t, syncPod.Status.ContainerStatuses, 1)
	assert.Equal(t, "container1", syncPod.Status.ContainerStatuses[0].Name)
	assert.True(t, syncPod.Status.ContainerStatuses[0].Ready)
}

func TestPhysicalPodReconciler_IsPodsStatusEqual(t *testing.T) {
	reconciler := &PhysicalPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name     string
		pod1     *corev1.Pod
		pod2     *corev1.Pod
		expected bool
	}{
		{
			name: "identical status",
			pod1: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase:   corev1.PodRunning,
					Message: "Running",
					PodIP:   "10.0.0.1",
				},
			},
			pod2: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase:   corev1.PodRunning,
					Message: "Running",
					PodIP:   "10.0.0.1",
				},
			},
			expected: true,
		},
		{
			name: "different phase",
			pod1: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			pod2: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			expected: false,
		},
		{
			name: "different message",
			pod1: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase:   corev1.PodRunning,
					Message: "Running normally",
				},
			},
			pod2: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase:   corev1.PodRunning,
					Message: "Running with issues",
				},
			},
			expected: false,
		},
		{
			name: "different pod IP",
			pod1: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					PodIP: "10.0.0.1",
				},
			},
			pod2: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					PodIP: "10.0.0.2",
				},
			},
			expected: false,
		},
		{
			name: "different container statuses",
			pod1: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "container1", Ready: true},
					},
				},
			},
			pod2: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "container1", Ready: false},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.isPodsStatusEqual(tt.pod1, tt.pod2)
			assert.Equal(t, tt.expected, result)
		})
	}
}
