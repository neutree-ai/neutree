import os
from typing import Optional, Dict, Any
import shutil
import fnmatch

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

        if recursive:
            # copy all files; skip existing unless overwrite
            for root, dirs, files in os.walk(src):
                rel = os.path.relpath(root, src)
                target_root = os.path.join(dest, rel) if rel != os.curdir else dest
                ensure_dir(target_root)
                for f in files:
                    if allow_pattern:
                        if not fnmatch.fnmatch(f, allow_pattern):
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
