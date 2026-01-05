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

package proxier

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitGlobalPodMapper tests the initialization of global pod mapper
func TestInitGlobalPodMapper(t *testing.T) {
	t.Run("initialize from nil", func(t *testing.T) {
		GlobalPodMapper = nil
		InitGlobalPodMapper()
		require.NotNil(t, GlobalPodMapper)
		assert.IsType(t, &sync.Map{}, GlobalPodMapper)
	})

	t.Run("reinitialize creates new instance", func(t *testing.T) {
		InitGlobalPodMapper()
		firstMapper := GlobalPodMapper
		
		// Store some data
		GlobalPodMapper.Store("test-pod", &VirtualPodInfo{
			VirtualNodeName:     "vnode-1",
			VirtualPodName:      "pod-1",
			VirtualPodNamespace: "default",
		})
		
		// Reinitialize
		InitGlobalPodMapper()
		secondMapper := GlobalPodMapper
		
		// Should be a different instance
		assert.NotEqual(t, firstMapper, secondMapper)
		
		// Data should be cleared
		_, exists := GlobalPodMapper.Load("test-pod")
		assert.False(t, exists)
	})
}

// TestVirtualPodInfo tests VirtualPodInfo struct
func TestVirtualPodInfo(t *testing.T) {
	tests := []struct {
		name     string
		info     *VirtualPodInfo
		validate func(t *testing.T, info *VirtualPodInfo)
	}{
		{
			name: "complete pod info",
			info: &VirtualPodInfo{
				VirtualNodeName:     "vnode-cluster1-node1",
				VirtualPodName:      "nginx-deployment-abc123",
				VirtualPodNamespace: "production",
			},
			validate: func(t *testing.T, info *VirtualPodInfo) {
				assert.Equal(t, "vnode-cluster1-node1", info.VirtualNodeName)
				assert.Equal(t, "nginx-deployment-abc123", info.VirtualPodName)
				assert.Equal(t, "production", info.VirtualPodNamespace)
			},
		},
		{
			name: "empty pod info",
			info: &VirtualPodInfo{},
			validate: func(t *testing.T, info *VirtualPodInfo) {
				assert.Empty(t, info.VirtualNodeName)
				assert.Empty(t, info.VirtualPodName)
				assert.Empty(t, info.VirtualPodNamespace)
			},
		},
		{
			name: "pod info with special characters",
			info: &VirtualPodInfo{
				VirtualNodeName:     "vnode-test-cluster-001",
				VirtualPodName:      "my-app-v1.2.3-xyz",
				VirtualPodNamespace: "test-namespace-123",
			},
			validate: func(t *testing.T, info *VirtualPodInfo) {
				assert.Equal(t, "vnode-test-cluster-001", info.VirtualNodeName)
				assert.Equal(t, "my-app-v1.2.3-xyz", info.VirtualPodName)
				assert.Equal(t, "test-namespace-123", info.VirtualPodNamespace)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.info)
		})
	}
}

