# Vnode cAdvisor 指标采集方案

## 概述

对于调度到Vnode（虚拟节点）上的容器，其cAdvisor指标需要通过"新建自定义监控"的方式进行采集。由于Vnode是虚拟节点，不能直接通过传统的cAdvisor端点获取指标，因此需要通过Proxier组件来代理和暴露这些指标。

本文档描述了两种可行的指标采集方案，每种方案都有其特定的使用场景和配置方式。

## 方案对比

| 特性 | 方案1 (单端口多路径) | 方案2 (多端口单路径) |
|------|-------------------|-------------------|
| 端口使用 | 单一端口(9006) | 每个Vnode独立端口 |
| 路径规则 | `/VnodeName/metrics` | `/metrics` |
| 协议 | HTTP | HTTPS |
| 配置复杂度 | 中等 | 简单 |
| 端口管理 | 简单 | 需要端口分配管理 |


## 方案1：单端口多路径方案

### 工作原理

1. **统一端口暴露**：所有Vnode的cAdvisor指标都通过Proxier Pod的同一个端口（默认9006）暴露
2. **路径区分**：每个Vnode通过不同的URL路径来区分，格式为 `{ProxierPodIP}:9006/{VnodeName}/metrics`
3. **注解标记**：每个Vnode都会被添加特定的annotation来标识其prometheus采集URL

### Vnode配置

每个Vnode节点会自动添加以下annotation：

```yaml
annotations:
  kubeocean.io/prometheus-url: "{ProxierPodIP}:9006/{VnodeName}"
```

### Prometheus配置

```yaml
scrape_configs:
- job_name: tke-cadvisor-new
  scheme: http
  metrics_path: /metrics
  kubernetes_sd_configs:
  - role: node
  relabel_configs:
  # 只采集vnode类型的节点
  - source_labels:
    - __meta_kubernetes_node_label_node_kubernetes_io_instance_type
    regex: vnode
    action: keep
  # 排除eklet节点
  - source_labels:
    - __meta_kubernetes_node_label_node_kubernetes_io_instance_type
    regex: eklet
    action: drop
  # 保留节点标签
  - action: labelmap
    regex: __meta_kubernetes_node_label_(.+)
  # 从annotation中提取地址
  - source_labels:
    - __meta_kubernetes_node_annotation_kubeocean_io_prometheus_url
    regex: (.+)/(.+)
    target_label: __address__
    replacement: $1
    action: replace
  # 从annotation中提取metrics路径
  - source_labels:
    - __meta_kubernetes_node_annotation_kubeocean_io_prometheus_url
    regex: (.+)/(.+)
    target_label: __metrics_path__
    replacement: $2/metrics
    action: replace
  # 清理kubernetes相关标签
  metric_relabel_configs:
  - regex: .*kubernetes_io.*
    action: labeldrop
```

## 方案2：多端口单路径方案

### 工作原理

1. **独立端口分配**：为每个Vnode分配一个唯一的端口号
2. **统一路径**：所有指标都通过 `/metrics` 路径暴露
3. **HTTPS协议**：使用HTTPS提供更好的安全性
4. **标签标记**：通过节点标签标识分配的端口

### Vnode配置

每个Vnode节点会自动添加以下label：

```yaml
labels:
  kubeocean.io/proxier_port: "{分配的端口号}"
```

### Prometheus配置

```yaml
scrape_configs:
- job_name: tke-cadvisor-4
  scheme: https
  tls_config:
    insecure_skip_verify: true
  metrics_path: /metrics
  kubernetes_sd_configs:
  - role: node
  relabel_configs:
  # 只采集vnode类型的节点
  - source_labels:
    - __meta_kubernetes_node_label_node_kubernetes_io_instance_type
    regex: vnode
    action: keep
  # 排除eklet节点
  - source_labels:
    - __meta_kubernetes_node_label_node_kubernetes_io_instance_type
    regex: eklet
    action: drop
  # 保留节点标签
  - action: labelmap
    regex: __meta_kubernetes_node_label_(.+)
  # 组合IP和端口形成地址
  - source_labels:
    - __meta_kubernetes_node_address_InternalIP
    - __meta_kubernetes_node_label_kubeocean_io_proxier_port
    regex: (.+);(.+)
    target_label: __address__
    replacement: $1:$2
    action: replace
```

### 部署步骤

1. **确定方案**：根据实际需求选择合适的方案
2. **配置Proxier**：确保Proxier组件支持选定的方案
3. **配置Prometheus**：应用相应的scrape配置
4. **验证采集**：确认指标能够正常采集
5. **监控告警**：设置相关的监控告警规则

### 故障排查

#### 常见问题

1. **指标无法采集**
   - 检查Vnode的annotation/label是否正确配置
   - 验证Proxier Pod是否正常运行
   - 确认网络连通性

2. **部分指标缺失**
   - 检查relabel配置是否正确
   - 验证指标路径是否可访问
   - 检查Prometheus日志

3. **性能问题**
   - 监控Proxier Pod的资源使用情况
   - 调整采集频率
   - 考虑增加Proxier Pod副本数

#### 调试命令

```bash
# 检查Vnode配置
kubectl get nodes -l node.kubernetes.io/instance-type=vnode -o yaml

# 测试指标端点（方案1）
curl http://{ProxierPodIP}:9006/{VnodeName}/metrics

# 测试指标端点（方案2）
curl -k https://{ProxierPodIP}:{Port}/metrics

# 检查Prometheus targets
# 在Prometheus UI中查看 Status -> Targets
```
