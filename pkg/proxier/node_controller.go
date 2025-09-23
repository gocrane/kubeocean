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
	"os"
	"path/filepath"
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

// NodeInfo represents a node's information for the nodes.txt file
type NodeInfo struct {
	InternalIP  string
	ProxierPort string
}

func (n NodeInfo) String() string {
	return fmt.Sprintf("%s:%s", n.InternalIP, n.ProxierPort)
}

// NodeController reconciles Node objects for proxier
type NodeController struct {
	client.Client
	Scheme             *runtime.Scheme
	Log                logr.Logger
	ClusterBindingName string
	NodesFile          string
	CurrentNodes       map[string]NodeInfo
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
	// Get InternalIP
	var internalIP string
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			internalIP = addr.Address
			break
		}
	}

	if internalIP == "" {
		return nil, fmt.Errorf("no InternalIP found for node %s", node.Name)
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

	// Log node change
	if !existed {
		r.Log.Info("Node added to proxier", "node", node.Name, "nodeInfo", nodeInfo)
	} else if oldNodeInfo != *nodeInfo {
		r.Log.Info("Node updated in proxier", "node", node.Name, "oldNodeInfo", oldNodeInfo, "newNodeInfo", nodeInfo)
	}

	if err := r.updateNodesFile(); err != nil {
		r.Log.Error(err, "Failed to update nodes file after node update")
	}
}

// handleNodeDelete handles node deletion
func (r *NodeController) handleNodeDelete(nodeName string) {
	r.mu.Lock()
	oldNodeInfo, existed := r.CurrentNodes[nodeName]
	delete(r.CurrentNodes, nodeName)
	r.mu.Unlock()

	if existed {
		r.Log.Info("Node removed from proxier", "node", nodeName, "nodeInfo", oldNodeInfo)
	}

	if err := r.updateNodesFile(); err != nil {
		r.Log.Error(err, "Failed to update nodes file after node delete")
	}
}

// updateNodesFile updates the nodes.txt file with current node information
func (r *NodeController) updateNodesFile() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Atomic write
	tempFile := r.NodesFile + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		r.Log.Error(err, "Failed to create temp file", "tempFile", tempFile, "nodesFile", r.NodesFile)
		return fmt.Errorf("failed to create temp file %s: %w", tempFile, err)
	}
	defer file.Close()

	// Write all current nodes
	for _, nodeInfo := range r.CurrentNodes {
		if _, err := file.WriteString(nodeInfo.String() + "\n"); err != nil {
			r.Log.Error(err, "Failed to write node info", "nodeInfo", nodeInfo)
			return err
		}
	}

	// Atomic rename
	if err := os.Rename(tempFile, r.NodesFile); err != nil {
		r.Log.Error(err, "Failed to rename temp file", "tempFile", tempFile, "finalFile", r.NodesFile)
		return err
	}

	r.Log.Info("Successfully updated nodes file", "nodeCount", len(r.CurrentNodes), "file", r.NodesFile)
	return nil
}

// updateNodesFileUnsafe updates the nodes.txt file without acquiring locks
// This should only be called when the caller already holds the appropriate lock
func (r *NodeController) updateNodesFileUnsafe() error {
	// Atomic write
	tempFile := r.NodesFile + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		r.Log.Error(err, "Failed to create temp file", "tempFile", tempFile, "nodesFile", r.NodesFile)
		return fmt.Errorf("failed to create temp file %s: %w", tempFile, err)
	}
	defer file.Close()

	// Write all current nodes
	for _, nodeInfo := range r.CurrentNodes {
		if _, err := file.WriteString(nodeInfo.String() + "\n"); err != nil {
			r.Log.Error(err, "Failed to write node info", "nodeInfo", nodeInfo)
			return err
		}
	}

	// Atomic rename
	if err := os.Rename(tempFile, r.NodesFile); err != nil {
		r.Log.Error(err, "Failed to rename temp file", "tempFile", tempFile, "finalFile", r.NodesFile)
		return err
	}

	r.Log.Info("Successfully updated nodes file", "nodeCount", len(r.CurrentNodes), "file", r.NodesFile)
	return nil
}

// ValidateNodesFile checks if the nodes file path is writable
func (r *NodeController) ValidateNodesFile() error {
	// Check if the directory exists and is writable
	dir := filepath.Dir(r.NodesFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	
	// Try to create a test file
	testFile := r.NodesFile + ".test"
	file, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("failed to create test file %s: %w", testFile, err)
	}
	file.Close()
	
	// Clean up test file
	os.Remove(testFile)
	
	r.Log.Info("Nodes file path validated", "path", r.NodesFile)
	return nil
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

	return r.updateNodesFileUnsafe()
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
