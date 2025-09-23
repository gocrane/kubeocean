package bottomup

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	// testNodeName is the test node name used in tests
	testNodeName = "test-node"
)

// createTestLeaseController creates a LeaseController for testing
func createTestLeaseController(nodeName string) (*LeaseController, client.Client) {
	kubeClient := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	logger := testr.New(&testing.T{})
	return NewLeaseController(nodeName, kubeClient, virtualClient, logger), virtualClient
}

// createTestNode creates a test node
func createTestNode(_ string, ready bool) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNodeName,
			UID:  "test-uid-123",
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

	if !ready {
		node.Status.Conditions[0].Status = corev1.ConditionFalse
	}

	return node
}

func TestNewLeaseController(t *testing.T) {
	nodeName := testNodeName
	lc, _ := createTestLeaseController(nodeName)

	if lc.nodeName != nodeName {
		t.Errorf("Expected node name %s, got %s", nodeName, lc.nodeName)
	}

	if lc.leaseDurationSeconds != DefaultLeaseDuration {
		t.Errorf("Expected lease duration %d, got %d", DefaultLeaseDuration, lc.leaseDurationSeconds)
	}

	expectedRenewInterval := time.Duration(float64(DefaultLeaseDuration)*DefaultRenewIntervalFraction) * time.Second
	if lc.renewInterval != expectedRenewInterval {
		t.Errorf("Expected renew interval %v, got %v", expectedRenewInterval, lc.renewInterval)
	}

	if !lc.IsRunning() {
		t.Error("Expected lease controller to be running after creation")
	}
}

func TestLeaseController_EnsureLease(t *testing.T) {
	nodeName := testNodeName
	lc, virtualClient := createTestLeaseController(nodeName)

	// Create and store the test node
	testNode := createTestNode(nodeName, true)
	err := virtualClient.Create(context.Background(), testNode)
	if err != nil {
		t.Fatalf("Failed to create test node: %v", err)
	}

	// Test creating a new lease
	lease, created, err := lc.ensureLease(testNode)
	if err != nil {
		t.Fatalf("Failed to ensure lease: %v", err)
	}

	if !created {
		t.Error("Expected lease to be created")
	}

	if lease.Name != nodeName {
		t.Errorf("Expected lease name %s, got %s", nodeName, lease.Name)
	}

	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != nodeName {
		t.Errorf("Expected holder identity %s, got %v", nodeName, lease.Spec.HolderIdentity)
	}

	// Verify OwnerReferences
	if len(lease.OwnerReferences) == 0 {
		t.Error("Expected lease to have OwnerReferences")
	} else {
		ownerRef := lease.OwnerReferences[0]
		if ownerRef.Name != testNode.Name {
			t.Errorf("Expected owner reference name %s, got %s", testNode.Name, ownerRef.Name)
		}
		if ownerRef.UID != testNode.UID {
			t.Errorf("Expected owner reference UID %s, got %s", testNode.UID, ownerRef.UID)
		}
	}

	// Test getting existing lease
	lease2, created2, err2 := lc.ensureLease(testNode)
	if err2 != nil {
		t.Fatalf("Failed to ensure existing lease: %v", err2)
	}

	if created2 {
		t.Error("Expected lease to not be created (should already exist)")
	}

	if lease2.Name != nodeName {
		t.Errorf("Expected lease name %s, got %s", nodeName, lease2.Name)
	}
}

func TestLeaseController_NewLease(t *testing.T) {
	nodeName := testNodeName
	fakeClock := clocktesting.NewFakeClock(time.Now())

	lc, _ := createTestLeaseController(nodeName)
	lc.clock = fakeClock

	testNode := createTestNode(nodeName, true)

	// Test creating new lease from scratch
	lease := lc.newLease(testNode)

	if lease.Name != nodeName {
		t.Errorf("Expected lease name %s, got %s", nodeName, lease.Name)
	}

	if lease.Namespace != corev1.NamespaceNodeLease {
		t.Errorf("Expected lease namespace %s, got %s", corev1.NamespaceNodeLease, lease.Namespace)
	}

	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != nodeName {
		t.Errorf("Expected holder identity %s, got %v", nodeName, lease.Spec.HolderIdentity)
	}

	if lease.Spec.LeaseDurationSeconds == nil || *lease.Spec.LeaseDurationSeconds != DefaultLeaseDuration {
		t.Errorf("Expected lease duration %d, got %v", DefaultLeaseDuration, lease.Spec.LeaseDurationSeconds)
	}

	if lease.Spec.RenewTime == nil {
		t.Error("Expected renew time to be set")
	}

	// Verify OwnerReferences are set correctly
	if len(lease.OwnerReferences) == 0 {
		t.Error("Expected lease to have OwnerReferences")
	} else {
		ownerRef := lease.OwnerReferences[0]
		if ownerRef.Name != testNode.Name {
			t.Errorf("Expected owner reference name %s, got %s", testNode.Name, ownerRef.Name)
		}
		if ownerRef.UID != testNode.UID {
			t.Errorf("Expected owner reference UID %s, got %s", testNode.UID, ownerRef.UID)
		}
		if ownerRef.Kind != "Node" {
			t.Errorf("Expected owner reference kind Node, got %s", ownerRef.Kind)
		}
	}

	// Test with nil node (should not set owner references)
	leaseWithoutOwner := lc.newLease(nil)
	if len(leaseWithoutOwner.OwnerReferences) != 0 {
		t.Error("Expected lease without node to have no OwnerReferences")
	}
}

