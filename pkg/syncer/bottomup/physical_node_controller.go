package bottomup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	"github.com/TKEColocation/tapestry/pkg/utils"
)

const (
	// Label keys for virtual nodes (additional to those in bottomup_syncer.go)
	LabelVirtualNodeType = "type"
	// Virtual node type
	VirtualNodeTypeValue = "virtual-kubelet"

	NodeInstanceTypeLabelBeta = "beta.kubernetes.io/instance-type"
	NodeInstanceTypeLabel     = "node.kubernetes.io/instance-type"
	NodeInstanceTypeExternal  = "external"

	// Taint keys for node deletion
	TaintKeyVirtualNodeDeleting = "tapestry.io/vnode-deleting"

	// Annotation keys for deletion tracking
	AnnotationDeletionTaintTime = "tapestry.io/deletion-taint-time"
)

// ExpectedNodeMetadata represents the expected metadata for a virtual node
type ExpectedNodeMetadata struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Taints      []corev1.Taint    `json:"taints,omitempty"`
}

// PhysicalNodeReconciler reconciles Node objects from physical cluster
type PhysicalNodeReconciler struct {
	ClusterBindingName string
	// Cached binding for optimization
	ClusterBinding *cloudv1beta1.ClusterBinding

	PhysicalClient client.Client
	VirtualClient  client.Client
	KubeClient     kubernetes.Interface
	Scheme         *runtime.Scheme
	Log            logr.Logger

	workQueue workqueue.TypedRateLimitingInterface[reconcile.Request]

	// Lease controller management
	leaseControllers      map[string]*LeaseController // nodeName -> LeaseController
	leaseControllersMutex sync.RWMutex
}

//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=nodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

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
			return r.handleNodeDeletion(ctx, req.Name, true, 0)
		}
		log.Error(err, "Failed to get physical node")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if physicalNode.DeletionTimestamp != nil {
		return r.handleNodeDeletion(ctx, physicalNode.Name, false, 0)
	}

	// Process the node
	return r.processNode(ctx, physicalNode)
}

// processNode processes a physical node and creates/updates corresponding virtual node
func (r *PhysicalNodeReconciler) processNode(ctx context.Context, physicalNode *corev1.Node) (ctrl.Result, error) {
	log := r.Log.WithValues("physicalNode", physicalNode.Name)

	// Load latest ClusterBinding and check nodeSelector match first
	var clusterBinding cloudv1beta1.ClusterBinding
	if err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: r.getClusterBindingName()}, &clusterBinding); err != nil {
		log.Error(err, "Failed to get cluster binding")
		return ctrl.Result{}, err
	}

	// If node does not match ClusterBinding selector, ensure virtual node is deleted
	if !r.nodeMatchesSelector(physicalNode, clusterBinding.Spec.NodeSelector) {
		log.V(1).Info("Node does not match ClusterBinding selector; deleting virtual node")
		return r.handleNodeDeletion(ctx, physicalNode.Name, false, 0)
	}

	// Find applicable ResourceLeasingPolicies by node selector
	policies, err := r.getApplicablePolicies(ctx, physicalNode, clusterBinding.Spec.NodeSelector)
	if err != nil {
		log.Error(err, "Failed to get applicable policies")
		return ctrl.Result{}, err
	}

	var policy *cloudv1beta1.ResourceLeasingPolicy
	// If one or more policies match, use the earliest one
	if len(policies) > 0 {
		if len(policies) > 1 {
			// Sort by creation time and use the earliest
			sort.Slice(policies, func(i, j int) bool {
				// Older (earlier creation) comes first
				if policies[i].CreationTimestamp.Time.Equal(policies[j].CreationTimestamp.Time) {
					return policies[i].Name < policies[j].Name
				}
				return policies[i].CreationTimestamp.Time.Before(policies[j].CreationTimestamp.Time)
			})
			var ignored []string
			for idx := 1; idx < len(policies); idx++ {
				ignored = append(ignored, policies[idx].Name)
			}
			log.Info("Multiple ResourceLeasingPolicies matched. Using the earliest one and ignoring others.",
				"selected", policies[0].Name, "ignored", strings.Join(ignored, ","))
		} else {
			log.V(1).Info("Single ResourceLeasingPolicy matched", "selected", policies[0].Name)
		}
		// Use the first (earliest) policy
		policy = &policies[0]
	}

	// If no matching policy, use all remaining resources; otherwise validate time windows and apply policy
	var availableResources corev1.ResourceList
	if policy == nil {
		log.V(1).Info("No matching ResourceLeasingPolicy; using all remaining resources of physical node")
	} else {
		// Check if within time windows
		if !r.isWithinTimeWindows([]cloudv1beta1.ResourceLeasingPolicy{*policy}) {
			log.V(1).Info("Node outside time windows")

			// Use policy's ForceReclaim settings for time window deletion
			forceReclaim := policy.Spec.ForceReclaim
			gracefulPeriod := policy.Spec.GracefulReclaimPeriodSeconds
			if gracefulPeriod < 0 {
				gracefulPeriod = 0
			}

			return r.handleNodeDeletion(ctx, physicalNode.Name, forceReclaim, gracefulPeriod)
		}
	}

	// Calculate available resources (with or without policy constraints)
	availableResources, err = r.calculateAvailableResources(ctx, physicalNode, policy)
	if err != nil {
		log.Error(err, "Failed to calculate available resources")
		return ctrl.Result{}, err
	}

	// Create or update virtual node
	virtualNodeName := r.generateVirtualNodeName(physicalNode.Name)
	err = r.createOrUpdateVirtualNode(ctx, physicalNode, availableResources, policies)
	if err != nil {
		log.Error(err, "Failed to create or update virtual node")
		return ctrl.Result{}, err
	}

	// Start lease controller for the virtual node
	r.startLeaseController(virtualNodeName)

	log.V(1).Info("Successfully processed physical node", "availableResources", availableResources)

	return ctrl.Result{RequeueAfter: DefaultNodeSyncInterval}, nil
}

