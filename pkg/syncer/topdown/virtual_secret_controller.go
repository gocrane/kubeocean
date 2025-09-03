package topdown

import (
	"context"
	"fmt"
	"reflect"

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

// VirtualSecretReconciler reconciles Secret objects from virtual cluster
type VirtualSecretReconciler struct {
	VirtualClient     client.Client
	PhysicalClient    client.Client
	PhysicalK8sClient kubernetes.Interface // Direct k8s client for bypassing cache
	Scheme            *runtime.Scheme
	ClusterBinding    *cloudv1beta1.ClusterBinding
	Log               logr.Logger
	clusterID         string // Cached cluster ID for performance
}

//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the main reconciliation logic for virtual secrets
func (r *VirtualSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualSecret", req.NamespacedName)

	// 1. Get the virtual secret
	virtualSecret := &corev1.Secret{}
	err := r.VirtualClient.Get(ctx, req.NamespacedName, virtualSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Virtual Secret not found, doing nothing")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual Secret")
		return ctrl.Result{}, err
	}

	// 2. Check if secret is managed by Tapestry
	if virtualSecret.Labels == nil || virtualSecret.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		logger.V(1).Info("Secret not managed by Tapestry, skipping")
		return ctrl.Result{}, nil
	}

	// 2.5. Check if secret belongs to this cluster
	managedByClusterIDLabel := GetManagedByClusterIDLabel(r.clusterID)
	if virtualSecret.Labels == nil || virtualSecret.Labels[managedByClusterIDLabel] != cloudv1beta1.LabelValueTrue {
		logger.V(1).Info("Secret not managed by this cluster, skipping", "clusterID", r.clusterID)
		return ctrl.Result{}, nil
	}

	// 3. Get physical name from annotations
	physicalName := virtualSecret.Annotations[cloudv1beta1.AnnotationPhysicalName]
	if physicalName == "" {
		logger.V(1).Info("Secret has no physical name annotation, skipping")
		return ctrl.Result{}, nil
	}
	physicalNamespace := virtualSecret.Annotations[cloudv1beta1.AnnotationPhysicalNamespace]
	if physicalNamespace == "" {
		logger.V(1).Info("Secret has no physical namespace annotation, skipping")
		return ctrl.Result{}, nil
	}

	// 4. Check if physical secret exists
	physicalSecretExists, physicalSecret, err := r.checkPhysicalSecretExists(ctx, physicalNamespace, physicalName)
	if err != nil {
		logger.Error(err, "Failed to check physical Secret existence")
		return ctrl.Result{}, err
	}

	// 4.5. Validate physical secret if it exists
	if physicalSecretExists {
		if err := r.validatePhysicalSecret(virtualSecret, physicalSecret); err != nil {
			logger.Error(err, "Physical Secret validation failed")
			return ctrl.Result{}, err
		}
	}

	// 5. Check if virtual secret is being deleted
	if virtualSecret.DeletionTimestamp != nil {
		logger.Info("Virtual Secret is being deleted, handling deletion")
		return r.handleVirtualSecretDeletion(ctx, virtualSecret, physicalNamespace, physicalName, physicalSecretExists, physicalSecret)
	}

	// 6. If physical secret doesn't exist, create it
	if !physicalSecretExists {
		if virtualSecret.Labels[cloudv1beta1.LabelUsedByPV] == cloudv1beta1.LabelValueTrue {
			logger.Info("Physical Secret doesn't exist, but it's used by PV, skip creating it")
			return ctrl.Result{}, nil
		}
		logger.Info("Physical Secret doesn't exist, creating it")
		return r.createPhysicalSecret(ctx, virtualSecret, physicalName)
	}

	// 7. Physical secret exists, check if update is needed
	logger.V(1).Info("Physical Secret exists, checking if update is needed")
	return r.updatePhysicalSecretIfNeeded(ctx, virtualSecret, physicalSecret, physicalName)
}

// handleVirtualSecretDeletion handles deletion of virtual secret
func (r *VirtualSecretReconciler) handleVirtualSecretDeletion(ctx context.Context, virtualSecret *corev1.Secret, physicalNamespace string, physicalName string, physicalSecretExists bool, physicalSecret *corev1.Secret) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualSecret", fmt.Sprintf("%s/%s", virtualSecret.Namespace, virtualSecret.Name))

	if !physicalSecretExists {
		logger.V(1).Info("Physical Secret doesn't exist, nothing to delete")
		return r.removeSyncedResourceFinalizer(ctx, virtualSecret)
	}

	// Delete physical secret with UID precondition
	err := r.PhysicalClient.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      physicalName,
			Namespace: physicalNamespace,
		},
	}, &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID: &physicalSecret.UID,
		},
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Physical Secret already deleted")
			return r.removeSyncedResourceFinalizer(ctx, virtualSecret)
		}
		logger.Error(err, "Failed to delete physical Secret")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully deleted physical Secret", "physicalSecret", fmt.Sprintf("%s/%s", physicalNamespace, physicalName))

	return r.removeSyncedResourceFinalizer(ctx, virtualSecret)
}

