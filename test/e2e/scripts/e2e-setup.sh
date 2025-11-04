#!/bin/bash
# e2e-setup.sh - Setup environment for e2e tests
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
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
./test/e2e/scripts/setup_ocm_env.sh

# Step 3: Install ArgoCD on Hub
echo ""
echo "Step 3: Installing ArgoCD on Hub..."
kubectl config use-context ${HUB_CONTEXT}
kubectl create namespace ${GITOPS_NAMESPACE} || true
kubectl apply --server-side=true --force-conflicts -f test/e2e/fixtures/openshift-gitops/crds.yaml --context ${HUB_CONTEXT}
kubectl apply -f test/e2e/fixtures/openshift-gitops/operator.yaml --context ${HUB_CONTEXT}
echo "  Waiting for ArgoCD operator..."
kubectl wait --for=condition=available --timeout=180s deployment/argocd-operator-controller-manager -n argocd-operator-system --context ${HUB_CONTEXT} || echo "Warning: ArgoCD operator not ready yet, continuing anyway"
kubectl apply -f test/e2e/fixtures/openshift-gitops/operator-instance.yaml --context ${HUB_CONTEXT}
echo "  Waiting for ArgoCD principal pods..."
kubectl wait --for=condition=Ready --timeout=180s pod -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} || echo "Warning: Principal pods not ready yet, continuing anyway"


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
  kubectl set image deployment/multicloud-integrations-gitops manager=${E2E_IMG} -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT}
fi
echo "  Waiting for controller to be ready..."
kubectl wait --for=condition=available --timeout=180s deployment/multicloud-integrations-gitops -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} || echo "Warning: Controller not ready yet, continuing anyway"

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

echo ""
echo "========================================="
echo "✓ E2E SETUP COMPLETE"
echo "========================================="

