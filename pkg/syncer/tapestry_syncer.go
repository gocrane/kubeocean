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

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
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
	Scheme                  *runtime.Scheme
	Log                     logr.Logger
	ClusterBindingName      string
	ClusterBindingNamespace string

	// Physical cluster connection
	physicalConfig     *rest.Config
	physicalClient     client.Client
	physicalKubeClient kubernetes.Interface

	// Cluster binding and syncers
	clusterBinding *cloudv1beta1.ClusterBinding
	bottomUpSyncer *BottomUpSyncer
	topDownSyncer  *TopDownSyncer

	// Control channels
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewTapestrySyncer creates a new TapestrySyncer instance
func NewTapestrySyncer(client client.Client, scheme *runtime.Scheme, bindingName, bindingNamespace string) (*TapestrySyncer, error) {
	log := ctrl.Log.WithName("tapestry-syncer").WithValues("binding", fmt.Sprintf("%s/%s", bindingNamespace, bindingName))

	return &TapestrySyncer{
		Client:                  client,
		Scheme:                  scheme,
		Log:                     log,
		ClusterBindingName:      bindingName,
		ClusterBindingNamespace: bindingNamespace,
		stopCh:                  make(chan struct{}),
		doneCh:                  make(chan struct{}),
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
	ts.initializeSyncers()

	// Start the synchronization control loop
	ts.startSyncLoop(ctx)

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
		Name:      ts.ClusterBindingName,
		Namespace: ts.ClusterBindingNamespace,
	}

	if err := ts.Get(ctx, key, &binding); err != nil {
		return fmt.Errorf("failed to get ClusterBinding %s: %w", key, err)
	}

	ts.clusterBinding = &binding
	ts.Log.Info("Loaded ClusterBinding", "name", binding.Name, "namespace", binding.Namespace)

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
	var physicalClient client.Client
	var kubeClient kubernetes.Interface

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

		// Create controller-runtime client for physical cluster
		physicalClient, err = client.New(config, client.Options{
			Scheme: ts.Scheme,
		})
		if err != nil {
			ts.Log.Error(err, "Failed to create physical cluster client")
			return false, fmt.Errorf("failed to create physical cluster client: %w", err) // Don't retry
		}

		// Create kubernetes clientset for additional operations
		kubeClient, err = kubernetes.NewForConfig(config)
		if err != nil {
			ts.Log.Error(err, "Failed to create kubernetes client")
			return false, fmt.Errorf("failed to create kubernetes client: %w", err) // Don't retry
		}

		// Test the connection
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
	ts.physicalClient = physicalClient
	ts.physicalKubeClient = kubeClient

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
func (ts *TapestrySyncer) initializeSyncers() {
	ts.Log.Info("Initializing syncers")

	// Initialize bottom-up syncer (physical -> virtual)
	ts.bottomUpSyncer = NewBottomUpSyncer(ts.Client, ts.physicalClient, ts.Scheme, ts.clusterBinding)

	// Initialize top-down syncer (virtual -> physical)
	ts.topDownSyncer = NewTopDownSyncer(ts.Client, ts.physicalClient, ts.Scheme, ts.clusterBinding)

	ts.Log.Info("Syncers initialized successfully")
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
