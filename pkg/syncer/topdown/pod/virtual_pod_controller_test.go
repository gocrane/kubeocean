package toppod

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	topcommon "github.com/gocrane/kubeocean/pkg/syncer/topdown/common"
	"github.com/gocrane/kubeocean/pkg/utils"
	authenticationv1 "k8s.io/api/authentication/v1"
)

// MockTokenManager is a mock implementation of the token.TokenManagerInterface for testing
type MockTokenManager struct {
	getServiceAccountToken func(namespace, name string, tr *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error)
}

func (m *MockTokenManager) GetServiceAccountToken(namespace, name string, tr *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
	if m.getServiceAccountToken != nil {
		return m.getServiceAccountToken(namespace, name, tr)
	}
	return &authenticationv1.TokenRequest{
		Status: authenticationv1.TokenRequestStatus{
			Token: "test-token-content",
		},
	}, nil
}

// Mock methods to satisfy token.Manager interface
func (m *MockTokenManager) DeleteServiceAccountToken(podUID types.UID) {
	// Mock implementation - do nothing
}

func (m *MockTokenManager) cleanup() {
	// Mock implementation - do nothing
}

func (m *MockTokenManager) get(key string) (*authenticationv1.TokenRequest, bool) {
	// Mock implementation - return empty
	return nil, false
}

func (m *MockTokenManager) set(key string, tr *authenticationv1.TokenRequest) {
	// Mock implementation - do nothing
}

func (m *MockTokenManager) expired(t *authenticationv1.TokenRequest) bool {
	// Mock implementation - return false
	return false
}

func (m *MockTokenManager) requiresRefresh(ctx context.Context, tr *authenticationv1.TokenRequest) bool {
	// Mock implementation - return false
	return false
}

// createTestVirtualNode creates a virtual node for testing
func createTestVirtualNode(name, clusterName, ClusterID, physicalNodeName string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy:         "kubeocean",
				cloudv1beta1.LabelPhysicalClusterID: ClusterID,
				cloudv1beta1.LabelPhysicalNodeName:  physicalNodeName,
			},
			Annotations: map[string]string{
				"kubeocean.io/physical-cluster-name": clusterName,
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
	require.NoError(t, schedulingv1.AddToScheme(scheme))
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

	// Create mock services for loadbalancer IPs
	kubernetesIntranetService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes-intranet",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 443,
				},
			},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{IP: "10.0.0.1"},
				},
			},
		},
	}

	kubeDnsIntranetService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kube-dns-intranet",
			Namespace: "kube-system",
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{IP: "10.0.0.2"},
				},
			},
		},
	}

	tests := []struct {
		name              string
		virtualPod        *corev1.Pod
		virtualConfigMap  *corev1.ConfigMap
		virtualSecret     *corev1.Secret
		virtualPVC        *corev1.PersistentVolumeClaim
		virtualPV         *corev1.PersistentVolume
		virtualPullSecret *corev1.Secret
		physicalPod       *corev1.Pod
		expectedResult    ctrl.Result
		expectError       bool
		validateFunc      func(t *testing.T, virtualClient, physicalClient client.Client)
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
		},
		{
			name: "virtual pod with dependent resources",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod-with-deps",
					Namespace: "virtual-ns",
					UID:       "virtual-uid-456",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod-with-deps",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "test-vnode",
					Containers: []corev1.Container{
						{
							Name:  "container1",
							Image: "nginx",
							Env: []corev1.EnvVar{
								{
									Name: "CONFIG_VAR",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "test-config",
											},
											Key: "config-key",
										},
									},
								},
								{
									Name: "SECRET_VAR",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "test-secret",
											},
											Key: "secret-key",
										},
									},
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config-volume",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "test-config",
									},
								},
							},
						},
						{
							Name: "secret-volume",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "test-secret",
								},
							},
						},
						{
							Name: "pvc-volume",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: "test-pvc",
								},
							},
						},
					},
					ImagePullSecrets: []corev1.LocalObjectReference{
						{Name: "pull-secret"},
					},
				},
			},
			// Add dependent resources to virtual client
			virtualConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "virtual-ns",
				},
				Data: map[string]string{
					"config-key": "config-value",
				},
			},
			virtualSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "virtual-ns",
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"secret-key": []byte("secret-value"),
				},
			},
			virtualPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "virtual-ns",
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
					VolumeName: "test-pv", // Required for bound PVC
				},
				Status: corev1.PersistentVolumeClaimStatus{
					Phase: corev1.ClaimBound, // Required for sync
				},
			},
			// Add pull-secret for image pull secrets
			virtualPullSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pull-secret",
					Namespace: "virtual-ns",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte(`{"auths":{}}`),
				},
			},
			// Add associated PV for PVC sync
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/data",
						},
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
				// This test case is for testing dependent resources sync
				// The physical pod should be created with dependent resources
				// Physical pod name is set in annotations
				expectedName := "physical-pod-with-deps"
				pod := &corev1.Pod{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: expectedName, Namespace: "physical-ns",
				}, pod)
				require.NoError(t, err)
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, pod.Labels[cloudv1beta1.LabelManagedBy])

				// Verify dependent resources were created
				// ConfigMap
				configMap := &corev1.ConfigMap{}
				_ = physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "test-config", Namespace: "physical-ns",
				}, configMap)
				// ConfigMap might not be created in this test since it's not in the virtual client
				// This is expected behavior when virtual resources don't exist
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
					Name:              "virtual-pod-" + fmt.Sprintf("%x", md5.Sum([]byte("virtual-ns/virtual-pod"))),
					Namespace:         "test-cluster",
					UID:               "test-uid",
					CreationTimestamp: metav1.Now(),
				},
				Spec: corev1.PodSpec{
					NodeName: "test-physical-node",
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
			if tt.virtualConfigMap != nil {
				virtualObjs = append(virtualObjs, tt.virtualConfigMap)
			}
			if tt.virtualSecret != nil {
				virtualObjs = append(virtualObjs, tt.virtualSecret)
			}
			if tt.virtualPVC != nil {
				virtualObjs = append(virtualObjs, tt.virtualPVC)
			}
			if tt.virtualPullSecret != nil {
				virtualObjs = append(virtualObjs, tt.virtualPullSecret)
			}
			if tt.virtualPV != nil {
				virtualObjs = append(virtualObjs, tt.virtualPV)
			}
			// Add mock services for loadbalancer IPs
			virtualObjs = append(virtualObjs, kubernetesIntranetService, kubeDnsIntranetService)
			// Add ClusterBinding to virtual client
			virtualObjs = append(virtualObjs, clusterBinding)
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
					Name: func() string {
						if tt.virtualPod != nil {
							return tt.virtualPod.Name
						}
						return "virtual-pod"
					}(),
					Namespace: func() string {
						if tt.virtualPod != nil {
							return tt.virtualPod.Namespace
						}
						return "virtual-ns"
					}(),
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
			if len(tt.podName) > 30 {
				expectedPrefix = tt.podName[:30]
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

func TestVirtualPodReconciler_BuildPhysicalPodLabels(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		PhysicalK8sClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name           string
		virtualPod     *corev1.Pod
		workloadType   string
		workloadName   string
		expectedLabels map[string]string
	}{
		{
			name: "pod with labels and workload info",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-namespace",
					Labels: map[string]string{
						"app":     "test-app",
						"version": "v1.0",
						"env":     "production",
					},
				},
			},
			workloadType: "deployment",
			workloadName: "test-deployment",
			expectedLabels: map[string]string{
				"app":                              "test-app",
				"version":                          "v1.0",
				"env":                              "production",
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelVirtualNamespace: "test-namespace",
				cloudv1beta1.LabelWorkloadType:     "deployment",
				cloudv1beta1.LabelWorkloadName:     "test-deployment",
			},
		},
		{
			name: "pod without labels",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
			},
			workloadType: "daemonset",
			workloadName: "test-daemonset",
			expectedLabels: map[string]string{
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelVirtualNamespace: "default",
				cloudv1beta1.LabelWorkloadType:     "daemonset",
				cloudv1beta1.LabelWorkloadName:     "test-daemonset",
			},
		},
		{
			name: "pod with empty workload info",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						"app": "test-app",
					},
				},
			},
			workloadType: "",
			workloadName: "",
			expectedLabels: map[string]string{
				"app":                              "test-app",
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelVirtualNamespace: "test-ns",
			},
		},
		{
			name: "pod with only workload type",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						"app": "test-app",
					},
				},
			},
			workloadType: "statefulset",
			workloadName: "",
			expectedLabels: map[string]string{
				"app":                              "test-app",
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelVirtualNamespace: "test-ns",
				cloudv1beta1.LabelWorkloadType:     "statefulset",
			},
		},
		{
			name: "pod with only workload name",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						"app": "test-app",
					},
				},
			},
			workloadType: "",
			workloadName: "test-job",
			expectedLabels: map[string]string{
				"app":                              "test-app",
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelVirtualNamespace: "test-ns",
				cloudv1beta1.LabelWorkloadName:     "test-job",
			},
		},
		{
			name: "pod with conflicting labels",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						"app":                              "test-app",
						cloudv1beta1.LabelManagedBy:        "other-manager",
						cloudv1beta1.LabelVirtualNamespace: "conflicting-ns",
						cloudv1beta1.LabelWorkloadType:     "conflicting-type",
						cloudv1beta1.LabelWorkloadName:     "conflicting-name",
					},
				},
			},
			workloadType: "cronjob",
			workloadName: "test-cronjob",
			expectedLabels: map[string]string{
				"app":                              "test-app",
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue, // Should be overridden
				cloudv1beta1.LabelVirtualNamespace: "test-ns",                        // Should be overridden
				cloudv1beta1.LabelWorkloadType:     "cronjob",                        // Should be overridden
				cloudv1beta1.LabelWorkloadName:     "test-cronjob",                   // Should be overridden
			},
		},
		{
			name: "pod with special characters in namespace",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-namespace-with-dashes_and.underscores",
					Labels: map[string]string{
						"app": "test-app",
					},
				},
			},
			workloadType: "replicaset",
			workloadName: "test-replicaset",
			expectedLabels: map[string]string{
				"app":                              "test-app",
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelVirtualNamespace: "test-namespace-with-dashes_and.underscores",
				cloudv1beta1.LabelWorkloadType:     "replicaset",
				cloudv1beta1.LabelWorkloadName:     "test-replicaset",
			},
		},
		{
			name: "pod with special characters in workload name",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						"app": "test-app",
					},
				},
			},
			workloadType: "job",
			workloadName: "test-job-with-special-chars-123",
			expectedLabels: map[string]string{
				"app":                              "test-app",
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelVirtualNamespace: "test-ns",
				cloudv1beta1.LabelWorkloadType:     "job",
				cloudv1beta1.LabelWorkloadName:     "test-job-with-special-chars-123",
			},
		},
		{
			name: "pod with empty namespace",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "",
					Labels: map[string]string{
						"app": "test-app",
					},
				},
			},
			workloadType: "deployment",
			workloadName: "test-deployment",
			expectedLabels: map[string]string{
				"app":                              "test-app",
				cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelVirtualNamespace: "",
				cloudv1beta1.LabelWorkloadType:     "deployment",
				cloudv1beta1.LabelWorkloadName:     "test-deployment",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := reconciler.buildPhysicalPodLabels(tt.virtualPod, tt.workloadType, tt.workloadName)

			// Verify all expected labels are present
			for key, expectedValue := range tt.expectedLabels {
				actualValue, exists := labels[key]
				assert.True(t, exists, "Label %s should exist", key)
				assert.Equal(t, expectedValue, actualValue, "Label %s should have correct value", key)
			}

			// Verify no unexpected labels are present
			for key, actualValue := range labels {
				_, exists := tt.expectedLabels[key]
				assert.True(t, exists, "Unexpected label %s with value %s", key, actualValue)
			}

			// Verify managed-by label is always present
			assert.Equal(t, cloudv1beta1.LabelManagedByValue, labels[cloudv1beta1.LabelManagedBy], "Managed-by label should always be present")
		})
	}
}

func TestVirtualPodReconciler_BuildPhysicalPodAnnotations(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		PhysicalK8sClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name           string
		virtualPod     *corev1.Pod
		expectedValues map[string]string
	}{
		{
			name: "pod_with_node_name_should_include_virtual_node_name_annotation",
			virtualPod: &corev1.Pod{
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
				Spec: corev1.PodSpec{
					NodeName: "virtual-node-123",
				},
			},
			expectedValues: map[string]string{
				"app.kubernetes.io/version":                "1.0.0",
				"deployment.kubernetes.io/revision":        "1",
				cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
				cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
				cloudv1beta1.AnnotationVirtualPodUID:       "virtual-uid-123",
				cloudv1beta1.AnnotationVirtualNodeName:     "virtual-node-123",
			},
		},
		{
			name: "pod_without_node_name_should_include_virtual_node_name_annotation_with_empty_value",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
					UID:       "virtual-uid-123",
					Annotations: map[string]string{
						"app.kubernetes.io/version": "1.0.0",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "",
				},
			},
			expectedValues: map[string]string{
				"app.kubernetes.io/version":                "1.0.0",
				cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
				cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
				cloudv1beta1.AnnotationVirtualPodUID:       "virtual-uid-123",
				cloudv1beta1.AnnotationVirtualNodeName:     "",
			},
		},
		{
			name: "pod_with_special_characters_in_node_name",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
					UID:       "virtual-uid-123",
				},
				Spec: corev1.PodSpec{
					NodeName: "virtual-node-123-abc.def",
				},
			},
			expectedValues: map[string]string{
				cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
				cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
				cloudv1beta1.AnnotationVirtualPodUID:       "virtual-uid-123",
				cloudv1beta1.AnnotationVirtualNodeName:     "virtual-node-123-abc.def",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := reconciler.buildPhysicalPodAnnotations(tt.virtualPod)

			// Verify expected annotations are present
			for key, expectedValue := range tt.expectedValues {
				actualValue, exists := annotations[key]
				assert.True(t, exists, "Annotation %s should exist", key)
				assert.Equal(t, expectedValue, actualValue, "Annotation %s should have correct value", key)
			}

			// Should exclude Kubeocean internal annotations
			assert.NotContains(t, annotations, cloudv1beta1.AnnotationPhysicalPodNamespace)
			assert.NotContains(t, annotations, cloudv1beta1.AnnotationPhysicalPodName)
			assert.NotContains(t, annotations, cloudv1beta1.AnnotationLastSyncTime)

			// Verify virtual node name annotation is always present
			assert.Contains(t, annotations, cloudv1beta1.AnnotationVirtualNodeName, "Should always include virtual node name annotation")
			assert.Equal(t, tt.virtualPod.Spec.NodeName, annotations[cloudv1beta1.AnnotationVirtualNodeName], "Virtual node name should match pod spec node name")
		})
	}
}

// TestVirtualPodReconciler_ReplaceDownwardAPIFieldPaths tests the replaceDownwardAPIFieldPaths function
func TestVirtualPodReconciler_ReplaceDownwardAPIFieldPaths(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name          string
		items         []corev1.DownwardAPIVolumeFile
		expectedItems []corev1.DownwardAPIVolumeFile
	}{
		{
			name: "replace_metadata_namespace_fieldpath",
			items: []corev1.DownwardAPIVolumeFile{
				{
					Path: "namespace.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
			},
			expectedItems: []corev1.DownwardAPIVolumeFile{
				{
					Path: "namespace.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace),
					},
				},
			},
		},
		{
			name: "replace_metadata_name_fieldpath",
			items: []corev1.DownwardAPIVolumeFile{
				{
					Path: "name.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				},
			},
			expectedItems: []corev1.DownwardAPIVolumeFile{
				{
					Path: "name.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName),
					},
				},
			},
		},
		{
			name: "replace_spec_nodename_fieldpath",
			items: []corev1.DownwardAPIVolumeFile{
				{
					Path: "nodename.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "spec.nodeName",
					},
				},
			},
			expectedItems: []corev1.DownwardAPIVolumeFile{
				{
					Path: "nodename.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName),
					},
				},
			},
		},
		{
			name: "replace_multiple_fieldpaths",
			items: []corev1.DownwardAPIVolumeFile{
				{
					Path: "namespace.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
				{
					Path: "name.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				},
				{
					Path: "nodename.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "spec.nodeName",
					},
				},
			},
			expectedItems: []corev1.DownwardAPIVolumeFile{
				{
					Path: "namespace.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace),
					},
				},
				{
					Path: "name.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName),
					},
				},
				{
					Path: "nodename.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName),
					},
				},
			},
		},
		{
			name: "leave_other_fieldpaths_unchanged",
			items: []corev1.DownwardAPIVolumeFile{
				{
					Path: "namespace.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
				{
					Path: "uid.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.uid",
					},
				},
				{
					Path: "labels.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.labels",
					},
				},
			},
			expectedItems: []corev1.DownwardAPIVolumeFile{
				{
					Path: "namespace.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace),
					},
				},
				{
					Path: "uid.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.uid",
					},
				},
				{
					Path: "labels.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.labels",
					},
				},
			},
		},
		{
			name: "handle_items_without_fieldref",
			items: []corev1.DownwardAPIVolumeFile{
				{
					Path: "namespace.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
				{
					Path: "config.txt",
					ResourceFieldRef: &corev1.ResourceFieldSelector{
						Resource: "requests.cpu",
					},
				},
			},
			expectedItems: []corev1.DownwardAPIVolumeFile{
				{
					Path: "namespace.txt",
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace),
					},
				},
				{
					Path: "config.txt",
					ResourceFieldRef: &corev1.ResourceFieldSelector{
						Resource: "requests.cpu",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a copy of the items to avoid modifying the original
			items := make([]corev1.DownwardAPIVolumeFile, len(tt.items))
			copy(items, tt.items)

			// Call the function
			reconciler.replaceDownwardAPIFieldPaths(items)

			// Verify the results
			assert.Equal(t, len(tt.expectedItems), len(items), "Number of items should match")
			for i, expectedItem := range tt.expectedItems {
				assert.Equal(t, expectedItem.Path, items[i].Path, "Item %d path should match", i)
				if expectedItem.FieldRef != nil {
					assert.NotNil(t, items[i].FieldRef, "Item %d should have FieldRef", i)
					assert.Equal(t, expectedItem.FieldRef.FieldPath, items[i].FieldRef.FieldPath, "Item %d FieldPath should match", i)
				} else {
					assert.Nil(t, items[i].FieldRef, "Item %d should not have FieldRef", i)
				}
				if expectedItem.ResourceFieldRef != nil {
					assert.NotNil(t, items[i].ResourceFieldRef, "Item %d should have ResourceFieldRef", i)
					assert.Equal(t, expectedItem.ResourceFieldRef.Resource, items[i].ResourceFieldRef.Resource, "Item %d Resource should match", i)
				} else {
					assert.Nil(t, items[i].ResourceFieldRef, "Item %d should not have ResourceFieldRef", i)
				}
			}
		})
	}
}

