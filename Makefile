# Copyright 2022 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

LOCAL_OS := $(shell uname)
ifeq ($(LOCAL_OS),Linux)
    TARGET_OS ?= linux
    XARGS_FLAGS="-r"
else ifeq ($(LOCAL_OS),Darwin)
    TARGET_OS ?= darwin
    XARGS_FLAGS=
else
    $(error "This system's OS $(LOCAL_OS) isn't recognized/supported")
endif

FINDFILES=find . \( -path ./.git -o -path ./.github \) -prune -o -type f
XARGS = xargs -0 ${XARGS_FLAGS}
CLEANXARGS = xargs ${XARGS_FLAGS}

REGISTRY = quay.io/stolostron
VERSION = latest
IMAGE_NAME_AND_VERSION ?= $(REGISTRY)/multicloud-integrations:$(VERSION)

# clusteradm CLI version - installs stable OCM components by default
CLUSTERADM_VERSION ?= v1.1.1
export GOPACKAGES   = $(shell go list ./... | grep -v /manager | grep -v /bindata  | grep -v /vendor | grep -v /internal | grep -v /build | grep -v /test | grep -v /e2e )

K8S_VERSION ?=1.28.3
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest

.PHONY: build

build:
	@common/scripts/gobuild.sh build/_output/bin/gitopscluster ./cmd/gitopscluster
	@common/scripts/gobuild.sh build/_output/bin/gitopssyncresc ./cmd/gitopssyncresc
	@common/scripts/gobuild.sh build/_output/bin/multiclusterstatusaggregation ./cmd/multiclusterstatusaggregation
	@common/scripts/gobuild.sh build/_output/bin/propagation ./cmd/propagation
	@common/scripts/gobuild.sh build/_output/bin/gitopsaddon ./cmd/gitopsaddon
	@common/scripts/gobuild.sh build/_output/bin/maestroPropagation ./cmd/maestropropagation
	@common/scripts/gobuild.sh build/_output/bin/maestroAggregation ./cmd/maestroaggregation

local:
	@GOOS=darwin common/scripts/gobuild.sh build/_output/bin/gitopscluster ./cmd/gitopscluster
	@GOOS=darwin common/scripts/gobuild.sh build/_output/bin/gitopssyncresc ./cmd/gitopssyncresc
	@GOOS=darwin common/scripts/gobuild.sh build/_output/bin/multiclusterstatusaggregation ./cmd/multiclusterstatusaggregation
	@GOOS=darwin common/scripts/gobuild.sh build/_output/bin/propagation ./cmd/propagation
	@GOOS=darwin common/scripts/gobuild.sh build/_output/bin/gitopsaddon ./cmd/gitopsaddon
	@GOOS=darwin common/scripts/gobuild.sh build/_output/bin/maestroPropagation ./cmd/maestropropagation
	@GOOS=darwin common/scripts/gobuild.sh build/_output/bin/maestroAggregation ./cmd/maestroaggregation

.PHONY: build-images

build-images: build
	@docker build -t ${IMAGE_NAME_AND_VERSION} -f build/Dockerfile .

# build local linux/amd64 images on non-amd64 hosts such as Apple M3
# need to create the buildx builder as a fixed name and clean it up after usage
# Or a new builder is created everytime and it will fail docker buildx image build eventually.
build-images-non-amd64-docker:
	@if docker buildx ls | grep -q "local-builder"; then \
		echo "Removing existing local-builder..."; \
		docker buildx rm local-builder; \
	fi
	docker buildx create --name local-builder --use
	docker buildx inspect local-builder --bootstrap
	docker buildx build --platform linux/amd64 -t ${IMAGE_NAME_AND_VERSION} -f build/Dockerfile --load .
	docker buildx rm local-builder


build-images-non-amd64-podman:
	podman build --platform linux/amd64 -t ${IMAGE_NAME_AND_VERSION} -f build/Dockerfile .

.PHONY: lint

lint: lint-all

.PHONY: lint-all

lint-all:lint-go

.PHONY: lint-go

lint-go:
	@true

.PHONY: test

# download the kubebuilder-tools to get kube-apiserver binaries from it
.PHONY: setup-envtest
setup-envtest: $(SETUP_ENVTEST)
$(SETUP_ENVTEST): $(LOCALBIN)
	test -s $(SETUP_ENVTEST) || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

test: test-unit

test-unit: setup-envtest
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./pkg/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./propagation-controller/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./gitopsaddon/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./maestroapplication/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./cmd/...

test-integration: setup-envtest
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -tags=integration -timeout 300s -v ./pkg/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -tags=integration -timeout 300s -v ./propagation-controller/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -tags=integration -timeout 300s -v ./gitopsaddon/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -tags=integration -timeout 300s -v ./maestroapplication/...

