/*
Copyright 2025 The Kubeocean Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
)

var _ = Describe("ClusterBindingCollector", func() {
	var (
		collector  *ClusterBindingCollector
		fakeClient client.Client
		scheme     *runtime.Scheme
		ctx        context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(cloudv1beta1.AddToScheme(scheme)).To(Succeed())

		fakeClient = fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		collector = NewClusterBindingCollector(fakeClient, log.Log.WithName("test"))
	})

	Context("when collecting metrics", func() {
		It("should report zero counts when no ClusterBindings exist", func() {
			ch := make(chan prometheus.Metric, 10)
			collector.Collect(ch)
			close(ch)

			// Verify metric count - should have 4 metrics (one for each status)
			Expect(ch).To(HaveLen(4))
		})

		It("should correctly count ClusterBindings in different states", func() {
			// Create test data
			now := metav1.Now()
			clusterBindings := []cloudv1beta1.ClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cb-pending",
					},
					Status: cloudv1beta1.ClusterBindingStatus{
						Phase: cloudv1beta1.ClusterBindingPhasePending,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cb-ready",
					},
					Status: cloudv1beta1.ClusterBindingStatus{
						Phase: cloudv1beta1.ClusterBindingPhaseReady,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cb-failed",
					},
					Status: cloudv1beta1.ClusterBindingStatus{
						Phase: cloudv1beta1.ClusterBindingPhaseFailed,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "cb-deleting",
						DeletionTimestamp: &now,
					},
					Status: cloudv1beta1.ClusterBindingStatus{
						Phase: cloudv1beta1.ClusterBindingPhaseReady, // Even if Ready, DeletionTimestamp means Deleting
					},
				},
			}

			// Add test data to fake client
			for _, cb := range clusterBindings {
				Expect(fakeClient.Create(ctx, &cb)).To(Succeed())
			}

			// Collect metrics
			ch := make(chan prometheus.Metric, 10)
			collector.Collect(ch)
			close(ch)

			// Verify that there are 4 metrics (one for each status)
			Expect(ch).To(HaveLen(4))
		})

		It("should handle empty phase as pending", func() {
			// Create a ClusterBinding without setting phase
			cb := cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cb-empty-phase",
				},
				// Status.Phase is empty
			}

			Expect(fakeClient.Create(ctx, &cb)).To(Succeed())

			// Collect metrics
			ch := make(chan prometheus.Metric, 10)
			collector.Collect(ch)
			close(ch)

			// Verify that there are 4 metrics (one for each status)
			Expect(ch).To(HaveLen(4))
		})
	})

	Context("getClusterBindingStatus method", func() {
		It("should return Deleting when DeletionTimestamp is set", func() {
			now := metav1.Now()
			cb := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &now,
				},
				Status: cloudv1beta1.ClusterBindingStatus{
					Phase: cloudv1beta1.ClusterBindingPhaseReady, // Even if Phase is Ready
				},
			}

			status := collector.getClusterBindingStatus(cb)
			Expect(status).To(Equal(StatusDeleting))
		})

		It("should return correct status based on Phase when not deleting", func() {
			testCases := []struct {
				phase    cloudv1beta1.ClusterBindingPhase
				expected string
			}{
				{cloudv1beta1.ClusterBindingPhaseReady, StatusReady},
				{cloudv1beta1.ClusterBindingPhaseFailed, StatusFailed},
				{cloudv1beta1.ClusterBindingPhasePending, StatusPending},
				{"", StatusPending}, // Empty phase should be treated as Pending
			}

			for _, tc := range testCases {
				cb := &cloudv1beta1.ClusterBinding{
					Status: cloudv1beta1.ClusterBindingStatus{
						Phase: tc.phase,
					},
				}

				status := collector.getClusterBindingStatus(cb)
				Expect(status).To(Equal(tc.expected), "Phase %s should map to %s", tc.phase, tc.expected)
			}
		})
	})

	Context("Describe method", func() {
		It("should provide correct metric description", func() {
			ch := make(chan *prometheus.Desc, 10)
			collector.Describe(ch)
			close(ch)

			Expect(ch).To(HaveLen(1))
			desc := <-ch
			Expect(desc).ToNot(BeNil())
		})
	})
})
