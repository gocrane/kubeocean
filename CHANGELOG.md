# Change Log

## v0.1.2

**Features**
- Support `/pods` interface for http server. [Proxier Server]
- Support hostNetwork for hostport fake pod. [Syncer Hostport]
- Support Optional configmap and secrets for virtual pod. [Syncer Pod]
- Support sync configmaps and secrets that pod needs even if configmaps and secrets lack managed labels. [Syncer Pod]
- Support directScheduling for physical pod. [Syncer Pod]
- Support building multi-arch images. [Build]
- Support running NodeConformance e2e tests on kind clusters. [Test]
- Support tke playbook and examples playbook. [Cookbook]

**Fixed Bugs and other changes**
- Fix pod with custom PriorityClass and PreemptionPolicy is Never. [Syncer Pod]
- Fix not labeling synced resources like configmaps when resources used by multi clusters. [Syncer Pod]
- Add direct-access and pass-to-target annotations when create kube-dns-intranet service. [Chore]
- Add ClusterID to clusterbindings CRD additionalPrinterColumns. [Charts]
- Add nodeAffinity for not to scheduled on vnode for manager, syncer and proxier. [Charts]
- Configure pod network routes for all KIND node containers. [Build Kind]
- Fix kind-load-images image names. [Build Kind]
- Update example label from role to kubeocean.io/role. [Chore]
- Delete physical pod when virtual pod not found. [Syncer Pod]
- Delete mismatched physical pod with mismatched virtual podUID. [Syncer Pod]
- Get pod object from cache.DeletedFinalStateUnknown event. [Syncer Pod]
- Add buildAndTest workflows for github actions. [CI]
- Add Release Charts workflows for github actions. [CI]
- Add make test-unit and print cover rate for go test. [Test]
- Update documentation for installation, tutorials and quick-start. [Docs]

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