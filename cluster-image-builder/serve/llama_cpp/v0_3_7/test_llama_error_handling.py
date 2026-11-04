"""Exercise shipped llama.cpp methods without native inference dependencies."""
import ast
import asyncio
import builtins
import copy
import json
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Dict, Optional, AsyncGenerator
from unittest.mock import Mock, patch

import pytest
from fastapi import FastAPI, HTTPException, Request
from fastapi.testclient import TestClient
from pydantic import BaseModel
from starlette.concurrency import iterate_in_threadpool
from starlette.responses import JSONResponse, StreamingResponse

from serve._utils import serve_response


APP_PATH = Path(__file__).with_name("app.py")
WIRE_PATH = Path(__file__).with_name("wire.py")


def neutral_error(message, error_type="RuntimeError", code=500):
    return {"error": {"message": message, "type": error_type, "code": code, "param": None}}


@pytest.fixture
def wrapper():
    tree, body = ast.parse(APP_PATH.read_text()), []
    wire_body = []
    for node in ast.parse(WIRE_PATH.read_text()).body:
        if isinstance(node, ast.ImportFrom) and node.module and node.module.startswith("serve._utils."):
            wire_body.append(copy.deepcopy(node))
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == "_stream_chunks":
            wire_body.append(copy.deepcopy(node))
    for node in tree.body:
        if isinstance(node, ast.ImportFrom) and node.module and node.module.startswith("serve._utils.") and node.module != "serve._utils.runtime_env":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id == "app" for t in node.targets):
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_exception_handler":
            body.append(copy.deepcopy(node))
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == "_filter_supported":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.ClassDef) and node.name in {"Backend", "Controller"}:
            cls = copy.deepcopy(node)
            cls.decorator_list = []
            cls.body = [n for n in cls.body if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and (cls.name == "Controller" or n.name in {"generate", "generate_embeddings", "show_available_models"})]
            for method in cls.body:
                method.decorator_list = []
            body.append(cls)
    llama = SimpleNamespace(create_chat_completion=Mock(return_value={"choices": []}), create_completion=Mock(return_value={"choices": []}), create_embedding=Mock(return_value={"data": []}))
    load = Mock(return_value=llama)
    class ChatRequest(BaseModel):
        messages: list[dict]
        max_tokens: int | None = None
        stream: bool = False
    class CompletionRequest(BaseModel):
        prompt: str
        max_tokens: int | None = None
        stream: bool = False
    class EmbeddingRequest(BaseModel):
        input: str
    ns = dict(Any=Any, Dict=Dict, Optional=Optional, AsyncGenerator=AsyncGenerator, Request=Request, JSONResponse=JSONResponse, StreamingResponse=StreamingResponse,
              FastAPI=FastAPI, HTTPException=HTTPException, DeploymentHandle=object, DeploymentResponseGenerator=object, json=json, iterate_in_threadpool=iterate_in_threadpool,
              CreateChatCompletionRequest=ChatRequest, CreateCompletionRequest=CompletionRequest, CreateEmbeddingRequest=EmbeddingRequest,
              LlamaProxy=SimpleNamespace(load_llama_from_model_settings=load), _CHAT_PARAMS={"messages", "model", "stream"}, _COMPLETION_PARAMS={"prompt", "model", "stream"}, _EMBEDDING_PARAMS={"input", "model"},
              backend_error=serve_response.backend_error, backend_response=serve_response.backend_response,
              error_stream=serve_response.error_stream, open_stream=serve_response.open_stream,
              RequestInputError=serve_response.RequestInputError, input_error_handler=serve_response.input_error_handler,
              is_streaming=serve_response.is_streaming, read_json_object=serve_response.read_json_object,
              to_wire_response=serve_response.to_wire_response)
    exec(compile(ast.fix_missing_locations(ast.Module(body=wire_body, type_ignores=[])), str(WIRE_PATH), "exec"), ns)
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


