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
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// decompressResponseBody decompresses response body if it's gzipped
func decompressResponseBody(resp *http.Response) ([]byte, error) {
	var reader io.Reader = resp.Body
	contentEncoding := resp.Header.Get("Content-Encoding")

	// Add debug logging
	fmt.Printf("[DEBUG] decompressResponseBody: Content-Encoding=%s, Transfer-Encoding=%v\n",
		contentEncoding, resp.TransferEncoding)

	// Check if response is gzipped
	if strings.Contains(contentEncoding, "gzip") {
		fmt.Printf("[DEBUG] Response is gzipped, creating gzip reader\n")
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	} else {
		fmt.Printf("[DEBUG] Response is not gzipped, reading directly\n")
	}

	// Read the decompressed data
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	fmt.Printf("[DEBUG] Successfully read response body: %d bytes\n", len(body))
	return body, nil
}

// KubeletClient provides methods to interact with the kubelet API
type KubeletClient struct {
	Log          logr.Logger
	TokenManager *TokenManager
	HTTPClient   *http.Client
}

// NewKubeletClient creates a new KubeletClient
func NewKubeletClient(log logr.Logger, tokenManager *TokenManager) *KubeletClient {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Skip TLS certificate verification
		},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &KubeletClient{
		Log:          log,
		TokenManager: tokenManager,
		HTTPClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

// GetCAdvisorMetrics retrieves cAdvisor metrics from a specific node
func (kc *KubeletClient) GetCAdvisorMetrics(ctx context.Context, nodeIP, proxierPort string) ([]byte, error) {
	// Get the current token
	token, err := kc.TokenManager.GetToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Construct the kubelet API URL
	url := fmt.Sprintf("https://%s:%s/metrics/cadvisor", nodeIP, proxierPort)

	// Create the request
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "kubeocean-proxier")

	// Make the request
	kc.Log.V(1).Info("Making request to kubelet API", "url", url, "nodeIP", nodeIP)
	resp, err := kc.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kubelet API returned status %d: %s", resp.StatusCode, resp.Status)
	}

	// Add detailed response header debug logs
	kc.Log.V(2).Info("HTTP Response Info",
		"nodeIP", nodeIP,
		"status", resp.Status,
		"statusCode", resp.StatusCode,
		"contentLength", resp.ContentLength,
		"contentEncoding", resp.Header.Get("Content-Encoding"),
		"transferEncoding", resp.TransferEncoding,
		"headers", resp.Header)

	// Read and decompress response body
	body, err := decompressResponseBody(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	kc.Log.V(1).Info("Successfully retrieved cAdvisor metrics", "nodeIP", nodeIP, "size", len(body))
	return body, nil
}

// Summary data structures (compatible with metrics-server and kubelet /stats/summary API)

// Summary is a top-level container for holding NodeStats and PodStats.
type Summary struct {
	// Overall node stats.
	Node NodeStats `json:"node"`
	// Per-pod stats.
	Pods []PodStats `json:"pods"`
}

// NodeStats holds node-level unprocessed sample stats.
type NodeStats struct {
	// Reference to the measured Node.
	NodeName string `json:"nodeName"`
	// The time at which data collection for this node was started.
	StartTime metav1.Time `json:"startTime"`
	// Stats pertaining to CPU resources.
	CPU *CPUStats `json:"cpu,omitempty"`
	// Stats pertaining to memory (RAM) resources.
	Memory *MemoryStats `json:"memory,omitempty"`
}

// PodStats holds pod-level unprocessed sample stats.
type PodStats struct {
	// Reference to the measured Pod.
	PodRef PodReference `json:"podRef"`
	// The time at which data collection for this pod was started.
	StartTime metav1.Time `json:"startTime"`
	// Stats of containers in the measured pod.
	Containers []ContainerStats `json:"containers"`
}

// ContainerStats holds container-level unprocessed sample stats.
type ContainerStats struct {
	// Reference to the measured container.
	Name string `json:"name"`
	// The time the container started.
	StartTime metav1.Time `json:"startTime"`
	// Stats pertaining to CPU resources.
	CPU *CPUStats `json:"cpu,omitempty"`
	// Stats pertaining to memory (RAM) resources.
	Memory *MemoryStats `json:"memory,omitempty"`
}

// PodReference contains enough information to locate the referenced pod.
type PodReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	UID       string `json:"uid,omitempty"`
}

// CPUStats contains data about CPU usage.
type CPUStats struct {
	// The time at which these stats were updated.
	Time metav1.Time `json:"time"`
	// Cumulative CPU usage (sum of all cores) since object creation.
	UsageCoreNanoSeconds *uint64 `json:"usageCoreNanoSeconds,omitempty"`
	// Total CPU usage (sum of all cores) averaged over the sample window.
	// The "core" unit can be interpreted as CPU core-nanoseconds per second.
	UsageNanoCores *uint64 `json:"usageNanoCores,omitempty"`
}

// MemoryStats contains data about memory usage.
type MemoryStats struct {
	// The time at which these stats were updated.
	Time metav1.Time `json:"time"`
	// Available memory for use. This is defined as the memory limit - workingSet.
	AvailableBytes *uint64 `json:"availableBytes,omitempty"`
	// Total memory in use. This includes all memory regardless of when it was accessed.
	UsageBytes *uint64 `json:"usageBytes,omitempty"`
	// The amount of working set memory. This includes recently accessed memory,
	// dirty memory, and kernel memory. WorkingSetBytes is <= UsageBytes
	WorkingSetBytes *uint64 `json:"workingSetBytes,omitempty"`
	// The amount of anonymous and swap cache memory (includes transparent hugepages).
	RSSBytes *uint64 `json:"rssBytes,omitempty"`
	// Cumulative number of minor page faults.
	PageFaults *uint64 `json:"pageFaults,omitempty"`
	// Cumulative number of major page faults.
	MajorPageFaults *uint64 `json:"majorPageFaults,omitempty"`
}

// GetSummary retrieves summary stats from kubelet (compatible with metrics-server)
func (kc *KubeletClient) GetSummary(ctx context.Context, nodeIP, port string) (*Summary, error) {
	// Get the current token
	token, err := kc.TokenManager.GetToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Construct the kubelet API URL
	url := fmt.Sprintf("https://%s:%s/stats/summary?only_cpu_and_memory=true", nodeIP, port)

	// Create the request
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kubeocean-proxier")

	// Make the request
	kc.Log.V(2).Info("Making request to kubelet summary API", "url", url, "nodeIP", nodeIP)
	resp, err := kc.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("kubelet API returned status %d: %s, body: %s", resp.StatusCode, resp.Status, string(body))
	}

	// Decode JSON response
	var summary Summary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		return nil, fmt.Errorf("failed to decode summary JSON: %w", err)
	}

	kc.Log.V(2).Info("Successfully retrieved summary stats",
		"nodeIP", nodeIP,
		"nodeName", summary.Node.NodeName,
		"podCount", len(summary.Pods))

	return &summary, nil
}