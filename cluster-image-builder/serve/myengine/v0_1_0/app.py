"""Minimal mock inference engine for Neutree (myengine v0.1.0).

This is a reference "custom engine" that conforms to the same Ray Serve contract
as the built-in engines (vllm / sglang / llama-cpp) but performs NO real model
loading and requires NO GPU. `generate` returns canned, OpenAI-compatible mock
content so the full neutree deploy + smoke-test path can be exercised end to end.

Contract mirrored from serve/llama_cpp/v0_3_7/app.py (Ray 2.43, cluster <= v1.0.0):
  - Backend deployment: generate / generate_embeddings / show_available_models
  - Controller deployment (@serve.ingress): OpenAI-compatible HTTP surface
      POST /v1/chat/completions   (streaming + non-streaming)
      POST /v1/completions
      GET  /v1/models
      POST /v1/embeddings
      GET  /health
  - app_builder(args) -> Application   (entry point; import path is a convention:
      serve.myengine.v0_1_0.app:app_builder)

Only stdlib + ray + fastapi/starlette are imported, all of which are present in the
cluster serve image, so this module can be dropped into an existing cluster without
extra dependencies (no llama_cpp / vllm / downloader / serve._utils needed).
"""
from __future__ import annotations

import enum
import json
import time
import uuid
from typing import Any, Dict, Iterator, List, Optional

import ray  # noqa: F401  (kept for parity with built-in engine apps)
from ray import serve
from ray.serve import Application
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator
from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse
from fastapi.middleware.cors import CORSMiddleware

try:  # request-id middleware is available in the cluster image; degrade gracefully otherwise
    from starlette_context.plugins import RequestIdPlugin
    from starlette_context.middleware import RawContextMiddleware
    _HAS_CONTEXT = True
except Exception:  # pragma: no cover
    _HAS_CONTEXT = False


class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"


# --- mock content helpers ----------------------------------------------------

def _last_user_message(payload: Dict[str, Any]) -> str:
    """Best-effort extraction of the latest user turn, for an echo-style reply."""
    messages = payload.get("messages") or []
    for msg in reversed(messages):
        if isinstance(msg, dict) and msg.get("role") == "user":
            content = msg.get("content", "")
            if isinstance(content, list):  # OpenAI content-parts form
                content = " ".join(
                    part.get("text", "") for part in content if isinstance(part, dict)
                )
            return str(content)
    # /v1/completions style
    prompt = payload.get("prompt", "")
    if isinstance(prompt, list):
        prompt = " ".join(str(p) for p in prompt)
    return str(prompt)


def _mock_reply(model_id: str, user_text: str) -> str:
    snippet = (user_text or "").strip()
    if len(snippet) > 120:
        snippet = snippet[:120] + "..."
    return (
        f"[myengine mock] Hello from the mock inference engine '{model_id}'. "
        f"This is a canned response and no real model was run. "
        f"You said: \"{snippet}\"."
    )


def _approx_tokens(text: str) -> int:
    return max(1, len(text.split()))


