package syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

// mockBottomUpSyncer is a test mock that implements BottomUpSyncerInterface
type mockBottomUpSyncer struct {
	clusterBinding   *cloudv1beta1.ClusterBinding
	getNodesFunc     func(ctx context.Context, selector *corev1.NodeSelector) ([]string, error)
	requeueNodesFunc func(nodeNames []string) error
}

func newMockBottomUpSyncer(binding *cloudv1beta1.ClusterBinding) *mockBottomUpSyncer {
	return &mockBottomUpSyncer{
		clusterBinding: binding,
	}
}

func (m *mockBottomUpSyncer) GetNodesMatchingSelector(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
	if m.getNodesFunc != nil {
		return m.getNodesFunc(ctx, selector)
	}
	// Default behavior: return empty list for tests
	return []string{}, nil
}

func (m *mockBottomUpSyncer) RequeueNodes(nodeNames []string) error {
	if m.requeueNodesFunc != nil {
		return m.requeueNodesFunc(nodeNames)
	}
	// Default behavior: do nothing for tests
	return nil
}

func (m *mockBottomUpSyncer) GetClusterBinding() *cloudv1beta1.ClusterBinding {
	return m.clusterBinding
}

func (m *mockBottomUpSyncer) SetClusterBinding(binding *cloudv1beta1.ClusterBinding) {
	m.clusterBinding = binding
}

// setupTestEnvironment creates a test environment for ClusterBindingReconciler tests
func setupTestEnvironment(t *testing.T) *ClusterBindingReconciler {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	virtualClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	physicalClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create a mock BottomUpSyncer for testing
	mockBottomUp := newMockBottomUpSyncer(nil) // Start with nil to test first time loading

	reconciler := &ClusterBindingReconciler{
		Client:                  virtualClient,
		Log:                     zap.New(zap.UseDevMode(true)),
		ClusterBindingName:      "test-cluster-binding",
		ClusterBindingNamespace: "default",
		PhysicalClient:          physicalClient,
		BottomUpSyncer:          mockBottomUp,
	}

	return reconciler
}

// setupTestEnvironmentWithExistingBinding creates a test environment with an existing ClusterBinding
func setupTestEnvironmentWithExistingBinding(t *testing.T) *ClusterBindingReconciler {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	virtualClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	physicalClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	existingBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-binding",
			Namespace: "default",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
			NodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/os",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
						},
					},
				},
			},
			DisableNodeDefaultTaint: false,
		},
	}

	// Create a mock BottomUpSyncer with existing binding
	mockBottomUp := newMockBottomUpSyncer(existingBinding)

	reconciler := &ClusterBindingReconciler{
		Client:                  virtualClient,
		Log:                     zap.New(zap.UseDevMode(true)),
		ClusterBindingName:      "test-cluster-binding",
		ClusterBindingNamespace: "default",
		PhysicalClient:          physicalClient,
		BottomUpSyncer:          mockBottomUp,
	}

	return reconciler
}

