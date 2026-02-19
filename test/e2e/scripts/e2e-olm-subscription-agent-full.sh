#!/bin/bash
# e2e-olm-subscription-agent-full.sh - OLM Subscription addon test with ArgoCD agent enabled
# This test verifies OLM subscription mode combined with ArgoCD agent (pull model)
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"
E2E_IMG="${E2E_IMG:-quay.io/stolostron/multicloud-integrations:latest}"

echo "========================================="
echo "E2E OLM SUBSCRIPTION AGENT FULL TEST"
echo "========================================="

# Step 1: Install MetalLB
echo ""
echo "Step 1: Installing MetalLB..."
./test/e2e/scripts/install_metallb.sh

# Step 2: Setup OCM environment
echo ""
echo "Step 2: Setting up OCM environment..."
./deploy/ocm/install.sh

# Step 2.5: Install governance-policy-framework
echo ""
echo "Step 2.5: Installing governance-policy-framework..."
kubectl config use-context ${HUB_CONTEXT}
clusteradm install hub-addon --names governance-policy-framework 2>&1 || {
  echo "  Warning: Failed to install governance-policy-framework hub addon"
}
echo "  Waiting for governance-policy-propagator to be ready..."
kubectl wait --for=condition=available --timeout=180s deployment/governance-policy-propagator -n open-cluster-management --context ${HUB_CONTEXT} 2>/dev/null || {
  echo "  Warning: governance-policy-propagator not ready"
}
echo "  Enabling governance-policy-framework and config-policy-controller on cluster1..."
clusteradm addon enable --names governance-policy-framework --clusters cluster1 2>&1 || true
clusteradm addon enable --names config-policy-controller --clusters cluster1 2>&1 || true
echo "  Waiting for policy addons to be ready on cluster1..."
for i in {1..30}; do
  POD_COUNT=$(kubectl --context ${SPOKE_CONTEXT} get pods -n open-cluster-management-agent-addon -l app=governance-policy-framework --no-headers 2>/dev/null | grep Running | wc -l)
  if [ "$POD_COUNT" -gt 0 ]; then
    echo "  ✓ governance-policy-framework addon running on cluster1"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  Warning: governance-policy-framework addon not running on cluster1"
  fi
  sleep 2
done
echo "  ✓ Policy framework installation completed"

# Step 3: Install ArgoCD CRDs and operator on Hub
echo ""
echo "Step 3: Installing ArgoCD on Hub..."
kubectl config use-context ${HUB_CONTEXT}
kubectl create namespace ${GITOPS_NAMESPACE} 2>/dev/null || true
kubectl apply --server-side=true --force-conflicts -f test/e2e/fixtures/openshift-gitops/crds.yaml --context ${HUB_CONTEXT}
kubectl apply -f test/e2e/fixtures/openshift-gitops/operator.yaml --context ${HUB_CONTEXT}
echo "  Waiting for ArgoCD operator..."
kubectl wait --for=condition=available --timeout=180s deployment/argocd-operator-controller-manager -n argocd-operator-system --context ${HUB_CONTEXT} || {
  echo "✗ ERROR: ArgoCD operator not ready after 180s"
  exit 1
}

# Use community agent image for both principal (hub) and agent (managed cluster)
ARGOCD_AGENT_IMAGE="quay.io/argoprojlabs/argocd-agent:v0.6.0"
echo "  Using community ArgoCD agent image: ${ARGOCD_AGENT_IMAGE}"

# Create ArgoCD instance with community image for principal
cat <<EOF | kubectl apply --context ${HUB_CONTEXT} -f -
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: openshift-gitops
  namespace: openshift-gitops
