# HAMi Chart Modifications

This is the official `hami-2.10.0.tgz` release chart, repackaged with the
Neutree scheduler rollout patch described below.

## Upstream Source

- Release: https://github.com/Project-HAMi/HAMi/releases/tag/v2.10.0
- Archive: https://github.com/Project-HAMi/HAMi/releases/download/v2.10.0/hami-2.10.0.tgz
- Original archive SHA-256: `e1d8429b2270da1a5c26343d6d6fd2a0099ba7b944cf0f52eb20d6b9f14e5099`

The checksum identifies the unmodified upstream download, not the repackaged
archive committed here. The embedded chart is loaded with `loader.LoadArchive`
in `internal/cluster/component/hami/chart.go`.

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

Neither behavior needs a local patch in 2.10.0. Keep the upstream device-config
defaults, including the new MIG profile allowlist and NUMA topology setting.

## DRA and Webhook Configuration

HAMi 2.10.0 removes DRA from its main chart. This archive has no DRA dependency;
Neutree does not install HAMi-DRA or ascend-dra-driver and no longer sets the
obsolete `dra.enabled` value. User `config_patch` still cannot enable DRA.

Neutree enforces `scheduler.admissionWebhook.enabled: true` and
`scheduler.admissionWebhook.manageNamespaceSelector: true`, supplies its own
TLS secret and CA bundle, and disables chart-managed certificate jobs. The
upstream webhook `failurePolicy: Ignore` and namespace selector remain intact;
namespaces labeled `hami.io/webhook: ignore` are excluded, but admission is not
restricted to the owning Neutree cluster namespace.

## Kubernetes Scheduler Images

`KubeSchedulerVersionsByMinor` in `types.go` pins a scheduler image for each
bundled Kubernetes minor, from 1.26 through 1.35. The offline Kubernetes image
list must include every pin. Existing 1.26–1.32 pins and the 1.32 discovery-error
fallback are unchanged.

This update adds the following scheduler image pins:

| Kubernetes minor | Scheduler image tag |
| --- | --- |
| 1.33 | `v1.33.13` |
| 1.34 | `v1.34.12` |
| 1.35 | `v1.35.9` |

Unmapped minors, including 1.36 and later, continue using the detected server
version, including its patch with vendor suffixes removed, and require a
separately supplied offline scheduler image. Older unmapped minors are not added.

Bundling or rendering an image is not real-cluster compatibility certification
and does not extend the Kubernetes project's security-support lifecycle.

## Verification

- `go test ./internal/cluster/component/hami` loads the embedded chart and
  renders every bundled Kubernetes minor, checking pinned images, scheduler
  configuration, absence of DRA, TLS/webhook protection, node scope, device
  configuration overrides/checksums, and zero-unavailable scheduler rollouts.
- `TestHAMiOfflineImages` checks the scheduler/HAMi pins against the offline
  Kubernetes image list.
- `TestHAMiReconcileInstallsAndUpgradesEmbeddedChart` uses a fake client with
  Kubernetes `v1.33.13` for fresh installation and HAMi 2.9.0 → 2.10.0 upgrades.
- E2E virtualization coverage lives in
  `tests/e2e/accelerator_virtualization_test.go` and requires a configured
  Neutree deployment and GPU Kubernetes cluster.
