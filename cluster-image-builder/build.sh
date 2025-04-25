#!/bin/bash

# Exit on error
set -e

# Define variables
IMAGE_NAME="neutree-serve"
TAG="v15"

# Parse command line arguments
while [[ "$#" -gt 0 ]]; do
  case $1 in
  -t | --tag)
    TAG="$2"
    shift
    ;;
  -h | --help)
    echo "Usage: $0 [-t|--tag TAG]"
    echo "Build Docker image for neutree-serve"
    exit 0
    ;;
  *)
    echo "Unknown parameter: $1"
    exit 1
    ;;
  esac
  shift
done

echo "Building $IMAGE_NAME:$TAG..."

# Build the Docker image
docker build \
  --tag "$IMAGE_NAME:$TAG" \
  --file "$(dirname "$0")/Dockerfile" \
  "$(dirname "$0")"

echo "Successfully built $IMAGE_NAME:$TAG"
