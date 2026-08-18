#!/usr/bin/env bash
# Migration hygiene gates for db/migrations/:
#   1. pair rule        — every new *.up.sql ships a matching *.down.sql
#   2. version uniqueness — every NNN_ prefix is claimed by exactly one migration
#
# Gate 2 exists because git cannot see the collision: 085_a.up.sql and
# 085_b.up.sql are two unrelated new files, so two PRs adding the same number
# both merge green. golang-migrate is what rejects the duplicate, when the
# deploy runs the migrations — so the breakage lands far from the change.
#
# Modes:
#   no arg            — pre-commit mode: inspects staged additions (git diff --cached)
#   <ref>             — CI mode: inspects additions on HEAD since merge-base with <ref>
#                       (e.g. 'origin/main' for PR-against-main)
#
# Both gates only fault files this branch adds; pre-existing state is grandfathered.
#
# See contributing/database.md#migration-rules
set -e

FAIL=0
BASE="${1:-}"
if [ -n "$BASE" ]; then
  ADDED=$(git diff --name-only --diff-filter=A "$BASE"...HEAD 2>/dev/null || true)
  TREE=$(git ls-tree -r --name-only HEAD -- db/migrations/ 2>/dev/null || true)
else
  ADDED=$(git diff --cached --name-only --diff-filter=A 2>/dev/null || true)
  TREE=$(git ls-files --cached -- db/migrations/ 2>/dev/null || true)
fi

MIGRATION_FILE_RE='^db/migrations/[0-9]+_.*\.(up|down)\.sql$'

# Migration name ("085_foo") for every path in a newline-separated file list.
migration_names() {
  grep -E "$MIGRATION_FILE_RE" \
    | sed -E 's#^db/migrations/##; s#\.(up|down)\.sql$##' \
    | sort -u
}

# ── Gate 1: up/down pairs ──
# Historical exceptions (001, 002, 003, 005) are grandfathered — only newly
# added files are inspected.
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

# ── Gate 2: version uniqueness ──
ALL_NAMES=$(echo "$TREE" | migration_names || true)
NEW_NAMES=$(echo "$ADDED" | migration_names || true)

# Leading zeros are not significant to golang-migrate: 085 and 85 are the same
# version, so compare the numeric value rather than the literal prefix.
version_of() { printf '%d' "$((10#${1%%_*}))"; }

DUP_VERSIONS=$(
  for name in $ALL_NAMES; do
    version_of "$name"
    echo
  done | sort -n | uniq -d
)

for version in $DUP_VERSIONS; do
  group=""
  for name in $ALL_NAMES; do
    [ "$(version_of "$name")" = "$version" ] && group="$group $name"
  done

  # Grandfather duplicates that already existed before this branch.
  introduced=""
  for name in $NEW_NAMES; do
    [ "$(version_of "$name")" = "$version" ] && introduced="$introduced $name"
  done
  [ -z "$introduced" ] && continue

  echo "❌ Two migrations claim the same version number:"
  for name in $group; do
    echo "   - $name"
  done
  echo "   ✅ FIX: renumber the migration this branch adds ($(echo "$introduced" | xargs)) above the highest number on the base branch:"
  echo "        ls db/migrations | sed -E 's/_.*//' | sort -n | tail -1"
  echo "   📖 See: contributing/database.md#migration-rules"
  echo
  FAIL=1
done

exit $FAIL
