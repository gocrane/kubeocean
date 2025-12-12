# Kubeocean 一键部署脚本箱

> [English](README.md) | 中文

本目录提供了 Kubeocean 组件和集群绑定的一键部署脚本箱，支持在 TKE 集群上一站式体验 Kubeocean 相关功能的能力。

## 功能

- 在算力集群和工作集群部署 Kubeocean 组件
- 绑定工作集群到算力集群，并配置简单资源抽取策略

## 前置准备（环境要求）

- 需要至少一个 kubernetes 集群作为虚拟算力集群，以及一个 kubernetes 集群作为工作集群。
- 算力集群和业务集群 Pod 网络直接互通、节点网络互通
- 可以使用 `kubectl` 访问集群
- 本地环境已安装 `helm`，且版本为 v3
- 集群都已开启 APIServer 内网访问，即在 `default` namespace 下存在名为 `kubernetes-intranet` 且类型为 `LoadBalancer` 的服务。若集群为 TKE 标准集群，可参考下图在集群控制台开启：
![k8s-svc](../../docs/images/k8s-svc.png)
- 其他要求参考：[要求](../../docs/requirements_zh.md)

## 基础使用

### 安装并绑定
```
# kubectl 切换到工作集群
bash install-worker.sh
# 给期望抽取资源的节点添加 label
kubectl label node <nodeName1> role=worker

# 将 kubectl 切换到算力集群，并将上一步生成的 /tmp/kubeconfig-worker 拷贝到本地
bash install-manager.sh
```
*注意：脚本执行顺序不能修改*

### 卸载
```
# kubectl 切换到算力集群
bash uninstall-manager.sh

# kubectl 切换到工作集群
bash uninstall-worker.sh
```
*注意：脚本执行顺序不能修改*

## 进阶使用

### 安装并绑定
```
# 指定 worker kubeconfig 的输出路径
bash install-worker.sh -o /tmp/my-kubeconfig
bash install-worker.sh --output /tmp/my-kubeconfig

# 跳过 ResourceLeasingPolicy 的部署
bash install-worker.sh --skip-rlp

# 安装 manager 指定工作集群的 ID 和名称
bash install-manager.sh -i cls-prod -n prod-cluster
bash install-manager.sh --cluster-id cls-prod --cluster-name prod-cluster

# 安装 manager 指定 worker kubeconfig 的输入路径
bash install-manager.sh -w /tmp/kubeconfig-worker1
bash install-manager.sh --worker-kubeconfig /tmp/kubeconfig-worker1

# 只绑定集群，跳过安装 manager 步骤，并指定集群ID和名称
bash install-manager.sh -i cls-prod -n prod-cluster --skip-manager
```

### 卸载
```
# 解绑特定名称的工作集群
bash uninstall-manager.sh -n worker1
bash uninstall-manager.sh --cluster-name worker1

# 只解绑工作集群，不卸载 manager 组件
bash uninstall-manager.sh -n worker1 --skip-manager

# 卸载并清理特定名称的 rlp 对象
bash uninstall-worker.sh -r my-policy
bash uninstall-worker.sh --rlp-name my-policy
```