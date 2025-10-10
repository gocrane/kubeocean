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
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/protoparser/prometheus"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/protoparser/prometheus/stream"
)

// ContainerInfo container identification (consistent with vnode-metrics)
type ContainerInfo struct {
	Id        string // Note: consistent with vnode-metrics, use Id instead of ID
	Name      string
	PodName   string
	NameSpace string
}

// ContainerMetrics container metrics data
type ContainerMetrics struct {
	// CPU related metrics
	CPUUsageSecondsTotal             float64 // container_cpu_usage_seconds_total
	CPUCfsPeriodsTotal               float64 // container_cpu_cfs_periods_total
	CPUCfsThrottledPeriodsTotal      float64 // container_cpu_cfs_throttled_periods_total
	CPUCfsThrottledSecondsTotal      float64 // container_cpu_cfs_throttled_seconds_total
	CPULoadAverage10s                float64 // container_cpu_load_average_10s
	CPUSchedstatRunPeriodsTotal      float64 // container_cpu_schedstat_run_periods_total
	CPUSchedstatRunqueueSecondsTotal float64 // container_cpu_schedstat_runqueue_seconds_total
	CPUSchedstatRunSecondsTotal      float64 // container_cpu_schedstat_run_seconds_total
	CPUSystemSecondsTotal            float64 // container_cpu_system_seconds_total
	CPUUserSecondsTotal              float64 // container_cpu_user_seconds_total
	CPUStatTime                      float64 // Time when data was generated

	// Memory related metrics
	MemoryUsageBytes      float64 // container_memory_usage_bytes
	MemoryCache           float64 // container_memory_cache
	MemoryWorkingSetBytes float64 // container_memory_working_set_bytes
	MemoryFailcnt         float64 // container_memory_failcnt
	MemoryFailuresTotal   float64 // container_memory_failures_total
	MemoryMappedFile      float64 // container_memory_mapped_file
	MemoryMaxUsageBytes   float64 // container_memory_max_usage_bytes
	MemoryMigrate         float64 // container_memory_migrate
	MemoryNumaPages       float64 // container_memory_numa_pages
	MemoryRss             float64 // container_memory_rss
	MemorySwap            float64 // container_memory_swap
	ReferencedBytes       float64 // container_referenced_bytes

	// Process and file descriptor metrics
	FileDescriptors float64 // container_file_descriptors
	Processes       float64 // container_processes
	Sockets         float64 // container_sockets
	Threads         float64 // container_threads
	ThreadsMax      float64 // container_threads_max

	// Container specification metrics
	SpecCpuPeriod                   float64 // container_spec_cpu_period
	SpecCpuQuota                    float64 // container_spec_cpu_quota
	SpecCpuShares                   float64 // container_spec_cpu_shares
	SpecMemoryLimitBytes            float64 // container_spec_memory_limit_bytes
	SpecMemoryReservationLimitBytes float64 // container_spec_memory_reservation_limit_bytes
	SpecMemorySwapLimitBytes        float64 // container_spec_memory_swap_limit_bytes

	// Other metrics
	LastSeen         float64 // container_last_seen
	OomEventsTotal   float64 // container_oom_events_total
	StartTimeSeconds float64 // container_start_time_seconds
	TasksState       float64 // container_tasks_state
	UlimitsSoft      float64 // container_ulimits_soft
}

// NetworkMetrics network metrics data
type NetworkMetrics struct {
	RxBytes   float64 // container_network_receive_bytes_total
	RxPackets float64 // container_network_receive_packets_total
	RxErrors  float64 // container_network_receive_errors_total
	RxDropped float64 // container_network_receive_packets_dropped_total
	TxBytes   float64 // container_network_transmit_bytes_total
	TxPackets float64 // container_network_transmit_packets_total
	TxErrors  float64 // container_network_transmit_errors_total
	TxDropped float64 // container_network_transmit_packets_dropped_total
	TcpUsage  float64 // container_network_tcp_usage_total
	Tcp6Usage float64 // container_network_tcp6_usage_total
	UdpUsage  float64 // container_network_udp_usage_total
	Udp6Usage float64 // container_network_udp6_usage_total
}

// FilesystemMetrics filesystem metrics data
type FilesystemMetrics struct {
	ReadsTotal                 float64 // container_fs_reads_total
	WritesTotal                float64 // container_fs_writes_total
	ReadsBytesTotal            float64 // container_fs_reads_bytes_total
	WritesBytesTotal           float64 // container_fs_writes_bytes_total
	UsageBytes                 float64 // container_fs_usage_bytes
	LimitBytes                 float64 // container_fs_limit_bytes
	InodesFree                 float64 // container_fs_inodes_free
	InodesTotal                float64 // container_fs_inodes_total
	IoCurrent                  float64 // container_fs_io_current
	IoTimeSecondsTotal         float64 // container_fs_io_time_seconds_total
	IoTimeWeightedSecondsTotal float64 // container_fs_io_time_weighted_seconds_total
	ReadSecondsTotal           float64 // container_fs_read_seconds_total
	ReadsMergedTotal           float64 // container_fs_reads_merged_total
	SectorReadsTotal           float64 // container_fs_sector_reads_total
	SectorWritesTotal          float64 // container_fs_sector_writes_total
	WriteSecondsTotal          float64 // container_fs_write_seconds_total
	WritesMergedTotal          float64 // container_fs_writes_merged_total
}

// BlkioMetrics block device I/O metrics data (ported from vnode_metrics)
type BlkioMetrics struct {
	Device    string  // Device name
	Major     string  // Major device number
	Minor     string  // Minor device number
	Operation string  // Operation type (read/write)
	Value     float64 // container_blkio_device_usage_total
}

// GpuMetrics GPU metrics data (fully consistent with vnode-metrics)
type GpuMetrics struct {
	MinorNumber       string  // GPU device number
	GpuDutyCycle      float64 // container_accelerator_duty_cycle
	GpuMemUsedMib     float64 // container_accelerator_memory_used_bytes (Note: vnode-metrics uses MiB)
	GpuMemoryTotalMib float64 // container_accelerator_memory_total_bytes (Note: vnode-metrics uses MiB)
	GpuDeviceNum      float64 // GPU device count
}

