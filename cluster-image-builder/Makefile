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
ENGINE_VLLM_DIR_VERSION ?= $(shell echo $(ENGINE_VLLM_VERSION) | sed 's/-.*//' | tr '.' '_')

# vLLM CUDA major used for the nixl-cuXX reconcile in Dockerfile.engine-vllm.
#
# vLLM upstream packaging history:
#   < 0.20.0           → CUDA 12.9 base → nixl-cu12 preinstalled
#   0.20.0..0.20.x     → CUDA 13.0 base → nixl-cu13 preinstalled
#   >= 0.21.0          → CUDA 13.0 base → nixl 1.1.0 META-WHEEL preinstalled.
#                        The meta-wheel ships both cu12 and cu13 backends and
#                        selects the right one at runtime from torch.version.cuda.
#                        No reconcile needed → empty value, Dockerfile skips.
#
# Computed from ENGINE_VLLM_VERSION at make parse-time. Overridable for
# non-standard forks or upstream packaging changes:
#     make docker-build-engine-vllm VLLM_CUDA_MAJOR=13
#     make docker-build-engine-vllm VLLM_CUDA_MAJOR=        # explicit skip
#
# For ROCm (Dockerfile.engine-vllm path with rocm base) the reconcile is
# short-circuited by passing TARGET_CUDA="" — no nixl-cuXX wheel for ROCm.
VLLM_CUDA_MAJOR ?= $(shell \
    ver=$$(echo $(ENGINE_VLLM_VERSION) | sed 's/^v//;s/-.*//'); \
    if ! echo "$$ver" | grep -Eq '^[0-9]+\.[0-9]+'; then \
        echo ""; \
    else \
        cmp_020=$$(printf '%s\n0.20.0\n' "$$ver" | sort -V | head -1); \
        cmp_021=$$(printf '0.21.0\n%s\n' "$$ver" | sort -V | head -1); \
        if [ "$$cmp_020" = "$$ver" ] && [ "$$ver" != "0.20.0" ]; then \
            echo 12; \
        elif [ "$$cmp_021" = "0.21.0" ]; then \
            echo ""; \
        else \
            echo 13; \
        fi; \
    fi)

# Optional user-supplied nixl version specifier (PEP 440 form). Empty by
# default — meaning "use the Dockerfile's vLLM-upstream-supported range
# fallback on the reinstall path; do nothing on the skip path". Set to
# something like '==0.10.0' or '>=0.9.0,<0.11.0' to pin a specific
# version when reinstall is in play (TARGET_CUDA non-empty, i.e. vLLM
# < 0.21.0). Has no effect when TARGET_CUDA is empty (vLLM >= 0.21.0 or
# ROCm) because the Dockerfile skips the reinstall RUN entirely.
NIXL_VERSION ?=
# Optional override of the upstream vLLM base image tag (defaults to
# ENGINE_VLLM_VERSION). Use to point at non-standard upstream tags (e.g. dev
# builds) without editing the Makefile; release workflows expose this via the
# `base_image_tag_override` input.
ENGINE_VLLM_BASE_IMAGE_TAG ?= $(ENGINE_VLLM_VERSION)
ENGINE_VLLM_ROCM_BASE_IMAGE_TAG ?= $(ENGINE_VLLM_VERSION)
ENGINE_LLAMA_CPP_VERSION ?= v0.3.7
ENGINE_LLAMA_CPP_DIR_VERSION ?= $(shell echo $(ENGINE_LLAMA_CPP_VERSION) | sed 's/-.*//' | tr '.' '_')
# SGLang base image. ENGINE_SGLANG_VERSION is the default value for both the
# upstream pull tag and the Neutree output image tag prefix; the actual
# variables consumed by the recipes are:
#   - NVIDIA: pull lmsysorg/sglang:<ENGINE_SGLANG_BASE_IMAGE_TAG> and tag the
#     output engine-sglang:<ENGINE_SGLANG_BASE_IMAGE_TAG>-... (both default to
#     ENGINE_SGLANG_VERSION).
#   - ROCm: pull lmsysorg/sglang:<ENGINE_SGLANG_ROCM_TAG> and tag the output
#     engine-sglang:<ENGINE_SGLANG_ROCM_TAG>-... (defaults to
#     <ENGINE_SGLANG_VERSION>-rocm720-mi30x), preserving the variant suffix in
#     the resulting Neutree tag, e.g. v0.5.10-rocm720-mi30x-ray2.53.0.
# Both base-image tags are overridable so release workflows can point at
# MI355x or other variants without editing the Makefile; with overrides set,
# the variant suffix flows through to the Neutree output tag too.
ENGINE_SGLANG_BASE_IMAGE_REPO ?= lmsysorg/sglang
ENGINE_SGLANG_VERSION ?= v0.5.10
ENGINE_SGLANG_BASE_IMAGE_TAG ?= $(ENGINE_SGLANG_VERSION)
ENGINE_SGLANG_ROCM_TAG ?= $(ENGINE_SGLANG_VERSION)-rocm720-mi30x
ENGINE_SGLANG_DIR_VERSION ?= $(shell echo $(ENGINE_SGLANG_VERSION) | sed 's/-.*//' | tr '.' '_')
RAY_SHORT_VERSION ?= ray2.53.0

