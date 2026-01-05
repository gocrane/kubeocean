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
	"io"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// mockKubeletProxyForMetrics is a mock implementation of KubeletProxy for testing
type mockKubeletProxyForMetrics struct {
	running bool
}

func (m *mockKubeletProxyForMetrics) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts ContainerLogOpts) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockKubeletProxyForMetrics) RunInContainer(ctx context.Context, namespace, podName, containerName string, cmd []string, attach AttachIO) error {
	return nil
}

func (m *mockKubeletProxyForMetrics) Start(ctx context.Context) error {
	m.running = true
	return nil
}

func (m *mockKubeletProxyForMetrics) Stop() error {
	m.running = false
	return nil
}

func (m *mockKubeletProxyForMetrics) IsRunning() bool {
	return m.running
}

// TestNewVNodeProxierAgent tests the creation of VNodeProxierAgent
func TestNewVNodeProxierAgent(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
		TargetNamespace:    "test-namespace",
		DebugLog:           false,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()
	clusterID := "test-cluster"

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, clusterID, logr.Discard())
	defer agent.Stop()

	assert.NotNil(t, agent)
	assert.Equal(t, config, agent.config)
	assert.Equal(t, tokenManager, agent.tokenManager)
	assert.Equal(t, kubeletProxy, agent.kubeletProxy)
	assert.Equal(t, clusterID, agent.clusterID)
	assert.NotNil(t, agent.kubeletClient)
	assert.NotNil(t, agent.metricsParser)
	assert.NotNil(t, agent.nodeStates)
	assert.NotNil(t, agent.httpServers)
	assert.NotNil(t, agent.metricsCache)
	assert.NotNil(t, agent.summaryCache)
	assert.NotNil(t, agent.lastUpdate)
}

// TestVNodeProxierAgent_OnNodeAdded tests node addition event handling
func TestVNodeProxierAgent_OnNodeAdded(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	nodeInfo := NodeInfo{
		InternalIP:  "192.168.1.10",
		ProxierPort: "8080",
	}

	agent.OnNodeAdded("test-node", nodeInfo)

	// Give some time for goroutine to start
	time.Sleep(100 * time.Millisecond)

	// Verify node is in state
	states := agent.GetCurrentNodeStates()
	assert.Equal(t, 1, len(states))
	assert.Equal(t, nodeInfo, states["test-node"])
}

// TestVNodeProxierAgent_OnNodeUpdated tests node update event handling
func TestVNodeProxierAgent_OnNodeUpdated(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	oldNodeInfo := NodeInfo{
		InternalIP:  "192.168.1.10",
		ProxierPort: "8080",
	}

	newNodeInfo := NodeInfo{
		InternalIP:  "192.168.1.10",
		ProxierPort: "9090",
	}

	// Add initial node
	agent.mu.Lock()
	agent.nodeStates["test-node"] = oldNodeInfo
	agent.mu.Unlock()

	agent.OnNodeUpdated("test-node", oldNodeInfo, newNodeInfo)

	// Give some time for goroutine to start
	time.Sleep(100 * time.Millisecond)

	// Verify node info is updated
	states := agent.GetCurrentNodeStates()
	assert.Equal(t, newNodeInfo, states["test-node"])
}

// TestVNodeProxierAgent_OnNodeDeleted tests node deletion event handling
func TestVNodeProxierAgent_OnNodeDeleted(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	nodeInfo := NodeInfo{
		InternalIP:  "192.168.1.10",
		ProxierPort: "8080",
	}

	// Add node
	agent.mu.Lock()
	agent.nodeStates["test-node"] = nodeInfo
	agent.mu.Unlock()

	agent.OnNodeDeleted("test-node", nodeInfo)

	// Verify node is removed
	states := agent.GetCurrentNodeStates()
	assert.Equal(t, 0, len(states))
}

// TestVNodeProxierAgent_GetCurrentNodeStates tests getting current node states
func TestVNodeProxierAgent_GetCurrentNodeStates(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	// Add some nodes
	agent.mu.Lock()
	agent.nodeStates["node-1"] = NodeInfo{InternalIP: "192.168.1.10", ProxierPort: "8080"}
	agent.nodeStates["node-2"] = NodeInfo{InternalIP: "192.168.1.20", ProxierPort: "8081"}
	agent.mu.Unlock()

	states := agent.GetCurrentNodeStates()
	assert.Equal(t, 2, len(states))

	// Verify it's a copy
	delete(states, "node-1")
	assert.Equal(t, 2, len(agent.nodeStates))
}

