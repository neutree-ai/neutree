#!/usr/bin/env bash
# Migration hygiene gates for db/migrations/:
#   1. historical migrations are immutable once they land on the base branch
#   2. pair rule        — every new migration ships matching *.up.sql and *.down.sql files
#   3. version uniqueness — every NNN_ prefix is claimed by exactly one migration
#   4. new migrations must not duplicate seed-owned PostgREST reload NOTIFY
#
# Gate 3 exists because git cannot see the collision: 085_a.up.sql and
# 085_b.up.sql are two unrelated new files, so two PRs adding the same number
# both merge green. golang-migrate is what rejects the duplicate, when the
# deploy runs the migrations — so the breakage lands far from the change.
#
# Modes:
#   no arg            — pre-commit mode: inspects staged additions (git diff --cached)
#   <ref>             — CI mode: inspects additions on HEAD since merge-base with <ref>
#                       (e.g. 'origin/main' for PR-against-main)
#
# Gate 1 faults modifications, deletions, and renames of migrations that
# already exist on the base branch.
#
# Gate 4 only inspects new migration files. Historical releases may already
# contain the notification and are left untouched.
#
# See contributing/database.md#migration-rules
set -euo pipefail

FAIL=0
BASE="${1:-}"
if [ -n "$BASE" ]; then
  if ! git rev-parse --verify --quiet "$BASE^{commit}" >/dev/null 2>&1; then
    echo "❌ Migration guard cannot resolve base ref: $BASE" >&2
    echo "   ✅ FIX: fetch the PR base branch before running this guard" >&2
    exit 2
  fi
  if ! git merge-base "$BASE" HEAD >/dev/null; then
    echo "❌ Migration guard cannot find a merge base for: $BASE" >&2
    echo "   ✅ FIX: fetch enough history for the PR base branch before running this guard" >&2
    exit 2
  fi

  CHANGED=$(git diff --name-status --find-renames "$BASE"...HEAD -- db/migrations/)
  ADDED=$(git diff --name-only --diff-filter=A "$BASE"...HEAD -- db/migrations/)
  # A long-lived branch can claim a version that the base branch gained after
  # the branch point. Compare both current trees, not just HEAD.
  TREE=$(
    {
      git ls-tree -r --name-only "$BASE" -- db/migrations/
      git ls-tree -r --name-only HEAD -- db/migrations/
    } | sort -u
  )
  CONTENT_REF="HEAD"
else
  CHANGED=$(git diff --cached --name-status --find-renames -- db/migrations/)
  ADDED=$(git diff --cached --name-only --diff-filter=A -- db/migrations/)
  TREE=$(git ls-files --cached -- db/migrations/)
  CONTENT_REF=":"
fi

MIGRATION_FILE_RE='^db/migrations/[0-9]+_.*\.(up|down)\.sql$'
PGRST_NOTIFY_RE="NOTIFY[[:space:]]+pgrst[[:space:]]*,[[:space:]]*['\"]reload[[:space:]]+schema['\"]"

# Migration name ("085_foo") for every path in a newline-separated file list.
migration_names() {
  grep -E "$MIGRATION_FILE_RE" \
    | sed -E 's#^db/migrations/##; s#\.(up|down)\.sql$##' \
    | sort -u
}

show_migration() {
  local path="$1"
  if [ "$CONTENT_REF" = ":" ]; then
    git show ":$path"
  else
    git show "HEAD:$path"
  fi
}

# ── Gate 1: historical migrations are immutable ──
while IFS=$'\t' read -r status source target; do
  [ -z "$status" ] && continue

  case "$status" in
    M|T)
      if [[ "$source" =~ $MIGRATION_FILE_RE ]]; then
        echo "❌ Historical migration file cannot be modified: $source"
        echo "   ✅ FIX: keep released migration files unchanged and add a later migration to reverse them"
        echo "   📖 See: contributing/database.md#migration-rules"
        echo
        FAIL=1
      fi
      ;;
    D)
      if [[ "$source" =~ $MIGRATION_FILE_RE ]]; then
        echo "❌ Historical migration file cannot be deleted: $source"
        echo "   ✅ FIX: restore the released file and add a later migration to reverse it"
        echo "   📖 See: contributing/database.md#migration-rules"
        echo
        FAIL=1
      fi
      ;;
    R*)
      if [[ "$source" =~ $MIGRATION_FILE_RE ]] || [[ "$target" =~ $MIGRATION_FILE_RE ]]; then
        echo "❌ Historical migration file cannot be renamed: $source -> $target"
        echo "   ✅ FIX: keep released migration files unchanged and add a new migration instead"
        echo "   📖 See: contributing/database.md#migration-rules"
        echo
        FAIL=1
      fi
      ;;
  esac
done <<EOF
$CHANGED
EOF

# ── Gate 2: up/down pairs ──
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

while IFS= read -r down; do
  [ -z "$down" ] && continue
  up="${down%.down.sql}.up.sql"
  if ! echo "$ADDED" | grep -qxF "$up"; then
    echo "❌ New down migration without matching up: $down"
    echo "   ✅ FIX: create $up in the same commit"
    echo "   📖 See: contributing/database.md#migration-rules"
    echo
    FAIL=1
  fi
done <<EOF
$(echo "$ADDED" | grep -E '^db/migrations/[0-9]+_.*\.down\.sql$' || true)
EOF

# ── Gate 3: version uniqueness ──
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

# ── Gate 4: seed owns the PostgREST reload NOTIFY ──
while IFS= read -r path; do
  [ -z "$path" ] && continue
  if ! [[ "$path" =~ $MIGRATION_FILE_RE ]]; then
    continue
  fi

  if show_migration "$path" | tr '\n' ' ' | grep -Eiq "$PGRST_NOTIFY_RE"; then
    echo "❌ New migration duplicates the seed-owned PostgREST reload NOTIFY: $path"
    echo "   ✅ FIX: remove the NOTIFY and keep schema reload in db/seed/999_notify_pgrst.sql"
    echo "   📖 See: contributing/database.md#migration-rules"
    echo
    FAIL=1
  fi
done <<EOF
$ADDED
EOF

exit $FAIL
