package syncer

import (
	"context"
	"fmt"
	"time"

	"reflect"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	"github.com/TKEColocation/tapestry/pkg/syncer/bottomup"
)

const (
	// Retry configuration
	defaultRetryBackoff    = 1 * time.Second
	defaultRetryMaxBackoff = 30 * time.Second
	defaultRetrySteps      = 10

	// Connection timeouts
	connectionTimeout = 30 * time.Second
)

// TapestrySyncer manages the synchronization between virtual and physical clusters
type TapestrySyncer struct {
	client.Client
	Scheme             *runtime.Scheme
	Log                logr.Logger
	ClusterBindingName string

	// Physical cluster connection
	physicalConfig *rest.Config

	// Cluster binding and syncers
	clusterBinding *cloudv1beta1.ClusterBinding
	bottomUpSyncer *bottomup.BottomUpSyncer
	topDownSyncer  *TopDownSyncer

	// virtual cluster manager
	manager manager.Manager
	// physical cluster manager
	physicalManager manager.Manager

	// Control channels
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewTapestrySyncer creates a new TapestrySyncer instance
func NewTapestrySyncer(mgr manager.Manager, client client.Client, scheme *runtime.Scheme, bindingName string) (*TapestrySyncer, error) {
	log := ctrl.Log.WithName("tapestry-syncer").WithValues("binding", fmt.Sprintf("%s", bindingName))

	return &TapestrySyncer{
		Client:             client,
		Scheme:             scheme,
		Log:                log,
		ClusterBindingName: bindingName,
		manager:            mgr,
		stopCh:             make(chan struct{}),
		doneCh:             make(chan struct{}),
	}, nil
}

// Start starts the Tapestry Syncer
func (ts *TapestrySyncer) Start(ctx context.Context) error {
	ts.Log.Info("Starting Tapestry Syncer")
	defer close(ts.doneCh)

	// Load the ClusterBinding
	if err := ts.loadClusterBinding(ctx); err != nil {
		return fmt.Errorf("failed to load cluster binding: %w", err)
	}

	// Configure physical cluster connection
	if err := ts.setupPhysicalClusterConnection(ctx); err != nil {
		return fmt.Errorf("failed to setup physical cluster connection: %w", err)
	}

	// Initialize bottom-up and top-down syncers
	if err := ts.initializeSyncers(); err != nil {
		return fmt.Errorf("failed to initialize syncers: %w", err)
	}

	// Setup and start ClusterBinding controller
	if err := ts.setupClusterBindingController(ctx); err != nil {
		return fmt.Errorf("failed to setup ClusterBinding controller: %w", err)
	}

	// Setup BottomUp Syncer
	if err := ts.bottomUpSyncer.Setup(ctx); err != nil {
		return fmt.Errorf("failed to setup BottomUp syncer: %w", err)
	}

	// Start the synchronization control loop
	ts.startSyncLoop(ctx)

	// Both managers are started by TapestrySyncer, just wait for caches to sync
	ts.Log.Info("Waiting for caches to sync")
	if !ts.physicalManager.GetCache().WaitForCacheSync(ctx) {
		return fmt.Errorf("failed to sync cache for physical cluster")
	}
	if !ts.manager.GetCache().WaitForCacheSync(ctx) {
		return fmt.Errorf("failed to sync cache for virtual cluster")
	}

	ts.Log.Info("Tapestry Syncer started successfully")

	// Wait for shutdown signal
	select {
	case <-ctx.Done():
		ts.Log.Info("Received shutdown signal")
	case <-ts.stopCh:
		ts.Log.Info("Received stop signal")
	}

	// Graceful shutdown
	ts.Log.Info("Tapestry Syncer stopping")
	ts.shutdown(ctx)
	return nil
}

// loadClusterBinding loads the ClusterBinding resource
func (ts *TapestrySyncer) loadClusterBinding(ctx context.Context) error {
	var binding cloudv1beta1.ClusterBinding
	key := types.NamespacedName{
		Name: ts.ClusterBindingName,
	}

	if err := ts.Get(ctx, key, &binding); err != nil {
		return fmt.Errorf("failed to get ClusterBinding %s: %w", key, err)
	}

	ts.clusterBinding = &binding
	ts.Log.Info("Loaded ClusterBinding", "name", binding.Name)

	return nil
}

// GetClusterBinding returns the current ClusterBinding
func (ts *TapestrySyncer) GetClusterBinding() *cloudv1beta1.ClusterBinding {
	return ts.clusterBinding
}

// setupPhysicalClusterConnection sets up connection to the physical cluster with retry mechanism
func (ts *TapestrySyncer) setupPhysicalClusterConnection(ctx context.Context) error {
	ts.Log.Info("Setting up physical cluster connection")

	var config *rest.Config

	// Retry setup with exponential backoff
	backoff := wait.Backoff{
		Duration: defaultRetryBackoff,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    defaultRetrySteps,
		Cap:      defaultRetryMaxBackoff,
	}

	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		// Read kubeconfig from the secret
		kubeconfigData, err := ts.readKubeconfigSecret(ctx)
		if err != nil {
			ts.Log.Error(err, "Failed to read kubeconfig secret, retrying...")
			return false, nil // Retry
		}

		// Create rest config from kubeconfig
		config, err = clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
		if err != nil {
			ts.Log.Error(err, "Failed to create rest config from kubeconfig")
			return false, fmt.Errorf("failed to create rest config: %w", err) // Don't retry
		}

		// Set connection timeout
		config.Timeout = connectionTimeout

		// Test the connection by creating a client
		_, err = client.New(config, client.Options{
			Scheme: ts.Scheme,
		})
		if err != nil {
			ts.Log.Error(err, "Failed to create physical cluster client")
			return false, fmt.Errorf("failed to create physical cluster client: %w", err) // Don't retry
		}

		// Test the connection by creating a kubernetes clientset
		kubeClient, err := kubernetes.NewForConfig(config)
		if err != nil {
			ts.Log.Error(err, "Failed to create kubernetes client")
			return false, fmt.Errorf("failed to create kubernetes client: %w", err) // Don't retry
		}

		if err := ts.testPhysicalClusterConnectivityWithTimeout(ctx, kubeClient); err != nil {
			ts.Log.Error(err, "Physical cluster connectivity test failed, retrying...")
			return false, nil // Retry
		}

		return true, nil // Success
	})

	if err != nil {
		return fmt.Errorf("failed to setup physical cluster connection after retries: %w", err)
	}

	ts.physicalConfig = config

	ts.Log.Info("Physical cluster connection established successfully")
	return nil
}

