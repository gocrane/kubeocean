# Kubeocean Architecture Documentation

## 1. Overview

Kubeocean is an open-source add-ons that supports converging cross-cluster computing resources in the form of virtual nodes within a Kubernetes cluster, much like an ocean. This cluster is also referred to as the computing cluster. All Pods deployed on virtual nodes managed by Kubeocean are automatically mapped and synchronized to the corresponding worker clusters for deployment, thereby enabling cross-cluster reuse of computing resources. The system adopts a controller pattern to implement dynamic resource extraction, virtual node management, and workload scheduling.

### Core Features

**Lightweight and Non-intrusive Worker Cluster Registration**

Worker clusters only need to deploy RBAC resources and provide the corresponding kubeconfig to complete registration, without deploying additional components. Kubeocean leverages the granted permissions to automatically complete cluster connection, virtual node registration, and other functions.

**Flexible and Dynamic Resource Extraction and Constraints**

Each worker cluster can flexibly configure the node scope from which computing resources can be extracted, the size and scope of extracted resources, and the time window during which resources can be extracted through `resourceLeasingPolicy`. For example: limit extraction to a maximum of 50% of CPU resources (to avoid affecting online services); only allow resource extraction during weekday nights, available all day on weekends (tidal reuse); only extract node resources with "GPU model A10" (to match model training requirements).

**Global Unified Optimal Scheduling**

Each node with cross-cluster resource extraction is registered as a virtual node on a one-to-one basis, enabling the computing cluster to have a globally unified scheduling view, achieving globally optimal scheduling strategies, minimizing resource fragmentation, and improving resource utilization.

**Native and Seamless Workload Deployment**

Deploying workload in the computing cluster managed by Kubeocean requires no modifications. The component automatically implements native Kubernetes capabilities, deploying, running, and recycling Pods on virtual nodes and their dependent resources (such as configmaps, secrets, etc.) across clusters.

**Minimal Permission Security Control**

Pods mapped and created by Kubeocean in worker clusters are centralized in a single namespace, and all operations on worker clusters are based on the minimal permission kubeconfig granted during registration, minimizing the impact on worker clusters.

**High Availability Design and Fault Isolation**

All components support multi-replica deployment and Leader Election mechanism, while each physical cluster is managed by an independent Syncer instance, ensuring that single point failures do not affect the overall system.

## 2. Architecture Diagram

Data Flow Architecture:

![Data Flow](images/kubeocean-data-flow.png)

Control Flow Architecture:

![Control Flow](images/kubeocean-control-flow.png)

## 3. Component Introduction

### 3.1 Kubeocean Manager

**Primary Responsibility**: Central control plane component responsible for managing cluster bindings

**Core Functions**:
- Monitor ClusterBinding resource changes
- Automatically create and manage Kubeocean Syncer components
- Manage cluster binding lifecycle

**Deployment Method**: Supports multi-replica deployment

### 3.2 Kubeocean Syncer

**Primary Responsibility**: Responsible for bidirectional synchronization between specific physical clusters and virtual clusters

**Core Functions**:
- Each instance is dedicated to synchronizing work for one physical cluster
- Supports multi-replica deployment through Leader Election mechanism
- Contains Bottom-up Syncer and Top-down Syncer submodules
- Independent fault recovery and scaling capabilities

**Deployment Method**: One Syncer instance per physical cluster, supports multi-replica deployment

### 3.3 Kubeocean Proxier

**Primary Responsibility**: Request proxy component responsible for forwarding Kubelet API requests from virtual nodes to physical nodes

**Core Functions**:
- Listen to requests on port 10250 (Kubelet port) on virtual nodes
- Proxy requests to the actual physical nodes in worker clusters
- Implement native Kubernetes capabilities such as `kubectl logs`, `kubectl exec`, etc.
- Handle authentication and authorization for cross-cluster requests
- Support TLS-encrypted connections

**Key Features**:
- Transparent proxy without modifying client behavior
- Support multiple concurrent connections
- Automatic routing to the correct physical node based on virtual node mapping
- Maintain connection stability and handle network failures gracefully

**Deployment Method**: One Proxier instance per physical cluster, supports multi-replica deployment

## 4. Submodule Detailed Description

### 4.1 Kubeocean Manager Submodules

#### 4.1.1 ClusterBinding Controller

**Responsibility**: Manage physical cluster registration and validation

