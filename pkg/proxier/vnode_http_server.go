package proxier

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// VNodeHTTPServer handles HTTP requests for VNode metrics
type VNodeHTTPServer struct {
	metricsCollector *MetricsCollector
	log              logr.Logger
}

// NewVNodeHTTPServer creates a new VNode HTTP server
func NewVNodeHTTPServer(metricsCollector *MetricsCollector, log logr.Logger) *VNodeHTTPServer {
	return &VNodeHTTPServer{
		metricsCollector: metricsCollector,
		log:              log,
	}
}

// createVNodeHandler creates HTTP handler for VNode metrics
func (s *VNodeHTTPServer) createVNodeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract VNode name from path
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(path, "/")

		if len(parts) < 2 {
			s.writeErrorResponse(w, http.StatusBadRequest, "Invalid path format. Expected: /{vnodename}/metrics")
			return
		}

		vnodeName := parts[0]
		endpoint := parts[1]

		// Only handle /metrics endpoint
		if endpoint != "metrics" {
			s.writeErrorResponse(w, http.StatusNotFound, fmt.Sprintf("Endpoint '%s' not found. Only /metrics is supported", endpoint))
			return
		}

		// Get port for VNode
		port, exists := VNodePortMapper.GetPortByVNodeName(vnodeName)
		if !exists {
			s.writeErrorResponse(w, http.StatusNotFound, fmt.Sprintf("VNode '%s' not found", vnodeName))
			return
		}

		// Get metrics data from MetricsCollector
		metricsData, exists := s.metricsCollector.GetMetricsData(port)
		if !exists {
			s.writeErrorResponse(w, http.StatusNotFound, fmt.Sprintf("No metrics data available for VNode '%s' (port: %s)", vnodeName, port))
			return
		}

		// Return metrics data
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(metricsData)

		s.log.V(1).Info("Served metrics for VNode", "vnode", vnodeName, "port", port, "dataSize", len(metricsData))
	}
}

// writeErrorResponse writes an error response
func (s *VNodeHTTPServer) writeErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(statusCode)
	fmt.Fprintf(w, "Error %d: %s\n", statusCode, message)

	s.log.V(1).Info("HTTP error response", "statusCode", statusCode, "message", message)
}

// StartVNodeHTTPServer starts the VNode HTTP server
func StartVNodeHTTPServer(metricsCollector *MetricsCollector, log logr.Logger, basePort int) error {
	server := NewVNodeHTTPServer(metricsCollector, log)

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.createVNodeHandler())

	// Try ports starting from basePort
	for port := basePort; port < basePort+10; port++ {
		addr := fmt.Sprintf(":%d", port)

		httpServer := &http.Server{
			Addr:    addr,
			Handler: mux,
		}

		// Try to start the server in a goroutine and check if it starts successfully
		started := make(chan bool, 1)
		go func(server *http.Server, port int) {
			log.Info("Starting VNode HTTP server", "port", port, "endpoint", fmt.Sprintf("http://localhost:%d/{vnodename}/metrics", port))

			started <- true
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error(err, "VNode HTTP server error", "port", port)
			}
		}(httpServer, port)

		// Wait a short time to see if the server starts successfully
		select {
		case <-started:
			log.Info("VNode HTTP server started successfully", "port", port)
			return nil
		case <-time.After(100 * time.Millisecond):
			// Server didn't start quickly, try next port
			log.V(1).Info("Port busy, trying next port", "port", port)
			continue
		}
	}

	return fmt.Errorf("failed to start VNode HTTP server on any port from %d to %d", basePort, basePort+9)
}
