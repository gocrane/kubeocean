package integration

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"
	"time"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	syncerpkg "github.com/TKEColocation/kubeocean/pkg/syncer"
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
	testTimeout         = 60 * time.Second
	testPollingInterval = time.Second
	testPodNamespace    = "pod-sync-test"
	testMountNamespace  = "default"
	// testClusterIDValue is the value used for cluster ID labels in tests
	testClusterIDValue = "true"
)

// verifyPhysicalPodNodeAffinity verifies that the physical pod has the correct node affinity
// to schedule to the specified physical node
func verifyPhysicalPodNodeAffinity(physicalPod *corev1.Pod, physicalNodeName string) {
	gomega.Expect(physicalPod.Spec.Affinity).NotTo(gomega.BeNil())
	gomega.Expect(physicalPod.Spec.Affinity.NodeAffinity).NotTo(gomega.BeNil())
	gomega.Expect(physicalPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).NotTo(gomega.BeNil())
	gomega.Expect(physicalPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms).To(gomega.HaveLen(1))
	gomega.Expect(physicalPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields).To(gomega.HaveLen(1))
	gomega.Expect(physicalPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Key).To(gomega.Equal("metadata.name"))
	gomega.Expect(physicalPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Operator).To(gomega.Equal(corev1.NodeSelectorOpIn))
	gomega.Expect(physicalPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Values).To(gomega.Equal([]string{physicalNodeName}))
}

// Kubernetes service environment variable names
const (
	kubernetesServiceHostConst = "KUBERNETES_SERVICE_HOST"
	kubernetesServicePortConst = "KUBERNETES_SERVICE_PORT"
	hostnameEnvVarConst        = "HOSTNAME"
	nodenameEnvVarConst        = "NODENAME"
)

