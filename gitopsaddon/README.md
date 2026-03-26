# GitOps Addon - Configuration Guide

This guide explains how to configure and test the GitOps Addon functionality using ACM hub and managed clusters.

## Key Concepts

- **GitOpsCluster CR**: Defines which managed clusters should have ArgoCD deployed via the GitOps addon
- **Placement**: Selects which managed clusters are targeted (including `local-cluster` / hub)
- **Policy**: The GitOpsCluster controller creates a Policy that deploys the ArgoCD CR to managed clusters. **This Policy is created once and is user-owned** - users can modify it to add RBAC, Applications, or customize ArgoCD settings. For `local-cluster`, the ArgoCD CR is deployed to the `local-cluster` namespace (not `openshift-gitops`) using a hub template.
- **ManagedClusterAddOn**: Created automatically for ALL managed clusters including `local-cluster`. Manages addon lifecycle.
- **Cluster Secrets**: For agent mode, the controller creates ArgoCD cluster secrets with proper server URLs including `agentName` query parameter
- **DISABLE_DEFAULT_ARGOCD_INSTANCE**: On OCP managed clusters, the addon creates an OLM Subscription with `DISABLE_DEFAULT_ARGOCD_INSTANCE=true`. On non-OCP clusters, the embedded operator chart is deployed. On the hub (`local-cluster`), the operator is already present so no installation occurs.
- **OLM Subscription Customization**: When `olmSubscription.enabled: true` is set in the GitOpsCluster spec, custom subscription values (channel, source, namespace, etc.) are passed to the addon agent via `AddOnDeploymentConfig` environment variables. The addon agent reads these and uses them when creating the OLM subscription on OCP clusters. Non-OCP clusters ignore these values. When absent or disabled, hardcoded defaults are used (`channel: latest`, `source: redhat-operators`, etc.).
- **AppProject Propagation (Agent Mode)**: In agent mode, the ArgoCD principal automatically propagates AppProjects from the hub to managed clusters. The `default` AppProject is created on the hub in the managed cluster's namespace alongside the Application.
- **Destination-Based Mapping (Agent Architecture)**: The principal keeps `destinationBasedMapping: true` for **routing** — it dispatches Applications to agents based on `spec.destination.name`. The **agents** have `destinationBasedMapping` disabled (not set in the ArgoCD CR). This is critical for `local-cluster`: the agent's `getTargetNamespaceForApp()` returns its own namespace (`local-cluster`), so agent-created Application copies go to the `local-cluster` namespace where the app controller watches. For remote clusters (Kind/OCP), the agent's namespace IS `openshift-gitops`, so the behavior is identical whether DBM is enabled or disabled on the agent. If agent DBM were enabled, the agent would keep the original namespace (`openshift-gitops`) from the hub — this fails on `local-cluster` because the app controller runs in `local-cluster`, not `openshift-gitops`, and it conflicts with existing ApplicationSet-generated apps in `openshift-gitops`.
- **Image Pull Secret Handling**: Two mechanisms work together:
  1. **Secret copying (OCM klusterlet)**: The addon agent labels namespaces it creates (`openshift-gitops-operator`, `openshift-gitops`) with `addon.open-cluster-management.io/namespace: true`. This label tells the OCM klusterlet to automatically copy the `open-cluster-management-image-pull-credentials` secret into those namespaces. No custom propagation code is needed for this step.
  2. **ServiceAccount patching (addon agent)**: On non-OCP clusters, the ArgoCD operator creates ServiceAccounts without `imagePullSecrets` references. Since the ArgoCD CRD has no native `imagePullSecrets` field, the addon agent patches all ServiceAccounts in the ArgoCD namespace to reference `open-cluster-management-image-pull-credentials` (via `patchArgoCDServiceAccountsWithImagePullSecrets`). It also deletes pods stuck in `ImagePullBackOff`/`ErrImagePull` so they restart with the patched SAs. On OCP clusters, node-level pull secrets handle registry authentication, so this patching is skipped.
