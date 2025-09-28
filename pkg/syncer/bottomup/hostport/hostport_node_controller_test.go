/*
Copyright 2024 The Kubeocean Authors.

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

package hostport

import (
	"context"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

func TestHostPortNodeReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	tests := []struct {
		name             string
		physicalNode     *corev1.Node
		virtualNode      *corev1.Node
		physicalPods     []corev1.Pod
		existingFakePods []corev1.Pod
		expectedResult   ctrl.Result
		expectedError    bool
		expectFakePod    bool
		description      string
	}{
		{
			name:           "physical node not found",
			expectedResult: ctrl.Result{},
			expectedError:  false,
			expectFakePod:  false,
			description:    "Should handle missing physical node gracefully",
		},
		{
			name: "physical node without policy-applied label",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
			expectFakePod:  false,
			description:    "Should skip nodes without policy-applied label",
		},
		{
			name: "virtual node not found",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPolicyApplied: "test-policy",
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
			expectFakePod:  false,
			description:    "Should skip when virtual node doesn't exist",
		},
		{
			name: "successful reconcile with hostport pods",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPolicyApplied: "test-policy",
					},
				},
			},
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vnode-test-cluster-test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
			},
			physicalPods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-node",
						Containers: []corev1.Container{
							{
								Name: "test-container",
								Ports: []corev1.ContainerPort{
									{
										Name:          "http",
										ContainerPort: 8080,
										HostPort:      30080,
										Protocol:      corev1.ProtocolTCP,
									},
								},
							},
						},
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
			expectFakePod:  true,
			description:    "Should create fake pod when hostport pods exist",
		},
		{
			name: "reconcile with kubeocean managed pod - should be skipped",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPolicyApplied: "test-policy",
					},
				},
			},
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vnode-test-cluster-test-node",
				},
			},
			physicalPods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeocean-managed-pod",
						Namespace: "default",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						},
					},
					Spec: corev1.PodSpec{
						NodeName: "test-node",
						Containers: []corev1.Container{
							{
								Ports: []corev1.ContainerPort{
									{
										HostPort: 30080,
									},
								},
							},
						},
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
			expectFakePod:  false,
			description:    "Should skip kubeocean managed pods",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake clients
			physicalObjs := []client.Object{}
			if tt.physicalNode != nil {
				physicalObjs = append(physicalObjs, tt.physicalNode)
			}
			for i := range tt.physicalPods {
				physicalObjs = append(physicalObjs, &tt.physicalPods[i])
			}

			virtualObjs := []client.Object{}
			if tt.virtualNode != nil {
				virtualObjs = append(virtualObjs, tt.virtualNode)
			}
			for i := range tt.existingFakePods {
				virtualObjs = append(virtualObjs, &tt.existingFakePods[i])
			}

			physicalClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(physicalObjs...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()

			virtualClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(virtualObjs...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()

			reconciler := &HostPortNodeReconciler{
				PhysicalClient: physicalClient,
				VirtualClient:  virtualClient,
				Scheme:         scheme,
				ClusterBinding: &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID: "test-cluster",
					},
				},
				ClusterBindingName: "test-cluster",
				Log:                ctrl.Log.WithName("test"),
				workQueue:          workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]()),
			}

			// Test reconcile
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "test-node"},
			}

			result, err := reconciler.Reconcile(context.Background(), req)

			if tt.expectedError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}

			assert.Equal(t, tt.expectedResult, result, tt.description)

			// Check if fake pod was created when expected
			if tt.expectFakePod {
				// Ensure fake namespace exists first
				namespace := &corev1.Namespace{}
				err := virtualClient.Get(context.Background(), client.ObjectKey{Name: FakePodNamespace}, namespace)
				assert.NoError(t, err, "Fake namespace should exist")

				// Check for fake pod
				fakePods := &corev1.PodList{}
				err = virtualClient.List(context.Background(), fakePods,
					client.InNamespace(FakePodNamespace),
					client.MatchingLabels{
						cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
						cloudv1beta1.LabelManagedBy:       cloudv1beta1.LabelManagedByValue,
					})
				assert.NoError(t, err, "Should be able to list fake pods")
				assert.Greater(t, len(fakePods.Items), 0, "Should have created fake pod")
			}
		})
	}
}

func TestHostPortNodeReconciler_hasAppliedPolicyLabel(t *testing.T) {
	reconciler := &HostPortNodeReconciler{}

	tests := []struct {
		name     string
		node     *corev1.Node
		expected bool
	}{
		{
			name:     "nil node",
			node:     nil,
			expected: false,
		},
		{
			name: "node with nil labels",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
			},
			expected: false,
		},
		{
			name: "node without policy-applied label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node",
					Labels: map[string]string{},
				},
			},
			expected: false,
		},
		{
			name: "node with empty policy-applied label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPolicyApplied: "",
					},
				},
			},
			expected: false,
		},
		{
			name: "node with policy-applied label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPolicyApplied: "test-policy",
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.hasAppliedPolicyLabel(tt.node)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHostPortNodeReconciler_podHasHostPort(t *testing.T) {
	reconciler := &HostPortNodeReconciler{}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod without hostPort",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8080,
								},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod with hostPort in container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8080,
									HostPort:      30080,
								},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod with hostPort in init container",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8080,
									HostPort:      30080,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 9090,
								},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod with no ports",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "test",
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.podHasHostPort(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHostPortNodeReconciler_isKubeoceanManagedPod(t *testing.T) {
	reconciler := &HostPortNodeReconciler{}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod without labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
			},
			expected: false,
		},
		{
			name: "pod without managed-by label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-pod",
					Labels: map[string]string{},
				},
			},
			expected: false,
		},
		{
			name: "pod with wrong managed-by value",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: "other-controller",
					},
				},
			},
			expected: false,
		},
		{
			name: "kubeocean managed pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.isKubeoceanManagedPod(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHostPortNodeReconciler_shouldProcessPod(t *testing.T) {
	reconciler := &HostPortNodeReconciler{}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "unscheduled pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
				Spec: corev1.PodSpec{
					NodeName: "",
				},
			},
			expected: false,
		},
		{
			name: "kubeocean managed pod with hostPort",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
				Spec: corev1.PodSpec{
					NodeName: "test-node",
					Containers: []corev1.Container{
						{
							Ports: []corev1.ContainerPort{
								{
									HostPort: 30080,
								},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod without hostPort",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
				Spec: corev1.PodSpec{
					NodeName: "test-node",
					Containers: []corev1.Container{
						{
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8080,
								},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "valid pod with hostPort",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
				},
				Spec: corev1.PodSpec{
					NodeName: "test-node",
					Containers: []corev1.Container{
						{
							Ports: []corev1.ContainerPort{
								{
									HostPort: 30080,
								},
							},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.shouldProcessPod(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHostPortNodeReconciler_getClusterID(t *testing.T) {
	tests := []struct {
		name              string
		clusterBinding    *cloudv1beta1.ClusterBinding
		bindingName       string
		expectedClusterID string
	}{
		{
			name:              "no cluster binding",
			clusterBinding:    nil,
			bindingName:       "test-cluster",
			expectedClusterID: "test-cluster",
		},
		{
			name: "cluster binding with empty cluster ID",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "",
				},
			},
			bindingName:       "test-cluster",
			expectedClusterID: "test-cluster",
		},
		{
			name: "cluster binding with cluster ID",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "production-cluster",
				},
			},
			bindingName:       "test-cluster",
			expectedClusterID: "production-cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &HostPortNodeReconciler{
				ClusterBinding:     tt.clusterBinding,
				ClusterBindingName: tt.bindingName,
			}

			result := reconciler.getClusterID()
			assert.Equal(t, tt.expectedClusterID, result)
		})
	}
}

func TestHostPortNodeReconciler_collectHostPorts(t *testing.T) {
	reconciler := &HostPortNodeReconciler{}

	tests := []struct {
		name          string
		pods          []corev1.Pod
		expectedPorts int
		description   string
	}{
		{
			name:          "empty pod list",
			pods:          []corev1.Pod{},
			expectedPorts: 0,
			description:   "Should return empty list for no pods",
		},
		{
			name: "pod without hostPort",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-node",
						Containers: []corev1.Container{
							{
								Name: "test-container",
								Ports: []corev1.ContainerPort{
									{
										ContainerPort: 8080,
									},
								},
							},
						},
					},
				},
			},
			expectedPorts: 0,
			description:   "Should not collect ports without hostPort",
		},
		{
			name: "pod with hostPort",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-node",
						Containers: []corev1.Container{
							{
								Name: "test-container",
								Ports: []corev1.ContainerPort{
									{
										Name:          "http",
										ContainerPort: 8080,
										HostPort:      30080,
										Protocol:      corev1.ProtocolTCP,
									},
								},
							},
						},
					},
				},
			},
			expectedPorts: 1,
			description:   "Should collect hostPorts from containers",
		},
		{
			name: "pod with hostPort in init container",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-node",
						InitContainers: []corev1.Container{
							{
								Name: "init-container",
								Ports: []corev1.ContainerPort{
									{
										HostPort: 30080,
									},
								},
							},
						},
						Containers: []corev1.Container{
							{
								Name: "test-container",
							},
						},
					},
				},
			},
			expectedPorts: 1,
			description:   "Should collect hostPorts from init containers",
		},
		{
			name: "kubeocean managed pod - should be skipped",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeocean-pod",
						Namespace: "default",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						},
					},
					Spec: corev1.PodSpec{
						NodeName: "test-node",
						Containers: []corev1.Container{
							{
								Ports: []corev1.ContainerPort{
									{
										HostPort: 30080,
									},
								},
							},
						},
					},
				},
			},
			expectedPorts: 0,
			description:   "Should skip kubeocean managed pods",
		},
		{
			name: "multiple pods with hostPorts",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-node",
						Containers: []corev1.Container{
							{
								Name: "container1",
								Ports: []corev1.ContainerPort{
									{
										Name:     "http",
										HostPort: 30080,
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-node",
						Containers: []corev1.Container{
							{
								Name: "container2",
								Ports: []corev1.ContainerPort{
									{
										Name:     "https",
										HostPort: 30443,
									},
								},
							},
						},
					},
				},
			},
			expectedPorts: 2,
			description:   "Should collect hostPorts from multiple pods",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.collectHostPorts(tt.pods)
			assert.Equal(t, tt.expectedPorts, len(result), tt.description)

			// Verify default values are set correctly
			for _, port := range result {
				if port.HostIP == "" {
					assert.Equal(t, "0.0.0.0", port.HostIP, "Default HostIP should be 0.0.0.0")
				}
				if port.Protocol == "" {
					assert.Equal(t, corev1.ProtocolTCP, port.Protocol, "Default protocol should be TCP")
				}
			}

			// Verify ordering (should be sorted)
			if len(result) > 1 {
				for i := 1; i < len(result); i++ {
					prev := result[i-1]
					curr := result[i]

					if prev.HostIP != curr.HostIP {
						assert.True(t, prev.HostIP < curr.HostIP, "Should be sorted by HostIP")
					} else if prev.Protocol != curr.Protocol {
						assert.True(t, prev.Protocol < curr.Protocol, "Should be sorted by Protocol")
					} else {
						assert.True(t, prev.HostPort < curr.HostPort, "Should be sorted by HostPort")
					}
				}
			}
		})
	}
}

func TestHostPortNodeReconciler_buildFakePod(t *testing.T) {
	reconciler := &HostPortNodeReconciler{}

	tests := []struct {
		name            string
		virtualNodeName string
		hostPorts       []corev1.ContainerPort
		description     string
	}{
		{
			name:            "build fake pod with no hostPorts",
			virtualNodeName: "vnode-test-cluster-test-node",
			hostPorts:       []corev1.ContainerPort{},
			description:     "Should build fake pod even with no hostPorts",
		},
		{
			name:            "build fake pod with single hostPort",
			virtualNodeName: "vnode-test-cluster-test-node",
			hostPorts: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: 8080,
					HostPort:      30080,
					Protocol:      corev1.ProtocolTCP,
					HostIP:        "0.0.0.0",
				},
			},
			description: "Should build fake pod with single hostPort",
		},
		{
			name:            "build fake pod with multiple hostPorts",
			virtualNodeName: "vnode-test-cluster-test-node",
			hostPorts: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: 8080,
					HostPort:      30080,
					Protocol:      corev1.ProtocolTCP,
				},
				{
					Name:          "https",
					ContainerPort: 8443,
					HostPort:      30443,
					Protocol:      corev1.ProtocolTCP,
				},
			},
			description: "Should build fake pod with multiple hostPorts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.buildFakePod(tt.virtualNodeName, tt.hostPorts)

			// Verify basic properties
			assert.Equal(t, FakePodNamespace, result.Namespace, "Should be in fake namespace")
			assert.True(t, result.GenerateName != "", "Should have generate name")
			assert.Equal(t, tt.virtualNodeName, result.Spec.NodeName, "Should be scheduled to virtual node")
			assert.Equal(t, SystemNodeCriticalPriorityClass, result.Spec.PriorityClassName, "Should have system priority")
			assert.Equal(t, corev1.RestartPolicyNever, result.Spec.RestartPolicy, "Should have Never restart policy")

			// Verify labels
			assert.Equal(t, cloudv1beta1.LabelValueTrue, result.Labels[cloudv1beta1.LabelHostPortFakePod])
			assert.Equal(t, cloudv1beta1.LabelManagedByValue, result.Labels[cloudv1beta1.LabelManagedBy])

			// Verify tolerations
			require.Equal(t, 1, len(result.Spec.Tolerations), "Should have one toleration")
			assert.Equal(t, corev1.TolerationOpExists, result.Spec.Tolerations[0].Operator, "Should tolerate all taints")

			// Verify container
			require.Equal(t, 1, len(result.Spec.Containers), "Should have one container")
			container := result.Spec.Containers[0]
			assert.Equal(t, FakeContainerName, container.Name, "Should have correct container name")
			assert.Equal(t, FakeContainerImage, container.Image, "Should have correct container image")
			assert.Equal(t, []string{"sleep", "100000000"}, container.Command, "Should have sleep command")

			// Verify hostPorts are set correctly
			assert.True(t, reflect.DeepEqual(tt.hostPorts, container.Ports), "HostPorts should match input")
		})
	}
}

func TestHostPortNodeReconciler_ensureFakeNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name              string
		existingNamespace *corev1.Namespace
		expectError       bool
		shouldCreateNew   bool
		description       string
	}{
		{
			name:              "namespace doesn't exist - should create",
			existingNamespace: nil,
			expectError:       false,
			shouldCreateNew:   true,
			description:       "Should create namespace when it doesn't exist",
		},
		{
			name: "namespace exists - should not create",
			existingNamespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: FakePodNamespace,
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
			},
			expectError:     false,
			shouldCreateNew: false,
			description:     "Should not create namespace when it already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{}
			if tt.existingNamespace != nil {
				objs = append(objs, tt.existingNamespace)
			}

			client := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			reconciler := &HostPortNodeReconciler{
				VirtualClient: client,
			}

			err := reconciler.ensureFakeNamespace(context.Background())

			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)

				// Verify namespace exists
				ns := &corev1.Namespace{}
				err := client.Get(context.Background(), types.NamespacedName{Name: FakePodNamespace}, ns)
				assert.NoError(t, err, "Namespace should exist after ensuring")

				// Verify labels
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, ns.Labels[cloudv1beta1.LabelManagedBy], "Should have managed-by label")
			}
		})
	}
}

func TestHostPortNodeReconciler_handleVirtualNodeEvent(t *testing.T) {
	tests := []struct {
		name               string
		node               *corev1.Node
		eventType          string
		clusterBindingName string
		shouldEnqueue      bool
		description        string
	}{
		{
			name: "node not managed by kubeocean",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: "other-controller",
					},
				},
			},
			eventType:          "add",
			clusterBindingName: "test-cluster",
			shouldEnqueue:      false,
			description:        "Should skip nodes not managed by kubeocean",
		},
		{
			name: "node belongs to different cluster",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding: "other-cluster",
					},
				},
			},
			eventType:          "add",
			clusterBindingName: "test-cluster",
			shouldEnqueue:      false,
			description:        "Should skip nodes from different cluster",
		},
		{
			name: "node missing physical node name label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding: "test-cluster",
					},
				},
			},
			eventType:          "add",
			clusterBindingName: "test-cluster",
			shouldEnqueue:      false,
			description:        "Should skip nodes without physical node name label",
		},
		{
			name: "valid node event",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:        cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding:   "test-cluster",
						cloudv1beta1.LabelPhysicalNodeName: "physical-node-1",
					},
				},
			},
			eventType:          "add",
			clusterBindingName: "test-cluster",
			shouldEnqueue:      true,
			description:        "Should enqueue valid node events",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workQueue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			reconciler := &HostPortNodeReconciler{
				ClusterBindingName: tt.clusterBindingName,
				Log:                logr.Discard(),
				workQueue:          workQueue,
			}

			initialQueueLen := workQueue.Len()
			reconciler.handleVirtualNodeEvent(tt.node, tt.eventType)

			if tt.shouldEnqueue {
				assert.Equal(t, initialQueueLen+1, workQueue.Len(), tt.description)
			} else {
				assert.Equal(t, initialQueueLen, workQueue.Len(), tt.description)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	// Test that our constants have reasonable values
	assert.Equal(t, "kubeocean-fake", FakePodNamespace, "Fake pod namespace should be kubeocean-fake")
	assert.Equal(t, "fake-pod-for-hostport", FakePodGenerateName, "Fake pod generate name should be fake-pod-for-hostport")
	assert.Equal(t, "fake-container", FakeContainerName, "Fake container name should be fake-container")
	assert.Equal(t, "busybox:1.28", FakeContainerImage, "Fake container image should be busybox:1.28")
	assert.Equal(t, "system-node-critical", SystemNodeCriticalPriorityClass, "Priority class should be system-node-critical")
}
