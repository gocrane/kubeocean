package topdown

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/syncer/topdown/token"
	"github.com/TKEColocation/kubeocean/pkg/utils"
	authenticationv1 "k8s.io/api/authentication/v1"
)

const (
	// Kubernetes service environment variable names
	KubernetesServiceHost = "KUBERNETES_SERVICE_HOST"
	KubernetesServicePort = "KUBERNETES_SERVICE_PORT"
	// Hostname environment variable name
	HostnameEnvVar = "HOSTNAME"
)

var (
	PhysicalPodSchedulingTimeout = 5 * time.Minute
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
	clusterID         string // Cached cluster ID for performance
	EventRecorder     record.EventRecorder
	TokenManager      token.TokenManagerInterface
	// Cached loadbalancer IPs and ports
	kubernetesIntranetIP   string // Cached kubernetes-intranet loadbalancer IP
	kubernetesIntranetPort string // Cached kubernetes-intranet service port
	kubeDnsIntranetIP      string // Cached kube-dns-intranet loadbalancer IP
}

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=get;list;watch;create;update;patch

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

	// 2. Check if pod is scheduled on a kubeocean-managed virtual node
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
	if !r.shouldCreatePhysicalPod(virtualPod) {
		logger.V(1).Info("Virtual pod should not be created, doing nothing")
		return ctrl.Result{}, nil
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

	// Physical pod exists and UID is already set, check if physical pod is scheduled
	if physicalPod.Spec.NodeName == "" {
		// Physical pod is not scheduled yet, check if it has exceeded the scheduling timeout
		if physicalPod.CreationTimestamp.IsZero() || time.Since(physicalPod.CreationTimestamp.Time) > PhysicalPodSchedulingTimeout {
			logger.Info("Physical pod scheduling timeout exceeded, deleting physical pod and setting FailedSchedulingPod event",
				"physicalPod", fmt.Sprintf("%s/%s", physicalPod.Namespace, physicalPod.Name),
				"creationTime", physicalPod.CreationTimestamp.Time,
				"timeout", PhysicalPodSchedulingTimeout)

			// Delete the physical pod
			err = r.PhysicalClient.Delete(ctx, physicalPod)
			if err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete unscheduled physical pod")
				return ctrl.Result{}, err
			}

			// Set FailedSchedulingPod event on virtual pod
			if r.EventRecorder != nil {
				r.EventRecorder.Event(virtualPod, corev1.EventTypeWarning, "FailedSchedulingPod",
					fmt.Sprintf("Physical pod %s/%s failed to schedule within %v timeout",
						physicalPod.Namespace, physicalPod.Name, PhysicalPodSchedulingTimeout))
			}

			return ctrl.Result{}, nil
		}

		// Physical pod is not scheduled but hasn't exceeded timeout, requeue with remaining time
		var remainingTime time.Duration
		if physicalPod.CreationTimestamp.IsZero() {
			remainingTime = PhysicalPodSchedulingTimeout
		} else {
			remainingTime = PhysicalPodSchedulingTimeout - time.Since(physicalPod.CreationTimestamp.Time)
		}
		logger.V(1).Info("Physical pod not scheduled yet, requeuing with remaining time",
			"physicalPod", fmt.Sprintf("%s/%s", physicalPod.Namespace, physicalPod.Name),
			"remainingTime", remainingTime)
		return ctrl.Result{RequeueAfter: remainingTime}, nil
	}

	// Physical pod is scheduled, check if service account token needs updating
	if virtualPod.Spec.ServiceAccountName != "" {
		// Check and update service account token, don't create if not exists
		_, err = r.syncServiceAccountToken(ctx, virtualPod, false)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to check and update service account token")
				return ctrl.Result{}, err
			}
			logger.Error(err, "Service account token not found, skip requeue")
			// Don't requeue on error, just log it
			return ctrl.Result{}, nil
		}

		// Set requeue after 10 minutes for token refresh
		return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
	}

	// No service account, no action needed
	logger.V(1).Info("Physical pod exists and is scheduled, no action needed",
		"physicalPod", fmt.Sprintf("%s/%s", physicalPod.Namespace, physicalPod.Name),
		"nodeName", physicalPod.Spec.NodeName)
	return ctrl.Result{}, nil
}

func (r *VirtualPodReconciler) shouldCreatePhysicalPod(virtualPod *corev1.Pod) bool {
	if virtualPod == nil {
		return false
	}
	// Only sync pods with spec.nodeName set (scheduled pods)
	if virtualPod.Spec.NodeName == "" {
		return false
	}
	// Skip system pods
	if utils.IsSystemPod(virtualPod) {
		return false
	}
	// Only sync pods that are not managed by DaemonSet
	if isDaemonSetPod(virtualPod) {
		return false
	}
	if virtualPod.Labels[cloudv1beta1.LabelHostPortFakePod] == cloudv1beta1.LabelValueTrue {
		return false
	}
	return true
}

