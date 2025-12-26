# LLM 训练和推理跨集群部署示例

> [English](README.md) | 中文

本目录包含使用 Kubeocean 进行 LLM 训练和推理跨集群部署的完整示例。

## 场景描述

本目录支持从零构建了一个实际的训推一体化的场景，该场景使用了两个集群，分别部署在线推理业务和离线训练业务，在线推理业务存在潮汐特性，白天（每日8点-18点）用量较高，需要使用在线集群的全部 GPU 资源，而晚上（每日18点-次日8点）用量较低，只需一半的 GPU 资源。离线训练业务只部署在离线集群，且只在每日晚上在线集群腾挪出 GPU 资源时才部署任务。

在线推理业务使用 vLLM 部署在线推理服务，而离线训练任务使用了 Kuberay+VeRL 部署大模型强化学习训练任务。使用的模型都是`Qwen2.5-0.5B-Instruct`，训练数据集为 GSM8K。

目标：实现夜间训练任务自动利用闲置GPU，白天训练任务自动退出，保障在线推理业务资源。

注：本项目做了一定简化，模型和数据集都保存在镜像中，训练完的 checkpoints 也保存在本地目录，最佳实践建议，模型和数据集可通过文件存储等云存储系统加载，训练完的 checkpoints 也可通过文件存储等云存储系统保存。


## 目录结构

```
.
├── config.env.example              # 配置文件示例
├── run-demo.sh                     # 一键部署脚本（推荐使用）
├── clean-demo.sh                   # 一键清理脚本（推荐使用）
├── vllm-infer-demo.sh              # vLLM 推理服务部署脚本
├── llm-kuberay-verl-demo.sh        # VERL 训练任务部署脚本
├── is-qwen2-5-05b-vllm.yaml        # vLLM 推理服务 YAML 配置
└── verl-raycluster.yaml            # RayCluster YAML 配置
```

## 前置准备

### 资源准备

- 需要至少一个 TKE 标准集群作为虚拟算力集群，以及一个 TKE 标准集群作为工作集群。
- 工作集群中至少包含4个 GPU 节点，每个节点至少带有2个 GPU，型号为 A10 或更新的代次型号，CPU 至少为 24核，内存100G。若不满足此要求，可手动调整本目录下 YAML 中的资源要求。
- 需要为算力集群和工作集群开启内网访问，需要耗费一定成本部署内网负载均衡。

### 环境准备

