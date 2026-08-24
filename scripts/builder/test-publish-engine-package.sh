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

assert_file() {
    local path="$1"

    [[ -f "$path" ]] || fail "expected file to exist: $path"
}

assert_not_exists() {
    local path="$1"

    [[ ! -e "$path" ]] || fail "expected path to be absent: $path"
}

fake_bin="$temp_root/bin"
remote_root="$temp_root/remote"
mkdir -p "$fake_bin" "$remote_root"

cat > "$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

remote_root="${TEST_WEBDAV_ROOT:?}"
method="GET"
output_file=""
write_out=""
dump_headers=""
upload_file=""
fail_on_http="false"
location_follow="false"
headers=("")
url=""

status=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --silent|--show-error|--location)
            [[ "$1" == "--location" ]] && location_follow="true"
            shift
            ;;
        --fail)
            fail_on_http="true"
            shift
            ;;
        --head)
            method="HEAD"
            shift
            ;;
        --request)
            method="$2"
            shift 2
            ;;
        --output)
            output_file="$2"
            shift 2
            ;;
        --write-out)
            write_out="$2"
            shift 2
            ;;
        --dump-header)
            dump_headers="$2"
            shift 2
            ;;
        --upload-file)
            upload_file="$2"
            shift 2
            ;;
        --header)
            headers+=("$2")
            shift 2
            ;;
        http://*|https://*)
            url="$1"
            shift
            ;;
        *)
            echo "unexpected curl arg: $1" >&2
            exit 1
            ;;
    esac
done

[[ -n "$url" ]] || { echo "missing URL" >&2; exit 1; }

origin="${url%%://*}://"
origin_rest="${url#"$origin"}"
origin="${origin}${origin_rest%%/*}"
path="${url#"$origin"}"
path="${path#/}"
target="$remote_root/$path"

destination=""
overwrite="T"
for header in "${headers[@]}"; do
    case "$header" in
        Destination:*)
            destination="${header#Destination: }"
            ;;
        Overwrite:*)
            overwrite="${header#Overwrite: }"
            ;;
    esac
done

maybe_fail() {
    local http_status="$1"

    if [[ "$fail_on_http" == "true" && "$http_status" =~ ^[45] ]]; then
        exit 22
    fi
}

write_output() {
    local http_status="$1"
    local body_path="${2:-}"

    if [[ -n "$dump_headers" ]]; then
        {
            printf 'HTTP/1.1 %s\n' "$http_status"
            if [[ "$method" == "OPTIONS" ]]; then
                printf 'Allow: %s\n' "${TEST_OPTIONS_ALLOW:-OPTIONS, MKCOL, PUT, MOVE, DELETE}"
            fi
        } > "$dump_headers"
    fi

    if [[ -n "$output_file" ]]; then
        : > "$output_file"
    fi

    if [[ -n "$body_path" && "$method" != "HEAD" ]]; then
        if [[ -n "$output_file" ]]; then
            cp "$body_path" "$output_file"
        else
            cat "$body_path"
        fi
    fi

    if [[ -n "$write_out" ]]; then
        [[ "$write_out" == '%{http_code}' ]] || { echo "unsupported --write-out: $write_out" >&2; exit 1; }
        printf '%s' "$http_status"
    fi
}

case "$method" in
    OPTIONS)
        status=204
        ;;
    MKCOL)
        if [[ -d "$target" ]]; then
            status=405
        else
            mkdir -p "$target"
            status=201
        fi
        ;;
    PUT)
        mkdir -p "$(dirname "$target")"
        cp "$upload_file" "$target"
        status=201
        ;;
    HEAD)
        if [[ -f "$target" ]]; then
            status=200
        else
            status=404
        fi
        ;;
    GET)
        if [[ -f "$target" ]]; then
            status=200
        else
            status=404
        fi
        ;;
    MOVE)
        if [[ ! -f "$target" ]]; then
            status=404
        else
            dest_origin="${destination%%://*}://"
            dest_rest="${destination#"$dest_origin"}"
            dest_origin="${dest_origin}${dest_rest%%/*}"
            dest_path="${destination#"$dest_origin"}"
            dest_path="${dest_path#/}"
            dest_target="$remote_root/$dest_path"
            if [[ "${TEST_FAIL_MOVE_ON_BASENAME:-}" == "$(basename "$dest_target")" ]]; then
                status=500
            elif [[ "$overwrite" == "F" && -e "$dest_target" ]]; then
                status=412
            else
                mkdir -p "$(dirname "$dest_target")"
                mv "$target" "$dest_target"
                status=201
            fi
        fi
        ;;
    DELETE)
        if [[ -e "$target" ]]; then
            rm -rf "$target"
            status=204
        else
            status=404
        fi
        ;;
    *)
        echo "unsupported method: $method" >&2
        exit 1
        ;;
