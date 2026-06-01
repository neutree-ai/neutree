"""PD same-host collocated app builder for SGLang v0.5.10.

Runtime shape matches the vLLM PD app:

    PDRouter
        -> PDCollocatedBackend replica
             -> PrefillActor[0..x-1]
             -> DecodeActor[0..y-1]

SGLang's protocol is different from vLLM. The backend creates one bootstrap
correlation per request, injects it into both OpenAI-compatible payloads, then
runs prefill and decode concurrently. Decode streams the client response after
SGLang observes KV readiness through the bootstrap room.
"""

from __future__ import annotations

import asyncio
import logging
import random
import time
import uuid
from typing import Any, Dict, List, Optional

import ray
from ray import serve
from ray.serve import Application
from ray.serve._private.constants import SERVE_LOGGER_NAME
from ray.serve.config import RequestRouterConfig
from ray.util.placement_group import PlacementGroupSchedulingStrategy, placement_group

from serve._router.pd_router import PDRouter
from serve._utils.runtime_env import build_backend_runtime_env
from serve.sglang.v0_5_10.app import _Backend as Backend


log = logging.getLogger(SERVE_LOGGER_NAME)
asyncio_gather = asyncio.gather
ROUTE_PAYLOAD_KEYS = ("prefill_index", "decode_index")

PLATFORM_ENV_KEYS = {
    "NEUTREE_RAY_STAT_DEPLOYMENT",
    "NEUTREE_RAY_STAT_REPLICA",
    "NEUTREE_RAY_STAT_APPLICATION",
    "NEUTREE_RAY_STAT_ROLE",
    "NEUTREE_RAY_STAT_RANK",
    "PYTHONUNBUFFERED",
}
PLATFORM_ENGINE_KWARG_KEYS = {
    "disaggregation_mode",
    "disaggregation_transfer_backend",
    "disaggregation_bootstrap_port",
}


def _merge_user_wins(
    platform: Dict[str, Any],
    user: Dict[str, Any],
    audit_keys: set,
    context: str,
) -> Dict[str, Any]:
    merged: Dict[str, Any] = {}
    merged.update(platform or {})
    if user:
        for key, value in user.items():
            if key in audit_keys and key in merged:
                log.warning(
                    "[sglang_pd][%s] user overriding platform-controlled key %r=%r (was %r)",
                    context,
                    key,
                    value,
                    merged[key],
                )
        merged.update(user)
    return merged


def _actor_options_to_bundle(actor_options: Dict[str, Any]) -> Dict[str, float]:
    bundle: Dict[str, float] = {}
    if "num_cpus" in actor_options:
        bundle["CPU"] = float(actor_options["num_cpus"])
    if "num_gpus" in actor_options:
        bundle["GPU"] = float(actor_options["num_gpus"])
    if "memory" in actor_options:
        bundle["memory"] = float(actor_options["memory"])
    for key, value in (actor_options.get("resources") or {}).items():
        bundle[key] = float(value)
    return bundle


def _role_resources_to_ray(role: Dict[str, Any]) -> Dict[str, Any]:
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
    if not opts:
        opts = {"num_cpus": 1, "num_gpus": 1}
    return opts


def _sglang_port(pd_config: Dict[str, Any], role_group_index: int, role: str, rank: int) -> int:
    """Return the side-channel port assigned to one SGLang P/D rank."""
    if not pd_config or "ports" not in pd_config or pd_config["ports"] is None:
        raise RuntimeError(
            f"SGLang PD requires pd_config.ports populated by portalloc; "
            f"got none for {role} rank {rank} role_group {role_group_index}"
        )
    try:
        slot = pd_config["ports"][role_group_index][role][rank]
    except (IndexError, KeyError, TypeError) as exc:
        raise RuntimeError(
            f"missing SGLang port slot for role_group={role_group_index} "
            f"role={role} rank={rank}: {exc}"
        ) from exc
    if not slot:
        raise RuntimeError(
            f"empty SGLang port slot for role_group={role_group_index} role={role} rank={rank}"
        )
    return _port_value(slot, "side_channel", role_group_index, role, rank)