// MetricsParser metrics parser
type MetricsParser struct {
	// Container metrics data grouped by port
	containerMetrics map[string]map[ContainerInfo]*ContainerMetrics             // key: port -> container -> metrics
	networkMetrics   map[string]map[ContainerInfo]map[string]*NetworkMetrics    // key: port -> container -> interface -> metrics
	fsMetrics        map[string]map[ContainerInfo]map[string]*FilesystemMetrics // key: port -> container -> device -> metrics
	blkioMetrics     map[string]map[ContainerInfo]map[string]*BlkioMetrics      // key: port -> container -> device -> metrics
	gpuMetrics       map[string]map[ContainerInfo]map[string]*GpuMetrics        // key: port -> container -> gpu -> metrics

	mu sync.RWMutex
}

// NewMetricsParser creates a new metrics parser
func NewMetricsParser() *MetricsParser {
	return &MetricsParser{
		containerMetrics: make(map[string]map[ContainerInfo]*ContainerMetrics),
		networkMetrics:   make(map[string]map[ContainerInfo]map[string]*NetworkMetrics),
		fsMetrics:        make(map[string]map[ContainerInfo]map[string]*FilesystemMetrics),
		blkioMetrics:     make(map[string]map[ContainerInfo]map[string]*BlkioMetrics),
		gpuMetrics:       make(map[string]map[ContainerInfo]map[string]*GpuMetrics),
	}
}

// metricLabels holds parsed metric labels
type metricLabels struct {
	podName       string
	namespace     string
	containerName string
	containerID   string
	interfaceName string
	device        string
	minorNumber   string
	tcpState      string
	major         string
	minor         string
	operation     string
}

// initializePortMetrics initializes metric storage for a specific port
func (mp *MetricsParser) initializePortMetrics(targetPort string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	// Initialize maps if needed
	if mp.containerMetrics[targetPort] == nil {
		mp.containerMetrics[targetPort] = make(map[ContainerInfo]*ContainerMetrics)
	}
	if mp.networkMetrics[targetPort] == nil {
		mp.networkMetrics[targetPort] = make(map[ContainerInfo]map[string]*NetworkMetrics)
	}
	if mp.fsMetrics[targetPort] == nil {
		mp.fsMetrics[targetPort] = make(map[ContainerInfo]map[string]*FilesystemMetrics)
	}
	if mp.blkioMetrics[targetPort] == nil {
		mp.blkioMetrics[targetPort] = make(map[ContainerInfo]map[string]*BlkioMetrics)
	}
	if mp.gpuMetrics[targetPort] == nil {
		mp.gpuMetrics[targetPort] = make(map[ContainerInfo]map[string]*GpuMetrics)
	}

	// Clear existing data for this port
	mp.containerMetrics[targetPort] = make(map[ContainerInfo]*ContainerMetrics)
	mp.networkMetrics[targetPort] = make(map[ContainerInfo]map[string]*NetworkMetrics)
	mp.fsMetrics[targetPort] = make(map[ContainerInfo]map[string]*FilesystemMetrics)
	mp.blkioMetrics[targetPort] = make(map[ContainerInfo]map[string]*BlkioMetrics)
	mp.gpuMetrics[targetPort] = make(map[ContainerInfo]map[string]*GpuMetrics)
}

// parseMetricLabels parses labels from metric tags
func (mp *MetricsParser) parseMetricLabels(metricName string, tags []prometheus.Tag) metricLabels {
	var labels metricLabels

	for _, label := range tags {
		value := string([]byte(label.Value)) // Deep copy to avoid memory reference issues
		switch label.Key {
		case "pod", "pod_name":
			labels.podName = value
			if strings.Contains(metricName, "pod_filesystem_") {
				pos := strings.Index(labels.podName, "-")
				if pos > 0 {
					labels.containerName = labels.podName[:pos]
				} else {
					labels.containerName = labels.podName
				}
			}
		case "namespace":
			labels.namespace = value
		case "container", "container_name":
			labels.containerName = value
			if labels.containerName == "POD" {
				labels.containerName = "pause"
			}
		case "id", "container_id":
			labels.containerID = value
			if strings.Contains(labels.containerID, "/kubepods/") || strings.Contains(labels.containerID, "/kubepods.slice/") {
				labels.containerID = extractIDFromCgroupPath(labels.containerID)
			}
		case "interface":
			labels.interfaceName = value
		case "device":
			labels.device = value
		case "minor_number":
			labels.minorNumber = value
		case "tcp_state":
			labels.tcpState = value
		case "major":
			labels.major = value
		case "minor":
			labels.minor = value
		case "operation":
			labels.operation = value
		}
	}

	return labels
}

// processRowMetric processes a single metric row
func (mp *MetricsParser) processRowMetric(targetPort string, metricName string, metricValue float64, timestamp int64, labels metricLabels) {
	refID := ContainerInfo{
		Id:        labels.containerID,
		Name:      labels.containerName,
		PodName:   labels.podName,
		NameSpace: labels.namespace,
	}

	// Get metric storage objects
	pStatInfo := mp.getContainerOriginStatInfoByPort(targetPort, refID)
	var pNetworkStat *NetworkMetrics
	var pFsStat *FilesystemMetrics
	var pBlkioStat *BlkioMetrics
	var pGpuStat *GpuMetrics

	pFsStat = mp.getOriginFsStatInfoByPort(targetPort, refID, labels.device)
	if labels.interfaceName != "" {
		pNetworkStat = mp.getOriginNetworkStatInfoByPort(targetPort, refID, labels.interfaceName)
	}
	if labels.major != "" && labels.minor != "" && labels.operation != "" {
		pBlkioStat = mp.getBlkioStatInfoByPort(targetPort, refID, labels.device, labels.major, labels.minor, labels.operation)
	}
	if labels.minorNumber != "" {
		pGpuStat = mp.getEksOriginGpuStatInfoByPort(targetPort, refID, labels.minorNumber)
	}

	// Process metrics data
	mp.processMetricForPort(targetPort, metricName, metricValue, timestamp, pStatInfo, pNetworkStat, pFsStat, pBlkioStat, pGpuStat, labels.tcpState)
}

