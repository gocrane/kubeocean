package topdown

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/syncer/topdown/token"
)

// TopDownSyncer handles synchronization from virtual cluster to physical cluster
// This implements requirement 5.1, 5.2, 5.4, 5.5 - Top-down synchronization
type TopDownSyncer struct {
	Scheme         *runtime.Scheme
	Log            logr.Logger
	ClusterBinding *cloudv1beta1.ClusterBinding

	// Virtual cluster manager for VirtualPod controller (passed from KubeoceanSyncer)
	virtualManager manager.Manager
	// Physical cluster manager for creating physical pods (passed from KubeoceanSyncer)
	physicalManager manager.Manager
	// Physical cluster config for direct k8s client (passed from KubeoceanSyncer)
	physicalConfig *rest.Config

	// prometheusVNodeBasePort is the base port used in VNode Prometheus URL annotations
	prometheusVNodeBasePort int

	// Controllers
	virtualPodController       *VirtualPodReconciler
	virtualConfigMapController *VirtualConfigMapReconciler
	virtualSecretController    *VirtualSecretReconciler
	virtualPVCController       *VirtualPVCReconciler
	virtualPVController        *VirtualPVReconciler
	virtualLogConfigController *VirtualLogConfigReconciler
	proxierWatchController     *ProxierWatchController
}

// NewTopDownSyncer creates a new TopDownSyncer instance
func NewTopDownSyncer(virtualManager manager.Manager, physicalManager manager.Manager, physicalConfig *rest.Config, scheme *runtime.Scheme, binding *cloudv1beta1.ClusterBinding) *TopDownSyncer {
	log := ctrl.Log.WithName("top-down-syncer").WithValues("cluster", binding.Name)

	return &TopDownSyncer{
		virtualManager:  virtualManager,  // Passed from KubeoceanSyncer
		physicalManager: physicalManager, // Passed from KubeoceanSyncer
		physicalConfig:  physicalConfig,  // Passed from KubeoceanSyncer for direct k8s client
		Scheme:          scheme,
		Log:             log,
		ClusterBinding:  binding,
	}
}

// SetPrometheusVNodeBasePort sets the base port used in VNode Prometheus URL annotations
func (tds *TopDownSyncer) SetPrometheusVNodeBasePort(port int) {
	tds.prometheusVNodeBasePort = port
}

func (tds *TopDownSyncer) Setup(ctx context.Context) error {
	tds.Log.Info("Setup Top-down Syncer with controller-runtime")

	// Setup controllers
	if err := tds.setupControllers(); err != nil {
		return fmt.Errorf("failed to setup controllers: %w", err)
	}
	return nil
}

// Start starts the top-down syncer with controller-runtime approach
func (tds *TopDownSyncer) Start(ctx context.Context) error {
	tds.Log.Info("Top-down Syncer started successfully")

	// Start serviceAccountToken garbage collection
	go tds.startServiceAccountTokenGC(ctx)

	// Keep running until context is cancelled
	<-ctx.Done()
	tds.Log.Info("Top-down Syncer stopping, cleaning up resources")

	// No additional cleanup needed as controller-runtime handles it
	tds.Log.Info("Top-down Syncer stopped")

	return nil
}

