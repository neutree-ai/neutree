ARG RAY_BASE_IMAGE
FROM ${RAY_BASE_IMAGE}

# Install NFS dependencies
USER root
RUN apt-get update && apt-get install util-linux nfs-common -y


USER ray
# Copy requirements file
COPY requirements.txt .

# Install dependencies
RUN pip install --no-cache-dir -r requirements.txt

# Copy Python files
COPY *.py ./
COPY serve ./serve

EXPOSE 8265 6379 8000

CMD ["/bin/bash"]
