package logconfig

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clsv1 "github.com/TKEColocation/kubeocean/api/cls/v1"
	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	topcommon "github.com/TKEColocation/kubeocean/pkg/syncer/topdown/common"
)

const (
	// KubeoceanWorkerNamespace is the target namespace for physical cluster resources
	KubeoceanWorkerNamespace = "kubeocean-worker"

	// LogConfig naming constraints
	maxLogConfigNameLength = 63
	maxBaseNameLength      = 54 // 63 - 1 (dash) - 8 (hash) = 54
	hashLength             = 6  // Use first 6 characters of hash
)

// VirtualLogConfigReconciler reconciles LogConfig objects from virtual cluster
type VirtualLogConfigReconciler struct {
	VirtualClient     client.Client
	PhysicalClient    client.Client
	PhysicalK8sClient kubernetes.Interface // Direct k8s client for bypassing cache
	Scheme            *runtime.Scheme
	ClusterBinding    *cloudv1beta1.ClusterBinding
	Log               logr.Logger
	ClusterID         string // Cached cluster ID for performance
}

//+kubebuilder:rbac:groups=cls.cloud.tencent.com,resources=logconfigs,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the main reconciliation logic for virtual logconfigs
func (r *VirtualLogConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualLogConfig", req.NamespacedName)

	// 1. Get the virtual logconfig
	virtualLogConfig := &clsv1.LogConfig{}
	err := r.VirtualClient.Get(ctx, req.NamespacedName, virtualLogConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("Virtual LogConfig not found, checking for cleanup")
			// Handle cleanup of physical resource if virtual is deleted
			return r.handleVirtualLogConfigNotFound(ctx, req.NamespacedName)
		}
		logger.Error(err, "Failed to get virtual LogConfig")
		return ctrl.Result{}, err
	}

	// Note: Removed managed-by label checks as requested - monitor all LogConfigs

	// 2. Check if virtual logconfig is being deleted
	if virtualLogConfig.DeletionTimestamp != nil {
		logger.Info("Virtual LogConfig is being deleted, handling deletion")
		return r.handleVirtualLogConfigDeletion(ctx, virtualLogConfig)
	}

	// 3. Use unified reconcile approach
	logger.Info("Using unified reconcile approach")
	return r.reconcilePhysicalLogConfigs(ctx, virtualLogConfig)
}

// handleVirtualLogConfigNotFound handles cleanup when virtual LogConfig is not found using unified approach
func (r *VirtualLogConfigReconciler) handleVirtualLogConfigNotFound(ctx context.Context, namespacedName types.NamespacedName) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualLogConfig", namespacedName.Name)

	// Use unified approach to find all related physical LogConfigs
	relatedConfigs, err := r.findAllRelatedPhysicalLogConfigs(ctx, namespacedName.Name)
	if err != nil {
		logger.Error(err, "Failed to find related physical LogConfigs during cleanup")
		return ctrl.Result{}, err
	}

	// Delete all related physical LogConfigs
	deletedCount := 0
	for _, physicalLogConfig := range relatedConfigs {
		if r.isLogConfigManagedByThisCluster(physicalLogConfig) {
			logger.Info("Deleting orphaned physical LogConfig", "physicalName", physicalLogConfig.Name)
			err := r.PhysicalClient.Delete(ctx, physicalLogConfig)
			if err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete orphaned physical LogConfig", "physicalName", physicalLogConfig.Name)
				return ctrl.Result{}, err
			}
			deletedCount++
		}
	}

	logger.Info("Cleaned up orphaned physical LogConfigs", "count", deletedCount)
	return ctrl.Result{}, nil
}

// handleVirtualLogConfigDeletion handles deletion of virtual LogConfig using unified approach
func (r *VirtualLogConfigReconciler) handleVirtualLogConfigDeletion(ctx context.Context, virtualLogConfig *clsv1.LogConfig) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualLogConfig", virtualLogConfig.Name)

	// Check if this cluster has the finalizer
	if !r.hasSyncedResourceFinalizer(virtualLogConfig) {
		logger.V(1).Info("Virtual LogConfig doesn't have our finalizer, skipping deletion handling")
		return ctrl.Result{}, nil
	}

	// Use unified approach to find all related physical LogConfigs
	relatedConfigs, err := r.findAllRelatedPhysicalLogConfigs(ctx, virtualLogConfig.Name)
	if err != nil {
		logger.Error(err, "Failed to find related physical LogConfigs for deletion")
		return ctrl.Result{}, err // Let controller-runtime handle retry with exponential backoff
	}

	// Delete all related physical LogConfigs
	deletedCount := 0
	for _, physicalConfig := range relatedConfigs {
		if r.isLogConfigManagedByThisCluster(physicalConfig) {
			logger.Info("Deleting related physical LogConfig", "physicalName", physicalConfig.Name)
			err := r.PhysicalClient.Delete(ctx, physicalConfig)
			if err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "Failed to delete physical LogConfig", "physicalName", physicalConfig.Name)
				// Let controller-runtime handle retry with exponential backoff
				return ctrl.Result{}, err
			}
			deletedCount++
		}
	}

	logger.Info("Deleted physical LogConfigs during virtual LogConfig deletion", "deletedCount", deletedCount)

	// Remove the finalizer
	return ctrl.Result{}, topcommon.RemoveSyncedResourceFinalizerWithClusterID(ctx, virtualLogConfig, r.VirtualClient, r.Log, r.ClusterID)
}

