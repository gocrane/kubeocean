package topdown

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

// SyncResourceOpt contains options for resource synchronization
type SyncResourceOpt struct {
	PhysicalPVName          string
	IsPVRefSecret           bool
	PhysicalPVRefSecretName string
}

// CheckPhysicalResourceExists checks if physical resource exists using both cached and direct client
// This is a common function that can be used by different reconcilers
func CheckPhysicalResourceExists(ctx context.Context, resourceType ResourceType, physicalName, physicalNamespace string, srcobj client.Object,
	physicalClient client.Client, physicalK8sClient kubernetes.Interface, logger logr.Logger) (bool, client.Object, error) {

	logger = logger.WithValues(fmt.Sprintf("physical%s", resourceType), physicalName)

	resourceKey := types.NamespacedName{Namespace: physicalNamespace, Name: physicalName}

	obj := srcobj.DeepCopyObject().(client.Object)
	err := physicalClient.Get(ctx, resourceKey, obj)
	if err == nil {
		return true, obj, nil
	}

	// If not found in cache, try with direct k8s client
	if apierrors.IsNotFound(err) {
		logger.V(1).Info(fmt.Sprintf("Physical %s not found in cache, trying direct k8s client", resourceType))

		var directObj client.Object
		var directErr error

		switch resourceType {
		case ResourceTypeConfigMap:
			directObj, directErr = physicalK8sClient.CoreV1().ConfigMaps(physicalNamespace).Get(ctx, physicalName, metav1.GetOptions{})
		case ResourceTypeSecret:
			directObj, directErr = physicalK8sClient.CoreV1().Secrets(physicalNamespace).Get(ctx, physicalName, metav1.GetOptions{})
		case ResourceTypePVC:
			directObj, directErr = physicalK8sClient.CoreV1().PersistentVolumeClaims(physicalNamespace).Get(ctx, physicalName, metav1.GetOptions{})
		case ResourceTypePV:
			directObj, directErr = physicalK8sClient.CoreV1().PersistentVolumes().Get(ctx, physicalName, metav1.GetOptions{})
		default:
			return false, nil, fmt.Errorf("unsupported resource type: %s", resourceType)
		}

		if directErr != nil {
			if apierrors.IsNotFound(directErr) {
				logger.V(1).Info(fmt.Sprintf("Physical %s confirmed not found via direct k8s client", resourceType))
				return false, nil, nil
			}
			logger.Error(directErr, fmt.Sprintf("Failed to get physical %s via direct k8s client", resourceType))
			return false, nil, directErr
		}

		logger.Info(fmt.Sprintf("Physical %s found via direct k8s client (cache delay detected)", resourceType))
		return true, directObj, nil
	}

	// Return original cached client error if it's not NotFound
	logger.Error(err, fmt.Sprintf("Failed to get physical %s via cached client", resourceType))
	return false, nil, err
}

// BuildPhysicalResourceLabels builds labels for physical resources
// This is a common function that can be used by different reconcilers
func BuildPhysicalResourceLabels(virtualObj client.Object) map[string]string {
	labels := make(map[string]string)

	// Copy all labels from virtual resource
	for k, v := range virtualObj.GetLabels() {
		labels[k] = v
	}

	// Add Tapestry managed-by label
	labels[cloudv1beta1.LabelManagedBy] = cloudv1beta1.LabelManagedByValue

	return labels
}

// BuildPhysicalResourceLabelsWithClusterID builds labels for physical resources with cluster-specific labels
// This is a common function that can be used by different reconcilers
func BuildPhysicalResourceLabelsWithClusterID(virtualObj client.Object, clusterID string) map[string]string {
	labels := BuildPhysicalResourceLabels(virtualObj)

	// Add cluster-specific managed-by label
	managedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterID)
	labels[managedByClusterIDLabel] = "true"

	return labels
}

