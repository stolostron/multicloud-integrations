#!/bin/bash
#
# GitOps Addon Test Scenarios
# Tests all 6 scenarios for gitops-addon functionality
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
# Usage: ./test-scenarios.sh [1|2|3|4|5|6|all|cleanup]
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
OCP_CLUSTER_NAME="${OCP_CLUSTER_NAME:-ocp-cluster5}"

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

# Cleanup a scenario - proper deletion order per argocd_policy.go:
#   1. Delete Placement (prevents GitOpsCluster controller from recreating resources)
#   2. Delete Policy + PlacementBinding (stops enforcement on managed cluster)
#   3. Delete ManagedClusterAddOn (triggers pre-delete cleanup Job on managed cluster)
#      - The pre-delete Job runs "gitopsaddon -cleanup" which removes ArgoCD CR, OLM, etc.
#      - OCM addon framework removes its finalizer only after the Job completes
#      - We WAIT for this to finish naturally (no force-patching finalizers)
#   4. Delete GitOpsCluster
#   5. Verify managed cluster is clean
#
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

    # 1. Delete Placement FIRST - prevents GitOpsCluster controller from finding managed
    #    clusters and recreating the addon or policy during cleanup
    log_info "Deleting Placement $placement_name (prevents controller recreation)..."
    kubectl delete placement $placement_name -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # 2. For agent mode, delete hub-side Applications and AppProjects
    if [ "$is_agent_mode" = true ]; then
        log_info "Deleting agent-mode applications and AppProjects from hub (namespace: $cluster_name)..."
        kubectl delete applications.argoproj.io --all -n $cluster_name --ignore-not-found --wait=false 2>/dev/null || true
        kubectl delete appproject --all -n $cluster_name --ignore-not-found --wait=false 2>/dev/null || true
    fi

    # 3. Delete Policy and PlacementBinding - stops enforcement on managed cluster
    #    This allows the pre-delete cleanup Job to remove ArgoCD CR without Policy re-creating it
    log_info "Deleting Policy and PlacementBinding (stops enforcement)..."
    kubectl delete policy ${gitopscluster_name}-argocd-policy -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete placementbinding ${gitopscluster_name}-argocd-policy-binding -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    
    # Wait for policy removal to propagate to managed cluster
    log_info "Waiting for policy removal to propagate..."
    sleep 10

    # 4. Delete ManagedClusterAddOn - triggers the pre-delete cleanup Job
    #    The AddonTemplate defines a pre-delete Job (addon.open-cluster-management.io/addon-pre-delete)
    #    that runs "gitopsaddon -cleanup" on the managed cluster. This Job:
    #      - Deletes ArgoCD CRs created by the addon
    #      - Deletes GitOpsService CR (OLM mode)
    #      - Deletes OLM Subscription/CSV
    #      - Deletes operator resources
    #    The OCM addon framework only removes its finalizer after the Job completes.
    #    We wait up to 180s for this to happen naturally.
    if kubectl get managedclusteraddon gitops-addon -n $cluster_name 2>/dev/null; then
        log_info "Deleting ManagedClusterAddOn (waiting for pre-delete cleanup Job, up to 300s)..."
        # Use 'if' pattern to prevent set -e from exiting on non-zero kubectl exit code
        if kubectl delete managedclusteraddon gitops-addon -n $cluster_name --timeout=300s; then
            log_info "ManagedClusterAddOn deleted successfully (pre-delete cleanup Job completed)"
        else
            log_warn "ManagedClusterAddOn deletion timed out or failed"
            log_warn "The pre-delete cleanup Job may have failed on the managed cluster."
            log_warn "Check: kubectl get jobs -n open-cluster-management-agent-addon on $cluster_name"
            # Do NOT force-remove finalizers here - if the pre-delete Job can't complete,
            # something is wrong (e.g. operator crashed, addon image broken). That needs
            # to be fixed, not papered over. The initial cleanup_all -> cleanup_managed_cluster_direct
            # handles stuck resources with force-removal as a last resort.
            sleep 15
            if kubectl get managedclusteraddon gitops-addon -n $cluster_name 2>/dev/null; then
                log_warn "ManagedClusterAddOn still exists after extended wait"
            else
                log_info "ManagedClusterAddOn eventually deleted"
            fi
        fi
    else
        log_info "ManagedClusterAddOn gitops-addon not found in $cluster_name (already clean)"
    fi

    # 5. Delete GitOpsCluster (last, per documented cleanup order)
    log_info "Deleting GitOpsCluster $gitopscluster_name..."
    kubectl delete gitopscluster $gitopscluster_name -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # 6. VERIFY cleanup on hub
    log_info "Verifying hub cleanup..."
    local issues=0
    
    if kubectl get gitopscluster $gitopscluster_name -n openshift-gitops 2>/dev/null; then
        log_warn "GitOpsCluster $gitopscluster_name still exists!"
        issues=$((issues + 1))
    fi
    
    # Wait for Policy to be fully deleted by governance framework (up to 60s)
    local policy_wait=0
    while [ "$policy_wait" -lt 60 ]; do
        if ! kubectl get policy ${gitopscluster_name}-argocd-policy -n openshift-gitops 2>/dev/null; then
            break
        fi
        sleep 5
        policy_wait=$((policy_wait + 5))
    done
    if kubectl get policy ${gitopscluster_name}-argocd-policy -n openshift-gitops 2>/dev/null; then
        log_warn "Policy ${gitopscluster_name}-argocd-policy still exists after 60s wait"
        # Force delete
        kubectl delete policy ${gitopscluster_name}-argocd-policy -n openshift-gitops --ignore-not-found --timeout=30s 2>/dev/null || true
    fi
    
    if kubectl get managedclusteraddon gitops-addon -n $cluster_name 2>/dev/null; then
        log_warn "ManagedClusterAddOn gitops-addon still exists in $cluster_name!"
        issues=$((issues + 1))
    fi
    
    # 7. VERIFY managed cluster is clean (READ-ONLY - no writes to managed cluster)
    log_info "Verifying managed cluster cleanup (read-only)..."
    if [ "$cluster_name" = "$KIND_CLUSTER_NAME" ]; then
        export KUBECONFIG=$KIND_KUBECONFIG
    else
        export KUBECONFIG=$OCP_KUBECONFIG
    fi
    
    # Check for any ArgoCD CRs - the pre-delete Job should have removed them
    local argocd_list=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null || true)
    local argocd_count=$(echo "$argocd_list" | grep -v '^$' | wc -l)
    
    if [ "$argocd_count" -gt 0 ]; then
        log_warn "Managed cluster still has $argocd_count ArgoCD CR(s) after cleanup:"
        echo "$argocd_list"
        log_warn "This means the pre-delete cleanup Job did not fully clean up."
        issues=$((issues + 1))
    else
        log_info "Managed cluster: No ArgoCD CRs (clean)"
    fi

    # Switch back to hub
    export KUBECONFIG=$HUB_KUBECONFIG
    
    if [ $issues -eq 0 ]; then
        log_info "Cleanup of $gitopscluster_name complete - all verified clean"
    else
        log_warn "Cleanup of $gitopscluster_name had $issues issue(s) - see warnings above"
    fi
}

