# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This is a multi-cloud integrations repository that provides controllers for integrating Open Cluster Management (OCM) with GitOps solutions like Argo CD. The repository contains multiple Go-based controllers that handle cluster management, application propagation, and status aggregation across multiple clusters.

## Common Commands

### Building
```bash
# Build all binaries
make build

# Build container images
make build-images

# Build for local development (macOS)
make local
```

### Testing
```bash
# Run all tests
make test

# Download kubebuilder tools (required for tests)
make ensure-kubebuilder-tools

# Run end-to-end tests
make test-e2e
```

### Code Quality
```bash
# Run linting
make lint

# Generate code (DeepCopy methods, manifests)
make generate

# Generate CRD manifests
make manifests
```

### Development Setup
```bash
# Deploy OCM for local development
make deploy-ocm

# Apply CRDs and controller
kubectl apply -f deploy/crds
kubectl apply -f deploy/controller
```

## Architecture

### Main Components

The repository is organized around several key controllers, each housed in its own `cmd/` directory:

- **gitopscluster**: Imports OCM ManagedCluster resources into Argo CD based on OCM Placement resources
- **gitopsaddon**: Manages GitOps add-on functionality
- **gitopssyncresc**: Handles GitOps resource synchronization
- **multiclusterstatusaggregation**: Aggregates status across multiple clusters
- **propagation**: Handles resource propagation across clusters
- **maestropropagation**: Maestro-based propagation controller
- **maestroaggregation**: Maestro-based aggregation controller

### Package Structure

- `pkg/apis/`: API definitions and scheme registration
  - `apps/`: Application-related APIs (GitOpsCluster, etc.)
  - `appsetreport/`: ApplicationSet reporting APIs
- `pkg/controller/`: Controller implementations
- `pkg/utils/`: Shared utilities
- `common/`: Common scripts and utilities
- `gitopsaddon/`: GitOps add-on controller implementation
- `maestroapplication/`: Maestro application controllers
- `propagation-controller/`: Resource propagation logic

### Key CRDs

- **GitOpsCluster**: Links OCM Placements to Argo CD clusters
- **Placement**: OCM resource for cluster selection
- **ManagedCluster**: OCM representation of managed clusters

## Development Workflow

1. **Prerequisites**: Ensure you have Go 1.23+ and access to a Kubernetes cluster with OCM installed
2. **Local Development**: Use `make local` to build binaries for macOS
3. **Testing**: Always run `make test` before submitting changes
4. **Code Generation**: Run `make generate` after modifying API types
5. **Manifests**: Run `make manifests` after changing RBAC or CRD annotations

## Key Dependencies

- Open Cluster Management (OCM) APIs and SDK
- Kubernetes controller-runtime
- Helm v3 for GitOps operations
- Maestro for multi-cluster orchestration

## Testing Framework

Uses Ginkgo/Gomega for testing with kubebuilder tools for controller testing. Tests require downloading kubebuilder assets via `make ensure-kubebuilder-tools`.

## Container Registry

Default registry: `quay.io/stolostron`
Configure via: `REGISTRY` and `VERSION` environment variables