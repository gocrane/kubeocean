package proxier

import (
	"fmt"
	"net/http"
	"strings"

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

		// Check if metrics data is empty
		if len(metricsData) == 0 {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "# No metrics data available for VNode '%s' (port: %s)\n", vnodeName, port)
			s.log.V(1).Info("Returned empty metrics data comment", "vnode", vnodeName, "port", port)
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

	addr := fmt.Sprintf(":%d", basePort)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	log.Info("Starting VNode HTTP server", "port", basePort, "endpoint", fmt.Sprintf("http://localhost:%d/{vnodename}/metrics", basePort))
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start VNode HTTP server on port %d: %w", basePort, err)
	}
	return nil
}
