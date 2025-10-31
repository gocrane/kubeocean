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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
)

func TestNewMetricsCollector(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	assert.NotNil(t, collector)
	assert.Equal(t, "test-cluster", collector.clusterbindingName)
	assert.NotNil(t, collector.metrics)
	assert.Equal(t, 0, len(collector.metrics))
}

func TestSetVnodeMetrics(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	allocatable := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("4000m"),
		corev1.ResourceMemory: resource.MustParse("8Gi"),
	}
	available := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("3000m"),
		corev1.ResourceMemory: resource.MustParse("6Gi"),
	}
	physicalUsage := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1000m"),
		corev1.ResourceMemory: resource.MustParse("2Gi"),
	}
	virtualUsage := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("1Gi"),
	}

	collector.SetVnodeMetrics("vnode-1", allocatable, available, physicalUsage, virtualUsage)

	// Verify metrics are stored
	assert.Equal(t, 1, len(collector.metrics))
	metrics, exists := collector.GetVnodeMetrics("vnode-1")
	assert.True(t, exists)
	assert.NotNil(t, metrics)
	assert.Equal(t, "vnode-1", metrics.VNodeName)
	assert.True(t, metrics.AllocatableResources[corev1.ResourceCPU].Equal(allocatable[corev1.ResourceCPU]))
	assert.True(t, metrics.AvailableResources[corev1.ResourceMemory].Equal(available[corev1.ResourceMemory]))
}

func TestGetVnodeMetrics(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	resources := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2000m"),
	}

	collector.SetVnodeMetrics("vnode-1", resources, resources, resources, resources)

	// Test existing metrics
	metrics, exists := collector.GetVnodeMetrics("vnode-1")
	assert.True(t, exists)
	assert.NotNil(t, metrics)
	assert.Equal(t, "vnode-1", metrics.VNodeName)

	// Test non-existing metrics
	_, exists = collector.GetVnodeMetrics("non-existing")
	assert.False(t, exists)
}

func TestDeleteVnodeMetrics(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	resources := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2000m"),
	}

	collector.SetVnodeMetrics("vnode-1", resources, resources, resources, resources)
	assert.Equal(t, 1, len(collector.metrics))

	collector.DeleteVnodeMetrics("vnode-1")
	assert.Equal(t, 0, len(collector.metrics))

	_, exists := collector.GetVnodeMetrics("vnode-1")
	assert.False(t, exists)
}

func TestGetAllMetrics(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	resources1 := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2000m"),
	}
	resources2 := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("4000m"),
	}

	collector.SetVnodeMetrics("vnode-1", resources1, resources1, resources1, resources1)
	collector.SetVnodeMetrics("vnode-2", resources2, resources2, resources2, resources2)

	allMetrics := collector.GetAllMetrics()
	assert.Equal(t, 2, len(allMetrics))
	assert.NotNil(t, allMetrics["vnode-1"])
	assert.NotNil(t, allMetrics["vnode-2"])
}

func TestClear(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	resources := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2000m"),
	}

	collector.SetVnodeMetrics("vnode-1", resources, resources, resources, resources)
	collector.SetVnodeMetrics("vnode-2", resources, resources, resources, resources)
	assert.Equal(t, 2, len(collector.metrics))

	collector.Clear()
	assert.Equal(t, 0, len(collector.metrics))
}

func TestCollectorDescribe(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	ch := make(chan *prometheus.Desc, 20)
	collector.Describe(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}

	// Should have 11 descriptors: vnodeCount + 4 vnode-level + 4 cluster-level + 2 pod count
	assert.Equal(t, 11, count)
}

func TestCollectorCollect(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	resources := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("4000m"),
		corev1.ResourceMemory: resource.MustParse("8Gi"),
	}

	collector.SetVnodeMetrics("vnode-1", resources, resources, resources, resources)

	ch := make(chan prometheus.Metric, 100)
	go func() {
		collector.Collect(ch)
		close(ch)
	}()

	metricCount := 0
	for range ch {
		metricCount++
	}

	// Should have: 1 vnode count + 8 resource metrics (4 types * 2 resources per vnode) + 8 cluster metrics (4 types * 2 resources)
	assert.Greater(t, metricCount, 0)
}

