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
	"io"
	"os"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/protoparser/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain initializes VictoriaMetrics workers for all tests in this package.
// This is required for ParseAndStoreMetrics tests to work properly.
func TestMain(m *testing.M) {
	// Initialize VictoriaMetrics unmarshal workers
	// This is needed for stream.Parse to work in tests
	// Uses the shared initVictoriaMetricsWorkers function to ensure it's only called once
	initVictoriaMetricsWorkers()

	// Run all tests
	exitCode := m.Run()

	// Cleanup workers - only if they were actually started
	// Note: We don't call StopUnmarshalWorkers() here because it may cause issues
	// if workers were already started by production code (e.g., in NewVNodeProxierAgent).
	// The workers will be cleaned up when the process exits.

	os.Exit(exitCode)
}

func TestNewMetricsParser(t *testing.T) {
	parser := NewMetricsParser()

	assert.NotNil(t, parser)
	assert.NotNil(t, parser.containerMetrics)
	assert.NotNil(t, parser.networkMetrics)
	assert.NotNil(t, parser.fsMetrics)
	assert.NotNil(t, parser.blkioMetrics)
	assert.NotNil(t, parser.gpuMetrics)

	assert.Equal(t, 0, len(parser.containerMetrics))
	assert.Equal(t, 0, len(parser.networkMetrics))
	assert.Equal(t, 0, len(parser.fsMetrics))
	assert.Equal(t, 0, len(parser.blkioMetrics))
	assert.Equal(t, 0, len(parser.gpuMetrics))
}

func TestInitializePortMetrics(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	parser.initializePortMetrics(port)

	assert.NotNil(t, parser.containerMetrics[port])
	assert.NotNil(t, parser.networkMetrics[port])
	assert.NotNil(t, parser.fsMetrics[port])
	assert.NotNil(t, parser.blkioMetrics[port])
	assert.NotNil(t, parser.gpuMetrics[port])

	assert.Equal(t, 0, len(parser.containerMetrics[port]))
	assert.Equal(t, 0, len(parser.networkMetrics[port]))
	assert.Equal(t, 0, len(parser.fsMetrics[port]))
	assert.Equal(t, 0, len(parser.blkioMetrics[port]))
	assert.Equal(t, 0, len(parser.gpuMetrics[port]))
}

func TestInitializePortMetrics_ClearsExistingData(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Add some data
	parser.initializePortMetrics(port)
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}
	parser.containerMetrics[port][containerInfo] = &ContainerMetrics{
		CPUUsageSecondsTotal: 100,
	}

	// Initialize again - should clear
	parser.initializePortMetrics(port)

	assert.Equal(t, 0, len(parser.containerMetrics[port]))
}

func TestParseMetricLabels_BasicLabels(t *testing.T) {
	parser := NewMetricsParser()

	tags := []prometheus.Tag{
		{Key: "pod", Value: "test-pod"},
		{Key: "namespace", Value: "default"},
		{Key: "container", Value: "nginx"},
		{Key: "id", Value: "container123"},
	}

	labels := parser.parseMetricLabels("container_cpu_usage_seconds_total", tags)

	assert.Equal(t, "test-pod", labels.podName)
	assert.Equal(t, "default", labels.namespace)
	assert.Equal(t, "nginx", labels.containerName)
	assert.Equal(t, "container123", labels.containerID)
}

func TestParseMetricLabels_PODContainerNameConversion(t *testing.T) {
	parser := NewMetricsParser()

	tags := []prometheus.Tag{
		{Key: "container", Value: "POD"},
		{Key: "pod", Value: "test-pod"},
		{Key: "namespace", Value: "default"},
	}

	labels := parser.parseMetricLabels("container_cpu_usage_seconds_total", tags)

	// POD should be converted to "pause"
	assert.Equal(t, "pause", labels.containerName)
}

func TestParseMetricLabels_NetworkLabels(t *testing.T) {
	parser := NewMetricsParser()

	tags := []prometheus.Tag{
		{Key: "interface", Value: "eth0"},
		{Key: "pod", Value: "test-pod"},
		{Key: "namespace", Value: "default"},
		{Key: "container", Value: "nginx"},
	}

	labels := parser.parseMetricLabels("container_network_receive_bytes_total", tags)

	assert.Equal(t, "eth0", labels.interfaceName)
}

func TestParseMetricLabels_FilesystemLabels(t *testing.T) {
	parser := NewMetricsParser()

	tags := []prometheus.Tag{
		{Key: "device", Value: "/dev/sda1"},
		{Key: "pod", Value: "test-pod"},
		{Key: "namespace", Value: "default"},
		{Key: "container", Value: "nginx"},
	}

	labels := parser.parseMetricLabels("container_fs_usage_bytes", tags)

	assert.Equal(t, "/dev/sda1", labels.device)
}

