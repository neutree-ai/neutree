"""Naive PD ingress for Phase 0 Demo.

End-to-end request flow:

    client -> PDIngress (FastAPI)
                |
                | dispatch_pd_request(req)  -- single collocated backend
                v
              PDCollocatedBackend
                |--> PrefillActor.prefill_at(req)        (returns kv_transfer_params)
                |--> DecodeActor.decode_at(req, kv_params) (streams completion chunks)

Demo invariants the architecture review needs to validate:
    V1: API -> IR -> orchestrator -> Ray Application end-to-end works
    V2: (strategy, placement.roles) routes to correct import_path
    V3: plan serialized to args reaches Python deserialization intact
    V4: STRICT_PACK PG actually colocates prefill + decode actors
    V6: vLLM kv_transfer_params is a plain dict round-trippable via Ray
    V7: Ray Serve handle dispatch latency stays sub-ms

The MVP (`PR-ingress-lib`) replaces this file with the full Ingress-as-Decider
stack: _SHARED state, ObserverRouter callbacks, candidate_builder,
dispatch_collocated, decode-first CHWBL + same-host prefill constraint.
"""
import json
import logging
from typing import Any

from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse

from ray import serve
from ray.serve.handle import DeploymentHandle


log = logging.getLogger("pd_ingress")
app = FastAPI()


@serve.deployment(ray_actor_options={"num_cpus": 0.1})
@serve.ingress(app)
class PDIngress:
    """Single-backend dispatcher. Phase 0 Demo: no candidate selection, no
    CHWBL scheduling, no multiplexed_model_id addressing. The backend handle
    itself dispatches to one of the N PDCollocatedBackend replicas via Ray
    Serve's default POW2 load balancing.
    """

    def __init__(self, backend: DeploymentHandle):
        self.backend = backend
        log.info("[PDIngress] Demo single-backend dispatcher initialized")

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        payload = await request.json()
        stream = payload.get("stream", False)
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