**Main Functions**:
- Handle CRUD operations for ClusterBinding resources
- Validate cluster connectivity and permissions
- Create corresponding Kubeocean Syncer instances for each ClusterBinding
- Manage Syncer configuration and status
- Handle cluster binding lifecycle management

**Key Features**:
- Automatically create Syncer's Deployment, ServiceAccount, ClusterRole, and other resources
- Support retry mechanism for cluster connection failures
- Provide detailed status management and event recording



### 4.2 Kubeocean Syncer Submodules

#### 4.2.1 BottomUp Syncer

**Responsibility**: Resource synchronization from physical clusters to virtual clusters

**Main Functions**:
- Monitor physical cluster node and Pod status changes
- Calculate extractable resources based on ResourceLeasingPolicy
- Create and update virtual nodes
- Synchronize Pod status to virtual clusters

**Included Submodules**:

##### 4.2.1.1 PhysicalNodeReconciler

**Responsibility**: Physical node controller, responsible for virtual node creation and management

**Main Functions**:
- Monitor physical cluster node changes
- Calculate extractable resources based on ResourceLeasingPolicy
- Create and update virtual nodes
- Manage virtual node lifecycle
- Handle node deletion and resource reclamation

**Key Features**:
- Support node selectors and resource policies
- Automatically calculate virtual node resource capacity
- Handle graceful reclamation of node deletion

##### 4.2.1.2 PhysicalPodReconciler

**Responsibility**: Physical Pod controller, responsible for Pod status synchronization

**Main Functions**:
- Monitor physical cluster Pod status changes
- Synchronize Pod status to virtual clusters
- Validate Pod Kubeocean management labels
- Handle Pod deletion and status updates

**Key Features**:
- Only synchronize Kubeocean-managed Pods
- Ensure idempotency of status synchronization
- Handle graceful processing of Pod deletion

##### 4.2.1.3 PhysicalCSINodeReconciler

**Responsibility**: Physical CSI node controller, responsible for storage-related node information synchronization

**Main Functions**:
- Monitor physical cluster CSINode changes
- Synchronize CSI node information to virtual clusters
- Manage virtual CSI node lifecycle
- Handle storage driver information synchronization

**Key Features**:
- Support dynamic discovery of storage drivers
- Ensure storage functionality availability
- Handle cleanup of CSI node deletion

##### 4.2.1.4 ResourceLeasingPolicyReconciler

**Responsibility**: Resource leasing policy controller, responsible for policy application and resource calculation

**Main Functions**:
- Monitor ResourceLeasingPolicy changes
- Trigger node re-evaluation
- Apply resource policies to physical nodes
- Manage policy lifecycle

**Key Features**:
- Support time windows and resource limits
- Automatically trigger node re-evaluation
- Handle resource reclamation on policy deletion

##### 4.2.1.5 LeaseController

**Responsibility**: Lease controller, responsible for virtual node lease management

**Main Functions**:
- Create and manage leases for each virtual node
- Periodically renew leases to ensure node active status
- Handle lease expiration and node cleanup
- Support graceful lease termination

**Key Features**:
- Automatic lease renewal mechanism
- Support lease expiration handling
- Graceful termination and cleanup

#### 4.2.2 TopDown Syncer

**Responsibility**: Resource synchronization from virtual clusters to physical clusters

**Main Functions**:
- Monitor resource creation in virtual clusters
- Map resources to target physical clusters
- Handle resource name conflicts and namespace mapping
- Maintain resource mapping relationships

**Included Submodules**:

##### 4.2.2.1 VirtualPodReconciler

**Responsibility**: Virtual Pod controller, responsible for Pod creation and synchronization

**Main Functions**:
- Monitor virtual cluster Pod creation
- Create corresponding Pods in physical clusters
- Handle Pod deletion and lifecycle management
- Maintain mapping relationships between virtual and physical Pods

**Key Features**:
- Support bidirectional Pod mapping
- Handle graceful cleanup of Pod deletion
- Ensure idempotency of Pod creation
- Support Pod status synchronization

##### 4.2.2.2 VirtualConfigMapReconciler

**Responsibility**: Virtual ConfigMap controller, responsible for ConfigMap synchronization

**Main Functions**:
- Monitor virtual cluster ConfigMap creation
- Create corresponding ConfigMaps in physical clusters
- Handle ConfigMap updates and deletion
- Maintain ConfigMap mapping relationships

**Key Features**:
- Support bidirectional ConfigMap synchronization
- Handle conflict resolution for ConfigMap updates
- Ensure data consistency

