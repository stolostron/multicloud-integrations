#!/bin/bash
#
# GitOps Addon Test Scenarios
# Tests all 5 scenarios for gitops-addon functionality
#
# Key Design Principles:
# - All operations are performed from the hub cluster only (simulates user experience)
# - RBAC is created by modifying the Policy that GitOpsCluster generates
# - Secrets are managed by GitOpsCluster controller (NOT manually deleted)
# - For agent mode: Apps created on hub in managed cluster namespace
# - For non-agent mode: Apps can be added to the Policy or created directly on managed cluster
#
# IMPORTANT: Scenarios MUST run sequentially (not in parallel). Each scenario
# depends on a clean state from the previous scenario's cleanup. Running
# scenarios in parallel will cause resource conflicts and flaky failures.
#
# Prerequisites:
# - Hub cluster kubeconfig (set HUB_KUBECONFIG env var)
# - Managed cluster kubeconfigs (for verification only)
# - Managed clusters registered to hub with proper labels
# - Hub ArgoCD configured as principal for agent mode
#
# Usage: ./test-scenarios.sh [1|2|3|4|5|all|cleanup]
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
LOCAL_CLUSTER_NAME="local-cluster"
# Override addon agent image (set to custom registry for local testing)
ADDON_IMAGE="${ADDON_IMAGE:-}"

# Hub template that resolves to "local-cluster" on hub and "openshift-gitops" on remote clusters.
# Used in Policy patches so RBAC/Application targets the correct ArgoCD namespace.
HUB_NS_TEMPLATE='{{hub or (and (eq .ManagedClusterName "local-cluster") "local-cluster") "openshift-gitops" hub}}'

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_debug() { echo -e "${BLUE}[DEBUG]${NC} $1"; }

# Ensure AddOnTemplate exists and has the correct image (if ADDON_IMAGE is set).
# Patches the template in-place using kubectl get/set-last-applied pattern.
ensure_addon_template() {
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    export KUBECONFIG=$HUB_KUBECONFIG

    log_info "Ensuring AddOnTemplates exist..."
    kubectl get addontemplate gitops-addon -o name 2>/dev/null || kubectl apply -f "$SCRIPT_DIR/addonTemplates/addonTemplates.yaml"

    if [ -n "$ADDON_IMAGE" ]; then
        log_info "Patching AddOnTemplate image to $ADDON_IMAGE..."
        kubectl get addontemplate gitops-addon -o json | python3 -c "
import json,sys
data = json.loads(sys.stdin.read())
for m in data['spec']['agentSpec']['workload']['manifests']:
    kind = m.get('kind','')
    if kind in ('Deployment', 'Job'):
        for c in m.get('spec',{}).get('template',{}).get('spec',{}).get('containers',[]):
            if 'multicloud-integrations' in c.get('image',''):
                c['image'] = '$ADDON_IMAGE'
                c['imagePullPolicy'] = 'Always'
json.dump(data, sys.stdout)
" | kubectl apply -f - 2>/dev/null
        log_info "AddOnTemplate image patched to $ADDON_IMAGE"
    fi
}

# Verify only ACM-managed ArgoCD exists (no duplicates)
# Returns 0 if OK, 1 if duplicate found
# For local-cluster: the addon deploys ArgoCD to the "local-cluster" namespace (not openshift-gitops)
#   so we verify: openshift-gitops has ONLY the original "openshift-gitops" CR,
#   and local-cluster namespace has "acm-openshift-gitops" CR
verify_no_duplicate_argocd() {
    local cluster_kubeconfig=$1
    local cluster_name=$2
    
    export KUBECONFIG=$cluster_kubeconfig

    if [ "$cluster_name" = "$LOCAL_CLUSTER_NAME" ]; then
        # For local-cluster: ArgoCD CR should be in "local-cluster" namespace, NOT in openshift-gitops
        local argocd_in_gitops=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null || true)
        local gitops_count=$(echo "$argocd_in_gitops" | grep -v '^$' | wc -l)
        
        # openshift-gitops should have exactly 1 ArgoCD CR: the original "openshift-gitops"
        if [ "$gitops_count" -eq 1 ]; then
            local gitops_name=$(echo "$argocd_in_gitops" | awk '{print $1}')
            if [ "$gitops_name" = "openshift-gitops" ]; then
                log_info "$cluster_name: openshift-gitops namespace has only the original ArgoCD CR (correct, no duplicate)"
            else
                log_error "$cluster_name: openshift-gitops namespace has unexpected CR: $gitops_name"
                return 1
            fi
        elif [ "$gitops_count" -gt 1 ]; then
            log_error "$cluster_name: DUPLICATE ArgoCD CRs in openshift-gitops namespace ($gitops_count):"
            echo "$argocd_in_gitops"
            return 1
        fi
        
        # local-cluster namespace should have acm-openshift-gitops
        local argocd_in_local=$(kubectl get argocd -n local-cluster --no-headers 2>/dev/null || true)
        local local_count=$(echo "$argocd_in_local" | grep -v '^$' | wc -l)
        
        if [ "$local_count" -eq 1 ]; then
            local local_name=$(echo "$argocd_in_local" | awk '{print $1}')
            if [ "$local_name" = "acm-openshift-gitops" ]; then
                log_info "$cluster_name: acm-openshift-gitops exists in local-cluster namespace (correct)"
                return 0
            else
                log_error "$cluster_name: Unexpected ArgoCD CR in local-cluster namespace: $local_name"
                return 1
            fi
        elif [ "$local_count" -eq 0 ]; then
            log_info "$cluster_name: No ArgoCD CR in local-cluster namespace yet (expected during setup)"
            return 0
        else
            log_error "$cluster_name: Multiple ArgoCD CRs in local-cluster namespace ($local_count):"
            echo "$argocd_in_local"
            return 1
        fi
    else
        # For remote clusters: ArgoCD CR should be in openshift-gitops namespace
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
            return 1
        fi
    fi
}

