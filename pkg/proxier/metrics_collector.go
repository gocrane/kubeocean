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
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/protoparser/common"
	"github.com/go-logr/logr"
)

// NodeEventHandler defines the node event handling interface
type NodeEventHandler interface {
	OnNodeAdded(nodeName string, nodeInfo NodeInfo)
	OnNodeUpdated(nodeName string, oldNodeInfo, newNodeInfo NodeInfo)
	OnNodeDeleted(nodeName string, nodeInfo NodeInfo)
}

// NodeInfo node information (consistent with NodeController)
type NodeInfo struct {
	InternalIP  string
	ProxierPort string
}

func (n NodeInfo) String() string {
	return fmt.Sprintf("%s %s", n.InternalIP, n.ProxierPort)
}

// ServerEntry HTTP server entry
type ServerEntry struct {
	srv      *http.Server
	stopChan chan struct{}
	nodeIP   string
}

// MetricsConfig metrics collection configuration
type MetricsConfig struct {
	CollectInterval    time.Duration // Collection interval, default 60s
	MaxConcurrentNodes int           // Maximum concurrent nodes, default 100
	TargetNamespace    string        // Target namespace, empty means all
	DebugLog           bool          // Whether to enable debug logging
}

// MetricsCollector metrics collector
type MetricsCollector struct {
	config        *MetricsConfig
	tokenManager  *TokenManager
	kubeletClient *KubeletClient
	metricsParser *MetricsParser
	log           logr.Logger

	// Node state management
	nodeStates  map[string]NodeInfo     // key: nodeName, value: NodeInfo
	httpServers map[string]*ServerEntry // key: port

	// Metrics cache
	metricsCache map[string][]byte    // key: port, value: cached metrics data
	lastUpdate   map[string]time.Time // key: port, value: last update time

	mu       sync.RWMutex
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(config *MetricsConfig, tokenManager *TokenManager, log logr.Logger) *MetricsCollector {
	// Initialize VictoriaMetrics unmarshal workers (referring to vnode_metrics)
	log.Info("Starting VictoriaMetrics unmarshal workers")
	common.StartUnmarshalWorkers()

	return &MetricsCollector{
		config:        config,
		tokenManager:  tokenManager,
		kubeletClient: NewKubeletClient(log.WithName("kubelet-client"), tokenManager),
		metricsParser: NewMetricsParser(),
		log:           log,
		nodeStates:    make(map[string]NodeInfo),
		httpServers:   make(map[string]*ServerEntry),
		metricsCache:  make(map[string][]byte),
		lastUpdate:    make(map[string]time.Time),
		stopChan:      make(chan struct{}),
	}
}

// Start starts the metrics collector
func (mc *MetricsCollector) Start(ctx context.Context) error {
	mc.log.Info("Starting MetricsCollector")

	// Start timed collection goroutine
	mc.wg.Add(1)
	go func() {
		defer mc.wg.Done()
		ticker := time.NewTicker(mc.config.CollectInterval)
		defer ticker.Stop()

		mc.log.Info("Metrics collection timer started", "interval", mc.config.CollectInterval)

		for {
			select {
			case <-ticker.C:
				mc.log.V(1).Info("Timer tick received, starting metrics collection")
				mc.collectMetricsFromAllNodes()
			case <-mc.stopChan:
				mc.log.Info("Metrics collection stopped")
				return
			}
		}
	}()

	mc.log.Info("MetricsCollector started successfully")
	return nil
}

// collectMetricsFromAllNodes collects metrics from all nodes
func (mc *MetricsCollector) collectMetricsFromAllNodes() {
	mc.mu.RLock()
	nodeStates := make(map[string]NodeInfo)
	for k, v := range mc.nodeStates {
		nodeStates[k] = v
	}
	mc.mu.RUnlock()

	// Use Debug level to avoid too many logs
	mc.log.V(1).Info("Collecting metrics from all nodes", "nodeCount", len(nodeStates))

	// If no nodes available, log warning
	if len(nodeStates) == 0 {
		mc.log.Info("No nodes available for metrics collection")
		return
	}

	// Collect metrics from all nodes concurrently, but serialize parsing to avoid VictoriaMetrics concurrency limits
	semaphore := make(chan struct{}, mc.config.MaxConcurrentNodes)
	var wg sync.WaitGroup

	// Used to store collected raw data
	type collectedData struct {
		nodeName string
		nodeInfo NodeInfo
		data     []byte
		err      error
	}

	collectedChan := make(chan collectedData, len(nodeStates))

	// Collect raw data concurrently
	for nodeName, nodeInfo := range nodeStates {
		wg.Add(1)
		go func(name string, info NodeInfo) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire semaphore
			defer func() { <-semaphore }() // Release semaphore

			// Only collect raw data, do not parse
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			mc.log.V(2).Info("Collecting raw metrics from node", "nodeName", name, "nodeIP", info.InternalIP, "port", info.ProxierPort)

			// Get cAdvisor metrics from kubelet API
			metricsData, err := mc.kubeletClient.GetCAdvisorMetrics(ctx, info.InternalIP, "10250")

			collectedChan <- collectedData{
				nodeName: name,
				nodeInfo: info,
				data:     metricsData,
				err:      err,
			}
		}(nodeName, nodeInfo)
	}

	// Wait for all collection to complete
	go func() {
		wg.Wait()
		close(collectedChan)
	}()

	// Serialize parsing of collected data
	for collected := range collectedChan {
		if collected.err != nil {
			mc.log.Error(collected.err, "Failed to collect metrics from node", "nodeName", collected.nodeName, "nodeIP", collected.nodeInfo.InternalIP)
			continue
		}

		// Parse metrics data (with timeout mechanism)
		parseCtx, parseCancel := context.WithTimeout(context.Background(), 60*time.Second)
		parseErr := make(chan error, 1)

		go func() {
			reader := strings.NewReader(string(collected.data))
			parseErr <- mc.metricsParser.ParseAndStoreMetrics(reader, collected.nodeInfo.ProxierPort)
		}()

		select {
		case err := <-parseErr:
			parseCancel()
			if err != nil {
				mc.log.Error(err, "Failed to parse metrics from node", "nodeName", collected.nodeName, "nodeIP", collected.nodeInfo.InternalIP)
				continue
			}
		case <-parseCtx.Done():
			parseCancel()
			mc.log.Error(parseCtx.Err(), "Parse timeout for node", "nodeName", collected.nodeName, "nodeIP", collected.nodeInfo.InternalIP, "port", collected.nodeInfo.ProxierPort)
			continue
		}

		// Generate parsed metrics data
		var buf strings.Builder
		mc.metricsParser.WritePrometheusMetrics(&buf, collected.nodeInfo.ProxierPort, collected.nodeInfo.InternalIP, mc.config.TargetNamespace)
		parsedMetricsData := []byte(buf.String())

		// Update cache
		mc.mu.Lock()
		oldData, existed := mc.metricsCache[collected.nodeInfo.ProxierPort]
		mc.metricsCache[collected.nodeInfo.ProxierPort] = parsedMetricsData
		mc.lastUpdate[collected.nodeInfo.ProxierPort] = time.Now()
		mc.mu.Unlock()

		// Log cache changes
		if existed {
			mc.log.V(1).Info("Updated metrics cache",
				"port", collected.nodeInfo.ProxierPort,
				"oldSize", len(oldData),
				"newSize", len(parsedMetricsData))
		} else {
			mc.log.V(1).Info("Created new metrics cache entry",
				"port", collected.nodeInfo.ProxierPort,
				"size", len(parsedMetricsData))
		}

		mc.log.V(2).Info("Successfully collected and parsed metrics from node",
			"nodeName", collected.nodeName,
			"nodeIP", collected.nodeInfo.InternalIP,
			"port", collected.nodeInfo.ProxierPort,
			"parsedSize", len(parsedMetricsData))
	}

	mc.log.V(1).Info("Completed metrics collection from all nodes")
}