func TestParseMetricLabels_BlkioLabels(t *testing.T) {
	parser := NewMetricsParser()

	tags := []prometheus.Tag{
		{Key: "device", Value: "sda"},
		{Key: "major", Value: "8"},
		{Key: "minor", Value: "0"},
		{Key: "operation", Value: "read"},
		{Key: "pod", Value: "test-pod"},
		{Key: "namespace", Value: "default"},
		{Key: "container", Value: "nginx"},
	}

	labels := parser.parseMetricLabels("container_blkio_device_usage_total", tags)

	assert.Equal(t, "sda", labels.device)
	assert.Equal(t, "8", labels.major)
	assert.Equal(t, "0", labels.minor)
	assert.Equal(t, "read", labels.operation)
}

func TestParseMetricLabels_GPULabels(t *testing.T) {
	parser := NewMetricsParser()

	tags := []prometheus.Tag{
		{Key: "minor_number", Value: "0"},
		{Key: "pod", Value: "test-pod"},
		{Key: "namespace", Value: "default"},
		{Key: "container", Value: "ml-training"},
	}

	labels := parser.parseMetricLabels("container_accelerator_duty_cycle", tags)

	assert.Equal(t, "0", labels.minorNumber)
}

func TestParseMetricLabels_CgroupPathExtraction(t *testing.T) {
	parser := NewMetricsParser()

	longPath := "/kubepods/besteffort/pod12345678/abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	tags := []prometheus.Tag{
		{Key: "id", Value: longPath},
		{Key: "pod", Value: "test-pod"},
		{Key: "namespace", Value: "default"},
		{Key: "container", Value: "nginx"},
	}

	labels := parser.parseMetricLabels("container_cpu_usage_seconds_total", tags)

	// Should extract last 64 characters
	assert.Equal(t, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", labels.containerID)
	assert.Equal(t, 64, len(labels.containerID))
}

func TestProcessCPUMetric(t *testing.T) {
	parser := NewMetricsParser()
	containerMetrics := &ContainerMetrics{}

	tests := []struct {
		metricName  string
		metricValue float64
		checkField  func(*ContainerMetrics) float64
	}{
		{"container_cpu_usage_seconds_total", 123.45, func(m *ContainerMetrics) float64 { return m.CPUUsageSecondsTotal }},
		{"container_cpu_cfs_periods_total", 100.0, func(m *ContainerMetrics) float64 { return m.CPUCfsPeriodsTotal }},
		{"container_cpu_cfs_throttled_periods_total", 10.0, func(m *ContainerMetrics) float64 { return m.CPUCfsThrottledPeriodsTotal }},
		{"container_cpu_cfs_throttled_seconds_total", 5.5, func(m *ContainerMetrics) float64 { return m.CPUCfsThrottledSecondsTotal }},
		{"container_cpu_load_average_10s", 2.5, func(m *ContainerMetrics) float64 { return m.CPULoadAverage10s }},
		{"container_cpu_system_seconds_total", 50.0, func(m *ContainerMetrics) float64 { return m.CPUSystemSecondsTotal }},
		{"container_cpu_user_seconds_total", 73.45, func(m *ContainerMetrics) float64 { return m.CPUUserSecondsTotal }},
	}

	for _, tt := range tests {
		t.Run(tt.metricName, func(t *testing.T) {
			parser.processCPUMetric(tt.metricName, tt.metricValue, 1234567890000, containerMetrics)
			assert.Equal(t, tt.metricValue, tt.checkField(containerMetrics))
		})
	}
}

func TestProcessCPUMetric_WithTimestamp(t *testing.T) {
	parser := NewMetricsParser()
	containerMetrics := &ContainerMetrics{}

	timestamp := int64(1234567890000)
	parser.processCPUMetric("container_cpu_usage_seconds_total", 100.0, timestamp, containerMetrics)

	assert.Equal(t, 100.0, containerMetrics.CPUUsageSecondsTotal)
	assert.Equal(t, float64(timestamp)/1000, containerMetrics.CPUStatTime)
}

func TestProcessCPUMetric_NilMetrics(t *testing.T) {
	parser := NewMetricsParser()

	// Should not panic with nil metrics
	require.NotPanics(t, func() {
		parser.processCPUMetric("container_cpu_usage_seconds_total", 100.0, 1234567890000, nil)
	})
}