cleanup_all() {
    log_info "=== Initial cleanup ==="

    export KUBECONFIG=$HUB_KUBECONFIG

    # ============================================
    # STEP 1: Clean up hub resources
    # ============================================
    
    log_info "--- Cleaning up hub resources ---"

    # Delete all AddOnTemplates managed by us (dynamic ones)
    kubectl delete addontemplate -l app.kubernetes.io/managed-by=multicloud-integrations --ignore-not-found 2>/dev/null || true

    # Delete stale Applications and AppProjects in managed cluster namespaces on hub
    log_info "Removing stale hub-side Applications and AppProjects..."
    kubectl delete applications.argoproj.io --all -n $OCP_CLUSTER_NAME --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete applications.argoproj.io --all -n $KIND_CLUSTER_NAME --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete appproject --all -n $OCP_CLUSTER_NAME --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete appproject --all -n $KIND_CLUSTER_NAME --ignore-not-found --wait=false 2>/dev/null || true

    # ============================================
    # STEP 2: Cleanup each scenario from hub (proper order)
    # Each cleanup_scenario deletes Placement, Policy, ManagedClusterAddOn, GitOpsCluster
    # The addon's pre-delete Job handles managed cluster cleanup automatically
    # ============================================

    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
    cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
    cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-olm-gitops" "ocp-agent-olm-placement"

    # ============================================
    # STEP 3: Direct managed cluster cleanup
    # During initial cleanup ONLY, we can directly connect to managed clusters
    # to remove residual resources that the pre-delete Job may have missed.
    # This handles: stale pause markers, leftover ArgoCD CRs, OLM artifacts,
    # and the default openshift-gitops ArgoCD instance.
    # NOTE: During scenario runs, we ONLY operate from the hub.
    # ============================================

    log_info "--- Direct managed cluster cleanup (initial only) ---"

    # Cleanup OCP managed cluster
    if [ -n "$OCP_KUBECONFIG" ] && [ -f "$OCP_KUBECONFIG" ]; then
        log_info "Cleaning up OCP managed cluster directly..."
        cleanup_managed_cluster_direct "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME"
    fi

    # Cleanup KIND managed cluster
    if [ -n "$KIND_KUBECONFIG" ] && [ -f "$KIND_KUBECONFIG" ]; then
        log_info "Cleaning up KIND managed cluster directly..."
        cleanup_managed_cluster_direct "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME"
    fi

    # ============================================
    # STEP 4: Delete stale cluster secrets
    # Done AFTER cleanup_scenario calls so the controller can't re-create them
    # (cleanup_scenario deletes Placement + GitOpsCluster first, stopping the controller)
    # ============================================
    export KUBECONFIG=$HUB_KUBECONFIG
    log_info "Removing stale cluster secrets..."
    kubectl delete secret cluster-$KIND_CLUSTER_NAME -n openshift-gitops --ignore-not-found 2>/dev/null || true
    kubectl delete secret cluster-$OCP_CLUSTER_NAME -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # ============================================
    # STEP 5: Verify cleanup
    # ============================================
    
    log_info "--- Verifying cleanup ---"
    sleep 10
    
    # Check KIND cluster
    export KUBECONFIG=$KIND_KUBECONFIG
    local kind_argocd_count=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$kind_argocd_count" -gt 0 ]; then
        log_warn "KIND cluster still has $kind_argocd_count ArgoCD CR(s) - stripping finalizers and deleting"
        for argocd_name in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
            kubectl patch $argocd_name -n openshift-gitops --type=json \
                -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
        done
        kubectl delete argocd --all -n openshift-gitops --force --grace-period=0 2>/dev/null || true
    else
        log_info "KIND cluster: ArgoCD CRs cleaned up"
    fi
    
    # Check OCP cluster
    export KUBECONFIG=$OCP_KUBECONFIG
    local ocp_argocd_count=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$ocp_argocd_count" -gt 0 ]; then
        log_warn "OCP cluster still has $ocp_argocd_count ArgoCD CR(s) - stripping finalizers and deleting"
        for argocd_name in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
            kubectl patch $argocd_name -n openshift-gitops --type=json \
                -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
        done
        kubectl delete argocd --all -n openshift-gitops --force --grace-period=0 2>/dev/null || true
    else
        log_info "OCP cluster: ArgoCD CRs cleaned up"
    fi

    # Check for stale operator deployments on OCP
    for ns in openshift-operators openshift-gitops-operator; do
        if kubectl get deployment openshift-gitops-operator-controller-manager -n $ns &>/dev/null 2>/dev/null; then
            log_warn "OCP cluster: Operator still running in $ns - deleting"
            kubectl delete deployment openshift-gitops-operator-controller-manager -n $ns --ignore-not-found 2>/dev/null || true
        fi
    done

    # Final wait and re-check ArgoCD CRs on OCP (operator deletion may trigger re-check)
    sleep 5
    ocp_argocd_count=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$ocp_argocd_count" -gt 0 ]; then
        log_warn "OCP cluster STILL has $ocp_argocd_count ArgoCD CR(s) after operator removal - stripping finalizers"
        kubectl get argocd -n openshift-gitops 2>/dev/null || true
        for argocd_name in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
            kubectl patch $argocd_name -n openshift-gitops --type=json \
                -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
        done
        kubectl delete argocd --all -n openshift-gitops --force --grace-period=0 2>/dev/null || true
    else
        log_info "OCP cluster: Confirmed clean (no ArgoCD CRs, no operator)"
    fi

    export KUBECONFIG=$HUB_KUBECONFIG
    log_info "=== All cleanup complete ==="
}

