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
echo "Step 4: Verifying GitOpsCluster conditions with ArgoCD agent enabled..."
# Check that ArgoCD agent specific conditions are also True
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

# Verify app sync
echo ""
echo "Step: Verifying application sync..."
echo ""

# Step 1: Deploy a test application on hub
echo "1. Creating application on hub..."
kubectl apply -f test/e2e/fixtures/app.yaml --context ${HUB_CONTEXT}
kubectl wait --for=jsonpath='{.metadata.name}'=guestbook --timeout=60s application/guestbook -n cluster1 --context ${HUB_CONTEXT} 2>/dev/null || true
echo "  ✓ Application created on hub (cluster1 namespace)"

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
  echo "  • Application synced to managed: ✗"
  echo ""
  echo "========================================="
  echo "✗ E2E FULL TEST FAILED"
  echo "========================================="
  exit 1
fi

# Step 3: Verify application is synced to managed cluster
echo ""
echo "3. Verifying application is synced to managed cluster..."
APP_NAME="guestbook"
APP_NAMESPACE="cluster1"
APP_SYNCED=false
for i in {1..60}; do
  if kubectl get application ${APP_NAME} -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} &>/dev/null; then
    echo "  ✓ Application '${APP_NAME}' synced to managed cluster (attempt $i/60)"
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
  echo "  • Application synced to managed: ✗"
  echo ""
  echo "========================================="
  echo "✗ E2E FULL TEST FAILED"
  echo "========================================="
  exit 1
fi

# Step 4: Verify app status on managed cluster
echo ""
echo "4. Verifying application status on managed cluster..."
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

# Step 5: Verify app status on hub matches managed
echo ""
echo "5. Verifying application status on hub..."
HUB_HEALTH=$(kubectl get application ${APP_NAME} -n ${APP_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.health.status}' 2>/dev/null || echo "Unknown")
HUB_SYNC=$(kubectl get application ${APP_NAME} -n ${APP_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "Unknown")

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
echo "========================================="
echo "✓ E2E FULL TEST PASSED"
echo "========================================="
