---
cwd: ../
---

# Quick Start

This document introduces:

- Deploying kubeocean components in a local KIND (kubernetes in docker) cluster
- Binding two worker clusters into kubeocean and extracting computing resources to form virtual computing nodes
- Creating Pods on computing nodes that can work normally

## Environment Requirements

- git
- kubectl v1.28+
- docker
- go v1.24+
- helm v3

## Build Environment and Deploy kubeocean Components

1. Clone the repository and enter the directory

```sh
git clone https://github.com/gocrane/kubeocean
cd kubeocean
```

2. Modify inotify kernel parameters to support KIND multi-cluster

```sh
sudo sysctl fs.inotify.max_user_watches=524288
sudo sysctl fs.inotify.max_user_instances=512
```

3. Build 3 KIND clusters locally

```sh
make kind-create-all
```

The above command will create 3 k8s clusters locally, named kubeocean-manager, kubeocean-worker1 and kubeocean-worker2.
You can use the following command to switch between different cluster contexts:

```sh
# CLUSTER_NAME 可为 kubeocean-manager，kubeocean-worker1 和 kubeocean-worker2
export CLUSTER_NAME=kubeocean-worker1
kubectl config use-context kind-$CLUSTER_NAME
```

4. Deploy kubernetes-intranet and kube-dns-intranet Services

```sh
make kind-deploy-pre
```

The above command will deploy kubernetes-intranet and kube-dns-intranet Services in the created kubeocean-manager cluster to prepare for kubeocean component deployment and usage.

5. Deploy kubeocean components in kubeocean-manager cluster

```sh
# Load images
KIND_CLUSTER_NAME=kubeocean-manager make kind-load-images
# Switch to manager cluster and deploy components
kubectl config use-context kind-kubeocean-manager
# Get current version
version=$(git describe --tags --always --dirty)-amd64

# Install components using helm
helm upgrade --install kubeocean charts/kubeocean \
--set global.imageRegistry="ccr.ccs.tencentyun.com/tke-eni-test" \
--set manager.image.tag=${version} \
--set syncer.image.tag=${version} \
--set proxier.image.tag=${version} \
--wait
# Or use preset make command to install
INSTALL_IMG_TAG=${version} make install-manager
```

## Bind Worker Clusters and Extract Computing Nodes

0. Set environment variables

```sh
export CLUSTER_NAME=kubeocean-worker1
export CLUSTERID=cls-worker1
# Set CLUSTER_NAME to kubeocean-worker2 and CLUSTERID to cls-worker2, then re-execute to complete the second worker cluster registration
```

1. Deploy kubeocean-worker in worker cluster

```sh
kubectl config use-context kind-$CLUSTER_NAME
# Install using helm
helm upgrade --install kubeocean-worker charts/kubeocean-worker --wait
# Or use preset make command to install
make install-worker
```

2. Extract kubeconfig from kubeocean-worker

```sh
# Use script to extract kubeconfig
bash hack/kubeconfig.sh kubeocean-syncer kubeocean-worker /tmp/kubeconfig-$CLUSTER_NAME
# Replace APIServer's localhost address with corresponding docker container address
WORKER1_IP=$(docker inspect $CLUSTER_NAME-control-plane --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
sed -i "s|server:.*|server: \"https://${WORKER1_IP}:6443\"|" /tmp/kubeconfig-$CLUSTER_NAME
```

3. Create related secrets in manager cluster

```sh
kubectl config use-context kind-kubeocean-manager
kubectl -nkubeocean-system create secret generic $CLUSTER_NAME-kubeconfig --from-file=kubeconfig=/tmp/kubeconfig-$CLUSTER_NAME
```

4. Bind worker cluster

```sh
cat > cb.yaml << EOF
apiVersion: cloud.tencent.com/v1beta1
kind: ClusterBinding
metadata:
  name: cb-$CLUSTER_NAME
  namespace: kubeocean-system
spec:
  clusterID: $CLUSTERID
  mountNamespace: kubeocean-worker
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubeocean.io/node-type
        operator: In
        values:
        - worker
  secretRef:
    name: $CLUSTER_NAME-kubeconfig
    namespace: kubeocean-system
EOF

```

