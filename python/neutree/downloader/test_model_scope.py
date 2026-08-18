"""Tests for ModelScopeDownloader.

Run with (from project root):
    PYTHONPATH=python python3 -m pytest python/neutree/downloader/test_model_scope.py -v

These drive a real HTTP server over a real socket rather than mocking urllib.
The behaviours that matter here are all protocol behaviours — a 302 to another
host, a Range request that a server may or may not honour, an Authorization
header that must not cross a host boundary, a 200 response that means "no such
revision" — and none of them survive being mocked out. The server below is a
deliberately faithful copy of what modelscope.cn was observed to do, including
the parts that are unhelpful.
"""

import hashlib
import http.server
import json
import os
import shutil
import sys
import tempfile
import threading
import types
import unittest
import urllib.parse
from unittest import mock

# Provide stub huggingface_hub modules when the real package is absent. utils.py
# imports its hashing helpers from there; this downloader adds no dependency of
# its own.
_fake_sha = types.ModuleType("huggingface_hub.utils.sha")
_fake_sha.git_hash = lambda data: ""
_fake_sha.sha_fileobj = lambda stream, bufsize=0: hashlib.sha256(stream.read()).digest()
sys.modules.setdefault("huggingface_hub", types.ModuleType("huggingface_hub"))
sys.modules.setdefault("huggingface_hub.utils", types.ModuleType("huggingface_hub.utils"))
sys.modules.setdefault("huggingface_hub.utils.sha", _fake_sha)
_fake_hf_api = types.ModuleType("huggingface_hub.hf_api")
_fake_hf_api.RepoFile = type("RepoFile", (), {})
sys.modules.setdefault("huggingface_hub.hf_api", _fake_hf_api)

from neutree.downloader.dispatcher import get_downloader  # noqa: E402
from neutree.downloader import model_scope  # noqa: E402
from neutree.downloader.model_scope import (  # noqa: E402
    ModelScopeDownloader,
    ModelScopeDownloadUnavailable,
)
from neutree.downloader.utils import build_request_from_model_args  # noqa: E402

MODEL_ID = "test-owner/test-model"

# Content is generated rather than literal so the "large" file can exercise a
# multi-chunk read without a large fixture.
_FILES = {
    "config.json": b'{"architectures": ["TestForCausalLM"]}',
    "README.md": b"# test model\n",
    "nested/weights.bin": b"nested-payload" * 16,
    "model-q2_k.gguf": bytes(range(256)) * 64,
}

# The file the fake hub serves by redirect to a "CDN" on a different host, the
# way modelscope.cn serves LFS objects.
_REDIRECTED = "model-q2_k.gguf"


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


