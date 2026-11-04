"""Behavioral tests for the engine-neutral Serve error boundary."""

import asyncio
import json
import subprocess
import sys
from types import SimpleNamespace

import pytest

from serve._utils import serve_errors as errors


def run(awaitable):
    return asyncio.run(awaitable)


async def collect(chunks):
    return [chunk async for chunk in chunks]


def test_controller_error_preserves_error_envelope():
    response = errors.error_response("Failed to initialize stream", 500, "InternalServerError")
    assert response.status_code == 500
    assert json.loads(response.body) == {"error": {
        "message": "Failed to initialize stream", "type": "InternalServerError", "code": 500, "param": None,
    }}


def test_raised_errors_name_the_exception_and_keep_their_message(caplog):
    exc = RuntimeError("engine details")
    body = errors.backend_error(exc, "generate")
    assert body == {"error": {
        "message": "engine details", "type": "RuntimeError", "code": 500, "param": None,
    }}
    assert any(record.exc_info and record.exc_info[1] is exc for record in caplog.records)


def test_schema_errors_are_client_errors():
    from pydantic import BaseModel

    class Request(BaseModel):
        count: int

    with pytest.raises(Exception) as raised:
        Request(count="not a number")
    body = errors.backend_error(raised.value, "generate")
    assert body["error"]["code"] == 400
    assert body["error"]["type"] == "ValidationError"


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


@pytest.mark.parametrize("as_model", [False, True])
def test_error_stream_replaces_non_finite_json(as_model):
    payload = {"error": {"code": float("nan")}}
    error = SimpleNamespace(model_dump=lambda **kwargs: payload) if as_model else payload
    frames = run(collect(errors.error_stream(error)))
    assert json.loads(frames[0].removeprefix("data:")) == {"error": {"code": None}}
    assert frames[1] == "data: [DONE]\n\n"


def test_json_safe_replaces_non_finite_floats_recursively():
    value = float("nan")
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


@pytest.mark.parametrize("payload,expected", [
    # Only a numeric HTTP code is read from an engine body.
    ({"code": 400}, 400),
    ({"code": 503}, 503),
    ({"code": "503"}, 503),
    # Symbolic codes and type names are not mapped: body shapes differ per
    # engine and per version, so mapping them would reintroduce a version table.
    ({"code": "context_length_exceeded"}, 500),
    ({"type": "RateLimitError"}, 500),
    ({"code": 200}, 500),
    ({"code": True}, 500),
    ({"code": None}, 500),
    (None, 500),
])
def test_stream_error_status_reads_only_numeric_codes(payload, expected):
    assert errors.stream_error_status(payload) == expected


@pytest.mark.parametrize("content,expected", [
    ({"object": "error", "code": 503}, 503),
    ({"error": {"code": 400}}, 400),
    ({"error": {"code": "context_length_exceeded"}}, 500),
    ({"error": "not a body"}, 200),
    ({"choices": []}, 200),
])
def test_returned_error_status(content, expected):
    assert errors.returned_error_status(content) == expected


def test_to_wire_response_dumps_models_and_keeps_http_status():
    from pydantic import BaseModel
    from starlette.responses import JSONResponse

    class Model(BaseModel):
        value: str = "ok"

    assert errors.to_wire_response(Model()) == errors.wire_response({"value": "ok"})
    rendered = errors.to_wire_response(JSONResponse({"detail": "busy"}, status_code=503))
    assert rendered["status_code"] == 503
    assert rendered["content"] == {"detail": "busy"}


def test_to_wire_response_keeps_a_returned_error_body_and_status():
    body = error_payload()
    wire = errors.to_wire_response(body)
    assert wire["status_code"] == 400
    assert wire["content"] == body


def test_to_wire_response_sanitizes_non_finite_values():
    wire = errors.to_wire_response({"value": float("nan")})
    assert wire["status_code"] == 200
    assert wire["content"] == {"value": None}
    json.dumps(wire, allow_nan=False)


def test_to_wire_response_contains_serialization_failures():
    class Broken:
        def model_dump(self, **kwargs):
            raise RuntimeError("serializer details")

    wire = errors.to_wire_response(Broken())
    assert wire["status_code"] == 500
    assert wire["content"] == {"error": {
        "message": "serializer details", "type": "RuntimeError", "code": 500, "param": None,
    }}


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


@pytest.mark.parametrize("frame", ["data: [DONE]\n\n", "data: invalid", 'data: {"choices":[]}'])
def test_other_first_frames_are_not_rewritten(frame):
    assert errors.stream_error_response(frame) is None


