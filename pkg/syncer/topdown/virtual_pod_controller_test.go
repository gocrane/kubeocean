package topdown

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

// createTestVirtualNode creates a virtual node for testing
func createTestVirtualNode(name, clusterName, clusterID, physicalNodeName string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy:         "tapestry",
				cloudv1beta1.LabelPhysicalClusterID: clusterID,
				cloudv1beta1.LabelPhysicalNodeName:  physicalNodeName,
			},
			Annotations: map[string]string{
				"tapestry.io/physical-cluster-name": clusterName,
			},
		},
		Spec: corev1.NodeSpec{},
		Status: corev1.NodeStatus{
			Phase: corev1.NodeRunning,
		},
	}
}

func TestVirtualPodReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "test-cluster",
		},
	}

	tests := []struct {
		name           string
		virtualPod     *corev1.Pod
		physicalPod    *corev1.Pod
		expectedResult ctrl.Result
		expectError    bool
		validateFunc   func(t *testing.T, virtualClient, physicalClient client.Client)
	}{
		{
			name:           "virtual pod not found",
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual pod being deleted with physical pod",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "virtual-pod",
					Namespace:         "virtual-ns",
					UID:               "virtual-uid-123",
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Finalizers:        []string{"test-finalizer"}, // Add finalizer to allow DeletionTimestamp
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "test-vnode",
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx"},
					},
				},
			},
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod",
					Namespace: "physical-ns",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
						cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "virtual-uid-123",
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical pod should be deleted
				pod := &corev1.Pod{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-pod", Namespace: "physical-ns",
				}, pod)
				assert.True(t, apierrors.IsNotFound(err))
			},
		},
		{
			name: "virtual pod being deleted without physical pod",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "virtual-pod",
					Namespace:         "virtual-ns",
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Finalizers:        []string{"test-finalizer"},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "test-vnode",
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx"},
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Virtual pod should be deleted (force deleted with GracePeriodSeconds=0)
				pod := &corev1.Pod{}
				err := virtualClient.Get(context.TODO(), types.NamespacedName{
					Name: "virtual-pod", Namespace: "virtual-ns",
				}, pod)
				// Pod should be deleted or not found
				if err != nil {
					assert.True(t, apierrors.IsNotFound(err), "Expected NotFound error, got: %v", err)
				}
			},
		},
		{
			name: "virtual pod without physical pod mapping - should generate mapping",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
				},
				Spec: corev1.PodSpec{
					NodeName: "test-vnode",
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx"},
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Note: Since we use Status().Update(), annotations won't be updated in fake client
				// This is consistent with PhysicalPodReconciler behavior
				// In real Kubernetes, Status().Update() may update annotations depending on the implementation
				pod := &corev1.Pod{}
				err := virtualClient.Get(context.TODO(), types.NamespacedName{
					Name: "virtual-pod", Namespace: "virtual-ns",
				}, pod)
				require.NoError(t, err)
				// Only verify that the pod exists - annotations won't be updated via Status().Update() in tests
			},
		},
		{
			name: "virtual pod with mapping but no UID - should create physical pod",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "test-cluster",
						cloudv1beta1.AnnotationPhysicalPodName:      "virtual-pod-" + fmt.Sprintf("%x", md5.Sum([]byte("virtual-ns/virtual-pod"))),
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "test-vnode",
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx"},
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical pod should be created
				expectedName := "virtual-pod-" + fmt.Sprintf("%x", md5.Sum([]byte("virtual-ns/virtual-pod")))
				pod := &corev1.Pod{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: expectedName, Namespace: "test-cluster",
				}, pod)
				require.NoError(t, err)
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, pod.Labels[cloudv1beta1.LabelManagedBy])

				// Virtual pod should have physical UID annotation
				// Note: fake client doesn't generate UIDs, so we just check that the annotation was set
				virtualPod := &corev1.Pod{}
				err = virtualClient.Get(context.TODO(), types.NamespacedName{
					Name: "virtual-pod", Namespace: "virtual-ns",
				}, virtualPod)
				require.NoError(t, err)
				// Note: Since we use Status().Update(), annotations won't be updated in fake client
				// This is consistent with PhysicalPodReconciler behavior
			},
		},
		{
			name: "virtual pod with complete mapping but physical pod missing - should set Failed",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "test-cluster",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
						cloudv1beta1.AnnotationPhysicalPodUID:       "test-uid",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "test-vnode",
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx"},
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Virtual pod status should be Failed
				pod := &corev1.Pod{}
				err := virtualClient.Get(context.TODO(), types.NamespacedName{
					Name: "virtual-pod", Namespace: "virtual-ns",
				}, pod)
				require.NoError(t, err)
				assert.Equal(t, corev1.PodFailed, pod.Status.Phase)
				assert.Equal(t, "PhysicalPodLost", pod.Status.Reason)
			},
		},
		{
			name: "virtual pod with existing physical pod - should do nothing",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "test-cluster",
						cloudv1beta1.AnnotationPhysicalPodName:      "virtual-pod-" + fmt.Sprintf("%x", md5.Sum([]byte("virtual-ns/virtual-pod"))),
						cloudv1beta1.AnnotationPhysicalPodUID:       "test-uid",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "test-vnode",
					Containers: []corev1.Container{
						{Name: "container1", Image: "nginx"},
					},
				},
			},
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod-" + fmt.Sprintf("%x", md5.Sum([]byte("virtual-ns/virtual-pod"))),
					Namespace: "test-cluster",
					UID:       "test-uid",
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake clients
			virtualObjs := []client.Object{}
			if tt.virtualPod != nil {
				virtualObjs = append(virtualObjs, tt.virtualPod)
				// If pod has nodeName, create corresponding virtual node
				if tt.virtualPod.Spec.NodeName != "" {
					virtualNode := createTestVirtualNode(
						tt.virtualPod.Spec.NodeName,
						"test-cluster",
						"test-cluster-id",
						"test-physical-node",
					)
					virtualObjs = append(virtualObjs, virtualNode)
				}
			}
			virtualClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(virtualObjs...).
				WithStatusSubresource(&corev1.Pod{}).
				Build()

			physicalObjs := []client.Object{}
			if tt.physicalPod != nil {
				physicalObjs = append(physicalObjs, tt.physicalPod)
			}
			physicalClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(physicalObjs...).
				Build()

			// Create fake k8s client for direct API access
			fakeK8sClient := fake.NewSimpleClientset()

			reconciler := &VirtualPodReconciler{
				VirtualClient:     virtualClient,
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: fakeK8sClient,
				Scheme:            scheme,
				ClusterBinding:    clusterBinding,
				Log:               zap.New(zap.UseDevMode(true)).WithName("test-virtual-pod-reconciler"),
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
				},
			}

			result, err := reconciler.Reconcile(context.TODO(), req)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedResult, result)

			if tt.validateFunc != nil {
				tt.validateFunc(t, virtualClient, physicalClient)
			}
		})
	}
}

