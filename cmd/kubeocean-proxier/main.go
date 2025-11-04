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

package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	"github.com/gocrane/kubeocean/pkg/proxier"
	"github.com/gocrane/kubeocean/pkg/version"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cloudv1beta1.AddToScheme(scheme))
}

func main() {
	// Parse command line flags
	config := parseFlags()
	if config.ClusterBindingName == "" {
		setupLog.Error(fmt.Errorf("cluster-binding-name is required"), "missing required parameter")
		os.Exit(1)
	}

	// Setup
	setupLog.Info("Kubeocean Proxier", "version", version.Get())

	setupLog.Info("Starting Kubeocean Proxier",
		"clusterBinding", config.ClusterBindingName,
		"listenPort", config.ListenPort)

	// Setup context and signal handling
	ctx, cancel := setupContextAndSignals()
	defer cancel()

	// Create clients and get cluster binding
	virtualClient, physicalClient, physicalConfig, clusterBinding, err := setupClients(ctx, config.ClusterBindingName)
	if err != nil {
		setupLog.Error(err, "failed to setup clients")
		os.Exit(1)
	}

	// Setup authentication
	tokenManager, err := setupAuthentication(ctx, physicalConfig)
	if err != nil {
		setupLog.Error(err, "failed to setup authentication")
		os.Exit(1)
	}

	// Setup pod monitoring
	err = setupPodMonitoring(ctx, physicalConfig)
	if err != nil {
		setupLog.Error(err, "failed to setup pod monitoring")
		os.Exit(1)
	}

	// Setup TLS configuration
	tlsConfig, err := setupTLSConfiguration(ctx, config, clusterBinding)
	if err != nil {
		setupLog.Error(err, "failed to setup TLS configuration")
		os.Exit(1)
	}

	// Setup proxier services
	kubeletProxy, httpServer, vnodeProxierAgent, err := setupProxierServices(ctx, config, tlsConfig, virtualClient, physicalClient, physicalConfig, clusterBinding, tokenManager)
	if err != nil {
		setupLog.Error(err, "failed to setup proxier services")
		os.Exit(1)
	}

	// Setup node monitoring
	err = setupNodeMonitoring(ctx, config, vnodeProxierAgent)
	if err != nil {
		setupLog.Error(err, "failed to setup node monitoring")
		os.Exit(1)
	}

	// Start all services
	err = startServices(ctx, config, kubeletProxy, httpServer, vnodeProxierAgent)
	if err != nil {
		setupLog.Error(err, "failed to start services")
		os.Exit(1)
	}

	setupLog.Info("Kubeocean Proxier started successfully")

	// Wait for shutdown
	<-ctx.Done()
	setupLog.Info("Shutting down Kubeocean Proxier")

	// Stop services
	stopServices(vnodeProxierAgent, httpServer, kubeletProxy)
	setupLog.Info("Kubeocean Proxier stopped")
}

// ProxierConfig holds configuration for the proxier
type ProxierConfig struct {
	ClusterBindingName     string
	ListenPort             int
	TLSSecretName          string
	TLSSecretNamespace     string
	MetricsEnabled         bool
	MetricsCollectInterval int
	MetricsTargetNamespace string
	VnodeBasePort          int
}

// TLSConfiguration holds TLS setup results
type TLSConfiguration struct {
	Enabled         bool
	SecretName      string
	SecretNamespace string
	CertManager     *proxier.CertificateManager
}

