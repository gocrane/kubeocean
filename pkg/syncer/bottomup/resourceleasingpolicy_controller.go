package bottomup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

const (
	// ConditionTypeValid is the type for Valid condition
	ConditionTypeValid = "Valid"
	// ConditionTypeActive is the type for Active condition
	ConditionTypeActive = "Active"
	// ReasonValidationError is the reason for validation error
	ReasonValidationError = "ValidationError"
)

// ResourceLeasingPolicyReconciler reconciles ResourceLeasingPolicy objects
type ResourceLeasingPolicyReconciler struct {
	Client         client.Client
	ClusterBinding *cloudv1beta1.ClusterBinding
	Log            logr.Logger
	// Functions for triggering node re-evaluation, provided by BottomUpSyncer
	GetNodesMatchingSelector func(ctx context.Context, selector *corev1.NodeSelector) ([]string, error)
	RequeueNodes             func(nodeNames []string) error
}

//+kubebuilder:rbac:groups=cloud.tencent.com,resources=resourceleasingpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cloud.tencent.com,resources=resourceleasingpolicies/status,verbs=get;update;patch

// hasFinalizer checks if the policy has our finalizer
func (r *ResourceLeasingPolicyReconciler) hasFinalizer(policy *cloudv1beta1.ResourceLeasingPolicy) bool {
	return controllerutil.ContainsFinalizer(policy, cloudv1beta1.PolicyFinalizerName)
}

// addFinalizer adds our finalizer to the policy
func (r *ResourceLeasingPolicyReconciler) addFinalizer(ctx context.Context, policy *cloudv1beta1.ResourceLeasingPolicy, log logr.Logger) (ctrl.Result, error) {
	log.V(1).Info("Adding finalizer to ResourceLeasingPolicy")
	controllerutil.AddFinalizer(policy, cloudv1beta1.PolicyFinalizerName)
	if err := r.Client.Update(ctx, policy); err != nil {
		log.Error(err, "Failed to add finalizer")
		return ctrl.Result{}, err
	}
	// not requeue, wait for it updated
	return ctrl.Result{}, nil
}

// removeFinalizer removes our finalizer from the policy
func (r *ResourceLeasingPolicyReconciler) removeFinalizer(ctx context.Context, policy *cloudv1beta1.ResourceLeasingPolicy, log logr.Logger) error {
	log.V(1).Info("Removing finalizer from ResourceLeasingPolicy")
	controllerutil.RemoveFinalizer(policy, cloudv1beta1.PolicyFinalizerName)
	return r.Client.Update(ctx, policy)
}

// handlePolicyDeletion handles the deletion of a policy using finalizer
func (r *ResourceLeasingPolicyReconciler) handlePolicyDeletion(ctx context.Context, policy *cloudv1beta1.ResourceLeasingPolicy, log logr.Logger) (ctrl.Result, error) {
	log.Info("ResourceLeasingPolicy is being deleted, triggering node re-evaluation with cached NodeSelector")

	// At this point, we still have access to the complete policy object including NodeSelector
	// Trigger node re-evaluation with the policy's NodeSelector
	if err := r.triggerNodeReEvaluation(policy); err != nil {
		log.Error(err, "Failed to trigger node re-evaluation during deletion")
		return ctrl.Result{}, err
	}

	// Remove finalizer to allow deletion to proceed
	if err := r.removeFinalizer(ctx, policy, log); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	log.Info("Successfully handled ResourceLeasingPolicy deletion")
	return ctrl.Result{}, nil
}

