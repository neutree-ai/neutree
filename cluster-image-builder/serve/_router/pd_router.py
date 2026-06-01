"""PD router for same-host P/D routing.

End-to-end request flow:

    client -> PDRouter (FastAPI)
                |
                | self.backend.generate.remote(
                |     payload_with_route_indices)
                |   |  ObserverRouter (Ray RequestRouter inside this process)
                |   |  - update_replicas() -> _SHARED.replace_replicas()
                |   |                          -> _SHARED.emit(replica_added)
                |   |                             -> PDRouter._on_replica_added
                |   |                                pulls get_actor_topology() now
                |   |  - on_replica_actor_died -> _SHARED.emit(replica_removed)
                |   |  - choose_replicas()    obeys ReplicaID direct target
                v
              PDCollocatedBackend (one of N replicas)
                |--> PrefillActor.generate(req)         (returns kv_transfer_params)
                '--> DecodeActor.generate(req, kv_params) (streams completion chunks)

Runtime invariants:
    V1: API -> pd_config -> orchestrator -> Ray Application end-to-end works
    V2: (strategy, placement.roles) routes to correct import_path
    V3: pd_config serialized to args reaches Python deserialization intact
    V4: STRICT_PACK PG actually colocates prefill + decode actors
    V6: vLLM kv_transfer_params is a plain dict round-trippable via Ray
    V7: Ray Serve handle dispatch latency stays sub-ms
    V10: ObserverRouter sees N PDCollocatedBackend replicas via update_replicas
    V11: replica add/remove drives ObserverRouter callbacks -> _SHARED updates
    V12: actor_topology cache keys by canonical Ray Serve ReplicaID
    V13: per-actor identity (actor_id + node_id + gpu_ids) reaches _SHARED
    V14: push-driven _SHARED callbacks (replica_added -> proactive topology pull)
         remove the request-path lazy refresh
    PDCollocatedBackend.check_health() already fan-out probes inner actors;
    any inner actor failure marks the whole replica unhealthy and Ray Serve
    recreates it. Replica recreation produces a fresh replica_id which triggers
    replica_added, so the callback path handles every steady-state transition.
    The PDRouter restart race against the router's initial update_replicas
    burst is handled by a one-shot prime during __init__.
"""
import asyncio
import bisect
import hashlib
import logging
from typing import Any, Dict, List, Optional

from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse

from ray import serve
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve.handle import DeploymentHandle

from .shared_state import (
    ActorInfo,
    ActorTopology,
    PDTopologyUnit,
    ReplicaSnapshot,
    RouteUnit,
    get_shared,
)


# Use the Ray Serve logger so log records land in the deployment-replica
# stdout / replica log file. Custom logger names ("pd_router" etc.) have
# no handler attached by default — Ray's logging config only wires
# SERVE_LOGGER_NAME ("ray.serve").
log = logging.getLogger(SERVE_LOGGER_NAME)
app = FastAPI()


