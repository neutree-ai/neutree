import os
import enum
import logging
import time
import json
from turtle import down
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
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator

from vllm.engine.arg_utils import AsyncEngineArgs
from vllm.engine.async_llm_engine import AsyncLLMEngine
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
from vllm.entrypoints.openai.serving_models import BaseModelPath, LoRAModulePath, PromptAdapterPath, OpenAIServingModels
from vllm.engine.metrics import RayPrometheusStatLogger

from serve._replica_scheduler.static_hash_scheduler import StaticHashReplicaScheduler
from serve._replica_scheduler.chwbl_scheduler import ConsistentHashReplicaScheduler

from downloader import get_downloader, build_request_from_model_args

class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"

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

        self.engine = AsyncLLMEngine.from_engine_args(engine_args)
        self.model_config = None
        self.openai_serving_chat = None
        self.openai_serving_embedding = None
        self.openai_serving_score = None
        self.openai_serving_models = None

        ctx = serve.get_replica_context()
        labels = {
            "deployment":  ctx.deployment,
            "replica":     ctx.replica_tag,
            "model_name":  self.engine.engine.model_config.served_model_name,
        }

        if hasattr(ctx, "app_name"):
            labels["application"] = ctx.app_name

        stat_logger = RayPrometheusStatLogger(
            local_interval=0.5,
            labels=labels,
            vllm_config=self.engine.engine.vllm_config)
        self.engine.add_logger("ray", stat_logger)

    async def _ensure_model_config(self):
        if self.model_config is None:
            self.model_config = await self.engine.get_model_config()
        return self.model_config

    async def _ensure_models(self):
        if self.openai_serving_models is None:
            model_config = await self._ensure_model_config()
            self.openai_serving_models = OpenAIServingModels(
                self.engine,
                model_config,
                [BaseModelPath(name=self.engine.engine.model_config.served_model_name, model_path=self.engine.engine.model_config.served_model_name)]
            )
        return self.openai_serving_models

    async def _ensure_chat(self):
        if self.openai_serving_chat is None:
            model_config = await self._ensure_model_config()
            models = await self._ensure_models()

            self.openai_serving_chat = OpenAIServingChat(
                self.engine,
                model_config,
                models,
                response_role=self.response_role,
                request_logger=None,
                chat_template=self.chat_template,
                chat_template_content_format=self.chat_template_content_format,
                enable_auto_tools=self.enable_auto_tools,
                tool_parser=self.tool_parser,
                enable_reasoning=self.enable_reasoning,
                reasoning_parser=self.reasoning_parser,
                enable_prompt_tokens_details=self.enable_prompt_tokens_details,
            )
        return self.openai_serving_chat

    async def _ensure_embedding(self):
        if self.openai_serving_embedding is None:
            model_config = await self._ensure_model_config()
            models = await self._ensure_models()
            self.openai_serving_embedding = OpenAIServingEmbedding(
                self.engine,
                model_config,
                models,
                request_logger=None,
                chat_template=self.chat_template,
                chat_template_content_format=self.chat_template_content_format,
            )
        return self.openai_serving_embedding

    async def _ensure_score(self):
        if self.openai_serving_score is None:
            model_config = await self._ensure_model_config()
            models = await self._ensure_models()
            self.openai_serving_score = ServingScores(
                self.engine,
                model_config,
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
    def __init__(self,
                 backend: DeploymentHandle,
                 scheduler_type: str = SchedulerType.POW2,
                 virtual_nodes: int = 100,
                 load_factor: float = 1.25):
        """
        Controller deployment that handles HTTP routing and calls the backend.

        Args:
            backend: Handle to the Backend deployment
            scheduler_type: Type of scheduler to use
            virtual_nodes: Number of virtual nodes for consistent hash scheduler
            load_factor: Load factor for consistent hash scheduler
        """
        self.backend = backend
        self.patched = False

        # Setup router with custom scheduler if specified
        handle = self.backend
        if not handle.is_initialized:
            handle._init()

        # Only modify the scheduler if necessary
        if scheduler_type != SchedulerType.POW2:
            try:
                router = handle._router._asyncio_router
                original_scheduler = router._replica_scheduler

                if scheduler_type == SchedulerType.STATIC_HASH:
                    from serve._replica_scheduler.static_hash_scheduler import StaticHashReplicaScheduler
                    new_scheduler = StaticHashReplicaScheduler()
                elif scheduler_type == SchedulerType.CONSISTENT_HASH:
                    from serve._replica_scheduler.chwbl_scheduler import ConsistentHashReplicaScheduler
                    new_scheduler = ConsistentHashReplicaScheduler(
                        virtual_nodes_per_replica=virtual_nodes,
                        load_factor=load_factor
                    )

                new_scheduler.update_replicas(list(original_scheduler.curr_replicas.values()))
                router._replica_scheduler = new_scheduler
                print(f"[Controller] Replaced scheduler with {scheduler_type}")
            except Exception as e:
                print(f"[Controller] Failed to replace scheduler: {e}")
                print("[Controller] Using default scheduler")
        else:
            print("[Controller] Using POW2 scheduler")

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

    # Extract scheduler configuration
    scheduler_config = deployment_options.get('scheduler', {})
    scheduler_type = scheduler_config.get('type', SchedulerType.POW2)
    virtual_nodes = scheduler_config.get('virtual_nodes', 100)
    load_factor = scheduler_config.get('load_factor', 1.25)

    # Configure backend deployment
    backend_deployment = Backend.options(
        max_ongoing_requests=backend_options.get('max_ongoing_requests', 100),
        num_replicas=backend_options.get('num_replicas', 1),
        ray_actor_options={
            "num_cpus": backend_options.get('num_cpus', 1),
            "num_gpus": backend_options.get('num_gpus', 1),
            "memory": backend_options.get('memory', None),
            "resources": backend_options.get('resources', {})
        }
    ).bind(
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

    # Configure controller deployment with scheduler config
    controller_deployment = Controller.options(
        max_ongoing_requests=backend_options.get('max_ongoing_requests', 100) * backend_options.get('num_replicas', 1),
        num_replicas=controller_options.get('num_replicas', 1),
        ray_actor_options={
            "num_cpus": controller_options.get('num_cpus', 0.1),
            "num_gpus": controller_options.get('num_gpus', 0)
        }
    ).bind(
        backend=backend_deployment,
        scheduler_type=scheduler_type,
        virtual_nodes=virtual_nodes,
        load_factor=load_factor
    )

    return controller_deployment