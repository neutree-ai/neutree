"""Phase 0 Demo — PD same-host collocated app builder for vLLM v0.17.1.

End-to-end runtime layout (1 replica = 1 PDCollocatedBackend):

    PDIngress (FastAPI, 0.1 CPU, 0 GPU)
        │  ObserverRouter (Ray 2.53 native rank) picks the replica
        ▼
    PDCollocatedBackend  (0.1 CPU, 0 GPU; owns PG + protocol conversion)
        ├── placement_group(STRICT_PACK, x + y bundles)
        ├── PrefillActor[0..x-1]     (extends monolithic Backend)
        │       bundle 0..x-1
        │       runtime_env from Role.Env + portalloc port (user wins)
        └── DecodeActor[0..y-1]      (extends monolithic Backend)
                bundle x..x+y-1

Per-request flow inside PDCollocatedBackend.pd_chat:
    1. Round-robin pick prefill[i % x] and decode[i % y] via atomic counters.
    2. prefill_payload (max_tokens=1) → prefill.generate (Backend.generate)
    3. Extract kv_transfer_params from the response.
    4. decode_payload (+kv_transfer_params) → decode.generate (Backend.generate)
       — NIXL cuda_ipc fetches KV from the paired prefill on the same host.

PD-specific responsibility split:
    * PrefillActor / DecodeActor: thin subclasses of monolithic Backend.
      kv_role + NIXL connector injected via engine_args at __init__.
    * PDCollocatedBackend: owns PG, actor handles, protocol conversion,
      round-robin pair selection.

Merge precedence (Demo + MVP):
    * runtime_env env_vars: platform port env first, user Role.Env LAST
      → user explicitly wins (with a warning log when overriding a
      port var, since that bypasses portalloc).
    * engine_kwargs:        platform NIXL kv_transfer_config first, user
      Role.Variables LAST → user wins (with a warning when overriding
      kv_transfer_config / kv_connector / kv_role).
"""
import asyncio
import itertools
import logging
import uuid
from typing import Any, Dict, List, Optional

import ray
from ray import serve
from ray.serve import Application
from ray.serve.config import RequestRouterConfig
from ray.util.placement_group import placement_group, PlacementGroupSchedulingStrategy

from serve._utils.runtime_env import build_backend_runtime_env
from serve._ingress_router.pd_ingress import PDIngress
# Reuse the monolithic Backend implementation: model download, AsyncLLM
# construction, OpenAI serving surfaces, error normalization. PrefillActor
# / DecodeActor are thin Ray-actor wrappers over it.
#
# Import the RAW class (`_Backend`), not the `@serve.deployment`-wrapped
# `Backend` symbol. Subclassing a Deployment instance triggers
# Deployment.__init__ at class-body time and raises "Deployment constructor
# should not be called directly". The raw class is what we want to extend
# with @ray.remote.
from serve.vllm.v0_17_1.app import _Backend as Backend


log = logging.getLogger("pd_collocated")

asyncio_gather = asyncio.gather

# Platform-controlled keys: surfaced as warnings when user Role.Env or
# Role.Variables shadow them. Users CAN still override (they win) but at
# least the log makes the bypass auditable.
#
# Note: UCX_TLS is NOT platform-controlled. The engine / OS / NIXL pick
# sensible defaults; if a user wants to pin (e.g. drop tcp on multi-NIC
# hosts to dodge vLLM Bug #35799) they set it via EndpointSpec.Roles[].Env.
PLATFORM_ENV_KEYS = {
    "VLLM_NIXL_SIDE_CHANNEL_HOST",
    "VLLM_NIXL_SIDE_CHANNEL_PORT",
}
PLATFORM_ENGINE_KWARG_KEYS = {
    "kv_transfer_config",
    "distributed_executor_backend",
}

# Bind-mount the host's nvidia-fabricmanager socket dir into every PD inner
# actor's container. Required on NVSwitch hosts (HGX H100 / A100 8-GPU SXM);
# harmless empty dir on NVLink-bridge / PCIe-only hosts because CUDA driver
# only queries fabric_manager when NVSwitch routing is in play.
PD_FABRIC_MANAGER_MOUNT = (
    "-v", "/var/run/nvidia-fabricmanager:/var/run/nvidia-fabricmanager:ro",
)


