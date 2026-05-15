"""Naive PD ingress for Phase 0 Demo.

End-to-end request flow:

    client -> PDIngress (FastAPI)
                |
                | self.backend.pd_chat.remote(payload)
                |   |  ObserverRouter (Ray RequestRouter inside this process)
                |   |  - update_replicas() -> _SHARED.replace_replicas()
                |   |                          -> _SHARED.emit(replica_added)
                |   |                             -> PDIngress._on_replica_added
                |   |                                pulls get_actor_topology() now
                |   |  - on_replica_actor_died -> _SHARED.emit(replica_removed)
                |   |  - choose_replicas()    reads _SHARED, round-robins
                v
              PDCollocatedBackend (one of N replicas)
                |--> PrefillActor.prefill_at(req)        (returns kv_transfer_params)
                '--> DecodeActor.decode_at(req, kv_params) (streams completion chunks)

Demo invariants the architecture review needs to validate:
    V1: API -> IR -> orchestrator -> Ray Application end-to-end works
    V2: (strategy, placement.roles) routes to correct import_path
    V3: plan serialized to args reaches Python deserialization intact
    V4: STRICT_PACK PG actually colocates prefill + decode actors
    V6: vLLM kv_transfer_params is a plain dict round-trippable via Ray
    V7: Ray Serve handle dispatch latency stays sub-ms
    V10: ObserverRouter sees N PDCollocatedBackend replicas via update_replicas
    V11: replica add/remove drives ObserverRouter callbacks -> _SHARED updates
    V12: actor_topology cache keys by canonical Ray Serve ReplicaID
    V13: per-actor identity (actor_id + node_id + gpu_ids) reaches _SHARED
    V14: push-driven _SHARED callbacks (replica_added -> proactive topology pull)
         remove the request-path lazy refresh
    MVP: drop the TTL list refresh entirely. PDCollocatedBackend.check_health()
         already fan-out probes inner actors; any inner actor failure marks
         the whole replica unhealthy and Ray Serve recreates it. Replica
         recreation produces a fresh replica_id which triggers replica_added,
         so the callback path single-sourcedly handles every steady-state
         transition. Only edge left is "PDIngress restart racing the router's
         initial update_replicas burst" — handled by a one-shot prime at
         async __init__.
"""
import asyncio
import itertools
import logging
from typing import Optional

from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse

from ray import serve
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve.handle import DeploymentHandle

from .observer_router import REPLICA_DISPATCH_PREFIX
from .shared_state import (
    ActorInfo,
    ActorTopology,
    ReplicaSnapshot,
    get_shared,
)


# Use the Ray Serve logger so log records land in the deployment-replica
# stdout / replica log file. Custom logger names ("pd_ingress" etc.) have
# no handler attached by default — Ray's logging config only wires
# SERVE_LOGGER_NAME ("ray.serve").
log = logging.getLogger(SERVE_LOGGER_NAME)
app = FastAPI()


