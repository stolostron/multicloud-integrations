#!/bin/bash
# e2e-deploy.sh - Deploy and verify GitOps addon
# This is for CI - assumes kind clusters and images already exist
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"

echo "========================================="
echo "E2E DEPLOY TEST - Verifying Deployment"
echo "========================================="

# Step 1: Verify controller is running
echo ""
echo "Step 1: Verifying GitOps controller is running..."
kubectl --context ${HUB_CONTEXT} wait --for=condition=available --timeout=300s \
  deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE}
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

# Step 2.5: Verify GitOpsCluster conditions
echo ""
echo "Step 2.5: Verifying GitOpsCluster conditions..."
READY=$(kubectl --context ${HUB_CONTEXT} get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
PLACEMENT=$(kubectl --context ${HUB_CONTEXT} get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} -o jsonpath='{.status.conditions[?(@.type=="PlacementResolved")].status}')
ARGOSERVER=$(kubectl --context ${HUB_CONTEXT} get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} -o jsonpath='{.status.conditions[?(@.type=="ArgoServerVerified")].status}')
CLUSTERS=$(kubectl --context ${HUB_CONTEXT} get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} -o jsonpath='{.status.conditions[?(@.type=="ClustersRegistered")].status}')

echo "  Core Conditions:"
echo "    Ready: ${READY}"
echo "    PlacementResolved: ${PLACEMENT}"
echo "    ArgoServerVerified: ${ARGOSERVER}"
echo "    ClustersRegistered: ${CLUSTERS}"

if [ "${READY}" != "True" ]; then
  echo "  ✗ ERROR: Ready condition is not True"
  kubectl --context ${HUB_CONTEXT} get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} -o yaml
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

echo "  ✓ All core conditions are True"

# Step 3: Verify CA certificates exist (only when ArgoCD agent is enabled)
echo ""
echo "Step 3: Verifying certificates..."
ARGOCD_AGENT_ENABLED=$(kubectl --context ${HUB_CONTEXT} get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} -o jsonpath='{.spec.gitopsAddon.argoCDAgent.enabled}' 2>/dev/null || echo "false")
if [ "${ARGOCD_AGENT_ENABLED}" == "true" ]; then
  kubectl --context ${HUB_CONTEXT} get secret argocd-agent-ca -n ${GITOPS_NAMESPACE}
  kubectl --context ${HUB_CONTEXT} get secret argocd-agent-principal-tls -n ${GITOPS_NAMESPACE}
  echo "✓ Certificates exist"
else
  echo "  ArgoCD agent disabled, skipping certificate check"
fi

# Step 4: Verify AddOnTemplate exists (dynamic template only exists when ArgoCD agent is enabled)
echo ""
echo "Step 4: Verifying AddOnTemplate..."
if [ "${ARGOCD_AGENT_ENABLED}" == "true" ]; then
  kubectl --context ${HUB_CONTEXT} get addontemplate gitops-addon-openshift-gitops-gitopscluster
  echo "✓ Dynamic AddOnTemplate exists"
else
  kubectl --context ${HUB_CONTEXT} get addontemplate gitops-addon
  echo "✓ Default AddOnTemplate exists"
fi

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
  echo "✗ ERROR: No addon pods found"
  echo "  Checking ManifestWorks..."
  kubectl --context ${HUB_CONTEXT} get manifestwork -n cluster1
  exit 1
fi

# Step 7: Check if ArgoCD components are deploying on managed cluster
echo ""
echo "Step 7: Checking ArgoCD components on managed cluster..."
for i in {1..30}; do
  ARGOCD_PODS=$(kubectl --context ${SPOKE_CONTEXT} get pods -n ${GITOPS_NAMESPACE} --no-headers 2>/dev/null | wc -l)
  if [ "$ARGOCD_PODS" -gt 0 ]; then
    echo "✓ Found $ARGOCD_PODS pod(s) in ${GITOPS_NAMESPACE} namespace"
    kubectl --context ${SPOKE_CONTEXT} get pods -n ${GITOPS_NAMESPACE}
    break
  fi
  echo "  Waiting for pods in ${GITOPS_NAMESPACE}... (attempt $i/30)"
  sleep 10
