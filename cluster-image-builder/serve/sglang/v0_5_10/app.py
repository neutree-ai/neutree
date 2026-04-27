"""Ray Serve deployment for SGLang inference (Neutree static cluster path).

Mirrors the vLLM v0.11.2 / llama_cpp v0.3.7 app structure so that
ray_orchestrator.go can drive it via `serve.sglang.v0_5_10.app:app_builder`.

Architecture:
    Controller (FastAPI ingress, CPU-only)
        - HTTP routing, request id middleware, optional custom replica scheduler
        - Forwards to Backend handles (streaming or one-shot)
    Backend (SGLang Engine + OpenAI serving handlers, GPU)
        - Owns sglang.srt.entrypoints.engine.Engine (multi-process under the hood)
        - Lazy-instantiates OpenAI serving handlers (chat/completion/embedding/rerank)
        - Splits each endpoint into non-streaming + streaming variants because
          SGLang's StreamingResponse is not Ray-serialisable across actors.
"""

from __future__ import annotations

import enum
import json
import logging
from typing import Any, AsyncGenerator, Dict, Optional

from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from starlette.responses import JSONResponse, StreamingResponse
from starlette_context.middleware import RawContextMiddleware
from starlette_context.plugins import RequestIdPlugin

from ray import serve
from ray.serve import Application
from ray.serve.config import RequestRouterConfig
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator

from downloader import build_request_from_model_args, get_downloader
from serve._utils import coerce_args, filter_engine_args
from serve._utils.runtime_env import build_backend_runtime_env

logger = logging.getLogger("ray.serve")


class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"


# Mapping from scheduler type to request router class path
SCHEDULER_CLASS_PATHS = {
    SchedulerType.STATIC_HASH: "serve._replica_scheduler.static_hash_scheduler:StaticHashReplicaScheduler",
    SchedulerType.CONSISTENT_HASH: "serve._replica_scheduler.chwbl_scheduler:ConsistentHashReplicaScheduler",
}


def _build_request_router_config(scheduler_config: Dict[str, Any]) -> Optional[RequestRouterConfig]:
    """Build RequestRouterConfig based on scheduler configuration.

    See vLLM v0.11.2 app for full documentation of supported scheduler types.
    """
    scheduler_type = scheduler_config.get("type", SchedulerType.POW2)

    if scheduler_type == SchedulerType.POW2:
        logger.info("[app_builder] Using default POW2 scheduler")
        return None

    router_class_path = SCHEDULER_CLASS_PATHS.get(scheduler_type)
    if not router_class_path:
        logger.warning(
            f"[app_builder] Unknown scheduler type: {scheduler_type}, falling back to default POW2"
        )
        return None

    router_kwargs: Dict[str, Any] = {}
    if scheduler_type == SchedulerType.CONSISTENT_HASH:
        router_kwargs = {
            "virtual_nodes_per_replica": scheduler_config.get("virtual_nodes", 100),
            "load_factor": scheduler_config.get("load_factor", 1.25),
            "max_user_messages_for_cache": scheduler_config.get("max_user_messages_for_cache", 2),
        }

    logger.info(
        f"[app_builder] Using custom scheduler: {scheduler_type}, "
        f"class: {router_class_path}, kwargs: {router_kwargs}"
    )

    return RequestRouterConfig(
        request_router_class=router_class_path,
        request_router_kwargs=router_kwargs,
    )


class _FakeRawRequest:
    """Minimal stand-in for FastAPI Request used when invoking SGLang's
    OpenAIServingBase from a Ray actor where no live HTTP request exists.

    SGLang serving handlers call ``raw_request.is_disconnected()`` to abort
    generation when the client disappears; passing ``None`` would raise
    ``AttributeError``. This shim returns ``False`` so generation always
    runs to completion (the Controller still owns the real client connection).
    """

    headers: Dict[str, str] = {}

    async def is_disconnected(self) -> bool:
        return False


def _extract_serializable(result) -> Any:
    """Convert a serving-handler result to a Ray-serialisable Python value."""
    try:
        from fastapi.responses import ORJSONResponse
        if isinstance(result, ORJSONResponse):
            import orjson
            return orjson.loads(result.body)
    except ImportError:
        pass

    if isinstance(result, StreamingResponse):
        raise RuntimeError(
            "Non-streaming request returned StreamingResponse — "
            "verify stream=False in the payload."
        )

    if hasattr(result, "model_dump"):
        return result.model_dump()

    return result


