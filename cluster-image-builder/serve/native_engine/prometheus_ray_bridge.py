"""Bridge native engine Prometheus samples into Ray's metrics exporter."""

from __future__ import annotations

import asyncio
import logging
from collections.abc import Awaitable, Callable
from typing import Any

from prometheus_client.parser import text_string_to_metric_families

logger = logging.getLogger("ray.serve")


def _ray_metric_classes() -> tuple[type[Any], type[Any]]:
    from ray.util.metrics import Counter, Gauge

    return Counter, Gauge


def _ray_metric_name(name: str) -> str:
    return name.replace(":", "_")


class HttpPrometheusToRayBridge:
    """Poll an engine's Prometheus endpoint and emit its samples through Ray.

    Ray counters accept deltas while native Prometheus counters expose absolute
    values. Histograms are re-emitted as their cumulative bucket/sum/count
    samples so downstream PromQL retains ``histogram_quantile`` support.
    """

    def __init__(
        self,
        actor_tags: dict[str, str],
        metrics_url: str = "",
        interval_s: float = 5.0,
        fetcher: Callable[[str], Awaitable[str]] | None = None,
        counter_cls: type[Any] | None = None,
        gauge_cls: type[Any] | None = None,
    ) -> None:
        default_counter_cls, default_gauge_cls = _ray_metric_classes() if counter_cls is None or gauge_cls is None else (None, None)
        self._actor_tags = dict(actor_tags)
        self._metrics_url = metrics_url
        self._interval_s = interval_s
        self._fetcher = fetcher or self._fetch_text
        self._counter_cls = counter_cls or default_counter_cls
        self._gauge_cls = gauge_cls or default_gauge_cls
        self._counters: dict[tuple[str, tuple[str, ...]], Any] = {}
        self._gauges: dict[tuple[str, tuple[str, ...]], Any] = {}
        self._counter_values: dict[tuple[str, tuple[tuple[str, str], ...]], float] = {}

    async def run(self) -> None:
        while True:
            try:
                await asyncio.sleep(self._interval_s)
                await self.poll_once()
            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception("native engine metrics bridge poll failed")

    async def poll_once(self) -> None:
        if not self._metrics_url:
            return

        self.emit_text(await self._fetcher(self._metrics_url))

    def emit_text(self, text: str) -> None:
        for family in text_string_to_metric_families(text):
            if family.type == "counter":
                self._emit_counter(family)
            elif family.type == "gauge":
                self._emit_gauge(family)
            elif family.type == "histogram":
                self._emit_histogram(family)
            elif family.type == "unknown":
                # prometheus_client splits colon-namespaced counter families
                # into an empty `counter` family and an `unknown` `_total`
                # sample family. Native vLLM uses that namespace form.
                self._emit_unknown(family)

    async def _fetch_text(self, url: str) -> str:
        import httpx

        async with httpx.AsyncClient(timeout=3.0) as client:
            response = await client.get(url)
            response.raise_for_status()
            return response.text

    def _emit_counter(self, family: Any) -> None:
        for sample in family.samples:
            if sample.name.endswith("_created"):
                continue

            self._emit_counter_sample(family.name, sample)

    def _emit_unknown(self, family: Any) -> None:
        for sample in family.samples:
            if sample.name.endswith("_total"):
                self._emit_counter_sample(sample.name.removesuffix("_total"), sample)
            elif not sample.name.endswith("_created"):
                tags = self._tags(sample.labels)
                self._metric(self._gauges, self._gauge_cls, sample.name, tags).set(float(sample.value), tags=tags)

    def _emit_counter_sample(self, name: str, sample: Any) -> None:
        tags = self._tags(sample.labels)
        baseline_key = (sample.name, tuple(sorted(tags.items())))
        previous = self._counter_values.get(baseline_key, 0.0)
        value = float(sample.value)
        delta = value - previous
        if delta < 0:
            delta = value
        self._counter_values[baseline_key] = value

        if delta > 0:
            self._metric(self._counters, self._counter_cls, name, tags).inc(delta, tags=tags)

    def _emit_gauge(self, family: Any) -> None:
        for sample in family.samples:
            if sample.name.endswith("_created"):
                continue

            tags = self._tags(sample.labels)
            self._metric(self._gauges, self._gauge_cls, sample.name, tags).set(float(sample.value), tags=tags)

    def _emit_histogram(self, family: Any) -> None:
        for sample in family.samples:
            if sample.name.endswith("_created"):
                continue

            tags = self._tags(sample.labels)
            self._metric(self._gauges, self._gauge_cls, sample.name, tags).set(float(sample.value), tags=tags)

    def _metric(
        self,
        cache: dict[tuple[str, tuple[str, ...]], Any],
        metric_cls: type[Any],
        name: str,
        tags: dict[str, str],
    ) -> Any:
        ray_name = _ray_metric_name(name)
        key = (ray_name, tuple(sorted(tags)))
        metric = cache.get(key)
        if metric is None:
            metric = metric_cls(name=ray_name, description=name, tag_keys=key[1])
            cache[key] = metric
        return metric

    def _tags(self, source_tags: dict[str, str]) -> dict[str, str]:
        tags = dict(source_tags)
        tags.update(self._actor_tags)
        return tags
