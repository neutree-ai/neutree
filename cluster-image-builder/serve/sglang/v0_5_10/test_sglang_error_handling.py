"""Exercise shipped SGLang request boundaries without an engine or GPU."""

import ast
import asyncio
import copy
import json
import sys
from pathlib import Path
from types import ModuleType, SimpleNamespace
from typing import Any, AsyncGenerator, Dict
from unittest.mock import AsyncMock, Mock, patch

import pytest
from fastapi import FastAPI, Request
from fastapi.testclient import TestClient
from pydantic import BaseModel
from starlette.responses import JSONResponse, StreamingResponse

from serve._utils import serve_response


PAYLOADS = {
    "chat_completion": {"model": "fixture", "messages": [{"role": "user", "content": "hi"}]},
    "completion": {"model": "fixture", "prompt": "hi"},
    "embedding": {"model": "fixture", "input": "hi"},
}


class Result(BaseModel):
    value: str = "ok"


class RequestModel(BaseModel):
    stream: bool = False


def neutral_error(message, error_type="RuntimeError", code=500):
    return {"error": {"message": message, "type": error_type, "code": code, "param": None}}


@pytest.fixture
def wrapper(monkeypatch):
    protocol = ModuleType("sglang.srt.entrypoints.openai.protocol")
    for name in ("ChatCompletionRequest", "CompletionRequest", "EmbeddingRequest"):
        setattr(protocol, name, RequestModel)
    monkeypatch.setitem(sys.modules, protocol.__name__, protocol)
    source = Path(__file__).with_name("app.py")
    tree = ast.parse(source.read_text())
    body = []
    for node in tree.body:
        if isinstance(node, ast.ImportFrom) and (
            node.module.startswith("serve._utils.") and node.module != "serve._utils.runtime_env"
            or node.module == "serve.sglang.v0_5_10.wire"
        ):
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.ClassDef) and node.name in {"Backend", "Controller"}:
            cls = copy.deepcopy(node)
            cls.decorator_list = []
            cls.body = [n for n in cls.body if not (isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and n.name in {"__init__", "__del__", "_ensure_chat", "_ensure_completion", "_ensure_embedding"})]
            for method in cls.body:
                if isinstance(method, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    method.decorator_list = []
            body.append(cls)
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == "_iter_response_body":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Assign) and any(isinstance(n, ast.Name) and n.id == "app" for n in node.targets):
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_exception_handler":
            body.append(copy.deepcopy(node))
    namespace = dict(
        Any=Any, Dict=Dict, AsyncGenerator=AsyncGenerator, FastAPI=FastAPI,
        Request=Request, JSONResponse=JSONResponse,
        StreamingResponse=StreamingResponse, DeploymentResponseGenerator=object,
        json=json,
    )
    exec(compile(ast.fix_missing_locations(ast.Module(body=body, type_ignores=[])), str(source), "exec"), namespace)
    backend = object.__new__(namespace["Backend"])
    for name in ("chat", "completion", "embedding"):
        setattr(backend, "_ensure_" + name, Mock(return_value=SimpleNamespace(handle_request=AsyncMock(return_value=Result()))))
    backend.engine = SimpleNamespace(tokenizer_manager=SimpleNamespace(model_config=SimpleNamespace(context_len=512)))
    backend.model_path, backend.served_model_name, backend.model_task = "model-path", "model", "text-generation"
    return SimpleNamespace(namespace=namespace, backend=backend)


def call(wrapper, method, payload=None):
    if method == "get_model_info":
        return wrapper.backend.get_model_info()
    request = {**PAYLOADS[method.removesuffix("_stream")], **(payload or {})}
    result = getattr(wrapper.backend, method)(request)
    return result if method.endswith("_stream") else asyncio.run(result)


async def collect(chunks):
    return [c async for c in chunks]


@pytest.mark.parametrize("method", ["chat_completion", "completion", "embedding"])
def test_success_is_plain_wire(wrapper, method):
    wire = call(wrapper, method)
    assert wire["status_code"] == 200
    assert wire["content"] == {"value": "ok"}
    json.dumps(wire, allow_nan=False)


@pytest.mark.parametrize("method,handler", [
    ("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding"),
    ("chat_completion_stream", "chat"), ("completion_stream", "completion"),
])
def test_handler_initialization_failure_is_contained(wrapper, method, handler):
    getattr(wrapper.backend, "_ensure_" + handler).side_effect = RuntimeError("init failed")
    result = call(wrapper, method)
    if method.endswith("_stream"):
        frames = asyncio.run(collect(result))
        assert json.loads(frames[0].removeprefix("data:")) == neutral_error("init failed")
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == 500
        assert result["content"] == neutral_error("init failed")


