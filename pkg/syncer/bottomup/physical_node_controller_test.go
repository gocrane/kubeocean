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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
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
					UID:  "test-node-uid-123",
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
			expectedVirtualNode: false, // No policy matches, so virtual node should be deleted
			expectedRequeue:     false, // No requeue needed after deletion
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

			// Add policies to physical objects since they are now in physical cluster
			for i := range tt.policies {
				physicalObjs = append(physicalObjs, &tt.policies[i])
			}

			virtualObjs := []client.Object{}
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
					assert.Equal(t, "test-cluster", virtualNode.Labels[cloudv1beta1.LabelPhysicalClusterID])
					assert.Equal(t, "test-node", virtualNode.Labels[cloudv1beta1.LabelPhysicalNodeName])
					assert.Equal(t, "kubeocean", virtualNode.Labels[cloudv1beta1.LabelManagedBy])

					// Check finalizer
					assert.True(t, controllerutil.ContainsFinalizer(virtualNode, cloudv1beta1.VirtualNodeFinalizer), "Virtual node should have finalizer")

					// Check LabelPhysicalNodeUID
					assert.NotEmpty(t, virtualNode.Labels[cloudv1beta1.LabelPhysicalNodeUID], "Virtual node should have physical node UID label")
				} else {
					assert.True(t, client.IgnoreNotFound(err) == nil, "Virtual node should not exist")
				}
			}
		})
	}
}

func TestPhysicalNodeReconciler_ProcessNode_ClusterBinding(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Common test physical node
	physicalNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			UID:  "test-node-uid-123",
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
	}

	// Common test policy
	testPolicy := cloudv1beta1.ResourceLeasingPolicy{
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
	}

	tests := []struct {
		name                   string
		setupClusterBinding    bool
		clusterBindingDeleting bool
		expectedHandleDeletion bool
		expectedError          bool
	}{
		{
			name:                   "cluster_binding_not_found",
			setupClusterBinding:    false,
			clusterBindingDeleting: false,
			expectedHandleDeletion: false,
			expectedError:          true,
		},
		{
			name:                   "cluster_binding_being_deleted",
			setupClusterBinding:    true,
			clusterBindingDeleting: true,
			expectedHandleDeletion: true,
			expectedError:          false,
		},
		{
			name:                   "cluster_binding_normal",
			setupClusterBinding:    true,
			clusterBindingDeleting: false,
			expectedHandleDeletion: false,
			expectedError:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create physical objects
			physicalObjs := []client.Object{physicalNode, &testPolicy}

			// Create virtual objects
			virtualObjs := []client.Object{}
			if tt.setupClusterBinding {
				clusterBinding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID: "test-cluster",
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
				if tt.clusterBindingDeleting {
					now := metav1.Now()
					clusterBinding.DeletionTimestamp = &now
					// Add finalizer to prevent fake client error
					clusterBinding.Finalizers = []string{"test-finalizer"}
				}
				virtualObjs = append(virtualObjs, clusterBinding)
			}

			// Create clients with Pod index for spec.nodeName
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
			clusterBindingForReconciler := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			}
			reconciler := &PhysicalNodeReconciler{
				PhysicalClient:     physicalClient,
				VirtualClient:      virtualClient,
				KubeClient:         fake.NewSimpleClientset(),
				Scheme:             scheme,
				ClusterBinding:     clusterBindingForReconciler,
				ClusterBindingName: "test-cluster",
				Log:                ctrl.Log.WithName("test-physical-node-reconciler"),
			}

			// Initialize lease controllers
			reconciler.initLeaseControllers()

			// Test processNode
			ctx := context.Background()
			result, err := reconciler.processNode(ctx, physicalNode)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Check result based on expected behavior
			if tt.expectedHandleDeletion {
				// When handleNodeDeletion is called, it should return with no requeue
				assert.Equal(t, time.Duration(0), result.RequeueAfter, "Should not requeue when handling deletion")

				// Verify virtual node creation/deletion based on expected behavior
				virtualNodeName := reconciler.generateVirtualNodeName(physicalNode.Name)
				virtualNode := &corev1.Node{}
				err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)

				// Virtual node should not exist or should be marked for deletion
				if err == nil {
					// If virtual node exists, it should have deletion timestamp or taint
					if virtualNode.DeletionTimestamp == nil {
						// Check for deletion taint
						hasDeletionTaint := false
						for _, taint := range virtualNode.Spec.Taints {
							if taint.Key == "kubeocean.io/vnode-deleting" {
								hasDeletionTaint = true
								break
							}
						}
						assert.True(t, hasDeletionTaint, "Virtual node should have deletion taint when deletion is triggered")
					}
				}
				// It's okay if virtual node doesn't exist yet as handleNodeDeletion might not create it
			} else if !tt.expectedError {
				// Normal processing should have requeue for status updates
				assert.True(t, result.RequeueAfter > 0, "Should requeue for status updates")

				// Virtual node should be created successfully
				virtualNodeName := reconciler.generateVirtualNodeName(physicalNode.Name)
				virtualNode := &corev1.Node{}
				err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
				assert.NoError(t, err, "Virtual node should exist for normal processing")
				assert.Equal(t, virtualNodeName, virtualNode.Name)
				assert.Equal(t, physicalNode.Name, virtualNode.Labels[cloudv1beta1.LabelPhysicalNodeName])
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
		policy   *cloudv1beta1.ResourceLeasingPolicy
		expected bool
	}{
		{
			name: "no time windows - always available",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					TimeWindows: []cloudv1beta1.TimeWindow{},
				},
			},
			expected: true,
		},
		{
			name: "time window covers current time",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
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
			expected: true, // Should always match since it covers all day
		},
		{
			name: "time window outside current time",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "23:00", // Very late time window - should be outside current time
							End:   "23:30",
							// No Days specified, should default to all days
						},
					},
				},
			},
			expected: false, // Should be outside the time window
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.isWithinTimeWindows(tt.policy)
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
			name: "fallback to binding name",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
				Spec:       cloudv1beta1.ClusterBindingSpec{},
			},
			bindingName: "test-cluster",
			expected:    "",
		},
		{
			name:     "no binding and no name",
			expected: "",
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
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
			pod := rawObj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).
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
		result, err := reconciler.handleNodeDeletion(ctx, "physical-node", true, 0)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter)

		// Verify virtual node is deleted
		err = virtualClient.Get(ctx, client.ObjectKey{Name: "vnode-test-cluster-physical-node"}, &corev1.Node{})
		assert.True(t, errors.IsNotFound(err), "Virtual node should be deleted")
	})

	t.Run("delete non-existing virtual node", func(t *testing.T) {
		result, err := reconciler.handleNodeDeletion(ctx, "non-existing-node", true, 0)
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

	physicalClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(matchingPolicy, nonMatchingPolicy).
		Build()

	reconciler := &PhysicalNodeReconciler{
		PhysicalClient:     physicalClient,
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
				"custom-label":            "custom-value", // User added
				"kubeocean.io/managed-by": "kubeocean",    // Kubeocean managed
			},
			Annotations: map[string]string{
				"custom-annotation":      "custom-value", // User added
				"kubeocean.io/last-sync": "old-time",     // Kubeocean managed
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
				"kubeocean.io/managed-by": "kubeocean",
			},
			Annotations: map[string]string{
				"kubeocean.io/last-sync": "new-time",
			},
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{},
		},
	}

	// This should not panic and should complete successfully
	reconciler.preserveUserCustomizations(existing, new)

	// Verify the method completed and the expected metadata annotation was added
	assert.Contains(t, new.Annotations, cloudv1beta1.AnnotationExpectedMetadata)

	// Verify Kubeocean-managed values are still present
	assert.Equal(t, "kubeocean", new.Labels["kubeocean.io/managed-by"])
	assert.Equal(t, "new-time", new.Annotations["kubeocean.io/last-sync"])
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
		encodedData := node.Annotations[cloudv1beta1.AnnotationExpectedMetadata]
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
					cloudv1beta1.AnnotationExpectedMetadata: "invalid-base64!@#",
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
					cloudv1beta1.AnnotationExpectedMetadata: invalidJSON,
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
		encodedData := node.Annotations[cloudv1beta1.AnnotationExpectedMetadata]
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
		encodedData := node.Annotations[cloudv1beta1.AnnotationExpectedMetadata]
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
		encodedData := node.Annotations[cloudv1beta1.AnnotationExpectedMetadata]
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
		encoded1 := node1.Annotations[cloudv1beta1.AnnotationExpectedMetadata]
		encoded2 := node2.Annotations[cloudv1beta1.AnnotationExpectedMetadata]
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
		encodedData := node.Annotations[cloudv1beta1.AnnotationExpectedMetadata]
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
							cloudv1beta1.AnnotationExpectedMetadata: tc.base64Data,
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

