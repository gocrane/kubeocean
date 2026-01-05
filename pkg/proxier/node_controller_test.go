// Copyright 2025 The Kubeocean Authors
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
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
)

// mockNodeEventHandler is a mock implementation of NodeEventHandler for testing
type mockNodeEventHandler struct {
	mu              sync.Mutex
	addedNodes      map[string]NodeInfo
	updatedNodes    map[string]NodeInfo
	deletedNodes    map[string]NodeInfo
	initializeNodes map[string]NodeInfo
}

func newMockNodeEventHandler() *mockNodeEventHandler {
	return &mockNodeEventHandler{
		addedNodes:      make(map[string]NodeInfo),
		updatedNodes:    make(map[string]NodeInfo),
		deletedNodes:    make(map[string]NodeInfo),
		initializeNodes: make(map[string]NodeInfo),
	}
}

func (m *mockNodeEventHandler) OnNodeAdded(nodeName string, nodeInfo NodeInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addedNodes[nodeName] = nodeInfo
}

func (m *mockNodeEventHandler) OnNodeUpdated(nodeName string, oldNodeInfo, newNodeInfo NodeInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedNodes[nodeName] = newNodeInfo
}

func (m *mockNodeEventHandler) OnNodeDeleted(nodeName string, nodeInfo NodeInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedNodes[nodeName] = nodeInfo
}

func (m *mockNodeEventHandler) InitializeWithNodes(nodes map[string]NodeInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range nodes {
		m.initializeNodes[k] = v
	}
}

// TestNodeController_Reconcile tests the reconcile method
func TestNodeController_Reconcile(t *testing.T) {
	// Clear VNodePortMapper before tests
	VNodePortMapper.Clear()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name             string
		node             *corev1.Node
		nodeExists       bool
		clusterBinding   string
		expectError      bool
		expectInCache    bool
		expectInMapper   bool
		setupFunc        func(controller *NodeController)
		validateFunc     func(t *testing.T, controller *NodeController)
	}{
		{
			name: "reconcile valid node with IP label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-1",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:              cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding:         "test-binding",
						cloudv1beta1.LabelPhysicalNodeInnerIP:    "192.168.1.10",
						cloudv1beta1.LabelProxierPort:            "8080",
					},
				},
			},
			nodeExists:     true,
			clusterBinding: "test-binding",
			expectError:    false,
			expectInCache:  true,
			expectInMapper: true,
			setupFunc:      func(controller *NodeController) {},
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				nodeInfo, exists := nodes["test-node-1"]
				assert.True(t, exists)
				assert.Equal(t, "192.168.1.10", nodeInfo.InternalIP)
				assert.Equal(t, "8080", nodeInfo.ProxierPort)

				port, exists := VNodePortMapper.GetPortByVNodeName("test-node-1")
				assert.True(t, exists)
				assert.Equal(t, "8080", port)
			},
		},
		{
			name: "reconcile node with IP from Status.Addresses",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-2",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding: "test-binding",
					},
				},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeInternalIP, Address: "192.168.1.20"},
					},
				},
			},
			nodeExists:     true,
			clusterBinding: "test-binding",
			expectError:    false,
			expectInCache:  true,
			expectInMapper: true,
			setupFunc:      func(controller *NodeController) {},
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				nodeInfo, exists := nodes["test-node-2"]
				assert.True(t, exists)
				assert.Equal(t, "192.168.1.20", nodeInfo.InternalIP)
				assert.Equal(t, "8080", nodeInfo.ProxierPort) // default port
			},
		},
		{
			name: "reconcile node without managed-by label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-unmanaged",
					Labels: map[string]string{
						cloudv1beta1.LabelClusterBinding: "test-binding",
					},
				},
			},
			nodeExists:     true,
			clusterBinding: "test-binding",
			expectError:    false,
			expectInCache:  false,
			expectInMapper: false,
			setupFunc:      func(controller *NodeController) {},
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				_, exists := nodes["test-node-unmanaged"]
				assert.False(t, exists)
			},
		},
		{
			name: "reconcile node from different cluster binding",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-other",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding: "other-binding",
					},
				},
			},
			nodeExists:     true,
			clusterBinding: "test-binding",
			expectError:    false,
			expectInCache:  false,
			expectInMapper: false,
			setupFunc:      func(controller *NodeController) {},
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				_, exists := nodes["test-node-other"]
				assert.False(t, exists)
			},
		},
		{
			name: "reconcile deleted node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-deleted",
				},
			},
			nodeExists:     false,
			clusterBinding: "test-binding",
			expectError:    false,
			expectInCache:  false,
			expectInMapper: false,
			setupFunc: func(controller *NodeController) {
				controller.CurrentNodes["test-node-deleted"] = NodeInfo{
					InternalIP:  "192.168.1.30",
					ProxierPort: "8080",
				}
				VNodePortMapper.AddVNodePort("test-node-deleted", "8080")
			},
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				_, exists := nodes["test-node-deleted"]
				assert.False(t, exists)

				_, exists = VNodePortMapper.GetPortByVNodeName("test-node-deleted")
				assert.False(t, exists)
			},
		},
		{
			name: "reconcile node with custom proxier port",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-custom-port",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:           cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding:      "test-binding",
						cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.40",
						cloudv1beta1.LabelProxierPort:         "9090",
					},
				},
			},
			nodeExists:     true,
			clusterBinding: "test-binding",
			expectError:    false,
			expectInCache:  true,
			expectInMapper: true,
			setupFunc:      func(controller *NodeController) {},
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				nodeInfo, exists := nodes["test-node-custom-port"]
				assert.True(t, exists)
				assert.Equal(t, "9090", nodeInfo.ProxierPort)

				port, exists := VNodePortMapper.GetPortByVNodeName("test-node-custom-port")
				assert.True(t, exists)
				assert.Equal(t, "9090", port)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear VNodePortMapper for each test
			VNodePortMapper.Clear()

			// Create fake client
			clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.nodeExists {
				clientBuilder = clientBuilder.WithObjects(tt.node)
			}
			fakeClient := clientBuilder.Build()

			// Create controller
			controller := &NodeController{
				Client:             fakeClient,
				Scheme:             scheme,
				Log:                logr.Discard(),
				ClusterBindingName: tt.clusterBinding,
				CurrentNodes:       make(map[string]NodeInfo),
			}

			// Setup
			if tt.setupFunc != nil {
				tt.setupFunc(controller)
			}

			// Execute reconcile
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: tt.node.Name,
				},
			}

			result, err := controller.Reconcile(context.Background(), req)

			// Validate
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, ctrl.Result{}, result)
			}

			if tt.validateFunc != nil {
				tt.validateFunc(t, controller)
			}
		})
	}
}

