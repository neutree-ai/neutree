VERSION ?= $(shell git describe --tags --always --dirty)

IMAGE_REPO ?= docker.io
IMAGE_PROJECT ?= neutree-ai
IMAGE_PREFIX ?= ${IMAGE_REPO}/${IMAGE_PROJECT}/
IMAGE_TAG ?= ${shell echo $(VERSION) | awk -F '/' '{print $$NF}'}
NEUTREE_SERVE_IMAGE := $(IMAGE_PREFIX)neutree-serve

ARCH ?= amd64
ACCELERATORS ?= gpu npu
ALL_ARCH ?= amd64 arm64
ALL_NPU_CHIP = 310p

RAY_BASE_IMAGE ?= rayproject/ray:2.43.0-py311-cu121
ifeq ($(ARCH), arm64)
RAY_BASE_IMAGE := $(RAY_BASE_IMAGE)-aarch64
endif

.PHONY: docker-build
docker-build: ## Run docker-build-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-,$(ACCELERATORS))

.PHONY: docker-build-gpu
docker-build-gpu: ## Build the GPU image
	docker build --build-arg RAY_BASE_IMAGE=$(RAY_BASE_IMAGE) -f Dockerfile -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG) .

.PHONY: docker-build-npu
docker-build-npu: ## Build the NPU image
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-npu-,$(ALL_NPU_CHIP))

docker-build-npu-310p:
	docker build --build-arg CANN_CHIP=310p -f Dockerfile.npu -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-npu-ascend310p .
docker-build-npu-910b:
	docker build --build-arg CANN_CHIP=910b -f Dockerfile.npu -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-npu-ascend910b .

.PHONY: docker-push
docker-push: ## Run docker-push-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-push-,$(ACCELERATORS))

.PHONY: docker-push-gpu
docker-push-gpu: ## Push the GPU image
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-npu
docker-push-npu: ## Push the NPU image
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-push-npu-,$(ALL_NPU_CHIP))

docker-push-npu-310p:
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-npu-ascend310p
docker-push-npu-910b:
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-npu-ascend910b

.PHONY: docker-push-manifest
docker-push-manifest: $(addprefix docker-push-manifest-,$(ACCELERATORS))

.PHONY: docker-push-manifest-gpu
docker-push-manifest-gpu: ## Push the core manifest docker image.
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG} ${NEUTREE_SERVE_IMAGE}-$${arch}:${IMAGE_TAG}; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}

.PHONY: docker-push-manifest-npu
docker-push-manifest-npu: ## Push the npu manifest docker image.
	$(MAKE) $(addprefix docker-push-manifest-npu-,$(ALL_NPU_CHIP))

docker-push-manifest-npu-310p:
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-npu-ascend310p $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)-npu-ascend310p~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-npu-ascend310p ${NEUTREE_SERVE_IMAGE}-$${arch}:${IMAGE_TAG}-npu-ascend310p; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-npu-ascend310p

docker-push-manifest-npu-910b:
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-npu-ascend910b $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)-&:$(IMAGE_TAG)-npu-ascend910b~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-npu-ascend910b ${NEUTREE_SERVE_IMAGE}-$${arch}:${IMAGE_TAG}-npu-ascend910b; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-npu-ascend910b