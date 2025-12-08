#!/bin/bash
# Update Helm Chart Snapshots
# This script renders templates and saves them as expected snapshots

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../.."
CASES_DIR="${SCRIPT_DIR}/cases"
EXPECTED_DIR="${SCRIPT_DIR}/expected"

# Color output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

# Check required dependencies
check_dependencies() {
    if ! command -v helm &> /dev/null; then
        echo "Error: helm is not installed"
        exit 1
    fi
}

# Render Helm template with values file
render_template() {
    local values_file="$1"

    helm template neutree-test "$CHART_DIR" \
        --namespace neutree-test \
        --values "$values_file"
}

# Update snapshot for a single case
update_snapshot() {
    local case_file="$1"
    local case_name=$(basename "$case_file" .yaml)
    local output_file="${EXPECTED_DIR}/${case_name}.yaml"

    echo -e "${BLUE}Updating snapshot: ${case_name}${NC}"

    # Create directory
    mkdir -p "${EXPECTED_DIR}"

    # Render and save
    if render_template "$case_file" > "$output_file" 2>&1; then
        echo -e "${GREEN}✓ Snapshot saved: ${output_file}${NC}"
    else
        echo -e "${RED}✗ Failed to render template${NC}"
        cat "$output_file"
        rm -f "$output_file"
        return 1
    fi
}

# Main function
main() {
    check_dependencies

    echo "Updating Helm Template Snapshots"
    echo ""

    if [ -n "$1" ]; then
        # Update specific snapshot
        case_file="${CASES_DIR}/$1.yaml"
        if [ ! -f "$case_file" ]; then
            echo "Error: Case file not found: ${case_file}"
            exit 1
        fi
        update_snapshot "$case_file"
    else
        # Update all snapshots
        for case_file in "${CASES_DIR}"/*.yaml; do
            if [ -f "$case_file" ]; then
                update_snapshot "$case_file"
            fi
        done
    fi

    echo ""
    echo -e "${GREEN}Done!${NC}"
    echo ""
    echo "Review changes with:"
    echo "  git diff ${EXPECTED_DIR}"
    echo ""
    echo "Stage changes with:"
    echo "  git add ${EXPECTED_DIR}"
}

main "$@"
