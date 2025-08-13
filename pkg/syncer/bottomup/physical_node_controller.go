package bottomup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

const (
	// Label keys for virtual nodes (additional to those in bottomup_syncer.go)
	LabelVirtualNodeType = "tapestry.io/node-type"

	// Virtual node type
	VirtualNodeTypeValue = "virtual"
)

// PhysicalNodeReconciler reconciles Node objects from physical cluster
type PhysicalNodeReconciler struct {
	PhysicalClient client.Client
	VirtualClient  client.Client
	Scheme         *runtime.Scheme
	ClusterBinding *cloudv1beta1.ClusterBinding
	Log            logr.Logger

	// Work queue for triggering manual reconciliation
	workQueue workqueue.TypedRateLimitingInterface[reconcile.Request]
}

//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=nodes/status,verbs=get;update;patch

// Reconcile handles Node events from physical cluster
func (r *PhysicalNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalNode", req.NamespacedName)

	// Get the physical node
	physicalNode := &corev1.Node{}
	err := r.PhysicalClient.Get(ctx, req.NamespacedName, physicalNode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Node was deleted, clean up virtual node
			logger.Info("Physical node deleted, cleaning up virtual node")
			return r.handleNodeDeletion(ctx, req.Name)
		}
		logger.Error(err, "Failed to get physical node")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if physicalNode.DeletionTimestamp != nil {
		return r.handleNodeDeletion(ctx, physicalNode.Name)
	}

	// Process the node
	return r.processNode(ctx, physicalNode)
}

// processNode processes a physical node and creates/updates corresponding virtual node
func (r *PhysicalNodeReconciler) processNode(ctx context.Context, physicalNode *corev1.Node) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalNode", physicalNode.Name)

	// Check if node should be processed based on ResourceLeasingPolicy
	policies, err := r.getApplicablePolicies(ctx, physicalNode)
	if err != nil {
		logger.Error(err, "Failed to get applicable policies")
		return ctrl.Result{}, err
	}

	if len(policies) == 0 {
		// No applicable policies, ensure virtual node is deleted
		logger.V(1).Info("No applicable policies for node")
		return r.handleNodeDeletion(ctx, physicalNode.Name)
	}

	// Check if within time windows
	if !r.isWithinTimeWindows(policies) {
		logger.V(1).Info("Node outside time windows")
		return r.handleNodeDeletion(ctx, physicalNode.Name)
	}

	// Calculate available resources
	availableResources := r.calculateAvailableResources(physicalNode, policies)

	// Create or update virtual node
	err = r.createOrUpdateVirtualNode(ctx, physicalNode, availableResources, policies)
	if err != nil {
		logger.Error(err, "Failed to create or update virtual node")
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Successfully processed physical node")
	return ctrl.Result{RequeueAfter: DefaultNodeSyncInterval}, nil
}

// handleNodeDeletion handles deletion of physical node
func (r *PhysicalNodeReconciler) handleNodeDeletion(ctx context.Context, physicalNodeName string) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalNode", physicalNodeName)

	virtualNodeName := r.generateVirtualNodeName(physicalNodeName)

	virtualNode := &corev1.Node{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Virtual node doesn't exist, nothing to do
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual node for deletion")
		return ctrl.Result{}, err
	}

	// Delete virtual node
	logger.Info("Deleting virtual node", "virtualNode", virtualNodeName)
	err = r.VirtualClient.Delete(ctx, virtualNode)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to delete virtual node")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// getApplicablePolicies gets ResourceLeasingPolicies that apply to the given node
func (r *PhysicalNodeReconciler) getApplicablePolicies(ctx context.Context, node *corev1.Node) ([]cloudv1beta1.ResourceLeasingPolicy, error) {
	var policyList cloudv1beta1.ResourceLeasingPolicyList
	if err := r.VirtualClient.List(ctx, &policyList); err != nil {
		return nil, fmt.Errorf("failed to list ResourceLeasingPolicies: %w", err)
	}

	var applicablePolicies []cloudv1beta1.ResourceLeasingPolicy
	for _, policy := range policyList.Items {
		// Check if policy references our ClusterBinding
		if policy.Spec.Cluster == r.ClusterBinding.Name {
			// Check if node matches the intersection of policy and ClusterBinding nodeSelectors
			if r.nodeMatchesCombinedSelector(node, policy.Spec.NodeSelector, r.ClusterBinding.Spec.NodeSelector) {
				applicablePolicies = append(applicablePolicies, policy)
			}
		}
	}

	return applicablePolicies, nil
}

