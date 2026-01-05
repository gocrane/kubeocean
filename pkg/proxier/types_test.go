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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerLogOpts tests ContainerLogOpts struct initialization and field access
func TestContainerLogOpts(t *testing.T) {
	tests := []struct {
		name     string
		opts     ContainerLogOpts
		validate func(t *testing.T, opts ContainerLogOpts)
	}{
		{
			name: "default values",
			opts: ContainerLogOpts{},
			validate: func(t *testing.T, opts ContainerLogOpts) {
				assert.Equal(t, 0, opts.Tail)
				assert.Equal(t, 0, opts.LimitBytes)
				assert.False(t, opts.Timestamps)
				assert.False(t, opts.Follow)
				assert.False(t, opts.Previous)
				assert.Equal(t, 0, opts.SinceSeconds)
				assert.True(t, opts.SinceTime.IsZero())
			},
		},
		{
			name: "all fields set",
			opts: ContainerLogOpts{
				Tail:         100,
				LimitBytes:   1024,
				Timestamps:   true,
				Follow:       true,
				Previous:     true,
				SinceSeconds: 300,
				SinceTime:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			},
			validate: func(t *testing.T, opts ContainerLogOpts) {
				assert.Equal(t, 100, opts.Tail)
				assert.Equal(t, 1024, opts.LimitBytes)
				assert.True(t, opts.Timestamps)
				assert.True(t, opts.Follow)
				assert.True(t, opts.Previous)
				assert.Equal(t, 300, opts.SinceSeconds)
				assert.Equal(t, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), opts.SinceTime)
			},
		},
		{
			name: "negative tail value",
			opts: ContainerLogOpts{
				Tail: -1,
			},
			validate: func(t *testing.T, opts ContainerLogOpts) {
				assert.Equal(t, -1, opts.Tail)
			},
		},
		{
			name: "large limit bytes",
			opts: ContainerLogOpts{
				LimitBytes: 1073741824, // 1GB
			},
			validate: func(t *testing.T, opts ContainerLogOpts) {
				assert.Equal(t, 1073741824, opts.LimitBytes)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.opts)
		})
	}
}

// TestContainerExecOpts tests ContainerExecOpts struct initialization and field access
func TestContainerExecOpts(t *testing.T) {
	tests := []struct {
		name     string
		opts     ContainerExecOpts
		validate func(t *testing.T, opts ContainerExecOpts)
	}{
		{
			name: "default values",
			opts: ContainerExecOpts{},
			validate: func(t *testing.T, opts ContainerExecOpts) {
				assert.Nil(t, opts.Command)
				assert.False(t, opts.Stdin)
				assert.False(t, opts.Stdout)
				assert.False(t, opts.Stderr)
				assert.False(t, opts.TTY)
			},
		},
		{
			name: "simple command",
			opts: ContainerExecOpts{
				Command: []string{"/bin/sh", "-c", "echo hello"},
				Stdout:  true,
			},
			validate: func(t *testing.T, opts ContainerExecOpts) {
				require.NotNil(t, opts.Command)
				assert.Equal(t, []string{"/bin/sh", "-c", "echo hello"}, opts.Command)
				assert.True(t, opts.Stdout)
				assert.False(t, opts.Stdin)
			},
		},
		{
			name: "interactive command with TTY",
			opts: ContainerExecOpts{
				Command: []string{"/bin/bash"},
				Stdin:   true,
				Stdout:  true,
				Stderr:  true,
				TTY:     true,
			},
			validate: func(t *testing.T, opts ContainerExecOpts) {
				assert.Equal(t, []string{"/bin/bash"}, opts.Command)
				assert.True(t, opts.Stdin)
				assert.True(t, opts.Stdout)
				assert.True(t, opts.Stderr)
				assert.True(t, opts.TTY)
			},
		},
		{
			name: "empty command array",
			opts: ContainerExecOpts{
				Command: []string{},
			},
			validate: func(t *testing.T, opts ContainerExecOpts) {
				require.NotNil(t, opts.Command)
				assert.Empty(t, opts.Command)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.opts)
		})
	}
}