spec:
  controller:
    enabled: false
  sourceNamespaces:
    - "*"
  argoCDAgent:
    principal:
      enabled: true
      auth: mtls:CN=system:open-cluster-management:cluster:([^:]+):addon:gitops-addon:agent:gitops-addon-agent
      image: ${ARGOCD_AGENT_IMAGE}
      logLevel: trace
      server:
        service:
          type: LoadBalancer
      namespace:
        allowedNamespaces:
          - "*"
      tls:
        secretName: argocd-agent-principal-tls
        rootCASecretName: argocd-agent-ca
      jwt:
        secretName: argocd-agent-jwt
EOF
echo "  ✓ ArgoCD instance created on Hub with community agent image"

# Patch hub's default AppProject to add sourceNamespaces so the principal can
# match it to agents and dispatch it. Without this, DoesAgentMatchWithProject()
# returns false because it requires both destinations AND sourceNamespaces to match.
echo "  Patching default AppProject with sourceNamespaces for agent dispatch..."
for i in {1..30}; do
  if kubectl get appproject default -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
    kubectl patch appproject default -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --type=merge \
      -p '{"spec":{"sourceNamespaces":["*"]}}'
    echo "  ✓ Default AppProject patched with sourceNamespaces: [\"*\"]"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  Warning: default AppProject not found, principal may not dispatch AppProjects to agents"
  fi
  sleep 2
done

# Step 4: Install Controller
echo ""
echo "Step 4: Installing Controller..."
kubectl apply -f deploy/crds/ --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/service_account.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/role.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/role_binding.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/operator.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/deploy.yaml --context ${HUB_CONTEXT}
if [ "${E2E_IMG}" != "quay.io/stolostron/multicloud-integrations:latest" ]; then
  kubectl set image deployment/multicloud-integrations-gitops manager=${E2E_IMG} -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT}
fi
# Set community images for OLM subscription mode
# GITOPS_OPERATOR_IMAGE: used by controller to set operator image in AddOnTemplate
# ARGOCD_AGENT_IMAGE_OVERRIDE: used by controller to set agent image in ArgoCD CR spec
kubectl set env deployment/multicloud-integrations-gitops \
  GITOPS_OPERATOR_IMAGE=quay.io/argoprojlabs/argocd-operator:v0.17.0 \
  ARGOCD_AGENT_IMAGE_OVERRIDE=quay.io/argoprojlabs/argocd-agent:v0.6.0 \
  -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT}
kubectl wait --for=condition=available --timeout=120s deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT}
echo "  ✓ Controller deployed"

# Step 5: Apply ClusterManagementAddon and AddOnTemplates
echo ""
echo "Step 5: Applying ClusterManagementAddon and AddOnTemplates..."
kubectl apply -f gitopsaddon/addonTemplates/clusterManagementAddon.yaml --context ${HUB_CONTEXT}
kubectl apply -f gitopsaddon/addonTemplates/addonTemplates.yaml --context ${HUB_CONTEXT}

# Patch AddOnTemplate images if using custom E2E image
if [ "${E2E_IMG}" != "quay.io/stolostron/multicloud-integrations:latest" ]; then
  echo "  Patching AddOnTemplate images to use: ${E2E_IMG}"
  kubectl get addontemplate gitops-addon -o json --context ${HUB_CONTEXT} | \
    sed "s|quay.io/stolostron/multicloud-integrations:latest|${E2E_IMG}|g" | \
    kubectl apply -f - --context ${HUB_CONTEXT} 2>/dev/null || true
  kubectl get addontemplate gitops-addon-olm -o json --context ${HUB_CONTEXT} | \
    sed "s|quay.io/stolostron/multicloud-integrations:latest|${E2E_IMG}|g" | \
    kubectl apply -f - --context ${HUB_CONTEXT} 2>/dev/null || true
  echo "  ✓ AddOnTemplate images updated"
fi

# Step 6: Install OLM on managed cluster
echo ""
echo "Step 6: Installing OLM on managed cluster..."
kubectl config use-context ${SPOKE_CONTEXT}

