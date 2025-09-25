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

package topdown

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
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

	// Rate limiting for patch operations
	patchLimiter  chan struct{}
	lastPatchTime time.Time
	patchMutex    sync.RWMutex

	// Track current Proxier IP to avoid unnecessary updates
	currentProxierIP string
	ipMutex          sync.RWMutex
}

// NewProxierWatchController creates a new ProxierWatchController
func NewProxierWatchController(virtualClient client.Client, scheme *runtime.Scheme, log logr.Logger, clusterBinding *cloudv1beta1.ClusterBinding) *ProxierWatchController {
	return &ProxierWatchController{
		VirtualClient:  virtualClient,
		Scheme:         scheme,
		Log:            log,
		ClusterBinding: clusterBinding,
		patchLimiter:   make(chan struct{}, 1), // Allow only 1 concurrent patch operation
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
			// Pod was deleted, no need to handle as there's no IP available
			log.V(1).Info("Proxier pod deleted, no action needed")
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch Proxier pod")
		return ctrl.Result{}, err
	}

	// Check if this is a Proxier pod for our cluster binding
	if !r.isProxierPodForClusterBinding(pod) {
		log.V(1).Info("Pod is not a Proxier pod for this cluster binding, skipping")
		return ctrl.Result{}, nil
	}

	// Check if pod is running and has an IP
	if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
		log.V(1).Info("Proxier pod is not running or has no IP, skipping",
			"phase", pod.Status.Phase, "podIP", pod.Status.PodIP)
		return ctrl.Result{}, nil
	}

	// Check if IP has actually changed
	newIP := pod.Status.PodIP
	r.ipMutex.RLock()
	currentIP := r.currentProxierIP
	r.ipMutex.RUnlock()

	if currentIP == newIP {
		log.V(1).Info("Proxier pod IP has not changed, skipping update", "podIP", newIP)
		return ctrl.Result{}, nil
	}

	// Check if current IP belongs to any running Proxier pod
	if currentIP != "" && r.isCurrentIPValidProxierPod(ctx, currentIP) {
		log.V(1).Info("Current IP belongs to a running Proxier pod, no update needed",
			"currentIP", currentIP, "newIP", newIP)
		return ctrl.Result{}, nil
	}

	// Update the current IP and handle the change
	r.ipMutex.Lock()
	r.currentProxierIP = newIP
	r.ipMutex.Unlock()

	// Handle Proxier pod IP change
	return r.handleProxierPodIPChange(ctx, log, pod)
}

// SetupWithManager sets up the controller with the Manager
func (r *ProxierWatchController) SetupWithManager(mgr ctrl.Manager) error {
	// Create a predicate to filter Proxier pods
	proxierPredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return false
		}
		return r.isProxierPodForClusterBinding(pod)
	})

	// Create a controller that watches Pods with Proxier labels
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(proxierPredicate).
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

// handleProxierPodIPChange handles when a Proxier pod IP changes
func (r *ProxierWatchController) handleProxierPodIPChange(ctx context.Context, log logr.Logger, pod *corev1.Pod) (ctrl.Result, error) {
	newIP := pod.Status.PodIP
	log.Info("Proxier pod IP changed, starting VNode update", "podName", pod.Name, "podIP", newIP)

	// Rate limiting: acquire patch limiter
	select {
	case r.patchLimiter <- struct{}{}:
		defer func() { <-r.patchLimiter }()
	case <-ctx.Done():
		return ctrl.Result{}, ctx.Err()
	}

	// Additional rate limiting: check if we've patched recently
	r.patchMutex.Lock()
	timeSinceLastPatch := time.Since(r.lastPatchTime)
	if timeSinceLastPatch < 5*time.Second {
		r.patchMutex.Unlock()
		log.Info("Rate limiting: too soon since last patch, skipping", "timeSinceLastPatch", timeSinceLastPatch)
		return ctrl.Result{}, nil
	}
	r.lastPatchTime = time.Now()
	r.patchMutex.Unlock()

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

	// Update VNode IPs with rate limiting between patches
	updatedCount := 0
	for i, vNode := range vNodes {
		if err := r.updateVNodeIP(ctx, log, &vNode, newIP); err != nil {
			log.Error(err, "Failed to update VNode IP", "vNodeName", vNode.Name)
			// Continue with other VNodes even if one fails
			continue
		}
		updatedCount++

		// Add small delay between patches to avoid overwhelming the API server
		if i < len(vNodes)-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	log.Info("VNode IP update completed",
		"totalVNodes", len(vNodes),
		"updatedVNodes", updatedCount,
		"newProxierIP", newIP)

	return ctrl.Result{}, nil
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

// verifyVNodeIPUpdateWithRetry verifies VNode IP update with retry mechanism
func (r *ProxierWatchController) verifyVNodeIPUpdateWithRetry(ctx context.Context, log logr.Logger, vNodeName, expectedIP, oldIP string) bool {
	const maxRetries = 5
	const baseDelay = 200 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Calculate delay with exponential backoff
		delay := baseDelay * time.Duration(1<<attempt) // 200ms, 400ms, 800ms, 1.6s, 3.2s
		if attempt > 0 {
			log.V(1).Info("Retrying VNode IP verification",
				"vNodeName", vNodeName,
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"delay", delay)
			time.Sleep(delay)
		}

		// Fetch the updated node
		updatedNode := &corev1.Node{}
		err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: vNodeName}, updatedNode)
		if err != nil {
			log.Error(err, "Failed to fetch VNode for verification",
				"vNodeName", vNodeName,
				"attempt", attempt+1)
			continue
		}

		// Check the current IP
		actualIP := r.getVNodeInternalIP(updatedNode)
		log.V(1).Info("VNode IP verification attempt",
			"vNodeName", vNodeName,
			"attempt", attempt+1,
			"expectedIP", expectedIP,
			"actualIP", actualIP,
			"allAddresses", r.getAllAddresses(updatedNode))

		if actualIP == expectedIP {
			log.Info("VNode IP update verified successfully",
				"vNodeName", vNodeName,
				"oldIP", oldIP,
				"newIP", expectedIP,
				"attempts", attempt+1)
			return true
		}

		// If this is the last attempt, log the final failure
		if attempt == maxRetries-1 {
			log.Info("VNode IP update verification failed after all retries",
				"vNodeName", vNodeName,
				"expectedIP", expectedIP,
				"actualIP", actualIP,
				"attempts", maxRetries,
				"note", "This may be due to API server cache delay or update failure")
		}
	}

	return false
}

