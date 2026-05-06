#!/usr/bin/env bash
# New migrations must ship an .up.sql and .down.sql in the same commit.
# Historical exceptions (001, 002, 003, 005) are grandfathered — this hook only
# inspects newly added files in the current commit.
# See contributing/database.md#migration-rules
set -e

FAIL=0
ADDED=$(git diff --cached --name-only --diff-filter=A 2>/dev/null || true)

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