@serve.deployment(ray_actor_options={"num_cpus": 0.1})
@serve.ingress(app)
class PDIngress:
    """HTTP ingress + replica routing strategy owner for PD same-host.

    Two-layer division of labor with ObserverRouter:
      - PDIngress (this class): picks WHICH PDCollocatedBackend replica
        each request should land on. Demo uses deterministic round-robin
        over _SHARED.known_replica_ids; MVP plugs in CHWBL on session_id /
        prefix-cache awareness here without touching the router.
      - ObserverRouter: passive — mirrors topology, obeys the pin we set
        via `multiplexed_model_id="replica:<rid>"`, defers everything
        else to the framework default. No decision logic.

    Topology cache is single-sourced from _SHARED callbacks:
      replica_added   -> _on_replica_added pulls topology
      replica_removed -> _SHARED already evicted; we just log
    Steady-state correctness relies on PDCollocatedBackend.check_health()
    marking the whole replica unhealthy when any inner actor fails, which
    forces Ray Serve to recreate the replica with a fresh replica_id —
    that path emits replica_removed + replica_added, which the callbacks
    here translate into a topology refresh.

    The only edge the callback path can't cover is the registration race
    when PDIngress itself restarts: Ray Serve fires update_replicas on the
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
        # Demo routing strategy: deterministic RR over known replicas.
        # MVP replaces with a pluggable strategy abstraction (see
        # `_pick_replica`).
        self._rr_cursor = itertools.count()
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
                "[PDIngress] no running loop at __init__; prime deferred to "
                "first request"
            )
        log.info("[PDIngress] dispatcher initialized (callbacks + prime task)")

    async def _prime_topology(self) -> None:
        """One-shot startup pull for every replica the router already knows.

        Covers the PDIngress restart race where update_replicas fires on
        the new router before the callback registration takes effect.
        Pure no-op when serve_replicas is empty (typical first deployment).
        Idempotent — safe to retry if the scheduled task didn't fire.
        """
        rids = self._shared.known_replica_ids()
        if not rids:
            return
        log.info("[PDIngress][prime] pulling topology for %d known replicas", len(rids))
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
        # Replica selection precedence:
        #   1. caller pin (X-Neutree-Replica-ID header / ?replica= query) — V15
        #   2. ingress strategy (Demo RR; MVP CHWBL / prefix-aware)
        # Either way we express the choice through
        # `multiplexed_model_id="replica:<rid>"`. ObserverRouter obeys
        # the pin verbatim; if the chosen rid raced replica death, the
        # router falls back to framework default (pow2 etc).
        pin_source = "caller"
        target_replica = (
            request.headers.get("X-Neutree-Replica-ID")
            or request.query_params.get("replica")
        )
        if not target_replica:
            target_replica = self._pick_replica()
            pin_source = "strategy"
        snap = self._shared.snapshot()
        log.info(
            "[PDIngress][chat] stream=%s pin_replica=%s pin_source=%s "
            "replicas_seen=%d topology_cached=%d",
            stream, target_replica, pin_source,
            len(snap.get("serve_replicas", {})),
            len(snap.get("actor_topology", {})),
        )

        handle = self.backend
        if target_replica:
            handle = handle.options(
                multiplexed_model_id=f"{REPLICA_DISPATCH_PREFIX}{target_replica}"
            )

        if stream:
            r = handle.options(stream=True).pd_chat.remote(payload)
            return StreamingResponse(r, media_type="text/event-stream")
        result = await handle.options(stream=False).pd_chat.remote(payload)
        if isinstance(result, dict) and "error" in result:
            log.warning(
                "[PDIngress][chat_error] err=%s", result.get("error"),
            )
            return JSONResponse(content=result, status_code=500)
        return JSONResponse(content=result)

    @app.get("/v1/models")
    async def models(self):
        await self._ensure_primed()
        result = await self.backend.show_available_models.remote()
        return JSONResponse(content=result)

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}

    # ----- Demo V10/V11/V14 debug surface -----

    @app.get("/v1/topology")
    async def topology(self):
        await self._ensure_primed()
        """Return the current PDIngress global view of PDCollocatedBackend
        replicas + their inner actor placement. See shared_state.ActorTopology.

        Both dicts are populated by callbacks:
          serve_replicas  - ObserverRouter.update_replicas / on_replica_actor_died
          actor_topology  - _on_replica_added → _pull_topology_for upsert
        Read-only — if a replica's actor_topology is missing here, the
        callback path failed (transient pull error) and the entry will be
        rebuilt when the replica's next update_replicas / restart fires.
        """
        return JSONResponse(content=self._shared.snapshot())

    # ----- replica selection strategy -----

    def _pick_replica(self) -> Optional[str]:
        """Replica routing strategy. Demo: deterministic round-robin over
        the _SHARED.known_replica_ids view. Returns None when nothing is
        known yet (cold-start window) — caller leaves the handle unpinned
        and lets the framework default pick.

        MVP slot: this method becomes a thin dispatcher over a pluggable
        strategy interface supporting weighted multi-strategy scoring
        (CHWBL on session_id + prefix-cache hit-rate + load, etc.).
        """
        rids = self._shared.known_replica_ids()
        if not rids:
            return None
        idx = next(self._rr_cursor) % len(rids)
        return rids[idx]

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
        log.info("[PDIngress][replica_added] replica=%s — pulling topology", replica_id)
        await self._pull_topology_for(replica_id)

    def _on_replica_removed(self, replica_id: str) -> None:
        """Fired by _SHARED when a replica died or fell out of the set.
        Demo: log only — _SHARED.replace_replicas / remove_replica already
        evict the actor_topology cache.
        """
        log.info("[PDIngress] replica_removed callback: %s", replica_id)

    # ----- internals -----

    async def _pull_topology_for(self, replica_id: str) -> None:
        """Pull get_actor_topology() from the exact replica `replica_id`.

        Uses Ray Serve's `multiplexed_model_id` metadata channel as a direct
        ReplicaID selector — see observer_router.ObserverRouter.choose_replicas.
        If `replica_id` has just died and is no longer in candidates, the
        router falls back to round-robin and the response self-identifies
        via its embedded `replica_id` field; we upsert under that id, which
        means a stale target degrades to "we refreshed *some* replica's
        topology" rather than failing the call.
        """
        try:
            # Use the "replica:<rid>" namespace so future LoRA / SGLang custom
            # routing on the same multiplexed_model_id channel coexists without
            # collision. See observer_router.REPLICA_DISPATCH_PREFIX.
            target = f"{REPLICA_DISPATCH_PREFIX}{replica_id}"
            handle = self.backend.options(multiplexed_model_id=target)
            topo_dict = await handle.get_actor_topology.remote()
            if not topo_dict:
                return
            self_reported = str(topo_dict.get("replica_id") or "")
            if not self_reported:
                log.warning(
                    "[PDIngress] backend topology missing replica_id; "
                    "skipping upsert (older Ray Serve API?)"
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
            self._shared.upsert_topology(
                self_reported,
                ActorTopology(
                    replica_id=self_reported,
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
                "[PDIngress][topology_pull/summary] replica=%s prefills=%d "
                "decodes=%d same_host=%s global_rank=%s node_rank=%s "
                "local_rank=%s world_size=%s pg_id=%s replica_actor=%s "
                "replica_node=%s",
                self_reported, len(prefills_raw), len(decodes_raw),
                topo_dict.get("same_host"), topo_dict.get("global_rank"),
                topo_dict.get("node_rank"), topo_dict.get("local_rank"),
                topo_dict.get("world_size"), topo_dict.get("pg_id"),
                topo_dict.get("replica_actor_id"),
                topo_dict.get("replica_node"),
            )
            for i, p in enumerate(prefills_raw):
                p = p or {}
                log.info(
                    "[PDIngress][topology_pull/prefill] replica=%s rank=%d "
                    "actor_id=%s node_id=%s gpu_ids=%s",
                    self_reported, i, p.get("actor_id"), p.get("node_id"),
                    p.get("gpu_ids"),
                )
            for i, d in enumerate(decodes_raw):
                d = d or {}
                log.info(
                    "[PDIngress][topology_pull/decode] replica=%s rank=%d "
                    "actor_id=%s node_id=%s gpu_ids=%s",
                    self_reported, i, d.get("actor_id"), d.get("node_id"),
                    d.get("gpu_ids"),
                )
            if self_reported != replica_id:
                # Should only happen when `replica_id` died between the
                # callback firing and the router resolving candidates.
                log.warning(
                    "[PDIngress] direct dispatch for %s degraded to %s; "
                    "target likely just died",
                    replica_id, self_reported,
                )
        except Exception as exc:  # noqa: BLE001 — Demo diagnostics only
            log.warning("[PDIngress] topology pull failed for %s: %s", replica_id, exc)