// readKubeconfigSecret reads kubeconfig data from the referenced secret
func (ts *TapestrySyncer) readKubeconfigSecret(ctx context.Context) ([]byte, error) {
	secretRef := ts.clusterBinding.Spec.SecretRef

	var secret corev1.Secret
	key := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: secretRef.Namespace,
	}

	if err := ts.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s: %w", key, err)
	}

	kubeconfigData, exists := secret.Data["kubeconfig"]
	if !exists {
		return nil, fmt.Errorf("kubeconfig key not found in secret %s", key)
	}

	if len(kubeconfigData) == 0 {
		return nil, fmt.Errorf("kubeconfig data is empty in secret %s", key)
	}

	return kubeconfigData, nil
}

// testPhysicalClusterConnectivityWithTimeout tests connectivity with timeout
func (ts *TapestrySyncer) testPhysicalClusterConnectivityWithTimeout(ctx context.Context, kubeClient kubernetes.Interface) error {
	// Create timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, connectionTimeout)
	defer cancel()

	// Test basic connectivity by getting server version
	done := make(chan error, 1)
	go func() {
		_, err := kubeClient.Discovery().ServerVersion()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("failed to connect to physical cluster: %w", err)
		}
		return nil
	case <-timeoutCtx.Done():
		return fmt.Errorf("physical cluster connectivity test timed out after %v", connectionTimeout)
	}
}

// initializeSyncers initializes the bottom-up and top-down syncers
func (ts *TapestrySyncer) initializeSyncers() error {
	ts.Log.Info("Initializing syncers")

	// Create physical cluster manager
	var err error
	ts.physicalManager, err = ctrl.NewManager(ts.physicalConfig, ctrl.Options{
		Scheme:         ts.Scheme,
		LeaderElection: false, // Disable leader election for physical manager
	})
	if err != nil {
		return fmt.Errorf("failed to create physical cluster manager: %w", err)
	}

	// Initialize bottom-up syncer (physical -> virtual)
	// Pass both virtualManager and physicalManager from TapestrySyncer
	ts.bottomUpSyncer = bottomup.NewBottomUpSyncer(ts.manager, ts.physicalManager, ts.Scheme, ts.clusterBinding)

	// Initialize top-down syncer (virtual -> physical)
	ts.topDownSyncer = NewTopDownSyncer(ts.Client, ts.Scheme, ts.clusterBinding)

	ts.Log.Info("Syncers initialized successfully")
	return nil
}

