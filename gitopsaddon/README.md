# GitOps Addon - Configuration Guide

This guide explains how to configure and test the GitOps Addon functionality using ACM hub and managed clusters.

## Key Concepts

- **GitOpsCluster CR**: Defines which managed clusters should have ArgoCD deployed via the GitOps addon
- **Placement**: Selects which managed clusters are targeted (including `local-cluster` / hub). Requires a `ManagedClusterSetBinding` for the `default` ManagedClusterSet in the `openshift-gitops` namespace — without it, Placement cannot find any ManagedClusters.
- **Policy**: The GitOpsCluster controller creates a Policy that deploys the ArgoCD CR to managed clusters. **This Policy is user-owned** — users can modify it to add RBAC, Applications, or customize ArgoCD settings. The controller will recreate the Policy if it is deleted (unless `skip-argocd-policy` annotation is set) and may patch the `argoCDAgent.agent.image` field during agent version drift heal, but will not overwrite other user customizations. For `local-cluster`, the ArgoCD CR is deployed to the `local-cluster` namespace (not `openshift-gitops`) using a hub template.
- **ManagedClusterAddOn**: Created automatically for ALL managed clusters including `local-cluster`. Manages addon lifecycle.
- **Addon RBAC**: The `gitops-addon` ServiceAccount uses a fine-grained `ClusterRole` named `gitops-addon` (not `cluster-admin`). Defined in `addonTemplates/addonTemplates.yaml` (static) and `addon_template_management.go` (dynamic, for agent mode). Includes `escalate`/`bind` on RBAC resources (needed because the addon deploys the ArgoCD operator which creates its own Roles/ClusterRoles), and `delete` on all resource types that the cleanup Job needs to remove. A separate `gitops-addon-cleanup` ClusterRole/ClusterRoleBinding is annotated with `addon-pre-delete` and included in the pre-delete ManifestWork, ensuring the cleanup Job retains RBAC even after the regular ManifestWork (which deploys the main `gitops-addon` ClusterRole) is deleted during MCA teardown.
- **Cluster Secrets**: For agent mode, the controller creates ArgoCD cluster secrets with proper server URLs including `agentName` query parameter
- **DISABLE_DEFAULT_ARGOCD_INSTANCE**: On OCP managed clusters, the addon creates an OLM Subscription with `DISABLE_DEFAULT_ARGOCD_INSTANCE=true`. On non-OCP clusters, the embedded operator chart is deployed. On the hub (`local-cluster`), the operator is already present so no installation occurs.
- **OLM Subscription Override**: When `olmSubscription.enabled: true` is set in the GitOpsCluster spec, the addon agent is **forced** to use OLM subscription mode for operator installation, bypassing OCP auto-detection. This ensures OLM is used even if the cluster detection fails or the cluster is non-OCP (though OLM must be available on the cluster for the installation to succeed). Custom subscription values (channel, source, namespace, etc.) are passed to the addon agent via `AddOnDeploymentConfig` environment variables. When absent or disabled, the agent falls back to OCP auto-detection and uses hardcoded defaults (`channel: latest`, `source: redhat-operators`, etc.).
- **AppProject Propagation (Agent Mode)**: In managed agent mode, the ArgoCD principal propagates AppProjects from the hub to managed clusters. In autonomous agent mode, AppProjects are created locally on the spoke and synced back to the hub with the agent name prefixed (e.g., `default` becomes `ocp-cluster1-default`). The `default` AppProject is included in the generated Policy for autonomous mode to ensure local reconciliation works before any agent sync occurs.
- **Default AppProject Wildcard Settings (Agent Mode Prerequisite)**: The `default` AppProject in `openshift-gitops` on the hub must have wildcard settings: `sourceNamespaces: ["*"]`, `destinations: [{name:"*", namespace:"*", server:"*"}]`, `clusterResourceWhitelist: [{group:"*", kind:"*"}]`, `sourceRepos: ["*"]`. The principal only propagates AppProjects whose destinations and sourceNamespaces match the agent's configuration. Without wildcards, propagation is skipped and agents fail with "project not found".
- **Destination-Based Mapping (Agent Architecture)**: The principal keeps `destinationBasedMapping: true` for **routing** — it dispatches Applications to agents based on `spec.destination.name`. The **agents** have `destinationBasedMapping` disabled (not set in the ArgoCD CR). This is critical for `local-cluster`: the agent's `getTargetNamespaceForApp()` returns its own namespace (`local-cluster`), so agent-created Application copies go to the `local-cluster` namespace where the app controller watches. For remote clusters (Kind/OCP), the agent's namespace IS `openshift-gitops`, so the behavior is identical whether DBM is enabled or disabled on the agent. If agent DBM were enabled, the agent would keep the original namespace (`openshift-gitops`) from the hub — this fails on `local-cluster` because the app controller runs in `local-cluster`, not `openshift-gitops`, and it conflicts with existing ApplicationSet-generated apps in `openshift-gitops`. **Critical gotcha**: For OCP remote clusters in managed mode where you want live manifest in the ArgoCD UI, the agent's `destinationBasedMapping` must match the principal's. If they differ, the Redis cache key format diverges (`|` vs `_` separator) and the UI shows `"Resource not found in cluster: <resource>"` with agent log error `"unexpected key format, missing '_': 'app|resources-tree|...'`. Fix by setting `spec.argoCDAgent.agent.client.destinationBasedMapping: true` in the agent's ArgoCD CR template inside the Policy.
- **Cluster Secret Resource Proxy URL**: In agent mode, the hub controller creates ArgoCD cluster secrets whose `server` field uses the resource proxy service URL format: `https://openshift-gitops-agent-principal-resource-proxy.<argocd-ns>.svc:9090?agentName=<cluster-name>`. This is an in-cluster URL pointing to the resource proxy sidecar in the principal pod; the `agentName` query parameter tells the proxy which agent cluster to route API requests to for live manifest and resource tree lookups. Verify with: `kubectl get secret cluster-<cluster-name> -n openshift-gitops -o jsonpath='{.data.server}' | base64 -d`.
- **Agent SA View ClusterRole (Live Manifest)**: For the ArgoCD UI live manifest view to work, the ArgoCD agent ServiceAccount on the managed cluster must have read access to cluster resources. Create a ClusterRoleBinding on the managed cluster: `kubectl create clusterrolebinding acm-openshift-gitops-argocd-agent-cluster-reader --clusterrole=view --serviceaccount=openshift-gitops:acm-openshift-gitops-agent-agent`. Without this, the resource proxy cannot query the managed cluster and the UI shows `"Resource not found in cluster: <resource-name>"`. This can be added to the Policy's object templates so it is applied via OCM governance.
- **Image Pull Secret Handling**: Two mechanisms work together:
  1. **Secret copying (OCM klusterlet)**: The addon agent labels namespaces it creates (`openshift-gitops-operator`, `openshift-gitops`) with `addon.open-cluster-management.io/namespace: true`. This label tells the OCM klusterlet to automatically copy the `open-cluster-management-image-pull-credentials` secret into those namespaces. No custom propagation code is needed for this step.
  2. **ServiceAccount patching (addon agent)**: On non-OCP clusters, the ArgoCD operator creates ServiceAccounts without `imagePullSecrets` references. Since the ArgoCD CRD has no native `imagePullSecrets` field, the addon agent patches all ServiceAccounts in the ArgoCD namespace to reference `open-cluster-management-image-pull-credentials` (via `patchArgoCDServiceAccountsWithImagePullSecrets`). It also deletes pods stuck in `ImagePullBackOff`/`ErrImagePull` so they restart with the patched SAs. On OCP clusters, node-level pull secrets handle registry authentication, so this patching is skipped.
  3. **CRITICAL: Hub `multiclusterhub-operator-pull-secret` MUST include `registry.redhat.io`**: The entire chain above works correctly — but ONLY if the source secret has the right registries. This secret exists in **TWO namespaces** on the hub: `open-cluster-management` AND `multicluster-engine`. The `managedcluster-import-controller` reads from the `multicluster-engine` copy (env var `DEFAULT_IMAGE_PULL_SECRET`), bakes it into the `{cluster}-import` secret, which feeds into the klusterlet ManifestWork. The klusterlet copies it to managed clusters as `open-cluster-management-image-pull-credentials`. If EITHER copy is missing `registry.redhat.io`, ALL non-OCP managed clusters (EKS, Kind, etc.) will fail with `ImagePullBackOff` / `401 Unauthorized`. The OCP hub is unaffected because OCP nodes have `registry.redhat.io` at the node level (via `openshift-config/pull-secret`), which masks this issue entirely. **Fix**: Merge the OCP global pull-secret into BOTH copies of the MCH pull-secret, then delete `{cluster}-import` secrets to force regeneration. The addon framework handles everything else. **This is the #1 root cause of EKS/non-OCP clusters failing to deploy ArgoCD. Non-OCP support is the entire point of gitopsaddon. Always verify this before testing.**
- **Smart Cluster Detection**: The addon agent detects the cluster type at runtime:
  - **OCP detection**: Checks for `clusterversions.config.openshift.io` CRD, `infrastructures.config.openshift.io` CRD, `version.openshift.io` ClusterClaim, or `product.open-cluster-management.io` ClusterClaim with value "OpenShift"
  - **Hub detection**: Checks for `ClusterManager` resources or a `ManagedCluster` with name `local-cluster` or label `local-cluster=true`
