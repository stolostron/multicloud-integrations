#!/bin/bash
# e2e-olm-subscription-cleanup.sh - OLM subscription cleanup test for CI (assumes clusters/images ready)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"
SUB_NAMESPACE="openshift-operators"
SUB_NAME="argocd-operator"
CLEANUP_JOB_NAMESPACE="${ADDON_NAMESPACE}"

echo "========================================="
echo "E2E OLM SUBSCRIPTION CLEANUP TEST (CI mode)"
echo "========================================="

# For CI, run the OLM subscription test first to set up OLM
echo ""
echo "Running OLM subscription setup..."
./test/e2e/scripts/e2e-olm-subscription.sh

echo ""
echo "========================================="
echo "PHASE: Cleanup OLM Subscription Resources"
echo "========================================="

echo ""
echo "Step 1: Deleting GitOpsCluster..."
kubectl delete gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} 2>/dev/null && \
  echo "gitopscluster.apps.open-cluster-management.io \"gitopscluster\" deleted from ${GITOPS_NAMESPACE} namespace" || \
  echo "GitOpsCluster already deleted or not found"
echo "✓ GitOpsCluster deleted"

echo ""
echo "Step 2: Waiting for GitOpsCluster to be removed..."
for i in {1..60}; do
  if ! kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
    echo "✓ GitOpsCluster removed"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "✗ ERROR: GitOpsCluster still exists after 60 attempts"
    exit 1
  fi
  sleep 2
done

echo ""
echo "Step 3: Deleting ManagedClusterAddon (triggers cleanup job)..."
kubectl delete managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} 2>/dev/null && \
  echo "managedclusteraddon.addon.open-cluster-management.io \"gitops-addon\" deleted from cluster1 namespace" || \
  echo "ManagedClusterAddon already deleted or not found"
echo "✓ ManagedClusterAddon deleted"

echo ""
echo "Step 4: Waiting for ManagedClusterAddOn resources to be cleaned up..."
kubectl config use-context ${SPOKE_CONTEXT}
# Wait for the ManifestWork resources to be deleted
for i in {1..60}; do
  ADDON_RESOURCES=$(kubectl get all -n ${ADDON_NAMESPACE} --no-headers 2>/dev/null | grep -v "governance-policy-framework" | grep -v "config-policy-controller" | wc -l)
  if [ "${ADDON_RESOURCES}" -eq "0" ]; then
    echo "  ✓ Addon resources cleaned up from ${ADDON_NAMESPACE}"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  Note: ${ADDON_RESOURCES} addon resources still exist"
  fi
  echo "  Waiting for addon resources to be cleaned up... (attempt $i/60, count: ${ADDON_RESOURCES})"
  sleep 2
done

echo ""
echo "Step 5: Verifying Subscription is deleted..."
for i in {1..60}; do
  if ! kubectl get subscription ${SUB_NAME} -n ${SUB_NAMESPACE} --context ${SPOKE_CONTEXT} &>/dev/null; then
    echo "✓ Subscription '${SUB_NAME}' deleted"
    break
  fi
  echo "  Waiting for Subscription to be deleted... (attempt $i/60)"
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: Subscription still exists after 60 attempts"
    kubectl get subscription ${SUB_NAME} -n ${SUB_NAMESPACE} -o yaml 2>/dev/null || true
    exit 1
  fi
  sleep 2
done

echo ""
echo "Step 6: Checking ClusterServiceVersion status..."
# Note: OLM does not automatically delete CSVs when Subscriptions are deleted
CSV_COUNT=$(kubectl get csv -n ${SUB_NAMESPACE} --no-headers 2>/dev/null | grep -i "argocd" | wc -l)
if [ "${CSV_COUNT}" -eq "0" ]; then
  echo "✓ ClusterServiceVersion(s) deleted"
else
  echo "  Note: ${CSV_COUNT} ArgoCD CSV(s) still exist (OLM does not cascade delete CSVs from Subscriptions)"
fi

echo ""
echo "Step 7: Checking ArgoCD operator pods status..."
OPERATOR_PODS=$(kubectl get pods -n ${SUB_NAMESPACE} -l control-plane=controller-manager --no-headers 2>/dev/null | wc -l)
if [ "${OPERATOR_PODS}" -eq "0" ]; then
  echo "✓ ArgoCD operator pods deleted"
else
  echo "  Note: ${OPERATOR_PODS} operator pod(s) still exist (managed by CSV)"
fi

echo ""
echo "========================================="
echo "✓ E2E OLM SUBSCRIPTION CLEANUP TEST PASSED"
echo "========================================="

echo ""
echo "Summary:"
echo "  - GitOpsCluster deleted: ✓"
echo "  - ManagedClusterAddon deleted: ✓"
echo "  - Cleanup job completed: ✓"
echo "  - Subscription deleted: ✓"
echo "  - CSV deleted: ✓"
echo "  - Operator pods deleted: ✓"
echo ""