// startSyncLoop starts the synchronization control loop
func (ts *TapestrySyncer) startSyncLoop(ctx context.Context) {
	ts.Log.Info("Starting sync loop")

	// Start bottom-up syncer
	go func() {
		ts.Log.Info("Starting bottom-up syncer")
		if err := ts.bottomUpSyncer.Start(ctx); err != nil {
			ts.Log.Error(err, "Bottom-up syncer failed")
		}
	}()

	// Start top-down syncer
	go func() {
		ts.Log.Info("Starting top-down syncer")
		if err := ts.topDownSyncer.Start(ctx); err != nil {
			ts.Log.Error(err, "Top-down syncer failed")
		}
	}()

	// Start physical cluster manager
	go func() {
		ts.Log.Info("Starting physical cluster manager")
		if err := ts.physicalManager.Start(ctx); err != nil {
			ts.Log.Error(err, "Physical cluster manager failed")
		}
	}()

	ts.Log.Info("Sync loop started successfully")
}

// shutdown performs graceful shutdown of the syncer
func (ts *TapestrySyncer) shutdown(_ context.Context) {
	ts.Log.Info("Shutting down Tapestry Syncer")

	// TODO: Implement graceful shutdown of syncers
	// This will be enhanced when syncers have more complex lifecycle management

	ts.Log.Info("Tapestry Syncer shutdown completed")
}

// Stop stops the Tapestry Syncer
func (ts *TapestrySyncer) Stop() {
	close(ts.stopCh)
}

// Done returns a channel that's closed when the syncer is completely stopped
func (ts *TapestrySyncer) Done() <-chan struct{} {
	return ts.doneCh
}

// setupClusterBindingController sets up the ClusterBinding controller
func (ts *TapestrySyncer) setupClusterBindingController(ctx context.Context) error {
	ts.Log.Info("Setting up ClusterBinding controller")

	if ts.manager == nil {
		return fmt.Errorf("manager is not initialized")
	}

	// Setup ClusterBinding reconciler
	reconciler := &ClusterBindingReconciler{
		Client:             ts.manager.GetClient(),
		Log:                ts.Log.WithName("clusterbinding-reconciler"),
		ClusterBindingName: ts.ClusterBindingName,
		BottomUpSyncer:     ts.bottomUpSyncer,
	}

	if err := reconciler.SetupWithManager(ts.manager); err != nil {
		return fmt.Errorf("failed to setup ClusterBinding reconciler: %w", err)
	}

	// no need to start the manager, it's already started by the main manager
	ts.Log.Info("ClusterBinding controller started successfully")
	return nil
}

// ClusterBindingReconciler reconciles ClusterBinding objects for this specific TapestrySyncer
type ClusterBindingReconciler struct {
	client.Client
	Log                     logr.Logger
	ClusterBindingName      string
	ClusterBindingNamespace string
	BottomUpSyncer          *bottomup.BottomUpSyncer
}

//+kubebuilder:rbac:groups=cloud.tencent.com,resources=clusterbindings,verbs=get;list;watch
//+kubebuilder:rbac:groups=cloud.tencent.com,resources=clusterbindings/status,verbs=get

// Reconcile handles ClusterBinding events for this specific binding
func (r *ClusterBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("clusterbinding", req.NamespacedName)

	// Only handle our specific ClusterBinding
	if req.Name != r.ClusterBindingName {
		return ctrl.Result{}, nil
	}

	logger.Info("ClusterBinding event received")

	// Get the ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{}
	err := r.Get(ctx, req.NamespacedName, clusterBinding)
	if err != nil {
		if errors.IsNotFound(err) {
			// ClusterBinding was deleted, trigger cleanup
			logger.Info("ClusterBinding deleted, triggering cleanup")
			return r.handleClusterBindingDeletion(ctx)
		}
		logger.Error(err, "Failed to get ClusterBinding")
		return ctrl.Result{}, err
	}

	// Check if nodeSelector changed
	if r.hasNodeSelectorChanged(clusterBinding) {
		logger.Info("ClusterBinding nodeSelector changed, triggering node re-evaluation")
		return r.handleNodeSelectorChange(ctx, clusterBinding)
	}

	return ctrl.Result{}, nil
}

// hasNodeSelectorChanged checks if the nodeSelector has changed
func (r *ClusterBindingReconciler) hasNodeSelectorChanged(newBinding *cloudv1beta1.ClusterBinding) bool {
	if r.BottomUpSyncer.ClusterBinding == nil {
		return true // First time loading
	}

	oldSelector := r.BottomUpSyncer.ClusterBinding.Spec.NodeSelector
	newSelector := newBinding.Spec.NodeSelector

	return !reflect.DeepEqual(oldSelector, newSelector)
}

