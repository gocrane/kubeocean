package configmap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	topcommon "github.com/gocrane/kubeocean/pkg/syncer/topdown/common"
)

func TestVirtualConfigMapReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "physical-ns",
		},
	}

	// Helper function to add ClusterID label to virtual ConfigMap
	addClusterIDLabel := func(configMap *corev1.ConfigMap) {
		if configMap != nil && configMap.Labels != nil {
			configMap.Labels["kubeocean.io/synced-by-test-cluster-id"] = cloudv1beta1.LabelValueTrue
		}
	}

	// Helper function to create a test ConfigMap
	// For virtual ConfigMap: otherName is the physical name
	// For physical ConfigMap: otherName is the virtual name
	createTestConfigMap := func(name, namespace, otherName string, data map[string]string, isVirtual bool) *corev1.ConfigMap {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				},
				Annotations: make(map[string]string),
			},
			Data: data,
		}
		if isVirtual {
			cm.Annotations[cloudv1beta1.AnnotationPhysicalName] = otherName
		} else {
			cm.Annotations[cloudv1beta1.AnnotationVirtualName] = otherName
		}
		return cm
	}

	tests := []struct {
		name              string
		virtualConfigMap  *corev1.ConfigMap
		physicalConfigMap *corev1.ConfigMap
		expectedResult    ctrl.Result
		expectError       bool
		validateFunc      func(t *testing.T, virtualClient, physicalClient client.Client)
	}{
		{
			name:           "virtual configmap not found",
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual configmap not managed by kubeocean",
			virtualConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "virtual-ns",
					Labels: map[string]string{
						"app": "test",
					},
				},
				Data: map[string]string{
					"key": "value",
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual configmap not managed by this cluster",
			virtualConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "virtual-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:               cloudv1beta1.LabelManagedByValue,
						"kubeocean.io/synced-by-other-cluster-id": cloudv1beta1.LabelValueTrue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "physical-config",
					},
				},
				Data: map[string]string{
					"key": "value",
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual configmap with no physical name label",
			virtualConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "virtual-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
				Data: map[string]string{
					"key": "value",
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual configmap being deleted",
			virtualConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-config",
					Namespace:         "virtual-ns",
					DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
					Finalizers:        []string{"test-finalizer"},
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "physical-config",
					},
				},
				Data: map[string]string{
					"key": "value",
				},
			},
			physicalConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-config",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-config",
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical configmap should be deleted
				configMap := &corev1.ConfigMap{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-config", Namespace: "physical-ns",
				}, configMap)
				assert.True(t, apierrors.IsNotFound(err))
			},
		},
		{
			name: "virtual configmap exists but physical configmap doesn't exist",
			virtualConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "virtual-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "physical-config",
					},
				},
				Data: map[string]string{
					"key": "value",
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical configmap should be created
				configMap := &corev1.ConfigMap{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-config", Namespace: "physical-ns",
				}, configMap)
				require.NoError(t, err)
				assert.Equal(t, "value", configMap.Data["key"])
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, configMap.Labels[cloudv1beta1.LabelManagedBy])
			},
		},
		{
			name:              "virtual configmap and physical configmap both exist and are in sync",
			virtualConfigMap:  createTestConfigMap("test-config", "virtual-ns", "physical-config", map[string]string{"key": "value"}, true),
			physicalConfigMap: createTestConfigMap("physical-config", "physical-ns", "test-config", map[string]string{"key": "value"}, false),
			expectedResult:    ctrl.Result{},
			expectError:       false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical configmap should remain unchanged
				configMap := &corev1.ConfigMap{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-config", Namespace: "physical-ns",
				}, configMap)
				require.NoError(t, err)
				assert.Equal(t, "value", configMap.Data["key"])
			},
		},
		{
			name:              "virtual configmap and physical configmap both exist but need update",
			virtualConfigMap:  createTestConfigMap("test-config", "virtual-ns", "physical-config", map[string]string{"key": "new-value"}, true),
			physicalConfigMap: createTestConfigMap("physical-config", "physical-ns", "test-config", map[string]string{"key": "old-value"}, false),
			expectedResult:    ctrl.Result{},
			expectError:       false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical configmap should be updated
				configMap := &corev1.ConfigMap{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-config", Namespace: "physical-ns",
				}, configMap)
				require.NoError(t, err)
				assert.Equal(t, "new-value", configMap.Data["key"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Add ClusterID label to virtual ConfigMap if it exists and has managed-by label
			if tt.virtualConfigMap != nil && tt.virtualConfigMap.Labels != nil &&
				tt.virtualConfigMap.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
				addClusterIDLabel(tt.virtualConfigMap)
			}

			// Setup clients
			var virtualObjects []client.Object
			var physicalObjects []client.Object

			if tt.virtualConfigMap != nil {
				virtualObjects = append(virtualObjects, tt.virtualConfigMap)
			}
			if tt.physicalConfigMap != nil {
				physicalObjects = append(physicalObjects, tt.physicalConfigMap)
			}

			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjects...).Build()
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjects...).Build()
			physicalK8sClient := fake.NewSimpleClientset()

			// Create reconciler
			reconciler := &VirtualConfigMapReconciler{
				VirtualClient:     virtualClient,
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: physicalK8sClient,
				Scheme:            scheme,
				ClusterBinding:    clusterBinding,
				Log:               zap.New(),
			}
			// Set ClusterID manually for testing
			reconciler.ClusterID = clusterBinding.Spec.ClusterID

			// Create request
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: func() string {
						if tt.virtualConfigMap != nil {
							return tt.virtualConfigMap.Name
						}
						return "test-config"
					}(),
					Namespace: func() string {
						if tt.virtualConfigMap != nil {
							return tt.virtualConfigMap.Namespace
						}
						return "virtual-ns"
					}(),
				},
			}

			// Run reconcile
			result, err := reconciler.Reconcile(context.TODO(), req)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedResult, result)

			// Run validation if provided
			if tt.validateFunc != nil {
				tt.validateFunc(t, virtualClient, physicalClient)
			}
		})
	}
}