// reconcilePhysicalLogConfigs handles LogConfig synchronization with unified approach
func (r *VirtualLogConfigReconciler) reconcilePhysicalLogConfigs(ctx context.Context, virtualLogConfig *clsv1.LogConfig) (ctrl.Result, error) {
	logger := r.Log.WithValues("virtualLogConfig", virtualLogConfig.Name)

	// Step 0: Add finalizer early to prevent dirty data
	if !r.hasSyncedResourceFinalizer(virtualLogConfig) {
		if err := r.addSyncedResourceFinalizer(ctx, virtualLogConfig); err != nil {
			logger.Error(err, "Failed to add finalizer to virtual LogConfig")
			return ctrl.Result{}, err
		}
		// Informer will automatically requeue the updated object
		return ctrl.Result{}, nil
	}

	// Step 1: Find all existing related physical LogConfigs using labels
	existingConfigs, err := r.findAllRelatedPhysicalLogConfigs(ctx, virtualLogConfig.Name)
	if err != nil {
		logger.Error(err, "Failed to find existing physical LogConfigs")
		return ctrl.Result{}, err
	}

	// Step 2: Generate new physical LogConfigs based on the configuration
	var newConfigs []*clsv1.LogConfig
	var strategy string

	if r.requiresMultiplePhysicalConfigs(virtualLogConfig) {
		// Multiple workload groups: generate multiple physical LogConfigs
		strategy = "multiple"
		newConfigs, err = r.generateMultiplePhysicalLogConfigs(virtualLogConfig)
		if err != nil {
			logger.Error(err, "Failed to generate multiple physical LogConfigs")
			return ctrl.Result{}, err
		}
	} else {
		// Single LogConfig: generate one physical LogConfig
		strategy = "single"
		newConfig := r.generateSinglePhysicalLogConfig(virtualLogConfig, virtualLogConfig.Name)
		newConfigs = []*clsv1.LogConfig{newConfig}
	}

	logger = logger.WithValues("strategy", strategy)

	// Step 3: Compare existing and new configs to decide whether rebuild is needed
	needsRebuild, differences := r.compareLogConfigSpecs(existingConfigs, newConfigs, logger)
	if !needsRebuild {
		logger.Info("No substantial differences found, skipping rebuild",
			"existingCount", len(existingConfigs),
			"newCount", len(newConfigs))

		return ctrl.Result{}, nil
	}

	// Log the differences found
	logger.Info("Substantial differences detected, performing rebuild",
		"differences", differences,
		"existingCount", len(existingConfigs),
		"newCount", len(newConfigs))

	// Step 4: Rebuild - delete all existing and create all new
	err = r.atomicRebuildPhysicalLogConfigs(ctx, existingConfigs, newConfigs, logger)
	if err != nil {
		logger.Error(err, "Failed to rebuild physical LogConfigs")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled physical LogConfigs",
		"strategy", strategy,
		"existingCount", len(existingConfigs),
		"newCount", len(newConfigs))

	return ctrl.Result{}, nil
}

// compareLogConfigSpecs compares existing and new LogConfig specs to determine if rebuild is necessary
func (r *VirtualLogConfigReconciler) compareLogConfigSpecs(existingConfigs, newConfigs []*clsv1.LogConfig, logger logr.Logger) (bool, []string) {
	var differences []string

	// Check if count differs
	if len(existingConfigs) != len(newConfigs) {
		differences = append(differences, fmt.Sprintf("config count changed: %d -> %d", len(existingConfigs), len(newConfigs)))
		return true, differences
	}

	// If both are empty, no rebuild needed
	if len(existingConfigs) == 0 && len(newConfigs) == 0 {
		return false, differences
	}

	// Create maps for easier comparison by name
	existingMap := make(map[string]*clsv1.LogConfig)
	for _, config := range existingConfigs {
		existingMap[config.Name] = config
	}

	newMap := make(map[string]*clsv1.LogConfig)
	for _, config := range newConfigs {
		newMap[config.Name] = config
	}

	// Check for new or removed configs
	for name := range newMap {
		if _, exists := existingMap[name]; !exists {
			differences = append(differences, fmt.Sprintf("new config added: %s", name))
		}
	}

	for name := range existingMap {
		if _, exists := newMap[name]; !exists {
			differences = append(differences, fmt.Sprintf("config removed: %s", name))
		}
	}

	// If there are new/removed configs, rebuild is needed
	if len(differences) > 0 {
		return true, differences
	}

	// Compare specs of configs with same names using deep comparison
	for name, newConfig := range newMap {
		existingConfig, exists := existingMap[name]
		if !exists {
			continue // Already handled above
		}

		// Use deep comparison for the entire spec
		if !r.deepEqualLogConfigSpec(&existingConfig.Spec, &newConfig.Spec) {
			differences = append(differences, fmt.Sprintf("config %s: spec changed", name))
		}
	}

	needsRebuild := len(differences) > 0
	if needsRebuild {
		logger.V(1).Info("Found differences requiring rebuild", "differences", differences)
	} else {
		logger.V(2).Info("No substantial differences found")
	}

	return needsRebuild, differences
}