// addDeletionTaint adds the deletion taint to a virtual node and returns the updated node
func (r *PhysicalNodeReconciler) addDeletionTaint(ctx context.Context, virtualNode *corev1.Node) (*corev1.Node, error) {
	logger := r.Log.WithValues("virtualNode", virtualNode.Name)

	// Check if deletion taint already exists
	for _, taint := range virtualNode.Spec.Taints {
		if taint.Key == TaintKeyVirtualNodeDeleting {
			logger.V(1).Info("Deletion taint already exists")
			return virtualNode, nil
		}
	}

	// Add deletion taint
	deletionTaint := corev1.Taint{
		Key:    TaintKeyVirtualNodeDeleting,
		Value:  "true",
		Effect: corev1.TaintEffectNoSchedule,
	}

	virtualNode.Spec.Taints = append(virtualNode.Spec.Taints, deletionTaint)

	// Add annotation with taint addition time
	if virtualNode.Annotations == nil {
		virtualNode.Annotations = make(map[string]string)
	}
	virtualNode.Annotations[AnnotationDeletionTaintTime] = time.Now().Format(time.RFC3339)

	// Update the node
	if err := r.VirtualClient.Update(ctx, virtualNode); err != nil {
		return nil, fmt.Errorf("failed to add deletion taint: %w", err)
	}

	logger.Info("Added deletion taint to virtual node")

	return virtualNode, nil
}

// hasDeletionTaint checks if a virtual node has the deletion taint
func (r *PhysicalNodeReconciler) hasDeletionTaint(virtualNode *corev1.Node) bool {
	for _, taint := range virtualNode.Spec.Taints {
		if taint.Key == TaintKeyVirtualNodeDeleting {
			return true
		}
	}
	return false
}

// getDeletionTaintTime gets the time when deletion taint was added
func (r *PhysicalNodeReconciler) getDeletionTaintTime(virtualNode *corev1.Node) (*time.Time, error) {
	if virtualNode.Annotations == nil {
		return nil, fmt.Errorf("no annotations found")
	}

	taintTimeStr, exists := virtualNode.Annotations[AnnotationDeletionTaintTime]
	if !exists {
		return nil, fmt.Errorf("deletion taint time annotation not found")
	}

	taintTime, err := time.Parse(time.RFC3339, taintTimeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse deletion taint time: %w", err)
	}

	return &taintTime, nil
}

// checkPodsOnVirtualNode checks if there are any pods (Pending or Running) on the virtual node
func (r *PhysicalNodeReconciler) checkPodsOnVirtualNode(ctx context.Context, virtualNodeName string) (bool, error) {
	logger := r.Log.WithValues("virtualNode", virtualNodeName)

	// List all pods running on this virtual node
	podList := &corev1.PodList{}
	listOptions := []client.ListOption{
		client.MatchingFields{"spec.nodeName": virtualNodeName},
	}

	if err := r.VirtualClient.List(ctx, podList, listOptions...); err != nil {
		return false, fmt.Errorf("failed to list pods on virtual node %s: %w", virtualNodeName, err)
	}

	// Check for pods in Pending or Running state
	for _, pod := range podList.Items {
		if pod.Spec.NodeName == virtualNodeName && (pod.Status.Phase == corev1.PodPending || pod.Status.Phase == corev1.PodRunning) {
			logger.Info("Found active pod on virtual node", "pod", pod.Name, "namespace", pod.Namespace, "phase", pod.Status.Phase)
			return true, nil
		}
	}

	logger.V(1).Info("No active pods found on virtual node")
	return false, nil
}

// forceEvictPodsOnVirtualNode forcefully evicts all pods on the virtual node by deleting them
func (r *PhysicalNodeReconciler) forceEvictPodsOnVirtualNode(ctx context.Context, virtualNodeName string) error {
	logger := r.Log.WithValues("virtualNode", virtualNodeName)

	// List all pods on this virtual node
	podList := &corev1.PodList{}
	listOptions := []client.ListOption{
		client.MatchingFields{"spec.nodeName": virtualNodeName},
	}

	if err := r.VirtualClient.List(ctx, podList, listOptions...); err != nil {
		return fmt.Errorf("failed to list pods on virtual node %s: %w", virtualNodeName, err)
	}

	// Delete all pods
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != virtualNodeName {
			continue
		}

		logger.Info("Deleting pod to evict", "pod", pod.Name, "namespace", pod.Namespace, "phase", pod.Status.Phase)
		// Delete the pod immediately
		deleteOptions := &client.DeleteOptions{}
		if err := r.VirtualClient.Delete(ctx, &pod, deleteOptions); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Failed to delete pod", "pod", pod.Name, "namespace", pod.Namespace)
			// Continue with other pods even if one fails
		} else {
			logger.Info("Successfully deleted pod", "pod", pod.Name, "namespace", pod.Namespace)
		}
	}

	return nil
}

