from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any, Dict, Mapping

from router.metrics import MetricsRegistry
from router.routing import (
    ConsistentHashRouter,
    EndpointInfo,
    PDSameHostRouter,
    RequestStatsMonitor,
    RoundRobinRouter,
)

LOG = logging.getLogger("router")


@dataclass(frozen=True)
class BackendSelection:
    url: str
    extra_headers: Dict[str, str] = field(default_factory=dict)
    stats_keys: tuple[str, ...] = ()


class RouterRuntime:
    def __init__(self, service_discovery: Any, request_stats_window: int = 60):
        self.service_discovery = service_discovery
        self.request_stats = RequestStatsMonitor(request_stats_window)
        self.metrics = MetricsRegistry()
        self.round_robin = RoundRobinRouter()
        self.consistent_hash = ConsistentHashRouter()
        self.pd_same_host = PDSameHostRouter()

    def select_backend(
        self,
        endpoints: list[EndpointInfo],
        request_json: Mapping[str, Any],
    ) -> BackendSelection:
        pd_endpoints = [endpoint for endpoint in endpoints if endpoint.is_pd_collocated]
        if pd_endpoints:
            decision = self.pd_same_host.route(
                pd_endpoints,
                {},
                self.request_stats.snapshot(),
                request_json,
            )
            return BackendSelection(
                url=decision.url,
                extra_headers={
                    "x-neutree-prefill-index": str(decision.prefill_index),
                    "x-neutree-decode-index": str(decision.decode_index),
                },
                stats_keys=decision.stats_keys,
            )

        routing_logic = endpoints[0].routing_logic or "roundrobin"
        stats = self.request_stats.snapshot()
        if routing_logic == "consistent_hash":
            endpoint = self.consistent_hash.route(endpoints, {}, stats, request_json)
            return BackendSelection(url=endpoint.url, stats_keys=(endpoint.stats_key,))
        if routing_logic != "roundrobin":
            LOG.warning("unsupported routing_logic=%s; falling back to roundrobin", routing_logic)
        endpoint = self.round_robin.route(endpoints, {}, stats, request_json)
        return BackendSelection(url=endpoint.url, stats_keys=(endpoint.stats_key,))
