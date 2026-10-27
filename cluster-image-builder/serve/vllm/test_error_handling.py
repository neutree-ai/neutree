"""Exercise both shipped wrappers without importing the GPU/Ray runtime."""

import ast
import builtins
import asyncio
import copy
import json
import logging
from pathlib import Path
from http import HTTPStatus
from types import SimpleNamespace
from typing import Any, Dict, Optional
from unittest.mock import AsyncMock

import pytest
from fastapi import FastAPI, Request
from fastapi.testclient import TestClient
from pydantic import BaseModel
from starlette.responses import JSONResponse, Response, StreamingResponse

from serve._utils import serve_errors


class ErrorInfo(BaseModel):
    message: str
    type: str
    code: int
    param: Optional[str] = None


class ErrorResponse(BaseModel):
    error: ErrorInfo


class Model(BaseModel):
    value: str = "ok"


def request_model(**payload):
    if payload.get("invalid_model"):
        raise ValueError("request model rejected")
    return payload


class NativeClientError(Exception):
    pass


class NativeNotFoundError(NativeClientError):
    pass


class NativeGracefulHTTPError(Exception):
    def __init__(self, message, http_status: HTTPStatus):
        super().__init__(message)
        self.http_status = http_status


class QueueOverflowError(NativeGracefulHTTPError):
    def __init__(self):
        super().__init__("engine queue is full", HTTPStatus.SERVICE_UNAVAILABLE)


class MaxQueuedTokensError(NativeGracefulHTTPError):
    def __init__(self):
        super().__init__("prefill token backlog exceeded", HTTPStatus.SERVICE_UNAVAILABLE)


class TemplateError(Exception):
    pass


class TemplateSyntaxError(TemplateError):
    pass



@pytest.fixture(params=("v0_24_0", "v0_31_0"))
def wrapper(request):
    app_path = Path(__file__).parent / request.param / "app.py"
    tree = ast.parse(app_path.read_text())
    names = {"generate", "generate_embeddings", "rerank", "show_available_models", "_to_wire_response", "_to_wire_stream"}
    validation_base = ValueError if request.param == "v0_24_0" else NativeClientError
    validation_error = type("NativeValidationError", (validation_base,), {"parameter": "input_tokens"})
    body = []
    for node in tree.body:
        if isinstance(node, ast.ImportFrom) and node.module == "serve._utils.serve_errors":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id in {"_WIRE_RESPONSE_MARKER", "app"} for target in node.targets
        ):
            body.append(copy.deepcopy(node))
        elif (isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
              and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_exception_handler"):
            body.append(copy.deepcopy(node))
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in {
            "_result_to_response",
        }:
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.ClassDef) and node.name in {"Backend", "Controller"}:
            cls = copy.deepcopy(node)
            cls.decorator_list = []
            cls.body = [method for method in cls.body if isinstance(method, (ast.FunctionDef, ast.AsyncFunctionDef))
                        and (cls.name == "Controller" or method.name in names)]
            for method in cls.body:
                method.decorator_list = []
            body.append(cls)
    namespace = dict(
        Any=Any, Dict=Dict, Optional=Optional, Request=Request, Response=Response, FastAPI=FastAPI,
        JSONResponse=JSONResponse, StreamingResponse=StreamingResponse, DeploymentHandle=object,
        ErrorResponse=ErrorResponse, ErrorInfo=ErrorInfo, ChatCompletionRequest=request_model,
        EmbeddingCompletionRequest=request_model, RerankRequest=request_model,
        json=json, logging=logging,
    )
    exec(compile(ast.fix_missing_locations(ast.Module(body=body, type_ignores=[])), str(app_path), "exec"), namespace)
    backend = object.__new__(namespace["Backend"])
    backend._ensure_chat = AsyncMock()
    backend._ensure_embedding = AsyncMock()
    backend._ensure_score = AsyncMock()
    backend.openai_serving_chat = SimpleNamespace(create_chat_completion=AsyncMock(return_value=Model()))
    backend.openai_serving_embedding = AsyncMock(return_value=Model())
    backend.openai_serving_score = AsyncMock(return_value=Model())
    backend._ensure_models = AsyncMock(return_value=SimpleNamespace(show_available_models=AsyncMock(return_value=Model())))
    return SimpleNamespace(namespace=namespace, backend=backend, validation_error=validation_error)


def call_backend(wrapper, method, payload=None):
    args = () if method == "show_available_models" else (payload if payload is not None else {},)
    return asyncio.run(getattr(wrapper.backend, method)(*args))