// handleClusterBindingDeletion handles ClusterBinding deletion
func (r *ClusterBindingReconciler) handleClusterBindingDeletion(_ context.Context) (ctrl.Result, error) {
	r.Log.Info("Handling ClusterBinding deletion")

	// Signal the TapestrySyncer to stop
	if r.BottomUpSyncer != nil {
		// Trigger cleanup of all virtual nodes
		r.Log.Info("Triggering cleanup of all virtual nodes")
		// The bottomUpSyncer should handle cleanup when it detects the binding is gone
	}

	return ctrl.Result{}, nil
}

// handleNodeSelectorChange handles nodeSelector changes
func (r *ClusterBindingReconciler) handleNodeSelectorChange(ctx context.Context, newBinding *cloudv1beta1.ClusterBinding) (ctrl.Result, error) {
	r.Log.Info("Handling nodeSelector change")

	// Get old and new nodeSelectors
	var oldSelector map[string]string
	if r.BottomUpSyncer.ClusterBinding != nil {
		oldSelector = r.BottomUpSyncer.ClusterBinding.Spec.NodeSelector
	}
	newSelector := newBinding.Spec.NodeSelector

	// Get nodes that match old selector
	var oldNodes []string
	var err error
	if len(oldSelector) > 0 {
		oldNodes, err = r.getNodesMatchingSelector(ctx, oldSelector)
		if err != nil {
			r.Log.Error(err, "Failed to get nodes matching old selector")
			return ctrl.Result{}, err
		}
	}

	// Get nodes that match new selector
	newNodes, err := r.getNodesMatchingSelector(ctx, newSelector)
	if err != nil {
		r.Log.Error(err, "Failed to get nodes matching new selector")
		return ctrl.Result{}, err
	}

	// Combine and deduplicate node names (union of old and new)
	affectedNodes := r.unionAndDeduplicateNodes(oldNodes, newNodes)

	r.Log.Info("Found affected nodes", "count", len(affectedNodes), "nodes", affectedNodes)

	// Update the cached ClusterBinding
	r.BottomUpSyncer.ClusterBinding = newBinding

	// Trigger node re-evaluation in bottomUpSyncer
	if r.BottomUpSyncer != nil && len(affectedNodes) > 0 {
		r.Log.Info("Triggering node re-evaluation due to nodeSelector change", "affectedNodes", affectedNodes)
		if err := r.BottomUpSyncer.RequeueNode(affectedNodes); err != nil {
			r.Log.Error(err, "Failed to requeue nodes")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// getNodesMatchingSelector gets all nodes that match the given selector from physical cluster
func (r *ClusterBindingReconciler) getNodesMatchingSelector(ctx context.Context, selector map[string]string) ([]string, error) {
	if r.BottomUpSyncer == nil {
		return nil, fmt.Errorf("BottomUpSyncer not available")
	}

	r.Log.Info("Getting nodes matching selector", "selector", selector)

	// Use BottomUpSyncer to get nodes from physical cluster
	return r.BottomUpSyncer.GetNodesMatchingSelector(ctx, selector)
}

// unionAndDeduplicateNodes combines two slices of node names and removes duplicates
func (r *ClusterBindingReconciler) unionAndDeduplicateNodes(oldNodes, newNodes []string) []string {
	// Use map to track unique nodes
	nodeSet := make(map[string]bool)

	// Add old nodes
	for _, node := range oldNodes {
		nodeSet[node] = true
	}

	// Add new nodes
	for _, node := range newNodes {
		nodeSet[node] = true
	}

	// Convert back to slice
	result := make([]string, 0, len(nodeSet))
	for node := range nodeSet {
		result = append(result, node)
	}

	return result
}

// SetupWithManager sets up the controller with the Manager
func (r *ClusterBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudv1beta1.ClusterBinding{},
			builder.WithPredicates(
				predicate.Funcs{
					CreateFunc: func(e event.CreateEvent) bool {
						obj, ok := e.Object.(*cloudv1beta1.ClusterBinding)
						if ok && obj.Name == r.ClusterBindingName {
							return true
						}
						return false
					},
					UpdateFunc: func(e event.UpdateEvent) bool {
						obj, ok := e.ObjectNew.(*cloudv1beta1.ClusterBinding)
						if ok && obj.Name == r.ClusterBindingName {
							return true
						}
						return false
					},
					DeleteFunc: func(e event.DeleteEvent) bool {
						obj, ok := e.Object.(*cloudv1beta1.ClusterBinding)
						if ok && obj.Name == r.ClusterBindingName {
							return true
						}
						return false
					},
				},
			)).
		Complete(r)
}