// deepEqualLogConfigSpec compares two LogConfig specs using deep equality
func (r *VirtualLogConfigReconciler) deepEqualLogConfigSpec(existing, new *clsv1.LogConfigSpec) bool {
	// Use reflect.DeepEqual for comprehensive comparison
	return reflect.DeepEqual(existing, new)
}

// generateSafePhysicalName generates a safe physical name for LogConfig following naming rules:
// 1. Max 63 characters, lowercase letters, numbers, and dash only
// 2. Must start with lowercase letter, end with number or lowercase letter
// 3. If original name <= 54 chars: newname = oldname + "-" + hash[0:6]
// 4. If original name > 54 chars: newname = oldname[0:54] + "-" + hash[0:6]
func (r *VirtualLogConfigReconciler) generateSafePhysicalName(virtualName string) string {
	// Calculate hash of the original name for uniqueness
	hasher := sha256.New()
	hasher.Write([]byte(virtualName))
	hash := fmt.Sprintf("%x", hasher.Sum(nil))
	hashSuffix := hash[:hashLength]

	var baseName string
	if len(virtualName) <= maxBaseNameLength {
		// Rule 1: Name fits within base length limit
		baseName = virtualName
	} else {
		// Rule 2: Name too long, truncate to fit
		baseName = virtualName[:maxBaseNameLength]
	}

	// Construct final name: baseName + "-" + hashSuffix
	physicalName := fmt.Sprintf("%s-%s", baseName, hashSuffix)

	// Validate the result (should always pass given our calculations)
	if len(physicalName) > maxLogConfigNameLength {
		// This should never happen with our logic, but add safety check
		r.Log.Error(nil, "Generated physical name exceeds maximum length",
			"virtualName", virtualName,
			"physicalName", physicalName,
			"length", len(physicalName),
			"maxLength", maxLogConfigNameLength)
	}

	return physicalName
}

// hasSyncedResourceFinalizer checks if the LogConfig has our finalizer
func (r *VirtualLogConfigReconciler) hasSyncedResourceFinalizer(obj client.Object) bool {
	clusterSpecificFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, r.ClusterID)
	return controllerutil.ContainsFinalizer(obj, clusterSpecificFinalizer)
}

// addSyncedResourceFinalizer adds our finalizer to the LogConfig
func (r *VirtualLogConfigReconciler) addSyncedResourceFinalizer(ctx context.Context, obj client.Object) error {
	clusterSpecificFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, r.ClusterID)

	if controllerutil.ContainsFinalizer(obj, clusterSpecificFinalizer) {
		return nil // Already has the finalizer
	}

	updatedObj := obj.DeepCopyObject().(client.Object)
	controllerutil.AddFinalizer(updatedObj, clusterSpecificFinalizer)

	if err := r.VirtualClient.Update(ctx, updatedObj); err != nil {
		return fmt.Errorf("failed to add finalizer to virtual LogConfig: %w", err)
	}

	r.Log.Info("Successfully added finalizer to virtual LogConfig",
		"logconfig", fmt.Sprintf("%s/%s", obj.GetNamespace(), obj.GetName()),
		"finalizer", clusterSpecificFinalizer)
	return nil
}

// copyLabelsAndAnnotations copies labels and annotations from virtual to physical LogConfig
func (r *VirtualLogConfigReconciler) copyLabelsAndAnnotations(virtual, physical *clsv1.LogConfig) {
	// Copy labels (excluding managed labels)
	if virtual.Labels != nil {
		if physical.Labels == nil {
			physical.Labels = make(map[string]string)
		}
		for key, value := range virtual.Labels {
			if !r.isManagedLabel(key) {
				physical.Labels[key] = value
			}
		}
	}

	// Copy annotations (excluding managed annotations)
	if virtual.Annotations != nil {
		if physical.Annotations == nil {
			physical.Annotations = make(map[string]string)
		}
		for key, value := range virtual.Annotations {
			if !r.isManagedAnnotation(key) {
				physical.Annotations[key] = value
			}
		}
	}
}

// updateLabelsAndAnnotations updates labels and annotations, returns true if changed
func (r *VirtualLogConfigReconciler) updateLabelsAndAnnotations(virtual, physical *clsv1.LogConfig) bool {
	changed := false

	// Update labels
	if virtual.Labels != nil {
		if physical.Labels == nil {
			physical.Labels = make(map[string]string)
		}
		for key, value := range virtual.Labels {
			if !r.isManagedLabel(key) {
				if physical.Labels[key] != value {
					physical.Labels[key] = value
					changed = true
				}
			}
		}
	}

	// Update annotations
	if virtual.Annotations != nil {
		if physical.Annotations == nil {
			physical.Annotations = make(map[string]string)
		}
		for key, value := range virtual.Annotations {
			if !r.isManagedAnnotation(key) {
				if physical.Annotations[key] != value {
					physical.Annotations[key] = value
					changed = true
				}
			}
		}
	}

	return changed
}

