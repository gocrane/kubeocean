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
)

// VirtualPodInfo contains virtual pod information from annotations
type VirtualPodInfo struct {
	VirtualNodeName     string // kubeocean.io/virtual-node-name
	VirtualPodName      string // kubeocean.io/virtual-pod-name
	VirtualPodNamespace string // kubeocean.io/virtual-pod-namespace
}

// GlobalPodMapper is a global sync.Map for pod mapping
// Key: string (physical pod name), Value: *VirtualPodInfo
var GlobalPodMapper *sync.Map

// InitGlobalPodMapper initializes the global pod mapper
func InitGlobalPodMapper() {
	GlobalPodMapper = &sync.Map{}
}

// GetVirtualPodInfo retrieves virtual pod information for a physical pod
func GetVirtualPodInfo(physicalPodName string) (*VirtualPodInfo, bool) {
	if GlobalPodMapper == nil {
		return nil, false
	}

	value, exists := GlobalPodMapper.Load(physicalPodName)
	if !exists {
		return nil, false
	}

	virtualInfo, ok := value.(*VirtualPodInfo)
	return virtualInfo, ok
}

// SetVirtualPodInfo sets virtual pod information for a physical pod
func SetVirtualPodInfo(physicalPodName string, virtualInfo *VirtualPodInfo) {
	if GlobalPodMapper == nil {
		return
	}
	GlobalPodMapper.Store(physicalPodName, virtualInfo)
}

// DeleteVirtualPodInfo removes virtual pod information for a physical pod
func DeleteVirtualPodInfo(physicalPodName string) {
	if GlobalPodMapper == nil {
		return
	}
	GlobalPodMapper.Delete(physicalPodName)
}

// GetPodMappingCount returns the current number of pod mappings
func GetPodMappingCount() int {
	if GlobalPodMapper == nil {
		return 0
	}

	count := 0
	GlobalPodMapper.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}