// handleVirtualPodDeletion handles the deletion of virtual pod
func (r *VirtualPodReconciler) handleVirtualPodDeletion(ctx context.Context, virtualPod *corev1.Pod) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// Clean up service account token secret if service account name exists
	if virtualPod.Spec.ServiceAccountName != "" {
		if err := r.cleanupServiceAccountToken(ctx, virtualPod); err != nil {
			logger.Error(err, "Failed to cleanup service account token secret")
			return ctrl.Result{}, err
		}
	}

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

	// Check if it's a kubeocean-managed virtual node
	managedBy := virtualNode.Labels[cloudv1beta1.LabelManagedBy]
	if managedBy != "kubeocean" {
		logger.V(1).Info("Virtual node not managed by kubeocean", "virtualNodeName", virtualNodeName, "managedBy", managedBy)
		return false, "", nil
	}

	// Check if the virtual node belongs to current cluster binding
	// Check by physical-cluster-name annotation (if exists) or physical-cluster-id label
	physicalClusterName := virtualNode.Annotations["kubeocean.io/physical-cluster-name"]
	physicalClusterID := virtualNode.Labels[cloudv1beta1.LabelPhysicalClusterID]

	currentClusterName := r.ClusterBinding.Name
	currentClusterID := r.clusterID

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
		err := r.createPhysicalPod(ctx, virtualPod, physicalNodeName)
		if err != nil {
			// Record warning event for virtual pod
			if r.EventRecorder != nil {
				r.EventRecorder.Eventf(virtualPod, corev1.EventTypeWarning, "FailedCreatePod", "Failed to create pod: %v", err)
			}
		}
		return ctrl.Result{}, err
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

	// 2. Generate physical pod name using new format: podName(前30字符)-md5(podNamespace+"/"+podName)
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
func (r *VirtualPodReconciler) createPhysicalPod(ctx context.Context, virtualPod *corev1.Pod, physicalNodeName string) error {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	physicalNamespace := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	physicalName := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]

	// 1. Sync dependent resources (ConfigMaps, Secrets, PVCs)
	resourceMapping, err := r.syncDependentResources(ctx, virtualPod)
	if err != nil {
		logger.Error(err, "Failed to sync dependent resources")
		return err
	}

	// 2. Build physical pod spec
	podSpec, err := r.buildPhysicalPodSpec(ctx, virtualPod, physicalNodeName, resourceMapping)
	if err != nil {
		logger.Error(err, "Failed to build physical pod spec")
		return err
	}

	// 3. Get workload information
	workloadType, workloadName, err := r.getWorkloadInfo(ctx, virtualPod)
	if err != nil {
		logger.Error(err, "Failed to get workload information")
		return err
	}

	physicalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        physicalName,
			Namespace:   physicalNamespace,
			Labels:      r.buildPhysicalPodLabels(virtualPod, workloadType, workloadName),
			Annotations: r.buildPhysicalPodAnnotations(virtualPod),
		},
		Spec: podSpec,
	}

	// 4. Create physical pod
	err = r.PhysicalClient.Create(ctx, physicalPod)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.Info("Physical pod already exists, updating virtual pod with UID")
			// Get the existing physical pod to retrieve its UID using fallback method
			_, getErr := r.getPhysicalPodWithFallback(ctx, physicalNamespace, physicalName)
			if getErr != nil {
				logger.Error(getErr, "Failed to get existing physical pod")
				return getErr
			}
			// skip updating virtual pod with physical pod UID, it will be updated by physical pod controller
			return nil
		}
		logger.Error(err, "Failed to create physical pod")
		return err
	}

	logger.Info("Successfully created physical pod",
		"physicalPod", fmt.Sprintf("%s/%s", physicalNamespace, physicalName),
		"physicalUID", string(physicalPod.UID))

	// skip updating virtual pod with physical pod UID, it will be updated by physical pod controller
	return nil
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
func (r *VirtualPodReconciler) buildPhysicalPodLabels(virtualPod *corev1.Pod, workloadType, workloadName string) map[string]string {
	labels := make(map[string]string)

	// Copy all labels from virtual pod
	for k, v := range virtualPod.Labels {
		labels[k] = v
	}

	// Add Kubeocean managed-by label
	labels[cloudv1beta1.LabelManagedBy] = cloudv1beta1.LabelManagedByValue

	// Add virtual namespace label
	labels[cloudv1beta1.LabelVirtualNamespace] = virtualPod.Namespace

	// Add workload type and name labels
	if workloadType != "" {
		labels[cloudv1beta1.LabelWorkloadType] = workloadType
	}
	if workloadName != "" {
		labels[cloudv1beta1.LabelWorkloadName] = workloadName
	}

	return labels
}

// getWorkloadInfo extracts workload type and name from pod's owner references
func (r *VirtualPodReconciler) getWorkloadInfo(ctx context.Context, pod *corev1.Pod) (workloadType, workloadName string, err error) {
	if len(pod.OwnerReferences) == 0 {
		return "pod", "", nil
	}

	// Get the first owner reference (usually the direct owner)
	ownerRef := pod.OwnerReferences[0]

	switch ownerRef.Kind {
	case "ReplicaSet":
		// For ReplicaSet, we need to find the corresponding Deployment
		deploymentName, err := r.getDeploymentFromReplicaSet(ctx, pod.Namespace, ownerRef.Name)
		if err != nil {
			return "", "", fmt.Errorf("failed to get deployment from replicaset %s: %w", ownerRef.Name, err)
		}
		if deploymentName != "" {
			return "deployment", deploymentName, nil
		}
		return "replicaset", ownerRef.Name, nil
	case "Deployment":
		return "deployment", ownerRef.Name, nil
	case "DaemonSet":
		return "daemonset", ownerRef.Name, nil
	case "StatefulSet":
		return "statefulset", ownerRef.Name, nil
	case "CronJob":
		return "cronjob", ownerRef.Name, nil
	case "Job":
		return "job", ownerRef.Name, nil
	default:
		return strings.ToLower(ownerRef.Kind), ownerRef.Name, nil
	}
}

// getDeploymentFromReplicaSet finds the deployment that owns the given replicaset
func (r *VirtualPodReconciler) getDeploymentFromReplicaSet(ctx context.Context, namespace, replicasetName string) (string, error) {
	var replicaset appsv1.ReplicaSet
	if err := r.VirtualClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: replicasetName}, &replicaset); err != nil {
		return "", fmt.Errorf("failed to get ReplicaSet %s: %w", replicasetName, err)
	}

	// Look for deployment owner reference
	for _, ownerRef := range replicaset.OwnerReferences {
		if ownerRef.Kind == "Deployment" {
			return ownerRef.Name, nil
		}
	}

	return "", nil
}

// buildPhysicalPodAnnotations builds annotations for physical pod
func (r *VirtualPodReconciler) buildPhysicalPodAnnotations(virtualPod *corev1.Pod) map[string]string {
	annotations := make(map[string]string)

	// Copy all annotations from virtual pod (excluding Kubeocean internal ones)
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

	// Add virtual node name annotation
	annotations[cloudv1beta1.AnnotationVirtualNodeName] = virtualPod.Spec.NodeName

	return annotations
}

const (
	// Field path constants for DownwardAPI
	metadataNamespaceFieldPath = "metadata.namespace"
	metadataNameFieldPath      = "metadata.name"
	specNodeNameFieldPath      = "spec.nodeName"
)

// replaceDownwardAPIFieldPaths replaces metadata.namespace, metadata.name and spec.nodeName fieldPath
// with annotation references in DownwardAPI items
func (r *VirtualPodReconciler) replaceDownwardAPIFieldPaths(items []corev1.DownwardAPIVolumeFile) {
	for i := range items {
		item := &items[i]
		if item.FieldRef != nil {
			// Replace metadata.namespace fieldPath with kubeocean.io/virtual-pod-namespace annotation
			if item.FieldRef.FieldPath == metadataNamespaceFieldPath {
				item.FieldRef.FieldPath = fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace)
			}
			// Replace metadata.name fieldPath with kubeocean.io/virtual-pod-name annotation
			if item.FieldRef.FieldPath == metadataNameFieldPath {
				item.FieldRef.FieldPath = fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName)
			}
			// Replace spec.nodeName fieldPath with kubeocean.io/virtual-node-name annotation
			if item.FieldRef.FieldPath == specNodeNameFieldPath {
				item.FieldRef.FieldPath = fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName)
			}
		}
	}
}

// replaceContainerDownwardAPIFieldPaths replaces metadata.namespace, metadata.name and spec.nodeName fieldPath
// with annotation references in container environment variables
func (r *VirtualPodReconciler) replaceContainerDownwardAPIFieldPaths(envVars []corev1.EnvVar) {
	for i := range envVars {
		envVar := &envVars[i]
		if envVar.ValueFrom != nil && envVar.ValueFrom.FieldRef != nil {
			// Replace metadata.namespace fieldPath with kubeocean.io/virtual-pod-namespace annotation
			if envVar.ValueFrom.FieldRef.FieldPath == metadataNamespaceFieldPath {
				envVar.ValueFrom.FieldRef.FieldPath = fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace)
			}
			// Replace metadata.name fieldPath with kubeocean.io/virtual-pod-name annotation
			if envVar.ValueFrom.FieldRef.FieldPath == metadataNameFieldPath {
				envVar.ValueFrom.FieldRef.FieldPath = fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName)
			}
			// Replace spec.nodeName fieldPath with kubeocean.io/virtual-node-name annotation
			if envVar.ValueFrom.FieldRef.FieldPath == specNodeNameFieldPath {
				envVar.ValueFrom.FieldRef.FieldPath = fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName)
			}
		}
	}
}

