import os
import time
import logging
from typing import Optional, Dict, Any

from .base import Downloader
from huggingface_hub.hf_api import RepoFile
from .utils import (
    ensure_dir,
    compute_sha256,
    compute_git_sha1,
    should_keep_failed_files,
    load_verification_record,
    save_verification_record_with_algo,
    delete_verification_record,
    FileLock,
    should_skip_verification,
    env_bool,
)

# Configure logger for this module
logger = logging.getLogger(__name__)

# Set log level from environment variable, default to INFO
_log_level = os.environ.get("NEUTREE_LOG_LEVEL", "INFO").upper()
try:
    logger.setLevel(getattr(logging, _log_level))
except (AttributeError, ValueError):
    logger.setLevel(logging.INFO)

# Add stderr handler if no handlers configured
if not logger.handlers:
    _handler = logging.StreamHandler()
    _handler.setFormatter(logging.Formatter(
        '%(asctime)s - %(name)s - %(levelname)s - %(message)s'
    ))
    logger.addHandler(_handler)


class HuggingFaceDownloader(Downloader):
    """Downloader for Hugging Face repositories.

    Accepts a credentials map (e.g. {"token":"hf_xxx"}).
    metadata can contain high-level model_args (path, file, name, version).
    """

    def _ensure_hf(self):
        try:
            import huggingface_hub as _hf  # type: ignore

            return _hf
        except Exception as e:
            raise RuntimeError(
                "huggingface_hub is required for HuggingFaceDownloader. Install with `pip install huggingface-hub`"
            ) from e

    def download(self, source: str, dest: str, *, credentials: Optional[Dict[str, str]] = None,
                 recursive: bool = True, overwrite: bool = False, retries: int = 3,
                 timeout: Optional[float] = None, metadata: Optional[Dict[str, Any]] = None) -> None:
        ensure_dir(dest)
        hf = self._ensure_hf()

        # Resolve repo id and file from metadata or source
        repo_id = source
        allow_pattern = metadata.get("file")
        if allow_pattern == "":
            allow_pattern = None

        token = None
        if credentials and credentials.get("token"):
            token = credentials.get("token")
        # Convert empty string to None so HuggingFace Hub uses default branch
        # HuggingFace checks "if revision is None:" to use default branch
        # Empty string would be treated as an invalid branch name
        version = metadata.get("version") or None if metadata else None

        hf.snapshot_download(repo_id=repo_id, allow_patterns=allow_pattern, local_dir=dest, token=token, revision=version)

        # Verify downloaded files if not skipped
        if not should_skip_verification():
            self._verify_downloaded_files(repo_id, dest, revision=version, token=token)

    def _verify_downloaded_files(self, repo_id: str, dest: str, revision: Optional[str] = None, token: Optional[str] = None) -> None:
        """Verify downloaded files against HF repository checksums.

        This method:
        1. Calls list_repo_tree API to get remote file information (with lfs.sha256 or blob_id)
        2. Compares with cached verification records to skip redundant checks
        3. Computes actual hash (SHA256 for LFS, git-sha1 for blobs) for files that need verification
        4. Deletes files that fail verification (unless NEUTREE_VERIFY_KEEP_FAILED=1)
        5. Saves verification records to .neutree/verify/

        Args:
            repo_id: The HuggingFace repository ID
            dest: Local destination directory
            revision: The git revision/branch/tag to verify against
            token: HuggingFace API token for authentication

        Raises:
            RuntimeError: If any files fail checksum verification
        """
        hf = self._ensure_hf()

        # Create verification directory (dest already contains repo info)
        verify_dir = os.path.join(dest, ".neutree", "verify")
        ensure_dir(verify_dir)

        # Use file lock to prevent concurrent verification operations
        lockfile = os.path.join(dest, ".neutree", "verify.lock")
        with FileLock(lockfile, timeout=600.0):
            logger.debug(f"Acquired verification lock for '{repo_id}'")
            self._do_verify(hf, repo_id, dest, verify_dir, revision=revision, token=token)

    def _do_verify(self, hf, repo_id: str, dest: str, verify_dir: str, revision: Optional[str] = None, token: Optional[str] = None) -> None:
        """Perform actual verification with lock held."""
        start_time = time.time()

        # Get remote file information from HF API
        try:
            logger.info(f"Fetching remote file list from HF API for '{repo_id}' (revision={revision})")
            remote_files = list(hf.list_repo_tree(repo_id=repo_id, revision=revision, token=token, recursive=True))
        except Exception as e:
            logger.error(f"Failed to fetch remote file info from HF API: {e}")
            logger.warning("Skipping verification due to API error")
            return

        # Filter only files and prepare verification list
        files_to_verify = []
        for entry in remote_files:
            # Check if entry is a RepoFile (not RepoFolder)
            if not isinstance(entry, RepoFile):
                continue

            local_path = os.path.join(dest, entry.path)
            if not os.path.exists(local_path):
                logger.debug(f"Skipping {entry.path} (not found locally)")
                continue

            # Determine hash algorithm and expected value
            if hasattr(entry, 'lfs') and entry.lfs and hasattr(entry.lfs, 'sha256'):
                # LFS file: use SHA256
                algorithm = 'sha256'
                expected_hash = entry.lfs.sha256.lower()
            elif hasattr(entry, 'blob_id') and entry.blob_id:
                # Regular blob: use git-sha1
                algorithm = 'git-sha1'
                expected_hash = entry.blob_id.lower()
            else:
                logger.warning(f"Skipping {entry.path} (no hash available)")
                continue

            files_to_verify.append((entry.path, local_path, algorithm, expected_hash))

        total_files = len(files_to_verify)

        if total_files == 0:
            logger.info("No files to verify")
            return

        logger.info(f"Starting verification for {total_files} file(s) from '{repo_id}'")

        verified_count = 0
        skipped_count = 0
        failures = []
        keep_failed = should_keep_failed_files()

        for i, (rel_path, abs_path, algorithm, expected_hash) in enumerate(files_to_verify, 1):
            logger.info(f"Verifying [{i}/{total_files}] {rel_path} (using {algorithm})")

            # Check if we have a cached verification record with matching expected hash
            cached_record = load_verification_record(verify_dir, rel_path)
            if (cached_record and
                cached_record.get("expected_hash") == expected_hash and
                cached_record.get("algorithm") == algorithm and
                cached_record.get("passed")):
                logger.debug(f"Skipping {rel_path} (already verified with matching checksum)")
                skipped_count += 1
                continue

            # Compute actual hash using appropriate algorithm
            try:
                if algorithm == 'sha256':
                    actual_hash = compute_sha256(abs_path)
                else:  # git-sha1
                    actual_hash = compute_git_sha1(abs_path)
            except Exception as e:
                logger.error(f"Failed to compute {algorithm} for {rel_path}: {e}")
                failures.append({
                    "path": rel_path,
                    "expected": expected_hash,
                    "actual": "error",
                    "algorithm": algorithm,
                    "error": str(e)
                })
                continue

            # Compare hashes
            passed = (actual_hash.lower() == expected_hash.lower())

            # Save verification record with algorithm info
            save_verification_record_with_algo(verify_dir, rel_path, algorithm, expected_hash, actual_hash, passed)

            if passed:
                verified_count += 1
                logger.debug(f"{rel_path} checksum matches")
            else:
                logger.error(f"Checksum mismatch for '{rel_path}': expected {expected_hash[:16]}..., got {actual_hash[:16]}... (algorithm: {algorithm})")
                failures.append({
                    "path": rel_path,
                    "expected": expected_hash,
                    "actual": actual_hash,
                    "algorithm": algorithm
                })

                # Delete failed file unless keeping for debugging
                if not keep_failed:
                    try:
                        os.remove(abs_path)
                        logger.info(f"Deleted failed file: {abs_path}")
                    except Exception as e:
                        logger.warning(f"Failed to delete {abs_path}: {e}")

                    # Delete verification record
                    delete_verification_record(verify_dir, rel_path)

        elapsed = time.time() - start_time

        # Summary
        if failures:
            logger.error(f"Verification failed in {elapsed:.2f}s: {len(failures)} file(s) failed, {verified_count} verified, {skipped_count} skipped")
            failure_paths = [f['path'] for f in failures]
            raise RuntimeError(f"File verification failed for {len(failures)} file(s): {failure_paths}")
        else:
            logger.info(f"Verification completed in {elapsed:.2f}s: {verified_count} verified, {skipped_count} skipped")