// OnNodeAdded handles node addition events
func (mc *MetricsCollector) OnNodeAdded(nodeName string, nodeInfo NodeInfo) {
	mc.log.Info("➕ Node added", "nodeName", nodeName, "nodeInfo", nodeInfo)

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Update node state
	mc.nodeStates[nodeName] = nodeInfo

	// Start HTTP server (call outside lock)
	go func() {
		mc.startHTTPServerForNode(nodeInfo.ProxierPort, nodeInfo)
	}()
}

// OnNodeUpdated handles node update events
func (mc *MetricsCollector) OnNodeUpdated(nodeName string, oldNodeInfo, newNodeInfo NodeInfo) {
	mc.log.Info("🔄 Node updated", "nodeName", nodeName, "oldNodeInfo", oldNodeInfo, "newNodeInfo", newNodeInfo)

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Update node state
	mc.nodeStates[nodeName] = newNodeInfo

	// If port changes, need to restart HTTP server
	if oldNodeInfo.ProxierPort != newNodeInfo.ProxierPort {
		mc.log.Info("🔄 Port changed for node",
			"nodeName", nodeName,
			"oldPort", oldNodeInfo.ProxierPort,
			"newPort", newNodeInfo.ProxierPort,
			"nodeIP", newNodeInfo.InternalIP)

		// Stop old HTTP server
		if entry, exists := mc.httpServers[oldNodeInfo.ProxierPort]; exists {
			mc.log.Info("🛑 Stopping old HTTP server", "port", oldNodeInfo.ProxierPort, "nodeIP", entry.nodeIP)
			close(entry.stopChan)
			delete(mc.httpServers, oldNodeInfo.ProxierPort)
		}

		// Clean up old cache data
		if _, exists := mc.metricsCache[oldNodeInfo.ProxierPort]; exists {
			mc.log.V(1).Info("🧹 Clearing old metrics cache", "port", oldNodeInfo.ProxierPort)
			delete(mc.metricsCache, oldNodeInfo.ProxierPort)
			delete(mc.lastUpdate, oldNodeInfo.ProxierPort)
		}

		// Start new HTTP server (call outside lock)
		go func() {
			mc.startHTTPServerForNode(newNodeInfo.ProxierPort, newNodeInfo)
		}()
	} else {
		mc.log.V(1).Info("✅ Port unchanged for node",
			"nodeName", nodeName,
			"port", newNodeInfo.ProxierPort,
			"nodeIP", newNodeInfo.InternalIP)
	}
}

