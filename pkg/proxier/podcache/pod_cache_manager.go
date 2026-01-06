// Copyright 2025 The Kubeocean Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package podcache

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	// IndexNameNodeName is the index name for pods by node name
	IndexNameNodeName = "spec.nodeName"
)

// Manager defines the interface for pod cache management
type Manager interface {
	// GetPodsByNode returns all pods running on the specified node
	GetPodsByNode(ctx context.Context, nodeName string) (*corev1.PodList, error)
}

// PodCacheManager manages a cache of pods using controller-runtime manager
// PodCacheManager implements the Manager interface
type PodCacheManager struct {
	manager ctrl.Manager
	indexer cache.Indexer
	logger  logr.Logger
	mu      sync.RWMutex
}

// NewPodCacheManager creates a new PodCacheManager
func NewPodCacheManager(manager ctrl.Manager) *PodCacheManager {
	return &PodCacheManager{
		manager: manager,
		logger:  ctrl.Log.WithName("pod-cache"),
	}
}

// Setup sets up the index for pods by node name
// Note: This should be called before starting the manager
func (pcm *PodCacheManager) Setup(ctx context.Context) error {
	pcm.mu.Lock()
	defer pcm.mu.Unlock()

	// Get pod informer from controller-runtime cache
	podInformer, err := pcm.manager.GetCache().GetInformer(ctx, &corev1.Pod{})
	if err != nil {
		return fmt.Errorf("failed to get pod informer: %w", err)
	}

	// Type assert to get the underlying SharedIndexInformer
	// controller-runtime's cache wraps client-go's SharedIndexInformer
	sharedIndexInformer, ok := podInformer.(cache.SharedIndexInformer)
	if !ok {
		return fmt.Errorf("failed to get indexer from informer: informer is not a SharedIndexInformer")
	}

	// Add indexer using client-go's AddIndexers method
	// This must be called before the informer starts
	indexers := cache.Indexers{
		IndexNameNodeName: func(obj interface{}) ([]string, error) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return nil, fmt.Errorf("object is not a Pod")
			}
			if pod.Spec.NodeName == "" {
				return nil, nil
			}
			return []string{pod.Spec.NodeName}, nil
		},
	}

	// Add the indexers to the informer's indexer
	// Note: AddIndexers will return an error if the index already exists
	// This can happen if the informer has already started or if the index was added elsewhere
	if err := sharedIndexInformer.GetIndexer().AddIndexers(indexers); err != nil {
		// Check if it's an "indexer conflict" error, which means the index already exists
		// In that case, we can safely proceed as the index is already set up
		if err.Error() != "indexer conflict: "+fmt.Sprintf("map[%s:{}]", IndexNameNodeName) {
			return fmt.Errorf("failed to add indexers: %w", err)
		}
		pcm.logger.V(1).Info("Index already exists, reusing existing index", "indexName", IndexNameNodeName)
	}

	// Store the indexer for later use
	pcm.indexer = sharedIndexInformer.GetIndexer()

	pcm.logger.Info("Pod cache manager index setup completed")

	return nil
}

// GetPodsByNode returns all pods running on the specified node
func (pcm *PodCacheManager) GetPodsByNode(ctx context.Context, nodeName string) (*corev1.PodList, error) {
	pcm.mu.RLock()
	defer pcm.mu.RUnlock()

	if pcm.indexer == nil {
		return nil, fmt.Errorf("pod indexer is not initialized")
	}

	// Directly use indexer.ByIndex for optimal performance
	// This bypasses the cache.List overhead and directly uses the index
	pods, err := pcm.indexer.ByIndex(IndexNameNodeName, nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get pods by node name %s: %w", nodeName, err)
	}

	// Convert to PodList
	podList := &corev1.PodList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PodList",
			APIVersion: "v1",
		},
		Items: make([]corev1.Pod, 0, len(pods)),
	}

	for _, obj := range pods {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			pcm.logger.V(1).Info("Object in indexer is not a pod, skipping", "nodeName", nodeName)
			continue
		}
		podList.Items = append(podList.Items, *pod)
	}

	return podList, nil
}
