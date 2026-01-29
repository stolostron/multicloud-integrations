# GitOps Addon with Argo CD Agent - End-to-End Testing Guide

This guide explains how to test the GitOps Addon with Argo CD Agent functionality from scratch using an ACM hub cluster and a managed OpenShift cluster.

## Prerequisites

- An ACM (Advanced Cluster Management) hub OpenShift cluster
- A managed OpenShift cluster registered to the hub
- `oc` CLI tool configured
- Access to both cluster kubeconfigs

## Architecture Overview

The Argo CD Agent architecture consists of:

1. **Hub Cluster (Principal)**: Runs the Argo CD principal component that coordinates with agents on managed clusters
2. **Managed Cluster (Agent)**: Runs the Argo CD agent component that reconciles applications locally and reports status back

```
+------------------------------------------+       +------------------------------------------+
|             Hub Cluster                  |       |          Managed Cluster                 |
|                                          |       |                                          |
|  +------------------------------------+  |       |  +------------------------------------+  |
|  |     openshift-gitops namespace     |  |       |  |     openshift-gitops namespace     |  |
|  |                                    |  |       |  |                                    |  |
|  |  +------------------------------+  |  |       |  |  +------------------------------+  |  |
|  |  |   ArgoCD CR (Principal)      |  |  |       |  |  |   ArgoCD CR (Agent)          |  |  |
|  |  |   - controller: disabled     |  |  |       |  |  |   - argoCDAgent.agent        |  |  |
|  |  |   - principal: enabled       |  |  |       |  |  |     enabled                  |  |  |
|  |  +------------------------------+  |  |       |  |  +------------------------------+  |  |
|  |                                    |  |       |  |                                    |  |
|  |  +------------------------------+  |  |       |  |  +------------------------------+  |  |
|  |  |   Principal Pod              |<-+--+-------+--+->|   Agent Pod                  |  |  |
|  |  |   (coordinates agents)       |  |  | gRPC  |  |  |   (reconciles apps)          |  |  |
|  |  +------------------------------+  |  |       |  |  +------------------------------+  |  |
|  |                                    |  |       |  |                                    |  |
|  +------------------------------------+  |       |  +------------------------------------+  |
|                                          |       |                                          |
|  +------------------------------------+  |       |                                          |
|  |     <managed-cluster> namespace    |  |       |                                          |
|  |                                    |  |       |                                          |
|  |  +------------------------------+  |  |       |                                          |
|  |  |   Application CRs            |  |  |       |                                          |
|  |  |   (created by user)          |--+--+-------+---> Propagated to agent                  |
|  |  +------------------------------+  |  |       |                                          |
|  |                                    |  |       |                                          |
|  +------------------------------------+  |       |                                          |
|                                          |       |                                          |
+------------------------------------------+       +------------------------------------------+
```

## Step 1: Install OpenShift GitOps Operator on the Hub Cluster

Install the OpenShift GitOps operator from OperatorHub on the hub cluster:

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

# Install OpenShift GitOps operator via Subscription
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: openshift-gitops-operator
  namespace: openshift-operators
spec:
  channel: latest
  installPlanApproval: Automatic
  name: openshift-gitops-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF

# Wait for the operator to be ready
echo "Waiting for OpenShift GitOps operator..."
sleep 60
oc wait --for=condition=Available deployment/gitops-operator-controller-manager -n openshift-gitops-operator --timeout=300s
```

## Step 2: Configure ArgoCD CR on Hub with Principal Mode

Delete the default ArgoCD CR and create one configured as a principal:

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

# Delete the default ArgoCD CR
oc delete argocd openshift-gitops -n openshift-gitops --ignore-not-found

# Create the ArgoCD CR configured as Principal
cat <<EOF | oc apply -f -
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: openshift-gitops
  namespace: openshift-gitops
spec:
  # Disable the application controller on the hub - applications are managed by agents
  controller:
    enabled: false
  # Enable the principal component for argocd-agent
  argoCDAgent:
    principal:
      enabled: true
      # mTLS authentication pattern matching OCM addon certificate format
      auth: "mtls:CN=system:open-cluster-management:cluster:([^:]+):addon:gitops-addon:agent:gitops-addon-agent"
      logLevel: info
      server:
        # Use Route for external access (OpenShift)
        route:
          enabled: true
      namespace:
        # Allow applications from any namespace
        allowedNamespaces:
          - "*"
      tls:
        # Let the operator generate TLS certificates
        secretName: argocd-agent-principal-tls
        rootCASecretName: argocd-agent-ca
      jwt:
        secretName: argocd-agent-jwt
  # Allow applications to be created in managed cluster namespaces
  sourceNamespaces:
    - "*"
EOF

# Wait for the principal to be ready
echo "Waiting for Argo CD principal to be ready..."
sleep 30
oc wait --for=condition=Available deployment/openshift-gitops-agent-principal -n openshift-gitops --timeout=300s
```

## Step 3: Get the Principal Server Address

The principal exposes an endpoint that agents connect to. Get this address:

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

