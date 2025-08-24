package topdown

import (
	"context"
	"crypto/md5"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

// VirtualPodReconciler reconciles Pod objects from virtual cluster
// This implements requirement 5.1, 5.2, 5.4, 5.5 - Top-down Pod synchronization
type VirtualPodReconciler struct {
	VirtualClient     client.Client
	PhysicalClient    client.Client
	PhysicalK8sClient kubernetes.Interface // Direct k8s client for bypassing cache
	Scheme            *runtime.Scheme
	ClusterBinding    *cloudv1beta1.ClusterBinding
	Log               logr.Logger
	workQueue         workqueue.TypedRateLimitingInterface[reconcile.Request]
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch

// Reconcile implements the main reconciliation logic for virtual pods
func (r *VirtualPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", req.NamespacedName)

	// 1. Get the virtual pod, if not exists, do nothing
	virtualPod := &corev1.Pod{}
	err := r.VirtualClient.Get(ctx, req.NamespacedName, virtualPod)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Virtual pod not found, doing nothing")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual pod")
		return ctrl.Result{}, err
	}

	// 2. Check if pod is scheduled on a tapestry-managed virtual node
	if virtualPod.Spec.NodeName == "" {
		logger.V(1).Info("Virtual pod not scheduled yet, doing nothing")
		return ctrl.Result{}, nil
	}

	// 3. Check if the virtual node belongs to current cluster binding
	shouldManage, physicalNodeName, err := r.shouldManageVirtualPod(ctx, virtualPod)
	if err != nil {
		logger.Error(err, "Failed to check if virtual pod should be managed")
		return ctrl.Result{}, err
	}
	if !shouldManage {
		logger.V(1).Info("Virtual pod not managed by this cluster binding, doing nothing",
			"virtualNodeName", virtualPod.Spec.NodeName)
		return ctrl.Result{}, nil
	}

	// 4. Check if virtual pod is being deleted
	if virtualPod.DeletionTimestamp != nil {
		logger.Info("Virtual pod is being deleted, handling deletion")
		return r.handleVirtualPodDeletion(ctx, virtualPod)
	}

	// 5. Check if physical pod exists
	physicalPodExists, physicalPod, err := r.checkPhysicalPodExists(ctx, virtualPod)
	if err != nil {
		logger.Error(err, "Failed to check physical pod existence")
		return ctrl.Result{}, err
	}

	// 6. If physical pod doesn't exist, enter creation flow
	if !physicalPodExists {
		logger.Info("Physical pod doesn't exist, entering creation flow")
		return r.handlePhysicalPodCreation(ctx, virtualPod, physicalNodeName)
	}

	// 7. Physical pod exists, check if UID annotation needs to be updated
	currentPhysicalUID := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID]
	actualPhysicalUID := string(physicalPod.UID)

	if currentPhysicalUID == "" {
		logger.Info("Virtual pod missing physical pod UID, updating annotation",
			"physicalPod", fmt.Sprintf("%s/%s", physicalPod.Namespace, physicalPod.Name),
			"physicalUID", actualPhysicalUID)
		return r.updateVirtualPodWithPhysicalUID(ctx, virtualPod, actualPhysicalUID)
	}

	// Physical pod exists and UID is already set, no action needed (handled by bottom-up syncer)
	logger.V(1).Info("Physical pod exists, no action needed",
		"physicalPod", fmt.Sprintf("%s/%s", physicalPod.Namespace, physicalPod.Name))
	return ctrl.Result{}, nil
}

// handleVirtualPodDeletion handles the deletion of virtual pod
func (r *VirtualPodReconciler) handleVirtualPodDeletion(ctx context.Context, virtualPod *corev1.Pod) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// Get physical pod reference from annotations
	physicalNamespace := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	physicalName := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]

	if physicalNamespace == "" || physicalName == "" {
		logger.Info("No physical pod mapping found, allowing virtual pod deletion")
		return r.forceDeleteVirtualPod(ctx, virtualPod)
	}

	// Check if physical pod exists using fallback method
	physicalPod, err := r.getPhysicalPodWithFallback(ctx, physicalNamespace, physicalName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Physical pod not found, allowing virtual pod deletion")
			return r.forceDeleteVirtualPod(ctx, virtualPod)
		}
		logger.Error(err, "Failed to get physical pod during deletion")
		return ctrl.Result{}, err
	}

	// If physical pod is not owned by virtual pod, force delete virtual pod
	if !r.isPhysicalPodOwnedByVirtualPod(physicalPod, virtualPod) {
		logger.Info("Physical pod is not owned by virtual pod, allowing virtual pod deletion")
		return r.forceDeleteVirtualPod(ctx, virtualPod)
	}

	// Physical pod exists, delete it
	logger.Info("Deleting physical pod", "physicalPod", fmt.Sprintf("%s/%s", physicalNamespace, physicalName))
	if physicalPod.DeletionTimestamp != nil {
		logger.Info("Physical pod is being deleted, waiting for deletion to complete")
		return ctrl.Result{}, nil
	}
	err = r.PhysicalClient.Delete(ctx, physicalPod)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "Failed to delete physical pod")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully triggered virtual and physical pod deletion")
	return ctrl.Result{}, nil
}

