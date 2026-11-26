"""Exercise both shipped wrappers without importing the GPU/Ray runtime."""

import ast
import asyncio
import copy
import json
import logging
from http import HTTPStatus
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Optional
from unittest.mock import AsyncMock, Mock

import pytest
from fastapi import FastAPI, HTTPException, Request
from fastapi.testclient import TestClient
from pydantic import BaseModel, ValidationError

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


# Only the fields exercised by these wrapper tests; full schemas belong to the engine.
class ChatRequest(BaseModel):
    model: str
    messages: list[dict]
    stream: bool | None = False


class EmbeddingRequest(BaseModel):
    model: str
    input: str


class RerankRequest(BaseModel):
    model: str
    query: str
    documents: list[str]


PAYLOADS = {
    "generate": {"model": "fixture", "messages": [{"role": "user", "content": "hi"}]},
    "generate_embeddings": {"model": "fixture", "input": "hi"},
    "rerank": {"model": "fixture", "query": "hi", "documents": ["hello"]},
}


@pytest.fixture(params=("v0_24_0", "v0_32_0"))
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
    factory = Mock(return_value=ErrorResponse(error=ErrorInfo(
        message="engine failure", type="InternalServerError", code=500,
    )))
    namespace = dict(
        Any=Any, Request=Request, FastAPI=FastAPI, DeploymentHandle=object,
        DeploymentResponseGenerator=object, ChatCompletionRequest=ChatRequest,
        EmbeddingCompletionRequest=EmbeddingRequest, RerankRequest=RerankRequest,
        logging=logging, HTTPStatus=HTTPStatus, HTTPException=HTTPException,
        ValidationError=ValidationError, create_error_response=factory,
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
    return SimpleNamespace(namespace=namespace, backend=backend, factory=factory)


def call_backend(wrapper, method, payload=None):
    args = () if method == "show_available_models" else ({**PAYLOADS[method], **(payload or {})},)
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


@pytest.mark.parametrize("method,initializer,stream", [
    ("generate", "_ensure_chat", False),
    ("generate", "_ensure_chat", True),
    ("generate_embeddings", "_ensure_embedding", False),
    ("rerank", "_ensure_score", False),
    ("show_available_models", "_ensure_models", False),
])
def test_raised_exceptions_become_neutral_errors(wrapper, method, initializer, stream):
    getattr(wrapper.backend, initializer).side_effect = RuntimeError("initialization failed")
    result = call_backend(wrapper, method, {"stream": True} if stream else None)
    expected = error_body("initialization failed")
    if stream:
        frames = asyncio.run(collect(result))
        assert json.loads(frames[0].removeprefix("data:")) == expected
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == 500
        assert result["content"] == expected
    wrapper.factory.assert_not_called()


@pytest.mark.parametrize("method,attribute", [
    ("generate", "openai_serving_chat"),
    ("generate_embeddings", "openai_serving_embedding"),
    ("rerank", "openai_serving_score"),
])
@pytest.mark.parametrize("error,status,error_type", [
    (ValueError("bad input"), 400, "BadRequestError"),
    (RuntimeError("engine failed"), 500, "InternalServerError"),
])
def test_inference_errors_delegate_the_original_exception(wrapper, method, attribute, error, status, error_type):
    native = ErrorResponse(error=ErrorInfo(message=str(error), type=error_type, code=status))
    wrapper.factory.return_value = native
    target = getattr(wrapper.backend, attribute)
    if method == "generate":
        target = target.create_chat_completion
    target.side_effect = error

    wire = call_backend(wrapper, method)

    wrapper.factory.assert_called_once_with(error)
    assert wire["status_code"] == status
    assert wire["content"] == native.model_dump(mode="json")


def test_model_listing_failure_stays_neutral(wrapper):
    wrapper.backend._ensure_models.return_value.show_available_models.side_effect = RuntimeError("model listing failed")
    wire = call_backend(wrapper, "show_available_models")
    assert wire["status_code"] == 500
    assert wire["content"] == error_body("model listing failed")
    wrapper.factory.assert_not_called()


@pytest.mark.parametrize("method,target_name,payload,stream", [
    ("generate", "openai_serving_chat", {"messages": [17]}, False),
    ("generate", "openai_serving_chat", {"messages": [17]}, True),
    ("generate_embeddings", "openai_serving_embedding", {"input": {}}, False),
    ("rerank", "openai_serving_score", {"documents": [17]}, False),
])
def test_request_schema_failure_is_a_client_error(wrapper, method, target_name, payload, stream):
    result = call_backend(wrapper, method, {**payload, "stream": stream})
    if stream:
        frames = asyncio.run(collect(result))
        body = json.loads(frames[0].removeprefix("data:"))
        assert frames[-1] == "data: [DONE]\n\n"
    else:
        assert result["status_code"] == 400
        body = result["content"]
    assert body["error"]["type"] == "ValidationError"
    assert body["error"]["code"] == 400
    target = getattr(wrapper.backend, target_name)
    if method == "generate":
        target = target.create_chat_completion
    target.assert_not_called()
    wrapper.factory.assert_not_called()


@pytest.mark.parametrize("method,initializer", (
    ("generate", "_ensure_chat"), ("show_available_models", "_ensure_models"),
))
def test_cancellation_still_propagates(wrapper, method, initializer):
    getattr(wrapper.backend, initializer).side_effect = asyncio.CancelledError()
    with pytest.raises(asyncio.CancelledError):
        call_backend(wrapper, method)


@pytest.mark.parametrize("midstream", [False, True])
def test_stream_failures_delegate_the_original_exception(wrapper, midstream):
    error = RuntimeError("engine interrupted")
    native = ErrorResponse(error=ErrorInfo(message=str(error), type="InternalServerError", code=500))
    wrapper.factory.return_value = native
    target = wrapper.backend.openai_serving_chat.create_chat_completion
    if midstream:
        async def broken_stream():
            yield 'data: {"choices":[]}\n\n'
            raise error
        target.return_value = broken_stream()
    else:
        target.side_effect = error

    frames = asyncio.run(collect(call_backend(wrapper, "generate", {"stream": True})))

    wrapper.factory.assert_called_once_with(error)
    assert json.loads(frames[-2].removeprefix("data:")) == native.model_dump(mode="json")
    assert frames.count("data: [DONE]\n\n") == 1
    if midstream:
        assert frames[0] == 'data: {"choices":[]}\n\n'


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
    ({"stream": False}, False),
])
def test_controller_dispatches_on_stream_truthiness(wrapper, payload, streaming):
    async def stream_body():
        yield "data: first\n\n"
    payload = {**PAYLOADS["generate"], **payload}
    handle = Handle(stream_body() if streaming else None)
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/chat/completions", json=payload)
    assert response.status_code == 200, response.text
    assert handle.calls == [payload]
    assert handle.stream is streaming


