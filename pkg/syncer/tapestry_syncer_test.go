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

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestNewTapestrySyncer(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	syncer, err := NewTapestrySyncer(fakeClient, scheme, "test-binding", "test-namespace")

	assert.NoError(t, err)
	assert.NotNil(t, syncer)
	assert.Equal(t, "test-binding", syncer.ClusterBindingName)
	assert.Equal(t, "test-namespace", syncer.ClusterBindingNamespace)
	assert.NotNil(t, syncer.stopCh)
	assert.NotNil(t, syncer.doneCh)
}

func TestTapestrySyncer_LoadClusterBinding(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Create test ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "test-namespace",
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

	syncer, err := NewTapestrySyncer(fakeClient, scheme, "test-binding", "test-namespace")
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

	syncer, err := NewTapestrySyncer(fakeClient, scheme, "nonexistent-binding", "test-namespace")
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
					Name:      "test-binding",
					Namespace: "test-namespace",
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
					Name:      "test-binding",
					Namespace: "test-namespace",
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
					Name:      "test-binding",
					Namespace: "test-namespace",
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
					Name:      "test-binding",
					Namespace: "test-namespace",
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

			syncer, err := NewTapestrySyncer(fakeClient, scheme, tt.binding.Name, tt.binding.Namespace)
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

func TestTapestrySyncer_StopAndDone(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	syncer, err := NewTapestrySyncer(fakeClient, scheme, "test-binding", "test-namespace")
	require.NoError(t, err)

	// Test that channels are initially open
	select {
	case <-syncer.stopCh:
		t.Fatal("stopCh should not be closed initially")
	case <-syncer.Done():
		t.Fatal("Done channel should not be closed initially")
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

	// Done channel should still be open (closed only when Start() finishes)
	select {
	case <-syncer.Done():
		t.Fatal("Done channel should not be closed until Start() finishes")
	default:
		// Expected
	}
}
