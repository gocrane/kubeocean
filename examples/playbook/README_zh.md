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
kubectl label node <nodeName1> kubeocean.io/role=worker

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

## TKE 集群一键部署

针对腾讯云 TKE 集群，提供了专门的一键部署脚本，可以自动完成集群配置（开启内网访问、创建 kube-dns-intranet 等）和组件安装。

### 前置准备

除了上述基础环境要求外，还需要：
- 安装并配置 [腾讯云 CLI 工具 (tccli)](https://cloud.tencent.com/document/product/440/6176)
- 安装 [jq](https://jqlang.org/download/) JSON 处理工具
- 准备 TKE 集群所在的地域、集群 ID 和子网 ID

### 部署 Worker 集群

```bash
# 基本使用（使用默认 kubeconfig 输出路径 /tmp/kubeconfig-worker）
bash install-worker-tke.sh \
  --region ap-guangzhou \
  --cluster-id cls-xxxxxxxx \
  --subnet-id subnet-xxxxxxxx

# 指定自定义 kubeconfig 输出路径
bash install-worker-tke.sh \
  --region ap-guangzhou \
  --cluster-id cls-xxxxxxxx \
  --subnet-id subnet-xxxxxxxx \
  --output /tmp/my-kubeconfig

# 短参数形式
bash install-worker-tke.sh \
  -r ap-guangzhou \
  -c cls-xxxxxxxx \
  -s subnet-xxxxxxxx
```

脚本会自动完成以下操作：
1. 检查前置条件（tccli、jq、kubectl、helm 等）
2. 开启集群内网访问（如未开启）
3. 获取集群 kubeconfig 并配置 context（名称：`worker-admin-<集群ID>`）
4. 安装 kubeocean-worker 组件
5. 生成 worker kubeconfig 用于 manager 集群绑定

### 部署 Manager 集群

```bash
# 基本使用（使用默认 worker kubeconfig 路径 /tmp/kubeconfig-worker）
bash install-manager-tke.sh \
  --region ap-guangzhou \
  --cluster-id cls-xxxxxxxx \
  --subnet-id subnet-xxxxxxxx \
  --worker-cluster-id cls-worker-xxx

# 指定自定义 worker kubeconfig 路径
bash install-manager-tke.sh \
  --region ap-guangzhou \
  --cluster-id cls-xxxxxxxx \
  --subnet-id subnet-xxxxxxxx \
  --worker-kubeconfig /tmp/my-kubeconfig \
  --worker-cluster-id cls-worker-xxx

# 短参数形式
bash install-manager-tke.sh \
  -r ap-guangzhou \
  -c cls-xxxxxxxx \
  -s subnet-xxxxxxxx \
  -i cls-worker-xxx
```

脚本会自动完成以下操作：
1. 检查前置条件（tccli、jq、kubectl、helm、worker kubeconfig 等）
2. 开启集群内网访问（如未开启）
3. 获取集群 kubeconfig 并配置 context（名称：`manager-admin-<集群ID>`）
4. 创建 kube-dns-intranet 服务（如不存在）
5. 安装 kubeocean-manager 组件
6. 创建 ClusterBinding 绑定 worker 集群

### 完整示例

```bash
# Step 1: 部署 worker 集群
bash install-worker-tke.sh \
  -r ap-guangzhou \
  -c cls-xxxxxxxx \
  -s subnet-xxxxxxxx

# 给节点添加 label 以标记资源抽取节点
kubectl label node <nodeName> kubeocean.io/role=worker

# Step 2: 部署 manager 集群（会自动使用 /tmp/kubeconfig-worker）
bash install-manager-tke.sh \
  -r ap-guangzhou \
  -c cls-xxxxxxxx \
  -s subnet-xxxxxxxx \
  -i cls-xxxxxxxx

# Step 3: 验证部署
# 切换到 manager 集群 context
kubectl config use-context manager-admin-cls-manager-xxx
kubectl get clusterbindings
kubectl get all -n kubeocean-system

# 切换到 worker 集群 context
kubectl config use-context worker-admin-cls-worker-xxx
kubectl get all -n kubeocean-worker
```

### 参数说明

#### install-worker-tke.sh

| 参数 | 短参数 | 说明 | 是否必需 | 默认值 |
|------|--------|------|----------|--------|
| `--region` | `-r` | TKE 集群所在地域 | 是 | - |
| `--cluster-id` | `-c` | TKE 集群 ID | 是 | - |
| `--subnet-id` | `-s` | 子网 ID（用于内网访问） | 是（开启内网访问时） | - |
| `--output` | `-o` | Worker kubeconfig 输出路径 | 否 | `/tmp/kubeconfig-worker` |

#### install-manager-tke.sh

| 参数 | 短参数 | 说明 | 是否必需 | 默认值 |
|------|--------|------|----------|--------|
| `--region` | `-r` | TKE 集群所在地域 | 是 | - |
| `--cluster-id` | `-c` | TKE 集群 ID | 是 | - |
| `--subnet-id` | `-s` | 子网 ID（用于内网访问和 DNS 服务） | 是（开启内网访问或创建 DNS 服务时） | - |
| `--worker-kubeconfig` | `-w` | Worker 集群 kubeconfig 路径 | 否 | `/tmp/kubeconfig-worker` |
| `--worker-cluster-id` | `-i` | Worker 集群 ID | 是 | - |
```