def test_controller_rejects_malformed_wire_response(wrapper):
    handle = Handle({"content": {"ok": True}, "status_code": 200})
    with client_for(wrapper, handle) as client:
        response = client.post("/v1/embeddings", json=PAYLOADS["generate_embeddings"])
    assert response.status_code == 500
    assert response.json()["error"]["type"] == "InternalServerError"


def test_controller_non_stream_rpc_failure_is_a_neutral_error(wrapper):
    class FailingHandle(Handle):
        def remote(self, payload=None):
            raise RuntimeError("RPC unavailable")
    with client_for(wrapper, FailingHandle()) as client:
        response = client.post("/v1/embeddings", json=PAYLOADS["generate_embeddings"])
    assert response.status_code == 500
    assert response.json() == error_body("RPC unavailable")


def test_controller_stream_error_preserves_http_status_and_envelope(wrapper):
    payload = {"error": {"message": "too long", "type": "BadRequestError", "code": 400, "param": "input_tokens"}}
    async def stream():
        yield "data: " + json.dumps(payload) + "\n\n"
        yield "data: [DONE]\n\n"
    with client_for(wrapper, Handle(stream())) as client:
        response = client.post("/v1/chat/completions", json={**PAYLOADS["generate"], "stream": True})
    assert response.status_code == 400
    assert response.json() == payload


def test_controller_replays_first_success_chunk(wrapper):
    async def stream():
        yield "data: first\n\n"
        yield "data: [DONE]\n\n"
    with client_for(wrapper, Handle(stream())) as client:
        response = client.post("/v1/chat/completions", json={**PAYLOADS["generate"], "stream": True})
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
        response = client.post("/v1/chat/completions", json={**PAYLOADS["generate"], "stream": True})
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


def test_raised_errors_are_logged_with_a_traceback(wrapper, caplog):
    error = RuntimeError("unexpected failure")
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error
    with caplog.at_level(logging.ERROR):
        wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == 500
    assert any(record.exc_info and record.exc_info[1] is error for record in caplog.records)


def test_http_exception_is_rebuilt_with_its_native_status(wrapper):
    error = HTTPException(status_code=HTTPStatus.SERVICE_UNAVAILABLE, detail="native HTTP detail")
    native = ErrorResponse(error=ErrorInfo(message=error.detail, type="Service Unavailable", code=503))
    wrapper.factory.return_value = native
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = error

    wire = call_backend(wrapper, "generate")

    wrapper.factory.assert_called_once_with(error.detail, err_type="Service Unavailable", status_code=HTTPStatus.SERVICE_UNAVAILABLE)
    assert wire["status_code"] == 503
    assert wire["content"] == native.model_dump(mode="json")


def test_engine_classifier_failure_falls_back_to_the_neutral_body(wrapper):
    def broken_factory(*_args, **_kwargs):
        raise RuntimeError("factory exploded")

    wrapper.namespace["create_error_response"] = broken_factory
    wrapper.backend.openai_serving_chat.create_chat_completion.side_effect = ValueError("engine failure")
    wire = call_backend(wrapper, "generate")
    assert wire["status_code"] == 500
    assert wire["content"] == error_body("factory exploded", "RuntimeError")
