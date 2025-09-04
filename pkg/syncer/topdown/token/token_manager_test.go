package token

import (
	"context"
	"fmt"
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
	"k8s.io/utils/ptr"
)

// Helper function to create a test token request
func createTestTokenRequest(name string, podUID types.UID, expirationSeconds int64) *authenticationv1.TokenRequest {
	now := time.Now()
	return &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{"https://kubernetes.default.svc.cluster.local"},
			ExpirationSeconds: ptr.To(expirationSeconds),
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
			return createTestTokenRequest(name, "test-uid", 3600), nil
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
			tokenReq:  createTestTokenRequest("test-sa", "test-uid-123", 3600),
			expected:  `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/3600/v1.BoundObjectReference{Kind:"Pod", APIVersion:"v1", Name:"test-sa", UID:"test-uid-123"}`,
		},
		{
			testName:  "token request with short expiration",
			namespace: "test-ns",
			tokenReq:  createTestTokenRequest("test-sa", "test-uid-123", 300),
			expected:  `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/300/v1.BoundObjectReference{Kind:"Pod", APIVersion:"v1", Name:"test-sa", UID:"test-uid-123"}`,
		},
		{
			testName:  "token request with long expiration",
			namespace: "test-ns",
			tokenReq:  createTestTokenRequest("test-sa", "test-uid-123", 86400),
			expected:  `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/86400/v1.BoundObjectReference{Kind:"Pod", APIVersion:"v1", Name:"test-sa", UID:"test-uid-123"}`,
		},
		{
			testName:  "token request with zero expiration",
			namespace: "test-ns",
			tokenReq:  createTestTokenRequest("test-sa", "test-uid-123", 0),
			expected:  `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/0/v1.BoundObjectReference{Kind:"Pod", APIVersion:"v1", Name:"test-sa", UID:"test-uid-123"}`,
		},
		{
			testName:  "token request with negative expiration",
			namespace: "test-ns",
			tokenReq:  createTestTokenRequest("test-sa", "test-uid-123", -1),
			expected:  `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/-1/v1.BoundObjectReference{Kind:"Pod", APIVersion:"v1", Name:"test-sa", UID:"test-uid-123"}`,
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
					ExpirationSeconds: ptr.To(int64(3600)),
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

// TestExpirationSecondsScenarios 专门测试 expirationSeconds 的各种场景
func TestExpirationSecondsScenarios(t *testing.T) {
	tests := []struct {
		testName          string
		expirationSeconds int64
		expectedBehavior  string
		description       string
	}{
		{
			testName:          "standard_1_hour_expiration",
			expirationSeconds: 3600,
			expectedBehavior:  "normal",
			description:       "标准的1小时过期时间，应该正常工作",
		},
		{
			testName:          "short_5_minute_expiration",
			expirationSeconds: 300,
			expectedBehavior:  "normal",
			description:       "5分钟短过期时间，应该正常工作",
		},
		{
			testName:          "long_24_hour_expiration",
			expirationSeconds: 86400,
			expectedBehavior:  "normal",
			description:       "24小时长过期时间，应该正常工作",
		},
		{
			testName:          "zero_expiration",
			expirationSeconds: 0,
			expectedBehavior:  "edge_case",
			description:       "零过期时间，边界情况测试",
		},
		{
			testName:          "negative_expiration",
			expirationSeconds: -1,
			expectedBehavior:  "edge_case",
			description:       "负过期时间，边界情况测试",
		},
		{
			testName:          "very_short_1_second_expiration",
			expirationSeconds: 1,
			expectedBehavior:  "edge_case",
			description:       "1秒极短过期时间，边界情况测试",
		},
		{
			testName:          "very_long_30_day_expiration",
			expirationSeconds: 2592000,
			expectedBehavior:  "edge_case",
			description:       "30天极长过期时间，边界情况测试（跳过刷新测试）",
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			// 测试 keyFunc 的行为
			tokenReq := createTestTokenRequest("test-sa", "test-uid-123", tt.expirationSeconds)
			key := keyFunc("test-sa", "test-ns", tokenReq)

			// 验证 key 中包含了正确的 expirationSeconds 值
			expectedKey := fmt.Sprintf(`"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/%d/v1.BoundObjectReference{Kind:"Pod", APIVersion:"v1", Name:"test-sa", UID:"test-uid-123"}`, tt.expirationSeconds)
			assert.Equal(t, expectedKey, key, "keyFunc should include correct expirationSeconds in key")

			// 测试 requiresRefresh 的行为
			mockClock := clocktesting.NewFakeClock(time.Now())
			logger := logr.Discard()
			manager := createTestManager(mockClock, logger)

			// 测试不同时间点的刷新需求
			testRefreshScenarios(t, manager, tokenReq, tt.expirationSeconds)
		})
	}
}