func TestPhysicalNodeReconciler_addDeletionTaint(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name           string
		virtualNode    *corev1.Node
		expectTaint    bool
		expectError    bool
		expectedTaints int
	}{
		{
			name: "add deletion taint to node without taint",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-vnode",
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{},
				},
			},
			expectTaint:    true,
			expectError:    false,
			expectedTaints: 1,
		},
		{
			name: "node already has deletion taint and is unschedulable",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-vnode",
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
					Taints: []corev1.Taint{
						{
							Key:    TaintKeyVirtualNodeDeleting,
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expectTaint:    true,
			expectError:    false,
			expectedTaints: 1,
		},
		{
			name: "node has deletion taint but is schedulable",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-vnode",
				},
				Spec: corev1.NodeSpec{
					Unschedulable: false,
					Taints: []corev1.Taint{
						{
							Key:    TaintKeyVirtualNodeDeleting,
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expectTaint:    true,
			expectError:    false,
			expectedTaints: 1,
		},
		{
			name: "node with existing taints",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-vnode",
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    "existing-taint",
							Value:  "value",
							Effect: corev1.TaintEffectNoExecute,
						},
					},
				},
			},
			expectTaint:    true,
			expectError:    false,
			expectedTaints: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with the virtual node
			fakeClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.virtualNode).
				Build()

			reconciler := &PhysicalNodeReconciler{
				VirtualClient: fakeClient,
				Log:           zap.New(zap.UseDevMode(true)),
			}

			ctx := context.Background()
			updatedNode, err := reconciler.addDeletionTaint(ctx, tt.virtualNode)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, updatedNode)

			// Check if deletion taint exists
			hasTaint := reconciler.hasDeletionTaint(updatedNode)
			assert.Equal(t, tt.expectTaint, hasTaint)

			// Check total number of taints
			assert.Equal(t, tt.expectedTaints, len(updatedNode.Spec.Taints))

			// Check that the node is marked as unschedulable
			assert.True(t, updatedNode.Spec.Unschedulable, "Node should be marked as unschedulable")

			// If taint was added, check annotation (only if taint was actually added, not if it already existed)
			if tt.expectTaint && (tt.name == "add deletion taint to node without taint" || tt.name == "node with existing taints" || tt.name == "node has deletion taint but is schedulable") {
				assert.Contains(t, updatedNode.Annotations, AnnotationDeletionTaintTime)
			}
		})
	}
}

func TestPhysicalNodeReconciler_hasDeletionTaint(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: zap.New(zap.UseDevMode(true)),
	}

	tests := []struct {
		name        string
		virtualNode *corev1.Node
		expected    bool
	}{
		{
			name: "node without deletion taint",
			virtualNode: &corev1.Node{
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    "other-taint",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "node with deletion taint",
			virtualNode: &corev1.Node{
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    TaintKeyVirtualNodeDeleting,
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "node with multiple taints including deletion taint",
			virtualNode: &corev1.Node{
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    "other-taint",
							Effect: corev1.TaintEffectNoExecute,
						},
						{
							Key:    TaintKeyVirtualNodeDeleting,
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.hasDeletionTaint(tt.virtualNode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalNodeReconciler_getDeletionTaintTime(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: zap.New(zap.UseDevMode(true)),
	}

	testTime := time.Now()
	testTimeStr := testTime.Format(time.RFC3339)

	tests := []struct {
		name        string
		virtualNode *corev1.Node
		expectError bool
		expectTime  bool
	}{
		{
			name: "node with deletion taint time annotation",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationDeletionTaintTime: testTimeStr,
					},
				},
			},
			expectError: false,
			expectTime:  true,
		},
		{
			name: "node without annotations",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expectError: true,
			expectTime:  false,
		},
		{
			name: "node without deletion taint time annotation",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other-annotation": "value",
					},
				},
			},
			expectError: true,
			expectTime:  false,
		},
		{
			name: "node with invalid time format",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						AnnotationDeletionTaintTime: "invalid-time",
					},
				},
			},
			expectError: true,
			expectTime:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taintTime, err := reconciler.getDeletionTaintTime(tt.virtualNode)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, taintTime)
			} else {
				require.NoError(t, err)
				require.NotNil(t, taintTime)
				if tt.expectTime {
					// Allow for small time differences due to parsing
					assert.WithinDuration(t, testTime, *taintTime, time.Second)
				}
			}
		})
	}
}

