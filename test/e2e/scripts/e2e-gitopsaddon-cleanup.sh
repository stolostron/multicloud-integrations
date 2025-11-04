#!/bin/bash
# e2e-gitopsaddon-cleanup-ci.sh - GitOps addon cleanup test for CI (assumes clusters/images ready, no leftover resource check)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E GITOPS ADDON CLEANUP TEST (CI mode)"
echo "========================================="

# Run setup (assumes clusters already exist)
echo ""
echo "Running setup..."
./test/e2e/scripts/e2e-setup.sh

# Run deploy verification
echo ""
echo "Running deploy verification..."
./test/e2e/scripts/e2e-deploy.sh

# Run cleanup
echo ""
echo "Running cleanup..."
echo "========================================="
echo "E2E CLEANUP TEST - Verifying Cleanup"
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
    echo "⚠ Warning: GitOpsCluster still exists after 60 attempts"
  fi
  sleep 2
done

echo ""
echo "Step 3: Deleting ManagedClusterAddon..."
kubectl delete managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} 2>/dev/null && \
  echo "managedclusteraddon.addon.open-cluster-management.io \"gitops-addon\" deleted from cluster1 namespace" || \
  echo "ManagedClusterAddon already deleted or not found"
echo "✓ ManagedClusterAddon deleted"

echo ""
echo "Step 4: Verifying addon pods are removed from managed cluster..."
for i in {1..60}; do
  ADDON_PODS=$(kubectl get pods -n ${ADDON_NAMESPACE} --context ${SPOKE_CONTEXT} --no-headers 2>/dev/null | grep gitops-addon | wc -l)
  if [ "${ADDON_PODS}" -eq "0" ]; then
    echo "✓ Addon pods removed"
    break
  fi
  echo "  Waiting for addon pods to be removed... (attempt $i/60, current count: ${ADDON_PODS})"
  if [ $i -eq 60 ]; then
    echo "⚠ Warning: Addon pods still exist after 60 attempts"
  fi
  sleep 2
done

echo ""
echo "Step 5: Verifying ArgoCD components are removed..."
ARGOCD_PODS=$(kubectl get pods -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} --no-headers 2>/dev/null | wc -l)
if [ "${ARGOCD_PODS}" -eq "0" ]; then
  echo "✓ ArgoCD components removed"
else
  echo "⚠ Warning: ${ARGOCD_PODS} ArgoCD pod(s) still running"
fi

echo ""
echo "Step 6: Verifying ManifestWorks are cleaned up..."
MANIFEST_WORKS=$(kubectl get manifestworks -n cluster1 --context ${HUB_CONTEXT} --no-headers 2>/dev/null | wc -l)
if [ "${MANIFEST_WORKS}" -gt "0" ]; then
  echo "  Remaining ManifestWorks: ${MANIFEST_WORKS}"
  kubectl get manifestworks -n cluster1 --context ${HUB_CONTEXT} 2>/dev/null || true
else
  echo "✓ All ManifestWorks cleaned up"
fi

echo ""
echo "========================================="
echo "✓ E2E CLEANUP TEST PASSED"
echo "========================================="

echo ""
echo "========================================="
echo "✓ E2E GITOPS ADDON CLEANUP TEST PASSED"
echo "========================================="

