# 要求

## kubernetes 版本要求

kubeocean 组件依赖 controller-runtime v0.21.0 和 client-go v0.33.3 构建，在 **kubernetes v1.28** 版本做了完整的测试验证，因此要求 kubeocean 组件部署的算力集群和 worker 集群的版本至少大于等于 **v1.28**。其他版本的 kubernetes 集群未经完整验证，不保证支持。

## 网络要求

- 算力集群和业务集群 Pod 网络直接互通、节点网络互通
- 算力集群可为 `apiserver` 和 `kube-dns` 提供 LoadBalancer 类型的服务，业务集群可访问

## 集群提供商要求

当前已经过验证的 kubernetes 集群提供商（provider）：

- [TKE(Tencent Kubernetes Engine)](https://cloud.tencent.com/product/tke)
- 本地部署 kubernetes 集群：需要构建类型为 `LoadBalancer` 的 `kubernetes-intranet` 和 `kube-dns-intranet` 的 Service，可参考项目中的 `make kind-deploy-pre` 命令的方案通过 nodePort 等方式构建这两个 Service。
