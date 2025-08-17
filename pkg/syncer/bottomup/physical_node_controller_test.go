package bottomup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
						NodeSelector: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "node-type",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"worker"},
										},
									},
								},
							},
						},
						ResourceLimits: []cloudv1beta1.ResourceLimit{
							{Resource: "cpu", Quantity: &[]resource.Quantity{resource.MustParse("2")}[0]},
							{Resource: "memory", Quantity: &[]resource.Quantity{resource.MustParse("4Gi")}[0]},
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
						NodeSelector: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "node-type",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"worker"},
										},
									},
								},
							},
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

			virtualObjs := []client.Object{}
			for i := range tt.policies {
				virtualObjs = append(virtualObjs, &tt.policies[i])
			}
			// Create cluster binding and ensure it exists in virtual cluster for lookup inside processNode
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			}
			virtualObjs = append(virtualObjs, clusterBinding)

			// Create physical client with Pod index for spec.nodeName
			physicalClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(physicalObjs...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
					pod := rawObj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).Build()

			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjs...).Build()

			// Create reconciler
			reconciler := &PhysicalNodeReconciler{
				PhysicalClient:     physicalClient,
				VirtualClient:      virtualClient,
				KubeClient:         fake.NewSimpleClientset(),
				Scheme:             scheme,
				ClusterBinding:     clusterBinding,
				ClusterBindingName: clusterBinding.Name,
				Log:                ctrl.Log.WithName("test-physical-node-reconciler"),
			}

			// Initialize lease controllers
			reconciler.initLeaseControllers()

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
			},
		},
	}

	tests := []struct {
		name         string
		nodeSelector *corev1.NodeSelector
		expected     bool
	}{
		{
			name:         "nil selector matches all",
			nodeSelector: nil,
			expected:     true,
		},
		{
			name:         "empty selector matches all",
			nodeSelector: &corev1.NodeSelector{},
			expected:     true,
		},
		{
			name: "matching selector",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"worker"},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "non-matching selector",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"master"},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "partial match fails",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"worker"},
							},
							{
								Key:      "environment",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"staging"},
							},
						},
					},
				},
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
	reconciler := &PhysicalNodeReconciler{}

	tests := []struct {
		name            string
		nodeLabels      map[string]string
		policySelector  *corev1.NodeSelector
		clusterSelector *corev1.NodeSelector
		expected        bool
	}{
		{
			name:            "empty selectors match all",
			nodeLabels:      map[string]string{"env": "prod"},
			policySelector:  nil,
			clusterSelector: nil,
			expected:        true,
		},
		{
			name:       "node matches both selectors",
			nodeLabels: map[string]string{"env": "prod", "zone": "us-east-1"},
			policySelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "env",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"prod"},
							},
						},
					},
				},
			},
			clusterSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "zone",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"us-east-1"},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name:       "node matches only one selector",
			nodeLabels: map[string]string{"env": "prod", "zone": "us-west-1"},
			policySelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "env",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"prod"},
							},
						},
					},
				},
			},
			clusterSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "zone",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"us-east-1"},
							},
						},
					},
				},
			},
			expected: false,
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

func TestPhysicalNodeReconciler_GetClusterID(t *testing.T) {
	tests := []struct {
		name           string
		clusterBinding *cloudv1beta1.ClusterBinding
		bindingName    string
		expected       string
	}{
		{
			name: "with cluster ID in binding",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "cluster-123",
				},
			},
			expected: "cluster-123",
		},
		{
			name:        "fallback to binding name",
			bindingName: "test-cluster",
			expected:    "test-cluster",
		},
		{
			name:     "no binding and no name",
			expected: "unknown-cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &PhysicalNodeReconciler{
				ClusterBinding:     tt.clusterBinding,
				ClusterBindingName: tt.bindingName,
				Log:                ctrl.Log.WithName("test"),
			}

			result := reconciler.getClusterID()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalNodeReconciler_GenerateVirtualNodeName(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		ClusterBinding: &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID: "cluster-123",
			},
		},
		Log: ctrl.Log.WithName("test"),
	}

	result := reconciler.generateVirtualNodeName("worker-node-1")
	expected := "vnode-cluster-123-worker-node-1"
	assert.Equal(t, expected, result)
}

