from __future__ import annotations

import json
import logging
import os
import random
import uuid
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import Any, Callable, Dict, Iterable, Mapping, Optional, Tuple
from urllib import error as urlerror
from urllib import request as urlrequest


LOG = logging.getLogger("neutree.pd_router")
CHAT_PATH = "/v1/chat/completions"
HEALTH_PATH = "/health"
GROUP_RANK_ENV = "NEUTREE_PD_GROUP_RANK"
ROUTE_PAYLOAD_KEYS = ("prefill_index", "decode_index")
ROUTE_HEADER_KEYS = {
    "prefill_index": "x-neutree-prefill-index",
    "decode_index": "x-neutree-decode-index",
}


@dataclass(frozen=True)
class RoleContainer:
    name: str
    role: str
    rank: int
    http_port: int
    side_channel_port: int = 0


@dataclass(frozen=True)
class RouterConfig:
    engine: str
    engine_version: str
    workspace: str
    endpoint: str
    group_id: str
    router_port: int
    router_health_path: str
    containers: Tuple[RoleContainer, ...]
    health_check_containers: bool = True

    @classmethod
    def from_env(cls) -> "RouterConfig":
        raw = os.environ.get("NEUTREE_PD_CONFIG", "")
        if not raw:
            raise ValueError("NEUTREE_PD_CONFIG is required")
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise ValueError(f"NEUTREE_PD_CONFIG is not valid JSON: {exc}") from exc

        if "NEUTREE_PD_ENGINE" in os.environ:
            data["engine"] = os.environ["NEUTREE_PD_ENGINE"]
        if "NEUTREE_PD_ENGINE_VERSION" in os.environ:
            data["engine_version"] = os.environ["NEUTREE_PD_ENGINE_VERSION"]
        if "NEUTREE_PD_GROUP_ID" in os.environ:
            data["group_id"] = os.environ["NEUTREE_PD_GROUP_ID"]
        if "NEUTREE_PD_ROUTER_PORT" in os.environ:
            router = dict(data.get("router") or {})
            router["port"] = os.environ["NEUTREE_PD_ROUTER_PORT"]
            data["router"] = router
        if GROUP_RANK_ENV in os.environ:
            data = _apply_group_rank_ports(data, _env_int(GROUP_RANK_ENV))

        config = cls.from_dict(data)
        if _env_bool("NEUTREE_PD_HEALTH_CHECK_CONTAINERS", config.health_check_containers) != config.health_check_containers:
            return cls(
                engine=config.engine,
                engine_version=config.engine_version,
                workspace=config.workspace,
                endpoint=config.endpoint,
                group_id=config.group_id,
                router_port=config.router_port,
                router_health_path=config.router_health_path,
                containers=config.containers,
                health_check_containers=_env_bool(
                    "NEUTREE_PD_HEALTH_CHECK_CONTAINERS",
                    config.health_check_containers,
                ),
            )
        return config

    @classmethod
    def from_dict(cls, data: Mapping[str, Any]) -> "RouterConfig":
        engine = str(data.get("engine") or data.get("engine_name") or "").strip().lower()
        if engine not in {"vllm", "sglang"}:
            raise ValueError(f"unsupported or missing PD router engine {engine!r}")

        router = data.get("router") or {}
        containers = []
        for raw in data.get("containers") or ():
            role = str(raw.get("role") or "").strip().lower()
            if role not in {"prefill", "decode"}:
                raise ValueError(f"unsupported PD role {role!r} in container {raw!r}")
            rank = int(raw.get("rank"))
            http_port = int(raw.get("http_port") or raw.get("port") or 0)
            if http_port <= 0:
                raise ValueError(f"container {raw.get('name')!r} has no http_port")
            containers.append(
                RoleContainer(
                    name=str(raw.get("name") or f"{role}-{rank}"),
                    role=role,
                    rank=rank,
                    http_port=http_port,
                    side_channel_port=int(raw.get("side_channel_port") or 0),
                )
            )
        if not containers:
            raise ValueError("PD router config requires at least one role container")

        by_role = {(c.role, c.rank) for c in containers}
        if not any(role == "prefill" for role, _ in by_role):
            raise ValueError("PD router config requires at least one prefill container")
        if not any(role == "decode" for role, _ in by_role):
            raise ValueError("PD router config requires at least one decode container")

        return cls(
            engine=engine,
            engine_version=str(data.get("engine_version") or ""),
            workspace=str(data.get("workspace") or ""),
            endpoint=str(data.get("endpoint") or ""),
            group_id=str(data.get("group_id") or data.get("endpoint") or ""),
            router_port=int(router.get("port") or 8000),
            router_health_path=str(router.get("health_path") or HEALTH_PATH),
            containers=tuple(sorted(containers, key=lambda c: (c.role, c.rank))),
            health_check_containers=bool(data.get("health_check_containers", True)),
        )

    def container(self, role: str, rank: int) -> RoleContainer:
        for container in self.containers:
            if container.role == role and container.rank == rank:
                return container
        raise IndexError(f"{role}_index {rank} is not configured")


