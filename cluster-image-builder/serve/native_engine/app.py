"""Ray Serve proxy for raw engine images launched as Docker siblings."""

from __future__ import annotations

import asyncio
import hashlib
import re
import socket
from collections.abc import AsyncIterator, Mapping
from typing import Any

import httpx
import ray
from fastapi import FastAPI, Request
from ray import serve
from ray.serve import Application
from ray.serve.handle import DeploymentHandle, DeploymentResponseGenerator
from starlette.responses import Response, StreamingResponse

from serve.native_engine.config import build_engine_args
from serve.native_engine.docker_runner import DockerEngineRunner
from serve.native_engine.prometheus_ray_bridge import HttpPrometheusToRayBridge

app = FastAPI()

_HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailers",
    "transfer-encoding",
    "upgrade",
}


def _available_loopback_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _response_headers(headers: Mapping[str, str]) -> dict[str, str]:
    return {
        key: value
        for key, value in headers.items()
        if key.lower() not in _HOP_BY_HOP_HEADERS
    }


def _request_headers(headers: Mapping[str, str]) -> dict[str, str]:
    return {
        key: value
        for key, value in headers.items()
        if key.lower() not in _HOP_BY_HOP_HEADERS | {"host", "content-length"}
    }


def _container_name() -> str:
    try:
        context = serve.get_replica_context()
        raw_name = f"neutree-native-{context.app_name}-{context.replica_tag}"
    except RuntimeError:
        raw_name = "neutree-native-local"
    normalized = re.sub(r"[^a-zA-Z0-9_.-]+", "-", raw_name).strip("-.")
    suffix = hashlib.sha256(raw_name.encode()).hexdigest()[:12]
    return f"{normalized[:90]}-{suffix}"


def _actor_tags(engine_identity: Mapping[str, str]) -> dict[str, str]:
    context = serve.get_replica_context()
    return {
        "application": context.app_name,
        "deployment": context.deployment,
        "replica": context.replica_tag,
        "engine": engine_identity.get("name", ""),
        "engine_version": engine_identity.get("version", ""),
    }


def _accelerator_ids() -> list[str]:
    allocations = ray.get_runtime_context().get_accelerator_ids()
    return [str(identifier) for identifier in allocations.get("GPU", [])]


def _with_task_defaults(
    engine_name: str, model_task: str, engine_args: Mapping[str, Any]
) -> dict[str, Any]:
    result = dict(engine_args)
    if engine_name == "vllm":
        if model_task == "text-embedding":
            result.setdefault("runner", "pooling")
            result.setdefault("convert", "embed")
        elif model_task == "text-rerank":
            result.setdefault("runner", "pooling")
            result.setdefault("convert", "classify")
    return result


@serve.deployment
class NativeEngineRunner:
    """Assign a Ray replica's accelerator allocation to one raw engine image."""

    def __init__(
        self,
        model: Mapping[str, Any],
        native_runtime: Mapping[str, Any],
        native_container: Mapping[str, Any],
        native_env: Mapping[str, str],
        engine_identity: Mapping[str, str],
        engine_args: Mapping[str, Any],
    ) -> None:
        self._model = dict(model)
        self._runtime = dict(native_runtime)
        self._container = dict(native_container)
        self._native_env = dict(native_env)
        self._engine_identity = dict(engine_identity)
        self._engine_args = _with_task_defaults(
            self._engine_identity.get("name", ""),
            str(self._model.get("task", "")),
            engine_args,
        )
        self._port = _available_loopback_port()
        self._base_url = f"http://127.0.0.1:{self._port}"
        self._runner: DockerEngineRunner | None = None
        self._bridge_task: asyncio.Task[None] | None = None
        self._startup_lock = asyncio.Lock()

    async def _ensure_started(self) -> None:
        if self._runner is not None:
            return
        async with self._startup_lock:
            if self._runner is not None:
                return
            engine_args = build_engine_args(
                runtime=self._runtime,
                model=self._model,
                engine_args=self._engine_args,
                port=self._port,
            )
            runner = DockerEngineRunner(
                container_name=_container_name(),
                image=str(self._container["image"]),
                run_options=[
                    *self._container.get("run_options", []),
                    *(self._runtime.get("run_options") or []),
                ],
                accelerator_ids=_accelerator_ids(),
                environment=self._native_env,
                engine_args=engine_args,
                health_url=self._base_url + str(self._runtime.get("health_path", "/health")),
            )
            await asyncio.to_thread(runner.start)
            self._runner = runner
            metrics_path = str(self._runtime.get("metrics_path", ""))
            if metrics_path:
                bridge = HttpPrometheusToRayBridge(
                    actor_tags=_actor_tags(self._engine_identity),
                    metrics_url=self._base_url + metrics_path,
                )
                self._bridge_task = asyncio.create_task(bridge.run())

    async def proxy(self, request: Mapping[str, Any]) -> dict[str, Any]:
        await self._ensure_started()
        async with httpx.AsyncClient(timeout=None) as client:
            response = await client.request(
                method=str(request["method"]),
                url=self._base_url + str(request["path"]),
                headers=request["headers"],
                content=request["body"],
            )
        return {
            "body": response.content,
            "headers": _response_headers(response.headers),
            "status_code": response.status_code,
        }

    async def proxy_stream(self, request: Mapping[str, Any]) -> AsyncIterator[dict[str, Any] | bytes]:
        await self._ensure_started()
        async with httpx.AsyncClient(timeout=None) as client:
            async with client.stream(
                method=str(request["method"]),
                url=self._base_url + str(request["path"]),
                headers=request["headers"],
                content=request["body"],
            ) as response:
                yield {
                    "headers": _response_headers(response.headers),
                    "status_code": response.status_code,
                }
                async for chunk in response.aiter_raw():
                    yield chunk

    def __del__(self) -> None:
        if self._bridge_task is not None:
            self._bridge_task.cancel()
        if self._runner is not None:
            self._runner.stop()


