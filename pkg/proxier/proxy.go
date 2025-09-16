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

package proxier

import (
	"context"
	"fmt"
	"io"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

// proxy implements KubeletProxy interface
type proxy struct {
	virtualClient  client.Client
	physicalClient kubernetes.Interface
	physicalConfig *rest.Config
	log            logr.Logger
	clusterBinding *cloudv1beta1.ClusterBinding
	running        bool
}

// NewKubeletProxy creates a new Kubelet proxy
func NewKubeletProxy(
	virtualClient client.Client,
	physicalClient kubernetes.Interface,
	physicalConfig *rest.Config,
	clusterBinding *cloudv1beta1.ClusterBinding,
	log logr.Logger,
) KubeletProxy {
	return &proxy{
		virtualClient:  virtualClient,
		physicalClient: physicalClient,
		physicalConfig: physicalConfig,
		clusterBinding: clusterBinding,
		log:            log.WithName("kubelet-proxy"),
		running:        false,
	}
}

// GetContainerLogs retrieves container logs from physical cluster
func (p *proxy) GetContainerLogs(ctx context.Context, namespace, podName, containerName string, opts ContainerLogOpts) (io.ReadCloser, error) {
	logger := p.log.WithValues(
		"namespace", namespace,
		"pod", podName,
		"container", containerName,
	)

	// 1. Get virtual pod and validate it exists
	virtualPod, err := p.getVirtualPod(ctx, namespace, podName)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Virtual pod not found")
			return nil, fmt.Errorf("virtual pod %s/%s not found", namespace, podName)
		}
		logger.Error(err, "Failed to get virtual pod")
		return nil, fmt.Errorf("failed to get virtual pod: %w", err)
	}

	// 2. Check if virtual pod is managed by this cluster binding
	if !p.isPodManagedByClusterBinding(virtualPod) {
		logger.Info("Virtual pod not managed by this cluster binding")
		return nil, fmt.Errorf("virtual pod %s/%s not managed by cluster binding %s", namespace, podName, p.clusterBinding.Name)
	}

	// 3. Get physical pod mapping information
	mappingInfo, err := p.getPodMappingInfo(virtualPod)
	if err != nil {
		logger.Error(err, "Failed to get pod mapping info")
		return nil, fmt.Errorf("failed to get pod mapping info: %w", err)
	}

	// 4. Validate container exists in physical pod
	if err := p.validateContainerExists(ctx, mappingInfo, containerName); err != nil {
		logger.Error(err, "Container not found in physical pod")
		return nil, fmt.Errorf("container %s not found in physical pod %s/%s: %w", containerName, mappingInfo.PhysicalNamespace, mappingInfo.PhysicalName, err)
	}

	// 5. Get logs from physical cluster
	logs, err := p.getPhysicalPodLogs(ctx, mappingInfo, containerName, opts)
	if err != nil {
		logger.Error(err, "Failed to get logs from physical cluster")
		return nil, fmt.Errorf("failed to get logs from physical cluster: %w", err)
	}

	logger.Info("Successfully retrieved container logs")
	return logs, nil
}

// Start starts the logs proxy
func (p *proxy) Start(ctx context.Context) error {
	p.log.Info("Starting logs proxy")
	p.running = true
	return nil
}

// Stop stops the logs proxy
func (p *proxy) Stop() error {
	if !p.running {
		return nil
	}

	p.log.Info("Stopping logs proxy")
	p.running = false
	return nil
}

// IsRunning returns true if the logs proxy is running
func (p *proxy) IsRunning() bool {
	return p.running
}

// getVirtualPod gets the virtual pod
func (p *proxy) getVirtualPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := p.virtualClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, pod)
	return pod, err
}

// isPodManagedByClusterBinding checks if the pod is managed by this cluster binding
func (p *proxy) isPodManagedByClusterBinding(pod *corev1.Pod) bool {
	// Check if pod has the required annotations for physical pod mapping
	physicalNamespace := pod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	physicalName := pod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]

	if physicalNamespace == "" || physicalName == "" {
		return false
	}

	// Check if the physical pod belongs to this cluster binding
	// This is validated by checking if the physical namespace matches the cluster binding's mount namespace
	return physicalNamespace == p.clusterBinding.Spec.MountNamespace
}

