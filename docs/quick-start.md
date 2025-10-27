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
```
git clone https://github.com/gocrane/kubeocean
cd kubeocean
```

2. Modify inotify kernel parameters to support KIND multi-cluster
```
sudo sysctl fs.inotify.max_user_watches=524288
sudo sysctl fs.inotify.max_user_instances=512
```

3. Build 3 KIND clusters locally
```
make kind-create-all
```
The above command will create 3 k8s clusters locally, named kubeocean-manager, kubeocean-worker1 and kubeocean-worker2.
You can use the following command to switch between different cluster contexts:
```
# <clusterName> can be kubeocean-manager, kubeocean-worker1 and kubeocean-worker2
kubectl config use-context kind-<clusterName>
```

4. Deploy kubernetes-intranet and kube-dns-intranet Services
```
make kind-deploy-pre
```
The above command will deploy kubernetes-intranet and kube-dns-intranet Services in the created kubeocean-manager cluster to prepare for kubeocean component deployment and usage.

5. Deploy kubeocean components in kubeocean-manager cluster
```
# Load images
KIND_CLUSTER_NAME=kubeocean-manager make kind-load-images
# Switch to manager cluster and deploy components
kubectl config use-context kind-kubeocean-manager
# Install components using helm
version=$(git describe --tags --always --dirty)
helm upgrade --install kubeocean charts/kubeocean \
--set global.imageRegistry="ccr.ccs.tencentyun.com/tke-eni-test" \
--set manager.image.tag=${version} \
--set syncer.image.tag=${version} \
--set proxier.image.tag=${version} \
--wait
# Or use preset make command to install
make install-manager
```

## Bind Worker Clusters and Extract Computing Nodes

**Note: Replace kubeocean-worker1 with kubeocean-worker2 in the following commands to complete worker2 cluster binding**

1. Deploy kubeocean-worker in worker cluster
```
kubectl config use-context kind-kubeocean-worker1
# Install using helm
helm upgrade --install kubeocean-worker charts/kubeocean-worker --wait
# Or use preset make command to install
make install-worker
```

2. Extract kubeconfig from kubeocean-worker
```
# Use script to extract kubeconfig
bash hack/kubeconfig.sh kubeocean-syncer kubeocean-worker /tmp/kubeconfig-worker1
# Replace APIServer's localhost address with corresponding docker container address
WORKER1_IP=$(docker inspect kubeocean-worker1-control-plane --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
sed -i "s|server:.*|server: \"https://${WORKER1_IP}:6443\"|" /tmp/kubeconfig-worker1
```

3. Create related secrets in manager cluster
```
kubectl config use-context kind-kubeocean-manager
kubectl -nkubeocean-system create secret generic worker1-cluster-kubeconfig --from-file=kubeconfig=/tmp/kubeconfig-worker1
```

4. Bind worker cluster
```
# cb1.yaml
apiVersion: cloud.tencent.com/v1beta1
kind: ClusterBinding
metadata:
  name: cb-worker1
  namespace: kubeocean-system
spec:
  clusterID: cls-worker1
  mountNamespace: kubeocean-worker
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubeocean.io/node-type
        operator: In
        values:
        - worker
  secretRef:
    name: worker1-cluster-kubeconfig
    namespace: kubeocean-system
```
Create the above clusterbinding object in manager cluster:
```
kubectl config use-context kind-kubeocean-manager
kubectl apply -f cb1.yaml
```
After the above command is executed, you can check if the corresponding clusterbinding status is Ready:
```
kubectl get cb cb-worker1
```
Expected execution result:
```
NAME         CLUSTERID     PHASE
cb-worker1   cls-worker1   Ready
```
At the same time, after cluster binding, corresponding worker and proxier pods will be synchronously created in the kubeocean-system namespace, which can be viewed with the following command:
```
kubectl -nkubeocean-system get po -owide
```

5. Extract computing resources to form virtual nodes
```
# rlp1.yaml
apiVersion: cloud.tencent.com/v1beta1
kind: ResourceLeasingPolicy
metadata:
  name: rlp-worker1
spec:
  cluster: cb-worker1
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
```
Create the above ResourceLeasingPolicy object in worker1 cluster to extract computing nodes:
```
kubectl config use-context kind-kubeocean-worker1
kubectl apply -f rlp1.yaml
```
After the above command is executed, you can observe in the manager cluster whether computing nodes are extracted normally:
```
kubectl config use-context kind-kubeocean-manager
kubectl get node
```
If nodes starting with vnode are created, it means computing resource extraction is successful:
```
NAME                                          STATUS   ROLES           AGE   VERSION
kubeocean-manager-control-plane               Ready    control-plane   92m   v1.28.0
kubeocean-manager-worker                      Ready    <none>          91m   v1.28.0
kubeocean-manager-worker2                     Ready    <none>          91m   v1.28.0
vnode-cls-worker1-kubeocean-worker1-worker    Ready    <none>          5m   v1.28.0
vnode-cls-worker1-kubeocean-worker1-worker2   Ready    <none>          5m   v1.28.0
```

## Create and Deploy Sample Pod

```
# job.yaml
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
```
Deploy the above job in manager cluster. You can cordon non-virtual nodes for better verification effect:
```
# Pull image
docker pull busybox:latest
bin/kind load docker-image busybox:latest --name kubeocean-worker1
bin/kind load docker-image busybox:latest --name kubeocean-worker2
# Deploy job
kubectl config use-context kind-kubeocean-manager
kubectl cordon kubeocean-manager-control-plane kubeocean-manager-worker kubeocean-manager-worker2
kubectl create -f job.yaml
```
Use `kubectl get po -owide -w` to view the results. You can observe that the job can run and complete normally:
```
NAME             READY   STATUS              RESTARTS   AGE   IP       NODE                                          NOMINATED NODE   READINESS GATES
test-job-9ln8m   0/1     ContainerCreating   0          3s    <none>   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   1/1     Running             0          8s    10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          28s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          29s   <none>       vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          30s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          30s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
```