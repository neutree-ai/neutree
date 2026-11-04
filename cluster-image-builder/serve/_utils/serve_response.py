"""Engine-neutral HTTP/SSE responses for Serve wrappers.

Two paths exist here and nowhere else:

* a result the engine returned is passed through with the status the engine put
  on it (its own error objects included);
* anything the engine raised becomes one neutral error body that names the
  exception and carries its message.

Keeping the second path engine-neutral is deliberate: the wrappers do not map
per-version exception classes, so a new pinned engine version needs no new error
table here.
"""

import json
import logging
import math
from typing import Any, AsyncGenerator, Callable

from pydantic import ValidationError
from starlette.responses import JSONResponse, StreamingResponse


# The Ray actor boundary only carries JSON, so the Backend returns this envelope
# instead of an HTTP response: the status code would otherwise be lost, and a
# bare body could not be told apart from an envelope. Both sides import this
# module from the same engine image, so the key stays consistent per deployment.
WIRE_RESPONSE_MARKER = "__neutree_wire_response__"


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
    """Turn a raised exception into the neutral error body.

    Engines that handle a failure themselves return it instead of raising it, so
    reaching this function means no engine classification exists. The body names
    the exception instead of guessing an engine status; a request-model
    ``ValidationError`` is the one exception, because every engine ships a
    schema and a schema failure is always a client error.
    """
    if isinstance(exc, ValidationError):
        logging.warning("%s rejected: %s", where, exc)
        return build_error(str(exc), 400, type(exc).__name__)
    logging.error("%s failed: %s", where, exc, exc_info=(type(exc), exc, exc.__traceback__))
    return build_error(str(exc), 500, type(exc).__name__)


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


def stream_error_status(error: Any) -> int:
    """Read the HTTP status an error body carries, falling back to 500.

    Only the numeric code is read. Body shape and type names differ per engine
    and per version, so deriving a status from them would reintroduce the
    per-version tables this boundary exists to avoid.
    """
    if isinstance(error, dict):
        code = error.get("code")
        if isinstance(code, bool):
            return 500
        if isinstance(code, int) and 400 <= code <= 599:
            return code
        if isinstance(code, str) and code.isdigit() and 400 <= int(code) <= 599:
            return int(code)
    return 500


def returned_error_status(content: dict) -> int:
    """Status carried by an error body the engine returned as a normal value."""
    if content.get("object") == "error":
        return stream_error_status(content)
    error = content.get("error")
    return stream_error_status(error) if isinstance(error, dict) else 200


def to_wire_response(result: Any) -> dict:
    """Convert a Backend result into the JSON-safe wire envelope.

    A returned result is the response: a pydantic model is dumped, a ready
    JSONResponse keeps its status, and an error body the engine returned as a
    value keeps the code it carries. Serialization failures are contained here
    so the Controller never receives an engine object or a bare exception.
    """
    try:
        if isinstance(result, JSONResponse):
            content, status_code = json.loads(result.body), result.status_code
        elif hasattr(result, "model_dump"):
            content, status_code = result.model_dump(mode="json"), 200
        else:
            content, status_code = result, 200
        if status_code == 200 and isinstance(content, dict):
            status_code = returned_error_status(content)
        content = json_safe(content, strict=True)
    except Exception as exc:
        return wire_response(backend_error(exc, "response serialization"), 500)
    return wire_response(content, status_code, sanitized=True)


async def to_wire_stream(chunks):
    """Frame Backend chunks as SSE and contain iteration errors in the Backend."""
    try:
        async for chunk in chunks:
            if isinstance(chunk, (str, bytes)):
                # Native SSE frames are already wire-formatted; re-encoding them
                # would double-escape the payload.
                yield chunk
            else:
                try:
                    if hasattr(chunk, "model_dump"):
                        chunk = chunk.model_dump(mode="json")
                    frame = f"data: {json.dumps(json_safe(chunk), allow_nan=False)}\n\n"
                except Exception as exc:
                    async for frame in error_stream(backend_error(exc, "stream serialization")):
                        yield frame
                    return
                yield frame
    except Exception as exc:
        # The Controller must never see a bare exception from the actor: turn it
        # into an error frame whose body carries the status, so a failure before
        # the first frame can still be rendered as an HTTP error.
        async for frame in error_stream(backend_error(exc, "stream interrupted")):
            yield frame


def wire_http_response(result: Any) -> JSONResponse:
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
    return error_response("Backend returned an invalid response", 500)


def rpc_error_response(exc: Exception, where: str) -> JSONResponse:
    """Render a Controller-side RPC failure as a neutral error response."""
    body = backend_error(exc, where)
    return JSONResponse(content=body, status_code=body["error"]["code"])


async def backend_response(start, where: str) -> JSONResponse:
    """Await a Backend RPC and render its envelope.

    ``start`` builds the call on demand so a failure while constructing the RPC
    (a dead actor, a dropped handle) is contained like any other interruption;
    the Controller fails closed with a JSON 500 instead of a FastAPI error page.
    """
    try:
        result = await start()
    except Exception as exc:
        return rpc_error_response(exc, where)
    return wire_http_response(result)


async def open_stream(
    start: Callable[[], Any],
    *,
    where: str,
) -> Any:
    """Commit to an SSE response only after the Backend's first frame is known.

    The first frame decides the HTTP status: an error frame becomes a JSON error
    response rather than a 200 stream, and an empty stream becomes an empty body.
    Frames after the first are forwarded unchanged, so mid-stream errors stay in
    band. ``start`` builds the Backend stream on call so RPC construction failures
    are caught by the same guard; ``where`` only names the step in logs, because
    the response body carries the exception itself.
    """
    try:
        chunks = start()
        first = await chunks.__anext__()
    except StopAsyncIteration:
        return StreamingResponse(content=iter(()), media_type="text/event-stream")
    except Exception as exc:
        return rpc_error_response(exc, where)
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
        return wire_http_response(data)
    if data.get("object") == "error":
        return JSONResponse(content=json_safe(data), status_code=stream_error_status(data))
    if "error" not in data:
        return None
    return JSONResponse(content=json_safe(data), status_code=stream_error_status(data["error"]))