func TestConvertResourceValue(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	tests := []struct {
		name         string
		resourceName string
		quantity     resource.Quantity
		expectedOk   bool
		expectedVal  float64
	}{
		{
			name:         "CPU in millicores",
			resourceName: ResourceCPU,
			quantity:     resource.MustParse("2000m"),
			expectedOk:   true,
			expectedVal:  2000,
		},
		{
			name:         "Memory in MiB",
			resourceName: ResourceMemory,
			quantity:     resource.MustParse("1024Mi"),
			expectedOk:   true,
			expectedVal:  1024,
		},
		{
			name:         "Unsupported resource",
			resourceName: "unsupported.io/resource",
			quantity:     resource.MustParse("100"),
			expectedOk:   false,
			expectedVal:  0,
		},
		{
			name:         "GPU resource",
			resourceName: ResourceNvidiaGPU,
			quantity:     resource.MustParse("2"),
			expectedOk:   true,
			expectedVal:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := collector.convertResourceValue(tt.resourceName, tt.quantity)
			assert.Equal(t, tt.expectedOk, ok)
			if ok {
				assert.InDelta(t, tt.expectedVal, value, 0.1)
			}
		})
	}
}

func TestIsSupportedResource(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	tests := []struct {
		resourceName string
		expected     bool
	}{
		{ResourceCPU, true},
		{ResourceMemory, true},
		{ResourceEphemeralStorage, true},
		{ResourceGocraneCPU, true},
		{ResourceGocraneMemory, true},
		{ResourceENIIP, true},
		{ResourceSubENI, true},
		{ResourceDirectENI, true},
		{ResourceNvidiaGPU, true},
		{ResourceTKESharedRDMA, true},
		{"unsupported.io/resource", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.resourceName, func(t *testing.T) {
			result := collector.isSupportedResource(tt.resourceName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCountVirtualPodsByPhase(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Create test pods
	pods := []client.Object{
		// Pod with vnode- prefix, should be counted
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-1",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: "vnode-test-1",
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
		// Another pod with vnode- prefix
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-2",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: "vnode-test-2",
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
			},
		},
		// Pod without vnode- prefix, should NOT be counted
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-3",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: "node-1",
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
		// Pod without nodeName, should NOT be counted
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-4",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pods...).Build()
	collector := NewMetricsCollector("test-cluster", fakeClient, nil)

	counts := collector.countVirtualPodsByPhase(fakeClient)

	assert.Equal(t, 1, counts[corev1.PodRunning])
	assert.Equal(t, 1, counts[corev1.PodPending])
	assert.Equal(t, 0, counts[corev1.PodSucceeded])
	assert.Equal(t, 0, counts[corev1.PodFailed])
}

func TestCountVirtualPodsByPhase_WithDaemonSetPods(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)

	// Helper function to create daemonset pod
	createDaemonsetPod := func(name, nodeName string, annotations map[string]string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   "default",
				Annotations: annotations,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind: "DaemonSet",
						Name: "test-daemonset",
					},
				},
			},
			Spec: corev1.PodSpec{
				NodeName: nodeName,
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		}
	}

	// Helper function to create regular pod
	createRegularPod := func(name, nodeName string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: nodeName,
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		}
	}

	// Helper function to create clusterBinding
	createClusterBinding := func(name string, runningDaemonsetByDefault bool) *cloudv1beta1.ClusterBinding {
		return &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:                 "test-cluster-id",
				RunningDaemonsetByDefault: runningDaemonsetByDefault,
				SecretRef: corev1.SecretReference{
					Name:      "test-secret",
					Namespace: "default",
				},
			},
		}
	}

	tests := []struct {
		name                   string
		pods                   []client.Object
		clusterBinding         *cloudv1beta1.ClusterBinding
		clusterBindingName     string
		expectedRunningCount   int
		expectedPendingCount   int
		expectedSucceededCount int
		expectedFailedCount    int
	}{
		{
			name: "daemonset pod without annotation and RunningDaemonsetByDefault=false should not be counted",
			pods: []client.Object{
				createDaemonsetPod("daemonset-pod", "vnode-test-1", nil),
				createRegularPod("regular-pod", "vnode-test-2"),
			},
			clusterBinding:       createClusterBinding("test-cluster-binding", false),
			clusterBindingName:   "test-cluster-binding",
			expectedRunningCount: 1, // Only regular pod should be counted
		},
		{
			name: "daemonset pod without annotation and RunningDaemonsetByDefault=true should be counted",
			pods: []client.Object{
				createDaemonsetPod("daemonset-pod", "vnode-test-1", nil),
				createRegularPod("regular-pod", "vnode-test-2"),
			},
			clusterBinding:       createClusterBinding("test-cluster-binding", true),
			clusterBindingName:   "test-cluster-binding",
			expectedRunningCount: 2, // Both daemonset and regular pod should be counted
		},
		{
			name: "daemonset pod with annotation and RunningDaemonsetByDefault=false should be counted",
			pods: []client.Object{
				createDaemonsetPod("daemonset-pod-with-annotation", "vnode-test-1", map[string]string{
					cloudv1beta1.AnnotationRunningDaemonSet: cloudv1beta1.LabelValueTrue,
				}),
			},
			clusterBinding:       createClusterBinding("test-cluster-binding", false),
			clusterBindingName:   "test-cluster-binding",
			expectedRunningCount: 1, // Daemonset pod with annotation should be counted
		},
		{
			name: "daemonset pod without annotation and clusterBinding not found should not be counted",
			pods: []client.Object{
				createDaemonsetPod("daemonset-pod", "vnode-test-1", nil),
			},
			clusterBinding:       nil,
			clusterBindingName:   "non-existent",
			expectedRunningCount: 0, // Daemonset pod should not be counted when clusterBinding not found
		},
		{
			name: "system daemonset pod should not be counted even with RunningDaemonsetByDefault=true",
			pods: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "system-daemonset-pod",
						Namespace: "kube-system",
						OwnerReferences: []metav1.OwnerReference{
							{
								Kind: "DaemonSet",
								Name: "test-daemonset",
							},
						},
					},
					Spec: corev1.PodSpec{
						NodeName: "vnode-test-1",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
			clusterBinding:       createClusterBinding("test-cluster-binding", true),
			clusterBindingName:   "test-cluster-binding",
			expectedRunningCount: 0, // System pods should never be counted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []client.Object
			objects = append(objects, tt.pods...)
			if tt.clusterBinding != nil {
				objects = append(objects, tt.clusterBinding)
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			collector := NewMetricsCollector(tt.clusterBindingName, fakeClient, nil)

			counts := collector.countVirtualPodsByPhase(fakeClient)

			assert.Equal(t, tt.expectedRunningCount, counts[corev1.PodRunning], "Running count mismatch")
			if tt.expectedPendingCount > 0 || counts[corev1.PodPending] > 0 {
				assert.Equal(t, tt.expectedPendingCount, counts[corev1.PodPending], "Pending count mismatch")
			}
			if tt.expectedSucceededCount > 0 || counts[corev1.PodSucceeded] > 0 {
				assert.Equal(t, tt.expectedSucceededCount, counts[corev1.PodSucceeded], "Succeeded count mismatch")
			}
			if tt.expectedFailedCount > 0 || counts[corev1.PodFailed] > 0 {
				assert.Equal(t, tt.expectedFailedCount, counts[corev1.PodFailed], "Failed count mismatch")
			}
		})
	}
}

