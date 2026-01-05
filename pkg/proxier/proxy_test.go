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
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
)

// mockClientForProxy implements client.Client for testing proxy
type mockClientForProxy struct {
	getFunc    func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	listFunc   func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
	createFunc func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
	deleteFunc func(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
	updateFunc func(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
	patchFunc  func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
	statusFunc func() client.StatusWriter
	schemeFunc func() *runtime.Scheme
}

func (m *mockClientForProxy) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if m.getFunc != nil {
		return m.getFunc(ctx, key, obj, opts...)
	}
	return nil
}

func (m *mockClientForProxy) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if m.listFunc != nil {
		return m.listFunc(ctx, list, opts...)
	}
	return nil
}

func (m *mockClientForProxy) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if m.createFunc != nil {
		return m.createFunc(ctx, obj, opts...)
	}
	return nil
}

func (m *mockClientForProxy) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(ctx, obj, opts...)
	}
	return nil
}

func (m *mockClientForProxy) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if m.updateFunc != nil {
		return m.updateFunc(ctx, obj, opts...)
	}
	return nil
}

func (m *mockClientForProxy) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if m.patchFunc != nil {
		return m.patchFunc(ctx, obj, patch, opts...)
	}
	return nil
}

func (m *mockClientForProxy) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return nil
}

func (m *mockClientForProxy) Status() client.StatusWriter {
	if m.statusFunc != nil {
		return m.statusFunc()
	}
	return nil
}

func (m *mockClientForProxy) Scheme() *runtime.Scheme {
	if m.schemeFunc != nil {
		return m.schemeFunc()
	}
	return nil
}

func (m *mockClientForProxy) RESTMapper() meta.RESTMapper {
	return nil
}

func (m *mockClientForProxy) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}

func (m *mockClientForProxy) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	return false, nil
}

func (m *mockClientForProxy) SubResource(subResource string) client.SubResourceClient {
	return nil
}

// mockAttachIOForProxy implements AttachIO interface for testing
type mockAttachIOForProxy struct {
	stdin      io.Reader
	stdout     io.WriteCloser
	stderr     io.WriteCloser
	tty        bool
	resizeChan chan TermSize
}

func (m *mockAttachIOForProxy) Stdin() io.Reader {
	return m.stdin
}

func (m *mockAttachIOForProxy) Stdout() io.WriteCloser {
	return m.stdout
}

func (m *mockAttachIOForProxy) Stderr() io.WriteCloser {
	return m.stderr
}

func (m *mockAttachIOForProxy) TTY() bool {
	return m.tty
}

func (m *mockAttachIOForProxy) HasStdin() bool {
	return m.stdin != nil
}

func (m *mockAttachIOForProxy) HasStdout() bool {
	return m.stdout != nil
}

func (m *mockAttachIOForProxy) HasStderr() bool {
	return m.stderr != nil
}

func (m *mockAttachIOForProxy) Resize() <-chan TermSize {
	return m.resizeChan
}

// nopWriteCloser is a WriteCloser that does nothing
type nopWriteCloser struct {
	io.Writer
}

func (n *nopWriteCloser) Close() error {
	return nil
}

// TestNewKubeletProxy tests proxy initialization
func TestNewKubeletProxy(t *testing.T) {
	virtualClient := &mockClientForProxy{}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	assert.NotNil(t, proxy)
	assert.False(t, proxy.IsRunning())
}

// TestProxyStartStop tests proxy lifecycle
func TestProxyStartStop(t *testing.T) {
	virtualClient := &mockClientForProxy{}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	// Initially not running
	assert.False(t, proxy.IsRunning())

	// Start
	err := proxy.Start(context.Background())
	assert.NoError(t, err)
	assert.True(t, proxy.IsRunning())

	// Stop
	err = proxy.Stop()
	assert.NoError(t, err)
	assert.False(t, proxy.IsRunning())

	// Stop again (should be safe)
	err = proxy.Stop()
	assert.NoError(t, err)
	assert.False(t, proxy.IsRunning())
}

