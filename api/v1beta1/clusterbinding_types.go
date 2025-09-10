package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterBindingSpec defines the desired state of ClusterBinding
type ClusterBindingSpec struct {
	// ClusterID is a unique identifier for the cluster that will be used as part of virtual node names
	// This field is required and immutable after creation
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=^[a-z0-9]([-a-z0-9]*[a-z0-9])?$
	// +kubebuilder:validation:MaxLength=24
	ClusterID string `json:"clusterID"`

	// SecretRef references the secret containing kubeconfig for the target cluster
	SecretRef corev1.SecretReference `json:"secretRef"`

	// NodeSelector specifies which nodes to monitor in the target cluster
	// +optional
	NodeSelector *corev1.NodeSelector `json:"nodeSelector,omitempty"`

	// MountNamespace specifies the namespace to mount cluster resources
	// +optional
	MountNamespace string `json:"mountNamespace,omitempty"`

	// ServiceNamespaces specifies the namespaces to sync services from
	// +optional
	ServiceNamespaces []string `json:"serviceNamespaces,omitempty"`

	// DisableNodeDefaultTaint controls whether to add the default kubeocean.io/vnode taint to virtual nodes
	// If true, the default taint will not be added; if false or not set, the default taint will be added
	// +optional
	DisableNodeDefaultTaint bool `json:"disableNodeDefaultTaint,omitempty"`
}

// ClusterBindingStatus defines the observed state of ClusterBinding
type ClusterBindingStatus struct {
	// Phase represents the current phase of the cluster binding
	// +optional
	Phase ClusterBindingPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the cluster binding's current state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastSyncTime represents the last time the cluster was successfully synced
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
}

// ClusterBindingPhase represents the phase of a ClusterBinding
type ClusterBindingPhase string

const (
	// ClusterBindingPhasePending means the cluster binding is being processed
	ClusterBindingPhasePending ClusterBindingPhase = "Pending"
	// ClusterBindingPhaseReady means the cluster binding is ready and syncing
	ClusterBindingPhaseReady ClusterBindingPhase = "Ready"
	// ClusterBindingPhaseFailed means the cluster binding has failed
	ClusterBindingPhaseFailed ClusterBindingPhase = "Failed"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster,shortName=cb
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ClusterBinding is the Schema for the clusterbindings API
type ClusterBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterBindingSpec   `json:"spec,omitempty"`
	Status ClusterBindingStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterBindingList contains a list of ClusterBinding
type ClusterBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterBinding{}, &ClusterBindingList{})
}
