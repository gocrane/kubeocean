/*
Copyright 2024.

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
	"crypto/md5"
	"fmt"
)

const (
	// Virtual node name prefix
	VirtualNodePrefix = "vnode"

	// Maximum node name length in Kubernetes
	MaxNodeNameLength = 63

	// Length of name prefix when truncating
	TruncatedPrefixLength = 30
)

// GenerateVirtualNodeName generates virtual node name from physical node name and cluster binding
// 1. First try format: vnode-{cluster-id}-{node-name}
// 2. If length < 64, return it
// 3. If length >= 64, return first 30 characters + "-" + md5(original name)
func GenerateVirtualNodeName(clusterID, physicalNodeName string) string {
	// Step 1: Generate name with standard format
	originalName := fmt.Sprintf("%s-%s-%s", VirtualNodePrefix, clusterID, physicalNodeName)

	// Step 2: If length is acceptable, return it
	if len(originalName) < 64 {
		return originalName
	}

	// Step 3: Truncate and append MD5 hash
	// Calculate MD5 hash of the original name
	hash := md5.Sum([]byte(originalName))
	hashString := fmt.Sprintf("%x", hash)

	// Take first 30 characters and append hash
	truncatedName := originalName[:TruncatedPrefixLength] + "-" + hashString

	return truncatedName
}