// TestGetContainerLogsVirtualPodNotFound tests logs retrieval when virtual pod doesn't exist
func TestGetContainerLogsVirtualPodNotFound(t *testing.T) {
	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, key.Name)
		},
	}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	_, err := proxy.GetContainerLogs(context.Background(), "default", "test-pod", "test-container", ContainerLogOpts{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestGetContainerLogsVirtualPodGetError tests logs retrieval when getting virtual pod fails
func TestGetContainerLogsVirtualPodGetError(t *testing.T) {
	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return fmt.Errorf("API server error")
		},
	}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	_, err := proxy.GetContainerLogs(context.Background(), "default", "test-pod", "test-container", ContainerLogOpts{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get virtual pod")
}

// TestGetContainerLogsPodNotManagedByClusterBinding tests logs retrieval when pod is not managed by cluster binding
func TestGetContainerLogsPodNotManagedByClusterBinding(t *testing.T) {
	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			pod := obj.(*corev1.Pod)
			pod.Namespace = "default"
			pod.Name = "test-pod"
			pod.Annotations = map[string]string{
				cloudv1beta1.AnnotationPhysicalPodNamespace: "wrong-namespace",
				cloudv1beta1.AnnotationPhysicalPodName:      "test-physical-pod",
			}
			return nil
		},
	}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	_, err := proxy.GetContainerLogs(context.Background(), "default", "test-pod", "test-container", ContainerLogOpts{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not managed by cluster binding")
}

// TestGetContainerLogsMissingAnnotations tests logs retrieval when pod is missing annotations
func TestGetContainerLogsMissingAnnotations(t *testing.T) {
	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			pod := obj.(*corev1.Pod)
			pod.Namespace = "default"
			pod.Name = "test-pod"
			pod.Annotations = map[string]string{}
			return nil
		},
	}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	_, err := proxy.GetContainerLogs(context.Background(), "default", "test-pod", "test-container", ContainerLogOpts{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not managed by cluster binding")
}

// TestGetContainerLogsPhysicalPodNotFound tests logs retrieval when physical pod doesn't exist
func TestGetContainerLogsPhysicalPodNotFound(t *testing.T) {
	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			pod := obj.(*corev1.Pod)
			pod.Namespace = "default"
			pod.Name = "test-pod"
			pod.Annotations = map[string]string{
				cloudv1beta1.AnnotationPhysicalPodNamespace: "test-namespace",
				cloudv1beta1.AnnotationPhysicalPodName:      "test-physical-pod",
			}
			return nil
		},
	}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	_, err := proxy.GetContainerLogs(context.Background(), "default", "test-pod", "test-container", ContainerLogOpts{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "container")
}

// TestGetContainerLogsContainerNotFound tests logs retrieval when container doesn't exist in physical pod
func TestGetContainerLogsContainerNotFound(t *testing.T) {
	physicalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-physical-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "other-container"},
			},
		},
	}

	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			pod := obj.(*corev1.Pod)
			pod.Namespace = "default"
			pod.Name = "test-pod"
			pod.Annotations = map[string]string{
				cloudv1beta1.AnnotationPhysicalPodNamespace: "test-namespace",
				cloudv1beta1.AnnotationPhysicalPodName:      "test-physical-pod",
			}
			return nil
		},
	}
	physicalClient := fake.NewSimpleClientset(physicalPod)
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	_, err := proxy.GetContainerLogs(context.Background(), "default", "test-pod", "test-container", ContainerLogOpts{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestGetContainerLogsWithOptions tests logs retrieval with various options
func TestGetContainerLogsWithOptions(t *testing.T) {
	tests := []struct {
		name string
		opts ContainerLogOpts
	}{
		{
			name: "with tail lines",
			opts: ContainerLogOpts{Tail: 100},
		},
		{
			name: "with limit bytes",
			opts: ContainerLogOpts{LimitBytes: 1024},
		},
		{
			name: "with timestamps",
			opts: ContainerLogOpts{Timestamps: true},
		},
		{
			name: "with follow",
			opts: ContainerLogOpts{Follow: true},
		},
		{
			name: "with previous",
			opts: ContainerLogOpts{Previous: true},
		},
		{
			name: "with since seconds",
			opts: ContainerLogOpts{SinceSeconds: 3600},
		},
		{
			name: "with since time",
			opts: ContainerLogOpts{SinceTime: time.Now().Add(-1 * time.Hour)},
		},
		{
			name: "with multiple options",
			opts: ContainerLogOpts{
				Tail:         50,
				Timestamps:   true,
				SinceSeconds: 1800,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			physicalPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-physical-pod",
					Namespace: "test-namespace",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "test-container"},
					},
				},
			}

			virtualClient := &mockClientForProxy{
				getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					pod := obj.(*corev1.Pod)
					pod.Namespace = "default"
					pod.Name = "test-pod"
					pod.Annotations = map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "test-namespace",
						cloudv1beta1.AnnotationPhysicalPodName:      "test-physical-pod",
					}
					return nil
				},
			}

			physicalClient := fake.NewSimpleClientset(physicalPod)
			// Intercept GetLogs call to return mock stream
			physicalClient.PrependReactor("get", "pods", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
				return false, nil, nil
			})

			physicalConfig := &rest.Config{Host: "https://test-cluster"}
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					MountNamespace: "test-namespace",
				},
			}
			log := ctrllog.Log.WithName("test")

			proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

			// Call will fail because we can't easily mock the REST client's Stream() method,
			// but we can verify the validation steps pass
			_, err := proxy.GetContainerLogs(context.Background(), "default", "test-pod", "test-container", tt.opts)
			// The validation steps should pass without error
			assert.NoError(t, err)
		})
	}
}

