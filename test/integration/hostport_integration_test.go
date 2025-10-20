package integration

import (
	"context"
	"fmt"
	"sort"
	"time"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	syncerpkg "github.com/gocrane/kubeocean/pkg/syncer"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// HostPort test constants
	hostPortTestTimeout         = 120 * time.Second
	hostPortTestPollingInterval = 2 * time.Second
	fakePodNamespace            = "kubeocean-fake"
	systemNodeCriticalPriority  = "system-node-critical"
	// HostIP constants
	hostIPAny       = "0.0.0.0"
	hostIPLocalhost = "127.0.0.1"
)

var _ = ginkgo.Describe("HostPort Integration Tests", func() {
	var (
		testCtx            context.Context
		testCancel         context.CancelFunc
		clusterBindingName string
		physicalNodeName   string
		virtualNodeName    string
	)

	ginkgo.BeforeEach(func() {
		testCtx, testCancel = context.WithCancel(context.Background())
		clusterBindingName = fmt.Sprintf("hostport-%s", uniqueID)
		physicalNodeName = fmt.Sprintf("physical-node-%s", uniqueID)
		virtualNodeName = fmt.Sprintf("vnode-%s-%s", clusterBindingName, physicalNodeName)

		// Setup test environment
		setupHostPortTestEnvironment(testCtx, clusterBindingName, physicalNodeName)
		createAndStartHostPortSyncer(testCtx, clusterBindingName)
	})

	ginkgo.AfterEach(func() {
		if testCancel != nil {
			testCancel()
		}
		cleanupHostPortTestResources(context.Background(), clusterBindingName, physicalNodeName)
	})

	ginkgo.Describe("HostPort Synchronization", func() {
		ginkgo.It("should handle complete hostPort lifecycle with comprehensive scenarios", func(ctx context.Context) {
			ginkgo.By("Step 1: Creating multiple physical pods with various hostPort configurations")
			createPhysicalHostPortPods(ctx, physicalNodeName)

			ginkgo.By("Step 2: Environment preparation - waiting for virtual node to be ready")
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Verifying fake pod creation with expected configurations")
			verifyFakePodCreation(ctx, virtualNodeName)

			ginkgo.By("Step 3: Creating virtual pods with hostPort and verifying physical pods creation")
			createVirtualHostPortPods(ctx, virtualNodeName)

			ginkgo.By("Verifying fake pod persistence after virtual-to-physical pod creation")
			time.Sleep(2 * time.Second)
			verifyFakePodPersistence(ctx, virtualNodeName)

			ginkgo.By("Step 4: Adding new physical pods with different hostPort configurations and removing one old pod")
			createNewPhysicalHostPortPods(ctx)
			deleteSpecificPhysicalPod(ctx, "hostport-pod1")

			ginkgo.By("Verifying fake pod update and old fake pod cleanup")
			verifyFakePodUpdate(ctx, virtualNodeName)

			ginkgo.By("Step 5: Cleaning up all non-kubeocean managed physical pods")
			cleanupPhysicalPods(ctx)

			ginkgo.By("Verifying fake pod cleanup")
			verifyFakePodCleanup(ctx, virtualNodeName)
		})

		ginkgo.It("should handle fake pod lifecycle when virtual node is deleted and recreated", func(ctx context.Context) {
			ginkgo.By("Step 1: Waiting for virtual node to be ready")
			waitForVirtualNodeReady(ctx, virtualNodeName)

			ginkgo.By("Step 2: Creating multiple physical pods with hostPort configurations")
			createPhysicalHostPortPods(ctx, physicalNodeName)

			ginkgo.By("Step 3: Waiting for virtual node to be ready and verifying fake pod creation")
			verifyFakePodCreation(ctx, virtualNodeName)

			ginkgo.By("Step 4: Deleting ResourceLeasingPolicy to trigger virtual node deletion")
			deleteResourceLeasingPolicy(ctx)
			verifyVirtualNodeDeleted(ctx, virtualNodeName)
			verifyFakePodCleanup(ctx, virtualNodeName)

			ginkgo.By("Step 5: Recreating ResourceLeasingPolicy to trigger virtual node recreation")
			recreateResourceLeasingPolicy(ctx, clusterBindingName)
			waitForVirtualNodeReady(ctx, virtualNodeName)
			verifyFakePodCreation(ctx, virtualNodeName)

			ginkgo.By("Step 6: Deleting physical node to trigger final cleanup")
			deletePhysicalNode(ctx, physicalNodeName)
			verifyVirtualNodeDeleted(ctx, virtualNodeName)
			verifyFakePodCleanup(ctx, virtualNodeName)
		})
	})
})

