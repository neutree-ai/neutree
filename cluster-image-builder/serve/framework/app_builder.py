"""Generic app_builder for the Neutree serve framework.

This module builds a Ray Serve Application graph using a closure-based
BackendWrapper pattern.  The ``_Backend`` class is defined **inside**
``app_builder()`` so that cloudpickle serialises it as bytecode (not a
module reference).  This means the engine container does *not* need any
framework code installed — the class travels in the pickle payload.

At init time the wrapper dynamically imports the real engine-specific
Backend class from the engine container's Python environment via
``importlib``.  Return values are serialised to plain dicts before
crossing the container boundary back to the Controller.
"""

import enum
import logging
from typing import Dict, Optional, Any

from ray import serve
from ray.serve import Application
from ray.serve.config import RequestRouterConfig

from serve.framework.controller import Controller

logger = logging.getLogger("ray.serve")


# ---------------------------------------------------------------------------
# Scheduler helpers (moved from per-engine app.py — identical across engines)
# ---------------------------------------------------------------------------

class SchedulerType(str, enum.Enum):
    POW2 = "pow2"
    STATIC_HASH = "static_hash"
    CONSISTENT_HASH = "consistent_hash"


# Class paths resolved inside the engine container at scheduling time.
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

    if scheduler_type == SchedulerType.POW2:
        print(f"[app_builder] Using default POW2 scheduler")
        return None

    router_class_path = SCHEDULER_CLASS_PATHS.get(scheduler_type)
    if not router_class_path:
        print(f"[app_builder] Unknown scheduler type: {scheduler_type}, using default POW2")
        return None

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


# ---------------------------------------------------------------------------
# Application builder
# ---------------------------------------------------------------------------

def app_builder(args: Dict[str, Any]) -> Application:
    """Build a Ray Serve Application with a decoupled Backend wrapper.

    The ``_Backend`` class defined below is a **closure** — cloudpickle
    serialises its bytecode together with the captured ``backend_cls_path``
    variable.  The engine container deserialises and runs it without needing
    any framework code on its filesystem.
    """

    # ---- extract configuration sections ----
    backend_cls_path = args.get('backend_class', '')
    backend_container = args.get('backend_container')

    model = args.get('model', {})
    deployment_options = args.get('deployment_options', {})
    engine_args = args.get('engine_args', {})

    backend_options = deployment_options.get('backend', {})
    controller_options = deployment_options.get('controller', {})
    scheduler_config = deployment_options.get('scheduler', {})

    request_router_config = _build_request_router_config(scheduler_config)

    # ---- closure-based Backend wrapper ----

    @serve.deployment
    class _Backend:
        """Thin wrapper that dynamically loads the real engine Backend.

        Defined as a local class so cloudpickle serialises bytecode, not a
        module reference.  The engine container can deserialise this without
        having the framework package installed.
        """

        def __init__(self, _backend_cls_path: str, **kwargs):
            import importlib
            mod_path, cls_name = _backend_cls_path.rsplit(":", 1)
            mod = importlib.import_module(mod_path)
            cls = getattr(mod, cls_name)
            self._impl = cls(**kwargs)
            print(f"[_Backend] Loaded real backend from {_backend_cls_path}")

        # -- serialisation boundary helpers --

        @staticmethod
        def _serialize(result):
            """Convert engine-specific types to a plain dict for cross-container transport."""
            if isinstance(result, dict):
                return {"body": result, "status_code": None}
            if hasattr(result, 'model_dump'):
                code = getattr(result, 'code', None)
                return {"body": result.model_dump(), "status_code": code}
            return {"body": result, "status_code": None}

        # -- delegated methods --

        async def generate(self, payload):
            result = await self._impl.generate(payload)
            # Streaming returns an AsyncGenerator of SSE strings — pass through.
            if hasattr(result, '__aiter__'):
                return result
            return self._serialize(result)

        async def generate_embeddings(self, payload):
            result = await self._impl.generate_embeddings(payload)
            return self._serialize(result)

        async def rerank(self, payload):
            result = await self._impl.rerank(payload)
            return self._serialize(result)

        async def show_available_models(self):
            result = await self._impl.show_available_models()
            return self._serialize(result)

    # ---- deployment configuration ----

    backend_deploy_options = {
        "max_ongoing_requests": backend_options.get('max_ongoing_requests', 100),
        "num_replicas": backend_options.get('num_replicas', 1),
        "ray_actor_options": {
            "num_cpus": backend_options.get('num_cpus', 1),
            "num_gpus": backend_options.get('num_gpus', 1),
            "memory": backend_options.get('memory', None),
            "resources": backend_options.get('resources', {}),
        },
    }

    if request_router_config is not None:
        backend_deploy_options["request_router_config"] = request_router_config

    # Override Backend runtime_env.container to run in the engine image.
    if backend_container:
        backend_deploy_options["ray_actor_options"]["runtime_env"] = {
            "container": backend_container,
        }

    # ---- bind ----

    backend_deployment = _Backend.options(**backend_deploy_options).bind(
        _backend_cls_path=backend_cls_path,
        model_registry_type=model.get('registry_type'),
        model_name=model.get('name'),
        model_version=model.get('version'),
        model_file=model.get('file', ''),
        model_task=model.get('task'),
        model_registry_path=model.get('registry_path', ''),
        model_path=model.get('path', ''),
        model_serve_name=model.get('serve_name', ''),
        **engine_args,
    )

    controller_deployment = Controller.options(
        max_ongoing_requests=backend_options.get('max_ongoing_requests', 100) * backend_options.get('num_replicas', 1),
        num_replicas=controller_options.get('num_replicas', 1),
        ray_actor_options={
            "num_cpus": controller_options.get('num_cpus', 0.1),
            "num_gpus": controller_options.get('num_gpus', 0),
        },
    ).bind(backend=backend_deployment)

    return controller_deployment