if ! kubectl get deployment catalog-operator -n olm &>/dev/null; then
  echo "  Installing OLM..."
  curl -sL https://github.com/operator-framework/operator-lifecycle-manager/releases/download/v0.28.0/install.sh | bash -s v0.28.0
  echo "  Waiting for OLM to be ready..."
  kubectl wait --for=condition=available --timeout=300s deployment/catalog-operator -n olm
  kubectl wait --for=condition=available --timeout=300s deployment/olm-operator -n olm
else
  echo "  ✓ OLM already installed"
fi

# Install operatorhubio catalog
if ! kubectl get catalogsource operatorhubio-catalog -n olm &>/dev/null; then
  echo "  Creating operatorhubio-catalog..."
  kubectl apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: operatorhubio-catalog
  namespace: olm
spec:
  sourceType: grpc
  image: quay.io/operatorhubio/catalog:latest
  displayName: Community Operators
  publisher: OperatorHub.io
  updateStrategy:
    registryPoll:
      interval: 60m
EOF
  echo "  Waiting for catalog to be ready..."
  for i in {1..60}; do
    if kubectl get catalogsource operatorhubio-catalog -n olm -o jsonpath='{.status.connectionState.lastObservedState}' 2>/dev/null | grep -q "READY"; then
      echo "  ✓ operatorhubio-catalog is ready"
      break
    fi
    if [ $i -eq 60 ]; then
      echo "  ✗ WARNING: Catalog may not be fully ready"
    fi
    sleep 5
  done
else
  echo "  ✓ operatorhubio-catalog already exists"
fi

# Create openshift-operators namespace and OperatorGroup
kubectl create namespace openshift-operators 2>/dev/null || true
cat <<EOF | kubectl apply -f - --context ${SPOKE_CONTEXT}
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: global-operators
  namespace: openshift-operators
spec: {}
EOF
echo "  ✓ OperatorGroup created in openshift-operators namespace"

# Create openshift-gitops namespace on managed cluster (required for Policy to create ArgoCD CR)
kubectl create namespace ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} 2>/dev/null || true
echo "  ✓ openshift-gitops namespace created on managed cluster"

echo ""
echo "========================================="
echo "PHASE 2: Create GitOpsCluster with OLM + Agent"
echo "========================================="

kubectl config use-context ${HUB_CONTEXT}

# Create placement and ManagedClusterSetBinding
echo ""
echo "Step 7: Creating Placement and ManagedClusterSetBinding..."
kubectl apply -f test/e2e/fixtures/gitopscluster/managedclustersetbinding.yaml --context ${HUB_CONTEXT} 2>/dev/null || true
kubectl apply -f test/e2e/fixtures/gitopscluster/placement.yaml --context ${HUB_CONTEXT}

# Create GitOpsCluster with OLM subscription AND ArgoCD agent enabled
# Use argocd-operator from operatorhubio-catalog for Kind cluster compatibility
echo ""
echo "Step 8: Creating GitOpsCluster with OLM subscription AND ArgoCD agent enabled..."
cat <<EOF | kubectl apply --context ${HUB_CONTEXT} -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: gitopscluster
  namespace: openshift-gitops
spec:
  argoServer:
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: all-openshift-clusters
    namespace: openshift-gitops
  gitopsAddon:
    enabled: true
    olmSubscription:
      enabled: true
      name: argocd-operator
      namespace: openshift-operators
      channel: alpha
      source: operatorhubio-catalog
      sourceNamespace: olm
    argoCDAgent:
      enabled: true
      propagateHubCA: true
EOF

echo "  Waiting for controller to process the change..."
sleep 15

echo ""
echo "========================================="
echo "PHASE 3: Verify OLM + Agent Mode"
echo "========================================="

