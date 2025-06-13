VERSION ?= $(shell git describe --tags --always --dirty)

IMAGE_REPO ?= docker.io
IMAGE_PROJECT ?= neutree-ai
IMAGE_PREFIX ?= ${IMAGE_REPO}/${IMAGE_PROJECT}/
IMAGE_TAG ?= ${shell echo $(VERSION) | awk -F '/' '{print $$NF}'}
NEUTREE_SERVE_IMAGE := $(IMAGE_PREFIX)neutree-serve

ARCH ?= amd64
ALL_ARCH = amd64 arm64
ALL_ACCELERATORS = gpu npu
ALL_NPU_CHIP = 310p 910p

.PHONY: docker-build
docker-build: ## Run docker-build-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-,$(ALL_ACCELERATORS))

.PHONY: docker-build-gpu
docker-build-gpu: ## Build the GPU image
	docker build -f Dockerfile -t $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-$(ARCH) .

.PHONY: docker-build-npu
docker-build-npu: ## Build the NPU image
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-npu-,$(ALL_NPU_CHIP))

docker-build-npu-310p:
	docker build -f Dockerfile.npu -t $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-npu-ascend310p $(ARCH) --build-arg NPU_CHIP=310p
docker-build-npu-910p:
	docker build -f Dockerfile.npu -t $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-npu-ascend910p $(ARCH) --build-arg NPU_CHIP=910p

.PHONY: docker-push
	docker-push: $(addprefix docker-push-,$(ALL_ACCELERATORS))

.PHONY: docker-push-gpu
docker-push-gpu: ## Push the GPU image
	docker push $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-$(ARCH)

.PHONY: docker-push-npu
docker-push-npu: ## Push the NPU image
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-push-npu-,$(ALL_NPU_CHIP))

docker-push-npu-310p:
	docker push $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-npu-ascend310p-$(ARCH)
docker-push-npu-910p:
	docker push $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-npu-ascend910p-$(ARCH)

.PHONY: docker-push-manifest
docker-push-manifest: $(addprefix docker-push-manifest-,$(ALL_ACCELERATORS))

.PHONY: docker-push-manifest-gpu
docker-push-manifest-gpu: ## Push the core manifest docker image.
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG} ${NEUTREE_SERVE_IMAGE}-$${arch}:${IMAGE_TAG}; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}

.PHONY: docker-push-manifest-npu
docker-push-manifest-npu: ## Push the npu manifest docker image.
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-push-manifest-npu-,$(ALL_NPU_CHIP))

docker-push-manifest-npu-310p:
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-npu-ascend310p $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)\-&:$(IMAGE_TAG)-npu-ascend310p~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-npu ${NEUTREE_SERVE_IMAGE}-npu-ascend310p-$${arch}:${IMAGE_TAG}; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-npu-ascend310p

docker-push-manifest-npu-910p:
	docker manifest create --amend $(NEUTREE_SERVE_IMAGE):$(IMAGE_TAG)-npu-ascend910p $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_SERVE_IMAGE)-&:$(IMAGE_TAG)-npu-ascend910p~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-npu-ascend910p ${NEUTREE_SERVE_IMAGE}-$${arch}:${IMAGE_TAG}-npu-ascend910p; done
	docker manifest push --purge ${NEUTREE_SERVE_IMAGE}:${IMAGE_TAG}-npu-ascend910p