@dataclass
class EngineHTTPResponse:
    status_code: int
    body: bytes = b""
    headers: Optional[Dict[str, str]] = None
    stream: Optional[Iterable[bytes]] = None

    @classmethod
    def json(cls, body: Mapping[str, Any], status_code: int = 200) -> "EngineHTTPResponse":
        return cls(
            status_code=status_code,
            body=json.dumps(body, separators=(",", ":")).encode("utf-8"),
            headers={"content-type": "application/json"},
        )

    def json_body(self) -> Any:
        return json.loads((self.body or b"{}").decode("utf-8"))

    def ok(self) -> bool:
        return 200 <= int(self.status_code) < 300


class UrllibEngineClient:
    def __init__(self, host: str = "127.0.0.1", timeout: float = 600.0, health_timeout: float = 0.5):
        self.host = host
        self.timeout = timeout
        self.health_timeout = health_timeout

    def post_json(
        self,
        container: RoleContainer,
        path: str,
        payload: Mapping[str, Any],
        stream: bool = False,
    ) -> EngineHTTPResponse:
        url = f"http://{self.host}:{container.http_port}{path}"
        data = json.dumps(payload).encode("utf-8")
        req = urlrequest.Request(
            url,
            data=data,
            method="POST",
            headers={"content-type": "application/json"},
        )
        try:
            resp = urlrequest.urlopen(req, timeout=self.timeout)
        except urlerror.HTTPError as exc:
            return EngineHTTPResponse(
                status_code=exc.code,
                headers={k.lower(): v for k, v in exc.headers.items()},
                body=exc.read(),
            )
        except urlerror.URLError as exc:
            return _error_response(f"engine request to {container.name} failed: {exc}", 502)

        headers = {k.lower(): v for k, v in resp.headers.items()}
        if stream:
            return EngineHTTPResponse(
                status_code=getattr(resp, "status", 200),
                headers=headers,
                stream=_iter_response(resp),
            )
        with resp:
            body = resp.read()
        return EngineHTTPResponse(status_code=getattr(resp, "status", 200), headers=headers, body=body)

    def get_health(self, container: RoleContainer, path: str = HEALTH_PATH, timeout: Optional[float] = None) -> bool:
        url = f"http://{self.host}:{container.http_port}{path}"
        req = urlrequest.Request(url, method="GET")
        try:
            with urlrequest.urlopen(req, timeout=timeout or self.health_timeout) as resp:
                return 200 <= getattr(resp, "status", 200) < 400
        except Exception:  # noqa: BLE001
            return False