// isLogConfigManagedByThisCluster checks if a LogConfig is managed by this cluster
func (r *VirtualLogConfigReconciler) isLogConfigManagedByThisCluster(logConfig *clsv1.LogConfig) bool {
	if logConfig.Labels == nil {
		return false
	}

	// Check if managed by this specific cluster
	managedByClusterIDLabel := topcommon.GetManagedByClusterIDLabel(r.ClusterID)
	return logConfig.Labels[managedByClusterIDLabel] == cloudv1beta1.LabelValueTrue
}

// isManagedLabel checks if a label is managed by Kubeocean
func (r *VirtualLogConfigReconciler) isManagedLabel(key string) bool {
	managedLabels := []string{
		topcommon.GetManagedByClusterIDLabel(r.ClusterID),
		cloudv1beta1.LabelVirtualNamespace,
	}

	for _, managedLabel := range managedLabels {
		if key == managedLabel {
			return true
		}
	}
	return false
}

// isManagedAnnotation checks if an annotation is managed by Kubeocean
func (r *VirtualLogConfigReconciler) isManagedAnnotation(key string) bool {
	managedAnnotations := []string{
		cloudv1beta1.AnnotationVirtualName,
		cloudv1beta1.AnnotationPhysicalName,
	}

	for _, managedAnnotation := range managedAnnotations {
		if key == managedAnnotation {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualLogConfigReconciler) SetupWithManager(virtualManager, physicalManager ctrl.Manager) error {
	// Cache cluster ID for performance
	r.ClusterID = r.ClusterBinding.Spec.ClusterID

	// Generate unique controller name using cluster binding name
	controllerName := fmt.Sprintf("virtuallogconfig-%s", r.ClusterBinding.Name)

	return ctrl.NewControllerManagedBy(virtualManager).
		For(&clsv1.LogConfig{}).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
		}).
		// Note: Removed event filters as requested - monitor all LogConfig resources
		Complete(r)
}

// transformLogConfigSpec transforms the LogConfig spec for physical cluster
func (r *VirtualLogConfigReconciler) transformLogConfigSpec(spec *clsv1.LogConfigSpec, logger logr.Logger) *clsv1.LogConfigSpec {
	if spec.InputDetail == nil || spec.InputDetail.Type == nil {
		return spec
	}

	switch *spec.InputDetail.Type {
	case "container_stdout":
		r.transformContainerStdoutSpec(spec, logger)
	case "container_file":
		r.transformContainerFileSpec(spec, logger)
	}

	return spec
}

// transformContainerStdoutSpec transforms container_stdout input specifications
func (r *VirtualLogConfigReconciler) transformContainerStdoutSpec(spec *clsv1.LogConfigSpec, logger logr.Logger) {
	if spec.InputDetail.ContainerStdout == nil {
		return
	}

	containerStdout := spec.InputDetail.ContainerStdout

	// Skip transformation if workloads exist (handled by multiple config generation)
	if len(containerStdout.Workloads) > 0 {
		return
	}
	// Apply transformation rules for container_stdout
	r.applyContainerStdoutRules(containerStdout, logger)
}

// applyContainerStdoutRules applies transformation rules to container_stdout configuration
func (r *VirtualLogConfigReconciler) applyContainerStdoutRules(containerStdout *clsv1.ContainerStdout, logger logr.Logger) {
	// Rule A: Transform namespace to includeLabels (highest priority after workloads)
	if containerStdout.Namespace != nil && *containerStdout.Namespace != "" {
		r.applyNamespaceToIncludeLabelsRuleForContainerStdout(containerStdout, logger)
		return
	}

	// Rule B: Transform excludeNamespace to excludeLabels
	if containerStdout.ExcludeNamespace != nil && *containerStdout.ExcludeNamespace != "" {
		r.applyExcludeNamespaceToExcludeLabelsRuleForContainerStdout(containerStdout, logger)
	}
}

// applyNamespaceToIncludeLabelsRuleForContainerStdout applies Rule A transformation for container_stdout
func (r *VirtualLogConfigReconciler) applyNamespaceToIncludeLabelsRuleForContainerStdout(containerStdout *clsv1.ContainerStdout, logger logr.Logger) {
	if containerStdout == nil || containerStdout.Namespace == nil {
		logger.Error(nil, "containerStdout or namespace is nil in applyNamespaceToIncludeLabelsRuleForContainerStdout")
		return
	}

	logger.Info("Transforming container_stdout configuration for physical cluster (namespace rule)",
		"originalNamespace", *containerStdout.Namespace)

	originalNamespace := *containerStdout.Namespace

	// Transform the configuration
	// (1) Change namespace to KubeoceanWorkerNamespace
	kubeoceanWorker := KubeoceanWorkerNamespace
	containerStdout.Namespace = &kubeoceanWorker

	// (2) Add includeLabels with virtual-namespace mapping (preserve existing labels)
	if containerStdout.IncludeLabels == nil {
		containerStdout.IncludeLabels = make(map[string]string)
	}
	containerStdout.IncludeLabels["kubeocean.io/virtual-namespace"] = originalNamespace

	logger.Info("Successfully transformed container_stdout configuration (namespace rule)",
		"newNamespace", KubeoceanWorkerNamespace,
		"includeLabels", containerStdout.IncludeLabels)
}