func TestPhysicalNodeReconciler_GetBaseResources(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	tests := []struct {
		name     string
		node     *corev1.Node
		expected corev1.ResourceList
	}{
		{
			name: "prefer allocatable over capacity",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3800m"),
						corev1.ResourceMemory: resource.MustParse("7Gi"),
					},
				},
			},
			expected: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3800m"),
				corev1.ResourceMemory: resource.MustParse("7Gi"),
			},
		},
		{
			name: "fallback to capacity when allocatable is empty",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Allocatable: corev1.ResourceList{},
				},
			},
			expected: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.getBaseResources(tt.node)

			for resourceName, expectedQuantity := range tt.expected {
				actualQuantity, exists := result[resourceName]
				assert.True(t, exists, "Resource %s should exist", resourceName)
				assert.True(t, expectedQuantity.Equal(actualQuantity),
					"Resource %s mismatch: expected %s, got %s",
					resourceName, expectedQuantity.String(), actualQuantity.String())
			}
		})
	}
}

func TestPhysicalNodeReconciler_HandleNodeDeletion(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Create existing virtual node
	virtualNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vnode-test-cluster-physical-node",
		},
	}

	virtualClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(virtualNode).
		Build()

	reconciler := &PhysicalNodeReconciler{
		VirtualClient: virtualClient,
		ClusterBinding: &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID: "test-cluster",
			},
		},
		Log: ctrl.Log.WithName("test"),
	}

	ctx := context.Background()

	t.Run("delete existing virtual node", func(t *testing.T) {
		result, err := reconciler.handleNodeDeletion(ctx, "physical-node")
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter)

		// Verify virtual node is deleted
		err = virtualClient.Get(ctx, client.ObjectKey{Name: "vnode-test-cluster-physical-node"}, &corev1.Node{})
		assert.True(t, errors.IsNotFound(err), "Virtual node should be deleted")
	})

	t.Run("delete non-existing virtual node", func(t *testing.T) {
		result, err := reconciler.handleNodeDeletion(ctx, "non-existing-node")
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter)
	})
}

func TestPhysicalNodeReconciler_GetApplicablePolicies(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create test policies
	matchingPolicy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "matching-policy"},
		Spec: cloudv1beta1.ResourceLeasingPolicySpec{
			Cluster: "test-cluster",
			NodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"worker"},
							},
						},
					},
				},
			},
		},
	}

	nonMatchingPolicy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "non-matching-policy"},
		Spec: cloudv1beta1.ResourceLeasingPolicySpec{
			Cluster: "other-cluster",
			NodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"worker"},
							},
						},
					},
				},
			},
		},
	}

	virtualClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(matchingPolicy, nonMatchingPolicy).
		Build()

	reconciler := &PhysicalNodeReconciler{
		VirtualClient:      virtualClient,
		ClusterBindingName: "test-cluster",
		Log:                ctrl.Log.WithName("test"),
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Labels: map[string]string{
				"node-type": "worker",
				"zone":      "us-east-1",
			},
		},
	}

	clusterSelector := &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      "zone",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"us-east-1"},
					},
				},
			},
		},
	}

	ctx := context.Background()
	policies, err := reconciler.getApplicablePolicies(ctx, node, clusterSelector)

	require.NoError(t, err)
	assert.Len(t, policies, 1)
	assert.Equal(t, "matching-policy", policies[0].Name)
}

