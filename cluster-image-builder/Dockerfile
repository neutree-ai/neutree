FROM rayproject/ray:2.43.0-py312-cu121

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