// parseFlags parses command line flags and returns configuration
func parseFlags() *ProxierConfig {
	config := &ProxierConfig{}

	flag.StringVar(&config.ClusterBindingName, "cluster-binding-name", "",
		"The name of the ClusterBinding resource this proxier is responsible for.")
	flag.IntVar(&config.ListenPort, "listen-port", 10250,
		"The port to listen on for Kubelet API requests.")
	flag.StringVar(&config.TLSSecretName, "tls-secret-name", "",
		"The name of the Kubernetes secret containing TLS certificates for HTTPS.")
	flag.StringVar(&config.TLSSecretNamespace, "tls-secret-namespace", "",
		"The namespace of the Kubernetes secret containing TLS certificates.")
	flag.BoolVar(&config.MetricsEnabled, "metrics-enabled", true,
		"Enable metrics collection from physical cluster nodes.")
	flag.IntVar(&config.MetricsCollectInterval, "metrics-collect-interval", 60,
		"The interval in seconds for collecting metrics from nodes.")
	flag.StringVar(&config.MetricsTargetNamespace, "metrics-target-namespace", "",
		"The target namespace for metrics collection. Empty means all namespaces.")
	flag.IntVar(&config.VnodeBasePort, "prometheus-vnode-base-port", 9006,
		"The port for Prometheus VNode HTTP server. Proxier will bind exactly this port.")

	opts := zap.Options{
		Development:     false,
		StacktraceLevel: zapcore.DPanicLevel,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	return config
}

// setupContextAndSignals sets up context and signal handling
func setupContextAndSignals() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		setupLog.Info("Received shutdown signal")
		cancel()
	}()

	return ctx, cancel
}

// setupClients creates virtual and physical cluster clients
func setupClients(ctx context.Context, clusterBindingName string) (client.Client, kubernetes.Interface, *rest.Config, *cloudv1beta1.ClusterBinding, error) {
	// Create virtual cluster client
	virtualConfig := ctrl.GetConfigOrDie()
	virtualClient, err := client.New(virtualConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unable to create virtual cluster client: %w", err)
	}

	// Get ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{}
	err = virtualClient.Get(ctx, types.NamespacedName{Name: clusterBindingName}, clusterBinding)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unable to get ClusterBinding %s: %w", clusterBindingName, err)
	}

	setupLog.Info("Retrieved ClusterBinding",
		"clusterID", clusterBinding.Spec.ClusterID,
		"mountNamespace", clusterBinding.Spec.MountNamespace)

	// Create physical cluster clients
	physicalConfig, physicalClient, err := createPhysicalClusterClients(ctx, virtualClient, clusterBinding)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unable to create physical cluster clients: %w", err)
	}

	setupLog.Info("Connected to physical cluster", "host", physicalConfig.Host)
	return virtualClient, physicalClient, physicalConfig, clusterBinding, nil
}

// setupAuthentication sets up token manager and authentication
func setupAuthentication(ctx context.Context, physicalConfig *rest.Config) (*proxier.TokenManager, error) {
	tokenManager := proxier.NewTokenManager(
		ctrl.Log.WithName("token-manager"),
		physicalConfig,
	)

	setupLog.Info("Extracting and saving authentication token from physical cluster")
	if err := tokenManager.ExtractAndSaveToken(ctx); err != nil {
		return nil, fmt.Errorf("unable to extract and save token from physical cluster: %w", err)
	}

	setupLog.Info("Authentication token extracted and ready for use")
	return tokenManager, nil
}

// setupPodMonitoring sets up pod controller and monitoring
func setupPodMonitoring(ctx context.Context, physicalConfig *rest.Config) error {
	proxier.InitGlobalPodMapper()
	setupLog.Info("Global pod mapper initialized")

	setupLog.Info("Setting up POD Controller for physical cluster")

	// Create physical cluster manager
	physicalManager, err := ctrl.NewManager(physicalConfig, ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		HealthProbeBindAddress: "0",
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Pod{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					}),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("unable to create physical manager for pod controller: %w", err)
	}

	// Create and setup POD controller
	podController := proxier.NewPodController(
		physicalManager.GetClient(),
		physicalManager.GetScheme(),
		ctrl.Log.WithName("pod-controller"),
	)

	if err := podController.SetupWithManager(physicalManager); err != nil {
		return fmt.Errorf("unable to setup pod controller: %w", err)
	}

	// Start physical manager
	go func() {
		setupLog.Info("Starting physical cluster manager for pod controller")
		if err := physicalManager.Start(ctx); err != nil {
			setupLog.Error(err, "problem running physical manager")
		}
	}()

	// Initialize existing pods
	time.Sleep(2 * time.Second)
	setupLog.Info("Initializing existing pods in physical cluster")
	if err := podController.InitializeExistingPods(ctx); err != nil {
		return fmt.Errorf("failed to initialize existing pods: %w", err)
	}
	setupLog.Info("Pod controller initialization completed")

	return nil
}

