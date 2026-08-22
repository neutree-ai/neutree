# Enterprise-owned accelerator metrics adapters

Status: accepted (amended 2026-08-20)

Amendment: fixed v26.1.0 runtime evidence showed that exporter-derived
container/process attribution and driver-derived inventory have independent
availability. The amendment removes the earlier socket-free readiness
assumption and records that exporter failure must not erase driver inventory.
The 2026-08-20 amendment also distinguishes portable generic metrics from
explicitly allowlisted vendor info descriptors.

Neutree OSS will provide only a vendor-neutral node-agent accelerator adapter
registry, explicit accelerator-type plumbing, and behavior for missing or
unregistered adapters. The Enterprise node-agent image owns registration and
implementation of the plugin-owned `npu` adapter, NPU Exporter parsing, and
Ascend runtime requirements. This prevents DCMI and Ascend-specific metric
semantics from entering OSS while retaining a canonical generic
`neutree_accelerator_*` contract plus explicitly allowlisted vendor info
descriptors. CPU-only nodes and nodes without an
accelerator exporter continue to expose node and runtime metrics. The adapter
type is deployed atomically with the selected Node-agent image by the component
planner; it selects an exporter protocol but does not assert that every
node in a unified DaemonSet has accelerator hardware. A configured type without
a registered adapter fails Node-agent startup, while an unavailable local
exporter only suppresses exporter-derived dynamic and process samples; adapter
driver discovery continues to publish the verified hardware inventory. The
current OSS
planner retains its hardcoded community image; the initial Enterprise NPU
planner temporarily selects its hardcoded Enterprise image. Later per-cluster
release information resolves Enterprise and OSS component versions
independently. NPU Exporter is Neutree-managed in the initial delivery, with
the Enterprise profile owning its image, runtime requirements, and readiness
contract. Readiness is an explicit `AcceleratorExporterProfile` field rather
than a metrics-component special case: Kubernetes transforms it to an HTTP
readiness probe and static Docker to a component health check. The NPU profile
defaults to its `/metrics` endpoint with a 15-second initial delay for the pinned
runtime Profile. That delay is a deployment setting, not evidence of socket-free
startup: Kubernetes container/process evidence requires the containerd runtime
mount defined by ADR 0002 and the authoritative Ascend monitoring design. The
initial Kubernetes scope accepts CPU nodes plus only one matching
accelerator exporter type: its unified Node-agent DaemonSet takes one
`--accelerator-type`. Planning rejects multiple matching `AcceleratorPlugin`
types rather than choosing a priority or falling back. Static clusters may use
each node's `AcceleratorType` independently. No matching Kubernetes plugin
selects CPU-only mode: no accelerator exporter is deployed and the Node-agent
receives no accelerator type. The plugin-supplied
`AcceleratorProfile.MetricsExporter.Runtime.NodeSelector` is the sole
authoritative match condition for both managed exporter placement and this
Node-agent type decision; the initial model adds no parallel selector API.
Every profile that declares `MetricsExporter` must supply a non-empty, valid
runtime node selector; absence is a profile configuration error rather than an
all-node or CPU-only interpretation.
If Node-label changes later produce multiple matching types, the controller
validates before mutation, retains the last successful component deployment,
and reports an invalid metrics-component configuration until an administrator
restores one type.

For an explicit type, the component planner also derives
`--accelerator-exporter-port` and `--accelerator-exporter-metrics-path` from
the selected exporter profile and deploys them atomically with the Node-agent
image and type. Kubernetes discovers the local exporter Pod address and static
nodes use localhost, but neither hardcodes a vendor endpoint. The type-less
legacy DCGM path retains its existing fixed endpoint behavior for compatibility.

vmagent continues to scrape the selected Exporter Profile's metrics path and
retains every series emitted by the enabled NPU exporter collectors unchanged,
including process-level series, as customer diagnostics. Node-agent consumption
does not alter this raw path. Generic `neutree_*` descriptors form the portable
contract; explicitly allowlisted descriptors such as `nvidia_info` and
`npu_info` are vendor-specific managed contracts. Initial NPU Profile
configuration disables vNPU and other
unverified collector groups before they reach the Exporter, rather than by
vmagent relabeling.
For static clusters, the Ray Head vmagent uses the same file-SD per-node
accelerator-exporter targets as GPU. The shared NPU Profile carries the required
exporter argument `-ip=0.0.0.0`, so each static node-IP target and each
Kubernetes Exporter Pod IP is reachable; neither backend overrides it. The
Node-agent may still scrape the static endpoint through localhost.

The `npu` adapter uses same-device metric-family fallback rather than per-product
metric mapping: it prefers a complete HBM memory pair, falls back to a complete
DDR pair, and prefers overall over base utilization. A single managed DaemonSet
has one exporter runtime profile. Those products
may be mixed in one Kubernetes cluster only after hardware verification proves
that their image, mounts, device files, sockets, capabilities, and privileged
setting are one runtime compatibility group. Otherwise planning rejects the
product mix. The initial delivery supplies exactly one `npu` runtime profile
and introduces no product-selected exporter variants, so it publishes
Kubernetes support for both products only after that shared group is verified;
otherwise 910B Kubernetes support is deferred. The same single-profile limit
applies to static exporters.

The shared allocation flow owns Kubernetes PodResources and Ray Dashboard
replica discovery, while vendor device-reference resolution belongs to the
adapter. The NPU adapter resolves only Ray Actor PID descendant process records
into canonical `vdie_id` values. Ascend visible-device environment variables
are diagnostic hints only: verified two-card Actors can observe all four logical
device indexes, so neither those values nor a logical index can establish an
allocation fallback.
The same adapter owns Kubernetes PodResources `ResourceName` matching before
interpreting device IDs. Shared allocation code retains the resource records,
but must not merge device IDs from all plugins or contain vendor resource-name
constants.

The Enterprise adapter remains the single plugin-owned `npu` adapter for 310P
and 910B. It uses metric-family fallback rather than product-specific adapter
types or mappings, preserving one profile, component, and canonical metrics
contract. `model_name` is a normalized label and validation input, not a metric
mapping selector.
