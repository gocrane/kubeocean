// Copyright 2024 The Kubeocean Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package topproxier

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
)

const (
	// Proxier pod labels
	ProxierComponentLabel = "app.kubernetes.io/component"
	ProxierNameLabel      = "app.kubernetes.io/name"
	ProxierInstanceLabel  = "app.kubernetes.io/instance"
	ProxierManagedByLabel = "app.kubernetes.io/managed-by"

	// Proxier label values
	ProxierComponentValue = "proxier"
	ProxierNameValue      = "kubeocean-proxier"
	ProxierManagedByValue = "kubeocean-manager"

	// VNode labels for filtering
	VNodeClusterBindingLabel = "kubeocean.io/cluster-binding"
	VNodeManagedByLabel      = "kubeocean.io/managed-by"
	VNodeManagedByValue      = "kubeocean"
)

// ProxierWatchController watches Proxier pods and updates VNode IPs when Proxier IP changes
type ProxierWatchController struct {
	VirtualClient  client.Client
	Scheme         *runtime.Scheme
	Log            logr.Logger
	ClusterBinding *cloudv1beta1.ClusterBinding

	// Rate limiting for patch operations using golang.org/x/time/rate
	rateLimiter *rate.Limiter // QPS limiter for VNode updates

	// PrometheusVNodeBasePort is the base port used in VNode Prometheus URL annotations
	PrometheusVNodeBasePort int
}

// NewProxierWatchController creates a new ProxierWatchController
func NewProxierWatchController(virtualClient client.Client, scheme *runtime.Scheme, log logr.Logger, clusterBinding *cloudv1beta1.ClusterBinding, prometheusVNodeBasePort int) *ProxierWatchController {
	// Create rate limiter: 20 QPS with burst of 5
	// This allows up to 20 VNode updates per second with a small burst capacity
	limiter := rate.NewLimiter(20, 5)

	return &ProxierWatchController{
		VirtualClient:           virtualClient,
		Scheme:                  scheme,
		Log:                     log,
		ClusterBinding:          clusterBinding,
		rateLimiter:             limiter,
		PrometheusVNodeBasePort: prometheusVNodeBasePort,
	}
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;update;patch

// Reconcile handles Proxier pod events
func (r *ProxierWatchController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("proxierPod", req.NamespacedName)

	// Fetch the Proxier Pod
	pod := &corev1.Pod{}
	err := r.VirtualClient.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Pod was deleted - this is the key trigger for updating VNode IPs
			// When a Proxier pod is deleted, we need to update all VNodes to point to a remaining running Proxier
			log.Info("Proxier pod deleted, triggering VNode IP update")
			return r.SyncProxierPod(ctx, log)
		}
		log.Error(err, "unable to fetch Proxier pod")
		return ctrl.Result{}, err
	}

	// Check if this is a Proxier pod for our cluster binding
	if !r.isProxierPodForClusterBinding(pod) {
		log.V(1).Info("Pod is not a Proxier pod for this cluster binding, skipping")
		return ctrl.Result{}, nil
	}

	return r.SyncProxierPod(ctx, log)
}

// SetupWithManager sets up the controller with the Manager
func (r *ProxierWatchController) SetupWithManager(mgr ctrl.Manager) error {
	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("proxierwatch-%s", r.ClusterBinding.Name)

	// Create a predicate to filter Proxier pods with specific event handling
	proxierPredicate := predicate.Funcs{
		// Create event: only enqueue if pod has PodIP
		CreateFunc: func(e event.CreateEvent) bool {
			pod, ok := e.Object.(*corev1.Pod)
			if !ok {
				return false
			}
			if !r.isProxierPodForClusterBinding(pod) {
				return false
			}
			// Only enqueue if pod has an IP
			return pod.Status.PodIP != ""
		},
		// Update event: only enqueue if old pod has no IP and new pod has IP
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok := e.ObjectOld.(*corev1.Pod)
			if !ok {
				return false
			}
			newPod, ok := e.ObjectNew.(*corev1.Pod)
			if !ok {
				return false
			}
			if !r.isProxierPodForClusterBinding(newPod) {
				return false
			}
			// Only enqueue if old pod has no IP and new pod has IP
			return oldPod.Status.PodIP == "" && newPod.Status.PodIP != ""
		},
		// Delete event: always enqueue
		DeleteFunc: func(e event.DeleteEvent) bool {
			pod, ok := e.Object.(*corev1.Pod)
			if !ok {
				return false
			}
			return r.isProxierPodForClusterBinding(pod)
		},
		// Generic events: no special handling needed
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}

	// Create a controller that watches Pods with Proxier labels
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(proxierPredicate).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1, // Process one at a time to avoid race conditions
		}).
		Complete(r)
}