- **ARGOCD_NAMESPACE**: Environment variable passed to the addon agent via `AddOnDeploymentConfig`. Set to `local-cluster` for the hub and `openshift-gitops` for remote clusters. Controls where ArgoCD CR lives and where client certs are copied.
- **ARGOCD_CLUSTER_CONFIG_NAMESPACES**: Environment variable on the `openshift-gitops-operator` Deployment (set via OLM Subscription). Controls which namespaces get cluster-scoped RBAC for their ArgoCD instances. When a namespace is listed, the operator creates ClusterRoles/ClusterRoleBindings granting cluster-scope `list`/`watch` on `namespaces` and other resources to the ArgoCD application-controller, agent, and principal ServiceAccounts. **Must be set to `openshift-gitops,local-cluster`** for agent mode — `openshift-gitops` for the principal and remote agents, `local-cluster` for the hub-local agent. Without this, agent/principal pods crash with `"namespaces is forbidden"` errors because only namespace-scoped Roles are created. On a fresh ACM hub, the default is typically `openshift-gitops` only.
- **Agent Version Drift Heal**: In agent mode, the hub controller **watches** the ArgoCD principal Deployment for container image changes. When the OpenShift GitOps Operator upgrades the principal (changing the image), the controller is automatically triggered and patches the ArgoCD Policy to enforce the correct `spec.argoCDAgent.agent.image` on managed clusters, ensuring spoke agents stay compatible with the hub principal without waiting for another reconciliation event. Only the addon-managed ArgoCD template named `acm-openshift-gitops` is patched; user-added ArgoCD templates in the Policy are left untouched. This is disabled with the `apps.open-cluster-management.io/skip-agent-version-heal: "true"` annotation on the GitOpsCluster CR.
- **Policy Recreation Control**: The ArgoCD Policy is created once and left for users to customize. If a user deletes the Policy intentionally, the controller will recreate it on the next reconcile. To prevent this, set `apps.open-cluster-management.io/skip-argocd-policy: "true"` on the GitOpsCluster CR. When skipped, the `ArgoCDPolicyReady` condition is set to `True` with `Reason=Skipped`.
- **Custom ArgoCD Namespace**: The `GitOpsCluster` CR supports `spec.argoServer.argoNamespace` to specify a custom namespace for the hub ArgoCD instance. The hub controller uses `GetEffectiveArgoNamespace()` to resolve the correct namespace for cert generation, CA propagation, AddOnTemplate signing CA, and service discovery. A `ManagedClusterSetBinding` for `default` must also exist in the custom namespace.
- **Cert Rotation & Autorefresh**: The `argocd-agent-client-tls` cert has a 24-hour validity. The OCM `ClientCertController` (klusterlet-agent) rotates the cert at ~80% of lifetime by creating a new CSR, and updates the source secret in the addon namespace. The `SecretReconciler` (`secret_controller.go`) propagates this to the ArgoCD namespace via: (1) source secret update watch (immediate copy), (2) target secret deletion watch (instant re-copy if deleted), (3) **periodic requeue every 5 minutes** (`SecretResyncInterval`) as a safety net against missed watch events. The `secretDataEqual()` helper avoids unnecessary API calls during periodic checks. **When cert data actually changes**, the reconciler automatically **rolling-restarts** all ArgoCD agent Deployments (`app.kubernetes.io/part-of=argocd-agent`) in the target namespace by annotating the pod template. This is required because the argocd-agent binary reads TLS certs at startup and does not hot-reload — without the restart, the agent keeps the expired cert in memory and loses connection. The restart is only triggered on actual cert changes, not on periodic resyncs where data is already in sync. No manual `kubectl rollout restart` is needed.

## Architecture

### Template Modes

The system uses 2 addon template modes:

| Mode | Template Name | When Used |
|------|---------------|-----------|
| **Static** | `gitops-addon` | `gitopsAddon.enabled=true`, `argoCDAgent.enabled=false` |
| **Dynamic** | `gitops-addon-{ns}-{name}` | `gitopsAddon.enabled=true`, `argoCDAgent.enabled=true` (includes `RegistrationSpec` for client cert provisioning) |

OLM vs. embedded operator installation is determined at runtime by the addon agent. When `olmSubscription.enabled: true` is set in the GitOpsCluster spec, OLM mode is forced (bypassing auto-detection). Otherwise, the agent auto-detects OCP clusters for OLM and falls back to embedded manifests for non-OCP clusters. **OperatorGroup**: Before creating the OLM Subscription, the addon agent creates an **AllNamespaces** OperatorGroup (`gitops-addon-operator-group`) in the subscription namespace. It must NOT use `OwnNamespace` mode (no `targetNamespaces`) because the openshift-gitops-operator CSV does not support `OwnNamespace` InstallModeType — using it causes the CSV to fail immediately with `"OwnNamespace InstallModeType not supported"`. If an OperatorGroup already exists in the namespace, it is left unchanged.

### Local-Cluster (Hub) Support

When `local-cluster` is included in the Placement:
1. The hub controller creates a `ManagedClusterAddOn` for `local-cluster` (no longer skipped)
2. The `AddOnDeploymentConfig` sets `ARGOCD_NAMESPACE=local-cluster`
3. The Policy deploys the ArgoCD CR to the `local-cluster` namespace (via hub template)
4. The addon agent on the hub detects it is a hub cluster and skips operator installation
5. The secret controller copies client certs to the `local-cluster` namespace (not `openshift-gitops`)
6. The hub CA is propagated to `local-cluster` via ManifestWork targeting the `local-cluster` namespace
7. Cleanup on hub only deletes addon-created ArgoCD CRs — operator and OLM resources are preserved

## Supported Scenarios

All scenarios target `local-cluster` (hub), `ocp-cluster1` (OCP), and `kind-cluster1` (Kind) unless noted.

| # | Scenario | Clusters | Application Creation | Success Criteria |
|---|----------|----------|---------------------|------------------|
| **1** | No Agent (auto-detect) | OCP + Kind + local-cluster | Policy deploys guestbook Application to all 3 | OLM auto-detected on OCP, embedded operator on Kind, ArgoCD + guestbook on all 3 |
| **2** | Agent Mode (auto-detect) | OCP + Kind + local-cluster | ApplicationSet on hub via principal/agent | Agents connected, destination-based mapping, Red Hat images, guestbook synced on all 3 |
| **3** | Custom OLM | OCP only | N/A (validates OLM config) | installPlanApproval=Manual, DISABLE_DEFAULT_ARGOCD_INSTANCE=true, no embedded operator |
| **4** | OLM Override on Kind | Kind only | N/A (validates OLM override) | olmSubscription.enabled forces OLM mode on Kind (no OCP auto-detect), agent attempts OLM subscription (fails gracefully since no OLM on Kind) |
| **5** | Agent Version Drift Heal | All (runs after S2) | N/A (validates drift heal) | Simulates principal image drift, verifies controller patches ArgoCD Policy agent image, verifies restore after drift correction |
| **6** | Autonomous Agent Mode | OCP + Kind + local-cluster | Policy deploys guestbook Application to spokes | Agents in autonomous mode, local reconciliation, status synced back to hub, AppProject included in Policy |
| **7** | Custom Hub ArgoCD Namespace | Kind + local-cluster | N/A (validates cert placement) | ArgoCD in `custom-gitops` namespace, CA cert in custom namespace, certs generated correctly |

**Notes:**
- On OCP, guestbook-ui pods **crash** due to OCP's `restricted-v2` SCC (port 80 binding). The test checks that the Deployment resource exists (ArgoCD synced it), not pod health. On Kind, pods run normally.
- Agent mode requires Red Hat registry access for agent images on non-OCP clusters (handled by OCM pull secret propagation).
- OLM customization (Scenario 3) is OCP-only. Non-OCP clusters always use embedded operator unless `olmSubscription.enabled: true` forces OLM mode (Scenario 4).
- `local-cluster` behaves as a fully managed cluster: gets its own ManagedClusterAddOn, client certs (agent mode), and ArgoCD instance in `local-cluster` namespace.
- `local-cluster` guestbook app may not fully reconcile due to agent timing — treated as a warning, not a failure.
- A full `cleanup_all` runs before each scenario to guarantee pristine state. It force-deletes hub resources, runs `cleanup_managed_cluster_direct` on each managed cluster (removes ArgoCD CRs, OLM artifacts, orphaned deployments/statefulsets, stale cleanup Jobs), deletes stale pre-delete ManifestWorks, and **restarts the hub controller** to clear informer cache UID mismatch errors. Between-scenario cleanup is also performed. OCP-specific wait timeouts use 600s (OLM catalog resolution is slow); non-OCP operations use 300s; cleanup timeouts are 120-180s with force-delete fallback (finalizer stripping).
- Scenario 2 includes cert rotation verification: deletes `argocd-agent-client-tls` on each managed cluster and verifies it is recreated within 120s.

## Prerequisites

