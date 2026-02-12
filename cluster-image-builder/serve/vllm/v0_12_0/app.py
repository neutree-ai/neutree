import os
import enum
import logging
import time
import json
from typing import Dict, Optional, Any, AsyncGenerator, List

from fastapi import FastAPI, Request
from starlette.responses import StreamingResponse, JSONResponse
from fastapi.middleware.cors import CORSMiddleware
from starlette_context.plugins import RequestIdPlugin
from starlette_context.middleware import RawContextMiddleware
from fastapi.middleware import Middleware

import bentoml
from ray import serve
from ray.serve import Application
from ray.serve.config import RequestRouterConfig
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator

from vllm.engine.arg_utils import AsyncEngineArgs
from vllm.v1.engine.async_llm import AsyncLLM
from vllm.entrypoints.openai.protocol import (
    ChatCompletionRequest, ChatCompletionResponse, ErrorResponse,
    EmbeddingRequest, EmbeddingResponse,
    ScoreRequest, ScoreResponse,
    RerankRequest, RerankResponse,
    EmbeddingCompletionRequest
)
from vllm.entrypoints.openai.serving_chat import OpenAIServingChat
from vllm.entrypoints.openai.serving_embedding import OpenAIServingEmbedding
from vllm.entrypoints.openai.serving_score import ServingScores
from vllm.entrypoints.openai.serving_models import BaseModelPath, OpenAIServingModels

from downloader import get_downloader, build_request_from_model_args
from serve._metrics.ray_stat_logger import NeutreeRayStatLogger


class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"


# Mapping from scheduler type to request router class path
SCHEDULER_CLASS_PATHS = {
    SchedulerType.STATIC_HASH: "serve._replica_scheduler.static_hash_scheduler:StaticHashReplicaScheduler",
    SchedulerType.CONSISTENT_HASH: "serve._replica_scheduler.chwbl_scheduler:ConsistentHashReplicaScheduler",
}


