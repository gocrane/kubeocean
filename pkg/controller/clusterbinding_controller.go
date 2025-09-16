package controller

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"text/template"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/metrics"
	"github.com/TKEColocation/kubeocean/pkg/proxier"
)

const (
	// ClusterBindingFinalizer is the finalizer used for ClusterBinding resources
	ClusterBindingFinalizer = "clusterbinding.cloud.tencent.com/finalizer"

	// Phase constants
	PhaseFailed = "Failed"

	// Default names
	DefaultSyncerName  = "kubeocean-syncer"
	DefaultProxierName = "kubeocean-proxier"
)

// SyncerTemplateData holds the data for rendering Syncer templates
type SyncerTemplateData struct {
	ClusterBindingName     string
	DeploymentName         string
	ServiceAccountName     string
	ClusterRoleName        string
	ClusterRoleBindingName string
	SyncerNamespace        string
}

// ProxierTemplateData holds the data for rendering Proxier templates
type ProxierTemplateData struct {
	ClusterBindingName     string
	DeploymentName         string
	ServiceAccountName     string
	ServiceName            string
	ClusterRoleName        string
	ClusterRoleBindingName string
	ProxierNamespace       string
	// TLS configuration from ClusterBinding annotations
	TLSEnabled         bool
	TLSSecretName      string
	TLSSecretNamespace string
}

// ResourceCleanupStatus tracks the cleanup status of different resource types
type ResourceCleanupStatus struct {
	Deployment         bool
	ServiceAccount     bool
	ClusterRole        bool
	ClusterRoleBinding bool
}