- **Smart Cluster Detection**: The addon agent detects the cluster type at runtime:
  - **OCP detection**: Checks for `clusterversions.config.openshift.io` CRD, `infrastructures.config.openshift.io` CRD, `version.openshift.io` ClusterClaim, or `product.open-cluster-management.io` ClusterClaim with value "OpenShift"
  - **Hub detection**: Checks for `ClusterManager` resources or a `ManagedCluster` with name `local-cluster` or label `local-cluster=true`
- **ARGOCD_NAMESPACE**: Environment variable passed to the addon agent via `AddOnDeploymentConfig`. Set to `local-cluster` for the hub and `openshift-gitops` for remote clusters. Controls where ArgoCD CR lives and where client certs are copied.

## Architecture

### Template Modes

The system uses 2 addon template modes:

| Mode | Template Name | When Used |
|------|---------------|-----------|
| **Static** | `gitops-addon` | `gitopsAddon.enabled=true`, `argoCDAgent.enabled=false` |
| **Dynamic** | `gitops-addon-{ns}-{name}` | `gitopsAddon.enabled=true`, `argoCDAgent.enabled=true` (includes `RegistrationSpec` for client cert provisioning) |

OLM vs. embedded operator installation is determined at runtime by the addon agent based on cluster detection, not by template selection.

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

**Notes:**
- On OCP, guestbook-ui pods **crash** due to OCP's `restricted-v2` SCC (port 80 binding). The test checks that the Deployment resource exists (ArgoCD synced it), not pod health. On Kind, pods run normally.
- Agent mode requires Red Hat registry access for agent images on non-OCP clusters (handled by OCM pull secret propagation).
- OLM customization (Scenario 3) is OCP-only. Non-OCP clusters always use embedded operator.
- `local-cluster` behaves as a fully managed cluster: gets its own ManagedClusterAddOn, client certs (agent mode), and ArgoCD instance in `local-cluster` namespace.
- Initial cleanup is forceful (direct spoke access OK). Between-scenario cleanup is hub-triggered only.

## Prerequisites

- An ACM (Advanced Cluster Management) hub OpenShift cluster
- Managed clusters registered to the hub with proper labels
- For Agent mode: Hub ArgoCD configured as principal with `allowedNamespaces: ["*"]`

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

**Important**: The Policy is created once and never automatically updated by the controller.

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
- **Agents receive Applications from the principal via gRPC and create copies in their own namespace** — the agent's own namespace is `openshift-gitops` for remote clusters and `local-cluster` for the hub. This means the `local-cluster` agent stores its Application copies in the `local-cluster` namespace, where the local app controller watches
- For ApplicationSet sync status on the hub: remote cluster apps show status in `openshift-gitops`, while `local-cluster` agent-created apps show status in the `local-cluster` namespace

### Hub Prerequisites for Agent Mode

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
# Check cluster secret exists with proper server URL
kubectl get secret cluster-${CLUSTER_NAME} -n openshift-gitops -o jsonpath='{.data.server}' | base64 -d

# Check principal logs for agent connections
kubectl logs -n openshift-gitops -l app.kubernetes.io/name=openshift-gitops-agent-principal --tail=20

# On managed cluster, check agent logs
KUBECONFIG=/path/to/managed kubectl logs -n openshift-gitops -l app.kubernetes.io/component=argocd-agent --tail=20

# Verify guestbook is deployed
KUBECONFIG=/path/to/managed kubectl get pods -n guestbook
```

---

## Cleanup

All cleanup operations are performed from the hub cluster. **The order matters** — see `argocd_policy.go` for the canonical reference.

### Cleanup Order

The proper cleanup sequence ensures the addon's pre-delete cleanup Job runs successfully on the managed cluster:

```bash
export KUBECONFIG=/path/to/hub/kubeconfig
CLUSTER_NAME="my-managed-cluster"
GITOPSCLUSTER_NAME="my-gitops"
PLACEMENT_NAME="my-placement"

# 1. Delete Placement (prevents GitOpsCluster controller from selecting managed clusters)
kubectl delete placement $PLACEMENT_NAME -n openshift-gitops --ignore-not-found

# 2. Delete Applications from hub (agent mode only)
kubectl delete applications.argoproj.io --all -n $CLUSTER_NAME --ignore-not-found

# 3. Delete GitOpsCluster (stops the controller from recreating Policy/addons)
kubectl delete gitopscluster $GITOPSCLUSTER_NAME -n openshift-gitops --ignore-not-found

