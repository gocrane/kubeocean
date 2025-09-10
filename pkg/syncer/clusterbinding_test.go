package syncer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/syncer/bottomup"
)

// setupTestEnvironment creates a test environment for ClusterBindingReconciler tests
func setupTestEnvironment(t *testing.T) *ClusterBindingReconciler {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &ClusterBindingReconciler{
		Client:                  client,
		Log:                     zap.New(zap.UseDevMode(true)),
		ClusterBindingName:      "test-cluster-binding",
		ClusterBindingNamespace: "default",
		BottomUpSyncer: &bottomup.BottomUpSyncer{
			ClusterBinding: nil, // Start with nil to test first time loading
		},
	}

	return reconciler
}

// setupTestEnvironmentWithExistingBinding creates a test environment with an existing ClusterBinding
func setupTestEnvironmentWithExistingBinding(t *testing.T) *ClusterBindingReconciler {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

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

	reconciler := &ClusterBindingReconciler{
		Client:                  client,
		Log:                     zap.New(zap.UseDevMode(true)),
		ClusterBindingName:      "test-cluster-binding",
		ClusterBindingNamespace: "default",
		BottomUpSyncer: &bottomup.BottomUpSyncer{
			ClusterBinding: existingBinding,
		},
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
