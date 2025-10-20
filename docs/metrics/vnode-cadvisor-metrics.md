# Vnode cAdvisor Metrics Collection Solution

## Overview

For containers scheduled on Vnodes (virtual nodes), their cAdvisor metrics need to be collected through "custom monitoring" approach. Since Vnodes are virtual nodes and cannot directly obtain metrics through traditional cAdvisor endpoints, the Proxier component is required to proxy and expose these metrics.

This document describes two viable metrics collection solutions, each with specific use cases and configuration methods.

## Solution Comparison

| Feature | Solution 1 (Single Port Multi Path) | Solution 2 (Multi Port Single Path) |
|---------|-------------------------------------|--------------------------------------|
| Port Usage | Single port (9006) | Independent port for each Vnode |
| Path Rule | `/VnodeName/metrics` | `/metrics` |
| Protocol | HTTP | HTTPS |
| Configuration Complexity | Medium | Simple |
| Port Management | Simple | Requires port allocation management |


## Solution 1: Single Port Multi Path Solution

### Working Principle

1. **Unified Port Exposure**: All Vnode cAdvisor metrics are exposed through the same port of Proxier Pod (default 9006)
2. **Path Differentiation**: Each Vnode is distinguished by different URL paths, formatted as `{ProxierPodIP}:9006/{VnodeName}/metrics`
3. **Annotation Marking**: Each Vnode will be added with a specific annotation to identify its Prometheus collection URL

### Vnode Configuration

Each Vnode will automatically add the following annotation:

```yaml
annotations:
  kubeocean.io/prometheus-url: "{ProxierPodIP}:9006/{VnodeName}"
```

### Prometheus Configuration

```yaml
scrape_configs:
- job_name: tke-cadvisor-new
  scheme: http
  metrics_path: /metrics
  kubernetes_sd_configs:
  - role: node
  relabel_configs:
  # Only collect vnode type nodes
  - source_labels:
    - __meta_kubernetes_node_label_node_kubernetes_io_instance_type
    regex: vnode
    action: keep
  # Exclude eklet nodes
  - source_labels:
    - __meta_kubernetes_node_label_node_kubernetes_io_instance_type
    regex: eklet
    action: drop
  # Preserve node labels
  - action: labelmap
    regex: __meta_kubernetes_node_label_(.+)
  # Extract address from annotation
  - source_labels:
    - __meta_kubernetes_node_annotation_kubeocean_io_prometheus_url
    regex: (.+)/(.+)
    target_label: __address__
    replacement: $1
    action: replace
  # Extract metrics path from annotation
  - source_labels:
    - __meta_kubernetes_node_annotation_kubeocean_io_prometheus_url
    regex: (.+)/(.+)
    target_label: __metrics_path__
    replacement: $2/metrics
    action: replace
  # Clean up kubernetes-related labels
  metric_relabel_configs:
  - regex: .*kubernetes_io.*
    action: labeldrop
```

## Solution 2: Multi Port Single Path Solution

### Working Principle

1. **Independent Port Allocation**: Assign a unique port number for each Vnode
2. **Unified Path**: All metrics are exposed through `/metrics` path
3. **HTTPS Protocol**: Use HTTPS for better security
4. **Label Marking**: Identify assigned port through node labels

### Vnode Configuration

Each Vnode will automatically add the following label:

```yaml
labels:
  kubeocean.io/proxier_port: "{Assigned Port Number}"
```

### Prometheus Configuration

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
  # Only collect vnode type nodes
  - source_labels:
    - __meta_kubernetes_node_label_node_kubernetes_io_instance_type
    regex: vnode
    action: keep
  # Exclude eklet nodes
  - source_labels:
    - __meta_kubernetes_node_label_node_kubernetes_io_instance_type
    regex: eklet
    action: drop
  # Preserve node labels
  - action: labelmap
    regex: __meta_kubernetes_node_label_(.+)
  # Combine IP and port to form address
  - source_labels:
    - __meta_kubernetes_node_address_InternalIP
    - __meta_kubernetes_node_label_kubeocean_io_proxier_port
    regex: (.+);(.+)
    target_label: __address__
    replacement: $1:$2
    action: replace
```

### Deployment Steps

1. **Determine Solution**: Choose appropriate solution based on actual requirements
2. **Configure Proxier**: Ensure Proxier component supports the selected solution
3. **Configure Prometheus**: Apply corresponding scrape configuration
4. **Verify Collection**: Confirm metrics can be collected normally
5. **Monitoring Alerts**: Set up related monitoring alert rules

### Troubleshooting

#### Common Issues

1. **Metrics Cannot Be Collected**
   - Check if Vnode annotations/labels are correctly configured
   - Verify if Proxier Pod is running normally
   - Confirm network connectivity

2. **Missing Partial Metrics**
   - Check if relabel configuration is correct
   - Verify if metrics path is accessible
   - Check Prometheus logs

3. **Performance Issues**
   - Monitor Proxier Pod resource usage
   - Adjust collection frequency
   - Consider increasing Proxier Pod replicas

#### Debug Commands

```bash
# Check Vnode configuration
kubectl get nodes -l node.kubernetes.io/instance-type=vnode -o yaml

# Test metrics endpoint (Solution 1)
curl http://{ProxierPodIP}:9006/{VnodeName}/metrics

# Test metrics endpoint (Solution 2)
curl -k https://{ProxierPodIP}:{Port}/metrics

# Check Prometheus targets
# View in Prometheus UI: Status -> Targets
```

