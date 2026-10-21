# HAMi Chart Modifications

Neutree embeds both the existing patched `hami-2.9.0.tgz` and the official
`hami-2.10.0.tgz` release chart repackaged with the scheduler rollout patch.
The Neutree cluster version selects a compatibility profile; the underlying
Kubernetes server version independently selects that profile's scheduler image.

## Cluster Version Matrix

`internal/component/version.go` defines the component image versions.
`internal/cluster/version/hami.go` owns the Neutree cluster compatibility
thresholds and HAMi selection. `internal/component/hami.go` owns the Kubernetes
scheduler pins and Kubernetes version normalization.
`clusterversion.HAMiVersionForCluster(clusterVersion)` resolves the HAMi version:

| Neutree cluster version | HAMi version | Embedded chart | Scheduler profile |
| --- | --- | --- | --- |
| Supported `1.1.x` and `1.2.x` versions | `v2.9.0` (`component.LegacyHAMi`) | `hami-2.9.0.tgz` | Existing 1.26–1.32 pins; detected-version fallback for unmapped minors |
| Base version `>= 1.3.0`, including RCs and build metadata | `v2.10.0` (`component.HAMi`) | `hami-2.10.0.tgz` | 1.26–1.35 pins; detected-version fallback for unmapped minors |
| Below `1.1.0`, empty, or invalid | Error | None | None |

The threshold uses the numeric base version: `1.3.0-rc.1` and
`1.3.0+build.1` select HAMi 2.10.0. Kubernetes server-version discovery does
not select the HAMi profile; upgrading Kubernetes alone does not change it.

Upgrading the control plane does not implicitly upgrade HAMi on an existing
Neutree 1.1/1.2 cluster. Reconciliation retains its 2.9.0 profile. A new cluster
explicitly selecting Neutree 1.3+ uses 2.10.0; an existing cluster moves to that
profile only when its Neutree cluster version is explicitly upgraded to 1.3+.
This does not upgrade Kubernetes. `component.HAMi` still means the latest HAMi
version, not the expected version for every cluster.

Both archives are loaded in-process with `loader.LoadArchive` in
`internal/cluster/component/hami/chart.go`.

## Legacy 2.9.0 Chart

The patched 2.9.0 archive is restored unchanged from the repository baseline,
not reconstructed from the 2.10.0 chart. Its existing Neutree patches remain:

- `device-config.content` overrides `files/device-config.yaml` and chart defaults.
- `scheduler.updateStrategy` defaults to a zero-unavailable rolling update
  (`maxUnavailable: 0`, `maxSurge: 1`).
- Scheduler pod anti-affinity is soft
  (`preferredDuringSchedulingIgnoredDuringExecution`) so a replacement can
  schedule on a single-node cluster while leader election protects the overlap.

## Upstream 2.10.0 Source

- Release: https://github.com/Project-HAMi/HAMi/releases/tag/v2.10.0
- Archive: https://github.com/Project-HAMi/HAMi/releases/download/v2.10.0/hami-2.10.0.tgz
- Original archive SHA-256: `e1d8429b2270da1a5c26343d6d6fd2a0099ba7b944cf0f52eb20d6b9f14e5099`

The checksum identifies the unmodified upstream 2.10.0 download, not either
repackaged archive committed here.

## Deviation From Upstream 2.10.0

### `scheduler.updateStrategy`

`templates/scheduler/deployment.yaml` renders `spec.strategy` from
`values.scheduler.updateStrategy`. The packaged `values.yaml` sets:

```yaml
scheduler:
  updateStrategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
```

This preserves the Neutree 2.9.0 chart's rollout behavior: a replacement must
become ready before the only scheduler can be removed. These are the only two
files changed from the upstream release archive.

## Former Patches Now Provided Upstream

- `device-config.content` is supported directly by 2.10.0. The precedence remains
  `device-config.content` > `files/device-config.yaml` > chart defaults, so
  cluster-specific device configurations do not require repackaging the chart.
- Scheduler pod anti-affinity is already
  `preferredDuringSchedulingIgnoredDuringExecution`. It allows a single-node
  cluster to schedule the replacement pod while retaining the multi-node spread
  preference. Leader election protects the brief overlap.

Neither behavior needs a local patch in 2.10.0. Keep that profile's upstream
device-config defaults, including the new MIG profile allowlist and NUMA
topology setting; do not apply them to the legacy chart.

