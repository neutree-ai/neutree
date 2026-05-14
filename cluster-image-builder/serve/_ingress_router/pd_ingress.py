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

from .shared_state import ActorInfo, ActorTopology, get_shared


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

        Each get_actor_topology() response self-identifies via `replica_id`
        (from `serve.get_replica_context()` on the backend side), so the
        ingress can key _SHARED.actor_topology by the same id ObserverRouter
        populates — no "any-replica-missing-topology" heuristic.

        ObserverRouter round-robins; we fire up to len(missing) pulls so the
        cursor walks through each candidate. The single-flight set guards
        against concurrent /v1/chat/completions traffic triggering N×M pulls.

        MVP (PR-ingress-lib) hoists this refresh into the ObserverRouter
        update_replicas callback directly so the view is always fresh and the
        request path stays pull-free.
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

        # Reserve single-flight slots upfront.
        for rid in targets:
            self._topology_inflight.add(rid)

        async def pull_once():
            try:
                topo_dict = await self.backend.get_actor_topology.remote()
                if not topo_dict:
                    return
                replica_id = str(topo_dict.get("replica_id") or "")
                if not replica_id:
                    log.warning(
                        "[PDIngress] backend returned topology without replica_id; "
                        "dropping (likely older Ray Serve API)"
                    )
                    return
                prefill_raw = topo_dict.get("prefill") or {}
                decode_raw = topo_dict.get("decode") or {}
                self._shared.upsert_topology(
                    replica_id,
                    ActorTopology(
                        replica_id=replica_id,
                        replica_actor_id=str(topo_dict.get("replica_actor_id", "")),
                        replica_node=str(topo_dict.get("replica_node", "")),
                        pg_id=str(topo_dict.get("pg_id", "")),
                        prefill=ActorInfo(
                            kind=str(prefill_raw.get("kind", "prefill")),
                            actor_id=str(prefill_raw.get("actor_id", "")),
                            node_id=str(prefill_raw.get("node_id", "")),
                            gpu_ids=[int(g) for g in (prefill_raw.get("gpu_ids") or [])],
                            healthy=bool(prefill_raw.get("healthy", False)),
                        ),
                        decode=ActorInfo(
                            kind=str(decode_raw.get("kind", "decode")),
                            actor_id=str(decode_raw.get("actor_id", "")),
                            node_id=str(decode_raw.get("node_id", "")),
                            gpu_ids=[int(g) for g in (decode_raw.get("gpu_ids") or [])],
                            healthy=bool(decode_raw.get("healthy", False)),
                        ),
                        same_host=bool(topo_dict.get("same_host", False)),
                    ),
                )
            except Exception as exc:  # noqa: BLE001 — Demo diagnostics only
                log.warning("[PDIngress] topology pull failed: %s", exc)

        try:
            # Fire one pull per missing target; ObserverRouter round-robins so
            # the cursor walks through each candidate over the burst.
            await asyncio.gather(*[pull_once() for _ in targets])
        finally:
            for rid in targets:
                self._topology_inflight.discard(rid)
