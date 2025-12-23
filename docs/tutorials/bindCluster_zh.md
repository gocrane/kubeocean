# 绑定工作集群

本文档介绍了：

- 绑定工作集群到已部署 kubeocean 组件的虚拟算力集群中
- 抽取算力资源，并在虚拟算力集群中生成虚拟节点

## 环境变量设置

首先设置一些环境变量，简化后续操作：

```sh
# 设置集群标识
export CLUSTER_NAME=example-cluster
export CLUSTER_ID=cls-example
# 设置 kubeconfig 路径
export WORKER_KUBECONFIG=/tmp/kubeconfig-worker
```

## 在 worker 集群部署 kubeocean-worker

1. 克隆代码仓库并进入目录

```sh
git clone https://github.com/gocrane/kubeocean
cd kubeocean
```

2. 在 worker 集群中部署 kubeocean-worker

```sh
# 使用 helm 安装
helm upgrade --install kubeocean-worker charts/kubeocean-worker
```

## 提取 kubeocean-worker 的 kubeconfig

1. 提取 kubeconfig

```sh
# 使用脚本提取 kubeconfig
# 需要确保当前 kubectl 访问的是 worker 集群
# 该脚本会使用当前默认 kubeconfig 里配置的 APIServer 地址
bash hack/kubeconfig.sh kubeocean-syncer kubeocean-worker $WORKER_KUBECONFIG
```

2. 在算力集群中创建相关 secret

```sh
# kubectl 切换到算力集群
kubectl config use-context <computing-cluster-context>
kubectl -nkubeocean-system create secret generic ${CLUSTER_NAME}-kubeconfig --from-file=kubeconfig=$WORKER_KUBECONFIG
```

## 创建 ClusterBinding 对象以绑定 worker 集群

1. 创建 ClusterBinding 对象

```sh
# 使用 heredoc 创建 ClusterBinding YAML
cat > cb.yaml <<EOF
# ClusterBinding 样例，表示注册一个业务集群到算力集群中
apiVersion: cloud.tencent.com/v1beta1
kind: ClusterBinding
metadata:
  name: $CLUSTER_NAME
spec:
  # worker 集群ID，需保证全局唯一，TKE 集群推荐填写实际集群ID
  clusterID: $CLUSTER_ID
  # Pod 等资源映射到的 namespace
  mountNamespace: kubeocean-worker
  # 注册的工作节点选择器
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubeocean.io/role
        operator: In
        values:
        - worker
  # syncer 所使用的 client config，注意与前述步骤创建的 secret 对应
  secretRef:
    name: ${CLUSTER_NAME}-kubeconfig
    namespace: kubeocean-system
EOF
```

在算力集群中创建上述 ClusterBinding 对象：

```sh
kubectl apply -f cb.yaml
```

2. 验证绑定结果

上述命令执行完成后，可查看对应 clusterbinding 的状态是否为 Ready：

```sh
kubectl get cb $CLUSTER_NAME
```

执行结果预期为：

```
NAME              PHASE   AGE
example-cluster   Ready   10s
```

同时，集群绑定后，kubeocean-system namespace 下会同步创建对应的 syncer 和 proxier pod，可以通过以下命令查看：

```sh
kubectl -nkubeocean-system get po -owide
```

## 抽取算力资源，生成虚拟节点

1. 在工作集群创建 ResourceLeasingPolicy 对象

```sh
# 使用 heredoc 创建 ResourceLeasingPolicy YAML
cat > rlp.yaml <<EOF
# ResourceLeasingPolicy 样例，定义和配置资源抽取的策略
apiVersion: cloud.tencent.com/v1beta1
kind: ResourceLeasingPolicy
metadata:
  name: ${CLUSTER_NAME}-policy
spec:
  # 关联到的 clusterBinding，与前述创建的 ClusterBinding 对象的名称对应
  cluster: $CLUSTER_NAME
  # 时间窗外是否强制驱逐，不驱逐只会添加禁止调度的污点，强制驱逐会添加禁止执行的污点
  forceReclaim: true
  # 策略匹配的工作节点，若为空，则匹配所有节点
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: kubeocean.io/role
        operator: In
        values: ["worker"]
  # 策略生效的时间窗，可定义多个时间窗，若列表为空则默认全时生效
  timeWindows:
      # 可定义开始时间和结束时间，若为空，则全天生效
    - start: "18:00"
      end: "08:00"
      # 可定义一周哪天生效，若为空，则每天生效
      days: ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday"]
    - start: "00:00"
      end: "23:59"
      days: ["Saturday", "Sunday"]
  # 资源抽取上限策略
  resourceLimits:
    # 可定义抽取的资源名，若资源未出现在此处，则按 100% 抽取
    - resource: cpu
      # 从剩余可用资源中抽取的上限（按实际数值）
      quantity: "4"
      # 从剩余可用资源中抽取的上限（按百分比）
      # quantity 和 percent 同时设置会取较小值
      percent: 80  # Take the smaller of 4 CPUs or 80% of available CPUs
    - resource: memory
      percent: 90  # Take 90% of available memory
EOF
```

在 worker 集群中创建上述 ResourceLeasingPolicy 对象，抽取算力节点：

```sh
# kubectl 切换到 worker 集群
kubectl config use-context <worker-cluster-context>
kubectl apply -f rlp.yaml
# 给期望抽取资源的节点添加 label
kubectl label node <nodeName1> <nodeName2> kubeocean.io/role=worker
```

2. 验证资源抽取结果

上述命令执行完后，可在算力集群中观察算力节点是否正常抽取：

```sh
# kubectl 切换到虚拟算力集群
kubectl config use-context <computing-cluster-context>
kubectl get node
```

若有 vnode 开头的节点创建，则说明算力资源抽取成功：

```
NAME                                          STATUS   ROLES           AGE   VERSION
vnode-cls-example-node1                       Ready    <none>          5m    v1.28.0
vnode-cls-example-node2                       Ready    <none>          5m    v1.28.0
```

## 关键特性说明

1. **ResourceLeasingPolicy 位置**：需要在 worker 集群中创建才能生效
2. **唯一性**：每个 worker 集群只有一个 ResourceLeasingPolicy 对象生效，若创建了多个，则最早的 ResourceLeasingPolicy 对象生效
3. **nodeSelector**：可为空，若为空则匹配所有节点
4. **timeWindows**：可为空，若为空则全时生效
5. **resourceLimits**：可为空，列表中未出现的资源按照剩余资源 100% 抽取