// nodeMatchesSelector checks if node matches the given node selector
func (r *PhysicalNodeReconciler) nodeMatchesSelector(node *corev1.Node, nodeSelector map[string]string) bool {
	if len(nodeSelector) == 0 {
		return true // No selector means all nodes match
	}

	for key, value := range nodeSelector {
		if nodeValue, exists := node.Labels[key]; !exists || nodeValue != value {
			return false
		}
	}
	return true
}

// isWithinTimeWindows checks if current time is within any of the policy time windows
func (r *PhysicalNodeReconciler) isWithinTimeWindows(policies []cloudv1beta1.ResourceLeasingPolicy) bool {
	now := time.Now()
	currentDay := strings.ToLower(now.Weekday().String())
	currentTime := now.Format("15:04")

	for _, policy := range policies {
		for _, window := range policy.Spec.TimeWindows {
			// Check if current day is in the allowed days
			// If no days specified, assume all days are allowed
			dayMatches := len(window.Days) == 0
			if !dayMatches {
				for _, day := range window.Days {
					if strings.ToLower(day) == currentDay {
						dayMatches = true
						break
					}
				}
			}

			if !dayMatches {
				continue
			}

			// Check if current time is within the window
			if currentTime >= window.Start && currentTime <= window.End {
				return true
			}
		}
	}

	// If no time windows specified, assume always available
	hasTimeWindows := false
	for _, policy := range policies {
		if len(policy.Spec.TimeWindows) > 0 {
			hasTimeWindows = true
			break
		}
	}

	return !hasTimeWindows
}

// calculateAvailableResources calculates available resources based on policies
func (r *PhysicalNodeReconciler) calculateAvailableResources(node *corev1.Node, policies []cloudv1beta1.ResourceLeasingPolicy) corev1.ResourceList {
	// Get node's total capacity
	totalCapacity := node.Status.Capacity.DeepCopy()

	// Start with total capacity
	availableResources := corev1.ResourceList{}

	// Apply resource limits from policies if any exist
	if len(policies) > 0 {
		// Collect all resource limits from all policies
		resourceLimits := make(map[corev1.ResourceName]resource.Quantity)

		for _, policy := range policies {
			for _, limit := range policy.Spec.ResourceLimits {
				resourceName := corev1.ResourceName(limit.Resource)
				limitQuantity := limit.Quantity

				// For multiple policies, use the minimum limit (most restrictive)
				if existingLimit, exists := resourceLimits[resourceName]; exists {
					if limitQuantity.Cmp(existingLimit) < 0 {
						resourceLimits[resourceName] = limitQuantity
					}
				} else {
					resourceLimits[resourceName] = limitQuantity
				}
			}
		}

		// Apply the collected limits
		for resourceName, limitQuantity := range resourceLimits {
			// Get the current capacity for this resource
			if totalQuantity, exists := totalCapacity[resourceName]; exists {
				// Take the minimum of policy limit and total capacity
				if limitQuantity.Cmp(totalQuantity) < 0 {
					availableResources[resourceName] = limitQuantity
				} else {
					availableResources[resourceName] = totalQuantity
				}
			} else {
				// Resource not found in node capacity, still apply the limit
				availableResources[resourceName] = limitQuantity
			}
		}

		// For resources not specified in any policy, apply conservative default
		for resourceName, quantity := range totalCapacity {
			if _, hasLimit := resourceLimits[resourceName]; !hasLimit {
				// Conservative approach: use 80% of available capacity
				conservativeQuantity := quantity.DeepCopy()
				conservativeQuantity.Set(quantity.Value() * 8 / 10)
				availableResources[resourceName] = conservativeQuantity
			}
		}
	}

	// If no specific limits from policies, use a conservative default (e.g., 80% of capacity)
	if len(availableResources) == 0 {
		for resourceName, quantity := range totalCapacity {
			// Conservative approach: use 80% of available capacity
			conservativeQuantity := quantity.DeepCopy()
			conservativeQuantity.Set(quantity.Value() * 8 / 10)
			availableResources[resourceName] = conservativeQuantity
		}
	}

	return availableResources
}