// TestNodeController_shouldProcessNode tests the shouldProcessNode method
func TestNodeController_shouldProcessNode(t *testing.T) {
	tests := []struct {
		name           string
		node           *corev1.Node
		clusterBinding string
		expected       bool
	}{
		{
			name: "valid node with all labels",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding: "test-binding",
					},
				},
			},
			clusterBinding: "test-binding",
			expected:       true,
		},
		{
			name: "node without managed-by label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cloudv1beta1.LabelClusterBinding: "test-binding",
					},
				},
			},
			clusterBinding: "test-binding",
			expected:       false,
		},
		{
			name: "node with wrong managed-by value",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:      "other-manager",
						cloudv1beta1.LabelClusterBinding: "test-binding",
					},
				},
			},
			clusterBinding: "test-binding",
			expected:       false,
		},
		{
			name: "node from different cluster binding",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
						cloudv1beta1.LabelClusterBinding: "other-binding",
					},
				},
			},
			clusterBinding: "test-binding",
			expected:       false,
		},
		{
			name: "node without cluster binding label",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
			},
			clusterBinding: "test-binding",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &NodeController{
				ClusterBindingName: tt.clusterBinding,
				Log:                logr.Discard(),
			}

			result := controller.shouldProcessNode(tt.node)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestNodeController_extractNodeInfo tests the extractNodeInfo method