// TestVNodeProxierAgent_InitializeWithNodes tests initialization with existing nodes
func TestVNodeProxierAgent_InitializeWithNodes(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	nodes := map[string]NodeInfo{
		"node-1": {InternalIP: "192.168.1.10", ProxierPort: "8080"},
		"node-2": {InternalIP: "192.168.1.20", ProxierPort: "8081"},
		"node-3": {InternalIP: "192.168.1.30", ProxierPort: "8082"},
	}

	agent.InitializeWithNodes(nodes)

	// Give some time for HTTP servers to start
	time.Sleep(100 * time.Millisecond)

	states := agent.GetCurrentNodeStates()
	assert.Equal(t, 3, len(states))
	assert.Equal(t, nodes["node-1"], states["node-1"])
	assert.Equal(t, nodes["node-2"], states["node-2"])
	assert.Equal(t, nodes["node-3"], states["node-3"])
}

// TestVNodeProxierAgent_GetActivePorts tests getting active ports
func TestVNodeProxierAgent_GetActivePorts(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	// Initially should be empty
	ports := agent.GetActivePorts()
	assert.Equal(t, 0, len(ports))

	// Add some server entries
	agent.mu.Lock()
	agent.httpServers["8080"] = &ServerEntry{nodeIP: "192.168.1.10", stopChan: make(chan struct{})}
	agent.httpServers["8081"] = &ServerEntry{nodeIP: "192.168.1.20", stopChan: make(chan struct{})}
	agent.mu.Unlock()

	ports = agent.GetActivePorts()
	assert.Equal(t, 2, len(ports))
	assert.Contains(t, ports, "8080")
	assert.Contains(t, ports, "8081")
}

// TestVNodeProxierAgent_GetMetricsData tests getting metrics data
func TestVNodeProxierAgent_GetMetricsData(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	// Test when no metrics available
	data, exists := agent.GetMetricsData("8080")
	assert.False(t, exists)
	assert.Nil(t, data)

	// Add metrics data
	metricsData := []byte("# HELP test_metric\ntest_metric 123")
	agent.mu.Lock()
	agent.metricsCache["8080"] = metricsData
	agent.mu.Unlock()

	// Test when metrics available
	data, exists = agent.GetMetricsData("8080")
	assert.True(t, exists)
	assert.Equal(t, metricsData, data)
}

// TestVNodeProxierAgent_GetSummaryData tests getting summary data
func TestVNodeProxierAgent_GetSummaryData(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	// Test when no summary available
	summary, exists := agent.GetSummaryData("8080")
	assert.False(t, exists)
	assert.Nil(t, summary)

	// Add summary data
	summaryData := &Summary{
		Node: NodeStats{
			NodeName: "test-node",
		},
	}
	agent.mu.Lock()
	agent.summaryCache["8080"] = summaryData
	agent.mu.Unlock()

	// Test when summary available
	summary, exists = agent.GetSummaryData("8080")
	assert.True(t, exists)
	assert.NotNil(t, summary)
	assert.Equal(t, "test-node", summary.Node.NodeName)
}

// TestVNodeProxierAgent_transformSummaryData tests summary data transformation
func TestVNodeProxierAgent_transformSummaryData(t *testing.T) {
	InitGlobalPodMapper()

	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	// Add pod mapping
	SetVirtualPodInfo("physical-pod-1", &VirtualPodInfo{
		VirtualNodeName:     "vnode-1",
		VirtualPodName:      "virtual-pod-1",
		VirtualPodNamespace: "virtual-ns",
	})

	summary := &Summary{
		Node: NodeStats{
			NodeName: "physical-node-1",
		},
		Pods: []PodStats{
			{
				PodRef: PodReference{
					Name:      "physical-pod-1",
					Namespace: KubeoceanWorkerNamespace,
				},
				Containers: []ContainerStats{
					{
						Name: "container-1",
						CPU:  &CPUStats{},
					},
				},
			},
			{
				PodRef: PodReference{
					Name:      "other-pod",
					Namespace: "other-namespace",
				},
			},
		},
	}

	agent.transformSummaryData(summary, "vnode-1")

	// Verify node name is transformed
	assert.Equal(t, "vnode-1", summary.Node.NodeName)

	// Verify only kubeocean-worker pods are kept and transformed
	assert.Equal(t, 1, len(summary.Pods))
	assert.Equal(t, "virtual-pod-1", summary.Pods[0].PodRef.Name)
	assert.Equal(t, "virtual-ns", summary.Pods[0].PodRef.Namespace)
}