func (r *VirtualPodReconciler) buildPhysicalPodSpec(ctx context.Context, virtualPod *corev1.Pod, physicalNodeName string, resourceMapping *ResourceMapping) (corev1.PodSpec, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	// Check if VirtualClient is available (e.g., in test environments)
	if r.VirtualClient == nil {
		return corev1.PodSpec{}, fmt.Errorf("VirtualClient is not available")
	}

	// Deep copy the spec to avoid modifying the original
	spec := *virtualPod.Spec.DeepCopy()

	// Set node affinity to force scheduling to the specific physical node
	spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchFields: []corev1.NodeSelectorRequirement{
							{
								Key:      "metadata.name",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{physicalNodeName},
							},
						},
					},
				},
			},
		},
	}

	// Get ClusterBinding
	cb := &cloudv1beta1.ClusterBinding{}
	if err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: r.ClusterBinding.Name}, cb); err != nil {
		return spec, fmt.Errorf("failed to get cluster binding: %w", err)
	}
	r.ClusterBinding = cb

	// Set PriorityClassName based on ClusterBinding configuration
	priorityClassName := r.ClusterBinding.Spec.PodPriorityClassName
	if priorityClassName == "" {
		priorityClassName = cloudv1beta1.DefaultPriorityClassName
		// Ensure default PriorityClass exists
		if err := r.ensureDefaultPriorityClass(ctx); err != nil {
			return spec, fmt.Errorf("failed to ensure default PriorityClass: %w", err)
		}
	} else {
		logger.Info("Using custom PriorityClass", "priorityClassName", priorityClassName)
	}
	spec.PriorityClassName = priorityClassName
	spec.Priority = nil

	// cleanup service account related fields
	spec.ServiceAccountName = ""
	spec.DeprecatedServiceAccount = ""
	spec.NodeName = ""

	// Replace resource names in volumes
	if err := r.replaceVolumeResourceNames(&spec, virtualPod, resourceMapping); err != nil {
		return spec, err
	}

	// Replace resource names in containers
	if err := r.replaceContainerResourceNamesInSpec(&spec, resourceMapping); err != nil {
		return spec, err
	}

	// Replace image pull secret names
	if err := r.replaceImagePullSecretNames(&spec, resourceMapping); err != nil {
		return spec, err
	}

	// Add hostAliases and inject environment variables
	if err := r.addHostAliasesAndEnvVars(ctx, &spec, virtualPod.Name); err != nil {
		return spec, err
	}

	// Configure DNS policy and DNS config
	if err := r.configureDNSPolicy(ctx, &spec); err != nil {
		return spec, err
	}

	return spec, nil
}

// replaceVolumeResourceNames replaces resource names in volumes
func (r *VirtualPodReconciler) replaceVolumeResourceNames(spec *corev1.PodSpec, virtualPod *corev1.Pod, resourceMapping *ResourceMapping) error {
	for i := range spec.Volumes {
		volume := &spec.Volumes[i]

		// Replace ConfigMap names
		if volume.ConfigMap != nil {
			if physicalName, exists := resourceMapping.ConfigMaps[volume.ConfigMap.Name]; exists {
				volume.ConfigMap.Name = physicalName
			} else {
				return fmt.Errorf("configMap mapping not found for virtual ConfigMap: %s", volume.ConfigMap.Name)
			}
		}

		// Replace Secret names
		if volume.Secret != nil {
			if physicalName, exists := resourceMapping.Secrets[volume.Secret.SecretName]; exists {
				volume.Secret.SecretName = physicalName
			} else {
				return fmt.Errorf("secret mapping not found for virtual Secret: %s", volume.Secret.SecretName)
			}
		}

		// Replace PVC names
		if volume.PersistentVolumeClaim != nil {
			if physicalName, exists := resourceMapping.PVCs[volume.PersistentVolumeClaim.ClaimName]; exists {
				volume.PersistentVolumeClaim.ClaimName = physicalName
			} else {
				return fmt.Errorf("PVC mapping not found for virtual PVC: %s", volume.PersistentVolumeClaim.ClaimName)
			}
		}

		// Handle projected volumes for ConfigMaps and Secrets
		if volume.Projected != nil {
			for j := range volume.Projected.Sources {
				source := &volume.Projected.Sources[j]

				// Replace ConfigMap names in projected sources
				if source.ConfigMap != nil {
					if physicalName, exists := resourceMapping.ConfigMaps[source.ConfigMap.Name]; exists {
						source.ConfigMap.Name = physicalName
					} else {
						return fmt.Errorf("configMap mapping not found for virtual ConfigMap in projected volume: %s", source.ConfigMap.Name)
					}
				}

				// Replace Secret names in projected sources
				if source.Secret != nil {
					if physicalName, exists := resourceMapping.Secrets[source.Secret.Name]; exists {
						source.Secret.Name = physicalName
					} else {
						return fmt.Errorf("secret mapping not found for virtual Secret in projected volume: %s", source.Secret.Name)
					}
				}

				// Handle DownwardAPI projections in projected volumes
				if source.DownwardAPI != nil {
					r.replaceDownwardAPIFieldPaths(source.DownwardAPI.Items)
				}
			}
		}

		// Handle kube-api-access projected volumes for service account tokens
		if strings.HasPrefix(volume.Name, "kube-api-access-") && virtualPod.Spec.ServiceAccountName != "" {
			if resourceMapping.ServiceAccountTokenName == "" {
				return fmt.Errorf("service account token mapping not found for virtual ServiceAccount: %s", virtualPod.Spec.ServiceAccountName)
			}
			if volume.Projected != nil {
				for j := range volume.Projected.Sources {
					source := &volume.Projected.Sources[j]
					if source.ServiceAccountToken != nil {
						// Replace the service account token source with our mapped secret
						source.ServiceAccountToken = nil
						source.Secret = &corev1.SecretProjection{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: resourceMapping.ServiceAccountTokenName,
							},
							Items: []corev1.KeyToPath{
								{
									Key:  "token",
									Path: "token",
								},
							},
						}
						break
					}
				}
			}
		}

		// Handle DownwardAPI volumes - replace metadata.namespace and metadata.name fieldPath
		if volume.DownwardAPI != nil {
			r.replaceDownwardAPIFieldPaths(volume.DownwardAPI.Items)
		}
	}
	return nil
}

// replaceContainerResourceNamesInSpec replaces resource names in all containers
func (r *VirtualPodReconciler) replaceContainerResourceNamesInSpec(spec *corev1.PodSpec, resourceMapping *ResourceMapping) error {
	// Replace resource names in containers
	for i := range spec.Containers {
		container := &spec.Containers[i]
		if err := r.replaceContainerResourceNames(container, resourceMapping); err != nil {
			return err
		}
	}

	// Replace resource names in init containers
	for i := range spec.InitContainers {
		container := &spec.InitContainers[i]
		if err := r.replaceContainerResourceNames(container, resourceMapping); err != nil {
			return err
		}
	}
	return nil
}

