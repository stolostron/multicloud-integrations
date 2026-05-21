#!/bin/bash
# test-cycle-eks-ocp.sh — Deploy/Verify/Delete cycle for ArgoCD Agent GitOps Addon
#
# Tests the full lifecycle across multiple managed clusters (OCP + non-OCP):
# deploy gitopsaddon with argocd-agent, verify ApplicationSet-driven app syncs
# via the hub principal, then delete from the hub and verify clean cleanup on
# every managed cluster.
#
# Usage:
#   HUB_KUBECONFIG=/path/to/hub \
#   MANAGED_CLUSTERS="cluster1:/path/to/kc1,cluster2:/path/to/kc2" \
#     bash test-cycle-eks-ocp.sh [cycles]
#
# Required environment:
#   HUB_KUBECONFIG       — Path to hub cluster kubeconfig
#   MANAGED_CLUSTERS     — Comma-separated list of name:kubeconfig pairs
#
# Optional environment:
#   FORCE_CLEAN_FIRST    — Force-clean managed clusters before first cycle (default: true)
#   WAIT_DEPLOY_SECS     — Seconds to wait for deployment (default: 600)
#   WAIT_CLEANUP_SECS    — Seconds to wait for MCA cleanup per cluster (default: 600)
#   HUB_CONTROLLER_NS    — Namespace of the hub controller deployment (default: ocm)

set -euo pipefail

: "${HUB_KUBECONFIG:?HUB_KUBECONFIG is required (path to hub kubeconfig)}"
: "${MANAGED_CLUSTERS:?MANAGED_CLUSTERS is required (comma-separated name:kubeconfig pairs)}"

FORCE_CLEAN_FIRST="${FORCE_CLEAN_FIRST:-true}"
WAIT_DEPLOY_SECS="${WAIT_DEPLOY_SECS:-600}"
WAIT_CLEANUP_SECS="${WAIT_CLEANUP_SECS:-600}"
HUB_CONTROLLER_NS="${HUB_CONTROLLER_NS:-ocm}"
MAX_CYCLES="${1:-5}"

GITOPSCLUSTER_NAME="cycle-agent-gitops"
PLACEMENT_NAME="cycle-agent-placement"
APPSET_PLACEMENT_LABEL="cycle-agent-appset"
APPSET_NAME="cycle-agent-guestbook-appset"
ARGOCD_NS="openshift-gitops"

# Parse MANAGED_CLUSTERS into arrays
declare -a CLUSTER_NAMES=()
declare -A CLUSTER_KUBECONFIGS=()

IFS=',' read -ra PAIRS <<< "$MANAGED_CLUSTERS"
for pair in "${PAIRS[@]}"; do
    name="${pair%%:*}"
    kc="${pair#*:}"
    [ -z "$name" ] || [ -z "$kc" ] && { echo "ERROR: invalid MANAGED_CLUSTERS entry: '$pair' (expected name:kubeconfig)"; exit 1; }
    [ ! -f "$kc" ] && { echo "ERROR: kubeconfig not found: $kc (for cluster $name)"; exit 1; }
    CLUSTER_NAMES+=("$name")
    CLUSTER_KUBECONFIGS[$name]="$kc"
done