def _port_value(slot: Any, purpose: str, role_group_index: int, role: str, rank: int) -> int:
    if isinstance(slot, dict):
        port = int(slot.get(purpose) or 0)
    elif isinstance(slot, (list, tuple)) and slot:
        port = int(slot[0])
    else:
        port = 0
    if port <= 0:
        raise RuntimeError(
            f"empty SGLang {purpose} port slot for role_group={role_group_index} "
            f"role={role} rank={rank}: {slot!r}"
        )
    return port


def _extract_route_indices(payload: Dict[str, Any]):
    clean = dict(payload or {})
    prefill_index = clean.pop(ROUTE_PAYLOAD_KEYS[0], None)
    decode_index = clean.pop(ROUTE_PAYLOAD_KEYS[1], None)
    return clean, prefill_index, decode_index


def _role_actor_name(pd_config: Dict[str, Any], role_group_key: str, role: str, rank: int) -> str:
    workspace = str((pd_config or {}).get("workspace") or "")
    endpoint = str((pd_config or {}).get("endpoint") or "")
    role_group_key = str(role_group_key or "")
    if not workspace or not endpoint or not role_group_key:
        raise RuntimeError(
            "SGLang PD role actor naming requires pd_config.workspace, "
            "pd_config.endpoint, and a non-empty Serve replica key"
        )
    return (
        f"neutree:{workspace}:{endpoint}:replica:{role_group_key}:"
        f"role:{role}:rank:{rank}"
    )


def _build_actor_runtime_env(
    role_env: Dict[str, str],
    metrics_env: Dict[str, str],
    backend_container: Optional[Dict[str, Any]],
) -> Dict[str, Any]:
    runtime_env: Dict[str, Any] = {}
    inherited_env: Dict[str, str] = {}
    if backend_container:
        merged = build_backend_runtime_env(backend_container)
        if "container" in merged:
            runtime_env["container"] = merged["container"]
        inherited_env.update(merged.get("env_vars") or {})

    platform_env = {}
    platform_env.update(inherited_env)
    platform_env.update(metrics_env or {})
    env_vars = _merge_user_wins(
        platform=platform_env,
        user=role_env or {},
        audit_keys=PLATFORM_ENV_KEYS,
        context="env_vars",
    )
    if env_vars:
        runtime_env["env_vars"] = env_vars
    return runtime_env


def _transfer_backend(connector: str, kv_extra: Dict[str, Any]) -> str:
    backend = (
        (kv_extra or {}).get("transfer_backend")
        or (kv_extra or {}).get("backend")
        or connector
        or "nixl"
    )
    return str(backend).strip().lower()


def _platform_engine_kwargs(
    role: str,
    connector: str,
    kv_extra: Dict[str, Any],
    bootstrap_port: Optional[int] = None,
) -> Dict[str, Any]:
    kwargs: Dict[str, Any] = {
        "disaggregation_mode": role,
        "disaggregation_transfer_backend": _transfer_backend(connector, kv_extra),
    }
    if bootstrap_port is not None:
        kwargs["disaggregation_bootstrap_port"] = int(bootstrap_port)
    if "disaggregation_ib_device" in (kv_extra or {}):
        kwargs["disaggregation_ib_device"] = kv_extra["disaggregation_ib_device"]
    return kwargs


def _is_error_payload(value: Any) -> bool:
    if not isinstance(value, dict):
        return False
    return "error" in value or value.get("object") == "error"


