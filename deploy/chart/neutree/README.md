# Neutree Helm chart

## CloudNativePG operator

PostgreSQL runs as a [CloudNativePG](https://cloudnative-pg.io) `Cluster`. The
chart always installs the operator itself, as the subchart `cnpg` (the official
`cloudnative-pg` chart 0.29.1, operator 1.30.1): one `helm install` sets up
everything, in both single and HA mode. The operator runs in the release
namespace and watches only that namespace (`cnpg.config.clusterWide=false`).
All `cnpg.*` values are passed to that chart.

- **Cluster-wide objects.** The operator chart also installs the CNPG CRDs,
  the admission webhook configurations `cnpg-mutating-webhook-configuration`
  and `cnpg-validating-webhook-configuration`, and the ClusterRoles
  `<release>-cloudnative-pg`, `<release>-cloudnative-pg-view`,
  `<release>-cloudnative-pg-edit` with a ClusterRoleBinding. Installing the
  chart therefore needs cluster-admin rights. The webhook names are fixed, so **a Kubernetes cluster can hold one
  Neutree release and no other CloudNativePG installation**. Installing into a
  cluster that already has CloudNativePG fails at render time.
- **Operator upgrades** come with chart upgrades. In-place updates of the
  instance manager are on (`ENABLE_INSTANCE_MANAGER_INPLACE_UPDATES`), so an
  operator upgrade neither restarts the PostgreSQL pods nor switches over.
- **First install.** The CRDs and the operator's webhook are not ready while
  helm creates the release, so helm does not create the `Cluster` on the
  first install. The migration Job `<release>-pg-migration-<revision>`
  creates it from the ConfigMap `<release>-pg-resources` as soon as the
  operator accepts it, and the next `helm upgrade` takes it over. (The Job
  also creates the Grafana database of HA mode, with SQL.) The same happens on the first upgrade from a
  release with the PostgreSQL Deployment. No manual step is needed; with
  `--wait`, allow a few minutes (`--timeout 15m`).
- **Uninstall deletes the database.** `helm uninstall` removes the `Cluster`
  and its volumes, and the operator. The CRDs stay (they are shared by every
  CNPG `Cluster` in the Kubernetes cluster). Delete them only when no other
  `Cluster` exists: deleting a CRD deletes every object of that kind. A new
  install under another release name or namespace must first take the CRDs
  over:

  ```bash
  kubectl annotate --overwrite $(kubectl get crd -o name | grep '\.postgresql\.cnpg\.io$') \
    meta.helm.sh/release-name=<release> meta.helm.sh/release-namespace=<namespace>
  ```
- **Rollback.** helm never deletes the `Cluster` itself
  (`helm.sh/resource-policy: keep`); it is owned by the ConfigMap
  `<release>-pg-resources`, so it goes with that ConfigMap: on uninstall, or
  on a rollback to a release with the PostgreSQL Deployment.
- **Offline installs.** `global.image.registry` does not apply to the operator
  image. Mirror `ghcr.io/cloudnative-pg/cloudnative-pg:1.30.1` and the
  PostgreSQL image in `db.image` (`ghcr.io/cloudnative-pg/postgresql:17.6`),
  then set `cnpg.image.repository=<registry>/cloudnative-pg/cloudnative-pg`
  (and `cnpg.imagePullSecrets` if the registry needs them). Rendering fails
  when `global.image.registry` is set and `cnpg.image.repository` is not.

## Database layout

PostgreSQL runs as one instance, or as three instances in HA mode (see
below). All components connect through the `<release>-pg-rw` Service, which
follows the primary. PostgREST and the seed Job add the TCP keepalive parameters in
`db.connectionParams`. GoTrue (pgx), golang-migrate (lib/pq) and Kong cannot
take them: the first two send unknown URL keys to the server, which rejects the
connection; Kong has no such option and relies on `pg_timeout` (5s per query).

## High availability

Install or upgrade in HA mode with the bundled overlay:

```bash
helm upgrade --install <release> <chart> -n <namespace> \
  -f <chart>/values-ha.yaml -f <your values>
```

See [High Availability](../../../docs/high-availability.md) for the
architecture, requirements, switching from single mode, and the behavior
under failures.

| Parameter | Default | HA mode (`values-ha.yaml`) |
|---|---|---|
| `ha.enabled` | `false` | `true`. Set it only through `values-ha.yaml`: rendering fails when `ha.enabled=true` is set without the overlay, and when the overlay is used with `ha.enabled=false` |
| `<component>.replicaCount` (`api`, `auth`, `postgrest`, `core`, `kong`, `vector`) | unset: 1 | unset: 3. An explicit value always wins |
| `<component>.affinity` | `{}` | When empty, a required one-pod-per-node anti-affinity. An explicit value is used as given |
| `db.instances` | unset: 1 | unset: 3, synchronous replication (`method: any, number: 1, dataDurability: required`), one instance per node |
| `vmagent.replicaCount` | unset: 1 | unset: 2 |
| `vector.persistence` | one PVC for the Vector Deployment (`Recreate`) | Vector runs as a StatefulSet; each pod gets its own PVC of the same size and storage class |
| `victoria-metrics-cluster.*` | 1 vminsert, vmselect, vmstorage | 3 each, `replicationFactor: 2`, `dedup.minScrapeInterval: 5s` |
| `grafana.replicas`, `grafanaDatabase.enabled` | 1, `false` (SQLite on a volume) | 2, `true` (the `grafana` database of the PostgreSQL Cluster) |

## Upgrading from a release with the PostgreSQL Deployment

Releases before 1.3 ran PostgreSQL 13 as the Deployment `<release>-postgresql`.
A plain `helm upgrade` moves the data into the new Cluster:

1. The chart keeps the old Deployment, Service and PVC exactly as they are (the
   PVC gets `helm.sh/resource-policy: keep`) and creates the Cluster (through
   the migration Job, see [CloudNativePG operator](#cloudnativepg-operator)).
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
