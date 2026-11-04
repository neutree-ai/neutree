"""Exercise shipped SGLang request boundaries without an engine or GPU."""

import ast
import asyncio
import copy
import json
import logging
import sys
from pathlib import Path
from types import ModuleType, SimpleNamespace
from typing import Any, AsyncGenerator, Dict, Optional
from unittest.mock import AsyncMock, Mock, patch

import pytest
from fastapi import FastAPI, HTTPException, Request
from fastapi.testclient import TestClient
from pydantic import BaseModel, Field
from starlette.responses import JSONResponse, StreamingResponse

from serve._utils import serve_response


class Result(BaseModel):
    value: str = "ok"


class RequestModel(BaseModel):
    stream: bool = False
    max_tokens: int = Field(default=1, gt=0)


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
    wire_path = Path(__file__).with_name("wire.py")
    wire_body = []
    for node in ast.parse(wire_path.read_text()).body:
        if isinstance(node, ast.ImportFrom) and node.module.startswith("serve._utils."):
            wire_body.append(copy.deepcopy(node))
        elif isinstance(node, (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in {
            "_FakeRawRequest", "_handle_request",
        }:
            wire_body.append(copy.deepcopy(node))
    body = []
    for node in tree.body:
        if isinstance(node, ast.ImportFrom) and node.module.startswith("serve._utils.") and node.module != "serve._utils.runtime_env":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.ClassDef) and node.name in {"Backend", "Controller"}:
            cls = copy.deepcopy(node)
            cls.decorator_list = []
            cls.body = [n for n in cls.body if not (isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and n.name in {"__init__", "__del__", "_ensure_chat", "_ensure_completion", "_ensure_embedding"})]
            for method in cls.body:
                if isinstance(method, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    method.decorator_list = []
            body.append(cls)
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in {"_iter_response_body", "_handle_request"}:
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Assign) and any(isinstance(n, ast.Name) and n.id == "app" for n in node.targets):
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_exception_handler":
            body.append(copy.deepcopy(node))
    namespace = dict(
        Any=Any, Dict=Dict, Optional=Optional, AsyncGenerator=AsyncGenerator, FastAPI=FastAPI,
        HTTPException=HTTPException, Request=Request, JSONResponse=JSONResponse,
        StreamingResponse=StreamingResponse, DeploymentResponseGenerator=object,
        logger=logging.getLogger("test.sglang"), json=json,
        backend_error=serve_response.backend_error,
        backend_response=serve_response.backend_response,
        error_stream=serve_response.error_stream,
        input_error_handler=serve_response.input_error_handler,
        is_streaming=serve_response.is_streaming,
        json_safe=serve_response.json_safe,
        open_stream=serve_response.open_stream,
        read_json_object=serve_response.read_json_object,
        to_wire_response=serve_response.to_wire_response,
        RequestInputError=serve_response.RequestInputError,
    )
    exec(compile(ast.fix_missing_locations(ast.Module(body=wire_body, type_ignores=[])), str(wire_path), "exec"), namespace)
    exec(compile(ast.fix_missing_locations(ast.Module(body=body, type_ignores=[])), str(source), "exec"), namespace)
    backend = object.__new__(namespace["Backend"])
    for name in ("chat", "completion", "embedding"):
        setattr(backend, "_ensure_" + name, Mock(return_value=SimpleNamespace(handle_request=AsyncMock(return_value=Result()))))
    backend.engine = SimpleNamespace(tokenizer_manager=SimpleNamespace(model_config=SimpleNamespace(context_len=512)))
    backend.model_path, backend.served_model_name, backend.model_task = "model-path", "model", "text-generation"
    return SimpleNamespace(namespace=namespace, backend=backend)


def call(wrapper, method, payload=None):
    fn = getattr(wrapper.backend, method)
    return fn() if method == "get_model_info" else asyncio.run(fn({} if payload is None else payload))


async def collect(chunks):
    return [c async for c in chunks]


@pytest.mark.parametrize("method", ["chat_completion", "completion", "embedding"])
def test_success_is_plain_wire(wrapper, method):
    wire = call(wrapper, method)
    assert wire["status_code"] == 200
    assert wire["content"] == {"value": "ok"}
    json.dumps(wire, allow_nan=False)


@pytest.mark.parametrize("method,handler", [("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding")])
@pytest.mark.parametrize("stage,exc,expected_type", [
    ("init", RuntimeError("init failed"), "RuntimeError"),
    ("serving", ValueError("too long"), "ValueError"),
    ("serving", HTTPException(503, "server failure"), "HTTPException"),
])
def test_backend_contains_whole_request_boundary(wrapper, method, handler, stage, exc, expected_type):
    ensure, payload = getattr(wrapper.backend, "_ensure_" + handler), {}
    if stage == "init":
        ensure.side_effect = exc
    else:
        ensure.return_value.handle_request.side_effect = exc
    wire = call(wrapper, method, payload)
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["type"] == expected_type
    assert wire["content"]["error"]["code"] == 500
    assert wire["content"]["error"]["message"] == str(exc)
    json.dumps(wire)


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
@pytest.mark.parametrize("exc", [RuntimeError("engine failure"), HTTPException(429, "HTTP failure")])
def test_escaped_stream_exception_is_neutral_error_and_done(wrapper, method, handler, when, exc):
    async def chunks():
        if when == "midstream":
            yield b'data: {"choices":[]}\n\n'
        raise exc
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
    frames = asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))
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