var _ = ginkgo.Describe("Virtual Pod E2E Tests", func() {
	var (
		testCtx            context.Context
		testCancel         context.CancelFunc
		clusterBindingName string
		physicalNodeName   string
		virtualNodeName    string
	)

	ginkgo.BeforeEach(func() {
		testCtx, testCancel = context.WithCancel(context.Background())
		clusterBindingName = fmt.Sprintf("pod-%s", uniqueID)
		physicalNodeName = fmt.Sprintf("physical-node-%s", uniqueID)
		virtualNodeName = fmt.Sprintf("vnode-pod-%s-%s", uniqueID, physicalNodeName)

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
			virtualPod := createTestVirtualPod("test-pod-create", testPodNamespace, virtualNodeName)
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
			// Verify node affinity instead of nodeName
			verifyPhysicalPodNodeAffinity(physicalPod, physicalNodeName)
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
			virtualPod := createTestVirtualPod("test-pod-delete", testPodNamespace, virtualNodeName)
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

		ginkgo.It("should not manage pods on non-kubeocean virtual nodes", func(ctx context.Context) {
			ginkgo.By("Creating a non-kubeocean virtual node")
			nonKubeoceanNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("non-kubeocean-node-%s", uniqueID),
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
			gomega.Expect(k8sVirtual.Create(ctx, nonKubeoceanNode)).To(gomega.Succeed())

			ginkgo.By("Creating virtual pod on non-kubeocean node")
			virtualPod := createTestVirtualPod("test-pod-non-kubeocean", testPodNamespace, nonKubeoceanNode.Name)
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
					Namespace: testPodNamespace,
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
			virtualPod := createTestVirtualPod("test-pod-status-sync", testPodNamespace, virtualNodeName)
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
			virtualPod := createTestVirtualPod("test-pod-metadata-sync", testPodNamespace, virtualNodeName)
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

				// Check annotations (excluding internal Kubeocean annotations)
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
						cloudv1beta1.AnnotationVirtualPodNamespace: testPodNamespace,
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
			virtualPod := createTestVirtualPod("test-pod-recovery", testPodNamespace, virtualNodeName)
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

	ginkgo.Describe("Virtual Pod Ref Resources Tests", func() {
		ginkgo.It("should create and manage physical resources for virtual pod with configmap, secret and pvc references", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating virtual ConfigMap")
			virtualConfigMap := createTestVirtualConfigMap("test-config", testPodNamespace)
			gomega.Expect(k8sVirtual.Create(ctx, virtualConfigMap)).To(gomega.Succeed())

			ginkgo.By("Creating virtual Secret")
			virtualSecret := createTestVirtualSecret("test-secret", testPodNamespace)
			gomega.Expect(k8sVirtual.Create(ctx, virtualSecret)).To(gomega.Succeed())

			ginkgo.By("Creating virtual ConfigMap for init container")
			virtualConfigMapInit := createTestVirtualConfigMapInit("test-config-init", testPodNamespace)
			gomega.Expect(k8sVirtual.Create(ctx, virtualConfigMapInit)).To(gomega.Succeed())

			ginkgo.By("Creating virtual Secret for init container")
			virtualSecretInit := createTestVirtualSecretInit("test-secret-init", testPodNamespace)
			gomega.Expect(k8sVirtual.Create(ctx, virtualSecretInit)).To(gomega.Succeed())

			ginkgo.By("Creating virtual PV")
			virtualPV := createTestVirtualPV("test-pv", "test-pvc", testPodNamespace)
			gomega.Expect(k8sVirtual.Create(ctx, virtualPV)).To(gomega.Succeed())

			ginkgo.By("Creating virtual PVC")
			virtualPVC := createTestVirtualPVC("test-pvc", testPodNamespace, "test-pv")
			gomega.Expect(k8sVirtual.Create(ctx, virtualPVC)).To(gomega.Succeed())

			ginkgo.By("Manually updating virtual PVC to be bound")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-pvc", Namespace: testPodNamespace}, virtualPVC)
				if err != nil {
					return false
				}
				// Manually set PVC to Bound status for testing
				virtualPVC.Status.Phase = corev1.ClaimBound
				err = k8sVirtual.Status().Update(ctx, virtualPVC)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Creating a virtual pod with resource references")
			virtualPod := createTestVirtualPodWithResources("test-pod-refs", testPodNamespace, virtualNodeName, "test-config", "test-secret", "test-pvc")
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
						if pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace] == virtualPod.Namespace &&
							pod.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
							physicalPod = pod
							return true
						}
					}
				}
				return false
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
			physicalPodName := physicalPod.Name
			physicalPodNamespace := physicalPod.Namespace

			ginkgo.By("Verifying physical pod properties")
			gomega.Expect(physicalPod).NotTo(gomega.BeNil())
			// Verify node affinity instead of nodeName
			verifyPhysicalPodNodeAffinity(physicalPod, physicalNodeName)
			gomega.Expect(physicalPod.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))

			// Verify bidirectional mapping annotations
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]).To(gomega.Equal(virtualPod.Namespace))
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodName]).To(gomega.Equal(virtualPod.Name))
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodUID]).To(gomega.Equal(string(virtualPod.UID)))

			// Verify HostAliases injection
			ginkgo.By("Verifying HostAliases injection")
			gomega.Expect(physicalPod.Spec.HostAliases).To(gomega.HaveLen(1))
			gomega.Expect(physicalPod.Spec.HostAliases[0].IP).To(gomega.Equal("10.0.0.1")) // kubernetes-intranet IP
			gomega.Expect(physicalPod.Spec.HostAliases[0].Hostnames).To(gomega.ContainElement("kubernetes.default.svc"))

			// Verify environment variable injection for containers
			ginkgo.By("Verifying environment variable injection for containers")
			gomega.Expect(physicalPod.Spec.Containers).To(gomega.HaveLen(1))
			mainContainer := physicalPod.Spec.Containers[0]

			// Find KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables
			var kubernetesServiceHost, kubernetesServicePort *corev1.EnvVar
			for i := range mainContainer.Env {
				switch mainContainer.Env[i].Name {
				case kubernetesServiceHostConst:
					kubernetesServiceHost = &mainContainer.Env[i]
				case kubernetesServicePortConst:
					kubernetesServicePort = &mainContainer.Env[i]
				}
			}
			gomega.Expect(kubernetesServiceHost).ToNot(gomega.BeNil(), "KUBERNETES_SERVICE_HOST should be injected")
			gomega.Expect(kubernetesServiceHost.Value).To(gomega.Equal("10.0.0.1"))
			gomega.Expect(kubernetesServicePort).ToNot(gomega.BeNil(), "KUBERNETES_SERVICE_PORT should be injected")
			gomega.Expect(kubernetesServicePort.Value).To(gomega.Equal("443"))

			// Verify NODENAME environment variable FieldPath replacement
			ginkgo.By("Verifying NODENAME environment variable FieldPath replacement")
			var nodenameEnvVar *corev1.EnvVar
			for i := range mainContainer.Env {
				if mainContainer.Env[i].Name == nodenameEnvVarConst {
					nodenameEnvVar = &mainContainer.Env[i]
					break
				}
			}
			gomega.Expect(nodenameEnvVar).ToNot(gomega.BeNil(), "NODENAME environment variable should exist")
			gomega.Expect(nodenameEnvVar.ValueFrom).ToNot(gomega.BeNil(), "NODENAME should have ValueFrom")
			gomega.Expect(nodenameEnvVar.ValueFrom.FieldRef).ToNot(gomega.BeNil(), "NODENAME should have FieldRef")
			gomega.Expect(nodenameEnvVar.ValueFrom.FieldRef.FieldPath).To(gomega.Equal(fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName)), "NODENAME FieldPath should be replaced with annotation reference")

			// Verify HOSTNAME environment variable injection
			ginkgo.By("Verifying HOSTNAME environment variable injection")
			var hostnameEnvVar *corev1.EnvVar
			for i := range mainContainer.Env {
				if mainContainer.Env[i].Name == hostnameEnvVarConst {
					hostnameEnvVar = &mainContainer.Env[i]
					break
				}
			}
			gomega.Expect(hostnameEnvVar).ToNot(gomega.BeNil(), "HOSTNAME environment variable should be injected")
			gomega.Expect(hostnameEnvVar.Value).To(gomega.Equal(virtualPod.Name), "HOSTNAME should be set to virtual pod name")
			gomega.Expect(hostnameEnvVar.ValueFrom).To(gomega.BeNil(), "HOSTNAME should not have ValueFrom")

			// Verify environment variable injection for init containers
			ginkgo.By("Verifying environment variable injection for init containers")
			gomega.Expect(physicalPod.Spec.InitContainers).To(gomega.HaveLen(1))
			initContainer := physicalPod.Spec.InitContainers[0]

			// Find KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables in init container
			var initKubernetesServiceHost, initKubernetesServicePort *corev1.EnvVar
			for i := range initContainer.Env {
				switch initContainer.Env[i].Name {
				case kubernetesServiceHostConst:
					initKubernetesServiceHost = &initContainer.Env[i]
				case kubernetesServicePortConst:
					initKubernetesServicePort = &initContainer.Env[i]
				}
			}
			gomega.Expect(initKubernetesServiceHost).ToNot(gomega.BeNil(), "KUBERNETES_SERVICE_HOST should be injected in init container")
			gomega.Expect(initKubernetesServiceHost.Value).To(gomega.Equal("10.0.0.1"))
			gomega.Expect(initKubernetesServicePort).ToNot(gomega.BeNil(), "KUBERNETES_SERVICE_PORT should be injected in init container")
			gomega.Expect(initKubernetesServicePort.Value).To(gomega.Equal("443"))

			// Verify NODENAME environment variable FieldPath replacement in init container
			ginkgo.By("Verifying NODENAME environment variable FieldPath replacement in init container")
			var initNodenameEnvVar *corev1.EnvVar
			for i := range initContainer.Env {
				if initContainer.Env[i].Name == nodenameEnvVarConst {
					initNodenameEnvVar = &initContainer.Env[i]
					break
				}
			}
			gomega.Expect(initNodenameEnvVar).ToNot(gomega.BeNil(), "NODENAME environment variable should exist in init container")
			gomega.Expect(initNodenameEnvVar.ValueFrom).ToNot(gomega.BeNil(), "NODENAME should have ValueFrom in init container")
			gomega.Expect(initNodenameEnvVar.ValueFrom.FieldRef).ToNot(gomega.BeNil(), "NODENAME should have FieldRef in init container")
			gomega.Expect(initNodenameEnvVar.ValueFrom.FieldRef.FieldPath).To(gomega.Equal(fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName)), "NODENAME FieldPath should be replaced with annotation reference in init container")

			// Verify HOSTNAME environment variable injection in init container
			ginkgo.By("Verifying HOSTNAME environment variable injection in init container")
			var initHostnameEnvVar *corev1.EnvVar
			for i := range initContainer.Env {
				if initContainer.Env[i].Name == hostnameEnvVarConst {
					initHostnameEnvVar = &initContainer.Env[i]
					break
				}
			}
			gomega.Expect(initHostnameEnvVar).ToNot(gomega.BeNil(), "HOSTNAME environment variable should be injected in init container")
			gomega.Expect(initHostnameEnvVar.Value).To(gomega.Equal(virtualPod.Name), "HOSTNAME should be set to virtual pod name in init container")
			gomega.Expect(initHostnameEnvVar.ValueFrom).To(gomega.BeNil(), "HOSTNAME should not have ValueFrom in init container")

			// Verify DNS configuration
			ginkgo.By("Verifying DNS configuration")
			// Since virtual pod doesn't specify DNSPolicy, it defaults to ClusterFirst, which should be changed to None
			gomega.Expect(physicalPod.Spec.DNSPolicy).To(gomega.Equal(corev1.DNSNone))
			gomega.Expect(physicalPod.Spec.DNSConfig).ToNot(gomega.BeNil())
			gomega.Expect(physicalPod.Spec.DNSConfig.Nameservers).To(gomega.ContainElement("10.0.0.2")) // kube-dns-intranet IP
			gomega.Expect(physicalPod.Spec.DNSConfig.Options).To(gomega.HaveLen(1))
			gomega.Expect(physicalPod.Spec.DNSConfig.Options[0].Name).To(gomega.Equal("ndots"))
			gomega.Expect(*physicalPod.Spec.DNSConfig.Options[0].Value).To(gomega.Equal("3"))
			gomega.Expect(physicalPod.Spec.DNSConfig.Searches).To(gomega.Equal([]string{
				"default.svc.cluster.local",
				"svc.cluster.local",
				"cluster.local",
			}))

			// Note: Resource name mapping verification will be done after physical resources are created
			// and we have the physical resource names available

			ginkgo.By("Verifying virtual resources have correct labels, annotations and finalizers")
			// Check virtual ConfigMap
			updatedVirtualConfigMap := &corev1.ConfigMap{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-config", Namespace: testPodNamespace}, updatedVirtualConfigMap)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualConfigMap.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualConfigMap.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualConfigMap.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualConfigMap.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == testMountNamespace
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check virtual Secret
			updatedVirtualSecret := &corev1.Secret{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-secret", Namespace: testPodNamespace}, updatedVirtualSecret)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualSecret.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualSecret.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualSecret.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualSecret.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == testMountNamespace
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check virtual ConfigMap for init container
			updatedVirtualConfigMapInit := &corev1.ConfigMap{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-config-init", Namespace: testPodNamespace}, updatedVirtualConfigMapInit)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualConfigMapInit.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualConfigMapInit.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualConfigMapInit.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualConfigMapInit.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == testMountNamespace
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check virtual Secret for init container
			updatedVirtualSecretInit := &corev1.Secret{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-secret-init", Namespace: testPodNamespace}, updatedVirtualSecretInit)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualSecretInit.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualSecretInit.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualSecretInit.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualSecretInit.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == testMountNamespace
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check virtual PV
			updatedVirtualPV := &corev1.PersistentVolume{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-pv"}, updatedVirtualPV)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualPV.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualPV.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualPV.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualPV.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == ""
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check virtual PVC
			updatedVirtualPVC := &corev1.PersistentVolumeClaim{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-pvc", Namespace: testPodNamespace}, updatedVirtualPVC)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualPVC.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualPVC.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualPVC.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualPVC.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == testMountNamespace
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check finalizers - now using cluster-specific finalizers
			expectedFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, clusterBindingName)
			gomega.Expect(updatedVirtualConfigMap.Finalizers).To(gomega.ContainElement(expectedFinalizer))
			gomega.Expect(updatedVirtualSecret.Finalizers).To(gomega.ContainElement(expectedFinalizer))
			gomega.Expect(updatedVirtualConfigMapInit.Finalizers).To(gomega.ContainElement(expectedFinalizer))
			gomega.Expect(updatedVirtualSecretInit.Finalizers).To(gomega.ContainElement(expectedFinalizer))
			gomega.Expect(updatedVirtualPV.Finalizers).To(gomega.ContainElement(expectedFinalizer))
			gomega.Expect(updatedVirtualPVC.Finalizers).To(gomega.ContainElement(expectedFinalizer))

			ginkgo.By("Verifying physical resources are created with correct properties")
			// Check physical ConfigMap
			physicalConfigMapName := updatedVirtualConfigMap.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalConfigMap := &corev1.ConfigMap{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalConfigMapName, Namespace: testMountNamespace}, physicalConfigMap)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalConfigMap.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalConfigMap.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-config"))
			gomega.Expect(physicalConfigMap.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(testPodNamespace))

			// Check physical Secret
			physicalSecretName := updatedVirtualSecret.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalSecret := &corev1.Secret{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalSecretName, Namespace: testMountNamespace}, physicalSecret)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalSecret.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalSecret.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-secret"))
			gomega.Expect(physicalSecret.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(testPodNamespace))

			// Check physical PV
			physicalPVName := updatedVirtualPV.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalPV := &corev1.PersistentVolume{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVName}, physicalPV)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalPV.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalPV.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-pv"))
			gomega.Expect(physicalPV.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(""))

			// Check physical PVC
			physicalPVCName := updatedVirtualPVC.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalPVC := &corev1.PersistentVolumeClaim{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace}, physicalPVC)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalPVC.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalPVC.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-pvc"))
			gomega.Expect(physicalPVC.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(testPodNamespace))

			// Check physical ConfigMap for init container
			physicalConfigMapInitName := updatedVirtualConfigMapInit.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalConfigMapInit := &corev1.ConfigMap{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalConfigMapInitName, Namespace: testMountNamespace}, physicalConfigMapInit)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalConfigMapInit.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalConfigMapInit.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-config-init"))
			gomega.Expect(physicalConfigMapInit.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(testPodNamespace))

			// Check physical Secret for init container
			physicalSecretInitName := updatedVirtualSecretInit.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalSecretInit := &corev1.Secret{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalSecretInitName, Namespace: testMountNamespace}, physicalSecretInit)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalSecretInit.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalSecretInit.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-secret-init"))
			gomega.Expect(physicalSecretInit.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(testPodNamespace))

			// Verify that all resource references in physical pod are mapped to physical resource names
			ginkgo.By("Verifying physical pod resource name mappings")

			// Get the latest physical pod to check resource mappings
			latestPhysicalPod := &corev1.Pod{}
			gomega.Expect(k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPodName, Namespace: physicalPodNamespace}, latestPhysicalPod)).To(gomega.Succeed())

			// Check init container env references
			gomega.Expect(latestPhysicalPod.Spec.InitContainers).To(gomega.HaveLen(1))
			latestInitContainer := latestPhysicalPod.Spec.InitContainers[0]

			// Find init container ConfigMap reference by name
			var initConfigEnv *corev1.EnvVar
			for i := range latestInitContainer.Env {
				if latestInitContainer.Env[i].Name == "INIT_CONFIG_VALUE" {
					initConfigEnv = &latestInitContainer.Env[i]
					break
				}
			}
			gomega.Expect(initConfigEnv).ToNot(gomega.BeNil(), "INIT_CONFIG_VALUE environment variable should exist")
			gomega.Expect(initConfigEnv.ValueFrom.ConfigMapKeyRef.Name).To(gomega.Equal(physicalConfigMapInitName))

			// Find init container Secret reference by name
			var initSecretEnv *corev1.EnvVar
			for i := range latestInitContainer.Env {
				if latestInitContainer.Env[i].Name == "INIT_SECRET_VALUE" {
					initSecretEnv = &latestInitContainer.Env[i]
					break
				}
			}
			gomega.Expect(initSecretEnv).ToNot(gomega.BeNil(), "INIT_SECRET_VALUE environment variable should exist")
			gomega.Expect(initSecretEnv.ValueFrom.SecretKeyRef.Name).To(gomega.Equal(physicalSecretInitName))

			// Check main container env references
			gomega.Expect(latestPhysicalPod.Spec.Containers).To(gomega.HaveLen(1))
			latestMainContainer := latestPhysicalPod.Spec.Containers[0]

			// Find main container ConfigMap reference by name
			var configEnv *corev1.EnvVar
			for i := range latestMainContainer.Env {
				if latestMainContainer.Env[i].Name == "CONFIG_VALUE" {
					configEnv = &latestMainContainer.Env[i]
					break
				}
			}
			gomega.Expect(configEnv).ToNot(gomega.BeNil(), "CONFIG_VALUE environment variable should exist")
			gomega.Expect(configEnv.ValueFrom.ConfigMapKeyRef.Name).To(gomega.Equal(physicalConfigMapName))

			// Find main container Secret reference by name
			var secretEnv *corev1.EnvVar
			for i := range latestMainContainer.Env {
				if latestMainContainer.Env[i].Name == "SECRET_VALUE" {
					secretEnv = &latestMainContainer.Env[i]
					break
				}
			}
			gomega.Expect(secretEnv).ToNot(gomega.BeNil(), "SECRET_VALUE environment variable should exist")
			gomega.Expect(secretEnv.ValueFrom.SecretKeyRef.Name).To(gomega.Equal(physicalSecretName))

			// Check volume references
			gomega.Expect(latestPhysicalPod.Spec.Volumes).To(gomega.HaveLen(3))

			// Verify ConfigMap volume
			configVolume := latestPhysicalPod.Spec.Volumes[0]
			gomega.Expect(configVolume.Name).To(gomega.Equal("config-volume"))
			gomega.Expect(configVolume.ConfigMap.Name).To(gomega.Equal(physicalConfigMapName))

			// Verify Secret volume
			secretVolume := latestPhysicalPod.Spec.Volumes[1]
			gomega.Expect(secretVolume.Name).To(gomega.Equal("secret-volume"))
			gomega.Expect(secretVolume.Secret.SecretName).To(gomega.Equal(physicalSecretName))

			// Verify PVC volume
			pvcVolume := latestPhysicalPod.Spec.Volumes[2]
			gomega.Expect(pvcVolume.Name).To(gomega.Equal("pvc-volume"))
			gomega.Expect(pvcVolume.PersistentVolumeClaim.ClaimName).To(gomega.Equal(physicalPVCName))

			ginkgo.By("Updating virtual ConfigMap and Secret")
			// Update ConfigMap
			updatedVirtualConfigMap.Data["new-key"] = "new-value"
			gomega.Expect(k8sVirtual.Update(ctx, updatedVirtualConfigMap)).To(gomega.Succeed())

			// Update Secret
			updatedVirtualSecret.Data["new-secret-key"] = []byte("new-secret-value")
			gomega.Expect(k8sVirtual.Update(ctx, updatedVirtualSecret)).To(gomega.Succeed())

			ginkgo.By("Verifying physical ConfigMap and Secret are updated")
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalConfigMapName, Namespace: testMountNamespace}, physicalConfigMap)
				if err != nil {
					return false
				}
				return physicalConfigMap.Data["new-key"] == "new-value"
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalSecretName, Namespace: testMountNamespace}, physicalSecret)
				if err != nil {
					return false
				}
				return string(physicalSecret.Data["new-secret-key"]) == "new-secret-value"
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Deleting virtual pod")
			gomega.Expect(k8sVirtual.Delete(ctx, virtualPod)).To(gomega.Succeed())

			ginkgo.By("Verifying physical pod has DeletionTimestamp")
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPod.Name, Namespace: physicalPod.Namespace}, physicalPod)
				if err != nil {
					return false
				}
				return physicalPod.DeletionTimestamp != nil
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

			ginkgo.By("Deleting virtual PVC first")
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualPVC)).To(gomega.Succeed())

			ginkgo.By("Deleting virtual PV")
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualPV)).To(gomega.Succeed())

			ginkgo.By("Verifying physical PVC and PV deletion process")

			// Step 1: Check that physical PVC and PV have deletionTimestamp set
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace}, physicalPVC)
				if err != nil {
					return false
				}
				return physicalPVC.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVName}, physicalPV)
				if err != nil {
					return false
				}
				return physicalPV.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Step 2: Remove finalizers from physical PVC and PV
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace}, physicalPVC)
				if err != nil {
					return false
				}
				physicalPVC.Finalizers = []string{}
				err = k8sPhysical.Update(ctx, physicalPVC)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVName}, physicalPV)
				if err != nil {
					return false
				}
				physicalPV.Finalizers = []string{}
				err = k8sPhysical.Update(ctx, physicalPV)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Step 3: Verify that physical PVC and PV are actually deleted
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace}, physicalPVC)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVName}, physicalPV)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying virtual PVC finalizer is removed")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-pvc", Namespace: testPodNamespace}, updatedVirtualPVC)
				if err != nil {
					return false
				}
				// Check if cluster-specific finalizer is removed
				expectedFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, clusterBindingName)
				for _, finalizer := range updatedVirtualPVC.Finalizers {
					if finalizer == expectedFinalizer {
						return false
					}
				}
				return true
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying virtual PV finalizer is removed")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-pv"}, updatedVirtualPV)
				if err != nil {
					return false
				}
				// Check if cluster-specific finalizer is removed
				expectedFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, clusterBindingName)
				for _, finalizer := range updatedVirtualPV.Finalizers {
					if finalizer == expectedFinalizer {
						return false
					}
				}
				return true
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Deleting virtual ConfigMap and Secret")
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualConfigMap)).To(gomega.Succeed())
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualSecret)).To(gomega.Succeed())

			ginkgo.By("Deleting virtual ConfigMap and Secret for init container")
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualConfigMapInit)).To(gomega.Succeed())
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualSecretInit)).To(gomega.Succeed())

			ginkgo.By("Verifying physical ConfigMap and Secret are deleted")
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalConfigMapName, Namespace: testMountNamespace}, physicalConfigMap)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalSecretName, Namespace: testMountNamespace}, physicalSecret)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalConfigMapInitName, Namespace: testMountNamespace}, physicalConfigMapInit)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalSecretInitName, Namespace: testMountNamespace}, physicalSecretInit)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying virtual ConfigMap and Secret are deleted")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-config", Namespace: testPodNamespace}, updatedVirtualConfigMap)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-secret", Namespace: testPodNamespace}, updatedVirtualSecret)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-config-init", Namespace: testPodNamespace}, updatedVirtualConfigMapInit)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-secret-init", Namespace: testPodNamespace}, updatedVirtualSecretInit)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("should create and manage physical resources for virtual pod with CSI PV and NodePublishSecretRef", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating physical namespace for CSI Secret")
			physicalNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testPodNamespace}}
			gomega.Expect(k8sPhysical.Create(ctx, physicalNamespace)).To(gomega.Succeed())

			ginkgo.By("Creating virtual CSI Secret")
			virtualCSISecret := createTestVirtualSecret("test-csi-secret", testPodNamespace)
			gomega.Expect(k8sVirtual.Create(ctx, virtualCSISecret)).To(gomega.Succeed())

			ginkgo.By("Creating virtual PV with CSI NodePublishSecretRef")
			virtualPV := createTestVirtualPVWithCSI("test-csi-pv", "test-csi-pvc", testPodNamespace, "test-csi-secret")
			gomega.Expect(k8sVirtual.Create(ctx, virtualPV)).To(gomega.Succeed())

			ginkgo.By("Creating virtual PVC")
			virtualPVC := createTestVirtualPVC("test-csi-pvc", testPodNamespace, "test-csi-pv")
			gomega.Expect(k8sVirtual.Create(ctx, virtualPVC)).To(gomega.Succeed())

			ginkgo.By("Manually updating virtual PVC to be bound")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-csi-pvc", Namespace: testPodNamespace}, virtualPVC)
				if err != nil {
					return false
				}
				// Manually set PVC to Bound status for testing
				virtualPVC.Status.Phase = corev1.ClaimBound
				err = k8sVirtual.Status().Update(ctx, virtualPVC)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Creating a virtual pod with PVC reference only")
			virtualPod := createTestVirtualPodWithPVCOnly("test-pod-csi", testPodNamespace, virtualNodeName, "test-csi-pvc")
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
						if pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace] == virtualPod.Namespace &&
							pod.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
							physicalPod = pod
							return true
						}
					}
				}
				return false
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
			physicalPodName := physicalPod.Name
			physicalPodNamespace := physicalPod.Namespace

			ginkgo.By("Verifying physical pod properties")
			gomega.Expect(physicalPod).NotTo(gomega.BeNil())
			// Verify node affinity instead of nodeName
			verifyPhysicalPodNodeAffinity(physicalPod, physicalNodeName)
			gomega.Expect(physicalPod.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))

			// Verify bidirectional mapping annotations
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]).To(gomega.Equal(virtualPod.Namespace))
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodName]).To(gomega.Equal(virtualPod.Name))
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodUID]).To(gomega.Equal(string(virtualPod.UID)))

			// Verify HostAliases injection
			ginkgo.By("Verifying HostAliases injection")
			gomega.Expect(physicalPod.Spec.HostAliases).To(gomega.HaveLen(1))
			gomega.Expect(physicalPod.Spec.HostAliases[0].IP).To(gomega.Equal("10.0.0.1")) // kubernetes-intranet IP
			gomega.Expect(physicalPod.Spec.HostAliases[0].Hostnames).To(gomega.ContainElement("kubernetes.default.svc"))

			// Verify environment variable injection for containers
			ginkgo.By("Verifying environment variable injection for containers")
			gomega.Expect(physicalPod.Spec.Containers).To(gomega.HaveLen(1))
			mainContainer := physicalPod.Spec.Containers[0]

			// Find KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables
			var kubernetesServiceHost, kubernetesServicePort *corev1.EnvVar
			for i := range mainContainer.Env {
				switch mainContainer.Env[i].Name {
				case kubernetesServiceHostConst:
					kubernetesServiceHost = &mainContainer.Env[i]
				case kubernetesServicePortConst:
					kubernetesServicePort = &mainContainer.Env[i]
				}
			}
			gomega.Expect(kubernetesServiceHost).ToNot(gomega.BeNil(), "KUBERNETES_SERVICE_HOST should be injected")
			gomega.Expect(kubernetesServiceHost.Value).To(gomega.Equal("10.0.0.1"))
			gomega.Expect(kubernetesServicePort).ToNot(gomega.BeNil(), "KUBERNETES_SERVICE_PORT should be injected")
			gomega.Expect(kubernetesServicePort.Value).To(gomega.Equal("443"))

			// Verify DNS configuration
			ginkgo.By("Verifying DNS configuration")
			// Since virtual pod doesn't specify DNSPolicy, it defaults to ClusterFirst, which should be changed to None
			gomega.Expect(physicalPod.Spec.DNSPolicy).To(gomega.Equal(corev1.DNSNone))
			gomega.Expect(physicalPod.Spec.DNSConfig).ToNot(gomega.BeNil())
			gomega.Expect(physicalPod.Spec.DNSConfig.Nameservers).To(gomega.ContainElement("10.0.0.2")) // kube-dns-intranet IP
			gomega.Expect(physicalPod.Spec.DNSConfig.Options).To(gomega.HaveLen(1))
			gomega.Expect(physicalPod.Spec.DNSConfig.Options[0].Name).To(gomega.Equal("ndots"))
			gomega.Expect(*physicalPod.Spec.DNSConfig.Options[0].Value).To(gomega.Equal("3"))
			gomega.Expect(physicalPod.Spec.DNSConfig.Searches).To(gomega.Equal([]string{
				"default.svc.cluster.local",
				"svc.cluster.local",
				"cluster.local",
			}))

			ginkgo.By("Verifying virtual resources have correct labels, annotations and finalizers")
			// Check virtual CSI Secret
			updatedVirtualCSISecret := &corev1.Secret{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-csi-secret", Namespace: testPodNamespace}, updatedVirtualCSISecret)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualCSISecret.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualCSISecret.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualCSISecret.Labels[cloudv1beta1.LabelUsedByPV] == testClusterIDValue &&
					updatedVirtualCSISecret.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualCSISecret.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == testPodNamespace
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check virtual PV
			updatedVirtualPV := &corev1.PersistentVolume{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-csi-pv"}, updatedVirtualPV)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualPV.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualPV.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualPV.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualPV.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == ""
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check virtual PVC
			updatedVirtualPVC := &corev1.PersistentVolumeClaim{}
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-csi-pvc", Namespace: testPodNamespace}, updatedVirtualPVC)
				if err != nil {
					return false
				}
				expectedManagedByClusterIDLabel := fmt.Sprintf("%s%s", cloudv1beta1.LabelManagedByClusterIDPrefix, clusterBindingName)
				return updatedVirtualPVC.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					updatedVirtualPVC.Labels[expectedManagedByClusterIDLabel] == testClusterIDValue &&
					updatedVirtualPVC.Annotations[cloudv1beta1.AnnotationPhysicalName] != "" &&
					updatedVirtualPVC.Annotations[cloudv1beta1.AnnotationPhysicalNamespace] == testMountNamespace
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Check finalizers - now using cluster-specific finalizers
			expectedFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, clusterBindingName)
			gomega.Expect(updatedVirtualCSISecret.Finalizers).To(gomega.ContainElement(expectedFinalizer))
			gomega.Expect(updatedVirtualPV.Finalizers).To(gomega.ContainElement(expectedFinalizer))
			gomega.Expect(updatedVirtualPVC.Finalizers).To(gomega.ContainElement(expectedFinalizer))

			ginkgo.By("Verifying physical resources are created with correct properties")
			// Check physical CSI Secret
			physicalCSISecretName := updatedVirtualCSISecret.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalCSISecret := &corev1.Secret{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalCSISecretName, Namespace: testPodNamespace}, physicalCSISecret)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalCSISecret.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalCSISecret.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-csi-secret"))
			gomega.Expect(physicalCSISecret.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(testPodNamespace))
			// Verify CSI secret has the special label
			gomega.Expect(physicalCSISecret.Labels[cloudv1beta1.LabelUsedByPV]).To(gomega.Equal("true"))

			// Check physical PV
			physicalPVName := updatedVirtualPV.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalPV := &corev1.PersistentVolume{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVName}, physicalPV)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalPV.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalPV.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-csi-pv"))
			gomega.Expect(physicalPV.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(""))
			// Verify PV has the correct CSI NodePublishSecretRef
			gomega.Expect(physicalPV.Spec.CSI).NotTo(gomega.BeNil())
			gomega.Expect(physicalPV.Spec.CSI.NodePublishSecretRef).NotTo(gomega.BeNil())
			gomega.Expect(physicalPV.Spec.CSI.NodePublishSecretRef.Name).To(gomega.Equal(physicalCSISecretName))
			gomega.Expect(physicalPV.Spec.CSI.NodePublishSecretRef.Namespace).To(gomega.Equal(testPodNamespace))

			// Check physical PVC
			physicalPVCName := updatedVirtualPVC.Annotations[cloudv1beta1.AnnotationPhysicalName]
			physicalPVC := &corev1.PersistentVolumeClaim{}
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace}, physicalPVC)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Expect(physicalPVC.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
			gomega.Expect(physicalPVC.Annotations[cloudv1beta1.AnnotationVirtualName]).To(gomega.Equal("test-csi-pvc"))
			gomega.Expect(physicalPVC.Annotations[cloudv1beta1.AnnotationVirtualNamespace]).To(gomega.Equal(testPodNamespace))

			ginkgo.By("Deleting virtual pod")
			gomega.Expect(k8sVirtual.Delete(ctx, virtualPod)).To(gomega.Succeed())

			ginkgo.By("Verifying physical pod has DeletionTimestamp")
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPodName, Namespace: physicalPodNamespace}, physicalPod)
				if err != nil {
					return false
				}
				return physicalPod.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Deleting physical pod with GracePeriodSeconds=0")
			gomega.Expect(k8sPhysical.Delete(ctx, physicalPod, &client.DeleteOptions{GracePeriodSeconds: &[]int64{0}[0]})).To(gomega.Succeed())

			ginkgo.By("Verifying virtual pod is deleted")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-pod-csi", Namespace: testPodNamespace}, virtualPod)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Deleting virtual PVC first")
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualPVC)).To(gomega.Succeed())

			ginkgo.By("Deleting virtual PV")
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualPV)).To(gomega.Succeed())

			ginkgo.By("Verifying physical PVC and PV deletion process")

			// Step 1: Check that physical PVC and PV have deletionTimestamp set
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace}, physicalPVC)
				if err != nil {
					return false
				}
				return physicalPVC.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVName}, physicalPV)
				if err != nil {
					return false
				}
				return physicalPV.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Step 2: Remove finalizers from physical PVC and PV
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace}, physicalPVC)
				if err != nil {
					return false
				}
				physicalPVC.Finalizers = []string{}
				err = k8sPhysical.Update(ctx, physicalPVC)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVName}, physicalPV)
				if err != nil {
					return false
				}
				physicalPV.Finalizers = []string{}
				err = k8sPhysical.Update(ctx, physicalPV)
				return err == nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			// Step 3: Verify that physical PVC and PV are actually deleted
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace}, physicalPVC)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPVName}, physicalPV)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying virtual PVC finalizer is removed")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-csi-pvc", Namespace: testPodNamespace}, updatedVirtualPVC)
				if err != nil {
					return false
				}
				// Check if cluster-specific finalizer is removed
				expectedFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, clusterBindingName)
				for _, finalizer := range updatedVirtualPVC.Finalizers {
					if finalizer == expectedFinalizer {
						return false
					}
				}
				return true
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying virtual PV finalizer is removed")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-csi-pv"}, updatedVirtualPV)
				if err != nil {
					return false
				}
				// Check if cluster-specific finalizer is removed
				expectedFinalizer := fmt.Sprintf("%s%s", cloudv1beta1.FinalizerClusterIDPrefix, clusterBindingName)
				for _, finalizer := range updatedVirtualPV.Finalizers {
					if finalizer == expectedFinalizer {
						return false
					}
				}
				return true
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Deleting virtual CSI Secret")
			gomega.Expect(k8sVirtual.Delete(ctx, updatedVirtualCSISecret)).To(gomega.Succeed())

			ginkgo.By("Verifying physical CSI Secret deletion")
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalCSISecretName, Namespace: testPodNamespace}, physicalCSISecret)
				return apierrors.IsNotFound(err) || (err == nil && physicalCSISecret.DeletionTimestamp != nil)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying virtual CSI Secret finalizer is deleted")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-csi-secret", Namespace: testPodNamespace}, updatedVirtualCSISecret)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
		})

		ginkgo.It("should create and manage physical resources for virtual pod with ServiceAccount", func(ctx context.Context) {
			// Wait for virtual node to be ready before creating pods
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Creating a ServiceAccount for the virtual pod")
			serviceAccount := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-serviceaccount",
					Namespace: testPodNamespace,
				},
			}
			err := k8sVirtual.Create(ctx, serviceAccount)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.By("Creating a virtual pod with ServiceAccount")
			virtualPod := createTestVirtualPodWithServiceAccount("test-pod-sa", testPodNamespace, virtualNodeName, "test-serviceaccount")
			err = k8sVirtual.Create(ctx, virtualPod)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.By("Verifying virtual pod has kube-api-access volume")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-pod-sa", Namespace: testPodNamespace}, virtualPod)
				if err != nil {
					return false
				}

				// Check if virtual pod has kube-api-access volume
				for _, volume := range virtualPod.Spec.Volumes {
					if strings.HasPrefix(volume.Name, "kube-api-access-") {
						return true
					}
				}
				return false
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

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
						if pod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace] == virtualPod.Namespace &&
							pod.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
							physicalPod = pod
							return true
						}
					}
				}
				return false
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			physicalPodName := physicalPod.Name
			physicalPodNamespace := physicalPod.Namespace

			ginkgo.By("Verifying physical pod properties")
			gomega.Expect(physicalPod).NotTo(gomega.BeNil())
			// Verify node affinity instead of nodeName
			verifyPhysicalPodNodeAffinity(physicalPod, physicalNodeName)
			gomega.Expect(physicalPod.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))

			// Verify bidirectional mapping annotations
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]).To(gomega.Equal(virtualPod.Namespace))
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodName]).To(gomega.Equal(virtualPod.Name))
			gomega.Expect(physicalPod.Annotations[cloudv1beta1.AnnotationVirtualPodUID]).To(gomega.Equal(string(virtualPod.UID)))

			// Verify HostAliases injection
			ginkgo.By("Verifying HostAliases injection")
			gomega.Expect(physicalPod.Spec.HostAliases).To(gomega.HaveLen(1))
			gomega.Expect(physicalPod.Spec.HostAliases[0].IP).To(gomega.Equal("10.0.0.1")) // kubernetes-intranet IP
			gomega.Expect(physicalPod.Spec.HostAliases[0].Hostnames).To(gomega.ContainElement("kubernetes.default.svc"))

			// Verify environment variable injection for containers
			ginkgo.By("Verifying environment variable injection for containers")
			gomega.Expect(physicalPod.Spec.Containers).To(gomega.HaveLen(1))
			mainContainer := physicalPod.Spec.Containers[0]

			// Find KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT environment variables
			var kubernetesServiceHost, kubernetesServicePort *corev1.EnvVar
			for i := range mainContainer.Env {
				switch mainContainer.Env[i].Name {
				case kubernetesServiceHostConst:
					kubernetesServiceHost = &mainContainer.Env[i]
				case kubernetesServicePortConst:
					kubernetesServicePort = &mainContainer.Env[i]
				}
			}
			gomega.Expect(kubernetesServiceHost).ToNot(gomega.BeNil(), "KUBERNETES_SERVICE_HOST should be injected")
			gomega.Expect(kubernetesServiceHost.Value).To(gomega.Equal("10.0.0.1"))
			gomega.Expect(kubernetesServicePort).ToNot(gomega.BeNil(), "KUBERNETES_SERVICE_PORT should be injected")
			gomega.Expect(kubernetesServicePort.Value).To(gomega.Equal("443"))

			// Verify NODENAME environment variable FieldPath replacement
			ginkgo.By("Verifying NODENAME environment variable FieldPath replacement")
			var nodenameEnvVar *corev1.EnvVar
			for i := range mainContainer.Env {
				if mainContainer.Env[i].Name == nodenameEnvVarConst {
					nodenameEnvVar = &mainContainer.Env[i]
					break
				}
			}
			gomega.Expect(nodenameEnvVar).ToNot(gomega.BeNil(), "NODENAME environment variable should exist")
			gomega.Expect(nodenameEnvVar.ValueFrom).ToNot(gomega.BeNil(), "NODENAME should have ValueFrom")
			gomega.Expect(nodenameEnvVar.ValueFrom.FieldRef).ToNot(gomega.BeNil(), "NODENAME should have FieldRef")
			gomega.Expect(nodenameEnvVar.ValueFrom.FieldRef.FieldPath).To(gomega.Equal(fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualNodeName)), "NODENAME FieldPath should be replaced with annotation reference")

			// Verify HOSTNAME environment variable injection
			ginkgo.By("Verifying HOSTNAME environment variable injection")
			var hostnameEnvVar *corev1.EnvVar
			for i := range mainContainer.Env {
				if mainContainer.Env[i].Name == hostnameEnvVarConst {
					hostnameEnvVar = &mainContainer.Env[i]
					break
				}
			}
			gomega.Expect(hostnameEnvVar).ToNot(gomega.BeNil(), "HOSTNAME environment variable should be injected")
			gomega.Expect(hostnameEnvVar.Value).To(gomega.Equal(virtualPod.Name), "HOSTNAME should be set to virtual pod name")
			gomega.Expect(hostnameEnvVar.ValueFrom).To(gomega.BeNil(), "HOSTNAME should not have ValueFrom")

			// Verify DNS configuration
			ginkgo.By("Verifying DNS configuration")
			// Since virtual pod doesn't specify DNSPolicy, it defaults to ClusterFirst, which should be changed to None
			gomega.Expect(physicalPod.Spec.DNSPolicy).To(gomega.Equal(corev1.DNSNone))
			gomega.Expect(physicalPod.Spec.DNSConfig).ToNot(gomega.BeNil())
			gomega.Expect(physicalPod.Spec.DNSConfig.Nameservers).To(gomega.ContainElement("10.0.0.2")) // kube-dns-intranet IP
			gomega.Expect(physicalPod.Spec.DNSConfig.Options).To(gomega.HaveLen(1))
			gomega.Expect(physicalPod.Spec.DNSConfig.Options[0].Name).To(gomega.Equal("ndots"))
			gomega.Expect(*physicalPod.Spec.DNSConfig.Options[0].Value).To(gomega.Equal("3"))
			gomega.Expect(physicalPod.Spec.DNSConfig.Searches).To(gomega.Equal([]string{
				"default.svc.cluster.local",
				"svc.cluster.local",
				"cluster.local",
			}))

			ginkgo.By("Verifying physical pod has empty serviceAccountName")
			gomega.Expect(physicalPod.Spec.DeprecatedServiceAccount).To(gomega.Equal(""))
			gomega.Expect(physicalPod.Spec.ServiceAccountName).To(gomega.Equal(""))

			ginkgo.By("Verifying physical pod has kube-api-access volumes with correct mappings")
			var kubeApiAccessVolumes []corev1.Volume
			for _, volume := range physicalPod.Spec.Volumes {
				if strings.HasPrefix(volume.Name, "kube-api-access-") {
					kubeApiAccessVolumes = append(kubeApiAccessVolumes, volume)
				}
			}
			gomega.Expect(kubeApiAccessVolumes).ToNot(gomega.BeEmpty())

			// Calculate expected physical secret name using the same logic as the controller
			expectedSecretKey := fmt.Sprintf("%s-%s", virtualPod.Name, virtualPod.UID)
			expectedSecretName := generatePhysicalName(expectedSecretKey, virtualPod.Namespace)

			// Verify each kube-api-access volume has corresponding physical resources
			for _, volume := range kubeApiAccessVolumes {
				if volume.Projected != nil {
					for _, source := range volume.Projected.Sources {
						// 1. Verify ServiceAccountToken is nil in physical pod
						gomega.Expect(source.ServiceAccountToken).To(gomega.BeNil())

						// 2. Verify Secret projection exists and points to the correct physical secret
						if source.Secret != nil {
							gomega.Expect(source.Secret.Name).To(gomega.Equal(expectedSecretName))

							// Verify the physical secret exists
							ginkgo.By(fmt.Sprintf("Verifying physical secret %s exists", expectedSecretName))
							physicalSecret := &corev1.Secret{}
							err := k8sPhysical.Get(ctx, types.NamespacedName{Name: expectedSecretName, Namespace: physicalPodNamespace}, physicalSecret)
							gomega.Expect(err).NotTo(gomega.HaveOccurred())
							gomega.Expect(physicalSecret.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
							gomega.Expect(physicalSecret.Annotations[cloudv1beta1.AnnotationVirtualPodName]).To(gomega.Equal(virtualPod.Name))
							gomega.Expect(physicalSecret.Annotations[cloudv1beta1.AnnotationVirtualPodNamespace]).To(gomega.Equal(virtualPod.Namespace))
							gomega.Expect(physicalSecret.Annotations[cloudv1beta1.AnnotationVirtualPodUID]).To(gomega.Equal(string(virtualPod.UID)))
						}

						// 3. Verify ConfigMap projection uses correct physical name
						if source.ConfigMap != nil {
							configMapName := source.ConfigMap.Name
							if configMapName != "" {
								// Calculate expected physical configmap name
								expectedConfigMapName := generatePhysicalName(configMapName, virtualPod.Namespace)
								gomega.Expect(configMapName).To(gomega.Equal(expectedConfigMapName))

								// Verify the physical configmap exists
								ginkgo.By(fmt.Sprintf("Verifying physical configmap %s exists", configMapName))
								physicalConfigMap := &corev1.ConfigMap{}
								err := k8sPhysical.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: physicalPodNamespace}, physicalConfigMap)
								gomega.Expect(err).NotTo(gomega.HaveOccurred())
								gomega.Expect(physicalConfigMap.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))
							}
						}

						// 4. Verify DownwardAPI fieldPath replacements
						if source.DownwardAPI != nil {
							for _, item := range source.DownwardAPI.Items {
								if item.FieldRef != nil {
									// Verify metadata.namespace is replaced with annotation reference
									if item.FieldRef.FieldPath == "metadata.namespace" {
										expectedFieldPath := fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodNamespace)
										gomega.Expect(item.FieldRef.FieldPath).To(gomega.Equal(expectedFieldPath))
									}
									// Verify metadata.name is replaced with annotation reference
									if item.FieldRef.FieldPath == "metadata.name" {
										expectedFieldPath := fmt.Sprintf("metadata.annotations['%s']", cloudv1beta1.AnnotationVirtualPodName)
										gomega.Expect(item.FieldRef.FieldPath).To(gomega.Equal(expectedFieldPath))
									}
								}
							}
						}
					}
				}
			}

			ginkgo.By("Deleting virtual pod")
			gomega.Expect(k8sVirtual.Delete(ctx, virtualPod)).To(gomega.Succeed())

			ginkgo.By("Verifying physical pod has DeletionTimestamp")
			gomega.Eventually(func() bool {
				err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalPodName, Namespace: physicalPodNamespace}, physicalPod)
				if err != nil {
					return false
				}
				return physicalPod.DeletionTimestamp != nil
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Force deleting physical pod")
			gomega.Expect(k8sPhysical.Delete(ctx, physicalPod, &client.DeleteOptions{GracePeriodSeconds: &[]int64{0}[0]})).To(gomega.Succeed())

			ginkgo.By("Verifying virtual pod is deleted")
			gomega.Eventually(func() bool {
				err := k8sVirtual.Get(ctx, types.NamespacedName{Name: "test-pod-sa", Namespace: testPodNamespace}, virtualPod)
				return apierrors.IsNotFound(err)
			}, testTimeout, testPollingInterval).Should(gomega.BeTrue())

			ginkgo.By("Verifying physical service account token secrets are deleted")
			// Check that all kube-api-access related secrets are deleted from physical cluster
			secrets := &corev1.SecretList{}
			listErr := k8sPhysical.List(ctx, secrets, client.InNamespace(physicalPodNamespace))
			gomega.Expect(listErr).NotTo(gomega.HaveOccurred())

			// Check that all secrets belonging to this pod are deleted
			for _, secret := range secrets.Items {
				if secret.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue &&
					secret.Annotations[cloudv1beta1.AnnotationVirtualPodName] == virtualPod.Name {
					gomega.Eventually(func() bool {
						getErr := k8sPhysical.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: physicalPodNamespace}, &corev1.Secret{})
						return apierrors.IsNotFound(getErr)
					}, testTimeout, testPollingInterval).Should(gomega.BeTrue())
				}
			}
		})
	})
})