// createPhysicalClusterClients creates clients for the physical cluster
func createPhysicalClusterClients(ctx context.Context, virtualClient client.Client, clusterBinding *cloudv1beta1.ClusterBinding) (*rest.Config, kubernetes.Interface, error) {
	// Read the kubeconfig secret
	kubeconfigData, err := readKubeconfigSecret(ctx, virtualClient, clusterBinding.Spec.SecretRef)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read kubeconfig secret: %w", err)
	}

	// Create rest config from kubeconfig
	physicalConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create REST config from kubeconfig: %w", err)
	}

	// Create kubernetes client
	physicalClient, err := kubernetes.NewForConfig(physicalConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create physical cluster client: %w", err)
	}

	return physicalConfig, physicalClient, nil
}

// readKubeconfigSecret reads the kubeconfig data from the referenced secret
func readKubeconfigSecret(ctx context.Context, client client.Client, secretRef corev1.SecretReference) ([]byte, error) {
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: secretRef.Namespace,
	}

	if err := client.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig secret %s/%s: %w", secretRef.Namespace, secretRef.Name, err)
	}

	kubeconfigData, exists := secret.Data["kubeconfig"]
	if !exists {
		return nil, fmt.Errorf("kubeconfig key not found in secret %s/%s", secretRef.Namespace, secretRef.Name)
	}

	if len(kubeconfigData) == 0 {
		return nil, fmt.Errorf("kubeconfig data is empty in secret %s/%s", secretRef.Namespace, secretRef.Name)
	}

	return kubeconfigData, nil
}

// getClusterBindingSecretAnnotation gets the TLS secret name from ClusterBinding annotation
func getClusterBindingSecretAnnotation(clusterBinding *cloudv1beta1.ClusterBinding) string {
	if clusterBinding.Annotations == nil {
		return ""
	}
	return clusterBinding.Annotations["kubeocean.io/logs-proxy-secret-name"]
}

// getClusterBindingSecretNamespaceAnnotation gets the TLS secret namespace from ClusterBinding annotation
func getClusterBindingSecretNamespaceAnnotation(clusterBinding *cloudv1beta1.ClusterBinding) string {
	if clusterBinding.Annotations == nil {
		return ""
	}
	return clusterBinding.Annotations["kubeocean.io/logs-proxy-secret-namespace"]
}

