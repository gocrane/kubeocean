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
	"io"
	"time"
)

// ContainerLogOpts represents the options for container log retrieval
type ContainerLogOpts struct {
	Tail         int
	LimitBytes   int
	Timestamps   bool
	Follow       bool
	Previous     bool
	SinceSeconds int
	SinceTime    time.Time
}

// ContainerExecOpts represents the options for container exec
type ContainerExecOpts struct {
	Command []string
	Stdin   bool
	Stdout  bool
	Stderr  bool
	TTY     bool
}

// TermSize represents terminal size for resize operations
type TermSize struct {
	Width  uint16
	Height uint16
}

// AttachIO defines the interface for attach input/output
type AttachIO interface {
	Stdin() io.Reader
	Stdout() io.WriteCloser
	Stderr() io.WriteCloser
	TTY() bool
	// HasStdin returns true if stdin is requested
	HasStdin() bool
	// HasStdout returns true if stdout is requested
	HasStdout() bool
	// HasStderr returns true if stderr is requested
	HasStderr() bool
	// Resize returns a channel that receives terminal size changes (optional)
	Resize() <-chan TermSize
}

// KubeletProxy defines the interface for Kubelet API proxy functionality
type KubeletProxy interface {
	// GetContainerLogs retrieves container logs from physical cluster
	GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts ContainerLogOpts) (io.ReadCloser, error)

	// RunInContainer executes a command in a container
	RunInContainer(ctx context.Context, namespace, podName, containerName string, cmd []string, attach AttachIO) error

	// Start starts the Kubelet proxy
	Start(ctx context.Context) error

	// Stop stops the Kubelet proxy
	Stop() error

	// IsRunning returns true if the Kubelet proxy is running
	IsRunning() bool
}

// HTTPServer defines the interface for HTTP server functionality
type HTTPServer interface {
	// Start starts the HTTP server
	Start(ctx context.Context) error

	// Stop stops the HTTP server
	Stop() error

	// IsRunning returns true if the HTTP server is running
	IsRunning() bool
}

// Config holds configuration for logs proxy
type Config struct {
	// Enabled indicates whether logs proxy is enabled
	Enabled bool `json:"enabled"`

	// ListenAddr is the address to listen on for logs API
	ListenAddr string `json:"listenAddr"`

	// TLSConfig holds TLS configuration
	TLSConfig *TLSConfig `json:"tlsConfig,omitempty"`

	// SecretName is the name of the Kubernetes secret containing TLS certificates
	SecretName string `json:"secretName,omitempty"`

	// SecretNamespace is the namespace of the Kubernetes secret
	SecretNamespace string `json:"secretNamespace,omitempty"`

	// AllowUnauthenticatedClients allows clients without certificates
	AllowUnauthenticatedClients bool `json:"allowUnauthenticatedClients,omitempty"`

	// StreamIdleTimeout is the idle timeout for log streams
	StreamIdleTimeout time.Duration `json:"streamIdleTimeout,omitempty"`

	// StreamCreationTimeout is the timeout for creating log streams
	StreamCreationTimeout time.Duration `json:"streamCreationTimeout,omitempty"`
}

// TLSConfig holds TLS configuration
type TLSConfig struct {
	// CertPath is the path to the TLS certificate
	CertPath string `json:"certPath"`

	// KeyPath is the path to the TLS private key
	KeyPath string `json:"keyPath"`

	// CAPath is the path to the CA certificate (optional)
	CAPath string `json:"caPath,omitempty"`
}

// PodMappingInfo holds information about pod mapping between virtual and physical clusters
type PodMappingInfo struct {
	// VirtualNamespace is the virtual pod namespace
	VirtualNamespace string

	// VirtualName is the virtual pod name
	VirtualName string

	// PhysicalNamespace is the physical pod namespace
	PhysicalNamespace string

	// PhysicalName is the physical pod name
	PhysicalName string

	// ClusterBindingName is the name of the cluster binding
	ClusterBindingName string
}
