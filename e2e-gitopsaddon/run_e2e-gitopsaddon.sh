#!/bin/bash
###############################################################################
# Copyright Contributors to the Open Cluster Management project
###############################################################################

set -o nounset
set -o pipefail

# Setup metallb
kubectl config use-context kind-hub
kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/main/config/manifests/metallb-native.yaml
kubectl wait --namespace metallb-system \
  --for=condition=Ready pods \
  --selector=app=metallb \
  --timeout=120s
cat <<EOF | kubectl apply -f -
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: kind-address-pool
  namespace: metallb-system
spec:
  addresses:
  - 172.18.255.200-172.18.255.250
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: kind-l2-advertisement
  namespace: metallb-system
spec:
  ipAddressPools:
  - kind-address-pool
EOF

# Setup openshift-gitops operator and multicloud-integrations controllers and instances
kubectl config use-context kind-hub
kubectl create ns openshift-gitops
kubectl apply -f e2e-gitopsaddon/openshift-gitops/crds.yaml --server-side
kubectl apply -f e2e-gitopsaddon/openshift-gitops/operator.yaml
kubectl apply -f e2e-gitopsaddon/openshift-gitops/operator-instance.yaml
kubectl apply -f e2e/hub/dummy-application-svc-ca-secret.yaml
kubectl apply -f gitopsaddon/addonTemplates/clusterManagementAddon.yaml
kubectl apply -f gitopsaddon/addonTemplates/addonTemplates.yaml
kubectl apply -f deploy/crds/apps.open-cluster-management.io_gitopsclusters.yaml
kubectl apply -f hack/test/crds/0000_00_authentication.open-cluster-management.io_managedserviceaccounts.yaml
kubectl apply -f deploy/controller/
kubectl apply -f e2e-gitopsaddon/gitopscluster

kubectl apply -f e2e-gitopsaddon/argocdexportcrd.yaml --context kind-cluster1

sleep 30s
kubectl create ns openshift-gitops --context kind-cluster1
kubectl apply -f e2e-gitopsaddon/argocd.yaml --context kind-cluster1
sleep 30s
kubectl patch deployment openshift-gitops-redis -n openshift-gitops --type='json' -p='[
  {
    "op": "add",
    "path": "/spec/template/spec/securityContext",
    "value": {"runAsUser": 1000}
  }
]' --context kind-cluster1
sleep 60s
kubectl patch networkpolicy openshift-gitops-redis-network-policy -n openshift-gitops --context kind-hub --type='json' -p='[{"op": "add", "path": "/spec/ingress/-", "value": {"ports": [{"port": 6379, "protocol": "TCP"}], "from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "argocd-agent-principal"}}}]}}]'
kubectl patch networkpolicy openshift-gitops-redis-network-policy -n openshift-gitops --context kind-cluster1 --type='json' -p='[{"op": "add", "path": "/spec/ingress/-", "value": {"ports": [{"port": 6379, "protocol": "TCP"}], "from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "argocd-agent-agent"}}}]}}]'
kubectl rollout restart deployment openshift-gitops-agent-principal -n openshift-gitops --context kind-hub
kubectl rollout restart deployment argocd-agent-agent -n openshift-gitops --context kind-cluster1
sleep 30s
kubectl config use-context kind-hub
kubectl apply -f e2e-gitopsaddon/app.yaml
sleep 30s

# Validate hub
kubectl config use-context kind-hub
if ! kubectl wait -n openshift-gitops \
  --for=condition=Ready pod \
  --selector=app.kubernetes.io/name=openshift-gitops-agent-principal \
  --timeout=120s; then
    echo "principal did not become ready in time"
    exit 1
fi

# Validate cluster1
kubectl config use-context kind-cluster1
if ! kubectl wait -n openshift-gitops \
  --for=condition=Ready pod \
  --selector=app.kubernetes.io/name=argocd-agent-agent \
  --timeout=120s; then
    echo "agent did not become ready in time"
    exit 1
fi

# Validate agent uninstall
kubectl config use-context kind-hub
kubectl patch gitopscluster gitopscluster -n openshift-gitops --type='merge' -p '{"spec":{"gitopsAddon":{"overrideExistingConfigs":true}}}'
kubectl patch gitopscluster gitopscluster -n openshift-gitops --type='merge' -p '{"spec":{"gitopsAddon":{"argoCDAgent":{"uninstall":true}}}}'
sleep 60s
kubectl config use-context kind-cluster1
if kubectl -n openshift-gitops get deploy | grep -q agent; then
    echo "Uninstall failed: agent deployment still exists."
    exit 1
else
    echo "Uninstall successful: agent deployment not found."
fi


# Validate uninstall
kubectl config use-context kind-hub
kubectl patch gitopscluster gitopscluster -n openshift-gitops --type='merge' -p '{"spec":{"gitopsAddon":{"uninstall":true}}}'
sleep 60s
kubectl config use-context kind-cluster1
if [ -z "$(kubectl -n openshift-gitops get all --no-headers 2>/dev/null)" ]; then
  echo "No resources found in openshift-gitops namespace"
else
  echo "Resources still exist in openshift-gitops namespace"
  exit 1
fi
if [ -z "$(kubectl -n openshift-gitops-operator get all --no-headers 2>/dev/null)" ]; then
  echo "No resources found in openshift-gitops-operator namespace"
else
  echo "Resources still exist in openshift-gitops-operator namespace"
  exit 1
fi


echo 
echo 
echo 
echo "kind export kubeconfig --name hub"
echo "kind get kubeconfig --name hub  > /tmp/hub-io-kubeconfig"
echo "export KUBECONFIG=/tmp/hub-io-kubeconfig"
echo 
echo 
echo 
echo "kind export kubeconfig --name cluster1"
echo "kind get kubeconfig --name cluster1 > /tmp/cluster1-io-kubeconfig"
echo "export KUBECONFIG=/tmp/cluster1-io-kubeconfig"
echo 
echo 
