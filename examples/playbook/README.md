# Kubeocean One-Click Deployment Playbook

> English | [中文](README_zh.md)

This directory provides one-click deployment scripts for Kubeocean components and cluster binding, supporting a one-stop experience of Kubeocean-related features on TKE clusters.

## Features

- Deploy Kubeocean components on compute clusters and worker clusters
- Bind worker clusters to compute clusters and configure simple resource leasing policies

## Prerequisites (Environment Requirements)

- At least one Kubernetes cluster is required as a virtual compute cluster, and another Kubernetes cluster as a worker cluster
- Pod network and node network are directly interconnected between compute cluster and worker cluster
- Can access clusters using `kubectl`
- Local environment has `helm` installed with version v3
- Both clusters have APIServer internal network access enabled, i.e., there is a service named `kubernetes-intranet` with type `LoadBalancer` in the `default` namespace. For TKE standard clusters, refer to the following image to enable it in the cluster console:
![k8s-svc](../../docs/images/k8s-svc.png)
- Other requirements refer to: [Requirements](../../docs/requirements.md)

## Basic Usage

### Install and Bind
```bash
# Switch kubectl to worker cluster
bash install-worker.sh
# Add label to nodes that need resource extraction
kubectl label node <nodeName1> kubeocean.io/role=worker

# Switch kubectl to compute cluster, and copy the generated /tmp/kubeconfig-worker to local
bash install-manager.sh
```
*Note: Script execution order cannot be changed*

### Uninstall
```bash
# Switch kubectl to compute cluster
bash uninstall-manager.sh

# Switch kubectl to worker cluster
bash uninstall-worker.sh
```
*Note: Script execution order cannot be changed*

## Advanced Usage

### Install and Bind
```bash
# Specify output path for worker kubeconfig
bash install-worker.sh -o /tmp/my-kubeconfig
bash install-worker.sh --output /tmp/my-kubeconfig

# Skip ResourceLeasingPolicy deployment
bash install-worker.sh --skip-rlp

# Install manager with specified worker cluster ID and name
bash install-manager.sh -i cls-prod -n prod-cluster
bash install-manager.sh --cluster-id cls-prod --cluster-name prod-cluster

# Install manager with specified worker kubeconfig input path
bash install-manager.sh -w /tmp/kubeconfig-worker1
bash install-manager.sh --worker-kubeconfig /tmp/kubeconfig-worker1

# Only bind cluster, skip manager installation, and specify cluster ID and name
bash install-manager.sh -i cls-prod -n prod-cluster --skip-manager
```

### Uninstall
```bash
# Unbind specific worker cluster by name
bash uninstall-manager.sh -n worker1
bash uninstall-manager.sh --cluster-name worker1

# Only unbind worker cluster without uninstalling manager components
bash uninstall-manager.sh -n worker1 --skip-manager

# Uninstall and clean up specific RLP object by name
bash uninstall-worker.sh -r my-policy
bash uninstall-worker.sh --rlp-name my-policy
```

## TKE Cluster One-Click Deployment

For Tencent Cloud TKE clusters, dedicated one-click deployment scripts are provided to automatically complete cluster configuration (enable internal network access, create kube-dns-intranet, etc.) and component installation.

### Prerequisites

