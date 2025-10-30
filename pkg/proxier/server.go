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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientremotecommand "k8s.io/client-go/tools/remotecommand"

	localremotecommand "github.com/gocrane/kubeocean/pkg/proxier/remotecommand"
)

const (
	// Boolean string values for query parameters
	trueStr = "true"
	oneStr  = "1"
)

// server implements HTTPServer interface
type server struct {
	config       *Config
	kubeletProxy KubeletProxy
	log          logr.Logger
	httpServer   *http.Server
	listener     net.Listener
	running      bool
	client       kubernetes.Interface
}

// NewHTTPServer creates a new HTTP server for logs proxy
func NewHTTPServer(config *Config, kubeletProxy KubeletProxy, client kubernetes.Interface, log logr.Logger) HTTPServer {
	return &server{
		config:       config,
		kubeletProxy: kubeletProxy,
		log:          log.WithName("kubelet-http-server"),
		running:      false,
		client:       client,
	}
}

// Start starts the HTTP server
func (s *server) Start(ctx context.Context) error {
	s.log.Info("Starting logs HTTP server", "addr", s.config.ListenAddr)

	// Setup routes
	mux := s.setupRoutes()

	// Create HTTP server
	s.httpServer = &http.Server{
		Handler: mux,
	}

	// Setup listener based on TLS configuration
	var err error
	if s.config.TLSConfig != nil && s.config.SecretName != "" {
		// HTTPS server with Kubernetes Secret
		tlsConfig, err := s.loadTLSConfigFromSecret()
		if err != nil {
			s.log.Error(err, "Failed to load TLS config from secret, falling back to HTTP mode")
			// Fall back to HTTP server if TLS config fails
			s.listener, err = net.Listen("tcp", s.config.ListenAddr)
			if err != nil {
				return fmt.Errorf("failed to create HTTP listener after TLS fallback: %w", err)
			}
			s.log.Info("Started logs HTTP server (TLS fallback)", "addr", s.config.ListenAddr)
		} else {
			// TLS config loaded successfully, use HTTPS
			s.httpServer.TLSConfig = tlsConfig
			s.listener, err = tls.Listen("tcp", s.config.ListenAddr, tlsConfig)
			if err != nil {
				return fmt.Errorf("failed to create TLS listener: %w", err)
			}
			s.log.Info("Started logs HTTPS server", "addr", s.config.ListenAddr)
		}
	} else {
		// HTTP server
		s.listener, err = net.Listen("tcp", s.config.ListenAddr)
		if err != nil {
			return fmt.Errorf("failed to create listener: %w", err)
		}

		s.log.Info("Started logs HTTP server (no TLS)", "addr", s.config.ListenAddr)
	}

	s.running = true

	// Start server in goroutine
	go func() {
		var err error
		if s.httpServer.TLSConfig != nil {
			err = s.httpServer.Serve(s.listener)
		} else {
			err = s.httpServer.Serve(s.listener)
		}

		if err != nil && err != http.ErrServerClosed {
			s.log.Error(err, "HTTP server error")
		}
	}()

	return nil
}

// Stop stops the HTTP server
func (s *server) Stop() error {
	if !s.running {
		return nil
	}

	s.log.Info("Stopping logs HTTP server")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := s.httpServer.Shutdown(ctx)
	if err != nil {
		s.log.Error(err, "Failed to shutdown HTTP server gracefully")
		return err
	}

	if s.listener != nil {
		s.listener.Close()
	}

	s.running = false
	s.log.Info("Logs HTTP server stopped")
	return nil
}

// IsRunning returns true if the HTTP server is running
func (s *server) IsRunning() bool {
	return s.running
}

// GetRouter returns the HTTP router for testing purposes
func (s *server) GetRouter() *mux.Router {
	return s.setupRoutes()
}

