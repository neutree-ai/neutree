import os
import json
import time
import logging
from typing import Optional, Dict, Any
import shutil
import fnmatch

from .base import Downloader
from .progress import ProgressReporter, get_dir_size
from .utils import (
    ensure_dir,
    resolve_allow_pattern,
    compute_sha256,
    should_skip_verification,
    should_keep_failed_files,
    load_verification_record,
    save_verification_record_with_algo,
    delete_verification_record,
    FileLock,
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


class LocalDownloader(Downloader):
    """Downloader for local filesystem resources.

    The `resource` can be either an absolute path (/host/path).
    """

    def download(self, source: str, dest: str, *, credentials: Optional[Dict[str, str]] = None,
                 recursive: bool = True, overwrite: bool = False, retries: int = 3,
                 timeout: Optional[float] = None, metadata: Optional[Dict[str, Any]] = None) -> None:
        src = source
        if not os.path.exists(src):
            raise FileNotFoundError(f"source path does not exist: {src}")
        if not os.path.isdir(src):
            raise ValueError(f"source path is not a directory: {src}")

        ensure_dir(dest)

        allow_pattern = resolve_allow_pattern(metadata)

        # Compute total source size for percentage-based progress reporting
        total_size = get_dir_size(src)

        with ProgressReporter(dest, logger, total_size=total_size, label="Local copy"):
            self._copy_files(src, dest, allow_pattern=allow_pattern, recursive=recursive, overwrite=overwrite)

        # Verify copied files against source-of-truth checksums
        if not should_skip_verification():
            self._verify_copied_files(dest)

    def _copy_files(self, src: str, dest: str, *, allow_pattern: Optional[str], recursive: bool, overwrite: bool) -> None:
        """Copy files from source to destination."""
        if recursive:
            # copy all files; skip existing unless overwrite
            for root, dirs, files in os.walk(src):
                rel = os.path.relpath(root, src)
                target_root = os.path.join(dest, rel) if rel != os.curdir else dest
                ensure_dir(target_root)
                for f in files:
                    if allow_pattern:
                        # Always copy .neutree/ metadata regardless of allow_pattern
                        if not rel.startswith(".neutree"):
                            file_relpath = os.path.join(rel, f) if rel != os.curdir else f
                            if not fnmatch.fnmatch(f, allow_pattern) and not fnmatch.fnmatch(file_relpath, allow_pattern):
                                continue
                    s = os.path.join(root, f)
                    t = os.path.join(target_root, f)
                    if os.path.exists(t) and not overwrite:
                        continue
                    shutil.copy2(s, t)
        else:
            # copy only top-level files (non-recursive)
            for entry in os.listdir(src):
                s = os.path.join(src, entry)
                t = os.path.join(dest, entry)
                if os.path.isfile(s):
                    if allow_pattern:
                        if not fnmatch.fnmatch(entry, allow_pattern):
                            continue
                    if os.path.exists(t) and not overwrite:
                        continue
                    shutil.copy2(s, t)

    def _verify_copied_files(self, dest: str) -> None:
        """Verify copied files against .neutree/checksums/ (source of truth).

        Reads checksum records produced by ImportModel (Go side), computes
        SHA256 of the local copies, and compares. Results are cached in
        .neutree/verify/ so subsequent runs skip already-verified files.
        """
        checksums_dir = os.path.join(dest, ".neutree", "checksums")
        if not os.path.isdir(checksums_dir):
            logger.info("No checksums found in downloaded model (.neutree/checksums), skipping verification")
            return

        verify_dir = os.path.join(dest, ".neutree", "verify")
        lockfile = os.path.join(dest, ".neutree", "verify.lock")

        with FileLock(lockfile):
            start_time = time.time()
            failures = []
            verified_count = 0
            skipped_count = 0
            keep_failed = should_keep_failed_files()

            # Collect all checksum records
            records = []
            for root, _, files in os.walk(checksums_dir):
                for fname in files:
                    if not fname.endswith(".json"):
                        continue
                    records.append(os.path.join(root, fname))

            total_files = len(records)
            if total_files == 0:
                logger.info("No checksum records found, skipping verification")
                return

            logger.info(f"Starting verification for {total_files} file(s)")

            for i, checksum_path in enumerate(records, 1):
                # Restore file relative path: strip checksums_dir prefix and .json suffix
                rel = os.path.relpath(checksum_path, checksums_dir)
                file_relpath = rel[:-5]  # strip .json

                try:
                    with open(checksum_path, "r", encoding="utf-8") as f:
                        record = json.load(f)

                    expected_hash = record["hash"]
                    algorithm = record["algorithm"]
                except (json.JSONDecodeError, OSError, KeyError) as e:
                    logger.error(
                        f"Failed to parse checksum record for '{file_relpath}' at '{checksum_path}': {e}")
                    failures.append(file_relpath)
                    continue

                logger.info(f"Verifying [{i}/{total_files}] {file_relpath} (using {algorithm})")

                # Check verify cache
                cached = load_verification_record(verify_dir, file_relpath)
                if (cached
                        and cached.get("expected_hash") == expected_hash
                        and cached.get("algorithm") == algorithm
                        and cached.get("passed")):
                    logger.debug(f"Skipping {file_relpath} (already verified)")
                    skipped_count += 1
                    continue

                abs_path = os.path.join(dest, file_relpath)
                if not os.path.exists(abs_path):
                    logger.debug(f"Skipping {file_relpath} (not found locally)")
                    continue

                if algorithm != "sha256":
                    logger.error(
                        f"Unsupported checksum algorithm '{algorithm}' for {file_relpath}; expected 'sha256'")
                    failures.append(file_relpath)
                    continue

                try:
                    actual_hash = compute_sha256(abs_path)
                except Exception as e:
                    logger.error(f"Failed to compute sha256 for {file_relpath}: {e}")
                    failures.append(file_relpath)
                    continue

                passed = (actual_hash == expected_hash)

                save_verification_record_with_algo(
                    verify_dir, file_relpath, algorithm, expected_hash, actual_hash, passed)

                if passed:
                    verified_count += 1
                    logger.debug(f"{file_relpath} checksum matches")
                else:
                    logger.error(
                        f"Checksum mismatch for '{file_relpath}': "
                        f"expected {expected_hash[:16]}..., got {actual_hash[:16]}...")
                    failures.append(file_relpath)

                    if not keep_failed:
                        try:
                            os.remove(abs_path)
                            logger.info(f"Deleted failed file: {abs_path}")
                        except Exception as e:
                            logger.warning(f"Failed to delete {abs_path}: {e}")
                        delete_verification_record(verify_dir, file_relpath)

            elapsed = time.time() - start_time

            if failures:
                logger.error(
                    f"Verification failed in {elapsed:.2f}s: "
                    f"{len(failures)} file(s) failed, {verified_count} verified, {skipped_count} skipped")
                raise RuntimeError(f"File verification failed for {len(failures)} file(s): {failures}")
            else:
                logger.info(
                    f"Verification completed in {elapsed:.2f}s: "
                    f"{verified_count} verified, {skipped_count} skipped")