// TestClusterBindingReconciler_hasNodeSelectorChanged tests the hasNodeSelectorChanged method
func TestClusterBindingReconciler_hasNodeSelectorChanged(t *testing.T) {
	reconciler := setupTestEnvironment(t)

	tests := []struct {
		name           string
		newBinding     *cloudv1beta1.ClusterBinding
		expectedResult bool
	}{
		{
			name: "first time loading - no existing binding",
			newBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/os",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"linux"},
									},
								},
							},
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name: "nodeSelector changed",
			newBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/os",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"windows"},
									},
								},
							},
						},
					},
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.hasNodeSelectorChanged(tt.newBinding)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestClusterBindingReconciler_hasDisableNodeDefaultTaintChanged tests the hasDisableNodeDefaultTaintChanged method
func TestClusterBindingReconciler_hasDisableNodeDefaultTaintChanged(t *testing.T) {
	reconciler := setupTestEnvironment(t)

	tests := []struct {
		name           string
		newBinding     *cloudv1beta1.ClusterBinding
		expectedResult bool
	}{
		{
			name: "first time loading - no existing binding",
			newBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					DisableNodeDefaultTaint: true,
				},
			},
			expectedResult: true,
		},
		{
			name: "disableNodeDefaultTaint changed from false to true",
			newBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					DisableNodeDefaultTaint: true,
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.hasDisableNodeDefaultTaintChanged(tt.newBinding)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestClusterBindingReconciler_unionAndDeduplicateNodes tests the unionAndDeduplicateNodes method
func TestClusterBindingReconciler_unionAndDeduplicateNodes(t *testing.T) {
	reconciler := setupTestEnvironment(t)

	tests := []struct {
		name     string
		oldNodes []string
		newNodes []string
		expected []string
	}{
		{
			name:     "empty slices",
			oldNodes: []string{},
			newNodes: []string{},
			expected: []string{},
		},
		{
			name:     "only old nodes",
			oldNodes: []string{"node1", "node2"},
			newNodes: []string{},
			expected: []string{"node1", "node2"},
		},
		{
			name:     "only new nodes",
			oldNodes: []string{},
			newNodes: []string{"node3", "node4"},
			expected: []string{"node3", "node4"},
		},
		{
			name:     "no duplicates",
			oldNodes: []string{"node1", "node2"},
			newNodes: []string{"node3", "node4"},
			expected: []string{"node1", "node2", "node3", "node4"},
		},
		{
			name:     "with duplicates",
			oldNodes: []string{"node1", "node2"},
			newNodes: []string{"node2", "node3"},
			expected: []string{"node1", "node2", "node3"},
		},
		{
			name:     "all duplicates",
			oldNodes: []string{"node1", "node2"},
			newNodes: []string{"node1", "node2"},
			expected: []string{"node1", "node2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.unionAndDeduplicateNodes(tt.oldNodes, tt.newNodes)
			// Sort both slices for comparison since order doesn't matter
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

// TestClusterBindingReconciler_hasDisableNodeDefaultTaintChanged_WithExistingBinding tests the hasDisableNodeDefaultTaintChanged method with existing binding
func TestClusterBindingReconciler_hasDisableNodeDefaultTaintChanged_WithExistingBinding(t *testing.T) {
	reconciler := setupTestEnvironmentWithExistingBinding(t)

	tests := []struct {
		name           string
		newBinding     *cloudv1beta1.ClusterBinding
		expectedResult bool
	}{
		{
			name: "disableNodeDefaultTaint unchanged",
			newBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					DisableNodeDefaultTaint: false,
				},
			},
			expectedResult: false,
		},
		{
			name: "disableNodeDefaultTaint changed from false to true",
			newBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					DisableNodeDefaultTaint: true,
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.hasDisableNodeDefaultTaintChanged(tt.newBinding)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestClusterBindingReconciler_hasNodeSelectorChanged_WithExistingBinding tests the hasNodeSelectorChanged method with existing binding
func TestClusterBindingReconciler_hasNodeSelectorChanged_WithExistingBinding(t *testing.T) {
	reconciler := setupTestEnvironmentWithExistingBinding(t)

	tests := []struct {
		name           string
		newBinding     *cloudv1beta1.ClusterBinding
		expectedResult bool
	}{
		{
			name: "nodeSelector unchanged",
			newBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/os",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"linux"},
									},
								},
							},
						},
					},
				},
			},
			expectedResult: false,
		},
		{
			name: "nodeSelector changed",
			newBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/os",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"windows"},
									},
								},
							},
						},
					},
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.hasNodeSelectorChanged(tt.newBinding)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestClusterBindingReconciler_Reconcile tests the Reconcile method
func TestClusterBindingReconciler_Reconcile(t *testing.T) {
	reconciler := setupTestEnvironmentWithExistingBinding(t)

	// Create a test ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-binding",
			Namespace: "default",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
			NodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/os",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"linux"},
							},
						},
					},
				},
			},
			DisableNodeDefaultTaint: false,
		},
	}

	// Create the ClusterBinding in the client
	err := reconciler.Create(context.Background(), clusterBinding)
	require.NoError(t, err)

	tests := []struct {
		name           string
		requestName    string
		expectedResult ctrl.Result
		expectError    bool
	}{
		{
			name:           "handle our specific ClusterBinding - no changes",
			requestName:    "test-cluster-binding",
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name:           "ignore other ClusterBinding",
			requestName:    "other-cluster-binding",
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.requestName,
					Namespace: "default",
				},
			}

			result, err := reconciler.Reconcile(context.Background(), req)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestClusterBindingReconciler_Reconcile_FinalizerLogic tests the finalizer addition logic
