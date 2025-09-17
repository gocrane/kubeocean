package utils

import (
	"context"
	"fmt"
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

// validateCSR validates CSR content (security check)
func validateCSR(csr *certificatesv1.CertificateSigningRequest) error {
	// 1. Check signer
	if csr.Spec.SignerName != "kubernetes.io/legacy-unknown" {
		return fmt.Errorf("invalid signer: %s", csr.Spec.SignerName)
	}

	// 2. Check usages
	validUsages := []certificatesv1.KeyUsage{
		certificatesv1.UsageDigitalSignature,
		certificatesv1.UsageKeyEncipherment,
		certificatesv1.UsageServerAuth,
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

	// 3. Parse and validate CSR content
	// TODO: Parse csr.Spec.Request (base64 encoded CSR)
	// Validate Subject, SAN and other information

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
