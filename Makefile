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

test-unit:
	go test -timeout 300s -v ./pkg/...
	go test -timeout 300s -v ./propagation-controller/...
	go test -timeout 300s -v ./gitopsaddon/...
	go test -timeout 300s -v ./maestroapplication/...
	go test -timeout 300s -v ./cmd/...

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

CONTROLLER_TOOLS_VERSION ?= v0.16.1

CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: deploy-ocm
deploy-ocm:
	deploy/ocm/install.sh

.PHONY: test-e2e
test-e2e: deploy-ocm
	e2e/run_e2e.sh

.PHONY: test-e2e
test-e2e-gitopsaddon: deploy-ocm
	e2e-gitopsaddon/run_e2e-gitopsaddon.sh
