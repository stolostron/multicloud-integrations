#!/bin/bash
# e2e-gitopsaddon-agent-full.sh - Full e2e test with ArgoCD agent enabled (local use)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E GITOPSADDON AGENT FULL TEST"
echo "========================================="

# Run setup with ArgoCD agent enabled
echo ""
echo "Running setup WITH ArgoCD agent enabled..."
ENABLE_ARGOCD_AGENT=true ./test/e2e/scripts/e2e-setup.sh

# Run deploy verification
echo ""
echo "Running deploy verification..."
./test/e2e/scripts/e2e-deploy.sh

echo ""
echo "========================================="
echo "Verifying ArgoCD Agent Configuration"
echo "========================================="

# Verify ManagedClusterAddOn has dynamic AddOnTemplate
echo ""
echo "Step 1: Verifying ManagedClusterAddOn has dynamic AddOnTemplate..."
TEMPLATE_COUNT=$(kubectl get managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.spec.configs[?(@.resource=="addontemplates")].name}' | wc -w)
if [ "$TEMPLATE_COUNT" -gt 0 ]; then
  TEMPLATE_NAME=$(kubectl get managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.spec.configs[?(@.resource=="addontemplates")].name}')
  echo "  ✓ Found dynamic AddOnTemplate: ${TEMPLATE_NAME}"
else
  echo "  ✗ ERROR: No dynamic AddOnTemplate config found"
  exit 1
fi

# Verify GitOpsCluster agent conditions
echo ""
echo "Step 2: Verifying GitOpsCluster ArgoCD agent conditions..."
READY=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
TEMPLATE=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="AddOnTemplateReady")].status}')
AGENT_PREREQS=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="ArgoCDAgentPrereqsReady")].status}')
CERTS=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="CertificatesReady")].status}')
MANIFESTWORKS=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="ManifestWorksApplied")].status}')

echo "  ArgoCD Agent Conditions:"
echo "    Ready: ${READY}"
echo "    AddOnTemplateReady: ${TEMPLATE}"
echo "    ArgoCDAgentPrereqsReady: ${AGENT_PREREQS}"
echo "    CertificatesReady: ${CERTS}"
echo "    ManifestWorksApplied: ${MANIFESTWORKS}"

if [ "${READY}" != "True" ]; then
  echo "  ✗ ERROR: Ready condition is not True"
  kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o yaml
  exit 1
fi

if [ "${TEMPLATE}" != "True" ]; then
  echo "  ✗ ERROR: AddOnTemplateReady condition is not True"
  exit 1
fi

if [ "${AGENT_PREREQS}" != "True" ]; then
  echo "  ✗ ERROR: ArgoCDAgentPrereqsReady condition is not True"
  exit 1
fi

if [ "${CERTS}" != "True" ]; then
  echo "  ✗ ERROR: CertificatesReady condition is not True"
  exit 1
fi

if [ "${MANIFESTWORKS}" != "True" ]; then
  echo "  ✗ ERROR: ManifestWorksApplied condition is not True"
  exit 1
fi

echo "  ✓ All ArgoCD agent conditions are True"

echo ""
echo "========================================="
echo "Verifying Application Sync (Agent Mode)"
echo "========================================="

# Step 1: Verify test application on hub (created by e2e-setup.sh via Policy + agent mode)
echo ""
echo "Step 1: Verifying application exists on hub..."
for i in {1..30}; do
  if kubectl get application guestbook -n cluster1 --context ${HUB_CONTEXT} &>/dev/null; then
    echo "  ✓ Application 'guestbook' exists on hub (cluster1 namespace)"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: Application 'guestbook' not found on hub after 30 attempts"
    exit 1
  fi
  echo "  Waiting for Application... (attempt $i/30)"
  sleep 2
done

# Step 2: Verify app is synced to managed cluster
echo ""
echo "Step 2: Verifying application is synced to managed cluster..."
APP_NAME="guestbook"
APP_NAMESPACE="cluster1"
for i in {1..60}; do
  if kubectl get application ${APP_NAME} -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} &>/dev/null; then
    echo "  ✓ Application synced to managed cluster (attempt $i/60)"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: Application not synced to managed cluster after 60 attempts"
    echo "  This indicates the ArgoCD agent failed to sync"
    exit 1
  fi
  sleep 2
done

# Step 3: Verify app status on managed cluster
echo ""
echo "Step 3: Verifying application status on managed cluster..."
for i in {1..60}; do
  MANAGED_HEALTH=$(kubectl get application ${APP_NAME} -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} -o jsonpath='{.status.health.status}' 2>/dev/null || echo "Unknown")
  MANAGED_SYNC=$(kubectl get application ${APP_NAME} -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "Unknown")
  
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

# Step 4: Verify app status on hub matches managed (with retry for status propagation)
echo ""
echo "Step 4: Verifying application status on hub..."
for i in {1..30}; do
  HUB_HEALTH=$(kubectl get application ${APP_NAME} -n ${APP_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.health.status}' 2>/dev/null || echo "Unknown")
  HUB_SYNC=$(kubectl get application ${APP_NAME} -n ${APP_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "Unknown")

  echo "  Hub cluster - Health: ${HUB_HEALTH}, Sync: ${HUB_SYNC} (attempt $i/30)"

  if [ "${HUB_HEALTH}" == "${MANAGED_HEALTH}" ] && [ "${HUB_SYNC}" == "${MANAGED_SYNC}" ]; then
    echo "  ✓ Application status on hub matches managed cluster"
    break
  fi

  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: Application status mismatch between hub and managed after 30 attempts"
    echo "    Hub: Health=${HUB_HEALTH}, Sync=${HUB_SYNC}"
    echo "    Managed: Health=${MANAGED_HEALTH}, Sync=${MANAGED_SYNC}"
    exit 1
  fi
  sleep 2
done

echo ""
echo "========================================="
echo "✓ E2E GITOPSADDON AGENT FULL TEST PASSED"
echo "========================================="
echo ""
echo "Summary:"
echo "  - GitOpsAddon deployed with ArgoCD agent enabled"
echo "  - Dynamic AddOnTemplate created"
echo "  - Certificates generated (CA, principal TLS)"
echo "  - Application synced from hub to managed cluster"
echo "  - Application status propagated back to hub"
echo "  - Managed cluster status: Health=${MANAGED_HEALTH}, Sync=${MANAGED_SYNC}"
echo "  - Hub cluster status: Health=${HUB_HEALTH}, Sync=${HUB_SYNC}"
