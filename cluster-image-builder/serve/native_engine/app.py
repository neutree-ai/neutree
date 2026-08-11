"""Ray Serve passthrough proxy for engines launched inside their Actor."""

from __future__ import annotations

import asyncio
import os
from collections.abc import AsyncIterator, Mapping
from typing import Any

import httpx
from fastapi import FastAPI, Request
from ray import serve
from ray.serve import Application
from starlette.responses import Response, StreamingResponse

from downloader import build_request_from_model_args, download_with_markers, get_downloader
from serve._utils.runtime_env import build_backend_runtime_env
from serve._utils.vllm_task_translate import task_kwargs
from serve.native_engine.config import build_engine_args, startup_timeout_s
from serve.native_engine.port_allocator import LocalPortAllocator, is_port_available_on_loopback
from serve.native_engine.process_runner import DirectEngineRunner, EngineExitedBeforeReady
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


def _response_headers(headers: Mapping[str, str]) -> dict[str, str]:
    return {key: value for key, value in headers.items() if key.lower() not in _HOP_BY_HOP_HEADERS}


def _request_headers(headers: Mapping[str, str]) -> dict[str, str]:
    return {
        key: value
        for key, value in headers.items()
        if key.lower() not in _HOP_BY_HOP_HEADERS | {"host", "content-length"}
    }


def _actor_tags(engine_identity: Mapping[str, str]) -> dict[str, str]:
    context = serve.get_replica_context()
    return {
        "application": context.app_name,
        "deployment": context.deployment,
        "replica": context.replica_tag,
        "engine": engine_identity.get("name", ""),
        "engine_version": engine_identity.get("version", ""),
    }


def _actor_owner_id() -> str:
    try:
        context = serve.get_replica_context()
        return f"{context.app_name}:{context.deployment}:{context.replica_tag}"
    except RuntimeError:
        return f"pid:{os.getpid()}"


def _with_task_defaults(model_task: str, engine_args: Mapping[str, Any]) -> dict[str, Any]:
    result = dict(engine_args)
    for key, value in task_kwargs(model_task).items():
        result.setdefault(key, value)
    return result


@serve.deployment
@serve.ingress(app)
class NativeEngineRunner:
    """Run an engine as a child process of the GPU-owning Ray Actor."""

    def __init__(
        self,
        model: Mapping[str, Any],
        native_runtime: Mapping[str, Any],
        engine_identity: Mapping[str, str],
        engine_args: Mapping[str, Any],
    ) -> None:
        self._model = dict(model)
        self._runtime = dict(native_runtime)
        self._engine_identity = dict(engine_identity)
        self._engine_args = _with_task_defaults(str(self._model.get("task", "")), engine_args)
        self._allocator = LocalPortAllocator(owner_id=_actor_owner_id())
        self._runner: DirectEngineRunner | None = None
        self._bridge_task: asyncio.Task[None] | None = None
        self._start_engine()

    def _start_engine(self) -> None:
        self._download_model()
        for attempt in range(2):
            lease = self._allocator.acquire()
            base_url = f"http://127.0.0.1:{lease.port}"
            runner = DirectEngineRunner(
                command=build_engine_args(
                    runtime=self._runtime,
                    model=self._model,
                    engine_args=self._engine_args,
                    port=lease.port,
                ),
                health_url=base_url + str(self._runtime.get("health_path", "/health")),
                startup_timeout_s=startup_timeout_s(self._runtime),
            )
            try:
                runner.start()
            except EngineExitedBeforeReady:
                # vLLM cannot receive a socket that was pre-bound by the allocator.
                # A non-participating process can therefore win the narrow race before
                # vLLM binds. Retry once only when the failed port is now occupied.
                # Other failures must fail the Actor so Ray cleans its child process
                # group and closes the allocator's lock descriptors.
                if attempt == 0 and not is_port_available_on_loopback(lease.port):
                    self._allocator.release(lease)
                    continue
                raise
            self._runner = runner
            self._base_url = base_url
            return
        raise RuntimeError("native engine could not reserve a port")

    def _download_model(self) -> None:
        backend, request = build_request_from_model_args(
            {
                "registry_type": self._model.get("registry_type", ""),
                "name": self._model.get("name", ""),
                "version": self._model.get("version", ""),
                "file": self._model.get("file", ""),
                "task": self._model.get("task", ""),
                "registry_path": self._model.get("registry_path", ""),
                "path": self._model.get("path", ""),
            }
        )
        print(f"[native-engine] downloading model with backend={backend}", flush=True)
        downloader = get_downloader(backend)
        download_with_markers(
            downloader,
            request.source,
            request.dest,
            credentials=request.credentials,
            recursive=request.recursive,
            overwrite=request.overwrite,
            retries=request.retries,
            timeout=request.timeout,
            metadata=request.metadata,
        )
        print("[native-engine] model download completed", flush=True)

    async def proxy_stream(self, request: Mapping[str, Any]) -> AsyncIterator[dict[str, Any] | bytes]:
        self._ensure_metrics_bridge()
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

    @app.api_route("/{path:path}", methods=["DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"])
    async def proxy(self, path: str, request: Request) -> Response:
        request_data = {
            "body": await request.body(),
            "headers": _request_headers(request.headers),
            "method": request.method,
            "path": "/" + path + (f"?{request.url.query}" if request.url.query else ""),
        }
        stream = self.proxy_stream(request_data)
        try:
            first = await stream.__anext__()
        except StopAsyncIteration:
            return StreamingResponse(iter(()), status_code=502)
        if not isinstance(first, dict):
            raise RuntimeError("native engine stream did not return response metadata")
        return StreamingResponse(
            _stream_chunks(stream),
            headers=first["headers"],
            media_type=first["headers"].get("content-type"),
            status_code=first["status_code"],
        )

    def _ensure_metrics_bridge(self) -> None:
        if self._bridge_task is not None:
            return
        metrics_path = str(self._runtime.get("metrics_path", ""))
        if metrics_path:
            bridge = HttpPrometheusToRayBridge(
                actor_tags=_actor_tags(self._engine_identity),
                metrics_url=self._base_url + metrics_path,
            )
            self._bridge_task = asyncio.get_running_loop().create_task(bridge.run())

async def _stream_chunks(stream: AsyncIterator[dict[str, Any] | bytes]) -> AsyncIterator[bytes]:
    async for chunk in stream:
        if isinstance(chunk, bytes):
            yield chunk


def app_builder(args: Mapping[str, Any]) -> Application:
    """Build the single-Actor passthrough engine application."""
    deployment_options = args.get("deployment_options", {})
    backend_options = deployment_options.get("backend", {})
    return NativeEngineRunner.options(
        max_ongoing_requests=backend_options.get("max_ongoing_requests", 100),
        num_replicas=backend_options.get("num_replicas", 1),
        ray_actor_options={
            "num_cpus": backend_options.get("num_cpus", 1),
            "num_gpus": backend_options.get("num_gpus", 0),
            "memory": backend_options.get("memory"),
            "resources": backend_options.get("resources", {}),
            "runtime_env": build_backend_runtime_env(args["backend_container"]),
        },
    ).bind(
        model=args.get("model", {}),
        native_runtime=args["native_runtime"],
        engine_identity=args.get("engine_identity", {}),
        engine_args=args.get("engine_args", {}),
    )
