from __future__ import annotations

import json
import logging
import os
import random
import uuid
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, Iterable, Mapping, Optional, Tuple
from urllib import error as urlerror
from urllib import parse, request


LOG = logging.getLogger("neutree.pd_router_sidecar")
CHAT_PATH = "/v1/chat/completions"
HEALTH_PATH = "/health"
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
class SidecarConfig:
    engine: str
    engine_version: str
    workspace: str
    endpoint: str
    sidecar_port: int
    sidecar_health_path: str
    containers: Tuple[RoleContainer, ...]
    health_check_containers: bool = True

    @classmethod
    def from_env(cls) -> "SidecarConfig":
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
        if "NEUTREE_PD_SIDECAR_PORT" in os.environ:
            sidecar = dict(data.get("sidecar") or {})
            sidecar["port"] = os.environ["NEUTREE_PD_SIDECAR_PORT"]
            data["sidecar"] = sidecar

        config = cls.from_dict(data)
        if _env_bool("NEUTREE_PD_HEALTH_CHECK_CONTAINERS", config.health_check_containers) != config.health_check_containers:
            return cls(
                engine=config.engine,
                engine_version=config.engine_version,
                workspace=config.workspace,
                endpoint=config.endpoint,
                sidecar_port=config.sidecar_port,
                sidecar_health_path=config.sidecar_health_path,
                containers=config.containers,
                health_check_containers=_env_bool(
                    "NEUTREE_PD_HEALTH_CHECK_CONTAINERS",
                    config.health_check_containers,
                ),
            )
        return config

    @classmethod
    def from_dict(cls, data: Mapping[str, Any]) -> "SidecarConfig":
        engine = str(data.get("engine") or data.get("engine_name") or "").strip().lower()
        if engine not in {"vllm", "sglang"}:
            raise ValueError(f"unsupported or missing PD sidecar engine {engine!r}")

        sidecar = data.get("sidecar") or {}
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
            raise ValueError("PD sidecar config requires at least one role container")

        by_role = {(c.role, c.rank) for c in containers}
        if not any(role == "prefill" for role, _ in by_role):
            raise ValueError("PD sidecar config requires at least one prefill container")
        if not any(role == "decode" for role, _ in by_role):
            raise ValueError("PD sidecar config requires at least one decode container")

        return cls(
            engine=engine,
            engine_version=str(data.get("engine_version") or ""),
            workspace=str(data.get("workspace") or ""),
            endpoint=str(data.get("endpoint") or ""),
            sidecar_port=int(sidecar.get("port") or 8000),
            sidecar_health_path=str(sidecar.get("health_path") or HEALTH_PATH),
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
        req = request.Request(
            url,
            data=data,
            method="POST",
            headers={"content-type": "application/json"},
        )
        try:
            resp = request.urlopen(req, timeout=self.timeout)
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
        req = request.Request(url, method="GET")
        try:
            with request.urlopen(req, timeout=timeout or self.health_timeout) as resp:
                return 200 <= getattr(resp, "status", 200) < 400
        except Exception:  # noqa: BLE001
            return False


class PDRouterSidecar:
    def __init__(self, config: SidecarConfig, client: Optional[UrllibEngineClient] = None):
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
                "containers": checks,
            },
            status_code=200 if ready else 503,
        )

    def topology(self) -> Dict[str, Any]:
        checks = self._container_health()
        units = []
        for container in self.config.containers:
            health = checks.get(container.name, {})
            units.append(
                {
                    "role": container.role,
                    "rank": container.rank,
                    "container": container.name,
                    "host": "127.0.0.1",
                    "http_port": container.http_port,
                    "side_channel_port": container.side_channel_port,
                    "ready": bool(health.get("ready")),
                }
            )
        return {
            "engine": self.config.engine,
            "engine_version": self.config.engine_version,
            "workspace": self.config.workspace,
            "endpoint": self.config.endpoint,
            "units": units,
        }

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
            prefill_future = self._executor.submit(
                self._drain_stream_request,
                prefill,
                prefill_payload,
                req_id,
            )
            decode_resp = self.client.post_json(decode, CHAT_PATH, decode_payload, stream=True)
            if decode_resp.stream is not None:
                decode_resp.stream = _join_stream_with_future(decode_resp.stream, prefill_future, req_id)
            return decode_resp

        prefill_future = self._executor.submit(
            self.client.post_json,
            prefill,
            CHAT_PATH,
            prefill_payload,
            False,
        )
        decode_resp = self.client.post_json(decode, CHAT_PATH, decode_payload, stream=False)
        prefill_resp = prefill_future.result()
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


