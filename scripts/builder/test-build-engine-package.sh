#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
BUILD_SCRIPT="$SCRIPT_DIR/build-engine-package.sh"

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

TEST_ROOT=$(mktemp -d "$REPO_ROOT/.test-build-engine-package.XXXXXX")
trap 'rm -rf -- "$TEST_ROOT"' EXIT

STUB_BIN="$TEST_ROOT/bin"
RUN_DIR="$TEST_ROOT/run"
OUTPUT_DIR="$TEST_ROOT/output"
mkdir -p "$STUB_BIN" "$RUN_DIR"

cat > "$STUB_BIN/docker" <<'EOF'
#!/usr/bin/env bash

set -euo pipefail

case "${1:-}" in
    image)
        [[ "${2:-}" == "inspect" ]] || {
            printf 'unexpected docker image command: %s\n' "$*" >&2
            exit 64
        }
        if [[ " $* " == *" --format "* ]]; then
            printf '1024\n'
        fi
        ;;
    pull)
        ;;
    save)
        output_file=""
        while (( $# > 0 )); do
            if [[ "$1" == "-o" ]]; then
                [[ $# -ge 2 ]] || {
                    printf 'docker save is missing the -o value\n' >&2
                    exit 64
                }
                output_file="$2"
                break
            fi
            shift
        done
        [[ -n "$output_file" ]] || {
            printf 'docker save is missing -o\n' >&2
            exit 64
        }
        printf 'stub docker image archive\n' > "$output_file"
        ;;
    *)
        printf 'unexpected docker command: %s\n' "$*" >&2
        exit 64
        ;;
esac
EOF

cat > "$STUB_BIN/tar" <<'EOF'
#!/usr/bin/env bash

set -euo pipefail

[[ -f manifest.yaml ]] || {
    printf 'tar was not invoked from a package directory containing manifest.yaml\n' >&2
    exit 64
}
[[ -d images ]] || {
    printf 'tar was not invoked from a package directory containing images/\n' >&2
    exit 64
}

output_file=""
has_manifest_input=false
has_images_input=false
while (( $# > 0 )); do
    case "$1" in
        -I)
            [[ $# -ge 2 ]] || {
                printf 'tar is missing the -I value\n' >&2
                exit 64
            }
            shift 2
            ;;
        -cf|-czf|-zcf|-f)
            [[ $# -ge 2 ]] || {
                printf 'tar is missing the output file\n' >&2
                exit 64
            }
            output_file="$2"
            shift 2
            ;;
        --file=*)
            output_file="${1#--file=}"
            shift
            ;;
        manifest.yaml|./manifest.yaml)
            has_manifest_input=true
            shift
            ;;
        images|images/|./images|./images/)
            has_images_input=true
            shift
            ;;
        *)
            shift
            ;;
    esac
done

[[ -n "$output_file" ]] || {
    printf 'tar output file was not provided\n' >&2
    exit 64
}
[[ "$has_manifest_input" == true ]] || {
    printf 'tar arguments do not include manifest.yaml\n' >&2
    exit 64
}
[[ "$has_images_input" == true ]] || {
    printf 'tar arguments do not include images/\n' >&2
    exit 64
}
printf 'stub engine package archive\n' > "$output_file"
EOF

chmod +x "$STUB_BIN/docker" "$STUB_BIN/tar"

MISSING_PLATFORM_LOG="$TEST_ROOT/missing-platform.log"
if (
    cd "$RUN_DIR"
    PATH="$STUB_BIN:$PATH" OUTPUT_DIR="$OUTPUT_DIR" bash "$BUILD_SCRIPT" \
        --name test-engine \
        --version v1.0.0 \
        --images cpu:example/test-engine:v1.0.0 \
        --supported-tasks generate \
        --platform --manifest-only
) > "$MISSING_PLATFORM_LOG" 2>&1; then
    fail "--platform accepted another flag as its value"
fi
grep -Fq -- "--platform requires a value" "$MISSING_PLATFORM_LOG" || \
    fail "missing --platform value did not produce a clear error"

