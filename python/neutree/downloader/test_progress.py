"""Tests for the progress reporting module.

Run with (from project root):
    PYTHONPATH=python python3 -m pytest python/neutree/downloader/test_progress.py -v
"""

import hashlib
import logging
import os
import shutil
import sys
import tempfile
import types
import unittest
from unittest import mock

# Provide stub huggingface_hub modules when the real package is absent.
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


class TestIsInteractive(unittest.TestCase):
    def test_returns_true_when_tty(self):
        with mock.patch.object(sys, "stderr") as mock_stderr:
            mock_stderr.isatty.return_value = True
            self.assertTrue(is_interactive())

    def test_returns_false_when_not_tty(self):
        with mock.patch.object(sys, "stderr") as mock_stderr:
            mock_stderr.isatty.return_value = False
            self.assertFalse(is_interactive())

    def test_returns_false_when_no_isatty(self):
        with mock.patch.object(sys, "stderr", new=object()):
            self.assertFalse(is_interactive())


class TestFormatSize(unittest.TestCase):
    def test_zero(self):
        self.assertEqual(format_size(0), "0.00 B")

    def test_bytes(self):
        self.assertEqual(format_size(500), "500.00 B")

    def test_kilobytes(self):
        self.assertEqual(format_size(1024), "1.00 KB")

    def test_megabytes(self):
        self.assertEqual(format_size(5 * 1024 * 1024), "5.00 MB")

    def test_gigabytes(self):
        result = format_size(2.5 * 1024 ** 3)
        self.assertEqual(result, "2.50 GB")

    def test_terabytes(self):
        result = format_size(1024 ** 4)
        self.assertEqual(result, "1.00 TB")


class TestGetDirSize(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()

    def tearDown(self):
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def test_known_files(self):
        with open(os.path.join(self.tmpdir, "a.bin"), "wb") as f:
            f.write(b"x" * 100)
        with open(os.path.join(self.tmpdir, "b.bin"), "wb") as f:
            f.write(b"y" * 200)
        self.assertEqual(get_dir_size(self.tmpdir), 300)

    def test_nested_files(self):
        sub = os.path.join(self.tmpdir, "sub")
        os.makedirs(sub)
        with open(os.path.join(sub, "c.bin"), "wb") as f:
            f.write(b"z" * 50)
        self.assertEqual(get_dir_size(self.tmpdir), 50)

    def test_empty_dir(self):
        self.assertEqual(get_dir_size(self.tmpdir), 0)

    def test_nonexistent_path(self):
        self.assertEqual(get_dir_size("/nonexistent/path/abc123"), 0)


class TestProgressReporter(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self.logger = logging.getLogger("test.progress")
        self.logger.setLevel(logging.DEBUG)

    def tearDown(self):
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    @mock.patch("neutree.downloader.progress.is_interactive", return_value=True)
    def test_noop_in_tty(self, _mock_interactive):
        reporter = ProgressReporter(self.tmpdir, self.logger, interval=0.1)
        with reporter:
            pass
        self.assertFalse(reporter._active)
        self.assertIsNone(reporter._thread)

    @mock.patch("neutree.downloader.progress.is_interactive", return_value=False)
    def test_starts_and_stops_thread(self, _mock_interactive):
        reporter = ProgressReporter(self.tmpdir, self.logger, interval=0.5)
        with reporter:
            self.assertTrue(reporter._active)
            self.assertTrue(reporter._thread.is_alive())
        # Thread should be stopped after exit
        self.assertFalse(reporter._thread.is_alive())

    @mock.patch("neutree.downloader.progress.is_interactive", return_value=False)
    def test_logs_progress(self, _mock_interactive):
        # Write a file so there's something to report
        with open(os.path.join(self.tmpdir, "data.bin"), "wb") as f:
            f.write(b"x" * 1024)

        with mock.patch.object(self.logger, "info") as mock_info:
            import time
            with ProgressReporter(self.tmpdir, self.logger, interval=0.3):
                time.sleep(0.5)
            # Should have at least the final summary log
            self.assertTrue(mock_info.called)
            # Check that at least one call contains "progress" or "completed"
            calls = [str(c) for c in mock_info.call_args_list]
            has_progress = any("progress" in c or "completed" in c for c in calls)
            self.assertTrue(has_progress, f"Expected progress/completed in logs, got: {calls}")

    @mock.patch("neutree.downloader.progress.is_interactive", return_value=False)
    def test_logs_percentage_with_total_size(self, _mock_interactive):
        with open(os.path.join(self.tmpdir, "data.bin"), "wb") as f:
            f.write(b"x" * 512)

        with mock.patch.object(self.logger, "info") as mock_info:
            import time
            with ProgressReporter(self.tmpdir, self.logger, total_size=1024, interval=0.3):
                time.sleep(0.5)
            calls = [str(c) for c in mock_info.call_args_list]
            has_pct = any("%" in c for c in calls)
            self.assertTrue(has_pct, f"Expected percentage in logs, got: {calls}")

    def test_interval_default(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("NEUTREE_DL_PROGRESS_INTERVAL", None)
            reporter = ProgressReporter(self.tmpdir, self.logger)
            self.assertEqual(reporter._interval, 30.0)

    def test_interval_from_env(self):
        with mock.patch.dict(os.environ, {"NEUTREE_DL_PROGRESS_INTERVAL": "15"}):
            reporter = ProgressReporter(self.tmpdir, self.logger)
            self.assertEqual(reporter._interval, 15.0)

    def test_interval_clamped_to_minimum(self):
        with mock.patch.dict(os.environ, {"NEUTREE_DL_PROGRESS_INTERVAL": "0.1"}):
            reporter = ProgressReporter(self.tmpdir, self.logger)
            self.assertEqual(reporter._interval, 1.0)

    def test_interval_invalid_env_uses_default(self):
        with mock.patch.dict(os.environ, {"NEUTREE_DL_PROGRESS_INTERVAL": "invalid"}):
            reporter = ProgressReporter(self.tmpdir, self.logger)
            self.assertEqual(reporter._interval, 30.0)


if __name__ == "__main__":
    unittest.main()
