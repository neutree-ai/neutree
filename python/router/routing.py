from __future__ import annotations

import bisect
import hashlib
import time
from dataclasses import dataclass, field
from typing import Any, Dict, List, Mapping, Optional, Protocol, Tuple


@dataclass(frozen=True)
class EndpointRouteDecision:
    endpoint: "EndpointInfo"
    prefill: Optional["EndpointInfo"] = None
    decode: Optional["EndpointInfo"] = None

    @property
    def url(self) -> str:
        return self.endpoint.dispatch_url or self.endpoint.url

    @property
    def sidecar_url(self) -> str:
        return self.url

    @property
    def prefill_index(self) -> Optional[int]:
        if self.prefill is None:
            return None
        return self.prefill.pd_index

    @property
    def decode_index(self) -> Optional[int]:
        if self.decode is None:
            return None
        return self.decode.pd_index

    @property
    def stats_keys(self) -> Tuple[str, ...]:
        keys = [self.url]
        if self.prefill is not None:
            keys.append(self.prefill.stats_key)
        if self.decode is not None:
            keys.append(self.decode.stats_key)
        return tuple(keys)


@dataclass(frozen=True)
class EndpointInfo:
    url: str
    model_names: List[str]
    id: str = ""
    workspace: Optional[str] = None
    endpoint: Optional[str] = None
    routing_logic: Optional[str] = None
    pod_name: Optional[str] = None
    sleep: bool = False
    is_pd_collocated: bool = False
    dispatch_url: Optional[str] = None
    pd_role_group_id: Optional[str] = None
    pd_role: Optional[str] = None
    pd_index: Optional[int] = None

    @property
    def route_key(self) -> str:
        if self.is_pd_collocated and self.pd_role_group_id and self.pd_role is not None and self.pd_index is not None:
            return f"{self.pd_role_group_id}:{self.pd_role}:{self.pd_index}"
        return self.id or self.pod_name or self.url

    @property
    def stats_key(self) -> str:
        if self.is_pd_collocated:
            return self.route_key
        return self.url


@dataclass
class RequestStats:
    active_requests: int = 0
    qps: float = 0.0
    avg_latency: float = 0.0
    avg_decoding_length: float = 0.0
    avg_itl: float = 0.0
    num_swapped_requests: int = 0
    _starts: Dict[str, float] = field(default_factory=dict, repr=False)
    _completed: List[Tuple[float, float]] = field(default_factory=list, repr=False)


class RequestStatsMonitor:
    def __init__(self, window_seconds: int = 60):
        self._window_seconds = window_seconds
        self._stats: Dict[str, RequestStats] = {}

    def on_request_start(self, backend_url: str, request_id: str, now: Optional[float] = None) -> None:
        now = time.time() if now is None else now
        stats = self._stats.setdefault(backend_url, RequestStats())
        stats.active_requests += 1
        stats._starts[request_id] = now

    def on_request_complete(self, backend_url: str, request_id: str, now: Optional[float] = None) -> None:
        now = time.time() if now is None else now
        stats = self._stats.setdefault(backend_url, RequestStats())
        start = stats._starts.pop(request_id, now)
        stats.active_requests = max(0, stats.active_requests - 1)
        stats._completed.append((now, max(0.0, now - start)))
        self._prune(stats, now)

    def snapshot(self, now: Optional[float] = None) -> Dict[str, RequestStats]:
        now = time.time() if now is None else now
        result: Dict[str, RequestStats] = {}
        for url, stats in self._stats.items():
            self._prune(stats, now)
            completed = stats._completed
            qps = len(completed) / self._window_seconds
            avg_latency = sum(latency for _, latency in completed) / len(completed) if completed else 0.0
            result[url] = RequestStats(
                active_requests=stats.active_requests,
                qps=qps,
                avg_latency=avg_latency,
                avg_decoding_length=stats.avg_decoding_length,
                avg_itl=stats.avg_itl,
                num_swapped_requests=stats.num_swapped_requests,
            )
        return result

    def _prune(self, stats: RequestStats, now: float) -> None:
        cutoff = now - self._window_seconds
        stats._completed = [(ts, latency) for ts, latency in stats._completed if ts >= cutoff]


@dataclass(frozen=True)
class SchedulingContext:
    engine_stats: Mapping[str, Any]
    request_stats: Mapping[str, RequestStats]
    request_json: Mapping[str, Any]
    selected_endpoint: Optional[EndpointInfo] = None

    def with_selected_endpoint(self, endpoint: EndpointInfo) -> "SchedulingContext":
        return SchedulingContext(
            engine_stats=self.engine_stats,
            request_stats=self.request_stats,
            request_json=self.request_json,
            selected_endpoint=endpoint,
        )