// setupRoutes sets up HTTP routes for logs API
func (s *server) setupRoutes() *mux.Router {
	router := mux.NewRouter()
	router.StrictSlash(true)

	// Container logs endpoint
	router.HandleFunc("/containerLogs/{namespace}/{pod}/{container}", s.handleContainerLogs).Methods("GET")

	// Container exec endpoint
	router.HandleFunc("/exec/{namespace}/{pod}/{container}", s.handleContainerExec).Methods("POST", "GET")

	// Health check endpoint
	router.HandleFunc("/healthz", s.handleHealthz).Methods("GET")

	// Version endpoint
	router.HandleFunc("/version", s.handleVersion).Methods("GET")

	// 404 handler
	router.NotFoundHandler = http.HandlerFunc(s.handleNotFound)

	return router
}

// handleContainerLogs handles container logs requests
func (s *server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	pod := vars["pod"]
	container := vars["container"]

	s.log.Info("Processing container logs request",
		"namespace", namespace,
		"pod", pod,
		"container", container,
		"remoteAddr", r.RemoteAddr,
		"userAgent", r.UserAgent(),
	)

	// Parse log options from query parameters
	opts, err := s.parseLogOptions(r.URL.Query())
	if err != nil {
		s.log.Error(err, "Failed to parse log options", "namespace", namespace, "pod", pod, "container", container)
		http.Error(w, fmt.Sprintf("Invalid log options: %v", err), http.StatusBadRequest)
		return
	}

	s.log.Info("Parsed log options successfully", "namespace", namespace, "pod", pod, "container", container)

	// Get logs from proxy
	logs, err := s.kubeletProxy.GetContainerLogs(r.Context(), namespace, pod, container, opts)
	if err != nil {
		s.log.Error(err, "Failed to get container logs", "namespace", namespace, "pod", pod, "container", container)
		http.Error(w, fmt.Sprintf("Failed to get logs: %v", err), http.StatusInternalServerError)
		return
	}
	defer logs.Close()

	// Set response headers
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Content-Type", "text/plain")

	// Stream logs to client
	_, err = io.Copy(w, logs)
	if err != nil {
		s.log.Error(err, "Failed to stream logs to client", "namespace", namespace, "pod", pod, "container", container)
	}
}

// handleContainerExec handles container exec requests using SPDY protocol
func (s *server) handleContainerExec(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	pod := vars["pod"]
	container := vars["container"]

	// Log the request details for debugging
	s.log.Info("Handling container exec request",
		"namespace", namespace,
		"pod", pod,
		"container", container,
		"method", r.Method,
		"url", r.URL.String(),
		"headers", r.Header,
		"remoteAddr", r.RemoteAddr,
	)

	// Get supported protocols from client
	clientSupportedProtocols := strings.Split(r.Header.Get("X-Stream-Protocol-Version"), ",")

	// Define our server supported protocols (in order of preference)
	serverSupportedProtocols := []string{
		"v4.channel.k8s.io",
		"v3.channel.k8s.io",
		"v2.channel.k8s.io",
		"channel.k8s.io",
	}

	// Log for debugging
	s.log.Info("Protocol negotiation",
		"clientSupported", clientSupportedProtocols,
		"serverSupported", serverSupportedProtocols,
	)

	// Get command from query parameters
	command := r.URL.Query()["command"]

	// Parse exec options using the same method as official virtual-kubelet
	streamOpts, err := s.getExecOptions(r)
	if err != nil {
		s.log.Error(err, "Failed to parse exec options",
			"namespace", namespace,
			"pod", pod,
			"container", container,
		)
		http.Error(w, fmt.Sprintf("Invalid exec options: %v", err), http.StatusBadRequest)
		return
	}

	s.log.Info("Parsed exec options",
		"namespace", namespace,
		"pod", pod,
		"container", container,
		"command", command,
		"stdin", streamOpts.Stdin,
		"stdout", streamOpts.Stdout,
		"stderr", streamOpts.Stderr,
		"tty", streamOpts.TTY,
	)

	// Create container exec context like official virtual-kubelet
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exec := &containerExecContext{
		ctx:       ctx,
		server:    s,
		namespace: namespace,
		pod:       pod,
		container: container,
	}

	// Use the same timeout settings as official virtual-kubelet
	idleTimeout := 30 * time.Second
	streamCreationTimeout := 30 * time.Second

	// Serve exec using tke_vnode's proven SPDY implementation - match exact parameter pattern
	localremotecommand.ServeExec(
		w,
		r,
		exec,
		"", // Consistent with tke_vnode, pass empty string
		"", // Consistent with tke_vnode, pass empty string
		container,
		command,
		&localremotecommand.Options{
			Stdin:  streamOpts.Stdin,
			Stdout: streamOpts.Stdout,
			Stderr: streamOpts.Stderr,
			TTY:    streamOpts.TTY,
		},
		idleTimeout,
		streamCreationTimeout,
		serverSupportedProtocols,
	)
}

