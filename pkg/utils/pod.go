package utils

import (
	"fmt"
	"strings"

	"github.com/go-logr/logr"
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