// ClusterBindingReconciler reconciles a ClusterBinding object
type ClusterBindingReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=cloud.tencent.com,resources=clusterbindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cloud.tencent.com,resources=clusterbindings/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cloud.tencent.com,resources=clusterbindings/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;list;watch;update;delete
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/legacy-unknown,verbs=approve
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClusterBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("clusterbinding", req.Name)

	// Fetch the ClusterBinding instance
	var originalClusterBinding cloudv1beta1.ClusterBinding
	if err := r.Get(ctx, req.NamespacedName, &originalClusterBinding); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ClusterBinding resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch ClusterBinding")
		return ctrl.Result{}, err
	}

	// Create a deep copy to avoid modifying the cached object
	clusterBinding := originalClusterBinding.DeepCopy()

	log.Info("Reconciling ClusterBinding", "name", clusterBinding.Name, "phase", clusterBinding.Status.Phase)

	// Update metrics
	r.updateMetrics(clusterBinding)

	// Record event for reconciliation start
	r.Recorder.Event(clusterBinding, corev1.EventTypeNormal, "Reconciling", "Starting ClusterBinding reconciliation")

	// Handle deletion
	if clusterBinding.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &originalClusterBinding)
	}

	// Add finalizer if not present
	if !r.hasFinalizer(clusterBinding) {
		r.addFinalizer(clusterBinding)
		if err := r.Update(ctx, clusterBinding); err != nil {
			log.Error(err, "unable to add finalizer")
			return ctrl.Result{}, err
		}
		log.Info("Added finalizer to ClusterBinding")
		// Return without requeue - the update will trigger a new reconcile via watch
		return ctrl.Result{}, nil
	}

	// Initialize status if not set
	if clusterBinding.Status.Phase == "" {
		clusterBinding.Status.Phase = "Pending"
		clusterBinding.Status.Conditions = []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "Initializing",
				Message:            "ClusterBinding is being initialized",
				LastTransitionTime: metav1.Now(),
			},
		}
		if err := r.Status().Update(ctx, clusterBinding); err != nil {
			log.Error(err, "unable to update ClusterBinding status")
			return ctrl.Result{}, err
		}
		log.Info("Update status to Pending")
		r.Recorder.Event(clusterBinding, corev1.EventTypeNormal, "StatusUpdated", "ClusterBinding status initialized")
		return ctrl.Result{}, nil
	}

	// Validate the ClusterBinding configuration
	if err := r.validateClusterBinding(clusterBinding); err != nil {
		log.Error(err, "ClusterBinding validation failed")
		r.Recorder.Event(clusterBinding, corev1.EventTypeWarning, "ValidationFailed", fmt.Sprintf("Validation failed: %v", err))

		// Update status to Failed
		clusterBinding.Status.Phase = PhaseFailed
		r.updateCondition(clusterBinding, "Ready", metav1.ConditionFalse, "ValidationFailed", err.Error())
		if updateErr := r.Status().Update(ctx, clusterBinding); updateErr != nil {
			log.Error(updateErr, "unable to update ClusterBinding status after validation failure")
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, err
	}

	// Validate kubeconfig secret and test cluster connectivity
	if err := r.validateKubeconfigAndConnectivity(ctx, clusterBinding); err != nil {
		log.Error(err, "Kubeconfig validation or connectivity check failed")
		r.Recorder.Event(clusterBinding, corev1.EventTypeWarning, "ConnectivityFailed", fmt.Sprintf("Connectivity check failed: %v", err))

		// Update status to Failed with connectivity condition
		clusterBinding.Status.Phase = PhaseFailed
		r.updateCondition(clusterBinding, "Ready", metav1.ConditionFalse, "ConnectivityFailed", err.Error())
		r.updateCondition(clusterBinding, "Connected", metav1.ConditionFalse, "ConnectivityFailed", err.Error())
		if updateErr := r.Status().Update(ctx, clusterBinding); updateErr != nil {
			log.Error(updateErr, "unable to update ClusterBinding status after connectivity failure")
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, err
	}

	// Create or update Kubeocean Proxier
	if err := r.reconcileKubeoceanProxier(ctx, clusterBinding); err != nil {
		log.Error(err, "Failed to reconcile Kubeocean Proxier")
		r.Recorder.Event(clusterBinding, corev1.EventTypeWarning, "ProxierFailed", fmt.Sprintf("Failed to reconcile Kubeocean Proxier: %v", err))

		// Update status to Failed for other errors
		clusterBinding.Status.Phase = PhaseFailed
		r.updateCondition(clusterBinding, "Ready", metav1.ConditionFalse, "ProxierFailed", err.Error())
		if updateErr := r.Status().Update(ctx, clusterBinding); updateErr != nil {
			log.Error(updateErr, "unable to update ClusterBinding status after proxier failure")
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, err
	} else {
		r.updateCondition(clusterBinding, "ProxierReady", metav1.ConditionTrue, "ProxierCreated", "Kubeocean Proxier created successfully")
	}

	// Create or update Kubeocean Syncer
	if err := r.reconcileKubeoceanSyncer(ctx, clusterBinding); err != nil {
		log.Error(err, "Failed to reconcile Kubeocean Syncer")
		r.Recorder.Event(clusterBinding, corev1.EventTypeWarning, "SyncerFailed", fmt.Sprintf("Failed to reconcile Kubeocean Syncer: %v", err))

		// Update status to Failed for other errors
		clusterBinding.Status.Phase = PhaseFailed
		r.updateCondition(clusterBinding, "Ready", metav1.ConditionFalse, "SyncerFailed", err.Error())
		if updateErr := r.Status().Update(ctx, clusterBinding); updateErr != nil {
			log.Error(updateErr, "unable to update ClusterBinding status after syncer failure")
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, err
	} else {
		r.updateCondition(clusterBinding, "SyncerReady", metav1.ConditionTrue, "SyncerCreated", "Kubeocean Syncer created successfully")
	}

	// Mark as Ready if validation and syncer creation passes
	if clusterBinding.Status.Phase != "Ready" {
		clusterBinding.Status.Phase = "Ready"
		clusterBinding.Status.LastSyncTime = &metav1.Time{Time: time.Now()}
		r.updateCondition(clusterBinding, "Ready", metav1.ConditionTrue, "ValidationPassed", "ClusterBinding validation and connectivity check passed")
		r.updateCondition(clusterBinding, "Connected", metav1.ConditionTrue, "ConnectivityPassed", "Successfully connected to target cluster")
		r.updateCondition(clusterBinding, "SyncerReady", metav1.ConditionTrue, "SyncerCreated", "Kubeocean Syncer created successfully")
		r.updateCondition(clusterBinding, "ProxierReady", metav1.ConditionTrue, "ProxierCreated", "Kubeocean Proxier created successfully")
		if err := r.Status().Update(ctx, clusterBinding); err != nil {
			log.Error(err, "unable to update ClusterBinding status to Ready")
			return ctrl.Result{}, err
		}
		r.Recorder.Event(clusterBinding, corev1.EventTypeNormal, "Ready", "ClusterBinding is ready and connected")
	}

	log.Info("ClusterBinding reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// validateClusterBinding validates the ClusterBinding configuration
func (r *ClusterBindingReconciler) validateClusterBinding(clusterBinding *cloudv1beta1.ClusterBinding) error {
	// Validate ClusterID
	if clusterBinding.Spec.ClusterID == "" {
		return fmt.Errorf("clusterID is required")
	}

	// Validate SecretRef
	if clusterBinding.Spec.SecretRef.Name == "" {
		return fmt.Errorf("secretRef.name is required")
	}
	if clusterBinding.Spec.SecretRef.Namespace == "" {
		return fmt.Errorf("secretRef.namespace is required")
	}

	// Validate MountNamespace
	if clusterBinding.Spec.MountNamespace == "" {
		return fmt.Errorf("mountNamespace is required")
	}

	// Validate ServiceNamespaces (if provided, should not be empty strings)
	for i, ns := range clusterBinding.Spec.ServiceNamespaces {
		if ns == "" {
			return fmt.Errorf("serviceNamespaces[%d] cannot be empty", i)
		}
	}

	return nil
}

// validateKubeconfigAndConnectivity validates the kubeconfig secret and tests cluster connectivity
func (r *ClusterBindingReconciler) validateKubeconfigAndConnectivity(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding) error {
	// Read the kubeconfig secret
	kubeconfigData, err := r.readKubeconfigSecret(ctx, clusterBinding.Spec.SecretRef)
	if err != nil {
		return fmt.Errorf("failed to read kubeconfig secret: %w", err)
	}

	// Validate kubeconfig format
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return fmt.Errorf("invalid kubeconfig format: %w", err)
	}

	// Test cluster connectivity
	if err := r.testClusterConnectivity(config); err != nil {
		return fmt.Errorf("cluster connectivity test failed: %w", err)
	}

	return nil
}

// readKubeconfigSecret reads the kubeconfig data from the referenced secret
func (r *ClusterBindingReconciler) readKubeconfigSecret(ctx context.Context, secretRef corev1.SecretReference) ([]byte, error) {
	var secret corev1.Secret
	secretKey := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: secretRef.Namespace,
	}

	if err := r.Get(ctx, secretKey, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("kubeconfig secret %s/%s not found", secretRef.Namespace, secretRef.Name)
		}
		return nil, fmt.Errorf("failed to get kubeconfig secret: %w", err)
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

// testClusterConnectivity tests connectivity to the target cluster
func (r *ClusterBindingReconciler) testClusterConnectivity(config *rest.Config) error {
	// Set timeout for connectivity test
	config.Timeout = 30 * time.Second

	// Create kubernetes client
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Test connectivity by getting cluster version
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = clientset.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("failed to connect to cluster: %w", err)
	}

	// Test basic permissions by listing nodes (if accessible)
	_, err = clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		// Log warning but don't fail - the user might not have node list permissions
		r.Log.V(1).Info("Warning: cannot list nodes, this might affect syncer functionality", "error", err)
	}

	return nil
}

// updateCondition updates a condition in the ClusterBinding status
func (r *ClusterBindingReconciler) updateCondition(clusterBinding *cloudv1beta1.ClusterBinding, conditionType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	// Find existing condition and update it, or append new one
	for i, existingCondition := range clusterBinding.Status.Conditions {
		if existingCondition.Type == conditionType {
			if existingCondition.Status != status {
				condition.LastTransitionTime = metav1.Now()
			} else {
				condition.LastTransitionTime = existingCondition.LastTransitionTime
			}
			clusterBinding.Status.Conditions[i] = condition
			return
		}
	}
	clusterBinding.Status.Conditions = append(clusterBinding.Status.Conditions, condition)
}

// updateMetrics updates Prometheus metrics for ClusterBinding
func (r *ClusterBindingReconciler) updateMetrics(clusterBinding *cloudv1beta1.ClusterBinding) {
	phase := string(clusterBinding.Status.Phase)
	if phase == "" {
		phase = "Unknown"
	}
	metrics.ClusterBindingTotal.WithLabelValues(phase).Inc()
}

// reconcileKubeoceanSyncer creates or updates the Kubeocean Syncer for the ClusterBinding
func (r *ClusterBindingReconciler) reconcileKubeoceanSyncer(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding) error {
	log := r.Log.WithValues("clusterbinding", client.ObjectKeyFromObject(clusterBinding))

	// Load the syncer template from mounted files
	templateFiles, err := r.loadSyncerTemplate()
	if err != nil {
		return fmt.Errorf("failed to load syncer template: %w", err)
	}

	// Prepare template data
	templateData := r.prepareSyncerTemplateData(clusterBinding, templateFiles)

	log.V(2).Info("Syncer template data", "templateData", *templateData)

	// Create or update the Syncer Deployment (RBAC resources are shared and deployed with manager)
	if err := r.createSyncerResourceFromTemplate(ctx, clusterBinding, templateFiles, templateData, "deployment.yaml"); err != nil {
		return fmt.Errorf("failed to create or update syncer deployment: %w", err)
	}

	log.Info("Kubeocean Syncer reconciled successfully")
	return nil
}

// loadSyncerTemplate loads the Syncer template from mounted files
func (r *ClusterBindingReconciler) loadSyncerTemplate() (map[string]string, error) {
	// Allow overriding template directory via environment variable for tests
	templateDir := os.Getenv("KUBEOCEAN_SYNCER_TEMPLATE_DIR")
	if templateDir == "" {
		templateDir = "/etc/kubeocean/syncer-template"
	}

	// Read all files in the template directory
	templateData := make(map[string]string)

	// Read basic configuration files
	configFiles := []string{
		"serviceAccountName",
		"roleName",
		"roleBindingName",
		"syncerNamespace",
	}

	for _, filename := range configFiles {
		filePath := fmt.Sprintf("%s/%s", templateDir, filename)
		if content, err := os.ReadFile(filePath); err == nil {
			templateData[filename] = strings.TrimSpace(string(content))
		}
	}

	// Read template files
	templateFiles := []string{
		"deployment.yaml",
	}

	for _, filename := range templateFiles {
		filePath := fmt.Sprintf("%s/%s", templateDir, filename)
		if content, err := os.ReadFile(filePath); err == nil {
			templateData[filename] = string(content)
		} else {
			return nil, fmt.Errorf("failed to read template file %s: %w", filename, err)
		}
	}

	return templateData, nil
}

// prepareSyncerTemplateData prepares the data for rendering Syncer templates
func (r *ClusterBindingReconciler) prepareSyncerTemplateData(clusterBinding *cloudv1beta1.ClusterBinding, templateData map[string]string) *SyncerTemplateData {
	// Get names from template data or use defaults
	serviceAccountName := templateData["serviceAccountName"]
	if serviceAccountName == "" {
		serviceAccountName = DefaultSyncerName
	}

	clusterRoleName := templateData["clusterRoleName"]
	if clusterRoleName == "" {
		clusterRoleName = DefaultSyncerName
	}

	clusterRoleBindingName := templateData["clusterRoleBindingName"]
	if clusterRoleBindingName == "" {
		clusterRoleBindingName = DefaultSyncerName
	}

	// Get syncer namespace from template data or use default
	syncerNamespace := templateData["syncerNamespace"]
	if syncerNamespace == "" {
		syncerNamespace = "kubeocean-system"
	}

	return &SyncerTemplateData{
		ClusterBindingName:     clusterBinding.Name,
		DeploymentName:         r.getSyncerName(clusterBinding),
		ServiceAccountName:     serviceAccountName,
		ClusterRoleName:        clusterRoleName,
		ClusterRoleBindingName: clusterRoleBindingName,
		SyncerNamespace:        syncerNamespace,
	}
}

// createSyncerResourceFromTemplate creates a Kubernetes resource from a template
func (r *ClusterBindingReconciler) createSyncerResourceFromTemplate(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding, templateFiles map[string]string, templateData *SyncerTemplateData, templateKey string) error {
	// Get the template from template files
	templateStr, exists := templateFiles[templateKey]
	if !exists {
		return fmt.Errorf("template %s not found in template files", templateKey)
	}

	// Parse and execute the template
	tmpl, err := template.New(templateKey).Parse(templateStr)
	if err != nil {
		return fmt.Errorf("failed to parse template %s: %w", templateKey, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData); err != nil {
		return fmt.Errorf("failed to execute template %s: %w", templateKey, err)
	}

	// Decode the YAML into a Kubernetes object
	decoder := serializer.NewCodecFactory(r.Scheme).UniversalDeserializer()
	obj, _, err := decoder.Decode(buf.Bytes(), nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode YAML from template %s: %w", templateKey, err)
	}

	// Set owner reference
	if err := r.setOwnerReference(clusterBinding, obj); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	// Create or update the resource
	return r.createOrUpdateResource(ctx, obj)
}

// setOwnerReference sets the owner reference for a Kubernetes object
func (r *ClusterBindingReconciler) setOwnerReference(clusterBinding *cloudv1beta1.ClusterBinding, obj runtime.Object) error {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return fmt.Errorf("object does not implement metav1.Object")
	}

	ownerRef := metav1.OwnerReference{
		APIVersion: clusterBinding.APIVersion,
		Kind:       clusterBinding.Kind,
		Name:       clusterBinding.Name,
		UID:        clusterBinding.UID,
		Controller: ptr.To(true),
	}

	// Fallback: if APIVersion/Kind are empty (common in tests when TypeMeta not set), fill them explicitly
	if ownerRef.APIVersion == "" || ownerRef.Kind == "" {
		ownerRef.APIVersion = cloudv1beta1.GroupVersion.String()
		ownerRef.Kind = "ClusterBinding"
	}

	metaObj.SetOwnerReferences([]metav1.OwnerReference{ownerRef})
	return nil
}

// createOrUpdateResource creates or updates a Kubernetes resource
func (r *ClusterBindingReconciler) createOrUpdateResource(ctx context.Context, obj runtime.Object) error {
	clientObj, ok := obj.(client.Object)
	if !ok {
		return fmt.Errorf("object does not implement client.Object")
	}

	// Try to get the existing resource
	key := client.ObjectKeyFromObject(clientObj)
	existing := obj.DeepCopyObject()
	existingClientObj, ok := existing.(client.Object)
	if !ok {
		return fmt.Errorf("existing object does not implement client.Object")
	}

	err := r.Get(ctx, key, existingClientObj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Resource doesn't exist, create it
			return r.Create(ctx, clientObj)
		}
		return err
	}

	// Resource exists - check if it's a Deployment
	if clientObj.GetObjectKind().GroupVersionKind().Kind == "Deployment" {
		// For Deployments, don't update if it already exists
		r.Log.V(1).Info("Deployment already exists, skipping update",
			"deployment", client.ObjectKeyFromObject(clientObj))
		return nil
	}

	// For other resources, update as before
	clientObj.SetResourceVersion(existingClientObj.GetResourceVersion())
	return r.Update(ctx, clientObj)
}

// getSyncerName returns the name for the Kubeocean Syncer resources
func (r *ClusterBindingReconciler) getSyncerName(clusterBinding *cloudv1beta1.ClusterBinding) string {
	return fmt.Sprintf("kubeocean-syncer-%s", clusterBinding.Name)
}

// getSyncerLabels returns the labels for the Kubeocean Syncer resources
func (r *ClusterBindingReconciler) getSyncerLabels(clusterBinding *cloudv1beta1.ClusterBinding) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":         "kubeocean-syncer",
		"app.kubernetes.io/instance":     clusterBinding.Name,
		"app.kubernetes.io/component":    "syncer",
		"app.kubernetes.io/part-of":      "kubeocean",
		"app.kubernetes.io/managed-by":   "kubeocean-manager",
		cloudv1beta1.LabelClusterBinding: clusterBinding.Name,
	}
}