// OnNodeDeleted handles node deletion events
func (mc *MetricsCollector) OnNodeDeleted(nodeName string, nodeInfo NodeInfo) {
	mc.log.Info("🗑️ Node deleted", "nodeName", nodeName, "nodeInfo", nodeInfo)

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Remove from node state
	delete(mc.nodeStates, nodeName)

	// Stop HTTP server
	if entry, exists := mc.httpServers[nodeInfo.ProxierPort]; exists {
		mc.log.Info("🛑 Stopping HTTP server for deleted node", "port", nodeInfo.ProxierPort, "nodeIP", entry.nodeIP)
		close(entry.stopChan)
		delete(mc.httpServers, nodeInfo.ProxierPort)

		// Print current listening ports list (call outside lock)
		go func() {
			mc.printListeningPorts()
		}()
	}

	// Clean up metrics cache
	if _, exists := mc.metricsCache[nodeInfo.ProxierPort]; exists {
		mc.log.V(1).Info("🧹 Clearing metrics cache for deleted node", "port", nodeInfo.ProxierPort)
		delete(mc.metricsCache, nodeInfo.ProxierPort)
		delete(mc.lastUpdate, nodeInfo.ProxierPort)
	}
}

// GetCurrentNodeStates gets current node states (for debugging and testing)
func (mc *MetricsCollector) GetCurrentNodeStates() map[string]NodeInfo {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	result := make(map[string]NodeInfo)
	for k, v := range mc.nodeStates {
		result[k] = v
	}
	return result
}

// InitializeWithNodes initializes MetricsCollector with existing nodes
func (mc *MetricsCollector) InitializeWithNodes(nodes map[string]NodeInfo) {
	mc.log.Info("Initializing MetricsCollector with existing nodes", "nodeCount", len(nodes))

	// Clear existing state and add all nodes
	mc.mu.Lock()
	mc.nodeStates = make(map[string]NodeInfo)
	for nodeName, nodeInfo := range nodes {
		mc.nodeStates[nodeName] = nodeInfo
	}
	mc.mu.Unlock()

	// Start HTTP servers for all nodes
	for _, nodeInfo := range nodes {
		// Start HTTP server (this method handles locking internally)
		mc.startHTTPServerForNode(nodeInfo.ProxierPort, nodeInfo)
	}
}

// Stop stops the metrics collector
func (mc *MetricsCollector) Stop() {
	mc.log.Info("Stopping MetricsCollector")
	close(mc.stopChan)

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Stop all HTTP servers
	for port, entry := range mc.httpServers {
		mc.log.V(1).Info("Stopping HTTP server", "port", port, "nodeIP", entry.nodeIP)
		close(entry.stopChan)
	}

	// Clear server mapping and cache
	mc.httpServers = make(map[string]*ServerEntry)
	mc.nodeStates = make(map[string]NodeInfo)
	mc.metricsCache = make(map[string][]byte)
	mc.lastUpdate = make(map[string]time.Time)

	// Wait for all goroutines to complete
	mc.wg.Wait()

	// Stop VictoriaMetrics unmarshal workers (referring to vnode_metrics)
	mc.log.Info("Stopping VictoriaMetrics unmarshal workers")
	common.StopUnmarshalWorkers()

	mc.log.Info("MetricsCollector stopped")
}

