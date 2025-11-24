package utils

import (
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
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

// TruncatePodHostnameIfNeeded truncates the pod hostname if it's longer than 63 chars.
func TruncateHostnameIfNeeded(logger logr.Logger, hostname string) (string, error) {
	// Cap hostname at 63 chars (specification is 64bytes which is 63 chars and the null terminating char).
	const hostnameMaxLen = 63
	if len(hostname) <= hostnameMaxLen {
		return hostname, nil
	}
	truncated := hostname[:hostnameMaxLen]
	logger.Info("Hostname was too long, truncated it", "hostnameMaxLen", hostnameMaxLen, "truncatedHostname", truncated)
	// hostname should not end with '-' or '.'
	truncated = strings.TrimRight(truncated, "-.")
	if len(truncated) == 0 {
		// This should never happen.
		return "", fmt.Errorf("hostname was invalid: %q", hostname)
	}
	return truncated, nil
}

// ExtractPodFromDeleteEvent extracts Pod object from delete event, handling DeletedFinalStateUnknown
// This follows the standard Kubernetes pattern for handling delete events in informers.
// Returns the extracted Pod and a boolean indicating success.
func ExtractPodFromDeleteEvent(obj interface{}, logger logr.Logger) (*corev1.Pod, bool) {
	var pod *corev1.Pod

	switch t := obj.(type) {
	case *corev1.Pod:
		// Normal delete event
		pod = t
	case cache.DeletedFinalStateUnknown:
		// Handle DeletedFinalStateUnknown which occurs when the informer's local cache
		// is out of sync with the API server (e.g., due to connection issues)
		logger.V(1).Info("Received DeletedFinalStateUnknown in delete event, extracting object from tombstone")
		var ok bool
		pod, ok = t.Obj.(*corev1.Pod)
		if !ok {
			logger.Error(nil, "Failed to convert DeletedFinalStateUnknown.Obj to Pod",
				"objectType", fmt.Sprintf("%T", t.Obj))
			return nil, false
		}
	default:
		logger.Error(nil, "Unexpected object type in delete event",
			"objectType", fmt.Sprintf("%T", obj))
		return nil, false
	}

	return pod, true
}