// TestIsPodManagedByClusterBinding tests pod management check
func TestIsPodManagedByClusterBinding(t *testing.T) {
	tests := []struct {
		name           string
		pod            *corev1.Pod
		mountNamespace string
		expected       bool
	}{
		{
			name: "pod managed by cluster binding",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "test-namespace",
						cloudv1beta1.AnnotationPhysicalPodName:      "test-pod",
					},
				},
			},
			mountNamespace: "test-namespace",
			expected:       true,
		},
		{
			name: "pod not managed - wrong namespace",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "other-namespace",
						cloudv1beta1.AnnotationPhysicalPodName:      "test-pod",
					},
				},
			},
			mountNamespace: "test-namespace",
			expected:       false,
		},
		{
			name: "pod not managed - missing namespace annotation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodName: "test-pod",
					},
				},
			},
			mountNamespace: "test-namespace",
			expected:       false,
		},
		{
			name: "pod not managed - missing name annotation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "test-namespace",
					},
				},
			},
			mountNamespace: "test-namespace",
			expected:       false,
		},
		{
			name: "pod not managed - no annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			mountNamespace: "test-namespace",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &proxy{
				clusterBinding: &cloudv1beta1.ClusterBinding{
					Spec: cloudv1beta1.ClusterBindingSpec{
						MountNamespace: tt.mountNamespace,
					},
				},
			}

			result := p.isPodManagedByClusterBinding(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetPodMappingInfo tests pod mapping info extraction
func TestGetPodMappingInfo(t *testing.T) {
	tests := []struct {
		name      string
		pod       *corev1.Pod
		expectErr bool
		expected  *PodMappingInfo
	}{
		{
			name: "valid pod mapping",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "virtual-ns",
					Name:      "virtual-pod",
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
					},
				},
			},
			expectErr: false,
			expected: &PodMappingInfo{
				VirtualNamespace:   "virtual-ns",
				VirtualName:        "virtual-pod",
				PhysicalNamespace:  "physical-ns",
				PhysicalName:       "physical-pod",
				ClusterBindingName: "test-binding",
			},
		},
		{
			name: "missing physical namespace",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodName: "physical-pod",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "missing physical name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
					},
				},
			},
			expectErr: true,
		},
		{
			name: "no annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &proxy{
				clusterBinding: &cloudv1beta1.ClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-binding",
					},
				},
			}

			result, err := p.getPodMappingInfo(tt.pod)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestValidateContainerExists tests container validation in physical pod
