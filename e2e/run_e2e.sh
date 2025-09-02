#!/bin/bash
###############################################################################
# Copyright Contributors to the Open Cluster Management project
###############################################################################

set -o nounset
set -o pipefail

echo "SETUP install multicloud-integrations"
kubectl config use-context kind-hub
kubectl create namespace argocd
kubectl apply -f deploy/crds/
kubectl apply -f hack/test/crds/0000_00_authentication.open-cluster-management.io_managedserviceaccounts.yaml

# Create dummy source secret before deploying controller
echo "Creating dummy source secret for CA secret testing..."
kubectl apply -f e2e/hub/dummy-application-svc-ca-secret.yaml

# Create dummy agent principal service for certificate testing
echo "Creating dummy agent principal service for certificate testing..."
kubectl apply -f e2e/hub/agent-principal.yaml

# Manually add the status to the service since kubectl apply doesn't preserve it
echo "Adding LoadBalancer status to agent principal service..."
kubectl patch service openshift-gitops-agent-principal -n argocd --subresource=status --patch '{"status":{"loadBalancer":{"ingress":[{"hostname":"foobar.us-west-1.elb.amazonaws.com"}]}}}'

kubectl apply -f deploy/controller/

kubectl -n open-cluster-management rollout status deployment multicloud-integrations-gitops --timeout=120s
kubectl -n open-cluster-management rollout status deployment multicloud-integrations --timeout=120s

echo "TEST Propgation controller startup (expecting error)"
POD_NAME=$(kubectl -n open-cluster-management get deploy multicloud-integrations -o yaml  | grep ReplicaSet | grep successful | cut -d'"' -f2)
POD_NAME=$(kubectl -n open-cluster-management get pod | grep $POD_NAME | cut -d' ' -f1)
if kubectl -n open-cluster-management logs $POD_NAME argocd-pull-integration-controller-manager | grep "failed to find CRD applications.argoproj.io"; then
    echo "Propgation controller failed to startup"
else
    echo "Propgation controller startup successfully"
    exit 1
fi

### Setup
echo "SETUP install Argo CD to Managed cluster"
kubectl config use-context kind-cluster1
kubectl create namespace argocd
kubectl apply -n argocd --force -f hack/test/e2e/argo-cd-install.yaml
kubectl -n argocd scale deployment/argocd-applicationset-controller --replicas 0
kubectl -n argocd scale deployment/argocd-server --replicas 0
kubectl -n argocd scale deployment/argocd-dex-server --replicas 0
kubectl -n argocd scale deployment/argocd-notifications-controller --replicas 0

echo "SETUP install Argo CD to Hub cluster"
kubectl config use-context kind-hub
kubectl apply -n argocd --force -f hack/test/e2e/argo-cd-install.yaml 
kubectl -n argocd scale deployment/argocd-dex-server --replicas 0
kubectl -n argocd scale deployment/argocd-repo-server --replicas 0
kubectl -n argocd scale deployment/argocd-server --replicas 0
kubectl -n argocd scale deployment/argocd-redis --replicas 0
kubectl -n argocd scale deployment/argocd-notifications-controller --replicas 0
kubectl -n argocd scale statefulset/argocd-application-controller --replicas 0

# enable progressive sync
kubectl -n argocd patch configmap argocd-cmd-params-cm --type merge -p '{"data":{"applicationsetcontroller.enable.progressive.syncs":"true"}}'
kubectl -n argocd rollout restart deployment argocd-applicationset-controller
kubectl -n argocd rollout status deployment argocd-applicationset-controller --timeout=60s

echo "TEST Propgation controller startup"
if kubectl -n open-cluster-management logs $POD_NAME argocd-pull-integration-controller-manager | grep "Starting Controller" | grep "Application"; then
    echo "Propgation controller startup successfully"
else
    echo "Propgation controller failed to startup"
    exit 1
fi

echo "SETUP print managed cluster setup"
kubectl config use-context kind-cluster1
kubectl -n argocd get deploy
kubectl -n argocd get statefulset

echo "SETUP print hub setup"
kubectl config use-context kind-hub
kubectl -n argocd get deploy
kubectl -n argocd get statefulset
kubectl -n open-cluster-management get deploy

### GitOpsCluster Auto-Provisioning Tests
echo "TEST Namespace and RBAC Auto-Provisioning"
kubectl config use-context kind-hub

