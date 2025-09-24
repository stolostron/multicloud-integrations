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

sleep 90s
kubectl patch networkpolicy openshift-gitops-redis-network-policy -n openshift-gitops --context kind-hub --type='json' -p='[{"op": "add", "path": "/spec/ingress/-", "value": {"ports": [{"port": 6379, "protocol": "TCP"}], "from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "argocd-agent-principal"}}}]}}]'
kubectl patch networkpolicy acm-openshift-gitops-redis-network-policy -n openshift-gitops --context kind-cluster1 --type='json' -p='[{"op": "add", "path": "/spec/ingress/-", "value": {"ports": [{"port": 6379, "protocol": "TCP"}], "from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "argocd-agent-agent"}}}]}}]'
kubectl rollout restart deployment openshift-gitops-agent-principal -n openshift-gitops --context kind-hub
kubectl rollout restart deployment argocd-agent-agent -n openshift-gitops --context kind-cluster1
sleep 90s
kubectl config use-context kind-hub
kubectl apply -f e2e-gitopsaddon/app.yaml

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