@serve.deployment
class Backend:
    def __init__(self,
                 model_registry_type: str,
                 model_name: str,
                 model_version: str,
                 model_file: str = "",
                 model_task: str = "",
                 model_registry_path: str = "",
                 model_path: str = "",
                 model_serve_name: str = "",
                 **model_settings):
        """Mock backend: records identity only, never downloads or loads a model."""
        self.model_id = model_serve_name or model_name
        self.model_task = model_task or "text-generation"
        self.model_settings = model_settings  # accepted (engine_args) but ignored
        print(
            f"[myengine.Backend] initialized mock backend model_id={self.model_id} "
            f"task={self.model_task} registry_type={model_registry_type} "
            f"(no model loaded, no GPU used)"
        )

    # --- generation ----------------------------------------------------------
    async def generate(self, payload: Any):
        """Return a dict (non-streaming) or a generator (streaming), OpenAI-shaped.

        Mirrors the built-in engines: when payload requests streaming, return an
        iterator of chunks; otherwise return the full response object.
        """
        is_chat = "messages" in payload
        stream = bool(payload.get("stream", False))
        user_text = _last_user_message(payload)
        text = _mock_reply(self.model_id, user_text)

        if stream:
            return self._stream(text, is_chat, user_text)
        return self._complete(text, is_chat, user_text)

    def _complete(self, text: str, is_chat: bool, user_text: str) -> Dict[str, Any]:
        created = int(time.time())
        prompt_tokens = _approx_tokens(user_text)
        completion_tokens = _approx_tokens(text)
        usage = {
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
            "total_tokens": prompt_tokens + completion_tokens,
        }
        if is_chat:
            return {
                "id": f"chatcmpl-mock-{uuid.uuid4().hex[:24]}",
                "object": "chat.completion",
                "created": created,
                "model": self.model_id,
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": text},
                    "finish_reason": "stop",
                }],
                "usage": usage,
            }
        return {
            "id": f"cmpl-mock-{uuid.uuid4().hex[:24]}",
            "object": "text_completion",
            "created": created,
            "model": self.model_id,
            "choices": [{
                "index": 0,
                "text": text,
                "finish_reason": "stop",
            }],
            "usage": usage,
        }

    def _stream(self, text: str, is_chat: bool, user_text: str) -> Iterator[Dict[str, Any]]:
        created = int(time.time())
        cid = f"{'chatcmpl' if is_chat else 'cmpl'}-mock-{uuid.uuid4().hex[:24]}"
        obj = "chat.completion.chunk" if is_chat else "text_completion"
        words = text.split(" ")

        def base_chunk(choice: Dict[str, Any]) -> Dict[str, Any]:
            return {
                "id": cid,
                "object": obj,
                "created": created,
                "model": self.model_id,
                "choices": [choice],
            }

        if is_chat:
            yield base_chunk({"index": 0, "delta": {"role": "assistant"}, "finish_reason": None})
            for i, word in enumerate(words):
                piece = word if i == 0 else " " + word
                yield base_chunk({"index": 0, "delta": {"content": piece}, "finish_reason": None})
            yield base_chunk({"index": 0, "delta": {}, "finish_reason": "stop"})
        else:
            for i, word in enumerate(words):
                piece = word if i == 0 else " " + word
                yield base_chunk({"index": 0, "text": piece, "finish_reason": None})
            yield base_chunk({"index": 0, "text": "", "finish_reason": "stop"})

    async def generate_embeddings(self, payload: Any) -> Dict[str, Any]:
        """Deterministic mock embeddings (fixed-width vectors)."""
        inputs = payload.get("input", "")
        if isinstance(inputs, str):
            inputs = [inputs]
        dim = 8
        data: List[Dict[str, Any]] = []
        total_tokens = 0
        for idx, item in enumerate(inputs):
            total_tokens += _approx_tokens(str(item))
            vec = [round(((idx + j + 1) % dim) / dim, 4) for j in range(dim)]
            data.append({"index": idx, "object": "embedding", "embedding": vec})
        return {
            "object": "list",
            "data": data,
            "model": self.model_id,
            "usage": {"prompt_tokens": total_tokens, "total_tokens": total_tokens},
        }

    async def show_available_models(self) -> Dict[str, Any]:
        return {
            "object": "list",
            "data": [{
                "id": self.model_id,
                "object": "model",
                "permissions": [],
                "owned_by": "neutree-myengine-mock",
            }],
        }


app = FastAPI()
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)
if _HAS_CONTEXT:
    app.add_middleware(RawContextMiddleware, plugins=(RequestIdPlugin(),))