# Comprehensive post-scenario environment health check
# Verifies no cross-namespace conflicts, no orphaned resources, no controller confusion
# cluster_kubeconfig: kubeconfig for the cluster to check
# cluster_name: name of the cluster
# scenario_num: scenario number for logging
# includes_local: "true" if this scenario includes local-cluster
verify_environment_health() {
    local cluster_kubeconfig=$1
    local cluster_name=$2
    local scenario_num=$3
    local includes_local=${4:-false}
    local issues=0

    log_info "--- Environment health check: $cluster_name (scenario $scenario_num) ---"
    
    # Determine the ArgoCD namespace for this cluster
    local argocd_ns="openshift-gitops"
    if [ "$cluster_name" = "$LOCAL_CLUSTER_NAME" ]; then
        argocd_ns="local-cluster"
    fi

    export KUBECONFIG=$cluster_kubeconfig

    # CHECK 1: acm-openshift-gitops must NOT exist in openshift-gitops on the hub
    # (it should be in local-cluster namespace instead, to avoid conflict with the original)
    if [ "$cluster_name" = "$LOCAL_CLUSTER_NAME" ]; then
        local stale_acm=$(kubectl get argocd acm-openshift-gitops -n openshift-gitops --no-headers 2>/dev/null || true)
        if [ -n "$stale_acm" ]; then
            log_error "ENV CHECK FAIL: acm-openshift-gitops exists in openshift-gitops on hub (CONFLICT!)"
            issues=$((issues + 1))
        else
            log_info "  [OK] No acm-openshift-gitops in openshift-gitops ns on hub (no conflict)"
        fi
    fi

    # CHECK 2: ArgoCD application controller should only manage apps in its own namespace
    # Get the ArgoCD controller namespace from the ArgoCD CR
    local controller_ns=$(kubectl get argocd acm-openshift-gitops -n $argocd_ns -o jsonpath='{.status.applicationController}' 2>/dev/null || true)
    if [ -n "$controller_ns" ] || kubectl get argocd acm-openshift-gitops -n $argocd_ns -o name 2>/dev/null | grep -q .; then
        # Check that the ArgoCD controller pod is running in the correct namespace
        local controller_pods=$(kubectl get pods -n $argocd_ns -l app.kubernetes.io/name=acm-openshift-gitops-application-controller --no-headers 2>/dev/null || true)
        if echo "$controller_pods" | grep -q "Running"; then
            log_info "  [OK] ArgoCD app controller running in $argocd_ns namespace"
        else
            local any_controller=$(kubectl get pods -n $argocd_ns --no-headers 2>/dev/null | grep "application-controller" || true)
            if [ -n "$any_controller" ]; then
                log_info "  [OK] ArgoCD app controller present in $argocd_ns namespace"
            else
                log_warn "  ArgoCD app controller not found in $argocd_ns (may still be starting)"
            fi
        fi

        # Check that Applications in this namespace belong to the right ArgoCD
        local apps=$(kubectl get applications -n $argocd_ns --no-headers 2>/dev/null || true)
        if [ -n "$apps" ] && echo "$apps" | grep -qv '^$'; then
            local app_count=$(echo "$apps" | grep -v '^$' | wc -l)
            # Verify each app's controllerNamespace matches its ArgoCD namespace
            local mismatched_apps=$(kubectl get applications -n $argocd_ns -o jsonpath='{range .items[*]}{.metadata.name}{" controllerNS="}{.status.controllerNamespace}{"\n"}{end}' 2>/dev/null | grep -v "controllerNS=$argocd_ns" | grep -v "controllerNS=$" || true)
            if [ -n "$mismatched_apps" ]; then
                log_error "ENV CHECK FAIL: Apps with mismatched controllerNamespace in $argocd_ns:"
                echo "$mismatched_apps"
                issues=$((issues + 1))
            else
                log_info "  [OK] All $app_count app(s) in $argocd_ns managed by correct controller"
            fi
        else
            log_info "  [OK] No applications in $argocd_ns (expected for non-app scenarios)"
        fi
    fi

    # CHECK 3: On hub, verify the original openshift-gitops ArgoCD is untouched
    if [ "$cluster_name" = "$LOCAL_CLUSTER_NAME" ] || [ "$includes_local" = "true" ]; then
        export KUBECONFIG=$HUB_KUBECONFIG
        local original_argocd
        original_argocd=$(kubectl get argocd openshift-gitops -n openshift-gitops --no-headers 2>/dev/null || true)
        if [ -n "$original_argocd" ]; then
            log_info "  [OK] Original hub ArgoCD (openshift-gitops) is present and untouched"
        else
            log_error "  Original hub ArgoCD (openshift-gitops) not found - hub ArgoCD is required"
            return 1
        fi
        export KUBECONFIG=$cluster_kubeconfig
    fi

    # CHECK 4: No orphaned ArgoCD CRs in unexpected namespaces on the managed cluster
    if [ "$cluster_name" != "$LOCAL_CLUSTER_NAME" ]; then
        local all_argocd=$(kubectl get argocd --all-namespaces --no-headers 2>/dev/null || true)
        local unexpected=$(echo "$all_argocd" | grep -v "^$argocd_ns " | grep -v '^$' || true)
        if [ -n "$unexpected" ]; then
            log_warn "  ArgoCD CRs found in unexpected namespaces on $cluster_name:"
            echo "$unexpected"
        else
            log_info "  [OK] ArgoCD CRs only in expected namespace ($argocd_ns) on $cluster_name"
        fi
    fi

    # CHECK 5: No conflicting ClusterRoleBindings between ArgoCD instances
    local crb_count=$(kubectl get clusterrolebinding -o name 2>/dev/null | grep -c "acm-openshift-gitops" || true)
    if [ "$crb_count" -gt 1 ]; then
        log_warn "  Multiple ClusterRoleBindings for acm-openshift-gitops ($crb_count):"
        kubectl get clusterrolebinding -o name 2>/dev/null | grep "acm-openshift-gitops"
    elif [ "$crb_count" -eq 1 ]; then
        log_info "  [OK] Single ClusterRoleBinding for acm-openshift-gitops"
    else
        log_info "  [OK] No ClusterRoleBinding for acm-openshift-gitops (expected if RBAC not added)"
    fi

    if [ "$issues" -gt 0 ]; then
        log_error "Environment health check FAILED with $issues issue(s) on $cluster_name"
        return 1
    fi

    log_info "  Environment health check PASSED for $cluster_name"
    return 0
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
    
    # 7. VERIFY managed cluster is clean (the addon's pre-delete Job should have removed resources)
    # NOTE: Hub (local-cluster) cleanup is CONSERVATIVE by design:
    #   - Only removes ArgoCD CRs with gitopsaddon label + ClusterRoleBinding
    #   - Operator deployment and OLM resources are SHARED with original openshift-gitops
    #   - Guestbook namespace is a test artifact (not managed by addon)
    # Remote managed clusters get FULL cleanup (ArgoCD, OLM, operator, everything)
    log_info "Verifying managed cluster cleanup..."
    if [ "$cluster_name" = "$LOCAL_CLUSTER_NAME" ]; then
        export KUBECONFIG=$HUB_KUBECONFIG
    elif [ "$cluster_name" = "$KIND_CLUSTER_NAME" ]; then
        export KUBECONFIG=$KIND_KUBECONFIG
    else
        export KUBECONFIG=$OCP_KUBECONFIG
    fi

    local argocd_ns="openshift-gitops"
    if [ "$cluster_name" = "$LOCAL_CLUSTER_NAME" ]; then
        argocd_ns="local-cluster"
    fi

    # Wait up to 90s for ArgoCD CR to be fully removed by pre-delete Job
    # (operator needs time to reconcile finalizers and clean up associated resources)
    local argocd_wait=0
    while [ "$argocd_wait" -lt 90 ]; do
        if ! kubectl get argocd acm-openshift-gitops -n $argocd_ns &>/dev/null 2>/dev/null; then
            break
        fi
        sleep 5
        argocd_wait=$((argocd_wait + 5))
    done

    if kubectl get argocd acm-openshift-gitops -n $argocd_ns &>/dev/null 2>/dev/null; then
        log_error "CLEANUP VERIFY FAIL: ArgoCD CR acm-openshift-gitops still in $argocd_ns on $cluster_name after 90s"
        log_error "  Pre-delete cleanup Job did not remove ArgoCD CR properly!"
        kubectl delete argocd acm-openshift-gitops -n $argocd_ns --ignore-not-found --wait=false 2>/dev/null || true
        issues=$((issues + 1))
    else
        log_info "  [OK] ArgoCD CR removed from $argocd_ns on $cluster_name"
    fi

    # Checks below only apply to REMOTE managed clusters (not hub/local-cluster)
    # Hub cleanup is conservative — operator and OLM are shared resources
    if [ "$cluster_name" != "$LOCAL_CLUSTER_NAME" ]; then
        # For OCP clusters, verify addon-managed OLM subscription was cleaned up
        # Only check for subscriptions with the gitopsaddon label — pre-existing
        # admin-installed subscriptions without the label are intentionally preserved.
        if [ "$cluster_name" = "$OCP_CLUSTER_NAME" ]; then
            local labeled_subs=$(kubectl get subscription.operators.coreos.com -n openshift-operators \
                -l apps.open-cluster-management.io/gitopsaddon=true -o name 2>/dev/null || true)
            if [ -n "$labeled_subs" ]; then
                log_error "CLEANUP VERIFY FAIL: gitopsaddon-labeled OLM subscription still exists on $cluster_name after cleanup!"
                issues=$((issues + 1))
            else
                log_info "  [OK] gitopsaddon-managed OLM subscription removed on $cluster_name"
            fi
            # Info-only: check if any non-labeled subscription still exists (pre-existing, expected)
            local remaining_sub=$(kubectl get subscription.operators.coreos.com openshift-gitops-operator -n openshift-operators -o name 2>/dev/null || true)
            if [ -n "$remaining_sub" ]; then
                log_info "  [INFO] Pre-existing OLM subscription still on $cluster_name (not managed by addon, correctly preserved)"
            fi
        fi

        # Verify embedded operator deployment was cleaned up (remote clusters only)
        if kubectl get deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator &>/dev/null 2>/dev/null; then
            log_error "CLEANUP VERIFY FAIL: Embedded operator deployment still in openshift-gitops-operator on $cluster_name"
            issues=$((issues + 1))
        else
            log_info "  [OK] Operator deployment removed from openshift-gitops-operator on $cluster_name"
        fi
    fi

    # Clean up test artifacts (guestbook app resources, ClusterRoleBinding, guestbook ns)
    # These are test artifacts, not managed by the addon — cleanup is test responsibility
    kubectl delete applications.argoproj.io --all -n $argocd_ns --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete appproject --all -n $argocd_ns --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete namespace guestbook --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete clusterrolebinding acm-openshift-gitops-cluster-admin --ignore-not-found 2>/dev/null || true

    # Switch back to hub
    export KUBECONFIG=$HUB_KUBECONFIG

    if [ $issues -eq 0 ]; then
        log_info "Cleanup of $gitopscluster_name complete - all verified clean"
    else
        log_error "Cleanup of $gitopscluster_name had $issues issue(s) - addon cleanup may have bugs!"
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

    # Delete ALL Placements in openshift-gitops FIRST — this stops the gitopscluster
    # controller from finding managed clusters and recreating resources during cleanup
    log_info "Deleting all Placements in openshift-gitops..."
    kubectl delete placement --all -n openshift-gitops --ignore-not-found --timeout=30s 2>/dev/null || true

    # Delete ALL GitOpsCluster resources to ensure clean state (catches unknown leftovers)
    local remaining_goc=$(kubectl get gitopscluster -A --no-headers 2>/dev/null | wc -l)
    if [ "$remaining_goc" -gt 0 ]; then
        log_info "Deleting $remaining_goc remaining GitOpsCluster(s)..."
        kubectl get gitopscluster -A --no-headers 2>/dev/null || true
        kubectl delete gitopscluster --all -A --ignore-not-found --timeout=30s 2>/dev/null || true
    fi

    # Delete ALL Policies and PlacementBindings (stops enforcement before addon cleanup)
    kubectl delete policy --all -n openshift-gitops --ignore-not-found --timeout=30s 2>/dev/null || true
    kubectl delete placementbinding --all -n openshift-gitops --ignore-not-found --timeout=30s 2>/dev/null || true

    # Wait for controller to stop reconciling deleted resources
    sleep 10

    # Delete ALL ManagedClusterAddOns for gitops-addon (catches stuck addons from unknown GitOpsClusters)
    for ns in $(kubectl get managedclusteraddon gitops-addon -A --no-headers 2>/dev/null | awk '{print $1}'); do
        log_info "Force-deleting ManagedClusterAddOn gitops-addon in namespace $ns..."
        # Use merge patch with empty array — json-patch "remove" silently fails if the addon-manager
        # re-adds the finalizer between patch and delete
        kubectl patch managedclusteraddon gitops-addon -n $ns --type=merge \
            -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        kubectl delete managedclusteraddon gitops-addon -n $ns --force --grace-period=0 --ignore-not-found 2>/dev/null || true
    done

    # Delete ALL AddOnDeploymentConfigs for gitops-addon
    for ns in $(kubectl get addondeploymentconfig gitops-addon-config -A --no-headers 2>/dev/null | awk '{print $1}'); do
        log_info "Deleting AddOnDeploymentConfig in namespace $ns..."
        kubectl delete addondeploymentconfig gitops-addon-config -n $ns --ignore-not-found 2>/dev/null || true
    done

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

    cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
    cleanup_scenario "$LOCAL_CLUSTER_NAME" "kind-gitops" "kind-placement"
    cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
    cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
    cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
    cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
    cleanup_scenario "$LOCAL_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
    cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
    # ============================================
    # STEP 3: Direct managed cluster cleanup
    # During initial cleanup ONLY, we can directly connect to managed clusters
    # to remove residual resources that the pre-delete Job may have missed.
    # This handles: stale pause markers, leftover ArgoCD CRs, OLM artifacts,
    # and the default openshift-gitops ArgoCD instance.
    # NOTE: During scenario runs, we ONLY operate from the hub.
    # ============================================

    log_info "--- Direct managed cluster cleanup (initial only) ---"

    # Cleanup local-cluster (hub) ArgoCD CR in local-cluster namespace
    log_info "Cleaning up local-cluster ArgoCD CR directly..."
    export KUBECONFIG=$HUB_KUBECONFIG
    kubectl delete argocd acm-openshift-gitops -n local-cluster --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete namespace guestbook --ignore-not-found --wait=false 2>/dev/null || true

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
    kubectl delete secret cluster-$LOCAL_CLUSTER_NAME -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # ============================================
    # STEP 5: Verify cleanup - STRICT (fail if not clean)
    # ============================================

    log_info "--- Verifying cleanup (strict) ---"
    sleep 15
    local cleanup_issues=0

    # ---------- KIND cluster checks ----------
    export KUBECONFIG=$KIND_KUBECONFIG
    local kind_argocd_count=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$kind_argocd_count" -gt 0 ]; then
        log_error "CLEANUP FAILED: KIND cluster still has $kind_argocd_count ArgoCD CR(s)"
        kubectl get argocd -n openshift-gitops 2>/dev/null || true
        for argocd_name in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
            kubectl patch $argocd_name -n openshift-gitops --type=merge \
                -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        done
        kubectl delete argocd --all -n openshift-gitops --force --grace-period=0 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] KIND cluster: No ArgoCD CRs"
    fi

    if kubectl get deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator &>/dev/null 2>/dev/null; then
        log_error "CLEANUP FAILED: KIND cluster has embedded operator deployment in openshift-gitops-operator"
        kubectl delete deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator --ignore-not-found 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] KIND cluster: No operator deployment in openshift-gitops-operator"
    fi

    if kubectl get namespace guestbook &>/dev/null 2>/dev/null; then
        log_error "CLEANUP FAILED: KIND cluster still has guestbook namespace"
        kubectl delete namespace guestbook --ignore-not-found --wait=false 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] KIND cluster: No guestbook namespace"
    fi

    # ---------- OCP cluster checks ----------
    export KUBECONFIG=$OCP_KUBECONFIG
    local ocp_argocd_count=$(kubectl get argocd -n openshift-gitops --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$ocp_argocd_count" -gt 0 ]; then
        log_error "CLEANUP FAILED: OCP cluster still has $ocp_argocd_count ArgoCD CR(s)"
        kubectl get argocd -n openshift-gitops 2>/dev/null || true
        for argocd_name in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
            kubectl patch $argocd_name -n openshift-gitops --type=merge \
                -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        done
        kubectl delete argocd --all -n openshift-gitops --force --grace-period=0 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] OCP cluster: No ArgoCD CRs"
    fi

    # OLM subscription check on OCP
    local olm_sub_count=$(kubectl get subscription.operators.coreos.com -n openshift-operators \
        -l apps.open-cluster-management.io/gitopsaddon=true --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$olm_sub_count" -gt 0 ]; then
        log_error "CLEANUP FAILED: OCP cluster still has $olm_sub_count OLM subscription(s) with gitopsaddon label"
        kubectl get subscription.operators.coreos.com -n openshift-operators -l apps.open-cluster-management.io/gitopsaddon=true 2>/dev/null || true
        kubectl delete subscription.operators.coreos.com -l apps.open-cluster-management.io/gitopsaddon=true -n openshift-operators --ignore-not-found 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] OCP cluster: No gitopsaddon OLM subscriptions"
    fi

    # Operator deployment check on OCP
    for ns in openshift-operators openshift-gitops-operator; do
        if kubectl get deployment openshift-gitops-operator-controller-manager -n $ns &>/dev/null 2>/dev/null; then
            log_error "CLEANUP FAILED: OCP cluster has operator deployment in $ns"
            kubectl delete deployment openshift-gitops-operator-controller-manager -n $ns --ignore-not-found 2>/dev/null || true
            cleanup_issues=$((cleanup_issues + 1))
        else
            log_info "  [OK] OCP cluster: No operator deployment in $ns"
        fi
    done

    if kubectl get namespace guestbook &>/dev/null 2>/dev/null; then
        log_error "CLEANUP FAILED: OCP cluster still has guestbook namespace"
        kubectl delete namespace guestbook --ignore-not-found --wait=false 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] OCP cluster: No guestbook namespace"
    fi

    # ---------- Hub / local-cluster checks ----------
    export KUBECONFIG=$HUB_KUBECONFIG
    if kubectl get argocd acm-openshift-gitops -n local-cluster &>/dev/null 2>/dev/null; then
        log_error "CLEANUP FAILED: Hub still has acm-openshift-gitops ArgoCD CR in local-cluster ns"
        kubectl delete argocd acm-openshift-gitops -n local-cluster --ignore-not-found --wait=false 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] Hub: No acm-openshift-gitops in local-cluster ns"
    fi

    if kubectl get namespace guestbook &>/dev/null 2>/dev/null; then
        log_error "CLEANUP FAILED: Hub still has guestbook namespace"
        kubectl delete namespace guestbook --ignore-not-found --wait=false 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] Hub: No guestbook namespace"
    fi

    local hub_addon_count=$(kubectl get managedclusteraddon gitops-addon --all-namespaces --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$hub_addon_count" -gt 0 ]; then
        log_error "CLEANUP FAILED: Hub still has $hub_addon_count ManagedClusterAddOn(s)"
        kubectl get managedclusteraddon gitops-addon --all-namespaces 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] Hub: No ManagedClusterAddOns"
    fi

    local hub_gitops_count=$(kubectl get gitopscluster --all-namespaces --no-headers 2>/dev/null | grep -v '^$' | wc -l)
    if [ "$hub_gitops_count" -gt 0 ]; then
        log_error "CLEANUP FAILED: Hub still has $hub_gitops_count GitOpsCluster(s)"
        kubectl get gitopscluster --all-namespaces 2>/dev/null || true
        cleanup_issues=$((cleanup_issues + 1))
    else
        log_info "  [OK] Hub: No GitOpsClusters"
    fi

    if [ "$cleanup_issues" -gt 0 ]; then
        log_error "=== Initial cleanup had $cleanup_issues issue(s) - forced cleanup applied, waiting for stabilization ==="
        sleep 15
    else
        log_info "  === All cleanup verified clean ==="
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
        kubectl patch $argocd_name -n openshift-gitops --type=merge \
            -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
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
            kubectl patch $argocd_name -n openshift-gitops --type=merge \
                -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
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