@pytest.mark.parametrize("method", ("generate", "generate_embeddings", "rerank", "show_available_models"))
def test_backend_success_returns_plain_wire(wrapper, method):
    wire = call_backend(wrapper, method)
    assert wire["status_code"] == 200
    assert wire["content"] == {"value": "ok"}
    json.dumps(wire)


@pytest.mark.parametrize("method,initializer", (
    ("generate", "_ensure_chat"), ("generate_embeddings", "_ensure_embedding"),
    ("rerank", "_ensure_score"), ("show_available_models", "_ensure_models"),
))
def test_lazy_initialization_exceptions_are_contained(wrapper, method, initializer):
    error = RuntimeError("initialization failed")
    getattr(wrapper.backend, initializer).side_effect = error
    wire = call_backend(wrapper, method)
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["message"] == "Internal server error"


@pytest.mark.parametrize("method,attribute", (
    ("generate", "openai_serving_chat"), ("generate_embeddings", "openai_serving_embedding"),
    ("rerank", "openai_serving_score"), ("show_available_models", "_ensure_models"),
))
def test_escaping_serving_exception_is_neutree_error(wrapper, method, attribute):
    error = wrapper.validation_error("input too long")
    if method == "generate":
        target = wrapper.backend.openai_serving_chat.create_chat_completion
    elif method == "show_available_models":
        target = wrapper.backend._ensure_models.return_value.show_available_models
    else:
        target = getattr(wrapper.backend, attribute)
    target.side_effect = error
    wire = call_backend(wrapper, method)
    assert wire["status_code"] == 500
    assert wire["content"] == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}


@pytest.mark.parametrize("method", ("generate", "generate_embeddings", "rerank"))
def test_request_model_exception_is_contained(wrapper, method):
    wire = call_backend(wrapper, method, {"invalid_model": True})
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["message"] == "Internal server error"


@pytest.mark.parametrize("payload", (None, [], "body", 1, True))
def test_non_mapping_backend_payload_is_contained(wrapper, payload):
    wire = asyncio.run(wrapper.backend.generate(payload))
    assert wire["status_code"] == 400
    assert "error" in wire["content"]


@pytest.mark.parametrize("payload", ({"stream": "true"}, {"stream": "false"}, {"stream": 1}, {"stream": None}))
def test_invalid_stream_never_reaches_serving(wrapper, payload):
    wire = call_backend(wrapper, "generate", payload)
    assert wire["status_code"] == 400
    wrapper.backend.openai_serving_chat.create_chat_completion.assert_not_called()


@pytest.mark.parametrize("method", ("generate", "generate_embeddings", "rerank", "show_available_models"))
@pytest.mark.parametrize("error_type", (RuntimeError, ValueError, TypeError))
def test_wire_serialization_failure_is_contained(wrapper, method, error_type):
    class BrokenModel:
        def model_dump(self, **kwargs):
            raise error_type("serialization failed")
    if method == "generate":
        target = wrapper.backend.openai_serving_chat.create_chat_completion
    elif method == "generate_embeddings":
        target = wrapper.backend.openai_serving_embedding
    elif method == "rerank":
        target = wrapper.backend.openai_serving_score
    else:
        target = wrapper.backend._ensure_models.return_value.show_available_models
    target.return_value = BrokenModel()
    wire = call_backend(wrapper, method)
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["message"] == "Internal server error"


@pytest.mark.parametrize("value", (float("nan"), float("inf")))
@pytest.mark.parametrize("as_model", (False, True))
def test_wire_nonfinite_numbers_are_rejected_before_ray(wrapper, value, as_model):
    class NonfiniteModel:
        def model_dump(self, **kwargs):
            return {"value": value}
    result = NonfiniteModel() if as_model else {"value": value}
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = result

    wire = call_backend(wrapper, "generate")

    assert wire["status_code"] == 500
    assert wire["content"] == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    # The Controller's real encoder must accept the boundary's replacement error.
    JSONResponse(content=wire["content"], status_code=wire["status_code"])


@pytest.mark.parametrize("method,initializer", (
    ("generate", "_ensure_chat"), ("generate_embeddings", "_ensure_embedding"),
    ("rerank", "_ensure_score"), ("show_available_models", "_ensure_models"),
))
def test_cancellation_still_propagates(wrapper, method, initializer):
    getattr(wrapper.backend, initializer).side_effect = asyncio.CancelledError()
    with pytest.raises(asyncio.CancelledError):
        call_backend(wrapper, method)


