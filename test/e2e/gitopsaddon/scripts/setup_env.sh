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
ARGOCD_OPERATOR_IMAGE=${ARGOCD_OPERATOR_IMAGE:-quay.io/argoprojlabs/argocd-operator:latest}

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

echo ">> Labeling ${SPOKE_CLUSTER} with name=${SPOKE_CLUSTER}"
${KUBECTL} --context ${HUB_CTX} label managedcluster ${SPOKE_CLUSTER} name=${SPOKE_CLUSTER} --overwrite

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
${KUBECTL} --context ${HUB_CTX} label managedcluster local-cluster local-cluster=true name=local-cluster --overwrite

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

# ------- Step 3: Install ArgoCD operator on hub -------
# NOTE: We install the ArgoCD CRDs and operator here, but we do NOT create
# the ArgoCD CR yet. The ArgoCD principal requires argocd-agent-resource-proxy-tls
# to exist before it can start. That secret is created by the hub controller
# when it reconciles a GitOpsCluster. We deploy the controller first (Step 4),
# trigger cert generation via a stub GitOpsCluster, then create the ArgoCD CR.
echo "===== Step 3: Installing ArgoCD operator on hub (CRDs + operator only) ====="
${KUBECTL} config use-context ${HUB_CTX}

# Apply ArgoCD CRDs
${KUBECTL} --context ${HUB_CTX} apply --server-side --force-conflicts -f "${REPO_DIR}/test/e2e/fixtures/argocd-operator-crds.yaml"

# Apply ArgoCD operator (substitute image from Makefile)
sed "s|image: quay.io/argoprojlabs/argocd-operator:[^ ]*|image: ${ARGOCD_OPERATOR_IMAGE}|" "${REPO_DIR}/test/e2e/fixtures/argocd-operator-deployment.yaml" | ${KUBECTL} --context ${HUB_CTX} apply -f -

# Wait for operator
echo ">> Waiting for ArgoCD operator deployment"
${KUBECTL} --context ${HUB_CTX} wait deployment argocd-operator-controller-manager -n argocd-operator-system --for=condition=Available --timeout=180s

# Create openshift-gitops namespace (needed by both the controller and ArgoCD)
${KUBECTL} --context ${HUB_CTX} create namespace openshift-gitops --dry-run=client -o yaml | ${KUBECTL} --context ${HUB_CTX} apply -f -

# ------- Step 4: Deploy controller -------
# Deploy the controller BEFORE creating the ArgoCD CR so it can generate
# argocd-agent-resource-proxy-tls before the principal pod starts.
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

# ------- Step 3.5: Bootstrap argocd-agent certs before ArgoCD CR -------
#
# The new argocd-operator:latest requires argocd-agent-resource-proxy-tls to exist
# when the principal pod starts (it FATALs if the secret is missing). The hub
# controller generates all three certs (CA, principal TLS, resource proxy TLS)
# during GitOpsCluster reconciliation.
#
# We create a stub GitOpsCluster here — with the same name and namespace the test
# uses — to trigger cert generation before the ArgoCD CR is created. The test's
# BeforeAll will later update this same object via kubectl apply with the full spec
# (Placement, etc.). The controller will fail to process the Placement (it doesn't
# exist yet), but cert generation happens early in the reconcile loop, before
# Placement processing, so all three secrets will be created.
echo "===== Step 3.5: Bootstrap argocd-agent certs via stub GitOpsCluster ====="

# Create the resource proxy service now so the controller can discover it when
# building the resource proxy cert SANs. This ensures the SANs in the generated
# cert match the service name, avoiding a SAN-drift delete-and-recreate cycle later.
echo ">> Pre-creating resource proxy service for SAN discovery"
cat <<'RESOURCE_PROXY_PRESVC_EOF' | ${KUBECTL} --context ${HUB_CTX} apply -f -
apiVersion: v1
kind: Service
metadata:
  name: openshift-gitops-agent-principal-resource-proxy
  namespace: openshift-gitops
  labels:
    app.kubernetes.io/part-of: argocd-agent
    app.kubernetes.io/name: openshift-gitops-agent-principal-resource-proxy
spec:
  selector:
    app.kubernetes.io/name: openshift-gitops-agent-principal
  ports:
  - name: resource-proxy
    port: 9090
    targetPort: 9090
RESOURCE_PROXY_PRESVC_EOF

echo ">> Creating stub GitOpsCluster to trigger cert generation"
cat <<'STUB_EOF' | ${KUBECTL} --context ${HUB_CTX} apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: gitops-cluster
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: all-openshift-clusters
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: true
STUB_EOF

