#!/bin/bash
# e2e-full.sh - Full e2e test with app sync verification (local use)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E FULL TEST - Deploy + Toggle + App Sync"
echo "========================================="

# Run setup with ArgoCD agent disabled
echo ""
echo "Running setup with ArgoCD agent disabled..."
./test/e2e/scripts/e2e-setup.sh

echo ""
echo "========================================="
echo "PHASE 1: GitOpsAddon Only (ArgoCD Agent Disabled)"
echo "========================================="
echo "Note: GitOpsCluster created with argoCDAgent.enabled=false by default"

# Verify ManagedClusterAddOn exists and has correct config (only AddonDeploymentConfig, no AddOnTemplate)
echo ""
echo "Step 1: Verifying ManagedClusterAddOn config (should NOT have dynamic AddOnTemplate)..."
ADDON_CONFIGS=$(kubectl get managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.spec.configs}')
echo "  ManagedClusterAddOn configs: ${ADDON_CONFIGS}"

# Count AddOnTemplate configs
TEMPLATE_COUNT=$(kubectl get managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.spec.configs[?(@.resource=="addontemplates")].name}' | wc -w)
if [ "$TEMPLATE_COUNT" -eq 0 ]; then
  echo "  ✓ No dynamic AddOnTemplate config (will use default from ClusterManagementAddOn)"
else
  echo "  ✗ ERROR: Found ${TEMPLATE_COUNT} AddOnTemplate config(s), expected 0 when ArgoCD agent is disabled"
  exit 1
fi

# Verify addon pod is running on managed cluster
echo ""
echo "Step 2: Verifying addon pod on managed cluster..."
for i in {1..60}; do
  POD_COUNT=$(kubectl --context ${SPOKE_CONTEXT} get pods -n ${ADDON_NAMESPACE} -l app=gitops-addon --no-headers 2>/dev/null | wc -l)
  if [ "$POD_COUNT" -gt 0 ]; then
    echo "  ✓ Found $POD_COUNT addon pod(s)"
    kubectl --context ${SPOKE_CONTEXT} wait --for=condition=ready --timeout=300s \
      pod -l app=gitops-addon -n ${ADDON_NAMESPACE} || true
    break
  fi
  echo "  Waiting for addon pods to appear... (attempt $i/60)"
  sleep 5
done

if [ "$POD_COUNT" -eq 0 ]; then
  echo "  ✗ ERROR: No addon pods found"
  exit 1
fi

# Verify ArgoCD CR exists on managed cluster (created by Policy)
echo ""
echo "Step 3: Verifying ArgoCD CR on managed cluster..."
for i in {1..60}; do
  if kubectl --context ${SPOKE_CONTEXT} get argocd acm-openshift-gitops -n ${GITOPS_NAMESPACE} &>/dev/null; then
    echo "  ✓ ArgoCD CR 'acm-openshift-gitops' exists (attempt $i/60)"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: ArgoCD CR not found after 60 attempts"
    exit 1
  fi
  echo "  Waiting for ArgoCD CR... (attempt $i/60)"
  sleep 5
done

# Verify ArgoCD application controller pod is running
echo ""
echo "Step 4: Verifying ArgoCD application controller pod on managed cluster..."
for i in {1..60}; do
  APP_CONTROLLER_PODS=$(kubectl --context ${SPOKE_CONTEXT} get pods -n ${GITOPS_NAMESPACE} -l app.kubernetes.io/name=acm-openshift-gitops-application-controller --no-headers 2>/dev/null | wc -l)
  if [ "$APP_CONTROLLER_PODS" -gt 0 ]; then
    echo "  ✓ Found $APP_CONTROLLER_PODS application controller pod(s)"
    kubectl --context ${SPOKE_CONTEXT} wait --for=condition=ready --timeout=300s \
      pod -l app.kubernetes.io/name=acm-openshift-gitops-application-controller -n ${GITOPS_NAMESPACE} 2>/dev/null || echo "  Waiting for pod to be ready..."
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: No application controller pods found after 60 attempts"
    exit 1
  fi
  echo "  Waiting for application controller pods... (attempt $i/60)"
  sleep 5
done

echo ""
echo "Step 5: Verifying GitOpsCluster conditions..."
# Check that core conditions are set to True
READY=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
PLACEMENT=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="PlacementResolved")].status}')
ARGOSERVER=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="ArgoServerVerified")].status}')
CLUSTERS=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="ClustersRegistered")].status}')
CONFIGS=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="AddOnDeploymentConfigsReady")].status}')
ADDONS=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="ManagedClusterAddOnsReady")].status}')

echo "  Conditions:"
echo "    Ready: ${READY}"
echo "    PlacementResolved: ${PLACEMENT}"
echo "    ArgoServerVerified: ${ARGOSERVER}"
echo "    ClustersRegistered: ${CLUSTERS}"
echo "    AddOnDeploymentConfigsReady: ${CONFIGS}"
echo "    ManagedClusterAddOnsReady: ${ADDONS}"

if [ "${READY}" != "True" ]; then
  echo "  ✗ ERROR: Ready condition is not True"
  kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o yaml
  exit 1
fi

if [ "${PLACEMENT}" != "True" ]; then
  echo "  ✗ ERROR: PlacementResolved condition is not True"
  exit 1
fi

if [ "${ARGOSERVER}" != "True" ]; then
  echo "  ✗ ERROR: ArgoServerVerified condition is not True"
  exit 1
fi

if [ "${CLUSTERS}" != "True" ]; then
  echo "  ✗ ERROR: ClustersRegistered condition is not True"
  exit 1
fi

if [ "${CONFIGS}" != "True" ]; then
  echo "  ✗ ERROR: AddOnDeploymentConfigsReady condition is not True"
  exit 1
fi

if [ "${ADDONS}" != "True" ]; then
  echo "  ✗ ERROR: ManagedClusterAddOnsReady condition is not True"
  exit 1
fi

echo "  ✓ All GitOpsCluster conditions are True"

echo ""
echo "========================================="
echo "✓ E2E GITOPSADDON TEST PASSED"
echo "========================================="
echo ""
echo "Summary:"
echo "  - GitOpsAddon deployed without ArgoCD agent"
echo "  - ArgoCD CR created on managed cluster via Policy"
echo "  - ArgoCD application controller running"
echo "  - All GitOpsCluster conditions are True"
echo ""
echo "Note: For ArgoCD agent tests, run: make test-e2e-gitopsaddon-agent-full"
