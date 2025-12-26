# LLM Training and Inference Cross-Cluster Deployment Example

> English | [中文](README_zh.md)

This directory contains a complete example of using Kubeocean for LLM training and inference across clusters.

## Scenario Description

This directory supports building a practical training and inference integrated scenario from scratch. The scenario uses two clusters to deploy online inference services and offline training tasks respectively. The online inference service exhibits tidal characteristics: during the day (8:00-18:00 daily), usage is high and requires all GPU resources of the online cluster, while at night (18:00-8:00 daily), usage is low and only requires half of the GPU resources. Offline training tasks are only deployed on the offline cluster and only when the online cluster frees up GPU resources at night.

The online inference service uses vLLM to deploy the online inference service, while the offline training task uses KubeRay + VeRL to deploy large model reinforcement learning training tasks. The model used is `Qwen2.5-0.5B-Instruct`, and the training dataset is GSM8K.

**Goal**: Automatically utilize idle GPUs for training tasks at night, and automatically exit training tasks during the day to ensure resources for online inference services.

**Note**: This project is simplified. Models and datasets are stored in images, and trained checkpoints are stored in local directories. Best practice recommends that models and datasets be loaded through cloud storage systems such as file storage, and trained checkpoints can also be saved through cloud storage systems.


## Directory Structure

```
.
├── config.env.example              # Configuration file example
├── run-demo.sh                     # One-click deployment script (recommended)
├── clean-demo.sh                   # One-click cleanup script (recommended)
├── vllm-infer-demo.sh              # vLLM inference service deployment script
├── llm-kuberay-verl-demo.sh        # VERL training job deployment script
├── is-qwen2-5-05b-vllm.yaml        # vLLM inference service YAML configuration
└── verl-raycluster.yaml            # RayCluster YAML configuration
```

## Prerequisites

### Resource Requirements

- At least one TKE standard cluster as the virtual compute cluster and one TKE standard cluster as the worker cluster.
- The worker cluster should have at least 4 GPU nodes, each with at least 2 GPUs (A10 or newer), 24 CPU cores, and 100GB memory. If these requirements are not met, you can manually adjust the resource requirements in the YAML files in this directory.
- Internal network access must be enabled for both compute and worker clusters, which requires deploying internal load balancers at a certain cost.

### Environment Requirements

