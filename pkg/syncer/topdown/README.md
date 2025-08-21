# Top-down Syncer

Top-down Syncer 实现了从虚拟集群到物理集群的Pod同步功能，这是tasks.md第9步的实现。

## 功能特性

### Virtual Pod Controller

`VirtualPodReconciler` 负责监听虚拟集群中的Pod事件，并实现以下同步逻辑：

#### 1. Pod删除处理
- **虚拟Pod处于删除状态且存在物理Pod映射**：删除对应的物理Pod
- **虚拟Pod处于删除状态但物理Pod不存在**：强制删除虚拟Pod（移除finalizers）

#### 2. Pod创建流程
- **无物理Pod映射注解**：生成随机名称映射，更新虚拟Pod注解，等待下次同步
- **有映射注解但无UID**：创建物理Pod，更新虚拟Pod的物理Pod UID注解
- **完整映射但物理Pod丢失**：设置虚拟Pod状态为Failed

#### 3. 已存在的物理Pod
- **物理Pod已存在**：不执行任何操作（由bottom-up syncer处理状态同步）

## 注解映射

### 虚拟Pod注解
- `tapestry.io/physical-pod-namespace`: 物理Pod的命名空间
- `tapestry.io/physical-pod-name`: 物理Pod的名称
- `tapestry.io/physical-pod-uid`: 物理Pod的UID
- `tapestry.io/last-sync-time`: 最后同步时间

### 物理Pod注解
- `tapestry.io/virtual-pod-namespace`: 虚拟Pod的命名空间
- `tapestry.io/virtual-pod-name`: 虚拟Pod的名称
- `tapestry.io/virtual-pod-uid`: 虚拟Pod的UID

## 使用示例

```go
package main

import (
    "context"
    "log"
    
    "k8s.io/apimachinery/pkg/runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"
    
    "github.com/TKEColocation/tapestry/pkg/syncer/topdown"
    cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func main() {
    // 初始化客户端和scheme
    var virtualClient, physicalClient client.Client
    var scheme *runtime.Scheme
    
    clusterBinding := &cloudv1beta1.ClusterBinding{
        ObjectMeta: metav1.ObjectMeta{
            Name: "my-cluster",
        },
    }
    
    // 创建TopDownSyncer
    syncer := topdown.NewTopDownSyncer(virtualClient, physicalClient, scheme, clusterBinding)
    
    // 启动同步器
    ctx := context.Background()
    if err := syncer.Start(ctx); err != nil {
        log.Fatal("Failed to start top-down syncer:", err)
    }
    
    // 运行直到收到停止信号
    // ...
    
    // 停止同步器
    syncer.Stop()
}
```

## 架构设计

### 组件结构
```
topdown/
├── virtual_pod_controller.go      # 虚拟Pod控制器实现
├── virtual_pod_controller_test.go # 单元测试
├── topdown_syncer.go              # 主同步器
├── topdown_syncer_test.go         # 集成测试
└── README.md                      # 文档
```

### 控制器流程
1. **监听虚拟集群Pod事件**（排除系统命名空间）
2. **检查Pod删除状态**：处理DeletionTimestamp不为nil的情况
3. **验证物理Pod存在性**：直接查询物理集群确认
4. **执行创建流程**：分阶段处理注解映射和Pod创建
5. **错误处理**：设置合适的Pod状态和重试机制

## 测试覆盖

- **单元测试**：覆盖所有核心方法和边界条件
- **集成测试**：测试完整的同步器生命周期
- **错误场景**：网络错误、资源冲突、状态不一致等

## 注意事项

1. **系统Pod过滤**：自动排除kube-system、kube-public、default等系统命名空间的Pod
2. **并发控制**：支持最多50个并发reconcile操作
3. **随机名称生成**：使用加密安全的随机字符串生成物理Pod名称
4. **双向映射**：确保虚拟Pod和物理Pod之间的双向引用一致性
5. **优雅处理**：正确处理Pod删除、网络错误和资源冲突等异常情况