// handleNodeDeletion handles deletion of physical node
// forceReclaim: whether to force reclaim resources by evicting pods
// gracefulReclaimPeriodSeconds: graceful period before force eviction (0 means immediate)
func (r *PhysicalNodeReconciler) handleNodeDeletion(ctx context.Context, physicalNodeName string, forceReclaim bool, gracefulReclaimPeriodSeconds int32) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalNode", physicalNodeName)

	virtualNodeName := r.generateVirtualNodeName(physicalNodeName)

	virtualNode := &corev1.Node{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Virtual node doesn't exist, nothing to do
			logger.V(1).Info("Virtual node not found, nothing to delete")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual node for deletion")
		return ctrl.Result{}, err
	}

	// Step 1: Add deletion taint to prevent new pod scheduling
	if !r.hasDeletionTaint(virtualNode) {
		logger.Info("Adding deletion taint to virtual node")
		updatedVirtualNode, err := r.addDeletionTaint(ctx, virtualNode)
		if err != nil {
			logger.Error(err, "Failed to add deletion taint")
			return ctrl.Result{}, err
		}
		// Use the updated virtual node for subsequent operations
		virtualNode = updatedVirtualNode
		logger.Info("Deletion taint added, continuing with deletion process")
	}

	// Step 2: Handle ForceReclaim based on input parameters
	logger.Info("Processing deletion", "forceReclaim", forceReclaim, "gracefulPeriod", gracefulReclaimPeriodSeconds)

	if forceReclaim {
		if gracefulReclaimPeriodSeconds == 0 {
			// Immediate force eviction (physical node deleted scenario)
			logger.Info("Immediate force eviction requested, evicting all pods")
			if err := r.forceEvictPodsOnVirtualNode(ctx, virtualNodeName); err != nil {
				logger.Error(err, "Failed to force evict pods")
				return ctrl.Result{}, err
			}
			// Continue to final deletion step
		} else {
			// Graceful force eviction (policy time window scenario)
			logger.Info("Graceful force eviction requested, checking graceful period", "gracefulPeriod", gracefulReclaimPeriodSeconds)

			// Check if graceful reclaim period has passed
			taintTime, err := r.getDeletionTaintTime(virtualNode)
			if err != nil {
				logger.Error(err, "Failed to get deletion taint time")
				return ctrl.Result{}, err
			}

			gracefulDuration := time.Duration(gracefulReclaimPeriodSeconds) * time.Second
			if time.Since(*taintTime) < gracefulDuration {
				remainingTime := gracefulDuration - time.Since(*taintTime)
				logger.Info("Graceful reclaim period not yet passed, waiting", "remainingTime", remainingTime)
				return ctrl.Result{RequeueAfter: remainingTime}, nil
			}
			logger.Info("Graceful reclaim period passed, force evicting pods")
			if err := r.forceEvictPodsOnVirtualNode(ctx, virtualNodeName); err != nil {
				logger.Error(err, "Failed to force evict pods")
				return ctrl.Result{}, err
			}
			// continue to next step
		}
	} else {
		logger.Info("ForceReclaim disabled, not force evicting pods")
		// continue to next step
	}

	// Step 3: Check if there are still pods on the virtual node
	hasPods, err := r.checkPodsOnVirtualNode(ctx, virtualNodeName)
	if err != nil {
		logger.Error(err, "Failed to check pods on virtual node")
		return ctrl.Result{}, err
	}

	if hasPods {
		logger.Info("Virtual node still has active pods, cannot delete node")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Step 4: All conditions met, proceed with virtual node deletion
	logger.Info("No active pods found, proceeding with virtual node deletion")

	// Stop lease controller for the virtual node
	r.stopLeaseController(virtualNodeName)

	// Delete virtual node
	logger.Info("Deleting virtual node", "virtualNode", virtualNodeName)
	err = r.VirtualClient.Delete(ctx, virtualNode)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to delete virtual node")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully deleted virtual node", "virtualNode", virtualNodeName)
	return ctrl.Result{}, nil
}

// getApplicablePolicies gets ResourceLeasingPolicies that apply to the given node
func (r *PhysicalNodeReconciler) getApplicablePolicies(ctx context.Context, node *corev1.Node, clusterSelector *corev1.NodeSelector) ([]cloudv1beta1.ResourceLeasingPolicy, error) {
	var policyList cloudv1beta1.ResourceLeasingPolicyList
	if err := r.VirtualClient.List(ctx, &policyList); err != nil {
		return nil, fmt.Errorf("failed to list ResourceLeasingPolicies: %w", err)
	}

	var applicablePolicies []cloudv1beta1.ResourceLeasingPolicy
	for _, policy := range policyList.Items {
		// Check if policy references our ClusterBinding
		if policy.DeletionTimestamp == nil && policy.Spec.Cluster == r.ClusterBindingName {
			// Check if node matches the intersection of policy and ClusterBinding nodeSelectors
			if r.nodeMatchesCombinedSelector(node, policy.Spec.NodeSelector, clusterSelector) {
				applicablePolicies = append(applicablePolicies, policy)
			}
		}
	}

	return applicablePolicies, nil
}