// setupHostPortTestEnvironment sets up the test environment for hostport tests
func setupHostPortTestEnvironment(ctx context.Context, clusterBindingName, physicalNodeName string) {
	ginkgo.By("Setting up hostport test environment")

	// Create kubeocean-system namespace in virtual cluster
	systemNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kubeocean-system"},
	}
	_ = k8sVirtual.Create(ctx, systemNS)

	// Create kubeconfig secret
	kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterBindingName + "-kc",
			Namespace: "kubeocean-system",
		},
		Data: map[string][]byte{"kubeconfig": kc},
	}
	gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

	// Create ClusterBinding resource
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterBindingName},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      clusterBindingName,
			SecretRef:      corev1.SecretReference{Name: clusterBindingName + "-kc", Namespace: "kubeocean-system"},
			MountNamespace: "default",
		},
	}
	gomega.Expect(k8sVirtual.Create(ctx, clusterBinding)).To(gomega.Succeed())

	// Create physical node
	physicalNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: physicalNodeName,
			Labels: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
		},
		Status: corev1.NodeStatus{
			Phase: corev1.NodeRunning,
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
					Reason: "KubeletReady",
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

	// Create ResourceLeasingPolicy
	policy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hostport-policy-" + uniqueID,
		},
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

// createAndStartHostPortSyncer creates and starts the syncer
func createAndStartHostPortSyncer(ctx context.Context, clusterBindingName string) {
	ginkgo.By("Creating and starting KubeoceanSyncer for hostport test")

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
}

// createPhysicalHostPortPods creates multiple physical pods with various hostPort configurations
func createPhysicalHostPortPods(ctx context.Context, physicalNodeName string) {
	// Pod 1: hostip and protocol same, hostport different (8080, 8081)
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostport-pod1",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: physicalNodeName,
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "http1", ContainerPort: 80, HostPort: 8080, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
						{Name: "http2", ContainerPort: 81, HostPort: 8081, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
					},
				},
			},
		},
	}

	// Pod 2: hostip and hostport same, protocol different (TCP, UDP)
	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostport-pod2",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: physicalNodeName,
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "tcp", ContainerPort: 9000, HostPort: 9000, Protocol: corev1.ProtocolTCP, HostIP: hostIPLocalhost},
						{Name: "udp", ContainerPort: 9001, HostPort: 9000, Protocol: corev1.ProtocolUDP, HostIP: hostIPLocalhost},
					},
				},
			},
		},
	}

	// pod3, pod4 hostPort and protocol same, hostip different
	pod3 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostport-pod3",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: physicalNodeName,
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "http1", ContainerPort: 80, HostPort: 8000, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
						{Name: "udp", ContainerPort: 9001, HostPort: 9001, Protocol: corev1.ProtocolUDP, HostIP: hostIPLocalhost},
						{Name: "port1", ContainerPort: 7000, HostPort: 7000, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
					},
				},
			},
		},
	}

	pod4 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostport-pod4",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: physicalNodeName,
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "http2", ContainerPort: 81, HostPort: 8001, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
						{Name: "tcp", ContainerPort: 9000, HostPort: 9001, Protocol: corev1.ProtocolTCP, HostIP: hostIPLocalhost},
						{Name: "port2", ContainerPort: 7001, HostPort: 7000, Protocol: corev1.ProtocolTCP, HostIP: hostIPLocalhost},
					},
				},
			},
		},
	}

	// Pod 5: hostNetwork pod with multiple hostPorts
	pod5 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostport-pod5-hostnet",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName:    physicalNodeName,
			HostNetwork: true,
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "api", ContainerPort: 6000, HostPort: 6000, Protocol: corev1.ProtocolTCP},
						{Name: "metrics", ContainerPort: 6001, HostPort: 6001, Protocol: corev1.ProtocolTCP},
					},
				},
			},
		},
	}

	// Pod 6: Regular pod with init container hostPorts
	pod6 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostport-pod6-init",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: physicalNodeName,
			InitContainers: []corev1.Container{
				{
					Name:  "init-container",
					Image: "busybox:latest",
					Ports: []corev1.ContainerPort{
						{Name: "init1", ContainerPort: 5000, HostPort: 5000, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
						{Name: "init2", ContainerPort: 5001, HostPort: 5001, Protocol: corev1.ProtocolUDP, HostIP: hostIPAny},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "main-container",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "main", ContainerPort: 5002, HostPort: 5002, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
					},
				},
			},
		},
	}

	// Pod 7: hostNetwork pod with init containers
	pod7 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostport-pod7-hostnet-init",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName:    physicalNodeName,
			HostNetwork: true,
			InitContainers: []corev1.Container{
				{
					Name:  "init-container",
					Image: "busybox:latest",
					Ports: []corev1.ContainerPort{
						{Name: "setup1", ContainerPort: 4000, HostPort: 4000, Protocol: corev1.ProtocolTCP},
						{Name: "setup2", ContainerPort: 4001, HostPort: 4001, Protocol: corev1.ProtocolTCP},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "app-container",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "app1", ContainerPort: 4002, HostPort: 4002, Protocol: corev1.ProtocolTCP},
						{Name: "app2", ContainerPort: 4003, HostPort: 4003, Protocol: corev1.ProtocolUDP},
					},
				},
			},
		},
	}

	pods := []*corev1.Pod{pod1, pod2, pod3, pod4, pod5, pod6, pod7}
	for _, pod := range pods {
		gomega.Expect(k8sPhysical.Create(ctx, pod)).To(gomega.Succeed())
		ginkgo.GinkgoWriter.Printf("Created physical pod: %s\n", pod.Name)
	}
}

