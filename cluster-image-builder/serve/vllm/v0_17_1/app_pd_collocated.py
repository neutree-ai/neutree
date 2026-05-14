"""Phase 0 Demo — PD same-host collocated app builder for vLLM v0.17.1.

Architecture (1P1D hardcoded for Demo; xP1D parameterization lands in MVP PR-10):

    RayServeApplication
        |--- PDIngress (FastAPI, 0.1 CPU, 0 GPU)
        |       single DeploymentHandle -> PDCollocatedBackend
        |
        '--- PDCollocatedBackend (replicas = num_replicas from plan)
                |---  placement_group (STRICT_PACK, 2 bundles)
                |---  PrefillActor (1 GPU bundle, vLLM AsyncLLM, NIXL kv_role=kv_producer)
                '---  DecodeActor  (1 GPU bundle, vLLM AsyncLLM, NIXL kv_role=kv_consumer)

Per-request flow inside PDCollocatedBackend.pd_chat:
    1. prefill_at(req): vLLM produces max_tokens=1, returns kv_transfer_params dict
    2. decode_at(req, kv_params): vLLM consumes kv_transfer_params and streams the
       rest of the completion (zero-copy KV via NIXL cuda_ipc on same host)

Demo intentionally:
- hardcodes 1 prefill + 1 decode (xP1D parameterization in MVP)
- skips check_health fan-out (P+D same-fate in MVP)
- skips actor-indexed RPC method (`prefill_at(req, idx)`) — single index
- builds independent PrefillActor / DecodeActor classes from scratch (per
  user direction) instead of inheriting the monolithic Backend

Demo design: .claude/knowledge/neutree-pd-same-host-phase1-detailed/00-overview-and-pr-plan.md §3.0
"""
import json
import logging
import os
import uuid
from typing import Any, Dict, List, Optional

from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse

import ray
from ray import serve
from ray.serve import Application
from ray.serve.handle import DeploymentHandle
from ray.util.placement_group import placement_group, PlacementGroupSchedulingStrategy

from vllm.engine.arg_utils import AsyncEngineArgs
from vllm.v1.engine.async_llm import AsyncLLM
from vllm.entrypoints.openai.chat_completion.protocol import ChatCompletionRequest
from vllm.entrypoints.openai.engine.protocol import ErrorResponse
from vllm.entrypoints.openai.chat_completion.serving import OpenAIServingChat
from vllm.entrypoints.openai.models.protocol import BaseModelPath
from vllm.entrypoints.openai.models.serving import OpenAIServingModels

from downloader import get_downloader, build_request_from_model_args
from serve._metrics.ray_stat_logger import NeutreeRayStatLogger
from serve._utils import coerce_args, filter_engine_args
from serve._utils.runtime_env import build_backend_runtime_env
from serve._ingress_router.pd_ingress import PDIngress


log = logging.getLogger("pd_collocated")


# ----------------------------- helpers -----------------------------


def _download_model(model_args: Dict[str, Any]) -> None:
    """Download the model artifacts using the standard Neutree downloader.
    Called once per actor process.
    """
    backend, dl_req = build_request_from_model_args({
        "registry_type": model_args.get("registry_type"),
        "name": model_args.get("name"),
        "version": model_args.get("version"),
        "file": model_args.get("file", ""),
        "task": model_args.get("task", ""),
        "registry_path": model_args.get("registry_path", ""),
        "path": model_args.get("path", ""),
    })
    dl = get_downloader(backend)
    log.info(f"[pd_collocated] downloading model via {backend}: {dl_req.source} -> {dl_req.dest}")
    dl.download(
        dl_req.source, dl_req.dest, credentials=dl_req.credentials,
        recursive=dl_req.recursive, overwrite=dl_req.overwrite,
        retries=dl_req.retries, timeout=dl_req.timeout, metadata=dl_req.metadata,
    )


def _build_engine(
    model_args: Dict[str, Any],
    engine_kwargs: Dict[str, Any],
    kv_transfer_config: Dict[str, Any],
) -> AsyncLLM:
    """Construct a vLLM AsyncLLM with NIXL kv_transfer_config baked in.
    The role (kv_producer / kv_consumer) is supplied by the caller.
    """
    args = dict(
        task="generate",
        model=model_args.get("path") or model_args.get("name"),
        served_model_name=model_args.get("serve_name") or model_args.get("name"),
        disable_log_stats=False,
        kv_transfer_config=kv_transfer_config,
    )
    args.update(engine_kwargs or {})
    coerce_args(args, AsyncEngineArgs)
    filter_engine_args(args, AsyncEngineArgs)
    engine_args = AsyncEngineArgs(**args)
    return AsyncLLM.from_engine_args(engine_args, stat_loggers=[NeutreeRayStatLogger])


