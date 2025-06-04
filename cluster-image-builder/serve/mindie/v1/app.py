from math import e
import os
import enum
from re import S
import subprocess
import multiprocessing
import sys
from textwrap import indent
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
from serve._util.port import get_available_port

class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"

@serve.deployment(
    graceful_shutdown_timeout_s=30,
    graceful_shutdown_wait_loop_s=2,
    health_check_period_s=5,
    health_check_timeout_s=5,
    ray_actor_options={"num_cpus": 1, "resources":{"NPU": 1}}
    )
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
            model_path = snapshot_download(
                    repo_id=model_name,
                    revision=model_version if model_version != "latest" else None,
            )

        self.model_id = f"{model_name}:{model_version}"
        self.model_path = model_path
        self.model_task = model_task
        self.model_name = model_name

        # Prepare the MindIE config
        # --- Mutating model config.
        model_config_path = Path(self.model_path).joinpath("config.json")
        with open(model_config_path, "r", encoding="utf-8") as f:
            model_config = json.load(f)
        if engine_kwargs and engine_kwargs["dtype"]:
            if engine_kwargs["dtype"] == "half":
                model_config["torch_dtype"] = "float16"
            elif engine_kwargs["dtype"] == "float":
                model_config["torch_dtype"] = "float32"
        with open(model_config_path, "w", encoding="utf-8") as f:
            json.dump(model_config, f, indent=4)
        os.chmod(model_config_path, 0o750)

        self._ascend_home = Path(os.getenv("ASCEND_HOME","/usr/local/Ascend") )
        self._mindie_home = self._ascend_home.joinpath("mindie","latest")
        self._mindie_service_home = self._mindie_home.joinpath("mindie-service")
        
        # --- Mutating MindIE config.          
        with open(self._mindie_service_home.joinpath("conf", "config.json"), "r", encoding="utf-8") as f:
            config = json.load(f)   
        server_config = config["ServerConfig"]
        backend_config = config["BackendConfig"] 
        model_deploy_config = backend_config["ModelDeployConfig"]
        model_config = model_deploy_config["ModelConfig"][0]
        schedule_config = backend_config["ScheduleConfig"]
        if engine_kwargs:
            for key, value in engine_kwargs.items():
                 if key in model_deploy_config:
                     model_deploy_config[key] = value
                 if key in schedule_config:
                     schedule_config[key] = value
                 if key in model_config:
                     model_config[key] = value
        
        # --- Mutating MindIE default config. 
        self._server_port = get_available_port()
        self._mgmt_server_port = self._server_port
        self._mgmt_metrics_port = self._server_port
        self._server_url = f"http://127.0.0.1:{self._server_port}"
        self._mgmt_url = f"http://127.0.0.1:{self._mgmt_server_port}"

        server_config["ipAddress"] = "127.0.0.1"
        server_config["managementIpAddress"] = "127.0.0.1"
        server_config["port"] = self._server_port
        server_config["managementPort"] = self._mgmt_server_port
        server_config["metricsPort"] = self._mgmt_metrics_port
        server_config["httpsEnabled"] = False

        backend_config["multiNodesInferEnabled"] = False
        backend_config["interNodeTLSEnabled"] = False
        deviceIds = os.environ.get("ASCEND_RT_VISIBLE_DEVICES", "0").split(",")
        world_size = len(deviceIds)
        backend_config["npuDeviceIds"][0] = [int(id) for id in deviceIds]
        # todo tokenizerProcessNumber config
        model_config["modelName"] = self.model_name
        if len(self.model_name.split("/")) > 1:
            model_config["modelName"] = self.model_name.split("/")[len(self.model_name.split("/"))-1]
        
        model_config["modelWeightPath"] = self.model_path
        model_config["worldSize"] = world_size
        # default to use prefix cache
        model_config["plugin_params"] = json.dumps(
                    {
                        "plugin_type": "prefix_cache",
                    }
        )
        actor_id = ray.get_runtime_context().get_actor_id()
        self._service_config_path = self._mindie_service_home.joinpath(
            "conf", f"config-{actor_id}.json"
        )
        config_str = json.dumps(config, indent=4, ensure_ascii=False)
        with open(
            self._service_config_path,
            "w",
            encoding="utf-8",
        ) as f:
            f.write(config_str)
        os.chmod(self._service_config_path, 0o640)

        self._service_ready = False
        self._service_initialized = False
        self._openai_client = None        
        self._init_service()

    def __del__(self):
        """
        Clean up resources when the object is deleted.
        """
        print("[Backend] Cleaning up resources")
        if self._service_config_path:
            os.remove(self._service_config_path)

        if self._stop_mindie_service():
            print("[Backend] MindLE service stopped")
           
    def _stop_mindie_service(self):
        """
        Stop MindIE inference.
        """
        try:
            response = requests.get(self._mgmt_url+"/stopService")
        except Exception as e:
            print(f"[Backend] Failed to stop MindLE service: {e}")
            return False

        return response.status_code == 200

    def _init_service(self):
        ""
        if self._openai_client is None:
            proc = multiprocessing.Process(target=self._run_service)
            proc.start()
            while not self._service_ready:
                if not proc.is_alive():
                    raise RuntimeError("MindLE service failed to start")
                self._service_ready = self._is_mindie_service_ready()
                time.sleep(1)  # Wait for the service to start
            print("[Backend] MindLE service is ready")
            self._openai_client = AsyncOpenAI(
                base_url=f"{self._server_url}/v1",
                api_key="mindie",
            )
            self._service_initialized = True      

    def _is_mindie_service_ready(self) -> bool:
        """
        Check if MindIE inference is ready.
        """
        try:
            response = requests.get(self._mgmt_url+"/v2/health/ready")
        except Exception as e:
            print(f"[Backend] Failed to check MindLE service health: {e}")
            return False

        return response.status_code == 200

    def _run_service(self):
        """
        Run MindIE Server.
        """

        mindie_service_env = os.environ.copy()
        mindie_service_env["MIES_CONFIG_JSON_PATH"] = str(self._service_config_path)
        # del ASCEND_RT_VISIBLE_DEVICES env to avoid torch npu device set failed. 
        mindie_service_env.pop("ASCEND_RT_VISIBLE_DEVICES","")

        script_paths = [
            self._ascend_home.joinpath("ascend-toolkit","set_env.sh"),
            self._ascend_home.joinpath("nnal","atb", "set_env.sh"),
            self._ascend_home.joinpath("atb-models","set_env.sh"),
            self._mindie_home.joinpath("mindie-rt", "set_env.sh"),
            self._mindie_home.joinpath("mindie-torch", "set_env.sh"),
            self._mindie_home.joinpath("mindie-service", "set_env.sh"),
            self._mindie_home.joinpath("mindie-llm", "set_env.sh"),
        ]
        init_mindie_env_shell = " && ".join([f"source {path}" for path in script_paths]) 
        command = f"{init_mindie_env_shell} && {self._mindie_service_home.joinpath('bin', 'mindieservice_daemon')}"
        print("[Backend] Starting MindLE service, command:", command)
        proc = subprocess.Popen(
                ["bash", "-c", command],
                env=mindie_service_env,
                stdout=sys.stdout,
                stderr=sys.stderr,
        )

        print("[Backend] Starting MindLE service, process ID:", proc.pid)
        exit_code = proc.wait()
        print(f"[Backend] Process exited with code {exit_code}")
        sys.exit(exit_code)
        
    def check_health(self):
        """
        Check the health of the backend.
        """
        if self._service_initialized is False:
            return
        
        if self._is_mindie_service_ready() is False:
            raise RuntimeError("MindLE service is not ready")
        
    async def generate(self, payload: Any):
        response = await self._openai_client.chat.completions.create(**payload)
        stream = payload.get("stream", False)
        if not stream:
            return response
        else:
            return aiter(response)

    async def show_available_models(self) -> Dict[str, Any]:
        models = self._openai_client.models.list()
        datas = []
        async for model in models:
            datas.append({
                "id": model.id,
                "object": "model",
                "permissions": [],
                "owned_by": "local",
            })
        """Return a list of available models"""
        return {
            "object": "list",
            "data": datas,
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
                    yield f"data: {chunk.to_json(indent=None)}\n\n"
                yield "data: [DONE]\n\n"
            
            return StreamingResponse(
                content=event_generator(),
                media_type="text/event-stream"
            )
        else:
            # Handle non-streaming response
            result = await self.backend.options(stream=False).generate.remote(req_obj)
            return JSONResponse(content=result.to_json(indent=None))

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
            "memory": backend_options.get('memory', 0),
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
        }
    ).bind(
        backend=backend_deployment,
        scheduler_type=scheduler_type,
        virtual_nodes=virtual_nodes,
        load_factor=load_factor
    )
    
    return controller_deployment