// getExecOptions parses exec options from the request - same as tke_vnode
func (s *server) getExecOptions(req *http.Request) (*remoteCommandOptions, error) {
	// Add debug info, consistent with tke_vnode
	s.log.Info("Exec request details",
		"method", req.Method,
		"url", req.URL.String(),
		"queryParams", req.URL.Query(),
	)

	// Support two parameter formats, following Kubernetes standard:
	// 1. Standard format: stdin=true, stdout=true, stderr=true, tty=true
	// 2. Numeric format: stdin=1, stdout=1, stderr=1, tty=1

	// TTY parameter
	ttyStr := req.FormValue(corev1.ExecTTYParam)
	tty := ttyStr == trueStr || ttyStr == oneStr

	// Stdin parameter - use Kubernetes standard parameter name
	stdinStr := req.FormValue(corev1.ExecStdinParam)
	stdin := stdinStr == trueStr || stdinStr == oneStr

	// Stdout parameter - use Kubernetes standard parameter name
	stdoutStr := req.FormValue(corev1.ExecStdoutParam)
	stdout := stdoutStr == trueStr || stdoutStr == oneStr

	// Stderr parameter - use Kubernetes standard parameter name
	stderrStr := req.FormValue(corev1.ExecStderrParam)
	stderr := stderrStr == trueStr || stderrStr == oneStr

	s.log.Info("Parsed exec params",
		"tty", fmt.Sprintf("%s(%t)", ttyStr, tty),
		"stdin", fmt.Sprintf("%s(%t)", stdinStr, stdin),
		"stdout", fmt.Sprintf("%s(%t)", stdoutStr, stdout),
		"stderr", fmt.Sprintf("%s(%t)", stderrStr, stderr),
	)

	if tty && stderr {
		return nil, errors.New("cannot exec with tty and stderr")
	}

	if !stdin && !stdout && !stderr {
		s.log.Info("ERROR: No streams specified",
			"stdin", stdin,
			"stdout", stdout,
			"stderr", stderr,
		)
		return nil, errors.New("you must specify at least one of stdin, stdout, stderr")
	}

	return &remoteCommandOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		TTY:    tty,
	}, nil
}

// containerExecContext implements the Executor interface - same pattern as official virtual-kubelet
type containerExecContext struct {
	server    *server
	namespace string
	pod       string
	container string
	ctx       context.Context
}

