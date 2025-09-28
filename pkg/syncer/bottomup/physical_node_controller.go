package bottomup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
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
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/utils"
)

const (
	// Label keys for virtual nodes (additional to those in bottomup_syncer.go)
	LabelVirtualNodeType = "type"
	// Virtual node type
	VirtualNodeTypeValue = "virtual-kubelet"

	NodeInstanceTypeLabelBeta = "beta.kubernetes.io/instance-type"
	NodeInstanceTypeLabel     = "node.kubernetes.io/instance-type"
	NodeInstanceTypeExternal  = "external"
	NodeInstanceTypeVNode     = "vnode"

	// Taint keys for node deletion
	TaintKeyVirtualNodeDeleting = "kubeocean.io/vnode-deleting"

	// Annotation keys for deletion tracking
	AnnotationDeletionTaintTime = "kubeocean.io/deletion-taint-time"
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
		return r.handleNodeDeletion(ctx, physicalNode.Name, true, 0)
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
		// return error if cluster binding is not found
		log.Error(err, "Failed to get cluster binding")
		return ctrl.Result{}, err
	}

	if clusterBinding.DeletionTimestamp != nil {
		log.Info("Cluster binding is being deleted, triggering deletion, delete virtual node")
		return r.handleNodeDeletion(ctx, physicalNode.Name, true, 0)
	}

	// If node does not match ClusterBinding selector, ensure virtual node is deleted
	if !r.nodeMatchesSelector(physicalNode, clusterBinding.Spec.NodeSelector) {
		log.Info("Node does not match ClusterBinding selector; deleting virtual node")
		return r.handleNodeDeletion(ctx, physicalNode.Name, true, 0)
	}

	// Check virtual node UID and deletion timestamp before processing policies
	if r.getClusterID() == "" {
		return ctrl.Result{}, fmt.Errorf("clusterID is unknown, skipping virtual node checks")
	}
	virtualNodeName := r.generateVirtualNodeName(physicalNode.Name)

	// Check if virtual node exists and verify UID match
	existingVirtualNode := &corev1.Node{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, existingVirtualNode)
	if err == nil {
		// Virtual node exists, check if UID matches
		if existingVirtualNode.Labels[cloudv1beta1.LabelPhysicalNodeUID] != string(physicalNode.UID) {
			log.Info("Virtual node UID mismatch, triggering deletion",
				"virtualNodeUID", existingVirtualNode.Labels[cloudv1beta1.LabelPhysicalNodeUID],
				"physicalNodeUID", string(physicalNode.UID))
			return r.handleNodeDeletion(ctx, physicalNode.Name, true, 0)
		}

		// Check if virtual node has deletion timestamp
		if existingVirtualNode.DeletionTimestamp != nil {
			log.Info("Virtual node has deletion timestamp, calling handleNodeDeletion")
			return r.handleNodeDeletion(ctx, physicalNode.Name, true, 0)
		}
	} else if !errors.IsNotFound(err) {
		log.Error(err, "Failed to get virtual node")
		return ctrl.Result{}, err
	}

	// Find applicable ResourceLeasingPolicies by node selector
	policies, err := r.getApplicablePolicies(ctx, physicalNode, clusterBinding.Spec.NodeSelector)
	if err != nil {
		log.Error(err, "Failed to get applicable policies")
		return ctrl.Result{}, err
	}

	if len(policies) == 0 {
		log.Info("No matching ResourceLeasingPolicy found; deleting virtual node")
		return r.handleNodeDeletion(ctx, physicalNode.Name, true, 0)
	}

	var policy *cloudv1beta1.ResourceLeasingPolicy
	// If one or more policies match, use the earliest one
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
		policies = policies[:1]
		log.Info("Multiple ResourceLeasingPolicies matched. Using the earliest one and ignoring others.",
			"selected", policies[0].Name, "ignored", strings.Join(ignored, ","))
	} else {
		log.V(1).Info("Single ResourceLeasingPolicy matched", "selected", policies[0].Name)
	}
	// Use the first (earliest) policy
	policy = &policies[0]
	// Handle time window management for policy-managed nodes
	var availableResources corev1.ResourceList

	// Calculate available resources (with or without policy constraints)
	availableResources, err = r.calculateAvailableResources(ctx, physicalNode, policy)
	if err != nil {
		log.Error(err, "Failed to calculate available resources")
		return ctrl.Result{}, err
	}

	// Create or update virtual node

	err = r.createOrUpdateVirtualNode(ctx, physicalNode, availableResources, policies, clusterBinding.Spec.DisableNodeDefaultTaint)
	if err != nil {
		log.Error(err, "Failed to create or update virtual node")
		return ctrl.Result{}, err
	}

	// Start lease controller for the virtual node
	r.startLeaseController(virtualNodeName)

	// Check and add policy-applied label to physical node if missing
	err = r.ensurePolicyAppliedLabel(ctx, physicalNode, policy.Name)
	if err != nil {
		log.Error(err, "Failed to ensure policy-applied label on physical node")
		return ctrl.Result{}, err
	}

	// Handle out-of-time-windows pod deletion if node is outside time windows
	if !r.isWithinTimeWindows(policy) {
		log.V(1).Info("Handling out-of-time-windows pod deletion")
		result, err := r.handleOutOfTimeWindows(ctx, physicalNode.Name, policy)
		if err != nil {
			log.Error(err, "Failed to handle out-of-time-windows pod deletion")
			return ctrl.Result{}, err
		}
		// If handleOutOfTimeWindows returns a requeue, use that instead of default
		if result.RequeueAfter > 0 {
			return result, nil
		}
	}

	log.V(1).Info("Successfully processed physical node", "availableResources", availableResources)

	return ctrl.Result{RequeueAfter: DefaultNodeSyncInterval}, nil
}

