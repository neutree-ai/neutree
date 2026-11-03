"""Wire-format helpers shared by this engine version's Backend and Controller.

Everything here runs inside the Backend actor. Errors are mapped through the
pinned engine version's own router formatter so clients see the native protocol.
"""

import json
from typing import Any, Dict, Tuple

from fastapi import HTTPException
from starlette.concurrency import iterate_in_threadpool
from starlette.responses import JSONResponse

from llama_cpp.server.app import create_chat_completion, create_completion, router as native_router
from llama_cpp.server.types import CreateChatCompletionRequest, CreateCompletionRequest, CreateEmbeddingRequest

from serve._utils.serve_errors import (
    RequestInputError,
    backend_error,
    error_stream,
    json_safe,
    stream_error_status,
    wire_response,
)


def _native_error_response(exc: Exception, payload, where: str) -> Tuple[Dict[str, Any], int]:
    """Map a Backend exception to the native router formatter's body and status."""
    if isinstance(exc, RequestInputError):
        return backend_error(exc, where), 400
    try:
        if isinstance(exc, HTTPException):
            # Only the body and status code cross the Ray actor boundary; HTTP
            # headers set by the engine are not reproduced at the Controller.
            return {"detail": exc.detail}, exc.status_code
        try:
            # Request parsing accepts lax stream spellings, including null, that
            # the native request models reject. The formatter only needs the
            # request shape, so rebuild without stream to keep native statuses.
            body_payload = {key: value for key, value in payload.items() if key != "stream"}
            if "messages" in body_payload:
                body = CreateChatCompletionRequest(**body_payload)
            elif "prompt" in body_payload:
                body = CreateCompletionRequest(**body_payload)
            else:
                body = CreateEmbeddingRequest(**body_payload)
        except Exception:
            body = None
        endpoint = create_chat_completion if isinstance(body, CreateChatCompletionRequest) else create_completion
        route = next(route for route in native_router.routes if route.endpoint is endpoint)
        status_code, detail = route.error_message_wrapper(error=exc, body=body)
        return {"error": detail}, status_code
    except Exception as mapping_error:
        return backend_error(mapping_error, f"{where} error formatting"), 500


async def _stream_chunks(chunks, payload, where: str):
    """Iterate native chunks as SSE, containing iteration errors in the Backend."""
    first = True
    try:
        async for chunk in iterate_in_threadpool(chunks):
            try:
                frame = f"data: {json.dumps(json_safe(chunk), allow_nan=False)}\n\n"
            except Exception as exc:
                async for error_frame in error_stream(backend_error(exc, "stream serialization")):
                    yield error_frame
                return
            first = False
            yield frame
        yield "data: [DONE]\n\n"
    except Exception as exc:
        # A native HTTPException body is only {"detail": ...}, so its status
        # cannot be recovered from the error payload. Pre-first failures carry
        # the status explicitly; mid-stream failures stay neutral in-band.
        if first:
            content, status_code = _native_error_response(exc, payload, where)
            error = wire_response(content, status_code)
        else:
            error = backend_error(exc, "stream interrupted")
        async for frame in error_stream(error):
            yield frame


def _to_wire_response(result: Any, status_code: int | None = None) -> Dict[str, Any]:
    """Convert a native result into the JSON-safe wire envelope."""
    try:
        if status_code is None:
            status_code = 200
            if isinstance(result, JSONResponse):
                content, status_code = json.loads(result.body), result.status_code
            else:
                content = result.model_dump(mode="json") if hasattr(result, "model_dump") else result
            if status_code == 200 and isinstance(content, dict) and "error" in content:
                status_code = stream_error_status(content["error"])
        else:
            content = result.model_dump(mode="json") if hasattr(result, "model_dump") else result
        content = json_safe(content, strict=True)
    except Exception as exc:
        return wire_response(backend_error(exc, "response serialization"), 500)
    return wire_response(content, status_code, sanitized=True)
