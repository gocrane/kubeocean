package bottomup

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

const (
	// Virtual node name prefix
	VirtualNodePrefix = "vnode-worker"

	// Labels for virtual nodes
	LabelPhysicalClusterID = "tapestry.io/physical-cluster-id"
	LabelPhysicalNodeName  = "tapestry.io/physical-node-name"
	LabelManagedBy         = "tapestry.io/managed-by"

	// Annotations for virtual nodes
	AnnotationLastSyncTime    = "tapestry.io/last-sync-time"
	AnnotationResourcePolicy  = "tapestry.io/resource-policy"
	AnnotationPoliciesApplied = "tapestry.io/policies-applied"

	// Sync intervals
	DefaultNodeSyncInterval   = 30 * time.Second
	DefaultPolicySyncInterval = 60 * time.Second
)

// BottomUpSyncer handles synchronization from physical cluster to virtual cluster
type BottomUpSyncer struct {
	Scheme         *runtime.Scheme
	Log            logr.Logger
	ClusterBinding *cloudv1beta1.ClusterBinding

	// Physical cluster manager for controllers (passed from TapestrySyncer)
	physicalManager manager.Manager
	// Virtual cluster manager for ResourceLeasingPolicy controller (passed from TapestrySyncer)
	virtualManager manager.Manager

	// Reconciler reference for triggering reconciliation
	nodeReconciler *PhysicalNodeReconciler
}

// NewBottomUpSyncer creates a new BottomUpSyncer instance
func NewBottomUpSyncer(virtualManager manager.Manager, physicalManager manager.Manager, scheme *runtime.Scheme, binding *cloudv1beta1.ClusterBinding) *BottomUpSyncer {
	log := ctrl.Log.WithName("bottom-up-syncer").WithValues("cluster", binding.Name)

	return &BottomUpSyncer{
		virtualManager:  virtualManager,  // Passed from TapestrySyncer
		physicalManager: physicalManager, // Passed from TapestrySyncer
		Scheme:          scheme,
		Log:             log,
		ClusterBinding:  binding,
	}
}

func (bus *BottomUpSyncer) Setup(ctx context.Context) error {
	bus.Log.Info("Setup Bottom-up Syncer with controller-runtime")

	// Setup managers
	if err := bus.setupManagers(); err != nil {
		return fmt.Errorf("failed to setup managers: %w", err)
	}

	// Setup controllers
	if err := bus.setupControllers(); err != nil {
		return fmt.Errorf("failed to setup controllers: %w", err)
	}
	return nil
}

// Start starts the bottom-up syncer with controller-runtime approach
func (bus *BottomUpSyncer) Start(ctx context.Context) error {
	bus.Log.Info("Bottom-up Syncer started successfully")

	// Keep running until context is cancelled
	<-ctx.Done()
	bus.Log.Info("Bottom-up Syncer stopped")

	return nil
}

// RequeueNode triggers re-reconciliation of specific nodes
func (bus *BottomUpSyncer) RequeueNode(nodeNames []string) error {
	if bus.nodeReconciler == nil {
		return fmt.Errorf("node reconciler not initialized")
	}

	bus.Log.Info("Requeuing nodes for reconciliation", "nodes", nodeNames)

	// Trigger reconciliation for each node through the reconciler's work queue
	for _, nodeName := range nodeNames {
		if err := bus.nodeReconciler.TriggerReconciliation(nodeName); err != nil {
			bus.Log.Error(err, "Failed to trigger reconciliation", "node", nodeName)
			// Continue with other nodes even if one fails
		}
	}

	return nil
}

// GetNodesMatchingSelector gets all nodes from physical cluster that match the given selector
func (bus *BottomUpSyncer) GetNodesMatchingSelector(ctx context.Context, selector map[string]string) ([]string, error) {
	if bus.physicalManager == nil {
		return nil, fmt.Errorf("physical manager not initialized")
	}

	// Get physical cluster client
	physicalClient := bus.physicalManager.GetClient()

	// List all nodes from physical cluster
	var nodeList corev1.NodeList
	if err := physicalClient.List(ctx, &nodeList); err != nil {
		return nil, fmt.Errorf("failed to list nodes from physical cluster: %w", err)
	}

	var matchingNodes []string
	for _, node := range nodeList.Items {
		if bus.nodeMatchesSelector(&node, selector) {
			matchingNodes = append(matchingNodes, node.Name)
		}
	}

	bus.Log.Info("Found nodes matching selector", "selector", selector, "matchingNodes", matchingNodes)
	return matchingNodes, nil
}

// nodeMatchesSelector checks if a node matches the given selector
func (bus *BottomUpSyncer) nodeMatchesSelector(node *corev1.Node, selector map[string]string) bool {
	// If no selector specified, match all nodes
	if len(selector) == 0 {
		return true
	}

	// Check if all selector labels match node labels
	for key, expectedValue := range selector {
		if nodeValue, exists := node.Labels[key]; !exists || nodeValue != expectedValue {
			return false
		}
	}

	return true
}

// setupManagers validates that both managers are provided by TapestrySyncer
func (bus *BottomUpSyncer) setupManagers() error {
	// Both managers should be provided by TapestrySyncer
	if bus.virtualManager == nil {
		return fmt.Errorf("virtualManager must be provided by TapestrySyncer")
	}

	if bus.physicalManager == nil {
		return fmt.Errorf("physicalManager must be provided by TapestrySyncer")
	}

	bus.Log.Info("Managers setup completed", "physicalManager", "provided by TapestrySyncer", "virtualManager", "provided by TapestrySyncer")
	return nil
}

// setupControllers sets up the controllers with their respective managers
func (bus *BottomUpSyncer) setupControllers() error {
	// Setup Physical Node Controller
	nodeReconciler := &PhysicalNodeReconciler{
		PhysicalClient: bus.physicalManager.GetClient(),
		VirtualClient:  bus.virtualManager.GetClient(),
		Scheme:         bus.Scheme,
		ClusterBinding: bus.ClusterBinding,
		Log:            bus.Log.WithName("physical-node-controller"),
	}

	// Save reference to the reconciler for triggering reconciliation
	bus.nodeReconciler = nodeReconciler

	if err := nodeReconciler.SetupWithManager(bus.physicalManager); err != nil {
		return fmt.Errorf("failed to setup physical node controller: %w", err)
	}

	// Setup ResourceLeasingPolicy Controller
	policyReconciler := &ResourceLeasingPolicyReconciler{
		Client:         bus.virtualManager.GetClient(),
		VirtualClient:  bus.virtualManager.GetClient(),
		Scheme:         bus.Scheme,
		ClusterBinding: bus.ClusterBinding,
		Log:            bus.Log.WithName("resource-leasing-policy-controller"),
	}

	if err := policyReconciler.SetupWithManager(bus.virtualManager); err != nil {
		return fmt.Errorf("failed to setup resource leasing policy controller: %w", err)
	}

	bus.Log.Info("Controllers setup completed")
	return nil
}
