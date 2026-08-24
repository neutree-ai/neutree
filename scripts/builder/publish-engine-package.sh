#!/usr/bin/env bash

set -euo pipefail

CURL_BIN="${CURL_BIN:-curl}"

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

log() {
    echo "[INFO] $*"
}

usage() {
    cat <<'EOF'
Usage: publish-engine-package.sh [OPTIONS]

Publish a built Engine Package to the internal file server via WebDAV.

Required options:
  --package-url URL
  --archive FILE
  --manifest FILE
  --checksum FILE

Optional options:
  --staging-id ID
EOF
}

require_value() {
    local option_name="$1"
    local option_value="${2:-}"

    if [[ -z "$option_value" || "$option_value" == -* ]]; then
        fail "$option_name requires a value"
    fi
}

trim_cr() {
    tr -d '\r'
}

validate_https_url() {
    local value="$1"
    local authority

    [[ "$value" =~ ^https://[^[:space:]\"\\]+$ && "$value" != *\?* && "$value" != *\#* ]] || fail "invalid HTTPS URL: $value"
    authority="${value#https://}"
    authority="${authority%%/*}"
    [[ "$authority" != *"@"* ]] || fail "HTTPS URL must not include credentials: $value"
}

join_url() {
    local base="${1%/}"
    local suffix="${2#/}"

    printf '%s/%s\n' "$base" "$suffix"
}

delete_url_quietly() {
    local target_url="$1"
    local status

    status=$(curl_https --silent --show-error --output /dev/null --write-out '%{http_code}' \
        --request DELETE "$target_url")
    [[ "$status" =~ ^(200|204|404)$ ]] || return 1
}

curl_https() {
    "$CURL_BIN" --proto '=https' --proto-redir '=https' "$@"
}

sha256_stream() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256
    else
        fail "sha256sum or shasum is required to verify the staged archive"
    fi
}

package_url=""
archive_path=""
manifest_path=""
checksum_path=""
staging_id="${GITHUB_RUN_ID:-manual}-${GITHUB_RUN_ATTEMPT:-0}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --package-url)
            require_value "$1" "${2:-}"
            package_url="$2"
            shift 2
            ;;
        --archive)
            require_value "$1" "${2:-}"
            archive_path="$2"
            shift 2
            ;;
        --manifest)
            require_value "$1" "${2:-}"
            manifest_path="$2"
            shift 2
            ;;
        --checksum)
            require_value "$1" "${2:-}"
            checksum_path="$2"
            shift 2
            ;;
        --staging-id)
            require_value "$1" "${2:-}"
            staging_id="$2"
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

for option_name in package_url archive_path manifest_path checksum_path staging_id; do
    [[ -n "${!option_name}" ]] || fail "--${option_name//_/-} is required"
done

validate_https_url "$package_url"
[[ "$staging_id" =~ ^[A-Za-z0-9._-]+$ ]] || fail "--staging-id must match [A-Za-z0-9._-]+"

for local_file in "$archive_path" "$manifest_path" "$checksum_path"; do
    [[ -f "$local_file" ]] || fail "file does not exist: $local_file"
done

archive_name="$(basename "$archive_path")"
manifest_name="$(basename "$manifest_path")"
checksum_name="$(basename "$checksum_path")"
[[ "$archive_name" == *.tar.gz ]] || fail "--archive must point to a .tar.gz file"
[[ "$package_url" == */"$archive_name" ]] || fail "--package-url must end with $archive_name"
[[ "$checksum_name" == "$archive_name.sha256" ]] || fail "--checksum must be named $archive_name.sha256"

stable_dir_url="${package_url%/*}"
release_marker="/engine-packages/"
case "$package_url" in
    *"$release_marker"*)
        file_server_base_url="${package_url%%"$release_marker"*}"
        ;;
    *)
        fail "--package-url must include $release_marker"
        ;;
esac
validate_https_url "$file_server_base_url"

staging_dir_url="$(join_url "$file_server_base_url" "engine-packages/.staging/$staging_id")"
staging_archive_url="$(join_url "$staging_dir_url" "$archive_name")"
staging_manifest_url="$(join_url "$staging_dir_url" "$manifest_name")"
staging_checksum_url="$(join_url "$staging_dir_url" "$checksum_name")"
stable_manifest_url="$(join_url "$stable_dir_url" "$manifest_name")"
stable_checksum_url="$(join_url "$stable_dir_url" "$checksum_name")"

staging_files=("$staging_archive_url" "$staging_manifest_url" "$staging_checksum_url")
promoted_files=()
headers_file="$(mktemp)"