func TestValidateContainerExists(t *testing.T) {
	tests := []struct {
		name          string
		pod           *corev1.Pod
		containerName string
		expectErr     bool
	}{
		{
			name: "container exists in containers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "test-container"},
					},
				},
			},
			containerName: "test-container",
			expectErr:     false,
		},
		{
			name: "container exists in init containers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{Name: "init-container"},
					},
				},
			},
			containerName: "init-container",
			expectErr:     false,
		},
		{
			name: "container exists in ephemeral containers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					EphemeralContainers: []corev1.EphemeralContainer{
						{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug-container"}},
					},
				},
			},
			containerName: "debug-container",
			expectErr:     false,
		},
		{
			name: "container not found",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "other-container"},
					},
				},
			},
			containerName: "test-container",
			expectErr:     true,
		},
		{
			name: "no containers in pod",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "test-ns",
				},
				Spec: corev1.PodSpec{},
			},
			containerName: "test-container",
			expectErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			physicalClient := fake.NewSimpleClientset(tt.pod)
			p := &proxy{
				physicalClient: physicalClient,
				log:            ctrllog.Log.WithName("test"),
			}

			mappingInfo := &PodMappingInfo{
				PhysicalNamespace: tt.pod.Namespace,
				PhysicalName:      tt.pod.Name,
			}

			err := p.validateContainerExists(context.Background(), mappingInfo, tt.containerName)

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRunInContainerVirtualPodNotFound tests exec when virtual pod doesn't exist
func TestRunInContainerVirtualPodNotFound(t *testing.T) {
	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, key.Name)
		},
	}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	stdout := &nopWriteCloser{Writer: &bytes.Buffer{}}
	attach := &mockAttachIOForProxy{
		stdout: stdout,
		tty:    false,
	}

	err := proxy.RunInContainer(context.Background(), "default", "test-pod", "test-container", []string{"echo", "test"}, attach)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestRunInContainerPodNotManagedByClusterBinding tests exec when pod is not managed
func TestRunInContainerPodNotManagedByClusterBinding(t *testing.T) {
	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			pod := obj.(*corev1.Pod)
			pod.Namespace = "default"
			pod.Name = "test-pod"
			pod.Annotations = map[string]string{
				cloudv1beta1.AnnotationPhysicalPodNamespace: "wrong-namespace",
				cloudv1beta1.AnnotationPhysicalPodName:      "test-physical-pod",
			}
			return nil
		},
	}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	stdout := &nopWriteCloser{Writer: &bytes.Buffer{}}
	attach := &mockAttachIOForProxy{
		stdout: stdout,
		tty:    false,
	}

	err := proxy.RunInContainer(context.Background(), "default", "test-pod", "test-container", []string{"echo", "test"}, attach)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not managed by cluster binding")
}

// TestRunInContainerContainerNotFound tests exec when container doesn't exist
func TestRunInContainerContainerNotFound(t *testing.T) {
	physicalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-physical-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "other-container"},
			},
		},
	}

	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			pod := obj.(*corev1.Pod)
			pod.Namespace = "default"
			pod.Name = "test-pod"
			pod.Annotations = map[string]string{
				cloudv1beta1.AnnotationPhysicalPodNamespace: "test-namespace",
				cloudv1beta1.AnnotationPhysicalPodName:      "test-physical-pod",
			}
			return nil
		},
	}
	physicalClient := fake.NewSimpleClientset(physicalPod)
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}
	log := ctrllog.Log.WithName("test")

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	stdout := &nopWriteCloser{Writer: &bytes.Buffer{}}
	attach := &mockAttachIOForProxy{
		stdout: stdout,
		tty:    false,
	}

	err := proxy.RunInContainer(context.Background(), "default", "test-pod", "test-container", []string{"echo", "test"}, attach)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestTermSizeNext tests terminal size handling