// isProxierPodForClusterBinding checks if the pod is a Proxier pod for this cluster binding
func (r *ProxierWatchController) isProxierPodForClusterBinding(pod *corev1.Pod) bool {
	if pod.Labels == nil {
		return false
	}

	// Check if it's a Proxier pod
	component, hasComponent := pod.Labels[ProxierComponentLabel]
	name, hasName := pod.Labels[ProxierNameLabel]
	managedBy, hasManagedBy := pod.Labels[ProxierManagedByLabel]
	instance, hasInstance := pod.Labels[ProxierInstanceLabel]

	if !hasComponent || component != ProxierComponentValue {
		return false
	}
	if !hasName || name != ProxierNameValue {
		return false
	}
	if !hasManagedBy || managedBy != ProxierManagedByValue {
		return false
	}
	if !hasInstance || instance != r.ClusterBinding.Name {
		return false
	}

	return true
}

// SyncProxierPod syncs Proxier pod to VNode IPs
// This is the main trigger for VNode IP updates during rolling updates
func (r *ProxierWatchController) SyncProxierPod(ctx context.Context, log logr.Logger) (ctrl.Result, error) {
	log.Info("Syncing Proxier pod to VNode IPs")

	// Get all VNodes for this cluster binding
	vNodes, err := r.getVNodesForClusterBinding(ctx)
	if err != nil {
		log.Error(err, "Failed to get VNodes for cluster binding")
		return ctrl.Result{}, err
	}

	if len(vNodes) == 0 {
		log.Info("No VNodes found for cluster binding, nothing to update")
		return ctrl.Result{}, nil
	}

	// Get all currently running Proxier Pod IPs (excluding the deleted one)
	runningProxierIPs, err := r.getRunningProxierPodIPs(ctx)
	if err != nil {
		log.Error(err, "Failed to get running Proxier Pod IPs")
		return ctrl.Result{}, err
	}

	if len(runningProxierIPs) == 0 {
		log.Error(fmt.Errorf("no running Proxier pods found"), "Cannot update VNode IPs, no available Proxier")
		return ctrl.Result{}, fmt.Errorf("no running Proxier pods available")
	}

	// Pick the first available running Proxier IP (they're all equivalent)
	var targetIP string
	for ip := range runningProxierIPs {
		targetIP = ip
		break
	}

	log.Info("Found running Proxier Pod IPs after deletion",
		"remainingProxierIPs", runningProxierIPs,
		"count", len(runningProxierIPs),
		"targetIP", targetIP)

	// Update VNode IPs with rate limiting
	updatedCount := 0
	skippedCount := 0
	for _, vNode := range vNodes {
		// Check if VNode's current InternalIP is already a valid running Proxier Pod IP
		currentIP := r.getVNodeInternalIP(&vNode)
		if currentIP != "" && runningProxierIPs[currentIP] {
			log.V(1).Info("VNode InternalIP is already a valid running Proxier Pod IP, skipping update",
				"vNodeName", vNode.Name,
				"currentIP", currentIP)
			skippedCount++
			continue
		}

		// VNode's current IP is invalid (points to deleted Proxier), update it
		log.Info("VNode IP needs update",
			"vNodeName", vNode.Name,
			"currentIP", currentIP,
			"targetIP", targetIP,
			"reason", "current IP is not in running Proxier list")

		// Apply rate limiting before each update (20 QPS)
		if err := r.rateLimiter.Wait(ctx); err != nil {
			log.Error(err, "Rate limiter wait failed", "vNodeName", vNode.Name)
			return ctrl.Result{}, err
		}

		if err := r.updateVNodeIP(ctx, log, &vNode, targetIP); err != nil {
			log.Error(err, "Failed to update VNode IP", "vNodeName", vNode.Name)
			// Continue with other VNodes even if one fails
			continue
		}
		updatedCount++
	}

	log.Info("VNode IP update completed after Proxier deletion",
		"totalVNodes", len(vNodes),
		"updatedVNodes", updatedCount,
		"skippedVNodes", skippedCount,
		"targetProxierIP", targetIP)

	return ctrl.Result{}, nil
}

