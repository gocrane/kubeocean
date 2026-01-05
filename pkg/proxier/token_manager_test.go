// Copyright 2025 The Kubeocean Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package proxier

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

// TestNewTokenManager tests TokenManager constructor
func TestNewTokenManager(t *testing.T) {
	t.Run("create with valid config", func(t *testing.T) {
		log := logr.Discard()
		config := &rest.Config{
			Host: "https://kubernetes.default.svc",
		}

		tm := NewTokenManager(log, config)

		require.NotNil(t, tm)
		assert.NotNil(t, tm.Log)
		assert.NotNil(t, tm.RestConfig)
		assert.Equal(t, config, tm.RestConfig)
		assert.Empty(t, tm.Token)
	})

	t.Run("create with nil config", func(t *testing.T) {
		log := logr.Discard()

		tm := NewTokenManager(log, nil)

		require.NotNil(t, tm)
		assert.Nil(t, tm.RestConfig)
		assert.Empty(t, tm.Token)
	})

	t.Run("create with bearer token in config", func(t *testing.T) {
		log := logr.Discard()
		config := &rest.Config{
			Host:        "https://kubernetes.default.svc",
			BearerToken: "test-bearer-token-123",
		}

		tm := NewTokenManager(log, config)

		require.NotNil(t, tm)
		assert.Equal(t, "test-bearer-token-123", tm.RestConfig.BearerToken)
	})
}

// TestExtractAndSaveToken tests the token extraction and saving functionality
func TestExtractAndSaveToken(t *testing.T) {
	ctx := context.Background()
	log := logr.Discard()

	t.Run("extract from bearer token", func(t *testing.T) {
		expectedToken := "my-bearer-token-abc123"
		config := &rest.Config{
			BearerToken: expectedToken,
		}

		tm := NewTokenManager(log, config)
		err := tm.ExtractAndSaveToken(ctx)

		require.NoError(t, err)
		assert.Equal(t, expectedToken, tm.Token)
	})

	t.Run("extract from bearer token file", func(t *testing.T) {
		// Create temporary token file
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token")
		expectedToken := "file-based-token-xyz789"
		
		err := os.WriteFile(tokenFile, []byte(expectedToken), 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(ctx)

		require.NoError(t, err)
		assert.Equal(t, expectedToken, tm.Token)
	})

	t.Run("extract from token file with whitespace", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token")
		expectedToken := "token-with-spaces"
		tokenWithSpaces := "  " + expectedToken + "\n\t"
		
		err := os.WriteFile(tokenFile, []byte(tokenWithSpaces), 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(ctx)

		require.NoError(t, err)
		assert.Equal(t, expectedToken, tm.Token)
	})

	t.Run("extract from token file with multiple newlines", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token")
		expectedToken := "multiline-token"
		tokenWithNewlines := "\n\n" + expectedToken + "\n\n\n"
		
		err := os.WriteFile(tokenFile, []byte(tokenWithNewlines), 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(ctx)

		require.NoError(t, err)
		assert.Equal(t, expectedToken, tm.Token)
	})

	t.Run("bearer token takes precedence over file", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token")
		fileToken := "file-token"
		bearerToken := "bearer-token-priority"
		
		err := os.WriteFile(tokenFile, []byte(fileToken), 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerToken:     bearerToken,
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(ctx)

		require.NoError(t, err)
		assert.Equal(t, bearerToken, tm.Token)
	})

	t.Run("error when no authentication method", func(t *testing.T) {
		config := &rest.Config{
			Host: "https://kubernetes.default.svc",
		}

		tm := NewTokenManager(log, config)
		err := tm.ExtractAndSaveToken(ctx)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no suitable authentication method found")
		assert.Empty(t, tm.Token)
	})

	t.Run("error when token file does not exist", func(t *testing.T) {
		config := &rest.Config{
			BearerTokenFile: "/non/existent/path/token",
		}

		tm := NewTokenManager(log, config)
		err := tm.ExtractAndSaveToken(ctx)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to extract token from config")
		assert.Contains(t, err.Error(), "failed to read token file")
		assert.Empty(t, tm.Token)
	})

	t.Run("error when token file is a directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		config := &rest.Config{
			BearerTokenFile: tmpDir,
		}

		tm := NewTokenManager(log, config)
		err := tm.ExtractAndSaveToken(ctx)

		require.Error(t, err)
		assert.Empty(t, tm.Token)
	})

	t.Run("empty bearer token file", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "empty-token")
		
		err := os.WriteFile(tokenFile, []byte(""), 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(ctx)

		require.NoError(t, err)
		assert.Empty(t, tm.Token)
	})

	t.Run("whitespace-only token file", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "whitespace-token")
		
		err := os.WriteFile(tokenFile, []byte("   \n\t\n  "), 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(ctx)

		require.NoError(t, err)
		assert.Empty(t, tm.Token)
	})

	t.Run("very long token", func(t *testing.T) {
		longToken := ""
		for i := 0; i < 10000; i++ {
			longToken += "a"
		}

		config := &rest.Config{
			BearerToken: longToken,
		}

		tm := NewTokenManager(log, config)
		err := tm.ExtractAndSaveToken(ctx)

		require.NoError(t, err)
		assert.Equal(t, longToken, tm.Token)
		assert.Equal(t, 10000, len(tm.Token))
	})

	t.Run("update token on subsequent call", func(t *testing.T) {
		config := &rest.Config{
			BearerToken: "first-token",
		}

		tm := NewTokenManager(log, config)
		
		// First extraction
		err := tm.ExtractAndSaveToken(ctx)
		require.NoError(t, err)
		assert.Equal(t, "first-token", tm.Token)

		// Update config and extract again
		tm.RestConfig.BearerToken = "second-token"
		err = tm.ExtractAndSaveToken(ctx)
		require.NoError(t, err)
		assert.Equal(t, "second-token", tm.Token)
	})
}

