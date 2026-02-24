#!/bin/bash
###############################################################################
# Copyright Contributors to the Open Cluster Management project
#
# E2E test: Cluster secret deletion protection
# Verifies that ArgoCD cluster secrets are only deleted when ManagedCluster
# is fully removed, NOT when:
#   - Cluster is removed from PlacementDecision
#   - ManagedCluster has a deletion timestamp but is not yet fully deleted
###############################################################################

set -o nounset
set -o pipefail

KUBECTL=${KUBECTL:-kubectl}
ARGOCD_NS=${ARGOCD_NS:-argocd}
CLUSTER_NAME="cluster1"
SECRET_NAME="cluster1-application-manager-cluster-secret"
GITOPSCLUSTER_NAME="argo-ocm-importer"
TIMEOUT_SECONDS=180

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; exit 1; }
info() { echo "  INFO: $1"; }

wait_for_secret() {
    local name=$1
    local ns=$2
    local should_exist=$3
    local wait=$4
    local elapsed=0

    while [ $elapsed -lt $wait ]; do
        if $KUBECTL -n "$ns" get secret "$name" > /dev/null 2>&1; then
            if [ "$should_exist" = "true" ]; then
                return 0
            fi
        else
            if [ "$should_exist" = "false" ]; then
                return 0
            fi
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
    return 1
}

echo "=== E2E: Cluster Secret Deletion Protection ==="
$KUBECTL config use-context kind-hub

# Prerequisite: verify the GitOpsCluster and cluster secret are set up
echo ""
echo "--- Prerequisite checks ---"
if ! $KUBECTL -n $ARGOCD_NS get gitopscluster $GITOPSCLUSTER_NAME > /dev/null 2>&1; then
    fail "GitOpsCluster $GITOPSCLUSTER_NAME not found in $ARGOCD_NS"
fi
info "GitOpsCluster $GITOPSCLUSTER_NAME exists"

if ! $KUBECTL get managedcluster $CLUSTER_NAME > /dev/null 2>&1; then
    fail "ManagedCluster $CLUSTER_NAME not found"
fi
info "ManagedCluster $CLUSTER_NAME exists"

if ! wait_for_secret "$SECRET_NAME" "$ARGOCD_NS" "true" $TIMEOUT_SECONDS; then
    fail "cluster secret $SECRET_NAME not found in $ARGOCD_NS before test start"
fi
pass "cluster secret $SECRET_NAME exists before tests"

echo ""
echo "--- Controller warmup: verify active reconciliation ---"
# Delete the secret and verify the controller recreates it (proves controller is active)
$KUBECTL -n "$ARGOCD_NS" delete secret "$SECRET_NAME" --ignore-not-found 2>/dev/null
info "Deleted secret to verify controller recreates it"
if wait_for_secret "$SECRET_NAME" "$ARGOCD_NS" "true" $TIMEOUT_SECONDS; then
    pass "Controller is actively reconciling (secret was recreated)"
else
    fail "Controller is not actively reconciling (secret was not recreated within ${TIMEOUT_SECONDS}s)"
fi

# --------------------------------------------------------------------------
# TEST 1: Remove cluster from PlacementDecision - secret must NOT be deleted
# --------------------------------------------------------------------------
echo ""
echo "--- Test 1: PlacementDecision removal must NOT delete cluster secret ---"

PLACEMENT_NAME=$($KUBECTL -n $ARGOCD_NS get gitopscluster $GITOPSCLUSTER_NAME -o jsonpath='{.spec.placementRef.name}')
info "Placement name: $PLACEMENT_NAME"

