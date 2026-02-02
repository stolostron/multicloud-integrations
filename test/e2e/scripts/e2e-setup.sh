#!/bin/bash
# e2e-setup.sh - Setup environment for e2e tests
set -e

HUB_CONTEXT="kind-hub"
SPOKE_CONTEXT="kind-cluster1"
CONTROLLER_NAMESPACE="open-cluster-management"
GITOPS_NAMESPACE="openshift-gitops"
E2E_IMG="${E2E_IMG:-quay.io/stolostron/multicloud-integrations:latest}"
ENABLE_ARGOCD_AGENT="${ENABLE_ARGOCD_AGENT:-false}"
ARGOCD_AGENT_IMG="${ARGOCD_AGENT_IMG:-ghcr.io/argoproj-labs/argocd-agent/argocd-agent:latest}"

echo "========================================="
echo "E2E SETUP - Installing Components"
echo "========================================="

# Step 0: Preload ArgoCD Agent image to kind clusters (required for ghcr.io images)
echo ""
echo "Step 0: Preloading ArgoCD Agent image to kind clusters..."
docker pull ${ARGOCD_AGENT_IMG} || echo "Warning: Could not pull ${ARGOCD_AGENT_IMG}"
kind load docker-image ${ARGOCD_AGENT_IMG} --name hub || echo "Warning: Could not load image to hub"
kind load docker-image ${ARGOCD_AGENT_IMG} --name cluster1 || echo "Warning: Could not load image to cluster1"

# Step 1: Install MetalLB
echo ""
echo "Step 1: Installing MetalLB..."
./test/e2e/scripts/install_metallb.sh

# Step 2: Setup OCM environment
echo ""
echo "Step 2: Setting up OCM environment..."
# OCM_VERSION is passed through from Makefile via environment variable
./deploy/ocm/install.sh

# Step 2.5: Install governance-policy-framework
echo ""
echo "Step 2.5: Installing governance-policy-framework..."
kubectl config use-context ${HUB_CONTEXT}
clusteradm install hub-addon --names governance-policy-framework 2>&1 || {
  echo "  Warning: Failed to install governance-policy-framework hub addon"
}
echo "  Waiting for governance-policy-propagator to be ready..."
kubectl wait --for=condition=available --timeout=180s deployment/governance-policy-propagator -n open-cluster-management --context ${HUB_CONTEXT} 2>/dev/null || {
  echo "  Warning: governance-policy-propagator not ready, Policy-based ArgoCD management may not work"
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
for i in {1..60}; do
  POD_COUNT=$(kubectl --context ${SPOKE_CONTEXT} get pods -n open-cluster-management-agent-addon -l app=config-policy-controller --no-headers 2>/dev/null | grep Running | wc -l)
  if [ "$POD_COUNT" -gt 0 ]; then
    echo "  ✓ config-policy-controller addon running on cluster1"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  Warning: config-policy-controller addon not running on cluster1"
    break
  fi
  sleep 2
done
echo "  ✓ Policy framework installation completed"

# Step 3: Install ArgoCD on Hub
echo ""
echo "Step 3: Installing ArgoCD on Hub..."
kubectl config use-context ${HUB_CONTEXT}
kubectl create namespace ${GITOPS_NAMESPACE} || true
kubectl apply --server-side=true --force-conflicts -f test/e2e/fixtures/openshift-gitops/crds.yaml --context ${HUB_CONTEXT}
kubectl apply -f test/e2e/fixtures/openshift-gitops/operator.yaml --context ${HUB_CONTEXT}
echo "  Waiting for ArgoCD operator..."
kubectl wait --for=condition=available --timeout=180s deployment/argocd-operator-controller-manager -n argocd-operator-system --context ${HUB_CONTEXT} || {
  echo "✗ ERROR: ArgoCD operator not ready after 180s"
  exit 1
}

# Step 3.1: Create ArgoCD instance to trigger service/deployment creation
echo ""
echo "Step 3.1: Creating ArgoCD instance..."
kubectl create -f test/e2e/fixtures/openshift-gitops/operator-instance.yaml --context "${HUB_CONTEXT}" --save-config \
  || kubectl apply -f test/e2e/fixtures/openshift-gitops/operator-instance.yaml --context "${HUB_CONTEXT}"
echo "  ArgoCD instance created"

# Step 4: Install Controller and GitOpsCluster
echo ""
echo "Step 4: Installing Controller..."
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
# Set community ArgoCD operator image for e2e tests (instead of Red Hat registry)
ARGOCD_OPERATOR_IMAGE="${ARGOCD_OPERATOR_IMAGE:-quay.io/argoprojlabs/argocd-operator:latest}"
echo "  Setting GITOPS_OPERATOR_IMAGE to ${ARGOCD_OPERATOR_IMAGE} for e2e tests..."
kubectl set env deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} GITOPS_OPERATOR_IMAGE=${ARGOCD_OPERATOR_IMAGE}
# Set public ArgoCD agent image for e2e tests (registry.redhat.io requires auth)
ARGOCD_AGENT_IMAGE="${ARGOCD_AGENT_IMAGE:-ghcr.io/argoproj-labs/argocd-agent/argocd-agent:latest}"
echo "  Setting ARGOCD_AGENT_IMAGE_OVERRIDE to ${ARGOCD_AGENT_IMAGE} for e2e tests..."
kubectl set env deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} ARGOCD_AGENT_IMAGE_OVERRIDE=${ARGOCD_AGENT_IMAGE}
echo "  Waiting for controller to be ready..."
kubectl wait --for=condition=available --timeout=180s deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} || {
  echo "✗ ERROR: Controller not ready after 180s"
  exit 1
}

