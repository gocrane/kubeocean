package syncer

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	"github.com/TKEColocation/tapestry/pkg/syncer/bottomup"
	"github.com/TKEColocation/tapestry/pkg/syncer/topdown"
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
	topDownSyncer  *topdown.TopDownSyncer

	// virtual cluster manager
	manager manager.Manager
	// physical cluster manager
	physicalManager manager.Manager

	// Control channels
	stopCh   chan struct{}
	phyMgrCh chan struct{}

	// Physical client QPS and Burst
	physicalClientQPS   int
	physicalClientBurst int
}

// NewTapestrySyncer creates a new TapestrySyncer instance
func NewTapestrySyncer(mgr manager.Manager, client client.Client, scheme *runtime.Scheme, bindingName string, physicalClientQPS int, physicalClientBurst int) (*TapestrySyncer, error) {
	log := ctrl.Log.WithName("tapestry-syncer").WithValues("binding", bindingName)

	return &TapestrySyncer{
		Client:              client,
		Scheme:              scheme,
		Log:                 log,
		ClusterBindingName:  bindingName,
		manager:             mgr,
		stopCh:              make(chan struct{}),
		phyMgrCh:            make(chan struct{}),
		physicalClientQPS:   physicalClientQPS,
		physicalClientBurst: physicalClientBurst,
	}, nil
}

// Start starts the Tapestry Syncer
func (ts *TapestrySyncer) Start(ctx context.Context) error {
	ts.Log.Info("Starting Tapestry Syncer")

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

	// Setup TopDown Syncer
	if err := ts.topDownSyncer.Setup(ctx); err != nil {
		return fmt.Errorf("failed to setup TopDown syncer: %w", err)
	}

	// Start the synchronization control loop
	ts.startSyncLoop(ctx)

	// Let managers handle their own cache synchronization internally

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
	<-ts.phyMgrCh
	ts.Log.Info("Physical cluster manager stopped")
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

		// Set QPS and Burst for the physical client
		if ts.physicalClientQPS > 0 {
			config.QPS = float32(ts.physicalClientQPS)
		} else {
			// disable client-side ratelimiter
			config.QPS = -1
		}
		if ts.physicalClientBurst > 0 {
			config.Burst = ts.physicalClientBurst
		}

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
		Metrics: server.Options{
			BindAddress: "0",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create physical cluster manager: %w", err)
	}

	// Initialize bottom-up syncer (physical -> virtual)
	// Pass both virtualManager and physicalManager from TapestrySyncer
	ts.bottomUpSyncer = bottomup.NewBottomUpSyncer(ts.manager, ts.physicalManager, ts.Scheme, ts.clusterBinding)

	// Initialize top-down syncer (virtual -> physical)
	// Pass both virtualManager, physicalManager and physicalConfig from TapestrySyncer
	ts.topDownSyncer = topdown.NewTopDownSyncer(ts.manager, ts.physicalManager, ts.physicalConfig, ts.Scheme, ts.clusterBinding)

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
		close(ts.phyMgrCh)
	}()

	ts.Log.Info("Sync loop started successfully")
}

// shutdown performs graceful shutdown of the syncer
func (ts *TapestrySyncer) shutdown(_ context.Context) {
	ts.Log.Info("Shutting down Tapestry Syncer")

	// Top-down syncer doesn't need explicit stop
	// It's managed by the controller-runtime manager lifecycle

	// Bottom-up syncer doesn't have a Stop method yet
	// It's managed by the controller-runtime manager lifecycle

	ts.Log.Info("Tapestry Syncer shutdown completed")
}

// Stop stops the Tapestry Syncer
func (ts *TapestrySyncer) Stop() {
	close(ts.stopCh)
}

// setupClusterBindingController sets up the ClusterBinding controller
func (ts *TapestrySyncer) setupClusterBindingController(_ context.Context) error {
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
