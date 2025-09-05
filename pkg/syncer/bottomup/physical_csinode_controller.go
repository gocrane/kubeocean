package bottomup

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

// PhysicalCSINodeReconciler reconciles CSINode objects from physical cluster
type PhysicalCSINodeReconciler struct {
	ClusterBindingName string
	// Cached binding for optimization
	ClusterBinding *cloudv1beta1.ClusterBinding

	PhysicalClient client.Client
	VirtualClient  client.Client
	Scheme         *runtime.Scheme
	Log            logr.Logger

	workQueue workqueue.TypedRateLimitingInterface[reconcile.Request]
}

//+kubebuilder:rbac:groups=storage.k8s.io,resources=csinodes,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles CSINode events from physical cluster
func (r *PhysicalCSINodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("physicalCSINode", req.NamespacedName)

	// Get the physical CSINode
	physicalCSINode := &storagev1.CSINode{}
	err := r.PhysicalClient.Get(ctx, req.NamespacedName, physicalCSINode)
	if err != nil {
		if errors.IsNotFound(err) {
			// CSINode was deleted, clean up virtual CSINode
			log.Info("Physical CSINode deleted, cleaning up virtual CSINode")
			return r.handleCSINodeDeletion(ctx, req.Name)
		}
		log.Error(err, "Failed to get physical CSINode")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if physicalCSINode.DeletionTimestamp != nil {
		return r.handleCSINodeDeletion(ctx, physicalCSINode.Name)
	}

	// Process the CSINode
	return r.processCSINode(ctx, physicalCSINode)
}

// processCSINode processes a physical CSINode and creates/updates corresponding virtual CSINode
func (r *PhysicalCSINodeReconciler) processCSINode(ctx context.Context, physicalCSINode *storagev1.CSINode) (ctrl.Result, error) {
	log := r.Log.WithValues("physicalCSINode", physicalCSINode.Name)

	// Generate virtual node name (same mapping as PhysicalNodeReconciler)
	if r.getClusterID() == "" {
		return ctrl.Result{}, fmt.Errorf("clusterID is unknown, skipping processCSINode")
	}
	virtualNodeName := GenerateVirtualNodeName(r.getClusterID(), physicalCSINode.Name)

	// Check if virtual node exists
	virtualNode := &corev1.Node{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Virtual node doesn't exist, delete virtual CSINode if exists
			log.Info("Virtual node does not exist, deleting virtual CSINode")
			return r.deleteVirtualCSINode(ctx, virtualNodeName)
		}
		log.Error(err, "Failed to get virtual node")
		return ctrl.Result{}, err
	}

	// Validate virtual node labels match current physical cluster and node
	if !r.validateVirtualNodeLabels(virtualNode, physicalCSINode.Name) {
		log.Error(fmt.Errorf("virtual node labels do not match current physical cluster and node"),
			"Virtual node validation failed",
			"virtualNode", virtualNodeName,
			"physicalNode", physicalCSINode.Name)
		// Don't requeue, just log error
		return ctrl.Result{}, nil
	}

	// Create or update virtual CSINode
	err = r.createOrUpdateVirtualCSINode(ctx, physicalCSINode, virtualNodeName)
	if err != nil {
		log.Error(err, "Failed to create or update virtual CSINode")
		return ctrl.Result{}, err
	}

	log.V(1).Info("Successfully processed physical CSINode")

	// no need to requeue for CSINode
	return ctrl.Result{}, nil
}

// handleCSINodeDeletion handles deletion of physical CSINode
func (r *PhysicalCSINodeReconciler) handleCSINodeDeletion(ctx context.Context, physicalCSINodeName string) (ctrl.Result, error) {
	// Generate virtual node name
	if r.getClusterID() == "" {
		return ctrl.Result{}, fmt.Errorf("clusterID is unknown, skipping handleCSINodeDeletion")
	}
	virtualNodeName := GenerateVirtualNodeName(r.getClusterID(), physicalCSINodeName)

	// Delete virtual CSINode
	return r.deleteVirtualCSINode(ctx, virtualNodeName)
}

// deleteVirtualCSINode deletes virtual CSINode if it exists and is managed by tapestry
func (r *PhysicalCSINodeReconciler) deleteVirtualCSINode(ctx context.Context, virtualNodeName string) (ctrl.Result, error) {
	log := r.Log.WithValues("virtualNode", virtualNodeName)

	virtualCSINode := &storagev1.CSINode{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualCSINode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Virtual CSINode doesn't exist, nothing to do
			log.V(1).Info("Virtual CSINode does not exist")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get virtual CSINode")
		return ctrl.Result{}, err
	}

	// Check if managed by tapestry
	if virtualCSINode.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		log.V(1).Info("Virtual CSINode is not managed by tapestry, skipping deletion")
		return ctrl.Result{}, nil
	}

	// Delete with UID for safety
	log.Info("Deleting virtual CSINode")
	err = r.VirtualClient.Delete(ctx, virtualCSINode, &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID: &virtualCSINode.UID,
		},
	})
	if err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to delete virtual CSINode")
		return ctrl.Result{}, err
	}

	log.Info("Successfully deleted virtual CSINode")
	return ctrl.Result{}, nil
}

