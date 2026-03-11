#!/bin/bash
# Setup script for gitopsaddon e2e tests.
# Assumes Kind clusters (hub, cluster1) already exist with the controller image loaded.

set -euo pipefail

KUBECTL=${KUBECTL:-kubectl}
CLUSTERADM_VERSION=${CLUSTERADM_VERSION:-v1.1.1}
HUB_CLUSTER=${HUB_CLUSTER:-hub}
SPOKE_CLUSTER=${SPOKE_CLUSTER:-cluster1}
HUB_CTX="kind-${HUB_CLUSTER}"
SPOKE_CTX="kind-${SPOKE_CLUSTER}"
E2E_IMG=${E2E_IMG:-quay.io/stolostron/multicloud-integrations:latest}
ARGOCD_OPERATOR_IMAGE=${ARGOCD_OPERATOR_IMAGE:-quay.io/argoprojlabs/argocd-operator:v0.17.0}

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"

echo "============================================"
echo "E2E Setup: OCM + ArgoCD + Controller"
echo "============================================"
echo "Hub context:   ${HUB_CTX}"
echo "Spoke context: ${SPOKE_CTX}"
echo "Image:         ${E2E_IMG}"
echo "Repo:          ${REPO_DIR}"
echo ""

# ------- Step 1: Install clusteradm and setup OCM -------
echo "===== Step 1: Setting up OCM ====="

# Ensure Go bin directory is on PATH so clusteradm is found after install
export PATH="${GOBIN:-${GOPATH:-${HOME}/go}/bin}:${PATH}"

go install open-cluster-management.io/clusteradm/cmd/clusteradm@${CLUSTERADM_VERSION}

echo ">> Init hub"
${KUBECTL} config use-context ${HUB_CTX}
clusteradm init --wait

joincmd=$(clusteradm get token | grep clusteradm)

echo ">> Join ${SPOKE_CLUSTER}"
${KUBECTL} config use-context ${SPOKE_CTX}
eval "$(echo "${joincmd}" --force-internal-endpoint-lookup --wait | sed "s/<cluster_name>/${SPOKE_CLUSTER}/g")"

echo ">> Accept ${SPOKE_CLUSTER}"
${KUBECTL} config use-context ${HUB_CTX}
clusteradm accept --clusters ${SPOKE_CLUSTER} --wait

echo ">> Waiting for ManagedCluster ${SPOKE_CLUSTER} to be available"
${KUBECTL} --context ${HUB_CTX} wait managedcluster ${SPOKE_CLUSTER} --for=condition=ManagedClusterConditionAvailable --timeout=120s

# ------- Step 1.5: Register hub as local-cluster -------
echo "===== Step 1.5: Registering hub as local-cluster ====="
${KUBECTL} config use-context ${HUB_CTX}
if ${KUBECTL} --context ${HUB_CTX} get managedcluster local-cluster &>/dev/null; then
  echo "local-cluster ManagedCluster already exists, skipping registration"
else
  echo ">> Joining hub as local-cluster"
  eval "$(echo "${joincmd}" --force-internal-endpoint-lookup --wait | sed "s/<cluster_name>/local-cluster/g")"

  echo ">> Accepting local-cluster"
  ${KUBECTL} config use-context ${HUB_CTX}
  clusteradm accept --clusters local-cluster --wait
fi

echo ">> Labeling local-cluster"
${KUBECTL} --context ${HUB_CTX} label managedcluster local-cluster local-cluster=true --overwrite

echo ">> Waiting for local-cluster to be available"
${KUBECTL} --context ${HUB_CTX} wait managedcluster local-cluster --for=condition=ManagedClusterConditionAvailable --timeout=120s

# ------- Step 2: Install governance-policy-framework -------
echo "===== Step 2: Installing governance-policy-framework ====="
${KUBECTL} config use-context ${HUB_CTX}
clusteradm install hub-addon --names governance-policy-framework
clusteradm addon enable --names governance-policy-framework --clusters ${SPOKE_CLUSTER}
clusteradm addon enable --names config-policy-controller --clusters ${SPOKE_CLUSTER}
clusteradm addon enable --names governance-policy-framework --clusters local-cluster
clusteradm addon enable --names config-policy-controller --clusters local-cluster

echo ">> Waiting for governance addons to be available"
sleep 10

wait_for_addon() {
  local addon_name=$1
  local cluster_name=$2
  for i in $(seq 1 30); do
    STATUS=$(${KUBECTL} --context ${HUB_CTX} get managedclusteraddon "${addon_name}" -n "${cluster_name}" -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null || echo "NotFound")
    if [ "$STATUS" = "True" ]; then
      echo "${addon_name} addon is Available on ${cluster_name}"
      return 0
    fi
    echo "  Waiting for ${addon_name} on ${cluster_name}... ($i/30)"
    sleep 10
  done
  echo "ERROR: ${addon_name} addon did not become Available on ${cluster_name} within timeout"
  exit 1
}

wait_for_addon governance-policy-framework ${SPOKE_CLUSTER}
wait_for_addon governance-policy-framework local-cluster
wait_for_addon config-policy-controller ${SPOKE_CLUSTER}
wait_for_addon config-policy-controller local-cluster

# ------- Step 3: Install ArgoCD on hub -------
echo "===== Step 3: Installing ArgoCD on hub ====="
${KUBECTL} config use-context ${HUB_CTX}

# Apply ArgoCD CRDs
${KUBECTL} --context ${HUB_CTX} apply --server-side --force-conflicts -f "${REPO_DIR}/test/e2e/fixtures/argocd-operator-crds.yaml"

