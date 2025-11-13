package hostport

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	"github.com/gocrane/kubeocean/pkg/utils"
)

const (
	// FakePodNamespace is the namespace for fake pods
	FakePodNamespace = "kubeocean-fake"
	// FakePodGenerateName is the generate name prefix for fake pods
	FakePodGenerateName = "fake-pod-for-hostport"
	// FakeContainerName is the name of the fake container
	FakeContainerName = "fake-container"
	// FakeContainerImage is the image for the fake container
	FakeContainerImage = "busybox:1.28"
	// PriorityClassName for fake pods
	SystemNodeCriticalPriorityClass = "system-node-critical"
)

// HostPortNodeReconciler monitors physical nodes and manages fake pods for hostPort synchronization
type HostPortNodeReconciler struct {
	PhysicalClient     client.Client
	VirtualClient      client.Client
	Scheme             *runtime.Scheme
	ClusterBindingName string
	ClusterBinding     *cloudv1beta1.ClusterBinding
	Log                logr.Logger

	workQueue workqueue.TypedRateLimitingInterface[reconcile.Request]
}

//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch

// Reconcile handles node events for hostPort synchronization
func (r *HostPortNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("node", req.Name)
	logger.V(1).Info("Starting hostport reconcile")

	// Get physical node
	physicalNode := &corev1.Node{}
	err := r.PhysicalClient.Get(ctx, req.NamespacedName, physicalNode)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Physical node not found, nothing to do")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get physical node")
		return ctrl.Result{}, err
	}

	// Check if physical node has policy-applied label
	if !r.hasAppliedPolicyLabel(physicalNode) {
		logger.V(1).Info("Physical node does not have policy-applied label, skipping")
		return ctrl.Result{}, nil
	}

	// Generate virtual node name
	virtualNodeName := r.generateVirtualNodeName(physicalNode.Name)
	logger = logger.WithValues("virtualNode", virtualNodeName)

	// Check if virtual node exists
	virtualNode := &corev1.Node{}
	err = r.VirtualClient.Get(ctx, client.ObjectKey{Name: virtualNodeName}, virtualNode)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Virtual node not found, skipping hostport sync")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get virtual node")
		return ctrl.Result{}, err
	}

	// List all pods on the physical node
	podList := &corev1.PodList{}
	listOptions := []client.ListOption{
		client.MatchingFields{"spec.nodeName": physicalNode.Name},
	}

	err = r.PhysicalClient.List(ctx, podList, listOptions...)
	if err != nil {
		logger.Error(err, "Failed to list pods on physical node")
		return ctrl.Result{}, err
	}

	// Collect hostPorts from all pods (excluding kubeocean managed pods)
	hostPorts := r.collectHostPorts(podList.Items)
	logger.V(1).Info("Collected hostPorts from physical node", "hostPorts", hostPorts)

	// Handle fake pod creation/update
	if err := r.handleFakePod(ctx, virtualNodeName, hostPorts, logger); err != nil {
		logger.Error(err, "Failed to handle fake pod")
		return ctrl.Result{}, err
	}

	logger.V(1).Info("Hostport reconcile completed successfully")
	return ctrl.Result{}, nil
}

// handleVirtualNodeEvent handles virtual node addition and deletion events
func (r *HostPortNodeReconciler) handleVirtualNodeEvent(node *corev1.Node, eventType string) {
	log := r.Log.WithValues("virtualNode", node.Name, "eventType", eventType)

	// Check if this is a kubeocean-managed node
	if node.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
		log.V(1).Info("Virtual node not managed by kubeocean, skipping")
		return
	}

	// Check if this node belongs to current cluster binding
	if node.Labels[cloudv1beta1.LabelClusterBinding] != r.ClusterBindingName {
		log.V(1).Info("Virtual node belongs to different cluster binding, skipping",
			"nodeClusterBinding", node.Labels[cloudv1beta1.LabelClusterBinding],
			"currentClusterBinding", r.ClusterBindingName)
		return
	}

	// Get physical node name from label
	physicalNodeName := node.Labels[cloudv1beta1.LabelPhysicalNodeName]
	if physicalNodeName == "" {
		log.V(1).Info("Virtual node missing physical node name label, skipping")
		return
	}

	log.Info("Virtual node event, triggering reconciliation",
		"eventType", eventType, "physicalNode", physicalNodeName)

	r.workQueue.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: physicalNodeName}})
}