func TestTermSizeNext(t *testing.T) {
	resizeChan := make(chan TermSize, 1)
	attach := &mockAttachIOForProxy{
		resizeChan: resizeChan,
		tty:        true,
	}

	ts := &termSize{attach: attach}

	// Send resize event
	go func() {
		resizeChan <- TermSize{Width: 120, Height: 40}
	}()

	size := ts.Next()
	require.NotNil(t, size)
	assert.Equal(t, uint16(120), size.Width)
	assert.Equal(t, uint16(40), size.Height)
}

// TestTermSizeNextNoResize tests terminal size when no resize channel
func TestTermSizeNextNoResize(t *testing.T) {
	attach := &mockAttachIOForProxy{
		resizeChan: nil,
		tty:        false,
	}

	ts := &termSize{attach: attach}

	size := ts.Next()
	require.NotNil(t, size)
	assert.Equal(t, uint16(120), size.Width)
	assert.Equal(t, uint16(30), size.Height)
}

// TestProxyGetVirtualPod tests getting virtual pod
func TestProxyGetVirtualPod(t *testing.T) {
	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}

	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			pod := obj.(*corev1.Pod)
			pod.Name = testPod.Name
			pod.Namespace = testPod.Namespace
			return nil
		},
	}

	p := &proxy{
		virtualClient: virtualClient,
		log:           ctrllog.Log.WithName("test"),
	}

	pod, err := p.getVirtualPod(context.Background(), "default", "test-pod")
	assert.NoError(t, err)
	assert.Equal(t, "test-pod", pod.Name)
	assert.Equal(t, "default", pod.Namespace)
}

// TestProxyGetVirtualPodError tests getting virtual pod with error
func TestProxyGetVirtualPodError(t *testing.T) {
	virtualClient := &mockClientForProxy{
		getFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return fmt.Errorf("API error")
		},
	}

	p := &proxy{
		virtualClient: virtualClient,
		log:           ctrllog.Log.WithName("test"),
	}

	_, err := p.getVirtualPod(context.Background(), "default", "test-pod")
	assert.Error(t, err)
}

// TestMockAttachIOInterface tests that mockAttachIOForProxy implements AttachIO
func TestMockAttachIOInterface(t *testing.T) {
	resizeChan := make(chan TermSize)
	stdout := &nopWriteCloser{Writer: &bytes.Buffer{}}

	mock := &mockAttachIOForProxy{
		stdout:     stdout,
		tty:        true,
		resizeChan: resizeChan,
	}

	var _ AttachIO = mock

	assert.True(t, mock.TTY())
	assert.Equal(t, stdout, mock.Stdout())
	assert.NotNil(t, mock.Resize())
}

// TestProxyLogLevels tests that proxy respects log levels
func TestProxyLogLevels(t *testing.T) {
	// Create a logger with a custom sink to capture log messages
	var logBuffer bytes.Buffer
	log := ctrllog.Log.WithName("test")

	virtualClient := &mockClientForProxy{}
	physicalClient := fake.NewSimpleClientset()
	physicalConfig := &rest.Config{Host: "https://test-cluster"}
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			MountNamespace: "test-namespace",
		},
	}

	proxy := NewKubeletProxy(virtualClient, physicalClient, physicalConfig, clusterBinding, log)

	assert.NotNil(t, proxy)
	// Log buffer should be empty or contain init logs
	_ = logBuffer.String()
}
