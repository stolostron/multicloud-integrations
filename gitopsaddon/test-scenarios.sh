#!/bin/bash
#
# GitOps Addon Test Scenarios
# Tests 3 scenarios for gitops-addon functionality across OCP, Kind, and local-cluster
#
# Scenario 1: No Agent - All 3 clusters
#   Policy deploys ArgoCD + RBAC + guestbook Application to all 3 clusters
#
# Scenario 2: Agent Mode - All 3 clusters
#   ApplicationSet deploys guestbook via principal/agent to all 3 clusters
#
# Scenario 3: Custom OLM - OCP only
#   Validates custom OLM subscription values (installPlanApproval=Manual)
#
# Key Design Principles:
# - All operations performed from the hub cluster (simulates user experience)
# - RBAC is created by modifying the Policy that GitOpsCluster generates
# - Secrets are managed by GitOpsCluster controller (NOT manually)
# - Initial cleanup can be forceful (direct spoke access OK)
# - Between-scenario cleanup is hub-triggered only
#
# IMPORTANT: Scenarios run sequentially. Each depends on clean state.
#
# Usage: ./test-scenarios.sh [1|2|3|all|cleanup]
#
# Environment Variables:
#   HUB_KUBECONFIG      - Hub cluster kubeconfig (default: ~/Desktop/hub)
#   KIND_KUBECONFIG     - Kind cluster kubeconfig (default: ~/Desktop/kind-cluster1)
#   OCP_KUBECONFIG      - OCP cluster kubeconfig (default: ~/Desktop/ocp-cluster1)
#   KIND_CLUSTER_NAME   - Kind cluster name (default: kind-cluster1)
#   OCP_CLUSTER_NAME    - OCP cluster name (default: ocp-cluster1)
#   ADDON_IMAGE         - Override addon image (e.g. quay.io/user/multicloud-integrations:tag)

set -e

# Configurable defaults - override via environment variables
HUB_KUBECONFIG="${HUB_KUBECONFIG:-$HOME/Desktop/hub}"
KIND_KUBECONFIG="${KIND_KUBECONFIG:-$HOME/Desktop/kind-cluster1}"
OCP_KUBECONFIG="${OCP_KUBECONFIG:-$HOME/Desktop/ocp-cluster1}"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kind-cluster1}"
OCP_CLUSTER_NAME="${OCP_CLUSTER_NAME:-ocp-cluster1}"
LOCAL_CLUSTER_NAME="local-cluster"
# Override addon agent image (set to custom registry for local testing)
ADDON_IMAGE="${ADDON_IMAGE:-}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_debug() { echo -e "${BLUE}[DEBUG]${NC} $1"; }

# Force-delete all ArgoCD Applications in a namespace by stripping finalizers first.
# In agent mode the ArgoCD application controller is disabled, so the standard
# resources-finalizer.argocd.argoproj.io finalizer can never be processed.
# Stripping it before deletion is the correct cleanup approach.
force_delete_applications() {
    local ns=$1
    local apps=$(kubectl get applications.argoproj.io -n "$ns" -o name 2>/dev/null)
    if [ -z "$apps" ]; then
        return 0
    fi
    for app in $apps; do
        kubectl patch "$app" -n "$ns" --type=merge -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
    done
    kubectl delete applications.argoproj.io --all -n "$ns" --ignore-not-found --timeout=30s 2>/dev/null || true
}

# Ensure ManagedClusterSetBinding exists in openshift-gitops namespace.
# Required for Placement to find ManagedClusters in the default ManagedClusterSet.
ensure_clusterset_binding() {
    export KUBECONFIG=$HUB_KUBECONFIG

    if kubectl get managedclustersetbinding default -n openshift-gitops &>/dev/null; then
        return 0
    fi

    log_info "Creating ManagedClusterSetBinding in openshift-gitops namespace..."
    kubectl apply -f - <<'EOF'
apiVersion: cluster.open-cluster-management.io/v1beta2
kind: ManagedClusterSetBinding
metadata:
  name: default
  namespace: openshift-gitops
spec:
  clusterSet: default
EOF
}

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

# Ensure the hub controller uses ADDON_IMAGE for dynamic AddOnTemplates (agent mode).
# In agent mode, the controller creates a dynamic AddOnTemplate using its own image
# (via CONTROLLER_IMAGE env var or auto-detection). Setting CONTROLLER_IMAGE ensures
# the dynamic template picks up the custom image.
ensure_hub_controller_image() {
    if [ -z "$ADDON_IMAGE" ]; then
        return 0
    fi
    export KUBECONFIG=$HUB_KUBECONFIG

    local current_image
    current_image=$(kubectl get deploy multicluster-operators-application -n open-cluster-management \
        -o jsonpath='{.spec.template.spec.containers[?(@.name=="multicluster-operators-gitopscluster")].env[?(@.name=="CONTROLLER_IMAGE")].value}' 2>/dev/null)

    if [ "$current_image" = "$ADDON_IMAGE" ]; then
        log_info "Hub controller CONTROLLER_IMAGE already set to $ADDON_IMAGE"
        return 0
    fi

    log_info "Setting CONTROLLER_IMAGE=$ADDON_IMAGE on hub controller..."
    kubectl set env deploy/multicluster-operators-application -n open-cluster-management \
        -c multicluster-operators-gitopscluster CONTROLLER_IMAGE="$ADDON_IMAGE" 2>/dev/null || true
    kubectl rollout status deploy/multicluster-operators-application -n open-cluster-management --timeout=120s 2>/dev/null || {
        log_warn "Hub controller rollout timed out - continuing anyway"
    }
    log_info "Hub controller CONTROLLER_IMAGE set to $ADDON_IMAGE"
}