class PDRouter:
    def __init__(self, config: RouterConfig, client: Optional[UrllibEngineClient] = None):
        self.config = config
        self.client = client or UrllibEngineClient()
        self._executor = ThreadPoolExecutor(max_workers=max(4, len(config.containers) * 2))

    def handle_chat(
        self,
        payload: Mapping[str, Any],
        prefill_index: int,
        decode_index: int,
    ) -> EngineHTTPResponse:
        clean_payload = _strip_route_fields(payload)
        try:
            prefill = self.config.container("prefill", int(prefill_index))
            decode = self.config.container("decode", int(decode_index))
        except (TypeError, ValueError, IndexError) as exc:
            return _error_response(f"invalid PD route indices: {exc}", 400)

        if self.config.engine == "vllm":
            return self._handle_vllm_chat(clean_payload, prefill, decode)
        if self.config.engine == "sglang":
            return self._handle_sglang_chat(clean_payload, prefill, decode)
        return _error_response(f"unsupported engine {self.config.engine!r}", 500)

    def health(self) -> EngineHTTPResponse:
        checks = self._container_health()
        ready = all(entry["ready"] for entry in checks.values()) if checks else False
        return EngineHTTPResponse.json(
            {
                "ready": ready,
                "engine": self.config.engine,
                "engine_version": self.config.engine_version,
                "workspace": self.config.workspace,
                "endpoint": self.config.endpoint,
                "group_id": self.config.group_id,
                "containers": checks,
            },
            status_code=200 if ready else 503,
        )

    def topology(self) -> Dict[str, Any]:
        checks = self._container_health()
        units = []
        for container in self.config.containers:
            health = checks.get(container.name, {})
            if bool(health.get("ready")):
                units.append({"role": container.role, "rank": container.rank})
        return {
            "group_id": self.config.group_id,
            "units": sorted(units, key=_role_rank_sort_key),
        }

    def metrics(self) -> str:
        checks = self._container_health()
        ready = 1 if checks and all(entry["ready"] for entry in checks.values()) else 0
        lines = [
            "# HELP neutree_pd_router_ready Whether the PD router local RoleGroup is ready.",
            "# TYPE neutree_pd_router_ready gauge",
            (
                "neutree_pd_router_ready"
                f'{{engine="{_metric_escape(self.config.engine)}",'
                f'engine_version="{_metric_escape(self.config.engine_version)}",'
                f'workspace="{_metric_escape(self.config.workspace)}",'
                f'endpoint="{_metric_escape(self.config.endpoint)}"}} {ready}'
            ),
            "# HELP neutree_pd_router_container_ready Whether a local PD role container is ready.",
            "# TYPE neutree_pd_router_container_ready gauge",
        ]
        for name, entry in sorted(checks.items()):
            rank = "" if entry.get("rank") is None else str(entry.get("rank"))
            value = 1 if entry.get("ready") else 0
            lines.append(
                "neutree_pd_router_container_ready"
                f'{{container="{_metric_escape(name)}",'
                f'role="{_metric_escape(str(entry.get("role") or ""))}",'
                f'rank="{_metric_escape(rank)}"}} {value}'
            )
        return "\n".join(lines) + "\n"

    def _handle_vllm_chat(
        self,
        payload: Mapping[str, Any],
        prefill: RoleContainer,
        decode: RoleContainer,
    ) -> EngineHTTPResponse:
        prefill_payload = dict(payload)
        prefill_payload.setdefault("request_id", uuid.uuid4().hex)
        prefill_payload["max_tokens"] = 1
        if "max_completion_tokens" in prefill_payload:
            prefill_payload["max_completion_tokens"] = 1
        prefill_payload["stream"] = False
        prefill_payload.pop("stream_options", None)
        prefill_payload["kv_transfer_params"] = {"do_remote_decode": True}
        req_id = str(prefill_payload["request_id"])

        LOG.info(
            "vLLM PD request req=%s prefill=%s decode=%s stream=%s",
            req_id,
            prefill.name,
            decode.name,
            bool(payload.get("stream")),
        )

        prefill_resp = self.client.post_json(prefill, CHAT_PATH, prefill_payload, stream=False)
        if not prefill_resp.ok():
            return _error_response(
                "prefill request failed",
                502,
                details={"status_code": prefill_resp.status_code, "body": _body_text(prefill_resp.body)},
            )
        try:
            prefill_body = prefill_resp.json_body()
        except (TypeError, ValueError) as exc:
            return _error_response(f"prefill response is not JSON: {exc}", 502)
        if isinstance(prefill_body, dict) and "error" in prefill_body:
            return _error_response("prefill request returned error", 502, details=prefill_body)

        kv_params = prefill_body.get("kv_transfer_params") if isinstance(prefill_body, dict) else None
        if kv_params is None:
            return _error_response("prefill response missing kv_transfer_params", 502)

        decode_payload = dict(payload)
        decode_payload["request_id"] = req_id
        decode_payload["kv_transfer_params"] = kv_params
        stream = bool(decode_payload.get("stream"))
        return self.client.post_json(decode, CHAT_PATH, decode_payload, stream=stream)

    def _handle_sglang_chat(
        self,
        payload: Mapping[str, Any],
        prefill: RoleContainer,
        decode: RoleContainer,
    ) -> EngineHTTPResponse:
        try:
            prefill_payload, decode_payload, req_id = self._sglang_payloads(payload, prefill)
        except (TypeError, ValueError) as exc:
            return _error_response(f"invalid SGLang PD request: {exc}", 400)

        stream = bool(decode_payload.get("stream"))
        LOG.info(
            "SGLang PD request req=%s prefill=%s decode=%s stream=%s bootstrap_port=%s",
            req_id,
            prefill.name,
            decode.name,
            stream,
            decode_payload.get("bootstrap_port"),
        )

        if stream:
            decode_future = self._executor.submit(
                self.client.post_json,
                decode,
                CHAT_PATH,
                decode_payload,
                True,
            )
            prefill_future = self._executor.submit(
                self._drain_stream_request,
                prefill,
                prefill_payload,
                req_id,
            )
            try:
                decode_resp = decode_future.result()
            except Exception as exc:  # noqa: BLE001
                prefill_future.cancel()
                return _error_response(f"decode request failed: {exc}", 502)
            if decode_resp.stream is not None:
                decode_resp.stream = _join_stream_with_future(decode_resp.stream, prefill_future, req_id)
            else:
                try:
                    prefill_future.result()
                except Exception as exc:  # noqa: BLE001
                    return _error_response(f"prefill request failed: {exc}", 502)
            return decode_resp

        decode_future = self._executor.submit(
            self.client.post_json,
            decode,
            CHAT_PATH,
            decode_payload,
            False,
        )
        prefill_future = self._executor.submit(
            self.client.post_json,
            prefill,
            CHAT_PATH,
            prefill_payload,
            False,
        )
        try:
            decode_resp = decode_future.result()
            prefill_resp = prefill_future.result()
        except Exception as exc:  # noqa: BLE001
            return _error_response(f"SGLang PD request failed: {exc}", 502)
        if not prefill_resp.ok():
            return _error_response(
                "prefill request failed",
                502,
                details={"status_code": prefill_resp.status_code, "body": _body_text(prefill_resp.body)},
            )
        if not decode_resp.ok():
            return _error_response(
                "decode request failed",
                502,
                details={"status_code": decode_resp.status_code, "body": _body_text(decode_resp.body)},
            )
        return decode_resp

    def _sglang_payloads(
        self,
        payload: Mapping[str, Any],
        prefill: RoleContainer,
    ) -> Tuple[Dict[str, Any], Dict[str, Any], str]:
        if prefill.side_channel_port <= 0:
            raise ValueError(f"prefill rank {prefill.rank} has no side_channel_port")
        req_id = str(payload.get("rid") or payload.get("request_id") or uuid.uuid4().hex)
        bootstrap_room = int(payload.get("bootstrap_room") or random.randint(0, 2**63 - 1))
        common = {
            "rid": req_id,
            "bootstrap_host": "127.0.0.1",
            "bootstrap_port": int(prefill.side_channel_port),
            "bootstrap_room": bootstrap_room,
            "stream": bool(payload.get("stream", False)),
        }
        prefill_payload = dict(payload)
        decode_payload = dict(payload)
        prefill_payload.update(common)
        decode_payload.update(common)
        return prefill_payload, decode_payload, req_id

    def _drain_stream_request(self, container: RoleContainer, payload: Mapping[str, Any], req_id: str) -> None:
        resp = self.client.post_json(container, CHAT_PATH, payload, stream=True)
        if not resp.ok():
            raise RuntimeError(f"prefill stream failed for {req_id}: HTTP {resp.status_code}")
        if resp.stream is not None:
            for _ in resp.stream:
                pass

    def _container_health(self) -> Dict[str, Dict[str, Any]]:
        checks = {}
        for container in self.config.containers:
            ready = True
            if self.config.health_check_containers:
                ready = self.client.get_health(container, HEALTH_PATH)
            checks[container.name] = {
                "role": container.role,
                "rank": container.rank,
                "http_port": container.http_port,
                "side_channel_port": container.side_channel_port,
                "ready": ready,
            }
        return checks


