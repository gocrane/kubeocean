package bottomup

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
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

	// 1. Get the physical pod, if not exists, do nothing
	physicalPod := &corev1.Pod{}
	err := r.PhysicalClient.Get(ctx, req.NamespacedName, physicalPod)
	if err != nil {
		if errors.IsNotFound(err) {
			// Physical pod not exists, do nothing
			logger.V(1).Info("Physical pod not found, doing nothing")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get physical pod")
		return ctrl.Result{}, err
	}

	// Check if this is a Tapestry-managed pod (has managed-by label)
	if !r.isTapestryManagedPod(physicalPod) {
		// Not a Tapestry pod, ignore
		return ctrl.Result{}, nil
	}

	// ignore deletion
	// 2. Check required annotations on physical pod
	if !r.hasRequiredAnnotations(physicalPod) {
		logger.Info("Physical pod missing required annotations, triggering deletion")
		return r.deletePhysicalPod(ctx, physicalPod)
	}

	// 3 Get corresponding virtual pod and validate
	virtualPod, isVirtualPodMatched, err := r.getAndValidateVirtualPod(ctx, physicalPod)
	if err != nil {
		logger.Error(err, "Failed to get or validate virtual pod, triggering physical pod deletion")
		return ctrl.Result{}, err
	}
	if virtualPod == nil || !isVirtualPodMatched {
		logger.Info("Virtual pod not found or not matched, triggering physical pod deletion")
		return r.deletePhysicalPod(ctx, physicalPod)
	}

	// 4. Check virtual pod annotations point back to current physical pod
	if !r.validateVirtualPodAnnotations(virtualPod, physicalPod) {
		logger.Info("Virtual pod annotations don't point back to current physical pod, triggering deletion")
		return r.deletePhysicalPod(ctx, physicalPod)
	}

	// 5. All checks passed, sync physical pod to virtual pod
	return r.syncPhysicalPodToVirtual(ctx, physicalPod, virtualPod)
}

// isTapestryManagedPod checks if a pod is managed by Tapestry
func (r *PhysicalPodReconciler) isTapestryManagedPod(pod *corev1.Pod) bool {
	// Check for Tapestry managed-by label
	if pod.Labels == nil {
		return false
	}

	managedBy, exists := pod.Labels[cloudv1beta1.LabelManagedBy]
	return exists && managedBy == cloudv1beta1.LabelManagedByValue
}

// hasRequiredAnnotations checks if physical pod has all required annotations
func (r *PhysicalPodReconciler) hasRequiredAnnotations(physicalPod *corev1.Pod) bool {
	if physicalPod.Annotations == nil {
		return false
	}

	requiredAnnotations := []string{
		cloudv1beta1.AnnotationVirtualPodNamespace,
		cloudv1beta1.AnnotationVirtualPodName,
		cloudv1beta1.AnnotationVirtualPodUID,
	}

	for _, annotation := range requiredAnnotations {
		if value, exists := physicalPod.Annotations[annotation]; !exists || value == "" {
			return false
		}
	}

	return true
}

// getAndValidateVirtualPod gets virtual pod and validates its existence and UID
// return: virtual pod, is virtual pod matched, error
func (r *PhysicalPodReconciler) getAndValidateVirtualPod(ctx context.Context, physicalPod *corev1.Pod) (*corev1.Pod, bool, error) {
	// Extract virtual pod information from annotations
	virtualNamespace := physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]
	virtualName := physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodName]
	expectedUID := physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodUID]
	logger := r.Log.WithValues("physicalPod", physicalPod.Namespace+"/"+physicalPod.Name, "virtualPod", virtualNamespace+"/"+virtualName, "expectedUID", expectedUID)

	// Get the virtual pod
	virtualPod := &corev1.Pod{}
	virtualPodKey := types.NamespacedName{
		Namespace: virtualNamespace,
		Name:      virtualName,
	}

	err := r.VirtualClient.Get(ctx, virtualPodKey, virtualPod)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Virtual pod not found, triggering deletion")
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("failed to get virtual pod: %w", err)
	}

	// Check if UID matches
	if string(virtualPod.UID) != expectedUID {
		logger.Info("Virtual pod UID mismatch, triggering deletion", "virtualPodUID", string(virtualPod.UID), "expectedUID", expectedUID)
		return nil, false, nil
	}

	return virtualPod, true, nil
}

// validateVirtualPodAnnotations checks if virtual pod annotations point back to current physical pod
func (r *PhysicalPodReconciler) validateVirtualPodAnnotations(virtualPod, physicalPod *corev1.Pod) bool {
	if virtualPod.Annotations == nil {
		return false
	}

	expectedNamespace := physicalPod.Namespace
	expectedName := physicalPod.Name

	actualNamespace, hasNamespace := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	actualName, hasName := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]

	return hasNamespace && hasName && actualNamespace == expectedNamespace && actualName == expectedName
}

