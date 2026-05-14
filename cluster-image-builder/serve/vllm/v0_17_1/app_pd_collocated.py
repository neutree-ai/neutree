"""Phase 0 Demo — PD same-host collocated app builder for vLLM v0.17.1.

End-to-end runtime layout (1 replica = 1 PDCollocatedBackend):

    PDIngress (FastAPI, 0.1 CPU, 0 GPU)
        │  ObserverRouter (Ray 2.53 native rank) picks the replica
        ▼
    PDCollocatedBackend  (0.1 CPU, 0 GPU; owns PG + protocol conversion)
        ├── placement_group(STRICT_PACK, 2 bundles)
        ├── PrefillActor (extends monolithic Backend)
        │       runtime_env.env_vars from Role.Env + portalloc PortSet
        └── DecodeActor  (extends monolithic Backend)
                runtime_env.env_vars from Role.Env + portalloc PortSet

Per-request flow inside PDCollocatedBackend.pd_chat:
    1. Apply prefill-side protocol mutation (max_tokens=1, kv_role hint).
    2. PrefillActor.generate(req) — reuses existing Backend.generate
       which speaks the OpenAI chat-completions protocol → vLLM AsyncLLM.
       Returns dict containing kv_transfer_params.
    3. Apply decode-side protocol mutation (inject kv_transfer_params).
    4. DecodeActor.generate(req) → streams completion via NIXL cuda_ipc.

PD-specific responsibility split (per user direction):
    * PrefillActor / DecodeActor: thin subclasses of monolithic Backend.
      They expose the same OpenAI generate/embed/rerank/show_models surface,
      so kv_role + NIXL config is injected via engine_args at __init__.
    * PDCollocatedBackend: owns the PG, the actor handles, and ALL
      protocol conversion (chat → prefill payload → decode payload).
      No per-actor protocol awareness — the monolithic Backend code path
      stays unchanged.
"""
import asyncio
import logging
import os
import uuid
from typing import Any, Dict, List, Optional

from fastapi import FastAPI, Request
from starlette.responses import JSONResponse, StreamingResponse

import ray
from ray import serve
from ray.serve import Application
from ray.serve.config import RequestRouterConfig
from ray.serve.handle import DeploymentHandle
from ray.util.placement_group import placement_group, PlacementGroupSchedulingStrategy

from serve._utils.runtime_env import build_backend_runtime_env
from serve._ingress_router.pd_ingress import PDIngress

# Reuse the monolithic Backend implementation: model download, AsyncLLM
# construction, OpenAI serving surfaces, error normalization. The PD path
# only differs in (a) engine_args (kv_transfer_config) and (b) protocol
# conversion between prefill and decode — neither belongs inside the
# Backend class.
from serve.vllm.v0_17_1.app import Backend


log = logging.getLogger("pd_collocated")

# Local alias to avoid line-noise when calling asyncio.gather() in async methods.
asyncio_gather = asyncio.gather


# ----------------------------- helpers -----------------------------


def _nixl_engine_args(kv_role: str, kv_extra: Dict[str, Any]) -> Dict[str, Any]:
    """Build the vLLM kv_transfer_config dict + any companion engine_args
    that PrefillActor / DecodeActor need to pass to AsyncEngineArgs.

    Keeping this on the Python side (not in IR) is the engine-side
    translation boundary: IR says "connector=nixl + extra={backend,
    buffer_size, ...}" — this helper turns that into vLLM-specific
    kv_transfer_config fields.
    """
    cfg: Dict[str, Any] = {
        "kv_connector": "NixlConnector",
        "kv_role": kv_role,  # "kv_producer" | "kv_consumer"
        "kv_buffer_size": kv_extra.get("buffer_size", 5_000_000_000),
    }
    # Surface user-supplied extras vLLM understands.
    if "kv_buffer_device" in kv_extra:
        cfg["kv_buffer_device"] = kv_extra["kv_buffer_device"]
    return {"kv_transfer_config": cfg}


