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
from pydantic import BaseModel
from starlette.responses import JSONResponse, StreamingResponse


class Result(BaseModel):
    value: str = "ok"


@pytest.fixture
def wrapper(monkeypatch):
    protocol = ModuleType("sglang.srt.entrypoints.openai.protocol")
    def request_model(**payload):
        if payload.get("bad_request"):
            raise ValueError("invalid schema")
        return payload
    for name in ("ChatCompletionRequest", "CompletionRequest", "EmbeddingRequest"):
        setattr(protocol, name, request_model)
    monkeypatch.setitem(sys.modules, protocol.__name__, protocol)
    source = Path(__file__).with_name("app.py")
    tree = ast.parse(source.read_text())
    body = []
    for node in tree.body:
        if isinstance(node, ast.ImportFrom) and node.module.startswith("serve._utils.") and node.module != "serve._utils.runtime_env":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.ClassDef) and node.name in {"Backend", "Controller", "_FakeRawRequest"}:
            cls = copy.deepcopy(node)
            cls.decorator_list = []
            cls.body = [n for n in cls.body if not (isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and n.name in {"__init__", "__del__", "_ensure_chat", "_ensure_completion", "_ensure_embedding"})]
            for method in cls.body:
                if isinstance(method, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    method.decorator_list = []
            body.append(cls)
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in {"_extract_serializable", "_iter_response_body", "_to_json_response", "_backend_error", "_stream_error"}:
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Assign) and any(isinstance(n, ast.Name) and n.id in {"app", "_WIRE_RESPONSE_MARKER"} for n in node.targets):
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_exception_handler":
            body.append(copy.deepcopy(node))
    namespace = dict(Any=Any, Dict=Dict, Optional=Optional, AsyncGenerator=AsyncGenerator, FastAPI=FastAPI, HTTPException=HTTPException, Request=Request, JSONResponse=JSONResponse, StreamingResponse=StreamingResponse, logger=logging.getLogger("test.sglang"), json=json)
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
@pytest.mark.parametrize("stage", ["init", "schema", "serving", "serialization"])
def test_backend_contains_whole_request_boundary(wrapper, method, handler, stage):
    ensure, payload = getattr(wrapper.backend, "_ensure_" + handler), {}
    if stage == "init":
        ensure.side_effect = RuntimeError("init failed")
    elif stage == "schema":
        payload["bad_request"] = True
    elif stage == "serving":
        ensure.return_value.handle_request.side_effect = ValueError("too long")
    else:
        ensure.return_value.handle_request.return_value = {"value": object()}
    wire = call(wrapper, method, payload)
    assert wire["status_code"] == (400 if stage in {"schema", "serving"} else 500)
    if stage in {"schema", "serving"}:
        assert wire["content"]["object"] == "error"
    else:
        assert wire["content"]["error"] == {"message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None}
    json.dumps(wire)


@pytest.mark.parametrize("method,handler", [("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding")])
def test_backend_preserves_returned_http_status(wrapper, method, handler):
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = JSONResponse({"object": "error", "message": "not found", "type": "NotFound", "code": 404, "param": None}, status_code=404)
    wire = call(wrapper, method)
    assert wire["status_code"] == 404
    assert wrapper.namespace["_to_json_response"](wire).status_code == 404


@pytest.mark.parametrize("method,handler", [("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding")])
def test_cancellation_unwinds(wrapper, method, handler):
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.side_effect = asyncio.CancelledError()
    with pytest.raises(asyncio.CancelledError):
        call(wrapper, method)


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
@pytest.mark.parametrize("when", ["before", "midstream"])
@pytest.mark.parametrize("exc,status", [(RuntimeError("engine failed"), 500), (ValueError("bad request"), 400)])
def test_stream_exception_is_native_error_and_done(wrapper, method, handler, when, exc, status):
    async def chunks():
        if when == "midstream":
            yield b'data: {"choices":[]}\n\n'
        raise exc
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
    frames = asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))
    assert frames[-1] == "data: [DONE]\n\n"
    assert json.loads(frames[-2].removeprefix("data:"))["error"]["code"] == status
    if when == "midstream":
        assert frames[0] == 'data: {"choices":[]}\n\n'


