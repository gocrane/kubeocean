package bottomup

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

const (
	// Annotations for Pod mapping
	AnnotationVirtualPodNamespace = "tapestry.io/virtual-pod-namespace"
	AnnotationVirtualPodName      = "tapestry.io/virtual-pod-name"
	AnnotationPhysicalPodUID      = "tapestry.io/physical-pod-uid"
	AnnotationSyncTimestamp       = "tapestry.io/sync-timestamp"
)

// PhysicalPodReconciler reconciles Pod objects from physical cluster
// This implements requirement 3.4, 3.5 - Pod status synchronization
type PhysicalPodReconciler struct {
	PhysicalClient client.Client
	VirtualClient  client.Client
	Scheme         *runtime.Scheme
	ClusterBinding *cloudv1beta1.ClusterBinding
	Log            logr.Logger
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch

// Reconcile handles Pod events from physical cluster
// This implements requirement 3.4 - monitoring physical cluster Pod status changes
func (r *PhysicalPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalPod", req.NamespacedName)

	// Get the physical pod
	physicalPod := &corev1.Pod{}
	err := r.PhysicalClient.Get(ctx, req.NamespacedName, physicalPod)
	if err != nil {
		if errors.IsNotFound(err) {
			// Pod was deleted, sync deletion to virtual cluster
			logger.Info("Physical pod deleted, syncing to virtual cluster")
			return r.handlePodDeletion(ctx, req.NamespacedName)
		}
		logger.Error(err, "Failed to get physical pod")
		return ctrl.Result{}, err
	}

	// Check if this is a Tapestry-managed pod (has virtual pod mapping)
	if !r.isTapestryManagedPod(physicalPod) {
		// Not a Tapestry pod, ignore
		return ctrl.Result{}, nil
	}

	// Handle deletion
	if physicalPod.DeletionTimestamp != nil {
		return r.handlePodDeletion(ctx, req.NamespacedName)
	}

	// Sync pod status to virtual cluster
	return r.syncPodStatus(ctx, physicalPod)
}

// syncPodStatus syncs physical pod status to corresponding virtual pod
// This implements requirement 3.5 - ensuring Pod status sync idempotency
func (r *PhysicalPodReconciler) syncPodStatus(ctx context.Context, physicalPod *corev1.Pod) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalPod", physicalPod.Namespace+"/"+physicalPod.Name)

	// Extract virtual pod information from annotations
	virtualNamespace, virtualName, err := r.getVirtualPodInfo(physicalPod)
	if err != nil {
		logger.Error(err, "Failed to get virtual pod info from physical pod")
		return ctrl.Result{}, err
	}

	// Get the virtual pod
	virtualPod := &corev1.Pod{}
	virtualPodKey := types.NamespacedName{
		Namespace: virtualNamespace,
		Name:      virtualName,
	}

	err = r.VirtualClient.Get(ctx, virtualPodKey, virtualPod)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Virtual pod not found, physical pod may be orphaned",
				"virtualPod", virtualPodKey)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual pod")
		return ctrl.Result{}, err
	}

	// Check if status sync is needed (idempotency check)
	if !r.needsStatusSync(virtualPod, physicalPod) {
		logger.V(1).Info("Pod status already in sync, skipping update")
		return ctrl.Result{}, nil
	}

	// Update virtual pod status
	err = r.updateVirtualPodStatus(ctx, virtualPod, physicalPod)
	if err != nil {
		logger.Error(err, "Failed to update virtual pod status")
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Successfully synced pod status",
		"virtualPod", virtualPodKey,
		"phase", physicalPod.Status.Phase)

	return ctrl.Result{}, nil
}

// handlePodDeletion handles deletion of physical pod
func (r *PhysicalPodReconciler) handlePodDeletion(ctx context.Context, physicalPodKey types.NamespacedName) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalPod", physicalPodKey)

	// We can't get the virtual pod info from the deleted physical pod
	// In a production system, we might maintain a mapping cache or use finalizers
	// For now, we'll just log the deletion
	logger.Info("Physical pod deleted, virtual pod status may need manual cleanup")

	return ctrl.Result{}, nil
}

// isTapestryManagedPod checks if a pod is managed by Tapestry
func (r *PhysicalPodReconciler) isTapestryManagedPod(pod *corev1.Pod) bool {
	// Check for Tapestry annotations
	if pod.Annotations == nil {
		return false
	}

	_, hasVirtualNamespace := pod.Annotations[AnnotationVirtualPodNamespace]
	_, hasVirtualName := pod.Annotations[AnnotationVirtualPodName]

	return hasVirtualNamespace && hasVirtualName
}

// getVirtualPodInfo extracts virtual pod information from physical pod annotations
func (r *PhysicalPodReconciler) getVirtualPodInfo(physicalPod *corev1.Pod) (namespace, name string, err error) {
	if physicalPod.Annotations == nil {
		return "", "", fmt.Errorf("physical pod has no annotations")
	}

	namespace, hasNamespace := physicalPod.Annotations[AnnotationVirtualPodNamespace]
	name, hasName := physicalPod.Annotations[AnnotationVirtualPodName]

	if !hasNamespace || !hasName {
		return "", "", fmt.Errorf("physical pod missing virtual pod mapping annotations")
	}

	if namespace == "" || name == "" {
		return "", "", fmt.Errorf("virtual pod mapping annotations are empty")
	}

	return namespace, name, nil
}