async def _iter_response_body(result) -> AsyncGenerator[str, None]:
    """Yield SSE chunks (as strings) from a serving-handler StreamingResponse.

    Bytes chunks are decoded so the result can be safely sent over the Ray
    actor boundary as Python strings.
    """
    if isinstance(result, StreamingResponse):
        async for chunk in result.body_iterator:
            if isinstance(chunk, bytes):
                chunk = chunk.decode("utf-8")
            yield chunk
        return

    # If the handler returned an error response (ORJSONResponse) instead of
    # streaming, wrap it as a single SSE error frame followed by [DONE].
    try:
        from fastapi.responses import ORJSONResponse
        if isinstance(result, ORJSONResponse):
            import orjson
            error_data = orjson.loads(result.body)
            yield f"data: {json.dumps(error_data)}\n\n"
            yield "data: [DONE]\n\n"
            return
    except ImportError:
        pass

    data = result.model_dump() if hasattr(result, "model_dump") else result
    yield f"data: {json.dumps(data)}\n\n"
    yield "data: [DONE]\n\n"


def _to_json_response(result) -> JSONResponse:
    """Wrap a backend-returned serialisable value in a JSONResponse."""
    if isinstance(result, dict):
        # SGLang error payloads use object='error' and include an HTTP code.
        if result.get("object") == "error":
            status = result.get("code", 400)
            return JSONResponse(content=result, status_code=status)
        return JSONResponse(content=result)
    if hasattr(result, "model_dump"):
        return JSONResponse(content=result.model_dump())
    return JSONResponse(content=result)