// startHTTPServerForNode starts HTTP server for specified node
func (mc *MetricsCollector) startHTTPServerForNode(port string, nodeInfo NodeInfo) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Check if port is already listening
	if _, exists := mc.httpServers[port]; exists {
		mc.log.V(1).Info("Port already listening, skipping", "port", port, "nodeIP", nodeInfo.InternalIP)
		return
	}

	stopChan := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/", mc.createHandler(port))

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Start server
	mc.wg.Add(1)
	go func() {
		defer mc.wg.Done()
		mc.log.Info("🚀 Starting HTTP server for metrics",
			"port", port,
			"nodeIP", nodeInfo.InternalIP,
			"endpoint", "http://localhost:"+port+"/")

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			mc.log.Error(err, "HTTP server error", "port", port, "nodeIP", nodeInfo.InternalIP)
		}
	}()

	// Graceful shutdown handling
	go func() {
		<-stopChan
		mc.log.Info("🛑 Shutting down HTTP server", "port", port, "nodeIP", nodeInfo.InternalIP)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	mc.httpServers[port] = &ServerEntry{
		srv:      server,
		stopChan: stopChan,
		nodeIP:   nodeInfo.InternalIP,
	}

	// Print current listening ports list (call outside lock)
	go func() {
		mc.printListeningPorts()
	}()
}

// printListeningPorts prints current listening ports list
func (mc *MetricsCollector) printListeningPorts() {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if len(mc.httpServers) == 0 {
		mc.log.Info("📊 No HTTP servers currently listening")
		return
	}

	var ports []string
	for port, entry := range mc.httpServers {
		ports = append(ports, fmt.Sprintf("%s(nodeIP:%s)", port, entry.nodeIP))
	}

	mc.log.Info("📊 Currently listening ports",
		"count", len(mc.httpServers),
		"ports", strings.Join(ports, ", "))
}

// createHandler creates HTTP handler
func (mc *MetricsCollector) createHandler(port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mc.mu.RLock()
		defer mc.mu.RUnlock()

		// Return different data based on path
		switch r.URL.Path {
		case "/metrics":
			// Return real cAdvisor metrics data
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			mc.writeRealMetrics(w, port)
		default:
			// Return status information
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"port":"%s","nodeIP":"%s","available_endpoints":["/metrics"],"status":"running"}`,
				port, mc.getNodeIPByPort(port))
		}
	}
}

// writeRealMetrics writes real cAdvisor metrics data
func (mc *MetricsCollector) writeRealMetrics(w http.ResponseWriter, port string) {
	// Get metrics data from cache
	mc.mu.RLock()
	metricsData, exists := mc.metricsCache[port]
	lastUpdate, _ := mc.lastUpdate[port]
	mc.mu.RUnlock()

	// Log cache access
	mc.log.V(2).Info("Accessing metrics cache",
		"port", port,
		"exists", exists,
		"dataSize", len(metricsData),
		"lastUpdate", lastUpdate)

	if !exists || len(metricsData) == 0 {
		// If no cached data, return empty metrics with 200 status
		mc.log.V(1).Info("No metrics data available for port", "port", port)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "# No metrics data available for port %s\n", port)
		fmt.Fprintf(w, "# This may indicate that metrics collection is still in progress or there is no metrics data available\n")
		return
	}

	// Add some metadata comments
	nodeIP := mc.getNodeIPByPort(port)
	fmt.Fprintf(w, "# Metrics from node %s (port %s)\n", nodeIP, port)
	fmt.Fprintf(w, "# Last updated: %s\n", lastUpdate.Format(time.RFC3339))
	fmt.Fprintf(w, "# Source: kubelet /metrics/cadvisor\n")
	fmt.Fprintf(w, "\n")

	// Write real cAdvisor metrics data
	w.Write(metricsData)
}

// getNodeIPByPort gets node IP by port
func (mc *MetricsCollector) getNodeIPByPort(port string) string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	for _, nodeInfo := range mc.nodeStates {
		if nodeInfo.ProxierPort == port {
			return nodeInfo.InternalIP
		}
	}
	return "unknown"
}

// GetActivePorts gets current active ports list
func (mc *MetricsCollector) GetActivePorts() []string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	ports := make([]string, 0, len(mc.httpServers))
	for port := range mc.httpServers {
		ports = append(ports, port)
	}
	return ports
}

// GetMetricsData gets metrics data for a specific port
func (mc *MetricsCollector) GetMetricsData(port string) ([]byte, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	data, exists := mc.metricsCache[port]
	return data, exists
}