// forceDeleteVirtualPod forces deletion of virtual pod using immediate deletion with UID precondition
func (r *VirtualPodReconciler) forceDeleteVirtualPod(ctx context.Context, virtualPod *corev1.Pod) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// Use immediate deletion with GracePeriodSeconds=0 and UID precondition to avoid accidental deletion
	gracePeriodSeconds := int64(0)
	deleteOptions := &client.DeleteOptions{
		GracePeriodSeconds: &gracePeriodSeconds,
		Preconditions: &metav1.Preconditions{
			UID: &virtualPod.UID,
		},
	}

	err := r.VirtualClient.Delete(ctx, virtualPod, deleteOptions)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Pod is already deleted, that's fine
			logger.Info("Virtual pod already deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to force delete virtual pod")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully force deleted virtual pod")
	return ctrl.Result{}, nil
}

// shouldManageVirtualPod checks if the virtual pod should be managed by this cluster binding
func (r *VirtualPodReconciler) shouldManageVirtualPod(ctx context.Context, virtualPod *corev1.Pod) (bool, string, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	virtualNodeName := virtualPod.Spec.NodeName
	if virtualNodeName == "" {
		return false, "", nil
	}

	// Get the virtual node
	virtualNode := &corev1.Node{}
	err := r.VirtualClient.Get(ctx, types.NamespacedName{Name: virtualNodeName}, virtualNode)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Virtual node not found", "virtualNodeName", virtualNodeName)
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to get virtual node %s: %w", virtualNodeName, err)
	}

	// Check if it's a tapestry-managed virtual node
	managedBy := virtualNode.Labels[cloudv1beta1.LabelManagedBy]
	if managedBy != "tapestry" {
		logger.V(1).Info("Virtual node not managed by tapestry", "virtualNodeName", virtualNodeName, "managedBy", managedBy)
		return false, "", nil
	}

	// Check if the virtual node belongs to current cluster binding
	// Check by physical-cluster-name annotation (if exists) or physical-cluster-id label
	physicalClusterName := virtualNode.Annotations["tapestry.io/physical-cluster-name"]
	physicalClusterID := virtualNode.Labels[cloudv1beta1.LabelPhysicalClusterID]

	currentClusterName := r.ClusterBinding.Name
	currentClusterID := r.ClusterBinding.Spec.ClusterID

	// Check cluster name match (preferred)
	if physicalClusterName != "" && physicalClusterName != currentClusterName {
		logger.V(1).Info("Virtual node belongs to different cluster",
			"virtualNodeName", virtualNodeName,
			"nodeClusterName", physicalClusterName,
			"currentClusterName", currentClusterName)
		return false, "", nil
	}

	// Check cluster ID match (fallback)
	if physicalClusterName == "" && physicalClusterID != "" && physicalClusterID != currentClusterID {
		logger.V(1).Info("Virtual node belongs to different cluster",
			"virtualNodeName", virtualNodeName,
			"nodeClusterID", physicalClusterID,
			"currentClusterID", currentClusterID)
		return false, "", nil
	}

	// Get physical node name from virtual node label
	physicalNodeName := virtualNode.Labels[cloudv1beta1.LabelPhysicalNodeName]
	if physicalNodeName == "" {
		return false, "", fmt.Errorf("virtual node %s missing physical node name label", virtualNodeName)
	}

	logger.V(1).Info("Virtual pod should be managed",
		"virtualNodeName", virtualNodeName,
		"physicalNodeName", physicalNodeName)
	return true, physicalNodeName, nil
}

// checkPhysicalPodExists checks if the corresponding physical pod exists
func (r *VirtualPodReconciler) checkPhysicalPodExists(ctx context.Context, virtualPod *corev1.Pod) (bool, *corev1.Pod, error) {
	physicalNamespace := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	physicalName := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]

	// If no mapping annotations, physical pod doesn't exist
	if physicalNamespace == "" || physicalName == "" {
		return false, nil, nil
	}

	// Query physical cluster using fallback method to handle cache delays
	physicalPod, err := r.getPhysicalPodWithFallback(ctx, physicalNamespace, physicalName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}

	return true, physicalPod, nil
}

// handlePhysicalPodCreation handles the creation flow for physical pod
func (r *VirtualPodReconciler) handlePhysicalPodCreation(ctx context.Context, virtualPod *corev1.Pod, physicalNodeName string) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	physicalNamespace := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	physicalName := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]
	physicalUID := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID]

	// 1. Check if all mapping annotations are present (indicating previous creation attempt)
	if physicalNamespace != "" && physicalName != "" && physicalUID != "" {
		logger.Info("Physical pod mapping exists but pod not found, setting virtual pod status to Failed")
		return r.setVirtualPodFailed(ctx, virtualPod, "Physical pod was deleted unexpectedly")
	}

	// 2. If cloudv1beta1.AnnotationPhysicalPodUID is empty and cloudv1beta1.AnnotationPhysicalPodName is empty, generate mapping
	if physicalUID == "" && physicalName == "" {
		logger.Info("Generating physical pod name mapping")
		return r.generatePhysicalPodMapping(ctx, virtualPod)
	}

	// 3. If cloudv1beta1.AnnotationPhysicalPodUID is empty but other annotations exist, create physical pod
	if physicalUID == "" && physicalName != "" && physicalNamespace != "" {
		logger.Info("Creating physical pod", "physicalPod", fmt.Sprintf("%s/%s", physicalNamespace, physicalName))
		return r.createPhysicalPod(ctx, virtualPod, physicalNodeName)
	}

	// Should not reach here
	logger.Info("Unexpected state in creation flow, not requeue", "physicalNamespace", physicalNamespace, "physicalName", physicalName, "physicalUID", physicalUID)
	return ctrl.Result{}, nil
}

