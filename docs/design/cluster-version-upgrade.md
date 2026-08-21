# Cluster Version Upgrade

> **历史文档（NEU-605）**：本文保留早期集群升级流程作为历史记录，其中关于 typed `(version, cluster_type)` Profile、旧的 Profile fallback、tag discovery 和隐式 API 准入的描述已废止。当前 `ReleaseInfo`/`ClusterProfile` 契约、package、CLI、API、UI 和 E2E 设计以 [NEU-605 权威设计文档](https://gitlab.smartx.com/wei.huang/neutree-dev-docs/-/merge_requests/87) 及其配套 E2E 文档为准。

## 1. Background

Neutree v1.0.1 introduces two key improvements to cluster base images:

1. **Ray upgrade**: Ray runtime upgraded to 2.53.0 to support newer vLLM inference engine versions.
2. **Lighter cluster images**: The `neutree-serve` image has been slimmed down by removing unnecessary components.

Previously Neutree only supported cluster creation and configuration updates (node scaling, parameter changes) but lacked the ability to upgrade the cluster serving version. Existing v1.0.0 clusters could not be upgraded to v1.0.1+; users had to delete and recreate, causing service disruption and endpoint loss.

This feature adds in-place cluster version upgrade for both SSH and Kubernetes cluster types. Starting with the v1.2.0 control-plane baseline, cluster versions are validated and published through a control-plane-owned release matrix (`ReleaseInfo` + `ClusterProfile`) instead of image-registry scanning.

## 2. Goals

1. **In-place version upgrade**: Users change `spec.version` to trigger an upgrade without recreating the cluster. Existing endpoints are preserved.
2. **Minimize downtime**: SSH clusters pre-pull images before shutdown. Kubernetes clusters use rolling updates for zero or near-zero downtime.
3. **Observable upgrade status**: A new `Upgrading` phase is distinct from configuration `Updating`, giving users real-time upgrade visibility.
4. **Controlled version selection**: The control plane publishes which cluster versions are compatible with the running control-plane baseline, and every compatible version and cluster type carries an exact component image profile.
5. **Version discovery**: An API lists the available versions for one requested cluster type from the published release matrix, usable for both cluster creation and upgrade.

## 3. User Story

**As a** Neutree platform user,

**I want to** upgrade an existing cluster to a newer version,

**So that** I can get new features and performance improvements without destroying the cluster or losing endpoint configurations.

### Acceptance Criteria

1. Users can query available cluster versions for `ssh` or `kubernetes` (filtered to those compatible with the current control-plane baseline)
2. Changing `spec.version` triggers a version upgrade
3. Cluster phase shows `Upgrading` during the upgrade process
4. SSH clusters automatically pre-pull images (cluster + engine) to minimize downtime
5. Kubernetes clusters use Deployment rolling updates; zero downtime when replicas > 1
6. Downgrades and same-version patches are rejected by the API
7. After upgrade completes, cluster returns to `Running` with `status.version` updated

## 4. Design Overview

### 4.1 Version Fields

| Field | Location | Description |
|-------|----------|-------------|
| `spec.version` | `ClusterSpec.Version` | Desired version (user-set) |
| `status.version` | `ClusterStatus.Version` | Actual running version (system-detected); defaults to `spec.version` on first reconcile |

Phase determination compares `status.version` against `spec.version` (`needsVersionUpgrade`); a mismatch moves the cluster to `Upgrading` until the observed version converges.

### 4.2 Control-Plane Release Matrix

Starting with the v1.2.0 control-plane baseline, the control plane owns two global, non-workspace-scoped resources that define what cluster versions exist and which component images they use:

#### ReleaseInfo — compatibility matrix

Named by control-plane baseline (`v1.2.0`), one row per baseline:

```yaml
metadata:
  name: v1.2.0
spec:
  compatible_cluster_baselines: ["v1.1", "v1.2"]
```

`compatible_cluster_baselines` lists the cluster minor lines the running control plane can operate. It is the semantic gate for both cluster creation and version upgrade.

#### ClusterProfile — exact typed component profile

A `ClusterProfile` is identified by the tuple `(metadata.name, spec.cluster_type)`, not by version alone. The supported types are exactly `ssh` and `kubernetes`; the database enforces the tuple as the unique identity. A version can therefore have separate SSH and Kubernetes component material without one package replacing the other.

For example, the two `v1.2.0` profiles are separate records:

```yaml
# SSH profile
metadata:
  name: v1.2.0
spec:
  cluster_type: ssh
  components:
    ray_runtime: {image: neutree/neutree-serve, tag: v1.1.1}
    node_agent: {image: neutree/neutree-node-agent, tag: v1.1.0-rc.1}
    node_exporter: {image: quay.io/prometheus/node-exporter, tag: v1.8.2}
    vmagent: {image: victoriametrics/vmagent, tag: v1.115.0}

# Kubernetes profile
metadata:
  name: v1.2.0
spec:
  cluster_type: kubernetes
  components:
    kubernetes_runtime: {image: neutree/neutree-runtime, tag: v1.1.1}
    router: {image: neutree/router, tag: v1.1.1}
    node_agent: {image: neutree/neutree-node-agent, tag: v1.1.0-rc.1}
    node_exporter: {image: quay.io/prometheus/node-exporter, tag: v1.8.2}
    vmagent: {image: victoriametrics/vmagent, tag: v1.115.0}
    kube_state_metrics: {image: registry.k8s.io/kube-state-metrics/kube-state-metrics, tag: v2.15.0}
```

A cluster version is *profile-aware* when its base semver is `>= v1.1.0` (`isClusterProfileAwareVersion`). Profile-aware clusters resolve required component images from the exact `(version, cluster_type)` profile; legacy pre-`v1.1.0` versions keep rendering from embedded default images.

#### Bootstrap and persistence

On control-plane startup, `App.Run` resolves the current baseline (`currentControlPlaneBaseline`):

- A released build resolves its own stable baseline (e.g. `v1.2.0`, including nightly/RC normalization) and synchronizes that baseline.
- A development, dirty, or workflow-short-commit build binds to the newest persisted `ReleaseInfo` baseline and consumes it without overwriting it from a potentially older local catalog.
- If a development build has no valid persisted baseline, it falls back to the builder's stable baseline and synchronizes it to bootstrap the database.

For a baseline that is synchronized, `SynchronizeCurrentBaseline` creates or overwrites the `ReleaseInfo` and one full-version `ClusterProfile` for each supported cluster type, using edition-specific builders (`releaseprofile.ReleaseInfoBuilder` / `CurrentClusterProfileBuilder`). Community builders and official package construction both consume the same `pkg/releaseprofile` catalog. Historical profiles (`v1.1.0`, `v1.1.1`) are seeded once for both types when the v1.2.0 baseline is first introduced.

Cluster package import is intentionally more restrictive than Core-owned publishing: an existing `(version, cluster_type)` profile is a no-op only when its material is identical; different material is rejected with a conflict. `force_update` is not accepted for this endpoint, so an imported package cannot drift a previously published profile.

Both resources are stored as global internal state (`api.release_infos`, `api.cluster_profiles`) with RLS enabled and no user-facing REST resource; the control plane accesses them with the service role.

### 4.3 Version Validation

The API validates every cluster create and version patch against the current `ReleaseInfo` + `ClusterProfile`:

- **Create** (`validateClusterVersionCreate`): the requested version's minor must be listed in `compatible_cluster_baselines`, must not be below the current control-plane baseline minor, and must have an exact `ClusterProfile` for the requested cluster type.
- **Update** (`validateClusterVersionUpdate`): the update must be a **strict semver increase** (source is the observed `status.version` when present, else `spec.version`), the target minor must be compatible, and an exact profile for the current cluster type must exist.

Downgrades and same-version patches are rejected with a `10212` validation error, so the previously documented "revert to previous version to roll back" flow is **not** available at the API layer.

### 4.4 SSH Cluster Upgrade Flow

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

For profile-aware SSH clusters, the selected `ray_runtime` image is used as the base runtime image. An accelerator profile's `cluster_runtime.image_suffix` is appended to that selected tag (for example, `v1.1.1-rocm`) before the cluster image registry is applied.

### 4.5 Kubernetes Cluster Upgrade Flow

```
reconcile -> update Router/Metrics Deployment image + Pod template labels
  |
K8s Rolling Update (old pods replaced by new pods)
  |
Component checks all Pod version labels match spec.version -> ready
  |
getDeployedVersion -> write status.version -> Running
```

Components (Router, Metrics) verify all running Pod version labels match `spec.version` during status checks (`AllPodsMatchVersion`). Mismatched pods cause the component to report not-ready, keeping the phase as `Upgrading` until rollout completes.

The version label `neutree.ai/cluster-version` is still applied to Pods and used for this readiness check. It is **not** used for version discovery (see 4.7).

Endpoint model downloads also use the exact Kubernetes profile: the `model-downloader` init container receives `kubernetes_runtime`, not an image synthesized from the control-plane version. The resolved runtime image is recorded on the Deployment so an endpoint spec that is unchanged across a cluster-only upgrade keeps its prior runtime material.

### 4.6 Crash Recovery

Because manual rollback is not supported at the API layer, upgrade safety relies on reconcile-time recovery from interrupted states:

- **prePullImages failed**: Nodes still on old version, auto-recover
- **downCluster failed**: Nodes partially stopped, reconcile restores them
- **upCluster(v2) failed**: Head not started, reconcile rebuilds with the desired version
- **Head version mismatch**: If the head node's detected version differs from `spec.version` (e.g. an interrupted upgrade then a spec change), the SSH reconciler rebuilds the head node from scratch

### 4.7 Available Cluster Versions API

```
GET /clusters/available_versions?cluster_type=kubernetes
```

`cluster_type` is required and must be `ssh` or `kubernetes`; a missing or invalid value returns `400`. The handler returns the versions that are both **compatible with the current control-plane ReleaseInfo** and have an **exact imported ClusterProfile for that type**:

1. Load the current `ReleaseInfo` (compatible baselines) and the current control-plane baseline minor
2. List all `ClusterProfile`s
3. Keep profiles with the requested `cluster_type` whose minor is compatible and not below the current baseline minor
4. Sort by semver

```json
{
  "available_versions": ["v1.2.0"]
}
```

This replaces the earlier design of scanning image-registry tags and reading the `neutree.ai/cluster-version` image label. Version availability is now a control-plane publication decision, not an artifact of what happens to be pushed to a registry. Registry and accelerator-type discovery parameters no longer exist; `cluster_type` remains because it is part of profile identity.

### 4.8 Cluster Package Publication

Official cluster packages are built from the same release-profile catalog as Core seeding. The build emits the catalog-owned component images and a `cluster_profile` section in `manifest.yaml`; SSH AMD packages derive the ROCm runtime tag from the selected SSH profile. The package importer validates the manifest's typed profile before image handling, then registers it through the no-drift upsert contract only after the package images have been pushed successfully.

### 4.9 Confirmed Decisions

| Decision | Chosen behavior | Rejected behavior |
|----------|-----------------|-------------------|
| Profile identity | `(version, cluster_type)` with `ssh` and `kubernetes` | A single global profile per version |
| Profile material source | One release-profile catalog drives Core and official packages | Separate hard-coded Core and package image lists |
| Package import conflict | Exact same content is idempotent; different content is rejected | `force_update` or mutable replacement |
| Version discovery | Required `cluster_type` query filters profiles by family | Type-agnostic version discovery |

The internal read-only profile-version helper used by control-plane upgrade preflight returns only version/type identities. It does not expose component material as a general user-facing CRUD resource.

## 5. Migration Notes

- Clusters at `v1.1.0`+ reconcile through the exact `(version, cluster_type)` `ClusterProfile`; a cluster whose matching profile is missing (for example, after upgrading the control plane before profiles were seeded) fails validation on the next patch. Historical profiles are auto-seeded for both types on the v1.2.0 baseline bootstrap.
- `v1.0.x` clusters remain profile-unaware and continue rendering legacy embedded images.
- Existing deployments without the runtime-image annotation retain the legacy version-derived runtime image once, then receive the annotation on reconcile. This avoids changing their model-downloader image merely because annotation support was introduced.

## 6. Verification Strategy

### Unit test

- Validate typed catalog output, Core synchronization, route validation, no-drift upsert, package parser/import registration, release-line guard, preflight exact-profile checks, SSH accelerator suffixing, and Kubernetes runtime template rendering.

### DB test

- Verify the `(metadata.name, spec.cluster_type)` uniqueness constraint and typed profile persistence with the repository PostgreSQL/PostgREST test stack.

### E2E test

- Reuse cluster upgrade and control-plane upgrade coverage for profile-aware SSH and Kubernetes clusters. A control-plane preflight must reject an existing profile-aware cluster that lacks its exact typed profile.

### Manual test

- Import a built cluster package into an environment with a reachable registry and control plane, then confirm its exact typed profile is visible to validation and a create or upgrade request. This remains manual until the E2E environment can reliably build and import package artifacts.
