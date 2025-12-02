"""neutree.downloader

Simple downloader package supporting Hugging Face and Local backends.
Run as module: python -m neutree.downloader
"""

from .dispatcher import get_downloader
from .utils import build_request_from_model_args

__all__ = [
    "base",
    "huggingface",
    "local",
    "entity",
    "utils",
    "get_downloader",
    "build_request_from_model_args",
]