def create_app(runtime: PDRouter) -> Any:
    try:
        from fastapi import FastAPI, Request
        from fastapi.responses import Response, StreamingResponse
    except ModuleNotFoundError as exc:
        raise RuntimeError("pd-router FastAPI runtime requires fastapi") from exc

    app = FastAPI(
        title="neutree-pd-router",
        docs_url=None,
        redoc_url=None,
        openapi_url=None,
    )

    async def health_handler() -> Any:
        return _fastapi_response(runtime.health(), Response, StreamingResponse)

    async def topology_handler() -> Any:
        return _fastapi_response(EngineHTTPResponse.json(runtime.topology()), Response, StreamingResponse)

    async def metrics_handler() -> Any:
        return _fastapi_response(
            EngineHTTPResponse(
                status_code=200,
                body=runtime.metrics().encode("utf-8"),
                headers={"content-type": "text/plain; version=0.0.4"},
            ),
            Response,
            StreamingResponse,
        )

    async def chat_completion_handler(request) -> Any:
        try:
            payload = await _read_fastapi_json(request)
            prefill_index, decode_index, clean = extract_route_indices(
                payload,
                headers={k.lower(): v for k, v in request.headers.items()},
                query=request.query_params,
            )
        except ValueError as exc:
            return _fastapi_response(_error_response(str(exc), 400), Response, StreamingResponse)

        return _fastapi_response(
            runtime.handle_chat(clean, prefill_index, decode_index),
            Response,
            StreamingResponse,
        )

    # FastAPI inspects annotations during route registration; bind the lazy import explicitly.
    chat_completion_handler.__annotations__["request"] = Request

    app.add_api_route(HEALTH_PATH, health_handler, methods=["GET"])
    if runtime.config.router_health_path != HEALTH_PATH:
        app.add_api_route(runtime.config.router_health_path, health_handler, methods=["GET"])
    app.add_api_route("/v1/topology", topology_handler, methods=["GET"])
    app.add_api_route("/metrics", metrics_handler, methods=["GET"])
    app.add_api_route(CHAT_PATH, chat_completion_handler, methods=["POST"])
    return app


