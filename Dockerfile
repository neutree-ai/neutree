# syntax=docker/dockerfile:1.4

# Copyright 2022.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build architecture
ARG ARCH

# Build the manager binary
FROM golang:1.23 as builder
WORKDIR /workspace

# Run this with docker build --build_arg $(go env GOPROXY) to override the goproxy
ARG goproxy=https://goproxy.cn,direct
ENV GOPROXY=$goproxy

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# Cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the sources
COPY ./ ./

# Build
ARG ARCH
ARG GO_BUILD_ARGS

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} \
    go build "${GO_BUILD_ARGS}" \
    -o neutree-core cmd/main.go

FROM --platform=linux/${ARCH} ubuntu:22.04

# Copy requirements file
COPY requirements.txt .

# Install dependencies
RUN apt-get update && apt-get install rsync openssh-client util-linux python3 python3-dev nfs-common python3-pip build-essential libssl-dev libffi-dev -y && pip install -U --no-cache-dir -r requirements.txt

WORKDIR /
COPY --from=builder /workspace/neutree-core .
USER root
ENTRYPOINT ["/neutree-core"]