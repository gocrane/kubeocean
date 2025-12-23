---
cwd: ../
---

# 快速开始

本文档介绍了：

- 在本地的 KIND（kubernetes in docker）集群中部署 kubeocean 组件
- 将两个业务集群绑定进 kubeocean，并抽取算力资源形成虚拟算力节点
- 在算力节点上创建 Pod，并能正常工作

## 环境要求

- git
- kubectl v1.28+
- docker
- go v1.24+
- helm v3

## 构建环境并部署 kubeocean 组件

1. 克隆代码仓库并进入目录

```sh
git clone https://github.com/gocrane/kubeocean
cd kubeocean
```

2. 修改 inotify 内核参数以支持 KIND 多集群

```sh
sudo sysctl fs.inotify.max_user_watches=524288
sudo sysctl fs.inotify.max_user_instances=512
```

3. 在本地构建3个 KIND 集群

```sh
make kind-create-all
```

以上命令会在本地创建3个 k8s 集群，名称分别为 kubeocean-manager，kubeocean-worker1 和 kubeocean-worker2。
可以使用以下命令切换不同集群的 context

```sh
# CLUSTER_NAME 可为 kubeocean-manager，kubeocean-worker1 和 kubeocean-worker2
export CLUSTER_NAME=kubeocean-worker1
kubectl config use-context kind-$CLUSTER_NAME
```

4. 部署 kubernetes-intranet 和 kube-dns-intranet Service

```sh
make kind-deploy-pre
```

以上命令会在上述创建的 kubeocean-manager 集群中部署 kubernetes-intranet 和 kube-dns-intranet Service，为 kubeocean 组件部署和使用做准备。

5. 在 kubeocean-manager 集群部署 kubeocean 组件

```sh
# 加载镜像
KIND_CLUSTER_NAME=kubeocean-manager make kind-load-images
# 切换到 manager 集群并部署组件
kubectl config use-context kind-kubeocean-manager
# 获取当前版本
version=$(git describe --tags --always --dirty)-amd64

# 使用 helm 安装组件
helm upgrade --install kubeocean charts/kubeocean \
--set global.imageRegistry="ccr.ccs.tencentyun.com/tke-eni-test" \
--set manager.image.tag=${version} \
--set syncer.image.tag=${version} \
--set proxier.image.tag=${version} \
--wait
# 或者使用预置 make 命令安装
INSTALL_IMG_TAG=${version} make install-manager
```

## 绑定业务集群并抽取算力节点

0. 设置环境变量

```sh
export CLUSTER_NAME=kubeocean-worker1
export CLUSTERID=cls-worker1
# 将CLUSTER_NAME设为kubeocean-worker2，CLUSTERID设为cls-worker2，再重新执行即可完成第二个业务集群注册
```

1. 在 worker 集群部署 kubeocean-worker

```sh
kubectl config use-context kind-$CLUSTER_NAME
# 使用 helm 安装
helm upgrade --install kubeocean-worker charts/kubeocean-worker --wait
# 或使用预置 make 命令安装
make install-worker
```

2. 提取 kubeocean-worker 的 kubeconfig

```sh
# 使用脚本提取 kubeconfig
bash hack/kubeconfig.sh kubeocean-syncer kubeocean-worker /tmp/kubeconfig-$CLUSTER_NAME
# 用对应 docker 容器的地址替换 APIServer 的 localhost 地址
WORKER1_IP=$(docker inspect $CLUSTER_NAME-control-plane --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
sed -i "s|server:.*|server: \"https://${WORKER1_IP}:6443\"|" /tmp/kubeconfig-$CLUSTER_NAME
```

3. 在 manager 集群中创建相关 secret

```sh
kubectl config use-context kind-kubeocean-manager
kubectl -nkubeocean-system create secret generic $CLUSTER_NAME-kubeconfig --from-file=kubeconfig=/tmp/kubeconfig-$CLUSTER_NAME
```

4. 绑定 worker 集群

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

在 manager 集群中创建上述 clusterbinding 对象：

```sh
kubectl config use-context kind-kubeocean-manager
kubectl apply -f cb.yaml
```

上述命令执行完成后，可查看对应 clusterbinding 的状态是否为 Ready：

```sh
kubectl get cb cb-$CLUSTER_NAME
```

执行结果预期为：

```sh
NAME                   PHASE   AGE
cb-kubeocean-worker1   Ready   Xs
```

同时，集群绑定后，kubeocean-system namespace 下会同步创建对应的 worker 和 proxier pod，可以通过以下命令查看：

```sh
kubectl -nkubeocean-system get po -owide
```

5. 抽取算力资源，形成虚拟节点

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

在 worker1 集群中创建上述 ResourceLeasingPolicy 对象，抽取算力节点

```sh
kubectl config use-context kind-$CLUSTER_NAME
kubectl apply -f rlp.yaml
```

上述命令执行完后，可在 manager 集群中观察算力节点是否正常抽取：

```sh
kubectl config use-context kind-kubeocean-manager
kubectl get node
```

若有 vnode 开头的节点创建，则说明算力资源抽取成功：

```sh
NAME                                          STATUS   ROLES           AGE   VERSION
kubeocean-manager-control-plane               Ready    control-plane   92m   v1.28.0
kubeocean-manager-worker                      Ready    <none>          91m   v1.28.0
kubeocean-manager-worker2                     Ready    <none>          91m   v1.28.0
vnode-cls-worker1-kubeocean-worker1-worker    Ready    <none>          5m   v1.28.0
vnode-cls-worker1-kubeocean-worker1-worker2   Ready    <none>          5m   v1.28.0
```

## 创建和部署样例 Pod

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

在 manager 集群中部署上述 job，可封锁非虚拟节点从而达到更好的验证效果：

```sh
# 拉取镜像
docker pull busybox:latest
bin/kind load docker-image busybox:latest --name kubeocean-worker1
bin/kind load docker-image busybox:latest --name kubeocean-worker2
# 部署 job
kubectl config use-context kind-kubeocean-manager
kubectl cordon kubeocean-manager-control-plane kubeocean-manager-worker kubeocean-manager-worker2
kubectl create -f job.yaml
```

部署完之后，可用 `kubectl` 查看结果

```sh
kubectl get po -owide -w
```

可以观察到该 job 能够正常运行和结束：

```sh
NAME             READY   STATUS              RESTARTS   AGE   IP       NODE                                          NOMINATED NODE   READINESS GATES
test-job-9ln8m   0/1     ContainerCreating   0          3s    <none>   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   1/1     Running             0          8s    10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          28s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          29s   <none>       vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          30s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
test-job-9ln8m   0/1     Completed           0          30s   10.242.1.2   vnode-cls-worker1-kubeocean-worker1-worker2   <none>           <none>
```
