package proxier

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

// CertificateManager handles automatic certificate request and approval
type CertificateManager struct {
	client         kubernetes.Interface
	clusterBinding *cloudv1beta1.ClusterBinding
	namespace      string
	log            logr.Logger
	renewalMutex   sync.Mutex
	stopCh         chan struct{}
}

// NewCertificateManager creates a new certificate manager
func NewCertificateManager(client kubernetes.Interface, clusterBinding *cloudv1beta1.ClusterBinding, namespace string, log logr.Logger) *CertificateManager {
	return &CertificateManager{
		client:         client,
		clusterBinding: clusterBinding,
		namespace:      namespace,
		log:            log,
		stopCh:         make(chan struct{}),
	}
}

// GetOrCreateTLSSecret gets existing TLS secret or creates one automatically
// This method is only called for automatic certificate management (Priority 3)
func (cm *CertificateManager) GetOrCreateTLSSecret(ctx context.Context) (*corev1.Secret, error) {
	// Use default naming pattern for auto-managed certificates
	defaultSecretName := fmt.Sprintf("%s-logs-proxy-tls", cm.clusterBinding.Spec.ClusterID)

	// Check if auto-managed secret already exists
	secret, err := cm.getExistingSecret(ctx, defaultSecretName)
	if err == nil {
		cm.log.Info("Using existing auto-managed TLS secret", "secretName", defaultSecretName)
		return secret, nil
	}

	// Create new auto-managed certificate
	cm.log.Info("No auto-managed TLS secret found, creating one automatically", "secretName", defaultSecretName)
	return cm.createTLSSecretWithAutoApproval(ctx, defaultSecretName)
}

// getSpecifiedSecretName gets secret name from ClusterBinding annotation
func (cm *CertificateManager) getSpecifiedSecretName() string {
	if cm.clusterBinding.Annotations == nil {
		return ""
	}
	return cm.clusterBinding.Annotations["kubeocean.io/logs-proxy-secret-name"]
}

// getExistingSecret gets existing secret
func (cm *CertificateManager) getExistingSecret(ctx context.Context, secretName string) (*corev1.Secret, error) {
	secret, err := cm.client.CoreV1().Secrets(cm.namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	// Validate secret has required keys
	if _, hasCert := secret.Data["tls.crt"]; !hasCert {
		return nil, fmt.Errorf("secret %s missing tls.crt", secretName)
	}
	if _, hasKey := secret.Data["tls.key"]; !hasKey {
		return nil, fmt.Errorf("secret %s missing tls.key", secretName)
	}

	return secret, nil
}

// createTLSSecretWithAutoApproval creates TLS secret with automatic certificate approval
func (cm *CertificateManager) createTLSSecretWithAutoApproval(ctx context.Context, secretName string) (*corev1.Secret, error) {
	// Generate certificate data
	certificate, privateKey, err := cm.generateCertificateData(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate data: %w", err)
	}

	// Create TLS Secret
	secret, err := cm.createTLSSecret(ctx, secretName, certificate, privateKey)
	if err != nil {
		// Handle race condition: if secret already exists, try to get it
		if errors.IsAlreadyExists(err) {
			cm.log.Info("Secret already exists (race condition detected), attempting to get existing secret",
				"secretName", secretName,
				"error", err.Error())

			// Try to get the existing secret
			existingSecret, getErr := cm.getExistingSecret(ctx, secretName)
			if getErr != nil {
				cm.log.Error(getErr, "Failed to get existing secret after race condition",
					"secretName", secretName,
					"createError", err.Error())
				return nil, fmt.Errorf("failed to create TLS secret and failed to get existing secret: create error=%w, get error=%w", err, getErr)
			}

			cm.log.Info("Successfully retrieved existing TLS secret (race condition resolved)",
				"secretName", secretName,
				"secretNamespace", existingSecret.Namespace)
			return existingSecret, nil
		}

		cm.log.Error(err, "Failed to create TLS secret", "secretName", secretName)
		return nil, fmt.Errorf("failed to create TLS secret: %w", err)
	}

	cm.log.Info("Successfully created TLS secret with auto-approved certificate", "secretName", secretName)

	return secret, nil
}

// generateCertificateData generates certificate data without creating a Secret
func (cm *CertificateManager) generateCertificateData(ctx context.Context) ([]byte, *rsa.PrivateKey, error) {
	// Try up to 3 times to handle race conditions
	maxRetries := 3
	baseDelay := 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		cm.log.Info("Attempting certificate generation", "attempt", attempt, "maxRetries", maxRetries)

		certificate, privateKey, err := cm.generateCertificateDataOnce(ctx)
		if err == nil {
			return certificate, privateKey, nil
		}

		// Check if it's a CSR race condition error
		if strings.Contains(err.Error(), "CSR race condition detected") ||
			strings.Contains(err.Error(), "CSR was deleted while waiting for certificate") {
			cm.log.V(1).Info("CSR race condition detected, retrying certificate generation",
				"attempt", attempt,
				"error", err.Error())

			// If this is not the last attempt, wait and retry
			if attempt < maxRetries {
				delay := time.Duration(attempt) * baseDelay
				cm.log.Info("Waiting before retry", "delay", delay.String())

				select {
				case <-ctx.Done():
					return nil, nil, fmt.Errorf("context cancelled during retry wait: %w", ctx.Err())
				case <-time.After(delay):
					// Continue to next iteration
				}
				continue
			}
		}

		// For other errors or final attempt, return the error
		if attempt == maxRetries {
			return nil, nil, fmt.Errorf("failed to generate certificate after %d attempts: %w", maxRetries, err)
		}
	}

	return nil, nil, fmt.Errorf("unexpected error in certificate generation retry loop")
}