// needsStatusSync checks if virtual pod status needs to be updated
// This implements requirement 3.5 - idempotent status synchronization
func (r *PhysicalPodReconciler) needsStatusSync(virtualPod, physicalPod *corev1.Pod) bool {
	// Check if phase has changed
	if virtualPod.Status.Phase != physicalPod.Status.Phase {
		return true
	}

	// Check if container statuses have changed
	if len(virtualPod.Status.ContainerStatuses) != len(physicalPod.Status.ContainerStatuses) {
		return true
	}

	// Compare container statuses
	for i, vStatus := range virtualPod.Status.ContainerStatuses {
		if i >= len(physicalPod.Status.ContainerStatuses) {
			return true
		}

		pStatus := physicalPod.Status.ContainerStatuses[i]

		if vStatus.Name != pStatus.Name ||
			vStatus.Ready != pStatus.Ready ||
			vStatus.RestartCount != pStatus.RestartCount {
			return true
		}

		// Compare state
		if !r.containerStatesEqual(vStatus.State, pStatus.State) {
			return true
		}
	}

	// Check if conditions have changed significantly
	if r.hasSignificantConditionChanges(virtualPod.Status.Conditions, physicalPod.Status.Conditions) {
		return true
	}

	return false
}

// containerStatesEqual compares two container states
func (r *PhysicalPodReconciler) containerStatesEqual(state1, state2 corev1.ContainerState) bool {
	// Compare running state
	if (state1.Running != nil) != (state2.Running != nil) {
		return false
	}

	// Compare waiting state
	if (state1.Waiting != nil) != (state2.Waiting != nil) {
		return false
	}
	if state1.Waiting != nil && state2.Waiting != nil {
		if state1.Waiting.Reason != state2.Waiting.Reason {
			return false
		}
	}

	// Compare terminated state
	if (state1.Terminated != nil) != (state2.Terminated != nil) {
		return false
	}
	if state1.Terminated != nil && state2.Terminated != nil {
		if state1.Terminated.ExitCode != state2.Terminated.ExitCode ||
			state1.Terminated.Reason != state2.Terminated.Reason {
			return false
		}
	}

	return true
}

// hasSignificantConditionChanges checks if there are significant changes in pod conditions
func (r *PhysicalPodReconciler) hasSignificantConditionChanges(virtualConditions, physicalConditions []corev1.PodCondition) bool {
	// Create maps for easier comparison
	vCondMap := make(map[corev1.PodConditionType]corev1.PodCondition)
	for _, cond := range virtualConditions {
		vCondMap[cond.Type] = cond
	}

	pCondMap := make(map[corev1.PodConditionType]corev1.PodCondition)
	for _, cond := range physicalConditions {
		pCondMap[cond.Type] = cond
	}

	// Check important condition types
	importantTypes := []corev1.PodConditionType{
		corev1.PodReady,
		corev1.PodInitialized,
		corev1.PodScheduled,
		corev1.ContainersReady,
	}

	for _, condType := range importantTypes {
		vCond, vExists := vCondMap[condType]
		pCond, pExists := pCondMap[condType]

		if vExists != pExists {
			return true
		}

		if vExists && pExists {
			if vCond.Status != pCond.Status || vCond.Reason != pCond.Reason {
				return true
			}
		}
	}

	return false
}

// updateVirtualPodStatus updates the virtual pod status based on physical pod
func (r *PhysicalPodReconciler) updateVirtualPodStatus(ctx context.Context, virtualPod, physicalPod *corev1.Pod) error {
	// Create a copy for update
	updatedPod := virtualPod.DeepCopy()

	// Update status fields
	updatedPod.Status.Phase = physicalPod.Status.Phase
	updatedPod.Status.Conditions = physicalPod.Status.Conditions
	updatedPod.Status.Message = physicalPod.Status.Message
	updatedPod.Status.Reason = physicalPod.Status.Reason
	updatedPod.Status.HostIP = physicalPod.Status.HostIP
	updatedPod.Status.PodIP = physicalPod.Status.PodIP
	updatedPod.Status.PodIPs = physicalPod.Status.PodIPs
	updatedPod.Status.StartTime = physicalPod.Status.StartTime
	updatedPod.Status.ContainerStatuses = physicalPod.Status.ContainerStatuses
	updatedPod.Status.InitContainerStatuses = physicalPod.Status.InitContainerStatuses
	updatedPod.Status.EphemeralContainerStatuses = physicalPod.Status.EphemeralContainerStatuses

	// Add sync timestamp annotation
	if updatedPod.Annotations == nil {
		updatedPod.Annotations = make(map[string]string)
	}
	updatedPod.Annotations[AnnotationSyncTimestamp] = time.Now().Format(time.RFC3339)
	updatedPod.Annotations[AnnotationPhysicalPodUID] = string(physicalPod.UID)

	// First update the annotations (metadata)
	if err := r.VirtualClient.Update(ctx, updatedPod); err != nil {
		return err
	}

	// Then update the status
	return r.VirtualClient.Status().Update(ctx, updatedPod)
}

// SetupWithManager sets up the controller with the Manager
func (r *PhysicalPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Generate unique controller name using cluster binding name
	uniqueControllerName := fmt.Sprintf("pod-%s", r.ClusterBinding.Name)

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named(uniqueControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50, // Higher concurrency for pod status sync
		}).
		Complete(r)
}