// checkPhysicalSecretExists checks if physical secret exists using both cached and direct client
func (r *VirtualSecretReconciler) checkPhysicalSecretExists(ctx context.Context, physicalNamespace, physicalName string) (bool, *corev1.Secret, error) {
	exists, obj, err := CheckPhysicalResourceExists(ctx, ResourceTypeSecret, physicalName, physicalNamespace, &corev1.Secret{},
		r.PhysicalClient, r.PhysicalK8sClient, r.Log)

	if err != nil {
		return false, nil, err
	}

	if !exists {
		return false, nil, nil
	}

	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return false, nil, fmt.Errorf("expected Secret but got %T", obj)
	}

	return true, secret, nil
}

// createPhysicalSecret creates physical secret
func (r *VirtualSecretReconciler) createPhysicalSecret(ctx context.Context, virtualSecret *corev1.Secret, physicalName string) (ctrl.Result, error) {
	physicalNamespace := r.ClusterBinding.Spec.MountNamespace

	err := CreatePhysicalResource(ctx, ResourceTypeSecret, virtualSecret, physicalName, physicalNamespace, r.PhysicalClient, r.Log)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// updatePhysicalSecretIfNeeded updates physical secret if it differs from virtual secret
func (r *VirtualSecretReconciler) updatePhysicalSecretIfNeeded(ctx context.Context, virtualSecret *corev1.Secret, physicalSecret *corev1.Secret, physicalName string) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualSecret", fmt.Sprintf("%s/%s", virtualSecret.Namespace, virtualSecret.Name))

	// Check if update is needed by comparing data and type
	if reflect.DeepEqual(virtualSecret.Data, physicalSecret.Data) &&
		reflect.DeepEqual(virtualSecret.StringData, physicalSecret.StringData) &&
		virtualSecret.Type == physicalSecret.Type {
		logger.V(1).Info("Physical Secret is up to date, no update needed")
		return ctrl.Result{}, nil
	}

	// Update physical secret
	updatedSecret := physicalSecret.DeepCopy()
	updatedSecret.Data = virtualSecret.Data
	updatedSecret.StringData = virtualSecret.StringData
	updatedSecret.Type = virtualSecret.Type
	updatedSecret.Labels = BuildPhysicalResourceLabels(virtualSecret)
	updatedSecret.Annotations = BuildPhysicalResourceAnnotations(virtualSecret)

	err := r.PhysicalClient.Update(ctx, updatedSecret)
	if err != nil {
		logger.Error(err, "Failed to update physical Secret")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully updated physical Secret", "physicalSecret", fmt.Sprintf("%s/%s", physicalSecret.Namespace, physicalName))
	return ctrl.Result{}, nil
}

// validatePhysicalSecret validates that the physical secret is correctly managed by Tapestry
func (r *VirtualSecretReconciler) validatePhysicalSecret(virtualSecret *corev1.Secret, physicalSecret *corev1.Secret) error {
	// Check if physical secret is managed by Tapestry
	if physicalSecret.Labels == nil || physicalSecret.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		return fmt.Errorf("physical Secret %s/%s is not managed by Tapestry", physicalSecret.Namespace, physicalSecret.Name)
	}

	// Check if physical secret's virtual name annotation points to the current virtual secret
	if physicalSecret.Annotations == nil || physicalSecret.Annotations[cloudv1beta1.AnnotationVirtualName] != virtualSecret.Name {
		return fmt.Errorf("physical Secret %s/%s virtual name annotation does not point to current virtual Secret %s",
			physicalSecret.Namespace, physicalSecret.Name, virtualSecret.Name)
	}

	return nil
}

// removeSyncedResourceFinalizer removes the synced-resource finalizer from the virtual secret
func (r *VirtualSecretReconciler) removeSyncedResourceFinalizer(ctx context.Context, virtualSecret *corev1.Secret) (ctrl.Result, error) {
	return ctrl.Result{}, RemoveSyncedResourceFinalizerWithClusterID(ctx, virtualSecret, r.VirtualClient, r.Log, r.clusterID)
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualSecretReconciler) SetupWithManager(virtualManager, physicalManager ctrl.Manager) error {
	// Cache cluster ID for performance
	r.clusterID = r.ClusterBinding.Spec.ClusterID

	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("virtualsecret-%s", r.ClusterBinding.Name)

	return ctrl.NewControllerManagedBy(virtualManager).
		For(&corev1.Secret{}).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
		}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			secret := obj.(*corev1.Secret)

			// Only sync secrets managed by Tapestry
			if secret.Labels == nil || secret.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
				return false
			}

			// Only sync secrets with physical name label
			if secret.Annotations == nil || secret.Annotations[cloudv1beta1.AnnotationPhysicalName] == "" {
				return false
			}

			// Only sync secrets managed by this cluster
			managedByClusterIDLabel := GetManagedByClusterIDLabel(r.clusterID)
			return secret.Labels[managedByClusterIDLabel] == cloudv1beta1.LabelValueTrue
		})).
		Complete(r)
}
