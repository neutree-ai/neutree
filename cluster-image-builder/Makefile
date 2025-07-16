VERSION ?= $(shell git describe --tags --always --dirty)

IMAGE_REPO ?= docker.io
IMAGE_PROJECT ?= neutree-ai
IMAGE_PREFIX ?= ${IMAGE_REPO}/${IMAGE_PROJECT}/
IMAGE_TAG ?= ${shell echo $(VERSION) | awk -F '/' '{print $$NF}'}
NEUTREE_SERVE_IMAGE := $(IMAGE_PREFIX)neutree-serve

ARCH ?= amd64
ACCELERATORS ?= gpu amd-gpu xpu
ALL_ARCH ?= amd64 arm64

RAY_BASE_IMAGE ?= rayproject/ray:2.43.0-py311-cu121
ifeq ($(ARCH), arm64)
RAY_BASE_IMAGE := $(RAY_BASE_IMAGE)-aarch64
endif

ROCM_BASE_IMAGE ?= $(NEUTREE_SERVE_IMAGE):rocm-base

XPU_BASE_IMAGE ?= intel/deep-learning-essentials:2025.0.2-0-devel-ubuntu22.04

.PHONY: docker-build
docker-build: ## Run docker-build-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-,$(ACCELERATORS))

.PHONY: docker-build-gpu
docker-build-gpu: ## Build the GPU image
	docker build --build-arg RAY_BASE_IMAGE=$(RAY_BASE_IMAGE) -f Dockerfile -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG) .

.PHONY: docker-build-amd-gpu
docker-build-amd-gpu: ## Build the AMD GPU image
	docker build --build-arg BASE_IMAGE=$(ROCM_BASE_IMAGE) -f Dockerfile.rocm -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-rocm .

.PHONY: docker-build-xpu
docker-build-xpu: ## Build the Intel XPU image
	docker build --build-arg BASE_IMAGE=$(XPU_BASE_IMAGE) -f Dockerfile.xpu -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-xpu .

.PHONY: docker-push
docker-push: ## Run docker-push-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-push-,$(ACCELERATORS))

.PHONY: docker-push-gpu
docker-push-gpu: ## Push the GPU image
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-amd-gpu
docker-push-amd-gpu: ## Push the AMD GPU image
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-rocm

.PHONY: docker-push-xpu
docker-push-xpu: ## Push the Intel XPU image
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-xpu

.PHONY: docker-push-manifest
docker-push-manifest: $(addprefix docker-push-manifest-,$(ACCELERATORS))

.PHONY: docker-push-manifest-amd-gpu
docker-push-manifest-amd-gpu: ## Push the core manifest docker image.
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-rocm $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)-rocm~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-rocm ${NEUTREE_SERVE_IMAGE}-$${arch}:${IMAGE_TAG}-rocm; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-rocm

.PHONY: docker-push-manifest-gpu
docker-push-manifest-gpu: ## Push the core manifest docker image.
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG} ${NEUTREE_SERVE_IMAGE}-$${arch}:${IMAGE_TAG}; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}

.PHONY: docker-push-manifest-xpu
docker-push-manifest-xpu: ## Push the Intel XPU manifest docker image.
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-xpu $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)-xpu~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-xpu ${NEUTREE_SERVE_IMAGE}-$${arch}:${IMAGE_TAG}-xpu; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-xpu