@pytest.mark.parametrize("method", ["chat_completion", "chat_completion_stream"])
def test_unexpected_handler_failure_is_contained(wrapper, method):
    wrapper.backend._ensure_chat.return_value.handle_request.side_effect = RuntimeError("handler failed")
    result = call(wrapper, method)
    if method.endswith("_stream"):
        frames = asyncio.run(collect(result))
        assert json.loads(frames[0].removeprefix("data:")) == neutral_error("handler failed")
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == 500
        assert result["content"] == neutral_error("handler failed")


@pytest.mark.parametrize("method,handler", [("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding")])
@pytest.mark.parametrize("status", [400, 503])
def test_backend_preserves_returned_http_status(wrapper, method, handler, status):
    native = {"object": "error", "message": "native error detail", "type": str(status), "code": status, "param": None}
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = JSONResponse(native, status_code=status)
    wire = call(wrapper, method)
    assert wire["status_code"] == status
    assert wire["content"] == native
    response = serve_response.wire_http_response(wire)
    assert response.status_code == status
    assert json.loads(response.body) == native


def test_cancellation_unwinds(wrapper):
    wrapper.backend._ensure_chat.return_value.handle_request.side_effect = asyncio.CancelledError()
    with pytest.raises(asyncio.CancelledError):
        call(wrapper, "chat_completion")


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
@pytest.mark.parametrize("when", ["before", "midstream"])
def test_escaped_stream_exception_is_neutral_error_and_done(wrapper, method, handler, when):
    exc = RuntimeError("engine failure")
    async def chunks():
        if when == "midstream":
            yield b'data: {"choices":[]}\n\n'
        raise exc
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
    frames = asyncio.run(collect(call(wrapper, method, {"stream": True})))
    assert frames[-1] == "data: [DONE]\n\n"
    assert json.loads(frames[-2].removeprefix("data:")) == neutral_error(str(exc), type(exc).__name__)
    assert frames.count("data: [DONE]\n\n") == 1
    if when == "midstream":
        assert frames[0] == 'data: {"choices":[]}\n\n'


def client(wrapper, result=None):
    calls = []
    class Remote:
        def remote(self, payload=None):
            calls.append(payload)
            async def value():
                return result if result is not None else serve_response.wire_response({"ok": True})
            return value()
    class Handle:
        chat_completion = completion = embedding = get_model_info = Remote()
        def options(self, **options):
            return self
    controller = object.__new__(wrapper.namespace["Controller"])
    controller.backend = Handle()
    app = wrapper.namespace["app"]
    for route, name in [("/chat", "chat"), ("/completion", "completions"), ("/embedding", "embeddings")]:
        app.post(route)(getattr(controller, name))
    app.get("/models")(controller.models)
    return TestClient(app), calls


def test_controller_rejects_invalid_json_before_dispatch(wrapper):
    http, calls = client(wrapper)
    result = http.post("/chat", content=b"{not json", headers={"content-type": "application/json"})
    assert result.status_code == 400
    assert result.json() == {"error": {"message": "Request body must be valid JSON", "type": "BadRequestError", "code": 400, "param": None}}
    assert calls == []


def test_controller_treats_false_like_spellings_as_non_streaming(wrapper):
    http, calls = client(wrapper)
    payload = {**PAYLOADS["chat_completion"], "stream": "false"}
    result = http.post("/chat", json=payload)
    assert result.status_code == 200
    assert calls == [payload]


def test_controller_rejects_uninterpretable_stream_values(wrapper):
    http, calls = client(wrapper)
    result = http.post("/chat", json={**PAYLOADS["chat_completion"], "stream": "maybe"})
    assert result.status_code == 400
    assert result.json()["error"]["message"] == "stream must be a boolean"
    assert calls == []


def test_models_error_is_not_model_info(wrapper):
    wire = {"__neutree_wire_response__": True, "status_code": 503, "content": {"object": "error", "code": 503, "message": "unavailable"}}
    http, _ = client(wrapper, wire)
    response = http.get("/models")
    assert response.status_code == 503
    assert response.json() == wire["content"]


def test_models_malformed_wire_fails_closed(wrapper):
    http, _ = client(wrapper, {"status_code": 200})
    response = http.get("/models")
    assert response.status_code == 500
    assert response.json()["error"]["code"] == 500


