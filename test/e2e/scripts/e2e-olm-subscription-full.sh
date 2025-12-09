#!/bin/bash
# e2e-olm-subscription-full.sh - OLM Subscription addon test for local development
# Creates clusters, builds images, and runs the OLM subscription e2e test
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

HUB_CLUSTER="hub"
SPOKE_CLUSTER="cluster1"
E2E_IMG="${E2E_IMG:-quay.io/stolostron/multicloud-integrations:latest}"
KIND="${KIND:-kind}"

echo "========================================="
echo "E2E OLM SUBSCRIPTION FULL TEST (Local)"
echo "========================================="
echo "Project root: ${PROJECT_ROOT}"
echo "Image: ${E2E_IMG}"
echo ""

cd "${PROJECT_ROOT}"

# Check if kind clusters already exist
echo "Step 1: Checking Kind clusters..."
HUB_EXISTS=$(${KIND} get clusters 2>/dev/null | grep -c "^${HUB_CLUSTER}$" || echo "0")
SPOKE_EXISTS=$(${KIND} get clusters 2>/dev/null | grep -c "^${SPOKE_CLUSTER}$" || echo "0")

if [ "$HUB_EXISTS" = "0" ]; then
  echo "  Creating ${HUB_CLUSTER} cluster..."
  ${KIND} create cluster --name ${HUB_CLUSTER}
else
  echo "  ✓ ${HUB_CLUSTER} cluster already exists"
fi

if [ "$SPOKE_EXISTS" = "0" ]; then
  echo "  Creating ${SPOKE_CLUSTER} cluster..."
  ${KIND} create cluster --name ${SPOKE_CLUSTER}
else
  echo "  ✓ ${SPOKE_CLUSTER} cluster already exists"
fi

# Build images
echo ""
echo "Step 2: Building images..."
make build-images IMAGE_NAME_AND_VERSION=${E2E_IMG}

# Load images into clusters
echo ""
echo "Step 3: Loading images into Kind clusters..."
${KIND} load docker-image ${E2E_IMG} --name ${HUB_CLUSTER}
${KIND} load docker-image ${E2E_IMG} --name ${SPOKE_CLUSTER}

# Run the CI e2e test
echo ""
echo "Step 4: Running OLM subscription e2e test..."
E2E_IMG=${E2E_IMG} ./test/e2e/scripts/e2e-olm-subscription.sh

echo ""
echo "========================================="
echo "✓ E2E OLM SUBSCRIPTION FULL TEST COMPLETE"
echo "========================================="
echo ""
echo "Logs saved to: /tmp/e2e-olm-subscription-full.log"