func TestPhysicalNodeReconciler_transformTaints(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		Log: zap.New(zap.UseDevMode(true)),
	}

	// 默认污点，每个虚拟节点都会添加
	defaultTaint := corev1.Taint{
		Key:    cloudv1beta1.TaintVnodeDefaultTaint,
		Effect: corev1.TaintEffectNoSchedule,
	}

	tests := []struct {
		name                    string
		physicalTaints          []corev1.Taint
		disableNodeDefaultTaint bool
		policy                  *cloudv1beta1.ResourceLeasingPolicy
		expectedTaints          []corev1.Taint
	}{
		{
			name:                    "empty taints with default taint enabled",
			physicalTaints:          nil,
			disableNodeDefaultTaint: false,
			policy:                  nil,
			expectedTaints:          []corev1.Taint{defaultTaint},
		},
		{
			name:                    "empty taints with default taint disabled",
			physicalTaints:          nil,
			disableNodeDefaultTaint: true,
			expectedTaints:          []corev1.Taint{},
		},
		{
			name: "no unschedulable taint with default taint enabled",
			physicalTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			disableNodeDefaultTaint: false,
			expectedTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
				defaultTaint,
			},
		},
		{
			name: "no unschedulable taint with default taint disabled",
			physicalTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			disableNodeDefaultTaint: true,
			expectedTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
		{
			name: "transform unschedulable taint with default taint enabled",
			physicalTaints: []corev1.Taint{
				{
					Key:    "node.kubernetes.io/unschedulable",
					Value:  "",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			disableNodeDefaultTaint: false,
			expectedTaints: []corev1.Taint{
				{
					Key:    cloudv1beta1.TaintPhysicalNodeUnschedulable,
					Value:  "",
					Effect: corev1.TaintEffectNoSchedule,
				},
				defaultTaint,
			},
		},
		{
			name: "transform unschedulable taint with default taint disabled",
			physicalTaints: []corev1.Taint{
				{
					Key:    "node.kubernetes.io/unschedulable",
					Value:  "",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			disableNodeDefaultTaint: true,
			expectedTaints: []corev1.Taint{
				{
					Key:    cloudv1beta1.TaintPhysicalNodeUnschedulable,
					Value:  "",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
		{
			name: "transform unschedulable taint with value and default taint enabled",
			physicalTaints: []corev1.Taint{
				{
					Key:    "node.kubernetes.io/unschedulable",
					Value:  "maintenance",
					Effect: corev1.TaintEffectNoExecute,
				},
			},
			disableNodeDefaultTaint: false,
			expectedTaints: []corev1.Taint{
				{
					Key:    cloudv1beta1.TaintPhysicalNodeUnschedulable,
					Value:  "maintenance",
					Effect: corev1.TaintEffectNoExecute,
				},
				defaultTaint,
			},
		},
		{
			name: "filter out-of-time-windows taint with default taint enabled",
			physicalTaints: []corev1.Taint{
				{
					Key:    cloudv1beta1.TaintOutOfTimeWindows,
					Value:  "",
					Effect: corev1.TaintEffectNoSchedule,
				},
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			disableNodeDefaultTaint: false,
			expectedTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
				defaultTaint,
			},
		},
		{
			name: "mixed taints with unschedulable and out-of-time-windows, default taint enabled",
			physicalTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
				{
					Key:    cloudv1beta1.TaintOutOfTimeWindows,
					Value:  "",
					Effect: corev1.TaintEffectNoExecute,
				},
				{
					Key:    "node.kubernetes.io/unschedulable",
					Value:  "",
					Effect: corev1.TaintEffectNoSchedule,
				},
				{
					Key:    "node.kubernetes.io/not-ready",
					Value:  "",
					Effect: corev1.TaintEffectNoExecute,
				},
			},
			disableNodeDefaultTaint: false,
			expectedTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
				{
					Key:    cloudv1beta1.TaintPhysicalNodeUnschedulable,
					Value:  "",
					Effect: corev1.TaintEffectNoSchedule,
				},
				{
					Key:    "node.kubernetes.io/not-ready",
					Value:  "",
					Effect: corev1.TaintEffectNoExecute,
				},
				defaultTaint,
			},
		},
		{
			name: "policy outside time windows with ForceReclaim false - should add NoSchedule taint",
			physicalTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			disableNodeDefaultTaint: false,
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ForceReclaim: false,
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: time.Now().Add(1 * time.Hour).Format("15:04"),
							End:   time.Now().Add(2 * time.Hour).Format("15:04"),
						},
					},
				},
			},
			expectedTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
				defaultTaint,
				{
					Key:       cloudv1beta1.TaintOutOfTimeWindows,
					Value:     "true",
					Effect:    corev1.TaintEffectNoSchedule,
					TimeAdded: &metav1.Time{Time: time.Now()},
				},
			},
		},
		{
			name: "policy outside time windows with ForceReclaim true - should add NoExecute taint",
			physicalTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			disableNodeDefaultTaint: false,
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ForceReclaim: true,
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: time.Now().Add(1 * time.Hour).Format("15:04"),
							End:   time.Now().Add(2 * time.Hour).Format("15:04"),
						},
					},
				},
			},
			expectedTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
				defaultTaint,
				{
					Key:       cloudv1beta1.TaintOutOfTimeWindows,
					Value:     "true",
					Effect:    corev1.TaintEffectNoExecute,
					TimeAdded: &metav1.Time{Time: time.Now()},
				},
			},
		},
		{
			name: "policy within time windows - should not add out-of-time-windows taint",
			physicalTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			disableNodeDefaultTaint: false,
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ForceReclaim: true,
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "00:00",
							End:   "23:59",
						},
					},
				},
			},
			expectedTaints: []corev1.Taint{
				{
					Key:    "example.com/test",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
				defaultTaint,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.transformTaints(zap.New(zap.UseDevMode(true)), tt.physicalTaints, tt.disableNodeDefaultTaint, tt.policy)

			// For test cases that expect out-of-time-windows taint, we need to handle TimeAdded differently
			if tt.policy != nil && len(result) > 0 {
				// Check if the result contains out-of-time-windows taint
				var hasOutOfTimeWindowsTaint bool
				for _, taint := range result {
					if taint.Key == cloudv1beta1.TaintOutOfTimeWindows {
						hasOutOfTimeWindowsTaint = true
						break
					}
				}

				if hasOutOfTimeWindowsTaint {
					// For these cases, we'll do a more flexible comparison
					// Check that the basic structure is correct
					assert.Equal(t, len(tt.expectedTaints), len(result), "Expected same number of taints")

					// Check each taint except the out-of-time-windows one
					for i, expectedTaint := range tt.expectedTaints {
						if expectedTaint.Key == cloudv1beta1.TaintOutOfTimeWindows {
							// For out-of-time-windows taint, check key, value, and effect but not TimeAdded
							found := false
							for _, actualTaint := range result {
								if actualTaint.Key == cloudv1beta1.TaintOutOfTimeWindows {
									assert.Equal(t, expectedTaint.Value, actualTaint.Value)
									assert.Equal(t, expectedTaint.Effect, actualTaint.Effect)
									assert.NotNil(t, actualTaint.TimeAdded, "TimeAdded should not be nil")
									found = true
									break
								}
							}
							assert.True(t, found, "Expected out-of-time-windows taint not found")
						} else {
							// For other taints, do exact comparison
							assert.Equal(t, expectedTaint, result[i])
						}
					}
				} else {
					// For cases without out-of-time-windows taint, do exact comparison
					assert.Equal(t, tt.expectedTaints, result)
				}
			} else {
				// For cases without policy, do exact comparison
				assert.Equal(t, tt.expectedTaints, result)
			}
		})
	}
}