def _vllm_port_env(plan: Dict[str, Any], replica_idx: int,
                   role_name: str, rank: int) -> Dict[str, str]:
    """vLLM × Ray PD positional port convention.

    IR carries an opaque ordered []int per (replica × role × rank) slot:
        plan["ports"][replica_idx][role_name][rank] = [p0, ...]

    Ray actors talk via actor-handle RPC — no HTTP engine port is needed.
    The single allocated port per actor (PortsPerRank=1) is the NIXL
    side_channel for the cuda_ipc handshake:
        pos-0 = VLLM_NIXL_SIDE_CHANNEL_PORT

    No defaults / fallbacks: portalloc must populate plan.Ports before this
    helper runs. A missing slot is a fatal control-plane misconfiguration
    and surfaces as a RuntimeError so the actor refuses to start instead
    of binding a vLLM-default port that may collide with a sibling actor.
    """
    if not plan or "ports" not in plan or plan["ports"] is None:
        raise RuntimeError(
            f"PD same-host requires plan.Ports to be populated by portalloc; "
            f"got plan.ports=None for {role_name} rank {rank} replica {replica_idx}"
        )
    try:
        replica_ports = plan["ports"][replica_idx]
        role_ports = replica_ports.get(role_name) or []
        slot = role_ports[rank]
    except (IndexError, KeyError, TypeError, AttributeError) as exc:
        raise RuntimeError(
            f"PD same-host missing port slot for ({replica_idx},{role_name},{rank}): {exc}"
        ) from exc
    if not slot:
        raise RuntimeError(
            f"PD same-host empty port slot for ({replica_idx},{role_name},{rank})"
        )
    return {
        "VLLM_NIXL_SIDE_CHANNEL_HOST": "0.0.0.0",
        "VLLM_NIXL_SIDE_CHANNEL_PORT": str(slot[0]),
    }


def _build_actor_runtime_env(role_env: Dict[str, str],
                             port_env: Dict[str, str],
                             backend_container: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    """Merge user-supplied Role.Env + portalloc-derived env + (optional)
    engine container config into a single Ray runtime_env. Platform
    (portalloc) ports win on key collision with user env.
    """
    env_vars: Dict[str, str] = {}
    if role_env:
        env_vars.update(role_env)
    if port_env:
        env_vars.update(port_env)

    runtime_env: Dict[str, Any] = {}
    if env_vars:
        runtime_env["env_vars"] = env_vars
    if backend_container:
        # Reuse the monolithic Backend's runtime_env composer so the container
        # image, NFS mounts, GPU options, etc. propagate identically.
        merged = build_backend_runtime_env(backend_container)
        if "container" in merged:
            runtime_env["container"] = merged["container"]
    return runtime_env


def _role_resources_to_ray(role: Dict[str, Any]) -> Dict[str, Any]:
    """Translate plan["group"]["roles"][i]["resources"] (api.ResourceSpec
    shape) into ray_actor_options fields. Sensible defaults match the
    monolithic Backend deployment annotation (1 cpu, 1 gpu) but every
    field can be overridden by the user-facing EndpointSpec.Roles[].Resources.
    """
    res = role.get("resources") or {}
    opts: Dict[str, Any] = {
        "num_cpus": float(res.get("cpu", 1)),
        "num_gpus": float(res.get("gpu", 1)),
    }
    # vLLM uses bytes; the user-facing field is a Gi-suffixed string per
    # the Neutree memory note "Endpoint spec memory unit defaults to GiB".
    mem = res.get("memory")
    if mem:
        try:
            opts["memory"] = int(float(mem) * (1024 ** 3))
        except (TypeError, ValueError):
            log.warning(f"[pd_collocated] memory value {mem!r} not parseable; skipping")
    if res.get("accelerator"):
        opts["resources"] = res["accelerator"]
    return opts


# ----------------------------- actors -----------------------------


@ray.remote
class PrefillActor(Backend):
    """Prefill-side vLLM engine. Extends the monolithic Backend class — it
    inherits model download + AsyncLLM construction + the OpenAI serving
    surfaces. The only behavior delta is the kv_transfer_config baked
    into engine_args at __init__: kv_role=kv_producer + NIXL connector.

    Protocol conversion (chat → prefill-with-max_tokens-1) does NOT happen
    here. PDCollocatedBackend.pd_chat owns that.
    """

    def __init__(self, *,
                 model_args: Dict[str, Any],
                 engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any]):
        merged_kwargs = dict(engine_kwargs or {})
        merged_kwargs.update(_nixl_engine_args("kv_producer", kv_extra))
        super().__init__(
            model_registry_type=model_args.get("registry_type"),
            model_name=model_args.get("name"),
            model_version=model_args.get("version"),
            model_file=model_args.get("file", ""),
            model_task=model_args.get("task", ""),
            model_registry_path=model_args.get("registry_path", ""),
            model_path=model_args.get("path", ""),
            model_serve_name=model_args.get("serve_name", ""),
            **merged_kwargs,
        )
        ctx = ray.get_runtime_context()
        self.node_id = ctx.get_node_id()
        self.actor_id = ctx.get_actor_id()
        try:
            self.gpu_ids = list(ctx.get_gpu_ids())
        except Exception:  # noqa: BLE001
            self.gpu_ids = []
        log.info(
            f"[PrefillActor] ready: actor_id={self.actor_id} node={self.node_id} gpus={self.gpu_ids}"
        )

    def get_self_info(self) -> Dict[str, Any]:
        return {
            "kind": "prefill",
            "actor_id": str(self.actor_id),
            "node_id": str(self.node_id),
            "gpu_ids": [int(g) for g in self.gpu_ids],
        }


