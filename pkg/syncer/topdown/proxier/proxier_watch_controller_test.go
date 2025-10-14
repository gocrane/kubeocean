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

package topproxier

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

func TestProxierWatchController_isProxierPodForClusterBinding(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-binding",
		},
	}

	controller := NewProxierWatchController(nil, scheme, logr.Discard(), clusterBinding, 9006)

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "valid proxier pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "kubeocean-proxier-test-123",
					Labels: map[string]string{
						ProxierComponentLabel: ProxierComponentValue,
						ProxierNameLabel:      ProxierNameValue,
						ProxierManagedByLabel: ProxierManagedByValue,
						ProxierInstanceLabel:  "test-cluster-binding",
					},
				},
			},
			expected: true,
		},
		{
			name: "wrong component",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "kubeocean-proxier-test-123",
					Labels: map[string]string{
						ProxierComponentLabel: "other-component",
						ProxierNameLabel:      ProxierNameValue,
						ProxierManagedByLabel: ProxierManagedByValue,
						ProxierInstanceLabel:  "test-cluster-binding",
					},
				},
			},
			expected: false,
		},
		{
			name: "wrong instance",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "kubeocean-proxier-test-123",
					Labels: map[string]string{
						ProxierComponentLabel: ProxierComponentValue,
						ProxierNameLabel:      ProxierNameValue,
						ProxierManagedByLabel: ProxierManagedByValue,
						ProxierInstanceLabel:  "other-cluster-binding",
					},
				},
			},
			expected: false,
		},
		{
			name: "missing labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "kubeocean-proxier-test-123",
					Labels: map[string]string{},
				},
			},
			expected: false,
		},
		{
			name: "nil labels",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "kubeocean-proxier-test-123",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.isProxierPodForClusterBinding(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestProxierWatchController_getVNodeInternalIP(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-binding",
		},
	}

	controller := NewProxierWatchController(nil, scheme, logr.Discard(), clusterBinding, 9006)

	tests := []struct {
		name     string
		vNode    *corev1.Node
		expected string
	}{
		{
			name: "has internal IP",
			vNode: &corev1.Node{
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeExternalIP, Address: "1.2.3.4"},
						{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
						{Type: corev1.NodeHostName, Address: "test-node"},
					},
				},
			},
			expected: "10.0.0.1",
		},
		{
			name: "no internal IP",
			vNode: &corev1.Node{
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeExternalIP, Address: "1.2.3.4"},
						{Type: corev1.NodeHostName, Address: "test-node"},
					},
				},
			},
			expected: "",
		},
		{
			name: "empty addresses",
			vNode: &corev1.Node{
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{},
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.getVNodeInternalIP(tt.vNode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestProxierWatchController_createVNodeIPPatch(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-binding",
		},
	}

	controller := NewProxierWatchController(nil, scheme, logr.Discard(), clusterBinding, 9006)

	tests := []struct {
		name     string
		vNode    *corev1.Node
		newIP    string
		expected string
	}{
		{
			name: "update existing internal IP",
			vNode: &corev1.Node{
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeExternalIP, Address: "1.2.3.4"},
						{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
						{Type: corev1.NodeHostName, Address: "test-node"},
					},
				},
			},
			newIP:    "10.0.0.2",
			expected: "10.0.0.2", // Just check that the new IP is in the patch
		},
		{
			name: "add new internal IP",
			vNode: &corev1.Node{
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeExternalIP, Address: "1.2.3.4"},
						{Type: corev1.NodeHostName, Address: "test-node"},
					},
				},
			},
			newIP:    "10.0.0.2",
			expected: "10.0.0.2", // Just check that the new IP is in the patch
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.createVNodeIPPatch(tt.vNode, tt.newIP)
			// Verify that the patch contains the new IP
			assert.Contains(t, string(result), tt.expected)
		})
	}
}

func TestProxierWatchController_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster-binding",
		},
	}

	// Create a fake client with a Proxier pod
	proxierPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubeocean-proxier-test-123",
			Namespace: "kubeocean-system",
			Labels: map[string]string{
				ProxierComponentLabel: ProxierComponentValue,
				ProxierNameLabel:      ProxierNameValue,
				ProxierManagedByLabel: ProxierManagedByValue,
				ProxierInstanceLabel:  "test-cluster-binding",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.100",
		},
	}

	// Create VNodes
	vNode1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vnode-1",
			Labels: map[string]string{
				VNodeClusterBindingLabel: "test-cluster-binding",
				VNodeManagedByLabel:      VNodeManagedByValue,
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
			},
		},
	}

	vNode2 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vnode-2",
			Labels: map[string]string{
				VNodeClusterBindingLabel: "test-cluster-binding",
				VNodeManagedByLabel:      VNodeManagedByValue,
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.2"},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(proxierPod, vNode1, vNode2).
		Build()

	controller := NewProxierWatchController(fakeClient, scheme, logr.Discard(), clusterBinding, 9006)

	// Test reconcile with Proxier pod (first time - should trigger update)
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "kubeocean-proxier-test-123",
			Namespace: "kubeocean-system",
		},
	}

	result, err := controller.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Test reconcile again with same IP (should skip update due to IP change detection)
	result, err = controller.Reconcile(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify that VNodes were updated (this would require checking the client's actions in a real test)
	// For now, we just verify that the reconcile completed without error
}
