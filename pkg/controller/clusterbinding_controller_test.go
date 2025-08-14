package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestClusterBindingReconciler_validateClusterBinding(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name           string
		clusterBinding *cloudv1beta1.ClusterBinding
		wantErr        bool
		errMsg         string
	}{
		{
			name: "valid cluster binding",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
					MountNamespace: "test-mount",
				},
			},
			wantErr: false,
		},
		{
			name: "valid cluster binding with service namespaces",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster-2",
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
					MountNamespace:    "test-mount",
					ServiceNamespaces: []string{"default", "kube-system"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing cluster ID",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
					MountNamespace: "test-mount",
				},
			},
			wantErr: true,
			errMsg:  "clusterID is required",
		},
		{
			name: "missing secret name",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
					SecretRef: corev1.SecretReference{
						Name:      "",
						Namespace: "test-namespace",
					},
					MountNamespace: "test-mount",
				},
			},
			wantErr: true,
			errMsg:  "secretRef.name is required",
		},
		{
			name: "missing secret namespace",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "",
					},
					MountNamespace: "test-mount",
				},
			},
			wantErr: true,
			errMsg:  "secretRef.namespace is required",
		},
		{
			name: "missing mount namespace",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
					MountNamespace: "",
				},
			},
			wantErr: true,
			errMsg:  "mountNamespace is required",
		},
		{
			name: "empty service namespace",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
					MountNamespace:    "test-mount",
					ServiceNamespaces: []string{"valid-ns", "", "another-valid-ns"},
				},
			},
			wantErr: true,
			errMsg:  "serviceNamespaces[1] cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			reconciler := &ClusterBindingReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Log:      zap.New(zap.UseDevMode(true)),
				Recorder: record.NewFakeRecorder(10),
			}

			err := reconciler.validateClusterBinding(tt.clusterBinding)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestClusterBindingReconciler_readKubeconfigSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name      string
		secret    *corev1.Secret
		secretRef corev1.SecretReference
		wantErr   bool
		errMsg    string
	}{
		{
			name: "valid secret with kubeconfig",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"kubeconfig": []byte("fake kubeconfig content"),
				},
			},
			secretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "test-namespace",
			},
			wantErr: false,
		},
		{
			name:   "secret not found",
			secret: nil,
			secretRef: corev1.SecretReference{
				Name:      "nonexistent-secret",
				Namespace: "test-namespace",
			},
			wantErr: true,
			errMsg:  "kubeconfig secret test-namespace/nonexistent-secret not found",
		},
		{
			name: "secret missing kubeconfig key",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"other-key": []byte("other content"),
				},
			},
			secretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "test-namespace",
			},
			wantErr: true,
			errMsg:  "kubeconfig key not found in secret",
		},
		{
			name: "empty kubeconfig data",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"kubeconfig": []byte(""),
				},
			},
			secretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "test-namespace",
			},
			wantErr: true,
			errMsg:  "kubeconfig data is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []client.Object
			if tt.secret != nil {
				objects = append(objects, tt.secret)
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			reconciler := &ClusterBindingReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Log:      zap.New(zap.UseDevMode(true)),
				Recorder: record.NewFakeRecorder(10),
			}

			data, err := reconciler.readKubeconfigSecret(context.Background(), tt.secretRef)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, data)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, data)
			}
		})
	}
}

func TestClusterBindingReconciler_finalizerMethods(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	reconciler := &ClusterBindingReconciler{
		Scheme: scheme,
		Log:    zap.New(zap.UseDevMode(true)),
	}

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
		},
	}

	// Test hasFinalizer - should be false initially
	assert.False(t, reconciler.hasFinalizer(clusterBinding))

	// Test addFinalizer
	reconciler.addFinalizer(clusterBinding)
	assert.True(t, reconciler.hasFinalizer(clusterBinding))
	assert.Contains(t, clusterBinding.Finalizers, ClusterBindingFinalizer)

	// Test removeFinalizer
	reconciler.removeFinalizer(clusterBinding)
	assert.False(t, reconciler.hasFinalizer(clusterBinding))
	assert.NotContains(t, clusterBinding.Finalizers, ClusterBindingFinalizer)
}

func TestClusterBindingReconciler_Reconcile_Integration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create a ClusterBinding with valid configuration
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "default",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
			SecretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "default",
			},
			MountNamespace: "test-mount",
		},
	}

	// Create a valid kubeconfig secret (with dummy data for testing)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"kubeconfig": []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://invalid-test-server
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
current-context: test-context
users:
- name: test-user
  user:
    token: fake-token