// applyExcludeNamespaceToExcludeLabelsRuleForContainerStdout applies Rule B transformation for container_stdout
func (r *VirtualLogConfigReconciler) applyExcludeNamespaceToExcludeLabelsRuleForContainerStdout(containerStdout *clsv1.ContainerStdout, logger logr.Logger) {
	if containerStdout == nil || containerStdout.ExcludeNamespace == nil {
		logger.Error(nil, "containerStdout or excludeNamespace is nil in applyExcludeNamespaceToExcludeLabelsRuleForContainerStdout")
		return
	}

	logger.Info("Transforming container_stdout configuration for physical cluster (excludeNamespace rule)",
		"originalExcludeNamespace", *containerStdout.ExcludeNamespace)

	// Check if both excludeNamespace and excludeLabels exist
	if len(containerStdout.ExcludeLabels) > 0 {
		logger.Error(nil, "Configuration error: both excludeNamespace and excludeLabels are specified, excludeLabels will be overwritten",
			"excludeNamespace", *containerStdout.ExcludeNamespace,
			"existingExcludeLabels", containerStdout.ExcludeLabels)
	}

	originalExcludeNamespace := *containerStdout.ExcludeNamespace

	// Transform the configuration
	// (1) Set namespace to KubeoceanWorkerNamespace
	kubeoceanWorker := KubeoceanWorkerNamespace
	containerStdout.Namespace = &kubeoceanWorker

	// (2) Add excludeLabels with virtual-namespace mapping
	containerStdout.ExcludeLabels = map[string]string{
		"kubeocean.io/virtual-namespace": originalExcludeNamespace,
	}

	// (3) Clear excludeNamespace
	emptyExcludeNamespace := ""
	containerStdout.ExcludeNamespace = &emptyExcludeNamespace

	logger.Info("Successfully transformed container_stdout configuration (excludeNamespace rule)",
		"newNamespace", KubeoceanWorkerNamespace,
		"excludeLabels", containerStdout.ExcludeLabels,
		"clearedExcludeNamespace", true)
}

// transformContainerFileSpec transforms container_file input specifications
func (r *VirtualLogConfigReconciler) transformContainerFileSpec(spec *clsv1.LogConfigSpec, logger logr.Logger) {
	if spec.InputDetail.ContainerFile == nil {
		return
	}

	containerFile := spec.InputDetail.ContainerFile

	// Apply transformation rules for container_file
	r.applyContainerFileRules(containerFile, logger)
}

// applyContainerFileRules applies transformation rules to container_file configuration
func (r *VirtualLogConfigReconciler) applyContainerFileRules(containerFile *clsv1.ContainerFile, logger logr.Logger) {
	// Rule C: Transform workload to includeLabels (highest priority)
	if r.hasValidWorkload(containerFile.Workload) {
		r.applyWorkloadToIncludeLabelsRule(containerFile, logger)
		return
	}

	// Rule A: Transform namespace to includeLabels
	if containerFile.Namespace != nil && *containerFile.Namespace != "" {
		r.applyNamespaceToIncludeLabelsRuleForContainerFile(containerFile, logger)
		return
	}

	// Rule B: Transform excludeNamespace to excludeLabels
	if containerFile.ExcludeNamespace != nil && *containerFile.ExcludeNamespace != "" {
		r.applyExcludeNamespaceToExcludeLabelsRuleForContainerFile(containerFile, logger)
	}
}

// hasValidWorkload checks if workload configuration is valid
func (r *VirtualLogConfigReconciler) hasValidWorkload(workload *clsv1.Workload) bool {
	return workload != nil &&
		workload.Kind != nil && *workload.Kind != "" &&
		workload.Name != nil && *workload.Name != ""
}

// applyNamespaceToIncludeLabelsRuleForContainerFile applies Rule A transformation for container_file
func (r *VirtualLogConfigReconciler) applyNamespaceToIncludeLabelsRuleForContainerFile(containerFile *clsv1.ContainerFile, logger logr.Logger) {
	if containerFile == nil || containerFile.Namespace == nil {
		logger.Error(nil, "containerFile or namespace is nil in applyNamespaceToIncludeLabelsRuleForContainerFile")
		return
	}

	logger.Info("Transforming container_file configuration for physical cluster (namespace rule)",
		"originalNamespace", *containerFile.Namespace)

	originalNamespace := *containerFile.Namespace

	// Transform the configuration
	// (1) Change namespace to KubeoceanWorkerNamespace
	kubeoceanWorker := KubeoceanWorkerNamespace
	containerFile.Namespace = &kubeoceanWorker

	// (2) Add includeLabels with virtual-namespace mapping (preserve existing labels)
	if containerFile.IncludeLabels == nil {
		containerFile.IncludeLabels = make(map[string]string)
	}
	containerFile.IncludeLabels["kubeocean.io/virtual-namespace"] = originalNamespace

	logger.Info("Successfully transformed container_file configuration (namespace rule)",
		"newNamespace", KubeoceanWorkerNamespace,
		"includeLabels", containerFile.IncludeLabels)
}