// createOrUpdateVirtualNode creates or updates a virtual node based on physical node
func (r *PhysicalNodeReconciler) createOrUpdateVirtualNode(ctx context.Context, physicalNode *corev1.Node, resources corev1.ResourceList, policies []cloudv1beta1.ResourceLeasingPolicy) error {
	virtualNodeName := r.generateVirtualNodeName(physicalNode.Name)

	// Check if virtual node already exists
	existingNode := &corev1.Node{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, existingNode)

	virtualNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        virtualNodeName,
			Labels:      r.buildVirtualNodeLabels(physicalNode),
			Annotations: r.buildVirtualNodeAnnotations(physicalNode, policies),
		},
		Spec: corev1.NodeSpec{
			// Copy taints from physical node (filtered)
			Taints: r.filterTaints(physicalNode.Spec.Taints),
		},
		Status: corev1.NodeStatus{
			Capacity:    resources,
			Allocatable: resources,
			Conditions:  r.buildNodeConditions(physicalNode),
			NodeInfo:    physicalNode.Status.NodeInfo,
			Phase:       corev1.NodeRunning,
		},
	}

	if err != nil {
		if errors.IsNotFound(err) {
			// Node doesn't exist, create it
			r.Log.Info("Creating virtual node", "virtualNode", virtualNodeName, "physicalNode", physicalNode.Name)
			return r.VirtualClient.Create(ctx, virtualNode)
		}
		return fmt.Errorf("failed to get virtual node: %w", err)
	} else {
		// Node exists, update it
		virtualNode.ResourceVersion = existingNode.ResourceVersion
		r.Log.V(1).Info("Updating virtual node", "virtualNode", virtualNodeName, "physicalNode", physicalNode.Name)
		return r.VirtualClient.Update(ctx, virtualNode)
	}
}

// generateVirtualNodeName generates a virtual node name based on physical node
func (r *PhysicalNodeReconciler) generateVirtualNodeName(physicalNodeName string) string {
	// Format: vnode-worker-{cluster-id}-{node-name}
	clusterID := r.getClusterID()
	// Sanitize node name for Kubernetes naming requirements
	sanitizedNodeName := strings.ToLower(strings.ReplaceAll(physicalNodeName, "_", "-"))
	return fmt.Sprintf("%s-%s-%s", VirtualNodePrefix, clusterID, sanitizedNodeName)
}

// getClusterID returns a cluster identifier for the physical cluster
func (r *PhysicalNodeReconciler) getClusterID() string {
	// Use ClusterBinding name as cluster ID
	return strings.ToLower(strings.ReplaceAll(r.ClusterBinding.Name, "_", "-"))
}

// buildVirtualNodeLabels builds labels for virtual node
func (r *PhysicalNodeReconciler) buildVirtualNodeLabels(physicalNode *corev1.Node) map[string]string {
	labels := make(map[string]string)

	// Copy relevant labels from physical node
	for key, value := range physicalNode.Labels {
		// Skip certain system labels that shouldn't be copied
		if !r.shouldSkipLabel(key) {
			labels[key] = value
		}
	}

	// Add Tapestry-specific labels
	labels[LabelPhysicalClusterID] = r.getClusterID()
	labels[LabelPhysicalNodeName] = physicalNode.Name
	labels[LabelManagedBy] = "tapestry"
	labels[LabelVirtualNodeType] = VirtualNodeTypeValue

	return labels
}

// buildVirtualNodeAnnotations builds annotations for virtual node
func (r *PhysicalNodeReconciler) buildVirtualNodeAnnotations(physicalNode *corev1.Node, policies []cloudv1beta1.ResourceLeasingPolicy) map[string]string {
	annotations := make(map[string]string)

	// Copy relevant annotations from physical node
	for key, value := range physicalNode.Annotations {
		if !r.shouldSkipAnnotation(key) {
			annotations[key] = value
		}
	}

	// Add Tapestry-specific annotations
	annotations[AnnotationLastSyncTime] = time.Now().Format(time.RFC3339)

	// Add policy information
	if len(policies) > 0 {
		var policyNames []string
		for _, policy := range policies {
			policyNames = append(policyNames, policy.Name)
		}
		// Set both annotations for compatibility
		annotations[AnnotationResourcePolicy] = strings.Join(policyNames, ",")
		annotations[AnnotationPoliciesApplied] = strings.Join(policyNames, ",")
	}

	return annotations
}

