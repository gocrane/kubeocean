package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestResourceLeasingPolicyReconciler_validateResourceLeasingPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	tests := []struct {
		name    string
		policy  *cloudv1beta1.ResourceLeasingPolicy
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid policy",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: resource.MustParse("4"),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing cluster reference",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "",
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: resource.MustParse("4"),
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "cluster reference is required",
		},
		{
			name: "empty resource limits",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster:        "test-cluster",
					ResourceLimits: []cloudv1beta1.ResourceLimit{},
				},
			},
			wantErr: true,
			errMsg:  "at least one resource limit must be specified",
		},
		{
			name: "resource limit with empty resource name",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "",
							Quantity: resource.MustParse("4"),
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "resource name is required in resource limits",
		},
		{
			name: "resource limit with zero quantity",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: resource.MustParse("0"),
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "resource quantity is required in resource limits",
		},
		{
			name: "invalid time window",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: resource.MustParse("4"),
						},
					},
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "",
							End:   "18:00",
							Days:  []string{"Monday"},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "time window start and end times are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &ResourceLeasingPolicyReconciler{
				Log: zap.New(zap.UseDevMode(true)),
			}

			err := reconciler.validateResourceLeasingPolicy(tt.policy)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResourceLeasingPolicyReconciler_validateClusterBindingReference(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	tests := []struct {
		name           string
		policy         *cloudv1beta1.ResourceLeasingPolicy
		clusterBinding *cloudv1beta1.ClusterBinding
		wantErr        bool
		errMsg         string
	}{
		{
			name: "valid cluster binding reference",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "test-namespace",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "test-cluster",
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
			},
			wantErr: false,
		},
		{
			name: "cluster binding not found",
			policy: &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "test-namespace",
				},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: "nonexistent-cluster",
				},
			},
			clusterBinding: nil,
			wantErr:        true,
			errMsg:         "referenced ClusterBinding nonexistent-cluster not found in namespace test-namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []client.Object
			if tt.clusterBinding != nil {
				objects = append(objects, tt.clusterBinding)
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			reconciler := &ResourceLeasingPolicyReconciler{
				Client: fakeClient,
				Log:    zap.New(zap.UseDevMode(true)),
			}

			err := reconciler.validateClusterBindingReference(context.Background(), tt.policy)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResourceLeasingPolicyReconciler_Reconcile_Integration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	// Create a ResourceLeasingPolicy
	policy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-policy",
			Namespace: "default",
		},
		Spec: cloudv1beta1.ResourceLeasingPolicySpec{
			Cluster: "test-cluster",
			ResourceLimits: []cloudv1beta1.ResourceLimit{
				{
					Resource: "cpu",
					Quantity: resource.MustParse("4"),
				},
			},
		},
	}

	// Create the referenced ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			SecretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "default",
			},
			MountNamespace: "test-mount",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, clusterBinding).
		Build()

	reconciler := &ResourceLeasingPolicyReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Log:      zap.New(zap.UseDevMode(true)),
		Recorder: record.NewFakeRecorder(100),
	}

	// Test reconcile
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      policy.Name,
			Namespace: policy.Namespace,
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Should not return an error and should not requeue
	assert.NoError(t, err)
	assert.False(t, result.Requeue) //nolint
	assert.Equal(t, int64(0), result.RequeueAfter.Nanoseconds())
}

func TestResourceLeasingPolicyReconciler_Reconcile_ValidationFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	// Create an invalid ResourceLeasingPolicy (missing cluster reference)
	policy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-policy",
			Namespace: "default",
		},
		Spec: cloudv1beta1.ResourceLeasingPolicySpec{
			Cluster: "", // Invalid: empty cluster reference
			ResourceLimits: []cloudv1beta1.ResourceLimit{
				{
					Resource: "cpu",
					Quantity: resource.MustParse("4"),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy).
		Build()

	reconciler := &ResourceLeasingPolicyReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Log:      zap.New(zap.UseDevMode(true)),
		Recorder: record.NewFakeRecorder(100),
	}

	// Test reconcile
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      policy.Name,
			Namespace: policy.Namespace,
		},
	}

	_, err := reconciler.Reconcile(context.Background(), req)

	// Should return an error due to validation failure
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cluster reference is required")
}
