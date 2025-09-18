# Multicloud Integrations

A comprehensive suite of controllers for integrating ACM with Argo CD GitOps workflows, providing enhanced multi-cluster application deployment and management capabilities.

## Overview

The **Multicloud Integrations** project provides several controllers that enable seamless integration between ACM and Argo CD GitOps workflows. It supports both traditional ManifestWork-based deployments and enhanced Maestro-based integrations for large-scale multi-cluster environments.

## Components

### Core Controllers

| Controller | Binary | Purpose |
|------------|--------|---------|
| **GitOps Cluster** | `gitopscluster` | Imports ACM ManagedClusters into Argo CD based on Placement resources |
| **GitOps Sync Resource** | `gitopssyncresc` | Synchronizes ApplicationSet resources and manages resource propagation |
| **Status Aggregation** | `multiclusterstatusaggregation` | Aggregates application status across multiple clusters |
| **Propagation** | `propagation` | Manages application propagation using ManifestWork resources |
| **GitOps Addon** | `gitopsaddon` | Manages OpenShift GitOps operator installation and Argo CD agent deployment |
| **Maestro Propagation** | `maestropropagation` | Enhanced propagation using Maestro for large-scale environments |
| **Maestro Aggregation** | `maestroaggregation` | Status aggregation with Maestro integration for improved performance |

### Key Features

- **Multi-cluster GitOps**: Import and manage clusters in Argo CD using ACM Placement resources
- **Maestro Integration**: Enhanced pull model addressing performance challenges in large-scale environments (100+ ApplicationSets on 3k+ clusters)
- **GitOps Addon Management**: Automated installation and configuration of OpenShift GitOps operator and instances
- **Argo CD Agent Support**: Deploy and manage Argo CD agents on managed clusters
- **Status Aggregation**: Collect and aggregate application status from distributed clusters
- **Application Propagation**: Deploy applications across multiple clusters using ManifestWork or Maestro

## Quick Start

### Prerequisites

- Red Hat Advanced Cluster Management (RHACM) hub cluster
- Kubernetes clusters registered as ACMM ManagedClusters
- OpenShift GitOps operator (for Argo CD functionality)

### Standard Installation

1. **Deploy the controllers:**
   ```bash
   kubectl apply -f deploy/crds
   kubectl apply -f deploy/controller
   ```

2. **Configure cluster import:**
   ```bash
   # Create ManagedClusterSet and ManagedClusterSetBinding
   # Create Placement resource targeting desired clusters
   # Create GitOpsCluster resource pointing to Placement and Argo CD namespace
   ```

3. **Verify cluster import:**
   Check Argo CD → Configuration → Clusters to see imported ManagedClusters

### Maestro-Enhanced Installation

For large-scale environments, use the Maestro-enhanced deployment:

1. **Install Maestro addon server on hub:**
   ```bash
   git clone https://github.com/stolostron/maestro-addon
   cd maestro-addon
   helm install maestro-addon ./charts/maestro-addon
   ```

2. **Install Maestro addon agent on managed clusters:**
   ```bash
   oc -n <cluster-namespace> apply -f - <<EOF
   apiVersion: addon.open-cluster-management.io/v1alpha1
   kind: ManagedClusterAddOn
   metadata:
     name: maestro-addon
   spec:
     installNamespace: open-cluster-management-agent
   EOF
   ```

3. **Deploy enhanced controllers:**
   ```bash
   helm install multicluster-maestro-integrations \
     ./maestroapplication/charts/multicluster-maestro-integrations \
     --namespace open-cluster-management
   ```

## Usage Examples

### Basic GitOps Cluster Configuration

```yaml
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: openshift-clusters
  namespace: default
spec:
  predicates:
  - requiredClusterSelector:
      labelSelector:
        matchLabels:
          vendor: OpenShift
---
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: gitops-cluster-sample
  namespace: default
spec:
  Argo CDNamespace: openshift-gitops
  placementRef:
    name: openshift-clusters
```

### GitOps Addon Installation

Deploy OpenShift GitOps on managed clusters:

```bash
oc -n <cluster-namespace> apply -f - <<EOF
apiVersion: addon.open-cluster-management.io/v1alpha1
kind: ManagedClusterAddOn
metadata:
  name: gitops-addon
spec:
  installNamespace: open-cluster-management-agent-addon
EOF
```

### Application Propagation

Deploy applications with pull model annotations:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: sample-app
  namespace: openshift-gitops
  annotations:
    apps.open-cluster-management.io/ocm-managed-cluster: "managed-cluster-1"
  labels:
    apps.open-cluster-management.io/pull-to-ocm-managed-cluster: "true"
spec:
  # Application specification
```

## Architecture

### Traditional Model
- **GitOps Cluster Controller**: Imports clusters into Argo CD
- **Propagation Controller**: Creates ManifestWork resources for app deployment
- **Status Aggregation**: Collects status via Search API

### Maestro-Enhanced Model
- **Maestro Integration**: Reduces ManifestWork overhead (300k → optimized)
- **Performance Optimization**: Addresses Search API performance bottlenecks
- **Native Argo CD Support**: Enhanced visibility in Argo CD console
- **Detailed Status Storage**: Comprehensive status information on hub cluster

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `GITOPS_OPERATOR_IMAGE` | OpenShift GitOps operator image | - |
| `GITOPS_IMAGE` | Argo CD instance image | - |
| `REDIS_IMAGE` | Redis image for Argo CD | - |
| `Argo CD_AGENT_ENABLED` | Enable Argo CD agent | `false` |
| `Argo CD_AGENT_IMAGE` | Argo CD agent image | - |
| `HTTP_PROXY` | HTTP proxy configuration | - |
| `HTTPS_PROXY` | HTTPS proxy configuration | - |
| `NO_PROXY` | No proxy configuration | - |

### Build and Development

**Build all controllers:**
```bash
make build
```

**Build container images:**
```bash
make build-images
```

**Run tests:**
```bash
make test-unit
make test-integration
```

**Run E2E tests:**
```bash
make test-e2e
```

## Troubleshooting

### Common Issues

1. **Clusters not appearing in Argo CD**
   - Check GitOpsCluster resource status
   - Verify Placement has generated PlacementDecision resources
   - Check controller logs: `kubectl logs -n open-cluster-management deployment/multicloud-integrations-gitops`

2. **Application deployment failures**
   - Verify ManifestWork resources created in cluster namespaces
   - Check propagation controller logs
   - Ensure proper RBAC permissions

3. **Maestro integration issues**
   - Verify Maestro addon server and agents are running
   - Check Maestro resource synchronization
   - Review maestro controller logs

### Log Analysis

```bash
# GitOps controllers
kubectl logs -n open-cluster-management deployment/multicloud-integrations-gitops
kubectl logs -n open-cluster-management deployment/multicloud-integrations

# Maestro controllers  
kubectl logs -n open-cluster-management deployment/multicluster-maestro-integrations
```

## Examples

Detailed examples are available in the [`examples/`](examples/) directory:

- Basic GitOpsCluster configuration
- ManagedClusterSetBinding setup
- Placement resource examples
- Application deployment templates

## License

This project is licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.
