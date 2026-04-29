#!/usr/bin/env bash
# Architecture import-direction checks.
# Enforces the layering described in contributing/architecture.md#layered-architecture.
set -e

FAIL=0

report() {
  echo "❌ $1"
  echo "   ✅ FIX: $2"
  echo "   📖 See: contributing/architecture.md#layered-architecture"
  echo
  FAIL=1
}

# R1: pkg/ (L0) must not import internal/ (L2).
# Grandfathered: pkg/model_registry/file_based.go imports internal/nfs.
#   Resolve by promoting internal/nfs to pkg/nfs, or by injecting the helper through an interface.
R1_VIOLATIONS=$(
  grep -rlE '"github\.com/neutree-ai/neutree/internal/' pkg/ --include='*.go' 2>/dev/null \
    | grep -vxF 'pkg/model_registry/file_based.go' || true
)
if [ -n "$R1_VIOLATIONS" ]; then
  echo "$R1_VIOLATIONS" | sed 's/^/   /'
  report \
    "pkg/ imports internal/" \
    "lift shared code out of internal/ into pkg/, or push the pkg/ code down into internal/"
fi

# R2: api/v1/ (L1) must not import internal/ (L2) or controllers/ (L3).
R2_VIOLATIONS=$(
  grep -rlE '"github\.com/neutree-ai/neutree/(internal|controllers)' api/v1/ --include='*.go' 2>/dev/null || true
)
if [ -n "$R2_VIOLATIONS" ]; then
  echo "$R2_VIOLATIONS" | sed 's/^/   /'
  report \
    "api/v1/ imports internal/ or controllers/" \
    "move the offending type or helper out of api/v1/; api/v1 must only define resource types"
fi

# R3: internal/orchestrator/ must not import internal/routes/.
# Specialization of R5 below; kept explicit so the dual-orchestrator constraint is obvious.
R3_VIOLATIONS=$(
  grep -rlE '"github\.com/neutree-ai/neutree/internal/routes' internal/orchestrator/ --include='*.go' 2>/dev/null || true
)
if [ -n "$R3_VIOLATIONS" ]; then
  echo "$R3_VIOLATIONS" | sed 's/^/   /'
  report \
    "internal/orchestrator/ imports internal/routes/" \
    "pass the orchestrator an interface implemented by the route handler; never import routes from orchestrator"
fi

# R4: cmd/ files (L4) must stay under 500 lines.
# Entry points should only do wiring; business logic belongs in internal/.
# Process substitution keeps the while loop in the main shell so set -e behaves predictably.
R4_VIOLATIONS=""
while IFS= read -r f; do
  [ -z "$f" ] && continue
  lines=$(wc -l < "$f" | tr -d ' ')
  if [ "$lines" -gt 500 ]; then
    R4_VIOLATIONS="${R4_VIOLATIONS}${f} (${lines} lines)
"
  fi
done < <(find cmd -name '*.go' -not -name '*_test.go' 2>/dev/null || true)
if [ -n "$R4_VIOLATIONS" ]; then
  printf '%s' "$R4_VIOLATIONS" | sed 's/^/   /'
  report \
    "cmd/ file exceeds 500 lines" \
    "extract business logic into internal/; cmd is for wiring and option parsing only"
fi

# R5: internal/routes is a terminal HTTP layer.
# No package outside internal/routes/ may import it.
R5_VIOLATIONS=""
while IFS= read -r f; do
  case "$f" in
    ""|internal/routes/*) ;;
    *) R5_VIOLATIONS="${R5_VIOLATIONS}${f}
" ;;
  esac
done < <(grep -rlE '"github\.com/neutree-ai/neutree/internal/routes' internal/ --include='*.go' 2>/dev/null || true)
if [ -n "$R5_VIOLATIONS" ]; then
  printf '%s' "$R5_VIOLATIONS" | sed 's/^/   /'
  report \
    "internal/* (other than internal/routes) imports internal/routes" \
    "routes is the terminal HTTP layer; pass route handlers an interface from upstream callers"
fi

exit $FAIL
