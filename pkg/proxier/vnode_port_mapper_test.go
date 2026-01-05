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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddVNodePort(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	tests := []struct {
		name      string
		vnodeName string
		port      string
	}{
		{
			name:      "Add single vnode port mapping",
			vnodeName: "vnode-1",
			port:      "10250",
		},
		{
			name:      "Add vnode with different port",
			vnodeName: "vnode-2",
			port:      "10251",
		},
		{
			name:      "Overwrite existing vnode port",
			vnodeName: "vnode-1",
			port:      "10252",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper.AddVNodePort(tt.vnodeName, tt.port)

			port, exists := mapper.GetPortByVNodeName(tt.vnodeName)
			require.True(t, exists)
			assert.Equal(t, tt.port, port)
		})
	}
}

func TestRemoveVNodePort(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	// Setup: add some mappings
	mapper.AddVNodePort("vnode-1", "10250")
	mapper.AddVNodePort("vnode-2", "10251")
	mapper.AddVNodePort("vnode-3", "10252")

	tests := []struct {
		name      string
		vnodeName string
	}{
		{
			name:      "Remove existing vnode",
			vnodeName: "vnode-2",
		},
		{
			name:      "Remove non-existing vnode (no error)",
			vnodeName: "vnode-999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper.RemoveVNodePort(tt.vnodeName)

			_, exists := mapper.GetPortByVNodeName(tt.vnodeName)
			assert.False(t, exists)
		})
	}

	// Verify remaining mappings
	port1, exists1 := mapper.GetPortByVNodeName("vnode-1")
	assert.True(t, exists1)
	assert.Equal(t, "10250", port1)

	port3, exists3 := mapper.GetPortByVNodeName("vnode-3")
	assert.True(t, exists3)
	assert.Equal(t, "10252", port3)
}

func TestGetPortByVNodeName(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	mapper.AddVNodePort("vnode-1", "10250")
	mapper.AddVNodePort("vnode-2", "10251")

	tests := []struct {
		name        string
		vnodeName   string
		expectedPort string
		expectExists bool
	}{
		{
			name:        "Get existing vnode port",
			vnodeName:   "vnode-1",
			expectedPort: "10250",
			expectExists: true,
		},
		{
			name:        "Get another existing vnode port",
			vnodeName:   "vnode-2",
			expectedPort: "10251",
			expectExists: true,
		},
		{
			name:        "Get non-existing vnode port",
			vnodeName:   "vnode-999",
			expectedPort: "",
			expectExists: false,
		},
		{
			name:        "Get empty vnode name",
			vnodeName:   "",
			expectedPort: "",
			expectExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, exists := mapper.GetPortByVNodeName(tt.vnodeName)
			assert.Equal(t, tt.expectExists, exists)
			if tt.expectExists {
				assert.Equal(t, tt.expectedPort, port)
			}
		})
	}
}

func TestGetAllVNodes(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	t.Run("Empty mapper returns empty slice", func(t *testing.T) {
		vnodes := mapper.GetAllVNodes()
		assert.Empty(t, vnodes)
		assert.NotNil(t, vnodes)
	})

	t.Run("Returns all vnode names", func(t *testing.T) {
		mapper.AddVNodePort("vnode-1", "10250")
		mapper.AddVNodePort("vnode-2", "10251")
		mapper.AddVNodePort("vnode-3", "10252")

		vnodes := mapper.GetAllVNodes()
		assert.Len(t, vnodes, 3)
		assert.Contains(t, vnodes, "vnode-1")
		assert.Contains(t, vnodes, "vnode-2")
		assert.Contains(t, vnodes, "vnode-3")
	})

	t.Run("After removal, returns updated list", func(t *testing.T) {
		mapper.RemoveVNodePort("vnode-2")

		vnodes := mapper.GetAllVNodes()
		assert.Len(t, vnodes, 2)
		assert.Contains(t, vnodes, "vnode-1")
		assert.Contains(t, vnodes, "vnode-3")
		assert.NotContains(t, vnodes, "vnode-2")
	})
}

