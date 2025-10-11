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
	"context"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/utils"
)

const (
	// Resource types to collect
	ResourceCPU              = "cpu"
	ResourceMemory           = "memory"
	ResourceEphemeralStorage = "ephemeral-storage"
	ResourceGocraneCPU       = "gocrane.io/cpu"
	ResourceGocraneMemory    = "gocrane.io/memory"
	ResourceENIIP            = "tke.cloud.tencent.com/eni-ip"
	ResourceSubENI           = "tke.cloud.tencent.com/sub-eni"
	ResourceDirectENI        = "tke.cloud.tencent.com/direct-eni"
	ResourceNvidiaGPU        = "nvidia.com/gpu"
	ResourceTKESharedRDMA    = "tke.cloud.tencent.com/tke-shared-rdma"
)

// VNodeMetrics stores resource metrics for a single VNode
type VNodeMetrics struct {
	// VNode name
	VNodeName string
	// Base allocatable resources of the VNode (origin)
	AllocatableResources corev1.ResourceList
	// Available resources after policy limits applied
	AvailableResources corev1.ResourceList
	// Physical cluster resource usage (excluding kubeocean-managed pods)
	PhysicalUsage corev1.ResourceList
	// Resource usage by kubeocean-managed pods in physical cluster
	VirtualUsage corev1.ResourceList
}

// MetricsCollector collects and manages VNode resource metrics
// Implements prometheus.Collector interface
type MetricsCollector struct {
	mu                 sync.RWMutex
	metrics            map[string]*VNodeMetrics
	clusterbindingName string

	// Kubernetes clients
	virtualClient  client.Client
	physicalClient client.Client

	// Prometheus metric descriptors
	vnodeCountDesc        *prometheus.Desc
	originResourceDesc    *prometheus.Desc
	availableResourceDesc *prometheus.Desc
	physicalUsageDesc     *prometheus.Desc
	virtualUsageDesc      *prometheus.Desc

	// Cluster-level metric descriptors
	clusterAvailableResourceDesc *prometheus.Desc
	clusterOriginResourceDesc    *prometheus.Desc
	clusterPhysicalUsageDesc     *prometheus.Desc
	clusterVirtualUsageDesc      *prometheus.Desc

	// Pod count metric descriptors
	virtualPodCountDesc  *prometheus.Desc
	physicalPodCountDesc *prometheus.Desc
}

// NewMetricsCollector creates a new MetricsCollector instance
func NewMetricsCollector(clusterbindingName string, virtualClient, physicalClient client.Client) *MetricsCollector {
	return &MetricsCollector{
		clusterbindingName: clusterbindingName,
		virtualClient:      virtualClient,
		physicalClient:     physicalClient,

		metrics: make(map[string]*VNodeMetrics),
		vnodeCountDesc: prometheus.NewDesc(
			"kubeocean_syncer_vnode_total",
			"Total number of virtual nodes",
			[]string{"clusterbinding"},
			nil,
		),
		originResourceDesc: prometheus.NewDesc(
			"kubeocean_syncer_vnode_origin_resource",
			"Origin allocatable resources of virtual nodes",
			[]string{"clusterbinding", "node", "resource"},
			nil,
		),
		availableResourceDesc: prometheus.NewDesc(
			"kubeocean_syncer_vnode_available_resource",
			"Available resources of virtual nodes after policy limits",
			[]string{"clusterbinding", "node", "resource"},
			nil,
		),
		physicalUsageDesc: prometheus.NewDesc(
			"kubeocean_syncer_vnode_physical_usage",
			"Physical cluster resource usage excluding kubeocean-managed pods",
			[]string{"clusterbinding", "node", "resource"},
			nil,
		),
		virtualUsageDesc: prometheus.NewDesc(
			"kubeocean_syncer_vnode_virtual_usage",
			"Resource usage by virtual pods in physical cluster",
			[]string{"clusterbinding", "node", "resource"},
			nil,
		),
		clusterAvailableResourceDesc: prometheus.NewDesc(
			"kubeocean_syncer_cluster_available_resource",
			"Total available resources of the cluster after policy limits",
			[]string{"clusterbinding", "resource"},
			nil,
		),
		clusterOriginResourceDesc: prometheus.NewDesc(
			"kubeocean_syncer_cluster_origin_resource",
			"Total origin allocatable resources of the cluster",
			[]string{"clusterbinding", "resource"},
			nil,
		),
		clusterPhysicalUsageDesc: prometheus.NewDesc(
			"kubeocean_syncer_cluster_physical_usage",
			"Total physical cluster resource usage excluding kubeocean-managed pods",
			[]string{"clusterbinding", "resource"},
			nil,
		),
		clusterVirtualUsageDesc: prometheus.NewDesc(
			"kubeocean_syncer_cluster_virtual_usage",
			"Total resource usage by virtual pods in physical cluster",
			[]string{"clusterbinding", "resource"},
			nil,
		),
		virtualPodCountDesc: prometheus.NewDesc(
			"kubeocean_syncer_virtual_pod_total",
			"Number of virtual pods by phase",
			[]string{"clusterbinding", "phase"},
			nil,
		),
		physicalPodCountDesc: prometheus.NewDesc(
			"kubeocean_syncer_physical_pod_total",
			"Number of physical pods by phase",
			[]string{"clusterbinding", "phase"},
			nil,
		),
	}
}

