# CLAUDE.md

This file provides guidance when working with code in this repository.

## Repository Overview

This is a multi-cloud integrations repository that provides controllers for integrating Open Cluster Management (OCM) with GitOps solutions like Argo CD. It contains two primary components:

1. **gitopscluster** — A hub-side controller that runs in `open-cluster-management` namespace on the ACM hub. It reconciles `GitOpsCluster` CRs, creates ArgoCD Policies via the OCM governance framework, manages `AddOnDeploymentConfig` and `ManagedClusterAddOn` resources, handles agent-mode cluster secrets, and performs agent version drift healing.

2. **gitopsaddon** — An addon agent that runs on each managed cluster. It installs the OpenShift GitOps operator (via OLM on OCP, via an embedded Helm chart on non-OCP), creates the ArgoCD CR, manages image pull secrets, and patches ServiceAccounts. It is deployed by the OCM addon framework based on `AddOnTemplate` resources.

Both binaries are built from the same image (`quay.io/stolostron/multicloud-integrations`). The gitopscluster controller runs as a container in the `multicluster-operators-application` deployment on the hub. The gitopsaddon binary runs as a standalone addon agent deployment on each managed cluster.

## Common Commands

### Building
```bash
# Build all binaries
make build

# Build container images
make build-images

# Build and push dev image for testing on real clusters
docker build -t quay.io/stolostron/multicloud-integrations:latest -f build/Dockerfile .
docker push quay.io/stolostron/multicloud-integrations:latest
```

### Testing
```bash
# Download kubebuilder tools (required for unit tests)
make ensure-kubebuilder-tools

# Run unit tests
make test

# Run legacy cluster-import e2e tests (requires kind clusters already running)
make test-e2e

# Run gitopsaddon e2e tests locally (creates kind clusters, builds image, runs tests)
make test-local-e2e-gitopsaddon-embedded
make test-local-e2e-gitopsaddon-embedded-agent
make test-local-e2e-gitopsaddon-olm-override

# Run gitopsaddon e2e tests on existing kind clusters (CI mode, no cluster creation)
make test-e2e-gitopsaddon-embedded
make test-e2e-gitopsaddon-embedded-agent
make test-e2e-gitopsaddon-olm-override

# Run integration test scenarios against real ACM hub + managed clusters
cd gitopsaddon && bash test-scenarios.sh all
```

### Code Quality
```bash
# Run linting
make lint

# Generate code (DeepCopy methods, manifests)
make generate

# Generate CRD manifests
make manifests
```

## Build / Push / Redeploy Workflow

When making code changes to the hub controller (gitopscluster) or addon agent (gitopsaddon), follow this workflow to test on a real ACM hub:

### 1. Build and Push
```bash
docker build -t quay.io/stolostron/multicloud-integrations:latest -f build/Dockerfile .
docker push quay.io/stolostron/multicloud-integrations:latest
```
If you don't have push access to `quay.io/stolostron`, tag and push to a personal registry, then update the deployment image.

### 2. Redeploy on Hub
The hub controller runs as `multicluster-operators-gitopscluster` container inside the `multicluster-operators-application` deployment:
```bash
export KUBECONFIG=~/Desktop/hub
kubectl -n open-cluster-management set image deployment/multicluster-operators-application \
  multicluster-operators-gitopscluster=quay.io/stolostron/multicloud-integrations:latest
kubectl -n open-cluster-management rollout restart deployment multicluster-operators-application
kubectl -n open-cluster-management rollout status deployment multicluster-operators-application --timeout=120s
```

### 3. Prevent MCH Operator Revert
The MultiClusterHub operator (`multiclusterhub-operator`) continuously reconciles deployments and may revert your image. Scale it down temporarily:
```bash
kubectl -n open-cluster-management scale deploy multiclusterhub-operator --replicas=0
```
After validating your changes, restore the operator:
```bash
kubectl -n open-cluster-management scale deploy multiclusterhub-operator --replicas=1
```