// generateCertificateDataOnce generates certificate data in a single attempt
func (cm *CertificateManager) generateCertificateDataOnce(ctx context.Context) ([]byte, *rsa.PrivateKey, error) {
	// 1. Get Pod IP
	podIP, err := cm.getPodIP()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get pod IP: %w", err)
	}

	// 2. Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// 3. Create certificate signing request
	csr, err := cm.createCSR(privateKey, podIP)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	// 4. Submit CSR to Kubernetes with unique name
	csrName := cm.generateUniqueCSRName()
	_, err = cm.submitCSR(ctx, csrName, csr)
	if err != nil {
		// Handle CSR already exists error (race condition during concurrent startup)
		if errors.IsAlreadyExists(err) {
			cm.log.Info("CSR already exists (race condition detected), attempting to use existing CSR",
				"csrName", csrName,
				"error", err.Error())

			// Try to get the existing CSR and check if it's already approved
			existingCSR, getErr := cm.client.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
			if getErr != nil {
				cm.log.Error(getErr, "Failed to get existing CSR after race condition",
					"csrName", csrName,
					"createError", err.Error())
				return nil, nil, fmt.Errorf("failed to submit CSR and failed to get existing CSR: create error=%w, get error=%w", err, getErr)
			}

			// Check if the existing CSR is already approved
			approved := false
			for _, condition := range existingCSR.Status.Conditions {
				if condition.Type == certificatesv1.CertificateApproved {
					approved = true
					break
				}
			}

			if !approved {
				cm.log.Info("Existing CSR not approved, attempting to approve it", "csrName", csrName)
				// Try to approve the existing CSR
				if approveErr := cm.approveCSR(ctx, csrName); approveErr != nil {
					cm.log.Error(approveErr, "Failed to approve existing CSR", "csrName", csrName)
					return nil, nil, fmt.Errorf("failed to approve existing CSR: %w", approveErr)
				}
			} else {
				cm.log.Info("Existing CSR already approved", "csrName", csrName)
			}
		} else {
			return nil, nil, fmt.Errorf("failed to submit CSR: %w", err)
		}
	}

	// 5. Automatically approve CSR
	err = cm.approveCSR(ctx, csrName)
	if err != nil {
		// Clean up CSR
		cm.client.CertificatesV1().CertificateSigningRequests().Delete(ctx, csrName, metav1.DeleteOptions{})
		return nil, nil, fmt.Errorf("failed to approve CSR: %w", err)
	}

	// 6. Wait for certificate to be issued with retry mechanism
	certificate, err := cm.waitForCertificateWithRetry(ctx, csrName, 60*time.Second)
	if err != nil {
		// Clean up CSR
		cm.client.CertificatesV1().CertificateSigningRequests().Delete(ctx, csrName, metav1.DeleteOptions{})
		return nil, nil, fmt.Errorf("failed to get certificate: %w", err)
	}

	// 7. Clean up CSR
	cm.client.CertificatesV1().CertificateSigningRequests().Delete(ctx, csrName, metav1.DeleteOptions{})

	cm.log.Info("Successfully generated certificate data", "podIP", podIP)

	return certificate, privateKey, nil
}