// TestTermSize tests TermSize struct initialization and field access
func TestTermSize(t *testing.T) {
	tests := []struct {
		name     string
		termSize TermSize
		validate func(t *testing.T, ts TermSize)
	}{
		{
			name:     "default values",
			termSize: TermSize{},
			validate: func(t *testing.T, ts TermSize) {
				assert.Equal(t, uint16(0), ts.Width)
				assert.Equal(t, uint16(0), ts.Height)
			},
		},
		{
			name: "standard terminal size",
			termSize: TermSize{
				Width:  80,
				Height: 24,
			},
			validate: func(t *testing.T, ts TermSize) {
				assert.Equal(t, uint16(80), ts.Width)
				assert.Equal(t, uint16(24), ts.Height)
			},
		},
		{
			name: "large terminal size",
			termSize: TermSize{
				Width:  320,
				Height: 100,
			},
			validate: func(t *testing.T, ts TermSize) {
				assert.Equal(t, uint16(320), ts.Width)
				assert.Equal(t, uint16(100), ts.Height)
			},
		},
		{
			name: "maximum uint16 values",
			termSize: TermSize{
				Width:  65535,
				Height: 65535,
			},
			validate: func(t *testing.T, ts TermSize) {
				assert.Equal(t, uint16(65535), ts.Width)
				assert.Equal(t, uint16(65535), ts.Height)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.termSize)
		})
	}
}

// TestConfig tests Config struct initialization and field access
func TestConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		validate func(t *testing.T, cfg Config)
	}{
		{
			name:   "default values",
			config: Config{},
			validate: func(t *testing.T, cfg Config) {
				assert.False(t, cfg.Enabled)
				assert.Empty(t, cfg.ListenAddr)
				assert.Nil(t, cfg.TLSConfig)
				assert.Empty(t, cfg.SecretName)
				assert.Empty(t, cfg.SecretNamespace)
				assert.False(t, cfg.AllowUnauthenticatedClients)
				assert.Equal(t, time.Duration(0), cfg.StreamIdleTimeout)
				assert.Equal(t, time.Duration(0), cfg.StreamCreationTimeout)
			},
		},
		{
			name: "fully configured",
			config: Config{
				Enabled:    true,
				ListenAddr: ":10250",
				TLSConfig: &TLSConfig{
					CertPath: "/etc/certs/tls.crt",
					KeyPath:  "/etc/certs/tls.key",
					CAPath:   "/etc/certs/ca.crt",
				},
				SecretName:                  "proxier-certs",
				SecretNamespace:             "kubeocean-system",
				AllowUnauthenticatedClients: true,
				StreamIdleTimeout:           5 * time.Minute,
				StreamCreationTimeout:       30 * time.Second,
			},
			validate: func(t *testing.T, cfg Config) {
				assert.True(t, cfg.Enabled)
				assert.Equal(t, ":10250", cfg.ListenAddr)
				require.NotNil(t, cfg.TLSConfig)
				assert.Equal(t, "/etc/certs/tls.crt", cfg.TLSConfig.CertPath)
				assert.Equal(t, "/etc/certs/tls.key", cfg.TLSConfig.KeyPath)
				assert.Equal(t, "/etc/certs/ca.crt", cfg.TLSConfig.CAPath)
				assert.Equal(t, "proxier-certs", cfg.SecretName)
				assert.Equal(t, "kubeocean-system", cfg.SecretNamespace)
				assert.True(t, cfg.AllowUnauthenticatedClients)
				assert.Equal(t, 5*time.Minute, cfg.StreamIdleTimeout)
				assert.Equal(t, 30*time.Second, cfg.StreamCreationTimeout)
			},
		},
		{
			name: "without TLS",
			config: Config{
				Enabled:                     true,
				ListenAddr:                  "0.0.0.0:8080",
				AllowUnauthenticatedClients: true,
			},
			validate: func(t *testing.T, cfg Config) {
				assert.True(t, cfg.Enabled)
				assert.Equal(t, "0.0.0.0:8080", cfg.ListenAddr)
				assert.Nil(t, cfg.TLSConfig)
				assert.True(t, cfg.AllowUnauthenticatedClients)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.config)
		})
	}
}