@serve.deployment(ray_actor_options={"num_cpus": 1, "num_gpus": 1})
class Backend:
    def __init__(self,
                 # Model config parameters
                 model_registry_type: str,
                 model_name: str,
                 model_version: str,
                 model_file: str = "",
                 model_task: str = "",
                 model_registry_path: str = "",
                 model_path: str = "",
                 model_serve_name: str = "",
                 **engine_kwargs):
        """
        Backend deployment for vLLM inference.

        Args:
            model_registry_type: Type of model registry ("bentoml" or "hugging-face")
            model_name: Name of the model in the registry
            model_version: Version of the model
            model_file: Specific model file name (for bentoml)
            model_task: Task type (e.g., "text-generation", "text-embedding", "text-rerank")
            **engine_kwargs: Additional keyword arguments passed directly to AsyncEngineArgs
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
        print(f"[Backend] Downloading model using backend={backend} from source={dl_req.source} to dest={dl_req.dest}")
        downloader.download(dl_req.source, dl_req.dest, credentials=dl_req.credentials,
                            recursive=dl_req.recursive, overwrite=dl_req.overwrite,
                            retries=dl_req.retries, timeout=dl_req.timeout, metadata=dl_req.metadata)
        print(f"[Backend] Model download completed.")

        self.model_id = model_serve_name
        self.model_task = model_task

        # Extract our custom parameters BEFORE creating AsyncEngineArgs to avoid unexpected keyword errors
        # Tool calling configuration
        self.enable_auto_tools = False
        self.tool_parser = engine_kwargs.pop("tool_call_parser", None)
        if self.tool_parser:
            self.enable_auto_tools = True

        # Reasoning configuration (read but don't pop - engine needs these too)
        self.enable_reasoning = engine_kwargs.get("enable_reasoning", False)
        self.reasoning_parser = engine_kwargs.get("reasoning_parser", None)

        # Extract chat template parameters
        self.chat_template = engine_kwargs.pop("chat_template", None)
        self.chat_template_content_format = engine_kwargs.pop("chat_template_content_format", "auto")

        # Extract other chat-specific parameters (keep defaults from vLLM)
        self.response_role = engine_kwargs.pop("response_role", "assistant")
        self.enable_prompt_tokens_details = engine_kwargs.pop("enable_prompt_tokens_details", False)

        # Map model task to vLLM task
        task = "generate"
        if model_task == "text-generation":
            task = "generate"
        elif model_task == "text-embedding":
            task = "embed"
        elif model_task in ["text-rerank", "score"]:
            task = "score"

        # merge engine args
        args = dict(
            task=task,
            model=model_path,
            served_model_name=self.model_id,
            disable_log_stats=False,
            enable_prefix_caching=True,
        )

        args.update(engine_kwargs)

        engine_args = AsyncEngineArgs(
            **args
        )

        self.engine = AsyncLLM.from_engine_args(
            engine_args,
            stat_loggers=[NeutreeRayStatLogger],
        )
        self.model_config = None
        self.openai_serving_chat = None
        self.openai_serving_embedding = None
        self.openai_serving_score = None
        self.openai_serving_models = None

    def _ensure_model_config(self):
        if self.model_config is None:
            self.model_config = self.engine.model_config
        return self.model_config

    async def _ensure_models(self):
        if self.openai_serving_models is None:
            self._ensure_model_config()
            self.openai_serving_models = OpenAIServingModels(
                self.engine,
                [BaseModelPath(name=self.engine.model_config.served_model_name,
                               model_path=self.engine.model_config.served_model_name)]
            )
        return self.openai_serving_models

    async def _ensure_chat(self):
        if self.openai_serving_chat is None:
            self._ensure_model_config()
            models = await self._ensure_models()

            self.openai_serving_chat = OpenAIServingChat(
                self.engine,
                models,
                self.response_role,
                request_logger=None,
                chat_template=self.chat_template,
                chat_template_content_format=self.chat_template_content_format,
                enable_auto_tools=self.enable_auto_tools,
                tool_parser=self.tool_parser,
                reasoning_parser=self.reasoning_parser if self.enable_reasoning else "",
                enable_prompt_tokens_details=self.enable_prompt_tokens_details,
            )
        return self.openai_serving_chat

    async def _ensure_embedding(self):
        if self.openai_serving_embedding is None:
            self._ensure_model_config()
            models = await self._ensure_models()
            self.openai_serving_embedding = OpenAIServingEmbedding(
                self.engine,
                models,
                request_logger=None,
                chat_template=self.chat_template,
                chat_template_content_format=self.chat_template_content_format,
            )
        return self.openai_serving_embedding

    async def _ensure_score(self):
        if self.openai_serving_score is None:
            self._ensure_model_config()
            models = await self._ensure_models()
            self.openai_serving_score = ServingScores(
                self.engine,
                models,
                request_logger=None,
            )
        return self.openai_serving_score

    async def generate(self, payload: Any):
        await self._ensure_chat()
        result = await self.openai_serving_chat.create_chat_completion(ChatCompletionRequest(**payload), None)

        is_stream = payload.get("stream") is True

        if isinstance(result, ErrorResponse):
            if is_stream:
                logging.error(f"Error during chat completion: {result.message}")
                async def error_generator():
                    import json
                    error_data = {
                        "error": {
                            "message": "Request processing failed",
                            "type": "internal_server_error",
                            "details": str(result.message)
                        }
                    }
                    yield f"data: {json.dumps(error_data)}\n\n"
                    yield "data: [DONE]\n\n"
                return error_generator()

        return result

    async def generate_embeddings(self, payload: Any):
        await self._ensure_embedding()
        try:
            # Validate and convert the payload to an EmbeddingCompletionRequest
            request = EmbeddingCompletionRequest(**payload)
        except (TypeError, ValueError) as e:
            logging.error(f"Invalid payload for EmbeddingCompletionRequest: {e}")
            return ErrorResponse(
                message={"error": "Invalid payload for EmbeddingCompletionRequest", "details": str(e)},
                status_code=400,
            )
        return await self.openai_serving_embedding.create_embedding(request, None)

    async def rerank(self, payload: Any):
        """
        Rerank documents based on their relevance to a query.
        Uses vLLM's native do_rerank method for maximum compatibility and performance.
        """
        await self._ensure_score()
        request = RerankRequest(**payload)
        return await self.openai_serving_score.do_rerank(request, None)

    async def show_available_models(self):
        models = await self._ensure_models()
        return await models.show_available_models()


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
        """
        Controller deployment that handles HTTP routing and calls the backend.

        Args:
            backend: Handle to the Backend deployment
        """
        self.backend = backend
        print("[Controller] Initialized with backend handle")

    @app.post("/v1/chat/completions")
    async def chat(self, request: Request):
        req_obj = await request.json()
        stream = req_obj.get("stream", False)

        if stream:
            # Get the streaming generator from the backend
            r: DeploymentResponseGenerator = self.backend.options(stream=True).generate.remote(req_obj)

            try:
                first_chunk = await r.__anext__()
                if isinstance(first_chunk, str) and "error" in first_chunk:
                    import json
                    error_data = json.loads(first_chunk.replace("data: ", "").strip())
                    return JSONResponse(
                        content=error_data["error"],
                        status_code=400
                    )
            except:
                pass

            return StreamingResponse(
                content=r,
                media_type="text/event-stream"
            )
        else:
            # Handle non-streaming response as before
            result = await self.backend.options(stream=False).generate.remote(req_obj)
            if isinstance(result, ErrorResponse):
                return JSONResponse(content=result.model_dump(), status_code=result.code)
            return JSONResponse(content=result.model_dump())

    @app.post("/v1/embeddings")
    async def embeddings(self, request: Request):
        """Embeddings endpoint for text-embedding models"""
        req_obj = await request.json()
        result = await self.backend.options(stream=False).generate_embeddings.remote(req_obj)
        if isinstance(result, ErrorResponse):
            return JSONResponse(content=result.model_dump(), status_code=result.code)
        return JSONResponse(content=result.model_dump())

    @app.post("/v1/rerank")
    async def rerank(self, request: Request):
        """Rerank endpoint for cross-encoder/reranker models"""
        req_obj = await request.json()
        result = await self.backend.options(stream=False).rerank.remote(req_obj)
        if isinstance(result, ErrorResponse):
            return JSONResponse(content=result.model_dump(), status_code=result.code)
        return JSONResponse(content=result.model_dump())

    @app.get("/v1/models")
    async def models(self, request: Request):
        result = await self.backend.show_available_models.remote()
        return JSONResponse(content=result.model_dump())

    @app.get("/health")
    async def health(self):
        return {"status": "ok"}


def _build_request_router_config(scheduler_config: Dict[str, Any]) -> Optional[RequestRouterConfig]:
    """Build RequestRouterConfig based on scheduler configuration.

    Args:
        scheduler_config: Dictionary containing scheduler configuration with keys:
            - type: SchedulerType (pow2, static_hash, consistent_hash)
            - virtual_nodes: Number of virtual nodes for consistent hash (default: 100)
            - load_factor: Load factor for bounded load (default: 1.25)
            - max_user_messages_for_cache: Number of user messages for cache key (default: 2)

    Returns:
        RequestRouterConfig if custom scheduler is specified, None for default POW2.
    """
    scheduler_type = scheduler_config.get('type', SchedulerType.POW2)

    # Use default POW2 scheduler
    if scheduler_type == SchedulerType.POW2:
        print(f"[app_builder] Using default POW2 scheduler")
        return None

    # Get the custom router class path
    router_class_path = SCHEDULER_CLASS_PATHS.get(scheduler_type)
    if not router_class_path:
        print(f"[app_builder] Unknown scheduler type: {scheduler_type}, using default POW2")
        return None

    # Build kwargs for the custom router
    router_kwargs = {}
    if scheduler_type == SchedulerType.CONSISTENT_HASH:
        router_kwargs = {
            "virtual_nodes_per_replica": scheduler_config.get('virtual_nodes', 100),
            "load_factor": scheduler_config.get('load_factor', 1.25),
            "max_user_messages_for_cache": scheduler_config.get('max_user_messages_for_cache', 2),
        }

    print(f"[app_builder] Using custom scheduler: {scheduler_type}, class: {router_class_path}, kwargs: {router_kwargs}")

    return RequestRouterConfig(
        request_router_class=router_class_path,
        request_router_kwargs=router_kwargs,
    )


def app_builder(args: Dict[str, Any]) -> Application:
    """
    Application builder function that configures and returns the Backend and Controller deployments.
    """
    # Extract configuration sections
    model = args.get('model', {})
    deployment_options = args.get('deployment_options', {})
    engine_args = args.get('engine_args', {})  # vLLM-specific engine arguments

    # Extract backend deployment options
    backend_options = deployment_options.get('backend', {})
    controller_options = deployment_options.get('controller', {})

    # Extract scheduler configuration and build RequestRouterConfig
    scheduler_config = deployment_options.get('scheduler', {})
    request_router_config = _build_request_router_config(scheduler_config)

    # Build backend deployment options
    backend_deploy_options = {
        "max_ongoing_requests": backend_options.get('max_ongoing_requests', 100),
        "num_replicas": backend_options.get('num_replicas', 1),
        "ray_actor_options": {
            "num_cpus": backend_options.get('num_cpus', 1),
            "num_gpus": backend_options.get('num_gpus', 1),
            "memory": backend_options.get('memory', None),
            "resources": backend_options.get('resources', {})
        }
    }

    # Add request_router_config if custom scheduler is specified
    if request_router_config is not None:
        backend_deploy_options["request_router_config"] = request_router_config

    # Configure backend deployment
    backend_deployment = Backend.options(**backend_deploy_options).bind(
        model_registry_type=model.get('registry_type'),
        model_name=model.get('name'),
        model_version=model.get('version'),
        model_file=model.get('file', ''),
        model_task=model.get('task'),
        model_registry_path=model.get('registry_path', ''),
        model_path=model.get('path', ''),
        model_serve_name=model.get('serve_name', ''),
        # Pass all other engine args directly through
        **engine_args
    )

    # Configure controller deployment
    controller_deployment = Controller.options(
        max_ongoing_requests=backend_options.get('max_ongoing_requests', 100) * backend_options.get('num_replicas', 1),
        num_replicas=controller_options.get('num_replicas', 1),
        ray_actor_options={
            "num_cpus": controller_options.get('num_cpus', 0.1),
            "num_gpus": controller_options.get('num_gpus', 0)
        }
    ).bind(
        backend=backend_deployment,
    )

    return controller_deployment