// nodeMatchesSelector checks if node matches the given node selector using shared utilities
func (r *PhysicalNodeReconciler) nodeMatchesSelector(node *corev1.Node, nodeSelector *corev1.NodeSelector) bool {
	return utils.MatchesSelector(node, nodeSelector)
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

// calculateNodeResourceUsage calculates the total resource usage of all pods running on the node

func (r *PhysicalNodeReconciler) calculateNodeResourceUsage(ctx context.Context, nodeName string) (corev1.ResourceList, error) {
	// List all pods running on this node
	podList := &corev1.PodList{}
	listOptions := []client.ListOption{
		client.MatchingFields{"spec.nodeName": nodeName},
	}

	if err := r.PhysicalClient.List(ctx, podList, listOptions...); err != nil {
		return nil, fmt.Errorf("failed to list pods on node %s: %w", nodeName, err)
	}

	totalUsage := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("0"),
		corev1.ResourceMemory: resource.MustParse("0"),
	}

	for idx := range podList.Items {
		pod := &podList.Items[idx]
		// Skip pods that are not running or are in terminal states
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}

		// Calculate pod resource requests (not limits, as requests are what's actually reserved)
		podUsage := r.calculatePodResourceRequests(pod)

		// Add pod usage to total
		for resourceName, quantity := range podUsage {
			if existing, exists := totalUsage[resourceName]; exists {
				existing.Add(quantity)
				totalUsage[resourceName] = existing
			} else {
				totalUsage[resourceName] = quantity
			}
		}
	}

	r.Log.V(1).Info("Calculated node resource usage", "node", nodeName, "totalUsage", totalUsage)

	return totalUsage, nil
}

// calculatePodResourceRequests calculates the total resource requests for a pod
func (r *PhysicalNodeReconciler) calculatePodResourceRequests(pod *corev1.Pod) corev1.ResourceList {
	totalRequests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("0"),
		corev1.ResourceMemory: resource.MustParse("0"),
	}

	// Sum up requests from init containers and regular containers
	allContainers := []corev1.Container{}
	allContainers = append(allContainers, pod.Spec.InitContainers...)
	allContainers = append(allContainers, pod.Spec.Containers...)

	for _, container := range allContainers {
		for resourceName, quantity := range container.Resources.Requests {
			if existing, exists := totalRequests[resourceName]; exists {
				existing.Add(quantity)
				totalRequests[resourceName] = existing
			} else {
				totalRequests[resourceName] = quantity
			}
		}
	}

	// Handle ephemeral containers separately (they have a different type)
	for _, ephemeralContainer := range pod.Spec.EphemeralContainers {
		for resourceName, quantity := range ephemeralContainer.Resources.Requests {
			if existing, exists := totalRequests[resourceName]; exists {
				existing.Add(quantity)
				totalRequests[resourceName] = existing
			} else {
				totalRequests[resourceName] = quantity
			}
		}
	}

	return totalRequests
}

func GetPolicyName(policy *cloudv1beta1.ResourceLeasingPolicy) string {
	if policy == nil {
		return ""
	}
	return policy.Name
}

// calculateAvailableResources calculates available resources based on policies and actual node usage
// 实际可用资源 = min(物理节点可用资源 - 已占用资源, policy限制)
func (r *PhysicalNodeReconciler) calculateAvailableResources(ctx context.Context, node *corev1.Node, policy *cloudv1beta1.ResourceLeasingPolicy) (corev1.ResourceList, error) {
	logger := r.Log.WithValues("node", node.Name, "policyName", GetPolicyName(policy))

	// Get node's allocatable resources (what's available for scheduling)
	allocatableResources := r.getBaseResources(node)

	// Calculate current resource usage by pods on this node
	currentUsage, err := r.calculateNodeResourceUsage(ctx, node.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate node resource usage for node %s: %w", node.Name, err)
	}

	// Calculate actual available resources = allocatable - current usage
	actualAvailable := corev1.ResourceList{}
	for resourceName, allocatable := range allocatableResources {
		used := resource.MustParse("0")
		if usedQuantity, exists := currentUsage[resourceName]; exists {
			used = usedQuantity
		}

		// Calculate remaining = allocatable - used
		remaining := allocatable.DeepCopy()
		remaining.Sub(used)

		// Ensure we don't go negative
		if remaining.Sign() < 0 {
			remaining = resource.MustParse("0")
		}

		actualAvailable[resourceName] = remaining
	}

	logger.V(1).Info("Calculated actual available resources", "actualAvailable", actualAvailable)

	availableResources := actualAvailable

	// Apply resource limits from policies if any exist
	if policy != nil {
		availableResources = r.applyResourceLimits(availableResources, policy.Spec.ResourceLimits, logger)
	}

	logger.V(1).Info("Calculated available resources", "availableResources", availableResources)
	return availableResources, nil
}

// applyResourceLimits applies resource limits from policy, supporting both quantity and percentage limits
func (r *PhysicalNodeReconciler) applyResourceLimits(availableResources corev1.ResourceList, resourceLimits []cloudv1beta1.ResourceLimit, logger logr.Logger) corev1.ResourceList {
	result := availableResources.DeepCopy()

	for _, limit := range resourceLimits {
		resourceName := corev1.ResourceName(limit.Resource)

		// Get current available resource for this type
		currentAvailable, exists := result[resourceName]
		if !exists {
			// If resource doesn't exist in available resources, skip or set to zero
			continue
		}

		// Calculate effective limit based on quantity and/or percentage
		effectiveLimit := r.calculateEffectiveLimit(limit, currentAvailable, logger)

		// Apply the more restrictive limit
		if effectiveLimit != nil && effectiveLimit.Cmp(currentAvailable) < 0 {
			result[resourceName] = *effectiveLimit
			logger.V(2).Info("Applied resource limit",
				"resource", limit.Resource,
				"originalAvailable", currentAvailable.String(),
				"effectiveLimit", effectiveLimit.String())
		}
	}

	return result
}

