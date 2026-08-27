# HAMi Chart Modifications

This chart is a repackaged `hami-2.9.0` with a cherry-picked upstream extension
that lets the scheduler device-config be supplied through chart values.

## Deviation from upstream hami-2.9.0

### `device-config.content` values override

Upstream `Project-HAMi/HAMi` master added the ability to provide the full
`device-config.yaml` payload through `values.device-config.content` instead of
shipping a `files/device-config.yaml` inside the chart or editing the hardcoded
defaults. This repackaged chart carries that change on top of 2.9.0.

**`values.yaml`** adds:

```yaml
# Device config overrides
# If content is set under device-config.content, it will be used as the device-config.yaml
# payload in the hami-scheduler-device ConfigMap instead of files/device-config.yaml or the default config.
device-config:
  content: ""
```

**`templates/scheduler/device-configmap.yaml`** renders the ConfigMap payload
with this precedence:

```yaml
data:
  device-config.yaml: |-
  {{- if and (index .Values "device-config") (index .Values "device-config" "content") }}
  {{- (index .Values "device-config" "content") | nindent 4 }}
  {{- else if .Files.Glob "files/device-config.yaml" }}
  {{- .Files.Get "files/device-config.yaml" | nindent 4}}
  {{- else }}
  ...default hardcoded config...
```

So the precedence is: `device-config.content` > `files/device-config.yaml` >
the hardcoded default. This lets a cluster supply its own device-config
templates (for example chip hard-slice templates) through chart values without
rebuilding the chart.

### `scheduler.updateStrategy` value

The scheduler Deployment previously used the Kubernetes default RollingUpdate
strategy (25% surge / 25% unavailable). On a single-replica scheduler with no
hostPort binding, an update could take down the only scheduler before its
replacement became ready. The chart now renders `spec.strategy` from
`values.scheduler.updateStrategy`; the packaged default is
`maxUnavailable: 0, maxSurge: 1` so the new scheduler is created before the
old one is removed and a single-node cluster never loses its only scheduler.

### Soft scheduler pod anti-affinity

The scheduler pod anti-affinity (active when `scheduler.leaderElect` is true)
is now `preferredDuringSchedulingIgnoredDuringExecution` instead of the
upstream hard `requiredDuringSchedulingIgnoredDuringExecution`. On a
single-node cluster the hard form made a scheduler rollout unschedulable
(the new pod could never satisfy the different-host constraint); the soft form
keeps the spread intent on multi-node clusters while allowing co-location when
no other node is available. Leader election still guards against two active
schedulers during the brief rollout overlap.

## Webhook configuration

Neutree does not override the HAMi chart's `failurePolicy`, so the packaged
chart default (`Ignore`) applies. It intentionally does not override the
chart's `namespaceSelector`, so matching admission requests are not limited to
the owning cluster namespace. The HAMi chart still emits its default selector,
which excludes namespaces marked `hami.io/webhook: ignore`.

Neutree also enforces `scheduler.admissionWebhook.enabled: true`; it cannot be
disabled through `accelerator_virtualization.config_patch`.

## Verification

- `helm template` renders `device-config.content` into the
  `hami-scheduler-device` ConfigMap when set, and falls back to the default
  config when empty.
- The chart is loaded in-process via `loader.LoadArchive` (see
  `internal/cluster/component/hami/chart.go`), which does not require the
  optional `hami-dra` dependency to be vendored.
