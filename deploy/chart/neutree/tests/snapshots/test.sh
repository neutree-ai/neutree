#!/bin/bash
# Helm Chart Snapshot Testing Script
# This script renders templates and uses git diff to check for changes

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="${SCRIPT_DIR}/../.."
CASES_DIR="${SCRIPT_DIR}/cases"
EXPECTED_DIR="${SCRIPT_DIR}/expected"

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Test statistics
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0
FAILED_CASES=()

# Check required dependencies
check_dependencies() {
    if ! command -v helm &> /dev/null; then
        echo -e "${RED}Error: helm is not installed${NC}"
        exit 1
    fi

    if ! command -v git &> /dev/null; then
        echo -e "${RED}Error: git is not installed${NC}"
        exit 1
    fi
}

# Render Helm template with values file
render_template() {
    local values_file="$1"

    helm template neutree-test "$CHART_DIR" \
        --namespace neutree-test \
        --values "$values_file" 2>&1
}

# Generate snapshot for a single case
generate_snapshot() {
    local case_file="$1"
    local case_name=$(basename "$case_file" .yaml)
    local output_file="${EXPECTED_DIR}/${case_name}.yaml"

    # Create directory
    mkdir -p "${EXPECTED_DIR}"

    # Render and save (overwrites existing file)
    if ! render_template "$case_file" > "$output_file" 2>&1; then
        echo -e "${RED}✗ Failed to render template${NC}"
        cat "$output_file"
        return 1
    fi

    return 0
}

# Test a single case by rendering and checking git diff
test_case() {
    local case_file="$1"
    local case_name=$(basename "$case_file" .yaml)
    local expected_file="${EXPECTED_DIR}/${case_name}.yaml"

    echo ""
    echo "========================================"
    echo "Testing: ${case_name}"
    echo "========================================"

    ((TESTS_RUN++)) || true

    # Check if expected file exists
    if [ ! -f "$expected_file" ]; then
        echo -e "${YELLOW}⚠ Expected file not found: ${expected_file}${NC}"
        echo -e "${YELLOW}Run 'make update-helm-snapshots' to create it${NC}"
        ((TESTS_FAILED++)) || true
        FAILED_CASES+=("$case_name")
        return 1
    fi

    # Generate snapshot (overwrites expected file)
    echo "Rendering template..."
    if ! generate_snapshot "$case_file"; then
        ((TESTS_FAILED++)) || true
        FAILED_CASES+=("$case_name")
        return 1
    fi

    # Check if file changed using git
    echo "Checking for changes..."
    if git diff --quiet "$expected_file" 2>/dev/null; then
        echo -e "${GREEN}✓ No changes detected${NC}"
        ((TESTS_PASSED++)) || true
        return 0
    else
        echo -e "${RED}✗ Changes detected${NC}"
        echo ""
        echo "Git diff:"
        git diff "$expected_file" > ${SCRIPT_DIR}/diff_${case_name}.txt || true
        cat ${SCRIPT_DIR}/diff_${case_name}.txt
        rm -f ${SCRIPT_DIR}/diff_${case_name}.txt
        echo ""
        echo -e "${YELLOW}If this change is expected:${NC}"
        echo -e "${YELLOW}  git add ${expected_file}${NC}"
        echo -e "${YELLOW}Or revert the changes:${NC}"
        echo -e "${YELLOW}  git checkout ${expected_file}${NC}"
        ((TESTS_FAILED++)) || true
        FAILED_CASES+=("$case_name")
        return 1
    fi
}

# Print test summary
print_summary() {
    echo ""
    echo "========================================"
    echo "Test Summary"
    echo "========================================"
    echo "Total:  ${TESTS_RUN}"
    echo -e "Passed: ${GREEN}${TESTS_PASSED}${NC}"
    echo -e "Failed: ${RED}${TESTS_FAILED}${NC}"

    if [ ${#FAILED_CASES[@]} -gt 0 ]; then
        echo ""
        echo "Failed cases:"
        for case in "${FAILED_CASES[@]}"; do
            echo -e "  ${RED}- ${case}${NC}"
        done
    fi

    echo "========================================"

    if [ $TESTS_FAILED -eq 0 ]; then
        echo -e "${GREEN}All tests passed!${NC}"
        return 0
    else
        echo -e "${RED}Some tests failed!${NC}"
        return 1
    fi
}

# Main function
main() {
    check_dependencies

    echo "Helm Template Snapshot Tests"
    echo "Chart: ${CHART_DIR}"
    echo "Using git diff to detect changes"
    echo ""

    if [ -n "$1" ]; then
        # Test specific case
        case_file="${CASES_DIR}/$1.yaml"
        if [ ! -f "$case_file" ]; then
            echo -e "${RED}Error: Case file not found: ${case_file}${NC}"
            exit 1
        fi
        test_case "$case_file"
    else
        # Test all cases
        for case_file in "${CASES_DIR}"/*.yaml; do
            if [ -f "$case_file" ]; then
                test_case "$case_file"
            fi
        done
    fi

    print_summary
}

main "$@"