// createOrUpdateVirtualCSINode creates or updates virtual CSINode
func (r *PhysicalCSINodeReconciler) createOrUpdateVirtualCSINode(ctx context.Context, physicalCSINode *storagev1.CSINode, virtualNodeName string) error {
	logger := r.Log.WithValues("virtualCSINode", virtualNodeName, "physicalCSINode", physicalCSINode.Name)

	virtualCSINode := &storagev1.CSINode{
		ObjectMeta: metav1.ObjectMeta{
			Name:        virtualNodeName,
			Labels:      r.buildVirtualCSINodeLabels(physicalCSINode),
			Annotations: r.buildVirtualCSINodeAnnotations(physicalCSINode),
		},
		Spec: physicalCSINode.Spec,
	}

	// Check if virtual CSINode already exists
	existingCSINode := &storagev1.CSINode{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, existingCSINode)

	if err != nil {
		if errors.IsNotFound(err) {
			// CSINode doesn't exist, create it
			logger.Info("Creating virtual CSINode", "virtualCSINode", virtualCSINode)
			return r.VirtualClient.Create(ctx, virtualCSINode)
		}
		return fmt.Errorf("failed to get virtual CSINode: %w", err)
	} else {
		// CSINode exists, update it
		// Check if managed by tapestry
		if existingCSINode.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
			logger.V(1).Info("Virtual CSINode is not managed by tapestry, skipping update")
			return nil
		}

		// Update spec and annotations
		newCSINode := existingCSINode.DeepCopy()
		newCSINode.Spec = virtualCSINode.Spec
		newCSINode.Annotations = virtualCSINode.Annotations
		newCSINode.Labels = virtualCSINode.Labels

		if !reflect.DeepEqual(existingCSINode, newCSINode) {
			logger.Info("Updating virtual CSINode", "newCSINode", newCSINode)
			err = r.VirtualClient.Update(ctx, newCSINode)
			if err != nil {
				return fmt.Errorf("failed to update virtual CSINode: %w", err)
			}
		}

		return nil
	}
}

// buildVirtualCSINodeLabels builds labels for virtual CSINode
func (r *PhysicalCSINodeReconciler) buildVirtualCSINodeLabels(physicalCSINode *storagev1.CSINode) map[string]string {
	labels := physicalCSINode.Labels
	if labels == nil {
		labels = make(map[string]string)
	}

	// Add Tapestry-specific labels
	labels[cloudv1beta1.LabelManagedBy] = cloudv1beta1.LabelManagedByValue
	labels[cloudv1beta1.LabelClusterBinding] = r.ClusterBindingName
	labels[cloudv1beta1.LabelPhysicalClusterID] = r.getClusterID()
	labels[cloudv1beta1.LabelPhysicalNodeName] = physicalCSINode.Name

	r.Log.V(1).Info("Built virtual CSINode labels", "physicalCSINode", physicalCSINode.Name, "labels", labels)

	return labels
}

// buildVirtualCSINodeAnnotations builds annotations for virtual CSINode
func (r *PhysicalCSINodeReconciler) buildVirtualCSINodeAnnotations(physicalCSINode *storagev1.CSINode) map[string]string {
	annotations := physicalCSINode.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Add Tapestry-specific annotations
	annotations[cloudv1beta1.AnnotationLastSyncTime] = time.Now().Format(time.RFC3339)

	// Add physical CSINode metadata
	annotations[cloudv1beta1.LabelPhysicalNodeName] = physicalCSINode.Name
	annotations["tapestry.io/physical-csinode-uid"] = string(physicalCSINode.UID)

	r.Log.V(1).Info("Built virtual CSINode annotations", "physicalCSINode", physicalCSINode.Name, "annotations", annotations)

	return annotations
}

// validateVirtualNodeLabels validates that virtual node labels match current physical cluster and node
func (r *PhysicalCSINodeReconciler) validateVirtualNodeLabels(virtualNode *corev1.Node, physicalNodeName string) bool {
	if virtualNode.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		return false
	}

	// Check cluster binding label
	if virtualNode.Labels[cloudv1beta1.LabelClusterBinding] != r.ClusterBindingName {
		return false
	}

	// Check physical cluster ID label
	if virtualNode.Labels[cloudv1beta1.LabelPhysicalClusterID] != r.getClusterID() {
		return false
	}

	// Check physical node name label
	if virtualNode.Labels[cloudv1beta1.LabelPhysicalNodeName] != physicalNodeName {
		return false
	}

	return true
}

// getClusterID returns the cluster ID
func (r *PhysicalCSINodeReconciler) getClusterID() string {
	if r.ClusterBinding != nil {
		return r.ClusterBinding.Spec.ClusterID
	}
	return ""
}

