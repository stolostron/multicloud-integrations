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

# Run setup with ArgoCD agent enabled (assumes clusters already exist)
echo ""
echo "Running setup with ArgoCD agent enabled..."
ENABLE_ARGOCD_AGENT=true ./test/e2e/scripts/e2e-setup.sh

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
echo "Step 4: Verifying ArgoCD CR is deleted from openshift-gitops namespace..."
for i in {1..60}; do
  if ! kubectl get argocd -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} --no-headers 2>/dev/null | grep -q .; then
    echo "✓ ArgoCD CR deleted"
    break
  fi
  echo "  Waiting for ArgoCD CR to be deleted... (attempt $i/60)"
  if [ $i -eq 60 ]; then
    echo "✗ ERROR: ArgoCD CR still exists after 60 attempts"
    kubectl get argocd -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} 2>/dev/null || true
    exit 1
  fi
  sleep 2
done

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
echo "Step 6: Verifying open-cluster-management-agent-addon namespace is cleaned up..."
for i in {1..60}; do
  ADDON_RESOURCES=$(kubectl get all -n ${ADDON_NAMESPACE} --context ${SPOKE_CONTEXT} --no-headers 2>/dev/null | wc -l)
  if [ "${ADDON_RESOURCES}" -eq "0" ]; then
    echo "✓ No resources found in open-cluster-management-agent-addon namespace"
    break
  fi
  echo "  Waiting for addon namespace to be cleaned up... (attempt $i/60, current count: ${ADDON_RESOURCES})"
  if [ $i -eq 60 ]; then
    echo "✗ ERROR: ${ADDON_RESOURCES} resource(s) still exist in open-cluster-management-agent-addon namespace"
    kubectl get all -n ${ADDON_NAMESPACE} --context ${SPOKE_CONTEXT} 2>/dev/null || true
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