## DRA and Webhook Configuration

The legacy 2.9.0 profile keeps `dra.enabled: false`; user `config_patch` cannot
enable it. Its optional DRA dependency need not be vendored for in-process
chart loading. HAMi 2.10.0 removes DRA from its main chart, so that
archive has no DRA dependency and the new profile does not set the obsolete
`dra.enabled` value. Neither profile installs HAMi-DRA or ascend-dra-driver;
user `config_patch` cannot enable DRA.

Neutree enforces `scheduler.admissionWebhook.enabled: true` and
`scheduler.admissionWebhook.manageNamespaceSelector: true`, supplies its own
TLS secret and CA bundle, and disables chart-managed certificate jobs. The
upstream webhook `failurePolicy: Ignore` and namespace selector remain intact;
namespaces labeled `hami.io/webhook: ignore` are excluded, but admission is not
restricted to the owning Neutree cluster namespace.

## Kubernetes Scheduler Images

Both profiles retain the existing Kubernetes 1.26–1.32 scheduler image pins
and the `v1.32.13` fallback when server-version discovery or parsing fails.
Only the Neutree 1.3+ profile adds pins for Kubernetes 1.33–1.35:

| Kubernetes minor | Legacy 1.1/1.2 profile | Neutree 1.3+ profile |
| --- | --- | --- |
| 1.26 | `v1.26.15` | `v1.26.15` |
| 1.27 | `v1.27.16` | `v1.27.16` |
| 1.28 | `v1.28.15` | `v1.28.15` |
| 1.29 | `v1.29.14` | `v1.29.14` |
| 1.30 | `v1.30.14` | `v1.30.14` |
| 1.31 | `v1.31.14` | `v1.31.14` |
| 1.32 | `v1.32.13` | `v1.32.13` |
| 1.33 | Detected server version | `v1.33.13` |
| 1.34 | Detected server version | `v1.34.12` |
| 1.35 | Detected server version | `v1.35.9` |

Unmapped minors use the detected server version, including its patch with
vendor suffixes removed. This applies to Kubernetes 1.33+ on the legacy
profile and 1.36+ on the new profile, as well as older unmapped minors. Such
clusters require a separately supplied offline scheduler image unless the
exact detected tag is already bundled; new-profile pins must not leak into
legacy scheduler selection.

The offline Kubernetes image list packages only the latest Neutree cluster
profile: HAMi `v2.10.0` and the 1.26–1.35 scheduler pins. The legacy 2.9.0 chart
remains embedded for existing Neutree 1.1/1.2 clusters, but its container image
is not included. Offline deployments of legacy clusters must supply their own
matching images.

Bundling or rendering an image is not real-cluster compatibility certification
and does not extend the Kubernetes project's security-support lifecycle.

## Node-Agent Artifacts

Node-agent publishing, builds, and image/version tiers remain independent work.
This change leaves the node-agent image-list entry unchanged; choosing a HAMi
profile does not publish or rebuild a matching node-agent artifact.

## Verification

- `go test ./internal/cluster/version` covers Neutree cluster compatibility
  gates, including HAMi selection and prerelease/base-version handling.
- `go test ./internal/cluster/component/hami` loads the embedded chart and
  renders every bundled Kubernetes minor, checking pinned images, scheduler
  configuration, absence of DRA, TLS/webhook protection, node scope, device
  configuration overrides/checksums, and zero-unavailable scheduler rollouts.
- `TestHAMiOfflineImages` checks that only the latest HAMi version and the
  latest profile's scheduler pins are in the offline Kubernetes image list.
- Reconciliation checks must distinguish unchanged legacy clusters from an
  explicit Neutree cluster-version upgrade to the 2.10.0 profile. Unit tests
  use fake clients and in-process rendering, not actual cluster certification.
- E2E virtualization coverage lives in
  `tests/e2e/accelerator_virtualization_test.go` and requires a configured
  Neutree deployment and GPU Kubernetes cluster. Its expected HAMi version is
  resolved with `clusterversion.HAMiVersionForCluster(cluster.GetVersion())`, so legacy
  profiles are not incorrectly required to run the latest HAMi.

This document does not assert that either profile has passed real-cluster E2E
validation. Local checks and compilation must be reported separately from any
deployment or compatibility certification.
