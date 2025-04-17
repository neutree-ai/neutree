import os
import enum
import logging
import time
import json
from typing import Dict, Optional, Any, AsyncGenerator

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
from vllm.entrypoints.openai.protocol import ChatCompletionRequest, ChatCompletionResponse, ErrorResponse
from vllm.entrypoints.openai.serving_chat import OpenAIServingChat
from vllm.entrypoints.openai.serving_models import BaseModelPath, LoRAModulePath, PromptAdapterPath, OpenAIServingModels
from vllm.engine.metrics import RayPrometheusStatLogger

from serve._replica_scheduler.static_hash_scheduler import StaticHashReplicaScheduler
from serve._replica_scheduler.chwbl_scheduler import ConsistentHashReplicaScheduler

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
                 **engine_kwargs):
        """
        Backend deployment for vLLM inference.
        
        Args:
            model_registry_type: Type of model registry ("bentoml" or "hugging-face")
            model_name: Name of the model in the registry
            model_version: Version of the model
            model_file: Specific model file name (for bentoml)
            model_task: Task type (e.g., "text-generation", "text-embedding")
            **engine_kwargs: Additional keyword arguments passed directly to AsyncEngineArgs
        """
        # Configure model based on registry
        if model_registry_type == "bentoml":
            model_ref = bentoml.models.get(f"{model_name}:{model_version}")
            model_file = model_ref.info.labels.get("model_file", "")
            model_path = model_ref.path_of(model_file)
        else:
            model_path = model_name

        self.model_id = f"{model_name}:{model_version}"
        
        task = "generate"
        if model_task == "text-generation":
            task = "generate"
        elif model_task == "text-embedding":
            task = "embed"

        engine_args = AsyncEngineArgs(
            task=task,
            model=model_path,
            disable_log_stats=False,
            enable_prefix_caching=True,
            **engine_kwargs
        )

        self.engine = AsyncLLMEngine.from_engine_args(engine_args)
        self.model_config = None
        self.openai_serving_chat = None
        self.openai_serving_models = None

        stat_logger = RayPrometheusStatLogger(
            local_interval=0.5,
            labels=dict(model_name=self.engine.engine.model_config.served_model_name),
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
                response_role="assistant",
                request_logger=None,
                chat_template=None,
                chat_template_content_format="auto",
            )
        return self.openai_serving_chat

    async def generate(self, payload: Any):
        await self._ensure_chat()
        return await self.openai_serving_chat.create_chat_completion(ChatCompletionRequest(**payload), None)

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
        num_replicas=backend_options.get('num_replicas', 1),
        ray_actor_options={
            "num_cpus": backend_options.get('num_cpus', 1),
            "num_gpus": backend_options.get('num_gpus', 1)
        }
    ).bind(
        model_registry_type=model.get('registry_type'),
        model_name=model.get('name'),
        model_version=model.get('version'),
        model_file=model.get('file', ''),
        model_task=model.get('task'),
        # Pass all other engine args directly through
        **engine_args
    )
    
    # Configure controller deployment with scheduler config
    controller_deployment = Controller.options(
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