func (mc *MetricsCollector) SetPhysicalClient(physicalClient client.Client) {
	mc.physicalClient = physicalClient
}

// SetVnodeMetrics sets resource metrics for the specified VNode
func (mc *MetricsCollector) SetVnodeMetrics(
	vnodeName string,
	allocatableResources corev1.ResourceList,
	availableResources corev1.ResourceList,
	physicalUsage corev1.ResourceList,
	virtualUsage corev1.ResourceList,
) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.metrics[vnodeName] = &VNodeMetrics{
		VNodeName:            vnodeName,
		AllocatableResources: allocatableResources.DeepCopy(),
		AvailableResources:   availableResources.DeepCopy(),
		PhysicalUsage:        physicalUsage.DeepCopy(),
		VirtualUsage:         virtualUsage.DeepCopy(),
	}
}

// GetVnodeMetrics gets resource metrics for the specified VNode
func (mc *MetricsCollector) GetVnodeMetrics(vnodeName string) (*VNodeMetrics, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	metrics, exists := mc.metrics[vnodeName]
	if !exists {
		return nil, false
	}

	// Return deep copy to prevent external modifications
	return &VNodeMetrics{
		VNodeName:            metrics.VNodeName,
		AllocatableResources: metrics.AllocatableResources.DeepCopy(),
		AvailableResources:   metrics.AvailableResources.DeepCopy(),
		PhysicalUsage:        metrics.PhysicalUsage.DeepCopy(),
		VirtualUsage:         metrics.VirtualUsage.DeepCopy(),
	}, true
}

// DeleteVnodeMetrics deletes resource metrics for the specified VNode
func (mc *MetricsCollector) DeleteVnodeMetrics(vnodeName string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	delete(mc.metrics, vnodeName)
}

// GetAllMetrics gets resource metrics for all VNodes
func (mc *MetricsCollector) GetAllMetrics() map[string]*VNodeMetrics {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	result := make(map[string]*VNodeMetrics, len(mc.metrics))
	for vnodeName, metrics := range mc.metrics {
		result[vnodeName] = &VNodeMetrics{
			VNodeName:            metrics.VNodeName,
			AllocatableResources: metrics.AllocatableResources.DeepCopy(),
			AvailableResources:   metrics.AvailableResources.DeepCopy(),
			PhysicalUsage:        metrics.PhysicalUsage.DeepCopy(),
			VirtualUsage:         metrics.VirtualUsage.DeepCopy(),
		}
	}

	return result
}

// Clear clears all VNode resource metrics
func (mc *MetricsCollector) Clear() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.metrics = make(map[string]*VNodeMetrics)
}