@ray.remote
class PrefillActor(Backend):
    def __init__(
        self,
        *,
        model_args: Dict[str, Any],
        engine_kwargs: Dict[str, Any],
        connector: str,
        kv_extra: Dict[str, Any],
        bootstrap_port: int,
    ):
        merged_kwargs = _merge_user_wins(
            platform=_platform_engine_kwargs(
                "prefill", connector, kv_extra, bootstrap_port=bootstrap_port,
            ),
            user=engine_kwargs or {},
            audit_keys=PLATFORM_ENGINE_KWARG_KEYS,
            context="engine_kwargs/prefill",
        )
        self.bootstrap_port = int(merged_kwargs.get("disaggregation_bootstrap_port"))
        log.info(
            "[SGLangPrefillActor][init] transfer_backend=%s bootstrap_port=%s keys=%s",
            merged_kwargs.get("disaggregation_transfer_backend"),
            self.bootstrap_port,
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

    def get_self_info(self) -> Dict[str, Any]:
        return {
            "kind": "prefill",
            "actor_id": str(self.actor_id),
            "node_id": str(self.node_id),
            "gpu_ids": [int(g) for g in self.gpu_ids],
            "bootstrap_port": int(self.bootstrap_port),
        }

    async def chat_completion_stream_actor(self, payload: Dict[str, Any]):
        async for chunk in self.chat_completion_stream(payload):
            yield chunk


@ray.remote
class DecodeActor(Backend):
    def __init__(
        self,
        *,
        model_args: Dict[str, Any],
        engine_kwargs: Dict[str, Any],
        connector: str,
        kv_extra: Dict[str, Any],
    ):
        merged_kwargs = _merge_user_wins(
            platform=_platform_engine_kwargs("decode", connector, kv_extra),
            user=engine_kwargs or {},
            audit_keys=PLATFORM_ENGINE_KWARG_KEYS,
            context="engine_kwargs/decode",
        )
        log.info(
            "[SGLangDecodeActor][init] transfer_backend=%s keys=%s",
            merged_kwargs.get("disaggregation_transfer_backend"),
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

    def get_self_info(self) -> Dict[str, Any]:
        return {
            "kind": "decode",
            "actor_id": str(self.actor_id),
            "node_id": str(self.node_id),
            "gpu_ids": [int(g) for g in self.gpu_ids],
        }

    async def chat_completion_stream_actor(self, payload: Dict[str, Any]):
        async for chunk in self.chat_completion_stream(payload):
            yield chunk


@serve.deployment(ray_actor_options={"num_cpus": 0.1, "num_gpus": 0})
class PDCollocatedBackend:
    def __init__(
        self,
        *,
        model_args: Dict[str, Any],
        prefill_engine_kwargs: Dict[str, Any],
        decode_engine_kwargs: Dict[str, Any],
        connector: str,
        kv_extra: Dict[str, Any],
        prefill_count: int,
        decode_count: int,
        prefill_actor_options: Dict[str, Any],
        decode_actor_options: Dict[str, Any],
        pd_config: Optional[Dict[str, Any]] = None,
    ):
        if prefill_count <= 0 or decode_count <= 0:
            raise ValueError(
                f"SGLang PD requires prefill_count>0 and decode_count>0, "
                f"got prefill={prefill_count} decode={decode_count}"
            )
        self._pd_config = pd_config or {}
        self._prefill_count = prefill_count
        self._decode_count = decode_count
        self._model_serve_name = (
            (model_args or {}).get("serve_name")
            or (model_args or {}).get("name", "")
        )

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

        prefill_bundle = _actor_options_to_bundle(prefill_actor_options)
        decode_bundle = _actor_options_to_bundle(decode_actor_options)
        bundles = (
            [dict(prefill_bundle) for _ in range(prefill_count)]
            + [dict(decode_bundle) for _ in range(decode_count)]
        )
        self.pg = placement_group(bundles, strategy="STRICT_PACK")
        ray.get(self.pg.ready())
        pg_id_bytes = getattr(self.pg, "id", None)
        self._pg_id_str = pg_id_bytes.hex() if isinstance(pg_id_bytes, bytes) else str(self.pg)

        role_group_index = self.global_rank if self.global_rank >= 0 else 0
        prefill_role = self._role_dict("prefill")
        decode_role = self._role_dict("decode")
        prefill_env = prefill_role.get("env") or {}
        decode_env = decode_role.get("env") or {}
        backend_container = self._pd_config.get("backend_container") if self._pd_config else None

        metrics_env_base = {"PYTHONUNBUFFERED": "1"}
        try:
            rc = serve.get_replica_context()
            dep = getattr(rc, "deployment", "") or ""
            rtag = getattr(rc, "replica_tag", "") or getattr(rc, "replica_id", "") or ""
            app_name = getattr(rc, "app_name", "") or ""
            if dep:
                metrics_env_base["NEUTREE_RAY_STAT_DEPLOYMENT"] = str(dep)
            if rtag:
                metrics_env_base["NEUTREE_RAY_STAT_REPLICA"] = str(rtag)
            if app_name:
                metrics_env_base["NEUTREE_RAY_STAT_APPLICATION"] = str(app_name)
        except Exception as exc:  # noqa: BLE001
            log.warning("[SGLangPDCollocatedBackend] no Serve context for metrics labels: %s", exc)

        self.prefills: List[Any] = []
        for rank in range(prefill_count):
            bootstrap_port = _sglang_port(self._pd_config, role_group_index, "prefill", rank)
            metrics_env = dict(metrics_env_base)
            metrics_env["NEUTREE_RAY_STAT_ROLE"] = "prefill"
            metrics_env["NEUTREE_RAY_STAT_RANK"] = str(rank)
            opts = dict(prefill_actor_options)
            opts["scheduling_strategy"] = PlacementGroupSchedulingStrategy(
                placement_group=self.pg,
                placement_group_bundle_index=rank,
            )
            runtime_env = _build_actor_runtime_env(prefill_env, metrics_env, backend_container)
            if runtime_env:
                opts["runtime_env"] = runtime_env
            opts["name"] = _role_actor_name(self._pd_config, self.replica_id_str, "prefill", rank)
            self.prefills.append(
                PrefillActor.options(**opts).remote(
                    model_args=model_args,
                    engine_kwargs=prefill_engine_kwargs,
                    connector=connector,
                    kv_extra=kv_extra,
                    bootstrap_port=bootstrap_port,
                )
            )

        self.decodes: List[Any] = []
        for rank in range(decode_count):
            metrics_env = dict(metrics_env_base)
            metrics_env["NEUTREE_RAY_STAT_ROLE"] = "decode"
            metrics_env["NEUTREE_RAY_STAT_RANK"] = str(rank)
            opts = dict(decode_actor_options)
            opts["scheduling_strategy"] = PlacementGroupSchedulingStrategy(
                placement_group=self.pg,
                placement_group_bundle_index=prefill_count + rank,
            )
            runtime_env = _build_actor_runtime_env(decode_env, metrics_env, backend_container)
            if runtime_env:
                opts["runtime_env"] = runtime_env
            opts["name"] = _role_actor_name(self._pd_config, self.replica_id_str, "decode", rank)
            self.decodes.append(
                DecodeActor.options(**opts).remote(
                    model_args=model_args,
                    engine_kwargs=decode_engine_kwargs,
                    connector=connector,
                    kv_extra=kv_extra,
                )
            )

        prefill_infos = ray.get([h.get_self_info.remote() for h in self.prefills])
        decode_infos = ray.get([h.get_self_info.remote() for h in self.decodes])
        self.prefill_bootstrap_ports = [
            int(info.get("bootstrap_port") or 0) for info in prefill_infos
        ]
        all_nodes = [i.get("node_id") for i in prefill_infos + decode_infos]
        log.info(
            "[SGLangPDCollocatedBackend][init/done] %dP + %dD actors ready "
            "same_host=%s nodes=%s bootstrap_ports=%s",
            prefill_count,
            decode_count,
            len(set(all_nodes)) == 1 if all_nodes else False,
            sorted(set(all_nodes)),
            self.prefill_bootstrap_ports,
        )

    def _role_dict(self, name: str) -> Dict[str, Any]:
        roles = ((self._pd_config or {}).get("group") or {}).get("roles") or []
        for role in roles:
            if role.get("name") == name:
                return role
        return {}

    def _pick_prefill(self, index: Optional[int]):
        if index is None:
            raise ValueError("prefill_index is required")
        idx = int(index)
        if idx < 0 or idx >= self._prefill_count:
            raise IndexError(f"prefill_index {idx} out of range [0,{self._prefill_count})")
        return idx, self.prefills[idx]

    def _pick_decode(self, index: Optional[int]):
        if index is None:
            raise ValueError("decode_index is required")
        idx = int(index)
        if idx < 0 or idx >= self._decode_count:
            raise IndexError(f"decode_index {idx} out of range [0,{self._decode_count})")
        return idx, self.decodes[idx]

    def _pd_payloads(
        self,
        payload: Dict[str, Any],
        prefill_idx: int,
        stream: bool,
    ) -> tuple[Dict[str, Any], Dict[str, Any], str]:
        req_id = str(payload.get("rid") or payload.get("request_id") or uuid.uuid4().hex)
        bootstrap_room = int(payload.get("bootstrap_room") or random.randint(0, 2**63 - 1))
        bootstrap_port = self.prefill_bootstrap_ports[prefill_idx]
        if bootstrap_port <= 0:
            raise RuntimeError(f"prefill rank {prefill_idx} has no bootstrap port")

        common = {
            "rid": req_id,
            "bootstrap_host": str(payload.get("bootstrap_host") or "127.0.0.1"),
            "bootstrap_port": int(payload.get("bootstrap_port") or bootstrap_port),
            "bootstrap_room": bootstrap_room,
            "stream": stream,
        }
        prefill_payload = dict(payload)
        decode_payload = dict(payload)
        prefill_payload.update(common)
        decode_payload.update(common)
        # Keep a deliberately explicit hook for future DP-attention routing:
        # SGLang supports disagg_prefill_dp_rank on the decode request, but
        # same-host actor rank is not the same thing as a DP rank, so do not
        # synthesize it by default.
        if "disagg_prefill_dp_rank" in payload:
            decode_payload["disagg_prefill_dp_rank"] = payload["disagg_prefill_dp_rank"]
        return prefill_payload, decode_payload, req_id

    async def generate(
        self,
        payload: Dict[str, Any],
    ):
        try:
            clean_payload, prefill_index, decode_index = _extract_route_indices(payload)
            prefill_idx, prefill_handle = self._pick_prefill(prefill_index)
            decode_idx, decode_handle = self._pick_decode(decode_index)
            stream = bool((clean_payload or {}).get("stream", False))
            prefill_payload, decode_payload, req_id = self._pd_payloads(
                clean_payload, prefill_idx, stream,
            )
        except (TypeError, ValueError, IndexError, RuntimeError) as exc:
            return {"error": f"invalid SGLang PD request: {exc}"}

        log.info(
            "[sglang_pd_generate][pair] req=%s prefill_rank=%d decode_rank=%d stream=%s "
            "bootstrap_host=%s bootstrap_port=%s bootstrap_room=%s",
            req_id,
            prefill_idx,
            decode_idx,
            stream,
            decode_payload.get("bootstrap_host"),
            decode_payload.get("bootstrap_port"),
            decode_payload.get("bootstrap_room"),
        )

        if stream:
            return self._stream_generate(prefill_handle, decode_handle, prefill_payload, decode_payload, req_id)

        decode_ref = decode_handle.chat_completion.remote(decode_payload)
        prefill_ref = prefill_handle.chat_completion.remote(prefill_payload)
        decode_resp, prefill_resp = await asyncio_gather(
            decode_ref,
            prefill_ref,
        )
        if _is_error_payload(prefill_resp):
            log.warning("[sglang_pd_generate][prefill_error] req=%s resp=%s", req_id, prefill_resp)
            return {"error": prefill_resp}
        if _is_error_payload(decode_resp):
            log.warning("[sglang_pd_generate][decode_error] req=%s resp=%s", req_id, decode_resp)
            return {"error": decode_resp}
        return decode_resp

    async def _drain_actor_stream(self, actor_stream, req_id: str, role: str) -> None:
        try:
            if hasattr(actor_stream, "__aiter__"):
                async for ref in actor_stream:
                    await ref
            else:
                await actor_stream
        except Exception as exc:  # noqa: BLE001
            log.warning("[sglang_pd_generate][%s_stream_error] req=%s err=%s", role, req_id, exc)
            raise

    async def _stream_generate(
        self,
        prefill_handle,
        decode_handle,
        prefill_payload: Dict[str, Any],
        decode_payload: Dict[str, Any],
        req_id: str,
    ):
        decode_stream = decode_handle.chat_completion_stream_actor.remote(decode_payload)
        prefill_stream = prefill_handle.chat_completion_stream_actor.remote(prefill_payload)
        prefill_task = asyncio.create_task(
            self._drain_actor_stream(prefill_stream, req_id, "prefill")
        )
        try:
            async for ref in decode_stream:
                yield await ref
            await prefill_task
        finally:
            if not prefill_task.done():
                prefill_task.cancel()

    def _teardown_inner_actors(self) -> None:
        try:
            for handle in list(getattr(self, "prefills", []) or []) + list(getattr(self, "decodes", []) or []):
                try:
                    ray.kill(handle, no_restart=True)
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
        self._teardown_inner_actors()

    async def check_health(self):
        handles = list(self.prefills) + list(self.decodes)
        if not handles:
            raise RuntimeError("[SGLangPDCollocatedBackend] no inner actors to probe")
        try:
            await asyncio.wait_for(
                asyncio_gather(*[h.get_self_info.remote() for h in handles]),
                timeout=5.0,
            )
        except Exception as exc:  # noqa: BLE001
            raise RuntimeError(
                f"[SGLangPDCollocatedBackend] inner actor health probe failed: {exc}"
            ) from exc

    async def show_available_models(self):
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
            *[actor.get_self_info.remote() for actor in self.prefills]
        )
        decode_infos = await asyncio_gather(
            *[actor.get_self_info.remote() for actor in self.decodes]
        )
        prefill_infos = [dict(info, healthy=True) for info in prefill_infos]
        decode_infos = [dict(info, healthy=True) for info in decode_infos]
        all_nodes = (
            [info.get("node_id") for info in prefill_infos]
            + [info.get("node_id") for info in decode_infos]
        )
        same_host = (
            len(all_nodes) > 0
            and all(node is not None for node in all_nodes)
            and len(set(all_nodes)) == 1
        )
        units = [
            {"role": "prefill", "rank": rank}
            for rank, _ in enumerate(prefill_infos)
        ] + [
            {"role": "decode", "rank": rank}
            for rank, _ in enumerate(decode_infos)
        ]
        return {
            "group_id": self.replica_id_str,
            "units": units,
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


def _find_role(pd_config: Dict[str, Any], name: str) -> Dict[str, Any]:
    roles = ((pd_config or {}).get("group") or {}).get("roles") or []
    for role in roles:
        if role.get("name") == name:
            return role
    raise RuntimeError(
        f"PD config missing role {name!r}; group.roles = {[r.get('name') for r in roles]}"
    )


def app_builder(args: Dict[str, Any]) -> Application:
    model = args.get("model") or {}
    pd_config = args.get("pd_config") or {}

    num_replicas = max(1, int(pd_config.get("num_replicas") or 1))
    transfer = pd_config.get("transfer") or {}
    connector = str(transfer.get("connector") or "nixl")
    kv_extra = transfer.get("extra") or {}

    prefill_role = _find_role(pd_config, "prefill")
    decode_role = _find_role(pd_config, "decode")

    prefill_count = int(prefill_role.get("instances") or 1)
    decode_count = int(decode_role.get("instances") or 1)
    prefill_actor_options = _role_resources_to_ray(prefill_role)
    decode_actor_options = _role_resources_to_ray(decode_role)

    if args.get("backend_container"):
        pd_config = dict(pd_config)
        pd_config["backend_container"] = args["backend_container"]

    backend = PDCollocatedBackend.options(
        num_replicas=num_replicas,
        max_ongoing_requests=100,
        ray_actor_options={"num_cpus": 0.1, "num_gpus": 0},
        request_router_config=RequestRouterConfig(
            request_router_class="serve._router.observer_router:ObserverRouter",
            request_router_kwargs={},
        ),
    ).bind(
        model_args=model,
        prefill_engine_kwargs=dict(prefill_role.get("variables") or {}),
        decode_engine_kwargs=dict(decode_role.get("variables") or {}),
        connector=connector,
        kv_extra=kv_extra,
        prefill_count=prefill_count,
        decode_count=decode_count,
        prefill_actor_options=prefill_actor_options,
        decode_actor_options=decode_actor_options,
        pd_config=pd_config,
    )

    return PDRouter.options(
        max_ongoing_requests=100 * num_replicas,
        num_replicas=1,
        ray_actor_options={"num_cpus": 0.1, "num_gpus": 0},
    ).bind(backend=backend)