def test_backend_streams_preserve_first_and_midstream_errors(wrapper):
    async def collect(stream):
        return [chunk async for chunk in stream]
    error = ValueError("before first chunk")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    stream = call_backend(wrapper, "generate", {"stream": True})
    chunks = asyncio.run(collect(stream))
    assert json.loads(chunks[0].removeprefix("data:"))["error"]["code"] == 500
    assert chunks[-1] == "data: [DONE]\n\n"

    async def broken_stream():
        yield "data: first\n\n"
        raise ValueError("midstream")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = None
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = broken_stream()
    chunks = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True})))
    assert chunks[0] == "data: first\n\n"
    assert json.loads(chunks[1].removeprefix("data:"))["error"]["message"] == "Internal server error"
    assert chunks[2] == "data: [DONE]\n\n"


class Handle:
    def __init__(self, result=None):
        self.calls = []
        self.result = result if result is not None else {"accepted": True}
        self.generate = self.generate_embeddings = self.rerank = self

    def options(self, **kwargs):
        self.stream = kwargs["stream"]
        return self

    def remote(self, payload):
        self.calls.append(payload)
        if self.stream:
            return self.result
        async def result():
            return self.result
        return result()


def client_for(wrapper, handle):
    cls = wrapper.namespace["Controller"]
    controller = cls(handle)
    app = wrapper.namespace["app"]
    for path, method in (("chat/completions", "chat"), ("embeddings", "embeddings"), ("rerank", "rerank")):
        app.post("/v1/" + path)(getattr(controller, method))
    return TestClient(app, raise_server_exceptions=False)


@pytest.mark.parametrize("path", ("chat/completions", "embeddings", "rerank"))
@pytest.mark.parametrize("body", ("", "not json", '{"input":}', "null", "[]", '"body"', "1", "true"))
def test_controller_rejects_bad_body_before_rpc(wrapper, path, body):
    handle = Handle()
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/" + path, content=body, headers={"Content-Type": "application/json"})
    assert response.status_code == 400, response.text
    assert set(response.json()["error"]) == {"message", "type", "code", "param"}
    assert not handle.calls


@pytest.mark.parametrize("path", ("chat/completions", "embeddings", "rerank"))
def test_controller_empty_object_reaches_business_validation(wrapper, path):
    handle = Handle()
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/" + path, json={})
    assert response.status_code == 200
    assert handle.calls == [{}]


@pytest.mark.parametrize("stream", ("true", "false", 0, 1, None, [], {}))
def test_controller_rejects_non_bool_stream_before_rpc(wrapper, stream):
    handle = Handle()
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/chat/completions", json={"stream": stream})
    assert response.status_code == 400, response.text
    assert not handle.calls


def test_controller_stream_error_preserves_http_status_and_envelope(wrapper):
    payload = {"error": {"message": "too long", "type": "BadRequestError", "code": 400, "param": "input_tokens"}}
    async def stream():
        yield "data: " + json.dumps(payload) + "\n\n"
        yield "data: [DONE]\n\n"
    with client_for(wrapper, Handle(stream())) as client:
        response = client.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 400
    assert response.json() == payload


def test_controller_replays_first_success_chunk(wrapper):
    async def stream():
        yield "data: first\n\n"
        yield "data: [DONE]\n\n"
    with client_for(wrapper, Handle(stream())) as client:
        response = client.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 200
    assert response.text == "data: first\n\ndata: [DONE]\n\n"


@pytest.mark.parametrize("stream_value", ({}, {"stream": False}))
def test_controller_accepts_non_streaming_bool(wrapper, stream_value):
    handle = Handle()
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/chat/completions", json=stream_value)
    assert response.status_code == 200
    assert handle.calls == [stream_value]
    assert handle.stream is False


@pytest.mark.parametrize("stage", ("options", "remote", "first_chunk"))
def test_stream_rpc_initialization_failure_has_error_envelope(wrapper, stage):
    class FailingHandle(Handle):
        def options(self, **kwargs):
            if stage == "options":
                raise RuntimeError("RPC unavailable")
            return super().options(**kwargs)

        def remote(self, payload):
            if stage == "remote":
                raise RuntimeError("RPC unavailable")
            return super().remote(payload)

    async def failed_stream():
        raise RuntimeError("RPC unavailable")
        yield
    with client_for(wrapper, FailingHandle(failed_stream())) as client:
        response = client.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 500
    assert set(response.json()["error"]) == {"message", "type", "code", "param"}


def test_backend_in_band_error_keeps_upstream_details(wrapper):
    error = ErrorResponse(error=ErrorInfo(message="unknown model", type="NotFoundError", code=404, param="model"))
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = error
    wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == 404
    assert wire["content"] == error.model_dump(mode="json")
    async def collect():
        return [chunk async for chunk in await wrapper.backend.generate({"stream": True})]
    chunks = asyncio.run(collect())
    assert json.loads(chunks[0].removeprefix("data:")) == error.model_dump(mode="json")
    assert chunks[-1] == "data: [DONE]\n\n"


