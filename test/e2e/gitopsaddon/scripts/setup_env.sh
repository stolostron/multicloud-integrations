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
E2E_IMG=${E2E_IMG:-quay.io/stolostron/multicloud-integrations:e2e}
# Docker config JSON with registry.redhat.io credentials for the chart's images.
RH_PULL_SECRET_FILE=${RH_PULL_SECRET_FILE:-}

# The hub-side gitopsaddon process started below; the Makefile kills it on exit.
ADDON_PID_FILE=${ADDON_PID_FILE:-/tmp/gitopsaddon-e2e.pid}
ADDON_LOG=${ADDON_LOG:-/tmp/gitopsaddon-e2e.log}

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"

if [[ -z "${RH_PULL_SECRET_FILE}" ]]; then
  echo "ERROR: RH_PULL_SECRET_FILE is not set. The embedded chart pulls its operator and ArgoCD" >&2
  echo "ERROR: images from registry.redhat.io, which cannot be pulled anonymously." >&2
  exit 1
fi

# seed_pull_secret writes the klusterlet's source pull-secret in a namespace.
seed_pull_secret() {
  local kctx=$1
  local ns=$2
  ${KUBECTL} --context "${kctx}" create namespace "${ns}" --dry-run=client -o yaml | ${KUBECTL} --context "${kctx}" apply -f -
  ${KUBECTL} --context "${kctx}" -n "${ns}" create secret generic open-cluster-management-image-pull-credentials \
    --from-file=.dockerconfigjson="${RH_PULL_SECRET_FILE}" \
    --type=kubernetes.io/dockerconfigjson \
    --dry-run=client -o yaml | ${KUBECTL} --context "${kctx}" apply -f -
  echo ">> Seeded open-cluster-management-image-pull-credentials in ${kctx}/${ns}"
}

