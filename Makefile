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

TEST_TMP :=/tmp
export KUBEBUILDER_ASSETS ?=$(TEST_TMP)/kubebuilder/bin
K8S_VERSION ?=1.28.3
GOHOSTOS ?=$(shell go env GOHOSTOS)
GOHOSTARCH ?= $(shell go env GOHOSTARCH)
KB_TOOLS_ARCHIVE_NAME :=kubebuilder-tools-$(K8S_VERSION)-$(GOHOSTOS)-$(GOHOSTARCH).tar.gz
KB_TOOLS_ARCHIVE_PATH := $(TEST_TMP)/$(KB_TOOLS_ARCHIVE_NAME)

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
ensure-kubebuilder-tools:
ifeq "" "$(wildcard $(KUBEBUILDER_ASSETS))"
	$(info Downloading kube-apiserver into '$(KUBEBUILDER_ASSETS)')
	mkdir -p '$(KUBEBUILDER_ASSETS)'
	curl -s -f -L https://storage.googleapis.com/kubebuilder-tools/$(KB_TOOLS_ARCHIVE_NAME) -o '$(KB_TOOLS_ARCHIVE_PATH)'
	tar -C '$(KUBEBUILDER_ASSETS)' --strip-components=2 -zvxf '$(KB_TOOLS_ARCHIVE_PATH)'
else
	$(info Using existing kube-apiserver from "$(KUBEBUILDER_ASSETS)")
endif
.PHONY: ensure-kubebuilder-tools

test: test-unit

test-unit: ensure-kubebuilder-tools
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -timeout 300s -v ./pkg/...
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -timeout 300s -v ./propagation-controller/...
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -timeout 300s -v ./gitopsaddon/...
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -timeout 300s -v ./maestroapplication/...
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -timeout 300s -v ./cmd/...

test-integration: ensure-kubebuilder-tools
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -tags=integration -timeout 300s -v ./pkg/...
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -tags=integration -timeout 300s -v ./propagation-controller/...
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -tags=integration -timeout 300s -v ./gitopsaddon/...
	KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS) go test -tags=integration -timeout 300s -v ./maestroapplication/...

.PHONY: manifests
manifests: controller-gen
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=deploy/crds

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
KIND ?= kind
KUBECTL ?= kubectl

# test-e2e: For CI - assumes clusters and images exist, runs legacy e2e script
.PHONY: test-e2e
test-e2e:
	e2e/run_e2e.sh

# test-e2e-full: For local - creates clusters, builds images, runs legacy e2e script
.PHONY: test-e2e-full
test-e2e-full:
	@echo "===== E2E Full Test (Local mode) ====="
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) e2e/run_e2e.sh 2>&1 | tee -a /tmp/e2e-full.log' && echo "✓ E2E Full Test Complete - Logs: /tmp/e2e-full.log"

# test-e2e-gitopsaddon-full: For local - creates clusters, builds images, verifies GitOps addon with app sync
.PHONY: test-e2e-gitopsaddon-full
test-e2e-gitopsaddon-full:
	@echo "===== E2E GitOps Addon Full Test (Local mode) ====="
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-gitopsaddon-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-gitopsaddon-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-gitopsaddon-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-gitopsaddon-full.sh 2>&1 | tee -a /tmp/e2e-gitopsaddon-full.log' && echo "✓ E2E GitOps Addon Full Test Complete - Logs: /tmp/e2e-gitopsaddon-full.log"

# test-e2e-gitopsaddon: For CI - assumes clusters and images exist, verifies GitOps addon (no app sync)
.PHONY: test-e2e-gitopsaddon
test-e2e-gitopsaddon: manifests
	@echo "===== E2E GitOps Addon Test (CI mode) ====="
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-gitopsaddon.sh 2>&1 | tee /tmp/e2e-gitopsaddon.log' && echo "✓ E2E GitOps Addon Test Complete - Logs: /tmp/e2e-gitopsaddon.log"

