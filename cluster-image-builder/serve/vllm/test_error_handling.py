"""Exercise both shipped wrappers without importing the GPU/Ray runtime."""

import ast
import asyncio
import copy
import json
import logging
from http import HTTPStatus
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Dict, Optional
from unittest.mock import AsyncMock

import pytest
from fastapi import FastAPI, HTTPException, Request
from fastapi.testclient import TestClient
from pydantic import BaseModel, ValidationError
from starlette.responses import JSONResponse, Response, StreamingResponse

from serve._utils import serve_response


class ErrorInfo(BaseModel):
    message: str
    type: str
    code: int
    param: Optional[str] = None


class ErrorResponse(BaseModel):
    error: ErrorInfo


class Model(BaseModel):
    value: str = "ok"


def fake_create_error_response(message, err_type="BadRequestError",
                               status_code=HTTPStatus.BAD_REQUEST, param=None):
    """Stand-in for vLLM's exception classifier.

    The real factory lives in the engine image and cannot be imported here, so
    this mirrors its contract for the exception classes the wrappers can hand
    it.
    """
    if isinstance(message, Exception):
        exc = message
        if isinstance(exc, (ValueError, TypeError, OverflowError)):
            err_type, status_code, param = "BadRequestError", HTTPStatus.BAD_REQUEST, None
        elif isinstance(exc, NotImplementedError):
            err_type, status_code, param = "NotImplementedError", HTTPStatus.NOT_IMPLEMENTED, None
        else:
            err_type, status_code, param = "InternalServerError", HTTPStatus.INTERNAL_SERVER_ERROR, None
        message = str(exc)
    return ErrorResponse(error=ErrorInfo(message=message, type=err_type,
                                         code=int(status_code), param=param))


def request_model(**payload):
    if payload.get("invalid_model"):
        raise ValueError("request model rejected")
    return payload