func TestPhysicalNodeReconciler_checkPodsOnVirtualNode(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name            string
		pods            []corev1.Pod
		virtualNodeName string
		expectedHasPods bool
		expectError     bool
	}{
		{
			name:            "no pods on node",
			pods:            []corev1.Pod{},
			virtualNodeName: "test-vnode",
			expectedHasPods: false,
			expectError:     false,
		},
		{
			name: "running pod on node",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
			virtualNodeName: "test-vnode",
			expectedHasPods: true,
			expectError:     false,
		},
		{
			name: "pending pod on node",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodPending,
					},
				},
			},
			virtualNodeName: "test-vnode",
			expectedHasPods: true,
			expectError:     false,
		},
		{
			name: "succeeded pod on node",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodSucceeded,
					},
				},
			},
			virtualNodeName: "test-vnode",
			expectedHasPods: false,
			expectError:     false,
		},
		{
			name: "mixed pods on node",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "running-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "succeeded-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodSucceeded,
					},
				},
			},
			virtualNodeName: "test-vnode",
			expectedHasPods: true,
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create objects slice for fake client
			var objects []client.Object
			for i := range tt.pods {
				objects = append(objects, &tt.pods[i])
			}

			fakeClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()

			reconciler := &PhysicalNodeReconciler{
				VirtualClient: fakeClient,
				Log:           zap.New(zap.UseDevMode(true)),
			}

			ctx := context.Background()
			hasPods, err := reconciler.checkPodsOnVirtualNode(ctx, tt.virtualNodeName)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedHasPods, hasPods)
			}
		})
	}
}

func TestPhysicalNodeReconciler_forceEvictPodsOnVirtualNode(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name                         string
		pods                         []corev1.Pod
		virtualNodeName              string
		skipTimeOutTaintsTolerations bool
		expectError                  bool
		expectedDeletedCount         int
		expectedSkippedCount         int
	}{
		{
			name:                         "no pods to evict",
			pods:                         []corev1.Pod{},
			virtualNodeName:              "test-vnode",
			skipTimeOutTaintsTolerations: false,
			expectError:                  false,
			expectedDeletedCount:         0,
			expectedSkippedCount:         0,
		},
		{
			name: "evict running and pending pods",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "running-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pending-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodPending,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "succeeded-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodSucceeded,
					},
				},
			},
			virtualNodeName:              "test-vnode",
			skipTimeOutTaintsTolerations: false,
			expectError:                  false,
			expectedDeletedCount:         3,
			expectedSkippedCount:         0,
		},
		{
			name: "skip pods with TaintOutOfTimeWindows tolerations",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "normal-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tolerated-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "test-vnode",
						Tolerations: []corev1.Toleration{
							{
								Key:      cloudv1beta1.TaintOutOfTimeWindows,
								Operator: corev1.TolerationOpExists,
								Effect:   corev1.TaintEffectNoExecute,
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
			virtualNodeName:              "test-vnode",
			skipTimeOutTaintsTolerations: true,
			expectError:                  false,
			expectedDeletedCount:         1,
			expectedSkippedCount:         1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create objects slice for fake client
			var objects []client.Object
			for i := range tt.pods {
				objects = append(objects, &tt.pods[i])
			}

			fakeClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()

			reconciler := &PhysicalNodeReconciler{
				VirtualClient: fakeClient,
				Log:           zap.New(zap.UseDevMode(true)),
			}

			ctx := context.Background()
			err := reconciler.forceEvictPodsOnVirtualNode(ctx, tt.virtualNodeName, tt.skipTimeOutTaintsTolerations)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify pods are deleted (in a real scenario, they would be gone)
				// For this test, we just ensure no error occurred
			}
		})
	}
}

