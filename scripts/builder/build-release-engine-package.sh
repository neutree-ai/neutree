#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

usage() {
    cat <<'EOF'
Usage: build-release-engine-package.sh [OPTIONS]

Build and validate an Engine Package for an image already published by an
Engine image release workflow.

Required options:
  --engine NAME
  --engine-version VERSION
  --accelerator nvidia|rocm|cpu
  --image-ref REGISTRY/PROJECT/IMAGE:TAG
  --package-url URL
  --output-dir DIR

Optional options:
  --engine-patch-suffix SUFFIX
  --neutree-cli PATH
EOF
}

engine=""
engine_version=""
accelerator=""
engine_patch_suffix=""
image_ref=""
package_url=""
output_dir=""
neutree_cli="$repo_root/bin/neutree-cli"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --engine)
            engine="$2"
            shift 2
            ;;
        --engine-version)
            engine_version="$2"
            shift 2
            ;;
        --accelerator)
            accelerator="$2"
            shift 2
            ;;
        --engine-patch-suffix)
            engine_patch_suffix="$2"
            shift 2
            ;;
        --image-ref)
            image_ref="$2"
            shift 2
            ;;
        --package-url)
            package_url="$2"
            shift 2
            ;;
        --output-dir)
            output_dir="$2"
            shift 2
            ;;
        --neutree-cli)
            neutree_cli="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            fail "unknown option: $1"
            ;;
    esac
done

for option_name in engine engine_version accelerator image_ref package_url output_dir; do
    [[ -n "${!option_name}" ]] || fail "--${option_name//_/-} is required"
done

if [[ ! "$engine_version" =~ ^v[A-Za-z0-9._-]+$ ]]; then
    fail "--engine-version must start with v and contain only letters, numbers, dots, underscores, or hyphens"
fi
if [[ -n "$engine_patch_suffix" && ! "$engine_patch_suffix" =~ ^[A-Za-z0-9._-]+$ ]]; then
    fail "--engine-patch-suffix must match [A-Za-z0-9._-]+"
fi
if [[ ! "$package_url" =~ ^https://[^[:space:]\"\\]+$ || "$package_url" == *\?* || "$package_url" == *\#* ]]; then
    fail "--package-url must be an HTTPS URL without whitespace or quotes"
fi
package_url_authority="${package_url#https://}"
package_url_authority="${package_url_authority%%/*}"
if [[ "$package_url_authority" == *"@"* ]]; then
    fail "--package-url must not include URL credentials"
fi

case "$engine:$accelerator" in
    vllm:nvidia)
        manifest_accelerator="nvidia_gpu"
        supported_tasks="text-generation,text-embedding,text-rerank"
        ;;
    vllm:rocm)
        manifest_accelerator="amd_gpu"
        supported_tasks="text-generation,text-embedding,text-rerank"
        ;;
    sglang:nvidia)
        manifest_accelerator="nvidia_gpu"
        supported_tasks="text-generation,text-embedding"
        ;;
    sglang:rocm)
        manifest_accelerator="amd_gpu"
        supported_tasks="text-generation,text-embedding"
        ;;
    llama-cpp:cpu)
        manifest_accelerator="ssh_cpu"
        supported_tasks="text-generation,text-embedding"
        ;;
    *)
        fail "unsupported engine and accelerator combination: $engine:$accelerator"
        ;;
esac

manifest_version="$engine_version"
if [[ -n "$engine_patch_suffix" ]]; then
    manifest_version="${engine_version}-neutree${engine_patch_suffix}"
fi

base_version="${engine_version%%-*}"
schema_path="$repo_root/internal/engine/$engine/$base_version/schema.json"
template_dir="$repo_root/internal/engine/$engine/$base_version/templates"
[[ -f "$schema_path" ]] || fail "engine schema is missing: $schema_path"
[[ -d "$template_dir" ]] || fail "engine templates are missing: $template_dir"
if ! find "$template_dir" -type f \( -name '*.yaml' -o -name '*.yml' \) -print -quit | grep -q .; then
    fail "engine templates are missing: $template_dir"
fi

image_name="${image_ref%:*}"
image_tag="${image_ref##*:}"
if [[ -z "$image_name" || -z "$image_tag" || "$image_name" == "$image_ref" || ! "$image_tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
    fail "--image-ref must use registry/project/image:tag"
fi

archive_name="$engine-$manifest_version.tar.gz"
if [[ "$package_url" != */"$archive_name" ]]; then
    fail "--package-url must end with $archive_name"
fi

mkdir -p "$output_dir"
make -C "$repo_root" build-engine-package \
    ENGINE_PACKAGE_OUTPUT_DIR="$output_dir" \
    ENGINE_PACKAGE_URL="$package_url" \
    ENGINE_NAME="$engine" \
    ENGINE_VERSION="$manifest_version" \
    ENGINE_IMAGES="$manifest_accelerator:$image_ref" \
    ENGINE_TASKS="$supported_tasks" \
    ENGINE_DESCRIPTION="$engine inference engine"

archive_path="$output_dir/$archive_name"
manifest_path="$output_dir/$engine-$manifest_version-manifest.yaml"
checksum_path="$archive_path.sha256"
for artifact in "$archive_path" "$manifest_path" "$checksum_path"; do
    [[ -f "$artifact" ]] || fail "expected package artifact is missing: $artifact"
done

[[ -x "$neutree_cli" ]] || fail "neutree-cli is not executable: $neutree_cli"
"$neutree_cli" import validate -p "$archive_path"

(
    cd "$repo_root"
    verify_command=(
        go run ./scripts/builder/verify_engine_package
        --package "$archive_path"
        --manifest "$manifest_path"
        --checksum "$checksum_path"
        --engine "$engine"
        --version "$manifest_version"
        --accelerator "$manifest_accelerator"
        --image-name "$image_name"
        --image-tag "$image_tag"
        --supported-tasks "$supported_tasks"
        --package-url "$package_url"
        --schema "$schema_path"
        --template-dir "$template_dir"
    )
    "${verify_command[@]}"
)

printf 'archive=%s\nmanifest=%s\nchecksum=%s\n' "$archive_path" "$manifest_path" "$checksum_path"
