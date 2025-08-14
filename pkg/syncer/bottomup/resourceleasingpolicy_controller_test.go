package bottomup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
					NodeSelector: map[string]string{
						"node-type": "worker",
					},
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{Resource: "cpu", Quantity: resource.MustParse("2")},
						{Resource: "memory", Quantity: resource.MustParse("4Gi")},
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
			expectRequeue: true,
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
					NodeSelector: map[string]string{
						"node-type": "worker",
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
			expectRequeue: true,
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
						{Resource: "cpu", Quantity: resource.MustParse("1")},
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
			expectRequeue: true,
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
				assert.True(t, result.RequeueAfter > 0, "Expected requeue after some time")
			} else {
				assert.Equal(t, time.Duration(0), result.RequeueAfter, "Expected no requeue")
			}
		})
	}
}

func TestResourceLeasingPolicyReconciler_TriggerNodeReEvaluation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

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
			result, err := reconciler.triggerNodeReEvaluation()

			assert.NoError(t, err)
			assert.True(t, result.RequeueAfter > 0, "Should requeue after some time")
			assert.Equal(t, DefaultPolicySyncInterval, result.RequeueAfter, "Should use default policy sync interval")
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
