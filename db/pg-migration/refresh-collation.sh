#!/usr/bin/env bash
# Rebuilds the indexes of every database whose collation version differs from
# the one the server's C library provides, then records the new version. A new
# PostgreSQL image with another glibc sorts text differently, so indexes built
# under the old one can return wrong results. Does nothing when the versions
# match, so it is safe to run on every install and upgrade.
#
# Environment: TARGET_HOST, PGUSER, PGPASSWORD.
set -euo pipefail

log() { echo "$(date -u +%FT%TZ) $*"; }
tpsql() { psql -X -v ON_ERROR_STOP=1 -h "$TARGET_HOST" "$@"; }

# template1 is included so that new databases do not inherit a stale version.
stale=$(tpsql -d postgres -qAt -F ' ' -c "
  select datname, coalesce(datcollversion, '-'), coalesce(pg_database_collation_actual_version(oid), '-')
  from pg_database
  where datallowconn and datcollversion is distinct from pg_database_collation_actual_version(oid)
  order by datname")

while read -r db recorded actual; do
  [ -n "$db" ] || continue
  log "database $db: collation version $recorded, the server provides $actual; rebuilding its indexes"
  # Refreshing the version without the rebuild would only hide the warning.
  if ! tpsql -d "$db" -q -v db="$db" <<'SQL'
reindex database :"db";
SQL
  then
    log "REINDEX of database $db failed; its collation version stays at $recorded, so this runs again." \
      "A unique index that fails to build has rows that are duplicates under the new collation:" \
      "remove the duplicate rows named above, then run helm upgrade again."
    exit 1
  fi
  tpsql -d "$db" -q -v db="$db" <<'SQL'
alter database :"db" refresh collation version;
SQL
done <<<"$stale"
