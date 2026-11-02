"""Wire-format helpers shared by this engine version's Backend and Controller.

Everything here runs inside the Backend actor. Validation errors reproduce the
pinned engine version's HTTP protocol without importing its server module.
"""

import json
from http import HTTPStatus
from typing import Any, Dict

from fastapi.exceptions import RequestValidationError
from pydantic import ValidationError
from starlette.responses import JSONResponse

from serve._utils.serve_errors import json_safe, stream_error_status, wire_response


class _FakeRawRequest:
    """Minimal stand-in for FastAPI Request used when invoking SGLang's
    OpenAIServingBase from a Ray actor where no live HTTP request exists.

    SGLang serving handlers call ``raw_request.is_disconnected()`` to abort
    generation when the client disappears; passing ``None`` would raise
    ``AttributeError``. This shim returns ``False`` so generation always
    runs to completion (the Controller still owns the real client connection).
    """

    headers: Dict[str, str] = {}

    async def is_disconnected(self) -> bool:
        return False


def _error_content(
    message: str,
    status_code: int = 400,
    error_type: str | None = None,
    param: str | None = None,
) -> Dict[str, Any]:
    if not isinstance(error_type, str) or not error_type:
        # HTTPStatus only knows registered codes; engines may report
        # non-standard ones such as 529.
        try:
            error_type = HTTPStatus(status_code).phrase
        except ValueError:
            error_type = "InternalServerError" if status_code >= 500 else "BadRequestError"
    return {
        "object": "error",
        "message": message,
        "type": error_type,
        "param": param,
        "code": status_code,
    }


def _error_response(
    message: str,
    status_code: int = 400,
    error_type: str | None = None,
    param: str | None = None,
) -> JSONResponse:
    """Build an SGLang-shaped HTTP error; SGLang reports errors flat."""
    return JSONResponse(
        content=_error_content(message, status_code, error_type, param),
        status_code=status_code,
    )


def _input_error_handler(request, exc) -> JSONResponse:
    return _error_response(str(exc))


def _flatten_http_error(content: Any) -> Any:
    """SGLang reports errors flat over HTTP and nested inside SSE frames."""
    if isinstance(content, dict) and content.get("object") != "error":
        error = content.get("error")
        if isinstance(error, dict):
            status = error.get("code")
            if (
                not isinstance(status, int)
                or isinstance(status, bool)
                or not 400 <= status <= 599
            ):
                status = stream_error_status(error)
            return _error_content(
                error.get("message", "Internal server error"),
                status,
                error.get("type", "InternalServerError"),
                error.get("param"),
            )
    return content


async def _handle_request(get_handler, request_type, payload):
    try:
        request = request_type(**payload)
    except ValidationError as exc:
        from sglang.srt.entrypoints.openai.protocol import ErrorResponse

        # Match SGLang HTTP validation without importing its server module.
        errors = [
            {**error, "loc": ("body",) + error["loc"]}
            for error in exc.errors(include_url=False)
        ]
        validation_error = RequestValidationError(errors, body=payload)
        message, errors_text = str(validation_error), str(validation_error.errors())
        if errors_text and errors_text != message:
            message = f"{message} {errors_text}"
        return ErrorResponse(message=message, type="Bad Request", code=400)
    return await get_handler().handle_request(request, _FakeRawRequest())


def _extract_serializable(result) -> Dict[str, Any]:
    """Keep the HTTP status and JSON data inside this wrapper's wire envelope."""
    status = 200
    if isinstance(result, JSONResponse):
        content, status = json.loads(result.body), result.status_code
    elif hasattr(result, "model_dump"):
        content = result.model_dump(mode="json")
    else:
        content = result
    if not isinstance(result, JSONResponse) and isinstance(content, dict):
        if content.get("object") == "error":
            status = stream_error_status(content)
        elif "error" in content:
            status = stream_error_status(content["error"])
    content = json_safe(content, strict=True)
    return wire_response(content, status, sanitized=True)