# Optional Neutree-side patch suffix appended only to the output image tag.
# Empty (default) keeps the upstream tag unchanged. Setting e.g. "1" produces
# tags like "v0.11.2-neutree1-ray2.53.0" while base image, ENGINE_VERSION_DIR,
# and pip install version still resolve to the upstream version.
# Use = (recursive) so the value is re-evaluated each time it is referenced,
# removing any ordering dependency on ENGINE_PATCH_SUFFIX.
ENGINE_PATCH_SUFFIX ?=
ENGINE_PATCH_TAG = $(if $(strip $(ENGINE_PATCH_SUFFIX)),-neutree$(strip $(ENGINE_PATCH_SUFFIX)))

.PHONY: docker-build
docker-build: ## Run docker-build-* targets for all the images
	$(MAKE) ARCH=$(ARCH) $(addprefix docker-build-,$(ACCELERATORS))

.PHONY: docker-build-gpu
docker-build-gpu: prepare ## Build the GPU cluster image
	docker build --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		--label neutree.ai/cluster-version=$(IMAGE_TAG) \
		--label neutree.ai/accelerator-type=nvidia_gpu \
		-f Dockerfile -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG) .

.PHONY: docker-build-amd-gpu
docker-build-amd-gpu: prepare ## Build the AMD GPU cluster image
	docker build --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		--label neutree.ai/cluster-version=$(IMAGE_TAG) \
		--label neutree.ai/accelerator-type=amd_gpu \
		-f Dockerfile.rocm -t $(NEUTREE_SERVE_IMAGE)-$(ARCH):$(IMAGE_TAG)-rocm .

.PHONY: docker-build-engine-vllm
docker-build-engine-vllm: prepare ## Build the vLLM engine image (NVIDIA)
	docker build --build-arg ENGINE_BASE_IMAGE=$(ENGINE_VLLM_BASE_IMAGE_REPO):$(ENGINE_VLLM_BASE_IMAGE_TAG) --build-arg ENGINE_VERSION_DIR=$(ENGINE_VLLM_DIR_VERSION) --build-arg TARGET_CUDA=$(VLLM_CUDA_MAJOR) --build-arg NIXL_VERSION='$(NIXL_VERSION)' --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		-f Dockerfile.engine-vllm -t $(NEUTREE_ENGINE_IMAGE)-vllm:$(ENGINE_VLLM_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) .

.PHONY: docker-build-engine-vllm-rocm
docker-build-engine-vllm-rocm: prepare ## Build the vLLM engine image (ROCm — TARGET_CUDA="" skips nixl-cuXX reconcile)
	docker build --build-arg ENGINE_BASE_IMAGE=$(ENGINE_VLLM_ROCM_BASE_IMAGE_REPO):$(ENGINE_VLLM_ROCM_BASE_IMAGE_TAG) --build-arg ENGINE_VERSION_DIR=$(ENGINE_VLLM_DIR_VERSION) --build-arg TARGET_CUDA="" --build-arg NIXL_VERSION='' --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		-f Dockerfile.engine-vllm -t $(NEUTREE_ENGINE_IMAGE)-vllm-rocm:$(ENGINE_VLLM_ROCM_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) .

.PHONY: docker-build-engine-llama-cpp
docker-build-engine-llama-cpp: prepare ## Build the llama-cpp-python engine image
	docker build --build-arg LLAMA_CPP_VERSION=$(shell echo $(ENGINE_LLAMA_CPP_VERSION) | sed 's/^v//') --build-arg ENGINE_VERSION_DIR=$(ENGINE_LLAMA_CPP_DIR_VERSION) --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		-f Dockerfile.engine-llama-cpp -t $(NEUTREE_ENGINE_IMAGE)-llama-cpp:$(ENGINE_LLAMA_CPP_VERSION)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) .