// ParseAndStoreMetrics parses metrics data and stores it to the specified port
func (mp *MetricsParser) ParseAndStoreMetrics(responseBody io.Reader, targetPort string) error {
	mp.initializePortMetrics(targetPort)

	// Parse metrics stream
	nMetrics := 0
	lk := sync.Mutex{}
	defaultScrapeTimestamp := time.Now().UnixNano() / 1e6

	// Use real VictoriaMetrics parser, referring to vnode_metrics implementation
	fmt.Printf("[DEBUG] Starting real VictoriaMetrics parsing for port %s, timestamp=%d\n", targetPort, defaultScrapeTimestamp)

	err := stream.Parse(responseBody, defaultScrapeTimestamp, false, func(rows []prometheus.Row) error {
		lk.Lock()
		defer lk.Unlock()

		fmt.Printf("[DEBUG] ParseAndStoreMetrics: Processing %d rows for port %s\n", len(rows), targetPort)

		for i, iter := range rows {
			if i < 5 { // Only print first 5 to avoid spamming
				fmt.Printf("[DEBUG] row[%d] for port %s: metric=%s, tags=%v, value=%.2f, timestamp=%d\n",
					i, targetPort, iter.Metric, iter.Tags, iter.Value, iter.Timestamp)
			}
			metricName := string([]byte(iter.Metric)) // Deep copy to avoid memory reference issues

			// Parse labels from tags
			labels := mp.parseMetricLabels(metricName, iter.Tags)

			// Only process valid container information
			if labels.containerName != "" && labels.podName != "" && labels.namespace != "" {
				nMetrics++
				mp.processRowMetric(targetPort, metricName, iter.Value, iter.Timestamp, labels)
			}
		}
		return nil
	}, func(s string) {
		// Parse log callback (temporarily ignored)
	})

	if err != nil {
		fmt.Printf("[ERROR] ParseAndStoreMetrics failed for port %s: %v\n", targetPort, err)
		return fmt.Errorf("parse cadvisor metrics stream failed for port %s: %v", targetPort, err)
	}

	// Parsing completed
	fmt.Printf("[DEBUG] ParseAndStoreMetrics completed successfully for port %s, processed %d metrics\n", targetPort, nMetrics)
	return nil
}

// processMetricForPort processes metrics data and stores it to the specified port (consistent with vnode-metrics)
func (mp *MetricsParser) processMetricForPort(_ string, metricName string, metricValue float64, timestamp int64,
	pStatInfo *ContainerMetrics, pNetworkStat *NetworkMetrics, pFsStat *FilesystemMetrics, pBlkioStat *BlkioMetrics, pGpuStat *GpuMetrics, _ string) {

	// Dispatch to specialized handlers based on metric prefix
	switch {
	case strings.HasPrefix(metricName, "container_cpu_"):
		mp.processCPUMetric(metricName, metricValue, timestamp, pStatInfo)
	case strings.HasPrefix(metricName, "container_memory_") || metricName == "container_referenced_bytes":
		mp.processMemoryMetric(metricName, metricValue, pStatInfo)
	case strings.HasPrefix(metricName, "container_network_"):
		mp.processNetworkMetricForPort(metricName, metricValue, pNetworkStat)
	case strings.HasPrefix(metricName, "container_fs_"):
		mp.processFilesystemMetricForPort(metricName, metricValue, pFsStat)
	case strings.HasPrefix(metricName, "container_spec_"):
		mp.processSpecMetric(metricName, metricValue, pStatInfo)
	case strings.HasPrefix(metricName, "container_accelerator_"):
		mp.processGPUMetric(metricName, metricValue, pGpuStat)
	case metricName == "container_blkio_device_usage_total":
		mp.processBlkioMetricForPort(metricValue, pBlkioStat)
	default:
		mp.processOtherMetric(metricName, metricValue, pStatInfo)
	}
}

// processCPUMetric processes CPU related metrics
func (mp *MetricsParser) processCPUMetric(metricName string, metricValue float64, timestamp int64, pStatInfo *ContainerMetrics) {
	if pStatInfo == nil {
		return
	}

	switch metricName {
	case "container_cpu_usage_seconds_total":
		pStatInfo.CPUUsageSecondsTotal = metricValue
		pStatInfo.CPUStatTime = float64(timestamp) / 1000
	case "container_cpu_cfs_periods_total":
		pStatInfo.CPUCfsPeriodsTotal = metricValue
	case "container_cpu_cfs_throttled_periods_total":
		pStatInfo.CPUCfsThrottledPeriodsTotal = metricValue
	case "container_cpu_cfs_throttled_seconds_total":
		pStatInfo.CPUCfsThrottledSecondsTotal = metricValue
	case "container_cpu_load_average_10s":
		pStatInfo.CPULoadAverage10s = metricValue
	case "container_cpu_schedstat_run_periods_total":
		pStatInfo.CPUSchedstatRunPeriodsTotal = metricValue
	case "container_cpu_schedstat_runqueue_seconds_total":
		pStatInfo.CPUSchedstatRunqueueSecondsTotal = metricValue
	case "container_cpu_schedstat_run_seconds_total":
		pStatInfo.CPUSchedstatRunSecondsTotal = metricValue
	case "container_cpu_system_seconds_total":
		pStatInfo.CPUSystemSecondsTotal = metricValue
	case "container_cpu_user_seconds_total":
		pStatInfo.CPUUserSecondsTotal = metricValue
	}
}

// processMemoryMetric processes memory related metrics
func (mp *MetricsParser) processMemoryMetric(metricName string, metricValue float64, pStatInfo *ContainerMetrics) {
	if pStatInfo == nil {
		return
	}

	switch metricName {
	case "container_memory_usage_bytes":
		pStatInfo.MemoryUsageBytes = metricValue
	case "container_memory_cache":
		pStatInfo.MemoryCache = metricValue
	case "container_memory_working_set_bytes":
		pStatInfo.MemoryWorkingSetBytes = metricValue
	case "container_memory_failcnt":
		pStatInfo.MemoryFailcnt = metricValue
	case "container_memory_failures_total":
		pStatInfo.MemoryFailuresTotal = metricValue
	case "container_memory_mapped_file":
		pStatInfo.MemoryMappedFile = metricValue
	case "container_memory_max_usage_bytes":
		pStatInfo.MemoryMaxUsageBytes = metricValue
	case "container_memory_migrate":
		pStatInfo.MemoryMigrate = metricValue
	case "container_memory_numa_pages":
		pStatInfo.MemoryNumaPages = metricValue
	case "container_memory_rss":
		pStatInfo.MemoryRss = metricValue
	case "container_memory_swap":
		pStatInfo.MemorySwap = metricValue
	case "container_referenced_bytes":
		pStatInfo.ReferencedBytes = metricValue
	}
}

