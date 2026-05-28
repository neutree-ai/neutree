from __future__ import annotations

from typing import Any, Dict, List, Mapping, Optional, Tuple

from router.scheduling.plugins import (
    ConsistentHashWithBoundedLoadScorer,
    EndpointFilterPlugin,
    EndpointScorePicker,
    MaxScoreEndpointPicker,
    WeightedEndpointScorer,
)
from router.scheduling.types import EndpointInfo, RequestStats, SchedulingContext


class SchedulingProfile:
    def __init__(
        self,
        filters: List[EndpointFilterPlugin],
        scorers: List[WeightedEndpointScorer],
        picker: Optional[EndpointScorePicker] = None,
        name: str = "default",
    ):
        self.name = name
        self._filters = filters
        self._scorers = scorers
        self._picker = picker or MaxScoreEndpointPicker()

    def pick(
        self,
        endpoints: List[EndpointInfo],
        context: SchedulingContext,
    ) -> EndpointInfo:
        candidates = [
            endpoint for endpoint in endpoints
            if all(filter_plugin.filter(endpoint, context) for filter_plugin in self._filters)
        ]
        if not candidates:
            raise ValueError("no endpoints available")
        total_scores = {endpoint.route_key: 0.0 for endpoint in candidates}
        for weighted_scorer in self._scorers:
            plugin_scores = weighted_scorer.scorer.score(candidates, context)
            for endpoint in candidates:
                score = _clamp_score(plugin_scores.get(endpoint.route_key, 0.0))
                total_scores[endpoint.route_key] += score * weighted_scorer.weight
        return self._picker.pick(candidates, total_scores)

    def schedule(
        self,
        endpoints: List[EndpointInfo],
        context: SchedulingContext,
    ) -> EndpointInfo:
        return self.pick(endpoints, context)


class RoundRobinEndpointPicker:
    def __init__(self):
        self._index_by_key: Dict[Tuple[str, ...], int] = {}

    def pick(
        self,
        endpoints: List[EndpointInfo],
        _context: SchedulingContext,
    ) -> EndpointInfo:
        if not endpoints:
            raise ValueError("no endpoints available")
        sorted_endpoints = sorted(endpoints, key=lambda endpoint: endpoint.route_key)
        route_keys = tuple(endpoint.route_key for endpoint in sorted_endpoints)
        index = self._index_by_key.get(route_keys, 0)
        chosen = sorted_endpoints[index % len(sorted_endpoints)]
        self._index_by_key[route_keys] = index + 1
        return chosen


class WeightedScoringEndpointPicker:
    def __init__(
        self,
        scorers: List[WeightedEndpointScorer],
        picker: Optional[EndpointScorePicker] = None,
        filters: Optional[List[EndpointFilterPlugin]] = None,
    ):
        self._profile = SchedulingProfile(filters or [], scorers, picker)

    def pick(
        self,
        endpoints: List[EndpointInfo],
        context: SchedulingContext,
    ) -> EndpointInfo:
        return self._profile.pick(endpoints, context)


class ConsistentHashEndpointPicker(WeightedScoringEndpointPicker):
    def __init__(
        self,
        virtual_nodes_per_replica: int = 100,
        load_factor: float = 1.25,
        max_user_messages_for_cache: int = 2,
    ):
        super().__init__(
            [
                WeightedEndpointScorer(
                    ConsistentHashWithBoundedLoadScorer(
                        virtual_nodes_per_replica,
                        load_factor,
                        max_user_messages_for_cache,
                    )
                ),
            ]
        )


def _clamp_score(score: float) -> float:
    if score < 0:
        return 0.0
    if score > 1:
        return 1.0
    return score


def context_from_legacy_route_args(
    engine_stats: Mapping[str, Any],
    request_stats: Mapping[str, RequestStats],
    request_json: Mapping[str, Any],
) -> SchedulingContext:
    return SchedulingContext(engine_stats, request_stats, request_json)