@pytest.fixture(params=("v0_24_0", "v0_31_0"))
def wrapper(request):
    app_path = Path(__file__).parent / request.param / "app.py"
    body = []
    for node in ast.parse(app_path.read_text()).body:
        if isinstance(node, ast.ImportFrom) and node.module == "serve._utils.serve_response":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Assign) and any(
            isinstance(target, ast.Name) and target.id == "app" for target in node.targets
        ):
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.FunctionDef) and node.name == "_engine_error":
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.ClassDef) and node.name in {"Backend", "Controller"}:
            cls = copy.deepcopy(node)
            cls.decorator_list = []
            names = {"generate", "generate_embeddings", "rerank", "show_available_models"}
            cls.body = [method for method in cls.body if isinstance(method, (ast.FunctionDef, ast.AsyncFunctionDef))
                        and (cls.name == "Controller" or method.name in names)]
            for method in cls.body:
                method.decorator_list = []
            body.append(cls)
        elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_exception_handler":
            body.append(copy.deepcopy(node))
    namespace = dict(
        Any=Any, Dict=Dict, Optional=Optional, Request=Request, Response=Response, FastAPI=FastAPI,
        JSONResponse=JSONResponse, StreamingResponse=StreamingResponse, DeploymentHandle=object,
        DeploymentResponseGenerator=object,
        ChatCompletionRequest=request_model, EmbeddingCompletionRequest=request_model,
        RerankRequest=request_model, json=json, logging=logging, HTTPStatus=HTTPStatus,
        HTTPException=HTTPException, ValidationError=ValidationError,
        # The module under test, not a stand-in: the wrappers now use the shared
        # boundary directly, so the tests exercise the real implementation.
        RequestInputError=serve_response.RequestInputError,
        backend_error=serve_response.backend_error,
        backend_response=serve_response.backend_response,
        error_stream=serve_response.error_stream,
        input_error_handler=serve_response.input_error_handler,
        is_streaming=serve_response.is_streaming,
        open_stream=serve_response.open_stream,
        read_json_object=serve_response.read_json_object,
        to_wire_response=serve_response.to_wire_response,
        to_wire_stream=serve_response.to_wire_stream,
        # No vLLM in this environment, so the classifier is a stand-in.
        create_error_response=fake_create_error_response,
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
    return SimpleNamespace(namespace=namespace, backend=backend, version=request.param)


def call_backend(wrapper, method, payload=None):
    args = () if method == "show_available_models" else (payload if payload is not None else {},)
    return asyncio.run(getattr(wrapper.backend, method)(*args))


async def collect(chunks):
    return [chunk async for chunk in chunks]


def error_body(message, error_type="RuntimeError", code=500):
    return {"error": {"message": message, "type": error_type, "code": code, "param": None}}


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
@pytest.mark.parametrize("stream", (False, True))
def test_raised_exceptions_become_neutral_errors(wrapper, method, initializer, stream):
    if stream and method != "generate":
        pytest.skip("Only generation supports streaming")
    error = RuntimeError("initialization failed")
    getattr(wrapper.backend, initializer).side_effect = error
    result = call_backend(wrapper, method, {"stream": True} if stream else None)
    expected = error_body("initialization failed")
    if stream:
        frames = asyncio.run(collect(result))
        assert json.loads(frames[0].removeprefix("data:")) == expected
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == 500
        assert result["content"] == expected


@pytest.mark.parametrize("spelling", ("true", "yes", 1))
def test_generate_follows_the_controller_stream_spelling(wrapper, spelling):
    async def chunks():
        yield "data: first\n\n"

    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = chunks()
    frames = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": spelling})))
    assert frames == ["data: first\n\n"]


@pytest.mark.parametrize("method,attribute", (
    ("generate", "openai_serving_chat"), ("generate_embeddings", "openai_serving_embedding"),
    ("rerank", "openai_serving_score"), ("show_available_models", "_ensure_models"),
))
def test_serving_exceptions_use_the_engine_classifier(wrapper, method, attribute):
    class EngineFailure(Exception):
        pass

    error = EngineFailure("engine details")
    if method == "generate":
        target = wrapper.backend.openai_serving_chat.create_chat_completion
    elif method == "show_available_models":
        target = wrapper.backend._ensure_models.return_value.show_available_models
    else:
        target = getattr(wrapper.backend, attribute)
    target.side_effect = error
    wire = call_backend(wrapper, method)
    assert wire["status_code"] == 500
    if method == "show_available_models":
        # Model listing is not an inference call, so it keeps the neutral body
        # instead of borrowing vLLM's request vocabulary.
        assert wire["content"] == error_body("engine details", "EngineFailure")
    else:
        assert wire["content"] == error_body("engine details", "InternalServerError")


@pytest.mark.parametrize("method", ("generate", "generate_embeddings", "rerank"))
def test_engine_client_errors_keep_the_vllm_status(wrapper, method):
    error = ValueError("input is too long")
    if method == "generate":
        wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    elif method == "generate_embeddings":
        wrapper.backend.openai_serving_embedding.side_effect = error
    else:
        wrapper.backend.openai_serving_score.side_effect = error
    wire = call_backend(wrapper, method)
    assert wire["status_code"] == 400
    assert wire["content"] == error_body("input is too long", "BadRequestError", 400)


@pytest.mark.parametrize("method", ("generate", "generate_embeddings", "rerank"))
def test_plain_value_error_follows_the_engine_classifier(wrapper, method):
    # vLLM's own route maps a ValueError to 400; only a pydantic ValidationError
    # keeps our body, because vLLM's table does not classify pydantic errors.
    wire = call_backend(wrapper, method, {"invalid_model": True})
    assert wire["status_code"] == 400
    assert wire["content"] == error_body("request model rejected", "BadRequestError", 400)


@pytest.mark.parametrize("method,request_type,target_name", [
    ("generate", "ChatCompletionRequest", "openai_serving_chat"),
    ("generate_embeddings", "EmbeddingCompletionRequest", "openai_serving_embedding"),
    ("rerank", "RerankRequest", "openai_serving_score"),
])
@pytest.mark.parametrize("stream", (False, True))
def test_request_schema_failure_is_a_client_error(wrapper, method, request_type, target_name, stream):
    if stream and method != "generate":
        pytest.skip("Only generation supports streaming")
    class RequestModel(BaseModel):
        messages: list[str]
    wrapper.namespace[request_type] = RequestModel
    result = call_backend(wrapper, method, {"messages": [17], "stream": stream})
    if stream:
        frames = asyncio.run(collect(result))
        body = json.loads(frames[0].removeprefix("data:"))
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == 400
        body = result["content"]
    # A schema failure is a client error, but the body is ours: the engine's
    # native validation handler is not part of the boundary any more.
    assert body["error"]["type"] == "ValidationError"
    assert body["error"]["code"] == 400
    target = getattr(wrapper.backend, target_name)
    if method == "generate":
        target = target.create_chat_completion
    target.assert_not_called()


@pytest.mark.parametrize("method", ("generate", "generate_embeddings", "rerank", "show_available_models"))
def test_wire_serialization_failure_is_contained(wrapper, method):
    class BrokenModel:
        def model_dump(self, **kwargs):
            raise RuntimeError("serialization failed")
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
    assert wire["content"] == error_body("serialization failed")


@pytest.mark.parametrize("as_model", (False, True))
def test_wire_nonfinite_numbers_are_sanitized_before_ray(wrapper, as_model):
    value = float("nan")
    class NonfiniteModel:
        def model_dump(self, **kwargs):
            return {"value": value}
    result = NonfiniteModel() if as_model else {"value": value}
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = result

    wire = call_backend(wrapper, "generate")

    assert wire["status_code"] == 200
    assert wire["content"] == {"value": None}
    # The Controller's real encoder must accept the sanitized value.
    JSONResponse(content=wire["content"], status_code=wire["status_code"])


@pytest.mark.parametrize("method,initializer", (
    ("generate", "_ensure_chat"), ("show_available_models", "_ensure_models"),
))
def test_cancellation_still_propagates(wrapper, method, initializer):
    getattr(wrapper.backend, initializer).side_effect = asyncio.CancelledError()
    with pytest.raises(asyncio.CancelledError):
        call_backend(wrapper, method)


def test_stream_failures_use_the_engine_classifier(wrapper):
    error = ValueError("before first chunk")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    chunks = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True})))
    assert json.loads(chunks[0].removeprefix("data:")) == error_body("before first chunk", "BadRequestError", 400)
    assert chunks[-1] == "data: [DONE]\n\n"

    async def broken_stream():
        yield "data: first\n\n"
        raise RuntimeError("midstream")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = None
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = broken_stream()
    chunks = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True})))
    assert chunks[0] == "data: first\n\n"
    # A mid-stream failure is already past the HTTP status, but the body still
    # carries the engine's classification.
    assert json.loads(chunks[1].removeprefix("data:")) == error_body("midstream", "InternalServerError")
    assert chunks[2] == "data: [DONE]\n\n"