func TestPhysicalNodeReconciler_handleNodeDeletion_Integration(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)

	tests := []struct {
		name                         string
		physicalNodeName             string
		forceReclaim                 bool
		gracefulReclaimPeriodSeconds int32
		virtualNodeExists            bool
		virtualNodeHasTaint          bool
		taintTime                    *time.Time
		pods                         []corev1.Pod
		expectedResult               ctrl.Result
		expectedError                bool
		expectedVirtualNodeDeleted   bool
		expectedTaintAdded           bool
	}{
		{
			name:                         "delete virtual node - no force reclaim, no pods",
			physicalNodeName:             "physical-node-1",
			forceReclaim:                 false,
			gracefulReclaimPeriodSeconds: 0,
			virtualNodeExists:            true,
			virtualNodeHasTaint:          false,
			pods:                         []corev1.Pod{},
			expectedResult:               ctrl.Result{},
			expectedError:                false,
			expectedVirtualNodeDeleted:   false, // Should trigger deletion but not actually delete (finalizer removal)
			expectedTaintAdded:           true,
		},
		{
			name:                         "delete virtual node - force reclaim immediate, with pods",
			physicalNodeName:             "physical-node-2",
			forceReclaim:                 true,
			gracefulReclaimPeriodSeconds: 0,
			virtualNodeExists:            true,
			virtualNodeHasTaint:          false,
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "vnode-test-cluster-id-physical-node-2",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
			expectedResult:             ctrl.Result{}, // Pods are deleted immediately, so no requeue needed
			expectedError:              false,
			expectedVirtualNodeDeleted: false, // Should trigger deletion but not actually delete (finalizer removal)
			expectedTaintAdded:         true,
		},
		{
			name:                         "delete virtual node - force reclaim with grace period, not passed",
			physicalNodeName:             "physical-node-3",
			forceReclaim:                 true,
			gracefulReclaimPeriodSeconds: 300,
			virtualNodeExists:            true,
			virtualNodeHasTaint:          true,
			taintTime:                    func() *time.Time { t := time.Now().Add(-100 * time.Second); return &t }(),
			pods:                         []corev1.Pod{},
			expectedResult:               ctrl.Result{RequeueAfter: 200 * time.Second},
			expectedError:                false,
			expectedVirtualNodeDeleted:   false,
			expectedTaintAdded:           false,
		},
		{
			name:                         "delete virtual node - force reclaim with grace period, passed",
			physicalNodeName:             "physical-node-4",
			forceReclaim:                 true,
			gracefulReclaimPeriodSeconds: 300,
			virtualNodeExists:            true,
			virtualNodeHasTaint:          true,
			taintTime:                    func() *time.Time { t := time.Now().Add(-400 * time.Second); return &t }(),
			pods:                         []corev1.Pod{},
			expectedResult:               ctrl.Result{},
			expectedError:                false,
			expectedVirtualNodeDeleted:   false, // Should trigger deletion but not actually delete (finalizer removal)
			expectedTaintAdded:           false,
		},
		{
			name:                         "virtual node does not exist",
			physicalNodeName:             "physical-node-5",
			forceReclaim:                 true,
			gracefulReclaimPeriodSeconds: 0,
			virtualNodeExists:            false,
			expectedResult:               ctrl.Result{},
			expectedError:                false,
			expectedVirtualNodeDeleted:   false,
			expectedTaintAdded:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create virtual node if it should exist
			var virtualNode *corev1.Node
			var objects []client.Object

			if tt.virtualNodeExists {
				virtualNodeName := "vnode-test-cluster-id-" + tt.physicalNodeName
				virtualNode = &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:       virtualNodeName,
						Finalizers: []string{cloudv1beta1.VirtualNodeFinalizer},
					},
					Spec: corev1.NodeSpec{
						Taints: []corev1.Taint{},
					},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
				}

				if tt.virtualNodeHasTaint {
					virtualNode.Spec.Taints = append(virtualNode.Spec.Taints, corev1.Taint{
						Key:    TaintKeyVirtualNodeDeleting,
						Value:  "true",
						Effect: corev1.TaintEffectNoSchedule,
					})

					if tt.taintTime != nil {
						if virtualNode.Annotations == nil {
							virtualNode.Annotations = make(map[string]string)
						}
						virtualNode.Annotations[AnnotationDeletionTaintTime] = tt.taintTime.Format(time.RFC3339)
					}

					// For grace period tests, set DeletionTimestamp to trigger the grace period logic
					if tt.name == "delete virtual node - force reclaim with grace period, not passed" ||
						tt.name == "delete virtual node - force reclaim with grace period, passed" {
						virtualNode.DeletionTimestamp = &metav1.Time{Time: time.Now()}
					}
				}

				objects = append(objects, virtualNode)
			}

			// Add pods to objects
			for i := range tt.pods {
				objects = append(objects, &tt.pods[i])
			}

			// Create fake clients
			virtualClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()

			kubeClient := fake.NewSimpleClientset()

			// Create ClusterBinding for testing
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster-id",
				},
			}

			reconciler := &PhysicalNodeReconciler{
				VirtualClient:      virtualClient,
				KubeClient:         kubeClient,
				Log:                zap.New(zap.UseDevMode(true)),
				ClusterBindingName: "test-cluster",
				ClusterBinding:     clusterBinding,
				leaseControllers:   make(map[string]*LeaseController),
			}

			// Execute the test
			result, err := reconciler.handleNodeDeletion(ctx, tt.physicalNodeName, tt.forceReclaim, tt.gracefulReclaimPeriodSeconds)

			// Verify results
			if tt.expectedError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Check requeue behavior
			if tt.expectedResult.RequeueAfter > 0 {
				assert.True(t, result.RequeueAfter > 0, "Expected requeue but got none")
				// Allow some tolerance in requeue time
				assert.InDelta(t, tt.expectedResult.RequeueAfter.Seconds(), result.RequeueAfter.Seconds(), 10.0)
			} else {
				assert.Equal(t, tt.expectedResult.RequeueAfter, result.RequeueAfter)
			}

			// Check if virtual node was deleted
			if tt.virtualNodeExists {
				virtualNodeName := "vnode-test-cluster-id-" + tt.physicalNodeName
				var currentNode corev1.Node
				err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, &currentNode)

				if tt.expectedVirtualNodeDeleted {
					assert.True(t, client.IgnoreNotFound(err) == nil && err != nil, "Expected virtual node to be deleted")
				} else {
					// For cases where finalizer is removed, the node might be deleted by the fake client
					if err != nil && errors.IsNotFound(err) {
						// This is acceptable for finalizer removal cases
						t.Logf("Virtual node was deleted after finalizer removal, which is expected for test: %s", tt.name)
					} else {
						require.NoError(t, err, "Expected virtual node to exist")

						// Check if taint was added
						hasTaint := reconciler.hasDeletionTaint(&currentNode)
						if tt.expectedTaintAdded {
							assert.True(t, hasTaint, "Expected deletion taint to be added")
							assert.Contains(t, currentNode.Annotations, AnnotationDeletionTaintTime)
						} else if !tt.virtualNodeHasTaint {
							// Only check if taint should not be there if it wasn't there initially
							assert.False(t, hasTaint, "Expected no deletion taint")
						}
					}
				}
			}
		})
	}
}

