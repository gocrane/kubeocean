package bottomup

import (
	"time"
)

const (
	// Labels for virtual nodes (defined in api/v1beta1/constants.go)
	// LabelClusterBinding, LabelPhysicalClusterID, LabelPhysicalNodeName, LabelManagedBy

	// Sync intervals
	DefaultNodeSyncInterval   = 300 * time.Second
	DefaultPolicySyncInterval = 300 * time.Second
)
