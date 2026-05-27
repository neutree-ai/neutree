"""PD same-host collocated app builder for vLLM v0.17.1.

End-to-end runtime layout (1 replica = 1 PDCollocatedBackend):

    PDIngress (FastAPI, 0.1 CPU, 0 GPU)
        │  Selects RoleGroup + prefill/decode ranks; ObserverRouter pins replica
        ▼
    PDCollocatedBackend  (0.1 CPU, 0 GPU; owns PG + protocol conversion)
        ├── placement_group(STRICT_PACK, x + y bundles)
        ├── PrefillActor[0..x-1]     (extends monolithic Backend)
        │       bundle 0..x-1
        │       runtime_env from Role.Env + portalloc port (user wins)
        └── DecodeActor[0..y-1]      (extends monolithic Backend)
                bundle x..x+y-1

Per-request flow inside PDCollocatedBackend.pd_chat:
    1. Receive prefill_index/decode_index selected by PDIngress.
    2. prefill_payload (max_tokens=1) → prefill.generate (Backend.generate)
    3. Extract kv_transfer_params from the response.
    4. decode_payload (+kv_transfer_params) → decode.generate (Backend.generate)
       — NIXL cuda_ipc fetches KV from the paired prefill on the same host.

PD-specific responsibility split:
    * PrefillActor / DecodeActor: thin subclasses of monolithic Backend.
      kv_role + NIXL connector injected via engine_args at __init__.
    * PDCollocatedBackend: owns PG, actor handles, protocol conversion,
      and executes the ingress-selected P/D pair.

Merge precedence:
    * runtime_env env_vars: platform port env first, user Role.Env LAST
      → user explicitly wins (with a warning log when overriding a
      port var, since that bypasses portalloc).
    * engine_kwargs:        platform NIXL kv_transfer_config first, user
      Role.Variables LAST → user wins (with a warning when overriding
      kv_transfer_config / kv_connector / kv_role).
"""
import asyncio
import logging
import time
import uuid
from typing import Any, Dict, List, Optional

import ray
from ray import serve
from ray.serve import Application
from ray.serve._private.constants import SERVE_LOGGER_NAME
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


# Route through the Ray Serve logger so logs land in the deployment-
# replica stdout / replica log file. Custom logger names have no handler
# attached by default and disappear silently.
log = logging.getLogger(SERVE_LOGGER_NAME)

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
    # PD inner actors are not Serve deployments, so they cannot call
    # serve.get_replica_context() to populate NeutreeRayStatLogger labels.
    # The outer PDCollocatedBackend forwards its own Serve context plus
    # the per-actor role/rank via these env vars so vLLM-side metrics keep
    # the deployment / replica / application / role / rank dimensions.
    "NEUTREE_RAY_STAT_DEPLOYMENT",
    "NEUTREE_RAY_STAT_REPLICA",
    "NEUTREE_RAY_STAT_APPLICATION",
    "NEUTREE_RAY_STAT_ROLE",
    "NEUTREE_RAY_STAT_RANK",
    # Force line-buffered stdio inside the engine container. Without this
    # CPython fully-buffers stdout/stderr in non-TTY mode (which is what
    # `docker run` produces), so vLLM's logger.info / print stay in the
    # process buffer until the actor exits — at which point `--rm` has
    # already removed the container. Setting this in runtime_env.env_vars
    # makes `docker logs <container>` / `ray logs actor <id>` tailable in
    # real time.
    "PYTHONUNBUFFERED",
}
PLATFORM_ENGINE_KWARG_KEYS = {
    "kv_transfer_config",
    "distributed_executor_backend",
}

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


