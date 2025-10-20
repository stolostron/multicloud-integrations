#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Default values
DEFAULT_NAMESPACE="openshift-gitops"
DEFAULT_ARGOCD_NAME="openshift-gitops"

# Initialize variables with defaults
NAMESPACE="${DEFAULT_NAMESPACE}"
ARGOCD_NAME="${DEFAULT_ARGOCD_NAME}"

# Function to display help
show_help() {
    cat << EOF
Usage: $(basename "$0") [OPTIONS]

Setup ApplicationSet controller to support managing ApplicationSets in any namespace.

This script performs two main tasks:
1. Patches the ArgoCD instance to enable ApplicationSet with any namespace support
2. Applies necessary RBAC resources (ClusterRole and ClusterRoleBinding)

OPTIONS:
    -n, --namespace NAMESPACE    Namespace where ArgoCD is installed (default: ${DEFAULT_NAMESPACE})
    -a, --argocd-name NAME       Name of the ArgoCD instance (default: ${DEFAULT_ARGOCD_NAME})
    -h, --help                   Display this help message and exit

EXAMPLES:
    # Use default values (namespace: openshift-gitops, argocd: openshift-gitops)
    $(basename "$0")

    # Specify both custom namespace and ArgoCD name
    $(basename "$0") --namespace my-gitops --argocd-name my-argocd

PREREQUISITES:
    - OpenShift CLI (oc) must be installed and available in PATH
    - Must be logged into an OpenShift cluster
    - ArgoCD instance must exist in the specified namespace
    - Cluster admin privileges required for applying ClusterRole/ClusterRoleBinding

EOF
}

parse_arguments() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -n|--namespace)
                NAMESPACE="$2"
                shift 2
                ;;
            -a|--argocd-name)
                ARGOCD_NAME="$2"
                shift 2
                ;;
            -h|--help)
                show_help
                exit 0
                ;;
            *)
                echo "Error: Unknown option $1"
                echo "Use --help for usage information."
                exit 1
                ;;
        esac
    done

    # Validate that required values are not empty
    if [[ -z "${NAMESPACE}" ]]; then
        echo "Error: Namespace cannot be empty"
        exit 1
    fi

    if [[ -z "${ARGOCD_NAME}" ]]; then
        echo "Error: ArgoCD name cannot be empty"
        exit 1
    fi
}

# check if oc command is available
check_oc_command() {
    if ! command -v oc &> /dev/null; then
        echo "Error: 'oc' command not found. Please install OpenShift CLI."
        exit 1
    fi
}

# check if we're logged into the cluster
check_cluster_connection() {
    if ! oc whoami &> /dev/null; then
        echo "Error: Not logged into an OpenShift cluster. Please run 'oc login' first."
        exit 1
    fi
}

# check if ArgoCD exists in openshift-gitops namespace
check_argocd_exists() {
    echo "Checking if ArgoCD '${ARGOCD_NAME}' exists in namespace '${NAMESPACE}'..."

    if oc get argocd "${ARGOCD_NAME}" -n "${NAMESPACE}" &> /dev/null; then
        echo "✓ Found ArgoCD '${ARGOCD_NAME}' in namespace '${NAMESPACE}'"
        return 0
    else
        echo "✗ ArgoCD '${ARGOCD_NAME}' not found in namespace '${NAMESPACE}'"
        return 1
    fi
}

# patch ArgoCD with applicationSet environment variables
patch_argocd_applicationset() {
    echo "Patching ArgoCD '${ARGOCD_NAME}' to enable ApplicationSet in any namespace support..."

    # Create the patch JSON
    local patch_json='{
        "spec": {
            "applicationSet": {
                "enabled": true,
                "env": [
                    {
                        "name": "ARGOCD_APPLICATIONSET_CONTROLLER_NAMESPACES",
                        "value": "*"
                    },
                    {
                        "name": "ARGOCD_APPLICATIONSET_CONTROLLER_ENABLE_SCM_PROVIDERS",
                        "value": "false"
                    }
                ]
            }
        }
    }'

    if oc patch argocd "${ARGOCD_NAME}" -n "${NAMESPACE}" --type=merge -p "${patch_json}"; then
        echo "Successfully patched ArgoCD '${ARGOCD_NAME}' with ApplicationSet configuration"
    else
        echo "Failed to patch ArgoCD '${ARGOCD_NAME}'"
        exit 1
    fi
}