// handleDeletion handles the deletion of ClusterBinding resource
func (r *ClusterBindingReconciler) handleDeletion(ctx context.Context, originalClusterBinding *cloudv1beta1.ClusterBinding) (ctrl.Result, error) {
	log := r.Log.WithValues("clusterbinding", client.ObjectKeyFromObject(originalClusterBinding))
	log.Info("Handling ClusterBinding deletion")

	// Delete associated Kubeocean Syncer resources with comprehensive cleanup tracking
	cleanupStatus, err := r.deleteSyncerResources(ctx, originalClusterBinding)
	if err != nil {
		log.Error(err, "Failed to delete syncer resources")
		r.Recorder.Event(originalClusterBinding, corev1.EventTypeWarning, "CleanupFailed", fmt.Sprintf("Failed to cleanup syncer resources: %v", err))
		return ctrl.Result{}, err
	}

	// Check if all resources are cleaned up
	if !r.isCleanupComplete(cleanupStatus) {
		log.Info("Resource cleanup still in progress", "status", cleanupStatus)
		r.Recorder.Event(originalClusterBinding, corev1.EventTypeNormal, "CleanupInProgress", "Resource cleanup still in progress")
		return ctrl.Result{}, fmt.Errorf("resource cleanup still in progress: %+v", cleanupStatus)
	}

	log.Info("Syncer Deployment cleaned up successfully, RBAC resources preserved", "status", cleanupStatus)
	r.Recorder.Event(originalClusterBinding, corev1.EventTypeNormal, "Cleanup", "Syncer Deployment cleaned up successfully, RBAC resources preserved")

	// Cleanup auto-managed certificates
	if err := r.cleanupAutoManagedCertificates(ctx, originalClusterBinding); err != nil {
		log.Error(err, "Failed to cleanup auto-managed certificates")
		r.Recorder.Event(originalClusterBinding, corev1.EventTypeWarning, "CertificateCleanupFailed", fmt.Sprintf("Failed to cleanup certificates: %v", err))
		// Continue with deletion even if certificate cleanup fails - this is not critical
		// as orphaned certificates will eventually expire
	}

	// Create a deep copy for modification
	clusterBinding := originalClusterBinding.DeepCopy()

	// Remove the finalizer only after all resources are cleaned up
	r.removeFinalizer(clusterBinding)
	if err := r.Update(ctx, clusterBinding); err != nil {
		log.Error(err, "unable to remove finalizer")
		return ctrl.Result{}, err
	}

	log.Info("ClusterBinding deletion completed successfully")
	r.Recorder.Event(originalClusterBinding, corev1.EventTypeNormal, "Deleted", "ClusterBinding deleted successfully")
	return ctrl.Result{}, nil
}