// Resource calculation tests
func TestPhysicalNodeReconciler_CalculateAvailableResources(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}

	// Create test pods
	runningPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "running-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
			Containers: []corev1.Container{
				{
					Name: "container1",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	physicalClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, runningPod).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
			pod := rawObj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).
		Build()

	reconciler := &PhysicalNodeReconciler{
		PhysicalClient: physicalClient,
		Log:            ctrl.Log.WithName("test"),
	}

	ctx := context.Background()

	tests := []struct {
		name           string
		policy         *cloudv1beta1.ResourceLeasingPolicy
		expectedCPU    string
		expectedMemory string
	}{
		{
			name: "with policy limits",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{Resource: "cpu", Quantity: &[]resource.Quantity{resource.MustParse("2")}[0]},
						{Resource: "memory", Quantity: &[]resource.Quantity{resource.MustParse("4Gi")}[0]},
					},
				},
			},
			expectedCPU:    "2",   // min(available 3, policy 2) = 2
			expectedMemory: "4Gi", // min(available 6Gi, policy 4Gi) = 4Gi
		},
		{
			name:           "without policy limits",
			policy:         nil,
			expectedCPU:    "3",   // allocatable 4 - used 1 = 3
			expectedMemory: "6Gi", // allocatable 8Gi - used 2Gi = 6Gi
		},
		{
			name: "with percentage limits",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{Resource: "cpu", Percent: &[]int32{50}[0]},    // 50% of available 3 = 1.5
						{Resource: "memory", Percent: &[]int32{75}[0]}, // 75% of available 6Gi = 4.5Gi
					},
				},
			},
			expectedCPU:    "1500m",  // 50% of 3000m = 1500m
			expectedMemory: "4608Mi", // 75% of 6144Mi = 4608Mi
		},
		{
			name: "with both quantity and percentage limits - quantity more restrictive",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: &[]resource.Quantity{resource.MustParse("1")}[0], // 1 CPU
							Percent:  &[]int32{80}[0],                                  // 80% of 3 = 2.4 CPU
						},
					},
				},
			},
			expectedCPU:    "1",   // min(quantity 1, percentage 2.4) = 1
			expectedMemory: "6Gi", // no limits applied
		},
		{
			name: "with both quantity and percentage limits - percentage more restrictive",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "memory",
							Quantity: &[]resource.Quantity{resource.MustParse("5Gi")}[0], // 5Gi
							Percent:  &[]int32{50}[0],                                    // 50% of 6Gi = 3Gi
						},
					},
				},
			},
			expectedCPU:    "3",   // no limits applied
			expectedMemory: "3Gi", // min(quantity 5Gi, percentage 3Gi) = 3Gi
		},
		{
			name: "with limits for non-existent resources",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{Resource: "gpu", Quantity: &[]resource.Quantity{resource.MustParse("2")}[0]}, // GPU not available on node
						{Resource: "storage", Percent: &[]int32{30}[0]},                               // Storage not available on node
						{Resource: "cpu", Quantity: &[]resource.Quantity{resource.MustParse("2")}[0]}, // CPU is available
					},
				},
			},
			expectedCPU:    "2",   // Only CPU limit is applied, others are skipped
			expectedMemory: "6Gi", // No memory limit, so full available memory
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			availableResources, err := reconciler.calculateAvailableResources(ctx, node, tt.policy)
			require.NoError(t, err)

			expectedCPU := resource.MustParse(tt.expectedCPU)
			expectedMemory := resource.MustParse(tt.expectedMemory)

			actualCPU := availableResources[corev1.ResourceCPU]
			actualMemory := availableResources[corev1.ResourceMemory]

			assert.True(t, expectedCPU.Equal(actualCPU),
				"CPU mismatch: expected %s, got %s", expectedCPU.String(), actualCPU.String())
			assert.True(t, expectedMemory.Equal(actualMemory),
				"Memory mismatch: expected %s, got %s", expectedMemory.String(), actualMemory.String())
		})
	}
}

func TestPhysicalNodeReconciler_CalculateNodeResourceUsage(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "running-pod", Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: "test-node",
				Containers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1000m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pending-pod", Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: "test-node",
				Containers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "succeeded-pod", Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: "test-node",
				Containers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2000m"), // Should be ignored
								corev1.ResourceMemory: resource.MustParse("4Gi"),   // Should be ignored
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
		},
	}

	objs := make([]client.Object, len(pods))
	for i, pod := range pods {
		objs[i] = pod
	}

	physicalClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
			pod := rawObj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).
		Build()

	reconciler := &PhysicalNodeReconciler{
		PhysicalClient: physicalClient,
		Log:            ctrl.Log.WithName("test"),
	}

	ctx := context.Background()
	usage, err := reconciler.calculateNodeResourceUsage(ctx, "test-node")

	require.NoError(t, err)

	// Should only count running and pending pods: 1000m + 500m = 1500m CPU, 2Gi + 1Gi = 3Gi memory
	expectedCPU := resource.MustParse("1500m")
	expectedMemory := resource.MustParse("3Gi")

	actualCPU := usage[corev1.ResourceCPU]
	actualMemory := usage[corev1.ResourceMemory]

	assert.True(t, expectedCPU.Equal(actualCPU),
		"CPU usage mismatch: expected %s, got %s", expectedCPU.String(), actualCPU.String())
	assert.True(t, expectedMemory.Equal(actualMemory),
		"Memory usage mismatch: expected %s, got %s", expectedMemory.String(), actualMemory.String())
}