// verifyFakePodCreation verifies that fake pod is created with correct configurations
func verifyFakePodCreation(ctx context.Context, virtualNodeName string) {
	var fakePod *corev1.Pod

	ginkgo.GinkgoWriter.Printf("Looking for fake pods in namespace: %s\n", fakePodNamespace)

	// Wait for fake pod to be created
	gomega.Eventually(func() bool {
		fakePods := &corev1.PodList{}
		err := k8sVirtual.List(ctx, fakePods,
			client.InNamespace(fakePodNamespace),
			client.MatchingLabels{
				cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
				cloudv1beta1.LabelManagedBy:       cloudv1beta1.LabelManagedByValue,
			})
		if err != nil {
			ginkgo.GinkgoWriter.Printf("Failed to list fake pods: %v\n", err)
			return false
		}

		ginkgo.GinkgoWriter.Printf("Found %d fake pods\n", len(fakePods.Items))

		// Find the fake pod for our virtual node
		for i := range fakePods.Items {
			if fakePods.Items[i].Spec.NodeName == virtualNodeName {
				fakePod = &fakePods.Items[i]
				ginkgo.GinkgoWriter.Printf("Found fake pod: %s on node: %s\n", fakePod.Name, fakePod.Spec.NodeName)
				// ginkgo.GinkgoWriter.Printf("Fake pod ports: %+v\n", fakePod.Spec.Containers[0].Ports)
				return len(fakePod.Spec.Containers) == 1 && len(fakePod.Spec.Containers[0].Ports) == 19
			}
		}
		return false
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.BeTrue(),
		fmt.Sprintf("Fake pod should be created for virtual node %s", virtualNodeName))

	gomega.Expect(fakePod).NotTo(gomega.BeNil())

	// Verify fake pod basic properties
	gomega.Expect(fakePod.Spec.NodeName).To(gomega.Equal(virtualNodeName))
	gomega.Expect(fakePod.Spec.PriorityClassName).To(gomega.Equal(systemNodeCriticalPriority))
	gomega.Expect(fakePod.Labels[cloudv1beta1.LabelHostPortFakePod]).To(gomega.Equal(cloudv1beta1.LabelValueTrue))
	gomega.Expect(fakePod.Labels[cloudv1beta1.LabelManagedBy]).To(gomega.Equal(cloudv1beta1.LabelManagedByValue))

	// Verify tolerations (should tolerate all taints)
	gomega.Expect(fakePod.Spec.Tolerations).To(gomega.ContainElement(corev1.Toleration{
		Operator: corev1.TolerationOpExists,
	}))

	// Verify hostPorts configuration
	gomega.Expect(fakePod.Spec.Containers).To(gomega.HaveLen(1))
	container := fakePod.Spec.Containers[0]

	// Expected hostPorts from all 7 pods (sorted by HostIP, Protocol, HostPort)
	expectedPorts := []corev1.ContainerPort{
		// HostIP: hostIPAny, Protocol: TCP (sorted by HostPort)
		{ContainerPort: 5000, HostPort: 5000, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny}, // Pod6-init
		{ContainerPort: 5002, HostPort: 5002, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny}, // Pod6-main
		{ContainerPort: 7000, HostPort: 7000, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny}, // Pod3
		{ContainerPort: 80, HostPort: 8000, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},   // Pod3
		{ContainerPort: 81, HostPort: 8001, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},   // Pod4
		{ContainerPort: 80, HostPort: 8080, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},   // Pod1
		{ContainerPort: 81, HostPort: 8081, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},   // Pod1
		// HostIP: hostIPAny, Protocol: UDP (sorted by HostPort)
		{ContainerPort: 5001, HostPort: 5001, Protocol: corev1.ProtocolUDP, HostIP: hostIPAny}, // Pod6-init
		// HostIP: hostIPLocalhost, Protocol: TCP (sorted by HostPort)
		{ContainerPort: 7001, HostPort: 7000, Protocol: corev1.ProtocolTCP, HostIP: hostIPLocalhost}, // Pod4
		{ContainerPort: 9000, HostPort: 9000, Protocol: corev1.ProtocolTCP, HostIP: hostIPLocalhost}, // Pod2
		{ContainerPort: 9000, HostPort: 9001, Protocol: corev1.ProtocolTCP, HostIP: hostIPLocalhost}, // Pod4
		// HostIP: hostIPLocalhost, Protocol: UDP (sorted by HostPort)
		{ContainerPort: 9001, HostPort: 9000, Protocol: corev1.ProtocolUDP, HostIP: hostIPLocalhost}, // Pod2
		{ContainerPort: 9001, HostPort: 9001, Protocol: corev1.ProtocolUDP, HostIP: hostIPLocalhost}, // Pod3
		// HostIP: "" (hostNetwork), Protocol: TCP (sorted by HostPort)
		{ContainerPort: 4000, HostPort: 4000, Protocol: corev1.ProtocolTCP, HostIP: ""}, // Pod7-init
		{ContainerPort: 4001, HostPort: 4001, Protocol: corev1.ProtocolTCP, HostIP: ""}, // Pod7-init
		{ContainerPort: 4002, HostPort: 4002, Protocol: corev1.ProtocolTCP, HostIP: ""}, // Pod7-main
		{ContainerPort: 6000, HostPort: 6000, Protocol: corev1.ProtocolTCP, HostIP: ""}, // Pod5
		{ContainerPort: 6001, HostPort: 6001, Protocol: corev1.ProtocolTCP, HostIP: ""}, // Pod5
		// HostIP: "" (hostNetwork), Protocol: UDP (sorted by HostPort)
		{ContainerPort: 4003, HostPort: 4003, Protocol: corev1.ProtocolUDP, HostIP: ""}, // Pod7-main
	}
	for i := range expectedPorts {
		if expectedPorts[i].HostIP == "" {
			expectedPorts[i].HostIP = hostIPAny
		}
		if expectedPorts[i].Protocol == "" {
			expectedPorts[i].Protocol = corev1.ProtocolTCP
		}
	}

	ginkgo.GinkgoWriter.Printf("Expected %d ports, actual %d ports\n", len(expectedPorts), len(container.Ports))
	ginkgo.GinkgoWriter.Printf("Expected ports: %+v\n", expectedPorts)
	ginkgo.GinkgoWriter.Printf("Actual ports: %+v\n", container.Ports)

	// Verify the number of ports (should be 19 ports from all 7 pods)
	gomega.Expect(container.Ports).To(gomega.HaveLen(19))

	// Sort both slices to ensure consistent comparison
	sortPorts := func(ports []corev1.ContainerPort) {
		sort.Slice(ports, func(i, j int) bool {
			if ports[i].HostIP != ports[j].HostIP {
				return ports[i].HostIP < ports[j].HostIP
			}
			if ports[i].Protocol != ports[j].Protocol {
				return ports[i].Protocol < ports[j].Protocol
			}
			return ports[i].HostPort < ports[j].HostPort
		})
	}

	actualPorts := make([]corev1.ContainerPort, len(container.Ports))
	copy(actualPorts, container.Ports)
	sortPorts(actualPorts)
	sortPorts(expectedPorts)
	for i := range expectedPorts {
		expectedPorts[i].Name = fmt.Sprintf("port-%d", i)
	}

	// Compare sorted ports
	for i, expectedPort := range expectedPorts {
		actualPort := actualPorts[i]
		gomega.Expect(actualPort.HostPort).To(gomega.Equal(expectedPort.HostPort),
			fmt.Sprintf("Port %d: HostPort mismatch", i))
		gomega.Expect(actualPort.Protocol).To(gomega.Equal(expectedPort.Protocol),
			fmt.Sprintf("Port %d: Protocol mismatch", i))
		gomega.Expect(actualPort.HostIP).To(gomega.Equal(expectedPort.HostIP),
			fmt.Sprintf("Port %d: HostIP mismatch", i))
	}

	ginkgo.GinkgoWriter.Printf("Verified fake pod creation with %d hostPorts\n", len(container.Ports))
}

// createVirtualHostPortPods creates virtual pods with hostPort configurations
func createVirtualHostPortPods(ctx context.Context, virtualNodeName string) {
	// Virtual Pod 1: Regular pod with multiple hostPorts
	virtualPod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "virtual-hostport-pod1",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: virtualNodeName,
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "web1", ContainerPort: 8080, HostPort: 3000, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
						{Name: "web2", ContainerPort: 8081, HostPort: 3001, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
						{Name: "api", ContainerPort: 8082, HostPort: 3002, Protocol: corev1.ProtocolUDP, HostIP: hostIPLocalhost},
					},
				},
			},
		},
	}

	// Virtual Pod 2: hostNetwork pod with multiple hostPorts
	virtualPod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "virtual-hostport-pod2-hostnet",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName:    virtualNodeName,
			HostNetwork: true,
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "svc1", ContainerPort: 2000, HostPort: 2000, Protocol: corev1.ProtocolTCP},
						{Name: "svc2", ContainerPort: 2001, HostPort: 2001, Protocol: corev1.ProtocolTCP},
						{Name: "metrics", ContainerPort: 2002, HostPort: 2002, Protocol: corev1.ProtocolUDP},
					},
				},
			},
		},
	}

	// Create virtual pods
	gomega.Expect(k8sVirtual.Create(ctx, virtualPod1)).To(gomega.Succeed())
	gomega.Expect(k8sVirtual.Create(ctx, virtualPod2)).To(gomega.Succeed())

	// Wait for physical pods to be created
	gomega.Eventually(func() int {
		physicalPods := &corev1.PodList{}
		err := k8sPhysical.List(ctx, physicalPods, client.InNamespace("default"))
		if err != nil {
			return 0
		}

		count := 0
		for _, pod := range physicalPods.Items {
			if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
				count++
			}
		}
		return count
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.Equal(2),
		"Should create 2 physical pods from virtual pods")

	ginkgo.GinkgoWriter.Printf("Created virtual pods and verified physical pod creation\n")
}

