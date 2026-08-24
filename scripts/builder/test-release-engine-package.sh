#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
temp_root=$(mktemp -d)

cleanup() {
    rm -rf "$temp_root"
}
trap cleanup EXIT

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

assert_contains() {
    local value="$1"
    local expected="$2"

    [[ "$value" == *"$expected"* ]] || fail "expected output to contain: $expected"
}

assert_file() {
    local path="$1"

    [[ -f "$path" ]] || fail "expected file to exist: $path"
}

assert_file_contains() {
    local path="$1"
    local expected="$2"

    grep -Fq -- "$expected" "$path" || fail "expected $path to contain: $expected"
}

fake_bin="$temp_root/bin"
mkdir -p "$fake_bin"

cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
    image)
        if [[ "${2:-}" == "inspect" && "$*" == *"{{.Size}}"* ]]; then
            echo 128
            exit 0
        fi
        exit 1
        ;;
    pull)
        exit 0
        ;;
    save)
        for ((index = 1; index <= $#; index++)); do
            if [[ "${!index}" == "-o" ]]; then
                next_index=$((index + 1))
                printf 'fake image archive\n' > "${!next_index}"
                exit 0
            fi
        done
        exit 1
        ;;
    *)
        exit 1
        ;;
esac
EOF
chmod +x "$fake_bin/docker"

cat > "$fake_bin/pigz" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

while [[ $# -gt 0 ]]; do
    case "$1" in
        -p)
            shift 2
            ;;
        *)
            shift
            ;;
    esac
done

exec gzip -c
EOF
chmod +x "$fake_bin/pigz"

cat > "$fake_bin/tar" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

output=""
inputs=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        -I)
            shift 2
            ;;
        -cf)
            output="$2"
            shift 2
            ;;
        *)
            inputs+=("$1")
            shift
            ;;
    esac
done

[[ -n "$output" ]] || exit 1
exec /usr/bin/bsdtar -czf "$output" "${inputs[@]}"
EOF
chmod +x "$fake_bin/tar"

fake_cli="$fake_bin/neutree-cli"
cat > "$fake_cli" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "${1:-}" == "import" && "${2:-}" == "validate" && "${3:-}" == "-p" ]]
[[ -f "${4:-}" ]]
EOF
chmod +x "$fake_cli"

package_url="http://files.internal/engine-packages/vllm/v0.24.0-neutree1/nvidia/v0.24.0-neutree1-ray2.53.0/vllm-v0.24.0-neutree1.tar.gz"
image_ref="registry.internal:5000/release/engine-vllm:v0.24.0-neutree1-ray2.53.0"
schema_path="$repo_root/internal/engine/vllm/v0.24.0/schema.json"
template_dir="$repo_root/internal/engine/vllm/v0.24.0/templates"

manifest_output="$temp_root/manifest-output"
OUTPUT_DIR="$manifest_output" bash "$repo_root/scripts/builder/build-engine-package.sh" \
    --manifest-only \
    --name vllm \
    --version v0.24.0-neutree1 \
    --images "nvidia_gpu:$image_ref" \
    --supported-tasks "text-generation,text-embedding,text-rerank" \
    --schema "$schema_path" \
    --template-dir "$template_dir" \
    --package-url "$package_url"

manifest_path="$manifest_output/vllm-v0.24.0-neutree1-manifest.yaml"
assert_file "$manifest_path"
assert_file_contains "$manifest_path" "package_url: \"$package_url\""
assert_file_contains "$manifest_path" "image_name: \"registry.internal:5000/release/engine-vllm\""
assert_file_contains "$manifest_path" "tag: \"v0.24.0-neutree1-ray2.53.0\""

if OUTPUT_DIR="$temp_root/invalid-image-output" bash "$repo_root/scripts/builder/build-engine-package.sh" \
    --manifest-only \
    --name vllm \
    --version v0.24.0-neutree1 \
    --images "nvidia_gpu:registry.internal:5000/release/engine-vllm" \
    --supported-tasks "text-generation,text-embedding,text-rerank" \
    --schema "$schema_path" \
    --template-dir "$template_dir" \
    --package-url "$package_url"; then
    fail "expected an image reference without a tag to fail"
fi

make_output=$(make -s --just-print -C "$repo_root" build-engine-manifest \
    ENGINE_PACKAGE_OUTPUT_DIR="$temp_root/make-output" \
    ENGINE_PACKAGE_URL="$package_url")
assert_contains "$make_output" "OUTPUT_DIR=\"$temp_root/make-output\""
assert_contains "$make_output" "--package-url \"$package_url\""