// processNetworkMetricForPort processes network related metrics
func (mp *MetricsParser) processNetworkMetricForPort(metricName string, metricValue float64, pNetworkStat *NetworkMetrics) {
	if pNetworkStat == nil {
		return
	}

	switch metricName {
	case "container_network_receive_bytes_total":
		pNetworkStat.RxBytes = metricValue
	case "container_network_receive_packets_total":
		pNetworkStat.RxPackets = metricValue
	case "container_network_receive_errors_total":
		pNetworkStat.RxErrors = metricValue
	case "container_network_receive_packets_dropped_total":
		pNetworkStat.RxDropped = metricValue
	case "container_network_transmit_bytes_total":
		pNetworkStat.TxBytes = metricValue
	case "container_network_transmit_packets_total":
		pNetworkStat.TxPackets = metricValue
	case "container_network_transmit_errors_total":
		pNetworkStat.TxErrors = metricValue
	case "container_network_transmit_packets_dropped_total":
		pNetworkStat.TxDropped = metricValue
	case "container_network_tcp_usage_total":
		pNetworkStat.TcpUsage = metricValue
	case "container_network_tcp6_usage_total":
		pNetworkStat.Tcp6Usage = metricValue
	case "container_network_udp_usage_total":
		pNetworkStat.UdpUsage = metricValue
	case "container_network_udp6_usage_total":
		pNetworkStat.Udp6Usage = metricValue
	case "container_network_tcp_connection_count":
		fmt.Printf("[DEBUG] Processing TCP connection count: value=%.2f\n", metricValue)
	}
}

// processFilesystemMetricForPort processes filesystem related metrics
func (mp *MetricsParser) processFilesystemMetricForPort(metricName string, metricValue float64, pFsStat *FilesystemMetrics) {
	if pFsStat == nil {
		return
	}

	switch metricName {
	case "container_fs_reads_total":
		pFsStat.ReadsTotal = metricValue
	case "container_fs_writes_total":
		pFsStat.WritesTotal = metricValue
	case "container_fs_reads_bytes_total":
		pFsStat.ReadsBytesTotal = metricValue
	case "container_fs_writes_bytes_total":
		pFsStat.WritesBytesTotal = metricValue
	case "container_fs_usage_bytes":
		pFsStat.UsageBytes = metricValue
	case "container_fs_limit_bytes":
		pFsStat.LimitBytes = metricValue
	case "container_fs_inodes_free":
		pFsStat.InodesFree = metricValue
	case "container_fs_inodes_total":
		pFsStat.InodesTotal = metricValue
	case "container_fs_io_current":
		pFsStat.IoCurrent = metricValue
	case "container_fs_io_time_seconds_total":
		pFsStat.IoTimeSecondsTotal = metricValue
	case "container_fs_io_time_weighted_seconds_total":
		pFsStat.IoTimeWeightedSecondsTotal = metricValue
	case "container_fs_read_seconds_total":
		pFsStat.ReadSecondsTotal = metricValue
	case "container_fs_reads_merged_total":
		pFsStat.ReadsMergedTotal = metricValue
	case "container_fs_sector_reads_total":
		pFsStat.SectorReadsTotal = metricValue
	case "container_fs_sector_writes_total":
		pFsStat.SectorWritesTotal = metricValue
	case "container_fs_write_seconds_total":
		pFsStat.WriteSecondsTotal = metricValue
	case "container_fs_writes_merged_total":
		pFsStat.WritesMergedTotal = metricValue
	}
}

// processSpecMetric processes container specification metrics
func (mp *MetricsParser) processSpecMetric(metricName string, metricValue float64, pStatInfo *ContainerMetrics) {
	if pStatInfo == nil {
		return
	}

	switch metricName {
	case "container_spec_cpu_period":
		pStatInfo.SpecCpuPeriod = metricValue
	case "container_spec_cpu_quota":
		pStatInfo.SpecCpuQuota = metricValue
	case "container_spec_cpu_shares":
		pStatInfo.SpecCpuShares = metricValue
	case "container_spec_memory_limit_bytes":
		pStatInfo.SpecMemoryLimitBytes = metricValue
	case "container_spec_memory_reservation_limit_bytes":
		pStatInfo.SpecMemoryReservationLimitBytes = metricValue
	case "container_spec_memory_swap_limit_bytes":
		pStatInfo.SpecMemorySwapLimitBytes = metricValue
	}
}

// processGPUMetric processes GPU related metrics
func (mp *MetricsParser) processGPUMetric(metricName string, metricValue float64, pGpuStat *GpuMetrics) {
	if pGpuStat == nil {
		return
	}

	switch metricName {
	case "container_accelerator_duty_cycle":
		pGpuStat.GpuDutyCycle = metricValue
		fmt.Printf("[DEBUG] Updated GPU duty cycle: gpu=%s, value=%.2f\n", pGpuStat.MinorNumber, metricValue)
	case "container_accelerator_memory_used_bytes":
		pGpuStat.GpuMemUsedMib = metricValue
		fmt.Printf("[DEBUG] Updated GPU memory used: gpu=%s, value=%.2f MiB\n", pGpuStat.MinorNumber, metricValue)
	case "container_accelerator_memory_total_bytes":
		pGpuStat.GpuMemoryTotalMib = metricValue
		fmt.Printf("[DEBUG] Updated GPU memory total: gpu=%s, value=%.2f MiB\n", pGpuStat.MinorNumber, metricValue)
	}
}

// processBlkioMetricForPort processes block I/O metrics
func (mp *MetricsParser) processBlkioMetricForPort(metricValue float64, pBlkioStat *BlkioMetrics) {
	if pBlkioStat == nil {
		return
	}
	pBlkioStat.Value = metricValue
	fmt.Printf("[DEBUG] Updated blkio metric: device=%s, operation=%s, value=%.2f\n", pBlkioStat.Device, pBlkioStat.Operation, metricValue)
}