func TestClusterBindingReconciler_Reconcile_FinalizerLogic(t *testing.T) {
	tests := []struct {
		name                 string
		setupClusterBinding  func() *cloudv1beta1.ClusterBinding
		expectFinalizerAdded bool
		expectError          bool
	}{
		{
			name: "add finalizer to ClusterBinding without finalizer",
			setupClusterBinding: func() *cloudv1beta1.ClusterBinding {
				return &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster-binding",
						Namespace: "default",
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID: "test-cluster",
					},
				}
			},
			expectFinalizerAdded: true,
			expectError:          false,
		},
		{
			name: "skip finalizer addition when already present",
			setupClusterBinding: func() *cloudv1beta1.ClusterBinding {
				return &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test-cluster-binding",
						Namespace:  "default",
						Finalizers: []string{cloudv1beta1.ClusterBindingSyncerFinalizer},
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID: "test-cluster",
					},
				}
			},
			expectFinalizerAdded: false,
			expectError:          false,
		},
		{
			name: "handle ClusterBinding not found",
			setupClusterBinding: func() *cloudv1beta1.ClusterBinding {
				return nil // Don't create the ClusterBinding
			},
			expectFinalizerAdded: false,
			expectError:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := setupTestEnvironment(t)

			var originalClusterBinding *cloudv1beta1.ClusterBinding
			if tt.setupClusterBinding != nil {
				originalClusterBinding = tt.setupClusterBinding()
				if originalClusterBinding != nil {
					err := reconciler.Create(context.Background(), originalClusterBinding)
					require.NoError(t, err)
				}
			}

			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-cluster-binding",
					Namespace: "default",
				},
			}

			result, err := reconciler.Reconcile(context.Background(), req)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if originalClusterBinding != nil {
				// Verify the finalizer state
				updatedClusterBinding := &cloudv1beta1.ClusterBinding{}
				err = reconciler.Get(context.Background(), types.NamespacedName{
					Name:      "test-cluster-binding",
					Namespace: "default",
				}, updatedClusterBinding)
				require.NoError(t, err)

				hasFinalizer := false
				for _, finalizer := range updatedClusterBinding.Finalizers {
					if finalizer == cloudv1beta1.ClusterBindingSyncerFinalizer {
						hasFinalizer = true
						break
					}
				}

				if tt.expectFinalizerAdded || (originalClusterBinding != nil && len(originalClusterBinding.Finalizers) > 0) {
					assert.True(t, hasFinalizer, "Expected finalizer to be present")
				}
			}

			// For the case where ClusterBinding is not found, we expect no requeue
			if originalClusterBinding == nil {
				assert.Equal(t, ctrl.Result{}, result)
			}
		})
	}
}

