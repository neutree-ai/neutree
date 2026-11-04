"""Wire-format helpers shared by this engine version's Backend and Controller.

Everything here runs inside the Backend actor. Errors are mapped to the pinned
engine version's own response factories so clients see the native protocol:
vLLM classifies failures itself (`create_error_response` for exceptions,
`ErrorResponse` for returned errors), so the wrapper defers to those instead of
re-deriving statuses from exception types.
"""

import json
import logging
from http import HTTPStatus
from typing import Any, Dict

from fastapi import HTTPException
from starlette.responses import JSONResponse

from vllm.exceptions import GenerationError
from vllm.entrypoints.serve.engine.protocol import ErrorResponse
from vllm.entrypoints.serve.exception_handling.error_response import create_error_response

from serve._utils.serve_errors import (
    backend_error,
    error_stream,
    json_safe,
    stream_error_status,
    wire_response,
)


def _backend_error(exc: Exception, where: str) -> Any:
    """Map an exception to the native factory, logging only unexpected failures."""
    # GenerationError is a normal client-visible outcome (for example a context
    # length overflow), so only unexpected failures are logged with a traceback.
    if not isinstance(exc, GenerationError):
        logging.error("%s failed: %s", where, exc, exc_info=(type(exc), exc, exc.__traceback__))
    try:
        # A bare HTTPException carries a status but not vLLM's error type; rebuild
        # it through the native factory so the body matches what vLLM serves.
        if isinstance(exc, HTTPException):
            status = HTTPStatus(exc.status_code)
            return create_error_response(exc.detail, err_type=status.phrase, status_code=status)
        return create_error_response(exc)
    except Exception as mapping_error:
        # The factory itself failed; a neutral 500 keeps the failure inside the
        # Backend instead of escaping the actor as a RayTaskError.
        return backend_error(mapping_error, where)


def _to_wire_response(result: Any) -> Dict[str, Any]:
    """Convert a native result into the JSON-safe wire envelope."""
    try:
        # Native handlers also return errors as values; the status lives in the
        # ErrorResponse, not in an HTTP response.
        status_code = result.error.code if isinstance(result, ErrorResponse) else 200
        if isinstance(result, JSONResponse):
            # Some paths already built an HTTP response; its status is authoritative.
            content = json.loads(result.body)
            status_code = result.status_code
        elif hasattr(result, "model_dump"):
            content = result.model_dump(mode="json")
        else:
            content = result
        if status_code == 200 and isinstance(content, dict) and "error" in content:
            # An error body can arrive as a 200 value (for example a stream's first
            # chunk); restore the status the engine encoded in the body.
            status_code = stream_error_status(content["error"])
        # Fail inside the Backend guard rather than sending engine objects to Controller.
        content = json_safe(content, strict=True)
    except Exception as exc:
        return wire_response(backend_error(exc, "response serialization"), 500)
    return wire_response(content, status_code, sanitized=True)

async def _to_wire_stream(chunks):
    """Frame native chunks as SSE and contain iteration errors in the Backend."""
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
        # into an error frame, and let the error body carry the status so a
        # failure before the first frame can still restore it over HTTP.
        async for frame in error_stream(_backend_error(exc, "stream interrupted")):
            yield frame
