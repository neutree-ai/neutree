VERSION ?= $(shell git describe --tags --always --dirty)

IMAGE_REPO ?= docker.io
IMAGE_PROJECT ?= neutree
IMAGE_PREFIX ?= ${IMAGE_REPO}/${IMAGE_PROJECT}/
IMAGE_TAG ?= ${shell echo $(VERSION) | awk -F '/' '{print $$NF}'}
NEUTREE_SERVE_IMAGE := $(IMAGE_PREFIX)neutree-serve

ARCH ?= amd64
ACCELERATORS ?= gpu amd-gpu
ALL_ARCH ?= amd64 arm64

RAY_BASE_IMAGE ?= rayproject/ray:2.53.0-py311-cu121
ifeq ($(ARCH), arm64)
RAY_BASE_IMAGE := $(RAY_BASE_IMAGE)-aarch64
endif

RAY_REPO ?= https://github.com/neutree-ai/ray.git
RAY_VERSION ?= ray-2.53.0-neutree
RAY_COMMIT ?= $(shell git ls-remote $(RAY_REPO) $(RAY_VERSION) | cut -f1 | head -c 7)

ROCM_BASE_IMAGE ?= $(NEUTREE_SERVE_IMAGE):rocm-base

.PHONY: docker-build
docker-build: ## Run docker-build-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-,$(ACCELERATORS))

.PHONY: docker-build-gpu
docker-build-gpu: prepare ## Build the GPU image
	docker build --build-arg RAY_BASE_IMAGE=$(RAY_BASE_IMAGE) --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) -f Dockerfile -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG) .

.PHONY: docker-build-amd-gpu
docker-build-amd-gpu: prepare ## Build the AMD GPU image
	docker build --build-arg BASE_IMAGE=$(ROCM_BASE_IMAGE) --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) -f Dockerfile.rocm -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-rocm .

.PHONY: docker-push
docker-push: ## Run docker-push-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-push-,$(ACCELERATORS))

.PHONY: docker-push-gpu
docker-push-gpu: ## Push the GPU image
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-amd-gpu
docker-push-amd-gpu: ## Push the AMD GPU image
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-rocm

.PHONY: docker-push-manifest
docker-push-manifest: $(addprefix docker-push-manifest-,$(ACCELERATORS))

.PHONY: docker-push-manifest-amd-gpu
docker-push-manifest-amd-gpu: ## Push the amd-gpu manifest docker image.
	docker buildx imagetools create -t $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-rocm $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)-rocm~g")

.PHONY: docker-push-manifest-gpu
docker-push-manifest-gpu: ## Push the gpu manifest docker image.
	docker buildx imagetools create -t $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)~g")

.PHONY: prepare
prepare: ## Prepare the build context
	cp -rf ../python/neutree/downloader .