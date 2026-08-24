"""neutree.downloader

Simple downloader package supporting Hugging Face, ModelScope and Local backends.
Run as module: python -m neutree.downloader
"""

from .dispatcher import get_downloader
from .utils import build_request_from_model_args, download_with_markers

__all__ = [
    "base",
    "huggingface",
    "model_scope",
    "local",
    "entity",
    "progress",
    "utils",
    "get_downloader",
    "build_request_from_model_args",
    "download_with_markers",
]