func TestGetVNodeCount(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	t.Run("Empty mapper returns 0", func(t *testing.T) {
		count := mapper.GetVNodeCount()
		assert.Equal(t, 0, count)
	})

	t.Run("Count increases with additions", func(t *testing.T) {
		mapper.AddVNodePort("vnode-1", "10250")
		assert.Equal(t, 1, mapper.GetVNodeCount())

		mapper.AddVNodePort("vnode-2", "10251")
		assert.Equal(t, 2, mapper.GetVNodeCount())

		mapper.AddVNodePort("vnode-3", "10252")
		assert.Equal(t, 3, mapper.GetVNodeCount())
	})

	t.Run("Count decreases with removals", func(t *testing.T) {
		mapper.RemoveVNodePort("vnode-2")
		assert.Equal(t, 2, mapper.GetVNodeCount())

		mapper.RemoveVNodePort("vnode-1")
		assert.Equal(t, 1, mapper.GetVNodeCount())
	})

	t.Run("Overwriting does not change count", func(t *testing.T) {
		currentCount := mapper.GetVNodeCount()
		mapper.AddVNodePort("vnode-3", "10999")
		assert.Equal(t, currentCount, mapper.GetVNodeCount())
	})
}

func TestClear(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	t.Run("Clear empty mapper", func(t *testing.T) {
		mapper.Clear()
		assert.Equal(t, 0, mapper.GetVNodeCount())
	})

	t.Run("Clear populated mapper", func(t *testing.T) {
		mapper.AddVNodePort("vnode-1", "10250")
		mapper.AddVNodePort("vnode-2", "10251")
		mapper.AddVNodePort("vnode-3", "10252")

		assert.Equal(t, 3, mapper.GetVNodeCount())

		mapper.Clear()

		assert.Equal(t, 0, mapper.GetVNodeCount())
		assert.Empty(t, mapper.GetAllVNodes())

		// Verify no mappings exist
		_, exists := mapper.GetPortByVNodeName("vnode-1")
		assert.False(t, exists)
	})

	t.Run("Can add after clear", func(t *testing.T) {
		mapper.AddVNodePort("vnode-new", "10250")
		assert.Equal(t, 1, mapper.GetVNodeCount())
		
		port, exists := mapper.GetPortByVNodeName("vnode-new")
		assert.True(t, exists)
		assert.Equal(t, "10250", port)
	})
}

func TestString(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	t.Run("Empty mapper string representation", func(t *testing.T) {
		str := mapper.String()
		assert.Contains(t, str, "GlobalVNodePortMapper")
		assert.Contains(t, str, "count: 0")
	})

	t.Run("Populated mapper string representation", func(t *testing.T) {
		mapper.AddVNodePort("vnode-1", "10250")
		mapper.AddVNodePort("vnode-2", "10251")

		str := mapper.String()
		assert.Contains(t, str, "GlobalVNodePortMapper")
		assert.Contains(t, str, "count: 2")
		assert.Contains(t, str, "vnode-1")
		assert.Contains(t, str, "10250")
		assert.Contains(t, str, "vnode-2")
		assert.Contains(t, str, "10251")
	})
}

func TestVNodePortMapperConcurrentAccess(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	t.Run("Concurrent additions", func(t *testing.T) {
		var wg sync.WaitGroup
		numGoroutines := 100

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				vnodeName := "vnode-" + string(rune(id))
				port := "10250"
				mapper.AddVNodePort(vnodeName, port)
			}(i)
		}

		wg.Wait()

		// All additions should complete without panic
		assert.True(t, mapper.GetVNodeCount() > 0)
	})

	t.Run("Concurrent reads and writes", func(t *testing.T) {
		mapper.Clear()
		mapper.AddVNodePort("vnode-base", "10250")

		var wg sync.WaitGroup
		numOperations := 50

		// Writers
		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				mapper.AddVNodePort("vnode-writer", "10250")
			}(i)
		}

		// Readers
		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = mapper.GetPortByVNodeName("vnode-base")
			}()
		}

		// Count readers
		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = mapper.GetVNodeCount()
			}()
		}

		wg.Wait()

		// No panic means thread-safety works
		assert.True(t, mapper.GetVNodeCount() >= 1)
	})

	t.Run("Concurrent additions and removals", func(t *testing.T) {
		mapper.Clear()

		var wg sync.WaitGroup
		numOperations := 50

		// Add operations
		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				vnodeName := "vnode-concurrent"
				port := "10250"
				mapper.AddVNodePort(vnodeName, port)
			}(i)
		}

		// Remove operations
		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				mapper.RemoveVNodePort("vnode-concurrent")
			}()
		}

		wg.Wait()

		// Operation completed without panic
		count := mapper.GetVNodeCount()
		assert.True(t, count >= 0)
	})

	t.Run("Concurrent GetAllVNodes", func(t *testing.T) {
		mapper.Clear()
		mapper.AddVNodePort("vnode-1", "10250")
		mapper.AddVNodePort("vnode-2", "10251")

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				vnodes := mapper.GetAllVNodes()
				assert.True(t, len(vnodes) >= 0)
			}()
		}

		wg.Wait()
	})

	t.Run("Concurrent Clear operations", func(t *testing.T) {
		mapper.Clear()
		mapper.AddVNodePort("vnode-1", "10250")
		mapper.AddVNodePort("vnode-2", "10251")

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				mapper.Clear()
			}()
		}

		wg.Wait()

		assert.Equal(t, 0, mapper.GetVNodeCount())
	})
}

