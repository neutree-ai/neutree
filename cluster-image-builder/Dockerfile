ARG RAY_BASE_IMAGE
ARG RAY_COMMIT
ARG COMMON_WORKDIR=/app
ARG RAY_REPO
ARG RAY_BUILD_BASE_IMAGE="quay.io/pypa/manylinux2014_x86_64:2024-07-02-9ac04ee"

FROM alpine/git:v2.47.2 as ray_fetch
ARG RAY_REPO
ARG RAY_COMMIT
ARG COMMON_WORKDIR
WORKDIR ${COMMON_WORKDIR}
RUN git clone ${RAY_REPO} \
	    && cd ray \
	    && git checkout ${RAY_COMMIT}

FROM ${RAY_BUILD_BASE_IMAGE} AS build_ray
ARG COMMON_WORKDIR
ARG RAY_COMMIT
ENV BUILDKITE_COMMIT=${RAY_COMMIT}
ENV TRAVIS_COMMIT=${BUILDKITE_COMMIT}
ENV BUILD_ONE_PYTHON_ONLY=py311
ENV RAY_DISABLE_EXTRA_CPP=1
WORKDIR /ray
COPY --from=ray_fetch ${COMMON_WORKDIR}/ray /ray
RUN /ray/python/build-wheel-manylinux2014.sh

FROM ${RAY_BASE_IMAGE}

# Install NFS dependencies
USER root
RUN apt-get update && apt-get install util-linux nfs-common -y

USER ray

# Install ray
RUN --mount=type=bind,from=build_ray,src=/ray,target=/install \
    cd /install \
    && pip install -U -r python/requirements.txt \
    && pip uninstall -y ray \
    && pip install .whl/*.whl

# Install dependencies
RUN --mount=type=bind,src=requirements,target=requirements \
    pip install --no-cache-dir -r requirements/common.txt && \
    if [ $(uname -m) = "aarch64" ]; then \
        pip install --no-cache-dir -r requirements/cuda_arm.txt; \
    else \
        pip install --no-cache-dir -r requirements/cuda.txt; \
    fi

# Copy Python files
COPY *.py ./
COPY serve ./serve
COPY accelerator ./accelerator
COPY downloader ./downloader

EXPOSE 8265 6379 8000

CMD ["/bin/bash"]