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
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// mockTokenManager is a mock implementation of TokenManager for testing
type mockTokenManager struct {
	token string
	err   error
}

func (m *mockTokenManager) GetToken() (string, error) {
	return m.token, m.err
}

// TestNewKubeletClient tests the NewKubeletClient constructor
func TestNewKubeletClient(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "create new kubelet client successfully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logr.Discard()
			tokenManager := &TokenManager{
				Log:   log,
				Token: "test-token",
			}

			client := NewKubeletClient(log, tokenManager)

			assert.NotNil(t, client)
			assert.NotNil(t, client.HTTPClient)
			assert.NotNil(t, client.TokenManager)
			assert.Equal(t, tokenManager, client.TokenManager)
			assert.Equal(t, log, client.Log)

			// Verify HTTP client configuration
			assert.NotNil(t, client.HTTPClient.Transport)
			assert.Equal(t, 30*time.Second, client.HTTPClient.Timeout)
		})
	}
}

// TestDecompressResponseBody tests the decompressResponseBody function
func TestDecompressResponseBody(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		compress       bool
		contentEncoding string
		wantErr        bool
		expectedBody   string
	}{
		{
			name:         "decompress gzipped content",
			body:         "test metrics data",
			compress:     true,
			contentEncoding: "gzip",
			wantErr:      false,
			expectedBody: "test metrics data",
		},
		{
			name:         "handle plain text content",
			body:         "plain text metrics",
			compress:     false,
			contentEncoding: "",
			wantErr:      false,
			expectedBody: "plain text metrics",
		},
		{
			name:         "handle content with gzip encoding but not compressed",
			body:         "test data",
			compress:     false,
			contentEncoding: "",
			wantErr:      false,
			expectedBody: "test data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Prepare response body
			var bodyReader io.Reader
			if tt.compress {
				var buf bytes.Buffer
				gzWriter := gzip.NewWriter(&buf)
				_, err := gzWriter.Write([]byte(tt.body))
				require.NoError(t, err)
				require.NoError(t, gzWriter.Close())
				bodyReader = &buf
			} else {
				bodyReader = bytes.NewReader([]byte(tt.body))
			}

			// Create mock response
			resp := &http.Response{
				Body:   io.NopCloser(bodyReader),
				Header: http.Header{},
			}
			if tt.contentEncoding != "" {
				resp.Header.Set("Content-Encoding", tt.contentEncoding)
			}

			// Call function
			result, err := decompressResponseBody(resp)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedBody, string(result))
		})
	}
}

// TestKubeletClient_GetCAdvisorMetrics tests GetCAdvisorMetrics method
func TestKubeletClient_GetCAdvisorMetrics(t *testing.T) {
	tests := []struct {
		name           string
		nodeIP         string
		proxierPort    string
		token          string
		tokenErr       error
		serverHandler  http.HandlerFunc
		wantErr        bool
		expectedData   string
		compress       bool
	}{
		{
			name:        "successfully get cAdvisor metrics",
			nodeIP:      "127.0.0.1",
			proxierPort: "8080",
			token:       "valid-token",
			tokenErr:    nil,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				assert.Equal(t, "Bearer valid-token", r.Header.Get("Authorization"))
				assert.Equal(t, "text/plain", r.Header.Get("Accept"))
				assert.Equal(t, "gzip", r.Header.Get("Accept-Encoding"))
				
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, "# HELP container_cpu_usage_seconds_total\n")
			},
			wantErr:      false,
			expectedData: "# HELP container_cpu_usage_seconds_total\n",
			compress:     false,
		},
		{
			name:        "get gzipped cAdvisor metrics",
			nodeIP:      "127.0.0.1",
			proxierPort: "8080",
			token:       "valid-token",
			tokenErr:    nil,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				metricsData := "# HELP container_memory_usage_bytes\n"
				
				var buf bytes.Buffer
				gzWriter := gzip.NewWriter(&buf)
				gzWriter.Write([]byte(metricsData))
				gzWriter.Close()
				
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Content-Encoding", "gzip")
				w.WriteHeader(http.StatusOK)
				w.Write(buf.Bytes())
			},
			wantErr:      false,
			expectedData: "# HELP container_memory_usage_bytes\n",
			compress:     true,
		},
		{
			name:        "fail when token manager returns error",
			nodeIP:      "127.0.0.1",
			proxierPort: "8080",
			token:       "",
			tokenErr:    fmt.Errorf("token not found"),
			wantErr:     true,
		},
		{
			name:        "fail when kubelet returns non-200 status",
			nodeIP:      "127.0.0.1",
			proxierPort: "8080",
			token:       "valid-token",
			tokenErr:    nil,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, "Unauthorized")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock HTTP server
			var server *httptest.Server
			if tt.serverHandler != nil {
				server = httptest.NewTLSServer(tt.serverHandler)
				defer server.Close()
			}

			// Create mock token manager
			tm := &TokenManager{
				Log:   logr.Discard(),
				Token: tt.token,
			}
			if tt.tokenErr != nil {
				tm.Token = ""
			}

			// Create kubelet client
			client := NewKubeletClient(logr.Discard(), tm)

			// If we have a server, use its URL parts
			var nodeIP, port string
			if server != nil {
				// Extract host and port from server URL
				nodeIP = server.Listener.Addr().(*net.TCPAddr).IP.String()
				port = fmt.Sprintf("%d", server.Listener.Addr().(*net.TCPAddr).Port)
			} else {
				nodeIP = tt.nodeIP
				port = tt.proxierPort
			}

			// Call GetCAdvisorMetrics
			ctx := context.Background()
			result, err := client.GetCAdvisorMetrics(ctx, nodeIP, port)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedData, string(result))
		})
	}
}

