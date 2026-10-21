# Resource observations, first version

This version adds a read-only monitoring path for the cluster and endpoint basic-information pages. Allocation and scheduling remain on their existing resource-status path. No resource-status writes, database schema changes, or new external service are required. The branch pins its matching NodeAgent build for normalized power telemetry.

## Configuration

Pass `--metrics-url=<Prometheus-compatible query base URL>` to neutree-api. For VictoriaMetrics cluster tenant 0 this is `http://vmselect:8481/select/0/prometheus`. For Docker, set `NEUTREE_METRICS_QUERY_URL` to an address reachable from the API container (the observability stack may use a separate Docker network); Helm installations can set `metrics.queryUrl`. With the built-in VictoriaMetrics service enabled, an empty value uses that service automatically. Set an explicit value to query an external Prometheus-compatible service; disabling the built-in service and leaving this value empty leaves observations unconfigured without disabling allocation views.

The browser never supplies PromQL or a backend address. Cluster and endpoint routes require their respective workspace read permission. Cluster readers receive workload measurements only if they also hold endpoint read permission. Endpoint queries resolve the cluster from the stored endpoint.

## Data contract

- `GET /api/v1/clusters/:workspace/:name/accelerator-observations`
- `GET /api/v1/endpoints/:workspace/:name/accelerator-observations`
- `status`: `ok`, `no_data`, or `not_configured`; upstream failures return 503.
- Each sample has its metric name, allowlisted identity/hardware labels, numeric value, and original `observed_at` scrape timestamp.
- Only the most recent sample within a 90-second range is returned for each series. A range-vector query preserves scrape time; query-evaluation time is not a freshness signal.
- Queries have a five-second timeout and an eight-MiB response bound. Partial upstream responses are rejected.
- Zero is a valid measurement. Missing, expired, non-finite, and negative measurements are not converted to zero.
- Physical-device and replica measurements are distinct. The UI matches node, device UUID, endpoint, replica/instance, and virtual-device identity. Ambiguous replica observations are left unavailable. Duplicate scrapes are not added together.

## UI

The cluster's existing node GPU grid and endpoint runtime allocation grid show actual VRAM, allocated VRAM, physical capacity, and GPU utilization. The cluster grid also shows allocation segments with stable per-instance colors and links. GPU buttons open hardware/details drawers. Unknown hardware fields and unsupported telemetry display a dash. Temperature and power are explicitly labeled as whole-device measurements even on an endpoint page.

The first version keeps allocation segments separate from the physical-usage bar: summing workload usage is not guaranteed to equal physical-card usage. It does not infer measured usage from `allocatable - available`. FP16 throughput is explicitly an estimate (nominal or calibrated peak multiplied by utilization), not a hardware FLOPS counter.

## FLOPS and topology

The cluster resource summary includes healthy-device FP16 capacity and utilization-based estimates, with per-model overrides in `metadata.annotations["neutree.ai/accelerator-tflops"]`. Metadata-only updates preserve spec and other metadata. Unknown models or missing utilization keep totals/usage unknown. The dictionary uses longest substring matches and explicit per-product calibration wins.

`GET /api/v1/clusters/:workspace/:name/accelerator-utilization-trend` requires cluster read access and queries a server-owned 30-day window with a six-hour step. Each point uses six-hour mean utilization; duplicate scrape targets are reduced with max by node/UUID/product. The browser applies current calibration to historical reported devices. Gaps are not zeros and historical values do not reconstruct past calibration or health.

The topology tab groups current physical GPUs by node and reported NUMA domain, with PCIe details, temperature/power, assignment links, and a shared hardware drawer. Interconnect banners reflect node-reported capability flags, not measured pairwise edges.

## Deferred collector work

- 3.b: pairwise GPU matrix probing within a node.
- 3.g: collecting host RDMA NIC binding.

Other vendor hardware paths still require hardware-specific runtime validation.