// Helper functions

// generatePhysicalName generates physical name using MD5 hash
// Format: name(前30字符)-md5(namespace+"/"+name)
// This replicates the logic from VirtualPodReconciler.generatePhysicalName
func generatePhysicalName(name, namespace string) string {
	// Truncate name to first 30 characters
	truncatedName := name
	if len(name) > 30 {
		truncatedName = name[:30]
	}

	// Generate MD5 hash of "namespace/name"
	input := fmt.Sprintf("%s/%s", namespace, name)
	hash := md5.Sum([]byte(input))
	hashString := fmt.Sprintf("%x", hash)

	// Return format: truncatedName-hashString
	return fmt.Sprintf("%s-%s", truncatedName, hashString)
}

func setupPodSyncTestEnvironment(ctx context.Context, clusterBindingName, physicalNodeName string) {
	ginkgo.By("Setting up pod sync test environment")

	// Create namespace for secrets
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kubeocean-system"}}
	_ = k8sVirtual.Create(ctx, ns)

	// Create test namespace
	testNs := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testPodNamespace}}
	_ = k8sVirtual.Create(ctx, testNs)

	// Create kubeconfig secret
	kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-kc", clusterBindingName),
			Namespace: "kubeocean-system",
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

	// Create a matching ResourceLeasingPolicy
	policy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-test-policy-%s", clusterBindingName)},
		Spec: cloudv1beta1.ResourceLeasingPolicySpec{
			Cluster: clusterBindingName,
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
	gomega.Expect(k8sPhysical.Create(ctx, policy)).To(gomega.Succeed())
}