// handleTerminalResize is a shared helper function that handles terminal resize events
// It converts clientremotecommand.TerminalSize to our TermSize and forwards to the resize channel
func handleTerminalResize(ctx context.Context, resize <-chan clientremotecommand.TerminalSize, resizeCh chan<- TermSize) {
	send := func(s clientremotecommand.TerminalSize) bool {
		select {
		case resizeCh <- TermSize{Width: s.Width, Height: s.Height}:
			return false
		case <-ctx.Done():
			return true
		}
	}

	for {
		select {
		case s := <-resize:
			if send(s) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// ExecInContainer implements remotecommand.Executor interface
func (c *containerExecContext) ExecInContainer(name string, uid types.UID, container string, cmd []string, in io.Reader, out, err io.WriteCloser, tty bool, resize <-chan clientremotecommand.TerminalSize, timeout time.Duration) error {
	// Create execIO like official virtual-kubelet
	eio := &execIO{
		tty:    tty,
		stdin:  in,
		stdout: out,
		stderr: err,
	}

	if tty {
		eio.chResize = make(chan TermSize)
	}

	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()

	if tty {
		go handleTerminalResize(ctx, resize, eio.chResize)
	}

	// Call our Kubelet proxy with the execIO
	return c.server.kubeletProxy.RunInContainer(c.ctx, c.namespace, c.pod, c.container, cmd, eio)
}

// execIO implements AttachIO interface - same as official virtual-kubelet
type execIO struct {
	tty      bool
	stdin    io.Reader
	stdout   io.WriteCloser
	stderr   io.WriteCloser
	chResize chan TermSize
}

func (e *execIO) TTY() bool {
	return e.tty
}

func (e *execIO) Stdin() io.Reader {
	return e.stdin
}

func (e *execIO) Stdout() io.WriteCloser {
	return e.stdout
}

func (e *execIO) Stderr() io.WriteCloser {
	return e.stderr
}

func (e *execIO) HasStdin() bool {
	return e.stdin != nil
}

func (e *execIO) HasStdout() bool {
	return e.stdout != nil
}

func (e *execIO) HasStderr() bool {
	return e.stderr != nil
}

func (e *execIO) Resize() <-chan TermSize {
	return e.chResize
}

// remoteCommandOptions contains details about which streams are required for remote command execution
type remoteCommandOptions struct {
	Stdin  bool
	Stdout bool
	Stderr bool
	TTY    bool
}

// handleHealthz handles health check requests
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

// handleVersion handles version requests
func (s *server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	version := map[string]string{
		"kubeletVersion": "v1.28.0-kubeocean-logs-proxy",
	}
	fmt.Fprintf(w, `{"kubeletVersion": "%s"}`, version["kubeletVersion"])
}

// handleNotFound handles 404 requests
func (s *server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Not Found", http.StatusNotFound)
}

// parseContainerLogOptions parses container log options from query parameters
// This is a shared function used by both server and metrics collector
func parseContainerLogOptions(query map[string][]string) (ContainerLogOpts, error) {
	opts := ContainerLogOpts{}

	if tailLines := getFirstValue(query, "tailLines"); tailLines != "" {
		tail, err := strconv.Atoi(tailLines)
		if err != nil {
			return opts, fmt.Errorf("invalid tailLines: %w", err)
		}
		if tail < 0 {
			return opts, fmt.Errorf("tailLines must be non-negative")
		}
		opts.Tail = tail
	}

	if follow := getFirstValue(query, "follow"); follow != "" {
		followBool, err := strconv.ParseBool(follow)
		if err != nil {
			return opts, fmt.Errorf("invalid follow: %w", err)
		}
		opts.Follow = followBool
	}

	if limitBytes := getFirstValue(query, "limitBytes"); limitBytes != "" {
		limit, err := strconv.Atoi(limitBytes)
		if err != nil {
			return opts, fmt.Errorf("invalid limitBytes: %w", err)
		}
		if limit < 1 {
			return opts, fmt.Errorf("limitBytes must be positive")
		}
		opts.LimitBytes = limit
	}

	if previous := getFirstValue(query, "previous"); previous != "" {
		prev, err := strconv.ParseBool(previous)
		if err != nil {
			return opts, fmt.Errorf("invalid previous: %w", err)
		}
		opts.Previous = prev
	}

	if sinceSeconds := getFirstValue(query, "sinceSeconds"); sinceSeconds != "" {
		since, err := strconv.Atoi(sinceSeconds)
		if err != nil {
			return opts, fmt.Errorf("invalid sinceSeconds: %w", err)
		}
		if since < 1 {
			return opts, fmt.Errorf("sinceSeconds must be positive")
		}
		opts.SinceSeconds = since
	}

	if sinceTime := getFirstValue(query, "sinceTime"); sinceTime != "" {
		since, err := time.Parse(time.RFC3339, sinceTime)
		if err != nil {
			return opts, fmt.Errorf("invalid sinceTime: %w", err)
		}
		if opts.SinceSeconds > 0 {
			return opts, fmt.Errorf("both sinceSeconds and sinceTime cannot be set")
		}
		opts.SinceTime = since
	}

	if timestamps := getFirstValue(query, "timestamps"); timestamps != "" {
		ts, err := strconv.ParseBool(timestamps)
		if err != nil {
			return opts, fmt.Errorf("invalid timestamps: %w", err)
		}
		opts.Timestamps = ts
	}

	return opts, nil
}

// parseLogOptions parses log options from query parameters
func (s *server) parseLogOptions(query map[string][]string) (ContainerLogOpts, error) {
	return parseContainerLogOptions(query)
}

// getFirstValue gets the first value for a key from query parameters
func getFirstValue(query map[string][]string, key string) string {
	if values, exists := query[key]; exists && len(values) > 0 {
		return values[0]
	}
	return ""
}

// loadTLSConfigFromSecret loads TLS configuration from Kubernetes Secret
func (s *server) loadTLSConfigFromSecret() (*tls.Config, error) {
	if s.client == nil {
		return nil, fmt.Errorf("kubernetes client is not available")
	}

	s.log.Info("Loading TLS certificate from Kubernetes Secret",
		"secretName", s.config.SecretName,
		"secretNamespace", s.config.SecretNamespace)

	// Get the secret
	secret, err := s.client.CoreV1().Secrets(s.config.SecretNamespace).Get(
		context.TODO(), s.config.SecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", s.config.SecretNamespace, s.config.SecretName, err)
	}

	// Extract certificate and key from secret
	certData, exists := secret.Data["tls.crt"]
	if !exists {
		return nil, fmt.Errorf("secret %s/%s does not contain tls.crt key", s.config.SecretNamespace, s.config.SecretName)
	}

	keyData, exists := secret.Data["tls.key"]
	if !exists {
		return nil, fmt.Errorf("secret %s/%s does not contain tls.key key", s.config.SecretNamespace, s.config.SecretName)
	}

	// Load certificate and key
	cert, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return nil, fmt.Errorf("failed to load X509 key pair: %w", err)
	}

	var (
		caPool     *x509.CertPool
		clientAuth = tls.RequireAndVerifyClientCert
	)

	if s.config.AllowUnauthenticatedClients {
		clientAuth = tls.NoClientCert
	}

	// Check for CA certificate in secret
	if caData, exists := secret.Data["ca.crt"]; exists && len(caData) > 0 {
		caPool = x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caData) {
			return nil, errors.New("error appending CA cert to certificate pool")
		}
	}

	s.log.Info("Successfully loaded TLS certificate from Kubernetes Secret")

	return &tls.Config{
		Certificates:             []tls.Certificate{cert},
		MinVersion:               tls.VersionTLS12,
		PreferServerCipherSuites: true,
		ClientCAs:                caPool,
		ClientAuth:               clientAuth,
	}, nil
}

// loggingMiddleware logs all incoming requests for debugging
func (s *server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Log incoming request
		s.log.Info("Incoming request",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"remoteAddr", r.RemoteAddr,
			"userAgent", r.UserAgent(),
			"contentLength", r.ContentLength,
		)

		// Create response writer wrapper to capture status code
		wrapped := &responseWriter{ResponseWriter: w, statusCode: 200}

		// Call next handler
		next.ServeHTTP(wrapped, r)

		// Log response
		duration := time.Since(start)
		s.log.Info("Request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"statusCode", wrapped.statusCode,
			"duration", duration.String(),
		)
	})
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// handleDebugCatchAll logs all unmatched requests for debugging
func (s *server) handleDebugCatchAll(w http.ResponseWriter, r *http.Request) {
	s.log.Info("Unmatched request (catch-all)",
		"method", r.Method,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"remoteAddr", r.RemoteAddr,
		"userAgent", r.UserAgent(),
		"headers", r.Header,
	)

	// Return 404 with debug info
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprintf(w, "Path not found: %s %s\n", r.Method, r.URL.Path)
	fmt.Fprintf(w, "Available endpoints:\n")
	fmt.Fprintf(w, "  GET /logs/{namespace}/{pod}/{container}\n")
	fmt.Fprintf(w, "  GET /containerLogs/{namespace}/{pod}/{container}\n")
	fmt.Fprintf(w, "  GET /exec/{namespace}/{pod}/{container}\n")
	fmt.Fprintf(w, "  GET /healthz\n")
	fmt.Fprintf(w, "  GET /version\n")
}
