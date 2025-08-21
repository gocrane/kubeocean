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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Test constants
	testTimeout         = 30 * time.Second
	testPollingInterval = time.Second
	testNamespace       = "pod-sync-test"
	testMountNamespace  = "default"
)

var _ = ginkgo.Describe("Pod Synchronization E2E Tests", func() {
	var (
		testCtx            context.Context
		testCancel         context.CancelFunc
		clusterBindingName string
		physicalNodeName   string
		virtualNodeName    string
	)

	ginkgo.BeforeEach(func() {
		testCtx, testCancel = context.WithCancel(context.Background())
		clusterBindingName = fmt.Sprintf("pod-sync-cluster-%s", uniqueID)
		physicalNodeName = fmt.Sprintf("physical-node-%s", uniqueID)
		virtualNodeName = fmt.Sprintf("vnode-pod-sync-cluster-%s-%s", uniqueID, physicalNodeName)

		// Setup test environment
		setupPodSyncTestEnvironment(testCtx, clusterBindingName, physicalNodeName)
		_ = createAndStartSyncer(testCtx, clusterBindingName)
	})

	ginkgo.AfterEach(func() {
		if testCancel != nil {
			testCancel()
		}
		cleanupPodSyncTestResources(context.Background(), clusterBindingName)
	})

	ginkgo.Describe("Virtual Pod Lifecycle Management", func() {
		ginkgo.It("should create and manage physical pod for virtual pod", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating a virtual pod in the virtual cluster")
			virtualPod := createTestVirtualPod("test-pod-create", testNamespace, virtualNodeName)
			gomega.Expect(k8sVirtual.Create(ctx, virtualPod)).To(gomega.Succeed())

			ginkgo.By("Waiting for physical pod to be created")
			var physicalPod *corev1.Pod
			gomega.Eventually(func() bool {
				pods := &corev1.PodList{}
				err := k8sPhysical.List(ctx, pods, client.InNamespace(testMountNamespace))
				if err != nil {
					return false
				}

				for i := range pods.Items {
					pod := &pods.Items[i]
					if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
						// Check if this physical pod belongs to our virtual pod
						if pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace] == virtualPod.Namespace &&
							pod.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
							physicalPod = pod
							return true
						}
					}
				}
				return false
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying physical pod properties")
			gomega.Expect(physicalPod).NotTo(gomega.BeNil())
			gomega.Expect(physicalPod.Spec.NodeName).To(gomega.Equal(physicalNodeName))
			gomega.Expect(physicalPod.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))

			// Verify bidirectional mapping annotations
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]).To(gomega.Equal(virtualPod.Namespace))
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodName]).To(gomega.Equal(virtualPod.Name))
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodUID]).To(gomega.Equal(string(virtualPod.UID)))

			ginkgo.By("Verifying virtual pod annotations are updated")
			updatedVirtualPod := &corev1.Pod{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{
					Name: virtualPod.Name, Namespace: virtualPod.Namespace,
				}, updatedVirtualPod)
				if err != nil {
					return false
				}
				return updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace] != "" &&
					updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName] != "" &&
					updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID] != ""
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]).To(gomega.Equal(physicalPod.Namespace))
			gomega.Expect(updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]).To(gomega.Equal(physicalPod.Name))
			gomega.Expect(updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodUID]).To(gomega.Equal(string(physicalPod.UID)))
			ginkgo.By("Verifying physical pod is created")
		}, ginkgo.SpecTimeout(testTimeout))

		ginkgo.It("should handle virtual pod deletion correctly", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating and waiting for virtual pod setup")
			virtualPod := createTestVirtualPod("test-pod-delete", testNamespace, virtualNodeName)
			gomega.Expect(k8sVirtual.Create(ctx, virtualPod)).To(gomega.Succeed())

			// Wait for physical pod creation
			var physicalPodName, physicalPodNamespace string
			gomega.Eventually(func() bool {
				updatedVirtualPod := &corev1.Pod{}
				err := k8sVirtual.Get(ctx, types.NamespacedName{
					Name: virtualPod.Name, Namespace: virtualPod.Namespace,
				}, updatedVirtualPod)
				if err != nil {
					return false
				}
				physicalPodName = updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]
				physicalPodNamespace = updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
				return physicalPodName != "" && physicalPodNamespace != ""
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Deleting virtual pod")
			gomega.Expect(k8sVirtual.Delete(ctx, virtualPod)).To(gomega.Succeed())

			ginkgo.By("Verifying physical pod is being deleted")
			physicalPod := &corev1.Pod{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{
					Name: physicalPodName, Namespace: physicalPodNamespace,
				}, physicalPod)
				// 验证 physical pod 是否被删除
				return err == nil && physicalPod.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// 测试环境无法真的回收 physical pod，这里直接删除
			ginkgo.By("Deleting physical pod with GracePeriodSeconds=0")
			zero := int64(0)
			gomega.Expect(k8sPhysical.Delete(ctx, physicalPod, &client.DeleteOptions{GracePeriodSeconds: &zero})).To(gomega.Succeed())
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{
					Name: physicalPodName, Namespace: physicalPodNamespace,
				}, physicalPod)
				// 验证 physical pod 是否真的被删除
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying virtual pod is deleted")
			gomega.Eventually(func() bool {
				deletedVirtualPod := &corev1.Pod{}
				err := k8sVirtual.Get(ctx, types.NamespacedName{
					Name: virtualPod.Name, Namespace: virtualPod.Namespace,
				}, deletedVirtualPod)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
		}, ginkgo.SpecTimeout(testTimeout))

		ginkgo.It("should not manage pods on non-tapestry virtual nodes", func(ctx context.Context) {
			ginkgo.By("Creating a non-tapestry virtual node")
			nonTapestryNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("non-tapestry-node-%s", uniqueID),
					Labels: map[string]string{
						"node-role.kubernetes.io/worker": "",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					}},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, nonTapestryNode)).To(gomega.Succeed())

			ginkgo.By("Creating virtual pod on non-tapestry node")
			virtualPod := createTestVirtualPod("test-pod-non-tapestry", testNamespace, nonTapestryNode.Name)
			gomega.Expect(k8sVirtual.Create(ctx, virtualPod)).To(gomega.Succeed())

			ginkgo.By("Verifying no physical pod is created")
			gomega.Consistently(func() bool {
				pods := &corev1.PodList{}
				err := k8sPhysical.List(ctx, pods, client.InNamespace(testMountNamespace))
				if err != nil {
					return true // Consider error as no pods found
				}

				for _, pod := range pods.Items {
					if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
						pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace] == virtualPod.Namespace &&
						pod.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
						return false // Found unexpected physical pod
					}
				}
				return true // No matching physical pod found (expected)
			}, 3*time.Second, testPollingInterval).Should(gomega.BeTrue())
		}, ginkgo.SpecTimeout(testTimeout))

		ginkgo.It("should not manage unscheduled virtual pods", func(ctx context.Context) {
			// Wait for virtual node to be ready (for consistency)
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating unscheduled virtual pod")
			virtualPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("unscheduled-pod-%s", uniqueID),
					Namespace: testNamespace,
				},
				Spec: corev1.PodSpec{
					// No NodeName set - unscheduled
					Containers: []corev1.Container{{
						Name:  "test-container",
						Image: "nginx:latest",
					}},
				},
			}
			gomega.Expect(k8sVirtual.Create(ctx, virtualPod)).To(gomega.Succeed())

			ginkgo.By("Verifying no physical pod is created for unscheduled pod")
			gomega.Consistently(func() bool {
				pods := &corev1.PodList{}
				err := k8sPhysical.List(ctx, pods, client.InNamespace(testMountNamespace))
				if err != nil {
					return true
				}

				for _, pod := range pods.Items {
					if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
						pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace] == virtualPod.Namespace &&
						pod.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
						return false
					}
				}
				return true
			}, 3*time.Second, testPollingInterval).Should(gomega.BeTrue())
		}, ginkgo.SpecTimeout(testTimeout))
	})

	ginkgo.Describe("Physical Pod Status Synchronization", func() {
		ginkgo.It("should sync physical pod status to virtual pod", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating virtual pod and waiting for physical pod")
			virtualPod := createTestVirtualPod("test-pod-status-sync", testNamespace, virtualNodeName)
			gomega.Expect(k8sVirtual.Create(ctx, virtualPod)).To(gomega.Succeed())

			var physicalPod *corev1.Pod
			gomega.Eventually(func() bool {
				pods := &corev1.PodList{}
				err := k8sPhysical.List(ctx, pods, client.InNamespace(testMountNamespace))
				if err != nil {
					return false
				}

				for i := range pods.Items {
					pod := &pods.Items[i]
					if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
						pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace] == virtualPod.Namespace &&
						pod.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
						physicalPod = pod
						return true
					}
				}
				return false
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Updating physical pod status")
			physicalPod.Status.Phase = corev1.PodRunning
			physicalPod.Status.PodIP = "10.0.0.100"
			physicalPod.Status.HostIP = "192.168.1.10"
			physicalPod.Status.Conditions = []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}}
			gomega.Expect(k8sPhysical.Status().Update(ctx, physicalPod)).To(gomega.Succeed())

			ginkgo.By("Verifying virtual pod status is synchronized")
			gomega.Eventually(func() bool {
				updatedVirtualPod := &corev1.Pod{}
				err := k8sVirtual.Get(ctx, types.NamespacedName{
					Name: virtualPod.Name, Namespace: virtualPod.Namespace,
				}, updatedVirtualPod)
				if err != nil {
					return false
				}

				return updatedVirtualPod.Status.Phase == corev1.PodRunning &&
					updatedVirtualPod.Status.HostIP == "10.0.0.100" && // HostIP set to PodIP
					len(updatedVirtualPod.Status.Conditions) > 0 &&
					updatedVirtualPod.Status.Conditions[0].Type == corev1.PodReady
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
		}, ginkgo.SpecTimeout(testTimeout))

		ginkgo.It("should sync physical pod labels and annotations", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating virtual pod and waiting for physical pod")
			virtualPod := createTestVirtualPod("test-pod-metadata-sync", testNamespace, virtualNodeName)
			gomega.Expect(k8sVirtual.Create(ctx, virtualPod)).To(gomega.Succeed())

			var physicalPod *corev1.Pod
			gomega.Eventually(func() bool {
				pods := &corev1.PodList{}
				err := k8sPhysical.List(ctx, pods, client.InNamespace(testMountNamespace))
				if err != nil {
					return false
				}

				for i := range pods.Items {
					pod := &pods.Items[i]
					if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
						pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace] == virtualPod.Namespace &&
						pod.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
						physicalPod = pod
						return true
					}
				}
				return false
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Adding labels and annotations to physical pod")
			if physicalPod.Labels == nil {
				physicalPod.Labels = make(map[string]string)
			}
			if physicalPod.Annotations == nil {
				physicalPod.Annotations = make(map[string]string)
			}

			physicalPod.Labels["test-label"] = "test-value"
			physicalPod.Labels["app"] = "test-app"
			physicalPod.Annotations["test-annotation"] = "test-annotation-value"
			physicalPod.Annotations["description"] = "test pod for metadata sync"

			gomega.Expect(k8sPhysical.Update(ctx, physicalPod)).To(gomega.Succeed())

			ginkgo.By("Verifying virtual pod metadata is synchronized")
			gomega.Eventually(func() bool {
				updatedVirtualPod := &corev1.Pod{}
				err := k8sVirtual.Get(ctx, types.NamespacedName{
					Name: virtualPod.Name, Namespace: virtualPod.Namespace,
				}, updatedVirtualPod)
				if err != nil {
					return false
				}

				// Check labels
				if updatedVirtualPod.Labels["test-label"] != "test-value" ||
					updatedVirtualPod.Labels["app"] != "test-app" {
					return false
				}

				// Check annotations (excluding internal Tapestry annotations)
				if updatedVirtualPod.Annotations["test-annotation"] != "test-annotation-value" ||
					updatedVirtualPod.Annotations["description"] != "test pod for metadata sync" {
					return false
				}

				// Verify internal annotations are preserved
				return updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace] != "" &&
					updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName] != ""
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
		}, ginkgo.SpecTimeout(testTimeout))

		ginkgo.It("should handle orphaned physical pods", func(ctx context.Context) {
			ginkgo.By("Creating a physical pod with invalid virtual pod reference")
			orphanedPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("orphaned-pod-%s", uniqueID),
					Namespace: testMountNamespace,
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualPodNamespace: testNamespace,
						cloudv1beta1.AnnotationVirtualPodName:      "non-existent-virtual-pod",
						cloudv1beta1.AnnotationVirtualPodUID:       "fake-uid-12345",
					},
				},
				Spec: corev1.PodSpec{
					NodeName: physicalNodeName,
					Containers: []corev1.Container{{
						Name:  "test-container",
						Image: "nginx:latest",
					}},
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, orphanedPod)).To(gomega.Succeed())

			ginkgo.By("Verifying physical pod is being deleted")
			physicalPod := &corev1.Pod{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{
					Name: orphanedPod.Name, Namespace: orphanedPod.Namespace,
				}, physicalPod)
				// 验证 physical pod 是否被删除
				return err == nil && physicalPod.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// 测试环境无法真的回收 physical pod，这里直接删除
			ginkgo.By("Deleting physical pod with GracePeriodSeconds=0")
			zero := int64(0)
			gomega.Expect(k8sPhysical.Delete(ctx, physicalPod, &client.DeleteOptions{GracePeriodSeconds: &zero})).To(gomega.Succeed())

			ginkgo.By("Verifying orphaned physical pod is deleted")
			gomega.Eventually(func() bool {
				deletedPod := &corev1.Pod{}
				err := k8sPhysical.Get(ctx, types.NamespacedName{
					Name: orphanedPod.Name, Namespace: orphanedPod.Namespace,
				}, deletedPod)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
		}, ginkgo.SpecTimeout(testTimeout))

		ginkgo.It("should handle physical pods without required annotations", func(ctx context.Context) {
			ginkgo.By("Creating physical pod with missing annotations")
			invalidPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("invalid-pod-%s", uniqueID),
					Namespace: testMountNamespace,
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					// Missing required annotations
				},
				Spec: corev1.PodSpec{
					NodeName: physicalNodeName,
					Containers: []corev1.Container{{
						Name:  "test-container",
						Image: "nginx:latest",
					}},
				},
			}
			gomega.Expect(k8sPhysical.Create(ctx, invalidPod)).To(gomega.Succeed())

			ginkgo.By("Verifying physical pod is being deleted")
			physicalPod := &corev1.Pod{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{
					Name: invalidPod.Name, Namespace: invalidPod.Namespace,
				}, physicalPod)
				// 验证 physical pod 是否被删除
				return err == nil && physicalPod.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// 测试环境无法真的回收 physical pod，这里直接删除
			ginkgo.By("Deleting physical pod with GracePeriodSeconds=0")
			zero := int64(0)
			gomega.Expect(k8sPhysical.Delete(ctx, physicalPod, &client.DeleteOptions{GracePeriodSeconds: &zero})).To(gomega.Succeed())

			ginkgo.By("Verifying invalid physical pod is deleted")
			gomega.Eventually(func() bool {
				deletedPod := &corev1.Pod{}
				err := k8sPhysical.Get(ctx, types.NamespacedName{
					Name: invalidPod.Name, Namespace: invalidPod.Namespace,
				}, deletedPod)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
		}, ginkgo.SpecTimeout(testTimeout))
	})

	ginkgo.Describe("Pod Naming and Conflict Resolution", func() {
		ginkgo.It("should generate deterministic physical pod names", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating multiple virtual pods with same name in different namespaces")
			namespace1 := fmt.Sprintf("ns1-%s", uniqueID)
			namespace2 := fmt.Sprintf("ns2-%s", uniqueID)

			// Create namespaces
			ns1 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace1}}
			ns2 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace2}}
			gomega.Expect(k8sVirtual.Create(ctx, ns1)).To(gomega.Succeed())
			gomega.Expect(k8sVirtual.Create(ctx, ns2)).To(gomega.Succeed())

			podName := "same-name-pod"
			virtualPod1 := createTestVirtualPod(podName, namespace1, virtualNodeName)
			virtualPod2 := createTestVirtualPod(podName, namespace2, virtualNodeName)

			gomega.Expect(k8sVirtual.Create(ctx, virtualPod1)).To(gomega.Succeed())
			gomega.Expect(k8sVirtual.Create(ctx, virtualPod2)).To(gomega.Succeed())

			ginkgo.By("Verifying both physical pods are created with different names")
			var physicalPod1Name, physicalPod2Name string

			gomega.Eventually(func() bool {
				updatedPod1 := &corev1.Pod{}
				err1 := k8sVirtual.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace1}, updatedPod1)
				updatedPod2 := &corev1.Pod{}
				err2 := k8sVirtual.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace2}, updatedPod2)

				if err1 != nil || err2 != nil {
					return false
				}

				physicalPod1Name = updatedPod1.Annotations[cloudv1beta1.AnnotationPhysicalPodName]
				physicalPod2Name = updatedPod2.Annotations[cloudv1beta1.AnnotationPhysicalPodName]

				return physicalPod1Name != "" && physicalPod2Name != "" && physicalPod1Name != physicalPod2Name
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying physical pod names are deterministic (contain MD5 hash)")
			gomega.Expect(physicalPod1Name).To(gomega.ContainSubstring(podName))
			gomega.Expect(physicalPod2Name).To(gomega.ContainSubstring(podName))
			gomega.Expect(len(physicalPod1Name)).To(gomega.BeNumerically(">", len(podName))) // Should have hash suffix
			gomega.Expect(len(physicalPod2Name)).To(gomega.BeNumerically(">", len(podName))) // Should have hash suffix
		}, ginkgo.SpecTimeout(testTimeout))
	})

	ginkgo.Describe("Error Recovery and Edge Cases", func() {
		ginkgo.It("should recover when physical pod is manually deleted", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating virtual pod and waiting for physical pod")
			virtualPod := createTestVirtualPod("test-pod-recovery", testNamespace, virtualNodeName)
			gomega.Expect(k8sVirtual.Create(ctx, virtualPod)).To(gomega.Succeed())

			var physicalPodName, physicalPodNamespace string
			gomega.Eventually(func() bool {
				updatedVirtualPod := &corev1.Pod{}
				err := k8sVirtual.Get(ctx, types.NamespacedName{
					Name: virtualPod.Name, Namespace: virtualPod.Namespace,
				}, updatedVirtualPod)
				if err != nil {
					return false
				}
				physicalPodName = updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodName]
				physicalPodNamespace = updatedVirtualPod.Annotations[cloudv1beta1.AnnotationPhysicalPodNamespace]
				return physicalPodName != "" && physicalPodNamespace != ""
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Manually deleting physical pod")
			physicalPod := &corev1.Pod{}
			gomega.Expect(k8sPhysical.Get(ctx, types.NamespacedName{
				Name: physicalPodName, Namespace: physicalPodNamespace,
			}, physicalPod)).To(gomega.Succeed())
			// 测试环境无法真的回收 physical pod，这里直接删除
			zero := int64(0)
			gomega.Expect(k8sPhysical.Delete(ctx, physicalPod, &client.DeleteOptions{GracePeriodSeconds: &zero})).To(gomega.Succeed())

			ginkgo.By("Verifying virtual pod status is updated to Failed")
			gomega.Eventually(func() bool {
				updatedVirtualPod := &corev1.Pod{}
				err := k8sVirtual.Get(ctx, types.NamespacedName{
					Name: virtualPod.Name, Namespace: virtualPod.Namespace,
				}, updatedVirtualPod)
				if err != nil {
					return false
				}
				return updatedVirtualPod.Status.Phase == corev1.PodFailed &&
					updatedVirtualPod.Status.Reason == "PhysicalPodLost"
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
		}, ginkgo.SpecTimeout(testTimeout))
	})
})