// processOtherMetric processes other container metrics
func (mp *MetricsParser) processOtherMetric(metricName string, metricValue float64, pStatInfo *ContainerMetrics) {
	if pStatInfo == nil {
		return
	}

	switch metricName {
	case "container_file_descriptors":
		pStatInfo.FileDescriptors = metricValue
	case "container_processes":
		pStatInfo.Processes = metricValue
	case "container_sockets":
		pStatInfo.Sockets = metricValue
	case "container_threads":
		pStatInfo.Threads = metricValue
	case "container_threads_max":
		pStatInfo.ThreadsMax = metricValue
	case "container_last_seen":
		pStatInfo.LastSeen = metricValue
	case "container_oom_events_total":
		pStatInfo.OomEventsTotal = metricValue
	case "container_start_time_seconds":
		pStatInfo.StartTimeSeconds = metricValue
	case "container_tasks_state":
		pStatInfo.TasksState = metricValue
	case "container_ulimits_soft":
		pStatInfo.UlimitsSoft = metricValue
	}
}

// getOrCreateContainerMetrics gets or creates container metrics
func (mp *MetricsParser) getOrCreateContainerMetrics(port string, containerInfo ContainerInfo) *ContainerMetrics {
	if mp.containerMetrics[port] == nil {
		mp.containerMetrics[port] = make(map[ContainerInfo]*ContainerMetrics)
	}

	if mp.containerMetrics[port][containerInfo] == nil {
		mp.containerMetrics[port][containerInfo] = &ContainerMetrics{}
	}

	return mp.containerMetrics[port][containerInfo]
}

// processNetworkMetric processes network metrics
func (mp *MetricsParser) processNetworkMetric(port string, containerInfo ContainerInfo, interfaceName, metricType string, value float64) {
	if mp.networkMetrics[port] == nil {
		mp.networkMetrics[port] = make(map[ContainerInfo]map[string]*NetworkMetrics)
	}
	if mp.networkMetrics[port][containerInfo] == nil {
		mp.networkMetrics[port][containerInfo] = make(map[string]*NetworkMetrics)
	}
	if mp.networkMetrics[port][containerInfo][interfaceName] == nil {
		mp.networkMetrics[port][containerInfo][interfaceName] = &NetworkMetrics{}
	}

	networkMetrics := mp.networkMetrics[port][containerInfo][interfaceName]
	switch metricType {
	case "rx_bytes":
		networkMetrics.RxBytes = value
	case "rx_packets":
		networkMetrics.RxPackets = value
	case "rx_errors":
		networkMetrics.RxErrors = value
	case "rx_dropped":
		networkMetrics.RxDropped = value
	case "tx_bytes":
		networkMetrics.TxBytes = value
	case "tx_packets":
		networkMetrics.TxPackets = value
	case "tx_errors":
		networkMetrics.TxErrors = value
	case "tx_dropped":
		networkMetrics.TxDropped = value
	case "tcp_usage":
		networkMetrics.TcpUsage = value
	case "tcp6_usage":
		networkMetrics.Tcp6Usage = value
	case "udp_usage":
		networkMetrics.UdpUsage = value
	case "udp6_usage":
		networkMetrics.Udp6Usage = value
	}
}

// processFilesystemMetric processes filesystem metrics
func (mp *MetricsParser) processFilesystemMetric(port string, containerInfo ContainerInfo, device, metricType string, value float64) {
	if mp.fsMetrics[port] == nil {
		mp.fsMetrics[port] = make(map[ContainerInfo]map[string]*FilesystemMetrics)
	}
	if mp.fsMetrics[port][containerInfo] == nil {
		mp.fsMetrics[port][containerInfo] = make(map[string]*FilesystemMetrics)
	}
	if mp.fsMetrics[port][containerInfo][device] == nil {
		mp.fsMetrics[port][containerInfo][device] = &FilesystemMetrics{}
	}

	fsMetrics := mp.fsMetrics[port][containerInfo][device]
	switch metricType {
	case "reads_total":
		fsMetrics.ReadsTotal = value
	case "writes_total":
		fsMetrics.WritesTotal = value
	case "reads_bytes_total":
		fsMetrics.ReadsBytesTotal = value
	case "writes_bytes_total":
		fsMetrics.WritesBytesTotal = value
	case "usage_bytes":
		fsMetrics.UsageBytes = value
	case "limit_bytes":
		fsMetrics.LimitBytes = value
	case "inodes_free":
		fsMetrics.InodesFree = value
	case "inodes_total":
		fsMetrics.InodesTotal = value
	case "io_current":
		fsMetrics.IoCurrent = value
	case "io_time_seconds_total":
		fsMetrics.IoTimeSecondsTotal = value
	case "io_time_weighted_seconds_total":
		fsMetrics.IoTimeWeightedSecondsTotal = value
	case "read_seconds_total":
		fsMetrics.ReadSecondsTotal = value
	case "reads_merged_total":
		fsMetrics.ReadsMergedTotal = value
	case "sector_reads_total":
		fsMetrics.SectorReadsTotal = value
	case "sector_writes_total":
		fsMetrics.SectorWritesTotal = value
	case "write_seconds_total":
		fsMetrics.WriteSecondsTotal = value
	case "writes_merged_total":
		fsMetrics.WritesMergedTotal = value
	}
}

// GetContainerMetrics gets container metrics for the specified port
func (mp *MetricsParser) GetContainerMetrics(port string) map[ContainerInfo]*ContainerMetrics {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	if mp.containerMetrics[port] == nil {
		return make(map[ContainerInfo]*ContainerMetrics)
	}

	result := make(map[ContainerInfo]*ContainerMetrics)
	for k, v := range mp.containerMetrics[port] {
		result[k] = v
	}
	return result
}

// GetNetworkMetrics gets network metrics for the specified port
func (mp *MetricsParser) GetNetworkMetrics(port string) map[ContainerInfo]map[string]*NetworkMetrics {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	if mp.networkMetrics[port] == nil {
		return make(map[ContainerInfo]map[string]*NetworkMetrics)
	}

	result := make(map[ContainerInfo]map[string]*NetworkMetrics)
	for k, v := range mp.networkMetrics[port] {
		result[k] = make(map[string]*NetworkMetrics)
		for k2, v2 := range v {
			result[k][k2] = v2
		}
	}
	return result
}

// GetFilesystemMetrics gets filesystem metrics for the specified port
func (mp *MetricsParser) GetFilesystemMetrics(port string) map[ContainerInfo]map[string]*FilesystemMetrics {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	if mp.fsMetrics[port] == nil {
		return make(map[ContainerInfo]map[string]*FilesystemMetrics)
	}

	result := make(map[ContainerInfo]map[string]*FilesystemMetrics)
	for k, v := range mp.fsMetrics[port] {
		result[k] = make(map[string]*FilesystemMetrics)
		for k2, v2 := range v {
			result[k][k2] = v2
		}
	}
	return result
}