func TestVirtualConfigMapReconciler_SetupWithManager(t *testing.T) {
	// Setup scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)

	// Create virtual and physical managers
	virtualManager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	require.NoError(t, err)

	physicalManager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	require.NoError(t, err)

	// Create reconciler
	reconciler := &VirtualConfigMapReconciler{
		VirtualClient:  virtualManager.GetClient(),
		PhysicalClient: physicalManager.GetClient(),
		Scheme:         scheme,
		ClusterBinding: &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cluster",
			},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:      "test-cluster-id",
				MountNamespace: "physical-namespace",
			},
		},
		Log: ctrl.Log.WithName("test"),
	}

	// Test SetupWithManager
	err = reconciler.SetupWithManager(virtualManager, physicalManager)
	assert.NoError(t, err)
}

func TestVirtualConfigMapReconciler_CheckPhysicalConfigMapExists(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "physical-ns",
		},
	}

	t.Run("physical configmap exists in cache", func(t *testing.T) {
		physicalConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config",
				Namespace: "physical-ns",
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalConfigMap).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualConfigMapReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			Scheme:            scheme,
			ClusterBinding:    clusterBinding,
			Log:               zap.New(),
		}

		exists, configMap, err := reconciler.checkPhysicalConfigMapExists(context.TODO(), "test-config")
		assert.NoError(t, err)
		assert.True(t, exists)
		assert.NotNil(t, configMap)
		assert.Equal(t, "test-config", configMap.Name)
	})

	t.Run("physical configmap not found", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualConfigMapReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			Scheme:            scheme,
			ClusterBinding:    clusterBinding,
			Log:               zap.New(),
		}

		exists, configMap, err := reconciler.checkPhysicalConfigMapExists(context.TODO(), "non-existent-config")
		assert.NoError(t, err)
		assert.False(t, exists)
		assert.Nil(t, configMap)
	})
}

// TestVirtualConfigMapReconciler_ClusterIDFunctionality tests ClusterID related functionality
func TestVirtualConfigMapReconciler_ClusterIDFunctionality(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "test-cluster",
		},
	}

	t.Run("ClusterID caching", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &VirtualConfigMapReconciler{
			VirtualClient:  virtualClient,
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test"),
		}

		// Set ClusterID directly for testing
		reconciler.ClusterID = clusterBinding.Spec.ClusterID

		// Verify ClusterID is cached
		assert.Equal(t, "test-cluster-id", reconciler.ClusterID)
	})

	t.Run("removeSyncedResourceFinalizer with ClusterID", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &VirtualConfigMapReconciler{
			VirtualClient:  virtualClient,
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test"),
			ClusterID:      "test-cluster-id",
		}

		virtualConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config",
				Namespace: "test-ns",
				Finalizers: []string{
					"kubeocean.io/finalizer-test-cluster-id",
					"other-finalizer",
				},
			},
		}

		// Add the configmap to the client
		err := virtualClient.Create(context.Background(), virtualConfigMap)
		require.NoError(t, err)

		// Test removing the ClusterID finalizer
		err = topcommon.RemoveSyncedResourceFinalizerAndLabels(context.Background(), virtualConfigMap, virtualClient, reconciler.Log, reconciler.ClusterID)
		result := ctrl.Result{}
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify the ClusterID finalizer is removed but other finalizer remains
		updatedConfigMap := &corev1.ConfigMap{}
		err = virtualClient.Get(context.Background(), types.NamespacedName{Name: "test-config", Namespace: "test-ns"}, updatedConfigMap)
		require.NoError(t, err)

		assert.NotContains(t, updatedConfigMap.Finalizers, "kubeocean.io/finalizer-test-cluster-id")
		assert.Contains(t, updatedConfigMap.Finalizers, "other-finalizer")
	})
}