@pytest.mark.parametrize("body,message", [
    (b"[1, 2]", "Request body must be a JSON object"),
    (b"{not json", "Request body must be valid JSON"),
])
def test_controller_rejects_non_object_bodies_before_dispatch(wrapper, body, message):
    http, calls = client(wrapper)
    response = http.post("/chat", content=body, headers={"content-type": "application/json"})
    assert response.status_code == 400
    assert response.json() == {"error": {"message": message, "type": "BadRequestError", "code": 400, "param": None}}
    assert calls == []


@pytest.mark.parametrize("stream", ["false", "no", 0, None])
def test_controller_treats_false_like_spellings_as_non_streaming(wrapper, stream):
    http, calls = client(wrapper)
    response = http.post("/chat", json={"stream": stream})
    assert response.status_code == 200
    assert calls == [{"stream": stream}]


@pytest.mark.parametrize("stream", [2, "maybe"])
def test_controller_rejects_uninterpretable_stream_values(wrapper, stream):
    http, calls = client(wrapper)
    response = http.post("/chat", json={"stream": stream})
    assert response.status_code == 400
    assert response.json()["error"]["message"] == "stream must be a boolean"
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
    response = http.post("/chat", json={})
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
    response = http.post(route, json={"stream": True})
    assert response.status_code == 200
    assert response.text == 'data: {"choices":[]}\n\ndata: [DONE]\n\n'
    assert calls == [{"stream": True}]


@pytest.mark.parametrize("route", ["/chat", "/completion"])
def test_first_stream_error_with_nonstandard_code_stays_json(wrapper, route):
    native = {"object": "error", "code": 529, "message": "busy", "type": "InternalServerError", "param": None}
    async def chunks():
        yield f'data: {json.dumps({"error": native})}\n\n'
        yield 'data: [DONE]\n\n'
    http, _ = stream_client(wrapper, chunks())
    response = http.post(route, json={"stream": True})
    assert response.status_code == 529
    assert response.json() == {"error": native}


