#!/bin/bash
# e2e-cleanup-full.sh - Full cleanup test (local use)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E CLEANUP FULL TEST - Setup + Cleanup"
echo "========================================="

# Run setup
echo ""
echo "Running setup..."
./test/e2e/scripts/e2e-setup.sh

# Run deploy verification
echo ""
echo "Running deploy verification..."
./test/e2e/scripts/e2e-deploy.sh

# Verify test application (already created in setup)
echo ""
echo "Verifying test application..."
echo ""

# Step 1: Verify application exists on hub
echo "1. Verifying application on hub..."
if kubectl get application guestbook -n cluster1 --context ${HUB_CONTEXT} &>/dev/null; then
  echo "  ✓ Application 'guestbook' exists on hub (cluster1 namespace)"
else
  echo "  ✗ Application 'guestbook' not found on hub"
  exit 1
fi

# Step 2: Verify app is synced to managed cluster
echo ""
echo "2. Verifying application is synced to managed cluster..."
APP_SYNCED=false
for i in {1..60}; do
  if kubectl get application guestbook -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} &>/dev/null; then
    echo "  ✓ Application synced to managed cluster (attempt $i/60)"
    APP_SYNCED=true
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: Application not synced to managed cluster after 60 attempts"
    echo "  This indicates the ArgoCD agent failed to sync"
    exit 1
  fi
  sleep 2
done

if [ "${APP_SYNCED}" == "false" ]; then
  echo ""
  echo "Application Sync Verification Summary:"
  echo "  • Application created on hub: ✓"
  echo "  • Application synced to managed: ✗ (ArgoCD agent may need more time)"
  echo ""
  # Continue to cleanup anyway
  echo "Continuing to cleanup test..."
else

# Step 3: Verify app status on managed cluster
echo ""
echo "3. Verifying application status on managed cluster..."
for i in {1..60}; do
  MANAGED_HEALTH=$(kubectl get application guestbook -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} -o jsonpath='{.status.health.status}' 2>/dev/null || echo "Unknown")
  MANAGED_SYNC=$(kubectl get application guestbook -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "Unknown")
  
  echo "  Managed cluster - Health: ${MANAGED_HEALTH}, Sync: ${MANAGED_SYNC} (attempt $i/60)"
  
  if [ "${MANAGED_HEALTH}" == "Healthy" ] && [ "${MANAGED_SYNC}" == "Synced" ]; then
    echo "  ✓ Application is Healthy and Synced on managed cluster"
    break
  fi
  
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: Application not healthy/synced after 60 attempts"
    echo "  Final status - Health: ${MANAGED_HEALTH}, Sync: ${MANAGED_SYNC}"
    exit 1
  fi
  sleep 5
done

# Step 4: Verify app status on hub matches managed
echo ""
echo "4. Verifying application status on hub..."
HUB_HEALTH=$(kubectl get application guestbook -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.status.health.status}' 2>/dev/null || echo "Unknown")
HUB_SYNC=$(kubectl get application guestbook -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "Unknown")

echo "  Hub cluster - Health: ${HUB_HEALTH}, Sync: ${HUB_SYNC}"

if [ "${HUB_HEALTH}" == "${MANAGED_HEALTH}" ] && [ "${HUB_SYNC}" == "${MANAGED_SYNC}" ]; then
  echo "  ✓ Application status on hub matches managed cluster"
else
  echo "  ✗ ERROR: Application status mismatch between hub and managed"
  echo "    Hub: Health=${HUB_HEALTH}, Sync=${HUB_SYNC}"
  echo "    Managed: Health=${MANAGED_HEALTH}, Sync=${MANAGED_SYNC}"
  exit 1
fi

  echo ""
  echo "Application Sync Verification Summary:"
  echo "  • Application created on hub: ✓"
  echo "  • Application synced to managed: ✓"
  echo "  • Managed cluster status: Health=${MANAGED_HEALTH}, Sync=${MANAGED_SYNC}"
  echo "  • Hub cluster status: Health=${HUB_HEALTH}, Sync=${HUB_SYNC}"
  echo ""
fi

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
echo "Step 7: Verifying ArgoCD Application still exists in openshift-gitops namespace..."
if kubectl get application guestbook -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} &>/dev/null; then
  echo "✓ ArgoCD Application 'guestbook' still exists in openshift-gitops namespace"
else
  echo "✗ ERROR: ArgoCD Application 'guestbook' not found in openshift-gitops namespace"
  exit 1
fi

echo ""
echo "Step 8: Verifying guestbook namespace still has resources..."
GUESTBOOK_RESOURCES=$(kubectl get all -n guestbook --context ${SPOKE_CONTEXT} --no-headers 2>/dev/null | wc -l)
if [ "${GUESTBOOK_RESOURCES}" -gt "0" ]; then
  echo "✓ Guestbook namespace has ${GUESTBOOK_RESOURCES} resource(s)"
  kubectl get all -n guestbook --context ${SPOKE_CONTEXT} 2>/dev/null || true
else
  echo "✗ ERROR: No resources found in guestbook namespace"
  exit 1
fi

echo ""
echo "========================================="
echo "✓ E2E CLEANUP FULL TEST PASSED"
echo "========================================="

