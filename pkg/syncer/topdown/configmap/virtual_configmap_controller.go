package configmap

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	topcommon "github.com/gocrane/kubeocean/pkg/syncer/topdown/common"
)

// VirtualConfigMapReconciler reconciles ConfigMap objects from virtual cluster
type VirtualConfigMapReconciler struct {
	VirtualClient     client.Client
	PhysicalClient    client.Client
	PhysicalK8sClient kubernetes.Interface // Direct k8s client for bypassing cache
	Scheme            *runtime.Scheme
	ClusterBinding    *cloudv1beta1.ClusterBinding
	Log               logr.Logger
	ClusterID         string // Cached cluster ID for performance
}

//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the main reconciliation logic for virtual configmaps
func (r *VirtualConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualConfigMap", req.NamespacedName)

	// 1. Get the virtual configmap
	virtualConfigMap := &corev1.ConfigMap{}
	err := r.VirtualClient.Get(ctx, req.NamespacedName, virtualConfigMap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Virtual ConfigMap not found, doing nothing")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual ConfigMap")
		return ctrl.Result{}, err
	}

	// 2. Check if configmap is managed by Kubeocean
	if virtualConfigMap.Labels == nil || virtualConfigMap.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		logger.V(1).Info("ConfigMap not managed by Kubeocean, skipping")
		return ctrl.Result{}, nil
	}

	// 2.5. Check if configmap belongs to this cluster
	managedByClusterIDLabel := topcommon.GetManagedByClusterIDLabel(r.ClusterID)
	if virtualConfigMap.Labels == nil || virtualConfigMap.Labels[managedByClusterIDLabel] != cloudv1beta1.LabelValueTrue {
		logger.V(1).Info("ConfigMap not managed by this cluster, skipping", "ClusterID", r.ClusterID)
		return ctrl.Result{}, nil
	}

	// 3. Get physical name from annotations
	physicalName := virtualConfigMap.Annotations[cloudv1beta1.AnnotationPhysicalName]
	if physicalName == "" {
		logger.V(1).Info("ConfigMap has no physical name annotation, skipping")
		return ctrl.Result{}, nil
	}

	// 4. Check if physical configmap exists
	physicalConfigMapExists, physicalConfigMap, err := r.checkPhysicalConfigMapExists(ctx, physicalName)
	if err != nil {
		logger.Error(err, "Failed to check physical ConfigMap existence")
		return ctrl.Result{}, err
	}

	// 4.5. Validate physical configmap if it exists
	if physicalConfigMapExists {
		if err := r.validatePhysicalConfigMap(virtualConfigMap, physicalConfigMap); err != nil {
			logger.Error(err, "Physical ConfigMap validation failed")
			return ctrl.Result{}, err
		}
	}

	// 5. Check if virtual configmap is being deleted or has clusterbinding-deleting annotation
	if virtualConfigMap.DeletionTimestamp != nil {
		logger.Info("Virtual ConfigMap is being deleted, handling deletion")
		return r.handleVirtualConfigMapDeletion(ctx, virtualConfigMap, physicalName, physicalConfigMapExists, physicalConfigMap)
	}

	// 5.5. Check if clusterbinding is being deleted (indicated by annotation)
	clusterBindingDeletingAnnotation := cloudv1beta1.GetClusterBindingDeletingAnnotation(r.ClusterID)
	if virtualConfigMap.Annotations != nil && virtualConfigMap.Annotations[clusterBindingDeletingAnnotation] == r.ClusterBinding.Name {
		logger.Info("ClusterBinding is being deleted, handling ConfigMap deletion")
		return r.handleVirtualConfigMapDeletion(ctx, virtualConfigMap, physicalName, physicalConfigMapExists, physicalConfigMap)
	}

	// 6. If physical configmap doesn't exist, create it
	if !physicalConfigMapExists {
		logger.Info("Physical ConfigMap doesn't exist, creating it")
		return r.createPhysicalConfigMap(ctx, virtualConfigMap, physicalName)
	}

	// 7. Physical configmap exists, check if update is needed
	logger.V(1).Info("Physical ConfigMap exists, checking if update is needed")
	return r.updatePhysicalConfigMapIfNeeded(ctx, virtualConfigMap, physicalConfigMap, physicalName)
}

