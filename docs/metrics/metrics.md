# Kubeocean Metrics

This document describes all Prometheus metrics exposed by Kubeocean components.

## Overview

Kubeocean exposes metrics at `:8080/metrics` endpoint for both Manager and Syncer components. These metrics provide visibility into synchronization operations, pod lifecycle management, and resource utilization.

## Metrics Endpoint

- **Manager**: `http://<manager-pod>:8080/metrics`
- **Syncer**: `http://<syncer-pod>:8080/metrics`
- **Format**: Prometheus text format

---

## Manager Metrics

Manager metrics track ClusterBinding synchronization operations and status.

| Metric Name | Type | Labels | Description | Example Query |
|-------------|------|--------|-------------|---------------|
| `kubeocean_manager_sync_duration_seconds` | Histogram | None | Duration of ClusterBinding synchronization operations | `histogram_quantile(0.95, rate(kubeocean_manager_sync_duration_seconds_bucket[5m]))` |
| `kubeocean_manager_sync_total` | Counter | `clusterbinding` | Total number of synchronization operations | `rate(kubeocean_manager_sync_total[5m])` |
| `kubeocean_manager_sync_errors_total` | Counter | `clusterbinding` | Total number of synchronization errors | `rate(kubeocean_manager_sync_errors_total[5m])` |
| `kubeocean_clusterbindings_total` | Gauge | `status` | Total number of ClusterBindings by status (Deleting, Pending, Ready, Failed) | `kubeocean_clusterbindings_total{status="Ready"}` |

### Manager Metrics Examples

**Sync Success Rate**:
```promql
100 * (1 - rate(kubeocean_manager_sync_errors_total[5m]) / rate(kubeocean_manager_sync_total[5m]))
```

**Ready ClusterBindings Count**:
```promql
kubeocean_clusterbindings_total{status="Ready"}
```

**Total ClusterBindings**:
```promql
sum(kubeocean_clusterbindings_total)
```

---

## Syncer Metrics

Syncer metrics track pod lifecycle, resource utilization, and cluster synchronization status.

### Pod Creation Metrics

| Metric Name | Type | Labels | Description | Example Query |
|-------------|------|--------|-------------|---------------|
| `kubeocean_syncer_create_pod_total` | Counter | `clusterbinding` | Total number of physical pod creation attempts | `rate(kubeocean_syncer_create_pod_total[5m])` |
| `kubeocean_syncer_create_pod_errors_total` | Counter | `clusterbinding` | Total number of pod creation errors | `rate(kubeocean_syncer_create_pod_errors_total[5m])` |
| `kubeocean_syncer_pod_created_total` | Counter | `clusterbinding` | Total number of successfully created pods | `rate(kubeocean_syncer_pod_created_total[5m])` |
| `kubeocean_syncer_pod_created_failed_total` | Counter | `clusterbinding` | Total number of pods marked as Failed | `rate(kubeocean_syncer_pod_created_failed_total[5m])` |

### Pod Count Metrics

| Metric Name | Type | Labels | Description | Filtering |
|-------------|------|--------|-------------|-----------|
| `kubeocean_syncer_virtual_pod_total` | Gauge | `clusterbinding`, `phase` | Number of virtual pods by phase | Only pods with `nodeName` starting with `vnode-` |
| `kubeocean_syncer_physical_pod_total` | Gauge | `clusterbinding`, `phase` | Number of physical pods by phase | Only pods with label `kubeocean.io/managed-by=kubeocean` |

**Pod Phases**: `Pending`, `Running`, `Succeeded`, `Failed`, `Unknown`

### VNode Resource Metrics

| Metric Name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `kubeocean_syncer_vnode_total` | Gauge | `clusterbinding` | Total number of virtual nodes |
| `kubeocean_syncer_vnode_origin_resource` | Gauge | `clusterbinding`, `node`, `resource` | Original allocatable resources of VNodes |
| `kubeocean_syncer_vnode_available_resource` | Gauge | `clusterbinding`, `node`, `resource` | Available resources after policy limits |
| `kubeocean_syncer_vnode_physical_usage` | Gauge | `clusterbinding`, `node`, `resource` | Physical resource usage (non-Kubeocean pods) |
| `kubeocean_syncer_vnode_virtual_usage` | Gauge | `clusterbinding`, `node`, `resource` | Virtual pod resource usage |

### Cluster Aggregated Metrics

| Metric Name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `kubeocean_syncer_cluster_origin_resource` | Gauge | `clusterbinding`, `resource` | Total original resources across all VNodes |
| `kubeocean_syncer_cluster_available_resource` | Gauge | `clusterbinding`, `resource` | Total available resources across all VNodes |
| `kubeocean_syncer_cluster_physical_usage` | Gauge | `clusterbinding`, `resource` | Total physical resource usage |
| `kubeocean_syncer_cluster_virtual_usage` | Gauge | `clusterbinding`, `resource` | Total virtual pod resource usage |

### Resource Types

Supported resource types in metrics:

| Resource | Unit | Description |
|----------|------|-------------|
| `cpu` | millicores (m) | CPU cores (1000m = 1 core) |
| `memory` | MiB | Memory in Mebibytes |
| `ephemeral-storage` | MiB | Ephemeral storage |
| `nvidia.com/gpu` | count | NVIDIA GPUs |
| `gocrane.io/cpu` | millicores (m) | Gocrane CPU |
| `gocrane.io/memory` | MiB | Gocrane memory |
| `tke.cloud.tencent.com/eni-ip` | count | TKE ENI IP resources(Shared ENI) |
| `tke.cloud.tencent.com/sub-eni` | count | TKE Sub ENI resources |
| `tke.cloud.tencent.com/direct-eni` | count | TKE Direct ENI resources(Exclusive ENI) |
| `tke.cloud.tencent.com/tke-shared-rdma` | count | TKE RDMA resources |

### Syncer Metrics Examples

**Pod Creation Success Rate**:
```promql
100 * (1 - rate(kubeocean_syncer_create_pod_errors_total[5m]) / rate(kubeocean_syncer_create_pod_total[5m]))
```

**Virtual vs Physical Pod Count**:
```promql
kubeocean_syncer_virtual_pod_total{phase="Running"} - ignoring(phase) kubeocean_syncer_physical_pod_total{phase="Running"}
```

**Cluster Resource Utilization**:
```promql
100 * kubeocean_syncer_cluster_virtual_usage / kubeocean_syncer_cluster_available_resource
```

**Total CPU Capacity**:
```promql
sum(kubeocean_syncer_cluster_origin_resource{resource="cpu"})
```

**Available Resources by Type**:
```promql
sum(kubeocean_syncer_cluster_available_resource) by (resource)
```
