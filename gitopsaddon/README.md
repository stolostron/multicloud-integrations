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

All scenarios also deploy to `local-cluster` (hub) alongside the target managed cluster. The hub ArgoCD CR runs in the `local-cluster` namespace.

| # | Scenario | Managed Cluster Type | Application Creation | Success Criteria |
|---|----------|---------------------|---------------------|------------------|
| **1** | gitops-addon (auto-detect) | OCP | Via Policy on managed cluster | OCP auto-detected, OLM subscription with defaults, ArgoCD running, guestbook deployed |
| **2** | gitops-addon (auto-detect) | Non-OCP (Kind) | Via Policy on managed cluster | Non-OCP auto-detected, embedded operator, ArgoCD running, guestbook deployed and pods running |
| **3** | gitops-addon + custom OLM | OCP only | Via Policy on managed cluster | Custom OLM values delivered via env vars, subscription uses custom source, guestbook deployed |
| **4** | gitops-addon + Agent (auto-detect) | OCP | On hub in managed cluster namespace | OCP auto-detected, agent connected, certs provisioned, guestbook deployed via agent |
| **5** | gitops-addon + Agent (auto-detect) | Non-OCP (Kind) | On hub in managed cluster namespace | Non-OCP auto-detected, agent connected, certs provisioned, guestbook deployed via agent |

**Notes:**
- On OCP, guestbook-ui pods **crash** due to OCP's `restricted-v2` SCC (port 80 binding). Success criteria checks that the Deployment resource exists (ArgoCD synced it), not pod health. On Kind, pods run normally.
- Agent mode on non-OCP clusters requires access to Red Hat registry for agent images.
- OLM scenarios are OCP-only (Red Hat operator catalog). Non-OCP clusters always use embedded operator regardless of `olmSubscription.enabled`.
- `local-cluster` behaves as a fully managed cluster: gets its own ManagedClusterAddOn, client certs (agent mode), and ArgoCD instance.
- Custom OLM scenario (3) sets `olmSubscription.enabled: true` with custom values to verify config passthrough from hub to managed cluster. The addon agent reads these values from env vars when creating the OLM subscription.

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

## Scenario 4 & 5: Agent Mode

In agent mode, ArgoCD agents run on managed clusters and connect to a principal on the hub. Applications are:
- **Created on the hub** in the **managed cluster's namespace**
- The destination server URL includes `?agentName=<cluster-name>`

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

# Create Application on hub in managed cluster namespace
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
    server: "https://${SERVER_ADDRESS}:${SERVER_PORT}?agentName=${CLUSTER_NAME}"
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

# 1. Delete Placement (prevents GitOpsCluster controller from recreating addon/policy)
kubectl delete placement $PLACEMENT_NAME -n openshift-gitops --ignore-not-found

# 2. Delete Applications from hub (agent mode only)
kubectl delete applications.argoproj.io --all -n $CLUSTER_NAME --ignore-not-found

# 3. Delete Policy and PlacementBinding (stops enforcement on managed cluster)
#    This must happen BEFORE deleting the addon, so the pre-delete cleanup Job
#    can remove ArgoCD CR without the Policy re-creating it.
kubectl delete policy ${GITOPSCLUSTER_NAME}-argocd-policy -n openshift-gitops --ignore-not-found
kubectl delete placementbinding ${GITOPSCLUSTER_NAME}-argocd-policy-binding -n openshift-gitops --ignore-not-found

# 4. Wait for policy removal to propagate to managed cluster
sleep 10

# 5. Delete ManagedClusterAddOn (triggers pre-delete cleanup Job on managed cluster)
#    The addon has a pre-delete Job that runs "gitopsaddon -cleanup" which removes:
#      - ArgoCD CRs created by the addon
#      - GitOpsService CR (OLM mode)
#      - OLM Subscription/CSV
#      - Operator resources
#    The OCM addon framework removes its finalizer only after the Job completes.
#    DO NOT force-remove finalizers — let the cleanup Job finish naturally.
kubectl delete managedclusteraddon gitops-addon -n $CLUSTER_NAME