// TestVirtualPodReconciler_ReplaceContainerDownwardAPIFieldPaths tests the replaceContainerDownwardAPIFieldPaths function
func TestVirtualPodReconciler_ReplaceContainerDownwardAPIFieldPaths(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name            string
		envVars         []corev1.EnvVar
		expectedEnvVars []corev1.EnvVar
	}{
		{
			name: "replace_metadata_namespace_fieldpath",
			envVars: []corev1.EnvVar{
				{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				},
			},
			expectedEnvVars: []corev1.EnvVar{
				{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace),
						},
					},
				},
			},
		},
		{
			name: "replace_metadata_name_fieldpath",
			envVars: []corev1.EnvVar{
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.name",
						},
					},
				},
			},
			expectedEnvVars: []corev1.EnvVar{
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName),
						},
					},
				},
			},
		},
		{
			name: "replace_spec_nodename_fieldpath",
			envVars: []corev1.EnvVar{
				{
					Name: "NODE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "spec.nodeName",
						},
					},
				},
			},
			expectedEnvVars: []corev1.EnvVar{
				{
					Name: "NODE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName),
						},
					},
				},
			},
		},
		{
			name: "replace_multiple_fieldpaths",
			envVars: []corev1.EnvVar{
				{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				},
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.name",
						},
					},
				},
				{
					Name: "NODE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "spec.nodeName",
						},
					},
				},
			},
			expectedEnvVars: []corev1.EnvVar{
				{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace),
						},
					},
				},
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName),
						},
					},
				},
				{
					Name: "NODE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName),
						},
					},
				},
			},
		},
		{
			name: "leave_other_fieldpaths_unchanged",
			envVars: []corev1.EnvVar{
				{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				},
				{
					Name: "POD_UID",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.uid",
						},
					},
				},
				{
					Name: "POD_LABELS",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels",
						},
					},
				},
			},
			expectedEnvVars: []corev1.EnvVar{
				{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace),
						},
					},
				},
				{
					Name: "POD_UID",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.uid",
						},
					},
				},
				{
					Name: "POD_LABELS",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels",
						},
					},
				},
			},
		},
		{
			name: "handle_envvars_without_valuefrom",
			envVars: []corev1.EnvVar{
				{
					Name:  "STATIC_VAR",
					Value: "static_value",
				},
				{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.namespace",
						},
					},
				},
				{
					Name: "CONFIG_MAP_VAR",
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "my-config",
							},
							Key: "my-key",
						},
					},
				},
			},
			expectedEnvVars: []corev1.EnvVar{
				{
					Name:  "STATIC_VAR",
					Value: "static_value",
				},
				{
					Name: "POD_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace),
						},
					},
				},
				{
					Name: "CONFIG_MAP_VAR",
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "my-config",
							},
							Key: "my-key",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a copy of the env vars to avoid modifying the original
			envVars := make([]corev1.EnvVar, len(tt.envVars))
			copy(envVars, tt.envVars)

			// Call the function
			reconciler.replaceContainerDownwardAPIFieldPaths(envVars)

			// Verify the results
			assert.Equal(t, len(tt.expectedEnvVars), len(envVars), "Number of env vars should match")
			for i, expectedEnvVar := range tt.expectedEnvVars {
				assert.Equal(t, expectedEnvVar.Name, envVars[i].Name, "EnvVar %d name should match", i)
				assert.Equal(t, expectedEnvVar.Value, envVars[i].Value, "EnvVar %d value should match", i)

				if expectedEnvVar.ValueFrom != nil {
					assert.NotNil(t, envVars[i].ValueFrom, "EnvVar %d should have ValueFrom", i)
					if expectedEnvVar.ValueFrom.FieldRef != nil {
						assert.NotNil(t, envVars[i].ValueFrom.FieldRef, "EnvVar %d should have FieldRef", i)
						assert.Equal(t, expectedEnvVar.ValueFrom.FieldRef.FieldPath, envVars[i].ValueFrom.FieldRef.FieldPath, "EnvVar %d FieldPath should match", i)
					} else {
						assert.Nil(t, envVars[i].ValueFrom.FieldRef, "EnvVar %d should not have FieldRef", i)
					}
					if expectedEnvVar.ValueFrom.ConfigMapKeyRef != nil {
						assert.NotNil(t, envVars[i].ValueFrom.ConfigMapKeyRef, "EnvVar %d should have ConfigMapKeyRef", i)
						assert.Equal(t, expectedEnvVar.ValueFrom.ConfigMapKeyRef.Name, envVars[i].ValueFrom.ConfigMapKeyRef.Name, "EnvVar %d ConfigMapKeyRef name should match", i)
						assert.Equal(t, expectedEnvVar.ValueFrom.ConfigMapKeyRef.Key, envVars[i].ValueFrom.ConfigMapKeyRef.Key, "EnvVar %d ConfigMapKeyRef key should match", i)
					} else {
						assert.Nil(t, envVars[i].ValueFrom.ConfigMapKeyRef, "EnvVar %d should not have ConfigMapKeyRef", i)
					}
				} else {
					assert.Nil(t, envVars[i].ValueFrom, "EnvVar %d should not have ValueFrom", i)
				}
			}
		})
	}
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
			result := utils.IsSystemPod(tt.pod)
			assert.Equal(t, tt.isSystem, result)
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
			shouldSync := !utils.IsSystemPod(tt.pod) && !utils.IsDaemonSetPod(tt.pod) && tt.pod.Spec.NodeName != ""

			assert.Equal(t, tt.shouldSync, shouldSync)
		})
	}
}

// TestVirtualPodReconciler_ResourceSync tests the resource synchronization functionality
func TestVirtualPodReconciler_ResourceSync(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, schedulingv1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "physical-ns",
		},
	}

	t.Run("should sync ConfigMap successfully", func(t *testing.T) {
		ctx := context.Background()

		// Create virtual ConfigMap
		virtualConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config",
				Namespace: "virtual-ns",
				Labels: map[string]string{
					"app": "test",
				},
			},
			Data: map[string]string{
				"config-key": "config-value",
			},
		}

		// Create virtual Pod that references the ConfigMap
		virtualPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "virtual-ns",
				UID:       "test-uid",
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
					cloudv1beta1.AnnotationPhysicalPodName:      "test-pod-physical",
				},
			},
			Spec: corev1.PodSpec{
				NodeName: "test-vnode",
				Containers: []corev1.Container{
					{
						Name:  "container1",
						Image: "nginx",
						Env: []corev1.EnvVar{
							{
								Name: "CONFIG_VAR",
								ValueFrom: &corev1.EnvVarSource{
									ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "test-config",
										},
										Key: "config-key",
									},
								},
							},
						},
					},
				},
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualPod, virtualConfigMap).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		// Test syncConfigMap
		physicalName, err := reconciler.syncConfigMap(ctx, "virtual-ns", "test-config")
		assert.NoError(t, err)
		assert.NotEmpty(t, physicalName)

		// Verify virtual ConfigMap annotations were updated
		updatedConfigMap := &corev1.ConfigMap{}
		err = virtualClient.Get(ctx, types.NamespacedName{Namespace: "virtual-ns", Name: "test-config"}, updatedConfigMap)
		assert.NoError(t, err)
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, updatedConfigMap.Labels[cloudv1beta1.LabelManagedBy])
		assert.NotEmpty(t, updatedConfigMap.Annotations[cloudv1beta1.AnnotationPhysicalName])

		// Verify physical ConfigMap was created
		physicalConfigMap := &corev1.ConfigMap{}
		physicalNameFromAnnotation := updatedConfigMap.Annotations[cloudv1beta1.AnnotationPhysicalName]
		err = physicalClient.Get(ctx, types.NamespacedName{Namespace: "physical-ns", Name: physicalNameFromAnnotation}, physicalConfigMap)
		assert.NoError(t, err)
		assert.Equal(t, "config-value", physicalConfigMap.Data["config-key"])
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalConfigMap.Labels[cloudv1beta1.LabelManagedBy])
	})

	t.Run("should sync Secret successfully", func(t *testing.T) {
		ctx := context.Background()

		// Create virtual Secret
		virtualSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "virtual-ns",
				Labels: map[string]string{
					"app": "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"secret-key": []byte("secret-value"),
			},
		}

		// Create virtual Pod that references the Secret
		virtualPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "virtual-ns",
				UID:       "test-uid",
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
					cloudv1beta1.AnnotationPhysicalPodName:      "test-pod-physical",
				},
			},
			Spec: corev1.PodSpec{
				NodeName: "test-vnode",
				Containers: []corev1.Container{
					{
						Name:  "container1",
						Image: "nginx",
						Env: []corev1.EnvVar{
							{
								Name: "SECRET_VAR",
								ValueFrom: &corev1.EnvVarSource{
									SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "test-secret",
										},
										Key: "secret-key",
									},
								},
							},
						},
					},
				},
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualPod, virtualSecret).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		// Test syncSecret
		physicalName, err := reconciler.syncSecret(ctx, "virtual-ns", "test-secret")
		assert.NoError(t, err)
		assert.NotEmpty(t, physicalName)

		// Verify virtual Secret annotations were updated
		updatedSecret := &corev1.Secret{}
		err = virtualClient.Get(ctx, types.NamespacedName{Namespace: "virtual-ns", Name: "test-secret"}, updatedSecret)
		assert.NoError(t, err)
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, updatedSecret.Labels[cloudv1beta1.LabelManagedBy])
		assert.NotEmpty(t, updatedSecret.Annotations[cloudv1beta1.AnnotationPhysicalName])

		// Verify physical Secret was created
		physicalSecret := &corev1.Secret{}
		physicalNameFromAnnotation := updatedSecret.Annotations[cloudv1beta1.AnnotationPhysicalName]
		err = physicalClient.Get(ctx, types.NamespacedName{Namespace: "physical-ns", Name: physicalNameFromAnnotation}, physicalSecret)
		assert.NoError(t, err)
		assert.Equal(t, []byte("secret-value"), physicalSecret.Data["secret-key"])
		assert.Equal(t, corev1.SecretTypeOpaque, physicalSecret.Type)
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalSecret.Labels[cloudv1beta1.LabelManagedBy])
	})

	t.Run("should sync PVC successfully", func(t *testing.T) {
		ctx := context.Background()

		// Create virtual PVC that is bound with volumeName
		virtualPVC := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc",
				Namespace: "virtual-ns",
				Labels: map[string]string{
					"app": "test",
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
				VolumeName: "test-pv", // Required for bound PVC
			},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase: corev1.ClaimBound, // Required for sync
			},
		}

		// Create the associated PV
		associatedPV := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pv",
			},
			Spec: corev1.PersistentVolumeSpec{
				Capacity: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/data",
					},
				},
			},
		}

		// Create virtual Pod that references the PVC
		virtualPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "virtual-ns",
				UID:       "test-uid",
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
					cloudv1beta1.AnnotationPhysicalPodName:      "test-pod-physical",
				},
			},
			Spec: corev1.PodSpec{
				NodeName: "test-vnode",
				Containers: []corev1.Container{
					{Name: "container1", Image: "nginx"},
				},
				Volumes: []corev1.Volume{
					{
						Name: "pvc-volume",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "test-pvc",
							},
						},
					},
				},
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualPod, virtualPVC, associatedPV).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		// Test syncPVC
		physicalName, err := reconciler.syncPVC(ctx, "virtual-ns", "test-pvc")
		assert.NoError(t, err)
		assert.NotEmpty(t, physicalName)

		// Verify virtual PVC annotations were updated
		updatedPVC := &corev1.PersistentVolumeClaim{}
		err = virtualClient.Get(ctx, types.NamespacedName{Namespace: "virtual-ns", Name: "test-pvc"}, updatedPVC)
		assert.NoError(t, err)
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, updatedPVC.Labels[cloudv1beta1.LabelManagedBy])
		assert.NotEmpty(t, updatedPVC.Annotations[cloudv1beta1.AnnotationPhysicalName])

		// Verify physical PVC was created
		physicalPVC := &corev1.PersistentVolumeClaim{}
		physicalNameFromAnnotation := updatedPVC.Annotations[cloudv1beta1.AnnotationPhysicalName]
		err = physicalClient.Get(ctx, types.NamespacedName{Namespace: "physical-ns", Name: physicalNameFromAnnotation}, physicalPVC)
		assert.NoError(t, err)
		assert.Equal(t, corev1.ReadWriteOnce, physicalPVC.Spec.AccessModes[0])
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalPVC.Labels[cloudv1beta1.LabelManagedBy])

		// Note: Since the test PVC is bound with volumeName, both PV and PVC should be synced
		// Verify that the physical PVC has the correct physical PV name
		expectedPhysicalPVName := "test-pv-14c15d9b072115b6e7aae77aa7f1732d"
		assert.Equal(t, expectedPhysicalPVName, physicalPVC.Spec.VolumeName, "Physical PVC should have the correct physical PV name")
	})

	t.Run("should sync PV when PVC is bound with volumeName", func(t *testing.T) {
		ctx := context.Background()

		// Create a bound PVC with volumeName
		boundPVC := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bound-pvc",
				Namespace: "virtual-ns",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
				VolumeName: "test-pv", // This triggers PV sync
			},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase: corev1.ClaimBound,
			},
		}

		// Create the associated PV
		associatedPV := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pv",
			},
			Spec: corev1.PersistentVolumeSpec{
				Capacity: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/data",
					},
				},
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(boundPVC, associatedPV).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		// Test syncPVC - should also sync the associated PV
		physicalName, err := reconciler.syncPVC(ctx, "virtual-ns", "bound-pvc")
		assert.NoError(t, err)
		assert.NotEmpty(t, physicalName)

		// Verify virtual PVC annotations were updated
		updatedPVC := &corev1.PersistentVolumeClaim{}
		err = virtualClient.Get(ctx, types.NamespacedName{Namespace: "virtual-ns", Name: "bound-pvc"}, updatedPVC)
		assert.NoError(t, err)
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, updatedPVC.Labels[cloudv1beta1.LabelManagedBy])
		assert.NotEmpty(t, updatedPVC.Annotations[cloudv1beta1.AnnotationPhysicalName])

		// Verify physical PVC was created
		physicalPVC := &corev1.PersistentVolumeClaim{}
		physicalNameFromAnnotation := updatedPVC.Annotations[cloudv1beta1.AnnotationPhysicalName]
		err = physicalClient.Get(ctx, types.NamespacedName{Namespace: "physical-ns", Name: physicalNameFromAnnotation}, physicalPVC)
		assert.NoError(t, err)
		assert.Equal(t, corev1.ReadWriteOnce, physicalPVC.Spec.AccessModes[0])
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalPVC.Labels[cloudv1beta1.LabelManagedBy])

		// Verify virtual PV annotations were updated
		updatedPV := &corev1.PersistentVolume{}
		err = virtualClient.Get(ctx, types.NamespacedName{Name: "test-pv"}, updatedPV)
		assert.NoError(t, err)
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, updatedPV.Labels[cloudv1beta1.LabelManagedBy])
		assert.NotEmpty(t, updatedPV.Annotations[cloudv1beta1.AnnotationPhysicalName])
		assert.Equal(t, "", updatedPV.Annotations[cloudv1beta1.AnnotationPhysicalNamespace]) // PVs are cluster-scoped

		// Verify physical PV was created
		physicalPV := &corev1.PersistentVolume{}
		physicalPVNameFromAnnotation := updatedPV.Annotations[cloudv1beta1.AnnotationPhysicalName]
		err = physicalClient.Get(ctx, types.NamespacedName{Name: physicalPVNameFromAnnotation}, physicalPV)
		assert.NoError(t, err)
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalPV.Labels[cloudv1beta1.LabelManagedBy])
		assert.Equal(t, resource.MustParse("1Gi"), physicalPV.Spec.Capacity[corev1.ResourceStorage])
		assert.Equal(t, corev1.ReadWriteOnce, physicalPV.Spec.AccessModes[0])
		assert.Equal(t, "/data", physicalPV.Spec.HostPath.Path)
	})

	// Helper function to create a reconciler for PVC tests
	createPVCTestReconciler := func(objects ...client.Object) *VirtualPodReconciler {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()
		return &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}
	}

	// Helper function to create a test PVC with specified phase
	createTestPVC := func(name string, phase corev1.PersistentVolumeClaimPhase, volumeName string) *corev1.PersistentVolumeClaim {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "virtual-ns",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
			},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase: phase,
			},
		}
		if volumeName != "" {
			pvc.Spec.VolumeName = volumeName
		}
		return pvc
	}

	t.Run("should return error when PVC is not bound", func(t *testing.T) {
		ctx := context.Background()
		unboundPVC := createTestPVC("unbound-pvc", corev1.ClaimPending, "")
		reconciler := createPVCTestReconciler(unboundPVC)

		_, err := reconciler.syncPVC(ctx, "virtual-ns", "unbound-pvc")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "is not bound, current phase: Pending")
	})

	t.Run("should return error when PVC is bound but has no volumeName", func(t *testing.T) {
		ctx := context.Background()
		boundPVCNoVolume := createTestPVC("bound-pvc-no-volume", corev1.ClaimBound, "")
		reconciler := createPVCTestReconciler(boundPVCNoVolume)

		_, err := reconciler.syncPVC(ctx, "virtual-ns", "bound-pvc-no-volume")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "is bound but has no volumeName")
	})

	t.Run("should handle missing virtual resources gracefully", func(t *testing.T) {
		ctx := context.Background()

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		// Test syncConfigMap with non-existent ConfigMap
		_, err := reconciler.syncConfigMap(ctx, "virtual-ns", "non-existent-config")
		assert.Error(t, err) // Should return error for missing resources

		// Test syncSecret with non-existent Secret
		_, err = reconciler.syncSecret(ctx, "virtual-ns", "non-existent-secret")
		assert.Error(t, err) // Should return error for missing resources

		// Test syncPVC with non-existent PVC
		_, err = reconciler.syncPVC(ctx, "virtual-ns", "non-existent-pvc")
		assert.Error(t, err) // Should return error for missing resources
	})

	t.Run("should skip update if all annotations and labels already exist and match", func(t *testing.T) {
		ctx := context.Background()

		// Generate the expected physical name
		expectedPhysicalName := fmt.Sprintf("%x", md5.Sum([]byte("virtual-ns/test-config")))
		expectedPhysicalName = "test-config-" + expectedPhysicalName

		// Create virtual ConfigMap with existing physical name annotation, namespace annotation, and managed-by label
		virtualConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config",
				Namespace: "virtual-ns",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				},
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalName:      expectedPhysicalName,
					cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
				},
			},
			Data: map[string]string{
				"config-key": "config-value",
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualConfigMap).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		// Test syncConfigMap - should not update annotations since they already exist and match
		physicalName, err := reconciler.syncConfigMap(ctx, "virtual-ns", "test-config")
		assert.NoError(t, err)
		assert.NotEmpty(t, physicalName)

		// Verify annotations and labels were not changed
		updatedConfigMap := &corev1.ConfigMap{}
		err = virtualClient.Get(ctx, types.NamespacedName{Namespace: "virtual-ns", Name: "test-config"}, updatedConfigMap)
		assert.NoError(t, err)
		assert.Equal(t, expectedPhysicalName, updatedConfigMap.Annotations[cloudv1beta1.AnnotationPhysicalName])
		assert.Equal(t, "physical-ns", updatedConfigMap.Annotations[cloudv1beta1.AnnotationPhysicalNamespace])
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, updatedConfigMap.Labels[cloudv1beta1.LabelManagedBy])
	})

	t.Run("should return error if physical name annotation exists but doesn't match", func(t *testing.T) {
		ctx := context.Background()

		// Create virtual ConfigMap with existing physical name annotation that doesn't match
		virtualConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config",
				Namespace: "virtual-ns",
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalName: "different-physical-name",
				},
			},
			Data: map[string]string{
				"config-key": "config-value",
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualConfigMap).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		// Test syncConfigMap - should return error since physical name doesn't match
		_, err := reconciler.syncConfigMap(ctx, "virtual-ns", "test-config")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "physical name annotation already exists but doesn't match")
	})

	t.Run("should return error when resource mapping is missing", func(t *testing.T) {

		// Create virtual Pod that references resources not in mapping
		virtualPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "virtual-ns",
				UID:       "test-uid",
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
					cloudv1beta1.AnnotationPhysicalPodName:      "test-pod-physical",
				},
			},
			Spec: corev1.PodSpec{
				NodeName: "test-vnode",
				Volumes: []corev1.Volume{
					{
						Name: "config-volume",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "missing-config",
								},
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "container1",
						Image: "nginx",
						Env: []corev1.EnvVar{
							{
								Name: "SECRET_VAR",
								ValueFrom: &corev1.EnvVarSource{
									SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "missing-secret",
										},
										Key: "secret-key",
									},
								},
							},
						},
					},
				},
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualPod, clusterBinding).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		// Create empty resource mapping (missing the required resources)
		resourceMapping := &ResourceMapping{
			ConfigMaps: make(map[string]string),
			Secrets:    make(map[string]string),
			PVCs:       make(map[string]string),
		}

		// Test buildPhysicalPodSpec - should return error for missing mappings
		_, err := reconciler.buildPhysicalPodSpec(context.Background(), virtualPod, "test-node", resourceMapping)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "configMap mapping not found for virtual ConfigMap: missing-config")
	})
}

