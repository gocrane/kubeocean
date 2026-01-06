// Copyright 2025 The Kubeocean Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package podcache

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clientcache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// mockManagerWithCache implements manager.Manager with real cache for testing
type mockManagerWithCache struct {
	client       client.Client
	scheme       *runtime.Scheme
	config       *rest.Config
	cache        cache.Cache
	fieldIndexer client.FieldIndexer
}

func newMockManagerWithCache(t *testing.T, pods []*corev1.Pod) *mockManagerWithCache {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create fake clientset with pods
	clientset := fake.NewSimpleClientset()
	for _, pod := range pods {
		_, err := clientset.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	// Convert pods to []client.Object
	objects := make([]client.Object, len(pods))
	for i, pod := range pods {
		objects[i] = pod
	}

	// Create fake client
	fakeClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

	// Create informer factory and pod informer
	informerFactory := informers.NewSharedInformerFactory(clientset, 0)
	podInformer := informerFactory.Core().V1().Pods().Informer()

	// Add indexer
	indexers := clientcache.Indexers{
		IndexNameNodeName: func(obj interface{}) ([]string, error) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return nil, nil
			}
			if pod.Spec.NodeName == "" {
				return nil, nil
			}
			return []string{pod.Spec.NodeName}, nil
		},
	}
	require.NoError(t, podInformer.AddIndexers(indexers))

	// Start informer factory
	stopCh := make(chan struct{})
	informerFactory.Start(stopCh)
	clientcache.WaitForCacheSync(stopCh, podInformer.HasSynced)

	// Create mock cache that wraps the informer
	mockCache := &mockCache{
		informer: podInformer,
		client:   fakeClient,
	}

	// Create mock field indexer
	mockFieldIndexer := &mockFieldIndexer{
		indexer: podInformer.GetIndexer(),
	}

	return &mockManagerWithCache{
		client:       fakeClient,
		scheme:       scheme,
		config:       &rest.Config{},
		cache:        mockCache,
		fieldIndexer: mockFieldIndexer,
	}
}

type mockCache struct {
	informer clientcache.SharedIndexInformer
	client   client.Client
}

func (m *mockCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return m.client.Get(ctx, key, obj)
}

func (m *mockCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return m.client.List(ctx, list, opts...)
}

func (m *mockCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	return m.informer, nil
}

func (m *mockCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind, opts ...cache.InformerGetOption) (cache.Informer, error) {
	return m.informer, nil
}

func (m *mockCache) Start(ctx context.Context) error {
	return nil
}

func (m *mockCache) WaitForCacheSync(ctx context.Context) bool {
	return m.informer.HasSynced()
}

func (m *mockCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	return nil
}

func (m *mockCache) RemoveInformer(ctx context.Context, obj client.Object) error {
	return nil
}

type mockFieldIndexer struct {
	indexer clientcache.Indexer
}

func (m *mockFieldIndexer) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	// Index already set up, just return nil
	return nil
}

func (m *mockManagerWithCache) Add(manager.Runnable) error {
	return nil
}

func (m *mockManagerWithCache) Elected() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (m *mockManagerWithCache) AddMetricsExtraHandler(path string, handler interface{}) error {
	return nil
}

func (m *mockManagerWithCache) AddMetricsServerExtraHandler(path string, handler http.Handler) error {
	return nil
}

func (m *mockManagerWithCache) AddHealthzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *mockManagerWithCache) AddReadyzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *mockManagerWithCache) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (m *mockManagerWithCache) GetConfig() *rest.Config {
	return m.config
}

func (m *mockManagerWithCache) GetScheme() *runtime.Scheme {
	return m.scheme
}

func (m *mockManagerWithCache) GetClient() client.Client {
	return m.client
}

func (m *mockManagerWithCache) GetFieldIndexer() client.FieldIndexer {
	return m.fieldIndexer
}

func (m *mockManagerWithCache) GetCache() cache.Cache {
	return m.cache
}

func (m *mockManagerWithCache) GetEventRecorderFor(name string) record.EventRecorder {
	return nil
}

func (m *mockManagerWithCache) GetRESTMapper() meta.RESTMapper {
	return nil
}

func (m *mockManagerWithCache) GetAPIReader() client.Reader {
	return m.client
}

func (m *mockManagerWithCache) GetWebhookServer() webhook.Server {
	return nil
}

func (m *mockManagerWithCache) GetLogger() logr.Logger {
	return ctrl.Log
}

func (m *mockManagerWithCache) GetControllerOptions() config.Controller {
	return config.Controller{}
}

func (m *mockManagerWithCache) GetHTTPClient() *http.Client {
	return nil
}

func TestNewPodCacheManager(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create a simple mock manager
	mockMgr := &mockManagerWithCache{
		scheme: scheme,
		config: &rest.Config{},
	}

	// Test NewPodCacheManager
	pcm := NewPodCacheManager(mockMgr)
	require.NotNil(t, pcm)
	assert.Equal(t, mockMgr, pcm.manager)
	assert.NotNil(t, pcm.logger)
}