# Step 4.5: Apply ClusterManagementAddon and default AddOnTemplate
echo ""
echo "Step 4.5: Applying ClusterManagementAddon and default AddOnTemplate..."
kubectl apply -f gitopsaddon/addonTemplates/clusterManagementAddon.yaml --context ${HUB_CONTEXT}
kubectl apply -f gitopsaddon/addonTemplates/addonTemplates.yaml --context ${HUB_CONTEXT}

# Update AddOnTemplate images if using a custom E2E image
if [ "${E2E_IMG}" != "quay.io/stolostron/multicloud-integrations:latest" ]; then
  echo "  Patching AddOnTemplate images to use: ${E2E_IMG}"
  # Patch gitops-addon template
  kubectl get addontemplate gitops-addon -o json --context ${HUB_CONTEXT} | \
    sed "s|quay.io/stolostron/multicloud-integrations:latest|${E2E_IMG}|g" | \
    kubectl apply -f - --context ${HUB_CONTEXT} 2>/dev/null || true
  # Patch gitops-addon-olm template
  kubectl get addontemplate gitops-addon-olm -o json --context ${HUB_CONTEXT} | \
    sed "s|quay.io/stolostron/multicloud-integrations:latest|${E2E_IMG}|g" | \
    kubectl apply -f - --context ${HUB_CONTEXT} 2>/dev/null || true
  echo "  ✓ AddOnTemplate images updated"
fi

# Step 5: Create GitOpsCluster
echo ""
echo "Step 5: Creating GitOpsCluster (with argoCDAgent.enabled=${ENABLE_ARGOCD_AGENT})..."
kubectl apply -f test/e2e/fixtures/gitopscluster/managedclustersetbinding.yaml --context ${HUB_CONTEXT} || true
kubectl apply -f test/e2e/fixtures/gitopscluster/placement.yaml --context ${HUB_CONTEXT}

# Create GitOpsCluster dynamically
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
    gitOpsOperatorImage: "quay.io/argoprojlabs/argocd-operator:latest"
    argoCDAgent:
      enabled: ${ENABLE_ARGOCD_AGENT}
      propagateHubCA: true
      serverAddress: ""
      serverPort: ""
      mode: "managed"
EOF

echo "  Waiting for GitOpsCluster to be created..."
kubectl wait --for=jsonpath='{.metadata.name}'=gitopscluster --timeout=60s gitopscluster/gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} 2>/dev/null || echo "GitOpsCluster created"