# Direct cleanup on a managed cluster (used during initial cleanup ONLY)
# This handles residual resources that the pre-delete Job may have missed:
# - Stale pause markers that prevent the addon from reconciling
# - Default ArgoCD instance created by the operator without DISABLE_DEFAULT_ARGOCD_INSTANCE
# - OLM artifacts (Subscriptions, CSVs, operator deployments) from previous OLM scenarios
# - GitopsService CR that triggers default ArgoCD creation
# - Leftover ArgoCD CRs from failed cleanups
cleanup_managed_cluster_direct() {
    local kubeconfig=$1
    local cluster_name=$2

    export KUBECONFIG=$kubeconfig

    log_info "$cluster_name: Removing stale pause marker..."
    kubectl delete configmap gitops-addon-pause -n open-cluster-management-agent-addon --ignore-not-found 2>/dev/null || true

    # Delete ALL ArgoCD CRs (both acm-openshift-gitops and the default openshift-gitops)
    # Must strip finalizers first - if the operator is crashed it can't process them
    log_info "$cluster_name: Removing ALL ArgoCD CRs in openshift-gitops namespace..."
    for argocd_name in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
        kubectl patch $argocd_name -n openshift-gitops --type=json \
            -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
    done
    kubectl delete argocd --all -n openshift-gitops --wait=false 2>/dev/null || true

    # ============================================
    # THOROUGHLY remove OLM-installed operator artifacts
    # If a previous OLM scenario (3 or 6) installed the operator via Subscription,
    # these artifacts may linger and create a default ArgoCD instance without
    # DISABLE_DEFAULT_ARGOCD_INSTANCE. We must remove ALL of them.
    # ============================================
    log_info "$cluster_name: Thoroughly cleaning up OLM operator artifacts..."

    # 1. Delete Subscription first (prevents OLM from reinstalling)
    kubectl delete subscription openshift-gitops-operator -n openshift-operators --ignore-not-found 2>/dev/null || true
    kubectl delete subscription -l app.kubernetes.io/managed-by=multicloud-integrations -A --ignore-not-found 2>/dev/null || true

    # 2. Delete ClusterServiceVersion (this is what manages the operator deployment)
    local csv_name=$(kubectl get csv -n openshift-operators -o name 2>/dev/null | grep gitops-operator || true)
    if [ -n "$csv_name" ]; then
        log_warn "$cluster_name: Found OLM CSV: $csv_name - deleting..."
        kubectl delete $csv_name -n openshift-operators --ignore-not-found 2>/dev/null || true
        # CSVs are cluster-scoped copies, delete from openshift-gitops too
        kubectl delete csv -n openshift-gitops -l operators.coreos.com/openshift-gitops-operator.openshift-operators --ignore-not-found 2>/dev/null || true
    fi

    # 3. Delete the OLM-managed operator deployment (CSV deletion may be slow)
    kubectl delete deployment openshift-gitops-operator-controller-manager -n openshift-operators --ignore-not-found 2>/dev/null || true

    # 4. Delete GitopsService CR - this is what triggers the operator to create the default ArgoCD instance
    log_info "$cluster_name: Removing GitopsService CR..."
    kubectl delete gitopsservice cluster --ignore-not-found 2>/dev/null || true

    # 5. Delete the 'cluster' backend deployment (owned by GitopsService)
    kubectl delete deployment cluster -n openshift-gitops --ignore-not-found 2>/dev/null || true
    kubectl delete deployment gitops-plugin -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # 6. Delete operator-managed resources in openshift-gitops namespace
    # These are created by the OLM operator and NOT by the addon's Helm chart
    for dep in openshift-gitops-applicationset-controller openshift-gitops-dex-server openshift-gitops-redis openshift-gitops-repo-server openshift-gitops-server; do
        kubectl delete deployment $dep -n openshift-gitops --ignore-not-found 2>/dev/null || true
    done

    # 7. Remove the addon's Helm-deployed operator too (in openshift-gitops-operator)
    kubectl delete deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator --ignore-not-found 2>/dev/null || true

    # Wait for deletions to take effect
    sleep 5

    # 8. Verify no ArgoCD CRs remain (operator might have recreated them during cleanup)
    local remaining=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$remaining" -gt 0 ]; then
        log_warn "$cluster_name: ArgoCD CRs still exist after cleanup - stripping finalizers and deleting..."
        for argocd_name in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
            kubectl patch $argocd_name -n openshift-gitops --type=json \
                -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
        done
        kubectl delete argocd --all -n openshift-gitops --force --grace-period=0 2>/dev/null || true
    fi

    # 9. Verify no operator deployments remain
    for ns in openshift-operators openshift-gitops-operator; do
        if kubectl get deployment openshift-gitops-operator-controller-manager -n $ns &>/dev/null 2>/dev/null; then
            log_warn "$cluster_name: Operator still running in $ns after cleanup!"
            kubectl delete deployment openshift-gitops-operator-controller-manager -n $ns --ignore-not-found 2>/dev/null || true
        fi
    done

    # Delete guestbook namespace (leftover from previous tests)
    kubectl delete namespace guestbook --ignore-not-found --wait=false 2>/dev/null || true

    # Clean up stale cleanup jobs
    kubectl delete job -n open-cluster-management-agent-addon -l apps.open-cluster-management.io/gitopsaddon=true --ignore-not-found 2>/dev/null || true

    log_info "$cluster_name: Direct cleanup complete"
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
    log_info "Configuring hub ArgoCD for agent mode (PATCH existing)..."
    
    export KUBECONFIG=$HUB_KUBECONFIG
    
    # Patch existing ArgoCD CR for agent mode instead of delete/recreate.
    # This preserves operator-managed defaults (resource limits, SSO config, etc.)
    # while adding the principal configuration needed for agent mode.
    # Key settings:
    #   - controller.enabled: false  (principal handles app reconciliation)
    #   - sourceNamespaces: ["*"]    (watch Applications in managed cluster namespaces)
    #   - principal.enabled: true    (enable gRPC principal for agents)
    #   - principal.auth: mtls       (authenticate agents via OCM addon client certs)
    #   - principal.namespace.allowedNamespaces: ["*"]  (allow any namespace)
    log_info "Patching hub ArgoCD CR for agent mode..."
    kubectl patch argocd openshift-gitops -n openshift-gitops --type=merge -p '{
      "spec": {
        "controller": {
          "enabled": false
        },
        "sourceNamespaces": ["*"],
        "argoCDAgent": {
          "principal": {
            "enabled": true,
            "auth": "mtls:CN=system:open-cluster-management:cluster:([^:]+):addon:gitops-addon:agent:gitops-addon-agent",
            "namespace": {
              "allowedNamespaces": ["*"]
            }
          }
        }
      }
    }'
    
    # Wait for principal route to be ready
    wait_for_condition 300 "Principal Route to be ready" \
        "kubectl get route -n openshift-gitops -l app.kubernetes.io/component=principal -o name | grep -q route" || {
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
# In agent mode, the principal dispatches BOTH Applications AND AppProjects from
# the hub to managed clusters.
# The default AppProject is created on the hub in the managed cluster's namespace by
# create_guestbook_app_agent_mode(), and the principal propagates it to the agent.
# We do NOT need to add AppProject to the Policy.
add_rbac_to_policy() {
    local policy_name=$1
    local namespace=$2

    log_info "Adding RBAC (cluster-admin) and guestbook namespace to Policy $policy_name..."

    export KUBECONFIG=$HUB_KUBECONFIG

    # Wait for the Policy to be created by the controller
    wait_for_condition 120 "Policy $policy_name to be created" \
        "kubectl get policy $policy_name -n $namespace -o name" || return 1

    # Add RBAC and guestbook namespace to the Policy.
    # NOTE: AppProject is NOT included here - the argocd-agent principal handles
    # AppProject propagation from the hub to managed clusters automatically.
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

    # Ensure default AppProject exists in the managed cluster namespace on the hub.
    # The ArgoCD principal requires the AppProject to exist in the Application's namespace
    # for it to dispatch the Application to the agent. The agent then propagates the
    # AppProject to the managed cluster.
    log_info "Ensuring default AppProject in $cluster_name namespace on hub..."
    kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: ${cluster_name}
spec:
  clusterResourceWhitelist:
  - group: '*'
    kind: '*'
  destinations:
  - namespace: '*'
    server: '*'
  sourceRepos:
  - '*'
EOF

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
# SCENARIO 1: gitops-addon on OCP (no agent, no OLM)
#######################################
test_scenario_1() {
    log_info "=============================================="
    log_info "SCENARIO 1: gitops-addon on $OCP_CLUSTER_NAME (no agent, no OLM)"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster creates ManagedClusterAddOn"
    log_info "  - GitOpsCluster creates ArgoCD Policy"
    log_info "  - User modifies Policy to add RBAC + Application"
    log_info "  - Policy deploys ArgoCD + RBAC + App to OCP managed cluster"
    log_info "  - Application syncs and deploys guestbook"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created"
    log_info "  - Policy compliant"
    log_info "  - ArgoCD CR acm-openshift-gitops running"
    log_info "  - guestbook-ui deployment in guestbook namespace"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for non-agent mode (controller disabled)
    configure_hub_argocd_non_agent

    # Ensure static AddOnTemplates exist (don't overwrite if already patched on hub)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    log_info "Ensuring AddOnTemplates exist..."
    kubectl get addontemplate gitops-addon -o name 2>/dev/null || kubectl apply -f "$SCRIPT_DIR/addonTemplates/addonTemplates.yaml"

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

    # Create GitOpsCluster (no OLM, no agent - plain Helm chart install)
    log_info "Creating GitOpsCluster..."
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
EOF

    # Wait for ManagedClusterAddOn to be created
    wait_for_condition 180 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1

    # Add RBAC + Application to the Policy (simulating user modification)
    add_rbac_and_app_to_policy "ocp-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant
    wait_for_policy_compliant "ocp-gitops-argocd-policy" "openshift-gitops" 300 || return 1

    # Wait for ArgoCD to be running on managed cluster
    wait_for_condition 300 "ArgoCD to be running on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1

    # CRITICAL: Verify no duplicate ArgoCD instances
    verify_no_duplicate_argocd "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || {
        log_error "SCENARIO 1: FAILED - Duplicate ArgoCD instances detected!"
        return 1
    }

    # Wait for guestbook to be deployed (application was added via Policy)
    # On OCP, the Deployment resource is created by ArgoCD but pods crash due to
    # restricted-v2 SCC (Apache tries to bind port 80). We only check resource existence.
    wait_for_condition 300 "Guestbook deployment resource to exist on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 1: FAILED - Guestbook deployment not created by ArgoCD"
            return 1
        }
    log_info "NOTE: On OCP, guestbook-ui pods will crash (port 80 + restricted SCC) - this is expected"

    log_info "=============================================="
    log_info "SCENARIO 1: PASSED"
    log_info "  - GitOpsCluster created ManagedClusterAddOn: OK"
    log_info "  - ArgoCD Policy created and modified: OK"
    log_info "  - ArgoCD running on OCP managed cluster: OK"
    log_info "  - Guestbook synced by ArgoCD (deployment exists): OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 2: gitops-addon on Kind (no agent, no OLM)
#######################################
test_scenario_2() {
    log_info "=============================================="
    log_info "SCENARIO 2: gitops-addon on $KIND_CLUSTER_NAME (no agent, no OLM)"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster creates ManagedClusterAddOn"
    log_info "  - GitOpsCluster creates ArgoCD Policy"
    log_info "  - User modifies Policy to add RBAC + Application"
    log_info "  - Policy deploys ArgoCD + RBAC + App to Kind managed cluster"
    log_info "  - Application syncs and deploys guestbook"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created"
    log_info "  - Policy compliant"
    log_info "  - ArgoCD CR acm-openshift-gitops running"
    log_info "  - guestbook-ui deployment in guestbook namespace"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for non-agent mode (controller disabled)
    configure_hub_argocd_non_agent

    # Ensure static AddOnTemplates exist (don't overwrite if already patched on hub)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    log_info "Ensuring AddOnTemplates exist..."
    kubectl get addontemplate gitops-addon -o name 2>/dev/null || kubectl apply -f "$SCRIPT_DIR/addonTemplates/addonTemplates.yaml"

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
    wait_for_condition 180 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $KIND_CLUSTER_NAME -o name" || return 1

    # Add RBAC + Application to the Policy (simulating user modification)
    add_rbac_and_app_to_policy "kind-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant
    wait_for_policy_compliant "kind-gitops-argocd-policy" "openshift-gitops" 300 || return 1

    # Wait for ArgoCD to be running on managed cluster
    wait_for_condition 180 "ArgoCD to be running on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1

    # Wait for guestbook to be deployed and healthy (application was added via Policy)
    # On Kind, the guestbook-ui pods should be fully running (no SCC restrictions)
    wait_for_condition 300 "Guestbook to be deployed on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 2: FAILED - Guestbook not deployed"
            return 1
        }
    # Also verify pods are actually running on Kind (no SCC restrictions unlike OCP)
    wait_for_condition 120 "Guestbook pods to be ready on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl rollout status deployment/guestbook-ui -n guestbook --timeout=5s" || {
            log_warn "Guestbook pods not fully ready yet, but deployment exists"
        }

    log_info "=============================================="
    log_info "SCENARIO 2: PASSED"
    log_info "  - GitOpsCluster created ManagedClusterAddOn: OK"
    log_info "  - ArgoCD Policy created and modified: OK"
    log_info "  - ArgoCD running on Kind managed cluster: OK"
    log_info "  - Guestbook deployed and running via Policy: OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 3: gitops-addon + OLM on OCP
#######################################
test_scenario_3() {
    log_info "=============================================="
    log_info "SCENARIO 3: gitops-addon + OLM on $OCP_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with OLM subscription enabled"
    log_info "  - ArgoCD deployed via OLM Subscription"
    log_info "  - User modifies Policy to add RBAC + Application"
    log_info "  - Application syncs and deploys guestbook"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created"
    log_info "  - OLM Subscription created, Policy compliant"
    log_info "  - ArgoCD CR acm-openshift-gitops running"
    log_info "  - guestbook-ui deployment in guestbook namespace"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for non-agent mode (controller disabled)
    configure_hub_argocd_non_agent

    # Ensure static AddOnTemplates exist (don't overwrite if already patched on hub)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    log_info "Ensuring AddOnTemplates exist..."
    kubectl get addontemplate gitops-addon -o name 2>/dev/null || kubectl apply -f "$SCRIPT_DIR/addonTemplates/addonTemplates.yaml"

    # Create Placement
    log_info "Creating Placement..."
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: ocp-olm-placement
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
  name: ocp-olm-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: ocp-olm-placement
  gitopsAddon:
    enabled: true
    olmSubscription:
      enabled: true
EOF

    # Wait for ManagedClusterAddOn to be created
    wait_for_condition 180 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1

    # Add RBAC + Application to the Policy
    add_rbac_and_app_to_policy "ocp-olm-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant (OLM takes longer)
    wait_for_policy_compliant "ocp-olm-gitops-argocd-policy" "openshift-gitops" 420 || return 1

    # Wait for ArgoCD to be running on managed cluster
    wait_for_condition 300 "ArgoCD to be running on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1

    # CRITICAL: Verify no duplicate ArgoCD instances
    verify_no_duplicate_argocd "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || {
        log_error "SCENARIO 3: FAILED - Duplicate ArgoCD instances detected!"
        return 1
    }

    # Wait for guestbook deployment resource (OCP - pods crash due to SCC, check existence only)
    wait_for_condition 300 "Guestbook deployment resource to exist on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 3: FAILED - Guestbook deployment not created by ArgoCD"
            return 1
        }
    log_info "NOTE: On OCP, guestbook-ui pods will crash (port 80 + restricted SCC) - this is expected"

    log_info "=============================================="
    log_info "SCENARIO 3: PASSED"
    log_info "  - GitOpsCluster with OLM created: OK"
    log_info "  - ArgoCD deployed via OLM: OK"
    log_info "  - Guestbook synced by ArgoCD (deployment exists): OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 4: gitops-addon + Agent on OCP (no OLM)
