"""Pump SGLang ``prometheus_client`` metrics through Ray's prometheus exporter.

Background
----------
SGLang's ``launch_server`` exposes ``/metrics`` by mounting a
``CollectorRegistry`` + ``MultiProcessCollector`` on a FastAPI route — see
SGLang ``python/sglang/srt/utils/common.py:add_prometheus_middleware``::

    registry = CollectorRegistry()
    multiprocess.MultiProcessCollector(registry)
    app.routes.append(Mount("/metrics", make_asgi_app(registry=registry)))

When we run SGLang via :class:`sglang.srt.entrypoints.engine.Engine` inside a
Ray Serve actor we do not get that route, but we **do** inherit the same
``PROMETHEUS_MULTIPROC_DIR`` environment variable (``Engine.__init__`` calls
``set_prometheus_multiproc_dir()`` when ``enable_metrics=True``). Constructing
an identical registry from the same shared dir gives us byte-for-byte
identical aggregated samples to what SGLang would have served externally —
no SGLang internal API is involved.

Ray Serve LLM has an upstream ``stat_loggers``-based SGLang metrics bridge in
progress, but that path depends on SGLang's pluggable metrics backend hooks
(``ServerArgs.stat_loggers`` and ``STAT_LOGGER_ROLE_*``), which first shipped in
SGLang v0.5.13. Neutree's current SGLang v0.5.10 image has
``metrics_collector.py`` but not those injection hooks, so the multiprocess
registry remains the compatible bridge point for this engine version.

This module polls that registry on a fixed cadence and re-emits each sample
through ``ray.util.metrics`` so Ray's prometheus exporter (already scraped by
vmagent's ``file_sd_configs``) sees SGLang metrics under the canonical
``ray_<name>`` path. Three Ray Serve replica context labels (``deployment``,
``replica``, ``application``) are attached at metric-create time so
dashboards can multi-tenant filter the same way they do for vLLM (which uses
``NeutreeRayStatLogger`` to inject the same triple).

The bridge never reaches into SGLang internals; it relies only on
``prometheus_client``'s documented multiprocess API and ``ray.util.metrics``.
"""

from __future__ import annotations

import asyncio
import logging
from typing import Dict, Tuple

from ray import serve
from ray.util.metrics import (
    Counter as RayCounter,
    Gauge as RayGauge,
)

logger = logging.getLogger("ray.serve")


def _sanitize_metric_name(name: str) -> str:
    """Replace characters Ray's metric API rejects.

    SGLang emits ``sglang:cache_config_info`` (colon-separated namespace), but
    ``ray.util.metrics`` rejects ``:`` since the move to OpenTelemetry naming.
    Replace with underscore so Ray can export the metric as
    ``ray_sglang_<suffix>``. vmagent converts that temporary Ray-safe name back
    to the canonical upstream ``sglang:<suffix>`` form for storage and queries.
    """
    return name.replace(":", "_")


def _replica_tags() -> Dict[str, str]:
    """Snapshot Ray Serve replica context once; tags are stable for the actor lifetime."""
    try:
        ctx = serve.get_replica_context()
        return {
            "deployment": ctx.deployment,
            "replica": ctx.replica_tag,
            "application": getattr(ctx, "app_name", ""),
        }
    except RuntimeError:
        # Ran outside a Serve replica (unit tests, REPL); skip context labels.
        return {}


