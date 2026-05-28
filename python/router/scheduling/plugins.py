from __future__ import annotations

import bisect
import hashlib
from dataclasses import dataclass
from typing import Any, Dict, List, Mapping, Optional, Protocol, Tuple

from router.scheduling.types import EndpointInfo, RequestStats, SchedulingContext


class EndpointFilterPlugin(Protocol):
    name: str

    def filter(self, endpoint: EndpointInfo, context: SchedulingContext) -> bool:
        ...


class EndpointScorePlugin(Protocol):
    name: str

    def score(
        self,
        endpoints: List[EndpointInfo],
        context: SchedulingContext,
    ) -> Dict[str, float]:
        ...


class EndpointScorePicker(Protocol):
    name: str

    def pick(
        self,
        endpoints: List[EndpointInfo],
        scores: Mapping[str, float],
    ) -> EndpointInfo:
        ...


@dataclass(frozen=True)
class WeightedEndpointScorer:
    scorer: EndpointScorePlugin
    weight: float = 1.0


class ReadyEndpointFilter:
    name = "ready-endpoint"

    def filter(self, endpoint: EndpointInfo, _context: SchedulingContext) -> bool:
        return not endpoint.sleep


class PDRoleFilter:
    def __init__(self, role: str):
        self._role = role
        self.name = f"pd-role-{role}"

    def filter(self, endpoint: EndpointInfo, _context: SchedulingContext) -> bool:
        return endpoint.pd_role == self._role


class DomainAffinityFilter:
    name = "domain-affinity"

    def filter(self, endpoint: EndpointInfo, context: SchedulingContext) -> bool:
        if context.selected_endpoint is None:
            return False
        if context.selected_endpoint.scheduling_domain is None:
            return False
        return endpoint.scheduling_domain == context.selected_endpoint.scheduling_domain


class PDSameRoleGroupFilter(DomainAffinityFilter):
    name = "pd-same-role-group"


class ConsistentHashWithBoundedLoadScorer:
    def __init__(
        self,
        virtual_nodes_per_replica: int = 100,
        load_factor: float = 1.25,
        max_user_messages_for_cache: int = 2,
    ):
        self._virtual_nodes = virtual_nodes_per_replica
        self._load_factor = load_factor
        self._max_user_messages_for_cache = max_user_messages_for_cache
        self.name = "consistent-hash-with-bounded-load"

    def score(
        self,
        endpoints: List[EndpointInfo],
        context: SchedulingContext,
    ) -> Dict[str, float]:
        if not endpoints:
            return {}
        cache_key = maybe_extract_cache_key(context.request_json, self._max_user_messages_for_cache)
        if cache_key is None:
            selected = min(endpoints, key=lambda endpoint: (self._load(endpoint, context), endpoint.route_key))
            return self._single_winner_scores(endpoints, selected)

        ring = self._build_ring(endpoints)
        payload_hash = _hash64(cache_key)
        index = bisect.bisect_left([point for point, _ in ring], payload_hash)
        if index >= len(ring):
            index = 0

        checked: set[str] = set()
        fallback: Optional[EndpointInfo] = None
        threshold = self._bounded_load_threshold(endpoints, context)
        while len(checked) < len(endpoints):
            _, endpoint = ring[index]
            index = (index + 1) % len(ring)
            route_key = endpoint.route_key
            if route_key in checked:
                continue
            checked.add(route_key)
            if fallback is None:
                fallback = endpoint
            if self._load(endpoint, context) + 1 <= threshold:
                return self._single_winner_scores(endpoints, endpoint)
        return self._single_winner_scores(endpoints, fallback or endpoints[0])

    def _build_ring(self, endpoints: List[EndpointInfo]) -> List[Tuple[int, EndpointInfo]]:
        ring: List[Tuple[int, EndpointInfo]] = []
        for endpoint in sorted(endpoints, key=lambda item: item.route_key):
            for virtual_node in range(self._virtual_nodes):
                ring.append((_hash64(f"{endpoint.route_key}:{virtual_node}"), endpoint))
        ring.sort(key=lambda item: item[0])
        return ring

    def _bounded_load_threshold(
        self,
        endpoints: List[EndpointInfo],
        context: SchedulingContext,
    ) -> float:
        total_load = sum(self._load(endpoint, context) for endpoint in endpoints)
        return ((total_load + 1) / len(endpoints)) * self._load_factor

    def _load(self, endpoint: EndpointInfo, context: SchedulingContext) -> int:
        return context.request_stats.get(endpoint.stats_key, RequestStats()).active_requests

    def _single_winner_scores(
        self,
        endpoints: List[EndpointInfo],
        selected: EndpointInfo,
    ) -> Dict[str, float]:
        return {
            endpoint.route_key: 1.0 if endpoint.route_key == selected.route_key else 0.0
            for endpoint in endpoints
        }


class MaxScoreEndpointPicker:
    name = "max-score"

    def pick(
        self,
        endpoints: List[EndpointInfo],
        scores: Mapping[str, float],
    ) -> EndpointInfo:
        return max(
            sorted(endpoints, key=lambda endpoint: endpoint.route_key),
            key=lambda endpoint: scores.get(endpoint.route_key, 0.0),
        )


def extract_cache_key(request_json: Mapping[str, Any], max_user_messages: int = 2) -> str:
    cache_key = maybe_extract_cache_key(request_json, max_user_messages)
    if cache_key is not None:
        return cache_key
    return repr(sorted(request_json.items()))


def maybe_extract_cache_key(request_json: Mapping[str, Any], max_user_messages: int = 2) -> Optional[str]:
    if "messages" in request_json:
        parts: List[str] = []
        user_messages = 0
        for message in request_json.get("messages", []):
            if not isinstance(message, Mapping):
                continue
            role = str(message.get("role", ""))
            content = str(message.get("content", ""))
            if role == "system":
                parts.append(f"system:{content}")
            elif role == "user" and user_messages < max_user_messages:
                parts.append(f"user:{content}")
                user_messages += 1
        if parts:
            return "|".join(parts)
    if request_json.get("prompt"):
        return f"prompt:{request_json.get('prompt', '')}"
    return None


def _hash64(value: str) -> int:
    return int(hashlib.md5(value.encode("utf-8")).hexdigest()[:16], 16)