if [ "${ENABLE_ARGOCD_AGENT}" == "true" ]; then
  echo "  ArgoCD Agent is enabled, waiting for controller to generate certificates..."
  
  # Wait for controller to generate all certificates (CA, principal TLS, resource proxy TLS)
  echo "  Waiting for argocd-agent-ca secret..."
  for i in {1..90}; do
    if kubectl get secret argocd-agent-ca -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
      echo "  ✓ Secret argocd-agent-ca created (attempt $i/90)"
      break
    fi
    if [ $i -eq 90 ]; then
      echo "✗ ERROR: Secret argocd-agent-ca not created after 180s"
      kubectl get gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o yaml
      kubectl logs deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} --tail=50
      exit 1
    fi
    sleep 2
  done

  echo "  Waiting for argocd-agent-principal-tls secret (may take up to 6 minutes for leader election + reconciliation)..."
  for i in {1..180}; do
    if kubectl get secret argocd-agent-principal-tls -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
      echo "  ✓ Secret argocd-agent-principal-tls created (attempt $i/180)"
      break
    fi
    if [ $i -eq 180 ]; then
      echo "✗ ERROR: Secret argocd-agent-principal-tls not created after 360s"
      kubectl logs deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} --tail=50
      exit 1
    fi
    sleep 2
  done

  echo "  Waiting for argocd-agent-resource-proxy-tls secret..."
  for i in {1..60}; do
    if kubectl get secret argocd-agent-resource-proxy-tls -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} &>/dev/null; then
      echo "  ✓ Secret argocd-agent-resource-proxy-tls created (attempt $i/60)"
      break
    fi
    if [ $i -eq 60 ]; then
      echo "✗ ERROR: Secret argocd-agent-resource-proxy-tls not created after 120s"
      kubectl logs deployment/multicloud-integrations-gitops -n ${CONTROLLER_NAMESPACE} --context ${HUB_CONTEXT} --tail=50
      exit 1
    fi
    sleep 2
  done

  echo "  All certificates generated by controller!"
  
  # Verify the principal TLS cert has the LoadBalancer IP in SANs
  echo "  Verifying principal TLS certificate SANs..."
  LB_IP=$(kubectl get svc openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
  if [ -n "$LB_IP" ]; then
    CERT_SANS=$(kubectl get secret argocd-agent-principal-tls -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.data.tls\.crt}' 2>/dev/null | base64 -d | openssl x509 -noout -text 2>/dev/null | grep -A1 "Subject Alternative Name" || echo "")
    if echo "$CERT_SANS" | grep -q "$LB_IP"; then
      echo "  ✓ Principal TLS certificate includes LoadBalancer IP $LB_IP in SANs"
    else
      echo "  Warning: Principal TLS certificate may not include LoadBalancer IP $LB_IP"
      echo "  Certificate SANs: $CERT_SANS"
    fi
  fi

  echo "  Restarting ArgoCD principal pods to pick up controller-generated secrets..."
  kubectl delete pod -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} || true
  echo "  Waiting for principal pods to be recreated and ready..."
  for i in {1..60}; do
    POD_COUNT=$(kubectl get pods -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --no-headers 2>/dev/null | grep -v Terminating | wc -l)
    if [ "$POD_COUNT" -gt 0 ]; then
      echo "  ✓ Found $POD_COUNT principal pod(s) running"
      if kubectl wait --for=condition=Ready --timeout=10s pod -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} 2>/dev/null; then
        echo "  ✓ Principal pods are ready"
        break
      fi
    fi
    if [ $i -eq 60 ]; then
      echo "✗ ERROR: Principal pods not ready after restart"
      kubectl get pods -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT}
      kubectl logs -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --tail=30
      exit 1
    fi
    sleep 2
  done
else
  echo "  ArgoCD Agent is disabled, skipping secret creation and principal pod checks"
  echo "  ✓ GitOpsCluster created successfully (agent disabled)"
fi

# Step 6: Wait for gitops-addon to install ArgoCD operator on managed cluster
echo ""
echo "Step 6: Waiting for gitops-addon to install ArgoCD operator on managed cluster..."
kubectl apply -f test/e2e/fixtures/argocdexportcrd.yaml --context ${SPOKE_CONTEXT}
echo "  Creating openshift-gitops namespace on managed cluster..."
kubectl create namespace ${GITOPS_NAMESPACE} --context ${SPOKE_CONTEXT} || true

