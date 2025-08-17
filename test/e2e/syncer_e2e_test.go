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
	"sigs.k8s.io/controller-runtime/pkg/client"
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
					ClusterID:      "basic-test-cls",
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
			expectedVirtualNode := "vnode-basic-test-cls-basic-test-node"

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

	ginkgo.Describe("Virtual Node Resource Tests", func() {
		var (
			testNamespace = "tapestry-system"
			uniqueID      = generateUniqueID()
			clusterName   = "test-" + uniqueID // 缩短名称以避免超过63字符限制
			secretName    = clusterName + "-kc"
		)

		ginkgo.BeforeEach(func(ctx context.Context) {
			// Create namespace for secrets
			_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}})

			// Create kubeconfig secret
			kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: testNamespace},
				Data:       map[string][]byte{"kubeconfig": kc},
			}
			gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())
		})

		ginkgo.It("should delete virtual node when ClusterBinding NodeSelector changes", func(ctx context.Context) {
			// Create initial ClusterBinding with specific NodeSelector
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      clusterName,
					SecretRef:      corev1.SecretReference{Name: secretName, Namespace: testNamespace},
					MountNamespace: "default",
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "node-role.kubernetes.io/worker",
										Operator: corev1.NodeSelectorOpExists,
									},
								},
							},
						},
					},
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			// Create physical node that matches the initial selector
			physicalNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-" + uniqueID,
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
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, physicalNode)).To(gomega.Succeed())

			ginkgo.By("Creating TapestrySyncer and starting it")

			syncer, err := syncerpkg.NewTapestrySyncer(mgrVirtual, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			syncerCtx, syncerCancel := context.WithCancel(ctx)
			defer syncerCancel()

			go func() {
				defer ginkgo.GinkgoRecover()
				_ = syncer.Start(syncerCtx)
			}()

			// Wait for virtual node to be created
			virtualNodeName := "vnode-" + clusterName + "-" + physicalNode.Name
			gomega.Eventually(func() bool {
				var virtualNode corev1.Node
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: virtualNodeName}, &virtualNode)
				return err == nil
			}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue())

			ginkgo.By("Virtual node created successfully")

			// Update ClusterBinding NodeSelector to exclude the node
			gomega.Eventually(func() error {
				var updatedBinding cloudv1beta1.ClusterBinding
				if err := k8sVirtual.Get(ctx, types.NamespacedName{Name: clusterName}, &updatedBinding); err != nil {
					return err
				}

				updatedBinding.Spec.NodeSelector = &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "node-role.kubernetes.io/master",
									Operator: corev1.NodeSelectorOpExists,
								},
							},
						},
					},
				}
				return k8sVirtual.Update(ctx, &updatedBinding)
			}, 10*time.Second, 1*time.Second).Should(gomega.Succeed())

			ginkgo.By("ClusterBinding NodeSelector updated")

			// Wait for virtual node to be deleted
			gomega.Eventually(func() bool {
				var virtualNode corev1.Node
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: virtualNodeName}, &virtualNode)
				return err != nil
			}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue())

			ginkgo.By("Virtual node deleted due to NodeSelector change")

			// Cleanup
			syncerCancel()
			_ = k8sPhysical.Delete(ctx, physicalNode)
			_ = k8sVirtual.Delete(ctx, clusterBinding)
		}, ginkgo.SpecTimeout(60*time.Second))

		ginkgo.It("should calculate available resources correctly when no ResourceLeasingPolicy matches but pods are running", func(ctx context.Context) {
			// Create ClusterBinding without NodeSelector restrictions
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      clusterName,
					SecretRef:      corev1.SecretReference{Name: secretName, Namespace: testNamespace},
					MountNamespace: "default",
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			// Create physical node
			physicalNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-" + uniqueID,
					Labels: map[string]string{
						"kubernetes.io/arch": "amd64",
						"kubernetes.io/os":   "linux",
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
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, physicalNode)).To(gomega.Succeed())

			// Create pods running on the physical node
			pod1 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-1-" + uniqueID,
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: physicalNode.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, pod1)).To(gomega.Succeed())

			pod2 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-2-" + uniqueID,
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: physicalNode.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("0.5"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, pod2)).To(gomega.Succeed())

			// Wait for pods to be indexed properly
			time.Sleep(5 * time.Second)

			// Verify pods are created and associated with the node
			podList := &corev1.PodList{}
			gomega.Expect(k8sPhysical.List(ctx, podList, client.MatchingFields{"spec.nodeName": physicalNode.Name})).To(gomega.Succeed())
			gomega.Expect(podList.Items).To(gomega.HaveLen(2), "Should find 2 pods on the physical node")

			ginkgo.By("Physical node and pods created")

			syncer, err := syncerpkg.NewTapestrySyncer(mgrVirtual, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			syncerCtx, syncerCancel := context.WithCancel(ctx)
			defer syncerCancel()

			go func() {
				defer ginkgo.GinkgoRecover()
				_ = syncer.Start(syncerCtx)
			}()

			// Wait for virtual node to be created
			virtualNodeName := "vnode-" + clusterName + "-" + physicalNode.Name
			var virtualNode corev1.Node
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: virtualNodeName}, &virtualNode)
				return err == nil
			}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue())

			ginkgo.By("Virtual node created")

			// Verify available resources = total - used
			// Expected: CPU: 4 - 1 - 0.5 = 2.5, Memory: 8Gi - 2Gi - 1Gi = 5Gi
			expectedCPU := resource.MustParse("2500m")
			expectedMemory := resource.MustParse("5Gi")

			actualCPU := virtualNode.Status.Allocatable[corev1.ResourceCPU]
			actualMemory := virtualNode.Status.Allocatable[corev1.ResourceMemory]

			gomega.Expect(actualCPU.Cmp(expectedCPU)).To(gomega.Equal(0),
				fmt.Sprintf("Expected CPU %s, got %s", expectedCPU.String(), actualCPU.String()))
			gomega.Expect(actualMemory.Cmp(expectedMemory)).To(gomega.Equal(0),
				fmt.Sprintf("Expected Memory %s, got %s", expectedMemory.String(), actualMemory.String()))

			ginkgo.By("Available resources calculated correctly")

			// Cleanup
			syncerCancel()
			_ = k8sPhysical.Delete(ctx, pod1)
			_ = k8sPhysical.Delete(ctx, pod2)
			_ = k8sPhysical.Delete(ctx, physicalNode)
			_ = k8sVirtual.Delete(ctx, clusterBinding)
		}, ginkgo.SpecTimeout(60*time.Second))

		ginkgo.It("should calculate available resources correctly with single ResourceLeasingPolicy and running pods", func(ctx context.Context) {
			// Create ClusterBinding
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      clusterName,
					SecretRef:      corev1.SecretReference{Name: secretName, Namespace: testNamespace},
					MountNamespace: "default",
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			// Create ResourceLeasingPolicy with quantity limits
			policy := &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "single-policy-" + uniqueID},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: clusterName,
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/arch",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"amd64"},
									},
								},
							},
						},
					},
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: &[]resource.Quantity{resource.MustParse("2")}[0],
						},
						{
							Resource: "memory",
							Quantity: &[]resource.Quantity{resource.MustParse("4Gi")}[0],
						},
					},
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "00:00",
							End:   "23:59",
							Days:  []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
						},
					},
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, policy)).To(gomega.Succeed())

			// Wait for ResourceLeasingPolicy to be indexed properly
			time.Sleep(2 * time.Second)

			// Create physical node
			physicalNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-" + uniqueID,
					Labels: map[string]string{
						"kubernetes.io/arch": "amd64",
						"kubernetes.io/os":   "linux",
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
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, physicalNode)).To(gomega.Succeed())

			// Create pods running on the physical node
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-" + uniqueID,
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: physicalNode.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("0.5"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, pod)).To(gomega.Succeed())

			// Wait for pods to be indexed properly
			time.Sleep(5 * time.Second)

			ginkgo.By("Physical node, ResourceLeasingPolicy and pod created")

			syncer, err := syncerpkg.NewTapestrySyncer(mgrVirtual, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			syncerCtx, syncerCancel := context.WithCancel(ctx)
			defer syncerCancel()

			go func() {
				defer ginkgo.GinkgoRecover()
				_ = syncer.Start(syncerCtx)
			}()

			// Wait for virtual node to be created
			virtualNodeName := "vnode-" + clusterName + "-" + physicalNode.Name
			var virtualNode corev1.Node
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: virtualNodeName}, &virtualNode)
				return err == nil
			}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue())

			ginkgo.By("Virtual node created")

			// Verify available resources = min(total - used, policy limits)
			// Available after pod usage: CPU: 4 - 0.5 = 3.5, Memory: 8Gi - 1Gi = 7Gi
			// Policy limits: CPU: 2, Memory: 4Gi
			// Expected: CPU: min(3.5, 2) = 2, Memory: min(7Gi, 4Gi) = 4Gi
			expectedCPU := resource.MustParse("2")
			expectedMemory := resource.MustParse("4Gi")

			actualCPU := virtualNode.Status.Allocatable[corev1.ResourceCPU]
			actualMemory := virtualNode.Status.Allocatable[corev1.ResourceMemory]

			gomega.Expect(actualCPU.Cmp(expectedCPU)).To(gomega.Equal(0),
				fmt.Sprintf("Expected CPU %s, got %s", expectedCPU.String(), actualCPU.String()))
			gomega.Expect(actualMemory.Cmp(expectedMemory)).To(gomega.Equal(0),
				fmt.Sprintf("Expected Memory %s, got %s", expectedMemory.String(), actualMemory.String()))

			ginkgo.By("Available resources calculated correctly with single policy")

			// Cleanup
			syncerCancel()
			_ = k8sPhysical.Delete(ctx, pod)
			_ = k8sPhysical.Delete(ctx, physicalNode)
			_ = k8sVirtual.Delete(ctx, policy)
			_ = k8sVirtual.Delete(ctx, clusterBinding)
		}, ginkgo.SpecTimeout(60*time.Second))

		ginkgo.It("should calculate available resources correctly with multiple ResourceLeasingPolicies and running pods", func(ctx context.Context) {
			// Create ClusterBinding
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      clusterName,
					SecretRef:      corev1.SecretReference{Name: secretName, Namespace: testNamespace},
					MountNamespace: "default",
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			// Create first ResourceLeasingPolicy (will be selected as it's created first)
			policy1 := &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy-1-" + uniqueID},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: clusterName,
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/arch",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"amd64"},
									},
								},
							},
						},
					},
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: &[]resource.Quantity{resource.MustParse("1.5")}[0],
						},
						{
							Resource: "memory",
							Quantity: &[]resource.Quantity{resource.MustParse("3Gi")}[0],
						},
					},
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "00:00",
							End:   "23:59",
							Days:  []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
						},
					},
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, policy1)).To(gomega.Succeed())

			// Wait for ResourceLeasingPolicy to be indexed properly
			time.Sleep(2 * time.Second)

			// Wait a bit to ensure different creation timestamps
			time.Sleep(100 * time.Millisecond)

			// Create second ResourceLeasingPolicy (will be ignored)
			policy2 := &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "policy-2-" + uniqueID},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: clusterName,
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/arch",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"amd64"},
									},
								},
							},
						},
					},
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: &[]resource.Quantity{resource.MustParse("3")}[0],
						},
						{
							Resource: "memory",
							Quantity: &[]resource.Quantity{resource.MustParse("6Gi")}[0],
						},
					},
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "00:00",
							End:   "23:59",
							Days:  []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
						},
					},
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, policy2)).To(gomega.Succeed())

			// Wait for second ResourceLeasingPolicy to be indexed properly
			time.Sleep(2 * time.Second)

			// Create physical node
			physicalNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-" + uniqueID,
					Labels: map[string]string{
						"kubernetes.io/arch": "amd64",
						"kubernetes.io/os":   "linux",
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
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, physicalNode)).To(gomega.Succeed())

			// Create pod running on the physical node
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-" + uniqueID,
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: physicalNode.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("0.5"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, pod)).To(gomega.Succeed())

			ginkgo.By("Physical node, multiple ResourceLeasingPolicies and pod created")

			syncer, err := syncerpkg.NewTapestrySyncer(mgrVirtual, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			syncerCtx, syncerCancel := context.WithCancel(ctx)
			defer syncerCancel()

			go func() {
				defer ginkgo.GinkgoRecover()
				_ = syncer.Start(syncerCtx)
			}()

			// Wait for virtual node to be created
			virtualNodeName := "vnode-" + clusterName + "-" + physicalNode.Name
			var virtualNode corev1.Node
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: virtualNodeName}, &virtualNode)
				return err == nil
			}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue())

			ginkgo.By("Virtual node created")

			// Verify available resources use the first (earliest) policy
			// Available after pod usage: CPU: 4 - 0.5 = 3.5, Memory: 8Gi - 1Gi = 7Gi
			// First policy limits: CPU: 1.5, Memory: 3Gi
			// Expected: CPU: min(3.5, 1.5) = 1.5, Memory: min(7Gi, 3Gi) = 3Gi
			expectedCPU := resource.MustParse("1500m")
			expectedMemory := resource.MustParse("3Gi")

			actualCPU := virtualNode.Status.Allocatable[corev1.ResourceCPU]
			actualMemory := virtualNode.Status.Allocatable[corev1.ResourceMemory]

			gomega.Expect(actualCPU.Cmp(expectedCPU)).To(gomega.Equal(0),
				fmt.Sprintf("Expected CPU %s, got %s", expectedCPU.String(), actualCPU.String()))
			gomega.Expect(actualMemory.Cmp(expectedMemory)).To(gomega.Equal(0),
				fmt.Sprintf("Expected Memory %s, got %s", expectedMemory.String(), actualMemory.String()))

			ginkgo.By("Available resources calculated correctly with multiple policies (using earliest)")

			// Cleanup
			syncerCancel()
			_ = k8sPhysical.Delete(ctx, pod)
			_ = k8sPhysical.Delete(ctx, physicalNode)
			_ = k8sVirtual.Delete(ctx, policy1)
			_ = k8sVirtual.Delete(ctx, policy2)
			_ = k8sVirtual.Delete(ctx, clusterBinding)
		}, ginkgo.SpecTimeout(60*time.Second))

		ginkgo.It("should calculate available resources correctly with both Quantity and Percent limits", func(ctx context.Context) {
			// Create ClusterBinding
			clusterBinding := &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      clusterName,
					SecretRef:      corev1.SecretReference{Name: secretName, Namespace: testNamespace},
					MountNamespace: "default",
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

			// Create ResourceLeasingPolicy with both quantity and percentage limits
			policy := &cloudv1beta1.ResourceLeasingPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "quantity-percent-policy-" + uniqueID},
				Spec: cloudv1beta1.ResourceLeasingPolicySpec{
					Cluster: clusterName,
					NodeSelector: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/arch",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"amd64"},
									},
								},
							},
						},
					},
					ResourceLimits: []cloudv1beta1.ResourceLimit{
						{
							Resource: "cpu",
							Quantity: &[]resource.Quantity{resource.MustParse("3")}[0], // 3 cores
							Percent:  &[]int32{50}[0],                                  // 50% of available
						},
						{
							Resource: "memory",
							Quantity: &[]resource.Quantity{resource.MustParse("2Gi")}[0], // 2Gi
							Percent:  &[]int32{75}[0],                                    // 75% of available
						},
					},
					TimeWindows: []cloudv1beta1.TimeWindow{
						{
							Start: "00:00",
							End:   "23:59",
							Days:  []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
						},
					},
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, policy)).To(gomega.Succeed())

			// Wait for ResourceLeasingPolicy to be indexed properly
			time.Sleep(2 * time.Second)

			// Create physical node
			physicalNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-" + uniqueID,
					Labels: map[string]string{
						"kubernetes.io/arch": "amd64",
						"kubernetes.io/os":   "linux",
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
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, physicalNode)).To(gomega.Succeed())

			// Create pod running on the physical node
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-" + uniqueID,
					Namespace: "default",
				},
				Spec: corev1.PodSpec{
					NodeName: physicalNode.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, pod)).To(gomega.Succeed())

			ginkgo.By("Physical node, ResourceLeasingPolicy with Quantity+Percent limits and pod created")

			syncer, err := syncerpkg.NewTapestrySyncer(mgrVirtual, k8sVirtual, scheme, clusterBinding.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			syncerCtx, syncerCancel := context.WithCancel(ctx)
			defer syncerCancel()

			go func() {
				defer ginkgo.GinkgoRecover()
				_ = syncer.Start(syncerCtx)
			}()

			// Wait for virtual node to be created
			virtualNodeName := "vnode-" + clusterName + "-" + physicalNode.Name
			var virtualNode corev1.Node
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: virtualNodeName}, &virtualNode)
				return err == nil
			}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue())

			ginkgo.By("Virtual node created")

			// Verify available resources use the more restrictive limit (min of quantity and percentage)
			// Available after pod usage: CPU: 4 - 1 = 3, Memory: 8Gi - 2Gi = 6Gi
			// CPU limits: min(3, 50% of 3) = min(3, 1.5) = 1.5
			// Memory limits: min(2Gi, 75% of 6Gi) = min(2Gi, 4.5Gi) = 2Gi
			expectedCPU := resource.MustParse("1500m")
			expectedMemory := resource.MustParse("2Gi")

			actualCPU := virtualNode.Status.Allocatable[corev1.ResourceCPU]
			actualMemory := virtualNode.Status.Allocatable[corev1.ResourceMemory]

			gomega.Expect(actualCPU.Cmp(expectedCPU)).To(gomega.Equal(0),
				fmt.Sprintf("Expected CPU %s, got %s", expectedCPU.String(), actualCPU.String()))
			gomega.Expect(actualMemory.Cmp(expectedMemory)).To(gomega.Equal(0),
				fmt.Sprintf("Expected Memory %s, got %s", expectedMemory.String(), actualMemory.String()))

			ginkgo.By("Available resources calculated correctly with Quantity and Percent limits")

			// Cleanup
			syncerCancel()
			_ = k8sPhysical.Delete(ctx, pod)
			_ = k8sPhysical.Delete(ctx, physicalNode)
			_ = k8sVirtual.Delete(ctx, policy)
			_ = k8sVirtual.Delete(ctx, clusterBinding)
		}, ginkgo.SpecTimeout(60*time.Second))
	})
})