@ray.remote
class DecodeActor(Backend):
    """Decode-side vLLM engine. Same inheritance pattern as PrefillActor;
    kv_role=kv_consumer. Receives KV blocks from the paired PrefillActor
    via NIXL cuda_ipc (zero-copy on the same host) at request time.
    """

    def __init__(self, *,
                 model_args: Dict[str, Any],
                 engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any]):
        merged_kwargs = dict(engine_kwargs or {})
        merged_kwargs.update(_nixl_engine_args("kv_consumer", kv_extra))
        super().__init__(
            model_registry_type=model_args.get("registry_type"),
            model_name=model_args.get("name"),
            model_version=model_args.get("version"),
            model_file=model_args.get("file", ""),
            model_task=model_args.get("task", ""),
            model_registry_path=model_args.get("registry_path", ""),
            model_path=model_args.get("path", ""),
            model_serve_name=model_args.get("serve_name", ""),
            **merged_kwargs,
        )
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
        return {
            "kind": "decode",
            "actor_id": str(self.actor_id),
            "node_id": str(self.node_id),
            "gpu_ids": [int(g) for g in self.gpu_ids],
        }


# --------------------------- PD backend ---------------------------


@serve.deployment(ray_actor_options={"num_cpus": 0.1, "num_gpus": 0})
class PDCollocatedBackend:
    """One per HA replica. Owns:
      - placement_group(STRICT_PACK, [prefill_bundle, decode_bundle])
      - PrefillActor / DecodeActor handles bound to the two bundles
      - per-request protocol conversion (chat → prefill payload → decode payload)

    The protocol conversion is the only PD-specific code path. The actors
    themselves use the standard monolithic Backend OpenAI surface; vLLM's
    NixlConnector handshakes the KV between them when the decode request
    carries the prefill's kv_transfer_params.
    """

    def __init__(self, *,
                 model_args: Dict[str, Any],
                 prefill_engine_kwargs: Dict[str, Any],
                 decode_engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any],
                 prefill_actor_options: Dict[str, Any],
                 decode_actor_options: Dict[str, Any],
                 plan: Optional[Dict[str, Any]] = None):
        self._plan = plan or {}

        # ── Ray Serve 2.53 native rank ────────────────────────────────
        rt_ctx = ray.get_runtime_context()
        self.replica_node_id = rt_ctx.get_node_id()
        self.replica_actor_id = rt_ctx.get_actor_id()
        self.replica_id_str = ""
        self.global_rank = -1
        self.node_rank = -1
        self.local_rank = -1
        self.world_size = 0
        try:
            rc = serve.get_replica_context()
            rid = getattr(rc, "replica_id", None) or getattr(rc, "replica_tag", None)
            self.replica_id_str = str(rid) if rid is not None else ""
            rank = getattr(rc, "rank", None)
            if rank is not None:
                self.global_rank = int(getattr(rank, "rank", -1))
                self.node_rank = int(getattr(rank, "node_rank", -1))
                self.local_rank = int(getattr(rank, "local_rank", -1))
            self.world_size = int(getattr(rc, "world_size", 0) or 0)
        except Exception:  # noqa: BLE001
            pass

        # ── placement_group for the two collocated actors ─────────────
        gpu_per_actor_pf = float(prefill_actor_options.get("num_gpus", 1))
        cpu_per_actor_pf = float(prefill_actor_options.get("num_cpus", 1))
        gpu_per_actor_de = float(decode_actor_options.get("num_gpus", 1))
        cpu_per_actor_de = float(decode_actor_options.get("num_cpus", 1))
        bundles = [
            {"CPU": cpu_per_actor_pf, "GPU": gpu_per_actor_pf},
            {"CPU": cpu_per_actor_de, "GPU": gpu_per_actor_de},
        ]
        self.pg = placement_group(bundles, strategy="STRICT_PACK")
        ray.get(self.pg.ready())
        log.info(f"[PDCollocatedBackend] PG ready: {self.pg.bundle_specs}")
        pg_id_bytes = getattr(self.pg, "id", None)
        self._pg_id_str = pg_id_bytes.hex() if isinstance(pg_id_bytes, bytes) else str(self.pg)

        # ── inject per-actor NIXL side_channel port via runtime_env ───
        # No fallback: portalloc is mandatory for PD same-host.
        prefill_port_env = _vllm_port_env(self._plan, self.global_rank, "prefill", 0)
        decode_port_env = _vllm_port_env(self._plan, self.global_rank, "decode", 0)

        prefill_role = self._role_dict("prefill")
        decode_role = self._role_dict("decode")
        backend_container = self._plan.get("backend_container") if self._plan else None

        prefill_full_opts = dict(prefill_actor_options)
        prefill_full_opts["scheduling_strategy"] = PlacementGroupSchedulingStrategy(
            placement_group=self.pg, placement_group_bundle_index=0,
        )
        prefill_runtime_env = _build_actor_runtime_env(
            role_env=prefill_role.get("env") or {},
            port_env=prefill_port_env,
            backend_container=backend_container,
        )
        if prefill_runtime_env:
            prefill_full_opts["runtime_env"] = prefill_runtime_env

        decode_full_opts = dict(decode_actor_options)
        decode_full_opts["scheduling_strategy"] = PlacementGroupSchedulingStrategy(
            placement_group=self.pg, placement_group_bundle_index=1,
        )
        decode_runtime_env = _build_actor_runtime_env(
            role_env=decode_role.get("env") or {},
            port_env=decode_port_env,
            backend_container=backend_container,
        )
        if decode_runtime_env:
            decode_full_opts["runtime_env"] = decode_runtime_env

        self.prefill = PrefillActor.options(**prefill_full_opts).remote(
            model_args=model_args,
            engine_kwargs=prefill_engine_kwargs,
            kv_extra=kv_extra,
        )
        self.decode = DecodeActor.options(**decode_full_opts).remote(
            model_args=model_args,
            engine_kwargs=decode_engine_kwargs,
            kv_extra=kv_extra,
        )

    def _role_dict(self, name: str) -> Dict[str, Any]:
        roles = ((self._plan or {}).get("group") or {}).get("roles") or []
        for r in roles:
            if r.get("name") == name:
                return r
        return {}

    # ── protocol conversion: chat → prefill → decode ─────────────────

    async def pd_chat(self, payload: Dict[str, Any]):
        """Per-request orchestration. Reuses PrefillActor/DecodeActor's
        OpenAI generate() (= Backend.generate) — the only PD-specific
        work is mutating the request payload across the two stages.
        """
        prefill_payload = dict(payload)
        prefill_payload.setdefault("request_id", uuid.uuid4().hex)
        # Force a single-token prefill so vLLM doesn't autoregress.
        prefill_payload["max_tokens"] = 1
        prefill_payload["stream"] = False

        prefill_resp = await self.prefill.generate.remote(prefill_payload)
        if isinstance(prefill_resp, dict) and "error" in prefill_resp:
            return prefill_resp

        # vLLM surfaces kv_transfer_params at the top level of the chat
        # completion response (set by the NixlConnector on the producer side).
        kv_params = None
        if hasattr(prefill_resp, "kv_transfer_params"):
            kv_params = prefill_resp.kv_transfer_params
        elif hasattr(prefill_resp, "model_dump"):
            kv_params = prefill_resp.model_dump().get("kv_transfer_params")
        elif isinstance(prefill_resp, dict):
            kv_params = prefill_resp.get("kv_transfer_params")

        decode_payload = dict(payload)
        decode_payload["request_id"] = prefill_payload["request_id"]
        if kv_params is not None:
            decode_payload["kv_transfer_params"] = kv_params

        return await self.decode.generate.remote(decode_payload)

    async def show_available_models(self):
        return await self.prefill.show_available_models.remote()

    async def get_actor_topology(self) -> Dict[str, Any]:
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