// shouldSkipLabel determines if a label should not be copied to virtual node
func (r *PhysicalNodeReconciler) shouldSkipLabel(key string) bool {
	skipPrefixes := []string{
		"node.kubernetes.io/",
		"kubernetes.io/hostname",
		"kubernetes.io/arch",
		"kubernetes.io/os",
		"beta.kubernetes.io/",
	}

	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// shouldSkipAnnotation determines if an annotation should not be copied to virtual node
func (r *PhysicalNodeReconciler) shouldSkipAnnotation(key string) bool {
	skipPrefixes := []string{
		"node.alpha.kubernetes.io/",
		"volumes.kubernetes.io/",
		"csi.volume.kubernetes.io/",
	}

	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// filterTaints filters taints that should be copied to virtual node
func (r *PhysicalNodeReconciler) filterTaints(taints []corev1.Taint) []corev1.Taint {
	var filteredTaints []corev1.Taint

	for _, taint := range taints {
		// Skip certain system taints
		if !r.shouldSkipTaint(taint.Key) {
			filteredTaints = append(filteredTaints, taint)
		}
	}

	return filteredTaints
}

// shouldSkipTaint determines if a taint should not be copied to virtual node
func (r *PhysicalNodeReconciler) shouldSkipTaint(key string) bool {
	skipTaints := []string{
		"node.kubernetes.io/not-ready",
		"node.kubernetes.io/unreachable",
		"node.kubernetes.io/unschedulable",
		"node.kubernetes.io/memory-pressure",
		"node.kubernetes.io/disk-pressure",
		"node.kubernetes.io/pid-pressure",
		"node.kubernetes.io/network-unavailable",
	}

	for _, skipTaint := range skipTaints {
		if key == skipTaint {
			return true
		}
	}
	return false
}

// buildNodeConditions builds node conditions for virtual node based on physical node
func (r *PhysicalNodeReconciler) buildNodeConditions(physicalNode *corev1.Node) []corev1.NodeCondition {
	// Start with basic ready condition
	conditions := []corev1.NodeCondition{
		{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionTrue,
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             "TapestryNodeReady",
			Message:            "Virtual node is ready",
		},
	}

	// Copy relevant conditions from physical node
	for _, condition := range physicalNode.Status.Conditions {
		switch condition.Type {
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			// Copy resource pressure conditions
			conditions = append(conditions, condition)
		}
	}

	return conditions
}

// nodeMatchesCombinedSelector checks if node matches the intersection of policy and ClusterBinding nodeSelectors
func (r *PhysicalNodeReconciler) nodeMatchesCombinedSelector(node *corev1.Node, policySelector, clusterSelector map[string]string) bool {
	// Node must match both policy selector and cluster selector (intersection)

	// First check if node matches policy selector
	if !r.nodeMatchesSelector(node, policySelector) {
		return false
	}

	// Then check if node matches cluster selector
	if !r.nodeMatchesSelector(node, clusterSelector) {
		return false
	}

	return true
}

// TriggerReconciliation triggers manual reconciliation for a specific node via the work queue
func (r *PhysicalNodeReconciler) TriggerReconciliation(nodeName string) error {
	if r.workQueue == nil {
		return fmt.Errorf("work queue not initialized")
	}

	r.Log.Info("Enqueuing node for reconciliation", "node", nodeName)

	// Add the node name to the work queue
	r.workQueue.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: nodeName,
		},
	})

	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *PhysicalNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 20,
			RateLimiter:             workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 5*time.Minute),
			NewQueue: func(controllerName string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
				wq := workqueue.NewTypedRateLimitingQueueWithConfig(rateLimiter, workqueue.TypedRateLimitingQueueConfig[reconcile.Request]{
					Name: controllerName,
				})
				r.workQueue = wq
				return wq
			},
		}).
		Complete(r)
}
