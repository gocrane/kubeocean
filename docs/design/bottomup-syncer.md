# BottomUp Syncer Component Documentation

## 1. Module Overview

### 1.1 Component Overview

BottomUp Syncer is one of the core synchronization components of the Kubeocean system, responsible for resource synchronization from physical clusters to virtual computing clusters. It implements the Kubernetes Controller pattern, monitoring resource changes in physical clusters and synchronizing relevant information to virtual clusters, providing real-time resource status information for the virtual computing cluster.

### 1.2 Main Responsibilities and Functions

- **Physical Node Synchronization**: Monitor physical cluster node changes, calculate extractable resources based on ResourceLeasingPolicy, create and update virtual nodes
- **Pod Status Synchronization**: Monitor Pod status changes managed by Kubeocean in physical clusters, synchronize status to virtual clusters
- **CSI Node Synchronization**: Synchronize physical cluster CSI node information to virtual clusters, ensure storage functionality availability
- **Resource Policy Management**: Monitor ResourceLeasingPolicy changes, trigger node re-evaluation and resource calculation
- **Lease Management**: Create and manage leases for each virtual node, ensure node active status
- **Node Re-evaluation**: Support triggering re-evaluation and resource calculation of specific nodes based on policy changes

## 2. Included Submodules and Related Introduction

### 2.1 PhysicalNodeReconciler

**Responsibility**: Physical node controller, responsible for virtual node creation and management

**Main Functions**:
- Monitor physical cluster node changes, filter nodes based on ClusterBinding node selectors
- Calculate extractable resource amounts based on ResourceLeasingPolicy
- Create and update virtual nodes, including resource capacity, labels, and annotations
- Manage time windows and node taints, handle policy time constraints
- Start and manage virtual node lease controllers

**Key Features**:
- Support priority handling of multiple resource policies
- Intelligent time window management and taint control
- Dynamic resource calculation and virtual node lifecycle management

### 2.2 PhysicalPodReconciler

**Responsibility**: Physical Pod controller, responsible for Pod status synchronization

**Main Functions**:
- Monitor physical cluster Pod status changes, only process Kubeocean-managed Pods
- Validate required annotations and labels of physical Pods
- Synchronize Pod status to corresponding virtual Pods
- Handle Pod deletion and status updates
- Maintain mapping relationships between physical and virtual Pods

**Key Features**:
- Only synchronize Kubeocean-managed Pods, avoid interfering with other workloads
- Bidirectional mapping validation, ensure data consistency
- Graceful status synchronization and error handling

### 2.3 PhysicalCSINodeReconciler

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

### 2.4 ResourceLeasingPolicyReconciler

**Responsibility**: Resource leasing policy controller, responsible for policy application and resource calculation

**Main Functions**:
- Monitor ResourceLeasingPolicy changes
- Validate policy time windows and resource limit configurations
- Trigger node re-evaluation and resource calculation
- Manage policy lifecycle and status updates

**Key Features**:
- Support validation of time windows and resource limits
- Automatically trigger node re-evaluation
- Handle resource reclamation on policy deletion

### 2.5 LeaseController

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

## 3. Submodule Call Relationship Diagram

```mermaid
flowchart TD
    subgraph BUS["BottomUp Syncer"]
        BUC[BottomUp Syncer Core]
    end
    
    subgraph Controllers["Controllers"]
        PNR[PhysicalNodeReconciler]
        PPR[PhysicalPodReconciler]
        PCR[PhysicalCSINodeReconciler]
        RLR[ResourceLeasingPolicyReconciler]
    end
    
    subgraph Managers["Managers"]
        PM[Physical Manager]
        VM[Virtual Manager]
    end
    
    subgraph Lease["Lease Management"]
        LC[LeaseController]
    end
    
    %% Core setup
    BUC --> PNR
    BUC --> PPR
    BUC --> PCR
    BUC --> RLR
    
    %% Manager connections
    PNR --> PM
    PNR --> VM
    PPR --> PM
    PPR --> VM
    PCR --> PM
    PCR --> VM
    RLR --> VM
    
    %% Lease controller
    PNR --> LC
    LC --> VM
    
    %% Policy interactions
    RLR -.->|Trigger re-evaluation| PNR
    PNR -.->|Get policy| RLR
    
    %% Data flow
    PM -->|Physical resource status| PNR
    PNR -->|Virtual node| VM
    PM -->|Pod status| PPR
    PPR -->|Status sync| VM
    PM -->|CSI info| PCR
    PCR -->|Virtual CSI| VM
```