esac

maybe_fail "$status"
if [[ "$method" == "GET" && -f "$target" ]]; then
    write_output "$status" "$target"
else
    write_output "$status"
fi
EOF
chmod +x "$fake_bin/curl"

archive="$temp_root/vllm-v0.24.0-neutree1.tar.gz"
manifest="$temp_root/vllm-v0.24.0-neutree1-manifest.yaml"
checksum="$archive.sha256"
printf 'package archive\n' > "$archive"
printf 'manifest body\n' > "$manifest"
sha256sum "$archive" > "$checksum"

file_server_base_url="http://files.internal/packages"
package_url="$file_server_base_url/engine-packages/vllm/v0.24.0-neutree1/nvidia/v0.24.0-neutree1-ray2.53.0/vllm-v0.24.0-neutree1.tar.gz"
stable_dir="$remote_root/packages/engine-packages/vllm/v0.24.0-neutree1/nvidia/v0.24.0-neutree1-ray2.53.0"

TEST_WEBDAV_ROOT="$remote_root" CURL_BIN="$fake_bin/curl" \
    "$repo_root/scripts/builder/publish-engine-package.sh" \
    --package-url "$package_url" \
    --archive "$archive" \
    --manifest "$manifest" \
    --checksum "$checksum" \
    --staging-id success-case

assert_file "$stable_dir/$(basename "$archive")"
assert_file "$stable_dir/$(basename "$manifest")"
assert_file "$stable_dir/$(basename "$checksum")"
assert_not_exists "$remote_root/packages/engine-packages/.staging/success-case"

duplicate_archive="$stable_dir/$(basename "$archive")"
printf 'do not overwrite\n' > "$duplicate_archive"
if TEST_WEBDAV_ROOT="$remote_root" CURL_BIN="$fake_bin/curl" \
    "$repo_root/scripts/builder/publish-engine-package.sh" \
    --package-url "$package_url" \
    --archive "$archive" \
    --manifest "$manifest" \
    --checksum "$checksum" \
    --staging-id duplicate-case; then
    fail "expected duplicate stable archive to fail"
fi
[[ "$(cat "$duplicate_archive")" == "do not overwrite" ]] || fail "stable archive was overwritten"

rm -rf "$stable_dir"
if TEST_WEBDAV_ROOT="$remote_root" TEST_FAIL_MOVE_ON_BASENAME="$(basename "$manifest")" CURL_BIN="$fake_bin/curl" \
    "$repo_root/scripts/builder/publish-engine-package.sh" \
    --package-url "$package_url" \
    --archive "$archive" \
    --manifest "$manifest" \
    --checksum "$checksum" \
    --staging-id cleanup-case; then
    fail "expected MOVE failure to abort publication"
fi
assert_not_exists "$stable_dir/$(basename "$archive")"
assert_not_exists "$stable_dir/$(basename "$manifest")"
assert_not_exists "$stable_dir/$(basename "$checksum")"
assert_not_exists "$remote_root/packages/engine-packages/.staging/cleanup-case"

if TEST_WEBDAV_ROOT="$remote_root" TEST_OPTIONS_ALLOW="OPTIONS, MKCOL, PUT, DELETE" CURL_BIN="$fake_bin/curl" \
    "$repo_root/scripts/builder/publish-engine-package.sh" \
    --package-url "$package_url" \
    --archive "$archive" \
    --manifest "$manifest" \
    --checksum "$checksum" \
    --staging-id missing-move; then
    fail "expected missing MOVE capability to fail preflight"
fi

echo "PASS: publish engine package"
