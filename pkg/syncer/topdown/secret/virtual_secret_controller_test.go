package secret

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

const (
	// testClusterIDValue is the value used for cluster ID labels in tests
	testClusterIDValue = "true"
	// testVirtualNamespace is the virtual namespace used in tests
	testVirtualNamespace = "virtual-ns"
)

func TestVirtualSecretReconciler_Reconcile(t *testing.T) {
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

	// Helper function to add ClusterID label to virtual Secret
	addClusterIDLabel := func(secret *corev1.Secret) {
		if secret != nil && secret.Labels != nil {
			secret.Labels["kubeocean.io/synced-by-test-cluster-id"] = testClusterIDValue
		}
	}

	// Helper function to create a test Secret
	// For virtual Secret: otherName is the physical name
	// For physical Secret: otherName is the virtual name
	createTestSecret := func(name, namespace, otherName, otherNamespace string, data map[string][]byte, isVirtual bool) *corev1.Secret {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				},
				Annotations: make(map[string]string),
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}
		if isVirtual {
			secret.Annotations[cloudv1beta1.AnnotationPhysicalName] = otherName
			secret.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] = otherNamespace
		} else {
			secret.Annotations[cloudv1beta1.AnnotationVirtualName] = otherName
		}
		return secret
	}

	tests := []struct {
		name           string
		virtualSecret  *corev1.Secret
		physicalSecret *corev1.Secret
		expectedResult ctrl.Result
		expectError    bool
		validateFunc   func(t *testing.T, virtualClient, physicalClient client.Client)
	}{
		{
			name:           "virtual secret not found",
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual secret not managed by kubeocean",
			virtualSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: testVirtualNamespace,
					Labels: map[string]string{
						"app": "test",
					},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual secret not managed by this cluster",
			virtualSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: testVirtualNamespace,
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:               cloudv1beta1.LabelManagedByValue,
						"kubeocean.io/synced-by-other-cluster-id": testClusterIDValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName:      "physical-secret",
						cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
					},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual secret with no physical name label",
			virtualSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: testVirtualNamespace,
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
		},
		{
			name: "virtual secret being deleted",
			virtualSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-secret",
					Namespace:         testVirtualNamespace,
					DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
					Finalizers:        []string{"test-finalizer"},
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName:      "physical-secret",
						cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
					},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			},
			physicalSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "physical-secret",
					Namespace: "physical-ns",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-secret",
					},
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical secret should be deleted
				secret := &corev1.Secret{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-secret", Namespace: "physical-ns",
				}, secret)
				assert.True(t, apierrors.IsNotFound(err))
			},
		},
		{
			name: "virtual secret exists but physical secret doesn't exist",
			virtualSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: testVirtualNamespace,
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName:      "physical-secret",
						cloudv1beta1.AnnotationPhysicalNamespace: "physical-ns",
					},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			},
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical secret should be created
				secret := &corev1.Secret{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-secret", Namespace: "physical-ns",
				}, secret)
				require.NoError(t, err)
				assert.Equal(t, []byte("value"), secret.Data["key"])
				assert.Equal(t, corev1.SecretTypeOpaque, secret.Type)
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, secret.Labels[cloudv1beta1.LabelManagedBy])
			},
		},
		{
			name:           "virtual secret and physical secret both exist and are in sync",
			virtualSecret:  createTestSecret("test-secret", testVirtualNamespace, "physical-secret", "physical-ns", map[string][]byte{"key": []byte("value")}, true),
			physicalSecret: createTestSecret("physical-secret", "physical-ns", "test-secret", testVirtualNamespace, map[string][]byte{"key": []byte("value")}, false),
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical secret should remain unchanged
				secret := &corev1.Secret{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-secret", Namespace: "physical-ns",
				}, secret)
				require.NoError(t, err)
				assert.Equal(t, []byte("value"), secret.Data["key"])
			},
		},
		{
			name:           "virtual secret and physical secret both exist but need update",
			virtualSecret:  createTestSecret("test-secret", testVirtualNamespace, "physical-secret", "physical-ns", map[string][]byte{"key": []byte("new-value")}, true),
			physicalSecret: createTestSecret("physical-secret", "physical-ns", "test-secret", testVirtualNamespace, map[string][]byte{"key": []byte("old-value")}, false),
			expectedResult: ctrl.Result{},
			expectError:    false,
			validateFunc: func(t *testing.T, virtualClient, physicalClient client.Client) {
				// Physical secret should be updated
				secret := &corev1.Secret{}
				err := physicalClient.Get(context.TODO(), types.NamespacedName{
					Name: "physical-secret", Namespace: "physical-ns",
				}, secret)
				require.NoError(t, err)
				assert.Equal(t, []byte("new-value"), secret.Data["key"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Add ClusterID label to virtual Secret if it exists and has managed-by label
			if tt.virtualSecret != nil && tt.virtualSecret.Labels != nil &&
				tt.virtualSecret.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
				addClusterIDLabel(tt.virtualSecret)
			}

			// Setup clients
			var virtualObjects []client.Object
			var physicalObjects []client.Object

			if tt.virtualSecret != nil {
				virtualObjects = append(virtualObjects, tt.virtualSecret)
			}
			if tt.physicalSecret != nil {
				physicalObjects = append(physicalObjects, tt.physicalSecret)
			}

			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjects...).Build()
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjects...).Build()
			physicalK8sClient := fake.NewSimpleClientset()

			// Create reconciler
			reconciler := &VirtualSecretReconciler{
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
						if tt.virtualSecret != nil {
							return tt.virtualSecret.Name
						}
						return "test-secret"
					}(),
					Namespace: func() string {
						if tt.virtualSecret != nil {
							return tt.virtualSecret.Namespace
						}
						return testVirtualNamespace
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

func TestVirtualSecretReconciler_CheckPhysicalSecretExists(t *testing.T) {
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

	t.Run("physical secret exists in cache", func(t *testing.T) {
		physicalSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "physical-ns",
			},
		}

		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalSecret).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualSecretReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			Scheme:            scheme,
			ClusterBinding:    clusterBinding,
			Log:               zap.New(),
		}

		exists, secret, err := reconciler.checkPhysicalSecretExists(context.TODO(), "physical-ns", "test-secret")
		assert.NoError(t, err)
		assert.True(t, exists)
		assert.NotNil(t, secret)
		assert.Equal(t, "test-secret", secret.Name)
	})

	t.Run("physical secret not found", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalK8sClient := fake.NewSimpleClientset()

		reconciler := &VirtualSecretReconciler{
			VirtualClient:     virtualClient,
			PhysicalClient:    physicalClient,
			PhysicalK8sClient: physicalK8sClient,
			Scheme:            scheme,
			ClusterBinding:    clusterBinding,
			Log:               zap.New(),
		}

		exists, secret, err := reconciler.checkPhysicalSecretExists(context.TODO(), "physical-ns", "non-existent-secret")
		assert.NoError(t, err)
		assert.False(t, exists)
		assert.Nil(t, secret)
	})
}

