#!/bin/bash
# e2e-gitopsaddon.sh - GitOps addon test for CI (assumes clusters/images ready, no app sync)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E GITOPS ADDON TEST (CI mode)"
echo "========================================="

# Run setup with ArgoCD agent disabled (assumes clusters already exist)
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

# Verify ArgoCD CR exists on managed cluster
echo ""
echo "Step 3: Verifying ArgoCD CR on managed cluster..."
for i in {1..60}; do
  if kubectl --context ${SPOKE_CONTEXT} get argocd openshift-gitops -n ${GITOPS_NAMESPACE} &>/dev/null; then
    echo "  ✓ ArgoCD CR 'openshift-gitops' exists (attempt $i/60)"
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
  APP_CONTROLLER_PODS=$(kubectl --context ${SPOKE_CONTEXT} get pods -n ${GITOPS_NAMESPACE} -l app.kubernetes.io/name=openshift-gitops-application-controller --no-headers 2>/dev/null | wc -l)
  if [ "$APP_CONTROLLER_PODS" -gt 0 ]; then
    echo "  ✓ Found $APP_CONTROLLER_PODS application controller pod(s)"
    kubectl --context ${SPOKE_CONTEXT} wait --for=condition=ready --timeout=300s \
      pod -l app.kubernetes.io/name=openshift-gitops-application-controller -n ${GITOPS_NAMESPACE} 2>/dev/null || echo "  Waiting for pod to be ready..."
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
echo "========================================="
echo "✓ PHASE 1 PASSED - GitOpsAddon Only Works"
echo "========================================="

echo ""
echo "========================================="
echo "PHASE 2: Enable ArgoCD Agent"
echo "========================================="

# Re-enable ArgoCD agent (AddOnDeploymentConfig will be updated automatically)
echo ""
echo "Step 1: Enabling ArgoCD Agent..."
kubectl patch gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --type=merge --context ${HUB_CONTEXT} -p '{"spec":{"gitopsAddon":{"argoCDAgent":{"enabled":true}}}}'

# Wait for the controller to process the change
echo "  Waiting for controller to process the change..."
sleep 10

# Verify AddOnDeploymentConfig was updated
echo ""
echo "Step 1.5: Verifying AddOnDeploymentConfig was updated..."
for i in {1..30}; do
  AGENT_ENABLED=$(kubectl get addondeploymentconfig gitops-addon-config -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.spec.customizedVariables[?(@.name=="ARGOCD_AGENT_ENABLED")].value}' 2>/dev/null || echo "false")
  if [ "${AGENT_ENABLED}" == "true" ]; then
    echo "  ✓ AddOnDeploymentConfig updated: ARGOCD_AGENT_ENABLED=true"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: AddOnDeploymentConfig not updated after 30 attempts"
    kubectl get addondeploymentconfig gitops-addon-config -n cluster1 --context ${HUB_CONTEXT} -o yaml
    exit 1
  fi
  echo "  Waiting for AddOnDeploymentConfig update... (attempt $i/30, current value: ${AGENT_ENABLED})"
  sleep 2
done

echo "  Waiting for addon deployment to pick up new configuration..."
for i in {1..60}; do
  ADDON_POD=$(kubectl --context ${SPOKE_CONTEXT} get pods -n ${ADDON_NAMESPACE} -l app=gitops-addon -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
  if [ -n "${ADDON_POD}" ]; then
    AGENT_ENABLED_ENV=$(kubectl --context ${SPOKE_CONTEXT} get pod ${ADDON_POD} -n ${ADDON_NAMESPACE} -o jsonpath='{.spec.containers[0].env[?(@.name=="ARGOCD_AGENT_ENABLED")].value}' 2>/dev/null || echo "false")
    if [ "${AGENT_ENABLED_ENV}" == "true" ]; then
      echo "  ✓ Addon pod has ARGOCD_AGENT_ENABLED=true"
      break
    fi
    echo "  Waiting for addon pod to reflect ARGOCD_AGENT_ENABLED=true... (attempt $i/60, current: ${AGENT_ENABLED_ENV})"
  else
    echo "  Waiting for addon pod to be ready... (attempt $i/60)"
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: Addon pod did not pick up ARGOCD_AGENT_ENABLED=true after 60 attempts"
    kubectl --context ${SPOKE_CONTEXT} get deployment -n ${ADDON_NAMESPACE} -l app=gitops-addon -o yaml
    exit 1
  fi
  sleep 2
done

# Verify ManagedClusterAddOn now has both configs
echo ""
echo "Step 2: Verifying ManagedClusterAddOn config (should NOW have dynamic AddOnTemplate)..."
for i in {1..30}; do
  TEMPLATE_COUNT=$(kubectl get managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.spec.configs[?(@.resource=="addontemplates")].name}' | wc -w)
  if [ "$TEMPLATE_COUNT" -gt 0 ]; then
    TEMPLATE_NAME=$(kubectl get managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.spec.configs[?(@.resource=="addontemplates")].name}')
    echo "  ✓ Found dynamic AddOnTemplate: ${TEMPLATE_NAME}"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: No AddOnTemplate config found after enabling ArgoCD agent"
    exit 1
  fi
  echo "  Waiting for AddOnTemplate config... (attempt $i/30)"
  sleep 2
done

# Run deploy verification
echo ""
echo "Step 3: Running full deploy verification with ArgoCD agent enabled..."
./test/e2e/scripts/e2e-deploy.sh

echo ""
echo "========================================="
echo "✓ E2E GITOPS ADDON TEST PASSED"
echo "========================================="