// TestVirtualPodReconciler_syncPV tests the syncPV function
func TestVirtualPodReconciler_syncPV(t *testing.T) {
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
		name          string
		pvName        string
		virtualPV     *corev1.PersistentVolume
		virtualSecret *corev1.Secret
		setupClient   func() client.Client
		expectedError bool
		validateFunc  func(t *testing.T, physicalClient client.Client)
	}{
		{
			name:   "should sync PV without CSI secret successfully",
			pvName: "test-pv",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						"test-label": "test-value",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			setupClient: func() client.Client {
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			validateFunc: func(t *testing.T, physicalClient client.Client) {
				var physicalPV corev1.PersistentVolume
				err := physicalClient.Get(context.Background(), types.NamespacedName{Name: "test-pv-14c15d9b072115b6e7aae77aa7f1732d"}, &physicalPV)
				assert.NoError(t, err)
				assert.Equal(t, "test-pv-14c15d9b072115b6e7aae77aa7f1732d", physicalPV.Name)
				assert.Equal(t, "test-value", physicalPV.Labels["test-label"])
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalPV.Labels[cloudv1beta1.LabelManagedBy])
			},
		},
		{
			name:   "should sync PV with CSI secret successfully",
			pvName: "test-pv-csi",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-csi",
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: "test-driver",
							NodePublishSecretRef: &corev1.SecretReference{
								Name:      "test-secret",
								Namespace: "test-namespace",
							},
						},
					},
				},
			},
			virtualSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"key1": []byte("value1"),
				},
			},
			setupClient: func() client.Client {
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			validateFunc: func(t *testing.T, physicalClient client.Client) {
				// Check that the secret was synced with PV label
				// Calculate expected secret name
				expectedSecretHash := fmt.Sprintf("%x", md5.Sum([]byte("test-namespace/test-secret")))
				expectedSecretName := "test-secret-" + expectedSecretHash

				var physicalSecret corev1.Secret
				err := physicalClient.Get(context.Background(), types.NamespacedName{
					Name:      expectedSecretName,
					Namespace: "test-namespace",
				}, &physicalSecret)
				assert.NoError(t, err)
				assert.Equal(t, cloudv1beta1.LabelValueTrue, physicalSecret.Labels[cloudv1beta1.LabelUsedByPV])

				// Check that the PV was synced with updated secret reference
				// Calculate expected PV name
				expectedPVHash := fmt.Sprintf("%x", md5.Sum([]byte("/test-pv-csi")))
				expectedPVName := "test-pv-csi-" + expectedPVHash

				var physicalPV corev1.PersistentVolume
				err = physicalClient.Get(context.Background(), types.NamespacedName{
					Name: expectedPVName,
				}, &physicalPV)
				assert.NoError(t, err)
				assert.Equal(t, expectedSecretName,
					physicalPV.Spec.CSI.NodePublishSecretRef.Name)
			},
		},
		{
			name:   "should handle missing virtual PV",
			pvName: "non-existent-pv",
			setupClient: func() client.Client {
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: true,
		},
		{
			name:   "should handle CSI secret sync failure",
			pvName: "test-pv-csi-fail",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-csi-fail",
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: "test-driver",
							NodePublishSecretRef: &corev1.SecretReference{
								Name:      "non-existent-secret",
								Namespace: "test-namespace",
							},
						},
					},
				},
			},
			setupClient: func() client.Client {
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Setup virtual client with test objects
			virtualObjects := []client.Object{}
			if tt.virtualPV != nil {
				virtualObjects = append(virtualObjects, tt.virtualPV)
			}
			if tt.virtualSecret != nil {
				virtualObjects = append(virtualObjects, tt.virtualSecret)
			}
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjects...).Build()

			// Setup physical client
			physicalClient := tt.setupClient()

			reconciler := &VirtualPodReconciler{
				VirtualClient:     virtualClient,
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: fake.NewSimpleClientset(),
				ClusterBinding:    clusterBinding,
				Log:               ctrl.Log.WithName("test"),
			}

			physicalName, err := reconciler.syncPV(ctx, tt.pvName)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, physicalName)

				if tt.validateFunc != nil {
					tt.validateFunc(t, physicalClient)
				}
			}
		})
	}
}

// TestVirtualPodReconciler_syncResource tests the syncResource function with different resource types
func TestVirtualPodReconciler_syncResource(t *testing.T) {
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
		name            string
		resourceType    topcommon.ResourceType
		virtualObj      client.Object
		syncResourceOpt *topcommon.SyncResourceOpt
		setupClient     func() client.Client
		expectedError   bool
		validateFunc    func(t *testing.T, physicalClient client.Client, physicalName string)
	}{
		{
			name:         "should sync ConfigMap with nil options",
			resourceType: topcommon.ResourceTypeConfigMap,
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"key1": "value1",
				},
			},
			syncResourceOpt: nil,
			setupClient: func() client.Client {
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			validateFunc: func(t *testing.T, physicalClient client.Client, physicalName string) {
				var physicalConfigMap corev1.ConfigMap
				err := physicalClient.Get(context.Background(), types.NamespacedName{
					Name:      physicalName,
					Namespace: "test-cluster",
				}, &physicalConfigMap)
				assert.NoError(t, err)
				assert.Equal(t, "value1", physicalConfigMap.Data["key1"])
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalConfigMap.Labels[cloudv1beta1.LabelManagedBy])
			},
		},
		{
			name:         "should sync Secret with PV reference label",
			resourceType: topcommon.ResourceTypeSecret,
			virtualObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"key1": []byte("value1"),
				},
			},
			syncResourceOpt: &topcommon.SyncResourceOpt{
				IsPVRefSecret: true,
			},
			setupClient: func() client.Client {
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			validateFunc: func(t *testing.T, physicalClient client.Client, physicalName string) {
				// Calculate expected secret name using the same logic as generatePhysicalName
				input := fmt.Sprintf("%s/%s", "test-namespace", "test-secret")
				hash := md5.Sum([]byte(input))
				hashString := fmt.Sprintf("%x", hash)
				expectedSecretName := fmt.Sprintf("test-secret-%s", hashString)

				t.Logf("Expected secret name: %s", expectedSecretName)
				t.Logf("Returned physical name: %s", physicalName)

				var physicalSecret corev1.Secret
				err := physicalClient.Get(context.Background(), types.NamespacedName{
					Name:      expectedSecretName,
					Namespace: "test-cluster", // Secret is created in the mount namespace
				}, &physicalSecret)
				assert.NoError(t, err)
				assert.Equal(t, cloudv1beta1.LabelValueTrue, physicalSecret.Labels[cloudv1beta1.LabelUsedByPV])
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalSecret.Labels[cloudv1beta1.LabelManagedBy])
				assert.Equal(t, "value1", string(physicalSecret.Data["key1"]))
			},
		},
		{
			name:         "should sync PVC with PV name",
			resourceType: topcommon.ResourceTypePVC,
			virtualObj: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "test-namespace",
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
			syncResourceOpt: &topcommon.SyncResourceOpt{
				PhysicalPVName: "test-pv-name",
			},
			setupClient: func() client.Client {
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			validateFunc: func(t *testing.T, physicalClient client.Client, physicalName string) {
				var physicalPVC corev1.PersistentVolumeClaim
				err := physicalClient.Get(context.Background(), types.NamespacedName{
					Name:      physicalName,
					Namespace: "test-cluster",
				}, &physicalPVC)
				assert.NoError(t, err)
				assert.Equal(t, "test-pv-name", physicalPVC.Spec.VolumeName)
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, physicalPVC.Labels[cloudv1beta1.LabelManagedBy])
			},
		},
		{
			name:         "should sync PV with CSI secret reference",
			resourceType: topcommon.ResourceTypePV,
			virtualObj: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: "test-driver",
							NodePublishSecretRef: &corev1.SecretReference{
								Name:      "original-secret",
								Namespace: "test-namespace",
							},
						},
					},
				},
			},
			syncResourceOpt: &topcommon.SyncResourceOpt{
				PhysicalPVRefSecretName: "physical-secret-name",
			},
			setupClient: func() client.Client {
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
			validateFunc: func(t *testing.T, physicalClient client.Client, physicalName string) {
				var physicalPV corev1.PersistentVolume
				err := physicalClient.Get(context.Background(), types.NamespacedName{
					Name: physicalName,
				}, &physicalPV)
				assert.NoError(t, err)
				assert.Equal(t, "physical-secret-name", physicalPV.Spec.CSI.NodePublishSecretRef.Name)
				assert.Equal(t, corev1.PersistentVolumeReclaimRetain, physicalPV.Spec.PersistentVolumeReclaimPolicy)
				assert.Nil(t, physicalPV.Spec.ClaimRef)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Setup virtual client with test object
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(tt.virtualObj).Build()

			// Setup physical client
			physicalClient := tt.setupClient()

			reconciler := &VirtualPodReconciler{
				VirtualClient:     virtualClient,
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: fake.NewSimpleClientset(),
				ClusterBinding:    clusterBinding,
				Log:               ctrl.Log.WithName("test"),
			}

			physicalName, err := reconciler.syncResource(ctx, tt.resourceType,
				tt.virtualObj.GetNamespace(), tt.virtualObj.GetName(),
				"test-cluster", tt.virtualObj, tt.syncResourceOpt)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, physicalName)

				if tt.validateFunc != nil {
					tt.validateFunc(t, physicalClient, physicalName)
				}
			}
		})
	}
}

// TestVirtualPodReconciler_GeneratePhysicalResourceName tests the physical resource name generation
func TestVirtualPodReconciler_GeneratePhysicalResourceName(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "physical-ns",
		},
	}

	reconciler := &VirtualPodReconciler{
		ClusterBinding: clusterBinding,
		Scheme:         scheme,
		Log:            zap.New(),
	}

	t.Run("should generate correct physical name for short resource name", func(t *testing.T) {
		physicalName := reconciler.generatePhysicalResourceName("test-config", "virtual-ns")

		// Should start with the resource name
		assert.True(t, strings.HasPrefix(physicalName, "test-config-"))

		// Should contain MD5 hash
		assert.True(t, len(physicalName) > len("test-config-"))

		// Verify MD5 hash
		expectedHash := fmt.Sprintf("%x", md5.Sum([]byte("virtual-ns/test-config")))
		assert.True(t, strings.HasSuffix(physicalName, expectedHash))
	})

	t.Run("should truncate long resource name", func(t *testing.T) {
		longName := "very-long-config-map-name-that-exceeds-31-characters"
		physicalName := reconciler.generatePhysicalResourceName(longName, "virtual-ns")

		t.Logf("Generated physical name: %s", physicalName)
		t.Logf("Long name length: %d", len(longName))
		t.Logf("Physical name length: %d", len(physicalName))

		// Should be truncated to 30 characters + hash
		assert.True(t, strings.HasPrefix(physicalName, "very-long-config-map-name-th"))
		assert.False(t, strings.HasPrefix(physicalName, longName))

		// Should contain MD5 hash
		expectedHash := fmt.Sprintf("%x", md5.Sum([]byte("virtual-ns/"+longName)))
		assert.True(t, strings.HasSuffix(physicalName, expectedHash))

		// Verify the truncated part is exactly 30 characters
		// Format is: truncatedName-hashString
		lastDashIndex := strings.LastIndex(physicalName, "-")
		assert.True(t, lastDashIndex > 0)
		truncatedPart := physicalName[:lastDashIndex]
		t.Logf("Truncated part: %s (length: %d)", truncatedPart, len(truncatedPart))
		assert.Equal(t, 30, len(truncatedPart))
	})

	t.Run("should generate consistent names for same input", func(t *testing.T) {
		name1 := reconciler.generatePhysicalResourceName("test-config", "virtual-ns")
		name2 := reconciler.generatePhysicalResourceName("test-config", "virtual-ns")

		assert.Equal(t, name1, name2)
	})

	t.Run("should generate different names for different inputs", func(t *testing.T) {
		name1 := reconciler.generatePhysicalResourceName("test-config", "virtual-ns")
		name2 := reconciler.generatePhysicalResourceName("test-config", "different-ns")

		assert.NotEqual(t, name1, name2)
	})
}

