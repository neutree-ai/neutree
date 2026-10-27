"""Exercise both shipped wrappers without importing the GPU/Ray runtime."""

import ast
import asyncio
import copy
import json
import logging
from pathlib import Path
from http import HTTPStatus
from types import SimpleNamespace
from typing import Any, Dict, Optional
from unittest.mock import AsyncMock, Mock

import pytest
from fastapi import FastAPI, HTTPException, Request
from fastapi.exceptions import RequestValidationError
from fastapi.testclient import TestClient
from pydantic import BaseModel, ValidationError
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
    native_code = 400


class NativeNotFoundError(NativeClientError):
    native_code = 404


class NativeUnprocessableError(NativeClientError):
    native_code = 422
    parameter = "messages"


class GenerationError(Exception):
    status_code = HTTPStatus.INTERNAL_SERVER_ERROR


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


def upstream_error(exc, err_type="BadRequestError", status_code=HTTPStatus.BAD_REQUEST):
    if isinstance(exc, str):
        return ErrorResponse(error=ErrorInfo(message=exc, type=err_type, code=status_code))
    code = getattr(exc, "http_status", getattr(exc, "status_code", getattr(exc, "native_code", None)))
    if code is None:
        if isinstance(exc, (ValueError, TypeError, OverflowError, TemplateError)):
            code = 400
        elif isinstance(exc, NotImplementedError):
            code = 501
        else:
            code = 500
    return ErrorResponse(error=ErrorInfo(
        message=str(exc), type=HTTPStatus(code).phrase, code=code, param=getattr(exc, "parameter", None),
    ))



@pytest.fixture(params=("v0_24_0", "v0_31_0"))
def wrapper(request):
    factory = Mock(side_effect=upstream_error)
    validation_handler = AsyncMock()
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
            "_result_to_response", "_backend_error", "_parse_request",
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
        HTTPException=HTTPException, HTTPStatus=HTTPStatus, ValidationError=ValidationError,
        RequestValidationError=RequestValidationError, SimpleNamespace=SimpleNamespace,
        validation_exception_handler=validation_handler,
        JSONResponse=JSONResponse, StreamingResponse=StreamingResponse, DeploymentHandle=object,
        ErrorResponse=ErrorResponse, ErrorInfo=ErrorInfo, ChatCompletionRequest=request_model,
        EmbeddingCompletionRequest=request_model, RerankRequest=request_model,
        json=json, logging=logging, GenerationError=GenerationError, create_error_response=factory,
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
    return SimpleNamespace(namespace=namespace, backend=backend, validation_error=validation_error, factory=factory, validation_handler=validation_handler, version=request.param)


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
    assert wire["content"]["error"]["message"] == "initialization failed"
    wrapper.factory.assert_called_once_with(error)


@pytest.mark.parametrize("method,attribute", (
    ("generate", "openai_serving_chat"), ("generate_embeddings", "openai_serving_embedding"),
    ("rerank", "openai_serving_score"), ("show_available_models", "_ensure_models"),
))
def test_serving_exception_preserves_native_error(wrapper, method, attribute):
    error = wrapper.validation_error("input too long")
    if method == "generate":
        target = wrapper.backend.openai_serving_chat.create_chat_completion
    elif method == "show_available_models":
        target = wrapper.backend._ensure_models.return_value.show_available_models
    else:
        target = getattr(wrapper.backend, attribute)
    target.side_effect = error
    wire = call_backend(wrapper, method)
    assert wire["status_code"] == 400
    assert wire["content"] == upstream_error(error).model_dump(mode="json")
    wrapper.factory.assert_called_once_with(error)


@pytest.mark.parametrize("method", ("generate", "generate_embeddings", "rerank"))
def test_request_model_exception_is_contained(wrapper, method):
    wire = call_backend(wrapper, method, {"invalid_model": True})
    assert wire["status_code"] == 400
    assert wire["content"]["error"]["message"] == "request model rejected"


