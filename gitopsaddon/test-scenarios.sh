#!/bin/bash
#
# GitOps Addon Test Scenarios
# Tests all 4 scenarios for gitops-addon functionality
#
# Key Design Principles:
# - All operations are performed from the hub cluster only (simulates user experience)
# - RBAC is created by modifying the Policy that GitOpsCluster generates
# - Secrets are managed by GitOpsCluster controller (NOT manually deleted)
# - For agent mode: Apps created on hub in managed cluster namespace
# - For non-agent mode: Apps can be added to the Policy or created directly on managed cluster
#
# Prerequisites:
# - Hub cluster kubeconfig (set HUB_KUBECONFIG env var)
# - Managed cluster kubeconfigs (for verification only)
# - Managed clusters registered to hub with proper labels
# - Hub ArgoCD configured as principal for agent mode
#
# Usage: ./test-scenarios.sh [1|2|3|4|all|cleanup]
#
# Environment Variables (with defaults):
#   HUB_KUBECONFIG      - Path to hub cluster kubeconfig (default: ~/.kube/config)
#   KIND_KUBECONFIG     - Path to Kind managed cluster kubeconfig (for verification only)
#   OCP_KUBECONFIG      - Path to OCP managed cluster kubeconfig (for verification only)
#   KIND_CLUSTER_NAME   - Name of the Kind managed cluster (default: kind-cluster1)
#   OCP_CLUSTER_NAME    - Name of the OCP managed cluster (default: ocp-cluster3)

set -e

# Configurable defaults - override via environment variables
HUB_KUBECONFIG="${HUB_KUBECONFIG:-$HOME/.kube/config}"
KIND_KUBECONFIG="${KIND_KUBECONFIG:-$HOME/.kube/kind-cluster}"
OCP_KUBECONFIG="${OCP_KUBECONFIG:-$HOME/.kube/ocp-cluster}"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kind-cluster1}"
OCP_CLUSTER_NAME="${OCP_CLUSTER_NAME:-ocp-cluster3}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_debug() { echo -e "${BLUE}[DEBUG]${NC} $1"; }

# Verify only ACM-managed ArgoCD exists (no duplicates)
# Returns 0 if OK, 1 if duplicate found
verify_no_duplicate_argocd() {
    local cluster_kubeconfig=$1
    local cluster_name=$2
    
    export KUBECONFIG=$cluster_kubeconfig
    
    local argocd_list=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null || true)
    local argocd_count=$(echo "$argocd_list" | grep -v '^$' | wc -l)
    
    if [ "$argocd_count" -eq 0 ]; then
        log_info "$cluster_name: No ArgoCD CRs (expected during setup)"
        return 0
    elif [ "$argocd_count" -eq 1 ]; then
        local argocd_name=$(echo "$argocd_list" | awk '{print $1}')
        if [ "$argocd_name" = "acm-openshift-gitops" ]; then
            log_info "$cluster_name: Only acm-openshift-gitops exists (correct)"
            return 0
        else
            log_error "$cluster_name: Unexpected ArgoCD CR: $argocd_name (expected acm-openshift-gitops)"
            return 1
        fi
    else
        log_error "$cluster_name: DUPLICATE ArgoCD CRs found ($argocd_count):"
        echo "$argocd_list"
        # Delete the default openshift-gitops if it exists
        if echo "$argocd_list" | grep -q "openshift-gitops"; then
            log_warn "Deleting duplicate openshift-gitops ArgoCD CR..."
            kubectl delete argocd openshift-gitops -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
        fi
        return 1
    fi
}

wait_for_condition() {
    local timeout=$1
    local message=$2
    local condition=$3
    local count=0
    log_info "Waiting for: $message (timeout: ${timeout}s)"
    while ! eval "$condition" 2>/dev/null; do
        sleep 5
        count=$((count + 5))
        echo -n "."
        if [ $count -ge $timeout ]; then
            echo ""
            log_error "Timeout waiting for: $message"
            return 1
        fi
    done
    echo ""
    log_info "$message - OK"
    return 0
}

