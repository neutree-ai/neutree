from __future__ import annotations

from typing import Any, List, Mapping

from router.scheduling import (
    ConsistentHashEndpointPicker,
    ConsistentHashWithBoundedLoadScorer,
    DomainAffinityFilter,
    EndpointFilterPlugin,
    EndpointInfo,
    EndpointRouteDecision,
    EndpointScorePicker,
    EndpointScorePlugin,
    MaxScoreEndpointPicker,
    PDRoleFilter,
    PDSameHostProfileHandler,
    PDSameRoleGroupFilter,
    ReadyEndpointFilter,
    RequestStats,
    RequestStatsMonitor,
    RouteDecision,
    RoundRobinEndpointPicker,
    SchedulingContext,
    SchedulingProfile,
    WeightedEndpointScorer,
    WeightedScoringEndpointPicker,
    context_from_legacy_route_args,
    extract_cache_key,
    maybe_extract_cache_key,
)


class _LegacyRouteMixin:
    def route(
        self,
        endpoints: List[EndpointInfo],
        engine_stats: Mapping[str, Any],
        request_stats: Mapping[str, RequestStats],
        request_json: Mapping[str, Any],
    ):
        return self.pick(
            endpoints,
            context_from_legacy_route_args(engine_stats, request_stats, request_json),
        )


class RoundRobinRouter(_LegacyRouteMixin, RoundRobinEndpointPicker):
    pass


class WeightedScoringRouter(_LegacyRouteMixin, WeightedScoringEndpointPicker):
    pass


class ConsistentHashRouter(_LegacyRouteMixin, ConsistentHashEndpointPicker):
    pass


class PDSameHostRouter(_LegacyRouteMixin, PDSameHostProfileHandler):
    pass


__all__ = [
    "ConsistentHashEndpointPicker",
    "ConsistentHashRouter",
    "ConsistentHashWithBoundedLoadScorer",
    "DomainAffinityFilter",
    "EndpointFilterPlugin",
    "EndpointInfo",
    "EndpointRouteDecision",
    "EndpointScorePicker",
    "EndpointScorePlugin",
    "MaxScoreEndpointPicker",
    "PDRoleFilter",
    "PDSameHostProfileHandler",
    "PDSameHostRouter",
    "PDSameRoleGroupFilter",
    "ReadyEndpointFilter",
    "RequestStats",
    "RequestStatsMonitor",
    "RouteDecision",
    "RoundRobinEndpointPicker",
    "RoundRobinRouter",
    "SchedulingContext",
    "SchedulingProfile",
    "WeightedEndpointScorer",
    "WeightedScoringEndpointPicker",
    "WeightedScoringRouter",
    "extract_cache_key",
    "maybe_extract_cache_key",
]
