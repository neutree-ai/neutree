"""Tests for the downloader CLI entrypoint's failure path.

Run with (from project root):
    PYTHONPATH=python python3 -m pytest python/neutree/downloader/test_main.py -v
"""

import os
import tempfile
import unittest
from unittest import mock

from neutree.downloader import __main__ as downloader_main
from neutree.downloader.failure import TERMINATION_LOG_ENV


class TestMainFailureReporting(unittest.TestCase):
    """A failed download must leave its reason where kubelet will read it.

    Without this the endpoint's status ends at "terminated with exit code 1
    after 5 restarts:" — the failure NEU-726 exists to remove.
    """

    argv = ["--name", "Qwen/Qwen3-8B", "--registry_type", "model-scope", "--path", "/models-cache"]

    def _run_with_failure(self, exc, tmp_path):
        with mock.patch.dict(os.environ, {TERMINATION_LOG_ENV: tmp_path}), \
                mock.patch.object(downloader_main, "_run", side_effect=exc):
            with self.assertRaises(SystemExit) as raised:
                downloader_main.main(self.argv)

        self.assertEqual(raised.exception.code, 1)

        with open(tmp_path, encoding="utf-8") as handle:
            return handle.read()

    def test_records_the_offline_reason(self):
        reason = ("runtime model download is unavailable: offline mode is enabled, so the weights "
                  "cannot be fetched from the hub, and none are present at /models-cache")

        with tempfile.TemporaryDirectory() as tmp:
            written = self._run_with_failure(RuntimeError(reason), os.path.join(tmp, "log"))

        self.assertIn("model-scope model 'Qwen/Qwen3-8B'", written)
        self.assertIn("offline mode is enabled", written)
        self.assertIn("none are present at /models-cache", written)

    def test_redacts_credentials_from_the_recorded_reason(self):
        with tempfile.TemporaryDirectory() as tmp:
            with mock.patch.dict(os.environ, {"MODELSCOPE_API_TOKEN": "ms-1a2b3c4d5e6f"}):
                written = self._run_with_failure(
                    RuntimeError("HTTP 401 with Authorization: Bearer ms-1a2b3c4d5e6f"),
                    os.path.join(tmp, "log"))

        self.assertNotIn("ms-1a2b3c4d5e6f", written)
        self.assertIn("<redacted>", written)

    def test_success_writes_nothing(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "log")
            with mock.patch.dict(os.environ, {TERMINATION_LOG_ENV: path}), \
                    mock.patch.object(downloader_main, "_run"):
                downloader_main.main(self.argv)

            self.assertFalse(os.path.exists(path))


if __name__ == "__main__":
    unittest.main()