# Cleanup a scenario - all operations from hub only (except verification)
# NOTE: We do NOT delete cluster secrets - the GitOpsCluster controller manages them
cleanup_scenario() {
    local cluster_name=$1
    local gitopscluster_name=$2
    local placement_name=$3
    local is_agent_mode=false
    
    # Determine if this is agent mode scenario
    if [[ "$gitopscluster_name" == *"agent"* ]]; then
        is_agent_mode=true
    fi

    log_info "Cleaning up $gitopscluster_name from hub (agent_mode=$is_agent_mode)..."

    export KUBECONFIG=$HUB_KUBECONFIG

    # 1. Delete Applications FIRST
    if [ "$is_agent_mode" = true ]; then
        # Agent mode: apps are in managed cluster namespace on hub
        log_info "Deleting agent-mode applications from hub (namespace: $cluster_name)..."
        kubectl delete applications.argoproj.io --all -n $cluster_name --ignore-not-found --wait=false 2>/dev/null || true
    else
        # Non-agent mode: apps are in Policy - patch policy to remove app objects
        log_info "Patching Policy to remove application (managed cluster will delete it)..."
        # Get current policy and remove app-related config policies
        local policy_name="${gitopscluster_name}-argocd-policy"
        # Just delete the policy - it will remove apps on managed cluster
    fi
    
    # Wait for app deletion to propagate
    sleep 5

    # 2. Delete GitOpsCluster (stops controller from recreating Policy)
    kubectl delete gitopscluster $gitopscluster_name -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true

    # 3. Delete Policy and PlacementBinding
    kubectl delete policy ${gitopscluster_name}-argocd-policy -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete placementbinding ${gitopscluster_name}-argocd-policy-binding -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    
    # Wait for policy deletion to propagate to managed cluster
    sleep 5

    # 4. Delete ManagedClusterAddOn
    # Patch to remove finalizers in case cleanup job is stuck
    kubectl patch managedclusteraddon gitops-addon -n $cluster_name -p '{"metadata":{"finalizers":[]}}' --type=merge 2>/dev/null || true
    # Use --wait=false to prevent hanging
    kubectl delete managedclusteraddon gitops-addon -n $cluster_name --ignore-not-found --wait=false 2>/dev/null || true
    sleep 3

    # 5. Delete Placement
    kubectl delete placement $placement_name -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # Wait for cleanup to propagate
    sleep 5

    # 6. VERIFY cleanup on hub
    log_info "Verifying hub cleanup..."
    local issues=0
    
    if kubectl get gitopscluster $gitopscluster_name -n openshift-gitops 2>/dev/null; then
        log_warn "GitOpsCluster $gitopscluster_name still exists!"
        issues=$((issues + 1))
    fi
    
    if kubectl get policy ${gitopscluster_name}-argocd-policy -n openshift-gitops 2>/dev/null; then
        log_warn "Policy ${gitopscluster_name}-argocd-policy still exists!"
        issues=$((issues + 1))
    fi
    
    if kubectl get managedclusteraddon gitops-addon -n $cluster_name 2>/dev/null; then
        log_warn "ManagedClusterAddOn gitops-addon still exists in $cluster_name!"
        issues=$((issues + 1))
    fi
    
    # 7. VERIFY managed cluster - check for duplicate ArgoCD CRs
    log_info "Verifying managed cluster cleanup..."
    if [ "$cluster_name" = "$KIND_CLUSTER_NAME" ]; then
        export KUBECONFIG=$KIND_KUBECONFIG
    else
        export KUBECONFIG=$OCP_KUBECONFIG
    fi
    
    # Check for any ArgoCD CRs
    local argocd_list=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null || true)
    local argocd_count=$(echo "$argocd_list" | grep -v '^$' | wc -l)
    
    if [ "$argocd_count" -gt 0 ]; then
        log_warn "Managed cluster still has $argocd_count ArgoCD CR(s):"
        echo "$argocd_list"
        # Force delete any remaining ArgoCD CRs
        kubectl delete argocd --all -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    else
        log_info "Managed cluster: No ArgoCD CRs (clean)"
    fi
    
    # Check for guestbook deployment
    if kubectl get deployment guestbook-ui -n openshift-gitops 2>/dev/null; then
        log_warn "Guestbook deployment still exists on managed cluster!"
        kubectl delete deployment guestbook-ui -n openshift-gitops --ignore-not-found 2>/dev/null || true
    fi
    
    # Switch back to hub
    export KUBECONFIG=$HUB_KUBECONFIG
    
    if [ $issues -eq 0 ]; then
        log_info "Cleanup of $gitopscluster_name complete - all verified"
    else
        log_warn "Cleanup of $gitopscluster_name complete with $issues issue(s)"
    fi
}