// getRunningProxierPodIPs gets all running Proxier Pod IPs for this cluster binding and returns them as a set
func (r *ProxierWatchController) getRunningProxierPodIPs(ctx context.Context) (map[string]bool, error) {
	// List all Proxier pods for this cluster binding
	podList := &corev1.PodList{}
	err := r.VirtualClient.List(ctx, podList, client.MatchingLabels{
		ProxierComponentLabel: ProxierComponentValue,
		ProxierNameLabel:      ProxierNameValue,
		ProxierManagedByLabel: ProxierManagedByValue,
		ProxierInstanceLabel:  r.ClusterBinding.Name,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list Proxier pods: %w", err)
	}

	// Create a set (map) of running Proxier Pod IPs
	runningIPs := make(map[string]bool)
	for _, pod := range podList.Items {
		// Only include pods that are running and have an IP
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			runningIPs[pod.Status.PodIP] = true
			r.Log.V(1).Info("Found running Proxier pod",
				"podName", pod.Name,
				"podIP", pod.Status.PodIP,
				"phase", pod.Status.Phase)
		}
	}

	r.Log.V(1).Info("Collected running Proxier Pod IPs",
		"totalPods", len(podList.Items),
		"runningPods", len(runningIPs),
		"runningIPs", runningIPs)

	return runningIPs, nil
}

// getVNodesForClusterBinding gets all VNodes for this cluster binding
func (r *ProxierWatchController) getVNodesForClusterBinding(ctx context.Context) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	err := r.VirtualClient.List(ctx, nodeList, client.MatchingLabels{
		VNodeClusterBindingLabel: r.ClusterBinding.Name,
		VNodeManagedByLabel:      VNodeManagedByValue,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list VNodes: %w", err)
	}

	return nodeList.Items, nil
}

// updateVNodeIP updates a VNode's InternalIP to point to the new Proxier pod IP
func (r *ProxierWatchController) updateVNodeIP(ctx context.Context, log logr.Logger, vNode *corev1.Node, newProxierIP string) error {
	// Check if the VNode already has the correct IP
	currentIP := r.getVNodeInternalIP(vNode)
	log.V(1).Info("Checking VNode IP update",
		"vNodeName", vNode.Name,
		"currentIP", currentIP,
		"targetIP", newProxierIP,
		"allAddresses", r.getAllAddresses(vNode))

	if currentIP == newProxierIP {
		log.V(1).Info("VNode already has correct IP, skipping update",
			"vNodeName", vNode.Name, "currentIP", currentIP)
		return nil
	}

	// Update VNode using Update mechanism with retry
	err := r.updateVNodeIPWithRetry(ctx, log, vNode.Name, newProxierIP, currentIP)
	if err != nil {
		log.Error(err, "Failed to update VNode IP after all retries",
			"vNodeName", vNode.Name,
			"oldIP", currentIP,
			"newIP", newProxierIP)
		return fmt.Errorf("failed to update VNode %s: %w", vNode.Name, err)
	}

	log.Info("Updated VNode IP",
		"vNodeName", vNode.Name,
		"oldIP", currentIP,
		"newIP", newProxierIP)

	return nil
}

