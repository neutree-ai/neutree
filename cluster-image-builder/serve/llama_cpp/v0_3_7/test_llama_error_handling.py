"""Test shipped llama.cpp methods without native inference dependencies."""
import ast
import asyncio
import builtins
import copy
import json
import logging
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Dict, Optional, AsyncGenerator
from unittest.mock import AsyncMock, Mock, patch

import pytest
from fastapi import FastAPI, Request
from fastapi.testclient import TestClient
from starlette.concurrency import iterate_in_threadpool
from starlette.responses import JSONResponse, StreamingResponse


APP_PATH = Path(__file__).with_name("app.py")


@pytest.fixture
def wrapper():
    tree, body = ast.parse(APP_PATH.read_text()), []
    for node in tree.body:
        if isinstance(node, ast.ImportFrom) and node.module and node.module.startswith("serve._utils.") and node.module != "serve._utils.runtime_env":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id in {"app", "_WIRE_RESPONSE_MARKER"} for t in node.targets):
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_exception_handler":
            body.append(copy.deepcopy(node))
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in {"_filter_supported", "_result_to_response", "_stream_chunks"}:
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.ClassDef) and node.name in {"Backend", "Controller"}:
            cls = copy.deepcopy(node)
            cls.decorator_list = []
            cls.body = [n for n in cls.body if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and (cls.name == "Controller" or n.name in {"generate", "generate_embeddings", "show_available_models", "_to_wire_response"})]
            for method in cls.body:
                method.decorator_list = []
            body.append(cls)
    llama = SimpleNamespace(create_chat_completion=Mock(return_value={"choices": []}), create_completion=Mock(return_value={"choices": []}), create_embedding=Mock(return_value={"data": []}))
    load = Mock(return_value=llama)
    ns = dict(Any=Any, Dict=Dict, Optional=Optional, AsyncGenerator=AsyncGenerator, Request=Request, JSONResponse=JSONResponse, StreamingResponse=StreamingResponse,
              FastAPI=FastAPI, DeploymentHandle=object, json=json, logging=logging, iterate_in_threadpool=iterate_in_threadpool,
              LlamaProxy=SimpleNamespace(load_llama_from_model_settings=load), _CHAT_PARAMS={"messages", "model", "stream"}, _COMPLETION_PARAMS={"prompt", "model", "stream"}, _EMBEDDING_PARAMS={"input", "model"})
    exec(compile(ast.fix_missing_locations(ast.Module(body=body, type_ignores=[])), str(APP_PATH), "exec"), ns)
    backend = object.__new__(ns["Backend"])
    backend.model_settings, backend.model_id = object(), "fixture"
    return SimpleNamespace(namespace=ns, backend=backend, llama=llama, load=load)


def call(wrapper, method="generate", payload=None):
    return asyncio.run(getattr(wrapper.backend, method)(*(() if method == "show_available_models" else (payload if payload is not None else {},))))


async def collect(stream):
    return [chunk async for chunk in stream]


@pytest.mark.parametrize("method,payload", [("generate", {"messages": []}), ("generate", {"prompt": "hi"}), ("generate_embeddings", {"input": "hi"}), ("show_available_models", None)])
def test_success_is_plain_wire(wrapper, method, payload):
    result = call(wrapper, method, payload)
    assert result["status_code"] == 200
    json.dumps(result)


@pytest.mark.parametrize("method,payload", [("generate", {"messages": []}), ("generate_embeddings", {"input": "hi"})])
def test_model_load_error_is_contained(wrapper, method, payload):
    wrapper.load.side_effect = RuntimeError("load failed")
    result = call(wrapper, method, payload)
    assert result["status_code"] == 500 and result["content"]["error"]["message"] == "Internal server error"


@pytest.mark.parametrize("method,attribute,payload", [("generate", "create_chat_completion", {"messages": []}), ("generate", "create_completion", {"prompt": "hi"}), ("generate_embeddings", "create_embedding", {"input": "hi"})])
def test_serving_error_is_contained(wrapper, method, attribute, payload):
    getattr(wrapper.llama, attribute).side_effect = RuntimeError("native failed")
    assert call(wrapper, method, payload)["status_code"] == 500


@pytest.mark.parametrize("bad", [object(), float("nan"), float("inf")])
def test_unsafe_serialization_is_contained(wrapper, bad):
    wrapper.llama.create_chat_completion.return_value = {"value": bad}
    assert call(wrapper, payload={"messages": []})["status_code"] == 500