func TestLeaseController_StartStop(t *testing.T) {
	nodeName := "test-node"
	lc, virtualClient := createTestLeaseController(nodeName)

	// Create test node
	testNode := createTestNode(nodeName, true)
	err := virtualClient.Create(context.Background(), testNode)
	if err != nil {
		t.Fatalf("Failed to create test node: %v", err)
	}

	if !lc.IsRunning() {
		t.Error("Expected lease controller to be running initially")
	}

	// Stop the controller
	lc.Stop()

	// Give it a moment to stop
	time.Sleep(100 * time.Millisecond)

	if lc.IsRunning() {
		t.Error("Expected lease controller to be stopped")
	}
}

func TestLeaseController_NodeStatusCheck(t *testing.T) {
	nodeName := testNodeName
	lc, virtualClient := createTestLeaseController(nodeName)

	// Test with ready node
	readyNode := createTestNode(nodeName, true)
	err := virtualClient.Create(context.Background(), readyNode)
	if err != nil {
		t.Fatalf("Failed to create ready node: %v", err)
	}

	if !lc.isNodeReady(readyNode) {
		t.Error("Expected ready node to be detected as ready")
	}

	// Test with not ready node
	notReadyNode := createTestNode(nodeName, false)
	if lc.isNodeReady(notReadyNode) {
		t.Error("Expected not ready node to be detected as not ready")
	}

	// Test node with no conditions
	nodeWithoutConditions := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			UID:  "test-uid-456",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{},
		},
	}

	if lc.isNodeReady(nodeWithoutConditions) {
		t.Error("Expected node without conditions to be detected as not ready")
	}
}

func TestLeaseController_GetVirtualNode(t *testing.T) {
	nodeName := testNodeName
	lc, virtualClient := createTestLeaseController(nodeName)

	// Test getting non-existent node
	_, err := lc.getVirtualNode(context.Background())
	if err == nil {
		t.Error("Expected error when getting non-existent node")
	}

	// Create and test getting existing node
	testNode := createTestNode(nodeName, true)
	err = virtualClient.Create(context.Background(), testNode)
	if err != nil {
		t.Fatalf("Failed to create test node: %v", err)
	}

	retrievedNode, err := lc.getVirtualNode(context.Background())
	if err != nil {
		t.Fatalf("Failed to get virtual node: %v", err)
	}

	if retrievedNode.Name != nodeName {
		t.Errorf("Expected retrieved node name %s, got %s", nodeName, retrievedNode.Name)
	}

	if retrievedNode.UID != testNode.UID {
		t.Errorf("Expected retrieved node UID %s, got %s", testNode.UID, retrievedNode.UID)
	}
}

func TestLeaseController_GetVirtualNode_WithRetry(t *testing.T) {
	nodeName := testNodeName
	lc, virtualClient := createTestLeaseController(nodeName)

	// Test retry mechanism with eventual success
	testNode := createTestNode(nodeName, true)

	// Test 1: Node doesn't exist initially, should timeout after 1 second
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err := lc.getVirtualNode(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Expected error when getting non-existent node with retry")
	}

	// Should have retried for approximately 1 second
	if elapsed < 800*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Errorf("Expected retry duration around 1s, got %v", elapsed)
	}

	// Test 2: Node exists, should succeed immediately
	err = virtualClient.Create(context.Background(), testNode)
	if err != nil {
		t.Fatalf("Failed to create test node: %v", err)
	}

	start = time.Now()
	retrievedNode, err := lc.getVirtualNode(context.Background())
	elapsed = time.Since(start)

	if err != nil {
		t.Fatalf("Failed to get virtual node: %v", err)
	}

	// Should succeed quickly without retries
	if elapsed > 100*time.Millisecond {
		t.Errorf("Expected quick success without retries, got %v", elapsed)
	}

	if retrievedNode.Name != nodeName {
		t.Errorf("Expected retrieved node name %s, got %s", nodeName, retrievedNode.Name)
	}
}

func TestLeaseController_RetryUpdateLease(t *testing.T) {
	nodeName := testNodeName
	lc, _ := createTestLeaseController(nodeName)

	// Create a test lease
	testLease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:            nodeName,
			Namespace:       corev1.NamespaceNodeLease,
			ResourceVersion: "1",
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &nodeName,
			LeaseDurationSeconds: &lc.leaseDurationSeconds,
			RenewTime:            &metav1.MicroTime{Time: time.Now().Add(-time.Minute)},
		},
	}

	// Pre-create the lease in the fake client
	_, err := lc.kubeClient.CoordinationV1().Leases(corev1.NamespaceNodeLease).Create(
		context.Background(), testLease, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test lease: %v", err)
	}

	// Test updating the lease
	err = lc.retryUpdateLease(testLease)
	if err != nil {
		t.Fatalf("Failed to update lease: %v", err)
	}

	// Verify the lease was updated
	if lc.latestLease == nil {
		t.Error("Expected latestLease to be set after update")
	}
}
