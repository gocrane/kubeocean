package bottomup

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultRenewIntervalFraction is the fraction of lease duration to renew the lease
	DefaultRenewIntervalFraction = 0.25
	// DefaultLeaseDuration is the default lease duration in seconds
	DefaultLeaseDuration = 40
	// maxUpdateRetries is the number of immediate, successive retries when renewing the lease
	maxUpdateRetries = 5
	// maxBackoff is the maximum sleep time during backoff
	maxBackoff = 7 * time.Second
)

// LeaseController manages the lease for a single virtual node
type LeaseController struct {
	// Configuration
	nodeName             string
	leaseDurationSeconds int32
	renewInterval        time.Duration

	// Clients and utilities
	kubeClient    kubernetes.Interface
	virtualClient client.Client
	logger        logr.Logger
	clock         clock.Clock

	// State management
	latestLease *coordinationv1.Lease

	// Control
	ctx        context.Context
	cancelFunc context.CancelFunc
	stopped    chan struct{}
	mu         sync.RWMutex
}

// NewLeaseController creates a new lease controller for a virtual node
func NewLeaseController(nodeName string, kubeClient kubernetes.Interface, virtualClient client.Client, logger logr.Logger) *LeaseController {
	leaseDuration := int32(DefaultLeaseDuration)
	renewInterval := time.Duration(float64(leaseDuration)*DefaultRenewIntervalFraction) * time.Second

	ctx, cancel := context.WithCancel(context.Background())

	return &LeaseController{
		nodeName:             nodeName,
		leaseDurationSeconds: leaseDuration,
		renewInterval:        renewInterval,
		kubeClient:           kubeClient,
		virtualClient:        virtualClient,
		logger:               logger.WithValues("node", nodeName),
		clock:                clock.RealClock{},
		ctx:                  ctx,
		cancelFunc:           cancel,
		stopped:              make(chan struct{}),
	}
}

// Start starts the lease renewal process
func (lc *LeaseController) Start() {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	if lc.ctx.Err() != nil {
		// Already stopped or cancelled
		lc.logger.Info("Lease controller already stopped or cancelled")
		return
	}

	lc.logger.Info("Starting lease controller")
	go lc.run()
}

// Stop stops the lease renewal process
func (lc *LeaseController) Stop() {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	lc.logger.Info("Stopping lease controller")
	lc.cancelFunc()

	// Wait for the controller to stop
	select {
	case <-lc.stopped:
		lc.logger.Info("Lease controller stopped")
	case <-time.After(5 * time.Second):
		lc.logger.Error(nil, "Timeout waiting for lease controller to stop")
	}
}

// IsRunning returns true if the lease controller is currently running
func (lc *LeaseController) IsRunning() bool {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.ctx.Err() == nil
}

// run is the main loop for lease renewal
func (lc *LeaseController) run() {
	defer close(lc.stopped)

	lc.logger.Info("Lease controller started", "renewInterval", lc.renewInterval)

	// Initial sync
	lc.sync(lc.ctx)

	// Periodic renewal
	wait.UntilWithContext(lc.ctx, lc.sync, lc.renewInterval)

	lc.logger.Info("Lease controller run loop exited")
}

// sync performs a single lease renewal cycle
func (lc *LeaseController) sync(ctx context.Context) {
	if lc.ctx.Err() != nil {
		return
	}

	logger := lc.logger.WithValues("renewTime", lc.clock.Now())

	// Check node status before updating lease
	node, err := lc.getVirtualNode(ctx)
	if err != nil {
		logger.Error(err, "Failed to get virtual node")
		return
	}

	if !lc.isNodeReady(node) {
		logger.V(1).Info("Node is not ready, skipping lease update")
		return
	}

	// Try to update existing lease first (optimistic approach)
	if lc.latestLease != nil {
		if err := lc.retryUpdateLease(lc.latestLease); err == nil {
			logger.V(1).Info("Successfully updated lease using cached version")
			return
		}
		logger.V(1).Info("Failed to update lease using cached version, falling back to ensure lease")
	}

	// Ensure lease exists and update it
	lease, created := lc.backoffEnsureLease(node)
	if lease == nil {
		logger.Error(fmt.Errorf("failed to ensure lease exists"), "Failed to ensure lease exists")
		return
	}

	lc.latestLease = lease

	// If we just created the lease, no need to update it
	if created {
		logger.Info("Successfully created new lease")
		return
	}

	// Update the existing lease
	if err := lc.retryUpdateLease(lease); err != nil {
		logger.Error(err, "Failed to update lease after ensuring it exists")
	} else {
		logger.V(1).Info("Successfully updated lease")
	}
}

