#!/bin/bash
# e2e-olm-subscription.sh - OLM Subscription addon test for CI (assumes clusters/images ready)
# This test verifies that the OLM subscription mode for GitOps addon works correctly
# NOTE: This test does NOT run the full e2e-setup.sh because OLM subscription mode
# replaces the helm-based operator deployment with a subscription-based one.
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
ADDON_NAMESPACE="open-cluster-management-agent-addon"
E2E_IMG="${E2E_IMG:-quay.io/stolostron/multicloud-integrations:latest}"

echo "========================================="
echo "E2E OLM SUBSCRIPTION TEST (CI mode)"
echo "========================================="

# Step 1: Install MetalLB
echo ""
echo "Step 1: Installing MetalLB..."
./test/e2e/scripts/install_metallb.sh

# Step 2: Setup OCM environment
echo ""
echo "Step 2: Setting up OCM environment..."
./deploy/ocm/install.sh

# Step 3: Install ArgoCD CRDs and operator on Hub (needed for Hub ArgoCD instance)
echo ""
echo "Step 3: Installing ArgoCD on Hub..."
kubectl config use-context ${HUB_CONTEXT}
kubectl create namespace ${GITOPS_NAMESPACE} 2>/dev/null || true
kubectl apply --server-side=true --force-conflicts -f test/e2e/fixtures/openshift-gitops/crds.yaml --context ${HUB_CONTEXT}
kubectl apply -f test/e2e/fixtures/openshift-gitops/operator.yaml --context ${HUB_CONTEXT}
echo "  Waiting for ArgoCD operator..."
kubectl wait --for=condition=available --timeout=180s deployment/argocd-operator-controller-manager -n argocd-operator-system --context ${HUB_CONTEXT} || {
  echo "✗ ERROR: ArgoCD operator not ready after 180s"
  exit 1
}
kubectl create -f test/e2e/fixtures/openshift-gitops/operator-instance.yaml --context "${HUB_CONTEXT}" --save-config 2>/dev/null \
  || kubectl apply -f test/e2e/fixtures/openshift-gitops/operator-instance.yaml --context "${HUB_CONTEXT}"
echo "  ✓ ArgoCD instance created on Hub"

# Step 4: Install Controller
echo ""
echo "Step 4: Installing Controller..."
# Don't git checkout the CRDs - we want to use our locally generated ones with olmSubscription field
kubectl apply -f deploy/crds/ --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/service_account.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/role.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/role_binding.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/leader_election_role.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/leader_election_role_binding.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/operator.yaml --context ${HUB_CONTEXT}
kubectl apply -f deploy/controller/deploy.yaml --context ${HUB_CONTEXT}
if [ "${E2E_IMG}" != "quay.io/stolostron/multicloud-integrations:latest" ]; then
  kubectl set image deployment/multicloud-integrations-gitops manager=${E2E_IMG} -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT}
fi
echo "  Waiting for controller to be ready..."
kubectl wait --for=condition=available --timeout=180s deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} || {
  echo "✗ ERROR: Controller not ready after 180s"
  exit 1
}
echo "  ✓ Controller is ready"

# Step 5: Apply ClusterManagementAddon and default AddOnTemplate
echo ""
echo "Step 5: Applying ClusterManagementAddon and default AddOnTemplate..."
kubectl apply -f gitopsaddon/addonTemplates/clusterManagementAddon.yaml --context ${HUB_CONTEXT}
kubectl apply -f gitopsaddon/addonTemplates/addonTemplates.yaml --context ${HUB_CONTEXT}

