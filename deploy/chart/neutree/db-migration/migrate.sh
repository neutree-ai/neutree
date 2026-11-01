#!/usr/bin/env bash
# Copies the database of the PostgreSQL Deployment used by earlier releases
# into the CloudNativePG Cluster. Runs on every helm upgrade and is safe to
# re-run: it exits early once the copy is complete, and a failed attempt is
# redone from scratch.
#
# Environment: NAMESPACE, CLUSTER, TARGET_HOST, DB_NAME, PGUSER, PGPASSWORD,
# LEGACY_DEPLOYMENT and LEGACY_SERVICE (empty when there is nothing to move),
# AUTH_ADMIN_PASSWORD, KONG_ADMIN_PASSWORD.
set -euo pipefail

export PATH=/tools:$PATH HOME=${WORK:-/work}
WORK=${WORK:-/work}
SCRIPTS=$(dirname "$0")
FROZEN_COMPONENT=neutree-postgresql-migrating
export PGCONNECT_TIMEOUT=10

log() { echo "$(date -u +%FT%TZ) $*"; }
tpsql() { psql -X -v ON_ERROR_STOP=1 -h "$TARGET_HOST" "$@"; }
spsql() { psql -X -v ON_ERROR_STOP=1 -h "$SOURCE_HOST" "$@"; }

# A Job from an earlier revision may still be finishing after helm deleted it.
while kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/component=neutree-pg-migration \
    --field-selector=status.phase=Running -o name | grep -v -x "pod/$HOSTNAME" | grep -q .; do
  log "waiting for another migration pod to finish"; sleep 5
done

log "waiting for Cluster $CLUSTER to be ready"
until kubectl -n "$NAMESPACE" wait "clusters.postgresql.cnpg.io/$CLUSTER" --for=condition=Ready --timeout=60s; do
  log "Cluster $CLUSTER not ready yet"
done
until tpsql -d postgres -qAtc 'select 1' >/dev/null; do
  log "waiting for $TARGET_HOST"; sleep 2
done

tpsql -d postgres -q <<'SQL'
create table if not exists neutree_pg_migration (
  source text primary key,
  completed_at timestamptz not null default now(),
  -- next transaction id of the source after the copy; a larger one later
  -- means the source took writes again (a rollback)
  source_xmax bigint,
  details text
);
SQL

if [ -z "${LEGACY_DEPLOYMENT:-}" ]; then
  log "no PostgreSQL Deployment from an earlier release; nothing to migrate"
  exit 0
fi

done_at=$(tpsql -d postgres -qAtc "select completed_at, source_xmax from neutree_pg_migration where source = '$LEGACY_DEPLOYMENT'")
if [ -n "$done_at" ]; then
  log "migration from $LEGACY_DEPLOYMENT already completed: $done_at"
  if [ "$(kubectl -n "$NAMESPACE" get "deployment/$LEGACY_DEPLOYMENT" -o jsonpath='{.status.readyReplicas}')" -ge 1 ] 2>/dev/null; then
    host=$(kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/component=neutree-postgresql \
      --field-selector=status.phase=Running -o jsonpath='{.items[0].status.podIP}')
    now=$(psql -X -h "$host" -d "$DB_NAME" -qAtc 'select txid_snapshot_xmax(txid_current_snapshot())')
    if [ "$now" != "${done_at#*|}" ]; then
      log "the old database took writes after the migration (was the release rolled back?)."
      log "To migrate again from it, delete the Cluster $CLUSTER (its data is discarded) and run helm upgrade again."
      exit 1
    fi
  fi
  kubectl -n "$NAMESPACE" scale "deployment/$LEGACY_DEPLOYMENT" --replicas=0 || true
  exit 0
fi

original_selector=$(kubectl -n "$NAMESPACE" get service "$LEGACY_SERVICE" -o jsonpath='{.spec.selector.app\.kubernetes\.io/component}')
if [ "$original_selector" = "$FROZEN_COMPONENT" ]; then
  # A previous attempt died before restoring it.
  original_selector=neutree-postgresql
fi