// deleteSpecificPhysicalPod deletes a specific physical pod by name
func deleteSpecificPhysicalPod(ctx context.Context, podName string) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
		},
	}

	err := k8sPhysical.Delete(ctx, pod, &client.DeleteOptions{
		GracePeriodSeconds: ptr.To(int64(0)),
	})
	if err != nil && !apierrors.IsNotFound(err) {
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}

	ginkgo.GinkgoWriter.Printf("Deleted specific physical pod: %s\n", podName)

	// Wait for pod to be actually deleted
	gomega.Eventually(func() bool {
		err := k8sPhysical.Get(ctx, client.ObjectKey{Name: podName, Namespace: "default"}, pod)
		return apierrors.IsNotFound(err)
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.BeTrue(),
		fmt.Sprintf("Pod %s should be deleted", podName))
}

// verifyFakePodPersistence verifies that fake pod persists after virtual pod creation
func verifyFakePodPersistence(ctx context.Context, virtualNodeName string) {
	// Verify fake pod still exists and hasn't been recreated
	fakePods := &corev1.PodList{}
	err := k8sVirtual.List(ctx, fakePods,
		client.InNamespace(fakePodNamespace),
		client.MatchingLabels{
			cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
		})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Should still have exactly one fake pod for our virtual node
	nodeSpecificFakePods := 0
	for _, pod := range fakePods.Items {
		if pod.Spec.NodeName == virtualNodeName {
			nodeSpecificFakePods++
		}
	}

	gomega.Expect(nodeSpecificFakePods).To(gomega.Equal(1),
		"Should have exactly one fake pod for the virtual node")

	ginkgo.GinkgoWriter.Printf("Verified fake pod persistence: 1 fake pod remains\n")
}