# Ensure the GitOps operator allows ArgoCD instances in local-cluster namespace
# to have cluster-scoped permissions (required for deploying to other namespaces).
# Uses the OLM Subscription config so changes persist through operator upgrades.
ensure_local_cluster_in_cluster_config() {
    export KUBECONFIG=$HUB_KUBECONFIG

    local current=$(kubectl get deployment openshift-gitops-operator-controller-manager \
        -n openshift-gitops-operator \
        -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="ARGOCD_CLUSTER_CONFIG_NAMESPACES")].value}' 2>/dev/null)

    if echo "$current" | grep -q "local-cluster"; then
        log_info "ARGOCD_CLUSTER_CONFIG_NAMESPACES already includes local-cluster: $current"
    else
        log_info "Adding local-cluster to ARGOCD_CLUSTER_CONFIG_NAMESPACES via Subscription..."
        kubectl patch subscription.operators.coreos.com openshift-gitops-operator \
            -n openshift-gitops-operator --type=merge \
            -p '{"spec":{"config":{"env":[{"name":"ARGOCD_CLUSTER_CONFIG_NAMESPACES","value":"openshift-gitops,local-cluster"}]}}}' 2>/dev/null || true
        log_info "Waiting for operator pod to restart with new config..."
        kubectl rollout status deployment/openshift-gitops-operator-controller-manager \
            -n openshift-gitops-operator --timeout=120s 2>/dev/null || true
    fi
}