// TriggerReconciliation triggers reconciliation for a specific CSINode
func (r *PhysicalCSINodeReconciler) TriggerReconciliation(csiNodeName string) error {
	if r.workQueue == nil {
		return fmt.Errorf("work queue not initialized")
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: csiNodeName,
		},
	}

	r.workQueue.Add(req)
	return nil
}

// handleVirtualNodeEvent handles virtual node addition and deletion events
func (r *PhysicalCSINodeReconciler) handleVirtualNodeEvent(node *corev1.Node, eventType string) {
	log := r.Log.WithValues("virtualNode", node.Name, "eventType", eventType)

	// Check if this is a tapestry-managed node
	if node.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		log.V(1).Info("Virtual node not managed by tapestry, skipping")
		return
	}

	// Check if this node belongs to current cluster binding
	if node.Labels[cloudv1beta1.LabelClusterBinding] != r.ClusterBindingName {
		log.V(1).Info("Virtual node belongs to different cluster binding, skipping",
			"nodeClusterBinding", node.Labels[cloudv1beta1.LabelClusterBinding],
			"currentClusterBinding", r.ClusterBindingName)
		return
	}

	// Get physical node name from label
	physicalNodeName := node.Labels[cloudv1beta1.LabelPhysicalNodeName]
	if physicalNodeName == "" {
		log.V(1).Info("Virtual node missing physical node name label, skipping")
		return
	}

	log.Info("Virtual node event, triggering CSINode reconciliation",
		"eventType", eventType, "physicalNode", physicalNodeName)

	// Trigger reconciliation for the physical CSINode
	if err := r.TriggerReconciliation(physicalNodeName); err != nil {
		log.Error(err, "Failed to trigger CSINode reconciliation")
	}
}

// handleVirtualNodeEvent handles virtual node addition and deletion events
func (r *PhysicalCSINodeReconciler) handleVirtualCSINodeEvent(csinode *storagev1.CSINode, eventType string) {
	log := r.Log.WithValues("virtualCSINode", csinode.Name, "eventType", eventType)

	// Check if this is a tapestry-managed node
	if csinode.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		log.V(1).Info("Virtual CSINode not managed by tapestry, skipping")
		return
	}

	// Check if this node belongs to current cluster binding
	if csinode.Labels[cloudv1beta1.LabelClusterBinding] != r.ClusterBindingName {
		log.V(1).Info("Virtual CSINode belongs to different cluster binding, skipping",
			"nodeClusterBinding", csinode.Labels[cloudv1beta1.LabelClusterBinding],
			"currentClusterBinding", r.ClusterBindingName)
		return
	}

	// Get physical node name from label
	physicalNodeName := csinode.Labels[cloudv1beta1.LabelPhysicalNodeName]
	if physicalNodeName == "" {
		log.V(1).Info("Virtual CSINode missing physical node name label, skipping")
		return
	}

	log.Info("Virtual CSINode event, triggering CSINode reconciliation",
		"eventType", eventType, "physicalNode", physicalNodeName)

	// Trigger reconciliation for the physical CSINode
	if err := r.TriggerReconciliation(physicalNodeName); err != nil {
		log.Error(err, "Failed to trigger CSINode reconciliation")
	}
}

// SetupWithManager sets up the controller with the manager
func (r *PhysicalCSINodeReconciler) SetupWithManager(physicalManager, virtualManager ctrl.Manager) error {

	uniqueControllerName := fmt.Sprintf("csiNode-%s", r.ClusterBindingName)

	rateLimiter := workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 5*time.Minute)
	r.workQueue = workqueue.NewTypedRateLimitingQueueWithConfig(rateLimiter, workqueue.TypedRateLimitingQueueConfig[reconcile.Request]{
		Name: uniqueControllerName,
	})

	// Setup virtualManager node informer for watching virtual node changes
	nodeInformer, err := virtualManager.GetCache().GetInformer(context.TODO(), &corev1.Node{})
	if err != nil {
		return fmt.Errorf("failed to get node informer: %w", err)
	}

	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node := obj.(*corev1.Node)
			if node != nil {
				r.handleVirtualNodeEvent(node, "add")
			}
		},
		DeleteFunc: func(obj interface{}) {
			node := obj.(*corev1.Node)
			if node != nil {
				r.handleVirtualNodeEvent(node, "delete")
			}
		},
	})

	csinodeInformer, err := virtualManager.GetCache().GetInformer(context.TODO(), &storagev1.CSINode{})
	if err != nil {
		return fmt.Errorf("failed to get csi node informer: %w", err)
	}
	csinodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			csinode := obj.(*storagev1.CSINode)
			if csinode != nil {
				r.handleVirtualCSINodeEvent(csinode, "add")
			}
		},
	})

	return ctrl.NewControllerManagedBy(physicalManager).
		For(&storagev1.CSINode{}).
		Named(uniqueControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
			RateLimiter:             rateLimiter,
			NewQueue: func(controllerName string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
				return r.workQueue
			},
		}).
		Complete(r)
}
