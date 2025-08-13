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
			NodeSelector: map[string]string{
				"node-type": "worker",
			},
			ResourceLimits: []cloudv1beta1.ResourceLimit{
				{Resource: "cpu", Quantity: resource.MustParse("2")},
				{Resource: "memory", Quantity: resource.MustParse("4Gi")},
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

	t.Run("PhysicalNodeReconciler processes node correctly", func(t *testing.T) {
		// Process the physical node
		result, err := nodeReconciler.processNode(ctx, physicalNode)
		require.NoError(t, err)
		assert.True(t, result.RequeueAfter > 0, "Should requeue for periodic sync")

		// Check if virtual node was created
		virtualNodeName := nodeReconciler.generateVirtualNodeName(physicalNode.Name)
		virtualNode := &corev1.Node{}
		err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
		require.NoError(t, err, "Virtual node should be created")

		// Verify virtual node properties
		assert.Equal(t, virtualNodeName, virtualNode.Name)
		assert.Equal(t, "test-cluster", virtualNode.Labels[LabelPhysicalClusterID])
		assert.Equal(t, "physical-worker-1", virtualNode.Labels[LabelPhysicalNodeName])
		assert.Equal(t, "tapestry", virtualNode.Labels[LabelManagedBy])
		assert.Equal(t, "worker", virtualNode.Labels["node-type"])
		assert.Equal(t, "test", virtualNode.Labels["environment"])

		// Verify system labels are filtered out
		assert.NotContains(t, virtualNode.Labels, "kubernetes.io/hostname")

		// Verify resource limits are applied
		expectedCPU := resource.MustParse("2")
		expectedMemory := resource.MustParse("4Gi")
		assert.True(t, virtualNode.Status.Capacity[corev1.ResourceCPU].Equal(expectedCPU))
		assert.True(t, virtualNode.Status.Capacity[corev1.ResourceMemory].Equal(expectedMemory))
		assert.True(t, virtualNode.Status.Allocatable[corev1.ResourceCPU].Equal(expectedCPU))
		assert.True(t, virtualNode.Status.Allocatable[corev1.ResourceMemory].Equal(expectedMemory))

		// Verify annotations
		assert.Contains(t, virtualNode.Annotations, AnnotationLastSyncTime)
		assert.Contains(t, virtualNode.Annotations, AnnotationResourcePolicy)
		assert.Equal(t, "test-policy", virtualNode.Annotations[AnnotationResourcePolicy])

		// Verify node conditions
		assert.Len(t, virtualNode.Status.Conditions, 1)
		assert.Equal(t, corev1.NodeReady, virtualNode.Status.Conditions[0].Type)
		assert.Equal(t, corev1.ConditionTrue, virtualNode.Status.Conditions[0].Status)
	})

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
	})

	t.Run("Node with conservative resource calculation", func(t *testing.T) {
		// Create a policy without resource limits
		policyWithoutLimits := &cloudv1beta1.ResourceLeasingPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: "policy-without-limits",
			},
			Spec: cloudv1beta1.ResourceLeasingPolicySpec{
				Cluster: "test-cluster",
				NodeSelector: map[string]string{
					"node-type": "worker",
				},
				// No ResourceLimits specified
			},
		}

		// Add the policy to virtual client
		err := virtualClient.Create(ctx, policyWithoutLimits)
		require.NoError(t, err)

		// Calculate resources without limits
		resources := nodeReconciler.calculateAvailableResources(physicalNode, []cloudv1beta1.ResourceLeasingPolicy{*policyWithoutLimits})

		// Verify conservative calculation (80% of capacity)
		expectedCPU := resource.MustParse("3")             // 80% of 4 = 3.2, truncated to 3
		expectedMemory := resource.MustParse("6871947673") // 80% of 8Gi
		assert.True(t, resources[corev1.ResourceCPU].Equal(expectedCPU))
		assert.True(t, resources[corev1.ResourceMemory].Equal(expectedMemory))

		// Clean up
		err = virtualClient.Delete(ctx, policyWithoutLimits)
		require.NoError(t, err)
	})
}