# For Route (OpenShift)
PRINCIPAL_ADDRESS=$(oc get route openshift-gitops-agent-principal -n openshift-gitops -o jsonpath='{.spec.host}')
echo "Principal Address (Route): $PRINCIPAL_ADDRESS"

# For LoadBalancer (if Route is not available)
# PRINCIPAL_ADDRESS=$(oc get svc openshift-gitops-agent-principal -n openshift-gitops -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
# echo "Principal Address (LoadBalancer): $PRINCIPAL_ADDRESS"
```

## Step 4: Create RBAC Policy for Managed Clusters

Create a Policy on the hub that will propagate the necessary RBAC to managed clusters. This grants the Argo CD application controller permissions to manage resources:

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

cat <<EOF | oc apply -f -
apiVersion: policy.open-cluster-management.io/v1
kind: Policy
metadata:
  name: argocd-agent-rbac-policy
  namespace: openshift-gitops
  annotations:
    policy.open-cluster-management.io/standards: NIST-CSF
    policy.open-cluster-management.io/categories: PR.PT Protective Technology
    policy.open-cluster-management.io/controls: PR.PT-3 Least Functionality
spec:
  remediationAction: enforce
  disabled: false
  policy-templates:
    - objectDefinition:
        apiVersion: policy.open-cluster-management.io/v1
        kind: ConfigurationPolicy
        metadata:
          name: argocd-agent-rbac-config
        spec:
          remediationAction: enforce
          severity: medium
          object-templates:
            - complianceType: musthave
              objectDefinition:
                apiVersion: rbac.authorization.k8s.io/v1
                kind: ClusterRole
                metadata:
                  name: acm-openshift-gitops-cluster-admin
                rules:
                  - apiGroups:
                      - '*'
                    resources:
                      - '*'
                    verbs:
                      - '*'
                  - nonResourceURLs:
                      - '*'
                    verbs:
                      - '*'
            - complianceType: musthave
              objectDefinition:
                apiVersion: rbac.authorization.k8s.io/v1
                kind: ClusterRoleBinding
                metadata:
                  name: acm-openshift-gitops-cluster-admin
                roleRef:
                  apiGroup: rbac.authorization.k8s.io
                  kind: ClusterRole
                  name: acm-openshift-gitops-cluster-admin
                subjects:
                  - kind: ServiceAccount
                    name: acm-openshift-gitops-argocd-application-controller
                    namespace: openshift-gitops
---
apiVersion: policy.open-cluster-management.io/v1
kind: PlacementBinding
metadata:
  name: argocd-agent-rbac-placement-binding
  namespace: openshift-gitops
placementRef:
  name: all-openshift-clusters
  kind: Placement
  apiGroup: cluster.open-cluster-management.io
subjects:
  - name: argocd-agent-rbac-policy
    kind: Policy
    apiGroup: policy.open-cluster-management.io
EOF
```

## Step 5: Create GitOpsCluster CR with Argo CD Agent Enabled

Create the Placement and GitOpsCluster CR that will deploy the GitOps Addon to managed clusters:

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

# Replace with your actual managed cluster name
MANAGED_CLUSTER_NAME="ocp-cluster1"

# Create a Placement to target managed clusters (exclude local-cluster)
cat <<EOF | oc apply -f -
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: all-openshift-clusters
  namespace: openshift-gitops
spec:
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchExpressions:
            - key: vendor
              operator: In
              values:
                - OpenShift
            - key: name
              operator: NotIn
              values:
                - local-cluster
EOF

# Create the GitOpsCluster CR with Argo CD Agent enabled
cat <<EOF | oc apply -f -
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
    argoCDAgent:
      enabled: true
      propagateHubCA: true
EOF

# Wait for the GitOpsCluster to be ready
echo "Waiting for GitOpsCluster to be ready..."
sleep 120
oc get gitopscluster gitopscluster -n openshift-gitops -o jsonpath='{.status.conditions}' | jq .
```

## Step 6: Verify the Agent is Running on the Managed Cluster

Check that the argocd-agent is deployed and running on the managed cluster:

```bash
export KUBECONFIG=/path/to/managed/kubeconfig

# Check the gitops-addon pod
echo "Checking gitops-addon pod..."
oc get pods -n open-cluster-management-agent-addon | grep gitops

# Check the ArgoCD CR on the managed cluster
echo "Checking ArgoCD CR with agent configuration..."
oc get argocd acm-openshift-gitops -n openshift-gitops -o jsonpath='{.spec.argoCDAgent}' | jq .

# Check the agent pod is running
echo "Checking Argo CD agent pod..."
oc get pods -n openshift-gitops | grep agent

# Check RBAC was applied by the policy
echo "Checking RBAC from policy..."
oc get clusterrolebinding acm-openshift-gitops-cluster-admin

# Check agent logs for connectivity
echo "Checking agent connectivity..."
oc logs -n openshift-gitops -l app.kubernetes.io/name=acm-openshift-gitops-agent-agent --tail=10
```

## Step 7: Create a Test Application on the Hub

First, create and label the target namespace on the managed cluster:

```bash
export KUBECONFIG=/path/to/managed/kubeconfig