@pytest.mark.parametrize("route", ["/chat", "/completion"])
def test_stream_rpc_failure_is_json_500(wrapper, route):
    http, _ = stream_client(wrapper, failure=RuntimeError("failure detail"))
    response = http.post(route, json={"stream": True})
    assert response.status_code == 500
    assert response.json() == neutral_error("failure detail")


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
@pytest.mark.parametrize("stage,exc", [
    ("init", RuntimeError("init failed")),
    ("serving", ValueError("too long")),
    ("serving", HTTPException(503, "server failure")),
])
def test_stream_start_failures_are_contained_before_iterator(wrapper, method, handler, stage, exc):
    ensure = getattr(wrapper.backend, "_ensure_" + handler)
    if stage == "init":
        ensure.side_effect = exc
    else:
        ensure.return_value.handle_request.side_effect = exc
    frames = asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))
    assert json.loads(frames[0].removeprefix("data:")) == neutral_error(str(exc), type(exc).__name__)
    assert frames[-1] == "data: [DONE]\n\n"


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
@pytest.mark.parametrize("status", [400, 503])
def test_stream_native_error_response_is_preserved(wrapper, method, handler, status):
    native = {"object": "error", "code": status, "message": "native failure detail", "type": str(status), "param": None}
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = JSONResponse(native, status_code=status)
    frames = asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))
    assert json.loads(frames[0].removeprefix("data:")) == {"error": native}
    assert frames[-1] == "data: [DONE]\n\n"


def test_stream_cancellation_unwinds(wrapper):
    async def chunks():
        raise asyncio.CancelledError()
        yield "unreachable"
    wrapper.backend._ensure_chat.return_value.handle_request.return_value = StreamingResponse(chunks())
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(collect(wrapper.backend.chat_completion_stream({"stream": True})))


@pytest.mark.parametrize("method,handler", [("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding")])
def test_nonfinite_result_is_sanitized_before_ray_transfer(wrapper, method, handler):
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = {"number": float("nan")}
    wire = call(wrapper, method)
    assert wire["status_code"] == 200
    assert wire["content"] == {"number": None}
    json.dumps(wire, allow_nan=False)


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
def test_nonfinite_stream_values_are_sanitized(wrapper, method, handler):
    async def chunks():
        yield {"number": float("nan")}
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
    frames = asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))
    assert json.loads(frames[0].removeprefix("data:")) == {"number": None}


@pytest.mark.parametrize("method,handler", [("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding")])
def test_model_dump_failure_is_server_error(wrapper, method, handler):
    exc = ValueError("serializer details")
    result = SimpleNamespace(model_dump=Mock(side_effect=exc))
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = result
    wire = call(wrapper, method)
    assert wire["status_code"] == 500
    assert wire["content"] == neutral_error("serializer details", "ValueError")
    json.dumps(wire, allow_nan=False)


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
    assert asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True}))) == frames


def test_server_error_path_never_imports_engine(wrapper):
    original_import = __import__
    def no_engine_import(name, *args, **kwargs):
        assert not name.startswith(("sglang", "vllm", "llama_cpp", "torch")), name
        return original_import(name, *args, **kwargs)
    wrapper.backend.engine = SimpleNamespace()
    with patch("builtins.__import__", side_effect=no_engine_import):
        wire = call(wrapper, "get_model_info")
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["type"] == "AttributeError"


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


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
@pytest.mark.parametrize("when", ["before", "midstream"])
@pytest.mark.parametrize("chunk,expected_type", [
    pytest.param({"value": object()}, "TypeError", id="unsupported-value"),
    pytest.param(SimpleNamespace(model_dump=Mock(side_effect=ValueError("serializer details"))), "ValueError", id="model-dump-value-error"),
])
def test_stream_serialization_failure_stays_in_backend(wrapper, method, handler, when, chunk, expected_type):
    async def chunks():
        if when == "midstream":
            yield 'data: {"choices":[]}\n\n'
        yield chunk
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
    frames = asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))
    assert json.loads(frames[-2].removeprefix("data:"))["error"]["type"] == expected_type
    assert frames[-1] == "data: [DONE]\n\n"
    assert frames.count("data: [DONE]\n\n") == 1
    if when == "midstream":
        assert frames[0] == 'data: {"choices":[]}\n\n'
    else:
        getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
        http, _ = stream_client(wrapper, getattr(wrapper.backend, method)({"stream": True}))
        response = http.post("/chat", json={"stream": True})
        assert response.status_code == 500
        assert response.json()["error"]["type"] == expected_type