// createNewPhysicalHostPortPods creates new physical pods with different configurations
func createNewPhysicalHostPortPods(ctx context.Context) {
	// New Pod 1: hostNetwork pod
	newPod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-hostport-pod1-hostnet",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName:    fmt.Sprintf("physical-node-%s", uniqueID),
			HostNetwork: true,
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "new1", ContainerPort: 1000, HostPort: 1000, Protocol: corev1.ProtocolTCP},
						{Name: "new2", ContainerPort: 1001, HostPort: 1001, Protocol: corev1.ProtocolUDP},
						{Name: "new3", ContainerPort: 1002, HostPort: 1002, Protocol: corev1.ProtocolTCP},
					},
				},
			},
		},
	}

	// New Pod 2: Regular pod
	newPod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-hostport-pod2",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: fmt.Sprintf("physical-node-%s", uniqueID),
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "nginx:latest",
					Ports: []corev1.ContainerPort{
						{Name: "api1", ContainerPort: 10080, HostPort: 10080, Protocol: corev1.ProtocolTCP, HostIP: hostIPAny},
						{Name: "api2", ContainerPort: 10081, HostPort: 10081, Protocol: corev1.ProtocolTCP, HostIP: hostIPLocalhost},
						{Name: "metrics", ContainerPort: 10082, HostPort: 10082, Protocol: corev1.ProtocolUDP, HostIP: hostIPAny},
					},
				},
			},
		},
	}

	gomega.Expect(k8sPhysical.Create(ctx, newPod1)).To(gomega.Succeed())
	gomega.Expect(k8sPhysical.Create(ctx, newPod2)).To(gomega.Succeed())

	ginkgo.GinkgoWriter.Printf("Created new physical pods: %s, %s\n", newPod1.Name, newPod2.Name)
}