@pytest.mark.parametrize("spelling,streaming", [
    ("false", False), ("no", False), (0, False), (None, False),
    ("yes", True), (1, True), (True, True),
])
def test_generate_hands_llama_the_dispatch_decision(wrapper, spelling, streaming):
    # llama_cpp tests `stream` for truthiness, so the Backend must pass the
    # boolean the Controller dispatched on, not the client's spelling.
    if streaming:
        wrapper.llama.create_chat_completion.return_value = iter([{"choices": []}])
    result = call(wrapper, payload={"messages": [], "stream": spelling})
    if streaming:
        assert json.loads(asyncio.run(collect(result))[0][6:]) == {"choices": []}
    else:
        assert result["status_code"] == 200
    assert wrapper.llama.create_chat_completion.call_args.kwargs["stream"] is streaming


@pytest.mark.parametrize("method,payload", [("generate", {"messages": []}), ("generate_embeddings", {"input": "hi"})])
def test_model_load_error_is_contained(wrapper, method, payload):
    wrapper.load.side_effect = RuntimeError("load failed")
    result = call(wrapper, method, payload)
    assert result["status_code"] == 500
    assert result["content"] == neutral_error("load failed")


def test_model_list_error_is_contained(wrapper):
    del wrapper.backend.model_id
    wire = call(wrapper, "show_available_models")
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["type"] == "AttributeError"


@pytest.mark.parametrize("attribute,payload", [
    ("create_chat_completion", {"messages": []}),
    ("create_completion", {"prompt": "hi"}),
])
@pytest.mark.parametrize("message", [
    "Requested tokens (17) exceed context window of 16",
    "Model path does not exist: private.gguf",
])
def test_engine_errors_keep_message_and_class_name(wrapper, attribute, payload, message):
    # llama_cpp raises for these; the native HTTP route's formatter is not on
    # this path, so the neutral body is the contract.
    getattr(wrapper.llama, attribute).side_effect = ValueError(message)
    wire = call(wrapper, payload=payload)
    assert wire["status_code"] == 500
    assert wire["content"] == neutral_error(message, "ValueError")


def test_nonfinite_values_are_sanitized(wrapper):
    wrapper.llama.create_chat_completion.return_value = {"value": float("nan")}
    wire = call(wrapper, payload={"messages": []})
    assert wire["status_code"] == 200
    assert wire["content"] == {"value": None}
    json.dumps(wire, allow_nan=False)


def test_sync_iterator_stays_in_backend(wrapper):
    wrapper.llama.create_chat_completion.return_value = iter([{"choices": []}])
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[0][6:]) == {"choices": []}
    assert frames.count("data: [DONE]\n\n") == 1


@pytest.mark.parametrize("before_first", [True, False])
def test_iteration_errors_are_neutral_and_terminate_once(wrapper, before_first):
    def chunks():
        if not before_first:
            yield {"choices": []}
        raise RuntimeError("iteration failed")
    wrapper.llama.create_chat_completion.return_value = chunks()
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[-2].removeprefix("data:")) == neutral_error("iteration failed")
    assert frames[-1] == "data: [DONE]\n\n"
    assert frames.count("data: [DONE]\n\n") == 1
    assert (frames[0] == 'data: {"choices": []}\n\n') is (not before_first)


def test_stream_serialization_errors_stay_in_backend(wrapper):
    wrapper.llama.create_chat_completion.return_value = iter([{"value": object()}])
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    body = json.loads(frames[0].removeprefix("data:"))
    assert body["error"]["type"] == "TypeError"
    assert body["error"]["code"] == 500
    assert frames.count("data: [DONE]\n\n") == 1


def test_stream_nonfinite_values_are_sanitized(wrapper):
    wrapper.llama.create_chat_completion.return_value = iter([{"value": float("nan")}])
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[0][6:]) == {"value": None}
    assert frames.count("data: [DONE]\n\n") == 1