// Describe implements prometheus.Collector interface
func (mc *MetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- mc.vnodeCountDesc
	ch <- mc.originResourceDesc
	ch <- mc.availableResourceDesc
	ch <- mc.physicalUsageDesc
	ch <- mc.virtualUsageDesc
	ch <- mc.clusterAvailableResourceDesc
	ch <- mc.clusterOriginResourceDesc
	ch <- mc.clusterPhysicalUsageDesc
	ch <- mc.clusterVirtualUsageDesc
	ch <- mc.virtualPodCountDesc
	ch <- mc.physicalPodCountDesc
}

// Collect implements prometheus.Collector interface
func (mc *MetricsCollector) Collect(ch chan<- prometheus.Metric) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	// Emit vnode count metrics
	ch <- prometheus.MustNewConstMetric(
		mc.vnodeCountDesc,
		prometheus.GaugeValue,
		float64(len(mc.metrics)),
		mc.clusterbindingName,
	)

	// Emit resource metrics for each vnode
	for _, m := range mc.metrics {
		mc.collectResourceMetrics(ch, m)
	}

	// Emit cluster-level aggregated metrics
	mc.collectClusterMetrics(ch)

	// Emit pod count metrics
	mc.collectPodMetrics(ch)
}

// collectResourceMetrics collects and emits resource metrics for a single vnode
func (mc *MetricsCollector) collectResourceMetrics(ch chan<- prometheus.Metric, m *VNodeMetrics) {
	// Collect origin resources
	for resourceName, quantity := range m.AllocatableResources {
		if value, ok := mc.convertResourceValue(string(resourceName), quantity); ok {
			ch <- prometheus.MustNewConstMetric(
				mc.originResourceDesc,
				prometheus.GaugeValue,
				value,
				mc.clusterbindingName,
				m.VNodeName,
				string(resourceName),
			)
		}
	}

	// Collect available resources
	for resourceName, quantity := range m.AvailableResources {
		if value, ok := mc.convertResourceValue(string(resourceName), quantity); ok {
			ch <- prometheus.MustNewConstMetric(
				mc.availableResourceDesc,
				prometheus.GaugeValue,
				value,
				mc.clusterbindingName,
				m.VNodeName,
				string(resourceName),
			)
		}
	}

	// Collect physical usage
	for resourceName, quantity := range m.PhysicalUsage {
		if value, ok := mc.convertResourceValue(string(resourceName), quantity); ok {
			ch <- prometheus.MustNewConstMetric(
				mc.physicalUsageDesc,
				prometheus.GaugeValue,
				value,
				mc.clusterbindingName,
				m.VNodeName,
				string(resourceName),
			)
		}
	}

	// Collect kubeocean physical usage
	for resourceName, quantity := range m.VirtualUsage {
		if value, ok := mc.convertResourceValue(string(resourceName), quantity); ok {
			ch <- prometheus.MustNewConstMetric(
				mc.virtualUsageDesc,
				prometheus.GaugeValue,
				value,
				mc.clusterbindingName,
				m.VNodeName,
				string(resourceName),
			)
		}
	}
}

