"""Engine-neutral HTTP/SSE errors for Serve wrappers."""

import json
import logging
import math
from typing import Any, AsyncGenerator, Callable

from starlette.responses import JSONResponse, StreamingResponse


# The Ray actor boundary only carries JSON, so the Backend returns this envelope
# instead of an HTTP response: the status code would otherwise be lost, and a
# bare body could not be told apart from an envelope. Both sides import this
# module from the same engine image, so the key stays consistent per deployment.
WIRE_RESPONSE_MARKER = "__neutree_wire_response__"

_CLIENT_ERROR_TYPES = {
    "BadRequestError": 400,
    "NotFoundError": 404,
    "UnprocessableEntityError": 422,
    "RateLimitError": 429,
    "invalid_request_error": 400,
    "not_found_error": 404,
    "rate_limit_exceeded": 429,
    "authentication_error": 401,
    "permission_error": 403,
}

# llama.cpp's native formatter classifies model-not-found as
# invalid_request_error (HTTP 400), not OpenAI's 404. A first-frame stream error
# must restore the engine's native status, so the symbolic code follows it.
_SYMBOLIC_ERROR_CODES = {
    "context_length_exceeded": 400,
    "model_not_found": 400,
    "rate_limit_exceeded": 429,
}


def build_error(
    message: str,
    status_code: int = 400,
    error_type: str = "BadRequestError",
    param: str | None = None,
) -> dict:
    """Build the Neutree error body used by Controller-owned responses."""
    return {"error": {
            "message": message,
            "type": error_type,
            "code": status_code,
            "param": param,
        }}


def error_response(
    message: str,
    status_code: int = 400,
    error_type: str | None = None,
    param: str | None = None,
) -> JSONResponse:
    """Build the HTTP response at the Controller boundary."""
    if error_type is None:
        error_type = "InternalServerError" if status_code >= 500 else "BadRequestError"
    return JSONResponse(
        content=build_error(message, status_code, error_type, param),
        status_code=status_code,
    )


def backend_error(exc: Exception, where: str) -> dict:
    """Turn a wrapper failure into a neutral error body and log its cause."""
    logging.error("%s failed: %s", where, exc, exc_info=(type(exc), exc, exc.__traceback__))
    return build_error("Internal server error", 500, "InternalServerError")


