#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: generate-profile.sh --output <path> [--github-env <path>]

Decodes E2E_PROFILE_B64 into an existing Profile-compatible YAML file. The
result is suitable for E2E_PROFILE_PATH and does not introduce a second
profile schema.
EOF
}

fail() {
  printf 'generate-profile: %s\n' "$*" >&2
  exit 1
}

decode_base64() {
  if base64 --decode </dev/null >/dev/null 2>&1; then
    base64 --decode
  else
    base64 -D
  fi
}

output_path=""
github_env=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output)
      [[ $# -ge 2 ]] || fail "--output requires a path"
      output_path="$2"
      shift 2
      ;;
    --github-env)
      [[ $# -ge 2 ]] || fail "--github-env requires a path"
      github_env="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      fail "unknown argument: $1"
      ;;
  esac
done

[[ -n "$output_path" ]] || fail "--output is required"
[[ -n "${E2E_PROFILE_B64:-}" ]] || fail "E2E_PROFILE_B64 must be set"

output_dir="$(dirname "$output_path")"
[[ -d "$output_dir" ]] || fail "output directory does not exist: $output_dir"
if [[ -e "$output_path" && ! -f "$output_path" ]]; then
  fail "output path is not a regular file: $output_path"
fi

umask 077
temp_path="$(mktemp "$output_dir/.$(basename "$output_path").XXXXXX")"
cleanup() {
  rm -f "$temp_path"
}
trap cleanup EXIT

if ! printf '%s' "$E2E_PROFILE_B64" | decode_base64 >"$temp_path"; then
  fail "E2E_PROFILE_B64 is not valid base64"
fi
[[ -s "$temp_path" ]] || fail "decoded profile is empty"

chmod 600 "$temp_path"
mv -f "$temp_path" "$output_path"
trap - EXIT

if [[ -n "$github_env" ]]; then
  printf 'E2E_PROFILE_PATH=%s\n' "$output_path" >>"$github_env"
fi

printf '%s\n' "$output_path"
