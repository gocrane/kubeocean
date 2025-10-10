// Copyright 2024 The Kubeocean Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package proxier

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
)

const (
	// Annotation keys for virtual pod information
	AnnotationVirtualNodeName     = "kubeocean.io/virtual-node-name"
	AnnotationVirtualPodName      = "kubeocean.io/virtual-pod-name"
	AnnotationVirtualPodNamespace = "kubeocean.io/virtual-pod-namespace"
)

// PodController watches physical cluster pods and maintains pod mapping
type PodController struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// NewPodController creates a new pod controller
func NewPodController(client client.Client, scheme *runtime.Scheme, log logr.Logger) *PodController {
	return &PodController{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}
}

// Reconcile handles pod events
func (r *PodController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("pod", req.NamespacedName)

	// Fetch the Pod
	pod := &corev1.Pod{}
	err := r.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Pod was deleted
			log.Info("Pod deleted, removing from mapping", "podName", req.Name)
			DeleteVirtualPodInfo(req.Name)
			r.logCurrentMappingCount()
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch Pod")
		return ctrl.Result{}, err
	}

	// Check if pod has the required label
	if !r.hasManagedByLabel(pod) {
		log.V(1).Info("Pod does not have required label, skipping", "podName", pod.Name)
		return ctrl.Result{}, nil
	}

	// Extract virtual pod information from annotations
	virtualInfo, err := r.extractVirtualPodInfo(pod)
	if err != nil {
		log.Info("Pod missing required annotations, ignoring", "podName", pod.Name, "error", err)
		// Remove from mapping if it was previously added
		DeleteVirtualPodInfo(pod.Name)
		return ctrl.Result{}, nil
	}

	// Update the mapping
	log.Info("Updating pod mapping",
		"physicalPod", pod.Name,
		"virtualNode", virtualInfo.VirtualNodeName,
		"virtualPod", virtualInfo.VirtualPodName,
		"virtualNamespace", virtualInfo.VirtualPodNamespace)

	SetVirtualPodInfo(pod.Name, virtualInfo)
	r.logCurrentMappingCount()

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *PodController) SetupWithManager(mgr ctrl.Manager) error {
	// Create a controller that watches Pods with specific labels
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		Complete(r)
}

// InitializeExistingPods lists and processes existing pods during startup
func (r *PodController) InitializeExistingPods(ctx context.Context) error {
	log := r.Log.WithName("initialize")
	log.Info("Initializing existing pods")

	// List all pods with the required label
	podList := &corev1.PodList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
	})

	err := r.List(ctx, podList, &client.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		log.Error(err, "failed to list existing pods")
		return err
	}

	log.Info("Found existing pods", "count", len(podList.Items))

	// Process each pod
	processed := 0
	for _, pod := range podList.Items {
		// Extract virtual pod information
		virtualInfo, err := r.extractVirtualPodInfo(&pod)
		if err != nil {
			log.V(1).Info("Pod missing required annotations, skipping",
				"podName", pod.Name, "error", err)
			continue
		}

		// Add to mapping
		SetVirtualPodInfo(pod.Name, virtualInfo)
		processed++

		log.V(1).Info("Added pod to mapping",
			"physicalPod", pod.Name,
			"virtualNode", virtualInfo.VirtualNodeName,
			"virtualPod", virtualInfo.VirtualPodName,
			"virtualNamespace", virtualInfo.VirtualPodNamespace)
	}

	log.Info("Pod initialization completed",
		"totalPods", len(podList.Items),
		"processedPods", processed,
		"mappingCount", GetPodMappingCount())

	return nil
}

// hasManagedByLabel checks if pod has the required managed-by label
func (r *PodController) hasManagedByLabel(pod *corev1.Pod) bool {
	if pod.Labels == nil {
		return false
	}
	return pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue
}

// extractVirtualPodInfo extracts virtual pod information from pod annotations
func (r *PodController) extractVirtualPodInfo(pod *corev1.Pod) (*VirtualPodInfo, error) {
	if pod.Annotations == nil {
		return nil, fmt.Errorf("pod has no annotations")
	}

	virtualNodeName := pod.Annotations[AnnotationVirtualNodeName]
	virtualPodName := pod.Annotations[AnnotationVirtualPodName]
	virtualPodNamespace := pod.Annotations[AnnotationVirtualPodNamespace]

	// Check if all required annotations are present
	if virtualNodeName == "" {
		return nil, fmt.Errorf("missing annotation: %s", AnnotationVirtualNodeName)
	}
	if virtualPodName == "" {
		return nil, fmt.Errorf("missing annotation: %s", AnnotationVirtualPodName)
	}
	if virtualPodNamespace == "" {
		return nil, fmt.Errorf("missing annotation: %s", AnnotationVirtualPodNamespace)
	}

	return &VirtualPodInfo{
		VirtualNodeName:     virtualNodeName,
		VirtualPodName:      virtualPodName,
		VirtualPodNamespace: virtualPodNamespace,
	}, nil
}

// logCurrentMappingCount logs the current number of pod mappings
func (r *PodController) logCurrentMappingCount() {
	count := GetPodMappingCount()
	r.Log.V(1).Info("Current pod mapping count", "count", count)
}
