# Tapestry

Tapestry 是一个 Kubernetes 算力集群项目，通过整合多个物理 Kubernetes 集群的闲置计算资源，形成统一的虚拟算力集群。

## 架构概述

Tapestry 包含两个主要组件：

- **Tapestry Manager**: 管理 ClusterBinding 和 ResourceLeasingPolicy 资源，负责创建和管理 Tapestry Syncer 实例
- **Tapestry Syncer**: 负责特定物理集群与虚拟集群之间的双向同步

## 环境要求

- Go 1.24.3 或更高版本
- Kubernetes 1.28+ 集群
- kubectl 配置正确

## 快速开始

### 构建项目

```bash
# 构建二进制文件
make build

# 或者直接使用 go build
go build -o bin/tapestry-manager cmd/tapestry-manager/main.go
go build -o bin/tapestry-syncer cmd/tapestry-syncer/main.go

# 构建 Docker 镜像
make docker-build
```

### 运行组件

```bash
# 运行 Tapestry Manager
make run-manager

# 运行 Tapestry Syncer (需要指定 ClusterBinding 名称)
make run-syncer
```

### 安装 CRDs

```bash
# 安装自定义资源定义
make install
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

## 部署

```bash
# 部署到 Kubernetes 集群
make deploy
```

## 许可证

本项目采用 Apache License 2.0 许可证。