// generatePhysicalPodMapping generates name mapping for physical pod using MD5 hash
func (r *VirtualPodReconciler) generatePhysicalPodMapping(ctx context.Context, virtualPod *corev1.Pod) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// 1. Validate ClusterBinding MountNamespace
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace
	if physicalNamespace == "" {
		logger.Error(nil, "ClusterBinding MountNamespace is empty, cannot create physical pod")
		return ctrl.Result{}, fmt.Errorf("ClusterBinding MountNamespace is empty")
	}

	// 2. Generate physical pod name using new format: podName(前31字符)-md5(podNamespace+"/"+podName)
	physicalName := r.generatePhysicalPodName(virtualPod.Name, virtualPod.Namespace)

	// 3. Check if physical pod with this name already exists using fallback method
	existingPod, err := r.getPhysicalPodWithFallback(ctx, physicalNamespace, physicalName)
	if err == nil {
		// Physical pod exists, check if it belongs to current virtual pod
		if r.isPhysicalPodOwnedByVirtualPod(existingPod, virtualPod) {
			// This physical pod belongs to current virtual pod, record the mapping
			logger.Info("Found existing physical pod that belongs to current virtual pod", "physicalPod", fmt.Sprintf("%s/%s", physicalNamespace, physicalName))
			return r.recordPhysicalPodMapping(ctx, virtualPod, existingPod)
		} else {
			// Physical pod exists but belongs to different virtual pod - name conflict
			logger.Error(nil, "Physical pod name conflict: pod exists but belongs to different virtual pod", "physicalPod", fmt.Sprintf("%s/%s", physicalNamespace, physicalName))
			return ctrl.Result{}, fmt.Errorf("physical pod name conflict")
		}
	} else if !apierrors.IsNotFound(err) {
		// Other error occurred while checking
		logger.Error(err, "Failed to check if physical pod exists")
		return ctrl.Result{}, err
	}

	// Physical pod doesn't exist, update virtual pod with mapping annotations
	updatedPod := virtualPod.DeepCopy()
	if updatedPod.Annotations == nil {
		updatedPod.Annotations = make(map[string]string)
	}
	updatedPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace] = physicalNamespace
	updatedPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName] = physicalName

	if err := r.VirtualClient.Status().Update(ctx, updatedPod); err != nil {
		logger.Error(err, "Failed to update virtual pod with physical pod mapping")
		return ctrl.Result{}, err
	}

	// not requeue, wait for next reconcile
	logger.Info("Generated physical pod mapping", "physicalPod", fmt.Sprintf("%s/%s", physicalNamespace, physicalName))
	return ctrl.Result{}, nil
}

// createPhysicalPod creates the physical pod based on virtual pod spec
func (r *VirtualPodReconciler) createPhysicalPod(ctx context.Context, virtualPod *corev1.Pod, physicalNodeName string) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	physicalNamespace := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	physicalName := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]

	// 1. Sync dependent resources (ConfigMaps, Secrets, PVCs)
	resourceMapping, err := r.syncDependentResources(ctx, virtualPod)
	if err != nil {
		logger.Error(err, "Failed to sync dependent resources")
		return ctrl.Result{}, err
	}

	// 2. Build physical pod spec
	podSpec, err := r.buildPhysicalPodSpec(virtualPod, physicalNodeName, resourceMapping)
	if err != nil {
		logger.Error(err, "Failed to build physical pod spec")
		return ctrl.Result{}, err
	}

	physicalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        physicalName,
			Namespace:   physicalNamespace,
			Labels:      r.buildPhysicalPodLabels(virtualPod),
			Annotations: r.buildPhysicalPodAnnotations(virtualPod),
		},
		Spec: podSpec,
	}

	// 3. Create physical pod
	err = r.PhysicalClient.Create(ctx, physicalPod)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.Info("Physical pod already exists, updating virtual pod with UID")
			// Get the existing physical pod to retrieve its UID using fallback method
			_, getErr := r.getPhysicalPodWithFallback(ctx, physicalNamespace, physicalName)
			if getErr != nil {
				logger.Error(getErr, "Failed to get existing physical pod")
				return ctrl.Result{}, getErr
			}
			// skip updating virtual pod with physical pod UID, it will be updated by physical pod controller
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to create physical pod")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully created physical pod",
		"physicalPod", fmt.Sprintf("%s/%s", physicalNamespace, physicalName),
		"physicalUID", string(physicalPod.UID))

	// skip updating virtual pod with physical pod UID, it will be updated by physical pod controller
	return ctrl.Result{}, nil
}

// updateVirtualPodWithPhysicalUID updates virtual pod with physical pod UID
func (r *VirtualPodReconciler) updateVirtualPodWithPhysicalUID(ctx context.Context, virtualPod *corev1.Pod, physicalUID string) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	updatedPod := virtualPod.DeepCopy()
	if updatedPod.Annotations == nil {
		updatedPod.Annotations = make(map[string]string)
	}
	updatedPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID] = physicalUID

	err := r.VirtualClient.Status().Update(ctx, updatedPod)
	if err != nil {
		logger.Error(err, "Failed to update virtual pod with physical pod UID")
		return ctrl.Result{}, err
	}

	logger.Info("Updated virtual pod with physical pod UID", "physicalUID", physicalUID)
	return ctrl.Result{}, nil
}

