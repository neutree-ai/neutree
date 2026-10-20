# Resource observations, first version

This version adds a read-only monitoring path for the cluster and endpoint basic-information pages. Allocation and scheduling remain on their existing resource-status path. No NodeAgent rollout, resource-status writes, database migration, or new external service is required.

## Configuration

Pass `--metrics-url=<Prometheus-compatible query base URL>` to neutree-api. For VictoriaMetrics cluster tenant 0 this is `http://vmselect:8481/select/0/prometheus`. The Docker deployment template supplies this address; Helm installations can set `metrics.queryUrl`. An empty value disables observations without disabling allocation views.

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

The first version keeps allocation segments separate from the physical-usage bar: summing workload usage is not guaranteed to equal physical-card usage. It does not infer measured usage from `allocatable - available` or estimate actual TFLOPS from utilization.

## Deliberately deferred

No TFLOPS dictionary or calibration editor, historical charts, topology graph, CPU/RAM actual-usage changes, inference throughput/KV-cache summary, or new vendor telemetry collectors. Power appears only when the normalized metric already exists. Hardware availability depends on the installed collector and driver. Tests with synthetic data cover sharing/identity rules but do not certify hardware environments that were not exercised.
