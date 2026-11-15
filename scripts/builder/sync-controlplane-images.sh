#!/usr/bin/env bash
# Sync or verify the control-plane offline image list from the Helm chart and
# the docker compose file.
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/builder/sync-controlplane-images.sh [--write|--check|--stdout]

  --write   update scripts/builder/image-lists/controlplane/images.txt (default)
  --check   fail if the checked-in image list is stale
  --stdout  print the generated list
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
    --stdout)
        mode="stdout"
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

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

OUTPUT="scripts/builder/image-lists/controlplane/images.txt"
COMPOSE_FILE="deploy/docker/neutree-core/docker-compose.yml"

require_command() {
    local command_name="$1"
    if ! command -v "$command_name" >/dev/null 2>&1; then
        echo "ERROR: required command not found: $command_name" >&2
        exit 1
    fi
}

normalize_image() {
    local image="$1"
    image="${image#docker.io/}"
    image="${image#library/}"
    printf '%s\n' "$image"
}

extract_images() {
    awk '
        /^[[:space:]]*(image|imageName):[[:space:]]*/ {
            line = $0
            sub(/^[[:space:]]*(image|imageName):[[:space:]]*/, "", line)
            sub(/[[:space:]]+#.*$/, "", line)
            gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
            gsub(/^["'\'']|["'\'']$/, "", line)
            if (line != "") {
                print line
            }
        }
    '
}

collect_helm_images() {
    require_command helm
    helm template neutree ./deploy/chart/neutree \
        --set jwtSecret=offline-image-list-placeholder \
        --set api.image.tag=latest \
        --set core.image.tag=latest \
        --set dbScripts.image.tag=latest \
        | extract_images
}

# Compose images whose tags are filled in by neutree-cli are rendered by the
# chart as well; the rest are taken as written.
collect_compose_images() {
    extract_images <"$COMPOSE_FILE" | grep -v '{{'
}

generate_images() {
    { collect_helm_images; collect_compose_images; } | while IFS= read -r image; do
        if [[ "$image" == *"{{"* || "$image" == *"}}"* ]]; then
            echo "ERROR: unresolved image template placeholder: $image" >&2
            exit 1
        fi
        normalize_image "$image"
    done | sort -u
}

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
generate_images > "$tmp"

case "$mode" in
    write)
        cp "$tmp" "$OUTPUT"
        ;;
    check)
        if ! diff -u "$OUTPUT" "$tmp"; then
            echo "ERROR: $OUTPUT is stale." >&2
            echo "FIX: run 'make sync-images-list' and stage the result." >&2
            exit 1
        fi
        ;;
    stdout)
        cat "$tmp"
        ;;
esac
