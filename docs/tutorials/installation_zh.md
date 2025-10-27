# 安装

## 环境要求

- 需要至少一个 kubernetes 集群作为虚拟算力集群，以及一个 kubernetes 集群作为工作集群。
- 可以使用 `kubectl` 访问集群
- 其他要求参考：[要求](requirements_zh.md)

## 用 helm 安装 kubeocean 组件

1. 克隆代码仓库并进入目录
```
git clone https://github.com/gocrane/kubeocean
cd kubeocean
```
2. 在算力集群中部署 kubeocean 组件
```
helm upgrade --install kubeocean charts/kubeocean
```

## 部署 kubernetes-intranet 和 kube-dns-intranet

### 部署 kubernetes-intranet

kubeocean 组件要求虚拟算力集群的 apiserver 给其他工作集群提供内网访问，目前的方案是需要提供名为 `kubernetes-intranet` 的 LoadBalancer 类型的服务。
在 [TKE(Tencent Kubernetes Engine)](https://cloud.tencent.com/product/tke) 集群中，可以在集群控制台开启为 APIServer 开启内网访问，如下图所示：

![k8s-svc](../images/k8s-svc.png)

### 部署 kube-dns-intranet

kubeocean 组件要求虚拟算力集群的 `kube-dns` 给其他工作集群提供内网访问，需要部署名为 `kube-dns-intranet` 的 LoadBalancer 类型的服务。

在 [TKE(Tencent Kubernetes Engine)](https://cloud.tencent.com/product/tke) 集群中，可以使用下述 YAML 创建该服务，需要填写替换其中的 `<subnetId>`：
```
# k8s-dns-svc.yaml
apiVersion: v1
kind: Service
metadata:
  annotations:
    service.kubernetes.io/qcloud-loadbalancer-internal-subnetid: <subnetId>
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
```
在 TKE 集群中创建部署上述 YAML：
```
# 用集群所在 VPC 中的子网填写替换 <subnetId>
sed -i "s|<subnetId>|subnet-xxxxxxxx|" k8s-dns-svc.yaml
# 创建部署 Service
kubectl create -f k8s-dns-svc.yaml
```