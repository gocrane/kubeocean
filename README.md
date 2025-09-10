# Kubeocean

Kubeocean 是一个 Kubernetes 算力集群项目，通过整合多个物理 Kubernetes 集群的闲置计算资源，形成统一的虚拟算力集群。

![alt text](docs/images/image.png)

## 架构概述

Kubeocean 包含两个主要组件：

- **Kubeocean Manager**: 管理 ClusterBinding 资源，负责创建和管理 Kubeocean Syncer 实例
- **Kubeocean Syncer**: 负责特定物理集群与虚拟集群之间的双向同步

## 环境要求

- Go 1.24.3 或更高版本
- Kubernetes 1.28+ 集群

## 快速开始

### 环境准备

需要至少两个集群：一个算力集群、多个业务集群，可以直接使用TKE集群。

### 部署使用样例

**在业务集群中：**

1. 创建权限 `helm install kubeocean-worker charts/kubeocean-worker`
2. 获取凭证 `bash hack/kubeconfig.sh kubeocean-syncer kubeocean-worker /tmp/kubeconfig.buz.1`

**在算力集群中：**

1. 安装kubeocean组件 `helm install kubeocean charts/kubeocean`
2. 设置凭证 `kubectl create secret generic -n kubeocean-system worker-cluster-kubeconfig --from-file=kubeconfig=/tmp/kubeconfig.buz.1`
3. 创建ClusterBinding和ResourceLeasingPolicy `kubectl create -f examples/`

### 构建项目

```bash
# 构建二进制文件
make build

# 或者直接使用 go build
go build -o bin/kubeocean-manager cmd/kubeocean-manager/main.go
go build -o bin/kubeocean-syncer cmd/kubeocean-syncer/main.go

# 构建 Docker 镜像
make docker-build
```

## 开发

### 生成代码

```bash
# 生成 CRD manifests 和 deepcopy 代码
make manifests generate
```

### 运行测试

```bash
# 运行单元测试
make test
```

### 代码格式化

```bash
# 格式化代码
make fmt vet
```

## 许可证

本项目采用 Apache License 2.0 许可证。