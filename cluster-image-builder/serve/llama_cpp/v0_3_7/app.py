from __future__ import annotations
import os
import enum
import json
import time
import fnmatch
from typing import Dict, Any, AsyncGenerator, Optional

import ray
from ray import serve
from ray.serve import Application
from ray.serve.config import RequestRouterConfig
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

from downloader import get_downloader, build_request_from_model_args

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


@serve.deployment
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

        matched_file = False
        file_pattern = model_file
        for file in os.listdir(model_path):
            if fnmatch.fnmatch(file, file_pattern):
                model_path = os.path.join(model_path, file)
                matched_file = True
                break

        if model_file and not matched_file:
            raise FileNotFoundError(f"Model file matching pattern '{model_file}' not found in path '{model_path}'")

        model_settings["model"] = model_path

        if model_task == "text-embedding":
            # Set embedding flag for embedding tasks
            model_settings["embedding"] = True
        # Store model info
        self.model_id = model_serve_name
        model_settings["model_alias"] = self.model_id

        # Create model settings and model instance
        self.model_settings = ModelSettings(**model_settings)

        # Ensure model can be loaded without errors
        LlamaProxy.load_llama_from_model_settings(self.model_settings)

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
    """
    # Extract configuration sections
    model = args.get('model', {})
    deployment_options = args.get('deployment_options', {})
    model_settings = args.get('engine_args', {})

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
            "num_gpus": backend_options.get('num_gpus', 0),
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
        model_file=model.get('file', ""),
        model_task=model.get('task', ""),
        model_registry_path=model.get('registry_path', ""),
        model_path=model.get('path', ""),
        model_serve_name=model.get('serve_name', ""),
        **model_settings
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
