"""Tests for model download marker logging."""

import contextlib
import hashlib
import io
import sys
import types
import unittest

_fake_sha = types.ModuleType("huggingface_hub.utils.sha")
_fake_sha.git_hash = lambda data: ""
_fake_sha.sha_fileobj = lambda stream, bufsize=0: hashlib.sha256(stream.read()).digest()
sys.modules.setdefault("huggingface_hub", types.ModuleType("huggingface_hub"))
sys.modules.setdefault("huggingface_hub.utils", types.ModuleType("huggingface_hub.utils"))
sys.modules.setdefault("huggingface_hub.utils.sha", _fake_sha)
_fake_hf_api = types.ModuleType("huggingface_hub.hf_api")
_fake_hf_api.RepoFile = type("RepoFile", (), {})
sys.modules.setdefault("huggingface_hub.hf_api", _fake_hf_api)

from neutree.downloader import download_with_markers  # noqa: E402


class FakeDownloader:
    def __init__(self, error=None):
        self.error = error
        self.calls = []

    def download(self, *args, **kwargs):
        self.calls.append((args, kwargs))
        if self.error:
            raise self.error


class TestDownloadMarkers(unittest.TestCase):
    def test_download_with_markers_prints_start_and_done(self):
        downloader = FakeDownloader()
        output = io.StringIO()

        with contextlib.redirect_stdout(output):
            download_with_markers(
                downloader,
                "source",
                "/dest",
                credentials={"token": "secret"},
                recursive=False,
                overwrite=True,
                retries=2,
                timeout=1.5,
                metadata={"file": "model.gguf"},
            )

        self.assertEqual(
            output.getvalue().splitlines(),
            ["NEUTREE_MODEL_DOWNLOAD_START", "NEUTREE_MODEL_DOWNLOAD_DONE"],
        )
        self.assertEqual(len(downloader.calls), 1)
        args, kwargs = downloader.calls[0]
        self.assertEqual(args, ("source", "/dest"))
        self.assertEqual(kwargs["credentials"], {"token": "secret"})
        self.assertFalse(kwargs["recursive"])
        self.assertTrue(kwargs["overwrite"])
        self.assertEqual(kwargs["retries"], 2)
        self.assertEqual(kwargs["timeout"], 1.5)
        self.assertEqual(kwargs["metadata"], {"file": "model.gguf"})

    def test_download_with_markers_prints_failed_and_reraises(self):
        downloader = FakeDownloader(RuntimeError("download failed"))
        output = io.StringIO()

        with self.assertRaisesRegex(RuntimeError, "download failed"):
            with contextlib.redirect_stdout(output):
                download_with_markers(downloader, "source", "/dest")

        self.assertEqual(
            output.getvalue().splitlines(),
            ["NEUTREE_MODEL_DOWNLOAD_START", "NEUTREE_MODEL_DOWNLOAD_FAILED"],
        )


if __name__ == "__main__":
    unittest.main()