// calculateEffectiveLimit calculates the effective resource limit considering both quantity and percentage
func (r *PhysicalNodeReconciler) calculateEffectiveLimit(limit cloudv1beta1.ResourceLimit, currentAvailable resource.Quantity, logger logr.Logger) *resource.Quantity {
	var limits []resource.Quantity

	// Add quantity limit if specified
	if limit.Quantity != nil && !limit.Quantity.IsZero() {
		limits = append(limits, *limit.Quantity)
		logger.V(2).Info("Added quantity limit", "resource", limit.Resource, "quantity", limit.Quantity.String())
	}

	// Add percentage limit if specified
	if limit.Percent != nil && *limit.Percent > 0 {
		percentLimit := r.calculatePercentageLimit(currentAvailable, *limit.Percent)
		limits = append(limits, percentLimit)
		logger.V(2).Info("Added percentage limit", "resource", limit.Resource, "percent", *limit.Percent, "calculated", percentLimit.String())
	}

	// Return the minimum (most restrictive) limit
	if len(limits) == 0 {
		return nil
	}

	minLimit := limits[0]
	for i := 1; i < len(limits); i++ {
		if limits[i].Cmp(minLimit) < 0 {
			minLimit = limits[i]
		}
	}

	return &minLimit
}

// calculatePercentageLimit calculates resource limit based on percentage of current available resources
func (r *PhysicalNodeReconciler) calculatePercentageLimit(currentAvailable resource.Quantity, percent int32) resource.Quantity {
	if percent <= 0 || percent > 100 {
		return resource.MustParse("0")
	}

	// For CPU resources (measured in millicores)
	if currentAvailable.Format == resource.DecimalSI {
		// Convert to millicores for calculation
		milliValue := currentAvailable.MilliValue()
		percentValue := milliValue * int64(percent) / 100
		return *resource.NewMilliQuantity(percentValue, resource.DecimalSI)
	}

	// For memory and other binary resources
	if currentAvailable.Format == resource.BinarySI {
		// Use binary calculation for memory
		value := currentAvailable.Value()
		percentValue := value * int64(percent) / 100
		return *resource.NewQuantity(percentValue, resource.BinarySI)
	}

	// Fallback for other resource types
	value := currentAvailable.Value()
	percentValue := value * int64(percent) / 100
	return *resource.NewQuantity(percentValue, currentAvailable.Format)
}

// initLeaseControllers initializes the lease controllers map
func (r *PhysicalNodeReconciler) initLeaseControllers() {
	r.leaseControllersMutex.Lock()
	defer r.leaseControllersMutex.Unlock()

	if r.leaseControllers == nil {
		r.leaseControllers = make(map[string]*LeaseController)
	}
}

// startLeaseController starts a lease controller for a virtual node
func (r *PhysicalNodeReconciler) startLeaseController(virtualNodeName string) {
	r.leaseControllersMutex.Lock()
	defer r.leaseControllersMutex.Unlock()

	// Initialize lease controllers map if needed
	if r.leaseControllers == nil {
		r.leaseControllers = make(map[string]*LeaseController)
	}

	// Check if lease controller already exists
	if _, exists := r.leaseControllers[virtualNodeName]; exists {
		r.Log.V(1).Info("Lease controller already exists for virtual node", "virtualNode", virtualNodeName)
		return
	}

	// Create and start new lease controller
	leaseController := NewLeaseController(virtualNodeName, r.KubeClient, r.VirtualClient, r.Log)
	r.leaseControllers[virtualNodeName] = leaseController
	leaseController.Start()

	r.Log.Info("Started lease controller for virtual node", "virtualNode", virtualNodeName)
}

// stopLeaseController stops and removes a lease controller for a virtual node
func (r *PhysicalNodeReconciler) stopLeaseController(virtualNodeName string) {
	r.leaseControllersMutex.Lock()
	defer r.leaseControllersMutex.Unlock()

	// Initialize lease controllers map if needed
	if r.leaseControllers == nil {
		r.leaseControllers = make(map[string]*LeaseController)
	}

	leaseController, exists := r.leaseControllers[virtualNodeName]
	if !exists {
		r.Log.V(1).Info("Lease controller not found for virtual node", "virtualNode", virtualNodeName)
		return
	}

	// Stop the lease controller
	leaseController.Stop()

	// Remove from map
	delete(r.leaseControllers, virtualNodeName)

	r.Log.Info("Stopped and removed lease controller for virtual node", "virtualNode", virtualNodeName)
}

// Stop stops the PhysicalNodeReconciler and cleans up all resources
func (r *PhysicalNodeReconciler) Stop() {
	r.Log.Info("Stopping PhysicalNodeReconciler")

	// Stop all lease controllers
	r.stopAllLeaseControllers()

	r.Log.Info("PhysicalNodeReconciler stopped")
}

// stopAllLeaseControllers stops all lease controllers
func (r *PhysicalNodeReconciler) stopAllLeaseControllers() {
	r.leaseControllersMutex.Lock()
	defer r.leaseControllersMutex.Unlock()

	// Initialize lease controllers map if needed
	if r.leaseControllers == nil {
		r.leaseControllers = make(map[string]*LeaseController)
		return
	}

	for virtualNodeName, leaseController := range r.leaseControllers {
		leaseController.Stop()
		r.Log.Info("Stopped lease controller", "virtualNode", virtualNodeName)
	}

	// Clear the map
	r.leaseControllers = make(map[string]*LeaseController)
	r.Log.Info("Stopped all lease controllers")
}