// TestClusterBindingReconciler_getVirtualNodesByClusterBinding tests the getVirtualNodesByClusterBinding method
func TestClusterBindingReconciler_getVirtualNodesByClusterBinding(t *testing.T) {
	tests := []struct {
		name           string
		setupNodes     func(*ClusterBindingReconciler)
		clusterBinding string
		expectedCount  int
		expectError    bool
	}{
		{
			name: "no nodes found",
			setupNodes: func(r *ClusterBindingReconciler) {
				// No nodes to create
			},
			clusterBinding: "test-cluster-binding",
			expectedCount:  0,
			expectError:    false,
		},
		{
			name: "single node found",
			setupNodes: func(r *ClusterBindingReconciler) {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node-1",
						Labels: map[string]string{
							cloudv1beta1.LabelClusterBinding: "test-cluster-binding",
						},
					},
				}
				err := r.Create(context.Background(), node)
				require.NoError(t, err)
			},
			clusterBinding: "test-cluster-binding",
			expectedCount:  1,
			expectError:    false,
		},
		{
			name: "multiple nodes found",
			setupNodes: func(r *ClusterBindingReconciler) {
				for i := 1; i <= 3; i++ {
					node := &corev1.Node{
						ObjectMeta: metav1.ObjectMeta{
							Name: fmt.Sprintf("test-node-%d", i),
							Labels: map[string]string{
								cloudv1beta1.LabelClusterBinding: "test-cluster-binding",
							},
						},
					}
					err := r.Create(context.Background(), node)
					require.NoError(t, err)
				}
			},
			clusterBinding: "test-cluster-binding",
			expectedCount:  3,
			expectError:    false,
		},
		{
			name: "nodes with different cluster binding",
			setupNodes: func(r *ClusterBindingReconciler) {
				// Node with matching label
				node1 := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node-1",
						Labels: map[string]string{
							cloudv1beta1.LabelClusterBinding: "test-cluster-binding",
						},
					},
				}
				err := r.Create(context.Background(), node1)
				require.NoError(t, err)

				// Node with different cluster binding
				node2 := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node-2",
						Labels: map[string]string{
							cloudv1beta1.LabelClusterBinding: "other-cluster-binding",
						},
					},
				}
				err = r.Create(context.Background(), node2)
				require.NoError(t, err)
			},
			clusterBinding: "test-cluster-binding",
			expectedCount:  1,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := setupTestEnvironment(t)
			tt.setupNodes(reconciler)

			nodes, err := reconciler.getVirtualNodesByClusterBinding(context.Background(), tt.clusterBinding)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedCount, len(nodes))

			// Verify all returned nodes have the correct label
			for _, node := range nodes {
				assert.Equal(t, tt.clusterBinding, node.Labels[cloudv1beta1.LabelClusterBinding])
			}
		})
	}
}

// TestClusterBindingReconciler_checkAndCleanVirtualResources tests the checkAndCleanVirtualResources method
func TestClusterBindingReconciler_checkAndCleanVirtualResources(t *testing.T) {
	tests := []struct {
		name           string
		setupResources func(*ClusterBindingReconciler)
		labelKey       string
		expectedResult string
		expectError    bool
	}{
		{
			name: "no resources found",
			setupResources: func(r *ClusterBindingReconciler) {
				// No resources to create
			},
			labelKey:       "kubeocean.io/synced-by-test-cluster",
			expectedResult: "",
			expectError:    false,
		},
		{
			name: "configmaps found",
			setupResources: func(r *ClusterBindingReconciler) {
				cm1 := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cm1",
						Namespace: "default",
						Labels: map[string]string{
							"kubeocean.io/synced-by-test-cluster": "true",
						},
					},
				}
				cm2 := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cm2",
						Namespace: "default",
						Labels: map[string]string{
							"kubeocean.io/synced-by-test-cluster": "true",
						},
					},
				}
				r.Create(context.Background(), cm1)
				r.Create(context.Background(), cm2)
			},
			labelKey:       "kubeocean.io/synced-by-test-cluster",
			expectedResult: "configmaps: [test-cm1, test-cm2]",
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := setupTestEnvironment(t)
			tt.setupResources(reconciler)

			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-binding",
					Namespace: "default",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			}
			result, err := reconciler.checkAndCleanVirtualResources(context.Background(), tt.labelKey, clusterBinding)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)

				// Additionally verify that clusterbinding-deleting annotation was added if resources exist
				if result != "" {
					// Check that the annotation was added to the resources
					var configMapList corev1.ConfigMapList
					labelSelector := labels.SelectorFromSet(map[string]string{tt.labelKey: cloudv1beta1.LabelValueTrue})
					err := reconciler.List(context.Background(), &configMapList, client.MatchingLabelsSelector{Selector: labelSelector})
					assert.NoError(t, err)

					for _, cm := range configMapList.Items {
						assert.NotNil(t, cm.Annotations, "ConfigMap should have annotations")
						annotationKey := cloudv1beta1.GetClusterBindingDeletingAnnotation("test-cluster")
						assert.Equal(t, "test-cluster-binding", cm.Annotations[annotationKey], "ConfigMap should have clusterbinding-deleting annotation")
					}
				}
			}
		})
	}
}