PD_NAME=$($KUBECTL -n $ARGOCD_NS get placementdecision \
    -l "cluster.open-cluster-management.io/placement=$PLACEMENT_NAME" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

if [ -z "$PD_NAME" ]; then
    fail "No PlacementDecision found for placement $PLACEMENT_NAME"
fi
info "PlacementDecision: $PD_NAME"

# Save current decisions
ORIGINAL_DECISIONS=$($KUBECTL -n $ARGOCD_NS get placementdecision "$PD_NAME" \
    -o jsonpath='{.status.decisions}')
info "Original decisions: $ORIGINAL_DECISIONS"

# Clear placement decisions (simulate cluster removal from placement)
$KUBECTL -n $ARGOCD_NS patch placementdecision "$PD_NAME" \
    --type merge --subresource status \
    -p '{"status":{"decisions":[]}}' 2>/dev/null

info "Cleared PlacementDecision, waiting for reconcile..."
sleep 30

if $KUBECTL -n $ARGOCD_NS get secret "$SECRET_NAME" > /dev/null 2>&1; then
    pass "Secret preserved after cluster removed from PlacementDecision (ManagedCluster still exists)"
else
    # Restore decisions before failing
    $KUBECTL -n $ARGOCD_NS patch placementdecision "$PD_NAME" \
        --type merge --subresource status \
        -p "{\"status\":{\"decisions\":[{\"clusterName\":\"$CLUSTER_NAME\",\"reason\":\"\"}]}}" 2>/dev/null
    fail "Secret was DELETED when cluster removed from PlacementDecision but ManagedCluster still exists"
fi

# Restore placement decisions
$KUBECTL -n $ARGOCD_NS patch placementdecision "$PD_NAME" \
    --type merge --subresource status \
    -p "{\"status\":{\"decisions\":[{\"clusterName\":\"$CLUSTER_NAME\",\"reason\":\"\"}]}}" 2>/dev/null
info "Restored PlacementDecision"
sleep 10

# --------------------------------------------------------------------------
# TEST 2: ManagedCluster with deletion timestamp (finalizer present) -
#          secret must NOT be deleted
# --------------------------------------------------------------------------
echo ""
echo "--- Test 2: ManagedCluster with deletion timestamp must NOT trigger secret deletion ---"

# Add a finalizer so ManagedCluster won't be fully deleted
$KUBECTL patch managedcluster "$CLUSTER_NAME" \
    --type=json -p='[{"op":"add","path":"/metadata/finalizers","value":["e2e-test/block-delete"]}]' 2>/dev/null
info "Added finalizer to ManagedCluster"

# Request deletion (will be blocked by finalizer)
$KUBECTL delete managedcluster "$CLUSTER_NAME" --wait=false 2>/dev/null
info "Requested ManagedCluster deletion (blocked by finalizer)"

sleep 30

# Verify ManagedCluster still exists with deletion timestamp
if $KUBECTL get managedcluster "$CLUSTER_NAME" > /dev/null 2>&1; then
    DELETION_TS=$($KUBECTL get managedcluster "$CLUSTER_NAME" -o jsonpath='{.metadata.deletionTimestamp}')
    if [ -n "$DELETION_TS" ]; then
        info "ManagedCluster has deletionTimestamp: $DELETION_TS"
    else
        fail "ManagedCluster should have deletion timestamp"
    fi
else
    fail "ManagedCluster was unexpectedly fully deleted"
fi

# Clear placement decisions to trigger orphan detection
$KUBECTL -n $ARGOCD_NS patch placementdecision "$PD_NAME" \
    --type merge --subresource status \
    -p '{"status":{"decisions":[]}}' 2>/dev/null
info "Cleared PlacementDecision while ManagedCluster has deletion timestamp"

sleep 30

if $KUBECTL -n $ARGOCD_NS get secret "$SECRET_NAME" > /dev/null 2>&1; then
    pass "Secret preserved while ManagedCluster has deletion timestamp but is not fully gone"
else
    fail "Secret was DELETED while ManagedCluster still exists (has deletion timestamp with finalizer)"
fi

# --------------------------------------------------------------------------
# TEST 3: Fully delete ManagedCluster - secret SHOULD be deleted
# --------------------------------------------------------------------------
echo ""
echo "--- Test 3: Fully deleted ManagedCluster SHOULD trigger secret deletion ---"

# Remove finalizer to allow full deletion
$KUBECTL patch managedcluster "$CLUSTER_NAME" \
    --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null
info "Removed finalizer, ManagedCluster should be fully deleted now"

# Wait for ManagedCluster to be fully gone
ELAPSED=0
while [ $ELAPSED -lt $TIMEOUT_SECONDS ]; do
    if ! $KUBECTL get managedcluster "$CLUSTER_NAME" > /dev/null 2>&1; then
        break
    fi
    sleep 5
    ELAPSED=$((ELAPSED + 5))
done

if $KUBECTL get managedcluster "$CLUSTER_NAME" > /dev/null 2>&1; then
    fail "ManagedCluster was not fully deleted within timeout"
fi
info "ManagedCluster fully deleted"

# Trigger reconciliation by annotating the GitOpsCluster
$KUBECTL -n $ARGOCD_NS annotate gitopscluster "$GITOPSCLUSTER_NAME" \
    e2e-trigger="$(date +%s)" --overwrite 2>/dev/null
info "Triggered GitOpsCluster reconciliation"

# Wait for reconcile to clean up the secret
if wait_for_secret "$SECRET_NAME" "$ARGOCD_NS" "false" $TIMEOUT_SECONDS; then
    pass "Secret deleted after ManagedCluster was fully removed"
else
    fail "Secret was NOT deleted after ManagedCluster was fully removed"
fi

# --------------------------------------------------------------------------
# CLEANUP: Recreate ManagedCluster for any subsequent tests
# --------------------------------------------------------------------------
echo ""
echo "--- Cleanup: recreating ManagedCluster ---"

$KUBECTL apply -f - <<EOF
apiVersion: cluster.open-cluster-management.io/v1
kind: ManagedCluster
metadata:
  name: $CLUSTER_NAME
  labels:
    test-label: test-value
    test-label-2: test-value-2
spec:
  hubAcceptsClient: true
  leaseDurationSeconds: 60
EOF

sleep 10

# Restore placement decision
$KUBECTL -n $ARGOCD_NS patch placementdecision "$PD_NAME" \
    --type merge --subresource status \
    -p "{\"status\":{\"decisions\":[{\"clusterName\":\"$CLUSTER_NAME\",\"reason\":\"\"}]}}" 2>/dev/null

info "ManagedCluster and PlacementDecision restored"

echo ""
echo "=== ALL Cluster Secret Deletion Protection tests PASSED ==="
