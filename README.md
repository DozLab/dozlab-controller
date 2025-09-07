# Dozlab Controller

Kubernetes controller for managing Dozlab custom resources and lab orchestration.

## Features

- **Custom Resource Definitions**: Manages LabSession CRDs
- **Lab Orchestration**: Automated deployment and lifecycle management
- **Resource Management**: Handles pods, services, and volumes for lab environments
- **Event-driven Architecture**: Responds to Kubernetes events
- **Multi-tenant Support**: Isolated lab environments per session

## Tech Stack

- **Language**: Go 1.23.1
- **Framework**: controller-runtime (Kubebuilder)
- **Kubernetes API**: k8s.io/client-go v0.31.3
- **Custom Resources**: k8s.io/api v0.31.3

## Architecture

The controller follows the Kubernetes operator pattern:

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   API Server    │────│   Controller    │────│  Lab Resources  │
│                 │    │                 │    │                 │
│ • LabSession    │    │ • Reconcile     │    │ • Pods          │
│   CRDs          │    │ • Watch Events  │    │ • Services      │
│ • Events        │    │ • Manage State  │    │ • Volumes       │
└─────────────────┘    └─────────────────┘    └─────────────────┘
```

## Custom Resources

### LabSession CRD

Defines lab environment specifications:

```yaml
apiVersion: dozlab.io/v1
kind: LabSession
metadata:
  name: session-123
spec:
  labId: "lab-golang-basics"
  userId: "user-456"
  image: "dozlab/golang-lab:latest"
  resources:
    requests:
      cpu: "500m"
      memory: "1Gi"
    limits:
      cpu: "2"
      memory: "4Gi"
  networking:
    ports:
      - port: 8080
        protocol: TCP
      - port: 22
        protocol: TCP
```

## Controller Logic

The controller reconciles LabSession resources by:

1. **Validation**: Ensures resource specifications are valid
2. **Pod Creation**: Creates multi-container pods with sidecars
3. **Service Creation**: Exposes lab services for external access
4. **Volume Management**: Sets up persistent and shared volumes
5. **Status Updates**: Maintains resource status and conditions
6. **Cleanup**: Removes resources when sessions end

## Getting Started

### Prerequisites

- Kubernetes cluster (v1.28+)
- kubectl configured
- Go 1.23.1+

### Installation

1. Clone the repository:
```bash
git clone <repository-url>
cd dozlab-controller
```

2. Install CRDs:
```bash
make install
```

3. Run locally (for development):
```bash
make run
```

4. Deploy to cluster:
```bash
make deploy IMG=your-registry/dozlab-controller:latest
```

### Configuration

Set environment variables:

```bash
# Kubernetes configuration
KUBECONFIG=/path/to/kubeconfig

# Controller settings
METRICS_BIND_ADDRESS=:8080
HEALTH_PROBE_BIND_ADDRESS=:8081
LEADER_ELECT=true

# Lab configuration
DEFAULT_NAMESPACE=dozlab-labs
LAB_IMAGE_REGISTRY=your-registry.com
SIDECAR_IMAGE=your-registry/terminal-sidecar:latest
```

## Development

### Project Structure

```
├── controllers/          # Controller reconciliation logic
│   ├── labsession_controller.go
│   └── types.go
├── deploy/              # Kubernetes manifests
│   └── deployment.yaml
├── main.go             # Controller entry point
├── go.mod              # Go modules
└── Makefile           # Build and deployment commands
```

### Building

```bash
# Build the binary
make build

# Build Docker image
make docker-build IMG=your-registry/dozlab-controller:latest

# Push to registry
make docker-push IMG=your-registry/dozlab-controller:latest
```

### Testing

```bash
# Run unit tests
make test

# Run integration tests (requires cluster)
make test-integration

# Generate manifests
make manifests

# Generate code
make generate
```

### Debugging

Enable debug logging:

```bash
export LOG_LEVEL=debug
make run
```

Monitor controller logs:

```bash
kubectl logs -f deployment/dozlab-controller-manager -n dozlab-system
```

## Deployment

### Development Environment

```bash
# Deploy to minikube/kind
make deploy IMG=dozlab-controller:dev

# Check deployment
kubectl get pods -n dozlab-system
```

### Production Environment

```bash
# Deploy with specific image
make deploy IMG=your-registry/dozlab-controller:v1.0.0

# Scale controller
kubectl scale deployment dozlab-controller-manager --replicas=2 -n dozlab-system

# Update configuration
kubectl edit configmap dozlab-controller-config -n dozlab-system
```

## Monitoring

The controller exposes metrics and health endpoints:

- **Metrics**: `:8080/metrics` (Prometheus format)
- **Health**: `:8081/healthz`
- **Ready**: `:8081/readyz`

### Key Metrics

- `controller_runtime_reconcile_total` - Total reconciliations
- `controller_runtime_reconcile_errors_total` - Reconciliation errors
- `dozlab_labsessions_total` - Total lab sessions managed
- `dozlab_labsessions_active` - Currently active sessions

## Troubleshooting

### Common Issues

1. **CRD Installation Failures**:
```bash
# Check CRD status
kubectl get crd labsessions.dozlab.io

# Reinstall CRDs
make uninstall && make install
```

2. **Permission Errors**:
```bash
# Check RBAC permissions
kubectl describe clusterrole dozlab-controller-manager-role

# Update RBAC
make manifests
kubectl apply -f deploy/
```

3. **Resource Creation Failures**:
```bash
# Check controller logs
kubectl logs -f deployment/dozlab-controller-manager -n dozlab-system

# Describe failing resources
kubectl describe labsession <session-name>
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Run `make test` and `make manifests`
6. Submit a pull request

## RBAC Permissions

The controller requires these Kubernetes permissions:

- **LabSession CRDs**: Full access
- **Pods**: Create, read, update, delete, list, watch
- **Services**: Create, read, update, delete, list, watch
- **PersistentVolumes**: Create, read, update, delete, list, watch
- **Events**: Create, patch

## License

[Add your license here]