"""Behavioral tests for the engine-neutral Serve error boundary."""

import asyncio
import json
import subprocess
import sys
from types import SimpleNamespace
from unittest.mock import Mock

import pytest
from fastapi import FastAPI, Request
from fastapi.testclient import TestClient

from serve._utils import serve_errors as errors


def run(awaitable):
    return asyncio.run(awaitable)


async def collect(chunks):
    return [chunk async for chunk in chunks]


def request(body):
    async def receive():
        return {"type": "http.request", "body": body}

    return Request({"type": "http", "headers": []}, receive)


@pytest.mark.parametrize("body", [b"", b"not json", b'{"input":}', b"\xff", b"null", b"[]", b'"x"', b"1", b"true", b"1" * 5000, b'{"input":' + b"1" * 5000 + b"}"])
def test_bad_json_never_reaches_business_handler(body):
    app = FastAPI()
    app.add_exception_handler(errors.RequestInputError, errors.input_error_handler)
    business = Mock()

    @app.post("/")
    async def handle(raw: Request):
        payload = await errors.read_json_object(raw)
        business(payload)
        return payload

    response = TestClient(app).post("/", content=body)
    assert response.status_code == 400
    assert response.json()["error"]["type"] == "BadRequestError"
    assert response.json()["error"]["code"] == 400
    business.assert_not_called()


@pytest.mark.parametrize("payload", [{}, {"input": "x"}])
def test_object_body_is_left_for_business_validation(payload):
    assert run(errors.read_json_object(request(json.dumps(payload).encode()))) == payload


@pytest.mark.parametrize("payload,expected", [({}, False), ({"stream": False}, False), ({"stream": True}, True)])
def test_stream_bool_contract(payload, expected):
    assert errors.is_streaming(payload) is expected


@pytest.mark.parametrize("payload", [None, [], "x", {"stream": None}, {"stream": "true"}, {"stream": "false"}, {"stream": 0}, {"stream": 1}, {"stream": []}])
def test_stream_rejects_pydantic_coercions(payload):
    with pytest.raises(errors.RequestInputError):
        errors.is_streaming(payload)


def test_controller_error_preserves_error_envelope():
    response = errors.error_response("Failed to initialize stream", 500, "InternalServerError")
    assert response.status_code == 500
    assert json.loads(response.body) == {"error": {
        "message": "Failed to initialize stream", "type": "InternalServerError", "code": 500, "param": None,
    }}


def test_backend_error_is_plain_serializable_content():
    assert errors.backend_error(RuntimeError("private"), "generate") == {
        "error": {
            "message": "Internal server error",
            "type": "InternalServerError",
            "code": 500,
            "param": None,
        }
    }


@pytest.mark.parametrize("exc", [RuntimeError("private engine details"), ValueError("private engine details"), TypeError("private engine details")])
def test_unexpected_errors_use_neutral_server_response(exc, caplog):
    response = errors.backend_error(exc, "generate")
    assert response == {"error": {
        "message": "Internal server error", "type": "InternalServerError", "code": 500, "param": None,
    }}
    assert "private engine details" in caplog.text
    assert "private engine details" not in json.dumps(response)


def test_known_input_error_keeps_bad_request_status():
    response = errors.backend_error(errors.RequestInputError("invalid stream"), "generate")
    assert response["error"]["code"] == 400
    assert response["error"]["message"] == "invalid stream"


def test_controller_import_does_not_import_engine_runtimes():
    result = subprocess.run([sys.executable, "-c", """
import sys
class Block:
    def find_spec(self, fullname, path=None, target=None):
        assert fullname.split('.')[0] not in ('vllm', 'torch', 'sglang', 'llama_cpp'), fullname
sys.meta_path.insert(0, Block())
import serve._utils.serve_errors
from serve._utils.serve_errors import backend_error
assert backend_error(RuntimeError("engine failure"), "test")["error"]["code"] == 500
"""], capture_output=True, text=True)
    assert result.returncode == 0, result.stderr


def error_payload():
    return {"error": {"message": "too long", "type": "BadRequestError", "code": 400, "param": "input_tokens"}}


@pytest.mark.parametrize("as_model", [False, True])
def test_error_stream_preserves_all_upstream_fields(as_model):
    payload = error_payload()
    error = SimpleNamespace(model_dump=lambda **kwargs: payload) if as_model else payload
    frames = run(collect(errors.error_stream(error)))
    assert json.loads(frames[0].removeprefix("data:")) == payload
    assert frames[1] == "data: [DONE]\n\n"


def test_error_stream_accepts_native_http_response():
    response = errors.error_response("engine failed", 500, "internal_server_error")
    frames = run(collect(errors.error_stream(response)))
    assert json.loads(frames[0].removeprefix("data:")) == json.loads(response.body)
    assert frames[1] == "data: [DONE]\n\n"


@pytest.mark.parametrize("value", [float("nan"), float("inf"), float("-inf")])
@pytest.mark.parametrize("as_model", [False, True])
def test_error_stream_rejects_non_finite_json(value, as_model):
    payload = {"error": {"code": value}}
    error = SimpleNamespace(model_dump=lambda **kwargs: payload) if as_model else payload
    with pytest.raises(ValueError):
        run(collect(errors.error_stream(error)))


@pytest.mark.parametrize("code,expected", [(400, 400), (503, 503), ("503", 503), (True, 400), (200, 400), (600, 400), ("oops", 400), ("200", 400), (None, 400)])
def test_stream_status_checks_code_range(code, expected):
    assert errors.stream_error_status({"code": code}) == expected


def test_non_mapping_error_status_defaults_to_bad_request():
    assert errors.stream_error_status(None) == 400


@pytest.mark.parametrize("code", [None, "engine_failure"])
@pytest.mark.parametrize("error_type", ["internal_server_error", "InternalServerError"])
def test_symbolic_native_error_codes_preserve_server_status(code, error_type):
    assert errors.stream_error_status({"code": code, "type": error_type}) == 500


def test_llama_context_error_keeps_symbolic_code_and_bad_request_status():
    payload = {"error": {"type": "invalid_request_error", "code": "context_length_exceeded"}}
    response = errors.stream_error_response("data: " + json.dumps(payload))
    assert response.status_code == 400
    assert json.loads(response.body) == payload


def test_flat_sglang_error_first_frame_preserves_native_payload():
    payload = {"object": "error", "message": "unavailable", "code": 503, "type": "503", "param": None}
    response = errors.stream_error_response("data: " + json.dumps(payload))
    assert response.status_code == 503
    assert json.loads(response.body) == payload


@pytest.mark.parametrize("as_bytes", [False, True])
def test_error_first_frame_is_http_error_with_envelope(as_bytes):
    payload = error_payload()
    frame = "data: " + json.dumps(payload) + "\n\n"
    response = errors.stream_error_response(frame.encode() if as_bytes else frame)
    assert response.status_code == 400
    assert json.loads(response.body) == payload


@pytest.mark.parametrize("frame", [None, 1, "not sse", "data: [DONE]\n\n", "data: invalid", "data: []", 'data: {"choices":[]}', b"\xff"])
def test_other_first_frames_are_not_rewritten(frame):
    assert errors.stream_error_response(frame) is None