def _find_role(plan: Dict[str, Any], name: str) -> Dict[str, Any]:
    roles = ((plan or {}).get("group") or {}).get("roles") or []
    for r in roles:
        if r.get("name") == name:
            return r
    raise RuntimeError(
        f"PD plan missing role {name!r}; group.roles = {[r.get('name') for r in roles]}"
    )


def app_builder(args: Dict[str, Any]) -> Application:
    """Phase 0 Demo app_builder — complete EndpointSpec → Ray Serve propagation.

    Args contract (matches orchestrator.SerializePlan):
        args = {
            "model": {...},
            "deployment_options": {...},
            "backend_container": {...},
            "plan": {
                "num_replicas": int,
                "group": {
                    "placement": {...},
                    "roles": [{
                        "name", "instances", "ports_per_rank",
                        "resources": {cpu, gpu, memory, accelerator},
                        "variables": engine_args,
                        "env": runtime_env_overrides,
                        "deployment_options",
                    }, ...],
                },
                "transfer": {"connector", "extra"},   # PD only
                "cache":    {"connector", "extra"},   # optional
                "ports":    [ReplicaPortMap],         # ★ mandatory for PD
            },
        }

    Propagation summary (the user's "ep → ray runtime_env / engine_args / replicas" ask):
        plan.num_replicas             → backend.options(num_replicas=…)
        role.variables                → AsyncEngineArgs (via Backend __init__)
        role.resources.{cpu,gpu,memory,accelerator}
                                      → ray_actor_options on each actor
        role.env                      → runtime_env.env_vars per actor
        plan.transfer.{connector,extra}
                                      → kv_transfer_config (NixlConnector + buffer_size + …)
        plan.ports[r][role][rank]     → VLLM_NIXL_SIDE_CHANNEL_PORT per actor
    """
    model = args.get("model") or {}
    plan = args.get("plan") or {}

    num_replicas = max(1, int(plan.get("num_replicas") or 1))
    transfer = plan.get("transfer") or {}
    kv_extra = (transfer.get("extra") or {})

    prefill_role = _find_role(plan, "prefill")
    decode_role = _find_role(plan, "decode")

    # Propagate full EndpointSpec.Resources for each role into ray_actor_options.
    prefill_actor_options = _role_resources_to_ray(prefill_role)
    decode_actor_options = _role_resources_to_ray(decode_role)

    # Propagate per-role engine_args (Variables).
    prefill_engine_kwargs = dict(prefill_role.get("variables") or {})
    decode_engine_kwargs = dict(decode_role.get("variables") or {})

    backend_deploy_options = {
        "num_replicas": num_replicas,
        "max_ongoing_requests": 100,
        "ray_actor_options": {"num_cpus": 0.1, "num_gpus": 0},
        "request_router_config": RequestRouterConfig(
            request_router_class="serve._ingress_router.observer_router:ObserverRouter",
            request_router_kwargs={},
        ),
    }
    # PDCollocatedBackend itself doesn't run an engine; only the inner
    # actors need backend_container. Stash on plan so PDCollocatedBackend.__init__
    # can build the per-actor runtime_env.
    backend_container = args.get("backend_container")
    if backend_container:
        plan = dict(plan)
        plan["backend_container"] = backend_container

    backend = PDCollocatedBackend.options(**backend_deploy_options).bind(
        model_args=model,
        prefill_engine_kwargs=prefill_engine_kwargs,
        decode_engine_kwargs=decode_engine_kwargs,
        kv_extra=kv_extra,
        prefill_actor_options=prefill_actor_options,
        decode_actor_options=decode_actor_options,
        plan=plan,
    )

    controller = PDIngress.options(
        max_ongoing_requests=100 * num_replicas,
        num_replicas=1,
        ray_actor_options={"num_cpus": 0.1, "num_gpus": 0},
    ).bind(backend=backend)

    return controller