// TestClusterBindingReconciler_checkPhysicalResourcesExist tests the checkPhysicalResourcesExist method
func TestClusterBindingReconciler_checkPhysicalResourcesExist(t *testing.T) {
	tests := []struct {
		name           string
		setupResources func(*ClusterBindingReconciler)
		expectedResult string
		expectError    bool
	}{
		{
			name: "no resources found",
			setupResources: func(r *ClusterBindingReconciler) {
				// No resources to create
			},
			expectedResult: "",
			expectError:    false,
		},
		{
			name: "pods found",
			setupResources: func(r *ClusterBindingReconciler) {
				pod1 := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod1",
						Namespace: "default",
						Labels: map[string]string{
							"kubeocean.io/managed-by": "kubeocean",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "test:latest",
							},
						},
					},
				}
				r.PhysicalClient.Create(context.Background(), pod1)
			},
			expectedResult: "pods: [test-pod1]",
			expectError:    false,
		},
		{
			name: "physical client not available",
			setupResources: func(r *ClusterBindingReconciler) {
				r.PhysicalClient = nil
			},
			expectedResult: "",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := setupTestEnvironment(t)
			tt.setupResources(reconciler)

			result, err := reconciler.checkPhysicalResourcesExist(context.Background())

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

// TestClusterBindingReconciler_handleClusterBindingDeletion tests the handleClusterBindingDeletion method
func TestClusterBindingReconciler_handleClusterBindingDeletion(t *testing.T) {
	tests := []struct {
		name           string
		setupResources func(*ClusterBindingReconciler)
		clusterBinding *cloudv1beta1.ClusterBinding
		expectedResult ctrl.Result
		expectError    bool
		errorContains  string
	}{
		{
			name: "virtual nodes exist - should requeue after 5 seconds",
			setupResources: func(r *ClusterBindingReconciler) {
				// Create virtual nodes with cluster binding label
				virtualNode1 := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "virtual-node-1",
						Labels: map[string]string{
							cloudv1beta1.LabelClusterBinding:   "test-cluster-binding",
							cloudv1beta1.LabelPhysicalNodeName: "physical-node-1",
						},
					},
				}
				virtualNode2 := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "virtual-node-2",
						Labels: map[string]string{
							cloudv1beta1.LabelClusterBinding:   "test-cluster-binding",
							cloudv1beta1.LabelPhysicalNodeName: "physical-node-2",
						},
					},
				}
				r.Create(context.Background(), virtualNode1)
				r.Create(context.Background(), virtualNode2)
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-binding",
					Namespace: "default",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			expectedResult: ctrl.Result{RequeueAfter: 5 * time.Second},
			expectError:    false,
			errorContains:  "",
		},
		{
			name: "virtual resources exist - should add annotations and return error",
			setupResources: func(r *ClusterBindingReconciler) {
				// Create virtual configmap with cluster-specific label
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-configmap",
						Namespace: "default",
						Labels: map[string]string{
							"kubeocean.io/synced-by-test-cluster": cloudv1beta1.LabelValueTrue,
						},
					},
				}
				r.Create(context.Background(), cm)
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-binding",
					Namespace: "default",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    true,
			errorContains:  "virtual resources still exist",
		},
		{
			name: "physical resources exist - should return error",
			setupResources: func(r *ClusterBindingReconciler) {
				// Create physical pod with kubeocean managed-by label
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "default",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
						},
					},
				}
				r.PhysicalClient.Create(context.Background(), pod)
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-binding",
					Namespace: "default",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    true,
			errorContains:  "physical resources still exist",
		},
		{
			name: "successful deletion - no resources exist",
			setupResources: func(r *ClusterBindingReconciler) {
				// No resources to create - clean environment
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-cluster-binding",
					Namespace:  "default",
					Finalizers: []string{cloudv1beta1.ClusterBindingSyncerFinalizer},
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := setupTestEnvironment(t)
			tt.setupResources(reconciler)

			// Create the ClusterBinding
			err := reconciler.Create(context.Background(), tt.clusterBinding)
			require.NoError(t, err)

			result, err := reconciler.handleClusterBindingDeletion(context.Background(), tt.clusterBinding)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedResult, result)

			// For successful deletion, verify finalizer was removed
			if !tt.expectError && tt.name == "successful deletion - no resources exist" {
				var updatedBinding cloudv1beta1.ClusterBinding
				err = reconciler.Get(context.Background(), types.NamespacedName{
					Name:      tt.clusterBinding.Name,
					Namespace: tt.clusterBinding.Namespace,
				}, &updatedBinding)
				assert.NoError(t, err)
				assert.NotContains(t, updatedBinding.Finalizers, cloudv1beta1.ClusterBindingSyncerFinalizer)
			}
		})
	}
}