# 6. Delete GitOpsCluster (last — per documented cleanup order in argocd_policy.go)
kubectl delete gitopscluster $GITOPSCLUSTER_NAME -n openshift-gitops
```

### Why This Order?

| Step | Why |
|------|-----|
| Placement first | Without Placement, GitOpsCluster controller can't find managed clusters to re-create addon |
| Policy before addon | Stops Policy enforcement so the cleanup Job can delete ArgoCD CR without conflict |
| Addon with wait | The pre-delete Job needs time to clean up managed cluster resources; finalizer is removed automatically |
| GitOpsCluster last | Safe to delete after addon cleanup is complete |

### Important Notes

- **Cluster secrets** are managed by the GitOpsCluster controller and will be cleaned up automatically. Do NOT manually delete them.
- **Do NOT force-remove finalizers** on ManagedClusterAddOn. If deletion hangs, check the pre-delete cleanup Job on the managed cluster: `kubectl get jobs -n open-cluster-management-agent-addon`.
- **Guestbook namespace** (deployed by ArgoCD Application) is not cleaned up by the addon cleanup Job. Delete it manually if needed: `kubectl delete namespace guestbook`.
- **Pause marker ConfigMap**: The pre-delete cleanup Job creates a `gitops-addon-pause` ConfigMap to pause the addon controller during cleanup. On OCP (OLM mode), the Deployment and marker are in different namespaces so the owner reference can't be set, meaning the marker won't be garbage collected. The addon controller **automatically clears stale pause markers at startup**, so this should not be an issue during normal operation.

---

## Automated Testing

Use the test script to run all scenarios:

```bash
export HUB_KUBECONFIG=~/Desktop/hub
export KIND_KUBECONFIG=~/Desktop/kind-cluster1
export OCP_KUBECONFIG=~/Desktop/ocp-cluster5
export KIND_CLUSTER_NAME=kind-cluster1
export OCP_CLUSTER_NAME=ocp-cluster5

# Run all scenarios
./gitopsaddon/test-scenarios.sh all

# Run individual scenarios
./gitopsaddon/test-scenarios.sh 1  # gitops-addon on OCP (OLM auto-detected)
./gitopsaddon/test-scenarios.sh 2  # gitops-addon on Kind (embedded manifests)
./gitopsaddon/test-scenarios.sh 3  # gitops-addon + custom OLM on OCP (Manual approval)
./gitopsaddon/test-scenarios.sh 4  # gitops-addon + Agent on OCP (OLM auto-detected)
./gitopsaddon/test-scenarios.sh 5  # gitops-addon + Agent on Kind (embedded manifests)

# Cleanup only
./gitopsaddon/test-scenarios.sh cleanup
```

### Success Criteria Per Scenario

**Non-Agent Scenarios (1, 2, 3):**
- ManagedClusterAddOn `gitops-addon` created in managed cluster namespace
- Policy created and becomes Compliant
- ArgoCD CR `acm-openshift-gitops` running on managed cluster
- Guestbook application synced by ArgoCD (deployment resource exists in `guestbook` namespace)
  - **Kind**: guestbook-ui pods should be running (healthy)
  - **OCP**: guestbook-ui pods will crash due to `restricted-v2` SCC (port 80 binding) - this is expected

**Agent Scenarios (4, 5):**
- ManagedClusterAddOn `gitops-addon` created in managed cluster namespace
- Policy created and becomes Compliant
- Principal server address auto-discovered and stored in GitOpsCluster
- Cluster secret created with `agentName` query parameter in server URL
- ArgoCD agent pod running on managed cluster
- Guestbook application created on hub, synced to managed cluster via agent
  - **Kind**: guestbook-ui pods should be running (healthy)
  - **OCP**: guestbook-ui pods will crash due to `restricted-v2` SCC (port 80 binding) - this is expected
- Application sync status reflected back on hub

### Cleanup Behavior

There are **two layers of cleanup** in the test script:

1. **Initial cleanup (`cleanup_all`)**: Runs before the first scenario. Directly connects to all managed clusters and forcibly removes any residual resources (ArgoCD CRs, OLM subscriptions, CSVs, operator deployments, stale pause markers). Verifies every cluster is truly clean before proceeding. **Fails loudly** if resources are left behind.

2. **Per-scenario cleanup (`cleanup_scenario`)**: Runs after each scenario. Operates **from the hub only** — deletes Placement, Policy, ManagedClusterAddOn, and GitOpsCluster in the correct order. The addon's pre-delete cleanup Job handles managed-cluster-side cleanup:
   - Pauses the addon controller (via pause marker ConfigMap)
   - **On remote managed clusters**: Deletes ArgoCD CRs, OLM subscriptions, operator resources, RBAC
   - **On hub (local-cluster)**: Conservative cleanup — only deletes ArgoCD CRs labeled `apps.open-cluster-management.io/gitopsaddon=true` and ClusterRoleBinding
   - Deletes the cleanup Job's own ClusterRoleBinding as the final step

   After the addon cleanup completes, the script **verifies** the managed cluster is clean (ArgoCD CR removed, OLM subscription removed, operator deployment removed, guestbook namespace removed). If the addon cleanup fails to remove resources, the script reports errors — this indicates a code bug in the addon cleanup logic.

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