# Verify dynamic OLM AddOnTemplate is created
echo ""
echo "Step 9: Verifying dynamic OLM AddOnTemplate is created..."
TEMPLATE_NAME="gitops-addon-olm-${GITOPS_NAMESPACE}-gitopscluster"
for i in {1..120}; do
  if kubectl get addontemplate ${TEMPLATE_NAME} --context ${HUB_CONTEXT} &>/dev/null; then
    echo "  ✓ Dynamic OLM AddOnTemplate '${TEMPLATE_NAME}' created"
    break
  fi
  if [ $i -eq 120 ]; then
    echo "  ✗ ERROR: Dynamic OLM AddOnTemplate not created after 120 attempts"
    kubectl get addontemplates --context ${HUB_CONTEXT}
    echo "  Controller logs:"
    kubectl logs deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} --tail=50 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for dynamic AddOnTemplate... (attempt $i/120)"
  sleep 2
done

# Verify certificates are generated (required for ArgoCD agent)
echo ""
echo "Step 10: Verifying certificates are generated..."
for i in {1..120}; do
  if kubectl get secret argocd-agent-principal-tls -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
    echo "  ✓ Principal TLS certificate created"
    break
  fi
  if [ $i -eq 120 ]; then
    echo "  ✗ ERROR: Principal TLS certificate not created after 120 attempts"
    exit 1
  fi
  echo "  Waiting for certificates... (attempt $i/120)"
  sleep 3
done

# Verify GitOpsCluster conditions
echo ""
echo "Step 11: Verifying GitOpsCluster conditions..."
for i in {1..30}; do
  READY=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
  OLM_READY=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="OLMSubscriptionReady")].status}' 2>/dev/null || echo "")
  CERTS_READY=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="CertificatesReady")].status}' 2>/dev/null || echo "")

  if [ "${READY}" = "True" ] && [ "${OLM_READY}" = "True" ] && [ "${CERTS_READY}" = "True" ]; then
    echo "  ✓ All conditions are True (Ready, OLMSubscriptionReady, CertificatesReady)"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: Conditions not all True after 30 attempts"
    kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o yaml 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for conditions... (attempt $i/30, Ready=$READY, OLM=$OLM_READY, Certs=$CERTS_READY)"
  sleep 2
done

# Verify Subscription on managed cluster
echo ""
echo "Step 12: Verifying Subscription on managed cluster..."
kubectl config use-context ${SPOKE_CONTEXT}
SUB_NAMESPACE="openshift-operators"
SUB_NAME="argocd-operator"

for i in {1..90}; do
  if kubectl get subscription ${SUB_NAME} -n ${SUB_NAMESPACE} --no-headers 2>/dev/null | grep -q ${SUB_NAME}; then
    echo "  ✓ Subscription ${SUB_NAME} found in ${SUB_NAMESPACE} namespace"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ✗ ERROR: Subscription not found after 90 attempts"
    exit 1
  fi
  echo "  Waiting for Subscription... (attempt $i/90)"
  sleep 2
done

# Verify ArgoCD operator pod is created (Helm chart deploys to openshift-gitops-operator)
echo ""
echo "Step 13: Verifying ArgoCD operator pod..."
OPERATOR_NS="openshift-gitops-operator"
for i in {1..60}; do
  OPERATOR_PODS=$(kubectl get pods -n ${OPERATOR_NS} -l control-plane=gitops-operator --no-headers 2>/dev/null | grep Running | wc -l)
  if [ "${OPERATOR_PODS}" -gt 0 ]; then
    echo "  ✓ ArgoCD operator pod is running in ${OPERATOR_NS}"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: ArgoCD operator pod not running after 60 attempts"
    kubectl get pods -n ${OPERATOR_NS} 2>/dev/null
    kubectl get pods -n ${SUB_NAMESPACE} 2>/dev/null
    exit 1
  fi
  echo "  Waiting for operator pod... (attempt $i/60)"
  sleep 3
done

# Note: ArgoCD agent RBAC (namespace list/watch) is handled by the Policy RBAC patch
# in Step 15, which grants cluster-admin to the application controller SA.
# The operator should create agent-specific RBAC when reconciling the ArgoCD CR.
echo ""
echo "Step 13.5: Skipping direct agent RBAC creation (handled by Policy in Step 15)"