// TestClusterBindingReconciler_Reconcile_DeletionScenarios tests the full Reconcile method with deletion scenarios
func TestClusterBindingReconciler_Reconcile_DeletionScenarios(t *testing.T) {
	tests := []struct {
		name           string
		setupBinding   func(*ClusterBindingReconciler) *cloudv1beta1.ClusterBinding
		setupResources func(*ClusterBindingReconciler)
		expectedResult ctrl.Result
		expectError    bool
		errorContains  string
	}{
		{
			name: "reconcile deletion with finalizer - should add finalizer if missing",
			setupBinding: func(r *ClusterBindingReconciler) *cloudv1beta1.ClusterBinding {
				binding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-cluster-binding",
						Namespace:         "default",
						DeletionTimestamp: &metav1.Time{Time: time.Now()},
						// No finalizer initially
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID: "test-cluster",
					},
				}
				r.Create(context.Background(), binding)

				// Set the existing ClusterBinding to avoid nodeSelector change detection
				r.BottomUpSyncer.SetClusterBinding(binding)
				return binding
			},
			setupResources: func(r *ClusterBindingReconciler) {
				// No additional resources
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "reconcile deletion without finalizer - should do nothing",
			setupBinding: func(r *ClusterBindingReconciler) *cloudv1beta1.ClusterBinding {
				binding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-cluster-binding",
						Namespace:         "default",
						DeletionTimestamp: &metav1.Time{Time: time.Now()},
						// No finalizer, so should do nothing
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID: "test-cluster",
					},
				}
				r.Create(context.Background(), binding)

				// Set the existing ClusterBinding to avoid nodeSelector change detection
				r.BottomUpSyncer.SetClusterBinding(binding)
				return binding
			},
			setupResources: func(r *ClusterBindingReconciler) {
				// No additional resources
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "reconcile normal operation - no deletion timestamp",
			setupBinding: func(r *ClusterBindingReconciler) *cloudv1beta1.ClusterBinding {
				binding := &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster-binding",
						Namespace: "default",
						// No deletion timestamp
					},
					Spec: cloudv1beta1.ClusterBindingSpec{
						ClusterID: "test-cluster",
					},
				}
				r.Create(context.Background(), binding)

				// Set the existing ClusterBinding to avoid nodeSelector change detection
				r.BottomUpSyncer.SetClusterBinding(binding)
				return binding
			},
			setupResources: func(r *ClusterBindingReconciler) {
				// No additional resources
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := setupTestEnvironment(t)
			tt.setupResources(reconciler)
			binding := tt.setupBinding(reconciler)

			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      binding.Name,
					Namespace: binding.Namespace,
				},
			}

			result, err := reconciler.Reconcile(context.Background(), req)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestClusterBindingReconciler_checkAndCleanVirtualResources_AnnotationAddition tests that annotations are properly added
