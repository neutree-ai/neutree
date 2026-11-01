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
FROZEN_COMPONENT=neutree-postgresql-migrating
SOURCE_ID=${LEGACY_DEPLOYMENT:-}
SOURCE_HOST=
# shellcheck source=pg-copy.sh
. "$(dirname "$0")/pg-copy.sh"

# A Job from an earlier revision may still be finishing after helm deleted it.
while kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/component=neutree-pg-migration \
    --field-selector=status.phase=Running -o name | grep -v -x "pod/$HOSTNAME" | grep -q .; do
  log "waiting for another migration pod to finish"; sleep 5
done

log "waiting for Cluster $CLUSTER to be ready"
until kubectl -n "$NAMESPACE" wait "clusters.postgresql.cnpg.io/$CLUSTER" --for=condition=Ready --timeout=60s; do
  log "Cluster $CLUSTER not ready yet"
done
pgm_wait_target

if [ -z "$SOURCE_ID" ]; then
  log "no PostgreSQL Deployment from an earlier release; nothing to migrate"
  # Deployments wait for any row here before they connect.
  tpsql -d postgres -qc "insert into neutree_pg_migration (source, details) values ('', 'nothing to migrate') on conflict do nothing"
  exit 0
fi

if [ "$(kubectl -n "$NAMESPACE" get "deployment/$LEGACY_DEPLOYMENT" -o jsonpath='{.status.readyReplicas}')" -ge 1 ] 2>/dev/null; then
  SOURCE_HOST=$(kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/component=neutree-postgresql \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].status.podIP}')
fi
rc=0; pgm_check || rc=$?
case $rc in
  0) kubectl -n "$NAMESPACE" scale "deployment/$LEGACY_DEPLOYMENT" --replicas=0 || true; exit 0 ;;
  3) ;;
  *) log "To migrate again from it, delete the Cluster $CLUSTER (its data is discarded) and run helm upgrade again."; exit 1 ;;
esac

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

# Freeze: no new connections through the Service, then end the open ones.
# Writers see a connection error (5xx) and retry; nothing is half-written.
trap 'rc=$?; [ $rc -ne 0 ] && unfreeze; exit $rc' EXIT
trap 'exit 143' TERM INT
FROZEN_AT=$(date +%s)
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

pgm_copy
trap - EXIT
kubectl -n "$NAMESPACE" scale "deployment/$LEGACY_DEPLOYMENT" --replicas=0