// SetupWithManager sets up the controller with the Manager
func (r *HostPortNodeReconciler) SetupWithManager(physicalManager ctrl.Manager, virtualManager ctrl.Manager) error {

	// Watch physical nodes with policy-applied label changes
	nodePredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			node, ok := e.Object.(*corev1.Node)
			if !ok {
				// invalid node means the object may be the pod object
				_, ok2 := e.Object.(*corev1.Pod)
				return ok2
			}
			return r.hasAppliedPolicyLabel(node)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, ok := e.ObjectOld.(*corev1.Node)
			if !ok {
				// invalid node means the object may be the pod object
				_, ok2 := e.ObjectOld.(*corev1.Pod)
				return ok2
			}
			newNode, ok := e.ObjectNew.(*corev1.Node)
			if !ok {
				_, ok2 := e.ObjectNew.(*corev1.Pod)
				return ok2
			}

			return oldNode.Labels[cloudv1beta1.LabelPolicyApplied] != newNode.Labels[cloudv1beta1.LabelPolicyApplied]
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			node, ok := e.Object.(*corev1.Node)
			if !ok {
				_, ok2 := e.Object.(*corev1.Pod)
				return ok2
			}
			return r.hasAppliedPolicyLabel(node)
		},
	}

	// Create a custom handler that handles pod events and enqueues node reconcile requests
	podHandler := handler.Funcs{
		CreateFunc: func(ctx context.Context, event event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			pod := event.Object.(*corev1.Pod)
			if pod == nil {
				return
			}
			if r.shouldProcessPod(pod) {
				r.Log.V(1).Info("Pod created", "namespace", pod.Namespace, "name", pod.Name, "node", pod.Spec.NodeName)
				q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: pod.Spec.NodeName}})
			}
		},
		UpdateFunc: func(ctx context.Context, event event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			pod := event.ObjectOld.(*corev1.Pod)
			npod := event.ObjectNew.(*corev1.Pod)
			if pod == nil || npod == nil {
				return
			}
			if r.shouldProcessPod(npod) {
				r.Log.V(1).Info("Pod updated", "namespace", npod.Namespace, "name", npod.Name, "node", npod.Spec.NodeName)
				q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: npod.Spec.NodeName}})
			}
		},
		DeleteFunc: func(ctx context.Context, event event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			pod := event.Object.(*corev1.Pod)
			if pod == nil {
				return
			}
			if r.shouldProcessPod(pod) {
				r.Log.V(1).Info("Pod deleted", "namespace", pod.Namespace, "name", pod.Name, "node", pod.Spec.NodeName)
				q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: pod.Spec.NodeName}})
			}
		},
	}

	// Generate unique controller name using cluster binding name (similar to physical node controller)
	uniqueControllerName := fmt.Sprintf("hostport-node-%s", r.ClusterBindingName)
	rateLimiter := workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 5*time.Minute)
	r.workQueue = workqueue.NewTypedRateLimitingQueueWithConfig(rateLimiter, workqueue.TypedRateLimitingQueueConfig[reconcile.Request]{
		Name: uniqueControllerName,
	})

	// Setup virtualManager node informer for watching virtual node changes
	nodeInformer, err := virtualManager.GetCache().GetInformer(context.TODO(), &corev1.Node{})
	if err != nil {
		return fmt.Errorf("failed to get node informer: %w", err)
	}

	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		// handle add event only
		AddFunc: func(obj interface{}) {
			node := obj.(*corev1.Node)
			if node != nil {
				r.handleVirtualNodeEvent(node, "add")
			}
		},
	})

	// Create controller using the builder pattern
	return ctrl.NewControllerManagedBy(physicalManager).
		For(&corev1.Node{}).
		WithEventFilter(nodePredicate).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 50,
			RateLimiter:             rateLimiter,
			NewQueue: func(controllerName string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
				return r.workQueue
			},
		}).
		Watches(&corev1.Pod{}, podHandler).
		Named(uniqueControllerName).
		Complete(r)
}

// hasAppliedPolicyLabel checks if node has the policy-applied label
func (r *HostPortNodeReconciler) hasAppliedPolicyLabel(node *corev1.Node) bool {
	if node == nil {
		return false
	}
	if node.Labels == nil {
		return false
	}
	value, exists := node.Labels[cloudv1beta1.LabelPolicyApplied]
	return exists && value != ""
}

// shouldProcessPod determines if a pod should be processed for hostPort sync
func (r *HostPortNodeReconciler) shouldProcessPod(pod *corev1.Pod) bool {
	// Skip if pod is not scheduled
	if pod.Spec.NodeName == "" {
		return false
	}

	// Skip kubeocean managed pods
	if r.isKubeoceanManagedPod(pod) {
		return false
	}

	// Skip if pod doesn't have hostPort
	return r.podHasHostPort(pod)
}

// podHasHostPort checks if pod has any container with hostPort
func (r *HostPortNodeReconciler) podHasHostPort(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.HostPort > 0 {
				return true
			}
		}
	}
	for _, container := range pod.Spec.InitContainers {
		for _, port := range container.Ports {
			if port.HostPort > 0 {
				return true
			}
		}
	}
	return false
}

