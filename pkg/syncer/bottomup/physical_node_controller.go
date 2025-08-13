package bottomup

import (
	"context"
	"fmt"
	"sort"
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
	ClusterBindingName string
	// Optional cached binding for helpers/tests; live reads happen in processNode
	ClusterBinding *cloudv1beta1.ClusterBinding

	PhysicalClient client.Client
	VirtualClient  client.Client
	Scheme         *runtime.Scheme
	Log            logr.Logger

	// Work queue for triggering manual reconciliation
	workQueue workqueue.TypedRateLimitingInterface[reconcile.Request]
}

//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=nodes/status,verbs=get;update;patch

// Reconcile handles Node events from physical cluster
func (r *PhysicalNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("physicalNode", req.NamespacedName)

	// Get the physical node
	physicalNode := &corev1.Node{}
	err := r.PhysicalClient.Get(ctx, req.NamespacedName, physicalNode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Node was deleted, clean up virtual node
			log.Info("Physical node deleted, cleaning up virtual node")
			return r.handleNodeDeletion(ctx, req.Name)
		}
		log.Error(err, "Failed to get physical node")
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
// This implements requirement 3.1, 3.2, 3.3 - real-time monitoring and status synchronization
func (r *PhysicalNodeReconciler) processNode(ctx context.Context, physicalNode *corev1.Node) (ctrl.Result, error) {
	log := r.Log.WithValues("physicalNode", physicalNode.Name)

	// Implement requirement 3.2 - sync status within 5 seconds by processing immediately
	startTime := time.Now()
	defer func() {
		syncDuration := time.Since(startTime)
		log.V(1).Info("Node processing completed", "duration", syncDuration)

		// Log warning if sync takes longer than expected
		if syncDuration > 5*time.Second {
			log.Info("Node sync took longer than expected",
				"duration", syncDuration,
				"expected", "5s",
				"node", physicalNode.Name)
		}
	}()

	// Load latest ClusterBinding and check nodeSelector match first
	var clusterBinding cloudv1beta1.ClusterBinding
	if err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: r.getClusterBindingName()}, &clusterBinding); err != nil {
		log.Error(err, "Failed to get cluster binding")
		return ctrl.Result{}, err
	}

	// If node does not match ClusterBinding selector, ensure virtual node is deleted
	if !r.nodeMatchesSelector(physicalNode, clusterBinding.Spec.NodeSelector) {
		log.V(1).Info("Node does not match ClusterBinding selector; deleting virtual node")
		return r.handleNodeDeletion(ctx, physicalNode.Name)
	}

	// Find applicable ResourceLeasingPolicies by node selector
	policies, err := r.getApplicablePolicies(ctx, physicalNode, clusterBinding.Spec.NodeSelector)
	if err != nil {
		log.Error(err, "Failed to get applicable policies")
		return ctrl.Result{}, err
	}

	// If multiple policies match, sort by creation time and use the earliest
	if len(policies) > 1 {
		sort.Slice(policies, func(i, j int) bool {
			// Older (earlier creation) comes first
			return policies[i].CreationTimestamp.Time.Before(policies[j].CreationTimestamp.Time)
		})
		var ignored []string
		for idx := 1; idx < len(policies); idx++ {
			ignored = append(ignored, policies[idx].Name)
		}
		log.Info("Multiple ResourceLeasingPolicies matched. Using the earliest one and ignoring others.",
			"selected", policies[0].Name, "ignored", strings.Join(ignored, ","))
		// Keep only the earliest policy effective
		policies = policies[:1]
	}

	// If no matching policy, use all remaining resources; otherwise validate time windows and apply policy
	var availableResources corev1.ResourceList
	if len(policies) == 0 {
		log.V(1).Info("No matching ResourceLeasingPolicy; using all remaining resources of physical node")
		availableResources = r.getBaseResources(physicalNode)
	} else {
		// Check if within time windows (requirement 4.9)
		if !r.isWithinTimeWindows(policies) {
			log.V(1).Info("Node outside time windows")
			return r.handleNodeDeletion(ctx, physicalNode.Name)
		}

		// Calculate available resources (requirement 3.3, 4.1, 4.2)
		availableResources = r.calculateAvailableResources(physicalNode, policies)
	}

	// Check if resources are sufficient for virtual node creation
	if r.hasInsufficientResources(availableResources) {
		log.Info("Insufficient resources for virtual node", "availableResources", availableResources)
		return r.handleNodeDeletion(ctx, physicalNode.Name)
	}

	// Create or update virtual node
	err = r.createOrUpdateVirtualNode(ctx, physicalNode, availableResources, policies)
	if err != nil {
		log.Error(err, "Failed to create or update virtual node")
		return ctrl.Result{}, err
	}

	log.V(1).Info("Successfully processed physical node",
		"availableResources", availableResources,
		"policiesCount", len(policies))

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
	// TODO: needs to drain node first, and wait for no pods
	logger.Info("Deleting virtual node", "virtualNode", virtualNodeName)
	err = r.VirtualClient.Delete(ctx, virtualNode)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to delete virtual node")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// getApplicablePolicies gets ResourceLeasingPolicies that apply to the given node
