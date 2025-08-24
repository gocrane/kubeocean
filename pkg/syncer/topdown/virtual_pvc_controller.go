package topdown

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

// VirtualPVCReconciler reconciles PersistentVolumeClaim objects from virtual cluster
type VirtualPVCReconciler struct {
	VirtualClient     client.Client
	PhysicalClient    client.Client
	PhysicalK8sClient kubernetes.Interface // Direct k8s client for bypassing cache
	Scheme            *runtime.Scheme
	ClusterBinding    *cloudv1beta1.ClusterBinding
	Log               logr.Logger
}

//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the main reconciliation logic for virtual PVCs
func (r *VirtualPVCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPVC", req.NamespacedName)

	// 1. Get the virtual PVC
	virtualPVC := &corev1.PersistentVolumeClaim{}
	err := r.VirtualClient.Get(ctx, req.NamespacedName, virtualPVC)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Virtual PVC not found, doing nothing")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual PVC")
		return ctrl.Result{}, err
	}

	// 2. Check if PVC is managed by Tapestry
	if virtualPVC.Labels == nil || virtualPVC.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		logger.V(1).Info("PVC not managed by Tapestry, skipping")
		return ctrl.Result{}, nil
	}

	// 3. Get physical name from annotations
	physicalName := virtualPVC.Annotations[cloudv1beta1.AnnotationPhysicalName]
	if physicalName == "" {
		logger.V(1).Info("PVC has no physical name annotation, skipping")
		return ctrl.Result{}, nil
	}

	// 4. Check if physical PVC exists
	physicalPVCExists, physicalPVC, err := r.checkPhysicalPVCExists(ctx, physicalName)
	if err != nil {
		logger.Error(err, "Failed to check physical PVC existence")
		return ctrl.Result{}, err
	}

	// 4.5. Validate physical PVC if it exists
	if physicalPVCExists {
		if err := r.validatePhysicalPVC(virtualPVC, physicalPVC); err != nil {
			logger.Error(err, "Physical PVC validation failed")
			return ctrl.Result{}, err
		}
	}

	// 5. Check if virtual PVC is being deleted
	if virtualPVC.DeletionTimestamp != nil {
		logger.Info("Virtual PVC is being deleted, handling deletion")
		return r.handleVirtualPVCDeletion(ctx, virtualPVC, physicalName, physicalPVCExists, physicalPVC)
	}

	// 6. If physical PVC doesn't exist, create it
	if !physicalPVCExists {
		logger.Info("Physical PVC doesn't exist, creating it")
		return r.createPhysicalPVC(ctx, virtualPVC, physicalName)
	}

	// 7. Physical PVC exists, no update needed (PVC spec is immutable)
	logger.V(1).Info("Physical PVC exists, no update needed")
	return ctrl.Result{}, nil
}

// handleVirtualPVCDeletion handles deletion of virtual PVC
func (r *VirtualPVCReconciler) handleVirtualPVCDeletion(ctx context.Context, virtualPVC *corev1.PersistentVolumeClaim, physicalName string, physicalPVCExists bool, physicalPVC *corev1.PersistentVolumeClaim) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPVC", fmt.Sprintf("%s/%s", virtualPVC.Namespace, virtualPVC.Name))

	if !physicalPVCExists {
		logger.V(1).Info("Physical PVC doesn't exist, nothing to delete")
		return r.removeSyncedResourceFinalizer(ctx, virtualPVC)
	}

	// Delete physical PVC with UID precondition
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace
	err := r.PhysicalClient.Delete(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      physicalName,
			Namespace: physicalNamespace,
		},
	}, &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID: &physicalPVC.UID,
		},
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Physical PVC already deleted")
			return r.removeSyncedResourceFinalizer(ctx, virtualPVC)
		}
		logger.Error(err, "Failed to delete physical PVC")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully deleted physical PVC", "physicalPVC", fmt.Sprintf("%s/%s", physicalNamespace, physicalName))

	return r.removeSyncedResourceFinalizer(ctx, virtualPVC)
}

// checkPhysicalPVCExists checks if physical PVC exists using both cached and direct client
func (r *VirtualPVCReconciler) checkPhysicalPVCExists(ctx context.Context, physicalName string) (bool, *corev1.PersistentVolumeClaim, error) {
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	exists, obj, err := CheckPhysicalResourceExists(ctx, ResourceTypePVC, physicalName, physicalNamespace, &corev1.PersistentVolumeClaim{},
		r.PhysicalClient, r.PhysicalK8sClient, r.Log)

	if err != nil {
		return false, nil, err
	}

	if !exists {
		return false, nil, nil
	}

	pvc, ok := obj.(*corev1.PersistentVolumeClaim)
	if !ok {
		return false, nil, fmt.Errorf("expected PersistentVolumeClaim but got %T", obj)
	}

	return true, pvc, nil
}

// createPhysicalPVC creates physical PVC
func (r *VirtualPVCReconciler) createPhysicalPVC(ctx context.Context, virtualPVC *corev1.PersistentVolumeClaim, physicalName string) (ctrl.Result, error) {
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	err := CreatePhysicalResource(ctx, ResourceTypePVC, virtualPVC, physicalName, physicalNamespace, r.PhysicalClient, r.Log)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualPVCReconciler) SetupWithManager(virtualManager, physicalManager ctrl.Manager) error {
	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("virtualpvc-%s", r.ClusterBinding.Name)

	return ctrl.NewControllerManagedBy(virtualManager).
		For(&corev1.PersistentVolumeClaim{}).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
		}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			pvc := obj.(*corev1.PersistentVolumeClaim)

			// Only sync PVCs managed by Tapestry
			if pvc.Labels == nil || pvc.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
				return false
			}

			// Only sync PVCs with physical name annotation
			if pvc.Annotations == nil || pvc.Annotations[cloudv1beta1.AnnotationPhysicalName] == "" {
				return false
			}

			return true
		})).
		Complete(r)
}

// validatePhysicalPVC validates that the physical PVC is correctly managed by Tapestry
func (r *VirtualPVCReconciler) validatePhysicalPVC(virtualPVC *corev1.PersistentVolumeClaim, physicalPVC *corev1.PersistentVolumeClaim) error {
	// Check if physical PVC is managed by Tapestry
	if physicalPVC.Labels == nil || physicalPVC.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		return fmt.Errorf("physical PVC %s/%s is not managed by Tapestry", physicalPVC.Namespace, physicalPVC.Name)
	}

	// Check if physical PVC's virtual name annotation points to the current virtual PVC
	if physicalPVC.Annotations == nil || physicalPVC.Annotations[cloudv1beta1.AnnotationVirtualName] != virtualPVC.Name {
		return fmt.Errorf("physical PVC %s/%s virtual name annotation does not point to current virtual PVC %s",
			physicalPVC.Namespace, physicalPVC.Name, virtualPVC.Name)
	}

	return nil
}

// removeSyncedResourceFinalizer removes the synced-resource finalizer from the virtual PVC
func (r *VirtualPVCReconciler) removeSyncedResourceFinalizer(ctx context.Context, virtualPVC *corev1.PersistentVolumeClaim) (ctrl.Result, error) {
	return ctrl.Result{}, RemoveSyncedResourceFinalizer(ctx, virtualPVC, r.VirtualClient, r.Log)
}
