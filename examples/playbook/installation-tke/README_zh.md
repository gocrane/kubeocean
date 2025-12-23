# TKE 集群上 Kubeocean 一键部署脚本

> [English](README.md) | 中文

本目录提供了 Kubeocean 组件和集群绑定的一键部署脚本，支持在腾讯云 TKE 集群上一站式体验 Kubeocean 相关功能。

## 功能

- 完成集群环境配置：开启 APIServer 内网访问，创建 kube-dns-intranet
- 在算力集群和工作集群部署 Kubeocean 组件
- 绑定工作集群到算力集群，并配置简单资源抽取策略

## 前置准备

- 需要至少一个 TKE 标准集群作为虚拟算力集群，以及一个 TKE 标准集群作为工作集群
- 算力集群和业务集群 Pod 网络直接互通、节点网络互通
- 可以使用 `kubectl` 并通过内网访问集群
- 本地环境已安装 `helm`，且版本为 v3
- 安装并配置 [腾讯云 CLI 工具 (tccli)](https://cloud.tencent.com/document/product/440/6176)
- 安装 [jq](https://jqlang.org/download/) JSON 处理工具
- 准备 TKE 集群所在的地域、集群 ID 和子网 ID
- 其他要求参考：[要求](../../../docs/requirements_zh.md)

## 基础使用

### 一键安装（推荐）

#### 1. 创建配置文件 (config.env)

```bash
cat > config.env <<EOF
# Manager Cluster Settings
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-xxx1"
MANAGER_SUBNET_ID="subnet-xxx1"

# Worker Cluster Settings
WORKER_REGION="ap-guangzhou"
WORKER_CLUSTER_ID="cls-xxx2"
WORKER_SUBNET_ID="subnet-xxx2"
WORKER_KUBECONFIG="/tmp/kubeconfig-worker"
WORKER_CLUSTER_NAME="example-cluster"
EOF
```

#### 2. 一键部署

```bash
./install-tke.sh
```

脚本会自动按顺序完成以下操作：
1. **Phase 1/2**: 部署 Worker 集群
   - 开启集群内网访问
   - 安装 kubeocean-worker 组件
   - 自动给所有节点添加 `kubeocean.io/role=worker` 标签
   - 生成 ResourceLeasingPolicy 资源抽取策略
2. **Phase 2/2**: 部署 Manager 集群并创建绑定
   - 开启集群内网访问
   - 创建 kube-dns-intranet 服务
   - 安装 kubeocean-manager 组件
   - 创建 ClusterBinding 绑定 worker 集群

#### 3. 验证部署

```bash
# 验证资源
kubectl get nodes
kubectl get clusterbindings
kubectl get resourceleasingpolicies
```

### 分集群安装和绑定集群

如果需要分别控制 Worker 和 Manager 集群的安装过程，可以使用独立的安装脚本。

#### 1. 创建配置文件 (config.env)

```bash
cat > config.env <<EOF
# Manager Cluster Settings
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-xxx1"
MANAGER_SUBNET_ID="subnet-xxx1"

# Worker Cluster Settings
WORKER_REGION="ap-guangzhou"
WORKER_CLUSTER_ID="cls-xxx2"
WORKER_SUBNET_ID="subnet-xxx2"
WORKER_KUBECONFIG="/tmp/kubeconfig-worker"
WORKER_CLUSTER_NAME="example-cluster"
EOF
```

#### 2. 部署 Worker 集群

```bash
./install-worker-tke.sh
```

脚本会自动完成以下操作：
- 检查前置条件
- 开启集群内网访问
- 获取集群 kubeconfig
- 安装 kubeocean-worker 组件
- **自动给所有节点添加 `kubeocean.io/role=worker` 标签**

#### 3. 部署 Manager 集群

```bash
./install-manager-tke.sh
```

#### 4. 验证部署

```bash
# 验证资源
kubectl get nodes -l kubeocean.io/role=worker
kubectl get clusterbindings
kubectl get resourceleasingpolicies
```

### 卸载集群

#### 一键卸载（推荐）

如果使用 `install-tke.sh` 进行了一键安装，可以使用 `uninstall-tke.sh` 进行一键卸载：

```bash
./uninstall-tke.sh
```

脚本会自动按正确的顺序完成以下操作：
1. **Phase 1/2**: 卸载 Manager 集群
   - 获取 Manager 集群 kubeconfig（或复用已有 context）
   - 删除 ClusterBinding 资源
   - 删除 worker kubeconfig secrets
   - 卸载 kubeocean-manager 组件
   - 清理 kubectl config 中的 manager context
2. **Phase 2/2**: 卸载 Worker 集群
   - 获取 Worker 集群 kubeconfig（或复用已有 context）
   - 删除 ResourceLeasingPolicy 资源
   - 卸载 kubeocean-worker 组件
   - 清理 kubectl config 中的 worker context

#### 分集群卸载

如果需要单独卸载某个集群，可以使用独立的卸载脚本。

##### 1. 卸载 Manager 集群

```bash
./uninstall-manager-tke.sh
```

脚本会自动完成以下操作：
- 获取 Manager 集群 kubeconfig（或复用已有 context）
- 删除 ClusterBinding 资源
- 删除 worker kubeconfig secrets
- 卸载 kubeocean-manager 组件
- 清理 kubectl config 中的 manager context

##### 2. 卸载 Worker 集群

```bash
./uninstall-worker-tke.sh
```

脚本会自动完成以下操作：
- 获取 Worker 集群 kubeconfig（或复用已有 context）
- 删除 ResourceLeasingPolicy 资源
- 卸载 kubeocean-worker 组件
- 清理 kubectl config 中的 worker context

**注意：** 分集群卸载时，卸载顺序应该先卸载 Manager，再卸载 Worker。

## 进阶使用

### 使用自定义配置文件

可以为不同环境创建不同的配置文件：

```bash
# 创建生产环境配置
cat > prod.env <<EOF
# Manager Cluster Settings
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-prod-manager"
MANAGER_SUBNET_ID="subnet-prod-manager"

# Worker Cluster Settings
WORKER_REGION="ap-guangzhou"
WORKER_CLUSTER_ID="cls-prod-worker"
WORKER_SUBNET_ID="subnet-prod-worker"
WORKER_KUBECONFIG="/data/kubeconfig/prod-worker"
WORKER_CLUSTER_NAME="prod-cluster"
EOF

# 使用自定义配置文件（一键安装）
./install-tke.sh -c prod.env

# 或者分别安装
./install-worker-tke.sh -c prod.env
./install-manager-tke.sh -c prod.env
```

### 只解绑集群（不卸载 Manager 组件）

适用于需要保留 Manager 组件，只解绑某个 Worker 集群的场景：

```bash
# 在配置文件中设置
cat > config.env <<EOF
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-manager-xxx"
SKIP_MANAGER_UNINSTALL="true"
EOF

./uninstall-manager-tke.sh

# 或使用环境变量覆盖
SKIP_MANAGER_UNINSTALL="true" ./uninstall-manager-tke.sh
```

### 环境变量覆盖

可以使用环境变量覆盖配置文件中的值：

```bash
# 一键安装时覆盖配置
WORKER_CLUSTER_NAME="prod-cluster" ./install-tke.sh -c prod.env

# 单独安装时覆盖 Worker kubeconfig 输出路径
WORKER_KUBECONFIG="/data/kubeconfig/prod-worker" ./install-worker-tke.sh -c prod.env

# 单独安装时覆盖 Worker 集群名称
WORKER_CLUSTER_NAME="prod-cluster" ./install-worker-tke.sh

# 覆盖多个配置
WORKER_REGION="ap-shanghai" WORKER_KUBECONFIG="/data/kubeconfig" ./install-worker-tke.sh -c prod.env
```

## 配置说明

### 必需配置

#### 一键安装 (install-tke.sh) 和完整部署

| 配置项 | 说明 | 示例值 |
|--------|------|--------|
| `MANAGER_REGION` | Manager 集群所在地域 | `ap-guangzhou` |
| `MANAGER_CLUSTER_ID` | Manager 集群 ID | `cls-manager-xxx` |
| `MANAGER_SUBNET_ID` | Manager 集群子网 ID | `subnet-manager-xxx` |
| `WORKER_REGION` | Worker 集群所在地域 | `ap-guangzhou` |
| `WORKER_CLUSTER_ID` | Worker 集群 ID | `cls-worker-xxx` |
| `WORKER_SUBNET_ID` | Worker 集群子网 ID | `subnet-worker-xxx` |

#### Worker 集群单独安装 (install-worker-tke.sh)

| 配置项 | 说明 | 示例值 |
|--------|------|--------|
| `WORKER_REGION` | Worker 集群所在地域 | `ap-guangzhou` |
| `WORKER_CLUSTER_ID` | Worker 集群 ID | `cls-worker-xxx` |
| `WORKER_SUBNET_ID` | Worker 集群子网 ID | `subnet-worker-xxx` |

#### Manager 集群单独安装 (install-manager-tke.sh)

| 配置项 | 说明 | 示例值 |
|--------|------|--------|
| `MANAGER_REGION` | Manager 集群所在地域 | `ap-guangzhou` |
| `MANAGER_CLUSTER_ID` | Manager 集群 ID | `cls-manager-xxx` |
| `MANAGER_SUBNET_ID` | Manager 集群子网 ID | `subnet-manager-xxx` |
| `WORKER_CLUSTER_ID` | Worker 集群 ID（用于创建绑定） | `cls-worker-xxx` |

### 可选配置

| 配置项 | 说明 | 默认值 | 适用脚本 |
|--------|------|--------|----------|
| `WORKER_KUBECONFIG` | Worker kubeconfig 路径 | `/tmp/kubeconfig-worker` | 一键安装、安装/卸载 Worker 和 Manager |
| `WORKER_CLUSTER_NAME` | Worker 集群名称（用于 ResourceLeasingPolicy） | `example-cluster` | 一键安装、安装 Worker |
| `SKIP_MANAGER_UNINSTALL` | 跳过 Manager 组件卸载（仅删除绑定） | `false` | 卸载 Manager |

## 配置优先级

配置项的优先级从高到低：

1. **环境变量** - 最高优先级
2. **配置文件** - 中等优先级
3. **默认值** - 最低优先级

## 脚本功能说明

### install-tke.sh（一键安装）

脚本会自动按顺序执行以下步骤：
1. 加载配置文件
2. 调用 `install-worker-tke.sh` 安装 Worker 集群组件
3. 调用 `install-manager-tke.sh` 安装 Manager 集群组件并创建集群绑定

**适用场景**：
- 首次部署 Kubeocean
- 快速搭建测试环境
- 自动化部署脚本

### install-worker-tke.sh

脚本会自动完成以下操作：
1. 检查前置条件（tccli、jq、kubectl、helm 等）
2. 开启集群内网访问（如未开启）
3. 获取集群 kubeconfig 并配置 context
4. 安装 kubeocean-worker 组件
5. **自动给所有节点添加 `kubeocean.io/role=worker` 标签**
6. 生成 worker kubeconfig 用于 manager 集群绑定
7. 部署 ResourceLeasingPolicy 资源抽取策略

### install-manager-tke.sh

脚本会自动完成以下操作：
1. 检查前置条件（tccli、jq、kubectl、helm、worker kubeconfig 等）
2. 开启集群内网访问（如未开启）
3. 获取集群 kubeconfig 并配置 context
4. 创建 kube-dns-intranet 服务（如不存在）
5. 安装 kubeocean-manager 组件
6. 创建 ClusterBinding 绑定 worker 集群

### uninstall-worker-tke.sh

脚本会自动完成以下操作：
1. 检查前置条件（tccli、jq、kubectl、helm 等）
2. 获取集群 kubeconfig 并配置 context（或复用已有 context）
3. 删除 ResourceLeasingPolicy 资源
4. 卸载 kubeocean-worker 组件
5. 清理 kubectl config 中的 worker context

### uninstall-manager-tke.sh

脚本会自动完成以下操作：
1. 检查前置条件（tccli、jq、kubectl、helm 等）
2. 获取集群 kubeconfig 并配置 context（或复用已有 context）
3. 删除 ClusterBinding 资源
4. 删除 worker kubeconfig secrets
5. 卸载 kubeocean-manager 组件（除非设置 `SKIP_MANAGER_UNINSTALL=true`）
6. 清理 kubectl config 中的 manager context

### uninstall-tke.sh（一键卸载）

脚本会自动按顺序执行以下步骤：
1. 加载配置文件
2. 调用 `uninstall-manager-tke.sh` 卸载 Manager 集群组件
3. 调用 `uninstall-worker-tke.sh` 卸载 Worker 集群组件
4. 自动清理 kubectl config 中的所有相关 contexts

**适用场景**：
- 完全卸载通过 `install-tke.sh` 部署的 Kubeocean
- 清理测试环境
- 自动化卸载脚本

## 故障排查

### 1. 配置文件未找到

如果默认配置文件 `./config.env` 不存在，脚本会静默跳过。确保：
- 已创建配置文件：`cp config.env.template config.env`
- 或使用 `-c` / `--config` 参数指定配置文件路径

### 2. 参数不支持错误

如果看到以下错误：
```
❌ Unknown argument: -r
ℹ️  Only -h/--help and -c/--config options are supported
ℹ️  All other configurations should be provided in config file
```

**原因**：脚本只支持 `-h/--help` 和 `-c/--config` 参数，其他配置必须在配置文件中提供。

**解决方法**：将配置写入配置文件而不是命令行。

### 3. 查看配置加载状态

脚本运行时会显示：

```bash
ℹ️  Loading configuration from: ./config.env
✅ Configuration loaded
```

如果没有看到这些信息，说明配置文件未被加载。

### 4. 验证配置文件

```bash
# 检查配置文件语法
bash -n config.env

# 手动加载配置查看
source config.env
echo "WORKER_REGION=$WORKER_REGION"
echo "WORKER_CLUSTER_ID=$WORKER_CLUSTER_ID"
```

### 5. tccli 认证失败

确保已正确配置 tccli：

```bash
# 配置 tccli
tccli configure

# 测试连接
tccli tke DescribeClusters --region ap-guangzhou
```

### 6. 集群内网访问开启失败

如果提示子网 ID 错误或内网访问开启失败：
- 确认 `SUBNET_ID` 配置正确
- 确认子网与集群在同一 VPC
- 检查腾讯云账号权限

### 7. 集群连接失败

#### 错误信息 1：`Failed to connect to cluster using the kubeconfig`

**场景**：在获取 kubeconfig 并替换内网地址后，验证集群连接时失败。

**可能原因**：
- 集群内网访问未正确开启
- 内网负载均衡信息尚未完全更新（通常需要等待几秒）
- 运行脚本的机器与集群网络不通
- 集群 API Server 不可达

**解决方法**：

1. **检查集群内网访问状态**
   ```bash
   tccli tke DescribeClusterEndpointStatus \
     --ClusterId <CLUSTER_ID> \
     --IsExtranet false \
     --region <REGION>
   ```
   
   确认返回状态为 `"Status": "Running"`

2. **等待内网负载均衡就绪**
   
   集群内网访问刚开启时，负载均衡可能需要几秒时间初始化。建议：
   - 等待 5-10 秒后重试
   - 脚本已内置等待机制，但某些场景可能需要更长时间

3. **检查网络连通性**
   ```bash
   # 获取内网访问地址
   tccli tke DescribeClusterSecurity \
     --ClusterId <CLUSTER_ID> \
     --region <REGION> | jq -r '.PgwEndpoint'
   
   # 测试连通性（示例地址）
   curl -k https://<PgwEndpoint>:443
   ```

4. **检查子网配置**
   
   确保提供的 `SUBNET_ID` 与运行脚本的机器在同一网络可达范围内。

#### 错误信息 2：`Cannot connect to Kubernetes cluster. Please check kubeconfig configuration`

**场景**：在部署组件时 kubectl 无法连接到集群。

**可能原因**：
- kubectl context 未正确设置
- kubeconfig 文件损坏或不完整
- 集群 API Server 临时不可用
- 网络连接中断

**解决方法**：

1. **检查当前 context**
   ```bash
   kubectl config current-context
   kubectl config get-contexts
   ```

2. **测试集群连接**
   ```bash
   kubectl cluster-info
   kubectl get nodes
   ```

3. **切换到正确的 context**
   ```bash
   # Worker 集群
   kubectl config use-context worker-admin-<CLUSTER_ID>
   
   # Manager 集群
   kubectl config use-context manager-admin-<CLUSTER_ID>
   ```

4. **重新获取 kubeconfig**
   
   如果 kubeconfig 已损坏，可以重新运行安装脚本获取：
   ```bash
   # 重新运行会自动检测并复用或更新 context
   ./install-worker-tke.sh -c config.env
   ```

5. **查看详细错误信息**
   ```bash
   kubectl get nodes -v=8
   ```

## 参考资料

- [Kubeocean 文档](../../../README.md)
- [系统要求](../../../docs/requirements_zh.md)
- [腾讯云 CLI 工具文档](https://cloud.tencent.com/document/product/440/6176)
