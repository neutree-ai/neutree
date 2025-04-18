FROM rayproject/ray:2.43.0-py312-cu124

# Copy requirements file
COPY requirements.txt .

# Install dependencies
RUN pip install --no-cache-dir -r requirements.txt

# Copy Python files
COPY *.py ./
COPY serve ./serve

USER ray

EXPOSE 8265 6379 8000

CMD ["/bin/bash"]