# ----------------------------- actors -----------------------------


@ray.remote
class PrefillActor:
    """Prefill-only vLLM engine. Produces KV blocks and metadata, never decodes
    beyond the first token. Bound into a STRICT_PACK placement_group bundle
    that is on the same host as the DecodeActor for NIXL cuda_ipc zero-copy.
    """

    def __init__(self, model_args: Dict[str, Any], engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any]):
        _download_model(model_args)
        kv_cfg = {
            "kv_connector": "NixlConnector",
            "kv_role": "kv_producer",
            "kv_buffer_size": kv_extra.get("buffer_size", 5_000_000_000),
        }
        self.engine = _build_engine(model_args, engine_kwargs, kv_cfg)
        self.model_id = model_args.get("serve_name") or model_args.get("name")
        log.info("[PrefillActor] ready")

    async def prefill_at(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        """Run prefill (max_tokens=1) and return kv_transfer_params metadata.

        Phase 0 Demo: we cap max_tokens=1 and read kv_transfer_params from the
        vLLM response (vLLM 0.17.1 surfaces it through the chat completion
        choice). The dict is engine-private but plain-JSON-serializable, which
        is the V6 assumption the architecture review validates.
        """
        prefill_payload = dict(payload)
        prefill_payload["max_tokens"] = 1
        prefill_payload["stream"] = False
        # Tag request_id so downstream NIXL handshake can correlate sides.
        prefill_payload.setdefault("request_id", uuid.uuid4().hex)

        models = OpenAIServingModels(
            self.engine,
            [BaseModelPath(name=self.engine.model_config.served_model_name,
                           model_path=self.engine.model_config.served_model_name)],
        )
        chat = OpenAIServingChat(
            self.engine, models, "assistant", request_logger=None,
            chat_template=None, chat_template_content_format="auto",
            enable_auto_tools=False, tool_parser=None,
            reasoning_parser=None, enable_prompt_tokens_details=False,
        )
        resp = await chat.create_chat_completion(ChatCompletionRequest(**prefill_payload), None)
        if isinstance(resp, ErrorResponse):
            return {"error": resp.error.message, "request_id": prefill_payload["request_id"]}

        # kv_transfer_params is surfaced via the vLLM response; for Demo we
        # extract a plain dict round-trippable through Ray.
        kv_params = getattr(resp, "kv_transfer_params", None)
        if kv_params is None and hasattr(resp, "model_dump"):
            kv_params = resp.model_dump().get("kv_transfer_params")
        return {
            "kv_transfer_params": kv_params,
            "request_id": prefill_payload["request_id"],
        }


@ray.remote
class DecodeActor:
    """Decode-only vLLM engine. Pulls KV via NIXL from a same-host PrefillActor."""

    def __init__(self, model_args: Dict[str, Any], engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any]):
        _download_model(model_args)
        kv_cfg = {
            "kv_connector": "NixlConnector",
            "kv_role": "kv_consumer",
            "kv_buffer_size": kv_extra.get("buffer_size", 5_000_000_000),
        }
        self.engine = _build_engine(model_args, engine_kwargs, kv_cfg)
        self.model_id = model_args.get("serve_name") or model_args.get("name")
        log.info("[DecodeActor] ready")

    async def decode_at(self, payload: Dict[str, Any], kv_params: Optional[Dict[str, Any]]):
        """Stream / return decode chunks, consuming KV blocks transferred from
        the paired PrefillActor via NIXL cuda_ipc (zero-copy on same host).
        """
        decode_payload = dict(payload)
        if kv_params is not None:
            decode_payload["kv_transfer_params"] = kv_params

        models = OpenAIServingModels(
            self.engine,
            [BaseModelPath(name=self.engine.model_config.served_model_name,
                           model_path=self.engine.model_config.served_model_name)],
        )
        chat = OpenAIServingChat(
            self.engine, models, "assistant", request_logger=None,
            chat_template=None, chat_template_content_format="auto",
            enable_auto_tools=False, tool_parser=None,
            reasoning_parser=None, enable_prompt_tokens_details=False,
        )
        result = await chat.create_chat_completion(ChatCompletionRequest(**decode_payload), None)
        if isinstance(result, ErrorResponse):
            return {"error": result.error.message}
        return result.model_dump() if hasattr(result, "model_dump") else result


# --------------------------- PD backend ---------------------------


