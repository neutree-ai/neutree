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

    def tearDown(self):
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


if __name__ == "__main__":
    unittest.main()
