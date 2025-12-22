# Installation

## Environment Requirements

- Need at least one Kubernetes cluster as virtual computing cluster and one Kubernetes cluster as worker cluster
- Can access clusters using `kubectl`
- Other requirements refer to: [Requirements](requirements.md)

## Install kubeocean Components with Helm

1. Clone the repository and enter the directory

```sh
git clone https://github.com/gocrane/kubeocean
cd kubeocean
```

2. Deploy kubeocean components in the computing cluster

```sh
helm upgrade --install kubeocean charts/kubeocean
```

## Deploy kubernetes-intranet and kube-dns-intranet

### Deploy kubernetes-intranet

kubeocean components require the virtual computing cluster's apiserver to provide intranet access to other worker clusters. The current solution requires providing a LoadBalancer type service named `kubernetes-intranet`.
In [TKE (Tencent Kubernetes Engine)](https://cloud.tencent.com/product/tke) clusters, you can enable intranet access for APIServer in the cluster console, as shown in the figure below:

![k8s-svc](../images/k8s-svc.png)

Or use Tencent Cloud CLI tool `tccli` to call cloud API to enable intranet access

```sh
# Set the region, cluster ID and subnet ID
export REGION=ap-guangzhou
export CLUSTER_ID=cls-abcdefgh
export SUBNET_ID=subnet-abcdefgh
TENCENTCLOUD_REGION="$REGION" tccli tke CreateClusterEndpoint \
        --ClusterId "$CLUSTER_ID" \
        --SubnetId "$SUBNET_ID" \
        --IsExtranet false
```

### Deploy kube-dns-intranet

kubeocean components require the virtual computing cluster's `kube-dns` to provide intranet access to other worker clusters. A LoadBalancer type service named `kube-dns-intranet` needs to be deployed.

In [TKE (Tencent Kubernetes Engine)](https://cloud.tencent.com/product/tke) clusters, you can use the following YAML to create this service, need to set the subnet for the intranet load balancer:

```sh
# Set the subnet ID in the VPC where the cluster is located for the intranet load balancer
export SUBNET_ID=subnet-abcdefgh
cat > tke-dns-svc.yaml <<EOF
apiVersion: v1
kind: Service
metadata:
  annotations:
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

Create and deploy the above YAML in TKE cluster:

```sh
# Create and deploy Service
kubectl create -f tke-dns-svc.yaml
```