func (r *PhysicalNodeReconciler) getApplicablePolicies(ctx context.Context, node *corev1.Node, clusterSelector map[string]string) ([]cloudv1beta1.ResourceLeasingPolicy, error) {
	var policyList cloudv1beta1.ResourceLeasingPolicyList
	if err := r.VirtualClient.List(ctx, &policyList); err != nil {
		return nil, fmt.Errorf("failed to list ResourceLeasingPolicies: %w", err)
	}

	var applicablePolicies []cloudv1beta1.ResourceLeasingPolicy
	for _, policy := range policyList.Items {
		// Check if policy references our ClusterBinding
		if policy.Spec.Cluster == r.ClusterBindingName {
			// Check if node matches the intersection of policy and ClusterBinding nodeSelectors
			if r.nodeMatchesCombinedSelector(node, policy.Spec.NodeSelector, clusterSelector) {
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
// This implements requirement 4.1, 4.2 - dynamic resource calculation based on ResourceLeasingPolicy
func (r *PhysicalNodeReconciler) calculateAvailableResources(node *corev1.Node, policies []cloudv1beta1.ResourceLeasingPolicy) corev1.ResourceList {
	logger := r.Log.WithValues("node", node.Name, "policiesCount", len(policies))

	// Get node's total capacity and allocatable resources
	baseResources := r.getBaseResources(node)

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
				// This implements requirement 2.5 - handling multiple policies
				if existingLimit, exists := resourceLimits[resourceName]; exists {
					if limitQuantity.Cmp(existingLimit) < 0 {
						resourceLimits[resourceName] = limitQuantity
						logger.V(1).Info("Using more restrictive limit",
							"resource", resourceName,
							"newLimit", limitQuantity.String(),
							"previousLimit", existingLimit.String())
					}
				} else {
					resourceLimits[resourceName] = limitQuantity
				}
			}
		}

		// Apply the collected limits
		for resourceName, limitQuantity := range resourceLimits {
			// Get the current allocatable for this resource
			if baseQuantity, exists := baseResources[resourceName]; exists {
				// Take the minimum of policy limit and base resources
				if limitQuantity.Cmp(baseQuantity) < 0 {
					availableResources[resourceName] = limitQuantity
					logger.V(1).Info("Applied policy limit",
						"resource", resourceName,
						"limit", limitQuantity.String(),
						"baseQuantity", baseQuantity.String())
				} else {
					availableResources[resourceName] = baseQuantity
					logger.V(1).Info("Used base quantity (policy limit higher)",
						"resource", resourceName,
						"baseQuantity", baseQuantity.String(),
						"policyLimit", limitQuantity.String())
				}
			} else {
				// Resource not found in node, still apply the limit
				availableResources[resourceName] = limitQuantity
				logger.V(1).Info("Applied policy limit for unknown resource",
					"resource", resourceName,
					"limit", limitQuantity.String())
			}
		}

		// For resources not specified in any policy, apply conservative default
		// This implements requirement 4.10 - conservative approach for unspecified resources
		for resourceName, quantity := range baseResources {
			if _, hasLimit := resourceLimits[resourceName]; !hasLimit {
				// Conservative approach: use 80% of available capacity
				conservativeQuantity := quantity.DeepCopy()
				conservativeQuantity.Set(quantity.Value() * 8 / 10)
				availableResources[resourceName] = conservativeQuantity
				logger.V(1).Info("Applied conservative default",
					"resource", resourceName,
					"original", quantity.String(),
					"conservative", conservativeQuantity.String())
			}
		}
	} else {
		// If no specific limits from policies, use a conservative default (e.g., 80% of capacity)
		// This implements requirement 4.10 - conservative strategy when no policies
		for resourceName, quantity := range baseResources {
			conservativeQuantity := quantity.DeepCopy()
			conservativeQuantity.Set(quantity.Value() * 8 / 10)
			availableResources[resourceName] = conservativeQuantity
			logger.V(1).Info("Applied default conservative limit (no policies)",
				"resource", resourceName,
				"original", quantity.String(),
				"conservative", conservativeQuantity.String())
		}
	}

	// Ensure we always have CPU and memory resources
	if _, hasCPU := availableResources[corev1.ResourceCPU]; !hasCPU {
		if baseCPU, exists := baseResources[corev1.ResourceCPU]; exists {
			conservativeCPU := baseCPU.DeepCopy()
			conservativeCPU.Set(baseCPU.Value() * 8 / 10)
			availableResources[corev1.ResourceCPU] = conservativeCPU
		}
	}

	if _, hasMemory := availableResources[corev1.ResourceMemory]; !hasMemory {
		if baseMemory, exists := baseResources[corev1.ResourceMemory]; exists {
			conservativeMemory := baseMemory.DeepCopy()
			conservativeMemory.Set(baseMemory.Value() * 8 / 10)
			availableResources[corev1.ResourceMemory] = conservativeMemory
		}
	}

	logger.Info("Calculated available resources", "availableResources", availableResources)
	return availableResources
}

// getBaseResources returns the base resources from the physical node, preferring Allocatable over Capacity
func (r *PhysicalNodeReconciler) getBaseResources(node *corev1.Node) corev1.ResourceList {
	// Get node's total capacity and allocatable resources
	totalCapacity := node.Status.Capacity.DeepCopy()
	totalAllocatable := node.Status.Allocatable.DeepCopy()

	// Use allocatable as the base (more realistic than capacity)
	baseResources := totalAllocatable
	if len(baseResources) == 0 {
		baseResources = totalCapacity
	}
	return baseResources
}

// createOrUpdateVirtualNode creates or updates a virtual node based on physical node
// This implements requirement 4.1, 4.6, 4.8 - dynamic virtual node creation and resource management
func (r *PhysicalNodeReconciler) createOrUpdateVirtualNode(ctx context.Context, physicalNode *corev1.Node, resources corev1.ResourceList, policies []cloudv1beta1.ResourceLeasingPolicy) error {
	virtualNodeName := r.generateVirtualNodeName(physicalNode.Name)
	logger := r.Log.WithValues("virtualNode", virtualNodeName, "physicalNode", physicalNode.Name)

	// Check if virtual node already exists
	existingNode := &corev1.Node{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, existingNode)

	// Determine if node should be schedulable based on resources
	// This implements requirement 4.8 - marking nodes as unschedulable when resources insufficient
	schedulable := !r.hasInsufficientResources(resources)

	// Build taints - add unschedulable taint if resources are insufficient
	taints := r.filterTaints(physicalNode.Spec.Taints)
	if !schedulable {
		unschedulableTaint := corev1.Taint{
			Key:    "tapestry.io/insufficient-resources",
			Value:  "true",
			Effect: corev1.TaintEffectNoSchedule,
		}
		taints = append(taints, unschedulableTaint)
		logger.Info("Adding unschedulable taint due to insufficient resources", "resources", resources)
	}

	virtualNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        virtualNodeName,
			Labels:      r.buildVirtualNodeLabels(physicalNode),
			Annotations: r.buildVirtualNodeAnnotations(physicalNode, policies),
		},
		Spec: corev1.NodeSpec{
			// This implements requirement 4.4 - syncing taints from physical node
			Taints:        taints,
			Unschedulable: !schedulable, // Mark as unschedulable if resources insufficient
		},
		Status: corev1.NodeStatus{
			Capacity:    resources,
			Allocatable: resources,
			Conditions:  r.buildNodeConditions(physicalNode, schedulable),
			NodeInfo:    physicalNode.Status.NodeInfo,
			Phase:       corev1.NodeRunning,
		},
	}

	if err != nil {
		if errors.IsNotFound(err) {
			// Node doesn't exist, create it
			logger.Info("Creating virtual node",
				"resources", resources,
				"schedulable", schedulable,
				"policiesCount", len(policies))
			return r.VirtualClient.Create(ctx, virtualNode)
		}
		return fmt.Errorf("failed to get virtual node: %w", err)
	} else {
		// Node exists, update it
		// This implements requirement 4.6 - dynamically updating virtual node resources
		virtualNode.ResourceVersion = existingNode.ResourceVersion

		// Preserve any custom taints, labels, or annotations added by users (requirement 4.5)
		r.preserveUserCustomizations(existingNode, virtualNode)

		logger.V(1).Info("Updating virtual node",
			"resources", resources,
			"schedulable", schedulable,
			"resourceVersion", virtualNode.ResourceVersion)
		return r.VirtualClient.Update(ctx, virtualNode)
	}
}