func TestNodeController_extractNodeInfo(t *testing.T) {
	tests := []struct {
		name        string
		node        *corev1.Node
		expectError bool
		expected    *NodeInfo
	}{
		{
			name: "node with IP label and custom port",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.10",
						cloudv1beta1.LabelProxierPort:         "9090",
					},
				},
			},
			expectError: false,
			expected: &NodeInfo{
				InternalIP:  "192.168.1.10",
				ProxierPort: "9090",
			},
		},
		{
			name: "node with IP label and default port",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.10",
					},
				},
			},
			expectError: false,
			expected: &NodeInfo{
				InternalIP:  "192.168.1.10",
				ProxierPort: "8080",
			},
		},
		{
			name: "node with IP from Status.Addresses",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeInternalIP, Address: "192.168.1.20"},
					},
				},
			},
			expectError: false,
			expected: &NodeInfo{
				InternalIP:  "192.168.1.20",
				ProxierPort: "8080",
			},
		},
		{
			name: "node with multiple addresses",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeExternalIP, Address: "10.0.0.1"},
						{Type: corev1.NodeInternalIP, Address: "192.168.1.30"},
						{Type: corev1.NodeHostName, Address: "node1"},
					},
				},
			},
			expectError: false,
			expected: &NodeInfo{
				InternalIP:  "192.168.1.30",
				ProxierPort: "8080",
			},
		},
		{
			name: "node with no IP available",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeHostName, Address: "node1"},
					},
				},
			},
			expectError: true,
			expected:    nil,
		},
		{
			name: "node with label takes precedence",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.100",
					},
				},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeInternalIP, Address: "192.168.1.200"},
					},
				},
			},
			expectError: false,
			expected: &NodeInfo{
				InternalIP:  "192.168.1.100",
				ProxierPort: "8080",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &NodeController{
				Log: logr.Discard(),
			}

			result, err := controller.extractNodeInfo(tt.node)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, tt.expected.InternalIP, result.InternalIP)
				assert.Equal(t, tt.expected.ProxierPort, result.ProxierPort)
			}
		})
	}
}

// TestNodeController_handleNodeUpdate tests the handleNodeUpdate method
func TestNodeController_handleNodeUpdate(t *testing.T) {
	VNodePortMapper.Clear()

	tests := []struct {
		name          string
		node          *corev1.Node
		existingNodes map[string]NodeInfo
		expectAdded   bool
		expectUpdated bool
		validateFunc  func(t *testing.T, controller *NodeController, mock *mockNodeEventHandler)
	}{
		{
			name: "add new node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "new-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.10",
						cloudv1beta1.LabelProxierPort:         "8080",
					},
				},
			},
			existingNodes: map[string]NodeInfo{},
			expectAdded:   true,
			expectUpdated: false,
			validateFunc: func(t *testing.T, controller *NodeController, mock *mockNodeEventHandler) {
				nodes := controller.GetCurrentNodes()
				_, exists := nodes["new-node"]
				assert.True(t, exists)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				_, added := mock.addedNodes["new-node"]
				assert.True(t, added)
			},
		},
		{
			name: "update existing node with port change",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "existing-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.10",
						cloudv1beta1.LabelProxierPort:         "9090",
					},
				},
			},
			existingNodes: map[string]NodeInfo{
				"existing-node": {
					InternalIP:  "192.168.1.10",
					ProxierPort: "8080",
				},
			},
			expectAdded:   false,
			expectUpdated: true,
			validateFunc: func(t *testing.T, controller *NodeController, mock *mockNodeEventHandler) {
				nodes := controller.GetCurrentNodes()
				nodeInfo, exists := nodes["existing-node"]
				assert.True(t, exists)
				assert.Equal(t, "9090", nodeInfo.ProxierPort)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				_, updated := mock.updatedNodes["existing-node"]
				assert.True(t, updated)
			},
		},
		{
			name: "update existing node without changes",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "unchanged-node",
					Labels: map[string]string{
						cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.10",
						cloudv1beta1.LabelProxierPort:         "8080",
					},
				},
			},
			existingNodes: map[string]NodeInfo{
				"unchanged-node": {
					InternalIP:  "192.168.1.10",
					ProxierPort: "8080",
				},
			},
			expectAdded:   false,
			expectUpdated: false,
			validateFunc: func(t *testing.T, controller *NodeController, mock *mockNodeEventHandler) {
				nodes := controller.GetCurrentNodes()
				_, exists := nodes["unchanged-node"]
				assert.True(t, exists)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				assert.Empty(t, mock.addedNodes)
				assert.Empty(t, mock.updatedNodes)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			VNodePortMapper.Clear()

			mock := newMockNodeEventHandler()
			controller := &NodeController{
				Log:                logr.Discard(),
				ClusterBindingName: "test-binding",
				CurrentNodes:       tt.existingNodes,
				vnodeProxierAgent:  mock,
			}

			controller.handleNodeUpdate(tt.node)

			if tt.validateFunc != nil {
				tt.validateFunc(t, controller, mock)
			}
		})
	}
}