// isKubeoceanManagedPod checks if pod is managed by kubeocean
func (r *HostPortNodeReconciler) isKubeoceanManagedPod(pod *corev1.Pod) bool {
	if pod.Labels == nil {
		return false
	}
	managedBy, exists := pod.Labels[cloudv1beta1.LabelManagedBy]
	return exists && managedBy == cloudv1beta1.LabelManagedByValue
}

// generateVirtualNodeName generates virtual node name from physical node name
func (r *HostPortNodeReconciler) generateVirtualNodeName(physicalNodeName string) string {
	clusterID := r.getClusterID()
	return utils.GenerateVirtualNodeName(clusterID, physicalNodeName)
}

// getClusterID returns the cluster ID from cluster binding
func (r *HostPortNodeReconciler) getClusterID() string {
	if r.ClusterBinding != nil && r.ClusterBinding.Spec.ClusterID != "" {
		return r.ClusterBinding.Spec.ClusterID
	}
	return r.ClusterBindingName
}

// collectHostPorts collects all hostPorts from pods, excluding kubeocean managed pods
func (r *HostPortNodeReconciler) collectHostPorts(pods []corev1.Pod) []corev1.ContainerPort {
	var hostPorts []corev1.ContainerPort

	for idx := range pods {
		pod := &pods[idx]
		// Skip kubeocean managed pods
		if !r.shouldProcessPod(pod) {
			continue
		}

		// Collect hostPorts from containers
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if port.HostPort != 0 {
					hostPorts = append(hostPorts, corev1.ContainerPort{
						Name:          pod.Namespace + "/" + pod.Name + "/" + container.Name + "/" + port.Name,
						ContainerPort: port.ContainerPort,
						HostPort:      port.HostPort,
						Protocol:      port.Protocol,
						HostIP:        port.HostIP,
					})
				}
			}
		}

		// Collect hostPorts from init containers
		for _, container := range pod.Spec.InitContainers {
			for _, port := range container.Ports {
				if port.HostPort != 0 {
					hostPorts = append(hostPorts, corev1.ContainerPort{
						Name:          pod.Namespace + "/" + pod.Name + "/" + container.Name + "/" + port.Name,
						ContainerPort: port.ContainerPort,
						HostPort:      port.HostPort,
						Protocol:      port.Protocol,
						HostIP:        port.HostIP,
					})
				}
			}
		}
	}

	for idx := range hostPorts {
		hostPort := &hostPorts[idx]
		if hostPort.HostIP == "" {
			hostPort.HostIP = "0.0.0.0"
		}
		if hostPort.Protocol == "" {
			hostPort.Protocol = corev1.ProtocolTCP
		}
	}

	// Sort for consistent ordering
	sort.Slice(hostPorts, func(i, j int) bool {
		if hostPorts[i].HostIP != hostPorts[j].HostIP {
			return hostPorts[i].HostIP < hostPorts[j].HostIP
		}
		if hostPorts[i].Protocol != hostPorts[j].Protocol {
			return hostPorts[i].Protocol < hostPorts[j].Protocol
		}
		return hostPorts[i].HostPort < hostPorts[j].HostPort
	})

	return hostPorts
}