func createAndStartSyncer(ctx context.Context, clusterBindingName string) *syncerpkg.KubeoceanSyncer {
	ginkgo.By("Creating and starting KubeoceanSyncer")

	syncer, err := syncerpkg.NewKubeoceanSyncer(mgrVirtual, k8sVirtual, scheme, clusterBindingName, 100, 150)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Start syncer in background
	go func() {
		defer ginkgo.GinkgoRecover()
		err := syncer.Start(ctx)
		if err != nil && ctx.Err() == nil {
			ginkgo.Fail(fmt.Sprintf("KubeoceanSyncer failed: %v", err))
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
	_ = k8sVirtual.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testPodNamespace}})
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

func createTestVirtualConfigMap(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app": "test-app",
			},
		},
		Data: map[string]string{
			"config-key": "config-value",
			"app.conf":   "server_port=8080",
		},
	}
}

func createTestVirtualSecret(name, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app": "test-app",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username": []byte("admin"),
			"password": []byte("secret123"),
		},
	}
}

func createTestVirtualConfigMapInit(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
		},
		Data: map[string]string{
			"init-config-key": "init-config-value",
		},
	}
}

func createTestVirtualSecretInit(name, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"init-secret-key": []byte("init-secret-value"),
		},
	}
}

func createTestVirtualPV(name, pvcName, namespace string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app": "test-app",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/tmp/test",
				},
			},
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "PersistentVolumeClaim",
				Name:       pvcName,
				Namespace:  namespace,
			},
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeBound,
		},
	}
}