// TestExtractTokenFromConfig tests the internal token extraction logic
func TestExtractTokenFromConfig(t *testing.T) {
	log := logr.Discard()

	t.Run("extract bearer token", func(t *testing.T) {
		expectedToken := "test-token-123"
		config := &rest.Config{
			BearerToken: expectedToken,
		}

		tm := NewTokenManager(log, config)
		token, err := tm.extractTokenFromConfig()

		require.NoError(t, err)
		assert.Equal(t, expectedToken, token)
	})

	t.Run("extract from file", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "token")
		expectedToken := "file-token-456"
		
		err := os.WriteFile(tokenFile, []byte(expectedToken), 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		token, err := tm.extractTokenFromConfig()

		require.NoError(t, err)
		assert.Equal(t, expectedToken, token)
	})

	t.Run("no authentication method", func(t *testing.T) {
		config := &rest.Config{}

		tm := NewTokenManager(log, config)
		token, err := tm.extractTokenFromConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no suitable authentication method found")
		assert.Empty(t, token)
	})

	t.Run("token file read error", func(t *testing.T) {
		config := &rest.Config{
			BearerTokenFile: "/invalid/path/token",
		}

		tm := NewTokenManager(log, config)
		token, err := tm.extractTokenFromConfig()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read token file")
		assert.Empty(t, token)
	})

	t.Run("token with special characters", func(t *testing.T) {
		specialToken := "token!@#$%^&*()_+-={}[]|:;<>?,./~`"
		config := &rest.Config{
			BearerToken: specialToken,
		}

		tm := NewTokenManager(log, config)
		token, err := tm.extractTokenFromConfig()

		require.NoError(t, err)
		assert.Equal(t, specialToken, token)
	})
}

// TestGetToken tests token retrieval functionality
func TestGetToken(t *testing.T) {
	log := logr.Discard()

	t.Run("get token after extraction", func(t *testing.T) {
		expectedToken := "extracted-token-123"
		config := &rest.Config{
			BearerToken: expectedToken,
		}

		tm := NewTokenManager(log, config)
		err := tm.ExtractAndSaveToken(context.Background())
		require.NoError(t, err)

		token, err := tm.GetToken()
		require.NoError(t, err)
		assert.Equal(t, expectedToken, token)
	})

	t.Run("get token without extraction", func(t *testing.T) {
		config := &rest.Config{}

		tm := NewTokenManager(log, config)
		token, err := tm.GetToken()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "token is empty")
		assert.Contains(t, err.Error(), "please extract token first")
		assert.Empty(t, token)
	})

	t.Run("get token multiple times", func(t *testing.T) {
		expectedToken := "persistent-token"
		config := &rest.Config{
			BearerToken: expectedToken,
		}

		tm := NewTokenManager(log, config)
		err := tm.ExtractAndSaveToken(context.Background())
		require.NoError(t, err)

		// Call GetToken multiple times
		for i := 0; i < 5; i++ {
			token, err := tm.GetToken()
			require.NoError(t, err)
			assert.Equal(t, expectedToken, token)
		}
	})

	t.Run("get token after manual set", func(t *testing.T) {
		config := &rest.Config{}
		tm := NewTokenManager(log, config)

		// Manually set token
		tm.Token = "manually-set-token"

		token, err := tm.GetToken()
		require.NoError(t, err)
		assert.Equal(t, "manually-set-token", token)
	})

	t.Run("get empty string token", func(t *testing.T) {
		config := &rest.Config{
			BearerToken: "",
		}

		tm := NewTokenManager(log, config)
		
		token, err := tm.GetToken()
		require.Error(t, err)
		assert.Empty(t, token)
	})
}

