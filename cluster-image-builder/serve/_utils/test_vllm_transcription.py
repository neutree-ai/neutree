"""HTTP transport tests; vLLM, Ray, audio decoding and inference are mocked."""

import asyncio
import io
from pathlib import Path
import sys
from typing import Annotated, Literal
import unittest
from unittest.mock import AsyncMock, Mock

from fastapi import FastAPI, Form, HTTPException, UploadFile
from fastapi.testclient import TestClient
from pydantic import BaseModel, Field

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from serve._utils.vllm_transcription import transcription_response, transcription_results


class RequestModel(BaseModel):
    file: UploadFile
    model: str | None = None
    language: str | None = None
    prompt: str = ""
    response_format: Literal["json", "text", "verbose_json"] = "json"
    temperature: float = 0
    stream: bool = False
    timestamp_granularities: list[Literal["word", "segment"]] = Field(
        default=[], alias="timestamp_granularities[]",
    )


class ResultModel(BaseModel):
    text: str


class RayStream:
    def __init__(self, iterator):
        self.iterator = iterator
        self.cancel = Mock()

    def __aiter__(self):
        return self

    async def __anext__(self):
        return await self.iterator.__anext__()


class TranscriptionTransportTests(unittest.TestCase):
    def setUp(self):
        self.handler = Mock()
        self.handler.create_transcription = AsyncMock(return_value=ResultModel(text="hello"))
        self.backend = Mock()
        self.backend._ensure_transcription = AsyncMock(return_value=self.handler)
        self.handle = Mock()
        self.handle.options.return_value = self.handle
        self.streams = []

        def remote(audio, payload):
            stream = RayStream(transcription_results(self.backend, RequestModel, audio, payload))
            self.streams.append(stream)
            return stream

        self.handle.create_transcription.remote.side_effect = remote
        app = FastAPI()

        @app.post("/v1/audio/transcriptions")
        async def transcriptions(request: Annotated[RequestModel, Form()]):
            return await transcription_response(self.handle, request, 1 / 1024)

        self.client = TestClient(app)

    def post(self, data=None, audio=b"audio"):
        return self.client.post(
            "/v1/audio/transcriptions", data=data,
            files={"file": ("clip.wav", audio, "audio/wav")},
        )

    def test_multipart_fields_and_bytes_cross_ray_boundary(self):
        response = self.post({
            "model": "asr", "language": "zh", "prompt": "names",
            "temperature": "0.25", "stream": "false",
            "timestamp_granularities[]": ["word", "segment"],
        })
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json(), {"text": "hello"})
        audio, payload = self.handle.create_transcription.remote.call_args.args
        self.assertEqual(audio, b"audio")
        self.assertNotIn("file", payload)
        self.assertEqual(payload["timestamp_granularities[]"], ["word", "segment"])
        self.assertIs(payload["stream"], False)
        request = self.handler.create_transcription.call_args.args[1]
        self.assertEqual((request.model, request.language, request.prompt), ("asr", "zh", "names"))
        self.assertEqual(request.temperature, 0.25)
        self.assertTrue(request.file.file.closed)
        self.assertIsNone(self.handler.create_transcription.call_args.args[2])

    def test_text_and_verbose_json(self):
        response = self.post({"response_format": "text"})
        self.assertEqual(response.text, "hello")
        self.assertTrue(response.headers["content-type"].startswith("text/plain"))
        response = self.post({"response_format": "verbose_json"})
        self.assertEqual(response.json(), {"text": "hello"})

    def test_invalid_form_does_not_call_backend(self):
        for data in ({"temperature": "invalid"}, {"stream": "invalid"}, {"response_format": "invalid"}):
            with self.subTest(data=data):
                self.assertEqual(self.post(data).status_code, 422)
        self.assertEqual(self.client.post("/v1/audio/transcriptions", data={"file": "name"}).status_code, 422)
        self.assertEqual(self.client.post("/v1/audio/transcriptions", json={"model": "asr"}).status_code, 422)
        self.handle.create_transcription.remote.assert_not_called()

    def test_empty_oversize_and_exact_limit(self):
        self.assertEqual(self.post(audio=b"").status_code, 400)
        self.assertEqual(self.post(audio=b"x" * 1025).status_code, 400)
        self.handle.create_transcription.remote.assert_not_called()
        self.assertEqual(self.post(audio=b"x" * 1024).status_code, 200)

    def test_upstream_error_preserves_envelope_and_status_even_for_stream(self):
        error = {"error": {"message": "model not found", "type": "NotFoundError", "code": 404}}
        self.handler.create_transcription.return_value = Mock(model_dump=lambda: error)
        response = self.post({"stream": "true"})
        self.assertEqual(response.status_code, 404)
        self.assertEqual(response.json(), error)

    def test_validation_unsupported_model_and_internal_failure(self):
        for exc, status in ((ValueError("unsupported model"), 400), (HTTPException(422, "bad audio"), 422), (RuntimeError("private details"), 500)):
            with self.subTest(exc=exc):
                self.backend._ensure_transcription.side_effect = exc
                response = self.post()
                self.assertEqual(response.status_code, status)
                self.assertEqual(response.json()["error"]["code"], status)
                self.assertNotIn("private details", response.text)

    def test_stream_preserves_first_chunk_and_done(self):
        chunks = ['data: {"text":"first"}\n\n', 'data: {"text":"second"}\n\n', "data: [DONE]\n\n"]

        async def generate():
            for chunk in chunks:
                yield chunk

        self.handler.create_transcription.return_value = generate()
        response = self.post({"stream": "true"})
        self.assertEqual(response.status_code, 200)
        self.assertTrue(response.headers["content-type"].startswith("text/event-stream"))
        self.assertEqual(response.text, "".join(chunks))
        self.streams[-1].cancel.assert_called_once()

    def test_failure_after_stream_starts_is_an_sse_error(self):
        async def generate():
            yield "data: first\n\n"
            raise RuntimeError("private details")

        self.handler.create_transcription.return_value = generate()
        response = self.post({"stream": "true"})
        self.assertEqual(response.status_code, 200)
        self.assertTrue(response.text.startswith("data: first\n\n"))
        self.assertIn('"code": 500', response.text)
        self.assertTrue(response.text.endswith("data: [DONE]\n\n"))
        self.assertNotIn("private details", response.text)

    def test_ray_failure_before_first_result_is_500(self):
        async def generate():
            raise RuntimeError("ray failure")
            yield

        stream = RayStream(generate())
        self.handle.create_transcription.remote.side_effect = lambda *args: stream
        response = self.post()
        self.assertEqual(response.status_code, 500)
        stream.cancel.assert_called_once()

    def test_ray_failure_after_first_chunk_is_an_sse_error(self):
        async def generate():
            yield "data: first\n\n"
            raise RuntimeError("ray failure")

        stream = RayStream(generate())
        self.handle.create_transcription.remote.side_effect = lambda *args: stream
        response = self.post({"stream": "true"})
        self.assertEqual(response.status_code, 200)
        self.assertTrue(response.text.startswith("data: first\n\n"))
        self.assertIn('"code": 500', response.text)
        self.assertTrue(response.text.endswith("data: [DONE]\n\n"))
        self.assertNotIn("ray failure", response.text)
        stream.cancel.assert_called_once()


class UploadLifecycleTests(unittest.IsolatedAsyncioTestCase):
    async def test_controller_closes_file_after_rejection(self):
        for audio in (b"", b"too large"):
            with self.subTest(audio=audio):
                request = RequestModel(file=UploadFile(io.BytesIO(audio)))
                response = await transcription_response(Mock(), request, 1 / (1024 * 1024))
                self.assertEqual(response.status_code, 400)
                self.assertTrue(request.file.file.closed)

    async def test_request_cancellation_cancels_ray_generation(self):
        async def generate():
            raise asyncio.CancelledError()
            yield

        stream = RayStream(generate())
        handle = Mock()
        handle.options.return_value.create_transcription.remote.return_value = stream
        request = RequestModel(file=UploadFile(io.BytesIO(b"audio")))
        with self.assertRaises(asyncio.CancelledError):
            await transcription_response(handle, request, 1)
        stream.cancel.assert_called_once()
        self.assertTrue(request.file.file.closed)


if __name__ == "__main__":
    unittest.main()
