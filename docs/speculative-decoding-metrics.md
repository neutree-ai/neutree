# Speculative Decoding Metrics

This document describes how speculative-decoding performance metrics flow from the
inference engines (vLLM / SGLang) into VictoriaMetrics and how they surface in Grafana,
for both cluster types (Kubernetes and SSH/static Ray).

Speculative decoding is a latency-optimization technique in which a small draft model
proposes tokens and the large target model verifies them in parallel, accepting some or
all of the proposed tokens. The metrics below report how well that process is working.

Design doc: [Speculative Decoding Metrics design](superpowers/specs/2026-08-17-spec-decode-metrics-design.md)

## Metric surface

### vLLM (v0.24.0)

vLLM exposes speculative-decoding **counters** (Prometheus `_total` suffix at scrape time).
Acceptance rate and length are derived with PromQL (see below).

| Metric | Type | Meaning |
| --- | --- | --- |
| `vllm:spec_decode_num_drafts_total` | Counter | Number of speculative decoding drafts (verify steps). |
| `vllm:spec_decode_num_draft_tokens_total` | Counter | Number of draft tokens proposed. |
| `vllm:spec_decode_num_accepted_tokens_total` | Counter | Number of draft tokens accepted by the target model. |
| `vllm:spec_decode_num_accepted_tokens_per_pos_total` | Counter | Accepted tokens per draft position (label `position` 0..N-1). |

### SGLang (v0.5.10)

SGLang exposes speculative-decoding **gauges** — engine-computed averages per batch:

| Metric | Type | Meaning |
| --- | --- | --- |
| `sglang:spec_accept_length` | Gauge | Mean acceptance length of speculative decoding (accepted drafts + bonus token per forward). |
| `sglang:spec_accept_rate` | Gauge | Speculative acceptance rate (`accepted drafts / proposed drafts` in batch). |

> **Asymmetry:** vLLM exposes counters (so acceptance rate/length and per-position
> behaviour are derived in PromQL), while SGLang v0.5.10 exposes only two engine-computed
> gauges. The Grafana panels below work for both engines (vLLM-derived values for vLLM
> endpoints, engine gauges for SGLang endpoints). Newer SGLang versions add richer spec
> metrics (`sglang:spec_verify_calls_total`, `sglang:spec_num_steps`,
> `sglang:spec_num_draft_tokens`, and `spec_verify_*` timing histograms); the panels are
> engine-agnostic and pick these up automatically after an engine upgrade.

## Data flow

### SSH / static Ray cluster

```
engine (vLLM / SGLang)
  └─> Ray Serve replica exports engine metrics as ray_<engine>_<metric>
       (SGLang via PromToRayBridge; vLLM via NeutreeRayStatLogger)
  └─> vmagent (static config observability/vmagent/prometheus.yml)
       └─ relabel: ray_vllm[:_](.+) -> vllm:$1
                   ray_sglang[:_](.+) -> sglang:$1
  └─> VictoriaMetrics
```

### Kubernetes cluster

```
engine Pod (vLLM `vllm serve` / SGLang `sglang.launch_server` on :8000/metrics)
  └─> vmagent (rendered config, neutree-inference job)
       └─ relabel: sglang[:_](.+) -> sglang:$1   (vLLM names pass through unchanged)
  └─> remote write -> VictoriaMetrics
```

Metrics are **enabled by default** on both paths:

- **vLLM:** the v0.24.0 metrics endpoint is always registered; the SSH path passes
  `stat_loggers` unconditionally.
- **SGLang:** the orchestrator forces `enable_metrics=true` unless the endpoint explicitly
  sets it — `setDefaultSGLangEnableMetrics` on the Kubernetes path and
  `setDefaultSGLangEnableMetricsForApplication` on the Ray path. An endpoint may still opt
  out with `enable_metrics: false` (then all SGLang metrics, not just spec decode, are absent).

Spec-decoding metrics are **emitted only when the endpoint enables speculative decoding**
(vLLM `speculative_config`; SGLang spec parameters). Merely enabling metrics is not enough.

## Grafana panels

The **Neutree Endpoint Overview Embed** dashboard
(`observability/grafana/dashboards/neutree_endpoint_overview_embed_dashboard.json`,
embedded in the endpoint detail page) includes a *Speculative Decoding* section.

Filtering: `{neutree_cluster=~"$Cluster", application="$Endpoint"}`, rate window
`$__rate_interval`. Panels:

| Panel | Expression |
| --- | --- |
| Spec Decode Acceptance Rate | `(sum(rate(vllm:spec_decode_num_accepted_tokens_total{...}[$__rate_interval])) / sum(rate(vllm:spec_decode_num_draft_tokens_total{...}[$__rate_interval]))) or sglang:spec_accept_rate{...}` |
| Mean Spec Decode Acceptance Length | `(1 + sum(rate(vllm:spec_decode_num_accepted_tokens_total{...}[$__rate_interval])) / sum(rate(vllm:spec_decode_num_drafts_total{...}[$__rate_interval]))) or sglang:spec_accept_length{...}` |
| Spec Decode Draft tokens/s | `sum(rate(vllm:spec_decode_num_draft_tokens_total{...}[$__rate_interval]))` |
| Spec Decode Accepted tokens/s | `sum(rate(vllm:spec_decode_num_accepted_tokens_total{...}[$__rate_interval]))` |
| Per-position Spec Decode Acceptance Rate | `sum by(position)(rate(vllm:spec_decode_num_accepted_tokens_per_pos_total{...}[$__rate_interval])) / sum(rate(vllm:spec_decode_num_drafts_total{...}[$__rate_interval]))` |

Notes:

- Acceptance rate unit is percentunit; both engines emit a rate in the 0..1 range for typical
  load, but SGLang's gauge can exceed 1 near-perfect acceptance (its definition includes the
  bonus token). Acceptance length is in tokens.
- vLLM acceptance rate/length are derived in PromQL from counters (per upstream
  docstring); SGLang reads engine gauges directly. `A or B` yields vLLM-derived series for
  vLLM endpoints and falls through to the SGLang gauge for SGLang endpoints.
- The three vLLM-only panels (draft/accepted tokens/s, per-position rate) render no-data on
  SGLang endpoints because SGLang v0.5.10 does not expose the underlying counters.

## Prerequisites

- The endpoint must run a vLLM or SGLang engine (spec-decode metrics do not apply to
  llama.cpp).
- The endpoint must enable speculative decoding (vLLM `speculative_config`; SGLang spec
  parameters) for the spec-decode metrics to be emitted.
- SGLang `enable_metrics` defaults to on, but an endpoint may explicitly disable it.

## Out of scope

- Enabling/configuring speculative decoding itself (engine args / recipe surface).
- Enriching SGLang metrics at the bridge or engine level.
- Upgrading the SGLang engine version to obtain richer spec metrics.
