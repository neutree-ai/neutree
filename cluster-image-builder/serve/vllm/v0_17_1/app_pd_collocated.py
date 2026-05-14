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
import asyncio
import json
import logging
import os
import uuid
from typing import Any, Dict, List, Optional

# Local alias to avoid line-noise when calling asyncio.gather() in async methods.
asyncio_gather = asyncio.gather

from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse

import ray
from ray import serve
from ray.serve import Application
from ray.serve.config import RequestRouterConfig
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


def _vllm_port_env(plan: Dict[str, Any], replica_idx: int,
                   role_name: str, rank: int) -> Dict[str, str]:
    """vLLM-side positional port convention.

    IR carries an opaque ordered []int per (replica × role × rank) slot:
        plan["ports"][replica_idx][role_name][rank] = [p0, p1, ...]

    This helper applies the vLLM convention — pos-0 → VLLM_PORT (HTTP engine),
    pos-1 → VLLM_NIXL_SIDE_CHANNEL_PORT — and returns the env-var dict to
    inject into the actor's runtime_env. Returns {} when no allocation
    exists for this slot (Demo NumReplicas=1, portalloc didn't populate).

    SGLang would have its own helper here translating pos-0/1/2 to its own
    flag names. Naming positional convention per engine is exactly the
    engine-side translation boundary.
    """
    if not plan or "ports" not in plan or plan["ports"] is None:
        return {}
    try:
        replica_ports = plan["ports"][replica_idx]
        role_ports = replica_ports.get(role_name) or []
        slot = role_ports[rank]
    except (IndexError, KeyError, TypeError, AttributeError):
        return {}
    if not slot:
        return {}
    env: Dict[str, str] = {}
    if len(slot) >= 1:
        env["VLLM_PORT"] = str(slot[0])
    if len(slot) >= 2:
        env["VLLM_NIXL_SIDE_CHANNEL_HOST"] = "0.0.0.0"
        env["VLLM_NIXL_SIDE_CHANNEL_PORT"] = str(slot[1])
    return env



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
        ctx = ray.get_runtime_context()
        self.node_id = ctx.get_node_id()
        self.actor_id = ctx.get_actor_id()
        try:
            self.gpu_ids = list(ctx.get_gpu_ids())
        except Exception:  # noqa: BLE001 — older Ray returns ints, some return strs
            self.gpu_ids = []
        log.info(
            f"[PrefillActor] ready: actor_id={self.actor_id} node={self.node_id} gpus={self.gpu_ids}"
        )

    def get_self_info(self) -> Dict[str, Any]:
        """Topology probe — returns {kind, actor_id, node_id, gpu_ids}."""
        return {
            "kind": "prefill",
            "actor_id": str(self.actor_id),
            "node_id": str(self.node_id),
            "gpu_ids": [int(g) for g in self.gpu_ids],
        }

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
        ctx = ray.get_runtime_context()
        self.node_id = ctx.get_node_id()
        self.actor_id = ctx.get_actor_id()
        try:
            self.gpu_ids = list(ctx.get_gpu_ids())
        except Exception:  # noqa: BLE001
            self.gpu_ids = []
        log.info(
            f"[DecodeActor] ready: actor_id={self.actor_id} node={self.node_id} gpus={self.gpu_ids}"
        )

    def get_self_info(self) -> Dict[str, Any]:
        """Topology probe — returns {kind, actor_id, node_id, gpu_ids}."""
        return {
            "kind": "decode",
            "actor_id": str(self.actor_id),
            "node_id": str(self.node_id),
            "gpu_ids": [int(g) for g in self.gpu_ids],
        }

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
                 kv_extra: Dict[str, Any], gpu_per_actor: float = 1.0,
                 plan: Optional[Dict[str, Any]] = None):
        self._plan = plan or {}
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
        # Per-actor port env from plan["ports"][global_rank].
        #
        # IR carries an opaque ordered []int per slot; vLLM convention is
        # pos-0 = VLLM_PORT (engine HTTP), pos-1 = VLLM_NIXL_SIDE_CHANNEL_PORT.
        # When portalloc hasn't populated ports (Demo NumReplicas=1), env stays
        # empty and vLLM falls back to its built-in defaults — works as long
        # as no two Serve replicas land on the same host.
        prefill_env = _vllm_port_env(self._plan, self.global_rank, "prefill", 0)
        decode_env  = _vllm_port_env(self._plan, self.global_rank, "decode",  0)

        prefill_opts = {"num_cpus": 1, "num_gpus": gpu_per_actor,
                        "scheduling_strategy": sched_prefill}
        if prefill_env:
            prefill_opts["runtime_env"] = {"env_vars": prefill_env}
        decode_opts = {"num_cpus": 1, "num_gpus": gpu_per_actor,
                       "scheduling_strategy": sched_decode}
        if decode_env:
            decode_opts["runtime_env"] = {"env_vars": decode_env}

        self.prefill = PrefillActor.options(**prefill_opts).remote(
            model_args, engine_kwargs, kv_extra)
        self.decode  = DecodeActor.options(**decode_opts).remote(
            model_args, engine_kwargs, kv_extra)
        rt_ctx = ray.get_runtime_context()
        self.replica_node_id = rt_ctx.get_node_id()
        self.replica_actor_id = rt_ctx.get_actor_id()
        # Ray Serve replica identity (matches ObserverRouter._replicas key in
        # _SHARED.serve_replicas). serve.get_replica_context() raises outside a
        # Serve context, so guard it.
        self.replica_id_str = ""
        # Ray Serve 2.53 native rank (replaces ad-hoc coordinator actor pattern).
        # ctx.rank.rank is the 0..world_size-1 global rank — exactly the index
        # we need to look up plan["ports"][rank] for portalloc integration.
        self.global_rank = -1
        self.node_rank = -1
        self.local_rank = -1
        self.world_size = 0
        try:
            rc = serve.get_replica_context()
            # Across Ray versions the attribute is either `replica_id` (newer)
            # or `replica_tag` (older). Probe both.
            rid = getattr(rc, "replica_id", None) or getattr(rc, "replica_tag", None)
            self.replica_id_str = str(rid) if rid is not None else ""
            rank = getattr(rc, "rank", None)
            if rank is not None:
                self.global_rank = int(getattr(rank, "rank", -1))
                self.node_rank   = int(getattr(rank, "node_rank", -1))
                self.local_rank  = int(getattr(rank, "local_rank", -1))
            self.world_size = int(getattr(rc, "world_size", 0) or 0)
        except Exception:  # noqa: BLE001 — non-Serve test contexts
            pass
        # Resolve placement_group ID into a stable string form for topology view.
        # In Ray 2.x PG identity is exposed via .id (binary) or via str(pg) repr.
        pg_id_bytes = getattr(self.pg, "id", None)
        if isinstance(pg_id_bytes, bytes):
            self._pg_id_str = pg_id_bytes.hex()
        else:
            self._pg_id_str = str(self.pg)

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

    async def get_actor_topology(self) -> Dict[str, Any]:
        """Return the per-replica actor topology view for the PDIngress
        `_SHARED.actor_topology` cache. Demo V10..V16 validation entrypoint.

        Shape (matches shared_state.ActorTopology):
            {
              "replica_id":       "<ray serve ReplicaID>",
              "replica_actor_id": "<ray actor id of the serve replica>",
              "replica_node":     "<node id of the serve replica process>",
              "global_rank":      <int>,     # Ray Serve 2.53 native rank
              "node_rank":        <int>,
              "local_rank":       <int>,
              "world_size":       <int>,
              "pg_id":            "<placement_group id hex>",
              "prefill": {"kind","actor_id","node_id","gpu_ids","healthy"},
              "decode":  {"kind","actor_id","node_id","gpu_ids","healthy"},
              "same_host": bool,
            }
        """
        prefill_info, decode_info = await asyncio_gather(
            self.prefill.get_self_info.remote(),
            self.decode.get_self_info.remote(),
        )
        prefill_info = dict(prefill_info)
        decode_info = dict(decode_info)
        prefill_info["healthy"] = True
        decode_info["healthy"] = True
        same_host = (
            prefill_info.get("node_id") == decode_info.get("node_id")
            and prefill_info.get("node_id") is not None
        )
        return {
            "replica_id": self.replica_id_str,
            "replica_actor_id": str(self.replica_actor_id),
            "replica_node": str(self.replica_node_id),
            "global_rank": self.global_rank,
            "node_rank": self.node_rank,
            "local_rank": self.local_rank,
            "world_size": self.world_size,
            "pg_id": self._pg_id_str,
            "prefill": prefill_info,
            "decode": decode_info,
            "same_host": same_host,
        }


