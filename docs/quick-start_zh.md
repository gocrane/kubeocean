# 快速开始

## 环境要求

需要至少两个 kubernetes 集群：一个算力集群、多个业务集群，可以直接使用 TKE 集群。kubernetes 版本要求至少 1.28+。

## 部署使用样例

**在业务集群中：**

1. 创建权限 `helm install kubeocean-worker charts/kubeocean-worker`
2. 获取凭证 `bash hack/kubeconfig.sh kubeocean-syncer kubeocean-worker /tmp/kubeconfig.buz.1`
3. 创建 ResourceLeasingPolicy `kubectl create -f examples/resourceleasingpolicy_sample.yaml`

**在算力集群中：**

1. 安装kubeocean组件 `helm install kubeocean charts/kubeocean`
2. 设置凭证 `kubectl create secret generic -n kubeocean-system worker-cluster-kubeconfig --from-file=kubeconfig=/tmp/kubeconfig.buz.1`
3. 创建 ClusterBinding `kubectl create -f examples/clusterbinding_sample.yaml`
