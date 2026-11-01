#!/usr/bin/env bash
# Copies the Neutree database of an earlier release (PostgreSQL 13) into a new
# PostgreSQL 17 server and proves that the copy matches the source. Shared by
# the Helm chart (migrate-k8s.sh sources it) and neutree-cli for docker compose
# (runs it as `pg-copy.sh check|copy`). Callers stop the writers first.
#
#   check  exit 0: SOURCE_ID was already copied and, when SOURCE_HOST is set,
#          the source took no writes since; 3: not copied yet; 4: the source
#          took writes after the copy (the release was rolled back)
#   copy   copy, verify, then record SOURCE_ID as done in the target
#
# Environment: SOURCE_ID, SOURCE_HOST, TARGET_HOST, DB_NAME, PGUSER, PGPASSWORD
# (the same superuser on both servers), AUTH_ADMIN_PASSWORD, KONG_ADMIN_PASSWORD,
# FROZEN_AT (unix time writes stopped, for the record), WORK (scratch dir).

PGM_SCRIPTS=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export PGCONNECT_TIMEOUT=10

log() { echo "$(date -u +%FT%TZ) $*"; }
tpsql() { psql -X -v ON_ERROR_STOP=1 -h "$TARGET_HOST" "$@"; }
spsql() { psql -X -v ON_ERROR_STOP=1 -h "$SOURCE_HOST" "$@"; }

pgm_wait() {
  local i
  for i in $(seq 1 150); do
    psql -X -h "$1" -d postgres -qAtc 'select 1' >/dev/null 2>&1 && return 0
    log "waiting for $1"; sleep 2
  done
  psql -X -h "$1" -d postgres -qAtc 'select 1' >/dev/null
}

pgm_wait_target() {
  pgm_wait "$TARGET_HOST"
  tpsql -d postgres -q <<'SQL'
set client_min_messages = warning;
create table if not exists neutree_pg_migration (
  source text primary key,
  completed_at timestamptz not null default now(),
  -- next transaction id of the source after the copy; a larger one later
  -- means the source took writes again (a rollback)
  source_xmax bigint,
  details text
);
SQL
}

pgm_source_xmax() {
  spsql -d "$DB_NAME" -qAtc 'select txid_snapshot_xmax(txid_current_snapshot())'
}

pgm_check() {
  local done_at now
  done_at=$(tpsql -d postgres -qAt -v src="$SOURCE_ID" <<'SQL'
select completed_at, source_xmax from neutree_pg_migration where source = :'src';
SQL
)
  [ -n "$done_at" ] || return 3
  log "migration from $SOURCE_ID already completed: $done_at"
  if [ -n "${SOURCE_HOST:-}" ]; then
    now=$(pgm_source_xmax)
    if [ "$now" != "${done_at#*|}" ]; then
      log "the old database took writes after the migration (was the release rolled back?)"
      return 4
    fi
  fi
  return 0
}

pgm_copy() {
  local version
  version=$(spsql -d "$DB_NAME" -qAtc 'select version()')
  log "source $SOURCE_HOST: $version"
  mkdir -p "$WORK"

  log "copying roles"
  pg_dumpall -h "$SOURCE_HOST" --roles-only --no-role-passwords >"$WORK/roles.raw.sql"
  # The target owns the postgres role; its init script already created some roles.
  grep -v -E '^(CREATE|ALTER) ROLE postgres( |;)' "$WORK/roles.raw.sql" \
    | sed -E 's/^CREATE ROLE ([^;]+);$/DO $$BEGIN CREATE ROLE \1; EXCEPTION WHEN duplicate_object THEN NULL; END$$;/' \
    >"$WORK/roles.sql"
  tpsql -d postgres -q -f "$WORK/roles.sql"

  log "dumping $DB_NAME"
  pg_dump -h "$SOURCE_HOST" -d "$DB_NAME" -Fc -f "$WORK/db.dump"
  ls -l "$WORK/db.dump"

  log "restoring $DB_NAME"
  tpsql -d postgres -qc "drop database if exists \"$DB_NAME\" with (force)"
  pg_restore -h "$TARGET_HOST" -d postgres --create --exit-on-error -j 4 "$WORK/db.dump"
  # Passwords were left out of the dump: PostgreSQL 17 only accepts scram hashes.
  tpsql -d postgres -q <<SQL
grant create on database postgres to auth_admin;
alter role auth_admin password '$AUTH_ADMIN_PASSWORD';
alter role kong_admin password '$KONG_ADMIN_PASSWORD';
SQL
  tpsql -d "$DB_NAME" -qc 'analyze'

  log "verifying"
  spsql -d "$DB_NAME" -qAt -f "$PGM_SCRIPTS/inventory.sql" >"$WORK/source.inv"
  tpsql -d "$DB_NAME" -qAt -f "$PGM_SCRIPTS/inventory.sql" >"$WORK/target.inv"
  local f
  for f in source target; do
    tail -1 "$WORK/$f.inv" | grep -qx END || { log "$f inventory is incomplete"; return 1; }
  done
  if ! diff "$WORK/source.inv" "$WORK/target.inv" >"$WORK/inventory.diff"; then
    log "the copy differs from the source; keeping the source in service"
    head -100 "$WORK/inventory.diff"
    return 1
  fi
  local summary source_xmax t_done
  summary=$(cut -d'|' -f1 "$WORK/source.inv" | sort | uniq -c | awk '{printf "%s=%s ", $2, $1}')
  log "verified: $summary"

  source_xmax=$(pgm_source_xmax)
  t_done=$(date +%s)
  tpsql -d postgres -q -v src="$SOURCE_ID" -v xmax="$source_xmax" \
    -v details="frozen ${t_done}s-${FROZEN_AT}s=$((t_done - FROZEN_AT))s; $summary" <<'SQL'
insert into neutree_pg_migration (source, source_xmax, details) values (:'src', :'xmax', :'details');
SQL
  log "migration complete; writes were frozen for $((t_done - FROZEN_AT))s"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  set -euo pipefail
  WORK=${WORK:-$(mktemp -d)}
  FROZEN_AT=${FROZEN_AT:-$(date +%s)}
  pgm_wait "$SOURCE_HOST"
  pgm_wait_target
  case "${1:-}" in
    check) pgm_check ;;
    copy) pgm_copy ;;
    *) echo "usage: $0 check|copy" >&2; exit 2 ;;
  esac
fi
