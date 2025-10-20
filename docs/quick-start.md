# Quick Start

## Environment Requirements

At least two Kubernetes clusters are required: one computing cluster and multiple worker clusters. You can directly use TKE clusters. Kubernetes version must be at least 1.28+.

## Deployment Example

**In the worker cluster:**

1. Create permissions: `helm install kubeocean-worker charts/kubeocean-worker`
2. Obtain credentials: `bash hack/kubeconfig.sh kubeocean-syncer kubeocean-worker /tmp/kubeconfig.buz.1`
3. Create ResourceLeasingPolicy: `kubectl create -f examples/resourceleasingpolicy_sample.yaml`

**In the computing cluster:**

1. Install kubeocean components: `helm install kubeocean charts/kubeocean`
2. Set credentials: `kubectl create secret generic -n kubeocean-system worker-cluster-kubeconfig --from-file=kubeconfig=/tmp/kubeconfig.buz.1`
3. Create ClusterBinding: `kubectl create -f examples/clusterbinding_sample.yaml`

