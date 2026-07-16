GIT_COMMIT = $(shell git rev-parse --short HEAD)
VERSION ?= $(shell git describe --tags --always --dirty)
UI_VERSION ?= main
CLUSTER_VERSION ?= v1.1.0
LATEST ?= false

IMAGE_REPO ?= docker.io
IMAGE_PROJECT ?= neutree
IMAGE_PREFIX ?= ${IMAGE_REPO}/${IMAGE_PROJECT}/
IMAGE_TAG ?= ${shell echo $(VERSION) | awk -F '/' '{print $$NF}'}
NEUTREE_CORE_IMAGE := $(IMAGE_PREFIX)neutree-core
NEUTREE_API_IMAGE := $(IMAGE_PREFIX)neutree-api
NEUTREE_DB_SCRIPTS_IMAGE := $(IMAGE_PREFIX)neutree-db-scripts
NEUTREE_RUNTIME_IMAGE := $(IMAGE_PREFIX)neutree-runtime
NEUTREE_NODE_AGENT_IMAGE := $(IMAGE_PREFIX)neutree-node-agent

ARCH ?= amd64
ALL_ARCH ?= amd64 arm64

TOOLS_BIN_DIR := $(shell pwd)/bin

GO := go
# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

GO_VERSION ?= 1.23.0

# Host information.
HOST_OS ?= $(shell sh -c "PATH=$(PATH) go env GOOS")
HOST_ARCH ?= $(shell sh -c "PATH=$(PATH) go env GOARCH")

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

RELEASE_DIR ?= out

SHELL := /bin/bash

MODULE_PATH = github.com/neutree-ai/neutree
GO_BUILD_ARGS = \
	-ldflags="-extldflags=-static \
	-X '$(MODULE_PATH)/internal/version.gitCommit=$(GIT_COMMIT)' \
	-X '$(MODULE_PATH)/internal/version.appVersion=$(IMAGE_TAG)' \
	-X '$(MODULE_PATH)/internal/version.buildTime=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")'"
NODE_AGENT_GO_BUILD_ARGS = \
	-ldflags="-X '$(MODULE_PATH)/internal/version.gitCommit=$(GIT_COMMIT)' \
	-X '$(MODULE_PATH)/internal/version.appVersion=$(IMAGE_TAG)' \
	-X '$(MODULE_PATH)/internal/version.buildTime=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")'"

MOCKERY_DIRS=./ internal/model_registry pkg/storage pkg/command internal/orchestrator internal/cluster internal/ray/dashboard internal/registry controllers/ internal/observability/monitoring internal/observability/config internal/gateway internal/accelerator internal/auth internal/util
MOCKERY_OUTPUT_DIRS=testing/mocks internal/model_registry/mocks pkg/storage/mocks pkg/command/mocks internal/orchestrator/mocks internal/cluster/mocks internal/ray/dashboard/mocks internal/registry/mocks controllers/mocks internal/observability/monitoring/mocks internal/observability/config/mocks internal/gateway/mocks internal/accelerator/mocks internal/auth/mocks internal/util/mocks


.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-21s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

all: build

.PHONY: install-hooks
install-hooks: ## Enable .githooks as local git hooks (run once per worktree)
	git config extensions.worktreeConfig true
	git config --worktree core.hooksPath "$(PROJECT_DIR)/.githooks"
	chmod +x .githooks/pre-commit
	chmod +x scripts/check-boundaries.sh scripts/check-migration-pairs.sh scripts/builder/sync-controlplane-images.sh
	@echo "Git hooks installed for this worktree. Pre-commit will run on every 'git commit'."

build: test build-neutree-core build-neutree-cli build-neutree-api

build-neutree-core:
	$(GO) build ${GO_BUILD_ARGS} -o bin/neutree-core ./cmd/neutree-core/neutree-core.go

prepare-build-cli: sync-deploy-manifests
	cd deploy/docker && tar -cvf obs-stack.tar obs-stack && tar -cvf neutree-core.tar neutree-core
	mv -f deploy/docker/neutree-core.tar cmd/neutree-cli/app/cmd/launch/manifests/
	mv -f deploy/docker/obs-stack.tar cmd/neutree-cli/app/cmd/launch/manifests/

build-neutree-cli: prepare-build-cli
	$(GO) build ${GO_BUILD_ARGS} -o bin/neutree-cli ./cmd/neutree-cli/neutree-cli.go

build-neutree-api:
	$(GO) build ${GO_BUILD_ARGS} -o bin/neutree-api ./cmd/neutree-api/neutree-api.go