# ----------------------------- helpers -----------------------------


def _nixl_engine_args(kv_role: str, kv_extra: Dict[str, Any]) -> Dict[str, Any]:
    """Platform NIXL kv_transfer_config translation (engine-side convention)."""
    cfg: Dict[str, Any] = {
        "kv_connector": "NixlConnector",
        "kv_role": kv_role,  # "kv_producer" | "kv_consumer"
        "kv_buffer_size": kv_extra.get("buffer_size", 5_000_000_000),
    }
    if "kv_buffer_device" in kv_extra:
        cfg["kv_buffer_device"] = kv_extra["kv_buffer_device"]
    return {"kv_transfer_config": cfg}


def _vllm_port_env(plan: Dict[str, Any], replica_idx: int,
                   role_name: str, rank: int) -> Dict[str, str]:
    """Engine-side positional-port convention.

    Reads plan["ports"][replica_idx][role_name][rank] = [side_channel_port]
    and maps pos-0 → VLLM_NIXL_SIDE_CHANNEL_PORT (Ray PD only needs that;
    HTTP engine port is not used because actors talk via Ray RPC).

    No defaults: missing/empty slot raises so PD same-host runs on exactly
    one code path. portalloc misconfiguration surfaces here loudly rather
    than at vLLM bind time.
    """
    if not plan or "ports" not in plan or plan["ports"] is None:
        raise RuntimeError(
            f"PD same-host requires plan.Ports populated by portalloc; "
            f"got plan.ports=None for {role_name} rank {rank} replica {replica_idx}"
        )
    try:
        slot = plan["ports"][replica_idx][role_name][rank]
    except (IndexError, KeyError, TypeError) as exc:
        raise RuntimeError(
            f"missing port slot for replica={replica_idx} role={role_name} rank={rank}: {exc}"
        ) from exc
    if not slot:
        raise RuntimeError(
            f"empty port slot for replica={replica_idx} role={role_name} rank={rank}"
        )
    return {
        "VLLM_NIXL_SIDE_CHANNEL_HOST": "0.0.0.0",
        "VLLM_NIXL_SIDE_CHANNEL_PORT": str(slot[0]),
    }


def _merge_user_wins(platform: Dict[str, Any],
                     user: Dict[str, Any],
                     audit_keys: set,
                     context: str) -> Dict[str, Any]:
    """Apply platform values first, then user values on top so user wins on
    key collision. Emits a warning for each platform-controlled key the
    user overrode so the bypass is auditable.
    """
    merged: Dict[str, Any] = {}
    merged.update(platform or {})
    if user:
        for k in user:
            if k in audit_keys and k in merged:
                log.warning(
                    "[pd_collocated][%s] user overriding platform-controlled "
                    "key %r=%r (was %r)",
                    context, k, user[k], merged[k],
                )
        merged.update(user)
    return merged


def _augment_pd_container(backend_container: Optional[Dict[str, Any]]) -> Optional[Dict[str, Any]]:
    """Append PD-specific docker run_options to the engine container config.

    Currently injects the fabric_manager bind mount required on NVSwitch
    hosts. On non-NVSwitch hosts the bind target is a non-existent dir;
    docker default behavior is to auto-create an empty dir on the host
    (cheap, harmless — CUDA driver never queries it on those hosts).
    """
    if not backend_container:
        return backend_container
    augmented = dict(backend_container)
    existing = list(augmented.get("run_options") or [])
    # idempotent — don't double-add if PD app_builder runs twice
    if PD_FABRIC_MANAGER_MOUNT[1] not in existing:
        existing.extend(PD_FABRIC_MANAGER_MOUNT)
    augmented["run_options"] = existing
    return augmented