@serve.deployment(
    ray_actor_options={"num_cpus": 1, "num_gpus": 1},
    graceful_shutdown_timeout_s=30,
)
class Backend:
    def __init__(
        self,
        # Model config parameters
        model_registry_type: str,
        model_name: str,
        model_version: str,
        model_file: str = "",
        model_task: str = "",
        model_registry_path: str = "",
        model_path: str = "",
        model_serve_name: str = "",
        **engine_kwargs,
    ):
        """Backend deployment for SGLang inference.

        Args:
            model_registry_type: Type of model registry ("bentoml" or "hugging-face")
            model_name: Name of the model in the registry
            model_version: Version of the model
            model_file: Specific model file name (registry-dependent)
            model_task: Task type (e.g., "text-generation", "text-embedding")
            **engine_kwargs: Additional keyword arguments forwarded to ServerArgs
        """
        backend, dl_req = build_request_from_model_args({
            "registry_type": model_registry_type,
            "name": model_name,
            "version": model_version,
            "file": model_file,
            "task": model_task,
            "registry_path": model_registry_path,
            "path": model_path,
        })

        downloader = get_downloader(backend)
        logger.info(
            f"[Backend] Downloading model using backend={backend} "
            f"from source={dl_req.source} to dest={dl_req.dest}"
        )
        downloader.download(
            dl_req.source,
            dl_req.dest,
            credentials=dl_req.credentials,
            recursive=dl_req.recursive,
            overwrite=dl_req.overwrite,
            retries=dl_req.retries,
            timeout=dl_req.timeout,
            metadata=dl_req.metadata,
        )
        logger.info("[Backend] Model download completed.")

        self.model_path = dl_req.dest if dl_req.dest else model_path
        self.model_task = model_task
        self.served_model_name = model_serve_name or self.model_path

        # SGLang exposes an `is_embedding` flag for embedding models; map it
        # from the registry-level model_task so users don't have to set it twice.
        if model_task == "text-embedding":
            engine_kwargs.setdefault("is_embedding", True)

        # Build ServerArgs kwargs with model identity injected.
        from sglang.srt.server_args import ServerArgs

        args: Dict[str, Any] = dict(
            model_path=self.model_path,
            served_model_name=self.served_model_name,
        )
        args.update(engine_kwargs)

        # Coerce JSON-string values for dict/list fields (SSH/Ray path lacks
        # argparse) and drop unknown keys to prevent Engine() TypeError.
        coerce_args(args, ServerArgs)
        filter_engine_args(args, ServerArgs)

        from sglang.srt.entrypoints.engine import Engine

        logger.info(
            f"[Backend] Initializing SGLang Engine with args: {sorted(args.keys())}"
        )
        # Engine.__init__ launches scheduler/detokenizer subprocesses and
        # blocks until all are ready before returning.
        self.engine = Engine(**args)
        logger.info("[Backend] SGLang Engine initialized.")

        # Lazy-init OpenAI serving handlers — created on first request.
        self._chat = None
        self._completion = None
        self._embedding = None
        self._rerank = None

    def __del__(self):
        # Belt-and-braces cleanup: Engine registers its own atexit handler,
        # but Ray actors that receive SIGKILL skip atexit. Calling shutdown
        # here lets graceful preemption clean up scheduler/detokenizer
        # subprocesses so they don't linger holding GPU memory.
        try:
            engine = getattr(self, "engine", None)
            if engine is not None:
                engine.shutdown()
        except Exception:  # nosec - best-effort cleanup
            pass

    # ---- Lazy serving-handler factories ----

    def _ensure_chat(self):
        if self._chat is None:
            from sglang.srt.entrypoints.openai.serving_chat import OpenAIServingChat
            self._chat = OpenAIServingChat(
                self.engine.tokenizer_manager,
                self.engine.template_manager,
            )
        return self._chat

    def _ensure_completion(self):
        if self._completion is None:
            from sglang.srt.entrypoints.openai.serving_completions import OpenAIServingCompletion
            self._completion = OpenAIServingCompletion(
                self.engine.tokenizer_manager,
                self.engine.template_manager,
            )
        return self._completion

    def _ensure_embedding(self):
        if self._embedding is None:
            from sglang.srt.entrypoints.openai.serving_embedding import OpenAIServingEmbedding
            self._embedding = OpenAIServingEmbedding(
                self.engine.tokenizer_manager,
                self.engine.template_manager,
            )
        return self._embedding

    def _ensure_rerank(self):
        if self._rerank is None:
            from sglang.srt.entrypoints.openai.serving_rerank import OpenAIServingRerank
            self._rerank = OpenAIServingRerank(
                self.engine.tokenizer_manager,
                self.engine.template_manager,
            )
        return self._rerank

    # ---- Public methods invoked by Controller via DeploymentHandle ----
    #
    # SGLang's handle_request returns StreamingResponse for stream=True, which
    # cannot be serialised across the Ray actor boundary. Each endpoint is
    # therefore split into a non-streaming method (returns dict / pydantic
    # dump) and a streaming method (async generator yielding SSE strings).

    async def chat_completion(self, payload: Dict[str, Any]) -> Any:
        from sglang.srt.entrypoints.openai.protocol import ChatCompletionRequest
        payload["stream"] = False
        result = await self._ensure_chat().handle_request(
            ChatCompletionRequest(**payload), _FakeRawRequest()
        )
        return _extract_serializable(result)

    async def chat_completion_stream(self, payload: Dict[str, Any]) -> AsyncGenerator[str, None]:
        from sglang.srt.entrypoints.openai.protocol import ChatCompletionRequest
        payload["stream"] = True
        result = await self._ensure_chat().handle_request(
            ChatCompletionRequest(**payload), _FakeRawRequest()
        )
        async for chunk in _iter_response_body(result):
            yield chunk

    async def completion(self, payload: Dict[str, Any]) -> Any:
        from sglang.srt.entrypoints.openai.protocol import CompletionRequest
        payload["stream"] = False
        result = await self._ensure_completion().handle_request(
            CompletionRequest(**payload), _FakeRawRequest()
        )
        return _extract_serializable(result)

    async def completion_stream(self, payload: Dict[str, Any]) -> AsyncGenerator[str, None]:
        from sglang.srt.entrypoints.openai.protocol import CompletionRequest
        payload["stream"] = True
        result = await self._ensure_completion().handle_request(
            CompletionRequest(**payload), _FakeRawRequest()
        )
        async for chunk in _iter_response_body(result):
            yield chunk

    async def embedding(self, payload: Dict[str, Any]) -> Any:
        from sglang.srt.entrypoints.openai.protocol import EmbeddingRequest
        result = await self._ensure_embedding().handle_request(
            EmbeddingRequest(**payload), _FakeRawRequest()
        )
        return _extract_serializable(result)

    async def rerank(self, payload: Dict[str, Any]) -> Any:
        from sglang.srt.entrypoints.openai.protocol import V1RerankReqInput
        result = await self._ensure_rerank().handle_request(
            V1RerankReqInput(**payload), _FakeRawRequest()
        )
        return _extract_serializable(result)

    def get_model_info(self) -> Dict[str, Any]:
        tm = self.engine.tokenizer_manager
        context_len = None
        model_config = getattr(tm, "model_config", None)
        if model_config is not None:
            context_len = getattr(model_config, "context_len", None)
        return {
            "model_path": self.model_path,
            "served_model_name": self.served_model_name,
            "model_task": self.model_task,
            "context_len": context_len,
            "is_generation": getattr(tm, "is_generation", True),
        }


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
        logger.info("[Controller] Initialized with backend handle")

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        payload = await request.json()
        if payload.get("stream", False):
            gen: DeploymentResponseGenerator = (
                self.backend.options(stream=True).chat_completion_stream.remote(payload)
            )
            return StreamingResponse(content=gen, media_type="text/event-stream")
        result = await self.backend.options(stream=False).chat_completion.remote(payload)
        return _to_json_response(result)

    @app.post("/v1/completions")
    async def completions(self, request: Request):
        payload = await request.json()
        if payload.get("stream", False):
            gen: DeploymentResponseGenerator = (
                self.backend.options(stream=True).completion_stream.remote(payload)
            )
            return StreamingResponse(content=gen, media_type="text/event-stream")
        result = await self.backend.options(stream=False).completion.remote(payload)
        return _to_json_response(result)

    @app.post("/v1/embeddings")
    async def embeddings(self, request: Request):
        payload = await request.json()
        result = await self.backend.options(stream=False).embedding.remote(payload)
        return _to_json_response(result)

    @app.post("/v1/rerank")
    async def rerank(self, request: Request):
        payload = await request.json()
        result = await self.backend.options(stream=False).rerank.remote(payload)
        return _to_json_response(result)

    @app.get("/v1/models")
    async def models(self, request: Request):
        info = await self.backend.get_model_info.remote()
        return JSONResponse(content={
            "object": "list",
            "data": [{
                "id": info["served_model_name"],
                "object": "model",
                "owned_by": "neutree",
                "permission": [],
                "root": info["model_path"],
                "max_model_len": info.get("context_len"),
            }],
        })

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}