// generateUniqueCSRName generates a unique CSR name to avoid conflicts between multiple pods
func (cm *CertificateManager) generateUniqueCSRName() string {
	// Get pod name for uniqueness
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		podName = "unknown-pod"
	}

	// Use nanosecond timestamp for better uniqueness
	timestamp := time.Now().UnixNano()

	// Generate unique CSR name with pod name and nanosecond timestamp
	return fmt.Sprintf("kubeocean-proxier-%s-%s-%d", cm.clusterBinding.Name, podName, timestamp)
}

// getPodIP gets current pod IP
func (cm *CertificateManager) getPodIP() (string, error) {
	// Try environment variable first
	if podIP := os.Getenv("POD_IP"); podIP != "" {
		return podIP, nil
	}

	// Try to get from pod name and namespace
	podName := os.Getenv("POD_NAME")
	podNamespace := os.Getenv("POD_NAMESPACE")
	if podName == "" || podNamespace == "" {
		return "", fmt.Errorf("POD_IP, POD_NAME, or POD_NAMESPACE environment variable not set")
	}

	pod, err := cm.client.CoreV1().Pods(podNamespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get pod: %w", err)
	}

	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod IP not available")
	}

	return pod.Status.PodIP, nil
}

// createCSR creates a certificate signing request
func (cm *CertificateManager) createCSR(privateKey *rsa.PrivateKey, podIP string) ([]byte, error) {
	// Build Subject Alternative Names
	var dnsNames []string
	var ipAddresses []net.IP

	// Add DNS names
	dnsNames = append(dnsNames,
		"localhost",
		"logs-proxy",
		"kubeocean-logs-proxy",
		"kubelet",
		"kubelet-api",
		cm.clusterBinding.Name, // Use cluster binding name as virtual node name
	)

	// Add IP addresses
	if ip := net.ParseIP(podIP); ip != nil {
		ipAddresses = append(ipAddresses, ip)
	}
	if ip := net.ParseIP("127.0.0.1"); ip != nil {
		ipAddresses = append(ipAddresses, ip)
	}

	// Create certificate request template
	template := x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   cm.clusterBinding.Name,
			Organization: []string{"kubeocean"},
		},
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
	}

	// Create CSR
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &template, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate request: %w", err)
	}

	return csrDER, nil
}

// submitCSR submits CSR to Kubernetes
func (cm *CertificateManager) submitCSR(ctx context.Context, csrName string, csrDER []byte) (*certificatesv1.CertificateSigningRequest, error) {
	// Convert DER to PEM format
	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	})

	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: csrName,
		},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/legacy-unknown",
			Usages: []certificatesv1.KeyUsage{
				certificatesv1.UsageDigitalSignature,
				certificatesv1.UsageKeyEncipherment,
				certificatesv1.UsageServerAuth,
			},
		},
	}

	return cm.client.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, metav1.CreateOptions{})
}

// approveCSR automatically approves the CSR
func (cm *CertificateManager) approveCSR(ctx context.Context, csrName string) error {
	// Get the CSR
	csr, err := cm.client.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get CSR: %w", err)
	}

	// Check if already approved
	for _, condition := range csr.Status.Conditions {
		if condition.Type == certificatesv1.CertificateApproved {
			cm.log.Info("CSR already approved", "csrName", csrName)
			return nil
		}
		if condition.Type == certificatesv1.CertificateDenied {
			return fmt.Errorf("CSR was denied: %s", condition.Message)
		}
	}

	// Add approval condition
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateApproved,
		Status:         "True",
		Reason:         "KubeoceanProxierAutoApproval",
		Message:        fmt.Sprintf("Automatically approved by Kubeocean Proxier for ClusterBinding %s", cm.clusterBinding.Name),
		LastUpdateTime: metav1.Time{Time: time.Now()},
	})

	// Update CSR with approval
	_, err = cm.client.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csrName, csr, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to approve CSR: %w", err)
	}

	cm.log.Info("Successfully approved CSR", "csrName", csrName)
	return nil
}