func TestCountPhysicalPodsByPhase(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)

	// Create test pods
	pods := []client.Object{
		// Pod with kubeocean label, should be counted
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-1",
				Namespace: "default",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
		// Another pod with kubeocean label
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-2",
				Namespace: "default",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
			},
		},
		// Pod without kubeocean label, should NOT be counted
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-3",
				Namespace: "default",
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
		// Pod with wrong label value, should NOT be counted
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-4",
				Namespace: "default",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: "other-system",
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pods...).Build()
	collector := NewMetricsCollector("test-cluster", nil, fakeClient)

	counts := collector.countPhysicalPodsByPhase(fakeClient)

	assert.Equal(t, 1, counts[corev1.PodRunning])
	assert.Equal(t, 0, counts[corev1.PodPending])
	assert.Equal(t, 0, counts[corev1.PodSucceeded])
	assert.Equal(t, 1, counts[corev1.PodFailed])
}

func TestCollectPodMetrics(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)

	// Create virtual pods
	virtualPods := []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "virtual-pod-1",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName: "vnode-test-1",
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
	}

	// Create physical pods
	physicalPods := []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "physical-pod-1",
				Namespace: "default",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		},
	}

	virtualClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(virtualPods...).Build()
	physicalClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(physicalPods...).Build()

	collector := NewMetricsCollector("test-cluster", virtualClient, physicalClient)

	ch := make(chan prometheus.Metric, 100)
	go func() {
		collector.collectPodMetrics(ch)
		close(ch)
	}()

	foundVirtualPod := false
	foundPhysicalPod := false

	for metric := range ch {
		dtoMetric := &dto.Metric{}
		err := metric.Write(dtoMetric)
		assert.NoError(t, err)

		desc := metric.Desc()
		descString := desc.String()

		if strings.Contains(descString, "virtual_pod_total") {
			foundVirtualPod = true
		}
		if strings.Contains(descString, "physical_pod_total") {
			foundPhysicalPod = true
		}
	}

	assert.True(t, foundVirtualPod)
	assert.True(t, foundPhysicalPod)
}

