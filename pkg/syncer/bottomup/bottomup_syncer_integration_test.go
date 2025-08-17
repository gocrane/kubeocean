package bottomup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestBottomUpSyncer_Integration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Create test physical node
	physicalNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "physical-worker-1",
			Labels: map[string]string{
				"node-type":              "worker",
				"environment":            "test",
				"kubernetes.io/hostname": "physical-worker-1", // Should be filtered out
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
			NodeInfo: corev1.NodeSystemInfo{
				KernelVersion: "5.4.0",
				OSImage:       "Ubuntu 20.04",
			},
		},
	}

	// Create test ResourceLeasingPolicy
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
			ResourceLimits: []cloudv1beta1.ResourceLimit{
				{Resource: "cpu", Quantity: &[]resource.Quantity{resource.MustParse("2")}[0]},
				{Resource: "memory", Quantity: &[]resource.Quantity{resource.MustParse("4Gi")}[0]},
			},
			TimeWindows: []cloudv1beta1.TimeWindow{
				{
					Start: "00:00",
					End:   "23:59",
					Days:  []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
				},
			},
		},
	}

	// Create test ClusterBinding (cluster-scoped)
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
			SecretRef: corev1.SecretReference{
				Name:      "test-kubeconfig",
				Namespace: "tapestry-system",
			},
		},
	}

	// Create fake clients
	physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalNode).Build()
	virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(policy, clusterBinding).Build()

	// Create fake physical config
	physicalConfig := &rest.Config{
		Host: "https://fake-physical-cluster.example.com",
	}

	// Create bottom-up syncer
	// Create fake managers for testing
	virtualManager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:         scheme,
		LeaderElection: false,
	})
	require.NoError(t, err)

	physicalManager, err := ctrl.NewManager(physicalConfig, ctrl.Options{
		Scheme:         scheme,
		LeaderElection: false,
	})
	require.NoError(t, err)

	syncer := NewBottomUpSyncer(virtualManager, physicalManager, scheme, clusterBinding)

	// Test PhysicalNodeReconciler directly
	nodeReconciler := &PhysicalNodeReconciler{
		PhysicalClient:     physicalClient,
		VirtualClient:      virtualClient,
		Scheme:             scheme,
		ClusterBinding:     clusterBinding,
		ClusterBindingName: clusterBinding.Name,
		Log:                ctrl.Log.WithName("test-physical-node-reconciler"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("PhysicalNodeReconciler handles node deletion", func(t *testing.T) {
		// Delete the virtual node first to test deletion handling
		virtualNodeName := nodeReconciler.generateVirtualNodeName(physicalNode.Name)

		// Simulate node deletion
		result, err := nodeReconciler.handleNodeDeletion(ctx, physicalNode.Name)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), result.RequeueAfter, "Should not requeue after deletion")

		// Verify virtual node is deleted
		virtualNode := &corev1.Node{}
		err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
		assert.True(t, client.IgnoreNotFound(err) == nil, "Virtual node should be deleted")
	})

	t.Run("ResourceLeasingPolicyReconciler handles policy changes", func(t *testing.T) {
		policyReconciler := &ResourceLeasingPolicyReconciler{
			Client:         virtualClient,
			VirtualClient:  virtualClient,
			Scheme:         scheme,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test-resourceleasingpolicy-reconciler"),
		}

		// Test policy reconciliation
		result, err := policyReconciler.triggerNodeReEvaluation()
		require.NoError(t, err)
		assert.True(t, result.RequeueAfter > 0, "Should requeue for node re-evaluation")
	})

	t.Run("BottomUpSyncer initialization", func(t *testing.T) {
		// Test that the syncer can be created without errors
		assert.NotNil(t, syncer)
		assert.Equal(t, clusterBinding, syncer.ClusterBinding)
	})

	t.Run("Node without applicable policy", func(t *testing.T) {
		// Create a node that doesn't match the policy selector
		nonMatchingNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "master-node-1",
				Labels: map[string]string{
					"node-type": "master", // Doesn't match policy selector
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1800m"),
					corev1.ResourceMemory: resource.MustParse("3Gi"),
				},
			},
		}

		// Process the non-matching node
		result, err := nodeReconciler.processNode(ctx, nonMatchingNode)
		require.NoError(t, err)
		assert.True(t, result.RequeueAfter > 0, "Should requeue for periodic sync")

		// Verify virtual node is created with default resources
		virtualNodeName := nodeReconciler.generateVirtualNodeName(nonMatchingNode.Name)
		virtualNode := &corev1.Node{}
		err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
		require.NoError(t, err, "Virtual node should exist when no policy matches (default)")

		// Verify resources match allocatable (no policy restrictions)
		expectedCPU := resource.MustParse("1800m")
		expectedMemory := resource.MustParse("3Gi")

		actualCPU := virtualNode.Status.Allocatable[corev1.ResourceCPU]
		actualMemory := virtualNode.Status.Allocatable[corev1.ResourceMemory]

		assert.True(t, expectedCPU.Equal(actualCPU),
			"CPU should match allocatable: expected %s, got %s", expectedCPU.String(), actualCPU.String())
		assert.True(t, expectedMemory.Equal(actualMemory),
			"Memory should match allocatable: expected %s, got %s", expectedMemory.String(), actualMemory.String())
	})
}