build-neutree-node-agent:
	$(GO) build ${NODE_AGENT_GO_BUILD_ARGS} -o bin/neutree-node-agent ./cmd/neutree-node-agent/neutree-node-agent.go

# Choice of images to build/push
ALL_DOCKER_BUILD ?= core api db-scripts
CLUSTER_DOCKER_BUILD ?= runtime node-agent

.PHONY: docker-build-all ## Build all the architecture docker images
docker-build-all: $(addprefix docker-build-,$(ALL_ARCH))

docker-build-%:
	$(MAKE) ARCH=$* docker-build

.PHONY: docker-build
docker-build: ## Run docker-build-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-,$(ALL_DOCKER_BUILD))

.PHONY: docker-build-core
docker-build-core: # build core docker image
	docker build --build-arg ARCH=$(ARCH) --build-arg GO_BUILD_ARGS=$(GO_BUILD_ARGS) . -t $(NEUTREE_CORE_IMAGE)-$(ARCH):$(IMAGE_TAG) -f Dockerfile.core

.PHONY: docker-build-api
docker-build-api: # build api docker image
	docker build --build-arg ARCH=$(ARCH) --build-arg DEFAULT_CLUSTER_VERSION=$(CLUSTER_VERSION) --build-arg UI_VERSION=$(UI_VERSION) --build-arg GO_BUILD_ARGS=$(GO_BUILD_ARGS) . -t $(NEUTREE_API_IMAGE)-$(ARCH):$(IMAGE_TAG) -f Dockerfile.api

.PHONY: docker-build-db-scripts
docker-build-db-scripts:
	docker build --build-arg ARCH=$(ARCH) . -t $(NEUTREE_DB_SCRIPTS_IMAGE)-$(ARCH):$(IMAGE_TAG) -f Dockerfile.db-scripts

.PHONY: docker-build-node-agent
docker-build-node-agent:
	docker build --build-arg ARCH=$(ARCH) --build-arg GO_BUILD_ARGS=$(NODE_AGENT_GO_BUILD_ARGS) . -t $(NEUTREE_NODE_AGENT_IMAGE)-$(ARCH):$(IMAGE_TAG) -f Dockerfile.node-agent

.PHONY: docker-build-runtime
docker-build-runtime:
	docker build --build-arg ARCH=$(ARCH) . -t $(NEUTREE_RUNTIME_IMAGE)-$(ARCH):$(IMAGE_TAG) -f Dockerfile.runtime

.PHONY: docker-build-cluster
docker-build-cluster: ## Build cluster-level images such as runtime and node-agent.
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-,$(CLUSTER_DOCKER_BUILD))

.PHONY: docker-push-all ## Push all the architecture docker images
docker-push-all:
	$(MAKE) ALL_ARCH="$(ALL_ARCH)" $(addprefix docker-push-,$(ALL_DOCKER_BUILD))

docker-push-%:
	$(MAKE) ARCH=$* docker-push

.PHONY: docker-push
docker-push: $(addprefix docker-push-,$(ALL_DOCKER_BUILD))