// handleFakePod manages the fake pod for hostPort synchronization
func (r *HostPortNodeReconciler) handleFakePod(ctx context.Context, virtualNodeName string, hostPorts []corev1.ContainerPort, logger logr.Logger) error {
	// Ensure fake namespace exists
	if err := r.ensureFakeNamespace(ctx); err != nil {
		return fmt.Errorf("failed to ensure fake namespace: %w", err)
	}
	// List existing fake pods for this virtual node
	fakePodList := &corev1.PodList{}
	listOptions := []client.ListOption{
		client.InNamespace(FakePodNamespace),
		client.MatchingLabels{
			cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
			cloudv1beta1.LabelManagedBy:       cloudv1beta1.LabelManagedByValue,
		},
		client.MatchingFields{"spec.nodeName": virtualNodeName},
	}

	err := r.VirtualClient.List(ctx, fakePodList, listOptions...)
	if err != nil {
		return fmt.Errorf("failed to list existing fake pods: %w", err)
	}

	// If no hostPorts, clean up any existing fake pods
	if len(hostPorts) == 0 {
		logger.V(1).Info("No hostPorts found, cleaning up fake pods")
		for idx := range fakePodList.Items {
			fakePod := &fakePodList.Items[idx]
			logger.Info("Deleting fake pod", "fakePod", fakePod.Name)
			if err := r.VirtualClient.Delete(ctx, fakePod); err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("failed to delete fake pod: %w", err)
			}
		}
		return nil
	}

	// Create desired fake pod
	for idx := range hostPorts {
		// need to ensure the name is unique, no more than 15 characters
		hostPorts[idx].Name = fmt.Sprintf("port-%d", idx)
	}
	desiredFakePod := r.buildFakePod(virtualNodeName, hostPorts)

	// Check if we have the correct fake pod
	var correctFakePod *corev1.Pod
	var extraFakePods []*corev1.Pod

	for i := range fakePodList.Items {
		fakePod := &fakePodList.Items[i]
		if len(fakePod.Spec.Containers) > 0 && reflect.DeepEqual(hostPorts, fakePod.Spec.Containers[0].Ports) {
			if correctFakePod == nil {
				correctFakePod = fakePod
			} else {
				extraFakePods = append(extraFakePods, fakePod)
			}
		} else {
			extraFakePods = append(extraFakePods, fakePod)
		}
	}

	// Create new fake pod if we don't have the correct one
	if correctFakePod == nil {
		logger.Info("Creating new fake pod for hostPorts", "hostPorts", hostPorts)
		if err := r.VirtualClient.Create(ctx, desiredFakePod); err != nil {
			return fmt.Errorf("failed to create fake pod: %w", err)
		}
		logger.Info("Created new fake pod for hostPorts", "fakePod", desiredFakePod.Name)
		if err := r.PatchFakePodScheduledCondition(ctx, desiredFakePod, logger); err != nil {
			return fmt.Errorf("failed to patch fake pod scheduled condition: %w", err)
		}
	} else {
		logger.Info("Found existing fake pod for hostPorts, skip creation", "fakePod", correctFakePod.Name, "hostPorts", hostPorts)
		if err := r.PatchFakePodScheduledCondition(ctx, correctFakePod, logger); err != nil {
			return fmt.Errorf("failed to patch fake pod scheduled condition: %w", err)
		}
	}

	// Delete extra fake pods
	for _, extraPod := range extraFakePods {
		logger.Info("Deleting extra fake pod", "fakePod", extraPod.Name)
		if err := r.VirtualClient.Delete(ctx, extraPod); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete extra fake pod: %w", err)
		}
	}

	return nil
}

func (r *HostPortNodeReconciler) PatchFakePodScheduledCondition(ctx context.Context, pod *corev1.Pod, logger logr.Logger) error {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionTrue {
			return nil
		}
	}
	podCopy := pod.DeepCopy()
	podCopy.Status.Conditions = []corev1.PodCondition{
		{
			Type:               corev1.PodScheduled,
			Status:             corev1.ConditionTrue,
			LastProbeTime:      metav1.Now(),
			LastTransitionTime: metav1.Now(),
		},
	}
	if err := r.VirtualClient.Status().Patch(ctx, podCopy, client.MergeFrom(pod)); err != nil {
		return fmt.Errorf("failed to patch fake pod status: %w", err)
	}
	logger.Info("Patched fake pod scheduled condition", "pod", podCopy.Name)
	return nil
}

// buildFakePod creates a fake pod with the specified hostPorts
func (r *HostPortNodeReconciler) buildFakePod(virtualNodeName string, hostPorts []corev1.ContainerPort) *corev1.Pod {

	// Create tolerations for all taints
	tolerations := []corev1.Toleration{
		{
			Operator: corev1.TolerationOpExists,
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    FakePodNamespace,
			GenerateName: FakePodGenerateName + "-",
			Labels: map[string]string{
				cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
				cloudv1beta1.LabelManagedBy:       cloudv1beta1.LabelManagedByValue,
			},
		},
		Spec: corev1.PodSpec{
			NodeName:          virtualNodeName,
			PriorityClassName: SystemNodeCriticalPriorityClass,
			Tolerations:       tolerations,
			RestartPolicy:     corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    FakeContainerName,
					Image:   FakeContainerImage,
					Command: []string{"sleep", "100000000"},
					Ports:   hostPorts,
				},
			},
		},
	}

	return pod
}

// ensureFakeNamespace ensures the fake namespace exists in the virtual cluster
func (r *HostPortNodeReconciler) ensureFakeNamespace(ctx context.Context) error {
	fakeNS := &corev1.Namespace{}
	err := r.VirtualClient.Get(ctx, client.ObjectKey{Name: FakePodNamespace}, fakeNS)
	if err == nil {
		return nil // Namespace exists
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check fake namespace: %w", err)
	}

	// Create the namespace
	newNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: FakePodNamespace,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
		},
	}

	if err := r.VirtualClient.Create(ctx, newNS); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create fake namespace: %w", err)
	}

	return nil
}
