/*
Copyright 2024 The Kubeocean Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package bottomup

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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
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

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	bottomnode "github.com/TKEColocation/kubeocean/pkg/syncer/bottomup/node"
)

// mockManager implements a minimal manager.Manager for testing
type mockManager struct {
	client client.Client
	scheme *runtime.Scheme
	config *rest.Config
}

func (m *mockManager) Add(manager.Runnable) error {
	return nil
}

func (m *mockManager) Elected() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (m *mockManager) AddMetricsExtraHandler(path string, handler interface{}) error {
	return nil
}

func (m *mockManager) AddMetricsServerExtraHandler(path string, handler http.Handler) error {
	return nil
}

func (m *mockManager) AddHealthzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *mockManager) AddReadyzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *mockManager) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (m *mockManager) GetConfig() *rest.Config {
	if m.config != nil {
		return m.config
	}
	return &rest.Config{}
}

func (m *mockManager) GetScheme() *runtime.Scheme {
	return m.scheme
}

func (m *mockManager) GetClient() client.Client {
	return m.client
}

func (m *mockManager) GetFieldIndexer() client.FieldIndexer {
	return nil
}

func (m *mockManager) GetCache() cache.Cache {
	return nil
}

func (m *mockManager) GetEventRecorderFor(name string) record.EventRecorder {
	return nil
}

func (m *mockManager) GetRESTMapper() meta.RESTMapper {
	return nil
}

func (m *mockManager) GetAPIReader() client.Reader {
	return m.client
}

func (m *mockManager) GetWebhookServer() webhook.Server {
	return nil
}

func (m *mockManager) GetLogger() logr.Logger {
	return ctrl.Log
}

func (m *mockManager) GetControllerOptions() config.Controller {
	return config.Controller{}
}

func (m *mockManager) GetHTTPClient() *http.Client {
	return nil
}

func TestNewBottomUpSyncer(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	virtualMgr := &mockManager{
		client: virtualClient,
		scheme: scheme,
	}
	physicalMgr := &mockManager{
		client: physicalClient,
		scheme: scheme,
	}

	binding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster-id",
		},
	}

	syncer := NewBottomUpSyncer(virtualMgr, physicalMgr, scheme, binding)

	assert.NotNil(t, syncer)
	assert.Equal(t, scheme, syncer.Scheme)
	assert.Equal(t, binding, syncer.ClusterBinding)
	assert.NotNil(t, syncer.Log)
	assert.Equal(t, virtualMgr, syncer.virtualManager)
	assert.Equal(t, physicalMgr, syncer.physicalManager)
	assert.Equal(t, 0, syncer.prometheusVNodeBasePort) // Default value
}

func TestBottomUpSyncer_SetPrometheusVNodeBasePort(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	syncer := &BottomUpSyncer{
		Scheme: scheme,
	}

	// Test setting port
	syncer.SetPrometheusVNodeBasePort(8080)
	assert.Equal(t, 8080, syncer.prometheusVNodeBasePort)

	// Test updating port
	syncer.SetPrometheusVNodeBasePort(9090)
	assert.Equal(t, 9090, syncer.prometheusVNodeBasePort)

	// Test setting zero port
	syncer.SetPrometheusVNodeBasePort(0)
	assert.Equal(t, 0, syncer.prometheusVNodeBasePort)
}

func TestBottomUpSyncer_RequeueNodes(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	tests := []struct {
		name              string
		setupReconciler   bool
		nodeNames         []string
		expectError       bool
		errorContains     string
		triggerShouldFail bool
	}{
		{
			name:            "reconciler not initialized",
			setupReconciler: false,
			nodeNames:       []string{"node1"},
			expectError:     true,
			errorContains:   "node reconciler not initialized",
		},
		{
			name:            "requeue single node",
			setupReconciler: true,
			nodeNames:       []string{"node1"},
			expectError:     false,
		},
		{
			name:            "requeue multiple nodes",
			setupReconciler: true,
			nodeNames:       []string{"node1", "node2", "node3"},
			expectError:     false,
		},
		{
			name:            "requeue empty list",
			setupReconciler: true,
			nodeNames:       []string{},
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
			}

			syncer := &BottomUpSyncer{
				Scheme:         scheme,
				ClusterBinding: binding,
				Log:            ctrl.Log.WithName("test-syncer"),
			}

			if tt.setupReconciler {
				// Create a mock reconciler with a work queue
				virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
				physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

				syncer.nodeReconciler = &bottomnode.PhysicalNodeReconciler{
					PhysicalClient:     physicalClient,
					VirtualClient:      virtualClient,
					Scheme:             scheme,
					ClusterBindingName: binding.Name,
					ClusterBinding:     binding,
					Log:                ctrl.Log.WithName("test-node-reconciler"),
				}
			}

			err := syncer.RequeueNodes(tt.nodeNames)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBottomUpSyncer_GetNodesMatchingSelector(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	tests := []struct {
		name          string
		physicalNodes []client.Object
		selector      *corev1.NodeSelector
		expectedNodes []string
		expectError   bool
		setupManager  bool
		errorContains string
	}{
		{
			name:          "manager not initialized",
			setupManager:  false,
			selector:      nil,
			expectError:   true,
			errorContains: "physical manager not initialized",
		},
		{
			name:         "nil selector matches all nodes",
			setupManager: true,
			physicalNodes: []client.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"type": "worker",
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node2",
						Labels: map[string]string{
							"type": "master",
						},
					},
				},
			},
			selector:      nil,
			expectedNodes: []string{"node1", "node2"},
			expectError:   false,
		},
		{
			name:         "selector matches specific nodes",
			setupManager: true,
			physicalNodes: []client.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-node-1",
						Labels: map[string]string{
							"node-type": "worker",
							"zone":      "zone-a",
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-node-2",
						Labels: map[string]string{
							"node-type": "worker",
							"zone":      "zone-b",
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "master-node",
						Labels: map[string]string{
							"node-type": "master",
							"zone":      "zone-a",
						},
					},
				},
			},
			selector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"worker"},
							},
						},
					},
				},
			},
			expectedNodes: []string{"worker-node-1", "worker-node-2"},
			expectError:   false,
		},
		{
			name:         "selector matches no nodes",
			setupManager: true,
			physicalNodes: []client.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"type": "worker",
						},
					},
				},
			},
			selector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"gpu"},
							},
						},
					},
				},
			},
			expectedNodes: []string{},
			expectError:   false,
		},
		{
			name:          "empty cluster",
			setupManager:  true,
			physicalNodes: []client.Object{},
			selector:      nil,
			expectedNodes: []string{},
			expectError:   false,
		},
		{
			name:         "complex selector with multiple requirements",
			setupManager: true,
			physicalNodes: []client.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"node-type": "worker",
							"zone":      "zone-a",
							"gpu":       "true",
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node2",
						Labels: map[string]string{
							"node-type": "worker",
							"zone":      "zone-a",
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node3",
						Labels: map[string]string{
							"node-type": "worker",
							"zone":      "zone-b",
							"gpu":       "true",
						},
					},
				},
			},
			selector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"worker"},
							},
							{
								Key:      "zone",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"zone-a"},
							},
							{
								Key:      "gpu",
								Operator: corev1.NodeSelectorOpExists,
							},
						},
					},
				},
			},
			expectedNodes: []string{"node1"},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
			}

			syncer := &BottomUpSyncer{
				Scheme:         scheme,
				ClusterBinding: binding,
				Log:            ctrl.Log.WithName("test-syncer"),
			}

			if tt.setupManager {
				physicalClient := fakeclient.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(tt.physicalNodes...).
					Build()

				syncer.physicalManager = &mockManager{
					client: physicalClient,
					scheme: scheme,
				}
			}

			ctx := context.Background()
			matchingNodes, err := syncer.GetNodesMatchingSelector(ctx, tt.selector)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tt.expectedNodes, matchingNodes)
			}
		})
	}
}

func TestBottomUpSyncer_nodeMatchesSelector(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	binding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	syncer := &BottomUpSyncer{
		Scheme:         scheme,
		ClusterBinding: binding,
		Log:            ctrl.Log.WithName("test-syncer"),
	}

	tests := []struct {
		name     string
		node     *corev1.Node
		selector *corev1.NodeSelector
		expected bool
	}{
		{
			name: "nil selector matches any node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"type": "worker",
					},
				},
			},
			selector: nil,
			expected: true,
		},
		{
			name: "matching node selector",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"node-type": "worker",
					},
				},
			},
			selector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"worker"},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "non-matching node selector",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"node-type": "worker",
					},
				},
			},
			selector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"master"},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "node with multiple labels matching complex selector",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"node-type": "worker",
						"zone":      "zone-a",
						"gpu":       "nvidia",
					},
				},
			},
			selector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "node-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"worker"},
							},
							{
								Key:      "gpu",
								Operator: corev1.NodeSelectorOpExists,
							},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := syncer.nodeMatchesSelector(tt.node, tt.selector)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBottomUpSyncer_Start(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	binding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	syncer := &BottomUpSyncer{
		Scheme:         scheme,
		ClusterBinding: binding,
		Log:            ctrl.Log.WithName("test-syncer"),
	}

	// Setup a node reconciler to test cleanup
	syncer.nodeReconciler = &bottomnode.PhysicalNodeReconciler{
		PhysicalClient:     physicalClient,
		VirtualClient:      virtualClient,
		Scheme:             scheme,
		ClusterBindingName: binding.Name,
		ClusterBinding:     binding,
		Log:                ctrl.Log.WithName("test-node-reconciler"),
	}

	// Create a context that we can cancel
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Start syncer in goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- syncer.Start(ctx)
	}()

	// Wait for context to be done
	<-ctx.Done()

	// Wait a bit for cleanup to complete
	time.Sleep(50 * time.Millisecond)

	// Get result
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(1 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

// TestBottomUpSyncer_Setup is skipped in unit tests because Setup requires
// a complete controller-runtime environment with real managers.
// Setup functionality should be tested in integration tests instead.
// This test verifies that the syncer can be initialized properly before Setup.
func TestBottomUpSyncer_Setup(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	virtualMgr := &mockManager{
		client: virtualClient,
		scheme: scheme,
		config: &rest.Config{
			Host: "https://virtual-cluster:6443",
		},
	}
	physicalMgr := &mockManager{
		client: physicalClient,
		scheme: scheme,
		config: &rest.Config{
			Host: "https://physical-cluster:6443",
		},
	}

	binding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster-id",
		},
	}

	syncer := NewBottomUpSyncer(virtualMgr, physicalMgr, scheme, binding)
	syncer.SetPrometheusVNodeBasePort(8080)

	// Verify the syncer is properly initialized
	assert.NotNil(t, syncer)
	assert.NotNil(t, syncer.virtualManager)
	assert.NotNil(t, syncer.physicalManager)
	assert.Equal(t, scheme, syncer.Scheme)
	assert.Equal(t, binding, syncer.ClusterBinding)
	assert.Equal(t, 8080, syncer.prometheusVNodeBasePort)

	// Note: Actual Setup() call is skipped in unit tests because it requires
	// a complete controller-runtime environment. This should be tested in
	// integration tests with envtest or a real cluster.
}

func TestBottomUpSyncer_IntegrationFlow(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Create test nodes
	physicalNodes := []client.Object{
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "worker-1",
				Labels: map[string]string{
					"node-type": "worker",
					"zone":      "zone-a",
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "worker-2",
				Labels: map[string]string{
					"node-type": "worker",
					"zone":      "zone-b",
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				},
			},
		},
	}

	virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	physicalClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(physicalNodes...).
		Build()

	virtualMgr := &mockManager{
		client: virtualClient,
		scheme: scheme,
	}
	physicalMgr := &mockManager{
		client: physicalClient,
		scheme: scheme,
	}

	binding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster-id",
		},
	}

	// Create syncer
	syncer := NewBottomUpSyncer(virtualMgr, physicalMgr, scheme, binding)
	syncer.SetPrometheusVNodeBasePort(9090)

	assert.Equal(t, 9090, syncer.prometheusVNodeBasePort)

	// Test GetNodesMatchingSelector with zone-a selector
	ctx := context.Background()
	selector := &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      "zone",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"zone-a"},
					},
				},
			},
		},
	}

	matchingNodes, err := syncer.GetNodesMatchingSelector(ctx, selector)
	assert.NoError(t, err)
	assert.Equal(t, []string{"worker-1"}, matchingNodes)

	// Test GetNodesMatchingSelector with worker selector
	workerSelector := &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      "node-type",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"worker"},
					},
				},
			},
		},
	}

	allWorkers, err := syncer.GetNodesMatchingSelector(ctx, workerSelector)
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{"worker-1", "worker-2"}, allWorkers)
}
