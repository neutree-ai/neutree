"""Run the actual versioned methods with Ray/vLLM replaced by strict mocks."""

import ast
import copy
from pathlib import Path
import sys
from types import SimpleNamespace
from typing import Annotated, Any
import unittest
from unittest.mock import AsyncMock, Mock, patch

from fastapi import FastAPI, Form, UploadFile
from fastapi.testclient import TestClient
from pydantic import BaseModel

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from serve._utils.vllm_transcription import transcription_response, transcription_results


class RequestModel(BaseModel):
    file: UploadFile
    model: str | None = None
    response_format: str = "json"


VERSIONS = ("v0_17_1", "v0_24_0", "v0_31_0")


def load_methods(version):
    path = Path(__file__).parent / version / "app.py"
    tree = ast.parse(path.read_text(encoding="utf-8"))
    namespace = dict(
        Annotated=Annotated, Any=Any, Form=Form, TranscriptionRequest=RequestModel,
        transcription_response=transcription_response, transcription_results=transcription_results,
        envs=SimpleNamespace(VLLM_MAX_AUDIO_CLIP_FILESIZE_MB=1),
    )
    for class_name, method_names in (
        ("Backend", ("_ensure_transcription", "create_transcription")),
        ("Controller", ("transcriptions",)),
    ):
        cls = copy.deepcopy(next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == class_name))
        cls.decorator_list = []
        cls.body = [node for node in cls.body if isinstance(node, ast.AsyncFunctionDef) and node.name in method_names]
        for node in cls.body:
            node.decorator_list = []
        exec(compile(ast.Module(body=[cls], type_ignores=[]), str(path), "exec"), namespace)
    return tree, namespace["Backend"](), namespace["Controller"]()


class VersionedTranscriptionTests(unittest.IsolatedAsyncioTestCase):
    async def test_lazy_constructor_matches_pinned_upstream_signatures(self):
        # Signatures checked against vLLM's tagged serving.py files; no GPU imports.
        for version in VERSIONS:
            with self.subTest(version=version):
                _, backend, _ = load_methods(version)
                handler = Mock(create_transcription=AsyncMock(return_value=RequestResult(text="hello")))
                calls = []

                def current(engine_client, models, *, request_logger,
                            return_tokens_as_token_ids=False, enable_force_include_usage=False):
                    calls.append((engine_client, models, request_logger))
                    return handler

                def old(engine_client, models, *, request_logger,
                        return_tokens_as_token_ids=False, enable_force_include_usage=False,
                        log_error_stack=False):
                    return current(engine_client, models, request_logger=request_logger)

                prefix = "vllm.entrypoints.openai.speech_to_text" if version == "v0_17_1" else "vllm.entrypoints.speech_to_text.transcription"
                module = SimpleNamespace(OpenAIServingTranscription=old if version == "v0_17_1" else current)
                backend.openai_serving_transcription = None
                backend.engine = Mock(get_supported_tasks=AsyncMock(return_value=("transcription",)))
                backend._ensure_supported_tasks = AsyncMock(return_value=("transcription",))
                backend._ensure_models = AsyncMock(return_value="models")
                backend.return_tokens_as_token_ids = False
                backend.enable_force_include_usage = False
                backend.log_error_stack = False
                with patch.dict(sys.modules, {prefix + ".serving": module}):
                    self.assertIs(await backend._ensure_transcription(), handler)
                    self.assertIs(await backend._ensure_transcription(), handler)
                    results = [item async for item in backend.create_transcription(b"audio", {"model": "asr"})]
                self.assertEqual(len(calls), 1)
                self.assertEqual(results, [{"text": "hello"}])
                args = handler.create_transcription.call_args.args
                self.assertEqual(args[0], b"audio")
                self.assertEqual(args[1].model, "asr")
                self.assertIsNone(args[2])
                self.assertTrue(args[1].file.file.closed)

    async def test_non_asr_model_is_rejected_before_loading_serving(self):
        for version in VERSIONS:
            with self.subTest(version=version):
                _, backend, _ = load_methods(version)
                backend.openai_serving_transcription = None
                backend.engine = Mock(get_supported_tasks=AsyncMock(return_value=("generate",)))
                backend._ensure_supported_tasks = AsyncMock(return_value=("generate",))
                results = [item async for item in backend.create_transcription(b"audio", {})]
                self.assertEqual(results[0]["error"]["code"], 400)
                self.assertIn("does not support", results[0]["error"]["message"])

    async def test_each_controller_route_accepts_multipart_and_forwards(self):
        for version in VERSIONS:
            with self.subTest(version=version):
                tree, _, controller = load_methods(version)
                route = next(node for node in ast.walk(tree) if isinstance(node, ast.AsyncFunctionDef) and node.name == "transcriptions")
                self.assertEqual(route.decorator_list[0].args[0].value, "/v1/audio/transcriptions")
                self.assertIn("Form()", ast.unparse(route.args.args[1].annotation))
                expected = "vllm.entrypoints.openai.speech_to_text.protocol" if version == "v0_17_1" else "vllm.entrypoints.speech_to_text.transcription.protocol"
                self.assertTrue(any(isinstance(node, ast.ImportFrom) and node.module == expected and any(name.name == "TranscriptionRequest" for name in node.names) for node in tree.body))

                async def generate(*args):
                    yield {"text": "hello"}

                handle = Mock()
                handle.options.return_value.create_transcription.remote.side_effect = generate
                controller.backend = handle
                app = FastAPI()
                app.post("/v1/audio/transcriptions")(controller.transcriptions)
                with TestClient(app) as client:
                    response = client.post("/v1/audio/transcriptions", data={"model": "asr"}, files={"file": ("clip.wav", b"audio")})
                self.assertEqual(response.status_code, 200)
                self.assertEqual(response.json(), {"text": "hello"})
                audio, payload = handle.options.return_value.create_transcription.remote.call_args.args
                self.assertEqual(audio, b"audio")
                self.assertEqual(payload["model"], "asr")
                self.assertNotIn("file", payload)


class RequestResult(BaseModel):
    text: str


if __name__ == "__main__":
    unittest.main()
