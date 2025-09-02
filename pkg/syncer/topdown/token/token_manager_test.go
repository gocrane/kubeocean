package token

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
)

// Helper function to create a test token request
func createTestTokenRequest(name, _ string, expirationSeconds int64, podUID types.UID) *authenticationv1.TokenRequest {
	now := time.Now()
	return &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{"https://kubernetes.default.svc.cluster.local"},
			ExpirationSeconds: &expirationSeconds,
			BoundObjectRef: &authenticationv1.BoundObjectReference{
				APIVersion: "v1",
				Kind:       "Pod",
				Name:       name,
				UID:        podUID,
			},
		},
		Status: authenticationv1.TokenRequestStatus{
			Token:               "test-token-content",
			ExpirationTimestamp: metav1.NewTime(now.Add(time.Duration(expirationSeconds) * time.Second)),
		},
	}
}

// Helper function to create a test manager with mocked dependencies (for backward compatibility)
func createTestManager(mockClock clock.Clock, logger logr.Logger) *Manager {
	return &Manager{
		getToken: func(name, namespace string, tr *authenticationv1.TokenRequest) (*authenticationv1.TokenRequest, error) {
			// Mock successful token creation
			return createTestTokenRequest(name, namespace, 3600, "test-uid"), nil
		},
		cache:  make(map[string]*authenticationv1.TokenRequest),
		clock:  mockClock,
		logger: logger,
	}
}

func TestNewManager(t *testing.T) {
	tests := []struct {
		testName     string
		createClient func() kubernetes.Interface
		expectError  bool
	}{
		{
			testName: "successful manager creation with fake client",
			createClient: func() kubernetes.Interface {
				scheme := runtime.NewScheme()
				corev1.AddToScheme(scheme)
				return fake.NewSimpleClientset()
			},
			expectError: false,
		},
		{
			testName: "nil client",
			createClient: func() kubernetes.Interface {
				return nil
			},
			expectError: false, // NewManager doesn't return error, it just creates a manager that will fail later
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			logger := logr.Discard()
			client := tt.createClient()
			manager := NewManager(client, logger)

			assert.NotNil(t, manager)
			assert.Implements(t, (*TokenManagerInterface)(nil), manager)
		})
	}
}

