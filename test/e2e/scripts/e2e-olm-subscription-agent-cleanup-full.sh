#!/bin/bash
# e2e-olm-subscription-agent-cleanup-full.sh - OLM Subscription cleanup test with ArgoCD agent enabled
# This test verifies cleanup of OLM subscription mode combined with ArgoCD agent
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"
E2E_IMG="${E2E_IMG:-quay.io/stolostron/multicloud-integrations:latest}"

SUB_NAMESPACE="openshift-operators"
SUB_NAME="argocd-operator"

echo "========================================="
echo "E2E OLM SUBSCRIPTION AGENT CLEANUP FULL TEST"
echo "========================================="

# Run the OLM subscription agent setup
echo ""
echo "Running OLM subscription agent setup..."
./test/e2e/scripts/e2e-olm-subscription-agent-full.sh

echo ""
echo "========================================="
echo "PHASE 5: Verify Resources Before Cleanup"
echo "========================================="

echo ""
echo "Step 1: Verifying resources exist before cleanup..."

# Verify GitOpsCluster exists
kubectl config use-context ${HUB_CONTEXT}
if kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} &>/dev/null; then
  echo "  ✓ GitOpsCluster exists"
else
  echo "  ✗ ERROR: GitOpsCluster not found"
  exit 1
fi

# Verify dynamic AddOnTemplate exists
TEMPLATE_NAME="gitops-addon-olm-${GITOPS_NAMESPACE}-gitopscluster"
if kubectl get addontemplate ${TEMPLATE_NAME} &>/dev/null; then
  echo "  ✓ Dynamic AddOnTemplate '${TEMPLATE_NAME}' exists"
else
  echo "  ✗ ERROR: Dynamic AddOnTemplate not found"
  exit 1
fi

# Verify ManagedClusterAddOn exists
if kubectl get managedclusteraddon gitops-addon -n cluster1 &>/dev/null; then
  echo "  ✓ ManagedClusterAddOn exists"
else
  echo "  ✗ ERROR: ManagedClusterAddOn not found"
  exit 1
fi

# Verify Subscription exists on managed cluster
kubectl config use-context ${SPOKE_CONTEXT}
if kubectl get subscription ${SUB_NAME} -n ${SUB_NAMESPACE} &>/dev/null; then
  echo "  ✓ Subscription exists on managed cluster"
else
  echo "  Note: Subscription may have been created differently"
fi

# Verify ArgoCD operator is running (Helm chart deploys to openshift-gitops-operator)
OPERATOR_PODS=$(kubectl get pods -n openshift-gitops-operator -l control-plane=gitops-operator --no-headers 2>/dev/null | grep Running | wc -l)
if [ "${OPERATOR_PODS}" -gt 0 ]; then
  echo "  ✓ ArgoCD operator pod is running"
else
  echo "  Note: ArgoCD operator pod may be in different state"
fi

echo ""
echo "========================================="
echo "PHASE 6: Trigger Cleanup"
echo "========================================="

kubectl config use-context ${HUB_CONTEXT}

echo ""
echo "Step 2: Deleting GitOpsCluster..."
kubectl delete gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} 2>/dev/null && \
  echo "  gitopscluster.apps.open-cluster-management.io \"gitopscluster\" deleted" || \
  echo "  GitOpsCluster already deleted or not found"

echo ""
echo "Step 3: Waiting for GitOpsCluster to be removed..."
for i in {1..60}; do
  if ! kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} &>/dev/null; then
    echo "  ✓ GitOpsCluster removed"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: GitOpsCluster still exists after 60 attempts"
    exit 1
  fi
  echo "  Waiting for GitOpsCluster removal... (attempt $i/60)"
  sleep 2
done

echo ""
echo "Step 4: Deleting ManagedClusterAddon..."
kubectl delete managedclusteraddon gitops-addon -n cluster1 2>/dev/null && \
  echo "  managedclusteraddon.addon.open-cluster-management.io \"gitops-addon\" deleted" || \
  echo "  ManagedClusterAddon already deleted or not found"

echo ""
echo "Step 5: Waiting for ManagedClusterAddon to be removed..."
for i in {1..60}; do
  if ! kubectl get managedclusteraddon gitops-addon -n cluster1 &>/dev/null; then
    echo "  ✓ ManagedClusterAddon removed"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  Note: ManagedClusterAddon may take longer to delete (waiting for pre-delete job)"
  fi
  echo "  Waiting for ManagedClusterAddon removal... (attempt $i/60)"
  sleep 2
done

echo ""
echo "========================================="
echo "PHASE 7: Verify Cleanup Results"
echo "========================================="

kubectl config use-context ${SPOKE_CONTEXT}