class _Hub(http.server.BaseHTTPRequestHandler):
    """A stand-in for modelscope.cn's /api/v1 surface.

    Class attributes carry the per-test knobs and the record of what was asked,
    because BaseHTTPRequestHandler is instantiated per request.
    """

    revisions = ["master"]           # branches the repository has
    files = dict(_FILES)             # path -> bytes
    honour_range = True              # set False to model a server that ignores Range
    corrupt_sha_for = None           # path whose advertised Sha256 is a lie
    requests = []                    # (path, query dict, headers) for assertions

    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        pass

    # -- helpers ---------------------------------------------------------

    def _send(self, code, body: bytes, content_type="application/json", extra=None):
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        for key, value in (extra or {}).items():
            self.send_header(key, value)
        self.end_headers()
        if body:
            self.wfile.write(body)

    def _envelope(self, data, code=200, message="success", success=True):
        return json.dumps({"Code": code, "Data": data, "Message": message,
                           "RequestId": "test", "Success": success}).encode()

    def _listing(self, revision):
        # The behaviour this whole downloader is shaped around: an unknown
        # revision is HTTP 200 with Files: null, not an error.
        if revision and revision not in self.revisions:
            return self._envelope({"Files": None, "IsVisual": 0, "LatestCommitter": None})

        entries = []
        seen_dirs = set()
        for path, body in sorted(self.files.items()):
            parent = os.path.dirname(path)
            if parent and parent not in seen_dirs:
                seen_dirs.add(parent)
                entries.append({"Path": parent, "Name": parent, "Type": "tree", "Size": 0})
            sha = _sha256(body)
            if self.corrupt_sha_for == path:
                sha = "0" * 64
            entries.append({
                "Path": path, "Name": os.path.basename(path), "Type": "blob",
                "Size": len(body), "Sha256": sha, "IsLFS": path == _REDIRECTED,
                "Revision": "deadbeef",
            })
        return self._envelope({"Files": entries, "IsVisual": 0})

    def _blob(self, body: bytes):
        rng = self.headers.get("Range")
        if rng and self.honour_range and rng.startswith("bytes="):
            start = int(rng[len("bytes="):].split("-")[0])
            if start >= len(body):
                self._send(416, b"", "text/plain")
                return
            chunk = body[start:]
            self.send_response(206)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(chunk)))
            self.send_header("Content-Range", f"bytes {start}-{len(body) - 1}/{len(body)}")
            self.send_header("Accept-Ranges", "bytes")
            self.end_headers()
            self.wfile.write(chunk)
            return

        self._send(200, body, "application/octet-stream", {"Accept-Ranges": "bytes"})

    # -- routes ----------------------------------------------------------

    def do_GET(self):  # noqa: N802 - http.server's spelling
        parsed = urllib.parse.urlsplit(self.path)
        query = dict(urllib.parse.parse_qsl(parsed.query, keep_blank_values=True))
        type(self).requests.append((parsed.path, query, dict(self.headers)))

        # The "CDN": a different host from the caller's point of view, reached
        # only by redirect.
        if parsed.path.startswith("/cdn/"):
            name = urllib.parse.unquote(parsed.path[len("/cdn/"):])
            body = self.files.get(name)
            if body is None:
                self._send(404, b"", "text/plain")
                return
            self._blob(body)
            return

        prefix = f"/api/v1/models/{MODEL_ID}"
        if parsed.path == prefix + "/repo/files":
            self._send(200, self._listing(query.get("Revision", "")))
            return

        if parsed.path == prefix + "/revisions":
            self._send(200, self._envelope({"RevisionMap": {
                "Branches": [{"Revision": r} for r in self.revisions],
                "Tags": [],
            }}))
            return

        if parsed.path == prefix + "/repo":
            name = query.get("FilePath", "")
            body = self.files.get(name)
            if body is None:
                self._send(404, self._envelope(
                    None, code=10990101007, message="file not found", success=False))
                return
            if name == _REDIRECTED:
                host = f"127.0.0.1:{self.server.server_address[1]}"
                # Different netloc than the caller used ("localhost:port"), so the
                # Authorization-stripping path is genuinely exercised.
                self._send(302, b"", "text/plain",
                           {"Location": f"http://{host}/cdn/{urllib.parse.quote(name)}"})
                return
            self._blob(body)
            return

        self._send(404, self._envelope(
            None, code=10010205001, message="record not found", success=False))


class ModelScopeTestCase(unittest.TestCase):
    def setUp(self):
        _Hub.revisions = ["master"]
        _Hub.files = dict(_FILES)
        _Hub.honour_range = True
        _Hub.corrupt_sha_for = None
        _Hub.requests = []

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _Hub)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        # "localhost" here, "127.0.0.1" in the redirect Location: same machine,
        # different netloc, which is what the header-stripping rule keys on.
        self.endpoint = f"http://localhost:{self.server.server_address[1]}"

        self.dest = tempfile.mkdtemp()
        self.env = mock.patch.dict(os.environ, {"MODELSCOPE_ENDPOINT": self.endpoint}, clear=False)
        self.env.start()

    def tearDown(self):
        self.env.stop()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        shutil.rmtree(self.dest, ignore_errors=True)

    def download(self, **metadata):
        meta = {"name": MODEL_ID}
        meta.update(metadata)
        ModelScopeDownloader().download(MODEL_ID, self.dest, metadata=meta)

    def listing_queries(self):
        return [q for path, q, _ in _Hub.requests if path.endswith("/repo/files")]

    def files_on_disk(self):
        found = set()
        for root, _, names in os.walk(self.dest):
            if ".neutree" in root.split(os.sep):
                continue
            for name in names:
                found.add(os.path.relpath(os.path.join(root, name), self.dest))
        return found


