"""Stable prometheus_client multiprocess directory helpers."""

from __future__ import annotations

import os
import shutil
from pathlib import Path
from typing import Any


def ensure_prometheus_multiproc_dir(
    *,
    base_dir: str = "/tmp/neutree-prometheus-multiproc",
    namespace: str = "default",
) -> Path:
    """Return a stable multiprocess dir and export it for prometheus_client."""
    existing = os.environ.get("PROMETHEUS_MULTIPROC_DIR")
    if existing:
        path = Path(existing)
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        return path

    path = Path(base_dir) / namespace / str(os.getpid())
    if path.exists():
        shutil.rmtree(path)
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.environ["PROMETHEUS_MULTIPROC_DIR"] = str(path)

    return path


def install_stable_prometheus_multiproc_dir(
    common_module: Any,
    engine_module: Any,
    *,
    base_dir: str = "/tmp/neutree-prometheus-multiproc",
    namespace: str = "default",
) -> Path:
    """Patch SGLang's tempdir-based multiprocess setup with a stable path."""
    path = ensure_prometheus_multiproc_dir(base_dir=base_dir, namespace=namespace)

    def set_prometheus_multiproc_dir() -> str:
        os.environ["PROMETHEUS_MULTIPROC_DIR"] = str(path)
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        return str(path)

    common_module.set_prometheus_multiproc_dir = set_prometheus_multiproc_dir
    engine_module.set_prometheus_multiproc_dir = set_prometheus_multiproc_dir

    return path
