from __future__ import annotations

import argparse
import json
import logging
import uuid
from typing import Any, Dict, Iterable, Mapping, Optional

import aiohttp
import uvicorn
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse

from router.metrics import render_prometheus
from router.scheduling import EndpointInfo
from router.runtime import BackendSelection, RouterRuntime
from router.service_discovery import K8sPodServiceDiscovery

HOP_BY_HOP_HEADERS = {
    "connection",
    "content-length",
    "host",
    "keep-alive",
    "proxy-connection",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}


app = FastAPI()


@app.on_event("startup")
async def _startup() -> None:
    app.state.http_client = aiohttp.ClientSession()


@app.on_event("shutdown")
async def _shutdown() -> None:
    client = getattr(app.state, "http_client", None)
    if client is not None:
        await client.close()
    runtime = getattr(app.state, "runtime", None)
    if runtime is not None:
        runtime.service_discovery.close()


@app.post("/{workspace}/{endpoint_name}/v1/chat/completions")
async def route_chat_completions(workspace: str, endpoint_name: str, request: Request):
    return await _route_backend_request(request, workspace, endpoint_name, "/v1/chat/completions")


@app.post("/{workspace}/{endpoint_name}/v1/embeddings")
async def route_embeddings(workspace: str, endpoint_name: str, request: Request):
    return await _route_backend_request(request, workspace, endpoint_name, "/v1/embeddings")


@app.post("/{workspace}/{endpoint_name}/v1/rerank")
async def route_rerank(workspace: str, endpoint_name: str, request: Request):
    return await _route_backend_request(request, workspace, endpoint_name, "/v1/rerank")


@app.get("/{workspace}/{endpoint_name}/v1/models")
async def route_models(workspace: str, endpoint_name: str):
    models: Dict[str, Dict[str, Any]] = {}
    for endpoint in _runtime().service_discovery.get_endpoint_info():
        if endpoint.workspace != workspace or endpoint.endpoint != endpoint_name:
            continue
        for model_name in endpoint.model_names:
            models.setdefault(
                model_name,
                {
                    "id": model_name,
                    "object": "model",
                    "created": 0,
                    "owned_by": "neutree",
                },
            )
    return JSONResponse({"object": "list", "data": list(models.values())})


@app.get("/{workspace}/{endpoint_name}/health")
async def route_endpoint_health(workspace: str, endpoint_name: str):
    for endpoint in _runtime().service_discovery.get_endpoint_info():
        if endpoint.workspace == workspace and endpoint.endpoint == endpoint_name:
            return JSONResponse({"status": "healthy"})
    return JSONResponse({"status": f"Endpoint {workspace}/{endpoint_name} not found."}, status_code=404)


@app.get("/health")
async def route_health():
    if not _runtime().service_discovery.get_health():
        return JSONResponse({"status": "service discovery module is down"}, status_code=503)
    return JSONResponse({"status": "healthy"})


@app.get("/metrics")
async def route_metrics():
    cpu_percent = _cpu_percent()
    runtime = _runtime()
    text = render_prometheus(
        runtime.metrics,
        runtime.service_discovery.get_endpoint_info(),
        runtime.request_stats.snapshot(),
        cpu_percent=cpu_percent,
    )
    return Response(text, media_type="text/plain; version=0.0.4")


async def _route_backend_request(
    request: Request,
    workspace: str,
    endpoint_name: str,
    backend_path: str,
):
    runtime = _runtime()
    request_id = request.headers.get("X-Request-Id") or str(uuid.uuid4())
    body = await request.body()
    try:
        request_json = json.loads(body.decode("utf-8")) if body else {}
    except json.JSONDecodeError:
        return JSONResponse(
            {"error": "Invalid request: body must be JSON."},
            status_code=400,
            headers={"X-Request-Id": request_id},
        )

    requested_model = request_json.get("model")
    if not requested_model:
        return JSONResponse(
            {"error": "Invalid request: missing 'model' in request body."},
            status_code=400,
            headers={"X-Request-Id": request_id},
        )

    endpoints = _filter_endpoints(
        runtime.service_discovery.get_endpoint_info(),
        workspace,
        endpoint_name,
        requested_model,
        request.query_params.get("id"),
    )
    runtime.metrics.record_incoming(workspace, endpoint_name)
    if not endpoints:
        if runtime.service_discovery.has_ever_seen_model(str(requested_model)):
            status = 503
            error = f"Model '{requested_model}' is temporarily unavailable. Please try again later."
        else:
            status = 404
            error = f"Model '{requested_model}' not found. Available models can be listed at /v1/models."
        return JSONResponse({"error": error}, status_code=status, headers={"X-Request-Id": request_id})

    try:
        selection = runtime.select_backend(endpoints, request_json)
    except ValueError as exc:
        return JSONResponse({"error": str(exc)}, status_code=503, headers={"X-Request-Id": request_id})
    return await _proxy_request(request, selection, backend_path, body, request_id)