// applyExcludeNamespaceToExcludeLabelsRuleForContainerFile applies Rule B transformation for container_file
func (r *VirtualLogConfigReconciler) applyExcludeNamespaceToExcludeLabelsRuleForContainerFile(containerFile *clsv1.ContainerFile, logger logr.Logger) {
	if containerFile == nil || containerFile.ExcludeNamespace == nil {
		logger.Error(nil, "containerFile or excludeNamespace is nil in applyExcludeNamespaceToExcludeLabelsRuleForContainerFile")
		return
	}

	logger.Info("Transforming container_file configuration for physical cluster (excludeNamespace rule)",
		"originalExcludeNamespace", *containerFile.ExcludeNamespace)

	// Check if both excludeNamespace and excludeLabels exist
	if len(containerFile.ExcludeLabels) > 0 {
		logger.Error(nil, "Configuration error: both excludeNamespace and excludeLabels are specified, excludeLabels will be overwritten",
			"excludeNamespace", *containerFile.ExcludeNamespace,
			"existingExcludeLabels", containerFile.ExcludeLabels)
	}

	originalExcludeNamespace := *containerFile.ExcludeNamespace

	// Transform the configuration
	// (1) Set namespace to KubeoceanWorkerNamespace
	kubeoceanWorker := KubeoceanWorkerNamespace
	containerFile.Namespace = &kubeoceanWorker

	// (2) Add excludeLabels with virtual-namespace mapping
	containerFile.ExcludeLabels = map[string]string{
		"kubeocean.io/virtual-namespace": originalExcludeNamespace,
	}

	// (3) Clear excludeNamespace
	emptyExcludeNamespace := ""
	containerFile.ExcludeNamespace = &emptyExcludeNamespace

	logger.Info("Successfully transformed container_file configuration (excludeNamespace rule)",
		"newNamespace", KubeoceanWorkerNamespace,
		"excludeLabels", containerFile.ExcludeLabels,
		"clearedExcludeNamespace", true)
}

// applyWorkloadToIncludeLabelsRule applies Rule C transformation (workload to includeLabels)
func (r *VirtualLogConfigReconciler) applyWorkloadToIncludeLabelsRule(containerFile *clsv1.ContainerFile, logger logr.Logger) {
	// Safety checks - this should never happen due to hasValidWorkload check, but be defensive
	if containerFile == nil {
		logger.Error(nil, "containerFile is nil in applyWorkloadToIncludeLabelsRule")
		return
	}
	if containerFile.Workload == nil || containerFile.Workload.Kind == nil || containerFile.Workload.Name == nil {
		logger.Error(nil, "Invalid workload configuration in applyWorkloadToIncludeLabelsRule")
		return
	}

	logger.Info("Transforming container_file configuration for physical cluster (workload rule)",
		"originalNamespace", func() string {
			if containerFile.Namespace != nil {
				return *containerFile.Namespace
			}
			return "<nil>"
		}(),
		"workloadKind", *containerFile.Workload.Kind,
		"workloadName", *containerFile.Workload.Name)

	// Get original namespace for virtual-namespace label
	originalNamespace := "default" // default fallback
	if containerFile.Namespace != nil && *containerFile.Namespace != "" {
		originalNamespace = *containerFile.Namespace
	}

	// Transform the configuration
	// (1) Change namespace to KubeoceanWorkerNamespace
	kubeoceanWorker := KubeoceanWorkerNamespace
	containerFile.Namespace = &kubeoceanWorker

	// (2) Set includeLabels with workload information
	containerFile.IncludeLabels = map[string]string{
		"kubeocean.io/virtual-namespace": originalNamespace,
		"kubeocean.io/workload-name":     *containerFile.Workload.Name,
		"kubeocean.io/workload-type":     *containerFile.Workload.Kind,
	}

	// (3) Clear workload field
	containerFile.Workload = nil

	logger.Info("Successfully transformed container_file configuration (workload rule)",
		"newNamespace", KubeoceanWorkerNamespace,
		"includeLabels", containerFile.IncludeLabels,
		"clearedWorkload", true)
}

// WorkloadGroup represents a group of workloads with same namespace and kind
type WorkloadGroup struct {
	Namespace string
	Kind      string
	Names     []string
}

// groupWorkloads groups workloads by namespace+kind for efficient transformation
func (r *VirtualLogConfigReconciler) groupWorkloads(workloads []clsv1.WorkloadSelector) []WorkloadGroup {
	groupMap := make(map[string]*WorkloadGroup)

	for _, workload := range workloads {
		// Skip invalid workloads
		if workload.Namespace == nil || workload.Kind == nil || workload.Name == nil {
			continue
		}

		// Create group key: namespace/kind
		key := fmt.Sprintf("%s/%s", *workload.Namespace, *workload.Kind)

		if group, exists := groupMap[key]; exists {
			// Add to existing group
			group.Names = append(group.Names, *workload.Name)
		} else {
			// Create new group
			groupMap[key] = &WorkloadGroup{
				Namespace: *workload.Namespace,
				Kind:      *workload.Kind,
				Names:     []string{*workload.Name},
			}
		}
	}

	// Convert map to slice
	groups := make([]WorkloadGroup, 0, len(groupMap))
	for _, group := range groupMap {
		groups = append(groups, *group)
	}

	return groups
}