// preserveUserCustomizations preserves user-added customizations on virtual nodes
// This implements requirement 4.5 - allowing user customizations without propagating to physical nodes
func (r *PhysicalNodeReconciler) preserveUserCustomizations(existing, new *corev1.Node) {
	// Preserve user-added labels (those not managed by Tapestry)
	for key, value := range existing.Labels {
		if !r.isTapestryManagedLabel(key) && !r.isPhysicalNodeLabel(key) {
			if new.Labels == nil {
				new.Labels = make(map[string]string)
			}
			new.Labels[key] = value
		}
	}

	// Preserve user-added annotations (those not managed by Tapestry)
	for key, value := range existing.Annotations {
		if !r.isTapestryManagedAnnotation(key) && !r.isPhysicalNodeAnnotation(key) {
			if new.Annotations == nil {
				new.Annotations = make(map[string]string)
			}
			new.Annotations[key] = value
		}
	}

	// Preserve user-added taints (those not from physical node or Tapestry)
	var preservedTaints []corev1.Taint
	for _, taint := range existing.Spec.Taints {
		if !r.isTapestryManagedTaint(taint.Key) && !r.isPhysicalNodeTaint(taint.Key) {
			preservedTaints = append(preservedTaints, taint)
		}
	}

	// Add preserved taints to new taints
	new.Spec.Taints = append(new.Spec.Taints, preservedTaints...)
}

