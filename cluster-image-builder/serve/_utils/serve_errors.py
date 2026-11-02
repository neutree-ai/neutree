"""Engine-neutral HTTP/SSE errors for Serve wrappers."""

import json
import logging
import math
from typing import Any, AsyncGenerator, Callable

from starlette.responses import JSONResponse, StreamingResponse


WIRE_RESPONSE_MARKER = "__neutree_wire_response__"

_TRUE_STRINGS = frozenset({"1", "on", "t", "true", "y", "yes"})
_FALSE_STRINGS = frozenset({"0", "off", "f", "false", "n", "no"})

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

_SYMBOLIC_ERROR_CODES = {
    "context_length_exceeded": 400,
    "model_not_found": 404,
    "rate_limit_exceeded": 429,
}


class RequestInputError(ValueError):
    """A request rejected before dispatch to the Backend."""


async def read_json_object(request) -> dict:
    try:
        payload = await request.json()
    except ValueError as exc:
        raise RequestInputError("Request body must be valid JSON") from exc
    if not isinstance(payload, dict):
        raise RequestInputError("Request body must be a JSON object")
    return payload


def is_streaming(payload: Any) -> bool:
    """Read `stream` with the engine's own lax boolean semantics."""
    if not isinstance(payload, dict):
        raise RequestInputError("Request body must be a JSON object")
    stream = payload.get("stream")
    if stream is None:
        return False
    if isinstance(stream, bool):
        return stream
    if isinstance(stream, int):
        if stream in (0, 1):
            return bool(stream)
    elif isinstance(stream, float):
        if stream in (0.0, 1.0):
            return bool(stream)
    elif isinstance(stream, str):
        value = stream.casefold()
        if value in _TRUE_STRINGS:
            return True
        if value in _FALSE_STRINGS:
            return False
    raise RequestInputError("stream must be a boolean")


def build_error(
    message: str,
    status_code: int = 400,
    error_type: str = "BadRequestError",
    param: str | None = None,
) -> dict:
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


async def input_error_handler(request, exc: RequestInputError) -> JSONResponse:
    return error_response(str(exc))


def backend_error(exc: Exception, where: str) -> dict:
    logging.error("%s failed: %s", where, exc, exc_info=(type(exc), exc, exc.__traceback__))
    if isinstance(exc, RequestInputError):
        return build_error(str(exc))
    return build_error("Internal server error", 500, "InternalServerError")


def json_safe(value: Any) -> Any:
    """Replace non-finite floats, which JSON encoders reject."""
    if isinstance(value, float):
        return value if math.isfinite(value) else None
    if isinstance(value, dict):
        return {key: json_safe(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [json_safe(item) for item in value]
    return value


def wire_response(content: Any, status_code: int = 200) -> dict:
    """Envelope a Backend result for the Ray actor boundary.

    Only the body and status code travel; HTTP headers set by the engine cannot
    cross the actor boundary and are not reproduced at the Controller.
    """
    return {
        WIRE_RESPONSE_MARKER: True,
        "content": json_safe(content),
        "status_code": status_code,
    }


def wire_http_response(
    result: Any,
    error_builder: Callable[..., JSONResponse] = error_response,
    content_transform: Callable[[Any], Any] | None = None,
) -> JSONResponse:
    """Render a Backend wire envelope, failing closed on a malformed one."""
    if isinstance(result, dict) and result.get(WIRE_RESPONSE_MARKER) is True:
        try:
            content = result["content"]
            status_code = int(result["status_code"])
            if content_transform is not None:
                content = content_transform(content)
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
    normalize_error: Callable[[JSONResponse], JSONResponse] | None = None,
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
        return normalize_error(failure) if normalize_error is not None else failure

    async def replay():
        yield first
        async for chunk in chunks:
            yield chunk

    return StreamingResponse(content=replay(), media_type="text/event-stream")


async def error_stream(error: Any) -> AsyncGenerator[str, None]:
    if isinstance(error, JSONResponse):
        error = json.loads(error.body)
    elif hasattr(error, "model_dump"):
        error = error.model_dump(mode="json")
    yield f"data: {json.dumps(json_safe(error), allow_nan=False)}\n\n"
    yield "data: [DONE]\n\n"


def stream_error_status(error: Any) -> int:
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
        return _CLIENT_ERROR_TYPES.get(error.get("type"), 500)
    return 500


def stream_error_response(first_chunk: Any) -> JSONResponse | None:
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
    if data.get("object") == "error":
        return JSONResponse(content=json_safe(data), status_code=stream_error_status(data))
    if "error" not in data:
        return None
    return JSONResponse(content=json_safe(data), status_code=stream_error_status(data["error"]))