def test_model_list_error_is_contained(wrapper):
    del wrapper.backend.model_id
    assert call(wrapper, "show_available_models")["status_code"] == 500


def test_sync_iterator_stays_in_backend(wrapper):
    wrapper.llama.create_chat_completion.return_value = iter([{"choices": []}])
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[0][6:]) == {"choices": []}
    assert frames.count("data: [DONE]\n\n") == 1


@pytest.mark.parametrize("before_first", [True, False])
def test_iteration_errors_become_neutral_server_frames(wrapper, before_first):
    def chunks():
        if not before_first:
            yield {"choices": []}
        raise RuntimeError("iteration failed")
    wrapper.llama.create_chat_completion.return_value = chunks()
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[-2][6:])["error"]["type"] == "InternalServerError"
    assert frames.count("data: [DONE]\n\n") == 1


def test_cancellation_propagates(wrapper):
    wrapper.load.side_effect = asyncio.CancelledError()
    with pytest.raises(asyncio.CancelledError):
        call(wrapper, payload={"messages": []})


def client(wrapper, result=None, stream=None):
    calls = Mock()
    class Remote:
        def remote(self, payload):
            calls(payload)
            return stream if stream is not None else AsyncMock(return_value=result)()
    backend = SimpleNamespace(generate=Remote(), generate_embeddings=Remote())
    backend.options = Mock(return_value=backend)
    controller, app = wrapper.namespace["Controller"](backend), wrapper.namespace["app"]
    app.post("/v1/chat/completions")(controller.chat)
    app.post("/v1/completions")(controller.completions)
    app.post("/v1/embeddings")(controller.embeddings)
    return TestClient(app), calls


@pytest.mark.parametrize("route", ["chat/completions", "completions", "embeddings"])
@pytest.mark.parametrize("body", [b"", b"{", b"null", b"[]", b'"x"', b"1", b"true"])
def test_invalid_body_never_dispatches(wrapper, route, body):
    http, calls = client(wrapper)
    response = http.post("/v1/" + route, content=body)
    assert response.status_code == 400 and response.json()["error"]["code"] == 400
    calls.assert_not_called()


@pytest.mark.parametrize("route", ["chat/completions", "completions"])
@pytest.mark.parametrize("stream", [None, "true", "false", 0, 1, [], {}])
def test_non_boolean_stream_never_dispatches(wrapper, route, stream):
    http, calls = client(wrapper)
    assert http.post("/v1/" + route, json={"stream": stream}).status_code == 400
    calls.assert_not_called()


@pytest.mark.parametrize("route", ["chat/completions", "completions", "embeddings"])
def test_empty_object_reaches_business(wrapper, route):
    http, calls = client(wrapper, result={"value": "ok"})
    assert http.post("/v1/" + route, json={}).status_code == 200
    calls.assert_called_once_with({})


def test_first_error_restores_native_http_status(wrapper):
    async def frames():
        yield 'data: {"error":{"message":"failed","type":"internal_server_error","param":null,"code":null}}\n\n'
        yield 'data: [DONE]\n\n'
    http, _ = client(wrapper, stream=frames())
    response = http.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 500 and response.json()["error"]["code"] is None


def test_first_success_chunk_replays_once(wrapper):
    async def frames():
        yield 'data: {"choices":[]}\n\n'
        yield 'data: [DONE]\n\n'
    http, _ = client(wrapper, stream=frames())
    response = http.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 200
    assert response.text.count('data: {"choices":[]}') == 1 and response.text.count('data: [DONE]') == 1


@pytest.mark.parametrize("bad", [object(), float("nan"), float("inf")])
def test_stream_serialization_errors_stay_in_backend(wrapper, bad):
    wrapper.llama.create_chat_completion.return_value = iter([{"value": bad}])
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[0][6:])["error"]["type"] == "InternalServerError"
    assert frames.count("data: [DONE]\n\n") == 1


def test_failure_before_stream_iteration_is_a_neutral_server_error(wrapper):
    wrapper.load.side_effect = RuntimeError("load failed")
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[0][6:])["error"]["message"] == "Internal server error"
    assert frames[-1] == "data: [DONE]\n\n"


