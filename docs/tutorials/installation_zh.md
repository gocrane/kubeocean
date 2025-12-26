# 安装

## 环境要求

- 需要至少一个 kubernetes 集群作为虚拟算力集群，以及一个 kubernetes 集群作为工作集群。
- 可以使用 `kubectl` 访问集群
- 其他要求参考：[要求](requirements_zh.md)

## 用 helm 安装 kubeocean 组件

1. 克隆代码仓库并进入目录

```sh
git clone https://github.com/gocrane/kubeocean
cd kubeocean
```

2. 在算力集群中部署 kubeocean 组件

```sh
helm upgrade --install kubeocean charts/kubeocean
```

## 部署 kubernetes-intranet 和 kube-dns-intranet

### 部署 kubernetes-intranet

kubeocean 组件要求虚拟算力集群的 apiserver 给其他工作集群提供内网访问，目前的方案是需要提供名为 `kubernetes-intranet` 的 LoadBalancer 类型的服务。
在 [TKE(Tencent Kubernetes Engine)](https://cloud.tencent.com/product/tke) 集群中，可以在集群控制台开启为 APIServer 开启内网访问，如下图所示：

![k8s-svc](../images/k8s-svc.png)

或者使用腾讯云 CLI 工具 `tccli` 调用云 API 开启内网访问

```sh
# 设置调用集群的地域、集群ID和子网ID
export REGION=ap-guangzhou
export CLUSTER_ID=cls-abcdefgh
export SUBNET_ID=subnet-abcdefgh
TENCENTCLOUD_REGION="$REGION" tccli tke CreateClusterEndpoint \
        --ClusterId "$CLUSTER_ID" \
        --SubnetId "$SUBNET_ID" \
        --IsExtranet false
```

### 部署 kube-dns-intranet

kubeocean 组件要求虚拟算力集群的 `kube-dns` 给其他工作集群提供内网访问，需要部署名为 `kube-dns-intranet` 的 LoadBalancer 类型的服务。

在 [TKE(Tencent Kubernetes Engine)](https://cloud.tencent.com/product/tke) 集群中，可以使用下述 YAML 创建该服务，需要设置内网负载均衡使用的子网`：

```sh
# 用集群所在 VPC 的子网ID设置内网负载均衡所在的子网
export SUBNET_ID=subnet-abcdefgh
cat > tke-dns-svc.yaml <<EOF
apiVersion: v1
kind: Service
metadata:
  annotations:
    service.cloud.tencent.com/direct-access: "true"
    service.cloud.tencent.com/pass-to-target: "true"
    service.kubernetes.io/qcloud-loadbalancer-internal-subnetid: $SUBNET_ID
  name: kube-dns-intranet
  namespace: kube-system
spec:
  allocateLoadBalancerNodePorts: true
  externalTrafficPolicy: Cluster
  internalTrafficPolicy: Cluster
  ipFamilies:
  - IPv4
  ipFamilyPolicy: SingleStack
  ports:
  - name: dns
    port: 53
    protocol: UDP
    targetPort: 53
  - name: dns-tcp
    port: 53
    protocol: TCP
    targetPort: 53
  selector:
    k8s-app: kube-dns
  sessionAffinity: None
  type: LoadBalancer
EOF
```

在 TKE 集群中创建部署上述 YAML：

```sh
# 创建部署 Service
kubectl create -f tke-dns-svc.yaml
```