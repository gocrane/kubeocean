package integration

import (
	"context"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = ginkgo.Describe("Syncer Integration Tests", func() {
	ginkgo.Describe("KubeoceanSyncer Initialization", func() {
		ginkgo.It("should create KubeoceanSyncer instance successfully", func(ctx context.Context) {
			// Create namespace for secrets
			ns := testSystemNamespace
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

			// Create KubeoceanSyncer instance
			syncer, err := syncerpkg.NewKubeoceanSyncer(testMgr, k8sVirtual, scheme, clusterBinding.Name, 100, 150)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(syncer).NotTo(gomega.BeNil())

			// Verify syncer properties
			gomega.Expect(syncer.GetClusterBinding()).To(gomega.BeNil()) // Not loaded yet

			ginkgo.By("KubeoceanSyncer instance created successfully")
		}, ginkgo.SpecTimeout(30*time.Second))

		ginkgo.It("should load ClusterBinding successfully", func(ctx context.Context) {
			// Create namespace for secrets
			ns := testSystemNamespace
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

			// Create KubeoceanSyncer and start it briefly to load ClusterBinding
			syncer, err := syncerpkg.NewKubeoceanSyncer(testMgr, k8sVirtual, scheme, clusterBinding.Name, 100, 150)
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

			// Try to create KubeoceanSyncer with non-existent ClusterBinding
			syncer, err := syncerpkg.NewKubeoceanSyncer(testMgr, k8sVirtual, scheme, "non-existent-binding", 100, 150)
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
			ns := testSystemNamespace
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

			// Create KubeoceanSyncer
			syncer, err := syncerpkg.NewKubeoceanSyncer(testMgr, k8sVirtual, scheme, clusterBinding.Name, 100, 150)
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

	ginkgo.Describe("ClusterBinding Deletion E2E Tests", func() {
		ginkgo.It("should handle complete ClusterBinding deletion lifecycle", func(ctx context.Context) {
			// Test parameters - keep names short due to clusterID length limit (max 24 chars)
			clusterBindingName := fmt.Sprintf("del-%s", uniqueID[:8])
			physicalNodeName := fmt.Sprintf("node-%s", uniqueID[:8])
			virtualNodeName := fmt.Sprintf("vnode-%s-%s", clusterBindingName, physicalNodeName)
			policyName := fmt.Sprintf("policy-%s", uniqueID[:8])

			// Step 1: Prepare environment, create ClusterBinding and ResourceLeasingPolicy
			ginkgo.By("Step 1: Setting up test environment with ClusterBinding and ResourceLeasingPolicy")
			setupClusterBindingDeletionEnvironment(ctx, clusterBindingName, physicalNodeName, policyName)

			// Create and start syncer
			createAndStartSyncer(ctx, clusterBindingName)

			// Wait for virtual node to be ready and verify finalizer
			waitForVirtualNodeReady(ctx, virtualNodeName)
			verifyClusterBindingFinalizer(ctx, clusterBindingName)

			// Step 2: Create virtual resources (2 pods with same resources + 1 pod with ServiceAccount)
			ginkgo.By("Step 2: Creating virtual resources - ConfigMap, Secret, PV, PVC and 3 Pods")
			configMapName, secretName, pvName, pvcName, serviceAccountName := createVirtualResourcesForDeletion(ctx, virtualNodeName)

			// Step 3: Verify physical resources are created but not scheduled
			ginkgo.By("Step 3: Verifying physical resources are created but not scheduled")
			verifyPhysicalResourcesCreated(ctx, configMapName, secretName, pvName, pvcName)
			physicalPodNames := verifyPhysicalPodsCreatedButNotScheduled(ctx)

			// Update physical pod bindings to schedule them
			ginkgo.By("Step 3: Scheduling physical pods to physical nodes")
			schedulePhysicalPodsToNode(ctx, physicalPodNames, physicalNodeName)

			// Step 4: Confirm physical resources exist
			ginkgo.By("Step 4: Confirming all physical resources exist and are properly referenced")
			verifyPhysicalResourcesExistAndReferenced(ctx, physicalPodNames)

			// Step 4.5: Create physical hostPort pods and verify fake pod creation
			ginkgo.By("Step 4.5: Creating physical hostPort pods and verifying fake pod creation")
			createPhysicalHostPortPods(ctx, physicalNodeName)
			verifyFakePodCreation(ctx, virtualNodeName)

			// Step 5: Delete ClusterBinding and ResourceLeasingPolicy
			ginkgo.By("Step 5: Deleting ClusterBinding")
			deleteClusterBinding(ctx, clusterBindingName)

			// Step 6: Verify physical pods have deletion timestamp and force delete them
			ginkgo.By("Step 6: Verifying physical pods have deletion timestamp and force deleting them")
			verifyPhysicalPodsDeletionTimestamp(ctx, physicalPodNames)
			forceDeletePhysicalPods(ctx, physicalPodNames)

			// Step 7: Confirm physical and virtual pods are deleted, virtual node is deleted
			ginkgo.By("Step 7: Confirming pods and virtual node are deleted")
			verifyPodsAndVirtualNodeDeleted(ctx, virtualNodeName)

			// Step 8: Confirm physical resources are cleaned up and ClusterBinding is deleted
			ginkgo.By("Step 8: Confirming physical resources cleanup and ClusterBinding deletion")
			verifyPhysicalResourcesCleanup(ctx, configMapName, secretName, pvName, pvcName, serviceAccountName)
			verifyClusterBindingDeleted(ctx, clusterBindingName)

			// Step 9: Confirm virtual resources don't have kubeocean-specific labels/annotations/finalizers
			ginkgo.By("Step 9: Confirming virtual resources are clean of kubeocean metadata")
			verifyVirtualResourcesCleanedUp(ctx, configMapName, secretName, pvName, pvcName, serviceAccountName)

			ginkgo.By("ClusterBinding deletion E2E test completed successfully")
		}, ginkgo.SpecTimeout(300*time.Second))

		ginkgo.It("should handle ClusterBinding deletion with different resources per Pod", func(ctx context.Context) {
			// Test parameters - keep names short due to clusterID length limit (max 24 chars)
			clusterBindingName := fmt.Sprintf("del2-%s", uniqueID[:7])
			physicalNodeName := fmt.Sprintf("node-%s", uniqueID[:8])
			virtualNodeName := fmt.Sprintf("vnode-%s-%s", clusterBindingName, physicalNodeName)
			policyName := fmt.Sprintf("pol2-%s", uniqueID[:7])

			// Step 1: Prepare environment, create ClusterBinding and ResourceLeasingPolicy
			ginkgo.By("Step 1: Setting up test environment with ClusterBinding and ResourceLeasingPolicy")
			setupClusterBindingDeletionEnvironment(ctx, clusterBindingName, physicalNodeName, policyName)

			// Create and start syncer
			createAndStartSyncer(ctx, clusterBindingName)

			// Wait for virtual node to be ready and verify finalizer
			waitForVirtualNodeReady(ctx, virtualNodeName)
			verifyClusterBindingFinalizer(ctx, clusterBindingName)

			// Step 2: Create virtual resources (2 pods with different resources + 1 pod with ServiceAccount)
			ginkgo.By("Step 2: Creating virtual resources - Different ConfigMaps, Secrets, PVs, PVCs for each Pod")
			allResourceNames := createVirtualResourcesForDeletionWithDifferentResources(ctx, virtualNodeName)

			// Step 3: Verify physical resources are created but not scheduled
			ginkgo.By("Step 3: Verifying physical resources are created but not scheduled")
			verifyPhysicalResourcesCreatedMultiple(ctx, allResourceNames)
			physicalPodNames := verifyPhysicalPodsCreatedButNotScheduled(ctx)

			// Update physical pod bindings to schedule them
			ginkgo.By("Step 3: Scheduling physical pods to physical nodes")
			schedulePhysicalPodsToNode(ctx, physicalPodNames, physicalNodeName)

			// Step 4: Confirm physical resources exist
			ginkgo.By("Step 4: Confirming all physical resources exist and are properly referenced")
			verifyPhysicalResourcesExistAndReferenced(ctx, physicalPodNames)

			// Step 4.5: Create physical hostPort pods and verify fake pod creation
			ginkgo.By("Step 4.5: Creating physical hostPort pods and verifying fake pod creation")
			createPhysicalHostPortPods(ctx, physicalNodeName)
			verifyFakePodCreation(ctx, virtualNodeName)

			// Step 5: Delete ClusterBinding and ResourceLeasingPolicy
			ginkgo.By("Step 5: Deleting ClusterBinding")
			deleteClusterBinding(ctx, clusterBindingName)

			// Step 6: Verify physical pods have deletion timestamp and force delete them
			ginkgo.By("Step 6: Verifying physical pods have deletion timestamp and force deleting them")
			verifyPhysicalPodsDeletionTimestamp(ctx, physicalPodNames)
			forceDeletePhysicalPods(ctx, physicalPodNames)

			// Step 7: Confirm physical and virtual pods are deleted, virtual node is deleted
			ginkgo.By("Step 7: Confirming pods and virtual node are deleted")
			verifyPodsAndVirtualNodeDeleted(ctx, virtualNodeName)

			// Step 8: Confirm physical resources are cleaned up and ClusterBinding is deleted
			ginkgo.By("Step 8: Confirming physical resources cleanup and ClusterBinding deletion")
			verifyPhysicalResourcesCleanupMultiple(ctx, allResourceNames)
			verifyClusterBindingDeleted(ctx, clusterBindingName)

			// Step 9: Confirm virtual resources don't have kubeocean-specific labels/annotations/finalizers
			ginkgo.By("Step 9: Confirming virtual resources are clean of kubeocean metadata")
			verifyVirtualResourcesCleanedUpMultiple(ctx, allResourceNames)

			ginkgo.By("ClusterBinding deletion with different resources E2E test completed successfully")
		}, ginkgo.SpecTimeout(300*time.Second))

		ginkgo.It("should handle ClusterBinding deletion with orphaned physical resources", func(ctx context.Context) {
			// Test parameters - keep names short due to clusterID length limit (max 24 chars)
			clusterBindingName := fmt.Sprintf("del3-%s", uniqueID[:7])
			physicalNodeName := fmt.Sprintf("node-%s", uniqueID[:8])
			virtualNodeName := fmt.Sprintf("vnode-%s-%s", clusterBindingName, physicalNodeName)
			policyName := fmt.Sprintf("pol3-%s", uniqueID[:7])

			// Step 1: Prepare environment, create ClusterBinding and ResourceLeasingPolicy
			ginkgo.By("Step 1: Setting up test environment with ClusterBinding and ResourceLeasingPolicy")
			setupClusterBindingDeletionEnvironment(ctx, clusterBindingName, physicalNodeName, policyName)

			// Create and start syncer
			createAndStartSyncer(ctx, clusterBindingName)

			// Wait for virtual node to be ready and verify finalizer
			waitForVirtualNodeReady(ctx, virtualNodeName)
			verifyClusterBindingFinalizer(ctx, clusterBindingName)

			// Step 2: Create virtual resources (1 pod with resources)
			ginkgo.By("Step 2: Creating virtual resources - 1 Pod with ConfigMap, Secret, PV, PVC")
			configMapName, secretName, pvName, pvcName := createVirtualResourcesForOrphanedTest(ctx, virtualNodeName)

			// Step 3: Verify physical resources are created but not scheduled
			ginkgo.By("Step 3: Verifying physical resources are created but not scheduled")
			verifyPhysicalResourcesCreated(ctx, configMapName, secretName, pvName, pvcName)
			physicalPodNames := verifySinglePhysicalPodCreatedButNotScheduled(ctx)

			// Update physical pod bindings to schedule them
			ginkgo.By("Step 3: Scheduling physical pods to physical nodes")
			schedulePhysicalPodsToNode(ctx, physicalPodNames, physicalNodeName)

			// Step 4: Create orphaned physical resources (no corresponding virtual resources)
			ginkgo.By("Step 4: Creating orphaned physical resources without corresponding virtual resources")
			orphanedResourceNames := createOrphanedPhysicalResources(ctx, clusterBindingName)

			// Step 5: Delete ClusterBinding and ResourceLeasingPolicy
			ginkgo.By("Step 5: Deleting ClusterBinding and ResourceLeasingPolicy")
			deleteClusterBinding(ctx, clusterBindingName)

			// Step 6: Verify physical pods have deletion timestamp and force delete them
			ginkgo.By("Step 6: Verifying physical pods have deletion timestamp and force deleting them")
			verifyPhysicalPodsDeletionTimestamp(ctx, physicalPodNames)
			forceDeletePhysicalPods(ctx, physicalPodNames)

			// Step 7: Confirm physical and virtual pods are deleted, virtual node is deleted
			ginkgo.By("Step 7: Confirming pods and virtual node are deleted")
			verifyPodsAndVirtualNodeDeleted(ctx, virtualNodeName)

			// Step 8: Wait for ClusterBinding deletion flow to process virtual resources
			ginkgo.By("Step 8: Waiting for ClusterBinding deletion flow to process virtual resources")
			verifyVirtualResourcesCleanedUp(ctx, configMapName, secretName, pvName, pvcName, "")
			verifyPhysicalResourcesCleanup(ctx, configMapName, secretName, pvName, pvcName, "")
			verifyClusterBindingInDeletingState(ctx, clusterBindingName)

			// Step 9: Delete orphaned physical resources and confirm ClusterBinding deletion
			ginkgo.By("Step 9: Deleting orphaned physical resources and confirming ClusterBinding deletion " + fmt.Sprintf("%v", orphanedResourceNames))
			deleteOrphanedPhysicalResources(ctx, orphanedResourceNames)
			verifyClusterBindingDeleted(ctx, clusterBindingName)

			ginkgo.By("ClusterBinding deletion with orphaned physical resources E2E test completed successfully")
		}, ginkgo.SpecTimeout(300*time.Second))
	})

})

// ResourceNames holds all resource names for the test
type ResourceNames struct {
	ConfigMapNames     []string
	SecretNames        []string
	PVNames            []string
	PVCNames           []string
	ServiceAccountName string
}

// Helper functions for ClusterBinding deletion test

func setupClusterBindingDeletionEnvironment(ctx context.Context, clusterBindingName, physicalNodeName, policyName string) {
	ginkgo.By("Setting up ClusterBinding deletion test environment")

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
		ObjectMeta: metav1.ObjectMeta{Name: policyName},
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

func verifyClusterBindingFinalizer(ctx context.Context, clusterBindingName string) {
	ginkgo.By("Verifying ClusterBinding has ClusterBindingSyncerFinalizer")

	gomega.Eventually(func() bool {
		clusterBinding := &cloudv1beta1.ClusterBinding{}
		err := k8sVirtual.Get(ctx, types.NamespacedName{Name: clusterBindingName}, clusterBinding)
		if err != nil {
			return false
		}

		// Check if finalizer exists
		for _, finalizer := range clusterBinding.Finalizers {
			if finalizer == cloudv1beta1.ClusterBindingSyncerFinalizer {
				return true
			}
		}
		return false
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(),
		"ClusterBinding should have ClusterBindingSyncerFinalizer")
}

func createVirtualResourcesForDeletion(ctx context.Context, virtualNodeName string) (configMapName, secretName, pvName, pvcName, serviceAccountName string) {
	// Generate unique names
	configMapName = fmt.Sprintf("test-config-%s", uniqueID)
	secretName = fmt.Sprintf("test-secret-%s", uniqueID)
	pvName = fmt.Sprintf("test-pv-%s", uniqueID)
	pvcName = fmt.Sprintf("test-pvc-%s", uniqueID)
	serviceAccountName = fmt.Sprintf("test-sa-%s", uniqueID)

	// Create ConfigMap
	configMap := createTestVirtualConfigMap(configMapName)
	gomega.Expect(k8sVirtual.Create(ctx, configMap)).To(gomega.Succeed())

	// Create additional ConfigMap for init container
	initConfigMap := createTestVirtualConfigMap("test-config-init")
	gomega.Expect(k8sVirtual.Create(ctx, initConfigMap)).To(gomega.Succeed())

	// Create Secret
	secret := createTestVirtualSecret(secretName)
	gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

	// Create additional Secret for init container
	initSecret := createTestVirtualSecret("test-secret-init")
	gomega.Expect(k8sVirtual.Create(ctx, initSecret)).To(gomega.Succeed())

	// Create PV
	pv := createTestVirtualPV(pvName, pvcName)
	gomega.Expect(k8sVirtual.Create(ctx, pv)).To(gomega.Succeed())

	// Create PVC
	pvc := createTestVirtualPVC(pvcName, pvName)
	gomega.Expect(k8sVirtual.Create(ctx, pvc)).To(gomega.Succeed())

	// Wait for PVC to be bound to PV (manually set status in envtest)
	gomega.Eventually(func() bool {
		var virtualPVC corev1.PersistentVolumeClaim
		err := k8sVirtual.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: testPodNamespace}, &virtualPVC)
		if err != nil {
			return false
		}
		// Manually set PVC to Bound status for testing
		virtualPVC.Status.Phase = corev1.ClaimBound
		err = k8sVirtual.Status().Update(ctx, &virtualPVC)
		return err == nil
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Virtual PVC should be bound to PV")

	// Create ServiceAccount
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: testPodNamespace,
		},
	}
	gomega.Expect(k8sVirtual.Create(ctx, serviceAccount)).To(gomega.Succeed())

	// Create 2 pods with same resources
	pod1 := createTestVirtualPodWithResources(
		fmt.Sprintf("test-pod-1-%s", uniqueID),
		virtualNodeName,
		configMapName,
		secretName,
		pvcName,
	)
	gomega.Expect(k8sVirtual.Create(ctx, pod1)).To(gomega.Succeed())

	pod2 := createTestVirtualPodWithResources(
		fmt.Sprintf("test-pod-2-%s", uniqueID),
		virtualNodeName,
		configMapName,
		secretName,
		pvcName,
	)
	gomega.Expect(k8sVirtual.Create(ctx, pod2)).To(gomega.Succeed())

	// Create 1 pod with ServiceAccount
	pod3 := createTestVirtualPodWithServiceAccount(
		fmt.Sprintf("test-pod-sa-%s", uniqueID),
		testPodNamespace,
		virtualNodeName,
		serviceAccountName,
	)
	gomega.Expect(k8sVirtual.Create(ctx, pod3)).To(gomega.Succeed())

	return configMapName, secretName, pvName, pvcName, serviceAccountName
}

func createVirtualResourcesForDeletionWithDifferentResources(ctx context.Context, virtualNodeName string) *ResourceNames {
	// Generate unique names for each pod's resources
	configMapNames := []string{
		fmt.Sprintf("test-config-1-%s", uniqueID[:8]),
		fmt.Sprintf("test-config-2-%s", uniqueID[:8]),
	}
	secretNames := []string{
		fmt.Sprintf("test-secret-1-%s", uniqueID[:8]),
		fmt.Sprintf("test-secret-2-%s", uniqueID[:8]),
	}
	pvNames := []string{
		fmt.Sprintf("test-pv-1-%s", uniqueID[:8]),
		fmt.Sprintf("test-pv-2-%s", uniqueID[:8]),
	}
	pvcNames := []string{
		fmt.Sprintf("test-pvc-1-%s", uniqueID[:8]),
		fmt.Sprintf("test-pvc-2-%s", uniqueID[:8]),
	}
	serviceAccountName := fmt.Sprintf("test-sa-%s", uniqueID[:8])

	// Create ConfigMaps
	for _, configMapName := range configMapNames {
		configMap := createTestVirtualConfigMap(configMapName)
		gomega.Expect(k8sVirtual.Create(ctx, configMap)).To(gomega.Succeed())
	}

	// Create additional ConfigMap for init container
	initConfigMap := createTestVirtualConfigMap("test-config-init")
	gomega.Expect(k8sVirtual.Create(ctx, initConfigMap)).To(gomega.Succeed())

	// Create Secrets
	for _, secretName := range secretNames {
		secret := createTestVirtualSecret(secretName)
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())
	}

	// Create additional Secret for init container
	initSecret := createTestVirtualSecret("test-secret-init")
	gomega.Expect(k8sVirtual.Create(ctx, initSecret)).To(gomega.Succeed())

	// Create PVs and PVCs
	for i := 0; i < 2; i++ {
		// Create PV
		pv := createTestVirtualPV(pvNames[i], pvcNames[i])
		gomega.Expect(k8sVirtual.Create(ctx, pv)).To(gomega.Succeed())

		// Create PVC
		pvc := createTestVirtualPVC(pvcNames[i], pvNames[i])
		gomega.Expect(k8sVirtual.Create(ctx, pvc)).To(gomega.Succeed())

		// Wait for PVC to be bound to PV (manually set status in envtest)
		gomega.Eventually(func() bool {
			var virtualPVC corev1.PersistentVolumeClaim
			err := k8sVirtual.Get(ctx, types.NamespacedName{Name: pvcNames[i], Namespace: testPodNamespace}, &virtualPVC)
			if err != nil {
				return false
			}
			// Manually set PVC to Bound status for testing
			virtualPVC.Status.Phase = corev1.ClaimBound
			err = k8sVirtual.Status().Update(ctx, &virtualPVC)
			return err == nil
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Virtual PVC %s should be bound to PV", pvcNames[i]))
	}

	// Create ServiceAccount
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: testPodNamespace,
		},
	}
	gomega.Expect(k8sVirtual.Create(ctx, serviceAccount)).To(gomega.Succeed())

	// Create Pod 1 with first set of resources
	pod1 := createTestVirtualPodWithResources(
		fmt.Sprintf("test-pod-1-%s", uniqueID[:8]),
		virtualNodeName,
		configMapNames[0],
		secretNames[0],
		pvcNames[0],
	)
	gomega.Expect(k8sVirtual.Create(ctx, pod1)).To(gomega.Succeed())

	// Create Pod 2 with second set of resources
	pod2 := createTestVirtualPodWithResources(
		fmt.Sprintf("test-pod-2-%s", uniqueID[:8]),
		virtualNodeName,
		configMapNames[1],
		secretNames[1],
		pvcNames[1],
	)
	gomega.Expect(k8sVirtual.Create(ctx, pod2)).To(gomega.Succeed())

	// Create Pod 3 with ServiceAccount
	pod3 := createTestVirtualPodWithServiceAccount(
		fmt.Sprintf("test-pod-sa-%s", uniqueID[:8]),
		testPodNamespace,
		virtualNodeName,
		serviceAccountName,
	)
	gomega.Expect(k8sVirtual.Create(ctx, pod3)).To(gomega.Succeed())

	return &ResourceNames{
		ConfigMapNames:     configMapNames,
		SecretNames:        secretNames,
		PVNames:            pvNames,
		PVCNames:           pvcNames,
		ServiceAccountName: serviceAccountName,
	}
}

func createVirtualResourcesForOrphanedTest(ctx context.Context, virtualNodeName string) (configMapName, secretName, pvName, pvcName string) {
	// Generate unique names
	configMapName = fmt.Sprintf("test-config-%s", uniqueID[:8])
	secretName = fmt.Sprintf("test-secret-%s", uniqueID[:8])
	pvName = fmt.Sprintf("test-pv-%s", uniqueID[:8])
	pvcName = fmt.Sprintf("test-pvc-%s", uniqueID[:8])

	// Create ConfigMap
	configMap := createTestVirtualConfigMap(configMapName)
	gomega.Expect(k8sVirtual.Create(ctx, configMap)).To(gomega.Succeed())

	// Create additional ConfigMap for init container
	initConfigMap := createTestVirtualConfigMap("test-config-init")
	gomega.Expect(k8sVirtual.Create(ctx, initConfigMap)).To(gomega.Succeed())

	// Create Secret
	secret := createTestVirtualSecret(secretName)
	gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

	// Create additional Secret for init container
	initSecret := createTestVirtualSecret("test-secret-init")
	gomega.Expect(k8sVirtual.Create(ctx, initSecret)).To(gomega.Succeed())

	// Create PV
	pv := createTestVirtualPV(pvName, pvcName)
	gomega.Expect(k8sVirtual.Create(ctx, pv)).To(gomega.Succeed())

	// Create PVC
	pvc := createTestVirtualPVC(pvcName, pvName)
	gomega.Expect(k8sVirtual.Create(ctx, pvc)).To(gomega.Succeed())

	// Wait for PVC to be bound to PV (manually set status in envtest)
	gomega.Eventually(func() bool {
		var virtualPVC corev1.PersistentVolumeClaim
		err := k8sVirtual.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: testPodNamespace}, &virtualPVC)
		if err != nil {
			return false
		}
		// Manually set PVC to Bound status for testing
		virtualPVC.Status.Phase = corev1.ClaimBound
		err = k8sVirtual.Status().Update(ctx, &virtualPVC)
		return err == nil
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Virtual PVC should be bound to PV")

	// Create 1 pod with resources
	pod := createTestVirtualPodWithResources(
		fmt.Sprintf("test-pod-%s", uniqueID[:8]),
		virtualNodeName,
		configMapName,
		secretName,
		pvcName,
	)
	gomega.Expect(k8sVirtual.Create(ctx, pod)).To(gomega.Succeed())

	return configMapName, secretName, pvName, pvcName
}

func verifyPhysicalResourcesCreated(ctx context.Context, configMapName, secretName, pvName, pvcName string) {
	ginkgo.By("Verifying physical resources are created")

	// Verify ConfigMap
	gomega.Eventually(func() bool {
		var physicalConfigMap corev1.ConfigMap
		physicalName := generatePhysicalName(configMapName, testPodNamespace)
		err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalConfigMap)
		return err == nil
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Physical ConfigMap should be created")

	// Verify Secret
	gomega.Eventually(func() bool {
		var physicalSecret corev1.Secret
		physicalName := generatePhysicalName(secretName, testPodNamespace)
		err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalSecret)
		return err == nil
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Physical Secret should be created")

	// Verify PV (physical PV name is generated with hash)
	gomega.Eventually(func() bool {
		var physicalPV corev1.PersistentVolume
		physicalName := generatePhysicalName(pvName, "") // PV is cluster-scoped, so empty namespace
		err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName}, &physicalPV)
		return err == nil
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Physical PV should be created")

	// Verify PVC
	gomega.Eventually(func() bool {
		var physicalPVC corev1.PersistentVolumeClaim
		physicalName := generatePhysicalName(pvcName, testPodNamespace)
		err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalPVC)
		return err == nil
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Physical PVC should be created")
}

func verifyPhysicalPodsCreatedButNotScheduled(ctx context.Context) []string {
	ginkgo.By("Verifying physical pods are created but not scheduled")

	var physicalPodNames []string

	gomega.Eventually(func() int {
		pods := &corev1.PodList{}
		err := k8sPhysical.List(ctx, pods, client.InNamespace(testMountNamespace))
		if err != nil {
			return 0
		}

		physicalPodNames = []string{}
		for _, pod := range pods.Items {
			if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
				physicalPodNames = append(physicalPodNames, pod.Name)
				// Verify pod is not scheduled (no spec.nodeName)
				gomega.Expect(pod.Spec.NodeName).To(gomega.BeEmpty(), "Physical pod should not be scheduled initially")
			}
		}
		return len(physicalPodNames)
	}, testTimeout, testPollingInterval).Should(gomega.Equal(3), "Should have 3 physical pods created")

	return physicalPodNames
}

func schedulePhysicalPodsToNode(ctx context.Context, physicalPodNames []string, physicalNodeName string) {
	ginkgo.By("Updating physical pod bindings to schedule them")

	for _, podName := range physicalPodNames {
		// Create Binding object to schedule pod
		binding := &corev1.Binding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: testMountNamespace,
			},
			Target: corev1.ObjectReference{
				Kind: "Node",
				Name: physicalNodeName,
			},
		}

		// Use the physical client's REST client to create binding
		err := k8sPhysical.Create(ctx, binding)
		gomega.Expect(err).To(gomega.Succeed(), fmt.Sprintf("Should be able to bind pod %s to node %s", podName, physicalNodeName))
	}

	// Verify pods are scheduled
	gomega.Eventually(func() bool {
		allScheduled := true
		for _, podName := range physicalPodNames {
			var pod corev1.Pod
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: podName, Namespace: testMountNamespace}, &pod)
			if err != nil || pod.Spec.NodeName != physicalNodeName {
				allScheduled = false
				break
			}
		}
		return allScheduled
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "All physical pods should be scheduled")
}

func verifyPhysicalResourcesExistAndReferenced(ctx context.Context, physicalPodNames []string) {
	ginkgo.By("Verifying physical resources exist and are properly referenced by pods")

	// Get all physical pods and verify they reference the resources
	for _, podName := range physicalPodNames {
		var pod corev1.Pod
		err := k8sPhysical.Get(ctx, types.NamespacedName{Name: podName, Namespace: testMountNamespace}, &pod)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Check if pod references the resources (this varies by pod type)
		ginkgo.By(fmt.Sprintf("Pod %s should reference physical resources", podName))
	}
}

func deleteClusterBinding(ctx context.Context, clusterBindingName string) {
	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterBindingName},
	}
	err := k8sVirtual.Delete(ctx, clusterBinding)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func verifyPhysicalPodsDeletionTimestamp(ctx context.Context, physicalPodNames []string) {
	ginkgo.By("Verifying physical pods have deletion timestamp")

	gomega.Eventually(func() bool {
		allHaveDeletionTimestamp := true
		for _, podName := range physicalPodNames {
			var pod corev1.Pod
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: podName, Namespace: testMountNamespace}, &pod)
			if err != nil || pod.DeletionTimestamp == nil {
				allHaveDeletionTimestamp = false
				break
			}
		}
		return allHaveDeletionTimestamp
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "All physical pods should have deletion timestamp")
}

func forceDeletePhysicalPods(ctx context.Context, physicalPodNames []string) {
	ginkgo.By("Force deleting physical pods")

	for _, podName := range physicalPodNames {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: testMountNamespace,
			},
		}

		// Force delete with grace period 0
		err := k8sPhysical.Delete(ctx, pod, &client.DeleteOptions{
			GracePeriodSeconds: &[]int64{0}[0],
		})
		if err != nil && !apierrors.IsNotFound(err) {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	}
}