// deleteSyncerResources deletes only the Deployment created for the Kubeocean Syncer
// RBAC resources (ServiceAccount, Role, RoleBinding) are left intact for reuse
func (r *ClusterBindingReconciler) deleteSyncerResources(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding) (*ResourceCleanupStatus, error) {
	log := r.Log.WithValues("clusterbinding", client.ObjectKeyFromObject(clusterBinding))
	log.Info("Starting syncer resource cleanup (Deployment only)")

	status := &ResourceCleanupStatus{}

	// Load the syncer template from mounted files to get resource names
	templateFiles, err := r.loadSyncerTemplate()
	var templateData *SyncerTemplateData

	if err != nil {
		// If template files are not found, use default names for cleanup
		log.V(1).Info("Template files not found, using default names for cleanup", "error", err)
		templateData = r.prepareSyncerTemplateDataWithDefaults(clusterBinding)
	} else {
		templateData = r.prepareSyncerTemplateData(clusterBinding, templateFiles)
	}

	// Delete only the Deployment
	status.Deployment, err = r.deleteResourceWithFallback(ctx, clusterBinding, "Deployment",
		func(name, namespace string) client.Object {
			return &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			}
		}, templateData.DeploymentName, templateData.SyncerNamespace)
	if err != nil {
		return status, fmt.Errorf("failed to delete Deployment: %w", err)
	}

	// Mark RBAC resources as cleaned up (but don't actually delete them)
	// This allows the cleanup completion check to pass
	status.ServiceAccount = true
	status.ClusterRole = true
	status.ClusterRoleBinding = true

	log.Info("Syncer resource cleanup completed (Deployment deleted, RBAC resources preserved)", "status", status)
	return status, nil
}

