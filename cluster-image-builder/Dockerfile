ARG RAY_BASE_IMAGE
FROM ${RAY_BASE_IMAGE}

# Install NFS dependencies
USER root
RUN apt-get update && apt-get install util-linux nfs-common -y


USER ray

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

EXPOSE 8265 6379 8000

CMD ["/bin/bash"]