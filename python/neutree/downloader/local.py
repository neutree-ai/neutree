import os
import sys
from typing import Optional, Dict, Any
import shutil
import fnmatch
import time

from .base import Downloader
from .utils import ensure_dir


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

        allow_pattern = metadata.get("file")
        if allow_pattern == "":
            allow_pattern = None

        files_to_copy = []
        total_size = 0

        if recursive:
            # copy all files; skip existing unless overwrite
            for root, dirs, files in os.walk(src):
                rel = os.path.relpath(root, src)
                target_root = os.path.join(dest, rel) if rel != os.curdir else dest
                for f in files:
                    if allow_pattern:
                        if not fnmatch.fnmatch(f, allow_pattern):
                            continue
                    s = os.path.join(root, f)
                    t = os.path.join(target_root, f)
                    if os.path.exists(t) and not overwrite:
                        continue
                    file_size = os.path.getsize(s)
                    files_to_copy.append((s, t, target_root, file_size))
                    total_size += file_size
        else:
            # copy only top-level files (non-recursive)
            for entry in os.listdir(src):
                s = os.path.join(src, entry)
                if os.path.isfile(s):
                    if allow_pattern:
                        if not fnmatch.fnmatch(entry, allow_pattern):
                            continue
                    t = os.path.join(dest, entry)
                    if os.path.exists(t) and not overwrite:
                        continue
                    file_size = os.path.getsize(s)
                    files_to_copy.append((s, t, dest, file_size))
                    total_size += file_size

        if not files_to_copy:
            print("[Downloader] No files to copy.", file=sys.stderr)
            return

        total_mb = total_size / (1024 * 1024)
        print(f"[Downloader] Starting to copy {len(files_to_copy)} files, total size: {total_mb:.2f} MB", file=sys.stderr)

        copied_size = 0
        copied_count = 0
        last_print_time = time.time()

        for s, t, target_root, file_size in files_to_copy:
            ensure_dir(target_root)
            shutil.copy2(s, t)
            copied_size += file_size
            copied_count += 1

            current_time = time.time()
            if current_time - last_print_time >= 10.0:
                progress_percent = (copied_size / total_size * 100) if total_size > 0 else 0
                copied_mb = copied_size / (1024 * 1024)
                print(f"[Downloader] Progress: {copied_count}/{len(files_to_copy)} files, "
                      f"{copied_mb:.2f}/{total_mb:.2f} MB ({progress_percent:.1f}%)", file=sys.stderr)
                last_print_time = current_time

        final_mb = copied_size / (1024 * 1024)
        print(f"[Downloader] Completed: {copied_count} files, {final_mb:.2f} MB copied successfully.", file=sys.stderr)
