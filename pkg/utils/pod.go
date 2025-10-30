package utils

import (
	corev1 "k8s.io/api/core/v1"
)

// IsSystemPod checks if a pod is a system pod that should be ignored
func IsSystemPod(pod *corev1.Pod) bool {
	// Skip system namespaces
	systemNamespaces := []string{
		"kube-system",
		"kube-public",
		"kube-node-lease",
		"kubeocean-system",
		"kubeocean-fake",
	}

	for _, ns := range systemNamespaces {
		if pod.Namespace == ns {
			return true
		}
	}

	return false
}

// IsDaemonSetPod checks if a pod is managed by a DaemonSet
func IsDaemonSetPod(pod *corev1.Pod) bool {
	// Check if the pod has DaemonSet as an owner reference
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}
