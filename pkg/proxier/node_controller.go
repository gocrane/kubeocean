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

package proxier

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

// NodeController reconciles Node objects for proxier
type NodeController struct {
	client.Client
	Scheme             *runtime.Scheme
	Log                logr.Logger
	ClusterBindingName string
	CurrentNodes       map[string]NodeInfo
	metricsCollector   NodeEventHandler // Direct reference to MetricsCollector
	mu                 sync.RWMutex
}

//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *NodeController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("node", req.NamespacedName)

	// Get the node
	node := &corev1.Node{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		if client.IgnoreNotFound(err) != nil {
			logger.Error(err, "unable to fetch Node")
			return ctrl.Result{}, err
		}

		// Node was deleted, remove from our cache
		r.handleNodeDelete(req.Name)
		return ctrl.Result{}, nil
	}

	// Check if we should process this node
	if !r.shouldProcessNode(node) {
		return ctrl.Result{}, nil
	}

	// Process the node
	r.handleNodeUpdate(node)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *NodeController) SetupWithManager(mgr ctrl.Manager) error {
	// Create predicate to filter nodes
	nodePredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		node := obj.(*corev1.Node)
		return r.shouldProcessNode(node)
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
		}).
		WithEventFilter(nodePredicate).
		Complete(r)
}

// shouldProcessNode checks if the node should be processed
func (r *NodeController) shouldProcessNode(node *corev1.Node) bool {
	// Check if managed by kubeocean
	if node.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		return false
	}

	// Check if belongs to current cluster binding
	if node.Labels[cloudv1beta1.LabelClusterBinding] != r.ClusterBindingName {
		return false
	}

	return true
}

// extractNodeInfo extracts node information from a Node object
func (r *NodeController) extractNodeInfo(node *corev1.Node) (*NodeInfo, error) {
	// Get InternalIP from node label first
	internalIP := node.Labels[cloudv1beta1.LabelPhysicalNodeInnerIP]

	// If label doesn't exist, fallback to Status.Addresses (for backward compatibility)
	if internalIP == "" {
		r.Log.V(1).Info("No physical-node-innerip label found, falling back to Status.Addresses",
			"node", node.Name)

		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				internalIP = addr.Address
				break
			}
		}

		if internalIP == "" {
			return nil, fmt.Errorf("no InternalIP found for node %s (neither in label nor Status.Addresses)", node.Name)
		}

		r.Log.Info("Using InternalIP from Status.Addresses",
			"node", node.Name, "internalIP", internalIP)
	}

	// Get proxier_port label
	proxierPort := node.Labels[cloudv1beta1.LabelProxierPort]
	if proxierPort == "" {
		// Use default port
		proxierPort = "8080"
		r.Log.V(1).Info("No LabelProxierPort label found, using default port",
			"node", node.Name, "defaultPort", proxierPort)
	}

	return &NodeInfo{
		InternalIP:  internalIP,
		ProxierPort: proxierPort,
	}, nil
}

// handleNodeUpdate handles node add/update
func (r *NodeController) handleNodeUpdate(node *corev1.Node) {
	nodeInfo, err := r.extractNodeInfo(node)
	if err != nil {
		r.Log.Error(err, "Failed to extract node info", "node", node.Name)
		return
	}

	r.mu.Lock()
	oldNodeInfo, existed := r.CurrentNodes[node.Name]
	r.CurrentNodes[node.Name] = *nodeInfo
	r.mu.Unlock()

	// Update VNodePortMapper
	if !existed {
		// Node added - add to mapper
		r.Log.Info("Adding new VNode to port mapper", "vnode", node.Name, "port", nodeInfo.ProxierPort, "nodeInfo", nodeInfo)
		VNodePortMapper.AddVNodePort(node.Name, nodeInfo.ProxierPort)
		r.Log.Info("Successfully added VNode to port mapper", "vnode", node.Name, "port", nodeInfo.ProxierPort)
	} else if oldNodeInfo.ProxierPort != nodeInfo.ProxierPort {
		// Port changed - update mapper
		r.Log.Info("Updating VNode port in mapper", "vnode", node.Name, "oldPort", oldNodeInfo.ProxierPort, "newPort", nodeInfo.ProxierPort, "nodeInfo", nodeInfo)
		VNodePortMapper.AddVNodePort(node.Name, nodeInfo.ProxierPort)
		r.Log.Info("Successfully updated VNode port in mapper", "vnode", node.Name, "oldPort", oldNodeInfo.ProxierPort, "newPort", nodeInfo.ProxierPort)
	} else {
		r.Log.V(1).Info("VNode port unchanged, no mapper update needed", "vnode", node.Name, "port", nodeInfo.ProxierPort)
	}

	// Call MetricsCollector directly
	if r.metricsCollector != nil {
		if !existed {
			r.Log.Info("Node added to proxier", "node", node.Name, "nodeInfo", nodeInfo)
			r.metricsCollector.OnNodeAdded(node.Name, *nodeInfo)
		} else if oldNodeInfo != *nodeInfo {
			r.Log.Info("Node updated in proxier", "node", node.Name, "oldNodeInfo", oldNodeInfo, "newNodeInfo", nodeInfo)
			r.metricsCollector.OnNodeUpdated(node.Name, oldNodeInfo, *nodeInfo)
		}
	} else {
		// If no MetricsCollector, only log
		if !existed {
			r.Log.Info("Node added to proxier (no metrics collector)", "node", node.Name, "nodeInfo", nodeInfo)
		} else if oldNodeInfo != *nodeInfo {
			r.Log.Info("Node updated in proxier (no metrics collector)", "node", node.Name, "oldNodeInfo", oldNodeInfo, "newNodeInfo", nodeInfo)
		}
	}
}