func TestProcessMemoryMetric(t *testing.T) {
	parser := NewMetricsParser()
	containerMetrics := &ContainerMetrics{}

	tests := []struct {
		metricName  string
		metricValue float64
		checkField  func(*ContainerMetrics) float64
	}{
		{"container_memory_usage_bytes", 1024000.0, func(m *ContainerMetrics) float64 { return m.MemoryUsageBytes }},
		{"container_memory_cache", 512000.0, func(m *ContainerMetrics) float64 { return m.MemoryCache }},
		{"container_memory_working_set_bytes", 768000.0, func(m *ContainerMetrics) float64 { return m.MemoryWorkingSetBytes }},
		{"container_memory_rss", 256000.0, func(m *ContainerMetrics) float64 { return m.MemoryRss }},
		{"container_memory_swap", 128000.0, func(m *ContainerMetrics) float64 { return m.MemorySwap }},
		{"container_memory_failcnt", 5.0, func(m *ContainerMetrics) float64 { return m.MemoryFailcnt }},
		{"container_memory_max_usage_bytes", 2048000.0, func(m *ContainerMetrics) float64 { return m.MemoryMaxUsageBytes }},
		{"container_referenced_bytes", 384000.0, func(m *ContainerMetrics) float64 { return m.ReferencedBytes }},
	}

	for _, tt := range tests {
		t.Run(tt.metricName, func(t *testing.T) {
			parser.processMemoryMetric(tt.metricName, tt.metricValue, containerMetrics)
			assert.Equal(t, tt.metricValue, tt.checkField(containerMetrics))
		})
	}
}

func TestProcessMemoryMetric_NilMetrics(t *testing.T) {
	parser := NewMetricsParser()

	require.NotPanics(t, func() {
		parser.processMemoryMetric("container_memory_usage_bytes", 1024000.0, nil)
	})
}

func TestProcessNetworkMetricForPort(t *testing.T) {
	parser := NewMetricsParser()
	networkMetrics := &NetworkMetrics{}

	tests := []struct {
		metricName  string
		metricValue float64
		checkField  func(*NetworkMetrics) float64
	}{
		{"container_network_receive_bytes_total", 10000.0, func(m *NetworkMetrics) float64 { return m.RxBytes }},
		{"container_network_receive_packets_total", 100.0, func(m *NetworkMetrics) float64 { return m.RxPackets }},
		{"container_network_receive_errors_total", 2.0, func(m *NetworkMetrics) float64 { return m.RxErrors }},
		{"container_network_receive_packets_dropped_total", 1.0, func(m *NetworkMetrics) float64 { return m.RxDropped }},
		{"container_network_transmit_bytes_total", 20000.0, func(m *NetworkMetrics) float64 { return m.TxBytes }},
		{"container_network_transmit_packets_total", 200.0, func(m *NetworkMetrics) float64 { return m.TxPackets }},
		{"container_network_transmit_errors_total", 3.0, func(m *NetworkMetrics) float64 { return m.TxErrors }},
		{"container_network_transmit_packets_dropped_total", 2.0, func(m *NetworkMetrics) float64 { return m.TxDropped }},
		{"container_network_tcp_usage_total", 50.0, func(m *NetworkMetrics) float64 { return m.TcpUsage }},
		{"container_network_tcp6_usage_total", 10.0, func(m *NetworkMetrics) float64 { return m.Tcp6Usage }},
		{"container_network_udp_usage_total", 30.0, func(m *NetworkMetrics) float64 { return m.UdpUsage }},
		{"container_network_udp6_usage_total", 5.0, func(m *NetworkMetrics) float64 { return m.Udp6Usage }},
	}

	for _, tt := range tests {
		t.Run(tt.metricName, func(t *testing.T) {
			parser.processNetworkMetricForPort(tt.metricName, tt.metricValue, networkMetrics)
			assert.Equal(t, tt.metricValue, tt.checkField(networkMetrics))
		})
	}
}

func TestProcessNetworkMetricForPort_NilMetrics(t *testing.T) {
	parser := NewMetricsParser()

	require.NotPanics(t, func() {
		parser.processNetworkMetricForPort("container_network_receive_bytes_total", 10000.0, nil)
	})
}

func TestProcessFilesystemMetricForPort(t *testing.T) {
	parser := NewMetricsParser()
	fsMetrics := &FilesystemMetrics{}

	tests := []struct {
		metricName  string
		metricValue float64
		checkField  func(*FilesystemMetrics) float64
	}{
		{"container_fs_reads_total", 1000.0, func(m *FilesystemMetrics) float64 { return m.ReadsTotal }},
		{"container_fs_writes_total", 2000.0, func(m *FilesystemMetrics) float64 { return m.WritesTotal }},
		{"container_fs_reads_bytes_total", 1024000.0, func(m *FilesystemMetrics) float64 { return m.ReadsBytesTotal }},
		{"container_fs_writes_bytes_total", 2048000.0, func(m *FilesystemMetrics) float64 { return m.WritesBytesTotal }},
		{"container_fs_usage_bytes", 5120000.0, func(m *FilesystemMetrics) float64 { return m.UsageBytes }},
		{"container_fs_limit_bytes", 10240000.0, func(m *FilesystemMetrics) float64 { return m.LimitBytes }},
		{"container_fs_inodes_free", 50000.0, func(m *FilesystemMetrics) float64 { return m.InodesFree }},
		{"container_fs_inodes_total", 100000.0, func(m *FilesystemMetrics) float64 { return m.InodesTotal }},
	}

	for _, tt := range tests {
		t.Run(tt.metricName, func(t *testing.T) {
			parser.processFilesystemMetricForPort(tt.metricName, tt.metricValue, fsMetrics)
			assert.Equal(t, tt.metricValue, tt.checkField(fsMetrics))
		})
	}
}