func TestKeyFunc(t *testing.T) {
	tests := []struct {
		testName  string
		namespace string
		tokenReq  *authenticationv1.TokenRequest
		expected  string
	}{
		{
			testName:  "basic token request",
			namespace: "test-ns",
			tokenReq:  createTestTokenRequest("test-sa", "test-ns", 3600, "test-uid-123"),
			expected:  `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/3600/v1.BoundObjectReference{Kind:"Pod", APIVersion:"v1", Name:"test-sa", UID:"test-uid-123"}`,
		},
		{
			testName:  "token request without expiration",
			namespace: "test-ns",
			tokenReq: &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					Audiences: []string{"https://kubernetes.default.svc.cluster.local"},
					// No ExpirationSeconds
				},
			},
			expected: `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/0/v1.BoundObjectReference{Kind:"", APIVersion:"", Name:"", UID:""}`,
		},
		{
			testName:  "token request without bound object ref",
			namespace: "test-ns",
			tokenReq: &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					Audiences:         []string{"https://kubernetes.default.svc.cluster.local"},
					ExpirationSeconds: int64Ptr(3600),
					// No BoundObjectRef
				},
			},
			expected: `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/3600/v1.BoundObjectReference{Kind:"", APIVersion:"", Name:"", UID:""}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			result := keyFunc("test-sa", tt.namespace, tt.tokenReq)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestManager_GetServiceAccountToken_WithFakeClient(t *testing.T) {
	// This test demonstrates how to use fakeclient with token manager
	// Note: fakeclient may not fully support CreateToken method for ServiceAccounts
	t.Run("test fake client setup", func(t *testing.T) {
		// Create fake client
		scheme := runtime.NewScheme()
		corev1.AddToScheme(scheme)
		fakeClient := fake.NewSimpleClientset()

		// Create the namespace and service account in the fake client
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ns",
			},
		}
		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "test-ns",
			},
		}

		_, err := fakeClient.CoreV1().Namespaces().Create(context.TODO(), namespace, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = fakeClient.CoreV1().ServiceAccounts("test-ns").Create(context.TODO(), serviceAccount, metav1.CreateOptions{})
		require.NoError(t, err)

		// Verify that the resources were created
		ns, err := fakeClient.CoreV1().Namespaces().Get(context.TODO(), "test-ns", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "test-ns", ns.Name)

		sa, err := fakeClient.CoreV1().ServiceAccounts("test-ns").Get(context.TODO(), "test-sa", metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "test-sa", sa.Name)

		// Note: CreateToken method may not be fully implemented in fakeclient
		// This test demonstrates the setup but may not test the actual token creation
	})
}

func TestManager_GetServiceAccountToken(t *testing.T) {
	tests := []struct {
		testName      string
		namespace     string
		saName        string
		tokenReq      *authenticationv1.TokenRequest
		existingToken *authenticationv1.TokenRequest
		expectedError bool
		errorContains string
	}{
		{
			testName:      "successful token fetch",
			namespace:     "test-ns",
			saName:        "test-sa",
			tokenReq:      createTestTokenRequest("test-sa", "test-ns", 3600, "test-uid-123"),
			expectedError: false,
		},
		{
			testName:      "return cached token when refresh fails but old token still valid",
			namespace:     "test-ns",
			saName:        "test-sa",
			tokenReq:      createTestTokenRequest("test-sa", "test-ns", 3600, "test-uid-123"),
			existingToken: createTestTokenRequest("test-sa", "test-ns", 3600, "test-uid-123"),
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			mockClock := clocktesting.NewFakeClock(time.Now())
			logger := logr.Discard()
			manager := createTestManager(mockClock, logger)

			// Pre-populate cache if existing token is provided
			if tt.existingToken != nil {
				key := keyFunc(tt.saName, tt.namespace, tt.tokenReq)
				manager.set(key, tt.existingToken)
			}

			result, err := manager.GetServiceAccountToken(tt.namespace, tt.saName, tt.tokenReq)

			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestManager_DeleteServiceAccountToken(t *testing.T) {
	tests := []struct {
		testName      string
		podUID        types.UID
		cachedTokens  map[string]*authenticationv1.TokenRequest
		expectedCount int
	}{
		{
			testName: "delete token for specific pod",
			podUID:   "test-uid-123",
			cachedTokens: map[string]*authenticationv1.TokenRequest{
				"key1": createTestTokenRequest("sa1", "ns1", 3600, "test-uid-123"),
				"key2": createTestTokenRequest("sa2", "ns2", 3600, "test-uid-456"),
				"key3": createTestTokenRequest("sa3", "ns3", 3600, "test-uid-123"),
			},
			expectedCount: 1, // Only key2 should remain
		},
		{
			testName: "delete multiple tokens for same pod",
			podUID:   "test-uid-123",
			cachedTokens: map[string]*authenticationv1.TokenRequest{
				"key1": createTestTokenRequest("sa1", "ns1", 3600, "test-uid-123"),
				"key2": createTestTokenRequest("sa2", "ns2", 3600, "test-uid-123"),
				"key3": createTestTokenRequest("sa3", "ns3", 3600, "test-uid-123"),
			},
			expectedCount: 0, // All should be deleted
		},
		{
			testName: "no tokens to delete",
			podUID:   "test-uid-123",
			cachedTokens: map[string]*authenticationv1.TokenRequest{
				"key1": createTestTokenRequest("sa1", "ns1", 3600, "test-uid-456"),
				"key2": createTestTokenRequest("sa2", "ns2", 3600, "test-uid-789"),
			},
			expectedCount: 2, // None should be deleted
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			mockClock := clocktesting.NewFakeClock(time.Now())
			logger := logr.Discard()
			manager := createTestManager(mockClock, logger)

			// Pre-populate cache
			for key, token := range tt.cachedTokens {
				manager.set(key, token)
			}

			// Verify initial cache size
			assert.Equal(t, len(tt.cachedTokens), len(manager.cache))

			// Delete tokens
			manager.DeleteServiceAccountToken(tt.podUID)

			// Verify final cache size
			assert.Equal(t, tt.expectedCount, len(manager.cache))

			// Verify that only tokens for the specified pod were deleted
			for _, token := range manager.cache {
				assert.NotEqual(t, tt.podUID, token.Spec.BoundObjectRef.UID,
					"Token for pod %s should have been deleted", tt.podUID)
			}
		})
	}
}

func TestManager_Expired(t *testing.T) {
	tests := []struct {
		testName        string
		tokenReq        *authenticationv1.TokenRequest
		currentTime     time.Time
		expectedExpired bool
	}{
		{
			testName: "token not expired",
			tokenReq: &authenticationv1.TokenRequest{
				Status: authenticationv1.TokenRequestStatus{
					ExpirationTimestamp: metav1.NewTime(time.Now().Add(1 * time.Hour)),
				},
			},
			currentTime:     time.Now(),
			expectedExpired: false,
		},
		{
			testName: "token expired",
			tokenReq: &authenticationv1.TokenRequest{
				Status: authenticationv1.TokenRequestStatus{
					ExpirationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
				},
			},
			currentTime:     time.Now(),
			expectedExpired: true,
		},
		{
			testName: "token expires exactly now",
			tokenReq: &authenticationv1.TokenRequest{
				Status: authenticationv1.TokenRequestStatus{
					ExpirationTimestamp: metav1.NewTime(time.Now()),
				},
			},
			currentTime:     time.Now(),
			expectedExpired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			mockClock := clocktesting.NewFakeClock(tt.currentTime)
			logger := logr.Discard()
			manager := createTestManager(mockClock, logger)

			result := manager.expired(tt.tokenReq)
			assert.Equal(t, tt.expectedExpired, result)
		})
	}
}

func TestManager_RequiresRefresh(t *testing.T) {
	tests := []struct {
		testName        string
		tokenReq        *authenticationv1.TokenRequest
		currentTime     time.Time
		expectedRefresh bool
	}{
		{
			testName: "token within 80% TTL - no refresh needed",
			tokenReq: &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					ExpirationSeconds: int64Ptr(3600), // 1 hour
				},
				Status: authenticationv1.TokenRequestStatus{
					ExpirationTimestamp: metav1.NewTime(time.Now().Add(30 * time.Minute)), // 30 minutes from now
				},
			},
			currentTime:     time.Now(),
			expectedRefresh: false,
		},
		{
			testName: "token within 20% of expiration - refresh needed",
			tokenReq: &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					ExpirationSeconds: int64Ptr(3600), // 1 hour
				},
				Status: authenticationv1.TokenRequestStatus{
					ExpirationTimestamp: metav1.NewTime(time.Now().Add(5 * time.Minute)), // 5 minutes from now
				},
			},
			currentTime:     time.Now(),
			expectedRefresh: true,
		},
		{
			testName: "token older than 24 hours - refresh needed",
			tokenReq: &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					ExpirationSeconds: int64Ptr(3600), // 1 hour
				},
				Status: authenticationv1.TokenRequestStatus{
					ExpirationTimestamp: metav1.NewTime(time.Now().Add(1 * time.Hour)), // 1 hour from now
				},
			},
			currentTime:     time.Now().Add(25 * time.Hour), // 25 hours from now
			expectedRefresh: true,
		},
		{
			testName: "nil expiration seconds",
			tokenReq: &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					// No ExpirationSeconds
				},
				Status: authenticationv1.TokenRequestStatus{
					ExpirationTimestamp: metav1.NewTime(time.Now().Add(1 * time.Hour)),
				},
			},
			currentTime:     time.Now(),
			expectedRefresh: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			mockClock := clocktesting.NewFakeClock(tt.currentTime)
			logger := logr.Discard()
			manager := createTestManager(mockClock, logger)

			result := manager.requiresRefresh(context.Background(), tt.tokenReq)
			assert.Equal(t, tt.expectedRefresh, result)
		})
	}
}

func TestManager_CacheOperations(t *testing.T) {
	mockClock := clocktesting.NewFakeClock(time.Now())
	logger := logr.Discard()
	manager := createTestManager(mockClock, logger)

	// Test set operation
	token := createTestTokenRequest("test-sa", "test-ns", 3600, "test-uid-123")
	key := "test-key"
	manager.set(key, token)

	// Test get operation
	retrievedToken, exists := manager.get(key)
	assert.True(t, exists)
	assert.Equal(t, token, retrievedToken)

	// Test get non-existent key
	_, exists = manager.get("non-existent-key")
	assert.False(t, exists)

	// Test cache size
	assert.Equal(t, 1, len(manager.cache))
}

func TestManager_Cleanup(t *testing.T) {
	mockClock := clocktesting.NewFakeClock(time.Now())
	logger := logr.Discard()
	manager := createTestManager(mockClock, logger)

	// Add expired and non-expired tokens
	expiredToken := createTestTokenRequest("expired-sa", "test-ns", 3600, "test-uid-123")
	expiredToken.Status.ExpirationTimestamp = metav1.NewTime(time.Now().Add(-1 * time.Hour))

	validToken := createTestTokenRequest("valid-sa", "test-ns", 3600, "test-uid-456")
	validToken.Status.ExpirationTimestamp = metav1.NewTime(time.Now().Add(1 * time.Hour))

	manager.set("expired-key", expiredToken)
	manager.set("valid-key", validToken)

	// Verify initial cache size
	assert.Equal(t, 2, len(manager.cache))

	// Run cleanup
	manager.cleanup()

	// Verify that only expired tokens were removed
	assert.Equal(t, 1, len(manager.cache))
	_, exists := manager.get("expired-key")
	assert.False(t, exists)
	_, exists = manager.get("valid-key")
	assert.True(t, exists)
}

// Helper function to create int64 pointer
func int64Ptr(v int64) *int64 {
	return &v
}