// getLeaseControllerStatus returns the status of lease controllers
func (r *PhysicalNodeReconciler) getLeaseControllerStatus() map[string]bool {
	r.leaseControllersMutex.RLock()
	defer r.leaseControllersMutex.RUnlock()

	status := make(map[string]bool)

	// Initialize lease controllers map if needed
	if r.leaseControllers == nil {
		return status
	}

	for virtualNodeName, leaseController := range r.leaseControllers {
		status[virtualNodeName] = leaseController.IsRunning()
	}

	return status
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
func (r *PhysicalNodeReconciler) createOrUpdateVirtualNode(ctx context.Context, physicalNode *corev1.Node, resources corev1.ResourceList, policies []cloudv1beta1.ResourceLeasingPolicy) error {
	virtualNodeName := r.generateVirtualNodeName(physicalNode.Name)
	logger := r.Log.WithValues("virtualNode", virtualNodeName, "physicalNode", physicalNode.Name)

	virtualNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        virtualNodeName,
			Labels:      r.buildVirtualNodeLabels(virtualNodeName, physicalNode),
			Annotations: r.buildVirtualNodeAnnotations(physicalNode, policies),
		},
		Spec: corev1.NodeSpec{
			Taints: physicalNode.Spec.Taints,
		},
		Status: corev1.NodeStatus{
			Capacity:    resources,
			Allocatable: resources,
			Conditions:  physicalNode.Status.Conditions,
			NodeInfo:    physicalNode.Status.NodeInfo,
			Phase:       physicalNode.Status.Phase,
		},
	}

	// Save expected metadata for user customization preservation
	expectedMetadata := &ExpectedNodeMetadata{
		Labels:      r.copyMap(virtualNode.Labels),
		Annotations: r.copyMap(virtualNode.Annotations),
		Taints:      r.copyTaints(virtualNode.Spec.Taints),
	}
	err := r.saveExpectedMetadataToAnnotation(virtualNode, expectedMetadata)
	if err != nil {
		return fmt.Errorf("failed to save expected metadata: %w", err)
	}

	// Check if virtual node already exists
	existingNode := &corev1.Node{}
	err = r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, existingNode)

	if err != nil {
		if errors.IsNotFound(err) {
			// Node doesn't exist, create it
			logger.Info("Creating virtual node",
				"resources", resources,
				"policiesCount", len(policies))
			return r.VirtualClient.Create(ctx, virtualNode)
		}
		return fmt.Errorf("failed to get virtual node: %w", err)
	} else {
		// Node exists, update it
		err = r.preserveUserCustomizations(existingNode, virtualNode)
		if err != nil {
			return fmt.Errorf("failed to preserve user customizations: %w", err)
		}

		newNode := existingNode.DeepCopy()
		newNode.Status.Conditions = virtualNode.Status.Conditions
		newNode.Status.Phase = virtualNode.Status.Phase
		newNode.Status.Allocatable = virtualNode.Status.Allocatable
		newNode.Status.Capacity = virtualNode.Status.Capacity
		newNode.Status.NodeInfo = virtualNode.Status.NodeInfo
		newNode.Spec.Taints = virtualNode.Spec.Taints
		newNode.Labels = virtualNode.Labels
		newNode.Annotations = virtualNode.Annotations

		if !reflect.DeepEqual(existingNode, newNode) {
			logger.V(1).Info("Updating virtual node",
				"resources", resources,
				"resourceVersion", newNode.ResourceVersion,
				"conditions", newNode.Status.Conditions)
			err = r.VirtualClient.Status().Update(ctx, newNode)
			if err != nil {
				return fmt.Errorf("failed to update virtual node status: %w", err)
			}
		}

		if !reflect.DeepEqual(existingNode.Spec.Taints, virtualNode.Spec.Taints) {
			patchNode := newNode.DeepCopy()
			patchNode.Spec.Taints = virtualNode.Spec.Taints
			logger.Info("taints changed, patching virtual node", "taints", patchNode.Spec.Taints)
			err = r.VirtualClient.Patch(ctx, patchNode, client.MergeFrom(newNode))
			if err != nil {
				return fmt.Errorf("failed to patch virtual node: %w", err)
			}
		}

		return nil
	}
}

// preserveUserCustomizations preserves user-added customizations on virtual nodes
// It uses intelligent diff-based updates to only modify what has changed
func (r *PhysicalNodeReconciler) preserveUserCustomizations(existing, new *corev1.Node) error {
	logger := r.Log.WithValues("virtualNode", new.Name)

	// Get the previously expected metadata from annotation
	previousExpected, err := r.getExpectedMetadataFromAnnotation(existing)
	if err != nil {
		return fmt.Errorf("failed to get previous expected metadata: %w", err)
	}

	// Create current expected metadata
	currentExpected := &ExpectedNodeMetadata{
		Labels:      r.copyMap(new.Labels),
		Annotations: r.copyMap(new.Annotations),
		Taints:      r.copyTaints(new.Spec.Taints),
	}

	// Save the current expected metadata for next time
	err = r.saveExpectedMetadataToAnnotation(new, currentExpected)
	if err != nil {
		return fmt.Errorf("failed to save current expected metadata: %w", err)
	}

	// Preserve user customizations by merging with intelligent diff
	r.mergeWithUserCustomizations(existing, new, previousExpected, currentExpected)

	logger.V(1).Info("User customizations preserved and expected metadata updated")
	return nil
}

// getExpectedMetadataFromAnnotation retrieves the expected metadata from the annotation
func (r *PhysicalNodeReconciler) getExpectedMetadataFromAnnotation(node *corev1.Node) (*ExpectedNodeMetadata, error) {
	if node.Annotations == nil {
		return &ExpectedNodeMetadata{}, nil
	}

	encodedMetadata, exists := node.Annotations[cloudv1beta1.AnnotationExpectedMetadata]
	if !exists {
		return &ExpectedNodeMetadata{}, nil
	}

	// Base64 decode the metadata
	metadataBytes, err := base64.StdEncoding.DecodeString(encodedMetadata)
	if err != nil {
		return nil, fmt.Errorf("failed to base64 decode expected metadata: %w", err)
	}

	// JSON unmarshal the decoded data
	var metadata ExpectedNodeMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal expected metadata: %w", err)
	}

	return &metadata, nil
}

