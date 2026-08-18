"""Downloader for ModelScope (魔搭) model repositories.

Deliberately built on `urllib.request` from the standard library rather than on
the `modelscope` SDK. That is not frugality, it is a constraint the repository
already imposes: `cluster-image-builder/Dockerfile.engine-vllm` and
`Dockerfile.engine-sglang` both copy this package's sources while explicitly
refusing to `pip install downloader/requirements.txt`, because re-resolving the
dependency set downgrades the `huggingface_hub` their bases ship and breaks the
engine. Two of the three inference images therefore cannot take a new dependency
at all. `modelscope` would pull in `requests`/`urllib3`/`tqdm` and, transitively,
`huggingface_hub` — exactly the re-resolution those comments exist to prevent.

Nothing is lost by it. Everything this downloader needs, ModelScope's REST API
gives directly:

  * list a repository's files, with size and SHA-256, at
    GET /api/v1/models/<id>/repo/files?Recursive=True&Root=[&Revision=<rev>]
  * read one file at
    GET /api/v1/models/<id>/repo?FilePath=<path>[&Revision=<rev>]
  * enumerate the revisions that exist at
    GET /api/v1/models/<id>/revisions

Large files answer 302 to a CDN host that honours `Range` (verified: a ranged
request for a 988 MB weight file returns 206 with `content-range`), so resume is
implementable here; and the file listing carries the real SHA-256 of every entry,
LFS or not, so verification is a single algorithm rather than the LFS-sha256 /
git-blob-sha1 pair the Hugging Face path has to juggle.

Two behaviours of the hub shape the code and are worth stating up front:

1. **A revision that does not exist answers 200, not 404.** Asking for
   `Revision=main` or `Revision=zzz-not-a-branch` on a repository whose branch is
   `master` yields `{"Code":200,"Message":"success","Data":{"Files":null}}`. An
   empty file list is therefore never evidence that a repository is empty, and
   `_no_files_error` refuses to treat it as such: it asks `/revisions` which of
   the two it is, and says so. Silently producing an empty directory — which is
   what a naive `for f in files:` loop would do — leaves the engine to fail later
   on a directory with no weights in it.

Concurrency is assumed, not exceptional: a neutree model cache is shared storage
and an endpoint's replicas start together, so two processes fetch the same model
into the same directory routinely. Two locks handle that, and are always taken in
this order — `_download_lock` (per file, held across the whole fetch so the losing
writer cannot share the winner's `.part`), then `_verify_record_lock` (the same
lock file `huggingface.py` takes, around the same `.neutree/verify` store).

2. **The default branch is `master`, not `main`.** Rather than hard-code either,
   the `Revision` parameter is omitted entirely for the default version, which
   makes the hub resolve the repository's own default. This mirrors
   `internal/model_registry/model_scope.go`'s `revision()`. `DEFAULT_REVISION`
   below is only ever used to *name* that version back to a caller.
"""

import fnmatch
import json
import logging
import os
import socket
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Dict, List, Optional, Tuple

from .base import Downloader
from .progress import ProgressReporter, is_interactive
from .utils import (
    FileLock,
    compute_sha256,
    delete_verification_record,
    ensure_dir,
    is_offline_mode,
    load_verification_record,
    resolve_allow_pattern,
    save_verification_record_with_algo,
    should_keep_failed_files,
    should_skip_verification,
)

logger = logging.getLogger(__name__)

_log_level = os.environ.get("NEUTREE_LOG_LEVEL", "INFO").upper()
try:
    logger.setLevel(getattr(logging, _log_level))
except (AttributeError, ValueError):
    logger.setLevel(logging.INFO)

if not logger.handlers:
    _handler = logging.StreamHandler()
    _handler.setFormatter(logging.Formatter(
        '%(asctime)s - %(name)s - %(levelname)s - %(message)s'
    ))
    logger.addHandler(_handler)


DEFAULT_ENDPOINT = "https://www.modelscope.cn"
ENDPOINT_ENV = "MODELSCOPE_ENDPOINT"

# What ModelScope calls a repository's default branch. Used to report a version
# name, never to address one — see the module docstring.
DEFAULT_REVISION = "master"

