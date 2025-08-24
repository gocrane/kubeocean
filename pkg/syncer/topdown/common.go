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

// CheckPhysicalResourceExists checks if physical resource exists using both cached and direct client
// This is a common function that can be used by different reconcilers
func CheckPhysicalResourceExists(ctx context.Context, resourceType ResourceType, physicalName, physicalNamespace string, obj client.Object,
	physicalClient client.Client, physicalK8sClient kubernetes.Interface, logger logr.Logger) (bool, client.Object, error) {

	logger = logger.WithValues(fmt.Sprintf("physical%s", resourceType), physicalName)

	resourceKey := types.NamespacedName{Namespace: physicalNamespace, Name: physicalName}

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

// BuildPhysicalResourceAnnotations builds annotations for physical resources
// This is a common function that can be used by different reconcilers
func BuildPhysicalResourceAnnotations(virtualObj client.Object) map[string]string {
	annotations := make(map[string]string)

	// Copy all annotations from virtual resource (excluding Tapestry internal ones)
	for k, v := range virtualObj.GetAnnotations() {
		if k != cloudv1beta1.AnnotationPhysicalPodNamespace &&
			k != cloudv1beta1.AnnotationPhysicalPodName &&
			k != cloudv1beta1.AnnotationPhysicalPodUID &&
			k != cloudv1beta1.AnnotationLastSyncTime {
			annotations[k] = v
		}
	}

	// Add virtual resource mapping annotations
	annotations[cloudv1beta1.AnnotationVirtualNamespace] = virtualObj.GetNamespace()
	annotations[cloudv1beta1.AnnotationVirtualName] = virtualObj.GetName()

	return annotations
}

// CreatePhysicalResource creates a physical resource by type
// This is a common function that can be used by different reconcilers
func CreatePhysicalResource(ctx context.Context, resourceType ResourceType, virtualObj client.Object, physicalName, physicalNamespace string,
	physicalClient client.Client, logger logr.Logger) error {

	logger = logger.WithValues(string(resourceType), fmt.Sprintf("%s/%s", virtualObj.GetNamespace(), virtualObj.GetName()))

	// Build physical resource
	physicalObj, err := BuildPhysicalResource(resourceType, virtualObj, physicalName, physicalNamespace)
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

	logger.Info(fmt.Sprintf("Successfully created physical %s", resourceType),
		fmt.Sprintf("physical%s", resourceType), fmt.Sprintf("%s/%s", physicalNamespace, physicalName))
	return nil
}

// BuildPhysicalResource builds a physical resource by type
// This is a common function that can be used by different reconcilers
func BuildPhysicalResource(resourceType ResourceType, virtualObj client.Object, physicalName, physicalNamespace string) (client.Object, error) {
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
			physicalSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:        physicalName,
					Namespace:   physicalNamespace,
					Labels:      BuildPhysicalResourceLabels(secret),
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
				Spec: pvc.Spec,
			}
			return physicalPVC, nil
		}
		return nil, fmt.Errorf("invalid object type for PVC: %T", virtualObj)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

// RemoveSyncedResourceFinalizer removes the synced-resource finalizer from a virtual resource
// This is a common function that can be used by different reconcilers
func RemoveSyncedResourceFinalizer(ctx context.Context, virtualObj client.Object, virtualClient client.Client, logger logr.Logger) error {
	logger = logger.WithValues("resourceKind", virtualObj.GetObjectKind().GroupVersionKind().Kind, "resource", fmt.Sprintf("%s/%s", virtualObj.GetNamespace(), virtualObj.GetName()))

	// Check if finalizer exists
	if !controllerutil.ContainsFinalizer(virtualObj, cloudv1beta1.SyncedResourceFinalizer) {
		logger.V(1).Info("Finalizer not found, nothing to remove")
		return nil
	}

	// Remove finalizer
	updatedObj := virtualObj.DeepCopyObject().(client.Object)
	controllerutil.RemoveFinalizer(updatedObj, cloudv1beta1.SyncedResourceFinalizer)

	// Update the resource
	if err := virtualClient.Update(ctx, updatedObj); err != nil {
		logger.Error(err, "Failed to remove finalizer from virtual resource")
		return err
	}

	logger.Info("Successfully removed finalizer from virtual resource")
	return nil
}