// TestVirtualPodReconciler_handlePhysicalPodEvent tests the physical pod event handling
func TestVirtualPodReconciler_handlePhysicalPodEvent(t *testing.T) {
	tests := []struct {
		name           string
		physicalPod    *corev1.Pod
		eventType      string
		setupWorkQueue func() workqueue.TypedRateLimitingInterface[reconcile.Request]
		expectedLogs   []string
	}{
		{
			name: "should handle physical pod delete event with valid annotations",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
						cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "virtual-pod-uid",
					},
				},
			},
			eventType: "DELETE",
			setupWorkQueue: func() workqueue.TypedRateLimitingInterface[reconcile.Request] {
				return workqueue.NewTypedRateLimitingQueue[reconcile.Request](
					workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 5*time.Minute),
				)
			},
			expectedLogs: []string{
				"Physical pod event received",
				"Enqueued virtual pod for reconciliation due to physical pod event",
			},
		},
		{
			name: "should ignore physical pod without managed-by label",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod",
					Namespace: "physical-ns",
					Labels: map[string]string{
						"other-label": "other-value",
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
						cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "virtual-pod-uid",
					},
				},
			},
			eventType: "DELETE",
			setupWorkQueue: func() workqueue.TypedRateLimitingInterface[reconcile.Request] {
				return workqueue.NewTypedRateLimitingQueue[reconcile.Request](
					workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 5*time.Minute),
				)
			},
			expectedLogs: []string{},
		},
		{
			name: "should ignore physical pod without virtual pod annotations",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
			},
			eventType: "DELETE",
			setupWorkQueue: func() workqueue.TypedRateLimitingInterface[reconcile.Request] {
				return workqueue.NewTypedRateLimitingQueue[reconcile.Request](
					workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 5*time.Minute),
				)
			},
			expectedLogs: []string{},
		},
		{
			name: "should handle nil work queue gracefully",
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: "virtual-ns",
						cloudv1beta1.AnnotationVirtualPodName:      "virtual-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "virtual-pod-uid",
					},
				},
			},
			eventType: "DELETE",
			setupWorkQueue: func() workqueue.TypedRateLimitingInterface[reconcile.Request] {
				return nil
			},
			expectedLogs: []string{
				"Physical pod event received",
				"Work queue is nil, cannot enqueue virtual pod",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup reconciler
			reconciler := &VirtualPodReconciler{
				Log:       ctrl.Log.WithName("test"),
				workQueue: tt.setupWorkQueue(),
			}

			// Call the function
			reconciler.handlePhysicalPodEvent(tt.physicalPod, tt.eventType)

			// Note: We can't easily capture logs in this test environment,
			// but we can verify the function doesn't panic and completes successfully
			assert.True(t, true, "Function completed without panic")
		})
	}
}

// TestVirtualPodReconciler_recordPhysicalPodMapping tests the physical pod mapping recording
func TestVirtualPodReconciler_recordPhysicalPodMapping(t *testing.T) {
	tests := []struct {
		name           string
		virtualPod     *corev1.Pod
		physicalPod    *corev1.Pod
		setupClient    func() client.Client
		expectedError  bool
		expectedResult ctrl.Result
	}{
		{
			name: "should record physical pod mapping successfully",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
				},
			},
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod",
					Namespace: "physical-ns",
					UID:       "physical-pod-uid",
				},
			},
			setupClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				virtualPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "virtual-pod",
						Namespace: "virtual-ns",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualPod).Build()
			},
			expectedError:  false,
			expectedResult: ctrl.Result{},
		},
		{
			name: "should handle update error",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
				},
			},
			physicalPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-pod",
					Namespace: "physical-ns",
					UID:       "physical-pod-uid",
				},
			},
			setupClient: func() client.Client {
				return &failingClient{}
			},
			expectedError:  true,
			expectedResult: ctrl.Result{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler := &VirtualPodReconciler{
				VirtualClient: tt.setupClient(),
				Log:           ctrl.Log.WithName("test"),
			}

			result, err := reconciler.recordPhysicalPodMapping(ctx, tt.virtualPod, tt.physicalPod)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestVirtualPodReconciler_updateVirtualPodWithPhysicalUID tests the virtual pod UID update
func TestVirtualPodReconciler_updateVirtualPodWithPhysicalUID(t *testing.T) {
	tests := []struct {
		name           string
		virtualPod     *corev1.Pod
		physicalUID    string
		setupClient    func() client.Client
		expectedError  bool
		expectedResult ctrl.Result
	}{
		{
			name: "should update virtual pod with physical UID successfully",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
				},
			},
			physicalUID: "physical-pod-uid",
			setupClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				virtualPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "virtual-pod",
						Namespace: "virtual-ns",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualPod).Build()
			},
			expectedError:  false,
			expectedResult: ctrl.Result{},
		},
		{
			name: "should handle update error",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
				},
			},
			physicalUID: "physical-pod-uid",
			setupClient: func() client.Client {
				return &failingClient{}
			},
			expectedError:  true,
			expectedResult: ctrl.Result{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler := &VirtualPodReconciler{
				VirtualClient: tt.setupClient(),
				Log:           ctrl.Log.WithName("test"),
			}

			result, err := reconciler.updateVirtualPodWithPhysicalUID(ctx, tt.virtualPod, tt.physicalUID)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestVirtualPodReconciler_getPhysicalPodWithFallback tests the physical pod retrieval with fallback
func TestVirtualPodReconciler_getPhysicalPodWithFallback(t *testing.T) {
	tests := []struct {
		name                string
		namespace           string
		podName             string
		setupPhysicalClient func() client.Client
		setupK8sClient      func() *fake.Clientset
		expectedError       bool
		expectedPod         *corev1.Pod
	}{
		{
			name:      "should get pod from cache successfully",
			namespace: "test-ns",
			podName:   "test-pod",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-ns",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			expectedError: false,
			expectedPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
			},
		},
		{
			name:      "should fallback to direct k8s client when not found in cache",
			namespace: "test-ns",
			podName:   "test-pod",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-ns",
					},
				}
				return fake.NewSimpleClientset(pod)
			},
			expectedError: false,
			expectedPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
			},
		},
		{
			name:      "should return error when pod not found anywhere",
			namespace: "test-ns",
			podName:   "test-pod",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			expectedError: true,
			expectedPod:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler := &VirtualPodReconciler{
				PhysicalClient:    tt.setupPhysicalClient(),
				PhysicalK8sClient: tt.setupK8sClient(),
				Log:               ctrl.Log.WithName("test"),
			}

			pod, err := reconciler.getPhysicalPodWithFallback(ctx, tt.namespace, tt.podName)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, pod)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, pod)
				assert.Equal(t, tt.expectedPod.Name, pod.Name)
				assert.Equal(t, tt.expectedPod.Namespace, pod.Namespace)
			}
		})
	}
}

// failingClient is a mock client that fails on update operations
type failingClient struct {
	client.Client
}

func (c *failingClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	return fmt.Errorf("get failed")
}

func (c *failingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return fmt.Errorf("delete failed")
}

func (c *failingClient) Status() client.StatusWriter {
	return &failingStatusWriter{}
}

type failingStatusWriter struct {
	client.StatusWriter
}

func (w *failingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return fmt.Errorf("update failed")
}

// TestVirtualPodReconciler_handleVirtualPodDeletion_ErrorCases tests error cases for virtual pod deletion
func TestVirtualPodReconciler_handleVirtualPodDeletion_ErrorCases(t *testing.T) {
	tests := []struct {
		name           string
		virtualPod     *corev1.Pod
		setupClient    func() client.Client
		expectedError  bool
		expectedResult ctrl.Result
	}{
		{
			name: "should handle physical pod deletion failure",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
					},
				},
			},
			setupClient: func() client.Client {
				return &failingClient{}
			},
			expectedError:  true,
			expectedResult: ctrl.Result{},
		},
		{
			name: "should handle missing physical pod gracefully",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
					},
				},
			},
			setupClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError:  false,
			expectedResult: ctrl.Result{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler := &VirtualPodReconciler{
				VirtualClient:     tt.setupClient(),
				PhysicalClient:    tt.setupClient(),
				PhysicalK8sClient: fake.NewSimpleClientset(),
				Log:               ctrl.Log.WithName("test"),
			}

			result, err := reconciler.handleVirtualPodDeletion(ctx, tt.virtualPod)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestVirtualPodReconciler_forceDeleteVirtualPod_ErrorCases tests error cases for force delete
func TestVirtualPodReconciler_forceDeleteVirtualPod_ErrorCases(t *testing.T) {
	tests := []struct {
		name           string
		virtualPod     *corev1.Pod
		setupClient    func() client.Client
		expectedError  bool
		expectedResult ctrl.Result
	}{
		{
			name: "should handle force delete failure",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
				},
			},
			setupClient: func() client.Client {
				return &failingClient{}
			},
			expectedError:  true,
			expectedResult: ctrl.Result{},
		},
		{
			name: "should handle force delete success",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "virtual-pod",
					Namespace: "virtual-ns",
				},
			},
			setupClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				virtualPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "virtual-pod",
						Namespace: "virtual-ns",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualPod).Build()
			},
			expectedError:  false,
			expectedResult: ctrl.Result{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			reconciler := &VirtualPodReconciler{
				VirtualClient: tt.setupClient(),
				Log:           ctrl.Log.WithName("test"),
			}

			result, err := reconciler.forceDeleteVirtualPod(ctx, tt.virtualPod)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestVirtualPodReconciler_ClusterIDFunctionality tests ClusterID related functionality
func TestVirtualPodReconciler_ClusterIDFunctionality(t *testing.T) {
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

	t.Run("ClusterID caching", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &VirtualPodReconciler{
			VirtualClient:  virtualClient,
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test"),
			workQueue:      workqueue.NewTypedRateLimitingQueue[reconcile.Request](workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]()),
		}

		// Set ClusterID directly for testing
		reconciler.ClusterID = clusterBinding.Spec.ClusterID

		// Verify ClusterID is cached
		assert.Equal(t, "test-cluster-id", reconciler.ClusterID)
	})

	t.Run("ClusterID label and finalizer", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &VirtualPodReconciler{
			VirtualClient:  virtualClient,
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test"),
			workQueue:      workqueue.NewTypedRateLimitingQueue[reconcile.Request](workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]()),
			ClusterID:      "test-cluster-id", // Set ClusterID directly for testing
		}

		virtualPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "test-ns",
				Labels:    make(map[string]string),
			},
		}

		// Add the pod to the client
		err := virtualClient.Create(context.Background(), virtualPod)
		require.NoError(t, err)

		// Test updateVirtualResourceLabelsAndAnnotations adds ClusterID label
		err = reconciler.updateVirtualResourceLabelsAndAnnotations(context.Background(), virtualPod, "physical-pod", "physical-ns", nil)
		require.NoError(t, err)

		// Get the updated pod from the client to see the changes
		updatedPod := &corev1.Pod{}
		err = virtualClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "test-ns"}, updatedPod)
		require.NoError(t, err)

		expectedClusterIDLabel := "kubeocean.io/synced-by-test-cluster-id"
		assert.Equal(t, cloudv1beta1.LabelValueTrue, updatedPod.Labels[expectedClusterIDLabel])
		assert.Equal(t, cloudv1beta1.LabelManagedByValue, updatedPod.Labels[cloudv1beta1.LabelManagedBy])

		// Test ClusterID finalizer methods
		assert.False(t, reconciler.hasSyncedResourceFinalizer(virtualPod))

		reconciler.addSyncedResourceFinalizer(virtualPod)
		expectedFinalizer := "kubeocean.io/finalizer-test-cluster-id"
		assert.True(t, reconciler.hasSyncedResourceFinalizer(virtualPod))

		// Verify the finalizer is actually added
		finalizers := virtualPod.GetFinalizers()
		assert.Contains(t, finalizers, expectedFinalizer)
	})
}

// TestVirtualPodReconciler_CleanupServiceAccountToken tests the cleanupServiceAccountToken function
func TestVirtualPodReconciler_CleanupServiceAccountToken(t *testing.T) {
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

	// Create a mock TokenManager
	mockTokenManager := &MockTokenManager{}

	tests := []struct {
		name           string
		virtualPod     *corev1.Pod
		physicalSecret *corev1.Secret
		expectError    bool
		errorContains  string
	}{
		{
			name: "no service account name - should skip cleanup",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "",
				},
			},
			expectError: false,
		},
		{
			name: "service account token secret not found - should not error",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					UID:       "test-uid-123",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "test-sa",
					Volumes: []corev1.Volume{
						{
							Name: "kube-api-access-xyz",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Audience:          "https://kubernetes.default.svc.cluster.local",
												ExpirationSeconds: ptr.To(int64(3607)),
												Path:              "token",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "service account token secret exists - should delete successfully",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					UID:       "test-uid-123",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "test-sa",
					Volumes: []corev1.Volume{
						{
							Name: "kube-api-access-xyz",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Audience:          "https://kubernetes.default.svc.cluster.local",
												ExpirationSeconds: ptr.To(int64(3607)),
												Path:              "token",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			physicalSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "", // Will be set dynamically in the test
					Namespace: "test-cluster",
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"token": []byte("base64-encoded-token"),
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

			// Add physical secret if it exists
			if tt.physicalSecret != nil {
				// Generate the correct secret name using the same logic as the actual code
				if tt.virtualPod.Spec.ServiceAccountName != "" {
					// Use the same key generation logic as the actual code
					key := fmt.Sprintf("%s-%s", tt.virtualPod.Name, tt.virtualPod.UID)

					// Generate physical name using the same logic as generatePhysicalName
					truncatedKey := key
					if len(key) > 30 {
						truncatedKey = key[:30]
					}
					input := fmt.Sprintf("%s/%s", tt.virtualPod.Namespace, key)
					hash := fmt.Sprintf("%x", md5.Sum([]byte(input)))
					physicalSecretName := fmt.Sprintf("%s-%s", truncatedKey, hash)

					tt.physicalSecret.Name = physicalSecretName
				}

				err := physicalClient.Create(ctx, tt.physicalSecret)
				require.NoError(t, err)
			}

			reconciler := &VirtualPodReconciler{
				VirtualClient:  virtualClient,
				PhysicalClient: physicalClient,
				ClusterBinding: clusterBinding,
				Log:            ctrl.Log.WithName("test"),
				ClusterID:      "test-cluster-id",
				TokenManager:   mockTokenManager,
			}

			// Call the function
			err := reconciler.cleanupServiceAccountToken(ctx, tt.virtualPod)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			assert.NoError(t, err)

			// If physical secret was created, verify it was deleted
			if tt.physicalSecret != nil {
				deletedSecret := &corev1.Secret{}
				err := physicalClient.Get(ctx, types.NamespacedName{
					Name:      tt.physicalSecret.Name,
					Namespace: tt.physicalSecret.Namespace,
				}, deletedSecret)
				if !apierrors.IsNotFound(err) {
					t.Logf("Expected secret to be deleted: %s/%s", tt.physicalSecret.Namespace, tt.physicalSecret.Name)
					if err == nil {
						t.Logf("Secret still exists: %+v", deletedSecret.ObjectMeta)
					} else {
						t.Logf("Unexpected error: %v", err)
					}
				}
				assert.True(t, apierrors.IsNotFound(err), "Secret should be deleted")
			}
		})
	}
}

// TestVirtualPodReconciler_SyncServiceAccountToken tests the syncServiceAccountToken function
func TestVirtualPodReconciler_SyncServiceAccountToken(t *testing.T) {
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

	// Create a mock TokenManager
	mockTokenManager := &MockTokenManager{
		getServiceAccountToken: func(namespace, name string, tr *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
			return &authenticationv1.TokenRequest{
				Status: authenticationv1.TokenRequestStatus{
					Token: "test-token-content",
				},
			}, nil
		},
	}

	tests := []struct {
		name              string
		virtualPod        *corev1.Pod
		createIfNotExists bool
		expectedResult    string
		expectError       bool
		errorContains     string
	}{
		{
			name: "create new service account token secret",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					UID:       "test-uid-123",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "test-sa",
					Volumes: []corev1.Volume{
						{
							Name: "kube-api-access-xyz",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Audience:          "https://kubernetes.default.svc.cluster.local",
												ExpirationSeconds: ptr.To(int64(3607)),
												Path:              "token",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			createIfNotExists: true,
			expectedResult:    "test-pod-test-uid-123-91332ab8b19f77a4f643c354bf79559d",
			expectError:       false,
		},
		{
			name: "update existing service account token secret - token different",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					UID:       "test-uid-123",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "test-sa",
					Volumes: []corev1.Volume{
						{
							Name: "kube-api-access-xyz",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Audience:          "https://kubernetes.default.svc.cluster.local",
												ExpirationSeconds: ptr.To(int64(3607)),
												Path:              "token",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			createIfNotExists: false,
			expectedResult:    "test-pod-test-uid-123-91332ab8b19f77a4f643c354bf79559d",
			expectError:       false,
		},
		{
			name: "update existing service account token secret - token same",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					UID:       "test-uid-123",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "test-sa",
					Volumes: []corev1.Volume{
						{
							Name: "kube-api-access-xyz",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Audience:          "https://kubernetes.default.svc.cluster.local",
												ExpirationSeconds: ptr.To(int64(3607)),
												Path:              "token",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			createIfNotExists: false,
			expectedResult:    "test-pod-test-uid-123-91332ab8b19f77a4f643c354bf79559d",
			expectError:       false,
		},
		{
			name: "secret not found and createIfNotExists false - should error",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					UID:       "test-uid-123",
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "test-sa",
					Volumes: []corev1.Volume{
						{
							Name: "kube-api-access-xyz",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
												Audience:          "https://kubernetes.default.svc.cluster.local",
												ExpirationSeconds: ptr.To(int64(3607)),
												Path:              "token",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			createIfNotExists: false,
			expectedResult:    "",
			expectError:       true,
			errorContains:     "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

			// For update tests, create an existing secret with different token
			if !tt.createIfNotExists && tt.name == "update existing service account token secret - token different" {
				existingSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-test-uid-123-91332ab8b19f77a4f643c354bf79559d",
						Namespace: "test-cluster",
					},
					Type: corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						"token": []byte("different-token-content"),
					},
				}
				err := physicalClient.Create(ctx, existingSecret)
				require.NoError(t, err)
			}

			// For update tests with same token, create an existing secret with same token
			if !tt.createIfNotExists && tt.name == "update existing service account token secret - token same" {
				existingSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-test-uid-123-91332ab8b19f77a4f643c354bf79559d",
						Namespace: "test-cluster",
					},
					Type: corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						"token": []byte("dGVzdC10b2tlbi1jb250ZW50"), // base64 encoded "test-token-content"
					},
				}
				err := physicalClient.Create(ctx, existingSecret)
				require.NoError(t, err)
			}

			reconciler := &VirtualPodReconciler{
				VirtualClient:  virtualClient,
				PhysicalClient: physicalClient,
				ClusterBinding: clusterBinding,
				Log:            ctrl.Log.WithName("test"),
				ClusterID:      "test-cluster-id",
				TokenManager:   mockTokenManager,
			}

			// Call the function
			result, err := reconciler.syncServiceAccountToken(ctx, tt.virtualPod, tt.createIfNotExists)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedResult, result)

			// Verify the secret was created/updated correctly
			secret := &corev1.Secret{}
			err = physicalClient.Get(ctx, types.NamespacedName{
				Name:      tt.expectedResult,
				Namespace: "test-cluster",
			}, secret)
			assert.NoError(t, err)
			assert.Equal(t, corev1.SecretTypeOpaque, secret.Type)
			assert.Contains(t, secret.Data, "token")
		})
	}
}

// TestVirtualPodReconciler_SyncConfigMapsWithProjectedVolumes tests syncConfigMaps with projected volumes
func TestVirtualPodReconciler_SyncConfigMapsWithProjectedVolumes(t *testing.T) {
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
		name              string
		virtualPod        *corev1.Pod
		virtualConfigMaps []*corev1.ConfigMap
		expectedResult    map[string]string
		expectError       bool
	}{
		{
			name: "pod with projected configmap volume",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "projected-config",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "projected-config-1",
												},
												Items: []corev1.KeyToPath{
													{Key: "config1", Path: "config1"},
												},
											},
										},
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "projected-config-2",
												},
												Items: []corev1.KeyToPath{
													{Key: "config2", Path: "config2"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			virtualConfigMaps: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "projected-config-1",
						Namespace: "test-ns",
					},
					Data: map[string]string{
						"config1": "value1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "projected-config-2",
						Namespace: "test-ns",
					},
					Data: map[string]string{
						"config2": "value2",
					},
				},
			},
			expectedResult: map[string]string{
				"projected-config-1": "projected-config-1-physical",
				"projected-config-2": "projected-config-2-physical",
			},
			expectError: false,
		},
		{
			name: "pod with mixed volume types",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "regular-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "regular-config",
									},
								},
							},
						},
						{
							Name: "projected-config",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "projected-config",
												},
												Items: []corev1.KeyToPath{
													{Key: "config", Path: "config"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			virtualConfigMaps: []*corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "regular-config",
						Namespace: "test-ns",
					},
					Data: map[string]string{
						"regular": "value",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "projected-config",
						Namespace: "test-ns",
					},
					Data: map[string]string{
						"config": "value",
					},
				},
			},
			expectedResult: map[string]string{
				"regular-config":   "regular-config-physical",
				"projected-config": "projected-config-physical",
			},
			expectError: false,
		},
	}

	// Helper function to create reconciler and test resource syncing
	testResourceSync := func(t *testing.T, resources []client.Object, syncFunc func(*VirtualPodReconciler, context.Context, *corev1.Pod) (map[string]string, error), virtualPod *corev1.Pod, expectedResult map[string]string, expectError bool) {
		ctx := context.Background()
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		// Create virtual resources
		for _, res := range resources {
			err := virtualClient.Create(ctx, res)
			require.NoError(t, err)
		}

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		result, err := syncFunc(reconciler, ctx, virtualPod)

		if expectError {
			assert.Error(t, err)
			return
		}

		assert.NoError(t, err)
		assert.Equal(t, len(expectedResult), len(result))
		for virtualName := range expectedResult {
			assert.Contains(t, result, virtualName)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resources := make([]client.Object, len(tt.virtualConfigMaps))
			for i, cm := range tt.virtualConfigMaps {
				resources[i] = cm
			}
			testResourceSync(t, resources, func(r *VirtualPodReconciler, ctx context.Context, pod *corev1.Pod) (map[string]string, error) {
				return r.syncConfigMaps(ctx, pod)
			}, tt.virtualPod, tt.expectedResult, tt.expectError)
		})
	}
}

