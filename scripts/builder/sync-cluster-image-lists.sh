#!/usr/bin/env bash
# Generate or verify exact cluster package inputs from pkg/releaseprofile.
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/builder/sync-cluster-image-lists.sh [--write|--check]

  --write   regenerate committed ClusterProfile manifests and image lists (default)
  --check   fail when the committed generated artifacts are stale
EOF
}

mode="write"
case "${1:---write}" in
    --write)
        mode="write"
        ;;
    --check)
        mode="check"
        ;;
    -h|--help)
        usage
        exit 0
        ;;
    *)
        usage >&2
        exit 2
        ;;
esac

root=$(git rev-parse --show-toplevel)
cd "$root"

output="scripts/builder/image-lists/cluster/generated"

case "$mode" in
    write)
        go generate ./pkg/releaseprofile
        ;;
    check)
        temporary_output=$(mktemp -d)
        trap 'rm -rf -- "$temporary_output"' EXIT
        go run ./pkg/releaseprofile/internal/generate --output "$temporary_output"
        if ! diff -ruN "$output" "$temporary_output"; then
            echo "ERROR: generated cluster package artifacts are stale." >&2
            echo "FIX: run 'make sync-cluster-image-lists' and stage the result." >&2
            exit 1
        fi
        ;;
esac