// deleteResourceWithFallback attempts to delete a resource by configured name, then by default names
func (r *ClusterBindingReconciler) deleteResourceWithFallback(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding,
	resourceType string, createResource func(string, string) client.Object, configuredName, namespace string) (bool, error) {

	log := r.Log.WithValues("clusterbinding", client.ObjectKeyFromObject(clusterBinding), "resourceType", resourceType)

	// Try configured name first
	log.V(1).Info("Checking if resource exists with configured name", "name", configuredName)
	if deleted, found, err := r.findAndDeleteResource(ctx, createResource(configuredName, namespace), resourceType, configuredName); found {
		if err != nil {
			return false, fmt.Errorf("failed to delete %s with configured name %s: %w", resourceType, configuredName, err)
		}
		if deleted {
			log.Info("Successfully deleted resource with configured name", "name", configuredName)
		}
		return deleted, nil
	}

	// Try default pattern: kubeocean-syncer-{clusterbinding-name}
	defaultName := fmt.Sprintf("kubeocean-syncer-%s", clusterBinding.Name)
	if defaultName != configuredName {
		log.V(1).Info("Checking if resource exists with default pattern name", "name", defaultName)
		if deleted, found, err := r.findAndDeleteResource(ctx, createResource(defaultName, namespace), resourceType, defaultName); found {
			if err != nil {
				return false, fmt.Errorf("failed to delete %s with default pattern name %s: %w", resourceType, defaultName, err)
			}
			if deleted {
				log.Info("Successfully deleted resource with default pattern name", "name", defaultName)
			}
			return deleted, nil
		}
	} else {
		log.V(1).Info("Skipping default pattern name as it's same as configured name", "name", defaultName)
	}

	// Try base default name: kubeocean-syncer
	baseName := "kubeocean-syncer"
	if baseName != configuredName && baseName != defaultName {
		log.V(1).Info("Checking if resource exists with base default name", "name", baseName)
		if deleted, found, err := r.findAndDeleteResource(ctx, createResource(baseName, namespace), resourceType, baseName); found {
			if err != nil {
				return false, fmt.Errorf("failed to delete %s with base default name %s: %w", resourceType, baseName, err)
			}
			if deleted {
				log.Info("Successfully deleted resource with base default name", "name", baseName)
			}
			return deleted, nil
		}
	} else {
		log.V(1).Info("Skipping base default name", "baseName", baseName, "configuredName", configuredName, "defaultName", defaultName)
	}

	log.Info("Resource not found with any naming pattern, considering as cleaned up", "configuredName", configuredName, "defaultName", defaultName, "baseName", baseName)
	return true, nil
}

