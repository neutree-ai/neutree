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
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PROJECT_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
CLUSTER_IMAGE_LIST_ROOT="${PROJECT_ROOT}/scripts/builder/image-lists/cluster"

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
    --version <VERSION>        Version tag (default: latest; required for cluster packages)
    --arch <ARCH>              Architecture: amd64, arm64 (default: amd64)
    --cluster-type <TYPE>      Cluster type: k8s or ssh (required if type=cluster)
    --accelerator <ACCEL>      Accelerator type: nvidia_gpu, amd_gpu (for k8s/ssh cluster; appends <ACCEL>-images.txt)
    --mirror-registry <URL>    Mirror registry URL to pull images from (e.g., registry.example.com)
    --output-dir <DIR>         Output directory (default: ./dist)
    -h, --help                 Show this help message

Examples:
    # Build control plane package for amd64
    $0 --type controlplane --version v1.0.0 --arch amd64

    # Build K8s cluster package for arm64
    $0 --type cluster --cluster-type k8s --version v1.0.0 --arch arm64

    # Build K8s cluster package with NVIDIA for amd64
    $0 --type cluster --cluster-type k8s --accelerator nvidia_gpu --version v1.0.0 --arch amd64

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
        PACKAGE_NAME="neutree-controlplane-${VERSION}-${ARCH}"
        ;;
    cluster)
        if [[ -z "$CLUSTER_TYPE" ]]; then
            log_error "Cluster type is required for cluster package"
            usage
        fi

        if [[ "$VERSION" == "latest" ]]; then
            log_error "Cluster packages require an explicit supported --version"
            exit 1
        fi
        # Profiles use exact semantic versions. Pre-release identifiers remain
        # valid package versions, but no argument may change the generated path.
        if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?(\+[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || [[ "$VERSION" == *..* ]]; then
            log_error "Cluster package version must be a v-prefixed semantic version: $VERSION"
            exit 1
        fi

        case "$ACCELERATOR" in
            ""|nvidia_gpu|amd_gpu)
                ;;
            *)
                log_error "Unsupported accelerator package variant: $ACCELERATOR"
                exit 1
                ;;
        esac

        if ! bash "${SCRIPT_DIR}/sync-cluster-image-lists.sh" --check; then
            log_error "Generated cluster package artifacts are stale"
            exit 1
        fi

        case "$CLUSTER_TYPE" in
            k8s)
                PACKAGE_NAME="neutree-cluster-k8s"
                CLUSTER_LIST_TYPE="kubernetes"
                ;;
            ssh)
                PACKAGE_NAME="neutree-cluster-ssh"
                CLUSTER_LIST_TYPE="ssh"
                ;;
            *)
                log_error "Unknown cluster type: $CLUSTER_TYPE"
                usage
                ;;
        esac

        CLUSTER_GENERATED_DIR="${CLUSTER_IMAGE_LIST_ROOT}/generated/${VERSION}/${CLUSTER_LIST_TYPE}"
        CLUSTER_PROFILE_SOURCE="${CLUSTER_IMAGE_LIST_ROOT}/generated/${VERSION}/cluster-profile.yaml"
        CLUSTER_ADDON_DIR="${CLUSTER_IMAGE_LIST_ROOT}/addons/${CLUSTER_LIST_TYPE}"

        if [[ ! -f "$CLUSTER_PROFILE_SOURCE" ]]; then
            log_error "Unsupported cluster package version: $VERSION"
            log_error "Missing generated profile: $CLUSTER_PROFILE_SOURCE"
            exit 1
        fi
        if [[ ! -f "${CLUSTER_GENERATED_DIR}/images.txt" ]]; then
            log_error "Generated image list not found: ${CLUSTER_GENERATED_DIR}/images.txt"
            exit 1
        fi

        IMAGE_LIST_FILES+=("${CLUSTER_GENERATED_DIR}/images.txt")
        if [[ -f "${CLUSTER_ADDON_DIR}/images.txt" ]]; then
            IMAGE_LIST_FILES+=("${CLUSTER_ADDON_DIR}/images.txt")
        fi

        if [[ -n "$ACCELERATOR" ]]; then
            accelerator_found=false
            if [[ -f "${CLUSTER_GENERATED_DIR}/${ACCELERATOR}-images.txt" ]]; then
                IMAGE_LIST_FILES+=("${CLUSTER_GENERATED_DIR}/${ACCELERATOR}-images.txt")
                accelerator_found=true
            fi
            if [[ -f "${CLUSTER_ADDON_DIR}/${ACCELERATOR}-images.txt" ]]; then
                IMAGE_LIST_FILES+=("${CLUSTER_ADDON_DIR}/${ACCELERATOR}-images.txt")
                accelerator_found=true
            fi
            if [[ "$accelerator_found" != "true" ]]; then
                log_error "Unsupported accelerator package variant: ${CLUSTER_TYPE}/${ACCELERATOR}"
                exit 1
            fi
            PACKAGE_NAME="${PACKAGE_NAME}-${ACCELERATOR}"
        fi
        PACKAGE_NAME="${PACKAGE_NAME}-${VERSION}-${ARCH}"
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
    while IFS= read -r line || [[ -n $line ]]; do
        # Skip comments and empty lines
        [[ "$line" =~ ^#.*$ ]] && continue
        [[ -z "$line" ]] && continue

        # Control-plane inputs intentionally retain latest placeholders so a
        # release package resolves its three Neutree images to --version. The
        # generated ClusterProfile lists already contain exact tags.
        if [[ "$PACKAGE_TYPE" == "controlplane" && "$line" =~ neutree ]]; then
            if [[ "$line" =~ ^([^:]+):(.+)$ ]]; then
                image_name="${BASH_REMATCH[1]}"
                image_tag="${BASH_REMATCH[2]}"
                printf '%s:%s\n' "$image_name" "${image_tag//latest/${VERSION}}" >> "$MERGED_IMAGE_LIST"
            elif [[ "$line" =~ ^[^:]+$ ]]; then
                printf '%s:%s\n' "$line" "$VERSION" >> "$MERGED_IMAGE_LIST"
            else
                printf '%s\n' "$line" >> "$MERGED_IMAGE_LIST"
            fi
        else
            printf '%s\n' "$line" >> "$MERGED_IMAGE_LIST"
        fi
    done < "$list_file"
done

PROFILE_MANIFEST=""
if [[ "$PACKAGE_TYPE" == "cluster" ]]; then
    PROFILE_MANIFEST="${TEMP_DIR}/cluster-profile.yaml"
    log_info "Using generated cluster profile: $CLUSTER_PROFILE_SOURCE"
    cp "$CLUSTER_PROFILE_SOURCE" "$PROFILE_MANIFEST"
fi

# Deduplicate
sort -u "$MERGED_IMAGE_LIST" -o "$MERGED_IMAGE_LIST"

log_info "Total images to package: $(wc -l < "$MERGED_IMAGE_LIST")"

# Pull and save images
IMAGES_TO_PULL=()
while IFS= read -r image || [[ -n $image ]]; do
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

if [[ -n "$PROFILE_MANIFEST" ]]; then
    cat "$PROFILE_MANIFEST" >> "${PACKAGE_DIR}/manifest.yaml"
fi

log_info "Manifest generated successfully"

# Create README
PACKAGE_TYPE_TITLE=$(printf '%s' "$PACKAGE_TYPE" | awk '{ print toupper(substr($0, 1, 1)) substr($0, 2) }')
cat > "${PACKAGE_DIR}/README.md" << EOF
# Neutree ${PACKAGE_TYPE_TITLE} Package

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
PACKAGE_OUTPUT_PATH="$PACKAGE_FILE"
if [[ "$PACKAGE_OUTPUT_PATH" != /* ]]; then
    PACKAGE_OUTPUT_PATH="${CURRENT_DIR}/${PACKAGE_OUTPUT_PATH}"
fi

cd "$TEMP_DIR" || exit 1

if command -v pigz &> /dev/null; then
    tar -I "pigz -p 16" -cf "$PACKAGE_OUTPUT_PATH" *
else
    tar -czf "$PACKAGE_OUTPUT_PATH" *
fi

cd "$CURRENT_DIR" || exit 1

# Calculate checksum
log_info "Calculating checksum..."
if command -v sha256sum &> /dev/null; then
    sha256sum "$PACKAGE_FILE" > "${PACKAGE_FILE}.sha256"
else
    shasum -a 256 "$PACKAGE_FILE" > "${PACKAGE_FILE}.sha256"
fi

# Clean up
rm -rf "$TEMP_DIR"

log_info "Package created successfully: $PACKAGE_FILE"
log_info "Package size: $(du -h "$PACKAGE_FILE" | cut -f1)"
log_info "Checksum: $(cat "${PACKAGE_FILE}.sha256")"