class Handle:
    def __init__(self, result=None):
        self.calls = []
        self.result = result if result is not None else serve_response.wire_response({"accepted": True})
        self.generate = self.generate_embeddings = self.rerank = self

    def options(self, **kwargs):
        self.stream = kwargs["stream"]
        return self

    def remote(self, payload=None):
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


@pytest.mark.parametrize("payload,streaming", [
    ({"stream": True}, True),
    ({"stream": 1}, True),
    ({"stream": "yes"}, True),
    ({"stream": "false"}, False),
    ({"stream": 0}, False),
    ({"stream": None}, False),
    ({}, False),
])
def test_controller_dispatches_on_engine_boolean_semantics(wrapper, payload, streaming):
    # The Controller must make the same stream decision the engine's request
    # model would make for the same spelling.
    async def stream_body():
        yield "data: first\n\n"
    handle = Handle(stream_body() if streaming else None)
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/chat/completions", json=payload)
    assert response.status_code == 200, response.text
    assert handle.calls == [payload]
    assert handle.stream is streaming


@pytest.mark.parametrize("body,message", [
    (b"[1, 2]", "Request body must be a JSON object"),
    (b'"text"', "Request body must be a JSON object"),
    (b"{not json", "Request body must be valid JSON"),
])
def test_controller_rejects_non_object_bodies_before_dispatch(wrapper, body, message):
    handle = Handle()
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/chat/completions", content=body, headers={"content-type": "application/json"})
    assert response.status_code == 400
    assert response.json() == {"error": {"message": message, "type": "BadRequestError", "code": 400, "param": None}}
    assert handle.calls == []


@pytest.mark.parametrize("payload", [{"stream": 2}, {"stream": "maybe"}])
def test_controller_rejects_uninterpretable_stream_values(wrapper, payload):
    handle = Handle()
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/chat/completions", json=payload)
    assert response.status_code == 400
    assert response.json()["error"]["message"] == "stream must be a boolean"
    assert handle.calls == []


def test_controller_rejects_malformed_wire_response(wrapper):
    handle = Handle({"content": {"ok": True}, "status_code": 200})
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/embeddings", json={})
    assert response.status_code == 500
    assert response.json()["error"]["type"] == "InternalServerError"