// findAndDeleteResource first checks if a resource exists, then deletes it if found
// Returns (deleted, found, error) where:
// - deleted: true if resource was successfully deleted, false if deletion failed
// - found: true if resource was found (regardless of deletion success), false if not found
// - error: non-nil if there was an error during deletion (but not if resource wasn't found)
func (r *ClusterBindingReconciler) findAndDeleteResource(ctx context.Context, resource client.Object, resourceType, name string) (bool, bool, error) {
	log := r.Log.WithValues("resourceType", resourceType, "name", name, "namespace", resource.GetNamespace())

	// First, check if the resource exists
	key := client.ObjectKeyFromObject(resource)
	err := r.Get(ctx, key, resource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("Resource not found")
			return false, false, nil // not deleted, not found, no error
		}
		log.Error(err, "Failed to get resource")
		return false, true, err // not deleted, found (get failed for other reason), return error
	}

	// Resource exists, now delete it
	log.V(1).Info("Resource found, attempting to delete")
	err = r.Delete(ctx, resource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Resource was deleted between Get and Delete calls
			log.V(1).Info("Resource was already deleted")
			return true, true, nil // deleted (by someone else), found, no error
		}
		log.Error(err, "Failed to delete resource")
		return false, true, err // not deleted, found, return error
	}

	log.Info("Successfully deleted resource")
	return true, true, nil // deleted, found, no error
}