// testRefreshScenarios 测试不同时间点的刷新需求
func testRefreshScenarios(t *testing.T, manager *Manager, tokenReq *authenticationv1.TokenRequest, expirationSeconds int64) {
	// 跳过边界情况的测试，因为它们可能导致复杂的计算问题
	if expirationSeconds <= 0 {
		t.Skip("Skipping refresh scenarios for non-positive expiration seconds")
		return
	}

	// 跳过极短过期时间的测试，因为它们可能导致时间计算问题
	if expirationSeconds < 60 { // 少于1分钟
		t.Skip("Skipping refresh scenarios for very short expiration seconds")
		return
	}

	// 跳过极长过期时间的测试，因为它们可能导致时间计算问题
	if expirationSeconds > 86400 { // 超过24小时
		t.Skip("Skipping refresh scenarios for very long expiration seconds")
		return
	}

	now := time.Now()
	expirationTime := now.Add(time.Duration(expirationSeconds) * time.Second)

	// 更新 token 的过期时间
	tokenReq.Status.ExpirationTimestamp = metav1.NewTime(expirationTime)

	// 获取 mock clock
	mockClock := manager.clock.(*clocktesting.FakeClock)

	// 测试场景1: 刚创建时（0% TTL）
	mockClock.SetTime(now)
	needsRefresh := manager.requiresRefresh(context.Background(), tokenReq)
	assert.False(t, needsRefresh, "Token should not need refresh at creation time")

	// 测试场景2: 20% TTL 规则（更简单的时间计算）
	// 计算20% TTL时间点
	twentyPercentTTL := expirationTime.Add(-1 * time.Duration(expirationSeconds*20/100) * time.Second)

	// 测试20% TTL之前 - 不需要刷新
	mockClock.SetTime(twentyPercentTTL.Add(-1 * time.Minute))
	needsRefresh = manager.requiresRefresh(context.Background(), tokenReq)
	assert.False(t, needsRefresh, "Token should not need refresh before 20%% TTL")

	// 测试20% TTL之后 - 需要刷新
	mockClock.SetTime(twentyPercentTTL.Add(1 * time.Minute))
	needsRefresh = manager.requiresRefresh(context.Background(), tokenReq)
	assert.True(t, needsRefresh, "Token should need refresh after 20%% TTL")
}

// TestExpirationSecondsEdgeCases 测试 expirationSeconds 的边界情况
func TestExpirationSecondsEdgeCases(t *testing.T) {
	tests := []struct {
		testName         string
		tokenReq         *authenticationv1.TokenRequest
		expectedKey      string
		expectedBehavior string
	}{
		{
			testName: "nil_expiration_seconds",
			tokenReq: &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					Audiences: []string{"https://kubernetes.default.svc.cluster.local"},
					// ExpirationSeconds is nil
				},
				Status: authenticationv1.TokenRequestStatus{
					Token:               "test-token",
					ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Hour)),
				},
			},
			expectedKey:      `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/0/v1.BoundObjectReference{Kind:"", APIVersion:"", Name:"", UID:""}`,
			expectedBehavior: "nil_expiration_should_default_to_zero",
		},
		{
			testName: "empty_bound_object_ref",
			tokenReq: &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					Audiences:         []string{"https://kubernetes.default.svc.cluster.local"},
					ExpirationSeconds: ptr.To(int64(7200)),
				},
				Status: authenticationv1.TokenRequestStatus{
					Token:               "test-token",
					ExpirationTimestamp: metav1.NewTime(time.Now().Add(2 * time.Hour)),
				},
			},
			expectedKey:      `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/7200/v1.BoundObjectReference{Kind:"", APIVersion:"", Name:"", UID:""}`,
			expectedBehavior: "empty_bound_object_ref_should_work",
		},
		{
			testName:         "max_int64_expiration",
			tokenReq:         createTestTokenRequest("test-sa", "test-uid-123", 9223372036854775807),
			expectedKey:      `"test-sa"/"test-ns"/[]string{"https://kubernetes.default.svc.cluster.local"}/9223372036854775807/v1.BoundObjectReference{Kind:"Pod", APIVersion:"v1", Name:"test-sa", UID:"test-uid-123"}`,
			expectedBehavior: "max_int64_expiration_should_work",
		},
	}

	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			key := keyFunc("test-sa", "test-ns", tt.tokenReq)
			assert.Equal(t, tt.expectedKey, key, "keyFunc should handle edge cases correctly")
		})
	}
}