// setVirtualPodFailed sets virtual pod status to Failed
func (r *VirtualPodReconciler) setVirtualPodFailed(ctx context.Context, virtualPod *corev1.Pod, reason string) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	updatedPod := virtualPod.DeepCopy()
	updatedPod.Status.Phase = corev1.PodFailed
	updatedPod.Status.Reason = "PhysicalPodLost"
	updatedPod.Status.Message = reason

	err := r.VirtualClient.Status().Update(ctx, updatedPod)
	if err != nil {
		logger.Error(err, "Failed to update virtual pod status to Failed")
		return ctrl.Result{}, err
	}

	logger.Info("Set virtual pod status to Failed", "reason", reason)
	return ctrl.Result{}, nil
}

// buildPhysicalPodLabels builds labels for physical pod
func (r *VirtualPodReconciler) buildPhysicalPodLabels(virtualPod *corev1.Pod) map[string]string {
	labels := make(map[string]string)

	// Copy all labels from virtual pod
	for k, v := range virtualPod.Labels {
		labels[k] = v
	}

	// Add Tapestry managed-by label
	labels[cloudv1beta1.LabelManagedBy] = cloudv1beta1.LabelManagedByValue

	return labels
}

// buildPhysicalPodAnnotations builds annotations for physical pod
func (r *VirtualPodReconciler) buildPhysicalPodAnnotations(virtualPod *corev1.Pod) map[string]string {
	annotations := make(map[string]string)

	// Copy all annotations from virtual pod (excluding Tapestry internal ones)
	for k, v := range virtualPod.Annotations {
		if k != cloudv1beta1.AnnotationPhysicalPodNamespace &&
			k != cloudv1beta1.AnnotationPhysicalPodName &&
			k != cloudv1beta1.AnnotationPhysicalPodUID &&
			k != cloudv1beta1.AnnotationLastSyncTime {
			annotations[k] = v
		}
	}

	// Add virtual pod mapping annotations
	annotations[cloudv1beta1.AnnotationVirtualPodNamespace] = virtualPod.Namespace
	annotations[cloudv1beta1.AnnotationVirtualPodName] = virtualPod.Name
	annotations[cloudv1beta1.AnnotationVirtualPodUID] = string(virtualPod.UID)

	return annotations
}

// buildPhysicalPodSpec builds spec for physical pod based on virtual pod with resource name mapping
func (r *VirtualPodReconciler) buildPhysicalPodSpec(virtualPod *corev1.Pod, physicalNodeName string, resourceMapping *ResourceMapping) (corev1.PodSpec, error) {
	// Deep copy the spec to avoid modifying the original
	spec := *virtualPod.Spec.DeepCopy()

	// Set the physical node name for scheduling
	spec.NodeName = physicalNodeName

	// Replace resource names in volumes
	for i := range spec.Volumes {
		volume := &spec.Volumes[i]

		// Replace ConfigMap names
		if volume.ConfigMap != nil {
			if physicalName, exists := resourceMapping.ConfigMaps[volume.ConfigMap.Name]; exists {
				volume.ConfigMap.Name = physicalName
			} else {
				return spec, fmt.Errorf("configMap mapping not found for virtual ConfigMap: %s", volume.ConfigMap.Name)
			}
		}

		// Replace Secret names
		if volume.Secret != nil {
			if physicalName, exists := resourceMapping.Secrets[volume.Secret.SecretName]; exists {
				volume.Secret.SecretName = physicalName
			} else {
				return spec, fmt.Errorf("secret mapping not found for virtual Secret: %s", volume.Secret.SecretName)
			}
		}

		// Replace PVC names
		if volume.PersistentVolumeClaim != nil {
			if physicalName, exists := resourceMapping.PVCs[volume.PersistentVolumeClaim.ClaimName]; exists {
				volume.PersistentVolumeClaim.ClaimName = physicalName
			} else {
				return spec, fmt.Errorf("PVC mapping not found for virtual PVC: %s", volume.PersistentVolumeClaim.ClaimName)
			}
		}
	}

	// Replace resource names in containers
	for i := range spec.Containers {
		container := &spec.Containers[i]
		if err := r.replaceContainerResourceNames(container, resourceMapping); err != nil {
			return spec, err
		}
	}

	// Replace resource names in init containers
	for i := range spec.InitContainers {
		container := &spec.InitContainers[i]
		if err := r.replaceContainerResourceNames(container, resourceMapping); err != nil {
			return spec, err
		}
	}

	// Replace image pull secret names
	for i := range spec.ImagePullSecrets {
		imagePullSecret := &spec.ImagePullSecrets[i]
		if physicalName, exists := resourceMapping.Secrets[imagePullSecret.Name]; exists {
			imagePullSecret.Name = physicalName
		} else {
			return spec, fmt.Errorf("secret mapping not found for image pull secret: %s", imagePullSecret.Name)
		}
	}

	return spec, nil
}