// getPodMappingInfo extracts pod mapping information from virtual pod annotations
func (p *proxy) getPodMappingInfo(virtualPod *corev1.Pod) (*PodMappingInfo, error) {
	physicalNamespace := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
	physicalName := virtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]

	if physicalNamespace == "" || physicalName == "" {
		return nil, fmt.Errorf("virtual pod missing physical pod mapping annotations")
	}

	return &PodMappingInfo{
		VirtualNamespace:   virtualPod.Namespace,
		VirtualName:        virtualPod.Name,
		PhysicalNamespace:  physicalNamespace,
		PhysicalName:       physicalName,
		ClusterBindingName: p.clusterBinding.Name,
	}, nil
}

// validateContainerExists validates that the container exists in the physical pod
func (p *proxy) validateContainerExists(ctx context.Context, mappingInfo *PodMappingInfo, containerName string) error {
	physicalPod, err := p.physicalClient.CoreV1().Pods(mappingInfo.PhysicalNamespace).Get(ctx, mappingInfo.PhysicalName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get physical pod: %w", err)
	}

	// Check if container exists in the pod
	for _, container := range physicalPod.Spec.Containers {
		if container.Name == containerName {
			return nil
		}
	}

	// Also check init containers
	for _, container := range physicalPod.Spec.InitContainers {
		if container.Name == containerName {
			return nil
		}
	}

	return fmt.Errorf("container %s not found in physical pod", containerName)
}

// getPhysicalPodLogs gets logs from the physical pod
func (p *proxy) getPhysicalPodLogs(ctx context.Context, mappingInfo *PodMappingInfo, containerName string, opts ContainerLogOpts) (io.ReadCloser, error) {
	// Convert our options to Kubernetes PodLogOptions
	podLogOptions := &corev1.PodLogOptions{
		Container:  containerName,
		Timestamps: opts.Timestamps,
		Follow:     opts.Follow,
		Previous:   opts.Previous,
	}

	if opts.Tail > 0 {
		tailLines := int64(opts.Tail)
		podLogOptions.TailLines = &tailLines
	}

	if opts.LimitBytes > 0 {
		limitBytes := int64(opts.LimitBytes)
		podLogOptions.LimitBytes = &limitBytes
	}

	if !opts.SinceTime.IsZero() {
		podLogOptions.SinceTime = &metav1.Time{Time: opts.SinceTime}
	}

	if opts.SinceSeconds > 0 {
		sinceSeconds := int64(opts.SinceSeconds)
		podLogOptions.SinceSeconds = &sinceSeconds
	}

	// Get logs from physical cluster
	req := p.physicalClient.CoreV1().Pods(mappingInfo.PhysicalNamespace).GetLogs(mappingInfo.PhysicalName, podLogOptions)
	return req.Stream(ctx)
}

// RunInContainer executes a command in a container
func (p *proxy) RunInContainer(ctx context.Context, namespace, podName, containerName string, cmd []string, attach AttachIO) error {
	logger := p.log.WithValues(
		"namespace", namespace,
		"pod", podName,
		"container", containerName,
		"command", cmd,
	)

	logger.Info("Starting container exec")

	// 1. Get virtual pod and validate it exists
	logger.V(1).Info("Step 1: Getting virtual pod")
	virtualPod, err := p.getVirtualPod(ctx, namespace, podName)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Virtual pod not found")
			return fmt.Errorf("virtual pod %s/%s not found", namespace, podName)
		}
		logger.Error(err, "Failed to get virtual pod")
		return fmt.Errorf("failed to get virtual pod: %w", err)
	}
	logger.V(1).Info("Successfully got virtual pod", "podUID", virtualPod.UID)

	// 2. Check if virtual pod is managed by this cluster binding
	logger.V(1).Info("Step 2: Checking if pod is managed by cluster binding")
	if !p.isPodManagedByClusterBinding(virtualPod) {
		logger.Info("Virtual pod not managed by this cluster binding")
		return fmt.Errorf("virtual pod %s/%s not managed by cluster binding %s", namespace, podName, p.clusterBinding.Name)
	}
	logger.V(1).Info("Pod is managed by cluster binding", "clusterBinding", p.clusterBinding.Name)

	// 3. Get physical pod mapping information
	logger.V(1).Info("Step 3: Getting physical pod mapping information")
	mappingInfo, err := p.getPodMappingInfo(virtualPod)
	if err != nil {
		logger.Error(err, "Failed to get pod mapping info")
		return fmt.Errorf("failed to get pod mapping info: %w", err)
	}
	logger.Info("Got physical pod mapping",
		"physicalNamespace", mappingInfo.PhysicalNamespace,
		"physicalPod", mappingInfo.PhysicalName,
	)

	// 4. Validate container exists in physical pod
	logger.V(1).Info("Step 4: Validating container exists in physical pod")
	if err := p.validateContainerExists(ctx, mappingInfo, containerName); err != nil {
		logger.Error(err, "Container not found in physical pod")
		return fmt.Errorf("container %s not found in physical pod %s/%s: %w", containerName, mappingInfo.PhysicalNamespace, mappingInfo.PhysicalName, err)
	}
	logger.V(1).Info("Container found in physical pod", "container", containerName)

	// 5. Execute command in physical pod
	logger.V(1).Info("Step 5: Executing command in physical pod")
	return p.executeInPhysicalPod(ctx, mappingInfo, containerName, cmd, attach)
}