- An ACM (Advanced Cluster Management) hub OpenShift cluster
- Managed clusters registered to the hub with proper labels
- **OpenShift GitOps operator installed on hub** via OLM Subscription in `openshift-gitops-operator` namespace. **Do NOT set `DISABLE_DEFAULT_ARGOCD_INSTANCE=true`** — the `GitopsService` CR creates the default ArgoCD instance, and disabling it prevents ArgoCD from starting.
- **ArgoCD instance with principal enabled**: The hub ArgoCD CR must include `spec.argoCDAgent.principal.enabled: true`. Without the principal pod, agent-mode managed clusters cannot connect. The gitopscluster controller's `VerifyArgocdNamespace` requires the ArgoCD server pod to be running — if it's not, the controller loops forever on `"invalid argocd namespace because argo server pod was not found"` and never creates MCAs, Policies, or AddOnTemplates. This is the #1 cause of "MCA stuck at Pending" on a fresh hub.
- A `ManagedClusterSetBinding` for `default` in the `openshift-gitops` namespace (without this, Placement finds zero clusters).
- For Agent mode: `ARGOCD_CLUSTER_CONFIG_NAMESPACES` set to `openshift-gitops,local-cluster` on the `openshift-gitops-operator` Subscription (see Key Concepts above). Without this, agent/principal pods in non-default namespaces (e.g., `local-cluster`) lack cluster-scoped RBAC and crash.
- For Agent mode: The `default` AppProject in `openshift-gitops` patched with wildcard `destinations`, `sourceNamespaces`, `clusterResourceWhitelist`, and `sourceRepos`. Without this, the principal won't propagate AppProjects to agents.

### Kind Managed Cluster Setup

To use a Kind cluster as a managed cluster for integration testing:

1. Create the Kind cluster: `kind create cluster --name kind-cluster1`
2. Export kubeconfig: `kind get kubeconfig --name kind-cluster1 > ~/Desktop/kind-cluster1`
3. Register as ManagedCluster on the hub (create ManagedCluster resource with `hubAcceptsClient: true`)
4. Extract import manifests from the auto-generated hub secret (`kind-cluster1-import`) and apply to the Kind cluster
5. Wait for `AVAILABLE=True` on the hub
6. **Critical**: Create `governance-policy-framework` and `config-policy-controller` ManagedClusterAddOns in the `kind-cluster1` namespace on the hub. Without these, the OCM Policy framework cannot enforce Policies on the Kind cluster, and Policy compliance will time out.

See `CLAUDE.md` for the full step-by-step commands.

### Running Test Scenarios

Scenarios can be run individually or all together. When running multiple scenarios, run them sequentially — each scenario modifies shared hub state (GitOpsCluster, Policy, ManagedClusterAddOn, ArgoCD CRs).

---

## Scenario 1, 2 & 3: Non-Agent Mode

In non-agent mode, ArgoCD runs independently on each managed cluster. Applications can be:
1. **Added to the Policy** (recommended) - The controller's Policy can be modified to include Applications
2. **Created directly on the managed cluster** - Using kubectl, ArgoCD CLI, or UI

### Step 1: Apply AddOnTemplates

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

kubectl apply -f gitopsaddon/addonTemplates/addonTemplates.yaml
```

### Step 2: Create Placement and GitOpsCluster

```bash
# Create a Placement to select managed clusters AND local-cluster (hub)
# Including local-cluster exercises the hub deployment path (ArgoCD CR in local-cluster namespace)
cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: my-placement
  namespace: openshift-gitops
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            name: my-managed-cluster
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            local-cluster: "true"
EOF

# Create GitOpsCluster with gitopsAddon enabled
# OLM vs. embedded operator is auto-detected by the addon agent (no config needed)
cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: my-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: my-placement
  gitopsAddon:
    enabled: true
EOF
```

### Step 3: Modify Policy to Add RBAC and Application

The GitOpsCluster controller creates a Policy but does NOT include RBAC or Applications by default. Users must modify the Policy.

**Important**: The Policy is user-owned. The controller recreates it if deleted (unless `skip-argocd-policy` annotation is set) and may patch `argoCDAgent.agent.image` during drift heal, but does not overwrite other user customizations.

```bash
# Wait for the Policy to be created
kubectl get policy my-gitops-argocd-policy -n openshift-gitops

# Add RBAC and Application to the Policy
# NOTE: The ServiceAccount namespace below must match the ArgoCD namespace on each
# target cluster. Use "openshift-gitops" for remote/spoke clusters, or "local-cluster"
# for hub-local deployments where ArgoCD is in the local-cluster namespace.
kubectl patch policy my-gitops-argocd-policy -n openshift-gitops --type=json -p='[
  {
    "op": "add",
    "path": "/spec/policy-templates/-",
    "value": {
      "objectDefinition": {
        "apiVersion": "policy.open-cluster-management.io/v1",
        "kind": "ConfigurationPolicy",
        "metadata": {
          "name": "my-gitops-argocd-policy-rbac"
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
            },
            {
              "complianceType": "musthave",
              "objectDefinition": {
                "apiVersion": "argoproj.io/v1alpha1",
                "kind": "Application",
                "metadata": {
                  "name": "guestbook",
                  "namespace": "openshift-gitops"
                },
                "spec": {
                  "project": "default",
                  "source": {
                    "repoURL": "https://github.com/argoproj/argocd-example-apps",
                    "targetRevision": "HEAD",
                    "path": "guestbook"
                  },
                  "destination": {
                    "server": "https://kubernetes.default.svc",
                    "namespace": "guestbook"
                  },
                  "syncPolicy": {
                    "automated": {
                      "prune": true,
                      "selfHeal": true
                    }
                  }
                }
              }
            }
          ]
        }
      }
    }
  }
]'
```

### Step 4: Verify Deployment

```bash
# Check Policy is compliant
kubectl get policy my-gitops-argocd-policy -n openshift-gitops

# On managed cluster, verify ArgoCD and guestbook
KUBECONFIG=/path/to/managed kubectl get argocd -n openshift-gitops
KUBECONFIG=/path/to/managed kubectl get pods -n guestbook
```

---

## Scenario 2: Agent Mode

In agent mode, ArgoCD agents run on managed clusters and connect to a principal on the hub. Applications are:
- **Created on the hub** in the `openshift-gitops` namespace (or any namespace watched by the principal)
- The principal uses **destination-based mapping** (`destinationBasedMapping: true` in the principal ArgoCD CR), where it routes Applications to agents based on `destination.name` matching the cluster name
- When using ApplicationSets with `clusterDecisionResource`, use `destination.name: '{{name}}'` so the OCM cluster name is used for routing
- **PlacementDecision**: The ApplicationSet's `clusterDecisionResource` generator uses the Placement controller's PlacementDecisions directly via `labelSelector: cluster.open-cluster-management.io/placement: <placement-name>`. No manual PlacementDecision workaround is needed — the `score` field bug (int64 panic in ArgoCD's DuckType processor) was fixed in OCM 2.17+
- **Agents receive Applications from the principal via gRPC and create copies in their own namespace** — the agent's own namespace is `openshift-gitops` for remote clusters and `local-cluster` for the hub. This means the `local-cluster` agent stores its Application copies in the `local-cluster` namespace, where the local app controller watches
- For ApplicationSet sync status on the hub: remote cluster apps show status in `openshift-gitops`, while `local-cluster` agent-created apps show status in the `local-cluster` namespace

### Hub Prerequisites for Agent Mode

**CRITICAL: Ensure `multiclusterhub-operator-pull-secret` includes `registry.redhat.io`** before proceeding. Without this, ALL non-OCP managed clusters will fail with `ImagePullBackOff`. See "Image Pull Secret Handling" above for details and the merge command.

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

# Configure hub ArgoCD as principal
kubectl patch argocd openshift-gitops -n openshift-gitops --type=merge -p '{
  "spec": {
    "controller": {
      "enabled": false
    },
    "argoCDAgent": {
      "principal": {
        "enabled": true,
        "auth": "mtls:CN=system:open-cluster-management:cluster:([^:]+):addon:gitops-addon:agent:gitops-addon-agent",
        "destinationBasedMapping": true,
        "namespace": {
          "allowedNamespaces": ["*"]
        },
        "server": {
          "route": {
            "enabled": true
          }
        }
      }
    },
    "sourceNamespaces": ["*"]
  }
}'
```

### Step 1: Create GitOpsCluster with Agent Mode

```bash
# Create Placement (includes local-cluster for hub deployment)
cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: agent-placement
  namespace: openshift-gitops
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            name: my-managed-cluster
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            local-cluster: "true"
EOF

# Create GitOpsCluster with Agent enabled
cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: agent-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: agent-placement
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: true
      mode: managed
    # olmSubscription:
    #   enabled: true      # Pass custom OLM values to OCP clusters
    #   channel: "stable"  # Override default OLM channel (default: latest)
    #   source: "redhat-operators"  # Override CatalogSource (default: redhat-operators)
EOF
```

### Step 2: Modify Policy to Add RBAC and Namespace

The Policy must include:
- **ClusterRoleBinding**: Grants `cluster-admin` to the ArgoCD application controller
- **Namespace**: Target namespace for the guestbook application

> **Note:** The `default` AppProject does NOT need to be in the Policy. The ArgoCD agent
> principal automatically propagates AppProjects from the hub to managed clusters. The `default` AppProject
> is created on the hub in the managed cluster's namespace alongside the Application.

```bash
kubectl patch policy agent-gitops-argocd-policy -n openshift-gitops --type=json -p='[
  {
    "op": "add",
    "path": "/spec/policy-templates/-",
    "value": {
      "objectDefinition": {
        "apiVersion": "policy.open-cluster-management.io/v1",
        "kind": "ConfigurationPolicy",
        "metadata": {
          "name": "agent-gitops-argocd-policy-rbac"
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
```

### Step 3: Get Server URL and Create Application on Hub

The GitOpsCluster controller auto-discovers the principal server address and stores it in the spec:

```bash
# Get the discovered server address
SERVER_ADDRESS=$(kubectl get gitopscluster agent-gitops -n openshift-gitops -o jsonpath='{.spec.gitopsAddon.argoCDAgent.serverAddress}')
SERVER_PORT=$(kubectl get gitopscluster agent-gitops -n openshift-gitops -o jsonpath='{.spec.gitopsAddon.argoCDAgent.serverPort}')
CLUSTER_NAME="my-managed-cluster"

# The cluster secret is created automatically with proper server URL
kubectl get secret cluster-${CLUSTER_NAME} -n openshift-gitops

# Create Application on hub in managed cluster namespace.
# With destination-based mapping enabled on the principal, use destination.name
# to route the Application to the correct agent.
cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook
  namespace: ${CLUSTER_NAME}
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps
    targetRevision: HEAD
    path: guestbook
  destination:
    name: ${CLUSTER_NAME}
    namespace: guestbook
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
EOF
```

### Step 4: Verify Agent Connection and Application

```bash
# Check cluster secret exists with proper server URL (resource proxy format)
# Expected output: https://openshift-gitops-agent-principal-resource-proxy.openshift-gitops.svc:9090?agentName=<cluster-name>
kubectl get secret cluster-${CLUSTER_NAME} -n openshift-gitops -o jsonpath='{.data.server}' | base64 -d

# Check principal logs for agent connections
kubectl logs -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-agent-principal --tail=20

# On managed cluster, check agent logs
KUBECONFIG=/path/to/managed kubectl logs -n openshift-gitops -l app.kubernetes.io/component=argocd-agent --tail=20

# Verify guestbook is deployed
KUBECONFIG=/path/to/managed kubectl get pods -n guestbook
```

---

## Scenario 6: Autonomous Mode

In autonomous mode, ArgoCD agents run on managed clusters and connect to the hub principal. Both managed and autonomous modes reconcile Applications **locally on the spoke** via the ArgoCD application-controller — the key difference is the **source of truth**. In managed mode, the hub is the source of truth and dispatches Application specs to agents. In autonomous mode, the spoke is the source of truth — Applications are created on the managed cluster (via Policy, Git, or `kubectl`), and the agent syncs both specs and status back to the hub principal, which acts as a **read-only mirror** (cannot modify or delete autonomous apps). This is the recommended pattern for the **App of Apps** bootstrap workflow. See `docs/autonomous-mode-best-practices.md` for a comprehensive guide.

> **Source**: Mode definitions verified against [argocd-agent](https://github.com/argoproj-labs/argocd-agent) docs — `docs/concepts/agent-modes/autonomous.md`, `docs/concepts/agent-modes/managed.md`, `docs/user-guide/applications.md`.

### Key Differences from Managed Mode

| Aspect | Managed Mode | Autonomous Mode |
|--------|-------------|-----------------|
| Source of truth | Hub (principal) | Spoke (workload cluster) |
| Application creation | On the hub; principal dispatches to agents | On the managed cluster (via Policy, Git, or `kubectl`) |
| Application reconciliation | Local on spoke (same as autonomous) | Local on spoke; agent syncs specs+status to hub |
| Hub capability | Full control — create, modify, delete apps | Read-only mirror — inspect only, no modifications |
| Spoke drift | Reverted by agent to match hub spec | N/A — spoke owns the spec |
| AppProject on hub | Original name | Prefixed with agent name (e.g., `ocp-cluster1-default`) |
| AppProject in Policy | Not included (principal propagates) | Included (needed for local reconciliation) |
| Best for | Centralized control | App of Apps, GitOps-first, edge/air-gap |

### Step 1: Create GitOpsCluster with Autonomous Mode

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

# Create Placement
cat <<EOF | kubectl apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: autonomous-placement
  namespace: openshift-gitops
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            name: my-managed-cluster
EOF

# Create GitOpsCluster with autonomous mode
cat <<EOF | kubectl apply -f -
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: autonomous-gitops
  namespace: openshift-gitops
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: autonomous-placement
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: true
      mode: autonomous
EOF
```

### Step 2: Modify Policy to Add RBAC and Bootstrap Application

In autonomous mode, the Application is deployed to the managed cluster via the Policy. The `default` AppProject is automatically included in the Policy (unlike managed mode where the principal propagates it).

```bash
# Wait for the Policy to be created
kubectl get policy autonomous-gitops-argocd-policy -n openshift-gitops

# Add RBAC and a root Application (e.g., for App of Apps pattern)
kubectl patch policy autonomous-gitops-argocd-policy -n openshift-gitops --type=json -p='[
  {
    "op": "add",
    "path": "/spec/policy-templates/-",
    "value": {
      "objectDefinition": {
        "apiVersion": "policy.open-cluster-management.io/v1",
        "kind": "ConfigurationPolicy",
        "metadata": {
          "name": "autonomous-gitops-argocd-policy-rbac"
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
                "apiVersion": "argoproj.io/v1alpha1",
                "kind": "Application",
                "metadata": {
                  "name": "app-of-apps-root",
                  "namespace": "openshift-gitops"
                },
                "spec": {
                  "project": "default",
                  "source": {
                    "repoURL": "https://github.com/YOUR-ORG/YOUR-GITOPS-REPO",
                    "targetRevision": "HEAD",
                    "path": "apps"
                  },
                  "destination": {
                    "server": "https://kubernetes.default.svc",
                    "namespace": "openshift-gitops"
                  },
                  "syncPolicy": {
                    "automated": {
                      "prune": true,
                      "selfHeal": true
                    }
                  }
                }
              }
            }
          ]
        }
      }
    }
  }
]'
```

### Step 3: Verify Autonomous Mode

```bash
# Verify agent mode is set to autonomous in AddOnDeploymentConfig
kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster> \
  -o jsonpath='{.spec.customizedVariables[?(@.name=="ARGOCD_AGENT_MODE")].value}'
# Expected: autonomous

# Verify ArgoCD CR on managed cluster has autonomous client mode
KUBECONFIG=/path/to/managed kubectl get argocd acm-openshift-gitops -n openshift-gitops \
  -o jsonpath='{.spec.argoCDAgent.agent.client.mode}'
# Expected: autonomous

# Verify agent pod is running and syncing status back to hub
KUBECONFIG=/path/to/managed kubectl logs -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent --tail=10
# Look for: "Updated application status" messages (agent syncing back to principal)

# Verify Applications are reconciled locally on managed cluster
KUBECONFIG=/path/to/managed kubectl get applications.argoproj.io -n openshift-gitops -o wide
```

### Known Limitations

- **Local-cluster (hub)**: In autonomous mode on `local-cluster`, the agent transforms Application specs (project name gets namespace-prefixed, destination gets cluster name reference), which can conflict with the Policy that enforces the original spec. Autonomous mode is primarily designed for remote managed clusters, not the hub itself. For hub-local Applications, use non-agent mode or managed agent mode.

---

## Cleanup

### Manual Deletion (Required Order)

Deleting a `GitOpsCluster` does NOT automatically cascade to other resources. You must explicitly delete resources in the correct order. **GitOpsCluster must be deleted BEFORE MCAs** — if the GitOpsCluster still exists, the controller will re-create deleted MCAs.

```bash
export KUBECONFIG=/path/to/hub/kubeconfig
NS="openshift-gitops"
GITOPSCLUSTER_NAME="my-gitops"

# 1. Delete ApplicationSet (removes ArgoCD apps)
kubectl delete applicationset my-appset -n $NS --ignore-not-found

# 2. Delete GitOpsCluster — stops the controller from re-creating MCAs.
#    This is a no-op (no finalizer, no automated cleanup). Dynamic AddOnTemplates
#    and ADCs are NOT owned by GitOpsCluster, so they persist.
kubectl delete gitopscluster $GITOPSCLUSTER_NAME -n $NS --ignore-not-found

# 3. Delete Policy + PlacementBinding — stops enforcement of ArgoCD CR on managed
#    clusters. Without this, the Policy re-creates the ArgoCD CR during cleanup.
kubectl delete placementbinding ${GITOPSCLUSTER_NAME}-argocd-policy-binding -n $NS --ignore-not-found
kubectl delete policy ${GITOPSCLUSTER_NAME}-argocd-policy -n $NS --ignore-not-found --wait=false
sleep 5

# 4. Delete MCAs — triggers pre-delete cleanup Job on each managed cluster.
#    ADC and AddOnTemplate still exist (orphaned from step 2), so the addon
#    framework can render the pre-delete ManifestWork.
kubectl delete managedclusteraddon gitops-addon -n cluster1 --ignore-not-found
kubectl delete managedclusteraddon gitops-addon -n cluster2 --ignore-not-found

# 5. Wait for MCAs to fully delete (cleanup Jobs complete, 2-5 minutes)
kubectl get managedclusteraddon -A | grep gitops-addon   # repeat until empty

# 6. Delete remaining hub resources (MCAs are gone, safe to remove)
kubectl delete placement my-placement -n $NS --ignore-not-found