In addition to the basic environment requirements above, you also need:
- Install and configure [Tencent Cloud CLI (tccli)](https://cloud.tencent.com/document/product/440/6176)
- Install [jq](https://jqlang.org/download/) JSON processor
- Prepare TKE cluster region, cluster ID, and subnet ID

### Deploy Worker Cluster

```bash
# Basic usage (uses default kubeconfig output path /tmp/kubeconfig-worker)
bash install-worker-tke.sh \
  --region ap-guangzhou \
  --cluster-id cls-xxxxxxxx \
  --subnet-id subnet-xxxxxxxx

# Specify custom kubeconfig output path
bash install-worker-tke.sh \
  --region ap-guangzhou \
  --cluster-id cls-xxxxxxxx \
  --subnet-id subnet-xxxxxxxx \
  --output /tmp/my-kubeconfig

# Short parameter form
bash install-worker-tke.sh \
  -r ap-guangzhou \
  -c cls-xxxxxxxx \
  -s subnet-xxxxxxxx
```

The script will automatically:
1. Check prerequisites (tccli, jq, kubectl, helm, etc.)
2. Enable cluster internal network access (if not enabled)
3. Get cluster kubeconfig and configure context (name: `worker-admin-<cluster-id>`)
4. Install kubeocean-worker components
5. Generate worker kubeconfig for manager cluster binding

### Deploy Manager Cluster

```bash
# Basic usage (uses default worker kubeconfig path /tmp/kubeconfig-worker)
bash install-manager-tke.sh \
  --region ap-guangzhou \
  --cluster-id cls-xxxxxxxx \
  --subnet-id subnet-xxxxxxxx \
  --worker-cluster-id cls-worker-xxx

# Specify custom worker kubeconfig path
bash install-manager-tke.sh \
  --region ap-guangzhou \
  --cluster-id cls-xxxxxxxx \
  --subnet-id subnet-xxxxxxxx \
  --worker-kubeconfig /tmp/my-kubeconfig \
  --worker-cluster-id cls-worker-xxx

# Short parameter form
bash install-manager-tke.sh \
  -r ap-guangzhou \
  -c cls-xxxxxxxx \
  -s subnet-xxxxxxxx \
  -i cls-worker-xxx
```

The script will automatically:
1. Check prerequisites (tccli, jq, kubectl, helm, worker kubeconfig, etc.)
2. Enable cluster internal network access (if not enabled)
3. Get cluster kubeconfig and configure context (name: `manager-admin-<cluster-id>`)
4. Create kube-dns-intranet service (if not exists)
5. Install kubeocean-manager components
6. Create ClusterBinding to bind worker cluster

### Complete Example

```bash
# Step 1: Deploy worker cluster
bash install-worker-tke.sh \
  -r ap-guangzhou \
  -c cls-xxxxxxxx \
  -s subnet-xxxxxxxx

# Add label to nodes for resource extraction
kubectl label node <nodeName> kubeocean.io/role=worker

# Step 2: Deploy manager cluster (will automatically use /tmp/kubeconfig-worker)
bash install-manager-tke.sh \
  -r ap-guangzhou \
  -c cls-xxxxxxxx \
  -s subnet-xxxxxxxx \
  -i cls-xxxxxxxx

# Step 3: Verify deployment
# Switch to manager cluster context
kubectl config use-context manager-admin-cls-manager-xxx
kubectl get clusterbindings
kubectl get all -n kubeocean-system

# Switch to worker cluster context
kubectl config use-context worker-admin-cls-worker-xxx
kubectl get all -n kubeocean-worker
```

### Parameter Description

#### install-worker-tke.sh

| Parameter | Short | Description | Required | Default |
|-----------|-------|-------------|----------|---------|
| `--region` | `-r` | TKE cluster region | Yes | - |
| `--cluster-id` | `-c` | TKE cluster ID | Yes | - |
| `--subnet-id` | `-s` | Subnet ID (for internal network access) | Yes (when enabling internal access) | - |
| `--output` | `-o` | Worker kubeconfig output path | No | `/tmp/kubeconfig-worker` |

#### install-manager-tke.sh

| Parameter | Short | Description | Required | Default |
|-----------|-------|-------------|----------|---------|
| `--region` | `-r` | TKE cluster region | Yes | - |
| `--cluster-id` | `-c` | TKE cluster ID | Yes | - |
| `--subnet-id` | `-s` | Subnet ID (for internal access and DNS service) | Yes (when enabling internal access or creating DNS service) | - |
| `--worker-kubeconfig` | `-w` | Worker cluster kubeconfig path | No | `/tmp/kubeconfig-worker` |
| `--worker-cluster-id` | `-i` | Worker cluster ID | Yes | - |
```