## 4. PhysicalNodeReconciler Workflow

### 4.1 Main Workflow Diagram

```mermaid
flowchart TD
    A[Start Reconcile] --> B{Physical node exists?}
    B -->|No| C[Handle node deletion]
    B -->|Yes| D{Node being deleted?}
    D -->|Yes| C
    D -->|No| E[Get ClusterBinding]
    E --> F{Node matches selector?}
    F -->|No| C
    F -->|Yes| G[Get applicable policies]
    G --> H{Has matching policy?}
    H -->|Yes| I[Select earliest policy]
    H -->|No| J[Use all remaining resources]
    I --> K{Within time window?}
    K -->|No| L[Handle outside time window]
    K -->|Yes| M[Remove time window taint]
    J --> N[Calculate available resources]
    L --> N
    M --> N
    N --> O{Calculation successful?}
    O -->|No| P[Return error]
    O -->|Yes| Q[Create or update virtual node]
    Q --> R{Operation successful?}
    R -->|No| S[Return error]
    R -->|Yes| T[Start lease controller]
    T --> U[Set resync interval]
    U --> V[End]
    C --> V
```

### 4.2 Handle Node Deletion Workflow

```mermaid
flowchart TD
    A[Start handling node deletion] --> B[Check ClusterID]
    B --> C{ClusterID exists?}
    C -->|No| D[Return error]
    C -->|Yes| E[Generate virtual node name]
    E --> F[Get virtual node]
    F --> G{Virtual node exists?}
    G -->|No| H[Log and end]
    G -->|Yes| I{Already has deletion taint?}
    I -->|No| J[Add deletion taint]
    I -->|Yes| K[Immediately force evict all Pods]
    J --> L[Check if taint already exists]
    L --> M{Taint exists?}
    M -->|Yes| N[Use existing node directly]
    M -->|No| O[Create deletion taint object]
    O --> P[Set taint key-value pair]
    P --> Q[Add taint to node taint list]
    Q --> R[Set deletion time annotation]
    R --> S[Update virtual node]
    S --> T{Update successful?}
    T -->|No| U[Return update error]
    T -->|Yes| V[Log taint addition success]
    V --> K
    N --> K
    K --> W[Force evict all Pods]
    W --> II[Check Pods on node]
    II --> LL{Active Pods exist?}
    LL -->|Yes| MM[Set recheck after 10 seconds]
    LL -->|No| OO[Stop lease controller]
    OO --> WW[Delete virtual node]
    WW --> ZZ{Delete successful?}
    ZZ -->|No| AAA[Return deletion error]
    ZZ -->|Yes| BBB[Log deletion success]
    BBB --> CCC[End]
    MM --> CCC
```

### 4.3 Create or Update Virtual Node Workflow

```mermaid
flowchart TD
    A[Start creating or updating virtual node] --> B[Generate virtual node name]
    B --> C[Build virtual node labels and annotations]
    C --> D[Convert physical node taints]
    D --> E[Calculate available resources]
    E --> F[Create virtual node object]
    F --> G[Save expected metadata to annotations]
    G --> H[Check if virtual node exists]
    H --> I{Virtual node exists?}
    I -->|No| J[Create virtual node]
    I -->|Yes| K[Get existing virtual node]
    K --> L[Preserve user customizations]
    L --> M[Update virtual node status]
    M --> N{Taints changed?}
    N -->|Yes| O[Update taints separately]
    N -->|No| P[Start lease controller]
    O --> P
    J --> P
    P --> Q[End]

    subgraph "Preserve User Customizations"
        L1[Get previously expected metadata] --> L2[Create current expected metadata]
        L2 --> L3[Save current expected metadata]
        L3 --> L4[Merge user-defined labels]
        L4 --> L5[Merge user-defined annotations]
        L5 --> L6[Merge user-defined taints]
        L6 --> L7[Complete user customization preservation]
    end
```

### 4.4 Handle Outside Time Window Workflow