echo "  Waiting for ArgoCD operator deployment to be ready..."
for i in {1..90}; do
  if kubectl --context ${SPOKE_CONTEXT} get deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator &>/dev/null; then
    echo "  ✓ ArgoCD operator deployment found (attempt $i/90)"
    kubectl --context ${SPOKE_CONTEXT} wait --for=condition=available --timeout=300s deployment/openshift-gitops-operator-controller-manager -n openshift-gitops-operator || {
      echo "  ✗ ERROR: ArgoCD operator deployment not available after 300s"
      echo "  Checking deployment status..."
      kubectl --context ${SPOKE_CONTEXT} describe deployment openshift-gitops-operator-controller-manager -n openshift-gitops-operator || true
      kubectl --context ${SPOKE_CONTEXT} get pods -n openshift-gitops-operator || true
      exit 1
    }
    echo "  ✓ ArgoCD operator deployment is ready"
    break
  fi
  if [ $i -eq 90 ]; then
    echo "  ✗ ERROR: ArgoCD operator deployment not found after 90 attempts"
    echo "  Checking addon pod logs..."
    kubectl --context ${SPOKE_CONTEXT} logs -n open-cluster-management-agent-addon -l app=gitops-addon --tail=30
    exit 1
  fi
  echo "  Waiting for ArgoCD operator deployment... (attempt $i/90)"
  sleep 5
done

echo "  Waiting for ArgoCD CRDs to be ready..."
for i in {1..30}; do
  if kubectl --context ${SPOKE_CONTEXT} get crd argocds.argoproj.io &>/dev/null; then
    echo "  ✓ ArgoCD CRD is installed"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "  ✗ ERROR: ArgoCD CRD not found after 30 attempts"
    exit 1
  fi
  echo "  Waiting for ArgoCD CRD... (attempt $i/30)"
  sleep 2
done

echo "  Waiting for ArgoCD CR on managed cluster..."
echo "  (ArgoCD CR is created by gitops-addon directly, with Policy framework as optional enhancement)"

# Wait for ArgoCD CR to be created on managed cluster (by Policy)
for i in {1..60}; do
  if kubectl --context ${SPOKE_CONTEXT} get argocd acm-openshift-gitops -n ${GITOPS_NAMESPACE} &>/dev/null; then
    echo "  ✓ ArgoCD CR 'acm-openshift-gitops' found (attempt $i/60)"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: ArgoCD CR not found after 60 attempts"
    echo "  Checking addon pod logs..."
    kubectl --context ${SPOKE_CONTEXT} logs -n open-cluster-management-agent-addon -l app=gitops-addon --tail=50
    exit 1
  fi
  echo "  Waiting for ArgoCD CR... (attempt $i/60)"
  sleep 5
done

# Step 7: Wait for ArgoCD components to be ready
echo ""
echo "Step 7: Waiting for ArgoCD application controller to be ready..."
for i in {1..60}; do
  if kubectl --context ${SPOKE_CONTEXT} get statefulset acm-openshift-gitops-application-controller -n ${GITOPS_NAMESPACE} &>/dev/null; then
    echo "  ✓ ArgoCD application controller found"
    kubectl --context ${SPOKE_CONTEXT} wait --for=jsonpath='{.status.readyReplicas}'=1 --timeout=120s statefulset/acm-openshift-gitops-application-controller -n ${GITOPS_NAMESPACE} || {
      echo "  Warning: Application controller not ready yet"
    }
    break
  fi
  if [ $i -eq 60 ]; then
    echo "  ✗ ERROR: ArgoCD application controller not found"
    exit 1
  fi
  echo "  Waiting for application controller... (attempt $i/60)"
  sleep 5
done