// prepareSyncerTemplateDataWithDefaults prepares template data using default values
func (r *ClusterBindingReconciler) prepareSyncerTemplateDataWithDefaults(clusterBinding *cloudv1beta1.ClusterBinding) *SyncerTemplateData {
	return &SyncerTemplateData{
		ClusterBindingName:     clusterBinding.Name,
		DeploymentName:         r.getSyncerName(clusterBinding),
		ServiceAccountName:     DefaultSyncerName,
		ClusterRoleName:        DefaultSyncerName,
		ClusterRoleBindingName: DefaultSyncerName,
		SyncerNamespace:        "kubeocean-system",
	}
}

// isCleanupComplete checks if all resources have been successfully cleaned up
func (r *ClusterBindingReconciler) isCleanupComplete(status *ResourceCleanupStatus) bool {
	return status.Deployment && status.ServiceAccount && status.ClusterRole && status.ClusterRoleBinding
}

// hasFinalizer checks if the ClusterBinding has the finalizer
func (r *ClusterBindingReconciler) hasFinalizer(clusterBinding *cloudv1beta1.ClusterBinding) bool {
	for _, finalizer := range clusterBinding.Finalizers {
		if finalizer == ClusterBindingFinalizer {
			return true
		}
	}
	return false
}

// addFinalizer adds the finalizer to the ClusterBinding
func (r *ClusterBindingReconciler) addFinalizer(clusterBinding *cloudv1beta1.ClusterBinding) {
	clusterBinding.Finalizers = append(clusterBinding.Finalizers, ClusterBindingFinalizer)
}

// removeFinalizer removes the finalizer from the ClusterBinding
func (r *ClusterBindingReconciler) removeFinalizer(clusterBinding *cloudv1beta1.ClusterBinding) {
	var finalizers []string
	for _, finalizer := range clusterBinding.Finalizers {
		if finalizer != ClusterBindingFinalizer {
			finalizers = append(finalizers, finalizer)
		}
	}
	clusterBinding.Finalizers = finalizers
}

// reconcileKubeoceanProxier creates or updates the Kubeocean Proxier for the ClusterBinding
func (r *ClusterBindingReconciler) reconcileKubeoceanProxier(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding) error {
	log := r.Log.WithValues("clusterbinding", client.ObjectKeyFromObject(clusterBinding))

	// Load the proxier template from mounted files
	templateFiles, err := r.loadProxierTemplate()
	if err != nil {
		return fmt.Errorf("failed to load proxier template: %w", err)
	}

	// Prepare template data
	templateData := r.prepareProxierTemplateData(clusterBinding, templateFiles)

	log.V(2).Info("Proxier template data", "templateData", *templateData)

	// Create or update the Proxier Deployment
	if err := r.createProxierResourceFromTemplate(ctx, clusterBinding, templateFiles, templateData, "deployment.yaml"); err != nil {
		return fmt.Errorf("failed to create or update proxier deployment: %w", err)
	}

	// Create or update the Proxier Service
	if err := r.createProxierResourceFromTemplate(ctx, clusterBinding, templateFiles, templateData, "service.yaml"); err != nil {
		return fmt.Errorf("failed to create or update proxier service: %w", err)
	}

	log.Info("Kubeocean Proxier reconciled successfully")
	return nil
}

// loadProxierTemplate loads the Proxier template from mounted files
func (r *ClusterBindingReconciler) loadProxierTemplate() (map[string]string, error) {
	// Allow overriding template directory via environment variable for tests
	templateDir := os.Getenv("KUBEOCEAN_PROXIER_TEMPLATE_DIR")
	if templateDir == "" {
		templateDir = "/etc/kubeocean/proxier-template"
	}

	// Read all files in the template directory
	templateData := make(map[string]string)

	// Read basic configuration files
	configFiles := []string{
		"serviceAccountName",
		"clusterRoleName",
		"clusterRoleBindingName",
		"proxierNamespace",
	}

	for _, filename := range configFiles {
		filePath := fmt.Sprintf("%s/%s", templateDir, filename)
		if content, err := os.ReadFile(filePath); err == nil {
			templateData[filename] = strings.TrimSpace(string(content))
		}
	}

	// Read template files
	templateFiles := []string{
		"deployment.yaml",
		"service.yaml",
	}

	for _, filename := range templateFiles {
		filePath := fmt.Sprintf("%s/%s", templateDir, filename)
		if content, err := os.ReadFile(filePath); err == nil {
			templateData[filename] = string(content)
		} else {
			return nil, fmt.Errorf("failed to read template file %s: %w", filename, err)
		}
	}

	return templateData, nil
}

