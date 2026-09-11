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
from neutree.downloader.utils import CREDENTIAL_ENV_VARS


class TestSanitize(unittest.TestCase):
    """Redaction is exact-value, so it is rendering-independent by construction.

    These cases exist to hold that property, not to enumerate spellings: if the
    check ever became pattern-based again, the dict and header renderings below
    are the first ones to slip through it.
    """

    def test_redacts_the_token_however_it_is_rendered(self):
        renderings = (
            "HTTP 401 with Authorization: Bearer {tok}",
            "{{'Authorization': 'Bearer {tok}'}} was rejected",
            '{{"headers": {{"authorization": "Basic {tok}"}}}}',
            "hub refused the credential {tok}",
            "https://www.modelscope.cn/api/v1/models?token={tok}",
        )

        with mock.patch.dict(os.environ, {"MODELSCOPE_API_TOKEN": "ms-8f2c1d9e4b7a"}):
            for rendering in renderings:
                with self.subTest(rendering=rendering):
                    cleaned = sanitize(rendering.format(tok="ms-8f2c1d9e4b7a"))
                    self.assertNotIn("ms-8f2c1d9e4b7a", cleaned)
                    self.assertIn("<redacted>", cleaned)

    def test_redacts_a_token_of_no_particular_shape(self):
        """The point of matching on value: this one looks like nothing."""
        with mock.patch.dict(os.environ, {"NEUTREE_DL_TOKEN": "plain-secret-value"}):
            cleaned = sanitize("hub refused the credential plain-secret-value")

        self.assertNotIn("plain-secret-value", cleaned)

    def test_covers_every_variable_a_credential_can_arrive_in(self):
        for name in CREDENTIAL_ENV_VARS:
            with self.subTest(env=name):
                with mock.patch.dict(os.environ, {name: "the-secret-value"}, clear=True):
                    self.assertNotIn("the-secret-value", sanitize("rejected the-secret-value"))

    def test_leaves_a_message_with_no_credential_in_it_alone(self):
        message = ("runtime model download is unavailable: offline mode is enabled, so the weights "
                   "for ModelScope model 'Qwen/Qwen3-8B' cannot be fetched from the hub")

        with mock.patch.dict(os.environ, {"MODELSCOPE_API_TOKEN": "ms-8f2c1d9e4b7a"}):
            self.assertEqual(sanitize(message), message)

    def test_ignores_a_token_too_short_to_tell_from_prose(self):
        """Blanking a 2-character value would corrupt the message, not protect it."""
        with mock.patch.dict(os.environ, {"HF_TOKEN": "ab"}):
            self.assertEqual(sanitize("unable to grab the weights"), "unable to grab the weights")


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