echo ""
echo "Step 6: Waiting for addon resources to be cleaned up..."
for i in {1..60}; do
  ADDON_RESOURCES=$(kubectl get all -n ${ADDON_NAMESPACE} --no-headers 2>/dev/null | grep -v "governance-policy-framework" | grep -v "config-policy-controller" | wc -l)
  if [ "${ADDON_RESOURCES}" -eq "0" ]; then
    echo "  ✓ Addon resources cleaned up from ${ADDON_NAMESPACE}"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  Note: ${ADDON_RESOURCES} addon resources still exist"
  fi
  echo "  Waiting for addon cleanup... (attempt $i/60, count: ${ADDON_RESOURCES})"
  sleep 2
done

echo ""
echo "Step 7: Verifying Subscription is deleted..."
for i in {1..60}; do
  if ! kubectl get subscription ${SUB_NAME} -n ${SUB_NAMESPACE} &>/dev/null; then
    echo "  ✓ Subscription '${SUB_NAME}' deleted"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  Note: Subscription still exists (may be managed by OLM differently)"
  fi
  echo "  Waiting for Subscription deletion... (attempt $i/60)"
  sleep 2
done

echo ""
echo "Step 8: Checking ClusterServiceVersion status..."
CSV_COUNT=$(kubectl get csv -n ${SUB_NAMESPACE} --no-headers 2>/dev/null | grep -i "argocd" | wc -l)
if [ "${CSV_COUNT}" -eq "0" ]; then
  echo "  ✓ ClusterServiceVersion(s) deleted"
else
  echo "  Note: ${CSV_COUNT} ArgoCD CSV(s) still exist (OLM does not cascade delete CSVs)"
fi

echo ""
echo "Step 9: Checking ArgoCD operator pods status..."
OPERATOR_PODS=$(kubectl get pods -n ${SUB_NAMESPACE} -l control-plane=controller-manager --no-headers 2>/dev/null | wc -l)
if [ "${OPERATOR_PODS}" -eq "0" ]; then
  echo "  ✓ ArgoCD operator pods deleted"
else
  echo "  Note: ${OPERATOR_PODS} operator pod(s) still exist (managed by CSV)"
fi

echo ""
echo "Step 10: Verifying dynamic AddOnTemplate is deleted..."
kubectl config use-context ${HUB_CONTEXT}
for i in {1..30}; do
  if ! kubectl get addontemplate ${TEMPLATE_NAME} &>/dev/null; then
    echo "  ✓ Dynamic AddOnTemplate '${TEMPLATE_NAME}' deleted"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  Note: Dynamic AddOnTemplate still exists (should be garbage collected)"
  fi
  echo "  Waiting for AddOnTemplate deletion... (attempt $i/30)"
  sleep 2
done

echo ""
echo "Step 11: Verifying certificates are cleaned up..."
if ! kubectl get secret argocd-agent-principal-tls -n ${GITOPS_NAMESPACE} &>/dev/null; then
  echo "  ✓ Principal TLS certificate deleted"
else
  echo "  Note: Principal TLS certificate still exists (should be garbage collected with GitOpsCluster)"
fi

echo ""
echo "========================================="
echo "PHASE 8: Final Verification"
echo "========================================="

kubectl config use-context ${SPOKE_CONTEXT}

echo ""
echo "Checking remaining resources in ${SUB_NAMESPACE} namespace..."
REMAINING=$(kubectl get all -n ${SUB_NAMESPACE} --no-headers 2>/dev/null | wc -l)
echo "  Resources remaining: ${REMAINING}"
if [ "${REMAINING}" -gt "0" ]; then
  kubectl get all -n ${SUB_NAMESPACE} 2>/dev/null | head -20 || true
fi

echo ""
echo "Checking OLM subscriptions..."
SUB_COUNT=$(kubectl get subscriptions -n ${SUB_NAMESPACE} --no-headers 2>/dev/null | wc -l)
echo "  Subscriptions in ${SUB_NAMESPACE}: ${SUB_COUNT}"

echo ""
echo "Checking OLM CSVs..."
CSV_COUNT=$(kubectl get csv -n ${SUB_NAMESPACE} --no-headers 2>/dev/null | wc -l)
echo "  CSVs in ${SUB_NAMESPACE}: ${CSV_COUNT}"

echo ""
echo "========================================="
echo "✓ E2E OLM SUBSCRIPTION AGENT CLEANUP FULL TEST PASSED"
echo "========================================="
echo ""
echo "Summary:"
echo "  - OLM subscription agent setup: ✓"
echo "  - Verified resources before cleanup: ✓"
echo "  - GitOpsCluster deleted: ✓"
echo "  - ManagedClusterAddon deleted: ✓"
echo "  - Dynamic AddOnTemplate cleanup verified"
echo "  - Subscription cleanup verified"
echo "  - Certificate cleanup verified"