// replaceContainerResourceNames replaces resource names in container environment variables
func (r *VirtualPodReconciler) replaceContainerResourceNames(container *corev1.Container, resourceMapping *ResourceMapping) error {
	for j := range container.Env {
		envVar := &container.Env[j]
		if envVar.ValueFrom != nil {
			// Replace ConfigMap names
			if envVar.ValueFrom.ConfigMapKeyRef != nil {
				if physicalName, exists := resourceMapping.ConfigMaps[envVar.ValueFrom.ConfigMapKeyRef.Name]; exists {
					envVar.ValueFrom.ConfigMapKeyRef.Name = physicalName
				} else {
					return fmt.Errorf("configMap mapping not found for virtual ConfigMap: %s", envVar.ValueFrom.ConfigMapKeyRef.Name)
				}
			}

			// Replace Secret names
			if envVar.ValueFrom.SecretKeyRef != nil {
				if physicalName, exists := resourceMapping.Secrets[envVar.ValueFrom.SecretKeyRef.Name]; exists {
					envVar.ValueFrom.SecretKeyRef.Name = physicalName
				} else {
					return fmt.Errorf("secret mapping not found for virtual Secret: %s", envVar.ValueFrom.SecretKeyRef.Name)
				}
			}
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualPodReconciler) SetupWithManager(virtualManager, physicalManager ctrl.Manager) error {

	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("virtualpod-%s", r.ClusterBinding.Name)

	// Setup physical pod informer for watching physical pod changes
	podInformer, err := physicalManager.GetCache().GetInformer(context.TODO(), &corev1.Pod{})
	if err != nil {
		return fmt.Errorf("failed to get pod informer: %w", err)
	}

	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		// 暂时只关心 delete event，确保删除时，virtual pod 也能被删除
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			r.handlePhysicalPodEvent(pod, "DELETE")
		},
	})

	return ctrl.NewControllerManagedBy(virtualManager).
		For(&corev1.Pod{}).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100,
			RateLimiter:             workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 5*time.Minute),
			NewQueue: func(controllerName string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
				wq := workqueue.NewTypedRateLimitingQueueWithConfig(rateLimiter, workqueue.TypedRateLimitingQueueConfig[reconcile.Request]{
					Name: controllerName,
				})
				r.workQueue = wq
				return wq
			},
		}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			pod := obj.(*corev1.Pod)

			// Skip system pods
			if isSystemPod(pod) {
				return false
			}

			// Only sync pods that are not managed by DaemonSet
			if isDaemonSetPod(pod) {
				return false
			}

			// Only sync pods with spec.nodeName set (scheduled pods)
			if pod.Spec.NodeName == "" {
				return false
			}

			return true
		})).
		Complete(r)
}

// handlePhysicalPodEvent handles physical pod create/update/delete events
// and enqueues the corresponding virtual pod for reconciliation
func (r *VirtualPodReconciler) handlePhysicalPodEvent(pod *corev1.Pod, eventType string) {
	// Filter physical pods: only care about pods with tapestry.io/managed-by=tapestry label
	if pod.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		return
	}

	// Filter physical pods: only care about pods with virtual pod annotations
	virtualPodNamespace := pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]
	virtualPodName := pod.Annotations[cloudv1beta1.AnnotationVirtualPodName]
	virtualPodUID := pod.Annotations[cloudv1beta1.AnnotationVirtualPodUID]

	if virtualPodNamespace == "" || virtualPodName == "" || virtualPodUID == "" {
		return
	}

	// Log the event for debugging
	r.Log.Info("Physical pod event received",
		"eventType", eventType,
		"physicalPod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name),
		"virtualPod", fmt.Sprintf("%s/%s", virtualPodNamespace, virtualPodName),
		"virtualPodUID", virtualPodUID,
	)

	// Enqueue the corresponding virtual pod for reconciliation
	virtualPodRequest := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: virtualPodNamespace,
			Name:      virtualPodName,
		},
	}

	if r.workQueue != nil {
		r.workQueue.Add(virtualPodRequest)
		r.Log.V(1).Info("Enqueued virtual pod for reconciliation due to physical pod event",
			"virtualPod", virtualPodRequest.NamespacedName,
			"physicalPod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name),
			"eventType", eventType,
		)
	} else {
		r.Log.Error(nil, "Work queue is nil, cannot enqueue virtual pod",
			"virtualPod", virtualPodRequest.NamespacedName,
			"eventType", eventType,
		)
	}
}

// isSystemPod checks if a pod is a system pod that should be ignored
func isSystemPod(pod *corev1.Pod) bool {
	// Skip system namespaces
	systemNamespaces := []string{
		"kube-system",
		"kube-public",
		"kube-node-lease",
	}

	for _, ns := range systemNamespaces {
		if pod.Namespace == ns {
			return true
		}
	}

	return false
}

// isDaemonSetPod checks if a pod is managed by a DaemonSet
func isDaemonSetPod(pod *corev1.Pod) bool {
	// Check if the pod has DaemonSet as an owner reference
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// generatePhysicalName generates physical name using MD5 hash
// Format: name(前31字符)-md5(namespace+"/"+name)
func (r *VirtualPodReconciler) generatePhysicalName(name, namespace string) string {
	// Truncate name to first 31 characters
	truncatedName := name
	if len(name) > 31 {
		truncatedName = name[:31]
	}

	// Generate MD5 hash of "namespace/name"
	input := fmt.Sprintf("%s/%s", namespace, name)
	hash := md5.Sum([]byte(input))
	hashString := fmt.Sprintf("%x", hash)

	// Return format: truncatedName-hashString
	return fmt.Sprintf("%s-%s", truncatedName, hashString)
}

// generatePhysicalPodName generates physical pod name using MD5 hash
// Format: podName(前31字符)-md5(podNamespace+"/"+podName)
func (r *VirtualPodReconciler) generatePhysicalPodName(podName, podNamespace string) string {
	return r.generatePhysicalName(podName, podNamespace)
}

// isPhysicalPodOwnedByVirtualPod checks if physical pod belongs to the given virtual pod
func (r *VirtualPodReconciler) isPhysicalPodOwnedByVirtualPod(physicalPod *corev1.Pod, virtualPod *corev1.Pod) bool {
	if physicalPod.Annotations == nil {
		return false
	}

	// Check if physical pod's annotations point to the current virtual pod
	virtualPodNamespace := physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]
	virtualPodName := physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodName]
	virtualPodUID := physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodUID]

	return virtualPodNamespace == virtualPod.Namespace &&
		virtualPodName == virtualPod.Name &&
		virtualPodUID == string(virtualPod.UID)
}