// collectClusterMetrics collects and emits cluster-level aggregated metrics
func (mc *MetricsCollector) collectClusterMetrics(ch chan<- prometheus.Metric) {
	// Aggregate resources from all VNodes
	clusterOriginResource := make(corev1.ResourceList)
	clusterAvailableResource := make(corev1.ResourceList)
	clusterPhysicalUsage := make(corev1.ResourceList)
	clusterVirtualUsage := make(corev1.ResourceList)

	for _, m := range mc.metrics {
		addResourceList(clusterOriginResource, m.AllocatableResources)
		addResourceList(clusterAvailableResource, m.AvailableResources)
		addResourceList(clusterPhysicalUsage, m.PhysicalUsage)
		addResourceList(clusterVirtualUsage, m.VirtualUsage)
	}

	// Emit cluster origin resources
	for resourceName, quantity := range clusterOriginResource {
		if value, ok := mc.convertResourceValue(string(resourceName), quantity); ok {
			ch <- prometheus.MustNewConstMetric(
				mc.clusterOriginResourceDesc,
				prometheus.GaugeValue,
				value,
				mc.clusterbindingName,
				string(resourceName),
			)
		}
	}

	// Emit cluster available resources
	for resourceName, quantity := range clusterAvailableResource {
		if value, ok := mc.convertResourceValue(string(resourceName), quantity); ok {
			ch <- prometheus.MustNewConstMetric(
				mc.clusterAvailableResourceDesc,
				prometheus.GaugeValue,
				value,
				mc.clusterbindingName,
				string(resourceName),
			)
		}
	}

	// Emit cluster physical usage
	for resourceName, quantity := range clusterPhysicalUsage {
		if value, ok := mc.convertResourceValue(string(resourceName), quantity); ok {
			ch <- prometheus.MustNewConstMetric(
				mc.clusterPhysicalUsageDesc,
				prometheus.GaugeValue,
				value,
				mc.clusterbindingName,
				string(resourceName),
			)
		}
	}

	// Emit cluster virtual usage
	for resourceName, quantity := range clusterVirtualUsage {
		if value, ok := mc.convertResourceValue(string(resourceName), quantity); ok {
			ch <- prometheus.MustNewConstMetric(
				mc.clusterVirtualUsageDesc,
				prometheus.GaugeValue,
				value,
				mc.clusterbindingName,
				string(resourceName),
			)
		}
	}
}

// addResourceList adds resources from src to dest
func addResourceList(dest, src corev1.ResourceList) {
	for resourceName, quantity := range src {
		if existingQuantity, exists := dest[resourceName]; exists {
			existingQuantity.Add(quantity)
			dest[resourceName] = existingQuantity
		} else {
			dest[resourceName] = quantity.DeepCopy()
		}
	}
}

// collectPodMetrics collects and emits pod count metrics
func (mc *MetricsCollector) collectPodMetrics(ch chan<- prometheus.Metric) {
	logger := log.FromContext(context.Background()).WithName("metrics-collector")

	// Collect virtual pod metrics
	if mc.virtualClient != nil {
		virtualPodCounts := mc.countVirtualPodsByPhase(mc.virtualClient)
		for phase, count := range virtualPodCounts {
			ch <- prometheus.MustNewConstMetric(
				mc.virtualPodCountDesc,
				prometheus.GaugeValue,
				float64(count),
				mc.clusterbindingName,
				string(phase),
			)
		}
	} else {
		logger.V(4).Info("Virtual client is nil, skipping virtual pod metrics")
	}

	// Collect physical pod metrics
	if mc.physicalClient != nil {
		physicalPodCounts := mc.countPhysicalPodsByPhase(mc.physicalClient)
		for phase, count := range physicalPodCounts {
			ch <- prometheus.MustNewConstMetric(
				mc.physicalPodCountDesc,
				prometheus.GaugeValue,
				float64(count),
				mc.clusterbindingName,
				string(phase),
			)
		}
	} else {
		logger.V(4).Info("Physical client is nil, skipping physical pod metrics")
	}
}

