"""Tests for LocalDownloader verification.

Run with (from project root):
    PYTHONPATH=python python3 -m pytest python/neutree/downloader/test_local.py -v

When huggingface_hub is not installed locally, use the helper runner:
    cd <project-root> && PYTHONPATH=python python3 -c "
    import sys, hashlib, types
    m = types.ModuleType; s = m('huggingface_hub.utils.sha')
    s.git_hash = lambda d: ''; s.sha_fileobj = lambda st, b=0: hashlib.sha256(st.read()).digest()
    for k,v in {'huggingface_hub': m('huggingface_hub'), 'huggingface_hub.utils': m('huggingface_hub.utils'),
                 'huggingface_hub.utils.sha': s, 'huggingface_hub.hf_api': m('huggingface_hub.hf_api')}.items():
        sys.modules.setdefault(k, v)
    import unittest; unittest.main(module='neutree.downloader.test_local', verbosity=2)
    "
"""

import hashlib
import json
import os
import shutil
import sys
import tempfile
import types
import unittest
from unittest import mock

# Provide stub huggingface_hub modules when the real package is absent so
# that the import chain (downloader.__init__ -> utils -> huggingface_hub)
# does not fail.  Uses setdefault so that the real package wins when present.
_fake_sha = types.ModuleType("huggingface_hub.utils.sha")
_fake_sha.git_hash = lambda data: ""
_fake_sha.sha_fileobj = lambda stream, bufsize=0: hashlib.sha256(stream.read()).digest()
sys.modules.setdefault("huggingface_hub", types.ModuleType("huggingface_hub"))
sys.modules.setdefault("huggingface_hub.utils", types.ModuleType("huggingface_hub.utils"))
sys.modules.setdefault("huggingface_hub.utils.sha", _fake_sha)
_fake_hf_api = types.ModuleType("huggingface_hub.hf_api")
_fake_hf_api.RepoFile = type("RepoFile", (), {})
sys.modules.setdefault("huggingface_hub.hf_api", _fake_hf_api)

from neutree.downloader.local import LocalDownloader  # noqa: E402


def _compute_sha256_pure(path):
    """Compute SHA256 without depending on huggingface_hub."""
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(8192), b""):
            h.update(chunk)
    return h.hexdigest()


def _write_checksum(checksums_dir, file_relpath, algorithm, hash_val):
    record_path = os.path.join(checksums_dir, file_relpath + ".json")
    os.makedirs(os.path.dirname(record_path), exist_ok=True)
    with open(record_path, "w") as f:
        json.dump({"algorithm": algorithm, "hash": hash_val}, f)