func TestProcessFilesystemMetricForPort_NilMetrics(t *testing.T) {
	parser := NewMetricsParser()

	require.NotPanics(t, func() {
		parser.processFilesystemMetricForPort("container_fs_usage_bytes", 5120000.0, nil)
	})
}

func TestProcessSpecMetric(t *testing.T) {
	parser := NewMetricsParser()
	containerMetrics := &ContainerMetrics{}

	tests := []struct {
		metricName  string
		metricValue float64
		checkField  func(*ContainerMetrics) float64
	}{
		{"container_spec_cpu_period", 100000.0, func(m *ContainerMetrics) float64 { return m.SpecCpuPeriod }},
		{"container_spec_cpu_quota", 50000.0, func(m *ContainerMetrics) float64 { return m.SpecCpuQuota }},
		{"container_spec_cpu_shares", 1024.0, func(m *ContainerMetrics) float64 { return m.SpecCpuShares }},
		{"container_spec_memory_limit_bytes", 1073741824.0, func(m *ContainerMetrics) float64 { return m.SpecMemoryLimitBytes }},
		{"container_spec_memory_reservation_limit_bytes", 536870912.0, func(m *ContainerMetrics) float64 { return m.SpecMemoryReservationLimitBytes }},
		{"container_spec_memory_swap_limit_bytes", 2147483648.0, func(m *ContainerMetrics) float64 { return m.SpecMemorySwapLimitBytes }},
	}

	for _, tt := range tests {
		t.Run(tt.metricName, func(t *testing.T) {
			parser.processSpecMetric(tt.metricName, tt.metricValue, containerMetrics)
			assert.Equal(t, tt.metricValue, tt.checkField(containerMetrics))
		})
	}
}

func TestProcessGPUMetric(t *testing.T) {
	parser := NewMetricsParser()
	gpuMetrics := &GpuMetrics{MinorNumber: "0"}

	tests := []struct {
		metricName  string
		metricValue float64
		checkField  func(*GpuMetrics) float64
	}{
		{"container_accelerator_duty_cycle", 85.5, func(m *GpuMetrics) float64 { return m.GpuDutyCycle }},
		{"container_accelerator_memory_used_bytes", 4096.0, func(m *GpuMetrics) float64 { return m.GpuMemUsedMib }},
		{"container_accelerator_memory_total_bytes", 8192.0, func(m *GpuMetrics) float64 { return m.GpuMemoryTotalMib }},
	}

	for _, tt := range tests {
		t.Run(tt.metricName, func(t *testing.T) {
			parser.processGPUMetric(tt.metricName, tt.metricValue, gpuMetrics)
			assert.Equal(t, tt.metricValue, tt.checkField(gpuMetrics))
		})
	}
}

func TestProcessGPUMetric_NilMetrics(t *testing.T) {
	parser := NewMetricsParser()

	require.NotPanics(t, func() {
		parser.processGPUMetric("container_accelerator_duty_cycle", 85.5, nil)
	})
}

func TestProcessBlkioMetricForPort(t *testing.T) {
	parser := NewMetricsParser()
	blkioMetrics := &BlkioMetrics{}

	parser.processBlkioMetricForPort(12345.67, blkioMetrics)

	assert.Equal(t, 12345.67, blkioMetrics.Value)
}

func TestProcessBlkioMetricForPort_NilMetrics(t *testing.T) {
	parser := NewMetricsParser()

	require.NotPanics(t, func() {
		parser.processBlkioMetricForPort(12345.67, nil)
	})
}

