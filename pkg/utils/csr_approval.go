package utils

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ApproveCertificateSigningRequest automatically approves CSR
func approveCertificateSigningRequest(client kubernetes.Interface, csrName string) error {
	// 1. Get CSR
	csr, err := client.CertificatesV1().CertificateSigningRequests().Get(
		context.TODO(), csrName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get CSR: %w", err)
	}

	// 2. Check if CSR is already approved
	for _, condition := range csr.Status.Conditions {
		if condition.Type == certificatesv1.CertificateApproved {
			return fmt.Errorf("CSR already approved")
		}
		if condition.Type == certificatesv1.CertificateDenied {
			return fmt.Errorf("CSR was denied")
		}
	}

	// 3. Validate CSR content (security check)
	if err := validateCSR(csr); err != nil {
		return fmt.Errorf("CSR validation failed: %w", err)
	}

	// 4. Approve CSR
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateApproved,
		Status:         "True",
		Reason:         "KubeoceanProxierAutoApproval",
		Message:        "Automatically approved by Kubeocean Proxier",
		LastUpdateTime: metav1.Time{Time: time.Now()},
	})

	// 5. Update CSR status
	_, err = client.CertificatesV1().CertificateSigningRequests().UpdateApproval(
		context.TODO(), csrName, csr, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to approve CSR: %w", err)
	}

	return nil
}

// validateCSR validates CSR content for kubernetes.io/kubelet-serving signer (security check)
func validateCSR(csr *certificatesv1.CertificateSigningRequest) error {
	// 1. Check signer - now supporting kubelet-serving
	if csr.Spec.SignerName != certificatesv1.KubeletServingSignerName {
		return fmt.Errorf("invalid signer: %s, expected: %s", csr.Spec.SignerName, certificatesv1.KubeletServingSignerName)
	}

	// 2. Validate requesting user identity for kubelet-serving
	if err := validateKubeletServingIdentity(csr); err != nil {
		return fmt.Errorf("identity validation failed: %w", err)
	}

	// 3. Check usages - kubelet-serving requires specific usages
	validUsages := []certificatesv1.KeyUsage{
		certificatesv1.UsageDigitalSignature,
		certificatesv1.UsageKeyEncipherment,
		certificatesv1.UsageServerAuth,
	}

	if len(csr.Spec.Usages) != len(validUsages) {
		return fmt.Errorf("invalid number of usages: expected %d, got %d", len(validUsages), len(csr.Spec.Usages))
	}

	for _, usage := range csr.Spec.Usages {
		found := false
		for _, validUsage := range validUsages {
			if usage == validUsage {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("invalid usage: %s", usage)
		}
	}

	// 4. Parse and validate CSR content
	if err := validateKubeletServingCSRContent(csr); err != nil {
		return fmt.Errorf("CSR content validation failed: %w", err)
	}

	return nil
}

// waitForCertificate waits for certificate to be issued
func waitForCertificate(client kubernetes.Interface, csrName string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting for certificate")
		case <-time.After(2 * time.Second):
			csr, err := client.CertificatesV1().CertificateSigningRequests().Get(
				context.TODO(), csrName, metav1.GetOptions{})
			if err != nil {
				continue
			}

			// Check if certificate is available
			if len(csr.Status.Certificate) > 0 {
				return csr.Status.Certificate, nil
			}

			// Check if request was denied
			for _, condition := range csr.Status.Conditions {
				if condition.Type == certificatesv1.CertificateDenied {
					return nil, fmt.Errorf("certificate request was denied: %s", condition.Message)
				}
			}
		}
	}
}

// validateKubeletServingIdentity validates the requesting user identity for kubelet-serving CSR
func validateKubeletServingIdentity(csr *certificatesv1.CertificateSigningRequest) error {
	// 1. Validate username format: must be system:node:<node-name>
	if csr.Spec.Username == "" {
		return fmt.Errorf("username is required for kubelet-serving CSR")
	}
	
	if !strings.HasPrefix(csr.Spec.Username, "system:node:") {
		return fmt.Errorf("invalid username format: %s, expected system:node:<node-name>", csr.Spec.Username)
	}
	
	nodeName := strings.TrimPrefix(csr.Spec.Username, "system:node:")
	if nodeName == "" {
		return fmt.Errorf("node name cannot be empty in username: %s", csr.Spec.Username)
	}

	// 2. Validate groups: must include system:nodes and system:authenticated
	requiredGroups := []string{"system:nodes", "system:authenticated"}
	groupMap := make(map[string]bool)
	for _, group := range csr.Spec.Groups {
		groupMap[group] = true
	}
	
	for _, required := range requiredGroups {
		if !groupMap[required] {
			return fmt.Errorf("required group %s not found in CSR groups", required)
		}
	}

	return nil
}

// validateKubeletServingCSRContent validates the content of the CSR for kubelet-serving
func validateKubeletServingCSRContent(csr *certificatesv1.CertificateSigningRequest) error {
	// Decode PEM block
	block, _ := pem.Decode(csr.Spec.Request)
	if block == nil {
		return fmt.Errorf("failed to decode PEM block from CSR request")
	}
	
	if block.Type != "CERTIFICATE REQUEST" {
		return fmt.Errorf("invalid PEM block type: %s, expected CERTIFICATE REQUEST", block.Type)
	}

	// Parse certificate request
	csrObj, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate request: %w", err)
	}

	// Extract node name from username for validation
	username := csr.Spec.Username
	if !strings.HasPrefix(username, "system:node:") {
		return fmt.Errorf("invalid username format for content validation")
	}
	expectedNodeName := strings.TrimPrefix(username, "system:node:")

	// 1. Validate Subject CommonName
	expectedCN := fmt.Sprintf("system:node:%s", expectedNodeName)
	if csrObj.Subject.CommonName != expectedCN {
		return fmt.Errorf("invalid CommonName: %s, expected: %s", csrObj.Subject.CommonName, expectedCN)
	}

	// 2. Validate Subject Organization
	if len(csrObj.Subject.Organization) != 1 || csrObj.Subject.Organization[0] != "system:nodes" {
		return fmt.Errorf("invalid Organization: %v, expected: [system:nodes]", csrObj.Subject.Organization)
	}

	// 3. Validate Subject Alternative Names (SAN)
	// For kubelet-serving, we need at least one DNS name or IP address
	if len(csrObj.DNSNames) == 0 && len(csrObj.IPAddresses) == 0 {
		return fmt.Errorf("CSR must contain at least one DNS name or IP address in SAN")
	}

	// 4. Check if node name is present in DNS names (recommended)
	nodeNameFound := false
	for _, dnsName := range csrObj.DNSNames {
		if dnsName == expectedNodeName {
			nodeNameFound = true
			break
		}
	}
	
	if !nodeNameFound {
		return fmt.Errorf("node name %s should be included in DNS names for kubelet-serving certificate", expectedNodeName)
	}

	return nil
}
