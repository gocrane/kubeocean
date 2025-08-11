package syncer

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

// TapestrySyncer manages the synchronization between virtual and physical clusters
type TapestrySyncer struct {
	client.Client
	Scheme                  *runtime.Scheme
	Log                     logr.Logger
	ClusterBindingName      string
	ClusterBindingNamespace string
	clusterBinding          *cloudv1beta1.ClusterBinding
	bottomUpSyncer          *BottomUpSyncer
	topDownSyncer           *TopDownSyncer
}

// NewTapestrySyncer creates a new TapestrySyncer instance
func NewTapestrySyncer(client client.Client, scheme *runtime.Scheme, bindingName, bindingNamespace string) (*TapestrySyncer, error) {
	log := ctrl.Log.WithName("tapestry-syncer").WithValues("binding", fmt.Sprintf("%s/%s", bindingNamespace, bindingName))

	return &TapestrySyncer{
		Client:                  client,
		Scheme:                  scheme,
		Log:                     log,
		ClusterBindingName:      bindingName,
		ClusterBindingNamespace: bindingNamespace,
	}, nil
}

// Start starts the Tapestry Syncer
func (ts *TapestrySyncer) Start(ctx context.Context) error {
	ts.Log.Info("Starting Tapestry Syncer")

	// Load the ClusterBinding
	if err := ts.loadClusterBinding(ctx); err != nil {
		return fmt.Errorf("failed to load cluster binding: %w", err)
	}

	// TODO: Initialize bottom-up and top-down syncers
	// This will be implemented in later tasks

	ts.Log.Info("Tapestry Syncer started successfully")

	// Keep the syncer running
	<-ctx.Done()
	ts.Log.Info("Tapestry Syncer stopping")

	return nil
}

// loadClusterBinding loads the ClusterBinding resource
func (ts *TapestrySyncer) loadClusterBinding(ctx context.Context) error {
	var binding cloudv1beta1.ClusterBinding
	key := types.NamespacedName{
		Name:      ts.ClusterBindingName,
		Namespace: ts.ClusterBindingNamespace,
	}

	if err := ts.Get(ctx, key, &binding); err != nil {
		return fmt.Errorf("failed to get ClusterBinding %s: %w", key, err)
	}

	ts.clusterBinding = &binding
	ts.Log.Info("Loaded ClusterBinding", "name", binding.Name, "namespace", binding.Namespace)

	return nil
}

// GetClusterBinding returns the current ClusterBinding
func (ts *TapestrySyncer) GetClusterBinding() *cloudv1beta1.ClusterBinding {
	return ts.clusterBinding
}