// WritePrometheusMetrics writes metrics in Prometheus format
func (mp *MetricsParser) WritePrometheusMetrics(w io.Writer, port string, nodeIP string, targetNamespace string) {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	// Get all data for this port
	containerData := mp.containerMetrics[port]
	networkData := mp.networkMetrics[port]
	fsData := mp.fsMetrics[port]
	blkioData := mp.blkioMetrics[port]
	gpuData := mp.gpuMetrics[port]

	// Write container CPU and memory metrics
	for container, stat := range containerData {
		// If targetNamespace is specified, only expose metrics for that namespace
		if targetNamespace != "" && container.NameSpace != targetNamespace {
			continue
		}

		// Try to get virtual pod information for label conversion
		virtualInfo, exists := GetVirtualPodInfo(container.PodName)
		if !exists {
			// Skip metrics for pods without virtual mapping
			fmt.Printf("[DEBUG] No virtual mapping found for pod: %s, skipping container CPU/memory metrics\n", container.PodName)
			continue
		}

		// Use virtual labels from mapping
		labels := fmt.Sprintf(`{container="%s",pod="%s",namespace="%s",node="%s"}`,
			container.Name, virtualInfo.VirtualPodName, virtualInfo.VirtualPodNamespace, virtualInfo.VirtualNodeName)

		// CPU metrics
		mp.writeMetric(w, "container_cpu_usage_seconds_total", labels, stat.CPUUsageSecondsTotal)
		mp.writeMetric(w, "container_cpu_cfs_periods_total", labels, stat.CPUCfsPeriodsTotal)
		mp.writeMetric(w, "container_cpu_cfs_throttled_periods_total", labels, stat.CPUCfsThrottledPeriodsTotal)
		mp.writeMetric(w, "container_cpu_cfs_throttled_seconds_total", labels, stat.CPUCfsThrottledSecondsTotal)
		mp.writeMetric(w, "container_cpu_load_average_10s", labels, stat.CPULoadAverage10s)
		mp.writeMetric(w, "container_cpu_schedstat_run_periods_total", labels, stat.CPUSchedstatRunPeriodsTotal)
		mp.writeMetric(w, "container_cpu_schedstat_runqueue_seconds_total", labels, stat.CPUSchedstatRunqueueSecondsTotal)
		mp.writeMetric(w, "container_cpu_schedstat_run_seconds_total", labels, stat.CPUSchedstatRunSecondsTotal)
		mp.writeMetric(w, "container_cpu_system_seconds_total", labels, stat.CPUSystemSecondsTotal)
		mp.writeMetric(w, "container_cpu_user_seconds_total", labels, stat.CPUUserSecondsTotal)

		// Memory metrics
		mp.writeMetric(w, "container_memory_usage_bytes", labels, stat.MemoryUsageBytes)
		mp.writeMetric(w, "container_memory_cache", labels, stat.MemoryCache)
		mp.writeMetric(w, "container_memory_working_set_bytes", labels, stat.MemoryWorkingSetBytes)
		mp.writeMetric(w, "container_memory_failcnt", labels, stat.MemoryFailcnt)
		mp.writeMetric(w, "container_memory_failures_total", labels, stat.MemoryFailuresTotal)
		mp.writeMetric(w, "container_memory_mapped_file", labels, stat.MemoryMappedFile)
		mp.writeMetric(w, "container_memory_max_usage_bytes", labels, stat.MemoryMaxUsageBytes)
		mp.writeMetric(w, "container_memory_migrate", labels, stat.MemoryMigrate)
		mp.writeMetric(w, "container_memory_numa_pages", labels, stat.MemoryNumaPages)
		mp.writeMetric(w, "container_memory_rss", labels, stat.MemoryRss)
		mp.writeMetric(w, "container_memory_swap", labels, stat.MemorySwap)
		mp.writeMetric(w, "container_referenced_bytes", labels, stat.ReferencedBytes)

		// Process and file descriptor metrics
		mp.writeMetric(w, "container_file_descriptors", labels, stat.FileDescriptors)
		mp.writeMetric(w, "container_processes", labels, stat.Processes)
		mp.writeMetric(w, "container_sockets", labels, stat.Sockets)
		mp.writeMetric(w, "container_threads", labels, stat.Threads)
		mp.writeMetric(w, "container_threads_max", labels, stat.ThreadsMax)

		// Container specification metrics
		mp.writeMetric(w, "container_spec_cpu_period", labels, stat.SpecCpuPeriod)
		mp.writeMetric(w, "container_spec_cpu_quota", labels, stat.SpecCpuQuota)
		mp.writeMetric(w, "container_spec_cpu_shares", labels, stat.SpecCpuShares)
		mp.writeMetric(w, "container_spec_memory_limit_bytes", labels, stat.SpecMemoryLimitBytes)
		mp.writeMetric(w, "container_spec_memory_reservation_limit_bytes", labels, stat.SpecMemoryReservationLimitBytes)
		mp.writeMetric(w, "container_spec_memory_swap_limit_bytes", labels, stat.SpecMemorySwapLimitBytes)

		// Other metrics
		mp.writeMetric(w, "container_last_seen", labels, stat.LastSeen)
		mp.writeMetric(w, "container_oom_events_total", labels, stat.OomEventsTotal)
		mp.writeMetric(w, "container_start_time_seconds", labels, stat.StartTimeSeconds)
		mp.writeMetric(w, "container_tasks_state", labels, stat.TasksState)
		mp.writeMetric(w, "container_ulimits_soft", labels, stat.UlimitsSoft)
	}

	// Write network metrics
	for container, interfaces := range networkData {
		if targetNamespace != "" && container.NameSpace != targetNamespace {
			continue
		}

		// Try to get virtual pod information for label conversion
		virtualInfo, exists := GetVirtualPodInfo(container.PodName)
		if !exists {
			// Skip metrics for pods without virtual mapping
			fmt.Printf("[DEBUG] No virtual mapping found for pod: %s, skipping network metrics\n", container.PodName)
			continue
		}

		for interfaceName, networkStat := range interfaces {
			networkLabels := fmt.Sprintf(`{container="%s",pod="%s",namespace="%s",node="%s",interface="%s"}`,
				container.Name, virtualInfo.VirtualPodName, virtualInfo.VirtualPodNamespace, virtualInfo.VirtualNodeName, interfaceName)

			mp.writeMetric(w, "container_network_receive_bytes_total", networkLabels, networkStat.RxBytes)
			mp.writeMetric(w, "container_network_receive_packets_total", networkLabels, networkStat.RxPackets)
			mp.writeMetric(w, "container_network_receive_errors_total", networkLabels, networkStat.RxErrors)
			mp.writeMetric(w, "container_network_receive_packets_dropped_total", networkLabels, networkStat.RxDropped)
			mp.writeMetric(w, "container_network_transmit_bytes_total", networkLabels, networkStat.TxBytes)
			mp.writeMetric(w, "container_network_transmit_packets_total", networkLabels, networkStat.TxPackets)
			mp.writeMetric(w, "container_network_transmit_errors_total", networkLabels, networkStat.TxErrors)
			mp.writeMetric(w, "container_network_transmit_packets_dropped_total", networkLabels, networkStat.TxDropped)
			mp.writeMetric(w, "container_network_tcp_usage_total", networkLabels, networkStat.TcpUsage)
			mp.writeMetric(w, "container_network_tcp6_usage_total", networkLabels, networkStat.Tcp6Usage)
			mp.writeMetric(w, "container_network_udp_usage_total", networkLabels, networkStat.UdpUsage)
			mp.writeMetric(w, "container_network_udp6_usage_total", networkLabels, networkStat.Udp6Usage)
		}
	}

	// Write filesystem metrics
	for container, devices := range fsData {
		if targetNamespace != "" && container.NameSpace != targetNamespace {
			continue
		}

		// Try to get virtual pod information for label conversion
		virtualInfo, exists := GetVirtualPodInfo(container.PodName)
		if !exists {
			// Skip metrics for pods without virtual mapping
			fmt.Printf("[DEBUG] No virtual mapping found for pod: %s, skipping filesystem metrics\n", container.PodName)
			continue
		}

		for device, fsStat := range devices {
			fsLabels := fmt.Sprintf(`{container="%s",pod="%s",namespace="%s",node="%s",device="%s"}`,
				container.Name, virtualInfo.VirtualPodName, virtualInfo.VirtualPodNamespace, virtualInfo.VirtualNodeName, device)

			mp.writeMetric(w, "container_fs_reads_total", fsLabels, fsStat.ReadsTotal)
			mp.writeMetric(w, "container_fs_writes_total", fsLabels, fsStat.WritesTotal)
			mp.writeMetric(w, "container_fs_reads_bytes_total", fsLabels, fsStat.ReadsBytesTotal)
			mp.writeMetric(w, "container_fs_writes_bytes_total", fsLabels, fsStat.WritesBytesTotal)
			mp.writeMetric(w, "container_fs_usage_bytes", fsLabels, fsStat.UsageBytes)
			mp.writeMetric(w, "container_fs_limit_bytes", fsLabels, fsStat.LimitBytes)
			mp.writeMetric(w, "container_fs_inodes_free", fsLabels, fsStat.InodesFree)
			mp.writeMetric(w, "container_fs_inodes_total", fsLabels, fsStat.InodesTotal)
			mp.writeMetric(w, "container_fs_io_current", fsLabels, fsStat.IoCurrent)
			mp.writeMetric(w, "container_fs_io_time_seconds_total", fsLabels, fsStat.IoTimeSecondsTotal)
			mp.writeMetric(w, "container_fs_io_time_weighted_seconds_total", fsLabels, fsStat.IoTimeWeightedSecondsTotal)
			mp.writeMetric(w, "container_fs_read_seconds_total", fsLabels, fsStat.ReadSecondsTotal)
			mp.writeMetric(w, "container_fs_reads_merged_total", fsLabels, fsStat.ReadsMergedTotal)
			mp.writeMetric(w, "container_fs_sector_reads_total", fsLabels, fsStat.SectorReadsTotal)
			mp.writeMetric(w, "container_fs_sector_writes_total", fsLabels, fsStat.SectorWritesTotal)
			mp.writeMetric(w, "container_fs_write_seconds_total", fsLabels, fsStat.WriteSecondsTotal)
			mp.writeMetric(w, "container_fs_writes_merged_total", fsLabels, fsStat.WritesMergedTotal)
		}
	}

	// Write block device I/O metrics (new, ported from vnode_metrics)
	for container, devices := range blkioData {
		if targetNamespace != "" && container.NameSpace != targetNamespace {
			continue
		}

		// Try to get virtual pod information for label conversion
		virtualInfo, exists := GetVirtualPodInfo(container.PodName)
		if !exists {
			// Skip metrics for pods without virtual mapping
			fmt.Printf("[DEBUG] No virtual mapping found for pod: %s, skipping block I/O metrics\n", container.PodName)
			continue
		}

		for _, blkioStat := range devices {
			if blkioStat.Value != 0 { // Only output non-zero values
				blkioLabels := fmt.Sprintf(`{container="%s",pod="%s",namespace="%s",node="%s",device="%s",operation="%s"}`,
					container.Name, virtualInfo.VirtualPodName, virtualInfo.VirtualPodNamespace, virtualInfo.VirtualNodeName, blkioStat.Device, blkioStat.Operation)
				mp.writeMetric(w, "container_blkio_device_usage_total", blkioLabels, blkioStat.Value)
			}
		}
	}

	// Write GPU metrics (new, ported from vnode_metrics)
	for container, gpus := range gpuData {
		if targetNamespace != "" && container.NameSpace != targetNamespace {
			continue
		}

		// Try to get virtual pod information for label conversion
		virtualInfo, exists := GetVirtualPodInfo(container.PodName)
		if !exists {
			// Skip metrics for pods without virtual mapping
			fmt.Printf("[DEBUG] No virtual mapping found for pod: %s, skipping GPU metrics\n", container.PodName)
			continue
		}

		for _, gpuStat := range gpus {
			gpuLabels := fmt.Sprintf(`{container="%s",pod="%s",namespace="%s",node="%s",gpu="%s"}`,
				container.Name, virtualInfo.VirtualPodName, virtualInfo.VirtualPodNamespace, virtualInfo.VirtualNodeName, gpuStat.MinorNumber)

			mp.writeMetric(w, "container_accelerator_duty_cycle", gpuLabels, gpuStat.GpuDutyCycle)
			mp.writeMetric(w, "container_accelerator_memory_used_bytes", gpuLabels, gpuStat.GpuMemUsedMib)      // Fixed: use correct field name
			mp.writeMetric(w, "container_accelerator_memory_total_bytes", gpuLabels, gpuStat.GpuMemoryTotalMib) // Fixed: use correct field name
		}
	}
}