def test_controller_rpc_startup_error_is_json_500(wrapper):
    class Backend:
        def options(self, **kwargs):
            raise RuntimeError("private stack")
    controller, app = wrapper.namespace["Controller"](Backend()), wrapper.namespace["app"]
    app.post("/v1/chat/completions")(controller.chat)
    response = TestClient(app).post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 500
    assert response.json()["error"]["type"] == "InternalServerError"
    assert "private stack" not in response.text


def test_stream_cancellation_propagates(wrapper):
    def chunks():
        raise asyncio.CancelledError()
        yield
    wrapper.llama.create_chat_completion.return_value = chunks()
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))


@pytest.mark.parametrize("attribute,payload", [
    ("create_chat_completion", {"messages": []}),
    ("create_completion", {"prompt": "hi"}),
])
@pytest.mark.parametrize("message", [
    "Requested tokens (17) exceed context window of 16",
    "Model path does not exist: private.gguf",
])
@pytest.mark.parametrize("mode", ["nonstream", "before", "midstream"])
def test_escaped_errors_are_neutral_without_engine_error_imports(wrapper, attribute, payload, message, mode):
    if mode == "nonstream":
        getattr(wrapper.llama, attribute).side_effect = ValueError(message)
    else:
        def chunks():
            if mode == "midstream":
                yield {"choices": []}
            raise ValueError(message)
        getattr(wrapper.llama, attribute).return_value = chunks()
    original_import = builtins.__import__
    def forbid_native(name, *args, **kwargs):
        if name.startswith("llama_cpp"):
            raise AssertionError("escaped error handling imported an engine")
        return original_import(name, *args, **kwargs)
    expected = {"error": {
        "message": "Internal server error", "type": "InternalServerError", "param": None, "code": 500,
    }}
    with patch.object(builtins, "__import__", side_effect=forbid_native) as importer:
        result = call(wrapper, payload={**payload, "stream": mode != "nonstream"})
        if mode == "nonstream":
            assert result["status_code"] == 500
            assert result["content"] == expected
        else:
            frames = asyncio.run(collect(result))
            assert json.loads(frames[-2].removeprefix("data:")) == expected
            assert frames.count("data: [DONE]\n\n") == 1
            if mode == "midstream":
                assert json.loads(frames[0].removeprefix("data:")) == {"choices": []}
    assert not any(call.args[0].startswith("llama_cpp") for call in importer.call_args_list)


@pytest.mark.parametrize("status,code", [(400, "context_length_exceeded"), (429, "rate_limit_exceeded")])
def test_returned_engine_error_keeps_status_and_symbolic_code(wrapper, status, code):
    content = {"error": {"message": "native rejection", "type": "invalid_request_error", "param": "messages", "code": code}}
    wrapper.llama.create_chat_completion.return_value = JSONResponse(content, status_code=status)
    wire = call(wrapper, payload={"messages": []})
    assert wire["status_code"] == status
    assert wire["content"] == content
    http, _ = client(wrapper, result=wire)
    response = http.post("/v1/chat/completions", json={"messages": []})
    assert response.status_code == status
    assert response.json() == content


def test_returned_engine_error_dict_keeps_symbolic_code(wrapper):
    content = {"error": {"message": "native rejection", "type": "invalid_request_error", "param": "messages", "code": "context_length_exceeded"}}
    wrapper.llama.create_chat_completion.return_value = content
    wire = call(wrapper, payload={"messages": []})
    assert wire["status_code"] == 400
    assert wire["content"] == content


@pytest.mark.parametrize("midstream", [False, True])
def test_returned_stream_error_keeps_native_fields(wrapper, midstream):
    content = {"error": {"message": "native rejection", "type": "invalid_request_error", "param": "messages", "code": "context_length_exceeded"}}
    chunks = [{"choices": []}, content] if midstream else [content]
    wrapper.llama.create_chat_completion.return_value = iter(chunks)
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[-2].removeprefix("data:")) == content
    assert frames.count("data: [DONE]\n\n") == 1
    async def stream():
        for frame in frames:
            yield frame
    http, _ = client(wrapper, stream=stream())
    response = http.post("/v1/chat/completions", json={"messages": [], "stream": True})
    if midstream:
        assert response.status_code == 200
        assert content == json.loads(response.text.split("\n\n")[1].removeprefix("data:"))
    else:
        assert response.status_code == 400
        assert response.json() == content