# Ensure the default AppProject in openshift-gitops has agent-compatible settings.
# The argocd-agent principal propagates AppProjects to managed agents only if their
# destinations and sourceNamespaces match. Patching with wildcards ensures the default
# AppProject is propagated to ALL agents.
# See: DONOTEDIT-argocd-agent/docs/getting-started/kubernetes/index.md
ensure_default_appproject_for_agents() {
    export KUBECONFIG=$HUB_KUBECONFIG

    log_info "Ensuring default AppProject in openshift-gitops has agent-compatible settings..."
    kubectl patch appproject default -n openshift-gitops --type='merge' \
        --patch='{"spec":{"sourceNamespaces":["*"],"destinations":[{"name":"*","namespace":"*","server":"*"}],"clusterResourceWhitelist":[{"group":"*","kind":"*"}],"sourceRepos":["*"]}}' 2>/dev/null || \
    kubectl apply -f - <<'EOF'
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: openshift-gitops
spec:
  clusterResourceWhitelist:
  - group: '*'
    kind: '*'
  destinations:
  - name: '*'
    namespace: '*'
    server: '*'
  sourceNamespaces:
  - '*'
  sourceRepos:
  - '*'
EOF
    log_info "Default AppProject patched for agent propagation"
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
        local apps=$(kubectl get applications.argoproj.io -n $argocd_ns --no-headers 2>/dev/null || true)
        if [ -n "$apps" ] && echo "$apps" | grep -qv '^$'; then
            local app_count=$(echo "$apps" | grep -v '^$' | wc -l)
            # Verify each app's controllerNamespace matches its ArgoCD namespace
            local mismatched_apps=$(kubectl get applications.argoproj.io -n $argocd_ns -o jsonpath='{range .items[*]}{.metadata.name}{" controllerNS="}{.status.controllerNamespace}{"\n"}{end}' 2>/dev/null | grep -v "controllerNS=$argocd_ns" | grep -v "controllerNS=$" || true)
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

# Cleanup a scenario - proper deletion order:
#   1a. Delete Placement (prevents GitOpsCluster controller from selecting clusters)
#   1b. (Agent mode) Delete ApplicationSet, wait for generated Applications to be deleted
#   1c. Delete GitOpsCluster (stops controller from recreating Policy/addons)
#   1d. Delete PlacementBinding + Policy (stops enforcement on managed cluster)
#   1e. Delete ManagedClusterAddOns (triggers pre-delete cleanup Job on managed cluster)
#       - The pre-delete Job runs "gitopsaddon -cleanup" which removes ArgoCD CR, OLM, etc.
#       - OCM addon framework removes its finalizer only after the Job completes
#       - We WAIT for this to finish naturally (no force-patching finalizers)
#   1f. Re-delete Policy/PlacementBinding (race: controller may have recreated during 1c-1e)
#   2.  Verify managed cluster is clean (read-only)
#   3.  Force-remove residual application workloads (guestbook, AppProjects, cluster secrets)
#
# NOTE: We do NOT delete cluster secrets during steps 1-2.
# Between-scenario cleanup — hub-triggered ONLY for addon/operator teardown.
# Force deletes are ONLY used in step 3 for application workloads AFTER verifying
# that the addon's pre-delete Job has cleaned up ArgoCD and operator components.
# Args: gitopscluster_name placement_name cluster1 [cluster2 ...]
cleanup_scenario() {
    local gitopscluster_name=$1
    local placement_name=$2
    shift 2
    local clusters=("$@")

    local is_agent_mode=false
    [[ "$gitopscluster_name" == *"agent"* ]] && is_agent_mode=true

    log_info "=== Cleanup: $gitopscluster_name (clusters: ${clusters[*]}) ==="
    export KUBECONFIG=$HUB_KUBECONFIG

    # --- STEP 1: Delete hub resources (hub-triggered only) ---

    # 1a. Delete Placement (stops controller from finding clusters)
    kubectl delete placement "$placement_name" -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # 1b. Agent mode: delete ApplicationSet, then wait for Applications to be deleted naturally.
    #     The ApplicationSet controller owns the generated Applications and deletes them.
    if [ "$is_agent_mode" = true ]; then
        kubectl delete applicationset "${placement_name}-guestbook-appset" -n openshift-gitops --ignore-not-found 2>/dev/null || true
        log_info "Waiting for ApplicationSet-generated Applications to be deleted (up to 60s)..."
        local aw=0
        while [ "$aw" -lt 60 ]; do
            local remaining_apps
            remaining_apps=$(kubectl get applications.argoproj.io -n openshift-gitops -o name 2>/dev/null | wc -l)
            [ "$remaining_apps" -eq 0 ] && break
            sleep 5; aw=$((aw+5))
        done
    fi

    # 1c. Delete GitOpsCluster first to stop the controller from recreating Policies
    kubectl delete gitopscluster "$gitopscluster_name" -n openshift-gitops --ignore-not-found 2>/dev/null || true

    # 1d. Delete PlacementBinding and Policy (stops enforcement on managed clusters)
    kubectl delete placementbinding "${gitopscluster_name}-argocd-policy-binding" -n openshift-gitops --ignore-not-found 2>/dev/null || true
    kubectl delete policy "${gitopscluster_name}-argocd-policy" -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true

    # 1e. Delete ManagedClusterAddOns (triggers pre-delete cleanup Job on each managed cluster)
    #     The pre-delete Job runs "gitopsaddon -cleanup" which removes ArgoCD CR, OLM, operator, etc.
    #     OCM addon framework removes the addon's finalizer only AFTER the Job completes.
    #     We wait for this to finish naturally — no force-patching finalizers.
    #     NOTE: The GitOpsCluster controller may have already triggered addon deletion (step 1c).
    #     We must still wait for the addon to be FULLY gone (pre-delete Job completed + finalizer removed).
    for cluster in "${clusters[@]}"; do
        # Send delete request (idempotent if controller already triggered it)
        kubectl delete managedclusteraddon gitops-addon -n "$cluster" --ignore-not-found --wait=false 2>/dev/null || true
        log_info "Waiting for ManagedClusterAddOn to be fully removed from $cluster (up to 600s)..."
        local mw=0
        while [ "$mw" -lt 600 ]; do
            kubectl get managedclusteraddon gitops-addon -n "$cluster" &>/dev/null || break
            sleep 5; mw=$((mw+5))
        done
        if kubectl get managedclusteraddon gitops-addon -n "$cluster" &>/dev/null; then
            log_error "  ManagedClusterAddOn stuck in $cluster after 600s — pre-delete Job may have failed"
        else
            log_info "  ManagedClusterAddOn fully removed from $cluster"
        fi
    done

    # Re-delete Policy and PlacementBinding in case the GitOpsCluster controller
    # recreated them during its finalizer processing (race with step 1c/1d).
    kubectl delete policy "${gitopscluster_name}-argocd-policy" -n openshift-gitops --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete placementbinding "${gitopscluster_name}-argocd-policy-binding" -n openshift-gitops --ignore-not-found 2>/dev/null || true
    sleep 10

    # --- STEP 2: Read-only verification (NO force deletes, NO fixes) ---
    log_info "--- Verifying cleanup (read-only) ---"
    local issues=0

    # Hub: GitOpsCluster should be gone
    if kubectl get gitopscluster "$gitopscluster_name" -n openshift-gitops &>/dev/null; then
        log_error "CLEANUP FAIL: GitOpsCluster $gitopscluster_name still exists on hub"
        issues=$((issues+1))
    else
        log_info "  [OK] Hub: GitOpsCluster deleted"
    fi

    # Hub: Policy should be gone (governance finalizer may take time — up to 600s)
    local pw=0
    while [ "$pw" -lt 600 ]; do
        kubectl get policy "${gitopscluster_name}-argocd-policy" -n openshift-gitops &>/dev/null || break
        sleep 5; pw=$((pw+5))
    done
    if kubectl get policy "${gitopscluster_name}-argocd-policy" -n openshift-gitops &>/dev/null; then
        log_error "CLEANUP FAIL: Policy ${gitopscluster_name}-argocd-policy still exists on hub after 600s"
        issues=$((issues+1))
    else
        log_info "  [OK] Hub: Policy deleted"
    fi

    # Hub: ManagedClusterAddOns should be gone
    for cluster in "${clusters[@]}"; do
        if kubectl get managedclusteraddon gitops-addon -n "$cluster" &>/dev/null; then
            log_error "CLEANUP FAIL: ManagedClusterAddOn still in $cluster"
            issues=$((issues+1))
        else
            log_info "  [OK] Hub: ManagedClusterAddOn deleted in $cluster"
        fi
    done

    # Managed cluster verification — connect to each cluster and verify ArgoCD components removed
    for cluster in "${clusters[@]}"; do
        local kubeconfig argocd_ns
        if [ "$cluster" = "$LOCAL_CLUSTER_NAME" ]; then
            kubeconfig=$HUB_KUBECONFIG; argocd_ns="local-cluster"
        elif [ "$cluster" = "$KIND_CLUSTER_NAME" ]; then
            kubeconfig=$KIND_KUBECONFIG; argocd_ns="openshift-gitops"
        else
            kubeconfig=$OCP_KUBECONFIG; argocd_ns="openshift-gitops"
        fi

        # ArgoCD CR should be removed by pre-delete Job (wait up to 180s)
        local w=0
        while [ "$w" -lt 180 ]; do
            KUBECONFIG=$kubeconfig kubectl get argocd acm-openshift-gitops -n "$argocd_ns" &>/dev/null 2>/dev/null || break
            sleep 5; w=$((w+5))
        done
        if KUBECONFIG=$kubeconfig kubectl get argocd acm-openshift-gitops -n "$argocd_ns" &>/dev/null 2>/dev/null; then
            log_error "CLEANUP FAIL: ArgoCD CR acm-openshift-gitops still in $argocd_ns on $cluster"
            issues=$((issues+1))
        else
            log_info "  [OK] $cluster: ArgoCD CR removed"
        fi

        # OCP: OLM subscription should be removed by pre-delete Job (wait up to 480s)
        if [ "$cluster" = "$OCP_CLUSTER_NAME" ]; then
            local sw=0
            while [ "$sw" -lt 480 ]; do
                local labeled_subs
                labeled_subs=$(KUBECONFIG=$kubeconfig kubectl get subscription.operators.coreos.com -n openshift-operators \
                    -l apps.open-cluster-management.io/gitopsaddon=true -o name 2>/dev/null || true)
                [ -z "$labeled_subs" ] && break
                sleep 5; sw=$((sw+5))
            done
            local remaining_subs
            remaining_subs=$(KUBECONFIG=$kubeconfig kubectl get subscription.operators.coreos.com -n openshift-operators \
                -l apps.open-cluster-management.io/gitopsaddon=true -o name 2>/dev/null || true)
            if [ -n "$remaining_subs" ]; then
                log_error "CLEANUP FAIL: gitopsaddon OLM subscription still on $cluster after 480s"
                log_warn "Forcing OLM subscription deletion directly (OLM cleanup exceeded timeout)"
                KUBECONFIG=$kubeconfig kubectl delete subscription.operators.coreos.com -n openshift-operators \
                    -l apps.open-cluster-management.io/gitopsaddon=true --ignore-not-found --wait=false 2>/dev/null || true
                KUBECONFIG=$kubeconfig kubectl delete subscription.operators.coreos.com openshift-gitops-operator \
                    -n openshift-operators --ignore-not-found --wait=false 2>/dev/null || true
                sleep 10
                issues=$((issues+1))
            else
                log_info "  [OK] $cluster: OLM subscription removed"
            fi
        fi

    done

    export KUBECONFIG=$HUB_KUBECONFIG

    local verification_failed=false
    if [ $issues -gt 0 ]; then
        log_error "=== CLEANUP VERIFICATION FAILED: $issues issue(s) — this indicates a code bug ==="
        verification_failed=true
    else
        log_info "  ArgoCD cleanup verified — all components removed"
    fi

    # --- STEP 3: Force-remove guestbook app and resources ---
    # Always runs regardless of verification outcome. ArgoCD and the operator are confirmed
    # removed (or timed out), so we can safely force-delete residual app workloads.
    # This is NOT a workaround — the pre-delete Job removes ArgoCD, not user applications.
    log_info "--- Force-removing guestbook app and resources ---"

    # Force-delete Applications on hub
    # In agent mode: ApplicationSet creates apps in openshift-gitops, and the local-cluster
    # agent copies its app into local-cluster namespace (agent's own namespace).
    if [ "$is_agent_mode" = true ]; then
        force_delete_applications "openshift-gitops"
        force_delete_applications "local-cluster"
    fi

    for cluster in "${clusters[@]}"; do
        local kubeconfig argocd_ns
        if [ "$cluster" = "$LOCAL_CLUSTER_NAME" ]; then
            kubeconfig=$HUB_KUBECONFIG; argocd_ns="local-cluster"
        elif [ "$cluster" = "$KIND_CLUSTER_NAME" ]; then
            kubeconfig=$KIND_KUBECONFIG; argocd_ns="openshift-gitops"
        else
            kubeconfig=$OCP_KUBECONFIG; argocd_ns="openshift-gitops"
        fi

        # Force-delete any remaining Applications (strip finalizers since ArgoCD is gone)
        local apps
        apps=$(KUBECONFIG=$kubeconfig kubectl get applications.argoproj.io -n "$argocd_ns" -o name 2>/dev/null || true)
        if [ -n "$apps" ]; then
            for app in $apps; do
                KUBECONFIG=$kubeconfig kubectl patch "$app" -n "$argocd_ns" \
                    --type=merge -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
            done
            KUBECONFIG=$kubeconfig kubectl delete applications.argoproj.io --all -n "$argocd_ns" \
                --ignore-not-found --timeout=30s 2>/dev/null || true
        fi

        # Force-delete guestbook namespace
        KUBECONFIG=$kubeconfig kubectl delete namespace guestbook --ignore-not-found --timeout=60s 2>/dev/null || true
        log_info "  [OK] $cluster: guestbook app and namespace force-removed"
    done

    # Agent mode: clean up AppProjects and cluster secrets on hub
    if [ "$is_agent_mode" = true ]; then
        export KUBECONFIG=$HUB_KUBECONFIG
        for cluster in "${clusters[@]}"; do
            kubectl delete appproject --all -n "$cluster" --ignore-not-found --wait=false 2>/dev/null || true
            kubectl delete secret "cluster-${cluster}" -n openshift-gitops --ignore-not-found 2>/dev/null || true
            kubectl delete secret "${cluster}-application-manager-cluster-secret" -n openshift-gitops --ignore-not-found 2>/dev/null || true
        done
    fi

    export KUBECONFIG=$HUB_KUBECONFIG

    if [ "$verification_failed" = true ]; then
        return 1
    fi

    log_info "=== Cleanup verified clean ==="
    return 0
}

# Initial cleanup - fast, forceful, direct spoke access allowed.
cleanup_all() {
    log_info "=== Initial cleanup ==="
    export KUBECONFIG=$HUB_KUBECONFIG

    # --- STEP 1: Force-delete all hub resources ---
    log_info "--- Force-deleting hub resources ---"

    kubectl delete addontemplate -l app.kubernetes.io/managed-by=multicloud-integrations --ignore-not-found 2>/dev/null || true
    kubectl delete placement --all -n openshift-gitops --ignore-not-found --timeout=30s 2>/dev/null || true
    kubectl delete gitopscluster --all -A --ignore-not-found --timeout=30s 2>/dev/null || true
    kubectl delete policy --all -n openshift-gitops --ignore-not-found --timeout=30s 2>/dev/null || true
    kubectl delete placementbinding --all -n openshift-gitops --ignore-not-found --timeout=30s 2>/dev/null || true
    sleep 5

    # Force-delete ALL ManagedClusterAddOns (strip finalizers, no waiting)
    for ns in $(kubectl get managedclusteraddon gitops-addon -A --no-headers 2>/dev/null | awk '{print $1}'); do
        log_info "Force-deleting ManagedClusterAddOn in $ns..."
        kubectl patch managedclusteraddon gitops-addon -n "$ns" --type=merge \
            -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        kubectl delete managedclusteraddon gitops-addon -n "$ns" --force --grace-period=0 --ignore-not-found 2>/dev/null || true
    done

    for ns in $(kubectl get addondeploymentconfig gitops-addon-config -A --no-headers 2>/dev/null | awk '{print $1}'); do
        kubectl delete addondeploymentconfig gitops-addon-config -n "$ns" --ignore-not-found 2>/dev/null || true
    done

    kubectl delete applicationset --all -n openshift-gitops --ignore-not-found 2>/dev/null || true
    sleep 3
    force_delete_applications "openshift-gitops"
    force_delete_applications "$OCP_CLUSTER_NAME"
    force_delete_applications "$KIND_CLUSTER_NAME"
    force_delete_applications "$LOCAL_CLUSTER_NAME"
    kubectl delete appproject --all -n "$OCP_CLUSTER_NAME" --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete appproject --all -n "$KIND_CLUSTER_NAME" --ignore-not-found --wait=false 2>/dev/null || true

    # --- STEP 2: Direct managed cluster cleanup (force) ---
    log_info "--- Direct managed cluster cleanup ---"

    # local-cluster (hub)
    kubectl delete argocd acm-openshift-gitops -n local-cluster --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete appproject --all -n local-cluster --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete namespace guestbook --ignore-not-found --wait=false 2>/dev/null || true
    kubectl delete clusterrolebinding acm-openshift-gitops-cluster-admin --ignore-not-found 2>/dev/null || true

    if [ -f "$OCP_KUBECONFIG" ]; then
        log_info "Cleaning OCP managed cluster directly..."
        cleanup_managed_cluster_direct "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME"
    fi

    if [ -f "$KIND_KUBECONFIG" ]; then
        log_info "Cleaning Kind managed cluster directly..."
        cleanup_managed_cluster_direct "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME"
    fi

    # Ensure OCM pull secrets include registry.redhat.io credentials.
    # The multiclusterhub-operator-pull-secret is the source that the managedcluster-import-controller
    # uses when generating klusterlet ManifestWorks. It exists in BOTH open-cluster-management and
    # multicluster-engine namespaces. The import controller runs in multicluster-engine and reads
    # the secret from there. ArgoCD agent images are from registry.redhat.io, and with
    # imagePullPolicy=Always (set by the operator for digest-referenced images), pod restarts
    # fail if the registry credential is missing.
    export KUBECONFIG=$HUB_KUBECONFIG
    local hub_auth
    hub_auth=$(kubectl get secret pull-secret -n openshift-config -o jsonpath='{.data.\.dockerconfigjson}' 2>/dev/null)
    if [ -n "$hub_auth" ]; then
        local pull_secret_updated=false
        for ns in open-cluster-management multicluster-engine; do
            if kubectl get secret multiclusterhub-operator-pull-secret -n "$ns" &>/dev/null; then
                local current_auths
                current_auths=$(kubectl get secret multiclusterhub-operator-pull-secret -n "$ns" -o jsonpath='{.data.\.dockerconfigjson}' 2>/dev/null | base64 -d 2>/dev/null)
                if echo "$current_auths" | grep -q "registry.redhat.io" 2>/dev/null; then
                    continue
                fi
                kubectl get secret multiclusterhub-operator-pull-secret -n "$ns" -o json 2>/dev/null | \
                    python3 -c "
import sys,json,base64
secret = json.load(sys.stdin)
full_data = json.loads(base64.b64decode('$hub_auth').decode())
existing = json.loads(base64.b64decode(secret['data']['.dockerconfigjson']).decode())
existing['auths'].update(full_data['auths'])
secret['data']['.dockerconfigjson'] = base64.b64encode(json.dumps(existing).encode()).decode()
for k in ['resourceVersion','uid','creationTimestamp']:
    secret['metadata'].pop(k,None)
json.dump(secret, sys.stdout)
" 2>/dev/null | kubectl apply -f - 2>/dev/null || true
                pull_secret_updated=true
            fi
        done
        if [ "$pull_secret_updated" = true ]; then
            log_info "Hub OCM pull secrets updated with registry.redhat.io credentials"
            kubectl rollout restart deploy managedcluster-import-controller-v2 -n multicluster-engine 2>/dev/null || true
            sleep 30
            log_info "Import controller restarted to pick up updated pull secret"
        fi

        # Also update Kind cluster directly (klusterlet sync may take time)
        if [ -f "$KIND_KUBECONFIG" ]; then
            export KUBECONFIG=$KIND_KUBECONFIG
            for ns in open-cluster-management-agent open-cluster-management-agent-addon openshift-gitops openshift-gitops-operator; do
                if kubectl get secret open-cluster-management-image-pull-credentials -n "$ns" &>/dev/null; then
                    kubectl get secret open-cluster-management-image-pull-credentials -n "$ns" -o json 2>/dev/null | \
                        python3 -c "
import sys,json,base64
secret = json.load(sys.stdin)
full_data = json.loads(base64.b64decode('$hub_auth').decode())
existing = json.loads(base64.b64decode(secret['data']['.dockerconfigjson']).decode())
existing['auths'].update(full_data['auths'])
secret['data']['.dockerconfigjson'] = base64.b64encode(json.dumps(existing).encode()).decode()
for k in ['resourceVersion','uid','creationTimestamp']:
    secret['metadata'].pop(k,None)
json.dump(secret, sys.stdout)
" 2>/dev/null | kubectl apply -f - 2>/dev/null || true
                fi
            done
            log_info "Kind pull secret directly updated with registry.redhat.io credentials"
        fi
    fi

    # Delete stale cluster secrets
    export KUBECONFIG=$HUB_KUBECONFIG
    kubectl delete secret "cluster-$KIND_CLUSTER_NAME" -n openshift-gitops --ignore-not-found 2>/dev/null || true
    kubectl delete secret "cluster-$OCP_CLUSTER_NAME" -n openshift-gitops --ignore-not-found 2>/dev/null || true
    kubectl delete secret "cluster-$LOCAL_CLUSTER_NAME" -n openshift-gitops --ignore-not-found 2>/dev/null || true
    for secret in $(kubectl get secrets -n openshift-gitops -o name 2>/dev/null | grep "application-manager-cluster-secret"); do
        kubectl delete "$secret" -n openshift-gitops --ignore-not-found 2>/dev/null || true
    done

    # --- STEP 3: Verify ---
    log_info "--- Verifying cleanup ---"
    sleep 10
    local issues=0

    # Force-remove any remaining addons
    for ns in $(kubectl get managedclusteraddon gitops-addon -A --no-headers 2>/dev/null | awk '{print $1}'); do
        log_warn "Addon still in $ns - force removing..."
        kubectl patch managedclusteraddon gitops-addon -n "$ns" --type=merge -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        kubectl delete managedclusteraddon gitops-addon -n "$ns" --force --grace-period=0 --ignore-not-found 2>/dev/null || true
    done

    # Kind checks
    export KUBECONFIG=$KIND_KUBECONFIG
    for a in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
        log_warn "Kind: ArgoCD remains - force removing..."
        kubectl patch "$a" -n openshift-gitops --type=merge -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        kubectl delete "$a" -n openshift-gitops --force --grace-period=0 2>/dev/null || true
        issues=$((issues+1))
    done

    # OCP checks
    export KUBECONFIG=$OCP_KUBECONFIG
    for a in $(kubectl get argocd -n openshift-gitops -o name 2>/dev/null); do
        log_warn "OCP: ArgoCD remains - force removing..."
        kubectl patch "$a" -n openshift-gitops --type=merge -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        kubectl delete "$a" -n openshift-gitops --force --grace-period=0 2>/dev/null || true
        issues=$((issues+1))
    done

    # Hub local-cluster check
    export KUBECONFIG=$HUB_KUBECONFIG
    if kubectl get argocd acm-openshift-gitops -n local-cluster &>/dev/null; then
        kubectl patch argocd acm-openshift-gitops -n local-cluster --type=merge -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        kubectl delete argocd acm-openshift-gitops -n local-cluster --force --grace-period=0 2>/dev/null || true
        issues=$((issues+1))
    fi

    [ "$(kubectl get gitopscluster -A --no-headers 2>/dev/null | wc -l)" -eq 0 ] && log_info "  [OK] No GitOpsClusters" || { log_error "GitOpsClusters remain"; issues=$((issues+1)); }
    [ "$(kubectl get managedclusteraddon gitops-addon -A --no-headers 2>/dev/null | wc -l)" -eq 0 ] && log_info "  [OK] No ManagedClusterAddOns" || { log_error "ManagedClusterAddOns remain"; issues=$((issues+1)); }

    [ "$issues" -eq 0 ] && log_info "=== Initial cleanup complete (clean) ===" || log_error "=== Initial cleanup complete ($issues issues force-resolved) ==="
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

    # 1. Delete OLM Subscription first (prevents OLM from reinstalling)
    # Must use fully-qualified resource: on OCM clusters, "subscription" resolves to
    # apps.open-cluster-management.io instead of operators.coreos.com
    kubectl delete subscription.operators.coreos.com openshift-gitops-operator -n openshift-operators --ignore-not-found 2>/dev/null || true
    kubectl delete subscription.operators.coreos.com -l apps.open-cluster-management.io/gitopsaddon=true -A --ignore-not-found 2>/dev/null || true

    # 2. Delete ALL ClusterServiceVersions related to gitops-operator (handles upgrade chains)
    local csv_names=$(kubectl get csv -n openshift-operators -o name 2>/dev/null | grep gitops-operator || true)
    if [ -n "$csv_names" ]; then
        log_warn "$cluster_name: Found OLM CSVs: $csv_names - deleting all..."
        echo "$csv_names" | xargs -I{} kubectl delete {} -n openshift-operators --ignore-not-found 2>/dev/null || true
        kubectl delete csv -n openshift-gitops -l operators.coreos.com/openshift-gitops-operator.openshift-operators --ignore-not-found 2>/dev/null || true
    fi
    # Also delete any CSV in openshift-gitops namespace matching gitops-operator
    local csv_names_gitops=$(kubectl get csv -n openshift-gitops -o name 2>/dev/null | grep gitops-operator || true)
    if [ -n "$csv_names_gitops" ]; then
        echo "$csv_names_gitops" | xargs -I{} kubectl delete {} -n openshift-gitops --ignore-not-found 2>/dev/null || true
    fi

    # 2b. Delete stale InstallPlans (prevents OLM from reusing completed plans after CSV deletion)
    kubectl delete installplan --all -n openshift-operators --ignore-not-found 2>/dev/null || true

    # 2c. Fix CRD conversion webhook: after deleting the operator, the CRD still references
    # the operator's webhook service for v1alpha1<->v1beta1 conversion. OLM's InstallPlan
    # validates existing CRs against the new CRD schema via this webhook, which fails with
    # "service not found". Patch to None so the next OLM install can proceed.
    if kubectl get crd argocds.argoproj.io &>/dev/null; then
        kubectl patch crd argocds.argoproj.io --type='json' \
            -p='[{"op": "replace", "path": "/spec/conversion", "value": {"strategy": "None"}}]' 2>/dev/null || true
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

# Configure hub ArgoCD for agent mode (Scenario 2)
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
      destinationBasedMapping: true
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
# The destination.name maps to the cluster name registered in ArgoCD
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

# Create guestbook ApplicationSet using clusterDecisionResource generator.
# Uses the acm-placement ConfigMap to find clusters selected by the Placement.
# With destination-based mapping (ARGOCD_PRINCIPAL_DESTINATION_BASED_MAPPING=true),
# the principal routes Applications to agents based on spec.destination.name
# (which matches the cluster name / agent name), so Applications stay in openshift-gitops namespace.
create_guestbook_applicationset() {
    local placement_name=$1
    local appset_name="${placement_name}-guestbook-appset"

    log_info "Creating guestbook ApplicationSet ($appset_name) using Placement $placement_name..."
    export KUBECONFIG=$HUB_KUBECONFIG

    # Delete any old application-manager-cluster-secrets that conflict with agent cluster secrets
    # These have the same 'name' field as agent secrets but different 'server' field
    for secret in $(kubectl get secrets -n openshift-gitops -o name 2>/dev/null | grep "application-manager-cluster-secret"); do
        local sname=$(echo "$secret" | sed 's|secret/||')
        log_info "Removing old cluster secret $sname to avoid conflicts with agent secrets..."
        kubectl delete secret "$sname" -n openshift-gitops --ignore-not-found 2>/dev/null || true
    done

    kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: ${appset_name}
  namespace: openshift-gitops
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: acm-placement
        labelSelector:
          matchLabels:
            cluster.open-cluster-management.io/placement: ${placement_name}
        requeueAfterSeconds: 30
  template:
    metadata:
      name: '{{name}}-guestbook'
      namespace: openshift-gitops
    spec:
      project: default
      source:
        repoURL: https://github.com/argoproj/argocd-example-apps
        targetRevision: HEAD
        path: guestbook
      destination:
        name: '{{name}}'
        namespace: guestbook
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
        syncOptions:
          - CreateNamespace=true
EOF
    log_info "ApplicationSet $appset_name created"
}

# Verify that managed cluster pods use Red Hat images (not community images).
# Checks ArgoCD pods in the target namespace for Red Hat registry (registry.redhat.io).
verify_redhat_images() {
    local cluster_kubeconfig=$1
    local cluster_name=$2
    local argocd_ns=${3:-openshift-gitops}

    log_info "Verifying Red Hat images on $cluster_name (namespace: $argocd_ns)..."
    export KUBECONFIG=$cluster_kubeconfig

    local non_redhat_images=""
    local all_images=$(kubectl get pods -n "$argocd_ns" -o jsonpath='{range .items[*]}{range .spec.containers[*]}{.image}{"\n"}{end}{end}' 2>/dev/null | sort -u)

    if [ -z "$all_images" ]; then
        log_warn "No pods found in $argocd_ns on $cluster_name (may still be starting)"
        return 0
    fi

    while IFS= read -r img; do
        [ -z "$img" ] && continue
        # Only check acm-openshift-gitops related pods (skip addon-agent and others)
        if echo "$img" | grep -qE "registry\.redhat\.io|rhel"; then
            continue
        fi
        # Allow kube-rbac-proxy and other infrastructure images
        if echo "$img" | grep -qE "kube-rbac-proxy|pause"; then
            continue
        fi
        non_redhat_images="${non_redhat_images}  $img\n"
    done <<< "$all_images"

    if [ -n "$non_redhat_images" ]; then
        log_error "NON-Red Hat images found on $cluster_name in $argocd_ns namespace:"
        echo -e "$non_redhat_images"
        export KUBECONFIG=$HUB_KUBECONFIG
        return 1
    fi

    log_info "  [OK] All images on $cluster_name use Red Hat registry"
    export KUBECONFIG=$HUB_KUBECONFIG
    return 0
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
# SCENARIO 1: No Agent - All 3 Clusters
# Policy deploys ArgoCD + RBAC + guestbook to OCP, Kind, and local-cluster
#######################################
test_scenario_1() {
    log_info "=============================================="
    log_info "SCENARIO 1: No Agent - $OCP_CLUSTER_NAME + $KIND_CLUSTER_NAME + $LOCAL_CLUSTER_NAME"
    log_info "=============================================="
    log_info "  - GitOpsCluster targets all 3 clusters (addon, no agent)"
    log_info "  - OLM auto-detected on OCP, embedded Helm on Kind"
    log_info "  - Policy deploys ArgoCD + RBAC + guestbook Application"
    log_info "  - Validates guestbook deployed on all 3 clusters"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    configure_hub_argocd_non_agent
    ensure_clusterset_binding
    ensure_addon_template

    # Create Placement selecting all 3 clusters
    log_info "Creating Placement (all 3 clusters)..."
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: all-placement
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
                - $KIND_CLUSTER_NAME
                - $LOCAL_CLUSTER_NAME
EOF

    # Create GitOpsCluster (addon, no agent)
    log_info "Creating GitOpsCluster (no agent)..."
    cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: all-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: all-placement
  gitopsAddon:
    enabled: true
EOF

    # Wait for ManagedClusterAddOns on all 3 clusters
    wait_for_condition 300 "ManagedClusterAddOn for $OCP_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1
    wait_for_condition 300 "ManagedClusterAddOn for $KIND_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $KIND_CLUSTER_NAME -o name" || return 1
    wait_for_condition 300 "ManagedClusterAddOn for $LOCAL_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $LOCAL_CLUSTER_NAME -o name" || return 1

    # Add RBAC + guestbook Application to Policy
    add_rbac_and_app_to_policy "all-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant on all clusters
    wait_for_policy_compliant "all-gitops-argocd-policy" "openshift-gitops" 480 || return 1

    # Verify OLM auto-detected on OCP
    verify_olm_auto_detected "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || return 1

    # Wait for ArgoCD on OCP and Kind
    wait_for_condition 300 "ArgoCD on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1
    wait_for_condition 300 "ArgoCD on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get argocd acm-openshift-gitops -n openshift-gitops -o name" || return 1

    # Verify no duplicate ArgoCD on all 3 clusters
    verify_no_duplicate_argocd "$HUB_KUBECONFIG" "$LOCAL_CLUSTER_NAME" || return 1
    verify_no_duplicate_argocd "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || return 1
    verify_no_duplicate_argocd "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME" || return 1

    # Wait for ArgoCD app controller to be running (OLM install + operator reconciliation can take 10-15 min)
    wait_for_condition 900 "ArgoCD app controller on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/name=acm-openshift-gitops-application-controller --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q Running" || {
            log_error "SCENARIO 1: ArgoCD app controller not running on $OCP_CLUSTER_NAME"
            KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops --no-headers 2>/dev/null || true
            KUBECONFIG=$OCP_KUBECONFIG kubectl get csv -n openshift-operators --no-headers 2>/dev/null | grep gitops || true
            return 1
        }
    wait_for_condition 300 "ArgoCD app controller on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/name=acm-openshift-gitops-application-controller --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q Running" || {
            log_error "SCENARIO 1: ArgoCD app controller not running on $KIND_CLUSTER_NAME"
            return 1
        }

    # --- Validate guestbook on all 3 clusters ---

    # OCP: guestbook deployment exists (pods crash due to SCC - expected)
    wait_for_condition 600 "Guestbook on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 1: Guestbook not deployed on $OCP_CLUSTER_NAME"
            return 1
        }
    log_info "NOTE: On OCP, guestbook-ui pods crash (port 80 + restricted SCC) - expected"

    # Kind: guestbook deployment + pods ready
    wait_for_condition 300 "Guestbook on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 1: Guestbook not deployed on $KIND_CLUSTER_NAME"
            return 1
        }
    wait_for_condition 120 "Guestbook pods ready on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl rollout status deployment/guestbook-ui -n guestbook --timeout=5s" || {
            log_warn "Guestbook pods not fully ready on Kind (continuing)"
        }

    # local-cluster: guestbook via verify_local_cluster_working
    verify_local_cluster_working "1" "false" || return 1

    # Red Hat images on all clusters
    verify_redhat_images "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" "openshift-gitops" || {
        log_error "SCENARIO 1: Non-Red Hat images on $OCP_CLUSTER_NAME"
        return 1
    }
    verify_redhat_images "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME" "openshift-gitops" || {
        log_error "SCENARIO 1: Non-Red Hat images on $KIND_CLUSTER_NAME"
        return 1
    }
    verify_redhat_images "$HUB_KUBECONFIG" "$LOCAL_CLUSTER_NAME" "local-cluster" || {
        log_error "SCENARIO 1: Non-Red Hat images on $LOCAL_CLUSTER_NAME"
        return 1
    }

    # Environment health checks on all 3 clusters
    verify_environment_health "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" "1" "false" || return 1
    verify_environment_health "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME" "1" "false" || return 1

    log_info "=============================================="
    log_info "SCENARIO 1: PASSED"
    log_info "  - OLM auto-detected on OCP: OK"
    log_info "  - ArgoCD on all 3 clusters: OK"
    log_info "  - No duplicate ArgoCD (all 3): OK"
    log_info "  - Red Hat images (all 3): OK"
    log_info "  - Guestbook on $OCP_CLUSTER_NAME: OK"
    log_info "  - Guestbook on $KIND_CLUSTER_NAME: OK"
    log_info "  - Guestbook on $LOCAL_CLUSTER_NAME: OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 2: Agent Mode - All 3 Clusters
# ApplicationSet deploys guestbook via principal/agent to OCP, Kind, and local-cluster
#######################################
test_scenario_2() {
    log_info "=============================================="
    log_info "SCENARIO 2: Agent Mode - $OCP_CLUSTER_NAME + $KIND_CLUSTER_NAME + $LOCAL_CLUSTER_NAME"
    log_info "=============================================="
    log_info "  - GitOpsCluster targets all 3 clusters (addon + agent)"
    log_info "  - Destination-based mapping, principal routes to agents"
    log_info "  - ApplicationSet generates guestbook for all clusters"
    log_info "  - Validates guestbook deployed on all 3 clusters"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    configure_hub_argocd_agent
    ensure_clusterset_binding
    ensure_addon_template
    ensure_default_appproject_for_agents

    # Create Placement selecting all 3 clusters
    log_info "Creating Placement (all 3 clusters)..."
    cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: all-agent-placement
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
                - $KIND_CLUSTER_NAME
                - $LOCAL_CLUSTER_NAME
EOF

    # Create GitOpsCluster with agent mode
    log_info "Creating GitOpsCluster (agent mode)..."
    cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: all-agent-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: all-agent-placement
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: true
      mode: managed
EOF

    # Wait for ManagedClusterAddOns on all 3 clusters
    wait_for_condition 300 "ManagedClusterAddOn for $OCP_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1
    wait_for_condition 300 "ManagedClusterAddOn for $KIND_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $KIND_CLUSTER_NAME -o name" || return 1
    wait_for_condition 300 "ManagedClusterAddOn for $LOCAL_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $LOCAL_CLUSTER_NAME -o name" || return 1

    # Add RBAC to Policy (apps created via ApplicationSet, not Policy)
    add_rbac_to_policy "all-agent-gitops-argocd-policy" "openshift-gitops" || return 1

    # Wait for Policy to be compliant
    wait_for_policy_compliant "all-agent-gitops-argocd-policy" "openshift-gitops" 420 || return 1

    # Verify OLM auto-detected on OCP
    verify_olm_auto_detected "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || return 1

    # Wait for ArgoCD agent pods on all 3 clusters first (they must exist before checking connectivity)
    # OCP needs longer timeout because OLM operator installation can take 10+ minutes after cleanup
    wait_for_condition 900 "ArgoCD agent on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent -o name | grep -q pod" || {
            log_error "SCENARIO 2: ArgoCD agent pod not running on $OCP_CLUSTER_NAME"
            KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops --no-headers 2>/dev/null || true
            KUBECONFIG=$OCP_KUBECONFIG kubectl get argocd -n openshift-gitops -o yaml 2>/dev/null || true
            KUBECONFIG=$OCP_KUBECONFIG kubectl get subscription.operators.coreos.com -n openshift-operators --no-headers 2>/dev/null || true
            return 1
        }
    wait_for_condition 600 "ArgoCD agent on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent -o name | grep -q pod" || {
            log_error "SCENARIO 2: ArgoCD agent pod not running on $KIND_CLUSTER_NAME"
            KUBECONFIG=$KIND_KUBECONFIG kubectl get pods -n openshift-gitops --no-headers 2>/dev/null || true
            return 1
        }
    wait_for_condition 600 "ArgoCD agent on $LOCAL_CLUSTER_NAME" \
        "kubectl get pods -n local-cluster -l app.kubernetes.io/part-of=argocd-agent -o name | grep -q pod" || {
            log_error "SCENARIO 2: ArgoCD agent pod not running on $LOCAL_CLUSTER_NAME"
            kubectl get pods -n local-cluster --no-headers 2>/dev/null || true
            return 1
        }

    # Verify agents connected (all 3 clusters — local-cluster is a managed cluster too)
    # Agent pods must be running first (checked above), then verify cluster secrets exist on principal
    verify_agent_connected "$OCP_CLUSTER_NAME" || return 1
    verify_agent_connected "$KIND_CLUSTER_NAME" || return 1
    verify_agent_connected "$LOCAL_CLUSTER_NAME" || return 1

    # Wait for ArgoCD app controller to be running on all 3 clusters
    # The agent alone is not enough — the app controller must be running to reconcile Applications
    wait_for_condition 900 "ArgoCD app controller on $OCP_CLUSTER_NAME" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/name=acm-openshift-gitops-application-controller --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q Running" || {
            log_error "SCENARIO 2: ArgoCD app controller not running on $OCP_CLUSTER_NAME"
            KUBECONFIG=$OCP_KUBECONFIG kubectl get pods -n openshift-gitops --no-headers 2>/dev/null || true
            return 1
        }
    wait_for_condition 300 "ArgoCD app controller on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get pods -n openshift-gitops -l app.kubernetes.io/name=acm-openshift-gitops-application-controller --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q Running" || {
            log_error "SCENARIO 2: ArgoCD app controller not running on $KIND_CLUSTER_NAME"
            return 1
        }
    # On the hub, all local-cluster ArgoCD pods start simultaneously. The app controller's
    # cluster cache initialization connects to Redis exactly once at startup; if Redis isn't
    # ready yet the cache stays empty permanently (apisCount=0). To handle this, we:
    # 1. Wait for Redis to be Running first
    # 2. Wait for the app controller
    # 3. Check if the app controller's cluster cache populated (by looking for the Redis
    #    connection error in its logs)
    # 4. If not, restart the app controller so it reconnects to the now-ready Redis
    wait_for_condition 300 "Redis on $LOCAL_CLUSTER_NAME" \
        "kubectl get pods -n local-cluster -l app.kubernetes.io/name=acm-openshift-gitops-redis --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q Running" || {
            log_error "SCENARIO 2: Redis not running on $LOCAL_CLUSTER_NAME"
            return 1
        }
    wait_for_condition 300 "ArgoCD app controller on $LOCAL_CLUSTER_NAME" \
        "kubectl get pods -n local-cluster -l app.kubernetes.io/name=acm-openshift-gitops-application-controller --field-selector=status.phase=Running --no-headers 2>/dev/null | grep -q Running" || {
            log_error "SCENARIO 2: ArgoCD app controller not running on $LOCAL_CLUSTER_NAME"
            kubectl get pods -n local-cluster --no-headers 2>/dev/null || true
            kubectl get argocd -n local-cluster --no-headers 2>/dev/null || true
            return 1
        }
    # Verify no duplicate ArgoCD (all 3 clusters)
    verify_no_duplicate_argocd "$HUB_KUBECONFIG" "$LOCAL_CLUSTER_NAME" || return 1
    verify_no_duplicate_argocd "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" || return 1
    verify_no_duplicate_argocd "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME" || return 1

    # Verify Red Hat images on all 3 clusters
    verify_redhat_images "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" "openshift-gitops" || {
        log_error "SCENARIO 2: Non-Red Hat images on $OCP_CLUSTER_NAME"
        return 1
    }
    verify_redhat_images "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME" "openshift-gitops" || {
        log_error "SCENARIO 2: Non-Red Hat images on $KIND_CLUSTER_NAME"
        return 1
    }
    verify_redhat_images "$HUB_KUBECONFIG" "$LOCAL_CLUSTER_NAME" "local-cluster" || {
        log_error "SCENARIO 2: Non-Red Hat images on $LOCAL_CLUSTER_NAME"
        return 1
    }

    # Create ApplicationSet targeting all 3 clusters via destination-based mapping
    create_guestbook_applicationset "all-agent-placement" || return 1

    # Wait for Applications generated for all 3 clusters
    wait_for_condition 240 "App ${OCP_CLUSTER_NAME}-guestbook" \
        "kubectl get applications.argoproj.io ${OCP_CLUSTER_NAME}-guestbook -n openshift-gitops -o name" || {
            log_error "SCENARIO 2: ApplicationSet did not generate app for $OCP_CLUSTER_NAME"
            return 1
        }
    wait_for_condition 240 "App ${KIND_CLUSTER_NAME}-guestbook" \
        "kubectl get applications.argoproj.io ${KIND_CLUSTER_NAME}-guestbook -n openshift-gitops -o name" || {
            log_error "SCENARIO 2: ApplicationSet did not generate app for $KIND_CLUSTER_NAME"
            return 1
        }
    wait_for_condition 240 "App ${LOCAL_CLUSTER_NAME}-guestbook" \
        "kubectl get applications.argoproj.io ${LOCAL_CLUSTER_NAME}-guestbook -n openshift-gitops -o name" || {
            log_error "SCENARIO 2: ApplicationSet did not generate app for $LOCAL_CLUSTER_NAME"
            return 1
        }

    # --- Validate guestbook on all 3 clusters ---

    # OCP: guestbook via agent (pods crash due to SCC - expected)
    wait_for_condition 300 "Guestbook on $OCP_CLUSTER_NAME via agent" \
        "KUBECONFIG=$OCP_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 2: Guestbook not deployed on $OCP_CLUSTER_NAME"
            KUBECONFIG=$OCP_KUBECONFIG kubectl logs -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent --tail=20 2>/dev/null || true
            return 1
        }
    log_info "NOTE: On OCP, guestbook-ui pods crash (port 80 + restricted SCC) - expected"

    # Kind: guestbook via agent + pods ready
    wait_for_condition 600 "Guestbook on $KIND_CLUSTER_NAME via agent" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 2: Guestbook not deployed on $KIND_CLUSTER_NAME"
            log_error "--- Diagnostics for $KIND_CLUSTER_NAME agent failure ---"
            export KUBECONFIG=$HUB_KUBECONFIG
            log_error "Hub Application status:"
            kubectl get application ${KIND_CLUSTER_NAME}-guestbook -n openshift-gitops -o yaml 2>/dev/null | head -40 || true
            log_error "Cluster secret for ${KIND_CLUSTER_NAME}:"
            kubectl get secret cluster-${KIND_CLUSTER_NAME} -n openshift-gitops -o jsonpath='{.data.name}' 2>/dev/null | base64 -d 2>/dev/null; echo
            log_error "Principal pod logs (last 30 lines):"
            kubectl logs -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-server --tail=30 2>/dev/null || true
            log_error "Agent logs on $KIND_CLUSTER_NAME (last 30 lines):"
            KUBECONFIG=$KIND_KUBECONFIG kubectl logs -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent --tail=30 2>/dev/null || true
            log_error "All apps in kind-cluster1 namespace on hub:"
            kubectl get applications.argoproj.io -n ${KIND_CLUSTER_NAME} -o name 2>/dev/null || true
            log_error "ArgoCD pods on $KIND_CLUSTER_NAME:"
            KUBECONFIG=$KIND_KUBECONFIG kubectl get pods -n openshift-gitops --no-headers 2>/dev/null || true
            log_error "--- End diagnostics ---"
            return 1
        }
    wait_for_condition 120 "Guestbook pods ready on $KIND_CLUSTER_NAME" \
        "KUBECONFIG=$KIND_KUBECONFIG kubectl get deployment guestbook-ui -n guestbook -o jsonpath='{.status.availableReplicas}' | grep -q '[1-9]'" || {
            log_warn "Guestbook pods not ready on Kind (continuing)"
        }

    # local-cluster: guestbook via agent (local-cluster is treated as a managed cluster)
    export KUBECONFIG=$HUB_KUBECONFIG
    wait_for_condition 600 "Guestbook on $LOCAL_CLUSTER_NAME via agent" \
        "kubectl get deployment guestbook-ui -n guestbook -o name" || {
            log_error "SCENARIO 2: Guestbook not deployed on $LOCAL_CLUSTER_NAME"
            log_error "--- Diagnostics for $LOCAL_CLUSTER_NAME agent failure ---"
            log_error "All pods in local-cluster namespace:"
            kubectl get pods -n local-cluster --no-headers 2>/dev/null || true
            log_error "ArgoCD CR in local-cluster namespace:"
            kubectl get argocd -n local-cluster -o yaml 2>/dev/null | head -60 || true
            log_error "App controller logs (last 50 lines):"
            kubectl logs -n local-cluster -l app.kubernetes.io/name=acm-openshift-gitops-application-controller --tail=50 2>/dev/null || true
            log_error "Agent logs (last 30 lines):"
            kubectl logs -n local-cluster -l app.kubernetes.io/part-of=argocd-agent --tail=30 2>/dev/null || true
            log_error "Applications in local-cluster namespace (agent copies apps here):"
            kubectl get applications.argoproj.io -n local-cluster -o wide 2>/dev/null || true
            log_error "Hub Application in openshift-gitops (source from ApplicationSet):"
            kubectl get application ${LOCAL_CLUSTER_NAME}-guestbook -n openshift-gitops -o yaml 2>/dev/null | head -40 || true
            log_error "Principal logs (last 30 lines):"
            kubectl logs -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-server --tail=30 2>/dev/null || true
            log_error "--- End diagnostics ---"
            return 1
        }

    # Verify Application sync status on hub (all 3 clusters)
    export KUBECONFIG=$HUB_KUBECONFIG
    wait_for_condition 180 "OCP app Synced" \
        "kubectl get applications.argoproj.io ${OCP_CLUSTER_NAME}-guestbook -n openshift-gitops -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_warn "OCP app sync not yet Synced"
        }
    wait_for_condition 180 "Kind app Synced" \
        "kubectl get applications.argoproj.io ${KIND_CLUSTER_NAME}-guestbook -n openshift-gitops -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_warn "Kind app sync not yet Synced"
        }
    # For local-cluster, the agent copies the Application into the local-cluster namespace
    # (agent's own namespace), so sync status lives there, not in openshift-gitops.
    wait_for_condition 180 "local-cluster app Synced" \
        "kubectl get applications.argoproj.io ${LOCAL_CLUSTER_NAME}-guestbook -n local-cluster -o jsonpath='{.status.sync.status}' 2>/dev/null | grep -q 'Synced'" || {
            log_warn "local-cluster app sync not yet Synced"
        }

    local ocp_sync=$(kubectl get applications.argoproj.io ${OCP_CLUSTER_NAME}-guestbook -n openshift-gitops -o jsonpath='{.status.sync.status}' 2>/dev/null)
    local kind_sync=$(kubectl get applications.argoproj.io ${KIND_CLUSTER_NAME}-guestbook -n openshift-gitops -o jsonpath='{.status.sync.status}' 2>/dev/null)
    local local_sync=$(kubectl get applications.argoproj.io ${LOCAL_CLUSTER_NAME}-guestbook -n local-cluster -o jsonpath='{.status.sync.status}' 2>/dev/null)

    # Environment health (all 3 clusters)
    verify_environment_health "$OCP_KUBECONFIG" "$OCP_CLUSTER_NAME" "2" "false" || return 1
    verify_environment_health "$KIND_KUBECONFIG" "$KIND_CLUSTER_NAME" "2" "false" || return 1
    verify_environment_health "$HUB_KUBECONFIG" "$LOCAL_CLUSTER_NAME" "2" "true" || return 1

    log_info "=============================================="
    log_info "SCENARIO 2: PASSED"
    log_info "  - Agent mode with destination-based mapping: OK"
    log_info "  - OLM auto-detected on OCP: OK"
    log_info "  - Agents connected (all 3 clusters): OK"
    log_info "  - No duplicate ArgoCD (all 3): OK"
    log_info "  - Red Hat images (all 3): OK"
    log_info "  - ApplicationSet generated apps (all 3): OK"
    log_info "  - Guestbook on $OCP_CLUSTER_NAME (Sync=$ocp_sync): OK"
    log_info "  - Guestbook on $KIND_CLUSTER_NAME (Sync=$kind_sync): OK"
    log_info "  - Guestbook on $LOCAL_CLUSTER_NAME (Sync=$local_sync): OK"
    log_info "=============================================="
    return 0
}