# make ApplicationSet controller Cluster-scoped
apply_rbac_resources_for_applicationset_controller() {
    echo "Applying RBAC resources to make ApplicationSet controller Cluster-scoped..."

    local clusterrole_file="${SCRIPT_DIR}/argocd-applicationset-controller-clusterrole.yaml"
    local clusterrolebinding_file="${SCRIPT_DIR}/argocd-applicationset-controller-clusterrolebinding.yaml"

    # Check if files exist
    if [[ ! -f "${clusterrole_file}" ]]; then
        echo "✗ Error: ClusterRole file not found: ${clusterrole_file}"
        exit 1
    fi

    if [[ ! -f "${clusterrolebinding_file}" ]]; then
        echo "✗ Error: ClusterRoleBinding file not found: ${clusterrolebinding_file}"
        exit 1
    fi

    # Apply ClusterRole
    echo "Applying ClusterRole ${clusterrole_file}..."
    if oc apply -f "${clusterrole_file}"; then
        echo "✓ Successfully applied ClusterRole"
    else
        echo "✗ Failed to apply ClusterRole"
        exit 1
    fi

    # Apply ClusterRoleBinding
    echo "Applying ClusterRoleBinding ${clusterrolebinding_file}..."
    if oc apply -f "${clusterrolebinding_file}"; then
        echo "✓ Successfully applied ClusterRoleBinding"
    else
        echo "✗ Failed to apply ClusterRoleBinding"
        exit 1
    fi
}

# wait for ApplicationSet controller to be ready
wait_for_applicationset_controller() {
    echo "Waiting for ApplicationSet controller to be ready..."

    local max_attempts=30
    local attempt=1

    while [[ ${attempt} -le ${max_attempts} ]]; do
        if oc get deployment openshift-gitops-applicationset-controller -n "${NAMESPACE}" &> /dev/null; then
            if oc rollout status deployment/openshift-gitops-applicationset-controller -n "${NAMESPACE}" --timeout=10s &> /dev/null; then
                echo "✓ ApplicationSet controller is ready"
                return 0
            fi
        fi

        echo "Waiting for ApplicationSet controller... (attempt ${attempt}/${max_attempts})"
        sleep 10
        ((attempt++))
    done

    echo "⚠ ApplicationSet controller may still be starting up. Check manually with:"
    echo "  oc get deployment openshift-gitops-applicationset-controller -n ${NAMESPACE}"
}

main() {
    parse_arguments "$@"

    echo "=== ApplicationSet Any Namespace Setup ==="
    echo "Using ArgoCD: '${ARGOCD_NAME}' in namespace: '${NAMESPACE}'"
    echo

    # Step 0: Check if oc command is available and we're logged into the cluster
    check_oc_command
    check_cluster_connection

    # Step 1: Check if ArgoCD exists and patch it
    if check_argocd_exists; then
        patch_argocd_applicationset
    else
        echo "Skipping ArgoCD patch as it doesn't exist in the expected location."
        echo "Please ensure OpenShift GitOps is installed and ArgoCD is available."
    fi

    echo

    # Step 2: Apply RBAC resources
    apply_rbac_resources_for_applicationset_controller

    echo

    # Step 3: Wait for controller to be ready
    wait_for_applicationset_controller

    echo
    echo "=== Setup Complete ==="
    echo "ApplicationSet controller should now be able to manage ApplicationSets in any namespace."
    echo
}

# Run main function
main "$@"
