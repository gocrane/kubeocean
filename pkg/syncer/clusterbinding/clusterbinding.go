package clusterbinding

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	"github.com/TKEColocation/kubeocean/pkg/syncer/bottomup"
	topcommon "github.com/TKEColocation/kubeocean/pkg/syncer/topdown/common"
)

// BottomUpSyncerInterface defines the interface for BottomUpSyncer operations needed by ClusterBindingReconciler
type BottomUpSyncerInterface interface {
	GetNodesMatchingSelector(ctx context.Context, selector *corev1.NodeSelector) ([]string, error)
	RequeueNodes(nodeNames []string) error
	GetClusterBinding() *cloudv1beta1.ClusterBinding
	SetClusterBinding(binding *cloudv1beta1.ClusterBinding)
}

// BottomUpSyncerAdapter wraps the concrete BottomUpSyncer to implement the interface
type BottomUpSyncerAdapter struct {
	syncer *bottomup.BottomUpSyncer
}

// NewBottomUpSyncerAdapter creates a new adapter for the concrete BottomUpSyncer
func NewBottomUpSyncerAdapter(syncer *bottomup.BottomUpSyncer) *BottomUpSyncerAdapter {
	return &BottomUpSyncerAdapter{syncer: syncer}
}

func (a *BottomUpSyncerAdapter) GetNodesMatchingSelector(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
	return a.syncer.GetNodesMatchingSelector(ctx, selector)
}

func (a *BottomUpSyncerAdapter) RequeueNodes(nodeNames []string) error {
	return a.syncer.RequeueNodes(nodeNames)
}

func (a *BottomUpSyncerAdapter) GetClusterBinding() *cloudv1beta1.ClusterBinding {
	return a.syncer.ClusterBinding
}

func (a *BottomUpSyncerAdapter) SetClusterBinding(binding *cloudv1beta1.ClusterBinding) {
	a.syncer.ClusterBinding = binding
}

// ClusterBindingReconciler reconciles ClusterBinding objects for this specific KubeoceanSyncer
type ClusterBindingReconciler struct {
	client.Client
	Log                     logr.Logger
	ClusterBindingName      string
	ClusterBindingNamespace string
	BottomUpSyncer          BottomUpSyncerInterface
	PhysicalClient          client.Client
}

//+kubebuilder:rbac:groups=cloud.tencent.com,resources=clusterbindings,verbs=get;list;watch
//+kubebuilder:rbac:groups=cloud.tencent.com,resources=clusterbindings/status,verbs=get