// TestTLSConfig tests TLSConfig struct initialization and field access
func TestTLSConfig(t *testing.T) {
	tests := []struct {
		name      string
		tlsConfig TLSConfig
		validate  func(t *testing.T, cfg TLSConfig)
	}{
		{
			name:      "default values",
			tlsConfig: TLSConfig{},
			validate: func(t *testing.T, cfg TLSConfig) {
				assert.Empty(t, cfg.CertPath)
				assert.Empty(t, cfg.KeyPath)
				assert.Empty(t, cfg.CAPath)
			},
		},
		{
			name: "with all paths",
			tlsConfig: TLSConfig{
				CertPath: "/var/run/secrets/tls.crt",
				KeyPath:  "/var/run/secrets/tls.key",
				CAPath:   "/var/run/secrets/ca.crt",
			},
			validate: func(t *testing.T, cfg TLSConfig) {
				assert.Equal(t, "/var/run/secrets/tls.crt", cfg.CertPath)
				assert.Equal(t, "/var/run/secrets/tls.key", cfg.KeyPath)
				assert.Equal(t, "/var/run/secrets/ca.crt", cfg.CAPath)
			},
		},
		{
			name: "without CA path",
			tlsConfig: TLSConfig{
				CertPath: "/etc/ssl/server.crt",
				KeyPath:  "/etc/ssl/server.key",
			},
			validate: func(t *testing.T, cfg TLSConfig) {
				assert.Equal(t, "/etc/ssl/server.crt", cfg.CertPath)
				assert.Equal(t, "/etc/ssl/server.key", cfg.KeyPath)
				assert.Empty(t, cfg.CAPath)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.tlsConfig)
		})
	}
}

// TestPodMappingInfo tests PodMappingInfo struct initialization and field access
func TestPodMappingInfo(t *testing.T) {
	tests := []struct {
		name     string
		info     PodMappingInfo
		validate func(t *testing.T, info PodMappingInfo)
	}{
		{
			name: "default values",
			info: PodMappingInfo{},
			validate: func(t *testing.T, info PodMappingInfo) {
				assert.Empty(t, info.VirtualNamespace)
				assert.Empty(t, info.VirtualName)
				assert.Empty(t, info.PhysicalNamespace)
				assert.Empty(t, info.PhysicalName)
				assert.Empty(t, info.ClusterBindingName)
			},
		},
		{
			name: "fully populated",
			info: PodMappingInfo{
				VirtualNamespace:   "default",
				VirtualName:        "nginx-pod",
				PhysicalNamespace:  "kubeocean-worker",
				PhysicalName:       "nginx-pod-abc123",
				ClusterBindingName: "worker-cluster-1",
			},
			validate: func(t *testing.T, info PodMappingInfo) {
				assert.Equal(t, "default", info.VirtualNamespace)
				assert.Equal(t, "nginx-pod", info.VirtualName)
				assert.Equal(t, "kubeocean-worker", info.PhysicalNamespace)
				assert.Equal(t, "nginx-pod-abc123", info.PhysicalName)
				assert.Equal(t, "worker-cluster-1", info.ClusterBindingName)
			},
		},
		{
			name: "with special characters",
			info: PodMappingInfo{
				VirtualNamespace:   "test-ns-123",
				VirtualName:        "my-app-v1.0",
				PhysicalNamespace:  "worker-ns-456",
				PhysicalName:       "my-app-v1.0-xyz789",
				ClusterBindingName: "cluster-binding-test",
			},
			validate: func(t *testing.T, info PodMappingInfo) {
				assert.Equal(t, "test-ns-123", info.VirtualNamespace)
				assert.Equal(t, "my-app-v1.0", info.VirtualName)
				assert.Equal(t, "worker-ns-456", info.PhysicalNamespace)
				assert.Equal(t, "my-app-v1.0-xyz789", info.PhysicalName)
				assert.Equal(t, "cluster-binding-test", info.ClusterBindingName)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.validate(t, tt.info)
		})
	}
}

// mockAttachIO is a mock implementation of AttachIO interface for testing
type mockAttachIO struct {
	stdin  io.Reader
	stdout io.WriteCloser
	stderr io.WriteCloser
	tty    bool
	resize chan TermSize
}

func (m *mockAttachIO) Stdin() io.Reader           { return m.stdin }
func (m *mockAttachIO) Stdout() io.WriteCloser     { return m.stdout }
func (m *mockAttachIO) Stderr() io.WriteCloser     { return m.stderr }
func (m *mockAttachIO) TTY() bool                  { return m.tty }
func (m *mockAttachIO) HasStdin() bool             { return m.stdin != nil }
func (m *mockAttachIO) HasStdout() bool            { return m.stdout != nil }
func (m *mockAttachIO) HasStderr() bool            { return m.stderr != nil }
func (m *mockAttachIO) Resize() <-chan TermSize    { return m.resize }