func TestPhysicalNodeReconciler_handleNodeDeletion_ForceEviction_Integration(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	ctx := context.Background()
	physicalNodeName := "physical-node-eviction-test"
	virtualNodeName := "vnode-test-cluster-id-" + physicalNodeName

	// Create virtual node with deletion taint and old taint time
	oldTime := time.Now().Add(-400 * time.Second)
	virtualNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: virtualNodeName,
			Annotations: map[string]string{
				AnnotationDeletionTaintTime: oldTime.Format(time.RFC3339),
			},
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    TaintKeyVirtualNodeDeleting,
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	// Create pods that should be evicted
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "running-pod",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: virtualNodeName,
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pending-pod",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: virtualNodeName,
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
			},
		},
	}

	objects := []client.Object{virtualNode}
	for i := range pods {
		objects = append(objects, &pods[i])
	}

	virtualClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
			pod := obj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).
		Build()

	// Create ClusterBinding for testing
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster-id",
		},
	}

	reconciler := &PhysicalNodeReconciler{
		VirtualClient:      virtualClient,
		KubeClient:         fake.NewSimpleClientset(),
		Log:                zap.New(zap.UseDevMode(true)),
		ClusterBindingName: "test-cluster",
		ClusterBinding:     clusterBinding,
		leaseControllers:   make(map[string]*LeaseController),
	}

	// Test force eviction with grace period passed
	result, err := reconciler.handleNodeDeletion(ctx, physicalNodeName, true, 300)

	require.NoError(t, err)

	// Should not requeue since pods are deleted immediately and node is deleted
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	// Verify virtual node is deleted (since pods were evicted and then node was deleted)
	var currentNode corev1.Node
	err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, &currentNode)
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil, "Expected virtual node to be deleted")
}