@serve.deployment(ray_actor_options={"num_cpus": 0.1})
@serve.ingress(app)
class PDRouter:
    """HTTP router + replica routing strategy owner for PD same-host.

    Two-layer division of labor with ObserverRouter:
      - PDRouter (this class): picks WHICH PDCollocatedBackend domain
        each request should land on and WHICH prefill/decode unit ranks inside
        that domain handle the request. For Ray, domain is the Serve replica
        id. Phase 1 uses decode-first CHWBL and restricts prefill selection to
        the chosen decode's local domain.
      - ObserverRouter: passive — mirrors topology, obeys the direct target we
        set via `multiplexed_model_id="<ReplicaID>"`, defers everything else
        to the framework default. No decision logic.

    Topology cache is single-sourced from _SHARED callbacks:
      replica_added   -> _on_replica_added pulls topology
      replica_removed -> _SHARED already evicted; we just log
    Steady-state correctness relies on PDCollocatedBackend.check_health()
    marking the whole replica unhealthy when any inner actor fails, which
    forces Ray Serve to recreate the replica with a fresh replica_id —
    that path emits replica_removed + replica_added, which the callbacks
    here translate into a topology refresh.

    The only edge the callback path can't cover is the registration race
    when PDRouter itself restarts: Ray Serve fires update_replicas on the
    fresh router before __init__ has registered callbacks. A one-shot
    `_prime_topology` at the end of __init__ closes that window.
    """

    def __init__(self, backend: DeploymentHandle):
        # NOTE: sync __init__ is mandatory under @serve.ingress(app).
        # Ray Serve does NOT await async def __init__ in the
        # @serve.deployment + @serve.ingress combo (the FastAPI ASGI
        # wrapper path calls `cls(...)` synchronously and discards the
        # returned coroutine). Symptom is the entire body silently
        # skipped, surfacing later as `AttributeError: ... has no
        # attribute 'backend'` on first request. Plain @serve.deployment
        # and @ray.remote actors do support async __init__, which is why
        # _Backend / PrefillActor / DecodeActor can keep theirs.
        self.backend = backend
        self._shared = get_shared()
        self._virtual_nodes = 100
        self._load_factor = 1.25
        self._active_unit_loads: Dict[str, int] = {}
        # Register BEFORE prime so any incremental replica_added the router
        # fires while we're priming is also captured. Double-pull on a rid
        # that's both in the prime snapshot AND fired through the callback
        # is idempotent (upsert_topology overwrites under a lock).
        self._shared.on_replica_added(self._on_replica_added)
        self._shared.on_replica_removed(self._on_replica_removed)
        # Schedule prime on the actor's event loop. Ray Serve has the
        # asyncio loop running by the time it constructs an @serve.ingress
        # deployment (FastAPI needs it), so get_running_loop() succeeds
        # here. If it ever stops being true, fall back to lazy prime on
        # the first request.
        try:
            asyncio.get_running_loop().create_task(self._prime_topology())
            self._prime_scheduled = True
        except RuntimeError:
            self._prime_scheduled = False
            log.warning(
                "[PDRouter] no running loop at __init__; prime deferred to "
                "first request"
            )
        log.info("[PDRouter] dispatcher initialized (decode-first CHWBL + prime task)")

    async def _prime_topology(self) -> None:
        """One-shot startup pull for every replica the router already knows.

        Covers the PDRouter restart race where update_replicas fires on
        the new router before the callback registration takes effect.
        Pure no-op when serve_replicas is empty (typical first deployment).
        Idempotent — safe to retry if the scheduled task didn't fire.
        """
        rids = self._shared.known_replica_ids()
        if not rids:
            return
        log.info("[PDRouter][prime] pulling topology for %d known replicas", len(rids))
        await asyncio.gather(*[self._pull_topology_for(r) for r in rids])

    async def _ensure_primed(self) -> None:
        """Lazy fallback when __init__ couldn't schedule prime (no loop).
        Idempotent; runs at most once."""
        if self._prime_scheduled:
            return
        self._prime_scheduled = True
        await self._prime_topology()

    # ----- routing surface -----

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        await self._ensure_primed()
        payload = await request.json()
        stream = payload.get("stream", False)
        # Decode-first CHWBL picks the domain and P/D unit ranks. We express
        # the selected domain through `multiplexed_model_id="<ReplicaID>"`
        # so ObserverRouter can direct dispatch to the chosen Serve replica.
        try:
            route = self._select_pd_route(payload)
        except RuntimeError as exc:
            log.warning("[PDRouter][route_error] %s", exc)
            return JSONResponse(content={"error": str(exc)}, status_code=503)

        target_replica = route["replica_id"]
        prefill_index = int(route["prefill_rank"])
        decode_index = int(route["decode_rank"])

        snap = self._shared.snapshot()
        log.info(
            "[PDRouter][chat] stream=%s target_replica=%s "
            "prefill_rank=%d decode_rank=%d replicas_seen=%d topology_cached=%d",
            stream, target_replica,
            prefill_index, decode_index,
            len(snap.get("serve_replicas", {})),
            len(snap.get("actor_topology", {})),
        )

        handle = self.backend.options(multiplexed_model_id=target_replica)

        self._begin_request_load_tracking(route, stream=stream)
        routed_payload = dict(payload)
        # Keep the backend generate(payload) surface aligned with standard
        # apps. The route indices travel in payload, matching the K8s router
        # contract; each index identifies a selected P/D unit rank.
        routed_payload["prefill_index"] = prefill_index
        routed_payload["decode_index"] = decode_index

        if stream:
            try:
                r = handle.options(stream=True).generate.remote(
                    routed_payload,
                )
            except Exception:
                self._finish_request_load_tracking(route)
                raise
            return StreamingResponse(
                self._stream_with_load_tracking(r, route),
                media_type="text/event-stream",
            )

        try:
            result = await handle.options(stream=False).generate.remote(
                routed_payload,
            )
            if isinstance(result, dict) and "error" in result:
                log.warning(
                    "[PDRouter][chat_error] err=%s", result.get("error"),
                )
                return JSONResponse(content=result, status_code=500)
            return JSONResponse(content=result)
        finally:
            self._finish_request_load_tracking(route)

    async def _stream_with_load_tracking(self, stream, route: Dict[str, Any]):
        try:
            first_chunk = True
            async for chunk in stream:
                if first_chunk:
                    first_chunk = False
                    self._move_stream_load_to_decode(route)
                yield chunk
        finally:
            self._finish_request_load_tracking(route)

    @app.get("/v1/models")
    async def models(self):
        await self._ensure_primed()
        result = await self.backend.show_available_models.remote()
        return JSONResponse(content=result)

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}

    # ----- topology debug surface -----

    @app.get("/v1/topology")
    async def topology(self):
        await self._ensure_primed()
        """Return the current PDRouter global view of PDCollocatedBackend
        replicas + their inner actor placement. See shared_state.ActorTopology.

        Both dicts are populated by callbacks:
          serve_replicas  - ObserverRouter.update_replicas / on_replica_actor_died
          actor_topology  - _on_replica_added → _pull_topology_for upsert
        Read-only — if a replica's actor_topology is missing here, the
        callback path failed (transient pull error) and the entry will be
        rebuilt when the replica's next update_replicas / restart fires.
        """
        return JSONResponse(content=self._shared.snapshot())

    # ----- decode-first route selection -----

    def _select_pd_route(
        self,
        payload: Dict[str, Any],
    ) -> Dict[str, Any]:
        cache_key = self._extract_cache_key(payload)
        decode_units = self._get_schedulable_route_units("decode")
        if not decode_units:
            raise RuntimeError("no ready decode unit")

        decode = self._select_unit_with_chwbl(decode_units, cache_key)
        prefill_units = [
            u for u in self._get_schedulable_route_units("prefill")
            if u.domain == decode.domain
        ]
        if not prefill_units:
            raise RuntimeError(
                f"no ready prefill unit in domain {decode.domain}"
            )

        prefill = self._select_unit_with_chwbl(prefill_units, cache_key)
        return {
            "replica_id": decode.domain,
            "domain": decode.domain,
            "decode_unit_id": decode.unit_id,
            "decode_rank": decode.rank,
            "prefill_unit_id": prefill.unit_id,
            "prefill_rank": prefill.rank,
        }

    def _get_schedulable_route_units(self, role: str) -> List[RouteUnit]:
        snap = self._shared.snapshot()
        out: List[RouteUnit] = []
        for group_id, topo in (snap.get("actor_topology") or {}).items():
            topo = topo or {}
            domain = str(topo.get("group_id") or group_id)
            units = topo.get("units") or []
            if units:
                for unit in units:
                    unit = unit or {}
                    unit_role = str(unit.get("role") or "")
                    if unit_role != role:
                        continue
                    try:
                        rank = int(unit.get("rank"))
                    except (TypeError, ValueError):
                        continue
                    if rank < 0:
                        continue
                    out.append(
                        RouteUnit(domain=domain, role=unit_role, rank=rank, url="")
                    )
                continue

            # Backward compatibility for topology snapshots produced before
            # the K8s-compatible "units" field existed.
            actor_key = "decodes" if role == "decode" else "prefills"
            for rank, actor in enumerate(topo.get(actor_key) or []):
                if not (actor or {}).get("healthy", False):
                    continue
                out.append(RouteUnit(domain=domain, role=role, rank=rank, url=""))
        return out

    def _select_unit_with_chwbl(
        self,
        units: List[RouteUnit],
        cache_key: Optional[str],
    ) -> RouteUnit:
        if not units:
            raise RuntimeError("no ready route units")

        if not cache_key:
            return min(
                units,
                key=lambda u: (self._get_unit_load(u.unit_id), u.unit_id),
            )

        ring = []
        by_hash: Dict[int, RouteUnit] = {}
        for unit in units:
            for i in range(self._virtual_nodes):
                h = self._hash(f"{unit.unit_id}:{i}")
                by_hash[h] = unit
                bisect.insort(ring, h)
        if not ring:
            raise RuntimeError("no ready route units")

        start = bisect.bisect_left(ring, self._hash(cache_key))
        if start >= len(ring):
            start = 0

        loads = {u.unit_id: self._get_unit_load(u.unit_id) for u in units}
        threshold = ((sum(loads.values()) + 1) / len(units)) * self._load_factor
        checked = set()
        fallback = None
        idx = start
        while len(checked) < len(units):
            unit = by_hash[ring[idx]]
            unit_id = unit.unit_id
            if unit_id in checked:
                idx = (idx + 1) % len(ring)
                continue
            checked.add(unit_id)
            if fallback is None:
                fallback = unit
            if loads.get(unit_id, 0) + 1 <= threshold:
                return unit
            idx = (idx + 1) % len(ring)
        return fallback or units[0]

    def _get_unit_load(self, unit_id: str) -> int:
        return self._active_unit_loads.get(unit_id, 0)

    def _increment_unit_load(self, unit_id: str) -> None:
        self._active_unit_loads[unit_id] = self._active_unit_loads.get(unit_id, 0) + 1

    def _decrement_unit_load(self, unit_id: str) -> None:
        new_load = max(0, self._active_unit_loads.get(unit_id, 0) - 1)
        if new_load == 0:
            self._active_unit_loads.pop(unit_id, None)
            return
        self._active_unit_loads[unit_id] = new_load

    def _begin_request_load_tracking(
        self, route: Dict[str, Any], stream: bool
    ) -> None:
        prefill_unit_id = route["prefill_unit_id"]
        decode_unit_id = route["decode_unit_id"]
        active_units = (
            [prefill_unit_id] if stream else [prefill_unit_id, decode_unit_id]
        )
        for unit_id in active_units:
            self._increment_unit_load(unit_id)
        route["_active_load_units"] = active_units
        route["_stream_decode_started"] = False

    def _move_stream_load_to_decode(self, route: Dict[str, Any]) -> None:
        if route.get("_stream_decode_started"):
            return
        route["_stream_decode_started"] = True
        active_units = list(route.get("_active_load_units") or [])
        prefill_unit_id = route["prefill_unit_id"]
        decode_unit_id = route["decode_unit_id"]
        if prefill_unit_id in active_units:
            self._decrement_unit_load(prefill_unit_id)
            active_units.remove(prefill_unit_id)
        if decode_unit_id not in active_units:
            self._increment_unit_load(decode_unit_id)
            active_units.append(decode_unit_id)
        route["_active_load_units"] = active_units

    def _finish_request_load_tracking(self, route: Dict[str, Any]) -> None:
        for unit_id in list(route.get("_active_load_units") or []):
            self._decrement_unit_load(unit_id)
        route["_active_load_units"] = []

    def _drop_route_state_for_replica(self, replica_id: str) -> None:
        prefix = f"{replica_id}:"
        stale_unit_ids = [
            unit_id for unit_id in self._active_unit_loads
            if unit_id.startswith(prefix)
        ]
        for unit_id in stale_unit_ids:
            self._active_unit_loads.pop(unit_id, None)

    def _extract_cache_key(self, payload: Dict[str, Any]) -> Optional[str]:
        try:
            cache_components = []
            messages = (payload or {}).get("messages", [])
            user_seen = 0
            for msg in messages:
                if not isinstance(msg, dict):
                    continue
                role = msg.get("role", "")
                content = msg.get("content", "")
                if role == "system":
                    cache_components.append(f"system:{content}")
                elif role == "user" and user_seen < 2:
                    cache_components.append(f"user_{user_seen}:{content}")
                    user_seen += 1
            if cache_components:
                return "|".join(cache_components)
        except Exception as exc:  # noqa: BLE001
            log.warning("[PDRouter][cache_key_error] %s", exc)
        return None

    @staticmethod
    def _hash(key: str) -> int:
        return int(hashlib.md5(key.encode()).hexdigest()[:16], 16)

    # ----- push-driven callbacks (V14) -----

    async def _on_replica_added(self, replica_id: str, snap: ReplicaSnapshot) -> None:
        """Fired by _SHARED when ObserverRouter saw a brand-new replica.
        Pull its actor topology immediately so the view is hot before the
        first request arrives.

        replica_added fires at most once per (replica_id) by _SHARED's
        contract (a rid added twice without an intervening remove can't
        happen — replace_replicas computes set diff). The only concurrent
        firing window is "prime + callback both racing for a rid the prime
        snapshot included" — handled by upsert_topology being a locked
        last-write-wins idempotent operation.
        """
        log.info("[PDRouter][replica_added] replica=%s — pulling topology", replica_id)
        await self._pull_topology_for(replica_id)

    def _on_replica_removed(self, replica_id: str) -> None:
        """Fired by _SHARED when a replica died or fell out of the set.
        _SHARED.replace_replicas / remove_replica already evict the
        actor_topology cache; this callback drops local CHWBL load state for
        the removed replica's route units so stale units cannot affect later
        scheduling decisions.
        """
        self._drop_route_state_for_replica(replica_id)
        log.info("[PDRouter] replica_removed callback: %s", replica_id)

    # ----- internals -----

    async def _pull_topology_for(self, replica_id: str) -> None:
        """Pull get_actor_topology() from the exact replica `replica_id`.

        Uses Ray Serve's `multiplexed_model_id` metadata channel as a direct
        ReplicaID selector — see observer_router.ObserverRouter.choose_replicas.
        If `replica_id` has just died and is no longer in candidates, the
        router returns no candidates. The topology pull fails and the next
        replica update/restart callback refreshes the cache.
        """
        try:
            handle = self.backend.options(multiplexed_model_id=replica_id)
            topo_dict = await handle.get_actor_topology.remote()
            if not topo_dict:
                return
            group_id = str(
                topo_dict.get("group_id") or topo_dict.get("replica_id") or ""
            )
            reported_replica_id = str(topo_dict.get("replica_id") or group_id)
            if not group_id:
                log.warning(
                    "[PDRouter] backend topology missing group_id; "
                    "skipping upsert (older Ray Serve API?)"
                )
                return

            known_replica_ids = set(self._shared.known_replica_ids())
            if group_id in known_replica_ids:
                topology_key = group_id
            elif reported_replica_id in known_replica_ids:
                topology_key = reported_replica_id
            elif replica_id in known_replica_ids:
                topology_key = replica_id
            else:
                log.warning(
                    "[PDRouter] topology pull for stale replica skipped: "
                    "requested=%s group_id=%s reported_replica=%s known=%s",
                    replica_id, group_id, reported_replica_id,
                    sorted(known_replica_ids),
                )
                return

            def _to_info(raw: dict, default_kind: str) -> ActorInfo:
                raw = raw or {}
                return ActorInfo(
                    kind=str(raw.get("kind", default_kind)),
                    actor_id=str(raw.get("actor_id", "")),
                    node_id=str(raw.get("node_id", "")),
                    gpu_ids=[int(g) for g in (raw.get("gpu_ids") or [])],
                    healthy=bool(raw.get("healthy", False)),
                )

            prefills_raw = topo_dict.get("prefills") or []
            decodes_raw = topo_dict.get("decodes") or []
            raw_units = topo_dict.get("units") or []
            if not raw_units:
                raw_units = [
                    {"role": "prefill", "rank": rank}
                    for rank, _ in enumerate(prefills_raw)
                ] + [
                    {"role": "decode", "rank": rank}
                    for rank, _ in enumerate(decodes_raw)
                ]
            units: List[PDTopologyUnit] = []
            for raw_unit in raw_units:
                raw_unit = raw_unit or {}
                role = str(raw_unit.get("role") or "")
                if role not in {"prefill", "decode"}:
                    continue
                try:
                    rank = int(raw_unit.get("rank"))
                except (TypeError, ValueError):
                    continue
                if rank < 0:
                    continue
                units.append(PDTopologyUnit(role=role, rank=rank))
            if not units:
                log.warning(
                    "[PDRouter] backend topology for %s has no schedulable "
                    "P/D units; skipping upsert",
                    topology_key,
                )
                return

            self._shared.upsert_topology(
                topology_key,
                ActorTopology(
                    group_id=topology_key,
                    units=units,
                    replica_id=reported_replica_id,
                    replica_actor_id=str(topo_dict.get("replica_actor_id", "")),
                    replica_node=str(topo_dict.get("replica_node", "")),
                    # Ray Serve 2.53 native rank — populated by
                    # PDCollocatedBackend reading serve.get_replica_context().rank
                    global_rank=int(topo_dict.get("global_rank", -1)),
                    node_rank=int(topo_dict.get("node_rank", -1)),
                    local_rank=int(topo_dict.get("local_rank", -1)),
                    world_size=int(topo_dict.get("world_size", 0) or 0),
                    pg_id=str(topo_dict.get("pg_id", "")),
                    prefills=[_to_info(p, "prefill") for p in prefills_raw],
                    decodes=[_to_info(d, "decode") for d in decodes_raw],
                    same_host=bool(topo_dict.get("same_host", False)),
                ),
            )
            # V12 / V13 / V14 — what the push or safety-net pull actually
            # absorbed into _SHARED. Summary + per-actor identity breakdown
            # so the operator can confirm placement (actor_id + node_id +
            # gpu_ids) without hitting /v1/topology.
            log.info(
                "[PDRouter][topology_pull/summary] group_id=%s replica=%s "
                "units=%s prefills=%d decodes=%d same_host=%s global_rank=%s "
                "node_rank=%s "
                "local_rank=%s world_size=%s pg_id=%s replica_actor=%s "
                "replica_node=%s",
                topology_key, reported_replica_id,
                [(u.role, u.rank) for u in units],
                len(prefills_raw), len(decodes_raw),
                topo_dict.get("same_host"), topo_dict.get("global_rank"),
                topo_dict.get("node_rank"), topo_dict.get("local_rank"),
                topo_dict.get("world_size"), topo_dict.get("pg_id"),
                topo_dict.get("replica_actor_id"),
                topo_dict.get("replica_node"),
            )
            for i, p in enumerate(prefills_raw):
                p = p or {}
                log.info(
                    "[PDRouter][topology_pull/prefill] replica=%s rank=%d "
                    "actor_id=%s node_id=%s gpu_ids=%s",
                    topology_key, i, p.get("actor_id"), p.get("node_id"),
                    p.get("gpu_ids"),
                )
            for i, d in enumerate(decodes_raw):
                d = d or {}
                log.info(
                    "[PDRouter][topology_pull/decode] replica=%s rank=%d "
                    "actor_id=%s node_id=%s gpu_ids=%s",
                    topology_key, i, d.get("actor_id"), d.get("node_id"),
                    d.get("gpu_ids"),
                )
            if topology_key != replica_id:
                # Should only happen when `replica_id` died between the
                # callback firing and the router resolving candidates.
                log.warning(
                    "[PDRouter] direct dispatch for %s degraded to %s; "
                    "target likely just died",
                    replica_id, topology_key,
                )
        except Exception as exc:  # noqa: BLE001 — diagnostics only
            log.warning("[PDRouter] topology pull failed for %s: %s", replica_id, exc)
