package integration

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	clsv1 "github.com/gocrane/kubeocean/api/cls/v1"
	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	testEnvVirtual  *envtest.Environment
	testEnvPhysical *envtest.Environment
	cfgVirtual      *rest.Config
	cfgPhysical     *rest.Config
	scheme          = runtime.NewScheme()
	k8sVirtual      client.Client
	k8sPhysical     client.Client
	mgrVirtual      ctrl.Manager
	mgrPhysical     ctrl.Manager
	suiteCtx        context.Context
	suiteCancel     context.CancelFunc
	uniqueID        string

	k8sVirtualClient kubernetes.Interface
)

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Kubeocean E2E Suite")
}

var _ = ginkgo.BeforeSuite(func(ctx context.Context) {
	// 日志
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// 注册必要的 Scheme
	gomega.Expect(cloudv1beta1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(corev1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(appsv1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(rbacv1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(coordinationv1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(storagev1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(schedulingv1.AddToScheme(scheme)).To(gomega.Succeed())
	gomega.Expect(clsv1.AddToScheme(scheme)).To(gomega.Succeed())
})

var _ = ginkgo.BeforeEach(func(ctx context.Context) {
	// 启动虚拟集群 envtest，加载 CRDs
	testEnvVirtual = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	}
	var err error
	cfgVirtual, err = testEnvVirtual.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfgVirtual).NotTo(gomega.BeNil())

	// 启动物理集群 envtest，加载 CRDs
	testEnvPhysical = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	}
	cfgPhysical, err = testEnvPhysical.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfgPhysical).NotTo(gomega.BeNil())

	// 构造两个集群的 client
	k8sVirtual, err = client.New(cfgVirtual, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	k8sVirtualClient, err = kubernetes.NewForConfig(cfgVirtual)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	k8sPhysical, err = client.New(cfgPhysical, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// 创建必要的 namespaces
	createRequiredNamespaces(ctx)

	// 创建必要的 services
	createRequiredServices(ctx)

	// 创建新的 Manager 实例，确保每个测试用例都有独立的 manager
	suiteCtx, suiteCancel = context.WithCancel(context.Background())
	mgrVirtual, err = ctrl.NewManager(cfgVirtual, ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Node{}: {
					Label: labels.SelectorFromSet(map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					}),
				},
			},
		},
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	mgrPhysical, err = ctrl.NewManager(cfgPhysical, ctrl.Options{
		Scheme: scheme,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// 启动虚拟集群 manager
	go func() {
		defer ginkgo.GinkgoRecover()
		if err := mgrVirtual.Start(suiteCtx); err != nil {
			ginkgo.Fail(fmt.Sprintf("Virtual manager failed to start: %v", err))
		}
	}()
	uniqueID = generateUniqueID()
})

var _ = ginkgo.AfterEach(func() {
	if suiteCancel != nil {
		suiteCancel()
	}
	if testEnvPhysical != nil {
		_ = testEnvPhysical.Stop()
	}
	if testEnvVirtual != nil {
		_ = testEnvVirtual.Stop()
	}
})

// kubeconfigFromRestConfig 将 rest.Config 转换为 kubeconfig 字节串，便于写入 Secret 使用
func kubeconfigFromRestConfig(cfg *rest.Config, _ string) ([]byte, error) {
	c := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"physical": {
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
			"default": {Cluster: "physical", AuthInfo: "default"},
		},
		CurrentContext: "default",
	}
	return clientcmd.Write(c)
}

// generateUniqueID generates a unique identifier for test resources
func generateUniqueID() string {
	return strconv.FormatInt(time.Now().UnixNano()+int64(rand.Intn(1000)), 36)
}

// createRequiredNamespaces creates necessary namespaces for integration tests
func createRequiredNamespaces(ctx context.Context) {
	// Create default namespace if it doesn't exist
	defaultNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
	}
	_ = k8sVirtual.Create(ctx, defaultNs)

	// Create kube-system namespace if it doesn't exist
	kubeSystemNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kube-system",
		},
	}
	_ = k8sVirtual.Create(ctx, kubeSystemNs)
}

// createRequiredServices creates necessary services for integration tests
func createRequiredServices(ctx context.Context) {
	// Create kubernetes-intranet service in default namespace
	kubernetesIntranetService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes-intranet",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				{
					Port: 443,
					Name: "https",
				},
			},
			Selector: map[string]string{
				"app": "kubernetes-intranet",
			},
		},
	}
	gomega.Expect(k8sVirtual.Create(ctx, kubernetesIntranetService)).To(gomega.Succeed())

	// Update the service status to simulate LoadBalancer ingress
	kubernetesIntranetService.Status = corev1.ServiceStatus{
		LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{
				{IP: "10.0.0.1"},
			},
		},
	}
	gomega.Expect(k8sVirtual.Status().Update(ctx, kubernetesIntranetService)).To(gomega.Succeed())

	// Create kube-dns-intranet service in kube-system namespace
	kubeDnsIntranetService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kube-dns-intranet",
			Namespace: "kube-system",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				{
					Port: 53,
					Name: "dns",
				},
			},
			Selector: map[string]string{
				"k8s-app": "kube-dns",
			},
		},
	}
	gomega.Expect(k8sVirtual.Create(ctx, kubeDnsIntranetService)).To(gomega.Succeed())

	// Update the service status to simulate LoadBalancer ingress
	kubeDnsIntranetService.Status = corev1.ServiceStatus{
		LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{
				{IP: "10.0.0.2"},
			},
		},
	}
	gomega.Expect(k8sVirtual.Status().Update(ctx, kubeDnsIntranetService)).To(gomega.Succeed())
}

// CreateDefaultResourceLeasingPolicy creates a default ResourceLeasingPolicy that matches any node
// in the specified cluster. This is useful for tests that need a policy to exist but don't
// want to specify complex matching criteria.
func CreateDefaultResourceLeasingPolicy(ctx context.Context, k8sPhysical client.Client, clusterName string, policyName string) *cloudv1beta1.ResourceLeasingPolicy {
	policy := &cloudv1beta1.ResourceLeasingPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: policyName,
		},
		Spec: cloudv1beta1.ResourceLeasingPolicySpec{
			Cluster: clusterName,
			// nil NodeSelector means it matches any node
			NodeSelector: nil,
			// empty ResourceLimits means no resource restrictions
			ResourceLimits: nil,
		},
	}

	gomega.Expect(k8sPhysical.Create(ctx, policy)).To(gomega.Succeed())
	return policy
}
