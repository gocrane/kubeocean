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

func TestResourceLeasingPolicyReconciler_ValidateTimeWindows(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	reconciler := &ResourceLeasingPolicyReconciler{
		ClusterBinding: clusterBinding,
		Log:            ctrl.Log.WithName("test-resourceleasingpolicy-reconciler"),
	}

	tests := []struct {
		name          string
		policy        *cloudv1beta1.ResourceLeasingPolicy
		expectError   bool
		expectedError string
	}{
		{
			name: "valid time windows",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "09:00",
							End:   "17:00",
							Days:  []string{"monday", "tuesday", "wednesday"},
						},
						{
							Start: "22:00",
							End:   "02:00",
							Days:  []string{"friday", "saturday"},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "invalid start time format",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-start-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "9:00", // Invalid: should be "09:00"
							End:   "17:00",
						},
					},
				},
			},
			expectError:   true,
			expectedError: "invalid start time format in time window 0",
		},
		{
			name: "invalid end time format",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-end-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "09:00",
							End:   "25:00", // Invalid: hour > 23
						},
					},
				},
			},
			expectError:   true,
			expectedError: "invalid end time format in time window 0",
		},
		{
			name: "invalid day",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-day-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "09:00",
							End:   "17:00",
							Days:  []string{"monday", "invalid-day"},
						},
					},
				},
			},
			expectError:   true,
			expectedError: "invalid day in time window 0",
		},
		{
			name: "empty time windows - should be valid",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "empty-windows-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					TimeWindows: []cloudv1beta1.TimeWindow{},
				},
			},
			expectError: false,
		},
		{
			name: "multiple validation errors - should return first",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "multiple-errors-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "invalid", // First error
							End:   "also-invalid",
							Days:  []string{"bad-day"},
						},
					},
				},
			},
			expectError:   true,
			expectedError: "invalid start time format in time window 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.validateTimeWindows(tt.policy)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResourceLeasingPolicyReconciler_UpdatePolicyStatusWithValidation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	tests := []struct {
		name                 string
		policy               *cloudv1beta1.ResourceLeasingPolicy
		expectedPhase        cloudv1beta1.ResourceLeasingPolicyPhase
		expectedValidStatus  metav1.ConditionStatus
		expectedActiveStatus metav1.ConditionStatus
		expectedValidReason  string
		expectedActiveReason string
	}{
		{
			name: "policy with valid time windows",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "09:00",
							End:   "17:00",
						},
					},
				},
				Status: cloudv1beta1.ResourceLeasingPolicyStatus{},
			},
			expectedPhase:        cloudv1beta1.ResourceLeasingPolicyPhaseInactive, // Assuming current time is outside 09:00-17:00
			expectedValidStatus:  metav1.ConditionTrue,
			expectedActiveStatus: metav1.ConditionFalse,
			expectedValidReason:  "ValidationPassed",
			expectedActiveReason: "InactiveTimeWindow",
		},
		{
			name: "policy with invalid time windows",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-policy",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "invalid-time",
							End:   "17:00",
						},
					},
				},
				Status: cloudv1beta1.ResourceLeasingPolicyStatus{},
			},
			expectedPhase:        cloudv1beta1.ResourceLeasingPolicyPhaseFailed,
			expectedValidStatus:  metav1.ConditionFalse,
			expectedActiveStatus: metav1.ConditionFalse,
			expectedValidReason:  "ValidationError",
			expectedActiveReason: "ValidationError",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with the policy
			fakeClient := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.policy).
				WithStatusSubresource(tt.policy).
				Build()

			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
			}

			reconciler := &ResourceLeasingPolicyReconciler{
				Client:         fakeClient,
				ClusterBinding: clusterBinding,
				Log:            ctrl.Log.WithName("test-resourceleasingpolicy-reconciler"),
			}

			ctx := context.Background()
			statusChanged, err := reconciler.updatePolicyStatus(ctx, tt.policy)

			require.NoError(t, err)
			assert.True(t, statusChanged) // Should be changed since we're starting from empty status

			// Check phase
			assert.Equal(t, tt.expectedPhase, tt.policy.Status.Phase)

			// Check conditions
			require.Len(t, tt.policy.Status.Conditions, 2) // Valid and Active conditions

			var validCondition, activeCondition *metav1.Condition
			for i := range tt.policy.Status.Conditions {
				switch tt.policy.Status.Conditions[i].Type {
				case "Valid":
					validCondition = &tt.policy.Status.Conditions[i]
				case "Active":
					activeCondition = &tt.policy.Status.Conditions[i]
				}
			}

			require.NotNil(t, validCondition)
			require.NotNil(t, activeCondition)

			assert.Equal(t, tt.expectedValidStatus, validCondition.Status)
			assert.Equal(t, tt.expectedValidReason, validCondition.Reason)

			assert.Equal(t, tt.expectedActiveStatus, activeCondition.Status)
			assert.Equal(t, tt.expectedActiveReason, activeCondition.Reason)

			if tt.expectedValidStatus == metav1.ConditionFalse {
				assert.Contains(t, validCondition.Message, "Policy validation failed")
			} else {
				assert.Contains(t, validCondition.Message, "Policy validation passed successfully")
			}
		})
	}
}
