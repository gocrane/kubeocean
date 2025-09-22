package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
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

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
		},
	}

	// Test hasFinalizer - should be false initially
	assert.False(t, controllerutil.ContainsFinalizer(clusterBinding, cloudv1beta1.ClusterBindingManagerFinalizer))

	// Test addFinalizer
	controllerutil.AddFinalizer(clusterBinding, cloudv1beta1.ClusterBindingManagerFinalizer)
	assert.True(t, controllerutil.ContainsFinalizer(clusterBinding, cloudv1beta1.ClusterBindingManagerFinalizer))
	assert.Contains(t, clusterBinding.Finalizers, cloudv1beta1.ClusterBindingManagerFinalizer)

	// Test removeFinalizer
	controllerutil.RemoveFinalizer(clusterBinding, cloudv1beta1.ClusterBindingManagerFinalizer)
	assert.False(t, controllerutil.ContainsFinalizer(clusterBinding, cloudv1beta1.ClusterBindingManagerFinalizer))
	assert.NotContains(t, clusterBinding.Finalizers, cloudv1beta1.ClusterBindingManagerFinalizer)
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
	assert.Contains(t, updatedBinding.Finalizers, cloudv1beta1.ClusterBindingManagerFinalizer)

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
			ClusterID: "test-binding",
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
	expectedName := "kubeocean-syncer-test-binding"
	assert.Equal(t, expectedName, syncerName)

	// Test getSyncerLabels
	labels := reconciler.getSyncerLabels(clusterBinding)
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":         "kubeocean-syncer",
		"app.kubernetes.io/instance":     "test-binding",
		"app.kubernetes.io/component":    "syncer",
		"app.kubernetes.io/part-of":      "kubeocean",
		"app.kubernetes.io/managed-by":   "kubeocean-manager",
		cloudv1beta1.LabelClusterBinding: "test-binding",
	}
	assert.Equal(t, expectedLabels, labels)

	// Create test template data
	templateFiles := map[string]string{
		"serviceAccountName": "kubeocean-syncer",
		"roleName":           "kubeocean-syncer",
		"roleBindingName":    "kubeocean-syncer",
		"syncerNamespace":    "kubeocean-system",
		"deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{.DeploymentName}}"
  namespace: "{{.SyncerNamespace}}"
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
        image: "kubeocean-syncer:latest"`,
	}

	// Test prepareSyncerTemplateData
	templateData := reconciler.prepareSyncerTemplateData(clusterBinding, templateFiles)
	assert.Equal(t, "test-binding", templateData.ClusterBindingName)
	assert.Equal(t, expectedName, templateData.DeploymentName)
	assert.Equal(t, "kubeocean-syncer", templateData.ServiceAccountName)
	assert.Equal(t, "kubeocean-system", templateData.SyncerNamespace)

	// Test createSyncerResourceFromTemplate directly
	err := reconciler.createSyncerResourceFromTemplate(ctx, clusterBinding, templateFiles, templateData, "deployment.yaml")
	assert.NoError(t, err)

	// Verify that Deployment was created (RBAC resources are shared and not created per ClusterBinding)
	var createdDeployment appsv1.Deployment
	err = fakeClient.Get(ctx, types.NamespacedName{Name: expectedName, Namespace: templateData.SyncerNamespace}, &createdDeployment)
	assert.NoError(t, err)
	assert.Equal(t, expectedName, createdDeployment.Name)
	if createdDeployment.Spec.Replicas != nil {
		assert.Equal(t, int32(2), *createdDeployment.Spec.Replicas)
	} else {
		t.Error("Deployment Spec.Replicas should not be nil")
	}

	// Verify that the deployment uses the shared ServiceAccount
	assert.Equal(t, "kubeocean-syncer", createdDeployment.Spec.Template.Spec.ServiceAccountName)
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
			name: "delete deployment successfully, preserve RBAC resources",
			existingResources: []client.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeocean-syncer-test-binding",
						Namespace: "kubeocean-system",
					},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeocean-syncer",
						Namespace: "kubeocean-system",
					},
				},
				&rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kubeocean-syncer",
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kubeocean-syncer",
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-binding",
				},
			},
			expectedStatus: &ResourceCleanupStatus{
				Deployment:         true,
				ServiceAccount:     true,
				ClusterRole:        true,
				ClusterRoleBinding: true,
			},
		},
		{
			name: "handle missing resources gracefully",
			existingResources: []client.Object{
				// Only deployment exists, others are missing
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeocean-syncer-test-binding",
						Namespace: "kubeocean-system",
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-binding",
				},
			},
			expectedStatus: &ResourceCleanupStatus{
				Deployment:         true,
				ServiceAccount:     true, // Should be true even if not found
				ClusterRole:        true,
				ClusterRoleBinding: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary directory for templates
			tempDir := t.TempDir()

			// Set environment variable to use temp directory
			t.Setenv("KUBEOCEAN_SYNCER_TEMPLATE_DIR", tempDir)

			// Create mock template files
			deploymentTemplate := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{.DeploymentName}}"
  namespace: "{{.SyncerNamespace}}"
spec:
  replicas: 1`

			err := os.WriteFile(filepath.Join(tempDir, "deployment.yaml"), []byte(deploymentTemplate), 0644)
			require.NoError(t, err)

			err = os.WriteFile(filepath.Join(tempDir, "syncerNamespace"), []byte("kubeocean-system"), 0644)
			require.NoError(t, err)

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
			deleted, err := reconciler.deleteSyncerResources(ctx, tt.clusterBinding)

			assert.NoError(t, err)
			// Since deleteSyncerResources now returns bool, we check if deployment was deleted
			expectedDeleted := tt.expectedStatus.Deployment
			assert.Equal(t, expectedDeleted, deleted)

			// Verify resources are handled correctly
			for _, resource := range tt.existingResources {
				key := client.ObjectKeyFromObject(resource)
				// Create a new instance of the same type to avoid modifying the original
				newResource := resource.DeepCopyObject().(client.Object)
				err := fakeClient.Get(ctx, key, newResource)

				// Only Deployment should be deleted, RBAC resources should remain
				switch resource.(type) {
				case *appsv1.Deployment:
					assert.True(t, apierrors.IsNotFound(err), "Deployment should be deleted: %s/%s", resource.GetNamespace(), resource.GetName())
				case *corev1.ServiceAccount, *rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding:
					assert.NoError(t, err, "RBAC resource should be preserved: %s/%s", resource.GetNamespace(), resource.GetName())
				}
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
						Name:      "kubeocean-syncer-test-binding",
						Namespace: "kubeocean-system",
					},
				},
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeocean-syncer",
						Namespace: "kubeocean-system",
					},
				},
				&rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kubeocean-syncer",
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kubeocean-syncer",
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-binding",
					Namespace:         "default",
					Finalizers:        []string{cloudv1beta1.ClusterBindingManagerFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-binding",
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
					Finalizers:        []string{cloudv1beta1.ClusterBindingManagerFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-binding",
				},
			},
			expectRequeue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary directories for templates
			syncerTempDir := t.TempDir()
			proxierTempDir := t.TempDir()

			// Set environment variables to use temp directories
			t.Setenv("KUBEOCEAN_SYNCER_TEMPLATE_DIR", syncerTempDir)
			t.Setenv("KUBEOCEAN_PROXIER_TEMPLATE_DIR", proxierTempDir)

			// Create mock syncer template files
			syncerDeploymentTemplate := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{.DeploymentName}}"
  namespace: "{{.SyncerNamespace}}"