// addDeletionTaint adds the deletion taint to a virtual node and returns the updated node
func (r *PhysicalNodeReconciler) addDeletionTaint(ctx context.Context, virtualNode *corev1.Node) (*corev1.Node, error) {
	logger := r.Log.WithValues("virtualNode", virtualNode.Name)

	// Check if deletion taint already exists
	taintExists := false
	for _, taint := range virtualNode.Spec.Taints {
		if taint.Key == TaintKeyVirtualNodeDeleting {
			taintExists = true
			break
		}
	}

	// If both taint exists and node is already unschedulable, no action needed
	if taintExists && virtualNode.Spec.Unschedulable {
		logger.V(1).Info("Deletion taint and unschedulable flag already set")
		return virtualNode, nil
	}

	// Create a deep copy to avoid modifying the original object
	updatedNode := virtualNode.DeepCopy()

	// Add deletion taint if it doesn't exist
	if !taintExists {
		deletionTaint := corev1.Taint{
			Key:    TaintKeyVirtualNodeDeleting,
			Value:  "true",
			Effect: corev1.TaintEffectNoSchedule,
		}
		updatedNode.Spec.Taints = append(updatedNode.Spec.Taints, deletionTaint)
	}

	// Always ensure the node is marked as unschedulable
	updatedNode.Spec.Unschedulable = true

	// Add annotation with taint addition time
	if updatedNode.Annotations == nil {
		updatedNode.Annotations = make(map[string]string)
	}
	updatedNode.Annotations[AnnotationDeletionTaintTime] = time.Now().Format(time.RFC3339)

	// Update the node
	if err := r.VirtualClient.Update(ctx, updatedNode); err != nil {
		return nil, fmt.Errorf("failed to add deletion taint: %w", err)
	}

	logger.Info("Added deletion taint to virtual node")

	return updatedNode, nil
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
		if utils.IsSystemPod(&pod) {
			logger.V(1).Info("skip system pod on virtual node", "pod", pod.Name, "namespace", pod.Namespace, "phase", pod.Status.Phase)
			continue
		}
		if pod.Spec.NodeName == virtualNodeName && (pod.Status.Phase == corev1.PodPending || pod.Status.Phase == corev1.PodRunning) {
			logger.Info("Found active pod on virtual node", "pod", pod.Name, "namespace", pod.Namespace, "phase", pod.Status.Phase)
			return true, nil
		}
	}

	logger.V(1).Info("No active pods found on virtual node")
	return false, nil
}

// forceEvictPodsOnVirtualNode forcefully evicts all pods on the virtual node by deleting them
// Pods with deletionTimestamp set will be skipped
// If any pod deletion fails, it continues with other pods but returns an error at the end
// skipTimeOutTaintsTolerations: if true, skip pods with TaintOutOfTimeWindows tolerations
func (r *PhysicalNodeReconciler) forceEvictPodsOnVirtualNode(ctx context.Context, virtualNodeName string, skipTimeOutTaintsTolerations bool) error {
	logger := r.Log.WithValues("virtualNode", virtualNodeName)

	// List all pods on this virtual node
	podList := &corev1.PodList{}
	listOptions := []client.ListOption{
		client.MatchingFields{"spec.nodeName": virtualNodeName},
	}

	if err := r.VirtualClient.List(ctx, podList, listOptions...); err != nil {
		return fmt.Errorf("failed to list pods on virtual node %s: %w", virtualNodeName, err)
	}

	if len(podList.Items) == 0 {
		logger.V(1).Info("No pods found on virtual node")
		return nil
	}

	var deletedCount, skippedCount, failedCount int
	var deletionErrors []error

	// Delete all pods
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName != virtualNodeName {
			continue
		}

		// Skip pods with non-nil deletionTimestamp
		if pod.DeletionTimestamp != nil {
			skippedCount++
			logger.V(1).Info("Skipping pod with deletionTimestamp set", "pod", pod.Name, "namespace", pod.Namespace)
			continue
		}

		// If skipTimeOutTaintsTolerations is true, check if Pod has TaintOutOfTimeWindows tolerations
		if skipTimeOutTaintsTolerations && r.hasTaintOutOfTimeWindowsToleration(pod) {
			skippedCount++
			logger.V(1).Info("Skipping pod with TaintOutOfTimeWindows toleration", "pod", pod.Name, "namespace", pod.Namespace)
			continue
		}

		logger.Info("Deleting pod to evict", "pod", pod.Name, "namespace", pod.Namespace, "phase", pod.Status.Phase)
		// Delete the pod immediately
		deleteOptions := &client.DeleteOptions{
			Preconditions: &metav1.Preconditions{
				UID: &pod.UID,
			},
		}
		if err := r.VirtualClient.Delete(ctx, pod, deleteOptions); err != nil {
			if errors.IsNotFound(err) {
				// Pod has already been deleted, ignore
				continue
			}
			failedCount++
			deletionErrors = append(deletionErrors, fmt.Errorf("failed to delete pod %s/%s: %w", pod.Namespace, pod.Name, err))
			logger.Error(err, "Failed to delete pod", "pod", pod.Name, "namespace", pod.Namespace)
			// Continue with other pods even if one fails
			continue
		}

		deletedCount++
		logger.Info("Successfully deleted pod", "pod", pod.Name, "namespace", pod.Namespace)
	}

	logger.Info("Completed virtual pod deletion", "deleted", deletedCount, "skipped", skippedCount, "failed", failedCount, "total", len(podList.Items))

	// Return error if any deletions failed
	if len(deletionErrors) > 0 {
		return fmt.Errorf("failed to delete %d pods: %v", len(deletionErrors), deletionErrors)
	}

	return nil
}