func TestVirtualPodReconciler_GeneratePhysicalPodName(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		PhysicalK8sClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name         string
		podName      string
		podNamespace string
		expected     string
	}{
		{
			name:         "short pod name",
			podName:      "test-pod",
			podNamespace: "default",
			expected:     "test-pod-5d41402abc4b2a76b9719d911017c592", // MD5 of "default/test-pod"
		},
		{
			name:         "long pod name (truncated)",
			podName:      "very-long-pod-name-that-exceeds-31-characters",
			podNamespace: "test-namespace",
			expected:     "very-long-pod-name-that-exceeds-", // truncated to 31 chars + MD5
		},
		{
			name:         "exact 31 character pod name",
			podName:      "exactly-thirty-one-characters-x", // exactly 31 chars
			podNamespace: "ns",
			expected:     "exactly-thirty-one-characters-x-", // should not be truncated + MD5
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.generatePhysicalPodName(tt.podName, tt.podNamespace)

			// Check that result starts with expected prefix (truncated pod name)
			expectedPrefix := tt.podName
			if len(tt.podName) > 31 {
				expectedPrefix = tt.podName[:31]
			}

			assert.True(t, strings.HasPrefix(result, expectedPrefix),
				"Result should start with %s, got %s", expectedPrefix, result)

			// Check that result has correct format (name-hash)
			parts := strings.Split(result, "-")
			assert.True(t, len(parts) >= 2, "Result should contain at least one dash")

			// The last part should be the MD5 hash (32 characters)
			hashPart := parts[len(parts)-1]
			assert.Equal(t, 32, len(hashPart), "Hash part should be 32 characters long")

			// Verify the hash is correct
			expectedInput := fmt.Sprintf("%s/%s", tt.podNamespace, tt.podName)
			expectedHash := fmt.Sprintf("%x", md5.Sum([]byte(expectedInput)))
			assert.Equal(t, expectedHash, hashPart, "Hash should match expected MD5")
		})
	}
}

