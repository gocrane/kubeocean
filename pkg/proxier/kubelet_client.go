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
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
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