// Helper functions

func setupPodSyncTestEnvironment(ctx context.Context, clusterBindingName, physicalNodeName string) {
	ginkgo.By("Setting up pod sync test environment")

	// Create namespace for secrets
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tapestry-system"}}
	_ = k8sVirtual.Create(ctx, ns)

	// Create test namespace
	testNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	_ = k8sVirtual.Create(ctx, testNs)

	// Create kubeconfig secret
	kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-kc", clusterBindingName),
			Namespace: "tapestry-system",
		},
		Data: map[string][]byte{"kubeconfig": kc},
	}
	gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

	// Create ClusterBinding
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterBindingName},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID: clusterBindingName,
			SecretRef: corev1.SecretReference{
				Name:      secret.Name,
				Namespace: secret.Namespace,
			},
			MountNamespace: testMountNamespace,
		},
	}
	gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

	// Create physical node
	physicalNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: physicalNodeName,
			Labels: map[string]string{
				"node-role.kubernetes.io/worker": "",
				"kubernetes.io/arch":             "amd64",
				"kubernetes.io/os":               "linux",
			},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			}},
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
}

func createAndStartSyncer(ctx context.Context, clusterBindingName string) *syncerpkg.TapestrySyncer {
	ginkgo.By("Creating and starting TapestrySyncer")

	syncer, err := syncerpkg.NewTapestrySyncer(mgrVirtual, k8sVirtual, scheme, clusterBindingName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Start syncer in background
	go func() {
		defer ginkgo.GinkgoRecover()
		err := syncer.Start(ctx)
		if err != nil && ctx.Err() == nil {
			ginkgo.Fail(fmt.Sprintf("TapestrySyncer failed: %v", err))
		}
	}()

	return syncer
}

// waitForVirtualNodeReady waits for the virtual node to be created and ready
func waitForVirtualNodeReady(ctx context.Context, virtualNodeName string) {
	ginkgo.By(fmt.Sprintf("Waiting for virtual node %s to be ready", virtualNodeName))

	gomega.Eventually(func() bool {
		virtualNode := &corev1.Node{}
		err := k8sVirtual.Get(ctx, types.NamespacedName{Name: virtualNodeName}, virtualNode)
		if err != nil {
			return false
		}

		// Check if node is ready
		for _, condition := range virtualNode.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(),
		fmt.Sprintf("Virtual node %s should be ready", virtualNodeName))

	ginkgo.By(fmt.Sprintf("Virtual node %s is ready", virtualNodeName))
}

func createTestVirtualPod(name, namespace, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app": "test-app",
			},
			Annotations: map[string]string{
				"test-annotation": "test-value",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{{
				Name:  "test-container",
				Image: "nginx:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
			}},
		},
	}
}

func cleanupPodSyncTestResources(ctx context.Context, clusterBindingName string) {
	// Clean up virtual cluster resources
	_ = k8sVirtual.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}})
	_ = k8sVirtual.Delete(ctx, &cloudv1beta1.ClusterBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterBindingName}})

	// Clean up physical cluster resources
	pods := &corev1.PodList{}
	_ = k8sPhysical.List(ctx, pods, client.InNamespace(testMountNamespace))
	for _, pod := range pods.Items {
		if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
			_ = k8sPhysical.Delete(ctx, &pod)
		}
	}

	nodes := &corev1.NodeList{}
	_ = k8sPhysical.List(ctx, nodes)
	for _, node := range nodes.Items {
		if node.Labels["node-role.kubernetes.io/worker"] == "" {
			_ = k8sPhysical.Delete(ctx, &node)
		}
	}
}