// waitForCertificateWithRetry waits for certificate to be issued with retry mechanism
func (cm *CertificateManager) waitForCertificateWithRetry(ctx context.Context, csrName string, timeout time.Duration) ([]byte, error) {
	// Try up to 3 times with exponential backoff
	maxRetries := 3
	baseDelay := 5 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		cm.log.Info("Attempting to wait for certificate",
			"csrName", csrName,
			"attempt", attempt,
			"maxRetries", maxRetries)

		certificate, err := cm.waitForCertificate(ctx, csrName, timeout)
		if err == nil {
			return certificate, nil
		}

		// Check if it's a CSR deleted error (race condition)
		if strings.Contains(err.Error(), "CSR was deleted while waiting for certificate") {
			cm.log.V(1).Info("CSR was deleted during certificate wait (race condition detected)",
				"csrName", csrName,
				"attempt", attempt,
				"error", err.Error())

			// If this is not the last attempt, retry with a new CSR
			if attempt < maxRetries {
				cm.log.Info("Retrying certificate generation due to CSR race condition",
					"csrName", csrName,
					"attempt", attempt)

				// Wait with exponential backoff before retry
				delay := time.Duration(attempt) * baseDelay
				cm.log.Info("Waiting before retry", "delay", delay.String())

				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("context cancelled during retry wait: %w", ctx.Err())
				case <-time.After(delay):
					// Continue to next iteration
				}

				// Return error to trigger full retry in generateCertificateData
				return nil, fmt.Errorf("CSR race condition detected, retry needed: %w", err)
			}
		}

		// For other errors, don't retry
		if attempt == maxRetries {
			return nil, fmt.Errorf("failed to get certificate after %d attempts: %w", maxRetries, err)
		}
	}

	return nil, fmt.Errorf("unexpected error in retry loop")
}

// waitForCertificate waits for certificate to be issued
func (cm *CertificateManager) waitForCertificate(ctx context.Context, csrName string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cm.log.Info("Waiting for certificate to be issued", "csrName", csrName)

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting for certificate")
		case <-time.After(2 * time.Second):
			csr, err := cm.client.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					return nil, fmt.Errorf("CSR was deleted while waiting for certificate")
				}
				continue
			}

			// Check if certificate is available
			if len(csr.Status.Certificate) > 0 {
				cm.log.Info("Certificate issued successfully", "csrName", csrName)
				return csr.Status.Certificate, nil
			}

			// Check if denied
			for _, condition := range csr.Status.Conditions {
				if condition.Type == certificatesv1.CertificateDenied {
					return nil, fmt.Errorf("certificate request was denied: %s", condition.Message)
				}
			}
		}
	}
}

// createTLSSecret creates TLS secret with certificate and private key
func (cm *CertificateManager) createTLSSecret(ctx context.Context, secretName string, certificate []byte, privateKey *rsa.PrivateKey) (*corev1.Secret, error) {
	// Encode private key to PEM
	privateKeyDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyDER,
	})

	// Create secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cm.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "kubeocean-proxier",
				"app.kubernetes.io/instance":  cm.clusterBinding.Name,
				"app.kubernetes.io/component": "tls",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certificate,
			"tls.key": privateKeyPEM,
		},
	}

	return cm.client.CoreV1().Secrets(cm.namespace).Create(ctx, secret, metav1.CreateOptions{})
}

// StartAutoRenewal starts the automatic certificate renewal process
func (cm *CertificateManager) StartAutoRenewal(ctx context.Context) {
	go cm.renewalLoop(ctx)
}

// StopAutoRenewal stops the automatic certificate renewal process
func (cm *CertificateManager) StopAutoRenewal() {
	close(cm.stopCh)
}