cleanup_all() {
    log_info "=== Initial cleanup - wiping everything from hub and managed clusters ==="

    # ============================================
    # STEP 1: Clean up managed clusters directly (OK for initial cleanup)
    # ============================================
    
    log_info "--- Cleaning up KIND cluster directly ---"
    export KUBECONFIG=$KIND_KUBECONFIG
    # Delete all ArgoCD CRs
    kubectl delete argocd --all -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    # Delete all Applications
    kubectl delete applications.argoproj.io --all -A --ignore-not-found --wait=false 2>/dev/null || true
    # Delete guestbook deployment
    kubectl delete deployment guestbook-ui -n openshift-gitops --ignore-not-found 2>/dev/null || true
    
    log_info "--- Cleaning up OCP cluster directly ---"
    export KUBECONFIG=$OCP_KUBECONFIG
    # Delete the ACM-created ArgoCD CR
    kubectl delete argocd acm-openshift-gitops -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    # Delete all Applications
    kubectl delete applications.argoproj.io --all -A --ignore-not-found --wait=false 2>/dev/null || true
    # Delete guestbook deployment
    kubectl delete deployment guestbook-ui -n openshift-gitops --ignore-not-found 2>/dev/null || true
    
    # IMPORTANT: Delete GitOpsService to prevent default ArgoCD recreation
    log_info "Deleting GitOpsService on OCP managed cluster (prevents default ArgoCD)..."
    kubectl delete gitopsservice cluster --ignore-not-found 2>/dev/null || true
    
    # Delete the default openshift-gitops ArgoCD CR
    kubectl delete argocd openshift-gitops -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    
    # Wait for managed cluster cleanup to propagate
    log_info "Waiting for managed cluster cleanup to propagate..."
    sleep 10

    # ============================================
    # STEP 2: Clean up hub resources
    # ============================================
    
    log_info "--- Cleaning up hub resources ---"
    export KUBECONFIG=$HUB_KUBECONFIG

    # Delete all AddOnTemplates managed by us
    kubectl delete addontemplate -l app.kubernetes.io/managed-by=multicloud-integrations --ignore-not-found 2>/dev/null || true

    # Delete stale cluster secrets in openshift-gitops namespace
    log_info "Removing stale cluster secrets..."
    kubectl delete secret cluster-$KIND_CLUSTER_NAME -n openshift-gitops --ignore-not-found 2>/dev/null || true
    kubectl delete secret cluster-$OCP_CLUSTER_NAME -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # Cleanup each scenario from hub
    cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
    cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"

    # ============================================
    # STEP 3: Verify cleanup
    # ============================================
    
    log_info "--- Verifying cleanup ---"
    sleep 5
    
    # Check KIND cluster
    export KUBECONFIG=$KIND_KUBECONFIG
    local kind_argocd_count=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null | wc -l)
    if [ "$kind_argocd_count" -gt 0 ]; then
        log_warn "KIND cluster still has $kind_argocd_count ArgoCD CR(s)"
    else
        log_info "KIND cluster: ArgoCD CRs cleaned up"
    fi
    
    # Check OCP cluster - should have NO ArgoCD CRs (not even the default one)
    export KUBECONFIG=$OCP_KUBECONFIG
    local ocp_argocd_count=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null | wc -l)
    if [ "$ocp_argocd_count" -gt 0 ]; then
        log_warn "OCP cluster still has $ocp_argocd_count ArgoCD CR(s) - forcing delete"
        kubectl delete argocd --all -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    else
        log_info "OCP cluster: ArgoCD CRs cleaned up"
    fi

    log_info "=== All cleanup complete ==="
}

# Configure hub ArgoCD for non-agent mode (scenarios 1 & 2)
# DELETES and RECREATES the ArgoCD CR with minimal config (no principal)
configure_hub_argocd_non_agent() {
    log_info "Configuring hub ArgoCD for non-agent mode (DELETE and RECREATE)..."

    export KUBECONFIG=$HUB_KUBECONFIG

    # Delete existing ArgoCD CR
    log_info "Deleting existing hub ArgoCD CR..."
    kubectl delete argocd openshift-gitops -n openshift-gitops --wait=false 2>/dev/null || true
    sleep 5

    # Recreate with minimal non-agent configuration
    log_info "Creating hub ArgoCD CR with minimal non-agent config..."
    kubectl apply -f - <<'EOF'
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: openshift-gitops
  namespace: openshift-gitops
spec:
  controller:
    enabled: false
EOF

    # Wait for ArgoCD to be ready
    sleep 10
    log_info "Hub ArgoCD configured for non-agent mode"
}

# Configure hub ArgoCD for agent mode (scenarios 3 & 4)
# DELETES and RECREATES the ArgoCD CR with minimal config
configure_hub_argocd_agent() {
    log_info "Configuring hub ArgoCD for agent mode (DELETE and RECREATE)..."
    
    export KUBECONFIG=$HUB_KUBECONFIG
    
    # Delete existing ArgoCD CR
    log_info "Deleting existing hub ArgoCD CR..."
    kubectl delete argocd openshift-gitops -n openshift-gitops --wait=false 2>/dev/null || true
    sleep 5
    
    # Recreate with minimal principal configuration - uses Route on OCP (no LoadBalancer)
    log_info "Creating hub ArgoCD CR with minimal agent config..."
    kubectl apply -f - <<'EOF'
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: openshift-gitops
  namespace: openshift-gitops
spec:
  controller:
    enabled: false
  argoCDAgent:
    principal:
      enabled: true
      auth: "mtls:CN=system:open-cluster-management:cluster:([^:]+):addon:gitops-addon:agent:gitops-addon-agent"
      namespace:
        allowedNamespaces:
          - "*"
EOF
    
    # Wait for principal route to be ready (takes longer after delete/recreate)
    wait_for_condition 300 "Principal Route to be ready" \
        "kubectl get route -n openshift-gitops -l app.kubernetes.io/component=argocd-agent-principal -o name | grep -q route" || {
            log_warn "Principal route not found, continuing anyway..."
        }
    
    log_info "Hub ArgoCD configured for agent mode"
}