Create the above clusterbinding object in manager cluster:

```sh
kubectl config use-context kind-kubeocean-manager
kubectl apply -f cb.yaml
```

After the above command is executed, you can check if the corresponding clusterbinding status is Ready:

```sh
kubectl get cb cb-$CLUSTER_NAME
```

Expected execution result:

```sh
NAME                   PHASE   AGE
cb-kubeocean-worker1   Ready   Xs
```

At the same time, after cluster binding, corresponding worker and proxier pods will be synchronously created in the kubeocean-system namespace, which can be viewed with the following command:

```sh
kubectl -nkubeocean-system get po -owide
```

5. Extract computing resources to form virtual nodes

```sh
cat > rlp.yaml << EOF
apiVersion: cloud.tencent.com/v1beta1
kind: ResourceLeasingPolicy
metadata:
  name: rlp-$CLUSTER_NAME
spec:
  cluster: cb-$CLUSTER_NAME
  forceReclaim: true
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubeocean.io/node-type
        operator: In
        values: ["worker"]
  timeWindows:
    - start: "08:01"
      end: "08:00" # 24h all days is allowed
  resourceLimits:
    - resource: cpu
      quantity: "4"
      percent: 80  # Take the smaller of 4 CPUs or 80% of available CPUs
    - resource: memory
      percent: 90  # Take 90% of available memory
EOF
```

Create the above ResourceLeasingPolicy object in worker1 cluster to extract computing nodes:

```sh
kubectl config use-context kind-$CLUSTER_NAME
kubectl apply -f rlp.yaml
```

After the above command is executed, you can observe in the manager cluster whether computing nodes are extracted normally:

```sh
kubectl config use-context kind-kubeocean-manager
kubectl get node
```

If nodes starting with vnode are created, it means computing resource extraction is successful:

```sh
NAME                                          STATUS   ROLES           AGE   VERSION
kubeocean-manager-control-plane               Ready    control-plane   92m   v1.28.0
kubeocean-manager-worker                      Ready    <none>          91m   v1.28.0
kubeocean-manager-worker2                     Ready    <none>          91m   v1.28.0
vnode-cls-worker1-kubeocean-worker1-worker    Ready    <none>          5m   v1.28.0
vnode-cls-worker1-kubeocean-worker1-worker2   Ready    <none>          5m   v1.28.0
```

## Create and Deploy Sample Pod

```sh
cat > job.yaml << EOF
kind: Job
apiVersion: batch/v1
metadata:
  name: test-job
spec:
  backoffLimit: 20
  activeDeadlineSeconds: 3600
  template:
    spec:
      restartPolicy: OnFailure
      containers:
      - image: busybox
        imagePullPolicy: IfNotPresent
        name: test-job
        command: ["sleep", "20"]
      tolerations:
      - operator: Exists
        key: kubeocean.io/vnode
EOF
```

Deploy the above job in manager cluster. You can cordon non-virtual nodes for better verification effect:

```sh
# Pull image
docker pull busybox:latest
bin/kind load docker-image busybox:latest --name kubeocean-worker1
bin/kind load docker-image busybox:latest --name kubeocean-worker2
# Deploy job
kubectl config use-context kind-kubeocean-manager
kubectl cordon kubeocean-manager-control-plane kubeocean-manager-worker kubeocean-manager-worker2
kubectl create -f job.yaml
```

After deployment, use `kubectl` to view the results

```sh
kubectl get po -owide -w
```

You can observe that the job can run and complete normally:

```sh
NAME             READY   STATUS              RESTARTS   AGE   IP       NODE                                          NOMINATED NODE   READINESS GATES
test-job-9ln8m   0/1     ContainerCreating   0          3s    <none>   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   1/1     Running             0          8s    10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          28s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          29s   <none>       vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          30s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          30s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
```