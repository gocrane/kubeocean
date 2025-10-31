package utils

import (
	"context"
	"fmt"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func IsRunningDaemonsetByDefault(ctx context.Context, client client.Client, clusterBindingName string) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("client is nil")
	}
	if clusterBindingName == "" {
		return false, fmt.Errorf("clusterBindingName is empty")
	}
	clusterBinding := &cloudv1beta1.ClusterBinding{}
	err := client.Get(ctx, types.NamespacedName{Name: clusterBindingName}, clusterBinding)
	if err != nil {
		return false, err
	}
	return clusterBinding.Spec.RunningDaemonsetByDefault, nil
}