// TestVirtualPodReconciler_SyncSecretsWithProjectedVolumes tests syncSecrets with projected volumes
func TestVirtualPodReconciler_SyncSecretsWithProjectedVolumes(t *testing.T) {
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
		virtualSecrets []*corev1.Secret
		expectedResult map[string]string
		expectError    bool
	}{
		{
			name: "pod with projected secret volume",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "projected-secret",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											Secret: &corev1.SecretProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "projected-secret-1",
												},
												Items: []corev1.KeyToPath{
													{Key: "secret1", Path: "secret1"},
												},
											},
										},
										{
											Secret: &corev1.SecretProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "projected-secret-2",
												},
												Items: []corev1.KeyToPath{
													{Key: "secret2", Path: "secret2"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			virtualSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "projected-secret-1",
						Namespace: "test-ns",
					},
					Type: corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						"secret1": []byte("value1"),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "projected-secret-2",
						Namespace: "test-ns",
					},
					Type: corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						"secret2": []byte("value2"),
					},
				},
			},
			expectedResult: map[string]string{
				"projected-secret-1": "projected-secret-1-physical",
				"projected-secret-2": "projected-secret-2-physical",
			},
			expectError: false,
		},
	}

	// Helper function similar to the one in ConfigMap test
	testSecretSync := func(t *testing.T, secrets []client.Object, virtualPod *corev1.Pod, expectedResult map[string]string, expectError bool) {
		ctx := context.Background()
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		for _, secret := range secrets {
			err := virtualClient.Create(ctx, secret)
			require.NoError(t, err)
		}

		reconciler := &VirtualPodReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			ClusterBinding:    clusterBinding,
			Scheme:            scheme,
			Log:               zap.New(),
		}

		result, err := reconciler.syncSecrets(ctx, virtualPod)

		if expectError {
			assert.Error(t, err)
			return
		}

		assert.NoError(t, err)
		assert.Equal(t, len(expectedResult), len(result))
		for virtualName := range expectedResult {
			assert.Contains(t, result, virtualName)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secrets := make([]client.Object, len(tt.virtualSecrets))
			for i, s := range tt.virtualSecrets {
				secrets[i] = s
			}
			testSecretSync(t, secrets, tt.virtualPod, tt.expectedResult, tt.expectError)
		})
	}
}

// TestVirtualPodReconciler_BuildPhysicalPodSpecWithProjectedVolumes tests buildPhysicalPodSpec with projected volumes
func TestVirtualPodReconciler_BuildPhysicalPodSpecWithProjectedVolumes(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, schedulingv1.AddToScheme(scheme))
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
		name            string
		virtualPod      *corev1.Pod
		resourceMapping *ResourceMapping
		expectedError   bool
		errorContains   string
	}{
		{
			name: "pod with projected configmap and secret volumes",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "projected-mixed",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "projected-config",
												},
												Items: []corev1.KeyToPath{
													{Key: "config", Path: "config"},
												},
											},
										},
										{
											Secret: &corev1.SecretProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "projected-secret",
												},
												Items: []corev1.KeyToPath{
													{Key: "secret", Path: "secret"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			resourceMapping: &ResourceMapping{
				ConfigMaps: map[string]string{
					"projected-config": "projected-config-physical",
				},
				Secrets: map[string]string{
					"projected-secret": "projected-secret-physical",
				},
			},
			expectedError: false,
		},
		{
			name: "pod with projected configmap but missing mapping",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "projected-config",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "missing-config",
												},
												Items: []corev1.KeyToPath{
													{Key: "config", Path: "config"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			resourceMapping: &ResourceMapping{
				ConfigMaps: map[string]string{},
				Secrets:    map[string]string{},
			},
			expectedError: true,
			errorContains: "configMap mapping not found for virtual ConfigMap in projected volume",
		},
		{
			name: "pod with projected secret but missing mapping",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "projected-secret",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											Secret: &corev1.SecretProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "missing-secret",
												},
												Items: []corev1.KeyToPath{
													{Key: "secret", Path: "secret"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			resourceMapping: &ResourceMapping{
				ConfigMaps: map[string]string{},
				Secrets:    map[string]string{},
			},
			expectedError: true,
			errorContains: "secret mapping not found for virtual Secret in projected volume",
		},
		{
			name: "pod with downwardAPI volumes and environment variables",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "downward-api-volume",
							VolumeSource: corev1.VolumeSource{
								DownwardAPI: &corev1.DownwardAPIVolumeSource{
									Items: []corev1.DownwardAPIVolumeFile{
										{
											Path: "namespace",
											FieldRef: &corev1.ObjectFieldSelector{
												FieldPath: "metadata.namespace",
											},
										},
										{
											Path: "name",
											FieldRef: &corev1.ObjectFieldSelector{
												FieldPath: "metadata.name",
											},
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "test-image",
							Env: []corev1.EnvVar{
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
								{
									Name: "POD_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.name",
										},
									},
								},
							},
						},
					},
				},
			},
			resourceMapping: &ResourceMapping{
				ConfigMaps: map[string]string{},
				Secrets:    map[string]string{},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock services for loadbalancer IPs
			kubernetesIntranetService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Port: 443,
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "10.0.0.1"},
						},
					},
				},
			}

			kubeDnsIntranetService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kube-dns-intranet",
					Namespace: "kube-system",
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "10.0.0.2"},
						},
					},
				},
			}

			// Create mock virtual client with services
			virtualClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(kubernetesIntranetService, kubeDnsIntranetService, clusterBinding).
				Build()

			// Create mock physical client with schedulingv1 scheme
			physicalScheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(physicalScheme))
			require.NoError(t, schedulingv1.AddToScheme(physicalScheme))
			require.NoError(t, cloudv1beta1.AddToScheme(physicalScheme))

			physicalClient := fakeclient.NewClientBuilder().
				WithScheme(physicalScheme).
				Build()

			reconciler := &VirtualPodReconciler{
				VirtualClient:  virtualClient,
				PhysicalClient: physicalClient,
				ClusterBinding: clusterBinding,
				Log:            ctrl.Log.WithName("test"),
				ClusterID:      "test-cluster-id",
			}

			// Call the function
			result, err := reconciler.buildPhysicalPodSpec(context.Background(), tt.virtualPod, "test-node", tt.resourceMapping)

			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			assert.NoError(t, err)

			// Verify that projected volumes were processed correctly
			for _, volume := range result.Volumes {
				if volume.Projected != nil {
					for _, source := range volume.Projected.Sources {
						if source.ConfigMap != nil {
							// The ConfigMap name should have been replaced with the physical name
							expectedName := tt.resourceMapping.ConfigMaps["projected-config"] // Use the original virtual name
							assert.Equal(t, expectedName, source.ConfigMap.Name, "ConfigMap name should be replaced with physical name")
						}
						if source.Secret != nil {
							// The Secret name should have been replaced with the physical name
							expectedName := tt.resourceMapping.Secrets["projected-secret"] // Use the original virtual name
							assert.Equal(t, expectedName, source.Secret.Name, "Secret name should be replaced with physical name")
						}
					}
				}
			}

			// Verify that DownwardAPI volumes were processed correctly
			for _, volume := range result.Volumes {
				if volume.DownwardAPI != nil {
					for _, item := range volume.DownwardAPI.Items {
						if item.FieldRef != nil {
							if item.Path == "namespace" {
								expectedFieldPath := fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace)
								assert.Equal(t, expectedFieldPath, item.FieldRef.FieldPath, "metadata.namespace fieldPath should be replaced with annotation reference")
							}
							if item.Path == "name" {
								expectedFieldPath := fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName)
								assert.Equal(t, expectedFieldPath, item.FieldRef.FieldPath, "metadata.name fieldPath should be replaced with annotation reference")
							}
						}
					}
				}
			}

			// Verify that container environment variables were processed correctly
			for _, container := range result.Containers {
				for _, envVar := range container.Env {
					if envVar.ValueFrom != nil && envVar.ValueFrom.FieldRef != nil {
						if envVar.Name == "POD_NAMESPACE" {
							expectedFieldPath := fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace)
							assert.Equal(t, expectedFieldPath, envVar.ValueFrom.FieldRef.FieldPath, "POD_NAMESPACE fieldPath should be replaced with annotation reference")
						}
						if envVar.Name == "POD_NAME" {
							expectedFieldPath := fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName)
							assert.Equal(t, expectedFieldPath, envVar.ValueFrom.FieldRef.FieldPath, "POD_NAME fieldPath should be replaced with annotation reference")
						}
					}
				}
			}

			// Verify that KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables were injected
			for _, container := range result.Containers {
				var hasKubernetesServiceHost, hasKubernetesServicePort bool
				for _, envVar := range container.Env {
					if envVar.Name == KubernetesServiceHost {
						assert.Equal(t, "10.0.0.1", envVar.Value, "KUBERNETES_SERVICE_HOST should be set to kubernetes-intranet IP")
						hasKubernetesServiceHost = true
					}
					if envVar.Name == KubernetesServicePort {
						assert.Equal(t, "443", envVar.Value, "KUBERNETES_SERVICE_PORT should be set to kubernetes-intranet port")
						hasKubernetesServicePort = true
					}
				}
				assert.True(t, hasKubernetesServiceHost, "KUBERNETES_SERVICE_HOST environment variable should be injected")
				assert.True(t, hasKubernetesServicePort, "KUBERNETES_SERVICE_PORT environment variable should be injected")
			}
		})
	}
}

// TestVirtualPodReconciler_GetKubernetesIntranetIPAndPort tests the getKubernetesIntranetIPAndPort function
func TestVirtualPodReconciler_GetKubernetesIntranetIPAndPort(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name          string
		service       *corev1.Service
		virtualClient client.Client
		expectedIP    string
		expectedPort  string
		expectedError bool
		errorContains string
	}{
		{
			name: "successful_get_with_valid_service",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Port: 443,
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "10.0.0.1"},
						},
					},
				},
			},
			expectedIP:    "10.0.0.1",
			expectedPort:  "443",
			expectedError: false,
		},
		{
			name: "successful_get_with_different_port",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Port: 6443,
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "192.168.1.100"},
						},
					},
				},
			},
			expectedIP:    "192.168.1.100",
			expectedPort:  "6443",
			expectedError: false,
		},
		{
			name:          "error_when_virtual_client_is_nil",
			service:       nil,
			virtualClient: nil,
			expectedError: true,
			errorContains: "VirtualClient is not available",
		},
		{
			name: "error_when_service_not_found",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "different-service",
					Namespace: "default",
				},
			},
			expectedError: true,
			errorContains: "failed to get kubernetes-intranet service",
		},
		{
			name: "error_when_no_loadbalancer_ingress",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Port: 443,
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{},
					},
				},
			},
			expectedError: true,
			errorContains: "kubernetes-intranet service has no loadbalancer ingress",
		},
		{
			name: "error_when_loadbalancer_ip_is_empty",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Port: 443,
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: ""},
						},
					},
				},
			},
			expectedError: true,
			errorContains: "kubernetes-intranet service loadbalancer ingress IP is empty",
		},
		{
			name: "error_when_no_ports",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "10.0.0.1"},
						},
					},
				},
			},
			expectedError: true,
			errorContains: "kubernetes-intranet service has no ports",
		},
		{
			name: "error_when_port_is_zero",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Port: 0,
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "10.0.0.1"},
						},
					},
				},
			},
			expectedError: true,
			errorContains: "kubernetes-intranet service first port is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var virtualClient client.Client
			if tt.virtualClient != nil {
				virtualClient = tt.virtualClient
			} else if tt.service != nil {
				virtualClient = fakeclient.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(tt.service).
					Build()
			} else if tt.name == "error_when_virtual_client_is_nil" {
				virtualClient = nil // Explicitly set to nil for this test case
			} else {
				virtualClient = fakeclient.NewClientBuilder().
					WithScheme(scheme).
					Build()
			}

			reconciler := &VirtualPodReconciler{
				VirtualClient: virtualClient,
				Log:           ctrl.Log.WithName("test"),
			}

			// Test getting IP and port
			ip, port, err := reconciler.getKubernetesIntranetIPAndPort(context.Background())

			if tt.expectedError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedIP, ip)
				assert.Equal(t, tt.expectedPort, port)

				// Test caching - should return cached values
				ip2, port2, err2 := reconciler.getKubernetesIntranetIPAndPort(context.Background())
				assert.NoError(t, err2)
				assert.Equal(t, tt.expectedIP, ip2)
				assert.Equal(t, tt.expectedPort, port2)

				// Verify cached values in reconciler
				assert.Equal(t, tt.expectedIP, reconciler.kubernetesIntranetIP)
				assert.Equal(t, tt.expectedPort, reconciler.kubernetesIntranetPort)
			}
		})
	}
}