// TestVNodeProxierAgent_aggregateNodeStats tests node stats aggregation
func TestVNodeProxierAgent_aggregateNodeStats(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	cpuUsage1 := uint64(1000000)
	cpuUsage2 := uint64(2000000)
	memUsage1 := uint64(100000000)
	memUsage2 := uint64(200000000)

	summary := &Summary{
		Node: NodeStats{
			NodeName: "test-node",
		},
		Pods: []PodStats{
			{
				Containers: []ContainerStats{
					{
						CPU: &CPUStats{
							UsageCoreNanoSeconds: &cpuUsage1,
							UsageNanoCores:       &cpuUsage1,
						},
						Memory: &MemoryStats{
							WorkingSetBytes: &memUsage1,
							UsageBytes:      &memUsage1,
						},
					},
				},
			},
			{
				Containers: []ContainerStats{
					{
						CPU: &CPUStats{
							UsageCoreNanoSeconds: &cpuUsage2,
							UsageNanoCores:       &cpuUsage2,
						},
						Memory: &MemoryStats{
							WorkingSetBytes: &memUsage2,
							UsageBytes:      &memUsage2,
						},
					},
				},
			},
		},
	}

	agent.aggregateNodeStats(summary)

	// Verify CPU aggregation
	require.NotNil(t, summary.Node.CPU)
	assert.Equal(t, cpuUsage1+cpuUsage2, *summary.Node.CPU.UsageCoreNanoSeconds)
	assert.Equal(t, cpuUsage1+cpuUsage2, *summary.Node.CPU.UsageNanoCores)

	// Verify Memory aggregation
	require.NotNil(t, summary.Node.Memory)
	assert.Equal(t, memUsage1+memUsage2, *summary.Node.Memory.WorkingSetBytes)
	assert.Equal(t, memUsage1+memUsage2, *summary.Node.Memory.UsageBytes)
}

// TestVNodeProxierAgent_aggregateCPUStats tests CPU stats aggregation
func TestVNodeProxierAgent_aggregateCPUStats(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	cpuUsage := uint64(1000000)
	cpuNanoCores := uint64(500000)

	container := ContainerStats{
		CPU: &CPUStats{
			UsageCoreNanoSeconds: &cpuUsage,
			UsageNanoCores:       &cpuNanoCores,
			Time:                 metav1.Now(),
		},
	}

	var totalUsage, totalNanoCores uint64
	cpuCount := 0
	hasAny := false
	latestTime := metav1.Time{}

	agent.aggregateCPUStats(container, &totalUsage, &totalNanoCores, &cpuCount, &hasAny, &latestTime)

	assert.True(t, hasAny)
	assert.Equal(t, cpuUsage, totalUsage)
	assert.Equal(t, cpuNanoCores, totalNanoCores)
	assert.Equal(t, 1, cpuCount)
	assert.False(t, latestTime.IsZero())
}

// TestVNodeProxierAgent_aggregateMemoryStats tests Memory stats aggregation
func TestVNodeProxierAgent_aggregateMemoryStats(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	workingSet := uint64(100000000)
	usageBytes := uint64(80000000)
	rssBytes := uint64(60000000)
	pageFaults := uint64(1000)
	majorPageFaults := uint64(100)

	container := ContainerStats{
		Memory: &MemoryStats{
			WorkingSetBytes:    &workingSet,
			UsageBytes:         &usageBytes,
			RSSBytes:           &rssBytes,
			PageFaults:         &pageFaults,
			MajorPageFaults:    &majorPageFaults,
			Time:               metav1.Now(),
		},
	}

	var totalWorkingSet, totalUsage, totalRSS, totalPageFaults, totalMajorPageFaults uint64
	memCount := 0
	hasAny := false
	latestTime := metav1.Time{}

	agent.aggregateMemoryStats(container, &totalWorkingSet, &totalUsage, &totalRSS,
		&totalPageFaults, &totalMajorPageFaults, &memCount, &hasAny, &latestTime)

	assert.True(t, hasAny)
	assert.Equal(t, workingSet, totalWorkingSet)
	assert.Equal(t, usageBytes, totalUsage)
	assert.Equal(t, rssBytes, totalRSS)
	assert.Equal(t, pageFaults, totalPageFaults)
	assert.Equal(t, majorPageFaults, totalMajorPageFaults)
	assert.Equal(t, 1, memCount)
	assert.False(t, latestTime.IsZero())
}

// TestVNodeProxierAgent_updateNodeCPUStats tests updating node CPU stats
func TestVNodeProxierAgent_updateNodeCPUStats(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	summary := &Summary{
		Node: NodeStats{
			NodeName: "test-node",
		},
	}

	totalUsage := uint64(3000000)
	totalNanoCores := uint64(1500000)
	cpuCount := 2
	latestTime := metav1.Now()

	agent.updateNodeCPUStats(summary, true, totalUsage, totalNanoCores, cpuCount, latestTime)

	require.NotNil(t, summary.Node.CPU)
	assert.Equal(t, totalUsage, *summary.Node.CPU.UsageCoreNanoSeconds)
	assert.Equal(t, totalNanoCores, *summary.Node.CPU.UsageNanoCores)
	assert.Equal(t, latestTime, summary.Node.CPU.Time)
}

