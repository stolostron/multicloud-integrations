#!/bin/bash
# e2e-setup.sh - Setup environment for e2e tests
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
E2E_IMG="${E2E_IMG:-quay.io/stolostron/multicloud-integrations:latest}"

echo "========================================="
echo "E2E SETUP - Installing Components"
echo "========================================="

# Step 1: Install MetalLB
echo ""
echo "Step 1: Installing MetalLB..."
./test/e2e/scripts/install_metallb.sh

# Step 2: Setup OCM environment
echo ""
echo "Step 2: Setting up OCM environment..."
./deploy/ocm/install.sh

# Step 3: Install ArgoCD on Hub
echo ""
echo "Step 3: Installing ArgoCD on Hub..."
kubectl config use-context ${HUB_CONTEXT}
kubectl create namespace ${GITOPS_NAMESPACE} || true
kubectl apply --server-side=true --force-conflicts -f test/e2e/fixtures/openshift-gitops/crds.yaml --context ${HUB_CONTEXT}
kubectl apply -f test/e2e/fixtures/openshift-gitops/operator.yaml --context ${HUB_CONTEXT}
echo "  Waiting for ArgoCD operator..."
kubectl wait --for=condition=available --timeout=180s deployment/argocd-operator-controller-manager -n argocd-operator-system --context ${HUB_CONTEXT} || {
  echo "✗ ERROR: ArgoCD operator not ready after 180s"
  exit 1
}
kubectl create -f test/e2e/fixtures/openshift-gitops/operator-instance.yaml --context "${HUB_CONTEXT}" --save-config \
  || kubectl apply -f test/e2e/fixtures/openshift-gitops/operator-instance.yaml --context "${HUB_CONTEXT}"
echo "  ArgoCD instance created (principal pods will be ready after GitOpsCluster creates secrets)"


# Step 4: Install Controller and GitOpsCluster
echo ""
echo "Step 4: Installing Controller..."
git checkout HEAD -- deploy/crds/apps.open-cluster-management.io_gitopsclusters.yaml 2>/dev/null || true
kubectl apply -f deploy/crds/ --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/service_account.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/role.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/role_binding.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/leader_election_role.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/leader_election_role_binding.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/operator.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/deploy.yaml --context ${HUB_CONTEXT}
if [ "${E2E_IMG}" != "quay.io/stolostron/multicloud-integrations:latest" ]; then
  kubectl set image deployment/multicloud-integrations-gitops manager=${E2E_IMG} -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT}
fi
echo "  Waiting for controller to be ready..."
kubectl wait --for=condition=available --timeout=180s deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} || {
  echo "✗ ERROR: Controller not ready after 180s"
  exit 1
}

# Step 5: Create GitOpsCluster
echo ""
# Step 4.5: Apply ClusterManagementAddon
echo ""
echo "Step 4.5: Applying ClusterManagementAddon..."
kubectl apply -f gitopsaddon/addonTemplates/clusterManagementAddon.yaml --context ${HUB_CONTEXT}

echo "Step 5: Creating GitOpsCluster..."
kubectl apply -f test/e2e/fixtures/gitopscluster/managedclustersetbinding.yaml --context ${HUB_CONTEXT} || true
kubectl apply -f test/e2e/fixtures/gitopscluster/placement.yaml --context ${HUB_CONTEXT}
kubectl apply -f test/e2e/fixtures/gitopscluster/gitopscluster.yaml --context ${HUB_CONTEXT}
echo "  Waiting for GitOpsCluster to be created..."
kubectl wait --for=jsonpath='{.metadata.name}'=gitopscluster --timeout=60s gitopscluster/gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} 2>/dev/null || echo "GitOpsCluster created"

echo "  Waiting for GitOpsCluster controller to create secrets..."
for i in {1..60}; do
  if kubectl get secret argocd-agent-resource-proxy-tls -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
    echo "  ✓ Secret argocd-agent-resource-proxy-tls created"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "✗ ERROR: Secret argocd-agent-resource-proxy-tls not created after 120s"
    kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o yaml
    exit 1
  fi
  sleep 2
done

echo "  Restarting ArgoCD principal pods to pick up secrets..."
kubectl delete pod -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} || true
echo "  Waiting for principal pods to be recreated and ready..."
for i in {1..60}; do
  POD_COUNT=$(kubectl get pods -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --no-headers 2>/dev/null | grep -v Terminating | wc -l)
  if [ "$POD_COUNT" -gt 0 ]; then
    echo "  ✓ Found $POD_COUNT principal pod(s) running"
    if kubectl wait --for=condition=Ready --timeout=10s pod -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} 2>/dev/null; then
      echo "  ✓ Principal pods are ready"
      break
    fi
  fi
  if [ $i -eq 60 ]; then
    echo "✗ ERROR: Principal pods not ready after restart"
    kubectl get pods -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT}
    kubectl logs -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --tail=30
    exit 1
  fi
  sleep 2
done

# Step 6: Setup ArgoCD on managed cluster (prerequisite for gitops addon)
echo ""
echo "Step 6: Setting up ArgoCD on managed cluster..."
kubectl apply -f test/e2e/fixtures/argocdexportcrd.yaml --context ${SPOKE_CONTEXT}
echo "  Creating openshift-gitops namespace on managed cluster..."
kubectl create namespace ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} || true
echo "  Creating ArgoCD CR on managed cluster..."
kubectl apply -f test/e2e/fixtures/argocd.yaml --context ${SPOKE_CONTEXT}
sleep 30s

# Step 7: Patch redis and network policy for agent communication
echo ""
echo "Step 7: Patching redis and network policy..."
echo "  Patching redis deployment..."
kubectl patch deployment openshift-gitops-redis -n ${GITOPS_NAMESPACE} --type='json' -p='[
  {
    "op": "add",
    "path": "/spec/template/spec/securityContext",
    "value": {"runAsUser": 1000}
  }
]' --context ${SPOKE_CONTEXT} || echo "Warning: Could not patch redis deployment"
sleep 60s
echo "  Patching network policy..."
kubectl patch networkpolicy openshift-gitops-redis-network-policy -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} --type='json' -p='[{"op": "add", "path": "/spec/ingress/-", "value": {"ports": [{"port": 6379, "protocol": "TCP"}], "from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "argocd-agent-agent"}}}]}}]' || echo "Warning: Could not patch network policy"

# Step 8: Restart deployments to apply patches
echo ""
echo "Step 8: Restarting deployments..."
kubectl rollout restart deployment openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} || echo "Warning: Could not restart principal"
kubectl rollout restart deployment argocd-agent-agent -n ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} || echo "Warning: Could not restart agent (may not exist yet)"
sleep 30s

# Step 9: Deploy test application
echo ""
echo "Step 9: Deploying test application..."
kubectl apply -f test/e2e/fixtures/app.yaml --context ${HUB_CONTEXT}
sleep 30s

echo ""
echo "========================================="
echo "✓ E2E SETUP COMPLETE"
echo "========================================="