// validateExternalTLSSecret validates that an external TLS secret exists and contains valid certificate data
func validateExternalTLSSecret(ctx context.Context, client kubernetes.Interface, secretName, secretNamespace string) error {
	// Get the secret
	secret, err := client.CoreV1().Secrets(secretNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get TLS secret %s/%s: %w", secretNamespace, secretName, err)
	}

	// Check if it's a TLS secret
	if secret.Type != corev1.SecretTypeTLS {
		return fmt.Errorf("secret %s/%s is not of type %s, got %s", secretNamespace, secretName, corev1.SecretTypeTLS, secret.Type)
	}

	// Validate required fields
	certData, hasCert := secret.Data["tls.crt"]
	keyData, hasKey := secret.Data["tls.key"]

	if !hasCert {
		return fmt.Errorf("TLS secret %s/%s missing required 'tls.crt' field", secretNamespace, secretName)
	}
	if !hasKey {
		return fmt.Errorf("TLS secret %s/%s missing required 'tls.key' field", secretNamespace, secretName)
	}

	if len(certData) == 0 {
		return fmt.Errorf("TLS secret %s/%s has empty 'tls.crt' field", secretNamespace, secretName)
	}
	if len(keyData) == 0 {
		return fmt.Errorf("TLS secret %s/%s has empty 'tls.key' field", secretNamespace, secretName)
	}

	// Validate certificate format
	block, _ := pem.Decode(certData)
	if block == nil {
		return fmt.Errorf("TLS secret %s/%s contains invalid certificate PEM data", secretNamespace, secretName)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("TLS secret %s/%s contains invalid certificate: %w", secretNamespace, secretName, err)
	}

	// Check if certificate is expired
	if time.Now().After(cert.NotAfter) {
		return fmt.Errorf("TLS certificate in secret %s/%s is expired (expired on %s)", secretNamespace, secretName, cert.NotAfter.Format(time.RFC3339))
	}

	// Warn if certificate expires soon (within 7 days)
	if time.Until(cert.NotAfter) < 7*24*time.Hour {
		setupLog.Info("Warning: TLS certificate expires soon",
			"secretName", secretName,
			"secretNamespace", secretNamespace,
			"expiresAt", cert.NotAfter.Format(time.RFC3339),
			"timeUntilExpiry", time.Until(cert.NotAfter).String())
	}

	// Validate private key format
	keyBlock, _ := pem.Decode(keyData)
	if keyBlock == nil {
		return fmt.Errorf("TLS secret %s/%s contains invalid private key PEM data", secretNamespace, secretName)
	}

	// Try to parse as different key types
	if _, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err != nil {
		if _, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err != nil {
			if _, err := x509.ParseECPrivateKey(keyBlock.Bytes); err != nil {
				return fmt.Errorf("TLS secret %s/%s contains unsupported private key format", secretNamespace, secretName)
			}
		}
	}

	setupLog.Info("External TLS secret validation successful",
		"secretName", secretName,
		"secretNamespace", secretNamespace,
		"certSubject", cert.Subject.String(),
		"certExpiry", cert.NotAfter.Format(time.RFC3339))

	return nil
}

// setupTLSConfiguration sets up TLS configuration based on priority
func setupTLSConfiguration(ctx context.Context, config *ProxierConfig, clusterBinding *cloudv1beta1.ClusterBinding) (*TLSConfiguration, error) {
	virtualClientset, err := kubernetes.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		return nil, fmt.Errorf("unable to create virtual cluster clientset: %w", err)
	}

	tlsConfig := &TLSConfiguration{}

	// Priority 1: Command line parameters
	if config.TLSSecretName != "" && config.TLSSecretNamespace != "" {
		tlsConfig.Enabled = true
		tlsConfig.SecretName = config.TLSSecretName
		tlsConfig.SecretNamespace = config.TLSSecretNamespace

		setupLog.Info("Using TLS secret from command line parameters",
			"secretName", tlsConfig.SecretName,
			"secretNamespace", tlsConfig.SecretNamespace,
			"managementType", "external")

		if err := validateExternalTLSSecret(ctx, virtualClientset, tlsConfig.SecretName, tlsConfig.SecretNamespace); err != nil {
			return nil, fmt.Errorf("external TLS secret validation failed: %w", err)
		}
		return tlsConfig, nil
	}

	// Priority 2 & 3: ClusterBinding annotation or automatic management
	certManager := proxier.NewCertificateManager(
		virtualClientset,
		clusterBinding,
		"kubeocean-system",
		ctrl.Log.WithName("cert-manager"),
	)
	tlsConfig.CertManager = certManager

	// Check ClusterBinding annotation
	if annotationSecretName := getClusterBindingSecretAnnotation(clusterBinding); annotationSecretName != "" {
		annotationSecretNamespace := getClusterBindingSecretNamespaceAnnotation(clusterBinding)
		if annotationSecretNamespace == "" {
			annotationSecretNamespace = clusterBinding.Namespace
		}

		tlsConfig.Enabled = true
		tlsConfig.SecretName = annotationSecretName
		tlsConfig.SecretNamespace = annotationSecretNamespace

		setupLog.Info("Using TLS secret from ClusterBinding annotation",
			"secretName", tlsConfig.SecretName,
			"secretNamespace", tlsConfig.SecretNamespace,
			"managementType", "external")

		if err := validateExternalTLSSecret(ctx, virtualClientset, tlsConfig.SecretName, tlsConfig.SecretNamespace); err != nil {
			return nil, fmt.Errorf("clusterBinding annotated TLS secret validation failed: %w", err)
		}
		return tlsConfig, nil
	}

	// Priority 3: Automatic certificate management
	setupLog.Info("No external TLS configuration found, using automatic certificate management", "managementType", "automatic")

	tlsSecret, err := certManager.GetOrCreateTLSSecret(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create TLS secret: %w", err)
	}

	certManager.StartAutoRenewal(ctx)
	tlsConfig.Enabled = true
	tlsConfig.SecretName = tlsSecret.Name
	tlsConfig.SecretNamespace = tlsSecret.Namespace

	setupLog.Info("Using automatically managed TLS certificate",
		"secretName", tlsConfig.SecretName,
		"secretNamespace", tlsConfig.SecretNamespace,
		"managementType", "automatic")

	return tlsConfig, nil
}