// replaceImagePullSecretNames replaces image pull secret names
func (r *VirtualPodReconciler) replaceImagePullSecretNames(spec *corev1.PodSpec, resourceMapping *ResourceMapping) error {
	for i := range spec.ImagePullSecrets {
		imagePullSecret := &spec.ImagePullSecrets[i]
		if physicalName, exists := resourceMapping.Secrets[imagePullSecret.Name]; exists {
			imagePullSecret.Name = physicalName
		} else {
			return fmt.Errorf("secret mapping not found for image pull secret: %s", imagePullSecret.Name)
		}
	}
	return nil
}

// addHostAliasesAndEnvVars adds hostAliases and injects environment variables
func (r *VirtualPodReconciler) addHostAliasesAndEnvVars(ctx context.Context, spec *corev1.PodSpec, virtualPodName string) error {
	// Add hostAliases for kubernetes.default.svc and get IP/port for environment variables
	kubernetesIntranetIP, kubernetesIntranetPort, err := r.getKubernetesIntranetIPAndPort(ctx)
	if err != nil {
		return fmt.Errorf("failed to get kubernetes-intranet IP and port: %v", err)
	}

	// Add hostAlias for kubernetes.default.svc
	spec.HostAliases = append(spec.HostAliases, corev1.HostAlias{
		IP:        kubernetesIntranetIP,
		Hostnames: []string{"kubernetes.default.svc"},
	})

	// Inject KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables to all containers
	for i := range spec.Containers {
		container := &spec.Containers[i]
		r.injectKubernetesServiceEnvVars(container, kubernetesIntranetIP, kubernetesIntranetPort)
		// Inject HOSTNAME environment variable with virtual pod name
		r.injectHostnameEnvVar(container, virtualPodName)
	}

	// Inject KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables to all init containers
	for i := range spec.InitContainers {
		container := &spec.InitContainers[i]
		r.injectKubernetesServiceEnvVars(container, kubernetesIntranetIP, kubernetesIntranetPort)
		// Inject HOSTNAME environment variable with virtual pod name
		r.injectHostnameEnvVar(container, virtualPodName)
	}
	return nil
}

// configureDNSPolicy configures DNS policy and DNS config
func (r *VirtualPodReconciler) configureDNSPolicy(ctx context.Context, spec *corev1.PodSpec) error {
	// Configure DNS policy and DNS config
	if spec.DNSPolicy == corev1.DNSClusterFirst || spec.DNSPolicy == corev1.DNSClusterFirstWithHostNet {
		// Get kube-dns-intranet IP
		kubeDnsIntranetIP, err := r.getKubeDnsIntranetIP(ctx)
		if err != nil {
			return fmt.Errorf("failed to get kube-dns-intranet IP: %v", err)
		}

		// Set DNS policy to None
		spec.DNSPolicy = corev1.DNSNone

		// Configure DNS config
		spec.DNSConfig = &corev1.PodDNSConfig{
			Nameservers: []string{kubeDnsIntranetIP},
			Options: []corev1.PodDNSConfigOption{
				{
					Name:  "ndots",
					Value: ptr.To("3"),
				},
			},
			Searches: []string{
				"default.svc.cluster.local",
				"svc.cluster.local",
				"cluster.local",
			},
		}
	}
	return nil
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

	// Handle DownwardAPI fieldRef in environment variables
	r.replaceContainerDownwardAPIFieldPaths(container.Env)

	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualPodReconciler) SetupWithManager(virtualManager, physicalManager ctrl.Manager) error {
	if r.VirtualClient == nil {
		return fmt.Errorf("VirtualClient is not available")
	}

	if r.ClusterBinding == nil {
		return fmt.Errorf("cluster binding is nil")
	}

	if r.ClusterBinding.Spec.MountNamespace == "" {
		return fmt.Errorf("cluster binding mount namespace is empty")
	}

	if r.ClusterBinding.Spec.ClusterID == "" {
		return fmt.Errorf("cluster binding cluster ID is empty")
	}

	// Cache cluster ID for performance
	r.clusterID = r.ClusterBinding.Spec.ClusterID

	// Set up EventRecorder
	r.EventRecorder = virtualManager.GetEventRecorderFor(fmt.Sprintf("kubeocean-syncer-%s", r.ClusterBinding.Name))

	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("virtualpod-%s", r.ClusterBinding.Name)

	rateLimiter := workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 5*time.Minute)
	r.workQueue = workqueue.NewTypedRateLimitingQueueWithConfig(rateLimiter, workqueue.TypedRateLimitingQueueConfig[reconcile.Request]{
		Name: controllerName,
	})

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
			RateLimiter:             rateLimiter,
			NewQueue: func(controllerName string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
				return r.workQueue
			},
		}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			pod := obj.(*corev1.Pod)
			if pod == nil {
				return false
			}
			// Only sync pods with spec.nodeName set (scheduled pods)
			if pod.Spec.NodeName == "" {
				return false
			}
			if pod.DeletionTimestamp != nil {
				return true
			}
			// Skip system pods
			if utils.IsSystemPod(pod) {
				return false
			}
			// Only sync pods that are not managed by DaemonSet
			if isDaemonSetPod(pod) {
				return false
			}
			return true
		})).
		Complete(r)
}

// handlePhysicalPodEvent handles physical pod create/update/delete events
// and enqueues the corresponding virtual pod for reconciliation
func (r *VirtualPodReconciler) handlePhysicalPodEvent(pod *corev1.Pod, eventType string) {
	// Filter physical pods: only care about pods with kubeocean.io/managed-by=kubeocean label
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
// Format: name(前30字符)-md5(namespace+"/"+name)
func (r *VirtualPodReconciler) generatePhysicalName(name, namespace string) string {
	// Truncate name to first 30 characters
	truncatedName := name
	if len(name) > 30 {
		truncatedName = name[:30]
	}

	// Generate MD5 hash of "namespace/name"
	input := fmt.Sprintf("%s/%s", namespace, name)
	hash := md5.Sum([]byte(input))
	hashString := fmt.Sprintf("%x", hash)

	// Return format: truncatedName-hashString
	return fmt.Sprintf("%s-%s", truncatedName, hashString)
}

// generatePhysicalPodName generates physical pod name using MD5 hash
// Format: podName(前30字符)-md5(podNamespace+"/"+podName)
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

	// 4. Sync ServiceAccountToken if ServiceAccountName is not empty
	var serviceAccountTokenName string
	if virtualPod.Spec.ServiceAccountName != "" {
		serviceAccountTokenName, err = r.syncServiceAccountToken(ctx, virtualPod, true) // Create if not exists during pod creation
		if err != nil {
			logger.Error(err, "Failed to sync ServiceAccountToken")
			return nil, err
		}
	}

	// Build resource mapping
	resourceMapping := &ResourceMapping{
		ConfigMaps:              configMapMappings,
		Secrets:                 secretMappings,
		PVCs:                    pvcMappings,
		ServiceAccountTokenName: serviceAccountTokenName,
	}

	// 5. Poll to verify all resources are created in physical cluster
	if err := r.pollPhysicalResources(ctx, resourceMapping, virtualPod.Namespace, virtualPod.Name); err != nil {
		logger.Error(err, "Failed to verify physical resources creation")
		return nil, err
	}

	return resourceMapping, nil
}

