# Multicloud Integrations

A suite of controllers that integrate Red Hat Advanced Cluster Management (RHACM) with Argo CD, enabling GitOps-driven multi-cluster application delivery and management.

## Overview

This project provides the essential components for implementing GitOps workflows across multi-cluster environments. It bridges RHACM's cluster management capabilities with Argo CD's application delivery, creating a seamless experience for managing applications across distributed Kubernetes clusters.

## Core Components

### GitOpsCluster Custom Resource

The `GitOpsCluster` CR is the primary interface for connecting RHACM with Argo CD. It automatically imports managed clusters into Argo CD based on placement policies, creating the foundation for multi-cluster GitOps workflows.

**Key Features:**
- **Placement-driven Import**: Uses RHACM placement resources to selectively import clusters into Argo CD
- **Automatic Registration**: Creates Argo CD cluster secrets and configurations for managed clusters
- **Service Account Management**: Handles authentication between RHACM and Argo CD using managed service accounts
- **Dynamic Updates**: Automatically adds or removes clusters as placement decisions change

### GitOps Addon

The GitOps addon automates the deployment and management of OpenShift GitOps (Argo CD) on managed clusters. It ensures that the necessary GitOps infrastructure is consistently deployed across your cluster fleet.

**Key Features:**
- **Automated Installation**: Deploys OpenShift GitOps operator to managed clusters via the addon framework
- **Configuration Management**: Standardizes Argo CD instance configuration across clusters
- **Lifecycle Management**: Handles updates and maintenance of GitOps components
- **Proxy Support**: Configures network settings and proxy configurations for connectivity

### Argo CD Agent Integration

An optional agent-based architecture that enhances connectivity between managed clusters and the central Argo CD server, particularly useful for clusters with network restrictions or enhanced security requirements.

**Key Features:**
- **Enhanced Connectivity**: Provides alternative connection methods for managed clusters
- **Certificate Management**: Handles TLS certificates and secure communication
- **Pull-based Architecture**: Enables clusters to pull configuration rather than requiring inbound connections
- **Network Policy Support**: Works with restrictive network policies and firewall configurations

## Architecture

The system operates on a hub-and-spoke model where the RHACM hub cluster hosts the central Argo CD instance and controllers, while managed clusters run the necessary agents and GitOps components.

### Workflow Overview

1. **Cluster Selection**: Define placement policies to control which managed clusters participate in GitOps workflows
2. **Automatic Import**: The GitOpsCluster controller imports selected clusters into Argo CD
3. **Addon Deployment**: The GitOps addon ensures OpenShift GitOps is installed on managed clusters
4. **Application Deployment**: Use Argo CD ApplicationSets to deploy applications across the imported clusters
5. **Status Monitoring**: Monitor application health and sync status from the central hub

## Getting Started

### Prerequisites

- Red Hat Advanced Cluster Management (RHACM) hub cluster
- Kubernetes clusters registered as RHACM ManagedClusters
- OpenShift GitOps operator installed on the hub cluster

## Configuration Options

### GitOpsCluster Configuration

- **Placement References**: Control cluster selection through RHACM placement policies
- **Service Account Management**: Configure authentication methods between clusters
- **Addon Integration**: Enable automatic GitOps addon deployment on imported clusters

### Addon Configuration

- **Image Customization**: Specify custom images for GitOps components
- **Network Settings**: Configure proxy settings and network policies
- **Resource Limits**: Set resource constraints for addon components

### Agent Configuration

- **Connection Mode**: Choose between different connectivity patterns
- **Certificate Management**: Configure TLS certificate handling
- **Server Endpoints**: Define Argo CD server connection details

## Additional Capabilities

### Pull Model Integration

The project also supports pull-based application delivery through several additional controllers:

- **Propagation Controller**: Manages application distribution using RHACM's ManifestWork resources
- **GitOps Sync Resource Controller**: Handles synchronization of application manifests
- **Multi-cluster Status Aggregation**: Collects and consolidates application status across clusters

### Maestro Application Integration

For development and testing scenarios, the project includes experimental integration with the Maestro resource management system, providing an alternative approach to resource propagation and status collection.

## Examples and Templates

The [`examples/`](examples/) directory contains ready-to-use configurations:

- **GitOpsCluster Examples**: Basic and advanced cluster import configurations
- **Placement Examples**: Cluster selection patterns and policies
- **Addon Templates**: GitOps addon deployment configurations

Additional examples and end-to-end scenarios are available in the [`e2e/`](e2e/) and [`e2e-gitopsaddon/`](e2e-gitopsaddon/) directories.

## Contributing

This project welcomes contributions. Please refer to [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on code style, testing requirements, and submission processes. 

## License

This project is licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.
