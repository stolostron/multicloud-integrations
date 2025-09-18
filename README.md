# Multicloud Integrations

A comprehensive suite of controllers that bridge Red Hat Advanced Cluster Management (RHACM) with Argo CD, enabling GitOps-driven multi-cluster application delivery at scale.

## Overview

This project provides the foundational components for implementing GitOps workflows across large multi-cluster environments. It offers both traditional manifest-based deployments and performance-optimized solutions for enterprise-scale operations.

## Architecture Models

### Traditional Model
Perfect for standard multi-cluster deployments, leveraging ACM's native capabilities:
- **Cluster Management**: Automatically imports managed clusters into Argo CD based on placement policies
- **Application Propagation**: Distributes applications using ManifestWork resources
- **Status Collection**: Aggregates application health and sync status across clusters

### Maestro-Enhanced Model  
Designed for large-scale environments (100+ ApplicationSets across 1000+ clusters):
- **Performance Optimization**: Reduces resource overhead and API pressure
- **Enhanced Visibility**: Provides detailed application status in both ACM and Argo CD consoles
- **Scalable Architecture**: Addresses performance bottlenecks in massive deployments

## Key Capabilities

- **Declarative Cluster Import**: Use Placement resources to control which clusters appear in Argo CD
- **GitOps Operator Management**: Automate OpenShift GitOps installation across managed clusters  
- **Pull-Based Application Delivery**: Deploy applications to managed clusters using GitOps principles
- **Unified Status Aggregation**: Centralized view of application health across all clusters
- **Agent-Based Architecture**: Optional Argo CD agents for enhanced cluster connectivity

## Getting Started

### Prerequisites

- Red Hat Advanced Cluster Management (RHACM) hub cluster
- Kubernetes clusters registered as ACM ManagedClusters  
- OpenShift GitOps operator (for Argo CD functionality)

### Installation Options

#### Standard Deployment
Deploy the core controllers for typical multi-cluster GitOps workflows:
- Install CustomResourceDefinitions (CRDs)
- Deploy controller managers
- Configure cluster placement and import policies

#### Maestro-Enhanced Deployment  
For large-scale environments requiring enhanced performance:
- Install Maestro addon components
- Deploy performance-optimized controllers
- Configure enhanced status aggregation

### Configuration Workflow

1. **Cluster Selection**: Define placement policies to control which managed clusters are imported into Argo CD
2. **GitOps Integration**: Configure GitOpsCluster resources to establish the connection between ACM and Argo CD
3. **Application Deployment**: Use Argo CD ApplicationSets with pull-model annotations for multi-cluster deployment
4. **Status Monitoring**: Monitor application health and sync status across all managed clusters

See the [`examples/`](examples/) directory for specific resource configurations and deployment templates.

## Common Use Cases

### Cluster Import and Management
- **Selective Import**: Use Placement resources to control which managed clusters appear in Argo CD based on labels, cluster properties, or custom criteria
- **Dynamic Updates**: Automatically add or remove clusters from Argo CD as managed clusters join or leave placements
- **Namespace Organization**: Configure target Argo CD namespaces and organize cluster access

### GitOps Operator Deployment
- **Automated Installation**: Deploy OpenShift GitOps operator across selected managed clusters using addon mechanisms
- **Configuration Management**: Standardize Argo CD instance configuration across your fleet
- **Proxy Support**: Configure proxy settings and network policies for managed cluster connectivity

### Multi-Cluster Application Delivery
- **Pull-Based Deployment**: Use Argo CD ApplicationSets with ACM annotations to deploy applications across multiple clusters
- **Status Aggregation**: Monitor application health, sync status, and deployment progress from a central hub
- **Targeted Deployment**: Control application placement using the same placement policies used for cluster import

## Architecture Overview

### Component Relationships
The system consists of several cooperating controllers that work together to provide seamless multi-cluster GitOps:

- **Cluster Management Layer**: Handles the import and lifecycle of managed clusters within Argo CD
- **Application Propagation Layer**: Manages the distribution and synchronization of applications across clusters  
- **Status Aggregation Layer**: Collects and consolidates application state information from distributed clusters
- **Addon Management Layer**: Automates the deployment and configuration of GitOps components on managed clusters

### Data Flow
1. **Placement Evaluation**: ACM placement policies determine target clusters
2. **Cluster Registration**: Selected clusters are registered in Argo CD with appropriate access configurations
3. **Application Distribution**: Argo CD ApplicationSets deploy applications to registered clusters using pull-based mechanisms
4. **Status Synchronization**: Application status flows back to the hub for centralized monitoring and reporting

## Configuration Patterns

The controllers support various configuration approaches to meet different organizational needs:

- **Image Management**: Configurable container images for GitOps components across the fleet
- **Network Configuration**: Proxy settings and connectivity options for managed cluster access
- **RBAC Integration**: Role-based access control alignment between ACM and Argo CD
- **Resource Optimization**: Tunable parameters for large-scale deployments and performance optimization

## Development and Build

This project follows standard Go development practices and includes comprehensive testing:

- **Local Development**: Standard Go toolchain and Make-based build system
- **Container Builds**: Multi-stage Docker builds for optimized production images  
- **Testing Suite**: Unit tests, integration tests, and end-to-end validation
- **CI/CD Integration**: Automated testing and release pipelines

Refer to `Makefile` for available build targets and development workflows.

## Troubleshooting

### Diagnostic Approach

When issues occur, follow this systematic troubleshooting approach:

1. **Resource Status**: Check the status of GitOpsCluster and related custom resources
2. **Placement Evaluation**: Verify that Placement resources are generating appropriate PlacementDecision resources  
3. **Controller Health**: Review controller pod status and resource utilization
4. **Network Connectivity**: Ensure managed clusters can communicate with the hub and Argo CD

### Common Resolution Patterns

- **Cluster Import Issues**: Often related to placement configuration, network connectivity, or RBAC permissions
- **Application Deployment Problems**: Typically involve ManifestWork creation, pull model annotations, or target cluster GitOps operator status
- **Status Aggregation Delays**: May indicate performance issues, search API problems, or network latency in large-scale deployments
- **Maestro Integration Problems**: Usually involve addon installation status, resource synchronization, or component compatibility

### Observability

The controllers provide detailed logging and metrics for operational visibility. Use standard Kubernetes tooling to access logs and monitor resource status. For large-scale deployments, consider implementing centralized logging and monitoring solutions.

## Examples and Reference

### Example Configurations

The [`examples/`](examples/) directory contains practical, ready-to-use resource configurations:

- **GitOpsCluster Setup**: Basic and advanced cluster import configurations
- **Placement Policies**: Cluster selection and targeting examples  
- **ManagedClusterSetBinding**: Cluster organization and access control
- **Agent Deployment**: Argo CD agent configuration templates

### Additional Resources

- **API Documentation**: Detailed API specifications are available in the CRD definitions under `deploy/crds/`
- **Maestro Integration**: See [`maestroapplication/`](maestroapplication/) for Maestro-specific implementation details
- **End-to-End Examples**: Complete scenarios are available in the `e2e/` directory for testing and validation

## Contributing

This project welcomes contributions and follows standard open-source practices. Please refer to [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on code style, testing requirements, and submission processes.

## License

This project is licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.