```mermaid
flowchart TD
    A[Start handling outside time window] --> B[Get virtual node]
    B --> C{Virtual node exists?}
    C -->|No| D[Set default resync time]
    C -->|Yes| E[Check existing outside time window taint]
    D --> Z[End]
    E --> F[Get policy graceful reclaim period]
    F --> G{Force reclaim flag?}
    G -->|No| H[Set NoSchedule taint effect]
    G -->|Yes| I{Graceful reclaim period is 0?}
    I -->|Yes| J[Set NoExecute taint effect]
    I -->|No| K{Existing taint exists?}
    K -->|No| L[Set NoSchedule taint effect]
    K -->|Yes| M{Taint effect is NoExecute?}
    M -->|Yes| N[Set default resync time]
    M -->|No| O{Taint has timestamp?}
    O -->|No| P[Set NoExecute taint effect]
    O -->|Yes| Q[Calculate taint addition time]
    Q --> R{Exceeded graceful reclaim period?}
    R -->|Yes| S[Set NoExecute taint effect]
    R -->|No| T[Calculate remaining time]
    T --> U[Set resync after remaining time]
    H --> V[Add or update taint]
    J --> V
    L --> V
    P --> V
    S --> V
    V --> W[Update virtual node]
    W --> X[Set resync time]
    X --> Z[End]
    N --> Z
    U --> Z
```

## 5. PhysicalPodReconciler Main Workflow

```mermaid
flowchart TD
    A[Start Reconcile] --> B{Physical Pod exists?}
    B -->|No| C[End]
    B -->|Yes| D{Is Kubeocean-managed Pod?}
    D -->|No| C
    D -->|Yes| E{Has required annotations?}
    E -->|No| F[Delete physical Pod]
    E -->|Yes| G[Get and validate virtual Pod]
    G --> H{Virtual Pod exists and matches?}
    H -->|No| F
    H -->|Yes| I{Annotation points to current physical Pod?}
    I -->|No| F
    I -->|Yes| J[Sync physical Pod to virtual Pod]
    J --> K{Sync successful?}
    K -->|No| L[Return error]
    K -->|Yes| M[End]
    F --> O{Delete successful?}
    O -->|No| P[Return error]
    O -->|Yes| M
```

## 6. Key Logic Implementation Details

### 6.1 Resource Calculation Mechanism

PhysicalNodeReconciler implements an intelligent resource calculation mechanism that dynamically calculates extractable resources based on ResourceLeasingPolicy:

- **Policy Matching**: Match applicable ResourceLeasingPolicy based on node selectors
- **Priority Handling**: When multiple policies match, select the earliest created policy
- **Time Window Management**: Check policy time windows, add taints when outside windows
- **Resource Calculation**: Calculate extractable CPU, memory, and other resources based on policy resource limits
- **Dynamic Adjustment**: Respond to policy changes in real-time, recalculate resource allocation

### 6.2 Node Lifecycle Management

A complete virtual node lifecycle management mechanism is implemented:

- **Creation Process**: Create virtual nodes based on physical nodes and policies, set correct labels and annotations
- **Update Mechanism**: Monitor physical node changes, update virtual node resource information in real-time
- **Deletion Handling**: Gracefully delete virtual nodes when physical nodes are deleted or don't match selectors
- **Taint Management**: Dynamically manage node taints based on time window status
- **Lease Maintenance**: Maintain active leases for each virtual node, ensure node availability

### 6.3 Pod Status Synchronization Mechanism

PhysicalPodReconciler implements precise Pod status synchronization:

- **Filtering Mechanism**: Only process Pods with Kubeocean management labels
- **Annotation Validation**: Validate that physical Pods contain required virtual Pod mapping annotations
- **Bidirectional Validation**: Ensure virtual Pod annotations correctly point to current physical Pod
- **Status Synchronization**: Synchronize physical Pod status information to virtual Pods
- **Cleanup Mechanism**: Automatically clean up physical Pods when mapping relationships become invalid

### 6.4 Policy Trigger Mechanism

ResourceLeasingPolicyReconciler implements an efficient policy trigger mechanism:

- **Change Detection**: Monitor policy create, update, and delete events
- **Node Lookup**: Find matching nodes based on policy node selectors
- **Re-evaluation**: Trigger re-evaluation and resource calculation of matching nodes
- **Status Updates**: Update policy status information, including time window status
- **Lifecycle Management**: Handle resource reclamation when policies are deleted

### 6.5 Lease Management Mechanism

LeaseController implements reliable lease management:

- **Automatic Renewal**: Periodically renew leases to ensure virtual node active status
- **Expiration Handling**: Handle lease expiration situations, ensure correct node status
- **Graceful Termination**: Support graceful termination and cleanup of lease controllers
- **Concurrency Control**: Handle concurrent operations of multiple lease controllers
- **Error Recovery**: Retry and recover when lease operations fail