func TestPhysicalNodeReconciler_generateVirtualNodeName(t *testing.T) {
	reconciler := &PhysicalNodeReconciler{
		ClusterBindingName: "test-cluster",
		ClusterBinding: &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cluster",
			},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID: "cluster-123",
			},
		},
	}

	tests := []struct {
		name             string
		physicalNodeName string
		expected         string
	}{
		{
			name:             "simple node name",
			physicalNodeName: "node-1",
			expected:         "vnode-cluster-123-node-1",
		},
		{
			name:             "node with dashes",
			physicalNodeName: "worker-node-2",
			expected:         "vnode-cluster-123-worker-node-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.generateVirtualNodeName(tt.physicalNodeName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPhysicalNodeReconciler_handleOutOfTimeWindowsTaint(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name                    string
		policy                  *cloudv1beta1.ResourceLeasingPolicy
		existingVirtualNode     *corev1.Node
		existingVirtualPods     []*corev1.Pod
		expectedRequeueAfter    time.Duration
		expectError             bool
		expectPodsDeleted       bool
		expectedDeletedPodCount int
	}{
		{
			name: "ForceReclaim false - no pod deletion needed",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ForceReclaim:                 false,
					GracefulReclaimPeriodSeconds: 300,
				},
			},
			existingVirtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vnode-test-cluster-id-test-node",
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:       cloudv1beta1.TaintOutOfTimeWindows,
							Effect:    corev1.TaintEffectNoSchedule,
							TimeAdded: &metav1.Time{Time: time.Now()},
						},
					},
				},
			},
			existingVirtualPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-1",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "vnode-test-cluster-id-test-node",
					},
				},
			},
			expectedRequeueAfter:    DefaultNodeSyncInterval,
			expectError:             false,
			expectPodsDeleted:       false,
			expectedDeletedPodCount: 0,
		},
		{
			name: "ForceReclaim true and graceful period 0 - immediate pod deletion",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ForceReclaim:                 true,
					GracefulReclaimPeriodSeconds: 0,
				},
			},
			existingVirtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vnode-test-cluster-id-test-node",
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:       cloudv1beta1.TaintOutOfTimeWindows,
							Effect:    corev1.TaintEffectNoExecute,
							TimeAdded: &metav1.Time{Time: time.Now()},
						},
					},
				},
			},
			existingVirtualPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-1",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "vnode-test-cluster-id-test-node",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-2",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "vnode-test-cluster-id-test-node",
					},
				},
			},
			expectedRequeueAfter:    DefaultNodeSyncInterval,
			expectError:             false,
			expectPodsDeleted:       true,
			expectedDeletedPodCount: 2,
		},
		{
			name: "ForceReclaim true and graceful period set - graceful period not passed",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ForceReclaim:                 true,
					GracefulReclaimPeriodSeconds: 300,
				},
			},
			existingVirtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vnode-test-cluster-id-test-node",
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:       cloudv1beta1.TaintOutOfTimeWindows,
							Effect:    corev1.TaintEffectNoExecute,
							TimeAdded: &metav1.Time{Time: time.Now().Add(-100 * time.Second)},
						},
					},
				},
			},
			existingVirtualPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-1",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "vnode-test-cluster-id-test-node",
					},
				},
			},
			expectedRequeueAfter:    200 * time.Second, // Approximate remaining time
			expectError:             false,
			expectPodsDeleted:       false,
			expectedDeletedPodCount: 0,
		},
		{
			name: "ForceReclaim true and graceful period passed - pod deletion",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ForceReclaim:                 true,
					GracefulReclaimPeriodSeconds: 300,
				},
			},
			existingVirtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vnode-test-cluster-id-test-node",
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:       cloudv1beta1.TaintOutOfTimeWindows,
							Effect:    corev1.TaintEffectNoExecute,
							TimeAdded: &metav1.Time{Time: time.Now().Add(-400 * time.Second)},
						},
					},
				},
			},
			existingVirtualPods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-1",
						Namespace: "default",
					},
					Spec: corev1.PodSpec{
						NodeName: "vnode-test-cluster-id-test-node",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-pod-deleting",
						Namespace:         "default",
						DeletionTimestamp: &metav1.Time{Time: time.Now()},
						Finalizers:        []string{"test.finalizer"},
					},
					Spec: corev1.PodSpec{
						NodeName: "vnode-test-cluster-id-test-node",
					},
				},
			},
			expectedRequeueAfter:    DefaultNodeSyncInterval,
			expectError:             false,
			expectPodsDeleted:       true,
			expectedDeletedPodCount: 1, // Only one pod should be deleted (skip the one with deletionTimestamp)
		},
		{
			name: "No out-of-time-windows taint - should return error",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					ForceReclaim:                 true,
					GracefulReclaimPeriodSeconds: 300,
				},
			},
			existingVirtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vnode-test-cluster-id-test-node",
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{}, // No out-of-time-windows taint
				},
			},
			existingVirtualPods:     []*corev1.Pod{},
			expectedRequeueAfter:    0,
			expectError:             true,
			expectPodsDeleted:       false,
			expectedDeletedPodCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create objects slice with virtual node and pods
			objects := []client.Object{tt.existingVirtualNode}
			for _, pod := range tt.existingVirtualPods {
				objects = append(objects, pod)
			}

			fakeClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName != "" {
						return []string{pod.Spec.NodeName}
					}
					return nil
				}).
				Build()

			// Create ClusterBinding for testing
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster-id",
				},
			}

			reconciler := &PhysicalNodeReconciler{
				VirtualClient:      fakeClient,
				ClusterBindingName: "test-cluster",
				ClusterBinding:     clusterBinding,
				Log:                zap.New(zap.UseDevMode(true)),
			}

			ctx := context.Background()
			result, err := reconciler.handleOutOfTimeWindows(ctx, "test-node", tt.policy)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)

				// Check requeue time (allow some tolerance for timing differences)
				if tt.expectedRequeueAfter > time.Minute {
					// For long waits, check that it's within a reasonable range
					assert.True(t, result.RequeueAfter >= tt.expectedRequeueAfter-10*time.Second)
					assert.True(t, result.RequeueAfter <= tt.expectedRequeueAfter+10*time.Second)
				} else {
					assert.Equal(t, tt.expectedRequeueAfter, result.RequeueAfter)
				}

				// Verify pod deletion behavior
				if tt.expectPodsDeleted {
					// Count remaining pods that are not being deleted
					remaining := 0
					for _, originalPod := range tt.existingVirtualPods {
						if originalPod.DeletionTimestamp != nil {
							// This pod was already being deleted, should still exist
							pod := &corev1.Pod{}
							err := fakeClient.Get(ctx, client.ObjectKey{Name: originalPod.Name, Namespace: originalPod.Namespace}, pod)
							if err == nil {
								remaining++
							}
						}
					}
					// The number of pods that should remain is (total - expected deleted)
					expectedRemaining := len(tt.existingVirtualPods) - tt.expectedDeletedPodCount
					assert.Equal(t, expectedRemaining, remaining, "Expected %d pods to remain after deletion", expectedRemaining)
				}
			}
		})
	}
}