.PHONY: manifests
manifests: controller-gen
	$(CONTROLLER_GEN) crd webhook paths="./..." output:crd:artifacts:config=deploy/crds

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

CONTROLLER_TOOLS_VERSION ?= v0.17.3

CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: deploy-ocm
deploy-ocm:
	CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) deploy/ocm/install.sh

# E2E Test Configuration
HUB_CLUSTER ?= hub
SPOKE_CLUSTER ?= cluster1
E2E_IMG ?= quay.io/stolostron/multicloud-integrations:latest
ARGOCD_OPERATOR_VERSION ?= v0.17.0
ARGOCD_OPERATOR_IMAGE ?= quay.io/argoprojlabs/argocd-operator:$(ARGOCD_OPERATOR_VERSION)
export ARGOCD_OPERATOR_IMAGE
KIND ?= kind
KUBECTL ?= kubectl

##@ Cluster-Import E2E Tests (legacy script-based)

.PHONY: test-e2e
test-e2e:
	e2e/run_e2e.sh

.PHONY: test-local-e2e
test-local-e2e:
	@$(KIND) delete cluster --name $(HUB_CLUSTER) || true
	@$(KIND) delete cluster --name $(SPOKE_CLUSTER) || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG)
	$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER)
	$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER)
	E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) e2e/run_e2e.sh

.PHONY: test-e2e-cluster-secret-deletion
test-e2e-cluster-secret-deletion:
	e2e/cluster_secret_deletion_test.sh

.PHONY: test-local-e2e-cluster-secret-deletion
test-local-e2e-cluster-secret-deletion:
	@$(KIND) delete cluster --name $(HUB_CLUSTER) || true
	@$(KIND) delete cluster --name $(SPOKE_CLUSTER) || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG)
	$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER)
	$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER)
	E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) e2e/run_e2e.sh
	e2e/cluster_secret_deletion_test.sh

##@ GitOps Addon E2E Tests (Go / Ginkgo, driven by test/e2e/gitopsaddon/)

# --- Internal helpers (not called directly) ---

define SETUP_KIND_CLUSTERS
	@$(KIND) delete cluster --name $(HUB_CLUSTER) || true
	@$(KIND) delete cluster --name $(SPOKE_CLUSTER) || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG)
	$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER)
	$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER)
	docker pull $(ARGOCD_OPERATOR_IMAGE) || true
	$(KIND) load docker-image $(ARGOCD_OPERATOR_IMAGE) --name $(HUB_CLUSTER)
	$(KIND) load docker-image $(ARGOCD_OPERATOR_IMAGE) --name $(SPOKE_CLUSTER)
endef

define RUN_GITOPSADDON_E2E
	E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ARGOCD_OPERATOR_IMAGE=$(ARGOCD_OPERATOR_IMAGE) ./test/e2e/gitopsaddon/scripts/setup_env.sh
	$(KUBECTL) config use-context kind-$(HUB_CLUSTER)
	go test -tags=e2e -timeout 30m ./test/e2e/gitopsaddon/ -v -ginkgo.v --ginkgo.label-filter="$(1)"
endef

# --- Scenarios (CI) ---

.PHONY: test-e2e-gitopsaddon-embedded
test-e2e-gitopsaddon-embedded: manifests
	$(call RUN_GITOPSADDON_E2E,embedded)

.PHONY: test-e2e-gitopsaddon-embedded-agent
test-e2e-gitopsaddon-embedded-agent: manifests
	$(call RUN_GITOPSADDON_E2E,embedded-agent)

# --- Scenarios (Local) ---

.PHONY: test-local-e2e-gitopsaddon-embedded
test-local-e2e-gitopsaddon-embedded:
	$(SETUP_KIND_CLUSTERS)
	$(MAKE) test-e2e-gitopsaddon-embedded

.PHONY: test-local-e2e-gitopsaddon-embedded-agent
test-local-e2e-gitopsaddon-embedded-agent:
	$(SETUP_KIND_CLUSTERS)
	$(MAKE) test-e2e-gitopsaddon-embedded-agent

# --- Aggregate targets ---

.PHONY: test-e2e-gitopsaddon-all
test-e2e-gitopsaddon-all: test-e2e-gitopsaddon-embedded test-e2e-gitopsaddon-embedded-agent

.PHONY: clean-e2e
clean-e2e:
	@$(KIND) delete cluster --name $(HUB_CLUSTER) || true
	@$(KIND) delete cluster --name $(SPOKE_CLUSTER) || true

.PHONY: all
all: build test

.PHONY: clean
clean: clean-e2e
