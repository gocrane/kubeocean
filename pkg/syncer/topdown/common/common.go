package topcommon

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

// ResourceType represents the type of Kubernetes resource
type ResourceType string

const (
	ResourceTypeConfigMap ResourceType = "ConfigMap"
	ResourceTypeSecret    ResourceType = "Secret"
	ResourceTypePVC       ResourceType = "PVC"
	ResourceTypePV        ResourceType = "PV"
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

	// Add Kubeocean managed-by label
	labels[cloudv1beta1.LabelManagedBy] = cloudv1beta1.LabelManagedByValue

	return labels
}

// BuildPhysicalResourceLabelsWithClusterID builds labels for physical resources with cluster-specific labels
// This is a common function that can be used by different reconcilers
func BuildPhysicalResourceLabelsWithClusterID(virtualObj client.Object, clusterID string) map[string]string {
	labels := BuildPhysicalResourceLabels(virtualObj)

	// Add cluster-specific managed-by label
	managedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterID)
	labels[managedByClusterIDLabel] = cloudv1beta1.LabelValueTrue

	return labels
}

// BuildPhysicalResourceAnnotations builds annotations for physical resources
// This is a common function that can be used by different reconcilers
func BuildPhysicalResourceAnnotations(virtualObj client.Object) map[string]string {
	annotations := make(map[string]string)

	// Copy all annotations from virtual resource (excluding Kubeocean internal ones)
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
				labels[cloudv1beta1.LabelUsedByPV] = cloudv1beta1.LabelValueTrue
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

// RemoveSyncedResourceFinalizerAndLabels removes the cluster-specific synced resource finalizer,
// related labels and annotations from a virtual resource that were added during sync process
func RemoveSyncedResourceFinalizerAndLabels(ctx context.Context, obj client.Object, virtualClient client.Client, logger logr.Logger, clusterID string) error {
	logger = logger.WithValues("resourceKind", obj.GetObjectKind().GroupVersionKind().Kind, "resource", fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName()))

	clusterSpecificFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, clusterID)
	managedByClusterIDLabel := GetManagedByClusterIDLabel(clusterID)

	// Check what needs to be removed
	finalizerExists := controllerutil.ContainsFinalizer(obj, clusterSpecificFinalizer)
	clusterIDLabelExists := obj.GetLabels() != nil && obj.GetLabels()[managedByClusterIDLabel] != ""
	usedByPVLabelExists := obj.GetLabels() != nil && obj.GetLabels()[cloudv1beta1.LabelUsedByPV] != ""
	physicalNameAnnotationExists := obj.GetAnnotations() != nil && obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalName] != ""
	physicalNamespaceAnnotationExists := obj.GetAnnotations() != nil && obj.GetAnnotations()[cloudv1beta1.AnnotationPhysicalNamespace] != ""

	// If nothing needs to be removed, return early
	if !finalizerExists && !clusterIDLabelExists && !usedByPVLabelExists && !physicalNameAnnotationExists && !physicalNamespaceAnnotationExists {
		logger.V(1).Info("No synced resource finalizer, labels or annotations found, nothing to remove", "clusterID", clusterID)
		return nil
	}

	// Create a copy of the object to modify
	updatedObj := obj.DeepCopyObject().(client.Object)

	// Remove finalizer
	if finalizerExists {
		controllerutil.RemoveFinalizer(updatedObj, clusterSpecificFinalizer)
		logger.V(1).Info("Removed cluster-specific finalizer", "clusterID", clusterID)
	}

	// Remove labels
	if updatedObj.GetLabels() != nil {
		labelsToRemove := []string{managedByClusterIDLabel, cloudv1beta1.LabelUsedByPV}

		// Check if this is the last FinalizerClusterIDPrefix finalizer
		// If so, also remove LabelManagedBy=LabelManagedByValue
		if finalizerExists {
			remainingClusterFinalizers := 0
			for _, finalizer := range updatedObj.GetFinalizers() {
				if strings.HasPrefix(finalizer, cloudv1beta1.FinalizerClusterIDPrefix) && finalizer != clusterSpecificFinalizer {
					remainingClusterFinalizers++
				}
			}

			// If this is the last cluster-specific finalizer, remove LabelManagedBy
			if remainingClusterFinalizers == 0 {
				labelsToRemove = append(labelsToRemove, cloudv1beta1.LabelManagedBy)
				logger.V(1).Info("This is the last cluster-specific finalizer, will also remove LabelManagedBy", "clusterID", clusterID)
			}
		}

		for _, labelKey := range labelsToRemove {
			if _, exists := updatedObj.GetLabels()[labelKey]; exists {
				delete(updatedObj.GetLabels(), labelKey)
				logger.V(1).Info("Removed label", "labelKey", labelKey)
			}
		}

		// If labels map is now empty, set it to nil to clean up
		if len(updatedObj.GetLabels()) == 0 {
			updatedObj.SetLabels(nil)
		}
	}

	// Remove annotations
	if updatedObj.GetAnnotations() != nil {
		annotationsToRemove := []string{
			cloudv1beta1.AnnotationPhysicalName,
			cloudv1beta1.AnnotationPhysicalNamespace,
			cloudv1beta1.GetClusterBindingDeletingAnnotation(clusterID), // Remove cluster-specific deleting annotation
		}
		for _, annotationKey := range annotationsToRemove {
			if _, exists := updatedObj.GetAnnotations()[annotationKey]; exists {
				delete(updatedObj.GetAnnotations(), annotationKey)
				logger.V(1).Info("Removed annotation", "annotationKey", annotationKey)
			}
		}

		// If annotations map is now empty, set it to nil to clean up
		if len(updatedObj.GetAnnotations()) == 0 {
			updatedObj.SetAnnotations(nil)
		}
	}

	// Update the resource
	if err := virtualClient.Update(ctx, updatedObj); err != nil {
		logger.Error(err, "Failed to remove synced resource finalizer, labels and annotations from virtual resource", "clusterID", clusterID)
		return err
	}

	logger.Info("Successfully removed synced resource finalizer, labels and annotations from virtual resource", "clusterID", clusterID)
	return nil
}