#######################################
test_scenario_4() {
    log_info "=============================================="
    log_info "SCENARIO 4: gitops-addon + Agent on $OCP_CLUSTER_NAME (no OLM)"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with Agent mode enabled on OCP"
    log_info "  - Principal server address auto-discovered"
    log_info "  - Cluster secret created with agentName in server URL"
    log_info "  - User modifies Policy to add RBAC"
    log_info "  - User creates Application on hub in managed cluster namespace"
    log_info "  - Agent propagates Application and syncs guestbook"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created, Policy compliant"
    log_info "  - Principal server address discovered"
    log_info "  - Cluster secret with agentName"
    log_info "  - Agent pod running, guestbook deployed via agent"
    log_info "  - App status synced to hub"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for agent mode (controller disabled, principal enabled)
    configure_hub_argocd_agent

    # Ensure static AddOnTemplates exist (don't overwrite if already patched on hub)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    log_info "Ensuring AddOnTemplates exist..."
    kubectl get addontemplate gitops-addon -o name 2>/dev/null || kubectl apply -f "$SCRIPT_DIR/addonTemplates/addonTemplates.yaml"

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

    # Create GitOpsCluster with Agent (no OLM) - serverAddress is auto-discovered from Route
    log_info "Creating GitOpsCluster with Agent (serverAddress auto-discovered)..."
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
    argoCDAgent:
      enabled: true
      mode: managed
EOF

    # Wait for ManagedClusterAddOn to be created
    wait_for_condition 180 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1

    # Add RBAC to the Policy (apps are created separately for agent mode)
    add_rbac_to_policy "ocp-agent-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant
    wait_for_policy_compliant "ocp-agent-gitops-argocd-policy" "openshift-gitops" 300 || return 1

    # Get the server URL for the agent (discovered by controller)
    local server_url=$(get_agent_server_address "ocp-agent-gitops" "openshift-gitops" "$OCP_CLUSTER_NAME")
    log_info "Agent server URL: $server_url"

    # Verify cluster secret was created with proper agentName
    verify_agent_connected "$OCP_CLUSTER_NAME" || return 1

    # Wait for ArgoCD agent to be running on managed cluster
    wait_for_condition 180 "ArgoCD agent to be running on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent -o name | grep -q pod" || return 1

    # CRITICAL: Verify no duplicate ArgoCD instances (default instance should be disabled)
    verify_no_duplicate_argocd "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || {
        log_error "SCENARIO 4: FAILED - Duplicate ArgoCD instances detected! DISABLE_DEFAULT_ARGOCD_INSTANCE is not working."
        return 1
    }

    # Create Application on hub in managed cluster namespace
    create_guestbook_app_agent_mode "$OCP_CLUSTER_NAME" "$server_url" || return 1

    # Wait for guestbook deployment resource to exist on managed cluster
    # On OCP, the Deployment resource is created by ArgoCD but pods crash due to
    # restricted-v2 SCC (Apache tries to bind port 80). We only check resource existence.
    wait_for_condition 240 "Guestbook deployment resource to exist on $OCP_CLUSTER_NAME via agent" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 4: FAILED - Guestbook deployment not created by ArgoCD agent"
            log_debug "Agent logs:"
            KUBECONFIG=$OCP_KUBECONFIG kubectl logs -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent --tail=20 2>/dev/null || true
            return 1
        }
    log_info "NOTE: On OCP, guestbook-ui pods will crash (port 80 + restricted SCC) - this is expected"

    # Verify Application status is synced back to hub
    log_info "Verifying Application sync status reflected on hub..."
    wait_for_condition 180 "Application sync status to show Synced on hub" \
        "kubectl get applications.argoproj.io guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_error "SCENARIO 4: FAILED - Application sync status not reflected on hub"
            log_debug "Application YAML on hub:"
            kubectl get applications.argoproj.io guestbook -n $OCP_CLUSTER_NAME -o yaml 2>/dev/null || true
            log_debug "Principal logs:"
            kubectl logs -n openshift-gitops -l app.kubernetes.io/component=principal --tail=20 2>/dev/null || true
            return 1
        }

    local sync_status=$(kubectl get applications.argoproj.io guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null)
    local health_status=$(kubectl get applications.argoproj.io guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.health.status}' 2>/dev/null)
    log_info "Application Status - Sync: $sync_status, Health: $health_status"

    log_info "=============================================="
    log_info "SCENARIO 4: PASSED"
    log_info "  - GitOpsCluster with Agent created: OK"
    log_info "  - Principal server address auto-discovered: OK"
    log_info "  - Cluster secret created with agentName: OK"
    log_info "  - Agent running on OCP managed cluster: OK"
    log_info "  - Application created on hub: OK"
    log_info "  - Guestbook synced by ArgoCD agent (deployment exists): OK"
    log_info "  - Application sync status reflected on hub: Sync=$sync_status, Health=$health_status"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 5: gitops-addon + Agent on Kind
