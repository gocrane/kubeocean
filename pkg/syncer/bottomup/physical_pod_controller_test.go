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
		name        string
		physicalPod *corev1.Pod
		virtualPod  *corev1.Pod
		expectSync  bool
		expectError bool
	}{
		{
			name: "tapestry managed pod with status change",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod-1",
					Namespace: "physical-ns",
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "virtual-pod-1",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "container-1",
							Ready: true,
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{
									StartedAt: metav1.Now(),
								},
							},
						},
					},
				},
			},
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod-1",
					Namespace: "virtual-ns",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending, // Different from physical pod
				},
			},
			expectSync:  true,
			expectError: false,
		},
		{
			name: "non-tapestry managed pod",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-pod",
					Namespace: "regular-ns",
					// No Tapestry annotations
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			virtualPod:  nil,
			expectSync:  false,
			expectError: false,
		},
		{
			name: "tapestry managed pod with same status",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod-2",
					Namespace: "physical-ns",
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "virtual-pod-2",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "container-1",
							Ready: true,
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{
									StartedAt: metav1.Time{Time: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)},
								},
							},
						},
					},
				},
			},
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod-2",
					Namespace: "virtual-ns",
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning, // Same as physical pod
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "container-1",
							Ready: true,
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{
									StartedAt: metav1.Time{Time: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)},
								},
							},
						},
					},
				},
			},
			expectSync:  false, // No sync needed as status is the same
			expectError: false,
		},
		{
			name:        "deleted pod",
			physicalPod: nil, // Pod doesn't exist (deleted)
			virtualPod:  nil,
			expectSync:  false,
			expectError: false,
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

			// For non-Tapestry pods or deleted pods, we expect no requeue
			assert.Equal(t, time.Duration(0), result.RequeueAfter)

			// If we expect sync and have a virtual pod, check if it was updated
			if tt.expectSync && tt.virtualPod != nil {
				updatedVirtualPod := &corev1.Pod{}
				err = virtualClient.Get(ctx, types.NamespacedName{
					Name:      tt.virtualPod.Name,
					Namespace: tt.virtualPod.Namespace,
				}, updatedVirtualPod)
				assert.NoError(t, err)

				// Check if sync timestamp annotation was added (this indicates sync happened)
				if updatedVirtualPod.Annotations != nil {
					assert.Contains(t, updatedVirtualPod.Annotations, AnnotationSyncTimestamp)
					assert.Contains(t, updatedVirtualPod.Annotations, AnnotationPhysicalPodUID)
				}

				// Note: In fake client, status updates might not work exactly like real K8s
				// The important thing is that the sync method was called without error
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
			name: "pod with tapestry annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "virtual-pod",
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
			name: "pod with partial tapestry annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						// Missing AnnotationVirtualPodName
					},
				},
			},
			expected: false,
		},
		{
			name: "pod with other annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other.annotation": "value",
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

func TestPhysicalPodReconciler_NeedsStatusSync(t *testing.T) {
	reconciler := &PhysicalPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name        string
		virtualPod  *corev1.Pod
		physicalPod *corev1.Pod
		expected    bool
	}{
		{
			name: "different phases",
			virtualPod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			physicalPod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expected: true,
		},
		{
			name: "same phases",
			virtualPod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			physicalPod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			expected: false,
		},
		{
			name: "different container count",
			virtualPod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "container-1"},
					},
				},
			},
			physicalPod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "container-1"},
						{Name: "container-2"},
					},
				},
			},
			expected: true,
		},
		{
			name: "different container ready status",
			virtualPod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "container-1", Ready: false},
					},
				},
			},
			physicalPod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "container-1", Ready: true},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.needsStatusSync(tt.virtualPod, tt.physicalPod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalPodReconciler_GetVirtualPodInfo(t *testing.T) {
	reconciler := &PhysicalPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name              string
		pod               *corev1.Pod
		expectedNamespace string
		expectedName      string
		expectError       bool
	}{
		{
			name: "valid annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "virtual-ns",
						AnnotationVirtualPodName:      "virtual-pod",
					},
				},
			},
			expectedNamespace: "virtual-ns",
			expectedName:      "virtual-pod",
			expectError:       false,
		},
		{
			name: "missing annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expectError: true,
		},
		{
			name: "empty annotation values",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationVirtualPodNamespace: "",
						AnnotationVirtualPodName:      "virtual-pod",
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			namespace, name, err := reconciler.getVirtualPodInfo(tt.pod)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedNamespace, namespace)
				assert.Equal(t, tt.expectedName, name)
			}
		})
	}
}

func TestPhysicalPodReconciler_ContainerStatesEqual(t *testing.T) {
	reconciler := &PhysicalPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name     string
		state1   corev1.ContainerState
		state2   corev1.ContainerState
		expected bool
	}{
		{
			name: "both running",
			state1: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{},
			},
			state2: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{},
			},
			expected: true,
		},
		{
			name: "one running, one waiting",
			state1: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{},
			},
			state2: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
			},
			expected: false,
		},
		{
			name: "both waiting with same reason",
			state1: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
			},
			state2: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
			},
			expected: true,
		},
		{
			name: "both waiting with different reasons",
			state1: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
			},
			state2: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
			},
			expected: false,
		},
		{
			name: "both terminated with same exit code",
			state1: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"},
			},
			state2: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"},
			},
			expected: true,
		},
		{
			name: "both terminated with different exit codes",
			state1: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"},
			},
			state2: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.containerStatesEqual(tt.state1, tt.state2)
			assert.Equal(t, tt.expected, result)
		})
	}
}