// pollPhysicalResources polls to verify that all resources in the mapping are created in the physical cluster
func (r *VirtualPodReconciler) pollPhysicalResources(ctx context.Context, resourceMapping *ResourceMapping, virtualNamespace, virtualPodName string) error {
	logger := r.Log.WithValues("operation", "pollPhysicalResources", "virtualPod", fmt.Sprintf("%s/%s", virtualNamespace, virtualPodName))

	// Convert virtual namespace to physical namespace
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	// Poll configuration: 500ms interval, 5s timeout
	pollInterval := 500 * time.Millisecond
	pollTimeout := 5 * time.Second

	logger.V(1).Info("Starting to poll physical resources",
		"physicalNamespace", physicalNamespace,
		"pollInterval", pollInterval,
		"pollTimeout", pollTimeout)

	// Use wait.PollUntilContextTimeout for polling (replaces deprecated PollImmediateWithContext)
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		// Check all ConfigMaps
		for virtualName, physicalName := range resourceMapping.ConfigMaps {
			var configMap corev1.ConfigMap
			err := r.PhysicalClient.Get(ctx, types.NamespacedName{Namespace: physicalNamespace, Name: physicalName}, &configMap)
			if err != nil {
				// For non-NotFound errors, return immediately
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "Error checking ConfigMap existence",
						"virtualName", virtualName,
						"physicalName", physicalName)
					return false, err
				}
				// Resource not found, continue polling
				logger.V(1).Info("ConfigMap not found, continuing to poll",
					"virtualName", virtualName,
					"physicalName", physicalName)
				return false, nil
			}
		}

		// Check all Secrets
		for virtualName, physicalName := range resourceMapping.Secrets {
			var secret corev1.Secret
			err := r.PhysicalClient.Get(ctx, types.NamespacedName{Namespace: physicalNamespace, Name: physicalName}, &secret)
			if err != nil {
				// For non-NotFound errors, return immediately
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "Error checking Secret existence",
						"virtualName", virtualName,
						"physicalName", physicalName)
					return false, err
				}
				// Resource not found, continue polling
				logger.V(1).Info("Secret not found, continuing to poll",
					"virtualName", virtualName,
					"physicalName", physicalName)
				return false, nil
			}
		}

		// Check all PVCs
		for virtualName, physicalName := range resourceMapping.PVCs {
			var pvc corev1.PersistentVolumeClaim
			err := r.PhysicalClient.Get(ctx, types.NamespacedName{Namespace: physicalNamespace, Name: physicalName}, &pvc)
			if err != nil {
				// For non-NotFound errors, return immediately
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "Error checking PVC existence",
						"virtualName", virtualName,
						"physicalName", physicalName)
					return false, err
				}
				// Resource not found, continue polling
				logger.V(1).Info("PVC not found, continuing to poll",
					"virtualName", virtualName,
					"physicalName", physicalName)
				return false, nil
			}
		}

		// Check ServiceAccountToken if exists
		if resourceMapping.ServiceAccountTokenName != "" {
			var secret corev1.Secret
			err := r.PhysicalClient.Get(ctx, types.NamespacedName{Namespace: physicalNamespace, Name: resourceMapping.ServiceAccountTokenName}, &secret)
			if err != nil {
				// For non-NotFound errors, return immediately
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "Error checking ServiceAccountToken existence",
						"physicalName", resourceMapping.ServiceAccountTokenName)
					return false, err
				}
				// Resource not found, continue polling
				logger.V(1).Info("ServiceAccountToken not found, continuing to poll",
					"physicalName", resourceMapping.ServiceAccountTokenName)
				return false, nil
			}
		}

		// All resources exist
		logger.Info("All physical resources verified successfully")
		return true, nil
	})

	if err != nil {
		if wait.Interrupted(err) {
			return fmt.Errorf("timeout waiting for physical resources to be created: %w", err)
		}
		return fmt.Errorf("error polling physical resources: %w", err)
	}

	return nil
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
		// From projected volumes
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ConfigMap != nil {
					configMapRefs[source.ConfigMap.Name] = true
				}
			}
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
		// From projected volumes
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil {
					secretRefs[source.Secret.Name] = true
				}
			}
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

// ResourceMapping represents the mapping from virtual resource names to physical resource names
type ResourceMapping struct {
	ConfigMaps              map[string]string // virtual name -> physical name
	Secrets                 map[string]string // virtual name -> physical name
	PVCs                    map[string]string // virtual name -> physical name
	ServiceAccountTokenName string            // physical secret name for service account token
}

// syncResource syncs a single resource (ConfigMap, Secret, or PVC) and returns the physical resource name
func (r *VirtualPodReconciler) syncResource(ctx context.Context, resourceType ResourceType, virtualNamespace, resourceName, physicalNamespace string, emptyObj client.Object, syncResourceOpt *SyncResourceOpt) (string, error) {
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
	physicalExists, physicalObj, err := r.checkPhysicalResourceExists(ctx, resourceType, physicalName, physicalNamespace, emptyObj)
	if err != nil {
		return "", err
	}

	if physicalExists {
		// Check if physical resource is owned by current virtual resource
		existingVirtualName := physicalObj.GetAnnotations()[cloudv1beta1.AnnotationVirtualName]
		existingVirtualNamespace := physicalObj.GetAnnotations()[cloudv1beta1.AnnotationVirtualNamespace]
		if existingVirtualName != resourceName || existingVirtualNamespace != virtualNamespace {
			logger.Error(nil, fmt.Sprintf("Physical %s exists but owned by different virtual %s", resourceType, resourceType),
				"physicalResource", fmt.Sprintf("%s/%s", physicalNamespace, physicalName),
				"currentVirtualName", fmt.Sprintf("%s/%s", virtualNamespace, resourceName),
				"existingVirtualName", fmt.Sprintf("%s/%s", existingVirtualNamespace, existingVirtualName))
			return "", fmt.Errorf("physical %s %s/%s is owned by different virtual %s %s/%s, expected %s/%s",
				resourceType, physicalNamespace, physicalName, resourceType, existingVirtualNamespace, existingVirtualName, virtualNamespace, resourceName)
		}
	}

	// 4. Update virtual resource annotations if needed
	if err := r.updateVirtualResourceLabelsAndAnnotations(ctx, virtualObj, physicalName, physicalNamespace, syncResourceOpt); err != nil {
		return "", err
	}

	// 5. Create physical resource if it doesn't exist
	if !physicalExists {
		if err := r.createPhysicalResourceWithOpt(ctx, resourceType, virtualObj, physicalName, physicalNamespace, syncResourceOpt); err != nil {
			return "", err
		}
	}

	return physicalName, nil
}

// syncConfigMap syncs a single ConfigMap and returns the physical resource name
func (r *VirtualPodReconciler) syncConfigMap(ctx context.Context, virtualNamespace, configMapName string) (string, error) {
	return r.syncResource(ctx, ResourceTypeConfigMap, virtualNamespace, configMapName, r.ClusterBinding.Spec.MountNamespace, &corev1.ConfigMap{}, nil)
}