func createTestVirtualPVWithCSI(name, pvcName, namespace, csiSecretName string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app": "test-app",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "test.csi.k8s.io",
					VolumeHandle: "test-volume-handle",
					NodePublishSecretRef: &corev1.SecretReference{
						Name:      csiSecretName,
						Namespace: namespace,
					},
				},
			},
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "PersistentVolumeClaim",
				Name:       pvcName,
				Namespace:  namespace,
			},
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeBound,
		},
	}
}

func createTestVirtualPVC(name, namespace, pvName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app": "test-app",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
			VolumeName: pvName,
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: corev1.ClaimBound,
		},
	}
}

func createTestVirtualPodWithResources(name, namespace, nodeName, configMapName, secretName, pvcName string) *corev1.Pod {
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
			InitContainers: []corev1.Container{
				{
					Name:    "init-container",
					Image:   "busybox:alpine",
					Command: []string{"sh", "-c", "echo 'Init container completed'"},
					Env: []corev1.EnvVar{
						{
							Name: "INIT_CONFIG_VALUE",
							ValueFrom: &corev1.EnvVarSource{
								ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "test-config-init",
									},
									Key: "init-config-key",
								},
							},
						},
						{
							Name: "INIT_SECRET_VALUE",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "test-secret-init",
									},
									Key: "init-secret-key",
								},
							},
						},
						{
							Name: "NODENAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "spec.nodeName",
								},
							},
						},
					},
				},
			},
			Containers: []corev1.Container{{
				Name:  "test-container",
				Image: "nginx:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
				Env: []corev1.EnvVar{
					{
						Name: "CONFIG_VALUE",
						ValueFrom: &corev1.EnvVarSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: configMapName,
								},
								Key: "config-key",
							},
						},
					},
					{
						Name: "SECRET_VALUE",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: secretName,
								},
								Key: "username",
							},
						},
					},
					{
						Name: "NODENAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: "spec.nodeName",
							},
						},
					},
				},
			}},
			Volumes: []corev1.Volume{
				{
					Name: "config-volume",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: configMapName,
							},
						},
					},
				},
				{
					Name: "secret-volume",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: secretName,
						},
					},
				},
				{
					Name: "pvc-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
			},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{
					Name: secretName,
				},
			},
		},
	}
}

func createTestVirtualPodWithPVCOnly(name, namespace, nodeName, pvcName string) *corev1.Pod {
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
			Volumes: []corev1.Volume{
				{
					Name: "pvc-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
			},
		},
	}
}

func createTestVirtualPodWithServiceAccount(name, namespace, nodeName, serviceAccountName string) *corev1.Pod {
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
			NodeName:                     nodeName,
			ServiceAccountName:           serviceAccountName,
			AutomountServiceAccountToken: &[]bool{true}[0],
			Containers: []corev1.Container{{
				Name:  "test-container",
				Image: "nginx:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
				Env: []corev1.EnvVar{
					{
						Name: "NODENAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: "spec.nodeName",
							},
						},
					},
				},
			}},
			Volumes: []corev1.Volume{
				{
					Name: "kube-api-access-xyz",
					VolumeSource: corev1.VolumeSource{
						Projected: &corev1.ProjectedVolumeSource{
							Sources: []corev1.VolumeProjection{
								{
									ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
										Audience:          "https://kubernetes.default.svc.cluster.local",
										ExpirationSeconds: &[]int64{3607}[0],
										Path:              "token",
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