# 7. Clean orphaned ADCs and PlacementDecisions (optional)
kubectl delete addondeploymentconfig gitops-addon-config -n cluster1 --ignore-not-found
kubectl delete addondeploymentconfig gitops-addon-config -n cluster2 --ignore-not-found
```

**Why this order matters**: GitOpsCluster must be deleted BEFORE MCAs — if the GitOpsCluster still exists, the controller re-creates deleted MCAs. Policy must be deleted BEFORE MCAs — if the Policy is still active, the governance framework re-enforces the ArgoCD CR on managed clusters while the cleanup Job tries to remove it, leaving orphaned ArgoCD CRs. ADC and AddOnTemplate must still exist when MCAs are deleted (the addon framework needs them to render the pre-delete ManifestWork). These resources are not owned by GitOpsCluster and persist after its deletion.

### Important Notes

- **Cluster secrets** are managed by the GitOpsCluster controller. They persist as long as the `ManagedCluster` exists and are cleaned up when the `ManagedCluster` is deleted.
- **Do NOT force-remove finalizers** on ManagedClusterAddOn. If deletion hangs, check the pre-delete cleanup Job: `kubectl get jobs -n open-cluster-management-agent-addon` on the managed cluster.
- **Pre-delete RBAC**: The cleanup Job uses a dedicated `gitops-addon-cleanup` ClusterRole/ClusterRoleBinding annotated with `addon-pre-delete`. This ensures the cleanup Job retains RBAC permissions even after the addon framework deletes the regular ManifestWork (which removes the main `gitops-addon` ClusterRole) during MCA teardown. Without this, the cleanup Job would lose all permissions and spend its entire retry loop hitting `Forbidden` errors. The cleanup Job's last step deletes both `gitops-addon` and `gitops-addon-cleanup` ClusterRole/ClusterRoleBinding as best-effort — failures are logged as warnings but don't block the pre-delete hook completion.
- **Guestbook namespace** and other namespaces deployed by ArgoCD Applications are not cleaned up by the addon cleanup Job. They can be deleted manually.
- **Empty namespaces on managed cluster** (`openshift-gitops`, `openshift-gitops-operator`) may remain after cleanup. CRDs installed by the addon may also remain. These are harmless and will be recreated/reused on the next deployment.
- **Pause marker ConfigMap**: The pre-delete cleanup Job creates a `gitops-addon-pause` ConfigMap to pause the addon controller during cleanup. The addon controller **automatically clears stale pause markers at startup**.
- **Cleanup only deletes what gitopsaddon laid down**: **CRITICAL RULE — NEVER list-and-delete resources blindly in any namespace. Never do a "list all Deployments and delete them" pattern.** The cleanup code only deletes: (1) gitopsaddon-labeled ArgoCD CRs (step 2 — waits indefinitely for the operator to process the finalizer and clean up its own workloads like redis, repo-server, app-controller, etc.), (2) OLM Subscription/CSV on OCP (step 3 — only after ArgoCD CR is fully gone), (3) gitopsaddon-labeled operator resources on embedded/EKS (step 4). ArgoCD workloads are the operator's responsibility — cleaned up via the ArgoCD CR finalizer. The cleanup NEVER force-strips the ArgoCD CR finalizer. If the finalizer hangs, that's a real bug in the operator.
- **OCP cleanup skips operator namespace resource deletion**: On OCP clusters with OLM, `deleteOperatorResources` is skipped entirely — OLM handles operator teardown via CSV deletion.
- **GitOpsAddon label on all deployed resources**: `templateAndApplyChart` injects `apps.open-cluster-management.io/gitopsaddon: "true"` on every resource before applying. The cleanup function `deleteOperatorResources` uses `requireLabel=true`, so it only deletes what gitopsaddon created — system resources (`kube-root-ca.crt`, `default` SA, etc.) are never touched. The test script verifies this with `verify_gitopsaddon_labels` after each deploy.
- **Post-cleanup verification**: `verify_cleanup` in `test-cycle-eks-ocp.sh` checks: hub resources (GitOpsCluster, Policy, MCA), ArgoCD CR removed (operator finalizer processed), ArgoCD workloads cleaned by operator, operator namespace clean, OLM subscription/CSV removed (OCP), addon deployment removed, and cluster accessibility.

---

## Automated Testing

There are two test systems: **Go/Ginkgo e2e tests** (CI-ready, Kind-only) and **integration test scripts** (manual, real clusters).

### Go/Ginkgo E2E Tests (CI and Local Development)

These tests run against Kind clusters and are used for both GitHub PR CI and local development. They use the upstream `argocd-operator` (not OCP/OLM) and test the embedded operator path.

**Architecture:** The Makefile creates two Kind clusters (`hub` + `cluster1`), installs OCM via `clusteradm`, registers `cluster1` as a managed cluster and registers the hub itself as `local-cluster`, deploys the ArgoCD operator on the hub, deploys the multicloud-integrations controller, and applies AddOnTemplates. The Go tests then exercise the gitopsaddon by creating GitOpsCluster resources and verifying spoke + local-cluster deployment.

**Test suites:**

| Make Target | Label Filter | What it Tests |
|---|---|---|
| `test-local-e2e-gitopsaddon-embedded` | `embedded` | Non-agent mode: spoke + local-cluster get ArgoCD via embedded operator, guestbook deploys and syncs on both, skip-argocd-policy annotation |
| `test-local-e2e-gitopsaddon-embedded-agent` | `embedded-agent` | Agent mode: spoke gets agent pod, ApplicationSet deploys guestbook via principal/agent, local-cluster MCA/ArgoCD verified, agent version drift auto-heal, cert rotation resilience |
| `test-local-e2e-gitopsaddon-olm-override` | `olm-override` | OLM override: validates `olmSubscription.enabled=true` propagates `OLM_SUBSCRIPTION_ENABLED=true` to AddOnDeploymentConfig |
| `test-local-e2e-gitopsaddon-embedded-autonomous` | `embedded-autonomous` | Autonomous mode: spoke gets agent in autonomous mode, guestbook deployed via Policy, local reconciliation, status synced to hub, local-cluster infrastructure verified |

**Local development (creates Kind clusters + runs tests):**

```bash
# Run embedded (non-agent) e2e
make test-local-e2e-gitopsaddon-embedded

# Run embedded-agent e2e
make test-local-e2e-gitopsaddon-embedded-agent

# Run OLM override e2e
make test-local-e2e-gitopsaddon-olm-override

# Clean up Kind clusters
make clean-e2e
```

**CI (assumes Kind clusters and images already loaded):**

```bash
# Run embedded e2e (no cluster setup)
make test-e2e-gitopsaddon-embedded

# Run embedded-agent e2e (no cluster setup)
make test-e2e-gitopsaddon-embedded-agent

# Run OLM override e2e (no cluster setup)
make test-e2e-gitopsaddon-olm-override

