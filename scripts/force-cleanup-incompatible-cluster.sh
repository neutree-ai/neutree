#!/usr/bin/env bash
# Remove only a Cluster that the control plane has classified as incompatible.
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: force-cleanup-incompatible-cluster.sh --name NAME --workspace WORKSPACE --confirm

The script reads the Cluster status through neutree-cli and only force-deletes
Clusters in Unsupported or Retired phase. Authentication and server settings
are inherited from the normal neutree-cli flags or NEUTREE_* environment.
EOF
}

name=""
workspace=""
confirmed=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)
            name="${2:-}"
            shift 2
            ;;
        --workspace)
            workspace="${2:-}"
            shift 2
            ;;
        --confirm)
            confirmed=true
            shift
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            echo "unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

if [[ -z "$name" || -z "$workspace" || "$confirmed" != true ]]; then
    echo "--name, --workspace, and --confirm are required" >&2
    usage >&2
    exit 2
fi

cli_bin="${NEUTREE_CLI_BIN:-neutree-cli}"
cluster_json="$($cli_bin get Cluster "$name" --workspace "$workspace" --output json)"
phase="$(jq -er '.status.phase' <<<"$cluster_json")"

case "$phase" in
    Unsupported|Retired)
        ;;
    *)
        echo "refusing force cleanup for Cluster $workspace/$name in phase $phase; expected Unsupported or Retired" >&2
        exit 1
        ;;
esac

exec "$cli_bin" delete Cluster "$name" --workspace "$workspace" --force --wait