def app_builder(args: Dict[str, Any]) -> Application:
    """Application builder that assembles the Backend + Controller deployments.

    Mirrors the vLLM v0.11.2 / llama_cpp v0.3.7 builders so the Go
    ray_orchestrator can drive it without engine-specific branches.
    """
    model = args.get("model", {})
    deployment_options = args.get("deployment_options", {})
    engine_args = args.get("engine_args", {})

    backend_options = deployment_options.get("backend", {})
    controller_options = deployment_options.get("controller", {})

    scheduler_config = deployment_options.get("scheduler", {})
    request_router_config = _build_request_router_config(scheduler_config)

    backend_deploy_options: Dict[str, Any] = {
        "max_ongoing_requests": backend_options.get("max_ongoing_requests", 100),
        "num_replicas": backend_options.get("num_replicas", 1),
        "ray_actor_options": {
            "num_cpus": backend_options.get("num_cpus", 1),
            "num_gpus": backend_options.get("num_gpus", 1),
            "memory": backend_options.get("memory", None),
            "resources": backend_options.get("resources", {}),
        },
    }

    if request_router_config is not None:
        backend_deploy_options["request_router_config"] = request_router_config

    backend_container = args.get("backend_container")
    if backend_container:
        backend_deploy_options["ray_actor_options"]["runtime_env"] = build_backend_runtime_env(backend_container)

    backend_deployment = Backend.options(**backend_deploy_options).bind(
        model_registry_type=model.get("registry_type"),
        model_name=model.get("name"),
        model_version=model.get("version"),
        model_file=model.get("file", ""),
        model_task=model.get("task"),
        model_registry_path=model.get("registry_path", ""),
        model_path=model.get("path", ""),
        model_serve_name=model.get("serve_name", ""),
        **engine_args,
    )

    controller_deployment = Controller.options(
        max_ongoing_requests=(
            backend_options.get("max_ongoing_requests", 100)
            * backend_options.get("num_replicas", 1)
        ),
        num_replicas=controller_options.get("num_replicas", 1),
        ray_actor_options={
            "num_cpus": controller_options.get("num_cpus", 0.1),
            "num_gpus": controller_options.get("num_gpus", 0),
        },
    ).bind(backend=backend_deployment)

    return controller_deployment