unfreeze() {
  log "restoring Service $LEGACY_SERVICE selector"
  kubectl -n "$NAMESPACE" patch service "$LEGACY_SERVICE" --type merge \
    -p "{\"spec\":{\"selector\":{\"app.kubernetes.io/component\":\"$original_selector\"}}}" || true
}

log "making sure the source is running"
kubectl -n "$NAMESPACE" scale "deployment/$LEGACY_DEPLOYMENT" --replicas=1
kubectl -n "$NAMESPACE" rollout status "deployment/$LEGACY_DEPLOYMENT" --timeout=10m
SOURCE_HOST=$(kubectl -n "$NAMESPACE" get pods -l "app.kubernetes.io/component=$original_selector" \
  --field-selector=status.phase=Running -o jsonpath='{.items[0].status.podIP}')
[ -n "$SOURCE_HOST" ] || { log "no running source pod"; exit 1; }
log "source pod $SOURCE_HOST"
spsql -d "$DB_NAME" -qAtc 'select version()'

# Freeze: no new connections through the Service, then end the open ones.
# Writers see a connection error (5xx) and retry; nothing is half-written.
trap 'rc=$?; [ $rc -ne 0 ] && unfreeze; exit $rc' EXIT
trap 'exit 143' TERM INT
t_freeze=$(date +%s)
log "freezing writes"
kubectl -n "$NAMESPACE" patch service "$LEGACY_SERVICE" --type merge \
  -p "{\"spec\":{\"selector\":{\"app.kubernetes.io/component\":\"$FROZEN_COMPONENT\"}}}"
quiet=0
for _ in $(seq 1 60); do
  n=$(spsql -d postgres -qAtc "select count(pg_terminate_backend(pid)) from pg_stat_activity
        where pid <> pg_backend_pid() and backend_type = 'client backend'")
  if [ "$n" = 0 ]; then quiet=$((quiet + 1)); else quiet=0; log "terminated $n connections"; fi
  [ $quiet -ge 3 ] && break
  sleep 2
done
[ $quiet -ge 3 ] || { log "clients keep reconnecting to the source"; exit 1; }

log "copying roles"
pg_dumpall -h "$SOURCE_HOST" --roles-only --no-role-passwords >"$WORK/roles.raw.sql"
# CNPG owns the postgres role; the init script already created some roles.
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
tpsql -d postgres -q <<SQL
grant create on database postgres to auth_admin;
alter role auth_admin password '$AUTH_ADMIN_PASSWORD';
alter role kong_admin password '$KONG_ADMIN_PASSWORD';
SQL
tpsql -d "$DB_NAME" -qc 'analyze'

log "verifying"
spsql -d "$DB_NAME" -qAt -f "$SCRIPTS/inventory.sql" >"$WORK/source.inv"
tpsql -d "$DB_NAME" -qAt -f "$SCRIPTS/inventory.sql" >"$WORK/target.inv"
for f in source target; do
  tail -1 "$WORK/$f.inv" | grep -qx END || { log "$f inventory is incomplete"; exit 1; }
done
if ! diff "$WORK/source.inv" "$WORK/target.inv" >"$WORK/inventory.diff"; then
  log "the copy differs from the source; keeping the source in service"
  head -100 "$WORK/inventory.diff"
  exit 1
fi
summary=$(cut -d'|' -f1 "$WORK/source.inv" | sort | uniq -c | awk '{printf "%s=%s ", $2, $1}')
log "verified: $summary"

source_xmax=$(spsql -d "$DB_NAME" -qAtc 'select txid_snapshot_xmax(txid_current_snapshot())')
t_done=$(date +%s)
tpsql -d postgres -q -v src="$LEGACY_DEPLOYMENT" -v xmax="$source_xmax" -v details="frozen ${t_done}s-${t_freeze}s=$((t_done - t_freeze))s; $summary" <<'SQL'
insert into neutree_pg_migration (source, source_xmax, details) values (:'src', :'xmax', :'details');
SQL
trap - EXIT
log "migration complete; writes were frozen for $((t_done - t_freeze))s"
kubectl -n "$NAMESPACE" scale "deployment/$LEGACY_DEPLOYMENT" --replicas=0