class SidecarHTTPRequestHandler(BaseHTTPRequestHandler):
    runtime: PDRouterSidecar

    def do_GET(self) -> None:  # noqa: N802
        path = parse.urlparse(self.path).path
        if path == self.runtime.config.sidecar_health_path or path == HEALTH_PATH:
            self._send_response(self.runtime.health())
            return
        if path == "/v1/pd/topology":
            self._send_response(EngineHTTPResponse.json(self.runtime.topology()))
            return
        self._send_response(_error_response("not found", 404))

    def do_POST(self) -> None:  # noqa: N802
        parsed = parse.urlparse(self.path)
        if parsed.path != CHAT_PATH:
            self._send_response(_error_response("not found", 404))
            return
        try:
            payload = self._read_json()
            query = parse.parse_qs(parsed.query)
            prefill_index, decode_index, clean = extract_route_indices(
                payload,
                headers={k.lower(): v for k, v in self.headers.items()},
                query=query,
            )
        except ValueError as exc:
            self._send_response(_error_response(str(exc), 400))
            return
        self._send_response(self.runtime.handle_chat(clean, prefill_index, decode_index))

    def log_message(self, fmt: str, *args: Any) -> None:
        LOG.info("%s - %s", self.address_string(), fmt % args)

    def _read_json(self) -> Dict[str, Any]:
        length = int(self.headers.get("content-length") or 0)
        raw = self.rfile.read(length) if length > 0 else b"{}"
        try:
            payload = json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError as exc:
            raise ValueError(f"request body is not valid JSON: {exc}") from exc
        if not isinstance(payload, dict):
            raise ValueError("request body must be a JSON object")
        return payload

    def _send_response(self, response: EngineHTTPResponse) -> None:
        headers = {k.lower(): v for k, v in (response.headers or {}).items()}
        self.send_response(response.status_code)
        if response.stream is not None:
            self.send_header("content-type", headers.get("content-type", "text/event-stream"))
            self.end_headers()
            try:
                for chunk in response.stream:
                    if isinstance(chunk, str):
                        chunk = chunk.encode("utf-8")
                    self.wfile.write(chunk)
                    self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                LOG.info("client disconnected while streaming")
            return

        body = response.body or b""
        self.send_header("content-type", headers.get("content-type", "application/json"))
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


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


def run_server(runtime: PDRouterSidecar, host: str = "0.0.0.0", port: Optional[int] = None) -> None:
    SidecarHTTPRequestHandler.runtime = runtime
    httpd = ThreadingHTTPServer((host, int(port or runtime.config.sidecar_port)), SidecarHTTPRequestHandler)
    LOG.info(
        "pd-router-sidecar listening on %s:%s engine=%s engine_version=%s workspace=%s endpoint=%s",
        host,
        port or runtime.config.sidecar_port,
        runtime.config.engine,
        runtime.config.engine_version,
        runtime.config.workspace,
        runtime.config.endpoint,
    )
    httpd.serve_forever()


def _strip_route_fields(payload: Mapping[str, Any]) -> Dict[str, Any]:
    clean = dict(payload)
    for key in ROUTE_PAYLOAD_KEYS:
        clean.pop(key, None)
    return clean


def _error_response(message: str, status_code: int, details: Optional[Mapping[str, Any]] = None) -> EngineHTTPResponse:
    error = {"message": message, "type": "pd_router_sidecar_error", "code": status_code}
    if details:
        error["details"] = dict(details)
    return EngineHTTPResponse.json({"error": error}, status_code=status_code)


def _iter_response(resp: Any) -> Iterable[bytes]:
    try:
        while True:
            chunk = resp.readline()
            if not chunk:
                break
            yield chunk
    finally:
        resp.close()


def _join_stream_with_future(stream: Iterable[bytes], future: Any, req_id: str) -> Iterable[bytes]:
    try:
        for chunk in stream:
            yield chunk
        future.result()
    except Exception as exc:  # noqa: BLE001
        LOG.warning("SGLang prefill stream failed for req=%s: %s", req_id, exc)
        raise


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
