# Kubeocean Documentation Index

> English | [中文](README_zh.md)

This directory contains detailed technical documentation for the Kubeocean project.

## Quick Start

- [Quick Start Guide](quick-start.md) - Get started with Kubeocean quickly

## Requirements

- [Requirements](requirements.md) - Environment requirements for deploying Kubeocean

## Tutorial Documentation

- [Installation Guide](tutorials/installation.md) - Kubeocean component installation and environment configuration
- [Binding Worker Clusters](tutorials/bindCluster.md) - Binding worker clusters to virtual computing clusters

## Architecture Documentation

- [Architecture Overview](architecture.md) - Overall system architecture design and component introduction

## Design Documentation

The `design/` directory contains detailed design documents for each component:

### Core Components

- [Manager Component](design/manager.md) - Kubeocean Manager component: cluster binding management and lifecycle control
- [BottomUp Syncer](design/bottomup-syncer.md) - Bottom-up synchronization: from physical clusters to virtual clusters
- [TopDown Syncer](design/topdown-syncer.md) - Top-down synchronization: from virtual clusters to physical clusters
- [CSINode Sync](design/csinode-sync.md) - CSI node synchronization mechanism

## Metrics and Monitoring

The `metrics/` directory contains monitoring and metrics related documentation:

- [Metrics Overview](metrics/metrics.md) - Monitoring metrics overview
- [Vnode cAdvisor Metrics](metrics/vnode-cadvisor-metrics.md) - Virtual node cAdvisor metrics collection solution