def test_models_errors_are_wire_errors(wrapper):
    wrapper.backend.engine = SimpleNamespace()
    wire = call(wrapper, "get_model_info")
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["message"] == "Internal server error"


def client(wrapper, result=None):
    calls = []
    class Remote:
        def remote(self, payload=None):
            calls.append(payload)
            async def value():
                return result if result is not None else wrapper.namespace["_extract_serializable"]({"ok": True})
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


@pytest.mark.parametrize("route", ["/chat", "/completion", "/embedding"])
@pytest.mark.parametrize("body", [b"", b"not json", b"null", b"[]", b'"x"', b"1", b"true"])
def test_invalid_http_body_never_calls_rpc(wrapper, route, body):
    http, calls = client(wrapper)
    response = http.post(route, content=body)
    assert response.status_code == 400
    assert response.json()["error"]["code"] == 400
    assert calls == []


@pytest.mark.parametrize("route", ["/chat", "/completion"])
@pytest.mark.parametrize("stream", [None, "true", "false", 0, 1, []])
def test_invalid_stream_never_calls_rpc(wrapper, route, stream):
    http, calls = client(wrapper)
    assert http.post(route, json={"stream": stream}).status_code == 400
    assert calls == []


def test_empty_object_reaches_business(wrapper):
    http, calls = client(wrapper)
    assert http.post("/chat", json={}).status_code == 200
    assert calls == [{}]


def test_models_error_is_not_model_info(wrapper):
    wire = {"__neutree_wire_response__": True, "status_code": 503, "content": {"object": "error", "code": 503, "message": "unavailable"}}
    http, _ = client(wrapper, wire)
    response = http.get("/models")
    assert response.status_code == 503
    assert response.json() == wire["content"]


def test_fastapi_route_annotations_are_eager():
    tree = ast.parse(Path(__file__).with_name("app.py").read_text())
    assert not any(isinstance(n, ast.ImportFrom) and n.module == "__future__" and any(a.name == "annotations" for a in n.names) for n in tree.body)


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
def test_first_stream_error_has_native_http_shape_and_status(wrapper, route):
    native = {"object": "error", "code": 503, "message": "busy", "type": "InternalServerError", "param": None}
    async def chunks():
        yield f'data: {json.dumps({"error": native})}\n\n'
        yield 'data: [DONE]\n\n'
    http, _ = stream_client(wrapper, chunks())
    response = http.post(route, json={"stream": True})
    assert response.status_code == 503
    assert response.json() == native


@pytest.mark.parametrize("route", ["/chat", "/completion"])
def test_stream_rpc_failure_is_json_500(wrapper, route):
    http, _ = stream_client(wrapper, failure=RuntimeError("private failure detail"))
    response = http.post(route, json={"stream": True})
    assert response.status_code == 500
    assert response.json()["error"]["code"] == 500
    assert "private failure detail" not in response.text


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
@pytest.mark.parametrize("stage", ["init", "schema", "serving"])
def test_stream_start_is_contained_before_iterator(wrapper, method, handler, stage):
    payload = {"stream": True}
    ensure = getattr(wrapper.backend, "_ensure_" + handler)
    if stage == "init":
        ensure.side_effect = RuntimeError("init failed")
    elif stage == "schema":
        payload["bad_request"] = True
    else:
        ensure.return_value.handle_request.side_effect = ValueError("too long")
    frames = asyncio.run(collect(getattr(wrapper.backend, method)(payload)))
    assert json.loads(frames[0].removeprefix("data:"))["error"]["code"] == (500 if stage == "init" else 400)
    assert frames[-1] == "data: [DONE]\n\n"


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
def test_stream_native_error_response_is_preserved(wrapper, method, handler):
    native = {"object": "error", "code": 429, "message": "busy", "type": "429", "param": None}
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = JSONResponse(native, status_code=429)
    frames = asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))
    assert json.loads(frames[0].removeprefix("data:")) == {"error": native}
    assert frames[-1] == "data: [DONE]\n\n"


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
def test_stream_cancellation_unwinds(wrapper, method, handler):
    async def chunks():
        raise asyncio.CancelledError()
        yield "unreachable"
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))


@pytest.mark.parametrize("method,handler", [("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding")])
@pytest.mark.parametrize("number", [float("nan"), float("inf")])
def test_nonfinite_result_is_caught_before_ray_transfer(wrapper, method, handler, number):
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = {"number": number}
    wire = call(wrapper, method)
    assert wire["status_code"] == 500
    assert wire["content"] == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    json.dumps(wire, allow_nan=False)