// TestVirtualSecretReconciler_ClusterIDFunctionality tests ClusterID related functionality
func TestVirtualSecretReconciler_ClusterIDFunctionality(t *testing.T) {
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

		reconciler := &VirtualSecretReconciler{
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

		reconciler := &VirtualSecretReconciler{
			VirtualClient:  virtualClient,
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test"),
			ClusterID:      "test-cluster-id",
		}

		virtualSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "test-ns",
				Finalizers: []string{
					"kubeocean.io/finalizer-test-cluster-id",
					"other-finalizer",
				},
			},
		}

		// Add the secret to the client
		err := virtualClient.Create(context.Background(), virtualSecret)
		require.NoError(t, err)

		// Test removing the ClusterID finalizer
		err = topcommon.RemoveSyncedResourceFinalizerAndLabels(context.Background(), virtualSecret, virtualClient, reconciler.Log, reconciler.ClusterID)
		result := ctrl.Result{}
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify the ClusterID finalizer is removed but other finalizer remains
		updatedSecret := &corev1.Secret{}
		err = virtualClient.Get(context.Background(), types.NamespacedName{Name: "test-secret", Namespace: "test-ns"}, updatedSecret)
		require.NoError(t, err)

		assert.NotContains(t, updatedSecret.Finalizers, "kubeocean.io/finalizer-test-cluster-id")
		assert.Contains(t, updatedSecret.Finalizers, "other-finalizer")
	})
}