- 算力集群和业务集群 Pod 网络直接互通、节点网络互通
- 可以使用 `kubectl` 并通过内网访问集群
- 本地环境已安装 `helm`，且版本为 v3
- 安装并配置 [腾讯云 CLI 工具 (tccli)](https://cloud.tencent.com/document/product/440/6176)
- 安装 [jq](https://jqlang.org/download/) JSON 处理工具
- 准备 TKE 集群所在的地域、集群 ID 和子网 ID
- 其他要求参考：[要求](../../../docs/requirements_zh.md)

## 一键部署（推荐）

### 1. 创建配置文件

```bash
cat > config.env <<EOF
# Manager 集群配置
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-xxx1"
MANAGER_SUBNET_ID="subnet-xxx1"

# Worker 集群配置
WORKER_REGION="ap-guangzhou"
WORKER_CLUSTER_ID="cls-xxx2"
WORKER_SUBNET_ID="subnet-xxx2"
WORKER_KUBECONFIG="/tmp/kubeconfig-worker"
WORKER_CLUSTER_NAME="example-cluster"
EOF
```

### 2. 运行一键部署脚本

```bash
./run-demo.sh
```

或者使用自定义配置文件：

```bash
./run-demo.sh -c /path/to/my-config.env
```

### 部署流程

`run-demo.sh` 脚本会自动执行以下步骤：

1. **前置检查**：检查依赖工具是否安装完成
2. **加载配置**：从配置文件中加载环境变量
3. **安装 Kubeocean**：调用 `../installation-tke/install-tke.sh` 安装 Kubeocean 并绑定 worker 集群
4. **部署推理服务**：调用 `vllm-infer-demo.sh` 在 worker 集群部署 vLLM 推理服务
5. **部署训练任务**：调用 `llm-kuberay-verl-demo.sh` 在 manager 集群部署 VERL 训练任务

## 分步部署

如果需要分步执行，可以单独运行各个脚本：

### 1. 部署 vLLM 推理服务

```bash
./vllm-infer-demo.sh -c config.env
```

此脚本会：
- 检查 worker 集群并配置访问
- 安装 tke-hpc-controller 插件（如果需要）
- 部署 vLLM 推理服务和 HorizontalPodCronscaler

### 2. 部署 VERL 训练任务

```bash
./llm-kuberay-verl-demo.sh -c config.env
```

此脚本会：
- 检查 manager 集群并配置访问
- 部署 KubeRay Operator
- 创建 RayCluster
- 提交 VERL 训练任务

## 单独部署资源

如果只需要部署 Kubernetes 资源，而不需要脚本的自动化配置，可以直接使用 kubectl apply 部署 YAML 文件。

### 1. 单独部署 vLLM 推理服务

```bash
# 切换到 worker 集群
kubectl config use-context worker-admin-$WORKER_CLUSTER_ID

# 部署 vLLM 推理服务
kubectl apply -f is-qwen2-5-05b-vllm.yaml -n default

# 查看部署状态
kubectl get deployment,service,horizontalpodcronscaler -n default | grep qwen
```

**注意**：使用此方式需要确保：
- Worker 集群已经配置好 kubeconfig 并可以访问
- 集群中已有足够的 GPU 资源
- 如需 HorizontalPodCronscaler 功能，需要提前安装 tke-hpc-controller

### 2. 单独部署 VERL 训练任务

```bash
# 切换到 manager 集群
kubectl config use-context manager-admin-$MANAGER_CLUSTER_ID

# 部署 RayCluster
kubectl apply -f verl-raycluster.yaml -n default

# 查看部署状态
kubectl get raycluster,pods -n default | grep verl
```

**注意**：使用此方式需要确保：
- Manager 集群已经配置好 kubeconfig 并可以访问
- 集群中已安装 KubeRay Operator（可使用 `llm-kuberay-verl-demo.sh` 的前几步安装）
- 集群中已有足够的 GPU 资源
- 如需提交训练任务，需要在 Ray head pod 中手动执行 `ray job submit` 命令

## 验证部署

部署完成后，可以使用以下命令验证：

### 检查 Kubeocean 组件

```bash
# 切换到 manager 集群
kubectl config use-context manager-admin-$MANAGER_CLUSTER_ID

# 查看 kubeocean-system 命名空间的资源
kubectl get all -n kubeocean-system

# 查看集群绑定
kubectl get clusterbindings
```

### 检查 vLLM 推理服务

```bash
# 切换到 worker 集群
kubectl config use-context worker-admin-$WORKER_CLUSTER_ID

# 查看 deployment
kubectl get deployment is-qwen2-5-05b-vllm -n default

# 查看 pods
kubectl get pods -l app.kubernetes.io/instance=is-qwen2-5-05b-vllm -n default

# 查看 service
kubectl get service is-qwen2-5-05b-vllm -n default

# 查看 HorizontalPodCronscaler
kubectl get horizontalpodcronscaler is-qwen -n default

# 使用 port-forward 暴露服务到本地（在新终端窗口中运行，或者在后台运行）
kubectl port-forward svc/is-qwen2-5-05b-vllm 8000:8000 -n default

# 在本地测试推理服务（在另一个终端窗口中执行）
curl --location http://localhost:8000/v1/chat/completions \
  --header 'Content-Type: application/json' \
  --data '{
    "model": "Qwen2.5-0.5B-Instruct",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Provide steps to serve an LLM using vllm."}
    ]
  }'
```

### 检查 VERL 训练任务

```bash
# 切换到 manager 集群
kubectl config use-context manager-admin-$MANAGER_CLUSTER_ID

# 查看 RayCluster
kubectl get raycluster verl-cluster -n default

# 查看 Ray pods
kubectl get pods -l ray.io/cluster=verl-cluster -n default

# 查看训练任务状态
HEAD_POD=$(kubectl get pods -n default -l ray.io/cluster=verl-cluster,ray.io/node-type=head -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n default $HEAD_POD -- ray job list

# 查看 HEAD POD 日志
kubectl logs -n default $HEAD_POD -f
```

## 清理资源

### 一键清理（推荐）

使用 `clean-demo.sh` 脚本可以自动清理所有部署的资源：

```bash
# 使用默认配置文件
./clean-demo.sh

# 使用自定义配置文件
./clean-demo.sh -c /path/to/my-config.env

# 同时卸载 HPC controller 和 KubeRay operator
SKIP_UNINSTALL_HPC=false SKIP_UNINSTALL_KUBERAY=false ./clean-demo.sh
```

`clean-demo.sh` 会自动执行以下步骤：

1. **前置检查**：检查依赖工具和 kubectl contexts
2. **删除训练资源**：删除 RayCluster，等待 pods 清理完成（40s）
3. **清理 KubeRay**：可选删除 KubeRay operator（需设置 `SKIP_UNINSTALL_KUBERAY=false`）
4. **删除推理服务**：删除 vLLM 服务，等待 pods 清理完成（40s）
5. **清理 HPC**：可选删除 HPC controller（需设置 `SKIP_UNINSTALL_HPC=false`）
6. **卸载 Kubeocean**：调用 `uninstall-tke.sh` 完成集群解绑和清理

**注意事项：**

- 默认情况下，HPC controller 和 KubeRay operator **不会被卸载**（`SKIP_UNINSTALL_HPC=true`, `SKIP_UNINSTALL_KUBERAY=true`）
- 如果这些组件还在被其他应用使用，请保持默认设置
- 脚本会自动等待 pods 删除完成，最多等待 40 秒

### 手动清理

如果需要手动清理资源，可以按以下顺序操作：

```bash
# 1. 删除 RayCluster 和训练任务
kubectl delete -f verl-raycluster.yaml -n default --context manager-admin-$MANAGER_CLUSTER_ID

# 2. （可选）删除 KubeRay Operator
helm uninstall kuberay-operator -n default --kube-context manager-admin-$MANAGER_CLUSTER_ID

# 3. 删除 vLLM 推理服务
kubectl delete -f is-qwen2-5-05b-vllm.yaml -n default --context worker-admin-$WORKER_CLUSTER_ID

# 4. （可选）删除 HPC controller
# 需要使用 tccli 命令删除 addon

# 5. 卸载 Kubeocean
cd ../installation-tke
./uninstall-tke.sh
```

## 配置说明

### 必填配置项

| 配置项 | 说明 |
|-------|------|
| `MANAGER_REGION` | Manager 集群所在地域 |
| `MANAGER_CLUSTER_ID` | Manager 集群 ID |
| `MANAGER_SUBNET_ID` | Manager 集群子网 ID（用于内网访问）|
| `WORKER_REGION` | Worker 集群所在地域 |
| `WORKER_CLUSTER_ID` | Worker 集群 ID |
| `WORKER_SUBNET_ID` | Worker 集群子网 ID（用于内网访问）|

### 可选配置项

| 配置项 | 默认值 | 说明 |
|-------|--------|------|
| `WORKER_KUBECONFIG` | `/tmp/kubeconfig-worker` | Worker 集群 kubeconfig 路径 |
| `WORKER_CLUSTER_NAME` | `example-cluster` | Worker 集群名称（用于绑定）|
| `SKIP_MANAGER_UNINSTALL` | `false` | 是否跳过 manager 卸载 |
| `SKIP_INSTALL_HPC` | `false` | 是否跳过 tke-hpc-controller 安装 |
| `SKIP_CURRENT_TIME_CHECKING` | `false` | 是否跳过时间窗口检查。为 `false` 时，训练任务只能在 18:00-08:00 提交；为 `true` 时跳过时间检查并清空 worker 集群 RLP 的 timeWindows |
| `SKIP_UNINSTALL_HPC` | `true` | 清理时是否跳过 HPC controller 卸载 |
| `SKIP_UNINSTALL_KUBERAY` | `true` | 清理时是否跳过 KubeRay operator 卸载 |

## 故障排除

### 1. 依赖工具未安装

如果提示缺少依赖工具，请安装：

```bash
# 安装 tccli
pip install tccli

# 配置 tccli
tccli configure

# 安装 jq（Ubuntu/Debian）
sudo apt-get install jq

# 安装 kubectl
# 参考：https://kubernetes.io/docs/tasks/tools/

# 安装 helm
# 参考：https://helm.sh/docs/intro/install/
```

### 2. 集群连接失败

确保：
- tccli 已正确配置凭证
- 集群 ID 和地域正确
- 子网 ID 正确（用于内网访问）
- 当前机器能够访问腾讯云 VPC 内网

### 3. ResourceLeasingPolicy 不存在

如果提示 RLP 不存在，可能是因为 Kubeocean 安装未完成。请检查：
- 安装脚本是否成功执行
- Worker 集群是否正确绑定

### 4. 时间窗口检查失败

如果训练任务提交时提示"当前时间不在允许的时间窗口内"：

**原因**：为保护在线推理业务，训练任务默认只能在 18:00-08:00 之间提交。

**解决方案**：
1. 等待到 18:00 后再提交训练任务（推荐用于生产环境）
2. 或在 `config.env` 中设置 `SKIP_CURRENT_TIME_CHECKING="true"` 跳过时间检查（适用于测试环境）

**注意**：设置 `SKIP_CURRENT_TIME_CHECKING="true"` 后，脚本会自动清空 worker 集群中 RLP 的 timeWindows 限制。

## 注意事项

1. **网络访问**：脚本需要访问腾讯云 API 和集群内网，请确保网络连通性
2. **资源需求**：训练和推理服务需要 GPU 资源，请确保集群有足够的 GPU 节点
3. **时间消耗**：完整部署过程可能需要 10-20 分钟，请耐心等待
4. **成本考虑**：部署会创建负载均衡器等云资源，请注意成本

## 参考文档

- [Kubeocean 主文档](../../../README.md)
- [安装指南](../installation-tke/README.md)
- [vLLM 文档](https://docs.vllm.ai/)
- [KubeRay 文档](https://docs.ray.io/en/latest/cluster/kubernetes/index.html)
- [VERL 文档](https://github.com/volcengine/verl)

