# kubeocean Makelib Modules

This directory contains modular Makefile configurations for the kubeocean project.

## Module Overview

- `build.mk` - Build-related targets and configurations
- `test.mk` - Test-related targets and configurations
- `kind.mk` - KIND (Kubernetes in Docker) local development environment

## KIND Module Usage Guide

The `kind.mk` provides a complete local Kubernetes development environment, supporting the creation of a KIND cluster with multiple nodes (1 control-plane + 2 workers).

### Main Features

#### Basic Operations
```bash
make kind-create     # Create KIND cluster (legacy)
make kind-delete     # Delete KIND cluster (legacy)
make kind-status     # Show multi-cluster status for kubeocean
make kind-clean         # Clean up all KIND related resources
```

#### Multi-Cluster Operations
```bash
# Create clusters
make kind-create-manager   # Create manager cluster (virtual cluster)
make kind-create-worker1   # Create worker cluster 1 (physical cluster)
make kind-create-worker2   # Create worker cluster 2 (physical cluster)
make kind-create-all       # Create all kubeocean clusters

# Delete clusters
make kind-delete-manager   # Delete manager cluster
make kind-delete-worker1   # Delete worker cluster 1
make kind-delete-worker2   # Delete worker cluster 2
make kind-delete-all       # Delete all kubeocean clusters
```

#### Development Utilities
```bash
make kind-load-images    # Load locally built images into KIND cluster
make kind-logs          # Show cluster logs
```

### Default Configuration

#### Legacy Single Cluster
- **Cluster Name**: `kubeocean-test`
- **Kubernetes Version**: `v1.28.0`
- **Node Configuration**: 1 control-plane + 2 worker nodes

#### Multi-Cluster Configuration
All clusters use the same configuration file `hack/makelib/kind/kind-config-kubeocean-test.yaml` and are accessed directly via container IP:

- **Manager Cluster**: `kubeocean-manager` (Virtual Cluster)
  - Config file: `kind-config-kubeocean-test.yaml`
  - Access method: Container IP:6443 (API Server)
  - Service ports: 8080 (Metrics), 8081 (Health Check)

- **Worker Cluster 1**: `kubeocean-worker1` (Physical Cluster)  
  - Config file: `kind-config-kubeocean-test.yaml`
  - Access method: Container IP:6443 (API Server)
  - Service ports: 8080 (Metrics), 8081 (Health Check)

- **Worker Cluster 2**: `kubeocean-worker2` (Physical Cluster)
  - Config file: `kind-config-kubeocean-test.yaml`
  - Access method: Container IP:6443 (API Server)
  - Service ports: 8080 (Metrics), 8081 (Health Check)

> **Note**: All clusters use unified configuration with no port mapping, accessing services directly through Docker container internal IP.

### Environment Variables

Customize configuration with environment variables:

```bash
# Basic Configuration
CLUSTER_NAME=my-cluster make kind-create          # Custom cluster name
KIND_K8S_VERSION=v1.29.0 make kind-create         # Custom K8s version

# Multi-cluster Management
CLUSTER_NAME=dev-cluster make kind-create         # Create dev cluster
CLUSTER_NAME=test-cluster make kind-create        # Create test cluster
CLUSTER_NAME=dev-cluster make kind-status         # Check dev cluster status
CLUSTER_NAME=test-cluster make kind-delete        # Delete test cluster
```