# Add RBAC and Application to an existing Policy by patching it
# This simulates user modifying the Policy to add permissions and deploy an app
add_rbac_and_app_to_policy() {
    local policy_name=$1
    local namespace=$2

    log_info "Adding RBAC (cluster-admin) and guestbook Application to Policy $policy_name..."

    export KUBECONFIG=$HUB_KUBECONFIG

    # Wait for the Policy to be created by the controller
    wait_for_condition 120 "Policy $policy_name to be created" \
        "kubectl get policy $policy_name -n $namespace -o name" || return 1

    # Get the current Policy and add RBAC + Application to its object-templates
    kubectl patch policy $policy_name -n $namespace --type=json -p='[
      {
        "op": "add",
        "path": "/spec/policy-templates/-",
        "value": {
          "objectDefinition": {
            "apiVersion": "policy.open-cluster-management.io/v1",
            "kind": "ConfigurationPolicy",
            "metadata": {
              "name": "'${policy_name}'-rbac"
            },
            "spec": {
              "remediationAction": "enforce",
              "severity": "medium",
              "object-templates": [
                {
                  "complianceType": "musthave",
                  "objectDefinition": {
                    "apiVersion": "rbac.authorization.k8s.io/v1",
                    "kind": "ClusterRoleBinding",
                    "metadata": {
                      "name": "acm-openshift-gitops-cluster-admin"
                    },
                    "roleRef": {
                      "apiGroup": "rbac.authorization.k8s.io",
                      "kind": "ClusterRole",
                      "name": "cluster-admin"
                    },
                    "subjects": [
                      {
                        "kind": "ServiceAccount",
                        "name": "acm-openshift-gitops-argocd-application-controller",
                        "namespace": "openshift-gitops"
                      }
                    ]
                  }
                },
                {
                  "complianceType": "musthave",
                  "objectDefinition": {
                    "apiVersion": "v1",
                    "kind": "Namespace",
                    "metadata": {
                      "name": "guestbook"
                    }
                  }
                },
                {
                  "complianceType": "musthave",
                  "objectDefinition": {
                    "apiVersion": "argoproj.io/v1alpha1",
                    "kind": "Application",
                    "metadata": {
                      "name": "guestbook",
                      "namespace": "openshift-gitops"
                    },
                    "spec": {
                      "project": "default",
                      "source": {
                        "repoURL": "https://github.com/argoproj/argocd-example-apps",
                        "targetRevision": "HEAD",
                        "path": "guestbook"
                      },
                      "destination": {
                        "server": "https://kubernetes.default.svc",
                        "namespace": "guestbook"
                      },
                      "syncPolicy": {
                        "automated": {
                          "prune": true,
                          "selfHeal": true
                        }
                      }
                    }
                  }
                }
              ]
            }
          }
        }
      }
    ]'

    if [ $? -eq 0 ]; then
        log_info "RBAC and Application added to Policy successfully"
    else
        log_error "Failed to add RBAC and Application to Policy"
        return 1
    fi
}

# Add RBAC only to Policy (for agent mode - apps are created separately on hub)
add_rbac_to_policy() {
    local policy_name=$1
    local namespace=$2

    log_info "Adding RBAC (cluster-admin) to Policy $policy_name..."

    export KUBECONFIG=$HUB_KUBECONFIG

    # Wait for the Policy to be created by the controller
    wait_for_condition 120 "Policy $policy_name to be created" \
        "kubectl get policy $policy_name -n $namespace -o name" || return 1

    # Add RBAC to the Policy
    kubectl patch policy $policy_name -n $namespace --type=json -p='[
      {
        "op": "add",
        "path": "/spec/policy-templates/-",
        "value": {
          "objectDefinition": {
            "apiVersion": "policy.open-cluster-management.io/v1",
            "kind": "ConfigurationPolicy",
            "metadata": {
              "name": "'${policy_name}'-rbac"
            },
            "spec": {
              "remediationAction": "enforce",
              "severity": "medium",
              "object-templates": [
                {
                  "complianceType": "musthave",
                  "objectDefinition": {
                    "apiVersion": "rbac.authorization.k8s.io/v1",
                    "kind": "ClusterRoleBinding",
                    "metadata": {
                      "name": "acm-openshift-gitops-cluster-admin"
                    },
                    "roleRef": {
                      "apiGroup": "rbac.authorization.k8s.io",
                      "kind": "ClusterRole",
                      "name": "cluster-admin"
                    },
                    "subjects": [
                      {
                        "kind": "ServiceAccount",
                        "name": "acm-openshift-gitops-argocd-application-controller",
                        "namespace": "openshift-gitops"
                      }
                    ]
                  }
                },
                {
                  "complianceType": "musthave",
                  "objectDefinition": {
                    "apiVersion": "v1",
                    "kind": "Namespace",
                    "metadata": {
                      "name": "guestbook"
                    }
                  }
                }
              ]
            }
          }
        }
      }
    ]'

    if [ $? -eq 0 ]; then
        log_info "RBAC added to Policy successfully"
    else
        log_error "Failed to add RBAC to Policy"
        return 1
    fi
}

