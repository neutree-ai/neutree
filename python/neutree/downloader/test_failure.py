"""Tests for failure reporting out of the model-downloader init container.

Run with (from project root):
    PYTHONPATH=python python3 -m pytest python/neutree/downloader/test_failure.py -v
"""

import os
import tempfile
import unittest
from unittest import mock

from neutree.downloader.failure import (
    MAX_MESSAGE_BYTES,
    TERMINATION_LOG_ENV,
    build_failure_message,
    describe_exception,
    sanitize,
    write_termination_message,
)


class TestSanitize(unittest.TestCase):
    def test_redacts_authorization_header(self):
        cleaned = sanitize("failed: {'Authorization': 'Bearer ms-8f2c1d9e4b7a'} rejected")
        self.assertNotIn("ms-8f2c1d9e4b7a", cleaned)
        self.assertIn("<redacted>", cleaned)

    def test_redacts_bearer_token_in_prose(self):
        cleaned = sanitize("sent Bearer hf_AbCdEfGhIjKlMnOpQrSt to the hub")
        self.assertNotIn("hf_AbCdEfGhIjKlMnOpQrSt", cleaned)

    def test_redacts_signed_url_query_parameters(self):
        cleaned = sanitize(
            "HTTP 403 fetching https://cdn.modelscope.cn/model.safetensors"
            "?Signature=abc123def&X-Amz-Security-Token=zzz&Expires=1700000000"
        )
        self.assertNotIn("abc123def", cleaned)
        self.assertNotIn("zzz", cleaned)
        # Non-credential parameters survive: they are part of the diagnosis.
        self.assertIn("Expires=1700000000", cleaned)

    def test_redacts_token_environment_values_even_when_shapeless(self):
        with mock.patch.dict(os.environ, {"MODELSCOPE_API_TOKEN": "plain-secret-value"}):
            cleaned = sanitize("hub refused the credential plain-secret-value")

        self.assertNotIn("plain-secret-value", cleaned)

    def test_keeps_the_actionable_text(self):
        message = ("runtime model download is unavailable: offline mode is enabled, so the weights "
                   "for ModelScope model 'Qwen/Qwen3-8B' cannot be fetched from the hub")
        self.assertEqual(sanitize(message), message)


class TestDescribeException(unittest.TestCase):
    def test_reports_the_cause_chain(self):
        try:
            try:
                raise OSError("[Errno -3] Temporary failure in name resolution")
            except OSError as inner:
                raise RuntimeError("runtime model download is unavailable") from inner
        except RuntimeError as exc:
            described = describe_exception(exc)

        self.assertIn("runtime model download is unavailable", described)
        self.assertIn("name resolution", described)

    def test_does_not_repeat_a_cause_already_quoted(self):
        try:
            try:
                raise OSError("connection refused")
            except OSError as inner:
                raise RuntimeError("cannot reach the hub (connection refused)") from inner
        except RuntimeError as exc:
            described = describe_exception(exc)

        self.assertEqual(described.count("connection refused"), 1)

    def test_collapses_newlines(self):
        described = describe_exception(RuntimeError("first line\nsecond line"))
        self.assertEqual(described, "first line second line")


class TestBuildFailureMessage(unittest.TestCase):
    def test_names_the_model_and_registry(self):
        message = build_failure_message(
            RuntimeError("offline mode is enabled and no files are present at /models-cache"),
            model_name="Qwen/Qwen3-8B", registry_type="model-scope")

        self.assertIn("model-scope model 'Qwen/Qwen3-8B'", message)
        self.assertIn("offline mode is enabled", message)

    def test_truncates_to_fit_the_termination_message_limit(self):
        message = build_failure_message(RuntimeError("x" * (MAX_MESSAGE_BYTES * 2)),
                                        model_name="m", registry_type="model-scope")

        self.assertLessEqual(len(message.encode("utf-8")), MAX_MESSAGE_BYTES + 64)
        self.assertTrue(message.endswith("...(truncated)"))

    def test_truncation_stays_decodable_with_multibyte_text(self):
        message = build_failure_message(RuntimeError("模型" * MAX_MESSAGE_BYTES),
                                        model_name="m", registry_type="model-scope")

        # Round-trips: the cut landed on a character boundary, not inside one.
        self.assertEqual(message, message.encode("utf-8").decode("utf-8"))

    def test_sanitizes_before_returning(self):
        with mock.patch.dict(os.environ, {"HF_TOKEN": "hf_supersecrettoken"}):
            message = build_failure_message(RuntimeError("401 with hf_supersecrettoken"),
                                            model_name="m", registry_type="hugging-face")

        self.assertNotIn("hf_supersecrettoken", message)


class TestWriteTerminationMessage(unittest.TestCase):
    def test_writes_to_the_configured_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "termination-log")
            with mock.patch.dict(os.environ, {TERMINATION_LOG_ENV: path}):
                write_termination_message("runtime model download is unavailable")

            with open(path, encoding="utf-8") as handle:
                self.assertEqual(handle.read(), "runtime model download is unavailable")

    def test_unwritable_path_does_not_raise(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "missing-dir", "termination-log")
            with mock.patch.dict(os.environ, {TERMINATION_LOG_ENV: path}):
                write_termination_message("reason")


if __name__ == "__main__":
    unittest.main()