// TestTokenManagerEdgeCases tests edge cases and error scenarios
func TestTokenManagerEdgeCases(t *testing.T) {
	log := logr.Discard()

	t.Run("nil RestConfig", func(t *testing.T) {
		tm := NewTokenManager(log, nil)
		require.NotNil(t, tm)
		assert.Nil(t, tm.RestConfig)

		// Note: ExtractAndSaveToken will panic with nil RestConfig
		// This test verifies we can create TokenManager with nil config
		// but we should not call ExtractAndSaveToken without a valid config
	})

	t.Run("cancelled context", func(t *testing.T) {
		config := &rest.Config{
			BearerToken: "test-token",
		}

		tm := NewTokenManager(log, config)
		
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		// ExtractAndSaveToken doesn't explicitly check context, but shouldn't panic
		err := tm.ExtractAndSaveToken(ctx)
		require.NoError(t, err) // Current implementation doesn't use context
		assert.Equal(t, "test-token", tm.Token)
	})

	t.Run("token file with no read permissions", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("Skipping test when running as root")
		}

		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "no-read-token")
		
		err := os.WriteFile(tokenFile, []byte("secret-token"), 0000)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(context.Background())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read token file")
	})

	t.Run("concurrent token extraction", func(t *testing.T) {
		config := &rest.Config{
			BearerToken: "concurrent-token",
		}

		tm := NewTokenManager(log, config)

		// Extract token concurrently
		done := make(chan bool, 10)
		for i := 0; i < 10; i++ {
			go func() {
				err := tm.ExtractAndSaveToken(context.Background())
				assert.NoError(t, err)
				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			<-done
		}

		// Token should be set
		token, err := tm.GetToken()
		require.NoError(t, err)
		assert.Equal(t, "concurrent-token", token)
	})

	t.Run("concurrent get and extract", func(t *testing.T) {
		config := &rest.Config{
			BearerToken: "race-token",
		}

		tm := NewTokenManager(log, config)

		done := make(chan bool, 20)

		// Start extractors
		for i := 0; i < 10; i++ {
			go func() {
				tm.ExtractAndSaveToken(context.Background())
				done <- true
			}()
		}

		// Start getters
		for i := 0; i < 10; i++ {
			go func() {
				tm.GetToken()
				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 20; i++ {
			<-done
		}

		// Final token should be set
		token, err := tm.GetToken()
		require.NoError(t, err)
		assert.Equal(t, "race-token", token)
	})

	t.Run("large token file", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "large-token")
		
		// Create a very large token (1MB)
		largeToken := make([]byte, 1024*1024)
		for i := range largeToken {
			largeToken[i] = 'a'
		}
		
		err := os.WriteFile(tokenFile, largeToken, 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(context.Background())

		require.NoError(t, err)
		assert.Equal(t, 1024*1024, len(tm.Token))
	})

	t.Run("token file with binary content", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "binary-token")
		
		binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
		err := os.WriteFile(tokenFile, binaryContent, 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}

		tm := NewTokenManager(log, config)
		err = tm.ExtractAndSaveToken(context.Background())

		require.NoError(t, err)
		// Binary content should be read as-is
		assert.Equal(t, string(binaryContent), tm.Token)
	})
}

// TestTokenManagerLifecycle tests the complete lifecycle of TokenManager
func TestTokenManagerLifecycle(t *testing.T) {
	log := logr.Discard()

	t.Run("complete lifecycle", func(t *testing.T) {
		// 1. Create TokenManager
		config := &rest.Config{
			BearerToken: "lifecycle-token",
		}
		tm := NewTokenManager(log, config)
		require.NotNil(t, tm)

		// 2. Verify initial state
		assert.Empty(t, tm.Token)
		_, err := tm.GetToken()
		require.Error(t, err)

		// 3. Extract and save token
		err = tm.ExtractAndSaveToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "lifecycle-token", tm.Token)

		// 4. Retrieve token
		token, err := tm.GetToken()
		require.NoError(t, err)
		assert.Equal(t, "lifecycle-token", token)

		// 5. Update and re-extract
		tm.RestConfig.BearerToken = "updated-token"
		err = tm.ExtractAndSaveToken(context.Background())
		require.NoError(t, err)

		// 6. Verify updated token
		token, err = tm.GetToken()
		require.NoError(t, err)
		assert.Equal(t, "updated-token", token)
	})

	t.Run("lifecycle with file-based token", func(t *testing.T) {
		tmpDir := t.TempDir()
		tokenFile := filepath.Join(tmpDir, "lifecycle-token")

		// 1. Create initial token file
		err := os.WriteFile(tokenFile, []byte("initial-token"), 0600)
		require.NoError(t, err)

		config := &rest.Config{
			BearerTokenFile: tokenFile,
		}
		tm := NewTokenManager(log, config)

		// 2. Extract initial token
		err = tm.ExtractAndSaveToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "initial-token", tm.Token)

		// 3. Update token file
		err = os.WriteFile(tokenFile, []byte("updated-file-token"), 0600)
		require.NoError(t, err)

		// 4. Re-extract to get updated token
		err = tm.ExtractAndSaveToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "updated-file-token", tm.Token)

		// 5. Verify retrieval
		token, err := tm.GetToken()
		require.NoError(t, err)
		assert.Equal(t, "updated-file-token", token)
	})
}
