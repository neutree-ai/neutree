# HAMi Virtualization Mode (template vs core)

## Metadata

- Date: 2026-08-11
- Branch: `feat-NEU-645`
- Scope: Community `neutree` — public virtualization contract, cluster spec/status, endpoint admission. Enterprise Ascend plugin consumption (NEU-647/648) is downstream.

## Background

HAMi exposes two virtualization mechanisms that differ in what a workload may request:

- **core mode** (NVIDIA default): memory and compute core are both free parameters.
  Resources: `nvidia.com/gpumem`, `nvidia.com/gpucores`.
- **template mode** (Ascend default): the device plugin hard-slices the NPU along
  predefined per-chip templates (`hami-scheduler-device` ConfigMap `vnpus` section).
  A memory request rounds **up** to the nearest template; `aiCore` is fixed by the
  template and is not a free parameter. Resources: `huawei.com/AscendXXX`,
  `huawei.com/AscendXXX-memory`. Ascend core mode (`devices.ascend.hamiVnpuCore`,
  soft slicing via `libvnpu`) additionally honors `huawei.com/AscendXXX-core`, but
  requires ARM nodes and driver >= 25.5.

Current Neutree code has no mode concept: `virtualization.core_percent` is a flat
string key validated only for shape (0..100), and the NVIDIA converter maps it to
`nvidia.com/gpucores` unconditionally. There is no way for the UI to know that
template mode does not accept `core_percent`, and no way to switch a cluster to
Ascend core mode.

## Goals

- Model template vs core as a first-class, cluster-level concept.
- Surface per-cluster virtualization compatibility to the UI (which
  `virtualization.*` keys are legal under the active mode).
- Reject `core_percent` at endpoint admission when the active mode is template.
- Provide a user configuration knob for mode switching (defaults: ascend=template,
  nvidia=core).

## Design Decisions

| Decision | Conclusion |
| --- | --- |
| Mode level | Cluster-level (`spec.accelerator_virtualization.mode`); endpoints inherit |
| Config surface | First-class `mode` field (enum `template`/`core`), not `config_patch` |
| Status | New top-level `status.accelerator_virtualization` block: `mode` + `supported_resources` |
| Enforcement | Endpoint admission rejects `core_percent > 0` when active mode is template; converter double-checks |
| Config resolution | No new parameter — plugins read the mode from `cluster.Spec.AcceleratorVirtualization.Mode` |
| Edge gate | Switching core -> template is blocked while any vGPU endpoint (any virtualization key set) exists |

## Contract Changes

`pkg/accelerator/virtualization.go`:

```go
type VirtualizationMode string

const (
    VirtualizationModeTemplate VirtualizationMode = "template"
    VirtualizationModeCore     VirtualizationMode = "core"
)

type VirtualizationConfig struct {
    // existing fields (Supported, BlockingReasons, CandidateNodes,
    // NodeScopeLabel, DevicePluginTemplate, ConfigPatch)
    Mode               VirtualizationMode   // effective mode for this cluster
    DefaultMode        VirtualizationMode   // ascend=template, nvidia=core
    SupportedModes     []VirtualizationMode // ascend=[template,core], nvidia=[core]
    SupportedResources []string             // flat keys legal under the effective mode,
                                            // e.g. ["virtualization.memory_mib", "virtualization.core_percent"]
}
```

Mode-to-chart-values mapping stays plugin-private. The Ascend plugin builds its
ConfigPatch per mode (`devices.ascend.enabled: true`, `devices.ascend.hamiVnpuCore:
<mode==core>`, plus runtimeClassName/nodeSelector as needed). The user-facing
`config_patch` whitelist (`scheduler`, `global`) is unchanged — `devices` is never
exposed to users.

`ClusterVirtualizationConfigProvider.ResolveClusterVirtualizationConfig(ctx,
*cluster)` keeps its signature. The plugin computes:

```
effectiveMode = userSpecifiedMode or DefaultMode, validated against SupportedModes
```

All resolution call sites (`planNodeScope`, `DisableNodeScope`, status) already pass
the cluster, so no new parameter is needed. Unsupported explicit modes are not
rejected at cluster admission (static check only); they surface as HAMi component
NotReady with a clear reason.

## Cluster Spec

```yaml
spec:
  accelerator_virtualization:
    enabled: true
    mode: core   # optional; empty = plugin default (ascend=template, nvidia=core)
```

- Cluster admission (static, alongside the existing `config_patch` whitelist check):
  `mode` must be `template` or `core` when set.
