# Kubeocean Worker

Kubeocean Worker 是一个 Helm chart，用于在物理 Kubernetes 集群上部署 Kubeocean Syncer 所需的 RBAC 资源。

## 概述

Kubeocean Worker chart 包含以下资源：

- **Namespace**: `kubeocean-system` 命名空间
- **ServiceAccount**: `kubeocean-syncer` 服务账户
- **ClusterRole**: 定义 Syncer 所需的集群级权限
- **ClusterRoleBinding**: 将 ClusterRole 绑定到 ServiceAccount

## 权限说明

Kubeocean Syncer 需要以下权限来管理物理集群资源：

### 核心资源权限
- **Nodes**: 创建、更新、删除虚拟节点，管理节点状态
- **Pods**: 创建、更新、删除物理 Pod，管理 Pod 状态
- **ServiceAccounts**: 管理服务账户
- **ConfigMaps**: 管理配置映射
- **Secrets**: 管理密钥
- **PersistentVolumeClaims**: 管理持久卷声明
- **PersistentVolumes**: 管理持久卷
- **Events**: 创建和更新事件

### 应用资源权限
- **Deployments**: 管理部署
- **StatefulSets**: 管理有状态集

### 存储资源权限
- **CSINodes**: 管理 CSI 节点

### 协调资源权限
- **Leases**: 管理租约（用于 Leader Election）

### 自定义资源权限
- **ClusterBindings**: 管理集群绑定
- **ResourceLeasingPolicies**: 管理资源租赁策略

## 安装

### 基本安装

```bash
# 安装到默认命名空间
helm install kubeocean-worker ./charts/kubeocean-worker

# 安装到指定命名空间
helm install kubeocean-worker ./charts/kubeocean-worker --namespace kubeocean-system --create-namespace
```

### 自定义配置

```bash
# 使用自定义 values 文件
helm install kubeocean-worker ./charts/kubeocean-worker -f custom-values.yaml

# 覆盖特定值
helm install kubeocean-worker ./charts/kubeocean-worker \
  --set namespace.name=custom-namespace \
  --set serviceAccount.name=custom-syncer
```

## 配置

### 命名空间配置

```yaml
namespace:
  create: true
  name: kubeocean-system
  labels:
    app.kubernetes.io/component: worker
    kubeocean.io/component: worker
```

### ServiceAccount 配置

```yaml
serviceAccount:
  create: true
  name: kubeocean-syncer
  annotations: {}
  labels:
    app.kubernetes.io/component: syncer
    kubeocean.io/component: syncer
```

### RBAC 配置

```yaml
rbac:
  create: true
  clusterRoleName: kubeocean-syncer
  clusterRoleBindingName: kubeocean-syncer
  clusterRoleLabels:
    app.kubernetes.io/component: syncer
    kubeocean.io/component: syncer
```

### 权限配置

```yaml
permissions:
  core:
    nodes: [get, list, watch, create, update, patch, delete]
    pods: [get, list, watch, create, update, patch, delete]
    # ... 其他核心资源权限
  apps:
    deployments: [get, list, watch, create, update, patch, delete]
    statefulSets: [get, list, watch, create, update, patch, delete]
  # ... 其他资源类型权限
```

## 功能开关

```yaml
features:
  namespace: true        # 创建命名空间
  serviceAccount: true   # 创建服务账户
  rbac: true            # 创建 RBAC 资源
  customResources: true # 启用自定义资源权限
```

## 升级

```bash
# 升级到新版本
helm upgrade kubeocean-worker ./charts/kubeocean-worker

# 升级并应用新配置
helm upgrade kubeocean-worker ./charts/kubeocean-worker -f new-values.yaml
```

## 卸载

```bash
# 卸载 chart
helm uninstall kubeocean-worker

# 卸载并删除命名空间
helm uninstall kubeocean-worker
kubectl delete namespace kubeocean-system
```

## 验证

### 检查资源创建

```bash
# 检查命名空间
kubectl get namespace kubeocean-system

# 检查 ServiceAccount
kubectl get serviceaccount kubeocean-syncer -n kubeocean-system

# 检查 ClusterRole
kubectl get clusterrole kubeocean-syncer

# 检查 ClusterRoleBinding
kubectl get clusterrolebinding kubeocean-syncer
```

### 检查权限

```bash
# 查看 ClusterRole 详情
kubectl describe clusterrole kubeocean-syncer

# 查看 ClusterRoleBinding 详情
kubectl describe clusterrolebinding kubeocean-syncer
```

## 故障排除

### 常见问题

1. **权限不足**: 确保安装用户有创建 ClusterRole 和 ClusterRoleBinding 的权限
2. **命名空间冲突**: 如果 `kubeocean-system` 命名空间已存在，设置 `namespace.create: false`
3. **资源名称冲突**: 如果资源名称已存在，修改 `serviceAccount.name` 或 `rbac.clusterRoleName`

### 日志查看

```bash
# 查看 Helm 安装日志
helm install kubeocean-worker ./charts/kubeocean-worker --debug --dry-run

# 查看资源状态
kubectl get all -n kubeocean-system
```

## 安全注意事项

1. **最小权限原则**: 此 chart 提供了 Syncer 运行所需的最小权限集
2. **命名空间隔离**: 所有资源都部署在 `kubeocean-system` 命名空间中
3. **标签管理**: 所有资源都带有适当的标签用于资源管理
4. **审计**: 建议启用 Kubernetes 审计日志来监控 Syncer 的活动

## 版本历史

- **0.1.0**: 初始版本，包含基本的 RBAC 资源
