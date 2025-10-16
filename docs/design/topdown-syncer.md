# TopDown Syncer Module Documentation

## 1. Module Overview

TopDown Syncer is one of the core components of the Kubeocean system, responsible for synchronizing resources from the virtual cluster to physical clusters. It implements top-down synchronization from virtual clusters to physical clusters, ensuring that resources created in the virtual cluster are correctly mapped and deployed to physical clusters.

### Main Responsibilities and Functions

- **Resource Synchronization**: Monitor resource changes in virtual clusters, synchronize virtual resources to physical clusters
- **State Management**: Maintain mapping relationships between virtual and physical resources
- **Lifecycle Management**: Handle creation, update, and deletion operations of virtual resources
- **Cross-cluster Coordination**: Establish resource mapping relationships between virtual and physical clusters
- **Annotation Management**: Maintain association relationships between resources through annotation mechanisms

## 2. Included Submodules and Related Introduction

### 2.1 VirtualPodReconciler

**Responsibility**: Responsible for synchronization management of virtual Pods, mapping Pods from virtual clusters to physical clusters.

**Main Functions**:
- Monitor Pod changes in virtual clusters
- Validate if Pods should be managed by the current cluster binding
- Create, update, and delete physical Pods
- Maintain mapping relationships between virtual and physical Pods
- Handle Pod status synchronization and lifecycle management

### 2.2 VirtualConfigMapReconciler

**Responsibility**: Responsible for synchronization management of virtual ConfigMaps, mapping ConfigMaps from virtual clusters to physical clusters.

**Main Functions**:
- Monitor ConfigMap changes in virtual clusters
- Validate if ConfigMaps should be managed by the current cluster binding
- Create, update, and delete physical ConfigMaps
- Maintain mapping relationships between virtual and physical ConfigMaps

### 2.3 VirtualSecretReconciler

**Responsibility**: Responsible for synchronization management of virtual Secrets, mapping Secrets from virtual clusters to physical clusters.

**Main Functions**:
- Monitor Secret changes in virtual clusters
- Validate if Secrets should be managed by the current cluster binding
- Create, update, and delete physical Secrets
- Maintain mapping relationships between virtual and physical Secrets
- Handle Secret data synchronization and validation

### 2.4 VirtualPVCReconciler

**Responsibility**: Responsible for synchronization management of virtual PVCs (PersistentVolumeClaims), mapping PVCs from virtual clusters to physical clusters.

**Main Functions**:
- Monitor PVC changes in virtual clusters
- Validate if PVCs should be managed by the current cluster binding
- Create, update, and delete physical PVCs
- Maintain mapping relationships between virtual and physical PVCs

### 2.5 VirtualPVReconciler

**Responsibility**: Responsible for synchronization management of virtual PVs (PersistentVolumes), mapping PVs from virtual clusters to physical clusters.

**Main Functions**:
- Monitor PV changes in virtual clusters
- Validate if PVs should be managed by the current cluster binding
- Create, update, and delete physical PVs
- Maintain mapping relationships between virtual and physical PVs



## 3. VirtualPodReconciler Workflow

### 3.1 Main Workflow

```mermaid
flowchart TD
    A[Start Reconcile] --> B[Get virtual Pod]
    B --> C{Virtual Pod exists?}
    C -->|No| D[End]
    C -->|Yes| E{Pod scheduled?}
    E -->|No| D
    E -->|Yes| F[Check if managed by current cluster]
    F --> G{Should manage?}
    G -->|No| D
    G -->|Yes| H{Virtual Pod being deleted?}
    H -->|Yes| I[Handle virtual Pod deletion]
    H -->|No| J[Check if physical Pod exists]
    J --> K{Physical Pod exists?}
    K -->|No| L[Handle physical Pod creation]
    K -->|Yes| M[Check if UID annotation needs update]
    M --> N{UID needs update?}
    N -->|Yes| O[Update UID annotation]
    N -->|No| P[No operation needed]
    O --> S[End]
    P --> S
    L --> L1{All mapping annotations exist?}
    L1 -->|Yes| L2[Set virtual Pod status to Failed]
    L1 -->|No| L3{Physical Pod UID empty and name empty?}
    L3 -->|Yes| L4[Generate physical Pod name mapping]
    L3 -->|No| L5{Physical Pod UID empty but other annotations exist?}
    L5 -->|Yes| L6[Create physical Pod]
    L5 -->|No| L7[End]
    L2 --> L8[End]
    L4 --> L9{Mapping generation successful?}
    L9 -->|No| U[Return error]
    L9 -->|Yes| L10[Wait for next sync]
    L10 --> S
    L6 --> L11[Sync dependent resources]
    L11 --> L12[Build physical Pod spec]
    L12 --> L13[Create physical Pod]
    L13 --> T
    L7 --> S
    T -->|No| U[Return error]
    T -->|Yes| V[Wait for BottomUp Syncer to update UID]
    V --> W[End]
    R --> X{Update successful?}
    X -->|No| Y[Return error]
    X -->|Yes| Z[End]
    I --> AA[Delete physical Pod]
    AA --> BB{Delete successful?}
    BB -->|No| CC[Return error]
    BB -->|Yes| DD[End]
```

