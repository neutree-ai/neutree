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
"""
import asyncio
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
    """Single backend handle, ObserverRouter does the per-request selection.

    PDIngress registers a `replica_added` callback on _SHARED so a topology
    pull happens the moment ObserverRouter sees a new PDCollocatedBackend
    replica (push-driven). The request-path lazy refresh stays only as a
    safety net for ids that somehow slipped past the callback (e.g. an
    ingress restart that misses the burst of update_replicas events fired
    against the previous incarnation).
    """

    # Safety-net lazy refresh: per-replica TTL before the on-request fallback
    # decides a topology entry is stale enough to re-pull.
    _TOPOLOGY_TTL_SEC = 30.0

    def __init__(self, backend: DeploymentHandle):
        self.backend = backend
        self._shared = get_shared()
        self._topology_inflight: set[str] = set()
        # Register push-driven hooks BEFORE the first request lands. The
        # _SHARED.on_* registration is idempotent, so a restarted PDIngress
        # replica registering again is harmless.
        self._shared.on_replica_added(self._on_replica_added)
        self._shared.on_replica_removed(self._on_replica_removed)
        log.info(
            "[PDIngress] dispatcher initialized (ObserverRouter + _SHARED with callbacks)"
        )

    # ----- routing surface -----

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        payload = await request.json()
        stream = payload.get("stream", False)
        snap = self._shared.snapshot()
        log.info(
            "[PDIngress][chat] stream=%s replicas_seen=%d topology_cached=%d",
            stream, len(snap.get("serve_replicas", {})),
            len(snap.get("actor_topology", {})),
        )
        # Safety-net topology refresh — push path covers the common case via
        # _on_replica_added; this catches any replica that the callback missed.
        asyncio.create_task(self._refresh_topology_async())
        if stream:
            r = self.backend.options(stream=True).pd_chat.remote(payload)
            return StreamingResponse(r, media_type="text/event-stream")
        result = await self.backend.options(stream=False).pd_chat.remote(payload)
        if isinstance(result, dict) and "error" in result:
            log.warning(
                "[PDIngress][chat_error] err=%s", result.get("error"),
            )
            return JSONResponse(content=result, status_code=500)
        return JSONResponse(content=result)

    @app.get("/v1/models")
    async def models(self):
        result = await self.backend.show_available_models.remote()
        return JSONResponse(content=result)

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}

    # ----- Demo V10/V11/V14 debug surface -----

    @app.get("/v1/topology")
    async def topology(self):
        """Return the current PDIngress global view of PDCollocatedBackend
        replicas + their inner actor placement. See shared_state.ActorTopology.

        The serve_replicas dict is populated by ObserverRouter.update_replicas.
        The actor_topology dict is populated by the replica_added callback
        (push path) and the on-request safety-net pull.
        """
        # Safety net for /v1/topology probes that happen before any chat call.
        await self._refresh_topology_async()
        return JSONResponse(content=self._shared.snapshot())

    # ----- push-driven callbacks (V14) -----

    async def _on_replica_added(self, replica_id: str, snap: ReplicaSnapshot) -> None:
        """Fired by _SHARED when ObserverRouter saw a brand-new replica.
        Pull its actor topology immediately so the view is hot before the
        first request arrives. Single-flight via _topology_inflight.
        """
        if replica_id in self._topology_inflight:
            return
        log.info("[PDIngress][replica_added] replica=%s — pulling topology", replica_id)
        self._topology_inflight.add(replica_id)
        try:
            await self._pull_topology_for(replica_id)
        finally:
            self._topology_inflight.discard(replica_id)

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

    async def _refresh_topology_async(self) -> None:
        """Safety-net refresh — only fires for replicas that the push path
        somehow missed or whose entry exceeded _TOPOLOGY_TTL_SEC. With the
        replica_added callback in place this is effectively a no-op in steady
        state; kept for restart races and TTL refresh.
        """
        snap = self._shared.snapshot()
        now = asyncio.get_running_loop().time()
        targets: list[str] = []
        for rid in snap["serve_replicas"].keys():
            if rid in self._topology_inflight:
                continue
            topo = snap["actor_topology"].get(rid)
            if topo is None:
                targets.append(rid)
                continue
            try:
                age = now - float(topo.get("observed_at", 0.0))
            except (TypeError, ValueError):
                age = float("inf")
            if age >= self._TOPOLOGY_TTL_SEC:
                targets.append(rid)

        if not targets:
            return

        for rid in targets:
            self._topology_inflight.add(rid)

        try:
            await asyncio.gather(*[self._pull_topology_for(rid) for rid in targets])
        finally:
            for rid in targets:
                self._topology_inflight.discard(rid)