// BuildPhysicalResourceAnnotations builds annotations for physical resources
// This is a common function that can be used by different reconcilers
func BuildPhysicalResourceAnnotations(virtualObj client.Object) map[string]string {
	annotations := make(map[string]string)

	// Copy all annotations from virtual resource (excluding Tapestry internal ones)
	for k, v := range virtualObj.GetAnnotations() {
		if k != cloudv1beta1.AnnotationPhysicalPodNamespace &&
			k != cloudv1beta1.AnnotationPhysicalPodName &&
			k != cloudv1beta1.AnnotationPhysicalPodUID &&
			k != cloudv1beta1.AnnotationPhysicalNamespace &&
			k != cloudv1beta1.AnnotationPhysicalName &&
			k != cloudv1beta1.AnnotationLastSyncTime {
			annotations[k] = v
		}
	}

	// Add virtual resource mapping annotations
	// For PVs, namespace might be empty (cluster-scoped resource)
	annotations[cloudv1beta1.AnnotationVirtualNamespace] = virtualObj.GetNamespace()
	annotations[cloudv1beta1.AnnotationVirtualName] = virtualObj.GetName()

	return annotations
}

// CreatePhysicalResource creates a physical resource by type
// This is a common function that can be used by different reconcilers
func CreatePhysicalResource(ctx context.Context, resourceType ResourceType, virtualObj client.Object, physicalName, physicalNamespace string,
	physicalClient client.Client, logger logr.Logger) error {
	return CreatePhysicalResourceWithOpt(ctx, resourceType, virtualObj, physicalName, physicalNamespace, physicalClient, logger, nil)
}

func CreatePhysicalResourceWithOpt(ctx context.Context, resourceType ResourceType, virtualObj client.Object, physicalName, physicalNamespace string,
	physicalClient client.Client, logger logr.Logger, syncResourceOpt *SyncResourceOpt) error {

	logger = logger.WithValues(string(resourceType), fmt.Sprintf("%s/%s", virtualObj.GetNamespace(), virtualObj.GetName()))

	// Build physical resource
	physicalObj, err := BuildPhysicalResource(resourceType, virtualObj, physicalName, physicalNamespace, syncResourceOpt)
	if err != nil {
		return err
	}

	// Create physical resource
	err = physicalClient.Create(ctx, physicalObj)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.V(1).Info(fmt.Sprintf("Physical %s already exists, skipping", resourceType))
			return nil
		}
		logger.Error(err, fmt.Sprintf("Failed to create physical %s", resourceType))
		return err
	}

	resourcePath := fmt.Sprintf("%s/%s", physicalNamespace, physicalName)

	logger.Info(fmt.Sprintf("Successfully created physical %s", resourceType),
		fmt.Sprintf("physical%s", resourceType), resourcePath)
	return nil
}

