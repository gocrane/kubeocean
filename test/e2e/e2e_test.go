package e2e

import (
	"context"
	"time"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	controllerpkg "github.com/TKEColocation/tapestry/pkg/controller"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
		gomega.Expect(reconciler.SetupWithManager(mgrVirtual)).To(gomega.Succeed())
		if !mgrVirtualStarted {
			mgrVirtualStarted = true
			go func() { _ = mgrVirtual.Start(ctx) }()
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
})

// helpers
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
