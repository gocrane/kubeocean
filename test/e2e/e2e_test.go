package e2e

import (
	"context"
	"os"
	"time"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	controllerpkg "github.com/TKEColocation/tapestry/pkg/controller"
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

var _ = ginkgo.Describe("Tapestry 基础 E2E 骨架", func() {
	ginkgo.It("集群注册：kubeconfig 连接性校验", func(ctx context.Context) {
		// 在 virtual 集群准备 kubeconfig Secret，并用它直连 apiserver 做连通性校验
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ns := "tapestry-system"
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "test-kubeconfig", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		// 使用该 kubeconfig 构造 client，验证 discovery ServerVersion 可用
		restCfg, err := clientcmd.RESTConfigFromKubeConfig(kc)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cs, err := kubernetes.NewForConfig(restCfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ver, err := cs.Discovery().ServerVersion()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ver).NotTo(gomega.BeNil())
	}, ginkgo.SpecTimeout(1*time.Minute))

	ginkgo.It("ClusterBindingReconciler：添加 finalizer、状态变更与 Syncer 模板缺失失败", func(ctx context.Context) {
		// 启动 manager 并注册 ClusterBindingReconciler（仅一次）
		reconciler := &controllerpkg.ClusterBindingReconciler{
			Client:   k8sVirtual,
			Scheme:   scheme,
			Log:      ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			Recorder: record.NewFakeRecorder(100),
		}
		// 仅在第一次注册 controller，避免重复注册报错
		if !cbControllerRegistered {
			gomega.Expect(reconciler.SetupWithManager(mgrVirtual)).To(gomega.Succeed())
			cbControllerRegistered = true
		}
		if !mgrVirtualStarted {
			mgrVirtualStarted = true
			go func() { _ = mgrVirtual.Start(suiteCtx) }()
		}

		// 准备 kubeconfig Secret
		ns := "tapestry-system"
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cb1-kc", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		// 创建 ClusterBinding（cluster-scoped）
		cb := &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "cb1"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				SecretRef:      corev1.SecretReference{Name: "cb1-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// 1) 最初应加上 finalizer（由于使用 GenerationChangedPredicate，不会自动二次 reconcile）
		type cbStatus struct {
			HasFinalizer bool
			Phase        string
		}
		gomega.Eventually(func() cbStatus {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return cbStatus{HasFinalizer: containsString(got.Finalizers, controllerpkg.ClusterBindingFinalizer), Phase: string(got.Status.Phase)}
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.Equal(cbStatus{HasFinalizer: true, Phase: ""}))

		// 触发一次 spec 变更（增加 ServiceNamespaces），使 generation 变化，驱动第二次 reconcile 设置 Phase=Pending
		var curr cloudv1beta1.ClusterBinding
		gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &curr)).To(gomega.Succeed())
		curr.Spec.ServiceNamespaces = []string{"default"}
		gomega.Expect(k8sVirtual.Update(ctx, &curr)).To(gomega.Succeed())
		gomega.Eventually(func() string {
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &curr)
			return string(curr.Status.Phase)
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.Equal("Pending"))

		// 2) 再次触发 spec 变更，进入后续流程；由于缺少 /etc/tapestry/syncer-template 模板，Syncer 创建会失败，Phase 变为 Failed
		gomega.Expect(k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &curr)).To(gomega.Succeed())
		curr.Spec.NodeSelector = map[string]string{"env": "test"}
		gomega.Expect(k8sVirtual.Update(ctx, &curr)).To(gomega.Succeed())
		type readyCheck struct {
			Phase  string
			Reason string
		}
		gomega.Eventually(func() readyCheck {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return readyCheck{Phase: string(got.Status.Phase), Reason: getReadyReason(&got)}
		}, 20*time.Second, 300*time.Millisecond).Should(gomega.Equal(readyCheck{Phase: "Failed", Reason: "SyncerFailed"}))
	}, ginkgo.SpecTimeout(2*time.Minute))

	ginkgo.It("ClusterBindingReconciler：成功部署 syncer（注入模板目录）", func(ctx context.Context) {
		// 设置测试模板目录环境变量（以 test/e2e 为工作目录）
		_ = os.Setenv("TAPESTRY_SYNCER_TEMPLATE_DIR", "testdata/syncer-template")

		// 注册 controller（一次）并启动 manager
		reconciler := &controllerpkg.ClusterBindingReconciler{
			Client:   k8sVirtual,
			Scheme:   scheme,
			Log:      ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			Recorder: record.NewFakeRecorder(100),
		}
		if !cbControllerRegistered {
			gomega.Expect(reconciler.SetupWithManager(mgrVirtual)).To(gomega.Succeed())
			cbControllerRegistered = true
		}
		if !mgrVirtualStarted {
			mgrVirtualStarted = true
			go func() { _ = mgrVirtual.Start(suiteCtx) }()
		}

		// 准备 kubeconfig Secret
		ns := "tapestry-system"
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cb-ok-kc", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		// 创建 ClusterBinding
		cb := &cloudv1beta1.ClusterBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: "cloud.tencent.com/v1beta1", Kind: "ClusterBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: "cb-ok"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				SecretRef:      corev1.SecretReference{Name: "cb-ok-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// 驱动到 Ready
		updateCBSpecWithRetry(ctx, cb.Name, func(o *cloudv1beta1.ClusterBinding) { o.Spec.ServiceNamespaces = []string{"default"} })
		updateCBSpecWithRetry(ctx, cb.Name, func(o *cloudv1beta1.ClusterBinding) { o.Spec.NodeSelector = map[string]string{"x": "y"} })

		gomega.Eventually(func() string {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return string(got.Status.Phase)
		}, 20*time.Second, 300*time.Millisecond).Should(gomega.Equal("Ready"))

		// 额外校验：Deployment 已创建且 ownerReference 指向该 ClusterBinding
		expectedDepName := "tapestry-syncer-" + cb.Name
		var dep appsv1.Deployment
		gomega.Eventually(func() bool {
			err := k8sVirtual.Get(ctx, types.NamespacedName{Namespace: "tapestry-system", Name: expectedDepName}, &dep)
			return err == nil
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())
		gomega.Expect(dep.OwnerReferences).NotTo(gomega.BeEmpty())
		gomega.Expect(dep.OwnerReferences[0].Name).To(gomega.Equal(cb.Name))
	}, ginkgo.SpecTimeout(3*time.Minute))

	ginkgo.It("ClusterBindingReconciler：kubeconfig Secret 缺少 key 导致 ConnectivityFailed", func(ctx context.Context) {
		// 确保 manager 已启动
		reconciler := &controllerpkg.ClusterBindingReconciler{
			Client:   k8sVirtual,
			Scheme:   scheme,
			Log:      ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			Recorder: record.NewFakeRecorder(100),
		}
		if !cbControllerRegistered {
			gomega.Expect(reconciler.SetupWithManager(mgrVirtual)).To(gomega.Succeed())
			cbControllerRegistered = true
		}
		if !mgrVirtualStarted {
			mgrVirtualStarted = true
			go func() { _ = mgrVirtual.Start(suiteCtx) }()
		}

		// Secret 缺少 kubeconfig 键
		ns := "tapestry-system"
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		bad := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bad-kc", Namespace: ns}, Data: map[string][]byte{"other": []byte("x")}}
		gomega.Expect(k8sVirtual.Create(ctx, bad)).To(gomega.Succeed())

		cb := &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "cb-missing"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				SecretRef:      corev1.SecretReference{Name: "bad-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// 等 finalizer
		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return containsString(got.Finalizers, controllerpkg.ClusterBindingFinalizer)
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		// 两次 spec 更新以推进到校验阶段
		updateCBSpecWithRetry(ctx, cb.Name, func(obj *cloudv1beta1.ClusterBinding) {
			obj.Spec.ServiceNamespaces = []string{"default"}
		})
		updateCBSpecWithRetry(ctx, cb.Name, func(obj *cloudv1beta1.ClusterBinding) {
			obj.Spec.NodeSelector = map[string]string{"x": "y"}
		})

		// 期望失败原因为 ConnectivityFailed
		gomega.Eventually(func() string {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return getReadyReason(&got)
		}, 15*time.Second, 300*time.Millisecond).Should(gomega.Equal("ConnectivityFailed"))
	}, ginkgo.SpecTimeout(2*time.Minute))

	ginkgo.It("ClusterBindingReconciler：删除流程应移除 finalizer 并删除资源", func(ctx context.Context) {
		// 确保 manager 已启动
		reconciler := &controllerpkg.ClusterBindingReconciler{
			Client:   k8sVirtual,
			Scheme:   scheme,
			Log:      ctrl.Log.WithName("e2e").WithName("ClusterBinding"),
			Recorder: record.NewFakeRecorder(100),
		}
		if !cbControllerRegistered {
			gomega.Expect(reconciler.SetupWithManager(mgrVirtual)).To(gomega.Succeed())
			cbControllerRegistered = true
		}
		if !mgrVirtualStarted {
			mgrVirtualStarted = true
			go func() { _ = mgrVirtual.Start(suiteCtx) }()
		}

		// 有效 kubeconfig Secret
		ns := "tapestry-system"
		_ = k8sVirtual.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		kc, err := kubeconfigFromRestConfig(cfgPhysical, "physical")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cb-del-kc", Namespace: ns}, Data: map[string][]byte{"kubeconfig": kc}}
		gomega.Expect(k8sVirtual.Create(ctx, secret)).To(gomega.Succeed())

		cb := &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "cb-del"},
			Spec: cloudv1beta1.ClusterBindingSpec{
				SecretRef:      corev1.SecretReference{Name: "cb-del-kc", Namespace: ns},
				MountNamespace: "default",
			},
		}
		gomega.Expect(k8sVirtual.Create(ctx, cb)).To(gomega.Succeed())

		// 等 finalizer
		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			_ = k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return containsString(got.Finalizers, controllerpkg.ClusterBindingFinalizer)
		}, 10*time.Second, 200*time.Millisecond).Should(gomega.BeTrue())

		// 触发删除
		gomega.Expect(k8sVirtual.Delete(ctx, cb)).To(gomega.Succeed())

		// 应最终被删除（finalizer 被控制器移除）
		gomega.Eventually(func() bool {
			var got cloudv1beta1.ClusterBinding
			err := k8sVirtual.Get(ctx, types.NamespacedName{Name: cb.Name}, &got)
			return apierrors.IsNotFound(err)
		}, 20*time.Second, 300*time.Millisecond).Should(gomega.BeTrue())
	}, ginkgo.SpecTimeout(2*time.Minute))
})

// helpers
func updateCBSpecWithRetry(ctx context.Context, name string, mutate func(*cloudv1beta1.ClusterBinding)) {
	// 使用重试避免与控制器的并发状态更新产生冲突
	gomega.Eventually(func() error {
		var curr cloudv1beta1.ClusterBinding
		if err := k8sVirtual.Get(ctx, types.NamespacedName{Name: name}, &curr); err != nil {
			return err
		}
		mutate(&curr)
		return k8sVirtual.Update(ctx, &curr)
	}, 5*time.Second, 200*time.Millisecond).Should(gomega.Succeed())
}

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