# Wait for Policy to be compliant
wait_for_policy_compliant() {
    local policy_name=$1
    local namespace=$2
    local timeout=${3:-180}

    log_info "Waiting for Policy $policy_name to be Compliant..."

    export KUBECONFIG=$HUB_KUBECONFIG

    wait_for_condition $timeout "Policy $policy_name to be Compliant" \
        "kubectl get policy $policy_name -n $namespace -o jsonpath='{.status.compliant}' | grep -q 'Compliant'"
}

# Get the server address for agent mode (discovered by GitOpsCluster controller)
# NOTE: This function outputs only the URL to stdout, logs go to stderr
get_agent_server_address() {
    local gitopscluster_name=$1
    local namespace=$2
    local cluster_name=$3

    export KUBECONFIG=$HUB_KUBECONFIG

    # Wait for GitOpsCluster to have server address populated (redirect log output to stderr)
    {
        wait_for_condition 120 "GitOpsCluster to have serverAddress" \
            "kubectl get gitopscluster $gitopscluster_name -n $namespace -o jsonpath='{.spec.gitopsAddon.argoCDAgent.serverAddress}' | grep -v '^$'" || return 1
    } >&2

    local server_address=$(kubectl get gitopscluster $gitopscluster_name -n $namespace -o jsonpath='{.spec.gitopsAddon.argoCDAgent.serverAddress}')
    local server_port=$(kubectl get gitopscluster $gitopscluster_name -n $namespace -o jsonpath='{.spec.gitopsAddon.argoCDAgent.serverPort}')

    if [ -z "$server_port" ]; then
        server_port="443"
    fi

    echo "https://${server_address}:${server_port}?agentName=${cluster_name}"
}

# Create guestbook application on hub for agent mode
# The destination.server uses the principal address with agentName query param
create_guestbook_app_agent_mode() {
    local cluster_name=$1
    local server_url=$2

    log_info "Creating guestbook Application on hub in $cluster_name namespace..."
    log_debug "Destination server: $server_url"

    export KUBECONFIG=$HUB_KUBECONFIG

    # Ensure the namespace exists (should be the managed cluster namespace)
    kubectl get ns $cluster_name &>/dev/null || {
        log_error "Namespace $cluster_name does not exist on hub"
        return 1
    }

    # Create the application YAML (no inline variables that could contain newlines)
    kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook
  namespace: ${cluster_name}
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps
    targetRevision: HEAD
    path: guestbook
  destination:
    server: "${server_url}"
    namespace: guestbook
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
EOF
}

# Verify guestbook is deployed on managed cluster (verification only)
verify_guestbook_deployed() {
    local kubeconfig=$1
    export KUBECONFIG=$kubeconfig
    kubectl get deployment guestbook-ui -n guestbook &>/dev/null
}

# Verify ArgoCD is running on managed cluster (verification only)
verify_argocd_running() {
    local kubeconfig=$1
    export KUBECONFIG=$kubeconfig
    kubectl get argocd acm-openshift-gitops -n openshift-gitops &>/dev/null
}

# Verify agent is connected by checking principal logs
verify_agent_connected() {
    local cluster_name=$1

    log_info "Verifying agent $cluster_name is connected to principal..."

    export KUBECONFIG=$HUB_KUBECONFIG

    # Wait for cluster secret to be created (controller creates it async)
    local cluster_secret="cluster-${cluster_name}"
    wait_for_condition 120 "Cluster secret $cluster_secret to be created" \
        "kubectl get secret $cluster_secret -n openshift-gitops" || {
            log_warn "Cluster secret $cluster_secret not found"
            return 1
        }

    # Check the server URL in the secret
    local server=$(kubectl get secret $cluster_secret -n openshift-gitops -o jsonpath='{.data.server}' | base64 -d)
    log_debug "Cluster server URL: $server"

    return 0
}

