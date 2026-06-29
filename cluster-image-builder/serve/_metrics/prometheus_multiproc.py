"""Helpers for Prometheus multiprocess mode in embedded engines."""

from __future__ import annotations

import os
import shutil
from pathlib import Path
from types import ModuleType


def ensure_prometheus_multiproc_dir(
    *,
    base_dir: str = "/tmp/neutree-prometheus-multiproc",
    namespace: str = "default",
) -> str:
    """Create a stable prometheus multiprocess directory for this process.

    ``prometheus_client`` writes one mmap database per process. SGLang's helper
    uses a temporary directory, which can disappear while embedded Ray Serve
    engines are still initializing. A pid-scoped directory under /tmp is stable
    for the process lifetime and avoids stale files from earlier actors.
    """
    existing = os.environ.get("PROMETHEUS_MULTIPROC_DIR")
    if existing:
        Path(existing).mkdir(parents=True, exist_ok=True)
        return existing

    metrics_dir = Path(base_dir) / namespace / str(os.getpid())
    if metrics_dir.exists():
        shutil.rmtree(metrics_dir)
    metrics_dir.mkdir(parents=True, mode=0o700)
    os.environ["PROMETHEUS_MULTIPROC_DIR"] = str(metrics_dir)
    return str(metrics_dir)


def install_stable_prometheus_multiproc_dir(
    *,
    common_module: ModuleType,
    engine_module: ModuleType,
    base_dir: str = "/tmp/neutree-prometheus-multiproc",
    namespace: str = "default",
) -> str:
    """Patch SGLang's temporary multiprocess dir helper.

    SGLang calls ``set_prometheus_multiproc_dir`` inside ``Engine`` even when
    ``PROMETHEUS_MULTIPROC_DIR`` is already set. Its implementation creates a
    ``TemporaryDirectory`` under the configured directory and then drops the
    object, so the child directory can disappear during long initialization.
    Keep both module references pointed at Neutree's stable directory helper.
    """

    def _set_prometheus_multiproc_dir() -> str:
        return ensure_prometheus_multiproc_dir(
            base_dir=base_dir,
            namespace=namespace,
        )

    metrics_dir = _set_prometheus_multiproc_dir()
    common_module.set_prometheus_multiproc_dir = _set_prometheus_multiproc_dir
    engine_module.set_prometheus_multiproc_dir = _set_prometheus_multiproc_dir
    return metrics_dir