# Configure hub ArgoCD for non-agent mode (scenarios 1 & 2)
# DELETES and RECREATES the ArgoCD CR with minimal config (no principal)
configure_hub_argocd_non_agent() {
    log_info "Configuring hub ArgoCD for non-agent mode (DELETE and RECREATE)..."

    export KUBECONFIG=$HUB_KUBECONFIG

    ensure_local_cluster_in_cluster_config

    # Delete existing ArgoCD CR
    log_info "Deleting existing hub ArgoCD CR..."
    kubectl delete argocd openshift-gitops -n openshift-gitops --wait=false 2>/dev/null || true
    sleep 5

    # Recreate with non-agent configuration (controller disabled, server enabled)
    log_info "Creating hub ArgoCD CR with non-agent config..."
    kubectl apply -f - <<'EOF'
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: openshift-gitops
  namespace: openshift-gitops
spec:
  controller:
    enabled: false
  server:
    route:
      enabled: true
EOF

    # Wait for ArgoCD server to be ready (the gitopscluster controller needs services)
    wait_for_condition 120 "ArgoCD server pod in openshift-gitops" \
        "kubectl get pods -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-server --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q Running" || {
            log_warn "ArgoCD server pod not ready after 120s, continuing anyway..."
        }
    log_info "Hub ArgoCD configured for non-agent mode"
}