// TestAttachIOInterface tests the AttachIO interface implementation
func TestAttachIOInterface(t *testing.T) {
	resizeCh := make(chan TermSize, 1)
	
	mock := &mockAttachIO{
		stdin:  nil,
		stdout: nil,
		stderr: nil,
		tty:    false,
		resize: resizeCh,
	}

	// Test interface implementation
	var _ AttachIO = mock

	t.Run("default state", func(t *testing.T) {
		assert.Nil(t, mock.Stdin())
		assert.Nil(t, mock.Stdout())
		assert.Nil(t, mock.Stderr())
		assert.False(t, mock.TTY())
		assert.False(t, mock.HasStdin())
		assert.False(t, mock.HasStdout())
		assert.False(t, mock.HasStderr())
		assert.NotNil(t, mock.Resize())
	})

	t.Run("with TTY enabled", func(t *testing.T) {
		mock.tty = true
		assert.True(t, mock.TTY())
	})

	t.Run("resize channel", func(t *testing.T) {
		ts := TermSize{Width: 100, Height: 50}
		resizeCh <- ts
		
		select {
		case receivedTS := <-mock.Resize():
			assert.Equal(t, uint16(100), receivedTS.Width)
			assert.Equal(t, uint16(50), receivedTS.Height)
		default:
			t.Fatal("expected to receive TermSize from channel")
		}
	})
}

// mockKubeletProxy is a mock implementation of KubeletProxy interface for testing
type mockKubeletProxy struct {
	running bool
}

func (m *mockKubeletProxy) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts ContainerLogOpts) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockKubeletProxy) RunInContainer(ctx context.Context, namespace, podName, containerName string, cmd []string, attach AttachIO) error {
	return nil
}

func (m *mockKubeletProxy) Start(ctx context.Context) error {
	m.running = true
	return nil
}

func (m *mockKubeletProxy) Stop() error {
	m.running = false
	return nil
}

func (m *mockKubeletProxy) IsRunning() bool {
	return m.running
}

// TestKubeletProxyInterface tests the KubeletProxy interface implementation
func TestKubeletProxyInterface(t *testing.T) {
	mock := &mockKubeletProxy{}

	// Test interface implementation
	var _ KubeletProxy = mock

	t.Run("initial state", func(t *testing.T) {
		assert.False(t, mock.IsRunning())
	})

	t.Run("start and stop", func(t *testing.T) {
		ctx := context.Background()
		
		err := mock.Start(ctx)
		require.NoError(t, err)
		assert.True(t, mock.IsRunning())
		
		err = mock.Stop()
		require.NoError(t, err)
		assert.False(t, mock.IsRunning())
	})

	t.Run("GetContainerLogs", func(t *testing.T) {
		ctx := context.Background()
		opts := ContainerLogOpts{Tail: 100}
		
		reader, err := mock.GetContainerLogs(ctx, "default", "test-pod", "container1", opts)
		assert.NoError(t, err)
		assert.Nil(t, reader)
	})

	t.Run("RunInContainer", func(t *testing.T) {
		ctx := context.Background()
		cmd := []string{"/bin/sh", "-c", "echo test"}
		attachIO := &mockAttachIO{}
		
		err := mock.RunInContainer(ctx, "default", "test-pod", "container1", cmd, attachIO)
		assert.NoError(t, err)
	})
}

// mockHTTPServer is a mock implementation of HTTPServer interface for testing
type mockHTTPServer struct {
	running bool
}

func (m *mockHTTPServer) Start(ctx context.Context) error {
	m.running = true
	return nil
}

func (m *mockHTTPServer) Stop() error {
	m.running = false
	return nil
}

func (m *mockHTTPServer) IsRunning() bool {
	return m.running
}

// TestHTTPServerInterface tests the HTTPServer interface implementation
func TestHTTPServerInterface(t *testing.T) {
	mock := &mockHTTPServer{}

	// Test interface implementation
	var _ HTTPServer = mock

	t.Run("initial state", func(t *testing.T) {
		assert.False(t, mock.IsRunning())
	})

	t.Run("start and stop", func(t *testing.T) {
		ctx := context.Background()
		
		err := mock.Start(ctx)
		require.NoError(t, err)
		assert.True(t, mock.IsRunning())
		
		err = mock.Stop()
		require.NoError(t, err)
		assert.False(t, mock.IsRunning())
	})

	t.Run("multiple start/stop cycles", func(t *testing.T) {
		ctx := context.Background()
		
		for i := 0; i < 3; i++ {
			err := mock.Start(ctx)
			require.NoError(t, err)
			assert.True(t, mock.IsRunning())
			
			err = mock.Stop()
			require.NoError(t, err)
			assert.False(t, mock.IsRunning())
		}
	})
}