// prepareProxierTemplateData prepares the data for rendering Proxier templates
func (r *ClusterBindingReconciler) prepareProxierTemplateData(clusterBinding *cloudv1beta1.ClusterBinding, templateFiles map[string]string) *ProxierTemplateData {
	// Generate deployment name based on ClusterBinding name
	deploymentName := fmt.Sprintf("%s-%s", DefaultProxierName, clusterBinding.Spec.ClusterID)
	serviceName := fmt.Sprintf("%s-%s-svc", DefaultProxierName, clusterBinding.Spec.ClusterID)

	// Read TLS configuration from ClusterBinding annotations
	var tlsEnabled bool
	var tlsSecretName, tlsSecretNamespace string

	if annotations := clusterBinding.GetAnnotations(); annotations != nil {
		// Check if logs proxy is enabled
		if enabled, exists := annotations["kubeocean.io/logs-proxy-enabled"]; exists {
			tlsEnabled = enabled == "true"
		}

		// Get TLS secret information
		if tlsEnabled {
			if name, exists := annotations["kubeocean.io/logs-proxy-secret-name"]; exists {
				tlsSecretName = name
			}
			if namespace, exists := annotations["kubeocean.io/logs-proxy-secret-namespace"]; exists {
				tlsSecretNamespace = namespace
			} else {
				// Default to proxier namespace if not specified
				tlsSecretNamespace = templateFiles["proxierNamespace"]
			}
		}
	}

	return &ProxierTemplateData{
		ClusterBindingName:     clusterBinding.Name,
		DeploymentName:         deploymentName,
		ServiceAccountName:     templateFiles["serviceAccountName"],
		ServiceName:            serviceName,
		ClusterRoleName:        templateFiles["clusterRoleName"],
		ClusterRoleBindingName: templateFiles["clusterRoleBindingName"],
		ProxierNamespace:       templateFiles["proxierNamespace"],
		TLSEnabled:             tlsEnabled,
		TLSSecretName:          tlsSecretName,
		TLSSecretNamespace:     tlsSecretNamespace,
	}
}

// createProxierResourceFromTemplate creates or updates a Kubernetes resource from a template
func (r *ClusterBindingReconciler) createProxierResourceFromTemplate(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding, templateFiles map[string]string, templateData *ProxierTemplateData, templateName string) error {

	// Get template content
	templateContent, exists := templateFiles[templateName]
	if !exists {
		return fmt.Errorf("template %s not found", templateName)
	}

	// Parse and execute template
	tmpl, err := template.New(templateName).Parse(templateContent)
	if err != nil {
		return fmt.Errorf("failed to parse template %s: %w", templateName, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData); err != nil {
		return fmt.Errorf("failed to execute template %s: %w", templateName, err)
	}

	// Decode the YAML into a Kubernetes object
	decoder := serializer.NewCodecFactory(r.Scheme).UniversalDeserializer()
	obj, _, err := decoder.Decode(buf.Bytes(), nil, nil)
	if err != nil {
		return fmt.Errorf("failed to decode YAML from template %s: %w", templateName, err)
	}

	// Set owner reference
	if err := r.setOwnerReference(clusterBinding, obj); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	// Create or update the resource
	return r.createOrUpdateResource(ctx, obj)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return r.SetupWithManagerAndName(mgr, "clusterbinding")
}

// SetupWithManagerAndName sets up the controller with the Manager using a custom name.
func (r *ClusterBindingReconciler) SetupWithManagerAndName(mgr ctrl.Manager, controllerName string) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudv1beta1.ClusterBinding{}).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				newobj, ok1 := e.ObjectNew.(*cloudv1beta1.ClusterBinding)
				oldobj, ok2 := e.ObjectOld.(*cloudv1beta1.ClusterBinding)
				if !ok1 || !ok2 {
					return false
				}
				if reflect.DeepEqual(newobj.Spec, oldobj.Spec) && reflect.DeepEqual(newobj.Finalizers, oldobj.Finalizers) &&
					newobj.Status.Phase == oldobj.Status.Phase && reflect.DeepEqual(newobj.DeletionTimestamp, oldobj.DeletionTimestamp) {
					return false
				}
				return true
			},
		}).
		Complete(r)
}

// cleanupAutoManagedCertificates cleans up auto-managed certificates when ClusterBinding is deleted
func (r *ClusterBindingReconciler) cleanupAutoManagedCertificates(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding) error {
	log := r.Log.WithValues("clusterbinding", client.ObjectKeyFromObject(clusterBinding))
	log.Info("Starting cleanup of auto-managed certificates")

	// Create Kubernetes clientset for certificate operations
	config := ctrl.GetConfigOrDie()
	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Create certificate manager
	certManager := proxier.NewCertificateManager(
		k8sClient,
		clusterBinding,
		"kubeocean-system", // Default namespace where certificates are stored
		log.WithName("cert-cleanup"),
	)

	// Perform complete cleanup
	return certManager.ForceCleanupAll(ctx)
}