// TestSetAndGetVirtualPodInfo tests setting and retrieving pod info
func TestSetAndGetVirtualPodInfo(t *testing.T) {
	// Initialize mapper before each test
	InitGlobalPodMapper()

	t.Run("set and get existing pod", func(t *testing.T) {
		physicalName := "physical-pod-1"
		expectedInfo := &VirtualPodInfo{
			VirtualNodeName:     "vnode-1",
			VirtualPodName:      "virtual-pod-1",
			VirtualPodNamespace: "default",
		}

		SetVirtualPodInfo(physicalName, expectedInfo)
		
		info, exists := GetVirtualPodInfo(physicalName)
		require.True(t, exists)
		require.NotNil(t, info)
		assert.Equal(t, expectedInfo.VirtualNodeName, info.VirtualNodeName)
		assert.Equal(t, expectedInfo.VirtualPodName, info.VirtualPodName)
		assert.Equal(t, expectedInfo.VirtualPodNamespace, info.VirtualPodNamespace)
	})

	t.Run("get non-existent pod", func(t *testing.T) {
		info, exists := GetVirtualPodInfo("non-existent-pod")
		assert.False(t, exists)
		assert.Nil(t, info)
	})

	t.Run("overwrite existing pod info", func(t *testing.T) {
		physicalName := "physical-pod-2"
		
		// Set initial info
		initialInfo := &VirtualPodInfo{
			VirtualNodeName:     "vnode-1",
			VirtualPodName:      "virtual-pod-2",
			VirtualPodNamespace: "default",
		}
		SetVirtualPodInfo(physicalName, initialInfo)
		
		// Overwrite with new info
		newInfo := &VirtualPodInfo{
			VirtualNodeName:     "vnode-2",
			VirtualPodName:      "virtual-pod-2-updated",
			VirtualPodNamespace: "production",
		}
		SetVirtualPodInfo(physicalName, newInfo)
		
		// Verify new info is stored
		info, exists := GetVirtualPodInfo(physicalName)
		require.True(t, exists)
		assert.Equal(t, newInfo.VirtualNodeName, info.VirtualNodeName)
		assert.Equal(t, newInfo.VirtualPodName, info.VirtualPodName)
		assert.Equal(t, newInfo.VirtualPodNamespace, info.VirtualPodNamespace)
	})

	t.Run("set with nil GlobalPodMapper", func(t *testing.T) {
		GlobalPodMapper = nil
		
		// Should not panic
		SetVirtualPodInfo("test-pod", &VirtualPodInfo{
			VirtualNodeName: "vnode-1",
		})
		
		// Re-initialize for cleanup
		InitGlobalPodMapper()
	})

	t.Run("get with nil GlobalPodMapper", func(t *testing.T) {
		GlobalPodMapper = nil
		
		info, exists := GetVirtualPodInfo("test-pod")
		assert.False(t, exists)
		assert.Nil(t, info)
		
		// Re-initialize for cleanup
		InitGlobalPodMapper()
	})
}

// TestDeleteVirtualPodInfo tests deleting pod info
func TestDeleteVirtualPodInfo(t *testing.T) {
	InitGlobalPodMapper()

	t.Run("delete existing pod", func(t *testing.T) {
		physicalName := "physical-pod-to-delete"
		info := &VirtualPodInfo{
			VirtualNodeName:     "vnode-1",
			VirtualPodName:      "virtual-pod-1",
			VirtualPodNamespace: "default",
		}

		// Store pod info
		SetVirtualPodInfo(physicalName, info)
		
		// Verify it exists
		_, exists := GetVirtualPodInfo(physicalName)
		require.True(t, exists)
		
		// Delete pod info
		DeleteVirtualPodInfo(physicalName)
		
		// Verify it's deleted
		_, exists = GetVirtualPodInfo(physicalName)
		assert.False(t, exists)
	})

	t.Run("delete non-existent pod", func(t *testing.T) {
		// Should not panic
		DeleteVirtualPodInfo("non-existent-pod")
	})

	t.Run("delete with nil GlobalPodMapper", func(t *testing.T) {
		GlobalPodMapper = nil
		
		// Should not panic
		DeleteVirtualPodInfo("test-pod")
		
		// Re-initialize for cleanup
		InitGlobalPodMapper()
	})

	t.Run("delete and re-add pod", func(t *testing.T) {
		physicalName := "physical-pod-cycle"
		info1 := &VirtualPodInfo{
			VirtualNodeName:     "vnode-1",
			VirtualPodName:      "virtual-pod-1",
			VirtualPodNamespace: "default",
		}
		info2 := &VirtualPodInfo{
			VirtualNodeName:     "vnode-2",
			VirtualPodName:      "virtual-pod-2",
			VirtualPodNamespace: "production",
		}

		// Add, delete, and re-add
		SetVirtualPodInfo(physicalName, info1)
		DeleteVirtualPodInfo(physicalName)
		SetVirtualPodInfo(physicalName, info2)
		
		// Verify latest info
		info, exists := GetVirtualPodInfo(physicalName)
		require.True(t, exists)
		assert.Equal(t, info2.VirtualNodeName, info.VirtualNodeName)
		assert.Equal(t, info2.VirtualPodName, info.VirtualPodName)
	})
}