// getVNodeInternalIP gets the current InternalIP of a VNode
func (r *ProxierWatchController) getVNodeInternalIP(vNode *corev1.Node) string {
	for _, addr := range vNode.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}

// createVNodeIPPatch creates a JSON Patch to update VNode InternalIP
func (r *ProxierWatchController) createVNodeIPPatch(vNode *corev1.Node, newIP string) []byte {
	// Find existing InternalIP index
	internalIPIndex := -1
	for i, addr := range vNode.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			internalIPIndex = i
			break
		}
	}

	var patch []map[string]interface{}

	if internalIPIndex >= 0 {
		// Update existing InternalIP using JSON Patch
		patch = []map[string]interface{}{
			{
				"op":    "replace",
				"path":  fmt.Sprintf("/status/addresses/%d/address", internalIPIndex),
				"value": newIP,
			},
		}
	} else {
		// Add new InternalIP using JSON Patch
		patch = []map[string]interface{}{
			{
				"op":   "add",
				"path": "/status/addresses/-",
				"value": map[string]string{
					"type":    "InternalIP",
					"address": newIP,
				},
			},
		}
	}

	// Convert to JSON
	jsonData, err := json.Marshal(patch)
	if err != nil {
		// Fallback to simple replace operation if JSON marshaling fails
		return []byte(fmt.Sprintf(`[
			{
				"op": "replace",
				"path": "/status/addresses/0/address",
				"value": "%s"
			}
		]`, newIP))
	}

	return jsonData
}

