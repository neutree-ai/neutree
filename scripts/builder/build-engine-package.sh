#!/bin/bash

# Engine Version Package Builder
# This script helps build engine version packages for Neutree

set -e

VERSION=$(git describe --tags --always --dirty)
OUTPUT_DIR="${OUTPUT_DIR:-./dist}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Functions
print_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

normalize_registry() {
    local registry="$1"
    registry="${registry#http://}"
    registry="${registry#https://}"
    registry="${registry%/}"
    echo "$registry"
}

strip_image_registry() {
    local image="$1"

    if [[ "$image" =~ ^((localhost)|([^/]*[.:][^/]*))/(.+)$ ]]; then
        echo "${BASH_REMATCH[4]}"
    else
        echo "$image"
    fi
}

parse_image_spec() {
    local spec="$1"
    local image_with_tag

    if [[ "$spec" != *:* ]]; then
        print_error "Invalid image specification '$spec': expected accelerator:image:tag"
        exit 1
    fi

    ACCELERATOR="${spec%%:*}"
    image_with_tag="${spec#*:}"
    IMAGE_NAME="${image_with_tag%:*}"
    IMAGE_TAG="${image_with_tag##*:}"

    if [ -z "$ACCELERATOR" ] || [ -z "$IMAGE_NAME" ] || [ -z "$IMAGE_TAG" ] || [ "$IMAGE_NAME" = "$image_with_tag" ] || [[ ! "$IMAGE_TAG" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
        print_error "Invalid image specification '$spec': expected accelerator:image:tag"
        exit 1
    fi
}

# Function to read and format deploy template
read_deploy_template() {
    local template_file="$1"
    local indent_level="$2"  # Number of spaces to indent

    if [ ! -f "$template_file" ]; then
        print_error "Template file not found: $template_file"
        return 1
    fi

    # Read the template and indent it properly for YAML
    # Use || [ -n "$line" ] to handle files without trailing newline
    local indent_spaces=$(printf "%${indent_level}s" "")
    while IFS= read -r line || [ -n "$line" ]; do
        echo "${indent_spaces}${line}"
    done < "$template_file"
}

# Function to validate template syntax
validate_template() {
    local template_file="$1"

    # Check for unmatched if/end tags
    local if_count=$(grep -o '{{-\? *if' "$template_file" 2>/dev/null | wc -l)
    local range_count=$(grep -o '{{-\? *range' "$template_file" 2>/dev/null | wc -l)
    local end_count=$(grep -o '{{-\? *end *}}' "$template_file" 2>/dev/null | wc -l)

    local expected_ends=$((if_count + range_count))

    if [ "$expected_ends" -ne "$end_count" ]; then
        print_error "Template has unmatched tags in $(basename "$template_file")"
        print_error "  Found $if_count 'if' tags"
        print_error "  Found $range_count 'range' tags"
        print_error "  Found $end_count 'end' tags (expected $expected_ends)"
        return 1
    fi

    return 0
}

# Function to scan template directory and generate deploy_template section
scan_and_generate_deploy_templates() {
    local template_base_dir="$1"

    if [ ! -d "$template_base_dir" ]; then
        print_warn "Template directory not found: $template_base_dir" >&2
        return 1
    fi

    local deploy_sections=""
    local first_cluster_type=true

    # Scan for cluster type directories (e.g., kubernetes, ssh)
    for cluster_type_dir in "$template_base_dir"/*; do
        if [ ! -d "$cluster_type_dir" ]; then
            continue
        fi

        local cluster_type=$(basename "$cluster_type_dir")
        print_info "Found cluster type: $cluster_type" >&2

        # Add separator for readability (except for the first one)
        if [ "$first_cluster_type" = false ]; then
            deploy_sections="${deploy_sections}\n"
        fi
        first_cluster_type=false

        deploy_sections="${deploy_sections}      ${cluster_type}:"

        # Scan for deployment type files (e.g., default.yaml, custom.yaml)
        local first_deploy_type=true
        for template_file in "$cluster_type_dir"/*.yaml "$cluster_type_dir"/*.yml; do
            if [ ! -f "$template_file" ]; then
                continue
            fi

            local filename=$(basename "$template_file")
            local deploy_type="${filename%.*}"  # Remove extension
            print_info "  Found deployment type: $deploy_type" >&2

            # Validate template syntax before processing
            if ! validate_template "$template_file"; then
                print_error "  Skipping invalid template: $filename" >&2
                continue
            fi

            # Read template content and encode it with Base64 to avoid JSON escaping issues
            local template_raw=$(cat "$template_file")
            # Use -w 0 on Linux, macOS base64 doesn't support -w but doesn't wrap by default
            if base64 --help 2>&1 | grep -q -- '-w'; then
                local template_encoded=$(echo -n "$template_raw" | base64 -w 0)
            else
                local template_encoded=$(echo -n "$template_raw" | base64)
            fi

            deploy_sections="${deploy_sections}
        ${deploy_type}: \"${template_encoded}\""
        done
    done

    if [ -z "$deploy_sections" ]; then
        return 1
    fi

    echo -e "$deploy_sections"
    return 0
}

show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Build an engine version package for Neutree.

Options:
    -n, --name NAME           Engine name (e.g., vllm, llama-cpp)
    -v, --version VERSION     Engine version (e.g., v0.5.0)
    -i, --images IMAGES       Comma-separated list of image specifications
                              Format: accelerator:image:tag
                              Example: nvidia_gpu:neutree/vllm-cuda:v0.5.0,amd_gpu:neutree/vllm-rocm:v0.5.0
                              For CPU-only engines: cpu:neutree/llama-cpp:v1.0.0
    -s, --supported-tasks TASKS
                              Comma-separated list of supported tasks
                              Example: generate,embedding
    --metrics-export BOOL     Declare whether this engine version exports Prometheus
                              metrics (true|false). Omit to leave it undeclared,
                              which keeps the legacy behaviour of scraping it.
    --metrics-export-port PORT
                              Metrics port, when it is not the default 8000.
                              Only valid together with --metrics-export true.
    --metrics-export-path PATH
                              Metrics path, when it is not the default /metrics.
                              Only valid together with --metrics-export true.
    --playground BOOL         Declare whether this engine version can back the
                              console Playground (true|false). Omit to leave it
                              undeclared, which keeps the tab visible.
    --playground-modes MODES  Comma-separated interaction modes the Playground can
                              render: chat, embedding, rerank. Only valid together
                              with --playground true. Omit to let the console infer
                              the surface from the endpoint's model task.
    -m, --manifest FILE       Path to manifest template file (optional)
    -t, --template-dir DIR    Path to template directory containing kubernetes/default.yaml
    -c, --schema FILE         Path to engine_schema.json file (optional)
    -o, --output FILE         Output package file path (default: ENGINE-VERSION-<arch>.tar.gz)
    -d, --description TEXT    Engine version description
    -p, --platform PLATFORM   Image platform used for pull and manifest (default: linux/amd64)
    --mirror-registry URL     Mirror registry for missing images (e.g., registry.example.com)
    --manifest-only           Generate only the manifest file (skip Docker image export and packaging)
    -h, --help                Show this help message

Examples:
    # Build vLLM package with CUDA and ROCm images for text generation
    $0 -n vllm -v v0.5.0 \\
        -i "nvidia-gpu:neutree/vllm-cuda:v0.5.0,amd-gpu:neutree/vllm-rocm:v0.5.0" \\
        -s "generate" \\
        -d "vLLM engine with multi-GPU support"

    # Build CPU-only engine (e.g., llama.cpp) with CPU accelerator
    $0 -n llama-cpp -v v1.0.0 \\
        -i "cpu:neutree/llama-cpp:v1.0.0" \\
        -s "generate" \\
        -d "LLaMA.cpp CPU inference engine"

    # Build embedding engine with multiple tasks
    $0 -n sentence-transformers -v v1.0.0 \\
        -i "cpu:neutree/embedding:v1.0.0,nvidia-gpu:neutree/embedding-cuda:v1.0.0" \\
        -s "embedding,rerank" \\
        -d "Sentence Transformers for embedding and reranking"

    # Build from manifest template
    $0 -n llama-cpp -v v1.0.0 \\
        -i "cpu:neutree/llama-cpp:v1.0.0" \\
        -s "generate" \\
        -m manifest-template.yaml

    # Build with template directory
    $0 -n vllm -v v0.5.0 \\
        -i "nvidia-gpu:neutree/vllm-cuda:v0.5.0" \\
        -s "text-generation" \\
        -t ./template \\
        -d "vLLM engine with custom template"

    # Build with engine schema
    $0 -n vllm -v v0.5.0 \\
        -i "nvidia-gpu:neutree/vllm-cuda:v0.5.0" \\
        -s "generate" \\
        -c ./engine_schema.json \\
        -d "vLLM engine with values schema"

    # Generate only the manifest file (no Docker image export or packaging)
    $0 --manifest-only -n vllm -v v0.5.0 \\
        -i "nvidia-gpu:neutree/vllm-cuda:v0.5.0" \\
        -s "generate" \\
        -t ./template \\
        -d "vLLM engine manifest only"

    # Declare capabilities: metrics on the default endpoint, chat-only Playground
    $0 -n my-engine -v v1.0.0 \\
        -i "nvidia_gpu:my-registry/my-engine:v1.0.0" \\
        -s "text-generation" \\
        --metrics-export true \\
        --playground true --playground-modes "chat"

    # An engine that serves no metrics endpoint, but can still back a chat
    # Playground even though it advertises no text-generation task
    $0 -n my-engine -v v1.0.0 \\
        -i "nvidia_gpu:my-registry/my-engine:v1.0.0" \\
        --metrics-export false \\
        --playground true --playground-modes "chat"

    # Metrics served somewhere other than :8000/metrics
    $0 -n my-engine -v v1.0.0 \\
        -i "cpu:my-registry/my-engine:v1.0.0" \\
        --metrics-export true --metrics-export-port 9100 --metrics-export-path /internal/metrics

EOF
}

# Parse arguments
ENGINE_NAME=""
ENGINE_VERSION=""
IMAGES=""
SUPPORTED_TASKS=""
MANIFEST_TEMPLATE=""
TEMPLATE_DIR=""
SCHEMA_FILE=""
OUTPUT_FILE=""
DESCRIPTION=""
PLATFORM="linux/amd64"
MIRROR_REGISTRY=""
MANIFEST_ONLY=""
# Capability declarations. Empty means "undeclared", which is not the same as
# declaring false: an engine version with no capabilities block keeps the
# behaviour Neutree had before the protocol existed (scraped for metrics,
# Playground shown). Never default these to a value.
METRICS_EXPORT=""
METRICS_EXPORT_PORT=""
METRICS_EXPORT_PATH=""
PLAYGROUND=""
PLAYGROUND_MODES=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -n|--name)
            ENGINE_NAME="$2"
            shift 2
            ;;
        -v|--version)
            ENGINE_VERSION="$2"
            shift 2
            ;;
        -i|--images)
            IMAGES="$2"
            shift 2
            ;;
        -s|--supported-tasks)
            SUPPORTED_TASKS="$2"
            shift 2
            ;;
        --metrics-export)
            METRICS_EXPORT="$2"
            shift 2
            ;;
        --metrics-export-port)
            METRICS_EXPORT_PORT="$2"
            shift 2
            ;;
        --metrics-export-path)
            METRICS_EXPORT_PATH="$2"
            shift 2
            ;;
        --playground)
            PLAYGROUND="$2"
            shift 2
            ;;
        --playground-modes)
            PLAYGROUND_MODES="$2"
            shift 2
            ;;
        -m|--manifest)
            MANIFEST_TEMPLATE="$2"
            shift 2
            ;;
        -t|--template-dir)
            TEMPLATE_DIR="$2"
            shift 2
            ;;
        -c|--schema)
            SCHEMA_FILE="$2"
            shift 2
            ;;
        -o|--output)
            OUTPUT_FILE="$2"
            shift 2
            ;;
        -d|--description)
            DESCRIPTION="$2"
            shift 2
            ;;
        -p|--platform)
            if [[ $# -lt 2 || -z "$2" || "$2" == -* ]]; then
                print_error "$1 requires a value"
                exit 1
            fi
            PLATFORM="$2"
            shift 2
            ;;
        --mirror-registry)
            MIRROR_REGISTRY="$2"
            shift 2
            ;;
        --manifest-only)
            MANIFEST_ONLY="true"
            shift
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# Validate required arguments
if [ -z "$ENGINE_NAME" ]; then
    print_error "Engine name is required"
    show_usage
    exit 1
fi

if [ -z "$ENGINE_VERSION" ]; then
    print_error "Engine version is required"
    show_usage
    exit 1
fi

if [ -z "$IMAGES" ]; then
    print_error "Images list is required"
    print_error "For CPU-only engines, use: cpu:image:tag"
    print_error "Example: -i \"cpu:neutree/llama-cpp:v1.0.0\""
    show_usage
    exit 1
fi

# Set default output file
if [ -z "$OUTPUT_FILE" ]; then
    PACKAGE_ARCH="${PLATFORM##*/}"
    OUTPUT_FILE="${ENGINE_NAME}-${ENGINE_VERSION}-${PACKAGE_ARCH}.tar.gz"
fi

# Create temporary directory
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

PACKAGE_DIR="$TEMP_DIR/package"
IMAGES_DIR="$PACKAGE_DIR/images"
mkdir -p "$IMAGES_DIR"

print_info "Building engine version package: $ENGINE_NAME $ENGINE_VERSION"
print_info "Temporary directory: $TEMP_DIR"
if [ -n "$MIRROR_REGISTRY" ]; then
    MIRROR_REGISTRY=$(normalize_registry "$MIRROR_REGISTRY")
    print_info "Mirror registry: $MIRROR_REGISTRY"
fi

IFS=',' read -ra IMAGE_SPECS <<< "$IMAGES"
IMAGE_ENTRIES=""

# Validate image specifications up front so manifest-only mode enforces the
# same accelerator:image:tag contract as packaged builds.
for spec in "${IMAGE_SPECS[@]}"; do
    parse_image_spec "$spec"
done

if [ -z "$MANIFEST_ONLY" ]; then
    # Export Docker images
    print_info "Exporting Docker images..."

    # Pull the selected platform before collecting images for export
    ALL_IMAGES=()
    for spec in "${IMAGE_SPECS[@]}"; do
        parse_image_spec "$spec"

        FULL_IMAGE="$IMAGE_NAME:$IMAGE_TAG"

        if [ -n "$MIRROR_REGISTRY" ]; then
            IMAGE_WITHOUT_REGISTRY=$(strip_image_registry "$FULL_IMAGE")
            MIRROR_IMAGE="${MIRROR_REGISTRY}/${IMAGE_WITHOUT_REGISTRY}"

            print_info "Pulling latest $PLATFORM image from mirror: $MIRROR_IMAGE"
            if ! docker pull --platform "$PLATFORM" "$MIRROR_IMAGE"; then
                print_error "Failed to pull image $MIRROR_IMAGE for platform $PLATFORM"
                exit 1
            fi
            print_info "Successfully pulled $MIRROR_IMAGE for platform $PLATFORM"

            print_info "Retagging $MIRROR_IMAGE to $FULL_IMAGE"
            if ! docker tag "$MIRROR_IMAGE" "$FULL_IMAGE"; then
                print_error "Failed to tag image: $MIRROR_IMAGE -> $FULL_IMAGE"
                exit 1
            fi
        else
            print_info "Pulling latest $PLATFORM image: $FULL_IMAGE"
            if ! docker pull --platform "$PLATFORM" "$FULL_IMAGE"; then
                print_error "Failed to pull image $FULL_IMAGE for platform $PLATFORM"
                exit 1
            fi
            print_info "Successfully pulled $FULL_IMAGE for platform $PLATFORM"
        fi

        ALL_IMAGES+=("$FULL_IMAGE")
    done

    # Save all images into a single tar
    if [ ${#ALL_IMAGES[@]} -eq 0 ]; then
        print_error "No images to save"
        exit 1
    fi

    COMBINED_TAR="$IMAGES_DIR/${ENGINE_NAME}-${ENGINE_VERSION}-images.tar"
    COMBINED_TAR_BASENAME=$(basename "$COMBINED_TAR")
    print_info "Saving ${#ALL_IMAGES[@]} image(s) into $COMBINED_TAR_BASENAME..."

    if ! docker save "${ALL_IMAGES[@]}" -o "$COMBINED_TAR"; then
        print_error "Failed to export images"
        exit 1
    fi

    IMAGE_SIZE=$(stat -f%z "$COMBINED_TAR" 2>/dev/null || stat -c%s "$COMBINED_TAR" 2>/dev/null)
    print_info "Exported ${#ALL_IMAGES[@]} image(s) ($(numfmt --to=iec-i --suffix=B $IMAGE_SIZE 2>/dev/null || echo $IMAGE_SIZE bytes))"

    # Build manifest entries with per-image size from docker inspect
    for spec in "${IMAGE_SPECS[@]}"; do
        parse_image_spec "$spec"

        FULL_IMAGE="$IMAGE_NAME:$IMAGE_TAG"
        INSPECT_SIZE=$(docker image inspect "$FULL_IMAGE" --format '{{.Size}}')

        IMAGE_ENTRIES="${IMAGE_ENTRIES}
    - accelerator: \"$ACCELERATOR\"
      image_name: \"$IMAGE_NAME\"
      tag: \"$IMAGE_TAG\"
      image_file: \"images/$COMBINED_TAR_BASENAME\"
      platform: \"$PLATFORM\"
      size: $INSPECT_SIZE"
    done
else
    # Manifest-only mode: build image entries without Docker export
    print_info "Manifest-only mode: skipping Docker image export"
    COMBINED_TAR_BASENAME="${ENGINE_NAME}-${ENGINE_VERSION}-images.tar"
    for spec in "${IMAGE_SPECS[@]}"; do
        parse_image_spec "$spec"

        IMAGE_ENTRIES="${IMAGE_ENTRIES}
    - accelerator: \"$ACCELERATOR\"
      image_name: \"$IMAGE_NAME\"
      tag: \"$IMAGE_TAG\"
      image_file: \"images/$COMBINED_TAR_BASENAME\"
      platform: \"$PLATFORM\""
    done
fi

# Create manifest
print_info "Creating manifest..."

CREATED_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

if [ -n "$MANIFEST_TEMPLATE" ] && [ -f "$MANIFEST_TEMPLATE" ]; then
    print_info "Using manifest template: $MANIFEST_TEMPLATE"
    cp "$MANIFEST_TEMPLATE" "$PACKAGE_DIR/manifest.yaml"
else
    print_info "Generating default manifest"

    # Build images map for engine_version section
    IMAGES_MAP=""
    IFS=',' read -ra IMAGE_SPECS <<< "$IMAGES"
    for spec in "${IMAGE_SPECS[@]}"; do
        parse_image_spec "$spec"

        IMAGES_MAP="${IMAGES_MAP}
      ${ACCELERATOR}:
        image_name: \"${IMAGE_NAME}\"
        tag: \"${IMAGE_TAG}\""
    done

    # Build supported_tasks array
    SUPPORTED_TASKS_YAML=""
    if [ -n "$SUPPORTED_TASKS" ]; then
        IFS=',' read -ra TASK_ARRAY <<< "$SUPPORTED_TASKS"
        for task in "${TASK_ARRAY[@]}"; do
            # Trim whitespace
            task=$(echo "$task" | xargs)
            SUPPORTED_TASKS_YAML="${SUPPORTED_TASKS_YAML}
      - \"${task}\""
        done
    fi

    # Check if template directory is provided
    DEPLOY_TEMPLATE_CONTENT=""
    if [ -n "$TEMPLATE_DIR" ]; then
        TEMPLATE_BASE_DIR="$TEMPLATE_DIR"
        if [ -d "$TEMPLATE_BASE_DIR" ]; then
            print_info "Scanning template directory: $TEMPLATE_BASE_DIR"
            if DEPLOY_TEMPLATE_CONTENT=$(scan_and_generate_deploy_templates "$TEMPLATE_BASE_DIR"); then
                if [ -z "$DEPLOY_TEMPLATE_CONTENT" ]; then
                    print_warn "No valid templates found in $TEMPLATE_BASE_DIR, skipping deploy_template"
                fi
            else
                print_warn "No valid templates found in $TEMPLATE_BASE_DIR, skipping deploy_template"
                DEPLOY_TEMPLATE_CONTENT=""
            fi
        else
            print_warn "Template directory not found: $TEMPLATE_BASE_DIR, skipping deploy_template"
        fi
    fi

    # Generate deploy_template section (only when templates are provided)
    if [ -n "$DEPLOY_TEMPLATE_CONTENT" ]; then
        DEPLOY_TEMPLATE_SECTION="    deploy_template:
${DEPLOY_TEMPLATE_CONTENT}"
    else
        DEPLOY_TEMPLATE_SECTION=""
    fi

    # Generate supported_tasks section
    if [ -n "$SUPPORTED_TASKS_YAML" ]; then
        SUPPORTED_TASKS_SECTION="
    supported_tasks:${SUPPORTED_TASKS_YAML}"
    else
        SUPPORTED_TASKS_SECTION=""
    fi

    # Generate capabilities section.
    #
    # Emitted only when something was actually declared. Omitting the block is
    # meaningful: Neutree reads a missing capability as "undeclared" and falls
    # back to its pre-protocol behaviour (metrics scraped on :8000/metrics,
    # Playground shown). Writing a default here would silently change how every
    # engine rebuilt with this script behaves.
    CAPABILITIES_SECTION=""
    CAPABILITIES_BODY=""

    if [ -n "$METRICS_EXPORT" ]; then
        if [ "$METRICS_EXPORT" != "true" ] && [ "$METRICS_EXPORT" != "false" ]; then
            print_error "--metrics-export must be 'true' or 'false', got: $METRICS_EXPORT"
            exit 1
        fi

        METRICS_BODY="        enabled: ${METRICS_EXPORT}"

        if [ -n "$METRICS_EXPORT_PORT" ]; then
            if ! [[ "$METRICS_EXPORT_PORT" =~ ^[0-9]+$ ]] || [ "$METRICS_EXPORT_PORT" -lt 1 ] || [ "$METRICS_EXPORT_PORT" -gt 65535 ]; then
                print_error "--metrics-export-port must be an integer in 1-65535, got: $METRICS_EXPORT_PORT"
                exit 1
            fi

            METRICS_BODY="${METRICS_BODY}
        port: ${METRICS_EXPORT_PORT}"
        fi

        if [ -n "$METRICS_EXPORT_PATH" ]; then
            case "$METRICS_EXPORT_PATH" in
                /*) ;;
                *)
                    print_error "--metrics-export-path must start with '/', got: $METRICS_EXPORT_PATH"
                    exit 1
                    ;;
            esac

            METRICS_BODY="${METRICS_BODY}
        path: \"${METRICS_EXPORT_PATH}\""
        fi

        CAPABILITIES_BODY="${CAPABILITIES_BODY}
      metrics_export:
${METRICS_BODY}"
    elif [ -n "$METRICS_EXPORT_PORT" ] || [ -n "$METRICS_EXPORT_PATH" ]; then
        # Without an enabled flag there is no capability to attach these to, and
        # silently dropping them would ship a package that ignores what the
        # caller asked for.
        print_error "--metrics-export-port/--metrics-export-path require --metrics-export"
        exit 1
    fi

    if [ -n "$PLAYGROUND" ]; then
        if [ "$PLAYGROUND" != "true" ] && [ "$PLAYGROUND" != "false" ]; then
            print_error "--playground must be 'true' or 'false', got: $PLAYGROUND"
            exit 1
        fi

        PLAYGROUND_BODY="        enabled: ${PLAYGROUND}"

        if [ -n "$PLAYGROUND_MODES" ]; then
            PLAYGROUND_MODES_YAML=""
            IFS=',' read -ra MODE_ARRAY <<< "$PLAYGROUND_MODES"
            for mode in "${MODE_ARRAY[@]}"; do
                mode=$(echo "$mode" | xargs)
                [ -z "$mode" ] && continue

                case "$mode" in
                    chat|embedding|rerank) ;;
                    *)
                        print_error "--playground-modes accepts chat, embedding, rerank; got: $mode"
                        exit 1
                        ;;
                esac

                PLAYGROUND_MODES_YAML="${PLAYGROUND_MODES_YAML}
        - \"${mode}\""
            done

            if [ -n "$PLAYGROUND_MODES_YAML" ]; then
                PLAYGROUND_BODY="${PLAYGROUND_BODY}
        modes:${PLAYGROUND_MODES_YAML}"
            fi
        fi

        CAPABILITIES_BODY="${CAPABILITIES_BODY}
      playground:
${PLAYGROUND_BODY}"
    elif [ -n "$PLAYGROUND_MODES" ]; then
        print_error "--playground-modes requires --playground"
        exit 1
    fi

    if [ -n "$CAPABILITIES_BODY" ]; then
        CAPABILITIES_SECTION="
    capabilities:${CAPABILITIES_BODY}"
    fi

    # Generate values_schema section
    VALUES_SCHEMA_SECTION=""
    if [ -n "$SCHEMA_FILE" ]; then
        if [ ! -f "$SCHEMA_FILE" ]; then
            print_error "Schema file not found: $SCHEMA_FILE"
            exit 1
        fi

        print_info "Loading engine schema from: $SCHEMA_FILE"

        # Validate JSON syntax
        if ! jq empty "$SCHEMA_FILE" 2>/dev/null; then
            print_error "Invalid JSON in schema file: $SCHEMA_FILE"
            exit 1
        fi

        # Read and encode the schema file with Base64
        if base64 --help 2>&1 | grep -q -- '-w'; then
            SCHEMA_BASE64=$(base64 -w 0 < "$SCHEMA_FILE")
        else
            SCHEMA_BASE64=$(base64 < "$SCHEMA_FILE" | tr -d '\n')
        fi

        VALUES_SCHEMA_SECTION="
    values_schema:
      values_schema_base64: \"${SCHEMA_BASE64}\""
    else
        VALUES_SCHEMA_SECTION="
    values_schema:
      type: \"object\"
      properties:
        # Add your configuration schema here"
    fi

    cat > "$PACKAGE_DIR/manifest.yaml" << EOF
manifest_version: "1.0"

metadata:
  description: "${DESCRIPTION:-Engine version $ENGINE_VERSION}"
  author: "Neutree Team"
  created_at: "$CREATED_AT"
  version: $VERSION
  tags:
    - "engine"
    - "$ENGINE_NAME"
    - "$ENGINE_VERSION"

images:$IMAGE_ENTRIES

engines:
- name: $ENGINE_NAME
  engine_versions:
  - version: "$ENGINE_VERSION"
${VALUES_SCHEMA_SECTION}

$DEPLOY_TEMPLATE_SECTION
${SUPPORTED_TASKS_SECTION}
${CAPABILITIES_SECTION}

    images:$IMAGES_MAP
EOF
fi

if [ -z "$MANIFEST_ONLY" ]; then
    # Create the package
    print_info "Creating package archive: $OUTPUT_FILE"
    cd "$PACKAGE_DIR"
    tar -I "pigz -p 16" -cf "$OUTPUT_FILE" *
    cd - > /dev/null

    # Move to final location
    mkdir -p "$OUTPUT_DIR"
    mv -f "$PACKAGE_DIR/$OUTPUT_FILE" "$OUTPUT_DIR/$OUTPUT_FILE"

    # Copy standalone manifest.yaml for release
    MANIFEST_OUTPUT="${ENGINE_NAME}-${ENGINE_VERSION}-manifest.yaml"
    cp "$PACKAGE_DIR/manifest.yaml" "$OUTPUT_DIR/$MANIFEST_OUTPUT"
    print_info "Standalone manifest copied to: $OUTPUT_DIR/$MANIFEST_OUTPUT"
    # Calculate checksum
    print_info "Calculating checksum..."
    if command -v sha256sum &> /dev/null; then
        sha256sum "$OUTPUT_DIR/$OUTPUT_FILE" > "${OUTPUT_DIR}/${OUTPUT_FILE}.sha256"
    else
        shasum -a 256 "$OUTPUT_DIR/$OUTPUT_FILE" > "${OUTPUT_DIR}/${OUTPUT_FILE}.sha256"
    fi

    # Get package size
    PACKAGE_SIZE=$(stat -f%z "$OUTPUT_DIR/$OUTPUT_FILE" 2>/dev/null || stat -c%s "$OUTPUT_DIR/$OUTPUT_FILE" 2>/dev/null)
    print_info "Package created successfully!"
    echo ""
    echo "================================================"
    echo "Package Information:"
    echo "================================================"
    echo "Name:        $ENGINE_NAME"
    echo "Version:     $ENGINE_VERSION"
    echo "File:        $OUTPUT_FILE"
    echo "Size:        $(numfmt --to=iec-i --suffix=B $PACKAGE_SIZE 2>/dev/null || echo $PACKAGE_SIZE bytes)"
    echo "================================================"
    echo ""
    print_info "You can now validate the package with:"
    echo "    neutree-cli import validate --package $OUTPUT_FILE"
    echo ""
    print_info "Or import it with:"
    echo "    neutree-cli import engine --package $OUTPUT_FILE --workspace <workspace> --api-key <api-key> --server-url <server-url>"
else
    # Manifest-only: output just the manifest file
    mkdir -p "$OUTPUT_DIR"
    MANIFEST_OUTPUT="${ENGINE_NAME}-${ENGINE_VERSION}-manifest.yaml"
    cp "$PACKAGE_DIR/manifest.yaml" "$OUTPUT_DIR/$MANIFEST_OUTPUT"

    print_info "Manifest generated successfully!"
    echo ""
    echo "================================================"
    echo "Manifest Information:"
    echo "================================================"
    echo "Name:        $ENGINE_NAME"
    echo "Version:     $ENGINE_VERSION"
    echo "File:        $OUTPUT_DIR/$MANIFEST_OUTPUT"
    echo "================================================"
fi