def test_controller_non_stream_rpc_failure_is_a_neutral_error(wrapper):
    class FailingHandle(Handle):
        def remote(self, payload=None):
            raise RuntimeError("RPC unavailable")
    with client_for(wrapper, FailingHandle()) as client:
        response = client.post("/v1/embeddings", json={})
    assert response.status_code == 500
    assert response.json() == error_body("RPC unavailable")


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


@pytest.mark.parametrize("stage", ("options", "remote", "first_chunk"))
def test_stream_rpc_initialization_failure_names_the_exception(wrapper, stage):
    class FailingHandle(Handle):
        def options(self, **kwargs):
            if stage == "options":
                raise RuntimeError("RPC unavailable")
            return super().options(**kwargs)

        def remote(self, payload=None):
            if stage == "remote":
                raise RuntimeError("RPC unavailable")
            return super().remote(payload)

    async def failed_stream():
        raise RuntimeError("RPC unavailable")
        yield
    with client_for(wrapper, FailingHandle(failed_stream())) as client:
        response = client.post("/v1/chat/completions", json={"stream": True})
    assert response.status_code == 500
    assert response.json() == error_body("RPC unavailable")


def test_returned_error_value_keeps_its_status_and_body(wrapper):
    error = ErrorResponse(error=ErrorInfo(message="unknown model", type="NotFoundError", code=404, param="model"))
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = error
    wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == 404
    assert wire["content"] == error.model_dump(mode="json")
    chunks = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True})))
    assert json.loads(chunks[0].removeprefix("data:")) == error.model_dump(mode="json")
    assert chunks[-1] == "data: [DONE]\n\n"


@pytest.mark.parametrize("status", (400, 500, 503))
def test_returned_error_values_are_preserved_in_both_modes(wrapper, status):
    result = ErrorResponse(error=ErrorInfo(message="native error details", type="NativeError", code=status, param="model"))
    target = wrapper.backend.openai_serving_chat.create_chat_completion
    target.return_value = result
    wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == status
    assert wire["content"] == result.model_dump(mode="json")

    async def chunks():
        yield f"data: {json.dumps(result.model_dump())}\n\n"
        yield "data: [DONE]\n\n"
    target.return_value = chunks()
    expected = [f"data: {json.dumps(result.model_dump())}\n\n", "data: [DONE]\n\n"]
    assert asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True}))) == expected


@pytest.mark.parametrize("midstream", (False, True))
def test_nonfinite_stream_values_are_sanitized(wrapper, midstream):
    value = float("nan")
    async def chunks():
        if midstream:
            yield 'data: {"choices":[]}\n\n'
        yield {"value": value}
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = chunks()
    frames = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True})))
    assert frames[1 if midstream else 0] == 'data: {"value": null}\n\n'


@pytest.mark.parametrize("midstream", (False, True))
def test_stream_serialization_failure_is_a_server_error(wrapper, midstream):
    async def chunks():
        if midstream:
            yield 'data: {"choices":[]}\n\n'
        yield {"value": object()}
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = chunks()
    frames = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True})))
    body = json.loads(frames[1 if midstream else 0].removeprefix("data:"))
    assert body["error"]["code"] == 500
    assert body["error"]["type"] == "TypeError"
    assert frames.count("data: [DONE]\n\n") == 1


def test_raised_errors_are_logged_with_a_traceback(wrapper, caplog):
    error = RuntimeError("unexpected failure")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    with caplog.at_level(logging.ERROR):
        wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == 500
    assert any(record.exc_info and record.exc_info[1] is error for record in caplog.records)


def test_http_exception_is_rebuilt_with_its_native_status(wrapper):
    error = HTTPException(status_code=HTTPStatus.SERVICE_UNAVAILABLE, detail="native HTTP detail")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    wire = call_backend(wrapper, "generate")
    # A bare HTTPException carries a status but not vLLM's error type, so the
    # factory is called with the status phrase as the type.
    assert wire["status_code"] == 503
    assert wire["content"] == error_body("native HTTP detail", "Service Unavailable", 503)


def test_engine_classifier_failure_falls_back_to_the_neutral_body(wrapper):
    def broken_factory(*_args, **_kwargs):
        raise RuntimeError("factory exploded")

    wrapper.namespace["create_error_response"] = broken_factory
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = ValueError("engine failure")
    wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == 500
    assert wire["content"] == error_body("factory exploded", "RuntimeError")
