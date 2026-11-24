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

.DEFAULT_GOAL := build

REGISTRY = quay.io/stolostron
VERSION = latest
IMAGE_NAME_AND_VERSION ?= $(REGISTRY)/multicloud-integrations:$(VERSION)

# clusteradm CLI version - installs stable OCM components by default
CLUSTERADM_VERSION ?= v1.1.1
export GOPACKAGES   = $(shell go list ./... | grep -v /manager | grep -v /bindata  | grep -v /vendor | grep -v /internal | grep -v /build | grep -v /test | grep -v /e2e )

K8S_VERSION ?=1.28.3
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest

.PHONY: generate-operator-images verify-operator-images

# Keep the hub-side image list in sync with the operator chart values.
generate-operator-images:
	@go run ./hack/gen-operator-images

verify-operator-images:
	@go run ./hack/gen-operator-images -check

.PHONY: build

build: generate-operator-images
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
# Pinned (not @latest): setup-envtest's newest tagged releases (v0.24.0+) require go >= 1.26,
# newer than this repo's go.mod (go 1.25.0) / controller-runtime v0.21.0 dependency. @latest
# breaks the build the moment a newer setup-envtest release ships, with no code change on our
# side - confirmed by "requires go >= 1.26.0 (running go 1.25.11)" failing here. Pinned to the
# commit setup-envtest was built from on the release-0.21 branch (the line matching this repo's
# controller-runtime version), verified installable and working under go 1.25.
SETUP_ENVTEST_VERSION ?= v0.0.0-20250626154428-7fd020cb5fc3
$(SETUP_ENVTEST): $(LOCALBIN)
	@if [ -s $(SETUP_ENVTEST) ] && [ "$$($(SETUP_ENVTEST) version 2>/dev/null | awk '{print $$NF}')" = "$(SETUP_ENVTEST_VERSION)" ]; then \
		exit 0; \
	fi; \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION)

test: test-unit

test-unit: setup-envtest
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./pkg/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./propagation-controller/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./gitopsaddon/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./maestroapplication/...
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -timeout 300s -v ./cmd/...
	go test -timeout 300s -v ./hack/...

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
E2E_IMG ?= quay.io/stolostron/multicloud-integrations:e2e
KIND ?= kind
KUBECTL ?= kubectl

# The gitopsaddon e2e pulls the chart's registry.redhat.io images. It skips itself
# when no docker config JSON with registry.redhat.io credentials is available.
RH_PULL_SECRET_FILE ?= $(shell if [ -f "$$HOME/.docker/config.json" ] && grep -q 'registry.redhat.io' "$$HOME/.docker/config.json" 2>/dev/null; then echo "$$HOME/.docker/config.json"; fi)
export RH_PULL_SECRET_FILE

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
endef

# setup_env.sh leaves a gitopsaddon process running against the hub; the trap
# guarantees it never outlives the test run and holds the metrics port.
define RUN_GITOPSADDON_E2E
	@set -e; \
	trap 'if [ -f /tmp/gitopsaddon-e2e.pid ]; then kill $$(cat /tmp/gitopsaddon-e2e.pid) 2>/dev/null || true; rm -f /tmp/gitopsaddon-e2e.pid; fi' EXIT; \
	if [ -z "$(RH_PULL_SECRET_FILE)" ]; then \
		echo "SKIP: RH_PULL_SECRET_FILE is not set and ~/.docker/config.json has no registry.redhat.io entry."; \
		echo "SKIP: the GitOps addon e2e installs the Red Hat operator and ArgoCD images from registry.redhat.io,"; \
		echo "SKIP: which cannot be pulled anonymously. Set RH_PULL_SECRET_FILE=/path/to/dockerconfig.json to run it."; \
		RH_PULL_SECRET_FILE= go test -tags=e2e -timeout 30m ./test/e2e/gitopsaddon/ -v -ginkgo.v --ginkgo.label-filter="$(1)"; \
	else \
		E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) RH_PULL_SECRET_FILE="$(RH_PULL_SECRET_FILE)" ADDON_PID_FILE=/tmp/gitopsaddon-e2e.pid ADDON_LOG=/tmp/gitopsaddon-e2e.log ./test/e2e/gitopsaddon/scripts/setup_env.sh; \
		$(KUBECTL) config use-context kind-$(HUB_CLUSTER); \
		RH_PULL_SECRET_FILE="$(RH_PULL_SECRET_FILE)" go test -tags=e2e -timeout 30m ./test/e2e/gitopsaddon/ -v -ginkgo.v --ginkgo.label-filter="$(1)"; \
	fi
endef

# --- Scenarios (CI) ---

.PHONY: test-e2e-gitopsaddon-embedded
test-e2e-gitopsaddon-embedded: manifests
	$(call RUN_GITOPSADDON_E2E,embedded)

.PHONY: test-e2e-gitopsaddon-embedded-agent
test-e2e-gitopsaddon-embedded-agent: manifests
	$(call RUN_GITOPSADDON_E2E,embedded-agent)

.PHONY: test-e2e-gitopsaddon-olm-override
test-e2e-gitopsaddon-olm-override: manifests
	$(call RUN_GITOPSADDON_E2E,olm-override)

.PHONY: test-e2e-gitopsaddon-embedded-autonomous
test-e2e-gitopsaddon-embedded-autonomous: manifests
	$(call RUN_GITOPSADDON_E2E,embedded-autonomous)

# --- Scenarios (Local) ---

.PHONY: test-local-e2e-gitopsaddon-embedded
test-local-e2e-gitopsaddon-embedded:
	$(SETUP_KIND_CLUSTERS)
	$(MAKE) test-e2e-gitopsaddon-embedded

.PHONY: test-local-e2e-gitopsaddon-embedded-agent
test-local-e2e-gitopsaddon-embedded-agent:
	$(SETUP_KIND_CLUSTERS)
	$(MAKE) test-e2e-gitopsaddon-embedded-agent

.PHONY: test-local-e2e-gitopsaddon-olm-override
test-local-e2e-gitopsaddon-olm-override:
	$(SETUP_KIND_CLUSTERS)
	$(MAKE) test-e2e-gitopsaddon-olm-override

.PHONY: test-local-e2e-gitopsaddon-embedded-autonomous
test-local-e2e-gitopsaddon-embedded-autonomous:
	$(SETUP_KIND_CLUSTERS)
	$(MAKE) test-e2e-gitopsaddon-embedded-autonomous

# --- Aggregate targets ---

.PHONY: test-e2e-gitopsaddon-all
test-e2e-gitopsaddon-all:
	$(MAKE) test-e2e-gitopsaddon-embedded
	$(MAKE) test-e2e-gitopsaddon-embedded-agent
	$(MAKE) test-e2e-gitopsaddon-olm-override
	$(MAKE) test-e2e-gitopsaddon-embedded-autonomous

.PHONY: clean-e2e
clean-e2e:
	@$(KIND) delete cluster --name $(HUB_CLUSTER) || true
	@$(KIND) delete cluster --name $(SPOKE_CLUSTER) || true

.PHONY: all
all: build test

.PHONY: clean
clean: clean-e2e