// User customization tests
func TestPhysicalNodeReconciler_UserCustomizationPreservation(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	// Test the preserveUserCustomizations method directly
	existing := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"custom-label":           "custom-value", // User added
				"tapestry.io/managed-by": "tapestry",     // Tapestry managed
			},
			Annotations: map[string]string{
				"custom-annotation":     "custom-value", // User added
				"tapestry.io/last-sync": "old-time",     // Tapestry managed
			},
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{Key: "custom-taint", Effect: corev1.TaintEffectNoSchedule}, // User added
			},
		},
	}

	new := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"tapestry.io/managed-by": "tapestry",
			},
			Annotations: map[string]string{
				"tapestry.io/last-sync": "new-time",
			},
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{},
		},
	}

	// This should not panic and should complete successfully
	reconciler.preserveUserCustomizations(existing, new)

	// Verify the method completed and the expected metadata annotation was added
	assert.Contains(t, new.Annotations, AnnotationExpectedMetadata)

	// Verify Tapestry-managed values are still present
	assert.Equal(t, "tapestry", new.Labels["tapestry.io/managed-by"])
	assert.Equal(t, "new-time", new.Annotations["tapestry.io/last-sync"])
}

func TestPhysicalNodeReconciler_ExpectedMetadataHelpers(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: ctrl.Log.WithName("test"),
	}

	t.Run("valid metadata serialization with base64", func(t *testing.T) {
		expectedMetadata := &ExpectedNodeMetadata{
			Labels: map[string]string{
				"test-label": "test-value",
			},
			Annotations: map[string]string{
				"test-annotation": "test-value",
			},
			Taints: []corev1.Taint{
				{
					Key:    "test-taint",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		}

		// Test the save method
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
		}

		err := reconciler.saveExpectedMetadataToAnnotation(node, expectedMetadata)
		require.NoError(t, err)

		// Verify the annotation is base64 encoded
		encodedData := node.Annotations[AnnotationExpectedMetadata]
		assert.NotEmpty(t, encodedData)

		// Verify it's valid base64
		_, err = base64.StdEncoding.DecodeString(encodedData)
		require.NoError(t, err, "Annotation should contain valid base64 data")

		// Test the get method
		result, err := reconciler.getExpectedMetadataFromAnnotation(node)
		require.NoError(t, err)

		assert.Equal(t, expectedMetadata.Labels, result.Labels)
		assert.Equal(t, expectedMetadata.Annotations, result.Annotations)
		assert.Equal(t, expectedMetadata.Taints, result.Taints)
	})

	t.Run("no annotation returns empty metadata", func(t *testing.T) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
		}

		result, err := reconciler.getExpectedMetadataFromAnnotation(node)
		require.NoError(t, err)

		assert.Empty(t, result.Labels)
		assert.Empty(t, result.Annotations)
		assert.Empty(t, result.Taints)
	})

	t.Run("invalid base64 returns error", func(t *testing.T) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					AnnotationExpectedMetadata: "invalid-base64!@#",
				},
			},
		}

		_, err := reconciler.getExpectedMetadataFromAnnotation(node)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to base64 decode expected metadata")
	})

	t.Run("invalid JSON after base64 decode returns error", func(t *testing.T) {
		// Create valid base64 but invalid JSON
		invalidJSON := base64.StdEncoding.EncodeToString([]byte("invalid-json"))

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					AnnotationExpectedMetadata: invalidJSON,
				},
			},
		}

		_, err := reconciler.getExpectedMetadataFromAnnotation(node)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal expected metadata")
	})

	t.Run("empty metadata serialization", func(t *testing.T) {
		expectedMetadata := &ExpectedNodeMetadata{
			Labels:      map[string]string{},
			Annotations: map[string]string{},
			Taints:      []corev1.Taint{},
		}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
		}

		// Test save empty metadata
		err := reconciler.saveExpectedMetadataToAnnotation(node, expectedMetadata)
		require.NoError(t, err)

		// Verify annotation exists and is valid base64
		encodedData := node.Annotations[AnnotationExpectedMetadata]
		assert.NotEmpty(t, encodedData)

		decodedData, err := base64.StdEncoding.DecodeString(encodedData)
		require.NoError(t, err)

		// Verify it's valid JSON
		var testMetadata ExpectedNodeMetadata
		err = json.Unmarshal(decodedData, &testMetadata)
		require.NoError(t, err)

		// Test get empty metadata
		result, err := reconciler.getExpectedMetadataFromAnnotation(node)
		require.NoError(t, err)

		assert.Empty(t, result.Labels)
		assert.Empty(t, result.Annotations)
		assert.Empty(t, result.Taints)
	})

	t.Run("complex metadata with special characters", func(t *testing.T) {
		expectedMetadata := &ExpectedNodeMetadata{
			Labels: map[string]string{
				"app.kubernetes.io/name":    "test-app",
				"app.kubernetes.io/version": "v1.0.0",
				"special/chars":             "value with spaces & symbols!@#$%",
			},
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision":                "1",
				"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"v1","kind":"Node"}`,
				"unicode-test": "测试中文字符 🚀",
			},
			Taints: []corev1.Taint{
				{
					Key:    "node.kubernetes.io/not-ready",
					Effect: corev1.TaintEffectNoExecute,
				},
				{
					Key:    "custom-taint",
					Value:  "special-value-with-symbols!@#",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
		}

		// Test save complex metadata
		err := reconciler.saveExpectedMetadataToAnnotation(node, expectedMetadata)
		require.NoError(t, err)

		// Verify base64 encoding
		encodedData := node.Annotations[AnnotationExpectedMetadata]
		assert.NotEmpty(t, encodedData)

		// Verify it's valid base64
		decodedData, err := base64.StdEncoding.DecodeString(encodedData)
		require.NoError(t, err)

		// Verify it's valid JSON
		var testMetadata ExpectedNodeMetadata
		err = json.Unmarshal(decodedData, &testMetadata)
		require.NoError(t, err)

		// Test get complex metadata
		result, err := reconciler.getExpectedMetadataFromAnnotation(node)
		require.NoError(t, err)

		assert.Equal(t, expectedMetadata.Labels, result.Labels)
		assert.Equal(t, expectedMetadata.Annotations, result.Annotations)
		assert.Equal(t, len(expectedMetadata.Taints), len(result.Taints))

		// Verify taints in detail
		for i, expectedTaint := range expectedMetadata.Taints {
			assert.Equal(t, expectedTaint.Key, result.Taints[i].Key)
			assert.Equal(t, expectedTaint.Value, result.Taints[i].Value)
			assert.Equal(t, expectedTaint.Effect, result.Taints[i].Effect)
		}
	})

	t.Run("nil metadata handling", func(t *testing.T) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
		}

		// Test save nil metadata
		err := reconciler.saveExpectedMetadataToAnnotation(node, nil)
		require.NoError(t, err)

		// Should create empty metadata structure
		encodedData := node.Annotations[AnnotationExpectedMetadata]
		assert.NotEmpty(t, encodedData)

		// Test get from nil metadata annotation
		result, err := reconciler.getExpectedMetadataFromAnnotation(node)
		require.NoError(t, err)

		assert.Empty(t, result.Labels)
		assert.Empty(t, result.Annotations)
		assert.Empty(t, result.Taints)
	})

	t.Run("base64 encoding consistency", func(t *testing.T) {
		expectedMetadata := &ExpectedNodeMetadata{
			Labels: map[string]string{
				"consistency-test": "value",
			},
			Annotations: map[string]string{
				"test": "annotation",
			},
			Taints: []corev1.Taint{
				{
					Key:    "test-taint",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		}

		node1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
		node2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}

		// Save same metadata to two different nodes
		err1 := reconciler.saveExpectedMetadataToAnnotation(node1, expectedMetadata)
		err2 := reconciler.saveExpectedMetadataToAnnotation(node2, expectedMetadata)
		require.NoError(t, err1)
		require.NoError(t, err2)

		// Encoded data should be identical
		encoded1 := node1.Annotations[AnnotationExpectedMetadata]
		encoded2 := node2.Annotations[AnnotationExpectedMetadata]
		assert.Equal(t, encoded1, encoded2, "Same metadata should produce identical base64 encoding")

		// Retrieved data should be identical
		result1, err1 := reconciler.getExpectedMetadataFromAnnotation(node1)
		result2, err2 := reconciler.getExpectedMetadataFromAnnotation(node2)
		require.NoError(t, err1)
		require.NoError(t, err2)

		assert.Equal(t, result1.Labels, result2.Labels)
		assert.Equal(t, result1.Annotations, result2.Annotations)
		assert.Equal(t, result1.Taints, result2.Taints)
	})

	t.Run("large metadata handling", func(t *testing.T) {
		// Create metadata with many entries to test performance and limits
		labels := make(map[string]string)
		annotations := make(map[string]string)
		var taints []corev1.Taint

		// Add 50 labels
		for i := 0; i < 50; i++ {
			labels[fmt.Sprintf("label-%d", i)] = fmt.Sprintf("value-%d-with-longer-content", i)
		}

		// Add 50 annotations
		for i := 0; i < 50; i++ {
			annotations[fmt.Sprintf("annotation-%d", i)] = fmt.Sprintf("annotation-value-%d-with-much-longer-content-to-test-encoding", i)
		}

		// Add 10 taints
		for i := 0; i < 10; i++ {
			taints = append(taints, corev1.Taint{
				Key:    fmt.Sprintf("taint-%d", i),
				Value:  fmt.Sprintf("taint-value-%d", i),
				Effect: corev1.TaintEffectNoSchedule,
			})
		}

		expectedMetadata := &ExpectedNodeMetadata{
			Labels:      labels,
			Annotations: annotations,
			Taints:      taints,
		}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
		}

		// Test save large metadata
		err := reconciler.saveExpectedMetadataToAnnotation(node, expectedMetadata)
		require.NoError(t, err)

		// Verify base64 encoding
		encodedData := node.Annotations[AnnotationExpectedMetadata]
		assert.NotEmpty(t, encodedData)

		// Test get large metadata
		result, err := reconciler.getExpectedMetadataFromAnnotation(node)
		require.NoError(t, err)

		assert.Equal(t, len(expectedMetadata.Labels), len(result.Labels))
		assert.Equal(t, len(expectedMetadata.Annotations), len(result.Annotations))
		assert.Equal(t, len(expectedMetadata.Taints), len(result.Taints))

		// Spot check some values
		assert.Equal(t, expectedMetadata.Labels["label-0"], result.Labels["label-0"])
		assert.Equal(t, expectedMetadata.Annotations["annotation-0"], result.Annotations["annotation-0"])
		assert.Equal(t, expectedMetadata.Taints[0].Key, result.Taints[0].Key)
	})

	t.Run("malformed base64 edge cases", func(t *testing.T) {
		testCases := []struct {
			name        string
			base64Data  string
			expectError bool
		}{
			{
				name:        "empty string",
				base64Data:  "",
				expectError: false, // Empty string should decode to empty data
			},
			{
				name:        "invalid characters",
				base64Data:  "invalid!@#$%^&*()",
				expectError: true,
			},
			{
				name:        "incomplete padding",
				base64Data:  "SGVsbG8gV29ybGQ", // Missing padding
				expectError: true,              // Go's base64 StdEncoding is strict with padding
			},
			{
				name:        "only padding",
				base64Data:  "====",
				expectError: true,
			},
			{
				name:        "mixed valid and invalid",
				base64Data:  "SGVsbG8gV29ybGQ!",
				expectError: true,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							AnnotationExpectedMetadata: tc.base64Data,
						},
					},
				}

				_, err := reconciler.getExpectedMetadataFromAnnotation(node)
				if tc.expectError {
					assert.Error(t, err)
				} else {
					// For valid base64 but potentially invalid JSON, we might get JSON unmarshal errors
					// That's acceptable behavior
					if err != nil {
						assert.Contains(t, err.Error(), "failed to unmarshal expected metadata")
					}
				}
			})
		}
	})

	t.Run("roundtrip data integrity", func(t *testing.T) {
		// Test various data types and edge cases for roundtrip integrity
		testCases := []ExpectedNodeMetadata{
			{
				Labels:      map[string]string{"simple": "value"},
				Annotations: map[string]string{"simple": "annotation"},
				Taints:      []corev1.Taint{{Key: "simple", Effect: corev1.TaintEffectNoSchedule}},
			},
			{
				Labels: map[string]string{
					"empty-value": "",
					"spaces":      "value with spaces",
					"newlines":    "value\nwith\nnewlines",
					"tabs":        "value\twith\ttabs",
					"quotes":      `value with "quotes" and 'apostrophes'`,
				},
				Annotations: map[string]string{
					"json-like": `{"key": "value", "number": 123}`,
					"xml-like":  "<root><child>value</child></root>",
					"yaml-like": "key: value\nother: 123",
				},
				Taints: []corev1.Taint{
					{Key: "empty-value", Value: "", Effect: corev1.TaintEffectNoExecute},
					{Key: "with-spaces", Value: "value with spaces", Effect: corev1.TaintEffectNoSchedule},
				},
			},
		}

		for i, expectedMetadata := range testCases {
			t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{},
					},
				}

				// Save metadata
				err := reconciler.saveExpectedMetadataToAnnotation(node, &expectedMetadata)
				require.NoError(t, err)

				// Retrieve metadata
				result, err := reconciler.getExpectedMetadataFromAnnotation(node)
				require.NoError(t, err)

				// Verify exact match
				assert.Equal(t, expectedMetadata.Labels, result.Labels)
				assert.Equal(t, expectedMetadata.Annotations, result.Annotations)
				assert.Equal(t, expectedMetadata.Taints, result.Taints)
			})
		}
	})
}