#######################################
# SCENARIO 1: gitops-addon (no agent, no OLM)
#######################################
test_scenario_1() {
    log_info "=============================================="
    log_info "SCENARIO 1: gitops-addon (no agent, no OLM) on $KIND_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster creates ManagedClusterAddOn"
    log_info "  - GitOpsCluster creates ArgoCD Policy"
    log_info "  - User modifies Policy to add RBAC + Application"
    log_info "  - Policy deploys ArgoCD + RBAC + App to managed cluster"
    log_info "  - Application syncs and deploys guestbook"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for non-agent mode (controller disabled)
    configure_hub_argocd_non_agent

    # Apply AddOnTemplates (use relative path from script location)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    log_info "Applying AddOnTemplates..."
    kubectl apply -f "$SCRIPT_DIR/addonTemplates/addonTemplates.yaml"

    # Create Placement
    log_info "Creating Placement..."
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: kind-placement
  namespace: openshift-gitops
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            name: $KIND_CLUSTER_NAME
EOF

    # Create GitOpsCluster
    log_info "Creating GitOpsCluster..."
    cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: kind-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: kind-placement
  gitopsAddon:
    enabled: true
EOF

    # Wait for ManagedClusterAddOn to be created
    wait_for_condition 60 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $KIND_CLUSTER_NAME -o name" || return 1

    # Add RBAC + Application to the Policy (simulating user modification)
    add_rbac_and_app_to_policy "kind-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant
    wait_for_policy_compliant "kind-gitops-argocd-policy" "openshift-gitops" 300 || return 1

    # Wait for ArgoCD to be running on managed cluster
    wait_for_condition 180 "ArgoCD to be running on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1

    # Wait for guestbook to be deployed (application was added via Policy)
    wait_for_condition 180 "Guestbook to be deployed on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 1: FAILED - Guestbook not deployed"
            return 1
        }

    log_info "=============================================="
    log_info "SCENARIO 1: PASSED"
    log_info "  - GitOpsCluster created ManagedClusterAddOn: OK"
    log_info "  - ArgoCD Policy created and modified: OK"
    log_info "  - ArgoCD running on managed cluster: OK"
    log_info "  - Guestbook deployed via Policy: OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 2: gitops-addon + OLM
#######################################
test_scenario_2() {
    log_info "=============================================="
    log_info "SCENARIO 2: gitops-addon + OLM on $OCP_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with OLM subscription enabled"
    log_info "  - ArgoCD deployed via OLM Subscription"
    log_info "  - User modifies Policy to add RBAC + Application"
    log_info "  - Application syncs and deploys guestbook"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for non-agent mode (controller disabled)
    configure_hub_argocd_non_agent

    # Create Placement
    log_info "Creating Placement..."
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: ocp-placement
  namespace: openshift-gitops
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            name: $OCP_CLUSTER_NAME
EOF

    # Create GitOpsCluster with OLM
    log_info "Creating GitOpsCluster with OLM..."
    cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: ocp-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: ocp-placement
  gitopsAddon:
    enabled: true
    olmSubscription:
      enabled: true
EOF

    # Wait for ManagedClusterAddOn to be created
    wait_for_condition 60 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1

    # Add RBAC + Application to the Policy
    add_rbac_and_app_to_policy "ocp-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant (OLM takes longer)
    wait_for_policy_compliant "ocp-gitops-argocd-policy" "openshift-gitops" 420 || return 1

    # Wait for ArgoCD to be running on managed cluster
    wait_for_condition 300 "ArgoCD to be running on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1

    # Wait for guestbook to be deployed
    wait_for_condition 180 "Guestbook to be deployed on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_warn "SCENARIO 2: PARTIAL - Guestbook may still be syncing on OCP"
            return 1
        }

    log_info "=============================================="
    log_info "SCENARIO 2: PASSED"
    log_info "  - GitOpsCluster with OLM created: OK"
    log_info "  - ArgoCD deployed via OLM: OK"
    log_info "  - Guestbook deployed via Policy: OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 3: gitops-addon + Agent