# wait_for_ready_pod polls a selector until a pod is Ready.
wait_for_ready_pod() {
  local kctx=$1
  local ns=$2
  local selector=$3
  local timeout=$4
  local deadline=$((SECONDS + timeout))
  while [[ $SECONDS -lt $deadline ]]; do
    local ready
    ready=$(${KUBECTL} --context "${kctx}" get pods -n "${ns}" -l "${selector}" \
      -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' 2>/dev/null \
      | grep -c '^True$' || true)
    if [[ "${ready:-0}" -gt 0 ]]; then
      return 0
    fi
    sleep 5
  done
  return 1
}

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

# Seed each cluster's klusterlet source pull-secret (no MCE in this environment).
seed_pull_secret "${HUB_CTX}" "open-cluster-management"
seed_pull_secret "${SPOKE_CTX}" "open-cluster-management"

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

# ------- Step 3: Install the GitOps operator on hub -------
# The hub is non-OCP, so install the operator from the embedded chart by running the
# addon against it. Keep it running: it also handles pull secrets and SA patching.
echo "===== Step 3: Installing GitOps operator on hub (embedded chart via gitopsaddon) ====="
${KUBECTL} config use-context ${HUB_CTX}

# Kill a leftover addon from an interrupted run.
if [[ -f "${ADDON_PID_FILE}" ]]; then
  kill "$(cat "${ADDON_PID_FILE}")" 2>/dev/null || true
  rm -f "${ADDON_PID_FILE}"
fi

# The klusterlet already propagated the seeded secret into the addon namespaces.

# Pin a hub kubeconfig; the script switches contexts but the addon must stay on the hub.
ADDON_BIN="$(mktemp -d)/gitopsaddon"
( cd "${REPO_DIR}" && go build -o "${ADDON_BIN}" ./cmd/gitopsaddon )
HUB_KUBECONFIG="$(mktemp -d)/hub.kubeconfig"
${KUBECTL} --context ${HUB_CTX} config view --minify --flatten > "${HUB_KUBECONFIG}"

KUBECONFIG="${HUB_KUBECONFIG}" \
  POD_NAMESPACE="open-cluster-management-agent-addon" \
  GITOPS_ADDON_NAMESPACE="open-cluster-management-agent-addon" \
  ARGOCD_NAMESPACE="openshift-gitops" \
  nohup "${ADDON_BIN}" > "${ADDON_LOG}" 2>&1 &
echo $! > "${ADDON_PID_FILE}"
echo ">> gitopsaddon running against the hub (pid $(cat "${ADDON_PID_FILE}"), log ${ADDON_LOG})"

echo ">> Waiting for the embedded GitOps operator deployment"
# Poll for the Deployment before waiting: it does not exist until the first reconcile.
for i in $(seq 1 120); do
  if ${KUBECTL} --context ${HUB_CTX} get deployment openshift-gitops-operator-controller-manager \
    -n openshift-gitops-operator &>/dev/null; then
    break
  fi
  if [[ "$i" -eq 120 ]]; then
    echo "ERROR: GitOps operator Deployment was not created within 10 minutes" >&2
    tail -50 "${ADDON_LOG}" || true
    exit 1
  fi
  sleep 5
done
if ! ${KUBECTL} --context ${HUB_CTX} wait deployment openshift-gitops-operator-controller-manager \
  -n openshift-gitops-operator --for=condition=Available --timeout=600s; then
  echo "ERROR: GitOps operator did not become Available within 10 minutes" >&2
  tail -50 "${ADDON_LOG}" || true
  exit 1
fi
echo "GitOps operator is Available on the hub"

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

# Apply RBAC and controller, pinning every controller container to the locally built image;
# operator images stay the chart's defaults.
for f in "${REPO_DIR}"/deploy/controller/*.yaml; do
  sed "s|quay.io/stolostron/multicloud-integrations:latest|${E2E_IMG}|g" "$f" | ${KUBECTL} --context ${HUB_CTX} apply -f -
done

echo ">> Waiting for controller rollout to complete"
${KUBECTL} --context ${HUB_CTX} rollout status deployment/multicloud-integrations-gitops -n open-cluster-management --timeout=180s
${KUBECTL} --context ${HUB_CTX} rollout status deployment/multicloud-integrations -n open-cluster-management --timeout=180s

# ------- Step 5: Apply ClusterManagementAddon + AddOnTemplates -------
echo "===== Step 5: Applying ClusterManagementAddon and AddOnTemplates ====="

# Patch the AddOnTemplate image to use the e2e image
sed "s|quay.io/stolostron/multicloud-integrations:latest|${E2E_IMG}|g" "${REPO_DIR}/gitopsaddon/addonTemplates/addonTemplates.yaml" | ${KUBECTL} --context ${HUB_CTX} apply -f -
${KUBECTL} --context ${HUB_CTX} apply -f "${REPO_DIR}/gitopsaddon/addonTemplates/clusterManagementAddon.yaml"

# ------- Step 3.5: Bootstrap argocd-agent certs before ArgoCD CR -------
#
# The principal requires argocd-agent-resource-proxy-tls at startup; the hub controller
# generates the certs during GitOpsCluster reconciliation.
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

echo ">> Waiting for argocd-agent secrets to be created by the controller"
for secret_name in argocd-agent-principal-tls argocd-agent-resource-proxy-tls argocd-agent-jwt; do
  echo ">> Waiting for ${secret_name}..."
  for i in $(seq 1 60); do
    if ${KUBECTL} --context ${HUB_CTX} -n openshift-gitops get secret "${secret_name}" &>/dev/null; then
      echo "${secret_name} created"
      break
    fi
    if [ "$i" -eq 60 ]; then
      echo "ERROR: ${secret_name} was not created within 300s"
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
    echo "  Waiting for ${secret_name}... ($i/60)"
    sleep 5
  done
done
echo "All agent secrets are ready"

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
  controller:
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

# Hybrid-mode prerequisite: the hub app-controller needs cluster-admin for
# in-cluster Applications before the first sync.
echo ">> Granting cluster-admin to the hub ArgoCD application controller"
cat <<'RBAC_EOF' | ${KUBECTL} --context ${HUB_CTX} apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: openshift-gitops-appcontroller-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: openshift-gitops-argocd-application-controller
  namespace: openshift-gitops
RBAC_EOF

echo ">> Waiting for a Ready ArgoCD server pod"
# Poll: the addon replaces pods that hit image-pull errors, kubectl wait would not.
if ! wait_for_ready_pod "${HUB_CTX}" "openshift-gitops" "app.kubernetes.io/name=openshift-gitops-server" 300; then
  echo "ERROR: ArgoCD server pod did not become Ready within 5 minutes"
  ${KUBECTL} --context ${HUB_CTX} get pods -n openshift-gitops || true
  exit 1
fi
echo "ArgoCD server is Ready"

# ------- Step 3d: Wait for principal pod -------
#
# The principal should start cleanly on the first attempt since all required TLS
# secrets exist. We no longer expect CrashLoopBackOff here.
echo ">> Waiting for ArgoCD agent principal pod to become Ready"
if ! wait_for_ready_pod "${HUB_CTX}" "openshift-gitops" "app.kubernetes.io/name=openshift-gitops-agent-principal" 180; then
  echo "WARN: principal pod did not become Ready within 180s — checking for crashes"
  ${KUBECTL} --context ${HUB_CTX} get pods -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-agent-principal
  ${KUBECTL} --context ${HUB_CTX} logs -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-agent-principal --tail=30 || true
  echo "WARN: continuing anyway — the cert exists so principal should eventually start"
fi

# The resource proxy service was already created in Step 3.5 (before cert bootstrap).
# Confirm it still exists (idempotent apply is safe here).
echo ">> Confirming resource proxy service exists"
${KUBECTL} --context ${HUB_CTX} get service openshift-gitops-agent-principal-resource-proxy -n openshift-gitops

# The spoke addon copies the propagated pull secret into its ArgoCD namespaces.

echo ""
echo "============================================"
echo "E2E Setup Complete!"
echo "============================================"
echo ">> The hub gitopsaddon (pid $(cat "${ADDON_PID_FILE}" 2>/dev/null || echo unknown)) stays running"
echo ">> for the suite; the Makefile target kills it after the tests finish."