spec:
  replicas: 1`

			err := os.WriteFile(filepath.Join(syncerTempDir, "deployment.yaml"), []byte(syncerDeploymentTemplate), 0644)
			require.NoError(t, err)

			err = os.WriteFile(filepath.Join(syncerTempDir, "syncerNamespace"), []byte("kubeocean-system"), 0644)
			require.NoError(t, err)

			// Create mock proxier template files
			proxierDeploymentTemplate := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{.DeploymentName}}"
  namespace: "{{.ProxierNamespace}}"
spec:
  replicas: 1`

			proxierServiceTemplate := `apiVersion: v1
kind: Service
metadata:
  name: "{{.ServiceName}}"
  namespace: "{{.ProxierNamespace}}"
spec:
  ports:
  - port: 80`

			err = os.WriteFile(filepath.Join(proxierTempDir, "deployment.yaml"), []byte(proxierDeploymentTemplate), 0644)
			require.NoError(t, err)

			err = os.WriteFile(filepath.Join(proxierTempDir, "service.yaml"), []byte(proxierServiceTemplate), 0644)
			require.NoError(t, err)

			err = os.WriteFile(filepath.Join(proxierTempDir, "proxierNamespace"), []byte("kubeocean-system"), 0644)
			require.NoError(t, err)

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
				Deployment:         true,
				ServiceAccount:     true,
				ClusterRole:        true,
				ClusterRoleBinding: true,
			},
			expected: true,
		},
		{
			name: "deployment not cleaned up",
			status: &ResourceCleanupStatus{
				Deployment:         false,
				ServiceAccount:     true,
				ClusterRole:        true,
				ClusterRoleBinding: true,
			},
			expected: false,
		},
		{
			name: "rbac resources not cleaned up",
			status: &ResourceCleanupStatus{
				Deployment:         true,
				ServiceAccount:     false,
				ClusterRole:        false,
				ClusterRoleBinding: false,
			},
			expected: false,
		},
		{
			name: "no resources cleaned up",
			status: &ResourceCleanupStatus{
				Deployment:         false,
				ServiceAccount:     false,
				ClusterRole:        false,
				ClusterRoleBinding: false,
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

func TestClusterBindingReconciler_createOrUpdateResource_DeploymentSkipsUpdate(t *testing.T) {
	// Create a fake client with scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	// Create a deployment that already exists
	existingDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "default",
			Labels:    map[string]string{"original": "true"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{1}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "original-image:v1",
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingDeployment).
		Build()

	reconciler := &ClusterBindingReconciler{
		Client: fakeClient,
		Log:    ctrl.Log.WithName("test"),
		Scheme: scheme,
	}

	// Create a new deployment object with different specs (simulating an update)
	newDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "default",
			Labels:    map[string]string{"updated": "true"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{3}[0], // Different from original
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "updated-image:v2", // Different from original
						},
					},
				},
			},
		},
	}

	// Set the GVK for the deployment
	newDeployment.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "Deployment",
	})

	ctx := context.Background()

	// Call createOrUpdateResource - should skip update for Deployment
	err := reconciler.createOrUpdateResource(ctx, newDeployment)
	assert.NoError(t, err)

	// Verify the original deployment was not updated
	var actualDeployment appsv1.Deployment
	err = fakeClient.Get(ctx, client.ObjectKey{
		Name:      "test-deployment",
		Namespace: "default",
	}, &actualDeployment)
	assert.NoError(t, err)

	// Check that the original values are preserved (not updated)
	assert.Equal(t, "true", actualDeployment.Labels["original"])
	_, hasUpdatedLabel := actualDeployment.Labels["updated"]
	assert.False(t, hasUpdatedLabel, "Updated label should not exist")
	assert.Equal(t, int32(1), *actualDeployment.Spec.Replicas)
	assert.Equal(t, "original-image:v1", actualDeployment.Spec.Template.Spec.Containers[0].Image)
}

