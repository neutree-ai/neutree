# Cluster Version Upgrade

## 1. Background

Neutree v1.0.1 introduces two key improvements to cluster base images:

1. **Ray upgrade**: Ray runtime upgraded to 2.53.0 to support newer vLLM inference engine versions.
2. **Lighter cluster images**: The `neutree-serve` image has been slimmed down by removing unnecessary components.

Currently, Neutree only supports cluster creation and configuration updates (node scaling, parameter changes) but lacks the ability to upgrade the cluster serving version. Existing v1.0.0 clusters cannot be upgraded to v1.0.1+; users must delete and recreate, causing service disruption and endpoint loss.

This feature adds in-place cluster version upgrade for both SSH and Kubernetes cluster types.

## 2. Goals

1. **In-place version upgrade**: Users change `spec.version` to trigger an upgrade without recreating the cluster. Existing endpoints are preserved.
2. **Minimize downtime**: SSH clusters pre-pull images before shutdown. Kubernetes clusters use rolling updates for zero or near-zero downtime.
3. **Observable upgrade status**: A new `Upgrading` phase is distinct from configuration `Updating`, giving users real-time upgrade visibility.
4. **Manual rollback**: Users revert `spec.version` to the previous version to trigger automatic rollback from any intermediate state.
5. **Version discovery**: An API queries the image registry for available cluster versions, usable for both cluster creation and upgrade.

## 3. User Story

**As a** Neutree platform user,

**I want to** upgrade an existing cluster to a newer version,

**So that** I can get new features and performance improvements without destroying the cluster or losing endpoint configurations.

### Acceptance Criteria

1. Users can query available cluster versions (filtered by image registry, cluster type, and accelerator type)
2. Changing `spec.version` triggers a version upgrade
3. Cluster phase shows `Upgrading` during the upgrade process
4. SSH clusters automatically pre-pull images (cluster + engine) to minimize downtime
5. Kubernetes clusters use Deployment rolling updates; zero downtime when replicas > 1
6. Rollback is triggered by reverting `spec.version` to the previous value
7. After upgrade completes, cluster returns to `Running` with `status.version` updated

## 4. Design Overview

### 4.1 Version Fields

| Field | Location | Description |
|-------|----------|-------------|
| `spec.version` | `ClusterSpec.Version` | Desired version (user-set) |
| `status.version` | `ClusterStatus.Version` | Actual running version (system-detected); defaults to `spec.version` on first reconcile |

### 4.2 Version Label System

All version tracking uses the label key `neutree.ai/cluster-version`, applied to:

- **Docker image labels** (set at build time via `--label`)
- **K8s Deployment / Pod labels**
- **Ray node labels**

For backward compatibility, `GetVersionFromLabels()` reads the new key first, falling back to the legacy key `neutree.ai/neutree-serving-version`.

#### Image Labels

Set at build time via `docker build --label`:

| Label Key | Description | Example |
|-----------|-------------|---------|
| `neutree.ai/cluster-version` | Logical version | `v1.0.0`, `v1.0.1-rc.1` |
| `neutree.ai/accelerator-type` | Accelerator type | `nvidia_gpu`, `amd_gpu` |

Accelerator variants of the same version share the same `cluster-version` label. Images without labels (dev/nightly builds) are excluded from the version query API.

### 4.3 SSH Cluster Upgrade Flow

Ray does not support mixed versions within a cluster. SSH upgrades require a full cluster rebuild:

```
prePullImages (blocking: cluster + engine images, all nodes concurrent)  <- services running
  |
downCluster (force stop workers + ray down)                              <- downtime starts
  |
upCluster (restart=true, new image)
  |
reconcileWorkerNode (start workers with new image)
  |
checkAndUpdateStatus (read status.version from Ray Dashboard)            <- cluster ready
  |
Endpoint reconcile -> redeploy Ray Serve applications                    <- downtime continues
  |
Endpoints ready (models loaded)                                          <- downtime ends
```

### 4.4 Kubernetes Cluster Upgrade Flow

```
reconcile -> update Router/Metrics Deployment image + Pod template labels
  |
K8s Rolling Update (old pods replaced by new pods)
  |
Component checks all Pod version labels match spec.version -> ready
  |
getDeployedVersion -> write status.version -> Running
```

Components (Router, Metrics) verify all running Pod version labels match `spec.version` during status checks. Mismatched pods cause the component to report not-ready, keeping the phase as `Upgrading` until rollout completes.

### 4.5 Manual Rollback

Reverting `spec.version` to the previous value triggers automatic rollback. All intermediate states are recoverable:

- **prePullImages failed**: Nodes still on old version, auto-recover
- **downCluster failed**: Nodes partially stopped, reconcile restores them
- **upCluster(v2) failed**: Head not started, reconcile rebuilds with v1
- **reconcileWorkerNode failed**: Head on v2, workers partial; reconcile detects version mismatch, rebuilds cluster

### 4.6 Available Cluster Versions API

```
GET /clusters/available_versions?workspace=default&image_registry=my-registry&cluster_type=ssh&accelerator_type=nvidia_gpu
```

| Parameter | Required | Description |
|-----------|----------|-------------|
| `workspace` | yes | Workspace name |
| `image_registry` | yes | Image registry name |
| `cluster_type` | yes | `ssh` -> queries `neutree-serve`, `kubernetes` -> queries `router` |
| `accelerator_type` | no | Filter by accelerator type; omit to return all labeled versions |

**Version discovery**:

1. List all image tags from the registry
2. Read `neutree.ai/cluster-version` label from each image config
3. Skip tags without the label (dev/nightly builds)
4. Filter by `accelerator_type` if specified, deduplicate by version label
5. Return sorted by semver

```json
{
  "available_versions": ["v1.0.0", "v1.0.1-rc.1", "v1.1.0"]
}
```

This API is cluster-independent and serves both **cluster creation** (version selection) and **cluster upgrade** (target version selection).