class RoundRobinRouter:
    def __init__(self):
        self._index_by_key: Dict[Tuple[str, ...], int] = {}

    def route(
        self,
        endpoints: List[EndpointInfo],
        _engine_stats: Mapping[str, Any],
        _request_stats: Mapping[str, RequestStats],
        _request_json: Mapping[str, Any],
    ) -> EndpointInfo:
        if not endpoints:
            raise ValueError("no endpoints available")
        sorted_endpoints = sorted(endpoints, key=lambda endpoint: endpoint.route_key)
        route_keys = tuple(endpoint.route_key for endpoint in sorted_endpoints)
        index = self._index_by_key.get(route_keys, 0)
        chosen = sorted_endpoints[index % len(sorted_endpoints)]
        self._index_by_key[route_keys] = index + 1
        return chosen


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


class PDSameRoleGroupFilter:
    name = "pd-same-role-group"

    def filter(self, endpoint: EndpointInfo, context: SchedulingContext) -> bool:
        if context.selected_endpoint is None:
            return False
        return endpoint.pd_role_group_id == context.selected_endpoint.pd_role_group_id


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
    def pick(
        self,
        endpoints: List[EndpointInfo],
        scores: Mapping[str, float],
    ) -> EndpointInfo:
        return max(
            sorted(endpoints, key=lambda endpoint: endpoint.route_key),
            key=lambda endpoint: scores.get(endpoint.route_key, 0.0),
        )


class WeightedScoringRouter:
    def __init__(
        self,
        scorers: List[WeightedEndpointScorer],
        picker: Optional[MaxScoreEndpointPicker] = None,
        filters: Optional[List[EndpointFilterPlugin]] = None,
    ):
        self._profile = SchedulingProfile(filters or [], scorers, picker)

    def route(
        self,
        endpoints: List[EndpointInfo],
        engine_stats: Mapping[str, Any],
        request_stats: Mapping[str, RequestStats],
        request_json: Mapping[str, Any],
    ) -> EndpointInfo:
        return self._profile.schedule(
            endpoints,
            SchedulingContext(engine_stats, request_stats, request_json),
        )


class SchedulingProfile:
    def __init__(
        self,
        filters: List[EndpointFilterPlugin],
        scorers: List[WeightedEndpointScorer],
        picker: Optional[MaxScoreEndpointPicker] = None,
    ):
        self._filters = filters
        self._scorers = scorers
        self._picker = picker or MaxScoreEndpointPicker()

    def schedule(
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


class ConsistentHashRouter(WeightedScoringRouter):
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


class PDSameHostRouter:
    def __init__(
        self,
        virtual_nodes_per_replica: int = 100,
        load_factor: float = 1.25,
        max_user_messages_for_cache: int = 2,
    ):
        scorers = [
            WeightedEndpointScorer(
                ConsistentHashWithBoundedLoadScorer(
                    virtual_nodes_per_replica,
                    load_factor,
                    max_user_messages_for_cache,
                )
            ),
        ]
        self._decode_profile = SchedulingProfile(
            [ReadyEndpointFilter(), PDRoleFilter("decode")],
            scorers,
        )
        self._prefill_profile = SchedulingProfile(
            [ReadyEndpointFilter(), PDRoleFilter("prefill"), PDSameRoleGroupFilter()],
            scorers,
        )

    def route(
        self,
        endpoints: List[EndpointInfo],
        _engine_stats: Mapping[str, Any],
        request_stats: Mapping[str, RequestStats],
        request_json: Mapping[str, Any],
    ) -> EndpointRouteDecision:
        context = SchedulingContext(_engine_stats, request_stats, request_json)
        try:
            decode = self._decode_profile.schedule(endpoints, context)
        except ValueError as exc:
            raise ValueError("no ready decode endpoints available") from exc

        try:
            prefill = self._prefill_profile.schedule(endpoints, context.with_selected_endpoint(decode))
        except ValueError as exc:
            raise ValueError(
                f"no ready prefill endpoint in role group {decode.pd_role_group_id}"
            ) from exc
        return EndpointRouteDecision(endpoint=decode, prefill=prefill, decode=decode)


def _clamp_score(score: float) -> float:
    if score < 0:
        return 0.0
    if score > 1:
        return 1.0
    return score


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
