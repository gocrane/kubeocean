# Kubeocean One-Click Deployment Scripts for TKE Clusters

> English | [中文](README_zh.md)

This directory provides one-click deployment scripts for Kubeocean components and cluster bindings, enabling a seamless experience with Kubeocean features on Tencent Cloud TKE clusters.

## Features

- Complete cluster environment configuration: Enable APIServer intranet access, create kube-dns-intranet
- Deploy Kubeocean components on compute and worker clusters
- Bind worker clusters to compute clusters and configure simple resource leasing policies

## Prerequisites

- At least one TKE standard cluster as the virtual compute cluster, and one TKE standard cluster as the worker cluster
- Direct connectivity between compute and worker cluster Pod networks and node networks
- Ability to access clusters using `kubectl`
- Local environment with `helm` installed (version v3)
- Installed and configured [Tencent Cloud CLI Tool (tccli)](https://cloud.tencent.com/document/product/440/6176)
- Installed [jq](https://jqlang.org/download/) JSON processing tool
- TKE cluster region, cluster ID, and subnet ID prepared
- Other requirements refer to: [Requirements](../../../docs/requirements.md)

## Basic Usage

### One-Click Installation (Recommended)

#### 1. Create Configuration File (config.env)

```bash
cat > config.env <<EOF
# Manager Cluster Settings
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-xxx1"
MANAGER_SUBNET_ID="subnet-xxx1"

# Worker Cluster Settings
WORKER_REGION="ap-guangzhou"
WORKER_CLUSTER_ID="cls-xxx2"
WORKER_SUBNET_ID="subnet-xxx2"
WORKER_KUBECONFIG="/tmp/kubeconfig-worker"
WORKER_CLUSTER_NAME="example-cluster"
EOF
```

#### 2. One-Click Deployment

```bash
./install-tke.sh
```

The script will automatically complete the following operations in order:
1. **Phase 1/2**: Deploy Worker Cluster
   - Enable cluster intranet access
   - Install kubeocean-worker components
   - Automatically add `kubeocean.io/role=worker` label to all nodes
   - Generate ResourceLeasingPolicy resource leasing strategy
2. **Phase 2/2**: Deploy Manager Cluster and Create Binding
   - Enable cluster intranet access
   - Create kube-dns-intranet service
   - Install kubeocean-manager components
   - Create ClusterBinding to bind worker cluster

#### 3. Verify Deployment

```bash
# Verify resources
kubectl get nodes
kubectl get clusterbindings
kubectl get resourceleasingpolicies
```

### Separate Cluster Installation and Binding

If you need to control the installation process of Worker and Manager clusters separately, you can use independent installation scripts.

#### 1. Create Configuration File (config.env)

```bash
cat > config.env <<EOF
# Manager Cluster Settings
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-xxx1"
MANAGER_SUBNET_ID="subnet-xxx1"

# Worker Cluster Settings
WORKER_REGION="ap-guangzhou"
WORKER_CLUSTER_ID="cls-xxx2"
WORKER_SUBNET_ID="subnet-xxx2"
WORKER_KUBECONFIG="/tmp/kubeconfig-worker"
WORKER_CLUSTER_NAME="example-cluster"
EOF
```

#### 2. Deploy Worker Cluster

```bash
./install-worker-tke.sh
```

The script will automatically complete the following operations:
- Check prerequisites
- Enable cluster intranet access
- Get cluster kubeconfig
- Install kubeocean-worker components
- **Automatically add `kubeocean.io/role=worker` label to all nodes**

#### 3. Deploy Manager Cluster

```bash
./install-manager-tke.sh
```

#### 4. Verify Deployment

```bash
# Verify resources
kubectl get nodes -l kubeocean.io/role=worker
kubectl get clusterbindings
kubectl get resourceleasingpolicies
```

### Uninstall Clusters

#### One-Click Uninstallation (Recommended)

If you used `install-tke.sh` for one-click installation, you can use `uninstall-tke.sh` for one-click uninstallation:

```bash
./uninstall-tke.sh
```

The script will automatically complete the following operations in the correct order:
1. **Phase 1/2**: Uninstall Manager Cluster
   - Get Manager cluster kubeconfig (or reuse existing context)
   - Delete ClusterBinding resources
   - Delete worker kubeconfig secrets
   - Uninstall kubeocean-manager components
   - Clean up manager context from kubectl config
2. **Phase 2/2**: Uninstall Worker Cluster
   - Get Worker cluster kubeconfig (or reuse existing context)
   - Delete ResourceLeasingPolicy resources
   - Uninstall kubeocean-worker components
   - Clean up worker context from kubectl config

#### Per-Cluster Uninstallation

If you need to uninstall a specific cluster individually, you can use separate uninstallation scripts.

##### 1. Uninstall Manager Cluster

```bash
./uninstall-manager-tke.sh
```

The script will automatically complete the following operations:
- Get Manager cluster kubeconfig (or reuse existing context)
- Delete ClusterBinding resources
- Delete worker kubeconfig secrets
- Uninstall kubeocean-manager components
- Clean up manager context from kubectl config

##### 2. Uninstall Worker Cluster

```bash
./uninstall-worker-tke.sh
```

The script will automatically complete the following operations:
- Get Worker cluster kubeconfig (or reuse existing context)
- Delete ResourceLeasingPolicy resources
- Uninstall kubeocean-worker components
- Clean up worker context from kubectl config

**Note:** For per-cluster uninstallation, the uninstallation order should be Manager first, then Worker.

## Advanced Usage

### Using Custom Configuration Files

You can create different configuration files for different environments:

```bash
# Create production environment configuration
cat > prod.env <<EOF
# Manager Cluster Settings
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-prod-manager"
MANAGER_SUBNET_ID="subnet-prod-manager"

# Worker Cluster Settings
WORKER_REGION="ap-guangzhou"
WORKER_CLUSTER_ID="cls-prod-worker"
WORKER_SUBNET_ID="subnet-prod-worker"
WORKER_KUBECONFIG="/data/kubeconfig/prod-worker"
WORKER_CLUSTER_NAME="prod-cluster"
EOF

# Use custom configuration file (one-click installation)
./install-tke.sh -c prod.env

# Or install separately
./install-worker-tke.sh -c prod.env
./install-manager-tke.sh -c prod.env
```

### Only Unbind Cluster (Without Uninstalling Manager Components)

Suitable for scenarios where you need to keep Manager components and only unbind a specific Worker cluster:

```bash
# Set in configuration file
cat > config.env <<EOF
MANAGER_REGION="ap-guangzhou"
MANAGER_CLUSTER_ID="cls-manager-xxx"
SKIP_MANAGER_UNINSTALL="true"
EOF

./uninstall-manager-tke.sh

# Or override with environment variable
SKIP_MANAGER_UNINSTALL="true" ./uninstall-manager-tke.sh
```

### Environment Variable Override

You can use environment variables to override values in the configuration file:

```bash
# Override configuration during one-click installation
WORKER_CLUSTER_NAME="prod-cluster" ./install-tke.sh -c prod.env

# Override Worker kubeconfig output path during separate installation
WORKER_KUBECONFIG="/data/kubeconfig/prod-worker" ./install-worker-tke.sh -c prod.env

# Override Worker cluster name during separate installation
WORKER_CLUSTER_NAME="prod-cluster" ./install-worker-tke.sh

# Override multiple configurations
WORKER_REGION="ap-shanghai" WORKER_KUBECONFIG="/data/kubeconfig" ./install-worker-tke.sh -c prod.env
```

## Configuration Description

### Required Configuration

#### One-Click Installation (install-tke.sh) and Full Deployment

| Configuration Item | Description | Example Value |
|--------|------|--------|
| `MANAGER_REGION` | Manager cluster region | `ap-guangzhou` |
| `MANAGER_CLUSTER_ID` | Manager cluster ID | `cls-manager-xxx` |
| `MANAGER_SUBNET_ID` | Manager cluster subnet ID | `subnet-manager-xxx` |
| `WORKER_REGION` | Worker cluster region | `ap-guangzhou` |
| `WORKER_CLUSTER_ID` | Worker cluster ID | `cls-worker-xxx` |
| `WORKER_SUBNET_ID` | Worker cluster subnet ID | `subnet-worker-xxx` |

#### Worker Cluster Separate Installation (install-worker-tke.sh)

| Configuration Item | Description | Example Value |
|--------|------|--------|
| `WORKER_REGION` | Worker cluster region | `ap-guangzhou` |
| `WORKER_CLUSTER_ID` | Worker cluster ID | `cls-worker-xxx` |
| `WORKER_SUBNET_ID` | Worker cluster subnet ID | `subnet-worker-xxx` |

#### Manager Cluster Separate Installation (install-manager-tke.sh)

| Configuration Item | Description | Example Value |
|--------|------|--------|
| `MANAGER_REGION` | Manager cluster region | `ap-guangzhou` |
| `MANAGER_CLUSTER_ID` | Manager cluster ID | `cls-manager-xxx` |
| `MANAGER_SUBNET_ID` | Manager cluster subnet ID | `subnet-manager-xxx` |
| `WORKER_CLUSTER_ID` | Worker cluster ID (for creating binding) | `cls-worker-xxx` |

### Optional Configuration

| Configuration Item | Description | Default Value | Applicable Scripts |
|--------|------|--------|----------|
| `WORKER_KUBECONFIG` | Worker kubeconfig path | `/tmp/kubeconfig-worker` | One-click installation, Install/Uninstall Worker and Manager |
| `WORKER_CLUSTER_NAME` | Worker cluster name (for ResourceLeasingPolicy) | `example-cluster` | One-click installation, Install Worker |
| `SKIP_MANAGER_UNINSTALL` | Skip Manager component uninstallation (only delete binding) | `false` | Uninstall Manager |

## Configuration Priority

Configuration item priority from high to low:

1. **Environment Variables** - Highest priority
2. **Configuration File** - Medium priority
3. **Default Values** - Lowest priority

## Script Function Description

### install-tke.sh (One-Click Installation)

The script will automatically execute the following steps in order:
1. Load configuration file
2. Call `install-worker-tke.sh` to install Worker cluster components
3. Call `install-manager-tke.sh` to install Manager cluster components and create cluster binding

**Applicable Scenarios**:
- First-time deployment of Kubeocean
- Quickly setting up test environments
- Automated deployment scripts

### install-worker-tke.sh

The script will automatically complete the following operations:
1. Check prerequisites (tccli, jq, kubectl, helm, etc.)
2. Enable cluster intranet access (if not enabled)
3. Get cluster kubeconfig and configure context
4. Install kubeocean-worker components
5. **Automatically add `kubeocean.io/role=worker` label to all nodes**
6. Generate worker kubeconfig for manager cluster binding
7. Deploy ResourceLeasingPolicy resource leasing strategy

### install-manager-tke.sh

The script will automatically complete the following operations:
1. Check prerequisites (tccli, jq, kubectl, helm, worker kubeconfig, etc.)
2. Enable cluster intranet access (if not enabled)
3. Get cluster kubeconfig and configure context
4. Create kube-dns-intranet service (if it doesn't exist)
5. Install kubeocean-manager components
6. Create ClusterBinding to bind worker cluster

### uninstall-worker-tke.sh

The script will automatically complete the following operations:
1. Check prerequisites (tccli, jq, kubectl, helm, etc.)
2. Get cluster kubeconfig and configure context (or reuse existing context)
3. Delete ResourceLeasingPolicy resources
4. Uninstall kubeocean-worker components
5. Clean up worker context from kubectl config

### uninstall-manager-tke.sh

The script will automatically complete the following operations:
1. Check prerequisites (tccli, jq, kubectl, helm, etc.)
2. Get cluster kubeconfig and configure context (or reuse existing context)
3. Delete ClusterBinding resources
4. Delete worker kubeconfig secrets
5. Uninstall kubeocean-manager components (unless `SKIP_MANAGER_UNINSTALL=true` is set)
6. Clean up manager context from kubectl config

### uninstall-tke.sh (One-Click Uninstallation)

The script will automatically execute the following steps in order:
1. Load configuration file
2. Call `uninstall-manager-tke.sh` to uninstall Manager cluster components
3. Call `uninstall-worker-tke.sh` to uninstall Worker cluster components
4. Automatically clean up all related contexts from kubectl config

**Applicable Scenarios**:
- Complete uninstallation of Kubeocean deployed via `install-tke.sh`
- Clean up test environments
- Automated uninstallation scripts

## Troubleshooting

### 1. Configuration File Not Found

If the default configuration file `./config.env` does not exist, the script will silently skip it. Ensure:
- Configuration file has been created: `cp config.env.template config.env`
- Or use `-c` / `--config` parameter to specify configuration file path

### 2. Unsupported Parameter Error

If you see the following error:
```
❌ Unknown argument: -r
ℹ️  Only -h/--help and -c/--config options are supported
ℹ️  All other configurations should be provided in config file
```

**Reason**: The script only supports `-h/--help` and `-c/--config` parameters, other configurations must be provided in the configuration file.

**Solution**: Write configurations to the configuration file instead of the command line.

### 3. View Configuration Loading Status

During script execution, you will see:

```bash
ℹ️  Loading configuration from: ./config.env
✅ Configuration loaded
```

If you don't see this information, it means the configuration file was not loaded.

### 4. Validate Configuration File

```bash
# Check configuration file syntax
bash -n config.env

# Manually load configuration to view
source config.env
echo "WORKER_REGION=$WORKER_REGION"
echo "WORKER_CLUSTER_ID=$WORKER_CLUSTER_ID"
```

### 5. tccli Authentication Failed

Ensure tccli is properly configured:

```bash
# Configure tccli
tccli configure

# Test connection
tccli tke DescribeClusters --region ap-guangzhou
```

### 6. Cluster Intranet Access Enablement Failed

If there are errors indicating incorrect subnet ID or failure to enable intranet access:
- Confirm `SUBNET_ID` configuration is correct
- Confirm subnet is in the same VPC as the cluster
- Check Tencent Cloud account permissions

### 7. Cluster Connection Failed

#### Error Message 1: `Failed to connect to cluster using the kubeconfig`

**Scenario**: Connection verification fails after obtaining kubeconfig and replacing the internal network address.

**Possible Causes**:
- Cluster intranet access not properly enabled
- Internal load balancer information not fully updated yet (usually requires a few seconds)
- Network connectivity issue between the script execution machine and the cluster
- Cluster API Server unreachable

**Solutions**:

1. **Check cluster intranet access status**
   ```bash
   tccli tke DescribeClusterEndpointStatus \
     --ClusterId <CLUSTER_ID> \
     --IsExtranet false \
     --region <REGION>
   ```
   
   Confirm the returned status is `"Status": "Running"`

2. **Wait for internal load balancer to be ready**
   
   When cluster intranet access is just enabled, the load balancer may need a few seconds to initialize. Suggestions:
   - Wait 5-10 seconds and retry
   - Script has built-in wait mechanism, but some scenarios may require longer time

3. **Check network connectivity**
   ```bash
   # Get internal network access address
   tccli tke DescribeClusterSecurity \
     --ClusterId <CLUSTER_ID> \
     --region <REGION> | jq -r '.PgwEndpoint'
   
   # Test connectivity (example address)
   curl -k https://<PgwEndpoint>:443
   ```

4. **Check subnet configuration**
   
   Ensure the provided `SUBNET_ID` is within the same network reachable range as the machine running the script.

#### Error Message 2: `Cannot connect to Kubernetes cluster. Please check kubeconfig configuration`

**Scenario**: kubectl cannot connect to the cluster when deploying components.

**Possible Causes**:
- kubectl context not properly set
- kubeconfig file corrupted or incomplete
- Cluster API Server temporarily unavailable
- Network connection interrupted

**Solutions**:

1. **Check current context**
   ```bash
   kubectl config current-context
   kubectl config get-contexts
   ```

2. **Test cluster connection**
   ```bash
   kubectl cluster-info
   kubectl get nodes
   ```

3. **Switch to the correct context**
   ```bash
   # Worker cluster
   kubectl config use-context worker-admin-<CLUSTER_ID>
   
   # Manager cluster
   kubectl config use-context manager-admin-<CLUSTER_ID>
   ```

4. **Re-obtain kubeconfig**
   
   If kubeconfig is corrupted, you can rerun the installation script to obtain it:
   ```bash
   # Rerunning will automatically detect and reuse or update context
   ./install-worker-tke.sh -c config.env
   ```

5. **View detailed error information**
   ```bash
   kubectl get nodes -v=8
   ```

## References

- [Kubeocean Documentation](../../../README.md)
- [System Requirements](../../../docs/requirements.md)
- [Tencent Cloud CLI Tool Documentation](https://cloud.tencent.com/document/product/440/6176)