@serve.deployment(ray_actor_options={"num_cpus": 0.1})
@serve.ingress(app)
class Controller:
    def __init__(self, backend: DeploymentHandle) -> None:
        self._backend = backend

    @app.api_route("/{path:path}", methods=["DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"])
    async def proxy(self, path: str, request: Request) -> Response:
        request_data = {
            "body": await request.body(),
            "headers": _request_headers(request.headers),
            "method": request.method,
            "path": "/" + path + (f"?{request.url.query}" if request.url.query else ""),
        }
        if _is_streaming_request(request_data):
            stream: DeploymentResponseGenerator = self._backend.options(stream=True).proxy_stream.remote(request_data)
            try:
                first = await stream.__anext__()
            except StopAsyncIteration:
                return StreamingResponse(iter(()), media_type="text/event-stream")
            if not isinstance(first, dict):
                raise RuntimeError("native engine stream did not return response metadata")
            return StreamingResponse(
                _stream_chunks(stream),
                headers=first["headers"],
                media_type=first["headers"].get("content-type", "text/event-stream"),
                status_code=first["status_code"],
            )

        result = await self._backend.proxy.remote(request_data)
        return Response(
            content=result["body"],
            headers=result["headers"],
            status_code=result["status_code"],
        )


async def _stream_chunks(stream: DeploymentResponseGenerator) -> AsyncIterator[bytes]:
    async for chunk in stream:
        if isinstance(chunk, bytes):
            yield chunk


def _is_streaming_request(request: Mapping[str, Any]) -> bool:
    try:
        import json

        return bool(json.loads(request["body"]).get("stream"))
    except (TypeError, ValueError):
        return False


def app_builder(args: Mapping[str, Any]) -> Application:
    """Build the generic controller/runner pair from control-plane metadata."""
    deployment_options = args.get("deployment_options", {})
    backend_options = deployment_options.get("backend", {})
    controller_options = deployment_options.get("controller", {})
    backend = NativeEngineRunner.options(
        max_ongoing_requests=backend_options.get("max_ongoing_requests", 100),
        num_replicas=backend_options.get("num_replicas", 1),
        ray_actor_options={
            "num_cpus": backend_options.get("num_cpus", 1),
            "num_gpus": backend_options.get("num_gpus", 0),
            "memory": backend_options.get("memory"),
            "resources": backend_options.get("resources", {}),
        },
    ).bind(
        model=args.get("model", {}),
        native_runtime=args["native_runtime"],
        native_container=args["native_container"],
        native_env=args.get("native_env", {}),
        engine_identity=args.get("engine_identity", {}),
        engine_args=args.get("engine_args", {}),
    )
    return Controller.options(
        max_ongoing_requests=backend_options.get("max_ongoing_requests", 100)
        * backend_options.get("num_replicas", 1),
        num_replicas=controller_options.get("num_replicas", 1),
        ray_actor_options={
            "num_cpus": controller_options.get("num_cpus", 0.1),
            "num_gpus": 0,
        },
    ).bind(backend)
