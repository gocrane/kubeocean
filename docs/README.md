# Kubeocean 文档索引

本目录包含 Kubeocean 项目的详细技术文档。

## 架构文档

- [architecture.md](architecture.md) - 系统整体架构设计
- [design.md](design.md) - 详细设计文档

## 组件文档

- [kubeocean-manager.md](kubeocean-manager.md) - Kubeocean Manager 组件详细说明
- [manager.md](manager.md) - 管理器实现细节
- [syncer-template-architecture.md](syncer-template-architecture.md) - Syncer 组件架构设计

## 同步机制

- [topdown-syncer.md](topdown-syncer.md) - 自上而下同步：从虚拟集群到物理集群
- [bottomup-syncer.md](bottomup-syncer.md) - 自下而上同步：从物理集群到虚拟集群

## 控制器文档

- [clusterbinding-controller.md](clusterbinding-controller.md) - ClusterBinding 控制器实现
- [csinode-sync.md](csinode-sync.md) - CSI 节点同步机制

## 监控与运维

- [vnode-cadvisor-metrics.md](vnode-cadvisor-metrics.md) - Vnode cAdvisor 指标采集方案
