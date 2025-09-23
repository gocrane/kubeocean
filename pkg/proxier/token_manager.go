// Copyright 2024 The Kubeocean Authors
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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TokenManager manages the extraction and storage of authentication tokens
type TokenManager struct {
	Log           logr.Logger
	TokenFilePath string
	KubeClient    kubernetes.Interface
	RestConfig    *rest.Config
}

// NewTokenManager creates a new TokenManager
func NewTokenManager(log logr.Logger, tokenFilePath string, kubeClient kubernetes.Interface, restConfig *rest.Config) *TokenManager {
	return &TokenManager{
		Log:           log,
		TokenFilePath: tokenFilePath,
		KubeClient:    kubeClient,
		RestConfig:    restConfig,
	}
}

// ExtractAndSaveToken extracts the token from the current kubeconfig and saves it to file
func (tm *TokenManager) ExtractAndSaveToken(ctx context.Context) error {
	// Extract token from the current REST config
	token, err := tm.extractTokenFromConfig()
	if err != nil {
		return fmt.Errorf("failed to extract token from config: %w", err)
	}

	// Save token to file
	if err := tm.saveTokenToFile(token); err != nil {
		return fmt.Errorf("failed to save token to file: %w", err)
	}

	tm.Log.Info("Successfully extracted and saved token", "tokenFile", tm.TokenFilePath)
	return nil
}

// extractTokenFromConfig extracts the authentication token from the REST config
func (tm *TokenManager) extractTokenFromConfig() (string, error) {
	// Method 1: Direct bearer token in config
	if tm.RestConfig.BearerToken != "" {
		tm.Log.V(1).Info("Using bearer token from REST config")
		return tm.RestConfig.BearerToken, nil
	}

	// Method 2: Bearer token file
	if tm.RestConfig.BearerTokenFile != "" {
		tm.Log.V(1).Info("Reading bearer token from file", "tokenFile", tm.RestConfig.BearerTokenFile)
		tokenBytes, err := os.ReadFile(tm.RestConfig.BearerTokenFile)
		if err != nil {
			return "", fmt.Errorf("failed to read token file %s: %w", tm.RestConfig.BearerTokenFile, err)
		}
		token := string(tokenBytes)
		// Clean up the token (remove newlines, spaces)
		token = strings.TrimSpace(token)
		return token, nil
	}

	// Method 3: Service Account token (in-cluster config)
	// Check for the default service account token path
	defaultTokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	if _, err := os.Stat(defaultTokenPath); err == nil {
		tm.Log.V(1).Info("Reading service account token from default path", "tokenFile", defaultTokenPath)
		tokenBytes, err := os.ReadFile(defaultTokenPath)
		if err != nil {
			return "", fmt.Errorf("failed to read service account token: %w", err)
		}
		token := string(tokenBytes)
		token = strings.TrimSpace(token)
		return token, nil
	}

	// Method 4: Exec-based authentication
	if tm.RestConfig.ExecProvider != nil {
		tm.Log.V(1).Info("Using exec-based authentication")
		return tm.getTokenFromExecProvider()
	}

	// Method 5: Try to get token from a test API call
	tm.Log.V(1).Info("Attempting to get token via API call")
	return tm.getTokenFromAPI()
}

// getTokenFromExecProvider executes the exec provider to get a token
func (tm *TokenManager) getTokenFromExecProvider() (string, error) {
	execProvider := tm.RestConfig.ExecProvider
	
	// Create the command
	cmd := exec.Command(execProvider.Command, execProvider.Args...)
	cmd.Env = os.Environ()
	
	// Add any additional environment variables
	for _, env := range execProvider.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", env.Name, env.Value))
	}
	
	// Execute the command
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute exec provider: %w", err)
	}
	
	// Parse the output to extract the token
	// The output should be JSON with a "status" field containing the token
	// This is a simplified implementation
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", fmt.Errorf("exec provider returned empty token")
	}
	
	tm.Log.V(1).Info("Successfully obtained token from exec provider")
	return token, nil
}

// getTokenFromAPI makes an API call to get a valid token
func (tm *TokenManager) getTokenFromAPI() (string, error) {
	// This method is used as a fallback when other methods fail
	// We'll make a simple API call and let the client handle authentication
	// The actual token will be in the request headers
	
	// Create a test request
	req := tm.KubeClient.CoreV1().RESTClient().Get().
		Resource("nodes").
		SetHeader("Accept", "application/json").
		SetHeader("User-Agent", "kubeocean-proxier").
		Timeout(10 * time.Second)

	// Execute the request
	resp := req.Do(context.Background())
	if resp.Error() != nil {
		return "", fmt.Errorf("failed to make API call: %w", resp.Error())
	}

	// For this method, we can't easily extract the token from the response
	// This is a limitation of the current approach
	// In practice, you might need to implement a custom transport that captures the token
	return "", fmt.Errorf("API-based token extraction not implemented")
}

