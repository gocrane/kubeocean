package syncer

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	"github.com/TKEColocation/tapestry/pkg/syncer/bottomup"
)

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
	var oldSelector *corev1.NodeSelector
	if r.BottomUpSyncer.ClusterBinding != nil {
		oldSelector = r.BottomUpSyncer.ClusterBinding.Spec.NodeSelector
	}
	newSelector := newBinding.Spec.NodeSelector

	// Get nodes that match old selector
	var oldNodes []string
	var err error
	if oldSelector != nil && len(oldSelector.NodeSelectorTerms) > 0 {
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
func (r *ClusterBindingReconciler) getNodesMatchingSelector(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
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
	// Use unique controller name to avoid conflicts when multiple TapestrySyncer instances are running
	controllerName := fmt.Sprintf("clusterbinding-%s", r.ClusterBindingName)

	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
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