// getVirtualNode retrieves the virtual node from the virtual cluster with retry mechanism
func (lc *LeaseController) getVirtualNode(ctx context.Context) (*corev1.Node, error) {
	var node *corev1.Node
	var lastErr error

	// Retry with 400ms interval and 1s timeout to handle virtualNode list-watch issues
	err := wait.PollUntilContextTimeout(ctx, 400*time.Millisecond, time.Second, true, func(ctx context.Context) (bool, error) {
		n := &corev1.Node{}
		err := lc.virtualClient.Get(ctx, client.ObjectKey{Name: lc.nodeName}, n)
		if err != nil {
			lastErr = err
			// Continue retrying for any error (including NotFound) to handle list-watch sync issues
			lc.logger.V(1).Info("Failed to get virtual node, retrying", "error", err)
			return false, nil
		}
		node = n
		return true, nil
	})

	if err != nil {
		if lastErr != nil {
			return nil, fmt.Errorf("failed to get node %s after retries: %w", lc.nodeName, lastErr)
		}
		return nil, fmt.Errorf("failed to get node %s: %w", lc.nodeName, err)
	}

	return node, nil
}

// isNodeReady checks if the node is in Ready condition
func (lc *LeaseController) isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// backoffEnsureLease ensures the lease exists with exponential backoff
func (lc *LeaseController) backoffEnsureLease(node *corev1.Node) (*coordinationv1.Lease, bool) {
	var (
		lease   *coordinationv1.Lease
		created bool
		err     error
	)

	sleep := 100 * time.Millisecond
	for {
		if lc.ctx.Err() != nil {
			return nil, false
		}

		lease, created, err = lc.ensureLease(node)
		if err == nil {
			return lease, created
		}

		sleep = minDuration(2*sleep, maxBackoff)
		lc.logger.Error(err, "Failed to ensure lease exists, retrying", "retryIn", sleep)

		timer := lc.clock.NewTimer(sleep)
		select {
		case <-timer.C():
			timer.Stop()
		case <-lc.ctx.Done():
			timer.Stop()
			return nil, false
		}
	}
}

// ensureLease creates the lease if it doesn't exist
func (lc *LeaseController) ensureLease(node *corev1.Node) (*coordinationv1.Lease, bool, error) {
	leaseClient := lc.kubeClient.CoordinationV1().Leases(corev1.NamespaceNodeLease)

	lease, err := leaseClient.Get(lc.ctx, lc.nodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Create new lease
		leaseToCreate := lc.newLease(node)
		if len(leaseToCreate.OwnerReferences) == 0 {
			// We want to ensure that a lease will always have OwnerReferences set.
			// Thus, given that we weren't able to set it correctly, we simply
			// not create it this time - we will retry in the next iteration.
			return nil, false, nil
		}
		lease, err := leaseClient.Create(lc.ctx, leaseToCreate, metav1.CreateOptions{})
		if err != nil {
			return nil, false, fmt.Errorf("failed to create lease: %w", err)
		}
		lc.logger.Info("Successfully created lease")
		return lease, true, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("failed to get lease: %w", err)
	}

	lc.logger.V(1).Info("Successfully retrieved existing lease")
	return lease, false, nil
}

// retryUpdateLease attempts to update the lease with retries
func (lc *LeaseController) retryUpdateLease(base *coordinationv1.Lease) error {
	leaseClient := lc.kubeClient.CoordinationV1().Leases(corev1.NamespaceNodeLease)

	for i := 0; i < maxUpdateRetries; i++ {
		if lc.ctx.Err() != nil {
			return lc.ctx.Err()
		}

		// Create a copy of the lease and update the renew time
		lease := base.DeepCopy()
		lease.Spec.RenewTime = &metav1.MicroTime{Time: lc.clock.Now()}

		lease, err := leaseClient.Update(lc.ctx, lease, metav1.UpdateOptions{})
		if err == nil {
			lc.logger.V(1).Info("Successfully updated lease", "retries", i)
			lc.latestLease = lease
			return nil
		}

		lc.logger.Error(err, "Failed to update lease", "attempt", i+1)

		// Handle conflict by getting the latest version
		if apierrors.IsConflict(err) {
			latestLease, err := leaseClient.Get(lc.ctx, lc.nodeName, metav1.GetOptions{})
			if err == nil {
				base = latestLease
				continue
			}
		}

		// For other errors, just retry
		if i < maxUpdateRetries-1 {
			time.Sleep(time.Duration(i+1) * time.Second)
		}
	}

	return fmt.Errorf("failed to update lease after %d attempts", maxUpdateRetries)
}

// newLease creates a new lease or updates an existing one
func (lc *LeaseController) newLease(node *corev1.Node) *coordinationv1.Lease {
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lc.nodeName,
			Namespace: corev1.NamespaceNodeLease,
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To(lc.nodeName),
			LeaseDurationSeconds: ptr.To(lc.leaseDurationSeconds),
			RenewTime:            &metav1.MicroTime{Time: lc.clock.Now()},
		},
	}

	// Setting owner reference needs node's UID. Note that it is different from
	// kubelet.nodeRef.UID. When lease is initially created, it is possible that
	// the connection between master and node is not ready yet. So try to set
	// owner reference every time when renewing the lease, until successful.
	if node != nil {
		lease.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
				Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
				Name:       node.Name,
				UID:        node.UID,
			},
		}
	}

	return lease
}

// minDuration returns the smaller of two durations
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