# Step 8: Verify agent setup
echo ""
echo "Step 8: Verifying agent setup..."
if [ "${ENABLE_ARGOCD_AGENT}" == "true" ]; then
  # Wait for principal pods to be ready
  echo "  Waiting for principal pods to be ready..."
  for i in {1..60}; do
    POD_COUNT=$(kubectl get pods -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --no-headers 2>/dev/null | grep -v Terminating | grep Running | wc -l)
    if [ "$POD_COUNT" -gt 0 ]; then
      if kubectl wait --for=condition=Ready --timeout=10s pod -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} 2>/dev/null; then
        echo "  ✓ Principal pods are ready"
        break
      fi
    fi
    if [ $i -eq 60 ]; then
      echo "✗ ERROR: Principal pods not ready"
      kubectl get pods -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT}
      kubectl logs -l app.kubernetes.io/name=openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --tail=30
      exit 1
    fi
    sleep 2
  done
  
  # Update GitOpsCluster with the LoadBalancer IP for agents
  echo ""
  echo "Step 8.1: Retrieving LoadBalancer IP and updating GitOpsCluster..."
  LB_IP=$(kubectl get svc openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
  if [ -z "$LB_IP" ]; then
    echo "  Warning: Could not retrieve LoadBalancer IP, using ClusterIP..."
    LB_IP=$(kubectl get svc openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} -o jsonpath='{.spec.clusterIP}' 2>/dev/null || echo "")
  fi
  echo "  Updating GitOpsCluster with serverAddress=${LB_IP}..."
  kubectl patch gitopscluster gitopscluster -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} --type=merge \
    -p '{"spec":{"gitopsAddon":{"argoCDAgent":{"serverAddress":"'"${LB_IP}"'","serverPort":"443"}}}}'
  echo "  ✓ GitOpsCluster updated with serverAddress"

  # Wait for agent on managed cluster
  echo ""
  echo "Step 8.2: Waiting for agent deployment on managed cluster..."
  sleep 10
  for i in {1..60}; do
    if kubectl --context ${SPOKE_CONTEXT} get deployment acm-openshift-gitops-agent-agent -n ${GITOPS_NAMESPACE} &>/dev/null; then
      echo "  ✓ Agent deployment found on managed cluster"
      kubectl --context ${SPOKE_CONTEXT} rollout restart deployment acm-openshift-gitops-agent-agent -n ${GITOPS_NAMESPACE} 2>/dev/null || true
      sleep 5
      break
    fi
    if [ $i -eq 60 ]; then
      echo "  Warning: Agent deployment not found on managed cluster (may be expected)"
      break
    fi
    echo "  Waiting for agent deployment... (attempt $i/60)"
    sleep 3
  done
else
  echo "  ArgoCD Agent is disabled, skipping agent setup"
  kubectl rollout restart deployment openshift-gitops-agent-principal -n ${GITOPS_NAMESPACE} --context ${HUB_CONTEXT} 2>/dev/null || echo "  Warning: Could not restart principal (may not exist)"
  sleep 10
fi

# Step 9: Deploy test application
echo ""
echo "Step 9: Deploying test application..."

# Create and label guestbook namespace on managed cluster for ArgoCD permission
# The namespace must be labeled with the ArgoCD instance name to allow deployments
echo "  Creating and labeling guestbook namespace on managed cluster..."
kubectl create namespace guestbook --context ${SPOKE_CONTEXT} 2>/dev/null || true
kubectl label namespace guestbook argocd.argoproj.io/managed-by=acm-openshift-gitops --overwrite --context ${SPOKE_CONTEXT}
echo "  ✓ Guestbook namespace labeled for ArgoCD permission"

# Create default AppProject in cluster1 namespace on hub for ArgoCD agent mode
echo "  Creating default AppProject in cluster1 namespace on hub..."
cat <<EOF | kubectl apply -f - --context ${HUB_CONTEXT}
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: cluster1
spec:
  clusterResourceWhitelist:
  - group: '*'
    kind: '*'
  destinations:
  - namespace: '*'
    server: '*'
  sourceRepos:
  - '*'
EOF
echo "  ✓ Default AppProject created in cluster1 namespace on hub"

# Also create default AppProject in openshift-gitops namespace on managed cluster
# This is required because the ArgoCD application controller on the managed cluster
# needs the AppProject to exist locally before it can reconcile the application
echo "  Creating default AppProject in openshift-gitops namespace on managed cluster..."
cat <<EOF | kubectl apply -f - --context ${SPOKE_CONTEXT}
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: ${GITOPS_NAMESPACE}
spec:
  clusterResourceWhitelist:
  - group: '*'
    kind: '*'
  destinations:
  - namespace: '*'
    server: '*'
  sourceRepos:
  - '*'
EOF
echo "  ✓ Default AppProject created in openshift-gitops namespace on managed cluster"

kubectl apply -f test/e2e/fixtures/app.yaml --context ${HUB_CONTEXT}
sleep 30s

echo ""
echo "========================================="
echo "✓ E2E SETUP COMPLETE"
echo "========================================="
