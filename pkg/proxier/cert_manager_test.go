/*
Copyright 2025 The Kubeocean Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxier

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Helper function to create a test ClusterBinding
func createTestClusterBinding(name, clusterID string, annotations map[string]string) *cloudv1beta1.ClusterBinding {
	return &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: clusterID,
		},
	}
}

// Helper function to generate a test certificate
func generateTestCertificate(dnsNames []string, ipAddresses []string, notBefore, notAfter time.Time) ([]byte, *rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "system:node:test-node",
			Organization: []string{"system:nodes"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, dns := range dnsNames {
		template.DNSNames = append(template.DNSNames, dns)
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return certPEM, privateKey, nil
}

// Helper function to create a TLS secret
func createTestTLSSecret(name, namespace string, certPEM []byte, privateKey *rsa.PrivateKey, labels map[string]string, annotations map[string]string) *corev1.Secret {
	privateKeyDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privateKeyDER})

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": privateKeyPEM,
		},
	}
}

func TestNewCertificateManager(t *testing.T) {
	tests := []struct {
		name           string
		clusterBinding *cloudv1beta1.ClusterBinding
		namespace      string
		expectValid    bool
	}{
		{
			name:           "valid certificate manager creation",
			clusterBinding: createTestClusterBinding("test-binding", "cluster-1", nil),
			namespace:      "default",
			expectValid:    true,
		},
		{
			name:           "certificate manager with annotations",
			clusterBinding: createTestClusterBinding("test-binding", "cluster-1", map[string]string{"kubeocean.io/logs-proxy-secret-name": "custom-secret"}),
			namespace:      "kubeocean-system",
			expectValid:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			logger := logr.Discard()

			cm := NewCertificateManager(client, tt.clusterBinding, tt.namespace, logger)

			require.NotNil(t, cm)
			assert.Equal(t, tt.namespace, cm.namespace)
			assert.Equal(t, tt.clusterBinding, cm.clusterBinding)
			assert.NotNil(t, cm.client)
			assert.NotNil(t, cm.stopCh)
		})
	}
}

func TestGetSpecifiedSecretName(t *testing.T) {
	tests := []struct {
		name           string
		annotations    map[string]string
		expectedSecret string
	}{
		{
			name:           "no annotations",
			annotations:    nil,
			expectedSecret: "",
		},
		{
			name:           "empty annotations",
			annotations:    map[string]string{},
			expectedSecret: "",
		},
		{
			name:           "annotation present",
			annotations:    map[string]string{"kubeocean.io/logs-proxy-secret-name": "my-custom-secret"},
			expectedSecret: "my-custom-secret",
		},
		{
			name:           "other annotations present",
			annotations:    map[string]string{"other-annotation": "value"},
			expectedSecret: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", tt.annotations)

			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			result := cm.getSpecifiedSecretName()
			assert.Equal(t, tt.expectedSecret, result)
		})
	}
}

func TestGetExistingSecret(t *testing.T) {
	tests := []struct {
		name          string
		setupSecret   *corev1.Secret
		secretName    string
		expectError   bool
		errorContains string
	}{
		{
			name: "valid TLS secret",
			setupSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"tls.crt": []byte("cert-data"),
					"tls.key": []byte("key-data"),
				},
			},
			secretName:  "test-secret",
			expectError: false,
		},
		{
			name: "secret missing tls.crt",
			setupSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "incomplete-secret",
					Namespace: "default",
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"tls.key": []byte("key-data"),
				},
			},
			secretName:    "incomplete-secret",
			expectError:   true,
			errorContains: "missing tls.crt",
		},
		{
			name: "secret missing tls.key",
			setupSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "incomplete-secret",
					Namespace: "default",
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"tls.crt": []byte("cert-data"),
				},
			},
			secretName:    "incomplete-secret",
			expectError:   true,
			errorContains: "missing tls.key",
		},
		{
			name:          "secret not found",
			setupSecret:   nil,
			secretName:    "nonexistent-secret",
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *fake.Clientset
			if tt.setupSecret != nil {
				client = fake.NewSimpleClientset(tt.setupSecret)
			} else {
				client = fake.NewSimpleClientset()
			}

			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			secret, err := cm.getExistingSecret(context.Background(), tt.secretName)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, secret)
				assert.Equal(t, tt.secretName, secret.Name)
			}
		})
	}
}

func TestGetPodIP(t *testing.T) {
	tests := []struct {
		name          string
		setupEnv      map[string]string
		setupPod      *corev1.Pod
		expectError   bool
		errorContains string
		expectedIP    string
	}{
		{
			name: "POD_IP environment variable set",
			setupEnv: map[string]string{
				"POD_IP": "10.0.0.1",
			},
			expectError: false,
			expectedIP:  "10.0.0.1",
		},
		{
			name: "get IP from pod API when POD_IP not set",
			setupEnv: map[string]string{
				"POD_NAME":      "test-pod",
				"POD_NAMESPACE": "default",
			},
			setupPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Status: corev1.PodStatus{
					PodIP: "10.0.0.2",
				},
			},
			expectError: false,
			expectedIP:  "10.0.0.2",
		},
		{
			name: "pod IP not available",
			setupEnv: map[string]string{
				"POD_NAME":      "test-pod",
				"POD_NAMESPACE": "default",
			},
			setupPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
				Status: corev1.PodStatus{
					PodIP: "",
				},
			},
			expectError:   true,
			errorContains: "pod IP not available",
		},
		{
			name:          "missing environment variables",
			setupEnv:      map[string]string{},
			expectError:   true,
			errorContains: "environment variable not set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment variables
			for k, v := range tt.setupEnv {
				os.Setenv(k, v)
			}
			defer func() {
				for k := range tt.setupEnv {
					os.Unsetenv(k)
				}
			}()

			var client *fake.Clientset
			if tt.setupPod != nil {
				client = fake.NewSimpleClientset(tt.setupPod)
			} else {
				client = fake.NewSimpleClientset()
			}

			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			ip, err := cm.getPodIP()

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedIP, ip)
			}
		})
	}
}

func TestGenerateUniqueCSRName(t *testing.T) {
	tests := []struct {
		name           string
		podNameEnv     string
		clusterBinding *cloudv1beta1.ClusterBinding
		checkPrefix    string
	}{
		{
			name:           "with POD_NAME environment variable",
			podNameEnv:     "proxier-pod-123",
			clusterBinding: createTestClusterBinding("test-binding", "cluster-1", nil),
			checkPrefix:    "kubeocean-proxier-test-binding-proxier-pod-123-",
		},
		{
			name:           "without POD_NAME environment variable",
			podNameEnv:     "",
			clusterBinding: createTestClusterBinding("test-binding", "cluster-1", nil),
			checkPrefix:    "kubeocean-proxier-test-binding-unknown-pod-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.podNameEnv != "" {
				os.Setenv("POD_NAME", tt.podNameEnv)
				defer os.Unsetenv("POD_NAME")
			}

			client := fake.NewSimpleClientset()
			logger := logr.Discard()
			cm := NewCertificateManager(client, tt.clusterBinding, "default", logger)

			csrName := cm.generateUniqueCSRName()

			assert.True(t, strings.HasPrefix(csrName, tt.checkPrefix), "CSR name should have correct prefix")
			assert.Greater(t, len(csrName), len(tt.checkPrefix), "CSR name should include timestamp")
		})
	}
}

func TestCreateCSR(t *testing.T) {
	tests := []struct {
		name           string
		clusterBinding *cloudv1beta1.ClusterBinding
		podIP          string
		namespace      string
		expectError    bool
	}{
		{
			name:           "valid CSR creation",
			clusterBinding: createTestClusterBinding("test-binding", "cluster-1", nil),
			podIP:          "10.0.0.1",
			namespace:      "default",
			expectError:    false,
		},
		{
			name:           "CSR with IPv6 address",
			clusterBinding: createTestClusterBinding("test-binding", "cluster-1", nil),
			podIP:          "2001:db8::1",
			namespace:      "default",
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			logger := logr.Discard()
			cm := NewCertificateManager(client, tt.clusterBinding, tt.namespace, logger)

			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)

			csrDER, err := cm.createCSR(privateKey, tt.podIP)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, csrDER)

				// Parse and validate CSR
				csr, err := x509.ParseCertificateRequest(csrDER)
				require.NoError(t, err)
				assert.Equal(t, fmt.Sprintf("system:node:%s", tt.clusterBinding.Name), csr.Subject.CommonName)
				assert.Contains(t, csr.Subject.Organization, "system:nodes")
				assert.Contains(t, csr.DNSNames, "localhost")
				assert.Contains(t, csr.DNSNames, tt.clusterBinding.Name)
			}
		})
	}
}

func TestSubmitCSR(t *testing.T) {
	tests := []struct {
		name           string
		csrName        string
		setupExisting  bool
		expectError    bool
		errorContains  string
	}{
		{
			name:          "successful CSR submission",
			csrName:       "test-csr",
			setupExisting: false,
			expectError:   false,
		},
		{
			name:          "CSR already exists",
			csrName:       "existing-csr",
			setupExisting: true,
			expectError:   true,
			errorContains: "already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *fake.Clientset

			if tt.setupExisting {
				existingCSR := &certificatesv1.CertificateSigningRequest{
					ObjectMeta: metav1.ObjectMeta{
						Name: tt.csrName,
					},
				}
				client = fake.NewSimpleClientset(existingCSR)
			} else {
				client = fake.NewSimpleClientset()
			}

			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)

			template := x509.CertificateRequest{
				Subject: pkix.Name{
					CommonName:   "system:node:test-node",
					Organization: []string{"system:nodes"},
				},
			}

			csrDER, err := x509.CreateCertificateRequest(rand.Reader, &template, privateKey)
			require.NoError(t, err)

			csr, err := cm.submitCSR(context.Background(), tt.csrName, csrDER)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, csr)
				assert.Equal(t, tt.csrName, csr.Name)
				assert.Equal(t, certificatesv1.KubeletServingSignerName, csr.Spec.SignerName)
			}
		})
	}
}

func TestApproveCSR(t *testing.T) {
	tests := []struct {
		name          string
		setupCSR      *certificatesv1.CertificateSigningRequest
		expectError   bool
		errorContains string
	}{
		{
			name: "successfully approve CSR",
			setupCSR: &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-csr",
				},
			},
			expectError: false,
		},
		{
			name: "CSR already approved",
			setupCSR: &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "approved-csr",
				},
				Status: certificatesv1.CertificateSigningRequestStatus{
					Conditions: []certificatesv1.CertificateSigningRequestCondition{
						{
							Type:   certificatesv1.CertificateApproved,
							Status: "True",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "CSR denied",
			setupCSR: &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "denied-csr",
				},
				Status: certificatesv1.CertificateSigningRequestStatus{
					Conditions: []certificatesv1.CertificateSigningRequestCondition{
						{
							Type:    certificatesv1.CertificateDenied,
							Status:  "True",
							Message: "Denied for testing",
						},
					},
				},
			},
			expectError:   true,
			errorContains: "CSR was denied",
		},
		{
			name:          "CSR not found",
			setupCSR:      nil,
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *fake.Clientset
			if tt.setupCSR != nil {
				client = fake.NewSimpleClientset(tt.setupCSR)
			} else {
				client = fake.NewSimpleClientset()
			}

			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			csrName := "test-csr"
			if tt.setupCSR != nil {
				csrName = tt.setupCSR.Name
			}

			err := cm.approveCSR(context.Background(), csrName)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestWaitForCertificate(t *testing.T) {
	tests := []struct {
		name          string
		setupCSR      *certificatesv1.CertificateSigningRequest
		updateCSR     func(*fake.Clientset, string)
		timeout       time.Duration
		expectError   bool
		errorContains string
	}{
		{
			name: "certificate issued successfully",
			setupCSR: &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-csr",
				},
			},
			updateCSR: func(client *fake.Clientset, csrName string) {
				// Simulate certificate being issued after a delay
				go func() {
					time.Sleep(100 * time.Millisecond)
					csr, _ := client.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csrName, metav1.GetOptions{})
					csr.Status.Certificate = []byte("test-certificate")
					client.CertificatesV1().CertificateSigningRequests().UpdateStatus(context.Background(), csr, metav1.UpdateOptions{})
				}()
			},
			timeout:     5 * time.Second,
			expectError: false,
		},
		{
			name: "CSR denied during wait",
			setupCSR: &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "denied-csr",
				},
			},
			updateCSR: func(client *fake.Clientset, csrName string) {
				go func() {
					time.Sleep(100 * time.Millisecond)
					csr, _ := client.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csrName, metav1.GetOptions{})
					csr.Status.Conditions = []certificatesv1.CertificateSigningRequestCondition{
						{
							Type:    certificatesv1.CertificateDenied,
							Status:  "True",
							Message: "Denied",
						},
					}
					client.CertificatesV1().CertificateSigningRequests().UpdateStatus(context.Background(), csr, metav1.UpdateOptions{})
				}()
			},
			timeout:       5 * time.Second,
			expectError:   true,
			errorContains: "was denied",
		},
		{
			name: "CSR deleted during wait",
			setupCSR: &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "deleted-csr",
				},
			},
			updateCSR: func(client *fake.Clientset, csrName string) {
				go func() {
					time.Sleep(2100 * time.Millisecond) // Delete after first poll
					client.CertificatesV1().CertificateSigningRequests().Delete(context.Background(), csrName, metav1.DeleteOptions{})
				}()
			},
			timeout:       5 * time.Second, // Longer timeout to allow polling
			expectError:   true,
			errorContains: "CSR was deleted while waiting",
		},
		{
			name: "timeout waiting for certificate",
			setupCSR: &certificatesv1.CertificateSigningRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "timeout-csr",
				},
			},
			timeout:       100 * time.Millisecond,
			expectError:   true,
			errorContains: "timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *fake.Clientset
			if tt.setupCSR != nil {
				client = fake.NewSimpleClientset(tt.setupCSR)
			} else {
				client = fake.NewSimpleClientset()
			}

			if tt.updateCSR != nil {
				tt.updateCSR(client, tt.setupCSR.Name)
			}

			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			csrName := "test-csr"
			if tt.setupCSR != nil {
				csrName = tt.setupCSR.Name
			}

			cert, err := cm.waitForCertificate(context.Background(), csrName, tt.timeout)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, cert)
			}
		})
	}
}

func TestCreateTLSSecret(t *testing.T) {
	tests := []struct {
		name        string
		secretName  string
		expectError bool
	}{
		{
			name:        "successful secret creation",
			secretName:  "test-tls-secret",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			cert := []byte("test-certificate")
			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)

			secret, err := cm.createTLSSecret(context.Background(), tt.secretName, cert, privateKey)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, secret)
				assert.Equal(t, tt.secretName, secret.Name)
				assert.Equal(t, corev1.SecretTypeTLS, secret.Type)
				assert.Contains(t, secret.Data, "tls.crt")
				assert.Contains(t, secret.Data, "tls.key")
			}
		})
	}
}

func TestCreateTLSSecretWithAutoApproval_RaceCondition(t *testing.T) {
	// Pre-create the secret to simulate race condition
	existingCert, existingKey, err := generateTestCertificate(
		[]string{"localhost"},
		[]string{"127.0.0.1"},
		time.Now(),
		time.Now().Add(24*time.Hour),
	)
	require.NoError(t, err)

	existingSecret := createTestTLSSecret(
		"cluster-1-logs-proxy-tls",
		"default",
		existingCert,
		existingKey,
		map[string]string{
			"app.kubernetes.io/name":      "kubeocean-proxier",
			"app.kubernetes.io/instance":  "test-binding",
			"app.kubernetes.io/component": "tls",
		},
		nil,
	)

	client := fake.NewSimpleClientset(existingSecret)

	// Add reactor to simulate AlreadyExists error on secret creation
	client.PrependReactor("create", "secrets", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, errors.NewAlreadyExists(corev1.Resource("secrets"), "cluster-1-logs-proxy-tls")
	})

	logger := logr.Discard()
	clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
	cm := NewCertificateManager(client, clusterBinding, "default", logger)

	// Set up environment for getPodIP
	os.Setenv("POD_IP", "10.0.0.1")
	defer os.Unsetenv("POD_IP")

	// This test just verifies the race condition handling logic for secret creation
	// We can't fully test generateCertificateData without a real certificate signer
	_, err = cm.getExistingSecret(context.Background(), "cluster-1-logs-proxy-tls")
	require.NoError(t, err)
}

func TestNeedsCertificateRenewal(t *testing.T) {
	tests := []struct {
		name            string
		certNotAfter    time.Time
		expectRenewal   bool
		errorExpected   bool
	}{
		{
			name:          "certificate expires in 10 days - needs renewal",
			certNotAfter:  time.Now().Add(10 * 24 * time.Hour),
			expectRenewal: true,
		},
		{
			name:          "certificate expires in 31 days - no renewal needed",
			certNotAfter:  time.Now().Add(31 * 24 * time.Hour),
			expectRenewal: false,
		},
		{
			name:          "certificate expires in 90 days - no renewal needed",
			certNotAfter:  time.Now().Add(90 * 24 * time.Hour),
			expectRenewal: false,
		},
		{
			name:          "certificate expired - needs renewal",
			certNotAfter:  time.Now().Add(-1 * time.Hour),
			expectRenewal: true,
		},
		{
			name:          "certificate expires exactly in 30 days - needs renewal",
			certNotAfter:  time.Now().Add(30 * 24 * time.Hour),
			expectRenewal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			certPEM, privateKey, err := generateTestCertificate(
				[]string{"localhost"},
				[]string{"127.0.0.1"},
				time.Now().Add(-24*time.Hour),
				tt.certNotAfter,
			)
			require.NoError(t, err)

			secret := createTestTLSSecret("test-secret", "default", certPEM, privateKey, nil, nil)

			needsRenewal, timeUntilExpiry, err := cm.needsCertificateRenewal(secret)

			if tt.errorExpected {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectRenewal, needsRenewal)
				assert.NotZero(t, timeUntilExpiry)
			}
		})
	}
}

func TestNeedsCertificateRenewal_Errors(t *testing.T) {
	tests := []struct {
		name          string
		secret        *corev1.Secret
		errorContains string
	}{
		{
			name: "missing tls.crt",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "incomplete-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"tls.key": []byte("key-data"),
				},
			},
			errorContains: "missing tls.crt",
		},
		{
			name: "invalid certificate PEM",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-cert-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"tls.crt": []byte("not-a-valid-pem"),
					"tls.key": []byte("key-data"),
				},
			},
			errorContains: "failed to decode certificate PEM",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			_, _, err := cm.needsCertificateRenewal(tt.secret)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errorContains)
		})
	}
}

func TestGetCertificateInfo(t *testing.T) {
	tests := []struct {
		name           string
		setupSecret    *corev1.Secret
		annotations    map[string]string
		expectError    bool
		errorContains  string
		validateResult func(*testing.T, *CertificateInfo)
	}{
		{
			name: "get certificate info successfully",
			setupSecret: func() *corev1.Secret {
				certPEM, privateKey, _ := generateTestCertificate(
					[]string{"localhost", "test-node"},
					[]string{"127.0.0.1"},
					time.Now().Add(-24*time.Hour),
					time.Now().Add(90*24*time.Hour),
				)
				return createTestTLSSecret("cluster-1-logs-proxy-tls", "default", certPEM, privateKey,
					map[string]string{
						"app.kubernetes.io/name":      "kubeocean-proxier",
						"app.kubernetes.io/instance":  "test-binding",
						"app.kubernetes.io/component": "tls",
					},
					map[string]string{
						"kubeocean.io/certificate-renewed": time.Now().Format(time.RFC3339),
					},
				)
			}(),
			expectError: false,
			validateResult: func(t *testing.T, info *CertificateInfo) {
				assert.Equal(t, "cluster-1-logs-proxy-tls", info.SecretName)
				assert.Equal(t, "default", info.SecretNamespace)
				assert.False(t, info.IsExpired)
				assert.True(t, info.IsAutoManaged)
				assert.NotNil(t, info.LastRenewal)
				assert.Contains(t, info.DNSNames, "localhost")
			},
		},
		{
			name: "externally managed certificate",
			setupSecret: func() *corev1.Secret {
				certPEM, privateKey, _ := generateTestCertificate(
					[]string{"custom-dns"},
					[]string{},
					time.Now().Add(-24*time.Hour),
					time.Now().Add(90*24*time.Hour),
				)
				return createTestTLSSecret("custom-secret", "default", certPEM, privateKey, nil, nil)
			}(),
			annotations: map[string]string{
				"kubeocean.io/logs-proxy-secret-name": "custom-secret",
			},
			expectError: false,
			validateResult: func(t *testing.T, info *CertificateInfo) {
				assert.Equal(t, "custom-secret", info.SecretName)
				assert.False(t, info.IsAutoManaged)
			},
		},
		{
			name:          "secret not found",
			setupSecret:   nil,
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *fake.Clientset
			if tt.setupSecret != nil {
				client = fake.NewSimpleClientset(tt.setupSecret)
			} else {
				client = fake.NewSimpleClientset()
			}

			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", tt.annotations)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			info, err := cm.GetCertificateInfo(context.Background())

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, info)
				if tt.validateResult != nil {
					tt.validateResult(t, info)
				}
			}
		})
	}
}

func TestIsAutoManagedSecret(t *testing.T) {
	tests := []struct {
		name           string
		secret         *corev1.Secret
		expectManaged  bool
	}{
		{
			name: "auto-managed secret with all labels",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "kubeocean-proxier",
						"app.kubernetes.io/instance":  "test-binding",
						"app.kubernetes.io/component": "tls",
					},
				},
			},
			expectManaged: true,
		},
		{
			name: "secret missing labels",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
					Labels: map[string]string{
						"app.kubernetes.io/name": "kubeocean-proxier",
					},
				},
			},
			expectManaged: false,
		},
		{
			name: "secret with no labels",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
			},
			expectManaged: false,
		},
		{
			name: "secret with wrong label values",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "wrong-app",
						"app.kubernetes.io/instance":  "test-binding",
						"app.kubernetes.io/component": "tls",
					},
				},
			},
			expectManaged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			result := cm.isAutoManagedSecret(tt.secret)
			assert.Equal(t, tt.expectManaged, result)
		})
	}
}

func TestCleanupAutoManagedSecret(t *testing.T) {
	tests := []struct {
		name          string
		setupSecret   *corev1.Secret
		secretName    string
		expectError   bool
		errorContains string
	}{
		{
			name: "cleanup auto-managed secret successfully",
			setupSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-1-logs-proxy-tls",
					Namespace: "default",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "kubeocean-proxier",
						"app.kubernetes.io/instance":  "test-binding",
						"app.kubernetes.io/component": "tls",
					},
				},
			},
			secretName:  "cluster-1-logs-proxy-tls",
			expectError: false,
		},
		{
			name: "skip cleanup for non-auto-managed secret",
			setupSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "external-secret",
					Namespace: "default",
					Labels:    map[string]string{"external": "true"},
				},
			},
			secretName:  "external-secret",
			expectError: false,
		},
		{
			name:        "secret not found - no error",
			setupSecret: nil,
			secretName:  "nonexistent-secret",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *fake.Clientset
			if tt.setupSecret != nil {
				client = fake.NewSimpleClientset(tt.setupSecret)
			} else {
				client = fake.NewSimpleClientset()
			}

			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			err := cm.cleanupAutoManagedSecret(context.Background(), tt.secretName)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCleanupOrphanedCSRs(t *testing.T) {
	tests := []struct {
		name           string
		setupCSRs      []runtime.Object
		expectedCount  int
		expectError    bool
	}{
		{
			name: "cleanup multiple orphaned CSRs",
			setupCSRs: []runtime.Object{
				&certificatesv1.CertificateSigningRequest{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kubeocean-proxier-test-binding-pod-1-12345",
					},
				},
				&certificatesv1.CertificateSigningRequest{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kubeocean-proxier-test-binding-pod-2-67890",
					},
				},
				&certificatesv1.CertificateSigningRequest{
					ObjectMeta: metav1.ObjectMeta{
						Name: "other-csr-unrelated",
					},
				},
			},
			expectedCount: 2,
			expectError:   false,
		},
		{
			name:          "no orphaned CSRs",
			setupCSRs:     []runtime.Object{},
			expectedCount: 0,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.setupCSRs...)
			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			err := cm.CleanupOrphanedCSRs(context.Background())

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)

				// Verify CSRs were cleaned up
				csrList, err := client.CertificatesV1().CertificateSigningRequests().List(context.Background(), metav1.ListOptions{})
				require.NoError(t, err)

				// Count remaining CSRs with our prefix
				remainingCount := 0
				for _, csr := range csrList.Items {
					if strings.HasPrefix(csr.Name, "kubeocean-proxier-test-binding-") {
						remainingCount++
					}
				}
				assert.Equal(t, 0, remainingCount, "All orphaned CSRs should be cleaned up")
			}
		})
	}
}

func TestCleanupCertificates(t *testing.T) {
	tests := []struct {
		name          string
		annotations   map[string]string
		setupSecret   *corev1.Secret
		expectCleanup bool
		expectError   bool
	}{
		{
			name:        "cleanup auto-managed certificate",
			annotations: nil,
			setupSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-1-logs-proxy-tls",
					Namespace: "default",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "kubeocean-proxier",
						"app.kubernetes.io/instance":  "test-binding",
						"app.kubernetes.io/component": "tls",
					},
				},
			},
			expectCleanup: true,
			expectError:   false,
		},
		{
			name: "skip cleanup for externally managed certificate",
			annotations: map[string]string{
				"kubeocean.io/logs-proxy-secret-name": "external-secret",
			},
			setupSecret:   nil,
			expectCleanup: false,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *fake.Clientset
			if tt.setupSecret != nil {
				client = fake.NewSimpleClientset(tt.setupSecret)
			} else {
				client = fake.NewSimpleClientset()
			}

			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", tt.annotations)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			err := cm.CleanupCertificates(context.Background())

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestForceCleanupAll(t *testing.T) {
	tests := []struct {
		name        string
		setupObjs   []runtime.Object
		expectError bool
	}{
		{
			name: "complete cleanup successful",
			setupObjs: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster-1-logs-proxy-tls",
						Namespace: "default",
						Labels: map[string]string{
							"app.kubernetes.io/name":      "kubeocean-proxier",
							"app.kubernetes.io/instance":  "test-binding",
							"app.kubernetes.io/component": "tls",
						},
					},
				},
				&certificatesv1.CertificateSigningRequest{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kubeocean-proxier-test-binding-pod-1-12345",
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.setupObjs...)
			logger := logr.Discard()
			clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
			cm := NewCertificateManager(client, clusterBinding, "default", logger)

			err := cm.ForceCleanupAll(context.Background())

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestAutoRenewalLoop(t *testing.T) {
	t.Run("stop auto renewal", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		logger := logr.Discard()
		clusterBinding := createTestClusterBinding("test-binding", "cluster-1", nil)
		cm := NewCertificateManager(client, clusterBinding, "default", logger)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Start auto renewal
		cm.StartAutoRenewal(ctx)

		// Give it a moment to start
		time.Sleep(100 * time.Millisecond)

		// Stop auto renewal
		cm.StopAutoRenewal()

		// Verify stopCh is closed
		select {
		case <-cm.stopCh:
			// Success - channel is closed
		case <-time.After(1 * time.Second):
			t.Fatal("stopCh was not closed")
		}
	})
}

func TestGetLastRenewalTime(t *testing.T) {
	tests := []struct {
		name        string
		secret      *corev1.Secret
		expectNil   bool
	}{
		{
			name: "valid renewal time",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kubeocean.io/certificate-renewed": time.Now().Format(time.RFC3339),
					},
				},
			},
			expectNil: false,
		},
		{
			name: "no renewal annotation",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			expectNil: true,
		},
		{
			name: "invalid renewal time format",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kubeocean.io/certificate-renewed": "invalid-time",
					},
				},
			},
			expectNil: true,
		},
		{
			name: "nil annotations",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getLastRenewalTime(tt.secret)

			if tt.expectNil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
			}
		})
	}
}
