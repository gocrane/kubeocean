package e2e

import (
	"context"
	"path/filepath"
	"testing"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	testEnvVirtual    *envtest.Environment
	testEnvPhysical   *envtest.Environment
	cfgVirtual        *rest.Config
	cfgPhysical       *rest.Config
	scheme            = runtime.NewScheme()
	k8sVirtual        client.Client
	k8sPhysical       client.Client
	mgrVirtual        ctrl.Manager
	mgrPhysical       ctrl.Manager
	mgrVirtualStarted bool
)

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Tapestry E2E Suite")
}

var _ = ginkgo.BeforeSuite(func(ctx context.Context) {
	// 日志
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// 注册必要的 Scheme
	gomega.Expect(cloudv1beta1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(corev1.AddToScheme(scheme)).To(gomega.Succeed())

	// 启动虚拟集群 envtest，加载 CRDs
	testEnvVirtual = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	}
	var err error
	cfgVirtual, err = testEnvVirtual.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfgVirtual).NotTo(gomega.BeNil())

	// 启动物理集群 envtest（仅内建资源）
	testEnvPhysical = &envtest.Environment{}
	cfgPhysical, err = testEnvPhysical.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfgPhysical).NotTo(gomega.BeNil())

	// 构造两个集群的 client
	k8sVirtual, err = client.New(cfgVirtual, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	k8sPhysical, err = client.New(cfgPhysical, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// 仅创建 Manager，测试中按需启动
	mgrVirtual, err = ctrl.NewManager(cfgVirtual, ctrl.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	mgrPhysical, err = ctrl.NewManager(cfgPhysical, ctrl.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})

var _ = ginkgo.AfterSuite(func() {
	if testEnvPhysical != nil {
		_ = testEnvPhysical.Stop()
	}
	if testEnvVirtual != nil {
		_ = testEnvVirtual.Stop()
	}
})

// kubeconfigFromRestConfig 将 rest.Config 转换为 kubeconfig 字节串，便于写入 Secret 使用
func kubeconfigFromRestConfig(cfg *rest.Config, clusterName string) ([]byte, error) {
	c := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterName: {
				Server:                   cfg.Host,
				CertificateAuthorityData: cfg.CAData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default": {
				ClientCertificateData: cfg.CertData,
				ClientKeyData:         cfg.KeyData,
				Token:                 cfg.BearerToken,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default": {Cluster: clusterName, AuthInfo: "default"},
		},
		CurrentContext: "default",
	}
	return clientcmd.Write(c)
}
