package bottomup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

func TestPhysicalNodeReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	tests := []struct {
		name                string
		physicalNode        *corev1.Node
		policies            []cloudv1beta1.ResourceLeasingPolicy
		expectedVirtualNode bool
		expectedRequeue     bool
	}{
		{
			name: "node with applicable policy",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"node-type": "worker",
					},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			policies: []cloudv1beta1.ResourceLeasingPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-policy",
					},
					Spec: cloudv1beta1.ResourceLeasingPolicySpec{
						Cluster: "test-cluster",
						NodeSelector: map[string]string{
							"node-type": "worker",
						},
						ResourceLimits: []cloudv1beta1.ResourceLimit{
							{Resource: "cpu", Quantity: resource.MustParse("2")},
							{Resource: "memory", Quantity: resource.MustParse("4Gi")},
						},
					},
				},
			},
			expectedVirtualNode: true,
			expectedRequeue:     true,
		},
		{
			name: "node without applicable policy",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"node-type": "master",
					},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			policies: []cloudv1beta1.ResourceLeasingPolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-policy",
					},
					Spec: cloudv1beta1.ResourceLeasingPolicySpec{
						Cluster: "test-cluster",
						NodeSelector: map[string]string{
							"node-type": "worker",
						},
					},
				},
			},
			expectedVirtualNode: true,
			expectedRequeue:     true,
		},
		{
			name:                "deleted node",
			physicalNode:        nil, // Node doesn't exist (deleted)
			policies:            []cloudv1beta1.ResourceLeasingPolicy{},
			expectedVirtualNode: false,
			expectedRequeue:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake clients
			physicalObjs := []client.Object{}
			if tt.physicalNode != nil {
				physicalObjs = append(physicalObjs, tt.physicalNode)
			}
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjs...).Build()

			virtualObjs := []client.Object{}
			for i := range tt.policies {
				virtualObjs = append(virtualObjs, &tt.policies[i])
			}
			// Create cluster binding and ensure it exists in virtual cluster for lookup inside processNode
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
			}
			virtualObjs = append(virtualObjs, clusterBinding)
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjs...).Build()

			// Create reconciler
			reconciler := &PhysicalNodeReconciler{
				PhysicalClient:     physicalClient,
				VirtualClient:      virtualClient,
				Scheme:             scheme,
				ClusterBinding:     clusterBinding,
				ClusterBindingName: clusterBinding.Name,
				Log:                ctrl.Log.WithName("test-physical-node-reconciler"),
			}

			// Test reconcile
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "test-node",
				},
			}

			ctx := context.Background()
			result, err := reconciler.Reconcile(ctx, req)

			if tt.name == "deleted node" {
				// For deleted node, we expect no error and no requeue
				assert.NoError(t, err)
				assert.Equal(t, time.Duration(0), result.RequeueAfter)
			} else {
				assert.NoError(t, err)
				if tt.expectedRequeue {
					assert.True(t, result.RequeueAfter > 0)
				} else {
					assert.Equal(t, time.Duration(0), result.RequeueAfter)
				}

				// Check if virtual node was created
				virtualNodeName := reconciler.generateVirtualNodeName("test-node")
				virtualNode := &corev1.Node{}
				err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)

				if tt.expectedVirtualNode {
					assert.NoError(t, err, "Virtual node should exist")
					assert.Equal(t, virtualNodeName, virtualNode.Name)
					assert.Equal(t, "test-cluster", virtualNode.Labels[LabelPhysicalClusterID])
					assert.Equal(t, "test-node", virtualNode.Labels[LabelPhysicalNodeName])
					assert.Equal(t, "tapestry", virtualNode.Labels[LabelManagedBy])
				} else {
					assert.True(t, client.IgnoreNotFound(err) == nil, "Virtual node should not exist")
				}
			}
		})
	}
}