- Compute cluster and worker cluster Pod networks are directly interconnected, and node networks are interconnected
- Access to clusters using `kubectl` through internal network
- Local environment has `helm` installed, version v3
- Install and configure [Tencent Cloud CLI tool (tccli)](https://cloud.tencent.com/document/product/440/6176)
- Install [jq](https://jqlang.org/download/) JSON processing tool
- Prepare TKE cluster region, cluster ID, and subnet ID
- Other requirements: [Requirements](../../../docs/requirements.md)

## One-Click Deployment (Recommended)

### 1. Create Configuration File

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

### 2. Run One-Click Deployment Script

```bash
./run-demo.sh
```

Or use a custom configuration file:

```bash
./run-demo.sh -c /path/to/my-config.env
```

### Deployment Process

The `run-demo.sh` script automatically executes the following steps:

1. **Prerequisites Check**: Check if dependency tools are installed
2. **Load Configuration**: Load environment variables from configuration file
3. **Install Kubeocean**: Call `../installation-tke/install-tke.sh` to install Kubeocean and bind worker cluster
4. **Deploy Inference Service**: Call `vllm-infer-demo.sh` to deploy vLLM inference service on worker cluster
5. **Deploy Training Job**: Call `llm-kuberay-verl-demo.sh` to deploy VERL training job on manager cluster

## Step-by-Step Deployment

If you need to execute step by step, you can run each script individually:

### 1. Deploy vLLM Inference Service

```bash
./vllm-infer-demo.sh -c config.env
```

This script will:
- Check worker cluster and configure access
- Install tke-hpc-controller addon (if needed)
- Deploy vLLM inference service and HorizontalPodCronscaler

### 2. Deploy VERL Training Job

```bash
./llm-kuberay-verl-demo.sh -c config.env
```

This script will:
- Check manager cluster and configure access
- Deploy KubeRay Operator
- Create RayCluster
- Submit VERL training job

## Deploy Resources Only

If you only need to deploy Kubernetes resources without script automation, you can directly use kubectl apply to deploy YAML files.

### 1. Deploy vLLM Inference Service Only

```bash
# Switch to worker cluster
kubectl config use-context worker-admin-$WORKER_CLUSTER_ID

# Deploy vLLM inference service
kubectl apply -f is-qwen2-5-05b-vllm.yaml -n default

# Check deployment status
kubectl get deployment,service,horizontalpodcronscaler -n default | grep qwen
```

**Note**: Using this method requires ensuring:
- Worker cluster kubeconfig is configured and accessible
- Cluster has sufficient GPU resources
- If HorizontalPodCronscaler functionality is needed, tke-hpc-controller must be installed in advance

### 2. Deploy VERL Training Job Only

```bash
# Switch to manager cluster
kubectl config use-context manager-admin-$MANAGER_CLUSTER_ID

# Deploy RayCluster
kubectl apply -f verl-raycluster.yaml -n default

# Check deployment status
kubectl get raycluster,pods -n default | grep verl
```

**Note**: Using this method requires ensuring:
- Manager cluster kubeconfig is configured and accessible
- KubeRay Operator is installed in the cluster (can be installed using the first few steps of `llm-kuberay-verl-demo.sh`)
- Cluster has sufficient GPU resources
- If you need to submit training jobs, you need to manually execute `ray job submit` command in the Ray head pod

## Verify Deployment

After deployment is complete, you can verify using the following commands:

### Check Kubeocean Components

```bash
# Switch to manager cluster
kubectl config use-context manager-admin-$MANAGER_CLUSTER_ID

# View resources in kubeocean-system namespace
kubectl get all -n kubeocean-system

# View cluster bindings
kubectl get clusterbindings
```

### Check vLLM Inference Service

```bash
# Switch to worker cluster
kubectl config use-context worker-admin-$WORKER_CLUSTER_ID

# View deployment
kubectl get deployment is-qwen2-5-05b-vllm -n default

# View pods
kubectl get pods -l app.kubernetes.io/instance=is-qwen2-5-05b-vllm -n default

# View service
kubectl get service is-qwen2-5-05b-vllm -n default

# View HorizontalPodCronscaler
kubectl get horizontalpodcronscaler is-qwen -n default

# Use port-forward to expose service to local (run in a new terminal window, or run in background)
kubectl port-forward svc/is-qwen2-5-05b-vllm 8000:8000 -n default

# Test inference service locally (execute in another terminal window)
curl --location http://localhost:8000/v1/chat/completions \
  --header 'Content-Type: application/json' \
  --data '{
    "model": "Qwen2.5-0.5B-Instruct",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Provide steps to serve an LLM using vllm."}
    ]
  }'
```

### Check VERL Training Job

```bash
# Switch to manager cluster
kubectl config use-context manager-admin-$MANAGER_CLUSTER_ID

# View RayCluster
kubectl get raycluster verl-cluster -n default

# View Ray pods
kubectl get pods -l ray.io/cluster=verl-cluster -n default

# Check training job status
HEAD_POD=$(kubectl get pods -n default -l ray.io/cluster=verl-cluster,ray.io/node-type=head -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n default $HEAD_POD -- ray job list

# View HEAD POD logs
kubectl logs -n default $HEAD_POD -f
```

## Cleanup Resources

### One-Click Cleanup (Recommended)

Use the `clean-demo.sh` script to automatically clean up all deployed resources:

```bash
# Use default configuration file
./clean-demo.sh

# Use custom configuration file
./clean-demo.sh -c /path/to/my-config.env

# Also uninstall HPC controller and KubeRay operator
SKIP_UNINSTALL_HPC=false SKIP_UNINSTALL_KUBERAY=false ./clean-demo.sh
```

The `clean-demo.sh` script automatically executes the following steps:

1. **Prerequisites Check**: Check dependency tools and kubectl contexts
2. **Delete Training Resources**: Delete RayCluster, wait for pods cleanup (40s)
3. **Cleanup KubeRay**: Optionally delete KubeRay operator (requires setting `SKIP_UNINSTALL_KUBERAY=false`)
4. **Delete Inference Service**: Delete vLLM service, wait for pods cleanup (40s)
5. **Cleanup HPC**: Optionally delete HPC controller (requires setting `SKIP_UNINSTALL_HPC=false`)
6. **Uninstall Kubeocean**: Call `uninstall-tke.sh` to complete cluster unbinding and cleanup

**Notes:**

- By default, HPC controller and KubeRay operator **will NOT be uninstalled** (`SKIP_UNINSTALL_HPC=true`, `SKIP_UNINSTALL_KUBERAY=true`)
- If these components are still being used by other applications, keep the default settings
- The script will automatically wait for pods to be deleted, up to 40 seconds

### Manual Cleanup

If you need to manually clean up resources, follow these steps in order:

```bash
# 1. Delete RayCluster and training job
kubectl delete -f verl-raycluster.yaml -n default --context manager-admin-$MANAGER_CLUSTER_ID

# 2. (Optional) Delete KubeRay Operator
helm uninstall kuberay-operator -n default --kube-context manager-admin-$MANAGER_CLUSTER_ID

# 3. Delete vLLM inference service
kubectl delete -f is-qwen2-5-05b-vllm.yaml -n default --context worker-admin-$WORKER_CLUSTER_ID

# 4. (Optional) Delete HPC controller
# Need to use tccli command to delete addon

# 5. Uninstall Kubeocean
cd ../installation-tke
./uninstall-tke.sh
```

## Configuration

### Required Configuration

| Configuration | Description |
|---------------|-------------|
| `MANAGER_REGION` | Manager cluster region |
| `MANAGER_CLUSTER_ID` | Manager cluster ID |
| `MANAGER_SUBNET_ID` | Manager cluster subnet ID (for internal network access) |
| `WORKER_REGION` | Worker cluster region |
| `WORKER_CLUSTER_ID` | Worker cluster ID |
| `WORKER_SUBNET_ID` | Worker cluster subnet ID (for internal network access) |

### Optional Configuration

| Configuration | Default Value | Description |
|---------------|---------------|-------------|
| `WORKER_KUBECONFIG` | `/tmp/kubeconfig-worker` | Worker cluster kubeconfig path |
| `WORKER_CLUSTER_NAME` | `example-cluster` | Worker cluster name (for binding) |
| `SKIP_MANAGER_UNINSTALL` | `false` | Whether to skip manager uninstall |
| `SKIP_INSTALL_HPC` | `false` | Whether to skip tke-hpc-controller installation |
| `SKIP_CURRENT_TIME_CHECKING` | `false` | Whether to skip time window checking. When `false`, training jobs can only be submitted between 18:00-08:00; when `true`, skips time check and clears RLP timeWindows in worker cluster |
| `SKIP_UNINSTALL_HPC` | `true` | Whether to skip HPC controller uninstall during cleanup |
| `SKIP_UNINSTALL_KUBERAY` | `true` | Whether to skip KubeRay operator uninstall during cleanup |

## Troubleshooting

### 1. Dependency Tools Not Installed

If prompted that dependency tools are missing, please install:

```bash
# Install tccli
pip install tccli

# Configure tccli
tccli configure

# Install jq (Ubuntu/Debian)
sudo apt-get install jq

# Install kubectl
# Reference: https://kubernetes.io/docs/tasks/tools/

# Install helm
# Reference: https://helm.sh/docs/intro/install/
```

### 2. Cluster Connection Failed

Ensure:
- tccli credentials are correctly configured
- Cluster ID and region are correct
- Subnet ID is correct (for internal network access)
- Current machine can access Tencent Cloud VPC internal network

### 3. ResourceLeasingPolicy Does Not Exist

If prompted that RLP does not exist, it may be because Kubeocean installation is not complete. Please check:
- Whether the installation script executed successfully
- Whether the worker cluster is correctly bound

### 4. Time Window Check Failed

If prompted "current time is not within allowed time window" when submitting training job:

**Reason**: To protect online inference services, training jobs can only be submitted between 18:00-08:00 by default.

**Solutions**:
1. Wait until 18:00 to submit training jobs (recommended for production environments)
2. Or set `SKIP_CURRENT_TIME_CHECKING="true"` in `config.env` to skip time checking (suitable for test environments)

**Note**: When `SKIP_CURRENT_TIME_CHECKING="true"` is set, the script will automatically clear the timeWindows restrictions in the RLP of the worker cluster.

## Notes

1. **Network Access**: Scripts need to access Tencent Cloud API and cluster internal network, ensure network connectivity
2. **Resource Requirements**: Training and inference services require GPU resources, ensure the cluster has sufficient GPU nodes
3. **Time Consumption**: Complete deployment process may take 10-20 minutes, please be patient
4. **Cost Considerations**: Deployment will create cloud resources such as load balancers, please note the cost

## References

- [Kubeocean Main Documentation](../../../README.md)
- [Installation Guide](../installation-tke/README.md)
- [vLLM Documentation](https://docs.vllm.ai/)
- [KubeRay Documentation](https://docs.ray.io/en/latest/cluster/kubernetes/index.html)
- [VERL Documentation](https://github.com/volcengine/verl)