# --------------------------- app_builder ---------------------------


def app_builder(args: Dict[str, Any]) -> Application:
    """Phase 0 Demo app_builder.

    Args contract (matches ray_pd_branch.SerializePlan):
        args = {
            "model": {registry_type, name, version, file, task, registry_path, path, serve_name},
            "deployment_options": {...},
            "backend_container": {...},                  # optional; runtime_env builder
            "plan": {
                "num_replicas": int,
                "group": {
                    "placement": {"strategy", "granularity"},
                    "roles": [{"name", "instances", "resources", "variables",
                               "env", "deployment_options"}, ...]
                },
                "transfer": {"connector", "extra"},      # PD only
                "cache":    {"connector", "extra"},      # optional
                "ports":    [{role_name: [[port,...], ...]}],  # nil in Demo;
                                                         # portalloc fills in MVP
            },
        }
    """
    model = args.get("model") or {}
    deployment_options = args.get("deployment_options") or {}
    plan = args.get("plan") or {}

    num_replicas = max(1, int(plan.get("num_replicas") or 1))
    group = plan.get("group") or {}
    roles = group.get("roles") or []
    transfer = plan.get("transfer") or {}
    kv_extra = (transfer.get("extra") or {})

    # Demo inspects the prefill role for engine_kwargs + per-actor GPU.
    # xP1D / role-heterogeneity lands in MVP.
    prefill_role = next((r for r in roles if r.get("name") == "prefill"), {})
    engine_kwargs = (prefill_role.get("variables") or {})
    gpu_per_actor = 1.0
    res = (prefill_role.get("resources") or {})
    if "gpu" in res:
        try:
            gpu_per_actor = float(res["gpu"])
        except (TypeError, ValueError):
            gpu_per_actor = 1.0

    backend_deploy_options = {
        "num_replicas": num_replicas,
        "max_ongoing_requests": 100,
        "ray_actor_options": {"num_cpus": 0.1, "num_gpus": 0},
        # ObserverRouter runs inside the *caller* process (PDIngress) and
        # maintains the global view of PDCollocatedBackend replicas in
        # _SHARED. See serve/_ingress_router/observer_router.py.
        "request_router_config": RequestRouterConfig(
            request_router_class="serve._ingress_router.observer_router:ObserverRouter",
            request_router_kwargs={},
        ),
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
        plan=plan,                                       # full plan including ports
    )

    controller = PDIngress.options(
        max_ongoing_requests=100 * num_replicas,
        num_replicas=1,
        ray_actor_options={"num_cpus": 0.1, "num_gpus": 0},
    ).bind(backend=backend)

    return controller