// recordPhysicalPodMapping records the physical pod mapping in virtual pod annotations
func (r *VirtualPodReconciler) recordPhysicalPodMapping(ctx context.Context, virtualPod *corev1.Pod, physicalPod *corev1.Pod) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// Update virtual pod with physical pod mapping
	updatedPod := virtualPod.DeepCopy()
	if updatedPod.Annotations == nil {
		updatedPod.Annotations = make(map[string]string)
	}
	updatedPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace] = physicalPod.Namespace
	updatedPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName] = physicalPod.Name
	updatedPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID] = string(physicalPod.UID)

	if err := r.VirtualClient.Status().Update(ctx, updatedPod); err != nil {
		logger.Error(err, "Failed to update virtual pod with physical pod mapping")
		return ctrl.Result{}, err
	}

	logger.Info("Recorded physical pod mapping", "physicalPod", fmt.Sprintf("%s/%s", physicalPod.Namespace, physicalPod.Name), "physicalUID", physicalPod.UID)
	return ctrl.Result{}, nil
}

// getPhysicalPodWithFallback attempts to get a physical pod using the cached client first,
// then falls back to direct k8s client if not found to handle cache update delays
func (r *VirtualPodReconciler) getPhysicalPodWithFallback(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	logger := r.Log.WithValues("physicalPod", fmt.Sprintf("%s/%s", namespace, name))

	podKey := types.NamespacedName{Namespace: namespace, Name: name}

	// First try with cached client (controller-runtime client)
	pod := &corev1.Pod{}
	err := r.PhysicalClient.Get(ctx, podKey, pod)
	if err == nil {
		return pod, nil
	}

	// If not found in cache, it might be a cache update delay
	if apierrors.IsNotFound(err) {
		logger.V(1).Info("Physical pod not found in cache, trying direct k8s client")

		// Try with direct k8s client to bypass cache
		directPod, directErr := r.PhysicalK8sClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if directErr == nil {
			logger.Info("Physical pod found via direct k8s client (cache delay detected)")
			return directPod, nil
		}

		// If still not found via direct client, it truly doesn't exist
		if apierrors.IsNotFound(directErr) {
			logger.V(1).Info("Physical pod confirmed not found via direct k8s client")
			return nil, directErr
		}

		// Return direct client error if it's not NotFound
		logger.Error(directErr, "Failed to get physical pod via direct k8s client")
		return nil, directErr
	}

	// Return original cached client error if it's not NotFound
	logger.Error(err, "Failed to get physical pod via cached client")
	return nil, err
}

// syncDependentResources syncs ConfigMaps, Secrets, and PVCs that the virtual pod depends on and returns the resource mapping
func (r *VirtualPodReconciler) syncDependentResources(ctx context.Context, virtualPod *corev1.Pod) (*ResourceMapping, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// 1. Sync ConfigMaps
	configMapMappings, err := r.syncConfigMaps(ctx, virtualPod)
	if err != nil {
		logger.Error(err, "Failed to sync ConfigMaps")
		return nil, err
	}

	// 2. Sync Secrets
	secretMappings, err := r.syncSecrets(ctx, virtualPod)
	if err != nil {
		logger.Error(err, "Failed to sync Secrets")
		return nil, err
	}

	// 3. Sync PVCs
	pvcMappings, err := r.syncPVCs(ctx, virtualPod)
	if err != nil {
		logger.Error(err, "Failed to sync PVCs")
		return nil, err
	}

	// Build resource mapping
	resourceMapping := &ResourceMapping{
		ConfigMaps: configMapMappings,
		Secrets:    secretMappings,
		PVCs:       pvcMappings,
	}

	return resourceMapping, nil
}

// syncConfigMaps syncs ConfigMaps referenced by the virtual pod and returns the mapping
func (r *VirtualPodReconciler) syncConfigMaps(ctx context.Context, virtualPod *corev1.Pod) (map[string]string, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// Collect all ConfigMap references from pod spec
	configMapRefs := make(map[string]bool)

	// From volumes
	for _, volume := range virtualPod.Spec.Volumes {
		if volume.ConfigMap != nil {
			configMapRefs[volume.ConfigMap.Name] = true
		}
	}

	// From env vars
	for _, container := range virtualPod.Spec.Containers {
		for _, envVar := range container.Env {
			if envVar.ValueFrom != nil && envVar.ValueFrom.ConfigMapKeyRef != nil {
				configMapRefs[envVar.ValueFrom.ConfigMapKeyRef.Name] = true
			}
		}
	}

	// From init containers
	for _, container := range virtualPod.Spec.InitContainers {
		for _, envVar := range container.Env {
			if envVar.ValueFrom != nil && envVar.ValueFrom.ConfigMapKeyRef != nil {
				configMapRefs[envVar.ValueFrom.ConfigMapKeyRef.Name] = true
			}
		}
	}

	// Sync each ConfigMap and collect mappings
	configMapMappings := make(map[string]string)
	for configMapName := range configMapRefs {
		physicalName, err := r.syncConfigMap(ctx, virtualPod.Namespace, configMapName)
		if err != nil {
			logger.Error(err, "Failed to sync ConfigMap", "configMapName", configMapName)
			return nil, err
		}
		configMapMappings[configMapName] = physicalName
	}

	return configMapMappings, nil
}