// renewalLoop runs the certificate renewal check loop
func (cm *CertificateManager) renewalLoop(ctx context.Context) {
	// Check every 6 hours
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	cm.log.Info("Starting certificate auto-renewal loop")

	// Initial check after 1 minute (allow system to stabilize)
	initialTimer := time.NewTimer(1 * time.Minute)
	defer initialTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			cm.log.Info("Certificate renewal loop stopped due to context cancellation")
			return
		case <-cm.stopCh:
			cm.log.Info("Certificate renewal loop stopped")
			return
		case <-initialTimer.C:
			cm.checkAndRenewCertificate(ctx)
			initialTimer.Stop() // Only run once
		case <-ticker.C:
			cm.checkAndRenewCertificate(ctx)
		}
	}
}

// checkAndRenewCertificate checks if certificate needs renewal and renews if necessary
func (cm *CertificateManager) checkAndRenewCertificate(ctx context.Context) {
	cm.renewalMutex.Lock()
	defer cm.renewalMutex.Unlock()

	cm.log.V(1).Info("Checking certificate for renewal")

	// Skip if manual secret is specified (external management)
	if secretName := cm.getSpecifiedSecretName(); secretName != "" {
		cm.log.V(1).Info("Using externally managed secret, skipping auto-renewal", "secretName", secretName)
		return
	}

	// Check default secret
	defaultSecretName := fmt.Sprintf("%s-logs-proxy-tls", cm.clusterBinding.Spec.ClusterID)
	secret, err := cm.getExistingSecret(ctx, defaultSecretName)
	if err != nil {
		cm.log.Error(err, "Failed to get secret for renewal check", "secretName", defaultSecretName)
		return
	}

	// Check if certificate needs renewal
	needsRenewal, timeUntilExpiry, err := cm.needsCertificateRenewal(secret)
	if err != nil {
		cm.log.Error(err, "Failed to check certificate renewal need", "secretName", defaultSecretName)
		return
	}

	if !needsRenewal {
		cm.log.V(1).Info("Certificate does not need renewal",
			"secretName", defaultSecretName,
			"timeUntilExpiry", timeUntilExpiry.String())
		return
	}

	cm.log.Info("Certificate needs renewal, starting renewal process",
		"secretName", defaultSecretName,
		"timeUntilExpiry", timeUntilExpiry.String())

	// Perform renewal
	err = cm.renewCertificate(ctx, defaultSecretName)
	if err != nil {
		cm.log.Error(err, "Failed to renew certificate", "secretName", defaultSecretName)
		return
	}

	cm.log.Info("Certificate renewed successfully", "secretName", defaultSecretName)
}

// needsCertificateRenewal checks if the certificate in the secret needs renewal
func (cm *CertificateManager) needsCertificateRenewal(secret *corev1.Secret) (bool, time.Duration, error) {
	certData, exists := secret.Data["tls.crt"]
	if !exists {
		return false, 0, fmt.Errorf("secret missing tls.crt")
	}

	// Parse certificate
	block, _ := pem.Decode(certData)
	if block == nil {
		return false, 0, fmt.Errorf("failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, 0, fmt.Errorf("failed to parse certificate: %w", err)
	}

	now := time.Now()
	timeUntilExpiry := cert.NotAfter.Sub(now)

	// Renew if certificate expires within 30 days
	renewalThreshold := 30 * 24 * time.Hour
	needsRenewal := timeUntilExpiry <= renewalThreshold

	return needsRenewal, timeUntilExpiry, nil
}

// renewCertificate renews an existing certificate
func (cm *CertificateManager) renewCertificate(ctx context.Context, secretName string) error {
	// Generate new certificate data
	certificate, privateKey, err := cm.generateCertificateData(ctx)
	if err != nil {
		return fmt.Errorf("failed to generate new certificate: %w", err)
	}

	// Encode private key to PEM
	privateKeyDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyDER,
	})

	// Get the existing secret
	existingSecret, err := cm.client.CoreV1().Secrets(cm.namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing secret: %w", err)
	}

	// Update existing secret with new certificate data
	existingSecret.Data = map[string][]byte{
		"tls.crt": certificate,
		"tls.key": privateKeyPEM,
	}

	// Add renewal annotations
	if existingSecret.Annotations == nil {
		existingSecret.Annotations = make(map[string]string)
	}
	existingSecret.Annotations["kubeocean.io/certificate-renewed"] = time.Now().Format(time.RFC3339)
	existingSecret.Annotations["kubeocean.io/renewal-reason"] = "auto-renewal"

	_, err = cm.client.CoreV1().Secrets(cm.namespace).Update(ctx, existingSecret, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update existing secret: %w", err)
	}

	return nil
}