# Run all gitopsaddon e2e
make test-e2e-gitopsaddon-all
```

**What the local targets do:**
1. Delete any existing `hub` and `cluster1` Kind clusters
2. Create fresh `hub` and `cluster1` Kind clusters
3. Build the controller image (`quay.io/stolostron/multicloud-integrations:latest`)
4. Load the controller image and ArgoCD operator image into both Kind clusters
5. Run `setup_env.sh` which installs OCM, governance framework, ArgoCD operator on hub, deploys the controller, and applies AddOnTemplates
6. Run the Go tests with the appropriate label filter

**What CI targets do:**
1. Run `setup_env.sh` (assumes Kind clusters and images already exist)
2. Run the Go tests with the appropriate label filter

**Local-cluster testing in e2e:**
- **Embedded (non-agent):** Fully tested — MCA, ArgoCD CR in `local-cluster` namespace, no duplicate ArgoCD, guestbook deployment + sync, controller namespace verification, environment health
- **Embedded-agent:** Fully tested — MCA, ArgoCD CR in `local-cluster` namespace, no duplicate ArgoCD, guestbook deployment + sync via ApplicationSet/agent pipeline, controller namespace verification (lenient: accepts `local-cluster` or `openshift-gitops`), environment health, agent version drift auto-heal. The Kind e2e uses the upstream `argocd-operator` which supports agent mode. The `controllerNamespace` assertion is lenient because the upstream operator may report either the hub ArgoCD namespace or the local-cluster namespace depending on version. Note: the controller namespace test does NOT assert `Synced` status — ArgoCD sync can take minutes on CI runners, and sync is already validated by the guestbook deployment check. The **drift heal e2e test** injects a fake agent image directly into the ArgoCD Policy, toggles `spec.gitopsAddon.overrideExistingConfigs` to trigger reconciliation, and verifies the controller restores the principal image.

**Key files:**
- `test/e2e/gitopsaddon/suite_test.go` — constants (cluster names, namespaces)
- `test/e2e/gitopsaddon/helpers_test.go` — YAML generators, wait helpers, verification, cleanup
- `test/e2e/gitopsaddon/gitopsaddon_embedded_test.go` — embedded (non-agent) scenario
- `test/e2e/gitopsaddon/gitopsaddon_embedded_agent_test.go` — embedded + agent scenario
- `test/e2e/gitopsaddon/gitopsaddon_embedded_autonomous_test.go` — embedded + autonomous agent scenario
- `test/e2e/gitopsaddon/scripts/setup_env.sh` — environment setup (OCM, ArgoCD, controller)
- `test/e2e/fixtures/` — ArgoCD operator CRDs and deployment YAML
- `gitopsaddon/routes-openshift-crd/routes.route.openshift.io.crd.yaml` — Route CRD stub with `served: false`, installed by the gitopsaddon agent on non-OCP managed clusters as part of the embedded chart. NOT applied by `setup_env.sh` — the upstream ArgoCD operator does not need it (gracefully handles absent Route API, reconciles ArgoCD CR to `Available` without it).

**Environment variables:**
- `E2E_IMG` — controller image (default: `quay.io/stolostron/multicloud-integrations:latest`)
- `ARGOCD_OPERATOR_IMAGE` — ArgoCD operator image (default: `quay.io/argoprojlabs/argocd-operator:latest`)
- `CLUSTERADM_VERSION` — clusteradm CLI version for OCM setup (default: `v1.1.1`)
- `HUB_CLUSTER` — Kind hub cluster name (default: `hub`)
- `SPOKE_CLUSTER` — Kind spoke cluster name (default: `cluster1`)

### Testing Philosophy

**Test scripts must NEVER contain hacks or workarounds.** If a verification check fails, the test fails — this means there is a real bug in the code. The entire purpose of the test is to detect problems. Masking failures with workarounds (e.g., force-deleting stuck resources, stripping finalizers, directly fixing state on spoke clusters) defeats the purpose because it hides bugs that users will hit in production.

- **Between-scenario cleanup is hub-triggered ONLY.** The script deletes hub resources (Placement, Policy, GitOpsCluster, ManagedClusterAddOn) and waits for the addon's pre-delete cleanup Job to handle spoke-side cleanup. If spoke-side cleanup fails, the test reports it as a failure — it does NOT directly connect to the spoke to fix it.
- **Initial cleanup (`cleanup_all`) IS allowed to be forceful** — it runs before any scenario to ensure a clean starting state regardless of prior failures. Direct spoke access and finalizer stripping are acceptable here only.
- **Verification is read-only.** After cleanup, the script checks the state of hub and spoke clusters but does NOT modify anything. If resources remain, it reports `CLEANUP FAIL` and returns failure.
- **RBAC safety check.** Each scenario calls `verify_cleanup_rbac_safety` to confirm the `gitops-addon` ClusterRole has `delete` for every resource type that `deleteOperatorResources()` targets.
- **Post-cleanup accessibility check.** After cleanup, `verify_cluster_accessible_after_cleanup` confirms each managed cluster is still reachable and has healthy RBAC.

### Scenario Verification Details

#### Scenario 1: No Agent — OCP + Kind + local-cluster

Creates: Placement `all-placement`, GitOpsCluster `all-gitops` (addon, no agent), Policy `all-gitops-argocd-policy` with RBAC + guestbook Application.

**Verification checks (all must pass):**

| # | What is checked | Where | How |
|---|----------------|-------|-----|
| 1 | ManagedClusterAddOn `gitops-addon` exists | Hub: `ocp-cluster1`, `kind-cluster1`, `local-cluster` namespaces | `kubectl get managedclusteraddon` |
| 2 | Policy `all-gitops-argocd-policy` is Compliant | Hub: `openshift-gitops` namespace | `kubectl get policy -o jsonpath='{.status.compliant}'` |
| 3 | OLM auto-detected on OCP: subscription exists with `DISABLE_DEFAULT_ARGOCD_INSTANCE=true` | Spoke: ocp-cluster1, `openshift-gitops-operator` namespace | `kubectl get subscription.operators.coreos.com` |
| 4 | No embedded operator on OCP (OLM-only mode) | Spoke: ocp-cluster1, `openshift-gitops-operator` namespace | Verify deployment does NOT exist |
| 5 | ArgoCD CR `acm-openshift-gitops` exists on OCP | Spoke: ocp-cluster1, `openshift-gitops` namespace | `kubectl get argocd` |
| 6 | ArgoCD CR `acm-openshift-gitops` exists on Kind | Spoke: kind-cluster1, `openshift-gitops` namespace | `kubectl get argocd` |
| 7 | No duplicate ArgoCD CRs on hub or OCP | Hub + spoke reads | `verify_no_duplicate_argocd` function |
| 8 | Guestbook Deployment `guestbook-ui` exists on OCP | Spoke: ocp-cluster1, `guestbook` namespace | `kubectl get deployment` (pods crash due to SCC — expected) |
| 9 | Guestbook Deployment `guestbook-ui` exists + pods ready on Kind | Spoke: kind-cluster1, `guestbook` namespace | `kubectl get deployment` + `kubectl rollout status` |
| 10 | local-cluster: ArgoCD in `local-cluster` ns, no duplicates, guestbook Application synced | Hub | `verify_local_cluster_working` function |
| 11 | Environment health (no orphaned resources, correct controller ownership) | Spoke: ocp-cluster1, kind-cluster1 | `verify_environment_health` function |

#### Scenario 2: Agent Mode — OCP + Kind + local-cluster

Creates: Placement `all-agent-placement`, GitOpsCluster `all-agent-gitops` (addon + agent), Policy with RBAC only, ApplicationSet `all-agent-placement-guestbook-appset`.

**Verification checks (all must pass):**

| # | What is checked | Where | How |
|---|----------------|-------|-----|
| 1 | ManagedClusterAddOn `gitops-addon` exists | Hub: all 3 cluster namespaces | `kubectl get managedclusteraddon` |
| 2 | Policy `all-agent-gitops-argocd-policy` is Compliant | Hub | `kubectl get policy` |
| 3 | OLM auto-detected on OCP (subscription + `DISABLE_DEFAULT_ARGOCD_INSTANCE=true`) | Spoke: ocp-cluster1 | `verify_olm_auto_detected` |
| 4 | Agent connected for ocp-cluster1 | Hub: addon status conditions | `verify_agent_connected` |
| 5 | Agent connected for kind-cluster1 | Hub: addon status conditions | `verify_agent_connected` |
| 6 | ArgoCD agent pods running on OCP | Spoke: ocp-cluster1, `openshift-gitops` | `kubectl get pods -l app.kubernetes.io/part-of=argocd-agent` |
| 7 | ArgoCD agent pods running on Kind | Spoke: kind-cluster1, `openshift-gitops` | `kubectl get pods -l app.kubernetes.io/part-of=argocd-agent` |
| 8 | No duplicate ArgoCD on OCP | Spoke: ocp-cluster1 | `verify_no_duplicate_argocd` |
| 9 | All pod images on OCP use Red Hat registry | Spoke: ocp-cluster1, `openshift-gitops` | `verify_redhat_images` |
| 10 | All pod images on Kind use Red Hat registry | Spoke: kind-cluster1, `openshift-gitops` | `verify_redhat_images` |
| 11 | ApplicationSet generated `ocp-cluster1-guestbook` Application | Hub: `openshift-gitops` | `kubectl get applications.argoproj.io` |
| 12 | ApplicationSet generated `kind-cluster1-guestbook` Application | Hub: `openshift-gitops` | `kubectl get applications.argoproj.io` |
| 13 | Guestbook Deployment exists on OCP (pods crash — expected) | Spoke: ocp-cluster1, `guestbook` | `kubectl get deployment` |
| 14 | Guestbook Deployment exists + pods ready on Kind | Spoke: kind-cluster1, `guestbook` | `kubectl get deployment` |
| 15 | local-cluster: Guestbook deployed via agent, Application copy in `local-cluster` namespace | Hub | Agent copies app to `local-cluster` ns where app controller watches |
| 16 | Application sync status = `Synced` for OCP/Kind (in `openshift-gitops`), local-cluster (in `local-cluster` ns) | Hub | `kubectl get applications.argoproj.io -o jsonpath='{.status.sync.status}'` |
| 17 | Environment health on OCP, Kind, and local-cluster | All clusters | `verify_environment_health` |

#### Scenario 3: Custom OLM — OCP only

Creates: Placement `ocp-olm-placement`, GitOpsCluster `ocp-olm-gitops` with `olmSubscription.enabled: true`, `installPlanApproval: Manual`.

**Verification checks (all must pass):**

| # | What is checked | Where | How |
|---|----------------|-------|-----|
| 1 | ManagedClusterAddOn `gitops-addon` exists in `ocp-cluster1` | Hub | `kubectl get managedclusteraddon` |
| 2 | AddOnDeploymentConfig has `OLM_SUBSCRIPTION_ENABLED=true` | Hub: `ocp-cluster1` namespace | `kubectl get addondeploymentconfig -o jsonpath` |
| 3 | AddOnDeploymentConfig has `OLM_SUBSCRIPTION_INSTALL_PLAN_APPROVAL=Manual` | Hub: `ocp-cluster1` namespace | `kubectl get addondeploymentconfig -o jsonpath` |
| 4 | OLM subscription exists on OCP with `installPlanApproval=Manual` | Spoke: ocp-cluster1, `openshift-gitops-operator` | `kubectl get subscription.operators.coreos.com -o jsonpath` |
| 5 | OLM subscription has `DISABLE_DEFAULT_ARGOCD_INSTANCE=true` | Spoke: ocp-cluster1, `openshift-gitops-operator` | `kubectl get subscription.operators.coreos.com -o jsonpath` |
| 6 | No embedded operator deployment (OLM-only mode) | Spoke: ocp-cluster1, `openshift-gitops-operator` | Verify deployment does NOT exist |

#### Scenario 4: OLM Override on Kind — Kind only

Creates: Placement `kind-olm-override-placement`, GitOpsCluster `kind-olm-override-gitops` with `olmSubscription.enabled: true`.

**Verification checks (all must pass):**

| # | What is checked | Where | How |
|---|----------------|-------|-----|
| 1 | ManagedClusterAddOn `gitops-addon` exists in `kind-cluster1` | Hub | `kubectl get managedclusteraddon` |
| 2 | AddOnDeploymentConfig has `OLM_SUBSCRIPTION_ENABLED=true` | Hub: `kind-cluster1` namespace | `kubectl get addondeploymentconfig -o jsonpath` |
| 3 | Addon agent attempts OLM subscription mode (expected failure — no OLM on Kind) | Spoke: kind-cluster1 | Agent logs show OLM path attempted |

#### Scenario 2 Cert Rotation Verification

After the main Scenario 2 checks pass, the test deletes the `argocd-agent-client-tls` secret on each managed cluster (OCP, Kind, local-cluster) and verifies it is recreated within 120 seconds. This validates the `SecretReconciler`'s target-deletion watch and the 5-minute periodic requeue safety net.

#### Scenario 5: Agent Version Drift Heal — All clusters (after S2)

Runs after Scenario 2 (requires agent mode deployment). Tests the hub controller's ability to detect principal image drift and auto-patch the ArgoCD Policy.

**Verification checks (all must pass):**

| # | What is checked | Where | How |
|---|----------------|-------|-----|
| 1 | Principal deployment found with image | Hub: `openshift-gitops` | `kubectl get deployment -l app.kubernetes.io/component=principal` |
| 2 | ArgoCD Policy exists | Hub: `openshift-gitops` | `kubectl get policy` |
| 3 | After patching principal with fake image and triggering reconciliation, controller patches Policy | Hub | Poll Policy for `argoCDAgent.agent.image` matching fake image |
| 4 | After restoring original principal image and re-triggering, controller restores Policy | Hub | Poll Policy for original image restored |
| 5 | OpenShift GitOps operator restored (was scaled down during test) | Hub: `openshift-gitops-operator` | `kubectl scale --replicas=1` |

#### Scenario 7: Custom Hub ArgoCD Namespace — Kind + local-cluster

Creates: ArgoCD instance in `custom-gitops` namespace, Placement `custom-ns-placement`, GitOpsCluster `custom-ns-gitops` with `argoServer.argoNamespace: custom-gitops`, ManagedClusterSetBinding in `custom-gitops`.

**Verification checks (all must pass):**

| # | What is checked | Where | How |
|---|----------------|-------|-----|
| 1 | ArgoCD server is Ready in `custom-gitops` namespace | Hub | `kubectl wait pod -l app.kubernetes.io/name=custom-argocd-server` |
| 2 | CA cert `argocd-agent-ca` exists in `custom-gitops` namespace | Hub | `kubectl get secret argocd-agent-ca -n custom-gitops` |
| 3 | GitOpsCluster conditions are True | Hub | `kubectl get gitopscluster -o jsonpath` |
| 4 | ManagedClusterAddOns created for selected clusters | Hub | `kubectl get managedclusteraddon` |

### Cleanup Verification After Each Scenario

After each scenario, `cleanup_scenario` performs hub-triggered deletion, then **read-only verification**. If any check fails, the cleanup returns failure (indicating a code bug).

**Hub-triggered deletion order:**
1. Delete Placement (stops controller from selecting clusters)
2. (Agent mode) Delete ApplicationSet, wait up to 60s for generated Applications to be deleted
3. Delete GitOpsCluster (stops controller from recreating Policy/addons during cleanup)
4. Delete PlacementBinding + Policy with `--wait=false` (stops enforcement on managed clusters)
5. Delete ManagedClusterAddOn for each target cluster (triggers pre-delete cleanup Job — waits up to 180s for the Job to complete naturally; if it does not finish in time, falls back to finalizer removal and force-delete of the ManagedClusterAddOn)
6. Re-delete Policy/PlacementBinding (race fix: controller may recreate during step 3-5)

**Read-only verification checks (all must pass):**

| # | What is checked | Where | Failure means |
|---|----------------|-------|---------------|
| 1 | GitOpsCluster is deleted | Hub | Controller bug — not cleaning up |
| 2 | Policy is deleted (waits up to 60s for governance framework) | Hub | Governance framework or controller bug |
| 3 | ManagedClusterAddOn is deleted for each target cluster | Hub | Pre-delete Job failed or addon framework bug |
| 4 | (Agent mode) No Applications remain in `openshift-gitops` | Hub | ApplicationSet controller or Application finalizer bug |
| 5 | ArgoCD CR `acm-openshift-gitops` removed (waits up to 120s) | Each spoke cluster | Pre-delete cleanup Job bug — failed to delete ArgoCD CR |
| 6 | (OCP) gitopsaddon-labeled OLM subscription removed | Spoke: ocp-cluster1 | Pre-delete cleanup Job bug — failed to delete OLM subscription |
| 7 | Guestbook namespace status logged | Each spoke cluster | Logged as info only — pre-delete Job does not clean app workloads |

**After verification, Step 3 runs regardless of verification outcome:**
- Force-deletes residual application workloads (guestbook Deployment/namespace, Applications, AppProjects)
- Strips Application finalizers since ArgoCD is already removed
- Cleans up agent cluster secrets on the hub
- This is NOT a workaround — the pre-delete Job removes ArgoCD/operator, not user applications

**Step 4: Post-cleanup cluster accessibility check** (runs after Step 3):
- Verifies each managed cluster is still accessible and has healthy RBAC

**The cleanup verification does NOT:**
- Strip finalizers on addon-managed resources
- Directly delete ArgoCD or operator resources on spoke clusters
- Fix or work around stuck addon/operator state

**OLM subscription edge case:** If the OLM subscription is still present on OCP after 480s, it is force-deleted as a timeout fallback. This is logged as an error since it indicates the pre-delete Job took too long, but it prevents cascading failures in subsequent scenarios.

If cleanup verification fails, it means the addon's pre-delete cleanup Job or the hub controllers have a bug. The test reports this failure.

### Initial Cleanup (`cleanup_all`)

Runs once before the first scenario. This IS allowed to be forceful because it must ensure a clean starting state regardless of prior failures:
- Strips finalizers from ManagedClusterAddOns and force-deletes them
- Directly connects to spoke clusters to remove residual ArgoCD CRs, OLM subscriptions, CSVs, InstallPlans, operator deployments, pause markers
- Deletes stale cluster secrets, ApplicationSets, Applications, AppProjects
- Completes in under a minute

The addon controller **automatically clears stale pause markers** on startup, so interrupted cleanups don't block future deployments.

---

## Troubleshooting

### Agent Not Connecting to Principal

1. Check principal pod is running:
   ```bash
   kubectl get pods -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-agent-principal
   ```

2. Check principal logs:
   ```bash
   kubectl logs -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-agent-principal --tail=50
   ```

3. Check agent logs on managed cluster (use `app.kubernetes.io/part-of` label):
   ```bash
   KUBECONFIG=/path/to/managed kubectl logs -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent --tail=50
   ```

4. Verify server address was discovered:
   ```bash
   kubectl get gitopscluster <name> -n openshift-gitops -o jsonpath='{.spec.gitopsAddon.argoCDAgent.serverAddress}'
   ```

### Route Discovery Verification

The controller automatically discovers the principal server address from the OpenShift Route. To verify the Route exists with correct labels:

```bash
# Check principal route exists
kubectl get route openshift-gitops-agent-principal -n openshift-gitops

