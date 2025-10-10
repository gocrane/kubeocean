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
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/protoparser/common"
	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientremotecommand "k8s.io/client-go/tools/remotecommand"

	localremotecommand "github.com/TKEColocation/kubeocean/pkg/proxier/remotecommand"
)

// Note: trueStr and oneStr constants are defined in server.go and shared across the package

const (
	// KubeoceanWorkerNamespace is the target namespace for physical cluster resources
	KubeoceanWorkerNamespace = "kubeocean-worker"
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

	// TLS configuration
	TLSSecretName      string // TLS secret name
	TLSSecretNamespace string // TLS secret namespace
}

// MetricsCollector metrics collector
type MetricsCollector struct {
	config        *MetricsConfig
	tokenManager  *TokenManager
	kubeletClient *KubeletClient
	metricsParser *MetricsParser
	kubeletProxy  KubeletProxy // Kubelet proxy for logs and exec
	log           logr.Logger

	// TLS configuration
	kubeClient kubernetes.Interface // Kubernetes client for loading TLS secrets
	tlsConfig  *tls.Config          // TLS configuration for HTTPS servers

	// Cluster identification for VNode name generation
	clusterID string // ClusterBinding.Spec.ClusterID for generating VNode names

	// Node state management
	nodeStates  map[string]NodeInfo     // key: nodeName, value: NodeInfo
	httpServers map[string]*ServerEntry // key: port

	// Metrics cache
	metricsCache map[string][]byte    // key: port, value: cached Prometheus metrics data
	summaryCache map[string]*Summary  // key: port, value: cached Summary data (for metrics-server compatibility)
	lastUpdate   map[string]time.Time // key: port, value: last update time

	mu       sync.RWMutex
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(config *MetricsConfig, tokenManager *TokenManager, kubeletProxy KubeletProxy, kubeClient kubernetes.Interface, clusterID string, log logr.Logger) *MetricsCollector {
	// Initialize VictoriaMetrics unmarshal workers (referring to vnode_metrics)
	log.Info("Starting VictoriaMetrics unmarshal workers")
	common.StartUnmarshalWorkers()

	mc := &MetricsCollector{
		config:        config,
		tokenManager:  tokenManager,
		kubeletClient: NewKubeletClient(log.WithName("kubelet-client"), tokenManager),
		metricsParser: NewMetricsParser(),
		kubeletProxy:  kubeletProxy,
		log:           log,
		kubeClient:    kubeClient,
		clusterID:     clusterID,
		nodeStates:    make(map[string]NodeInfo),
		httpServers:   make(map[string]*ServerEntry),
		metricsCache:  make(map[string][]byte),
		summaryCache:  make(map[string]*Summary),
		lastUpdate:    make(map[string]time.Time),
		stopChan:      make(chan struct{}),
	}

	// Load TLS configuration if provided
	if config.TLSSecretName != "" && config.TLSSecretNamespace != "" {
		log.Info("Loading TLS configuration for metrics HTTPS servers",
			"secretName", config.TLSSecretName,
			"secretNamespace", config.TLSSecretNamespace)

		tlsConfig, err := mc.loadTLSConfigFromSecret()
		if err != nil {
			log.Error(err, "Failed to load TLS config, will use HTTP instead of HTTPS")
		} else {
			mc.tlsConfig = tlsConfig
			log.Info("TLS configuration loaded successfully, metrics servers will use HTTPS")
		}
	} else {
		log.Info("No TLS configuration provided, metrics servers will use HTTP")
	}

	return mc
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
		nodeName    string
		nodeInfo    NodeInfo
		metricsData []byte   // Prometheus format metrics
		summaryData *Summary // Summary format data (for metrics-server)
		err         error
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

			// Get cAdvisor Prometheus metrics from kubelet API
			metricsData, metricsErr := mc.kubeletClient.GetCAdvisorMetrics(ctx, info.InternalIP, "10250")

			// Get Summary stats from kubelet API (for metrics-server compatibility)
			summaryData, summaryErr := mc.kubeletClient.GetSummary(ctx, info.InternalIP, "10250")

			// Combine errors if any
			var combinedErr error
			if metricsErr != nil && summaryErr != nil {
				combinedErr = fmt.Errorf("metrics error: %v; summary error: %v", metricsErr, summaryErr)
			} else if metricsErr != nil {
				combinedErr = metricsErr
			} else if summaryErr != nil {
				combinedErr = summaryErr
			}

			collectedChan <- collectedData{
				nodeName:    name,
				nodeInfo:    info,
				metricsData: metricsData,
				summaryData: summaryData,
				err:         combinedErr,
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
			// Continue processing if we have partial data
			if collected.metricsData == nil && collected.summaryData == nil {
				continue
			}
		}

		// Parse Prometheus metrics data (with timeout mechanism) if available
		if collected.metricsData != nil {
			parseCtx, parseCancel := context.WithTimeout(context.Background(), 60*time.Second)
			parseErr := make(chan error, 1)

			go func() {
				reader := strings.NewReader(string(collected.metricsData))
				parseErr <- mc.metricsParser.ParseAndStoreMetrics(reader, collected.nodeInfo.ProxierPort)
			}()

			select {
			case err := <-parseErr:
				parseCancel()
				if err != nil {
					mc.log.Error(err, "Failed to parse metrics from node", "nodeName", collected.nodeName, "nodeIP", collected.nodeInfo.InternalIP)
				}
			case <-parseCtx.Done():
				parseCancel()
				mc.log.Error(parseCtx.Err(), "Parse timeout for node", "nodeName", collected.nodeName, "nodeIP", collected.nodeInfo.InternalIP, "port", collected.nodeInfo.ProxierPort)
			}

			// Generate parsed metrics data
			var buf strings.Builder
			mc.metricsParser.WritePrometheusMetrics(&buf, collected.nodeInfo.ProxierPort, collected.nodeInfo.InternalIP, mc.config.TargetNamespace)
			parsedMetricsData := []byte(buf.String())

			// Update Prometheus metrics cache
			mc.mu.Lock()
			oldData, existed := mc.metricsCache[collected.nodeInfo.ProxierPort]
			mc.metricsCache[collected.nodeInfo.ProxierPort] = parsedMetricsData
			mc.mu.Unlock()

			// Log cache changes
			if existed {
				mc.log.V(1).Info("Updated Prometheus metrics cache",
					"port", collected.nodeInfo.ProxierPort,
					"oldSize", len(oldData),
					"newSize", len(parsedMetricsData))
			} else {
				mc.log.V(1).Info("Created new Prometheus metrics cache entry",
					"port", collected.nodeInfo.ProxierPort,
					"size", len(parsedMetricsData))
			}
		}

		// Store Summary data if available (for metrics-server compatibility)
		if collected.summaryData != nil {
			// Transform Summary data for metrics-server compatibility
			mc.transformSummaryData(collected.summaryData, collected.nodeName)

			mc.mu.Lock()
			mc.summaryCache[collected.nodeInfo.ProxierPort] = collected.summaryData
			mc.lastUpdate[collected.nodeInfo.ProxierPort] = time.Now()
			mc.mu.Unlock()

			mc.log.V(1).Info("Stored Summary data for metrics-server",
				"port", collected.nodeInfo.ProxierPort,
				"nodeName", collected.summaryData.Node.NodeName,
				"podCount", len(collected.summaryData.Pods))
		}

		mc.log.V(2).Info("Successfully collected and stored metrics from node",
			"nodeName", collected.nodeName,
			"nodeIP", collected.nodeInfo.InternalIP,
			"port", collected.nodeInfo.ProxierPort,
			"hasPrometheus", collected.metricsData != nil,
			"hasSummary", collected.summaryData != nil)
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
	mc.summaryCache = make(map[string]*Summary)
	mc.lastUpdate = make(map[string]time.Time)

	// Wait for all goroutines to complete
	mc.wg.Wait()

	// Stop VictoriaMetrics unmarshal workers (referring to vnode_metrics)
	mc.log.Info("Stopping VictoriaMetrics unmarshal workers")
	common.StopUnmarshalWorkers()

	mc.log.Info("MetricsCollector stopped")
}

// startHTTPServerForNode starts HTTP/HTTPS server for specified node
func (mc *MetricsCollector) startHTTPServerForNode(port string, nodeInfo NodeInfo) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Check if port is already listening
	if _, exists := mc.httpServers[port]; exists {
		mc.log.V(1).Info("Port already listening, skipping", "port", port, "nodeIP", nodeInfo.InternalIP)
		return
	}

	stopChan := make(chan struct{})

	// Use gorilla/mux router to support multiple endpoints
	router := mc.setupRoutes(port, nodeInfo)

	server := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	// Determine server type based on TLS configuration
	serverType := "HTTP"
	protocol := "http"
	if mc.tlsConfig != nil {
		serverType = "HTTPS"
		protocol = "https"
		server.TLSConfig = mc.tlsConfig
	}

	// Start server
	mc.wg.Add(1)
	go func() {
		defer mc.wg.Done()
		mc.log.Info("🚀 Starting "+serverType+" server for metrics",
			"port", port,
			"nodeIP", nodeInfo.InternalIP,
			"endpoint", protocol+"://localhost:"+port+"/",
			"tls", mc.tlsConfig != nil)

		var err error
		if mc.tlsConfig != nil {
			// Start HTTPS server
			listener, listenErr := tls.Listen("tcp", ":"+port, mc.tlsConfig)
			if listenErr != nil {
				mc.log.Error(listenErr, "Failed to create TLS listener", "port", port, "nodeIP", nodeInfo.InternalIP)
				return
			}
			err = server.Serve(listener)
		} else {
			// Start HTTP server
			err = server.ListenAndServe()
		}

		if err != nil && err != http.ErrServerClosed {
			mc.log.Error(err, serverType+" server error", "port", port, "nodeIP", nodeInfo.InternalIP)
		}
	}()

	// Graceful shutdown handling
	go func() {
		<-stopChan
		mc.log.Info("🛑 Shutting down "+serverType+" server", "port", port, "nodeIP", nodeInfo.InternalIP)
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

	serverType := "HTTP"
	if mc.tlsConfig != nil {
		serverType = "HTTPS"
	}

	if len(mc.httpServers) == 0 {
		mc.log.Info("📊 No " + serverType + " servers currently listening")
		return
	}

	var ports []string
	for port, entry := range mc.httpServers {
		ports = append(ports, fmt.Sprintf("%s(nodeIP:%s)", port, entry.nodeIP))
	}

	mc.log.Info("📊 Currently listening ports",
		"count", len(mc.httpServers),
		"type", serverType,
		"ports", strings.Join(ports, ", "))
}

// setupRoutes sets up HTTP routes for metrics server
func (mc *MetricsCollector) setupRoutes(port string, nodeInfo NodeInfo) *mux.Router {
	router := mux.NewRouter()
	router.StrictSlash(true)

	// Metrics endpoint (Prometheus format)
	router.HandleFunc("/metrics", mc.handleMetrics(port)).Methods("GET")

	// Summary stats endpoint (metrics-server compatible)
	router.HandleFunc("/stats/summary", mc.handleSummary(port)).Methods("GET")

	// Container logs endpoint (same as main server)
	router.HandleFunc("/containerLogs/{namespace}/{pod}/{container}", mc.handleContainerLogs).Methods("GET")

	// Container exec endpoint (same as main server)
	router.HandleFunc("/exec/{namespace}/{pod}/{container}", mc.handleContainerExec).Methods("POST", "GET")

	// Health check endpoint
	router.HandleFunc("/healthz", mc.handleHealthz).Methods("GET")

	// Default handler for root path
	router.HandleFunc("/", mc.handleRoot(port, nodeInfo)).Methods("GET")

	return router
}

// handleMetrics handles metrics endpoint (Prometheus format)
func (mc *MetricsCollector) handleMetrics(port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		mc.writeRealMetrics(w, port)
	}
}