// writeMetric writes a single metric
func (mp *MetricsParser) writeMetric(w io.Writer, metricName, labels string, value float64) {
	if value != 0 { // Only output non-zero values
		fmt.Fprintf(w, "%s%s %.6f\n", metricName, labels, value)
	}
}

// extractIDFromCgroupPath extracts container ID from cgroup path
func extractIDFromCgroupPath(cgroupPath string) string {
	// Implementation consistent with vnode-metrics
	if len(cgroupPath) > 64 {
		return cgroupPath[len(cgroupPath)-64:]
	}
	return cgroupPath
}

// processBlkioMetric processes block device I/O metrics (referring to vnode_metrics)
func (mp *MetricsParser) processBlkioMetric(port string, containerInfo ContainerInfo, device, major, minor, operation string, value float64) {
	// For simplicity, only count for now, do not store detailed information
	// Can be extended later to store complete block device I/O metrics
	fmt.Printf("[DEBUG] Processing blkio metric: port=%s, device=%s, operation=%s, value=%.2f\n", port, device, operation, value)
}

// processGpuMetric processes GPU metrics (referring to vnode_metrics)
func (mp *MetricsParser) processGpuMetric(port string, containerInfo ContainerInfo, minorNumber, metricType string, value float64) {
	// For simplicity, only count for now, do not store detailed information
	// Can be extended later to store complete GPU metrics
	fmt.Printf("[DEBUG] Processing GPU metric: port=%s, gpu=%s, type=%s, value=%.2f\n", port, minorNumber, metricType, value)
}

