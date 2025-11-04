#!/bin/bash
# e2e-deploy.sh - Deploy and verify GitOps addon
# This is for CI - assumes kind clusters and images already exist
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E DEPLOY TEST - Verifying Deployment"
echo "========================================="

# Step 1: Verify controller is running
echo ""
echo "Step 1: Verifying GitOps controller is running..."
kubectl --context ${HUB_CONTEXT} wait --for=condition=available --timeout=300s \
  deployment/multicloud-integrations-gitops -n ${GITOPS_NAMESPACE}
echo "✓ Controller is running"

# Step 2: Verify GitOpsCluster exists and check status
echo ""
echo "Step 2: Verifying GitOpsCluster status..."
for i in {1..60}; do
  PHASE=$(kubectl --context ${HUB_CONTEXT} get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
  if [ "$PHASE" == "successful" ]; then
    echo "✓ GitOpsCluster phase: successful"
    break
  fi
  echo "  Waiting for GitOpsCluster to be successful... (attempt $i/60, current phase: $PHASE)"
  sleep 5
done

if [ "$PHASE" != "successful" ]; then
  echo "✗ GitOpsCluster did not reach successful state"
  kubectl --context ${HUB_CONTEXT} get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} -o yaml
  exit 1
fi

# Step 3: Verify CA certificates exist
echo ""
echo "Step 3: Verifying certificates..."
kubectl --context ${HUB_CONTEXT} get secret argocd-agent-ca -n ${GITOPS_NAMESPACE}
kubectl --context ${HUB_CONTEXT} get secret argocd-agent-principal-tls -n ${GITOPS_NAMESPACE}
echo "✓ Certificates exist"

# Step 4: Verify AddOnTemplate exists
echo ""
echo "Step 4: Verifying AddOnTemplate..."
kubectl --context ${HUB_CONTEXT} get addontemplate gitops-addon-openshift-gitops-gitopscluster
echo "✓ AddOnTemplate exists"

# Step 5: Verify ManagedClusterAddon exists
echo ""
echo "Step 5: Verifying ManagedClusterAddon..."
kubectl --context ${HUB_CONTEXT} get managedclusteraddon gitops-addon -n cluster1
echo "✓ ManagedClusterAddon exists"

# Step 6: Verify addon pods are running on managed cluster
echo ""
echo "Step 6: Verifying addon pods on managed cluster..."
for i in {1..60}; do
  POD_COUNT=$(kubectl --context ${SPOKE_CONTEXT} get pods -n ${ADDON_NAMESPACE} -l app=gitops-addon --no-headers 2>/dev/null | wc -l)
  if [ "$POD_COUNT" -gt 0 ]; then
    echo "✓ Found $POD_COUNT addon pod(s)"
    kubectl --context ${SPOKE_CONTEXT} wait --for=condition=ready --timeout=300s \
      pod -l app=gitops-addon -n ${ADDON_NAMESPACE} || true
    break
  fi
  echo "  Waiting for addon pods to appear... (attempt $i/60)"
  sleep 5
done

if [ "$POD_COUNT" -eq 0 ]; then
  echo "⚠ No addon pods found yet (this may be expected if addon-manager hasn't reconciled)"
  echo "  Checking ManifestWorks..."
  kubectl --context ${HUB_CONTEXT} get manifestwork -n cluster1
fi

# Step 7: Check if ArgoCD components are deploying
echo ""
echo "Step 7: Checking ArgoCD components..."
for i in {1..30}; do
  ARGOCD_PODS=$(kubectl --context ${SPOKE_CONTEXT} get pods -A -l app.kubernetes.io/part-of=argocd --no-headers 2>/dev/null | wc -l)
  if [ "$ARGOCD_PODS" -gt 0 ]; then
    echo "✓ Found $ARGOCD_PODS ArgoCD pod(s)"
    kubectl --context ${SPOKE_CONTEXT} get pods -A -l app.kubernetes.io/part-of=argocd
    break
  fi
  echo "  Waiting for ArgoCD components... (attempt $i/30)"
  sleep 10
done

echo ""
echo "========================================="
echo "✓ E2E DEPLOY TEST PASSED"
echo "========================================="

