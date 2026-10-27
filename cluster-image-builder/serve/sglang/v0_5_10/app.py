"""Ray Serve deployment for SGLang v0.5.10 inference (Neutree static cluster path).

Architecture:
    Controller (FastAPI ingress, CPU-only)
        - HTTP routing, request id middleware, optional custom replica scheduler
        - Forwards to Backend handles (streaming or one-shot)
    Backend (SGLang Engine + OpenAI serving handlers, GPU)
        - Owns ``sglang.srt.entrypoints.engine.Engine`` (multi-process under the hood)
        - Lazy-instantiates OpenAI serving handlers (chat/completion/embedding)
        - Splits each endpoint into non-streaming + streaming variants because
          SGLang's StreamingResponse is not Ray-serialisable across actors.
"""

import asyncio
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
from ray.serve.handle import DeploymentHandle

from downloader import build_request_from_model_args, download_with_markers, get_downloader
from serve._metrics.prometheus_multiproc import install_stable_prometheus_multiproc_dir
from serve._metrics.sglang_ray_bridge import PromToRayBridge
from serve._utils import coerce_args, filter_engine_args
from serve._utils.runtime_env import build_backend_runtime_env
from serve._utils.serve_errors import (
    RequestInputError,
    backend_error,
    error_response,
    error_stream,
    input_error_handler,
    is_streaming,
    read_json_object,
    stream_error_response,
    stream_error_status,
)

logger = logging.getLogger("ray.serve")
_WIRE_RESPONSE_MARKER = "__neutree_wire_response__"


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
    """Build RequestRouterConfig based on scheduler configuration."""
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


def _extract_serializable(result) -> Dict[str, Any]:
    """Keep the HTTP status and JSON data inside this wrapper's wire envelope."""
    status = 200
    if isinstance(result, JSONResponse):
        content, status = json.loads(result.body), result.status_code
    elif hasattr(result, "model_dump"):
        content = result.model_dump(mode="json")
    else:
        content = result
    if not isinstance(result, JSONResponse) and isinstance(content, dict):
        if content.get("object") == "error":
            status = stream_error_status(content)
        elif "error" in content:
            status = stream_error_status(content["error"])
    json.dumps(content, allow_nan=False)
    return {_WIRE_RESPONSE_MARKER: True, "content": content, "status_code": status}


async def _iter_response_body(result) -> AsyncGenerator[str, None]:
    if isinstance(result, StreamingResponse):
        async for chunk in result.body_iterator:
            if isinstance(chunk, bytes):
                chunk = chunk.decode("utf-8")
            if not isinstance(chunk, str):
                if hasattr(chunk, "model_dump"):
                    chunk = chunk.model_dump(mode="json")
                chunk = f"data: {json.dumps(chunk, allow_nan=False)}\n\n"
            yield chunk
        return
    content = _extract_serializable(result)["content"]
    if isinstance(content, dict) and content.get("object") == "error":
        content = {"error": content}
    async for frame in error_stream(content):
        yield frame


