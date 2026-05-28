from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Any, Dict, List, Mapping, Optional, Tuple


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


RouteDecision = EndpointRouteDecision


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