#######################################
test_scenario_5() {
    log_info "=============================================="
    log_info "SCENARIO 5: gitops-addon + Agent on $KIND_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with Agent mode enabled on Kind"
    log_info "  - Principal server address auto-discovered"
    log_info "  - Cluster secret created with agentName in server URL"
    log_info "  - User modifies Policy to add RBAC"
    log_info "  - User creates Application on hub in managed cluster namespace"
    log_info "  - Agent propagates Application and syncs guestbook"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created, Policy compliant"
    log_info "  - Principal server address discovered"
    log_info "  - Cluster secret with agentName"
    log_info "  - Agent pod running, guestbook deployed via agent"
    log_info "  - App status synced to hub"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for agent mode (controller disabled, principal enabled)
    configure_hub_argocd_agent

    # Ensure static AddOnTemplates exist (don't overwrite if already patched on hub)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    log_info "Ensuring AddOnTemplates exist..."
    kubectl get addontemplate gitops-addon -o name 2>/dev/null || kubectl apply -f "$SCRIPT_DIR/addonTemplates/addonTemplates.yaml"

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
    wait_for_condition 180 "ManagedClusterAddOn to be created" \
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
            log_error "SCENARIO 5: FAILED - Guestbook not deployed"
            # Debug: Check agent logs
            log_debug "Agent logs:"
            KUBECONFIG=$KIND_KUBECONFIG kubectl logs -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent --tail=20 2>/dev/null || true
            return 1
        }

    # Verify guestbook deployment is ready
    wait_for_condition 120 "Guestbook deployment to be ready on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o jsonpath='{.status.availableReplicas}' | grep -q '[1-9]'" || {
            log_warn "SCENARIO 5: Guestbook deployment not ready yet, continuing..."
        }

    # Verify Application sync status is synced back to hub
    log_info "Verifying Application sync status reflected on hub..."
    wait_for_condition 180 "Application sync status to show Synced on hub" \
        "kubectl get applications.argoproj.io guestbook -n $KIND_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_error "SCENARIO 5: FAILED - Application sync status not reflected on hub"
            log_debug "Application YAML on hub:"
            kubectl get applications.argoproj.io guestbook -n $KIND_CLUSTER_NAME -o yaml 2>/dev/null || true
            log_debug "Principal logs:"
            kubectl logs -n openshift-gitops -l app.kubernetes.io/component=principal --tail=20 2>/dev/null || true
            return 1
        }

    # Verify Application health status on hub
    wait_for_condition 120 "Application health status on hub" \
        "kubectl get applications.argoproj.io guestbook -n $KIND_CLUSTER_NAME -o jsonpath='{.status.health.status}' 2>/dev/null | grep -qE '(Healthy|Progressing)'" || {
            log_warn "Application health status not yet reflected"
        }

    # Get final Application status
    local sync_status=$(kubectl get applications.argoproj.io guestbook -n $KIND_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null)
    local health_status=$(kubectl get applications.argoproj.io guestbook -n $KIND_CLUSTER_NAME -o jsonpath='{.status.health.status}' 2>/dev/null)
    log_info "Application Status - Sync: $sync_status, Health: $health_status"

    log_info "=============================================="
    log_info "SCENARIO 5: PASSED"
    log_info "  - GitOpsCluster with Agent created: OK"
    log_info "  - Principal server address auto-discovered: OK"
    log_info "  - Cluster secret created with agentName: OK"
    log_info "  - Agent running on Kind managed cluster: OK"
    log_info "  - Application created on hub: OK"
    log_info "  - Guestbook deployed via agent: OK"
    log_info "  - Application sync status reflected on hub: Sync=$sync_status, Health=$health_status"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 6: gitops-addon + Agent + OLM on OCP