def _vllm_port_env(pd_config: Dict[str, Any], role_group_index: int,
                   role_name: str, rank: int) -> Dict[str, str]:
    """Engine-side single-port convention.

    Reads pd_config["ports"][role_group_index][role_name][rank] =
    [side_channel_port] and maps the sole value to
    VLLM_NIXL_SIDE_CHANNEL_PORT (Ray PD only needs that; HTTP engine port is
    not used because actors talk via Ray RPC).

    No defaults: missing/empty slot raises so PD same-host runs on exactly
    one code path. portalloc misconfiguration surfaces here loudly rather
    than at vLLM bind time.
    """
    if not pd_config or "ports" not in pd_config or pd_config["ports"] is None:
        raise RuntimeError(
            f"PD same-host requires pd_config.Ports populated by portalloc; "
            f"got pd_config.ports=None for {role_name} rank {rank} role_group {role_group_index}"
        )
    try:
        slot = pd_config["ports"][role_group_index][role_name][rank]
    except (IndexError, KeyError, TypeError) as exc:
        raise RuntimeError(
            f"missing port slot for role_group={role_group_index} role={role_name} rank={rank}: {exc}"
        ) from exc
    if not slot:
        raise RuntimeError(
            f"empty port slot for role_group={role_group_index} role={role_name} rank={rank}"
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


def _build_actor_runtime_env(role_env: Dict[str, str],
                             port_env: Dict[str, str],
                             metrics_env: Dict[str, str],
                             backend_container: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    """User Role.Env wins over portalloc + metrics-platform env.

    Platform env = port_env (NIXL side_channel*) ∪ metrics_env
    (NEUTREE_RAY_STAT_* for NeutreeRayStatLogger labels). User Role.Env
    overrides both — warnings are logged for any PLATFORM_ENV_KEYS the
    user shadows so the bypass is auditable.
    """
    platform = {}
    platform.update(metrics_env or {})
    platform.update(port_env or {})
    env_vars = _merge_user_wins(
        platform=platform,
        user=role_env or {},
        audit_keys=PLATFORM_ENV_KEYS,
        context="env_vars",
    )
    runtime_env: Dict[str, Any] = {}
    if env_vars:
        runtime_env["env_vars"] = env_vars

    # backend_container is fully assembled CP-side (image + GPU run_options +
    # NFS / model-cache mounts + PD-specific FM mount for NVSwitch hosts);
    # engine side just forwards it onto each actor's runtime_env.container.
    if backend_container:
        merged = build_backend_runtime_env(backend_container)
        if "container" in merged:
            runtime_env["container"] = merged["container"]
    return runtime_env


def _summarize_kv_params(kv: Any, max_block_ids: int = 8) -> Any:
    """Render kv_transfer_params for debug logs.

    Returns the dict as-is, except remote_block_ids is trimmed to the
    first `max_block_ids` entries with a "+N more" suffix so a 4k-token
    prefill doesn't blow up the log line. Non-dict input passes through
    so callers can pipe None / errors verbatim.
    """
    if not isinstance(kv, dict):
        return kv
    out = dict(kv)
    blocks = out.get("remote_block_ids")
    if isinstance(blocks, list) and len(blocks) > max_block_ids:
        out["remote_block_ids"] = (
            list(blocks[:max_block_ids]) + [f"...+{len(blocks) - max_block_ids} more"]
        )
    return out


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
    """pd_config.Role.RayResource → ray_actor_options.

    The CP runs the accelerator-plugin matrix (NVIDIA / AMD / future Ascend)
    and serializes the converted Ray-shape under pd_config.group.roles[*].resources:
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

    async def generate_stream(self, payload: Dict[str, Any]):
        """Yield-based streaming wrapper for SSE chat completions.

        Backend.generate returns an AsyncGenerator object when payload has
        stream=True. Generator objects do not survive Ray's RPC pickling,
        so we cannot just `await decode_handle.generate.remote(...)` and
        iterate on the caller side — that fails for plain @ray.remote
        actors. Instead this yield-based method iterates the underlying
        generator inside the actor process; Ray transparently turns
        `.remote()` on a yield-based async method into a
        StreamingObjectRefGenerator that the caller can `async for`.
        """
        gen_or_value = await self.generate(payload)
        if hasattr(gen_or_value, "__aiter__"):
            async for chunk in gen_or_value:
                yield chunk
        else:
            # stream=False path or error response; yield once so the
            # streaming contract still holds.
            yield gen_or_value


# --------------------------- PD backend ---------------------------


@serve.deployment(ray_actor_options={"num_cpus": 0.1, "num_gpus": 0})
class PDCollocatedBackend:
    """One per HA replica. Owns x PrefillActors + y DecodeActors collocated
    via STRICT_PACK PG. PDIngress selects the RoleGroup and local
    prefill/decode ranks; this deployment only performs protocol conversion.
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
                 pd_config: Optional[Dict[str, Any]] = None):
        if prefill_count <= 0 or decode_count <= 0:
            raise ValueError(
                f"PD same-host requires prefill_count>0 and decode_count>0, "
                f"got prefill={prefill_count} decode={decode_count}"
            )
        self._pd_config = pd_config or {}
        self._prefill_count = prefill_count
        self._decode_count = decode_count
        # Stashed so show_available_models can synthesize the /v1/models
        # response directly without bouncing through a PrefillActor RPC.
        self._model_serve_name = (model_args or {}).get("serve_name") \
            or (model_args or {}).get("name", "")

        # ── PD debug: pd_config + actor_options shape (V3 / V8 / V18) ─────
        log.info(
            "[PDCollocatedBackend][init/pd_config] num_replicas=%s prefill_count=%d decode_count=%d "
            "transfer=%s ports_present=%s backend_container_present=%s",
            self._pd_config.get("num_replicas"), prefill_count, decode_count,
            (self._pd_config.get("transfer") or {}).get("connector"),
            bool(self._pd_config.get("ports")),
            bool(self._pd_config.get("backend_container")),
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
        backend_container = self._pd_config.get("backend_container") if self._pd_config else None

        # Metrics-label forwarding for NeutreeRayStatLogger. PD inner actors
        # have no Serve context; we forward the outer replica's triple plus
        # per-actor role/rank so vLLM-side metrics keep the deployment /
        # replica / application dimensions and gain role/rank for per-actor
        # slicing.
        # PYTHONUNBUFFERED=1 makes the inner-actor container's stdio
        # line-buffered so vLLM's logger.info / print show up immediately
        # via `docker logs` / `ray logs actor`, instead of being trapped
        # in CPython's full-buffer (default for non-TTY stdio).
        metrics_env_base = {"PYTHONUNBUFFERED": "1"}
        try:
            rc = serve.get_replica_context()
            dep = getattr(rc, "deployment", "") or ""
            rtag = getattr(rc, "replica_tag", "") or getattr(rc, "replica_id", "") or ""
            app = getattr(rc, "app_name", "") or ""
            if dep:
                metrics_env_base["NEUTREE_RAY_STAT_DEPLOYMENT"] = str(dep)
            if rtag:
                metrics_env_base["NEUTREE_RAY_STAT_REPLICA"] = str(rtag)
            if app:
                metrics_env_base["NEUTREE_RAY_STAT_APPLICATION"] = str(app)
        except Exception as exc:  # noqa: BLE001 — outer is always Serve; log if not
            log.warning(
                "[PDCollocatedBackend] no Serve context for metrics-label "
                "forwarding (%s); PD actor metrics will lack labels",
                type(exc).__name__,
            )

        self.prefills: List[Any] = []
        for rank in range(prefill_count):
            port_env = _vllm_port_env(self._pd_config, self.global_rank, "prefill", rank)
            metrics_env = dict(metrics_env_base)
            metrics_env["NEUTREE_RAY_STAT_ROLE"] = "prefill"
            metrics_env["NEUTREE_RAY_STAT_RANK"] = str(rank)
            opts = dict(prefill_actor_options)
            opts["scheduling_strategy"] = PlacementGroupSchedulingStrategy(
                placement_group=self.pg, placement_group_bundle_index=rank,
            )
            rt = _build_actor_runtime_env(prefill_env, port_env, metrics_env, backend_container)
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
            port_env = _vllm_port_env(self._pd_config, self.global_rank, "decode", rank)
            metrics_env = dict(metrics_env_base)
            metrics_env["NEUTREE_RAY_STAT_ROLE"] = "decode"
            metrics_env["NEUTREE_RAY_STAT_RANK"] = str(rank)
            opts = dict(decode_actor_options)
            opts["scheduling_strategy"] = PlacementGroupSchedulingStrategy(
                placement_group=self.pg,
                placement_group_bundle_index=prefill_count + rank,
            )
            rt = _build_actor_runtime_env(decode_env, port_env, metrics_env, backend_container)
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

        # Block until every inner actor finishes vLLM init (model download,
        # AsyncLLM construction, NIXL handshake). Ray Serve calls
        # check_health right after __init__ returns; without this wait the
        # 5s probe always times out because actors are still loading the
        # model in the background. Same shape as monolithic Backend
        # blocking on AsyncLLM.from_engine_args (sync ctor).
        log.info(
            "[PDCollocatedBackend][init/wait_ready] waiting for %dP + %dD "
            "actors to finish vLLM init",
            prefill_count, decode_count,
        )
        prefill_infos = ray.get([h.get_self_info.remote() for h in self.prefills])
        decode_infos = ray.get([h.get_self_info.remote() for h in self.decodes])
        # Bridge inner-actor identity into PDCollocatedBackend's logger so
        # the deployment-replica log file is one-stop for "where did each
        # P/D actor land". Inner actors also log to worker stderr via
        # _setup_actor_stderr_logging (queryable with `ray logs actor`)
        # but the bridge here keeps verification readable.
        for i, info in enumerate(prefill_infos):
            log.info(
                "[PDCollocatedBackend][actor_ready/prefill rank=%d] "
                "actor_id=%s node_id=%s gpu_ids=%s",
                i, info.get("actor_id"), info.get("node_id"), info.get("gpu_ids"),
            )
        for i, info in enumerate(decode_infos):
            log.info(
                "[PDCollocatedBackend][actor_ready/decode rank=%d] "
                "actor_id=%s node_id=%s gpu_ids=%s",
                i, info.get("actor_id"), info.get("node_id"), info.get("gpu_ids"),
            )
        all_nodes = [i.get("node_id") for i in prefill_infos + decode_infos]
        same_host = len(set(all_nodes)) == 1 if all_nodes else False
        log.info(
            "[PDCollocatedBackend][init/done] %dP + %dD actors ready "
            "same_host=%s nodes=%s",
            prefill_count, decode_count, same_host, sorted(set(all_nodes)),
        )

    def _role_dict(self, name: str) -> Dict[str, Any]:
        roles = ((self._pd_config or {}).get("group") or {}).get("roles") or []
        for r in roles:
            if r.get("name") == name:
                return r
        return {}

    def _pick_prefill(self, index: Optional[int]):
        if index is None:
            raise ValueError("prefill_index is required")
        idx = int(index)
        if idx < 0 or idx >= self._prefill_count:
            raise IndexError(
                f"prefill_index {idx} out of range [0,{self._prefill_count})"
            )
        return idx, self.prefills[idx]

    def _pick_decode(self, index: Optional[int]):
        if index is None:
            raise ValueError("decode_index is required")
        idx = int(index)
        if idx < 0 or idx >= self._decode_count:
            raise IndexError(
                f"decode_index {idx} out of range [0,{self._decode_count})"
            )
        return idx, self.decodes[idx]

    # ── protocol conversion: chat → prefill → decode ─────────────────

    async def pd_chat(
        self,
        payload: Dict[str, Any],
        prefill_index: Optional[int] = None,
        decode_index: Optional[int] = None,
    ):
        """Chat-completion protocol conversion for the ingress-selected P/D pair.

        vLLM NIXL contract (vllm/distributed/kv_transfer/.../nixl/scheduler.py):
          - Prefill request must carry kv_transfer_params={"do_remote_decode":
            True}. Without this flag NIXL treats the request as a normal
            completion, releases KV after prefill, and response.kv_transfer_params
            stays None — no transfer happens.
          - Prefill response then carries the producer-side blob:
            {do_remote_prefill: True, remote_block_ids, remote_engine_id,
             remote_request_id, remote_host, remote_port, tp_size,
             remote_num_tokens}. Pass it through as-is to decode so NIXL on the
             consumer side fetches the staged KV via cuda_ipc.
        """
        try:
            prefill_idx, prefill_handle = self._pick_prefill(prefill_index)
            decode_idx, decode_handle = self._pick_decode(decode_index)
        except (TypeError, ValueError, IndexError) as exc:
            return {"error": f"invalid PD route indices: {exc}"}

        prefill_payload = dict(payload)
        prefill_payload.setdefault("request_id", uuid.uuid4().hex)
        # Cap output to exactly 1 token. OpenAI chat completions accept two
        # equivalent knobs:
        #   - max_tokens             (legacy alias)
        #   - max_completion_tokens  (preferred since 2024; wins when both set)
        # If we only overrode max_tokens, a client request carrying
        # max_completion_tokens=N would have the prefill actually generate N
        # tokens — defeating the PD split and wasting GPU. Override both so
        # the effective limit is 1 regardless of which name the client used.
        prefill_payload["max_tokens"] = 1
        if "max_completion_tokens" in prefill_payload:
            prefill_payload["max_completion_tokens"] = 1
        prefill_payload["stream"] = False
        # Signal the prefill engine: stage KV for a remote decode. Drop any
        # client-supplied kv_transfer_params on the prefill side — it would
        # confuse the scheduler (clients send decode-side params for D-only
        # endpoints, not P-only).
        prefill_payload["kv_transfer_params"] = {"do_remote_decode": True}
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

        # V6 validation (debug-phase verbose): emit the full kv_transfer_params
        # dict so the NIXL hand-off blob (remote_engine_id / remote_host /
        # remote_port / remote_block_ids / tp_size / remote_num_tokens) can be
        # eyeballed end-to-end. Trim to a repr() with a soft cap on
        # remote_block_ids so the log line stays grep-friendly when prefill
        # produced thousands of blocks.
        log.info(
            "[pd_chat][prefill_done] req=%s resp_type=%s kv_source=%s "
            "kv_present=%s kv_transfer_params=%s",
            req_id, type(prefill_resp).__name__, kv_source,
            kv_params is not None, _summarize_kv_params(kv_params),
        )

        decode_payload = dict(payload)
        decode_payload["request_id"] = req_id
        if kv_params is not None:
            decode_payload["kv_transfer_params"] = kv_params

        is_stream = bool(decode_payload.get("stream"))
        log.info(
            "[pd_chat][decode_dispatch] req=%s kv_injected=%s stream=%s "
            "decode_kv_transfer_params=%s",
            req_id, kv_params is not None, is_stream,
            _summarize_kv_params(decode_payload.get("kv_transfer_params")),
        )
        if is_stream:
            # Return a local async generator that transparently forwards
            # the decode actor's streaming chunks back to PDIngress, which
            # is wrapping pd_chat with options(stream=True). Returning a
            # generator object (vs `await ...`) is what makes Ray Serve
            # actually stream over SSE instead of buffering.
            return self._stream_decode(decode_handle, decode_payload, req_id)
        return await decode_handle.generate.remote(decode_payload)

    async def _stream_decode(self, decode_handle, decode_payload, req_id):
        """Iterate the decode actor's yield-based generate_stream and
        re-yield each chunk so Ray Serve's stream-mode wrapper turns the
        coroutine into an SSE response.

        decode_handle is a plain @ray.remote actor (not a Serve handle),
        so the streaming `.remote()` returns an ObjectRefGenerator. Each
        iterated item is an ObjectRef — `await`ing it resolves to the
        actual yielded value from the actor side (the SSE chunk string).
        Skipping the await would forward raw refs into starlette which
        crashes with "ObjectRef has no attribute 'encode'".
        """
        gen = decode_handle.generate_stream.remote(decode_payload)
        try:
            async for ref in gen:
                chunk = await ref
                yield chunk
        except Exception as exc:  # noqa: BLE001
            log.warning(
                "[pd_chat][stream_error] req=%s err=%s", req_id, exc,
            )
            raise

    # ── teardown (避免 EP 更新残留容器) ────────────────────────────────
    # Ray actor ownership normally ensures: PDCollocatedBackend dies →
    # placement_group released → inner actors lose bundles → killed →
    # their container exits → `--rm` removes it. The chain breaks when
    # Ray Serve SIGKILLs the outer replica past its graceful-shutdown
    # window before owner-death signals propagate; the inner vLLM
    # process keeps the container alive indefinitely and each EP update
    # leaks one container per inner actor.
    #
    # Explicit cleanup hook in both __del__ and a Serve-style
    # async shutdown method (Ray Serve calls __del__ on graceful
    # shutdown; the async variant is defensive for future versions).

    def _teardown_inner_actors(self) -> None:
        """Idempotent. Best-effort kill of every inner P/D actor + remove
        the PG. Swallows every exception — running during interpreter
        teardown means imports / state may already be partly gone.
        """
        try:
            handles = list(getattr(self, "prefills", []) or []) + \
                      list(getattr(self, "decodes", []) or [])
            for h in handles:
                try:
                    ray.kill(h, no_restart=True)
                except Exception:  # noqa: BLE001
                    pass
            pg = getattr(self, "pg", None)
            if pg is not None:
                try:
                    from ray.util.placement_group import remove_placement_group
                    remove_placement_group(pg)
                except Exception:  # noqa: BLE001
                    pass
        except Exception:  # noqa: BLE001
            pass

    def __del__(self):
        # Ray Serve calls __del__ at replica shutdown (with a best-effort
        # graceful window). Containers auto-remove via --rm once the inner
        # actor's worker process exits.
        try:
            log.info(
                "[PDCollocatedBackend][teardown] killing %d prefill + %d decode "
                "actors; releasing PG",
                len(getattr(self, "prefills", []) or []),
                len(getattr(self, "decodes", []) or []),
            )
        except Exception:  # noqa: BLE001 — log may already be torn down
            pass
        self._teardown_inner_actors()

    # ── health probe (P+D 同生共死) ───────────────────────────────────
    # Ray Serve calls check_health periodically on each replica. Raising
    # marks the replica unhealthy and triggers controller-side recycling.
    # Default (no method) only checks "actor alive"; we extend it to fan
    # out a cheap RPC to every inner PrefillActor / DecodeActor so a dead
    # inner actor takes down its replica instead of silently routing
    # requests to a stale handle.

    _HEALTH_TIMEOUT_SEC = 5.0

    async def check_health(self):
        """Replica health hook.

        Success: every inner actor responded to get_self_info within
        _HEALTH_TIMEOUT_SEC.
        Failure: any inner actor is dead or unresponsive → raise →
        Ray Serve tears down the replica (and its PG, and the rest of
        the inner actors), then re-creates one. Same-host semantic
        means we don't try to "rescue" a half-living replica.
        """
        handles = list(self.prefills) + list(self.decodes)
        if not handles:
            raise RuntimeError("[PDCollocatedBackend] no inner actors to probe")
        try:
            await asyncio.wait_for(
                asyncio_gather(*[h.get_self_info.remote() for h in handles]),
                timeout=self._HEALTH_TIMEOUT_SEC,
            )
        except asyncio.TimeoutError as exc:
            raise RuntimeError(
                f"[PDCollocatedBackend] inner-actor health probe timed out "
                f">{self._HEALTH_TIMEOUT_SEC}s (replica={self.replica_id_str})"
            ) from exc
        except Exception as exc:  # noqa: BLE001
            raise RuntimeError(
                f"[PDCollocatedBackend] inner-actor health probe failed "
                f"(replica={self.replica_id_str}): {exc}"
            ) from exc

    async def show_available_models(self):
        """OpenAI-compatible /v1/models response.

        Synthesized directly from the endpoint's model serve_name —
        PrefillActor.show_available_models would just echo the same model
        (it's downloaded with the same model_args), so the extra Ray RPC
        hop adds latency without information. Matches OpenAI's response
        shape so the JSONResponse wrapper in PDIngress passes it through
        unchanged.
        """
        return {
            "object": "list",
            "data": [
                {
                    "id": self._model_serve_name,
                    "object": "model",
                    "created": int(time.time()),
                    "owned_by": "neutree",
                }
            ],
        }

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


def _find_role(pd_config: Dict[str, Any], name: str) -> Dict[str, Any]:
    roles = ((pd_config or {}).get("group") or {}).get("roles") or []
    for r in roles:
        if r.get("name") == name:
            return r
    raise RuntimeError(
        f"PD config missing role {name!r}; group.roles = {[r.get('name') for r in roles]}"
    )


def app_builder(args: Dict[str, Any]) -> Application:
    """PD same-host app_builder — xPyD parameterized + full EndpointSpec propagation."""
    model = args.get("model") or {}
    pd_config = args.get("pd_config") or {}

    num_replicas = max(1, int(pd_config.get("num_replicas") or 1))
    transfer = pd_config.get("transfer") or {}
    kv_extra = (transfer.get("extra") or {})

    prefill_role = _find_role(pd_config, "prefill")
    decode_role = _find_role(pd_config, "decode")

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
        pd_config = dict(pd_config)
        pd_config["backend_container"] = backend_container

    backend = PDCollocatedBackend.options(**backend_deploy_options).bind(
        model_args=model,
        prefill_engine_kwargs=prefill_engine_kwargs,
        decode_engine_kwargs=decode_engine_kwargs,
        kv_extra=kv_extra,
        prefill_count=prefill_count,
        decode_count=decode_count,
        prefill_actor_options=prefill_actor_options,
        decode_actor_options=decode_actor_options,
        pd_config=pd_config,
    )

    controller = PDIngress.options(
        max_ongoing_requests=100 * num_replicas,
        num_replicas=1,
        ray_actor_options={"num_cpus": 0.1, "num_gpus": 0},
    ).bind(backend=backend)

    return controller