// hasTaintOutOfTimeWindowsToleration checks if a pod has tolerations for TaintOutOfTimeWindows
func (r *PhysicalNodeReconciler) hasTaintOutOfTimeWindowsToleration(pod *corev1.Pod) bool {
	for _, toleration := range pod.Spec.Tolerations {
		if toleration.Key == cloudv1beta1.TaintOutOfTimeWindows {
			return true
		}
	}
	return false
}

// handleNodeDeletion handles deletion of physical node
// forceReclaim: whether to force reclaim resources by evicting pods
// gracefulReclaimPeriodSeconds: graceful period before force eviction (0 means immediate)
func (r *PhysicalNodeReconciler) handleNodeDeletion(ctx context.Context, physicalNodeName string, forceReclaim bool, gracefulReclaimPeriodSeconds int32) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalNode", physicalNodeName)

	if r.getClusterID() == "" {
		return ctrl.Result{}, fmt.Errorf("clusterID is unknown, skipping handleNodeDeletion")
	}

	// Clean up policy-applied label from physical node before processing virtual node deletion
	if err := r.removePolicyAppliedLabel(ctx, physicalNodeName); err != nil {
		logger.Error(err, "Failed to remove policy-applied label from physical node")
		return ctrl.Result{}, err
	}

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

	// Step 1.5: Check if virtual node deletion timestamp is empty
	if virtualNode.DeletionTimestamp == nil {
		logger.Info("Virtual node deletion timestamp is empty, calling Delete to trigger deletion")
		deleteOptions := &client.DeleteOptions{
			Preconditions: &metav1.Preconditions{
				UID: &virtualNode.UID,
			},
		}
		err = r.VirtualClient.Delete(ctx, virtualNode, deleteOptions)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Failed to delete virtual node")
			return ctrl.Result{}, err
		}
		logger.Info("Successfully triggered virtual node deletion")
		return ctrl.Result{}, nil
	}

	// Step 2: Handle ForceReclaim based on input parameters
	logger.Info("Processing deletion", "forceReclaim", forceReclaim, "gracefulPeriod", gracefulReclaimPeriodSeconds)

	if forceReclaim {
		if gracefulReclaimPeriodSeconds == 0 {
			// Immediate force eviction (physical node deleted scenario)
			logger.Info("Immediate force eviction requested, evicting all pods")
			if err := r.forceEvictPodsOnVirtualNode(ctx, virtualNodeName, false); err != nil {
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
			if err := r.forceEvictPodsOnVirtualNode(ctx, virtualNodeName, false); err != nil {
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

	// TODO: may to consider to list-watch pod and requeue when pod is deleted
	if hasPods {
		logger.Info("Virtual node still has active pods, cannot delete node")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Step 4: All conditions met, proceed with finalizer removal
	logger.Info("No active pods found, proceeding with finalizer removal")

	// Stop lease controller for the virtual node
	r.stopLeaseController(virtualNodeName)

	// Remove finalizer to allow virtual node deletion
	logger.Info("Removing finalizer from virtual node", "virtualNode", virtualNodeName)
	updatedVirtualNode := virtualNode.DeepCopy()
	controllerutil.RemoveFinalizer(updatedVirtualNode, cloudv1beta1.VirtualNodeFinalizer)

	err = r.VirtualClient.Update(ctx, updatedVirtualNode)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to remove finalizer from virtual node")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully removed finalizer from virtual node", "virtualNode", virtualNodeName)
	return ctrl.Result{}, nil
}

// getApplicablePolicies gets ResourceLeasingPolicies that apply to the given node
func (r *PhysicalNodeReconciler) getApplicablePolicies(ctx context.Context, node *corev1.Node, clusterSelector *corev1.NodeSelector) ([]cloudv1beta1.ResourceLeasingPolicy, error) {
	var policyList cloudv1beta1.ResourceLeasingPolicyList
	if err := r.PhysicalClient.List(ctx, &policyList); err != nil {
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
func (r *PhysicalNodeReconciler) isWithinTimeWindows(policy *cloudv1beta1.ResourceLeasingPolicy) bool {
	now := time.Now()
	currentDay := strings.ToLower(now.Weekday().String())
	currentTime := now.Format("15:04")
	r.Log.V(1).Info("Checking if policy is active now", "policy", policy.Name, "timeWindows", policy.Spec.TimeWindows, "currentDay", currentDay, "currentTime", currentTime)

	return policy.IsWithinTimeWindowsAt(currentDay, currentTime)
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
		if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
			r.Log.V(1).Info("Skipping pod that is managed by Kubeocean", "pod", pod.Namespace+"/"+pod.Name)
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
// Actual available resources = min(physical node available resources - used resources, policy limits)
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

// buildVirtualNodeAddresses builds addresses for virtual node
func (r *PhysicalNodeReconciler) buildVirtualNodeAddresses(physicalNode *corev1.Node) []corev1.NodeAddress {
	// Keep all original node addresses, but force update InternalIP to point to proxier pod IP
	// Use Pod IP instead of Service ClusterIP to avoid ClusterIP connectivity issues
	proxierPodIP := r.getProxierPodIP()
	if proxierPodIP != "" {
		// First find and update existing InternalIP
		internalIPUpdated := false
		addresses := make([]corev1.NodeAddress, len(physicalNode.Status.Addresses))
		copy(addresses, physicalNode.Status.Addresses)

		for i := range addresses {
			if addresses[i].Type == corev1.NodeInternalIP {
				addresses[i].Address = proxierPodIP
				internalIPUpdated = true
				r.Log.Info("Updated existing InternalIP for virtual node to point to proxier pod", "node", physicalNode.Name, "podIP", proxierPodIP)
				break
			}
		}

		// If no InternalIP found, add a new one
		if !internalIPUpdated {
			addresses = append(addresses, corev1.NodeAddress{
				Type:    corev1.NodeInternalIP,
				Address: proxierPodIP,
			})
			r.Log.Info("Added new InternalIP for virtual node pointing to proxier pod", "node", physicalNode.Name, "podIP", proxierPodIP)
		}

		return addresses
	} else {
		r.Log.Info("Unable to get proxier pod IP for virtual node, using original addresses", "node", physicalNode.Name)
		// If unable to get proxier pod IP, return original addresses
		return physicalNode.Status.Addresses
	}
}

// createOrUpdateVirtualNode creates or updates a virtual node based on physical node
func (r *PhysicalNodeReconciler) createOrUpdateVirtualNode(ctx context.Context, physicalNode *corev1.Node, resources corev1.ResourceList, policies []cloudv1beta1.ResourceLeasingPolicy, disableNodeDefaultTaint bool) error {
	virtualNodeName := r.generateVirtualNodeName(physicalNode.Name)
	logger := r.Log.WithValues("virtualNode", virtualNodeName, "physicalNode", physicalNode.Name)

	// Get applicable policy for taint management
	var policy *cloudv1beta1.ResourceLeasingPolicy
	if len(policies) > 0 {
		policy = &policies[0]
	}

	// Build virtual node addresses
	addresses := r.buildVirtualNodeAddresses(physicalNode)

	virtualNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        virtualNodeName,
			Labels:      r.buildVirtualNodeLabels(virtualNodeName, physicalNode),
			Annotations: r.buildVirtualNodeAnnotations(physicalNode, policies),
			Finalizers:  []string{cloudv1beta1.VirtualNodeFinalizer},
		},
		Spec: corev1.NodeSpec{
			// Transform taints including out-of-time-windows taint based on policy
			Taints: r.transformTaints(logger, physicalNode.Spec.Taints, disableNodeDefaultTaint, policy),
		},
		Status: corev1.NodeStatus{
			Capacity:    resources,
			Allocatable: resources,
			Conditions:  physicalNode.Status.Conditions,
			NodeInfo:    physicalNode.Status.NodeInfo,
			Phase:       physicalNode.Status.Phase,
			Addresses:   addresses,
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

		// Preserve existing proxier port to avoid port changes
		existingProxierPort := existingNode.Labels[cloudv1beta1.LabelProxierPort]
		newNode.Labels = virtualNode.Labels
		if existingProxierPort != "" {
			newNode.Labels[cloudv1beta1.LabelProxierPort] = existingProxierPort
			logger.V(1).Info("Preserved existing proxier port", "port", existingProxierPort)
		}

		newNode.Annotations = virtualNode.Annotations
		newNode.Status.Addresses = existingNode.Status.Addresses

		// Ensure finalizer exists
		if !controllerutil.ContainsFinalizer(newNode, cloudv1beta1.VirtualNodeFinalizer) {
			controllerutil.AddFinalizer(newNode, cloudv1beta1.VirtualNodeFinalizer)
		}

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
		// If exists in currentExpected, don't override
		if _, ok := currentExpected[key]; ok {
			continue
		}
		// If not in previousExpected, consider it as user-added
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
		// If exists in currentExpected, don't override
		if _, ok := currentExpected[key]; ok {
			continue
		}
		if key == AnnotationDeletionTaintTime {
			r.Log.Info("skipping deletion taint time", "key", key, "value", value, "node", new.Name)
			continue
		}
		// If not in previousExpected, consider it as user-added
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

	// Preserve TimeAdded for out-of-time-windows taint so graceful period is not reset on each reconciliation
	for i := range new.Spec.Taints {
		if new.Spec.Taints[i].Key == cloudv1beta1.TaintOutOfTimeWindows {
			for j := range existing.Spec.Taints {
				if existing.Spec.Taints[j].Key == cloudv1beta1.TaintOutOfTimeWindows && existing.Spec.Taints[j].TimeAdded != nil {
					new.Spec.Taints[i].TimeAdded = existing.Spec.Taints[j].TimeAdded
					break
				}
			}
		}
	}

	// Identify user-added taints
	for _, taint := range existing.Spec.Taints {
		// If exists in currentExpected, don't override
		if _, ok := currentExpectedMap[taint.Key]; ok {
			continue
		}
		if taint.Key == TaintKeyVirtualNodeDeleting {
			r.Log.Info("skipping deletion taint", "taint", taint, "node", new.Name)
			continue
		}
		// If not in previousExpected, consider it as user-added
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
	return utils.GenerateVirtualNodeName(r.getClusterID(), physicalNodeName)
}

// getPhysicalNodeInternalIP extracts the InternalIP from a physical node
func (r *PhysicalNodeReconciler) getPhysicalNodeInternalIP(physicalNode *corev1.Node) string {
	for _, addr := range physicalNode.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}

// generateStableProxierPort generates a stable port number for the VNode
// This method ensures the same node name always gets the same port
// Port range: 10000-65535 (55536 ports total)
func (r *PhysicalNodeReconciler) generateStableProxierPort(nodeName string) string {
	// Use a deterministic hash based on node name + cluster ID for stability
	stableKey := nodeName + "-" + r.getClusterID()
	hash := r.hashString(stableKey)

	// Base port in range 10000-65535
	basePort := 10000 + int(hash%55536)

	r.Log.V(2).Info("Generated stable proxier port",
		"nodeName", nodeName,
		"clusterID", r.getClusterID(),
		"stableKey", stableKey,
		"port", basePort)

	return fmt.Sprintf("%d", basePort)
}

// generateProxierPort generates a unique port number for the VNode
// Port range: 10000-65535 (55536 ports total)
// Uses a deterministic hash based on node name to ensure consistency
// Includes collision detection and resolution
func (r *PhysicalNodeReconciler) generateProxierPort(nodeName string) string {
	// Use a more robust hash function to reduce collisions
	hash := r.hashString(nodeName)

	// Base port in range 10000-65535
	basePort := 10000 + int(hash%55536)

	// Check for conflicts and resolve if necessary
	port := r.resolvePortConflict(nodeName, basePort)

	return fmt.Sprintf("%d", port)
}

// hashString generates a more robust hash for the given string
func (r *PhysicalNodeReconciler) hashString(s string) uint32 {
	// Use FNV-1a hash algorithm for better distribution
	const fnvPrime = 0x01000193
	hash := uint32(0x811c9dc5) // FNV offset basis

	for _, c := range s {
		hash ^= uint32(c)
		hash *= fnvPrime
	}

	return hash
}

// resolvePortConflict resolves port conflicts by finding an available port
func (r *PhysicalNodeReconciler) resolvePortConflict(nodeName string, basePort int) int {
	// Get existing VNode ports from informer cache
	existingPorts := r.getExistingPortsFromInformer()

	// Try the base port first
	if !r.isPortInUse(basePort, existingPorts) {
		return basePort
	}

	// If base port is in use, try with small offsets
	for offset := 1; offset <= 100; offset++ {
		candidatePort := basePort + offset

		// Ensure port is in valid range
		if candidatePort > 65535 {
			candidatePort = 10000 + (candidatePort % 55536)
		}

		if !r.isPortInUse(candidatePort, existingPorts) {
			r.Log.V(1).Info("Resolved port conflict",
				"nodeName", nodeName,
				"originalPort", basePort,
				"resolvedPort", candidatePort)
			return candidatePort
		}
	}

	// If still no available port found, use a hash-based fallback
	fallbackHash := r.hashString(nodeName + "-fallback")
	fallbackPort := 10000 + (int(fallbackHash) % 55536)

	r.Log.Info("Using fallback port due to conflicts",
		"nodeName", nodeName,
		"fallbackPort", fallbackPort)

	return fallbackPort
}

// getExistingPortsFromInformer gets ports from informer cache
func (r *PhysicalNodeReconciler) getExistingPortsFromInformer() map[int]bool {
	ports := make(map[int]bool)

	// If VirtualClient is not available, return empty map
	if r.VirtualClient == nil {
		r.Log.V(1).Info("VirtualClient not available, skipping port conflict detection")
		return ports
	}

	// List all VNodes in the virtual cluster
	virtualNodes := &corev1.NodeList{}
	err := r.VirtualClient.List(context.Background(), virtualNodes)
	if err != nil {
		r.Log.Error(err, "Failed to list virtual nodes for port conflict detection")
		return ports
	}

	// Extract ports from VNode labels
	for _, node := range virtualNodes.Items {
		if portStr, exists := node.Labels[cloudv1beta1.LabelProxierPort]; exists {
			if port, err := strconv.Atoi(portStr); err == nil {
				ports[port] = true
			}
		}
	}

	return ports
}

// isPortInUse checks if a port is already in use
func (r *PhysicalNodeReconciler) isPortInUse(port int, existingPorts map[int]bool) bool {
	return existingPorts[port]
}

// getClusterID returns a cluster identifier for the physical cluster
func (r *PhysicalNodeReconciler) getClusterID() string {
	// Use ClusterBinding.Spec.ClusterID as cluster ID
	// This field is immutable and specifically designed for this purpose
	if r.ClusterBinding != nil && r.ClusterBinding.Spec.ClusterID != "" {
		return r.ClusterBinding.Spec.ClusterID
	}

	return ""
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

	// Add Kubeocean-specific labels
	labels[cloudv1beta1.LabelClusterBinding] = r.ClusterBindingName
	labels[cloudv1beta1.LabelPhysicalClusterID] = r.getClusterID()
	labels[cloudv1beta1.LabelPhysicalNodeName] = physicalNode.Name
	labels[cloudv1beta1.LabelPhysicalNodeUID] = string(physicalNode.UID)
	labels[cloudv1beta1.LabelManagedBy] = "kubeocean"
	labels[LabelVirtualNodeType] = VirtualNodeTypeValue
	labels[NodeInstanceTypeLabel] = NodeInstanceTypeVNode
	labels[NodeInstanceTypeLabelBeta] = NodeInstanceTypeVNode
	labels["kubernetes.io/hostname"] = nodeName

	// Add physical node InternalIP
	internalIP := r.getPhysicalNodeInternalIP(physicalNode)
	if internalIP != "" {
		labels[cloudv1beta1.LabelPhysicalNodeInnerIP] = internalIP
	}

	// Add unique proxier port - use stable generation
	proxierPort := r.generateStableProxierPort(nodeName)
	labels[cloudv1beta1.LabelProxierPort] = proxierPort

	r.Log.V(1).Info("Built virtual node labels", "physicalNode", physicalNode.Name, "labelCount", len(labels), "proxierPort", proxierPort)

	return labels
}

// buildVirtualNodeAnnotations builds annotations for virtual node
func (r *PhysicalNodeReconciler) buildVirtualNodeAnnotations(physicalNode *corev1.Node, policies []cloudv1beta1.ResourceLeasingPolicy) map[string]string {
	annotations := physicalNode.Annotations

	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Add Kubeocean-specific annotations
	annotations[cloudv1beta1.AnnotationLastSyncTime] = time.Now().Format(time.RFC3339)

	// Add physical node metadata
	annotations[cloudv1beta1.LabelPhysicalNodeName] = physicalNode.Name
	annotations["kubeocean.io/physical-node-uid"] = string(physicalNode.UID)

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
					quantity := ""
					if limit.Quantity != nil {
						quantity = limit.Quantity.String()
					}
					percent := ""
					if limit.Percent != nil {
						percent = fmt.Sprintf("%d%%", *limit.Percent)
					}
					limits = append(limits, limit.Resource+"="+quantity+"/"+percent)
				}
				policyDetail += "(" + strings.Join(limits, ",") + ")"
			}
			policyDetails = append(policyDetails, policyDetail)
		}

		annotations[cloudv1beta1.AnnotationPoliciesApplied] = strings.Join(policyNames, ",")
		annotations["kubeocean.io/policy-details"] = strings.Join(policyDetails, ";")
	} else {
		annotations[cloudv1beta1.AnnotationPoliciesApplied] = ""
	}

	r.Log.V(1).Info("Built virtual node annotations", "physicalNode", physicalNode.Name, "annotationCount", len(annotations))

	return annotations
}

// transformTaints transforms physical node taints to virtual node taints
// Specifically converts node.kubernetes.io/unschedulable to kubeocean.io/physical-node-unschedulable
// Optionally adds the default virtual node taint based on disableNodeDefaultTaint setting
// Also adds out-of-time-windows taint based on policy if node is outside time windows
func (r *PhysicalNodeReconciler) transformTaints(logger logr.Logger, physicalTaints []corev1.Taint, disableNodeDefaultTaint bool, policy *cloudv1beta1.ResourceLeasingPolicy) []corev1.Taint {
	virtualTaints := make([]corev1.Taint, 0, len(physicalTaints))

	for _, taint := range physicalTaints {
		if taint.Key == cloudv1beta1.TaintOutOfTimeWindows {
			// Delete out-of-time-windows taint
			continue
		}
		// Transform node.kubernetes.io/unschedulable to kubeocean.io/physical-node-unschedulable
		if taint.Key == "node.kubernetes.io/unschedulable" {
			transformedTaint := corev1.Taint{
				Key:    cloudv1beta1.TaintPhysicalNodeUnschedulable,
				Value:  taint.Value,
				Effect: taint.Effect,
			}
			virtualTaints = append(virtualTaints, transformedTaint)
		} else {
			// Keep other taints as-is
			virtualTaints = append(virtualTaints, taint)
		}
	}

	// Add the default virtual node taint only if not disabled
	if !disableNodeDefaultTaint {
		virtualTaints = append(virtualTaints, corev1.Taint{
			Key:    cloudv1beta1.TaintVnodeDefaultTaint,
			Effect: corev1.TaintEffectNoSchedule,
		})
	}

	// Add out-of-time-windows taint based on policy if node is outside time windows
	if policy != nil && !r.isWithinTimeWindows(policy) {
		forceReclaim := policy.Spec.ForceReclaim
		var effect corev1.TaintEffect
		if forceReclaim {
			effect = corev1.TaintEffectNoExecute
		} else {
			effect = corev1.TaintEffectNoSchedule
		}

		virtualTaints = append(virtualTaints, corev1.Taint{
			Key:       cloudv1beta1.TaintOutOfTimeWindows,
			Value:     "true",
			Effect:    effect,
			TimeAdded: &metav1.Time{Time: time.Now()},
		})
		logger.V(1).Info("Node outside time windows, will add out-of-time-windows taint", "effect", effect, "forceReclaim", forceReclaim)
	} else {
		logger.V(1).Info("Node within time windows, no out-of-time-windows taint needed")
	}

	return virtualTaints
}

// handleOutOfTimeWindows handles the out-of-time-windows pod deletion logic
func (r *PhysicalNodeReconciler) handleOutOfTimeWindows(ctx context.Context, physicalNodeName string, policy *cloudv1beta1.ResourceLeasingPolicy) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalNode", physicalNodeName)
	virtualNodeName := r.generateVirtualNodeName(physicalNodeName)

	// Get the virtual node
	virtualNode := &corev1.Node{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Virtual node doesn't exist, nothing to do
			logger.V(1).Info("Virtual node not found, nothing to handle")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get virtual node: %w", err)
	}

	// Check if out-of-time-windows taint already exists
	var existingTaint *corev1.Taint
	for i, taint := range virtualNode.Spec.Taints {
		if taint.Key == cloudv1beta1.TaintOutOfTimeWindows {
			existingTaint = &virtualNode.Spec.Taints[i]
			break
		}
	}

	gracefulPeriod := policy.Spec.GracefulReclaimPeriodSeconds
	forceReclaim := policy.Spec.ForceReclaim

	// If ForceReclaim is not set, no pod deletion needed
	if !forceReclaim {
		logger.V(1).Info("ForceReclaim not set, no pod deletion needed")
		return ctrl.Result{RequeueAfter: DefaultNodeSyncInterval}, nil
	}

	// If gracefulPeriod is 0, immediately delete all pods
	if gracefulPeriod == 0 {
		logger.Info("Graceful period is 0, immediately deleting all virtual pods")
		// do not delete pods with taints tolerations because it will be recreated by controller
		err = r.forceEvictPodsOnVirtualNode(ctx, virtualNodeName, true)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to delete virtual pods on node %s: %w", virtualNodeName, err)
		}
		return ctrl.Result{RequeueAfter: DefaultNodeSyncInterval}, nil
	}

	if existingTaint == nil {
		// return error to requeue to handle out-of-time-windows taint
		return ctrl.Result{}, fmt.Errorf("no out-of-time-windows taint found")
	}

	// Check if graceful period has passed
	if existingTaint.TimeAdded == nil {
		logger.Info("No taint time found, immediately deleting all virtual pods")
		err = r.forceEvictPodsOnVirtualNode(ctx, virtualNodeName, true)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to delete virtual pods on node %s: %w", virtualNodeName, err)
		}
		return ctrl.Result{RequeueAfter: DefaultNodeSyncInterval}, nil
	}

	taintTime := existingTaint.TimeAdded.Time
	timeSinceTaint := time.Since(taintTime)
	gracefulDuration := time.Duration(gracefulPeriod) * time.Second

	// If graceful period has passed, delete all pods
	if timeSinceTaint >= gracefulDuration {
		logger.Info("Graceful period passed, deleting all virtual pods", "taintTime", taintTime, "timeSinceTaint", timeSinceTaint, "gracefulPeriod", gracefulPeriod)
		err = r.forceEvictPodsOnVirtualNode(ctx, virtualNodeName, true)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to delete virtual pods on node %s: %w", virtualNodeName, err)
		}
		return ctrl.Result{RequeueAfter: DefaultNodeSyncInterval}, nil
	}

	// Graceful period not yet passed, requeue for remaining time
	remainingTime := gracefulDuration - timeSinceTaint
	logger.Info("Still within graceful period, will requeue", "remainingTime", remainingTime, "gracefulPeriod", gracefulPeriod)
	return ctrl.Result{RequeueAfter: remainingTime}, nil
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
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldNode := oldObj.(*corev1.Node)
			newNode := newObj.(*corev1.Node)
			if oldNode != nil && newNode != nil {
				// Check if deletion timestamp changed from empty to non-empty
				oldDeletionTimestamp := oldNode.DeletionTimestamp
				newDeletionTimestamp := newNode.DeletionTimestamp

				if oldDeletionTimestamp == nil && newDeletionTimestamp != nil {
					// Virtual node deletion timestamp was set, trigger physical node reconciliation
					r.handleVirtualNodeEvent(newNode, "update")
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			node := obj.(*corev1.Node)
			if node != nil {
				r.handleVirtualNodeEvent(node, "delete")
			}
		},
	})

	return ctrl.NewControllerManagedBy(physicalManager).
		For(&corev1.Node{}).
		Named(uniqueControllerName).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			node, ok := obj.(*corev1.Node)
			if !ok {
				// invalid node means the object may be the pod object
				_, ok2 := obj.(*corev1.Pod)
				return ok2
			}
			return r.shouldProcessNode(node)
		})).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
			RateLimiter:             rateLimiter,
			NewQueue: func(controllerName string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
				return r.workQueue
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
					r.Log.V(1).Info("pod created", "namespace", pod.Namespace, "name", pod.Name, "node", pod.Spec.NodeName)
					q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: pod.Spec.NodeName}})
				},
				UpdateFunc: func(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					opod := event.ObjectOld.(*corev1.Pod)
					npod := event.ObjectNew.(*corev1.Pod)
					if opod == nil || npod == nil || npod.Spec.NodeName == "" {
						return
					}
					if opod.Spec.NodeName != npod.Spec.NodeName {
						r.Log.V(1).Info("pod updated", "namespace", npod.Namespace, "name", npod.Name, "node", npod.Spec.NodeName)
						q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: npod.Spec.NodeName}})
					}
				},
				DeleteFunc: func(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
					pod := event.Object.(*corev1.Pod)
					if pod == nil || pod.Spec.NodeName == "" {
						return
					}
					r.Log.V(1).Info("pod deleted", "namespace", pod.Namespace, "name", pod.Name, "node", pod.Spec.NodeName)
					q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: pod.Spec.NodeName}})
				}},
		).
		Complete(r)
}