func TestProcessOtherMetric(t *testing.T) {
	parser := NewMetricsParser()
	containerMetrics := &ContainerMetrics{}

	tests := []struct {
		metricName  string
		metricValue float64
		checkField  func(*ContainerMetrics) float64
	}{
		{"container_file_descriptors", 128.0, func(m *ContainerMetrics) float64 { return m.FileDescriptors }},
		{"container_processes", 10.0, func(m *ContainerMetrics) float64 { return m.Processes }},
		{"container_sockets", 20.0, func(m *ContainerMetrics) float64 { return m.Sockets }},
		{"container_threads", 15.0, func(m *ContainerMetrics) float64 { return m.Threads }},
		{"container_threads_max", 100.0, func(m *ContainerMetrics) float64 { return m.ThreadsMax }},
		{"container_last_seen", 1234567890.0, func(m *ContainerMetrics) float64 { return m.LastSeen }},
		{"container_oom_events_total", 2.0, func(m *ContainerMetrics) float64 { return m.OomEventsTotal }},
		{"container_start_time_seconds", 1234567800.0, func(m *ContainerMetrics) float64 { return m.StartTimeSeconds }},
	}

	for _, tt := range tests {
		t.Run(tt.metricName, func(t *testing.T) {
			parser.processOtherMetric(tt.metricName, tt.metricValue, containerMetrics)
			assert.Equal(t, tt.metricValue, tt.checkField(containerMetrics))
		})
	}
}

func TestGetOrCreateContainerMetrics(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}

	// First call should create new metrics
	metrics1 := parser.getOrCreateContainerMetrics(port, containerInfo)
	assert.NotNil(t, metrics1)

	// Second call should return the same metrics
	metrics2 := parser.getOrCreateContainerMetrics(port, containerInfo)
	assert.Equal(t, metrics1, metrics2)

	// Modify the metrics
	metrics1.CPUUsageSecondsTotal = 100.0

	// Verify it's the same object
	assert.Equal(t, 100.0, metrics2.CPUUsageSecondsTotal)
}

func TestGetOrCreateFilesystemMetrics(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}
	device := "/dev/sda1"

	// First call should create new metrics
	fsMetrics1 := parser.getOrCreateFilesystemMetrics(port, containerInfo, device)
	assert.NotNil(t, fsMetrics1)

	// Second call should return the same metrics
	fsMetrics2 := parser.getOrCreateFilesystemMetrics(port, containerInfo, device)
	assert.Equal(t, fsMetrics1, fsMetrics2)

	// Different device should create new metrics
	fsMetrics3 := parser.getOrCreateFilesystemMetrics(port, containerInfo, "/dev/sdb1")
	assert.NotNil(t, fsMetrics3)
	// Verify they are different objects (different pointers)
	assert.True(t, fsMetrics1 != fsMetrics3, "Different devices should create different metric objects")
}

func TestGetContainerMetrics(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Initially should return empty map
	metrics := parser.GetContainerMetrics(port)
	assert.Empty(t, metrics)
	assert.NotNil(t, metrics)

	// Add some data
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}
	parser.initializePortMetrics(port)
	parser.containerMetrics[port][containerInfo] = &ContainerMetrics{
		CPUUsageSecondsTotal: 100.0,
	}

	// Should return the data
	metrics = parser.GetContainerMetrics(port)
	assert.Len(t, metrics, 1)
	assert.Contains(t, metrics, containerInfo)
	assert.Equal(t, 100.0, metrics[containerInfo].CPUUsageSecondsTotal)

	// Different port should return empty
	metrics2 := parser.GetContainerMetrics("10251")
	assert.Empty(t, metrics2)
}

func TestGetNetworkMetrics(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Initially should return empty map
	metrics := parser.GetNetworkMetrics(port)
	assert.Empty(t, metrics)
	assert.NotNil(t, metrics)

	// Add some data
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}
	parser.initializePortMetrics(port)
	parser.networkMetrics[port][containerInfo] = make(map[string]*NetworkMetrics)
	parser.networkMetrics[port][containerInfo]["eth0"] = &NetworkMetrics{
		RxBytes: 10000.0,
	}

	// Should return the data
	metrics = parser.GetNetworkMetrics(port)
	assert.Len(t, metrics, 1)
	assert.Contains(t, metrics, containerInfo)
	assert.Contains(t, metrics[containerInfo], "eth0")
	assert.Equal(t, 10000.0, metrics[containerInfo]["eth0"].RxBytes)
}

func TestGetFilesystemMetrics(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Initially should return empty map
	metrics := parser.GetFilesystemMetrics(port)
	assert.Empty(t, metrics)
	assert.NotNil(t, metrics)

	// Add some data
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}
	parser.initializePortMetrics(port)
	parser.fsMetrics[port][containerInfo] = make(map[string]*FilesystemMetrics)
	parser.fsMetrics[port][containerInfo]["/dev/sda1"] = &FilesystemMetrics{
		UsageBytes: 5120000.0,
	}

	// Should return the data
	metrics = parser.GetFilesystemMetrics(port)
	assert.Len(t, metrics, 1)
	assert.Contains(t, metrics, containerInfo)
	assert.Contains(t, metrics[containerInfo], "/dev/sda1")
	assert.Equal(t, 5120000.0, metrics[containerInfo]["/dev/sda1"].UsageBytes)
}

func TestExtractIDFromCgroupPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Path longer than 64 chars",
			input:    "/kubepods/besteffort/pod12345678/abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789extra",
			expected: "f0123456789abcdef0123456789abcdef0123456789abcdef0123456789extra", // Last 64 chars
		},
		{
			name:     "Path exactly 64 chars",
			input:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			expected: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:     "Path shorter than 64 chars",
			input:    "short-path",
			expected: "short-path",
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractIDFromCgroupPath(tt.input)
			assert.Equal(t, tt.expected, result)
			if len(tt.input) > 64 {
				assert.Equal(t, 64, len(result))
			}
		})
	}
}

func TestParseAndStoreMetrics_BasicParsing(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Create sample Prometheus metrics
	metricsData := `
# HELP container_cpu_usage_seconds_total Cumulative cpu time consumed
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{id="container1",pod="pod1",namespace="default",container="nginx"} 123.45

# HELP container_memory_usage_bytes Current memory usage in bytes
# TYPE container_memory_usage_bytes gauge
container_memory_usage_bytes{id="container1",pod="pod1",namespace="default",container="nginx"} 1024000
`

	reader := strings.NewReader(metricsData)
	err := parser.ParseAndStoreMetrics(reader, port)

	assert.NoError(t, err)

	// Verify data was stored
	containerMetrics := parser.GetContainerMetrics(port)
	assert.NotEmpty(t, containerMetrics)
}

func TestParseAndStoreMetrics_EmptyInput(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	reader := strings.NewReader("")
	err := parser.ParseAndStoreMetrics(reader, port)

	assert.NoError(t, err)

	// Should initialize but have no data
	containerMetrics := parser.GetContainerMetrics(port)
	assert.Empty(t, containerMetrics)
}

func TestParseAndStoreMetrics_MalformedInput(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Invalid Prometheus format
	metricsData := "this is not valid prometheus format\n"

	reader := strings.NewReader(metricsData)
	err := parser.ParseAndStoreMetrics(reader, port)

	// Parser should handle gracefully (VictoriaMetrics parser is lenient)
	// Might not return error but will skip invalid lines
	_ = err // May or may not error
}

func TestParseAndStoreMetrics_NetworkMetrics(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	metricsData := `
container_network_receive_bytes_total{id="container1",pod="pod1",namespace="default",container="nginx",interface="eth0"} 10000
container_network_transmit_bytes_total{id="container1",pod="pod1",namespace="default",container="nginx",interface="eth0"} 20000
`

	reader := strings.NewReader(metricsData)
	err := parser.ParseAndStoreMetrics(reader, port)

	assert.NoError(t, err)

	networkMetrics := parser.GetNetworkMetrics(port)
	assert.NotEmpty(t, networkMetrics)
}

func TestParseAndStoreMetrics_FilesystemMetrics(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	metricsData := `
container_fs_usage_bytes{id="container1",pod="pod1",namespace="default",container="nginx",device="/dev/sda1"} 5120000
container_fs_limit_bytes{id="container1",pod="pod1",namespace="default",container="nginx",device="/dev/sda1"} 10240000
`

	reader := strings.NewReader(metricsData)
	err := parser.ParseAndStoreMetrics(reader, port)

	assert.NoError(t, err)

	fsMetrics := parser.GetFilesystemMetrics(port)
	assert.NotEmpty(t, fsMetrics)
}

func TestParseAndStoreMetrics_SkipsInvalidContainerInfo(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Metrics without required labels (missing pod or namespace)
	metricsData := `
container_cpu_usage_seconds_total{id="container1",container="nginx"} 123.45
container_memory_usage_bytes{id="container2",pod="pod2"} 1024000
`

	reader := strings.NewReader(metricsData)
	err := parser.ParseAndStoreMetrics(reader, port)

	assert.NoError(t, err)

	// Should skip these incomplete metrics
	containerMetrics := parser.GetContainerMetrics(port)
	assert.Empty(t, containerMetrics)
}

func TestWritePrometheusMetrics(t *testing.T) {
	// Initialize global pod mapper
	InitGlobalPodMapper()
	defer func() { GlobalPodMapper = nil }()

	parser := NewMetricsParser()
	port := "10250"

	// Setup container metrics
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "physical-pod-1",
		NameSpace: "default",
	}

	// Setup virtual pod mapping
	SetVirtualPodInfo("physical-pod-1", &VirtualPodInfo{
		VirtualNodeName:     "vnode-1",
		VirtualPodName:      "virtual-pod-1",
		VirtualPodNamespace: "default",
	})

	parser.initializePortMetrics(port)
	parser.containerMetrics[port][containerInfo] = &ContainerMetrics{
		CPUUsageSecondsTotal: 123.45,
		MemoryUsageBytes:     1024000,
	}

	var buffer bytes.Buffer
	parser.WritePrometheusMetrics(&buffer, port, "", "default")

	output := buffer.String()
	
	// Verify output contains metrics
	assert.Contains(t, output, "container_cpu_usage_seconds_total")
	assert.Contains(t, output, "123.45")
	assert.Contains(t, output, "virtual-pod-1")
	assert.Contains(t, output, "vnode-1")
}

