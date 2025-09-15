package v1beta1

const (
	// Labels
	LabelManagedBy      = "kubeocean.io/managed-by"
	LabelManagedByValue = "kubeocean"

	// Pod mapping annotations
	AnnotationVirtualPodNamespace  = "kubeocean.io/virtual-pod-namespace"
	AnnotationVirtualPodName       = "kubeocean.io/virtual-pod-name"
	AnnotationVirtualPodUID        = "kubeocean.io/virtual-pod-uid"
	AnnotationPhysicalPodNamespace = "kubeocean.io/physical-pod-namespace"
	AnnotationPhysicalPodName      = "kubeocean.io/physical-pod-name"
	AnnotationPhysicalPodUID       = "kubeocean.io/physical-pod-uid"
	AnnotationVirtualNodeName      = "kubeocean.io/virtual-node-name"

	// Sync annotations
	AnnotationLastSyncTime     = "kubeocean.io/last-sync-time"
	AnnotationPoliciesApplied  = "kubeocean.io/policies-applied"
	AnnotationExpectedMetadata = "kubeocean.io/expected-metadata"

	// Finalizers
	VirtualPodFinalizer     = "kubeocean.io/virtual-pod"
	VirtualNodeFinalizer    = "kubeocean.io/vnode"
	PolicyFinalizerName     = "policy.kubeocean.io/finalizer"
	SyncedResourceFinalizer = "kubeocean.io/synced-resource"

	// Taints
	TaintVnodeDefaultTaint         = "kubeocean.io/vnode"
	TaintPhysicalNodeUnschedulable = "kubeocean.io/physical-node-unschedulable"
	TaintOutOfTimeWindows          = "kubeocean.io/out-of-time-windows"

	// Node and CSINode labels
	LabelClusterBinding    = "kubeocean.io/cluster-binding"
	LabelPhysicalClusterID = "kubeocean.io/physical-cluster-id"
	LabelPhysicalNodeName  = "kubeocean.io/physical-node-name"
	LabelPhysicalNodeUID   = "kubeocean.io/physical-node-uid"
	LabelValueTrue         = "true"

	// Resource mapping labels
	LabelPhysicalName = "kubeocean.io/physical-name"

	// Resource mapping annotations
	AnnotationPhysicalName      = "kubeocean.io/physical-name"
	AnnotationPhysicalNamespace = "kubeocean.io/physical-namespace"
	AnnotationVirtualName       = "kubeocean.io/virtual-name"
	AnnotationVirtualNamespace  = "kubeocean.io/virtual-namespace"

	// PV-related labels
	LabelUsedByPV = "kubeocean.io/used-by-pv"

	// Cluster-specific labels and finalizers
	LabelManagedByClusterIDPrefix = "kubeocean.io/synced-by-"
	FinalizerClusterIDPrefix      = "kubeocean.io/finalizer-"
)