// saveExpectedMetadataToAnnotation saves the expected metadata to the annotation
func (r *PhysicalNodeReconciler) saveExpectedMetadataToAnnotation(node *corev1.Node, metadata *ExpectedNodeMetadata) error {
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	// JSON marshal the metadata
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal expected metadata: %w", err)
	}

	// Base64 encode the JSON data
	encodedMetadata := base64.StdEncoding.EncodeToString(metadataBytes)
	node.Annotations[cloudv1beta1.AnnotationExpectedMetadata] = encodedMetadata
	return nil
}

// mergeWithUserCustomizations intelligently merges user customizations with expected changes
func (r *PhysicalNodeReconciler) mergeWithUserCustomizations(existing, new *corev1.Node, previousExpected, currentExpected *ExpectedNodeMetadata) {
	// Merge labels
	r.mergeLabels(existing, new, previousExpected.Labels, currentExpected.Labels)

	// Merge annotations
	r.mergeAnnotations(existing, new, previousExpected.Annotations, currentExpected.Annotations)

	// Merge taints
	r.mergeTaints(existing, new, previousExpected.Taints, currentExpected.Taints)
}

// mergeLabels merges labels while preserving user customizations
func (r *PhysicalNodeReconciler) mergeLabels(existing, new *corev1.Node, previousExpected, currentExpected map[string]string) {
	if new.Labels == nil {
		new.Labels = make(map[string]string)
	}

	// Start with current expected labels
	for key, value := range currentExpected {
		new.Labels[key] = value
	}

	// Identify user-added labels (those in existing but not in previous expected)
	for key, value := range existing.Labels {
		// 如果currentExpected中存在，则不覆盖
		if _, ok := currentExpected[key]; ok {
			continue
		}
		// 如果不在previousExpected中，则认为是用户添加的
		if _, ok := previousExpected[key]; !ok {
			r.Log.Info("user added label", "key", key, "value", value, "node", new.Name)
			new.Labels[key] = value
		}
	}
}

// mergeAnnotations merges annotations while preserving user customizations
func (r *PhysicalNodeReconciler) mergeAnnotations(existing, new *corev1.Node, previousExpected, currentExpected map[string]string) {
	if new.Annotations == nil {
		new.Annotations = make(map[string]string)
	}

	// Start with current expected annotations
	for key, value := range currentExpected {
		new.Annotations[key] = value
	}

	// Identify user-added annotations
	for key, value := range existing.Annotations {
		// 如果currentExpected中存在，则不覆盖
		if _, ok := currentExpected[key]; ok {
			continue
		}
		if key == AnnotationDeletionTaintTime {
			r.Log.Info("skipping deletion taint time", "key", key, "value", value, "node", new.Name)
			continue
		}
		// 如果不在previousExpected中，则认为是用户添加的
		if _, ok := previousExpected[key]; !ok {
			r.Log.Info("user added annotation", "key", key, "value", value, "node", new.Name)
			new.Annotations[key] = value
		}
	}
}

// mergeTaints merges taints while preserving user customizations
func (r *PhysicalNodeReconciler) mergeTaints(existing, new *corev1.Node, previousExpected, currentExpected []corev1.Taint) {
	// Start with current expected taints
	new.Spec.Taints = r.copyTaints(currentExpected)

	// Create maps for easier lookup
	previousExpectedMap := r.taintsToMap(previousExpected)
	currentExpectedMap := r.taintsToMap(currentExpected)

	// Identify user-added taints
	for _, taint := range existing.Spec.Taints {
		// 如果currentExpected中存在，则不覆盖
		if _, ok := currentExpectedMap[taint.Key]; ok {
			continue
		}
		if taint.Key == TaintKeyVirtualNodeDeleting {
			r.Log.Info("skipping deletion taint", "taint", taint, "node", new.Name)
			continue
		}
		// 如果不在previousExpected中，则认为是用户添加的
		if _, ok := previousExpectedMap[taint.Key]; !ok {
			r.Log.Info("user added taint", "taint", taint, "node", new.Name)
			new.Spec.Taints = append(new.Spec.Taints, taint)
		}
	}
}

// Helper methods for taint operations
func (r *PhysicalNodeReconciler) taintsToMap(taints []corev1.Taint) map[string]corev1.Taint {
	result := make(map[string]corev1.Taint)
	for _, taint := range taints {
		key := r.taintToKey(taint)
		result[key] = taint
	}
	return result
}

func (r *PhysicalNodeReconciler) taintToKey(taint corev1.Taint) string {
	return taint.Key
}

// Helper methods for copying data structures
func (r *PhysicalNodeReconciler) copyMap(original map[string]string) map[string]string {
	if original == nil {
		return nil
	}
	result := make(map[string]string)
	for k, v := range original {
		result[k] = v
	}
	return result
}

func (r *PhysicalNodeReconciler) copyTaints(original []corev1.Taint) []corev1.Taint {
	if original == nil {
		return nil
	}
	result := make([]corev1.Taint, len(original))
	copy(result, original)
	return result
}

// generateVirtualNodeName generates a virtual node name based on physical node
func (r *PhysicalNodeReconciler) generateVirtualNodeName(physicalNodeName string) string {
	// Format: vnode-{cluster-id}-{node-name}
	clusterID := r.getClusterID()
	return fmt.Sprintf("%s-%s-%s", VirtualNodePrefix, clusterID, physicalNodeName)
}

