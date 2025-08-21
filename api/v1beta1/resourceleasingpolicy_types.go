package v1beta1

import (
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceLeasingPolicySpec defines the desired state of ResourceLeasingPolicy
type ResourceLeasingPolicySpec struct {
	// Cluster references the ClusterBinding this policy applies to
	Cluster string `json:"cluster"`

	// NodeSelector specifies which nodes this policy applies to
	// +optional
	NodeSelector *v1.NodeSelector `json:"nodeSelector,omitempty"`

	// TimeWindows specifies when resources can be leased
	// +optional
	TimeWindows []TimeWindow `json:"timeWindows,omitempty"`

	// ResourceLimits specifies the maximum resources that can be leased
	// +optional
	ResourceLimits []ResourceLimit `json:"resourceLimits,omitempty"`

	// Kill compute pod force when time window is not active.
	ForceReclaim bool `json:"forceReclaim,omitempty"`

	// GracefulReclaimPeriodSeconds is the graceful period of reclaiming resources.
	GracefulReclaimPeriodSeconds int32 `json:"gracefulReclaimPeriodSeconds,omitempty"`
}

// TimeWindow defines a time period when resources can be leased
type TimeWindow struct {
	// Start time in HH:MM format
	Start string `json:"start"`

	// End time in HH:MM format
	End string `json:"end"`

	// Days of the week when this window applies
	// +optional
	Days []string `json:"days,omitempty"`
}

// ResourceLimit defines limits for specific resource types
type ResourceLimit struct {
	// Resource name (e.g., cpu, memory, storage)
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`

	// Quantity of the resource
	// +optional
	Quantity *resource.Quantity `json:"quantity,omitempty"`

	// Percent is the percentage of the resource to borrow.
	// +optional
	Percent *int32 `json:"percent,omitempty"`
}

// ResourceLeasingPolicyStatus defines the observed state of ResourceLeasingPolicy
type ResourceLeasingPolicyStatus struct {
	// Phase represents the current phase of the policy
	// +optional
	Phase ResourceLeasingPolicyPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the policy's current state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastAppliedTime represents the last time the policy was successfully applied
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

	// ActiveTimeWindow indicates if the policy is currently in an active time window
	// +optional
	ActiveTimeWindow bool `json:"activeTimeWindow,omitempty"`
}

// ResourceLeasingPolicyPhase represents the phase of a ResourceLeasingPolicy
type ResourceLeasingPolicyPhase string

const (
	// ResourceLeasingPolicyPhasePending means the policy is being processed
	ResourceLeasingPolicyPhasePending ResourceLeasingPolicyPhase = "Pending"
	// ResourceLeasingPolicyPhaseActive means the policy is active and being applied
	ResourceLeasingPolicyPhaseActive ResourceLeasingPolicyPhase = "Active"
	// ResourceLeasingPolicyPhaseInactive means the policy is inactive (outside time window)
	ResourceLeasingPolicyPhaseInactive ResourceLeasingPolicyPhase = "Inactive"
	// ResourceLeasingPolicyPhaseFailed means the policy has failed
	ResourceLeasingPolicyPhaseFailed ResourceLeasingPolicyPhase = "Failed"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster,shortName=rlp
//+kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.cluster"
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Active",type="boolean",JSONPath=".status.activeTimeWindow"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ResourceLeasingPolicy is the Schema for the resourceleasingpolicies API
type ResourceLeasingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ResourceLeasingPolicySpec   `json:"spec,omitempty"`
	Status ResourceLeasingPolicyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ResourceLeasingPolicyList contains a list of ResourceLeasingPolicy
type ResourceLeasingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceLeasingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ResourceLeasingPolicy{}, &ResourceLeasingPolicyList{})
}

// IsWithinTimeWindows checks if the current time is within any of the policy's time windows
// If no time windows are specified, it returns true (policy is always active)
func (p *ResourceLeasingPolicy) IsWithinTimeWindows() bool {
	now := time.Now()
	currentDay := strings.ToLower(now.Weekday().String())
	currentTime := now.Format("15:04")
	return p.IsWithinTimeWindowsAt(currentDay, currentTime)
}

// IsWithinTimeWindowsAt checks if the specified time is within any of the policy's time windows
// If no time windows are specified, it returns true (policy is always active)
// currentDay should be in lowercase (e.g., "monday", "tuesday")
// currentTime should be in HH:MM format (e.g., "14:30")
func (p *ResourceLeasingPolicy) IsWithinTimeWindowsAt(currentDay, currentTime string) bool {
	return IsWithinTimeWindows(p.Spec.TimeWindows, currentDay, currentTime)
}

// IsWithinTimeWindows checks if the specified time is within any of the time windows
// If no time windows are specified, it returns true (always active)
// currentDay should be in lowercase (e.g., "monday", "tuesday")
// currentTime should be in HH:MM format (e.g., "14:30")
func IsWithinTimeWindows(timeWindows []TimeWindow, currentDay, currentTime string) bool {
	// If no time windows specified, always active
	if len(timeWindows) == 0 {
		return true
	}

	for _, window := range timeWindows {
		// Check if current day is in the allowed days
		// If no days specified, assume all days are allowed
		dayMatches := len(window.Days) == 0
		if !dayMatches {
			for _, day := range window.Days {
				if strings.ToLower(day) == currentDay {
					dayMatches = true
					break
				}
			}
		}

		if !dayMatches {
			continue
		}

		// Check if current time is within the window
		if isTimeInRange(currentTime, window.Start, window.End) {
			return true
		}
	}

	return false
}

// isTimeInRange checks if current time is within the specified range
// Handles the case where end time is before start time (crosses midnight)
// Returns false if start or end time format is invalid (not HH:MM format)
func isTimeInRange(current, start, end string) bool {
	// Validate time format for start and end times
	if !isValidTimeFormat(start) || !isValidTimeFormat(end) {
		return false
	}

	// Handle the case where end time is before start time (crosses midnight)
	if end < start {
		return current >= start || current <= end
	}
	return current >= start && current <= end
}

// isValidTimeFormat checks if the time string is in valid HH:MM format
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
