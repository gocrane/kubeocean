/*
Copyright 2025 The Kubeocean Authors

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

package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

const (
	// ClusterBinding status constants
	StatusDeleting = "Deleting"
	StatusPending  = "Pending"
	StatusReady    = "Ready"
	StatusFailed   = "Failed"
)

// ClusterBindingCollector implements prometheus.Collector interface
// for collecting ClusterBinding resource status statistics
type ClusterBindingCollector struct {
	client client.Client
	logger logr.Logger
	mu     sync.RWMutex

	// Prometheus metric descriptor
	desc *prometheus.Desc
}

// NewClusterBindingCollector creates a new ClusterBindingCollector
func NewClusterBindingCollector(client client.Client, logger logr.Logger) *ClusterBindingCollector {
	return &ClusterBindingCollector{
		client: client,
		logger: logger,
		desc: prometheus.NewDesc(
			"kubeocean_clusterbindings_total",
			"Total number of clusterbindings by status",
			[]string{"status"},
			nil,
		),
	}
}

// Describe implements prometheus.Collector interface
func (c *ClusterBindingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect implements prometheus.Collector interface
func (c *ClusterBindingCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get all ClusterBinding resources
	var clusterBindingList cloudv1beta1.ClusterBindingList
	if err := c.client.List(ctx, &clusterBindingList); err != nil {
		c.logger.Error(err, "Failed to list ClusterBinding resources for metrics collection")
		return
	}

	// Count the number of each status
	statusCounts := map[string]int{
		StatusDeleting: 0,
		StatusPending:  0,
		StatusReady:    0,
		StatusFailed:   0,
	}

	for _, cb := range clusterBindingList.Items {
		status := c.getClusterBindingStatus(&cb)
		statusCounts[status]++
	}

	// Send metrics to Prometheus
	for status, count := range statusCounts {
		metric, err := prometheus.NewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			float64(count),
			status,
		)
		if err != nil {
			c.logger.Error(err, "Failed to create metric", "status", status, "count", count)
			continue
		}
		ch <- metric
	}

	c.logger.V(1).Info("ClusterBinding metrics collected",
		"deleting", statusCounts[StatusDeleting],
		"pending", statusCounts[StatusPending],
		"ready", statusCounts[StatusReady],
		"failed", statusCounts[StatusFailed],
	)
}

// getClusterBindingStatus determines the current status of a ClusterBinding
func (c *ClusterBindingCollector) getClusterBindingStatus(cb *cloudv1beta1.ClusterBinding) string {
	// If DeletionTimestamp is not empty, status is Deleting
	if cb.DeletionTimestamp != nil {
		return StatusDeleting
	}

	// Determine status based on Phase
	switch cb.Status.Phase {
	case cloudv1beta1.ClusterBindingPhaseReady:
		return StatusReady
	case cloudv1beta1.ClusterBindingPhaseFailed:
		return StatusFailed
	case cloudv1beta1.ClusterBindingPhasePending:
		return StatusPending
	default:
		// If Phase is empty or unknown, default to Pending
		return StatusPending
	}
}

// SetClient updates the client (mainly for testing)
func (c *ClusterBindingCollector) SetClient(client client.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = client
}