// TestVNodeProxierAgent_updateNodeMemoryStats tests updating node memory stats
func TestVNodeProxierAgent_updateNodeMemoryStats(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	summary := &Summary{
		Node: NodeStats{
			NodeName: "test-node",
		},
	}

	totalWorkingSet := uint64(300000000)
	totalUsage := uint64(250000000)
	totalRSS := uint64(200000000)
	totalPageFaults := uint64(5000)
	totalMajorPageFaults := uint64(500)
	memCount := 3
	latestTime := metav1.Now()

	agent.updateNodeMemoryStats(summary, true, totalWorkingSet, totalUsage, totalRSS,
		totalPageFaults, totalMajorPageFaults, memCount, latestTime)

	require.NotNil(t, summary.Node.Memory)
	assert.Equal(t, totalWorkingSet, *summary.Node.Memory.WorkingSetBytes)
	assert.Equal(t, totalUsage, *summary.Node.Memory.UsageBytes)
	assert.Equal(t, totalRSS, *summary.Node.Memory.RSSBytes)
	assert.Equal(t, totalPageFaults, *summary.Node.Memory.PageFaults)
	assert.Equal(t, totalMajorPageFaults, *summary.Node.Memory.MajorPageFaults)
	assert.Equal(t, latestTime, summary.Node.Memory.Time)
}

// TestVNodeProxierAgent_ConcurrentNodeOperations tests concurrent node operations
func TestVNodeProxierAgent_ConcurrentNodeOperations(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	// Add nodes concurrently
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(index int) {
			nodeName := "concurrent-node-" + string(rune('a'+index))
			nodeInfo := NodeInfo{
				InternalIP:  "192.168.1." + string(rune('1'+index)),
				ProxierPort: "808" + string(rune('0'+index)),
			}
			agent.OnNodeAdded(nodeName, nodeInfo)
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Give time for goroutines to process
	time.Sleep(200 * time.Millisecond)

	// Verify all nodes are added
	states := agent.GetCurrentNodeStates()
	assert.Equal(t, 10, len(states))
}

// TestMetricsConfig tests MetricsConfig struct
func TestMetricsConfig(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    30 * time.Second,
		MaxConcurrentNodes: 50,
		TargetNamespace:    "default",
		DebugLog:           true,
		TLSSecretName:      "tls-secret",
		TLSSecretNamespace: "kube-system",
	}

	assert.Equal(t, 30*time.Second, config.CollectInterval)
	assert.Equal(t, 50, config.MaxConcurrentNodes)
	assert.Equal(t, "default", config.TargetNamespace)
	assert.True(t, config.DebugLog)
	assert.Equal(t, "tls-secret", config.TLSSecretName)
	assert.Equal(t, "kube-system", config.TLSSecretNamespace)
}

// TestServerEntry tests ServerEntry struct
func TestServerEntry(t *testing.T) {
	stopChan := make(chan struct{})
	entry := &ServerEntry{
		srv:      nil,
		stopChan: stopChan,
		nodeIP:   "192.168.1.10",
	}

	assert.NotNil(t, entry.stopChan)
	assert.Equal(t, "192.168.1.10", entry.nodeIP)
}

// TestNodeInfo_String tests NodeInfo String method (already tested in node_controller_test but included for completeness)
func TestNodeInfo_StringInMetricsCollector(t *testing.T) {
	nodeInfo := NodeInfo{
		InternalIP:  "10.0.0.1",
		ProxierPort: "9090",
	}

	str := nodeInfo.String()
	assert.Equal(t, "10.0.0.1 9090", str)
}

// TestVNodeProxierAgent_EmptyTransformSummaryData tests transformation with nil summary
func TestVNodeProxierAgent_EmptyTransformSummaryData(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	// Should not panic with nil summary
	assert.NotPanics(t, func() {
		agent.transformSummaryData(nil, "vnode-1")
	})
}

// TestVNodeProxierAgent_EmptyAggregateNodeStats tests aggregation with empty pods
func TestVNodeProxierAgent_EmptyAggregateNodeStats(t *testing.T) {
	config := &MetricsConfig{
		CollectInterval:    60 * time.Second,
		MaxConcurrentNodes: 100,
	}

	restConfig := &rest.Config{}
	tokenManager := NewTokenManager(logr.Discard(), restConfig)
	kubeletProxy := &mockKubeletProxyForMetrics{}
	kubeClient := fake.NewSimpleClientset()

	agent := NewVNodeProxierAgent(config, tokenManager, kubeletProxy, kubeClient, "test-cluster", logr.Discard())
	defer agent.Stop()

	summary := &Summary{
		Node: NodeStats{
			NodeName: "test-node",
		},
		Pods: []PodStats{},
	}

	// Should not panic with empty pods
	assert.NotPanics(t, func() {
		agent.aggregateNodeStats(summary)
	})

	// Node stats should still be nil
	assert.Nil(t, summary.Node.CPU)
	assert.Nil(t, summary.Node.Memory)
}