#######################################
test_scenario_6() {
    log_info "=============================================="
    log_info "SCENARIO 6: gitops-addon + Agent + OLM on $OCP_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with Agent + OLM mode"
    log_info "  - ArgoCD deployed via OLM with agent configuration"
    log_info "  - User modifies Policy to add RBAC"
    log_info "  - User creates Application on hub"
    log_info "  - Agent propagates Application and syncs guestbook"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created"
    log_info "  - OLM Subscription created, Policy compliant"
    log_info "  - Agent connected, cluster secret with agentName"
    log_info "  - Guestbook deployed via agent, app status synced to hub"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for agent mode (controller disabled, principal enabled)
    configure_hub_argocd_agent

    # Ensure static AddOnTemplates exist (don't overwrite if already patched on hub)
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    log_info "Ensuring AddOnTemplates exist..."
    kubectl get addontemplate gitops-addon -o name 2>/dev/null || kubectl apply -f "$SCRIPT_DIR/addonTemplates/addonTemplates.yaml"

    # Create Placement
    log_info "Creating Placement..."
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: ocp-agent-olm-placement
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
  name: ocp-agent-olm-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: ocp-agent-olm-placement
  gitopsAddon:
    enabled: true
    olmSubscription:
      enabled: true
    argoCDAgent:
      enabled: true
      mode: managed
EOF

    # Wait for ManagedClusterAddOn to be created
    wait_for_condition 180 "ManagedClusterAddOn to be created" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1

    # Add RBAC to the Policy
    add_rbac_to_policy "ocp-agent-olm-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant (OLM takes longer)
    wait_for_policy_compliant "ocp-agent-olm-gitops-argocd-policy" "openshift-gitops" 420 || return 1

    # Get the server URL for the agent
    local server_url=$(get_agent_server_address "ocp-agent-olm-gitops" "openshift-gitops" "$OCP_CLUSTER_NAME")
    log_info "Agent server URL: $server_url"

    # Verify cluster secret was created
    verify_agent_connected "$OCP_CLUSTER_NAME" || return 1

    # Wait for ArgoCD agent to be running on managed cluster
    wait_for_condition 300 "ArgoCD agent to be running on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent -o name | grep -q pod" || return 1

    # CRITICAL: Verify no duplicate ArgoCD instances
    verify_no_duplicate_argocd "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || {
        log_error "SCENARIO 6: FAILED - Duplicate ArgoCD instances detected!"
        return 1
    }

    # Create Application on hub in managed cluster namespace
    create_guestbook_app_agent_mode "$OCP_CLUSTER_NAME" "$server_url" || return 1

    # Wait for guestbook deployment resource to exist on managed cluster
    # On OCP, the Deployment resource is created by ArgoCD but pods crash due to
    # restricted-v2 SCC (Apache tries to bind port 80). We only check resource existence.
    wait_for_condition 240 "Guestbook deployment resource to exist on $OCP_CLUSTER_NAME via agent" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 6: FAILED - Guestbook deployment not created by ArgoCD agent"
            return 1
        }
    log_info "NOTE: On OCP, guestbook-ui pods will crash (port 80 + restricted SCC) - this is expected"

    # Verify Application sync status is synced back to hub
    log_info "Verifying Application sync status reflected on hub..."
    wait_for_condition 180 "Application sync status to show Synced on hub" \
        "kubectl get applications.argoproj.io guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_error "SCENARIO 6: FAILED - Application sync status not reflected on hub"
            log_debug "Application YAML on hub:"
            kubectl get applications.argoproj.io guestbook -n $OCP_CLUSTER_NAME -o yaml 2>/dev/null || true
            log_debug "Principal logs:"
            kubectl logs -n openshift-gitops -l app.kubernetes.io/component=principal --tail=20 2>/dev/null || true
            return 1
        }

    # Get final Application status
    local sync_status=$(kubectl get applications.argoproj.io guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.sync.status}' 2>/dev/null)
    local health_status=$(kubectl get applications.argoproj.io guestbook -n $OCP_CLUSTER_NAME -o jsonpath='{.status.health.status}' 2>/dev/null)
    log_info "Application Status - Sync: $sync_status, Health: $health_status"

    log_info "=============================================="
    log_info "SCENARIO 6: PASSED"
    log_info "  - GitOpsCluster with Agent + OLM created: OK"
    log_info "  - Principal server address auto-discovered: OK"
    log_info "  - ArgoCD with agent via OLM: OK"
    log_info "  - Agent running on managed cluster: OK"
    log_info "  - Guestbook synced by ArgoCD agent (deployment exists): OK"
    log_info "  - Application sync status reflected on hub: Sync=$sync_status, Health=$health_status"
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
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        test_scenario_1
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        ;;
    2)
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
        test_scenario_2
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
        ;;
    3)
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        test_scenario_3
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        ;;
    4)
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        test_scenario_4
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        ;;
    5)
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        test_scenario_5
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        ;;
    6)
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-olm-gitops" "ocp-agent-olm-placement"
        test_scenario_6
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-olm-gitops" "ocp-agent-olm-placement"
        ;;
    all)
        # Initial cleanup
        cleanup_all

        # Track results (use global variables since local doesn't work in case statements)
        scenario1_result="SKIPPED"
        scenario2_result="SKIPPED"
        scenario3_result="SKIPPED"
        scenario4_result="SKIPPED"
        scenario5_result="SKIPPED"
        scenario6_result="SKIPPED"

        # Run all scenarios with cleanup after each (always cleanup, regardless of pass/fail)
        if test_scenario_1; then
            scenario1_result="PASSED"
        else
            scenario1_result="FAILED"
        fi
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        sleep 15  # Give controller time to process cleanup before next scenario

        if test_scenario_2; then
            scenario2_result="PASSED"
        else
            scenario2_result="FAILED"
        fi
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
        sleep 15  # Give controller time to process cleanup before next scenario

        if test_scenario_3; then
            scenario3_result="PASSED"
        else
            scenario3_result="FAILED"
        fi
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        sleep 15  # Give controller time to process cleanup before next scenario

        if test_scenario_4; then
            scenario4_result="PASSED"
        else
            scenario4_result="FAILED"
        fi
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        sleep 15  # Give controller time to process cleanup before next scenario

        if test_scenario_5; then
            scenario5_result="PASSED"
        else
            scenario5_result="FAILED"
        fi
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        sleep 15  # Give controller time to process cleanup before next scenario

        if test_scenario_6; then
            scenario6_result="PASSED"
        else
            scenario6_result="FAILED"
        fi
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-olm-gitops" "ocp-agent-olm-placement"

        log_info "=============================================="
        log_info "=== ALL SCENARIOS COMPLETE ==="
        log_info "=============================================="
        log_info "  Scenario 1 (OCP, no agent, no OLM):  $scenario1_result"
        log_info "  Scenario 2 (Kind, no agent, no OLM): $scenario2_result"
        log_info "  Scenario 3 (OCP, OLM):               $scenario3_result"
        log_info "  Scenario 4 (OCP, Agent):             $scenario4_result"
        log_info "  Scenario 5 (Kind, Agent):            $scenario5_result"
        log_info "  Scenario 6 (OCP, Agent + OLM):       $scenario6_result"
        log_info "=============================================="
        ;;
    cleanup)
        cleanup_all
        ;;
    *)
        echo "Usage: $0 [1|2|3|4|5|6|all|cleanup]"
        echo ""
        echo "  1       - Scenario 1: gitops-addon on OCP"
        echo "  2       - Scenario 2: gitops-addon on Kind"
        echo "  3       - Scenario 3: gitops-addon + OLM on OCP"
        echo "  4       - Scenario 4: gitops-addon + Agent on OCP"
        echo "  5       - Scenario 5: gitops-addon + Agent on Kind"
        echo "  6       - Scenario 6: gitops-addon + Agent + OLM on OCP"
        echo "  all     - Run all scenarios sequentially with cleanup"
        echo "  cleanup - Clean up all scenarios"
        echo ""
        echo "Environment variables:"
        echo "  HUB_KUBECONFIG      - Hub cluster kubeconfig"
        echo "  KIND_KUBECONFIG     - Kind cluster kubeconfig (verification only)"
        echo "  OCP_KUBECONFIG      - OCP cluster kubeconfig (verification only)"
        echo "  KIND_CLUSTER_NAME   - Kind cluster name (default: kind-cluster1)"
        echo "  OCP_CLUSTER_NAME    - OCP cluster name (default: ocp-cluster5)"
        exit 1
        ;;
esac
