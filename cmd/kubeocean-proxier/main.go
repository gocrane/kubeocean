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

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/proxier"
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
	var clusterBindingName string
	var listenPort int
	var tlsSecretName string
	var tlsSecretNamespace string
	var nodesFilePath string

	flag.StringVar(&clusterBindingName, "cluster-binding-name", "",
		"The name of the ClusterBinding resource this proxier is responsible for.")
	flag.IntVar(&listenPort, "listen-port", 10250,
		"The port to listen on for Kubelet API requests.")
	flag.StringVar(&tlsSecretName, "tls-secret-name", "",
		"The name of the Kubernetes secret containing TLS certificates for HTTPS.")
	flag.StringVar(&tlsSecretNamespace, "tls-secret-namespace", "",
		"The namespace of the Kubernetes secret containing TLS certificates.")
	flag.StringVar(&nodesFilePath, "nodes-file-path", "/tmp/nodes.txt",
		"The path to the nodes.txt file for storing node information.")

	opts := zap.Options{
		Development:     false,
		StacktraceLevel: zapcore.DPanicLevel,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if clusterBindingName == "" {
		setupLog.Error(fmt.Errorf("cluster-binding-name is required"), "missing required parameter")
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	setupLog.Info("Starting Kubeocean Proxier",
		"clusterBinding", clusterBindingName,
		"listenPort", listenPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		setupLog.Info("Received shutdown signal")
		cancel()
	}()

	// Create virtual cluster client (in-cluster config)
	virtualConfig := ctrl.GetConfigOrDie()
	virtualClient, err := client.New(virtualConfig, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create virtual cluster client")
		os.Exit(1)
	}

	// Create virtual cluster Kubernetes clientset for TLS secret access
	virtualClientset, err := kubernetes.NewForConfig(virtualConfig)
	if err != nil {
		setupLog.Error(err, "unable to create virtual cluster clientset")
		os.Exit(1)
	}

	// Get ClusterBinding to retrieve physical cluster connection info
	clusterBinding := &cloudv1beta1.ClusterBinding{}
	err = virtualClient.Get(ctx, types.NamespacedName{Name: clusterBindingName}, clusterBinding)
	if err != nil {
		setupLog.Error(err, "unable to get ClusterBinding", "name", clusterBindingName)
		os.Exit(1)
	}

	setupLog.Info("Retrieved ClusterBinding",
		"clusterID", clusterBinding.Spec.ClusterID,
		"mountNamespace", clusterBinding.Spec.MountNamespace)

	// Get physical cluster kubeconfig from secret
	physicalConfig, physicalClient, err := createPhysicalClusterClients(ctx, virtualClient, clusterBinding)
	if err != nil {
		setupLog.Error(err, "unable to create physical cluster clients")
		os.Exit(1)
	}

	setupLog.Info("Connected to physical cluster", "host", physicalConfig.Host)

	// Determine TLS configuration with comprehensive priority handling
	var finalSecretName, finalSecretNamespace string
	var tlsConfigEnabled bool
	var certManager *proxier.CertificateManager

	// Priority 1: Command line parameters (highest priority, external management)
	if tlsSecretName != "" && tlsSecretNamespace != "" {
		finalSecretName = tlsSecretName
		finalSecretNamespace = tlsSecretNamespace
		tlsConfigEnabled = true
		setupLog.Info("Using TLS secret from command line parameters",
			"secretName", finalSecretName,
			"secretNamespace", finalSecretNamespace,
			"managementType", "external")

		// Validate external secret exists and is valid
		if err := validateExternalTLSSecret(ctx, virtualClientset, finalSecretName, finalSecretNamespace); err != nil {
			setupLog.Error(err, "External TLS secret validation failed")
			os.Exit(1)
		}

	} else {
		// Priority 2 & 3: ClusterBinding annotation or automatic management
		certManager = proxier.NewCertificateManager(
			virtualClientset,
			clusterBinding,
			"kubeocean-system", // Default namespace for auto-managed certificates
			ctrl.Log.WithName("cert-manager"),
		)

		// Check ClusterBinding annotation for external secret
		if annotationSecretName := getClusterBindingSecretAnnotation(clusterBinding); annotationSecretName != "" {
			// Priority 2: ClusterBinding annotation (external management)
			annotationSecretNamespace := getClusterBindingSecretNamespaceAnnotation(clusterBinding)
			if annotationSecretNamespace == "" {
				annotationSecretNamespace = clusterBinding.Namespace // Default to ClusterBinding namespace
			}

			finalSecretName = annotationSecretName
			finalSecretNamespace = annotationSecretNamespace
			tlsConfigEnabled = true
			setupLog.Info("Using TLS secret from ClusterBinding annotation",
				"secretName", finalSecretName,
				"secretNamespace", finalSecretNamespace,
				"managementType", "external")

			// Validate external secret exists and is valid
			if err := validateExternalTLSSecret(ctx, virtualClientset, finalSecretName, finalSecretNamespace); err != nil {
				setupLog.Error(err, "ClusterBinding annotated TLS secret validation failed")
				os.Exit(1)
			}

		} else {
			// Priority 3: Automatic certificate management (lowest priority)
			setupLog.Info("No external TLS configuration found, using automatic certificate management",
				"managementType", "automatic")

			tlsSecret, err := certManager.GetOrCreateTLSSecret(ctx)
			if err != nil {
				setupLog.Error(err, "Failed to get or create TLS secret")
				os.Exit(1)
			}

			// Start automatic certificate renewal for auto-managed certificates
			certManager.StartAutoRenewal(ctx)
			defer certManager.StopAutoRenewal()

			finalSecretName = tlsSecret.Name
			finalSecretNamespace = tlsSecret.Namespace
			tlsConfigEnabled = true
			setupLog.Info("Using automatically managed TLS certificate",
				"secretName", finalSecretName,
				"secretNamespace", finalSecretNamespace,
				"managementType", "automatic")
		}
	}

	// Create Kubelet proxy configuration
	config := &proxier.Config{
		Enabled:                     true,
		ListenAddr:                  fmt.Sprintf(":%d", listenPort),
		AllowUnauthenticatedClients: true, // Allow health probes and kubectl without client certs
		TLSConfig:                   nil,  // Will be set if TLS is enabled
		SecretName:                  finalSecretName,
		SecretNamespace:             finalSecretNamespace,
	}

	// Enable TLS if we have a secret
	if tlsConfigEnabled {
		config.TLSConfig = &proxier.TLSConfig{} // Enable TLS
		setupLog.Info("TLS enabled", "secretName", finalSecretName, "secretNamespace", finalSecretNamespace)
	} else {
		setupLog.Info("TLS disabled - running in HTTP mode")
	}

	// Setup Node Controller for proxier
	setupLog.Info("Setting up Node Controller for proxier", "clusterBinding", clusterBindingName)
	
	// Create virtual cluster manager for Node controller
	virtualManager, err := ctrl.NewManager(virtualConfig, ctrl.Options{
		Scheme: scheme,
		// Only watch Node resources with specific labels
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Node{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelClusterBinding: clusterBindingName,
						cloudv1beta1.LabelManagedBy:      cloudv1beta1.LabelManagedByValue,
					}),
				},
			},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to create virtual manager for node controller")
		os.Exit(1)
	}

	// Create Node controller
	nodeController := &proxier.NodeController{
		Client:            virtualManager.GetClient(),
		Scheme:            virtualManager.GetScheme(),
		Log:               ctrl.Log.WithName("proxier-node-controller"),
		ClusterBindingName: clusterBindingName,
		NodesFile:         nodesFilePath,
		CurrentNodes:      make(map[string]proxier.NodeInfo),
	}

	// Validate nodes file path
	if err := nodeController.ValidateNodesFile(); err != nil {
		setupLog.Error(err, "unable to validate nodes file path")
		os.Exit(1)
	}

	// Setup Node controller
	if err := nodeController.SetupWithManager(virtualManager); err != nil {
		setupLog.Error(err, "unable to setup node controller")
		os.Exit(1)
	}

	// Start virtual manager (runs in background)
	go func() {
		if err := virtualManager.Start(ctx); err != nil {
			setupLog.Error(err, "problem running virtual manager")
		}
	}()

	// Wait for manager cache to sync
	if !virtualManager.GetCache().WaitForCacheSync(ctx) {
		setupLog.Error(fmt.Errorf("failed to wait for cache sync"), "unable to sync caches")
		os.Exit(1)
	}

	// Sync existing nodes
	if err := nodeController.SyncExistingNodes(ctx); err != nil {
		setupLog.Error(err, "failed to sync existing nodes")
		os.Exit(1)
	}

	setupLog.Info("Node Controller setup completed successfully")

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
		config,
		kubeletProxy,
		virtualClientset, // Use virtual clientset for TLS secret access since secrets are in virtual cluster
		ctrl.Log.WithName("http-server"),
	)

	// Start Kubelet proxy
	if err := kubeletProxy.Start(ctx); err != nil {
		setupLog.Error(err, "failed to start Kubelet proxy")
		os.Exit(1)
	}

	// Start HTTP server
	if err := httpServer.Start(ctx); err != nil {
		setupLog.Error(err, "failed to start HTTP server")
		os.Exit(1)
	}

	setupLog.Info("Kubeocean Proxier started successfully")

	// Wait for context cancellation
	<-ctx.Done()

	setupLog.Info("Shutting down Kubeocean Proxier")

	// Stop services
	if err := httpServer.Stop(); err != nil {
		setupLog.Error(err, "failed to stop HTTP server")
	}

	if err := kubeletProxy.Stop(); err != nil {
		setupLog.Error(err, "failed to stop Kubelet proxy")
	}

	setupLog.Info("Kubeocean Proxier stopped")
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