// requiresMultiplePhysicalConfigs checks if the virtual LogConfig requires multiple physical LogConfigs
func (r *VirtualLogConfigReconciler) requiresMultiplePhysicalConfigs(virtualLogConfig *clsv1.LogConfig) bool {
	if virtualLogConfig.Spec.InputDetail == nil ||
		virtualLogConfig.Spec.InputDetail.Type == nil ||
		*virtualLogConfig.Spec.InputDetail.Type != "container_stdout" ||
		virtualLogConfig.Spec.InputDetail.ContainerStdout == nil {
		return false
	}

	containerStdout := virtualLogConfig.Spec.InputDetail.ContainerStdout

	// Check if workloads exist and require multiple groups
	if len(containerStdout.Workloads) == 0 {
		return false
	}

	// Group workloads and check if we need multiple physical configs
	workloadGroups := r.groupWorkloads(containerStdout.Workloads)
	return len(workloadGroups) >= 1
}

// generateSinglePhysicalLogConfig generates a single physical LogConfig
func (r *VirtualLogConfigReconciler) generateSinglePhysicalLogConfig(virtualLogConfig *clsv1.LogConfig, baseName string) *clsv1.LogConfig {
	// Generate safe physical name using naming rules
	physicalName := r.generateSafePhysicalName(baseName)

	// Create physical LogConfig with transformed spec
	transformedSpec := r.transformLogConfigSpec(virtualLogConfig.Spec.DeepCopy(), r.Log.WithValues("virtualLogConfig", virtualLogConfig.Name))

	physicalConfig := &clsv1.LogConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: physicalName,
			Labels: map[string]string{
				topcommon.GetManagedByClusterIDLabel(r.ClusterID): cloudv1beta1.LabelValueTrue,
				cloudv1beta1.LabelManagedBy:                       cloudv1beta1.LabelManagedByValue,
				"kubeocean.io/virtual-logconfig":                  virtualLogConfig.Name, // Add this label for unified discovery           // Mark as single strategy
			},
			Annotations: map[string]string{
				cloudv1beta1.AnnotationVirtualName: virtualLogConfig.Name,
			},
			Finalizers: []string{}, // Remove all finalizers for physical cluster
		},
		Spec: *transformedSpec,
	}

	// Copy additional labels and annotations from virtual LogConfig
	r.copyLabelsAndAnnotations(virtualLogConfig, physicalConfig)

	return physicalConfig
}

// findAllRelatedPhysicalLogConfigs finds all physical LogConfigs related to a virtual LogConfig
func (r *VirtualLogConfigReconciler) findAllRelatedPhysicalLogConfigs(ctx context.Context, virtualLogConfigName string) ([]*clsv1.LogConfig, error) {
	// Use label selector to find all related physical LogConfigs
	logConfigList := &clsv1.LogConfigList{}
	err := r.PhysicalClient.List(ctx, logConfigList, client.MatchingLabels{
		"kubeocean.io/virtual-logconfig":                  virtualLogConfigName,
		topcommon.GetManagedByClusterIDLabel(r.ClusterID): cloudv1beta1.LabelValueTrue,
		cloudv1beta1.LabelManagedBy:                       cloudv1beta1.LabelManagedByValue,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list related physical LogConfigs: %w", err)
	}

	configs := make([]*clsv1.LogConfig, len(logConfigList.Items))
	for i := range logConfigList.Items {
		configs[i] = &logConfigList.Items[i]
	}

	return configs, nil
}

// generateMultiplePhysicalLogConfigs generates multiple physical LogConfigs based on workload groups
func (r *VirtualLogConfigReconciler) generateMultiplePhysicalLogConfigs(virtualLogConfig *clsv1.LogConfig) ([]*clsv1.LogConfig, error) {
	if virtualLogConfig.Spec.InputDetail == nil ||
		virtualLogConfig.Spec.InputDetail.ContainerStdout == nil ||
		virtualLogConfig.Spec.InputDetail.ContainerStdout.Workloads == nil {
		return nil, fmt.Errorf("invalid LogConfig structure for multiple generation")
	}

	workloads := virtualLogConfig.Spec.InputDetail.ContainerStdout.Workloads
	workloadGroups := r.groupWorkloads(workloads)

	physicalConfigs := make([]*clsv1.LogConfig, 0, len(workloadGroups))

	for i, group := range workloadGroups {
		// Generate base name: virtualName-g0, virtualName-g1, ...
		baseName := fmt.Sprintf("%s-g%d", virtualLogConfig.Name, i)

		// Generate safe physical name using naming rules
		physicalName := r.generateSafePhysicalName(baseName)

		// Create a copy of the virtual spec for transformation
		transformedSpec := virtualLogConfig.Spec.DeepCopy()

		// Transform this specific group
		r.transformSingleWorkloadGroup(transformedSpec, group)

		// Create physical LogConfig
		physicalConfig := &clsv1.LogConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: physicalName,
				Labels: map[string]string{
					topcommon.GetManagedByClusterIDLabel(r.ClusterID): cloudv1beta1.LabelValueTrue,
					cloudv1beta1.LabelManagedBy:                       cloudv1beta1.LabelManagedByValue,
					"kubeocean.io/virtual-logconfig":                  virtualLogConfig.Name,
				},
				Annotations: map[string]string{
					cloudv1beta1.AnnotationVirtualName: virtualLogConfig.Name,
				},
				Finalizers: []string{}, // Remove all finalizers for physical cluster
			},
			Spec: *transformedSpec,
		}

		// Copy additional labels and annotations from virtual LogConfig
		r.copyLabelsAndAnnotations(virtualLogConfig, physicalConfig)

		physicalConfigs = append(physicalConfigs, physicalConfig)
	}

	return physicalConfigs, nil
}

