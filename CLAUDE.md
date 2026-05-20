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
make test-local-e2e-gitopsaddon-embedded-autonomous

# Run gitopsaddon e2e tests on existing kind clusters (CI mode, no cluster creation)
make test-e2e-gitopsaddon-embedded
make test-e2e-gitopsaddon-embedded-agent
make test-e2e-gitopsaddon-olm-override
make test-e2e-gitopsaddon-embedded-autonomous

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

### 5. Set CONTROLLER_IMAGE
The hub controller's `getControllerImage()` reads the `CONTROLLER_IMAGE` env var to determine which image to embed in dynamic `AddOnTemplate`s. If this env var still points to the old image, managed cluster addon pods will run the old code. Update it:
```bash
kubectl -n open-cluster-management set env deployment/multicluster-operators-application \
  -c multicluster-operators-gitopscluster CONTROLLER_IMAGE=quay.io/stolostron/multicloud-integrations:latest
```

### 6. Verify
```bash
# Verify hub controller image
kubectl -n open-cluster-management get deploy multicluster-operators-application \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="multicluster-operators-gitopscluster")].image}'

# Verify addon agent image on managed cluster
kubectl --context kind-kind-cluster1 get deploy -n open-cluster-management-agent-addon gitops-addon -o jsonpath='{.spec.template.spec.containers[0].image}'
```

