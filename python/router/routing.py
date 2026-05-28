from __future__ import annotations

import bisect
import hashlib
import time
from dataclasses import dataclass, field
from typing import Any, Dict, List, Mapping, Optional, Tuple


@dataclass(frozen=True)
class PDRouteUnit:
    role_group_id: str
    role: str
    index: int
    ready: bool
    sidecar_url: str

    @property
    def stats_key(self) -> str:
        return f"{self.role_group_id}:{self.role}:{self.index}"

    @property
    def address(self) -> str:
        return f"{self.role_group_id}:{self.sidecar_url}:{self.role}:{self.index}"


@dataclass(frozen=True)
class PDRouteDecision:
    prefill: PDRouteUnit
    decode: PDRouteUnit

    @property
    def sidecar_url(self) -> str:
        return self.decode.sidecar_url

    @property
    def prefill_index(self) -> int:
        return self.prefill.index

    @property
    def decode_index(self) -> int:
        return self.decode.index


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
    pd_route_units: List[PDRouteUnit] = field(default_factory=list)


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


class RoundRobinRouter:
    def __init__(self):
        self._index_by_key: Dict[Tuple[str, ...], int] = {}

    def route(
        self,
        endpoints: List[EndpointInfo],
        _engine_stats: Mapping[str, Any],
        _request_stats: Mapping[str, RequestStats],
        _request_json: Mapping[str, Any],
    ) -> str:
        if not endpoints:
            raise ValueError("no endpoints available")
        sorted_urls = tuple(sorted(endpoint.url for endpoint in endpoints))
        index = self._index_by_key.get(sorted_urls, 0)
        chosen = sorted_urls[index % len(sorted_urls)]
        self._index_by_key[sorted_urls] = index + 1
        return chosen


class ConsistentHashRouter:
    def __init__(
        self,
        virtual_nodes_per_replica: int = 100,
        load_factor: float = 1.25,
        max_user_messages_for_cache: int = 2,
    ):
        self._virtual_nodes = virtual_nodes_per_replica
        self._load_factor = load_factor
        self._max_user_messages_for_cache = max_user_messages_for_cache

    def route(
        self,
        endpoints: List[EndpointInfo],
        _engine_stats: Mapping[str, Any],
        request_stats: Mapping[str, RequestStats],
        request_json: Mapping[str, Any],
    ) -> str:
        if not endpoints:
            raise ValueError("no endpoints available")

        urls = sorted(endpoint.url for endpoint in endpoints)
        ring = self._build_ring(urls)
        payload_hash = _hash64(self._extract_cache_key(request_json))
        index = bisect.bisect_left([point for point, _ in ring], payload_hash)
        if index >= len(ring):
            index = 0

        checked: set[str] = set()
        default_url: Optional[str] = None
        while len(checked) < len(urls):
            _, url = ring[index]
            index = (index + 1) % len(ring)
            if url in checked:
                continue
            checked.add(url)
            if default_url is None:
                default_url = url
            if self._check_load(urls, url, request_stats):
                return url

        return default_url or urls[0]

    def _build_ring(self, urls: List[str]) -> List[Tuple[int, str]]:
        ring: List[Tuple[int, str]] = []
        for url in urls:
            for virtual_node in range(self._virtual_nodes):
                ring.append((_hash64(f"{url}:{virtual_node}"), url))
        ring.sort(key=lambda item: item[0])
        return ring

    def _check_load(
        self,
        urls: List[str],
        candidate_url: str,
        request_stats: Mapping[str, RequestStats],
    ) -> bool:
        if not urls:
            return False
        loads = {
            url: request_stats.get(url, RequestStats()).active_requests
            for url in urls
        }
        total_load = sum(loads.values())
        threshold = ((total_load + 1) / len(urls)) * self._load_factor
        return loads[candidate_url] + 1 <= threshold

    def _extract_cache_key(self, request_json: Mapping[str, Any]) -> str:
        return extract_cache_key(request_json, self._max_user_messages_for_cache)


class PDSameHostRouter:
    def __init__(
        self,
        virtual_nodes_per_replica: int = 100,
        load_factor: float = 1.25,
        max_user_messages_for_cache: int = 2,
    ):
        self._virtual_nodes = virtual_nodes_per_replica
        self._load_factor = load_factor
        self._max_user_messages_for_cache = max_user_messages_for_cache

    def route(
        self,
        endpoints: List[EndpointInfo],
        _engine_stats: Mapping[str, Any],
        request_stats: Mapping[str, RequestStats],
        request_json: Mapping[str, Any],
    ) -> PDRouteDecision:
        units = [
            unit
            for endpoint in endpoints
            for unit in endpoint.pd_route_units
            if unit.ready
        ]
        decode = self._select_unit(
            [unit for unit in units if unit.role == "decode"],
            request_stats,
            request_json,
            "decode",
        )
        local_prefill_candidates = [
            unit
            for unit in units
            if unit.role == "prefill" and unit.role_group_id == decode.role_group_id
        ]
        if not local_prefill_candidates:
            raise ValueError(f"no ready prefill in role group {decode.role_group_id}")
        prefill = self._select_unit(local_prefill_candidates, request_stats, request_json, "prefill")
        return PDRouteDecision(prefill=prefill, decode=decode)

    def _select_unit(
        self,
        units: List[PDRouteUnit],
        request_stats: Mapping[str, RequestStats],
        request_json: Mapping[str, Any],
        role: str,
    ) -> PDRouteUnit:
        if not units:
            raise ValueError(f"no ready {role} units available")
        sorted_units = sorted(units, key=lambda item: item.stats_key)
        cache_key = maybe_extract_cache_key(request_json, self._max_user_messages_for_cache)
        if cache_key is None:
            return self._select_least_loaded(sorted_units, request_stats)

        ring = self._build_ring(sorted_units)
        payload_hash = _hash64(cache_key)
        index = bisect.bisect_left([point for point, _ in ring], payload_hash)
        if index >= len(ring):
            index = 0

        checked: set[str] = set()
        default_unit: Optional[PDRouteUnit] = None
        while len(checked) < len(sorted_units):
            _, unit = ring[index]
            index = (index + 1) % len(ring)
            if unit.stats_key in checked:
                continue
            checked.add(unit.stats_key)
            if default_unit is None:
                default_unit = unit
            if self._check_load(sorted_units, unit, request_stats):
                return unit
        return default_unit or sorted_units[0]

    def _build_ring(self, units: List[PDRouteUnit]) -> List[Tuple[int, PDRouteUnit]]:
        ring: List[Tuple[int, PDRouteUnit]] = []
        for unit in units:
            for virtual_node in range(self._virtual_nodes):
                ring.append((_hash64(f"{unit.stats_key}:{virtual_node}"), unit))
        ring.sort(key=lambda item: item[0])
        return ring

    def _check_load(
        self,
        units: List[PDRouteUnit],
        candidate: PDRouteUnit,
        request_stats: Mapping[str, RequestStats],
    ) -> bool:
        loads = {
            unit.stats_key: request_stats.get(unit.stats_key, RequestStats()).active_requests
            for unit in units
        }
        total_load = sum(loads.values())
        threshold = ((total_load + 1) / len(units)) * self._load_factor
        return loads[candidate.stats_key] + 1 <= threshold

    def _select_least_loaded(
        self,
        units: List[PDRouteUnit],
        request_stats: Mapping[str, RequestStats],
    ) -> PDRouteUnit:
        return min(
            units,
            key=lambda unit: (
                request_stats.get(unit.stats_key, RequestStats()).active_requests,
                unit.stats_key,
            ),
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