// getClusterID returns a cluster identifier for the physical cluster
func (r *PhysicalNodeReconciler) getClusterID() string {
	// Use ClusterBinding.Spec.ClusterID as cluster ID
	// This field is immutable and specifically designed for this purpose
	if r.ClusterBinding != nil && r.ClusterBinding.Spec.ClusterID != "" {
		return r.ClusterBinding.Spec.ClusterID
	}

	// Fallback to ClusterBinding name for backward compatibility
	// This should not happen in normal cases as clusterID is required
	name := r.getClusterBindingName()
	if name != "" {
		r.Log.Info("ClusterBinding.Spec.ClusterID is empty, falling back to name",
			"clusterBinding", name)
		return name
	}

	return "unknown-cluster"
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
func (r *PhysicalNodeReconciler) buildVirtualNodeLabels(nodeName string, physicalNode *corev1.Node) map[string]string {
	labels := physicalNode.Labels

	if labels == nil {
		labels = make(map[string]string)
	}

	// Add Tapestry-specific labels
	labels[LabelClusterBinding] = r.ClusterBindingName
	labels[LabelPhysicalClusterID] = r.getClusterID()
	labels[LabelPhysicalNodeName] = physicalNode.Name
	labels[cloudv1beta1.LabelManagedBy] = "tapestry"
	labels[LabelVirtualNodeType] = VirtualNodeTypeValue
	labels[NodeInstanceTypeLabel] = NodeInstanceTypeExternal
	labels[NodeInstanceTypeLabelBeta] = NodeInstanceTypeExternal
	labels["kubernetes.io/hostname"] = nodeName
	labels["tapestry.io/cluster-binding"] = r.ClusterBindingName

	r.Log.V(1).Info("Built virtual node labels", "physicalNode", physicalNode.Name, "labelCount", len(labels))

	return labels
}

// buildVirtualNodeAnnotations builds annotations for virtual node
func (r *PhysicalNodeReconciler) buildVirtualNodeAnnotations(physicalNode *corev1.Node, policies []cloudv1beta1.ResourceLeasingPolicy) map[string]string {
	annotations := physicalNode.Annotations

	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Add Tapestry-specific annotations
	annotations[cloudv1beta1.AnnotationLastSyncTime] = time.Now().Format(time.RFC3339)

	// Add physical node metadata
	annotations["tapestry.io/physical-cluster-name"] = r.ClusterBindingName
	annotations[LabelPhysicalClusterID] = r.getClusterID()
	annotations["tapestry.io/physical-node-uid"] = string(physicalNode.UID)

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

		annotations[cloudv1beta1.AnnotationPoliciesApplied] = strings.Join(policyNames, ",")
		annotations["tapestry.io/policy-details"] = strings.Join(policyDetails, ";")
	} else {
		annotations[cloudv1beta1.AnnotationPoliciesApplied] = ""
	}

	r.Log.V(1).Info("Built virtual node annotations", "physicalNode", physicalNode.Name, "annotationCount", len(annotations))

	return annotations
}

// nodeMatchesCombinedSelector checks if node matches the intersection of policy and ClusterBinding nodeSelectors
func (r *PhysicalNodeReconciler) nodeMatchesCombinedSelector(node *corev1.Node, policySelector, clusterSelector *corev1.NodeSelector) bool {
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
func (r *PhysicalNodeReconciler) SetupWithManager(physicalManager, virtualManager ctrl.Manager) error {
	// Initialize lease controllers map
	r.initLeaseControllers()

	// Add index for pods by node name to efficiently query pods running on a specific node
	if err := physicalManager.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
		pod := rawObj.(*corev1.Pod)
		if pod.Spec.NodeName == "" {
			return nil
		}
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return fmt.Errorf("failed to setup pod index by node name for physical manager: %w", err)
	}

	if err := virtualManager.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
		pod := rawObj.(*corev1.Pod)
		if pod.Spec.NodeName == "" {
			return nil
		}
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return fmt.Errorf("failed to setup pod index by node name for virtual manager: %w", err)
	}

	// Generate unique controller name using cluster binding name
	uniqueControllerName := fmt.Sprintf("node-%s", r.ClusterBindingName)

	return ctrl.NewControllerManagedBy(physicalManager).
		For(&corev1.Node{}).
		Named(uniqueControllerName).
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
		Watches(
			&corev1.Pod{},
			handler.Funcs{
				CreateFunc: func(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					pod := event.Object.(*corev1.Pod)
					if pod == nil || pod.Spec.NodeName == "" {
						return
					}
					r.Log.Info("pod created", "namespace", pod.Namespace, "name", pod.Name, "node", pod.Spec.NodeName)
					q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: pod.Spec.NodeName}})
				},
				UpdateFunc: func(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					opod := event.ObjectOld.(*corev1.Pod)
					npod := event.ObjectNew.(*corev1.Pod)
					if opod == nil || npod == nil || npod.Spec.NodeName == "" {
						return
					}
					if opod.Spec.NodeName != npod.Spec.NodeName {
						r.Log.Info("pod updated", "namespace", npod.Namespace, "name", npod.Name, "node", npod.Spec.NodeName)
						q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: npod.Spec.NodeName}})
					}
				},
				DeleteFunc: func(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					pod := event.Object.(*corev1.Pod)
					if pod == nil || pod.Spec.NodeName == "" {
						return
					}
					r.Log.Info("pod deleted", "namespace", pod.Namespace, "name", pod.Name, "node", pod.Spec.NodeName)
					q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: pod.Spec.NodeName}})
				}},
		).
		Complete(r)
}
