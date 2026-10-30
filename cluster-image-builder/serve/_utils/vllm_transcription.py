"""Multipart and Ray streaming transport for the versioned vLLM adapters."""

import asyncio
import io
import json
import logging

from fastapi import HTTPException, UploadFile
from starlette.responses import JSONResponse, PlainTextResponse, StreamingResponse


def _error(message, status_code):
    return {"error": {
        "message": message,
        "type": "invalid_request_error" if status_code < 500 else "internal_server_error",
        "code": status_code,
    }}


async def transcription_results(backend, request_type, audio_data, payload):
    # UploadFile and the HTTP Request must stay out of the Ray object store.
    upload = UploadFile(io.BytesIO(audio_data))
    streaming = False
    error = None
    try:
        request = request_type(file=upload, **payload)
        handler = await backend._ensure_transcription()
        result = await handler.create_transcription(audio_data, request, None)
        if hasattr(result, "__aiter__"):
            async for chunk in result:
                streaming = True
                yield chunk
        else:
            yield result.model_dump()
    except HTTPException as exc:
        error = _error(str(exc.detail), exc.status_code)
    except ValueError as exc:
        error = _error(str(exc), 400)
    except Exception:
        logging.exception("Transcription failed")
        error = _error("Transcription processing failed", 500)
    finally:
        await upload.close()
    if error is not None:
        if streaming:
            yield f"data: {json.dumps(error)}\n\n"
            yield "data: [DONE]\n\n"
        else:
            yield error


async def transcription_response(backend, request, max_audio_filesize_mb):
    try:
        # Bound materialization before copying the audio into the Ray object store.
        limit = int(max_audio_filesize_mb * 1024 * 1024)
        audio_data = await request.file.read(limit + 1)
    finally:
        await request.file.close()
    if len(audio_data) > limit:
        return JSONResponse(_error("Maximum file size exceeded", 400), status_code=400)
    if not audio_data:
        return JSONResponse(_error("Audio file is empty", 400), status_code=400)

    # vLLM allows extra fields, which can contain additional uploaded files.
    payload = request.model_dump(
        include=set(type(request).model_fields) - {"file"},
        by_alias=True, exclude_unset=True,
    )
    result = backend.options(stream=True).create_transcription.remote(audio_data, payload)
    try:
        first = await result.__anext__()
    except asyncio.CancelledError:
        result.cancel()
        raise
    except Exception:
        result.cancel()
        logging.exception("Failed to initialize transcription response")
        return JSONResponse(_error("Transcription processing failed", 500), status_code=500)

    if isinstance(first, dict):
        if "error" in first:
            return JSONResponse(first, status_code=first["error"]["code"])
        if request.response_format == "text":
            return PlainTextResponse(first["text"])
        return JSONResponse(first)

    async def stream():
        try:
            yield first
            async for chunk in result:
                yield chunk
        except Exception:
            logging.exception("Transcription stream failed")
            yield f"data: {json.dumps(_error('Transcription processing failed', 500))}\n\n"
            yield "data: [DONE]\n\n"
        finally:
            result.cancel()

    return StreamingResponse(stream(), media_type="text/event-stream")
