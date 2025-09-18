<!-- START doctoc generated TOC please keep comment here to allow auto update -->
<!-- DON'T EDIT THIS SECTION, INSTEAD RE-RUN doctoc TO UPDATE -->
**Table of Contents**  *generated with [DocToc](https://github.com/thlorenz/doctoc)*

- [Contributing guidelines](#contributing-guidelines)
    - [Developer Certificate of Origin](#developer-certificate-of-origin)
    - [Contributing A Patch](#contributing-a-patch)
    - [Issue and Pull Request Management](#issue-and-pull-request-management)
    - [Development Setup](#development-setup)
        - [Prerequisites](#prerequisites)
        - [Local Development](#local-development)
    - [Pre-check before submitting a PR](#pre-check-before-submitting-a-pr)
        - [Code Quality and Linting](#code-quality-and-linting)
        - [Testing](#testing)
        - [Build Verification](#build-verification)
    - [Testing Guidelines](#testing-guidelines)
        - [Controller-Specific Testing](#controller-specific-testing)
        - [Environment Setup for Testing](#environment-setup-for-testing)
    - [Documentation Updates](#documentation-updates)
    - [Code Style Guidelines](#code-style-guidelines)
    - [Commit Message Guidelines](#commit-message-guidelines)

<!-- END doctoc generated TOC please keep comment here to allow auto update -->

# Contributing guidelines

## Developer Certificate of Origin

This repository built with [probot](https://github.com/probot/probot) that enforces the [Developer Certificate of Origin](https://developercertificate.org/) (DCO) on Pull Requests. It requires all commit messages to contain the `Signed-off-by` line with an email address that matches the commit author.

## Contributing A Patch

1. Submit an issue describing your proposed change to the repo in question.
1. The [repo owners](OWNERS) will respond to your issue promptly.
1. Fork the desired repo, develop and test your code changes.
1. Commit your changes with DCO
1. Submit a pull request.

## Issue and Pull Request Management

Anyone may comment on issues and submit reviews for pull requests.

Repo maintainers can assign you an issue or pull request by leaving a
`/assign <your Github ID>` comment on the issue or pull request.

## Development Setup

### Prerequisites

Before contributing to the multicloud-integrations project, ensure you have:

- Go 1.20 or higher
- Kubernetes cluster with Red Hat Advanced Cluster Management (RHACM)
- kubectl and oc CLI tools
- Docker or Podman for building container images
- Access to an OpenShift cluster for testing

### Local Development

1. **Clone the repository:**
   ```shell
   git clone https://github.com/stolostron/multicloud-integrations.git
   cd multicloud-integrations
   ```

2. **Install dependencies:**
   ```shell
   go mod download
   ```

3. **Build the project:**
   ```shell
   make build
   ```

## Pre-check before submitting a PR

After your PR is ready to commit, please run the following commands to check your code:

### Code Quality and Linting
```shell
make lint
make verify
```

### Testing
Run the complete test suite to ensure your changes don't break existing functionality:

```shell
# Unit tests
make test-unit

# Integration tests  
make test-integration

# End-to-end tests (requires test environment)
make test-e2e
```

### Build Verification
Ensure all controllers build successfully:

```shell
# Build all binaries
make build

# Build container images
make build-images
```

## Testing Guidelines

### Controller-Specific Testing

This project includes multiple controllers. When making changes, test the relevant components:

- **GitOps Cluster Controller** (`gitopscluster`)
- **GitOps Sync Resource Controller** (`gitopssyncresc`) 
- **Status Aggregation Controller** (`multiclusterstatusaggregation`)
- **Propagation Controller** (`propagation`)
- **GitOps Addon Controller** (`gitopsaddon`)
- **Maestro Controllers** (`maestropropagation`, `maestroaggregation`)

### Environment Setup for Testing

For comprehensive testing, you'll need:
1. RHACM hub cluster with managed clusters
2. OpenShift GitOps operator installed
3. Argo CD instance configured
4. For Maestro testing: Maestro addon server and agents

## Documentation Updates

When contributing new features or changes:

1. Update relevant documentation in the `docs/` directory
2. Update the main README.md if adding new functionality
3. Add or update examples in the `examples/` directory
4. Ensure all new APIs are properly documented

## Code Style Guidelines

### Go Code Standards

- Follow standard Go formatting with `gofmt`
- Use `golint` and `go vet` for code quality checks
- Maintain consistent naming conventions:
  - Use camelCase for local variables and functions
  - Use PascalCase for exported functions and types
  - Use descriptive names that clearly indicate purpose

### Controller Best Practices

- Follow Kubernetes controller patterns and conventions
- Implement proper error handling with structured logging
- Use controller-runtime patterns for reconciliation loops
- Ensure controllers are idempotent and handle race conditions
- Add appropriate metrics and observability

### API Design

- Follow Kubernetes API conventions for custom resources
- Use proper API versioning (`v1alpha1`, `v1beta1`, `v1`)
- Include comprehensive field validation and documentation
- Maintain backward compatibility when possible

## Commit Message Guidelines

### Format

Use the conventional commit format for clear, structured commit messages:

```
<type>(<scope>): <description>

[optional body]

[optional footer(s)]
```

### Types

- **feat**: New features
- **fix**: Bug fixes
- **docs**: Documentation changes
- **style**: Code style changes (formatting, etc.)
- **refactor**: Code refactoring without functional changes
- **test**: Adding or updating tests
- **chore**: Maintenance tasks, dependency updates

### Examples

```
feat(gitopscluster): add support for custom Argo CD namespaces

Add configuration option to specify custom Argo CD namespace
for cluster imports, enabling better multi-tenancy support.

Closes #123

Signed-off-by: Your Name <your.email@example.com>
```

```
fix(propagation): handle ManifestWork deletion race condition

Improve controller logic to properly handle cases where
ManifestWork resources are deleted during reconciliation.

Signed-off-by: Your Name <your.email@example.com>
```

### Requirements

- Include `Signed-off-by` line for DCO compliance
- Reference relevant issues with `Closes #<issue-number>`
- Keep the first line under 50 characters
- Use imperative mood ("add" not "added")
- Include scope when changes affect specific controllers

Now, you can follow the [Quick Start guide](./README.md#quick-start) to get started with the multicloud-integrations project.
