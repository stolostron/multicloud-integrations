#!/bin/bash

set -o nounset
set -o pipefail
set -o errexit

# kind delete clusters --all; kind create cluster --name hub; kind create cluster --name cluster1; kind load docker-image --name=hub quay.io/stolostron/multicloud-integrations:latest; kind load docker-image --name=cluster1 quay.io/stolostron/multicloud-integrations:latest

KUBECTL=${KUBECTL:-kubectl}
# CLUSTERADM_VERSION should be passed from Makefile, fallback to v1.1.1 stable release
CLUSTERADM_CLI_VERSION="${CLUSTERADM_VERSION:-v1.1.1}"

BUILD_DIR="$( cd "$(dirname "$0")" >/dev/null 2>&1 ; pwd -P )"
DEPLOY_DIR="$(dirname "$BUILD_DIR")"
EXAMPLE_DIR="$(dirname "$DEPLOY_DIR")"
REPO_DIR="$(dirname "$EXAMPLE_DIR")"
WORK_DIR="${REPO_DIR}/_output"
CLUSTERADM="clusteradm"

export PATH=$PATH:${WORK_DIR}/bin

echo "############  Install clusteradm ${CLUSTERADM_CLI_VERSION}"
go install open-cluster-management.io/clusteradm/cmd/clusteradm@${CLUSTERADM_CLI_VERSION}

if ! command -v "${CLUSTERADM}" >/dev/null 2>&1; then
    echo "ERROR: ${CLUSTERADM} is not on PATH after go install (GOBIN/PATH mismatch)" >&2
    exit 1
fi

echo "############  clusteradm will install stable OCM components by default"

echo "############ Init hub"
$KUBECTL config use-context kind-hub
${CLUSTERADM} init --wait
joincmd=$(${CLUSTERADM} get token | grep clusteradm || true)
if [[ -z "${joincmd}" ]]; then
    echo "ERROR: could not obtain the clusteradm join token" >&2
    exit 1
fi

echo "############ Init agent as cluster1"
$KUBECTL config use-context kind-cluster1
$(echo ${joincmd} --force-internal-endpoint-lookup --wait | sed "s/<cluster_name>/cluster1/g")

echo "############ Accept join of cluster1"
$KUBECTL config use-context kind-hub
${CLUSTERADM} accept --clusters cluster1

if ! ${KUBECTL} wait --for=condition=Established --timeout=120s \
    crd/managedclusters.cluster.open-cluster-management.io; then
    echo "ERROR: OCM did not establish the ManagedCluster CRD" >&2
    exit 1
fi

echo "############  All-in-one env is installed successfully!!"
