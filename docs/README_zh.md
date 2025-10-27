# Kubeocean 文档索引

> [English](README.md) | 中文

本目录包含 Kubeocean 项目的详细技术文档。

## 快速开始

- [快速开始指南](quick-start_zh.md) - 快速上手 Kubeocean

## 环境要求

- [环境要求](requirements_zh.md) - 部署 Kubeocean 的环境要求

## 教程文档

- [安装指南](tutorials/installation_zh.md) - Kubeocean 组件安装和环境配置
- [绑定工作集群](tutorials/bindCluster_zh.md) - 绑定工作集群到虚拟算力集群

## 架构文档

- [架构概览](architecture_zh.md) - 系统整体架构设计和组件介绍

## 设计文档

`design/` 目录包含各个组件的详细设计文档：

### 核心组件

- [Manager 组件](design/manager_zh.md) - Kubeocean Manager 组件：集群绑定管理和生命周期控制
- [BottomUp Syncer](design/bottomup-syncer_zh.md) - 自下而上同步：从物理集群到虚拟集群
- [TopDown Syncer](design/topdown-syncer_zh.md) - 自上而下同步：从虚拟集群到物理集群
- [CSINode 同步](design/csinode-sync_zh.md) - CSI 节点同步机制

## 监控指标

`metrics/` 目录包含监控和指标相关文档：

- [监控指标概览](metrics/metrics_zh.md) - 监控指标概览
- [Vnode cAdvisor 指标](metrics/vnode-cadvisor-metrics_zh.md) - 虚拟节点 cAdvisor 指标采集方案