# test-e2e-gitopsaddon-cleanup: For CI - assumes clusters and images exist, verifies GitOps addon cleanup
.PHONY: test-e2e-gitopsaddon-cleanup
test-e2e-gitopsaddon-cleanup: manifests
	@echo "===== E2E GitOps Addon Cleanup Test (CI mode) ====="
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-gitopsaddon-cleanup.sh 2>&1 | tee /tmp/e2e-gitopsaddon-cleanup.log' && echo "✓ E2E GitOps Addon Cleanup Test Complete - Logs: /tmp/e2e-gitopsaddon-cleanup.log"

# test-e2e-gitopsaddon-cleanup-full: For local - creates clusters, builds images, verifies GitOps addon cleanup (no agent)
.PHONY: test-e2e-gitopsaddon-cleanup-full
test-e2e-gitopsaddon-cleanup-full:
	@echo "===== E2E GitOps Addon Cleanup Full Test (Local mode) ====="
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-gitopsaddon-cleanup-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-gitopsaddon-cleanup-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-gitopsaddon-cleanup-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-gitopsaddon-cleanup-full.sh 2>&1 | tee -a /tmp/e2e-gitopsaddon-cleanup-full.log' && echo "✓ E2E GitOps Addon Cleanup Full Test Complete - Logs: /tmp/e2e-gitopsaddon-cleanup-full.log"

# test-e2e-gitopsaddon-agent-full: For local - creates clusters, builds images, verifies GitOps addon WITH ArgoCD agent
.PHONY: test-e2e-gitopsaddon-agent-full
test-e2e-gitopsaddon-agent-full:
	@echo "===== E2E GitOps Addon Agent Full Test (Local mode) ====="
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-gitopsaddon-agent-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-gitopsaddon-agent-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-gitopsaddon-agent-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-gitopsaddon-agent-full.sh 2>&1 | tee -a /tmp/e2e-gitopsaddon-agent-full.log' && echo "✓ E2E GitOps Addon Agent Full Test Complete - Logs: /tmp/e2e-gitopsaddon-agent-full.log"

# test-e2e-gitopsaddon-agent-cleanup-full: For local - creates clusters, builds images, verifies GitOps addon cleanup WITH ArgoCD agent
.PHONY: test-e2e-gitopsaddon-agent-cleanup-full
test-e2e-gitopsaddon-agent-cleanup-full:
	@echo "===== E2E GitOps Addon Agent Cleanup Full Test (Local mode) ====="
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-gitopsaddon-agent-cleanup-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-gitopsaddon-agent-cleanup-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-gitopsaddon-agent-cleanup-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-gitopsaddon-agent-cleanup-full.sh 2>&1 | tee -a /tmp/e2e-gitopsaddon-agent-cleanup-full.log' && echo "✓ E2E GitOps Addon Agent Cleanup Full Test Complete - Logs: /tmp/e2e-gitopsaddon-agent-cleanup-full.log"

# test-e2e-olm-subscription: For CI - assumes clusters and images exist, verifies OLM subscription mode
.PHONY: test-e2e-olm-subscription
test-e2e-olm-subscription: manifests
	@echo "===== E2E OLM Subscription Test (CI mode) ====="
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-olm-subscription.sh 2>&1 | tee /tmp/e2e-olm-subscription.log' && echo "✓ E2E OLM Subscription Test Complete - Logs: /tmp/e2e-olm-subscription.log"

# test-e2e-olm-subscription-full: For local - creates clusters, builds images, verifies OLM subscription mode
.PHONY: test-e2e-olm-subscription-full
test-e2e-olm-subscription-full:
	@echo "===== E2E OLM Subscription Full Test (Local mode) ====="
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-olm-subscription-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-olm-subscription-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-olm-subscription-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-olm-subscription.sh 2>&1 | tee -a /tmp/e2e-olm-subscription-full.log' && echo "✓ E2E OLM Subscription Full Test Complete - Logs: /tmp/e2e-olm-subscription-full.log"