func verifyPodsAndVirtualNodeDeleted(ctx context.Context, virtualNodeName string) {
	ginkgo.By("Verifying physical pods are deleted")
	gomega.Eventually(func() int {
		pods := &corev1.PodList{}
		err := k8sPhysical.List(ctx, pods, client.InNamespace(testMountNamespace))
		if err != nil {
			return -1
		}

		count := 0
		for _, pod := range pods.Items {
			if pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
				count++
			}
		}
		return count
	}, testTimeout, testPollingInterval).Should(gomega.Equal(0), "All physical pods should be deleted")

	ginkgo.By("Verifying virtual pods are deleted")
	gomega.Eventually(func() int {
		pods := &corev1.PodList{}
		err := k8sVirtual.List(ctx, pods, client.InNamespace(testPodNamespace))
		if err != nil {
			return -1
		}
		return len(pods.Items)
	}, testTimeout, testPollingInterval).Should(gomega.Equal(0), "All virtual pods should be deleted")

	ginkgo.By("Verifying virtual node is deleted")
	gomega.Eventually(func() bool {
		var virtualNode corev1.Node
		err := k8sVirtual.Get(ctx, types.NamespacedName{Name: virtualNodeName}, &virtualNode)
		return apierrors.IsNotFound(err)
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Virtual node should be deleted")
}

func verifyPhysicalResourcesCleanup(ctx context.Context, configMapName, secretName, pvName, pvcName, serviceAccountName string) {
	ginkgo.By("Verifying physical resources are cleaned up")

	// Verify ConfigMap is deleted
	gomega.Eventually(func() bool {
		var physicalConfigMap corev1.ConfigMap
		physicalName := generatePhysicalName(configMapName, testPodNamespace)
		err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalConfigMap)
		return apierrors.IsNotFound(err)
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Physical ConfigMap should be deleted")

	// Verify Secret is deleted
	gomega.Eventually(func() bool {
		var physicalSecret corev1.Secret
		physicalName := generatePhysicalName(secretName, testPodNamespace)
		err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalSecret)
		return apierrors.IsNotFound(err)
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "Physical Secret should be deleted")

	// Verify PV is deleted (PV may have finalizers like kubernetes.io/pv-protection)
	physicalPVName := generatePhysicalName(pvName, "")
	verifyResourceDeletionWithFinalizers(ctx, "Physical PV",
		types.NamespacedName{Name: physicalPVName},
		&corev1.PersistentVolume{},
		[]string{"kubernetes.io/pv-protection"})

	// Verify PVC is deleted (PVC has finalizer kubernetes.io/pvc-protection)
	physicalPVCName := generatePhysicalName(pvcName, testPodNamespace)
	verifyResourceDeletionWithFinalizers(ctx, "Physical PVC",
		types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace},
		&corev1.PersistentVolumeClaim{},
		[]string{"kubernetes.io/pvc-protection"})

	if serviceAccountName != "" {
		// Verify ServiceAccount token secrets are deleted
		ginkgo.By("Verifying ServiceAccount token secrets are cleaned up")
		gomega.Eventually(func() bool {
			var secretList corev1.SecretList
			err := k8sPhysical.List(ctx, &secretList, client.InNamespace(testMountNamespace), client.MatchingLabels{
				cloudv1beta1.LabelServiceAccountToken: "true",
			})
			if err != nil {
				return false
			}

			// Filter secrets that belong to the current test ServiceAccount
			for _, secret := range secretList.Items {
				if secret.Annotations != nil {
					if saName, exists := secret.Annotations["kubeocean.io/service-account"]; exists && saName == serviceAccountName {
						// Found a ServiceAccount token secret that should have been deleted
						return false
					}
				}
			}
			return true
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("ServiceAccount token secrets for %s should be deleted", serviceAccountName))
	}
}

func verifyClusterBindingDeleted(ctx context.Context, clusterBindingName string) {
	ginkgo.By("Verifying ClusterBinding is deleted")

	gomega.Eventually(func() bool {
		var clusterBinding cloudv1beta1.ClusterBinding
		err := k8sVirtual.Get(ctx, types.NamespacedName{Name: clusterBindingName}, &clusterBinding)
		return apierrors.IsNotFound(err)
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "ClusterBinding should be deleted")
}

// verifyResourceCleanedUp verifies that a resource is clean of kubeocean metadata using polling
func verifyResourceCleanedUp(ctx context.Context, obj client.Object, namespacedName types.NamespacedName, resourceType string) {
	gomega.Eventually(func() bool {
		err := k8sVirtual.Get(ctx, namespacedName, obj)
		if err != nil {
			// Resource not found is OK, it means it's been deleted
			return apierrors.IsNotFound(err)
		}

		// Verify no kubeocean-specific labels (except LabelManagedBy)
		for key := range obj.GetLabels() {
			if strings.HasPrefix(key, "kubeocean.io/") && key != cloudv1beta1.LabelManagedBy {
				return false
			}
		}

		// Verify no kubeocean-specific annotations
		for key := range obj.GetAnnotations() {
			if strings.HasPrefix(key, "kubeocean.io/") {
				return false
			}
		}

		// Verify no kubeocean-specific finalizers
		for _, finalizer := range obj.GetFinalizers() {
			if strings.HasPrefix(finalizer, "kubeocean.io/") {
				return false
			}
		}

		return true
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("%s should be clean of kubeocean metadata", resourceType))
}

func verifyVirtualResourcesCleanedUp(ctx context.Context, configMapName, secretName, pvName, pvcName, serviceAccountName string) {
	ginkgo.By("Verifying virtual resources are clean of kubeocean metadata")

	// Check ConfigMap
	verifyResourceCleanedUp(ctx, &corev1.ConfigMap{}, types.NamespacedName{Name: configMapName, Namespace: testPodNamespace}, "ConfigMap")

	// Check Secret
	verifyResourceCleanedUp(ctx, &corev1.Secret{}, types.NamespacedName{Name: secretName, Namespace: testPodNamespace}, "Secret")

	// Check PV
	verifyResourceCleanedUp(ctx, &corev1.PersistentVolume{}, types.NamespacedName{Name: pvName}, "PV")

	// Check PVC
	verifyResourceCleanedUp(ctx, &corev1.PersistentVolumeClaim{}, types.NamespacedName{Name: pvcName, Namespace: testPodNamespace}, "PVC")

	// Check ServiceAccount
	if serviceAccountName != "" {
		verifyResourceCleanedUp(ctx, &corev1.ServiceAccount{}, types.NamespacedName{Name: serviceAccountName, Namespace: testPodNamespace}, "ServiceAccount")
	}

	// Check that fake pods are cleaned up from kubeocean-fake namespace
	verifyFakePodsCleanedUp(ctx)
}

func verifyPhysicalResourcesCreatedMultiple(ctx context.Context, allResourceNames *ResourceNames) {
	ginkgo.By("Verifying multiple physical resources are created")

	// Verify ConfigMaps
	for _, configMapName := range allResourceNames.ConfigMapNames {
		gomega.Eventually(func() bool {
			var physicalConfigMap corev1.ConfigMap
			physicalName := generatePhysicalName(configMapName, testPodNamespace)
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalConfigMap)
			return err == nil
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Physical ConfigMap %s should be created", configMapName))
	}

	// Verify Secrets
	for _, secretName := range allResourceNames.SecretNames {
		gomega.Eventually(func() bool {
			var physicalSecret corev1.Secret
			physicalName := generatePhysicalName(secretName, testPodNamespace)
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalSecret)
			return err == nil
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Physical Secret %s should be created", secretName))
	}

	// Verify PVs
	for _, pvName := range allResourceNames.PVNames {
		gomega.Eventually(func() bool {
			var physicalPV corev1.PersistentVolume
			physicalName := generatePhysicalName(pvName, "")
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName}, &physicalPV)
			return err == nil
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Physical PV %s should be created", pvName))
	}

	// Verify PVCs
	for _, pvcName := range allResourceNames.PVCNames {
		gomega.Eventually(func() bool {
			var physicalPVC corev1.PersistentVolumeClaim
			physicalName := generatePhysicalName(pvcName, testPodNamespace)
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalPVC)
			return err == nil
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Physical PVC %s should be created", pvcName))
	}
}

func verifyPhysicalResourcesCleanupMultiple(ctx context.Context, allResourceNames *ResourceNames) {
	ginkgo.By("Verifying multiple physical resources are cleaned up")

	// Verify ConfigMaps are deleted
	for _, configMapName := range allResourceNames.ConfigMapNames {
		gomega.Eventually(func() bool {
			var physicalConfigMap corev1.ConfigMap
			physicalName := generatePhysicalName(configMapName, testPodNamespace)
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalConfigMap)
			return apierrors.IsNotFound(err)
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Physical ConfigMap %s should be deleted", configMapName))
	}

	// Verify Secrets are deleted
	for _, secretName := range allResourceNames.SecretNames {
		gomega.Eventually(func() bool {
			var physicalSecret corev1.Secret
			physicalName := generatePhysicalName(secretName, testPodNamespace)
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &physicalSecret)
			return apierrors.IsNotFound(err)
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Physical Secret %s should be deleted", secretName))
	}

	// Verify PVs are deleted (with finalizer handling)
	for _, pvName := range allResourceNames.PVNames {
		physicalPVName := generatePhysicalName(pvName, "")
		verifyResourceDeletionWithFinalizers(ctx, fmt.Sprintf("Physical PV %s", pvName),
			types.NamespacedName{Name: physicalPVName},
			&corev1.PersistentVolume{},
			[]string{"kubernetes.io/pv-protection"})
	}

	// Verify PVCs are deleted (with finalizer handling)
	for _, pvcName := range allResourceNames.PVCNames {
		physicalPVCName := generatePhysicalName(pvcName, testPodNamespace)
		verifyResourceDeletionWithFinalizers(ctx, fmt.Sprintf("Physical PVC %s", pvcName),
			types.NamespacedName{Name: physicalPVCName, Namespace: testMountNamespace},
			&corev1.PersistentVolumeClaim{},
			[]string{"kubernetes.io/pvc-protection"})
	}

	// Verify ServiceAccount token secrets are deleted
	ginkgo.By("Verifying ServiceAccount token secrets are cleaned up")
	gomega.Eventually(func() bool {
		var secretList corev1.SecretList
		err := k8sPhysical.List(ctx, &secretList, client.InNamespace(testMountNamespace), client.MatchingLabels{
			cloudv1beta1.LabelServiceAccountToken: "true",
		})
		if err != nil {
			return false
		}

		// Filter secrets that belong to the current test ServiceAccount
		for _, secret := range secretList.Items {
			if secret.Annotations != nil {
				if saName, exists := secret.Annotations["kubeocean.io/service-account"]; exists && saName == allResourceNames.ServiceAccountName {
					// Found a ServiceAccount token secret that should have been deleted
					return false
				}
			}
		}
		return true
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("ServiceAccount token secrets for %s should be deleted", allResourceNames.ServiceAccountName))
}

func verifyVirtualResourcesCleanedUpMultiple(ctx context.Context, allResourceNames *ResourceNames) {
	ginkgo.By("Verifying multiple virtual resources are clean of kubeocean metadata")

	// Check ConfigMaps
	for _, configMapName := range allResourceNames.ConfigMapNames {
		verifyResourceCleanedUp(ctx, &corev1.ConfigMap{}, types.NamespacedName{Name: configMapName, Namespace: testPodNamespace}, fmt.Sprintf("ConfigMap %s", configMapName))
	}

	// Check Secrets
	for _, secretName := range allResourceNames.SecretNames {
		verifyResourceCleanedUp(ctx, &corev1.Secret{}, types.NamespacedName{Name: secretName, Namespace: testPodNamespace}, fmt.Sprintf("Secret %s", secretName))
	}

	// Check PVs
	for _, pvName := range allResourceNames.PVNames {
		verifyResourceCleanedUp(ctx, &corev1.PersistentVolume{}, types.NamespacedName{Name: pvName}, fmt.Sprintf("PV %s", pvName))
	}

	// Check PVCs
	for _, pvcName := range allResourceNames.PVCNames {
		verifyResourceCleanedUp(ctx, &corev1.PersistentVolumeClaim{}, types.NamespacedName{Name: pvcName, Namespace: testPodNamespace}, fmt.Sprintf("PVC %s", pvcName))
	}

	// Check ServiceAccount
	verifyResourceCleanedUp(ctx, &corev1.ServiceAccount{}, types.NamespacedName{Name: allResourceNames.ServiceAccountName, Namespace: testPodNamespace}, "ServiceAccount")

	// Check that fake pods are cleaned up from kubeocean-fake namespace
	verifyFakePodsCleanedUp(ctx)
}

// verifyResourceDeletionWithFinalizers verifies that a resource is deleted, handling finalizers that may block deletion
func verifyResourceDeletionWithFinalizers(ctx context.Context, resourceType string, namespacedName types.NamespacedName, obj client.Object, finalizersToRemove []string) {
	ginkgo.By(fmt.Sprintf("Verifying %s deletion with finalizer handling", resourceType))

	// Step 1: Verify resource has deletionTimestamp set (deletion initiated)
	gomega.Eventually(func() bool {
		err := k8sPhysical.Get(ctx, namespacedName, obj)
		if err != nil {
			return false
		}
		return obj.GetDeletionTimestamp() != nil
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("%s should have deletionTimestamp set", resourceType))

	// Step 2: Remove specified finalizers to allow deletion
	if len(finalizersToRemove) > 0 {
		gomega.Eventually(func() error {
			err := k8sPhysical.Get(ctx, namespacedName, obj)
			if err != nil {
				return err
			}

			// Remove specified finalizers
			var updatedFinalizers []string
			currentFinalizers := obj.GetFinalizers()
			for _, finalizer := range currentFinalizers {
				shouldRemove := false
				for _, toRemove := range finalizersToRemove {
					if finalizer == toRemove {
						shouldRemove = true
						break
					}
				}
				if !shouldRemove {
					updatedFinalizers = append(updatedFinalizers, finalizer)
				}
			}
			obj.SetFinalizers(updatedFinalizers)

			return k8sPhysical.Update(ctx, obj)
		}, testTimeout, testPollingInterval).Should(gomega.Succeed(), fmt.Sprintf("Should remove finalizers from %s", resourceType))
	}

	// Step 3: Verify resource is finally deleted
	gomega.Eventually(func() bool {
		err := k8sPhysical.Get(ctx, namespacedName, obj)
		return apierrors.IsNotFound(err)
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("%s should be deleted", resourceType))
}

func createOrphanedPhysicalResources(ctx context.Context, clusterBindingName string) *ResourceNames {
	ginkgo.By("Creating orphaned physical resources without corresponding virtual resources")

	// Generate unique names for orphaned resources
	orphanedConfigMapName := fmt.Sprintf("orphaned-config-%s", uniqueID[:8])
	orphanedSecretName := fmt.Sprintf("orphaned-secret-%s", uniqueID[:8])
	orphanedPVName := fmt.Sprintf("orphaned-pv-%s", uniqueID[:8])
	orphanedPVCName := fmt.Sprintf("orphaned-pvc-%s", uniqueID[:8])

	// Create orphaned ConfigMap in physical cluster
	orphanedConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generatePhysicalName(orphanedConfigMapName, testPodNamespace),
			Namespace: testMountNamespace,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy:                                  cloudv1beta1.LabelManagedByValue,
				fmt.Sprintf("kubeocean.io/synced-by-%s", clusterBindingName): cloudv1beta1.LabelValueTrue,
			},
			Annotations: map[string]string{
				cloudv1beta1.AnnotationVirtualName:      orphanedConfigMapName,
				cloudv1beta1.AnnotationVirtualNamespace: testPodNamespace,
			},
		},
		Data: map[string]string{
			"orphaned": "true",
		},
	}
	gomega.Expect(k8sPhysical.Create(ctx, orphanedConfigMap)).To(gomega.Succeed())

	// Create orphaned Secret in physical cluster
	orphanedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generatePhysicalName(orphanedSecretName, testPodNamespace),
			Namespace: testMountNamespace,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy:                                  cloudv1beta1.LabelManagedByValue,
				fmt.Sprintf("kubeocean.io/synced-by-%s", clusterBindingName): cloudv1beta1.LabelValueTrue,
			},
			Annotations: map[string]string{
				cloudv1beta1.AnnotationVirtualName:      orphanedSecretName,
				cloudv1beta1.AnnotationVirtualNamespace: testPodNamespace,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"orphaned": []byte("true"),
		},
	}
	gomega.Expect(k8sPhysical.Create(ctx, orphanedSecret)).To(gomega.Succeed())

	// Create orphaned PV in physical cluster
	orphanedPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: generatePhysicalName(orphanedPVName, ""),
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy:                                  cloudv1beta1.LabelManagedByValue,
				fmt.Sprintf("kubeocean.io/synced-by-%s", clusterBindingName): cloudv1beta1.LabelValueTrue,
			},
			Annotations: map[string]string{
				cloudv1beta1.AnnotationVirtualName: orphanedPVName,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/tmp/orphaned-pv",
				},
			},
			ClaimRef: &corev1.ObjectReference{
				Name:      generatePhysicalName(orphanedPVCName, testPodNamespace),
				Namespace: testMountNamespace,
			},
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeBound,
		},
	}
	gomega.Expect(k8sPhysical.Create(ctx, orphanedPV)).To(gomega.Succeed())

	// Create orphaned PVC in physical cluster
	orphanedPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      generatePhysicalName(orphanedPVCName, testPodNamespace),
			Namespace: testMountNamespace,
			Labels: map[string]string{
				cloudv1beta1.LabelManagedBy:                                  cloudv1beta1.LabelManagedByValue,
				fmt.Sprintf("kubeocean.io/synced-by-%s", clusterBindingName): cloudv1beta1.LabelValueTrue,
			},
			Annotations: map[string]string{
				cloudv1beta1.AnnotationVirtualName:      orphanedPVCName,
				cloudv1beta1.AnnotationVirtualNamespace: testPodNamespace,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
			VolumeName: generatePhysicalName(orphanedPVName, ""),
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: corev1.ClaimBound,
		},
	}
	gomega.Expect(k8sPhysical.Create(ctx, orphanedPVC)).To(gomega.Succeed())

	return &ResourceNames{
		ConfigMapNames: []string{orphanedConfigMapName},
		SecretNames:    []string{orphanedSecretName},
		PVNames:        []string{orphanedPVName},
		PVCNames:       []string{orphanedPVCName},
	}
}

func verifyClusterBindingInDeletingState(ctx context.Context, clusterBindingName string) {
	ginkgo.By("Verifying ClusterBinding is in deleting state")

	gomega.Eventually(func() bool {
		var clusterBinding cloudv1beta1.ClusterBinding
		err := k8sVirtual.Get(ctx, types.NamespacedName{Name: clusterBindingName}, &clusterBinding)
		if err != nil {
			return false
		}
		// ClusterBinding should exist but have a deletion timestamp
		return clusterBinding.DeletionTimestamp != nil
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "ClusterBinding should be in deleting state (with deletion timestamp)")
}

func deleteOrphanedPhysicalResources(ctx context.Context, orphanedResourceNames *ResourceNames) {
	ginkgo.By("Deleting orphaned physical resources")

	// Delete orphaned ConfigMaps
	for _, configMapName := range orphanedResourceNames.ConfigMapNames {
		physicalName := generatePhysicalName(configMapName, testPodNamespace)
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      physicalName,
				Namespace: testMountNamespace,
			},
		}
		err := k8sPhysical.Delete(ctx, configMap)
		if err != nil && !apierrors.IsNotFound(err) {
			gomega.Expect(err).To(gomega.Succeed())
		}
	}

	// Delete orphaned Secrets
	for _, secretName := range orphanedResourceNames.SecretNames {
		physicalName := generatePhysicalName(secretName, testPodNamespace)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      physicalName,
				Namespace: testMountNamespace,
			},
		}
		err := k8sPhysical.Delete(ctx, secret)
		if err != nil && !apierrors.IsNotFound(err) {
			gomega.Expect(err).To(gomega.Succeed())
		}
	}

	// Delete orphaned PVCs with finalizer handling
	for _, pvcName := range orphanedResourceNames.PVCNames {
		physicalName := generatePhysicalName(pvcName, testPodNamespace)
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      physicalName,
				Namespace: testMountNamespace,
			},
		}
		err := k8sPhysical.Delete(ctx, pvc)
		if err != nil && !apierrors.IsNotFound(err) {
			gomega.Expect(err).To(gomega.Succeed())
		}
	}

	// Delete orphaned PVs with finalizer handling
	for _, pvName := range orphanedResourceNames.PVNames {
		physicalName := generatePhysicalName(pvName, "")
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: physicalName,
			},
		}
		err := k8sPhysical.Delete(ctx, pv)
		if err != nil && !apierrors.IsNotFound(err) {
			gomega.Expect(err).To(gomega.Succeed())
		}
	}

	// Wait for ConfigMaps and Secrets to be deleted
	for _, configMapName := range orphanedResourceNames.ConfigMapNames {
		physicalName := generatePhysicalName(configMapName, testPodNamespace)
		gomega.Eventually(func() bool {
			var configMap corev1.ConfigMap
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &configMap)
			return apierrors.IsNotFound(err)
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Orphaned ConfigMap %s should be deleted", configMapName))
	}

	for _, secretName := range orphanedResourceNames.SecretNames {
		physicalName := generatePhysicalName(secretName, testPodNamespace)
		gomega.Eventually(func() bool {
			var secret corev1.Secret
			err := k8sPhysical.Get(ctx, types.NamespacedName{Name: physicalName, Namespace: testMountNamespace}, &secret)
			return apierrors.IsNotFound(err)
		}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), fmt.Sprintf("Orphaned Secret %s should be deleted", secretName))
	}

	// Use verifyResourceDeletionWithFinalizers for PVCs and PVs
	for _, pvcName := range orphanedResourceNames.PVCNames {
		physicalName := generatePhysicalName(pvcName, testPodNamespace)
		verifyResourceDeletionWithFinalizers(ctx, "Orphaned PVC",
			types.NamespacedName{Name: physicalName, Namespace: testMountNamespace},
			&corev1.PersistentVolumeClaim{},
			[]string{"kubernetes.io/pvc-protection"})
	}

	for _, pvName := range orphanedResourceNames.PVNames {
		physicalName := generatePhysicalName(pvName, "")
		verifyResourceDeletionWithFinalizers(ctx, "Orphaned PV",
			types.NamespacedName{Name: physicalName},
			&corev1.PersistentVolume{},
			[]string{"kubernetes.io/pv-protection"})
	}
}

