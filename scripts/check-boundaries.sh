#!/usr/bin/env bash
# Architecture import-direction checks.
# Enforces the layering described in contributing/architecture.md#layered-architecture.
#
# Layer hierarchy (downward-only):
#   L4 cmd/        →  L3 controllers/  →  L2 internal/  →  L1 pkg/  →  L0 api/v1/
#
# `pkg/scheme` is exempt from layering (foundational type-registration framework,
# treated as Go-runtime-equivalent infrastructure per architecture.md).
set -e

FAIL=0

report() {
  echo "❌ $1"
  echo "   ✅ FIX: $2"
  echo "   📖 See: contributing/architecture.md#layered-architecture"
  echo
  FAIL=1
}

# Helper: list every Go file (excluding tests) under $1 that imports any path matching $2.
# Uses grep -lE on full import lines so a comment containing the string isn't matched.
imports_into() {
  local from_dir="$1"
  local pattern="$2"
  grep -rlE "\"$pattern" "$from_dir" --include='*.go' 2>/dev/null || true
}

# ─────────────────────────────────────────────────────────────────────────────
# R1: pkg/ (L1) must not import internal/ (L2).
# Grandfathered: pkg/model_registry/file_based.go imports internal/nfs.
#   Resolve by promoting internal/nfs to pkg/nfs, or by injecting the helper through an interface.
# ─────────────────────────────────────────────────────────────────────────────
R1=$(imports_into pkg/ 'github\.com/neutree-ai/neutree/internal/' \
  | grep -vxF 'pkg/model_registry/file_based.go' || true)
if [ -n "$R1" ]; then
  echo "$R1" | sed 's/^/   /'
  report \
    "R1: pkg/ imports internal/" \
    "lift shared code out of internal/ into pkg/, or push the pkg/ code down into internal/"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R2: api/v1/ (L0) must not import internal/ (L2) or controllers/ (L3).
# ─────────────────────────────────────────────────────────────────────────────
R2=$(imports_into api/v1/ 'github\.com/neutree-ai/neutree/(internal|controllers)' || true)
if [ -n "$R2" ]; then
  echo "$R2" | sed 's/^/   /'
  report \
    "R2: api/v1/ imports internal/ or controllers/" \
    "move the offending type or helper out of api/v1/; api/v1 must only define resource types"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R3: internal/orchestrator/ must not import internal/routes/.
# Specialization of R5 below; kept explicit so the dual-orchestrator constraint is obvious.
# ─────────────────────────────────────────────────────────────────────────────
R3=$(imports_into internal/orchestrator/ 'github\.com/neutree-ai/neutree/internal/routes' || true)
if [ -n "$R3" ]; then
  echo "$R3" | sed 's/^/   /'
  report \
    "R3: internal/orchestrator/ imports internal/routes/" \
    "pass the orchestrator an interface implemented by the route handler; never import routes from orchestrator"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R4: cmd/ files (L4) must stay under 500 lines.
# Entry points should only do wiring; business logic belongs in internal/.
# ─────────────────────────────────────────────────────────────────────────────
R4=""
while IFS= read -r f; do
  [ -z "$f" ] && continue
  lines=$(wc -l < "$f" | tr -d ' ')
  if [ "$lines" -gt 500 ]; then
    R4="${R4}${f} (${lines} lines)
"
  fi
done < <(find cmd -name '*.go' -not -name '*_test.go' 2>/dev/null || true)
if [ -n "$R4" ]; then
  printf '%s' "$R4" | sed 's/^/   /'
  report \
    "R4: cmd/ file exceeds 500 lines" \
    "extract business logic into internal/; cmd is for wiring and option parsing only"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R5: internal/routes is a terminal HTTP layer.