# 4. Delete Policy and PlacementBinding (stops enforcement on managed cluster)
#    With GitOpsCluster already deleted, the controller can't recreate these.
kubectl delete policy ${GITOPSCLUSTER_NAME}-argocd-policy -n openshift-gitops --ignore-not-found
kubectl delete placementbinding ${GITOPSCLUSTER_NAME}-argocd-policy-binding -n openshift-gitops --ignore-not-found

# 5. Wait for policy removal to propagate to managed cluster
sleep 10

# 6. Delete ManagedClusterAddOn (triggers pre-delete cleanup Job on managed cluster)
#    The addon has a pre-delete Job that runs "gitopsaddon -cleanup" which removes:
#      - ArgoCD CRs created by the addon
#      - GitOpsService CR (OLM mode)
#      - OLM Subscription/CSV
#      - Operator resources
#    The OCM addon framework removes its finalizer only after the Job completes.
#    DO NOT force-remove finalizers — let the cleanup Job finish naturally.
kubectl delete managedclusteraddon gitops-addon -n $CLUSTER_NAME

# 7. Re-delete Policy (race fix: controller may have recreated it during finalizer processing)
kubectl delete policy ${GITOPSCLUSTER_NAME}-argocd-policy -n openshift-gitops --ignore-not-found
kubectl delete placementbinding ${GITOPSCLUSTER_NAME}-argocd-policy-binding -n openshift-gitops --ignore-not-found
```

### Why This Order?

| Step | Why |
|------|-----|
| Placement first | Without Placement, GitOpsCluster controller can't find managed clusters to re-create addon |
| GitOpsCluster before Policy | Stops the controller from recreating Policy/addons during cleanup |
| Policy before addon | Stops Policy enforcement so the cleanup Job can delete ArgoCD CR without conflict |
| Addon with wait | The pre-delete Job needs time to clean up managed cluster resources; finalizer is removed automatically |
| Re-delete Policy | Race fix: GitOpsCluster controller may recreate Policy during its finalizer processing |

### Important Notes

- **Cluster secrets** are managed by the GitOpsCluster controller and will be cleaned up automatically. Do NOT manually delete them.
- **Do NOT force-remove finalizers** on ManagedClusterAddOn. If deletion hangs, check the pre-delete cleanup Job on the managed cluster: `kubectl get jobs -n open-cluster-management-agent-addon`.
- **Guestbook namespace** (deployed by ArgoCD Application) is not cleaned up by the addon cleanup Job. Delete it manually if needed: `kubectl delete namespace guestbook`.
- **Pause marker ConfigMap**: The pre-delete cleanup Job creates a `gitops-addon-pause` ConfigMap to pause the addon controller during cleanup. On OCP (OLM mode), the Deployment and marker are in different namespaces so the owner reference can't be set, meaning the marker won't be garbage collected. The addon controller **automatically clears stale pause markers at startup**, so this should not be an issue during normal operation.

---

## Automated Testing

There are two test systems: **Go/Ginkgo e2e tests** (CI-ready, Kind-only) and **test-scenarios.sh** (manual/integration, real clusters).

### Go/Ginkgo E2E Tests (CI and Local Development)

These tests run against Kind clusters and are used for both GitHub PR CI and local development. They use the upstream `argocd-operator` (not OCP/OLM) and test the embedded operator path.

**Architecture:** The Makefile creates two Kind clusters (`hub` + `cluster1`), installs OCM via `clusteradm`, registers `cluster1` as a managed cluster and registers the hub itself as `local-cluster`, deploys the ArgoCD operator on the hub, deploys the multicloud-integrations controller, and applies AddOnTemplates. The Go tests then exercise the gitopsaddon by creating GitOpsCluster resources and verifying spoke + local-cluster deployment.

**Test suites:**

| Make Target | Label Filter | What it Tests |
|---|---|---|
| `test-local-e2e-gitopsaddon-embedded` | `embedded` | Non-agent mode: spoke + local-cluster get ArgoCD via embedded operator, guestbook deploys and syncs on both |
| `test-local-e2e-gitopsaddon-embedded-agent` | `embedded-agent` | Agent mode: spoke gets agent pod, ApplicationSet deploys guestbook via principal/agent, local-cluster MCA/ArgoCD verified |

**Local development (creates Kind clusters + runs tests):**

```bash
# Run embedded (non-agent) e2e
make test-local-e2e-gitopsaddon-embedded