`),
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(clusterBinding, secret).
		WithStatusSubresource(clusterBinding).
		Build()

	reconciler := &ClusterBindingReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Log:      zap.New(zap.UseDevMode(true)),
		Recorder: record.NewFakeRecorder(100),
	}

	// Test reconcile
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      clusterBinding.Name,
			Namespace: clusterBinding.Namespace,
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Should not return an error, and should not requeue (first call adds finalizer)
	assert.NoError(t, err)
	assert.False(t, result.Requeue) //nolint:staticcheck
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	// Verify the ClusterBinding was updated with finalizer
	var updatedBinding cloudv1beta1.ClusterBinding
	err = fakeClient.Get(context.Background(), req.NamespacedName, &updatedBinding)
	assert.NoError(t, err)
	assert.Contains(t, updatedBinding.Finalizers, ClusterBindingFinalizer)

	// Run reconcile again to test the full flow (after finalizer is added)
	_, err2 := reconciler.Reconcile(context.Background(), req)
	assert.NoError(t, err2)

	err = fakeClient.Get(context.Background(), req.NamespacedName, &updatedBinding)
	assert.NoError(t, err)
	var actulPending cloudv1beta1.ClusterBindingPhase = "Pending"
	assert.Equal(t, updatedBinding.Status.Phase, actulPending)

	_, err3 := reconciler.Reconcile(context.Background(), req)
	// Third reconcile should handle the validation and connectivity check
	// This will fail connectivity (expected with fake kubeconfig) and return error
	assert.Error(t, err3)
	assert.Contains(t, err3.Error(), "cluster connectivity test failed")
}
func TestClusterBindingReconciler_SyncerCreation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	// Create a fake client
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create the reconciler
	reconciler := &ClusterBindingReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Log:      zap.New(zap.UseDevMode(true)),
		Recorder: record.NewFakeRecorder(100),
	}

	// Create a test ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			SecretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "default",
			},
			MountNamespace: "test-mount",
		},
	}

	ctx := context.Background()

	// Test getSyncerName
	syncerName := reconciler.getSyncerName(clusterBinding)
	expectedName := "tapestry-syncer-test-binding"
	assert.Equal(t, expectedName, syncerName)

	// Test getSyncerLabels
	labels := reconciler.getSyncerLabels(clusterBinding)
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":       "tapestry-syncer",
		"app.kubernetes.io/instance":   "test-binding",
		"app.kubernetes.io/component":  "syncer",
		"app.kubernetes.io/part-of":    "tapestry",
		"app.kubernetes.io/managed-by": "tapestry-manager",
		"tapestry.io/cluster-binding":  "test-binding",
	}
	assert.Equal(t, expectedLabels, labels)

	// Create test template data
	templateFiles := map[string]string{
		"serviceAccountName": "tapestry-syncer",
		"roleName":           "tapestry-syncer",
		"roleBindingName":    "tapestry-syncer",
		"syncerNamespace":    "tapestry-system",
		"deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{.DeploymentName}}"
  namespace: "{{.Namespace}}"
spec:
  replicas: 2
  selector:
    matchLabels:
      app: syncer
  template:
    metadata:
      labels:
        app: syncer
    spec:
      serviceAccountName: "{{.ServiceAccountName}}"
      containers:
      - name: syncer
        image: "tapestry-syncer:latest"`,
	}

	// Test prepareSyncerTemplateData
	templateData := reconciler.prepareSyncerTemplateData(clusterBinding, templateFiles)
	assert.Equal(t, "test-binding", templateData.ClusterBindingName)
	assert.Equal(t, "default", templateData.Namespace)
	assert.Equal(t, expectedName, templateData.DeploymentName)
	assert.Equal(t, "tapestry-syncer", templateData.ServiceAccountName)
	assert.Equal(t, "tapestry-system", templateData.SyncerNamespace)

	// Test createSyncerResourceFromTemplate directly
	err := reconciler.createSyncerResourceFromTemplate(ctx, clusterBinding, templateFiles, templateData, "deployment.yaml")
	assert.NoError(t, err)

	// Verify that Deployment was created (RBAC resources are shared and not created per ClusterBinding)
	var createdDeployment appsv1.Deployment
	err = fakeClient.Get(ctx, types.NamespacedName{Name: expectedName, Namespace: "default"}, &createdDeployment)
	assert.NoError(t, err)
	assert.Equal(t, expectedName, createdDeployment.Name)
	assert.Equal(t, int32(2), *createdDeployment.Spec.Replicas)

	// Verify that the deployment uses the shared ServiceAccount
	assert.Equal(t, "tapestry-syncer", createdDeployment.Spec.Template.Spec.ServiceAccountName)
}
func TestClusterBindingReconciler_deleteSyncerResources(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	tests := []struct {
		name              string
		existingResources []client.Object
		clusterBinding    *cloudv1beta1.ClusterBinding
		expectedStatus    *ResourceCleanupStatus
	}{
		{
			name: "delete all syncer resources successfully",
			existingResources: []client.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer-test-binding",
						Namespace: "tapestry-system",
					},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer",
						Namespace: "tapestry-system",
					},
				},
				&rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer",
						Namespace: "tapestry-system",
					},
				},
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer",
						Namespace: "tapestry-system",
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
				},
			},
			expectedStatus: &ResourceCleanupStatus{
				Deployment:     true,
				ServiceAccount: true,
				Role:           true,
				RoleBinding:    true,
			},
		},
		{
			name: "handle missing resources gracefully",
			existingResources: []client.Object{
				// Only deployment exists, others are missing
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer-test-binding",
						Namespace: "tapestry-system",
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
				},
			},
			expectedStatus: &ResourceCleanupStatus{
				Deployment:     true,
				ServiceAccount: true, // Should be true even if not found
				Role:           true,
				RoleBinding:    true,
			},
		},
		{
			name: "fallback to default names",
			existingResources: []client.Object{
				// Resources with base default name instead of configured names
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer", // Base default name
						Namespace: "tapestry-system",
					},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer",
						Namespace: "tapestry-system",
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
				},
			},
			expectedStatus: &ResourceCleanupStatus{
				Deployment:     true,
				ServiceAccount: true,
				Role:           true, // Should be true even if not found
				RoleBinding:    true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.existingResources...).
				Build()

			reconciler := &ClusterBindingReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Log:      zap.New(zap.UseDevMode(true)),
				Recorder: record.NewFakeRecorder(100),
			}

			ctx := context.Background()
			status, err := reconciler.deleteSyncerResources(ctx, tt.clusterBinding)

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, status)

			// Verify resources are actually deleted
			for _, resource := range tt.existingResources {
				key := client.ObjectKeyFromObject(resource)
				// Create a new instance of the same type to avoid modifying the original
				newResource := resource.DeepCopyObject().(client.Object)
				err := fakeClient.Get(ctx, key, newResource)
				assert.True(t, apierrors.IsNotFound(err), "Resource should be deleted: %s/%s", resource.GetNamespace(), resource.GetName())
			}
		})
	}
}