def extract_route_indices(
    payload: Mapping[str, Any],
    headers: Mapping[str, Any],
    query: Mapping[str, Any],
) -> Tuple[int, int, Dict[str, Any]]:
    prefill_raw = _first_value(query.get("prefill_index"))
    if prefill_raw is None:
        prefill_raw = _first_value(query.get("prefill"))
    if prefill_raw is None:
        prefill_raw = _header_value(headers, ROUTE_HEADER_KEYS["prefill_index"])
    if prefill_raw is None:
        prefill_raw = payload.get("prefill_index")

    decode_raw = _first_value(query.get("decode_index"))
    if decode_raw is None:
        decode_raw = _first_value(query.get("decode"))
    if decode_raw is None:
        decode_raw = _header_value(headers, ROUTE_HEADER_KEYS["decode_index"])
    if decode_raw is None:
        decode_raw = payload.get("decode_index")

    if prefill_raw is None or decode_raw is None:
        raise ValueError("prefill_index and decode_index are required")
    try:
        prefill_index = int(prefill_raw)
        decode_index = int(decode_raw)
    except (TypeError, ValueError) as exc:
        raise ValueError("prefill_index and decode_index must be integers") from exc
    if prefill_index < 0 or decode_index < 0:
        raise ValueError("prefill_index and decode_index must be non-negative")
    return prefill_index, decode_index, _strip_route_fields(payload)


