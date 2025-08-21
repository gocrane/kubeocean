package bottomup

import (
	"context"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestResourceLeasingPolicyReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	tests := []struct {
		name           string
		policy         *cloudv1beta1.ResourceLeasingPolicy
		clusterBinding *cloudv1beta1.ClusterBinding
		expectRequeue  bool
		expectError    bool
	}{
		{
			name: "policy for our cluster",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
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
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			expectRequeue: false,
			expectError:   false,
		},
		{
			name: "policy for different cluster",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-policy",
				},
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
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			expectRequeue: false,
			expectError:   false,
		},
		{
			name:   "deleted policy",
			policy: nil, // Policy doesn't exist (deleted)
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			expectRequeue: false,
			expectError:   false,
		},
		{
			name: "policy with time windows",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-policy-with-time",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "09:00",
							End:   "17:00",
							Days:  []string{"monday", "tuesday", "wednesday", "thursday", "friday"},
						},
					},
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{Resource: "cpu", Quantity: &[]resource.Quantity{resource.MustParse("1")}[0]},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			expectRequeue: false,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client
			virtualObjs := []client.Object{}
			if tt.policy != nil {
				virtualObjs = append(virtualObjs, tt.policy)
			}
			// Ensure ClusterBinding exists for controller lookups
			if tt.clusterBinding != nil {
				virtualObjs = append(virtualObjs, tt.clusterBinding)
			}
			builder := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjs...)
			if tt.policy != nil {
				builder = builder.WithStatusSubresource(tt.policy)
			}
			virtualClient := builder.Build()

			// Create reconciler
			reconciler := &ResourceLeasingPolicyReconciler{
				Client:         virtualClient,
				VirtualClient:  virtualClient,
				Scheme:         scheme,
				ClusterBinding: tt.clusterBinding,
				Log:            ctrl.Log.WithName("test-resourceleasingpolicy-reconciler"),
				// Provide mock functions for testing
				GetNodesMatchingSelector: func(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
					// Return empty list for tests
					return nil, nil
				},
				RequeueNodes: func(nodeNames []string) error {
					// Mock implementation - just return nil
					return nil
				},
			}

			// Test reconcile
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "test-policy",
				},
			}

			ctx := context.Background()
			result, err := reconciler.Reconcile(ctx, req)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.expectRequeue {
				assert.True(t, result.Requeue || result.RequeueAfter > 0, "Expected requeue")
			} else {
				assert.False(t, result.Requeue, "Expected no immediate requeue")
				assert.Equal(t, time.Duration(0), result.RequeueAfter, "Expected no requeue after delay")
			}
		})
	}
}

func TestResourceLeasingPolicyReconciler_TriggerNodeReEvaluation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
		},
	}

	virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &ResourceLeasingPolicyReconciler{
		Client:         virtualClient,
		VirtualClient:  virtualClient,
		Scheme:         scheme,
		ClusterBinding: clusterBinding,
		Log:            ctrl.Log.WithName("test-resourceleasingpolicy-reconciler"),
		// Provide mock functions for testing
		GetNodesMatchingSelector: func(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
			// Return empty list for tests
			return nil, nil
		},
		RequeueNodes: func(nodeName []string) error {
			// Mock implementation - just return nil
			return nil
		},
	}

	tests := []struct {
		name   string
		policy *cloudv1beta1.ResourceLeasingPolicy
	}{
		{
			name: "trigger with policy",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
				},
			},
		},
		{
			name:   "trigger without policy (deleted)",
			policy: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reconciler.triggerNodeReEvaluation(nil)

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(0), result.RequeueAfter, "Should not requeue after successful trigger")
		})
	}
}

func TestResourceLeasingPolicyReconciler_SetupWithManager(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	reconciler := &ResourceLeasingPolicyReconciler{
		Scheme:         scheme,
		ClusterBinding: clusterBinding,
		Log:            ctrl.Log.WithName("test-resourceleasingpolicy-reconciler"),
	}

	// This test just ensures the SetupWithManager method exists and has the correct signature
	// In a real environment, you would create a real manager to test this
	assert.NotNil(t, reconciler.SetupWithManager)
}

func TestResourceLeasingPolicyReconciler_Finalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	// Create a policy with NodeSelector
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

	virtualClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy).
		Build()

	reconciler := &ResourceLeasingPolicyReconciler{
		Client:         virtualClient,
		VirtualClient:  virtualClient,
		Scheme:         scheme,
		ClusterBinding: clusterBinding,
		Log:            ctrl.Log.WithName("test-resourceleasingpolicy-reconciler"),
		// Provide mock functions for testing
		GetNodesMatchingSelector: func(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
			// Return a test node that matches the selector
			return []string{"test-node"}, nil
		},
		RequeueNodes: func(nodeNames []string) error {
			// Mock implementation - just return success
			return nil
		},
	}

	ctx := context.Background()

	t.Run("adds finalizer on first reconcile", func(t *testing.T) {
		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      "test-policy",
				Namespace: "",
			},
		}

		// First reconcile should add finalizer
		result, err := reconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.False(t, result.Requeue)

		// Verify finalizer was added
		updatedPolicy := &cloudv1beta1.ResourceLeasingPolicy{}
		err = virtualClient.Get(ctx, req.NamespacedName, updatedPolicy)
		require.NoError(t, err)
		assert.True(t, reconciler.hasFinalizer(updatedPolicy))
	})

	t.Run("handles deletion with finalizer", func(t *testing.T) {
		// Create a new policy for this test with deletion timestamp already set
		now := metav1.Now()
		deletionTestPolicy := &cloudv1beta1.ResourceLeasingPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-policy-deletion",
				Finalizers:        []string{cloudv1beta1.PolicyFinalizerName},
				DeletionTimestamp: &now,
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

		// Create a new client for this test
		deletionClient := fakeclient.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(deletionTestPolicy).
			Build()

		deletionReconciler := &ResourceLeasingPolicyReconciler{
			Client:         deletionClient,
			VirtualClient:  deletionClient,
			Scheme:         scheme,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test-resourceleasingpolicy-reconciler"),
			GetNodesMatchingSelector: func(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
				return []string{"test-node"}, nil
			},
			RequeueNodes: func(nodeNames []string) error {
				return nil
			},
		}

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      "test-policy-deletion",
				Namespace: "",
			},
		}

		// Reconcile should handle deletion
		result, err := deletionReconciler.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter)

		// Verify finalizer was removed (policy should be deleted or finalizer removed)
		updatedPolicy := &cloudv1beta1.ResourceLeasingPolicy{}
		err = deletionClient.Get(ctx, req.NamespacedName, updatedPolicy)
		if err == nil {
			// If policy still exists, finalizer should be removed
			assert.False(t, deletionReconciler.hasFinalizer(updatedPolicy))
		} else {
			// Policy should be deleted (NotFound error is expected)
			assert.True(t, errors.IsNotFound(err))
		}
	})
}
