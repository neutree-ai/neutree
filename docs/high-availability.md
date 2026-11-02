# High Availability

This document describes the high availability (HA) mode of the Neutree control plane on Kubernetes. It covers what HA mode runs, what it needs, how to turn it on, and what to expect when something fails.

HA mode is available only for the Helm chart (`deploy/chart/neutree`). The docker compose deployment runs a single instance of every component.

## Scope

HA mode covers three layers:

| Layer | Components |
|---|---|
| Control plane | neutree-api, neutree-core, auth (GoTrue), PostgREST, PostgreSQL, Grafana |
| Inference gateway | Kong with the Neutree plugins; Vector, which writes usage records back through the API |
| Observability | vmagent, VictoriaMetrics cluster, VictoriaLogs |

HA mode does not cover the inference clusters themselves (Ray head, router, engine replicas). Their availability depends on the cluster and endpoint configuration.

## Architecture

| Component | Single mode | HA mode | Model |
|---|---|---|---|
| neutree-api | 1 | 3 | Active-active |
| neutree-core | 1 | 3 | Active-standby. One replica holds a Kubernetes Lease and runs the controllers; the others wait |
| auth (GoTrue) | 1 | 3 | Active-active |
| PostgREST | 1 | 3 | Active-active |
| PostgreSQL | 1 instance | 3 instances | One primary, two standbys (CloudNativePG). Synchronous replication: a commit returns after one standby has it |
| Kong | 1 | 3 | Active-active |
| Vector | 1 | 3 | Active-active |
| vmagent | 1 | 2 | Active-active. Each replica scrapes every target; storage keeps one copy |
| vminsert / vmselect | 1 | 3 | Active-active |
| vmstorage | 1 | 3 | Every sample is stored on 2 nodes, so one node can be lost without gaps |
| Grafana | 1, state in SQLite | 2, state in PostgreSQL | Active-active |
| VictoriaLogs | 1 | 1 | **Single point of failure.** See [Known limitations](#known-limitations) |

In HA mode, each multi-replica component:

- Places at most one pod per node (required pod anti-affinity).
- Rolls with `maxSurge: 0` and `maxUnavailable: 1`.
- Has a PodDisruptionBudget with `maxUnavailable: 1`.

All components reach PostgreSQL through the `<release>-pg-rw` Service. This Service always points to the current primary.

## Requirements

- **At least 3 schedulable nodes.** The required anti-affinity places one replica per node. With fewer nodes, some pods stay Pending.
- **Nodes on different physical hosts.** Put each of the 3 nodes on its own physical host (for example, with a VM anti-affinity placement group). If two nodes share a host, one host failure takes down two nodes. HA mode does not cover that case.
- **Low network latency between nodes.** Login runs many sequential database queries. When 200 ms was added between the applications and PostgreSQL, login p99 reached about 4.8 seconds. Do not spread the nodes across distant data centers.
- **CloudNativePG operator.** Install the operator before the chart. See [Prerequisites](../deploy/chart/neutree/README.md#prerequisites) in the chart README.
- **Storage.** A StorageClass that provisions ReadWriteOnce volumes. In HA mode the chart creates:
  - one PostgreSQL volume per instance (`db.persistence.size`, 40Gi by default), 3 in total;
  - one volume per vmstorage node, 3 in total;
  - one VictoriaLogs volume.

  Each Vector pod also uses up to 1 GiB of node disk (an `emptyDir`) as a trace buffer.

## Enabling HA mode

HA mode has one switch: the chart's `values-ha.yaml` file. Pass it on install or upgrade:

```bash
helm upgrade --install <release> <chart> -n <namespace> \
  -f <chart>/values-ha.yaml -f <your values>
```

`values-ha.yaml` sets `ha.enabled: true`. It also carries the settings of the VictoriaMetrics and Grafana subcharts, which `ha.enabled` cannot reach. Do not set `ha.enabled` by hand. Rendering fails when `ha.enabled=true` is used without the file, or when the file is used with `ha.enabled=false`.

Pass `values-ha.yaml` on every later `helm upgrade` too. Put your own values file after it to override any setting. The chart parameters are listed in the [chart README](../deploy/chart/neutree/README.md#high-availability).

### From single mode to HA mode

You can switch an existing single-mode installation to HA mode. Run the command above against the release.

Switching back from HA mode to single mode is not supported.

The switch is a planned operational change. Schedule a maintenance window for it. What to expect:

- **Inference is not interrupted.**
- **Management writes and logins pause once,** for about 9 seconds in our tests (target: 15 seconds or less). The pause comes from the final switchover of the PostgreSQL primary.
- **No data is lost.** The operator copies the database to the new standbys online. Until every standby is ready, commits do not wait for a standby. When all instances are ready, the chart turns on synchronous replication.
- **The upgrade takes longer for larger databases.** The new standbys copy the whole database first. Writes are not blocked during the copy.
- **Grafana starts on an empty database.** Provisioned dashboards and data sources come back. Users and dashboards saved in the Grafana UI stay in the old SQLite volume, which is deleted.
- **Existing metrics stay on the first vmstorage node only.** Replication applies to samples written after the switch.

### From a release with PostgreSQL 13

Releases before 1.3 ran PostgreSQL 13 as a Deployment. You can upgrade such a release directly to HA mode: run the command above with the new chart.

The upgrade moves the data into a new PostgreSQL 17 cluster with 3 instances. The chart README describes the [migration, rollback and cleanup steps](../deploy/chart/neutree/README.md#upgrading-from-a-release-with-the-postgresql-deployment).

This is a large operational change. Schedule a maintenance window for it. What to expect:

- **Management writes, logins and reads** are unavailable for up to 5 minutes. The window grows with the database size.
- **Inference** is interrupted for at most 15 seconds. The Kong pods of the earlier release check the database in their health probes. While writes to the old database are frozen (about 20 seconds), Kubernetes may restart them.
- **No data is lost.** The migration compares the copy with the source before it switches over.
- **The upgrade command takes several minutes.** It waits until all 3 PostgreSQL instances are ready before it freezes writes.
- Grafana and metrics behave as described in [From single mode to HA mode](#from-single-mode-to-ha-mode).

## What to expect during failures

The tables below give the target values for HA mode with 3 nodes. "0" means no failed requests.

### Recovery time (RTO)

| Failure | Inference | Management API and login | Control loop (for example, a new API key takes effect) |
|---|---|---|---|
| A stateless pod crashes | Only the requests in flight on that replica fail | 0 | ≤ 30s |
| The neutree-core leader crashes | 0 | 0 | ≤ 30s |
| The PostgreSQL primary crashes | 0 | ≤ 40s | ≤ 60s |
| Planned primary switchover (node drain, rolling update that restarts PostgreSQL) | 0 | ≤ 15s | ≤ 40s |
| Node drain (maintenance) | 0 | 0; ≤ 15s on the node of the PostgreSQL primary | ≤ 30s |
| Rolling update that does not restart PostgreSQL | 0 | 0 | ≤ 30s |
| A node fails or becomes unreachable | ≤ 60s | ≤ 60s; ≤ 90s on the node of the PostgreSQL primary | ≤ 60s; ≤ 120s on the node of the PostgreSQL primary |
| Network partition between the applications and PostgreSQL | 0 | ≤ partition length + 10s. Everything recovers within 30s after the partition ends, without pod restarts | — |
| Network partition between the PostgreSQL primary and its standbys | 0 | Reads: 0. Writes and logins: blocked for the partition length, ≤ partition length + 15s | — |
| All 3 nodes lose power at the same time | Full recovery within 10 minutes after the nodes are back, without manual action | Same | Same |

Kubernetes needs about 40 seconds to mark an unreachable node as not ready. Until then, Services still send requests to pods on that node. This sets the lower limit for node failures.

### Data loss (RPO)

| Data | Planned changes (drain, rolling update) | Crashes (pod crash, node failure) |
|---|---|---|
| Configuration and business data (PostgreSQL) | 0 | 0 |
| Usage records | 0 | At most the requests that the failed node accepted in the last 10 seconds |
| Traces | 0 | Same as usage records |
| Metrics | Gap of at most 1 scrape interval | Gap of at most 2 scrape intervals |

PostgreSQL loses no committed data in any case in the tables above, including when 3 nodes lose power at the same time.

## What HA mode does not guarantee

### Two of three nodes fail at the same time

HA mode tolerates the loss of one node. When two nodes fail at the same time:

- The management API and login are unavailable.
- Inference continues on the remaining node, after the node failure is detected (about 40 seconds).
- No committed data is lost (RPO stays 0).
- The system recovers on its own after the nodes come back.

### Writes block without a standby

PostgreSQL uses synchronous replication. A commit waits until one standby has it. This gives RPO 0 on failover. The cost: when no standby is reachable, commits wait instead of going on without a copy.

While no standby is reachable:

- Reads continue.
- Writes are blocked.
- Logins are blocked, because login writes a session.
- Creating API keys is blocked.

This happens when two of three nodes fail, or when the primary is cut off from both standbys. Writes resume as soon as one standby is back.

## Known limitations

- **VictoriaLogs is a single point of failure.** It stores inference traces. While it is down, trace queries are unavailable. Vector buffers traces on disk and sends them when it is back. After a pod crash, VictoriaLogs returns within seconds. After a node failure, Kubernetes evicts the pod after about 5 minutes and must then detach its volume from the failed node. Trace queries can be unavailable for about 11 minutes.
- **Trace delivery can be delayed by up to about 10 seconds.** A trace can reach VictoriaLogs up to about 10 seconds after the request ends.
- **The quota check fails open during a primary switchover.** While the PostgreSQL primary is switching, Kong cannot read the quota balance. Requests are let through without quota enforcement during that window.
- **Usage loss can exceed 10 seconds in one case.** While the API or PostgreSQL is unavailable, Vector holds usage records in memory. If a Vector pod crashes during that time, it loses its whole backlog.
