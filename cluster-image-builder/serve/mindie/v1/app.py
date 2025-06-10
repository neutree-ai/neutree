from math import e
import os
import enum
import logging
import subprocess
import atexit
import multiprocessing
import sys
import asyncio
import time
import json
from typing import Dict, Optional, Any, AsyncGenerator, List
from pathlib import Path
import requests

from fastapi import FastAPI, Request
from starlette.responses import StreamingResponse, JSONResponse
from fastapi.middleware.cors import CORSMiddleware
from starlette_context.plugins import RequestIdPlugin
from starlette_context.middleware import RawContextMiddleware
from fastapi.middleware import Middleware

from openai import AsyncOpenAI
from huggingface_hub import snapshot_download

import bentoml
import ray
from ray import serve
from ray.serve import Application
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator

from serve._replica_scheduler.static_hash_scheduler import StaticHashReplicaScheduler
from serve._replica_scheduler.chwbl_scheduler import ConsistentHashReplicaScheduler

class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"

@serve.deployment(ray_actor_options={"num_cpus": 1, "resources":{"NPU": 1}})
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
        Backend deployment for MindLE inference.
        
        Args:
            model_registry_type: Type of model registry ("bentoml" or "hugging-face")
            model_name: Name of the model in the registry
            model_version: Version of the model
            model_file: Specific model file name (for bentoml)
            model_task: Task type (e.g., "text-generation", "text-embedding", "text-rerank")
            **engine_kwargs: Additional keyword arguments passed directly to AsyncEngineArgs
        """
        # Configure model based on registry
        if model_registry_type == "bentoml":
            model_ref = bentoml.models.get(f"{model_name}:{model_version}")
            model_file = model_ref.info.labels.get("model_file", "")
            model_path = model_ref.path_of(model_file)
        else:
            try:
                os.environ['HF_ENDPOINT'] = 'https://hf-mirror.com'
                local_dir = os.path.join(os.getcwd(), "hf","models", model_name)
                print(f"[Backend] Downloading model {model_name} to {local_dir}")
                model_path = snapshot_download(
                    repo_id=model_name,
                    revision=model_version if model_version != "latest" else None,
                    local_dir=local_dir,
                    )
                print(f"model_path: {model_path}")
            except Exception as e:
                print(f"[Backend] Failed to download model: {e}")
                raise e

        self.model_id = f"{model_name}:{model_version}"
        self.model_task = model_task
        
        task = "generate"
        if model_task == "text-generation":
            task = "generate"

        print(engine_kwargs)
        # --- Mutating model config.
        model_config_path = Path(model_path).joinpath("config.json")
        with open(model_config_path, "r", encoding="utf-8") as f:
            model_config = json.load(f)
        if engine_kwargs is not None:
            for key, value in engine_kwargs.items():
                if key == "dtype":
                    if value == "half":
                        model_config["torch_dtype"] = "float16"
                    elif value == "float":
                        model_config["torch_dtype"] = "float32"
                else:
                    model_config[key] = value
        with open(model_config_path, "w", encoding="utf-8") as f:
            json.dump(model_config, f, indent=4)
        os.chmod(model_config_path, 0o750)
         # --- Mutating model deploy config.          
        install_path = Path(os.getenv("MIES_INSTALL_PATH","/usr/local/Ascend/mindie/latest/mindie-service"))
        with open(install_path.joinpath("conf", "config.json"), "r", encoding="utf-8") as f:
            config = json.load(f)   
        server_config = config["ServerConfig"]
        backend_config = config["BackendConfig"] 
        model_deploy_config = backend_config["ModelDeployConfig"]
        model_config = model_deploy_config["ModelConfig"][0]
        schedule_config = backend_config["ScheduleConfig"]
        
        # inject neutree default config
        server_config["ipAddress"] = "127.0.0.1"
        server_config["managementIpAddress"] = "127.0.0.1"
        # todo random port
        server_config["port"] = 50051
        server_config["httpsEnabled"] = False

        backend_config["multiNodesInferEnabled"] = False
        backend_config["interNodeTLSEnabled"] = False
        backend_config["npuDeviceIds"][0]
        deviceIds = os.environ.get("ASCEND_RT_VISIBLE_DEVICES", "0").split(",")
        backend_config["npuDeviceIds"][0] = [int(id) for id in deviceIds]
       
        model_config["modelName"] = model_name.split("/")[1]
        model_config["modelWeightPath"] = model_path
        world_size = len(os.getenv("ASCEND_RT_VISIBLE_DEVICES", "0").split(","))
        model_config["worldSize"] = world_size
        actor_id = ray.get_runtime_context().get_actor_id()
        config_path = install_path.joinpath(
            "conf", f"config-{actor_id}.json"
        )
        config_str = json.dumps(config, indent=4, ensure_ascii=False)
        with open(
            config_path,
            "w",
            encoding="utf-8",
        ) as f:
            f.write(config_str)
        os.chmod(config_path, 0o640)
         # Start, configure environment variable to indicate the JSON configuration file.
        self.mindie_service_env = os.environ.copy()
        self.mindie_service_env["MIES_CONFIG_JSON_PATH"] = str(config_path)
        self.mindie_service_path = install_path
        self.mindie_service_bin_path = install_path.joinpath("bin", "mindieservice_daemon")
        self.openai_client = None
        self.service_ready = False
        self.management_url = "http://127.0.0.1:1026"
        print(os.environ)
        self.proc = multiprocessing.Process(target=self.run_mindie_service)
        self.proc.start()
        # Register the process to be terminated when the application exits
        atexit.register(self.proc.terminate)
        self._ensure_mindie_service()

    def _ensure_mindie_service(self):
        ""
        if self.openai_client is None:
            self.openai_client = AsyncOpenAI(
                base_url="http://127.0.0.1:50051/v1",
                api_key="xxx",
            )
            while not self.service_ready:
                self.service_ready = self.is_mindie_service_ready()
                time.sleep(1)  # Wait for the service to start
            print("[Backend] MindLE service is ready")

    async def is_mindie_service_ready(self) -> bool:
        """
        Check if MindLE inference is ready.
        """
        try:
            response = requests.get(self.management_url+"/v2/health/ready")
        except Exception as e:
            print(f"[Backend] Failed to check health: {e}")
            return False

        return response.status_code == 200

    def run_mindie_service(self):
        """
        Run MindLE inference.
        """
        try:
            proc = subprocess.Popen(
                self.mindie_service_bin_path,
                env=self.mindie_service_env,
                stdout=sys.stdout,
                stderr=sys.stderr,
                cwd=self.mindie_service_path,
            # preexec_fn=os.setsid,
            # start_new_session=False,
            )
            self.proc = proc

            print("[Backend] Starting MindLE service, process ID:", proc.pid)
            exit_code = proc.wait()
            print(f"[Backend] Process exited with code {exit_code}")
            self.exit_with_code(exit_code)
        except Exception as e:
            print(f"[Backend] Failed to start MindLE service: {e}")
            raise e
        

    async def exit_with_code(self,exit_code):
        """
        Exit the process with the given code.
        """
        ray.actor.exit_actor()

    async def generate(self, payload: Any):
        response =  await self.openai_client.chat.completions.create(**payload)
        stream = payload.get("stream", False)
        if not stream:
            return response
        else:
            return response.__aiter__()

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
            
            async def event_generator():
                async for chunk in r:
                    yield f"data: {chunk.to_json()}\n\n"
                yield "data: [DONE]\n\n"
            
            return StreamingResponse(
                content=event_generator(),
                media_type="text/event-stream"
            )
        else:
            # Handle non-streaming response
            result = await self.backend.options(stream=False).generate.remote(req_obj)
            return JSONResponse(content=result.to_json())

    @app.get("/v1/models")
    async def models(self, request: Request):
        result = await self.backend.show_available_models.remote()
        return JSONResponse(content=result)

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
            "resources": backend_options.get('resources')
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