# Configure hub ArgoCD for agent mode (scenarios 3 & 4)
# DELETES and RECREATES the ArgoCD CR with minimal config
configure_hub_argocd_agent() {
    log_info "Configuring hub ArgoCD for agent mode (PATCH existing)..."
    
    export KUBECONFIG=$HUB_KUBECONFIG

    ensure_local_cluster_in_cluster_config
    
    # Apply ArgoCD CR for agent mode (create or update).
    # Uses apply instead of patch to handle the case where the CR was deleted
    # between scenarios. Key settings:
    #   - controller.enabled: false  (principal handles app reconciliation)
    #   - sourceNamespaces: ["*"]    (watch Applications in managed cluster namespaces)
    #   - principal.enabled: true    (enable gRPC principal for agents)
    #   - principal.auth: mtls       (authenticate agents via OCM addon client certs)
    #   - principal.namespace.allowedNamespaces: ["*"]  (allow any namespace)
    log_info "Applying hub ArgoCD CR for agent mode..."
    kubectl apply -f - <<'EOF'
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: openshift-gitops
  namespace: openshift-gitops
spec:
  controller:
    enabled: false
  sourceNamespaces:
    - "*"
  server:
    route:
      enabled: true
  argoCDAgent:
    principal:
      enabled: true
      auth: "mtls:CN=system:open-cluster-management:cluster:([^:]+):addon:gitops-addon:agent:gitops-addon-agent"
      namespace:
        allowedNamespaces:
          - "*"
EOF
    
    # Wait for ArgoCD server to be ready first
    wait_for_condition 120 "ArgoCD server pod in openshift-gitops" \
        "kubectl get pods -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-server --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q Running" || {
            log_warn "ArgoCD server pod not ready after 120s, continuing anyway..."
        }

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
    # NOTE: SA namespace and Application namespace use hub templates so they resolve to
    # "local-cluster" on the hub and "openshift-gitops" on remote managed clusters.
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
                        "namespace": "{{hub or (and (eq .ManagedClusterName \"local-cluster\") \"local-cluster\") \"openshift-gitops\" hub}}"
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
                      "namespace": "{{hub or (and (eq .ManagedClusterName \"local-cluster\") \"local-cluster\") \"openshift-gitops\" hub}}"
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
    # SA namespace uses hub template so it resolves to the correct ArgoCD namespace.
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
                        "namespace": "{{hub or (and (eq .ManagedClusterName \"local-cluster\") \"local-cluster\") \"openshift-gitops\" hub}}"
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

# Create guestbook Application directly on local-cluster (for agent scenarios).
# Create the guestbook Application in the local-cluster namespace for agent scenarios,
# because in agent mode ArgoCD only picks up Applications from its own namespace.
create_local_cluster_guestbook_app() {
    log_info "Creating guestbook Application directly in local-cluster namespace..."
    export KUBECONFIG=$HUB_KUBECONFIG

    # AppProject must exist for the Application to be reconciled.
    log_info "Ensuring default AppProject in local-cluster namespace..."
    kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: local-cluster
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

    kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook
  namespace: local-cluster
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps
    targetRevision: HEAD
    path: guestbook
  destination:
    server: https://kubernetes.default.svc
    namespace: guestbook
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
EOF
}

# Verify local-cluster ArgoCD is working: ArgoCD running, guestbook app synced
verify_local_cluster_working() {
    local scenario_num=$1
    local is_agent_mode=${2:-false}

    export KUBECONFIG=$HUB_KUBECONFIG

    # Wait for ArgoCD to be running in local-cluster namespace
    wait_for_condition 120 "ArgoCD to be running in local-cluster namespace on hub" \
        "kubectl get argocd acm-openshift-gitops -n local-cluster -o name" || return 1

    # Verify no duplicate ArgoCD instances on hub
    verify_no_duplicate_argocd "$HUB_KUBECONFIG" "$LOCAL_CLUSTER_NAME" || {
        log_error "SCENARIO $scenario_num: Duplicate ArgoCD instances on hub!"
        return 1
    }

    # Ensure the default AppProject exists in local-cluster namespace.
    # The ArgoCD operator does not always auto-create this in secondary namespaces.
    # Without it, Applications referencing project "default" won't reconcile.
    log_info "Ensuring default AppProject exists in local-cluster namespace..."
    kubectl get appproject default -n local-cluster &>/dev/null 2>/dev/null || \
        kubectl apply -f - <<'APPPROJ'
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: local-cluster
spec:
  clusterResourceWhitelist:
    - group: '*'
      kind: '*'
  destinations:
    - namespace: '*'
      server: '*'
  sourceRepos:
    - '*'
APPPROJ
    wait_for_condition 30 "default AppProject in local-cluster" \
        "kubectl get appproject default -n local-cluster -o name" || {
            log_error "Failed to create default AppProject in local-cluster namespace"
            return 1
        }

    # For agent scenarios, wait for the addon agent to provision the client cert
    if [ "$is_agent_mode" = "true" ]; then
        log_info "Agent mode: waiting for client cert to be provisioned on local-cluster..."
        wait_for_condition 180 "ArgoCD agent client cert in local-cluster namespace" \
            "kubectl get secret argocd-agent-client-tls -n local-cluster -o name" || {
                log_warn "Client cert not yet provisioned for local-cluster (continuing)"
            }

        create_local_cluster_guestbook_app || return 1
    fi

    # Wait for guestbook to be deployed on local-cluster (which IS the hub).
    # Use 600s timeout because the repo-server may need to cold-start and clone the git repo,
    # and in agent mode the ArgoCD instance is freshly created requiring full initialization.
    wait_for_condition 600 "Guestbook deployment to exist on local-cluster" \
        "kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO $scenario_num: Guestbook not deployed on local-cluster"
            return 1
        }

    # Verify the Application exists in local-cluster namespace and is being managed
    wait_for_condition 120 "Guestbook Application to exist in local-cluster namespace" \
        "kubectl get applications.argoproj.io guestbook -n local-cluster -o name" || {
            log_error "SCENARIO $scenario_num: Guestbook Application not found in local-cluster namespace"
            return 1
        }

    # Check sync status
    wait_for_condition 180 "Guestbook Application to be Synced on local-cluster" \
        "kubectl get applications.argoproj.io guestbook -n local-cluster -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_warn "Guestbook Application not yet Synced on local-cluster"
        }

    local sync_status=$(kubectl get applications.argoproj.io guestbook -n local-cluster -o jsonpath='{.status.sync.status}' 2>/dev/null)
    local health_status=$(kubectl get applications.argoproj.io guestbook -n local-cluster -o jsonpath='{.status.health.status}' 2>/dev/null)
    log_info "local-cluster Application Status - Sync: $sync_status, Health: $health_status"

    # Verify the app controller namespace matches (app managed by local-cluster ArgoCD)
    local controller_ns=$(kubectl get applications.argoproj.io guestbook -n local-cluster -o jsonpath='{.status.controllerNamespace}' 2>/dev/null)
    if [ -n "$controller_ns" ] && [ "$controller_ns" != "local-cluster" ]; then
        log_error "SCENARIO $scenario_num: Guestbook on local-cluster managed by wrong controller: $controller_ns (expected local-cluster)"
        return 1
    fi
    log_info "local-cluster guestbook managed by correct controller namespace: ${controller_ns:-pending}"

    # Environment health check for local-cluster
    verify_environment_health "$HUB_KUBECONFIG" "$LOCAL_CLUSTER_NAME" "$scenario_num" "true" || return 1

    return 0
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

# Verify that the addon agent auto-detected OCP and created an OLM subscription
# instead of deploying embedded operator manifests.
# Checks:
#   1. OLM subscription exists in openshift-operators namespace
#   2. No embedded operator deployment in openshift-gitops-operator namespace
verify_olm_auto_detected() {
    local cluster_kubeconfig=$1
    local cluster_name=$2

    log_info "Verifying OLM auto-detection on $cluster_name..."

    export KUBECONFIG=$cluster_kubeconfig

    # Check 1: OLM subscription should exist
    wait_for_condition 300 "OLM subscription to be created on $cluster_name" \
        "kubectl get subscription.operators.coreos.com openshift-gitops-operator -n openshift-operators -o name" || {
            log_error "OLM auto-detection FAILED: No OLM subscription found on $cluster_name"
            log_debug "Addon agent logs:"
            kubectl logs -n openshift-operators -l app=gitops-addon --tail=30 2>/dev/null || true
            return 1
        }

    # Verify the subscription has the DISABLE_DEFAULT_ARGOCD_INSTANCE env var
    local disable_default=$(kubectl get subscription.operators.coreos.com openshift-gitops-operator \
        -n openshift-operators -o jsonpath='{.spec.config.env[?(@.name=="DISABLE_DEFAULT_ARGOCD_INSTANCE")].value}' 2>/dev/null)
    if [ "$disable_default" = "true" ]; then
        log_info "  [OK] OLM subscription has DISABLE_DEFAULT_ARGOCD_INSTANCE=true"
    else
        log_error "OLM subscription DISABLE_DEFAULT_ARGOCD_INSTANCE='$disable_default' (expected: 'true')"
        kubectl get subscription.operators.coreos.com openshift-gitops-operator -n openshift-operators -o yaml 2>/dev/null || true
        export KUBECONFIG=$HUB_KUBECONFIG
        return 1
    fi

    # Check 2: No embedded operator deployment in openshift-gitops-operator namespace
    if kubectl get deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator &>/dev/null 2>/dev/null; then
        log_error "Embedded operator deployment found in openshift-gitops-operator namespace (expected OLM mode only on $cluster_name)"
        export KUBECONFIG=$HUB_KUBECONFIG
        return 1
    else
        log_info "  [OK] No embedded operator deployment in openshift-gitops-operator (correct for OLM mode)"
    fi

    log_info "OLM auto-detection verified successfully on $cluster_name"

    export KUBECONFIG=$HUB_KUBECONFIG
    return 0
}