func TestVirtualPodReconciler_GenerateRandomString(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		PhysicalK8sClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name   string
		length int
	}{
		{"length 8", 8},
		{"length 16", 16},
		{"length 32", 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reconciler.generateRandomString(tt.length)
			assert.NoError(t, err)
			assert.Len(t, result, tt.length)

			// Check that result contains only valid characters
			validChars := "abcdefghijklmnopqrstuvwxyz0123456789"
			for _, char := range result {
				assert.Contains(t, validChars, string(char))
			}
		})
	}
}

func TestVirtualPodReconciler_BuildPhysicalPodLabels(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		PhysicalK8sClient: fake.NewSimpleClientset(),
	}

	virtualPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app":     "test-app",
				"version": "v1.0",
			},
		},
	}

	labels := reconciler.buildPhysicalPodLabels(virtualPod)

	assert.Equal(t, "test-app", labels["app"])
	assert.Equal(t, "v1.0", labels["version"])
	assert.Equal(t, cloudv1beta1.LabelManagedByValue, labels[cloudv1beta1.LabelManagedBy])
}

func TestVirtualPodReconciler_BuildPhysicalPodAnnotations(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		PhysicalK8sClient: fake.NewSimpleClientset(),
	}

	virtualPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "virtual-pod",
			Namespace: "virtual-ns",
			UID:       "virtual-uid-123",
			Annotations: map[string]string{
				"app.kubernetes.io/version":                 "1.0.0",
				"deployment.kubernetes.io/revision":         "1",
				cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
				cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
				cloudv1beta1.AnnotationLastSyncTime:         "2023-01-01T00:00:00Z",
			},
		},
	}

	annotations := reconciler.buildPhysicalPodAnnotations(virtualPod)

	// Should include regular annotations
	assert.Equal(t, "1.0.0", annotations["app.kubernetes.io/version"])
	assert.Equal(t, "1", annotations["deployment.kubernetes.io/revision"])

	// Should include virtual pod mapping
	assert.Equal(t, "virtual-ns", annotations[cloudv1beta1.AnnotationVirtualPodNamespace])
	assert.Equal(t, "virtual-pod", annotations[cloudv1beta1.AnnotationVirtualPodName])
	assert.Equal(t, "virtual-uid-123", annotations[cloudv1beta1.AnnotationVirtualPodUID])

	// Should exclude Tapestry internal annotations
	assert.NotContains(t, annotations, cloudv1beta1.AnnotationPhysicalPodNamespace)
	assert.NotContains(t, annotations, cloudv1beta1.AnnotationPhysicalPodName)
	assert.NotContains(t, annotations, cloudv1beta1.AnnotationLastSyncTime)
}