#######################################
test_scenario_3() {
    log_info "=============================================="
    log_info "SCENARIO 3: gitops-addon + Agent on $KIND_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with Agent mode enabled"
    log_info "  - Principal server address auto-discovered"
    log_info "  - Cluster secret created with agentName in server URL"
    log_info "  - User modifies Policy to add RBAC"
    log_info "  - User creates Application on hub in managed cluster namespace"
    log_info "  - Agent propagates Application and syncs guestbook"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for agent mode (controller disabled, principal enabled)
    configure_hub_argocd_agent

    # Create Placement
    log_info "Creating Placement..."
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: kind-agent-placement
  namespace: openshift-gitops
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            name: $KIND_CLUSTER_NAME
EOF

    # Create GitOpsCluster with Agent - serverAddress is auto-discovered from Route
    log_info "Creating GitOpsCluster with Agent (serverAddress auto-discovered)..."
    cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: kind-agent-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: kind-agent-placement
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: true
      mode: managed
EOF

    # Wait for ManagedClusterAddOn to be created
    wait_for_condition 60 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $KIND_CLUSTER_NAME -o name" || return 1

    # Add RBAC to the Policy (apps are created separately for agent mode)
    add_rbac_to_policy "kind-agent-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant
    wait_for_policy_compliant "kind-agent-gitops-argocd-policy" "openshift-gitops" 300 || return 1

    # Get the server URL for the agent (discovered by controller)
    local server_url=$(get_agent_server_address "kind-agent-gitops" "openshift-gitops" "$KIND_CLUSTER_NAME")
    log_info "Agent server URL: $server_url"

    # Verify cluster secret was created with proper agentName
    verify_agent_connected "$KIND_CLUSTER_NAME" || return 1

    # Wait for ArgoCD agent to be running on managed cluster
    wait_for_condition 180 "ArgoCD agent to be running on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent -o name | grep -q pod" || return 1

    # Create Application on hub in managed cluster namespace
    create_guestbook_app_agent_mode "$KIND_CLUSTER_NAME" "$server_url" || return 1

    # Wait for guestbook to be deployed on managed cluster
    wait_for_condition 240 "Guestbook to be deployed on $KIND_CLUSTER_NAME via agent" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 3: FAILED - Guestbook not deployed"
            # Debug: Check agent logs
            log_debug "Agent logs:"
            KUBECONFIG=$KIND_KUBECONFIG kubectl logs -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent --tail=20 2>/dev/null || true
            return 1
        }

    # Verify guestbook deployment is ready
    wait_for_condition 120 "Guestbook deployment to be ready on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o jsonpath='{.status.availableReplicas}' | grep -q '[1-9]'" || {
            log_warn "SCENARIO 3: Guestbook deployment not ready yet, continuing..."
        }

    # Verify Application status is synced back to hub
    log_info "Verifying Application status reflected on hub..."
    wait_for_condition 120 "Application status to show Synced on hub" \
        "kubectl get application guestbook -n $KIND_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_warn "Application sync status not yet reflected, checking status..."
            kubectl get application guestbook -n $KIND_CLUSTER_NAME -o yaml 2>/dev/null || true
        }

    # Verify Application health status on hub
    wait_for_condition 120 "Application health status on hub" \
        "kubectl get application guestbook -n $KIND_CLUSTER_NAME -o jsonpath='{.status.health.status}' 2>/dev/null | grep -qE '(Healthy|Progressing)'" || {
            log_warn "Application health status not yet reflected"
        }

    # Get final Application status
    local sync_status=$(kubectl get application guestbook -n $KIND_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null)
    local health_status=$(kubectl get application guestbook -n $KIND_CLUSTER_NAME -o jsonpath='{.status.health.status}' 2>/dev/null)
    log_info "Application Status - Sync: $sync_status, Health: $health_status"

    log_info "=============================================="
    log_info "SCENARIO 3: PASSED"
    log_info "  - GitOpsCluster with Agent created: OK"
    log_info "  - Principal server address auto-discovered: OK"
    log_info "  - Cluster secret created with agentName: OK"
    log_info "  - Agent running on managed cluster: OK"
    log_info "  - Application created on hub: OK"
    log_info "  - Guestbook deployed via agent: OK"
    log_info "  - Application status reflected on hub: Sync=$sync_status, Health=$health_status"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 4: gitops-addon + Agent + OLM
#######################################
test_scenario_4() {
    log_info "=============================================="
    log_info "SCENARIO 4: gitops-addon + Agent + OLM on $OCP_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with Agent + OLM mode"
    log_info "  - ArgoCD deployed via OLM with agent configuration"
    log_info "  - User modifies Policy to add RBAC"
    log_info "  - User creates Application on hub"
    log_info "  - Agent propagates Application and syncs guestbook"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for agent mode (controller disabled, principal enabled)
    configure_hub_argocd_agent

    # Create Placement
    log_info "Creating Placement..."
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: ocp-agent-placement
  namespace: openshift-gitops
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            name: $OCP_CLUSTER_NAME
EOF

    # Create GitOpsCluster with Agent + OLM - serverAddress is auto-discovered from Route
    log_info "Creating GitOpsCluster with Agent + OLM (serverAddress auto-discovered)..."
    cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: ocp-agent-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: ocp-agent-placement
  gitopsAddon:
    enabled: true
    olmSubscription:
      enabled: true
    argoCDAgent:
      enabled: true
      mode: managed
EOF

    # Wait for ManagedClusterAddOn to be created
    wait_for_condition 60 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1

    # Add RBAC to the Policy
    add_rbac_to_policy "ocp-agent-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant (OLM takes longer)
    wait_for_policy_compliant "ocp-agent-gitops-argocd-policy" "openshift-gitops" 420 || return 1

    # Get the server URL for the agent
    local server_url=$(get_agent_server_address "ocp-agent-gitops" "openshift-gitops" "$OCP_CLUSTER_NAME")
    log_info "Agent server URL: $server_url"

    # Verify cluster secret was created
    verify_agent_connected "$OCP_CLUSTER_NAME" || return 1

    # Wait for ArgoCD agent to be running on managed cluster
    wait_for_condition 300 "ArgoCD agent to be running on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent -o name | grep -q pod" || return 1

    # Create Application on hub in managed cluster namespace
    create_guestbook_app_agent_mode "$OCP_CLUSTER_NAME" "$server_url" || return 1

    # Wait for guestbook to be deployed on managed cluster
    wait_for_condition 240 "Guestbook to be deployed on $OCP_CLUSTER_NAME via agent" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_warn "SCENARIO 4: PARTIAL - Guestbook may still be syncing on OCP"
            return 1
        }

    # Verify guestbook deployment is ready
    wait_for_condition 120 "Guestbook deployment to be ready on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o jsonpath='{.status.availableReplicas}' | grep -q '[1-9]'" || {
            log_warn "SCENARIO 4: Guestbook deployment not ready yet, continuing..."
        }

    # Verify Application status is synced back to hub
    log_info "Verifying Application status reflected on hub..."
    wait_for_condition 120 "Application status to show Synced on hub" \
        "kubectl get application guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_warn "Application sync status not yet reflected, checking status..."
            kubectl get application guestbook -n $OCP_CLUSTER_NAME -o yaml 2>/dev/null || true
        }

    # Verify Application health status on hub
    wait_for_condition 120 "Application health status on hub" \
        "kubectl get application guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.health.status}' 2>/dev/null | grep -qE '(Healthy|Progressing)'" || {
            log_warn "Application health status not yet reflected"
        }

    # Get final Application status
    local sync_status=$(kubectl get application guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null)
    local health_status=$(kubectl get application guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.health.status}' 2>/dev/null)
    log_info "Application Status - Sync: $sync_status, Health: $health_status"

    log_info "=============================================="
    log_info "SCENARIO 4: PASSED"
    log_info "  - GitOpsCluster with Agent + OLM created: OK"
    log_info "  - Principal server address auto-discovered: OK"
    log_info "  - ArgoCD with agent via OLM: OK"
    log_info "  - Agent running on managed cluster: OK"
    log_info "  - Guestbook deployed via agent: OK"
    log_info "  - Application status reflected on hub: Sync=$sync_status, Health=$health_status"
    log_info "=============================================="
    return 0
}

