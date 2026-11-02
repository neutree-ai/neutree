"""Wire-format helpers shared by this engine version's Backend and Controller.

Everything here runs inside the Backend actor. Errors are mapped to the pinned
engine version's own response factories so clients see the native protocol.
"""

import json
import logging
from http import HTTPStatus
from types import SimpleNamespace
from typing import Any, Dict

from fastapi import HTTPException, Request
from fastapi.exceptions import RequestValidationError
from pydantic import ValidationError
from starlette.responses import JSONResponse

from vllm.exceptions import GenerationError
from vllm.entrypoints.serve.engine.protocol import ErrorResponse
from vllm.entrypoints.serve.exception_handling.error_response import create_error_response
from vllm.entrypoints.serve.exception_handling.handlers.validation import validation_exception_handler

from serve._utils.serve_errors import (
    RequestInputError,
    backend_error,
    error_stream,
    json_safe,
    stream_error_status,
    wire_response,
)


def _backend_error(exc: Exception, where: str) -> Any:
    if isinstance(exc, RequestInputError):
        return backend_error(exc, where)
    if not isinstance(exc, GenerationError):
        logging.error("%s failed: %s", where, exc, exc_info=(type(exc), exc, exc.__traceback__))
    try:
        if isinstance(exc, HTTPException):
            status = HTTPStatus(exc.status_code)
            return create_error_response(exc.detail, err_type=status.phrase, status_code=status)
        return create_error_response(exc)
    except Exception as mapping_error:
        return backend_error(mapping_error, where)


async def _parse_request(request_type, payload):
    try:
        return request_type(**payload)
    except ValidationError as exc:
        errors = [{**error, "loc": ("body", *error["loc"])} for error in exc.errors(include_url=False)]
        app = SimpleNamespace(state=SimpleNamespace(args=SimpleNamespace(log_error_stack=False)))
        request = Request({"type": "http", "app": app})
        return await validation_exception_handler(request, RequestValidationError(errors, body=payload))


def _to_wire_response(result: Any) -> Dict[str, Any]:
    try:
        status_code = result.error.code if isinstance(result, ErrorResponse) else 200
        if isinstance(result, JSONResponse):
            content = json.loads(result.body)
            status_code = result.status_code
        elif hasattr(result, "model_dump"):
            content = result.model_dump(mode="json")
        else:
            content = result
        if status_code == 200 and isinstance(content, dict) and "error" in content:
            status_code = stream_error_status(content["error"])
        # Fail inside the Backend guard rather than sending engine objects to Controller.
        content = json_safe(content, strict=True)
    except Exception as exc:
        return wire_response(backend_error(exc, "response serialization"), 500)
    return wire_response(content, status_code, sanitized=True)

async def _to_wire_stream(chunks):
    try:
        async for chunk in chunks:
            if isinstance(chunk, (str, bytes)):
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
        async for frame in error_stream(_backend_error(exc, "stream interrupted")):
            yield frame