// handleVirtualNodeEvent handles virtual node addition and deletion events
func (r *PhysicalNodeReconciler) handleVirtualNodeEvent(node *corev1.Node, eventType string) {
	log := r.Log.WithValues("virtualNode", node.Name, "eventType", eventType)

	// Check if this is a kubeocean-managed node
	if node.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		log.V(1).Info("Virtual node not managed by kubeocean, skipping")
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

	log.Info("Virtual node event, triggering reconciliation",
		"eventType", eventType, "physicalNode", physicalNodeName)

	// Trigger reconciliation for the physical node
	if err := r.TriggerReconciliation(physicalNodeName); err != nil {
		log.Error(err, "Failed to trigger node reconciliation")
	}
}

// shouldProcessNode determines if a node should be processed based on its labels
func (r *PhysicalNodeReconciler) shouldProcessNode(node *corev1.Node) bool {
	if node == nil {
		return false
	}

	// Check instance-type labels for external, vnode, and eklet
	if instanceType, exists := node.Labels[NodeInstanceTypeLabel]; exists {
		if instanceType == NodeInstanceTypeExternal || instanceType == NodeInstanceTypeVNode || instanceType == "eklet" {
			r.Log.V(1).Info("Skipping node with excluded instance type",
				"node", node.Name, "instanceType", instanceType, "label", NodeInstanceTypeLabel)
			return false
		}
	}

	if instanceType, exists := node.Labels[NodeInstanceTypeLabelBeta]; exists {
		if instanceType == NodeInstanceTypeExternal || instanceType == NodeInstanceTypeVNode || instanceType == "eklet" {
			r.Log.V(1).Info("Skipping node with excluded instance type",
				"node", node.Name, "instanceType", instanceType, "label", NodeInstanceTypeLabelBeta)
			return false
		}
	}

	// Check for virtual-kubelet type
	if nodeType, exists := node.Labels[LabelVirtualNodeType]; exists && nodeType == VirtualNodeTypeValue {
		r.Log.V(1).Info("Skipping virtual-kubelet node", "node", node.Name, "type", nodeType)
		return false
	}

	return true
}