@pytest.mark.parametrize("method,request_type,target_name", [
    ("generate", "ChatCompletionRequest", "openai_serving_chat"),
    ("generate_embeddings", "EmbeddingCompletionRequest", "openai_serving_embedding"),
    ("rerank", "RerankRequest", "openai_serving_score"),
])
@pytest.mark.parametrize("stream", (False, True))
def test_constructor_validation_uses_native_http_handler(wrapper, method, request_type, target_name, stream):
    if stream and method != "generate":
        pytest.skip("Only generation supports streaming")
    class RequestModel(BaseModel):
        messages: list[str]
    wrapper.namespace[request_type] = RequestModel
    payload = {"messages": [17], "stream": stream}
    expected = {"error": {
        "message": "native formatted validation details", "type": "Bad Request", "code": 400, "param": "body.messages.0",
    }}
    native_response = JSONResponse(content=expected, status_code=400)
    wrapper.validation_handler.return_value = native_response
    result = call_backend(wrapper, method, payload)
    if stream:
        async def collect():
            return [frame async for frame in result]
        frames = asyncio.run(collect())
        assert json.loads(frames[0].removeprefix("data:")) == expected
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == 400
        assert result["content"] == expected
    wrapper.validation_handler.assert_awaited_once()
    raw_request, error = wrapper.validation_handler.call_args.args
    assert isinstance(raw_request, Request)
    assert raw_request.app.state.args.log_error_stack is False
    assert isinstance(error, RequestValidationError)
    assert error.body is payload
    assert error.errors()[0]["loc"] == ("body", "messages", 0)
    assert "url" not in error.errors()[0]
    wrapper.factory.assert_not_called()
    target = getattr(wrapper.backend, target_name)
    if method == "generate":
        target = target.create_chat_completion
    target.assert_not_called()


def test_engine_validation_error_is_not_reclassified_as_http_schema_error(wrapper):
    class EngineResult(BaseModel):
        count: int
    with pytest.raises(ValidationError) as captured:
        EngineResult(count="invalid")
    error = captured.value
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    wire = call_backend(wrapper, "generate")
    assert wire["content"] == upstream_error(error).model_dump(mode="json")
    wrapper.factory.assert_called_once_with(error)
    wrapper.validation_handler.assert_not_called()


@pytest.mark.parametrize("payload", (None, [], "body", 1, True))
def test_non_mapping_backend_payload_is_contained(wrapper, payload):
    wire = asyncio.run(wrapper.backend.generate(payload))
    assert wire["status_code"] == 400
    assert "error" in wire["content"]
    wrapper.factory.assert_not_called()


@pytest.mark.parametrize("payload", ({"stream": "true"}, {"stream": "false"}, {"stream": 1}, {"stream": None}))
def test_invalid_stream_never_reaches_serving(wrapper, payload):
    wire = call_backend(wrapper, "generate", payload)
    assert wire["status_code"] == 400
    wrapper.backend.openai_serving_chat.create_chat_completion.assert_not_called()
    wrapper.factory.assert_not_called()


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
    wrapper.factory.assert_not_called()


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
    assert json.loads(chunks[0].removeprefix("data:"))["error"]["code"] == 400
    assert chunks[-1] == "data: [DONE]\n\n"

    async def broken_stream():
        yield "data: first\n\n"
        raise ValueError("midstream")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = None
    wrapper.backend.openai_serving_chat.create_chat_completion.return_value = broken_stream()
    chunks = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True})))
    assert chunks[0] == "data: first\n\n"
    assert json.loads(chunks[1].removeprefix("data:"))["error"]["message"] == "midstream"
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


