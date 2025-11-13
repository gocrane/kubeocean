# Change Log

## v0.1.1

**Features**
- Support daemonset running by default when it is set by clusterbinding. [Syncer Pod]
- Support more dns hostAlises for 'kubernetes.default', 'kubernetes', 'kubernetes.default.svc.cluster.local'. [Syncer Pod]
- Support multiple volumes with projected ServiceAccountToken. [Syncer Pod]
- Support logging for each http request. [Proxier]
- Support synchronizing virtual pod annotations and labels to physical pod. [Syncer Pod]
- Support synchronizing virtual pod ActiveDeadlineSeconds to physical pod. [Syncer Pod]
- Support synchronizing virtual pod EphemeralContainers to physical pod. [Syncer Pod]

**Fixed Bugs and other changes**
- Fix nil pointer error for workQueue in hostport controller. [Syncer Hostport]
- Fix physical pod with dnsconfig ndots value to 5 and add namespace to 'searches'. [Syncer Pod]
- Add tcp 53 port mapping for coredns and kube-dns-intranet. [Build Kind]
- Support containers configmap and secrets of 'envFrom'. [Syncer Pod]
- Fix setup logger for proxier. [Proxier]
- Fix containerlogs proxy to flush when write. [Proxier]
- Truncate pod hostName when pod name is too long. [Syncer Pod]
- Delete unit tests that will get local kubeconfig. [Test Syncer]
- Use external kubeclient to build manager for tests. [Test Manager]
- ValidateContainerExists will also check container in EphemeralContainers. [Proxier]
- Add PodScheduled condition to hostport pod. [Syncer Hostport]