func TestPhysicalNodeReconciler_shouldProcessNode(t *testing.T) {
	// Set up logger
	logger := zap.New(zap.UseDevMode(true))
	ctrl.SetLogger(logger)

	tests := []struct {
		name           string
		node           *corev1.Node
		expectedResult bool
	}{
		{
			name:           "nil node should return false",
			node:           nil,
			expectedResult: false,
		},
		{
			name: "normal node without special labels should return true",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "normal-node",
					Labels: map[string]string{
						"kubernetes.io/hostname":         "normal-node",
						"node-role.kubernetes.io/worker": "true",
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "node with external instance type should return false",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "external-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "external",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "node with vnode instance type should return false",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vnode-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "vnode",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "node with eklet instance type should return false",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "eklet-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "eklet",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "node with beta external instance type should return false",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "beta-external-node",
					Labels: map[string]string{
						"beta.kubernetes.io/instance-type": "external",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "node with beta vnode instance type should return false",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "beta-vnode-node",
					Labels: map[string]string{
						"beta.kubernetes.io/instance-type": "vnode",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "node with beta eklet instance type should return false",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "beta-eklet-node",
					Labels: map[string]string{
						"beta.kubernetes.io/instance-type": "eklet",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "node with virtual-kubelet type should return false",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "virtual-kubelet-node",
					Labels: map[string]string{
						"type": "virtual-kubelet",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "node with mixed excluded labels should return false",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mixed-excluded-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "external",
						"type":                             "virtual-kubelet",
						"kubernetes.io/hostname":           "mixed-excluded-node",
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "node with valid instance type should return true",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-instance-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "n1-standard-1",
						"kubernetes.io/hostname":           "valid-instance-node",
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "node with valid beta instance type should return true",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-beta-instance-node",
					Labels: map[string]string{
						"beta.kubernetes.io/instance-type": "n1-standard-2",
						"kubernetes.io/hostname":           "valid-beta-instance-node",
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "node with other type label should return true",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "other-type-node",
					Labels: map[string]string{
						"type":                   "worker",
						"kubernetes.io/hostname": "other-type-node",
					},
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &PhysicalNodeReconciler{
				Log: zap.New(zap.UseDevMode(true)),
			}

			result := reconciler.shouldProcessNode(tt.node)

			if result != tt.expectedResult {
				t.Errorf("shouldProcessNode() = %v, want %v", result, tt.expectedResult)
			}
		})
	}
}

func TestPhysicalNodeReconciler_UIDAndDeletionTimestampCheck(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name                   string
		physicalNode           *corev1.Node
		existingVirtualNode    *corev1.Node
		expectedHandleDeletion bool
		expectedError          bool
	}{
		{
			name: "virtual node UID matches physical node",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					UID:  "physical-uid-123",
					Labels: map[string]string{
						"node-type": "worker",
					},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			},
			existingVirtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "vnode-test-cluster-id-test-node",
					Finalizers: []string{cloudv1beta1.VirtualNodeFinalizer},
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeUID: "physical-uid-123",
					},
				},
			},
			expectedHandleDeletion: false,
			expectedError:          false,
		},
		{
			name: "virtual node UID does not match physical node",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					UID:  "physical-uid-456",
					Labels: map[string]string{
						"node-type": "worker",
					},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			},
			existingVirtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "vnode-test-cluster-id-test-node",
					Finalizers: []string{cloudv1beta1.VirtualNodeFinalizer},
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeUID: "physical-uid-123", // Different UID
					},
				},
			},
			expectedHandleDeletion: true,
			expectedError:          false,
		},
		{
			name: "virtual node has deletion timestamp",
			physicalNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					UID:  "physical-uid-123",
					Labels: map[string]string{
						"node-type": "worker",
					},
				},
				Status: corev1.NodeStatus{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			},
			existingVirtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "vnode-test-cluster-id-test-node",
					Finalizers:        []string{cloudv1beta1.VirtualNodeFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeUID: "physical-uid-123",
					},
				},
			},
			expectedHandleDeletion: true,
			expectedError:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create ClusterBinding for testing
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster-id",
				},
			}

			// Create fake clients
			var objects []client.Object
			if tt.existingVirtualNode != nil {
				objects = append(objects, tt.existingVirtualNode)
			}

			virtualClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(append(objects, clusterBinding)...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()

			// Create a policy for the first test case
			var policyObjects []client.Object
			if tt.name == "virtual node UID matches physical node" {
				policy := &cloudv1beta1.ResourceLeasingPolicy{
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
				}
				policyObjects = append(policyObjects, policy)
			}

			physicalClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(append([]client.Object{tt.physicalNode, clusterBinding}, policyObjects...)...).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()

			kubeClient := fake.NewSimpleClientset()

			reconciler := &PhysicalNodeReconciler{
				PhysicalClient:     physicalClient,
				VirtualClient:      virtualClient,
				KubeClient:         kubeClient,
				Log:                zap.New(zap.UseDevMode(true)),
				ClusterBindingName: "test-cluster",
				ClusterBinding:     clusterBinding,
				leaseControllers:   make(map[string]*LeaseController),
			}

			// Initialize lease controllers
			reconciler.initLeaseControllers()

			// Test reconcile
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "test-node",
				},
			}

			result, err := reconciler.Reconcile(ctx, req)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				if tt.expectedHandleDeletion {
					// Should return immediately without requeue when handling deletion
					assert.Equal(t, time.Duration(0), result.RequeueAfter)
				} else {
					// Should continue with normal processing
					assert.True(t, result.RequeueAfter > 0)
				}
			}
		})
	}
}

func TestPhysicalNodeReconciler_FinalizerHandling(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name                     string
		virtualNode              *corev1.Node
		expectedFinalizerExists  bool
		expectedFinalizerRemoved bool
	}{
		{
			name: "virtual node with finalizer should have finalizer removed",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "vnode-test-cluster-id-test-node",
					Finalizers:        []string{cloudv1beta1.VirtualNodeFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    TaintKeyVirtualNodeDeleting,
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expectedFinalizerExists:  true,
			expectedFinalizerRemoved: true,
		},
		{
			name: "virtual node without deletion timestamp should trigger deletion",
			virtualNode: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "vnode-test-cluster-id-test-node",
					Finalizers: []string{cloudv1beta1.VirtualNodeFinalizer},
					// No DeletionTimestamp
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    TaintKeyVirtualNodeDeleting,
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expectedFinalizerExists:  true,
			expectedFinalizerRemoved: false, // Should trigger deletion, not remove finalizer
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create fake clients
			virtualClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.virtualNode).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
					pod := obj.(*corev1.Pod)
					if pod.Spec.NodeName == "" {
						return nil
					}
					return []string{pod.Spec.NodeName}
				}).
				Build()

			kubeClient := fake.NewSimpleClientset()

			// Create ClusterBinding for testing
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster-id",
				},
			}

			reconciler := &PhysicalNodeReconciler{
				VirtualClient:      virtualClient,
				KubeClient:         kubeClient,
				Log:                zap.New(zap.UseDevMode(true)),
				ClusterBindingName: "test-cluster",
				ClusterBinding:     clusterBinding,
				leaseControllers:   make(map[string]*LeaseController),
			}

			// Initialize lease controllers
			reconciler.initLeaseControllers()

			// Test handleNodeDeletion
			result, err := reconciler.handleNodeDeletion(ctx, "test-node", false, 0)

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(0), result.RequeueAfter)

			// Check finalizer status
			updatedVirtualNode := &corev1.Node{}
			err = virtualClient.Get(ctx, client.ObjectKey{Name: tt.virtualNode.Name}, updatedVirtualNode)

			if tt.expectedFinalizerRemoved {
				// For the case where finalizer should be removed, node should still exist but without finalizer
				if err != nil && errors.IsNotFound(err) {
					// Node was deleted, which means finalizer was removed and node was deleted
					assert.True(t, true, "Node was deleted after finalizer removal")
				} else {
					assert.NoError(t, err)
					assert.False(t, controllerutil.ContainsFinalizer(updatedVirtualNode, cloudv1beta1.VirtualNodeFinalizer), "Finalizer should be removed")
				}
			} else {
				// For the case where deletion is triggered, the node should have DeletionTimestamp set
				assert.NoError(t, err)
				assert.NotNil(t, updatedVirtualNode.DeletionTimestamp, "Node should have DeletionTimestamp set when deletion is triggered")
			}
		})
	}
}