// countVirtualPodsByPhase counts virtual cluster pods by their phase
// Only counts pods with non-empty spec.nodeName starting with "vnode-"
func (mc *MetricsCollector) countVirtualPodsByPhase(c client.Client) map[corev1.PodPhase]int {
	logger := log.FromContext(context.Background()).WithName("metrics-collector")

	podCounts := map[corev1.PodPhase]int{
		corev1.PodPending:   0,
		corev1.PodRunning:   0,
		corev1.PodSucceeded: 0,
		corev1.PodFailed:    0,
		corev1.PodUnknown:   0,
	}

	podList := &corev1.PodList{}
	ctx := context.Background()

	if err := c.List(ctx, podList); err != nil {
		logger.Error(err, "Failed to list virtual pods for metrics")
		return podCounts
	}

	for i := range podList.Items {
		pod := &podList.Items[i]

		// Filter: only count pods with non-empty nodeName starting with "vnode-"
		if pod.Spec.NodeName == "" || !strings.HasPrefix(pod.Spec.NodeName, "vnode-") {
			continue
		}

		// Skip system pods
		if utils.IsSystemPod(pod) {
			continue
		}
		// Skip DaemonSet pods unless they have the running annotation
		if utils.IsDaemonSetPod(pod) {
			// Check if the pod has the kubeocean.io/running-daemonset:"true" annotation
			if pod.Annotations == nil || pod.Annotations[cloudv1beta1.AnnotationRunningDaemonSet] != cloudv1beta1.LabelValueTrue {
				continue
			}
		}

		phase := pod.Status.Phase
		if phase == "" {
			phase = corev1.PodUnknown
		}
		podCounts[phase]++
	}

	return podCounts
}

// countPhysicalPodsByPhase counts physical cluster pods by their phase
// Only counts pods with label kubeocean.io/managed-by=kubeocean
func (mc *MetricsCollector) countPhysicalPodsByPhase(c client.Client) map[corev1.PodPhase]int {
	logger := log.FromContext(context.Background()).WithName("metrics-collector")

	podCounts := map[corev1.PodPhase]int{
		corev1.PodPending:   0,
		corev1.PodRunning:   0,
		corev1.PodSucceeded: 0,
		corev1.PodFailed:    0,
		corev1.PodUnknown:   0,
	}

	podList := &corev1.PodList{}
	ctx := context.Background()

	if err := c.List(ctx, podList); err != nil {
		logger.Error(err, "Failed to list physical pods for metrics")
		return podCounts
	}

	for i := range podList.Items {
		pod := &podList.Items[i]

		// Filter: only count pods with label kubeocean.io/managed-by=kubeocean
		if pod.Labels == nil {
			continue
		}
		managedBy, exists := pod.Labels[cloudv1beta1.LabelManagedBy]
		if !exists || managedBy != cloudv1beta1.LabelManagedByValue {
			continue
		}

		phase := pod.Status.Phase
		if phase == "" {
			phase = corev1.PodUnknown
		}
		podCounts[phase]++
	}

	return podCounts
}

// convertResourceValue converts resource quantity to standardized float64 value
// Returns (value, true) if resource should be reported, (0, false) otherwise
func (mc *MetricsCollector) convertResourceValue(resourceName string, quantity resource.Quantity) (float64, bool) {
	// Filter only supported resource types
	if !mc.isSupportedResource(resourceName) {
		return 0, false
	}

	// Convert based on resource type
	switch resourceName {
	case ResourceCPU, ResourceGocraneCPU:
		// Convert to millicores (1000m = 1 core)
		return float64(quantity.MilliValue()), true

	case ResourceMemory, ResourceEphemeralStorage, ResourceGocraneMemory:
		// Convert to MiBytes (1Mi = 1024*1024 bytes)
		valueBytes := quantity.Value()
		valueMiBytes := float64(valueBytes) / (1024 * 1024)
		return valueMiBytes, true

	default:
		return float64(quantity.Value()), true
	}
}

// isSupportedResource checks if a resource type is supported for metrics collection
func (mc *MetricsCollector) isSupportedResource(resourceName string) bool {
	supportedResources := []string{
		ResourceCPU,
		ResourceMemory,
		ResourceEphemeralStorage,
		ResourceGocraneCPU,
		ResourceGocraneMemory,
		ResourceENIIP,
		ResourceSubENI,
		ResourceDirectENI,
		ResourceNvidiaGPU,
		ResourceTKESharedRDMA,
	}

	for _, supported := range supportedResources {
		if strings.EqualFold(resourceName, supported) {
			return true
		}
	}
	return false
}

// MetricsCollectorInst is the global MetricsCollector instance
var MetricsCollectorInst = NewMetricsCollector("unknown", nil, nil)
