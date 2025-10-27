# Requirements

## Kubernetes Version Requirements

kubeocean components are built with controller-runtime v0.21.0 and client-go v0.33.3, and have been fully tested and validated on **Kubernetes v1.28**. Therefore, both the computing cluster and worker cluster where kubeocean components are deployed must be at least **v1.28** or higher. Other versions of Kubernetes clusters have not been fully validated and are not guaranteed to be supported.

## Network Requirements

- Direct network connectivity between Pod networks of computing cluster and worker cluster, and node network connectivity
- The computing cluster can provide LoadBalancer type services for `apiserver` and `kube-dns`, accessible by worker clusters

## Cluster Provider Requirements

Currently validated Kubernetes cluster providers:

- [TKE (Tencent Kubernetes Engine)](https://cloud.tencent.com/product/tke)
- Locally deployed Kubernetes clusters: Need to build `kubernetes-intranet` and `kube-dns-intranet` Services of type `LoadBalancer`. You can refer to the `make kind-deploy-pre` command solution in the project to build these two Services through nodePort or other methods.