### 3.2 Sync Dependent Resources Workflow

```mermaid
flowchart TD
    A[Start syncing dependent resources] --> B[Sync ConfigMaps]
    B --> C[Sync Secrets]
    C --> D[Sync PVCs]
    D --> E[Build resource mapping]
    E --> F[End]

    subgraph "Sync ConfigMaps"
        B1[Collect ConfigMap references in Pod] --> B2[Iterate all ConfigMap references]
        B2 --> B3[Collect from volumes]
        B3 --> B4[Collect from environment variables]
        B4 --> B5[Collect from init containers]
        B5 --> B6[Sync each ConfigMap]
        B6 --> B7[Collect ConfigMap mappings]
    end

    subgraph "Sync Secrets"
        C1[Collect Secret references in Pod] --> C2[Iterate all Secret references]
        C2 --> C3[Collect from volumes]
        C3 --> C4[Collect from environment variables]
        C4 --> C5[Collect from init containers]
        C5 --> C6[Collect from image pull secrets]
        C6 --> C7[Sync each Secret]
        C7 --> C8[Collect Secret mappings]
    end

    subgraph "Sync PVCs"
        D1[Collect PVC references in Pod] --> D2[Iterate all PVC references]
        D2 --> D3[Collect from volumes]
        D3 --> D4[Sync each PVC]
        D4 --> D5[Collect PVC mappings]
    end

    subgraph "Build Resource Mapping"
        E1[Create ConfigMap mapping] --> E2[Create Secret mapping]
        E2 --> E3[Create PVC mapping]
        E3 --> E4[Build complete resource mapping]
    end

    B --> B1
    C --> C1
    D --> D1
    E --> E1
```

### 3.3 Sync PVC Workflow

```mermaid
flowchart TD
    A[Start syncing PVC] --> B[Get virtual PVC]
    B --> C{Virtual PVC exists?}
    C -->|No| D[Return error]
    C -->|Yes| E{Check PVC status}
    E --> F{PVC bound?}
    F -->|No| G[Return error: PVC not bound]
    F -->|Yes| H{PVC has volumeName?}
    H -->|No| I[Return error: PVC has no volumeName]
    H -->|Yes| J[Sync associated PV]
    J --> K{PV sync successful?}
    K -->|No| L[Return error: PV sync failed]
    K -->|Yes| M[Generate physical PVC name]
    M --> N[Check if physical PVC exists]
    N --> O{Physical PVC exists?}
    O -->|Yes| P{Validate physical PVC ownership}
    O -->|No| Q[Update virtual PVC annotation]
    P --> R{Ownership validation passed?}
    R -->|No| S[Return error: Ownership conflict]
    R -->|Yes| T[Return physical PVC name]
    Q --> U[Create physical PVC]
    U --> V{Creation successful?}
    V -->|No| W[Return error: Creation failed]
    V -->|Yes| X[Return physical PVC name]

    subgraph "Sync Associated PV"
        J1[Get virtual PV] --> J2{PV has CSI nodePublishSecretRef?}
        J2 -->|Yes| J3[Sync CSI Secret]
        J2 -->|No| J4[Generate physical PV name]
        J3 --> J5{Secret sync successful?}
        J5 -->|No| J6[Return error]
        J5 -->|Yes| J7[Update PV Secret reference]
        J7 --> J8[Create physical PV]
        J4 --> J8
        J8 --> J9{PV creation successful?}
        J9 -->|No| J10[Return error]
        J9 -->|Yes| J11[Return physical PV name]
    end

    J --> J1
```

## 4. VirtualSecretReconciler Main Workflow

```mermaid
flowchart TD
    A[Start Reconcile] --> B[Get virtual Secret]
    B --> C{Virtual Secret exists?}
    C -->|No| D[End]
    C -->|Yes| E{Managed by Kubeocean?}
    E -->|No| D
    E -->|Yes| F{Belongs to current cluster?}
    F -->|No| D
    F -->|Yes| G[Get physical name and namespace]
    G --> H{Has physical name annotation?}
    H -->|No| D
    H -->|Yes| I[Check if physical Secret exists]
    I --> J{Physical Secret exists?}
    J -->|Yes| K[Validate physical Secret]
    J -->|No| L{Virtual Secret being deleted?}
    K --> L
    L -->|Yes| M[Handle virtual Secret deletion]
    L -->|No| N{Secret used by PV?}
    N -->|Yes| O[Skip creation]
    N -->|No| P[Create physical Secret]
    O --> Q[End]
    P --> R{Creation successful?}
    R -->|No| S[Return error]
    R -->|Yes| T[End]
    M --> U[Delete physical Secret]
    U --> V{Deletion successful?}
    V -->|No| W[Return error]
    V -->|Yes| X[End]
    J -->|No| Y{Virtual Secret being deleted?}
    Y -->|Yes| Z[Handle virtual Secret deletion]
    Y -->|No| AA[Check if physical Secret needs update]
    Z --> BB[Delete physical Secret]
    BB --> CC{Deletion successful?}
    CC -->|No| DD[Return error]
    CC -->|Yes| EE[End]
    AA --> FF{Needs update?}
    FF -->|Yes| GG[Update physical Secret]
    FF -->|No| HH[End]
    GG --> II{Update successful?}
    II -->|No| JJ[Return error]
    II -->|Yes| KK[End]
```