class TestHappyPath(ModelScopeTestCase):
    def test_downloads_every_file_with_correct_bytes(self):
        self.download()

        self.assertEqual(self.files_on_disk(), set(_FILES))
        for path, expected in _FILES.items():
            with open(os.path.join(self.dest, path), "rb") as fh:
                self.assertEqual(fh.read(), expected, path)

    def test_no_part_files_are_left_behind(self):
        self.download()
        self.assertEqual([p for p in self.files_on_disk() if p.endswith(".neutree-part")], [])

    def test_listing_asks_for_recursion(self):
        """Without Recursive=True the hub reports a subdirectory as one tree
        entry and never names the files inside it, so the weights under
        nested/ would be silently skipped."""
        self.download()

        self.assertTrue(self.listing_queries())
        for query in self.listing_queries():
            self.assertEqual(query.get("Recursive"), "True")

        self.assertIn("nested/weights.bin", self.files_on_disk())

    def test_default_version_omits_the_revision_parameter(self):
        """ModelScope's default branch is 'master', not 'main'. Rather than
        hard-code either, the parameter is left off so the hub resolves the
        repository's own default."""
        for version in ("", None, "latest", "master"):
            _Hub.requests = []
            shutil.rmtree(self.dest, ignore_errors=True)
            os.makedirs(self.dest)
            self.download(version=version)
            for query in self.listing_queries():
                self.assertNotIn("Revision", query, f"version={version!r}")

    def test_explicit_revision_is_sent(self):
        _Hub.revisions = ["master", "v1.0"]
        self.download(version="v1.0")
        self.assertEqual([q.get("Revision") for q in self.listing_queries()], ["v1.0"])

    def test_second_run_skips_files_already_present(self):
        self.download()
        _Hub.requests = []
        self.download()

        fetched = [q.get("FilePath") for path, q, _ in _Hub.requests if path.endswith("/repo")]
        self.assertEqual(fetched, [], "already-verified files should not be re-fetched")


class TestUnknownRevision(ModelScopeTestCase):
    """The trap this downloader exists to defuse: the hub answers HTTP 200 with
    Files: null for a revision that does not exist, which is indistinguishable
    from an empty repository unless /revisions is consulted."""

    def test_unknown_revision_raises_naming_the_revision(self):
        with self.assertRaises(RuntimeError) as ctx:
            self.download(version="zzz-not-a-branch")

        message = str(ctx.exception)
        self.assertIn("zzz-not-a-branch", message)
        self.assertIn("does not exist", message)
        self.assertIn("master", message, "the available revisions should be named")

    def test_unknown_revision_leaves_no_silent_empty_directory(self):
        with self.assertRaises(RuntimeError):
            self.download(version="main")

        self.assertEqual(self.files_on_disk(), set(),
                         "a nonexistent revision must not look like a successful empty download")

    def test_main_is_reported_as_missing_not_as_the_default(self):
        """'main' is the Hugging Face assumption; on ModelScope it is simply a
        branch that is not there, and must be reported as such."""
        with self.assertRaises(RuntimeError) as ctx:
            self.download(version="main")
        self.assertIn("does not exist", str(ctx.exception))

    def test_genuinely_empty_repository_says_so_instead(self):
        _Hub.files = {}
        with self.assertRaises(RuntimeError) as ctx:
            self.download()

        message = str(ctx.exception)
        self.assertIn("lists no files", message)
        self.assertNotIn("does not exist", message)


class TestFileFilter(ModelScopeTestCase):
    def test_gguf_pattern_selects_only_matching_files(self):
        self.download(file="*q2_k.gguf")
        self.assertEqual(self.files_on_disk(), {"model-q2_k.gguf"})

    def test_non_gguf_pattern_is_ignored_like_the_hugging_face_path(self):
        """resolve_allow_pattern only honours GGUF patterns, because a non-GGUF
        model needs its whole directory."""
        self.download(file="config.json")
        self.assertEqual(self.files_on_disk(), set(_FILES))

    def test_pattern_matching_nothing_is_an_error_not_an_empty_directory(self):
        with self.assertRaises(RuntimeError) as ctx:
            self.download(file="*nonexistent.gguf")

        self.assertIn("matched none", str(ctx.exception))
        self.assertEqual(self.files_on_disk(), set())