@pytest.mark.parametrize("method", ("generate", "generate_embeddings", "rerank", "show_available_models"))
@pytest.mark.parametrize("error,status,kind,param", [
    pytest.param(ValueError("invalid argument"), 400, "BadRequestError", "argument", id="validation"),
    pytest.param(NativeNotFoundError("missing model"), 404, "NotFoundError", "model", id="not-found"),
    pytest.param(NativeUnprocessableError("invalid messages"), 422, "UnprocessableEntityError", "messages", id="unprocessable"),
    pytest.param(NotImplementedError("unsupported operation"), 501, "NotImplementedError", None, id="not-implemented"),
    pytest.param(QueueOverflowError(), 503, "Service Unavailable", None, id="queue"),
    pytest.param(GenerationError("generation details"), 500, "InternalServerError", None, id="generation"),
    pytest.param(RuntimeError("engine details"), 500, "InternalServerError", None, id="server"),
])
def test_backend_delegates_error_fields_to_native_factory(wrapper, method, error, status, kind, param):
    if wrapper.version == "v0_24_0" and isinstance(error, (NativeUnprocessableError, QueueOverflowError)):
        pytest.skip("Native 422 and admission-control exception types were introduced in v0.31")
    expected = ErrorResponse(error=ErrorInfo(message=str(error), type=kind, code=status, param=param))
    wrapper.factory.side_effect = None
    wrapper.factory.return_value = expected
    if method == "generate":
        target = wrapper.backend.openai_serving_chat.create_chat_completion
    elif method == "show_available_models":
        target = wrapper.backend._ensure_models.return_value.show_available_models
    else:
        target = wrapper.backend.openai_serving_embedding if method == "generate_embeddings" else wrapper.backend.openai_serving_score
    target.side_effect = error
    wire = call_backend(wrapper, method)
    assert wire["status_code"] == status
    assert wire["content"] == expected.model_dump(mode="json")
    wrapper.factory.assert_called_once_with(error)


@pytest.mark.parametrize("error", (ValueError("invalid input"), GenerationError("generation details"), RuntimeError("engine details"), QueueOverflowError()))
@pytest.mark.parametrize("stage", ("call", "first", "midstream"))
def test_stream_exceptions_use_native_factory(wrapper, error, stage):
    if wrapper.version == "v0_24_0" and isinstance(error, QueueOverflowError):
        pytest.skip("Native admission-control exception types were introduced in v0.31")
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
    assert json.loads(frames[-2].removeprefix("data:")) == upstream_error(error).model_dump(mode="json")
    assert frames.count("data: [DONE]\n\n") == 1
    if stage == "midstream":
        assert frames[0] == 'data: {"choices":[]}\n\n'
    wrapper.factory.assert_called_once_with(error)


@pytest.mark.parametrize("status", (429, 503))
@pytest.mark.parametrize("stream", (False, True))
def test_http_exception_uses_native_http_error_fields(wrapper, status, stream):
    error = HTTPException(status_code=status, detail="native HTTP detail")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    result = call_backend(wrapper, "generate", {"stream": stream})
    expected = {"error": {
        "message": error.detail, "type": HTTPStatus(status).phrase, "code": status, "param": None,
    }}
    if stream:
        async def collect():
            return [frame async for frame in result]
        frames = asyncio.run(collect())
        assert json.loads(frames[0].removeprefix("data:")) == expected
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == status
        assert result["content"] == expected
    wrapper.factory.assert_called_once_with(error.detail, err_type=HTTPStatus(status).phrase, status_code=HTTPStatus(status))


@pytest.mark.parametrize("stream", (False, True))
def test_native_factory_failure_uses_neutree_fallback(wrapper, stream):
    error = RuntimeError("native factory failed")
    wrapper.factory.side_effect = error
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = ValueError("invalid input")
    result = call_backend(wrapper, "generate", {"stream": stream})
    if stream:
        async def collect():
            return [frame async for frame in result]
        frames = asyncio.run(collect())
        content = json.loads(frames[0].removeprefix("data:"))
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == 500
        content = result["content"]
    assert content == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}


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


@pytest.mark.parametrize("error,has_stack", [(GenerationError("known generation failure"), False), (RuntimeError("unexpected failure"), True)])
def test_generation_error_logging_matches_native_server(wrapper, caplog, error, has_stack):
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    with caplog.at_level(logging.ERROR):
        wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == 500
    assert any(record.exc_info and record.exc_info[1] is error for record in caplog.records) is has_stack
    wrapper.factory.assert_called_once_with(error)
