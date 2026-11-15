#!/usr/bin/env bash
# Copies the database of the PostgreSQL Deployment used by earlier releases
# into the CloudNativePG Cluster. Runs on every helm upgrade and is safe to
# re-run: it exits early once the copy is complete, and a failed attempt is
# redone from scratch.
#
# Environment: NAMESPACE, CLUSTER, RESOURCES_CONFIGMAP and CREATE_RESOURCES
# (the manifest of the Cluster is in /resources; "true" when helm does not
# create the Cluster), GRAFANA_DATABASE and
# GRAFANA_USER (empty without the Grafana database), TARGET_HOST, DB_NAME, PGUSER, PGPASSWORD,
# LEGACY_DEPLOYMENT and LEGACY_SERVICE (empty when there is nothing to move),
# AUTH_ADMIN_PASSWORD, KONG_ADMIN_PASSWORD; DATA_DURABILITY (the synchronous
# replication durability the Cluster must end with, empty for one instance).
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

# Applies manifest $1 in full. helm patches custom resources with the
# difference between its previous and new manifests, so a Cluster it adopts
# (created by this Job, or kept by a rollback to a revision without it) would
# keep stale values. The operator's webhook rejects every change until the
# operator is ready, so retry.
apply_resource() {
  until kubectl -n "$NAMESPACE" apply --server-side --force-conflicts \
      --field-manager=neutree-pg-migration -f "$1"; do
    log "cannot apply $(basename "$1") yet"; sleep 5
  done
}

# Replaces object $1 with manifest $2 in full. On an install the Cluster can be
# one that an earlier helm uninstall kept, and server-side apply would leave the
# fields that the old release's helm set (an HA Cluster reinstalled as single
# would keep its synchronous replication).
replace_resource() {
  until kubectl -n "$NAMESPACE" replace -f "$2"; do
    log "cannot replace $1 yet"; sleep 5
  done
}

# Makes the resources ConfigMap an owner of object $1 (see the ConfigMap in
# postgresql-cluster.yaml).
own_resource() {
  local uid refs ref patch
  uid=$(kubectl -n "$NAMESPACE" get "configmap/$RESOURCES_CONFIGMAP" -o jsonpath='{.metadata.uid}')
  refs=$(kubectl -n "$NAMESPACE" get "$1" -o jsonpath='{.metadata.ownerReferences[*].uid}')
  case " $refs " in *" $uid "*) return ;; esac
  ref="{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"name\":\"$RESOURCES_CONFIGMAP\",\"uid\":\"$uid\"}"
  if [ -z "$refs" ]; then
    patch="[{\"op\":\"add\",\"path\":\"/metadata/ownerReferences\",\"value\":[$ref]}]"
  else
    patch="[{\"op\":\"add\",\"path\":\"/metadata/ownerReferences/-\",\"value\":$ref}]"
  fi
  # Goes through the operator's webhook too.
  until kubectl -n "$NAMESPACE" patch "$1" --type json -p "$patch"; do
    log "cannot set the owner of $1 yet"; sleep 5
  done
}

# helm uninstall hibernates the Cluster it keeps (postgresql-uninstall-hook.yaml).
hibernated=$(kubectl -n "$NAMESPACE" get "clusters.postgresql.cnpg.io/$CLUSTER" --ignore-not-found \
  -o jsonpath='{.metadata.annotations.cnpg\.io/hibernation}')

for manifest in /resources/*.yaml; do
  [ -e "$manifest" ] || continue
  # Otherwise helm creates it; do not race it.
  if [ "${CREATE_RESOURCES:-}" != true ]; then
    until kubectl -n "$NAMESPACE" get -f "$manifest" -o name >/dev/null 2>&1; do
      log "waiting for $(basename "$manifest" .yaml) to exist"; sleep 5
    done
  fi
  object=$(kubectl -n "$NAMESPACE" get -f "$manifest" -o name --ignore-not-found)
  if [ "${CREATE_RESOURCES:-}" = true ] && [ -n "$object" ]; then
    replace_resource "$object" "$manifest"
  else
    apply_resource "$manifest"
    object=$(kubectl -n "$NAMESPACE" get -f "$manifest" -o name)
  fi
  own_resource "$object"
done

if [ -n "$hibernated" ]; then
  log "waking Cluster $CLUSTER from hibernation"
  # Replacing it has usually dropped the annotation already.
  until kubectl -n "$NAMESPACE" annotate "clusters.postgresql.cnpg.io/$CLUSTER" cnpg.io/hibernation-; do
    log "cannot wake Cluster $CLUSTER yet"; sleep 5
  done
  # Its Ready condition is still the one from before the hibernation.
  until psql -X -h "$TARGET_HOST" -d postgres -qAtc 'select 1' >/dev/null 2>&1; do
    log "waiting for $TARGET_HOST to start"; sleep 5
  done
fi

log "waiting for Cluster $CLUSTER to be ready"
until kubectl -n "$NAMESPACE" wait "clusters.postgresql.cnpg.io/$CLUSTER" --for=condition=Ready --timeout=60s; do
  log "Cluster $CLUSTER not ready yet"
done
pgm_wait_target

# Created here rather than with a CloudNativePG Database: its finalizer needs
# the operator, which helm uninstall removes at the same time.
if [ -n "${GRAFANA_DATABASE:-}" ]; then
  until [ "$(tpsql -d postgres -qAtc "select 1 from pg_roles where rolname = '$GRAFANA_USER'")" = 1 ]; do
    log "waiting for the role $GRAFANA_USER"; sleep 5
  done
  if [ "$(tpsql -d postgres -qAtc "select 1 from pg_database where datname = '$GRAFANA_DATABASE'")" != 1 ]; then
    tpsql -d postgres -qc "create database \"$GRAFANA_DATABASE\" owner \"$GRAFANA_USER\""
    log "created database $GRAFANA_DATABASE"
  fi
fi

# The chart renders "preferred" while a single instance grows to HA.
if [ -n "${DATA_DURABILITY:-}" ]; then
  current=$(kubectl -n "$NAMESPACE" get "clusters.postgresql.cnpg.io/$CLUSTER" -o jsonpath='{.spec.postgresql.synchronous.dataDurability}')
  if [ "$current" != "$DATA_DURABILITY" ]; then
    log "every instance is ready; synchronous replication dataDurability $current -> $DATA_DURABILITY"
    kubectl -n "$NAMESPACE" patch "clusters.postgresql.cnpg.io/$CLUSTER" --type merge \
      -p "{\"spec\":{\"postgresql\":{\"synchronous\":{\"dataDurability\":\"$DATA_DURABILITY\"}}}}"
  fi
fi

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