// handleNodeDelete handles node deletion
func (r *NodeController) handleNodeDelete(nodeName string) {
	r.mu.Lock()
	oldNodeInfo, existed := r.CurrentNodes[nodeName]
	delete(r.CurrentNodes, nodeName)
	r.mu.Unlock()

	if existed {
		// Remove from VNodePortMapper
		r.Log.Info("Removing VNode from port mapper", "vnode", nodeName, "nodeInfo", oldNodeInfo)
		VNodePortMapper.RemoveVNodePort(nodeName)
		r.Log.Info("Successfully removed VNode from port mapper", "vnode", nodeName)

		r.Log.Info("Node removed from proxier", "node", nodeName, "nodeInfo", oldNodeInfo)

		// Call MetricsCollector directly
		if r.metricsCollector != nil {
			r.metricsCollector.OnNodeDeleted(nodeName, oldNodeInfo)
		}
	}
}

// SyncExistingNodes syncs existing nodes on startup
func (r *NodeController) SyncExistingNodes(ctx context.Context) error {
	nodeList := &corev1.NodeList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		cloudv1beta1.LabelClusterBinding: r.ClusterBindingName,
		cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
	})

	if err := r.List(ctx, nodeList, client.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear existing nodes
	r.CurrentNodes = make(map[string]NodeInfo)

	for _, node := range nodeList.Items {
		if nodeInfo, err := r.extractNodeInfo(&node); err == nil {
			r.CurrentNodes[node.Name] = *nodeInfo
		} else {
			r.Log.Error(err, "Failed to extract node info during sync", "node", node.Name)
		}
	}

	r.Log.Info("Synced existing nodes for proxier", "nodeCount", len(r.CurrentNodes))

	// Initialize VNodePortMapper with existing nodes
	VNodePortMapper.Clear() // Clear any existing mappings
	for nodeName, nodeInfo := range r.CurrentNodes {
		VNodePortMapper.AddVNodePort(nodeName, nodeInfo.ProxierPort)
		r.Log.Info("Initialized VNode in port mapper", "vnode", nodeName, "port", nodeInfo.ProxierPort)
	}

	// Log final VNodePortMapper state
	r.Log.Info("VNodePortMapper initialized", "totalVNodes", len(r.CurrentNodes))

	// If MetricsCollector exists, initialize it directly
	if r.metricsCollector != nil {
		// Create a copy of node states
		nodeStates := make(map[string]NodeInfo)
		for k, v := range r.CurrentNodes {
			nodeStates[k] = v
		}

		// Initialize MetricsCollector
		if collector, ok := r.metricsCollector.(*MetricsCollector); ok {
			collector.InitializeWithNodes(nodeStates)
		}
	}

	return nil
}

// GetCurrentNodes returns a copy of current nodes (for testing/debugging)
func (r *NodeController) GetCurrentNodes() map[string]NodeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]NodeInfo)
	for k, v := range r.CurrentNodes {
		result[k] = v
	}
	return result
}

// SetMetricsCollector sets the MetricsCollector reference
func (r *NodeController) SetMetricsCollector(collector NodeEventHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metricsCollector = collector
}