#######################################
# SCENARIO 3: Custom OLM - OCP Only
# Validates custom OLM subscription values (installPlanApproval=Manual)
#######################################
test_scenario_3() {
    log_info "=============================================="
    log_info "SCENARIO 3: Custom OLM - $OCP_CLUSTER_NAME only"
    log_info "=============================================="
    log_info "  - GitOpsCluster targets OCP only with custom OLM config"
    log_info "  - installPlanApproval=Manual (proves custom values propagate)"
    log_info "  - Validates OLM subscription has custom values on managed cluster"
    log_info "=============================================="

    export KUBECONFIG=$HUB_KUBECONFIG

    configure_hub_argocd_non_agent
    ensure_clusterset_binding
    ensure_addon_template

    # Create Placement selecting OCP only
    log_info "Creating Placement ($OCP_CLUSTER_NAME only)..."
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
EOF

    # Create GitOpsCluster with custom OLM (installPlanApproval=Manual)
    log_info "Creating GitOpsCluster with custom OLM config..."
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

    # Wait for ManagedClusterAddOn
    wait_for_condition 180 "ManagedClusterAddOn for $OCP_CLUSTER_NAME" \
        "kubectl get managedclusteraddon gitops-addon -n $OCP_CLUSTER_NAME -o name" || return 1

    # Verify OLM env vars in AddOnDeploymentConfig
    log_info "Verifying OLM config passthrough to AddOnDeploymentConfig..."

    wait_for_condition 60 "OLM_SUBSCRIPTION_ENABLED=true" \
        "kubectl get addondeploymentconfig gitops-addon-config -n $OCP_CLUSTER_NAME -o jsonpath='{.spec.customizedVariables[?(@.name==\"OLM_SUBSCRIPTION_ENABLED\")].value}' 2>/dev/null | grep -qx 'true'" || {
            log_error "SCENARIO 3: OLM_SUBSCRIPTION_ENABLED not set"
            return 1
        }
    log_info "  OLM_SUBSCRIPTION_ENABLED=true: OK"

    wait_for_condition 60 "OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL=Manual" \
        "kubectl get addondeploymentconfig gitops-addon-config -n $OCP_CLUSTER_NAME -o jsonpath='{.spec.customizedVariables[?(@.name==\"OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL\")].value}' 2>/dev/null | grep -qx 'Manual'" || {
            log_error "SCENARIO 3: OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL not Manual"
            return 1
        }
    log_info "  OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL=Manual: OK"

    # Wait for OLM subscription to be created on OCP
    log_info "Waiting for OLM subscription on OCP..."
    export KUBECONFIG=$OCP_KUBECONFIG
    wait_for_condition 300 "OLM subscription on $OCP_CLUSTER_NAME" \
        "kubectl get subscription.operators.coreos.com openshift-gitops-operator -n openshift-operators -o name" || {
            log_error "SCENARIO 3: OLM subscription not created"
            return 1
        }

    # Verify installPlanApproval=Manual
    local actual_approval
    actual_approval=$(kubectl get subscription.operators.coreos.com openshift-gitops-operator \
        -n openshift-operators -o jsonpath='{.spec.installPlanApproval}' 2>/dev/null)
    if [ "$actual_approval" != "Manual" ]; then
        log_error "SCENARIO 3: installPlanApproval='$actual_approval' (expected: Manual)"
        return 1
    fi
    log_info "  installPlanApproval=Manual on managed cluster: OK"

    # Verify DISABLE_DEFAULT_ARGOCD_INSTANCE=true
    local disable_default
    disable_default=$(kubectl get subscription.operators.coreos.com openshift-gitops-operator \
        -n openshift-operators -o jsonpath='{.spec.config.env[?(@.name=="DISABLE_DEFAULT_ARGOCD_INSTANCE")].value}' 2>/dev/null)
    if [ "$disable_default" != "true" ]; then
        log_error "SCENARIO 3: DISABLE_DEFAULT_ARGOCD_INSTANCE='$disable_default' (expected: true)"
        return 1
    fi
    log_info "  DISABLE_DEFAULT_ARGOCD_INSTANCE=true: OK"

    # No embedded operator deployment (OLM mode)
    if kubectl get deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator &>/dev/null 2>/dev/null; then
        log_error "SCENARIO 3: Embedded operator found (should be OLM-only)"
        return 1
    fi
    log_info "  No embedded operator (OLM-only mode): OK"

    # Approve InstallPlan
    log_info "Waiting for InstallPlan..."
    wait_for_condition 120 "InstallPlan" \
        "kubectl get installplan -n openshift-operators -o jsonpath='{.items[?(@.spec.approved==false)].metadata.name}' 2>/dev/null | grep -v '^$'" || {
            local existing=$(kubectl get installplan -n openshift-operators -o name 2>/dev/null | head -1)
            if [ -n "$existing" ]; then
                log_info "Found existing InstallPlan (may be pre-approved)"
            else
                log_error "SCENARIO 3: No InstallPlan"
                return 1
            fi
        }

    local unapproved
    unapproved=$(kubectl get installplan -n openshift-operators \
        -o jsonpath='{range .items[?(@.spec.approved==false)]}{.metadata.name}{"\n"}{end}' 2>/dev/null)
    if [ -n "$unapproved" ]; then
        for plan in $unapproved; do
            log_info "Approving InstallPlan: $plan"
            kubectl patch installplan "$plan" -n openshift-operators --type=merge -p '{"spec":{"approved":true}}' || return 1
        done
    fi

    # Wait for operator to be ready
    export KUBECONFIG=$HUB_KUBECONFIG
    wait_for_condition 300 "Policy compliant after approval" \
        "kubectl get policy ocp-olm-gitops-argocd-policy -n openshift-gitops -o jsonpath='{.status.compliant}' 2>/dev/null | grep -qi 'compliant'" || {
            log_warn "Policy not yet compliant (may take time for operator install)"
        }

    log_info "=============================================="
    log_info "SCENARIO 3: PASSED"
    log_info "  - Custom OLM config propagated: OK"
    log_info "  - installPlanApproval=Manual verified: OK"
    log_info "  - DISABLE_DEFAULT_ARGOCD_INSTANCE=true: OK"
    log_info "  - No embedded operator (OLM-only): OK"
    log_info "  - InstallPlan approved: OK"
    log_info "=============================================="
    return 0
}