// TestNodeController_handleNodeDelete tests the handleNodeDelete method
func TestNodeController_handleNodeDelete(t *testing.T) {
	VNodePortMapper.Clear()

	tests := []struct {
		name          string
		nodeName      string
		existingNodes map[string]NodeInfo
		expectDeleted bool
		validateFunc  func(t *testing.T, controller *NodeController, mock *mockNodeEventHandler)
	}{
		{
			name:     "delete existing node",
			nodeName: "delete-node",
			existingNodes: map[string]NodeInfo{
				"delete-node": {
					InternalIP:  "192.168.1.10",
					ProxierPort: "8080",
				},
			},
			expectDeleted: true,
			validateFunc: func(t *testing.T, controller *NodeController, mock *mockNodeEventHandler) {
				nodes := controller.GetCurrentNodes()
				_, exists := nodes["delete-node"]
				assert.False(t, exists)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				_, deleted := mock.deletedNodes["delete-node"]
				assert.True(t, deleted)

				_, exists = VNodePortMapper.GetPortByVNodeName("delete-node")
				assert.False(t, exists)
			},
		},
		{
			name:          "delete non-existent node",
			nodeName:      "non-existent-node",
			existingNodes: map[string]NodeInfo{},
			expectDeleted: false,
			validateFunc: func(t *testing.T, controller *NodeController, mock *mockNodeEventHandler) {
				nodes := controller.GetCurrentNodes()
				_, exists := nodes["non-existent-node"]
				assert.False(t, exists)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				assert.Empty(t, mock.deletedNodes)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			VNodePortMapper.Clear()

			// Pre-populate VNodePortMapper
			for nodeName, nodeInfo := range tt.existingNodes {
				VNodePortMapper.AddVNodePort(nodeName, nodeInfo.ProxierPort)
			}

			mock := newMockNodeEventHandler()
			controller := &NodeController{
				Log:                logr.Discard(),
				ClusterBindingName: "test-binding",
				CurrentNodes:       tt.existingNodes,
				vnodeProxierAgent:  mock,
			}

			controller.handleNodeDelete(tt.nodeName)

			if tt.validateFunc != nil {
				tt.validateFunc(t, controller, mock)
			}
		})
	}
}

// TestNodeController_SyncExistingNodes tests the SyncExistingNodes method
func TestNodeController_SyncExistingNodes(t *testing.T) {
	VNodePortMapper.Clear()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name          string
		nodes         []*corev1.Node
		expectedCount int
		validateFunc  func(t *testing.T, controller *NodeController)
	}{
		{
			name: "sync multiple nodes",
			nodes: []*corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy:           cloudv1beta1.LabelManagedByValue,
							cloudv1beta1.LabelClusterBinding:      "test-binding",
							cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.10",
							cloudv1beta1.LabelProxierPort:         "8080",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-2",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy:           cloudv1beta1.LabelManagedByValue,
							cloudv1beta1.LabelClusterBinding:      "test-binding",
							cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.20",
							cloudv1beta1.LabelProxierPort:         "8081",
						},
					},
				},
			},
			expectedCount: 2,
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				assert.Equal(t, 2, len(nodes))

				port1, exists := VNodePortMapper.GetPortByVNodeName("node-1")
				assert.True(t, exists)
				assert.Equal(t, "8080", port1)

				port2, exists := VNodePortMapper.GetPortByVNodeName("node-2")
				assert.True(t, exists)
				assert.Equal(t, "8081", port2)
			},
		},
		{
			name:          "sync with no nodes",
			nodes:         []*corev1.Node{},
			expectedCount: 0,
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				assert.Equal(t, 0, len(nodes))
				assert.Equal(t, 0, VNodePortMapper.GetVNodeCount())
			},
		},
		{
			name: "sync nodes from different cluster binding",
			nodes: []*corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-other",
						Labels: map[string]string{
							cloudv1beta1.LabelManagedBy:           cloudv1beta1.LabelManagedByValue,
							cloudv1beta1.LabelClusterBinding:      "other-binding",
							cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1.30",
						},
					},
				},
			},
			expectedCount: 0,
			validateFunc: func(t *testing.T, controller *NodeController) {
				nodes := controller.GetCurrentNodes()
				assert.Equal(t, 0, len(nodes))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			VNodePortMapper.Clear()

			objects := make([]client.Object, len(tt.nodes))
			for i, node := range tt.nodes {
				objects[i] = node
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			controller := &NodeController{
				Client:             fakeClient,
				Scheme:             scheme,
				Log:                logr.Discard(),
				ClusterBindingName: "test-binding",
				CurrentNodes:       make(map[string]NodeInfo),
			}

			err := controller.SyncExistingNodes(context.Background())
			assert.NoError(t, err)

			nodes := controller.GetCurrentNodes()
			assert.Equal(t, tt.expectedCount, len(nodes))

			if tt.validateFunc != nil {
				tt.validateFunc(t, controller)
			}
		})
	}
}