@pytest.mark.parametrize("method,initializer", (
    ("generate", "_ensure_chat"), ("generate_embeddings", "_ensure_embedding"),
    ("rerank", "_ensure_score"), ("show_available_models", "_ensure_models"),
))
@pytest.mark.parametrize("error_type", (RuntimeError, ValueError, TypeError, NativeNotFoundError))
def test_escaping_errors_do_not_import_engine_helpers(wrapper, monkeypatch, method, initializer, error_type):
    original_import = builtins.__import__
    engine_imports = []
    def block_engine_import(name, *args, **kwargs):
        if name.startswith(("vllm", "sglang", "llama_cpp", "torch")):
            engine_imports.append(name)
            raise AssertionError("exception containment must not import engine helpers")
        return original_import(name, *args, **kwargs)
    monkeypatch.setattr(builtins, "__import__", block_engine_import)
    getattr(wrapper.backend, initializer).side_effect = error_type("private engine failure")

    wire = call_backend(wrapper, method)

    assert wire["status_code"] == 500
    assert wire["content"] == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    assert not engine_imports


def test_escaping_native_not_found_is_neutree_error(wrapper):
    error = NativeNotFoundError("unknown native model")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == 500
    assert wire["content"]["error"]["type"] == "InternalServerError"


@pytest.mark.parametrize("stage", ("before_stream", "midstream"))
def test_server_stream_errors_use_neutral_error_without_native_factory(wrapper, stage):
    async def failed_stream():
        yield "data: first\n\n"
        raise RuntimeError("private stream detail")
    serving = wrapper.backend.openai_serving_chat.create_chat_completion
    if stage == "before_stream":
        serving.side_effect = RuntimeError("private startup detail")
    else:
        serving.return_value = failed_stream()

    async def collect():
        return [chunk async for chunk in await wrapper.backend.generate({"stream": True})]
    frames = asyncio.run(collect())

    error_frame = frames[0] if stage == "before_stream" else frames[1]
    assert json.loads(error_frame.removeprefix("data:")) == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    assert frames[-1] == "data: [DONE]\n\n"


@pytest.mark.parametrize("error_type", (ValueError, TypeError, TemplateError, TemplateSyntaxError, QueueOverflowError, MaxQueuedTokensError))
@pytest.mark.parametrize("stage", ("call", "first", "midstream"))
def test_escaping_stream_exceptions_are_neutree_errors(wrapper, error_type, stage):
    error = error_type() if error_type in (QueueOverflowError, MaxQueuedTokensError) else error_type("private error")
    async def chunks():
        if stage == "midstream":
            yield 'data: {"choices":[]}\n\n'
        raise error
        yield
    target = wrapper.backend.openai_serving_chat.create_chat_completion
    if stage == "call":
        target.side_effect = error
    else:
        target.return_value = chunks()
    async def collect():
        return [frame async for frame in await wrapper.backend.generate({"stream": True})]
    frames = asyncio.run(collect())
    assert json.loads(frames[-2].removeprefix("data:")) == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    assert frames.count("data: [DONE]\n\n") == 1


@pytest.mark.parametrize("status", (400, 404, 422, 429, 500, 503))
def test_native_error_values_are_preserved_in_both_modes(wrapper, status):
    result = ErrorResponse(error=ErrorInfo(message="native error details", type="NativeError", code=status, param="model"))
    target = wrapper.backend.openai_serving_chat.create_chat_completion
    target.return_value = result
    wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == status
    assert wire["content"] == result.model_dump(mode="json")
    assert wrapper.namespace["_result_to_response"](wire).status_code == status
    async def chunks():
        yield f"data: {json.dumps(result.model_dump())}\n\n"
        yield "data: [DONE]\n\n"
    expected = [f"data: {json.dumps(result.model_dump())}\n\n", "data: [DONE]\n\n"]
    target.return_value = chunks()
    async def collect():
        return [frame async for frame in await wrapper.backend.generate({"stream": True})]
    assert asyncio.run(collect()) == expected


@pytest.mark.parametrize("value", (float("nan"), float("inf"), object()))
@pytest.mark.parametrize("midstream", (False, True))
def test_stream_serialization_errors_are_server_errors(wrapper, value, midstream):
    async def chunks():
        if midstream:
            yield 'data: {"choices":[]}\n\n'
        yield {"value": value}
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = chunks()
    async def collect():
        return [frame async for frame in await wrapper.backend.generate({"stream": True})]
    frames = asyncio.run(collect())
    error_index = 1 if midstream else 0
    assert json.loads(frames[error_index].removeprefix("data:")) == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    assert frames.count("data: [DONE]\n\n") == 1
