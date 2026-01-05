/*
Copyright 2025 The Kubeocean Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxier

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestAgent creates a minimal VNodeProxierAgent for testing
func createTestAgent() *VNodeProxierAgent {
	return &VNodeProxierAgent{
		metricsCache: make(map[string][]byte),
		log:          logr.Discard(),
	}
}

// setTestMetricsData sets metrics data in the agent's cache
func setTestMetricsData(agent *VNodeProxierAgent, port string, data []byte) {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	agent.metricsCache[port] = data
}

func TestNewVNodeHTTPServer(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()

	server := NewVNodeHTTPServer(agent, log)

	assert.NotNil(t, server)
	assert.Equal(t, agent, server.vnodeProxierAgent)
	assert.Equal(t, log, server.log)
}

func TestCreateVNodeHandler_Success(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()

	// Setup: Add VNode mapping and metrics data
	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	
	metricsData := []byte(`# HELP container_cpu_usage_seconds_total Total CPU usage
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{container="nginx"} 123.45
`)
	setTestMetricsData(agent, "10250", metricsData)

	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	req := httptest.NewRequest("GET", "/vnode-1/metrics", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, string(metricsData), w.Body.String())
}

func TestCreateVNodeHandler_InvalidPathFormat(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	tests := []struct {
		name         string
		path         string
		expectedCode int
		expectedMsg  string
	}{
		{
			name:         "Path with only vnode name",
			path:         "/vnode-1",
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "Invalid path format",
		},
		{
			name:         "Empty path",
			path:         "/",
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "Invalid path format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			handler(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedMsg)
			assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
		})
	}
}

func TestCreateVNodeHandler_UnsupportedEndpoint(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	tests := []struct {
		name        string
		path        string
		expectedMsg string
	}{
		{
			name:        "Health endpoint not supported",
			path:        "/vnode-1/health",
			expectedMsg: "Endpoint 'health' not found",
		},
		{
			name:        "Stats endpoint not supported",
			path:        "/vnode-1/stats",
			expectedMsg: "Endpoint 'stats' not found",
		},
		{
			name:        "Unknown endpoint",
			path:        "/vnode-1/unknown",
			expectedMsg: "Endpoint 'unknown' not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			handler(w, req)

			assert.Equal(t, http.StatusNotFound, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedMsg)
			assert.Contains(t, w.Body.String(), "Only /metrics is supported")
		})
	}
}

func TestCreateVNodeHandler_VNodeNotFound(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()

	req := httptest.NewRequest("GET", "/vnode-nonexistent/metrics", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "VNode 'vnode-nonexistent' not found")
}

func TestCreateVNodeHandler_NoMetricsData(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	// Don't set any metrics data for this port

	req := httptest.NewRequest("GET", "/vnode-1/metrics", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "No metrics data available for VNode 'vnode-1' (port: 10250)")
}

func TestCreateVNodeHandler_EmptyMetricsData(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte{})

	req := httptest.NewRequest("GET", "/vnode-1/metrics", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "No metrics data available")
	assert.Contains(t, w.Body.String(), "vnode-1")
}

func TestCreateVNodeHandler_DifferentVNodes(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	VNodePortMapper.AddVNodePort("vnode-2", "10251")
	VNodePortMapper.AddVNodePort("vnode-3", "10252")

	metrics1 := []byte("# Metrics for vnode-1\nmetric1 100\n")
	metrics2 := []byte("# Metrics for vnode-2\nmetric2 200\n")
	metrics3 := []byte("# Metrics for vnode-3\nmetric3 300\n")

	setTestMetricsData(agent, "10250", metrics1)
	setTestMetricsData(agent, "10251", metrics2)
	setTestMetricsData(agent, "10252", metrics3)

	tests := []struct {
		vnodeName       string
		expectedMetrics string
	}{
		{"vnode-1", "# Metrics for vnode-1"},
		{"vnode-2", "# Metrics for vnode-2"},
		{"vnode-3", "# Metrics for vnode-3"},
	}

	for _, tt := range tests {
		t.Run("Get metrics for "+tt.vnodeName, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/"+tt.vnodeName+"/metrics", nil)
			w := httptest.NewRecorder()

			handler(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedMetrics)
		})
	}
}

func TestCreateVNodeHandler_PathWithExtraSegments(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte("test metrics"))

	// Path with extra segments after /metrics should still work
	// because we only check parts[0] and parts[1]
	req := httptest.NewRequest("GET", "/vnode-1/metrics/extra/segments", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	// Should succeed because parts[0]="vnode-1" and parts[1]="metrics"
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCreateVNodeHandler_SpecialCharactersInVNodeName(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	specialNames := []string{
		"vnode-with-dashes",
		"vnode.with.dots",
		"vnode_with_underscores",
	}

	VNodePortMapper.Clear()

	for i, name := range specialNames {
		port := "1025" + string(rune('0'+i))
		VNodePortMapper.AddVNodePort(name, port)
		setTestMetricsData(agent, port, []byte("metrics for "+name))

		req := httptest.NewRequest("GET", "/"+name+"/metrics", nil)
		w := httptest.NewRecorder()

		handler(w, req)

		assert.Equal(t, http.StatusOK, w.Code, "Failed for vnode name: %s", name)
		assert.Contains(t, w.Body.String(), name)
	}
}

func TestWriteErrorResponse(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)

	tests := []struct {
		name         string
		statusCode   int
		message      string
		expectedBody string
	}{
		{
			name:         "400 Bad Request",
			statusCode:   http.StatusBadRequest,
			message:      "Invalid input",
			expectedBody: "Error 400: Invalid input\n",
		},
		{
			name:         "404 Not Found",
			statusCode:   http.StatusNotFound,
			message:      "Resource not found",
			expectedBody: "Error 404: Resource not found\n",
		},
		{
			name:         "500 Internal Server Error",
			statusCode:   http.StatusInternalServerError,
			message:      "Internal error",
			expectedBody: "Error 500: Internal error\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			server.writeErrorResponse(w, tt.statusCode, tt.message)

			assert.Equal(t, tt.statusCode, w.Code)
			assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
			assert.Equal(t, tt.expectedBody, w.Body.String())
		})
	}
}

func TestVNodeHTTPServer_HTTPMethods(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte("test metrics"))

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
		http.MethodHead,
	}

	for _, method := range methods {
		t.Run(method+" request", func(t *testing.T) {
			req := httptest.NewRequest(method, "/vnode-1/metrics", nil)
			w := httptest.NewRecorder()

			handler(w, req)

			// All methods should be accepted (handler doesn't check method)
			// GET and HEAD should succeed
			if method == http.MethodGet || method == http.MethodHead {
				assert.Equal(t, http.StatusOK, w.Code)
			}
		})
	}
}

func TestVNodeHTTPServer_ConcurrentRequests(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte("test metrics"))

	var wg sync.WaitGroup
	numRequests := 100

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			req := httptest.NewRequest("GET", "/vnode-1/metrics", nil)
			w := httptest.NewRecorder()

			handler(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
		}()
	}

	wg.Wait()
}

func TestVNodeHTTPServer_LargeMetricsPayload(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")

	// Create large metrics payload (1MB)
	var buffer bytes.Buffer
	metricLine := "container_cpu_usage_seconds_total{container=\"nginx\"} 123.45\n"
	for i := 0; i < 20000; i++ {
		buffer.WriteString(metricLine)
	}
	largeMetrics := buffer.Bytes()

	setTestMetricsData(agent, "10250", largeMetrics)

	req := httptest.NewRequest("GET", "/vnode-1/metrics", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, len(largeMetrics), len(w.Body.Bytes()))
	assert.Equal(t, largeMetrics, w.Body.Bytes())
}

func TestVNodeHTTPServer_QueryParameters(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte("test metrics"))

	// Query parameters should be ignored
	req := httptest.NewRequest("GET", "/vnode-1/metrics?format=json&verbose=true", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "test metrics", w.Body.String())
}

func TestVNodeHTTPServer_Headers(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte("test metrics"))

	req := httptest.NewRequest("GET", "/vnode-1/metrics", nil)
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "Prometheus/2.0")
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8", w.Header().Get("Content-Type"))
}

func TestVNodeHTTPServer_CaseSensitivity(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte("test metrics"))

	tests := []struct {
		name         string
		path         string
		expectedCode int
	}{
		{
			name:         "Correct case",
			path:         "/vnode-1/metrics",
			expectedCode: http.StatusOK,
		},
		{
			name:         "Different case vnode name",
			path:         "/VNODE-1/metrics",
			expectedCode: http.StatusNotFound, // Case sensitive
		},
		{
			name:         "Different case endpoint",
			path:         "/vnode-1/Metrics",
			expectedCode: http.StatusNotFound, // Case sensitive
		},
		{
			name:         "All uppercase",
			path:         "/VNODE-1/METRICS",
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			handler(w, req)

			assert.Equal(t, tt.expectedCode, w.Code)
		})
	}
}

func TestVNodeHTTPServer_MetricsWithDifferentFormats(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()

	tests := []struct {
		name        string
		vnodeName   string
		port        string
		metricsData []byte
	}{
		{
			name:      "Prometheus exposition format",
			vnodeName: "vnode-1",
			port:      "10250",
			metricsData: []byte(`# HELP metric1 Help text
# TYPE metric1 counter
metric1{label="value"} 123.45
`),
		},
		{
			name:      "Multiple metrics",
			vnodeName: "vnode-2",
			port:      "10251",
			metricsData: []byte(`metric1 100
metric2 200
metric3 300
`),
		},
		{
			name:      "Metrics with complex labels",
			vnodeName: "vnode-3",
			port:      "10252",
			metricsData: []byte(`metric{a="1",b="2",c="3"} 456
`),
		},
		{
			name:        "Binary data (non-UTF8)",
			vnodeName:   "vnode-4",
			port:        "10253",
			metricsData: []byte{0xFF, 0xFE, 0xFD, 0xFC},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			VNodePortMapper.AddVNodePort(tt.vnodeName, tt.port)
			setTestMetricsData(agent, tt.port, tt.metricsData)

			req := httptest.NewRequest("GET", "/"+tt.vnodeName+"/metrics", nil)
			w := httptest.NewRecorder()

			handler(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tt.metricsData, w.Body.Bytes())
		})
	}
}

func TestVNodeHTTPServer_NilLogger(t *testing.T) {
	agent := createTestAgent()
	
	// Create server with zero-value logger (should not panic)
	server := &VNodeHTTPServer{
		vnodeProxierAgent: agent,
		log:               logr.Logger{}, // zero-value logger
	}

	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte("test"))

	req := httptest.NewRequest("GET", "/vnode-1/metrics", nil)
	w := httptest.NewRecorder()

	// Should not panic
	require.NotPanics(t, func() {
		handler(w, req)
	})

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVNodeHTTPServer_PortUpdate(t *testing.T) {
	agent := createTestAgent()
	log := logr.Discard()
	server := NewVNodeHTTPServer(agent, log)
	handler := server.createVNodeHandler()

	VNodePortMapper.Clear()
	
	// Initially map to port 10250
	VNodePortMapper.AddVNodePort("vnode-1", "10250")
	setTestMetricsData(agent, "10250", []byte("metrics on port 10250"))

	req := httptest.NewRequest("GET", "/vnode-1/metrics", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "port 10250")

	// Update to port 10251
	VNodePortMapper.AddVNodePort("vnode-1", "10251")
	setTestMetricsData(agent, "10251", []byte("metrics on port 10251"))

	req = httptest.NewRequest("GET", "/vnode-1/metrics", nil)
	w = httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "port 10251")
}