// GetCertificateInfo returns information about the current certificate
func (cm *CertificateManager) GetCertificateInfo(ctx context.Context) (*CertificateInfo, error) {
	var secretName string

	// Check if manual secret is specified
	if specifiedName := cm.getSpecifiedSecretName(); specifiedName != "" {
		secretName = specifiedName
	} else {
		secretName = fmt.Sprintf("%s-logs-proxy-tls", cm.clusterBinding.Spec.ClusterID)
	}

	secret, err := cm.getExistingSecret(ctx, secretName)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	certData, exists := secret.Data["tls.crt"]
	if !exists {
		return nil, fmt.Errorf("secret missing tls.crt")
	}

	// Parse certificate
	block, _ := pem.Decode(certData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	info := &CertificateInfo{
		SecretName:      secretName,
		SecretNamespace: secret.Namespace,
		Subject:         cert.Subject.String(),
		Issuer:          cert.Issuer.String(),
		SerialNumber:    cert.SerialNumber.String(),
		NotBefore:       cert.NotBefore,
		NotAfter:        cert.NotAfter,
		DNSNames:        cert.DNSNames,
		IPAddresses:     make([]string, len(cert.IPAddresses)),
		IsExpired:       time.Now().After(cert.NotAfter),
		TimeUntilExpiry: time.Until(cert.NotAfter),
		IsAutoManaged:   cm.getSpecifiedSecretName() == "", // True if not manually specified
		LastRenewal:     getLastRenewalTime(secret),
	}

	for i, ip := range cert.IPAddresses {
		info.IPAddresses[i] = ip.String()
	}

	return info, nil
}

// CertificateInfo contains information about a certificate
type CertificateInfo struct {
	SecretName      string
	SecretNamespace string
	Subject         string
	Issuer          string
	SerialNumber    string
	NotBefore       time.Time
	NotAfter        time.Time
	DNSNames        []string
	IPAddresses     []string
	IsExpired       bool
	TimeUntilExpiry time.Duration
	IsAutoManaged   bool
	LastRenewal     *time.Time
}

// getLastRenewalTime extracts last renewal time from secret annotations
func getLastRenewalTime(secret *corev1.Secret) *time.Time {
	if secret.Annotations == nil {
		return nil
	}

	renewalTimeStr, exists := secret.Annotations["kubeocean.io/certificate-renewed"]
	if !exists {
		return nil
	}

	renewalTime, err := time.Parse(time.RFC3339, renewalTimeStr)
	if err != nil {
		return nil
	}

	return &renewalTime
}

// CleanupCertificates cleans up auto-managed certificates when ClusterBinding is being deleted
func (cm *CertificateManager) CleanupCertificates(ctx context.Context) error {
	cm.log.Info("Starting certificate cleanup for ClusterBinding", "clusterBinding", cm.clusterBinding.Name)

	// Only cleanup auto-managed certificates, not externally specified ones
	if secretName := cm.getSpecifiedSecretName(); secretName != "" {
		cm.log.Info("Skipping cleanup - certificate is externally managed", "secretName", secretName)
		return nil
	}

	// Cleanup auto-generated certificate
	defaultSecretName := fmt.Sprintf("%s-logs-proxy-tls", cm.clusterBinding.Spec.ClusterID)

	return cm.cleanupAutoManagedSecret(ctx, defaultSecretName)
}

// cleanupAutoManagedSecret removes auto-managed TLS secret
func (cm *CertificateManager) cleanupAutoManagedSecret(ctx context.Context, secretName string) error {
	cm.log.Info("Cleaning up auto-managed TLS secret", "secretName", secretName)

	// First check if the secret exists and is auto-managed
	secret, err := cm.client.CoreV1().Secrets(cm.namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			cm.log.V(1).Info("Secret not found, no cleanup needed", "secretName", secretName)
			return nil
		}
		return fmt.Errorf("failed to get secret for cleanup: %w", err)
	}

	// Verify this is our auto-managed secret by checking labels
	if !cm.isAutoManagedSecret(secret) {
		cm.log.Info("Secret is not auto-managed, skipping cleanup", "secretName", secretName)
		return nil
	}

	// Delete the secret
	err = cm.client.CoreV1().Secrets(cm.namespace).Delete(ctx, secretName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete auto-managed secret: %w", err)
	}

	cm.log.Info("Successfully cleaned up auto-managed TLS secret", "secretName", secretName)
	return nil
}