@pytest.mark.parametrize("method,attribute,payload", [
    ("generate", "create_chat_completion", {"messages": []}),
    ("generate_embeddings", "create_embedding", {"input": "hi"}),
])
def test_cancellation_propagates(wrapper, method, attribute, payload):
    getattr(wrapper.llama, attribute).side_effect = asyncio.CancelledError()
    with pytest.raises(asyncio.CancelledError):
        call(wrapper, method, payload)


def test_stream_cancellation_propagates(wrapper):
    def chunks():
        raise asyncio.CancelledError()
        yield
    wrapper.llama.create_chat_completion.return_value = chunks()
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))


class Handle:
    """Backend handle that records what the Controller asked for."""

    def __init__(self, result=None, stream=None):
        self.calls, self.result, self.stream, self.streaming = [], result, stream, False
        self.generate = self.generate_embeddings = self.show_available_models = self

    def options(self, **kwargs):
        self.streaming = kwargs["stream"]
        return self

    def remote(self, payload=None):
        self.calls.append(payload)
        if self.streaming:
            return self.stream
        async def value():
            return self.result if self.result is not None else serve_response.wire_response({"choices": []})
        return value()


def client(wrapper, handle):
    controller, app = wrapper.namespace["Controller"](handle), wrapper.namespace["app"]
    app.post("/v1/chat/completions")(controller.chat)
    app.post("/v1/completions")(controller.completions)
    app.post("/v1/embeddings")(controller.embeddings)
    app.get("/v1/models")(controller.models)
    return TestClient(app, raise_server_exceptions=False)


@pytest.mark.parametrize("body,message", [
    (b"[1, 2]", "Request body must be a JSON object"),
    (b"{not json", "Request body must be valid JSON"),
])
def test_controller_rejects_non_object_bodies_before_dispatch(wrapper, body, message):
    handle = Handle()
    with client(wrapper, handle) as http:
        response = http.post("/v1/chat/completions", content=body, headers={"content-type": "application/json"})
    assert response.status_code == 400
    assert response.json() == {"error": {"message": message, "type": "BadRequestError", "code": 400, "param": None}}
    assert handle.calls == []


@pytest.mark.parametrize("stream", ["false", "no", 0, None])
def test_controller_treats_false_like_spellings_as_non_streaming(wrapper, stream):
    handle = Handle()
    with client(wrapper, handle) as http:
        response = http.post("/v1/chat/completions", json={"messages": [], "stream": stream})
    assert response.status_code == 200
    assert handle.streaming is False


@pytest.mark.parametrize("stream", [2, "maybe"])
def test_controller_rejects_uninterpretable_stream_values(wrapper, stream):
    handle = Handle()
    with client(wrapper, handle) as http:
        response = http.post("/v1/chat/completions", json={"messages": [], "stream": stream})
    assert response.status_code == 400
    assert response.json()["error"]["message"] == "stream must be a boolean"
    assert handle.calls == []


def test_first_error_restores_http_status(wrapper):
    async def frames():
        yield 'data: {"error":{"message":"failed","type":"RuntimeError","param":null,"code":500}}\n\n'
        yield 'data: [DONE]\n\n'
    handle = Handle(stream=frames())
    with client(wrapper, handle) as http:
        response = http.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 500
    assert response.json() == neutral_error("failed")


def test_first_success_chunk_replays_once(wrapper):
    async def frames():
        yield 'data: {"choices":[]}\n\n'
        yield 'data: [DONE]\n\n'
    handle = Handle(stream=frames())
    with client(wrapper, handle) as http:
        response = http.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 200
    assert response.text == 'data: {"choices":[]}\n\ndata: [DONE]\n\n'


def test_stream_error_reaches_the_client_with_its_message(wrapper):
    wrapper.llama.create_chat_completion.side_effect = ValueError("Requested tokens (17) exceed context window of 16")
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    async def replay():
        for frame in frames:
            yield frame
    handle = Handle(stream=replay())
    with client(wrapper, handle) as http:
        response = http.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 500
    assert response.json() == neutral_error("Requested tokens (17) exceed context window of 16", "ValueError")