done

# Step 8: Verify ArgoCD CR exists on managed cluster
echo ""
echo "Step 8: Verifying ArgoCD CR on managed cluster..."
if kubectl --context ${SPOKE_CONTEXT} get argocd acm-openshift-gitops -n ${GITOPS_NAMESPACE} &>/dev/null; then
  echo "✓ ArgoCD CR 'acm-openshift-gitops' exists"
  ARGOCD_PHASE=$(kubectl --context ${SPOKE_CONTEXT} get argocd acm-openshift-gitops -n ${GITOPS_NAMESPACE} -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
  echo "  ArgoCD Phase: ${ARGOCD_PHASE}"
else
  echo "✗ ArgoCD CR 'acm-openshift-gitops' not found"
  exit 1
fi

# Step 9: Verify agent pods are running (only when ArgoCD agent is enabled)
echo ""
echo "Step 9: Verifying agent pods..."
if [ "${ARGOCD_AGENT_ENABLED}" == "true" ]; then
  # Wait for principal deployment rollout (handles pod updates during reconciliation)
  if kubectl --context ${HUB_CONTEXT} get deployment openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} &>/dev/null; then
    kubectl --context ${HUB_CONTEXT} rollout status deployment/openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --timeout=120s || {
      echo "✗ Principal deployment rollout failed"
      kubectl --context ${HUB_CONTEXT} get pods -n ${GITOPS_NAMESPACE}
      exit 1
    }
    echo "✓ Principal pods are ready on hub"
  else
    # Fallback to pod wait if deployment doesn't exist
    kubectl --context ${HUB_CONTEXT} wait --for=condition=Ready --timeout=120s \
      pod -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} || {
      echo "✗ Principal pods not ready"
      kubectl --context ${HUB_CONTEXT} get pods -n ${GITOPS_NAMESPACE}
      exit 1
    }
    echo "✓ Principal pods are ready on hub"
  fi

  # Wait for agent deployment on managed cluster (operator creates acm-openshift-gitops-agent-agent)
  # Note: Agent image is configured via ARGOCD_AGENT_IMAGE_OVERRIDE env var in controller deployment
  for i in {1..30}; do
    if kubectl --context ${SPOKE_CONTEXT} get deployment acm-openshift-gitops-agent-agent -n ${GITOPS_NAMESPACE} &>/dev/null; then
      kubectl --context ${SPOKE_CONTEXT} rollout status deployment/acm-openshift-gitops-agent-agent -n ${GITOPS_NAMESPACE} --timeout=120s || {
        echo "✗ Agent deployment rollout failed"
        kubectl --context ${SPOKE_CONTEXT} get pods -n ${GITOPS_NAMESPACE}
        kubectl --context ${SPOKE_CONTEXT} describe deployment acm-openshift-gitops-agent-agent -n ${GITOPS_NAMESPACE}
        exit 1
      }
      echo "✓ Agent pods are ready on managed cluster"
      break
    fi
    if [ $i -eq 30 ]; then
      echo "✗ Agent deployment not found after 30 attempts"
      echo "  Note: Agent deployment is only created when argoCDAgent.enabled=true in GitOpsCluster"
      echo "  Checking ArgoCD CR on managed cluster..."
      kubectl --context ${SPOKE_CONTEXT} get argocd acm-openshift-gitops -n ${GITOPS_NAMESPACE} -o yaml
      kubectl --context ${SPOKE_CONTEXT} get pods -n ${GITOPS_NAMESPACE}
      exit 1
    fi
    echo "  Waiting for agent deployment... (attempt $i/30)"
    sleep 5
  done
else
  echo "  ArgoCD agent disabled, skipping agent pod checks"
fi

echo ""
echo "========================================="
echo "✓ E2E DEPLOY TEST PASSED"
echo "========================================="
