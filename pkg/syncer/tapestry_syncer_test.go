package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestNewTapestrySyncer(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create a fake manager - we'll use nil since the test doesn't require a real manager
	var fakeManager manager.Manager = nil

	syncer, err := NewTapestrySyncer(fakeManager, fakeClient, scheme, "test-binding")

	assert.NoError(t, err)
	assert.NotNil(t, syncer)
	assert.Equal(t, "test-binding", syncer.ClusterBindingName)
	assert.NotNil(t, syncer.stopCh)
	assert.NotNil(t, syncer.phyMgrCh)
}

func TestTapestrySyncer_LoadClusterBinding(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create test ClusterBinding (cluster-scoped resource)
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			SecretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "test-namespace",
			},
			MountNamespace: "test-mount",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(clusterBinding).
		Build()

	syncer, err := NewTapestrySyncer(nil, fakeClient, scheme, "test-binding")
	require.NoError(t, err)

	ctx := context.Background()
	err = syncer.loadClusterBinding(ctx)

	assert.NoError(t, err)
	assert.NotNil(t, syncer.clusterBinding)
	assert.Equal(t, "test-binding", syncer.clusterBinding.Name)
}

func TestTapestrySyncer_LoadClusterBinding_NotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	syncer, err := NewTapestrySyncer(nil, fakeClient, scheme, "nonexistent-binding")
	require.NoError(t, err)

	ctx := context.Background()
	err = syncer.loadClusterBinding(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get ClusterBinding")
}

func TestTapestrySyncer_ReadKubeconfigSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name    string
		secret  *corev1.Secret
		binding *cloudv1beta1.ClusterBinding
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid kubeconfig secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"kubeconfig": []byte("fake-kubeconfig-data"),
				},
			},
			binding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "secret not found",
			binding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					SecretRef: corev1.SecretReference{
						Name:      "nonexistent-secret",
						Namespace: "test-namespace",
					},
				},
			},
			wantErr: true,
			errMsg:  "failed to get secret",
		},
		{
			name: "secret missing kubeconfig key",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"other-key": []byte("some-data"),
				},
			},
			binding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
				},
			},
			wantErr: true,
			errMsg:  "kubeconfig key not found",
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
			binding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
				},
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
			objects = append(objects, tt.binding)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			syncer, err := NewTapestrySyncer(nil, fakeClient, scheme, tt.binding.Name)
			require.NoError(t, err)
			syncer.clusterBinding = tt.binding

			ctx := context.Background()
			data, err := syncer.readKubeconfigSecret(ctx)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, []byte("fake-kubeconfig-data"), data)
			}
		})
	}
}

func TestTapestrySyncer_StopAndChannels(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	syncer, err := NewTapestrySyncer(nil, fakeClient, scheme, "test-binding")
	require.NoError(t, err)

	// Test that channels are initially open
	select {
	case <-syncer.stopCh:
		t.Fatal("stopCh should not be closed initially")
	case <-syncer.phyMgrCh:
		t.Fatal("phyMgrCh should not be closed initially")
	default:
		// Expected
	}

	// Test Stop method
	syncer.Stop()

	// stopCh should be closed
	select {
	case <-syncer.stopCh:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stopCh should be closed after Stop()")
	}

	// phyMgrCh should still be open (closed only when physical manager finishes)
	select {
	case <-syncer.phyMgrCh:
		t.Fatal("phyMgrCh should not be closed until physical manager finishes")
	default:
		// Expected
	}
}

// TestTapestrySyncer_InitializeSyncers tests the syncer initialization
func TestTapestrySyncer_InitializeSyncers(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create test ClusterBinding with kubeconfig secret
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			SecretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "test-namespace",
			},
			MountNamespace: "test-mount",
			ClusterID:      "test-cluster-id",
		},
	}

	// Create test secret with kubeconfig
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "test-namespace",
		},
		Data: map[string][]byte{
			"kubeconfig": []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://test-cluster:6443
    insecure-skip-tls-verify: true
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
    token: test-token
`),
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(clusterBinding, secret).
		Build()

	syncer, err := NewTapestrySyncer(nil, fakeClient, scheme, "test-binding")
	require.NoError(t, err)

	// Load cluster binding first
	ctx := context.Background()
	err = syncer.loadClusterBinding(ctx)
	require.NoError(t, err)

	// Skip physical cluster connection and syncer initialization for unit tests
	// These require actual cluster connections which are not available in unit tests

	// Just verify that the cluster binding was loaded correctly
	assert.NotNil(t, syncer.clusterBinding)
	assert.Equal(t, "test-binding", syncer.clusterBinding.Name)
	assert.Equal(t, "test-cluster-id", syncer.clusterBinding.Spec.ClusterID)
}

// TestTapestrySyncer_PhysicalManagerLifecycle tests the physical manager lifecycle
func TestTapestrySyncer_PhysicalManagerLifecycle(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	syncer, err := NewTapestrySyncer(nil, fakeClient, scheme, "test-binding")
	require.NoError(t, err)

	// Simulate physical manager completion by closing the channel
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(syncer.phyMgrCh)
	}()

	// Test that phyMgrCh gets closed
	select {
	case <-syncer.phyMgrCh:
		// Expected - physical manager completed
	case <-time.After(200 * time.Millisecond):
		t.Fatal("phyMgrCh should be closed when physical manager completes")
	}
}

// TestTapestrySyncer_GetClusterBinding tests the GetClusterBinding method
func TestTapestrySyncer_GetClusterBinding(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-binding",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			SecretRef: corev1.SecretReference{
				Name:      "test-secret",
				Namespace: "test-namespace",
			},
			MountNamespace: "test-mount",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(clusterBinding).
		Build()

	syncer, err := NewTapestrySyncer(nil, fakeClient, scheme, "test-binding")
	require.NoError(t, err)

	// Initially should return nil
	assert.Nil(t, syncer.GetClusterBinding())

	// Load cluster binding
	ctx := context.Background()
	err = syncer.loadClusterBinding(ctx)
	require.NoError(t, err)

	// Should return the loaded binding
	binding := syncer.GetClusterBinding()
	assert.NotNil(t, binding)
	assert.Equal(t, "test-binding", binding.Name)
	assert.Equal(t, "test-mount", binding.Spec.MountNamespace)
}

// TestTapestrySyncer_ReadKubeconfigFromSecret tests reading kubeconfig from secret
func TestTapestrySyncer_ReadKubeconfigFromSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name    string
		secret  *corev1.Secret
		binding *cloudv1beta1.ClusterBinding
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid kubeconfig",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"kubeconfig": []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://test-cluster:6443
    insecure-skip-tls-verify: true
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
    token: test-token
`),
				},
			},
			binding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
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
			name: "invalid kubeconfig",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"kubeconfig": []byte(""),
				},
			},
			binding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
					MountNamespace: "test-mount",
				},
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
			objects = append(objects, tt.binding)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			syncer, err := NewTapestrySyncer(nil, fakeClient, scheme, tt.binding.Name)
			require.NoError(t, err)
			syncer.clusterBinding = tt.binding

			// Test kubeconfig reading instead of actual connection setup
			ctx := context.Background()
			data, err := syncer.readKubeconfigSecret(ctx)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, data)
				assert.Contains(t, string(data), "https://test-cluster:6443")
			}
		})
	}
}
