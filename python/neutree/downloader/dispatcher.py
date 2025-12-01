"""Dispatcher helper utilities.

Dispatcher now exposes a small helper to obtain a backend downloader implementation
instance based on an explicit backend string. Main is expected to parse high-level
modelArgs into a low-level DownloadRequest (source/dest/credentials) and explicit
backend value, then request the downloader implementation via get_downloader().
"""
from typing import Dict, Any

from .entity import DownloadRequest


def get_downloader(backend: str):
    """Return a downloader implementation instance for the given backend.

    Backend string is expected to be a simple identifier like 'huggingface' or 'nfs'.
    Lazy imports are used so optional dependencies remain optional.
    """
    if not backend:
        raise RuntimeError("backend must be provided to get_downloader")

    backend = backend.lower()
    if backend == "hugging-face":
        from .huggingface import HuggingFaceDownloader as D

        return D()
    if backend == "local":
        from .local import LocalDownloader as D

        return D()

    # Future: support 's3', 'http', etc.
    raise RuntimeError(f"unsupported backend: {backend}")