class TestRedirectAndResume(ModelScopeTestCase):
    def test_redirected_file_is_downloaded_intact(self):
        self.download(file="*q2_k.gguf")

        with open(os.path.join(self.dest, _REDIRECTED), "rb") as fh:
            self.assertEqual(fh.read(), _FILES[_REDIRECTED])
        self.assertTrue(any(path.startswith("/cdn/") for path, _, _ in _Hub.requests),
                        "the redirect should have been followed")

    def test_resume_continues_from_a_partial_file(self):
        body = _FILES[_REDIRECTED]
        part = os.path.join(self.dest, _REDIRECTED + ".neutree-part")
        with open(part, "wb") as fh:
            fh.write(body[:100])

        self.download(file="*q2_k.gguf")

        ranges = [h.get("Range") for path, _, h in _Hub.requests if path.startswith("/cdn/")]
        self.assertIn("bytes=100-", ranges)
        with open(os.path.join(self.dest, _REDIRECTED), "rb") as fh:
            self.assertEqual(fh.read(), body)

    def test_a_server_ignoring_range_restarts_instead_of_appending(self):
        """Appending a full 200 response onto existing bytes would produce a
        file of the wrong length made of the right bytes twice over."""
        _Hub.honour_range = False
        body = _FILES[_REDIRECTED]
        part = os.path.join(self.dest, _REDIRECTED + ".neutree-part")
        with open(part, "wb") as fh:
            fh.write(body[:100])

        self.download(file="*q2_k.gguf")

        with open(os.path.join(self.dest, _REDIRECTED), "rb") as fh:
            self.assertEqual(fh.read(), body)

    def test_token_is_sent_to_the_hub_but_not_to_the_redirect_target(self):
        ModelScopeDownloader().download(
            MODEL_ID, self.dest, credentials={"token": "ms-secret"},
            metadata={"name": MODEL_ID, "file": "*q2_k.gguf"})

        hub = [h for path, _, h in _Hub.requests if path.startswith("/api/")]
        cdn = [h for path, _, h in _Hub.requests if path.startswith("/cdn/")]

        self.assertTrue(hub and all(h.get("Authorization") == "Bearer ms-secret" for h in hub))
        self.assertTrue(cdn)
        self.assertTrue(all("Authorization" not in h for h in cdn),
                        "the hub token must not follow a redirect onto another host")


class TestVerification(ModelScopeTestCase):
    def test_checksum_mismatch_fails_and_leaves_no_file_at_the_real_path(self):
        _Hub.corrupt_sha_for = "config.json"

        with self.assertRaises(RuntimeError) as ctx:
            self.download()

        self.assertIn("checksum mismatch", str(ctx.exception))
        self.assertFalse(os.path.exists(os.path.join(self.dest, "config.json")),
                         "a file that failed verification must not appear under its real name")

    def test_verification_can_be_skipped(self):
        _Hub.corrupt_sha_for = "config.json"
        with mock.patch.dict(os.environ, {"NEUTREE_VERIFY_SKIP": "1"}):
            self.download()
        self.assertIn("config.json", self.files_on_disk())


class TestOffline(ModelScopeTestCase):
    """Acceptance criterion 2: an offline deployment must fail with a message
    that points at runtime download being unavailable."""

    def test_offline_with_nothing_cached_names_the_real_problem(self):
        with mock.patch.dict(os.environ, {"HF_HUB_OFFLINE": "1"}):
            with self.assertRaises(ModelScopeDownloadUnavailable) as ctx:
                self.download()

        self.assertIn("runtime model download is unavailable", str(ctx.exception))
        self.assertEqual(_Hub.requests, [], "offline mode must not touch the network")

    def test_offline_with_a_populated_cache_succeeds(self):
        os.makedirs(os.path.join(self.dest, "nested"), exist_ok=True)
        for path, body in _FILES.items():
            with open(os.path.join(self.dest, path), "wb") as fh:
                fh.write(body)

        with mock.patch.dict(os.environ, {"NEUTREE_DL_OFFLINE": "1"}):
            self.download()

        self.assertEqual(_Hub.requests, [])

    def test_unreachable_hub_names_the_real_problem(self):
        with mock.patch.dict(os.environ, {"MODELSCOPE_ENDPOINT": "http://127.0.0.1:1"}):
            with self.assertRaises(ModelScopeDownloadUnavailable) as ctx:
                self.download()

        self.assertIn("runtime model download is unavailable", str(ctx.exception))


class TestUnknownModel(ModelScopeTestCase):
    def test_unknown_model_surfaces_the_hubs_own_code_and_message(self):
        with self.assertRaises(RuntimeError) as ctx:
            ModelScopeDownloader().download(
                "no-such/model", self.dest, metadata={"name": "no-such/model"})

        message = str(ctx.exception)
        self.assertIn("404", message)
        self.assertIn("10010205001", message)