func TestClusterBindingReconciler_createOrUpdateResource_NonDeploymentUpdates(t *testing.T) {
	// Create a fake client with scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Create a ConfigMap that already exists
	existingConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-configmap",
			Namespace: "default",
		},
		Data: map[string]string{
			"key": "original-value",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingConfigMap).
		Build()

	reconciler := &ClusterBindingReconciler{
		Client: fakeClient,
		Log:    ctrl.Log.WithName("test"),
		Scheme: scheme,
	}

	// Create a new ConfigMap object with different data (simulating an update)
	newConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-configmap",
			Namespace: "default",
		},
		Data: map[string]string{
			"key": "updated-value",
		},
	}

	// Set the GVK for the configmap
	newConfigMap.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "ConfigMap",
	})

	ctx := context.Background()

	// Call createOrUpdateResource - should update for non-Deployment resources
	err := reconciler.createOrUpdateResource(ctx, newConfigMap)
	assert.NoError(t, err)

	// Verify the ConfigMap was updated
	var actualConfigMap corev1.ConfigMap
	err = fakeClient.Get(ctx, client.ObjectKey{
		Name:      "test-configmap",
		Namespace: "default",
	}, &actualConfigMap)
	assert.NoError(t, err)

	// Check that the value was updated
	assert.Equal(t, "updated-value", actualConfigMap.Data["key"])
}