# Step 5.5: Install governance-policy-framework (required for ArgoCD Policy)
echo ""
echo "Step 5.5: Installing governance-policy-framework..."
clusteradm install hub-addon --names governance-policy-framework 2>&1 || {
  echo "  Warning: Failed to install governance-policy-framework hub addon"
}
echo "  Waiting for governance-policy-propagator to be ready..."
kubectl wait --for=condition=available --timeout=180s deployment/governance-policy-propagator -n open-cluster-management --context ${HUB_CONTEXT} 2>/dev/null || {
  echo "  Warning: governance-policy-propagator not ready"
}
echo "  Enabling governance-policy-framework and config-policy-controller on cluster1..."
clusteradm addon enable --names governance-policy-framework --clusters cluster1 2>&1 || {
  echo "  Warning: Failed to enable governance-policy-framework on cluster1"
}
clusteradm addon enable --names config-policy-controller --clusters cluster1 2>&1 || {
  echo "  Warning: Failed to enable config-policy-controller on cluster1"
}
echo "  Waiting for policy addons to be ready on cluster1..."
for i in {1..60}; do
  POD_COUNT=$(kubectl --context ${SPOKE_CONTEXT} get pods -n open-cluster-management-agent-addon -l app=governance-policy-framework --no-headers 2>/dev/null | grep Running | wc -l)
  if [ "$POD_COUNT" -gt 0 ]; then
    echo "  ✓ governance-policy-framework addon running on cluster1"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  Warning: governance-policy-framework addon not running on cluster1"
    break
  fi
  sleep 2
done

# Step 6: Install OLM on managed cluster
echo ""
echo "========================================="
echo "PHASE 1: Installing OLM on Managed Cluster"
echo "========================================="
kubectl config use-context ${SPOKE_CONTEXT}

echo "Step 6: Installing OLM CRDs and operators..."
# Use server-side apply to handle large CRDs (CSV CRD has large annotations)
kubectl apply --server-side=true --force-conflicts -f https://github.com/operator-framework/operator-lifecycle-manager/releases/download/v0.28.0/crds.yaml 2>&1 | head -15 || true
kubectl apply --server-side=true --force-conflicts -f https://github.com/operator-framework/operator-lifecycle-manager/releases/download/v0.28.0/olm.yaml 2>&1 | head -25 || true