@pytest.mark.parametrize("method,handler", [("chat_completion", "chat"), ("completion", "completion"), ("embedding", "embedding")])
@pytest.mark.parametrize("exc", [ValueError("private serializer details"), TypeError("private serializer details")])
def test_model_dump_failure_is_server_error(wrapper, method, handler, exc):
    result = SimpleNamespace(model_dump=Mock(side_effect=exc))
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = result
    wire = call(wrapper, method)
    assert wire["status_code"] == 500
    assert wire["content"] == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    json.dumps(wire, allow_nan=False)


def test_response_http_status_is_authoritative(wrapper):
    wire = wrapper.namespace["_extract_serializable"](JSONResponse({"object": "error", "code": 400}, status_code=429))
    assert wire["status_code"] == 429


@pytest.mark.parametrize("exc,status,kind,message", [
    (ValueError("bad request"), 400, "BadRequest", "bad request"),
    (HTTPException(429, "busy"), 429, "429", "busy"),
])
def test_client_business_errors_keep_native_protocol(wrapper, exc, status, kind, message):
    assert wrapper.namespace["_backend_error"](exc, "test") == {
        "object": "error", "message": message, "type": kind, "code": status, "param": None,
    }


@pytest.mark.parametrize("exc", [RuntimeError("private server details"), HTTPException(503, "private server details")])
def test_generic_server_error_uses_engine_neutral_shape(wrapper, exc):
    error = wrapper.namespace["_backend_error"](exc, "test")
    assert error == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}


def test_server_error_path_never_imports_engine(wrapper):
    original_import = __import__
    def no_engine_import(name, *args, **kwargs):
        assert not name.startswith(("sglang", "vllm", "llama_cpp", "torch")), name
        return original_import(name, *args, **kwargs)
    wrapper.backend.engine = SimpleNamespace()
    with patch("builtins.__import__", side_effect=no_engine_import):
        wire = call(wrapper, "get_model_info")
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["message"] == "Internal server error"


def test_backend_stream_validation_uses_shared_request_error(wrapper):
    wire = call(wrapper, "chat_completion", {"stream": "false"})
    assert wire["status_code"] == 400
    assert wire["content"]["error"]["code"] == 400
    wrapper.backend._ensure_chat.assert_not_called()


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
    assert wire["content"]["error"]["message"] == "Internal server error"
    assert engine_imports == ["sglang.srt.entrypoints.openai.protocol"]


@pytest.mark.parametrize("method,handler", [("chat_completion_stream", "chat"), ("completion_stream", "completion")])
@pytest.mark.parametrize("when", ["before", "midstream"])
@pytest.mark.parametrize("chunk", [
    pytest.param({"value": object()}, id="unsupported-value"),
    pytest.param({"value": float("nan")}, id="nan"),
    pytest.param({"value": float("inf")}, id="infinity"),
    pytest.param(SimpleNamespace(model_dump=Mock(side_effect=ValueError("private serializer details"))), id="model-dump-value-error"),
    pytest.param(SimpleNamespace(model_dump=Mock(side_effect=TypeError("private serializer details"))), id="model-dump-type-error"),
])
def test_stream_serialization_failure_stays_in_backend(wrapper, method, handler, when, chunk):
    async def chunks():
        if when == "midstream":
            yield 'data: {"choices":[]}\n\n'
        yield chunk
    getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
    frames = asyncio.run(collect(getattr(wrapper.backend, method)({"stream": True})))
    expected = {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    assert json.loads(frames[-2].removeprefix("data:")) == expected
    assert frames[-1] == "data: [DONE]\n\n"
    assert frames.count("data: [DONE]\n\n") == 1
    if when == "midstream":
        assert frames[0] == 'data: {"choices":[]}\n\n'
    else:
        getattr(wrapper.backend, "_ensure_" + handler).return_value.handle_request.return_value = StreamingResponse(chunks())
        http, _ = stream_client(wrapper, getattr(wrapper.backend, method)({"stream": True}))
        response = http.post("/chat", json={"stream": True})
        assert response.status_code == 500
        assert response.json() == expected
