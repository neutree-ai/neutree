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
from starlette.responses import JSONResponse

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


@pytest.mark.parametrize("payload,expected", [
    ({}, False),
    ({"stream": False}, False),
    ({"stream": True}, True),
    ({"stream": None}, False),
    ({"stream": 0}, False),
    ({"stream": 1}, True),
    ({"stream": 0.0}, False),
    ({"stream": 1.0}, True),
    ({"stream": "false"}, False),
    ({"stream": "FALSE"}, False),
    ({"stream": "no"}, False),
    ({"stream": "off"}, False),
    ({"stream": "0"}, False),
    ({"stream": "true"}, True),
    ({"stream": "TRUE"}, True),
    ({"stream": "yes"}, True),
    ({"stream": "on"}, True),
    ({"stream": "1"}, True),
])
def test_stream_lax_boolean_contract(payload, expected):
    assert errors.is_streaming(payload) is expected


@pytest.mark.parametrize("payload", [
    None, [], "x", {"stream": "maybe"}, {"stream": 2}, {"stream": 0.5}, {"stream": []}, {"stream": {}},
])
def test_stream_rejects_non_boolean(payload):
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
def test_error_stream_replaces_non_finite_json(value, as_model):
    payload = {"error": {"code": value}}
    error = SimpleNamespace(model_dump=lambda **kwargs: payload) if as_model else payload
    frames = run(collect(errors.error_stream(error)))
    assert json.loads(frames[0].removeprefix("data:")) == {"error": {"code": None}}
    assert frames[1] == "data: [DONE]\n\n"


@pytest.mark.parametrize("value", [float("nan"), float("inf"), float("-inf")])
def test_json_safe_replaces_non_finite_floats_recursively(value):
    assert errors.json_safe({"value": value, "items": [value, {"value": value}], "ok": 1.5}) == {
        "value": None, "items": [None, {"value": None}], "ok": 1.5,
    }


def test_json_safe_strict_accepts_json_values():
    assert errors.json_safe(
        {"value": float("inf"), "items": [1, True, None, "x"]}, strict=True,
    ) == {"value": None, "items": [1, True, None, "x"]}


def test_json_safe_strict_rejects_unserializable_objects():
    with pytest.raises(TypeError):
        errors.json_safe({"value": object()}, strict=True)
    with pytest.raises(TypeError):
        errors.json_safe({1: "value"}, strict=True)


@pytest.mark.parametrize("code,expected", [
    (400, 400), (503, 503), ("503", 503), (True, 500), (200, 500), (600, 500), ("oops", 500), ("200", 500), (None, 500),
])
def test_stream_status_checks_code_range(code, expected):
    assert errors.stream_error_status({"code": code}) == expected


def test_non_mapping_error_status_is_server_error():
    assert errors.stream_error_status(None) == 500


@pytest.mark.parametrize("code,expected", [
    ("context_length_exceeded", 400), ("model_not_found", 400), ("rate_limit_exceeded", 429),
])
def test_symbolic_client_error_codes_map_to_client_status(code, expected):
    assert errors.stream_error_status({"code": code}) == expected


@pytest.mark.parametrize("error_type,expected", [
    ("BadRequestError", 400), ("NotFoundError", 404), ("UnprocessableEntityError", 422),
    ("RateLimitError", 429), ("invalid_request_error", 400), ("not_found_error", 404),
    ("unknown_engine_error", 500),
])
def test_client_error_types_map_to_client_status(error_type, expected):
    assert errors.stream_error_status({"type": error_type}) == expected


@pytest.mark.parametrize("error_type", [["BadRequestError"], {"BadRequestError": 400}, 400, None])
def test_non_string_error_type_is_server_error(error_type):
    assert errors.stream_error_status({"type": error_type}) == 500


@pytest.mark.parametrize("code", [None, "engine_failure"])
@pytest.mark.parametrize("error_type", ["internal_server_error", "InternalServerError"])
def test_symbolic_native_error_codes_preserve_server_status(code, error_type):
    assert errors.stream_error_status({"code": code, "type": error_type}) == 500


@pytest.mark.parametrize("payload", [
    {"error": {
        "message": "This model's maximum context length is 16 tokens.",
        "type": "invalid_request_error", "param": "messages", "code": "context_length_exceeded",
    }},
    {"error": {
        "message": "The model `missing` does not exist",
        "type": "invalid_request_error", "param": None, "code": "model_not_found",
    }},
])
def test_llama_native_business_errors_keep_bad_request_status(payload):
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


def test_first_frame_wire_envelope_restores_explicit_status():
    wire = errors.wire_response({"detail": "busy"}, 503)
    response = errors.stream_error_response("data: " + json.dumps(wire))
    assert response.status_code == 503
    assert json.loads(response.body) == {"detail": "busy"}


@pytest.mark.parametrize("wire", [
    {errors.WIRE_RESPONSE_MARKER: True},
    {errors.WIRE_RESPONSE_MARKER: True, "content": {}, "status_code": "not-a-status"},
])
def test_first_frame_malformed_wire_envelope_fails_closed(wire):
    response = errors.stream_error_response("data: " + json.dumps(wire))
    assert response.status_code == 500
    assert json.loads(response.body)["error"]["type"] == "InternalServerError"


def test_wire_response_envelope_is_json_safe():
    wire = errors.wire_response({"value": float("inf")}, 503)
    assert wire == {
        errors.WIRE_RESPONSE_MARKER: True,
        "content": {"value": None},
        "status_code": 503,
    }


def test_wire_response_skips_already_sanitized_content():
    content = {"value": float("inf")}
    wire = errors.wire_response(content, 503, sanitized=True)
    assert wire["content"] is content


def test_wire_http_response_renders_envelope():
    response = errors.wire_http_response(errors.wire_response({"ok": True}, 201))
    assert response.status_code == 201
    assert json.loads(response.body) == {"ok": True}


@pytest.mark.parametrize("result", [
    {"content": {"ok": True}, "status_code": 200},
    {"__neutree_wire_response__": True},
    "raw",
    None,
])
def test_wire_http_response_fails_closed_without_envelope(result):
    response = errors.wire_http_response(result)
    assert response.status_code == 500
    assert json.loads(response.body)["error"]["type"] == "InternalServerError"


def stream_of(*frames):
    async def iterator():
        for frame in frames:
            yield frame
    return iterator()


async def collect_body(response):
    return "".join([chunk.decode() if isinstance(chunk, bytes) else chunk async for chunk in response.body_iterator])


def test_open_stream_replays_success_frames():
    async def scenario():
        response = await errors.open_stream(
            lambda: stream_of("data: first\n\n", "data: [DONE]\n\n"), message="Failed",
        )
        assert response.status_code == 200
        return await collect_body(response)
    assert run(scenario()) == "data: first\n\ndata: [DONE]\n\n"


def test_open_stream_returns_first_error_frame_as_http_error():
    frame = "data: " + json.dumps(error_payload()) + "\n\n"
    response = run(errors.open_stream(lambda: stream_of(frame), message="Failed"))
    assert response.status_code == 400
    assert json.loads(response.body) == error_payload()


def test_open_stream_empty_stream_has_empty_body():
    response = run(errors.open_stream(lambda: stream_of(), message="Failed"))
    assert response.status_code == 200
    assert run(collect_body(response)) == ""


def test_open_stream_start_failure_is_server_error():
    def start():
        raise RuntimeError("private details")

    response = run(errors.open_stream(start, message="Failed to initialize stream"))
    assert response.status_code == 500
    assert json.loads(response.body) == {"error": {
        "message": "Failed to initialize stream", "type": "InternalServerError", "code": 500, "param": None,
    }}