# Create and label the target namespace
oc create ns guestbook 2>/dev/null || true
oc label ns guestbook argocd.argoproj.io/managed-by=acm-openshift-gitops --overwrite
```

Now create the Application on the hub cluster:

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

# Get the principal server address (Route or LoadBalancer)
PRINCIPAL_ADDRESS=$(oc get route openshift-gitops-agent-principal -n openshift-gitops -o jsonpath='{.spec.host}' 2>/dev/null)
if [ -z "$PRINCIPAL_ADDRESS" ]; then
  PRINCIPAL_ADDRESS=$(oc get svc openshift-gitops-agent-principal -n openshift-gitops -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
fi

# Replace with your managed cluster name
MANAGED_CLUSTER_NAME="ocp-cluster1"

# Create the Application in the managed cluster's namespace on the hub
cat <<EOF | oc apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook
  namespace: ${MANAGED_CLUSTER_NAME}
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps
    targetRevision: HEAD
    path: kustomize-guestbook
  destination:
    # The server URL includes the agentName query parameter to route to the correct agent
    server: https://${PRINCIPAL_ADDRESS}:443?agentName=${MANAGED_CLUSTER_NAME}
    namespace: guestbook
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
EOF
```

## Step 8: Trigger Sync and Verify Application

Trigger a sync on the managed cluster and verify the application status:

```bash
export KUBECONFIG=/path/to/managed/kubeconfig

# Trigger sync
oc patch applications.argoproj.io guestbook -n openshift-gitops \
  -p '{"operation":{"initiatedBy":{"username":"admin"},"sync":{}}}' --type=merge

# Wait for sync
sleep 60

# Check the Hub
export KUBECONFIG=/path/to/hub/kubeconfig
echo "=== Hub Application Status ==="
oc get applications.argoproj.io guestbook -n ${MANAGED_CLUSTER_NAME}

# Check the Managed Cluster
export KUBECONFIG=/path/to/managed/kubeconfig
echo "=== Managed Cluster Application Status ==="
oc get applications.argoproj.io guestbook -n openshift-gitops

echo "=== Guestbook Resources ==="
oc get pods -n guestbook
```

Expected output:
- Hub should show: `guestbook   Synced   Healthy` (or `Progressing` if still syncing)
- Managed should show: `guestbook   Synced   Healthy`
- Guestbook pods should be running in the `guestbook` namespace

## Troubleshooting

### Agent Not Connecting to Principal

1. Check the agent logs:
   ```bash
   oc logs -n openshift-gitops -l app.kubernetes.io/name=acm-openshift-gitops-agent-agent --tail=50
   ```

2. Verify the CA certificate is propagated:
   ```bash
   oc get secret argocd-agent-ca -n openshift-gitops -o jsonpath='{.data.ca\.crt}' | base64 -d | openssl x509 -text -noout
   ```

3. Check the principal is accessible:
   ```bash
   curl -vk https://${PRINCIPAL_ADDRESS}:443
   ```

### Application Not Syncing

1. Check application controller logs on the managed cluster:
   ```bash
   oc logs -n openshift-gitops statefulset/acm-openshift-gitops-application-controller --tail=50
   ```

2. Verify RBAC Policy is compliant:
   ```bash
   # On hub
   oc get policy argocd-agent-rbac-policy -n openshift-gitops
   ```

3. Check if the namespace is properly labeled:
   ```bash
   oc get ns guestbook -o jsonpath='{.metadata.labels}'
   ```

### Policy Not Being Applied

1. Check the GitOpsCluster status:
   ```bash
   oc get gitopscluster gitopscluster -n openshift-gitops -o yaml
   ```

2. Check the Policy status:
   ```bash
   oc get policy -n openshift-gitops
   oc describe policy gitopscluster-argocd-policy -n openshift-gitops
   ```

3. Check the ConfigurationPolicy on the managed cluster:
   ```bash
   oc get configurationpolicy -n ${MANAGED_CLUSTER_NAME}
   ```

## Cleanup

To remove the test setup:

```bash
export KUBECONFIG=/path/to/hub/kubeconfig

# Delete Application
oc delete application guestbook -n ${MANAGED_CLUSTER_NAME}

# Delete GitOpsCluster
oc delete gitopscluster gitopscluster -n openshift-gitops

# Delete RBAC Policy
oc delete policy argocd-agent-rbac-policy -n openshift-gitops
oc delete placementbinding argocd-agent-rbac-placement-binding -n openshift-gitops

# Delete Placement
oc delete placement all-openshift-clusters -n openshift-gitops

# Delete ArgoCD CR
oc delete argocd openshift-gitops -n openshift-gitops
```

## Notes

- The guestbook application may show `Progressing` status on OpenShift because the sample uses port 80 which requires elevated privileges. This is expected behavior and demonstrates that the sync is working.
- For production use, consider using applications that are compatible with OpenShift security constraints.
- The `sourceNamespaces` configuration on the hub ArgoCD CR allows applications to be created in managed cluster namespaces.
- The RBAC Policy can be customized based on your security requirements - the example grants cluster-admin permissions for simplicity.