// transformSingleWorkloadGroup transforms a LogConfigSpec for a single workload group
func (r *VirtualLogConfigReconciler) transformSingleWorkloadGroup(spec *clsv1.LogConfigSpec, group WorkloadGroup) {
	if spec.InputDetail == nil || spec.InputDetail.ContainerStdout == nil {
		return
	}

	containerStdout := spec.InputDetail.ContainerStdout

	// Set namespace to KubeoceanWorkerNamespace
	kubeoceanWorker := KubeoceanWorkerNamespace
	containerStdout.Namespace = &kubeoceanWorker

	// Set includeLabels for this group
	containerStdout.IncludeLabels = map[string]string{
		"kubeocean.io/workload-type":     group.Kind,
		"kubeocean.io/virtual-namespace": group.Namespace,
	}

	// Handle single vs multiple names
	if len(group.Names) == 1 {
		containerStdout.IncludeLabels["kubeocean.io/workload-name"] = group.Names[0]
	} else {
		containerStdout.IncludeLabels["kubeocean.io/workload-name"] = strings.Join(group.Names, ",")
	}

	// Clear workloads
	containerStdout.Workloads = nil
}

// atomicRebuildPhysicalLogConfigs performs rebuild: delete old configs first, wait for deletion, then create new ones
func (r *VirtualLogConfigReconciler) atomicRebuildPhysicalLogConfigs(ctx context.Context, existingConfigs, newConfigs []*clsv1.LogConfig, logger logr.Logger) error {
	// Phase 1: Delete all existing configs first
	deletedCount := 0
	for _, oldConfig := range existingConfigs {
		logger.V(1).Info("Deleting existing physical LogConfig", "name", oldConfig.Name)
		if err := r.PhysicalClient.Delete(ctx, oldConfig); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to delete existing physical LogConfig", "name", oldConfig.Name)
			return fmt.Errorf("failed to delete existing config %s: %w", oldConfig.Name, err)
		}
		deletedCount++
	}

	// Phase 2: Wait for all deletions to complete to avoid "object is being deleted" errors
	if deletedCount > 0 {
		logger.V(1).Info("Waiting for all deletions to complete", "count", deletedCount)
		for _, oldConfig := range existingConfigs {
			if err := r.waitForLogConfigDeletion(ctx, oldConfig.Name, logger); err != nil {
				logger.Error(err, "Failed to wait for LogConfig deletion", "name", oldConfig.Name)
				return fmt.Errorf("failed to wait for deletion of config %s: %w", oldConfig.Name, err)
			}
		}
		logger.V(1).Info("All deletions completed")
	}

	// Phase 3: Create all new configs
	createdCount := 0
	for _, newConfig := range newConfigs {
		logger.V(1).Info("Creating new physical LogConfig", "name", newConfig.Name)
		if err := r.PhysicalClient.Create(ctx, newConfig); err != nil {
			// If still getting "object is being deleted", it means the deletion is still in progress
			if apierrors.IsAlreadyExists(err) && strings.Contains(err.Error(), "object is being deleted") {
				logger.Info("LogConfig still being deleted, will retry", "name", newConfig.Name)
				return fmt.Errorf("logconfig %s is still being deleted, will retry: %w", newConfig.Name, err)
			}
			logger.Error(err, "Failed to create new physical LogConfig", "name", newConfig.Name)
			return fmt.Errorf("failed to create new config %s: %w", newConfig.Name, err)
		}
		createdCount++
	}

	logger.Info("Rebuild completed successfully",
		"deletedCount", deletedCount,
		"createdCount", createdCount)

	return nil
}

// waitForLogConfigDeletion waits for a LogConfig to be completely deleted
func (r *VirtualLogConfigReconciler) waitForLogConfigDeletion(ctx context.Context, configName string, logger logr.Logger) error {
	// Poll until the resource is completely gone
	for i := 0; i < 30; i++ { // Max 30 attempts
		config := &clsv1.LogConfig{}
		err := r.PhysicalClient.Get(ctx, types.NamespacedName{Name: configName}, config)
		if apierrors.IsNotFound(err) {
			// Resource is completely deleted
			logger.V(2).Info("LogConfig deletion confirmed", "name", configName, "attempts", i+1)
			return nil
		}
		if err != nil {
			// Some other error occurred
			return fmt.Errorf("error checking deletion status: %w", err)
		}

		// Resource still exists, wait a bit
		logger.V(2).Info("LogConfig still exists, waiting...", "name", configName, "attempt", i+1)

		// Use exponential backoff: 100ms, 200ms, 400ms, 800ms, then 1s max
		waitTime := 100 * (1 << i) // 100ms * 2^i
		if waitTime > 1000 {
			waitTime = 1000 // Cap at 1 second
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(waitTime) * time.Millisecond):
			// Continue to next iteration
		}
	}

	return fmt.Errorf("timeout waiting for LogConfig %s to be deleted", configName)
}