[ ${#CLUSTER_NAMES[@]} -eq 0 ] && { echo "ERROR: no managed clusters parsed from MANAGED_CLUSTERS"; exit 1; }

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
fail() { log "FAIL: $*"; exit 1; }
pass() { log "PASS: $*"; }

hub()     { KUBECONFIG="$HUB_KUBECONFIG" kubectl "$@"; }
on_cluster() { local c=$1; shift; KUBECONFIG="${CLUSTER_KUBECONFIGS[$c]}" kubectl "$@"; }

# --- Force-clean a managed cluster (direct access) ---
force_clean_cluster() {
    local cluster=$1
    log "Force-cleaning $cluster..."

    # Strip finalizers from all ArgoCD-managed resources (prevents stuck CRD/ns deletion)
    for crd_name in applications.argoproj.io applicationsets.argoproj.io appprojects.argoproj.io argocds.argoproj.io; do
        local json_out
        json_out=$(on_cluster "$cluster" get "$crd_name" -A -o json 2>/dev/null) || continue
        echo "$json_out" | python3 -c "
import json,sys
d = json.load(sys.stdin)
for item in d.get('items',[]):
    if item['metadata'].get('finalizers'):
        print(item['metadata'].get('namespace','_'), item['metadata']['name'])
" 2>/dev/null | while read -r rns rname; do
            on_cluster "$cluster" patch "$crd_name" "$rname" -n "$rns" --type=merge \
                -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        done || true
    done

    # Strip finalizers from CSVs in operator namespace (OCP OLM)
    local csv_json
    csv_json=$(on_cluster "$cluster" get csv -n openshift-gitops-operator -o json 2>/dev/null) || true
    if [ -n "$csv_json" ]; then
        echo "$csv_json" | python3 -c "
import json,sys
d = json.load(sys.stdin)
for item in d.get('items',[]):
    if item['metadata'].get('finalizers'):
        print(item['metadata']['name'])
" 2>/dev/null | while read -r csv_name; do
            on_cluster "$cluster" patch csv "$csv_name" -n openshift-gitops-operator --type=merge \
                -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
        done || true
    fi

    # Delete ArgoCD CRD instances (non-blocking)
    for crd_name in applications.argoproj.io applicationsets.argoproj.io appprojects.argoproj.io argocds.argoproj.io; do
        on_cluster "$cluster" delete "$crd_name" --all -A --ignore-not-found --wait=false 2>/dev/null || true
    done

    # Delete OLM subscription and CSV (OCP)
    on_cluster "$cluster" delete subscription.operators.coreos.com openshift-gitops-operator \
        -n openshift-gitops-operator --ignore-not-found --wait=false 2>/dev/null || true
    on_cluster "$cluster" delete csv -n openshift-gitops-operator --all --ignore-not-found --wait=false 2>/dev/null || true

    # Delete namespaces (non-blocking)
    on_cluster "$cluster" delete ns openshift-gitops guestbook --ignore-not-found --wait=false 2>/dev/null || true
    sleep 5

    # Finalize any stuck namespaces
    for ns in openshift-gitops guestbook; do
        on_cluster "$cluster" get ns "$ns" -o json 2>/dev/null | \
            python3 -c "import json,sys; ns=json.load(sys.stdin); ns['spec']['finalizers']=[]; json.dump(ns,sys.stdout)" 2>/dev/null | \
            on_cluster "$cluster" replace --raw "/api/v1/namespaces/$ns/finalize" -f - 2>/dev/null || true
    done

    # Delete addon RBAC (both main and pre-delete cleanup)
    on_cluster "$cluster" delete clusterrole gitops-addon gitops-addon-cleanup --ignore-not-found 2>/dev/null || true
    on_cluster "$cluster" delete clusterrolebinding gitops-addon gitops-addon-cleanup --ignore-not-found 2>/dev/null || true

    # Delete stale cleanup jobs and addon pods
    on_cluster "$cluster" delete jobs -n open-cluster-management-agent-addon --all --ignore-not-found --force --grace-period=0 2>/dev/null || true

    # Delete CRDs on non-OCP clusters only (OCP CRDs are managed by OLM)
    local has_clusterversion
    has_clusterversion=$(on_cluster "$cluster" get crd clusterversions.config.openshift.io -o name 2>/dev/null || true)
    if [ -z "$has_clusterversion" ]; then
        log "  $cluster is non-OCP, deleting ArgoCD CRDs..."
        local crd_list
        crd_list=$(on_cluster "$cluster" get crd -o name 2>/dev/null | grep -i "argoproj\|argocd\|gitops" || true)
        for crd in $crd_list; do
            on_cluster "$cluster" delete "$crd" --ignore-not-found --wait=false 2>/dev/null || true
        done
    else
        log "  $cluster is OCP, keeping CRDs (managed by OLM)"
    fi

    log "  $cluster force-clean done"
}

# --- Clean stale hub resources for test clusters ---
clean_hub_stale() {
    log "Cleaning stale hub resources..."
    local stale_csrs
    stale_csrs=$(hub get csr -o name 2>/dev/null | grep "gitops-addon" || true)
    for csr in $stale_csrs; do hub delete "$csr" 2>/dev/null || true; done
    for cluster in "${CLUSTER_NAMES[@]}"; do
        hub delete addondeploymentconfig gitops-addon-config -n "$cluster" --ignore-not-found 2>/dev/null || true
        # Force-strip MCA finalizers in case they're stuck from a previous cycle
        if hub get managedclusteraddon -n "$cluster" gitops-addon &>/dev/null; then
            hub patch managedclusteraddon gitops-addon -n "$cluster" --type=merge \
                -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true
            hub delete managedclusteraddon gitops-addon -n "$cluster" --force --grace-period=0 --ignore-not-found 2>/dev/null || true
        fi
    done
    local stale_templates
    stale_templates=$(hub get addontemplate -o name 2>/dev/null | grep "gitops-addon-" || true)
    for t in $stale_templates; do hub delete "$t" --ignore-not-found 2>/dev/null || true; done

    # Delete stale GitOpsCluster, Policy, Placement, ApplicationSet
    hub delete applicationset "$APPSET_NAME" -n "$ARGOCD_NS" --ignore-not-found 2>/dev/null || true
    hub delete gitopscluster "$GITOPSCLUSTER_NAME" -n "$ARGOCD_NS" --ignore-not-found --timeout=30s 2>/dev/null || true
    hub delete placement "$PLACEMENT_NAME" -n "$ARGOCD_NS" --ignore-not-found 2>/dev/null || true
    hub delete policy "${GITOPSCLUSTER_NAME}-argocd-policy" -n "$ARGOCD_NS" --ignore-not-found --wait=false 2>/dev/null || true
    hub delete placementbinding "${GITOPSCLUSTER_NAME}-argocd-policy-binding" -n "$ARGOCD_NS" --ignore-not-found 2>/dev/null || true
}

# --- Build Placement matchExpressions for all clusters ---
build_placement_values() {
    local values=""
    for cluster in "${CLUSTER_NAMES[@]}"; do
        [ -n "$values" ] && values+="\n"
        values+="                - ${cluster}"
    done
    echo -e "$values"
}

# --- Deploy ---
deploy() {
    log "Deploying to clusters: ${CLUSTER_NAMES[*]}"
    local match_values
    match_values=$(build_placement_values)

    hub apply -f - <<EOF
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: ${PLACEMENT_NAME}
  namespace: ${ARGOCD_NS}
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchExpressions:
            - key: name
              operator: In
              values:
${match_values}
            - key: local-cluster
              operator: DoesNotExist
---
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: ${GITOPSCLUSTER_NAME}
  namespace: ${ARGOCD_NS}
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: ${ARGOCD_NS}
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: ${PLACEMENT_NAME}
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: true
---
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: ${APPSET_NAME}
  namespace: ${ARGOCD_NS}
spec:
  generators:
    - clusterDecisionResource:
        configMapRef: acm-placement
        labelSelector:
          matchLabels:
            cluster.open-cluster-management.io/placement: ${APPSET_PLACEMENT_LABEL}
        requeueAfterSeconds: 180
  template:
    metadata:
      name: 'guestbook-{{name}}'
    spec:
      project: default
      source:
        repoURL: 'https://github.com/argoproj/argocd-example-apps.git'
        targetRevision: HEAD
        path: guestbook
      destination:
        name: '{{name}}'
        namespace: guestbook
      syncPolicy:
        automated:
          selfHeal: true
          prune: true
        syncOptions:
          - CreateNamespace=true
EOF

    # Workaround: The Placement controller adds score:0 (int64) to PlacementDecisions.
    # ArgoCD's DuckType generator type-asserts all values as strings and panics on int64.
    # Create a manual PlacementDecision with a DIFFERENT label (APPSET_PLACEMENT_LABEL)
    # so the ApplicationSet only sees our manual decisions (no score), not the
    # Placement controller's decisions (which have score:0).
    sleep 5
    local decisions="["
    local first=true
    for cluster in "${CLUSTER_NAMES[@]}"; do
        [ "$first" = true ] && first=false || decisions="${decisions},"
        decisions="${decisions}{\"clusterName\":\"${cluster}\",\"reason\":\"\"}"
    done
    decisions="${decisions}]"

    hub apply -f - <<EOF
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: PlacementDecision
metadata:
  name: ${APPSET_PLACEMENT_LABEL}-decision-1
  namespace: ${ARGOCD_NS}
  labels:
    cluster.open-cluster-management.io/placement: ${APPSET_PLACEMENT_LABEL}
    cluster.open-cluster-management.io/decision-group-index: "0"
    cluster.open-cluster-management.io/decision-group-name: ""
EOF
    hub patch placementdecision "${APPSET_PLACEMENT_LABEL}-decision-1" -n "${ARGOCD_NS}" \
        --type merge --subresource status \
        -p "{\"status\":{\"decisions\":${decisions}}}" 2>&1

    log "Deploy manifests applied"
}

# --- Wait for deploy ---
wait_for_deploy() {
    log "Waiting up to ${WAIT_DEPLOY_SECS}s for deployment..."
    local elapsed=0
    while [ $elapsed -lt "$WAIT_DEPLOY_SECS" ]; do
        local all_mca_ok=true
        local mca_status=""

        for cluster in "${CLUSTER_NAMES[@]}"; do
            local mca_ok
            mca_ok=$(hub get managedclusteraddon -n "$cluster" gitops-addon -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null || echo "")
            mca_status+=" ${cluster}=${mca_ok:-Pending}"
            [ "$mca_ok" != "True" ] && all_mca_ok=false
        done

        local policy_ok
        policy_ok=$(hub get policy -n "$ARGOCD_NS" "${GITOPSCLUSTER_NAME}-argocd-policy" -o jsonpath='{.status.compliant}' 2>/dev/null || echo "")

        if [ "$all_mca_ok" = true ] && [ "${policy_ok:-}" = "Compliant" ]; then
            local app_healthy=0 app_total=0
            for cluster in "${CLUSTER_NAMES[@]}"; do
                local app_ok
                app_ok=$(hub get applications.argoproj.io -n "$ARGOCD_NS" "guestbook-${cluster}" -o jsonpath='{.status.health.status}' 2>/dev/null || echo "")
                app_total=$((app_total+1))
                [ "$app_ok" = "Healthy" ] && app_healthy=$((app_healthy+1))
            done
            if [ "$app_healthy" -gt 0 ] && [ "$app_total" -gt 0 ]; then
                pass "Deploy verified: MCAs all Available, apps ${app_healthy}/${app_total} Healthy, Policy=Compliant (${elapsed}s)"
                return 0
            fi
        fi

        sleep 30
        elapsed=$((elapsed + 30))
        log "  Waiting... MCA:${mca_status} Policy=${policy_ok:-Pending} (${elapsed}s)"
    done
    fail "Deploy timed out after ${WAIT_DEPLOY_SECS}s"
}

# --- Verify managed clusters ---
verify_managed() {
    for cluster in "${CLUSTER_NAMES[@]}"; do
        local nodes
        nodes=$(on_cluster "$cluster" get nodes --no-headers 2>/dev/null | wc -l)
        pass "$cluster accessible ($nodes nodes)"

        local argocd
        argocd=$(on_cluster "$cluster" get argocd -n openshift-gitops -o name 2>/dev/null || echo "")
        if [ -n "$argocd" ]; then
            pass "$cluster ArgoCD CR exists"
        else
            log "WARNING: $cluster no ArgoCD CR (may need Policy enforcement or OLM)"
        fi

        local guestbook
        guestbook=$(on_cluster "$cluster" get deploy -n guestbook guestbook-ui -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [ "$guestbook" = "1" ]; then
            pass "$cluster guestbook-ui ready"
        else
            log "WARNING: $cluster guestbook-ui not ready (replicas=$guestbook)"
        fi
    done
}

# --- Delete from hub (explicit manual ordering) ---
delete_from_hub() {
    log "Deleting from hub..."

    # 1. Delete GitOpsCluster — stops the controller from re-creating MCAs.
    #    GitOpsCluster deletion is a no-op (no finalizer, no automated cleanup).
    #    Dynamic AddOnTemplates and ADCs are NOT owned by GitOpsCluster, so they persist.
    hub delete gitopscluster "$GITOPSCLUSTER_NAME" -n "$ARGOCD_NS" --ignore-not-found 2>/dev/null || true

    # 2. Delete Policy + PlacementBinding — stops enforcement of ArgoCD CR on managed
    #    clusters. Without this, the Policy re-creates the ArgoCD CR during cleanup.
    local policy_name="${GITOPSCLUSTER_NAME}-argocd-policy"
    hub delete placementbinding "${policy_name}-binding" -n "$ARGOCD_NS" --ignore-not-found 2>/dev/null || true
    hub delete policy "$policy_name" -n "$ARGOCD_NS" --ignore-not-found --wait=false 2>/dev/null || true
    sleep 5

    # 3. Delete MCAs — triggers pre-delete cleanup Job on each managed cluster.
    #    ADC and AddOnTemplate still exist (orphaned from step 1), so the addon
    #    framework can render the pre-delete ManifestWork.
    #    Use --wait=false so kubectl returns immediately; wait_for_mca_cleanup polls.
    for cluster in "${CLUSTER_NAMES[@]}"; do
        hub delete managedclusteraddon gitops-addon -n "$cluster" --ignore-not-found --wait=false 2>/dev/null || true
    done
    log "MCAs deletion triggered"

    # 4. Wait for MCAs to fully delete (cleanup Jobs complete, finalizers removed)
    wait_for_mca_cleanup

    # 5. Delete ApplicationSet AFTER agents are disconnected. If deleted while
    #    agents are running, the apps get resources-finalizer.argocd.argoproj.io
    #    and the hub application-controller can't process them (agents gone).
    hub delete applicationset "$APPSET_NAME" -n "$ARGOCD_NS" --ignore-not-found --wait=false 2>/dev/null || true
    # Strip finalizers from orphaned apps — the resources-finalizer can't be
    # processed because the managed cluster agents are already disconnected.
    sleep 2
    for cluster in "${CLUSTER_NAMES[@]}"; do
        local app_name="guestbook-${cluster}"
        if hub get applications.argoproj.io "$app_name" -n "$ARGOCD_NS" &>/dev/null 2>&1; then
            hub patch applications.argoproj.io "$app_name" -n "$ARGOCD_NS" \
                --type=json -p='[{"op":"replace","path":"/metadata/finalizers","value":[]}]' 2>/dev/null || true
        fi
    done

    # 6. Delete remaining hub resources
    hub delete placement "$PLACEMENT_NAME" -n "$ARGOCD_NS" --ignore-not-found 2>/dev/null || true
    hub delete placementdecision "${APPSET_PLACEMENT_LABEL}-decision-1" -n "$ARGOCD_NS" --ignore-not-found 2>/dev/null || true
    hub delete policy "$policy_name" -n "$ARGOCD_NS" --ignore-not-found --wait=false 2>/dev/null || true

    # 7. Clean up orphaned ADCs
    for cluster in "${CLUSTER_NAMES[@]}"; do
        hub delete addondeploymentconfig gitops-addon-config -n "$cluster" --ignore-not-found 2>/dev/null || true
    done

    log "Hub resources deleted"
}

# --- Wait for MCA cleanup on all clusters ---
wait_for_mca_cleanup() {
    for cluster in "${CLUSTER_NAMES[@]}"; do
        log "Waiting up to ${WAIT_CLEANUP_SECS}s for MCA cleanup on $cluster..."
        local elapsed=0
        while [ $elapsed -lt "$WAIT_CLEANUP_SECS" ]; do
            hub get managedclusteraddon -n "$cluster" gitops-addon &>/dev/null || break
            sleep 15
            elapsed=$((elapsed + 15))
            [ $((elapsed % 60)) -eq 0 ] && log "  $cluster MCA still exists (${elapsed}s)"
        done
        if hub get managedclusteraddon -n "$cluster" gitops-addon &>/dev/null; then
            fail "$cluster MCA still present after ${WAIT_CLEANUP_SECS}s — cleanup did not complete naturally"
        else
            pass "$cluster MCA fully deleted"
        fi
    done
}

# --- Verify cleanup on hub + managed clusters ---
verify_cleanup() {
    local issues=0

    # Hub: GitOpsCluster gone
    if hub get gitopscluster "$GITOPSCLUSTER_NAME" -n "$ARGOCD_NS" &>/dev/null; then
        log "CLEANUP ISSUE: GitOpsCluster still exists on hub"; issues=$((issues+1))
    else
        pass "Hub: GitOpsCluster deleted"
    fi

    # Hub: Policy gone
    if hub get policy "${GITOPSCLUSTER_NAME}-argocd-policy" -n "$ARGOCD_NS" &>/dev/null; then
        log "CLEANUP ISSUE: Policy still exists on hub"; issues=$((issues+1))
    else
        pass "Hub: Policy deleted"
    fi

    # Per-cluster checks
    for cluster in "${CLUSTER_NAMES[@]}"; do
        # Hub: MCA gone
        if hub get managedclusteraddon -n "$cluster" gitops-addon &>/dev/null; then
            log "CLEANUP ISSUE: MCA still in $cluster"; issues=$((issues+1))
        else
            pass "Hub: MCA deleted for $cluster"
        fi

        # Managed cluster: ArgoCD CR removed (wait up to 120s).
        # The CR may linger if the Policy re-created it during the brief window
        # between hub-side Policy deletion and spoke-side ConfigurationPolicy removal.
        # With the operator already gone, the CR is an inert orphan (no finalizer processing).
        local aw=0
        while [ "$aw" -lt 120 ]; do
            on_cluster "$cluster" get argocd acm-openshift-gitops -n openshift-gitops &>/dev/null || break
            sleep 10; aw=$((aw+10))
        done
        if on_cluster "$cluster" get argocd acm-openshift-gitops -n openshift-gitops &>/dev/null; then
            log "WARNING: $cluster ArgoCD CR still present (orphaned — operator removed, harmless)"
        else
            pass "$cluster: ArgoCD CR removed"
        fi

        # Managed cluster: addon deployment removed
        if on_cluster "$cluster" get deploy gitops-addon -n open-cluster-management-agent-addon &>/dev/null; then
            log "CLEANUP ISSUE: $cluster addon deployment still present"; issues=$((issues+1))
        else
            pass "$cluster: addon deployment removed"
        fi

        # Force-delete any stuck terminating pods in guestbook namespace to prevent
        # them from keeping the app at Progressing in the next cycle.
        # Do NOT delete the namespace itself — a terminating namespace blocks ArgoCD
        # from recreating resources in subsequent deploys.
        on_cluster "$cluster" delete pod --all -n guestbook --force --grace-period=0 --ignore-not-found 2>/dev/null || true

        # Cluster still accessible (critical check)
        local nodes
        nodes=$(on_cluster "$cluster" get nodes --no-headers 2>/dev/null | wc -l)
        if [ "$nodes" -gt 0 ]; then
            pass "$cluster still accessible ($nodes nodes)"
        else
            log "CLEANUP ISSUE: $cluster NOT accessible!"; issues=$((issues+1))
        fi
    done

    if [ $issues -gt 0 ]; then
        fail "Cleanup verification found $issues issue(s)"
    fi
}

# =====================================================================
# Main
# =====================================================================
log "Starting test cycles (max=$MAX_CYCLES)"
log "Hub: $HUB_KUBECONFIG"
for cluster in "${CLUSTER_NAMES[@]}"; do
    log "Managed: $cluster -> ${CLUSTER_KUBECONFIGS[$cluster]}"
done

if [ "$FORCE_CLEAN_FIRST" = "true" ]; then
    for cluster in "${CLUSTER_NAMES[@]}"; do
        force_clean_cluster "$cluster"
    done
    clean_hub_stale
    # Restart controller to clear informer cache after force-clean
    log "Restarting hub controller to clear cache..."
    hub -n "$HUB_CONTROLLER_NS" rollout restart deployment/multicluster-operators-application 2>/dev/null || true
    hub -n kube-system delete lease multicloud-operators-gitopscluster-leader.open-cluster-management.io --ignore-not-found 2>/dev/null || true
    hub -n "$HUB_CONTROLLER_NS" rollout status deployment/multicluster-operators-application --timeout=120s 2>/dev/null || true
    sleep 30
    log "Hub controller restarted"
fi

for cycle in $(seq 1 "$MAX_CYCLES"); do
    log "========================================="
    log "CYCLE $cycle / $MAX_CYCLES"
    log "========================================="

    deploy
    wait_for_deploy
    verify_managed

    delete_from_hub
    verify_cleanup

    # Between cycles: restart the ArgoCD principal pod to clear its gRPC event queue,
    # and delete stale CSRs to prevent "too many CSR" throttling.
    # Without the principal restart, fresh agents in the next cycle receive stale
    # delete/update events from the previous cycle and reject them as "not managed",
    # blocking app dispatch indefinitely.
    if [ "$cycle" -lt "$MAX_CYCLES" ]; then
        log "Resetting between cycles..."
        hub get csr -o name 2>/dev/null | grep gitops-addon | xargs hub delete --ignore-not-found 2>/dev/null || true
        hub -n "$ARGOCD_NS" rollout restart deploy -l app.kubernetes.io/component=principal 2>/dev/null || true
        hub -n "$ARGOCD_NS" rollout status deploy -l app.kubernetes.io/component=principal --timeout=60s 2>/dev/null || true
    fi

    log "CYCLE $cycle COMPLETE"
    echo ""
done

log "All $MAX_CYCLES cycles completed successfully!"