// syncSecret syncs a single Secret and returns the physical resource name
func (r *VirtualPodReconciler) syncSecret(ctx context.Context, virtualNamespace, secretName string) (string, error) {
	return r.syncResource(ctx, ResourceTypeSecret, virtualNamespace, secretName, r.ClusterBinding.Spec.MountNamespace, &corev1.Secret{}, nil)
}

// syncPVC syncs a single PVC and returns the physical resource name
// If PVC is bound and has a volumeName, it also syncs the associated PV
func (r *VirtualPodReconciler) syncPVC(ctx context.Context, virtualNamespace, pvcName string) (string, error) {
	// First check if PVC is bound and has a volumeName (associated PV)
	var pvc corev1.PersistentVolumeClaim
	if err := r.VirtualClient.Get(ctx, types.NamespacedName{Namespace: virtualNamespace, Name: pvcName}, &pvc); err != nil {
		r.Log.Error(err, "Failed to get PVC for PV sync check", "pvc", fmt.Sprintf("%s/%s", virtualNamespace, pvcName))
		return "", fmt.Errorf("failed to get PVC %s/%s: %w", virtualNamespace, pvcName, err)
	}

	// Check if PVC is bound and has a volumeName
	if pvc.Status.Phase != corev1.ClaimBound {
		return "", fmt.Errorf("PVC %s/%s is not bound, current phase: %s", virtualNamespace, pvcName, pvc.Status.Phase)
	}

	if pvc.Spec.VolumeName == "" {
		return "", fmt.Errorf("PVC %s/%s is bound but has no volumeName", virtualNamespace, pvcName)
	}

	// If PVC is bound and has a volumeName, sync the associated PV first
	r.Log.Info("PVC is bound with volumeName, syncing associated PV first",
		"pvc", fmt.Sprintf("%s/%s", virtualNamespace, pvcName),
		"volumeName", pvc.Spec.VolumeName)

	// Sync PV with empty namespace (PVs are cluster-scoped)
	physicalPVName, err := r.syncPV(ctx, pvc.Spec.VolumeName)
	if err != nil {
		r.Log.Error(err, "Failed to sync associated PV",
			"pvc", fmt.Sprintf("%s/%s", virtualNamespace, pvcName),
			"pv", pvc.Spec.VolumeName)
		return "", fmt.Errorf("failed to sync associated PV %s for PVC %s/%s: %w", pvc.Spec.VolumeName, virtualNamespace, pvcName, err)
	}

	r.Log.Info("Successfully synced associated PV, now syncing PVC",
		"pvc", fmt.Sprintf("%s/%s", virtualNamespace, pvcName),
		"originalPV", pvc.Spec.VolumeName,
		"physicalPV", physicalPVName)

	// Then sync the PVC itself, passing the physical PV name
	syncOpt := &SyncResourceOpt{
		PhysicalPVName: physicalPVName,
	}
	physicalPVCName, err := r.syncResource(ctx, ResourceTypePVC, virtualNamespace, pvcName, r.ClusterBinding.Spec.MountNamespace, &corev1.PersistentVolumeClaim{}, syncOpt)
	if err != nil {
		return "", err
	}

	return physicalPVCName, nil
}

// syncPV syncs a single PV and returns the physical resource name
// If PV has CSI nodePublishSecretRef, it also syncs the associated secret
func (r *VirtualPodReconciler) syncPV(ctx context.Context, pvName string) (string, error) {
	// Get the virtual PV
	var virtualPV corev1.PersistentVolume
	if err := r.VirtualClient.Get(ctx, types.NamespacedName{Name: pvName}, &virtualPV); err != nil {
		r.Log.Error(err, "Failed to get virtual PV", "pv", pvName)
		return "", fmt.Errorf("failed to get virtual PV %s: %w", pvName, err)
	}

	// Check if PV has CSI spec with nodePublishSecretRef
	if virtualPV.Spec.CSI != nil && virtualPV.Spec.CSI.NodePublishSecretRef != nil {
		secretRef := virtualPV.Spec.CSI.NodePublishSecretRef
		logger := r.Log.WithValues("pv", pvName, "secretRef", fmt.Sprintf("%s/%s", secretRef.Namespace, secretRef.Name))

		logger.Info("PV has CSI nodePublishSecretRef, syncing the secret first")

		// Sync the CSI secret using existing syncResource function
		syncOpt := &SyncResourceOpt{
			IsPVRefSecret: true,
		}
		physicalSecretName, err := r.syncResource(ctx, ResourceTypeSecret, secretRef.Namespace, secretRef.Name, secretRef.Namespace, &corev1.Secret{}, syncOpt)
		if err != nil {
			logger.Error(err, "Failed to sync CSI secret")
			return "", fmt.Errorf("failed to sync CSI secret %s/%s for PV %s: %w", secretRef.Namespace, secretRef.Name, pvName, err)
		}

		logger.Info("Successfully synced CSI secret, now syncing PV with updated secret reference",
			"originalSecret", fmt.Sprintf("%s/%s", secretRef.Namespace, secretRef.Name),
			"physicalSecret", fmt.Sprintf("%s/%s", secretRef.Namespace, physicalSecretName))

		// Sync PV with updated CSI secret reference using syncResource
		syncOpt = &SyncResourceOpt{
			PhysicalPVRefSecretName: physicalSecretName,
		}
		return r.syncResource(ctx, ResourceTypePV, "", pvName, "", &corev1.PersistentVolume{}, syncOpt)
	}
	// No CSI secret reference, sync PV normally
	return r.syncResource(ctx, ResourceTypePV, "", pvName, "", &corev1.PersistentVolume{}, nil)
}

// cleanupServiceAccountToken cleans up the service account token secret when pod is deleted
func (r *VirtualPodReconciler) cleanupServiceAccountToken(ctx context.Context, virtualPod *corev1.Pod) error {
	if virtualPod.Spec.ServiceAccountName == "" {
		return nil // No service account, nothing to clean up
	}
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name), "serviceAccountName", virtualPod.Spec.ServiceAccountName)

	// Delete the service account token from TokenManager
	r.TokenManager.DeleteServiceAccountToken(virtualPod.UID)

	key := fmt.Sprintf("%s-%s", virtualPod.Name, virtualPod.UID)
	// Generate physical secret name using the key
	physicalSecretName := r.generatePhysicalName(key, virtualPod.Namespace)
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	// Delete the secret from physical cluster
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      physicalSecretName,
			Namespace: physicalNamespace,
		},
	}
	logger.V(1).Info("Deleting service account token secret", "secret", fmt.Sprintf("%s/%s", physicalNamespace, physicalSecretName))

	err := r.PhysicalClient.Delete(ctx, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Service account token secret already deleted", "secret", fmt.Sprintf("%s/%s", physicalNamespace, physicalSecretName))
			return nil
		}
		logger.Error(err, "Failed to delete service account token secret", "secret", fmt.Sprintf("%s/%s", physicalNamespace, physicalSecretName))
		return fmt.Errorf("failed to delete service account token secret %s/%s: %v", physicalNamespace, physicalSecretName, err)
	}

	logger.Info("Successfully deleted service account token secret", "secret", fmt.Sprintf("%s/%s", physicalNamespace, physicalSecretName))
	return nil
}

