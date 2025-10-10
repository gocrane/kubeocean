package bottomup

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/syncer/bottomup/hostport"
	"github.com/TKEColocation/kubeocean/pkg/utils"
)

// BottomUpSyncer handles synchronization from physical cluster to virtual cluster
type BottomUpSyncer struct {
	Scheme         *runtime.Scheme
	Log            logr.Logger
	ClusterBinding *cloudv1beta1.ClusterBinding

	// Physical cluster manager for controllers (passed from KubeoceanSyncer)
	physicalManager manager.Manager
	// Virtual cluster manager for ResourceLeasingPolicy controller (passed from KubeoceanSyncer)
	virtualManager manager.Manager

	// Reconciler reference for triggering reconciliation
	nodeReconciler *PhysicalNodeReconciler

	// VNode Prometheus base port (injected from KubeoceanSyncer)
	prometheusVNodeBasePort int
}

// SetPrometheusVNodeBasePort sets the base port used when constructing VNode Prometheus URLs
func (bus *BottomUpSyncer) SetPrometheusVNodeBasePort(port int) {
	bus.prometheusVNodeBasePort = port
}

// NewBottomUpSyncer creates a new BottomUpSyncer instance
func NewBottomUpSyncer(virtualManager manager.Manager, physicalManager manager.Manager, scheme *runtime.Scheme, binding *cloudv1beta1.ClusterBinding) *BottomUpSyncer {
	log := ctrl.Log.WithName("bottom-up-syncer").WithValues("cluster", binding.Name)

	return &BottomUpSyncer{
		virtualManager:  virtualManager,  // Passed from KubeoceanSyncer
		physicalManager: physicalManager, // Passed from KubeoceanSyncer
		Scheme:          scheme,
		Log:             log,
		ClusterBinding:  binding,
	}
}

func (bus *BottomUpSyncer) Setup(ctx context.Context) error {
	bus.Log.Info("Setup Bottom-up Syncer with controller-runtime")

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
	bus.Log.Info("Bottom-up Syncer stopping, cleaning up resources")

	// Clean up PhysicalNodeReconciler resources
	if bus.nodeReconciler != nil {
		bus.nodeReconciler.Stop()
	}

	bus.Log.Info("Bottom-up Syncer stopped")

	return nil
}

// RequeueNodes triggers re-reconciliation of specific nodes
func (bus *BottomUpSyncer) RequeueNodes(nodeNames []string) error {
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
func (bus *BottomUpSyncer) GetNodesMatchingSelector(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
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
	for idx, node := range nodeList.Items {
		if bus.nodeMatchesSelector(&nodeList.Items[idx], selector) {
			matchingNodes = append(matchingNodes, node.Name)
		}
	}

	bus.Log.Info("Found nodes matching selector", "selector", selector, "matchingNodes", matchingNodes)
	return matchingNodes, nil
}

// nodeMatchesSelector checks if node matches the given node selector using shared utilities
func (bus *BottomUpSyncer) nodeMatchesSelector(node *corev1.Node, nodeSelector *corev1.NodeSelector) bool {
	return utils.MatchesSelector(node, nodeSelector)
}

// setupControllers sets up the controllers with their respective managers
func (bus *BottomUpSyncer) setupControllers() error {
	// Create Kubernetes client for lease management
	kubeClient, err := kubernetes.NewForConfig(bus.virtualManager.GetConfig())
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Setup Physical CSINode Controller
	csiNodeReconciler := &PhysicalCSINodeReconciler{
		PhysicalClient:     bus.physicalManager.GetClient(),
		VirtualClient:      bus.virtualManager.GetClient(),
		Scheme:             bus.Scheme,
		ClusterBindingName: bus.ClusterBinding.Name,
		ClusterBinding:     bus.ClusterBinding,
		Log:                bus.Log.WithName("physical-csinode-controller"),
	}

	if err := csiNodeReconciler.SetupWithManager(bus.physicalManager, bus.virtualManager); err != nil {
		return fmt.Errorf("failed to setup physical CSINode controller: %w", err)
	}

	// Setup Physical Node Controller
	nodeReconciler := &PhysicalNodeReconciler{
		PhysicalClient:          bus.physicalManager.GetClient(),
		VirtualClient:           bus.virtualManager.GetClient(),
		KubeClient:              kubeClient,
		Scheme:                  bus.Scheme,
		ClusterBindingName:      bus.ClusterBinding.Name,
		ClusterBinding:          bus.ClusterBinding,
		Log:                     bus.Log.WithName("physical-node-controller"),
		PrometheusVNodeBasePort: bus.prometheusVNodeBasePort,
	}

	// Save reference to the reconciler for triggering reconciliation
	bus.nodeReconciler = nodeReconciler

	// 初始化端口分配缓存（在 SetupWithManager 之前，直接 List VNode）
	bus.Log.Info("Initializing port allocation cache before setting up node controller")
	if err := nodeReconciler.InitAllocatedPortsCache(context.Background()); err != nil {
		return fmt.Errorf("failed to initialize port allocation cache: %w", err)
	}
	bus.Log.Info("Port allocation cache initialized successfully")

	if err := nodeReconciler.SetupWithManager(bus.physicalManager, bus.virtualManager); err != nil {
		return fmt.Errorf("failed to setup physical node controller: %w", err)
	}

	// Setup Physical Pod Controller for status synchronization
	// This implements requirement 3.4, 3.5 - Pod status monitoring and sync
	podReconciler := &PhysicalPodReconciler{
		PhysicalClient: bus.physicalManager.GetClient(),
		VirtualClient:  bus.virtualManager.GetClient(),
		Scheme:         bus.Scheme,
		ClusterBinding: bus.ClusterBinding,
		Log:            bus.Log.WithName("physical-pod-controller"),
	}

	if err := podReconciler.SetupWithManager(bus.physicalManager); err != nil {
		return fmt.Errorf("failed to setup physical pod controller: %w", err)
	}

	// Setup ResourceLeasingPolicy Controller
	policyReconciler := &ResourceLeasingPolicyReconciler{
		Client:         bus.physicalManager.GetClient(),
		ClusterBinding: bus.ClusterBinding,
		Log:            bus.Log.WithName("resource-leasing-policy-controller"),
		// Provide functions for triggering node re-evaluation
		GetNodesMatchingSelector: bus.GetNodesMatchingSelector,
		RequeueNodes:             bus.RequeueNodes,
	}

	if err := policyReconciler.SetupWithManager(bus.physicalManager); err != nil {
		return fmt.Errorf("failed to setup resource leasing policy controller: %w", err)
	}

	// Setup HostPort Node Controller
	hostPortReconciler := &hostport.HostPortNodeReconciler{
		PhysicalClient:     bus.physicalManager.GetClient(),
		VirtualClient:      bus.virtualManager.GetClient(),
		Scheme:             bus.Scheme,
		ClusterBindingName: bus.ClusterBinding.Name,
		ClusterBinding:     bus.ClusterBinding,
		Log:                bus.Log.WithName("hostport-node-controller"),
	}

	if err := hostPortReconciler.SetupWithManager(bus.physicalManager, bus.virtualManager); err != nil {
		return fmt.Errorf("failed to setup hostport node controller: %w", err)
	}

	bus.Log.Info("Controllers setup completed",
		"csiNodeController", "enabled",
		"nodeController", "enabled",
		"podController", "enabled",
		"policyController", "enabled",
		"hostPortController", "enabled")
	return nil
}