// getProxierServiceClusterIP retrieves the ClusterIP of the proxier service for this ClusterBinding
func (r *PhysicalNodeReconciler) getProxierServiceClusterIP() string {
	if r.ClusterBinding == nil {
		r.Log.Error(nil, "ClusterBinding is nil, cannot determine proxier service name")
		return ""
	}

	// Generate proxier service name based on ClusterBinding
	// This should match the naming convention used in the manager controller
	proxierServiceName := fmt.Sprintf("kubeocean-proxier-%s-svc", r.ClusterBinding.Spec.ClusterID)

	// Get the service from the virtual cluster (where proxier service is deployed)
	service := &corev1.Service{}
	err := r.VirtualClient.Get(context.Background(), client.ObjectKey{
		Name:      proxierServiceName,
		Namespace: "kubeocean-system", // This should match the namespace where proxier is deployed
	}, service)

	if err != nil {
		r.Log.Error(err, "Failed to get proxier service", "serviceName", proxierServiceName)
		return ""
	}

	if service.Spec.ClusterIP == "" || service.Spec.ClusterIP == "None" {
		r.Log.Error(nil, "Proxier service does not have a valid ClusterIP", "serviceName", proxierServiceName, "clusterIP", service.Spec.ClusterIP)
		return ""
	}

	r.Log.V(2).Info("Found proxier service ClusterIP", "serviceName", proxierServiceName, "clusterIP", service.Spec.ClusterIP)
	return service.Spec.ClusterIP
}

