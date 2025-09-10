# Kubeocean Helm Chart

Kubeocean 是一个 Kubernetes 算力集群项目，通过整合多个物理 Kubernetes 集群的闲置计算资源，形成统一的虚拟算力集群。

## 架构概述

Kubeocean 包含两个主要组件：

- **Kubeocean Manager**: 管理 ClusterBinding 和 ResourceLeasingPolicy 资源，负责创建和管理 Kubeocean Syncer 实例
- **Kubeocean Syncer**: 负责特定物理集群与虚拟集群之间的双向同步

## 前置条件

- Kubernetes 1.28+ 集群
- Helm 3.0+
- kubectl 配置正确

## 安装

### 使用默认配置安装

```bash
helm install kubeocean ./charts/kubeocean \
  --namespace kubeocean-system \
  --create-namespace
```

### 使用自定义配置安装

```bash
helm install kubeocean ./charts/kubeocean \
  --namespace kubeocean-system \
  --create-namespace \
  --values custom-values.yaml
```

## 配置

### 主要配置参数

| 参数 | 描述 | 默认值 |
|------|------|--------|
| `global.imageRegistry` | 镜像仓库地址 | `ccr.ccs.tencentyun.com/tke-eni-test` |
| `manager.enabled` | 是否启用 Manager | `true` |
| `manager.replicas` | Manager 副本数 | `2` |
| `manager.image.repository` | Manager 镜像仓库 | `kubeocean-manager` |
| `manager.image.tag` | Manager 镜像标签 | `latest` |
| `syncer.enabled` | 是否启用 Syncer | `true` |
| `syncer.replicas` | Syncer 副本数 | `2` |
| `syncer.image.repository` | Syncer 镜像仓库 | `kubeocean-syncer` |
| `syncer.image.tag` | Syncer 镜像标签 | `latest` |
| `namespace.create` | 是否创建命名空间 | `true` |
| `namespace.name` | 命名空间名称 | `kubeocean-system` |

## 升级

```bash
helm upgrade kubeocean ./charts/kubeocean \
  --namespace kubeocean-system \
  --values custom-values.yaml
```

## 卸载

```bash
helm uninstall kubeocean --namespace kubeocean-system
```

## 验证安装

### 检查 Pod 状态

```bash
kubectl get pods -n kubeocean-system
```

### 检查服务状态

```bash
kubectl get svc -n kubeocean-system
```

### 检查 CRD

```bash
kubectl get crd | grep cloud.tencent.com
```

### 检查 Manager 日志

```bash
kubectl logs -n kubeocean-system -l app.kubernetes.io/component=manager
```

## 许可证

本项目采用 Apache License 2.0 许可证。