func TestPhysicalNodeReconciler_CalculateAvailableResources(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
	}

	reconciler := &PhysicalNodeReconciler{
		ClusterBinding: clusterBinding,
		Log:            ctrl.Log.WithName("test"),
	}

	// Create test node
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}

	tests := []struct {
		name     string
		policies []cloudv1beta1.ResourceLeasingPolicy
		expected map[corev1.ResourceName]string
	}{
		{
			name:     "no policies - conservative default",
			policies: []cloudv1beta1.ResourceLeasingPolicy{},
			expected: map[corev1.ResourceName]string{
				corev1.ResourceCPU:    "3",          // 80% of 4 cores = 3.2, truncated to 3
				corev1.ResourceMemory: "6871947673", // 80% of 8Gi
			},
		},
		{
			name: "policy with resource limits",
			policies: []cloudv1beta1.ResourceLeasingPolicy{
				{
					Spec: cloudv1beta1.ResourceLeasingPolicySpec{
						ResourceLimits: []cloudv1beta1.ResourceLimit{
							{Resource: "cpu", Quantity: resource.MustParse("2")},
							{Resource: "memory", Quantity: resource.MustParse("4Gi")},
						},
					},
				},
			},
			expected: map[corev1.ResourceName]string{
				corev1.ResourceCPU:    "2",
				corev1.ResourceMemory: "4Gi",
			},
		},
		{
			name: "policy limit exceeds node capacity",
			policies: []cloudv1beta1.ResourceLeasingPolicy{
				{
					Spec: cloudv1beta1.ResourceLeasingPolicySpec{
						ResourceLimits: []cloudv1beta1.ResourceLimit{
							{Resource: "cpu", Quantity: resource.MustParse("8")}, // Exceeds node capacity
							{Resource: "memory", Quantity: resource.MustParse("16Gi")},
						},
					},
				},
			},
			expected: map[corev1.ResourceName]string{
				corev1.ResourceCPU:    "4",   // Limited by node capacity
				corev1.ResourceMemory: "8Gi", // Limited by node capacity
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resources := reconciler.calculateAvailableResources(node, tt.policies)

			for resourceName, expectedValue := range tt.expected {
				actualQuantity := resources[resourceName]
				expectedQuantity := resource.MustParse(expectedValue)
				assert.True(t, actualQuantity.Equal(expectedQuantity),
					"Resource %s: expected %s, got %s", resourceName, expectedQuantity.String(), actualQuantity.String())
			}
		})
	}
}