resolved_image=$(make -s -C "$repo_root/cluster-image-builder" print-engine-image-ref \
    ENGINE_TYPE=vllm \
    ENGINE_ACCELERATOR=nvidia \
    IMAGE_REPO=registry.internal:5000 \
    IMAGE_PROJECT=release \
    ENGINE_VLLM_VERSION=v0.24.0 \
    ENGINE_VLLM_BASE_IMAGE_TAG=v0.24.0 \
    ENGINE_PATCH_SUFFIX=1 \
    RAY_SHORT_VERSION=ray2.53.0)
[[ "$resolved_image" == "nvidia_gpu:$image_ref" ]] || fail "unexpected image mapping: $resolved_image"

resolved_image=$(make -s -C "$repo_root/cluster-image-builder" print-engine-image-ref \
    ENGINE_TYPE=vllm \
    ENGINE_ACCELERATOR=rocm \
    IMAGE_REPO=registry.internal:5000 \
    IMAGE_PROJECT=release \
    ENGINE_VLLM_ROCM_BASE_IMAGE_TAG=v0.24.0-rocm \
    ENGINE_PATCH_SUFFIX=1 \
    RAY_SHORT_VERSION=ray2.53.0)
[[ "$resolved_image" == "amd_gpu:registry.internal:5000/release/engine-vllm-rocm:v0.24.0-rocm-neutree1-ray2.53.0" ]] || fail "unexpected image mapping: $resolved_image"

resolved_image=$(make -s -C "$repo_root/cluster-image-builder" print-engine-image-ref \
    ENGINE_TYPE=sglang \
    ENGINE_ACCELERATOR=nvidia \
    IMAGE_REPO=registry.internal:5000 \
    IMAGE_PROJECT=release \
    ENGINE_SGLANG_BASE_IMAGE_TAG=v0.5.10 \
    ENGINE_PATCH_SUFFIX=1 \
    RAY_SHORT_VERSION=ray2.53.0)
[[ "$resolved_image" == "nvidia_gpu:registry.internal:5000/release/engine-sglang:v0.5.10-neutree1-ray2.53.0" ]] || fail "unexpected image mapping: $resolved_image"

resolved_image=$(make -s -C "$repo_root/cluster-image-builder" print-engine-image-ref \
    ENGINE_TYPE=sglang \
    ENGINE_ACCELERATOR=rocm \
    IMAGE_REPO=registry.internal:5000 \
    IMAGE_PROJECT=release \
    ENGINE_SGLANG_ROCM_TAG=v0.5.10-rocm720-mi30x \
    ENGINE_PATCH_SUFFIX=1 \
    RAY_SHORT_VERSION=ray2.53.0)
[[ "$resolved_image" == "amd_gpu:registry.internal:5000/release/engine-sglang:v0.5.10-rocm720-mi30x-neutree1-ray2.53.0" ]] || fail "unexpected image mapping: $resolved_image"

resolved_image=$(make -s -C "$repo_root/cluster-image-builder" print-engine-image-ref \
    ENGINE_TYPE=llama-cpp \
    ENGINE_ACCELERATOR=cpu \
    IMAGE_REPO=registry.internal:5000 \
    IMAGE_PROJECT=release \
    ENGINE_LLAMA_CPP_VERSION=v0.3.7 \
    ENGINE_PATCH_SUFFIX=1 \
    RAY_SHORT_VERSION=ray2.53.0)
[[ "$resolved_image" == "ssh_cpu:registry.internal:5000/release/engine-llama-cpp:v0.3.7-neutree1-ray2.53.0" ]] || fail "unexpected image mapping: $resolved_image"

package_output="$temp_root/package-output"
PATH="$fake_bin:$PATH" "$repo_root/scripts/builder/build-release-engine-package.sh" \
    --engine vllm \
    --engine-version v0.24.0 \
    --accelerator nvidia \
    --engine-patch-suffix 1 \
    --image-ref "$image_ref" \
    --package-url "$package_url" \
    --output-dir "$package_output" \
    --neutree-cli "$fake_cli"

assert_file "$package_output/vllm-v0.24.0-neutree1.tar.gz"
assert_file "$package_output/vllm-v0.24.0-neutree1-manifest.yaml"
assert_file "$package_output/vllm-v0.24.0-neutree1.tar.gz.sha256"
assert_file_contains "$package_output/vllm-v0.24.0-neutree1-manifest.yaml" "package_url: \"$package_url\""

if PATH="$fake_bin:$PATH" "$repo_root/scripts/builder/build-release-engine-package.sh" \
    --engine vllm \
    --engine-version v9.99.9 \
    --accelerator nvidia \
    --image-ref "$image_ref" \
    --package-url "$package_url" \
    --output-dir "$temp_root/missing-schema" \
    --neutree-cli "$fake_cli"; then
    fail "expected missing schema/template preflight to fail"
fi

echo "PASS: release engine package builder"