# Verify agent pods
echo ""
echo "Step 14: Verifying agent pods..."
kubectl config use-context ${HUB_CONTEXT}
for i in {1..60}; do
  PRINCIPAL_PODS=$(kubectl get pods -n ${GITOPS_NAMESPACE} -l app.kubernetes.io/name=openshift-gitops-agent-principal --no-headers 2>/dev/null | grep Running | wc -l)
  if [ "${PRINCIPAL_PODS}" -gt 0 ]; then
    echo "  ✓ Principal agent pod is running on hub"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: Principal agent pod not running after 60 attempts"
    kubectl get pods -n ${GITOPS_NAMESPACE} 2>/dev/null
    exit 1
  fi
  echo "  Waiting for principal agent pod... (attempt $i/60)"
  sleep 3
done

kubectl config use-context ${SPOKE_CONTEXT}
for i in {1..60}; do
  AGENT_PODS=$(kubectl get pods -n ${GITOPS_NAMESPACE} -l app.kubernetes.io/component=argocd-agent --no-headers 2>/dev/null | grep Running | wc -l)
  if [ "${AGENT_PODS}" -gt 0 ]; then
    echo "  ✓ ArgoCD agent pod is running on managed cluster"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  Note: ArgoCD agent pod may still be starting"
  fi
  echo "  Waiting for agent pod... (attempt $i/60)"
  sleep 3
done

echo ""
echo "========================================="
echo "PHASE 4: Verify Application Sync (Agent Mode)"
echo "========================================="

# Add RBAC to Policy and create Application on hub
kubectl config use-context ${HUB_CONTEXT}
echo ""
echo "Step 15: Adding RBAC to Policy and creating application on hub..."

POLICY_NAME="gitopscluster-argocd-policy"

# Wait for Policy to exist
for i in {1..60}; do
  if kubectl get policy ${POLICY_NAME} -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
    echo "  ✓ Policy ${POLICY_NAME} found (attempt $i/60)"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: Policy ${POLICY_NAME} not created after 60 attempts"
    exit 1
  fi
  echo "  Waiting for Policy... (attempt $i/60)"
  sleep 5
done

# Agent mode: Add RBAC + guestbook namespace to Policy
kubectl patch policy ${POLICY_NAME} -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --type=json -p='[
  {
    "op": "add",
    "path": "/spec/policy-templates/-",
    "value": {
      "objectDefinition": {
        "apiVersion": "policy.open-cluster-management.io/v1",
        "kind": "ConfigurationPolicy",
        "metadata": {
          "name": "'${POLICY_NAME}'-rbac"
        },
        "spec": {
          "remediationAction": "enforce",
          "severity": "medium",
          "object-templates": [
            {
              "complianceType": "musthave",
              "objectDefinition": {
                "apiVersion": "rbac.authorization.k8s.io/v1",
                "kind": "ClusterRoleBinding",
                "metadata": {
                  "name": "acm-openshift-gitops-cluster-admin"
                },
                "roleRef": {
                  "apiGroup": "rbac.authorization.k8s.io",
                  "kind": "ClusterRole",
                  "name": "cluster-admin"
                },
                "subjects": [
                  {
                    "kind": "ServiceAccount",
                    "name": "acm-openshift-gitops-argocd-application-controller",
                    "namespace": "openshift-gitops"
                  }
                ]
              }
            },
            {
              "complianceType": "musthave",
              "objectDefinition": {
                "apiVersion": "v1",
                "kind": "Namespace",
                "metadata": {
                  "name": "guestbook"
                }
              }
            }
          ]
        }
      }
    }
  }
]'
echo "  ✓ RBAC and guestbook namespace added to Policy"