##### 4.2.2.3 VirtualSecretReconciler

**Responsibility**: Virtual Secret controller, responsible for Secret synchronization

**Main Functions**:
- Monitor virtual cluster Secret creation
- Create corresponding Secrets in physical clusters
- Handle Secret updates and deletion
- Maintain Secret mapping relationships

**Key Features**:
- Support bidirectional Secret synchronization
- Handle secure transmission of sensitive data
- Ensure Secret data consistency

##### 4.2.2.4 VirtualPVCReconciler

**Responsibility**: Virtual PVC controller, responsible for PersistentVolumeClaim synchronization

**Main Functions**:
- Monitor virtual cluster PVC creation
- Validate physical cluster PVC existence
- Handle PVC status synchronization
- Maintain PVC mapping relationships

**Key Features**:
- Only synchronize vPod-associated PVCs
- Validate PVC binding status
- Ensure storage resource consistency

##### 4.2.2.5 VirtualPVReconciler

**Responsibility**: Virtual PV controller, responsible for PersistentVolume synchronization

**Main Functions**:
- Monitor virtual cluster PV creation
- Validate physical cluster PV existence
- Handle PV status synchronization
- Maintain PV mapping relationships

**Key Features**:
- Only synchronize vPod-associated PVs
- Validate PV availability
- Ensure storage resource consistency

### 4.3 Core API Resources

#### 4.3.1 ClusterBinding

**Purpose**: Define binding relationships between physical clusters and virtual clusters

**Main Fields**:
- `clusterID`: Unique cluster identifier
- `secretRef`: Secret reference containing kubeconfig
- `nodeSelector`: Node selector
- `mountNamespace`: Resource mount namespace
- `serviceNamespaces`: List of service synchronization namespaces



### 4.4 Key Labels and Annotations

#### 4.4.1 Management Labels
- `kubeocean.io/managed-by`: Identifies resources managed by Kubeocean
- `kubeocean.io/cluster-binding`: Associated cluster binding
- `kubeocean.io/physical-cluster-id`: Physical cluster ID
- `kubeocean.io/physical-node-name`: Physical node name

#### 4.4.2 Mapping Annotations
- `kubeocean.io/physical-pod-namespace`: Physical Pod namespace
- `kubeocean.io/physical-pod-name`: Physical Pod name
- `kubeocean.io/physical-pod-uid`: Physical Pod UID
- `kubeocean.io/virtual-pod-namespace`: Virtual Pod namespace
- `kubeocean.io/virtual-pod-name`: Virtual Pod name
- `kubeocean.io/virtual-pod-uid`: Virtual Pod UID

#### 4.4.3 Synchronization Annotations
- `kubeocean.io/last-sync-time`: Last synchronization time
- `kubeocean.io/policies-applied`: List of applied policies
- `kubeocean.io/expected-metadata`: Expected metadata

### 4.5 High Availability Design

#### 4.5.1 Fault Isolation
- Independent Syncer instances for each physical cluster
- Single Syncer failure does not affect other clusters
- Support automatic Syncer restart and recovery

#### 4.5.2 State Management
- Use Kubernetes native state management mechanisms
- Support graceful deletion and cleanup of resources
- Provide detailed error information and retry mechanisms

#### 4.5.3 High Availability Deployment
- Kubeocean Manager supports multi-replica deployment
- Each Syncer supports multi-replica deployment
- Support automatic component scaling

### 4.6 Monitoring and Observability

#### 4.6.1 Metrics Monitoring
- Prometheus metrics exposed at `:8080/metrics`
- Includes cluster binding, synchronization latency, error counts, and other metrics
- Support custom metrics and alerting rules

#### 4.6.2 Health Checks
- Liveness probe: `:8081/healthz`
- Readiness probe: `:8081/readyz`
- Support graceful startup and shutdown

#### 4.6.3 Logging
- Structured log format
- Support different log levels
- Include component, operation, and other contextual information

### 4.7 Security Design

#### 4.7.1 RBAC Permissions
- Principle of least privilege
- Configure dedicated ServiceAccount for each component
- Support permission separation and fine-grained control

#### 4.7.2 Network Security
- Support TLS-encrypted inter-cluster communication
- Use Secrets to store sensitive kubeconfig information
- Support network policies and access control

#### 4.7.3 Resource Isolation
- Run components in dedicated namespaces
- Support resource quotas and limits
- Ensure secure isolation between components