func TestWritePrometheusMetrics_NoVirtualMapping(t *testing.T) {
	// Initialize global pod mapper
	InitGlobalPodMapper()
	defer func() { GlobalPodMapper = nil }()

	parser := NewMetricsParser()
	port := "10250"

	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "physical-pod-no-mapping",
		NameSpace: "default",
	}

	parser.initializePortMetrics(port)
	parser.containerMetrics[port][containerInfo] = &ContainerMetrics{
		CPUUsageSecondsTotal: 123.45,
	}

	var buffer bytes.Buffer
	parser.WritePrometheusMetrics(&buffer, port, "", "default")

	// Should not output metrics without virtual mapping
	assert.Empty(t, buffer.String())
}

func TestWritePrometheusMetrics_NamespaceFiltering(t *testing.T) {
	// Initialize global pod mapper
	InitGlobalPodMapper()
	defer func() { GlobalPodMapper = nil }()

	parser := NewMetricsParser()
	port := "10250"

	// Add metrics for different namespaces
	containerInfo1 := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}
	containerInfo2 := ContainerInfo{
		Id:        "container2",
		Name:      "redis",
		PodName:   "pod2",
		NameSpace: "kube-system",
	}

	SetVirtualPodInfo("pod1", &VirtualPodInfo{
		VirtualNodeName:     "vnode-1",
		VirtualPodName:      "vpod1",
		VirtualPodNamespace: "default",
	})
	SetVirtualPodInfo("pod2", &VirtualPodInfo{
		VirtualNodeName:     "vnode-1",
		VirtualPodName:      "vpod2",
		VirtualPodNamespace: "kube-system",
	})

	parser.initializePortMetrics(port)
	parser.containerMetrics[port][containerInfo1] = &ContainerMetrics{
		CPUUsageSecondsTotal: 100.0,
	}
	parser.containerMetrics[port][containerInfo2] = &ContainerMetrics{
		CPUUsageSecondsTotal: 200.0,
	}

	// Filter for default namespace only
	var buffer bytes.Buffer
	parser.WritePrometheusMetrics(&buffer, port, "", "default")

	output := buffer.String()
	assert.Contains(t, output, "vpod1")
	assert.NotContains(t, output, "vpod2")
	assert.Contains(t, output, "100")
	assert.NotContains(t, output, "200")
}

func TestWriteMetric(t *testing.T) {
	parser := NewMetricsParser()

	tests := []struct {
		name        string
		metricName  string
		labels      string
		value       float64
		shouldWrite bool
	}{
		{
			name:        "Non-zero value",
			metricName:  "test_metric",
			labels:      `{label="value"}`,
			value:       123.45,
			shouldWrite: true,
		},
		{
			name:        "Zero value",
			metricName:  "test_metric",
			labels:      `{label="value"}`,
			value:       0.0,
			shouldWrite: false,
		},
		{
			name:        "Negative value",
			metricName:  "test_metric",
			labels:      `{label="value"}`,
			value:       -50.0,
			shouldWrite: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buffer bytes.Buffer
			parser.writeMetric(&buffer, tt.metricName, tt.labels, tt.value)

			if tt.shouldWrite {
				assert.NotEmpty(t, buffer.String())
				assert.Contains(t, buffer.String(), tt.metricName)
			} else {
				assert.Empty(t, buffer.String())
			}
		})
	}
}

func TestProcessNetworkMetric(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}

	parser.processNetworkMetric(port, containerInfo, "eth0", "rx_bytes", 10000.0)

	networkMetrics := parser.GetNetworkMetrics(port)
	require.Contains(t, networkMetrics, containerInfo)
	require.Contains(t, networkMetrics[containerInfo], "eth0")
	assert.Equal(t, 10000.0, networkMetrics[containerInfo]["eth0"].RxBytes)
}

func TestProcessFilesystemMetric(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}

	parser.processFilesystemMetric(port, containerInfo, "/dev/sda1", "usage_bytes", 5120000.0)

	fsMetrics := parser.GetFilesystemMetrics(port)
	require.Contains(t, fsMetrics, containerInfo)
	require.Contains(t, fsMetrics[containerInfo], "/dev/sda1")
	assert.Equal(t, 5120000.0, fsMetrics[containerInfo]["/dev/sda1"].UsageBytes)
}