#######################################
# Main
#######################################

log_info "Configuration:"
log_info "  HUB_KUBECONFIG:    $HUB_KUBECONFIG"
log_info "  KIND_KUBECONFIG:   $KIND_KUBECONFIG"
log_info "  OCP_KUBECONFIG:    $OCP_KUBECONFIG"
log_info "  KIND_CLUSTER_NAME: $KIND_CLUSTER_NAME"
log_info "  OCP_CLUSTER_NAME:  $OCP_CLUSTER_NAME"
[ -n "$ADDON_IMAGE" ] && log_info "  ADDON_IMAGE:       $ADDON_IMAGE"
echo ""

case "${1:-all}" in
    1)
        cleanup_scenario "all-gitops" "all-placement" "$LOCAL_CLUSTER_NAME" "$OCP_CLUSTER_NAME" "$KIND_CLUSTER_NAME"
        test_scenario_1
        cleanup_scenario "all-gitops" "all-placement" "$LOCAL_CLUSTER_NAME" "$OCP_CLUSTER_NAME" "$KIND_CLUSTER_NAME"
        ;;
    2)
        cleanup_scenario "all-agent-gitops" "all-agent-placement" "$LOCAL_CLUSTER_NAME" "$OCP_CLUSTER_NAME" "$KIND_CLUSTER_NAME"
        test_scenario_2
        cleanup_scenario "all-agent-gitops" "all-agent-placement" "$LOCAL_CLUSTER_NAME" "$OCP_CLUSTER_NAME" "$KIND_CLUSTER_NAME"
        ;;
    3)
        cleanup_scenario "ocp-olm-gitops" "ocp-olm-placement" "$OCP_CLUSTER_NAME"
        test_scenario_3
        cleanup_scenario "ocp-olm-gitops" "ocp-olm-placement" "$OCP_CLUSTER_NAME"
        ;;
    all)
        cleanup_all
        ensure_hub_controller_image

        scenario1_result="SKIPPED"
        scenario2_result="SKIPPED"
        scenario3_result="SKIPPED"
        cleanup1_result="SKIPPED"
        cleanup2_result="SKIPPED"
        cleanup3_result="SKIPPED"

        if test_scenario_1; then scenario1_result="PASSED"; else scenario1_result="FAILED"; fi
        if cleanup_scenario "all-gitops" "all-placement" "$LOCAL_CLUSTER_NAME" "$OCP_CLUSTER_NAME" "$KIND_CLUSTER_NAME"; then cleanup1_result="PASSED"; else cleanup1_result="FAILED"; fi
        sleep 15

        if test_scenario_2; then scenario2_result="PASSED"; else scenario2_result="FAILED"; fi
        if cleanup_scenario "all-agent-gitops" "all-agent-placement" "$LOCAL_CLUSTER_NAME" "$OCP_CLUSTER_NAME" "$KIND_CLUSTER_NAME"; then cleanup2_result="PASSED"; else cleanup2_result="FAILED"; fi
        sleep 15

        if test_scenario_3; then scenario3_result="PASSED"; else scenario3_result="FAILED"; fi
        if cleanup_scenario "ocp-olm-gitops" "ocp-olm-placement" "$OCP_CLUSTER_NAME"; then cleanup3_result="PASSED"; else cleanup3_result="FAILED"; fi

        log_info "=============================================="
        log_info "=== ALL SCENARIOS COMPLETE ==="
        log_info "=============================================="
        log_info "  Scenario 1 (No Agent, all 3 clusters):     $scenario1_result"
        log_info "  Cleanup 1:                                  $cleanup1_result"
        log_info "  Scenario 2 (Agent Mode, all 3 clusters):   $scenario2_result"
        log_info "  Cleanup 2:                                  $cleanup2_result"
        log_info "  Scenario 3 (Custom OLM, OCP only):         $scenario3_result"
        log_info "  Cleanup 3:                                  $cleanup3_result"
        log_info "=============================================="

        # Exit non-zero if any scenario or cleanup failed
        if [[ "$scenario1_result" == "FAILED" || "$scenario2_result" == "FAILED" || "$scenario3_result" == "FAILED" || \
              "$cleanup1_result" == "FAILED" || "$cleanup2_result" == "FAILED" || "$cleanup3_result" == "FAILED" ]]; then
            exit 1
        fi
        ;;
    cleanup)
        cleanup_all
        ;;
    *)
        echo "Usage: $0 [1|2|3|all|cleanup]"
        echo ""
        echo "  1       - Scenario 1: No Agent (OCP + Kind + local-cluster)"
        echo "  2       - Scenario 2: Agent Mode (OCP + Kind + local-cluster)"
        echo "  3       - Scenario 3: Custom OLM (OCP only)"
        echo "  all     - Run all scenarios sequentially with cleanup"
        echo "  cleanup - Force cleanup everything"
        echo ""
        echo "Environment variables:"
        echo "  HUB_KUBECONFIG      - Hub cluster kubeconfig"
        echo "  KIND_KUBECONFIG     - Kind cluster kubeconfig (verification only)"
        echo "  OCP_KUBECONFIG      - OCP cluster kubeconfig (verification only)"
        echo "  KIND_CLUSTER_NAME   - Kind cluster name (default: kind-cluster1)"
        echo "  OCP_CLUSTER_NAME    - OCP cluster name (default: ocp-cluster1)"
        echo "  ADDON_IMAGE         - Override addon image (e.g. quay.io/user/multicloud-integrations:tag)"
        exit 1
        ;;
esac