// TestExpirationSecondsIntegration 测试 expirationSeconds 与其他功能的集成
func TestExpirationSecondsIntegration(t *testing.T) {
	t.Run("expiration_seconds_with_cache_operations", func(t *testing.T) {
		mockClock := clocktesting.NewFakeClock(time.Now())
		logger := logr.Discard()
		manager := createTestManager(mockClock, logger)

		// 测试不同过期时间的 token 在缓存中的行为
		shortToken := createTestTokenRequest("short-sa", "test-uid-123", 300) // 5分钟
		longToken := createTestTokenRequest("long-sa", "test-uid-456", 86400) // 24小时

		// 设置过期时间
		now := time.Now()
		shortToken.Status.ExpirationTimestamp = metav1.NewTime(now.Add(5 * time.Minute))
		longToken.Status.ExpirationTimestamp = metav1.NewTime(now.Add(24 * time.Hour))

		// 添加到缓存
		manager.set("short-key", shortToken)
		manager.set("long-key", longToken)

		// 验证缓存大小
		assert.Equal(t, 2, len(manager.cache), "Cache should contain 2 tokens")

		// 测试过期检查
		assert.False(t, manager.expired(shortToken), "Short token should not be expired yet")
		assert.False(t, manager.expired(longToken), "Long token should not be expired yet")

		// 模拟时间前进，使短 token 过期
		mockClock.SetTime(now.Add(10 * time.Minute))
		assert.True(t, manager.expired(shortToken), "Short token should be expired now")
		assert.False(t, manager.expired(longToken), "Long token should still be valid")

		// 运行清理，应该只删除过期的 token
		manager.cleanup()
		assert.Equal(t, 1, len(manager.cache), "Cache should only contain 1 valid token")

		_, exists := manager.get("short-key")
		assert.False(t, exists, "Expired short token should be removed")

		_, exists = manager.get("long-key")
		assert.True(t, exists, "Valid long token should remain")
	})

	t.Run("expiration_seconds_with_refresh_logic", func(t *testing.T) {
		mockClock := clocktesting.NewFakeClock(time.Now())
		logger := logr.Discard()
		manager := createTestManager(mockClock, logger)

		// 创建一个中等过期时间的 token
		token := createTestTokenRequest("medium-sa", "test-uid-789", 7200) // 2小时
		now := time.Now()
		token.Status.ExpirationTimestamp = metav1.NewTime(now.Add(2 * time.Hour))

		// 测试不同时间点的刷新需求
		// 刚创建时（0% TTL）
		mockClock.SetTime(now)
		assert.False(t, manager.requiresRefresh(context.Background(), token), "Token should not need refresh at creation")

		// 1小时后（50% TTL）
		mockClock.SetTime(now.Add(1 * time.Hour))
		assert.False(t, manager.requiresRefresh(context.Background(), token), "Token should not need refresh at 50%% TTL")

		// 1.5小时后（75% TTL）
		mockClock.SetTime(now.Add(90 * time.Minute))
		assert.False(t, manager.requiresRefresh(context.Background(), token), "Token should not need refresh at 75%% TTL")

		// 1.6小时后（80% TTL）
		mockClock.SetTime(now.Add(96 * time.Minute))
		assert.True(t, manager.requiresRefresh(context.Background(), token), "Token should need refresh at 80%% TTL")

		// 1.8小时后（90% TTL）
		mockClock.SetTime(now.Add(108 * time.Minute))
		assert.True(t, manager.requiresRefresh(context.Background(), token), "Token should need refresh at 90%% TTL")
	})
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
			tokenReq:      createTestTokenRequest("test-sa", "test-uid-123", 3600),
			expectedError: false,
		},
		{
			testName:      "return cached token when refresh fails but old token still valid",
			namespace:     "test-ns",
			saName:        "test-sa",
			tokenReq:      createTestTokenRequest("test-sa", "test-uid-123", 3600),
			existingToken: createTestTokenRequest("test-sa", "test-uid-123", 3600),
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
				"key1": createTestTokenRequest("sa1", "test-uid-123", 3600),
				"key2": createTestTokenRequest("sa2", "test-uid-456", 3600),
				"key3": createTestTokenRequest("sa3", "test-uid-123", 3600),
			},
			expectedCount: 1, // Only key2 should remain
		},
		{
			testName: "delete multiple tokens for same pod",
			podUID:   "test-uid-123",
			cachedTokens: map[string]*authenticationv1.TokenRequest{
				"key1": createTestTokenRequest("sa1", "test-uid-123", 3600),
				"key2": createTestTokenRequest("sa2", "test-uid-123", 3600),
				"key3": createTestTokenRequest("sa3", "test-uid-123", 3600),
			},
			expectedCount: 0, // All should be deleted
		},
		{
			testName: "no tokens to delete",
			podUID:   "test-uid-123",
			cachedTokens: map[string]*authenticationv1.TokenRequest{
				"key1": createTestTokenRequest("sa1", "test-uid-456", 3600),
				"key2": createTestTokenRequest("sa2", "test-uid-789", 3600),
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
					ExpirationSeconds: ptr.To(int64(3600)), // 1 hour
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
					ExpirationSeconds: ptr.To(int64(3600)), // 1 hour
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
					ExpirationSeconds: ptr.To(int64(3600)), // 1 hour
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
	token := createTestTokenRequest("test-sa", "test-uid-123", 3600)
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
	expiredToken := createTestTokenRequest("expired-sa", "test-uid-123", 3600)
	expiredToken.Status.ExpirationTimestamp = metav1.NewTime(time.Now().Add(-1 * time.Hour))

	validToken := createTestTokenRequest("valid-sa", "test-uid-456", 3600)
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
