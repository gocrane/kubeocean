package logconfig

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	clsv1 "github.com/gocrane/kubeocean/api/cls/v1"
	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	topcommon "github.com/gocrane/kubeocean/pkg/syncer/topdown/common"
)

type lostResponseLogConfigCreateClient struct {
	client.Client
	createCalls int
}

func (c *lostResponseLogConfigCreateClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.createCalls++
	if c.createCalls == 1 {
		createdObj := obj.DeepCopyObject().(client.Object)
		if err := c.Client.Create(ctx, createdObj, opts...); err != nil {
			return err
		}
		return errors.New("http: server closed idle connection")
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestVirtualLogConfigReconcilerAtomicRebuildTreatsAlreadyExistsAfterTransientErrorAsSuccess(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clsv1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	baseClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	physicalClient := &lostResponseLogConfigCreateClient{Client: baseClient}
	reconciler := &VirtualLogConfigReconciler{
		PhysicalClient: physicalClient,
		ClusterID:      "test-cluster-id",
		Log:            ctrl.Log.WithName("test"),
	}
	newConfig := &clsv1.LogConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "physical-logconfig",
			Labels: map[string]string{
				topcommon.GetManagedByClusterIDLabel("test-cluster-id"): cloudv1beta1.LabelValueTrue,
				cloudv1beta1.LabelManagedBy:                             cloudv1beta1.LabelManagedByValue,
				"kubeocean.io/virtual-logconfig":                        "virtual-logconfig",
			},
		},
	}

	err := reconciler.atomicRebuildPhysicalLogConfigs(context.Background(), nil, []*clsv1.LogConfig{newConfig}, ctrl.Log.WithName("test"))

	require.NoError(t, err)
	assert.Equal(t, 2, physicalClient.createCalls)
	stored := &clsv1.LogConfig{}
	require.NoError(t, baseClient.Get(context.Background(), types.NamespacedName{Name: "physical-logconfig"}, stored))
	assert.Equal(t, "virtual-logconfig", stored.Labels["kubeocean.io/virtual-logconfig"])
}