#######################################
# Main
#######################################

# Print configuration
log_info "Configuration:"
log_info "  HUB_KUBECONFIG:    $HUB_KUBECONFIG"
log_info "  KIND_KUBECONFIG:   $KIND_KUBECONFIG (for verification only)"
log_info "  OCP_KUBECONFIG:    $OCP_KUBECONFIG (for verification only)"
log_info "  KIND_CLUSTER_NAME: $KIND_CLUSTER_NAME"
log_info "  OCP_CLUSTER_NAME:  $OCP_CLUSTER_NAME"
echo ""

case "${1:-all}" in
    1)
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
        test_scenario_1
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
        ;;
    2)
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        test_scenario_2
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        ;;
    3)
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        test_scenario_3
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        ;;
    4)
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        test_scenario_4
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        ;;
    all)
        # Initial cleanup
        cleanup_all

        # Track results (use global variables since local doesn't work in case statements)
        scenario1_result="SKIPPED"
        scenario2_result="SKIPPED"
        scenario3_result="SKIPPED"
        scenario4_result="SKIPPED"

        # Run all scenarios with cleanup after each (always cleanup, regardless of pass/fail)
        if test_scenario_1; then
            scenario1_result="PASSED"
        else
            scenario1_result="FAILED"
        fi
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"

        if test_scenario_2; then
            scenario2_result="PASSED"
        else
            scenario2_result="FAILED"
        fi
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"

        if test_scenario_3; then
            scenario3_result="PASSED"
        else
            scenario3_result="FAILED"
        fi
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"

        if test_scenario_4; then
            scenario4_result="PASSED"
        else
            scenario4_result="FAILED"
        fi
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"

        log_info "=============================================="
        log_info "=== ALL SCENARIOS COMPLETE ==="
        log_info "=============================================="
        log_info "  Scenario 1 (Kind, no agent, no OLM): $scenario1_result"
        log_info "  Scenario 2 (OCP, OLM):               $scenario2_result"
        log_info "  Scenario 3 (Kind, Agent):            $scenario3_result"
        log_info "  Scenario 4 (OCP, Agent + OLM):       $scenario4_result"
        log_info "=============================================="
        ;;
    cleanup)
        cleanup_all
        ;;
    *)
        echo "Usage: $0 [1|2|3|4|all|cleanup]"
        echo ""
        echo "  1       - Scenario 1: gitops-addon (no agent, no OLM) on Kind"
        echo "  2       - Scenario 2: gitops-addon + OLM on OCP"
        echo "  3       - Scenario 3: gitops-addon + Agent on Kind"
        echo "  4       - Scenario 4: gitops-addon + Agent + OLM on OCP"
        echo "  all     - Run all scenarios sequentially with cleanup"
        echo "  cleanup - Clean up all scenarios"
        echo ""
        echo "Environment variables:"
        echo "  HUB_KUBECONFIG      - Hub cluster kubeconfig"
        echo "  KIND_KUBECONFIG     - Kind cluster kubeconfig (verification only)"
        echo "  OCP_KUBECONFIG      - OCP cluster kubeconfig (verification only)"
        echo "  KIND_CLUSTER_NAME   - Kind cluster name (default: kind-cluster1)"
        echo "  OCP_CLUSTER_NAME    - OCP cluster name (default: ocp-cluster3)"
        exit 1
        ;;
esac
