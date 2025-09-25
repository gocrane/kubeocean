/*
Copyright 2024 The Kubeocean Authors.

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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateVirtualNodeName(t *testing.T) {
	tests := []struct {
		name         string
		clusterID    string
		physicalName string
		expected     string
		description  string
	}{
		{
			name:         "short name - basic case",
			clusterID:    "cluster1",
			physicalName: "node1",
			expected:     "vnode-cluster1-node1",
			description:  "Basic case with short names",
		},
		{
			name:         "short name - with dashes",
			clusterID:    "test-cluster",
			physicalName: "worker-node-1",
			expected:     "vnode-test-cluster-worker-node-1",
			description:  "Names with dashes should work normally",
		},
		{
			name:         "medium length name",
			clusterID:    "production-cluster-east",
			physicalName: "worker-node-production-001",
			expected:     "vnode-production-cluster-east-worker-node-production-001",
			description:  "Medium length names under 64 characters",
		},
		{
			name:         "exactly 63 characters",
			clusterID:    "test",
			physicalName: "node-12345678901234567890123456789012345678901234567",
			expected:     "vnode-test-node-12345678901234567890123456789012345678901234567",
			description:  "Exactly 63 characters should not be truncated",
		},
		{
			name:         "long name requiring truncation",
			clusterID:    "very-long-cluster-id-that-should-cause-truncation",
			physicalName: "very-long-physical-node-name-that-will-definitely-exceed-sixty-four-characters-limit",
			expected:     "", // Will be calculated in test
			description:  "Long names should be truncated with MD5 hash",
		},
		{
			name:         "empty strings",
			clusterID:    "",
			physicalName: "",
			expected:     "vnode--",
			description:  "Empty strings should work",
		},
		{
			name:         "single character inputs",
			clusterID:    "a",
			physicalName: "b",
			expected:     "vnode-a-b",
			description:  "Single character inputs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateVirtualNodeName(tt.clusterID, tt.physicalName)

			// For the truncation case, calculate expected result
			if tt.name == "long name requiring truncation" {
				originalName := fmt.Sprintf("%s-%s-%s", VirtualNodePrefix, tt.clusterID, tt.physicalName)
				hash := md5.Sum([]byte(originalName))
				hashString := fmt.Sprintf("%x", hash)
				tt.expected = originalName[:TruncatedPrefixLength] + "-" + hashString
			}

			assert.Equal(t, tt.expected, result, tt.description)

			// Validate that result never exceeds 63 characters
			assert.LessOrEqual(t, len(result), 63, "Node name should not exceed 63 characters")

			// Validate that result starts with virtual node prefix
			if tt.clusterID != "" || tt.physicalName != "" {
				assert.True(t, strings.HasPrefix(result, VirtualNodePrefix), "Should start with virtual node prefix")
			}
		})
	}
}

func TestGenerateVirtualNodeName_TruncationLogic(t *testing.T) {
	tests := []struct {
		name         string
		clusterID    string
		physicalName string
		description  string
	}{
		{
			name:         "64 characters exactly - should truncate",
			clusterID:    "test",
			physicalName: "node-123456789012345678901234567890123456789012345678",
			description:  "64 characters should trigger truncation",
		},
		{
			name:         "65 characters - should truncate",
			clusterID:    "test-cluster",
			physicalName: "very-long-node-name-that-exceeds-the-limit-and-should-be-truncated",
			description:  "65+ characters should trigger truncation",
		},
		{
			name:         "extreme length - should truncate",
			clusterID:    strings.Repeat("a", 50),
			physicalName: strings.Repeat("b", 100),
			description:  "Extremely long names should be handled correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateVirtualNodeName(tt.clusterID, tt.physicalName)
			originalName := fmt.Sprintf("%s-%s-%s", VirtualNodePrefix, tt.clusterID, tt.physicalName)

			// Should be exactly 63 characters when truncated
			assert.Equal(t, 63, len(result), "Truncated name should be exactly 63 characters")

			// Should start with the first 30 characters of original name
			expectedPrefix := originalName[:TruncatedPrefixLength]
			assert.True(t, strings.HasPrefix(result, expectedPrefix), "Should start with first 30 characters")

			// Should end with MD5 hash
			hash := md5.Sum([]byte(originalName))
			expectedHash := fmt.Sprintf("%x", hash)
			assert.True(t, strings.HasSuffix(result, expectedHash), "Should end with MD5 hash")

			// Should have exactly one dash between prefix and hash
			expectedResult := expectedPrefix + "-" + expectedHash
			assert.Equal(t, expectedResult, result, "Should match expected truncated format")
		})
	}
}

func TestGenerateVirtualNodeName_MD5Consistency(t *testing.T) {
	clusterID := "long-cluster-id-name"
	physicalName := "very-long-physical-node-name-that-exceeds-limits"

	// Generate the same name multiple times
	results := make([]string, 5)
	for i := 0; i < 5; i++ {
		results[i] = GenerateVirtualNodeName(clusterID, physicalName)
	}

	// All results should be identical
	for i := 1; i < len(results); i++ {
		assert.Equal(t, results[0], results[i], "MD5 hash should be consistent across multiple calls")
	}

	// Result should always be 63 characters for this long input
	assert.Equal(t, 63, len(results[0]), "Should be exactly 63 characters")
}

func TestGenerateVirtualNodeName_UniquenessForDifferentInputs(t *testing.T) {
	testCases := []struct {
		clusterID    string
		physicalName string
	}{
		{"cluster1", "very-long-node-name-that-will-definitely-cause-truncation-and-hashing"},
		{"cluster2", "very-long-node-name-that-will-definitely-cause-truncation-and-hashing"},
		{"cluster1", "very-long-node-name-that-will-definitely-cause-truncation-and-hashing-different"},
		{"different-cluster", "very-long-node-name-that-will-definitely-cause-truncation"},
	}

	results := make([]string, len(testCases))
	for i, tc := range testCases {
		results[i] = GenerateVirtualNodeName(tc.clusterID, tc.physicalName)
	}

	// All results should be unique
	uniqueResults := make(map[string]bool)
	for i, result := range results {
		assert.False(t, uniqueResults[result], "Result %d should be unique: %s", i, result)
		uniqueResults[result] = true
	}
}

func TestGenerateVirtualNodeName_EdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		clusterID    string
		physicalName string
		description  string
	}{
		{
			name:         "cluster ID with special characters",
			clusterID:    "cluster-with_special.chars",
			physicalName: "node1",
			description:  "Special characters in cluster ID",
		},
		{
			name:         "physical name with special characters",
			clusterID:    "cluster1",
			physicalName: "node-with_special.chars",
			description:  "Special characters in physical name",
		},
		{
			name:         "unicode characters",
			clusterID:    "cluster测试",
			physicalName: "node节点",
			description:  "Unicode characters should be handled",
		},
		{
			name:         "numbers only",
			clusterID:    "123456",
			physicalName: "789012",
			description:  "Numeric cluster and node names",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateVirtualNodeName(tt.clusterID, tt.physicalName)

			// Should not panic or return empty string (unless inputs are empty)
			if tt.clusterID != "" || tt.physicalName != "" {
				assert.NotEmpty(t, result, tt.description)
			}

			// Should respect length limit
			assert.LessOrEqual(t, len(result), 63, "Should not exceed 63 characters")

			// Should start with prefix if not empty inputs
			if tt.clusterID != "" || tt.physicalName != "" {
				assert.True(t, strings.HasPrefix(result, VirtualNodePrefix), "Should start with prefix")
			}
		})
	}
}

func TestConstants(t *testing.T) {
	// Test that our constants are reasonable
	assert.Equal(t, "vnode", VirtualNodePrefix, "Virtual node prefix should be 'vnode'")
	assert.Equal(t, 63, MaxNodeNameLength, "Max node name length should be 63")
	assert.Equal(t, 30, TruncatedPrefixLength, "Truncated prefix length should be 30")

	// Test that our truncation math is correct
	// 30 (prefix) + 1 (dash) + 32 (MD5 hash) = 63 characters
	expectedTruncatedLength := TruncatedPrefixLength + 1 + 32
	assert.Equal(t, MaxNodeNameLength, expectedTruncatedLength, "Truncation math should equal max length")
}