func TestClusterBindingReconciler_handleDeletion(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	tests := []struct {
		name              string
		existingResources []client.Object
		clusterBinding    *cloudv1beta1.ClusterBinding
		expectRequeue     bool
	}{
		{
			name: "successful deletion with all resources present",
			existingResources: []client.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer-test-binding",
						Namespace: "tapestry-system",
					},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer",
						Namespace: "tapestry-system",
					},
				},
				&rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer",
						Namespace: "tapestry-system",
					},
				},
				&rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tapestry-syncer",
						Namespace: "tapestry-system",
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-binding",
					Namespace:         "default",
					Finalizers:        []string{ClusterBindingFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
			},
			expectRequeue: false,
		},
		{
			name:              "successful deletion with no resources present",
			existingResources: []client.Object{},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-binding",
					Namespace:         "default",
					Finalizers:        []string{ClusterBindingFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
			},
			expectRequeue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(append(tt.existingResources, tt.clusterBinding)...).
				Build()

			reconciler := &ClusterBindingReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Log:      zap.New(zap.UseDevMode(true)),
				Recorder: record.NewFakeRecorder(100),
			}

			ctx := context.Background()
			result, err := reconciler.handleDeletion(ctx, tt.clusterBinding)

			assert.NoError(t, err)
			if tt.expectRequeue {
				assert.True(t, result.RequeueAfter > 0, "Should requeue when cleanup is not complete")
			} else {
				assert.Equal(t, ctrl.Result{}, result, "Should not requeue when cleanup is complete")
			}

			// Verify finalizer is removed when cleanup is complete
			if !tt.expectRequeue {
				var updatedBinding cloudv1beta1.ClusterBinding
				err := fakeClient.Get(ctx, client.ObjectKeyFromObject(tt.clusterBinding), &updatedBinding)
				// When cleanup is complete, the ClusterBinding should be deleted (finalizer removed)
				// So we expect a NotFound error
				if err != nil {
					assert.True(t, apierrors.IsNotFound(err), "ClusterBinding should be deleted when cleanup is complete")
				} else {
					assert.Empty(t, updatedBinding.Finalizers, "Finalizer should be removed after successful cleanup")
				}
			}
		})
	}
}

func TestClusterBindingReconciler_isCleanupComplete(t *testing.T) {
	reconciler := &ClusterBindingReconciler{}

	tests := []struct {
		name     string
		status   *ResourceCleanupStatus
		expected bool
	}{
		{
			name: "all resources cleaned up",
			status: &ResourceCleanupStatus{
				Deployment:     true,
				ServiceAccount: true,
				Role:           true,
				RoleBinding:    true,
			},
			expected: true,
		},
		{
			name: "deployment not cleaned up",
			status: &ResourceCleanupStatus{
				Deployment:     false,
				ServiceAccount: true,
				Role:           true,
				RoleBinding:    true,
			},
			expected: false,
		},
		{
			name: "rbac resources not cleaned up",
			status: &ResourceCleanupStatus{
				Deployment:     true,
				ServiceAccount: false,
				Role:           false,
				RoleBinding:    false,
			},
			expected: false,
		},
		{
			name: "no resources cleaned up",
			status: &ResourceCleanupStatus{
				Deployment:     false,
				ServiceAccount: false,
				Role:           false,
				RoleBinding:    false,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.isCleanupComplete(tt.status)
			assert.Equal(t, tt.expected, result)
		})
	}
}
