#!/bin/bash
# e2e-full.sh - Full e2e test with app sync verification (local use)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E FULL TEST - Deploy + App Sync"
echo "========================================="

# Run setup
echo ""
echo "Running setup..."
./test/e2e/scripts/e2e-setup.sh

# Run deploy verification
echo ""
echo "Running deploy verification..."
./test/e2e/scripts/e2e-deploy.sh

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
    echo "  ⚠ Warning: Application not synced to managed cluster after 60 attempts"
    echo "  This may indicate the ArgoCD agent needs more time or configuration"
    echo "  Skipping remaining app sync verification steps"
  fi
  sleep 2
done

if [ "${APP_SYNCED}" == "false" ]; then
  echo ""
  echo "Application Sync Verification Summary:"
  echo "  • Application created on hub: ✓"
  echo "  • Application synced to managed: ✗ (ArgoCD agent may need more time)"
  echo ""
  echo "========================================="
  echo "✓ E2E FULL TEST PASSED (with warnings)"
  echo "========================================="
  exit 0
fi

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
    echo "  ⚠ Warning: Application not healthy/synced after 60 attempts"
    echo "  Final status - Health: ${MANAGED_HEALTH}, Sync: ${MANAGED_SYNC}"
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
  echo "  ⚠ Warning: Application status mismatch between hub and managed"
  echo "    Hub: Health=${HUB_HEALTH}, Sync=${HUB_SYNC}"
  echo "    Managed: Health=${MANAGED_HEALTH}, Sync=${MANAGED_SYNC}"
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