func TestAddResourceList(t *testing.T) {
	dest := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1000m"),
	}

	src := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("2000m"),
		corev1.ResourceMemory: resource.MustParse("4Gi"),
	}

	addResourceList(dest, src)

	// CPU should be added (1000m + 2000m = 3000m)
	expectedCPU := resource.MustParse("3000m")
	assert.True(t, dest[corev1.ResourceCPU].Equal(expectedCPU))

	// Memory should be copied
	expectedMemory := resource.MustParse("4Gi")
	assert.True(t, dest[corev1.ResourceMemory].Equal(expectedMemory))
}

func TestCollectClusterMetrics(t *testing.T) {
	collector := NewMetricsCollector("test-cluster", nil, nil)

	// Add two vnodes with resources
	resources1 := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("4000m"),
		corev1.ResourceMemory: resource.MustParse("8Gi"),
	}
	resources2 := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("4000m"),
		corev1.ResourceMemory: resource.MustParse("8Gi"),
	}

	collector.SetVnodeMetrics("vnode-1", resources1, resources1, resources1, resources1)
	collector.SetVnodeMetrics("vnode-2", resources2, resources2, resources2, resources2)

	ch := make(chan prometheus.Metric, 100)
	go func() {
		collector.collectClusterMetrics(ch)
		close(ch)
	}()

	foundOrigin := false
	foundAvailable := false
	foundPhysical := false
	foundVirtual := false

	for metric := range ch {
		desc := metric.Desc()
		descString := desc.String()

		if strings.Contains(descString, "cluster_origin_resource") {
			foundOrigin = true
		}
		if strings.Contains(descString, "cluster_available_resource") {
			foundAvailable = true
		}
		if strings.Contains(descString, "cluster_physical_usage") {
			foundPhysical = true
		}
		if strings.Contains(descString, "cluster_virtual_usage") {
			foundVirtual = true
		}
	}

	assert.True(t, foundOrigin)
	assert.True(t, foundAvailable)
	assert.True(t, foundPhysical)
	assert.True(t, foundVirtual)
}

func TestCollectorWithNilClients(t *testing.T) {
	// Test that collector doesn't panic with nil clients
	collector := NewMetricsCollector("test-cluster", nil, nil)

	ch := make(chan prometheus.Metric, 100)
	assert.NotPanics(t, func() {
		collector.collectPodMetrics(ch)
	})
	close(ch)
}

func TestMetricsCollectorContext(t *testing.T) {
	// Test with context cancellation
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	collector := NewMetricsCollector("test-cluster", fakeClient, fakeClient)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel() // Cancel immediately

	// Should not panic even with cancelled context
	assert.NotPanics(t, func() {
		_ = collector.countVirtualPodsByPhase(fakeClient)
	})

	// Verify context is cancelled
	assert.Error(t, ctx.Err())
}
