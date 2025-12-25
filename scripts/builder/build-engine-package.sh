#!/bin/bash

# Engine Version Package Builder
# This script helps build engine version packages for Neutree

set -e

VERSION=$(git describe --tags --always --dirty)
OUTPUT_DIR="./dist"

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
    -m, --manifest FILE       Path to manifest template file (optional)
    -t, --template-dir DIR    Path to template directory containing template/kubernetes/default.yaml
    -c, --schema FILE         Path to engine_schema.json file (optional)
    -o, --output FILE         Output package file path (default: ENGINE-VERSION.tar.gz)
    -d, --description TEXT    Engine version description
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
    OUTPUT_FILE="${ENGINE_NAME}-${ENGINE_VERSION}.tar.gz"
fi

# Create temporary directory
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

PACKAGE_DIR="$TEMP_DIR/package"
IMAGES_DIR="$PACKAGE_DIR/images"
mkdir -p "$IMAGES_DIR"

print_info "Building engine version package: $ENGINE_NAME $ENGINE_VERSION"
print_info "Temporary directory: $TEMP_DIR"

# Export Docker images
print_info "Exporting Docker images..."
IFS=',' read -ra IMAGE_SPECS <<< "$IMAGES"
IMAGE_ENTRIES=""

for spec in "${IMAGE_SPECS[@]}"; do
    IFS=':' read -ra PARTS <<< "$spec"
    ACCELERATOR="${PARTS[0]}"
    IMAGE_NAME="${PARTS[1]}"
    IMAGE_TAG="${PARTS[2]}"

    FULL_IMAGE="$IMAGE_NAME:$IMAGE_TAG"
    IMAGE_FILE="images/${IMAGE_NAME//\//-}-${IMAGE_TAG}.tar"
    IMAGE_FILE_BASENAME=$(basename "$IMAGE_FILE")
    OUTPUT_TAR="$IMAGES_DIR/$IMAGE_FILE_BASENAME"

    print_info "Exporting $FULL_IMAGE for $ACCELERATOR..."

    # Check if image exists, if not, pull it
    if ! docker image inspect "$FULL_IMAGE" > /dev/null 2>&1; then
        print_warn "Image $FULL_IMAGE not found locally. Pulling image..."
        if ! docker pull "$FULL_IMAGE"; then
            print_error "Failed to pull image $FULL_IMAGE"
            exit 1
        fi
        print_info "Successfully pulled $FULL_IMAGE"
    fi

    docker save "$FULL_IMAGE" -o "$OUTPUT_TAR"

    if [ $? -eq 0 ]; then
        IMAGE_SIZE=$(stat -f%z "$OUTPUT_TAR" 2>/dev/null || stat -c%s "$OUTPUT_TAR" 2>/dev/null)
        print_info "Exported $FULL_IMAGE ($(numfmt --to=iec-i --suffix=B $IMAGE_SIZE 2>/dev/null || echo $IMAGE_SIZE bytes))"

        # Add to manifest entries
        IMAGE_ENTRIES="${IMAGE_ENTRIES}
    - accelerator: \"$ACCELERATOR\"
      image_name: \"$IMAGE_NAME\"
      tag: \"$IMAGE_TAG\"
      image_file: \"images/$IMAGE_FILE_BASENAME\"
      platform: \"linux/amd64\"
      size: $IMAGE_SIZE"
    else
        print_error "Failed to export $FULL_IMAGE"
        exit 1
    fi
done

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
        IFS=':' read -ra PARTS <<< "$spec"
        ACCELERATOR="${PARTS[0]}"
        IMAGE_NAME="${PARTS[1]}"
        IMAGE_TAG="${PARTS[2]}"

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
        TEMPLATE_BASE_DIR="$TEMPLATE_DIR/template"
        if [ -d "$TEMPLATE_BASE_DIR" ]; then
            print_info "Scanning template directory: $TEMPLATE_BASE_DIR"
            DEPLOY_TEMPLATE_CONTENT=$(scan_and_generate_deploy_templates "$TEMPLATE_BASE_DIR")
            if [ $? -ne 0 ] || [ -z "$DEPLOY_TEMPLATE_CONTENT" ]; then
                print_warn "No valid templates found in $TEMPLATE_BASE_DIR"
                print_warn "Using default deploy template configuration"
                DEPLOY_TEMPLATE_CONTENT=""
            fi
        else
            print_warn "Template directory not found: $TEMPLATE_BASE_DIR"
            print_warn "Using default deploy template configuration"
        fi
    fi

    # Generate deploy_template section
    if [ -n "$DEPLOY_TEMPLATE_CONTENT" ]; then
        DEPLOY_TEMPLATE_SECTION="    deploy_template:
${DEPLOY_TEMPLATE_CONTENT}"
    else
        DEPLOY_TEMPLATE_SECTION="    deploy_template:
      kubernetes:
        default:
          replicas: 1
          resources:
            requests:
              cpu: \"2\"
              memory: \"8Gi\"
            limits:
              cpu: \"4\"
              memory: \"16Gi\"

      ssh:
        default:
          workers: 1
          resources:
            cpu: 4
            memory: \"16Gi\""
    fi

    # Generate supported_tasks section
    if [ -n "$SUPPORTED_TASKS_YAML" ]; then
        SUPPORTED_TASKS_SECTION="
    supported_tasks:${SUPPORTED_TASKS_YAML}"
    else
        SUPPORTED_TASKS_SECTION=""
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

    images:$IMAGES_MAP
EOF
fi

# Create the package
print_info "Creating package archive: $OUTPUT_FILE"
cd "$PACKAGE_DIR"
tar -I "pigz -p 16" -cf "$OUTPUT_FILE" *
cd - > /dev/null

# Move to final location
mv -f "$PACKAGE_DIR/$OUTPUT_FILE" "$OUTPUT_DIR/$OUTPUT_FILE"
# Calculate checksum
print_info "Calculating checksum..."
if command -v md5sum &> /dev/null; then
    md5sum "$OUTPUT_DIR/$OUTPUT_FILE" > "${OUTPUT_DIR}/${OUTPUT_FILE}.md5"
else
    md5 "$OUTPUT_DIR/$OUTPUT_FILE" | awk '{print $4}' > "${OUTPUT_DIR}/${OUTPUT_FILE}.md5"
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
