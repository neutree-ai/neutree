VERSION ?= $(shell git describe --tags --always --dirty)

IMAGE_REPO ?= docker.io
IMAGE_PROJECT ?= neutree
IMAGE_PREFIX ?= ${IMAGE_REPO}/${IMAGE_PROJECT}/
IMAGE_TAG ?= ${shell echo $(VERSION) | awk -F '/' '{print $$NF}'}
NEUTREE_SERVE_IMAGE := $(IMAGE_PREFIX)neutree-serve
NEUTREE_ENGINE_IMAGE := $(IMAGE_PREFIX)engine

ARCH ?= amd64
ACCELERATORS ?= gpu amd-gpu
ALL_ARCH ?= amd64 arm64

RAY_REPO ?= https://github.com/neutree-ai/ray.git
RAY_VERSION ?= ray-2.53.0-neutree
RAY_COMMIT ?= $(shell git ls-remote $(RAY_REPO) $(RAY_VERSION) | cut -f1 | head -c 7)

# Engine image build args
ENGINE_VLLM_BASE_IMAGE_REPO ?= vllm/vllm-openai
ENGINE_VLLM_ROCM_BASE_IMAGE_REPO ?= vllm/vllm-openai-rocm
ENGINE_VLLM_VERSION ?= v0.11.2
ENGINE_LLAMA_CPP_VERSION ?= 0.3.7
RAY_SHORT_VERSION ?= ray2.53.0

.PHONY: docker-build
docker-build: ## Run docker-build-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-,$(ACCELERATORS))

.PHONY: docker-build-gpu
docker-build-gpu: prepare ## Build the GPU cluster image
	docker build --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) -f Dockerfile -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG) .

.PHONY: docker-build-amd-gpu
docker-build-amd-gpu: prepare ## Build the AMD GPU cluster image
	docker build --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) -f Dockerfile.rocm -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-rocm .

.PHONY: docker-build-engine-vllm
docker-build-engine-vllm: prepare ## Build the vLLM engine image (NVIDIA)
	docker build --build-arg ENGINE_BASE_IMAGE=$(ENGINE_VLLM_BASE_IMAGE_REPO):$(ENGINE_VLLM_VERSION) --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		-f Dockerfile.engine-vllm -t $(NEUTREE_ENGINE_IMAGE)-vllm:$(ENGINE_VLLM_VERSION)-$(RAY_SHORT_VERSION) .

.PHONY: docker-build-engine-vllm-rocm
docker-build-engine-vllm-rocm: prepare ## Build the vLLM engine image (ROCm)
	docker build --build-arg ENGINE_BASE_IMAGE=$(ENGINE_VLLM_ROCM_BASE_IMAGE_REPO):$(ENGINE_VLLM_VERSION) --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		-f Dockerfile.engine-vllm -t $(NEUTREE_ENGINE_IMAGE)-vllm-rocm:$(ENGINE_VLLM_VERSION)-$(RAY_SHORT_VERSION) .

.PHONY: docker-build-engine-llama-cpp
docker-build-engine-llama-cpp: prepare ## Build the llama-cpp-python engine image
	docker build --build-arg LLAMA_CPP_VERSION=$(ENGINE_LLAMA_CPP_VERSION) --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		-f Dockerfile.engine-llama-cpp -t $(NEUTREE_ENGINE_IMAGE)-llama-cpp:v$(ENGINE_LLAMA_CPP_VERSION)-$(RAY_SHORT_VERSION) .

.PHONY: docker-push
docker-push: ## Run docker-push-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-push-,$(ACCELERATORS))

.PHONY: docker-push-gpu
docker-push-gpu: ## Push the GPU image
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)

.PHONY: docker-push-amd-gpu
docker-push-amd-gpu: ## Push the AMD GPU image
	docker push $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-rocm

.PHONY: docker-push-engine-vllm
docker-push-engine-vllm: ## Push the vLLM engine image (NVIDIA)
	docker push $(NEUTREE_ENGINE_IMAGE)-vllm:$(ENGINE_VLLM_VERSION)-$(RAY_SHORT_VERSION)

.PHONY: docker-push-engine-vllm-rocm
docker-push-engine-vllm-rocm: ## Push the vLLM engine image (ROCm)
	docker push $(NEUTREE_ENGINE_IMAGE)-vllm-rocm:$(ENGINE_VLLM_VERSION)-$(RAY_SHORT_VERSION)

.PHONY: docker-push-engine-llama-cpp
docker-push-engine-llama-cpp: ## Push the llama-cpp-python engine image
	docker push $(NEUTREE_ENGINE_IMAGE)-llama-cpp:v$(ENGINE_LLAMA_CPP_VERSION)-$(RAY_SHORT_VERSION)

.PHONY: docker-push-manifest-engine-vllm
docker-push-manifest-engine-vllm: ## Push the vLLM engine manifest image
	docker buildx imagetools create -t $(NEUTREE_ENGINE_IMAGE)-vllm:$(ENGINE_VLLM_VERSION)-$(RAY_SHORT_VERSION) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_ENGINE_IMAGE)\-vllm:$(ENGINE_VLLM_VERSION)-$(RAY_SHORT_VERSION)~g")

.PHONY: docker-push-manifest-engine-llama-cpp
docker-push-manifest-engine-llama-cpp: ## Push the llama-cpp engine manifest image
	docker buildx imagetools create -t $(NEUTREE_ENGINE_IMAGE)-llama-cpp:v$(ENGINE_LLAMA_CPP_VERSION)-$(RAY_SHORT_VERSION) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_ENGINE_IMAGE)\-llama-cpp:v$(ENGINE_LLAMA_CPP_VERSION)-$(RAY_SHORT_VERSION)~g")

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
