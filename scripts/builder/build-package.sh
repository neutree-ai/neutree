#!/bin/bash

set -e

VERSION="${VERSION:-latest}"
PACKAGE_TYPE=""
CLUSTER_TYPE=""
ACCELERATOR=""
ARCH="${ARCH:-amd64}"
OUTPUT_DIR="./dist"
MIRROR_REGISTRY=""
TEMP_DIR=$(mktemp -d)

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Options:
    --type <TYPE>              Package type: controlplane, cluster
    --version <VERSION>        Version tag (default: latest)
    --arch <ARCH>              Architecture: amd64, arm64 (default: amd64)
    --cluster-type <TYPE>      Cluster type: k8s or ssh (required if type=cluster)
    --accelerator <ACCEL>      Accelerator type: nvidia_gpu, amd_gpu (for ssh cluster)
    --mirror-registry <URL>    Mirror registry URL to pull images from (e.g., registry.example.com)
    --output-dir <DIR>         Output directory (default: ./dist)
    -h, --help                 Show this help message

Examples:
    # Build control plane package for amd64
    $0 --type controlplane --version v1.0.0 --arch amd64

    # Build K8s cluster package for arm64
    $0 --type cluster --cluster-type k8s --version v1.0.0 --arch arm64

    # Build SSH cluster package with NVIDIA for amd64
    $0 --type cluster --cluster-type ssh --accelerator nvidia_gpu --version v1.0.0 --arch amd64

    # Build with mirror registry
    $0 --type controlplane --version v1.0.0 --arch amd64 --mirror-registry registry.example.com
EOF
    exit 1
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --type)
            PACKAGE_TYPE="$2"
            shift 2
            ;;
        --version)
            VERSION="$2"
            shift 2
            ;;
        --arch)
            ARCH="$2"
            shift 2
            ;;
        --cluster-type)
            CLUSTER_TYPE="$2"
            shift 2
            ;;
        --accelerator)
            ACCELERATOR="$2"
            shift 2
            ;;
        --mirror-registry)
            MIRROR_REGISTRY="$2"
            shift 2
            ;;
        --output-dir)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            ;;
        *)
            log_error "Unknown option: $1"
            usage
            ;;
    esac
done

# Validation
if [[ -z "$PACKAGE_TYPE" ]]; then
    log_error "Package type is required"
    usage
fi

# Validate architecture
case "$ARCH" in
    amd64|arm64)
        ;;
    *)
        log_error "Unsupported architecture: $ARCH. Supported: amd64, arm64"
        exit 1
        ;;
esac

# Ensure image list files based on package type
IMAGE_LIST_FILES=()
PACKAGE_NAME=""

case "$PACKAGE_TYPE" in
    controlplane)
        IMAGE_LIST_FILES+=("image-lists/controlplane/images.txt")
        PACKAGE_NAME="neutree-control-plane-${VERSION}-${ARCH}"
        ;;
    cluster)
        if [[ -z "$CLUSTER_TYPE" ]]; then
            log_error "Cluster type is required for cluster package"
            usage
        fi

        case "$CLUSTER_TYPE" in
            k8s)
                IMAGE_LIST_FILES+=("image-lists/cluster/kubernetes/images.txt")
                PACKAGE_NAME="neutree-cluster-k8s-${VERSION}-${ARCH}"
                ;;
            ssh)
                PACKAGE_NAME="neutree-cluster-ssh"

                if [[ -n "$ACCELERATOR" ]]; then
                    IMAGE_LIST_FILES+=("image-lists/cluster/ssh/${ACCELERATOR}-images.txt")
                    PACKAGE_NAME="${PACKAGE_NAME}-${ACCELERATOR}"
                fi
                PACKAGE_NAME="${PACKAGE_NAME}-${VERSION}-${ARCH}"
                ;;
            *)
                log_error "Unknown cluster type: $CLUSTER_TYPE"
                usage
                ;;
        esac
        ;;
    engine)
        log_error "Engine packages should use build-engine-package.sh"
        exit 1
        ;;
    *)
        log_error "Unknown package type: $PACKAGE_TYPE"
        usage
        ;;
esac

log_info "Building package: $PACKAGE_NAME"
log_info "Version: $VERSION"
log_info "Architecture: $ARCH"
log_info "Image list files: ${IMAGE_LIST_FILES[*]}"
if [[ -n "$MIRROR_REGISTRY" ]]; then
    log_info "Mirror registry: $MIRROR_REGISTRY"
fi

# Create package directory
PACKAGE_DIR="${TEMP_DIR}"
mkdir -p "${PACKAGE_DIR}/images"

# Merge image lists
MERGED_IMAGE_LIST="${TEMP_DIR}/images.txt"
> "$MERGED_IMAGE_LIST"

