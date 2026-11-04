"""Wire-format helpers shared by this engine version's Backend and Controller.

Everything here runs inside the Backend actor. Errors are mapped to JSON inside
the Backend so engine objects never cross the Ray actor boundary. SGLang reports
errors flat over HTTP but nested inside SSE frames, so the two renderings are
kept separate below instead of sharing one error builder.
"""

import json
from http import HTTPStatus
from typing import Any, Dict

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
        """Report a live client; the Controller owns the real connection."""
        return False


def _error_content(
    message: str,
    status_code: int = 400,
    error_type: str | None = None,
    param: str | None = None,
) -> Dict[str, Any]:
    """Build SGLang's flat error body."""
    # SGLang puts the error fields at the top level (object="error") instead of
    # nesting them under "error", so the shared OpenAI-shaped builder cannot
    # produce the native HTTP body.
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


def _flatten_http_error(content: Any) -> Any:
    """SGLang reports errors flat over HTTP and nested inside SSE frames."""
    # Frames keep the nested shape SGLang emits inside a stream; a client calling
    # the HTTP endpoint sees the flat shape, so unwrap it before rendering.
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
    """Invoke SGLang's serving handler with the request model."""
    # ``handle_request`` is the engine's public entry point: it owns validation
    # and native error classification, so delegating to it keeps statuses and
    # bodies native instead of re-deriving them here.
    return await get_handler().handle_request(request_type(**payload), _FakeRawRequest())


def _extract_serializable(result) -> Dict[str, Any]:
    """Keep the HTTP status and JSON data inside this wrapper's wire envelope."""
    # A JSONResponse already decided its status; other results encode it in an
    # error body (flat for SGLang errors), so recover it here rather than
    # defaulting every returned value to 200.
    status = 200
    if isinstance(result, JSONResponse):
        content, status = json.loads(result.body), result.status_code
    elif hasattr(result, "model_dump"):
        content = result.model_dump(mode="json")
    else:
        content = result
    if not isinstance(result, JSONResponse) and isinstance(content, dict):
        if content.get("object") == "error":
            # SGLang error payloads use object='error' and include an HTTP code.
            status = stream_error_status(content)
        elif "error" in content:
            status = stream_error_status(content["error"])
    content = json_safe(content, strict=True)
    return wire_response(content, status, sanitized=True)