def test_first_frame_wire_envelope_restores_explicit_status():
    wire = errors.wire_response({"detail": "busy"}, 503)
    response = errors.stream_error_response("data: " + json.dumps(wire))
    assert response.status_code == 503
    assert json.loads(response.body) == {"detail": "busy"}


def test_first_frame_malformed_wire_envelope_fails_closed():
    wire = {errors.WIRE_RESPONSE_MARKER: True}
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


def test_wire_http_response_renders_envelope():
    response = errors.wire_http_response(errors.wire_response({"ok": True}, 201))
    assert response.status_code == 201
    assert json.loads(response.body) == {"ok": True}


@pytest.mark.parametrize("result", [
    {"content": {"ok": True}, "status_code": 200},
    {"__neutree_wire_response__": True},
])
def test_wire_http_response_fails_closed_without_envelope(result):
    response = errors.wire_http_response(result)
    assert response.status_code == 500
    assert json.loads(response.body)["error"]["type"] == "InternalServerError"


def test_rpc_error_response_is_a_neutral_json_500():
    response = errors.rpc_error_response(RuntimeError("rpc unavailable"), "chat")
    assert response.status_code == 500
    assert json.loads(response.body) == {"error": {
        "message": "rpc unavailable", "type": "RuntimeError", "code": 500, "param": None,
    }}


@pytest.mark.parametrize("stage", ["build", "await"])
def test_backend_response_contains_rpc_failures(stage):
    if stage == "build":
        def start():
            raise RuntimeError("rpc unavailable")
    else:
        async def start():
            raise RuntimeError("rpc unavailable")

    response = run(errors.backend_response(start, "chat"))
    assert response.status_code == 500
    assert json.loads(response.body) == {"error": {
        "message": "rpc unavailable", "type": "RuntimeError", "code": 500, "param": None,
    }}


def test_backend_response_renders_the_wire_envelope():
    async def start():
        return errors.wire_response({"ok": True}, 202)

    response = run(errors.backend_response(start, "chat"))
    assert response.status_code == 202
    assert json.loads(response.body) == {"ok": True}


@pytest.mark.parametrize("midstream", [False, True])
def test_to_wire_stream_frames_chunks_and_contains_failures(midstream):
    async def chunks():
        if midstream:
            yield 'data: native\n\n'
        yield {"value": 1}
        raise RuntimeError("stream died")

    frames = run(collect(errors.to_wire_stream(chunks())))
    offset = 1 if midstream else 0
    assert frames[offset] == 'data: {"value": 1}\n\n'
    assert json.loads(frames[offset + 1].removeprefix("data:")) == {"error": {
        "message": "stream died", "type": "RuntimeError", "code": 500, "param": None,
    }}
    assert frames[-1] == "data: [DONE]\n\n"


def test_to_wire_stream_serialization_failure_ends_the_stream():
    async def chunks():
        yield {"value": object()}

    frames = run(collect(errors.to_wire_stream(chunks())))
    assert json.loads(frames[0].removeprefix("data:"))["error"]["type"] == "TypeError"
    assert frames.count("data: [DONE]\n\n") == 1


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
            lambda: stream_of("data: first\n\n", "data: [DONE]\n\n"), where="stream",
        )
        assert response.status_code == 200
        return await collect_body(response)
    assert run(scenario()) == "data: first\n\ndata: [DONE]\n\n"


def test_open_stream_returns_first_error_frame_as_http_error():
    frame = "data: " + json.dumps(error_payload()) + "\n\n"
    response = run(errors.open_stream(lambda: stream_of(frame), where="stream"))
    assert response.status_code == 400
    assert json.loads(response.body) == error_payload()


def test_open_stream_failure_after_first_frame_becomes_in_band_error():
    async def failing():
        yield "data: first\n\n"
        raise RuntimeError("replica died")

    async def scenario():
        response = await errors.open_stream(lambda: failing(), where="stream")
        assert response.status_code == 200
        return await collect_body(response)

    body = run(scenario())
    assert body.startswith("data: first\n\n")
    assert '"message": "replica died"' in body
    assert '"type": "RuntimeError"' in body
    assert body.endswith("data: [DONE]\n\n")


def test_open_stream_start_failure_names_the_exception():
    def start():
        raise RuntimeError("private details")

    response = run(errors.open_stream(start, where="stream"))
    assert response.status_code == 500
    assert json.loads(response.body) == {"error": {
        "message": "private details", "type": "RuntimeError", "code": 500, "param": None,
    }}