# No package outside internal/routes/ may import it.
# ─────────────────────────────────────────────────────────────────────────────
R5=""
while IFS= read -r f; do
  case "$f" in
    ""|internal/routes/*) ;;
    *) R5="${R5}${f}
" ;;
  esac
done < <(imports_into internal/ 'github\.com/neutree-ai/neutree/internal/routes')
if [ -n "$R5" ]; then
  printf '%s' "$R5" | sed 's/^/   /'
  report \
    "R5: internal/* (other than internal/routes) imports internal/routes" \
    "routes is the terminal HTTP layer; pass route handlers an interface from upstream callers"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R6: api/v1/ (L0) must not import pkg/ except pkg/scheme.
# pkg/scheme is the apimachinery/runtime equivalent — registering types into it
# is intrinsic to defining them. Anything else under pkg/ is L1 utility code
# that L0 must not reach upward into.
# ─────────────────────────────────────────────────────────────────────────────
R6=""
while IFS= read -r match; do
  [ -z "$match" ] && continue
  # match format: <file>:<line>:<full import line>
  file=${match%%:*}
  rest=${match#*:}
  rest=${rest#*:}
  # extract the pkg path between the quotes
  pkg=$(printf '%s\n' "$rest" | sed -E 's|.*"(github\.com/neutree-ai/neutree/pkg/[^"]+)".*|\1|')
  if [ "$pkg" != "github.com/neutree-ai/neutree/pkg/scheme" ]; then
    R6="${R6}${file}: imports ${pkg}
"
  fi
done < <(grep -rnE '"github\.com/neutree-ai/neutree/pkg/[^"]+"' api/v1/ --include='*.go' 2>/dev/null || true)
if [ -n "$R6" ]; then
  printf '%s' "$R6" | sed 's/^/   /'
  report \
    "R6: api/v1/ imports pkg/ (other than pkg/scheme)" \
    "api/v1 holds pure types; only pkg/scheme is allowed (foundational type-registration framework)"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R7: pkg/ (L1) must not import controllers/ (L3) or cmd/ (L4).
# Utility code never reaches up into reconcilers or entry points.
# ─────────────────────────────────────────────────────────────────────────────
R7=$(imports_into pkg/ 'github\.com/neutree-ai/neutree/(controllers|cmd)' || true)
if [ -n "$R7" ]; then
  echo "$R7" | sed 's/^/   /'
  report \
    "R7: pkg/ imports controllers/ or cmd/" \
    "pkg/ is L1 reusable utilities; depend downward on api/v1 only, never upward into controllers or cmd"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R8: internal/ (L2) must not import controllers/ (L3) or cmd/ (L4).
# Grandfathered: internal/cluster/component/metrics/manifests.go imports
#   cmd/neutree-cli/app/constants. Resolve by lifting the shared constants
#   into internal/ (or pkg/) so cli and the metrics manifests reference a
#   single source instead of cli being the constants owner.
# ─────────────────────────────────────────────────────────────────────────────
R8=$(imports_into internal/ 'github\.com/neutree-ai/neutree/(controllers|cmd)' \
  | grep -vxF 'internal/cluster/component/metrics/manifests.go' || true)
if [ -n "$R8" ]; then
  echo "$R8" | sed 's/^/   /'
  report \
    "R8: internal/ imports controllers/ or cmd/" \
    "internal/ is L2 business logic; controllers (L3) and cmd (L4) sit above it — invert via interfaces"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R9: controllers/ (L3) must not import cmd/ (L4).
# Reconcilers are wired by cmd, never the other way around.
# ─────────────────────────────────────────────────────────────────────────────
R9=$(imports_into controllers/ 'github\.com/neutree-ai/neutree/cmd' || true)
if [ -n "$R9" ]; then
  echo "$R9" | sed 's/^/   /'
  report \
    "R9: controllers/ imports cmd/" \
    "controllers are reconcilers; cmd wires them, never the inverse"
fi

# ─────────────────────────────────────────────────────────────────────────────
# R10: cmd/neutree-api and cmd/neutree-core are independent processes.
# Per architecture.md "Data Flow": both connect directly to PostgREST and do
# not call each other. Cross-imports between the two binaries indicate the
# split has been compromised.
# ─────────────────────────────────────────────────────────────────────────────
R10_AC=$(imports_into cmd/neutree-api/ 'github\.com/neutree-ai/neutree/cmd/neutree-core' || true)
R10_CA=$(imports_into cmd/neutree-core/ 'github\.com/neutree-ai/neutree/cmd/neutree-api' || true)
R10="${R10_AC}${R10_CA:+
$R10_CA}"
if [ -n "$R10" ]; then
  echo "$R10" | sed 's/^/   /'
  report \
    "R10: cmd/neutree-api ↔ cmd/neutree-core cross-import" \
    "the two control-plane processes are independent; share via api/v1, internal/, or pkg/ only"
fi

exit $FAIL