// isAutoManagedSecret checks if a secret is auto-managed by checking labels
func (cm *CertificateManager) isAutoManagedSecret(secret *corev1.Secret) bool {
	if secret.Labels == nil {
		return false
	}

	// Check for our specific labels that indicate auto-management
	appName, hasAppName := secret.Labels["app.kubernetes.io/name"]
	instance, hasInstance := secret.Labels["app.kubernetes.io/instance"]
	component, hasComponent := secret.Labels["app.kubernetes.io/component"]

	return hasAppName && appName == "kubeocean-proxier" &&
		hasInstance && instance == cm.clusterBinding.Name &&
		hasComponent && component == "tls"
}

// CleanupOrphanedCSRs cleans up any orphaned CSRs that might have been left behind
func (cm *CertificateManager) CleanupOrphanedCSRs(ctx context.Context) error {
	cm.log.Info("Cleaning up orphaned CSRs for ClusterBinding", "clusterBinding", cm.clusterBinding.Name)

	// List all CSRs with our naming pattern
	csrList, err := cm.client.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list CSRs: %w", err)
	}

	csrPrefix := fmt.Sprintf("kubeocean-proxier-%s-", cm.clusterBinding.Name)
	cleanedCount := 0

	for _, csr := range csrList.Items {
		if strings.HasPrefix(csr.Name, csrPrefix) {
			cm.log.Info("Found orphaned CSR, cleaning up", "csrName", csr.Name)

			err := cm.client.CertificatesV1().CertificateSigningRequests().Delete(ctx, csr.Name, metav1.DeleteOptions{})
			if err != nil && !errors.IsNotFound(err) {
				cm.log.Error(err, "Failed to delete orphaned CSR", "csrName", csr.Name)
				continue
			}

			cleanedCount++
			cm.log.Info("Successfully cleaned up orphaned CSR", "csrName", csr.Name)
		}
	}

	if cleanedCount > 0 {
		cm.log.Info("Cleaned up orphaned CSRs", "count", cleanedCount)
	} else {
		cm.log.V(1).Info("No orphaned CSRs found")
	}

	return nil
}

// ForceCleanupAll performs a complete cleanup of all certificate-related resources
// This should only be used when ClusterBinding is being deleted
func (cm *CertificateManager) ForceCleanupAll(ctx context.Context) error {
	cm.log.Info("Performing complete certificate cleanup", "clusterBinding", cm.clusterBinding.Name)

	var errors []error

	// 1. Cleanup certificates (respects external management)
	if err := cm.CleanupCertificates(ctx); err != nil {
		errors = append(errors, fmt.Errorf("certificate cleanup failed: %w", err))
	}

	// 2. Cleanup orphaned CSRs
	if err := cm.CleanupOrphanedCSRs(ctx); err != nil {
		errors = append(errors, fmt.Errorf("CSR cleanup failed: %w", err))
	}

	// 3. Stop any running renewal process
	cm.StopAutoRenewal()

	if len(errors) > 0 {
		// Return combined error
		var errMsgs []string
		for _, err := range errors {
			errMsgs = append(errMsgs, err.Error())
		}
		return fmt.Errorf("cleanup completed with errors: %s", strings.Join(errMsgs, "; "))
	}

	cm.log.Info("Complete certificate cleanup successful", "clusterBinding", cm.clusterBinding.Name)
	return nil
}