async def _proxy_request(
    request: Request,
    selection: BackendSelection,
    backend_path: str,
    body: bytes,
    request_id: str,
):
    runtime = _runtime()
    for stats_key in selection.stats_keys:
        runtime.request_stats.on_request_start(stats_key, request_id)
    headers = _forward_headers(request.headers, request_id)
    headers.update(selection.extra_headers)
    try:
        response = await request.app.state.http_client.request(
            method=request.method,
            url=selection.url + backend_path,
            headers=headers,
            data=body,
            timeout=aiohttp.ClientTimeout(total=None),
        )
    except aiohttp.ClientError as exc:
        for stats_key in selection.stats_keys:
            runtime.request_stats.on_request_complete(stats_key, request_id)
        return JSONResponse(
            {"error": f"Failed to connect to backend: {exc}"},
            status_code=503,
            headers={"X-Request-Id": request_id},
        )

    async def stream_response():
        try:
            async for chunk in response.content.iter_any():
                yield chunk
        finally:
            response.release()
            for stats_key in selection.stats_keys:
                runtime.request_stats.on_request_complete(stats_key, request_id)

    response_headers = {
        key: value
        for key, value in response.headers.items()
        if key.lower() not in HOP_BY_HOP_HEADERS
    }
    response_headers["X-Request-Id"] = request_id
    return StreamingResponse(
        stream_response(),
        status_code=response.status,
        headers=response_headers,
        media_type=response.headers.get("content-type", "application/json"),
    )


def _filter_endpoints(
    endpoints: Iterable[EndpointInfo],
    workspace: str,
    endpoint_name: str,
    model_name: str,
    endpoint_id: Optional[str],
) -> list[EndpointInfo]:
    result = [
        endpoint
        for endpoint in endpoints
        if not endpoint.sleep
        and endpoint.workspace == workspace
        and endpoint.endpoint == endpoint_name
        and model_name in endpoint.model_names
    ]
    if endpoint_id:
        result = [endpoint for endpoint in result if endpoint.id == endpoint_id]
    return result


def _forward_headers(headers: Mapping[str, str], request_id: str) -> Dict[str, str]:
    forwarded = {
        key: value
        for key, value in headers.items()
        if key.lower() not in HOP_BY_HOP_HEADERS
    }
    forwarded["X-Request-Id"] = request_id
    return forwarded


def _runtime() -> RouterRuntime:
    runtime = getattr(app.state, "runtime", None)
    if runtime is None:
        raise RuntimeError("router runtime is not initialized")
    return runtime


def _cpu_percent() -> float:
    try:
        import psutil

        return float(psutil.cpu_percent(interval=0.0))
    except Exception:
        return 0.0


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run Neutree router.")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8000)
    parser.add_argument("--service-discovery", choices=["k8s"], default="k8s")
    parser.add_argument("--k8s-namespace", default="default")
    parser.add_argument("--k8s-label-selector", default="")
    parser.add_argument("--k8s-port", type=int, default=8000)
    parser.add_argument("--k8s-watcher-timeout-seconds", type=int, default=0)
    parser.add_argument("--backend-health-check-timeout-seconds", type=int, default=10)
    parser.add_argument("--session-key", default=None)
    parser.add_argument("--routing-logic", default="default", choices=["default"])
    parser.add_argument("--request-stats-window", type=int, default=60)
    parser.add_argument("--log-level", default="info")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    logging.basicConfig(level=getattr(logging, args.log_level.upper(), logging.INFO))
    service_discovery = K8sPodServiceDiscovery(
        namespace=args.k8s_namespace,
        port=args.k8s_port,
        label_selector=args.k8s_label_selector,
        watcher_timeout_seconds=args.k8s_watcher_timeout_seconds,
        health_check_timeout_seconds=args.backend_health_check_timeout_seconds,
    )
    app.state.runtime = RouterRuntime(service_discovery, args.request_stats_window)
    uvicorn.run(app, host=args.host, port=args.port, log_level=args.log_level)


if __name__ == "__main__":
    main()
