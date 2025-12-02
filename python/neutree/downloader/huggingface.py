import os
from typing import Optional, Dict, Any

from .base import Downloader
from .utils import ensure_dir


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
        version = metadata.get("version") if metadata else None

        hf.snapshot_download(repo_id=repo_id, allow_patterns=allow_pattern, local_dir=dest, token=token, revision=version)
