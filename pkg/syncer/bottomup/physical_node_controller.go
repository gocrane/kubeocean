package bottomup

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	LabelVirtualNodeType = "type"
	// Virtual node type
	VirtualNodeTypeValue = "virtual-kubelet"

	NodeInstanceTypeLabelBeta = "beta.kubernetes.io/instance-type"
	NodeInstanceTypeLabel     = "node.kubernetes.io/instance-type"
	NodeInstanceTypeExternal  = "external"
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
	Scheme         *runtime.Scheme
	Log            logr.Logger

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
		return r.handleNodeDeletion(ctx, physicalNode.Name)
	}

	// Find applicable ResourceLeasingPolicies by node selector
	policies, err := r.getApplicablePolicies(ctx, physicalNode, clusterBinding.Spec.NodeSelector)
	if err != nil {
		log.Error(err, "Failed to get applicable policies")
		return ctrl.Result{}, err
	}

	var policy *cloudv1beta1.ResourceLeasingPolicy
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
		policy = &policies[0]
	}

	// If no matching policy, use all remaining resources; otherwise validate time windows and apply policy
	var availableResources corev1.ResourceList
	if policy == nil {
		log.V(1).Info("No matching ResourceLeasingPolicy; using all remaining resources of physical node")
		availableResources = r.getBaseResources(physicalNode)
	} else {
		// Check if within time windows
		if !r.isWithinTimeWindows([]cloudv1beta1.ResourceLeasingPolicy{*policy}) {
			log.V(1).Info("Node outside time windows")
			return r.handleNodeDeletion(ctx, physicalNode.Name)
		}

		// Calculate available resources (considering actual pod usage)
		availableResources, err = r.calculateAvailableResources(ctx, physicalNode, policy)
		if err != nil {
			log.Error(err, "Failed to calculate available resources")
			return ctrl.Result{}, err
		}
	}

	// Create or update virtual node
	err = r.createOrUpdateVirtualNode(ctx, physicalNode, availableResources, policies)
	if err != nil {
		log.Error(err, "Failed to create or update virtual node")
		return ctrl.Result{}, err
	}

	log.V(1).Info("Successfully processed physical node", "availableResources", availableResources)

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

	for _, pod := range podList.Items {
		// Skip pods that are not running or are in terminal states
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}

		// Calculate pod resource requests (not limits, as requests are what's actually reserved)
		podUsage := r.calculatePodResourceRequests(&pod)

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
		// Collect all resource limits from all policies
		resourceLimits := make(map[corev1.ResourceName]resource.Quantity)

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

		// Apply the collected limits
		for resourceName, limitQuantity := range resourceLimits {
			if baseQuantity, exists := availableResources[resourceName]; exists {
				// Take the minimum of policy limit and base resources
				if limitQuantity.Cmp(baseQuantity) < 0 {
					availableResources[resourceName] = limitQuantity
				}
			} else {
				// Resource not found in node, still apply the limit
				availableResources[resourceName] = limitQuantity
			}
		}
	}

	logger.V(1).Info("Calculated available resources", "availableResources", availableResources)
	return availableResources, nil
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
		virtualNode.ResourceVersion = existingNode.ResourceVersion

		logger.V(1).Info("Updating virtual node",
			"resources", resources,
			"resourceVersion", virtualNode.ResourceVersion)
		return r.VirtualClient.Update(ctx, virtualNode)
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

	encodedMetadata, exists := node.Annotations[AnnotationExpectedMetadata]
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
	node.Annotations[AnnotationExpectedMetadata] = encodedMetadata
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
		// 如果不在previousExpected中，则认为是用户添加的
		if _, ok := previousExpected[key]; !ok {
			new.Labels[key] = value
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
		// 如果不在previousExpected中，则认为是用户添加的
		if _, ok := previousExpectedMap[taint.Key]; !ok {
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
	labels[LabelManagedBy] = "tapestry"
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
	annotations[AnnotationLastSyncTime] = time.Now().Format(time.RFC3339)

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

		annotations[AnnotationPoliciesApplied] = strings.Join(policyNames, ",")
		annotations["tapestry.io/policy-details"] = strings.Join(policyDetails, ";")
	} else {
		annotations[AnnotationPoliciesApplied] = ""
	}

	r.Log.V(1).Info("Built virtual node annotations", "physicalNode", physicalNode.Name, "annotationCount", len(annotations))

	return annotations
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
	// Add index for pods by node name to efficiently query pods running on a specific node
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
		pod := rawObj.(*corev1.Pod)
		if pod.Spec.NodeName == "" {
			return nil
		}
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return fmt.Errorf("failed to setup pod index by node name: %w", err)
	}

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