// deletePhysicalPod deletes the physical pod
func (r *PhysicalPodReconciler) deletePhysicalPod(ctx context.Context, physicalPod *corev1.Pod) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalPod", physicalPod.Namespace+"/"+physicalPod.Name)

	if physicalPod.DeletionTimestamp != nil {
		logger.Info("Physical pod is being deleted, skipping deletion")
		return ctrl.Result{}, nil
	}

	deleteOpt := &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &physicalPod.UID},
	}
	err := r.PhysicalClient.Delete(ctx, physicalPod, deleteOpt)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to delete physical pod")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully triggered physical pod deletion")
	return ctrl.Result{}, nil
}

// syncPhysicalPodToVirtual syncs physical pod annotations, labels, and status to virtual pod
func (r *PhysicalPodReconciler) syncPhysicalPodToVirtual(ctx context.Context, physicalPod, virtualPod *corev1.Pod) (ctrl.Result, error) {
	logger := r.Log.WithValues("physicalPod", physicalPod.Namespace+"/"+physicalPod.Name, "virtualPod", virtualPod.Namespace+"/"+virtualPod.Name)

	// 1. Build syncPod object based on current physical pod and virtual pod
	syncPod := r.buildSyncPod(physicalPod, virtualPod)

	// 2. Compare syncPod with virtual pod using reflect.DeepEqual
	if r.isPodsStatusEqual(syncPod, virtualPod) {
		logger.V(1).Info("Pod already in sync, skipping update")
		return ctrl.Result{}, nil
	}

	// 3. Update virtual pod if not equal
	syncPod.Annotations[cloudv1beta1.AnnotationLastSyncTime] = time.Now().Format(time.RFC3339)
	err := r.VirtualClient.Status().Update(ctx, syncPod)
	if err != nil {
		logger.Error(err, "Failed to update virtual pod")
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Successfully synced physical pod to virtual pod",
		"status", syncPod.Status)

	return ctrl.Result{}, nil
}

// buildSyncPod builds the expected virtual pod state based on physical pod and current virtual pod
func (r *PhysicalPodReconciler) buildSyncPod(physicalPod, virtualPod *corev1.Pod) *corev1.Pod {
	// Start with a deep copy of the virtual pod to preserve its structure
	syncPod := virtualPod.DeepCopy()

	// Replace labels
	syncPod.Labels = make(map[string]string)
	// Copy labels from physical pod
	for k, v := range physicalPod.Labels {
		syncPod.Labels[k] = v
	}

	// Replace annotations (excluding Tapestry internal annotations)
	syncPod.Annotations = make(map[string]string)
	// Copy annotations from physical pod (excluding Tapestry internal ones)
	for k, v := range physicalPod.Annotations {
		if k != cloudv1beta1.AnnotationVirtualPodNamespace && k != cloudv1beta1.AnnotationVirtualPodName && k != cloudv1beta1.AnnotationVirtualPodUID &&
			k != cloudv1beta1.AnnotationPhysicalPodNamespace && k != cloudv1beta1.AnnotationPhysicalPodName && k != cloudv1beta1.AnnotationPhysicalPodUID {
			syncPod.Annotations[k] = v
		}
	}
	syncPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace] = virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	syncPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName] = virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]
	syncPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID] = virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID]
	if syncPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID] == "" {
		syncPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID] = string(physicalPod.UID)
	}
	syncPod.Annotations[cloudv1beta1.AnnotationLastSyncTime] = virtualPod.Annotations[cloudv1beta1.AnnotationLastSyncTime]

	// Update status fields from physical pod
	syncPod.Status.Phase = physicalPod.Status.Phase
	syncPod.Status.Conditions = physicalPod.Status.Conditions
	syncPod.Status.Message = physicalPod.Status.Message
	syncPod.Status.Reason = physicalPod.Status.Reason
	syncPod.Status.HostIP = physicalPod.Status.PodIP
	syncPod.Status.PodIP = physicalPod.Status.PodIP
	syncPod.Status.PodIPs = physicalPod.Status.PodIPs
	syncPod.Status.StartTime = physicalPod.Status.StartTime
	syncPod.Status.ContainerStatuses = physicalPod.Status.ContainerStatuses
	syncPod.Status.InitContainerStatuses = physicalPod.Status.InitContainerStatuses
	syncPod.Status.EphemeralContainerStatuses = physicalPod.Status.EphemeralContainerStatuses

	return syncPod
}

// isPodsEqual compares two pods using reflect.DeepEqual for status, annotations, and labels
func (r *PhysicalPodReconciler) isPodsStatusEqual(pod1, pod2 *corev1.Pod) bool {
	// Compare status
	if !reflect.DeepEqual(pod1.Status, pod2.Status) {
		return false
	}

	// Compare annotations
	if !reflect.DeepEqual(pod1.Annotations, pod2.Annotations) {
		return false
	}

	// Compare labels
	if !reflect.DeepEqual(pod1.Labels, pod2.Labels) {
		return false
	}

	return true
}

// SetupWithManager sets up the controller with the Manager
func (r *PhysicalPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Generate unique controller name using cluster binding name
	uniqueControllerName := fmt.Sprintf("pod-%s", r.ClusterBinding.Name)

	// Create predicate to only watch Tapestry-managed pods
	tapestryPodPredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return false
		}
		return r.isTapestryManagedPod(pod)
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named(uniqueControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100, // Higher concurrency for pod status sync
		}).
		WithEventFilter(tapestryPodPredicate).
		Complete(r)
}
