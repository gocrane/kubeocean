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
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
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

func TestTruncateHostnameIfNeeded(t *testing.T) {
	logger := logr.Discard() // Use a no-op logger for testing

	tests := []struct {
		name            string
		hostname        string
		expectedResult  string
		expectedError   bool
		errorContains   string
		validateTrimmed bool // If true, verify no trailing '-' or '.'
	}{
		{
			name:           "short hostname - no truncation needed",
			hostname:       "simple-hostname",
			expectedResult: "simple-hostname",
			expectedError:  false,
		},
		{
			name:           "exactly 63 chars - no truncation needed",
			hostname:       strings.Repeat("a", 63),
			expectedResult: strings.Repeat("a", 63),
			expectedError:  false,
		},
		{
			name:           "empty hostname",
			hostname:       "",
			expectedResult: "",
			expectedError:  false,
		},
		{
			name:           "single character",
			hostname:       "a",
			expectedResult: "a",
			expectedError:  false,
		},
		{
			name:            "64 chars - truncate to 63",
			hostname:        strings.Repeat("a", 64),
			expectedResult:  strings.Repeat("a", 63),
			expectedError:   false,
			validateTrimmed: true,
		},
		{
			name:            "100 chars - truncate to 63",
			hostname:        strings.Repeat("x", 100),
			expectedResult:  strings.Repeat("x", 63),
			expectedError:   false,
			validateTrimmed: true,
		},
		{
			name:            "64 chars ending with dash - truncate and trim dash",
			hostname:        strings.Repeat("a", 63) + "-",
			expectedResult:  strings.Repeat("a", 63),
			expectedError:   false,
			validateTrimmed: true,
		},
		{
			name:            "64 chars ending with dot - truncate and trim dot",
			hostname:        strings.Repeat("b", 63) + ".",
			expectedResult:  strings.Repeat("b", 63),
			expectedError:   false,
			validateTrimmed: true,
		},
		{
			name:            "65 chars ending with dash - truncate and trim",
			hostname:        strings.Repeat("c", 62) + "--",
			expectedResult:  strings.Repeat("c", 62),
			expectedError:   false,
			validateTrimmed: true,
		},
		{
			name:            "70 chars with multiple trailing dashes and dots",
			hostname:        strings.Repeat("d", 60) + "---...----",
			expectedResult:  strings.Repeat("d", 60),
			expectedError:   false,
			validateTrimmed: true,
		},
		{
			name:            "long hostname with mixed characters ending with dash",
			hostname:        "my-very-long-hostname-that-exceeds-the-63-characters-limit-abcdefg-", // 67 chars
			expectedResult:  "my-very-long-hostname-that-exceeds-the-63-characters-limit-abcd",     // 63 chars (truncated, no trim needed)
			expectedError:   false,
			validateTrimmed: true,
		},
		{
			name:            "long hostname with valid ending after truncation",
			hostname:        "my-super-duper-long-hostname-that-definitely-exceeds-the-limit123", // 69 chars
			expectedResult:  "my-super-duper-long-hostname-that-definitely-exceeds-the-limit1",   // 63 chars
			expectedError:   false,
			validateTrimmed: true,
		},
		{
			name:           "64 chars all dashes and dots - should error",
			hostname:       strings.Repeat("-.", 32),
			expectedResult: "",
			expectedError:  true,
			errorContains:  "hostname was invalid",
		},
		{
			name:            "normal hostname that needs truncation",
			hostname:        "pod-name-with-very-long-suffix-0123456789abcdef0123456789abcdef0123456789", // 81 chars
			expectedResult:  "pod-name-with-very-long-suffix-0123456789abcdef0123456789abcdef",           // 68 chars (first 63 of input)
			expectedError:   false,
			validateTrimmed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := TruncateHostnameIfNeeded(logger, tt.hostname)

			if tt.expectedError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)

				// Verify length constraint
				assert.LessOrEqual(t, len(result), 63, "Result should not exceed 63 characters")

				// Verify no trailing dashes or dots if validateTrimmed is set
				if tt.validateTrimmed && len(result) > 0 {
					lastChar := result[len(result)-1]
					assert.NotEqual(t, byte('-'), lastChar, "Result should not end with '-'")
					assert.NotEqual(t, byte('.'), lastChar, "Result should not end with '.'")
				}
			}
		})
	}
}

// TestTruncateHostnameIfNeeded_EdgeCases tests additional edge cases
func TestTruncateHostnameIfNeeded_EdgeCases(t *testing.T) {
	logger := logr.Discard()

	t.Run("hostname with unicode characters", func(t *testing.T) {
		// Even though DNS hostnames shouldn't have unicode, test that truncation works
		hostname := "pod-名字-" + strings.Repeat("a", 60)
		result, err := TruncateHostnameIfNeeded(logger, hostname)
		require.NoError(t, err)
		// Length is counted in bytes, not runes
		assert.LessOrEqual(t, len(result), 63)
	})

	t.Run("hostname exactly at boundary with valid ending", func(t *testing.T) {
		hostname := strings.Repeat("a", 62) + "z"
		result, err := TruncateHostnameIfNeeded(logger, hostname)
		require.NoError(t, err)
		assert.Equal(t, hostname, result)
		assert.Equal(t, 63, len(result))
	})

	t.Run("hostname with dots in middle but not at end", func(t *testing.T) {
		hostname := "my.pod.name." + strings.Repeat("x", 60)
		result, err := TruncateHostnameIfNeeded(logger, hostname)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(result), 63)
		// Should preserve dots in the middle
		assert.Contains(t, result, ".")
	})

	t.Run("hostname with dashes in middle but not at end", func(t *testing.T) {
		hostname := "my-pod-name-" + strings.Repeat("y", 60)
		result, err := TruncateHostnameIfNeeded(logger, hostname)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(result), 63)
		// Should preserve dashes in the middle
		assert.Contains(t, result, "-")
	})
}

func TestExtractPodFromDeleteEvent(t *testing.T) {
	logger := logr.Discard()

	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			UID:       "test-uid-123",
		},
	}

	tests := []struct {
		name        string
		obj         interface{}
		expectedPod *corev1.Pod
		expectOk    bool
	}{
		{
			name:        "normal pod delete event",
			obj:         testPod,
			expectedPod: testPod,
			expectOk:    true,
		},
		{
			name: "DeletedFinalStateUnknown with pod",
			obj: cache.DeletedFinalStateUnknown{
				Key: "default/test-pod",
				Obj: testPod,
			},
			expectedPod: testPod,
			expectOk:    true,
		},
		{
			name: "DeletedFinalStateUnknown with wrong type",
			obj: cache.DeletedFinalStateUnknown{
				Key: "default/test-service",
				Obj: &corev1.Service{},
			},
			expectOk: false,
		},
		{
			name:     "unexpected object type",
			obj:      &corev1.Service{},
			expectOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod, ok := ExtractPodFromDeleteEvent(tt.obj, logger)
			assert.Equal(t, tt.expectOk, ok)
			if tt.expectOk {
				require.NotNil(t, pod)
				assert.Equal(t, tt.expectedPod.Name, pod.Name)
				assert.Equal(t, tt.expectedPod.Namespace, pod.Namespace)
			} else {
				assert.Nil(t, pod)
			}
		})
	}
}