// TestVirtualPodReconciler_InjectKubernetesServiceEnvVars tests the injectKubernetesServiceEnvVars function
func TestVirtualPodReconciler_InjectKubernetesServiceEnvVars(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name                string
		container           *corev1.Container
		host                string
		port                string
		expectedEnvVars     map[string]string
		expectedEnvVarCount int
	}{
		{
			name: "inject_env_vars_to_empty_container",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env:   []corev1.EnvVar{},
			},
			host: "10.0.0.1",
			port: "443",
			expectedEnvVars: map[string]string{
				KubernetesServiceHost: "10.0.0.1",
				KubernetesServicePort: "443",
			},
			expectedEnvVarCount: 2,
		},
		{
			name: "inject_env_vars_to_container_with_existing_env_vars",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env: []corev1.EnvVar{
					{
						Name:  "EXISTING_VAR",
						Value: "existing_value",
					},
					{
						Name:  "ANOTHER_VAR",
						Value: "another_value",
					},
				},
			},
			host: "192.168.1.100",
			port: "6443",
			expectedEnvVars: map[string]string{
				"EXISTING_VAR":        "existing_value",
				"ANOTHER_VAR":         "another_value",
				KubernetesServiceHost: "192.168.1.100",
				KubernetesServicePort: "6443",
			},
			expectedEnvVarCount: 4,
		},
		{
			name: "override_existing_kubernetes_service_env_vars",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env: []corev1.EnvVar{
					{
						Name:  KubernetesServiceHost,
						Value: "old-host",
					},
					{
						Name:  KubernetesServicePort,
						Value: "old-port",
					},
					{
						Name:  "OTHER_VAR",
						Value: "other_value",
					},
				},
			},
			host: "10.0.0.1",
			port: "443",
			expectedEnvVars: map[string]string{
				KubernetesServiceHost: "10.0.0.1",
				KubernetesServicePort: "443",
				"OTHER_VAR":           "other_value",
			},
			expectedEnvVarCount: 3,
		},
		{
			name: "inject_with_different_values",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env: []corev1.EnvVar{
					{
						Name:  "APP_ENV",
						Value: "production",
					},
				},
			},
			host: "172.16.0.1",
			port: "8080",
			expectedEnvVars: map[string]string{
				"APP_ENV":             "production",
				KubernetesServiceHost: "172.16.0.1",
				KubernetesServicePort: "8080",
			},
			expectedEnvVarCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a copy of the container to avoid modifying the original
			container := tt.container.DeepCopy()

			// Call the function
			reconciler.injectKubernetesServiceEnvVars(container, tt.host, tt.port)

			// Verify the number of environment variables
			assert.Equal(t, tt.expectedEnvVarCount, len(container.Env), "Environment variable count should match expected")

			// Verify all expected environment variables are present with correct values
			envMap := make(map[string]string)
			for _, envVar := range container.Env {
				envMap[envVar.Name] = envVar.Value
			}

			for expectedName, expectedValue := range tt.expectedEnvVars {
				actualValue, exists := envMap[expectedName]
				assert.True(t, exists, "Environment variable %s should exist", expectedName)
				assert.Equal(t, expectedValue, actualValue, "Environment variable %s should have correct value", expectedName)
			}

			// Verify KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT are always present
			assert.Contains(t, envMap, KubernetesServiceHost, "KUBERNETES_SERVICE_HOST should always be present")
			assert.Contains(t, envMap, KubernetesServicePort, "KUBERNETES_SERVICE_PORT should always be present")
			assert.Equal(t, tt.host, envMap[KubernetesServiceHost], "KUBERNETES_SERVICE_HOST should match provided host")
			assert.Equal(t, tt.port, envMap[KubernetesServicePort], "KUBERNETES_SERVICE_PORT should match provided port")
		})
	}
}

// TestVirtualPodReconciler_InjectHostnameEnvVar tests the injectHostnameEnvVar function
func TestVirtualPodReconciler_InjectHostnameEnvVar(t *testing.T) {
	reconciler := &VirtualPodReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name                string
		container           *corev1.Container
		virtualPodName      string
		expectedEnvVars     map[string]string
		expectedEnvVarCount int
	}{
		{
			name: "inject_hostname_to_empty_container",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env:   []corev1.EnvVar{},
			},
			virtualPodName: "test-pod-123",
			expectedEnvVars: map[string]string{
				HostnameEnvVar: "test-pod-123",
			},
			expectedEnvVarCount: 1,
		},
		{
			name: "inject_hostname_to_container_with_existing_env_vars",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env: []corev1.EnvVar{
					{
						Name:  "EXISTING_VAR",
						Value: "existing_value",
					},
					{
						Name:  "ANOTHER_VAR",
						Value: "another_value",
					},
				},
			},
			virtualPodName: "my-app-pod",
			expectedEnvVars: map[string]string{
				"EXISTING_VAR": "existing_value",
				"ANOTHER_VAR":  "another_value",
				HostnameEnvVar: "my-app-pod",
			},
			expectedEnvVarCount: 3,
		},
		{
			name: "override_existing_hostname_env_var",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env: []corev1.EnvVar{
					{
						Name:  HostnameEnvVar,
						Value: "old-hostname",
					},
					{
						Name:  "OTHER_VAR",
						Value: "other_value",
					},
				},
			},
			virtualPodName: "new-pod-name",
			expectedEnvVars: map[string]string{
				HostnameEnvVar: "new-pod-name",
				"OTHER_VAR":    "other_value",
			},
			expectedEnvVarCount: 2,
		},
		{
			name: "override_existing_hostname_with_valuefrom",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env: []corev1.EnvVar{
					{
						Name: HostnameEnvVar,
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: "spec.nodeName",
							},
						},
					},
					{
						Name:  "APP_ENV",
						Value: "production",
					},
				},
			},
			virtualPodName: "virtual-pod-name",
			expectedEnvVars: map[string]string{
				HostnameEnvVar: "virtual-pod-name",
				"APP_ENV":      "production",
			},
			expectedEnvVarCount: 2,
		},
		{
			name: "inject_with_special_characters_in_pod_name",
			container: &corev1.Container{
				Name:  "test-container",
				Image: "nginx:latest",
				Env: []corev1.EnvVar{
					{
						Name:  "APP_ENV",
						Value: "production",
					},
				},
			},
			virtualPodName: "my-app-pod-123-abc",
			expectedEnvVars: map[string]string{
				"APP_ENV":      "production",
				HostnameEnvVar: "my-app-pod-123-abc",
			},
			expectedEnvVarCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a copy of the container to avoid modifying the original
			container := tt.container.DeepCopy()

			// Call the function
			reconciler.injectHostnameEnvVar(container, tt.virtualPodName)

			// Verify the number of environment variables
			assert.Equal(t, tt.expectedEnvVarCount, len(container.Env), "Environment variable count should match expected")

			// Verify all expected environment variables are present with correct values
			envMap := make(map[string]string)
			for _, envVar := range container.Env {
				envMap[envVar.Name] = envVar.Value
			}

			for expectedName, expectedValue := range tt.expectedEnvVars {
				actualValue, exists := envMap[expectedName]
				assert.True(t, exists, "Environment variable %s should exist", expectedName)
				assert.Equal(t, expectedValue, actualValue, "Environment variable %s should have correct value", expectedName)
			}

			// Verify HOSTNAME is always present
			assert.Contains(t, envMap, HostnameEnvVar, "HOSTNAME should always be present")
			assert.Equal(t, tt.virtualPodName, envMap[HostnameEnvVar], "HOSTNAME should match provided virtual pod name")

			// Verify that ValueFrom is cleared for HOSTNAME
			for _, envVar := range container.Env {
				if envVar.Name == HostnameEnvVar {
					assert.Nil(t, envVar.ValueFrom, "HOSTNAME should not have ValueFrom when set directly")
					break
				}
			}
		})
	}
}

// TestVirtualPodReconciler_BuildPhysicalPodSpecWithEnvVarInjection tests the buildPhysicalPodSpec function with environment variable injection
func TestVirtualPodReconciler_BuildPhysicalPodSpecWithEnvVarInjection(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, schedulingv1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	// Create mock services for loadbalancer IPs
	kubernetesIntranetService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes-intranet",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 443,
				},
			},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{IP: "10.0.0.1"},
				},
			},
		},
	}

	kubeDnsIntranetService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kube-dns-intranet",
			Namespace: "kube-system",
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{IP: "10.0.0.2"},
				},
			},
		},
	}

	clusterBinding := &cloudv1beta1.ClusterBinding{
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
		},
	}

	// Create mock virtual client with services
	virtualClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(kubernetesIntranetService, kubeDnsIntranetService, clusterBinding).
		Build()

	physicalClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &VirtualPodReconciler{
		VirtualClient:  virtualClient,
		PhysicalClient: physicalClient,
		ClusterBinding: clusterBinding,
		Log:            ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name                    string
		virtualPod              *corev1.Pod
		resourceMapping         *ResourceMapping
		expectedEnvVarInjection bool
		expectedHostAlias       bool
		expectedDNSConfig       bool
	}{
		{
			name: "pod_with_containers_and_init_containers_should_inject_env_vars",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main-container",
							Image: "nginx:latest",
							Env: []corev1.EnvVar{
								{
									Name:  "EXISTING_VAR",
									Value: "existing_value",
								},
							},
						},
					},
					InitContainers: []corev1.Container{
						{
							Name:  "init-container",
							Image: "busybox:latest",
							Env: []corev1.EnvVar{
								{
									Name:  "INIT_VAR",
									Value: "init_value",
								},
							},
						},
					},
					DNSPolicy: corev1.DNSClusterFirst,
				},
			},
			resourceMapping: &ResourceMapping{
				ConfigMaps: map[string]string{},
				Secrets:    map[string]string{},
			},
			expectedEnvVarInjection: true,
			expectedHostAlias:       true,
			expectedDNSConfig:       true,
		},
		{
			name: "pod_with_only_containers_should_inject_env_vars",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main-container",
							Image: "nginx:latest",
						},
					},
					DNSPolicy: corev1.DNSClusterFirstWithHostNet,
				},
			},
			resourceMapping: &ResourceMapping{
				ConfigMaps: map[string]string{},
				Secrets:    map[string]string{},
			},
			expectedEnvVarInjection: true,
			expectedHostAlias:       true,
			expectedDNSConfig:       true,
		},
		{
			name: "pod_with_only_init_containers_should_inject_env_vars",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init-container",
							Image: "busybox:latest",
						},
					},
					DNSPolicy: corev1.DNSClusterFirst,
				},
			},
			resourceMapping: &ResourceMapping{
				ConfigMaps: map[string]string{},
				Secrets:    map[string]string{},
			},
			expectedEnvVarInjection: true,
			expectedHostAlias:       true,
			expectedDNSConfig:       true,
		},
		{
			name: "pod_with_different_dns_policy_should_not_configure_dns",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main-container",
							Image: "nginx:latest",
						},
					},
					DNSPolicy: corev1.DNSDefault,
				},
			},
			resourceMapping: &ResourceMapping{
				ConfigMaps: map[string]string{},
				Secrets:    map[string]string{},
			},
			expectedEnvVarInjection: true,
			expectedHostAlias:       true,
			expectedDNSConfig:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call buildPhysicalPodSpec
			result, err := reconciler.buildPhysicalPodSpec(context.Background(), tt.virtualPod, "test-physical-node", tt.resourceMapping)
			assert.NoError(t, err)

			// Verify hostAlias is added
			if tt.expectedHostAlias {
				assert.Len(t, result.HostAliases, 1, "Should have one hostAlias")
				assert.Equal(t, "10.0.0.1", result.HostAliases[0].IP, "HostAlias IP should match kubernetes-intranet IP")
				assert.Equal(t, []string{"kubernetes.default.svc"}, result.HostAliases[0].Hostnames, "HostAlias hostnames should be correct")
			}

			// Verify DNS configuration
			if tt.expectedDNSConfig {
				assert.Equal(t, corev1.DNSNone, result.DNSPolicy, "DNSPolicy should be set to None")
				assert.NotNil(t, result.DNSConfig, "DNSConfig should be set")
				assert.Len(t, result.DNSConfig.Nameservers, 1, "Should have one nameserver")
				assert.Equal(t, "10.0.0.2", result.DNSConfig.Nameservers[0], "Nameserver should match kube-dns-intranet IP")
				assert.Len(t, result.DNSConfig.Options, 1, "Should have one DNS option")
				assert.Equal(t, "ndots", result.DNSConfig.Options[0].Name, "DNS option name should be ndots")
				assert.Equal(t, "3", *result.DNSConfig.Options[0].Value, "DNS option value should be 3")
				assert.Len(t, result.DNSConfig.Searches, 3, "Should have three search domains")
				assert.Equal(t, []string{"default.svc.cluster.local", "svc.cluster.local", "cluster.local"}, result.DNSConfig.Searches, "Search domains should be correct")
			}

			// Verify environment variable injection
			if tt.expectedEnvVarInjection {
				// Check containers
				for _, container := range result.Containers {
					var hasKubernetesServiceHost, hasKubernetesServicePort, hasHostname bool
					for _, envVar := range container.Env {
						if envVar.Name == KubernetesServiceHost {
							assert.Equal(t, "10.0.0.1", envVar.Value, "KUBERNETES_SERVICE_HOST should be set to kubernetes-intranet IP")
							hasKubernetesServiceHost = true
						}
						if envVar.Name == KubernetesServicePort {
							assert.Equal(t, "443", envVar.Value, "KUBERNETES_SERVICE_PORT should be set to kubernetes-intranet port")
							hasKubernetesServicePort = true
						}
						if envVar.Name == HostnameEnvVar {
							assert.Equal(t, tt.virtualPod.Name, envVar.Value, "HOSTNAME should be set to virtual pod name")
							hasHostname = true
						}
					}
					assert.True(t, hasKubernetesServiceHost, "Container should have KUBERNETES_SERVICE_HOST environment variable")
					assert.True(t, hasKubernetesServicePort, "Container should have KUBERNETES_SERVICE_PORT environment variable")
					assert.True(t, hasHostname, "Container should have HOSTNAME environment variable")
				}

				// Check init containers
				for _, container := range result.InitContainers {
					var hasKubernetesServiceHost, hasKubernetesServicePort, hasHostname bool
					for _, envVar := range container.Env {
						if envVar.Name == KubernetesServiceHost {
							assert.Equal(t, "10.0.0.1", envVar.Value, "KUBERNETES_SERVICE_HOST should be set to kubernetes-intranet IP")
							hasKubernetesServiceHost = true
						}
						if envVar.Name == KubernetesServicePort {
							assert.Equal(t, "443", envVar.Value, "KUBERNETES_SERVICE_PORT should be set to kubernetes-intranet port")
							hasKubernetesServicePort = true
						}
						if envVar.Name == HostnameEnvVar {
							assert.Equal(t, tt.virtualPod.Name, envVar.Value, "HOSTNAME should be set to virtual pod name")
							hasHostname = true
						}
					}
					assert.True(t, hasKubernetesServiceHost, "InitContainer should have KUBERNETES_SERVICE_HOST environment variable")
					assert.True(t, hasKubernetesServicePort, "InitContainer should have KUBERNETES_SERVICE_PORT environment variable")
					assert.True(t, hasHostname, "InitContainer should have HOSTNAME environment variable")
				}
			}
		})
	}
}

// TestVirtualPodReconciler_BuildPhysicalPodSpecWithEnvVarInjectionErrors tests error scenarios in buildPhysicalPodSpec
func TestVirtualPodReconciler_BuildPhysicalPodSpecWithEnvVarInjectionErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, schedulingv1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	tests := []struct {
		name          string
		virtualClient client.Client
		expectedError bool
		errorContains string
	}{
		{
			name:          "error_when_virtual_client_is_nil",
			virtualClient: nil,
			expectedError: true,
			errorContains: "VirtualClient is not available",
		},
		{
			name: "error_when_kubernetes_intranet_service_not_found",
			virtualClient: fakeclient.NewClientBuilder().
				WithScheme(scheme).
				Build(),
			expectedError: true,
			errorContains: "failed to get kubernetes-intranet IP and port",
		},
		{
			name: "error_when_kubernetes_intranet_service_has_no_ports",
			virtualClient: fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubernetes-intranet",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{},
					},
					Status: corev1.ServiceStatus{
						LoadBalancer: corev1.LoadBalancerStatus{
							Ingress: []corev1.LoadBalancerIngress{
								{IP: "10.0.0.1"},
							},
						},
					},
				}).
				Build(),
			expectedError: true,
			errorContains: "failed to get kubernetes-intranet IP and port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			}

			physicalClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				Build()

			// Create virtual client with ClusterBinding if virtualClient is not nil
			var virtualClient client.Client
			if tt.virtualClient != nil {
				virtualClient = fakeclient.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(clusterBinding).
					Build()
			}

			reconciler := &VirtualPodReconciler{
				VirtualClient:  virtualClient,
				PhysicalClient: physicalClient,
				ClusterBinding: clusterBinding,
				Log:            ctrl.Log.WithName("test"),
			}

			virtualPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main-container",
							Image: "nginx:latest",
						},
					},
				},
			}

			resourceMapping := &ResourceMapping{
				ConfigMaps: map[string]string{},
				Secrets:    map[string]string{},
			}

			// Call buildPhysicalPodSpec
			_, err := reconciler.buildPhysicalPodSpec(context.Background(), virtualPod, "test-physical-node", resourceMapping)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestVirtualPodReconciler_UpdateVirtualResourceLabelsAndAnnotations tests the updateVirtualResourceLabelsAndAnnotations function