.PHONY: docker-push-core
docker-push-core: # push core docker image
	docker push $(NEUTREE_CORE_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-api
docker-push-api: # push api docker image
	docker push $(NEUTREE_API_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-db-scripts
docker-push-db-scripts: # push db scripts docker image
	docker push $(NEUTREE_DB_SCRIPTS_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-node-agent
docker-push-node-agent:
	docker push $(NEUTREE_NODE_AGENT_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-runtime
docker-push-runtime: # push runtime docker image
	docker push $(NEUTREE_RUNTIME_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-cluster
docker-push-cluster: ## Push cluster-level images such as runtime and node-agent.
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-push-,$(CLUSTER_DOCKER_BUILD))

.PHONY: docker-push-manifest
docker-push-manifest: $(addprefix docker-push-manifest-,$(ALL_DOCKER_BUILD))

.PHONY: docker-push-manifest-core
docker-push-manifest-core: ## Push the core manifest docker image.
	docker buildx imagetools create -t $(NEUTREE_CORE_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_CORE_IMAGE)\-&:$(IMAGE_TAG)~g")

.PHONY: docker-push-manifest-api
docker-push-manifest-api: ## Push the api manifest docker image.
	docker buildx imagetools create -t $(NEUTREE_API_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_API_IMAGE)\-&:$(IMAGE_TAG)~g")

.PHONY: docker-push-manifest-db-scripts
docker-push-manifest-db-scripts: ## Push the db scripts manifest docker image.
	docker buildx imagetools create -t $(NEUTREE_DB_SCRIPTS_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_DB_SCRIPTS_IMAGE)\-&:$(IMAGE_TAG)~g")

.PHONY: docker-push-manifest-node-agent
docker-push-manifest-node-agent: ## Push the node agent manifest docker image.
	docker buildx imagetools create -t $(NEUTREE_NODE_AGENT_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_NODE_AGENT_IMAGE)\-&:$(IMAGE_TAG)~g")

.PHONY: docker-push-manifest-runtime
docker-push-manifest-runtime: ## Push the runtime manifest docker image.
	docker buildx imagetools create -t $(NEUTREE_RUNTIME_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_RUNTIME_IMAGE)\-&:$(IMAGE_TAG)~g")

.PHONY: docker-push-manifest-cluster
docker-push-manifest-cluster: ## Push cluster-level multi-arch manifests.
	$(MAKE) $(addprefix docker-push-manifest-,$(CLUSTER_DOCKER_BUILD))

ENVTEST_ASSETS_DIR=$(shell pwd)/bin

.PHONY: test
test: prepare-build-cli mockgen fmt vet lint ## Run unit test
	go test -coverprofile coverage.out -covermode=atomic $(shell go list ./... | grep -v 'e2e\|mocks\|db/dbtest')

LABEL_FILTER ?=
E2E_TIMEOUT ?= 30m

.PHONY: e2e-test
e2e-test: ## Run E2E tests (requires NEUTREE_SERVER_URL and NEUTREE_API_KEY)
	$(if $(wildcard .env),set -a && source .env && set +a &&) go test -v -timeout 6h ./tests/e2e/... \
		--ginkgo.v --ginkgo.no-color --ginkgo.silence-skips --ginkgo.timeout=$(E2E_TIMEOUT) $(if $(LABEL_FILTER),--ginkgo.label-filter="$(LABEL_FILTER)")

##@ Gateway Lua Testing

GATEWAY_PLUGIN_DIR ?= gateway/kong/plugins/neutree-ai-gateway
GATEWAY_LUA_TEST_IMAGE ?= neutree-gateway-lua-test:latest

.PHONY: gateway-lua-test
gateway-lua-test: ## Run neutree-ai-gateway Lua unit tests (LuaJIT + busted, in Docker)
	docker build -q -t $(GATEWAY_LUA_TEST_IMAGE) $(GATEWAY_PLUGIN_DIR)/spec >/dev/null
	docker run --rm -v $(CURDIR)/$(GATEWAY_PLUGIN_DIR):/plugin -w /plugin $(GATEWAY_LUA_TEST_IMAGE) sh spec/run.sh

##@ Database Testing

.PHONY: db-test
db-test: ## Run database tests with isolated PostgreSQL
	@echo "Starting test database and auth service..."
	@cd db && docker compose -f docker-compose.test.yml up -d postgres auth
	@echo "Waiting for services to be ready..."
	@cd db && docker compose -f docker-compose.test.yml up --wait postgres auth
	@echo "Running migrations..."
	@cd db && docker compose -f docker-compose.test.yml run --rm migration || \
		(docker compose -f docker-compose.test.yml down -v && exit 1)
	@echo "Running seed..."
	@cd db && docker compose -f docker-compose.test.yml run --rm seed || \
		(docker compose -f docker-compose.test.yml down -v && exit 1)
	@echo "Running database tests..."
	@cd db/dbtest && go test -v ./... || (cd .. && docker compose -f docker-compose.test.yml down -v && exit 1)
	@echo "Cleaning up test database..."
	@cd db && docker compose -f docker-compose.test.yml down -v

.PHONY: db-test-clean
db-test-clean: ## Clean up test database
	@cd db && docker compose -f docker-compose.test.yml down -v

.PHONY: clean
clean: ## Clean up
	rm -rf bin
	rm -rf out

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


.PHONY: $(RELEASE_DIR)
$(RELEASE_DIR):
	mkdir -p $(RELEASE_DIR)/

.PHONY: release
release: clean # release files excluding image.
	$(MAKE) $(RELEASE_DIR)
	$(MAKE) release-binaries
	$(MAKE) release-chart

.PHONY: release-binaries
release-binaries: prepare-build-cli ## Build the binaries to publish with a release
	RELEASE_BINARY=neutree-cli-amd64 BUILD_PATH=./cmd/neutree-cli GOOS=linux GOARCH=amd64 $(MAKE) release-binary
	RELEASE_BINARY=neutree-cli-arm64 BUILD_PATH=./cmd/neutree-cli GOOS=linux GOARCH=arm64 $(MAKE) release-binary


.PHONY: release-binary
release-binary:
	docker run \
		--rm \
		-e CGO_ENABLED=0 \
		-e GOOS=$(GOOS) \
		-e GOARCH=$(GOARCH) \
		-e GOCACHE=/tmp/ \
		--user $$(id -u):$$(id -g) \
		-v "$$(pwd):/workspace:z" \
		-w /workspace \
		$(IMAGE_REPO)/golang:$(GO_VERSION) \
		go build $(GO_BUILD_ARGS) \
		-o $(RELEASE_DIR)/$(notdir $(RELEASE_BINARY)) $(BUILD_PATH)

.PHONY: release-chart
release-chart: sync-deploy-manifests ## Build the chart to publish with a release
	@if echo "${VERSION}" | grep -qE '^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$'; then \
		echo "Updating chart version to ${VERSION}"; \
		if [ "$(HOST_OS)" = "darwin" ]; then \
			sed -i '' 's/^version: .*/version: ${VERSION}/' deploy/chart/neutree/Chart.yaml; \
			sed -i '' 's/^appVersion: .*/appVersion: ${VERSION}/' deploy/chart/neutree/Chart.yaml; \
		else \
			sed -i 's/^version: .*/version: ${VERSION}/' deploy/chart/neutree/Chart.yaml; \
			sed -i 's/^appVersion: .*/appVersion: ${VERSION}/' deploy/chart/neutree/Chart.yaml; \
		fi; \
	else \
		echo "Skipping chart version update because VERSION (${VERSION}) is not a valid semver."; \
	fi
	helm package ./deploy/chart/neutree -d $(RELEASE_DIR)

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

.PHONY: docker-test-api
docker-test-api: ## Redeploy local neutree-api for testing
	$(MAKE) build-neutree-api
	docker cp bin/neutree-api neutree-api:/neutree-api
	docker restart neutree-api

.PHONY: docker-test-core
docker-test-core: ## Redeploy local neutree-core for testing
	$(MAKE) build-neutree-core
	docker cp bin/neutree-core neutree-core:/neutree-core
	docker restart neutree-core

.PHONY: docker-test-node-agent
docker-test-node-agent: ## Build local neutree-node-agent binary for testing
	$(MAKE) build-neutree-node-agent

.PHONY: docker-test-db-scripts
docker-test-db-scripts: ## Overwrite db scripts for testing, and restart related services
	docker cp db copy-db-scripts:/
	docker restart copy-db-scripts
	docker restart migration
	docker restart post-migration-hook

VENDIR := $(TOOLS_BIN_DIR)/vendir

vendir: $(VENDIR) # Download vendir if not yet.
$(VENDIR):
	@[ -f $(VENDIR) ] || { \
	set -e ;\
	mkdir -p $(TOOLS_BIN_DIR) ;\
	curl -LO https://github.com/vmware-tanzu/carvel-vendir/releases/download/v0.26.0/vendir-$(HOST_OS)-$(HOST_ARCH) ;\
	mv vendir-$(HOST_OS)-$(HOST_ARCH) $(@) ;\
	chmod a+x $(@) ;\
	}

.PHONY: sync-deploy-manifests
sync-deploy-manifests: vendir ## Sync third-party dependencies using vendir
	$(VENDIR) sync

.PHONY: sync-grafana-dashboards
sync-grafana-dashboards: vendir ## Sync grafana dashboards using vendir
	cd scripts/dashboard  && $(VENDIR) sync && bash sync-grafana-dashboards.sh

##@ Engine Package

ENGINE_PACKAGE_SCRIPT := scripts/builder/build-engine-package.sh
ENGINE_PACKAGE_OUTPUT_DIR ?= dist
ENGINE_BASE_DIR := internal/engine
ENGINE_NAME ?= vllm
ENGINE_VERSION ?= v0.11.2
# ENGINE_DIR_VERSION strips any prerelease/build suffix (e.g. -neutree1, -beta,
# -cu130) from ENGINE_VERSION so schema.json / templates lookups always resolve
# to the upstream version directory under internal/engine/<name>/. The full
# ENGINE_VERSION (with suffix) is still passed to the script via -v so the
# manifest's version field carries the patch identifier.
ENGINE_DIR_VERSION = $(shell echo $(ENGINE_VERSION) | sed 's/-.*//')
ENGINE_IMAGES ?= nvidia_gpu:neutree/engine-vllm:v0.11.2-ray2.53.0
ENGINE_TASKS ?= text-generation,text-embedding,text-rerank
ENGINE_DESCRIPTION ?= $(ENGINE_NAME) inference engine

.PHONY: build-engine-package
build-engine-package: ## Build engine package (configurable via ENGINE_NAME, ENGINE_VERSION, ENGINE_IMAGES, ENGINE_TASKS, ENGINE_DESCRIPTION)
	@mkdir -p $(ENGINE_PACKAGE_OUTPUT_DIR)
	bash $(ENGINE_PACKAGE_SCRIPT) \
		-n $(ENGINE_NAME) \
		-v $(ENGINE_VERSION) \
		-i "$(ENGINE_IMAGES)" \
		-s "$(ENGINE_TASKS)" \
		$(if $(wildcard $(ENGINE_BASE_DIR)/$(ENGINE_NAME)/$(ENGINE_DIR_VERSION)/schema.json),-c $(ENGINE_BASE_DIR)/$(ENGINE_NAME)/$(ENGINE_DIR_VERSION)/schema.json) \
		$(if $(wildcard $(ENGINE_BASE_DIR)/$(ENGINE_NAME)/$(ENGINE_DIR_VERSION)/templates),-t $(ENGINE_BASE_DIR)/$(ENGINE_NAME)/$(ENGINE_DIR_VERSION)/templates) \
		-o $(ENGINE_NAME)-$(ENGINE_VERSION).tar.gz \
		-d "$(ENGINE_DESCRIPTION)"

.PHONY: build-engine-manifest
build-engine-manifest: ## Build engine manifest only (no Docker image export, configurable via ENGINE_NAME, ENGINE_VERSION, ENGINE_IMAGES, ENGINE_TASKS, ENGINE_DESCRIPTION)
	@mkdir -p $(ENGINE_PACKAGE_OUTPUT_DIR)
	bash $(ENGINE_PACKAGE_SCRIPT) --manifest-only \
		-n $(ENGINE_NAME) \
		-v $(ENGINE_VERSION) \
		-i "$(ENGINE_IMAGES)" \
		-s "$(ENGINE_TASKS)" \
		$(if $(wildcard $(ENGINE_BASE_DIR)/$(ENGINE_NAME)/$(ENGINE_DIR_VERSION)/schema.json),-c $(ENGINE_BASE_DIR)/$(ENGINE_NAME)/$(ENGINE_DIR_VERSION)/schema.json) \
		$(if $(wildcard $(ENGINE_BASE_DIR)/$(ENGINE_NAME)/$(ENGINE_DIR_VERSION)/templates),-t $(ENGINE_BASE_DIR)/$(ENGINE_NAME)/$(ENGINE_DIR_VERSION)/templates) \
		-d "$(ENGINE_DESCRIPTION)"

##@ Cluster Package

CLUSTER_PACKAGE_SCRIPT := scripts/builder/build-package.sh
CLUSTER_PACKAGE_SCRIPT_DIR := $(dir $(CLUSTER_PACKAGE_SCRIPT))
CLUSTER_PACKAGE_SCRIPT_NAME := $(notdir $(CLUSTER_PACKAGE_SCRIPT))
CLUSTER_PACKAGE_OUTPUT_DIR ?= dist
CLUSTER_PACKAGE_TYPE ?= k8s
CLUSTER_PACKAGE_ACCELERATOR ?=
CLUSTER_PACKAGE_MIRROR_REGISTRY ?=

.PHONY: build-cluster-package
build-cluster-package: ## Build cluster package (CLUSTER_PACKAGE_TYPE=k8s|ssh, optional CLUSTER_PACKAGE_ACCELERATOR=nvidia_gpu|amd_gpu)
	@mkdir -p $(CLUSTER_PACKAGE_OUTPUT_DIR)
	cd $(CLUSTER_PACKAGE_SCRIPT_DIR) && bash $(CLUSTER_PACKAGE_SCRIPT_NAME) \
		--type cluster \
		--cluster-type $(CLUSTER_PACKAGE_TYPE) \
		--version $(IMAGE_TAG) \
		--arch $(ARCH) \
		--output-dir $(abspath $(CLUSTER_PACKAGE_OUTPUT_DIR)) \
		$(if $(CLUSTER_PACKAGE_ACCELERATOR),--accelerator $(CLUSTER_PACKAGE_ACCELERATOR)) \
		$(if $(CLUSTER_PACKAGE_MIRROR_REGISTRY),--mirror-registry $(CLUSTER_PACKAGE_MIRROR_REGISTRY))

.PHONY: sync-images-list
sync-images-list: ## Sync images list for building package
	bash scripts/builder/sync-controlplane-images.sh --write

.PHONY: check-images-list
check-images-list: ## Check controlplane images list is up to date
	bash scripts/builder/sync-controlplane-images.sh --check