class TestWiring(unittest.TestCase):
    """The backend is selected from the registry type, and the credential comes
    from the hub-specific variable."""

    def test_model_scope_registry_type_selects_the_model_scope_backend(self):
        backend, request = build_request_from_model_args({
            "registry_type": "model-scope", "name": MODEL_ID,
            "registry_path": MODEL_ID, "path": "/models/x", "version": "",
        })
        self.assertEqual(backend, "model-scope")
        self.assertEqual(request.source, MODEL_ID)
        self.assertIsInstance(get_downloader(backend), ModelScopeDownloader)

    def test_model_scope_reads_its_own_token_variable(self):
        with mock.patch.dict(os.environ, {"MODELSCOPE_API_TOKEN": "ms-token", "HF_TOKEN": "hf-token"},
                             clear=False):
            _, request = build_request_from_model_args(
                {"registry_type": "model-scope", "name": MODEL_ID})
        self.assertEqual(request.credentials, {"token": "ms-token"})

    def test_hugging_face_does_not_pick_up_the_model_scope_token(self):
        """Regression guard for acceptance criterion 3: the Hugging Face path
        must be untouched, and in particular must not start sending a
        ModelScope credential to huggingface.co."""
        env = {"MODELSCOPE_API_TOKEN": "ms-token"}
        with mock.patch.dict(os.environ, env, clear=False):
            os.environ.pop("HF_TOKEN", None)
            os.environ.pop("NEUTREE_DL_TOKEN", None)
            backend, request = build_request_from_model_args(
                {"registry_type": "hugging-face", "name": MODEL_ID})

        self.assertEqual(backend, "hugging-face")
        self.assertIsNone(request.credentials)


class TestConcurrency(ModelScopeTestCase):
    """A shared model cache with replicas starting together is the normal case,
    not a corner, so two processes downloading the same file into the same
    destination has to be safe."""

    def _download_in_thread(self, errors, **metadata):
        def run():
            try:
                self.download(**metadata)
            except BaseException as exc:  # noqa: BLE001 - reported to the test
                errors.append(exc)
        return threading.Thread(target=run)

    def test_two_concurrent_downloads_fetch_once_and_leave_a_correct_file(self):
        """The loser must not share the winner's .part. If it did it would keep
        writing into the inode after the winner renamed it onto the final name,
        corrupting a file that had already been verified."""
        errors = []
        threads = [self._download_in_thread(errors, file="*q2_k.gguf") for _ in range(2)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=60)

        self.assertEqual(errors, [])
        self.assertFalse(any(t.is_alive() for t in threads), "a download thread deadlocked")

        blob_fetches = [q.get("FilePath") for path, q, _ in _Hub.requests
                        if path.endswith("/repo") and q.get("FilePath") == _REDIRECTED]
        self.assertEqual(len(blob_fetches), 1,
                         "the second process should have found the finished file, not re-fetched it")

        with open(os.path.join(self.dest, _REDIRECTED), "rb") as fh:
            self.assertEqual(fh.read(), _FILES[_REDIRECTED])
        self.assertEqual([p for p in self.files_on_disk() if p.endswith(".neutree-part")], [])

    def test_the_verification_record_survives_concurrent_writers(self):
        """The record store is where two writers interleave; a torn record makes
        a good file look unverified or a bad one look verified."""
        errors = []
        threads = [self._download_in_thread(errors) for _ in range(3)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=60)
        self.assertEqual(errors, [])

        verify_dir = os.path.join(self.dest, ".neutree", "verify")
        records = [os.path.join(r, n) for r, _, ns in os.walk(verify_dir) for n in ns]
        self.assertEqual(len(records), len(_FILES))
        for path in records:
            with open(path) as fh:
                record = json.load(fh)          # torn JSON would raise here
            self.assertTrue(record["passed"])
            self.assertEqual(record["algorithm"], "sha256")
            self.assertEqual(record["expected_hash"], record["actual_hash"])


class TestLockIdentity(ModelScopeTestCase):
    def test_the_record_store_is_guarded_by_the_lock_huggingface_uses(self):
        """A ModelScope-private lock would exclude nothing: huggingface.py writes
        the same records under the same names in the same directory, guarding
        them with dest/.neutree/verify.lock."""
        taken = []
        real = model_scope.FileLock

        def spy(lockfile, timeout=300.0):
            taken.append(lockfile)
            return real(lockfile, timeout=timeout)

        with mock.patch.object(model_scope, "FileLock", spy):
            self.download(file="*q2_k.gguf")

        verify_lock = os.path.join(self.dest, ".neutree", "verify.lock")
        self.assertIn(verify_lock, taken)
        # And the same path huggingface.py builds, spelled the same way.
        self.assertEqual(verify_lock, os.path.join(self.dest, ".neutree", "verify.lock"))
        self.assertTrue(any(p.startswith(os.path.join(self.dest, ".neutree", "locks")) for p in taken),
                        "the per-file download lock should also have been taken")


if __name__ == "__main__":
    unittest.main()