@serve.deployment
@serve.ingress(app)
class Controller:
    def __init__(self,
                 backend: DeploymentHandle,
                 scheduler_type: str = SchedulerType.POW2,
                 virtual_nodes: int = 100,
                 load_factor: float = 1.25):
        self.backend = backend

        handle = self.backend
        if not handle.is_initialized:
            handle._init()

        if scheduler_type != SchedulerType.POW2:
            try:
                router = handle._router._asyncio_router
                original_scheduler = router._replica_scheduler
                new_scheduler = None
                if scheduler_type == SchedulerType.STATIC_HASH:
                    from serve._replica_scheduler.static_hash_scheduler import StaticHashReplicaScheduler
                    new_scheduler = StaticHashReplicaScheduler()
                elif scheduler_type == SchedulerType.CONSISTENT_HASH:
                    from serve._replica_scheduler.chwbl_scheduler import ConsistentHashReplicaScheduler
                    new_scheduler = ConsistentHashReplicaScheduler(
                        virtual_nodes_per_replica=virtual_nodes,
                        load_factor=load_factor,
                    )
                if new_scheduler is not None:
                    new_scheduler.update_replicas(list(original_scheduler.curr_replicas.values()))
                    router._replica_scheduler = new_scheduler
                    print(f"[myengine.Controller] Replaced scheduler with {scheduler_type}")
            except Exception as e:  # pragma: no cover
                print(f"[myengine.Controller] Failed to replace scheduler: {e}; using default")
        else:
            print("[myengine.Controller] Using POW2 scheduler")

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        req_obj = await request.json()
        stream = req_obj.get("stream", False)
        if stream:
            r: DeploymentResponseGenerator = self.backend.options(stream=True).generate.remote(req_obj)

            async def event_generator():
                async for chunk in r:
                    yield f"data: {json.dumps(chunk)}\n\n"
                yield "data: [DONE]\n\n"

            return StreamingResponse(content=event_generator(), media_type="text/event-stream")
        result = await self.backend.options(stream=False).generate.remote(req_obj)
        return JSONResponse(content=result)

    @app.post("/v1/completions")
    async def completions(self, request: Request):
        return await self.chat(request)

    @app.get("/v1/models")
    async def models(self, request: Request):
        result = await self.backend.show_available_models.remote()
        return JSONResponse(content=result)

    @app.post("/v1/embeddings")
    async def embeddings(self, request: Request):
        req_obj = await request.json()
        result = await self.backend.options(stream=False).generate_embeddings.remote(req_obj)
        return JSONResponse(content=result)

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}


def app_builder(args: Dict[str, Any]) -> Application:
    """Configure and return the Controller deployment (binds the Backend)."""
    model = args.get("model", {})
    deployment_options = args.get("deployment_options", {})
    engine_args = args.get("engine_args", {})

    backend_options = deployment_options.get("backend", {})
    controller_options = deployment_options.get("controller", {})

    scheduler_config = deployment_options.get("scheduler", {})
    scheduler_type = scheduler_config.get("type", SchedulerType.POW2)
    virtual_nodes = scheduler_config.get("virtual_nodes", 100)
    load_factor = scheduler_config.get("load_factor", 1.25)

    backend_deployment = Backend.options(
        max_ongoing_requests=backend_options.get("max_ongoing_requests", 100),
        num_replicas=backend_options.get("num_replicas", 1),
        ray_actor_options={
            # mock engine: CPU-only, never requests a GPU
            "num_cpus": backend_options.get("num_cpus", 1),
            "num_gpus": backend_options.get("num_gpus", 0),
            "memory": backend_options.get("memory", None),
            "resources": backend_options.get("resources", {}),
        },
    ).bind(
        model_registry_type=model.get("registry_type"),
        model_name=model.get("name"),
        model_version=model.get("version"),
        model_file=model.get("file", ""),
        model_task=model.get("task", ""),
        model_registry_path=model.get("registry_path", ""),
        model_path=model.get("path", ""),
        model_serve_name=model.get("serve_name", ""),
        **engine_args,
    )

    controller_deployment = Controller.options(
        max_ongoing_requests=backend_options.get("max_ongoing_requests", 100)
        * backend_options.get("num_replicas", 1),
        num_replicas=controller_options.get("num_replicas", 1),
        ray_actor_options={
            "num_cpus": controller_options.get("num_cpus", 0.1),
            "num_gpus": controller_options.get("num_gpus", 0),
        },
    ).bind(
        backend=backend_deployment,
        scheduler_type=scheduler_type,
        virtual_nodes=virtual_nodes,
        load_factor=load_factor,
    )

    return controller_deployment