// Reconcile handles ResourceLeasingPolicy events
func (r *ResourceLeasingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("resourceleasingpolicy", req.NamespacedName)
	//log.V(1).Info("Reconcile", "req", req)

	// Get the ResourceLeasingPolicy
	policy := &cloudv1beta1.ResourceLeasingPolicy{}
	err := r.Client.Get(ctx, req.NamespacedName, policy)
	if err != nil {
		if errors.IsNotFound(err) {
			log.V(1).Info("ResourceLeasingPolicy not found, likely already deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ResourceLeasingPolicy")
		return ctrl.Result{}, err
	}

	// Check if this policy applies to our ClusterBinding
	if policy.Spec.Cluster != r.ClusterBinding.Name {
		log.V(1).Info("Policy does not apply to our cluster", "policyCluster", policy.Spec.Cluster, "ourCluster", r.ClusterBinding.Name)
		return ctrl.Result{}, nil
	}

	// Handle deletion with finalizer
	if policy.DeletionTimestamp != nil {
		return r.handlePolicyDeletion(ctx, policy, log)
	}

	// Add finalizer if not present
	if !r.hasFinalizer(policy) {
		return r.addFinalizer(ctx, policy, log)
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
	if err := r.triggerNodeReEvaluation(policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: DefaultPolicySyncInterval}, nil
}

// triggerNodeReEvaluation triggers re-evaluation of all nodes by using the provided functions
func (r *ResourceLeasingPolicyReconciler) triggerNodeReEvaluation(policy *cloudv1beta1.ResourceLeasingPolicy) error {
	policyName := ""
	var nodeSelector *corev1.NodeSelector
	if policy != nil {
		policyName = policy.Name
		nodeSelector = policy.Spec.NodeSelector
	}
	log := r.Log.WithValues("resourceleasingpolicy", policyName)
	if r.GetNodesMatchingSelector == nil || r.RequeueNodes == nil {
		log.Error(fmt.Errorf("node re-evaluation functions not provided, skipping trigger"), "Node re-evaluation functions not provided, skipping trigger")
		return nil
	}

	ctx := context.Background()

	// Get matching nodes
	nodes, err := r.GetNodesMatchingSelector(ctx, nodeSelector)
	if err != nil {
		log.Error(err, "Failed to get nodes for re-evaluation")
		return err
	}

	// Requeue each node for re-evaluation
	log.Info("Requeuing nodes for re-evaluation", "nodes", nodes)
	if len(nodes) > 0 {
		if err := r.RequeueNodes(nodes); err != nil {
			log.Error(err, "Failed to requeue nodes for re-evaluation")
			return err
		}
	}

	return nil
}

// updatePolicyStatus updates the status of a ResourceLeasingPolicy
func (r *ResourceLeasingPolicyReconciler) updatePolicyStatus(ctx context.Context, policy *cloudv1beta1.ResourceLeasingPolicy) (bool, error) {
	// First, validate time windows format
	if validationErr := r.validateTimeWindows(policy); validationErr != nil {
		// Set policy to Failed phase with validation error
		statusChanged := policy.Status.Phase != cloudv1beta1.ResourceLeasingPolicyPhaseFailed

		if statusChanged {
			policy.Status.Phase = cloudv1beta1.ResourceLeasingPolicyPhaseFailed
			policy.Status.ActiveTimeWindow = false
			policy.Status.LastAppliedTime = &metav1.Time{Time: time.Now()}

			// Update conditions with validation error
			r.updatePolicyConditionsWithValidationError(policy, validationErr.Error())

			// Update the status
			if err := r.Client.Status().Update(ctx, policy); err != nil {
				return false, err
			}
		}

		return statusChanged, nil
	}

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
	now := time.Now()
	currentDay := strings.ToLower(now.Weekday().String())
	currentTime := now.Format("15:04")
	r.Log.V(1).Info("Checking if policy is active now", "policy", policy.Name, "timeWindows", policy.Spec.TimeWindows, "currentDay", currentDay, "currentTime", currentTime)

	return policy.IsWithinTimeWindowsAt(currentDay, currentTime)
}

// updatePolicyConditions updates the conditions of a ResourceLeasingPolicy
func (r *ResourceLeasingPolicyReconciler) updatePolicyConditions(policy *cloudv1beta1.ResourceLeasingPolicy, isActive bool) {
	now := metav1.Now()

	// Update Valid condition to True since validation passed
	var validCondition *metav1.Condition
	for i := range policy.Status.Conditions {
		if policy.Status.Conditions[i].Type == ConditionTypeValid {
			validCondition = &policy.Status.Conditions[i]
			break
		}
	}

	if validCondition == nil {
		// Create new Valid condition
		policy.Status.Conditions = append(policy.Status.Conditions, metav1.Condition{
			Type:               ConditionTypeValid,
			LastTransitionTime: now,
		})
		validCondition = &policy.Status.Conditions[len(policy.Status.Conditions)-1]
	}

	// Set Valid condition to True
	if validCondition.Status != metav1.ConditionTrue || validCondition.Reason != "ValidationPassed" {
		validCondition.LastTransitionTime = now
	}

	validCondition.Status = metav1.ConditionTrue
	validCondition.Reason = "ValidationPassed"
	validCondition.Message = "Policy validation passed successfully"
	validCondition.ObservedGeneration = policy.Generation

	// Find or create the Active condition
	var activeCondition *metav1.Condition
	for i := range policy.Status.Conditions {
		if policy.Status.Conditions[i].Type == ConditionTypeActive {
			activeCondition = &policy.Status.Conditions[i]
			break
		}
	}

	if activeCondition == nil {
		// Create new condition
		policy.Status.Conditions = append(policy.Status.Conditions, metav1.Condition{
			Type:               ConditionTypeActive,
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
	// Generate unique controller name using cluster binding name
	uniqueControllerName := fmt.Sprintf("resourceleasingpolicy-%s", r.ClusterBinding.Name)

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
		Named(uniqueControllerName).
		Complete(r)
}

// validateTimeWindows validates the format of time windows in a policy
func (r *ResourceLeasingPolicyReconciler) validateTimeWindows(policy *cloudv1beta1.ResourceLeasingPolicy) error {
	for i, window := range policy.Spec.TimeWindows {
		// Validate start time format
		if !isValidTimeFormat(window.Start) {
			return fmt.Errorf("invalid start time format in time window %d: %q, expected HH:MM format", i, window.Start)
		}

		// Validate end time format
		if !isValidTimeFormat(window.End) {
			return fmt.Errorf("invalid end time format in time window %d: %q, expected HH:MM format", i, window.End)
		}

		// Validate days if specified
		for j, day := range window.Days {
			dayLower := strings.ToLower(day)
			validDays := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
			isValidDay := false
			for _, validDay := range validDays {
				if dayLower == validDay {
					isValidDay = true
					break
				}
			}
			if !isValidDay {
				return fmt.Errorf("invalid day in time window %d, day %d: %q, expected one of: %v", i, j, day, validDays)
			}
		}
	}
	return nil
}

// updatePolicyConditionsWithValidationError updates conditions with validation error
func (r *ResourceLeasingPolicyReconciler) updatePolicyConditionsWithValidationError(policy *cloudv1beta1.ResourceLeasingPolicy, errorMessage string) {
	now := metav1.Now()

	// Find or create the Valid condition
	var validCondition *metav1.Condition
	for i := range policy.Status.Conditions {
		if policy.Status.Conditions[i].Type == "Valid" {
			validCondition = &policy.Status.Conditions[i]
			break
		}
	}

	if validCondition == nil {
		// Create new condition
		policy.Status.Conditions = append(policy.Status.Conditions, metav1.Condition{
			Type:               "Valid",
			LastTransitionTime: now,
		})
		validCondition = &policy.Status.Conditions[len(policy.Status.Conditions)-1]
	}

	// Update condition with validation error
	newStatus := metav1.ConditionFalse
	newReason := ReasonValidationError
	newMessage := fmt.Sprintf("Policy validation failed: %s", errorMessage)

	// Update condition if status changed
	if validCondition.Status != newStatus || validCondition.Reason != newReason {
		validCondition.LastTransitionTime = now
	}

	validCondition.Status = newStatus
	validCondition.Reason = newReason
	validCondition.Message = newMessage
	validCondition.ObservedGeneration = policy.Generation

	// Also update or remove the Active condition since the policy is invalid
	var activeCondition *metav1.Condition
	for i := range policy.Status.Conditions {
		if policy.Status.Conditions[i].Type == "Active" {
			activeCondition = &policy.Status.Conditions[i]
			break
		}
	}

	if activeCondition == nil {
		// Create new Active condition
		policy.Status.Conditions = append(policy.Status.Conditions, metav1.Condition{
			Type:               "Active",
			LastTransitionTime: now,
		})
		activeCondition = &policy.Status.Conditions[len(policy.Status.Conditions)-1]
	}

	// Set Active condition to False due to validation error
	if activeCondition.Status != metav1.ConditionFalse || activeCondition.Reason != "ValidationError" {
		activeCondition.LastTransitionTime = now
	}

	activeCondition.Status = metav1.ConditionFalse
	activeCondition.Reason = "ValidationError"
	activeCondition.Message = "Policy cannot be activated due to validation errors"
	activeCondition.ObservedGeneration = policy.Generation
}

// isValidTimeFormat checks if the time string is in valid HH:MM format
// This is a duplicate of the function in api/v1beta1 package to avoid circular import
func isValidTimeFormat(timeStr string) bool {
	if len(timeStr) != 5 {
		return false
	}

	// Check format: HH:MM
	if timeStr[2] != ':' {
		return false
	}

	// Parse hours
	hourStr := timeStr[0:2]
	for _, r := range hourStr {
		if r < '0' || r > '9' {
			return false
		}
	}
	hour := int(hourStr[0]-'0')*10 + int(hourStr[1]-'0')
	if hour < 0 || hour > 23 {
		return false
	}

	// Parse minutes
	minuteStr := timeStr[3:5]
	for _, r := range minuteStr {
		if r < '0' || r > '9' {
			return false
		}
	}
	minute := int(minuteStr[0]-'0')*10 + int(minuteStr[1]-'0')
	if minute < 0 || minute > 59 {
		return false
	}

	return true
}