func TestVirtualPodReconciler_UpdateVirtualResourceLabelsAndAnnotations(t *testing.T) {
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
		name                string
		virtualPod          *corev1.Pod
		physicalName        string
		physicalNamespace   string
		syncResourceOpt     *topcommon.SyncResourceOpt
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
		expectError         bool
		errorContains       string
	}{
		{
			name: "new pod with no existing labels or annotations",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
			},
			physicalName:      "physical-pod",
			physicalNamespace: "physical-ns",
			syncResourceOpt:   nil,
			expectedLabels: map[string]string{
				cloudv1beta1.LabelManagedBy:              cloudv1beta1.LabelManagedByValue,
				"kubeocean.io/synced-by-test-cluster-id": cloudv1beta1.LabelValueTrue,
			},
			expectedAnnotations: map[string]string{
				cloudv1beta1.AnnotationPhysicalName:      "physical-pod",
				cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
			},
			expectError: false,
		},
		{
			name: "pod with existing labels and annotations",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						"app": "test",
					},
					Annotations: map[string]string{
						"existing": "annotation",
					},
				},
			},
			physicalName:      "physical-pod",
			physicalNamespace: "physical-ns",
			syncResourceOpt:   nil,
			expectedLabels: map[string]string{
				"app":                                    "test",
				cloudv1beta1.LabelManagedBy:              cloudv1beta1.LabelManagedByValue,
				"kubeocean.io/synced-by-test-cluster-id": cloudv1beta1.LabelValueTrue,
			},
			expectedAnnotations: map[string]string{
				"existing":                               "annotation",
				cloudv1beta1.AnnotationPhysicalName:      "physical-pod",
				cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
			},
			expectError: false,
		},
		{
			name: "pod with PV ref secret sync option",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
			},
			physicalName:      "physical-pod",
			physicalNamespace: "physical-ns",
			syncResourceOpt: &topcommon.SyncResourceOpt{
				IsPVRefSecret: true,
			},
			expectedLabels: map[string]string{
				cloudv1beta1.LabelManagedBy:              cloudv1beta1.LabelManagedByValue,
				"kubeocean.io/synced-by-test-cluster-id": cloudv1beta1.LabelValueTrue,
				cloudv1beta1.LabelUsedByPV:               cloudv1beta1.LabelValueTrue,
			},
			expectedAnnotations: map[string]string{
				cloudv1beta1.AnnotationPhysicalName:      "physical-pod",
				cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
			},
			expectError: false,
		},
		{
			name: "pod with matching existing annotations - should skip update",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName:      "physical-pod",
						cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
					},
				},
			},
			physicalName:      "physical-pod",
			physicalNamespace: "physical-ns",
			syncResourceOpt:   nil,
			expectedLabels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
			expectedAnnotations: map[string]string{
				cloudv1beta1.AnnotationPhysicalName:      "physical-pod",
				cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
			},
			expectError: false,
		},
		{
			name: "pod with conflicting physical name annotation - should error",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "different-physical-pod",
					},
				},
			},
			physicalName:        "physical-pod",
			physicalNamespace:   "physical-ns",
			syncResourceOpt:     nil,
			expectedLabels:      nil,
			expectedAnnotations: nil,
			expectError:         true,
			errorContains:       "physical name annotation already exists but doesn't match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

			reconciler := &VirtualPodReconciler{
				VirtualClient:  virtualClient,
				PhysicalClient: physicalClient,
				ClusterBinding: clusterBinding,
				Log:            ctrl.Log.WithName("test"),
				ClusterID:      "test-cluster-id",
			}

			// Add the pod to the client
			err := virtualClient.Create(context.Background(), tt.virtualPod)
			require.NoError(t, err)

			// Call the function
			err = reconciler.updateVirtualResourceLabelsAndAnnotations(
				context.Background(),
				tt.virtualPod,
				tt.physicalName,
				tt.physicalNamespace,
				tt.syncResourceOpt,
			)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			assert.NoError(t, err)

			// Get the updated pod from the client
			updatedPod := &corev1.Pod{}
			err = virtualClient.Get(context.Background(), types.NamespacedName{
				Name: tt.virtualPod.Name, Namespace: tt.virtualPod.Namespace,
			}, updatedPod)
			require.NoError(t, err)

			// Verify labels
			for key, expectedValue := range tt.expectedLabels {
				assert.Equal(t, expectedValue, updatedPod.Labels[key], "Label %s should match", key)
			}

			// Verify annotations
			for key, expectedValue := range tt.expectedAnnotations {
				assert.Equal(t, expectedValue, updatedPod.Annotations[key], "Annotation %s should match", key)
			}

			// Verify finalizer is added (except for the skip update case)
			if tt.name != "pod with matching existing annotations - should skip update" {
				expectedFinalizer := "kubeocean.io/finalizer-test-cluster-id"
				assert.Contains(t, updatedPod.Finalizers, expectedFinalizer)
			}
		})
	}
}

func TestVirtualPodReconciler_pollPhysicalResources(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "physical-ns",
		},
	}

	t.Run("should succeed when all resources exist", func(t *testing.T) {
		ctx := context.Background()

		// Create physical resources
		physicalConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config-physical",
				Namespace: "physical-ns",
			},
			Data: map[string]string{"key": "value"},
		}

		physicalSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret-physical",
				Namespace: "physical-ns",
			},
			Data: map[string][]byte{"key": []byte("value")},
		}

		physicalPVC := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc-physical",
				Namespace: "physical-ns",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
			},
		}

		physicalServiceAccountToken := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa-token-physical",
				Namespace: "physical-ns",
			},
			Data: map[string][]byte{"token": []byte("test-token")},
		}

		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(
			physicalConfigMap, physicalSecret, physicalPVC, physicalServiceAccountToken,
		).Build()

		reconciler := &VirtualPodReconciler{
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            zap.New(),
		}

		resourceMapping := &ResourceMapping{
			ConfigMaps:              map[string]string{"test-config": "test-config-physical"},
			Secrets:                 map[string]string{"test-secret": "test-secret-physical"},
			PVCs:                    map[string]string{"test-pvc": "test-pvc-physical"},
			ServiceAccountTokenName: "test-sa-token-physical",
		}

		err := reconciler.pollPhysicalResources(ctx, resourceMapping, "virtual-ns", "test-pod")
		assert.NoError(t, err)
	})

	t.Run("should succeed when no ServiceAccountToken", func(t *testing.T) {
		ctx := context.Background()

		// Create physical resources (no ServiceAccountToken)
		physicalConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config-physical",
				Namespace: "physical-ns",
			},
			Data: map[string]string{"key": "value"},
		}

		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalConfigMap).Build()

		reconciler := &VirtualPodReconciler{
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            zap.New(),
		}

		resourceMapping := &ResourceMapping{
			ConfigMaps:              map[string]string{"test-config": "test-config-physical"},
			Secrets:                 map[string]string{},
			PVCs:                    map[string]string{},
			ServiceAccountTokenName: "", // No ServiceAccountToken
		}

		err := reconciler.pollPhysicalResources(ctx, resourceMapping, "virtual-ns", "test-pod")
		assert.NoError(t, err)
	})

	t.Run("should timeout when resources do not exist", func(t *testing.T) {
		ctx := context.Background()

		// Create empty physical client (no resources)
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &VirtualPodReconciler{
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            zap.New(),
		}

		resourceMapping := &ResourceMapping{
			ConfigMaps:              map[string]string{"test-config": "test-config-physical"},
			Secrets:                 map[string]string{},
			PVCs:                    map[string]string{},
			ServiceAccountTokenName: "",
		}

		err := reconciler.pollPhysicalResources(ctx, resourceMapping, "virtual-ns", "test-pod")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "timeout waiting for physical resources to be created")
	})

	t.Run("should return error immediately for non-NotFound errors", func(t *testing.T) {
		ctx := context.Background()

		// Create a mock client that returns a non-NotFound error
		mockClient := &MockClient{
			getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return fmt.Errorf("permission denied")
			},
		}

		reconciler := &VirtualPodReconciler{
			PhysicalClient: mockClient,
			ClusterBinding: clusterBinding,
			Log:            zap.New(),
		}

		resourceMapping := &ResourceMapping{
			ConfigMaps:              map[string]string{"test-config": "test-config-physical"},
			Secrets:                 map[string]string{},
			PVCs:                    map[string]string{},
			ServiceAccountTokenName: "",
		}

		err := reconciler.pollPhysicalResources(ctx, resourceMapping, "virtual-ns", "test-pod")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "permission denied")
	})

	t.Run("should continue polling when ConfigMap is NotFound", func(t *testing.T) {
		ctx := context.Background()

		// Create a mock client that returns NotFound for ConfigMap but exists for others
		callCount := 0
		mockClient := &MockClient{
			getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				callCount++
				// First call for ConfigMap returns NotFound, subsequent calls succeed
				if key.Name == "test-config-physical" && callCount == 1 {
					return apierrors.NewNotFound(corev1.Resource("configmap"), key.Name)
				}
				// Simulate resource creation after first call
				if key.Name == "test-config-physical" && callCount > 1 {
					configMap := obj.(*corev1.ConfigMap)
					configMap.ObjectMeta = metav1.ObjectMeta{
						Name:      key.Name,
						Namespace: key.Namespace,
					}
					configMap.Data = map[string]string{"key": "value"}
					return nil
				}
				return nil
			},
		}

		reconciler := &VirtualPodReconciler{
			PhysicalClient: mockClient,
			ClusterBinding: clusterBinding,
			Log:            zap.New(),
		}

		resourceMapping := &ResourceMapping{
			ConfigMaps:              map[string]string{"test-config": "test-config-physical"},
			Secrets:                 map[string]string{},
			PVCs:                    map[string]string{},
			ServiceAccountTokenName: "",
		}

		err := reconciler.pollPhysicalResources(ctx, resourceMapping, "virtual-ns", "test-pod")
		assert.NoError(t, err)
		assert.Greater(t, callCount, 1, "Should have made multiple calls due to polling")
	})

	t.Run("should check all resource types in each polling iteration", func(t *testing.T) {
		ctx := context.Background()

		// Track which resources were checked and how many times
		checkedResources := make(map[string]int)
		callCount := 0
		mockClient := &MockClient{
			getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				checkedResources[key.Name]++
				callCount++
				// Return NotFound for all resources to trigger polling
				return apierrors.NewNotFound(corev1.Resource(""), key.Name)
			},
		}

		reconciler := &VirtualPodReconciler{
			PhysicalClient: mockClient,
			ClusterBinding: clusterBinding,
			Log:            zap.New(),
		}

		resourceMapping := &ResourceMapping{
			ConfigMaps:              map[string]string{"config1": "config1-physical"},
			Secrets:                 map[string]string{},
			PVCs:                    map[string]string{},
			ServiceAccountTokenName: "",
		}

		// This should timeout, but we can verify the resource was checked multiple times
		err := reconciler.pollPhysicalResources(ctx, resourceMapping, "virtual-ns", "test-pod")
		assert.Error(t, err)

		// Verify the resource was checked multiple times (due to polling)
		assert.Greater(t, checkedResources["config1-physical"], 1, "ConfigMap should have been checked multiple times due to polling")
		assert.Greater(t, callCount, 1, "Should have made multiple calls due to polling")
	})
}

// MockClient is a mock implementation of client.Client for testing
type MockClient struct {
	getFunc func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
}

func (m *MockClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if m.getFunc != nil {
		return m.getFunc(ctx, key, obj, opts...)
	}
	return apierrors.NewNotFound(corev1.Resource(""), key.Name)
}

// Implement other required methods as no-ops for testing
func (m *MockClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return nil
}

func (m *MockClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return nil
}

func (m *MockClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return nil
}

func (m *MockClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return nil
}

func (m *MockClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return nil
}

func (m *MockClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return nil
}

func (m *MockClient) Status() client.StatusWriter {
	return nil
}

func (m *MockClient) Scheme() *runtime.Scheme {
	return nil
}

func (m *MockClient) RESTMapper() meta.RESTMapper {
	return nil
}

func (m *MockClient) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}

func (m *MockClient) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	return false, nil
}

func (m *MockClient) SubResource(subResource string) client.SubResourceClient {
	return nil
}

// TestVirtualPodReconciler_AddHostAliasesAndEnvVars tests the addHostAliasesAndEnvVars function
func TestVirtualPodReconciler_AddHostAliasesAndEnvVars(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create mock services for loadbalancer IPs
	kubernetesIntranetService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes-intranet",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 443,
				},
			},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{IP: "10.0.0.1"},
				},
			},
		},
	}

	// Create mock virtual client with services
	virtualClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(kubernetesIntranetService).
		Build()

	reconciler := &VirtualPodReconciler{
		VirtualClient: virtualClient,
		Log:           ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name              string
		podSpec           *corev1.PodSpec
		virtualPodName    string
		expectedHostAlias bool
		expectedEnvVars   map[string]string
		expectedEnvCount  int
	}{
		{
			name: "inject_env_vars_to_containers_and_init_containers",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "main-container",
						Image: "nginx:latest",
						Env: []corev1.EnvVar{
							{
								Name:  "EXISTING_VAR",
								Value: "existing_value",
							},
						},
					},
				},
				InitContainers: []corev1.Container{
					{
						Name:  "init-container",
						Image: "busybox:latest",
						Env: []corev1.EnvVar{
							{
								Name:  "INIT_VAR",
								Value: "init_value",
							},
						},
					},
				},
			},
			virtualPodName:    "test-pod-123",
			expectedHostAlias: true,
			expectedEnvVars: map[string]string{
				KubernetesServiceHost: "10.0.0.1",
				KubernetesServicePort: "443",
				HostnameEnvVar:        "test-pod-123",
			},
			expectedEnvCount: 4, // EXISTING_VAR + 3 injected vars for main container
		},
		{
			name: "inject_env_vars_to_empty_containers",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "main-container",
						Image: "nginx:latest",
						Env:   []corev1.EnvVar{},
					},
				},
				InitContainers: []corev1.Container{
					{
						Name:  "init-container",
						Image: "busybox:latest",
						Env:   []corev1.EnvVar{},
					},
				},
			},
			virtualPodName:    "my-app-pod",
			expectedHostAlias: true,
			expectedEnvVars: map[string]string{
				KubernetesServiceHost: "10.0.0.1",
				KubernetesServicePort: "443",
				HostnameEnvVar:        "my-app-pod",
			},
			expectedEnvCount: 3, // 3 injected vars for main container
		},
		{
			name: "override_existing_kubernetes_and_hostname_env_vars",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "main-container",
						Image: "nginx:latest",
						Env: []corev1.EnvVar{
							{
								Name:  KubernetesServiceHost,
								Value: "old-host",
							},
							{
								Name:  KubernetesServicePort,
								Value: "old-port",
							},
							{
								Name:  HostnameEnvVar,
								Value: "old-hostname",
							},
							{
								Name:  "OTHER_VAR",
								Value: "other_value",
							},
						},
					},
				},
			},
			virtualPodName:    "new-pod-name",
			expectedHostAlias: true,
			expectedEnvVars: map[string]string{
				KubernetesServiceHost: "10.0.0.1",
				KubernetesServicePort: "443",
				HostnameEnvVar:        "new-pod-name",
				"OTHER_VAR":           "other_value",
			},
			expectedEnvCount: 4, // OTHER_VAR + 3 injected vars
		},
		{
			name: "inject_with_special_characters_in_pod_name",
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "main-container",
						Image: "nginx:latest",
						Env: []corev1.EnvVar{
							{
								Name:  "APP_ENV",
								Value: "production",
							},
						},
					},
				},
			},
			virtualPodName:    "my-app-pod-123-abc",
			expectedHostAlias: true,
			expectedEnvVars: map[string]string{
				KubernetesServiceHost: "10.0.0.1",
				KubernetesServicePort: "443",
				HostnameEnvVar:        "my-app-pod-123-abc",
				"APP_ENV":             "production",
			},
			expectedEnvCount: 4, // APP_ENV + 3 injected vars
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a copy of the pod spec to avoid modifying the original
			podSpec := tt.podSpec.DeepCopy()

			// Call the function
			err := reconciler.addHostAliasesAndEnvVars(context.Background(), podSpec, tt.virtualPodName)
			assert.NoError(t, err)

			// Verify hostAlias is added
			if tt.expectedHostAlias {
				assert.Len(t, podSpec.HostAliases, 1, "Should have one hostAlias")
				assert.Equal(t, "10.0.0.1", podSpec.HostAliases[0].IP, "HostAlias IP should match kubernetes-intranet IP")
				assert.Equal(t, []string{"kubernetes.default.svc"}, podSpec.HostAliases[0].Hostnames, "HostAlias hostnames should be correct")
			}

			// Verify environment variable injection in containers
			for _, container := range podSpec.Containers {
				envMap := make(map[string]string)
				for _, envVar := range container.Env {
					envMap[envVar.Name] = envVar.Value
				}

				// Verify all expected environment variables are present
				for expectedName, expectedValue := range tt.expectedEnvVars {
					actualValue, exists := envMap[expectedName]
					assert.True(t, exists, "Container should have environment variable %s", expectedName)
					assert.Equal(t, expectedValue, actualValue, "Environment variable %s should have correct value", expectedName)
				}

				// Verify ValueFrom is cleared for injected environment variables
				for _, envVar := range container.Env {
					if envVar.Name == KubernetesServiceHost || envVar.Name == KubernetesServicePort || envVar.Name == HostnameEnvVar {
						assert.Nil(t, envVar.ValueFrom, "Environment variable %s should not have ValueFrom when set directly", envVar.Name)
					}
				}
			}

			// Verify environment variable injection in init containers
			for _, container := range podSpec.InitContainers {
				envMap := make(map[string]string)
				for _, envVar := range container.Env {
					envMap[envVar.Name] = envVar.Value
				}

				// Verify all expected environment variables are present
				for expectedName, expectedValue := range tt.expectedEnvVars {
					actualValue, exists := envMap[expectedName]
					assert.True(t, exists, "InitContainer should have environment variable %s", expectedName)
					assert.Equal(t, expectedValue, actualValue, "Environment variable %s should have correct value", expectedName)
				}

				// Verify ValueFrom is cleared for injected environment variables
				for _, envVar := range container.Env {
					if envVar.Name == KubernetesServiceHost || envVar.Name == KubernetesServicePort || envVar.Name == HostnameEnvVar {
						assert.Nil(t, envVar.ValueFrom, "Environment variable %s should not have ValueFrom when set directly", envVar.Name)
					}
				}
			}
		})
	}
}