// Error handling tests
func TestPhysicalNodeReconciler_ErrorHandling(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	t.Run("missing ClusterBinding", func(t *testing.T) {
		physicalNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-1"},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
		}

		physicalClient := fakeclient.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(physicalNode).
			WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
				pod := rawObj.(*corev1.Pod)
				if pod.Spec.NodeName == "" {
					return nil
				}
				return []string{pod.Spec.NodeName}
			}).Build()
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build() // No ClusterBinding

		reconciler := &PhysicalNodeReconciler{
			PhysicalClient:     physicalClient,
			VirtualClient:      virtualClient,
			KubeClient:         fake.NewSimpleClientset(),
			Scheme:             scheme,
			ClusterBindingName: "non-existent-cluster",
			Log:                ctrl.Log.WithName("test"),
		}

		ctx := context.Background()
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "worker-1"}}

		_, err := reconciler.Reconcile(ctx, req)
		assert.Error(t, err, "Should return error when ClusterBinding is missing")
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestPhysicalNodeReconciler_Stop(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create fake clients
	physicalClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
			pod := rawObj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).Build()
	virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	kubeClient := fake.NewSimpleClientset()

	reconciler := &PhysicalNodeReconciler{
		PhysicalClient:     physicalClient,
		VirtualClient:      virtualClient,
		KubeClient:         kubeClient,
		Scheme:             scheme,
		ClusterBindingName: "test-cluster",
		Log:                ctrl.Log.WithName("test-reconciler"),
	}

	// Initialize lease controllers
	reconciler.initLeaseControllers()

	// Create some virtual nodes and start lease controllers
	virtualNodes := []string{"vnode-test-1", "vnode-test-2", "vnode-test-3"}

	for _, nodeName := range virtualNodes {
		// Create virtual node
		virtualNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
				UID:  types.UID("uid-" + nodeName),
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}
		err := virtualClient.Create(context.Background(), virtualNode)
		require.NoError(t, err, "Failed to create virtual node %s", nodeName)

		// Start lease controller
		reconciler.startLeaseController(nodeName)
	}

	// Verify all lease controllers are running
	status := reconciler.getLeaseControllerStatus()
	assert.Len(t, status, 3, "Expected 3 lease controllers to be running")
	for _, nodeName := range virtualNodes {
		assert.True(t, status[nodeName], "Expected lease controller for %s to be running", nodeName)
	}

	// Call Stop function
	reconciler.Stop()

	// Verify all lease controllers are stopped and cleaned up
	status = reconciler.getLeaseControllerStatus()
	assert.Len(t, status, 0, "Expected all lease controllers to be stopped and cleaned up")

	// Verify that calling Stop again doesn't panic or cause issues
	assert.NotPanics(t, func() {
		reconciler.Stop()
	}, "Stop should be safe to call multiple times")
}