// removePolicyAppliedLabel removes the policy-applied label from the physical node if it exists
func (r *PhysicalNodeReconciler) removePolicyAppliedLabel(ctx context.Context, physicalNodeName string) error {
	logger := r.Log.WithValues("physicalNode", physicalNodeName)

	// Get the physical node
	physicalNode := &corev1.Node{}
	err := r.PhysicalClient.Get(ctx, client.ObjectKey{Name: physicalNodeName}, physicalNode)
	if err != nil {
		if errors.IsNotFound(err) {
			// Physical node doesn't exist, ignore
			logger.V(1).Info("Physical node not found, ignoring policy-applied label removal")
			return nil
		}
		// Return error for retry
		return fmt.Errorf("failed to get physical node for label removal: %w", err)
	}

	// Check if the label exists
	if len(physicalNode.Labels) == 0 {
		logger.V(1).Info("Physical node has no labels, no action needed")
		return nil
	}

	if _, exists := physicalNode.Labels[cloudv1beta1.LabelPolicyApplied]; !exists {
		logger.V(1).Info("Policy-applied label not found, no action needed")
		return nil
	}

	logger.Info("Removing policy-applied label from physical node")

	// Create a patch to remove the label
	patchData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				cloudv1beta1.LabelPolicyApplied: nil,
			},
		},
	}

	// Convert to JSON for the patch
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to marshal patch data for label removal: %w", err)
	}

	// Apply the strategic merge patch
	patch := client.RawPatch(types.StrategicMergePatchType, patchBytes)
	if err := r.PhysicalClient.Patch(ctx, physicalNode, patch); err != nil {
		return fmt.Errorf("failed to patch physical node to remove policy-applied label: %w", err)
	}

	logger.Info("Successfully removed policy-applied label from physical node")
	return nil
}

