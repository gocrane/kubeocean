# Kubeocean Manager

Kubeocean Manager is the central control plane component of the Kubeocean system that manages cluster bindings and resource leasing policies.

## Features

### Leader Election
- **High Availability**: Supports multiple replicas with automatic leader election
- **Configurable Parameters**: Customizable lease duration, renew deadline, and retry period
- **Graceful Failover**: Automatic failover when the current leader becomes unavailable
- **Metrics**: Exposes leader election status via Prometheus metrics

### Controller Management
- **ClusterBinding Controller**: Manages physical cluster registrations and validations
- **ResourceLeasingPolicy Controller**: Handles resource extraction policies and time windows
- **Event Recording**: Records Kubernetes events for all major operations
- **Status Management**: Maintains detailed status and conditions for all resources

### Monitoring and Observability
- **Prometheus Metrics**: Exposes comprehensive metrics for monitoring
- **Health Checks**: Provides liveness and readiness probes
- **Structured Logging**: JSON-formatted logs with configurable levels
- **Event Recording**: Kubernetes events for operational visibility

### Security
- **RBAC**: Minimal required permissions with proper role-based access control
- **Service Account**: Dedicated service account with least privilege principle
- **TLS Support**: Webhook server with TLS encryption
- **Security Context**: Non-root execution with security constraints

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Kubeocean Manager                         │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────────┐    ┌─────────────────────────────────┐ │
│  │ Leader Election │    │        Controllers             │ │
│  │                 │    │  ┌─────────────────────────────┐ │ │
│  │ • Lease Mgmt    │    │  │ ClusterBinding Controller   │ │ │
│  │ • Failover      │    │  │ • Validation               │ │ │
│  │ • Status        │    │  │ • Status Management        │ │ │
│  └─────────────────┘    │  │ • Event Recording          │ │ │
│                         │  └─────────────────────────────┘ │ │
│  ┌─────────────────┐    │  ┌─────────────────────────────┐ │ │
│  │    Metrics      │    │  │ResourceLeasingPolicy Ctrl  │ │ │
│  │                 │    │  │ • Policy Validation        │ │ │
│  │ • Prometheus    │    │  │ • Reference Checking       │ │ │
│  │ • Health Checks │    │  │ • Event Recording          │ │ │
│  └─────────────────┘    │  └─────────────────────────────┘ │ │
│                         └─────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

## Configuration

### Command Line Options

```bash
./kubeocean-manager \
  --leader-elect=true \
  --leader-election-id=kubeocean-manager-leader \
  --leader-election-lease-duration=15s \
  --leader-election-renew-deadline=10s \
  --leader-election-retry-period=2s \
  --metrics-bind-address=:8080 \
  --health-probe-bind-address=:8081
```

### Environment Variables

- `KUBERNETES_CLUSTER_DOMAIN`: Kubernetes cluster domain (default: cluster.local)

### Resource Requirements

**Recommended for Production:**
```yaml
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi
```

## Deployment

### Using Kustomize

```bash
# Install CRDs
make install

# Deploy the manager
make deploy

# Deploy with examples
kubectl apply -k examples/
```

### Manual Deployment

```bash
# Build the binary
make build

# Build Docker image
make docker-build

# Apply manifests
kubectl apply -k config/default
```

## Monitoring

### Prometheus Metrics

The manager exposes the following metrics on `:8080/metrics`:

- `kubeocean_cluster_bindings_total`: Total number of cluster bindings by phase
- `kubeocean_leader_election_status`: Leader election status (1 for leader, 0 for follower)
- `kubeocean_sync_duration_seconds`: Duration of synchronization operations
- `kubeocean_sync_errors_total`: Total number of synchronization errors

### Health Checks

- **Liveness**: `GET :8081/healthz`
- **Readiness**: `GET :8081/readyz`

## RBAC Permissions

The Kubeocean Manager requires the following permissions:

### Cluster-level Permissions
- `clusterbindings`, `resourceleasingpolicies`: Full CRUD operations
- `secrets`: Read access for kubeconfig secrets
- `events`: Create and patch for event recording
- `deployments`, `statefulsets`: Full CRUD for syncer management
- `serviceaccounts`, `roles`, `rolebindings`: Full CRUD for syncer RBAC
- `leases`: Full CRUD for leader election

### Namespace-level Permissions
- `configmaps`: Full CRUD in the manager namespace for leader election
- `events`: Create and patch for event recording

## Troubleshooting

### Common Issues

1. **Leader Election Failures**
   - Check RBAC permissions for leases
   - Verify namespace permissions
   - Check network connectivity between replicas

2. **Controller Errors**
   - Check logs for validation errors
   - Verify CRD installation
   - Check RBAC permissions

3. **Metrics Not Available**
   - Verify metrics port is not blocked
   - Check service configuration
   - Verify RBAC proxy configuration

### Debugging

Enable debug logging:
```bash
./kubeocean-manager --zap-devel --zap-log-level=debug
```

Check leader election status:
```bash
kubectl get lease kubeocean-manager-leader -n kubeocean-system
```

View controller events:
```bash
kubectl get events -n kubeocean-system --field-selector involvedObject.kind=ClusterBinding
```

## Examples

The `examples/` directory contains sample configurations:

- `clusterbinding_sample.yaml`: Example ClusterBinding with kubeconfig secret
- `resourceleasingpolicy_sample.yaml`: Example ResourceLeasingPolicy with time windows and resource limits

To deploy the examples:
```bash
kubectl apply -k examples/
```

## Development

### Running Locally

```bash
# Install CRDs
make install

# Run the manager
make run-manager
```

### Testing

```bash
# Run unit tests
make test

# Run with coverage
make test ARGS="-coverprofile=coverage.out"
```

### Building

```bash
# Build binary
make build

# Build Docker images
make docker-build

# Cross-platform build
make docker-buildx
```