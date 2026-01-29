import os
import shutil
import json
import datetime
import time
from typing import Callable, Optional
from typing import Dict, Any, Tuple
from .entity import DownloadRequest
from huggingface_hub.utils.sha import git_hash, sha_fileobj


def ensure_dir(path: str) -> None:
    os.makedirs(path, exist_ok=True)

def copy_tree(src: str, dest: str, *, on_progress: Callable[[str], None] | None = None) -> None:
    """Recursively copy src to dest. Calls on_progress for each file copied."""
    ensure_dir(dest)
    if os.path.isfile(src):
        shutil.copy2(src, dest)
        if on_progress:
            on_progress(src)
        return

    for root, dirs, files in os.walk(src):
        rel = os.path.relpath(root, src)
        target_root = os.path.join(dest, rel) if rel != os.curdir else dest
        ensure_dir(target_root)
        for f in files:
            s = os.path.join(root, f)
            t = os.path.join(target_root, f)
            shutil.copy2(s, t)
            if on_progress:
                on_progress(s)

def env_bool(key: str, default: bool) -> bool:
    v = os.environ.get(key)
    if v is None:
        return default
    return v.lower() not in ("0", "false", "no")

def build_request_from_model_args(model_args: Dict[str, Any]) -> Tuple[str, DownloadRequest]:
    """Convert high-level model_args + environment into (backend, DownloadRequest).

    Rules (same as previous implementation):
    - backend: NEUTREE_DL_BACKEND env > model_args.registry-type heuristics > resource scheme heuristics > 'nfs'
    - source: model_args.path or model_args.name
    - dest: NEUTREE_DL_DEST or NEUTREE_DL_CACHE_DIR or '/models'
    - credentials: model_args.credentials (dict) or token from NEUTREE_DL_TOKEN/NEUTREE_HF_TOKEN
    - recursive/overwrite/retries/timeout read from env or defaults
    """
    backend = os.environ.get("NEUTREE_DL_BACKEND")
    if not backend:
        rt = (model_args.get("registry_type") or "")
        if isinstance(rt, str) and "hugging-face" in rt.lower():
            backend = "hugging-face"
    if not backend:
        # infer from path/name heuristics
        candidate = model_args.get("registry_path") or model_args.get("name") or ""
        if isinstance(candidate, str):
            if candidate.startswith("/"):
                backend = "local"
            elif candidate.startswith("http://") or candidate.startswith("https://"):
                backend = "http"

    if not backend:
        backend = "local"

    source = model_args.get("registry_path") or model_args.get("name") or ""
    dest = model_args.get("path") or "/models-cache"

    credentials = None
    token = os.environ.get("NEUTREE_DL_TOKEN") or os.environ.get("HF_TOKEN")
    if token and token != "":
        credentials = {"token": token}

    recursive = env_bool("NEUTREE_DL_RECURSIVE", True)
    overwrite = env_bool("NEUTREE_DL_OVERWRITE", False)
    retries = int(os.environ.get("NEUTREE_DL_RETRIES", "3"))
    timeout = None
    if os.environ.get("NEUTREE_DL_TIMEOUT"):
        try:
            timeout = float(os.environ.get("NEUTREE_DL_TIMEOUT"))
        except Exception:
            timeout = None

    dl_req = DownloadRequest(source=source, dest=dest, credentials=credentials,
                             recursive=recursive, overwrite=overwrite, retries=retries,
                             timeout=timeout, metadata=model_args)
    return backend, dl_req


# File verification utilities

class FileLock:
    """Simple file lock using fcntl for Unix/Linux systems.

    Uses non-blocking mode with retry logic to acquire the lock.
    """
    def __init__(self, lockfile: str, timeout: float = 300.0):
        self.lockfile = lockfile
        self.timeout = timeout
        self.lockfd = None

    def __enter__(self):
        import fcntl

        ensure_dir(os.path.dirname(self.lockfile))
        self.lockfd = open(self.lockfile, 'w')

        start_time = time.time()
        try:
            while True:
                try:
                    fcntl.flock(self.lockfd.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                    return self
                except (IOError, OSError):
                    elapsed = time.time() - start_time
                    if elapsed > self.timeout:
                        raise TimeoutError(f"Could not acquire lock on {self.lockfile} within {self.timeout}s")
                    time.sleep(0.1)
        except:
            # If we fail to acquire the lock, close the file descriptor to prevent leak
            if self.lockfd:
                self.lockfd.close()
                self.lockfd = None
            raise

    def __exit__(self, exc_type, exc_val, exc_tb):
        import fcntl

        if self.lockfd:
            try:
                fcntl.flock(self.lockfd.fileno(), fcntl.LOCK_UN)
            except:
                pass
            self.lockfd.close()
            self.lockfd = None

def compute_sha256(filepath: str) -> str:
    """Compute SHA256 hash of a file using 8MB buffer."""
    with open(filepath, "rb") as stream:
        return sha_fileobj(stream, 8 * 1024 * 1024).hex()


def compute_git_sha1(filepath: str) -> str:
    """Compute git SHA1 hash of a file (blob format)."""
    with open(filepath, "rb") as f:
        data = f.read()

    return git_hash(data)

def should_skip_verification() -> bool:
    """Check if file verification should be skipped.

    Set environment variable NEUTREE_VERIFY_SKIP=1/true/yes to skip verification.
    Or when HF_HUB_OFFLINE=1 (offline mode), verification is also skipped.
    Default is false (perform verification).
    """
    # Skip verification if explicitly requested
    if env_bool("NEUTREE_VERIFY_SKIP", False):
        return True

    # Skip verification in offline mode (cannot fetch remote file list from API)
    if env_bool("HF_HUB_OFFLINE", False):
        return True

    return False

def should_keep_failed_files() -> bool:
    """Check if failed verification files should be kept (for debugging).

    Set environment variable NEUTREE_VERIFY_KEEP_FAILED=1/true/yes to keep failed files.
    Default is false (delete failed files to force re-download).
    """
    return env_bool("NEUTREE_VERIFY_KEEP_FAILED", False)


def load_verification_record(verify_dir: str, file_relpath: str) -> Optional[Dict[str, Any]]:
    """Load verification record for a file.

    Returns:
        Dict with keys: algorithm, expected_hash, actual_hash, verified_at, passed
        or None if record doesn't exist.
    """
    record_path = os.path.join(verify_dir, file_relpath + ".json")
    try:
        with open(record_path, "r", encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return None

def save_verification_record_with_algo(verify_dir: str, file_relpath: str, algorithm: str, expected: str, actual: str, passed: bool) -> None:
    """Save verification record with algorithm information."""
    record_path = os.path.join(verify_dir, file_relpath + ".json")
    ensure_dir(os.path.dirname(record_path))

    record = {
        "algorithm": algorithm,
        "expected_hash": expected,
        "actual_hash": actual,
        "verified_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "passed": passed
    }

    with open(record_path, "w", encoding="utf-8") as f:
        json.dump(record, f, indent=2)


def delete_verification_record(verify_dir: str, file_relpath: str) -> None:
    """Delete verification record for a file."""
    record_path = os.path.join(verify_dir, file_relpath + ".json")
    try:
        os.remove(record_path)
    except FileNotFoundError:
        # If the verification record file does not exist, there is nothing to delete.
        pass