#!/usr/bin/env bash

set -euo pipefail

usage() {
  printf 'Usage: sanitize-e2e-log.sh <input-log> <output-log|->\n' >&2
}

[[ $# -eq 2 ]] || {
  usage
  exit 1
}

input_path="$1"
output_path="$2"
[[ -f "$input_path" ]] || {
  printf 'sanitize-e2e-log: input log does not exist: %s\n' "$input_path" >&2
  exit 1
}

sanitize() {
  awk '
    {
      line = tolower($0)
      if (line ~ /(authorization|api[_ -]?key|password|token|secret|credential|private[ _-]?key|kubeconfig)/) {
        print "[REDACTED SENSITIVE LOG LINE]"
        next
      }
      print
    }
  ' "$input_path"
}

if [[ "$output_path" == "-" ]]; then
  sanitize
  exit 0
fi

output_dir="$(dirname "$output_path")"
[[ -d "$output_dir" ]] || {
  printf 'sanitize-e2e-log: output directory does not exist: %s\n' "$output_dir" >&2
  exit 1
}

temp_path="$(mktemp "$output_dir/.$(basename "$output_path").XXXXXX")"
cleanup() {
  rm -f "$temp_path"
}
trap cleanup EXIT

sanitize >"$temp_path"
mv -f "$temp_path" "$output_path"
trap - EXIT
