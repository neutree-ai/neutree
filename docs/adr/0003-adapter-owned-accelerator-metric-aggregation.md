# Adapter-owned accelerator metric aggregation

Status: accepted (amended 2026-08-20)

The registry stores exactly one base `Accelerator` object for each
`accelerator_type`. `Accelerator` owns registration identity and driver-backed
`DiscoverHardware`. The same concrete object can additionally implement
`KubernetesAccelerator`, `StaticAccelerator`, or both. The Node-agent selects the
base object by explicit `accelerator_type`, then asserts the capability required
by explicit `cluster_type`; a missing registration or capability is a startup
error. It must not register separate Kubernetes/static vendor objects or infer
cluster mode from non-empty evidence fields.

The shared Node-agent layer gathers only the bounded raw evidence for the
selected cluster capability. `KubernetesAcceleratorEvidence` contains
`CommonAcceleratorEvidence`, Kubernetes PodResources, and Endpoint Pod metadata;
`StaticAcceleratorEvidence` contains the same common exporter evidence plus Ray
Dashboard actor and process evidence. `AllocationAvailable` distinguishes a
failed allocation-evidence collection from a successful collection with zero
allocations. `AcceleratorType` is registry/configuration state and is not part of
either evidence object.

Both capability interfaces return the same `AcceleratorMetricResult`. The
adapter owns all vendor semantics: exporter parsing, device identity,
resource-name filtering, visible-device and process interpretation, and joins
between physical measurements and scheduler allocation evidence. Shared code
only selects the cluster capability, validates descriptors, labels, and units,
adds common labels, serializes Prometheus output, and preserves CPU-only and
readiness behavior. Selecting a capability by configured cluster type is shared
orchestration, not vendor-semantic interpretation.

For NPU, `ProductName` preserves a driver/DCMI-reported human-readable product or
chip name, while `ProductModel` is the adapter-normalized stable family key from
the driver device type. The target adapter must populate both, and every generic
`product` label uses `ProductModel` (for the initial scope, `Ascend310P` or
`Ascend910B`). Exporter `model_name` and the 310P-only `product_type` are dynamic
join and cross-check inputs only. Ray and Device Plugin resource names remain
adapter-internal scheduler aliases; they must not become physical inventory or
metric product labels.

Permit-listed vendor info descriptors are allowed alongside the generic
contract. The initial NPU descriptor carries the driver version and an
`hccs_capable` product-capability bit. That bit records eligibility for the
pinned upstream HCCS collector; it does not assert that the collector is enabled,
the link is present or healthy, or Neutree publishes HCCS dynamic telemetry. If
any required info field is unknown, the adapter omits the complete info sample;
shared serialization must not manufacture an `unknown` label value.

`Accelerator`, `KubernetesAccelerator`, `StaticAccelerator`, the common and
cluster-specific evidence types, `AcceleratorMetricResult`, and the resolver
interfaces remain under `internal/observability/...`. They are ephemeral
Node-agent implementation data, not `api/v1` configuration or an external
plugin protocol; the public API continues to describe only deployable profiles
and runtime requirements.

Driver discovery, exporter dynamic evidence, and scheduler allocation evidence
degrade independently. If exporter parsing succeeds but the selected evidence
has `AllocationAvailable=false`, the adapter emits verified physical samples and
omits `allocated/free` and all replica samples. If collection succeeds with an
empty allocation set, `AllocationAvailable=true` allows explicit
`allocated=0/free=total`. The adapter emits neither a synthetic zero for unknown
allocation nor unknown values, and it does not change exporter readiness.

For a uniquely allocated whole card, replica `memory_allocated_bytes` is the
physical device's verified total memory capacity and `CoreUnits` is 100. For a
static, non-sliced card shared by multiple processes, the adapter preserves the
existing Ray fraction contract:
`MemoryMiB = round(device.MemoryMiB * gpuQuantity)` and
`CoreUnits = round(100 * gpuQuantity)` for an explicit finite
`0 < gpuQuantity < 1`; `MemoryMiB` is then published as
`memory_allocated_bytes`. Missing or invalid fraction evidence in a detected
shared-card case omits the affected allocation amounts and must not fall back to
the whole-card capacity. Replica `memory_used_bytes` is separately the sum of
its associated descendant process memory records; it must not use aggregate
device used memory. Physical utilization remains absent because it cannot be
attributed to one sharing process.

Adapters cannot emit arbitrary Prometheus names or cause vendor constants to
enter shared normalizers. Generic and vendor-info descriptors and their required
labels must be registered explicitly in the shared collector. This centralizes
the complete NPU data flow in the Enterprise adapter while allowing the current
GPU/DCGM implementation to migrate incrementally and preserving the type-less
legacy DCGM compatibility path during migration.

For static Ray NPU allocation, the adapter uses the Dashboard Backend Actor PID
as the process-tree root and joins its descendant NPU processes to
Exporter-reported `process_id` and `vdie_id`. Ascend visible-device environment
variables are diagnostic hints only, not allocation truth: verified workloads
can request two cards while observing all four logical device indexes.

The NPU adapter also derives the requested card count from the matched vendor
entries in the Dashboard Actor `required_resources` map, rather than GPU-only
`num_gpus` deployment configuration. A verified 310P Ray Serve fixture exposes
both `HUAWEI_Ascend310P` and `NPU` with equal values for the same card count.
The adapter reads one configured canonical resource and treats other configured
keys as equality-checked aliases, never as additive capacity. A missing,
non-integral, or unequal alias makes allocation and replica evidence ambiguous,
so those samples are omitted. Resource-name selection remains vendor adapter
semantics and does not enter shared Ray code.
