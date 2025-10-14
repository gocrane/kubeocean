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

package metrics

import (
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// CreatePodTotal counts the total number of physical pod creation attempts
	CreatePodTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeocean_syncer_create_pod_total",
			Help: "Total number of physical pod creation attempts",
		},
		[]string{"clusterbinding"},
	)

	// CreatePodErrorsTotal counts the total number of physical pod creation errors
	CreatePodErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeocean_syncer_create_pod_errors_total",
			Help: "Total number of physical pod creation errors",
		},
		[]string{"clusterbinding"},
	)

	// PodCreatedTotal counts the total number of successfully created physical pods
	PodCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeocean_syncer_pod_created_total",
			Help: "Total number of successfully created physical pods",
		},
		[]string{"clusterbinding"},
	)

	// PodCreatedFailedTotal counts the total number of pods that failed and were marked as Failed
	PodCreatedFailedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kubeocean_syncer_pod_created_failed_total",
			Help: "Total number of pods that failed and were marked as Failed",
		},
		[]string{"clusterbinding"},
	)
)

// RegisterVNodeCollector registers the VNode metrics collector with the metrics registry
func RegisterMetricsCollector(clusterbindingName string, virtualClient, physicalClient client.Client, logger logr.Logger) {
	MetricsCollectorInst = NewMetricsCollector(clusterbindingName, virtualClient, physicalClient)
	metrics.Registry.MustRegister(MetricsCollectorInst)
	metrics.Registry.MustRegister(CreatePodTotal)
	metrics.Registry.MustRegister(CreatePodErrorsTotal)
	metrics.Registry.MustRegister(PodCreatedTotal)
	metrics.Registry.MustRegister(PodCreatedFailedTotal)
	logger.Info("Syncer metrics collector registered")
}