// Reconcile handles ClusterBinding events for this specific binding
func (r *ClusterBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("clusterbinding", req.NamespacedName)

	// Only handle our specific ClusterBinding
	if req.Name != r.ClusterBindingName {
		return ctrl.Result{}, nil
	}

	logger.Info("ClusterBinding event received")

	// Get the ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{}
	err := r.Get(ctx, req.NamespacedName, clusterBinding)
	if err != nil {
		if errors.IsNotFound(err) {
			// ClusterBinding 不存在，不做任何事
			logger.Info("ClusterBinding not found, doing nothing")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get ClusterBinding")
		return ctrl.Result{}, err
	}

	// 检查是否正在删除
	if clusterBinding.DeletionTimestamp != nil {
		if !controllerutil.ContainsFinalizer(clusterBinding, cloudv1beta1.ClusterBindingSyncerFinalizer) {
			// finalizer 不存在，不做任何事
			logger.Info("ClusterBinding is being deleted but finalizer not found, doing nothing")
			return r.handleClusterBindingDeletion(ctx, clusterBinding)
		}
		// 进入删除处理逻辑
		logger.Info("ClusterBinding is being deleted with finalizer, handling deletion")
		return r.handleClusterBindingDeletion(ctx, clusterBinding)
	}

	// 添加 finalizer，如果失败则重新入队重试
	if !controllerutil.ContainsFinalizer(clusterBinding, cloudv1beta1.ClusterBindingSyncerFinalizer) {
		clusterBindingCopy := clusterBinding.DeepCopy()
		controllerutil.AddFinalizer(clusterBindingCopy, cloudv1beta1.ClusterBindingSyncerFinalizer)
		if err := r.Update(ctx, clusterBindingCopy); err != nil {
			logger.Error(err, "Failed to add finalizer to ClusterBinding")
			return ctrl.Result{Requeue: true}, err
		}
		logger.Info("Added finalizer to ClusterBinding")
		clusterBinding = clusterBindingCopy
	}

	// Check if nodeSelector changed
	if r.hasNodeSelectorChanged(clusterBinding) {
		logger.Info("ClusterBinding nodeSelector changed, triggering node re-evaluation")
		return r.handleNodeSelectorChange(ctx, clusterBinding)
	}

	// Check if disableNodeDefaultTaint changed
	if r.hasDisableNodeDefaultTaintChanged(clusterBinding) {
		logger.Info("ClusterBinding disableNodeDefaultTaint changed, triggering node re-evaluation")
		return r.handleDisableNodeDefaultTaintChange(ctx, clusterBinding)
	}

	return ctrl.Result{}, nil
}

// hasNodeSelectorChanged checks if the nodeSelector has changed
func (r *ClusterBindingReconciler) hasNodeSelectorChanged(newBinding *cloudv1beta1.ClusterBinding) bool {
	if r.BottomUpSyncer == nil {
		return true // First time loading
	}

	oldBinding := r.BottomUpSyncer.GetClusterBinding()
	if oldBinding == nil {
		return true // First time loading
	}

	oldSelector := oldBinding.Spec.NodeSelector
	newSelector := newBinding.Spec.NodeSelector

	return !reflect.DeepEqual(oldSelector, newSelector)
}

// hasDisableNodeDefaultTaintChanged checks if the disableNodeDefaultTaint has changed
func (r *ClusterBindingReconciler) hasDisableNodeDefaultTaintChanged(newBinding *cloudv1beta1.ClusterBinding) bool {
	if r.BottomUpSyncer == nil {
		return true // First time loading
	}

	oldBinding := r.BottomUpSyncer.GetClusterBinding()
	if oldBinding == nil {
		return true // First time loading
	}

	oldValue := oldBinding.Spec.DisableNodeDefaultTaint
	newValue := newBinding.Spec.DisableNodeDefaultTaint

	return oldValue != newValue
}

// handleClusterBindingDeletion handles ClusterBinding deletion
func (r *ClusterBindingReconciler) handleClusterBindingDeletion(ctx context.Context, clusterBinding *cloudv1beta1.ClusterBinding) (ctrl.Result, error) {
	logger := r.Log.WithValues("clusterbinding", clusterBinding.Name)
	logger.Info("Handling ClusterBinding deletion")

	// 根据 LabelClusterBinding 在虚拟集群中查询所有 clusterbinding 关联的虚拟节点
	virtualNodes, err := r.getVirtualNodesByClusterBinding(ctx, r.ClusterBindingName)
	if err != nil {
		logger.Error(err, "Failed to get virtual nodes by cluster binding")
		return ctrl.Result{}, err
	}

	if len(virtualNodes) > 0 {
		// 存在虚拟节点，查询所有虚拟节点对应的物理节点，然后调用 bottomUpSyncer 的 RequeueNodes
		physicalNodeNames := r.getPhysicalNodesFromVirtualNodes(ctx, virtualNodes)

		if len(physicalNodeNames) > 0 {
			logger.Info("Requeuing physical nodes for cleanup", "nodes", physicalNodeNames)
			if err := r.BottomUpSyncer.RequeueNodes(physicalNodeNames); err != nil {
				logger.Error(err, "Failed to requeue nodes")
				return ctrl.Result{}, err
			}
		}

		virtualNames := make([]string, 0, len(virtualNodes))
		for _, virtualNode := range virtualNodes {
			virtualNames = append(virtualNames, virtualNode.Name)
		}

		logger.Info("Virtual nodes still exist, requeuing after 5 seconds", "count", len(virtualNodes), "nodes", virtualNames)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 继续根据 managedByClusterIDLabel 在虚拟集群中查找 configmap、secret、pv、pvc，并添加删除注解
	managedByClusterIDLabel := topcommon.GetManagedByClusterIDLabel(clusterBinding.Spec.ClusterID)
	resourcesNames, err := r.checkAndCleanVirtualResources(ctx, managedByClusterIDLabel, clusterBinding)
	if err != nil {
		logger.Error(err, "Failed to check and clean virtual resources")
		return ctrl.Result{}, err
	}

	if len(resourcesNames) > 0 {
		logger.Error(fmt.Errorf("virtual resources still exist"), "Virtual resources still exist, cannot complete deletion", "resources", resourcesNames)
		return ctrl.Result{}, fmt.Errorf("virtual resources still exist, cannot complete deletion, resources: %s", resourcesNames)
	}

	// 不存在虚拟节点和虚拟资源，检查物理集群中是否有 kubeocean 管理的资源
	physicalResourcesNames, err := r.checkPhysicalResourcesExist(ctx)
	if err != nil {
		logger.Error(err, "Failed to check physical resources")
		return ctrl.Result{}, err
	}

	if len(physicalResourcesNames) > 0 {
		logger.Error(fmt.Errorf("physical resources still exist"), "Physical resources still exist, cannot complete deletion", "resources", physicalResourcesNames)
		return ctrl.Result{}, fmt.Errorf("physical resources still exist, cannot complete deletion, resources: %s", physicalResourcesNames)
	}

	if !controllerutil.ContainsFinalizer(clusterBinding, cloudv1beta1.ClusterBindingSyncerFinalizer) {
		logger.Info("ClusterBinding is being deleted but finalizer not found, doing nothing")
		return ctrl.Result{}, nil
	}

	// 移除 finalizer
	controllerutil.RemoveFinalizer(clusterBinding, cloudv1beta1.ClusterBindingSyncerFinalizer)
	if err := r.Update(ctx, clusterBinding); err != nil {
		logger.Error(err, "Failed to remove finalizer from ClusterBinding")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully completed ClusterBinding deletion")
	return ctrl.Result{}, nil
}

// handleNodeSelectorChange handles nodeSelector changes
func (r *ClusterBindingReconciler) handleNodeSelectorChange(ctx context.Context, newBinding *cloudv1beta1.ClusterBinding) (ctrl.Result, error) {
	r.Log.Info("Handling nodeSelector change")

	// Get old and new nodeSelectors
	var oldSelector *corev1.NodeSelector
	if r.BottomUpSyncer != nil {
		oldBinding := r.BottomUpSyncer.GetClusterBinding()
		if oldBinding != nil {
			oldSelector = oldBinding.Spec.NodeSelector
		}
	}
	newSelector := newBinding.Spec.NodeSelector

	// Get nodes that match old selector
	var oldNodes []string
	var err error
	if oldSelector != nil && len(oldSelector.NodeSelectorTerms) > 0 {
		oldNodes, err = r.getNodesMatchingSelector(ctx, oldSelector)
		if err != nil {
			r.Log.Error(err, "Failed to get nodes matching old selector")
			return ctrl.Result{}, err
		}
	}

	// Get nodes that match new selector
	newNodes, err := r.getNodesMatchingSelector(ctx, newSelector)
	if err != nil {
		r.Log.Error(err, "Failed to get nodes matching new selector")
		return ctrl.Result{}, err
	}

	// Combine and deduplicate node names (union of old and new)
	affectedNodes := r.unionAndDeduplicateNodes(oldNodes, newNodes)

	r.Log.Info("Found affected nodes", "count", len(affectedNodes), "nodes", affectedNodes)

	// Update the cached ClusterBinding
	if r.BottomUpSyncer != nil {
		r.BottomUpSyncer.SetClusterBinding(newBinding)
	}

	// Trigger node re-evaluation in bottomUpSyncer
	if r.BottomUpSyncer != nil && len(affectedNodes) > 0 {
		r.Log.Info("Triggering node re-evaluation due to nodeSelector change", "affectedNodes", affectedNodes)
		if err := r.BottomUpSyncer.RequeueNodes(affectedNodes); err != nil {
			r.Log.Error(err, "Failed to requeue nodes")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// handleDisableNodeDefaultTaintChange handles disableNodeDefaultTaint changes
func (r *ClusterBindingReconciler) handleDisableNodeDefaultTaintChange(ctx context.Context, newBinding *cloudv1beta1.ClusterBinding) (ctrl.Result, error) {
	r.Log.Info("Handling disableNodeDefaultTaint change")

	// Get nodes that match the current nodeSelector
	var affectedNodes []string
	var err error
	if newBinding.Spec.NodeSelector != nil && len(newBinding.Spec.NodeSelector.NodeSelectorTerms) > 0 {
		affectedNodes, err = r.getNodesMatchingSelector(ctx, newBinding.Spec.NodeSelector)
		if err != nil {
			r.Log.Error(err, "Failed to get nodes matching selector")
			return ctrl.Result{}, err
		}
	}

	r.Log.Info("Found affected nodes for disableNodeDefaultTaint change", "count", len(affectedNodes), "nodes", affectedNodes)

	// Update the cached ClusterBinding
	if r.BottomUpSyncer != nil {
		r.BottomUpSyncer.SetClusterBinding(newBinding)
	}

	// Trigger node re-evaluation in bottomUpSyncer
	if r.BottomUpSyncer != nil && len(affectedNodes) > 0 {
		r.Log.Info("Triggering node re-evaluation due to disableNodeDefaultTaint change", "affectedNodes", affectedNodes)
		if err := r.BottomUpSyncer.RequeueNodes(affectedNodes); err != nil {
			r.Log.Error(err, "Failed to requeue nodes")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// getNodesMatchingSelector gets all nodes that match the given selector from physical cluster
func (r *ClusterBindingReconciler) getNodesMatchingSelector(ctx context.Context, selector *corev1.NodeSelector) ([]string, error) {
	r.Log.Info("Getting nodes matching selector", "selector", selector)

	if r.BottomUpSyncer == nil {
		return nil, fmt.Errorf("BottomUpSyncer not available")
	}

	// Use BottomUpSyncer to get nodes from physical cluster
	return r.BottomUpSyncer.GetNodesMatchingSelector(ctx, selector)
}

// unionAndDeduplicateNodes combines two slices of node names and removes duplicates
func (r *ClusterBindingReconciler) unionAndDeduplicateNodes(oldNodes, newNodes []string) []string {
	// Use map to track unique nodes
	nodeSet := make(map[string]bool)

	// Add old nodes
	for _, node := range oldNodes {
		nodeSet[node] = true
	}

	// Add new nodes
	for _, node := range newNodes {
		nodeSet[node] = true
	}

	// Convert back to slice
	result := make([]string, 0, len(nodeSet))
	for node := range nodeSet {
		result = append(result, node)
	}

	return result
}

// getVirtualNodesByClusterBinding gets virtual nodes by cluster binding name from virtual cluster
func (r *ClusterBindingReconciler) getVirtualNodesByClusterBinding(ctx context.Context, clusterBindingName string) ([]corev1.Node, error) {
	var nodeList corev1.NodeList
	labelSelector := labels.SelectorFromSet(map[string]string{cloudv1beta1.LabelClusterBinding: clusterBindingName})

	err := r.List(ctx, &nodeList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("failed to list virtual nodes by cluster binding: %w", err)
	}

	return nodeList.Items, nil
}

// getPhysicalNodesFromVirtualNodes extracts physical node names from virtual nodes
func (r *ClusterBindingReconciler) getPhysicalNodesFromVirtualNodes(_ context.Context, virtualNodes []corev1.Node) []string {
	var physicalNodeNames []string

	for _, virtualNode := range virtualNodes {
		if physicalNodeName, exists := virtualNode.Labels[cloudv1beta1.LabelPhysicalNodeName]; exists {
			physicalNodeNames = append(physicalNodeNames, physicalNodeName)
		}
	}

	return physicalNodeNames
}

// checkAndCleanVirtualResources checks virtual resources and adds clusterbinding-deleting annotation
func (r *ClusterBindingReconciler) checkAndCleanVirtualResources(ctx context.Context, labelKey string, clusterBinding *cloudv1beta1.ClusterBinding) (string, error) {
	labelSelector := labels.SelectorFromSet(map[string]string{labelKey: cloudv1beta1.LabelValueTrue})

	var allResourceNames []string

	// Check and clean fake pods for hostport
	fakePodNames, err := r.checkAndCleanFakePods(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to check and clean fake pods: %w", err)
	}
	if len(fakePodNames) > 0 {
		r.Log.Info("Found fake pods for hostport", "count", len(fakePodNames))
		allResourceNames = append(allResourceNames, fmt.Sprintf("fake pods: [%s]", strings.Join(fakePodNames, ", ")))
	}

	// Check and clean ConfigMaps
	var configMapList corev1.ConfigMapList
	err = r.List(ctx, &configMapList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list configmaps: %w", err)
	}

	configMapNames := make([]string, 0, len(configMapList.Items))
	for i := range configMapList.Items {
		configMap := &configMapList.Items[i]
		configMapNames = append(configMapNames, configMap.Name)

		// Add clusterbinding-deleting annotation using common function
		if err := topcommon.AddClusterBindingDeletingAnnotation(ctx, configMap, r.Client, r.Log, clusterBinding.Spec.ClusterID, clusterBinding.Name); err != nil {
			return "", fmt.Errorf("failed to update ConfigMap %s with clusterbinding-deleting annotation: %w", configMap.Name, err)
		}
	}
	if len(configMapNames) > 0 {
		r.Log.Info("Found virtual configmaps", "count", len(configMapNames))
		allResourceNames = append(allResourceNames, fmt.Sprintf("configmaps: [%s]", strings.Join(configMapNames, ", ")))
	}

	// Check and clean Secrets
	var secretList corev1.SecretList
	err = r.List(ctx, &secretList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list secrets: %w", err)
	}

	secretNames := make([]string, 0, len(secretList.Items))
	for i := range secretList.Items {
		secret := &secretList.Items[i]
		secretNames = append(secretNames, secret.Name)

		// Add clusterbinding-deleting annotation using common function
		if err := topcommon.AddClusterBindingDeletingAnnotation(ctx, secret, r.Client, r.Log, clusterBinding.Spec.ClusterID, clusterBinding.Name); err != nil {
			return "", fmt.Errorf("failed to update Secret %s with clusterbinding-deleting annotation: %w", secret.Name, err)
		}
	}
	if len(secretNames) > 0 {
		r.Log.Info("Found virtual secrets", "count", len(secretNames))
		allResourceNames = append(allResourceNames, fmt.Sprintf("secrets: [%s]", strings.Join(secretNames, ", ")))
	}

	// Check and clean PVs
	var pvList corev1.PersistentVolumeList
	err = r.List(ctx, &pvList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list persistent volumes: %w", err)
	}

	pvNames := make([]string, 0, len(pvList.Items))
	for i := range pvList.Items {
		pv := &pvList.Items[i]
		pvNames = append(pvNames, pv.Name)

		// Add clusterbinding-deleting annotation using common function
		if err := topcommon.AddClusterBindingDeletingAnnotation(ctx, pv, r.Client, r.Log, clusterBinding.Spec.ClusterID, clusterBinding.Name); err != nil {
			return "", fmt.Errorf("failed to update PV %s with clusterbinding-deleting annotation: %w", pv.Name, err)
		}
	}
	if len(pvNames) > 0 {
		r.Log.Info("Found virtual persistent volumes", "count", len(pvNames))
		allResourceNames = append(allResourceNames, fmt.Sprintf("persistent volumes: [%s]", strings.Join(pvNames, ", ")))
	}

	// Check and clean PVCs
	var pvcList corev1.PersistentVolumeClaimList
	err = r.List(ctx, &pvcList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list persistent volume claims: %w", err)
	}

	pvcNames := make([]string, 0, len(pvcList.Items))
	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		pvcNames = append(pvcNames, pvc.Name)

		// Add clusterbinding-deleting annotation using common function
		if err := topcommon.AddClusterBindingDeletingAnnotation(ctx, pvc, r.Client, r.Log, clusterBinding.Spec.ClusterID, clusterBinding.Name); err != nil {
			return "", fmt.Errorf("failed to update PVC %s with clusterbinding-deleting annotation: %w", pvc.Name, err)
		}
	}
	if len(pvcNames) > 0 {
		r.Log.Info("Found virtual persistent volume claims", "count", len(pvcNames))
		allResourceNames = append(allResourceNames, fmt.Sprintf("persistent volume claims: [%s]", strings.Join(pvcNames, ", ")))
	}

	// Return combined resource names
	if len(allResourceNames) > 0 {
		return strings.Join(allResourceNames, "; "), nil
	}

	return "", nil
}

// checkAndCleanFakePods checks and cleans fake pods for hostport in virtual cluster
func (r *ClusterBindingReconciler) checkAndCleanFakePods(ctx context.Context) ([]string, error) {
	// List all fake pods for hostport with kubeocean labels
	var podList corev1.PodList
	labelSelector := labels.SelectorFromSet(map[string]string{
		cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
		cloudv1beta1.LabelManagedBy:       cloudv1beta1.LabelManagedByValue,
	})

	err := r.List(ctx, &podList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("failed to list fake pods for hostport: %w", err)
	}

	fakePodNames := make([]string, 0, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		fakePodNames = append(fakePodNames, pod.Name)

		r.Log.Info("Deleting fake pod for hostport", "pod", pod.Name, "namespace", pod.Namespace)

		// Delete fake pod with grace period 0 for immediate deletion
		if err := r.Delete(ctx, pod, &client.DeleteOptions{
			GracePeriodSeconds: &[]int64{0}[0],
		}); err != nil && !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to delete fake pod %s: %w", pod.Name, err)
		}
	}

	return fakePodNames, nil
}

// checkPhysicalResourcesExist checks if any kubeocean-managed resources exist in physical cluster
func (r *ClusterBindingReconciler) checkPhysicalResourcesExist(ctx context.Context) (string, error) {
	if r.PhysicalClient == nil {
		return "", fmt.Errorf("physical client not available")
	}

	labelSelector := labels.SelectorFromSet(map[string]string{
		cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
	})

	// Check Pods
	var podList corev1.PodList
	err := r.PhysicalClient.List(ctx, &podList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}
	physicalPodNames := make([]string, 0, len(podList.Items))
	for _, pod := range podList.Items {
		physicalPodNames = append(physicalPodNames, pod.Name)
	}
	if len(podList.Items) > 0 {
		r.Log.Info("Found kubeocean-managed pods in physical cluster", "count", len(podList.Items))
		return fmt.Sprintf("pods: [%s]", strings.Join(physicalPodNames, ", ")), nil
	}

	// Check ConfigMaps
	var configMapList corev1.ConfigMapList
	err = r.PhysicalClient.List(ctx, &configMapList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list configmaps: %w", err)
	}
	physicalConfigMapNames := make([]string, 0, len(configMapList.Items))
	for _, configMap := range configMapList.Items {
		physicalConfigMapNames = append(physicalConfigMapNames, configMap.Name)
	}
	if len(configMapList.Items) > 0 {
		r.Log.Info("Found kubeocean-managed configmaps in physical cluster", "count", len(configMapList.Items))
		return fmt.Sprintf("configmaps: [%s]", strings.Join(physicalConfigMapNames, ", ")), nil
	}

	// Check Secrets
	var secretList corev1.SecretList
	err = r.PhysicalClient.List(ctx, &secretList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list secrets: %w", err)
	}
	physicalSecretNames := make([]string, 0, len(secretList.Items))
	for _, secret := range secretList.Items {
		physicalSecretNames = append(physicalSecretNames, secret.Name)
	}
	if len(secretList.Items) > 0 {
		r.Log.Info("Found kubeocean-managed secrets in physical cluster", "count", len(secretList.Items))
		return fmt.Sprintf("secrets: [%s]", strings.Join(physicalSecretNames, ", ")), nil
	}

	// Check PVCs
	var pvcList corev1.PersistentVolumeClaimList
	err = r.PhysicalClient.List(ctx, &pvcList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list persistent volume claims: %w", err)
	}
	physicalPVCNames := make([]string, 0, len(pvcList.Items))
	for _, pvc := range pvcList.Items {
		physicalPVCNames = append(physicalPVCNames, pvc.Name)
	}
	if len(pvcList.Items) > 0 {
		r.Log.Info("Found kubeocean-managed persistent volume claims in physical cluster", "count", len(pvcList.Items))
		return fmt.Sprintf("persistent volume claims: [%s]", strings.Join(physicalPVCNames, ", ")), nil
	}

	// Check PVs
	var pvList corev1.PersistentVolumeList
	err = r.PhysicalClient.List(ctx, &pvList, client.MatchingLabelsSelector{Selector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("failed to list persistent volumes: %w", err)
	}
	physicalPVNames := make([]string, 0, len(pvList.Items))
	for _, pv := range pvList.Items {
		physicalPVNames = append(physicalPVNames, pv.Name)
	}
	if len(pvList.Items) > 0 {
		r.Log.Info("Found kubeocean-managed persistent volumes in physical cluster", "count", len(pvList.Items))
		return fmt.Sprintf("persistent volumes: [%s]", strings.Join(physicalPVNames, ", ")), nil
	}

	return "", nil
}

// SetupWithManager sets up the controller with the Manager
func (r *ClusterBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Use unique controller name to avoid conflicts when multiple KubeoceanSyncer instances are running
	controllerName := fmt.Sprintf("clusterbinding-%s", r.ClusterBindingName)

	if r.Client == nil {
		return fmt.Errorf("client not available")
	}
	if r.PhysicalClient == nil {
		return fmt.Errorf("physical client not available")
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		For(&cloudv1beta1.ClusterBinding{},
			builder.WithPredicates(
				predicate.Funcs{
					CreateFunc: func(e event.CreateEvent) bool {
						obj, ok := e.Object.(*cloudv1beta1.ClusterBinding)
						if ok && obj.Name == r.ClusterBindingName {
							return true
						}
						return false
					},
					UpdateFunc: func(e event.UpdateEvent) bool {
						obj, ok := e.ObjectNew.(*cloudv1beta1.ClusterBinding)
						if ok && obj.Name == r.ClusterBindingName {
							return true
						}
						return false
					},
					DeleteFunc: func(e event.DeleteEvent) bool {
						obj, ok := e.Object.(*cloudv1beta1.ClusterBinding)
						if ok && obj.Name == r.ClusterBindingName {
							return true
						}
						return false
					},
				},
			)).
		Complete(r)
}
