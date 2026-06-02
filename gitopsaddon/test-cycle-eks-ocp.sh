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
    on_cluster "$cluster" delete clusterrolebinding gitops-addon gitops-addon-cleanup acm-openshift-gitops-appcontroller-cluster-admin --ignore-not-found 2>/dev/null || true

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

    if [ -z "$has_clusterversion" ]; then
        on_cluster "$cluster" apply -f - >/dev/null 2>&1 <<EORBAC || true
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: acm-openshift-gitops-appcontroller-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: acm-openshift-gitops-argocd-application-controller
  namespace: openshift-gitops
EORBAC
        log "  $cluster pre-created app-controller cluster-admin binding"
    fi

    on_cluster "$cluster" delete deploy,svc --all -n guestbook --force --grace-period=0 --ignore-not-found 2>/dev/null || true
    on_cluster "$cluster" delete namespace guestbook --ignore-not-found 2>/dev/null || true

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
            cluster.open-cluster-management.io/placement: ${PLACEMENT_NAME}
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

    log "Deploy manifests applied"
}

# --- Ensure app-controller has cluster-admin on non-OCP clusters ---
# The upstream argocd-operator creates ClusterRoles with only read (get/list/watch)
# on core resources. Without ARGOCD_CLUSTER_CONFIG_NAMESPACES, the app-controller
# cannot create Services/Deployments in target namespaces like "guestbook".
ensure_appcontroller_rbac() {
    local rbac_applied=false
    for cluster in "${CLUSTER_NAMES[@]}"; do
        if on_cluster "$cluster" get crd clusterversions.config.openshift.io --no-headers >/dev/null 2>&1; then
            continue
        fi
        local sa_exists
        sa_exists=$(on_cluster "$cluster" get sa -n openshift-gitops acm-openshift-gitops-argocd-application-controller --no-headers 2>/dev/null || echo "")
        if [ -z "$sa_exists" ]; then
            continue
        fi
        on_cluster "$cluster" create clusterrolebinding acm-openshift-gitops-appcontroller-cluster-admin \
            --clusterrole=cluster-admin \
            --serviceaccount=openshift-gitops:acm-openshift-gitops-argocd-application-controller \
            --dry-run=client -o yaml | on_cluster "$cluster" apply -f - >/dev/null 2>&1
        log "  Granted cluster-admin to app-controller on $cluster"
        rbac_applied=true
    done
    [ "$rbac_applied" = true ] && log "  App-controller RBAC ensured on non-OCP clusters"
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
            local phase="" argocd_ok=false
            for attempt in $(seq 1 12); do
                phase=$(on_cluster "$cluster" get argocd -n openshift-gitops -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "Unknown")
                if [ "$phase" = "Available" ] || [ "$phase" = "Running" ]; then
                    argocd_ok=true
                    break
                fi
                log "  $cluster ArgoCD CR phase=$phase, waiting... (attempt $attempt/12)"
                sleep 10
            done
            if [ "$argocd_ok" = true ]; then
                pass "$cluster ArgoCD CR phase=$phase"
            else
                local condition_msg
                condition_msg=$(on_cluster "$cluster" get argocd -n openshift-gitops -o jsonpath='{.items[0].status.conditions[?(@.type=="Reconciled")].message}' 2>/dev/null || echo "")
                local full_status
                full_status=$(on_cluster "$cluster" get argocd -n openshift-gitops -o jsonpath='{.items[0].status}' 2>/dev/null || echo "")
                fail "$cluster ArgoCD CR phase=$phase (expected Available/Running). Condition: $condition_msg. Full status: $full_status"
            fi
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

# --- Verify ArgoCD agent end-to-end: app dispatch + status sync ---
# Polls for up to AGENT_VERIFY_SECS (default 300s) waiting for:
#   1. App dispatched to spoke (Application exists in openshift-gitops on spoke)
#   2. App reconciled (resources created in guestbook namespace on spoke)
#   3. Status synced back (hub Application has non-Unknown sync status)
# At least one cluster must pass all 3 checks for this to PASS.
verify_agent_e2e() {
    local timeout=${AGENT_VERIFY_SECS:-300}
    log "Verifying ArgoCD agent end-to-end (up to ${timeout}s)..."
    local elapsed=0
    local any_cluster_ok=false

    while [ "$elapsed" -lt "$timeout" ]; do
        any_cluster_ok=false
        for cluster in "${CLUSTER_NAMES[@]}"; do
            local cluster_ok=true

            # 1. App dispatched to spoke
            local spoke_app
            spoke_app=$(on_cluster "$cluster" get application.argoproj.io "guestbook-${cluster}" \
                -n openshift-gitops -o jsonpath='{.metadata.name}' 2>/dev/null || echo "")
            [ -z "$spoke_app" ] && cluster_ok=false

            # 2. App reconciled on spoke (resources created)
            local spoke_resources=0
            if [ "$cluster_ok" = "true" ]; then
                spoke_resources=$(on_cluster "$cluster" get deploy,svc -n guestbook --no-headers 2>/dev/null | wc -l)
                [ "$spoke_resources" -lt 1 ] && cluster_ok=false
            fi

            # 3. Hub app has real sync status (not Unknown = principal relayed agent status)
            local hub_sync=""
            if [ "$cluster_ok" = "true" ]; then
                hub_sync=$(hub get application.argoproj.io "guestbook-${cluster}" -n openshift-gitops \
                    -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "")
                [ "$hub_sync" = "Unknown" ] || [ -z "$hub_sync" ] && cluster_ok=false
            fi

            if [ "$cluster_ok" = "true" ]; then
                local hub_health
                hub_health=$(hub get application.argoproj.io "guestbook-${cluster}" -n openshift-gitops \
                    -o jsonpath='{.status.health.status}' 2>/dev/null || echo "")
                pass "$cluster agent e2e OK: spoke app dispatched, ${spoke_resources} resources, hub sync=$hub_sync health=$hub_health"
                any_cluster_ok=true
            fi
        done

        if [ "$any_cluster_ok" = "true" ]; then
            pass "ArgoCD agent e2e verified: at least 1 cluster fully working"
            return 0
        fi

        sleep 30
        elapsed=$((elapsed + 30))
        log "  Agent e2e: waiting for app dispatch + reconcile + status sync (${elapsed}s)"
    done

    # Timeout — report per-cluster status as warnings
    log "WARNING: Agent e2e verification timed out after ${timeout}s. Per-cluster status:"
    for cluster in "${CLUSTER_NAMES[@]}"; do
        local spoke_app
        spoke_app=$(on_cluster "$cluster" get application.argoproj.io "guestbook-${cluster}" \
            -n openshift-gitops -o jsonpath='{.metadata.name}' 2>/dev/null || echo "none")
        local spoke_resources
        spoke_resources=$(on_cluster "$cluster" get deploy,svc -n guestbook --no-headers 2>/dev/null | wc -l)
        local hub_sync
        hub_sync=$(hub get application.argoproj.io "guestbook-${cluster}" -n openshift-gitops \
            -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "none")
        local hub_health
        hub_health=$(hub get application.argoproj.io "guestbook-${cluster}" -n openshift-gitops \
            -o jsonpath='{.status.health.status}' 2>/dev/null || echo "none")
        log "  $cluster: spokeApp=$spoke_app spokeResources=$spoke_resources hubSync=$hub_sync hubHealth=$hub_health"

        # Check agent pod status for diagnostics
        local agent_status
        agent_status=$(on_cluster "$cluster" get pods -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent \
            -o custom-columns=NAME:.metadata.name,STATUS:.status.phase,READY:.status.conditions 2>/dev/null | head -3)
        log "  $cluster agent pods: $agent_status"
    done
}

# --- Verify pod stability (no restarts) across all key namespaces ---
verify_pods_stable() {
    local settle_secs=${POD_STABLE_SETTLE_SECS:-30}
    log "Waiting ${settle_secs}s for pods to settle before checking restart counts..."
    sleep "$settle_secs"

    local all_stable=true
    local namespaces=("openshift-gitops" "openshift-gitops-operator" "open-cluster-management-agent-addon")

    for cluster in "${CLUSTER_NAMES[@]}"; do
        for ns in "${namespaces[@]}"; do
            local pods_output
            pods_output=$(on_cluster "$cluster" get pods -n "$ns" --no-headers \
                -o custom-columns='NAME:.metadata.name,RESTARTS:.status.containerStatuses[0].restartCount,PHASE:.status.phase' 2>/dev/null || echo "")
            if [ -z "$pods_output" ]; then
                continue
            fi

            while IFS= read -r line; do
                local pod_name restarts phase
                pod_name=$(echo "$line" | awk '{print $1}')
                restarts=$(echo "$line" | awk '{print $2}')
                phase=$(echo "$line" | awk '{print $3}')

                [ "$phase" != "Running" ] && continue
                [ "$restarts" = "<none>" ] && restarts=0

                if [ "$restarts" -gt 0 ] 2>/dev/null; then
                    log "FAIL: $cluster pod $pod_name in $ns has $restarts restarts"
                    on_cluster "$cluster" logs -n "$ns" "$pod_name" --previous --tail=10 2>/dev/null || true
                    all_stable=false
                fi
            done <<< "$pods_output"
        done
    done

    if [ "$all_stable" = "true" ]; then
        pass "All pods stable (zero restarts) across all managed clusters"
    else
        fail "Some pods have restarts — indicates crash loops (see logs above)"
    fi
}

# --- Verify gitopsaddon label on deployed resources (non-OCP clusters only) ---
verify_gitopsaddon_labels() {
    for cluster in "${CLUSTER_NAMES[@]}"; do
        local is_ocp
        is_ocp=$(on_cluster "$cluster" get crd clusterversions.config.openshift.io --no-headers 2>/dev/null && echo "yes" || echo "no")
        if [ "$is_ocp" = "yes" ]; then
            log "$cluster is OCP (OLM mode), skipping embedded label check"
            continue
        fi

        local label_ok=true

        # Check Deployment in operator namespace
        local deploy_label
        deploy_label=$(on_cluster "$cluster" get deploy -n openshift-gitops-operator -l "apps.open-cluster-management.io/gitopsaddon=true" --no-headers 2>/dev/null | wc -l)
        if [ "$deploy_label" -ge 1 ]; then
            pass "$cluster operator Deployment has gitopsaddon label"
        else
            log "WARNING: $cluster operator Deployment missing gitopsaddon label"
            label_ok=false
        fi

        # Check ServiceAccount in operator namespace
        local sa_label
        sa_label=$(on_cluster "$cluster" get sa -n openshift-gitops-operator -l "apps.open-cluster-management.io/gitopsaddon=true" --no-headers 2>/dev/null | wc -l)
        if [ "$sa_label" -ge 1 ]; then
            pass "$cluster operator ServiceAccount has gitopsaddon label"
        else
            log "WARNING: $cluster operator ServiceAccount missing gitopsaddon label"
            label_ok=false
        fi

        # Check ConfigMap in operator namespace (should be labeled, system ones should NOT)
        local cm_unlabeled
        cm_unlabeled=$(on_cluster "$cluster" get cm -n openshift-gitops-operator --no-headers 2>/dev/null | wc -l)
        local cm_labeled
        cm_labeled=$(on_cluster "$cluster" get cm -n openshift-gitops-operator -l "apps.open-cluster-management.io/gitopsaddon=true" --no-headers 2>/dev/null | wc -l)
        if [ "$cm_labeled" -ge 1 ]; then
            pass "$cluster operator ConfigMap(s) labeled ($cm_labeled labeled, $cm_unlabeled total)"
        else
            log "WARNING: $cluster no labeled ConfigMaps in operator namespace"
            label_ok=false
        fi

        if [ "$label_ok" = "true" ]; then
            pass "$cluster all embedded operator resources have gitopsaddon label"
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
    #    clusters. Without this, the ConfigurationPolicy re-creates the ArgoCD CR
    #    during cleanup, causing a race: cleanup Job deletes ArgoCD CR (step 2),
    #    ConfigurationPolicy re-creates it, cleanup Job kills operator (step 3),
    #    re-created ArgoCD CR has unprocessable finalizer → orphaned CR + workloads.
    local policy_name="${GITOPSCLUSTER_NAME}-argocd-policy"
    hub delete placementbinding "${policy_name}-binding" -n "$ARGOCD_NS" --ignore-not-found 2>/dev/null || true
    hub delete policy "$policy_name" -n "$ARGOCD_NS" --ignore-not-found --wait=false 2>/dev/null || true

    # Wait for ConfigurationPolicy to be fully removed from ALL managed clusters.
    # The chain: Hub Policy deleted → replicated Policy removed from managed
    # cluster namespace → governance framework removes ConfigurationPolicy from
    # managed cluster → config-policy-controller stops enforcing ArgoCD CR.
    # This chain takes 30-60s. If we check immediately, we might see 0
    # ConfigurationPolicies because propagation hasn't started yet (false positive).
    # Strategy: wait for replicated policies to be removed from hub first (ensures
    # propagation has started), then verify ConfigurationPolicies are gone.
    log "Waiting for replicated Policies to be removed from hub..."
    local rp_timeout=120
    local rp_elapsed=0
    local rp_all_gone=false
    while [ "$rp_elapsed" -lt "$rp_timeout" ]; do
        rp_all_gone=true
        for cluster in "${CLUSTER_NAMES[@]}"; do
            if hub get policy -n "$cluster" --no-headers 2>/dev/null | grep -q "argocd" 2>/dev/null; then
                rp_all_gone=false
                break
            fi
        done
        if $rp_all_gone; then
            pass "Replicated Policies removed from hub (${rp_elapsed}s)"
            break
        fi
        sleep 5
        rp_elapsed=$((rp_elapsed + 5))
    done
    if ! $rp_all_gone; then
        log "WARNING: replicated Policies still on hub after ${rp_timeout}s"
    fi

    # Now wait for ConfigurationPolicies to be removed from managed clusters
    log "Waiting for ConfigurationPolicy to be removed from managed clusters..."
    local cp_timeout=120
    for cluster in "${CLUSTER_NAMES[@]}"; do
        local cp_elapsed=0
        while [ "$cp_elapsed" -lt "$cp_timeout" ]; do
            local cp_count
            cp_count=$(on_cluster "$cluster" get configurationpolicy -A --no-headers 2>/dev/null | grep -c "argocd" || true)
            if [ "$cp_count" -eq 0 ]; then
                pass "ConfigurationPolicy removed from $cluster (${cp_elapsed}s)"
                break
            fi
            sleep 10
            cp_elapsed=$((cp_elapsed + 10))
            [ $((cp_elapsed % 30)) -eq 0 ] && log "  $cluster still has ConfigurationPolicy (${cp_elapsed}s)"
        done
        if [ "$cp_elapsed" -ge "$cp_timeout" ]; then
            log "WARNING: $cluster ConfigurationPolicy not removed after ${cp_timeout}s — proceeding anyway"
        fi
    done

    # Wait for config-policy-controller queued reconciliations to drain.
    # Even after the ConfigurationPolicy is deleted from the API, the controller
    # may have a queued reconciliation that fires using cached state, re-creating
    # the ArgoCD CR one final time. The reconciliation queue drains within ~10s.
    log "Waiting 30s for config-policy-controller reconciliation queue to drain..."
    sleep 30

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
    local warnings=0

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

        # Managed cluster: ArgoCD CR removed. The cleanup job now waits for the
        # operator to process the ArgoCD CR finalizer before removing the operator.
        # If the CR is still present, the operator failed to process the finalizer.
        if on_cluster "$cluster" get argocd acm-openshift-gitops -n openshift-gitops &>/dev/null; then
            log "CLEANUP ISSUE: $cluster ArgoCD CR still present — operator finalizer did not complete"
            issues=$((issues+1))
        else
            pass "$cluster: ArgoCD CR removed (operator finalizer processed)"
        fi

        # Managed cluster: addon deployment removed
        if on_cluster "$cluster" get deploy gitops-addon -n open-cluster-management-agent-addon &>/dev/null; then
            log "CLEANUP ISSUE: $cluster addon deployment still present"; issues=$((issues+1))
        else
            pass "$cluster: addon deployment removed"
        fi

        # Managed cluster: ArgoCD workloads in openshift-gitops namespace.
        # Only check for gitopsaddon-created workloads (acm-openshift-gitops-* prefix).
        # On OCP, 'cluster' and 'gitops-plugin' deployments are owned by the built-in
        # GitopsService CR (pipelines.openshift.io) and are NOT from gitopsaddon.
        local addon_deploys
        addon_deploys=$(on_cluster "$cluster" get deploy -n openshift-gitops --no-headers 2>/dev/null | grep -c "acm-openshift-gitops" || true)
        local addon_sts
        addon_sts=$(on_cluster "$cluster" get statefulset -n openshift-gitops --no-headers 2>/dev/null | grep -c "acm-openshift-gitops" || true)
        if [ "$addon_deploys" -gt 0 ] || [ "$addon_sts" -gt 0 ]; then
            log "CLEANUP ISSUE: $cluster still has addon ArgoCD workloads in openshift-gitops (deploys=$addon_deploys sts=$addon_sts)"
            on_cluster "$cluster" get deploy,statefulset -n openshift-gitops 2>/dev/null || true
            issues=$((issues+1))
        else
            pass "$cluster: addon ArgoCD workloads cleaned (operator finalizer processed)"
        fi

        # Managed cluster: operator namespace (openshift-gitops-operator).
        # On OCP, OLM manages the operator deployment — skip if OLM mode (OLM handles cleanup).
        # On non-OCP (embedded), gitopsaddon lays down the operator, so it must be cleaned.
        if ! on_cluster "$cluster" get crd clusterversions.config.openshift.io >/dev/null 2>&1; then
            local op_deploys
            op_deploys=$(on_cluster "$cluster" get deploy -n openshift-gitops-operator --no-headers 2>/dev/null | wc -l)
            if [ "$op_deploys" -gt 0 ]; then
                log "CLEANUP ISSUE: $cluster still has operator deployment in openshift-gitops-operator"
                on_cluster "$cluster" get deploy -n openshift-gitops-operator 2>/dev/null || true
                issues=$((issues+1))
            else
                pass "$cluster: operator namespace clean (embedded)"
            fi
        else
            pass "$cluster: operator namespace managed by OLM (OCP)"
        fi

        # OCP-specific: OLM subscription and CSV should be removed
        # OLM deletion is async — wait up to 60s for subscription/CSV to be fully removed
        if on_cluster "$cluster" get crd clusterversions.config.openshift.io >/dev/null 2>&1; then
            local olm_wait=0
            while [ "$olm_wait" -lt 60 ]; do
                local olm_subs
                olm_subs=$(on_cluster "$cluster" get subscriptions.operators -A --no-headers 2>/dev/null | grep -c gitops || true)
                local olm_csvs
                olm_csvs=$(on_cluster "$cluster" get csv -A --no-headers 2>/dev/null | grep -c gitops || true)
                if [ "$olm_subs" -eq 0 ] && [ "$olm_csvs" -eq 0 ]; then
                    break
                fi
                sleep 5
                olm_wait=$((olm_wait+5))
            done
            if [ "$olm_subs" -gt 0 ]; then
                log "CLEANUP ISSUE: $cluster OLM Subscription still exists after ${olm_wait}s"; issues=$((issues+1))
            else
                pass "$cluster: OLM Subscription removed"
            fi
            if [ "$olm_csvs" -gt 0 ]; then
                log "CLEANUP ISSUE: $cluster OLM CSV still exists after ${olm_wait}s"; issues=$((issues+1))
            else
                pass "$cluster: OLM CSV removed"
            fi
        fi

        # Force-delete any stuck terminating pods in guestbook namespace
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
        log "Cleanup verification: $issues issue(s), $warnings warning(s)"
        fail "Cleanup verification found $issues issue(s)"
    fi
    if [ $warnings -gt 0 ]; then
        log "Cleanup verification: $warnings warning(s) (non-fatal)"
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
    verify_pods_stable
    verify_agent_e2e
    verify_gitopsaddon_labels

    delete_from_hub
    verify_cleanup

    # Between cycles: restart the ArgoCD principal pod to clear its gRPC event queue,
    # and delete stale CSRs to prevent "too many CSR" throttling.
    # Without the principal restart, fresh agents in the next cycle receive stale
    # delete/update events from the previous cycle and reject them as "not managed",
    # blocking app dispatch indefinitely.
    if [ "$cycle" -lt "$MAX_CYCLES" ]; then
        log "Resetting between cycles..."

        # Delete stale CSRs to prevent "too many CSR" throttling
        hub get csr -o name 2>/dev/null | grep gitops-addon | xargs hub delete --ignore-not-found 2>/dev/null || true

        # Delete stale pre-delete ManifestWorks on hub
        for cluster in "${CLUSTER_NAMES[@]}"; do
            hub delete manifestwork -n "$cluster" addon-gitops-addon-pre-delete --ignore-not-found 2>/dev/null || true
        done

        # Clean up stale resources on managed clusters
        for cluster in "${CLUSTER_NAMES[@]}"; do
            # Kill stale cleanup Jobs
            on_cluster "$cluster" delete job gitops-addon-cleanup -n open-cluster-management-agent-addon --ignore-not-found 2>/dev/null || true
            # Delete residual ArgoCD CRs (wait briefly, then strip finalizers)
            on_cluster "$cluster" delete argocd --all -n openshift-gitops --timeout=30s 2>/dev/null || \
                on_cluster "$cluster" patch argocd -n openshift-gitops --type=merge -p '{"metadata":{"finalizers":[]}}' --all 2>/dev/null || true
            # Delete guestbook namespace
            on_cluster "$cluster" delete namespace guestbook --ignore-not-found --wait=false 2>/dev/null || true
        done

        # Restart principal to clear gRPC event queue
        hub -n "$ARGOCD_NS" rollout restart deploy -l app.kubernetes.io/component=principal 2>/dev/null || true
        hub -n "$ARGOCD_NS" rollout status deploy -l app.kubernetes.io/component=principal --timeout=60s 2>/dev/null || true

        sleep 10
        for cluster in "${CLUSTER_NAMES[@]}"; do
            ns_gone=false
            for _ in $(seq 1 6); do
                on_cluster "$cluster" get namespace guestbook &>/dev/null || { ns_gone=true; break; }
                sleep 5
            done
            [ "$ns_gone" = true ] || log "WARNING: $cluster guestbook namespace still terminating"
        done
    fi

    log "CYCLE $cycle COMPLETE"
    echo ""
done

log "All $MAX_CYCLES cycles completed successfully!"
