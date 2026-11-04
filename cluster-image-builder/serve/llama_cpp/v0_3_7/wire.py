"""llama.cpp adapter for the shared Serve error boundary.

The ``Llama`` API has no HTTP layer: context-length, model and formatting
failures are raised as exceptions, and the native HTTP server classifies them
only after they reach its own route handler. The wrapper calls the API directly,
so those exceptions take the shared neutral path instead of re-deriving the
native route's statuses here.
"""

import json

from starlette.concurrency import iterate_in_threadpool

from serve._utils.serve_errors import backend_error, error_stream, json_safe


async def _stream_chunks(chunks):
    """Iterate native chunks as SSE, containing iteration errors in the Backend."""
    try:
        async for chunk in iterate_in_threadpool(chunks):
            try:
                frame = f"data: {json.dumps(json_safe(chunk), allow_nan=False)}\n\n"
            except Exception as exc:
                async for error_frame in error_stream(backend_error(exc, "stream serialization")):
                    yield error_frame
                return
            yield frame
        yield "data: [DONE]\n\n"
    except Exception as exc:
        async for frame in error_stream(backend_error(exc, "stream interrupted")):
            yield frame
