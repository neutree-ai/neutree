"""Tests for non-TTY downloader progress reporting."""

import hashlib
import os
import shutil
import sys
import tempfile
import types
import unittest
from unittest import mock

_fake_sha = types.ModuleType("huggingface_hub.utils.sha")
_fake_sha.git_hash = lambda data: ""
_fake_sha.sha_fileobj = lambda stream, bufsize=0: hashlib.sha256(stream.read()).digest()
sys.modules.setdefault("huggingface_hub", types.ModuleType("huggingface_hub"))
sys.modules.setdefault("huggingface_hub.utils", types.ModuleType("huggingface_hub.utils"))
sys.modules.setdefault("huggingface_hub.utils.sha", _fake_sha)
_fake_hf_api = types.ModuleType("huggingface_hub.hf_api")
_fake_hf_api.RepoFile = type("RepoFile", (), {})
sys.modules.setdefault("huggingface_hub.hf_api", _fake_hf_api)

from neutree.downloader.progress import (  # noqa: E402
    ProgressReporter,
    format_size,
    get_dir_size,
    is_interactive,
)


class _Stream:
    def __init__(self, tty):
        self._tty = tty

    def isatty(self):
        return self._tty


class TestProgressHelpers(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()

    def tearDown(self):
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def test_is_interactive_uses_stdout_or_stderr_tty(self):
        with mock.patch.object(sys, "stdout", _Stream(True)), mock.patch.object(sys, "stderr", _Stream(False)):
            self.assertTrue(is_interactive())

        with mock.patch.object(sys, "stdout", _Stream(False)), mock.patch.object(sys, "stderr", _Stream(False)):
            self.assertFalse(is_interactive())

    def test_format_size(self):
        self.assertEqual(format_size(0), "0 B")
        self.assertEqual(format_size(512), "512 B")
        self.assertEqual(format_size(1024), "1.0 KiB")
        self.assertEqual(format_size(1536), "1.5 KiB")
        self.assertEqual(format_size(1024 * 1024), "1.0 MiB")

    def test_get_dir_size_handles_files_directories_and_missing_paths(self):
        os.makedirs(os.path.join(self.tmpdir, "nested"))
        with open(os.path.join(self.tmpdir, "a.bin"), "wb") as f:
            f.write(b"a" * 3)
        with open(os.path.join(self.tmpdir, "nested", "b.bin"), "wb") as f:
            f.write(b"b" * 5)

        self.assertEqual(get_dir_size(os.path.join(self.tmpdir, "a.bin")), 3)
        self.assertEqual(get_dir_size(self.tmpdir), 8)
        self.assertEqual(get_dir_size(os.path.join(self.tmpdir, "missing")), 0)

    def test_interval_defaults_and_validation(self):
        logger = mock.Mock()

        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(ProgressReporter(self.tmpdir, logger, interactive=False).interval, 30.0)

        with mock.patch.dict(os.environ, {"NEUTREE_DL_PROGRESS_INTERVAL": "0.2"}, clear=True):
            self.assertEqual(ProgressReporter(self.tmpdir, logger, interactive=False).interval, 1.0)

        for value in ("0", "-5", "nan", "inf", "bad"):
            with mock.patch.dict(os.environ, {"NEUTREE_DL_PROGRESS_INTERVAL": value}, clear=True):
                self.assertEqual(ProgressReporter(self.tmpdir, logger, interactive=False).interval, 30.0)

        self.assertEqual(ProgressReporter(self.tmpdir, logger, interval=float("inf"), interactive=False).interval, 30.0)


class TestProgressReporter(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.logger = mock.Mock()

    def tearDown(self):
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def test_reporter_noops_in_interactive_shell(self):
        reporter = ProgressReporter(self.tmpdir, self.logger, label="Test download", interactive=True)

        with reporter:
            with open(os.path.join(self.tmpdir, "model.bin"), "wb") as f:
                f.write(b"content")

        self.logger.info.assert_not_called()

    def test_reporter_logs_completion_on_normal_exit(self):
        with ProgressReporter(self.tmpdir, self.logger, label="Test download", interactive=False):
            with open(os.path.join(self.tmpdir, "model.bin"), "wb") as f:
                f.write(b"content")

        messages = [call.args[0] for call in self.logger.info.call_args_list]
        self.assertTrue(any("Test download progress" in message for message in messages))
        self.assertTrue(any("Test download completed" in message for message in messages))

    def test_reporter_logs_aborted_on_exception(self):
        with self.assertRaisesRegex(RuntimeError, "boom"):
            with ProgressReporter(self.tmpdir, self.logger, label="Test download", interactive=False):
                raise RuntimeError("boom")

        messages = [call.args[0] for call in self.logger.info.call_args_list]
        self.assertTrue(any("Test download aborted" in message for message in messages))
        self.assertFalse(any("Test download completed" in message for message in messages))


if __name__ == "__main__":
    unittest.main()
