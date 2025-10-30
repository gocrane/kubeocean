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
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
)

// TokenManager manages the extraction and storage of authentication tokens
type TokenManager struct {
	Log        logr.Logger
	Token      string
	RestConfig *rest.Config
}

// NewTokenManager creates a new TokenManager
func NewTokenManager(log logr.Logger, restConfig *rest.Config) *TokenManager {
	return &TokenManager{
		Log:        log,
		RestConfig: restConfig,
	}
}

// ExtractAndSaveToken extracts the token from the current kubeconfig and saves it to memory
func (tm *TokenManager) ExtractAndSaveToken(ctx context.Context) error {
	// Extract token from the current REST config
	token, err := tm.extractTokenFromConfig()
	if err != nil {
		return fmt.Errorf("failed to extract token from config: %w", err)
	}

	// Save token to memory
	tm.Token = token

	tm.Log.Info("Successfully extracted and saved token", "tokenLength", len(token))
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

	// No more authentication methods available
	return "", fmt.Errorf("no suitable authentication method found in REST config")
}

// GetToken returns the current token from memory
func (tm *TokenManager) GetToken() (string, error) {
	if tm.Token == "" {
		return "", fmt.Errorf("token is empty, please extract token first")
	}

	return tm.Token, nil
}