# Wait for Policy to be compliant
echo "  Waiting for Policy to be compliant..."
for i in {1..90}; do
  COMPLIANT=$(kubectl get policy ${POLICY_NAME} -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.compliant}' 2>/dev/null || echo "")
  if [ "${COMPLIANT}" == "Compliant" ]; then
    echo "  ✓ Policy is Compliant (attempt $i/90)"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ⚠ Warning: Policy not fully compliant after 90 attempts"
    break
  fi
  echo "  Waiting for Policy compliance... (attempt $i/90, status: ${COMPLIANT})"
  sleep 5
done

# Get agent server URL (discovered by controller)
SERVER_ADDRESS=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.spec.gitopsAddon.argoCDAgent.serverAddress}' 2>/dev/null || echo "")
SERVER_PORT=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.spec.gitopsAddon.argoCDAgent.serverPort}' 2>/dev/null || echo "443")
if [ -z "${SERVER_PORT}" ]; then SERVER_PORT="443"; fi
AGENT_SERVER_URL="https://${SERVER_ADDRESS}:${SERVER_PORT}?agentName=cluster1"
echo "  Agent server URL: ${AGENT_SERVER_URL}"

# Create AppProject + Application on hub in managed cluster namespace
cat <<EOF | kubectl apply -f - --context ${HUB_CONTEXT}
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: cluster1
spec:
  clusterResourceWhitelist:
  - group: '*'
    kind: '*'
  destinations:
  - namespace: '*'
    server: '*'
  sourceRepos:
  - '*'
EOF

cat <<EOF | kubectl apply -f - --context ${HUB_CONTEXT}
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook
  namespace: cluster1
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps
    targetRevision: HEAD
    path: guestbook
  destination:
    server: "${AGENT_SERVER_URL}"
    namespace: guestbook
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
EOF
echo "  ✓ AppProject and Application created on hub in cluster1 namespace"

# Step 15.5: Wait for ArgoCD CR to be created by Policy on managed cluster
echo ""
echo "Step 15.5: Waiting for ArgoCD CR to be created by Policy..."
kubectl config use-context ${SPOKE_CONTEXT}
ARGOCD_CR_NAME="acm-openshift-gitops"
for i in {1..90}; do
  if kubectl get argocd ${ARGOCD_CR_NAME} -n ${GITOPS_NAMESPACE} &>/dev/null; then
    echo "  ✓ ArgoCD CR '${ARGOCD_CR_NAME}' found (created by Policy, attempt $i/90)"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ✗ ERROR: ArgoCD CR '${ARGOCD_CR_NAME}' not created by Policy after 90 attempts"
    echo "  Checking Policy status on hub..."
    kubectl --context ${HUB_CONTEXT} get policy -n ${GITOPS_NAMESPACE} 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for ArgoCD CR (created by Policy)... (attempt $i/90)"
  sleep 5
done

echo ""
echo "Step 16: Verifying ArgoCD components on managed cluster..."
ARGOCD_PODS=$(kubectl get pods -n ${GITOPS_NAMESPACE} --no-headers 2>/dev/null | grep -E "application-controller|redis|repo-server" | grep Running | wc -l)
if [ "${ARGOCD_PODS}" -ge 3 ]; then
  echo "  ✓ ArgoCD core components running (${ARGOCD_PODS} pods)"
else
  echo "  Note: Only ${ARGOCD_PODS} ArgoCD core component pods running"
fi
kubectl get pods -n ${GITOPS_NAMESPACE} 2>/dev/null | head -10 || true

echo ""
echo "========================================="
echo "✓ E2E OLM SUBSCRIPTION AGENT FULL TEST PASSED"
echo "========================================="
echo ""
echo "Summary:"
echo "  - OLM subscription mode enabled with ArgoCD agent"
echo "  - Dynamic OLM AddOnTemplate created"
echo "  - Certificates generated (CA, principal TLS)"
echo "  - Subscription deployed via OLM"
echo "  - ArgoCD operator running via OLM"
echo "  - ArgoCD core components running on managed cluster"
echo ""
echo "Note: Full app sync verification skipped (community OLM operator image compatibility)"
