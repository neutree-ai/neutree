# Neutree Helm chart

## Prerequisites

PostgreSQL runs as a [CloudNativePG](https://cloudnative-pg.io) `Cluster`. The
chart renders only the `Cluster` resource; the operator must already be
installed and must watch the release namespace. Install it once per Kubernetes
cluster, for example:

```bash
helm repo add cnpg https://cloudnative-pg.github.io/charts
helm upgrade --install cnpg cnpg/cloudnative-pg --version 0.29.1 \
  -n cnpg-system --create-namespace
```

The operator chart installs cluster-wide objects (CRDs, admission webhooks,
ClusterRoles), so only one copy can exist per Kubernetes cluster; every Neutree
release shares it. To restrict it to some namespaces instead, set
`config.clusterWide=false`, list the namespaces in the operator's
`WATCH_NAMESPACE` environment variable (comma separated) and give its service
account the operator Role in each of them.

`helm install` / `helm upgrade` fail with a message pointing here when the
`postgresql.cnpg.io/v1` API is missing. `helm template` cannot see the cluster's
APIs; render with `--set db.operatorCheck=false` (or `--api-versions
postgresql.cnpg.io/v1`).

Images to mirror for offline installs: the operator image
(`ghcr.io/cloudnative-pg/cloudnative-pg`) and the PostgreSQL image in
`db.image` (`ghcr.io/cloudnative-pg/postgresql:17.6`).

## Database layout

| | `ha.enabled=false` | `ha.enabled=true` |
|---|---|---|
| Instances | 1 | 3, one per node (required anti-affinity) |
| Replication | none | synchronous, `method: any, number: 1, dataDurability: required` |
| Writes without a standby | n/a | blocked until a standby is back |
| Node drain | evicts the instance; the database is down until it restarts elsewhere | the operator switches over first (PDB) |

`db.instances` overrides the count. Moving from single to HA needs only
`ha.enabled` (through `values-ha.yaml`); the operator clones the new standbys
online. Until every standby is ready the Cluster runs with
`dataDurability: preferred`, so writes do not wait for the copy; the migration
Job then sets `required`. Writes pause once, for the switchover that applies
the HA pod settings to the primary (about 10 seconds).

All components connect through the `<release>-pg-rw` Service, which follows the
primary. PostgREST and the seed Job add the TCP keepalive parameters in
`db.connectionParams`. GoTrue (pgx), golang-migrate (lib/pq) and Kong cannot
take them: the first two send unknown URL keys to the server, which rejects the
connection; Kong has no such option and relies on `pg_timeout` (5s per query).

## Observability in HA mode

Install or upgrade in HA mode with the bundled overlay, which sets
`ha.enabled` together with the VictoriaMetrics and Grafana subchart settings
that `ha.enabled` cannot reach:

```bash
helm upgrade --install <release> <chart> -n <namespace> \
  -f <chart>/values-ha.yaml -f <your values>
```

Rendering fails when `ha.enabled=true` is set without the overlay (the
message lists what is missing), and when the overlay is used with
`ha.enabled=false`.

| Component | Single | HA (`values-ha.yaml`) |
|---|---|---|
| vmagent | 1 replica | 2 replicas, each scrapes every target; storage keeps one copy (`dedup.minScrapeInterval`) |
| vminsert / vmselect | 1 replica | 3 replicas |
| vmstorage | 1 node | 3 nodes, every sample on 2 (`replicationFactor: 2`); one node can be lost without gaps |
| Grafana | 1 replica, SQLite on a volume | 2 replicas, state in the `grafana` database of the PostgreSQL Cluster |
| VictoriaLogs | 1 replica | 1 replica |

Switching to HA starts Grafana on an empty database: provisioned dashboards
and data sources come back, while users and dashboards saved in its UI stay in
the old SQLite volume, which is deleted. Existing metrics stay on the first
vmstorage node only; replication applies to samples written afterwards.

## Upgrading from a release with the PostgreSQL Deployment

Releases before 1.3 ran PostgreSQL 13 as the Deployment `<release>-postgresql`.
A plain `helm upgrade` moves the data into the new Cluster:

1. The chart keeps the old Deployment, Service and PVC exactly as they are (the
   PVC gets `helm.sh/resource-policy: keep`) and creates the Cluster.
2. The Job `<release>-pg-migration-<revision>` waits until the Cluster is
   ready (in HA mode the Cluster is created with `dataDurability: preferred`
   and the Job sets `required` at this point), then stops writes to the old database: it points the old Service at
   no pod and closes the open connections. Writers get connection errors and
   retry; the running Kong pods keep routing from their cache.
3. It copies roles (`pg_dumpall --roles-only`) and the database
   (`pg_dump` / `pg_restore`), resets the `auth_admin` and `kong_admin`
   passwords (PostgreSQL 17 uses scram-sha-256), and compares the copy with the
   source: roles, schemas, tables, columns, constraints, functions, triggers,
   policies, sequences, and the row count and content checksum of every table.
4. When they match, it records the migration in the `neutree_pg_migration` table
   of the Cluster's `postgres` database and scales the old Deployment to 0.
   Pods that use the database wait for this Job before they start.

If any step fails, the Job puts the old Service back, so the pods that have not
been replaced yet keep working on the old database, and the Job retries
(`db.migration.backoffLimit`). Re-running is safe: each attempt restores into a
freshly created database. Inspect a failure with
`kubectl logs job/<release>-pg-migration-<revision>`.

Writes are unavailable from step 2 until the new PostgREST and auth pods are
ready, typically under a minute plus the copy time (about 10 s for 25 MB).

### Rolling back

Before the cleanup below, the old database is untouched:

```bash
helm rollback <release> <previous-revision>
```

This restores the old Deployment (1 replica), its Service selector and the old
components, and deletes the Cluster. Data written after the upgrade is not
carried back. Upgrading again repeats the migration from the old database.

### Cleaning up

Once the upgrade is confirmed, delete the old objects; the chart never deletes
them on its own:

```bash
kubectl -n <namespace> delete deployment <release>-postgresql
kubectl -n <namespace> delete service <release>-postgresql-service
kubectl -n <namespace> delete pvc <release>-postgresql-pvc
```

After this, rolling back to a release before 1.3 is no longer possible.