// handleSummary handles /stats/summary endpoint (metrics-server compatible)
func (mc *MetricsCollector) handleSummary(port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mc.mu.RLock()
		summary, exists := mc.summaryCache[port]
		lastUpdate, _ := mc.lastUpdate[port]
		mc.mu.RUnlock()

		if !exists || summary == nil {
			mc.log.V(1).Info("No summary data available for port", "port", port)
			http.Error(w, fmt.Sprintf("No summary data available for port %s", port), http.StatusNotFound)
			return
		}

		// Log access
		mc.log.V(2).Info("Serving summary data",
			"port", port,
			"nodeName", summary.Node.NodeName,
			"podCount", len(summary.Pods),
			"lastUpdate", lastUpdate,
			"remoteAddr", r.RemoteAddr)

		// Set response headers
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Encode and write JSON response
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ") // Pretty print for debugging
		if err := encoder.Encode(summary); err != nil {
			mc.log.Error(err, "Failed to encode summary JSON", "port", port)
			return
		}

		mc.log.V(2).Info("Successfully served summary data",
			"port", port,
			"nodeName", summary.Node.NodeName)
	}
}

// handleContainerLogs handles container logs requests (same as main server)
func (mc *MetricsCollector) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	if mc.kubeletProxy == nil {
		http.Error(w, "Kubelet proxy not available", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	namespace := vars["namespace"]
	pod := vars["pod"]
	container := vars["container"]

	mc.log.Info("Processing container logs request",
		"namespace", namespace,
		"pod", pod,
		"container", container,
		"remoteAddr", r.RemoteAddr,
		"userAgent", r.UserAgent(),
	)

	// Parse log options from query parameters
	opts, err := mc.parseLogOptions(r.URL.Query())
	if err != nil {
		mc.log.Error(err, "Failed to parse log options", "namespace", namespace, "pod", pod, "container", container)
		http.Error(w, fmt.Sprintf("Invalid log options: %v", err), http.StatusBadRequest)
		return
	}

	mc.log.Info("Parsed log options successfully", "namespace", namespace, "pod", pod, "container", container)

	// Get logs from proxy
	logs, err := mc.kubeletProxy.GetContainerLogs(r.Context(), namespace, pod, container, opts)
	if err != nil {
		mc.log.Error(err, "Failed to get container logs", "namespace", namespace, "pod", pod, "container", container)
		http.Error(w, fmt.Sprintf("Failed to get logs: %v", err), http.StatusInternalServerError)
		return
	}
	defer logs.Close()

	// Set response headers
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Content-Type", "text/plain")

	// Stream logs to client
	_, err = io.Copy(w, logs)
	if err != nil {
		mc.log.Error(err, "Failed to stream logs to client", "namespace", namespace, "pod", pod, "container", container)
	}
}

// handleContainerExec handles container exec requests (same as main server)
func (mc *MetricsCollector) handleContainerExec(w http.ResponseWriter, r *http.Request) {
	if mc.kubeletProxy == nil {
		http.Error(w, "Kubelet proxy not available", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	namespace := vars["namespace"]
	pod := vars["pod"]
	container := vars["container"]

	// Log the request details for debugging
	mc.log.Info("Handling container exec request",
		"namespace", namespace,
		"pod", pod,
		"container", container,
		"method", r.Method,
		"url", r.URL.String(),
		"headers", r.Header,
		"remoteAddr", r.RemoteAddr,
	)

	// Get supported protocols from client
	clientSupportedProtocols := strings.Split(r.Header.Get("X-Stream-Protocol-Version"), ",")

	// Define our server supported protocols (in order of preference)
	serverSupportedProtocols := []string{
		"v4.channel.k8s.io",
		"v3.channel.k8s.io",
		"v2.channel.k8s.io",
		"channel.k8s.io",
	}

	// Log for debugging
	mc.log.Info("Protocol negotiation",
		"clientSupported", clientSupportedProtocols,
		"serverSupported", serverSupportedProtocols,
	)

	// Get command from query parameters
	command := r.URL.Query()["command"]

	// Parse exec options using the same method as official virtual-kubelet
	streamOpts, err := getExecOptions(r, mc.log)
	if err != nil {
		mc.log.Error(err, "Failed to parse exec options",
			"namespace", namespace,
			"pod", pod,
			"container", container,
		)
		http.Error(w, fmt.Sprintf("Invalid exec options: %v", err), http.StatusBadRequest)
		return
	}

	mc.log.Info("Parsed exec options",
		"namespace", namespace,
		"pod", pod,
		"container", container,
		"command", command,
		"stdin", streamOpts.Stdin,
		"stdout", streamOpts.Stdout,
		"stderr", streamOpts.Stderr,
		"tty", streamOpts.TTY,
	)

	// Create container exec context like official virtual-kubelet
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exec := &metricsCollectorExecContext{
		ctx:       ctx,
		collector: mc,
		namespace: namespace,
		pod:       pod,
		container: container,
	}

	// Use the same timeout settings as official virtual-kubelet
	idleTimeout := 30 * time.Second
	streamCreationTimeout := 30 * time.Second

	// Serve exec using tke_vnode's proven SPDY implementation - match exact parameter pattern
	localremotecommand.ServeExec(
		w,
		r,
		exec,
		"", // Consistent with tke_vnode, pass empty string
		"", // Consistent with tke_vnode, pass empty string
		container,
		command,
		&localremotecommand.Options{
			Stdin:  streamOpts.Stdin,
			Stdout: streamOpts.Stdout,
			Stderr: streamOpts.Stderr,
			TTY:    streamOpts.TTY,
		},
		idleTimeout,
		streamCreationTimeout,
		serverSupportedProtocols,
	)
}

// handleHealthz handles health check requests
func (mc *MetricsCollector) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

// handleRoot handles root path requests
func (mc *MetricsCollector) handleRoot(port string, nodeInfo NodeInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"port":"%s","nodeIP":"%s","available_endpoints":["/metrics","/stats/summary","/containerLogs/{namespace}/{pod}/{container}","/exec/{namespace}/{pod}/{container}","/healthz"],"status":"running"}`,
			port, nodeInfo.InternalIP)
	}
}

// writeRealMetrics writes real cAdvisor metrics data
func (mc *MetricsCollector) writeRealMetrics(w http.ResponseWriter, port string) {
	// Snapshot needed data under a short read lock
	mc.mu.RLock()
	metricsData, exists := mc.metricsCache[port]
	lastUpdate, _ := mc.lastUpdate[port]
	nodeIP := "unknown"
	if entry, ok := mc.httpServers[port]; ok {
		nodeIP = entry.nodeIP
	}
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
	fmt.Fprintf(w, "# Metrics from node %s (port %s)\n", nodeIP, port)
	fmt.Fprintf(w, "# Last updated: %s\n", lastUpdate.Format(time.RFC3339))
	fmt.Fprintf(w, "# Source: kubelet /metrics/cadvisor\n")
	fmt.Fprintf(w, "\n")

	// Write real cAdvisor metrics data
	w.Write(metricsData)
}

// loadTLSConfigFromSecret loads TLS configuration from Kubernetes Secret
func (mc *MetricsCollector) loadTLSConfigFromSecret() (*tls.Config, error) {
	if mc.kubeClient == nil {
		return nil, fmt.Errorf("kubernetes client is not available")
	}

	mc.log.Info("Loading TLS certificate from Kubernetes Secret for metrics servers",
		"secretName", mc.config.TLSSecretName,
		"secretNamespace", mc.config.TLSSecretNamespace)

	// Get the secret
	secret, err := mc.kubeClient.CoreV1().Secrets(mc.config.TLSSecretNamespace).Get(
		context.TODO(), mc.config.TLSSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w",
			mc.config.TLSSecretNamespace, mc.config.TLSSecretName, err)
	}

	// Extract certificate and key from secret
	certData, exists := secret.Data["tls.crt"]
	if !exists {
		return nil, fmt.Errorf("secret %s/%s does not contain tls.crt key",
			mc.config.TLSSecretNamespace, mc.config.TLSSecretName)
	}

	keyData, exists := secret.Data["tls.key"]
	if !exists {
		return nil, fmt.Errorf("secret %s/%s does not contain tls.key key",
			mc.config.TLSSecretNamespace, mc.config.TLSSecretName)
	}

	// Load certificate and key
	cert, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return nil, fmt.Errorf("failed to load X509 key pair: %w", err)
	}

	mc.log.Info("Successfully loaded TLS certificate from Kubernetes Secret for metrics servers")

	// Return TLS config - no client authentication required for metrics endpoints
	return &tls.Config{
		Certificates:             []tls.Certificate{cert},
		MinVersion:               tls.VersionTLS12,
		PreferServerCipherSuites: true,
		ClientAuth:               tls.NoClientCert, // Metrics endpoints don't require client certs
	}, nil
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

// GetMetricsData gets Prometheus metrics data for a specific port
func (mc *MetricsCollector) GetMetricsData(port string) ([]byte, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	data, exists := mc.metricsCache[port]
	return data, exists
}

// GetSummaryData gets Summary data for a specific port (for metrics-server compatibility)
func (mc *MetricsCollector) GetSummaryData(port string) (*Summary, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	data, exists := mc.summaryCache[port]
	return data, exists
}

// transformSummaryData transforms Summary data for metrics-server compatibility
// 1. Convert physical node name to VNode name
// 2. Filter pods to only include those in kubeocean-worker namespace
// 3. Convert physical pod names and namespaces to virtual pod names and namespaces
// 4. Aggregate CPU and Memory stats from all pods/containers to Node level
func (mc *MetricsCollector) transformSummaryData(summary *Summary, VirtualNodeName string) {
	if summary == nil {
		return
	}

	summary.Node.NodeName = VirtualNodeName

	// 2. Filter pods and convert names to virtual pod info
	originalPodCount := len(summary.Pods)
	filteredPods := make([]PodStats, 0, originalPodCount)
	skippedPods := 0
	convertedPods := 0

	for _, pod := range summary.Pods {
		// Only process pods in kubeocean-worker namespace
		if pod.PodRef.Namespace != KubeoceanWorkerNamespace {
			continue
		}

		// Try to get virtual pod information for name conversion
		virtualInfo, exists := GetVirtualPodInfo(pod.PodRef.Name)
		if !exists {
			// Skip pods without virtual mapping
			mc.log.V(2).Info("No virtual mapping found for pod, skipping from summary",
				"physicalPodName", pod.PodRef.Name,
				"physicalNamespace", pod.PodRef.Namespace)
			skippedPods++
			continue
		}

		// Convert physical pod name and namespace to virtual pod name and namespace
		pod.PodRef.Name = virtualInfo.VirtualPodName
		pod.PodRef.Namespace = virtualInfo.VirtualPodNamespace

		filteredPods = append(filteredPods, pod)
		convertedPods++

		mc.log.V(2).Info("Converted pod reference to virtual names",
			"virtualPodName", virtualInfo.VirtualPodName,
			"virtualNamespace", virtualInfo.VirtualPodNamespace,
			"virtualNodeName", virtualInfo.VirtualNodeName)
	}

	summary.Pods = filteredPods

	// 4. Aggregate CPU and Memory stats from all filtered pods/containers to Node level
	mc.aggregateNodeStats(summary)

	mc.log.V(2).Info("Transformed summary data",
		"originalPodCount", originalPodCount,
		"convertedPodCount", convertedPods,
		"skippedPodCount", skippedPods,
		"targetNamespace", KubeoceanWorkerNamespace,
		"nodeName", summary.Node.NodeName)
}

// aggregateNodeStats aggregates CPU and Memory stats from all pods/containers to Node level
func (mc *MetricsCollector) aggregateNodeStats(summary *Summary) {
	if summary == nil || len(summary.Pods) == 0 {
		return
	}

	// Initialize aggregation variables
	var (
		totalCPUUsageCoreNanoSeconds uint64
		totalCPUUsageNanoCores       uint64
		totalMemoryWorkingSetBytes   uint64
		totalMemoryUsageBytes        uint64
		totalMemoryRSSBytes          uint64
		totalMemoryPageFaults        uint64
		totalMemoryMajorPageFaults   uint64

		cpuCount              int
		memoryCount           int
		latestCPUTime         metav1.Time
		latestMemoryTime      metav1.Time
		hasAnyCPUStats        bool
		hasAnyMemoryStats     bool
	)

	// Aggregate from all pods and their containers
	for _, pod := range summary.Pods {
		for _, container := range pod.Containers {
			// Aggregate CPU stats
			if container.CPU != nil {
				hasAnyCPUStats = true
				if container.CPU.UsageCoreNanoSeconds != nil {
					totalCPUUsageCoreNanoSeconds += *container.CPU.UsageCoreNanoSeconds
					cpuCount++
				}
				if container.CPU.UsageNanoCores != nil {
					totalCPUUsageNanoCores += *container.CPU.UsageNanoCores
				}
				// Track latest timestamp
				if container.CPU.Time.After(latestCPUTime.Time) {
					latestCPUTime = container.CPU.Time
				}
			}

			// Aggregate Memory stats
			if container.Memory != nil {
				hasAnyMemoryStats = true
				if container.Memory.WorkingSetBytes != nil {
					totalMemoryWorkingSetBytes += *container.Memory.WorkingSetBytes
					memoryCount++
				}
				if container.Memory.UsageBytes != nil {
					totalMemoryUsageBytes += *container.Memory.UsageBytes
				}
				if container.Memory.RSSBytes != nil {
					totalMemoryRSSBytes += *container.Memory.RSSBytes
				}
				if container.Memory.PageFaults != nil {
					totalMemoryPageFaults += *container.Memory.PageFaults
				}
				if container.Memory.MajorPageFaults != nil {
					totalMemoryMajorPageFaults += *container.Memory.MajorPageFaults
				}
				// Track latest timestamp
				if container.Memory.Time.After(latestMemoryTime.Time) {
					latestMemoryTime = container.Memory.Time
				}
			}
		}
	}

	// Update Node CPU stats with aggregated values
	if hasAnyCPUStats {
		if summary.Node.CPU == nil {
			summary.Node.CPU = &CPUStats{}
		}
		summary.Node.CPU.Time = latestCPUTime
		if cpuCount > 0 {
			summary.Node.CPU.UsageCoreNanoSeconds = &totalCPUUsageCoreNanoSeconds
		}
		summary.Node.CPU.UsageNanoCores = &totalCPUUsageNanoCores

		mc.log.V(2).Info("Aggregated Node CPU stats",
			"nodeName", summary.Node.NodeName,
			"totalUsageCoreNanoSeconds", totalCPUUsageCoreNanoSeconds,
			"totalUsageNanoCores", totalCPUUsageNanoCores,
			"containerCount", cpuCount)
	}

	// Update Node Memory stats with aggregated values
	if hasAnyMemoryStats {
		if summary.Node.Memory == nil {
			summary.Node.Memory = &MemoryStats{}
		}
		summary.Node.Memory.Time = latestMemoryTime
		if memoryCount > 0 {
			summary.Node.Memory.WorkingSetBytes = &totalMemoryWorkingSetBytes
		}
		summary.Node.Memory.UsageBytes = &totalMemoryUsageBytes
		summary.Node.Memory.RSSBytes = &totalMemoryRSSBytes
		summary.Node.Memory.PageFaults = &totalMemoryPageFaults
		summary.Node.Memory.MajorPageFaults = &totalMemoryMajorPageFaults

		mc.log.V(2).Info("Aggregated Node Memory stats",
			"nodeName", summary.Node.NodeName,
			"totalWorkingSetBytes", totalMemoryWorkingSetBytes,
			"totalUsageBytes", totalMemoryUsageBytes,
			"totalRSSBytes", totalMemoryRSSBytes,
			"containerCount", memoryCount)
	}
}

// parseLogOptions parses log options from query parameters (same as server.go)
func (mc *MetricsCollector) parseLogOptions(query map[string][]string) (ContainerLogOpts, error) {
	opts := ContainerLogOpts{}

	if tailLines := getFirstValue(query, "tailLines"); tailLines != "" {
		tail, err := strconv.Atoi(tailLines)
		if err != nil {
			return opts, fmt.Errorf("invalid tailLines: %w", err)
		}
		if tail < 0 {
			return opts, fmt.Errorf("tailLines must be non-negative")
		}
		opts.Tail = tail
	}

	if follow := getFirstValue(query, "follow"); follow != "" {
		followBool, err := strconv.ParseBool(follow)
		if err != nil {
			return opts, fmt.Errorf("invalid follow: %w", err)
		}
		opts.Follow = followBool
	}

	if limitBytes := getFirstValue(query, "limitBytes"); limitBytes != "" {
		limit, err := strconv.Atoi(limitBytes)
		if err != nil {
			return opts, fmt.Errorf("invalid limitBytes: %w", err)
		}
		if limit < 1 {
			return opts, fmt.Errorf("limitBytes must be positive")
		}
		opts.LimitBytes = limit
	}

	if previous := getFirstValue(query, "previous"); previous != "" {
		prev, err := strconv.ParseBool(previous)
		if err != nil {
			return opts, fmt.Errorf("invalid previous: %w", err)
		}
		opts.Previous = prev
	}

	if sinceSeconds := getFirstValue(query, "sinceSeconds"); sinceSeconds != "" {
		since, err := strconv.Atoi(sinceSeconds)
		if err != nil {
			return opts, fmt.Errorf("invalid sinceSeconds: %w", err)
		}
		if since < 1 {
			return opts, fmt.Errorf("sinceSeconds must be positive")
		}
		opts.SinceSeconds = since
	}

	if sinceTime := getFirstValue(query, "sinceTime"); sinceTime != "" {
		since, err := time.Parse(time.RFC3339, sinceTime)
		if err != nil {
			return opts, fmt.Errorf("invalid sinceTime: %w", err)
		}
		if opts.SinceSeconds > 0 {
			return opts, fmt.Errorf("both sinceSeconds and sinceTime cannot be set")
		}
		opts.SinceTime = since
	}

	if timestamps := getFirstValue(query, "timestamps"); timestamps != "" {
		ts, err := strconv.ParseBool(timestamps)
		if err != nil {
			return opts, fmt.Errorf("invalid timestamps: %w", err)
		}
		opts.Timestamps = ts
	}

	return opts, nil
}

// getExecOptions parses exec options from the request - same as server.go
func getExecOptions(req *http.Request, log logr.Logger) (*remoteCommandOptions, error) {
	// Add debug info, consistent with tke_vnode
	log.Info("Exec request details",
		"method", req.Method,
		"url", req.URL.String(),
		"queryParams", req.URL.Query(),
	)

	// Support two parameter formats, consistent with tke_vnode:
	// 1. Standard format: stdin=true, stdout=true, stderr=true, tty=true
	// 2. Numeric format: stdin=1, stdout=1, stderr=1, tty=1
	query := req.URL.Query()

	// TTY parameter
	ttyStr := query.Get("tty")
	tty := ttyStr == trueStr || ttyStr == oneStr

	// Stdin parameter - use "input" consistent with tke_vnode
	stdinStr := query.Get("input")
	stdin := stdinStr == trueStr || stdinStr == oneStr

	// Stdout parameter - use "output" consistent with tke_vnode
	stdoutStr := query.Get("output")
	stdout := stdoutStr == trueStr || stdoutStr == oneStr

	// Stderr parameter - use "stderr" consistent with tke_vnode
	stderrStr := query.Get("stderr")
	stderr := stderrStr == trueStr || stderrStr == oneStr

	log.Info("Parsed exec params",
		"tty", fmt.Sprintf("%s(%t)", ttyStr, tty),
		"stdin", fmt.Sprintf("%s(%t)", stdinStr, stdin),
		"stdout", fmt.Sprintf("%s(%t)", stdoutStr, stdout),
		"stderr", fmt.Sprintf("%s(%t)", stderrStr, stderr),
	)

	if tty && stderr {
		return nil, errors.New("cannot exec with tty and stderr")
	}

	if !stdin && !stdout && !stderr {
		log.Info("ERROR: No streams specified",
			"stdin", stdin,
			"stdout", stdout,
			"stderr", stderr,
		)
		return nil, errors.New("you must specify at least one of stdin, stdout, stderr")
	}

	return &remoteCommandOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		TTY:    tty,
	}, nil
}

// metricsCollectorExecContext implements the Executor interface for metrics collector
type metricsCollectorExecContext struct {
	collector *MetricsCollector
	namespace string
	pod       string
	container string
	ctx       context.Context
}

// ExecInContainer implements remotecommand.Executor interface
func (c *metricsCollectorExecContext) ExecInContainer(name string, uid types.UID, container string, cmd []string, in io.Reader, out, err io.WriteCloser, tty bool, resize <-chan clientremotecommand.TerminalSize, timeout time.Duration) error {
	// Create execIO like official virtual-kubelet
	eio := &metricsCollectorExecIO{
		tty:    tty,
		stdin:  in,
		stdout: out,
		stderr: err,
	}

	if tty {
		eio.chResize = make(chan TermSize)
	}

	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()

	if tty {
		go func() {
			send := func(s clientremotecommand.TerminalSize) bool {
				select {
				case eio.chResize <- TermSize{Width: s.Width, Height: s.Height}:
					return false
				case <-ctx.Done():
					return true
				}
			}

			for {
				select {
				case s := <-resize:
					if send(s) {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Call our Kubelet proxy with the execIO
	return c.collector.kubeletProxy.RunInContainer(c.ctx, c.namespace, c.pod, c.container, cmd, eio)
}

// metricsCollectorExecIO implements AttachIO interface for metrics collector
type metricsCollectorExecIO struct {
	tty      bool
	stdin    io.Reader
	stdout   io.WriteCloser
	stderr   io.WriteCloser
	chResize chan TermSize
}

func (e *metricsCollectorExecIO) TTY() bool {
	return e.tty
}

func (e *metricsCollectorExecIO) Stdin() io.Reader {
	return e.stdin
}

func (e *metricsCollectorExecIO) Stdout() io.Writer {
	return e.stdout
}

func (e *metricsCollectorExecIO) Stderr() io.Writer {
	return e.stderr
}

func (e *metricsCollectorExecIO) HasStdin() bool {
	return e.stdin != nil
}

func (e *metricsCollectorExecIO) HasStdout() bool {
	return e.stdout != nil
}

func (e *metricsCollectorExecIO) HasStderr() bool {
	return e.stderr != nil
}

func (e *metricsCollectorExecIO) Resize() <-chan TermSize {
	return e.chResize
}

// Note: getFirstValue function and remoteCommandOptions type are defined in server.go and shared across the package