func TestClusterBindingReconciler_reconcileKubeoceanSyncer(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		clusterBinding *cloudv1beta1.ClusterBinding
		templateFiles  map[string]string
		wantErr        bool
		errMsg         string
	}{
		{
			name: "successful syncer reconciliation",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			templateFiles: map[string]string{
				"serviceAccountName": "kubeocean-syncer",
				"syncerNamespace":    "kubeocean-system",
				"deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{.DeploymentName}}"
  namespace: "{{.SyncerNamespace}}"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: syncer
  template:
    metadata:
      labels:
        app: syncer
    spec:
      containers:
      - name: syncer
        image: "kubeocean-syncer:latest"`,
			},
			wantErr: false,
		},
		{
			name: "missing deployment template",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			templateFiles: map[string]string{
				"serviceAccountName": "kubeocean-syncer",
				"syncerNamespace":    "kubeocean-system",
			},
			wantErr: true,
			errMsg:  "failed to read template file deployment.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary directory for templates
			tempDir := t.TempDir()
			t.Setenv("KUBEOCEAN_SYNCER_TEMPLATE_DIR", tempDir)

			// Create template files in temp directory
			for filename, content := range tt.templateFiles {
				err := os.WriteFile(filepath.Join(tempDir, filename), []byte(content), 0644)
				require.NoError(t, err)
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			reconciler := &ClusterBindingReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Log:      zap.New(zap.UseDevMode(true)),
				Recorder: record.NewFakeRecorder(10),
			}

			ctx := context.Background()
			err := reconciler.reconcileKubeoceanSyncer(ctx, tt.clusterBinding)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)

				// Verify deployment was created
				expectedName := fmt.Sprintf("kubeocean-syncer-%s", tt.clusterBinding.Spec.ClusterID)
				var deployment appsv1.Deployment
				err = fakeClient.Get(ctx, types.NamespacedName{
					Name:      expectedName,
					Namespace: tt.templateFiles["syncerNamespace"],
				}, &deployment)
				assert.NoError(t, err)
				assert.Equal(t, expectedName, deployment.Name)

				// Verify owner reference is set
				require.Len(t, deployment.OwnerReferences, 1)
				ownerRef := deployment.OwnerReferences[0]
				assert.Equal(t, tt.clusterBinding.Name, ownerRef.Name)
				assert.Equal(t, tt.clusterBinding.UID, ownerRef.UID)
				assert.True(t, *ownerRef.Controller)
			}
		})
	}
}

func TestClusterBindingReconciler_reconcileKubeoceanProxier(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		clusterBinding *cloudv1beta1.ClusterBinding
		templateFiles  map[string]string
		wantErr        bool
		errMsg         string
	}{
		{
			name: "successful proxier reconciliation",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			templateFiles: map[string]string{
				"serviceAccountName": "kubeocean-proxier",
				"proxierNamespace":   "kubeocean-system",
				"deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{.DeploymentName}}"
  namespace: "{{.ProxierNamespace}}"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: proxier
  template:
    metadata:
      labels:
        app: proxier
    spec:
      containers:
      - name: proxier
        image: "kubeocean-proxier:latest"`,
				"service.yaml": `apiVersion: v1
kind: Service
metadata:
  name: "{{.ServiceName}}"
  namespace: "{{.ProxierNamespace}}"
spec:
  selector:
    app: proxier
  ports:
  - port: 8080`,
			},
			wantErr: false,
		},
		{
			name: "proxier with TLS enabled",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-binding",
					Namespace: "default",
					UID:       "test-uid",
					Annotations: map[string]string{
						"kubeocean.io/logs-proxy-enabled":          "true",
						"kubeocean.io/logs-proxy-secret-name":      "tls-secret",
						"kubeocean.io/logs-proxy-secret-namespace": "custom-ns",
					},
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			templateFiles: map[string]string{
				"serviceAccountName": "kubeocean-proxier",
				"proxierNamespace":   "kubeocean-system",
				"deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{.DeploymentName}}"
  namespace: "{{.ProxierNamespace}}"
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: proxier
        image: "kubeocean-proxier:latest"
        {{if .TLSEnabled}}
        env:
        - name: TLS_ENABLED
          value: "true"
        - name: TLS_SECRET_NAME
          value: "{{.TLSSecretName}}"
        {{end}}`,
				"service.yaml": `apiVersion: v1
kind: Service
metadata:
  name: "{{.ServiceName}}"
  namespace: "{{.ProxierNamespace}}"
spec:
  ports:
  - port: 8080`,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary directory for templates
			tempDir := t.TempDir()
			t.Setenv("KUBEOCEAN_PROXIER_TEMPLATE_DIR", tempDir)

			// Create template files in temp directory
			for filename, content := range tt.templateFiles {
				err := os.WriteFile(filepath.Join(tempDir, filename), []byte(content), 0644)
				require.NoError(t, err)
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			reconciler := &ClusterBindingReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Log:      zap.New(zap.UseDevMode(true)),
				Recorder: record.NewFakeRecorder(10),
			}

			ctx := context.Background()
			err := reconciler.reconcileKubeoceanProxier(ctx, tt.clusterBinding)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)

				// Verify deployment was created
				expectedDeploymentName := fmt.Sprintf("%s-%s", DefaultProxierName, tt.clusterBinding.Spec.ClusterID)
				var deployment appsv1.Deployment
				err = fakeClient.Get(ctx, types.NamespacedName{
					Name:      expectedDeploymentName,
					Namespace: tt.templateFiles["proxierNamespace"],
				}, &deployment)
				assert.NoError(t, err)

				// Verify service was created
				expectedServiceName := fmt.Sprintf("%s-%s-svc", DefaultProxierName, tt.clusterBinding.Spec.ClusterID)
				var service corev1.Service
				err = fakeClient.Get(ctx, types.NamespacedName{
					Name:      expectedServiceName,
					Namespace: tt.templateFiles["proxierNamespace"],
				}, &service)
				assert.NoError(t, err)

				// Verify owner references are set
				require.Len(t, deployment.OwnerReferences, 1)
				assert.Equal(t, tt.clusterBinding.Name, deployment.OwnerReferences[0].Name)
				require.Len(t, service.OwnerReferences, 1)
				assert.Equal(t, tt.clusterBinding.Name, service.OwnerReferences[0].Name)
			}
		})
	}
}

func TestClusterBindingReconciler_prepareProxierTemplateData_TLS(t *testing.T) {
	tests := []struct {
		name           string
		clusterBinding *cloudv1beta1.ClusterBinding
		templateFiles  map[string]string
		expectedTLS    struct {
			enabled    bool
			secretName string
			secretNS   string
		}
	}{
		{
			name: "TLS disabled",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			templateFiles: map[string]string{
				"proxierNamespace": "kubeocean-system",
			},
			expectedTLS: struct {
				enabled    bool
				secretName string
				secretNS   string
			}{
				enabled:    false,
				secretName: "",
				secretNS:   "",
			},
		},
		{
			name: "TLS enabled with custom secret",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
					Annotations: map[string]string{
						"kubeocean.io/logs-proxy-enabled":          "true",
						"kubeocean.io/logs-proxy-secret-name":      "custom-tls-secret",
						"kubeocean.io/logs-proxy-secret-namespace": "custom-namespace",
					},
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			templateFiles: map[string]string{
				"proxierNamespace": "kubeocean-system",
			},
			expectedTLS: struct {
				enabled    bool
				secretName string
				secretNS   string
			}{
				enabled:    true,
				secretName: "custom-tls-secret",
				secretNS:   "custom-namespace",
			},
		},
		{
			name: "TLS enabled with default namespace",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
					Annotations: map[string]string{
						"kubeocean.io/logs-proxy-enabled":     "true",
						"kubeocean.io/logs-proxy-secret-name": "tls-secret",
					},
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster",
				},
			},
			templateFiles: map[string]string{
				"proxierNamespace": "kubeocean-system",
			},
			expectedTLS: struct {
				enabled    bool
				secretName string
				secretNS   string
			}{
				enabled:    true,
				secretName: "tls-secret",
				secretNS:   "kubeocean-system", // Should default to proxier namespace
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, cloudv1beta1.AddToScheme(scheme))

			reconciler := &ClusterBindingReconciler{
				Log: zap.New(zap.UseDevMode(true)),
			}

			templateData := reconciler.prepareProxierTemplateData(tt.clusterBinding, tt.templateFiles)

			assert.Equal(t, tt.expectedTLS.enabled, templateData.TLSEnabled)
			assert.Equal(t, tt.expectedTLS.secretName, templateData.TLSSecretName)
			assert.Equal(t, tt.expectedTLS.secretNS, templateData.TLSSecretNamespace)
		})
	}
}

func TestClusterBindingReconciler_handleDeletion_WaitForSyncerFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "default",
			Finalizers: []string{
				cloudv1beta1.ClusterBindingManagerFinalizer,
				cloudv1beta1.ClusterBindingSyncerFinalizer, // Syncer finalizer still present
			},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: "test-cluster",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(clusterBinding).
		Build()

	reconciler := &ClusterBindingReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Log:      zap.New(zap.UseDevMode(true)),
		Recorder: record.NewFakeRecorder(100),
	}

	ctx := context.Background()
	result, err := reconciler.handleDeletion(ctx, clusterBinding)

	// Should return an error indicating waiting for syncer finalizer
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "waiting for syncer finalizer")
	assert.Equal(t, ctrl.Result{}, result)
}

func TestClusterBindingReconciler_updateCondition(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	reconciler := &ClusterBindingReconciler{
		Log: zap.New(zap.UseDevMode(true)),
	}

	tests := []struct {
		name               string
		existingConditions []metav1.Condition
		conditionType      string
		status             metav1.ConditionStatus
		reason             string
		message            string
		expectedLength     int
		shouldUpdateTime   bool
	}{
		{
			name:               "add new condition",
			existingConditions: []metav1.Condition{},
			conditionType:      "Ready",
			status:             metav1.ConditionTrue,
			reason:             "AllGood",
			message:            "Everything is working",
			expectedLength:     1,
			shouldUpdateTime:   true,
		},
		{
			name: "update existing condition with different status",
			existingConditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionFalse,
					Reason:             "NotReady",
					Message:            "Not ready yet",
					LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
				},
			},
			conditionType:    "Ready",
			status:           metav1.ConditionTrue,
			reason:           "AllGood",
			message:          "Everything is working",
			expectedLength:   1,
			shouldUpdateTime: true,
		},
		{
			name: "update existing condition with same status",
			existingConditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "AllGood",
					Message:            "Everything is working",
					LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
				},
			},
			conditionType:    "Ready",
			status:           metav1.ConditionTrue,
			reason:           "StillGood",
			message:          "Still working fine",
			expectedLength:   1,
			shouldUpdateTime: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusterBinding := &cloudv1beta1.ClusterBinding{
				Status: cloudv1beta1.ClusterBindingStatus{
					Conditions: tt.existingConditions,
				},
			}

			oldTime := metav1.Time{}
			if len(tt.existingConditions) > 0 {
				for _, cond := range tt.existingConditions {
					if cond.Type == tt.conditionType {
						oldTime = cond.LastTransitionTime
						break
					}
				}
			}

			reconciler.updateCondition(clusterBinding, tt.conditionType, tt.status, tt.reason, tt.message)

			assert.Len(t, clusterBinding.Status.Conditions, tt.expectedLength)

			// Find the updated condition
			var updatedCondition *metav1.Condition
			for i, cond := range clusterBinding.Status.Conditions {
				if cond.Type == tt.conditionType {
					updatedCondition = &clusterBinding.Status.Conditions[i]
					break
				}
			}

			require.NotNil(t, updatedCondition)
			assert.Equal(t, tt.conditionType, updatedCondition.Type)
			assert.Equal(t, tt.status, updatedCondition.Status)
			assert.Equal(t, tt.reason, updatedCondition.Reason)
			assert.Equal(t, tt.message, updatedCondition.Message)

			if tt.shouldUpdateTime {
				if !oldTime.IsZero() {
					assert.True(t, updatedCondition.LastTransitionTime.After(oldTime.Time))
				}
			} else {
				if !oldTime.IsZero() {
					assert.Equal(t, oldTime, updatedCondition.LastTransitionTime)
				}
			}
		})
	}
}

func TestClusterBindingReconciler_Reconcile_ResourceNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &ClusterBindingReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Log:      zap.New(zap.UseDevMode(true)),
		Recorder: record.NewFakeRecorder(10),
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent-binding",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestClusterBindingReconciler_Reconcile_ValidationFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create invalid ClusterBinding (missing ClusterID)
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "invalid-binding",
			Namespace:  "default",
			Finalizers: []string{cloudv1beta1.ClusterBindingManagerFinalizer},
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			// ClusterID is missing - should cause validation failure
			SecretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "default",
			},
			MountNamespace: "test-mount",
		},
		Status: cloudv1beta1.ClusterBindingStatus{
			Phase: "Pending", // Already initialized
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(clusterBinding).
		WithStatusSubresource(clusterBinding).
		Build()

	reconciler := &ClusterBindingReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Log:      zap.New(zap.UseDevMode(true)),
		Recorder: record.NewFakeRecorder(100),
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      clusterBinding.Name,
			Namespace: clusterBinding.Namespace,
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)

	// Should return error due to validation failure
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "clusterID is required")
	assert.Equal(t, ctrl.Result{}, result)

	// Verify status was updated to Failed
	var updatedBinding cloudv1beta1.ClusterBinding
	err = fakeClient.Get(context.Background(), req.NamespacedName, &updatedBinding)
	assert.NoError(t, err)
	assert.Equal(t, cloudv1beta1.ClusterBindingPhase(PhaseFailed), updatedBinding.Status.Phase)

	// Verify Ready condition is set to False
	readyCondition := findCondition(updatedBinding.Status.Conditions, "Ready")
	require.NotNil(t, readyCondition)
	assert.Equal(t, metav1.ConditionFalse, readyCondition.Status)
	assert.Equal(t, "ValidationFailed", readyCondition.Reason)
}

func TestClusterBindingReconciler_SetupWithManager(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	reconciler := &ClusterBindingReconciler{
		Scheme: scheme,
		Log:    zap.New(zap.UseDevMode(true)),
	}

	// This is a basic test to ensure the method doesn't panic
	// In a real environment, this would set up with an actual manager
	err := reconciler.SetupWithManager(nil)
	// We expect this to fail since we're passing nil, but it shouldn't panic
	assert.Error(t, err)
}

func TestClusterBindingReconciler_SetupWithManagerAndName(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	reconciler := &ClusterBindingReconciler{
		Scheme: scheme,
		Log:    zap.New(zap.UseDevMode(true)),
	}

	// This is a basic test to ensure the method doesn't panic
	err := reconciler.SetupWithManagerAndName(nil, "test-controller")
	// We expect this to fail since we're passing nil, but it shouldn't panic
	assert.Error(t, err)
}

// Helper function to find a condition by type
func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i, condition := range conditions {
		if condition.Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