.PHONY: docker-build-engine-sglang
docker-build-engine-sglang: prepare ## Build the SGLang engine image (NVIDIA)
	docker build --build-arg ENGINE_BASE_IMAGE=$(ENGINE_SGLANG_BASE_IMAGE_REPO):$(ENGINE_SGLANG_BASE_IMAGE_TAG) --build-arg ENGINE_VERSION_DIR=$(ENGINE_SGLANG_DIR_VERSION) --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		-f Dockerfile.engine-sglang -t $(NEUTREE_ENGINE_IMAGE)-sglang:$(ENGINE_SGLANG_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) .

.PHONY: docker-build-engine-sglang-rocm
docker-build-engine-sglang-rocm: prepare ## Build the SGLang engine image (AMD ROCm, MI300x)
	docker build --build-arg ENGINE_BASE_IMAGE=$(ENGINE_SGLANG_BASE_IMAGE_REPO):$(ENGINE_SGLANG_ROCM_TAG) --build-arg ENGINE_VERSION_DIR=$(ENGINE_SGLANG_DIR_VERSION) --build-arg RAY_COMMIT=$(RAY_COMMIT) --build-arg RAY_REPO=$(RAY_REPO) \
		-f Dockerfile.engine-sglang -t $(NEUTREE_ENGINE_IMAGE)-sglang:$(ENGINE_SGLANG_ROCM_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) .

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
	docker push $(NEUTREE_ENGINE_IMAGE)-vllm:$(ENGINE_VLLM_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)

.PHONY: docker-push-engine-vllm-rocm
docker-push-engine-vllm-rocm: ## Push the vLLM engine image (ROCm)
	docker push $(NEUTREE_ENGINE_IMAGE)-vllm-rocm:$(ENGINE_VLLM_ROCM_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)

.PHONY: docker-push-engine-llama-cpp
docker-push-engine-llama-cpp: ## Push the llama-cpp-python engine image
	docker push $(NEUTREE_ENGINE_IMAGE)-llama-cpp:$(ENGINE_LLAMA_CPP_VERSION)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)

.PHONY: docker-push-engine-sglang
docker-push-engine-sglang: ## Push the SGLang engine image (NVIDIA)
	docker push $(NEUTREE_ENGINE_IMAGE)-sglang:$(ENGINE_SGLANG_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)

.PHONY: docker-push-engine-sglang-rocm
docker-push-engine-sglang-rocm: ## Push the SGLang engine image (AMD ROCm, MI300x)
	docker push $(NEUTREE_ENGINE_IMAGE)-sglang:$(ENGINE_SGLANG_ROCM_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)

.PHONY: docker-push-manifest-engine-vllm
docker-push-manifest-engine-vllm: ## Push the vLLM engine manifest image
	docker buildx imagetools create -t $(NEUTREE_ENGINE_IMAGE)-vllm:$(ENGINE_VLLM_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_ENGINE_IMAGE)\-vllm:$(ENGINE_VLLM_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)~g")

.PHONY: docker-push-manifest-engine-vllm-rocm
docker-push-manifest-engine-vllm-rocm: ## Push the vLLM engine manifest image (ROCm)
	docker buildx imagetools create -t $(NEUTREE_ENGINE_IMAGE)-vllm-rocm:$(ENGINE_VLLM_ROCM_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_ENGINE_IMAGE)\-vllm-rocm:$(ENGINE_VLLM_ROCM_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)~g")

.PHONY: docker-push-manifest-engine-llama-cpp
docker-push-manifest-engine-llama-cpp: ## Push the llama-cpp engine manifest image
	docker buildx imagetools create -t $(NEUTREE_ENGINE_IMAGE)-llama-cpp:$(ENGINE_LLAMA_CPP_VERSION)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_ENGINE_IMAGE)\-llama-cpp:$(ENGINE_LLAMA_CPP_VERSION)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)~g")

.PHONY: docker-push-manifest-engine-sglang
docker-push-manifest-engine-sglang: ## Push the SGLang engine manifest image (NVIDIA)
	docker buildx imagetools create -t $(NEUTREE_ENGINE_IMAGE)-sglang:$(ENGINE_SGLANG_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_ENGINE_IMAGE)\-sglang:$(ENGINE_SGLANG_BASE_IMAGE_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)~g")

.PHONY: docker-push-manifest-engine-sglang-rocm
docker-push-manifest-engine-sglang-rocm: ## Push the SGLang engine manifest image (AMD ROCm)
	docker buildx imagetools create -t $(NEUTREE_ENGINE_IMAGE)-sglang:$(ENGINE_SGLANG_ROCM_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(NEUTREE_ENGINE_IMAGE)\-sglang:$(ENGINE_SGLANG_ROCM_TAG)$(ENGINE_PATCH_TAG)-$(RAY_SHORT_VERSION)~g")

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