# Test 1: Verify openshift-gitops namespace is created
if kubectl get namespace openshift-gitops; then
    echo "Namespace Auto-Provisioning: openshift-gitops namespace exists"
else
    echo "Namespace Auto-Provisioning FAILED: openshift-gitops namespace not created"
    exit 1
fi

# Test 2: Verify addon-manager-controller-role is created
if kubectl -n openshift-gitops get role addon-manager-controller-role; then
    echo "RBAC Auto-Provisioning: addon-manager-controller-role exists"
else
    echo "RBAC Auto-Provisioning FAILED: addon-manager-controller-role not created"
    exit 1
fi

# Test 3: Verify addon-manager-controller-rolebinding is created
if kubectl -n openshift-gitops get rolebinding addon-manager-controller-rolebinding; then
    echo "RBAC Auto-Provisioning: addon-manager-controller-rolebinding exists"
else
    echo "RBAC Auto-Provisioning FAILED: addon-manager-controller-rolebinding not created"
    exit 1
fi

# Test 4: Verify role has correct permissions for secrets
if kubectl -n openshift-gitops get role addon-manager-controller-role -o yaml | grep -A3 "resources:" | grep "secrets"; then
    echo "RBAC Permissions: role has secrets permissions"
else
    echo "RBAC Permissions FAILED: role missing secrets permissions"
    exit 1
fi

# Test 5: Verify role has correct verbs (get, list, watch)
if kubectl -n openshift-gitops get role addon-manager-controller-role -o yaml | grep -A5 "verbs:" | grep -E "(get|list|watch)"; then
    echo "RBAC Permissions: role has correct verbs"
else
    echo "RBAC Permissions FAILED: role missing required verbs"
    exit 1
fi

# Test 6: Verify RBAC resources have correct labels
if kubectl -n openshift-gitops get role addon-manager-controller-role -o yaml | grep "apps.open-cluster-management.io/gitopsaddon.*true"; then
    echo "RBAC Labels: role has correct gitopsaddon label"
else
    echo "RBAC Labels FAILED: role missing gitopsaddon label"
    exit 1
fi

if kubectl -n openshift-gitops get rolebinding addon-manager-controller-rolebinding -o yaml | grep "apps.open-cluster-management.io/gitopsaddon.*true"; then
    echo "RBAC Labels: rolebinding has correct gitopsaddon label"
else
    echo "RBAC Labels FAILED: rolebinding missing gitopsaddon label"
    exit 1
fi

# Test 7: Verify rolebinding has correct subject (addon-manager-controller-sa in open-cluster-management-hub)
if kubectl -n openshift-gitops get rolebinding addon-manager-controller-rolebinding -o yaml | grep -A3 "subjects:" | grep "addon-manager-controller-sa"; then
    echo "RBAC Subject: rolebinding has correct service account subject"
else
    echo "RBAC Subject FAILED: rolebinding missing correct service account subject"
    exit 1
fi

if kubectl -n openshift-gitops get rolebinding addon-manager-controller-rolebinding -o yaml | grep -A5 "subjects:" | grep "open-cluster-management-hub"; then
    echo "RBAC Subject: rolebinding has correct namespace for subject"
else
    echo "RBAC Subject FAILED: rolebinding missing correct namespace for subject"
    exit 1
fi

# Test 8: Verify rolebinding references the correct role
if kubectl -n openshift-gitops get rolebinding addon-manager-controller-rolebinding -o yaml | grep -A3 "roleRef:" | grep "addon-manager-controller-role"; then
    echo "RBAC RoleRef: rolebinding references correct role"
else
    echo "RBAC RoleRef FAILED: rolebinding does not reference correct role"
    exit 1
fi

### CA Secret Auto-Provisioning Tests
echo "TEST CA Secret Auto-Provisioning"

# Verify argocd-agent-ca secret is created in openshift-gitops namespace
if kubectl -n openshift-gitops get secret argocd-agent-ca; then
    echo "CA Secret Copy: argocd-agent-ca secret created in openshift-gitops namespace"
else
    echo "CA Secret Copy FAILED: argocd-agent-ca secret not created"
    kubectl -n open-cluster-management logs deployment/multicloud-integrations-gitops
    exit 1
fi

### GitOpsCluster
echo "TEST GitOpsCluster"
# Add test label to cluster1 to test that labels are propagated
kubectl label managedcluster cluster1 test-label=test-value
kubectl apply -f examples/argocd/
sleep 10s
if kubectl -n argocd get gitopsclusters argo-ocm-importer -o yaml | grep successful; then
    echo "GitOpsCluster: status successful"