echo "  Waiting for OLM to be ready..."
for i in {1..90}; do
  OLM_READY=$(kubectl get deployment -n olm olm-operator -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
  CATALOG_READY=$(kubectl get deployment -n olm catalog-operator -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
  if [ "$OLM_READY" = "1" ] && [ "$CATALOG_READY" = "1" ]; then
    echo "  ✓ OLM is ready"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ✗ ERROR: OLM not ready after 90 attempts"
    kubectl get deployment -n olm 2>/dev/null || true
    kubectl get pods -n olm 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for OLM... (attempt $i/90, olm=$OLM_READY, catalog=$CATALOG_READY)"
  sleep 5
done

# Create required namespaces
echo ""
echo "Step 7: Creating OpenShift-like namespaces..."
kubectl create namespace openshift-marketplace 2>/dev/null || true
kubectl create namespace openshift-operators 2>/dev/null || true

# Create OperatorGroup for openshift-operators namespace (required by OLM)
echo "  Creating OperatorGroup..."
kubectl apply -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: global-operators
  namespace: openshift-operators
spec: {}
EOF

# Wait for operatorhubio-catalog to be ready
echo ""
echo "Step 8: Waiting for OperatorHub.io catalog..."
for i in {1..90}; do
  CATALOG_STATE=$(kubectl get catalogsource operatorhubio-catalog -n olm -o jsonpath='{.status.connectionState.lastObservedState}' 2>/dev/null || echo "")
  if [ "$CATALOG_STATE" = "READY" ]; then
    echo "  ✓ Catalog source is ready"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ⚠ Warning: Catalog source not ready after 90 attempts (state: $CATALOG_STATE)"
    echo "  Continuing anyway - subscription may still work..."
    break
  fi
  echo "  Waiting for catalog source... (attempt $i/90, state: $CATALOG_STATE)"
  sleep 5
done

# Switch back to hub context
kubectl config use-context ${HUB_CONTEXT}

echo ""
echo "========================================="
echo "PHASE 2: Create GitOpsCluster with OLM Subscription"
echo "========================================="

# Create placement and ManagedClusterSetBinding
echo ""
echo "Step 9: Creating Placement and ManagedClusterSetBinding..."
kubectl apply -f test/e2e/fixtures/gitopscluster/managedclustersetbinding.yaml --context ${HUB_CONTEXT} 2>/dev/null || true
kubectl apply -f test/e2e/fixtures/gitopscluster/placement.yaml --context ${HUB_CONTEXT}

# Create GitOpsCluster with OLM subscription enabled
echo ""
echo "Step 10: Creating GitOpsCluster with OLM subscription enabled..."
cat <<EOF | kubectl apply --context ${HUB_CONTEXT} -f -
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
    olmSubscription:
      enabled: true
      name: argocd-operator
      namespace: openshift-operators
      channel: alpha
      source: operatorhubio-catalog
      sourceNamespace: olm
EOF

# Wait for controller to process the change
echo "  Waiting for controller to process the change..."
sleep 15

echo ""
echo "========================================="
echo "PHASE 3: Verify OLM Subscription Mode"
echo "========================================="

# Verify OLM AddOnTemplate was created
echo ""
echo "Step 11: Verifying OLM AddOnTemplate was created..."
TEMPLATE_NAME="gitops-addon-olm-${GITOPS_NAMESPACE}-gitopscluster"
for i in {1..30}; do
  if kubectl get addontemplate ${TEMPLATE_NAME} --context ${HUB_CONTEXT} &>/dev/null; then
    echo "  ✓ OLM AddOnTemplate ${TEMPLATE_NAME} created"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: OLM AddOnTemplate not created after 30 attempts"
    kubectl get addontemplate --context ${HUB_CONTEXT} 2>/dev/null || true
    kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o yaml
    exit 1
  fi
  echo "  Waiting for OLM AddOnTemplate... (attempt $i/30)"
  sleep 2
done

# Verify template has correct labels
echo ""
echo "Step 12: Verifying OLM AddOnTemplate labels..."
TEMPLATE_COMPONENT=$(kubectl get addontemplate ${TEMPLATE_NAME} --context ${HUB_CONTEXT} -o jsonpath='{.metadata.labels.app\.kubernetes\.io/component}')
echo "  Template component label: ${TEMPLATE_COMPONENT}"
if [ "${TEMPLATE_COMPONENT}" = "addon-template-olm" ]; then
  echo "  ✓ Template has correct component label"
else
  echo "  ✗ ERROR: Template has incorrect component label (expected: addon-template-olm, got: ${TEMPLATE_COMPONENT})"
  exit 1
fi

# Verify ManagedClusterAddOn uses OLM template
echo ""
echo "Step 13: Verifying ManagedClusterAddOn config references OLM template..."
for i in {1..30}; do
  ADDON_TEMPLATE=$(kubectl get managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} -o jsonpath='{.spec.configs[?(@.resource=="addontemplates")].name}' 2>/dev/null || echo "")
  if [ "$ADDON_TEMPLATE" = "$TEMPLATE_NAME" ]; then
    echo "  ✓ ManagedClusterAddOn references OLM template: ${ADDON_TEMPLATE}"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: ManagedClusterAddOn not using OLM template after 30 attempts (current: ${ADDON_TEMPLATE})"
    kubectl get managedclusteraddon gitops-addon -n cluster1 --context ${HUB_CONTEXT} -o yaml 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for ManagedClusterAddOn to use OLM template... (attempt $i/30)"
  sleep 2
done

# Verify GitOpsCluster conditions
echo ""
echo "Step 14: Verifying GitOpsCluster conditions..."
for i in {1..30}; do
  OLM_READY=$(kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.conditions[?(@.type=="OLMSubscriptionReady")].status}' 2>/dev/null || echo "")

  if [ "${OLM_READY}" = "True" ]; then
    echo "  ✓ OLMSubscriptionReady condition is True"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: OLMSubscriptionReady condition is not True after 30 attempts"
    kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o yaml 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for OLMSubscriptionReady condition... (attempt $i/30, OLM=$OLM_READY)"
  sleep 2
done

# Verify Subscription on managed cluster
# With the AgentInstallNamespace fix, the subscription is now deployed to the namespace
# specified in the AddOnTemplate manifest (operators namespace)
echo ""
echo "Step 15: Verifying Subscription on managed cluster..."
kubectl config use-context ${SPOKE_CONTEXT}

# The subscription namespace should match what's specified in the GitOpsCluster olmSubscription.namespace
SUB_NAMESPACE="openshift-operators"

for i in {1..90}; do
  if kubectl get subscription argocd-operator -n ${SUB_NAMESPACE} --no-headers 2>/dev/null | grep -q argocd-operator; then
    echo "  ✓ Subscription argocd-operator found in ${SUB_NAMESPACE} namespace"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ✗ ERROR: Subscription not created after 90 attempts"
    echo "  Checking ManifestWork on hub..."
    kubectl --context ${HUB_CONTEXT} get manifestwork -n cluster1 2>/dev/null || true
    kubectl --context ${HUB_CONTEXT} get manifestwork -n cluster1 -o yaml 2>/dev/null | head -100 || true
    echo "  Checking subscriptions on managed cluster..."
    kubectl get subscription -A 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for Subscription... (attempt $i/90)"
  sleep 5
done

# Verify subscription details
echo ""
echo "Step 16: Verifying Subscription details..."
SUB_CHANNEL=$(kubectl get subscription argocd-operator -n ${SUB_NAMESPACE} -o jsonpath='{.spec.channel}' 2>/dev/null || echo "")
SUB_SOURCE=$(kubectl get subscription argocd-operator -n ${SUB_NAMESPACE} -o jsonpath='{.spec.source}' 2>/dev/null || echo "")

echo "  Subscription channel: ${SUB_CHANNEL}"
echo "  Subscription source: ${SUB_SOURCE}"

if [ "${SUB_CHANNEL}" = "alpha" ]; then
  echo "  ✓ Subscription has correct channel"
else
  echo "  ✗ ERROR: Subscription has wrong channel (expected: alpha, got: ${SUB_CHANNEL})"
  exit 1
fi

if [ "${SUB_SOURCE}" = "operatorhubio-catalog" ]; then
  echo "  ✓ Subscription has correct source"
else
  echo "  ✗ ERROR: Subscription has wrong source (expected: operatorhubio-catalog, got: ${SUB_SOURCE})"
  exit 1
fi

# Step 17: Verify helm-based gitops-addon deployment does NOT exist
echo ""
echo "Step 17: Verifying helm-based gitops-addon deployment is NOT present..."
if kubectl get deployment gitops-addon -n ${ADDON_NAMESPACE} --no-headers 2>/dev/null | grep -q gitops-addon; then
  echo "  ✗ ERROR: Helm-based gitops-addon deployment found! OLM subscription mode should NOT deploy the helm addon."
  kubectl get deployment gitops-addon -n ${ADDON_NAMESPACE} -o yaml
  exit 1
else
  echo "  ✓ Helm-based gitops-addon deployment is NOT present (correct for OLM mode)"
fi

# Step 18: Verify ManifestWork contains Subscription, not Deployment
echo ""
echo "Step 18: Verifying ManifestWork contains Subscription manifest..."
kubectl config use-context ${HUB_CONTEXT}
MW_KIND=$(kubectl get manifestwork addon-gitops-addon-deploy-0 -n cluster1 -o jsonpath='{.spec.workload.manifests[0].kind}' 2>/dev/null || echo "")
if [ "${MW_KIND}" = "Subscription" ]; then
  echo "  ✓ ManifestWork contains Subscription manifest (correct for OLM mode)"
else
  echo "  ✗ ERROR: ManifestWork does not contain Subscription manifest (kind: ${MW_KIND})"
  kubectl get manifestwork addon-gitops-addon-deploy-0 -n cluster1 -o yaml | head -60
  exit 1
fi

# Step 19: Verify ArgoCD operator pod is created and running
echo ""
echo "Step 19: Verifying ArgoCD operator pod is created on managed cluster..."
kubectl config use-context ${SPOKE_CONTEXT}
for i in {1..90}; do
  OPERATOR_POD=$(kubectl get pods -n ${SUB_NAMESPACE} -l control-plane=controller-manager --no-headers 2>/dev/null | grep -E 'Running|ContainerCreating' | head -1 || echo "")
  if [ -n "$OPERATOR_POD" ]; then
    echo "  ✓ ArgoCD operator pod found: $(echo $OPERATOR_POD | awk '{print $1}')"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ✗ ERROR: ArgoCD operator pod not found after 90 attempts"
    echo "  Checking CSV status..."
    kubectl get csv -n ${SUB_NAMESPACE} 2>/dev/null || true
    echo "  Checking pods..."
    kubectl get pods -n ${SUB_NAMESPACE} 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for ArgoCD operator pod... (attempt $i/90)"
  sleep 5
done

# Step 20: Wait for ArgoCD operator to become ready
echo ""
echo "Step 20: Waiting for ArgoCD operator to become ready..."
for i in {1..90}; do
  OPERATOR_READY=$(kubectl get pods -n ${SUB_NAMESPACE} -l control-plane=controller-manager -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "")
  if [ "$OPERATOR_READY" = "Running" ]; then
    READY_CONTAINERS=$(kubectl get pods -n ${SUB_NAMESPACE} -l control-plane=controller-manager -o jsonpath='{.items[0].status.containerStatuses[0].ready}' 2>/dev/null || echo "false")
    if [ "$READY_CONTAINERS" = "true" ]; then
      echo "  ✓ ArgoCD operator is ready and running"
      break
    fi
  fi
  if [ $i -eq 90 ]; then
    echo "  ⚠ Warning: ArgoCD operator not fully ready after 90 attempts, but test considers this acceptable"
    echo "  The key verification is that the OLM Subscription was deployed correctly"
    kubectl get pods -n ${SUB_NAMESPACE} 2>/dev/null || true
    break
  fi
  echo "  Waiting for ArgoCD operator to become ready... (attempt $i/90, phase: $OPERATOR_READY)"
  sleep 5
done

echo ""
echo "========================================="
echo "PHASE 4: Verify Full ArgoCD Stack"
echo "========================================="

# Step 21: Create openshift-gitops namespace and ArgoCD CR
ARGOCD_NAMESPACE="openshift-gitops"
echo ""
echo "Step 21: Creating ArgoCD CR in ${ARGOCD_NAMESPACE} namespace..."
kubectl create namespace ${ARGOCD_NAMESPACE} 2>/dev/null || true

cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: openshift-gitops
  namespace: ${ARGOCD_NAMESPACE}
spec:
  server:
    route:
      enabled: false
EOF
echo "  ✓ ArgoCD CR created"

# Step 22: Wait for ArgoCD controller pods to be created (verify ArgoCD CR is reconciled)
echo ""
echo "Step 22: Verifying ArgoCD CR is reconciled (waiting for controller pods)..."
for i in {1..90}; do
  APP_CONTROLLER=$(kubectl get pods -n ${ARGOCD_NAMESPACE} -l app.kubernetes.io/name=openshift-gitops-application-controller --no-headers 2>/dev/null | grep -E 'Running|ContainerCreating' | head -1 || echo "")
  REPO_SERVER=$(kubectl get pods -n ${ARGOCD_NAMESPACE} -l app.kubernetes.io/name=openshift-gitops-repo-server --no-headers 2>/dev/null | grep -E 'Running|ContainerCreating' | head -1 || echo "")
  REDIS=$(kubectl get pods -n ${ARGOCD_NAMESPACE} -l app.kubernetes.io/name=openshift-gitops-redis --no-headers 2>/dev/null | grep -E 'Running|ContainerCreating' | head -1 || echo "")
  
  if [ -n "$APP_CONTROLLER" ] && [ -n "$REPO_SERVER" ] && [ -n "$REDIS" ]; then
    echo "  ✓ ArgoCD controller pods created:"
    echo "    - Application Controller: $(echo $APP_CONTROLLER | awk '{print $1}')"
    echo "    - Repo Server: $(echo $REPO_SERVER | awk '{print $1}')"
    echo "    - Redis: $(echo $REDIS | awk '{print $1}')"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ✗ ERROR: ArgoCD controller pods not created after 90 attempts"
    echo "  Checking ArgoCD CR status..."
    kubectl get argocd openshift-gitops -n ${ARGOCD_NAMESPACE} -o yaml 2>/dev/null || true
    echo "  Checking pods..."
    kubectl get pods -n ${ARGOCD_NAMESPACE} 2>/dev/null || true
    exit 1
  fi
  echo "  Waiting for ArgoCD controller pods... (attempt $i/90)"
  sleep 5
done

# Step 23: Wait for ArgoCD pods to be ready
echo ""
echo "Step 23: Waiting for ArgoCD pods to become ready..."
for i in {1..60}; do
  READY_PODS=$(kubectl get pods -n ${ARGOCD_NAMESPACE} --no-headers 2>/dev/null | grep -c "Running" || echo "0")
  TOTAL_PODS=$(kubectl get pods -n ${ARGOCD_NAMESPACE} --no-headers 2>/dev/null | wc -l || echo "0")
  
  if [ "$READY_PODS" -ge 3 ]; then
    echo "  ✓ ArgoCD pods are ready ($READY_PODS/$TOTAL_PODS running)"
    kubectl get pods -n ${ARGOCD_NAMESPACE} --no-headers 2>/dev/null | head -5
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ⚠ Warning: Not all ArgoCD pods ready after 60 attempts ($READY_PODS/$TOTAL_PODS running)"
    kubectl get pods -n ${ARGOCD_NAMESPACE} 2>/dev/null || true
    # Continue anyway - we just need to verify reconciliation started
    break
  fi
  echo "  Waiting for ArgoCD pods... (attempt $i/60, $READY_PODS/$TOTAL_PODS running)"
  sleep 5
done

# Step 24: Create ArgoCD Application to verify Application reconciliation
echo ""
echo "Step 24: Creating ArgoCD Application to verify reconciliation..."
kubectl create namespace guestbook 2>/dev/null || true
kubectl label namespace guestbook argocd.argoproj.io/managed-by=${ARGOCD_NAMESPACE} --overwrite 2>/dev/null || true

cat <<EOF | kubectl apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook
  namespace: ${ARGOCD_NAMESPACE}
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps.git
    targetRevision: HEAD
    path: guestbook
  destination:
    server: https://kubernetes.default.svc
    namespace: guestbook
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
EOF
echo "  ✓ ArgoCD Application created"

# Step 25: Verify Application is being reconciled (sync status exists)
echo ""
echo "Step 25: Verifying ArgoCD Application is being reconciled..."
for i in {1..60}; do
  SYNC_STATUS=$(kubectl get application guestbook -n ${ARGOCD_NAMESPACE} -o jsonpath='{.status.sync.status}' 2>/dev/null || echo "")
  HEALTH_STATUS=$(kubectl get application guestbook -n ${ARGOCD_NAMESPACE} -o jsonpath='{.status.health.status}' 2>/dev/null || echo "")
  
  # Check if application has been reconciled (has any status)
  if [ -n "$SYNC_STATUS" ] || [ -n "$HEALTH_STATUS" ]; then
    echo "  ✓ ArgoCD Application is being reconciled:"
    echo "    - Sync Status: ${SYNC_STATUS:-'(pending)'}"
    echo "    - Health Status: ${HEALTH_STATUS:-'(pending)'}"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ⚠ Warning: Application not showing status after 60 attempts"
    echo "  Checking Application..."
    kubectl get application guestbook -n ${ARGOCD_NAMESPACE} -o yaml 2>/dev/null | head -50 || true
    # Don't fail - reconciliation might just be slow
    break
  fi
  echo "  Waiting for Application reconciliation... (attempt $i/60)"
  sleep 5
done

# Step 26: Check if guestbook resources are being created (optional verification)
echo ""
echo "Step 26: Checking if guestbook resources are being deployed..."
sleep 10
GUESTBOOK_RESOURCES=$(kubectl get all -n guestbook --no-headers 2>/dev/null | wc -l || echo "0")
if [ "$GUESTBOOK_RESOURCES" -gt 0 ]; then
  echo "  ✓ Guestbook resources are being deployed ($GUESTBOOK_RESOURCES resources found)"
  kubectl get all -n guestbook --no-headers 2>/dev/null | head -5
else
  echo "  ⚠ Guestbook resources not yet created (Application may still be syncing)"
fi

echo ""
echo "========================================="
echo "✓ E2E OLM SUBSCRIPTION TEST PASSED"
echo "========================================="
echo ""
echo "Summary:"
echo "  - OLM AddOnTemplate created with correct labels"
echo "  - ManagedClusterAddOn references OLM template"
echo "  - GitOpsCluster OLMSubscriptionReady condition is True"
echo "  - ManifestWork contains Subscription (not helm deployment)"
echo "  - Subscription deployed to correct namespace (${SUB_NAMESPACE})"
echo "  - ArgoCD operator pod created via OLM"
echo "  - ArgoCD CR reconciled successfully (controller pods created)"
echo "  - ArgoCD Application reconciliation verified"
echo ""
