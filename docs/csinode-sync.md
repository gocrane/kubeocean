# CSINode 同步功能

## 概述

PhysicalCSINodeReconciler 模块实现了从物理集群 CSINode 到虚拟集群 CSINode 的同步功能。该模块与 PhysicalNodeReconciler 协调工作，确保虚拟集群中的 CSINode 对象与对应的虚拟节点保持生命周期一致。

## 功能特性

### 1. 自动同步
- 监听物理集群中的 CSINode 对象变化
- 自动创建、更新和删除虚拟集群中的对应 CSINode 对象
- 与虚拟节点的生命周期保持一致

### 2. 依赖验证
- 验证虚拟节点是否存在
- 验证虚拟节点标签是否匹配当前物理集群和节点
- 确保 CSINode 同步的准确性

### 3. 标签和注解管理
- 自动添加 Tapestry 管理标签
- 记录同步时间和物理集群信息
- 支持 UID 验证的安全删除

## 架构设计

### 组件关系
```
PhysicalNodeReconciler
    ↓ (引用)
PhysicalCSINodeReconciler
    ↓ (监听)
物理集群 CSINode
    ↓ (同步)
虚拟集群 CSINode
```

### 同步流程

1. **创建流程**
   - PhysicalNodeReconciler 创建虚拟节点
   - 调用 PhysicalCSINodeReconciler.CreateVirtualCSINode()
   - 检查物理 CSINode 是否存在
   - 创建虚拟 CSINode 并添加标签和注解

2. **更新流程**
   - 监听物理 CSINode 变化
   - 验证虚拟节点标签匹配
   - 更新虚拟 CSINode 的 spec 和注解

3. **删除流程**
   - PhysicalNodeReconciler 删除虚拟节点
   - 调用 PhysicalCSINodeReconciler.DeleteVirtualCSINode()
   - 验证 CSINode 是否由 Tapestry 管理
   - 安全删除虚拟 CSINode

## 标签和注解

### 标签
- `tapestry.io/managed-by`: 标识由 Tapestry 管理
- `tapestry.io/cluster-binding`: 集群绑定名称
- `tapestry.io/physical-cluster-id`: 物理集群 ID
- `tapestry.io/physical-node-name`: 物理节点名称

> **注意**: 这些标签常量定义在 `api/v1beta1/constants.go` 中，确保在整个项目中保持一致性。

### 注解
- `tapestry.io/last-sync-time`: 最后同步时间
- `tapestry.io/physical-cluster-name`: 物理集群名称
- `tapestry.io/physical-csinode-uid`: 物理 CSINode UID

## 配置

### RBAC 权限
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: tapestry-csinode-controller
rules:
- apiGroups: ["storage.k8s.io"]
  resources: ["csinodes"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

### 控制器配置
- 最大并发协调数: 1
- 同步间隔: 300秒
- 支持优雅关闭

## 测试

### 单元测试
- CSINode 创建和删除功能
- 标签和注解构建
- 虚拟节点标签验证

### 集成测试
- 与 PhysicalNodeReconciler 的协调工作
- 完整的生命周期管理
- 错误处理和恢复

## 使用示例

### 自动同步
当物理集群中存在 CSINode 时，系统会自动创建对应的虚拟 CSINode：

```yaml
# 物理集群 CSINode
apiVersion: storage.k8s.io/v1
kind: CSINode
metadata:
  name: worker-node-1
spec:
  drivers:
  - name: csi-driver
    nodeID: worker-node-1
```

```yaml
# 自动创建的虚拟 CSINode
apiVersion: storage.k8s.io/v1
kind: CSINode
metadata:
  name: vnode-worker-node-1
  labels:
    tapestry.io/managed-by: tapestry
    tapestry.io/cluster-binding: my-cluster
    tapestry.io/physical-cluster-id: cluster-1
    tapestry.io/physical-node-name: worker-node-1
  annotations:
    tapestry.io/last-sync-time: "2025-08-22T10:41:06Z"
    tapestry.io/physical-cluster-name: my-cluster
    tapestry.io/physical-csinode-uid: "12345678-1234-1234-1234-123456789abc"
spec:
  drivers:
  - name: csi-driver
    nodeID: worker-node-1
```

## 故障排除

### 常见问题

1. **虚拟 CSINode 未创建**
   - 检查物理 CSINode 是否存在
   - 验证虚拟节点标签是否正确
   - 查看控制器日志

2. **同步失败**
   - 检查 RBAC 权限
   - 验证集群绑定配置
   - 确认网络连接正常

3. **删除失败**
   - 检查 CSINode 是否由 Tapestry 管理
   - 验证 UID 匹配
   - 查看删除权限

### 日志示例
```
INFO    Creating virtual CSINode
INFO    Successfully created virtual CSINode
INFO    Virtual node validation failed
ERROR   Failed to create virtual CSINode
```
