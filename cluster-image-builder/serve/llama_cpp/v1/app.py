from __future__ import annotations
import os
import enum
import json
import time
from typing import Dict, Any, AsyncGenerator, Optional

import ray
from ray import serve
from ray.serve import Application
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator
from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse
import bentoml
import llama_cpp
from llama_cpp import Llama, LlamaGrammar
from llama_cpp.server.settings import ModelSettings
from llama_cpp.server.model import LlamaProxy
from llama_cpp.server.app import create_chat_completion, create_completion

from fastapi.middleware.cors import CORSMiddleware
from starlette_context.plugins import RequestIdPlugin
from starlette_context.middleware import RawContextMiddleware

class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"

@serve.deployment
class Backend:
    def __init__(self,
                 # Model config parameters
                 model_registry_type: str,
                 model_name: str,
                 model_version: str,
                 model_file: str = "",
                 model_task: str = "",
                 **model_settings):
        """
        Backend deployment for LlamaCpp model inference.
        
        Args:
            model_registry_type: Type of model registry ("bentoml" or "hugging-face")
            model_name: Name of the model in the registry
            model_version: Version of the model
            model_file: Specific model file name (for hugging-face)
            model_task: Task type (e.g., "text-generation", "text-embedding")
            **model_settings: Additional model settings for llama-cpp
        """
       # Configure model based on registry
        if model_registry_type == "bentoml":
            model_ref = bentoml.models.get(f"{model_name}:{model_version}")
            model_file = model_ref.info.labels.get("model_file", "")
            model_path = model_ref.path_of(model_file)
            # Override model path in settings
            model_settings["model"] = model_path
        elif model_registry_type == "hugging-face":
            # Use the hf_model_repo_id from settings or default to model_name
            model_settings["hf_model_repo_id"] = model_name
            model_settings["model"] = model_file
        else:
            raise ValueError(f"Unsupported MODEL_REGISTRY_TYPE: {model_registry_type}")
        
        if model_task == "text-embedding":
            # Set embedding flag for embedding tasks
            model_settings["embedding"] = True

        # Create model settings and model instance
        self.model_settings = ModelSettings(**model_settings)
        
        # Store model info
        self.model_id = f"{model_name}:{model_version}"

    async def generate(self, payload: Any):
        llama = LlamaProxy.load_llama_from_model_settings(self.model_settings)
        if "messages" in payload:
            # Chat completion
            response = llama.create_chat_completion(**payload)
            return response
        else:
            # Regular completion
            response = llama.create_completion(**payload)
            return response
    
    async def generate_embeddings(self, payload: Any):
        llama = LlamaProxy.load_llama_from_model_settings(self.model_settings)
        response = llama.create_embedding(**payload)
        return response
    
    async def show_available_models(self) -> Dict[str, Any]:
        """Return a list of available models"""
        return {
            "object": "list",
            "data": [{
                "id": self.model_id,
                "object": "model",
                "permissions": [],
                "owned_by": "local",
            }]
        }

app = FastAPI()
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)
app.add_middleware(RawContextMiddleware, plugins=(RequestIdPlugin(),))

@serve.deployment
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
        """Chat completions endpoint"""
        req_obj = await request.json()
        stream = req_obj.get("stream", False)
        
        if stream:
            # Get the streaming generator from the backend
            r: DeploymentResponseGenerator = self.backend.options(stream=True).generate.remote(req_obj)
            
            async def event_generator():
                async for chunk in r:
                    yield f"data: {json.dumps(chunk)}\n\n"
                yield "data: [DONE]\n\n"
            
            return StreamingResponse(
                content=event_generator(),
                media_type="text/event-stream"
            )
        else:
            # Handle non-streaming response
            result = await self.backend.options(stream=False).generate.remote(req_obj)
            return JSONResponse(content=result)

    @app.post("/v1/completions")
    async def completions(self, request: Request):
        """Text completions endpoint"""
        # This is identical to the chat endpoint since the Backend handles format differences
        return await self.chat(request)

    @app.get("/v1/models")
    async def models(self, request: Request):
        """Available models endpoint"""
        result = await self.backend.show_available_models.remote()
        return JSONResponse(content=result)

    @app.post("/v1/embeddings")
    async def embeddings(self, request: Request):
        """Embeddings endpoint"""
        req_obj = await request.json()
        result = await self.backend.options(stream=False).generate_embeddings.remote(req_obj)
        return JSONResponse(content=result)

    @app.get("/health")
    async def health(self):
        """Health check endpoint"""
        return {"status": "ok"}

def app_builder(args: Dict[str, Any]) -> Application:
    """
    Application builder function that configures and returns the Backend and Controller deployments.
    
    Args:
        args: A dictionary containing all the configuration parameters, expected to have
              'model', 'deployment_options', and 'model_settings' keys.
    """
    # Extract configuration sections
    model = args.get('model', {})
    deployment_options = args.get('deployment_options', {})
    model_settings = args.get('model_settings', {})
    
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
            "num_gpus": backend_options.get('num_gpus', 0)
        }
    ).bind(
        model_registry_type=model.get('registry_type'),
        model_name=model.get('name'),
        model_version=model.get('version'),
        model_file=model.get('file', ""),
        model_task=model.get('task', ""),
        **model_settings
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