### 7. Run Tests
After redeploying, run the integration tests:
```bash
cd gitopsaddon && bash test-scenarios.sh all
```
Then run the local e2e tests (these create fresh Kind clusters, so they don't interfere with the real hub):
```bash
make test-local-e2e-gitopsaddon-embedded
make test-local-e2e-gitopsaddon-embedded-agent
make test-local-e2e-gitopsaddon-olm-override
make test-local-e2e-gitopsaddon-embedded-autonomous
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
  - `argocd_agent_certificates.go`: CA, principal TLS, and resource proxy TLS cert generation
  - `addon_management.go`: AddOnDeploymentConfig and ManagedClusterAddOn management
  - `addon_template_management.go`: Dynamic AddOnTemplate with RegistrationSpec for client cert provisioning
  - `propagate_hub_ca.go`: Propagates argocd-agent-ca secret from hub to managed clusters via ManifestWork
  - `olm_subscription.go`: OLM subscription env var management
- `gitopsaddon/`: Addon agent
  - `gitopsaddon_install.go`: Core installation logic (OLM vs embedded, hub/OCP detection)
  - `gitopsaddon_controller.go`: Agent reconciliation loop
  - `gitopsaddon_utils.go`: CRD installation, image parsing, discovery helpers
  - `secret_controller.go`: Copies `argocd-agent-client-tls` from addon namespace to ArgoCD namespace; watches source updates (cert rotation), target secret deletion (instant re-copy), requeues every 5 minutes as a safety net, and **rolling-restarts agent pods** when cert data actually changes so the renewed cert takes effect
  - `charts/openshift-gitops-operator/`: Embedded Helm chart (CRDs + operator deployment)
  - `routes-openshift-crd/`: Route CRD stub (`served: false`) installed by the addon agent on non-OCP managed clusters. Not needed by the upstream ArgoCD operator — only relevant for the Red Hat operator / ROSA HCP route API checks.
  - `test-scenarios.sh`: Integration test script for real ACM clusters
  - `README.md`: Comprehensive feature documentation
- `deploy/crds/`: CRD definitions (GitOpsCluster, ArgoCD, etc.)
- `docs/autonomous-mode-best-practices.md`: Best practices for bootstrapping autonomous mode with App of Apps pattern
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
- **ManagedClusterSetBinding**: A `ManagedClusterSetBinding` for the `default` ManagedClusterSet must exist in the `openshift-gitops` namespace. Without it, the Placement controller cannot find any ManagedClusters and no clusters will be selected. The `test-scenarios.sh` helper `ensure_clusterset_binding` creates this. On a fresh ACM hub this does not exist by default.
- **Default AppProject Wildcard Settings (Agent Mode)**: For agent mode, the `default` AppProject in `openshift-gitops` must have wildcard settings: `sourceNamespaces: ["*"]`, `destinations: [{name:"*", namespace:"*", server:"*"}]`, `clusterResourceWhitelist: [{group:"*", kind:"*"}]`, `sourceRepos: ["*"]`. The argocd-agent principal only propagates AppProjects to managed agents if their destinations and sourceNamespaces match. Without these wildcards, the principal skips propagation and agents can't find the AppProject for their Applications. The `test-scenarios.sh` helper `ensure_default_appproject_for_agents` patches this.
- **ARGOCD_CLUSTER_CONFIG_NAMESPACES**: The `openshift-gitops-operator` uses this env var to decide which namespaces get cluster-scoped RBAC for their ArgoCD instances. When a namespace is listed, the operator creates ClusterRoles and ClusterRoleBindings for the ArgoCD application-controller, agent, and principal ServiceAccounts in that namespace, granting cluster-scope `list`/`watch` on `namespaces` (among other resources). Without this, only namespace-scoped Roles are created, and agent/principal pods crash with `"namespaces is forbidden"` errors. Must be set to `openshift-gitops,local-cluster` for agent mode — `openshift-gitops` for the principal and remote agents, `local-cluster` for the hub-local agent. The value `*` is also supported (confirmed in `argoutil.IsNamespaceClusterConfigNamespace` → `allowedNamespace` which checks `clusterConfigNamespaces[0] == "*"`) but explicit namespaces are preferred. The `test-scenarios.sh` helper `ensure_cluster_config_namespaces` patches the OLM Subscription to set this. On a fresh ACM hub the default is typically `openshift-gitops` only.
- **OLM Subscription Namespace & OperatorGroup**: The OLM Subscription is always created in the `openshift-gitops-operator` namespace (the same namespace used by the embedded Helm chart install). This ensures OLM deploys the operator controller-manager — and its conversion webhook service (`openshift-gitops-operator-webhook-service`) — in `openshift-gitops-operator`, which is the namespace the Red Hat OLM bundle hardcodes in the ArgoCD CRD's `spec.conversion.webhook.clientConfig.service`. Using `openshift-operators` (AllNamespaces OperatorGroup) instead would place the service in `openshift-operators` while the CRD references `openshift-gitops-operator`, causing an OLM InstallPlan failure: "conversion webhook service not found". An **AllNamespaces** `OperatorGroup` (`gitops-addon-operator-group`) is automatically created in `openshift-gitops-operator` before the subscription — it must NOT use `OwnNamespace` mode (i.e., no `targetNamespaces`) because the openshift-gitops-operator CSV does not support `OwnNamespace` InstallModeType, only `AllNamespaces`. Using `OwnNamespace` causes the CSV to fail immediately with `"OwnNamespace InstallModeType not supported, cannot configure to watch own namespace"`. If an OperatorGroup already exists there, it is used as-is. The namespace can be overridden via the `OLM_SUBSCRIPTION_NAMESPACE` env var (passed through `AddOnDeploymentConfig`). The cleanup path looks for subscriptions in `openshift-gitops-operator`, `openshift-operators`, and `operators` to handle both old and new installs. **Before** creating the subscription, `patchArgoCDCRDConversionWebhookIfNotReady` proactively patches the ArgoCD CRD conversion strategy to `None` if the webhook is not ready (service missing, or service exists but no ready Endpoints). This prevents two OLM InstallPlan failure modes: (1) "service not found" — old namespace mismatch; (2) "connection refused" — service resolves to the new operator pod's IP but the webhook server on port 9443 hasn't started yet. The running operator restores the webhook config once it is up. **Recovery from failed InstallPlan**: When OLM applies the CRD from its bundle it sets `strategy: Webhook`, then immediately validates existing ArgoCD CRs via the webhook. If the operator pod was not ready (e.g., restarting due to an upgrade) the validation fails with "connection refused" and OLM marks the InstallPlan as `InstallPlanFailed` — permanently; OLM never retries a failed InstallPlan automatically. `recoverFromFailedWebhookInstallPlan` detects this condition (checks for `InstallPlanFailed` with "conversion webhook" in the message) and deletes the referenced InstallPlan. On the next reconcile OLM transitions the subscription to `InstallPlanMissing`; the existing `InstallPlanMissing` handler then deletes and recreates the subscription, OLM issues a fresh InstallPlan, and since the operator IS already running (installedCSV is set) the webhook validation succeeds on the second attempt.
- **Addon RBAC (No cluster-admin)**: The `gitops-addon` ServiceAccount uses a **fine-grained `ClusterRole`** named `gitops-addon` instead of `cluster-admin`. This is defined in two places that must stay in sync: (1) `gitopsaddon/addonTemplates/addonTemplates.yaml` (static template) and (2) `gitopsAddonClusterRoleRules()` in `pkg/controller/gitopscluster/addon_template_management.go` (dynamic templates for agent mode). Key permissions include: `escalate`/`bind` on `roles`/`clusterroles`/`rolebindings`/`clusterrolebindings` (required because the addon deploys the ArgoCD operator which creates its own RBAC), `watch` on `configmaps`/`serviceaccounts`/`pods`/`customresourcedefinitions`, `create`/`patch` on `events`, and `update` on `deployments/finalizers`. If the ArgoCD operator adds new RBAC requirements in future versions, these rules must be updated.
- **OLM Override**: `olmSubscription.enabled: true` in GitOpsCluster spec forces the addon to use OLM mode regardless of cluster type detection.
- **Policy Recreation Control**: The controller recreates deleted Policies unless `skip-argocd-policy` annotation is set. When skipped, the `ArgoCDPolicyReady` condition is set to `True` with `Reason=Skipped` (not `Reason=Success`) so operators can distinguish intentional skips from actual readiness.
- **Routes CRD Stub**: The repo ships a routes CRD at `gitopsaddon/routes-openshift-crd/routes.route.openshift.io.crd.yaml` with `served: false`. The gitopsaddon agent installs this on non-OCP managed clusters as part of the embedded Helm chart deployment. It is NOT installed by the e2e `setup_env.sh` — the upstream ArgoCD operator does not need it at all (it gracefully handles the absence: logs "route.openshift.io/v1 API is not registered" and skips Route creation, reconciling the ArgoCD CR fully to `Available`). The `served: false` flag prevents conflicts with ROSA HCP route API checks on real OCP clusters.
- **Custom ArgoCD Namespace**: The `GitOpsCluster` CR supports `spec.argoServer.argoNamespace` to specify a custom namespace for the ArgoCD instance on the hub (instead of the CR's own namespace). The `GetEffectiveArgoNamespace(gitOpsCluster)` helper in `gitopscluster_controller.go` resolves this: it returns `spec.argoServer.argoNamespace` if set, otherwise falls back to the CR's namespace. This helper **must** be used for all operations targeting the ArgoCD instance (cert generation, CA propagation, AddOnTemplate signing CA namespace, service discovery). Without it, certificates and secrets are created in the wrong namespace and agent TLS fails. Scenario 7 in `test-scenarios.sh` validates this end-to-end.
- **Cert Rotation & Autorefresh**: The `argocd-agent-client-tls` cert has a **24-hour validity**. The OCM `ClientCertController` (in the klusterlet-agent on the managed cluster) handles rotation: it monitors cert expiry, creates a new CSR on the hub at ~80% of lifetime (~19.2h), and **updates** the source secret (`gitops-addon-open-cluster-management.io-argocd-agent-addon-client-cert` in `open-cluster-management-agent-addon`). Nothing deletes the expired secret — the refresh happens via in-place update. The `SecretReconciler` in `gitopsaddon/secret_controller.go` propagates this to the target (`argocd-agent-client-tls` in `openshift-gitops`) via three mechanisms: (1) **source secret watch** — detects create/update/delete events and copies immediately, (2) **target secret deletion watch** — if the target is deleted, instantly re-copies from source, (3) **periodic requeue** (`SecretResyncInterval = 5 minutes`) — catches missed watch events (e.g., from network blips or pod restarts) by periodically verifying source and target are in sync. The `secretDataEqual()` helper avoids unnecessary updates during periodic checks. **Crucially**, when the cert data actually changes (rotation detected), the reconciler automatically **rolling-restarts** all ArgoCD agent Deployments in the target namespace (label `app.kubernetes.io/part-of=argocd-agent`) by patching the pod template annotation `apps.open-cluster-management.io/cert-rotated-at`. This is required because the argocd-agent binary reads TLS certs at startup and does not hot-reload them — without the restart, the agent would keep using the expired cert in memory and lose connection to the hub principal. The restart only happens when cert data actually changes, NOT on periodic resyncs where data is already in sync. Scenario 2 in `test-scenarios.sh` verifies cert rotation resilience by deleting the secret on each managed cluster and confirming recreation.
- **Cluster Secret Server URL (Resource Proxy vs NodePort)**: In agent mode, the hub controller creates ArgoCD cluster secrets whose `server` field uses one of two URL formats depending on what's available in the cluster. **Preferred (Red Hat operator)**: the in-cluster resource proxy service URL `https://openshift-gitops-agent-principal-resource-proxy.<argocd-ns>.svc:9090?agentName=<cluster-name>` — the resource proxy forwards hub ArgoCD UI live-manifest / resource-tree requests to the appropriate agent. **Fallback (upstream / embedded argocd-operator)**: when no resource proxy service exists (e.g. `app.kubernetes.io/part-of=argocd-agent` services with a `resource-proxy` port), the controller falls back to the external NodePort / LoadBalancer principal address `https://<node-ip>:<nodeport>?agentName=<cluster-name>`. This fallback is critical for the embedded e2e tests (upstream argocd-operator creates no resource proxy sidecar). The routing to the correct agent is determined by the `argocd-agent.argoproj-labs.io/agent-name` label on the secret, not solely by the `server` URL. Verify with: `kubectl get secret cluster-<cluster-name> -n openshift-gitops -o jsonpath='{.data.server}' | base64 -d`.
- **Non-Agent → Agent Mode Transition (Duplicate Cluster Name)**: When a `GitOpsCluster` is first created without agent mode, the controller creates a traditional blank pull-model cluster secret (e.g. `<cluster>-application-manager-cluster-secret`, label `apps.open-cluster-management.io/data-source=blank`) with `data["name"] = <cluster>`. When agent mode is later enabled, the controller creates a new agent cluster secret (`cluster-<cluster>`) with the same `data["name"]`. ArgoCD errors on duplicate cluster names: `"there are 2 clusters with the same name: [<agent-url> <traditional-url>]"` causing ApplicationSet generators to fail. The orphan-secret cleanup (`cleanupOrphanSecrets`) will NOT remove the old secret because the `ManagedCluster` still exists — it only removes secrets for deleted clusters. The fix: `CreateArgoCDAgentClusters` calls `deleteBlankClusterSecretsForCluster` after each successful agent secret create/update. This lists secrets matching `apps.open-cluster-management.io/acm-cluster=true` + `apps.open-cluster-management.io/cluster-name=<cluster>` + `apps.open-cluster-management.io/data-source=blank` and deletes them. **Only blank secrets are removed** — non-blank traditional secrets (e.g. MSA-backed secrets with `data-source=managed-service-account` and real credentials) are intentionally left untouched.
- **Agent SA View ClusterRole Binding (Live Manifest)**: For the ArgoCD UI live manifest view to work on OCP managed clusters, the ArgoCD agent ServiceAccount (`acm-openshift-gitops-agent-agent` in `openshift-gitops`) must have the `view` ClusterRole bound to it. Without this, the resource proxy cannot query the managed cluster for live resource state and the UI shows `"Resource not found in cluster: <resource-name>"`. Create the binding manually or via the Policy: `kubectl create clusterrolebinding acm-openshift-gitops-argocd-agent-cluster-reader --clusterrole=view --serviceaccount=openshift-gitops:acm-openshift-gitops-agent-agent` on the managed cluster.
- **Destination-Based Mapping Consistency (Agent + Principal)**: The `destinationBasedMapping` setting in the ArgoCD CR controls the Redis key format used to store resource tree data. Principal and agent **must** use the same setting, otherwise their Redis keys diverge and the ArgoCD UI live manifest fails with `"Resource not found in cluster: <resource-name>"`. The agent logs show: `"unexpected key format, missing '_': 'app|resources-tree|<cluster>-<appname>|<version>.gz'"`. The `|` separator in the error means DBM is disabled on the agent while the principal uses the `_` separator (DBM enabled). Fix: set `spec.argoCDAgent.agent.client.destinationBasedMapping: true` in the agent's ArgoCD CR (in the Policy's object templates) to match the principal's setting.
- **Autonomous Agent Mode**: When `argoCDAgent.mode: "autonomous"` is set in the GitOpsCluster spec, agents run in autonomous mode instead of managed mode. Both modes reconcile Applications **locally** on the spoke via the ArgoCD application-controller — the key difference is the **source of truth**. In managed mode, the hub (principal) is the source of truth and dispatches Application specs to agents. In autonomous mode, the spoke is the source of truth — Applications are created directly on the managed cluster (via Git/Policy/kubectl), and the agent syncs specs and status back to the hub principal, which acts as a **read-only mirror** (users can inspect but cannot modify or delete autonomous apps via the hub UI/CLI/API). Key implementation details: (1) the `default` AppProject is included in the generated Policy (autonomous agents need it for local reconciliation), (2) Applications are typically deployed to the managed cluster via OCM Policy or Git (not via principal dispatch), (3) the `ARGOCD_AGENT_MODE=autonomous` env var is propagated to the addon agent via `AddOnDeploymentConfig`, which sets `spec.argoCDAgent.agent.client.mode: autonomous` in the ArgoCD CR, (4) AppProjects synced from autonomous agents are prefixed with the agent name on the hub (e.g., `default` becomes `ocp-cluster1-default`). Autonomous mode is the recommended pattern for App of Apps workflows where all ongoing changes flow through Git after initial bootstrap. See `docs/autonomous-mode-best-practices.md` for a comprehensive guide. Known limitation: autonomous mode on `local-cluster` (hub) has conflicts because the agent transforms Application specs (project name, destination) which fights with Policy enforcement. Reference: [argocd-agent docs](https://github.com/argoproj-labs/argocd-agent) — `docs/concepts/agent-modes/autonomous.md`, `docs/user-guide/applications.md`.

## Development Workflow

1. **Prerequisites**: Go 1.25+ and access to a Kubernetes cluster with OCM installed
2. **Testing**: Always run `make test` before submitting changes
3. **Code Generation**: Run `make generate` after modifying API types in `pkg/apis/`
4. **Manifests**: Run `make manifests` after changing RBAC or CRD annotations
5. **CRD Sync**: When updating to a new OpenShift GitOps Operator version:
   - Extract CRDs from the live hub (`~/Desktop/hub`) using `kubectl get crd <name> -o yaml`, strip runtime metadata (`uid`, `resourceVersion`, `creationTimestamp`, `generation`, managed fields, OLM-specific labels/annotations), and place in `gitopsaddon/charts/openshift-gitops-operator/templates/crds/` and `deploy/crds/`.
   - Update image SHAs in `pkg/utils/config.go` (`DefaultOperatorImages` map).
   - Update `gitopsaddon/charts/openshift-gitops-operator/Chart.yaml` version.
   - **Critical**: Check for Go template expressions (e.g., `{{ .app.path.path }}`) in CRD description fields and escape them for Helm (see E2E Testing Gotchas).
   - Update the ClusterRole in `templates/openshift-gitops-operator-manager-role.clusterrole.yaml` if RBAC rules changed (e.g., `argocdexports` was removed in v1.20.3).
6. **Integration Testing**: Use `gitopsaddon/test-scenarios.sh` against real ACM clusters. **WARNING: Scenarios must run sequentially, never in parallel** — they modify shared hub state (GitOpsCluster, Policy, MCA, ArgoCD CRs). Run `bash test-scenarios.sh all` or individual scenarios one at a time.
7. **E2E Tests**: Run `make test-local-e2e-gitopsaddon-embedded-agent` for local e2e (creates Kind clusters automatically). Use `make test-local-e2e-*` targets (not `make test-e2e-*`) to mirror CI behavior — they create fresh Kind clusters, build the image, and run setup from scratch.
8. **Documentation**: After any code change or when learning new context about the codebase (architecture, behaviors, gotchas, testing patterns, environment setup), always update both `CLAUDE.md` and `gitopsaddon/README.md` to reflect the new knowledge. Future chat sessions start fresh and rely on these files for context.
9. **Ask before acting**: If confused about requirements, scope, or trade-offs, always ask clarifying questions before proceeding. Prefer switching to plan mode for large or ambiguous tasks.

## Test Environments

### test-scenarios.sh (Real ACM Clusters)
Runs against a real ACM hub + OCP managed cluster + Kind managed cluster. Environment variables:
- `HUB_KUBECONFIG` — Hub kubeconfig (default: `~/Desktop/hub`)
- `KIND_KUBECONFIG` — Kind cluster kubeconfig (default: `~/Desktop/kind-cluster1`)
- `OCP_KUBECONFIG` — OCP cluster kubeconfig (default: `~/Desktop/ocp-cluster1`)

Scenarios run sequentially. A **full `cleanup_all`** runs before each scenario by default to guarantee pristine state (avoids duplicate ArgoCD CRs and stale resources from prior runs). `cleanup_all` force-deletes all hub resources, runs `cleanup_managed_cluster_direct` on each managed cluster (which removes ArgoCD CRs, OLM artifacts, orphaned deployments/statefulsets, stale cleanup Jobs, and OperatorGroups), deletes stale pre-delete ManifestWorks, then **restarts the hub controller** (rollout restart + leader lease deletion) to clear stale informer cache entries that cause UID mismatch errors. The exception is S5 (Drift Heal), which intentionally reuses S2 (Agent Mode) state since it validates drift detection on an already-running agent deployment. Flow: `cleanup_all` → S1 → cleanup → `cleanup_all` → S2 → S5 (reuses S2 state, no `cleanup_all`) → cleanup → `cleanup_all` → S3 → cleanup → `cleanup_all` → S4 → cleanup → `cleanup_all` → S6 → cleanup → `cleanup_all` → S7 → cleanup. Wait timeouts: MCA creation and OCP-specific operations use 600s; non-OCP operations use 300s; cleanup timeouts are 120-180s with force-delete fallback (finalizer stripping + `--force --grace-period=0`).

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
- **e2e-gitopsaddon (embedded-autonomous)**: `make test-e2e-gitopsaddon-embedded-autonomous` — tests autonomous agent mode flow + Policy-based app deployment + local-cluster infrastructure verification

To run locally what CI runs: `make test-local-e2e-gitopsaddon-embedded` (creates kind clusters, builds image, runs tests).

## Testing Framework

Uses Ginkgo/Gomega for e2e testing with kubebuilder tools for unit tests. Unit tests require downloading kubebuilder assets via `make ensure-kubebuilder-tools`.

### E2E Testing Gotchas

- **Resource proxy service in embedded e2e**: The hub controller's `CreateArgoCDAgentClusters` prefers the in-cluster resource proxy URL (`https://openshift-gitops-agent-principal-resource-proxy.<ns>.svc:9090?agentName=<cluster>`) for cluster secrets when the resource proxy Service exists. The Red Hat OpenShift GitOps operator creates this service automatically. The upstream `argoprojlabs/argocd-operator` only creates the NodePort gRPC service but the principal pod's argocd-agent binary still listens on port 9090 for resource proxy requests. `setup_env.sh` explicitly creates the `openshift-gitops-agent-principal-resource-proxy` ClusterIP Service after the principal pod is Ready to replicate the Red Hat operator behaviour. Without this service, `FindArgoCDAgentResourceProxyService` fails and the controller falls back to the NodePort URL (which also works, but using the resource proxy URL is the intended production path). `verifyClusterSecret` asserts that the resource proxy service exists and that the cluster secret uses the resource proxy URL.
- **CI vs local timing**: GitHub Actions runners are slower than local dev machines. ArgoCD Application sync can take significantly longer (or stay `OutOfSync` for minutes) on CI. Avoid asserting `Synced` status with tight timeouts — prefer checking resource existence or controller state instead.
- **`make test-local-e2e-*` vs `make test-e2e-*`**: The `test-local-*` targets create fresh Kind clusters and run full setup; `test-e2e-*` targets reuse existing clusters. Always use `test-local-*` to mirror CI behavior when validating changes.
- **Principal container naming**: The upstream ArgoCD operator names the principal container after the deployment (e.g., `openshift-gitops-agent-principal`), while the Red Hat operator names it `"principal"`. The `findContainerImage(containers, deploymentName)` helper handles both conventions by checking `"principal"` first, then the deployment name, then `containers[0]` as a last resort.
- **`test-scenarios.sh` Policy compliance check**: The `wait_for_policy_compliant` function uses exact string equality (`= 'Compliant'`), not `grep`. Using `grep -q 'Compliant'` is a bug because it matches both `"Compliant"` and `"NonCompliant"` (substring match).
- **Helm template expressions in CRDs**: CRD descriptions extracted from the hub may contain Go template expressions like `{{ .app.path.path }}`. When these CRDs are placed in a Helm chart (`gitopsaddon/charts/`), Helm tries to evaluate them, causing `nil pointer evaluating interface {}.path` errors. These must be escaped as `{{ "{{" }} .app.path.path {{ "}}" }}`. The `imageupdaters.argocd-image-updater.argoproj.io.crd.yaml` CRD is a known instance of this. Always check for `{{` in CRD description fields after extracting from the hub.
- **Kubeconfig contamination with e2e tests**: If `KUBECONFIG` is set to the hub kubeconfig (e.g., `~/Desktop/hub`) when running `make test-local-e2e-*`, the `kind create cluster` command writes Kind cluster contexts directly into the hub kubeconfig, corrupting it. Always `unset KUBECONFIG` before running e2e tests, or ensure it points to `~/.kube/config`.
- **`cleanup_scenario` namespace handling**: The `cleanup_scenario` function accepts an optional `CLEANUP_NS` environment variable to specify the ArgoCD namespace for cleanup. This is needed for Scenario 7 (custom namespace). Without it, cleanup operations default to `openshift-gitops` and miss resources in custom namespaces.
- **local-cluster guestbook known limitation**: In the embedded-agent e2e test, `local-cluster-guestbook` is generated by the ApplicationSet in `openshift-gitops`. The local-cluster ArgoCD agent (running on the same cluster as the hub) has a cluster-wide informer that sees this hub-side Application in `openshift-gitops`. When the principal dispatches the Application to the local-cluster agent (to create it in `local-cluster` namespace), the agent's identity check finds the existing hub-side Application (which lacks the argocd-agent source UID annotation) and fails with `"source UID Annotation is not found for app: local-cluster-guestbook"`. This is a fundamental conflict for local-cluster agent mode on the same cluster as the hub: Applications with the same name exist in both `openshift-gitops` (created by ApplicationSet, no agent annotation) and `local-cluster` (target of dispatch), and the agent's cluster-wide identity check cannot distinguish them. The agent IS connected (gRPC recv events confirmed), but Application dispatch fails. The test checks for guestbook propagation as a best-effort/warning (not a hard failure), consistent with `test-scenarios.sh` behavior. The connectivity itself (agent pod running, `cluster-local-cluster` secret with `agentName` label, `argocd-agent-ca` secret) IS verified as hard assertions.
- **local-cluster argocd-agent-ca direct write**: For `local-cluster`, `PropagateHubCA` writes the `argocd-agent-ca` secret directly into the `local-cluster` namespace via the hub controller's Kubernetes client instead of via ManifestWork. ManifestWork is processed asynchronously by the work-agent, which can race against the ArgoCD agent pod startup — the agent may attempt its first TLS connection to the principal before the CA secret lands, enter a long retry loop, and time out the e2e test. Direct write eliminates this race. Remote clusters still use ManifestWork because the hub has no direct API access to spoke clusters. The e2e test also explicitly waits for `argocd-agent-ca` in the `local-cluster` namespace before checking for the `local-cluster-guestbook` Application.
- **UID mismatch / StorageError after cleanup**: When a GitOpsCluster is rapidly deleted and recreated, the hub controller's informer cache may hold the old UID. Status updates fail with `StorageError: invalid object, UID mismatch` and the controller enters a 3-minute retry loop, blocking MCA creation for other clusters. The fix is to restart the hub controller (`rollout restart`) and delete the leader election lease to clear the cache. `cleanup_all` does this automatically.
- **OperatorGroup must be AllNamespaces**: The `gitops-addon-operator-group` OperatorGroup created by the addon agent must NOT have `targetNamespaces` set (i.e., AllNamespaces mode). The openshift-gitops-operator CSV does not support `OwnNamespace` InstallModeType. Setting `targetNamespaces: [openshift-gitops-operator]` causes the CSV to fail immediately with `"OwnNamespace InstallModeType not supported"`. This was a critical bug that caused all OCP scenarios to fail — OLM never deployed the operator, so ArgoCD was never reconciled.
- **Orphaned ArgoCD deployments after cleanup**: When `cleanup_managed_cluster_direct` deletes the OLM subscription, CSV, and operator deployment, the ArgoCD deployments/statefulsets created by the operator (e.g., `acm-openshift-gitops-redis`, `acm-openshift-gitops-repo-server`, `acm-openshift-gitops-application-controller`) may survive because the operator can't clean them up if it's already gone. These orphans consume resources and confuse subsequent scenario runs. `cleanup_managed_cluster_direct` now explicitly deletes them.
- **Pre-delete ManifestWork race**: When a MCA is deleted, OCM creates a pre-delete ManifestWork that runs a cleanup Job on the managed cluster. If the cleanup Job is slow and a new MCA is created, the old pre-delete MW can interfere (e.g., deleting resources the new MW just applied). `cleanup_all` now deletes stale `addon-gitops-addon-pre-delete` ManifestWorks and kills stale cleanup Jobs on managed clusters.
- **OCP OLM timing**: On real OCP clusters, OLM catalog resolution + InstallPlan + CSV installation can take 30-120 seconds even when healthy. After a full cleanup that removes the CSV and operator, OLM must re-resolve the package from the catalog, which involves gRPC calls to the catalog pod. If the catalog pod was recently restarted, this can take even longer. The test script uses 600s timeouts for OLM operations.
- **MCA cleanup finalizer timeout**: The OCM addon framework removes MCA finalizers only after the pre-delete Job completes (which runs `gitopsaddon -cleanup`). On OCP this can take 2-3 minutes as OLM uninstalls. `cleanup_scenario` waits 180s then falls back to stripping finalizers and force-deleting. `cleanup_all` strips finalizers immediately (no waiting).

## Container Registry

Default registry: `quay.io/stolostron`
Configure via: `REGISTRY` and `VERSION` environment variables