// setupProxierServices sets up kubelet proxy, HTTP server, and optionally VNode proxier agent
func setupProxierServices(ctx context.Context, config *ProxierConfig, tlsConfig *TLSConfiguration, virtualClient client.Client, physicalClient kubernetes.Interface, physicalConfig *rest.Config, clusterBinding *cloudv1beta1.ClusterBinding, tokenManager *proxier.TokenManager) (proxier.KubeletProxy, proxier.HTTPServer, *proxier.VNodeProxierAgent, error) {
	virtualClientset, err := kubernetes.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to create virtual cluster clientset: %w", err)
	}

	// Create kubelet proxy configuration
	proxierConfig := &proxier.Config{
		Enabled:                     true,
		ListenAddr:                  fmt.Sprintf(":%d", config.ListenPort),
		AllowUnauthenticatedClients: true,
		TLSConfig:                   nil,
		SecretName:                  tlsConfig.SecretName,
		SecretNamespace:             tlsConfig.SecretNamespace,
	}

	if tlsConfig.Enabled {
		proxierConfig.TLSConfig = &proxier.TLSConfig{}
		setupLog.Info("TLS enabled", "secretName", tlsConfig.SecretName, "secretNamespace", tlsConfig.SecretNamespace)
	} else {
		setupLog.Info("TLS disabled - running in HTTP mode")
	}

	// Create Kubelet proxy
	kubeletProxy := proxier.NewKubeletProxy(
		virtualClient,
		physicalClient,
		physicalConfig,
		clusterBinding,
		ctrl.Log.WithName("kubelet-proxy"),
	)

	// Create HTTP server
	httpServer := proxier.NewHTTPServer(
		proxierConfig,
		kubeletProxy,
		virtualClientset,
		ctrl.Log.WithName("http-server"),
	)

	// Create VNode proxier agent if metrics enabled
	var vnodeProxierAgent *proxier.VNodeProxierAgent
	if config.MetricsEnabled {
		setupLog.Info("Setting up VNode proxier agent with kubelet proxy support")

		metricsConfig := &proxier.MetricsConfig{
			CollectInterval:    time.Duration(config.MetricsCollectInterval) * time.Second,
			MaxConcurrentNodes: 100,
			TargetNamespace:    config.MetricsTargetNamespace,
			DebugLog:           false,
		}

		if tlsConfig.Enabled {
			metricsConfig.TLSSecretName = tlsConfig.SecretName
			metricsConfig.TLSSecretNamespace = tlsConfig.SecretNamespace
		}

		vnodeProxierAgent = proxier.NewVNodeProxierAgent(
			metricsConfig,
			tokenManager,
			kubeletProxy,
			virtualClientset,
			clusterBinding.Spec.ClusterID,
			ctrl.Log.WithName("vnode-proxier-agent"),
		)

		if err := vnodeProxierAgent.Start(ctx); err != nil {
			return nil, nil, nil, fmt.Errorf("unable to start VNode proxier agent: %w", err)
		}
		setupLog.Info("VNode proxier agent started successfully with logs/exec endpoints")
	} else {
		setupLog.Info("Metrics collection disabled")
	}

	return kubeletProxy, httpServer, vnodeProxierAgent, nil
}