// TestNodeController_GetCurrentNodes tests the GetCurrentNodes method
func TestNodeController_GetCurrentNodes(t *testing.T) {
	controller := &NodeController{
		CurrentNodes: map[string]NodeInfo{
			"node-1": {InternalIP: "192.168.1.10", ProxierPort: "8080"},
			"node-2": {InternalIP: "192.168.1.20", ProxierPort: "8081"},
		},
		Log: logr.Discard(),
	}

	nodes := controller.GetCurrentNodes()
	assert.Equal(t, 2, len(nodes))

	// Verify it's a copy
	delete(nodes, "node-1")
	assert.Equal(t, 2, len(controller.CurrentNodes))
}

// TestNodeController_SetMetricsCollector tests the SetMetricsCollector method
func TestNodeController_SetMetricsCollector(t *testing.T) {
	controller := &NodeController{
		CurrentNodes: make(map[string]NodeInfo),
		Log:          logr.Discard(),
	}

	mock := newMockNodeEventHandler()
	controller.SetMetricsCollector(mock)

	assert.NotNil(t, controller.vnodeProxierAgent)
}

// TestNodeController_ConcurrentAccess tests concurrent access to node controller
func TestNodeController_ConcurrentAccess(t *testing.T) {
	VNodePortMapper.Clear()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Create multiple nodes
	nodes := make([]client.Object, 10)
	for i := 0; i < 10; i++ {
		nodes[i] = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "concurrent-node-" + string(rune('a'+i)),
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy:           cloudv1beta1.LabelManagedByValue,
					cloudv1beta1.LabelClusterBinding:      "test-binding",
					cloudv1beta1.LabelPhysicalNodeInnerIP: "192.168.1." + string(rune('1'+i)),
					cloudv1beta1.LabelProxierPort:         "808" + string(rune('0'+i)),
				},
			},
		}
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodes...).
		Build()

	controller := &NodeController{
		Client:             fakeClient,
		Scheme:             scheme,
		Log:                logr.Discard(),
		ClusterBindingName: "test-binding",
		CurrentNodes:       make(map[string]NodeInfo),
	}

	// Reconcile all nodes concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: "concurrent-node-" + string(rune('a'+index)),
				},
			}
			_, err := controller.Reconcile(context.Background(), req)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Verify all nodes are in cache
	currentNodes := controller.GetCurrentNodes()
	assert.Equal(t, 10, len(currentNodes))
}

// TestNodeInfo_String tests the String method of NodeInfo
func TestNodeInfo_String(t *testing.T) {
	tests := []struct {
		name     string
		nodeInfo NodeInfo
		expected string
	}{
		{
			name: "standard node info",
			nodeInfo: NodeInfo{
				InternalIP:  "192.168.1.10",
				ProxierPort: "8080",
			},
			expected: "192.168.1.10 8080",
		},
		{
			name: "empty node info",
			nodeInfo: NodeInfo{
				InternalIP:  "",
				ProxierPort: "",
			},
			expected: " ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.nodeInfo.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}