def run_server(runtime: PDRouter, host: str = "0.0.0.0", port: Optional[int] = None) -> None:
    LOG.info(
        "pd-router listening on %s:%s engine=%s engine_version=%s workspace=%s endpoint=%s",
        host,
        port or runtime.config.router_port,
        runtime.config.engine,
        runtime.config.engine_version,
        runtime.config.workspace,
        runtime.config.endpoint,
    )
    try:
        import uvicorn
    except ModuleNotFoundError as exc:
        raise RuntimeError("pd-router FastAPI runtime requires uvicorn") from exc
    uvicorn.run(create_app(runtime), host=host, port=int(port or runtime.config.router_port))


def _strip_route_fields(payload: Mapping[str, Any]) -> Dict[str, Any]:
    clean = dict(payload)
    for key in ROUTE_PAYLOAD_KEYS:
        clean.pop(key, None)
    return clean


def _error_response(message: str, status_code: int, details: Optional[Mapping[str, Any]] = None) -> EngineHTTPResponse:
    error = {"message": message, "type": "pd_router_error", "code": status_code}
    if details:
        error["details"] = dict(details)
    return EngineHTTPResponse.json({"error": error}, status_code=status_code)


def _apply_group_rank_ports(data: Mapping[str, Any], group_rank: int) -> Dict[str, Any]:
    if group_rank < 0:
        raise ValueError(f"{GROUP_RANK_ENV} must be >= 0, got {group_rank}")

    out = dict(data)
    ports = out.get("ports")
    if not isinstance(ports, list):
        raise ValueError(f"{GROUP_RANK_ENV} requires pd_config.ports")
    if group_rank >= len(ports):
        raise ValueError(f"{GROUP_RANK_ENV}={group_rank} is outside pd_config.ports length {len(ports)}")

    replica_ports = ports[group_rank]
    if not isinstance(replica_ports, Mapping):
        raise ValueError(f"pd_config.ports[{group_rank}] must be an object")

    router_port = _group_rank_port(replica_ports, "router", 0, "http")
    router = dict(out.get("router") or {})
    router["port"] = router_port
    out["router"] = router

    containers = []
    for raw in out.get("containers") or ():
        container = dict(raw)
        role = str(container.get("role") or "").strip().lower()
        rank = int(container.get("rank"))
        container["http_port"] = _group_rank_port(replica_ports, role, rank, "http")
        side_channel_port = _group_rank_port(replica_ports, role, rank, "side_channel", required=False)
        if side_channel_port > 0:
            container["side_channel_port"] = side_channel_port
        containers.append(container)
    out["containers"] = containers

    return out


def _group_rank_port(replica_ports: Mapping[str, Any], role: str, rank: int, purpose: str, required: bool = True) -> int:
    try:
        role_ports = replica_ports[role]
        rank_ports = role_ports[rank]
        port = int(rank_ports.get(purpose) or 0)
    except (IndexError, KeyError, TypeError, ValueError, AttributeError) as exc:
        if not required:
            return 0
        raise ValueError(f"missing port for role={role} rank={rank} purpose={purpose}") from exc

    if port <= 0 and required:
        raise ValueError(f"missing port for role={role} rank={rank} purpose={purpose}")
    return port


async def _read_fastapi_json(request: Any) -> Dict[str, Any]:
    raw = await request.body()
    if not raw:
        return {}
    try:
        payload = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValueError(f"request body is not valid JSON: {exc}") from exc
    if not isinstance(payload, dict):
        raise ValueError("request body must be a JSON object")
    return payload