// TestKubeletClient_GetSummary tests GetSummary method
func TestKubeletClient_GetSummary(t *testing.T) {
	tests := []struct {
		name          string
		nodeIP        string
		port          string
		token         string
		tokenErr      error
		serverHandler http.HandlerFunc
		wantErr       bool
		validateFunc  func(*testing.T, *Summary)
	}{
		{
			name:     "successfully get summary stats",
			nodeIP:   "127.0.0.1",
			port:     "10250",
			token:    "valid-token",
			tokenErr: nil,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				// Verify request
				assert.Equal(t, "Bearer valid-token", r.Header.Get("Authorization"))
				assert.Equal(t, "application/json", r.Header.Get("Accept"))
				assert.Contains(t, r.URL.Path, "/stats/summary")
				
				// Create mock summary response
				summary := &Summary{
					Node: NodeStats{
						NodeName:  "test-node",
						StartTime: metav1.Now(),
					},
					Pods: []PodStats{
						{
							PodRef: PodReference{
								Name:      "test-pod",
								Namespace: "default",
							},
						},
					},
				}
				
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(summary)
			},
			wantErr: false,
			validateFunc: func(t *testing.T, summary *Summary) {
				assert.Equal(t, "test-node", summary.Node.NodeName)
				assert.Len(t, summary.Pods, 1)
				assert.Equal(t, "test-pod", summary.Pods[0].PodRef.Name)
			},
		},
		{
			name:     "fail when token manager returns error",
			nodeIP:   "127.0.0.1",
			port:     "10250",
			token:    "",
			tokenErr: fmt.Errorf("token not found"),
			wantErr:  true,
		},
		{
			name:     "fail when kubelet returns error status",
			nodeIP:   "127.0.0.1",
			port:     "10250",
			token:    "valid-token",
			tokenErr: nil,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, "Internal Server Error")
			},
			wantErr: true,
		},
		{
			name:     "fail when response is invalid JSON",
			nodeIP:   "127.0.0.1",
			port:     "10250",
			token:    "valid-token",
			tokenErr: nil,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, "invalid json")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock HTTP server
			var server *httptest.Server
			if tt.serverHandler != nil {
				server = httptest.NewTLSServer(tt.serverHandler)
				defer server.Close()
			}

			// Create mock token manager
			tm := &TokenManager{
				Log:   logr.Discard(),
				Token: tt.token,
			}
			if tt.tokenErr != nil {
				tm.Token = ""
			}

			// Create kubelet client
			client := NewKubeletClient(logr.Discard(), tm)

			// If we have a server, use its URL parts
			var nodeIP, port string
			if server != nil {
				nodeIP = server.Listener.Addr().(*net.TCPAddr).IP.String()
				port = fmt.Sprintf("%d", server.Listener.Addr().(*net.TCPAddr).Port)
			} else {
				nodeIP = tt.nodeIP
				port = tt.port
			}

			// Call GetSummary
			ctx := context.Background()
			result, err := client.GetSummary(ctx, nodeIP, port)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, result)
			
			if tt.validateFunc != nil {
				tt.validateFunc(t, result)
			}
		})
	}
}

// TestKubeletClient_GetCAdvisorMetrics_Context tests context cancellation
func TestKubeletClient_GetCAdvisorMetrics_Context(t *testing.T) {
	// Create a server that delays response
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tm := &TokenManager{
		Log:   logr.Discard(),
		Token: "test-token",
	}

	client := NewKubeletClient(logr.Discard(), tm)

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	nodeIP := server.Listener.Addr().(*net.TCPAddr).IP.String()
	port := fmt.Sprintf("%d", server.Listener.Addr().(*net.TCPAddr).Port)

	_, err := client.GetCAdvisorMetrics(ctx, nodeIP, port)

	// Should fail due to context timeout
	assert.Error(t, err)
}