func verifySinglePhysicalPodCreatedButNotScheduled(ctx context.Context) []string {
	ginkgo.By("Verifying single physical pod is created but not scheduled")

	var physicalPodNames []string
	gomega.Eventually(func() int {
		physicalPodNames = []string{} // Reset the slice
		var podList corev1.PodList
		err := k8sPhysical.List(ctx, &podList, client.InNamespace(testMountNamespace))
		if err != nil {
			return 0
		}

		for _, pod := range podList.Items {
			if pod.Labels != nil && pod.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
				physicalPodNames = append(physicalPodNames, pod.Name)
				// Verify pod is not scheduled (no spec.nodeName)
				gomega.Expect(pod.Spec.NodeName).To(gomega.BeEmpty(), "Physical pod should not be scheduled initially")
			}
		}
		return len(physicalPodNames)
	}, testTimeout, testPollingInterval).Should(gomega.Equal(1), "Should have 1 physical pod created")

	return physicalPodNames
}

// verifyFakePodsCleanedUp verifies that all fake pods are cleaned up from kubeocean-fake namespace
func verifyFakePodsCleanedUp(ctx context.Context) {
	ginkgo.By("Verifying fake pods are cleaned up from kubeocean-fake namespace")

	gomega.Eventually(func() bool {
		fakePods := &corev1.PodList{}
		err := k8sVirtual.List(ctx, fakePods,
			client.InNamespace("kubeocean-fake"),
			client.MatchingLabels{
				cloudv1beta1.LabelHostPortFakePod: cloudv1beta1.LabelValueTrue,
				cloudv1beta1.LabelManagedBy:       cloudv1beta1.LabelManagedByValue,
			})
		if err != nil {
			// If we can't list, assume they are not cleaned up yet
			return false
		}

		// Check if all fake pods are deleted
		if len(fakePods.Items) == 0 {
			ginkgo.GinkgoWriter.Printf("All fake pods have been cleaned up from kubeocean-fake namespace\n")
			return true
		}

		ginkgo.GinkgoWriter.Printf("Still found %d fake pods in kubeocean-fake namespace\n", len(fakePods.Items))
		return false
	}, testTimeout, testPollingInterval).Should(gomega.BeTrue(), "All fake pods should be cleaned up from kubeocean-fake namespace")
}