// syncSecrets syncs Secrets referenced by the virtual pod and returns the mapping
func (r *VirtualPodReconciler) syncSecrets(ctx context.Context, virtualPod *corev1.Pod) (map[string]string, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// Collect all Secret references from pod spec
	secretRefs := make(map[string]bool)

	// From volumes
	for _, volume := range virtualPod.Spec.Volumes {
		if volume.Secret != nil {
			secretRefs[volume.Secret.SecretName] = true
		}
	}

	// From env vars
	for _, container := range virtualPod.Spec.Containers {
		for _, envVar := range container.Env {
			if envVar.ValueFrom != nil && envVar.ValueFrom.SecretKeyRef != nil {
				secretRefs[envVar.ValueFrom.SecretKeyRef.Name] = true
			}
		}
	}

	// From init containers
	for _, container := range virtualPod.Spec.InitContainers {
		for _, envVar := range container.Env {
			if envVar.ValueFrom != nil && envVar.ValueFrom.SecretKeyRef != nil {
				secretRefs[envVar.ValueFrom.SecretKeyRef.Name] = true
			}
		}
	}

	// From image pull secrets
	for _, imagePullSecret := range virtualPod.Spec.ImagePullSecrets {
		secretRefs[imagePullSecret.Name] = true
	}

	// Sync each Secret and collect mappings
	secretMappings := make(map[string]string)
	for secretName := range secretRefs {
		physicalName, err := r.syncSecret(ctx, virtualPod.Namespace, secretName)
		if err != nil {
			logger.Error(err, "Failed to sync Secret", "secretName", secretName)
			return nil, err
		}
		secretMappings[secretName] = physicalName
	}

	return secretMappings, nil
}

// syncPVCs syncs PVCs referenced by the virtual pod and returns the mapping
func (r *VirtualPodReconciler) syncPVCs(ctx context.Context, virtualPod *corev1.Pod) (map[string]string, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// Collect all PVC references from pod spec
	pvcRefs := make(map[string]bool)

	// From volumes
	for _, volume := range virtualPod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			pvcRefs[volume.PersistentVolumeClaim.ClaimName] = true
		}
	}

	// Sync each PVC and collect mappings
	pvcMappings := make(map[string]string)
	for pvcName := range pvcRefs {
		physicalName, err := r.syncPVC(ctx, virtualPod.Namespace, pvcName)
		if err != nil {
			logger.Error(err, "Failed to sync PVC", "pvcName", pvcName)
			return nil, err
		}
		pvcMappings[pvcName] = physicalName
	}

	return pvcMappings, nil
}

// ResourceType represents the type of Kubernetes resource
type ResourceType string

const (
	ResourceTypeConfigMap ResourceType = "ConfigMap"
	ResourceTypeSecret    ResourceType = "Secret"
	ResourceTypePVC       ResourceType = "PVC"
)

// ResourceMapping represents the mapping from virtual resource names to physical resource names
type ResourceMapping struct {
	ConfigMaps map[string]string // virtual name -> physical name
	Secrets    map[string]string // virtual name -> physical name
	PVCs       map[string]string // virtual name -> physical name
}

// syncResource syncs a single resource (ConfigMap, Secret, or PVC) and returns the physical resource name
func (r *VirtualPodReconciler) syncResource(ctx context.Context, resourceType ResourceType, virtualNamespace, resourceName string, emptyObj client.Object) (string, error) {
	logger := r.Log.WithValues(string(resourceType), fmt.Sprintf("%s/%s", virtualNamespace, resourceName))

	// 1. Get virtual resource
	err := r.VirtualClient.Get(ctx, types.NamespacedName{Namespace: virtualNamespace, Name: resourceName}, emptyObj)
	if err != nil {
		logger.Error(err, fmt.Sprintf("Failed to get virtual %s %s/%s", resourceType, virtualNamespace, resourceName))
		return "", err
	}
	virtualObj := emptyObj

	// 2. Generate physical name
	physicalName := r.generatePhysicalResourceName(resourceName, virtualNamespace)

	// 3. Check if physical resource exists and validate ownership
	physicalExists, physicalObj, err := r.checkPhysicalResourceExists(ctx, resourceType, physicalName, emptyObj)
	if err != nil {
		return "", err
	}

	if physicalExists {
		// Check if physical resource is owned by current virtual resource
		existingVirtualName := physicalObj.GetAnnotations()[cloudv1beta1.AnnotationVirtualName]
		existingVirtualNamespace := physicalObj.GetAnnotations()[cloudv1beta1.AnnotationVirtualNamespace]
		if existingVirtualName != resourceName || existingVirtualNamespace != virtualNamespace {
			logger.Error(nil, fmt.Sprintf("Physical %s exists but owned by different virtual %s", resourceType, resourceType),
				"physicalResource", fmt.Sprintf("%s/%s", r.ClusterBinding.Spec.MountNamespace, physicalName),
				"currentVirtualName", fmt.Sprintf("%s/%s", virtualNamespace, resourceName),
				"existingVirtualName", fmt.Sprintf("%s/%s", existingVirtualNamespace, existingVirtualName))
			return "", fmt.Errorf("physical %s %s/%s is owned by different virtual %s %s/%s, expected %s/%s",
				resourceType, r.ClusterBinding.Spec.MountNamespace, physicalName, resourceType, existingVirtualNamespace, existingVirtualName, virtualNamespace, resourceName)
		}
	}

	// 4. Update virtual resource annotations if needed
	if err := r.updateVirtualResourceLabelsAndAnnotations(ctx, virtualObj, physicalName); err != nil {
		return "", err
	}

	// 5. Create physical resource if it doesn't exist
	if !physicalExists {
		if err := r.createPhysicalResource(ctx, resourceType, virtualObj, physicalName); err != nil {
			return "", err
		}
	}

	return physicalName, nil
}

