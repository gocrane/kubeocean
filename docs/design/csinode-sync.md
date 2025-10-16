# CSINode Synchronization Functionality

## Overview

The PhysicalCSINodeReconciler module implements synchronization functionality from physical cluster CSINodes to virtual cluster CSINodes. This module works in coordination with PhysicalNodeReconciler, ensuring that CSINode objects in the virtual cluster maintain lifecycle consistency with corresponding virtual nodes.

## Functional Features

### 1. Automatic Synchronization
- Monitor CSINode object changes in physical clusters
- Automatically create, update, and delete corresponding CSINode objects in virtual clusters
- Maintain consistency with virtual node lifecycle

### 2. Dependency Validation
- Validate if virtual nodes exist
- Validate if virtual node labels match current physical cluster and node
- Ensure accuracy of CSINode synchronization

### 3. Label and Annotation Management
- Automatically add Kubeocean management labels
- Record synchronization time and physical cluster information
- Support secure deletion with UID validation

## Architecture Design

### Component Relationships
```
PhysicalNodeReconciler
    ↓ (reference)
PhysicalCSINodeReconciler
    ↓ (monitor)
Physical Cluster CSINode
    ↓ (synchronize)
Virtual Cluster CSINode
```

### Synchronization Process

1. **Creation Process**
   - PhysicalNodeReconciler creates virtual node
   - Call PhysicalCSINodeReconciler.CreateVirtualCSINode()
   - Check if physical CSINode exists
   - Create virtual CSINode and add labels and annotations

2. **Update Process**
   - Monitor physical CSINode changes
   - Validate virtual node label matches
   - Update virtual CSINode spec and annotations

3. **Deletion Process**
   - PhysicalNodeReconciler deletes virtual node
   - Call PhysicalCSINodeReconciler.DeleteVirtualCSINode()
   - Validate if CSINode is managed by Kubeocean
   - Securely delete virtual CSINode

## Labels and Annotations

### Labels
- `kubeocean.io/managed-by`: Identifies managed by Kubeocean
- `kubeocean.io/cluster-binding`: Cluster binding name
- `kubeocean.io/physical-cluster-id`: Physical cluster ID
- `kubeocean.io/physical-node-name`: Physical node name

> **Note**: These label constants are defined in `api/v1beta1/constants.go`, ensuring consistency throughout the project.

### Annotations
- `kubeocean.io/last-sync-time`: Last synchronization time
- `kubeocean.io/physical-cluster-name`: Physical cluster name
- `kubeocean.io/physical-csinode-uid`: Physical CSINode UID

## Configuration

### RBAC Permissions
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubeocean-csinode-controller
rules:
- apiGroups: ["storage.k8s.io"]
  resources: ["csinodes"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

### Controller Configuration
- Maximum concurrent reconciliations: 1
- Synchronization interval: 300 seconds
- Support graceful shutdown

## Testing

### Unit Testing
- CSINode creation and deletion functionality
- Label and annotation building
- Virtual node label validation

### Integration Testing
- Coordinated work with PhysicalNodeReconciler
- Complete lifecycle management
- Error handling and recovery

## Usage Examples

### Automatic Synchronization
When a CSINode exists in the physical cluster, the system automatically creates the corresponding virtual CSINode:

```yaml
# Physical Cluster CSINode
apiVersion: storage.k8s.io/v1
kind: CSINode
metadata:
  name: worker-node-1
spec:
  drivers:
  - name: csi-driver
    nodeID: worker-node-1
```

```yaml
# Automatically Created Virtual CSINode
apiVersion: storage.k8s.io/v1
kind: CSINode
metadata:
  name: vnode-worker-node-1
  labels:
    kubeocean.io/managed-by: kubeocean
    kubeocean.io/cluster-binding: my-cluster
    kubeocean.io/physical-cluster-id: cluster-1
    kubeocean.io/physical-node-name: worker-node-1
  annotations:
    kubeocean.io/last-sync-time: "2025-08-22T10:41:06Z"
    kubeocean.io/physical-cluster-name: my-cluster
    kubeocean.io/physical-csinode-uid: "12345678-1234-1234-1234-123456789abc"
spec:
  drivers:
  - name: csi-driver
    nodeID: worker-node-1
```

## Troubleshooting

### Common Issues

1. **Virtual CSINode Not Created**
   - Check if physical CSINode exists
   - Validate if virtual node labels are correct
   - Review controller logs

2. **Synchronization Failure**
   - Check RBAC permissions
   - Validate cluster binding configuration
   - Confirm network connectivity is normal

3. **Deletion Failure**
   - Check if CSINode is managed by Kubeocean
   - Validate UID matches
   - Review deletion permissions

### Log Examples
```
INFO    Creating virtual CSINode
INFO    Successfully created virtual CSINode
INFO    Virtual node validation failed
ERROR   Failed to create virtual CSINode
```

