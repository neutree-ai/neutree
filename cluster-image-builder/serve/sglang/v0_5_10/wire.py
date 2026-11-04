"""SGLang adapter for the shared Serve response boundary.

SGLang's serving handlers expect an HTTP request object and classify their own
failures: ``handle_request`` converts HTTPException, ValueError and any other
exception into an ORJSONResponse with a status and an error body. The wrapper
therefore only supplies the request shim and passes the result through; it does
not re-derive statuses or bodies.
"""

from typing import Dict


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


async def _handle_request(get_handler, request_type, payload):
    """Invoke SGLang's serving handler with the request model.

    ``handle_request`` is the engine's public entry point: it owns validation
    and native error classification, so delegating to it keeps statuses and
    bodies native instead of re-deriving them here.
    """
    return await get_handler().handle_request(request_type(**payload), _FakeRawRequest())