# Version strings that mean "whatever the repository's default is". Kept in step
# with modelScope.revision() in internal/model_registry/model_scope.go and with
# v1.LatestVersion.
_DEFAULT_VERSION_ALIASES = ("", "latest", DEFAULT_REVISION)

_MODELS_PATH = "/api/v1/models"

# Read timeout for a single socket operation, not for a whole download: a
# multi-gigabyte weight file is many reads, each of which must make progress
# within this window.
_DEFAULT_TIMEOUT = 60.0

_CHUNK_SIZE = 8 * 1024 * 1024

# Suffix for the in-progress copy of a file. A download is only renamed onto its
# final path after its checksum has been checked, so a half-written file is never
# mistaken for a complete one by a later run or by the engine.
_PART_SUFFIX = ".neutree-part"

# Matches huggingface.py's timeout for the same lock over the same store.
_VERIFY_LOCK_TIMEOUT = 600.0

# The per-file lock is held for a whole download, so it has to outlast one. A
# 600s cap would expire on any large weight file over a slow link and turn a
# working download into a lock timeout.
_DEFAULT_FILE_LOCK_TIMEOUT = 3600.0
FILE_LOCK_TIMEOUT_ENV = "NEUTREE_DL_FILE_LOCK_TIMEOUT"


def _file_lock_timeout() -> float:
    raw = os.environ.get(FILE_LOCK_TIMEOUT_ENV)
    if not raw:
        return _DEFAULT_FILE_LOCK_TIMEOUT
    try:
        value = float(raw)
    except (TypeError, ValueError):
        return _DEFAULT_FILE_LOCK_TIMEOUT

    return value if value > 0 else _DEFAULT_FILE_LOCK_TIMEOUT


def _verify_record_lock(dest: str) -> FileLock:
    """The lock huggingface.py takes around the verification-record store.

    Deliberately the *same* lock file rather than a ModelScope-specific one:
    both downloaders write `.neutree/verify/<path>.json` under the same names in
    the same destination, so a private lock would exclude nothing that matters.
    """
    return FileLock(os.path.join(dest, ".neutree", "verify.lock"), timeout=_VERIFY_LOCK_TIMEOUT)


def _download_lock(dest: str, rel_path: str) -> FileLock:
    """Serialises one file's download into one destination.

    A neutree model cache is shared storage and the replicas of an endpoint
    start together, so two processes fetching the same model into the same
    directory is the normal case. Without this they share a `.part` path, and
    the losing writer keeps writing into the inode after the winner has renamed
    it onto the final name — corrupting a file that has already been verified
    and recorded as passing. Holding the lock across the whole fetch also means
    the second process finds the finished file and skips it, instead of
    downloading the same weights twice.

    Lock ordering, wherever both are taken: this one first, then the verify
    record lock. Never the other way round.
    """
    return FileLock(os.path.join(dest, ".neutree", "locks", rel_path + ".lock"),
                    timeout=_file_lock_timeout())


class ModelScopeDownloadUnavailable(RuntimeError):
    """The hub could not be reached at all.

    Distinct from every other failure on purpose: this is the one an air-gapped
    deployment hits, and the message has to say that runtime download is
    unavailable rather than surfacing a bare socket error or, worse, letting the
    engine start against a directory with no model in it.
    """


def _endpoint(metadata: Optional[Dict[str, Any]] = None) -> str:
    """Resolve the hub address.

    The orchestrator passes the registry's own URL through the environment, so a
    ModelScope mirror is reachable the same way `HF_ENDPOINT` makes a Hugging
    Face mirror reachable.
    """
    url = os.environ.get(ENDPOINT_ENV) or ""
    if not url and metadata:
        url = metadata.get("registry_url") or ""
    return (url or DEFAULT_ENDPOINT).rstrip("/")


def _revision_param(version: Optional[str]) -> str:
    """Map a version onto the hub's wire name for it.

    Returns "" for "the repository's default", which is expressed by omitting the
    parameter rather than by guessing a branch name.
    """
    if not version:
        return ""
    return "" if version.strip().lower() in _DEFAULT_VERSION_ALIASES else version


def reported_revision(version: Optional[str]) -> str:
    """The name to give a version back to a caller. Pairs with _revision_param."""
    return _revision_param(version) or DEFAULT_REVISION


