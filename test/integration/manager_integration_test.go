package integration

import (
	"context"
	"os"
	"time"

	cloudv1beta1 "github.com/TKEColocation/kubeocean/api/v1beta1"
	controllerpkg "github.com/TKEColocation/kubeocean/pkg/controller"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	// testSystemNamespace is the namespace used for system resources in tests
	testSystemNamespace = "kubeocean-system"
)

var _ = ginkgo.Describe("Manager Integration Tests", func() {
	ginkgo.It("Cluster Registration: kubeconfig connectivity validation", func(ctx context.Context) {
		// Prepare kubeconfig Secret in virtual cluster and use it to connect to apiserver for connectivity validation
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ns := testSystemNamespace
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "test-kubeconfig", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		// Use the kubeconfig to construct client and verify discovery ServerVersion is available
		restCfg, err := clientcmd.RESTConfigFromKubeConfig(kc)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cs, err := kubernetes.NewForConfig(restCfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ver, err := cs.Discovery().ServerVersion()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ver).NotTo(gomega.BeNil())
	}, ginkgo.SpecTimeout(1*time.Minute))

	ginkgo.It("ClusterBindingReconciler: add finalizer, status change and Syncer template missing failure", func(ctx context.Context) {
		_ = os.Setenv("KUBEOCEAN_PROXIER_TEMPLATE_DIR", "testdata/proxier-template")
		// Start manager and register ClusterBindingReconciler (once only)
		reconciler := controllerpkg.NewClusterBindingReconciler(
			k8sVirtual,
			scheme,
			ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			record.NewFakeRecorder(100),
		)
		// Register controller only once to avoid duplicate registration errors
		gomega.Expect(reconciler.SetupWithManagerAndName(mgrVirtual, "cb-finalizer")).To(gomega.Succeed())

		// Prepare kubeconfig Secret
		ns := testSystemNamespace
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cb1-kc", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		// Create ClusterBinding (cluster-scoped)
		cb := &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "cb1"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:      "cb1",
				SecretRef:      corev1.SecretReference{Name: "cb1-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// 1) Initially should add finalizer (automatic second reconcile)

		// 2) Proceed to subsequent flow; due to missing /etc/kubeocean/syncer-template template, Syncer creation will fail, Phase becomes Failed
		type readyCheck struct {
			HasFinalizer bool
			Phase        string
			Reason       string
		}
		gomega.Eventually(func() readyCheck {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return readyCheck{
				HasFinalizer: containsString(got.Finalizers, cloudv1beta1.ClusterBindingManagerFinalizer),
				Phase:        string(got.Status.Phase),
				Reason:       getReadyReason(&got),
			}
		}, 20*time.Second, 300*time.Millisecond).Should(gomega.Equal(readyCheck{HasFinalizer: true, Phase: "Failed", Reason: "SyncerFailed"}))
	}, ginkgo.SpecTimeout(2*time.Minute))

	ginkgo.It("ClusterBindingReconciler: successfully deploy syncer and proxier (with template directories)", func(ctx context.Context) {
		// Set test template directory environment variables (with test/e2e as working directory)
		_ = os.Setenv("KUBEOCEAN_SYNCER_TEMPLATE_DIR", "testdata/syncer-template")
		_ = os.Setenv("KUBEOCEAN_PROXIER_TEMPLATE_DIR", "testdata/proxier-template")

		// Register controller (once) and start manager
		reconciler := controllerpkg.NewClusterBindingReconciler(
			k8sVirtual,
			scheme,
			ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			record.NewFakeRecorder(100),
		)
		gomega.Expect(reconciler.SetupWithManagerAndName(mgrVirtual, "cb-syncer")).To(gomega.Succeed())

		// Prepare kubeconfig Secret
		ns := testSystemNamespace
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cb-ok-kc", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		// Create ClusterBinding
		cb := &cloudv1beta1.ClusterBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "cloud.tencent.com/v1beta1", Kind: "ClusterBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: "cb-ok"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:      "cb-ok",
				SecretRef:      corev1.SecretReference{Name: "cb-ok-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// Wait for ClusterBinding to be Ready
		gomega.Eventually(func() string {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return string(got.Status.Phase)
		}, 20*time.Second, 300*time.Millisecond).Should(gomega.Equal("Ready"))

		// Additional validation: Syncer Deployment is created and ownerReference points to the ClusterBinding
		expectedSyncerDepName := "kubeocean-syncer-" + cb.Spec.ClusterID
		var syncerDep appsv1.Deployment
		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedSyncerDepName}, &syncerDep)
			return err == nil
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())
		gomega.Expect(syncerDep.OwnerReferences).NotTo(gomega.BeEmpty())
		gomega.Expect(syncerDep.OwnerReferences[0].Name).To(gomega.Equal(cb.Name))

		// Additional validation: Proxier Deployment is created and ownerReference points to the ClusterBinding
		expectedProxierDepName := "kubeocean-proxier-" + cb.Spec.ClusterID
		var proxierDep appsv1.Deployment
		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedProxierDepName}, &proxierDep)
			return err == nil
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())
		gomega.Expect(proxierDep.OwnerReferences).NotTo(gomega.BeEmpty())
		gomega.Expect(proxierDep.OwnerReferences[0].Name).To(gomega.Equal(cb.Name))

		// Additional validation: Proxier Service is created and ownerReference points to the ClusterBinding
		expectedProxierSvcName := "kubeocean-proxier-" + cb.Spec.ClusterID + "-svc"
		var proxierSvc corev1.Service
		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedProxierSvcName}, &proxierSvc)
			return err == nil
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())
		gomega.Expect(proxierSvc.OwnerReferences).NotTo(gomega.BeEmpty())
		gomega.Expect(proxierSvc.OwnerReferences[0].Name).To(gomega.Equal(cb.Name))
	}, ginkgo.SpecTimeout(3*time.Minute))

	ginkgo.It("ClusterBindingReconciler: kubeconfig Secret missing key causes ConnectivityFailed", func(ctx context.Context) {
		// Ensure manager is started
		reconciler := controllerpkg.NewClusterBindingReconciler(
			k8sVirtual,
			scheme,
			ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			record.NewFakeRecorder(100),
		)
		gomega.Expect(reconciler.SetupWithManagerAndName(mgrVirtual, "cb-bad-kc")).To(gomega.Succeed())

		// Secret missing kubeconfig key
		ns := testSystemNamespace
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		bad := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bad-kc", Namespace: ns}, Data: map[string][]byte{"other": []byte("x")}}
		gomega.Expect(k8sVirtual.Create(ctx, bad)).To(gomega.Succeed())

		cb := &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "cb-missing"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:      "cb-missing",
				SecretRef:      corev1.SecretReference{Name: "bad-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// Wait for finalizer
		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return containsString(got.Finalizers, cloudv1beta1.ClusterBindingManagerFinalizer)
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		// Expect failure reason to be ConnectivityFailed
		gomega.Eventually(func() string {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return getReadyReason(&got)
		}, 15*time.Second, 300*time.Millisecond).Should(gomega.Equal("ConnectivityFailed"))
	}, ginkgo.SpecTimeout(2*time.Minute))

	ginkgo.It("ClusterBindingReconciler: deletion process should remove finalizer and delete resources", func(ctx context.Context) {
		// Set test template directory environment variables (with test/e2e as working directory)
		_ = os.Setenv("KUBEOCEAN_SYNCER_TEMPLATE_DIR", "testdata/syncer-template")
		_ = os.Setenv("KUBEOCEAN_PROXIER_TEMPLATE_DIR", "testdata/proxier-template")

		// Ensure manager is started
		reconciler := controllerpkg.NewClusterBindingReconciler(
			k8sVirtual,
			scheme,
			ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			record.NewFakeRecorder(100),
		)
		gomega.Expect(reconciler.SetupWithManagerAndName(mgrVirtual, "cb-clean")).To(gomega.Succeed())

		// Valid kubeconfig Secret
		ns := testSystemNamespace
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cb-del-kc", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		cb := &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "cb-del"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:      "cb-del",
				SecretRef:      corev1.SecretReference{Name: "cb-del-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// Wait for finalizer
		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return containsString(got.Finalizers, cloudv1beta1.ClusterBindingManagerFinalizer)
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		// Trigger deletion
		gomega.Expect(k8sVirtual.Delete(ctx, cb)).To(gomega.Succeed())

		// Should eventually be deleted (finalizer removed by controller)
		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			err := k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return apierrors.IsNotFound(err)
		}, 20*time.Second, 300*time.Millisecond).Should(gomega.BeTrue())
	}, ginkgo.SpecTimeout(2*time.Minute))

	ginkgo.It("ClusterBindingReconciler: complete ClusterBinding lifecycle test", func(ctx context.Context) {
		// 1. Prepare environment and create ClusterBinding
		_ = os.Setenv("KUBEOCEAN_SYNCER_TEMPLATE_DIR", "testdata/syncer-template")
		_ = os.Setenv("KUBEOCEAN_PROXIER_TEMPLATE_DIR", "testdata/proxier-template")

		// Register controller and start manager
		reconciler := controllerpkg.NewClusterBindingReconciler(
			k8sVirtual,
			scheme,
			ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			record.NewFakeRecorder(100),
		)
		gomega.Expect(reconciler.SetupWithManagerAndName(mgrVirtual, "cb-lifecycle")).To(gomega.Succeed())

		// Prepare kubeconfig Secret
		ns := testSystemNamespace
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cb-lifecycle-kc", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		// Create ClusterBinding
		ginkgo.By("Creating ClusterBinding")
		cb := &cloudv1beta1.ClusterBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "cloud.tencent.com/v1beta1", Kind: "ClusterBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: "cb-lifecycle"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:      "cb-lifecycle",
				SecretRef:      corev1.SecretReference{Name: "cb-lifecycle-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// 2. Verify ClusterBinding status is Ready with manager-added finalizer
		ginkgo.By("Verifying ClusterBinding status is Ready with manager-added finalizer")
		gomega.Eventually(func() string {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return string(got.Status.Phase)
		}, 20*time.Second, 300*time.Millisecond).Should(gomega.Equal("Ready"))

		// Verify manager finalizer is present
		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return containsString(got.Finalizers, cloudv1beta1.ClusterBindingManagerFinalizer)
		}, 5*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		// 3. Verify syncer deployment, proxier deployment, and service are successfully created
		ginkgo.By("Verifying syncer deployment, proxier deployment, and service are successfully created")
		expectedSyncerDepName := "kubeocean-syncer-" + cb.Spec.ClusterID
		var syncerDep appsv1.Deployment
		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedSyncerDepName}, &syncerDep)
			return err == nil
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		expectedProxierDepName := "kubeocean-proxier-" + cb.Spec.ClusterID
		var proxierDep appsv1.Deployment
		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedProxierDepName}, &proxierDep)
			return err == nil
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		expectedProxierSvcName := "kubeocean-proxier-" + cb.Spec.ClusterID + "-svc"
		var proxierSvc corev1.Service
		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedProxierSvcName}, &proxierSvc)
			return err == nil
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		// 4. Add ClusterBindingSyncerFinalizer to ClusterBinding
		ginkgo.By("Adding ClusterBindingSyncerFinalizer to ClusterBinding")
		var currentCB cloudv1beta1.ClusterBinding
		gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &currentCB)).To(gomega.Succeed())
		currentCB.Finalizers = append(currentCB.Finalizers, cloudv1beta1.ClusterBindingSyncerFinalizer)
		gomega.Expect(k8sVirtual.Update(ctx, &currentCB)).To(gomega.Succeed())

		// Verify syncer finalizer is added
		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return containsString(got.Finalizers, cloudv1beta1.ClusterBindingSyncerFinalizer)
		}, 5*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		// 5. Delete ClusterBinding, wait 2s, confirm ClusterBinding still exists with non-empty deletionTimestamp
		// and confirm syncer deploy, proxier deploy, service still exist
		ginkgo.By("Deleting ClusterBinding, waiting 2s, confirming ClusterBinding still exists with non-empty deletionTimestamp and confirming syncer deploy, proxier deploy, service still exist")
		gomega.Expect(k8sVirtual.Delete(ctx, &currentCB)).To(gomega.Succeed())

		// Wait 2 seconds as requested
		time.Sleep(2 * time.Second)

		// Verify ClusterBinding still exists with deletionTimestamp
		var deletingCB cloudv1beta1.ClusterBinding
		gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &deletingCB)).To(gomega.Succeed())
		gomega.Expect(deletingCB.DeletionTimestamp).NotTo(gomega.BeNil())

		// Verify resources still exist
		gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedSyncerDepName}, &syncerDep)).To(gomega.Succeed())
		gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedProxierDepName}, &proxierDep)).To(gomega.Succeed())
		gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedProxierSvcName}, &proxierSvc)).To(gomega.Succeed())

		// 6. Remove ClusterBindingSyncerFinalizer
		ginkgo.By("Removing ClusterBindingSyncerFinalizer from ClusterBinding")
		gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &deletingCB)).To(gomega.Succeed())

		// Remove syncer finalizer from the list
		var newFinalizers []string
		for _, finalizer := range deletingCB.Finalizers {
			if finalizer != cloudv1beta1.ClusterBindingSyncerFinalizer {
				newFinalizers = append(newFinalizers, finalizer)
			}
		}
		deletingCB.Finalizers = newFinalizers
		gomega.Expect(k8sVirtual.Update(ctx, &deletingCB)).To(gomega.Succeed())

		// 7. Confirm syncer deploy, proxier deploy, service are successfully deleted,
		// and confirm ClusterBinding is successfully deleted
		ginkgo.By("Confirming syncer deploy, proxier deploy, service are successfully deleted, and ClusterBinding is successfully deleted")
		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedSyncerDepName}, &syncerDep)
			return apierrors.IsNotFound(err)
		}, 15*time.Second, 300*time.Millisecond).Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedProxierDepName}, &proxierDep)
			return apierrors.IsNotFound(err)
		}, 15*time.Second, 300*time.Millisecond).Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "kubeocean-system", Name: expectedProxierSvcName}, &proxierSvc)
			return apierrors.IsNotFound(err)
		}, 15*time.Second, 300*time.Millisecond).Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			err := k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return apierrors.IsNotFound(err)
		}, 15*time.Second, 300*time.Millisecond).Should(gomega.BeTrue())
	}, ginkgo.SpecTimeout(4*time.Minute))
})

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func getReadyReason(cb *cloudv1beta1.ClusterBinding) string {
	for _, c := range cb.Status.Conditions {
		if c.Type == "Ready" {
			return c.Reason
		}
	}
	return ""
}