// syncServiceAccountToken syncs the service account token for the virtual pod
// createIfNotExists: if true, create secret when not exists; if false, return error when not exists
func (r *VirtualPodReconciler) syncServiceAccountToken(ctx context.Context, virtualPod *corev1.Pod, createIfNotExists bool) (string, error) {
	logger := r.Log.WithValues("virtualPod", fmt.Sprintf("%s/%s", virtualPod.Namespace, virtualPod.Name))

	if r.TokenManager == nil {
		return "", fmt.Errorf("TokenManager is not initialized")
	}

	var tp *corev1.ServiceAccountTokenProjection
	for _, volume := range virtualPod.Spec.Volumes {
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ServiceAccountToken != nil {
					tp = source.ServiceAccountToken
					break
				}
			}
		}
	}
	if tp == nil {
		return "", fmt.Errorf("service account token projection not found in virtual pod %s/%s", virtualPod.Namespace, virtualPod.Name)
	}

	var auds []string
	if len(tp.Audience) != 0 {
		auds = []string{tp.Audience}
	}
	// Create TokenRequest for the service account
	tokenRequest := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         auds,
			ExpirationSeconds: tp.ExpirationSeconds,
			BoundObjectRef: &authenticationv1.BoundObjectReference{
				APIVersion: "v1",
				Kind:       "Pod",
				Name:       virtualPod.Name,
				UID:        virtualPod.UID,
			},
		},
	}

	// Get service account token using TokenManager
	token, err := r.TokenManager.GetServiceAccountToken(virtualPod.Namespace, virtualPod.Spec.ServiceAccountName, tokenRequest)
	if err != nil {
		return "", fmt.Errorf("failed to get service account token %s: %v", virtualPod.Spec.ServiceAccountName, err)
	}

	// Generate physical secret name using the key
	key := fmt.Sprintf("%s-%s", virtualPod.Name, virtualPod.UID)
	physicalSecretName := r.generatePhysicalName(key, virtualPod.Namespace)
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	// Create or update the secret in physical cluster
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      physicalSecretName,
			Namespace: physicalNamespace,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy:           cloudv1beta1.LabelManagedByValue,
				cloudv1beta1.LabelServiceAccountToken: "true",
			},
			Annotations: map[string]string{
				cloudv1beta1.AnnotationVirtualPodName:      virtualPod.Name,
				cloudv1beta1.AnnotationVirtualPodNamespace: virtualPod.Namespace,
				cloudv1beta1.AnnotationVirtualPodUID:       string(virtualPod.UID),
				"kubeocean.io/service-account":             virtualPod.Spec.ServiceAccountName,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"token": []byte(token.Status.Token),
		},
	}

	// Check if secret already exists
	existingSecret := &corev1.Secret{}
	err = r.PhysicalClient.Get(ctx, types.NamespacedName{Namespace: physicalNamespace, Name: physicalSecretName}, existingSecret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("failed to check if service account token secret exists %s/%s: %v", physicalNamespace, physicalSecretName, err)
		}

		// Secret not found
		if !createIfNotExists {
			return "", err
		}
		// Create new secret
		if err := r.PhysicalClient.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("failed to create service account token secret %s/%s: %v", physicalNamespace, physicalSecretName, err)
		}
		logger.Info("Created service account token secret", "secret", fmt.Sprintf("%s/%s", physicalNamespace, physicalSecretName))
	} else {
		// Check if token needs updating
		currentToken := string(existingSecret.Data["token"])
		newTokenBase64 := base64.StdEncoding.EncodeToString([]byte(token.Status.Token))
		if currentToken == newTokenBase64 {
			// Token is the same, no update needed
			logger.V(1).Info("Service account token is up to date, no update needed", "secret", fmt.Sprintf("%s/%s", physicalNamespace, physicalSecretName))
		} else {
			// Token is different, update the secret
			newSecret := existingSecret.DeepCopy()
			newSecret.Data["token"] = []byte(token.Status.Token)
			if err := r.PhysicalClient.Update(ctx, newSecret); err != nil {
				return "", fmt.Errorf("failed to update service account token secret %s/%s: %v", physicalNamespace, physicalSecretName, err)
			}
			logger.Info("Updated service account token secret", "secret", fmt.Sprintf("%s/%s", physicalNamespace, physicalSecretName))
		}
	}

	return physicalSecretName, nil
}

// checkPhysicalResourceExists checks if physical resource exists using both cached and direct client
func (r *VirtualPodReconciler) checkPhysicalResourceExists(ctx context.Context, resourceType ResourceType, physicalName, physicalNamespace string, obj client.Object) (bool, client.Object, error) {
	return CheckPhysicalResourceExists(ctx, resourceType, physicalName, physicalNamespace, obj,
		r.PhysicalClient, r.PhysicalK8sClient, r.Log)
}

// createPhysicalResourceWithOpt creates a physical resource by type with sync options
func (r *VirtualPodReconciler) createPhysicalResourceWithOpt(ctx context.Context, resourceType ResourceType, virtualObj client.Object, physicalName, physicalNamespace string, syncResourceOpt *SyncResourceOpt) error {
	return CreatePhysicalResourceWithOpt(ctx, resourceType, virtualObj, physicalName, physicalNamespace, r.PhysicalClient, r.Log, syncResourceOpt)
}

// generatePhysicalResourceName generates physical resource name using MD5 hash
// Format: resourceName(前30字符)-md5(resourceNamespace+"/"+resourceName)
func (r *VirtualPodReconciler) generatePhysicalResourceName(resourceName, resourceNamespace string) string {
	return r.generatePhysicalName(resourceName, resourceNamespace)
}

// updateVirtualResourceLabelsAndAnnotations updates virtual resource labels and annotations with physical name mapping
func (r *VirtualPodReconciler) updateVirtualResourceLabelsAndAnnotations(ctx context.Context, obj client.Object, physicalName, physicalNamespace string, syncResourceOpt *SyncResourceOpt) error {
	// Create logger with appropriate resource path
	resourcePath := fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName())
	logger := r.Log.WithValues("resourceKind", obj.GetObjectKind().GroupVersionKind().Kind, "resource", resourcePath)

	// Check if annotation already exists and matches
	if obj.GetAnnotations() != nil && obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName] == physicalName &&
		obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalNamespace] == physicalNamespace &&
		obj.GetLabels() != nil && obj.GetLabels()[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
		logger.V(1).Info("Physical name and namespace annotation, managed-by label already exists and matches, skipping update",
			"physicalName", physicalName,
			"physicalNamespace", physicalNamespace)
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

	// Add cluster-specific managed-by label
	managedByClusterIDLabel := GetManagedByClusterIDLabel(r.clusterID)
	updatedObj.GetLabels()[managedByClusterIDLabel] = "true"

	if syncResourceOpt != nil && syncResourceOpt.IsPVRefSecret {
		updatedObj.GetLabels()[cloudv1beta1.LabelUsedByPV] = "true"
	}

	// Update annotations
	if updatedObj.GetAnnotations() == nil {
		updatedObj.SetAnnotations(make(map[string]string))
	}
	updatedObj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName] = physicalName
	updatedObj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalNamespace] = physicalNamespace

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
	clusterSpecificFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, r.clusterID)
	return controllerutil.ContainsFinalizer(obj, clusterSpecificFinalizer)
}