// isTapestryManagedLabel checks if a label is managed by Tapestry
func (r *PhysicalNodeReconciler) isTapestryManagedLabel(key string) bool {
	tapestryPrefixes := []string{
		"tapestry.io/",
	}

	for _, prefix := range tapestryPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// isPhysicalNodeLabel checks if a label comes from the physical node
func (r *PhysicalNodeReconciler) isPhysicalNodeLabel(key string) bool {
	// These are labels that we copy from physical nodes
	physicalPrefixes := []string{
		"kubernetes.io/",
		"node.kubernetes.io/",
		"beta.kubernetes.io/",
		"node-role.kubernetes.io/",
	}

	for _, prefix := range physicalPrefixes {
		if strings.HasPrefix(key, prefix) {
			return false // We want to copy these, so they're not user customizations
		}
	}
	return false
}

// isTapestryManagedAnnotation checks if an annotation is managed by Tapestry
func (r *PhysicalNodeReconciler) isTapestryManagedAnnotation(key string) bool {
	return strings.HasPrefix(key, "tapestry.io/")
}

// isPhysicalNodeAnnotation checks if an annotation comes from the physical node
func (r *PhysicalNodeReconciler) isPhysicalNodeAnnotation(key string) bool {
	physicalPrefixes := []string{
		"node.alpha.kubernetes.io/",
		"volumes.kubernetes.io/",
		"csi.volume.kubernetes.io/",
	}

	for _, prefix := range physicalPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// isTapestryManagedTaint checks if a taint is managed by Tapestry
func (r *PhysicalNodeReconciler) isTapestryManagedTaint(key string) bool {
	return strings.HasPrefix(key, "tapestry.io/")
}

// isPhysicalNodeTaint checks if a taint comes from the physical node
func (r *PhysicalNodeReconciler) isPhysicalNodeTaint(key string) bool {
	physicalTaintPrefixes := []string{
		"node.kubernetes.io/",
		"node.cloudprovider.kubernetes.io/",
	}

	for _, prefix := range physicalTaintPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// generateVirtualNodeName generates a virtual node name based on physical node
func (r *PhysicalNodeReconciler) generateVirtualNodeName(physicalNodeName string) string {
	// Format: vnode-{cluster-id}-{node-name}
	clusterID := r.getClusterID()
	// Sanitize node name for Kubernetes naming requirements
	sanitizedNodeName := strings.ToLower(strings.ReplaceAll(physicalNodeName, "_", "-"))
	return fmt.Sprintf("%s-%s-%s", VirtualNodePrefix, clusterID, sanitizedNodeName)
}

// getClusterID returns a cluster identifier for the physical cluster
func (r *PhysicalNodeReconciler) getClusterID() string {
	// Use ClusterBinding name as cluster ID
	name := r.getClusterBindingName()
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

func (r *PhysicalNodeReconciler) getClusterBindingName() string {
	if r.ClusterBindingName != "" {
		return r.ClusterBindingName
	}
	if r.ClusterBinding != nil {
		return r.ClusterBinding.Name
	}
	return ""
}

// buildVirtualNodeLabels builds labels for virtual node
// This implements requirement 4.3, 4.4 - virtual node labeling and physical cluster mapping
func (r *PhysicalNodeReconciler) buildVirtualNodeLabels(physicalNode *corev1.Node) map[string]string {
	labels := make(map[string]string)

	// Copy relevant labels from physical node
	// This implements requirement 4.4 - syncing physical node labels to virtual node
	for key, value := range physicalNode.Labels {
		// Skip certain system labels that shouldn't be copied
		if !r.shouldSkipLabel(key) {
			labels[key] = value
		}
	}

	// Add Tapestry-specific labels
	// This implements requirement 4.3 - recording physical cluster ID and node name in labels
	labels[LabelPhysicalClusterID] = r.getClusterID()
	labels[LabelPhysicalNodeName] = physicalNode.Name
	labels[LabelManagedBy] = "tapestry"
	labels[LabelVirtualNodeType] = VirtualNodeTypeValue

	// Add additional metadata labels for better identification
	labels["tapestry.io/syncer-version"] = "v1beta1"
	labels["tapestry.io/cluster-binding"] = r.ClusterBindingName

	// Add node role information if available
	if role, exists := physicalNode.Labels["node-role.kubernetes.io/worker"]; exists {
		labels["tapestry.io/physical-node-role"] = "worker"
		_ = role // Acknowledge the value
	} else if _, exists := physicalNode.Labels["node-role.kubernetes.io/master"]; exists {
		labels["tapestry.io/physical-node-role"] = "master"
	} else if _, exists := physicalNode.Labels["node-role.kubernetes.io/control-plane"]; exists {
		labels["tapestry.io/physical-node-role"] = "control-plane"
	}

	r.Log.V(1).Info("Built virtual node labels",
		"physicalNode", physicalNode.Name,
		"labelCount", len(labels),
		"tapestryLabels", map[string]string{
			LabelPhysicalClusterID: labels[LabelPhysicalClusterID],
			LabelPhysicalNodeName:  labels[LabelPhysicalNodeName],
			LabelManagedBy:         labels[LabelManagedBy],
		})

	return labels
}

// buildVirtualNodeAnnotations builds annotations for virtual node
// This implements requirement 4.4 - virtual node annotation management
func (r *PhysicalNodeReconciler) buildVirtualNodeAnnotations(physicalNode *corev1.Node, policies []cloudv1beta1.ResourceLeasingPolicy) map[string]string {
	annotations := make(map[string]string)

	// Copy relevant annotations from physical node
	// This implements requirement 4.4 - syncing physical node annotations to virtual node
	for key, value := range physicalNode.Annotations {
		if !r.shouldSkipAnnotation(key) {
			annotations[key] = value
		}
	}

	// Add Tapestry-specific annotations
	annotations[AnnotationLastSyncTime] = time.Now().Format(time.RFC3339)

	// Add physical node metadata
	annotations["tapestry.io/physical-cluster-name"] = r.ClusterBindingName
	annotations["tapestry.io/physical-node-uid"] = string(physicalNode.UID)
	annotations["tapestry.io/sync-source"] = "bottom-up-syncer"

	// Add resource information
	if physicalNode.Status.Capacity != nil {
		annotations["tapestry.io/physical-node-capacity-cpu"] = physicalNode.Status.Capacity.Cpu().String()
		annotations["tapestry.io/physical-node-capacity-memory"] = physicalNode.Status.Capacity.Memory().String()
	}

	if physicalNode.Status.Allocatable != nil {
		annotations["tapestry.io/physical-node-allocatable-cpu"] = physicalNode.Status.Allocatable.Cpu().String()
		annotations["tapestry.io/physical-node-allocatable-memory"] = physicalNode.Status.Allocatable.Memory().String()
	}

	// Add policy information
	if len(policies) > 0 {
		var policyNames []string
		var policyDetails []string

		for _, policy := range policies {
			policyNames = append(policyNames, policy.Name)

			// Add detailed policy information
			policyDetail := policy.Name
			if len(policy.Spec.ResourceLimits) > 0 {
				var limits []string
				for _, limit := range policy.Spec.ResourceLimits {
					limits = append(limits, limit.Resource+"="+limit.Quantity.String())
				}
				policyDetail += "(" + strings.Join(limits, ",") + ")"
			}
			policyDetails = append(policyDetails, policyDetail)
		}

		// Set both annotations for compatibility
		annotations[AnnotationResourcePolicy] = strings.Join(policyNames, ",")
		annotations[AnnotationPoliciesApplied] = strings.Join(policyNames, ",")
		annotations["tapestry.io/policy-details"] = strings.Join(policyDetails, ";")

		// Add time window information if available
		var timeWindows []string
		for _, policy := range policies {
			for _, window := range policy.Spec.TimeWindows {
				timeWindow := window.Start + "-" + window.End
				if len(window.Days) > 0 {
					timeWindow += "(" + strings.Join(window.Days, ",") + ")"
				}
				timeWindows = append(timeWindows, timeWindow)
			}
		}
		if len(timeWindows) > 0 {
			annotations["tapestry.io/time-windows"] = strings.Join(timeWindows, ";")
		}
	} else {
		annotations["tapestry.io/policy-status"] = "no-applicable-policies"
	}

	r.Log.V(1).Info("Built virtual node annotations",
		"physicalNode", physicalNode.Name,
		"annotationCount", len(annotations),
		"policiesApplied", annotations[AnnotationPoliciesApplied])

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
func (r *PhysicalNodeReconciler) buildNodeConditions(physicalNode *corev1.Node, schedulable bool) []corev1.NodeCondition {
	now := metav1.Now()

	// Start with basic ready condition based on schedulability
	readyStatus := corev1.ConditionTrue
	readyReason := "TapestryNodeReady"
	readyMessage := "Virtual node is ready"

	if !schedulable {
		readyStatus = corev1.ConditionFalse
		readyReason = "TapestryInsufficientResources"
		readyMessage = "Virtual node has insufficient resources"
	}

	conditions := []corev1.NodeCondition{
		{
			Type:               corev1.NodeReady,
			Status:             readyStatus,
			LastHeartbeatTime:  now,
			LastTransitionTime: now,
			Reason:             readyReason,
			Message:            readyMessage,
		},
	}

	// Copy relevant conditions from physical node
	for _, condition := range physicalNode.Status.Conditions {
		switch condition.Type {
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			// Copy resource pressure conditions
			conditions = append(conditions, condition)
		case corev1.NodeNetworkUnavailable:
			// Copy network conditions
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

// isNodeReady checks if a physical node is ready for resource extraction
// This implements requirement 3.1 - monitoring physical cluster node status
func (r *PhysicalNodeReconciler) isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// getNodeConditionSummary returns a summary of node conditions for logging
func (r *PhysicalNodeReconciler) getNodeConditionSummary(node *corev1.Node) map[string]string {
	summary := make(map[string]string)
	for _, condition := range node.Status.Conditions {
		summary[string(condition.Type)] = string(condition.Status)
	}
	return summary
}

// hasInsufficientResources checks if available resources are too low for virtual node creation
// This implements requirement 4.8 - marking nodes as unschedulable when resources insufficient
func (r *PhysicalNodeReconciler) hasInsufficientResources(resources corev1.ResourceList) bool {
	// Check CPU
	if cpu, exists := resources[corev1.ResourceCPU]; exists {
		// Require at least 100m CPU
		minCPU := resource.MustParse("100m")
		if cpu.Cmp(minCPU) < 0 {
			return true
		}
	} else {
		return true // No CPU resource available
	}

	// Check Memory
	if memory, exists := resources[corev1.ResourceMemory]; exists {
		// Require at least 128Mi memory
		minMemory := resource.MustParse("128Mi")
		if memory.Cmp(minMemory) < 0 {
			return true
		}
	} else {
		return true // No memory resource available
	}

	return false
}

// SetupWithManager sets up the controller with the Manager
func (r *PhysicalNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
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