// BuildPhysicalResource builds a physical resource by type
// This is a common function that can be used by different reconcilers
func BuildPhysicalResource(resourceType ResourceType, virtualObj client.Object, physicalName, physicalNamespace string, syncResourceOpt *SyncResourceOpt) (client.Object, error) {
	switch resourceType {
	case ResourceTypeConfigMap:
		if configMap, ok := virtualObj.(*corev1.ConfigMap); ok {
			physicalConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        physicalName,
					Namespace:   physicalNamespace,
					Labels:      BuildPhysicalResourceLabels(configMap),
					Annotations: BuildPhysicalResourceAnnotations(configMap),
				},
				Data:       configMap.Data,
				BinaryData: configMap.BinaryData,
			}
			return physicalConfigMap, nil
		}
		return nil, fmt.Errorf("invalid object type for ConfigMap: %T", virtualObj)
	case ResourceTypeSecret:
		if secret, ok := virtualObj.(*corev1.Secret); ok {
			labels := BuildPhysicalResourceLabels(secret)

			// Add PV usage label if this secret is referenced by PV
			if syncResourceOpt != nil && syncResourceOpt.IsPVRefSecret {
				labels[cloudv1beta1.LabelUsedByPV] = "true"
			}

			physicalSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:        physicalName,
					Namespace:   physicalNamespace,
					Labels:      labels,
					Annotations: BuildPhysicalResourceAnnotations(secret),
				},
				Type:       secret.Type,
				Data:       secret.Data,
				StringData: secret.StringData,
			}
			return physicalSecret, nil
		}
		return nil, fmt.Errorf("invalid object type for Secret: %T", virtualObj)
	case ResourceTypePVC:
		if pvc, ok := virtualObj.(*corev1.PersistentVolumeClaim); ok {
			physicalPVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        physicalName,
					Namespace:   physicalNamespace,
					Labels:      BuildPhysicalResourceLabels(pvc),
					Annotations: BuildPhysicalResourceAnnotations(pvc),
				},
				Spec:   pvc.Spec,
				Status: pvc.Status,
			}
			physicalPVC.Spec.StorageClassName = nil
			physicalPVC.Status.Phase = corev1.ClaimPending

			// If physical PV name is provided in opts, update the VolumeName
			if syncResourceOpt != nil && syncResourceOpt.PhysicalPVName != "" {
				physicalPVC.Spec.VolumeName = syncResourceOpt.PhysicalPVName
			}

			return physicalPVC, nil
		}
		return nil, fmt.Errorf("invalid object type for PVC: %T", virtualObj)
	case ResourceTypePV:
		if pv, ok := virtualObj.(*corev1.PersistentVolume); ok {
			physicalPV := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:        physicalName,
					Labels:      BuildPhysicalResourceLabels(pv),
					Annotations: BuildPhysicalResourceAnnotations(pv),
				},
				Spec: pv.Spec,
				// empty status
			}
			// need to rebuild ref to pvc
			physicalPV.Spec.ClaimRef = nil
			// need to retain the volume when pv is deleted
			physicalPV.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain

			// Update CSI secret reference if provided
			if syncResourceOpt != nil && syncResourceOpt.PhysicalPVRefSecretName != "" &&
				physicalPV.Spec.CSI != nil && physicalPV.Spec.CSI.NodePublishSecretRef != nil {
				physicalPV.Spec.CSI.NodePublishSecretRef.Name = syncResourceOpt.PhysicalPVRefSecretName
				// Keep the same namespace as virtual cluster for NodePublishSecretRef
				// The namespace should remain the same as the virtual namespace
			}

			return physicalPV, nil
		}
		return nil, fmt.Errorf("invalid object type for PV: %T", virtualObj)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

// RemoveSyncedResourceFinalizerWithClusterID removes the cluster-specific synced resource finalizer from an object
func RemoveSyncedResourceFinalizerWithClusterID(ctx context.Context, obj client.Object, virtualClient client.Client, logger logr.Logger, clusterID string) error {
	logger = logger.WithValues("resourceKind", obj.GetObjectKind().GroupVersionKind().Kind, "resource", fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName()))

	clusterSpecificFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, clusterID)

	// Check if finalizer exists
	if !controllerutil.ContainsFinalizer(obj, clusterSpecificFinalizer) {
		logger.V(1).Info("Cluster-specific finalizer not found, nothing to remove", "clusterID", clusterID)
		return nil
	}

	// Remove finalizer
	updatedObj := obj.DeepCopyObject().(client.Object)
	controllerutil.RemoveFinalizer(updatedObj, clusterSpecificFinalizer)

	// Update the resource
	if err := virtualClient.Update(ctx, updatedObj); err != nil {
		logger.Error(err, "Failed to remove cluster-specific finalizer from virtual resource", "clusterID", clusterID)
		return err
	}

	logger.Info("Successfully removed cluster-specific finalizer from virtual resource", "clusterID", clusterID)
	return nil
}

// GetManagedByClusterIDLabel returns the cluster-specific managed-by label key
func GetManagedByClusterIDLabel(clusterID string) string {
	return fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterID)
}