# test-e2e-olm-subscription-cleanup: For CI - verifies OLM subscription cleanup (assumes setup already done)
.PHONY: test-e2e-olm-subscription-cleanup
test-e2e-olm-subscription-cleanup:
	@bash -o pipefail -c 'CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-olm-subscription-cleanup.sh 2>&1 | tee /tmp/e2e-olm-subscription-cleanup.log' && echo "✓ E2E OLM Subscription Cleanup Test Complete - Logs: /tmp/e2e-olm-subscription-cleanup.log"

# test-e2e-olm-subscription-cleanup-full: For local - creates clusters, builds images, verifies OLM subscription cleanup (no agent)
.PHONY: test-e2e-olm-subscription-cleanup-full
test-e2e-olm-subscription-cleanup-full:
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-olm-subscription-cleanup-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-olm-subscription-cleanup-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-olm-subscription-cleanup-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-olm-subscription-cleanup-full.sh 2>&1 | tee -a /tmp/e2e-olm-subscription-cleanup-full.log' && echo "✓ E2E OLM Subscription Cleanup Full Test Complete - Logs: /tmp/e2e-olm-subscription-cleanup-full.log"

# test-e2e-olm-subscription-agent-full: For local - creates clusters, builds images, verifies OLM subscription WITH ArgoCD agent
.PHONY: test-e2e-olm-subscription-agent-full
test-e2e-olm-subscription-agent-full:
	@echo "===== E2E OLM Subscription Agent Full Test (Local mode) ====="
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-olm-subscription-agent-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-olm-subscription-agent-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-olm-subscription-agent-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-olm-subscription-agent-full.sh 2>&1 | tee -a /tmp/e2e-olm-subscription-agent-full.log' && echo "✓ E2E OLM Subscription Agent Full Test Complete - Logs: /tmp/e2e-olm-subscription-agent-full.log"

# test-e2e-olm-subscription-agent-cleanup-full: For local - creates clusters, builds images, verifies OLM subscription cleanup WITH ArgoCD agent
.PHONY: test-e2e-olm-subscription-agent-cleanup-full
test-e2e-olm-subscription-agent-cleanup-full:
	@echo "===== E2E OLM Subscription Agent Cleanup Full Test (Local mode) ====="
	@$(KIND) delete clusters --all || true
	@$(KIND) create cluster --name $(HUB_CLUSTER)
	@$(KIND) create cluster --name $(SPOKE_CLUSTER)
	@bash -o pipefail -c '$(MAKE) build-images IMAGE_NAME_AND_VERSION=$(E2E_IMG) 2>&1 | tee /tmp/e2e-olm-subscription-agent-cleanup-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(HUB_CLUSTER) 2>&1 | tee -a /tmp/e2e-olm-subscription-agent-cleanup-full.log'
	@bash -o pipefail -c '$(KIND) load docker-image $(E2E_IMG) --name $(SPOKE_CLUSTER) 2>&1 | tee -a /tmp/e2e-olm-subscription-agent-cleanup-full.log'
	@bash -o pipefail -c 'E2E_IMG=$(E2E_IMG) CLUSTERADM_VERSION=$(CLUSTERADM_VERSION) ./test/e2e/scripts/e2e-olm-subscription-agent-cleanup-full.sh 2>&1 | tee -a /tmp/e2e-olm-subscription-agent-cleanup-full.log' && echo "✓ E2E OLM Subscription Agent Cleanup Full Test Complete - Logs: /tmp/e2e-olm-subscription-agent-cleanup-full.log"

.PHONY: clean-full
clean-full:
	@echo "===== Cleaning up all KinD clusters ====="
	@$(KIND) delete clusters --all || true
	@echo "===== Cleanup complete ====="