# Verify labels on the route
kubectl get route -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent
```

If you need to override auto-discovery (for non-standard setups), you can manually specify the server address:

```yaml
spec:
  gitopsAddon:
    argoCDAgent:
      enabled: true
      mode: managed
      serverAddress: "openshift-gitops-agent-principal-openshift-gitops.apps.YOUR-HUB.com"
      serverPort: "443"
```

### Image Pull Issues on Non-OCP Clusters

The addon relies on the OCM klusterlet to propagate the `open-cluster-management-image-pull-credentials` secret to namespaces labeled with `addon.open-cluster-management.io/namespace: true`. If pods are stuck in `ImagePullBackOff`:

1. Verify the secret exists in the addon namespace on the managed cluster:
   ```bash
   KUBECONFIG=/path/to/managed kubectl get secret open-cluster-management-image-pull-credentials -n open-cluster-management-agent-addon
   ```

2. Verify the namespace has the correct label:
   ```bash
   KUBECONFIG=/path/to/managed kubectl get ns openshift-gitops-operator -o jsonpath='{.metadata.labels}'
   # Should include: addon.open-cluster-management.io/namespace: "true"
   ```

3. Verify the secret was copied to the operator namespace:
   ```bash
   KUBECONFIG=/path/to/managed kubectl get secret open-cluster-management-image-pull-credentials -n openshift-gitops-operator
   ```

4. Verify the operator ServiceAccount references the pull secret:
   ```bash
   KUBECONFIG=/path/to/managed kubectl get sa openshift-gitops-operator-controller-manager -n openshift-gitops-operator -o jsonpath='{.imagePullSecrets}'
   ```

5. If the secret exists but doesn't contain the required registry credentials, verify the hub's `open-cluster-management-image-pull-credentials` secret has the correct registries configured.

### OLM Operator Not Installing on OCP

If the OLM operator deployment never appears in `openshift-gitops-operator` after the addon creates the Subscription:

1. Check the CSV status:
   ```bash
   KUBECONFIG=/path/to/ocp kubectl get csv -n openshift-gitops-operator
   ```
   If it shows `Failed` with `"OwnNamespace InstallModeType not supported"`, the OperatorGroup is misconfigured. Delete it and let the addon recreate it:
   ```bash
   KUBECONFIG=/path/to/ocp kubectl delete operatorgroup gitops-addon-operator-group -n openshift-gitops-operator
   ```

2. Check the OperatorGroup mode — it must be **AllNamespaces** (no `targetNamespaces`):
   ```bash
   KUBECONFIG=/path/to/ocp kubectl get operatorgroup -n openshift-gitops-operator -o yaml
   ```
   If `spec.targetNamespaces` is set, this is the bug. The addon code (`createOrUpdateOperatorGroup`) should create it without `targetNamespaces`.

3. Check the CatalogSource is healthy:
   ```bash
   KUBECONFIG=/path/to/ocp kubectl get catalogsource redhat-operators -n openshift-marketplace -o jsonpath='{.status.connectionState}'
   ```

4. Check for failed InstallPlans:
   ```bash
   KUBECONFIG=/path/to/ocp kubectl get installplan -n openshift-gitops-operator
   ```

### ArgoCD UI Live Manifest Shows "Resource not found in cluster"

This error has two common causes in agent mode:

**Cause 1: Destination-Based Mapping mismatch between principal and agent**

The `destinationBasedMapping` setting controls the Redis key separator (principal uses `_`, agent without DBM uses `|`). If the principal has DBM enabled but the agent does not, the agent logs show:

```
unexpected key format, missing '_': 'app|resources-tree|<cluster>-<appname>|<version>.gz'
```

Fix: ensure the agent's ArgoCD CR has `destinationBasedMapping` enabled to match the principal. Add this to the Policy's object templates for the agent's ArgoCD CR:

```yaml
spec:
  argoCDAgent:
    agent:
      client:
        destinationBasedMapping: true