BUILD_LOG="$TEST_ROOT/build.log"
if ! (
    cd "$RUN_DIR"
    PATH="$STUB_BIN:$PATH" OUTPUT_DIR="$OUTPUT_DIR" bash "$BUILD_SCRIPT" \
        --name test-engine \
        --version v1.0.0 \
        --images cpu:example/test-engine:v1.0.0 \
        --supported-tasks generate \
        --platform linux/arm64
) > "$BUILD_LOG" 2>&1; then
    sed 's/^/build-engine-package: /' "$BUILD_LOG" >&2
    fail "package build failed"
fi

ARCHIVE="$OUTPUT_DIR/test-engine-v1.0.0.tar.gz"
MANIFEST="$OUTPUT_DIR/test-engine-v1.0.0-manifest.yaml"
CHECKSUM="$ARCHIVE.sha256"

[[ -s "$ARCHIVE" ]] || fail "archive was not created: $ARCHIVE"
[[ -s "$MANIFEST" ]] || fail "standalone manifest was not created: $MANIFEST"
[[ -s "$CHECKSUM" ]] || fail "SHA-256 checksum was not created: $CHECKSUM"

ACTUAL_HASH=$(awk 'NR == 1 { print $1 }' "$CHECKSUM")
[[ "$ACTUAL_HASH" =~ ^[0-9a-f]{64}$ ]] || fail "invalid SHA-256 format: $ACTUAL_HASH"

if command -v sha256sum >/dev/null 2>&1; then
    EXPECTED_HASH=$(sha256sum "$ARCHIVE" | awk '{ print $1 }')
else
    EXPECTED_HASH=$(shasum -a 256 "$ARCHIVE" | awk '{ print $1 }')
fi
[[ "$ACTUAL_HASH" == "$EXPECTED_HASH" ]] || fail "SHA-256 checksum does not match the archive"

if compgen -G "$OUTPUT_DIR/*.md5" >/dev/null; then
    fail "legacy MD5 checksum was created"
fi

grep -Eq '^[[:space:]]*platform:[[:space:]]*"linux/arm64"[[:space:]]*$' "$MANIFEST" || \
    fail "manifest does not contain platform linux/arm64"
if grep -Eq '^[[:space:]]*platform:[[:space:]]*"linux/amd64"[[:space:]]*$' "$MANIFEST"; then
    fail "manifest still contains platform linux/amd64"
fi

MANIFEST_ONLY_OUTPUT_DIR="$TEST_ROOT/manifest-only-output"
MANIFEST_ONLY_LOG="$TEST_ROOT/manifest-only.log"
if ! (
    cd "$RUN_DIR"
    PATH="$STUB_BIN:$PATH" OUTPUT_DIR="$MANIFEST_ONLY_OUTPUT_DIR" bash "$BUILD_SCRIPT" \
        --manifest-only \
        --name test-engine \
        --version v1.0.0 \
        --images cpu:example/test-engine:v1.0.0 \
        --supported-tasks generate \
        --platform linux/arm64
) > "$MANIFEST_ONLY_LOG" 2>&1; then
    sed 's/^/build-engine-manifest: /' "$MANIFEST_ONLY_LOG" >&2
    fail "manifest-only build failed"
fi

MANIFEST_ONLY_MANIFEST="$MANIFEST_ONLY_OUTPUT_DIR/test-engine-v1.0.0-manifest.yaml"
[[ -s "$MANIFEST_ONLY_MANIFEST" ]] || \
    fail "manifest-only output was not created: $MANIFEST_ONLY_MANIFEST"
grep -Eq '^[[:space:]]*platform:[[:space:]]*"linux/arm64"[[:space:]]*$' "$MANIFEST_ONLY_MANIFEST" || \
    fail "manifest-only output does not contain platform linux/arm64"
if grep -Eq '^[[:space:]]*platform:[[:space:]]*"linux/amd64"[[:space:]]*$' "$MANIFEST_ONLY_MANIFEST"; then
    fail "manifest-only output still contains platform linux/amd64"
fi

printf 'PASS: package and manifest-only artifacts use platform linux/arm64 and SHA-256 only\n'
