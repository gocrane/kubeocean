package bottomup

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

func TestPhysicalNodeReconciler_LeaseControllerIntegration(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)
	_ = coordinationv1.AddToScheme(scheme)

	// Create fake clients
	virtualClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
			pod := rawObj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).Build()
	physicalClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
			pod := rawObj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).Build()
	kubeClient := fake.NewSimpleClientset()

	// Create test node
	physicalNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "physical-node-1",
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
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

	// Create ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
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
	}

	// Create physical node reconciler
	reconciler := &PhysicalNodeReconciler{
		PhysicalClient:     physicalClient,
		VirtualClient:      virtualClient,
		KubeClient:         kubeClient,
		Scheme:             scheme,
		ClusterBindingName: "test-binding",
		Log:                testr.New(t),
	}

	// Initialize lease controllers
	reconciler.initLeaseControllers()

	// Create required resources
	ctx := context.Background()
	err := physicalClient.Create(ctx, physicalNode)
	if err != nil {
		t.Fatalf("Failed to create physical node: %v", err)
	}

	err = virtualClient.Create(ctx, clusterBinding)
	if err != nil {
		t.Fatalf("Failed to create cluster binding: %v", err)
	}

	virtualNodeName := reconciler.generateVirtualNodeName(physicalNode.Name)

	t.Run("StartLeaseController", func(t *testing.T) {
		// Create virtual node first
		virtualNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: virtualNodeName,
				UID:  types.UID("virtual-node-uid-123"),
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
		err := virtualClient.Create(ctx, virtualNode)
		if err != nil {
			t.Fatalf("Failed to create virtual node: %v", err)
		}

		// Start lease controller
		reconciler.startLeaseController(virtualNodeName)

		// Verify lease controller was created and started
		status := reconciler.getLeaseControllerStatus()
		if !status[virtualNodeName] {
			t.Errorf("Expected lease controller for %s to be running", virtualNodeName)
		}

		// Wait a bit for lease to be created
		time.Sleep(200 * time.Millisecond)

		// Verify lease was created in Kubernetes
		lease, err := kubeClient.CoordinationV1().Leases(corev1.NamespaceNodeLease).Get(
			ctx, virtualNodeName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Expected lease to be created, got error: %v", err)
		}

		if lease.Name != virtualNodeName {
			t.Errorf("Expected lease name %s, got %s", virtualNodeName, lease.Name)
		}

		if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != virtualNodeName {
			t.Errorf("Expected holder identity %s, got %v", virtualNodeName, lease.Spec.HolderIdentity)
		}
	})

	t.Run("StopLeaseController", func(t *testing.T) {
		// Stop lease controller
		reconciler.stopLeaseController(virtualNodeName)

		// Verify lease controller was stopped and removed
		status := reconciler.getLeaseControllerStatus()
		if status[virtualNodeName] {
			t.Errorf("Expected lease controller for %s to be stopped", virtualNodeName)
		}

		// Verify lease controller was removed from cache
		if _, exists := status[virtualNodeName]; exists {
			t.Errorf("Expected lease controller for %s to be removed from cache", virtualNodeName)
		}
	})

	t.Run("MultipleLeaseControllers", func(t *testing.T) {
		virtualNodeName1 := "vnode-test-1"
		virtualNodeName2 := "vnode-test-2"

		// Create virtual nodes first
		for _, nodeName := range []string{virtualNodeName1, virtualNodeName2} {
			virtualNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					UID:  types.UID("virtual-node-uid-" + nodeName),
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
			err := virtualClient.Create(ctx, virtualNode)
			if err != nil {
				t.Fatalf("Failed to create virtual node %s: %v", nodeName, err)
			}
		}

		// Start multiple lease controllers
		reconciler.startLeaseController(virtualNodeName1)
		reconciler.startLeaseController(virtualNodeName2)

		// Verify both are running
		status := reconciler.getLeaseControllerStatus()
		if !status[virtualNodeName1] || !status[virtualNodeName2] {
			t.Error("Expected both lease controllers to be running")
		}

		// Wait for leases to be created
		time.Sleep(200 * time.Millisecond)

		// Verify both leases were created
		lease1, err := kubeClient.CoordinationV1().Leases(corev1.NamespaceNodeLease).Get(
			ctx, virtualNodeName1, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Expected lease1 to be created, got error: %v", err)
		}

		lease2, err := kubeClient.CoordinationV1().Leases(corev1.NamespaceNodeLease).Get(
			ctx, virtualNodeName2, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Expected lease2 to be created, got error: %v", err)
		}

		if lease1.Name != virtualNodeName1 || lease2.Name != virtualNodeName2 {
			t.Error("Lease names don't match expected virtual node names")
		}

		// Stop all lease controllers
		reconciler.stopAllLeaseControllers()

		// Verify all are stopped
		status = reconciler.getLeaseControllerStatus()
		if len(status) != 0 {
			t.Errorf("Expected all lease controllers to be stopped, got %d", len(status))
		}
	})

	t.Run("DuplicateStartLeaseController", func(t *testing.T) {
		virtualNodeName := "vnode-duplicate-test"

		// Start lease controller twice
		reconciler.startLeaseController(virtualNodeName)
		reconciler.startLeaseController(virtualNodeName) // Should not create duplicate

		// Verify only one is running
		status := reconciler.getLeaseControllerStatus()
		if len(status) != 1 {
			t.Errorf("Expected exactly 1 lease controller, got %d", len(status))
		}

		if !status[virtualNodeName] {
			t.Errorf("Expected lease controller for %s to be running", virtualNodeName)
		}

		// Clean up
		reconciler.stopLeaseController(virtualNodeName)
	})

	t.Run("StopNonExistentLeaseController", func(t *testing.T) {
		// This should not panic or cause errors
		reconciler.stopLeaseController("non-existent-node")

		// Verify no controllers are running
		status := reconciler.getLeaseControllerStatus()
		if len(status) != 0 {
			t.Errorf("Expected no lease controllers, got %d", len(status))
		}
	})
}