def test_controller_passes_native_flat_errors_through(wrapper):
    # SGLang returns errors flat over HTTP; the Controller must not reshape a
    # body the engine returned.
    native = {"object": "error", "message": "native detail", "type": "503", "param": None, "code": 503}
    http, _ = client(wrapper, serve_response.wire_response(native, 503))
    response = http.post("/chat", json=PAYLOADS["chat_completion"])
    assert response.status_code == 503
    assert response.json() == native


def stream_client(wrapper, chunks=None, failure=None):
    calls = []
    class Remote:
        def remote(self, payload):
            calls.append(payload)
            if failure is not None:
                raise failure
            return chunks
    class Handle:
        chat_completion_stream = completion_stream = Remote()
        def options(self, **options):
            assert options == {"stream": True}
            return self
    controller = object.__new__(wrapper.namespace["Controller"])
    controller.backend = Handle()
    app = wrapper.namespace["app"]
    app.post("/chat")(controller.chat)
    app.post("/completion")(controller.completions)
    return TestClient(app), calls


@pytest.mark.parametrize("route", ["/chat", "/completion"])
def test_stream_first_frame_is_replayed(wrapper, route):
    async def chunks():
        yield 'data: {"choices":[]}\n\n'
        yield 'data: [DONE]\n\n'
    http, calls = stream_client(wrapper, chunks())
    payload = {**PAYLOADS["chat_completion" if route == "/chat" else "completion"], "stream": True}
    response = http.post(route, json=payload)
    assert response.status_code == 200
    assert response.text == 'data: {"choices":[]}\n\ndata: [DONE]\n\n'
    assert calls == [payload]


@pytest.mark.parametrize("route", ["/chat", "/completion"])
def test_first_stream_error_with_nonstandard_code_stays_json(wrapper, route):
    native = {"object": "error", "code": 529, "message": "busy", "type": "InternalServerError", "param": None}
    async def chunks():
        yield f'data: {json.dumps({"error": native})}\n\n'
        yield 'data: [DONE]\n\n'
    http, _ = stream_client(wrapper, chunks())
    payload = {**PAYLOADS["chat_completion" if route == "/chat" else "completion"], "stream": True}
    response = http.post(route, json=payload)
    assert response.status_code == 529
    assert response.json() == {"error": native}


@pytest.mark.parametrize("route", ["/chat", "/completion"])
def test_stream_rpc_failure_is_json_500(wrapper, route):
    http, _ = stream_client(wrapper, failure=RuntimeError("failure detail"))
    payload = {**PAYLOADS["chat_completion" if route == "/chat" else "completion"], "stream": True}
    response = http.post(route, json=payload)
    assert response.status_code == 500
    assert response.json() == neutral_error("failure detail")


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
@pytest.mark.parametrize("status", [400, 503])
def test_stream_native_error_response_is_preserved(wrapper, method, handler, status):
    native = {"object": "error", "code": status, "message": "native failure detail", "type": str(status), "param": None}
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = JSONResponse(native, status_code=status)
    frames = asyncio.run(collect(call(wrapper, method, {"stream": True})))
    assert json.loads(frames[0].removeprefix("data:")) == {"error": native}
    assert frames[-1] == "data: [DONE]\n\n"


def test_stream_cancellation_unwinds(wrapper):
    async def chunks():
        raise asyncio.CancelledError()
        yield "unreachable"
    wrapper.backend._ensure_chat.return_value.handle_request.return_value = StreamingResponse(chunks())
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(collect(call(wrapper, "chat_completion_stream", {"stream": True})))


def test_response_http_status_is_authoritative():
    wire = serve_response.to_wire_response(JSONResponse({"object": "error", "code": 400}, status_code=429))
    assert wire["status_code"] == 429


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
def test_native_stream_error_frames_are_preserved(wrapper, method, handler):
    frames = ['data: {"choices":[]}\n\n', 'data: {"error":{"object":"error","message":"native failure detail","type":"BadRequestError","code":400,"param":null}}\n\n', 'data: [DONE]\n\n']
    async def chunks():
        for frame in frames:
            yield frame
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
    assert asyncio.run(collect(call(wrapper, method, {"stream": True}))) == frames


def test_engine_import_failure_is_contained_without_more_engine_imports(wrapper):
    original_import, engine_imports = __import__, []
    def missing_engine(name, *args, **kwargs):
        if name.startswith(("sglang", "vllm", "llama_cpp", "torch")):
            engine_imports.append(name)
            raise ModuleNotFoundError(name=name)
        return original_import(name, *args, **kwargs)
    with patch("builtins.__import__", side_effect=missing_engine):
        wire = call(wrapper, "chat_completion")
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["type"] == "ModuleNotFoundError"
    assert engine_imports == ["sglang.srt.entrypoints.openai.protocol"]