// saveTokenToFile saves the token to the specified file path
func (tm *TokenManager) saveTokenToFile(token string) error {
	// Ensure the directory exists
	dir := filepath.Dir(tm.TokenFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Write token to file atomically
	tempFile := tm.TokenFilePath + ".tmp"
	if err := os.WriteFile(tempFile, []byte(token), 0600); err != nil {
		return fmt.Errorf("failed to write token to temp file: %w", err)
	}

	// Rename temp file to final file
	if err := os.Rename(tempFile, tm.TokenFilePath); err != nil {
		// Clean up temp file on error
		os.Remove(tempFile)
		return fmt.Errorf("failed to rename temp file to final file: %w", err)
	}

	tm.Log.V(1).Info("Token saved to file", "tokenFile", tm.TokenFilePath, "tokenLength", len(token))
	return nil
}

// ValidateTokenFile checks if the token file exists and is readable
func (tm *TokenManager) ValidateTokenFile() error {
	if _, err := os.Stat(tm.TokenFilePath); os.IsNotExist(err) {
		return fmt.Errorf("token file does not exist: %s", tm.TokenFilePath)
	}

	// Try to read the file to ensure it's readable
	tokenBytes, err := os.ReadFile(tm.TokenFilePath)
	if err != nil {
		return fmt.Errorf("failed to read token file: %w", err)
	}

	if len(tokenBytes) == 0 {
		return fmt.Errorf("token file is empty: %s", tm.TokenFilePath)
	}

	tm.Log.V(1).Info("Token file validation successful", "tokenFile", tm.TokenFilePath, "tokenLength", len(tokenBytes))
	return nil
}

// RefreshTokenIfNeeded refreshes the token if it's expired or about to expire
func (tm *TokenManager) RefreshTokenIfNeeded(ctx context.Context) error {
	// Check if token file exists and is valid
	if err := tm.ValidateTokenFile(); err != nil {
		tm.Log.Info("Token file validation failed, refreshing token", "error", err)
		return tm.ExtractAndSaveToken(ctx)
	}

	// For now, we'll always refresh the token to ensure it's up to date
	// In a production environment, you might want to check token expiration
	tm.Log.V(1).Info("Refreshing token")
	return tm.ExtractAndSaveToken(ctx)
}

// StartTokenRefreshRoutine starts a background routine to periodically refresh the token
func (tm *TokenManager) StartTokenRefreshRoutine(ctx context.Context, refreshInterval time.Duration) {
	tm.Log.Info("Starting token refresh routine", "interval", refreshInterval)
	
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		
		for {
			select {
			case <-ctx.Done():
				tm.Log.Info("Token refresh routine stopped")
				return
			case <-ticker.C:
				if err := tm.RefreshTokenIfNeeded(ctx); err != nil {
					tm.Log.Error(err, "Failed to refresh token")
				} else {
					tm.Log.V(1).Info("Token refreshed successfully")
				}
			}
		}
	}()
}

// GetTokenFromFile reads the current token from the file
func (tm *TokenManager) GetTokenFromFile() (string, error) {
	tokenBytes, err := os.ReadFile(tm.TokenFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read token file: %w", err)
	}
	
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}
	
	return token, nil
}

// IsTokenValid checks if the current token is valid by making a test API call
func (tm *TokenManager) IsTokenValid(ctx context.Context) bool {
	token, err := tm.GetTokenFromFile()
	if err != nil {
		tm.Log.V(1).Info("Failed to get token from file", "error", err)
		return false
	}
	
	// Create a test request with the token
	req := tm.KubeClient.CoreV1().RESTClient().Get().
		Resource("nodes").
		SetHeader("Accept", "application/json").
		SetHeader("User-Agent", "kubeocean-proxier").
		SetHeader("Authorization", "Bearer "+token).
		Timeout(5 * time.Second)

	// Execute the request
	resp := req.Do(ctx)
	if resp.Error() != nil {
		tm.Log.V(1).Info("Token validation failed", "error", resp.Error())
		return false
	}
	
	tm.Log.V(1).Info("Token validation successful")
	return true
}
