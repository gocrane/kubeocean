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

// VirtualPVReconciler reconciles PersistentVolume objects from virtual cluster
type VirtualPVReconciler struct {
	VirtualClient     client.Client
	PhysicalClient    client.Client
	PhysicalK8sClient kubernetes.Interface // Direct k8s client for bypassing cache
	Scheme            *runtime.Scheme
	ClusterBinding    *cloudv1beta1.ClusterBinding
	Log               logr.Logger
	clusterID         string // Cached cluster ID for performance
}

//+kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the main reconciliation logic for virtual PVs
func (r *VirtualPVReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPV", req.NamespacedName)

	// 1. Get the virtual PV
	virtualPV := &corev1.PersistentVolume{}
	err := r.VirtualClient.Get(ctx, req.NamespacedName, virtualPV)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Virtual PV not found, doing nothing")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual PV")
		return ctrl.Result{}, err
	}

	// 2. Check if PV is managed by Tapestry
	if virtualPV.Labels == nil || virtualPV.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		logger.V(1).Info("PV not managed by Tapestry, skipping")
		return ctrl.Result{}, nil
	}

	// 2.5. Check if PV belongs to this cluster
	managedByClusterIDLabel := GetManagedByClusterIDLabel(r.clusterID)
	if virtualPV.Labels == nil || virtualPV.Labels[managedByClusterIDLabel] != "true" {
		logger.V(1).Info("PV not managed by this cluster, skipping", "clusterID", r.clusterID)
		return ctrl.Result{}, nil
	}

	// 3. Get physical name from annotations
	physicalName := virtualPV.Annotations[cloudv1beta1.AnnotationPhysicalName]
	if physicalName == "" {
		logger.V(1).Info("PV has no physical name annotation, skipping")
		return ctrl.Result{}, nil
	}

	// 4. Check if physical PV exists
	physicalPVExists, physicalPV, err := r.checkPhysicalPVExists(ctx, physicalName)
	if err != nil {
		logger.Error(err, "Failed to check physical PV existence")
		return ctrl.Result{}, err
	}

	// 4.5. Validate physical PV if it exists
	if physicalPVExists {
		if err := r.validatePhysicalPV(virtualPV, physicalPV); err != nil {
			logger.Error(err, "Physical PV validation failed")
			return ctrl.Result{}, err
		}
	}

	// 5. Check if virtual PV is being deleted
	if virtualPV.DeletionTimestamp != nil {
		logger.Info("Virtual PV is being deleted, handling deletion")
		return r.handleVirtualPVDeletion(ctx, virtualPV, physicalName, physicalPVExists, physicalPV)
	}

	// 6. If physical PV doesn't exist, do nothing (PVs are not created by Tapestry)
	if !physicalPVExists {
		logger.V(1).Info("Physical PV doesn't exist, but PVs are not created by Tapestry, doing nothing")
		return ctrl.Result{}, nil
	}

	// 7. Physical PV exists, no update needed (PV spec is immutable)
	logger.V(1).Info("Physical PV exists, no update needed")
	return ctrl.Result{}, nil
}

// handleVirtualPVDeletion handles deletion of virtual PV
func (r *VirtualPVReconciler) handleVirtualPVDeletion(ctx context.Context, virtualPV *corev1.PersistentVolume, physicalName string, physicalPVExists bool, physicalPV *corev1.PersistentVolume) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualPV", fmt.Sprintf("/%s", virtualPV.Name))

	if !physicalPVExists {
		logger.V(1).Info("Physical PV doesn't exist, nothing to delete")
		return r.removeSyncedResourceFinalizer(ctx, virtualPV)
	}

	// Delete physical PV with UID precondition
	err := r.PhysicalClient.Delete(ctx, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: physicalName,
		},
	}, &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID: &physicalPV.UID,
		},
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Physical PV already deleted")
			return r.removeSyncedResourceFinalizer(ctx, virtualPV)
		}
		logger.Error(err, "Failed to delete physical PV")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully deleted physical PV", "physicalPV", fmt.Sprintf("/%s", physicalName))

	return r.removeSyncedResourceFinalizer(ctx, virtualPV)
}

// checkPhysicalPVExists checks if physical PV exists using both cached and direct client
func (r *VirtualPVReconciler) checkPhysicalPVExists(ctx context.Context, physicalName string) (bool, *corev1.PersistentVolume, error) {
	exists, obj, err := CheckPhysicalResourceExists(ctx, ResourceTypePV, physicalName, "", &corev1.PersistentVolume{},
		r.PhysicalClient, r.PhysicalK8sClient, r.Log)

	if err != nil {
		return false, nil, err
	}

	if !exists {
		return false, nil, nil
	}

	pv, ok := obj.(*corev1.PersistentVolume)
	if !ok {
		return false, nil, fmt.Errorf("expected PersistentVolume but got %T", obj)
	}

	return true, pv, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualPVReconciler) SetupWithManager(virtualManager, physicalManager ctrl.Manager) error {
	// Cache cluster ID for performance
	r.clusterID = r.ClusterBinding.Spec.ClusterID

	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("virtualpv-%s", r.ClusterBinding.Name)

	return ctrl.NewControllerManagedBy(virtualManager).
		For(&corev1.PersistentVolume{}).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
		}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			pv := obj.(*corev1.PersistentVolume)

			// Only sync PVs managed by Tapestry
			if pv.Labels == nil || pv.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
				return false
			}

			// Only sync PVs with physical name annotation
			if pv.Annotations == nil || pv.Annotations[cloudv1beta1.AnnotationPhysicalName] == "" {
				return false
			}

			// Only sync PVs managed by this cluster
			managedByClusterIDLabel := GetManagedByClusterIDLabel(r.clusterID)
			return pv.Labels[managedByClusterIDLabel] == "true"
		})).
		Complete(r)
}

// validatePhysicalPV validates that the physical PV is correctly managed by Tapestry
func (r *VirtualPVReconciler) validatePhysicalPV(virtualPV *corev1.PersistentVolume, physicalPV *corev1.PersistentVolume) error {
	// Check if physical PV is managed by Tapestry
	if physicalPV.Labels == nil || physicalPV.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		return fmt.Errorf("physical PV %s is not managed by Tapestry", physicalPV.Name)
	}

	// Check if physical PV's virtual name annotation points to the current virtual PV
	if physicalPV.Annotations == nil || physicalPV.Annotations[cloudv1beta1.AnnotationVirtualName] != virtualPV.Name {
		return fmt.Errorf("physical PV %s virtual name annotation does not point to current virtual PV %s",
			physicalPV.Name, virtualPV.Name)
	}

	return nil
}

// removeSyncedResourceFinalizer removes the synced-resource finalizer from the virtual PV
func (r *VirtualPVReconciler) removeSyncedResourceFinalizer(ctx context.Context, virtualPV *corev1.PersistentVolume) (ctrl.Result, error) {
	return ctrl.Result{}, RemoveSyncedResourceFinalizerWithClusterID(ctx, virtualPV, r.VirtualClient, r.Log, r.clusterID)
}