// updateVNodeIPWithRetry updates VNode IP using Status().Patch() mechanism with retry
// Uses JSON Patch for higher success rate and better conflict handling
func (r *ProxierWatchController) updateVNodeIPWithRetry(ctx context.Context, log logr.Logger, vNodeName, newIP, _ string) error {
	const maxRetries = 3
	const baseDelay = 100 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Calculate delay with exponential backoff
		delay := baseDelay * time.Duration(1<<attempt) // 100ms, 200ms, 400ms
		if attempt > 0 {
			log.V(1).Info("Retrying VNode IP update",
				"vNodeName", vNodeName,
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"delay", delay)
			time.Sleep(delay)
		}

		// Get the latest VNode object to check current state
		currentNode := &corev1.Node{}
		err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: vNodeName}, currentNode)
		if err != nil {
			log.Error(err, "Failed to fetch VNode for update",
				"vNodeName", vNodeName,
				"attempt", attempt+1)
			if attempt == maxRetries-1 {
				return fmt.Errorf("failed to fetch VNode after %d attempts: %w", maxRetries, err)
			}
			continue
		}

		// Check if IP is already correct
		currentIP := r.getVNodeInternalIP(currentNode)
		if currentIP == newIP {
			log.V(1).Info("VNode IP is already correct, no update needed",
				"vNodeName", vNodeName,
				"currentIP", currentIP,
				"attempt", attempt+1)
			return nil
		}

		// Step 1: Update annotations using regular Patch (if needed)
		needsAnnotationUpdate := false
		var newPrometheusURL string

		if proxierPort, exists := currentNode.Labels[cloudv1beta1.LabelProxierPort]; exists {
			newPrometheusURL = fmt.Sprintf("%s:%s/%s", newIP, proxierPort, vNodeName)

			if currentNode.Annotations == nil {
				needsAnnotationUpdate = true
			} else if existingURL, hasAnnotation := currentNode.Annotations[cloudv1beta1.AnnotationPrometheusURL]; !hasAnnotation || existingURL != newPrometheusURL {
				needsAnnotationUpdate = true
			}
		}

		if needsAnnotationUpdate {
			// Create annotation patch
			annotationPatch := map[string]interface{}{
				"metadata": map[string]interface{}{
					"annotations": map[string]string{
						cloudv1beta1.AnnotationPrometheusURL: newPrometheusURL,
					},
				},
			}
			annotationPatchData, err := json.Marshal(annotationPatch)
			if err != nil {
				log.Error(err, "Failed to marshal annotation patch", "vNodeName", vNodeName)
			} else {
				err = r.VirtualClient.Patch(ctx, currentNode, client.RawPatch(types.MergePatchType, annotationPatchData))
				if err != nil {
					log.Error(err, "Failed to patch VNode annotations", "vNodeName", vNodeName, "attempt", attempt+1)
					// Continue to status patch even if annotation patch fails
				} else {
					log.V(1).Info("Successfully patched VNode prometheus URL annotation",
						"vNodeName", vNodeName,
						"prometheusURL", newPrometheusURL,
						"attempt", attempt+1)
				}
			}
		}

		// Step 2: Update status.addresses using Status().Patch()
		// Create JSON Patch for InternalIP update
		statusPatchData := r.createVNodeIPPatch(currentNode, newIP)

		log.V(1).Info("Applying VNode status patch",
			"vNodeName", vNodeName,
			"oldIP", currentIP,
			"newIP", newIP,
			"patchData", string(statusPatchData),
			"attempt", attempt+1)

		err = r.VirtualClient.Status().Patch(ctx, currentNode, client.RawPatch(types.JSONPatchType, statusPatchData))
		if err != nil {
			// Check if it's a conflict error (ResourceVersion mismatch)
			if isConflictError(err) {
				log.V(1).Info("VNode status patch conflict, will retry with latest version",
					"vNodeName", vNodeName,
					"attempt", attempt+1,
					"error", err.Error())
				continue
			}

			// Check if it's a forbidden error (permission issue)
			if isForbiddenError(err) {
				log.Error(err, "VNode status patch forbidden, likely permission issue",
					"vNodeName", vNodeName,
					"attempt", attempt+1)
				return fmt.Errorf("VNode status patch forbidden: %w", err)
			}

			// Other errors
			log.Error(err, "Failed to patch VNode status",
				"vNodeName", vNodeName,
				"attempt", attempt+1)
			if attempt == maxRetries-1 {
				return fmt.Errorf("failed to patch VNode after %d attempts: %w", maxRetries, err)
			}
			continue
		}

		// Status patch successful
		log.Info("VNode status patch successful",
			"vNodeName", vNodeName,
			"oldIP", currentIP,
			"newIP", newIP,
			"attempts", attempt+1)
		return nil
	}

	return fmt.Errorf("failed to patch VNode after %d attempts", maxRetries)
}

// isConflictError checks if the error is a conflict error
func isConflictError(err error) bool {
	if err == nil {
		return false
	}
	// Check for common conflict error patterns
	errStr := err.Error()
	return strings.Contains(errStr, "conflict") ||
		strings.Contains(errStr, "ResourceVersion") ||
		strings.Contains(errStr, "409")
}

// isForbiddenError checks if the error is a forbidden error
func isForbiddenError(err error) bool {
	if err == nil {
		return false
	}
	// Check for common forbidden error patterns
	errStr := err.Error()
	return strings.Contains(errStr, "forbidden") ||
		strings.Contains(errStr, "403") ||
		strings.Contains(errStr, "permission")
}

// getAllAddresses returns all addresses as a string for debugging
func (r *ProxierWatchController) getAllAddresses(vNode *corev1.Node) string {
	if len(vNode.Status.Addresses) == 0 {
		return "[]"
	}

	addresses := make([]string, 0, len(vNode.Status.Addresses))
	for _, addr := range vNode.Status.Addresses {
		addresses = append(addresses, fmt.Sprintf("%s:%s", addr.Type, addr.Address))
	}
	return fmt.Sprintf("[%s]", fmt.Sprintf("%s", addresses))
}