# Run embedded-agent e2e
make test-local-e2e-gitopsaddon-embedded-agent

# Clean up Kind clusters
make clean-e2e
```

**CI (assumes Kind clusters and images already loaded):**

```bash
# Run embedded e2e (no cluster setup)
make test-e2e-gitopsaddon-embedded

# Run embedded-agent e2e (no cluster setup)
make test-e2e-gitopsaddon-embedded-agent

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
- **Embedded-agent:** Partially tested — MCA, ArgoCD CR, no duplicate ArgoCD, environment health are verified. Guestbook and controller namespace are skipped (`PIt`) because the Kind hub uses the upstream `argocd-operator` which does not support `argoCDAgent.agent` — it cannot create agent pods. In a real ACM environment, the hub has the Red Hat OpenShift GitOps Operator with agent support, and `test-scenarios.sh` fully tests this path against real OCP clusters.

**Key files:**
- `test/e2e/gitopsaddon/suite_test.go` — constants (cluster names, namespaces)
- `test/e2e/gitopsaddon/helpers_test.go` — YAML generators, wait helpers, verification, cleanup
- `test/e2e/gitopsaddon/gitopsaddon_embedded_test.go` — embedded (non-agent) scenario
- `test/e2e/gitopsaddon/gitopsaddon_embedded_agent_test.go` — embedded + agent scenario
- `test/e2e/gitopsaddon/scripts/setup_env.sh` — environment setup (OCM, ArgoCD, controller)
- `test/e2e/fixtures/` — ArgoCD operator CRDs and deployment YAML

**Environment variables:**
- `E2E_IMG` — controller image (default: `quay.io/stolostron/multicloud-integrations:latest`)
- `ARGOCD_OPERATOR_IMAGE` — ArgoCD operator image (default: `quay.io/argoprojlabs/argocd-operator:latest`)
- `CLUSTERADM_VERSION` — clusteradm CLI version for OCM setup (default: `v1.1.1`)
- `HUB_CLUSTER` — Kind hub cluster name (default: `hub`)
- `SPOKE_CLUSTER` — Kind spoke cluster name (default: `cluster1`)

### test-scenarios.sh (Manual/Integration Testing)

This script tests against real clusters (ACM hub + OCP-managed cluster + Kind-managed cluster) and exercises the full production path including OLM, agent mode with Red Hat operator, and local-cluster with agent support.

```bash
export HUB_KUBECONFIG=~/Desktop/hub
export KIND_KUBECONFIG=~/Desktop/kind-cluster1
export OCP_KUBECONFIG=~/Desktop/ocp-cluster1
export KIND_CLUSTER_NAME=kind-cluster1
export OCP_CLUSTER_NAME=ocp-cluster1

# Run all scenarios
./gitopsaddon/test-scenarios.sh all

# Run individual scenarios
./gitopsaddon/test-scenarios.sh 1  # No Agent (OCP + Kind + local-cluster)
./gitopsaddon/test-scenarios.sh 2  # Agent Mode (OCP + Kind + local-cluster)
./gitopsaddon/test-scenarios.sh 3  # Custom OLM (OCP only)

# Cleanup only
./gitopsaddon/test-scenarios.sh cleanup
```

### Testing Philosophy

**The test script (`test-scenarios.sh`) must NEVER contain hacks or workarounds.** If a verification check fails, the test fails — this means there is a real bug in the code. The entire purpose of the test is to detect problems. Masking failures with workarounds (e.g., force-deleting stuck resources, stripping finalizers, directly fixing state on spoke clusters) defeats the purpose because it hides bugs that users will hit in production.