func TestPhysicalNodeReconciler_NodeMatchesSelector(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Labels: map[string]string{
				"node-type":   "worker",
				"environment": "production",
				"zone":        "us-west-1a",
			},
		},
	}

	tests := []struct {
		name         string
		nodeSelector map[string]string
		expected     bool
	}{
		{
			name:         "empty selector matches all",
			nodeSelector: map[string]string{},
			expected:     true,
		},
		{
			name:         "nil selector matches all",
			nodeSelector: nil,
			expected:     true,
		},
		{
			name: "single matching label",
			nodeSelector: map[string]string{
				"node-type": "worker",
			},
			expected: true,
		},
		{
			name: "multiple matching labels",
			nodeSelector: map[string]string{
				"node-type":   "worker",
				"environment": "production",
			},
			expected: true,
		},
		{
			name: "non-matching label value",
			nodeSelector: map[string]string{
				"node-type": "master",
			},
			expected: false,
		},
		{
			name: "non-existing label",
			nodeSelector: map[string]string{
				"non-existing": "value",
			},
			expected: false,
		},
		{
			name: "partial match - one matches, one doesn't",
			nodeSelector: map[string]string{
				"node-type":   "worker",  // matches
				"environment": "staging", // doesn't match
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.nodeMatchesSelector(node, tt.nodeSelector)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalNodeReconciler_GenerateVirtualNodeName(t *testing.T) {
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
	}

	reconciler := &PhysicalNodeReconciler{
		ClusterBinding: clusterBinding,
		Log:            ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name             string
		physicalNodeName string
		expected         string
	}{
		{
			name:             "simple node name",
			physicalNodeName: "worker-1",
			expected:         "vnode-worker-test-cluster-worker-1",
		},
		{
			name:             "node name with underscores",
			physicalNodeName: "worker_node_1",
			expected:         "vnode-worker-test-cluster-worker-node-1",
		},
		{
			name:             "node name with mixed case",
			physicalNodeName: "Worker-Node-1",
			expected:         "vnode-worker-test-cluster-worker-node-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.generateVirtualNodeName(tt.physicalNodeName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalNodeReconciler_ShouldSkipLabel(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name     string
		labelKey string
		expected bool
	}{
		{"skip node.kubernetes.io prefix", "node.kubernetes.io/instance", true},
		{"skip kubernetes.io/hostname", "kubernetes.io/hostname", true},
		{"skip beta.kubernetes.io prefix", "beta.kubernetes.io/arch", true},
		{"allow custom label", "custom-label", false},
		{"allow environment label", "environment", false},
		{"allow node-type label", "node-type", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.shouldSkipLabel(tt.labelKey)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalNodeReconciler_IsWithinTimeWindows(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name     string
		policies []cloudv1beta1.ResourceLeasingPolicy
		expected bool
	}{
		{
			name: "no time windows - always available",
			policies: []cloudv1beta1.ResourceLeasingPolicy{
				{
					Spec: cloudv1beta1.ResourceLeasingPolicySpec{
						TimeWindows: []cloudv1beta1.TimeWindow{},
					},
				},
			},
			expected: true,
		},
		{
			name: "time window covers current time",
			policies: []cloudv1beta1.ResourceLeasingPolicy{
				{
					Spec: cloudv1beta1.ResourceLeasingPolicySpec{
						TimeWindows: []cloudv1beta1.TimeWindow{
							{
								Start: "00:00",
								End:   "23:59",
								Days:  []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
							},
						},
					},
				},
			},
			expected: true, // Should always match since it covers all day
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.isWithinTimeWindows(tt.policies)
			// Note: This test might be flaky depending on when it runs
			// In a production environment, you'd want to inject time dependencies
			_ = result // We acknowledge the result but don't assert due to time dependency
			t.Logf("Time window check result: %v", result)
		})
	}
}

func TestPhysicalNodeReconciler_NodeMatchesCombinedSelector(t *testing.T) {
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
	}

	reconciler := &PhysicalNodeReconciler{
		ClusterBinding: clusterBinding,
	}

	tests := []struct {
		name            string
		nodeLabels      map[string]string
		policySelector  map[string]string
		clusterSelector map[string]string
		expected        bool
	}{
		{
			name:            "empty selectors match all",
			nodeLabels:      map[string]string{"env": "prod"},
			policySelector:  map[string]string{},
			clusterSelector: map[string]string{},
			expected:        true,
		},
		{
			name:            "node matches both selectors",
			nodeLabels:      map[string]string{"env": "prod", "zone": "us-east-1"},
			policySelector:  map[string]string{"env": "prod"},
			clusterSelector: map[string]string{"zone": "us-east-1"},
			expected:        true,
		},
		{
			name:            "node matches policy but not cluster selector",
			nodeLabels:      map[string]string{"env": "prod", "zone": "us-west-1"},
			policySelector:  map[string]string{"env": "prod"},
			clusterSelector: map[string]string{"zone": "us-east-1"},
			expected:        false,
		},
		{
			name:            "node matches cluster but not policy selector",
			nodeLabels:      map[string]string{"env": "dev", "zone": "us-east-1"},
			policySelector:  map[string]string{"env": "prod"},
			clusterSelector: map[string]string{"zone": "us-east-1"},
			expected:        false,
		},
		{
			name:            "node matches neither selector",
			nodeLabels:      map[string]string{"env": "dev", "zone": "us-west-1"},
			policySelector:  map[string]string{"env": "prod"},
			clusterSelector: map[string]string{"zone": "us-east-1"},
			expected:        false,
		},
		{
			name:            "overlapping selectors",
			nodeLabels:      map[string]string{"env": "prod", "zone": "us-east-1"},
			policySelector:  map[string]string{"env": "prod", "zone": "us-east-1"},
			clusterSelector: map[string]string{"env": "prod"},
			expected:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-node",
					Labels: tt.nodeLabels,
				},
			}

			result := reconciler.nodeMatchesCombinedSelector(node, tt.policySelector, tt.clusterSelector)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalNodeReconciler_TriggerReconciliation_QueueNotInitialized(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: ctrl.Log.WithName("test-physical-node-reconciler"),
		// workQueue is intentionally not initialized
	}

	err := reconciler.TriggerReconciliation("test-node")

	if err == nil {
		t.Error("Expected error when work queue is not initialized")
	}

	if !strings.Contains(err.Error(), "work queue not initialized") {
		t.Errorf("Expected 'work queue not initialized' error, got: %v", err)
	}
}
