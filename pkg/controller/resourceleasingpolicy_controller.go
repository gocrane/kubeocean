package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

// ResourceLeasingPolicyReconciler reconciles a ResourceLeasingPolicy object
type ResourceLeasingPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=cloud.tencent.com,resources=resourceleasingpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cloud.tencent.com,resources=resourceleasingpolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cloud.tencent.com,resources=resourceleasingpolicies/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ResourceLeasingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("resourceleasingpolicy", req.NamespacedName)

	// Fetch the ResourceLeasingPolicy instance
	policy := &cloudv1beta1.ResourceLeasingPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ResourceLeasingPolicy resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch ResourceLeasingPolicy")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ResourceLeasingPolicy", "name", policy.Name, "namespace", policy.Namespace)

	// Record event for reconciliation start
	r.Recorder.Event(policy, corev1.EventTypeNormal, "Reconciling", "Starting ResourceLeasingPolicy reconciliation")

	// Validate the ResourceLeasingPolicy configuration
	if err := r.validateResourceLeasingPolicy(policy); err != nil {
		log.Error(err, "ResourceLeasingPolicy validation failed")
		r.Recorder.Event(policy, corev1.EventTypeWarning, "ValidationFailed", fmt.Sprintf("Validation failed: %v", err))
		return ctrl.Result{}, err
	}

	// Validate that the referenced ClusterBinding exists
	if err := r.validateClusterBindingReference(ctx, policy); err != nil {
		log.Error(err, "ClusterBinding reference validation failed")
		r.Recorder.Event(policy, corev1.EventTypeWarning, "ClusterBindingNotFound", fmt.Sprintf("Referenced ClusterBinding not found: %v", err))
		return ctrl.Result{}, err
	}

	// TODO: Implement full reconciliation logic in later tasks
	r.Recorder.Event(policy, corev1.EventTypeNormal, "Ready", "ResourceLeasingPolicy is ready")

	log.Info("ResourceLeasingPolicy reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// validateResourceLeasingPolicy validates the ResourceLeasingPolicy configuration
func (r *ResourceLeasingPolicyReconciler) validateResourceLeasingPolicy(policy *cloudv1beta1.ResourceLeasingPolicy) error {
	// Validate Cluster reference
	if policy.Spec.Cluster == "" {
		return fmt.Errorf("cluster reference is required")
	}

	// Validate ResourceLimits
	if len(policy.Spec.ResourceLimits) == 0 {
		return fmt.Errorf("at least one resource limit must be specified")
	}

	for _, limit := range policy.Spec.ResourceLimits {
		if limit.Resource == "" {
			return fmt.Errorf("resource name is required in resource limits")
		}
		// At least one of Quantity or Percent must be specified
		hasQuantity := limit.Quantity != nil && !limit.Quantity.IsZero()
		hasPercent := limit.Percent != nil && *limit.Percent > 0

		if !hasQuantity && !hasPercent {
			return fmt.Errorf("either quantity or percent must be specified for resource %s", limit.Resource)
		}
		if hasPercent && (*limit.Percent < 0 || *limit.Percent > 100) {
			return fmt.Errorf("percent must be between 0 and 100 for resource %s", limit.Resource)
		}
	}

	// Validate TimeWindows if specified
	for _, window := range policy.Spec.TimeWindows {
		if window.Start == "" || window.End == "" {
			return fmt.Errorf("time window start and end times are required")
		}
		// TODO: Add time format validation
	}

	return nil
}

// validateClusterBindingReference validates that the referenced ClusterBinding exists
func (r *ResourceLeasingPolicyReconciler) validateClusterBindingReference(ctx context.Context, policy *cloudv1beta1.ResourceLeasingPolicy) error {
	clusterBinding := &cloudv1beta1.ClusterBinding{}
	key := client.ObjectKey{
		Name:      policy.Spec.Cluster,
		Namespace: policy.Namespace,
	}

	if err := r.Get(ctx, key, clusterBinding); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("referenced ClusterBinding %s not found in namespace %s", policy.Spec.Cluster, policy.Namespace)
		}
		return fmt.Errorf("failed to get ClusterBinding %s: %v", policy.Spec.Cluster, err)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ResourceLeasingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudv1beta1.ResourceLeasingPolicy{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 3,
		}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