def json_safe(value: Any, *, strict: bool = False) -> Any:
    """Replace non-finite floats, which JSON encoders reject.

    ``strict`` also rejects values the JSON encoder cannot represent, so a
    Backend can validate its response without a second encoding pass.
    """
    if isinstance(value, float):
        return value if math.isfinite(value) else None
    if isinstance(value, dict):
        if strict:
            for key in value:
                if not isinstance(key, str):
                    raise TypeError(f"JSON object key must be str, got {type(key).__name__}")
        return {key: json_safe(item, strict=strict) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [json_safe(item, strict=strict) for item in value]
    if strict and value is not None and not isinstance(value, (str, int, bool)):
        raise TypeError(f"Object of type {type(value).__name__} is not JSON serializable")
    return value


def wire_response(content: Any, status_code: int = 200, *, sanitized: bool = False) -> dict:
    """Envelope a Backend result for the Ray actor boundary.

    Only the body and status code travel; HTTP headers set by the engine cannot
    cross the actor boundary and are not reproduced at the Controller. Set
    ``sanitized`` when the caller already ran ``json_safe`` and validated the
    body, so the same object graph is not walked twice.
    """
    # The marker is what lets the Controller unpack the result without guessing:
    # a body that happens to contain "content" or "status_code" is still content.
    return {
        WIRE_RESPONSE_MARKER: True,
        "content": content if sanitized else json_safe(content),
        "status_code": status_code,
    }


def wire_http_response(
    result: Any,
    error_builder: Callable[..., JSONResponse] = error_response,
) -> JSONResponse:
    """Render a Backend wire envelope, failing closed on a malformed one."""
    # Identity check, not truthiness: only the exact envelope is unpacked.
    # Anything else fails closed instead of leaking a Backend shape to clients.
    if isinstance(result, dict) and result.get(WIRE_RESPONSE_MARKER) is True:
        try:
            content = result["content"]
            status_code = int(result["status_code"])
            return JSONResponse(content=content, status_code=status_code)
        except Exception:
            logging.exception("Backend wire response could not be rendered")
    else:
        logging.error("Backend returned no wire response: %s", type(result).__name__)
    return error_builder("Backend returned an invalid response", 500)


async def open_stream(
    start: Callable[[], Any],
    *,
    message: str,
    error_builder: Callable[..., JSONResponse] = error_response,
) -> Any:
    """Commit to an SSE response only after the Backend's first frame is known.

    The first frame decides the HTTP status: an error frame becomes a JSON error
    response rather than a 200 stream, and an empty stream becomes an empty body.
    Frames after the first are forwarded unchanged, so mid-stream errors stay in
    band. ``start`` builds the Backend stream on call so RPC construction failures
    are caught by the same guard.
    """
    try:
        chunks = start()
        first = await chunks.__anext__()
    except StopAsyncIteration:
        return StreamingResponse(content=iter(()), media_type="text/event-stream")
    except Exception:
        logging.exception(message)
        return error_builder(message, 500)
    failure = stream_error_response(first)
    if failure is not None:
        return failure

    async def replay():
        """Replay the already-read first frame, then forward the remainder.

        A Backend that dies after the first frame cannot change the HTTP status,
        so its failure becomes an in-band error frame plus the terminator rather
        than a stream that just stops without saying why.
        """
        yield first
        try:
            async for chunk in chunks:
                yield chunk
        except Exception as exc:
            async for frame in error_stream(backend_error(exc, "backend stream")):
                yield frame

    return StreamingResponse(content=replay(), media_type="text/event-stream")


async def error_stream(error: Any) -> AsyncGenerator[str, None]:
    """Render one error frame followed by the SSE terminator."""
    if isinstance(error, JSONResponse):
        error = json.loads(error.body)
    elif hasattr(error, "model_dump"):
        error = error.model_dump(mode="json")
    yield f"data: {json.dumps(json_safe(error), allow_nan=False)}\n\n"
    yield "data: [DONE]\n\n"


def stream_error_status(error: Any) -> int:
    """Recover an HTTP status from a native error body without inventing one."""
    if isinstance(error, dict):
        code = error.get("code")
        if isinstance(code, int) and not isinstance(code, bool) and 400 <= code <= 599:
            return code
        if isinstance(code, str):
            try:
                status_code = int(code)
            except ValueError:
                status_code = None
            if status_code is not None and 400 <= status_code <= 599:
                return status_code
            if code in _SYMBOLIC_ERROR_CODES:
                return _SYMBOLIC_ERROR_CODES[code]
        error_type = error.get("type")
        if not isinstance(error_type, str):
            return 500
        return _CLIENT_ERROR_TYPES.get(error_type, 500)
    return 500


def stream_error_response(first_chunk: Any) -> JSONResponse | None:
    """Turn a first error frame into JSON, or return None to replay it as SSE."""
    if isinstance(first_chunk, bytes):
        try:
            first_chunk = first_chunk.decode("utf-8")
        except UnicodeDecodeError:
            return None
    if not isinstance(first_chunk, str) or not first_chunk.startswith("data:"):
        return None
    payload = first_chunk.removeprefix("data:").strip()
    if payload == "[DONE]":
        return None
    try:
        data = json.loads(payload)
    except json.JSONDecodeError:
        return None
    if not isinstance(data, dict):
        return None
    if data.get(WIRE_RESPONSE_MARKER) is True:
        # Pre-first-frame failures can carry an explicit status. A native
        # HTTPException body is only {"detail": ...}, so the status cannot be
        # recovered from the error payload itself.
        return wire_http_response(data)
    if data.get("object") == "error":
        return JSONResponse(content=json_safe(data), status_code=stream_error_status(data))
    if "error" not in data:
        return None
    return JSONResponse(content=json_safe(data), status_code=stream_error_status(data["error"]))