// updateVNodeIPWithRetry updates VNode IP using Status().Update() mechanism with retry
func (r *ProxierWatchController) updateVNodeIPWithRetry(ctx context.Context, log logr.Logger, vNodeName, newIP, oldIP string) error {
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

		// Get the latest VNode object
		updatedNode := &corev1.Node{}
		err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: vNodeName}, updatedNode)
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
		currentIP := r.getVNodeInternalIP(updatedNode)
		if currentIP == newIP {
			log.V(1).Info("VNode IP is already correct, no update needed",
				"vNodeName", vNodeName,
				"currentIP", currentIP,
				"attempt", attempt+1)
			return nil
		}

		// Update the InternalIP
		updated := r.updateVNodeInternalIP(updatedNode, newIP)
		if !updated {
			log.V(1).Info("No InternalIP found to update, adding new one",
				"vNodeName", vNodeName,
				"newIP", newIP,
				"attempt", attempt+1)
		}

		// Apply the status update
		log.V(1).Info("Applying VNode status update",
			"vNodeName", vNodeName,
			"oldIP", currentIP,
			"newIP", newIP,
			"attempt", attempt+1)

		err = r.VirtualClient.Status().Update(ctx, updatedNode)
		if err != nil {
			// Check if it's a conflict error (ResourceVersion mismatch)
			if isConflictError(err) {
				log.V(1).Info("VNode update conflict, will retry with latest version",
					"vNodeName", vNodeName,
					"attempt", attempt+1,
					"error", err.Error())
				continue
			}

			// Check if it's a forbidden error (permission issue)
			if isForbiddenError(err) {
				log.Error(err, "VNode status update forbidden, likely permission issue",
					"vNodeName", vNodeName,
					"attempt", attempt+1)
				return fmt.Errorf("VNode status update forbidden: %w", err)
			}

			// Other errors
			log.Error(err, "Failed to update VNode status",
				"vNodeName", vNodeName,
				"attempt", attempt+1)
			if attempt == maxRetries-1 {
				return fmt.Errorf("failed to update VNode after %d attempts: %w", maxRetries, err)
			}
			continue
		}

		// Status update successful
		log.Info("VNode status update successful",
			"vNodeName", vNodeName,
			"oldIP", currentIP,
			"newIP", newIP,
			"attempts", attempt+1)
		return nil
	}

	return fmt.Errorf("failed to update VNode after %d attempts", maxRetries)
}

// updateVNodeInternalIP updates the InternalIP in the VNode object
func (r *ProxierWatchController) updateVNodeInternalIP(vNode *corev1.Node, newIP string) bool {
	// Find and update existing InternalIP
	for i, addr := range vNode.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			vNode.Status.Addresses[i].Address = newIP
			return true
		}
	}

	// If no InternalIP found, add it
	vNode.Status.Addresses = append(vNode.Status.Addresses, corev1.NodeAddress{
		Type:    corev1.NodeInternalIP,
		Address: newIP,
	})
	return false
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

// isCurrentIPValidProxierPod checks if the current IP belongs to any running Proxier pod
func (r *ProxierWatchController) isCurrentIPValidProxierPod(ctx context.Context, currentIP string) bool {
	// List all Proxier pods for this cluster binding
	podList := &corev1.PodList{}
	err := r.VirtualClient.List(ctx, podList, client.MatchingLabels{
		ProxierComponentLabel: ProxierComponentValue,
		ProxierNameLabel:      ProxierNameValue,
		ProxierManagedByLabel: ProxierManagedByValue,
		ProxierInstanceLabel:  r.ClusterBinding.Name,
	})

	if err != nil {
		r.Log.Error(err, "Failed to list Proxier pods for IP validation")
		return false
	}

	// Check if any running Proxier pod has the current IP
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP == currentIP {
			r.Log.V(1).Info("Found running Proxier pod with current IP",
				"podName", pod.Name,
				"podIP", pod.Status.PodIP,
				"phase", pod.Status.Phase)
			return true
		}
	}

	r.Log.V(1).Info("No running Proxier pod found with current IP",
		"currentIP", currentIP,
		"totalPods", len(podList.Items))
	return false
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