else
    echo "GitOpsCluster FAILED: status not successful"

    kubectl -n argocd get gitopsclusters argo-ocm-importer -o yaml

    kubectl logs -n open-cluster-management deployment/multicloud-integrations-gitops
    
    exit 1
fi
if [[ "$(kubectl -n argocd get secret -l=test-label=test-value -o jsonpath='{.items[0].metadata.name}')" == "cluster1-application-manager-cluster-secret" ]]; then
    echo "GitOpsCluster: cluster1-application-manager-cluster-secret created"
else
    echo "GitOpsCluster FAILED: cluster1-application-manager-cluster-secret not created"
    exit 1
fi
# Add another test label to cluster1 to test that updated labels are propagated
kubectl label managedcluster cluster1 test-label-2=test-value-2
sleep 20s
if kubectl -n argocd get gitopsclusters argo-ocm-importer -o yaml | grep successful; then
    echo "GitOpsCluster: status successful"
else
    echo "GitOpsCluster FAILED: status not successful"
    exit 1
fi
if [[ "$(kubectl -n argocd get secret -l=test-label-2=test-value-2 -o jsonpath='{.items[0].metadata.name}')" == "cluster1-application-manager-cluster-secret" ]]; then
    echo "GitOpsCluster: cluster1-application-manager-cluster-secret updated"
else
    echo "GitOpsCluster FAILED: cluster1-application-manager-cluster-secret not updated"
    exit 1
fi
# Test GitOpsCluster error
kubectl -n argocd patch gitopscluster argo-ocm-importer --type merge -p '{"spec":{"createBlankClusterSecrets":false}}'
sleep 20s
if kubectl -n argocd get gitopsclusters argo-ocm-importer -o yaml | grep "phase: failed"; then
    echo "GitOpsCluster: status failed"
else
    echo "GitOpsCluster FAILED: status not failed"
    exit 1
fi
if kubectl -n argocd get gitopsclusters argo-ocm-importer -o yaml | grep "not found"; then
    echo "GitOpsCluster: message not found"
else
    echo "GitOpsCluster FAILED: message not not found"
    exit 1
fi
kubectl -n argocd patch gitopscluster argo-ocm-importer --type merge -p '{"spec":{"createBlankClusterSecrets":true}}'
sleep 20s

### Propagation
echo "TEST Propagation"
kubectl config use-context kind-cluster1
kubectl apply -f e2e/managed/
kubectl config use-context kind-hub
kubectl apply -f e2e/hub/
kubectl apply -f e2e/hub_app/
sleep 120s
if kubectl -n argocd get application cluster1-guestbook-app; then
    echo "Propagation: hub application cluster1-guestbook-app created"
else
    echo "Propagation FAILED: hub application cluster1-guestbook-app not created"
    kubectl -n argocd get applicationset guestbook-app-set -o yaml
    kubectl -n argocd get placementdecision guestbook-app-placement-decision-1 -o yaml
    kubectl -n argocd logs $(kubectl -n argocd get pods -l app.kubernetes.io/name=argocd-applicationset-controller -o jsonpath="{.items[0].metadata.name}")
    exit 1
fi
if kubectl -n cluster1 get manifestwork | grep cluster1-guestbook-app; then
    echo "Propagation: manifestwork created"
else
    echo "Propagation FAILED: manifestwork not created"
    exit 1
fi
if kubectl -n cluster1 get manifestwork -o yaml | grep ed58e4a1479ef2d7fb1a60bc2b7300100f262779; then
    echo "Propagation: manifestwork contains appSet hash"
else
    echo "Propagation FAILED: manifestwork does not contain appSet hash"
    exit 1
fi
kubectl config use-context kind-cluster1
if kubectl -n argocd get app cluster1-guestbook-app; then
    echo "Propagation: managed cluster application cluster1-guestbook-app created"
else
    echo "Propagation FAILED: managed application cluster1-guestbook-app not created"
    kubectl -n argocd get app cluster1-guestbook-app -o yaml
    exit 1
fi
if kubectl -n argocd get app cluster1-guestbook-app -o yaml | grep RollingSync; then
    echo "Propagation: application contains operation RollingSync"
else
    echo "Propagation FAILED: application does not contain operation RollingSync"
    exit 1
fi
if kubectl get namespace guestbook; then
    echo "Propagation: guestbook namespace created"
