"""Exercise shipped llama.cpp methods without native inference dependencies."""
import ast
import asyncio
import builtins
import copy
import json
from pathlib import Path
from threading import Event
from types import SimpleNamespace
from typing import Any, Dict
from unittest.mock import Mock, patch

import pytest
from fastapi import FastAPI, Request
from fastapi.testclient import TestClient

from serve._utils import serve_response


APP_PATH = Path(__file__).with_name("app.py")


def neutral_error(message, error_type="RuntimeError", code=500):
    return {"error": {"message": message, "type": error_type, "code": code, "param": None}}


@pytest.fixture
def wrapper():
    tree, body = ast.parse(APP_PATH.read_text()), []
    for node in tree.body:
        if isinstance(node, ast.ImportFrom) and node.module and (
            node.module.startswith("serve._utils.") and node.module != "serve._utils.runtime_env"
            or node.module == "serve.llama_cpp.v0_3_7.wire"
        ):
            body.append(copy.deepcopy(node))
        elif isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id == "app" for t in node.targets):
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
    ns = dict(
        Any=Any, Dict=Dict, Request=Request, FastAPI=FastAPI,
        DeploymentHandle=object, DeploymentResponseGenerator=object,
        LlamaProxy=SimpleNamespace(load_llama_from_model_settings=load),
        _CHAT_PARAMS={"messages", "model", "stream"},
        _COMPLETION_PARAMS={"prompt", "model", "stream"},
        _EMBEDDING_PARAMS={"input", "model"},
    )
    exec(compile(ast.fix_missing_locations(ast.Module(body=body, type_ignores=[])), str(APP_PATH), "exec"), ns)
    backend = object.__new__(ns["Backend"])
    backend.model_settings, backend.model_id = object(), "fixture"
    return SimpleNamespace(namespace=ns, backend=backend, llama=llama, load=load)


def call(wrapper, method="generate", payload=None):
    if method == "show_available_models":
        return asyncio.run(wrapper.backend.show_available_models())
    if payload is None:
        payload = {"input": "hi"} if method == "generate_embeddings" else {"messages": [{"role": "user", "content": "hi"}]}
    return asyncio.run(getattr(wrapper.backend, method)({"model": "fixture", **payload}))


async def collect(stream):
    return [chunk async for chunk in stream]


@pytest.mark.parametrize("method,payload", [("generate", {"messages": [{"role": "user", "content": "hi"}]}), ("generate", {"prompt": "hi"}), ("generate_embeddings", {"input": "hi"}), ("show_available_models", None)])
def test_success_is_plain_wire(wrapper, method, payload):
    result = call(wrapper, method, payload)
    assert result["status_code"] == 200
    json.dumps(result)


@pytest.mark.parametrize("method,payload", [("generate", {"messages": [{"role": "user", "content": "hi"}]}), ("generate_embeddings", {"input": "hi"})])
def test_model_load_error_is_contained(wrapper, method, payload):
    wrapper.load.side_effect = ValueError("Model path does not exist: fixture.gguf")
    result = call(wrapper, method, payload)
    assert result["status_code"] == 500
    assert result["content"] == neutral_error("Model path does not exist: fixture.gguf", "ValueError")


@pytest.mark.parametrize("attribute,payload", [
    ("create_chat_completion", {"messages": [{"role": "user", "content": "hi"}]}),
    ("create_completion", {"prompt": "hi"}),
])
def test_engine_errors_keep_message_and_class_name(wrapper, attribute, payload):
    message = "Requested tokens (17) exceed context window of 16"
    getattr(wrapper.llama, attribute).side_effect = ValueError(message)
    wire = call(wrapper, payload=payload)
    assert wire["status_code"] == 500
    assert wire["content"] == neutral_error(message, "ValueError")


def test_nonfinite_values_are_sanitized(wrapper):
    wrapper.llama.create_chat_completion.return_value = {"value": float("nan")}
    wire = call(wrapper, payload={"messages": [{"role": "user", "content": "hi"}]})
    assert wire["status_code"] == 200
    assert wire["content"] == {"value": None}
    json.dumps(wire, allow_nan=False)


def test_sync_iterator_stays_in_backend(wrapper):
    wrapper.llama.create_chat_completion.return_value = iter([{"choices": []}])
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [{"role": "user", "content": "hi"}], "stream": True})))
    assert json.loads(frames[0][6:]) == {"choices": []}
    assert frames.count("data: [DONE]\n\n") == 1


@pytest.mark.parametrize("before_first", [True, False])
def test_iteration_errors_are_neutral_and_terminate_once(wrapper, before_first):
    def chunks():
        if not before_first:
            yield {"choices": []}
        raise RuntimeError("iteration failed")
    wrapper.llama.create_chat_completion.return_value = chunks()
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [{"role": "user", "content": "hi"}], "stream": True})))
    assert json.loads(frames[-2].removeprefix("data:")) == neutral_error("iteration failed")
    assert frames[-1] == "data: [DONE]\n\n"
    assert frames.count("data: [DONE]\n\n") == 1
    assert (frames[0] == 'data: {"choices": []}\n\n') is (not before_first)


def test_stream_serialization_errors_stay_in_backend(wrapper):
    wrapper.llama.create_chat_completion.return_value = iter([{"value": object()}])
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [{"role": "user", "content": "hi"}], "stream": True})))
    body = json.loads(frames[0].removeprefix("data:"))
    assert body["error"]["type"] == "TypeError"
    assert body["error"]["code"] == 500
    assert frames.count("data: [DONE]\n\n") == 1


def test_stream_nonfinite_values_are_sanitized(wrapper):
    wrapper.llama.create_chat_completion.return_value = iter([{"value": float("nan")}])
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [{"role": "user", "content": "hi"}], "stream": True})))
    assert json.loads(frames[0][6:]) == {"value": None}
    assert frames.count("data: [DONE]\n\n") == 1


def test_stream_task_cancellation_propagates(wrapper):
    started, release, finished = Event(), Event(), Event()

    class Chunks:
        def __iter__(self):
            return self

        def __next__(self):
            started.set()
            try:
                assert release.wait(timeout=5)
                return {"choices": []}
            finally:
                finished.set()

    wrapper.llama.create_chat_completion.return_value = Chunks()

    async def scenario():
        stream = await wrapper.backend.generate({"messages": [{"role": "user", "content": "hi"}], "stream": True})
        task = asyncio.create_task(collect(stream))
        try:
            assert await asyncio.to_thread(started.wait, 5)
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await task
        finally:
            release.set()
            assert await asyncio.to_thread(finished.wait, 5)

    asyncio.run(scenario())


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
    frames = asyncio.run(collect(call(wrapper, payload={"messages": [{"role": "user", "content": "hi"}], "stream": True})))
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


@pytest.mark.parametrize("method,attribute,payload", [
    ("generate", "create_chat_completion", {"messages": [{"role": "user", "content": "hi"}]}),
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
