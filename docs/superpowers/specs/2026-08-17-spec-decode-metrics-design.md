# Speculative Decoding Metrics — Generation, Collection, Display

**Date:** 2026-08-17
**Status:** Draft (pending review)
**Scope:** vLLM + SGLang engines, both cluster types (K8s and SSH/static Ray), observability pipeline only — no speculative-decoding feature work.

## 1. Context

Neutree endpoints that run speculative decoding currently produce speculative-decoding performance
metrics inside the engine, but none of them surface in any Grafana dashboard. This spec covers the
metrics pipeline for speculative decoding end to end:

- **Generation** — what vLLM / SGLang emit, and whether it already flows out of the engine.
- **Collection** — whether both vmagent configurations (SSH static + K8s rendered) already carry the
  metrics.
- **Display** — where the metrics surface for operators (per-endpoint embed dashboard), and which
  panels to add.

Decisions taken with the stakeholder:

1. **Display surface** — add speculative-decoding panels to the per-endpoint embed dashboard
   (`neutree_endpoint_overview_embed_dashboard.json`). Not a new dashboard, not the upstream-converted
   engine dashboards.
2. **SGLang metric scope** — display what SGLang v0.5.10 provides (2 gauges) and document the
   asymmetry vs vLLM; no bridge-level metric synthesis, no engine upgrade.
3. **Verification depth** — Go unit tests + metric-surface verification (assert the canonical spec
   metric names survive relabeling on both paths). No real speculative-decoding E2E in this task.

## 2. Current State (verified by exploration)

### 2.1 Generation — already wired, no engine changes

**vLLM v0.24.0** (`vllm/v1/spec_decode/metrics.py`, `SpecDecodingProm`): all `prometheus_client.Counter`
exposed with `_total` suffix:

| Metric (canonical, after relabel) | Type | Labels |
| --- | --- | --- |
| `vllm:spec_decode_num_drafts_total` | Counter | engine, deployment, replica, application… |
| `vllm:spec_decode_num_draft_tokens_total` | Counter | engine, deployment, replica, application… |
| `vllm:spec_decode_num_accepted_tokens_total` | Counter | engine, deployment, replica, application… |
| `vllm:spec_decode_num_accepted_tokens_per_pos_total` | Counter | `position` 0..N-1 in addition |

- **SSH path:** `serve/_metrics/ray_stat_logger.py` → `NeutreeRayStatLogger` already extends
  `RaySpecDecodingProm` with Ray Serve context labels and is passed as `stat_loggers` in
  `serve/vllm/v0_24_0/app.py:208`. Metrics emit only when `SpeculativeConfig` is set.
- **K8s path:** template `internal/engine/vllm/v0.24.0/templates/kubernetes/default.yaml` runs plain
  `vllm serve` on `:8000`, which serves `/metrics` with native `vllm:` names. No change.

**SGLang v0.5.10** (`python/sglang/srt/observability/metrics_collector.py`, `SchedulerMetricsCollector`):

| Metric (canonical) | Type | Notes |
| --- | --- | --- |
| `sglang:spec_accept_length` | Gauge | engine-computed mean acceptance length; `multiprocess_mode="mostrecent"` |
| `sglang:spec_accept_rate` | Gauge | engine-computed acceptance rate (accepted / draft tokens); `multiprocess_mode="mostrecent"` |

- **SSH path:** `serve/_metrics/sglang_ray_bridge.py` → `PromToRayBridge` polls the multiprocess
  registry and forwards every `sglang:*` family (allowlist `("sglang",)`). Gauges are re-emitted
  verbatim. No change. Metrics are on by default: `setDefaultSGLangEnableMetricsForApplication` in
  `internal/orchestrator/ray_orchestrator.go` forces `enable_metrics=true` (backstopped by the
  `setdefault("enable_metrics", True)` in `app.py:241`).
- **K8s path:** `internal/engine/sglang/v0.5.10/templates/kubernetes/default.yaml` runs
  `sglang.launch_server` on `:8000`, which serves `/metrics` with native `sglang:` names. Metrics are on
  by default: `setDefaultSGLangEnableMetrics` in
  `internal/orchestrator/kubernetes_orchestrator_resource.go` forces `enable_metrics=true` when the
  endpoint does not set it.

### 2.2 Collection — already normalizes, no config changes

Both vmagent configs relabel metric names and drop nothing on `__name__`:

- **SSH static** `observability/vmagent/prometheus.yml` (deployed copy
  `deploy/docker/neutree-core/vmagent/prometheus.yml`, verified byte-identical):
  - `ray_vllm[:_](.+)` → `vllm:$1` (handles both the old `:` and Ray 2.53+ `_` forms)
  - `ray_sglang[:_](.+)` → `sglang:$1`
- **K8s rendered** `internal/cluster/component/metrics/vmagent_config.go` (`neutree-inference` job):
  - `sglang[:_](.+)` → `sglang:$1` (vLLM stays native `vllm:`)

With Ray pinned at `ray-2.53.0-neutree`, metric names on the SSH path take the underscore form
(`ray_vllm_spec_decode_num_drafts_total`), which the relabel regex maps back to the canonical
`vllm:spec_decode_num_drafts_total` form the dashboards query.

### 2.3 Display — the only real gap

`grep` across `observability/grafana/dashboards/*.json` (all deployed dashboards, including the
converted `vllm_grafana_dashboard.json` / `sglang_grafana_dashboard.json`) finds **no** speculative
decoding panels. Upstream vLLM (v0.8.5) and SGLang (v0.5.10) dashboards do not include them either.

The dashboard source of truth is `observability/grafana/dashboards/`; the `deploy/chart/...` and
`deploy/docker/obs-stack/...` copies are regenerated by the packaging pipeline.

## 3. Design

### 3.1 Display — panels in the per-endpoint embed dashboard

Add a **"Speculative Decoding"** section to
`observability/grafana/dashboards/neutree_endpoint_overview_embed_dashboard.json`, following the file's
existing conventions (schemaVersion 40, `${datasource}` variable, `neutree_cluster=~"$Cluster"`,
`application="$Endpoint"` filter, gridPos appended after the current last row at `y=30`).

Prometheus expressions (`{...}` = `{neutree_cluster=~"$Cluster",application="$Endpoint"}`;
`$__rate_interval` = `[$__rate_interval]`):

| Panel | Type | Expression |
| --- | --- | --- |
| Spec Decode Acceptance Rate | stat | `(sum(rate(vllm:spec_decode_num_accepted_tokens_total{...}$__rate_interval)) / sum(rate(vllm:spec_decode_num_draft_tokens_total{...}$__rate_interval))) or sglang:spec_accept_rate{...}` |
| Mean Spec Decode Acceptance Length | stat | `(1 + sum(rate(vllm:spec_decode_num_accepted_tokens_total{...}$__rate_interval)) / sum(rate(vllm:spec_decode_num_drafts_total{...}$__rate_interval))) or sglang:spec_accept_length{...}` |
| Spec Decode Draft tokens/s | timeseries | `sum(rate(vllm:spec_decode_num_draft_tokens_total{...}$__rate_interval))` (vLLM only) |
| Spec Decode Accepted tokens/s | timeseries | `sum(rate(vllm:spec_decode_num_accepted_tokens_total{...}$__rate_interval))` (vLLM only) |
| Per-position Acceptance Rate | timeseries | `sum by(position)(rate(vllm:spec_decode_num_accepted_tokens_per_pos_total{...}$__rate_interval)) / sum(rate(vllm:spec_decode_num_drafts_total{...}$__rate_interval))` (vLLM only), legend `position {{position}}` |

Notes:

- `A or B` yields the vLLM-computed series for vLLM endpoints and falls through to the SGLang gauge for
  SGLang endpoints, mirroring the `or` pattern already used by `neutree_endpoint_token_latency_embed_dashboard.json`.
- Acceptance rate/length are **derived via PromQL** from vLLM counters (per upstream docstring) and read
  directly from SGLang gauges. Units: rate = percentunit (both engines emit 0..1), length = tokens.
- The three vLLM-only panels render no-data for SGLang endpoints. That is intentional and documented
  (decision 2).

Layout (gridPos, appended after the current last row which ends at `y=30`):

- `y=30`: text header panel "Speculative Decoding" (`w=24, h=1`).
- `y=31`: two stat panels side by side — Acceptance Rate (`x=0, w=6`) and Mean Acceptance Length
  (`x=6, w=6`), each `h=4`.
- `y=35`: two timeseries panels side by side — Draft tokens/s (`x=0, w=12`) and Accepted tokens/s
  (`x=12, w=12`), each `h=8`.
- `y=43`: Per-position Acceptance Rate (`x=0, w=24, h=8`).

### 3.2 Verification — unit tests + metric-surface verification (CI-enforced)

Add table-driven tests in `internal/cluster/component/metrics/metrics_resource_test.go`:

- **Relabel surface:** for each canonical spec metric (vLLM: 4 counters, SGLang: 2 gauges) and for
  each raw form the scrapers can see, extract the `metric_relabel_configs` `__name__` rules from both
  configs and apply them with Go's `regexp` (Prometheus relabel regexes are RE2, matching Go), asserting
  the canonical `vllm:` / `sglang:` form is produced:
  - SSH static config (`observability/vmagent/prometheus.yml`):
    `ray_vllm_spec_decode_num_drafts_total` and `ray_vllm:spec_decode_num_drafts_total` →
    `vllm:spec_decode_num_drafts_total`; `ray_sglang_spec_accept_length` → `sglang:spec_accept_length`.
  - K8s rendered config: `sglang:spec_accept_rate` and `sglang_spec_accept_rate` →
    `sglang:spec_accept_rate`; assert vLLM names pass through unchanged.
- **No drop:** assert neither config filters `__name__` with an `action: keep` / `action: drop` rule, so
  spec metrics are never excluded.

This is the "metric-surface verification": it proves the expected six metrics survive relabeling on both
cluster types. (Optional best-effort, not CI-wired: a `serve/_metrics` pytest that the `PromToRayBridge`
allowlist forwards `sglang:spec_accept_*`.)

### 3.3 Documentation

Add `docs/speculative-decoding-metrics.md` (English, mirroring the style of `docs/npu-metrics-support-matrix.md`):

- Metric-surface parity table (vLLM 4 counters vs SGLang 2 gauges) and the PromQL derivations.
- Both-cluster-type data flow (SSH: engine → Ray exporter → vmagent → VictoriaMetrics; K8s: engine
  `/metrics` → vmagent → remote write).
- **SGLang v0.5.10 limitation + upgrade path:** newer SGLang versions already add
  `sglang:spec_verify_calls_total`, `sglang:spec_num_steps`, `sglang:spec_num_draft_tokens`,
  `sglang:spec_verify_*` timing metrics; the panel set above is engine-agnostic and these appear
  automatically after an engine upgrade (vLLM-only panels start showing SGLang data where semantics
  align).

## 4. Files touched

| File | Change |
| --- | --- |
| `observability/grafana/dashboards/neutree_endpoint_overview_embed_dashboard.json` | Add Speculative Decoding section (1 text header + 5 panels) |
| `internal/cluster/component/metrics/metrics_resource_test.go` | Add relabel-surface + no-drop tests |
| `docs/speculative-decoding-metrics.md` | New doc |
| `observability/vmagent/prometheus.yml`, `internal/cluster/component/metrics/vmagent_config.go`, engine templates, `serve/_metrics/*` | **No change** (verified already functional) |

`deploy/chart/neutree/grafana-dashboards/` and `deploy/docker/obs-stack/grafana/dashboards/` are
packaging-generated copies of `observability/`; they are regenerated by the existing build pipeline, not
edited directly.

## 5. Dependencies & risks

1. **UI coordination (open):** the endpoint detail page embeds dashboards by UID from a separate UI
   repo (no frontend lives in this repo). Adding panels to the overview embed surfaces them only if the
   UI embeds the full dashboard. Before shipping, confirm with the UI owner whether the embed renders
   the full dashboard or a fixed panel list; if the latter, a UI-side change is required and this task's
   display work lands but is not visible until that lands.
2. **SGLang metrics are on by default but can be explicitly disabled:** `setDefaultSGLangEnableMetrics`
   (K8s path) and `setDefaultSGLangEnableMetricsForApplication` (SSH path) force `enable_metrics=true`
   unless the endpoint sets it. An endpoint may still set `enable_metrics: false` to turn them off (all
   SGLang metrics, not just spec decode, then go missing — inconsistent with vLLM, but the endpoint's
   explicit choice).
3. **Conditional emission:** spec metrics appear only when the endpoint actually enables speculative
   decoding (vLLM `speculative_config`; SGLang spec parameters). Enabling speculative decoding itself is
   out of scope.
4. **Metric asymmetry:** vLLM exposes counters (rate/length derived via PromQL, per-position vector);
   SGLang v0.5.10 exposes only two engine-computed gauges. Documented, not engineered around.

## 6. Out of scope

- Enabling/configuring speculative decoding on endpoints (engine args / recipe surface).
- Enriching SGLang metrics at the bridge or engine level.
- Upgrading the SGLang engine version to obtain richer spec metrics.
- Real speculative-decoding E2E against a live endpoint.
