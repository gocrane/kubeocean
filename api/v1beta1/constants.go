package v1beta1

const (
	// Labels
	LabelManagedBy      = "tapestry.io/managed-by"
	LabelManagedByValue = "tapestry"

	// Pod mapping annotations
	AnnotationVirtualPodNamespace  = "tapestry.io/virtual-pod-namespace"
	AnnotationVirtualPodName       = "tapestry.io/virtual-pod-name"
	AnnotationVirtualPodUID        = "tapestry.io/virtual-pod-uid"
	AnnotationPhysicalPodNamespace = "tapestry.io/physical-pod-namespace"
	AnnotationPhysicalPodName      = "tapestry.io/physical-pod-name"
	AnnotationPhysicalPodUID       = "tapestry.io/physical-pod-uid"

	// Sync annotations
	AnnotationLastSyncTime     = "tapestry.io/last-sync-time"
	AnnotationPoliciesApplied  = "tapestry.io/policies-applied"
	AnnotationExpectedMetadata = "tapestry.io/expected-metadata"

	// Finalizers
	VirtualPodFinalizer = "tapestry.io/virtual-pod"
	PolicyFinalizerName = "policy.tapestry.io/finalizer"
)
