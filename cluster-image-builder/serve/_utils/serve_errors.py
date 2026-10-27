"""Engine-neutral HTTP/SSE errors for Serve wrappers."""

import json
import logging
from typing import Any, AsyncGenerator

from starlette.responses import JSONResponse


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
    if not isinstance(payload, dict):
        raise RequestInputError("Request body must be a JSON object")
    stream = payload.get("stream", False)
    if not isinstance(stream, bool):
        raise RequestInputError("stream must be a boolean")
    return stream


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
    error_type: str = "BadRequestError",
    param: str | None = None,
) -> JSONResponse:
    """Build the HTTP response at the Controller boundary."""
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


async def error_stream(error: Any) -> AsyncGenerator[str, None]:
    if isinstance(error, JSONResponse):
        error = json.loads(error.body)
    elif hasattr(error, "model_dump"):
        error = error.model_dump(mode="json")
    yield f"data: {json.dumps(error, allow_nan=False)}\n\n"
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
        # Symbolic error codes carry server status through the error type.
        if error.get("type") in ("internal_server_error", "InternalServerError"):
            return 500
    return 400


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
        return JSONResponse(content=data, status_code=stream_error_status(data))
    if "error" not in data:
        return None
    return JSONResponse(content=data, status_code=stream_error_status(data["error"]))