def _to_json_response(result) -> JSONResponse:
    if isinstance(result, dict) and result.get(_WIRE_RESPONSE_MARKER):
        return JSONResponse(content=result["content"], status_code=result["status_code"])
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
            model_registry_type: Type of model registry ("bentoml", "hugging-face" or "model-scope")
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
        download_with_markers(
            downloader,
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
        # from the registry-level model_task so users don't have to set it
        # twice.
        if model_task == "text-embedding":
            engine_kwargs.setdefault("is_embedding", True)

        # Force Prometheus multiprocess mode on so the bridge below can read
        # SGLang's metric .db files via PROMETHEUS_MULTIPROC_DIR. SGLang's
        # set_prometheus_multiproc_dir() is gated on enable_metrics, and the
        # bridge silently sees an empty registry if this flag is left off.
        engine_kwargs.setdefault("enable_metrics", True)

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

        from sglang.srt.entrypoints import engine as sglang_engine

        if args.get("enable_metrics"):
            from sglang.srt.utils import common as sglang_common

            metrics_dir = install_stable_prometheus_multiproc_dir(
                sglang_common,
                sglang_engine,
                namespace="sglang",
            )
            logger.info(
                "[Backend] Using stable PROMETHEUS_MULTIPROC_DIR for SGLang: %s",
                metrics_dir,
            )

        logger.info(
            f"[Backend] Initializing SGLang Engine with args: {sorted(args.keys())}"
        )
        # Engine.__init__ launches scheduler/detokenizer subprocesses and
        # blocks until all are ready before returning. When metrics are
        # enabled, PROMETHEUS_MULTIPROC_DIR is already set in our env, so the
        # bridge below can construct an identical CollectorRegistry.
        self.engine = sglang_engine.Engine(**args)
        logger.info("[Backend] SGLang Engine initialized.")

        # Lazy-init OpenAI serving handlers — created on first request.
        self._chat = None
        self._completion = None
        self._embedding = None

        if args.get("enable_metrics"):
            # Spin up the SGLang prometheus -> Ray prometheus bridge. The bridge
            # task survives until __del__ cancels it on actor preemption.
            self._metrics_bridge = PromToRayBridge()
            self._metrics_task = asyncio.get_event_loop().create_task(
                self._metrics_bridge.run()
            )

    def __del__(self):
        # Belt-and-braces cleanup: Engine registers its own atexit handler,
        # but Ray actors that receive SIGKILL skip atexit. Calling shutdown
        # here lets graceful preemption clean up scheduler/detokenizer
        # subprocesses so they don't linger holding GPU memory.
        try:
            task = getattr(self, "_metrics_task", None)
            if task is not None:
                task.cancel()
        except Exception:  # nosec - best-effort cleanup
            pass
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

    # ---- Public methods invoked by Controller via DeploymentHandle ----
    #
    # SGLang's handle_request returns StreamingResponse for stream=True, which
    # cannot be serialised across the Ray actor boundary. Each endpoint is
    # therefore split into a non-streaming method (returns dict / pydantic
    # dump) and a streaming method (async generator yielding SSE strings).

    async def chat_completion(self, payload: Dict[str, Any]) -> Any:
        try:
            from sglang.srt.entrypoints.openai.protocol import ChatCompletionRequest
            is_streaming(payload)
            result = await self._ensure_chat().handle_request(
                ChatCompletionRequest(**{**payload, "stream": False}), _FakeRawRequest()
            )
            return _extract_serializable(result)
        except Exception as exc:
            return _extract_serializable(backend_error(exc, "chat_completion"))

    async def chat_completion_stream(self, payload: Dict[str, Any]) -> AsyncGenerator[str, None]:
        try:
            from sglang.srt.entrypoints.openai.protocol import ChatCompletionRequest
            is_streaming(payload)
            result = await self._ensure_chat().handle_request(
                ChatCompletionRequest(**{**payload, "stream": True}), _FakeRawRequest()
            )
            async for chunk in _iter_response_body(result):
                yield chunk
        except Exception as exc:
            async for chunk in error_stream(backend_error(exc, "chat_completion_stream")):
                yield chunk

    async def completion(self, payload: Dict[str, Any]) -> Any:
        try:
            from sglang.srt.entrypoints.openai.protocol import CompletionRequest
            is_streaming(payload)
            result = await self._ensure_completion().handle_request(
                CompletionRequest(**{**payload, "stream": False}), _FakeRawRequest()
            )
            return _extract_serializable(result)
        except Exception as exc:
            return _extract_serializable(backend_error(exc, "completion"))

    async def completion_stream(self, payload: Dict[str, Any]) -> AsyncGenerator[str, None]:
        try:
            from sglang.srt.entrypoints.openai.protocol import CompletionRequest
            is_streaming(payload)
            result = await self._ensure_completion().handle_request(
                CompletionRequest(**{**payload, "stream": True}), _FakeRawRequest()
            )
            async for chunk in _iter_response_body(result):
                yield chunk
        except Exception as exc:
            async for chunk in error_stream(backend_error(exc, "completion_stream")):
                yield chunk

    async def embedding(self, payload: Dict[str, Any]) -> Any:
        try:
            from sglang.srt.entrypoints.openai.protocol import EmbeddingRequest
            result = await self._ensure_embedding().handle_request(
                EmbeddingRequest(**payload), _FakeRawRequest()
            )
            return _extract_serializable(result)
        except Exception as exc:
            return _extract_serializable(backend_error(exc, "embedding"))

    def get_model_info(self) -> Dict[str, Any]:
        try:
            tm = self.engine.tokenizer_manager
            model_config = getattr(tm, "model_config", None)
            return _extract_serializable({
                "model_path": self.model_path,
                "served_model_name": self.served_model_name,
                "model_task": self.model_task,
                "context_len": getattr(model_config, "context_len", None),
                "is_generation": getattr(tm, "is_generation", True),
            })
        except Exception as exc:
            return _extract_serializable(backend_error(exc, "get_model_info"))


app = FastAPI()
app.add_exception_handler(RequestInputError, input_error_handler)
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

    async def _stream(self, method: str, payload: Dict[str, Any]):
        try:
            chunks = getattr(self.backend.options(stream=True), method).remote(payload)
            first = await chunks.__anext__()
        except StopAsyncIteration:
            return StreamingResponse(content=iter(()), media_type="text/event-stream")
        except Exception:
            logger.exception("Failed to initialize SGLang stream")
            return error_response("Failed to initialize completion stream", 500, "InternalServerError")
        failure = stream_error_response(first)
        if failure is not None:
            content = json.loads(failure.body)
            # SGLang uses a nested SSE error but a flat HTTP error response.
            if isinstance(content.get("error"), dict) and content["error"].get("object") == "error":
                return JSONResponse(content=content["error"], status_code=failure.status_code)
            return failure

        async def with_first():
            yield first
            async for chunk in chunks:
                yield chunk
        return StreamingResponse(content=with_first(), media_type="text/event-stream")

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        payload = await read_json_object(request)
        if is_streaming(payload):
            return await self._stream("chat_completion_stream", payload)
        return _to_json_response(await self.backend.options(stream=False).chat_completion.remote(payload))

    @app.post("/v1/completions")
    async def completions(self, request: Request):
        payload = await read_json_object(request)
        if is_streaming(payload):
            return await self._stream("completion_stream", payload)
        return _to_json_response(await self.backend.options(stream=False).completion.remote(payload))

    @app.post("/v1/embeddings")
    async def embeddings(self, request: Request):
        payload = await read_json_object(request)
        return _to_json_response(await self.backend.options(stream=False).embedding.remote(payload))

    @app.get("/v1/models")
    async def models(self, request: Request):
        result = await self.backend.get_model_info.remote()
        if result["status_code"] >= 400:
            return _to_json_response(result)
        info = result["content"]
        return JSONResponse(content={
            "object": "list",
            "data": [{
                "id": info["served_model_name"],
                "object": "model",
                # `created` is a UNIX timestamp; older OpenAI Python SDK versions
                # access this field unconditionally, so emit a zero rather than
                # omit it. Real model creation time is not tracked here.
                "created": 0,
                "owned_by": "neutree",
                "permission": [],
                "root": info["model_path"],
                "parent": None,
                "max_model_len": info.get("context_len"),
            }],
        })

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}


def app_builder(args: Dict[str, Any]) -> Application:
    """Application builder that assembles the Backend + Controller deployments."""
    model = args.get("model", {})
    deployment_options = args.get("deployment_options", {})
    engine_args = args.get("engine_args") or {}

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