// DeletePhysicalResourceParams contains parameters for deleting physical resources
type DeletePhysicalResourceParams struct {
	ResourceType      ResourceType
	PhysicalName      string
	PhysicalNamespace string
	PhysicalResource  client.Object
	PhysicalClient    client.Client
	Logger            logr.Logger
}

// DeletePhysicalResource deletes a physical resource with UID precondition
// This is a common function that can be used by different reconcilers for handling virtual resource deletion
func DeletePhysicalResource(ctx context.Context, params DeletePhysicalResourceParams) error {
	if params.PhysicalResource == nil {
		params.Logger.V(1).Info(fmt.Sprintf("Physical %s doesn't exist, nothing to delete", params.ResourceType))
		return nil
	}

	// Create the appropriate resource object for deletion
	var deleteObj client.Object
	switch params.ResourceType {
	case ResourceTypeConfigMap:
		deleteObj = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      params.PhysicalName,
				Namespace: params.PhysicalNamespace,
			},
		}
	case ResourceTypeSecret:
		deleteObj = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      params.PhysicalName,
				Namespace: params.PhysicalNamespace,
			},
		}
	case ResourceTypePVC:
		deleteObj = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      params.PhysicalName,
				Namespace: params.PhysicalNamespace,
			},
		}
	case ResourceTypePV:
		deleteObj = &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: params.PhysicalName,
			},
		}
	default:
		return fmt.Errorf("unsupported resource type: %s", params.ResourceType)
	}

	// Delete physical resource with UID precondition
	uid := params.PhysicalResource.GetUID()
	err := params.PhysicalClient.Delete(ctx, deleteObj, &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID: &uid,
		},
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			params.Logger.V(1).Info(fmt.Sprintf("Physical %s already deleted", params.ResourceType))
			return nil
		}
		params.Logger.Error(err, fmt.Sprintf("Failed to delete physical %s", params.ResourceType))
		return err
	}

	var resourcePath string
	if params.PhysicalNamespace != "" {
		resourcePath = fmt.Sprintf("%s/%s", params.PhysicalNamespace, params.PhysicalName)
	} else {
		resourcePath = params.PhysicalName
	}
	params.Logger.Info(fmt.Sprintf("Successfully deleted physical %s", params.ResourceType),
		fmt.Sprintf("physical%s", params.ResourceType), resourcePath)

	return nil
}

// AddClusterBindingDeletingAnnotation adds the cluster-specific deleting annotation to a virtual resource
func AddClusterBindingDeletingAnnotation(ctx context.Context, obj client.Object, virtualClient client.Client, logger logr.Logger, clusterID, clusterBindingName string) error {
	logger = logger.WithValues("resourceKind", obj.GetObjectKind().GroupVersionKind().Kind, "resource", fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName()))

	annotationKey := cloudv1beta1.GetClusterBindingDeletingAnnotation(clusterID)

	// Check if annotation already exists
	if obj.GetAnnotations() != nil && obj.GetAnnotations()[annotationKey] == clusterBindingName {
		logger.V(1).Info("ClusterBinding deleting annotation already exists, skipping")
		return nil
	}

	// Create a copy of the object to modify
	updatedObj := obj.DeepCopyObject().(client.Object)

	// Add clusterbinding-deleting annotation
	if updatedObj.GetAnnotations() == nil {
		updatedObj.SetAnnotations(make(map[string]string))
	}
	updatedObj.GetAnnotations()[annotationKey] = clusterBindingName

	// Update the resource
	if err := virtualClient.Update(ctx, updatedObj); err != nil {
		logger.Error(err, "Failed to add clusterbinding-deleting annotation", "clusterID", clusterID, "clusterBindingName", clusterBindingName)
		return fmt.Errorf("failed to add clusterbinding-deleting annotation: %w", err)
	}

	logger.Info("Successfully added clusterbinding-deleting annotation", "clusterID", clusterID, "clusterBindingName", clusterBindingName)
	return nil
}