// setupControllers sets up all the controllers
func (tds *TopDownSyncer) setupControllers() error {
	// Create direct k8s client for virtual cluster
	virtualConfig := tds.virtualManager.GetConfig()
	virtualK8sClient, err := kubernetes.NewForConfig(virtualConfig)
	if err != nil {
		return fmt.Errorf("failed to create direct k8s client for virtual cluster: %w", err)
	}

	// Create direct k8s client for bypassing cache
	physicalK8sClient, err := kubernetes.NewForConfig(tds.physicalConfig)
	if err != nil {
		return fmt.Errorf("failed to create direct k8s client for physical cluster: %w", err)
	}

	if tds.ClusterBinding == nil {
		return fmt.Errorf("cluster binding is nil")
	}
	if tds.ClusterBinding.Spec.ClusterID == "" {
		return fmt.Errorf("cluster binding cluster ID is empty")
	}
	if tds.ClusterBinding.Spec.MountNamespace == "" {
		return fmt.Errorf("cluster binding mount namespace is empty")
	}

	// Setup Virtual Pod Controller
	tds.virtualPodController = &VirtualPodReconciler{
		VirtualClient:     tds.virtualManager.GetClient(),
		PhysicalClient:    tds.physicalManager.GetClient(),
		PhysicalK8sClient: physicalK8sClient,
		Scheme:            tds.Scheme,
		ClusterBinding:    tds.ClusterBinding,
		clusterID:         tds.ClusterBinding.Spec.ClusterID,
		Log:               tds.Log.WithName("virtual-pod-controller"),
		TokenManager:      token.NewManager(virtualK8sClient, tds.Log.WithName("token-manager")),
	}

	if err := tds.virtualPodController.SetupWithManager(tds.virtualManager, tds.physicalManager); err != nil {
		return fmt.Errorf("failed to setup virtual pod controller: %w", err)
	}

	// Setup Virtual ConfigMap Controller
	tds.virtualConfigMapController = &VirtualConfigMapReconciler{
		VirtualClient:     tds.virtualManager.GetClient(),
		PhysicalClient:    tds.physicalManager.GetClient(),
		PhysicalK8sClient: physicalK8sClient,
		Scheme:            tds.Scheme,
		ClusterBinding:    tds.ClusterBinding,
		clusterID:         tds.ClusterBinding.Spec.ClusterID,
		Log:               tds.Log.WithName("virtual-configmap-controller"),
	}

	if err := tds.virtualConfigMapController.SetupWithManager(tds.virtualManager, tds.physicalManager); err != nil {
		return fmt.Errorf("failed to setup virtual configmap controller: %w", err)
	}

	// Setup Virtual Secret Controller
	tds.virtualSecretController = &VirtualSecretReconciler{
		VirtualClient:     tds.virtualManager.GetClient(),
		PhysicalClient:    tds.physicalManager.GetClient(),
		PhysicalK8sClient: physicalK8sClient,
		Scheme:            tds.Scheme,
		ClusterBinding:    tds.ClusterBinding,
		clusterID:         tds.ClusterBinding.Spec.ClusterID,
		Log:               tds.Log.WithName("virtual-secret-controller"),
	}

	if err := tds.virtualSecretController.SetupWithManager(tds.virtualManager, tds.physicalManager); err != nil {
		return fmt.Errorf("failed to setup virtual secret controller: %w", err)
	}

	// Setup Virtual PVC Controller
	tds.virtualPVCController = &VirtualPVCReconciler{
		VirtualClient:     tds.virtualManager.GetClient(),
		PhysicalClient:    tds.physicalManager.GetClient(),
		PhysicalK8sClient: physicalK8sClient,
		Scheme:            tds.Scheme,
		ClusterBinding:    tds.ClusterBinding,
		clusterID:         tds.ClusterBinding.Spec.ClusterID,
		Log:               tds.Log.WithName("virtual-pvc-controller"),
	}

	if err := tds.virtualPVCController.SetupWithManager(tds.virtualManager, tds.physicalManager); err != nil {
		return fmt.Errorf("failed to setup virtual pvc controller: %w", err)
	}

	// Setup Virtual PV Controller
	tds.virtualPVController = &VirtualPVReconciler{
		VirtualClient:     tds.virtualManager.GetClient(),
		PhysicalClient:    tds.physicalManager.GetClient(),
		PhysicalK8sClient: physicalK8sClient,
		Scheme:            tds.Scheme,
		ClusterBinding:    tds.ClusterBinding,
		clusterID:         tds.ClusterBinding.Spec.ClusterID,
		Log:               tds.Log.WithName("virtual-pv-controller"),
	}

	if err := tds.virtualPVController.SetupWithManager(tds.virtualManager, tds.physicalManager); err != nil {
		return fmt.Errorf("failed to setup virtual pv controller: %w", err)
	}

	// Setup Virtual LogConfig Controller
	tds.virtualLogConfigController = &VirtualLogConfigReconciler{
		VirtualClient:     tds.virtualManager.GetClient(),
		PhysicalClient:    tds.physicalManager.GetClient(),
		PhysicalK8sClient: physicalK8sClient,
		Scheme:            tds.Scheme,
		ClusterBinding:    tds.ClusterBinding,
		clusterID:         tds.ClusterBinding.Spec.ClusterID,
		Log:               tds.Log.WithName("virtual-logconfig-controller"),
	}

	if err := tds.virtualLogConfigController.SetupWithManager(tds.virtualManager, tds.physicalManager); err != nil {
		return fmt.Errorf("failed to setup virtual logconfig controller: %w", err)
	}

	// Setup Proxier Watch Controller
	tds.proxierWatchController = NewProxierWatchController(
		tds.virtualManager.GetClient(),
		tds.Scheme,
		tds.Log.WithName("proxier-watch-controller"),
		tds.ClusterBinding,
		tds.prometheusVNodeBasePort,
	)

	if err := tds.proxierWatchController.SetupWithManager(tds.virtualManager); err != nil {
		return fmt.Errorf("failed to setup proxier watch controller: %w", err)
	}

	tds.Log.Info("Controllers setup completed",
		"virtualPodController", "enabled",
		"virtualConfigMapController", "enabled",
		"virtualSecretController", "enabled",
		"virtualPVCController", "enabled",
		"virtualPVController", "enabled",
		"virtualLogConfigController", "enabled",
		"proxierWatchController", "enabled")
	return nil
}

// startServiceAccountTokenGC starts the serviceAccountToken garbage collection routine using k8s wait library
func (tds *TopDownSyncer) startServiceAccountTokenGC(ctx context.Context) {
	logger := tds.Log.WithName("sa-token-gc")
	logger.Info("Starting serviceAccountToken garbage collection", "interval", "5m")

	// Use wait.UntilWithContext for more elegant polling with proper context handling
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		tds.runServiceAccountTokenGC(ctx, logger)
	}, 5*time.Minute)

	logger.Info("ServiceAccountToken garbage collection stopped")
}