```

**Cause 2: Agent SA missing `view` ClusterRole binding**

The resource proxy forwards live manifest requests to the managed cluster using the agent ServiceAccount. Without cluster read access, the proxy cannot retrieve resource state.

Fix: create a ClusterRoleBinding on the managed cluster (or add it to the Policy):

```bash
KUBECONFIG=/path/to/managed kubectl create clusterrolebinding acm-openshift-gitops-argocd-agent-cluster-reader \
  --clusterrole=view \
  --serviceaccount=openshift-gitops:acm-openshift-gitops-agent-agent
```

Verify the binding exists:
```bash
KUBECONFIG=/path/to/managed kubectl get clusterrolebinding acm-openshift-gitops-argocd-agent-cluster-reader -o yaml
```

### Hub Controller Stuck / UID Mismatch

If the hub controller logs show `StorageError: invalid object, UID mismatch`, the informer cache is stale. Fix by restarting the controller:
```bash
export KUBECONFIG=~/Desktop/hub
kubectl delete lease multicloud-operators-gitopscluster-leader.open-cluster-management.io -n kube-system --ignore-not-found
kubectl -n open-cluster-management rollout restart deployment/multicluster-operators-application
```

### Policy Not Compliant

1. Check Policy status:
   ```bash
   kubectl describe policy <name>-argocd-policy -n openshift-gitops
   ```

2. Check ConfigurationPolicy on managed cluster namespace:
   ```bash
   kubectl get configurationpolicy -n <managed-cluster-name>
   ```

---

## Known Limitations

1. **Policy is User-Owned**: Users must modify the Policy for RBAC/Apps. The controller recreates deleted Policies (unless `skip-argocd-policy` is set) and may patch `argoCDAgent.agent.image` during drift heal, but does not overwrite other customizations.

2. **Cleanup Order Matters**: Delete GitOpsCluster BEFORE ManagedClusterAddOn (stops the controller from re-creating MCAs). See [Cleanup](#cleanup) section for the full sequence.

3. **Hub Configuration Required for Agent**: The hub ArgoCD must be configured as principal with `allowedNamespaces: ["*"]`.

4. **OLM Mode for OCP Only**: Uses Red Hat operator catalog.

5. **Cluster Secrets Managed by Controller**: Do not manually delete cluster secrets.

6. **Local-Cluster Agent Namespace**: The `local-cluster` agent creates Application copies in the `local-cluster` namespace (its own namespace), not in `openshift-gitops`. This is because the hub's `openshift-gitops` namespace is occupied by the principal's ArgoCD, and the ArgoCD operator uses hardcoded ConfigMap names (`argocd-cm`) that would collide if two ArgoCD CRs were in the same namespace. The agent-side `destinationBasedMapping` must be disabled so `getTargetNamespaceForApp()` returns the agent's own namespace rather than the hub's `openshift-gitops`. For remote clusters, this distinction doesn't matter because both the agent namespace and the hub app namespace are `openshift-gitops`.

7. **Policy Deletion Timing**: OCM governance framework finalizers on Policy objects can take several minutes (up to 10 minutes) to process. The cleanup verification waits up to 120s for Policy deletion, with force-delete fallback.

8. **Custom Namespace Cleanup**: When using a custom ArgoCD namespace (Scenario 7), cleanup must specify `CLEANUP_NS=<namespace>` so that `cleanup_scenario` targets the correct namespace. Without this, ArgoCD CRs and Policies in the custom namespace are not cleaned up.

9. **OCP OLM Timing**: On real OCP clusters, OLM catalog resolution + InstallPlan + CSV installation can take 30-120 seconds even when the catalog is healthy. After a full cleanup that removes the CSV and operator, OLM must re-resolve the package from the catalog via gRPC. The test script uses 600s timeouts for all OCP OLM operations.

10. **Orphaned ArgoCD Resources After Cleanup**: When the OLM operator is deleted (via CSV/Subscription removal), the ArgoCD deployments/statefulsets it created (e.g., `acm-openshift-gitops-redis`, `acm-openshift-gitops-repo-server`) survive because the operator can't process their owner references if it's already gone. `cleanup_managed_cluster_direct` explicitly deletes these orphans.

11. **Pre-delete ManifestWork Race**: When a MCA is deleted, OCM creates a pre-delete ManifestWork that runs a cleanup Job. If the Job is slow and a new MCA is created before it finishes, the old pre-delete MW may interfere (e.g., deleting resources the new MW just applied, causing the ClusterRole to vanish). `cleanup_all` deletes stale pre-delete ManifestWorks to prevent this.

12. **Destination-Based Mapping Must Match Principal and Agent**: The `destinationBasedMapping` setting controls the Redis key format for resource tree data. If the principal has it enabled but the agent does not (or vice versa), the Redis keys use different separators (`_` vs `|`) and the ArgoCD UI live manifest shows `"Resource not found in cluster"`. Always keep `destinationBasedMapping` consistent across principal and all agents. The agent setting is `spec.argoCDAgent.agent.client.destinationBasedMapping` in the ArgoCD CR.

13. **Agent SA View ClusterRole Not Auto-Provisioned**: The ArgoCD agent ServiceAccount (`acm-openshift-gitops-agent-agent`) on OCP managed clusters is not automatically granted cluster read access. Without a `view` ClusterRoleBinding, the resource proxy cannot serve live manifest requests to the ArgoCD UI. This must be added manually or included in the Policy's object templates.

---

## Updating the Embedded Operator Manifests

The `charts/openshift-gitops-operator/` Helm chart contains the embedded operator used on non-OCP clusters (Kind, EKS). When the OpenShift GitOps Operator is updated to a new version, these manifests must be synced.

### Source of Truth

The **hub cluster** with the target operator version installed via OLM is the authoritative source. Upstream repos and the `orb` tool are supplementary:

| Source | Use For |
|--------|---------|
| Hub cluster (`~/Desktop/hub`) | CRDs, ClusterRole rules, image SHAs, Deployment structure — **primary source** |
| `orb` tool | Extracting image SHAs from Red Hat catalog bundles when hub is unavailable |
| [redhat-developer/gitops-operator](https://github.com/redhat-developer/gitops-operator) | Cross-referencing RBAC changes, understanding feature additions |
| [argoproj-labs/argocd-operator](https://github.com/argoproj-labs/argocd-operator) | Understanding upstream operator behavior (embedded chart uses this operator) |

### Quick Reference

1. **CRDs**: Extract from hub (`kubectl get crd <name> -o yaml`), strip runtime metadata, disable conversion webhooks (`strategy: None`), escape Go templates (`{{ }}` → `{{ "{{" }}`), place in `charts/.../templates/crds/` and `deploy/crds/`
2. **Manager ClusterRole**: Extract rules from hub CSV (`spec.install.spec.clusterPermissions[0].rules`), update `templates/openshift-gitops-operator-manager-role.clusterrole.yaml`
3. **Image SHAs**: Extract from hub CSV env vars or use `orb bundle convert plain`, update `pkg/utils/config.go` `DefaultOperatorImages`
4. **Chart.yaml**: Bump `version` field
5. **Check for removed resources**: e.g., `argocdexports.argoproj.io` was removed in v1.20.4
6. **Build and test**: `make build && make test`, then build/push image and run test cycles

### Detailed Procedure

See `CLAUDE.md` section "Manifest Update Procedure" for the full step-by-step with exact commands.

### orb Tool

Install: `go install github.com/joelanford/orb@latest`

The `orb` tool converts OLM bundles from container registries into plain YAML. It requires Red Hat registry auth (`DOCKER_CONFIG` pointing to a dir with `config.json`). Useful primarily for extracting image SHAs — the OLM bundle format is *transformed* (aggregated RBAC, OLM-modified Deployments) and not directly usable as chart input.

```bash
orb bundle convert plain registry.redhat.io/openshift-gitops-1/gitops-operator-bundle:v1.20.4 /tmp/orb-output/
# Output: /tmp/orb-output/manifests.yaml (CSV + CRDs)
```