func TestGlobalVNodePortMapper(t *testing.T) {
	t.Run("Global instance exists", func(t *testing.T) {
		assert.NotNil(t, VNodePortMapper)
		assert.NotNil(t, VNodePortMapper.nodes)
	})

	t.Run("Global instance is usable", func(t *testing.T) {
		// Clear any existing state
		VNodePortMapper.Clear()

		VNodePortMapper.AddVNodePort("test-vnode", "10250")
		port, exists := VNodePortMapper.GetPortByVNodeName("test-vnode")
		assert.True(t, exists)
		assert.Equal(t, "10250", port)

		// Cleanup
		VNodePortMapper.Clear()
	})
}

func TestEdgeCases(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	t.Run("Empty string vnode name", func(t *testing.T) {
		mapper.AddVNodePort("", "10250")
		port, exists := mapper.GetPortByVNodeName("")
		assert.True(t, exists)
		assert.Equal(t, "10250", port)
	})

	t.Run("Empty string port", func(t *testing.T) {
		mapper.AddVNodePort("vnode-empty-port", "")
		port, exists := mapper.GetPortByVNodeName("vnode-empty-port")
		assert.True(t, exists)
		assert.Equal(t, "", port)
	})

	t.Run("Special characters in vnode name", func(t *testing.T) {
		specialNames := []string{
			"vnode-with-dashes",
			"vnode.with.dots",
			"vnode_with_underscores",
			"vnode/with/slashes",
			"vnode:with:colons",
		}

		for _, name := range specialNames {
			mapper.AddVNodePort(name, "10250")
			port, exists := mapper.GetPortByVNodeName(name)
			assert.True(t, exists, "Failed for name: %s", name)
			assert.Equal(t, "10250", port)
		}
	})

	t.Run("Very long vnode name", func(t *testing.T) {
		longName := string(make([]byte, 1000))
		for i := range longName {
			longName = longName[:i] + "a"
		}

		mapper.AddVNodePort(longName, "10250")
		port, exists := mapper.GetPortByVNodeName(longName)
		assert.True(t, exists)
		assert.Equal(t, "10250", port)
	})

	t.Run("Non-standard port numbers", func(t *testing.T) {
		testPorts := []string{
			"80",
			"443",
			"65535",
			"1",
			"not-a-number",
			"10250:10251", // port range
		}

		for i, port := range testPorts {
			vnodeName := "vnode-" + string(rune(i))
			mapper.AddVNodePort(vnodeName, port)
			retrievedPort, exists := mapper.GetPortByVNodeName(vnodeName)
			assert.True(t, exists)
			assert.Equal(t, port, retrievedPort)
		}
	})
}

func TestMultipleOverwrites(t *testing.T) {
	mapper := &GlobalVNodePortMapper{
		nodes: make(map[string]string),
	}

	vnodeName := "vnode-overwrite-test"
	ports := []string{"10250", "10251", "10252", "10253", "10254"}

	for _, port := range ports {
		mapper.AddVNodePort(vnodeName, port)
		retrievedPort, exists := mapper.GetPortByVNodeName(vnodeName)
		assert.True(t, exists)
		assert.Equal(t, port, retrievedPort)
	}

	// Count should still be 1
	assert.Equal(t, 1, mapper.GetVNodeCount())

	// Final port should be the last one
	finalPort, exists := mapper.GetPortByVNodeName(vnodeName)
	assert.True(t, exists)
	assert.Equal(t, "10254", finalPort)
}
