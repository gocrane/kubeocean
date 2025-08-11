# ClusterBinding Controller 实现

## 概述

ClusterBinding Controller 是 Tapestry Manager 的核心组件之一，负责管理物理 Kubernetes 集群的绑定和连接验证。本文档描述了任务列表第4步的完整实现。

## 功能特性

### 1. ClusterBinding 资源管理
- 监听 ClusterBinding 资源的 CRUD 操作
- 自动添加和管理 finalizer 确保资源正确清理
- 支持状态更新和条件管理

### 2. Kubeconfig Secret 验证
- 读取和验证 kubeconfig secret 的存在性和格式
- 检查 secret 中是否包含有效的 kubeconfig 数据
- 验证 kubeconfig 格式的正确性

### 3. 集群连接性检查
- 测试到目标集群的网络连接
- 验证 kubeconfig 中的认证信息
- 检查基本的集群访问权限

### 4. 错误处理和重试机制
- 指数退避重试策略
- 最大重试次数限制（5次）
- 重试间隔从30秒开始，最大不超过5分钟
- 成功后自动重置重试计数

### 5. 状态管理
- 支持 Pending、Ready、Failed 三种状态
- 详细的条件信息（Ready、Connected）
- 记录最后同步时间

## 实现细节

### 核心方法

#### `Reconcile()`
主要的协调方法，处理 ClusterBinding 资源的完整生命周期：
1. 检查资源是否被删除，如果是则执行清理逻辑
2. 添加 finalizer（如果不存在）
3. 初始化状态（如果需要）
4. 验证配置
5. 验证 kubeconfig 和测试连接性
6. 更新状态为 Ready

#### `validateClusterBinding()`
验证 ClusterBinding 配置的基本字段：
- `secretRef.name` 和 `secretRef.namespace` 必填
- `mountNamespace` 必填

#### `validateKubeconfigAndConnectivity()`
完整的连接性验证流程：
1. 读取 kubeconfig secret
2. 验证 kubeconfig 格式
3. 测试集群连接性

#### `readKubeconfigSecret()`
从 Kubernetes Secret 中读取 kubeconfig 数据：
- 检查 Secret 是否存在
- 验证 `kubeconfig` 键是否存在
- 确保数据不为空

#### `testClusterConnectivity()`
测试与目标集群的连接：
- 设置30秒超时
- 获取集群版本信息
- 尝试列出节点（权限允许的情况下）

### 重试机制

#### 指数退避算法
```go
interval = baseRetryInterval * 2^retryCount
```
- `baseRetryInterval`: 30秒
- `maxRetryInterval`: 5分钟
- `maxRetries`: 5次

#### 重试状态管理
- 使用 annotation 存储重试次数
- 成功时重置重试计数
- 达到最大重试次数时标记为永久失败

### Finalizer 处理

使用 `clusterbinding.cloud.tencent.com/finalizer` 确保资源正确清理：
- 创建时自动添加 finalizer
- 删除时执行清理逻辑后移除 finalizer
- 为后续任务预留清理 Syncer 实例的入口点

## RBAC 权限

Controller 需要以下权限：

```yaml
# ClusterBinding 资源权限
- apiGroups: ["cloud.tencent.com"]
  resources: ["clusterbindings"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

- apiGroups: ["cloud.tencent.com"]
  resources: ["clusterbindings/status"]
  verbs: ["get", "update", "patch"]

- apiGroups: ["cloud.tencent.com"]
  resources: ["clusterbindings/finalizers"]
  verbs: ["update"]

# Secret 读取权限
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "watch"]

# 事件记录权限
- apiGroups: [""]
  resources: ["events"]
  verbs: ["create", "patch"]

# 后续任务需要的权限（Syncer 管理）
- apiGroups: ["apps"]
  resources: ["deployments", "statefulsets"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

## 使用示例

### 创建 ClusterBinding

1. 首先创建包含 kubeconfig 的 Secret：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-cluster-kubeconfig
  namespace: tapestry-system
type: Opaque
data:
  kubeconfig: <base64-encoded-kubeconfig>
```

2. 创建 ClusterBinding 资源：

```yaml
apiVersion: cloud.tencent.com/v1beta1
kind: ClusterBinding
metadata:
  name: my-cluster-binding
  namespace: tapestry-system
spec:
  secretRef:
    name: my-cluster-kubeconfig
    namespace: tapestry-system
  mountNamespace: "my-virtual-cluster"
  nodeSelector:
    node-type: worker
  serviceNamespaces:
    - "default"
    - "kube-system"
```

### 查看状态

```bash
kubectl get clusterbinding my-cluster-binding -o yaml
```

状态示例：
```yaml
status:
  phase: Ready
  lastSyncTime: "2024-01-01T10:00:00Z"
  conditions:
  - type: Ready
    status: "True"
    reason: ValidationPassed
    message: "ClusterBinding validation and connectivity check passed"
    lastTransitionTime: "2024-01-01T10:00:00Z"
  - type: Connected
    status: "True"
    reason: ConnectivityPassed
    message: "Successfully connected to target cluster"
    lastTransitionTime: "2024-01-01T10:00:00Z"
```

## 监控和可观测性

### 日志
- 结构化日志记录所有重要操作
- 错误信息包含详细的上下文
- 支持不同日志级别

### 事件
- 记录所有状态变化的 Kubernetes 事件
- 包含验证失败、连接失败等错误信息
- 成功状态变更的确认事件

### 指标
- 集成 Prometheus 指标
- 记录不同状态的 ClusterBinding 数量
- 为后续添加连接延迟、重试次数等指标预留接口

## 测试

项目包含完整的单元测试覆盖：
- 配置验证测试
- Secret 读取测试
- Finalizer 操作测试
- 重试机制测试
- 集成测试

运行测试：
```bash
go test ./pkg/controller/ -v
```

## 后续任务集成

当前实现为后续任务提供了以下集成点：

1. **Syncer 管理**：`handleDeletion()` 方法中预留了清理 Syncer 实例的逻辑入口
2. **状态同步**：Ready 状态的 ClusterBinding 可以被后续任务用来创建 Syncer
3. **配置传递**：验证过的 kubeconfig 和配置信息可以传递给 Syncer 实例

## 故障排查

### 常见问题

1. **Secret 不存在**
   - 错误：`kubeconfig secret namespace/name not found`
   - 解决：确保 Secret 存在且 secretRef 配置正确

2. **kubeconfig 格式错误**
   - 错误：`invalid kubeconfig format`
   - 解决：验证 kubeconfig 文件格式和 base64 编码

3. **连接失败**
   - 错误：`cluster connectivity test failed`
   - 解决：检查网络连接、集群地址和认证信息

4. **权限不足**
   - 错误：权限相关错误
   - 解决：确保 kubeconfig 中的用户有足够权限

### 调试命令

```bash
# 查看 ClusterBinding 状态
kubectl describe clusterbinding <name>

# 查看相关事件
kubectl get events --field-selector involvedObject.name=<clusterbinding-name>

# 查看控制器日志
kubectl logs -n tapestry-system deployment/tapestry-manager
```