// portCheck represents a port configuration to verify
type portCheck struct {
	hostPort int32
	protocol corev1.Protocol
	hostIP   string
	name     string
}

// checkPortInContainer checks if a specific port exists in the container ports
func checkPortInContainer(containerPorts []corev1.ContainerPort, pc portCheck) bool {
	for _, port := range containerPorts {
		if port.HostPort == pc.hostPort && port.Protocol == pc.protocol && port.HostIP == pc.hostIP {
			return true
		}
	}
	return false
}

// verifyExpectedPorts verifies that all expected ports exist in the container
func verifyExpectedPorts(containerPorts []corev1.ContainerPort, expectedPorts []portCheck) {
	for _, expected := range expectedPorts {
		found := checkPortInContainer(containerPorts, expected)
		gomega.Expect(found).To(gomega.BeTrue(), fmt.Sprintf("Should contain %s", expected.name))
	}
}

// verifyAbsentPorts verifies that specified ports are not present in the container
func verifyAbsentPorts(containerPorts []corev1.ContainerPort, absentPorts []portCheck) {
	for _, absent := range absentPorts {
		found := checkPortInContainer(containerPorts, absent)
		gomega.Expect(found).To(gomega.BeFalse(), fmt.Sprintf("Should not contain %s", absent.name))
	}
}

