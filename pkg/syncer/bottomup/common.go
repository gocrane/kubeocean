package bottomup

import (
	"fmt"
	"time"
)

const (
	// Virtual node name prefix
	VirtualNodePrefix = "vnode"

	// Labels for virtual nodes (defined in api/v1beta1/constants.go)
	// LabelClusterBinding, LabelPhysicalClusterID, LabelPhysicalNodeName, LabelManagedBy

	// Sync intervals
	DefaultNodeSyncInterval   = 300 * time.Second
	DefaultPolicySyncInterval = 300 * time.Second
)

// GenerateVirtualNodeName generates virtual node name from physical node name and cluster binding
// Format: vnode-{cluster-id}-{node-name}
func GenerateVirtualNodeName(clusterID, physicalNodeName string) string {
	return fmt.Sprintf("%s-%s-%s", VirtualNodePrefix, clusterID, physicalNodeName)
}