class _NoAuthRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Follow redirects, but do not carry the hub token onto another host.

    Weight files answer 302 to a CDN whose URL is already signed. urllib's stock
    handler copies every header onto the redirected request, which would hand the
    caller's ModelScope token to a host that neither needs nor should see it.
    """

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        new = super().redirect_request(req, fp, code, msg, headers, newurl)
        if new is None:
            return None

        if urllib.parse.urlsplit(req.full_url).netloc != urllib.parse.urlsplit(newurl).netloc:
            # Request.headers keys are capitalised by add_header/Request.__init__.
            for key in list(new.headers):
                if key.lower() == "authorization":
                    del new.headers[key]

        return new


_opener = urllib.request.build_opener(_NoAuthRedirectHandler)


def _open(url: str, token: Optional[str], timeout: float, extra_headers: Optional[Dict[str, str]] = None):
    """Issue a GET and return the open response.

    Raises ModelScopeDownloadUnavailable when the hub cannot be reached at all;
    HTTP status codes come back as urllib.error.HTTPError for the caller to read.
    """
    req = urllib.request.Request(url, method="GET")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    for key, value in (extra_headers or {}).items():
        req.add_header(key, value)

    try:
        return _opener.open(req, timeout=timeout)
    except urllib.error.HTTPError:
        raise
    except (urllib.error.URLError, socket.timeout, OSError) as exc:
        raise ModelScopeDownloadUnavailable(
            f"runtime model download is unavailable: cannot reach the ModelScope endpoint "
            f"{urllib.parse.urlsplit(url).scheme}://{urllib.parse.urlsplit(url).netloc} ({exc}). "
            f"This deployment has no network path to the model hub, so the weights cannot be "
            f"fetched at runtime."
        ) from exc


def _get_envelope(url: str, token: Optional[str], timeout: float, what: str) -> Dict[str, Any]:
    """GET an endpoint that answers in ModelScope's JSON envelope, return its Data.

    The hub's own Code/Message is finer-grained than the HTTP status and is what a
    user needs to see, so it is carried into the error rather than replaced by it.
    """
    try:
        with _open(url, token, timeout) as resp:
            raw = resp.read()
    except urllib.error.HTTPError as exc:
        detail = ""
        try:
            body = json.loads(exc.read().decode("utf-8", "replace"))
            detail = f" (Code {body.get('Code')}: {body.get('Message')})" if isinstance(body, dict) else ""
        except Exception:
            detail = ""
        raise RuntimeError(f"failed to {what}: HTTP {exc.code}{detail}") from exc

    try:
        envelope = json.loads(raw.decode("utf-8", "replace"))
    except Exception as exc:
        raise RuntimeError(f"failed to {what}: response was not JSON") from exc

    if not isinstance(envelope, dict):
        raise RuntimeError(f"failed to {what}: response was not a JSON object")

    if not envelope.get("Success", True) or envelope.get("Code") not in (None, 200):
        raise RuntimeError(
            f"failed to {what}: ModelScope answered Code {envelope.get('Code')}: {envelope.get('Message')}"
        )

    data = envelope.get("Data")
    return data if isinstance(data, dict) else {}


def list_repo_files(endpoint: str, model_id: str, revision: str, token: Optional[str],
                    timeout: float) -> List[Dict[str, Any]]:
    """List every blob in a repository, recursively.

    `Recursive=True` is not optional. Without it the hub returns a single level
    and reports a subdirectory as one `Type: "tree"` entry with no contents, so a
    repository that keeps weights under `original/` would download as its
    top-level files alone — a partial model, not an error.

    Returns [] both for "this revision does not exist" and for "this repository
    is empty"; the caller must not treat that as success. See _no_files_error.
    """
    params = {"Root": "", "Recursive": "True"}
    if revision:
        params["Revision"] = revision

    url = f"{endpoint}{_MODELS_PATH}/{model_id}/repo/files?{urllib.parse.urlencode(params)}"
    data = _get_envelope(url, token, timeout, f"list files of ModelScope model {model_id!r}")

    files = data.get("Files")
    if not isinstance(files, list):
        # Files is literally null for a nonexistent revision.
        return []

    return [f for f in files if isinstance(f, dict) and f.get("Type") != "tree"]


def list_revisions(endpoint: str, model_id: str, token: Optional[str],
                   timeout: float) -> Tuple[List[str], List[str]]:
    """Return (branches, tags) for a repository."""
    url = f"{endpoint}{_MODELS_PATH}/{model_id}/revisions"
    data = _get_envelope(url, token, timeout, f"list revisions of ModelScope model {model_id!r}")

    revision_map = data.get("RevisionMap")
    if not isinstance(revision_map, dict):
        return [], []

    def names(key: str) -> List[str]:
        entries = revision_map.get(key)
        if not isinstance(entries, list):
            return []
        return [e.get("Revision") for e in entries if isinstance(e, dict) and e.get("Revision")]

    return names("Branches"), names("Tags")


def _no_files_error(endpoint: str, model_id: str, revision: str, token: Optional[str],
                    timeout: float) -> RuntimeError:
    """Turn an empty file list into a statement of which failure it actually was.

    The hub answers 200 "success" with `Files: null` for a revision that does not
    exist, which is indistinguishable from an empty repository at this endpoint.
    `/revisions` can tell them apart, so it is asked rather than guessed at. If
    that call itself fails, the ambiguity is reported as ambiguity — never
    resolved in favour of "the repository is empty", which is the reading that
    would let a caller proceed with nothing downloaded.
    """
    asked = revision or DEFAULT_REVISION

    try:
        branches, tags = list_revisions(endpoint, model_id, token, timeout)
    except Exception as exc:
        return RuntimeError(
            f"ModelScope model {model_id!r} returned no files for revision {asked!r}, and the "
            f"revision list could not be read to tell whether that revision exists ({exc}). "
            f"Refusing to continue with an empty model directory."
        )

    known = branches + tags
    if revision and revision not in known:
        available = ", ".join(known) if known else "none"
        return RuntimeError(
            f"revision {revision!r} does not exist in ModelScope model {model_id!r} "
            f"(available revisions: {available}). Note that ModelScope answers HTTP 200 with an "
            f"empty file list for an unknown revision, and that its default branch is "
            f"{DEFAULT_REVISION!r}, not 'main'."
        )

    return RuntimeError(
        f"ModelScope model {model_id!r} lists no files at revision {asked!r}; there is nothing "
        f"to download."
    )


def _select(files: List[Dict[str, Any]], allow_pattern: Optional[str],
            model_id: str) -> List[Dict[str, Any]]:
    """Apply the caller's file filter, refusing to match nothing silently."""
    if not allow_pattern:
        return files

    selected = [f for f in files if fnmatch.fnmatch(f.get("Path") or "", allow_pattern)]
    if not selected:
        listed = ", ".join(sorted((f.get("Path") or "") for f in files)[:20])
        raise RuntimeError(
            f"file pattern {allow_pattern!r} matched none of the {len(files)} file(s) in "
            f"ModelScope model {model_id!r} (files: {listed})"
        )

    return selected


