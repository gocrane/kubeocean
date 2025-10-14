package metrics

import (
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// SyncLatency tracks the latency of synchronization operations
	SyncLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kubeocean_manager_sync_duration_seconds",
			Help:    "Duration of synchronization operations",
			Buckets: prometheus.DefBuckets,
		},
	)

	SyncTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeocean_manager_sync_total",
			Help: "Total number of synchronization operations",
		},
		[]string{"clusterbinding"},
	)

	// SyncErrors tracks synchronization errors
	SyncErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeocean_manager_sync_errors_total",
			Help: "Total number of synchronization errors",
		},
		[]string{"clusterbinding"},
	)
)

func init() {
	// Register metrics with the global prometheus registry
	metrics.Registry.MustRegister(
		SyncLatency,
		SyncTotal,
		SyncErrors,
	)
}

// RegisterClusterBindingCollector registers the ClusterBinding collector with the metrics registry
func RegisterClusterBindingCollector(client client.Client, logger logr.Logger) {
	collector := NewClusterBindingCollector(client, logger)
	metrics.Registry.MustRegister(collector)
	logger.Info("ClusterBinding metrics collector registered")
}
