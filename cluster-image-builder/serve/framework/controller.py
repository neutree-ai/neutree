"""Generic Controller deployment for the Neutree serve framework.

This module contains the HTTP gateway (Controller) that routes requests to
a Backend deployment via Ray Serve DeploymentHandle.  It has **zero**
engine-specific imports — all response handling uses a dict-based
serialization protocol so that it can run in a lightweight framework
container without vLLM, llama-cpp, or any other inference engine installed.

Serialization protocol (enforced by the BackendWrapper in app_builder.py):
  Non-streaming:  {"body": <dict>, "status_code": <int|None>}
  Streaming:      AsyncGenerator of SSE-formatted strings (passthrough)
"""

import json
import logging
from typing import Any

from fastapi import FastAPI, Request
from starlette.responses import StreamingResponse, JSONResponse
from fastapi.middleware.cors import CORSMiddleware
from starlette_context.plugins import RequestIdPlugin
from starlette_context.middleware import RawContextMiddleware

from ray import serve
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator

logger = logging.getLogger("ray.serve")

app = FastAPI()
app.add_middleware(RawContextMiddleware, plugins=(RequestIdPlugin(),))
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@serve.deployment(ray_actor_options={"num_cpus": 0.1})
@serve.ingress(app)
class Controller:
    def __init__(self, backend: DeploymentHandle):
        self.backend = backend
        print("[Controller] Initialized with backend handle (framework mode)")

    # ------------------------------------------------------------------
    # Helper
    # ------------------------------------------------------------------

    @staticmethod
    def _json_response(result: dict) -> JSONResponse:
        """Convert the dict-based serialization protocol to a JSONResponse."""
        status_code = result.get("status_code")
        body = result.get("body", result)
        if status_code is not None:
            return JSONResponse(content=body, status_code=status_code)
        return JSONResponse(content=body)

    # ------------------------------------------------------------------
    # Endpoints
    # ------------------------------------------------------------------

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        req_obj = await request.json()
        stream = req_obj.get("stream", False)

        if stream:
            r: DeploymentResponseGenerator = (
                self.backend.options(stream=True).generate.remote(req_obj)
            )
            return StreamingResponse(content=r, media_type="text/event-stream")
        else:
            result = await self.backend.options(stream=False).generate.remote(req_obj)
            return self._json_response(result)

    @app.post("/v1/completions")
    async def completions(self, request: Request):
        return await self.chat(request)

    @app.post("/v1/embeddings")
    async def embeddings(self, request: Request):
        req_obj = await request.json()
        result = await self.backend.options(stream=False).generate_embeddings.remote(req_obj)
        return self._json_response(result)

    @app.post("/v1/rerank")
    async def rerank(self, request: Request):
        req_obj = await request.json()
        result = await self.backend.options(stream=False).rerank.remote(req_obj)
        return self._json_response(result)

    @app.get("/v1/models")
    async def models(self, request: Request):
        result = await self.backend.show_available_models.remote()
        return self._json_response(result)

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}