// waitForFakePodUpdate waits for the fake pod to be updated with new configuration
func waitForFakePodUpdate(ctx context.Context, virtualNodeName string) *corev1.Pod {
	var newFakePod *corev1.Pod

	gomega.Eventually(func() bool {
		fakePods := &corev1.PodList{}
		err := k8sVirtual.List(ctx, fakePods,
			client.InNamespace(fakePodNamespace),
			client.MatchingLabels{
				cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
			})
		if err != nil {
			return false
		}

		// Find the fake pod for our virtual node
		for i := range fakePods.Items {
			if fakePods.Items[i].Spec.NodeName == virtualNodeName {
				newFakePod = &fakePods.Items[i]
				break
			}
		}

		if newFakePod == nil || len(newFakePod.Spec.Containers) == 0 {
			return false
		}

		// Check if it has the expected ports after deletion and addition
		container := newFakePod.Spec.Containers[0]
		// Should have ports from remaining old pods + new pods
		// Original: 19 ports - 2 (deleted pod1) + 6 (new pods) = 23 total ports
		return len(container.Ports) == 23
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.BeTrue(),
		"Fake pod should be updated with new hostPort configuration")

	return newFakePod
}

// verifyFakePodUpdate verifies that fake pod is updated with new configuration
func verifyFakePodUpdate(ctx context.Context, virtualNodeName string) {
	// Wait for fake pod to be updated with new configuration
	newFakePod := waitForFakePodUpdate(ctx, virtualNodeName)

	// Verify the updated fake pod has correct configuration
	gomega.Expect(newFakePod).NotTo(gomega.BeNil())
	container := newFakePod.Spec.Containers[0]

	// Verify total port count
	gomega.Expect(container.Ports).To(gomega.HaveLen(23), "Should have ports from remaining old pods and new pods")
	ginkgo.GinkgoWriter.Printf("Updated fake pod has ports: %v\n", container.Ports)

	// Define expected new ports
	expectedNewPorts := []portCheck{
		{1000, corev1.ProtocolTCP, hostIPAny, "new port 1000/TCP from new-hostport-pod1-hostnet"},
		{1001, corev1.ProtocolUDP, hostIPAny, "new port 1001/UDP from new-hostport-pod1-hostnet"},
		{1002, corev1.ProtocolTCP, hostIPAny, "new port 1002/TCP from new-hostport-pod1-hostnet"},
		{10080, corev1.ProtocolTCP, hostIPAny, "new port 10080/TCP/0.0.0.0 from new-hostport-pod2"},
		{10081, corev1.ProtocolTCP, hostIPLocalhost, "new port 10081/TCP/127.0.0.1 from new-hostport-pod2"},
		{10082, corev1.ProtocolUDP, hostIPAny, "new port 10082/UDP/0.0.0.0 from new-hostport-pod2"},
	}

	// Define expected absent ports (deleted ports)
	absentPorts := []portCheck{
		{8080, corev1.ProtocolTCP, hostIPAny, "deleted port 8080"},
		{8081, corev1.ProtocolTCP, hostIPAny, "deleted port 8081"},
	}

	// Verify all new ports exist
	verifyExpectedPorts(container.Ports, expectedNewPorts)

	// Verify deleted ports are gone
	verifyAbsentPorts(container.Ports, absentPorts)

	ginkgo.GinkgoWriter.Printf("Verified fake pod update: now has %d ports\n", len(container.Ports))
}

// cleanupPhysicalPods removes all non-kubeocean managed physical pods
func cleanupPhysicalPods(ctx context.Context) {
	physicalPods := &corev1.PodList{}
	err := k8sPhysical.List(ctx, physicalPods, client.InNamespace("default"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	deletedCount := 0
	for _, pod := range physicalPods.Items {
		// Skip kubeocean managed pods
		if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
			continue
		}

		err := k8sPhysical.Delete(ctx, &pod, &client.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})
		if err != nil && !apierrors.IsNotFound(err) {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		deletedCount++
	}

	ginkgo.GinkgoWriter.Printf("Deleted %d non-kubeocean managed physical pods\n", deletedCount)

	// Wait for pods to be actually deleted
	gomega.Eventually(func() int {
		pods := &corev1.PodList{}
		err := k8sPhysical.List(ctx, pods, client.InNamespace("default"))
		if err != nil {
			return -1
		}

		nonKubeoceanPods := 0
		for _, pod := range pods.Items {
			if pod.Labels[cloudv1beta1.LabelManagedBy] != cloudv1beta1.LabelManagedByValue {
				nonKubeoceanPods++
			}
		}
		return nonKubeoceanPods
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.Equal(0),
		"All non-kubeocean managed physical pods should be deleted")
}

// verifyFakePodCleanup verifies that fake pod is cleaned up
func verifyFakePodCleanup(ctx context.Context, virtualNodeName string) {
	// Wait for fake pod to be deleted
	gomega.Eventually(func() int {
		fakePods := &corev1.PodList{}
		err := k8sVirtual.List(ctx, fakePods,
			client.InNamespace(fakePodNamespace),
			client.MatchingLabels{
				cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
			})
		if err != nil {
			return -1
		}

		nodeSpecificFakePods := 0
		for _, pod := range fakePods.Items {
			if pod.Spec.NodeName == virtualNodeName {
				nodeSpecificFakePods++
			}
		}
		return nodeSpecificFakePods
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.Equal(0),
		"All fake pods should be cleaned up when no hostPorts remain")

	ginkgo.GinkgoWriter.Printf("Verified fake pod cleanup: all fake pods removed\n")
}

// deleteResourceLeasingPolicy deletes the ResourceLeasingPolicy
func deleteResourceLeasingPolicy(ctx context.Context) {
	policyName := "hostport-policy-" + uniqueID
	policy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: policyName,
		},
	}

	err := k8sPhysical.Delete(ctx, policy)
	if err != nil && !apierrors.IsNotFound(err) {
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}

	ginkgo.GinkgoWriter.Printf("Deleted ResourceLeasingPolicy: %s\n", policyName)

	// Wait for policy to be actually deleted
	gomega.Eventually(func() bool {
		err := k8sPhysical.Get(ctx, client.ObjectKey{Name: policyName}, policy)
		return apierrors.IsNotFound(err)
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.BeTrue(),
		fmt.Sprintf("ResourceLeasingPolicy %s should be deleted", policyName))
}

// verifyVirtualNodeDeleted verifies that the virtual node is deleted
func verifyVirtualNodeDeleted(ctx context.Context, virtualNodeName string) {
	gomega.Eventually(func() bool {
		node := &corev1.Node{}
		err := k8sVirtual.Get(ctx, client.ObjectKey{Name: virtualNodeName}, node)
		return apierrors.IsNotFound(err)
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.BeTrue(),
		fmt.Sprintf("Virtual node %s should be deleted", virtualNodeName))

	ginkgo.GinkgoWriter.Printf("Verified virtual node deleted: %s\n", virtualNodeName)
}

// recreateResourceLeasingPolicy recreates the ResourceLeasingPolicy
func recreateResourceLeasingPolicy(ctx context.Context, clusterBindingName string) {
	policyName := "hostport-policy2-" + uniqueID
	policy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: policyName,
		},
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
	ginkgo.GinkgoWriter.Printf("Recreated ResourceLeasingPolicy: %s\n", policyName)
}

// deletePhysicalNode deletes the physical node
func deletePhysicalNode(ctx context.Context, physicalNodeName string) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: physicalNodeName,
		},
	}

	err := k8sPhysical.Delete(ctx, node)
	if err != nil && !apierrors.IsNotFound(err) {
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}

	ginkgo.GinkgoWriter.Printf("Deleted physical node: %s\n", physicalNodeName)

	// Wait for node to be actually deleted
	gomega.Eventually(func() bool {
		err := k8sPhysical.Get(ctx, client.ObjectKey{Name: physicalNodeName}, node)
		return apierrors.IsNotFound(err)
	}, hostPortTestTimeout, hostPortTestPollingInterval).Should(gomega.BeTrue(),
		fmt.Sprintf("Physical node %s should be deleted", physicalNodeName))
}

// cleanupHostPortTestResources cleans up test resources
func cleanupHostPortTestResources(ctx context.Context, clusterBindingName, physicalNodeName string) {
	// Clean up virtual cluster resources
	_ = k8sVirtual.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fakePodNamespace}})
	_ = k8sVirtual.Delete(ctx, &cloudv1beta1.ClusterBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterBindingName}})
	_ = k8sVirtual.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: clusterBindingName + "-kc", Namespace: "kubeocean-system"},
	})

	// Clean up physical cluster resources
	pods := &corev1.PodList{}
	_ = k8sPhysical.List(ctx, pods, client.InNamespace("default"))
	for _, pod := range pods.Items {
		_ = k8sPhysical.Delete(ctx, &pod, &client.DeleteOptions{GracePeriodSeconds: ptr.To[int64](0)})
	}

	policies := &cloudv1beta1.ResourceLeasingPolicyList{}
	_ = k8sPhysical.List(ctx, policies)
	for _, policy := range policies.Items {
		if policy.Name == "hostport-policy-"+uniqueID {
			_ = k8sPhysical.Delete(ctx, &policy)
		}
	}

	_ = k8sPhysical.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: physicalNodeName}})
}