else
    echo "Propagation FAILED: guestbook namespace not created"
    exit 1
fi
if kubectl -n guestbook get deploy guestbook-ui; then
    echo "Propagation: guestbook-ui deploy created"
else
    echo "Propagation FAILED: guestbook-ui deploy not created"
    exit 1
fi
kubectl config use-context kind-hub
if [[ -n $(kubectl -n argocd get app cluster1-guestbook-app -o jsonpath='{.status.operationState.phase}') ]]; then
    echo "Propagation: hub cluster application cluster1-guestbook-app phase is not empty"
else
    echo "Propagation FAILED: hub cluster application cluster1-guestbook-app phase is empty"
    kubectl -n argocd get app cluster1-guestbook-app -o yaml
    exit 1
fi
if [[ -n $(kubectl -n argocd get app cluster1-guestbook-app -o jsonpath='{.status.sync.revision}') ]]; then
    echo "Propagation: hub cluster application cluster1-guestbook-app revision not empty"
else
    echo "Propagation FAILED: hub cluster application cluster1-guestbook-app revision is empty"
    kubectl -n argocd get app cluster1-guestbook-app -o yaml
    exit 1
fi

echo "TEST Refresh Annotation"
kubectl apply -f e2e/hub_app/apponly.yaml
sleep 10s
if kubectl -n argocd get application cluster1-guestbook-app-only; then
    echo "Refresh Annotation: standalone application cluster1-guestbook-app-only created"
else
    echo "Refresh Annotation FAILED: standalone application cluster1-guestbook-app-only not created"
    exit 1
fi
if kubectl -n argocd annotate app cluster1-guestbook-app-only argocd.argoproj.io/refresh=normal; then
    echo "Refresh Annotation: annotation added successfully"
else
    echo "Refresh Annotation FAILED: annotation command failed"
    exit 1
fi
sleep 60s
if kubectl -n argocd get app cluster1-guestbook-app-only -o yaml | grep "argocd.argoproj.io/refresh"; then
    echo "Refresh Annotation FAILED: annotation still exists after 60 seconds"
    kubectl -n argocd get app cluster1-guestbook-app-only -o yaml
    kubectl -n open-cluster-management logs $POD_NAME argocd-pull-integration-controller-manager
    exit 1
else
    echo "Refresh Annotation: annotation deleted successfully after 60 seconds"
fi

### ArgoCD Agent Certificate Tests
echo "TEST ArgoCD Agent Certificates"
kubectl config use-context kind-hub

# Test 1: Verify certificates are NOT created when ArgoCD agent is disabled (default state)
if kubectl -n argocd get secret argocd-agent-principal-tls 2>/dev/null; then
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-principal-tls should not exist when agent disabled"
    exit 1
else
    echo "ArgoCD Agent Certificates: argocd-agent-principal-tls correctly does not exist when agent disabled"
fi

if kubectl -n argocd get secret argocd-agent-resource-proxy-tls 2>/dev/null; then
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-resource-proxy-tls should not exist when agent disabled"
    exit 1
else
    echo "ArgoCD Agent Certificates: argocd-agent-resource-proxy-tls correctly does not exist when agent disabled"
fi

# Setup: Copy argocd-agent-ca secret to argocd namespace (required for agent)
# The secret was verified to exist in openshift-gitops namespace in the CA Secret Auto-Provisioning test above
echo "Copying argocd-agent-ca secret from openshift-gitops to argocd namespace..."
if kubectl -n openshift-gitops get secret argocd-agent-ca; then
    kubectl -n openshift-gitops get secret argocd-agent-ca -o yaml | \
    sed 's/namespace: openshift-gitops/namespace: argocd/' | \
    kubectl apply -f -
    echo "ArgoCD Agent Certificates: argocd-agent-ca secret copied to argocd namespace"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-ca secret not found in openshift-gitops namespace"
    exit 1
fi

# Test 2: Enable ArgoCD agent and verify certificates are created
echo "Enabling ArgoCD agent in GitOpsCluster..."
kubectl -n argocd patch gitopscluster argo-ocm-importer --type merge -p '{"spec":{"argoCDAgent":{"enabled":true}}}'
sleep 30s

# Verify GitOpsCluster status is still successful after enabling agent
if kubectl -n argocd get gitopsclusters argo-ocm-importer -o yaml | grep successful; then
    echo "ArgoCD Agent Certificates: GitOpsCluster status successful with agent enabled"