// TestGetPodMappingCount tests the pod mapping count functionality
func TestGetPodMappingCount(t *testing.T) {
	InitGlobalPodMapper()

	t.Run("empty mapper", func(t *testing.T) {
		count := GetPodMappingCount()
		assert.Equal(t, 0, count)
	})

	t.Run("single pod", func(t *testing.T) {
		SetVirtualPodInfo("pod-1", &VirtualPodInfo{
			VirtualNodeName: "vnode-1",
		})
		
		count := GetPodMappingCount()
		assert.Equal(t, 1, count)
	})

	t.Run("multiple pods", func(t *testing.T) {
		InitGlobalPodMapper() // Clear previous state
		
		for i := 1; i <= 10; i++ {
			SetVirtualPodInfo(
				"pod-"+string(rune(i)),
				&VirtualPodInfo{
					VirtualNodeName:     "vnode-1",
					VirtualPodName:      "vpod-" + string(rune(i)),
					VirtualPodNamespace: "default",
				},
			)
		}
		
		count := GetPodMappingCount()
		assert.Equal(t, 10, count)
	})

	t.Run("count after deletion", func(t *testing.T) {
		InitGlobalPodMapper()
		
		// Add 5 pods
		for i := 1; i <= 5; i++ {
			SetVirtualPodInfo(
				"pod-"+string(rune('a'+i)),
				&VirtualPodInfo{VirtualNodeName: "vnode-1"},
			)
		}
		
		assert.Equal(t, 5, GetPodMappingCount())
		
		// Delete 2 pods
		DeleteVirtualPodInfo("pod-b")
		DeleteVirtualPodInfo("pod-d")
		
		assert.Equal(t, 3, GetPodMappingCount())
	})

	t.Run("count with nil GlobalPodMapper", func(t *testing.T) {
		GlobalPodMapper = nil
		
		count := GetPodMappingCount()
		assert.Equal(t, 0, count)
		
		// Re-initialize for cleanup
		InitGlobalPodMapper()
	})

	t.Run("count with overwritten pods", func(t *testing.T) {
		InitGlobalPodMapper()
		
		// Add a pod
		SetVirtualPodInfo("pod-1", &VirtualPodInfo{VirtualNodeName: "vnode-1"})
		assert.Equal(t, 1, GetPodMappingCount())
		
		// Overwrite same pod (count should remain 1)
		SetVirtualPodInfo("pod-1", &VirtualPodInfo{VirtualNodeName: "vnode-2"})
		assert.Equal(t, 1, GetPodMappingCount())
	})
}

// TestConcurrentAccess tests concurrent access to the pod mapper
func TestConcurrentAccess(t *testing.T) {
	InitGlobalPodMapper()

	t.Run("concurrent writes", func(t *testing.T) {
		InitGlobalPodMapper()
		
		var wg sync.WaitGroup
		numGoroutines := 100
		
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				
				podName := "concurrent-pod-" + string(rune(id))
				info := &VirtualPodInfo{
					VirtualNodeName:     "vnode-1",
					VirtualPodName:      podName,
					VirtualPodNamespace: "default",
				}
				
				SetVirtualPodInfo(podName, info)
			}(i)
		}
		
		wg.Wait()
		
		// Should have all pods stored
		count := GetPodMappingCount()
		assert.Equal(t, numGoroutines, count)
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		InitGlobalPodMapper()
		
		// Pre-populate some data
		for i := 0; i < 50; i++ {
			SetVirtualPodInfo(
				"init-pod-"+string(rune(i)),
				&VirtualPodInfo{VirtualNodeName: "vnode-1"},
			)
		}
		
		var wg sync.WaitGroup
		
		// Start readers
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < 10; j++ {
					GetVirtualPodInfo("init-pod-" + string(rune(id)))
				}
			}(i)
		}
		
		// Start writers
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < 10; j++ {
					SetVirtualPodInfo(
						"new-pod-"+string(rune(id)),
						&VirtualPodInfo{VirtualNodeName: "vnode-2"},
					)
				}
			}(i)
		}
		
		wg.Wait()
		
		// Should not panic and have data
		count := GetPodMappingCount()
		assert.Greater(t, count, 0)
	})

	t.Run("concurrent deletes", func(t *testing.T) {
		InitGlobalPodMapper()
		
		// Pre-populate data
		numPods := 100
		for i := 0; i < numPods; i++ {
			SetVirtualPodInfo(
				"delete-pod-"+string(rune(i)),
				&VirtualPodInfo{VirtualNodeName: "vnode-1"},
			)
		}
		
		assert.Equal(t, numPods, GetPodMappingCount())
		
		var wg sync.WaitGroup
		
		// Delete all pods concurrently
		for i := 0; i < numPods; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				DeleteVirtualPodInfo("delete-pod-" + string(rune(id)))
			}(i)
		}
		
		wg.Wait()
		
		// All pods should be deleted
		count := GetPodMappingCount()
		assert.Equal(t, 0, count)
	})
}