func TestPodCacheManager_Setup(t *testing.T) {
	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Create test pods
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
		},
	}

	// Create mock manager with cache
	mockMgr := newMockManagerWithCache(t, []*corev1.Pod{pod1})

	// Create PodCacheManager
	pcm := NewPodCacheManager(mockMgr)

	// Setup should succeed
	ctx := context.Background()
	err := pcm.Setup(ctx)
	require.NoError(t, err)
	assert.NotNil(t, pcm.indexer)
}

func TestPodCacheManager_GetPodsByNode(t *testing.T) {
	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	node1 := "node-1"
	node2 := "node-2"

	// Create test pods
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-1",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: node1,
		},
	}
	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-2",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: node1,
		},
	}
	pod3 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-3",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: node2,
		},
	}
	pod4 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-4",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: "",
		},
	}

	// Create mock manager with cache
	mockMgr := newMockManagerWithCache(t, []*corev1.Pod{pod1, pod2, pod3, pod4})

	// Create PodCacheManager and setup
	pcm := NewPodCacheManager(mockMgr)
	ctx := context.Background()
	err := pcm.Setup(ctx)
	require.NoError(t, err)

	// Wait a bit for informer to sync
	time.Sleep(100 * time.Millisecond)

	// Test GetPodsByNode for node1
	podList, err := pcm.GetPodsByNode(ctx, node1)
	require.NoError(t, err)
	require.NotNil(t, podList)
	assert.Equal(t, 2, len(podList.Items), "Should find 2 pods on node1")

	// Verify pod names
	podNames := make(map[string]bool)
	for _, pod := range podList.Items {
		podNames[pod.Name] = true
		assert.Equal(t, node1, pod.Spec.NodeName)
	}
	assert.True(t, podNames["pod-1"], "Should contain pod-1")
	assert.True(t, podNames["pod-2"], "Should contain pod-2")

	// Test GetPodsByNode for node2
	podList, err = pcm.GetPodsByNode(ctx, node2)
	require.NoError(t, err)
	require.NotNil(t, podList)
	assert.Equal(t, 1, len(podList.Items), "Should find 1 pod on node2")
	assert.Equal(t, "pod-3", podList.Items[0].Name)
	assert.Equal(t, node2, podList.Items[0].Spec.NodeName)

	// Test GetPodsByNode for non-existent node
	podList, err = pcm.GetPodsByNode(ctx, "non-existent-node")
	require.NoError(t, err)
	require.NotNil(t, podList)
	assert.Equal(t, 0, len(podList.Items), "Should find 0 pods on non-existent node")
}

func TestPodCacheManager_GetPodsByNode_WithoutSetup(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create a simple mock manager without cache
	mockMgr := &mockManagerWithCache{
		scheme: scheme,
		config: &rest.Config{},
	}

	// Create PodCacheManager without setup
	pcm := NewPodCacheManager(mockMgr)

	// GetPodsByNode should fail when indexer is not initialized
	ctx := context.Background()
	podList, err := pcm.GetPodsByNode(ctx, "node-1")
	assert.Error(t, err)
	assert.Nil(t, podList)
	assert.Contains(t, err.Error(), "pod indexer is not initialized")
}

func TestPodCacheManager_GetPodsByNode_Concurrent(t *testing.T) {
	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	node1 := "node-1"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-concurrent",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: node1,
		},
	}

	// Create mock manager with cache
	mockMgr := newMockManagerWithCache(t, []*corev1.Pod{pod})

	// Create PodCacheManager and setup
	pcm := NewPodCacheManager(mockMgr)
	ctx := context.Background()
	err := pcm.Setup(ctx)
	require.NoError(t, err)

	// Wait a bit for informer to sync
	time.Sleep(100 * time.Millisecond)

	// Test concurrent access
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			podList, err := pcm.GetPodsByNode(ctx, node1)
			assert.NoError(t, err)
			assert.NotNil(t, podList)
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestPodCacheManager_ManagerInterface(t *testing.T) {
	// Create test pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-interface",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
		},
	}

	// Create mock manager with cache
	mockMgr := newMockManagerWithCache(t, []*corev1.Pod{pod})

	// Create PodCacheManager
	pcm := NewPodCacheManager(mockMgr)

	// Verify PodCacheManager implements Manager interface
	var _ Manager = pcm

	// Setup
	ctx := context.Background()
	err := pcm.Setup(ctx)
	require.NoError(t, err)

	// Wait a bit for informer to sync
	time.Sleep(100 * time.Millisecond)

	// Test through interface
	var manager Manager = pcm
	podList, err := manager.GetPodsByNode(ctx, "test-node")
	// Should not error even if no pods found
	assert.NoError(t, err)
	assert.NotNil(t, podList)
}
