# Binding Worker Clusters

This document covers:

- Binding worker clusters to virtual computing clusters where kubeocean components are deployed
- Extracting computing resources and generating virtual nodes in virtual computing clusters

## Environment Variables Setup

First, set some environment variables to simplify subsequent operations:

```sh
# Set cluster identifiers
export CLUSTER_NAME=example-cluster
export CLUSTER_ID=cls-example
# Set kubeconfig path
export WORKER_KUBECONFIG=/tmp/kubeconfig-worker
```

## Deploy kubeocean-worker in Worker Cluster

1. Clone the repository and enter the directory

```sh
git clone https://github.com/gocrane/kubeocean
cd kubeocean
```

2. Deploy kubeocean-worker in the worker cluster

```sh
# Install using helm
helm upgrade --install kubeocean-worker charts/kubeocean-worker
```

## Extract kubeconfig from kubeocean-worker

1. Extract kubeconfig

```sh
# Use script to extract kubeconfig
# Ensure current kubectl accesses the worker cluster
# This script will use the APIServer address configured in the current default kubeconfig
bash hack/kubeconfig.sh kubeocean-syncer kubeocean-worker $WORKER_KUBECONFIG
```

2. Create related secret in the computing cluster

```sh
# Switch kubectl to computing cluster
kubectl config use-context <computing-cluster-context>
kubectl -nkubeocean-system create secret generic ${CLUSTER_NAME}-kubeconfig --from-file=kubeconfig=$WORKER_KUBECONFIG
```

## Create ClusterBinding Object to Bind Worker Cluster

1. Create ClusterBinding object

```sh
# Use heredoc to create ClusterBinding YAML
cat > cb.yaml <<EOF
# ClusterBinding example, representing registering a worker cluster to a computing cluster
apiVersion: cloud.tencent.com/v1beta1
kind: ClusterBinding
metadata:
  name: $CLUSTER_NAME
spec:
  # Worker cluster ID, must be globally unique, TKE clusters recommend using actual cluster ID
  clusterID: $CLUSTER_ID
  # Namespace where Pod and other resources are mapped
  mountNamespace: kubeocean-worker
  # Worker node selector for registration
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubeocean.io/role
        operator: In
        values:
        - worker
  # Client config used by syncer, note correspondence with secret created in previous steps
  secretRef:
    name: ${CLUSTER_NAME}-kubeconfig
    namespace: kubeocean-system
EOF
```

Create the above ClusterBinding object in the computing cluster:

```sh
kubectl apply -f cb.yaml
```

2. Verify binding result

After executing the above command, check if the corresponding clusterbinding status is Ready:

```sh
kubectl get cb $CLUSTER_NAME
```

Expected execution result:

```
NAME              PHASE   AGE
example-cluster   Ready   10s
```

After cluster binding, corresponding syncer and proxier pods will be synchronously created under the kubeocean-system namespace, which can be viewed with the following command:

```sh
kubectl -nkubeocean-system get po -owide
```

## Extract Computing Resources and Generate Virtual Nodes

1. Create ResourceLeasingPolicy object in worker cluster

```sh
# Use heredoc to create ResourceLeasingPolicy YAML
cat > rlp.yaml <<EOF
# ResourceLeasingPolicy example, defining and configuring resource extraction strategy
apiVersion: cloud.tencent.com/v1beta1
kind: ResourceLeasingPolicy
metadata:
  name: ${CLUSTER_NAME}-policy
spec:
  # Associated clusterBinding, corresponding to the name of the ClusterBinding object created above
  cluster: $CLUSTER_NAME
  # Whether to force eviction outside time windows, non-eviction only adds no-schedule taint, forced eviction adds no-execute taint
  forceReclaim: true
  # Worker nodes matched by policy, if empty, matches all nodes
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubeocean.io/role
        operator: In
        values: ["worker"]
  # Time windows when policy takes effect, can define multiple time windows, if list is empty then defaults to full-time effect
  timeWindows:
      # Can define start and end times, if empty, then effective all day
    - start: "18:00"
      end: "08:00"
      # Can define which days of the week it's effective, if empty, then effective every day
      days: ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday"]
    - start: "00:00"
      end: "23:59"
      days: ["Saturday", "Sunday"]
  # Resource extraction limit strategy
  resourceLimits:
    # Can define extracted resource names, if resource doesn't appear here, then extract 100%
    - resource: cpu
      # Upper limit extracted from remaining available resources (by actual value)
      quantity: "4"
      # Upper limit extracted from remaining available resources (by percentage)
      # If both quantity and percent are set, take the smaller value
      percent: 80  # Take the smaller of 4 CPUs or 80% of available CPUs
    - resource: memory
      percent: 90  # Take 90% of available memory
EOF
```

Create the above ResourceLeasingPolicy object in the worker cluster to extract computing nodes:

```sh
# Switch kubectl to worker cluster
kubectl config use-context <worker-cluster-context>
kubectl apply -f rlp.yaml
# Add a label to nodes that expect to extract resources
kubectl label node <nodeName1> <nodeName2> kubeocean.io/role=worker
```

2. Verify resource extraction result

After executing the above command, observe in the computing cluster whether computing nodes are extracted normally:

```sh
# Switch kubectl to virtual computing cluster
kubectl config use-context <computing-cluster-context>
kubectl get node
```

If nodes starting with vnode are created, it indicates successful resource extraction:

```
NAME                                          STATUS   ROLES           AGE   VERSION
vnode-cls-example-node1                       Ready    <none>          5m    v1.28.0
vnode-cls-example-node2                       Ready    <none>          5m    v1.28.0
```

## Key Features

1. **ResourceLeasingPolicy Location**: Needs to be created in the worker cluster to take effect
2. **Uniqueness**: Only one ResourceLeasingPolicy object takes effect per worker cluster. If multiple are created, the earliest ResourceLeasingPolicy object takes effect
3. **nodeSelector**: Can be empty, if empty then matches all nodes
4. **timeWindows**: Can be empty, if empty then effective full-time
5. **resourceLimits**: Can be empty, resources not appearing in the list are extracted at 100% of remaining resources
