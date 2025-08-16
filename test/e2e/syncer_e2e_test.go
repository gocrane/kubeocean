package e2e

import (
	"context"
	"fmt"
	"time"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	syncerpkg "github.com/TKEColocation/tapestry/pkg/syncer"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = ginkgo.Describe("Syncer E2E Tests", func() {
	ginkgo.Describe("TapestrySyncer Initialization", func() {
		ginkgo.It("should create TapestrySyncer instance successfully", func(ctx context.Context) {
			// Create namespace for secrets
			ns := "tapestry-system"
			_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})

			// Create kubeconfig secret for physical cluster connection
			kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-kc", Namespace: ns},
				Data:       map[string][]byte{"kubeconfig": kc},
			}
			gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

			// Create ClusterBinding resource
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster",
					SecretRef:      corev1.SecretReference{Name: "test-cluster-kc", Namespace: ns},
					MountNamespace: "default",
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			ginkgo.By("ClusterBinding created")

			// Create dedicated Manager for this test
			testMgr, err := ctrl.NewManager(cfgVirtual, ctrl.Options{
				Scheme: scheme,
				Metrics: metricsserver.Options{
					BindAddress: "0", // 禁用 metrics 服务器
				},
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Create TapestrySyncer instance
			syncer, err := syncerpkg.NewTapestrySyncer(testMgr, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(syncer).NotTo(gomega.BeNil())

			// Verify syncer properties
			gomega.Expect(syncer.GetClusterBinding()).To(gomega.BeNil()) // Not loaded yet

			ginkgo.By("TapestrySyncer instance created successfully")
		}, ginkgo.SpecTimeout(30*time.Second))

		ginkgo.It("should load ClusterBinding successfully", func(ctx context.Context) {
			// Create namespace for secrets
			ns := "tapestry-system"
			_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})

			// Create kubeconfig secret
			kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "load-test-kc", Namespace: ns},
				Data:       map[string][]byte{"kubeconfig": kc},
			}
			gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

			// Create ClusterBinding resource
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "load-test-cluster"},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "load-test-cluster",
					SecretRef:      corev1.SecretReference{Name: "load-test-kc", Namespace: ns},
					MountNamespace: "default",
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			// Create dedicated Manager for this test
			testMgr, err := ctrl.NewManager(cfgVirtual, ctrl.Options{
				Scheme: scheme,
				Metrics: metricsserver.Options{
					BindAddress: "0", // 禁用 metrics 服务器
				},
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Create TapestrySyncer and start it briefly to load ClusterBinding
			syncer, err := syncerpkg.NewTapestrySyncer(testMgr, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Start syncer in background for a short time to trigger loading
			syncerCtx, syncerCancel := context.WithTimeout(ctx, 10*time.Second)
			defer syncerCancel()

			go func() {
				defer ginkgo.GinkgoRecover()
				_ = syncer.Start(syncerCtx) // Expected to timeout, that's OK
			}()

			// Wait a bit for loading to happen
			time.Sleep(2 * time.Second)

			// Verify ClusterBinding was loaded
			loadedBinding := syncer.GetClusterBinding()
			gomega.Expect(loadedBinding).NotTo(gomega.BeNil())
			gomega.Expect(loadedBinding.Name).To(gomega.Equal("load-test-cluster"))
			gomega.Expect(loadedBinding.Spec.ClusterID).To(gomega.Equal("load-test-cluster"))

			ginkgo.By("ClusterBinding loaded successfully")
		}, ginkgo.SpecTimeout(30*time.Second))
	})

	ginkgo.Describe("Virtual Node Creation Basic Test", func() {
		ginkgo.It("should create virtual node for single physical node", func(ctx context.Context) {
			// Create namespace for secrets
			ns := "tapestry-system"
			_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})

			// Create kubeconfig secret
			kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "basic-test-kc", Namespace: ns},
				Data:       map[string][]byte{"kubeconfig": kc},
			}
			gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

			// Create ClusterBinding resource
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "basic-test-cluster"},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "basic-test-cluster",
					SecretRef:      corev1.SecretReference{Name: "basic-test-kc", Namespace: ns},
					MountNamespace: "default",
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			// Create a simple physical node
			physicalNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "basic-test-node",
					Labels: map[string]string{
						"node-role.kubernetes.io/worker": "",
						"kubernetes.io/arch":             "amd64",
						"kubernetes.io/os":               "linux",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, physicalNode)).To(gomega.Succeed())

			ginkgo.By("Physical node created")

			// Create and start TapestrySyncer
			syncer, err := syncerpkg.NewTapestrySyncer(mgrVirtual, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Start syncer in background
			go func() {
				defer ginkgo.GinkgoRecover()
				err := syncer.Start(ctx)
				if err != nil && ctx.Err() == nil {
					ginkgo.Fail(fmt.Sprintf("TapestrySyncer failed: %v", err))
				}
			}()

			ginkgo.By("TapestrySyncer started")

			// Wait for virtual node to be created
			expectedVirtualNode := "vnode-basic-test-cluster-basic-test-node"

			gomega.Eventually(func() bool {
				var vnode corev1.Node
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: expectedVirtualNode}, &vnode)
				return err == nil
			}, 45*time.Second, 2*time.Second).Should(gomega.BeTrue(), "Virtual node should be created")

			ginkgo.By("Virtual node created successfully")

			// Verify basic virtual node properties
			var virtualNode corev1.Node
			gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Name: expectedVirtualNode}, &virtualNode)).To(gomega.Succeed())

			// Check essential labels
			gomega.Expect(virtualNode.Labels).To(gomega.HaveKeyWithValue("tapestry.io/cluster-binding", "basic-test-cluster"))
			gomega.Expect(virtualNode.Labels).To(gomega.HaveKeyWithValue("tapestry.io/physical-node-name", "basic-test-node"))
			gomega.Expect(virtualNode.Labels).To(gomega.HaveKeyWithValue("tapestry.io/managed-by", "tapestry"))

			// Check resources
			gomega.Expect(virtualNode.Status.Allocatable[corev1.ResourceCPU]).To(gomega.Equal(resource.MustParse("2")))
			gomega.Expect(virtualNode.Status.Allocatable[corev1.ResourceMemory]).To(gomega.Equal(resource.MustParse("4Gi")))

			ginkgo.By("Virtual node properties verified")
		}, ginkgo.SpecTimeout(90*time.Second))
	})

	ginkgo.Describe("Error Handling", func() {
		ginkgo.It("should handle missing ClusterBinding gracefully", func(ctx context.Context) {
			// Create dedicated Manager for this test
			testMgr, err := ctrl.NewManager(cfgVirtual, ctrl.Options{
				Scheme: scheme,
				Metrics: metricsserver.Options{
					BindAddress: "0", // 禁用 metrics 服务器
				},
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Try to create TapestrySyncer with non-existent ClusterBinding
			syncer, err := syncerpkg.NewTapestrySyncer(testMgr, k8sVirtual, scheme, "non-existent-binding")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(syncer).NotTo(gomega.BeNil())

			// Start syncer - should fail gracefully when trying to load ClusterBinding
			syncerCtx, syncerCancel := context.WithTimeout(ctx, 5*time.Second)
			defer syncerCancel()

			err = syncer.Start(syncerCtx)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("failed to load cluster binding"))

			ginkgo.By("Missing ClusterBinding handled gracefully")
		}, ginkgo.SpecTimeout(30*time.Second))

		ginkgo.It("should handle invalid kubeconfig gracefully", func(ctx context.Context) {
			// Create namespace for secrets
			ns := "tapestry-system"
			_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})

			// Create invalid kubeconfig secret
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-kc", Namespace: ns},
				Data:       map[string][]byte{"kubeconfig": []byte("invalid-kubeconfig-data")},
			}
			gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

			// Create ClusterBinding with invalid kubeconfig
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-cluster"},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "invalid-cluster",
					SecretRef:      corev1.SecretReference{Name: "invalid-kc", Namespace: ns},
					MountNamespace: "default",
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			// Create dedicated Manager for this test
			testMgr, err := ctrl.NewManager(cfgVirtual, ctrl.Options{
				Scheme: scheme,
				Metrics: metricsserver.Options{
					BindAddress: "0", // 禁用 metrics 服务器
				},
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Create TapestrySyncer
			syncer, err := syncerpkg.NewTapestrySyncer(testMgr, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// Start syncer - should fail when trying to setup physical cluster connection
			syncerCtx, syncerCancel := context.WithTimeout(ctx, 10*time.Second)
			defer syncerCancel()

			err = syncer.Start(syncerCtx)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("failed to setup physical cluster connection"))

			ginkgo.By("Invalid kubeconfig handled gracefully")
		}, ginkgo.SpecTimeout(30*time.Second))
	})
})