echo ">> Waiting for argocd-agent-resource-proxy-tls to be created by the controller"
for i in $(seq 1 60); do
  if ${KUBECTL} --context ${HUB_CTX} -n openshift-gitops get secret argocd-agent-resource-proxy-tls &>/dev/null; then
    echo "argocd-agent-resource-proxy-tls created — all agent certs are ready"
    break
  fi
  if [ "$i" -eq 60 ]; then
    echo "ERROR: argocd-agent-resource-proxy-tls was not created within 300s"
    echo "Controller logs (last 50 lines):"
    CTRL_POD=$(${KUBECTL} --context ${HUB_CTX} get pods -n open-cluster-management \
      --no-headers -o name 2>/dev/null | grep multicloud-integrations-gitops | head -1)
    if [ -n "${CTRL_POD}" ]; then
      ${KUBECTL} --context ${HUB_CTX} logs -n open-cluster-management "${CTRL_POD}" --tail=50 || true
    else
      echo "  (controller pod not found — listing pods in open-cluster-management:)"
      ${KUBECTL} --context ${HUB_CTX} get pods -n open-cluster-management || true
    fi
    exit 1
  fi
  echo "  Waiting for resource proxy TLS cert... ($i/60)"
  sleep 5
done

# ------- Step 3c: Create ArgoCD instance on hub -------
#
# All three argocd-agent certs now exist (argocd-agent-ca, argocd-agent-principal-tls,
# argocd-agent-resource-proxy-tls). The principal pod will load them successfully
# on first start without crashing or entering CrashLoopBackOff.
#
# Controller must be enabled: the ApplicationSet controller is embedded in the
# application-controller process in ArgoCD 2.14+, so disabling the controller
# also disables ApplicationSet generation.
echo "===== Step 3c: Creating ArgoCD instance on hub ====="
cat <<'EOF' | ${KUBECTL} --context ${HUB_CTX} apply -f -
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: openshift-gitops
  namespace: openshift-gitops
spec:
  applicationSet:
    enabled: true
  sourceNamespaces:
    - "*"
  argoCDAgent:
    principal:
      enabled: true
      destinationBasedMapping: true
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

# ------- Step 3d: Wait for principal pod -------
#
# The principal should start cleanly on the first attempt since all required TLS
# secrets exist. We no longer expect CrashLoopBackOff here.
echo ">> Waiting for ArgoCD agent principal pod to become Ready"
for i in $(seq 1 36); do
  POD_COUNT=$(${KUBECTL} --context ${HUB_CTX} get pods -n openshift-gitops \
    -l app.kubernetes.io/name=openshift-gitops-agent-principal --no-headers 2>/dev/null | wc -l)
  if [ "$POD_COUNT" -gt 0 ]; then
    echo "  Principal pod found, waiting for Ready..."
    break
  fi
  echo "  Waiting for principal pod to appear... ($i/36)"
  sleep 10
done
if ! ${KUBECTL} --context ${HUB_CTX} -n openshift-gitops wait \
  --for=condition=Ready pod \
  -l app.kubernetes.io/name=openshift-gitops-agent-principal \
  --timeout=180s; then
  echo "WARN: principal pod did not become Ready within 180s — checking for crashes"
  ${KUBECTL} --context ${HUB_CTX} get pods -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-agent-principal
  ${KUBECTL} --context ${HUB_CTX} logs -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-agent-principal --tail=30 || true
  echo "WARN: continuing anyway — the cert exists so principal should eventually start"
fi

# The resource proxy service was already created in Step 3.5 (before cert bootstrap).
# Confirm it still exists (idempotent apply is safe here).
echo ">> Confirming resource proxy service exists"
${KUBECTL} --context ${HUB_CTX} get service openshift-gitops-agent-principal-resource-proxy -n openshift-gitops

# Apply ArgoCD CRDs on spoke (needed for ArgoCD operator to work)
${KUBECTL} --context ${SPOKE_CTX} apply --server-side --force-conflicts -f "${REPO_DIR}/test/e2e/fixtures/argocd-operator-crds.yaml"
if [ -f "${REPO_DIR}/test/e2e/fixtures/argocd-export-crd.yaml" ]; then
  ${KUBECTL} --context ${SPOKE_CTX} apply --server-side --force-conflicts -f "${REPO_DIR}/test/e2e/fixtures/argocd-export-crd.yaml"
else
  echo "WARN: argocd-export-crd.yaml not found at ${REPO_DIR}/test/e2e/fixtures/argocd-export-crd.yaml, skipping"
fi

# The Route CRD is NOT installed here. The upstream ArgoCD operator gracefully handles the
# absence of Route API (logs "route.openshift.io/v1 API is not registered" and skips Route
# creation). On managed clusters, the gitopsaddon agent installs the Route CRD stub
# (gitopsaddon/routes-openshift-crd/) as part of the embedded chart deployment.

echo ""
echo "============================================"
echo "E2E Setup Complete!"
echo "============================================"