// ensurePolicyAppliedLabel ensures the physical node has the policy-applied label set to the current policy name
func (r *PhysicalNodeReconciler) ensurePolicyAppliedLabel(ctx context.Context, physicalNode *corev1.Node, policyName string) error {
	logger := r.Log.WithValues("physicalNode", physicalNode.Name, "policyName", policyName)

	// Check if the label already exists and has the correct value
	if existingPolicyName, exists := physicalNode.Labels[cloudv1beta1.LabelPolicyApplied]; exists {
		if existingPolicyName == policyName {
			logger.V(1).Info("Policy-applied label already set correctly")
			return nil
		}
		logger.Info("Policy-applied label exists but with different value, updating",
			"existingValue", existingPolicyName, "newValue", policyName)
	} else {
		logger.Info("Policy-applied label missing, adding it", "newValue", policyName)
	}

	// Create a patch to add/update the label
	patchData := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]interface{}{
				cloudv1beta1.LabelPolicyApplied: policyName,
			},
		},
	}

	// Convert to JSON for the patch
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to marshal patch data: %w", err)
	}

	// Apply the strategic merge patch
	patch := client.RawPatch(types.StrategicMergePatchType, patchBytes)
	if err := r.PhysicalClient.Patch(ctx, physicalNode, patch); err != nil {
		return fmt.Errorf("failed to patch physical node with policy-applied label: %w", err)
	}

	logger.Info("Successfully added/updated policy-applied label on physical node")
	return nil
}