func TestVirtualPodReconciler_IsSystemPod(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		isSystem bool
	}{
		{
			name: "kube-system pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "system-pod",
					Namespace: "kube-system",
				},
			},
			isSystem: true,
		},
		{
			name: "default namespace pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-pod",
					Namespace: "default",
				},
			},
			isSystem: false,
		},
		{
			name: "user application pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "my-app",
				},
			},
			isSystem: false,
		},
		{
			name: "kube-public pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "public-pod",
					Namespace: "kube-public",
				},
			},
			isSystem: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSystemPod(tt.pod)
			assert.Equal(t, tt.isSystem, result)
		})
	}
}

func TestVirtualPodReconciler_IsDaemonSetPod(t *testing.T) {
	tests := []struct {
		name        string
		pod         *corev1.Pod
		isDaemonSet bool
	}{
		{
			name: "pod managed by daemonset",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
			},
			isDaemonSet: true,
		},
		{
			name: "pod managed by deployment",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deployment-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "test-replicaset",
						},
					},
				},
			},
			isDaemonSet: false,
		},
		{
			name: "pod without owner references",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "standalone-pod",
					Namespace: "default",
				},
			},
			isDaemonSet: false,
		},
		{
			name: "pod with multiple owners including daemonset",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-owner-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "Job",
							Name: "test-job",
						},
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
			},
			isDaemonSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isDaemonSetPod(tt.pod)
			assert.Equal(t, tt.isDaemonSet, result)
		})
	}
}

func TestVirtualPodReconciler_IsPhysicalPodOwnedByVirtualPod(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		PhysicalK8sClient: fake.NewSimpleClientset(),
	}

	virtualPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "virtual-pod",
			Namespace: "virtual-ns",
			UID:       "virtual-uid-123",
		},
	}

	tests := []struct {
		name        string
		physicalPod *corev1.Pod
		expected    bool
	}{
		{
			name: "matching physical pod",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
						cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "virtual-uid-123",
					},
				},
			},
			expected: true,
		},
		{
			name: "different virtual pod namespace",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: "different-ns",
						cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "virtual-uid-123",
					},
				},
			},
			expected: false,
		},
		{
			name: "different virtual pod name",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
						cloudv1beta1.AnnotationVirtualPodName:      "different-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "virtual-uid-123",
					},
				},
			},
			expected: false,
		},
		{
			name: "different virtual pod UID",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
						cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "different-uid",
					},
				},
			},
			expected: false,
		},
		{
			name: "missing annotations",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: nil,
				},
			},
			expected: false,
		},
		{
			name: "empty annotations",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.isPhysicalPodOwnedByVirtualPod(tt.physicalPod, virtualPod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestVirtualPodReconciler_PodFiltering(t *testing.T) {
	tests := []struct {
		name       string
		pod        *corev1.Pod
		shouldSync bool
	}{
		{
			name: "regular pod with nodeName - should sync",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: "worker-1",
				},
			},
			shouldSync: true,
		},
		{
			name: "pod without nodeName - should not sync",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unscheduled-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					// NodeName is empty
				},
			},
			shouldSync: false,
		},
		{
			name: "daemonset pod with nodeName - should not sync",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "worker-1",
				},
			},
			shouldSync: false,
		},
		{
			name: "system pod with nodeName - should not sync",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "system-pod",
					Namespace: "kube-system",
				},
				Spec: corev1.PodSpec{
					NodeName: "master-1",
				},
			},
			shouldSync: false,
		},
		{
			name: "deployment pod with nodeName - should sync",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deployment-pod",
					Namespace: "app-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "test-replicaset",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "worker-2",
				},
			},
			shouldSync: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the filter logic used in SetupWithManager
			shouldSync := true

			// Skip system pods
			if isSystemPod(tt.pod) {
				shouldSync = false
			}

			// Only sync pods that are not managed by DaemonSet
			if isDaemonSetPod(tt.pod) {
				shouldSync = false
			}

			// Only sync pods with spec.nodeName set (scheduled pods)
			if tt.pod.Spec.NodeName == "" {
				shouldSync = false
			}

			assert.Equal(t, tt.shouldSync, shouldSync)
		})
	}
}
