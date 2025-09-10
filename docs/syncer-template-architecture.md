# Kubeocean Syncer 模板架构

## 概述

Kubeocean Syncer 的部署已重构为使用基于模板的方法，并采用共享的 RBAC 资源。这种设计提供了更好的灵活性、可维护性和资源效率。

## 架构组件

### 1. 共享 RBAC 资源

以下 RBAC 资源在所有 Syncer 实例之间共享，并与 manager 一起部署：

- **ServiceAccount**: `kubeocean-syncer`，位于 `kubeocean-system` 命名空间
- **Role**: `kubeocean-syncer`，具有 nodes、pods、services 等权限
- **RoleBinding**: 将 ServiceAccount 链接到 Role

这些资源在 `config/syncer/rbac.yaml` 中定义，与 manager 一起部署一次。

### 2. ConfigMap 模板

`kubeocean-syncer-template` ConfigMap 包含：

- **配置信息**: 共享 RBAC 资源的名称
- **Deployment 模板**: 用于创建 Syncer Deployment 的 YAML 模板

ConfigMap 与 manager 一起部署，并由 ClusterBinding Controller 引用。

### 3. 动态 Deployment 创建

对于每个 ClusterBinding，控制器会：

1. 加载 ConfigMap 模板
2. 使用 ClusterBinding 特定数据渲染 Deployment 模板
3. 为 Syncer 创建唯一的 Deployment
4. 使用共享的 RBAC 资源

## 优势

### 资源效率
- **共享 RBAC**: 多个 Syncer 共享相同的 ServiceAccount、Role 和 RoleBinding
- **减少开销**: 无需为每个 ClusterBinding 创建 RBAC 资源

### 灵活性
- **可配置模板**: 通过 ConfigMap 轻松修改 Syncer 配置
- **模板参数**: 支持通过模板变量进行自定义

### 可维护性
- **集中配置**: 所有 Syncer 配置集中在一处
- **版本控制**: 模板更改与 manager 一起跟踪

## 配置

### 文件挂载配置

ConfigMap 直接挂载到 Manager 容器中，无需额外参数：

- **挂载路径**: `/etc/kubeocean/syncer-template`
- **ConfigMap 名称**: `kubeocean-syncer-template`
- **命名空间**: `kubeocean-system`

### ConfigMap 结构

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kubeocean-syncer-template
  namespace: kubeocean-system
data:
  serviceAccountName: "kubeocean-syncer"
  roleName: "kubeocean-syncer"
  roleBindingName: "kubeocean-syncer"
  syncerNamespace: "kubeocean-system"
  
  deployment.yaml: |
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: "{{.DeploymentName}}"
      namespace: "{{.Namespace}}"
    spec:
      replicas: 2
      # ... 模板内容
```

### 模板变量

模板中可用的变量：

- `{{.ClusterBindingName}}`: ClusterBinding 的名称
- `{{.Namespace}}`: Syncer 将部署到的命名空间
- `{{.DeploymentName}}`: 为 Syncer Deployment 生成的名称
- `{{.ServiceAccountName}}`: 共享 ServiceAccount 的名称
- `{{.RoleName}}`: 共享 Role 的名称
- `{{.RoleBindingName}}`: 共享 RoleBinding 的名称
- `{{.SyncerNamespace}}`: 共享 RBAC 资源所在的命名空间

注意：`replicas` 和 `image` 直接在模板中配置，不使用变量。

## 部署流程

1. **Manager 启动**: 部署共享 RBAC 资源和 ConfigMap 模板，ConfigMap 挂载到 Manager 容器
2. **ClusterBinding 创建**: 控制器从挂载的文件读取模板并创建 Syncer Deployment
3. **Syncer 启动**: 使用共享 ServiceAccount 进行身份验证
4. **ClusterBinding 删除**: 控制器仅删除 Syncer Deployment

## 从旧实现的迁移

新实现向后兼容：

- 现有的 ClusterBinding 无需更改即可工作
- 旧的 RBAC 资源将在 ClusterBinding 删除时清理
- 新的 ClusterBinding 将使用基于模板的方法

## 故障排除

### 常见问题

1. **ConfigMap 未找到**: 确保 manager 部署时包含了 syncer 配置
2. **模板解析错误**: 检查 ConfigMap 模板中的 YAML 语法
3. **RBAC 问题**: 验证共享 RBAC 资源是否正确部署

### 调试

启用调试日志以查看模板渲染：

```bash
--log-level=debug
```

检查 ConfigMap 内容：

```bash
kubectl get configmap kubeocean-syncer-template -n kubeocean-system -o yaml
```

## 未来增强

- **多命名空间支持**: 支持在不同命名空间中的 Syncer
- **自定义镜像**: 每个 ClusterBinding 的镜像配置
- **资源限制**: 每个 Syncer 的可配置资源限制
- **高级模板**: 支持更复杂的模板逻辑