@serve.deployment(ray_actor_options={"num_cpus": 0.1, "num_gpus": 0})
class PDCollocatedBackend:
    """Owns a STRICT_PACK placement_group with 1 prefill + 1 decode bundle.
    Phase 0 Demo hardcodes 1P1D; MVP PR-10 makes it xP1D parameterized.
    """

    def __init__(self, model_args: Dict[str, Any], engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any], gpu_per_actor: float = 1.0):
        bundles = [
            {"CPU": 1, "GPU": gpu_per_actor},  # prefill
            {"CPU": 1, "GPU": gpu_per_actor},  # decode
        ]
        self.pg = placement_group(bundles, strategy="STRICT_PACK")
        ray.get(self.pg.ready())
        log.info(f"[PDCollocatedBackend] placement_group ready: {self.pg.bundle_specs}")

        sched_prefill = PlacementGroupSchedulingStrategy(
            placement_group=self.pg, placement_group_bundle_index=0,
        )
        sched_decode = PlacementGroupSchedulingStrategy(
            placement_group=self.pg, placement_group_bundle_index=1,
        )
        self.prefill = PrefillActor.options(
            num_cpus=1, num_gpus=gpu_per_actor, scheduling_strategy=sched_prefill,
        ).remote(model_args, engine_kwargs, kv_extra)
        self.decode = DecodeActor.options(
            num_cpus=1, num_gpus=gpu_per_actor, scheduling_strategy=sched_decode,
        ).remote(model_args, engine_kwargs, kv_extra)

    async def pd_chat(self, payload: Dict[str, Any]):
        # Step 1: prefill -> kv_transfer_params
        kv_resp = await self.prefill.prefill_at.remote(payload)
        if "error" in kv_resp:
            return kv_resp
        kv_params = kv_resp.get("kv_transfer_params")

        # Step 2: decode (consumes KV via NIXL cuda_ipc same-host)
        decoded = await self.decode.decode_at.remote(payload, kv_params)
        return decoded

    async def show_available_models(self):
        # Pull the model id from the prefill actor (decode would also work).
        return {
            "object": "list",
            "data": [{"id": await self.prefill.__ray_call__.remote(lambda a: a.model_id),
                      "object": "model"}],
        }


# --------------------------- app_builder ---------------------------


def app_builder(args: Dict[str, Any]) -> Application:
    """Phase 0 Demo app_builder.

    Args contract (matches ray_pd_branch.SerializePlan):
        args = {
            "model": {registry_type, name, version, file, task, registry_path, path, serve_name},
            "deployment_options": {...},
            "backend_container": {...},     # optional; runtime_env builder
            "plan": {
                "replicas": [{id, pools, affinity}, ...],
                "kv_config": {transfer: {connector, extra}, ...},
            },
        }
    """
    model = args.get("model") or {}
    deployment_options = args.get("deployment_options") or {}
    plan = args.get("plan") or {}
    kv_config = plan.get("kv_config") or {}
    kv_extra = (kv_config.get("transfer") or {}).get("extra") or {}

    replicas = plan.get("replicas") or []
    num_replicas = max(1, len(replicas))

    # Demo only inspects the first replica's pools to derive engine_kwargs +
    # per-actor GPU count. xP1D / spec.replicas heterogeneity lands in MVP.
    first_replica = replicas[0] if replicas else {}
    pools = first_replica.get("pools") or []
    prefill_pool = next((p for p in pools if p.get("name") == "prefill"), {})
    engine_kwargs = (prefill_pool.get("variables") or {})
    gpu_per_actor = 1.0
    res = (prefill_pool.get("resources") or {})
    if "gpu" in res:
        try:
            gpu_per_actor = float(res["gpu"])
        except (TypeError, ValueError):
            gpu_per_actor = 1.0

    backend_deploy_options = {
        "num_replicas": num_replicas,
        "max_ongoing_requests": 100,
        "ray_actor_options": {"num_cpus": 0.1, "num_gpus": 0},
    }
    backend_container = args.get("backend_container")
    if backend_container:
        backend_deploy_options["ray_actor_options"]["runtime_env"] = \
            build_backend_runtime_env(backend_container)

    backend = PDCollocatedBackend.options(**backend_deploy_options).bind(
        model_args=model,
        engine_kwargs=engine_kwargs,
        kv_extra=kv_extra,
        gpu_per_actor=gpu_per_actor,
    )

    controller = PDIngress.options(
        max_ongoing_requests=100 * num_replicas,
        num_replicas=1,
        ray_actor_options={"num_cpus": 0.1, "num_gpus": 0},
    ).bind(backend=backend)

    return controller