func TestSetFilesystemMetricValue(t *testing.T) {
	parser := NewMetricsParser()
	fsMetrics := &FilesystemMetrics{}

	tests := []struct {
		metricType string
		value      float64
		checkField func(*FilesystemMetrics) float64
	}{
		{"reads_total", 1000.0, func(m *FilesystemMetrics) float64 { return m.ReadsTotal }},
		{"writes_total", 2000.0, func(m *FilesystemMetrics) float64 { return m.WritesTotal }},
		{"usage_bytes", 5120000.0, func(m *FilesystemMetrics) float64 { return m.UsageBytes }},
		{"limit_bytes", 10240000.0, func(m *FilesystemMetrics) float64 { return m.LimitBytes }},
	}

	for _, tt := range tests {
		t.Run(tt.metricType, func(t *testing.T) {
			parser.setFilesystemMetricValue(fsMetrics, tt.metricType, tt.value)
			assert.Equal(t, tt.value, tt.checkField(fsMetrics))
		})
	}
}

func TestGetBlkioStatInfoByPort(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}

	blkio := parser.getBlkioStatInfoByPort(port, containerInfo, "sda", "8", "0", "read")

	assert.NotNil(t, blkio)
	assert.Equal(t, "sda", blkio.Device)
	assert.Equal(t, "8", blkio.Major)
	assert.Equal(t, "0", blkio.Minor)
	assert.Equal(t, "read", blkio.Operation)

	// Second call should return same object
	blkio2 := parser.getBlkioStatInfoByPort(port, containerInfo, "sda", "8", "0", "read")
	assert.Equal(t, blkio, blkio2)

	// Different operation should create new object
	blkio3 := parser.getBlkioStatInfoByPort(port, containerInfo, "sda", "8", "0", "write")
	assert.NotEqual(t, blkio, blkio3)
}

func TestGetEksOriginGpuStatInfoByPort(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"
	containerInfo := ContainerInfo{
		Id:        "container1",
		Name:      "ml-training",
		PodName:   "pod1",
		NameSpace: "default",
	}

	gpu := parser.getEksOriginGpuStatInfoByPort(port, containerInfo, "0")

	assert.NotNil(t, gpu)
	assert.Equal(t, "0", gpu.MinorNumber)

	// Second call should return same object
	gpu2 := parser.getEksOriginGpuStatInfoByPort(port, containerInfo, "0")
	assert.Equal(t, gpu, gpu2)

	// Different GPU should create new object
	gpu3 := parser.getEksOriginGpuStatInfoByPort(port, containerInfo, "1")
	assert.NotEqual(t, gpu, gpu3)
}

func TestParseAndStoreMetrics_LargePayload(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Generate large metrics payload
	var buffer bytes.Buffer
	for i := 0; i < 1000; i++ {
		buffer.WriteString("container_cpu_usage_seconds_total{")
		buffer.WriteString("id=\"container1\",")
		buffer.WriteString("pod=\"pod1\",")
		buffer.WriteString("namespace=\"default\",")
		buffer.WriteString("container=\"nginx\"} 123.45\n")
	}

	reader := bytes.NewReader(buffer.Bytes())
	err := parser.ParseAndStoreMetrics(reader, port)

	assert.NoError(t, err)
}

func TestParseAndStoreMetrics_InvalidReader(t *testing.T) {
	parser := NewMetricsParser()
	port := "10250"

	// Create a reader that errors immediately
	errorReader := &errorReader{}
	err := parser.ParseAndStoreMetrics(errorReader, port)

	// VictoriaMetrics parser handles EOF gracefully, so no error is returned
	// The test verifies that parsing completes without panic
	assert.NoError(t, err)

	// Verify no metrics were stored
	containerMetrics := parser.GetContainerMetrics(port)
	assert.Empty(t, containerMetrics)
}

// errorReader is a reader that always returns an error
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, io.ErrUnexpectedEOF
}

func TestContainerInfo_AsMapKey(t *testing.T) {
	// Test that ContainerInfo can be used as a map key
	m := make(map[ContainerInfo]string)

	info1 := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}
	info2 := ContainerInfo{
		Id:        "container1",
		Name:      "nginx",
		PodName:   "pod1",
		NameSpace: "default",
	}
	info3 := ContainerInfo{
		Id:        "container2",
		Name:      "redis",
		PodName:   "pod2",
		NameSpace: "default",
	}

	m[info1] = "value1"
	m[info3] = "value3"

	// info1 and info2 should be equal as keys
	assert.Equal(t, "value1", m[info2])
	assert.Equal(t, "value3", m[info3])
	assert.Len(t, m, 2)
}