// addSyncedResourceFinalizer adds our finalizer to the resource
func (r *VirtualPodReconciler) addSyncedResourceFinalizer(obj client.Object) {
	clusterSpecificFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, r.clusterID)
	controllerutil.AddFinalizer(obj, clusterSpecificFinalizer)
}

// getKubernetesIntranetIP gets the kubernetes-intranet loadbalancer IP from virtual cluster
// Returns cached value if available, otherwise fetches from virtual cluster and caches it
func (r *VirtualPodReconciler) getKubernetesIntranetIPAndPort(ctx context.Context) (string, string, error) {
	// Return cached values if available
	if r.kubernetesIntranetIP != "" && r.kubernetesIntranetPort != "" {
		return r.kubernetesIntranetIP, r.kubernetesIntranetPort, nil
	}

	// Check if VirtualClient is available (e.g., in test environments)
	if r.VirtualClient == nil {
		return "", "", fmt.Errorf("VirtualClient is not available")
	}

	// Fetch from virtual cluster
	service := &corev1.Service{}
	err := r.VirtualClient.Get(ctx, types.NamespacedName{
		Name:      "kubernetes-intranet",
		Namespace: "default",
	}, service)
	if err != nil {
		return "", "", fmt.Errorf("failed to get kubernetes-intranet service: %v", err)
	}

	// Check if service has loadbalancer ingress
	if len(service.Status.LoadBalancer.Ingress) == 0 {
		return "", "", fmt.Errorf("kubernetes-intranet service has no loadbalancer ingress")
	}

	// Get the first ingress IP
	ip := service.Status.LoadBalancer.Ingress[0].IP
	if ip == "" {
		return "", "", fmt.Errorf("kubernetes-intranet service loadbalancer ingress IP is empty")
	}

	// Get the first port from service spec
	if len(service.Spec.Ports) == 0 {
		return "", "", fmt.Errorf("kubernetes-intranet service has no ports")
	}

	port := service.Spec.Ports[0].Port
	if port == 0 {
		return "", "", fmt.Errorf("kubernetes-intranet service first port is invalid")
	}

	// Convert port to string
	portStr := fmt.Sprintf("%d", port)

	// Cache the IP and port
	r.kubernetesIntranetIP = ip
	r.kubernetesIntranetPort = portStr
	return ip, portStr, nil
}

// getKubeDnsIntranetIP gets the kube-dns-intranet loadbalancer IP from virtual cluster
// Returns cached value if available, otherwise fetches from virtual cluster and caches it
func (r *VirtualPodReconciler) getKubeDnsIntranetIP(ctx context.Context) (string, error) {
	// Return cached value if available
	if r.kubeDnsIntranetIP != "" {
		return r.kubeDnsIntranetIP, nil
	}

	// Check if VirtualClient is available (e.g., in test environments)
	if r.VirtualClient == nil {
		return "", fmt.Errorf("VirtualClient is not available")
	}

	// Fetch from virtual cluster
	service := &corev1.Service{}
	err := r.VirtualClient.Get(ctx, types.NamespacedName{
		Name:      "kube-dns-intranet",
		Namespace: "kube-system",
	}, service)
	if err != nil {
		return "", fmt.Errorf("failed to get kube-dns-intranet service: %v", err)
	}

	// Check if service has loadbalancer ingress
	if len(service.Status.LoadBalancer.Ingress) == 0 {
		return "", fmt.Errorf("kube-dns-intranet service has no loadbalancer ingress")
	}

	// Get the first ingress IP
	ip := service.Status.LoadBalancer.Ingress[0].IP
	if ip == "" {
		return "", fmt.Errorf("kube-dns-intranet service loadbalancer ingress IP is empty")
	}

	// Cache the IP
	r.kubeDnsIntranetIP = ip
	return ip, nil
}

// injectKubernetesServiceEnvVars injects KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables
// into the container's environment variables, overriding any existing values
func (r *VirtualPodReconciler) injectKubernetesServiceEnvVars(container *corev1.Container, host, port string) {
	// Track if we found and updated existing environment variables
	hostFound := false
	portFound := false

	// First pass: update existing environment variables if they exist
	for i := range container.Env {
		switch container.Env[i].Name {
		case KubernetesServiceHost:
			container.Env[i].Value = host
			container.Env[i].ValueFrom = nil // Clear ValueFrom if it exists
			hostFound = true
		case KubernetesServicePort:
			container.Env[i].Value = port
			container.Env[i].ValueFrom = nil // Clear ValueFrom if it exists
			portFound = true
		}
	}

	// Second pass: append missing environment variables
	if !hostFound {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  KubernetesServiceHost,
			Value: host,
		})
	}
	if !portFound {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  KubernetesServicePort,
			Value: port,
		})
	}
}

// injectHostnameEnvVar injects HOSTNAME environment variable into the container's environment variables,
// setting the value to the virtual pod name, overriding any existing values
func (r *VirtualPodReconciler) injectHostnameEnvVar(container *corev1.Container, virtualPodName string) {
	// Track if we found and updated existing HOSTNAME environment variable
	hostnameFound := false

	// First pass: update existing HOSTNAME environment variable if it exists
	for i := range container.Env {
		if container.Env[i].Name == HostnameEnvVar {
			container.Env[i].Value = virtualPodName
			container.Env[i].ValueFrom = nil // Clear ValueFrom if it exists
			hostnameFound = true
			break
		}
	}

	// Second pass: append missing HOSTNAME environment variable
	if !hostnameFound {
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  HostnameEnvVar,
			Value: virtualPodName,
		})
	}
}

// ensureDefaultPriorityClass ensures that the kubeocean-default PriorityClass exists
func (r *VirtualPodReconciler) ensureDefaultPriorityClass(ctx context.Context) error {
	logger := r.Log.WithValues("priorityClass", cloudv1beta1.DefaultPriorityClassName)

	// Check if PriorityClass already exists
	priorityClass := &schedulingv1.PriorityClass{}
	err := r.PhysicalClient.Get(ctx, client.ObjectKey{Name: cloudv1beta1.DefaultPriorityClassName}, priorityClass)
	if err == nil {
		// PriorityClass exists, nothing to do
		logger.V(1).Info("Default PriorityClass already exists")
		return nil
	}

	if !apierrors.IsNotFound(err) {
		logger.Error(err, "Failed to check if default PriorityClass exists")
		return err
	}

	// PriorityClass doesn't exist, create it
	logger.Info("Creating default PriorityClass")
	priorityClass = &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: cloudv1beta1.DefaultPriorityClassName,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
		},
		Value:            0,
		PreemptionPolicy: ptr.To(corev1.PreemptLowerPriority),
		Description:      "Default PriorityClass for Kubeocean physical pods",
	}

	err = r.PhysicalClient.Create(ctx, priorityClass)
	if err != nil {
		if !apierrors.IsAlreadyExists(err) {
			logger.Error(err, "Failed to create default PriorityClass")
			return err
		}
		logger.V(1).Info("Default PriorityClass already exists")
		return nil
	}

	logger.Info("Successfully created default PriorityClass")
	return nil
}
