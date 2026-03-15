ARG RAY_COMMIT
ARG COMMON_WORKDIR=/app
ARG RAY_REPO
ARG RAY_BUILD_BASE_IMAGE="quay.io/pypa/manylinux2014_x86_64:2024-07-02-9ac04ee"
ARG PYTHON_VERSION=3.12

# --- Ray source fetch ---
FROM alpine/git:v2.47.2 AS ray_fetch
ARG RAY_REPO
ARG RAY_COMMIT
ARG COMMON_WORKDIR
WORKDIR ${COMMON_WORKDIR}
RUN git clone ${RAY_REPO} \
	    && cd ray \
	    && git checkout ${RAY_COMMIT}

# --- Ray wheel build ---
FROM ${RAY_BUILD_BASE_IMAGE} AS build_ray
ARG COMMON_WORKDIR
ARG RAY_COMMIT
ENV BUILDKITE_COMMIT=${RAY_COMMIT}
ENV TRAVIS_COMMIT=${BUILDKITE_COMMIT}
ENV BUILD_ONE_PYTHON_ONLY=py312
ENV RAY_DISABLE_EXTRA_CPP=1
WORKDIR /ray
COPY --from=ray_fetch ${COMMON_WORKDIR}/ray /ray
RUN /ray/python/build-wheel-manylinux2014.sh

# --- Final image ---
FROM ubuntu:22.04

# Disable OTEL metrics backend due to metrics loss issue, fall back to OpenCensus
ENV RAY_enable_open_telemetry=false

ENV DEBIAN_FRONTEND=noninteractive
ENV HOME=/home/ray
ARG PYTHON_VERSION

# System packages + Python 3.12 (via deadsnakes PPA)
RUN apt-get update -y \
    && apt-get install -y --no-install-recommends \
    curl ca-certificates software-properties-common gnupg \
    sudo tzdata git wget \
    rsync openssh-client netbase \
    tmux screen \
    libjemalloc-dev \
    util-linux nfs-common \
    docker.io podman \
    && for i in 1 2 3; do \
        add-apt-repository -y ppa:deadsnakes/ppa && break || \
        { echo "Attempt $i failed, retrying in 5s..."; sleep 5; }; \
    done \
    && apt-get update -y \
    && apt-get install -y --no-install-recommends \
    python${PYTHON_VERSION} python${PYTHON_VERSION}-dev python${PYTHON_VERSION}-venv \
    && rm -rf /var/lib/apt/lists/* \
    && update-alternatives --install /usr/bin/python3 python3 /usr/bin/python${PYTHON_VERSION} 1 \
    && update-alternatives --set python3 /usr/bin/python${PYTHON_VERSION} \
    && ln -sf /usr/bin/python3 /usr/bin/python \
    && curl -sS https://bootstrap.pypa.io/get-pip.py | python${PYTHON_VERSION} \
    && python3 -m pip install uv \
    && python3 --version && python3 -m pip --version

RUN mkdir -p ${HOME} && touch ${HOME}/.bashrc
WORKDIR ${HOME}

# Install Ray from source
RUN --mount=type=bind,from=build_ray,src=/ray,target=/install \
    cd /install \
    && uv pip install --system -r python/requirements.txt \
    && uv pip install --system .whl/*.whl

# Relax Python patch version check (cluster 3.12.x vs engine 3.12.x)
ENV RAY_DEFAULT_PYTHON_VERSION_MATCH_LEVEL=minor
# Disable OTEL metrics backend due to metrics loss issue, fall back to OpenCensus
ENV RAY_enable_open_telemetry=false

# Copy GPU detection + Ray start scripts (no engine code)
COPY start.py ./
COPY accelerator ./accelerator

EXPOSE 8265 6379 8000

CMD ["/bin/bash"]
