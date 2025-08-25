package topdown

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestNewTopDownSyncer(t *testing.T) {
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

	// Create cluster binding
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "physical-namespace",
		},
	}

	// Test valid syncer creation
	syncer := NewTopDownSyncer(virtualManager, physicalManager, ctrl.GetConfigOrDie(), scheme, clusterBinding)
	assert.NotNil(t, syncer)
	assert.Equal(t, virtualManager, syncer.virtualManager)
	assert.Equal(t, physicalManager, syncer.physicalManager)
	assert.Equal(t, clusterBinding, syncer.ClusterBinding)
	assert.Equal(t, scheme, syncer.Scheme)
}

func TestTopDownSyncer_Setup(t *testing.T) {
	// Skip this test if we don't have a real Kubernetes cluster
	// This test requires a real cluster to setup controllers properly
	t.Skip("Skipping TestTopDownSyncer_Setup as it requires a real Kubernetes cluster")
}

func TestTopDownSyncer_Start(t *testing.T) {
	// Setup scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)

	// Create syncer
	virtualManager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	require.NoError(t, err)

	physicalManager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	require.NoError(t, err)

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "physical-namespace",
		},
	}

	syncer := NewTopDownSyncer(virtualManager, physicalManager, ctrl.GetConfigOrDie(), scheme, clusterBinding)
	require.NotNil(t, syncer)

	// Test Start with context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err = syncer.Start(ctx)
	assert.NoError(t, err) // Should return gracefully when context is cancelled
}

func TestTopDownSyncer_setupControllers(t *testing.T) {
	// Skip this test if we don't have a real Kubernetes cluster
	// This test requires a real cluster to setup controllers properly
	t.Skip("Skipping TestTopDownSyncer_setupControllers as it requires a real Kubernetes cluster")
}