// syncConfigMap syncs a single ConfigMap and returns the physical resource name
func (r *VirtualPodReconciler) syncConfigMap(ctx context.Context, virtualNamespace, configMapName string) (string, error) {
	return r.syncResource(ctx, ResourceTypeConfigMap, virtualNamespace, configMapName, &corev1.ConfigMap{})
}

// syncSecret syncs a single Secret and returns the physical resource name
func (r *VirtualPodReconciler) syncSecret(ctx context.Context, virtualNamespace, secretName string) (string, error) {
	return r.syncResource(ctx, ResourceTypeSecret, virtualNamespace, secretName, &corev1.Secret{})
}

// syncPVC syncs a single PVC and returns the physical resource name
func (r *VirtualPodReconciler) syncPVC(ctx context.Context, virtualNamespace, pvcName string) (string, error) {
	return r.syncResource(ctx, ResourceTypePVC, virtualNamespace, pvcName, &corev1.PersistentVolumeClaim{})
}

// checkPhysicalResourceExists checks if physical resource exists using both cached and direct client
func (r *VirtualPodReconciler) checkPhysicalResourceExists(ctx context.Context, resourceType ResourceType, physicalName string, obj client.Object) (bool, client.Object, error) {
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	return CheckPhysicalResourceExists(ctx, resourceType, physicalName, physicalNamespace, obj,
		r.PhysicalClient, r.PhysicalK8sClient, r.Log)
}

// createPhysicalResource creates a physical resource by type
func (r *VirtualPodReconciler) createPhysicalResource(ctx context.Context, resourceType ResourceType, virtualObj client.Object, physicalName string) error {
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace
	return CreatePhysicalResource(ctx, resourceType, virtualObj, physicalName, physicalNamespace, r.PhysicalClient, r.Log)
}

// generatePhysicalResourceName generates physical resource name using MD5 hash
// Format: resourceName(前31字符)-md5(resourceNamespace+"/"+resourceName)
func (r *VirtualPodReconciler) generatePhysicalResourceName(resourceName, resourceNamespace string) string {
	return r.generatePhysicalName(resourceName, resourceNamespace)
}

// updateVirtualResourceLabelsAndAnnotations updates virtual resource labels and annotations with physical name mapping
func (r *VirtualPodReconciler) updateVirtualResourceLabelsAndAnnotations(ctx context.Context, obj client.Object, physicalName string) error {
	logger := r.Log.WithValues("resourceKind", obj.GetObjectKind().GroupVersionKind().Kind, "resource", fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName()))

	// Check if annotation already exists and matches
	if obj.GetAnnotations() != nil && obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName] == physicalName &&
		obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalNamespace] == r.ClusterBinding.Spec.MountNamespace &&
		obj.GetLabels() != nil && obj.GetLabels()[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
		logger.V(1).Info("Physical name and namespace annotation, managed-by label already exists and matches, skipping update",
			"physicalName", physicalName,
			"physicalNamespace", r.ClusterBinding.Spec.MountNamespace)
		return nil
	}

	// Check if annotation already exists but doesn't match (should not happen in normal flow)
	if obj.GetAnnotations() != nil && obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName] != "" && obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName] != physicalName {
		logger.V(1).Info("Physical name annotation already exists but doesn't match, skipping update",
			"existingPhysicalName", obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName],
			"newPhysicalName", physicalName)
		return fmt.Errorf("physical name annotation already exists but doesn't match, physicalName: %s, newPhysicalName: %s",
			obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName], physicalName)
	}

	// Update resource
	updatedObj := obj.DeepCopyObject().(client.Object)

	// Update labels
	if updatedObj.GetLabels() == nil {
		updatedObj.SetLabels(make(map[string]string))
	}
	updatedObj.GetLabels()[cloudv1beta1.LabelManagedBy] = cloudv1beta1.LabelManagedByValue

	// Update annotations
	if updatedObj.GetAnnotations() == nil {
		updatedObj.SetAnnotations(make(map[string]string))
	}
	updatedObj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName] = physicalName
	updatedObj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalNamespace] = r.ClusterBinding.Spec.MountNamespace

	// Add finalizer if not present
	if !r.hasSyncedResourceFinalizer(updatedObj) {
		r.addSyncedResourceFinalizer(updatedObj)
		logger.V(1).Info("Added synced-resource finalizer")
	}

	// Update the resource
	if err := r.VirtualClient.Update(ctx, updatedObj); err != nil {
		logger.Error(err, "Failed to update virtual resource annotations and labels")
		return err
	}

	logger.Info("Updated virtual resource annotations and labels", "physicalName", physicalName)
	return nil
}

// hasSyncedResourceFinalizer checks if the resource has our finalizer
func (r *VirtualPodReconciler) hasSyncedResourceFinalizer(obj client.Object) bool {
	return controllerutil.ContainsFinalizer(obj, cloudv1beta1.SyncedResourceFinalizer)
}

// addSyncedResourceFinalizer adds our finalizer to the resource
func (r *VirtualPodReconciler) addSyncedResourceFinalizer(obj client.Object) {
	controllerutil.AddFinalizer(obj, cloudv1beta1.SyncedResourceFinalizer)
}