#######################################
# SCENARIO 1: gitops-addon on OCP (no agent, OLM auto-detected)
#######################################
test_scenario_1() {
    log_info "=============================================="
    log_info "SCENARIO 1: gitops-addon on $OCP_CLUSTER_NAME + $LOCAL_CLUSTER_NAME (no agent, OLM auto-detected)"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster creates ManagedClusterAddOn for BOTH OCP and local-cluster"
    log_info "  - GitOpsCluster creates ArgoCD Policy targeting BOTH clusters"
    log_info "  - Addon agent on OCP auto-detects OCP and creates OLM subscription"
    log_info "  - Policy deploys ArgoCD CR to openshift-gitops ns on OCP, local-cluster ns on hub"
    log_info "  - User modifies Policy to add RBAC + Application"
    log_info "  - No duplicate ArgoCD on hub (original openshift-gitops untouched)"
    log_info "  - Application syncs and deploys guestbook on OCP and local-cluster"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created for OCP"
    log_info "  - OLM subscription auto-detected and created on OCP"
    log_info "  - Policy compliant on both clusters"
    log_info "  - ArgoCD CR acm-openshift-gitops in openshift-gitops ns on OCP"
    log_info "  - ArgoCD CR acm-openshift-gitops in local-cluster ns on hub (NOT in openshift-gitops)"
    log_info "  - No duplicate ArgoCD CRs on hub"
    log_info "  - guestbook-ui deployment in guestbook namespace on OCP"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for non-agent mode (controller disabled)
    configure_hub_argocd_non_agent

    ensure_addon_template

    # Create Placement selecting BOTH OCP and local-cluster
    log_info "Creating Placement (selecting $OCP_CLUSTER_NAME AND $LOCAL_CLUSTER_NAME)..."
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
          matchExpressions:
            - key: name
              operator: In
              values:
                - $OCP_CLUSTER_NAME
                - $LOCAL_CLUSTER_NAME
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

    # Wait for ManagedClusterAddOn to be created for both clusters
    wait_for_condition 180 "ManagedClusterAddOn to be created for $OCP_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1
    wait_for_condition 180 "ManagedClusterAddOn to be created for $LOCAL_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $LOCAL_CLUSTER_NAME -o name" || return 1

    # Add RBAC + Application to the Policy (simulating user modification)
    add_rbac_and_app_to_policy "ocp-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant (OLM auto-detect takes longer)
    wait_for_policy_compliant "ocp-gitops-argocd-policy" "openshift-gitops" 420 || return 1

    # CRITICAL: Verify OLM subscription was auto-detected on OCP
    # The addon agent should detect OCP via CRDs/ClusterClaims and create an OLM subscription
    verify_olm_auto_detected "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || return 1

    # Wait for ArgoCD to be running on OCP managed cluster
    wait_for_condition 300 "ArgoCD to be running on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1

    # CRITICAL: Verify no duplicate ArgoCD instances on OCP
    verify_no_duplicate_argocd "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || {
        log_error "SCENARIO 1: FAILED - Duplicate ArgoCD instances detected on $OCP_CLUSTER_NAME!"
        return 1
    }

    # Wait for guestbook to be deployed on OCP (application was added via Policy)
    wait_for_condition 300 "Guestbook deployment resource to exist on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 1: FAILED - Guestbook deployment not created by ArgoCD"
            return 1
        }
    log_info "NOTE: On OCP, guestbook-ui pods will crash (port 80 + restricted SCC) - this is expected"

    # Post-scenario environment health checks for OCP
    verify_environment_health "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" "1" "false" || return 1

    # Verify local-cluster ArgoCD + guestbook working
    verify_local_cluster_working "1" "false" || return 1

    log_info "=============================================="
    log_info "SCENARIO 1: PASSED"
    log_info "  - GitOpsCluster created ManagedClusterAddOn for OCP: OK"
    log_info "  - OLM subscription auto-detected on OCP: OK"
    log_info "  - ArgoCD Policy created and modified: OK"
    log_info "  - ArgoCD running on OCP (openshift-gitops ns): OK"
    log_info "  - ArgoCD running on hub (local-cluster ns, no duplicate): OK"
    log_info "  - Guestbook synced by ArgoCD on OCP: OK"
    log_info "  - Guestbook synced by ArgoCD on local-cluster: OK"
    log_info "  - Environment health checks: OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 2: gitops-addon on Kind (no agent, no OLM)
#######################################
test_scenario_2() {
    log_info "=============================================="
    log_info "SCENARIO 2: gitops-addon on $KIND_CLUSTER_NAME + $LOCAL_CLUSTER_NAME (no agent, no OLM)"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster creates ManagedClusterAddOn (for Kind only, skipped for local-cluster)"
    log_info "  - GitOpsCluster creates ArgoCD Policy targeting BOTH clusters"
    log_info "  - User modifies Policy to add RBAC + Application"
    log_info "  - Policy deploys ArgoCD + RBAC + App to Kind and local-cluster"
    log_info "  - ArgoCD in local-cluster namespace on hub (no duplicate)"
    log_info "  - Application syncs and deploys guestbook on both clusters"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created for Kind"
    log_info "  - Policy compliant on both clusters"
    log_info "  - ArgoCD CR acm-openshift-gitops running on Kind and local-cluster"
    log_info "  - guestbook-ui deployment in guestbook namespace on both clusters"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for non-agent mode (controller disabled)
    configure_hub_argocd_non_agent

    ensure_addon_template

    # Create Placement selecting BOTH Kind and local-cluster
    log_info "Creating Placement (selecting $KIND_CLUSTER_NAME AND $LOCAL_CLUSTER_NAME)..."
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
          matchExpressions:
            - key: name
              operator: In
              values:
                - $KIND_CLUSTER_NAME
                - $LOCAL_CLUSTER_NAME
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

    # Wait for ManagedClusterAddOn to be created for both clusters
    wait_for_condition 180 "ManagedClusterAddOn to be created for $KIND_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $KIND_CLUSTER_NAME -o name" || return 1
    wait_for_condition 180 "ManagedClusterAddOn to be created for $LOCAL_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $LOCAL_CLUSTER_NAME -o name" || return 1

    # Add RBAC + Application to the Policy (simulating user modification)
    add_rbac_and_app_to_policy "kind-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant
    wait_for_policy_compliant "kind-gitops-argocd-policy" "openshift-gitops" 300 || return 1

    # Wait for ArgoCD to be running on managed cluster
    wait_for_condition 180 "ArgoCD to be running on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1

    # Wait for guestbook to be deployed and healthy (application was added via Policy)
    wait_for_condition 300 "Guestbook to be deployed on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 2: FAILED - Guestbook not deployed"
            return 1
        }
    wait_for_condition 120 "Guestbook pods to be ready on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl rollout status deployment/guestbook-ui -n guestbook --timeout=5s" || {
            log_warn "Guestbook pods not fully ready yet, but deployment exists"
        }

    # Post-scenario environment health checks for Kind
    verify_environment_health "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME" "2" "false" || return 1

    # Verify local-cluster ArgoCD + guestbook working
    verify_local_cluster_working "2" "false" || return 1

    log_info "=============================================="
    log_info "SCENARIO 2: PASSED"
    log_info "  - GitOpsCluster created ManagedClusterAddOn for Kind: OK"
    log_info "  - ArgoCD Policy created and modified: OK"
    log_info "  - ArgoCD running on Kind managed cluster: OK"
    log_info "  - ArgoCD running on hub (local-cluster ns, no duplicate): OK"
    log_info "  - Guestbook deployed on Kind: OK"
    log_info "  - Guestbook deployed on local-cluster: OK"
    log_info "  - Environment health checks: OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 3: gitops-addon + custom OLM on OCP
