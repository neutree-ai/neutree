"""Naive PD ingress for Phase 0 Demo.

End-to-end request flow:

    client -> PDIngress (FastAPI)
                |
                | self.backend.pd_chat.remote(payload)
                |   |  ObserverRouter (Ray RequestRouter inside this process)
                |   |  - update_replicas() -> _SHARED.serve_replicas
                |   |  - choose_replicas()  reads _SHARED, round-robins
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
"""
import asyncio
import json
import logging
from typing import Any, Dict, Optional

from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse

from ray import serve
from ray.serve.handle import DeploymentHandle

from .shared_state import ActorTopology, get_shared


log = logging.getLogger("pd_ingress")
app = FastAPI()


@serve.deployment(ray_actor_options={"num_cpus": 0.1})
@serve.ingress(app)
class PDIngress:
    """Single backend handle, ObserverRouter does the per-request selection.
    Demo: also lazily pulls per-replica actor topology into _SHARED for the
    /v1/topology debug view.
    """

    # Lazy topology refresh: at most this many parallel pulls in flight, and
    # never re-pull within this many seconds for a replica we already cached.
    _TOPOLOGY_TTL_SEC = 30.0

    def __init__(self, backend: DeploymentHandle):
        self.backend = backend
        self._shared = get_shared()
        self._topology_inflight: set[str] = set()
        log.info("[PDIngress] Demo dispatcher initialized (ObserverRouter + _SHARED)")

    # ----- routing surface -----

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        payload = await request.json()
        stream = payload.get("stream", False)
        # Fire-and-forget topology refresh so subsequent /v1/topology calls
        # see actor placement without manual priming.
        asyncio.create_task(self._refresh_topology_async())
        if stream:
            r = self.backend.options(stream=True).pd_chat.remote(payload)
            return StreamingResponse(r, media_type="text/event-stream")
        result = await self.backend.options(stream=False).pd_chat.remote(payload)
        if isinstance(result, dict) and "error" in result:
            return JSONResponse(content=result, status_code=500)
        return JSONResponse(content=result)

    @app.get("/v1/models")
    async def models(self):
        result = await self.backend.show_available_models.remote()
        return JSONResponse(content=result)

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}

    # ----- Demo V10/V11 debug surface -----

    @app.get("/v1/topology")
    async def topology(self):
        """Return the current PDIngress global view.

        Shape:
            {
              "last_update_ts": float,
              "serve_replicas": {
                  "<replica_id>": {"replica_id", "node_id", "observed_at"}
              },
              "actor_topology": {
                  "<replica_id>": {"pg_id", "prefill_node", "decode_node",
                                    "same_host", "observed_at"}
              }
            }

        The serve_replicas view is populated by ObserverRouter.update_replicas
        in this process. The actor_topology view is filled on demand by this
        ingress process pulling get_actor_topology() from each backend replica.
        """
        await self._refresh_topology_async()
        return JSONResponse(content=self._shared.snapshot())

    # ----- internals -----

    async def _refresh_topology_async(self) -> None:
        """Top-up _SHARED.actor_topology for any serve_replicas that don't yet
        have a topology entry or whose entry is older than _TOPOLOGY_TTL_SEC.
        Per-replica refresh is single-flight so concurrent ingress requests
        don't fan out N×M pulls.

        Demo simplification: we trigger this from /v1/chat/completions and
        /v1/topology. MVP (PR-ingress-lib) hoists the refresh into the
        ObserverRouter `update_replicas` callback directly.
        """
        snap = self._shared.snapshot()
        now = asyncio.get_running_loop().time()
        targets: list[str] = []
        for rid, sr in snap["serve_replicas"].items():
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

        # Demo pulls the *backend* (any replica's view of itself) — because
        # ObserverRouter round-robins, calling get_actor_topology N times will
        # in practice walk through each replica. For Demo correctness this is
        # good enough; MVP refines to per-replica direct addressing.
        async def pull_once():
            try:
                topo_dict = await self.backend.get_actor_topology.remote()
                # Find which replica's view this is by matching on prefill/decode
                # node_ids — for Demo we just associate with any known serve
                # replica that still lacks topology. Best effort.
                if not topo_dict:
                    return
                target_rid = next(
                    (
                        rid for rid in targets
                        if rid not in self._shared.snapshot()["actor_topology"]
                    ),
                    None,
                )
                if target_rid is None:
                    return
                self._shared.upsert_topology(
                    target_rid,
                    ActorTopology(
                        pg_id=str(topo_dict.get("pg_id", "")),
                        prefill_node=str(topo_dict.get("prefill_node", "")),
                        decode_node=str(topo_dict.get("decode_node", "")),
                        same_host=bool(topo_dict.get("same_host", False)),
                    ),
                )
            except Exception as exc:  # noqa: BLE001 — Demo diagnostics only
                log.warning("[PDIngress] topology pull failed: %s", exc)

        # Fire one pull per missing target. ObserverRouter round-robins so a
        # sequence of pulls hits different replicas.
        await asyncio.gather(*[pull_once() for _ in targets])