// TestPodMappingEdgeCases tests edge cases for pod mapping
func TestPodMappingEdgeCases(t *testing.T) {
	InitGlobalPodMapper()

	t.Run("empty physical pod name", func(t *testing.T) {
		info := &VirtualPodInfo{VirtualNodeName: "vnode-1"}
		SetVirtualPodInfo("", info)
		
		retrievedInfo, exists := GetVirtualPodInfo("")
		assert.True(t, exists)
		assert.Equal(t, info.VirtualNodeName, retrievedInfo.VirtualNodeName)
	})

	t.Run("very long pod name", func(t *testing.T) {
		longName := ""
		for i := 0; i < 1000; i++ {
			longName += "a"
		}
		
		info := &VirtualPodInfo{
			VirtualNodeName:     "vnode-1",
			VirtualPodName:      "virtual-pod",
			VirtualPodNamespace: "default",
		}
		
		SetVirtualPodInfo(longName, info)
		
		retrievedInfo, exists := GetVirtualPodInfo(longName)
		require.True(t, exists)
		assert.Equal(t, info.VirtualPodName, retrievedInfo.VirtualPodName)
	})

	t.Run("pod name with special characters", func(t *testing.T) {
		specialNames := []string{
			"pod-with-dashes",
			"pod.with.dots",
			"pod_with_underscores",
			"pod-123-456",
			"pod-name-with-very-long-suffix-abcdef123456",
		}
		
		for _, name := range specialNames {
			info := &VirtualPodInfo{
				VirtualNodeName: "vnode-1",
				VirtualPodName:  name,
			}
			
			SetVirtualPodInfo(name, info)
			
			retrievedInfo, exists := GetVirtualPodInfo(name)
			require.True(t, exists, "failed for name: %s", name)
			assert.Equal(t, name, retrievedInfo.VirtualPodName)
		}
	})

	t.Run("nil VirtualPodInfo", func(t *testing.T) {
		// Setting nil should not panic
		SetVirtualPodInfo("nil-pod", nil)
		
		// Getting should return nil with exists=true (since key exists)
		info, exists := GetVirtualPodInfo("nil-pod")
		assert.True(t, exists)
		assert.Nil(t, info)
	})

	t.Run("type assertion failure", func(t *testing.T) {
		// Directly store wrong type in sync.Map
		GlobalPodMapper.Store("wrong-type-pod", "not-a-VirtualPodInfo")
		
		info, ok := GetVirtualPodInfo("wrong-type-pod")
		assert.False(t, ok)
		assert.Nil(t, info)
	})
}

// TestPodMapperIsolation tests that each test properly isolates the global state
func TestPodMapperIsolation(t *testing.T) {
	t.Run("test 1 - add pods", func(t *testing.T) {
		InitGlobalPodMapper()
		
		SetVirtualPodInfo("isolation-test-1", &VirtualPodInfo{VirtualNodeName: "vnode-1"})
		assert.Equal(t, 1, GetPodMappingCount())
	})

	t.Run("test 2 - should be isolated", func(t *testing.T) {
		InitGlobalPodMapper()
		
		// Should not see pods from previous test
		assert.Equal(t, 0, GetPodMappingCount())
		
		SetVirtualPodInfo("isolation-test-2", &VirtualPodInfo{VirtualNodeName: "vnode-2"})
		assert.Equal(t, 1, GetPodMappingCount())
	})

	t.Run("test 3 - verify isolation again", func(t *testing.T) {
		InitGlobalPodMapper()
		
		assert.Equal(t, 0, GetPodMappingCount())
	})
}
