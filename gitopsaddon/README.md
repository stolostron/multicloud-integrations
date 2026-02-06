# GitOps Addon - Configuration Guide

This guide explains how to configure and test the GitOps Addon functionality using ACM hub and managed clusters.

## Key Concepts

- **GitOpsCluster CR**: Defines which managed clusters should have ArgoCD deployed via the GitOps addon
- **Placement**: Selects which managed clusters are targeted
- **Policy**: The GitOpsCluster controller creates a Policy that deploys the ArgoCD CR to managed clusters. **This Policy is created once and is user-owned** - users can modify it to add RBAC, Applications, or customize ArgoCD settings.
- **ManagedClusterAddOn**: Created automatically when GitOps addon is enabled - manages addon lifecycle on managed clusters
- **Cluster Secrets**: For agent mode, the controller creates ArgoCD cluster secrets with proper server URLs including `agentName` query parameter

## Supported Scenarios

| Scenario | Description | Managed Cluster Type | Application Creation |
|----------|-------------|---------------------|---------------------|
| **1. gitops-addon** | Deploys ArgoCD via Helm charts | OCP & Non-OCP | Via Policy or directly on managed cluster |
| **2. gitops-addon + OLM** | Deploys ArgoCD via OLM Subscription | OCP only | Via Policy or directly on managed cluster |
| **3. gitops-addon + Agent** | ArgoCD with agent for pull-based sync | OCP & Non-OCP* | On hub in managed cluster namespace |
| **4. gitops-addon + Agent + OLM** | Agent mode deployed via OLM | OCP only | On hub in managed cluster namespace |

*Agent mode on non-OCP clusters requires access to Red Hat registry for agent images.

## Prerequisites

- An ACM (Advanced Cluster Management) hub OpenShift cluster
- Managed clusters registered to the hub with proper labels
- For Agent mode: Hub ArgoCD configured as principal with `allowedNamespaces: ["*"]`

---

## Scenario 1 & 2: Non-Agent Mode

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
# Create a Placement to select managed clusters
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
EOF

# Create GitOpsCluster with gitopsAddon enabled
# For OLM mode (OCP only), add olmSubscription.enabled: true
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
    # olmSubscription:
    #   enabled: true  # Uncomment for OCP clusters with OLM
EOF
```

### Step 3: Modify Policy to Add RBAC and Application

The GitOpsCluster controller creates a Policy but does NOT include RBAC or Applications by default. Users must modify the Policy.

**Important**: The Policy is created once and never automatically updated by the controller.

```bash
# Wait for the Policy to be created
kubectl get policy my-gitops-argocd-policy -n openshift-gitops

# Add RBAC and Application to the Policy
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

## Scenario 3 & 4: Agent Mode

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
# Create Placement
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
    #   enabled: true  # For OCP clusters
EOF
```

### Step 2: Modify Policy to Add RBAC

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

All cleanup operations are performed from the hub cluster. The order matters!

```bash
export KUBECONFIG=/path/to/hub/kubeconfig
CLUSTER_NAME="my-managed-cluster"

# 1. Delete Applications from hub (for agent mode)
kubectl delete applications.argoproj.io --all -n $CLUSTER_NAME --ignore-not-found

# 2. Delete GitOpsCluster (stops controller from recreating Policy)
kubectl delete gitopscluster <name> -n openshift-gitops

# 3. Delete Policy and PlacementBinding
kubectl delete policy <name>-argocd-policy -n openshift-gitops --ignore-not-found
kubectl delete placementbinding <name>-argocd-policy-binding -n openshift-gitops --ignore-not-found

# 4. Delete ManagedClusterAddOn (triggers cleanup job)
kubectl delete managedclusteraddon gitops-addon -n $CLUSTER_NAME

# 5. Delete Placement
kubectl delete placement <name> -n openshift-gitops
```

**Note**: Cluster secrets are managed by the GitOpsCluster controller and will be cleaned up automatically. Do NOT manually delete them.

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
./gitopsaddon/test-scenarios.sh 1  # Non-agent on Kind
./gitopsaddon/test-scenarios.sh 2  # Non-agent + OLM on OCP
./gitopsaddon/test-scenarios.sh 3  # Agent on Kind
./gitopsaddon/test-scenarios.sh 4  # Agent + OLM on OCP

# Cleanup only
./gitopsaddon/test-scenarios.sh cleanup
```

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

2. **Cleanup Order Matters**: Delete GitOpsCluster before Policy.

3. **Hub Configuration Required for Agent**: The hub ArgoCD must be configured as principal with `allowedNamespaces: ["*"]`.

4. **OLM Mode for OCP Only**: Uses Red Hat operator catalog.

5. **Cluster Secrets Managed by Controller**: Do not manually delete cluster secrets.