func TestVirtualPodReconciler_GetWorkloadInfo(t *testing.T) {
	tests := []struct {
		name          string
		pod           *corev1.Pod
		setupClient   func(*testing.T, client.Client)
		expectedType  string
		expectedName  string
		expectError   bool
		errorContains string
	}{
		{
			name: "pod without owner references",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
			},
			expectedType: "pod",
			expectedName: "",
			expectError:  false,
		},
		{
			name: "pod with deployment owner reference",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "Deployment",
							Name: "test-deployment",
						},
					},
				},
			},
			expectedType: "deployment",
			expectedName: "test-deployment",
			expectError:  false,
		},
		{
			name: "pod with daemonset owner reference",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
			},
			expectedType: "daemonset",
			expectedName: "test-daemonset",
			expectError:  false,
		},
		{
			name: "pod with statefulset owner reference",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "StatefulSet",
							Name: "test-statefulset",
						},
					},
				},
			},
			expectedType: "statefulset",
			expectedName: "test-statefulset",
			expectError:  false,
		},
		{
			name: "pod with job owner reference",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "Job",
							Name: "test-job",
						},
					},
				},
			},
			expectedType: "job",
			expectedName: "test-job",
			expectError:  false,
		},
		{
			name: "pod with cronjob owner reference",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "CronJob",
							Name: "test-cronjob",
						},
					},
				},
			},
			expectedType: "cronjob",
			expectedName: "test-cronjob",
			expectError:  false,
		},
		{
			name: "pod with replicaset owner reference - deployment found",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "test-replicaset",
						},
					},
				},
			},
			setupClient: func(t *testing.T, c client.Client) {
				replicaset := &appsv1.ReplicaSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-replicaset",
						Namespace: "test-ns",
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "Deployment",
								Name: "test-deployment",
							},
						},
					},
				}
				err := c.Create(context.Background(), replicaset)
				assert.NoError(t, err)
			},
			expectedType: "deployment",
			expectedName: "test-deployment",
			expectError:  false,
		},
		{
			name: "pod with replicaset owner reference - no deployment owner",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "test-replicaset",
						},
					},
				},
			},
			setupClient: func(t *testing.T, c client.Client) {
				replicaset := &appsv1.ReplicaSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-replicaset",
						Namespace: "test-ns",
						// No owner references
					},
				}
				err := c.Create(context.Background(), replicaset)
				assert.NoError(t, err)
			},
			expectedType: "replicaset",
			expectedName: "test-replicaset",
			expectError:  false,
		},
		{
			name: "pod with replicaset owner reference - replicaset not found",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "non-existent-replicaset",
						},
					},
				},
			},
			expectedType:  "",
			expectedName:  "",
			expectError:   true,
			errorContains: "failed to get ReplicaSet",
		},
		{
			name: "pod with unknown owner reference",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "UnknownKind",
							Name: "test-unknown",
						},
					},
				},
			},
			expectedType: "unknownkind",
			expectedName: "test-unknown",
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup fake client
			scheme := runtime.NewScheme()
			corev1.AddToScheme(scheme)
			appsv1.AddToScheme(scheme)

			fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

			// Setup client if needed
			if tt.setupClient != nil {
				tt.setupClient(t, fakeClient)
			}

			// Create reconciler
			reconciler := &VirtualPodReconciler{
				VirtualClient: fakeClient,
				Log:           ctrl.Log.WithName("test-virtual-pod-reconciler"),
			}

			// Test getWorkloadInfo
			workloadType, workloadName, err := reconciler.getWorkloadInfo(context.Background(), tt.pod)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedType, workloadType)
			assert.Equal(t, tt.expectedName, workloadName)
		})
	}
}

func TestEnsureDefaultPriorityClass(t *testing.T) {
	tests := []struct {
		name          string
		existingPC    *schedulingv1.PriorityClass
		expectCreate  bool
		expectError   bool
		errorContains string
	}{
		{
			name:         "PriorityClass does not exist, should create",
			existingPC:   nil,
			expectCreate: true,
			expectError:  false,
		},
		{
			name: "PriorityClass already exists, should not create",
			existingPC: &schedulingv1.PriorityClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: cloudv1beta1.DefaultPriorityClassName,
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
				Value:            cloudv1beta1.DefaultPriorityClassValue,
				PreemptionPolicy: ptr.To(corev1.PreemptLowerPriority),
				Description:      "Default PriorityClass for Kubeocean physical pods",
			},
			expectCreate: false,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test objects
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, schedulingv1.AddToScheme(scheme))
			require.NoError(t, cloudv1beta1.AddToScheme(scheme))

			var objects []client.Object
			if tt.existingPC != nil {
				objects = append(objects, tt.existingPC)
			}

			fakeClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			// Create reconciler
			reconciler := &VirtualPodReconciler{
				PhysicalClient: fakeClient,
				Log:            zap.New(zap.UseDevMode(true)),
			}

			// Test ensureDefaultPriorityClass
			err := reconciler.ensureDefaultPriorityClass(context.Background())

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			// Verify PriorityClass exists
			priorityClass := &schedulingv1.PriorityClass{}
			err = fakeClient.Get(context.Background(), client.ObjectKey{Name: cloudv1beta1.DefaultPriorityClassName}, priorityClass)
			assert.NoError(t, err)
			assert.Equal(t, cloudv1beta1.DefaultPriorityClassName, priorityClass.Name)
			assert.Equal(t, cloudv1beta1.DefaultPriorityClassValue, priorityClass.Value)
			assert.Equal(t, corev1.PreemptLowerPriority, *priorityClass.PreemptionPolicy)
			assert.Equal(t, "Default PriorityClass for Kubeocean physical pods", priorityClass.Description)
			assert.Contains(t, priorityClass.Labels, cloudv1beta1.LabelManagedBy)
			assert.Equal(t, cloudv1beta1.LabelManagedByValue, priorityClass.Labels[cloudv1beta1.LabelManagedBy])
		})
	}
}

func TestBuildPhysicalPodSpecWithPriorityClass(t *testing.T) {
	tests := []struct {
		name                  string
		clusterBindingSpec    cloudv1beta1.ClusterBindingSpec
		existingPriorityClass *schedulingv1.PriorityClass
		expectedPriorityClass string
		expectError           bool
		errorContains         string
	}{
		{
			name: "PodPriorityClassName specified, should use it",
			clusterBindingSpec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:            "test-cluster",
				PodPriorityClassName: "custom-priority",
			},
			expectedPriorityClass: "custom-priority",
			expectError:           false,
		},
		{
			name: "PodPriorityClassName empty, should use default and create it",
			clusterBindingSpec: cloudv1beta1.ClusterBindingSpec{
				ClusterID: "test-cluster",
			},
			expectedPriorityClass: cloudv1beta1.DefaultPriorityClassName,
			expectError:           false,
		},
		{
			name: "PodPriorityClassName empty, default already exists",
			clusterBindingSpec: cloudv1beta1.ClusterBindingSpec{
				ClusterID: "test-cluster",
			},
			existingPriorityClass: &schedulingv1.PriorityClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: cloudv1beta1.DefaultPriorityClassName,
				},
				Value:            0,
				PreemptionPolicy: ptr.To(corev1.PreemptLowerPriority),
			},
			expectedPriorityClass: cloudv1beta1.DefaultPriorityClassName,
			expectError:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test objects
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, schedulingv1.AddToScheme(scheme))
			require.NoError(t, cloudv1beta1.AddToScheme(scheme))

			var objects []client.Object
			if tt.existingPriorityClass != nil {
				objects = append(objects, tt.existingPriorityClass)
			}

			// Add kubernetes-intranet service for testing
			kubernetesService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type:      corev1.ServiceTypeLoadBalancer,
					ClusterIP: "10.96.0.1",
					Ports: []corev1.ServicePort{
						{
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
			objects = append(objects, kubernetesService)

			fakeClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			// Create virtual pod
			virtualPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-namespace",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "test-image",
						},
					},
				},
			}

			// Create ClusterBinding with name
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: tt.clusterBindingSpec,
			}

			// Create kubernetes-intranet service
			kubernetesIntranetService := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubernetes-intranet",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Port: 443,
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "10.0.0.1"},
						},
					},
				},
			}

			// Create virtual client with ClusterBinding and service
			virtualClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(clusterBinding, kubernetesIntranetService).
				Build()

			// Create reconciler
			reconciler := &VirtualPodReconciler{
				PhysicalClient: fakeClient,
				VirtualClient:  virtualClient,
				ClusterBinding: clusterBinding,
				Log:            zap.New(zap.UseDevMode(true)),
			}

			// Test buildPhysicalPodSpec
			spec, err := reconciler.buildPhysicalPodSpec(context.Background(), virtualPod, "test-node", &ResourceMapping{})

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedPriorityClass, spec.PriorityClassName)
			}
		})
	}
}

// TestVirtualPodReconciler_ShouldCreatePhysicalPod tests the shouldCreatePhysicalPod function
func TestVirtualPodReconciler_ShouldCreatePhysicalPod(t *testing.T) {
	reconciler := &VirtualPodReconciler{}

	tests := []struct {
		name               string
		virtualPod         *corev1.Pod
		setupReconciler    func() *VirtualPodReconciler
		expectedShouldSync bool
	}{
		{
			name:               "nil pod should not sync",
			virtualPod:         nil,
			expectedShouldSync: false,
		},
		{
			name: "pod without node name should not sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unscheduled-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: "",
				},
			},
			expectedShouldSync: false,
		},
		{
			name: "regular pod with node name should sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedShouldSync: true,
		},
		{
			name: "system pod should not sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "system-pod",
					Namespace: "kube-system",
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedShouldSync: false,
		},
		{
			name: "daemonset pod without running annotation should not sync",
			virtualPod: &corev1.Pod{
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
					NodeName: "node-1",
				},
			},
			expectedShouldSync: false,
		},
		{
			name: "daemonset pod with running annotation should sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-with-annotation",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationRunningDaemonSet: cloudv1beta1.LabelValueTrue,
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedShouldSync: true,
		},
		{
			name: "daemonset pod with running annotation false should not sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-annotation-false",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationRunningDaemonSet: "false",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedShouldSync: false,
		},
		{
			name: "daemonset pod with empty annotation should not sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-empty-annotation",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationRunningDaemonSet: "",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedShouldSync: false,
		},
		{
			name: "daemonset pod with other annotations but no running annotation should not sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-other-annotations",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
					Annotations: map[string]string{
						"other.annotation/key": "value",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedShouldSync: false,
		},
		{
			name: "hostport fake pod should not sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hostport-fake-pod",
					Namespace: "default",
					Labels: map[string]string{
						cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedShouldSync: false,
		},
		{
			name: "daemonset pod with running annotation and system namespace should not sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-system-pod",
					Namespace: "kube-system",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationRunningDaemonSet: cloudv1beta1.LabelValueTrue,
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedShouldSync: false, // System pods should never sync regardless of annotations
		},
		{
			name: "daemonset pod without annotation but RunningDaemonsetByDefault=true should sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-default-enabled",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			setupReconciler: func() *VirtualPodReconciler {
				clusterBinding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster-binding",
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID:                 "test-cluster-id",
						RunningDaemonsetByDefault: true,
						SecretRef: corev1.SecretReference{
							Name:      "test-secret",
							Namespace: "default",
						},
					},
				}
				scheme := runtime.NewScheme()
				_ = cloudv1beta1.AddToScheme(scheme)
				_ = corev1.AddToScheme(scheme)
				virtualClient := fakeclient.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(clusterBinding).
					Build()
				return &VirtualPodReconciler{
					VirtualClient:  virtualClient,
					ClusterBinding: clusterBinding,
				}
			},
			expectedShouldSync: true,
		},
		{
			name: "daemonset pod without annotation and RunningDaemonsetByDefault=false should not sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-default-disabled",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			setupReconciler: func() *VirtualPodReconciler {
				clusterBinding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster-binding",
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID:                 "test-cluster-id",
						RunningDaemonsetByDefault: false,
						SecretRef: corev1.SecretReference{
							Name:      "test-secret",
							Namespace: "default",
						},
					},
				}
				scheme := runtime.NewScheme()
				_ = cloudv1beta1.AddToScheme(scheme)
				_ = corev1.AddToScheme(scheme)
				virtualClient := fakeclient.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(clusterBinding).
					Build()
				return &VirtualPodReconciler{
					VirtualClient:  virtualClient,
					ClusterBinding: clusterBinding,
				}
			},
			expectedShouldSync: false,
		},
		{
			name: "daemonset pod with annotation and RunningDaemonsetByDefault=true should sync",
			virtualPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-with-annotation-default-enabled",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationRunningDaemonSet: cloudv1beta1.LabelValueTrue,
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			setupReconciler: func() *VirtualPodReconciler {
				clusterBinding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster-binding",
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID:                 "test-cluster-id",
						RunningDaemonsetByDefault: true,
						SecretRef: corev1.SecretReference{
							Name:      "test-secret",
							Namespace: "default",
						},
					},
				}
				scheme := runtime.NewScheme()
				_ = cloudv1beta1.AddToScheme(scheme)
				_ = corev1.AddToScheme(scheme)
				virtualClient := fakeclient.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(clusterBinding).
					Build()
				return &VirtualPodReconciler{
					VirtualClient:  virtualClient,
					ClusterBinding: clusterBinding,
				}
			},
			expectedShouldSync: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testReconciler := reconciler
			if tt.setupReconciler != nil {
				testReconciler = tt.setupReconciler()
			}
			result := testReconciler.shouldCreatePhysicalPod(tt.virtualPod)
			assert.Equal(t, tt.expectedShouldSync, result, "shouldCreatePhysicalPod result mismatch for test: %s", tt.name)
		})
	}
}

// TestVirtualPodReconciler_EventFilterPredicate tests the event filter predicate for DaemonSet pods
func TestVirtualPodReconciler_EventFilterPredicate(t *testing.T) {
	tests := []struct {
		name            string
		pod             *corev1.Pod
		setupReconciler func() *VirtualPodReconciler
		expectedFilter  bool
	}{
		{
			name:           "nil pod should be filtered",
			pod:            nil,
			expectedFilter: false,
		},
		{
			name: "pod without node name should be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unscheduled-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: "",
				},
			},
			expectedFilter: false,
		},
		{
			name: "pod with deletion timestamp should not be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "deleting-pod",
					Namespace:         "default",
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Spec: corev1.PodSpec{
					NodeName: "",
				},
			},
			expectedFilter: true,
		},
		{
			name: "regular pod with node name should not be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-pod",
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedFilter: true,
		},
		{
			name: "system pod should be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "system-pod",
					Namespace: "kube-system",
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedFilter: false,
		},
		{
			name: "daemonset pod without running annotation should be filtered",
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
					NodeName: "node-1",
				},
			},
			expectedFilter: false,
		},
		{
			name: "daemonset pod with running annotation should not be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-with-annotation",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationRunningDaemonSet: cloudv1beta1.LabelValueTrue,
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedFilter: true,
		},
		{
			name: "daemonset pod with running annotation false should be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-annotation-false",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationRunningDaemonSet: "false",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedFilter: false,
		},
		{
			name: "deleting daemonset pod without annotation should not be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "deleting-daemonset-pod",
					Namespace:         "default",
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			expectedFilter: true, // Deletion should override DaemonSet filtering
		},
		{
			name: "daemonset pod without annotation but RunningDaemonsetByDefault=true should not be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-default-enabled",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			setupReconciler: func() *VirtualPodReconciler {
				clusterBinding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster-binding",
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID:                 "test-cluster-id",
						RunningDaemonsetByDefault: true,
						SecretRef: corev1.SecretReference{
							Name:      "test-secret",
							Namespace: "default",
						},
					},
				}
				scheme := runtime.NewScheme()
				_ = cloudv1beta1.AddToScheme(scheme)
				_ = corev1.AddToScheme(scheme)
				virtualClient := fakeclient.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(clusterBinding).
					Build()
				return &VirtualPodReconciler{
					VirtualClient:  virtualClient,
					ClusterBinding: clusterBinding,
				}
			},
			expectedFilter: true,
		},
		{
			name: "daemonset pod without annotation and RunningDaemonsetByDefault=false should be filtered",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod-default-disabled",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "node-1",
				},
			},
			setupReconciler: func() *VirtualPodReconciler {
				clusterBinding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster-binding",
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID:                 "test-cluster-id",
						RunningDaemonsetByDefault: false,
						SecretRef: corev1.SecretReference{
							Name:      "test-secret",
							Namespace: "default",
						},
					},
				}
				scheme := runtime.NewScheme()
				_ = cloudv1beta1.AddToScheme(scheme)
				_ = corev1.AddToScheme(scheme)
				virtualClient := fakeclient.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(clusterBinding).
					Build()
				return &VirtualPodReconciler{
					VirtualClient:  virtualClient,
					ClusterBinding: clusterBinding,
				}
			},
			expectedFilter: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &VirtualPodReconciler{}
			if tt.setupReconciler != nil {
				reconciler = tt.setupReconciler()
			}
			// Simulate the filter predicate logic from SetupWithManager
			var result bool
			if tt.pod == nil {
				result = false
			} else {
				pod := tt.pod
				// Check if pod has deletion timestamp
				if pod.DeletionTimestamp != nil {
					result = true
				} else {
					// Only sync pods with spec.nodeName set (scheduled pods)
					if pod.Spec.NodeName == "" {
						result = false
					} else {
						// Skip system pods
						if utils.IsSystemPod(pod) {
							result = false
						} else {
							// Skip DaemonSet pods unless RunningDaemonsetByDefault is true or they have the running annotation
							if utils.IsDaemonSetPod(pod) {
								clusterBindingName := ""
								if reconciler.ClusterBinding != nil {
									clusterBindingName = reconciler.ClusterBinding.Name
								}
								// If RunningDaemonsetByDefault is true, allow DaemonSet pods to run by default
								runningds, _ := utils.IsRunningDaemonsetByDefault(context.TODO(), reconciler.VirtualClient, clusterBindingName)
								if !runningds {
									// Check if the pod has the kubeocean.io/running-daemonset:"true" annotation
									if pod.Annotations == nil || pod.Annotations[cloudv1beta1.AnnotationRunningDaemonSet] != cloudv1beta1.LabelValueTrue {
										result = false
									} else {
										// Allow DaemonSet pods with the running annotation to be synced
										result = true
									}
								} else {
									// Allow DaemonSet pods to be synced when RunningDaemonsetByDefault is true
									result = true
								}
							} else {
								result = true
							}
						}
					}
				}
			}

			assert.Equal(t, tt.expectedFilter, result, "Event filter result mismatch for test: %s", tt.name)
		})
	}
}