// getProxierPodIP retrieves the Pod IP of the proxier pod for this ClusterBinding
func (r *PhysicalNodeReconciler) getProxierPodIP() string {
	if r.ClusterBinding == nil {
		r.Log.Error(nil, "ClusterBinding is nil, cannot determine proxier pod name")
		return ""
	}

	// Generate proxier deployment name based on ClusterBinding
	proxierDeploymentName := fmt.Sprintf("kubeocean-proxier-%s", r.ClusterBinding.Spec.ClusterID)

	// List pods with the proxier deployment label selector
	podList := &corev1.PodList{}
	err := r.VirtualClient.List(context.Background(), podList, client.InNamespace("kubeocean-system"), client.MatchingLabels{
		"app.kubernetes.io/name":     "kubeocean-proxier",
		"app.kubernetes.io/instance": r.ClusterBinding.Name,
	})

	if err != nil {
		r.Log.Error(err, "Failed to list proxier pods", "deploymentName", proxierDeploymentName)
		return ""
	}

	// Find a running proxier pod
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			r.Log.V(2).Info("Found running proxier pod IP", "podName", pod.Name, "podIP", pod.Status.PodIP)
			return pod.Status.PodIP
		}
	}

	r.Log.Error(nil, "No running proxier pod found", "deploymentName", proxierDeploymentName)
	return ""
}