// handleVirtualConfigMapDeletion handles deletion of virtual configmap
func (r *VirtualConfigMapReconciler) handleVirtualConfigMapDeletion(ctx context.Context, virtualConfigMap *corev1.ConfigMap, physicalName string, physicalConfigMapExists bool, physicalConfigMap *corev1.ConfigMap) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualConfigMap", fmt.Sprintf("%s/%s", virtualConfigMap.Namespace, virtualConfigMap.Name))

	if !physicalConfigMapExists {
		logger.V(1).Info("Physical ConfigMap doesn't exist, nothing to delete")
		return ctrl.Result{}, topcommon.RemoveSyncedResourceFinalizerAndLabels(ctx, virtualConfigMap, r.VirtualClient, r.Log, r.ClusterID)
	}

	// Use the common deletion function
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace
	err := topcommon.DeletePhysicalResource(ctx, topcommon.DeletePhysicalResourceParams{
		ResourceType:      topcommon.ResourceTypeConfigMap,
		PhysicalName:      physicalName,
		PhysicalNamespace: physicalNamespace,
		PhysicalResource:  physicalConfigMap,
		PhysicalClient:    r.PhysicalClient,
		Logger:            logger,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, topcommon.RemoveSyncedResourceFinalizerAndLabels(ctx, virtualConfigMap, r.VirtualClient, r.Log, r.ClusterID)
}

// checkPhysicalConfigMapExists checks if physical configmap exists using both cached and direct client
func (r *VirtualConfigMapReconciler) checkPhysicalConfigMapExists(ctx context.Context, physicalName string) (bool, *corev1.ConfigMap, error) {
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	exists, obj, err := topcommon.CheckPhysicalResourceExists(ctx, topcommon.ResourceTypeConfigMap, physicalName, physicalNamespace, &corev1.ConfigMap{},
		r.PhysicalClient, r.PhysicalK8sClient, r.Log)

	if err != nil {
		return false, nil, err
	}

	if !exists {
		return false, nil, nil
	}

	configMap, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return false, nil, fmt.Errorf("expected ConfigMap but got %T", obj)
	}

	return true, configMap, nil
}

// createPhysicalConfigMap creates physical configmap
func (r *VirtualConfigMapReconciler) createPhysicalConfigMap(ctx context.Context, virtualConfigMap *corev1.ConfigMap, physicalName string) (ctrl.Result, error) {
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	err := topcommon.CreatePhysicalResource(ctx, topcommon.ResourceTypeConfigMap, virtualConfigMap, physicalName, physicalNamespace, r.PhysicalClient, r.Log)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// updatePhysicalConfigMapIfNeeded updates physical configmap if it differs from virtual configmap
func (r *VirtualConfigMapReconciler) updatePhysicalConfigMapIfNeeded(ctx context.Context, virtualConfigMap *corev1.ConfigMap, physicalConfigMap *corev1.ConfigMap, physicalName string) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualConfigMap", fmt.Sprintf("%s/%s", virtualConfigMap.Namespace, virtualConfigMap.Name))

	// Check if update is needed by comparing data
	if reflect.DeepEqual(virtualConfigMap.Data, physicalConfigMap.Data) &&
		reflect.DeepEqual(virtualConfigMap.BinaryData, physicalConfigMap.BinaryData) {
		logger.V(1).Info("Physical ConfigMap is up to date, no update needed")
		return ctrl.Result{}, nil
	}

	// Update physical configmap
	updatedConfigMap := physicalConfigMap.DeepCopy()
	updatedConfigMap.Data = virtualConfigMap.Data
	updatedConfigMap.BinaryData = virtualConfigMap.BinaryData
	updatedConfigMap.Labels = topcommon.BuildPhysicalResourceLabels(virtualConfigMap)
	updatedConfigMap.Annotations = topcommon.BuildPhysicalResourceAnnotations(virtualConfigMap)

	err := r.PhysicalClient.Update(ctx, updatedConfigMap)
	if err != nil {
		logger.Error(err, "Failed to update physical ConfigMap")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully updated physical ConfigMap", "physicalConfigMap", fmt.Sprintf("%s/%s", physicalConfigMap.Namespace, physicalName))
	return ctrl.Result{}, nil
}

// validatePhysicalConfigMap validates that the physical configmap is correctly managed by Kubeocean
func (r *VirtualConfigMapReconciler) validatePhysicalConfigMap(virtualConfigMap *corev1.ConfigMap, physicalConfigMap *corev1.ConfigMap) error {
	// Check if physical configmap is managed by Kubeocean
	if physicalConfigMap.Labels == nil || physicalConfigMap.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		return fmt.Errorf("physical ConfigMap %s/%s is not managed by Kubeocean", physicalConfigMap.Namespace, physicalConfigMap.Name)
	}

	// Check if physical configmap's virtual name annotation points to the current virtual configmap
	if physicalConfigMap.Annotations == nil || physicalConfigMap.Annotations[cloudv1beta1.AnnotationVirtualName] != virtualConfigMap.Name {
		return fmt.Errorf("physical ConfigMap %s/%s virtual name annotation does not point to current virtual ConfigMap %s",
			physicalConfigMap.Namespace, physicalConfigMap.Name, virtualConfigMap.Name)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualConfigMapReconciler) SetupWithManager(virtualManager, physicalManager ctrl.Manager) error {
	// Cache cluster ID for performance
	r.ClusterID = r.ClusterBinding.Spec.ClusterID

	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("virtualconfigmap-%s", r.ClusterBinding.Name)

	return ctrl.NewControllerManagedBy(virtualManager).
		For(&corev1.ConfigMap{}).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
		}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			configMap := obj.(*corev1.ConfigMap)

			// Only sync configmaps managed by Kubeocean
			if configMap.Labels == nil || configMap.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
				return false
			}

			// Only sync configmaps with physical name label
			if configMap.Annotations == nil || configMap.Annotations[cloudv1beta1.AnnotationPhysicalName] == "" {
				return false
			}

			// Only sync configmaps managed by this cluster
			managedByClusterIDLabel := topcommon.GetManagedByClusterIDLabel(r.ClusterID)
			return configMap.Labels[managedByClusterIDLabel] == cloudv1beta1.LabelValueTrue
		})).
		Complete(r)
}