def _build_actor_runtime_env(role_env: Dict[str, str],
                             port_env: Dict[str, str],
                             backend_container: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    """User Role.Env wins over portalloc-derived port env."""
    env_vars = _merge_user_wins(
        platform=port_env or {},
        user=role_env or {},
        audit_keys=PLATFORM_ENV_KEYS,
        context="env_vars",
    )
    runtime_env: Dict[str, Any] = {}
    if env_vars:
        runtime_env["env_vars"] = env_vars

    pd_container = _augment_pd_container(backend_container)
    if pd_container:
        merged = build_backend_runtime_env(pd_container)
        if "container" in merged:
            runtime_env["container"] = merged["container"]
    return runtime_env


def _actor_options_to_bundle(actor_options: Dict[str, Any]) -> Dict[str, float]:
    """ray_actor_options → placement_group bundle.

    Ray requires the bundle to declare *every* resource key the actor
    requests (CPU, GPU, memory, and any custom resources like NVIDIA_L20).
    A bundle missing any requested key triggers:

        ValueError: resource request {...} cannot fit into any bundles ...

    The bundle key naming follows Ray conventions:
      num_cpus            → "CPU"
      num_gpus            → "GPU"
      memory              → "memory"
      resources[k] = v    → k = v   (custom resources passthrough)
    """
    bundle: Dict[str, float] = {}
    if "num_cpus" in actor_options:
        bundle["CPU"] = float(actor_options["num_cpus"])
    if "num_gpus" in actor_options:
        bundle["GPU"] = float(actor_options["num_gpus"])
    if "memory" in actor_options:
        bundle["memory"] = float(actor_options["memory"])
    extra = actor_options.get("resources") or {}
    for k, v in extra.items():
        bundle[k] = float(v)
    return bundle


def _role_resources_to_ray(role: Dict[str, Any]) -> Dict[str, Any]:
    """plan.Role.RayResource → ray_actor_options.

    The CP runs the accelerator-plugin matrix (NVIDIA / AMD / future Ascend)
    and serializes the converted Ray-shape under plan.group.roles[*].resources:
    {num_cpus, num_gpus, memory(bytes), resources(map[str, float])}. This
    helper is a direct passthrough — keeping plugin variation single-sourced
    in Go avoids forking it across every engine's app.py.
    """
    res = role.get("resources") or {}
    opts: Dict[str, Any] = {}
    if "num_cpus" in res:
        opts["num_cpus"] = float(res["num_cpus"])
    if "num_gpus" in res:
        opts["num_gpus"] = float(res["num_gpus"])
    if "memory" in res:
        opts["memory"] = int(res["memory"])
    if res.get("resources"):
        opts["resources"] = dict(res["resources"])
    return opts


# ----------------------------- actors -----------------------------


@ray.remote
class PrefillActor(Backend):
    """Prefill-side vLLM engine. Extends Backend so model download +
    AsyncLLM + OpenAI generate surface are inherited unchanged.

    Merge precedence: NIXL kv_transfer_config is the platform default;
    user Role.Variables override on key collision (warning logged).
    """

    def __init__(self, *,
                 model_args: Dict[str, Any],
                 engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any]):
        log.info(
            "[PrefillActor][init/pre-merge] user_engine_kwargs_keys=%s kv_extra=%s",
            sorted((engine_kwargs or {}).keys()), kv_extra,
        )
        merged_kwargs = _merge_user_wins(
            platform=_nixl_engine_args("kv_producer", kv_extra),
            user=engine_kwargs or {},
            audit_keys=PLATFORM_ENGINE_KWARG_KEYS,
            context="engine_kwargs/prefill",
        )
        kv_cfg = merged_kwargs.get("kv_transfer_config") or {}
        log.info(
            "[PrefillActor][init/post-merge] kv_role=%s kv_connector=%s "
            "merged_keys=%s",
            kv_cfg.get("kv_role"), kv_cfg.get("kv_connector"),
            sorted(merged_kwargs.keys()),
        )
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
            "[PrefillActor][init/done] actor_id=%s node=%s gpus=%s",
            self.actor_id, self.node_id, self.gpu_ids,
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
    """Decode-side vLLM engine. Same inheritance + merge contract as
    PrefillActor; kv_role=kv_consumer.
    """

    def __init__(self, *,
                 model_args: Dict[str, Any],
                 engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any]):
        log.info(
            "[DecodeActor][init/pre-merge] user_engine_kwargs_keys=%s kv_extra=%s",
            sorted((engine_kwargs or {}).keys()), kv_extra,
        )
        merged_kwargs = _merge_user_wins(
            platform=_nixl_engine_args("kv_consumer", kv_extra),
            user=engine_kwargs or {},
            audit_keys=PLATFORM_ENGINE_KWARG_KEYS,
            context="engine_kwargs/decode",
        )
        kv_cfg = merged_kwargs.get("kv_transfer_config") or {}
        log.info(
            "[DecodeActor][init/post-merge] kv_role=%s kv_connector=%s "
            "merged_keys=%s",
            kv_cfg.get("kv_role"), kv_cfg.get("kv_connector"),
            sorted(merged_kwargs.keys()),
        )
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
            "[DecodeActor][init/done] actor_id=%s node=%s gpus=%s",
            self.actor_id, self.node_id, self.gpu_ids,
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
    """One per HA replica. Owns x PrefillActors + y DecodeActors collocated
    via STRICT_PACK PG. Round-robins prefill+decode selection on each
    pd_chat call (MVP will replace with CHWBL).
    """

    def __init__(self, *,
                 model_args: Dict[str, Any],
                 prefill_engine_kwargs: Dict[str, Any],
                 decode_engine_kwargs: Dict[str, Any],
                 kv_extra: Dict[str, Any],
                 prefill_count: int,
                 decode_count: int,
                 prefill_actor_options: Dict[str, Any],
                 decode_actor_options: Dict[str, Any],
                 plan: Optional[Dict[str, Any]] = None):
        if prefill_count <= 0 or decode_count <= 0:
            raise ValueError(
                f"PD same-host requires prefill_count>0 and decode_count>0, "
                f"got prefill={prefill_count} decode={decode_count}"
            )
        self._plan = plan or {}
        self._prefill_count = prefill_count
        self._decode_count = decode_count

        # ── Demo debug: plan + actor_options shape (V3 / V8 / V18) ─────
        log.info(
            "[PDCollocatedBackend][init/plan] num_replicas=%s prefill_count=%d decode_count=%d "
            "transfer=%s ports_present=%s backend_container_present=%s",
            self._plan.get("num_replicas"), prefill_count, decode_count,
            (self._plan.get("transfer") or {}).get("connector"),
            bool(self._plan.get("ports")),
            bool(self._plan.get("backend_container")),
        )
        log.info(
            "[PDCollocatedBackend][init/actor_options] prefill=%s decode=%s",
            prefill_actor_options, decode_actor_options,
        )

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

        # ── placement_group: x + y bundles, all STRICT_PACK ───────────
        # Bundle keys must be a superset of every key the actor_options request
        # (CPU/GPU/memory + custom resources like NVIDIA_L20). Otherwise Ray
        # rejects scheduling with "resource request {...} cannot fit into any
        # bundles".
        prefill_bundle = _actor_options_to_bundle(prefill_actor_options)
        decode_bundle = _actor_options_to_bundle(decode_actor_options)
        bundles = (
            [dict(prefill_bundle) for _ in range(prefill_count)]
            + [dict(decode_bundle) for _ in range(decode_count)]
        )
        # V8 validation: bundle shape must be a superset of actor_options keys
        # (else Ray PG rejects scheduling). Log both so debug is one-shot.
        log.info(
            "[PDCollocatedBackend][init/bundles] prefill_bundle=%s decode_bundle=%s total=%d",
            prefill_bundle, decode_bundle, len(bundles),
        )
        self.pg = placement_group(bundles, strategy="STRICT_PACK")
        ray.get(self.pg.ready())
        log.info(
            "[PDCollocatedBackend][init/pg] PG ready: %dP + %dD = %d bundles strategy=STRICT_PACK "
            "global_rank=%d node_rank=%d local_rank=%d world_size=%d replica_id=%s",
            prefill_count, decode_count, len(bundles),
            self.global_rank, self.node_rank, self.local_rank, self.world_size,
            self.replica_id_str,
        )
        pg_id_bytes = getattr(self.pg, "id", None)
        self._pg_id_str = pg_id_bytes.hex() if isinstance(pg_id_bytes, bytes) else str(self.pg)

        # ── spawn x PrefillActors + y DecodeActors ─────────────────────
        prefill_role = self._role_dict("prefill")
        decode_role = self._role_dict("decode")
        prefill_env = prefill_role.get("env") or {}
        decode_env = decode_role.get("env") or {}
        backend_container = self._plan.get("backend_container") if self._plan else None

        self.prefills: List[Any] = []
        for rank in range(prefill_count):
            port_env = _vllm_port_env(self._plan, self.global_rank, "prefill", rank)
            opts = dict(prefill_actor_options)
            opts["scheduling_strategy"] = PlacementGroupSchedulingStrategy(
                placement_group=self.pg, placement_group_bundle_index=rank,
            )
            rt = _build_actor_runtime_env(prefill_env, port_env, backend_container)
            if rt:
                opts["runtime_env"] = rt
            # V19 / V21 / V22 — per-actor spawn point: bundle_index, port env,
            # runtime_env composition (container vs env_vars) all in one line.
            log.info(
                "[PDCollocatedBackend][spawn/prefill rank=%d] bundle_index=%d "
                "port_env=%s role_env_keys=%s rt_keys=%s",
                rank, rank, port_env, sorted((prefill_env or {}).keys()),
                sorted((rt or {}).keys()),
            )
            self.prefills.append(
                PrefillActor.options(**opts).remote(
                    model_args=model_args,
                    engine_kwargs=prefill_engine_kwargs,
                    kv_extra=kv_extra,
                )
            )

        self.decodes: List[Any] = []
        for rank in range(decode_count):
            port_env = _vllm_port_env(self._plan, self.global_rank, "decode", rank)
            opts = dict(decode_actor_options)
            opts["scheduling_strategy"] = PlacementGroupSchedulingStrategy(
                placement_group=self.pg,
                placement_group_bundle_index=prefill_count + rank,
            )
            rt = _build_actor_runtime_env(decode_env, port_env, backend_container)
            if rt:
                opts["runtime_env"] = rt
            log.info(
                "[PDCollocatedBackend][spawn/decode rank=%d] bundle_index=%d "
                "port_env=%s role_env_keys=%s rt_keys=%s",
                rank, prefill_count + rank, port_env,
                sorted((decode_env or {}).keys()),
                sorted((rt or {}).keys()),
            )
            self.decodes.append(
                DecodeActor.options(**opts).remote(
                    model_args=model_args,
                    engine_kwargs=decode_engine_kwargs,
                    kv_extra=kv_extra,
                )
            )

        # Round-robin counters for prefill / decode pair selection.
        self._prefill_cursor = itertools.count()
        self._decode_cursor = itertools.count()
        log.info(
            "[PDCollocatedBackend][init/done] %dP + %dD actors spawned "
            "(handles pending; first .remote() will block on actor ready)",
            prefill_count, decode_count,
        )

    def _role_dict(self, name: str) -> Dict[str, Any]:
        roles = ((self._plan or {}).get("group") or {}).get("roles") or []
        for r in roles:
            if r.get("name") == name:
                return r
        return {}

    def _pick_prefill(self):
        idx = next(self._prefill_cursor) % self._prefill_count
        return idx, self.prefills[idx]

    def _pick_decode(self):
        idx = next(self._decode_cursor) % self._decode_count
        return idx, self.decodes[idx]

    # ── protocol conversion: chat → prefill → decode ─────────────────

    async def pd_chat(self, payload: Dict[str, Any]):
        """Round-robin pair selection + chat-completion protocol conversion."""
        prefill_idx, prefill_handle = self._pick_prefill()
        decode_idx, decode_handle = self._pick_decode()

        prefill_payload = dict(payload)
        prefill_payload.setdefault("request_id", uuid.uuid4().hex)
        prefill_payload["max_tokens"] = 1
        prefill_payload["stream"] = False
        req_id = prefill_payload["request_id"]

        log.info(
            "[pd_chat][pair] req=%s prefill_rank=%d decode_rank=%d "
            "(replica global_rank=%d)",
            req_id, prefill_idx, decode_idx, self.global_rank,
        )

        prefill_resp = await prefill_handle.generate.remote(prefill_payload)
        if isinstance(prefill_resp, dict) and "error" in prefill_resp:
            log.warning(
                "[pd_chat][prefill_error] req=%s err=%s", req_id, prefill_resp.get("error"),
            )
            return prefill_resp

        kv_params = None
        kv_source = "none"
        if hasattr(prefill_resp, "kv_transfer_params"):
            kv_params = prefill_resp.kv_transfer_params
            kv_source = "attr"
        elif hasattr(prefill_resp, "model_dump"):
            kv_params = prefill_resp.model_dump().get("kv_transfer_params")
            kv_source = "model_dump"
        elif isinstance(prefill_resp, dict):
            kv_params = prefill_resp.get("kv_transfer_params")
            kv_source = "dict"

        # V6 validation: kv_transfer_params is a plain dict round-trippable
        # via Ray, and prefill returned it before decode runs.
        log.info(
            "[pd_chat][prefill_done] req=%s resp_type=%s kv_source=%s kv_present=%s "
            "kv_keys=%s",
            req_id, type(prefill_resp).__name__, kv_source, kv_params is not None,
            sorted(kv_params.keys()) if isinstance(kv_params, dict) else None,
        )

        decode_payload = dict(payload)
        decode_payload["request_id"] = req_id
        if kv_params is not None:
            decode_payload["kv_transfer_params"] = kv_params

        log.info(
            "[pd_chat][decode_dispatch] req=%s kv_injected=%s stream=%s",
            req_id, kv_params is not None, decode_payload.get("stream", False),
        )
        return await decode_handle.generate.remote(decode_payload)

    async def show_available_models(self):
        return await self.prefills[0].show_available_models.remote()

    async def get_actor_topology(self) -> Dict[str, Any]:
        prefill_infos = await asyncio_gather(
            *[a.get_self_info.remote() for a in self.prefills]
        )
        decode_infos = await asyncio_gather(
            *[a.get_self_info.remote() for a in self.decodes]
        )
        prefill_infos = [dict(i, healthy=True) for i in prefill_infos]
        decode_infos = [dict(i, healthy=True) for i in decode_infos]
        all_nodes = (
            [i.get("node_id") for i in prefill_infos]
            + [i.get("node_id") for i in decode_infos]
        )
        same_host = (
            len(all_nodes) > 0
            and all(n is not None for n in all_nodes)
            and len(set(all_nodes)) == 1
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
            "prefills": prefill_infos,
            "decodes": decode_infos,
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
    """Phase 0 Demo app_builder — xPyD parameterized + full EndpointSpec propagation."""
    model = args.get("model") or {}
    plan = args.get("plan") or {}

    num_replicas = max(1, int(plan.get("num_replicas") or 1))
    transfer = plan.get("transfer") or {}
    kv_extra = (transfer.get("extra") or {})

    prefill_role = _find_role(plan, "prefill")
    decode_role = _find_role(plan, "decode")

    prefill_count = int(prefill_role.get("instances") or 1)
    decode_count = int(decode_role.get("instances") or 1)

    prefill_actor_options = _role_resources_to_ray(prefill_role)
    decode_actor_options = _role_resources_to_ray(decode_role)

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
    backend_container = args.get("backend_container")
    if backend_container:
        plan = dict(plan)
        plan["backend_container"] = backend_container

    backend = PDCollocatedBackend.options(**backend_deploy_options).bind(
        model_args=model,
        prefill_engine_kwargs=prefill_engine_kwargs,
        decode_engine_kwargs=decode_engine_kwargs,
        kv_extra=kv_extra,
        prefill_count=prefill_count,
        decode_count=decode_count,
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