class ModelScopeDownloader(Downloader):
    """Downloader for ModelScope repositories.

    Accepts a credentials map (e.g. {"token": "ms-..."}); metadata carries the
    high-level model_args (name, file, version, registry_path).
    """

    def download(self, source: str, dest: str, *, credentials: Optional[Dict[str, str]] = None,
                 recursive: bool = True, overwrite: bool = False, retries: int = 3,
                 timeout: Optional[float] = None, metadata: Optional[Dict[str, Any]] = None) -> None:
        ensure_dir(dest)

        model_id = (source or "").strip("/")
        if not model_id:
            raise RuntimeError("a ModelScope model id (\"<owner>/<name>\") is required")

        endpoint = _endpoint(metadata)
        token = (credentials or {}).get("token") or None
        revision = _revision_param((metadata or {}).get("version"))
        allow_pattern = resolve_allow_pattern(metadata)
        request_timeout = timeout if timeout and timeout > 0 else _DEFAULT_TIMEOUT

        if is_offline_mode():
            self._assert_offline_copy_usable(model_id, dest)
            return

        logger.info(
            f"Listing ModelScope model '{model_id}' at {endpoint} "
            f"(revision={revision or 'default (' + DEFAULT_REVISION + ')'})"
        )
        files = list_repo_files(endpoint, model_id, revision, token, request_timeout)
        if not files:
            raise _no_files_error(endpoint, model_id, revision, token, request_timeout)

        files = _select(files, allow_pattern, model_id)
        total_bytes = sum(int(f.get("Size") or 0) for f in files)
        logger.info(
            f"ModelScope model '{model_id}': {len(files)} file(s) selected, "
            f"{total_bytes} byte(s) total"
        )

        verify = not should_skip_verification()
        interactive = is_interactive()

        with ProgressReporter(dest, logger, label="ModelScope download",
                              total_size=total_bytes, interactive=interactive):
            for index, entry in enumerate(files, 1):
                self._fetch_one(endpoint, model_id, revision, entry, dest, token,
                                request_timeout, retries, overwrite, verify, index, len(files))

    def _assert_offline_copy_usable(self, model_id: str, dest: str) -> None:
        """Offline mode: use what is already on disk, or say why we cannot.

        Offline is a legitimate configuration — the weights may have been staged
        into the model cache out of band — so an already-populated destination is
        honoured. What must not happen is proceeding with an empty one: the engine
        would then fail somewhere inside itself on a missing model path, which is
        the failure this whole refusal exists to replace with a reason.
        """
        existing = []
        for root, _, names in os.walk(dest):
            if os.path.basename(root) == ".neutree":
                continue
            existing.extend(os.path.join(root, n) for n in names if not n.endswith(_PART_SUFFIX))

        if existing:
            logger.info(
                f"Offline mode: skipping ModelScope download of '{model_id}', using the "
                f"{len(existing)} file(s) already present at {dest}"
            )
            return

        raise ModelScopeDownloadUnavailable(
            f"runtime model download is unavailable: offline mode is enabled, so the weights for "
            f"ModelScope model {model_id!r} cannot be fetched from the hub, and none are present "
            f"at {dest}. Stage the model into the cluster's model cache, or deploy from a "
            f"registry this cluster can read offline."
        )

    def _fetch_one(self, endpoint: str, model_id: str, revision: str, entry: Dict[str, Any],
                   dest: str, token: Optional[str], timeout: float, retries: int,
                   overwrite: bool, verify: bool, index: int, total: int) -> None:
        rel_path = entry.get("Path") or ""
        if not rel_path or rel_path.startswith("/") or ".." in rel_path.split("/"):
            # The destination is written from names the hub chose; a name that
            # escapes it is refused rather than normalised, because there is no
            # legitimate repository that needs one.
            raise RuntimeError(f"ModelScope model {model_id!r} returned an unusable file path {rel_path!r}")

        expected_size = int(entry.get("Size") or 0)
        expected_hash = (entry.get("Sha256") or "").lower() or None

        target = os.path.join(dest, rel_path)
        ensure_dir(os.path.dirname(target) or dest)

        # The presence check is inside the lock on purpose: a process that waited
        # here re-checks afterwards and finds the file the winner just committed.
        with _download_lock(dest, rel_path):
            if not overwrite and self._already_have(target, dest, rel_path, expected_size,
                                                    expected_hash, verify):
                logger.info(f"[{index}/{total}] {rel_path}: already present, skipping")
                return

            logger.info(f"[{index}/{total}] {rel_path}: downloading ({expected_size} bytes)")
            part = target + _PART_SUFFIX

            last_error: Optional[BaseException] = None
            for attempt in range(1, max(1, retries) + 1):
                try:
                    self._stream(endpoint, model_id, revision, rel_path, part, token, timeout, expected_size)
                    self._commit(part, target, dest, rel_path, expected_size, expected_hash, verify)
                    return
                except ModelScopeDownloadUnavailable:
                    # No network path: retrying cannot help, and the message is the
                    # point of this class.
                    raise
                except Exception as exc:  # noqa: BLE001 - retried below, re-raised at the end
                    last_error = exc
                    logger.warning(f"{rel_path}: attempt {attempt}/{max(1, retries)} failed: {exc}")
                    if attempt < max(1, retries):
                        time.sleep(min(2 ** attempt, 30))

        raise RuntimeError(f"failed to download {rel_path} of ModelScope model {model_id!r}: {last_error}")

    def _already_have(self, target: str, dest: str, rel_path: str, expected_size: int,
                      expected_hash: Optional[str], verify: bool) -> bool:
        if not os.path.exists(target):
            return False

        try:
            if expected_size and os.path.getsize(target) != expected_size:
                return False
        except OSError:
            return False

        if not verify or not expected_hash:
            return True

        verify_dir = os.path.join(dest, ".neutree", "verify")
        with _verify_record_lock(dest):
            cached = load_verification_record(verify_dir, rel_path)

        if (cached and cached.get("expected_hash") == expected_hash
                and cached.get("algorithm") == "sha256" and cached.get("passed")):
            return True

        try:
            return compute_sha256(target).lower() == expected_hash
        except OSError:
            return False

    def _stream(self, endpoint: str, model_id: str, revision: str, rel_path: str, part: str,
                token: Optional[str], timeout: float, expected_size: int) -> None:
        """Fetch one file into its .part, resuming an interrupted transfer.

        Weight files are served by redirect to a CDN that honours Range, so an
        attempt that died partway through does not restart from zero. A server
        that ignores the Range header answers 200 rather than 206, which is
        detected here and restarts the file cleanly instead of appending fresh
        bytes onto stale ones.
        """
        params = {"FilePath": rel_path}
        if revision:
            params["Revision"] = revision
        url = f"{endpoint}{_MODELS_PATH}/{model_id}/repo?{urllib.parse.urlencode(params)}"

        have = 0
        if os.path.exists(part):
            try:
                have = os.path.getsize(part)
            except OSError:
                have = 0

        if expected_size and have >= expected_size:
            # A .part at or past the full length is not resumable: a ranged
            # request from there answers 416. Start it again rather than commit
            # bytes whose provenance is unknown.
            have = 0

        headers = {"Range": f"bytes={have}-"} if have else None

        try:
            with _open(url, token, timeout, headers) as resp:
                resumed = have > 0 and resp.status == 206
                mode = "ab" if resumed else "wb"
                if have and not resumed:
                    logger.info(f"{rel_path}: server ignored the resume request, restarting")
                    have = 0

                with open(part, mode) as fh:
                    while True:
                        chunk = resp.read(_CHUNK_SIZE)
                        if not chunk:
                            break
                        fh.write(chunk)
        except urllib.error.HTTPError as exc:
            detail = ""
            try:
                body = json.loads(exc.read().decode("utf-8", "replace"))
                if isinstance(body, dict):
                    detail = f" (Code {body.get('Code')}: {body.get('Message')})"
            except Exception:
                detail = ""
            raise RuntimeError(f"HTTP {exc.code} fetching {rel_path}{detail}") from exc

        if expected_size:
            actual = os.path.getsize(part)
            if actual != expected_size:
                raise RuntimeError(
                    f"{rel_path}: downloaded {actual} bytes but the repository lists {expected_size}"
                )

    def _commit(self, part: str, target: str, dest: str, rel_path: str, expected_size: int,
                expected_hash: Optional[str], verify: bool) -> None:
        """Verify the .part and move it onto the final path.

        Verification happens before the rename, so a file that fails its checksum
        never appears at its real name — a later run sees it as absent and fetches
        it again, rather than seeing a corrupt file of the right size and
        skipping it.

        The hash is computed outside the record lock — it is the expensive part
        and touches nothing shared — and only the write to the record store is
        held under it, which is what huggingface.py's lock protects too.
        """
        if verify and expected_hash:
            verify_dir = os.path.join(dest, ".neutree", "verify")
            ensure_dir(verify_dir)

            actual_hash = compute_sha256(part).lower()
            passed = actual_hash == expected_hash

            with _verify_record_lock(dest):
                save_verification_record_with_algo(verify_dir, rel_path, "sha256",
                                                   expected_hash, actual_hash, passed)
                if not passed:
                    delete_verification_record(verify_dir, rel_path)

            if not passed:
                if should_keep_failed_files():
                    logger.error(f"{rel_path}: checksum mismatch, keeping {part} for inspection")
                else:
                    try:
                        os.remove(part)
                    except OSError:
                        pass
                raise RuntimeError(
                    f"checksum mismatch for {rel_path}: expected sha256 {expected_hash[:16]}..., "
                    f"got {actual_hash[:16]}..."
                )
        elif verify and not expected_hash:
            logger.warning(f"{rel_path}: repository lists no Sha256, cannot verify")

        os.replace(part, target)
        logger.info(f"{rel_path}: done ({expected_size} bytes)")