def test_controller_rpc_startup_error_is_json_500(wrapper):
    class Backend:
        def options(self, **kwargs):
            raise RuntimeError("rpc unavailable")
    controller, app = wrapper.namespace["Controller"](Backend()), wrapper.namespace["app"]
    app.post("/v1/chat/completions")(controller.chat)
    response = TestClient(app).post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 500
    assert response.json() == neutral_error("rpc unavailable")


@pytest.mark.parametrize("status", [400, 503])
def test_returned_engine_error_keeps_status_and_body(wrapper, status):
    content = {"error": {"message": "native rejection", "type": "invalid_request_error", "param": "messages", "code": status}}
    wrapper.llama.create_chat_completion.return_value = JSONResponse(content, status_code=status)
    wire = call(wrapper, payload={"messages": []})
    assert wire["status_code"] == status
    assert wire["content"] == content
    with client(wrapper, Handle(wire)) as http:
        response = http.post("/v1/chat/completions", json={"messages": []})
    assert response.status_code == status
    assert response.json() == content


@pytest.mark.parametrize("midstream", [False, True])
def test_returned_stream_error_keeps_native_fields(wrapper, midstream):
    content = {"error": {"message": "native rejection", "type": "invalid_request_error", "param": "messages", "code": 400}}
    chunks = [{"choices": []}, content] if midstream else [content]
    wrapper.llama.create_chat_completion.return_value = iter(chunks)
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    assert json.loads(frames[-2].removeprefix("data:")) == content
    assert frames.count("data: [DONE]\n\n") == 1


def test_symbolic_error_code_is_not_reinterpreted(wrapper):
    # Only a numeric HTTP code is read from a body the engine returns; mapping
    # symbolic engine codes would restore the per-version table this boundary
    # removes.
    content = {"error": {"message": "too long", "type": "invalid_request_error", "param": "messages", "code": "context_length_exceeded"}}
    wrapper.llama.create_chat_completion.return_value = content
    wire = call(wrapper, payload={"messages": []})
    assert wire["status_code"] == 500
    assert wire["content"] == content


@pytest.mark.parametrize("stage", ["setup", "midstream"])
def test_http_exception_is_not_reinterpreted(wrapper, stage):
    error = HTTPException(503, "busy")
    if stage == "setup":
        wrapper.llama.create_chat_completion.side_effect = error
        frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    else:
        def chunks():
            yield {"choices": []}
            raise error
        wrapper.llama.create_chat_completion.return_value = chunks()
        frames = asyncio.run(collect(call(wrapper, payload={"messages": [], "stream": True})))
    expected = neutral_error(f"{error.status_code}: {error.detail}", "HTTPException")
    assert json.loads(frames[-2].removeprefix("data:")) == expected
    async def replay():
        for frame in frames:
            yield frame
    with client(wrapper, Handle(stream=replay())) as http:
        response = http.post("/v1/chat/completions", json={"stream": True})
    if stage == "midstream":
        assert response.status_code == 200
        assert json.loads(response.text.split("\n\n")[1].removeprefix("data:")) == expected
    else:
        assert response.status_code == 500
        assert response.json() == expected


@pytest.mark.parametrize("method,attribute,payload", [
    ("generate", "create_chat_completion", {"messages": []}),
    ("generate_embeddings", "create_embedding", {"input": "hi"}),
])
def test_engine_errors_never_import_the_engine(wrapper, method, attribute, payload):
    getattr(wrapper.llama, attribute).side_effect = ValueError("context window exceeded")
    original_import, engine_imports = builtins.__import__, []

    def forbid_engine(name, *args, **kwargs):
        if name.startswith("llama_cpp"):
            engine_imports.append(name)
            raise AssertionError("error handling imported the engine")
        return original_import(name, *args, **kwargs)

    with patch.object(builtins, "__import__", side_effect=forbid_engine):
        wire = call(wrapper, method, payload)
    assert wire["status_code"] == 500
    assert wire["content"] == neutral_error("context window exceeded", "ValueError")
    assert engine_imports == []