class PromToRayBridge:
    """Pumps SGLang-emitted prometheus samples into Ray's metric API on a fixed cadence.

    The aggregated samples come from ``prometheus_client``'s
    ``MultiProcessCollector`` pointed at ``PROMETHEUS_MULTIPROC_DIR`` — the same
    mechanism SGLang's launch_server uses for its ``/metrics`` HTTP route.

    Parameters
    ----------
    prefix_allowlist:
        Only metric families whose names start with one of these prefixes are
        forwarded. Defaults to ``("sglang",)`` because Ray Serve already
        exports its own ``ray_serve_*`` metrics natively and we don't want to
        double-emit them.
    interval_s:
        Poll cadence. The default of 5s matches Ray's prometheus scrape
        granularity.
    """

    def __init__(
        self,
        prefix_allowlist: Tuple[str, ...] = ("sglang",),
        interval_s: float = 5.0,
    ) -> None:
        # Lazy import: PROMETHEUS_MULTIPROC_DIR must be set before
        # ``prometheus_client`` is loaded for multiprocess mode to take effect.
        from prometheus_client import CollectorRegistry, multiprocess

        self.registry = CollectorRegistry()
        multiprocess.MultiProcessCollector(self.registry)
        self.allowlist = tuple(prefix_allowlist)
        self.interval = float(interval_s)
        self._extra_tags = _replica_tags()
        self._extra_tag_keys: Tuple[str, ...] = tuple(self._extra_tags.keys())

        # Ray metrics are created lazily on first sight of each (name, label-set)
        # pair, so the bridge automatically picks up new metrics added by future
        # SGLang releases. tag_keys is fixed at construction in OpenCensus, so
        # different label-sets get separate Ray metric instances.
        self._counters: Dict[Tuple[str, Tuple[str, ...]], RayCounter] = {}
        self._gauges: Dict[Tuple[str, Tuple[str, ...]], RayGauge] = {}

        # SGLang uses ``multiprocess_mode="mostrecent"`` for every metric; that
        # means counter samples carry absolute totals, not increments. We
        # diff against the previous tick to recover delta semantics.
        self._counter_prev: Dict[Tuple[str, Tuple[Tuple[str, str], ...]], float] = {}

        logger.info(
            "[PromToRayBridge] init: allowlist=%s interval=%ss replica_tags=%s",
            self.allowlist,
            self.interval,
            list(self._extra_tag_keys),
        )

    async def run(self) -> None:
        """Cooperative poll loop; cancel the asyncio task to stop."""
        while True:
            try:
                await asyncio.sleep(self.interval)
                self._tick()
            except asyncio.CancelledError:
                logger.info("[PromToRayBridge] cancelled, exiting")
                raise
            except Exception:
                # Best-effort: never crash the actor over a metric blip.
                logger.exception("[PromToRayBridge] tick failed")

    # ---- internals ---------------------------------------------------------

    def _tick(self) -> None:
        for fam in self.registry.collect():
            if not any(fam.name.startswith(p) for p in self.allowlist):
                continue
            kind = fam.type
            if kind == "counter":
                self._emit_counter(fam)
            elif kind == "gauge":
                self._emit_gauge(fam)
            elif kind == "histogram":
                self._emit_histogram(fam)
            # summary / info / unknown silently dropped — SGLang doesn't use them.

    def _emit_counter(self, fam) -> None:
        for sample in fam.samples:
            # prometheus_client emits both ``<name>_total`` (the value) and
            # ``<name>_created`` (a metadata gauge). Drop the metadata.
            if sample.name.endswith("_created"):
                continue
            label_keys = tuple(sorted(sample.labels.keys()))
            metric = self._get_or_create_counter(fam.name, label_keys)
            key = (sample.name, tuple(sorted(sample.labels.items())))
            prev = self._counter_prev.get(key, 0.0)
            delta = sample.value - prev
            if delta < 0:
                # Counter reset (worker restart): re-baseline, do not emit
                # a negative inc().
                delta = sample.value
            self._counter_prev[key] = sample.value
            if delta > 0:
                metric.inc(delta, tags=self._full_tags(sample.labels))

    def _emit_gauge(self, fam) -> None:
        for sample in fam.samples:
            if sample.name.endswith("_created"):
                continue
            label_keys = tuple(sorted(sample.labels.keys()))
            metric = self._get_or_create_gauge(fam.name, label_keys)
            metric.set(sample.value, tags=self._full_tags(sample.labels))

    def _emit_histogram(self, fam) -> None:
        # Mirrors ray-project/ray#63123: emit raw `<name>_bucket{le=...}`,
        # `<name>_sum`, `<name>_count` samples as gauges. The `le` label is
        # carried through verbatim, so PromQL `histogram_quantile()` on the
        # consumer side reconstructs the histogram without observation
        # replay.
        #
        # Avoids `RayHistogram.observe()` — its API takes one value at a
        # time (no batch path through the Cython binding). At high QPS the
        # replay loop is O(observations) and CPU-bound; emitting cumulative
        # gauge values is O(buckets) per scrape regardless of rate. The
        # tradeoff is that downstream sees the metric typed as a gauge
        # rather than a histogram — fine for vmagent + Prometheus / Grafana
        # where `histogram_quantile()` only needs the `le`-labeled
        # timeseries.
        for sample in fam.samples:
            # `_created` is prometheus_client metadata (family creation
            # timestamp); we don't expose it.
            if sample.name.endswith("_created"):
                continue
            label_keys = tuple(sorted(sample.labels.keys()))
            # Use sample.name (not fam.name) so each suffix lands on its own
            # Ray gauge: `<name>_bucket`, `<name>_sum`, `<name>_count`. For
            # bucket samples this preserves the `le` label as a tag key,
            # which is exactly what `histogram_quantile()` needs.
            metric = self._get_or_create_gauge(sample.name, label_keys)
            metric.set(sample.value, tags=self._full_tags(sample.labels))

    # ---- ray.util.metrics get-or-create -----------------------------------

    def _get_or_create_counter(
        self, name: str, label_keys: Tuple[str, ...]
    ) -> RayCounter:
        ray_name = _sanitize_metric_name(name)
        key = (ray_name, label_keys)
        m = self._counters.get(key)
        if m is None:
            m = RayCounter(
                name=ray_name,
                description=name,
                tag_keys=label_keys + self._extra_tag_keys,
            )
            self._counters[key] = m
        return m

    def _get_or_create_gauge(
        self, name: str, label_keys: Tuple[str, ...]
    ) -> RayGauge:
        ray_name = _sanitize_metric_name(name)
        key = (ray_name, label_keys)
        m = self._gauges.get(key)
        if m is None:
            m = RayGauge(
                name=ray_name,
                description=name,
                tag_keys=label_keys + self._extra_tag_keys,
            )
            self._gauges[key] = m
        return m

    # ---- tag helpers -------------------------------------------------------

    def _full_tags(self, sample_labels) -> Dict[str, str]:
        # ``sample_labels`` is a frozen dict-like from prometheus_client; copy and
        # overlay replica-context tags so the returned dict is a fresh value.
        tags = dict(sample_labels)
        tags.update(self._extra_tags)
        return tags