func TestPhysicalNodeReconciler_ProcessNodeWithLeaseController(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)
	_ = coordinationv1.AddToScheme(scheme)

	// Create fake clients
	virtualClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
			pod := rawObj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).Build()
	physicalClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
			pod := rawObj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).Build()
	kubeClient := fake.NewSimpleClientset()

	// Create test node with labels to match selector
	physicalNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "physical-node-1",
			Labels: map[string]string{
				"kubernetes.io/os": "linux",
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
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

	// Create ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster-id",
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
	}

	// Create a matching ResourceLeasingPolicy
	matchingPolicy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-policy",
		},
		Spec: cloudv1beta1.ResourceLeasingPolicySpec{
			Cluster: "test-binding",
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
	}

	// Create physical node reconciler
	reconciler := &PhysicalNodeReconciler{
		PhysicalClient:     physicalClient,
		VirtualClient:      virtualClient,
		KubeClient:         kubeClient,
		Scheme:             scheme,
		ClusterBindingName: "test-binding",
		ClusterBinding:     clusterBinding,
		Log:                testr.New(t),
	}

	// Create required resources
	ctx := context.Background()
	err := physicalClient.Create(ctx, physicalNode)
	if err != nil {
		t.Fatalf("Failed to create physical node: %v", err)
	}

	err = physicalClient.Create(ctx, matchingPolicy)
	if err != nil {
		t.Fatalf("Failed to create matching policy: %v", err)
	}

	err = virtualClient.Create(ctx, clusterBinding)
	if err != nil {
		t.Fatalf("Failed to create cluster binding: %v", err)
	}

	virtualNodeName := reconciler.generateVirtualNodeName(physicalNode.Name)

	t.Run("ProcessNode_CreatesLeaseController", func(t *testing.T) {
		// Process the node (should create virtual node and start lease controller)
		_, err := reconciler.processNode(ctx, physicalNode)
		if err != nil {
			t.Fatalf("Failed to process node: %v", err)
		}

		// Verify lease controller was started
		status := reconciler.getLeaseControllerStatus()
		if !status[virtualNodeName] {
			t.Errorf("Expected lease controller for %s to be running after processing node", virtualNodeName)
		}

		// Wait for lease to be created
		time.Sleep(200 * time.Millisecond)

		// Verify lease was created
		lease, err := kubeClient.CoordinationV1().Leases(corev1.NamespaceNodeLease).Get(
			ctx, virtualNodeName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Expected lease to be created after processing node, got error: %v", err)
		}

		if lease.Name != virtualNodeName {
			t.Errorf("Expected lease name %s, got %s", virtualNodeName, lease.Name)
		}

		// Verify virtual node was created
		virtualNode := &corev1.Node{}
		err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
		if err != nil {
			t.Fatalf("Expected virtual node to be created, got error: %v", err)
		}
	})

	t.Run("HandleNodeDeletion_StopsLeaseController", func(t *testing.T) {
		// First, ensure virtual node exists
		virtualNode := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: virtualNodeName,
			},
		}
		err := virtualClient.Create(ctx, virtualNode)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("Failed to create virtual node: %v", err)
		}

		// Handle node deletion (should stop lease controller and delete virtual node)
		// Physical node deleted, force reclaim immediately
		_, err = reconciler.handleNodeDeletion(ctx, physicalNode.Name, true, 0)
		if err != nil {
			t.Fatalf("Failed to handle node deletion: %v", err)
		}

		// Verify lease controller was stopped
		status := reconciler.getLeaseControllerStatus()
		if status[virtualNodeName] {
			t.Errorf("Expected lease controller for %s to be stopped after node deletion", virtualNodeName)
		}

		// Verify virtual node was deleted
		err = virtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
		if err == nil {
			t.Error("Expected virtual node to be deleted")
		}
	})
}