// TestVirtualSecretReconciler_WithEventFilter tests the WithEventFilter functionality
func TestVirtualSecretReconciler_WithEventFilter(t *testing.T) {
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

	t.Run("event filter with ClusterID label", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &VirtualSecretReconciler{
			VirtualClient:  virtualClient,
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test"),
			ClusterID:      "test-cluster-id",
		}

		// Test secret managed by this cluster
		secretManagedByThisCluster := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "test-ns",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy:              cloudv1beta1.LabelManagedByValue,
					"kubeocean.io/synced-by-test-cluster-id": testClusterIDValue,
				},
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalName: "physical-secret",
				},
			},
		}

		// Test secret managed by other cluster
		secretManagedByOtherCluster := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret-other",
				Namespace: "test-ns",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy:               cloudv1beta1.LabelManagedByValue,
					"kubeocean.io/synced-by-other-cluster-id": testClusterIDValue,
				},
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalName: "physical-secret-other",
				},
			},
		}

		// Test secret without ClusterID label
		secretWithoutClusterID := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret-no-cluster",
				Namespace: "test-ns",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				},
				Annotations: map[string]string{
					cloudv1beta1.AnnotationPhysicalName: "physical-secret-no-cluster",
				},
			},
		}

		// Test secret not managed by kubeocean
		secretNotManagedByKubeocean := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret-not-kubeocean",
				Namespace: "test-ns",
				Labels: map[string]string{
					"app": "test",
				},
			},
		}

		// Test secret without physical name annotation
		secretWithoutPhysicalName := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret-no-physical",
				Namespace: "test-ns",
				Labels: map[string]string{
					cloudv1beta1.LabelManagedBy:              cloudv1beta1.LabelManagedByValue,
					"kubeocean.io/synced-by-test-cluster-id": testClusterIDValue,
				},
			},
		}

		// Create a mock predicate function that simulates the WithEventFilter logic
		predicateFunc := func(obj client.Object) bool {
			secret := obj.(*corev1.Secret)

			// Only sync secrets managed by Kubeocean
			if secret.Labels == nil || secret.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
				return false
			}

			// Only sync secrets with physical name label
			if secret.Annotations == nil || secret.Annotations[cloudv1beta1.AnnotationPhysicalName] == "" {
				return false
			}

			// Only sync secrets managed by this cluster
			managedByClusterIDLabel := topcommon.GetManagedByClusterIDLabel(reconciler.ClusterID)
			return secret.Labels[managedByClusterIDLabel] == testClusterIDValue
		}

		// Test the predicate function
		assert.True(t, predicateFunc(secretManagedByThisCluster), "Secret managed by this cluster should be accepted")
		assert.False(t, predicateFunc(secretManagedByOtherCluster), "Secret managed by other cluster should be rejected")
		assert.False(t, predicateFunc(secretWithoutClusterID), "Secret without ClusterID label should be rejected")
		assert.False(t, predicateFunc(secretNotManagedByKubeocean), "Secret not managed by kubeocean should be rejected")
		assert.False(t, predicateFunc(secretWithoutPhysicalName), "Secret without physical name should be rejected")
	})
}
