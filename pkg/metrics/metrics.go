package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// ClusterBindingTotal tracks the total number of cluster bindings
	ClusterBindingTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kubeocean_cluster_bindings_total",
			Help: "Total number of cluster bindings",
		},
		[]string{"phase"},
	)

	// SyncLatency tracks the latency of synchronization operations
	SyncLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kubeocean_sync_duration_seconds",
			Help:    "Duration of synchronization operations",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"cluster", "operation", "direction"},
	)

	// SyncErrors tracks synchronization errors
	SyncErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeocean_sync_errors_total",
			Help: "Total number of synchronization errors",
		},
		[]string{"cluster", "operation", "error_type"},
	)

	// VirtualNodesTotal tracks the total number of virtual nodes
	VirtualNodesTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kubeocean_virtual_nodes_total",
			Help: "Total number of virtual nodes",
		},
		[]string{"cluster", "status"},
	)

	// ResourceUtilization tracks resource utilization
	ResourceUtilization = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kubeocean_resource_utilization",
			Help: "Resource utilization percentage",
		},
		[]string{"cluster", "node", "resource"},
	)

	// LeaderElectionStatus tracks leader election status
	LeaderElectionStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kubeocean_leader_election_status",
			Help: "Leader election status (1 for leader, 0 for follower)",
		},
		[]string{"component", "instance"},
	)
)

func init() {
	// Register metrics with the global prometheus registry
	metrics.Registry.MustRegister(
		ClusterBindingTotal,
		SyncLatency,
		SyncErrors,
		VirtualNodesTotal,
		ResourceUtilization,
		LeaderElectionStatus,
	)
}

// InitMetrics initializes the metrics system
func InitMetrics() {
	// Initialize default metric values
	LeaderElectionStatus.WithLabelValues("kubeocean-manager", "unknown").Set(0)
}
