# Kubeocean 指标

本文档描述了 Kubeocean 组件暴露的所有 Prometheus 指标。

## 概述

Kubeocean 的 Manager 和 Syncer 组件都在 `:8080/metrics` 端点暴露指标。这些指标提供了同步操作、Pod 生命周期管理和资源利用率的可见性。

## 指标端点

- **Manager**: `http://<manager-pod>:8080/metrics`
- **Syncer**: `http://<syncer-pod>:8080/metrics`
- **格式**: Prometheus 文本格式

---

## Manager 指标

Manager 指标跟踪 ClusterBinding 同步操作和状态。

| 指标名称 | 类型 | 标签 | 描述 | 查询示例 |
|---------|------|------|------|---------|
| `kubeocean_manager_sync_duration_seconds` | Histogram | 无 | ClusterBinding 同步操作的持续时间 | `histogram_quantile(0.95, rate(kubeocean_manager_sync_duration_seconds_bucket[5m]))` |
| `kubeocean_manager_sync_total` | Counter | `clusterbinding` | 同步操作总次数 | `rate(kubeocean_manager_sync_total[5m])` |
| `kubeocean_manager_sync_errors_total` | Counter | `clusterbinding` | 同步错误总次数 | `rate(kubeocean_manager_sync_errors_total[5m])` |
| `kubeocean_clusterbindings_total` | Gauge | `status` | 按状态统计的 ClusterBinding 总数（Deleting、Pending、Ready、Failed） | `kubeocean_clusterbindings_total{status="Ready"}` |

### Manager 指标示例

**同步成功率**：
```promql
100 * (1 - rate(kubeocean_manager_sync_errors_total[5m]) / rate(kubeocean_manager_sync_total[5m]))
```

**Ready 状态的 ClusterBinding 数量**：
```promql
kubeocean_clusterbindings_total{status="Ready"}
```

**ClusterBinding 总数**：
```promql
sum(kubeocean_clusterbindings_total)
```

---

## Syncer 指标

Syncer 指标跟踪 Pod 生命周期、资源利用率和集群同步状态。

### Pod 创建指标

| 指标名称 | 类型 | 标签 | 描述 | 查询示例 |
|---------|------|------|------|---------|
| `kubeocean_syncer_create_pod_total` | Counter | `clusterbinding` | 物理 Pod 创建尝试总次数 | `rate(kubeocean_syncer_create_pod_total[5m])` |
| `kubeocean_syncer_create_pod_errors_total` | Counter | `clusterbinding` | Pod 创建错误总次数 | `rate(kubeocean_syncer_create_pod_errors_total[5m])` |
| `kubeocean_syncer_pod_created_total` | Counter | `clusterbinding` | 成功创建的 Pod 总数 | `rate(kubeocean_syncer_pod_created_total[5m])` |
| `kubeocean_syncer_pod_created_failed_total` | Counter | `clusterbinding` | 标记为 Failed 的 Pod 总数 | `rate(kubeocean_syncer_pod_created_failed_total[5m])` |

### Pod 数量指标

| 指标名称 | 类型 | 标签 | 描述 | 过滤规则 |
|---------|------|------|------|---------|
| `kubeocean_syncer_virtual_pod_total` | Gauge | `clusterbinding`, `phase` | 按阶段统计的虚拟 Pod 数量 | 仅统计 `nodeName` 以 `vnode-` 开头的 Pod |
| `kubeocean_syncer_physical_pod_total` | Gauge | `clusterbinding`, `phase` | 按阶段统计的物理 Pod 数量 | 仅统计带有标签 `kubeocean.io/managed-by=kubeocean` 的 Pod |

**Pod 阶段**: `Pending`（等待中）、`Running`（运行中）、`Succeeded`（成功）、`Failed`（失败）、`Unknown`（未知）

### VNode 资源指标

| 指标名称 | 类型 | 标签 | 描述 |
|---------|------|------|------|
| `kubeocean_syncer_vnode_total` | Gauge | `clusterbinding` | 虚拟节点总数 |
| `kubeocean_syncer_vnode_origin_resource` | Gauge | `clusterbinding`, `node`, `resource` | VNode 原始可分配资源 |
| `kubeocean_syncer_vnode_available_resource` | Gauge | `clusterbinding`, `node`, `resource` | 应用策略限制后的可用资源 |
| `kubeocean_syncer_vnode_physical_usage` | Gauge | `clusterbinding`, `node`, `resource` | 物理资源使用量（非 Kubeocean Pod） |
| `kubeocean_syncer_vnode_virtual_usage` | Gauge | `clusterbinding`, `node`, `resource` | 虚拟 Pod 资源使用量 |

### 集群聚合指标

| 指标名称 | 类型 | 标签 | 描述 |
|---------|------|------|------|
| `kubeocean_syncer_cluster_origin_resource` | Gauge | `clusterbinding`, `resource` | 所有 VNode 的原始资源总和 |
| `kubeocean_syncer_cluster_available_resource` | Gauge | `clusterbinding`, `resource` | 所有 VNode 的可用资源总和 |
| `kubeocean_syncer_cluster_physical_usage` | Gauge | `clusterbinding`, `resource` | 物理资源使用总量 |
| `kubeocean_syncer_cluster_virtual_usage` | Gauge | `clusterbinding`, `resource` | 虚拟 Pod 资源使用总量 |

### 资源类型

指标中支持的资源类型：

| 资源 | 单位 | 描述 |
|------|------|------|
| `cpu` | 毫核 (m) | CPU 核心（1000m = 1 核） |
| `memory` | MiB | 内存（兆字节） |
| `ephemeral-storage` | MiB | 临时存储 |
| `nvidia.com/gpu` | 个数 | NVIDIA GPU |
| `gocrane.io/cpu` | 毫核 (m) | Gocrane CPU |
| `gocrane.io/memory` | MiB | Gocrane 内存 |
| `tke.cloud.tencent.com/eni-ip` | 个数 | TKE ENI IP 资源（共享网卡模式） |
| `tke.cloud.tencent.com/sub-eni` | 个数 | TKE 子网卡资源 |
| `tke.cloud.tencent.com/direct-eni` | 个数 | TKE Direct ENI 资源（独立网卡模式） |
| `tke.cloud.tencent.com/tke-shared-rdma` | 个数 | TKE RDMA 资源 |

### Syncer 指标示例

**Pod 创建成功率**：
```promql
100 * (1 - rate(kubeocean_syncer_create_pod_errors_total[5m]) / rate(kubeocean_syncer_create_pod_total[5m]))
```

**虚拟 vs 物理 Pod 数量**：
```promql
kubeocean_syncer_virtual_pod_total{phase="Running"} - ignoring(phase) kubeocean_syncer_physical_pod_total{phase="Running"}
```

**集群资源利用率**：
```promql
100 * kubeocean_syncer_cluster_virtual_usage / kubeocean_syncer_cluster_available_resource
```

**总 CPU 容量**：
```promql
sum(kubeocean_syncer_cluster_origin_resource{resource="cpu"})
```

**按类型的可用资源**：
```promql
sum(kubeocean_syncer_cluster_available_resource) by (resource)
```