else
    echo "ArgoCD Agent Certificates FAILED: GitOpsCluster status not successful with agent enabled"
    kubectl -n argocd get gitopsclusters argo-ocm-importer -o yaml
    kubectl logs -n open-cluster-management deployment/multicloud-integrations-gitops
    exit 1
fi

# Test 3: Verify principal TLS certificate is created
if kubectl -n argocd get secret argocd-agent-principal-tls; then
    echo "ArgoCD Agent Certificates: argocd-agent-principal-tls created when agent enabled"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-principal-tls not created when agent enabled"
    kubectl logs -n open-cluster-management deployment/multicloud-integrations-gitops
    exit 1
fi

# Test 4: Verify resource proxy TLS certificate is created  
if kubectl -n argocd get secret argocd-agent-resource-proxy-tls; then
    echo "ArgoCD Agent Certificates: argocd-agent-resource-proxy-tls created when agent enabled"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-resource-proxy-tls not created when agent enabled"
    kubectl logs -n open-cluster-management deployment/multicloud-integrations-gitops
    exit 1
fi

# Test 5: Verify principal TLS secret has correct type and keys
if kubectl -n argocd get secret argocd-agent-principal-tls -o yaml | grep "type: kubernetes.io/tls"; then
    echo "ArgoCD Agent Certificates: argocd-agent-principal-tls has correct TLS type"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-principal-tls missing TLS type"
    exit 1
fi

if kubectl -n argocd get secret argocd-agent-principal-tls -o yaml | grep "tls.crt:"; then
    echo "ArgoCD Agent Certificates: argocd-agent-principal-tls contains tls.crt"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-principal-tls missing tls.crt"
    exit 1
fi

if kubectl -n argocd get secret argocd-agent-principal-tls -o yaml | grep "tls.key:"; then
    echo "ArgoCD Agent Certificates: argocd-agent-principal-tls contains tls.key"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-principal-tls missing tls.key"
    exit 1
fi

# Check that tls.crt is not empty
if [[ -n $(kubectl -n argocd get secret argocd-agent-principal-tls -o jsonpath='{.data.tls\.crt}') ]]; then
    echo "ArgoCD Agent Certificates: argocd-agent-principal-tls tls.crt is not empty"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-principal-tls tls.crt is empty"
    exit 1
fi

# Check that tls.key is not empty
if [[ -n $(kubectl -n argocd get secret argocd-agent-principal-tls -o jsonpath='{.data.tls\.key}') ]]; then
    echo "ArgoCD Agent Certificates: argocd-agent-principal-tls tls.key is not empty"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-principal-tls tls.key is empty"
    exit 1
fi

# Test 6: Verify resource proxy TLS secret has correct type and keys
if kubectl -n argocd get secret argocd-agent-resource-proxy-tls -o yaml | grep "type: kubernetes.io/tls"; then
    echo "ArgoCD Agent Certificates: argocd-agent-resource-proxy-tls has correct TLS type"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-resource-proxy-tls missing TLS type"
    exit 1
fi

if kubectl -n argocd get secret argocd-agent-resource-proxy-tls -o yaml | grep "tls.crt:"; then
    echo "ArgoCD Agent Certificates: argocd-agent-resource-proxy-tls contains tls.crt"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-resource-proxy-tls missing tls.crt"
    exit 1
fi

if kubectl -n argocd get secret argocd-agent-resource-proxy-tls -o yaml | grep "tls.key:"; then
    echo "ArgoCD Agent Certificates: argocd-agent-resource-proxy-tls contains tls.key"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-resource-proxy-tls missing tls.key"
    exit 1
fi

# Check that tls.crt is not empty
if [[ -n $(kubectl -n argocd get secret argocd-agent-resource-proxy-tls -o jsonpath='{.data.tls\.crt}') ]]; then
    echo "ArgoCD Agent Certificates: argocd-agent-resource-proxy-tls tls.crt is not empty"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-resource-proxy-tls tls.crt is empty"
    exit 1
fi

# Check that tls.key is not empty
if [[ -n $(kubectl -n argocd get secret argocd-agent-resource-proxy-tls -o jsonpath='{.data.tls\.key}') ]]; then
    echo "ArgoCD Agent Certificates: argocd-agent-resource-proxy-tls tls.key is not empty"
else
    echo "ArgoCD Agent Certificates FAILED: argocd-agent-resource-proxy-tls tls.key is empty"
    exit 1
fi

echo "All ArgoCD Agent Certificate tests passed successfully!"