## 5. Key Logic Implementation Details

### 5.1 Resource Mapping Mechanism

TopDown Syncer implements a precise resource mapping mechanism, maintaining mapping relationships between virtual and physical resources through annotations, labels, finalizers, and name mapping rules.

#### 5.1.1 Pod Mapping Mechanism

**Virtual Pod Annotations**:
- `kubeocean.io/physical-pod-namespace`: Physical Pod namespace
- `kubeocean.io/physical-pod-name`: Physical Pod name
- `kubeocean.io/physical-pod-uid`: Physical Pod UID
- `kubeocean.io/last-sync-time`: Last sync time

**Physical Pod Annotations**:
- `kubeocean.io/virtual-pod-namespace`: Virtual Pod namespace
- `kubeocean.io/virtual-pod-name`: Virtual Pod name
- `kubeocean.io/virtual-pod-uid`: Virtual Pod UID

**Physical Pod Labels**:
- `kubeocean.io/managed-by`: Set to "kubeocean", identifies managed by Kubeocean
- Copy all labels from virtual Pod

#### 5.1.2 Dependent Resource Mapping Mechanism

**Virtual Resource Annotations**:
- `kubeocean.io/physical-name`: Physical resource name
- `kubeocean.io/physical-namespace`: Physical resource namespace

**Virtual Resource Labels**:
- `kubeocean.io/managed-by`: Set to "kubeocean"
- `kubeocean.io/synced-by-{clusterID}`: Identifies synced by specific cluster
- `kubeocean.io/used-by-pv`: If secret resource is used by PV, set to "true"

**Physical Resource Annotations**:
- `kubeocean.io/virtual-name`: Virtual resource name
- `kubeocean.io/virtual-namespace`: Virtual resource namespace

**Physical Resource Labels**:
- `kubeocean.io/managed-by`: Set to "kubeocean"
- Copy all labels from virtual resource

**Finalizer Mechanism**:
- Virtual resource: `kubeocean.io/finalizer-{clusterID}` - Ensure cleanup of physical resources when resource is deleted
- Physical resource: No finalizer added, managed by Kubernetes native mechanisms

#### 5.1.3 Name Mapping Rules

**Pod Name Mapping**:
- Format: `podName(first 30 chars)-md5(podNamespace+"/"+podName)`
- Example: Virtual Pod `my-app` in `default` namespace → Physical Pod `my-app-abc123def456`

**Dependent Resource Name Mapping**:
- Format: `resourceName(first 30 chars)-md5(resourceNamespace+"/"+resourceName)`
- Applies to ConfigMap, Secret, PVC, PV, and other resources
- Ensures name uniqueness and predictability

**Mapping Rule Characteristics**:
- **Uniqueness**: Ensure resources with the same name in different namespaces map to different names through MD5 hash
- **Predictability**: Same input always produces same output
- **Length Constraint**: Complies with Kubernetes resource name length limit (63 characters)
- **Conflict Avoidance**: Avoid name conflicts through namespace information

#### 5.1.4 Namespace Mapping

**Pod Namespace Mapping**:
- Virtual cluster namespace → ClusterBinding.Spec.MountNamespace
- All Pods map to the specified namespace in physical cluster

**Dependent Resource Namespace Mapping**:
- ConfigMap/Secret: Virtual namespace → ClusterBinding.Spec.MountNamespace
- PVC: Virtual namespace → ClusterBinding.Spec.MountNamespace
- PV: Cluster-level resource, no namespace mapping
- CSI Secret: Maintain original namespace (for PV reference)

#### 5.1.5 Mapping Relationship Maintenance

**Mapping on Creation**:
1. Generate physical resource name
2. Update virtual resource annotations and labels
3. Add finalizer
4. Create physical resource

**Mapping on Update**:
1. Check if mapping relationship is consistent
2. Synchronize resource content
3. Maintain mapping annotations

**Mapping on Deletion**:
1. Ensure cleanup of physical resources through finalizer
2. Remove mapping annotations
3. Delete virtual resource

### 5.2 Status Synchronization Mechanism

Implements real-time status synchronization:

- **Monitoring Mechanism**: Monitor change events of resources in virtual clusters
- **Validation Mechanism**: Validate if resources should be managed by current cluster binding
- **Synchronization Strategy**: Adopt different synchronization strategies based on resource types
- **Conflict Handling**: Handle status conflicts between virtual and physical resources

### 5.3 Lifecycle Management

Implements complete resource lifecycle management:

- **Creation Process**: Create corresponding physical resources based on virtual resources
- **Update Process**: Monitor virtual resource changes, synchronously update physical resources
- **Deletion Process**: Handle virtual resource deletion, clean up corresponding physical resources
- **Status Maintenance**: Maintain resource status information and mapping relationships