cleanup() {
    local exit_code=$?
    local index

    set +e
    for url in "${staging_files[@]}"; do
        delete_url_quietly "$url" >/dev/null 2>&1 || true
    done
    delete_url_quietly "$staging_dir_url" >/dev/null 2>&1 || true

    if (( exit_code != 0 )); then
        for (( index=${#promoted_files[@]}-1; index>=0; index-- )); do
            delete_url_quietly "${promoted_files[$index]}" >/dev/null 2>&1 || true
        done
    fi

    rm -f "$headers_file"
}
trap cleanup EXIT

curl_status() {
    local method="$1"
    local target_url="$2"
    shift 2

    curl_https --silent --show-error --output /dev/null --write-out '%{http_code}' \
        --request "$method" "$@" "$target_url"
}

create_remote_dir() {
    local target_url="$1"
    local relative_path="${target_url#"$file_server_base_url"}"
    local current_url="$file_server_base_url"
    local segment
    local status

    IFS='/' read -r -a segments <<< "${relative_path#/}"
    for segment in "${segments[@]}"; do
        [[ -n "$segment" ]] || continue
        current_url="$(join_url "$current_url" "$segment")"
        status=$(curl_status MKCOL "$current_url")
        [[ "$status" =~ ^(201|405)$ ]] || fail "MKCOL $current_url failed with HTTP $status"
    done
}

upload_file() {
    local local_file="$1"
    local target_url="$2"
    local status

    status=$(curl_status PUT "$target_url" --upload-file "$local_file")
    [[ "$status" =~ ^(200|201|204)$ ]] || fail "PUT $target_url failed with HTTP $status"
}

move_file() {
    local source_url="$1"
    local target_url="$2"
    local status

    status=$(curl_status MOVE "$source_url" \
        --header "Destination: $target_url" \
        --header "Overwrite: F")
    case "$status" in
        201|204)
            promoted_files+=("$target_url")
            ;;
        412)
            fail "stable artifact already exists: $target_url"
            ;;
        *)
            fail "MOVE $source_url -> $target_url failed with HTTP $status"
            ;;
    esac
}

options_status=$(curl_https --silent --show-error --output /dev/null --dump-header "$headers_file" \
    --write-out '%{http_code}' --request OPTIONS "${file_server_base_url%/}/")
[[ "$options_status" =~ ^(200|204|207)$ ]] || fail "OPTIONS $file_server_base_url failed with HTTP $options_status"

allow_header="$(awk 'BEGIN{IGNORECASE=1} /^Allow:/{sub(/^Allow:[[:space:]]*/,""); print; exit}' "$headers_file" | trim_cr)"
[[ -n "$allow_header" ]] || fail "OPTIONS $file_server_base_url did not return an Allow header"
for required_method in MKCOL PUT MOVE DELETE; do
    printf '%s\n' "$allow_header" | grep -Eq "(^|[[:space:],])${required_method}([[:space:],]|$)" \
        || fail "file server Allow header is missing $required_method: $allow_header"
done
rm -f "$headers_file"

ensure_stable_target_absent() {
    local target_url="$1"
    local status

    status=$(curl_https --silent --show-error --head --output /dev/null --write-out '%{http_code}' "$target_url")
    case "$status" in
        200|204|301|302|303|307|308)
            fail "stable artifact already exists: $target_url"
            ;;
        404)
            ;;
        *)
            fail "HEAD $target_url returned unexpected HTTP $status"
            ;;
    esac
}

for stable_target_url in "$package_url" "$stable_checksum_url" "$stable_manifest_url"; do
    ensure_stable_target_absent "$stable_target_url"
done

create_remote_dir "$stable_dir_url"
create_remote_dir "$staging_dir_url"

log "Uploading package artifacts to staging"
upload_file "$archive_path" "$staging_archive_url"
upload_file "$manifest_path" "$staging_manifest_url"
upload_file "$checksum_path" "$staging_checksum_url"

expected_sha256="$(awk '{print $1}' "$checksum_path")"
actual_sha256=$(curl_https --silent --show-error --fail --location "$staging_archive_url" | sha256_stream | awk '{print $1}')
[[ "$actual_sha256" == "$expected_sha256" ]] || fail "remote archive checksum mismatch: expected $expected_sha256, got $actual_sha256"

log "Promoting staged artifacts to stable release path"
move_file "$staging_archive_url" "$package_url"
move_file "$staging_checksum_url" "$stable_checksum_url"
move_file "$staging_manifest_url" "$stable_manifest_url"

printf 'package_url=%s\nmanifest_url=%s\nchecksum_url=%s\n' \
    "$package_url" "$stable_manifest_url" "$stable_checksum_url"
