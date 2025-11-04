#!/bin/bash
# e2e-gitopsaddon.sh - GitOps addon test for CI (assumes clusters/images ready, no app sync)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
GITOPS_NAMESPACE="openshift-gitops"

echo "========================================="
echo "E2E GITOPS ADDON TEST (CI mode)"
echo "========================================="

# Run setup (assumes clusters already exist)
echo ""
echo "Running setup..."
./test/e2e/scripts/e2e-setup.sh

# Run deploy verification
echo ""
echo "Running deploy verification..."
./test/e2e/scripts/e2e-deploy.sh

echo ""
echo "========================================="
echo "✓ E2E GITOPS ADDON TEST PASSED"
echo "========================================="