for list_file in "${IMAGE_LIST_FILES[@]}"; do
    if [[ ! -f "$list_file" ]]; then
        log_error "Image list file not found: $list_file"
        exit 1
    fi

    log_info "Processing: $list_file"
    # Process image list
    while IFS= read -r line; do
        # Skip comments and empty lines
        [[ "$line" =~ ^#.*$ ]] && continue
        [[ -z "$line" ]] && continue

        # If the image contains neutree
        if [[ "$line" =~ neutree ]]; then
            # Extract image name and tag
            if [[ "$line" =~ ^([^:]+):(.+)$ ]]; then
                image_name="${BASH_REMATCH[1]}"
                image_tag="${BASH_REMATCH[2]}"

                # Replace "latest" in tag with version
                new_tag="${image_tag//latest/${VERSION}}"
                echo "${image_name}:${new_tag}" >> "$MERGED_IMAGE_LIST"
            elif [[ "$line" =~ ^[^:]+$ ]]; then
                # If no tag, default to version
                echo "${line}:${VERSION}" >> "$MERGED_IMAGE_LIST"
            else
                echo "$line" >> "$MERGED_IMAGE_LIST"
            fi
        else
            # Non-neutree images remain unchanged
            echo "$line" >> "$MERGED_IMAGE_LIST"
        fi
    done < "$list_file"
done


# Deduplicate
sort -u "$MERGED_IMAGE_LIST" -o "$MERGED_IMAGE_LIST"

log_info "Total images to package: $(wc -l < "$MERGED_IMAGE_LIST")"

# Pull and save images
IMAGES_TO_PULL=()
while IFS= read -r image; do
    [[ -z "$image" ]] && continue

    # Determine the actual image address to pull
    pull_image="$image"
    if [[ -n "$MIRROR_REGISTRY" ]]; then
        # Pull from mirror registry
        # Remove original registry (if any)
        if [[ "$image" =~ ^([^/]*[.:][^/]*)/(.+)$ ]]; then
            image_without_registry="${BASH_REMATCH[2]}"
        else
            image_without_registry="$image"
        fi
        pull_image="${MIRROR_REGISTRY}/${image_without_registry}"
        log_info "Pulling image from mirror: $pull_image (original: $image)"
    else
        log_info "Pulling image: $image"
    fi

    # Pull image with specified platform
    if ! docker pull --platform "linux/${ARCH}" "$pull_image"; then
        log_error "Failed to pull image: $pull_image for platform linux/${ARCH}"
        exit 1
    fi

    # If using mirror registry, retag to original image name
    if [[ -n "$MIRROR_REGISTRY" && "$pull_image" != "$image" ]]; then
        log_info "Retagging to original image name: $image"
        if ! docker tag "$pull_image" "$image"; then
            log_error "Failed to tag image: $pull_image -> $image"
            exit 1
        fi
    fi

    IMAGES_TO_PULL+=("$image")
done < "$MERGED_IMAGE_LIST"

log_info "Saving all images to single archive..."
ALL_IMAGES_FILE="${PACKAGE_DIR}/images/all-images.tar"

if ! docker save -o "$ALL_IMAGES_FILE" "${IMAGES_TO_PULL[@]}"; then
    log_error "Failed to save images"
    exit 1
fi

log_info "All images saved successfully"

# Generate manifest.yaml
log_info "Generating manifest..."
cat > "${PACKAGE_DIR}/manifest.yaml" << EOF
manifest_version: "1.0"
metadata:
  version: "${VERSION}"
  author: "Neutree Team"
  created_at: "$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  tags:
    - "${PACKAGE_TYPE}"
    - "${ARCH}"
$(if [[ -n "$CLUSTER_TYPE" ]]; then echo "    - \"${CLUSTER_TYPE}\""; fi)
$(if [[ -n "$ACCELERATOR" ]]; then echo "    - \"${ACCELERATOR}\""; fi)

images:
EOF

# Add image information to manifest
for image in "${IMAGES_TO_PULL[@]}"; do
    IFS=':' read -r image_name image_tag <<< "$image"

    # Get image information
    digest=$(docker inspect --format='{{.Id}}' "$image" 2>/dev/null || echo "")
    size=$(docker inspect --format='{{.Size}}' "$image" 2>/dev/null || echo "0")
    platform="linux/${ARCH}"

    cat >> "${PACKAGE_DIR}/manifest.yaml" << EOF
  - image_name: "${image_name}"
    tag: "${image_tag}"
    image_file: "images/all-images.tar"
    platform: "${platform}"
    size: ${size}
    digest: "${digest}"
EOF
done

log_info "Manifest generated successfully"

# Create README
cat > "${PACKAGE_DIR}/README.md" << EOF
# Neutree ${PACKAGE_TYPE^} Package

Version: ${VERSION}
Architecture: ${ARCH}
Created: $(date -u +"%Y-%m-%d %H:%M:%S UTC")

## Package Contents

- Total Images: $(wc -l < "$MERGED_IMAGE_LIST")
- Package Type: ${PACKAGE_TYPE}
- Architecture: ${ARCH}
$(if [[ -n "$CLUSTER_TYPE" ]]; then echo "- Cluster Type: ${CLUSTER_TYPE}"; fi)
$(if [[ -n "$ACCELERATOR" ]]; then echo "- Accelerator: ${ACCELERATOR}"; fi)

## Import Instructions

\`\`\`bash
neutree package import \\
  --package ${PACKAGE_NAME}.tar.gz \\
  --registry your-registry.com
\`\`\`

## Image List

\`\`\`
$(cat "$MERGED_IMAGE_LIST")
\`\`\`
EOF

# Package
mkdir -p "$OUTPUT_DIR"
PACKAGE_FILE="${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz"

log_info "Creating package: $PACKAGE_FILE"

CURRENT_DIR=$(pwd)

cd "$TEMP_DIR" || exit 1

if command -v pigz &> /dev/null; then
    tar -I "pigz -p 16" -cf "${CURRENT_DIR}/${PACKAGE_FILE}" *
else
    tar -czf "${CURRENT_DIR}/${PACKAGE_FILE}" *
fi

cd "$CURRENT_DIR" || exit 1

# Calculate checksum
log_info "Calculating checksum..."
if command -v md5sum &> /dev/null; then
    md5sum "$PACKAGE_FILE" > "${PACKAGE_FILE}.md5"
else
    md5 "$PACKAGE_FILE" | awk '{print $4}' > "${PACKAGE_FILE}.md5"
fi

# Clean up
rm -rf "$TEMP_DIR"

log_info "Package created successfully: $PACKAGE_FILE"
log_info "Package size: $(du -h "$PACKAGE_FILE" | cut -f1)"
log_info "Checksum: $(cat "${PACKAGE_FILE}.md5")"