- Reconcile gate: if the user mode is not in the plugin's `SupportedModes`, the HAMi
  component reports NotReady and does not apply.

## Cluster Status

New top-level block (written by the HAMi component from the resolved config):

```yaml
status:
  accelerator_virtualization:
    mode: core                    # effective mode
    supported_resources:          # keys legal under the effective mode
      - virtualization.memory_mib
      - virtualization.core_percent
```

The existing `status.component_status.accelerator_virtualization` (phase/version/
reason/message) is unchanged. The capability block exists only while virtualization
is enabled — which is exactly when endpoint admission (requiring phase Ready) can
observe it.

## Endpoint Admission

`validateEndpointVGPUEffective` gains a mode gate: if `core_percent > 0` and the
cluster's effective mode is `template`, reject with a dedicated error code and a
hint ("switch the cluster virtualization mode to core, or remove core_percent").

- Defensive fallback: if the status capability block is missing (stale), fall back
  to the current shape-only validation.
- Result: `core_percent` never reaches the converter on a template-mode cluster.

## Converter

- The orchestrator (`convertToKubernetes`, which already resolves the endpoint's
  cluster) passes the effective mode into converter calls (extended signature or
  optional request field on the converter DTO).
- NVIDIA converter: ignores the mode; behavior unchanged.
- Enterprise Ascend converter: `memory_mib -> huawei.com/AscendXXX-memory`; in core
  mode `core_percent -> huawei.com/AscendXXX-core`; in template mode defensively
  rejects `core_percent` (belt-and-braces, since admission already blocks it).

## UI Behavior

- Endpoint form (on clusters with virtualization enabled): render fields from the
  cluster's `supported_resources` — template shows only `memory_mib`; core shows
  `memory_mib` + `core_percent`. This is the Ascend compatibility surface.
- Cluster virtualization form: `mode` dropdown (`template`/`core`) with empty =
  plugin default (hint text states the defaults); an unsupported explicit choice
  surfaces as NotReady with reason.

## Edge Cases

| Case | Handling |
| --- | --- |
| Existing NVIDIA clusters on upgrade | Status gains `mode=core` + resources; no behavior change |
| Any vGPU endpoint exists, cluster switches core -> template | Cluster proxy gate rejects the switch (mirrors the existing disable gate for vGPU endpoints) |
| template -> core switch | Safe (capability superset); allowed |
| Already-deployed pods | Admission guards create/patch only; running deployments are unaffected |
| memory_percent | Unchanged: rejected by Community, never listed in `supported_resources` |

## Non-Goals

- Per-endpoint mode overrides (cluster-level only).
- Exposing `devices.*` chart keys through `config_patch`.
- Ascend node-level `huawei.com/vnpu-mode` annotation override.
- Memory rounding preview against per-chip templates (future per-product metadata work).
- Any runtime/E2E verification of Ascend hardware (deferred to NEU-647/648).

## Test Plan Sketch

- Plugin contract: default mode, supported modes, per-mode ConfigPatch and
  supported-resources resolution (table-driven, NVIDIA + fake Ascend-style provider).
- Cluster admission: mode enum validation; config_patch whitelist unchanged.
- HAMi component: effective mode in status; NotReady when mode unsupported.
- Endpoint admission: `core_percent` rejected under template mode (new code),
  accepted under core mode, shape-only fallback when status capability missing.
- Cluster proxy: core -> template switch blocked while any vGPU endpoint exists.
- Converter: mode passthrough; NVIDIA behavior unchanged.

## Implementation Deviations

- **Converter is untouched.** The design above proposed passing the effective mode
  into converter calls (extended signature) so the Enterprise Ascend converter
  could defensively reject `core_percent` in template mode. This was scoped out:
  `Converter` and `/v1/resource/convert-to-kubernetes` keep their contracts
  unchanged. The invariant "no `core_percent` reaches the converter under template
  mode" is instead guaranteed by two admission gates — endpoint admission (code
  10227) rejects the create/patch, and the cluster proxy gate (code 10228) blocks
  switching to template while any vGPU endpoint exists. Both run before converter
  resolution.
- **Mode switch gate semantics.** The edge gate blocks core -> template while *any*
  vGPU endpoint exists (any `virtualization.*` key set), not only endpoints with
  `core_percent > 0`. vGPU endpoints are incompatible with template mode regardless
  of the parameters they request, so the narrower check would have admitted an
  invalid switch.
- **Clear mode (explicit empty) is not gated.** Setting `spec.accelerator_virtualization.mode`
  back to empty restores the plugin default and is allowed even with vGPU endpoints
  present; only an explicit switch to `template` is blocked.

