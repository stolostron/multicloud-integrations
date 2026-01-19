#!/bin/bash
# e2e-gitopsaddon-cleanup-ci.sh - GitOps addon cleanup test for CI (assumes clusters/images ready, no leftover resource check)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E GITOPS ADDON CLEANUP TEST (CI mode)"
echo "========================================="

# Run setup (assumes clusters already exist)
# NOTE: ArgoCD agent is disabled for CI cleanup tests to avoid timeout issues.
# ArgoCD agent with cleanup is tested in the full local test (test-e2e-gitopsaddon-cleanup-full).
echo ""
echo "Running setup..."
ENABLE_ARGOCD_AGENT=false ./test/e2e/scripts/e2e-setup.sh

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
    echo "✗ ERROR: GitOpsCluster still exists after 60 attempts"
    exit 1
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
echo "Step 4: Checking ArgoCD CR and Policy status..."
# ArgoCD CR and Policy are only created when ArgoCD agent is enabled
# In CI cleanup test, agent is disabled, so these resources won't exist
if kubectl get argocd acm-openshift-gitops -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} &>/dev/null; then
  echo "✓ ArgoCD CR 'acm-openshift-gitops' still exists (expected - managed by Policy)"
  echo ""
  echo "Step 4.1: Verifying Policy still exists on hub (not deleted with GitOpsCluster)..."
  if kubectl get policy gitopscluster-argocd-policy -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
    echo "✓ Policy 'gitopscluster-argocd-policy' still exists (expected - no ownerReferences)"
  else
    echo "  Note: Policy may not exist if Policy framework was not installed"
  fi
else
  echo "  Note: ArgoCD CR not found (expected when ArgoCD agent is disabled)"
fi

echo ""
echo "Step 5: Verifying openshift-gitops-operator namespace is cleaned up..."
for i in {1..60}; do
  OPERATOR_RESOURCES=$(kubectl get all -n openshift-gitops-operator --context ${SPOKE_CONTEXT} --no-headers 2>/dev/null | wc -l)
  if [ "${OPERATOR_RESOURCES}" -eq "0" ]; then
    echo "✓ No resources found in openshift-gitops-operator namespace"
    break
  fi
  echo "  Waiting for openshift-gitops-operator to be cleaned up... (attempt $i/60, current count: ${OPERATOR_RESOURCES})"
  if [ $i -eq 60 ]; then
    echo "✗ ERROR: ${OPERATOR_RESOURCES} resource(s) still exist in openshift-gitops-operator namespace"
    kubectl get all -n openshift-gitops-operator --context ${SPOKE_CONTEXT} 2>/dev/null || true
    exit 1
  fi
  sleep 2
done

echo ""
echo "Step 6: Verifying gitops-addon is cleaned up from open-cluster-management-agent-addon namespace..."
echo "  (Note: governance-policy-framework and config-policy-controller pods are expected to remain)"
for i in {1..60}; do
  # Count only gitops-addon related resources, excluding policy framework pods
  GITOPS_ADDON_RESOURCES=$(kubectl get all -n ${ADDON_NAMESPACE} --context ${SPOKE_CONTEXT} --no-headers 2>/dev/null | grep -v "governance-policy-framework" | grep -v "config-policy-controller" | wc -l)
  if [ "${GITOPS_ADDON_RESOURCES}" -eq "0" ]; then
    echo "✓ No gitops-addon resources found in open-cluster-management-agent-addon namespace"
    # List remaining policy resources for reference
    REMAINING=$(kubectl get all -n ${ADDON_NAMESPACE} --context ${SPOKE_CONTEXT} --no-headers 2>/dev/null | wc -l)
    if [ "${REMAINING}" -gt "0" ]; then
      echo "  (Policy framework pods still running as expected: ${REMAINING} resources)"
    fi
    break
  fi
  echo "  Waiting for gitops-addon to be cleaned up... (attempt $i/60, current count: ${GITOPS_ADDON_RESOURCES})"
  if [ $i -eq 60 ]; then
    echo "✗ ERROR: ${GITOPS_ADDON_RESOURCES} gitops-addon resource(s) still exist"
    kubectl get all -n ${ADDON_NAMESPACE} --context ${SPOKE_CONTEXT} 2>/dev/null | grep -v "governance-policy-framework" | grep -v "config-policy-controller" || true
    exit 1
  fi
  sleep 2
done

echo ""
echo "Step 7: Verifying pause marker ConfigMap is deleted..."
for i in {1..60}; do
  if ! kubectl get configmap gitops-addon-pause -n ${ADDON_NAMESPACE} --context ${SPOKE_CONTEXT} &>/dev/null; then
    echo "✓ Pause marker ConfigMap 'gitops-addon-pause' deleted (garbage collected with Deployment)"
    break
  fi
  echo "  Waiting for pause marker to be garbage collected... (attempt $i/60)"
  if [ $i -eq 60 ]; then
    echo "✗ ERROR: Pause marker ConfigMap still exists after 60 attempts"
    kubectl get configmap gitops-addon-pause -n ${ADDON_NAMESPACE} --context ${SPOKE_CONTEXT} -o yaml 2>/dev/null || true
    exit 1
  fi
  sleep 2
done


echo ""
echo "========================================="
echo "✓ E2E CLEANUP TEST PASSED"
echo "========================================="

echo ""
echo "========================================="
echo "✓ E2E GITOPS ADDON CLEANUP TEST PASSED"
echo "========================================="