// runServiceAccountTokenGC performs the garbage collection of serviceAccountToken secrets
func (tds *TopDownSyncer) runServiceAccountTokenGC(ctx context.Context, logger logr.Logger) {
	logger.V(1).Info("Running serviceAccountToken garbage collection")

	secrets, err := tds.listServiceAccountTokenSecrets(ctx)
	if err != nil {
		logger.Error(err, "Failed to list serviceAccountToken secrets")
		return
	}

	if len(secrets) == 0 {
		logger.V(1).Info("No serviceAccountToken secrets found")
		return
	}

	saTokenSecretNames := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		saTokenSecretNames = append(saTokenSecretNames, secret.Name)
	}

	logger.V(1).Info("Found serviceAccountToken secrets", "count", len(secrets), "names", saTokenSecretNames)

	deletedCount := 0
	failedCount := 0

	// Process secrets with context check for graceful shutdown
	for _, secret := range secrets {
		// Check context cancellation between operations
		select {
		case <-ctx.Done():
			logger.Info("ServiceAccountToken garbage collection cancelled",
				"processed", deletedCount+failedCount, "total", len(secrets))
			return
		default:
		}

		if tds.shouldDeleteServiceAccountTokenSecret(ctx, &secret, logger) {
			logger.Info("Deleting orphaned serviceAccountToken secret",
				"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name))

			if err := tds.physicalManager.GetClient().Delete(ctx, &secret); err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "Failed to delete orphaned serviceAccountToken secret",
						"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name))
					failedCount++
					continue
				}
				logger.V(1).Info("ServiceAccountToken secret already deleted",
					"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name))
			}

			logger.Info("Successfully deleted serviceAccountToken secret",
				"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name))
			deletedCount++
		}
	}

	if deletedCount > 0 || failedCount > 0 {
		logger.Info("ServiceAccountToken garbage collection completed",
			"total", len(secrets), "deleted", deletedCount, "failed", failedCount)
	} else {
		logger.V(1).Info("ServiceAccountToken garbage collection completed, no orphaned secrets found",
			"total", len(secrets))
	}
}

// listServiceAccountTokenSecrets lists all secrets with serviceAccountToken label
func (tds *TopDownSyncer) listServiceAccountTokenSecrets(ctx context.Context) ([]corev1.Secret, error) {
	secretList := &corev1.SecretList{}

	// List secrets with serviceAccountToken label in the mount namespace
	listOptions := []client.ListOption{
		client.InNamespace(tds.ClusterBinding.Spec.MountNamespace),
		client.MatchingLabels{
			cloudv1beta1.LabelServiceAccountToken: "true",
		},
	}

	if err := tds.physicalManager.GetClient().List(ctx, secretList, listOptions...); err != nil {
		return nil, fmt.Errorf("failed to list serviceAccountToken secrets: %w", err)
	}

	return secretList.Items, nil
}

// shouldDeleteServiceAccountTokenSecret checks if a serviceAccountToken secret should be deleted
func (tds *TopDownSyncer) shouldDeleteServiceAccountTokenSecret(ctx context.Context, secret *corev1.Secret, logger logr.Logger) bool {
	// Extract virtual pod information from secret annotations
	virtualPodName := secret.Annotations[cloudv1beta1.AnnotationVirtualPodName]
	virtualPodNamespace := secret.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]
	virtualPodUID := secret.Annotations[cloudv1beta1.AnnotationVirtualPodUID]

	if virtualPodName == "" || virtualPodNamespace == "" {
		logger.V(1).Info("ServiceAccountToken secret missing virtual pod annotations, marking for deletion",
			"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name))
		return true
	}

	// Check if the corresponding virtual pod still exists
	virtualPod := &corev1.Pod{}
	err := tds.virtualManager.GetClient().Get(ctx, types.NamespacedName{
		Name:      virtualPodName,
		Namespace: virtualPodNamespace,
	}, virtualPod)

	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Virtual pod not found, marking serviceAccountToken secret for deletion",
				"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name),
				"virtualPod", fmt.Sprintf("%s/%s", virtualPodNamespace, virtualPodName))
			return true
		}
		logger.Error(err, "Error checking virtual pod existence, skipping secret",
			"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name),
			"virtualPod", fmt.Sprintf("%s/%s", virtualPodNamespace, virtualPodName))
		return false
	}

	if virtualPodUID != "" && string(virtualPod.UID) != virtualPodUID {
		logger.V(1).Info("Virtual pod UID mismatch, marking serviceAccountToken secret for deletion",
			"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name),
			"virtualPod", fmt.Sprintf("%s/%s", virtualPodNamespace, virtualPodName),
			"virtualPodUID", virtualPodUID,
			"actualUID", string(virtualPod.UID))
		return true
	}

	logger.V(2).Info("Virtual pod exists, keeping serviceAccountToken secret",
		"secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name),
		"virtualPod", fmt.Sprintf("%s/%s", virtualPodNamespace, virtualPodName))
	return false
}