# Apply ArgoCD operator (substitute image from Makefile)
sed "s|image: quay.io/argoprojlabs/argocd-operator:[^ ]*|image: ${ARGOCD_OPERATOR_IMAGE}|" "${REPO_DIR}/test/e2e/fixtures/argocd-operator-deployment.yaml" | ${KUBECTL} --context ${HUB_CTX} apply -f -

# Wait for operator
echo ">> Waiting for ArgoCD operator deployment"
${KUBECTL} --context ${HUB_CTX} wait deployment argocd-operator-controller-manager -n argocd-operator-system --for=condition=Available --timeout=180s

# Create openshift-gitops namespace
${KUBECTL} --context ${HUB_CTX} create namespace openshift-gitops --dry-run=client -o yaml | ${KUBECTL} --context ${HUB_CTX} apply -f -

# Create ArgoCD instance on hub (agent principal enabled for agent tests)
cat <<'EOF' | ${KUBECTL} --context ${HUB_CTX} apply -f -
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
      auth: "mtls:CN=system:open-cluster-management:cluster:([^:]+):addon:gitops-addon:agent:gitops-addon-agent"
      logLevel: trace
      server:
        service:
          type: NodePort
      namespace:
        allowedNamespaces:
          - "*"
      tls:
        secretName: argocd-agent-principal-tls
        rootCASecretName: argocd-agent-ca
      jwt:
        secretName: argocd-agent-jwt
EOF

echo ">> Waiting for ArgoCD server pods"
for i in $(seq 1 30); do
  POD_COUNT=$(${KUBECTL} --context ${HUB_CTX} get pods -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-server --no-headers 2>/dev/null | wc -l)
  if [ "$POD_COUNT" -gt 0 ]; then
    echo "ArgoCD server pod found, waiting for Ready condition..."
    break
  fi
  echo "  Waiting for ArgoCD server pod to appear... ($i/30)"
  sleep 10
done
if ! ${KUBECTL} --context ${HUB_CTX} -n openshift-gitops wait --for=condition=Ready pod -l app.kubernetes.io/name=openshift-gitops-server --timeout=300s; then
  echo "ERROR: ArgoCD server pod did not become Ready within 5 minutes"
  exit 1
fi
echo "ArgoCD server is Ready"

# Apply ArgoCD CRDs on spoke (needed for ArgoCD operator to work)
${KUBECTL} --context ${SPOKE_CTX} apply --server-side --force-conflicts -f "${REPO_DIR}/test/e2e/fixtures/argocd-operator-crds.yaml"
if [ -f "${REPO_DIR}/test/e2e/fixtures/argocd-export-crd.yaml" ]; then
  ${KUBECTL} --context ${SPOKE_CTX} apply --server-side --force-conflicts -f "${REPO_DIR}/test/e2e/fixtures/argocd-export-crd.yaml"
else
  echo "WARN: argocd-export-crd.yaml not found at ${REPO_DIR}/test/e2e/fixtures/argocd-export-crd.yaml, skipping"
fi

# Install a minimal Route CRD on both clusters so the ArgoCD operator doesn't fail on route reconciliation.
# Hub needs it for the local-cluster ArgoCD that the policy deploys there.
ROUTE_CRD='apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: routes.route.openshift.io
spec:
  group: route.openshift.io
  names:
    kind: Route
    listKind: RouteList
    plural: routes
    singular: route
  scope: Namespaced
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true'
echo "${ROUTE_CRD}" | ${KUBECTL} --context ${SPOKE_CTX} apply --server-side --force-conflicts -f -
echo "${ROUTE_CRD}" | ${KUBECTL} --context ${HUB_CTX} apply --server-side --force-conflicts -f -

# ------- Step 4: Deploy controller -------
echo "===== Step 4: Deploying multicloud-integrations controller ====="
${KUBECTL} config use-context ${HUB_CTX}

# Apply CRDs
for f in "${REPO_DIR}"/deploy/crds/*.yaml; do
  ${KUBECTL} --context ${HUB_CTX} apply --server-side --force-conflicts -f "$f"
done

# Create namespace
${KUBECTL} --context ${HUB_CTX} create namespace open-cluster-management --dry-run=client -o yaml | ${KUBECTL} --context ${HUB_CTX} apply -f -

# Apply RBAC and controller
for f in "${REPO_DIR}"/deploy/controller/*.yaml; do
  ${KUBECTL} --context ${HUB_CTX} apply -f "$f"
done

# Patch the deployment image and set operator image env var in a single rollout
${KUBECTL} --context ${HUB_CTX} set image deployment/multicloud-integrations-gitops -n open-cluster-management manager="${E2E_IMG}"
${KUBECTL} --context ${HUB_CTX} set env deployment/multicloud-integrations-gitops -n open-cluster-management \
  GITOPS_OPERATOR_IMAGE="${ARGOCD_OPERATOR_IMAGE}"

echo ">> Waiting for controller rollout to complete"
${KUBECTL} --context ${HUB_CTX} rollout status deployment/multicloud-integrations-gitops -n open-cluster-management --timeout=180s

# ------- Step 5: Apply ClusterManagementAddon + AddOnTemplates -------
echo "===== Step 5: Applying ClusterManagementAddon and AddOnTemplates ====="

# Patch the AddOnTemplate image to use the e2e image
sed "s|quay.io/stolostron/multicloud-integrations:latest|${E2E_IMG}|g" "${REPO_DIR}/gitopsaddon/addonTemplates/addonTemplates.yaml" | ${KUBECTL} --context ${HUB_CTX} apply -f -
${KUBECTL} --context ${HUB_CTX} apply -f "${REPO_DIR}/gitopsaddon/addonTemplates/clusterManagementAddon.yaml"

echo ""
echo "============================================"
echo "E2E Setup Complete!"
echo "============================================"
