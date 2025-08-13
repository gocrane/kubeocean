package bottomup

import (
	"context"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

// ResourceLeasingPolicyReconciler reconciles ResourceLeasingPolicy objects
type ResourceLeasingPolicyReconciler struct {
	Client         client.Client
	VirtualClient  client.Client
	Scheme         *runtime.Scheme
	ClusterBinding *cloudv1beta1.ClusterBinding
	Log            logr.Logger
}

//+kubebuilder:rbac:groups=cloud.tencent.com,resources=resourceleasingpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cloud.tencent.com,resources=resourceleasingpolicies/status,verbs=get;update;patch

// Reconcile handles ResourceLeasingPolicy events
func (r *ResourceLeasingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("resourceleasingpolicy", req.NamespacedName)

	// Get the ResourceLeasingPolicy
	policy := &cloudv1beta1.ResourceLeasingPolicy{}
	err := r.Client.Get(ctx, req.NamespacedName, policy)
	if err != nil {
		if errors.IsNotFound(err) {
			// Policy was deleted, trigger re-evaluation to remove nodes that no longer have policies
			log.Info("ResourceLeasingPolicy deleted, triggering node re-evaluation")
			return r.triggerNodeReEvaluation()
		}
		log.Error(err, "Failed to get ResourceLeasingPolicy")
		return ctrl.Result{}, err
	}

	// Check if this policy applies to our ClusterBinding
	if policy.Spec.Cluster != r.ClusterBinding.Name {
		log.V(1).Info("Policy does not apply to our cluster", "policyCluster", policy.Spec.Cluster, "ourCluster", r.ClusterBinding.Name)
		return ctrl.Result{}, nil
	}

	// Handle deletion
	if policy.DeletionTimestamp != nil {
		return r.triggerNodeReEvaluation()
	}

	// Update policy status (ignore errors for testing compatibility)
	if statusChanged, err := r.updatePolicyStatus(ctx, policy); err != nil {
		return ctrl.Result{}, err
	} else if statusChanged {
		log.Info("ResourceLeasingPolicy status changed, scheduling re-evaluation")
		return ctrl.Result{RequeueAfter: DefaultPolicySyncInterval}, nil
	}

	// Policy was created or updated, trigger re-evaluation
	log.Info("ResourceLeasingPolicy changed, triggering node re-evaluation")
	res, err := r.triggerNodeReEvaluation()
	if err != nil {
		return res, err
	}
	return ctrl.Result{RequeueAfter: DefaultPolicySyncInterval}, nil
}

// triggerNodeReEvaluation triggers re-evaluation of all nodes
// TODO: implement this
func (r *ResourceLeasingPolicyReconciler) triggerNodeReEvaluation() (ctrl.Result, error) {
	// For now, simply request a requeue after the default policy sync interval
	return ctrl.Result{RequeueAfter: DefaultPolicySyncInterval}, nil
}

// updatePolicyStatus updates the status of a ResourceLeasingPolicy
func (r *ResourceLeasingPolicyReconciler) updatePolicyStatus(ctx context.Context, policy *cloudv1beta1.ResourceLeasingPolicy) (bool, error) {
	// Check if policy is currently in an active time window
	isActive := r.isPolicyActiveNow(policy)

	// Determine phase
	var phase cloudv1beta1.ResourceLeasingPolicyPhase
	if isActive {
		phase = cloudv1beta1.ResourceLeasingPolicyPhaseActive
	} else {
		phase = cloudv1beta1.ResourceLeasingPolicyPhaseInactive
	}

	// Update status if changed
	statusChanged := policy.Status.Phase != phase || policy.Status.ActiveTimeWindow != isActive

	if statusChanged {
		policy.Status.Phase = phase
		policy.Status.ActiveTimeWindow = isActive
		policy.Status.LastAppliedTime = &metav1.Time{Time: time.Now()}

		// Update conditions
		r.updatePolicyConditions(policy, isActive)

		// Update the status
		if err := r.Client.Status().Update(ctx, policy); err != nil {
			return false, err
		}
	}

	return statusChanged, nil
}

// isPolicyActiveNow checks if a policy is currently active based on time windows
func (r *ResourceLeasingPolicyReconciler) isPolicyActiveNow(policy *cloudv1beta1.ResourceLeasingPolicy) bool {
	// If no time windows specified, policy is always active
	if len(policy.Spec.TimeWindows) == 0 {
		return true
	}

	now := time.Now()
	currentDay := strings.ToLower(now.Weekday().String())
	currentTime := now.Format("15:04")

	for _, window := range policy.Spec.TimeWindows {
		// Check if current day is in the allowed days
		dayMatches := len(window.Days) == 0 // If no days specified, assume all days
		if !dayMatches {
			for _, day := range window.Days {
				if strings.ToLower(day) == currentDay {
					dayMatches = true
					break
				}
			}
		}

		if dayMatches {
			// Check if current time is within the window
			if r.isTimeInRange(currentTime, window.Start, window.End) {
				return true
			}
		}
	}

	return false
}

// isTimeInRange checks if current time is within the specified range
func (r *ResourceLeasingPolicyReconciler) isTimeInRange(current, start, end string) bool {
	// Handle the case where end time is before start time (crosses midnight)
	if end < start {
		return current >= start || current <= end
	}
	return current >= start && current <= end
}

// updatePolicyConditions updates the conditions of a ResourceLeasingPolicy
func (r *ResourceLeasingPolicyReconciler) updatePolicyConditions(policy *cloudv1beta1.ResourceLeasingPolicy, isActive bool) {
	now := metav1.Now()

	// Find or create the Active condition
	var activeCondition *metav1.Condition
	for i := range policy.Status.Conditions {
		if policy.Status.Conditions[i].Type == "Active" {
			activeCondition = &policy.Status.Conditions[i]
			break
		}
	}

	if activeCondition == nil {
		// Create new condition
		policy.Status.Conditions = append(policy.Status.Conditions, metav1.Condition{
			Type:               "Active",
			LastTransitionTime: now,
		})
		activeCondition = &policy.Status.Conditions[len(policy.Status.Conditions)-1]
	}

	// Update condition
	newStatus := metav1.ConditionFalse
	newReason := "InactiveTimeWindow"
	newMessage := "Policy is outside of active time windows"

	if isActive {
		newStatus = metav1.ConditionTrue
		newReason = "ActiveTimeWindow"
		newMessage = "Policy is within active time window"
	}

	if activeCondition.Status != newStatus {
		activeCondition.LastTransitionTime = now
	}

	activeCondition.Status = newStatus
	activeCondition.Reason = newReason
	activeCondition.Message = newMessage
	activeCondition.ObservedGeneration = policy.Generation
}

// SetupWithManager sets up the controller with the Manager
func (r *ResourceLeasingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(
			&cloudv1beta1.ResourceLeasingPolicy{},
			builder.WithPredicates(
				predicate.Funcs{
					CreateFunc: func(e event.CreateEvent) bool {
						obj, ok := e.Object.(*cloudv1beta1.ResourceLeasingPolicy)
						return ok && obj.Spec.Cluster == r.ClusterBinding.Name
					},
					UpdateFunc: func(e event.UpdateEvent) bool {
						obj, ok := e.ObjectNew.(*cloudv1beta1.ResourceLeasingPolicy)
						return ok && obj.Spec.Cluster == r.ClusterBinding.Name
					},
					DeleteFunc: func(e event.DeleteEvent) bool {
						obj, ok := e.Object.(*cloudv1beta1.ResourceLeasingPolicy)
						return ok && obj.Spec.Cluster == r.ClusterBinding.Name
					},
				},
			)).
		Complete(r)
}