def _fastapi_response(response: EngineHTTPResponse, response_cls: Any, streaming_response_cls: Any) -> Any:
    headers = {str(k).lower(): str(v) for k, v in (response.headers or {}).items()}
    forwarded_headers = _forward_response_headers(headers)
    if response.stream is not None:
        return streaming_response_cls(
            _stream_with_close(response.stream),
            status_code=response.status_code,
            media_type=headers.get("content-type", "text/event-stream"),
            headers=forwarded_headers,
        )
    return response_cls(
        content=response.body or b"",
        status_code=response.status_code,
        media_type=headers.get("content-type", "application/json"),
        headers=forwarded_headers,
    )


def _forward_response_headers(headers: Mapping[str, str]) -> Dict[str, str]:
    hop_by_hop = {"connection", "content-length", "content-type", "transfer-encoding"}
    return {k: v for k, v in headers.items() if k not in hop_by_hop}


def _stream_with_close(stream: Iterable[bytes]) -> Iterable[bytes]:
    try:
        for chunk in stream:
            if isinstance(chunk, str):
                chunk = chunk.encode("utf-8")
            yield chunk
    finally:
        _close_iterable(stream)


def _iter_response(resp: Any) -> Iterable[bytes]:
    try:
        while True:
            chunk = resp.readline()
            if not chunk:
                break
            yield chunk
    finally:
        resp.close()


class _SGLangJoinedStream:
    def __init__(self, stream: Iterable[bytes], future: Any, req_id: str):
        self._stream = stream
        self._iterator = iter(stream)
        self._future = future
        self._req_id = req_id
        self._closed = False

    def __iter__(self) -> "_SGLangJoinedStream":
        return self

    def __next__(self) -> bytes:
        if self._closed:
            raise StopIteration
        try:
            return next(self._iterator)
        except StopIteration:
            try:
                self._future.result()
            except Exception as exc:  # noqa: BLE001
                LOG.warning("SGLang prefill stream failed for req=%s: %s", self._req_id, exc)
                raise
            raise

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        _close_iterable(self._stream)
        cancel = getattr(self._future, "cancel", None)
        if callable(cancel):
            cancel()


def _join_stream_with_future(stream: Iterable[bytes], future: Any, req_id: str) -> Iterable[bytes]:
    return _SGLangJoinedStream(stream, future, req_id)


def _close_iterable(stream: Any) -> None:
    close: Optional[Callable[[], Any]] = getattr(stream, "close", None)
    if callable(close):
        try:
            close()
        except Exception as exc:  # noqa: BLE001
            LOG.warning("failed to close upstream stream: %s", exc)


def _first_value(value: Any) -> Any:
    if isinstance(value, (list, tuple)):
        return value[0] if value else None
    return value


def _header_value(headers: Mapping[str, Any], key: str) -> Any:
    lower = {str(k).lower(): v for k, v in headers.items()}
    return lower.get(key.lower())


def _body_text(body: bytes) -> str:
    try:
        return (body or b"").decode("utf-8")
    except UnicodeDecodeError:
        return repr(body)


def _env_bool(name: str, default: bool) -> bool:
    raw = os.environ.get(name)
    if raw is None:
        return default
    return raw.strip().lower() not in {"0", "false", "no", "off"}


def _env_int(name: str) -> int:
    raw = os.environ.get(name, "")
    try:
        return int(raw)
    except ValueError as exc:
        raise ValueError(f"{name} must be an integer, got {raw!r}") from exc


def _metric_escape(value: str) -> str:
    return value.replace("\\", "\\\\").replace("\n", "\\n").replace('"', '\\"')


def _role_rank_sort_key(unit: Mapping[str, Any]) -> Tuple[int, int]:
    role_order = {"prefill": 0, "decode": 1}
    role = str(unit.get("role") or "")
    return role_order.get(role, 99), int(unit.get("rank") or 0)
