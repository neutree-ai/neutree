"""Tests for HuggingFaceDownloader GGUF file filtering.

Run with (from project root):
    PYTHONPATH=python python3 -m pytest python/neutree/downloader/test_huggingface.py -v
"""

import hashlib
import os
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

from neutree.downloader.huggingface import HuggingFaceDownloader  # noqa: E402


class TestHuggingFaceDownloaderGGUFFilter(unittest.TestCase):
    """Verify that allow_patterns passed to snapshot_download respects GGUF filtering."""

    def setUp(self):
        self.dest_dir = tempfile.mkdtemp()
        self.interactive_patcher = mock.patch("neutree.downloader.huggingface.is_interactive", return_value=True)
        self.interactive_patcher.start()

    def tearDown(self):
        self.interactive_patcher.stop()
        import shutil
        shutil.rmtree(self.dest_dir, ignore_errors=True)

    @mock.patch("neutree.downloader.huggingface.should_skip_verification", return_value=True)
    def test_gguf_pattern_passed_to_snapshot_download(self, _mock_skip):
        """GGUF pattern should be forwarded as allow_patterns."""
        dl = HuggingFaceDownloader()
        fake_hf = mock.MagicMock()
        with mock.patch.object(dl, "_ensure_hf", return_value=fake_hf):
            dl.download("org/model", self.dest_dir, metadata={"file": "*q4_0.gguf"})

        fake_hf.snapshot_download.assert_called_once()
        call_kwargs = fake_hf.snapshot_download.call_args
        self.assertEqual(call_kwargs.kwargs.get("allow_patterns") or call_kwargs[1].get("allow_patterns"), "*q4_0.gguf")

    @mock.patch("neutree.downloader.huggingface.should_skip_verification", return_value=True)
    def test_non_gguf_pattern_ignored(self, _mock_skip):
        """Non-GGUF pattern should result in allow_patterns=None."""
        dl = HuggingFaceDownloader()
        fake_hf = mock.MagicMock()
        with mock.patch.object(dl, "_ensure_hf", return_value=fake_hf):
            dl.download("org/model", self.dest_dir, metadata={"file": "*.safetensors"})

        fake_hf.snapshot_download.assert_called_once()
        _, kwargs = fake_hf.snapshot_download.call_args
        self.assertIsNone(kwargs.get("allow_patterns"))

    @mock.patch("neutree.downloader.huggingface.should_skip_verification", return_value=True)
    def test_empty_pattern_ignored(self, _mock_skip):
        """Empty file pattern should result in allow_patterns=None."""
        dl = HuggingFaceDownloader()
        fake_hf = mock.MagicMock()
        with mock.patch.object(dl, "_ensure_hf", return_value=fake_hf):
            dl.download("org/model", self.dest_dir, metadata={"file": ""})

        fake_hf.snapshot_download.assert_called_once()
        _, kwargs = fake_hf.snapshot_download.call_args
        self.assertIsNone(kwargs.get("allow_patterns"))

    @mock.patch("neutree.downloader.huggingface.should_skip_verification", return_value=True)
    @mock.patch("neutree.downloader.huggingface.is_interactive", return_value=False)
    @mock.patch("neutree.downloader.huggingface.ProgressReporter")
    def test_non_tty_disables_hf_progress_bars_and_wraps_download(self, mock_reporter, _mock_interactive, _mock_skip):
        """Non-TTY downloads should disable HF bars and use log-based progress."""
        dl = HuggingFaceDownloader()
        fake_hf = mock.MagicMock()
        fake_hf.utils.disable_progress_bars.return_value = None
        fake_hf.utils.are_progress_bars_disabled.return_value = False
        reporter_context = mock_reporter.return_value
        reporter_context.__enter__.return_value = reporter_context
        reporter_context.__exit__.return_value = False

        with mock.patch.object(dl, "_ensure_hf", return_value=fake_hf):
            dl.download("org/model", self.dest_dir, metadata={"file": ""})

        fake_hf.utils.disable_progress_bars.assert_called_once()
        fake_hf.utils.enable_progress_bars.assert_called_once()
        mock_reporter.assert_called_once()
        args, kwargs = mock_reporter.call_args
        self.assertEqual(args[0], self.dest_dir)
        self.assertEqual(kwargs["label"], "HuggingFace download")
        reporter_context.__enter__.assert_called_once()
        reporter_context.__exit__.assert_called_once()

    @mock.patch("neutree.downloader.huggingface.should_skip_verification", return_value=True)
    @mock.patch("neutree.downloader.huggingface.is_interactive", return_value=False)
    def test_non_tty_keeps_previously_disabled_hf_progress_bars_disabled(
            self, _mock_interactive, _mock_skip):
        """A non-TTY download should not re-enable bars that were already disabled."""
        dl = HuggingFaceDownloader()
        fake_hf = mock.MagicMock()
        fake_hf.utils.disable_progress_bars.return_value = None
        fake_hf.utils.are_progress_bars_disabled.return_value = True

        with mock.patch.object(dl, "_ensure_hf", return_value=fake_hf):
            dl.download("org/model", self.dest_dir, metadata={"file": ""})

        fake_hf.utils.disable_progress_bars.assert_called_once()
        fake_hf.utils.enable_progress_bars.assert_not_called()

    @mock.patch("neutree.downloader.huggingface.should_skip_verification", return_value=True)
    @mock.patch("neutree.downloader.huggingface.is_interactive", return_value=True)
    def test_tty_keeps_hf_progress_bars_enabled(self, _mock_interactive, _mock_skip):
        """Interactive downloads should leave upstream HF progress behavior intact."""
        dl = HuggingFaceDownloader()
        fake_hf = mock.MagicMock()

        with mock.patch.object(dl, "_ensure_hf", return_value=fake_hf):
            dl.download("org/model", self.dest_dir, metadata={"file": ""})

        fake_hf.utils.disable_progress_bars.assert_not_called()


if __name__ == "__main__":
    unittest.main()