class TestLocalDownloaderVerification(unittest.TestCase):
    def setUp(self):
        self.src_dir = tempfile.mkdtemp()
        self.dest_dir = tempfile.mkdtemp()

        # Create source files
        self.model_content = b"fake model weights data"
        self.config_content = b'{"model_type": "test"}'
        with open(os.path.join(self.src_dir, "model.bin"), "wb") as f:
            f.write(self.model_content)
        with open(os.path.join(self.src_dir, "config.json"), "wb") as f:
            f.write(self.config_content)

    def tearDown(self):
        shutil.rmtree(self.src_dir, ignore_errors=True)
        shutil.rmtree(self.dest_dir, ignore_errors=True)

    def _add_correct_checksums(self):
        checksums_dir = os.path.join(self.src_dir, ".neutree", "checksums")
        _write_checksum(checksums_dir, "model.bin", "sha256",
                        _compute_sha256_pure(os.path.join(self.src_dir, "model.bin")))
        _write_checksum(checksums_dir, "config.json", "sha256",
                        _compute_sha256_pure(os.path.join(self.src_dir, "config.json")))

    def _add_wrong_checksum(self, filename):
        checksums_dir = os.path.join(self.src_dir, ".neutree", "checksums")
        _write_checksum(checksums_dir, filename, "sha256", "0" * 64)

    @mock.patch("neutree.downloader.local.should_skip_verification", return_value=False)
    def test_verification_passes_with_correct_checksums(self, _mock_skip):
        self._add_correct_checksums()

        dl = LocalDownloader()
        dl.download(self.src_dir, self.dest_dir, metadata={"file": ""})

        # Files should exist
        self.assertTrue(os.path.exists(os.path.join(self.dest_dir, "model.bin")))
        self.assertTrue(os.path.exists(os.path.join(self.dest_dir, "config.json")))

        # Verify cache should exist
        verify_dir = os.path.join(self.dest_dir, ".neutree", "verify")
        self.assertTrue(os.path.exists(os.path.join(verify_dir, "model.bin.json")))
        self.assertTrue(os.path.exists(os.path.join(verify_dir, "config.json.json")))

        # Check verify record content
        with open(os.path.join(verify_dir, "model.bin.json")) as f:
            rec = json.load(f)
        self.assertTrue(rec["passed"])
        self.assertEqual(rec["algorithm"], "sha256")
        self.assertEqual(rec["expected_hash"], rec["actual_hash"])

    @mock.patch("neutree.downloader.local.should_skip_verification", return_value=False)
    @mock.patch("neutree.downloader.local.should_keep_failed_files", return_value=False)
    def test_verification_fails_and_deletes_corrupt_file(self, _mock_keep, _mock_skip):
        self._add_correct_checksums()
        self._add_wrong_checksum("model.bin")  # Override with wrong hash

        dl = LocalDownloader()
        with self.assertRaises(RuntimeError) as ctx:
            dl.download(self.src_dir, self.dest_dir, metadata={"file": ""})

        self.assertIn("model.bin", str(ctx.exception))
        # Corrupt file should be deleted
        self.assertFalse(os.path.exists(os.path.join(self.dest_dir, "model.bin")))
        # Good file should still exist
        self.assertTrue(os.path.exists(os.path.join(self.dest_dir, "config.json")))

    @mock.patch("neutree.downloader.local.should_skip_verification", return_value=False)
    @mock.patch("neutree.downloader.local.should_keep_failed_files", return_value=True)
    def test_verification_keeps_failed_files_when_configured(self, _mock_keep, _mock_skip):
        self._add_correct_checksums()
        self._add_wrong_checksum("model.bin")

        dl = LocalDownloader()
        with self.assertRaises(RuntimeError):
            dl.download(self.src_dir, self.dest_dir, metadata={"file": ""})

        # File should be kept for debugging
        self.assertTrue(os.path.exists(os.path.join(self.dest_dir, "model.bin")))

    @mock.patch("neutree.downloader.local.should_skip_verification", return_value=False)
    def test_no_checksums_skips_verification(self, _mock_skip):
        # No .neutree/checksums/ in source — should not raise
        dl = LocalDownloader()
        dl.download(self.src_dir, self.dest_dir, metadata={"file": ""})

        self.assertTrue(os.path.exists(os.path.join(self.dest_dir, "model.bin")))
        self.assertFalse(os.path.isdir(os.path.join(self.dest_dir, ".neutree", "verify")))

    @mock.patch("neutree.downloader.local.should_skip_verification", return_value=True)
    def test_skip_verification_env(self, _mock_skip):
        self._add_correct_checksums()
        self._add_wrong_checksum("model.bin")  # Wrong, but should be ignored

        dl = LocalDownloader()
        # Should not raise even with wrong checksum
        dl.download(self.src_dir, self.dest_dir, metadata={"file": ""})

        self.assertTrue(os.path.exists(os.path.join(self.dest_dir, "model.bin")))

    @mock.patch("neutree.downloader.local.should_skip_verification", return_value=False)
    def test_verify_cache_hit_skips_recomputation(self, _mock_skip):
        self._add_correct_checksums()

        dl = LocalDownloader()
        # First download: computes and caches
        dl.download(self.src_dir, self.dest_dir, metadata={"file": ""}, overwrite=True)

        # Second download with overwrite: should hit verify cache
        with mock.patch("neutree.downloader.local.compute_sha256") as mock_hash:
            dl.download(self.src_dir, self.dest_dir, metadata={"file": ""}, overwrite=True)
            mock_hash.assert_not_called()

    @mock.patch("neutree.downloader.local.should_skip_verification", return_value=False)
    def test_checksums_copied_with_allow_pattern(self, _mock_skip):
        """Checksums should be copied even when allow_pattern filters model files."""
        self._add_correct_checksums()

        dl = LocalDownloader()
        # Only allow *.bin files — but .neutree/ should still be copied
        dl.download(self.src_dir, self.dest_dir, metadata={"file": "*.bin"})

        # model.bin should be copied (matches pattern)
        self.assertTrue(os.path.exists(os.path.join(self.dest_dir, "model.bin")))
        # config.json should NOT be copied (filtered)
        self.assertFalse(os.path.exists(os.path.join(self.dest_dir, "config.json")))
        # checksums should still be present
        self.assertTrue(os.path.isdir(os.path.join(self.dest_dir, ".neutree", "checksums")))


if __name__ == "__main__":
    unittest.main()