func TestClusterBindingReconciler_checkAndCleanVirtualResources_AnnotationAddition(t *testing.T) {
	tests := []struct {
		name           string
		setupResources func(*ClusterBindingReconciler)
		labelKey       string
		expectedResult string
		expectError    bool
	}{
		{
			name: "add annotations to multiple resource types",
			setupResources: func(r *ClusterBindingReconciler) {
				// Create resources of different types
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cm",
						Namespace: "default",
						Labels: map[string]string{
							"kubeocean.io/synced-by-test-cluster": cloudv1beta1.LabelValueTrue,
						},
					},
				}
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-secret",
						Namespace: "default",
						Labels: map[string]string{
							"kubeocean.io/synced-by-test-cluster": cloudv1beta1.LabelValueTrue,
						},
					},
				}
				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
						Labels: map[string]string{
							"kubeocean.io/synced-by-test-cluster": cloudv1beta1.LabelValueTrue,
						},
					},
				}
				pvc := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "default",
						Labels: map[string]string{
							"kubeocean.io/synced-by-test-cluster": cloudv1beta1.LabelValueTrue,
						},
					},
				}
				r.Create(context.Background(), cm)
				r.Create(context.Background(), secret)
				r.Create(context.Background(), pv)
				r.Create(context.Background(), pvc)
			},
			labelKey:       "kubeocean.io/synced-by-test-cluster",
			expectedResult: "configmaps: [test-cm]; secrets: [test-secret]; persistent volumes: [test-pv]; persistent volume claims: [test-pvc]",
			expectError:    false,
		},
		{
			name: "resources already have annotation - should be idempotent",
			setupResources: func(r *ClusterBindingReconciler) {
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cm",
						Namespace: "default",
						Labels: map[string]string{
							"kubeocean.io/synced-by-test-cluster": cloudv1beta1.LabelValueTrue,
						},
						Annotations: map[string]string{
							cloudv1beta1.GetClusterBindingDeletingAnnotation("test-cluster"): "test-cluster-binding",
						},
					},
				}
				r.Create(context.Background(), cm)
			},
			labelKey:       "kubeocean.io/synced-by-test-cluster",
			expectedResult: "configmaps: [test-cm]",
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := setupTestEnvironment(t)
			tt.setupResources(reconciler)

			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-binding",
					Namespace: "default",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			}
			result, err := reconciler.checkAndCleanVirtualResources(context.Background(), tt.labelKey, clusterBinding)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)

				// Verify annotations were added
				if result != "" {
					// Check ConfigMaps
					var cmList corev1.ConfigMapList
					labelSelector := labels.SelectorFromSet(map[string]string{tt.labelKey: cloudv1beta1.LabelValueTrue})
					err := reconciler.List(context.Background(), &cmList, client.MatchingLabelsSelector{Selector: labelSelector})
					assert.NoError(t, err)
					for _, cm := range cmList.Items {
						annotationKey := cloudv1beta1.GetClusterBindingDeletingAnnotation("test-cluster")
						assert.Equal(t, "test-cluster-binding", cm.Annotations[annotationKey])
					}

					// Check Secrets
					var secretList corev1.SecretList
					err = reconciler.List(context.Background(), &secretList, client.MatchingLabelsSelector{Selector: labelSelector})
					assert.NoError(t, err)
					for _, secret := range secretList.Items {
						annotationKey := cloudv1beta1.GetClusterBindingDeletingAnnotation("test-cluster")
						assert.Equal(t, "test-cluster-binding", secret.Annotations[annotationKey])
					}

					// Check PVs
					var pvList corev1.PersistentVolumeList
					err = reconciler.List(context.Background(), &pvList, client.MatchingLabelsSelector{Selector: labelSelector})
					assert.NoError(t, err)
					for _, pv := range pvList.Items {
						annotationKey := cloudv1beta1.GetClusterBindingDeletingAnnotation("test-cluster")
						assert.Equal(t, "test-cluster-binding", pv.Annotations[annotationKey])
					}

					// Check PVCs
					var pvcList corev1.PersistentVolumeClaimList
					err = reconciler.List(context.Background(), &pvcList, client.MatchingLabelsSelector{Selector: labelSelector})
					assert.NoError(t, err)
					for _, pvc := range pvcList.Items {
						annotationKey := cloudv1beta1.GetClusterBindingDeletingAnnotation("test-cluster")
						assert.Equal(t, "test-cluster-binding", pvc.Annotations[annotationKey])
					}
				}
			}
		})
	}
}
