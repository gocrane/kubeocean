package topdown

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
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

	// Controllers
	virtualPodController       *VirtualPodReconciler
	virtualConfigMapController *VirtualConfigMapReconciler
	virtualSecretController    *VirtualSecretReconciler
	virtualPVCController       *VirtualPVCReconciler
	virtualPVController        *VirtualPVReconciler
	virtualLogConfigController *VirtualLogConfigReconciler
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

	tds.Log.Info("Controllers setup completed",
		"virtualPodController", "enabled",
		"virtualConfigMapController", "enabled",
		"virtualSecretController", "enabled",
		"virtualPVCController", "enabled",
		"virtualPVController", "enabled",
		"virtualLogConfigController", "enabled")
	return nil
}
