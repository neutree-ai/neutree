GIT_COMMIT = $(shell git rev-parse --short HEAD)
VERSION ?= $(shell git describe --tags --always --dirty)
LATEST ?= false

IMAGE_REPO ?= docker.io
IMAGE_PROJECT ?= neutree-ai
IMAGE_PREFIX ?= ${IMAGE_REPO}/${IMAGE_PROJECT}/
IMAGE_TAG ?= ${shell echo $(VERSION) | awk -F '/' '{print $$NF}'}
NEUTREE_CORE_IMAGE := $(IMAGE_PREFIX)neutree-core

ARCH ?= amd64
ALL_ARCH = amd64 arm64

GO := go
# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef

SHELL := /bin/bash

MODULE_PATH = github.com/neutree-ai/neutree
GO_BUILD_ARGS = \
	-ldflags="-extldflags=-static \
	-X '$(MODULE_PATH)/pkg/version.gitCommit=$(GIT_COMMIT)' \
	-X '$(MODULE_PATH)/pkg/version.appVersion=$(IMAGE_TAG)' \
	-X '$(MODULE_PATH)/pkg/version.buildTime=$(shell date --iso-8601=seconds)'"

MOCKERY_DIRS=./ pkg/model_registry pkg/storage pkg/command internal/orchestrator internal/orchestrator/ray internal/orchestrator/ray/dashboard internal/registry controllers/ internal/observability/monitoring internal/observability/config
MOCKERY_OUTPUT_DIRS=testing/mocks pkg/model_registry/mocks pkg/storage/mocks pkg/command/mocks internal/orchestrator/mocks internal/orchestrator/ray/mocks internal/orchestrator/ray/dashboard/mocks internal/registry/mocks controllers/mocks internal/observability/monitoring/mocks internal/observability/config/mocks


.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-21s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

all: build

build: test build-neutree-core build-neutree-cli

build-neutree-core:
	$(GO) build -o bin/neutree-core ./cmd/neutree-core/neutree-core.go

prepare-build-cli:
	tar -cvf db.tar db
	cd deploy/docker && tar -cvf obs-stack.tar obs-stack && tar -cvf neutree-core.tar neutree-core
	mv -f db.tar cmd/neutree-cli/app/cmd/launch/manifests/
	mv -f deploy/docker/neutree-core.tar cmd/neutree-cli/app/cmd/launch/manifests/
	mv -f deploy/docker/obs-stack.tar cmd/neutree-cli/app/cmd/launch/manifests/

build-neutree-cli: prepare-build-cli
	$(GO) build -o bin/neutree-cli ./cmd/neutree-cli/neutree-cli.go

.PHONY: docker-build
docker-build:
	docker build --build-arg ARCH=$(ARCH) --build-arg GO_BUILD_ARGS=$(GO_BUILD_ARGS) . -t $(NEUTREE_CORE_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push
docker-push:
	docker push $(NEUTREE_CORE_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-build-all ## Build all the architecture docker images
docker-build-all: $(addprefix docker-build-,$(ALL_ARCH))

docker-build-%:
	$(MAKE) ARCH=$* docker-build

.PHONY: docker-push-all ## Push all the architecture docker images
docker-push-all: $(addprefix docker-push-,$(ALL_ARCH))
	$(MAKE) docker-push-manifest

docker-push-%:
	$(MAKE) ARCH=$* docker-push

.PHONY: docker-push-manifest
docker-push-manifest: ## Push the fat manifest docker image.
	## Minimum docker version 18.06.0 is required for creating and pushing manifest images.
	docker manifest create --amend $(NEUTREE_CORE_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_CORE_IMAGE)\-&:$(IMAGE_TAG)~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_CORE_IMAGE}:${IMAGE_TAG} ${NEUTREE_CORE_IMAGE}-$${arch}:${IMAGE_TAG}; done
	docker manifest push --purge ${NEUTREE_CORE_IMAGE}:${IMAGE_TAG}


ENVTEST_ASSETS_DIR=$(shell pwd)/bin

.PHONY: test
test: prepare-build-cli mockgen fmt vet lint ## Run unit test
	go test -coverprofile coverage.out -covermode=atomic $(shell go list ./... | grep -v 'e2e\|mocks')

.PHONY: clean
clean: ## Clean up
	rm -rf bin

GOLANGCI_LINT := $(shell pwd)/bin/golangci-lint
golangci-lint: ## Download golangci-lint if not yet.
	$(call go-get-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.6)

.PHONY: lint
lint: prepare-build-cli golangci-lint ## Lint codebase
	$(GOLANGCI_LINT) run -v --fast=false --fix

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: release
release:
	$(MAKE) docker-build-all
	$(MAKE) docker-push-all

MOCKERY := $(shell pwd)/bin/mockery
mockery: ## Download mockery if not yet.
	$(call go-get-tool,$(MOCKERY),github.com/vektra/mockery/v2@v2.53.3)

.PHONY: mockgen
mockgen: mockery
	@for dir in $(MOCKERY_OUTPUT_DIRS); do \
		rm -rf $$dir; \
    done

	@for dir in $(MOCKERY_DIRS); do \
		cd $(PROJECT_DIR); \
		cd $$dir; \
		$(MOCKERY); \
    done