- **Between-scenario cleanup is hub-triggered ONLY.** The script deletes hub resources (Placement, Policy, GitOpsCluster, ManagedClusterAddOn) and waits for the addon's pre-delete cleanup Job to handle spoke-side cleanup. If spoke-side cleanup fails, the test reports it as a failure — it does NOT directly connect to the spoke to fix it.
- **Initial cleanup (`cleanup_all`) IS allowed to be forceful** — it runs before any scenario to ensure a clean starting state regardless of prior failures. Direct spoke access and finalizer stripping are acceptable here only.
- **Verification is read-only.** After cleanup, the script checks the state of hub and spoke clusters but does NOT modify anything. If resources remain, it reports `CLEANUP FAIL` and returns failure.

### Scenario Verification Details

#### Scenario 1: No Agent — OCP + Kind + local-cluster

Creates: Placement `all-placement`, GitOpsCluster `all-gitops` (addon, no agent), Policy `all-gitops-argocd-policy` with RBAC + guestbook Application.

**Verification checks (all must pass):**

| # | What is checked | Where | How |
|---|----------------|-------|-----|
| 1 | ManagedClusterAddOn `gitops-addon` exists | Hub: `ocp-cluster1`, `kind-cluster1`, `local-cluster` namespaces | `kubectl get managedclusteraddon` |
| 2 | Policy `all-gitops-argocd-policy` is Compliant | Hub: `openshift-gitops` namespace | `kubectl get policy -o jsonpath='{.status.compliant}'` |
| 3 | OLM auto-detected on OCP: subscription exists with `DISABLE_DEFAULT_ARGOCD_INSTANCE=true` | Spoke: ocp-cluster1, `openshift-operators` namespace | `kubectl get subscription.operators.coreos.com` |
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
| 4 | OLM subscription exists on OCP with `installPlanApproval=Manual` | Spoke: ocp-cluster1, `openshift-operators` | `kubectl get subscription.operators.coreos.com -o jsonpath` |
| 5 | OLM subscription has `DISABLE_DEFAULT_ARGOCD_INSTANCE=true` | Spoke: ocp-cluster1, `openshift-operators` | `kubectl get subscription.operators.coreos.com -o jsonpath` |
| 6 | No embedded operator deployment (OLM-only mode) | Spoke: ocp-cluster1, `openshift-gitops-operator` | Verify deployment does NOT exist |

### Cleanup Verification After Each Scenario

After each scenario, `cleanup_scenario` performs hub-triggered deletion, then **read-only verification**. If any check fails, the cleanup returns failure (indicating a code bug).

**Hub-triggered deletion order:**
1. Delete Placement (stops controller from selecting clusters)
2. (Agent mode) Delete ApplicationSet, wait up to 60s for generated Applications to be deleted
3. Delete GitOpsCluster (stops controller from recreating Policy/addons during cleanup)
4. Delete PlacementBinding + Policy with `--wait=false` (stops enforcement on managed clusters)
5. Delete ManagedClusterAddOn for each target cluster (triggers pre-delete cleanup Job — waits up to 600s for the Job to complete naturally, NO finalizer stripping)
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

1. **Policy is User-Owned**: Created once, never auto-updated. Users must modify for RBAC/Apps.

2. **Cleanup Order Matters**: Delete Policy before ManagedClusterAddOn, and GitOpsCluster last. See [Cleanup](#cleanup) section for the full sequence.

3. **Hub Configuration Required for Agent**: The hub ArgoCD must be configured as principal with `allowedNamespaces: ["*"]`.

4. **OLM Mode for OCP Only**: Uses Red Hat operator catalog.

5. **Cluster Secrets Managed by Controller**: Do not manually delete cluster secrets.

6. **Local-Cluster Agent Namespace**: The `local-cluster` agent creates Application copies in the `local-cluster` namespace (its own namespace), not in `openshift-gitops`. This is because the hub's `openshift-gitops` namespace is occupied by the principal's ArgoCD, and the ArgoCD operator uses hardcoded ConfigMap names (`argocd-cm`) that would collide if two ArgoCD CRs were in the same namespace. The agent-side `destinationBasedMapping` must be disabled so `getTargetNamespaceForApp()` returns the agent's own namespace rather than the hub's `openshift-gitops`. For remote clusters, this distinction doesn't matter because both the agent namespace and the hub app namespace are `openshift-gitops`.

7. **Policy Deletion Timing**: OCM governance framework finalizers on Policy objects can take several minutes (up to 10 minutes) to process. The cleanup verification waits up to 600s for Policy deletion.