### 4. Addon Template
The addon agent image is specified in the `AddOnTemplate` resource on the hub (named `gitops-addon`). If the addon template already references the same image tag you pushed, managed cluster addons will pick up the new image on their next reconcile. Check:
```bash
kubectl get addontemplate gitops-addon -o json | python3 -c "
import json,sys
d = json.load(sys.stdin)
for m in d['spec']['agentSpec']['workload']['manifests']:
    if m.get('kind') == 'Deployment':
        for c in m.get('spec',{}).get('template',{}).get('spec',{}).get('containers',[]):
            print(c.get('name'), c.get('image'))
"
```
If agent mode is enabled, a dynamic template `gitops-addon-{ns}-{name}` is also created per GitOpsCluster.

### 5. Verify
```bash
# Verify hub controller image
kubectl -n open-cluster-management get deploy multicluster-operators-application \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="multicluster-operators-gitopscluster")].image}'

# Verify addon agent image on managed cluster
kubectl --context kind-kind-cluster1 get deploy -n open-cluster-management-agent-addon gitops-addon -o jsonpath='{.spec.template.spec.containers[0].image}'
```

### 6. Run Tests
After redeploying, run the integration tests:
```bash
cd gitopsaddon && bash test-scenarios.sh all
```
Then run the local e2e tests (these create fresh Kind clusters, so they don't interfere with the real hub):
```bash
make test-local-e2e-gitopsaddon-embedded
make test-local-e2e-gitopsaddon-embedded-agent
make test-local-e2e-gitopsaddon-olm-override
```

## Architecture

### Hub Controller ↔ Addon Agent Relationship

The **gitopscluster controller** (hub) creates:
- `AddOnDeploymentConfig` per managed cluster — passes env vars (`ARGOCD_AGENT_ENABLED`, `OLM_SUBSCRIPTION_ENABLED`, `ARGOCD_NAMESPACE`, etc.) to the addon agent
- `ManagedClusterAddOn` for each managed cluster — triggers the addon framework to deploy the addon agent
- `AddOnTemplate` — defines the addon agent's Deployment/Job/SA manifests
- ArgoCD `Policy` — wraps the ArgoCD CR for enforcement on managed clusters via OCM governance
- `PlacementBinding` — binds the Policy to the Placement
- Cluster secrets — ArgoCD cluster secrets with `agentName` for agent mode routing

The **gitopsaddon agent** (managed cluster) receives env vars from `AddOnDeploymentConfig` and:
- Detects cluster type (OCP vs non-OCP, hub vs remote)
- Installs the GitOps operator (OLM on OCP, embedded Helm chart on non-OCP, skip on hub)
- Waits for the ArgoCD CR to be deployed by the Policy
- Patches ServiceAccounts with image pull secret references (non-OCP only)

### Package Structure

- `pkg/apis/apps/v1beta1/`: GitOpsCluster CRD types
- `pkg/controller/gitopscluster/`: Hub controller
  - `gitopscluster_controller.go`: Main reconciliation loop
  - `argocd_policy.go`: ArgoCD Policy generation
  - `agent_version_heal.go`: Detects principal/agent version drift and patches Policy
  - `server_discovery.go`: Discovers ArgoCD agent principal service/route
  - `argocd_agent_clusters.go`: Agent mode cluster secret management
  - `addon_management.go`: AddOnDeploymentConfig and ManagedClusterAddOn management
  - `olm_subscription.go`: OLM subscription env var management
- `gitopsaddon/`: Addon agent
  - `gitopsaddon_install.go`: Core installation logic (OLM vs embedded, hub/OCP detection)
  - `gitopsaddon_controller.go`: Agent reconciliation loop
  - `gitopsaddon_utils.go`: CRD installation, image parsing, discovery helpers
  - `charts/openshift-gitops-operator/`: Embedded Helm chart (CRDs + operator deployment)
  - `routes-openshift-crd/`: Route CRD stub (`served: false`) installed by the addon agent on non-OCP managed clusters. Not needed by the upstream ArgoCD operator — only relevant for the Red Hat operator / ROSA HCP route API checks.
  - `test-scenarios.sh`: Integration test script for real ACM clusters
  - `README.md`: Comprehensive feature documentation
- `deploy/crds/`: CRD definitions (GitOpsCluster, ArgoCD, etc.)
- `test/e2e/gitopsaddon/`: Ginkgo-based e2e tests (Kind clusters)

### Key CRDs

- **GitOpsCluster** (`apps.open-cluster-management.io/v1beta1`): Links OCM Placements to ArgoCD. Controls addon deployment, agent mode, OLM subscription overrides, and annotations for drift heal / policy recreation control.
- **Placement**: OCM resource for cluster selection
- **ManagedCluster**: OCM representation of managed clusters
- **AddOnTemplate**: Defines the addon agent's deployment manifests
- **AddOnDeploymentConfig**: Passes environment variables from hub to addon agent

### Key Annotations on GitOpsCluster

- `apps.open-cluster-management.io/skip-agent-version-heal: "true"` — Disables agent version drift detection and policy patching
- `apps.open-cluster-management.io/skip-argocd-policy: "true"` — Prevents ArgoCD Policy recreation if user deletes it

### Key Features

- **Agent Version Drift Heal**: In agent mode, the controller **watches** the principal Deployment (label `app.kubernetes.io/component=principal`, name suffix `-agent-principal`) for container image changes. When the image changes (e.g., operator upgrade), the watch automatically triggers reconciliation and patches the ArgoCD Policy's `spec.argoCDAgent.agent.image` to match. Only the addon-managed template named `acm-openshift-gitops` is patched; user-added ArgoCD templates are left untouched. The Deployment cache is scoped to principal-labeled Deployments only (configured in `cmd/gitopscluster/exec/manager.go`). The principal container image is found by `findContainerImage(containers, deploymentName)`, which checks in order: (1) a container named `"principal"` (Red Hat operator convention), (2) a container matching the deployment name (upstream operator convention, e.g., `openshift-gitops-agent-principal`), (3) falls back to `containers[0]`. The deployment name parameter prevents sidecar containers (e.g., Istio) from being picked up by the `containers[0]` fallback. This ensures spoke agents stay version-compatible with the hub principal without manual intervention.
- **OLM Override**: `olmSubscription.enabled: true` in GitOpsCluster spec forces the addon to use OLM mode regardless of cluster type detection.
- **Policy Recreation Control**: The controller recreates deleted Policies unless `skip-argocd-policy` annotation is set. When skipped, the `ArgoCDPolicyReady` condition is set to `True` with `Reason=Skipped` (not `Reason=Success`) so operators can distinguish intentional skips from actual readiness.
- **Routes CRD Stub**: The repo ships a routes CRD at `gitopsaddon/routes-openshift-crd/routes.route.openshift.io.crd.yaml` with `served: false`. The gitopsaddon agent installs this on non-OCP managed clusters as part of the embedded Helm chart deployment. It is NOT installed by the e2e `setup_env.sh` — the upstream ArgoCD operator does not need it at all (it gracefully handles the absence: logs "route.openshift.io/v1 API is not registered" and skips Route creation, reconciling the ArgoCD CR fully to `Available`). The `served: false` flag prevents conflicts with ROSA HCP route API checks on real OCP clusters.

## Development Workflow

1. **Prerequisites**: Go 1.25+ and access to a Kubernetes cluster with OCM installed
2. **Testing**: Always run `make test` before submitting changes
3. **Code Generation**: Run `make generate` after modifying API types in `pkg/apis/`
4. **Manifests**: Run `make manifests` after changing RBAC or CRD annotations
5. **CRD Sync**: When updating to a new OpenShift GitOps Operator version, sync CRDs from the operator bundle and update image SHAs in `pkg/utils/config.go` and `gitopsaddon/charts/openshift-gitops-operator/templates/openshift-gitops-operator-controller-manager.deployment.yaml`
6. **Integration Testing**: Use `gitopsaddon/test-scenarios.sh` against real ACM clusters
7. **E2E Tests**: Run `make test-local-e2e-gitopsaddon-embedded-agent` for local e2e (creates Kind clusters automatically). Use `make test-local-e2e-*` targets (not `make test-e2e-*`) to mirror CI behavior — they create fresh Kind clusters, build the image, and run setup from scratch.
8. **Documentation**: After any code change or when learning new context about the codebase (architecture, behaviors, gotchas, testing patterns, environment setup), always update both `CLAUDE.md` and `gitopsaddon/README.md` to reflect the new knowledge. Future chat sessions start fresh and rely on these files for context.
9. **Ask before acting**: If confused about requirements, scope, or trade-offs, always ask clarifying questions before proceeding. Prefer switching to plan mode for large or ambiguous tasks.

## Test Environments

### test-scenarios.sh (Real ACM Clusters)
Runs against a real ACM hub + OCP managed cluster + Kind managed cluster. Environment variables:
- `HUB_KUBECONFIG` — Hub kubeconfig (default: `~/Desktop/hub`)
- `KIND_KUBECONFIG` — Kind cluster kubeconfig (default: `~/Desktop/kind-cluster1`)
- `OCP_KUBECONFIG` — OCP cluster kubeconfig (default: `~/Desktop/ocp-cluster1`)

Scenarios run sequentially: cleanup → S1 (no agent) → cleanup → S2 (agent) → S5 (drift heal, after S2) → cleanup → S3 (OLM, OCP only) → cleanup → S4 (OLM override, Kind)

### E2E Tests (Kind Clusters)
The `make test-local-e2e-gitopsaddon-*` targets create fresh Kind clusters (`hub` + `cluster1`), install OCM, deploy the controller, and run Ginkgo tests. These use the upstream `argocd-operator` (not Red Hat/OLM) and test the embedded operator path. The e2e Kind clusters (`kind-hub`, `kind-cluster1`) are separate from any real managed clusters.

### Creating and Importing a Kind Managed Cluster

If a Kind managed cluster doesn't exist and you need one for `test-scenarios.sh`, follow these steps carefully:

#### Step 1: Create the Kind cluster
```bash
kind create cluster --name kind-cluster1
```

#### Step 2: Export the kubeconfig
```bash
kind get kubeconfig --name kind-cluster1 > ~/Desktop/kind-cluster1
```

#### Step 3: Register as ManagedCluster on the hub
On the hub, create the ManagedCluster resource so ACM knows about it:
```bash
KUBECONFIG=~/Desktop/hub kubectl apply -f - <<'EOF'
apiVersion: cluster.open-cluster-management.io/v1
kind: ManagedCluster
metadata:
  name: kind-cluster1
spec:
  hubAcceptsClient: true
  leaseDurationSeconds: 60
EOF
```

#### Step 4: Extract and apply import manifests
ACM auto-generates an import secret for each ManagedCluster. Extract the manifests and apply them to the Kind cluster:
```bash
# Wait for the import secret to be created
KUBECONFIG=~/Desktop/hub kubectl -n kind-cluster1 get secret kind-cluster1-import -o jsonpath='{.data.import\.yaml}' | base64 -d > /tmp/kind-import.yaml

# Apply to the Kind cluster
KUBECONFIG=~/Desktop/kind-cluster1 kubectl apply -f /tmp/kind-import.yaml
```

#### Step 5: Wait for the cluster to become Available
```bash
KUBECONFIG=~/Desktop/hub kubectl get managedcluster kind-cluster1 -w
# Wait until AVAILABLE=True
```

#### Step 6: Install governance addons (CRITICAL)
The manual import only installs the base klusterlet agent. For `test-scenarios.sh` to work, the Kind cluster **must** have the governance policy framework addons. Without these, Policies will never be enforced on the cluster:
```bash
KUBECONFIG=~/Desktop/hub kubectl apply -f - <<'EOF'
apiVersion: addon.open-cluster-management.io/v1alpha1
kind: ManagedClusterAddOn
metadata:
  name: governance-policy-framework
  namespace: kind-cluster1
spec:
  installNamespace: open-cluster-management-agent-addon
---
apiVersion: addon.open-cluster-management.io/v1alpha1
kind: ManagedClusterAddOn
metadata:
  name: config-policy-controller
  namespace: kind-cluster1
spec:
  installNamespace: open-cluster-management-agent-addon
EOF

# Verify both become Available
KUBECONFIG=~/Desktop/hub kubectl get managedclusteraddon -n kind-cluster1
```

**Why this matters**: Without `governance-policy-framework` and `config-policy-controller`, the OCM Policy framework cannot enforce ConfigurationPolicies on the managed cluster. The ArgoCD Policy created by the gitopscluster controller will never become Compliant, and `test-scenarios.sh` Scenario 1 (no-agent mode) will time out. OCP clusters get these addons automatically when imported through the ACM console, but manually imported Kind clusters do not.

#### Alternative: clusteradm (if available)
```bash
clusteradm join --hub-token <token> --hub-apiserver <hub-api> --cluster-name kind-cluster1 --context kind-kind-cluster1
clusteradm accept --clusters kind-cluster1
```
Note: `clusteradm` may not be available on all environments. The manual import method above always works.

## CI Pipeline

The GitHub Actions CI (`.github/workflows/e2e.yml`) runs on PRs to main/release branches:
- **e2e**: Legacy cluster-import tests + cluster secret deletion tests
- **e2e-gitopsaddon (embedded)**: `make test-e2e-gitopsaddon-embedded` — tests non-agent addon flow + skip-argocd-policy annotation
- **e2e-gitopsaddon (embedded-agent)**: `make test-e2e-gitopsaddon-embedded-agent` — tests agent mode flow + drift auto-heal + env var propagation
- **e2e-gitopsaddon (olm-override)**: `make test-e2e-gitopsaddon-olm-override` — tests OLM override hub-side propagation

To run locally what CI runs: `make test-local-e2e-gitopsaddon-embedded` (creates kind clusters, builds image, runs tests).

## Testing Framework

Uses Ginkgo/Gomega for e2e testing with kubebuilder tools for unit tests. Unit tests require downloading kubebuilder assets via `make ensure-kubebuilder-tools`.

### E2E Testing Gotchas

- **CI vs local timing**: GitHub Actions runners are slower than local dev machines. ArgoCD Application sync can take significantly longer (or stay `OutOfSync` for minutes) on CI. Avoid asserting `Synced` status with tight timeouts — prefer checking resource existence or controller state instead.
- **`make test-local-e2e-*` vs `make test-e2e-*`**: The `test-local-*` targets create fresh Kind clusters and run full setup; `test-e2e-*` targets reuse existing clusters. Always use `test-local-*` to mirror CI behavior when validating changes.
- **Principal container naming**: The upstream ArgoCD operator names the principal container after the deployment (e.g., `openshift-gitops-agent-principal`), while the Red Hat operator names it `"principal"`. The `findContainerImage(containers, deploymentName)` helper handles both conventions by checking `"principal"` first, then the deployment name, then `containers[0]` as a last resort.
- **`test-scenarios.sh` Policy compliance check**: The `wait_for_policy_compliant` function uses exact string equality (`= 'Compliant'`), not `grep`. Using `grep -q 'Compliant'` is a bug because it matches both `"Compliant"` and `"NonCompliant"` (substring match).

## Container Registry

Default registry: `quay.io/stolostron`
Configure via: `REGISTRY` and `VERSION` environment variables
