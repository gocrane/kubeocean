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
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

// Manager manages the Kubelet proxy and HTTP server
type Manager struct {
	config       *Config
	kubeletProxy KubeletProxy
	httpServer   HTTPServer
	log          logr.Logger
	running      bool
}

// NewManager creates a new Kubelet proxy manager
func NewManager(
	config *Config,
	virtualClient client.Client,
	physicalClient kubernetes.Interface,
	physicalConfig *rest.Config,
	virtualK8sClient kubernetes.Interface,
	clusterBinding *cloudv1beta1.ClusterBinding,
	log logr.Logger,
) *Manager {
	kubeletProxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)
	// Use virtual kubernetes client for TLS secret access since secrets are in virtual cluster
	httpServer := NewHTTPServer(config, kubeletProxy, virtualK8sClient, log)

	return &Manager{
		config:       config,
		kubeletProxy: kubeletProxy,
		httpServer:   httpServer,
		log:          log.WithName("kubelet-proxy-manager"),
		running:      false,
	}
}

// Start starts the logs manager
func (m *Manager) Start(ctx context.Context) error {
	if !m.config.Enabled {
		m.log.Info("Logs proxy is disabled, skipping startup")
		return nil
	}

	m.log.Info("Starting logs manager")

	// Start Kubelet proxy
	if err := m.kubeletProxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start Kubelet proxy: %w", err)
	}

	// Start HTTP server
	if err := m.httpServer.Start(ctx); err != nil {
		// Stop Kubelet proxy if HTTP server fails to start
		if stopErr := m.kubeletProxy.Stop(); stopErr != nil {
			m.log.Error(stopErr, "Failed to stop Kubelet proxy after HTTP server startup failure")
		}
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	m.running = true
	m.log.Info("Logs manager started successfully")
	return nil
}

// Stop stops the logs manager
func (m *Manager) Stop() error {
	if !m.running {
		return nil
	}

	m.log.Info("Stopping logs manager")

	var errs []error

	// Stop HTTP server
	if err := m.httpServer.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("failed to stop HTTP server: %w", err))
	}

	// Stop Kubelet proxy
	if err := m.kubeletProxy.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("failed to stop Kubelet proxy: %w", err))
	}

	m.running = false

	if len(errs) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errs)
	}

	m.log.Info("Logs manager stopped successfully")
	return nil
}

// IsRunning returns true if the logs manager is running
func (m *Manager) IsRunning() bool {
	return m.running
}

// GetConfig returns the current configuration
func (m *Manager) GetConfig() *Config {
	return m.config
}

// UpdateConfig updates the configuration (requires restart to take effect)
func (m *Manager) UpdateConfig(config *Config) error {
	if m.running {
		return fmt.Errorf("cannot update config while running, stop first")
	}
	m.config = config
	return nil
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:                     true,
		ListenAddr:                  ":10250",
		TLSConfig:                   nil, // No TLS by default
		AllowUnauthenticatedClients: true,
		StreamIdleTimeout:           30 * time.Second,
		StreamCreationTimeout:       10 * time.Second,
	}
}

// ConfigFromClusterBinding creates configuration from cluster binding
//
// Logs proxy behavior:
// - By default, logs proxy is ALWAYS ENABLED regardless of ClusterBinding configuration
// - Only disabled when explicitly set to "false" via kubeocean.io/logs-proxy-enabled annotation
// - This ensures logs functionality is available by default for all cluster bindings
func ConfigFromClusterBinding(clusterBinding *cloudv1beta1.ClusterBinding) *Config {
	config := DefaultConfig()

	// Logs proxy is always enabled by default, regardless of ClusterBinding configuration
	// Only disable if explicitly set to false in annotations
	if enabled, exists := clusterBinding.Annotations["kubeocean.io/logs-proxy-enabled"]; exists {
		config.Enabled = enabled == "true"
	}
	// If annotation doesn't exist, keep the default enabled state (true)

	// Check if we should use Kubernetes Secret for TLS
	if certSource, exists := clusterBinding.Annotations["kubeocean.io/logs-proxy-cert-source"]; exists && certSource == "kubernetes" {
		// Get secret name and namespace from annotations
		secretName := "logs-proxy-tls" // default
		if name, exists := clusterBinding.Annotations["kubeocean.io/logs-proxy-secret-name"]; exists {
			secretName = name
		}

		secretNamespace := "kubeocean-system" // default
		if ns, exists := clusterBinding.Annotations["kubeocean.io/logs-proxy-secret-namespace"]; exists {
			secretNamespace = ns
		}

		// Set up TLS config for Kubernetes Secret
		config.TLSConfig = &TLSConfig{
			CertPath: "", // Will be loaded from secret
			KeyPath:  "", // Will be loaded from secret
			CAPath:   "", // Will be loaded from secret
		}

		// Store secret info in a custom field (we'll add this to TLSConfig)
		config.SecretName = secretName
		config.SecretNamespace = secretNamespace
	}

	// Check if unauthenticated clients are allowed
	if allowUnauth, exists := clusterBinding.Annotations["kubeocean.io/logs-proxy-allow-unauthenticated"]; exists {
		config.AllowUnauthenticatedClients = allowUnauth == "true"
	}

	return config
}
