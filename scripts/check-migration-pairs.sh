#!/usr/bin/env bash
# New migrations must ship an .up.sql and .down.sql in the same commit.
# Historical exceptions (001, 002, 003, 005) are grandfathered — this script only
# inspects newly added files.
#
# Modes:
#   no arg            — pre-commit mode: inspects staged additions (git diff --cached)
#   <ref>             — CI mode: inspects additions on HEAD since merge-base with <ref>
#                       (e.g. 'origin/main' for PR-against-main)
#
# See contributing/database.md#migration-rules
set -e

FAIL=0
BASE="${1:-}"
if [ -n "$BASE" ]; then
  ADDED=$(git diff --name-only --diff-filter=A "$BASE"...HEAD 2>/dev/null || true)
else
  ADDED=$(git diff --cached --name-only --diff-filter=A 2>/dev/null || true)
fi

# Process new *.up.sql under db/migrations/
while IFS= read -r up; do
  [ -z "$up" ] && continue
  down="${up%.up.sql}.down.sql"
  if ! echo "$ADDED" | grep -qxF "$down"; then
    echo "❌ New up migration without matching down: $up"
    echo "   ✅ FIX: create $down in the same commit"
    echo "   📖 See: contributing/database.md#migration-rules"
    echo
    FAIL=1
  fi
done <<EOF
$(echo "$ADDED" | grep -E '^db/migrations/[0-9]+_.*\.up\.sql$' || true)
EOF

exit $FAIL
