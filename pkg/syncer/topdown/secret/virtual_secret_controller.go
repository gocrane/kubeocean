package secret

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	topcommon "github.com/gocrane/kubeocean/pkg/syncer/topdown/common"
)

// VirtualSecretReconciler reconciles Secret objects from virtual cluster
type VirtualSecretReconciler struct {
	VirtualClient     client.Client
	PhysicalClient    client.Client
	PhysicalK8sClient kubernetes.Interface // Direct k8s client for bypassing cache
	Scheme            *runtime.Scheme
	ClusterBinding    *cloudv1beta1.ClusterBinding
	Log               logr.Logger
	ClusterID         string // Cached cluster ID for performance

	// Cached secrets synced, key is virtual secret namespace/name, value is pod namespace/name that use it
	secretsNeedsSynced     map[string]map[string]struct{}
	secretsNeedsSyncedLock sync.RWMutex
	podSecretsRefs         map[string]map[string]bool
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
			logger.V(1).Info("Virtual Secret not found, removing from needs synced list")
			// if some pods need this secret, the pods needs to be recreated to sync the secret
			// TODO: consider deleting the physical secret if it is not used by any other pods
			r.UnsetSecretNeedsSynced(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual Secret")
		return ctrl.Result{}, err
	}

	// 2. Check if secret is managed by Kubeocean
	if r.shouldSkipSync(virtualSecret, logger) {
		logger.V(1).Info("Secret not managed by Kubeocean or not managed by this cluster, skipping")
		return ctrl.Result{}, nil
	}

	// 2.5. Update virtual resource labels and annotations if needed
	updated, err := r.updateVirtualSecretLabelsAndAnnotations(ctx, virtualSecret, logger)
	if err != nil {
		logger.Error(err, "Failed to update virtual resource labels and annotations")
		return ctrl.Result{}, err
	}
	if updated {
		logger.Info("Update virtual resource labels and annotations, wait for it updated")
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

	// 5. Check if virtual secret is being deleted or has clusterbinding-deleting annotation
	if virtualSecret.DeletionTimestamp != nil {
		logger.Info("Virtual Secret is being deleted, handling deletion")
		return r.handleVirtualSecretDeletion(ctx, virtualSecret, physicalNamespace, physicalName, physicalSecretExists, physicalSecret)
	}

	// 5.5. Check if clusterbinding is being deleted (indicated by annotation)
	clusterBindingDeletingAnnotation := cloudv1beta1.GetClusterBindingDeletingAnnotation(r.ClusterID)
	if virtualSecret.Annotations != nil && virtualSecret.Annotations[clusterBindingDeletingAnnotation] == r.ClusterBinding.Name {
		logger.Info("ClusterBinding is being deleted, handling Secret deletion")
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

func (r *VirtualSecretReconciler) shouldSkipSync(virtualSecret *corev1.Secret, logger logr.Logger) bool {
	managedByClusterIDLabel := topcommon.GetManagedByClusterIDLabel(r.ClusterID)
	if virtualSecret.Labels == nil || virtualSecret.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue || virtualSecret.Labels[managedByClusterIDLabel] != cloudv1beta1.LabelValueTrue {
		if !r.IsSecretNeedsSynced(virtualSecret.Namespace, virtualSecret.Name) {
			return true
		}
		logger.Info("Secret lacks managed labels, but still needs to be synced, syncing")
	}
	logger.V(1).Info("Secret is managed by Kubeocean and managed by this cluster, syncing")
	return false
}

func (r *VirtualSecretReconciler) updateVirtualSecretLabelsAndAnnotations(ctx context.Context, virtualSecret *corev1.Secret, logger logr.Logger) (bool, error) {
	if virtualSecret.Labels != nil && virtualSecret.Labels[cloudv1beta1.LabelUsedByPV] == cloudv1beta1.LabelValueTrue {
		logger.Info("Secret is used by PV, skipping update of labels and annotations")
		return false, nil
	}
	if !r.IsSecretNeedsSynced(virtualSecret.Namespace, virtualSecret.Name) {
		logger.V(1).Info("Secret does not need to be synced, skipping update")
		return false, nil
	}

	clusterSpecificFinalizer := topcommon.GetClusterSpecificFinalizer(r.ClusterID)
	managedByClusterIDLabel := topcommon.GetManagedByClusterIDLabel(r.ClusterID)
	if virtualSecret.Labels == nil || virtualSecret.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue || virtualSecret.Labels[managedByClusterIDLabel] != cloudv1beta1.LabelValueTrue ||
		virtualSecret.Annotations == nil || virtualSecret.Annotations[cloudv1beta1.AnnotationPhysicalName] == "" ||
		len(virtualSecret.Finalizers) == 0 || !controllerutil.ContainsFinalizer(virtualSecret, clusterSpecificFinalizer) {
		logger.Info("Secret lacks managed labels, annotations, finalizer or physical name, updating labels and annotations")
		// generate physical name and namespace
		physicalName := topcommon.GeneratePhysicalResourceName(virtualSecret.Name, virtualSecret.Namespace)
		physicalNamespace := r.ClusterBinding.Spec.MountNamespace
		err := topcommon.UpdateVirtualResourceLabelsAndAnnotations(ctx, r.VirtualClient, r.Log, r.ClusterID, virtualSecret, physicalName, physicalNamespace, nil)
		if err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

func (r *VirtualSecretReconciler) shouldSecretNeedsSynced(secret *corev1.Secret) bool {
	managedByClusterIDLabel := topcommon.GetManagedByClusterIDLabel(r.ClusterID)
	if secret.Labels == nil || secret.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue || secret.Labels[managedByClusterIDLabel] != cloudv1beta1.LabelValueTrue {
		if !r.IsSecretNeedsSynced(secret.Namespace, secret.Name) {
			return false
		}
	}
	return true
}

func (r *VirtualSecretReconciler) IsSecretNeedsSynced(namespace, name string) bool {
	r.secretsNeedsSyncedLock.RLock()
	defer r.secretsNeedsSyncedLock.RUnlock()
	_, ok := r.secretsNeedsSynced[fmt.Sprintf("%s/%s", namespace, name)]
	return ok
}

func (r *VirtualSecretReconciler) AddSecretsNeedsSynced(namespace string, names map[string]bool, podNamespace, podName string) {
	r.secretsNeedsSyncedLock.Lock()
	defer r.secretsNeedsSyncedLock.Unlock()
	if len(names) == 0 {
		return
	}
	podKey := fmt.Sprintf("%s/%s", podNamespace, podName)
	for name := range names {
		key := fmt.Sprintf("%s/%s", namespace, name)
		if _, ok := r.secretsNeedsSynced[key]; !ok {
			r.secretsNeedsSynced[key] = make(map[string]struct{})
		}
		r.secretsNeedsSynced[key][podKey] = struct{}{}
	}
	r.podSecretsRefs[podKey] = names
}

func (r *VirtualSecretReconciler) DeleteSecretsNeedsSynced(podNamespace, podName string) {
	r.secretsNeedsSyncedLock.Lock()
	defer r.secretsNeedsSyncedLock.Unlock()
	podKey := fmt.Sprintf("%s/%s", podNamespace, podName)
	names := r.podSecretsRefs[podKey]
	for name := range names {
		delete(r.secretsNeedsSynced[fmt.Sprintf("%s/%s", podNamespace, name)], podKey)
		// TODO: consider deleting the physical secret if it is not used by any other pods
		// if len(r.secretsNeedsSynced[fmt.Sprintf("%s/%s", namespace, name)]) == 0 {
		//	 delete(r.secretsNeedsSynced, fmt.Sprintf("%s/%s", namespace, name))
		// }
	}
	delete(r.podSecretsRefs, podKey)
}

func (r *VirtualSecretReconciler) UnsetSecretNeedsSynced(namespace, name string) {
	r.secretsNeedsSyncedLock.Lock()
	defer r.secretsNeedsSyncedLock.Unlock()
	delete(r.secretsNeedsSynced, fmt.Sprintf("%s/%s", namespace, name))
}

// handleVirtualSecretDeletion handles deletion of virtual secret
func (r *VirtualSecretReconciler) handleVirtualSecretDeletion(ctx context.Context, virtualSecret *corev1.Secret, physicalNamespace string, physicalName string, physicalSecretExists bool, physicalSecret *corev1.Secret) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualSecret", fmt.Sprintf("%s/%s", virtualSecret.Namespace, virtualSecret.Name))

	logger.V(1).Info("Unset secret needs synced")
	r.UnsetSecretNeedsSynced(virtualSecret.Namespace, virtualSecret.Name)

	if !physicalSecretExists {
		logger.V(1).Info("Physical Secret doesn't exist, nothing to delete")
		return ctrl.Result{}, topcommon.RemoveSyncedResourceFinalizerAndLabels(ctx, virtualSecret, r.VirtualClient, r.Log, r.ClusterID)
	}

	// Use the common deletion function
	err := topcommon.DeletePhysicalResource(ctx, topcommon.DeletePhysicalResourceParams{
		ResourceType:      topcommon.ResourceTypeSecret,
		PhysicalName:      physicalName,
		PhysicalNamespace: physicalNamespace,
		PhysicalResource:  physicalSecret,
		PhysicalClient:    r.PhysicalClient,
		Logger:            logger,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, topcommon.RemoveSyncedResourceFinalizerAndLabels(ctx, virtualSecret, r.VirtualClient, r.Log, r.ClusterID)
}

// checkPhysicalSecretExists checks if physical secret exists using both cached and direct client
func (r *VirtualSecretReconciler) checkPhysicalSecretExists(ctx context.Context, physicalNamespace, physicalName string) (bool, *corev1.Secret, error) {
	exists, obj, err := topcommon.CheckPhysicalResourceExists(ctx, topcommon.ResourceTypeSecret, physicalName, physicalNamespace, &corev1.Secret{},
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

	err := topcommon.CreatePhysicalResource(ctx, topcommon.ResourceTypeSecret, virtualSecret, physicalName, physicalNamespace, r.PhysicalClient, r.Log)
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
	updatedSecret.Labels = topcommon.BuildPhysicalResourceLabels(virtualSecret)
	updatedSecret.Annotations = topcommon.BuildPhysicalResourceAnnotations(virtualSecret)

	err := r.PhysicalClient.Update(ctx, updatedSecret)
	if err != nil {
		logger.Error(err, "Failed to update physical Secret")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully updated physical Secret", "physicalSecret", fmt.Sprintf("%s/%s", physicalSecret.Namespace, physicalName))
	return ctrl.Result{}, nil
}

// validatePhysicalSecret validates that the physical secret is correctly managed by Kubeocean
func (r *VirtualSecretReconciler) validatePhysicalSecret(virtualSecret *corev1.Secret, physicalSecret *corev1.Secret) error {
	// Check if physical secret is managed by Kubeocean
	if physicalSecret.Labels == nil || physicalSecret.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		return fmt.Errorf("physical Secret %s/%s is not managed by Kubeocean", physicalSecret.Namespace, physicalSecret.Name)
	}

	// Check if physical secret's virtual name annotation points to the current virtual secret
	if physicalSecret.Annotations == nil || physicalSecret.Annotations[cloudv1beta1.AnnotationVirtualName] != virtualSecret.Name {
		return fmt.Errorf("physical Secret %s/%s virtual name annotation does not point to current virtual Secret %s",
			physicalSecret.Namespace, physicalSecret.Name, virtualSecret.Name)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualSecretReconciler) SetupWithManager(virtualManager, physicalManager ctrl.Manager) error {
	// Cache cluster ID for performance
	r.ClusterID = r.ClusterBinding.Spec.ClusterID

	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("virtualsecret-%s", r.ClusterBinding.Name)

	r.secretsNeedsSynced = make(map[string]map[string]struct{})
	r.podSecretsRefs = make(map[string]map[string]bool)

	return ctrl.NewControllerManagedBy(virtualManager).
		For(&corev1.Secret{}).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
		}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			secret := obj.(*corev1.Secret)
			if secret == nil {
				return false
			}

			return r.shouldSecretNeedsSynced(secret)
		})).
		Complete(r)
}