// processNetworkTcpMetric processes network TCP state metrics (referring to vnode_metrics)
func (mp *MetricsParser) processNetworkTcpMetric(port string, containerInfo ContainerInfo, interfaceName, tcpState string, value float64) {
	// For simplicity, only count for now, do not store detailed information
	// Can be extended later to store complete TCP state metrics
	fmt.Printf("[DEBUG] Processing network TCP metric: port=%s, interface=%s, state=%s, value=%.2f\n", port, interfaceName, tcpState, value)
}

// getContainerOriginStatInfoByPort gets or creates container original statistics (ported from vnode_metrics)
func (mp *MetricsParser) getContainerOriginStatInfoByPort(targetPort string, refID ContainerInfo) *ContainerMetrics {
	return mp.getOrCreateContainerMetrics(targetPort, refID)
}

// getOriginNetworkStatInfoByPort gets or creates network statistics (ported from vnode_metrics)
func (mp *MetricsParser) getOriginNetworkStatInfoByPort(targetPort string, refID ContainerInfo, interfaceName string) *NetworkMetrics {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	if mp.networkMetrics[targetPort] == nil {
		mp.networkMetrics[targetPort] = make(map[ContainerInfo]map[string]*NetworkMetrics)
	}
	if mp.networkMetrics[targetPort][refID] == nil {
		mp.networkMetrics[targetPort][refID] = make(map[string]*NetworkMetrics)
	}
	if mp.networkMetrics[targetPort][refID][interfaceName] == nil {
		mp.networkMetrics[targetPort][refID][interfaceName] = &NetworkMetrics{}
	}
	return mp.networkMetrics[targetPort][refID][interfaceName]
}

// getOriginFsStatInfoByPort gets or creates filesystem statistics (ported from vnode_metrics)
func (mp *MetricsParser) getOriginFsStatInfoByPort(targetPort string, refID ContainerInfo, device string) *FilesystemMetrics {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	if mp.fsMetrics[targetPort] == nil {
		mp.fsMetrics[targetPort] = make(map[ContainerInfo]map[string]*FilesystemMetrics)
	}
	if mp.fsMetrics[targetPort][refID] == nil {
		mp.fsMetrics[targetPort][refID] = make(map[string]*FilesystemMetrics)
	}
	if mp.fsMetrics[targetPort][refID][device] == nil {
		mp.fsMetrics[targetPort][refID][device] = &FilesystemMetrics{}
	}
	return mp.fsMetrics[targetPort][refID][device]
}

// getBlkioStatInfoByPort gets or creates block device I/O statistics (ported from vnode_metrics)
func (mp *MetricsParser) getBlkioStatInfoByPort(targetPort string, refID ContainerInfo, device, major, minor, operation string) *BlkioMetrics {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	if mp.blkioMetrics[targetPort] == nil {
		mp.blkioMetrics[targetPort] = make(map[ContainerInfo]map[string]*BlkioMetrics)
	}
	if mp.blkioMetrics[targetPort][refID] == nil {
		mp.blkioMetrics[targetPort][refID] = make(map[string]*BlkioMetrics)
	}

	// Use device+operation as key
	key := fmt.Sprintf("%s-%s", device, operation)
	if mp.blkioMetrics[targetPort][refID][key] == nil {
		mp.blkioMetrics[targetPort][refID][key] = &BlkioMetrics{
			Device:    device,
			Major:     major,
			Minor:     minor,
			Operation: operation,
		}
	}
	return mp.blkioMetrics[targetPort][refID][key]
}

// getEksOriginGpuStatInfoByPort gets or creates GPU statistics (ported from vnode_metrics)
func (mp *MetricsParser) getEksOriginGpuStatInfoByPort(targetPort string, refID ContainerInfo, minorNumber string) *GpuMetrics {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	if mp.gpuMetrics[targetPort] == nil {
		mp.gpuMetrics[targetPort] = make(map[ContainerInfo]map[string]*GpuMetrics)
	}
	if mp.gpuMetrics[targetPort][refID] == nil {
		mp.gpuMetrics[targetPort][refID] = make(map[string]*GpuMetrics)
	}
	if mp.gpuMetrics[targetPort][refID][minorNumber] == nil {
		mp.gpuMetrics[targetPort][refID][minorNumber] = &GpuMetrics{
			MinorNumber: minorNumber, // Keep consistent with vnode-metrics
		}
	}
	return mp.gpuMetrics[targetPort][refID][minorNumber]
}