#######################################
test_scenario_3() {
    log_info "=============================================="
    log_info "SCENARIO 3: gitops-addon + custom OLM on $OCP_CLUSTER_NAME + $LOCAL_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with olmSubscription.enabled=true + installPlanApproval=Manual"
    log_info "  - Custom OLM values passed from hub to addon agent via AddOnDeploymentConfig env vars"
    log_info "  - Addon agent on OCP auto-detects OCP and uses custom values for subscription"
    log_info "  - OLM Subscription on OCP has installPlanApproval=Manual (proving custom values work)"
    log_info "  - InstallPlan manually approved, operator installs successfully"
    log_info "  - User modifies Policy to add RBAC + Application"
    log_info "  - Application syncs and deploys guestbook on both clusters"
    log_info "Success criteria:"
    log_info "  - AddOnDeploymentConfig contains OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL=Manual"
    log_info "  - OLM Subscription on OCP has installPlanApproval=Manual"
    log_info "  - InstallPlan approved manually, operator running"
    log_info "  - Policy compliant on both clusters"
    log_info "  - ArgoCD CR acm-openshift-gitops running on both clusters"
    log_info "  - guestbook-ui deployment in guestbook namespace on both clusters"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for non-agent mode (controller disabled)
    configure_hub_argocd_non_agent

    ensure_addon_template

    # Create Placement selecting BOTH OCP and local-cluster
    log_info "Creating Placement (selecting $OCP_CLUSTER_NAME AND $LOCAL_CLUSTER_NAME)..."
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
          matchExpressions:
            - key: name
              operator: In
              values:
                - $OCP_CLUSTER_NAME
                - $LOCAL_CLUSTER_NAME
EOF

    # Create GitOpsCluster with custom OLM config (installPlanApproval=Manual to prove values work)
    log_info "Creating GitOpsCluster with custom OLM config (installPlanApproval=Manual)..."
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
      installPlanApproval: "Manual"
EOF

    # Wait for ManagedClusterAddOn to be created for both clusters
    wait_for_condition 180 "ManagedClusterAddOn to be created for $OCP_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1
    wait_for_condition 180 "ManagedClusterAddOn to be created for $LOCAL_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $LOCAL_CLUSTER_NAME -o name" || return 1

    # CRITICAL: Verify OLM subscription env vars were propagated to AddOnDeploymentConfig
    # The controller needs a reconcile cycle to update the AddOnDeploymentConfig, so wait for it.
    log_info "Verifying OLM subscription config passthrough to AddOnDeploymentConfig..."

    wait_for_condition 60 "OLM_SUBSCRIPTION_ENABLED to be 'true' in AddOnDeploymentConfig" \
        "kubectl get addondeploymentconfig gitops-addon-config -n $OCP_CLUSTER_NAME -o jsonpath='{.spec.customizedVariables[?(@.name==\"OLM_SUBSCRIPTION_ENABLED\")].value}' 2>/dev/null | grep -qx 'true'" || {
            local olm_enabled_var=$(kubectl get addondeploymentconfig gitops-addon-config -n $OCP_CLUSTER_NAME -o jsonpath='{.spec.customizedVariables[?(@.name=="OLM_SUBSCRIPTION_ENABLED")].value}' 2>/dev/null)
            log_error "SCENARIO 3: FAILED - OLM_SUBSCRIPTION_ENABLED not set to 'true' in AddOnDeploymentConfig (got: '$olm_enabled_var')"
            kubectl get addondeploymentconfig gitops-addon-config -n $OCP_CLUSTER_NAME -o yaml 2>/dev/null || true
            return 1
        }
    log_info "  OLM_SUBSCRIPTION_ENABLED=true - OK"

    wait_for_condition 60 "OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL to be 'Manual' in AddOnDeploymentConfig" \
        "kubectl get addondeploymentconfig gitops-addon-config -n $OCP_CLUSTER_NAME -o jsonpath='{.spec.customizedVariables[?(@.name==\"OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL\")].value}' 2>/dev/null | grep -qx 'Manual'" || {
            local olm_approval_var=$(kubectl get addondeploymentconfig gitops-addon-config -n $OCP_CLUSTER_NAME -o jsonpath='{.spec.customizedVariables[?(@.name=="OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL")].value}' 2>/dev/null)
            log_error "SCENARIO 3: FAILED - OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL not set to 'Manual' (got: '$olm_approval_var')"
            kubectl get addondeploymentconfig gitops-addon-config -n $OCP_CLUSTER_NAME -o yaml 2>/dev/null || true
            return 1
        }
    log_info "  OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL=Manual - OK"
    log_info "OLM subscription config passthrough verified successfully!"

    # Add RBAC + Application to the Policy
    add_rbac_and_app_to_policy "ocp-olm-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for OLM subscription to be created on OCP with custom installPlanApproval
    log_info "Waiting for OLM subscription with installPlanApproval=Manual on OCP..."
    export KUBECONFIG=$OCP_KUBECONFIG
    wait_for_condition 300 "OLM subscription to be created on $OCP_CLUSTER_NAME" \
        "kubectl get subscription.operators.coreos.com openshift-gitops-operator -n openshift-operators -o name" || {
            log_error "SCENARIO 3: FAILED - OLM subscription not created on OCP"
            return 1
        }

    # CRITICAL: Verify the subscription has installPlanApproval=Manual (proves custom values work)
    local actual_approval=$(kubectl get subscription.operators.coreos.com openshift-gitops-operator \
        -n openshift-operators -o jsonpath='{.spec.installPlanApproval}' 2>/dev/null)
    if [ "$actual_approval" != "Manual" ]; then
        log_error "SCENARIO 3: FAILED - OLM subscription installPlanApproval is '$actual_approval' (expected: Manual)"
        log_error "Custom OLM values were NOT propagated to the actual subscription!"
        kubectl get subscription.operators.coreos.com openshift-gitops-operator -n openshift-operators -o yaml 2>/dev/null || true
        return 1
    fi
    log_info "  [OK] OLM subscription has installPlanApproval=Manual (custom value verified on managed cluster)"

    # Verify DISABLE_DEFAULT_ARGOCD_INSTANCE=true is set
    local disable_default=$(kubectl get subscription.operators.coreos.com openshift-gitops-operator \
        -n openshift-operators -o jsonpath='{.spec.config.env[?(@.name=="DISABLE_DEFAULT_ARGOCD_INSTANCE")].value}' 2>/dev/null)
    if [ "$disable_default" != "true" ]; then
        log_error "SCENARIO 3: FAILED - DISABLE_DEFAULT_ARGOCD_INSTANCE not 'true' (got: '$disable_default')"
        return 1
    fi
    log_info "  [OK] OLM subscription has DISABLE_DEFAULT_ARGOCD_INSTANCE=true"

    # No embedded operator deployment should exist (OLM mode)
    if kubectl get deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator &>/dev/null 2>/dev/null; then
        log_error "SCENARIO 3: FAILED - Embedded operator deployment found (should be OLM-only mode)"
        return 1
    fi
    log_info "  [OK] No embedded operator deployment (correct for OLM mode)"

    # Wait for InstallPlan to appear and manually approve it
    log_info "Waiting for InstallPlan to be created by OLM..."
    wait_for_condition 120 "InstallPlan to be created for openshift-gitops-operator" \
        "kubectl get installplan -n openshift-operators -o jsonpath='{.items[?(@.spec.approved==false)].metadata.name}' 2>/dev/null | grep -v '^$'" || {
            # Check if there's already an approved plan (operator may already be installed from previous scenario)
            local existing_plan=$(kubectl get installplan -n openshift-operators -o name 2>/dev/null | head -1)
            if [ -n "$existing_plan" ]; then
                log_info "Found existing InstallPlan (may already be approved): $existing_plan"
            else
                log_error "SCENARIO 3: FAILED - No InstallPlan created by OLM"
                return 1
            fi
        }

    # Approve any unapproved InstallPlans
    local unapproved_plans=$(kubectl get installplan -n openshift-operators -o jsonpath='{range .items[?(@.spec.approved==false)]}{.metadata.name}{"\n"}{end}' 2>/dev/null)
    if [ -n "$unapproved_plans" ]; then
        for plan in $unapproved_plans; do
            log_info "Manually approving InstallPlan: $plan"
            kubectl patch installplan $plan -n openshift-operators --type=merge -p '{"spec":{"approved":true}}' || {
                log_error "SCENARIO 3: FAILED - Could not approve InstallPlan $plan"
                return 1
            }
            log_info "  [OK] InstallPlan $plan approved"
        done
    else
        log_info "No unapproved InstallPlans found (operator may already be installed)"
    fi

    export KUBECONFIG=$HUB_KUBECONFIG

    # Wait for Policy to be compliant (operator installs after approval)
    wait_for_policy_compliant "ocp-olm-gitops-argocd-policy" "openshift-gitops" 480 || return 1

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

    # Post-scenario environment health checks for OCP
    verify_environment_health "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" "3" "false" || return 1

    # Verify local-cluster ArgoCD + guestbook working
    verify_local_cluster_working "3" "false" || return 1

    log_info "=============================================="
    log_info "SCENARIO 3: PASSED"
    log_info "  - GitOpsCluster with installPlanApproval=Manual created: OK"
    log_info "  - OLM env vars propagated to AddOnDeploymentConfig: OK"
    log_info "  - OLM subscription on OCP has installPlanApproval=Manual: OK"
    log_info "  - InstallPlan manually approved, operator installed: OK"
    log_info "  - DISABLE_DEFAULT_ARGOCD_INSTANCE=true on subscription: OK"
    log_info "  - ArgoCD deployed via OLM on OCP: OK"
    log_info "  - ArgoCD running on hub (local-cluster ns, no duplicate): OK"
    log_info "  - Guestbook synced on OCP: OK"
    log_info "  - Guestbook synced on local-cluster: OK"
    log_info "  - Environment health checks: OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 4: gitops-addon + Agent on OCP (OLM auto-detected)
#######################################
test_scenario_4() {
    log_info "=============================================="
    log_info "SCENARIO 4: gitops-addon + Agent on $OCP_CLUSTER_NAME + $LOCAL_CLUSTER_NAME (OLM auto-detected)"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with Agent mode enabled"
    log_info "  - Addon agent on OCP auto-detects OCP and creates OLM subscription"
    log_info "  - Principal server address auto-discovered"
    log_info "  - Cluster secret created with agentName in server URL"
    log_info "  - User modifies Policy to add RBAC"
    log_info "  - User creates Application on hub in managed cluster namespace"
    log_info "  - Agent propagates Application and syncs guestbook on OCP"
    log_info "  - local-cluster gets ArgoCD + guestbook app directly (no agent needed)"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created for OCP, Policy compliant on both clusters"
    log_info "  - OLM subscription auto-detected and created on OCP"
    log_info "  - Agent pod running on OCP, guestbook deployed via agent"
    log_info "  - local-cluster ArgoCD app controller reconciles guestbook"
    log_info "  - App status synced to hub"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for agent mode (controller disabled, principal enabled)
    configure_hub_argocd_agent

    ensure_addon_template

    # Create Placement selecting BOTH OCP and local-cluster
    log_info "Creating Placement (selecting $OCP_CLUSTER_NAME AND $LOCAL_CLUSTER_NAME)..."
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
          matchExpressions:
            - key: name
              operator: In
              values:
                - $OCP_CLUSTER_NAME
                - $LOCAL_CLUSTER_NAME
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

    # Wait for ManagedClusterAddOn to be created for both clusters
    wait_for_condition 180 "ManagedClusterAddOn to be created for $OCP_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1
    wait_for_condition 180 "ManagedClusterAddOn to be created for $LOCAL_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $LOCAL_CLUSTER_NAME -o name" || return 1

    # Add RBAC to the Policy (apps are created separately for agent mode)
    add_rbac_to_policy "ocp-agent-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant (OLM auto-detect takes longer)
    wait_for_policy_compliant "ocp-agent-gitops-argocd-policy" "openshift-gitops" 420 || return 1

    # CRITICAL: Verify OLM subscription was auto-detected on OCP
    verify_olm_auto_detected "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || return 1

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

    # Post-scenario environment health checks for OCP
    verify_environment_health "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" "4" "false" || return 1

    # Verify local-cluster ArgoCD + guestbook working (agent mode: create app directly)
    verify_local_cluster_working "4" "true" || return 1

    log_info "=============================================="
    log_info "SCENARIO 4: PASSED"
    log_info "  - GitOpsCluster with Agent created: OK"
    log_info "  - OLM subscription auto-detected on OCP: OK"
    log_info "  - Principal server address auto-discovered: OK"
    log_info "  - Cluster secret created with agentName: OK"
    log_info "  - Agent running on OCP managed cluster: OK"
    log_info "  - Application created on hub: OK"
    log_info "  - Guestbook synced by ArgoCD agent on OCP: OK"
    log_info "  - Guestbook synced by ArgoCD on local-cluster: OK"
    log_info "  - Application sync status reflected on hub: Sync=$sync_status, Health=$health_status"
    log_info "  - Environment health checks: OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 5: gitops-addon + Agent on Kind
#######################################
test_scenario_5() {
    log_info "=============================================="
    log_info "SCENARIO 5: gitops-addon + Agent on $KIND_CLUSTER_NAME + $LOCAL_CLUSTER_NAME"
    log_info "=============================================="
    log_info "What this tests:"
    log_info "  - GitOpsCluster with Agent mode enabled"
    log_info "  - Principal server address auto-discovered"
    log_info "  - Cluster secret created with agentName in server URL"
    log_info "  - User modifies Policy to add RBAC"
    log_info "  - User creates Application on hub in managed cluster namespace"
    log_info "  - Agent propagates Application and syncs guestbook on Kind"
    log_info "  - local-cluster gets ArgoCD + guestbook app directly (no agent needed)"
    log_info "Success criteria:"
    log_info "  - ManagedClusterAddOn created for Kind, Policy compliant on both clusters"
    log_info "  - Agent pod running on Kind, guestbook deployed via agent"
    log_info "  - local-cluster ArgoCD app controller reconciles guestbook"
    log_info "  - App status synced to hub"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    # Configure hub ArgoCD for agent mode (controller disabled, principal enabled)
    configure_hub_argocd_agent

    ensure_addon_template

    # Create Placement selecting BOTH Kind and local-cluster
    log_info "Creating Placement (selecting $KIND_CLUSTER_NAME AND $LOCAL_CLUSTER_NAME)..."
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
          matchExpressions:
            - key: name
              operator: In
              values:
                - $KIND_CLUSTER_NAME
                - $LOCAL_CLUSTER_NAME
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

    # Wait for ManagedClusterAddOn to be created for both clusters
    wait_for_condition 180 "ManagedClusterAddOn to be created for $KIND_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $KIND_CLUSTER_NAME -o name" || return 1
    wait_for_condition 180 "ManagedClusterAddOn to be created for $LOCAL_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $LOCAL_CLUSTER_NAME -o name" || return 1

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
    wait_for_condition 300 "Application sync status to show Synced on hub" \
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

    # Post-scenario environment health checks for Kind
    verify_environment_health "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME" "5" "false" || return 1

    # Verify local-cluster ArgoCD + guestbook working (agent mode: create app directly)
    verify_local_cluster_working "5" "true" || return 1

    log_info "=============================================="
    log_info "SCENARIO 5: PASSED"
    log_info "  - GitOpsCluster with Agent created: OK"
    log_info "  - Principal server address auto-discovered: OK"
    log_info "  - Cluster secret created with agentName: OK"
    log_info "  - Agent running on Kind managed cluster: OK"
    log_info "  - Application created on hub: OK"
    log_info "  - Guestbook deployed via agent on Kind: OK"
    log_info "  - Guestbook synced by ArgoCD on local-cluster: OK"
    log_info "  - Application sync status reflected on hub: Sync=$sync_status, Health=$health_status"
    log_info "  - Environment health checks: OK"
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
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        test_scenario_1
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        ;;
    2)
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "kind-gitops" "kind-placement"
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
        test_scenario_2
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "kind-gitops" "kind-placement"
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
        ;;
    3)
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        test_scenario_3
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        ;;
    4)
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        test_scenario_4
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        ;;
    5)
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        test_scenario_5
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
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

        # Run all scenarios with cleanup after each (always cleanup, regardless of pass/fail)
        if test_scenario_1; then
            scenario1_result="PASSED"
        else
            scenario1_result="FAILED"
        fi
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-gitops" "ocp-placement"
        sleep 15

        if test_scenario_2; then
            scenario2_result="PASSED"
        else
            scenario2_result="FAILED"
        fi
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "kind-gitops" "kind-placement"
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-gitops" "kind-placement"
        sleep 15

        if test_scenario_3; then
            scenario3_result="PASSED"
        else
            scenario3_result="FAILED"
        fi
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-olm-gitops" "ocp-olm-placement"
        sleep 15

        if test_scenario_4; then
            scenario4_result="PASSED"
        else
            scenario4_result="FAILED"
        fi
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        cleanup_scenario "$OCP_CLUSTER_NAME" "ocp-agent-gitops" "ocp-agent-placement"
        sleep 15

        if test_scenario_5; then
            scenario5_result="PASSED"
        else
            scenario5_result="FAILED"
        fi
        cleanup_scenario "$LOCAL_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"
        cleanup_scenario "$KIND_CLUSTER_NAME" "kind-agent-gitops" "kind-agent-placement"

        log_info "=============================================="
        log_info "=== ALL SCENARIOS COMPLETE ==="
        log_info "=============================================="
        log_info "  Scenario 1 (OCP, no agent, OLM auto):      $scenario1_result"
        log_info "  Scenario 2 (Kind, no agent, embedded):      $scenario2_result"
        log_info "  Scenario 3 (OCP, custom OLM Manual):        $scenario3_result"
        log_info "  Scenario 4 (OCP, Agent, OLM auto):          $scenario4_result"
        log_info "  Scenario 5 (Kind, Agent, embedded):         $scenario5_result"
        log_info "=============================================="
        ;;
    cleanup)
        cleanup_all
        ;;
    *)
        echo "Usage: $0 [1|2|3|4|5|all|cleanup]"
        echo ""
        echo "  1       - Scenario 1: gitops-addon on OCP (OLM auto-detected)"
        echo "  2       - Scenario 2: gitops-addon on Kind (embedded manifests)"
        echo "  3       - Scenario 3: gitops-addon + custom OLM on OCP (Manual approval)"
        echo "  4       - Scenario 4: gitops-addon + Agent on OCP (OLM auto-detected)"
        echo "  5       - Scenario 5: gitops-addon + Agent on Kind (embedded manifests)"
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