// executeInPhysicalPod executes command in the physical pod
func (p *proxy) executeInPhysicalPod(ctx context.Context, mappingInfo *PodMappingInfo, containerName string, cmd []string, attach AttachIO) error {
	logger := p.log.WithValues(
		"physicalNamespace", mappingInfo.PhysicalNamespace,
		"physicalPod", mappingInfo.PhysicalName,
		"container", containerName,
		"command", cmd,
	)

	logger.Info("Executing command in physical pod")

	// Create exec request to physical cluster (following tke_vnode pattern exactly)
	req := p.physicalClient.CoreV1().RESTClient().
		Post().
		Namespace(mappingInfo.PhysicalNamespace).
		Resource("pods").
		Name(mappingInfo.PhysicalName).
		SubResource("exec").
		Timeout(0).
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   cmd,
			Stdin:     attach.Stdin() != nil,
			Stdout:    attach.Stdout() != nil,
			Stderr:    attach.Stderr() != nil,
			TTY:       attach.TTY(),
		}, scheme.ParameterCodec)

	// Log the request URL for debugging
	fullURL := req.URL()
	logger.Info("Created exec request", "url", fullURL.String())

	// Create SPDY executor (following tke_vnode pattern exactly)
	exec, err := remotecommand.NewSPDYExecutor(p.physicalConfig, "POST", req.URL())
	if err != nil {
		logger.Error(err, "Failed to create SPDY executor",
			"url", req.URL().String(),
			"configHost", p.physicalConfig.Host,
		)
		return fmt.Errorf("could not make remote command: %v", err)
	}

	logger.Info("Created SPDY executor successfully")

	// Create terminal size handler following tke_vnode pattern exactly
	ts := &termSize{attach: attach}

	// Log before streaming
	logger.Info("Starting command stream")

	// Stream the exec using StreamWithContext like tke_vnode (critical fix!)
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             attach.Stdin(),
		Stdout:            attach.Stdout(),
		Stderr:            attach.Stderr(),
		Tty:               attach.TTY(),
		TerminalSizeQueue: ts,
	})
	if err != nil {
		logger.Error(err, "Failed to stream exec",
			"errorType", fmt.Sprintf("%T", err),
		)
		return err
	}

	logger.Info("Container exec completed successfully")
	return nil
}

// termSize handles terminal size changes (following your reference implementation)
type termSize struct {
	attach AttachIO
}

func (t *termSize) Next() *remotecommand.TerminalSize {
	// Follow tke_vnode exactly: blocking read from resize channel
	if t.attach.Resize() != nil {
		resize := <-t.attach.Resize()
		return &remotecommand.TerminalSize{
			Height: resize.Height,
			Width:  resize.Width,
		}
	}
	// Fallback for non-TTY sessions (this should rarely happen)
	return &remotecommand.TerminalSize{
		Width:  120,
		Height: 30,
	}
}