// setupNodeMonitoring sets up node controller and monitoring
func setupNodeMonitoring(ctx context.Context, config *ProxierConfig, vnodeProxierAgent *proxier.VNodeProxierAgent) error {
	setupLog.Info("Setting up Node Controller for proxier", "clusterBinding", config.ClusterBindingName)

	virtualConfig := ctrl.GetConfigOrDie()
	virtualManager, err := ctrl.NewManager(virtualConfig, ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		HealthProbeBindAddress: "0",
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Node{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelClusterBinding: config.ClusterBindingName,
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
					}),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("unable to create virtual manager for node controller: %w", err)
	}

	nodeController := &proxier.NodeController{
		Client:             virtualManager.GetClient(),
		Scheme:             virtualManager.GetScheme(),
		Log:                ctrl.Log.WithName("proxier-node-controller"),
		ClusterBindingName: config.ClusterBindingName,
		CurrentNodes:       make(map[string]proxier.NodeInfo),
	}

	if err := nodeController.SetupWithManager(virtualManager); err != nil {
		return fmt.Errorf("unable to setup node controller: %w", err)
	}

	if vnodeProxierAgent != nil {
		nodeController.SetMetricsCollector(vnodeProxierAgent)
		setupLog.Info("Connected NodeController with VNodeProxierAgent")
	}

	go func() {
		if err := virtualManager.Start(ctx); err != nil {
			setupLog.Error(err, "problem running virtual manager")
		}
	}()

	if !virtualManager.GetCache().WaitForCacheSync(ctx) {
		return fmt.Errorf("failed to wait for cache sync")
	}

	if err := nodeController.SyncExistingNodes(ctx); err != nil {
		return fmt.Errorf("failed to sync existing nodes: %w", err)
	}

	if vnodeProxierAgent != nil {
		existingNodes := nodeController.GetCurrentNodes()
		if len(existingNodes) > 0 {
			setupLog.Info("Initializing VNodeProxierAgent with existing nodes", "nodeCount", len(existingNodes))
			vnodeProxierAgent.InitializeWithNodes(existingNodes)
		}
	}

	setupLog.Info("Node Controller setup completed successfully")
	return nil
}

// startServices starts all the services
func startServices(ctx context.Context, config *ProxierConfig, kubeletProxy proxier.KubeletProxy, httpServer proxier.HTTPServer, vnodeProxierAgent *proxier.VNodeProxierAgent) error {
	if err := kubeletProxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start Kubelet proxy: %w", err)
	}

	if err := httpServer.Start(ctx); err != nil {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	if vnodeProxierAgent != nil {
		if err := proxier.StartVNodeHTTPServer(vnodeProxierAgent, ctrl.Log.WithName("vnode-http-server"), config.VnodeBasePort); err != nil {
			return fmt.Errorf("failed to start VNode HTTP server: %w", err)
		}
	} else {
		setupLog.Info("VNode proxier agent is not enabled, skipping VNode HTTP server")
	}

	return nil
}

// stopServices stops all services gracefully
func stopServices(vnodeProxierAgent *proxier.VNodeProxierAgent, httpServer proxier.HTTPServer, kubeletProxy proxier.KubeletProxy) {
	if vnodeProxierAgent != nil {
		vnodeProxierAgent.Stop()
	}

	if err := httpServer.Stop(); err != nil {
		setupLog.Error(err, "failed to stop HTTP server")
	}

	if err := kubeletProxy.Stop(); err != nil {
		setupLog.Error(err, "failed to stop Kubelet proxy")
	}
}
