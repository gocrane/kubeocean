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

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsSystemPod(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		isSystem bool
	}{
		{
			name: "kube-system namespace pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kube-proxy-abc",
					Namespace: "kube-system",
				},
			},
			isSystem: true,
		},
		{
			name: "kube-public namespace pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-info-pod",
					Namespace: "kube-public",
				},
			},
			isSystem: true,
		},
		{
			name: "kube-node-lease namespace pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "lease-pod",
					Namespace: "kube-node-lease",
				},
			},
			isSystem: true,
		},
		{
			name: "kubeocean-system namespace pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubeocean-controller",
					Namespace: "kubeocean-system",
				},
			},
			isSystem: true,
		},
		{
			name: "default namespace pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-pod",
					Namespace: "default",
				},
			},
			isSystem: false,
		},
		{
			name: "custom namespace pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-app",
					Namespace: "my-app",
				},
			},
			isSystem: false,
		},
		{
			name: "empty namespace pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-namespace-pod",
					Namespace: "",
				},
			},
			isSystem: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsSystemPod(tt.pod)
			assert.Equal(t, tt.isSystem, result)
		})
	}
}

func TestIsDaemonSetPod(t *testing.T) {
	tests := []struct {
		name        string
		pod         *corev1.Pod
		isDaemonSet bool
	}{
		{
			name: "pod managed by daemonset",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "daemonset-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
			},
			isDaemonSet: true,
		},
		{
			name: "pod managed by deployment",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deployment-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "ReplicaSet",
							Name: "test-replicaset",
						},
					},
				},
			},
			isDaemonSet: false,
		},
		{
			name: "pod without owner references",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "standalone-pod",
					Namespace: "default",
				},
			},
			isDaemonSet: false,
		},
		{
			name: "pod with multiple owners including daemonset",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-owner-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind: "Job",
							Name: "test-job",
						},
						{
							Kind: "DaemonSet",
							Name: "test-daemonset",
						},
					},
				},
			},
			isDaemonSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsDaemonSetPod(tt.pod)
			assert.Equal(t, tt.isDaemonSet, result)
		})
	}
}
