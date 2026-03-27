"""Progress reporting for non-TTY environments.

When running inside a Kubernetes init container or CI pipeline, stderr is not a
TTY and tqdm progress bars are silently disabled.  This module provides a
log-based progress reporter that periodically scans the destination directory
and emits human-readable progress lines via the standard logging framework.
"""

import logging
import os
import sys
import threading
import time


def is_interactive() -> bool:
    """Return True when stderr is connected to an interactive terminal."""
    return hasattr(sys.stderr, "isatty") and sys.stderr.isatty()


def format_size(num_bytes: float) -> str:
    """Format a byte count as a human-readable string (e.g. ``1.50 GB``)."""
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if abs(num_bytes) < 1024.0:
            return f"{num_bytes:.2f} {unit}"
        num_bytes /= 1024.0
    return f"{num_bytes:.2f} PB"


def get_dir_size(path: str) -> int:
    """Return the total size in bytes of all files under *path*.

    Silently skips files that disappear mid-walk (common while downloads are
    in progress).  Returns 0 for non-existent paths.
    """
    if not os.path.isdir(path):
        return 0
    total = 0
    for root, _dirs, files in os.walk(path):
        for fname in files:
            try:
                total += os.path.getsize(os.path.join(root, fname))
            except OSError:
                pass
    return total


class ProgressReporter:
    """Context manager that periodically logs download progress.

    In interactive (TTY) mode the reporter is a complete no-op so that tqdm
    progress bars are not disrupted.  In non-interactive mode a daemon thread
    polls the destination directory size and emits INFO-level log lines.

    Parameters
    ----------
    dest : str
        Directory being written to.
    dest_logger : logging.Logger
        Logger instance used for output (typically the caller's module logger).
    total_size : int or None
        Expected final size in bytes.  When provided the log lines include a
        percentage.
    interval : float or None
        Seconds between progress reports.  Overrides the
        ``NEUTREE_DL_PROGRESS_INTERVAL`` environment variable.
    label : str
        Human-readable prefix for log messages (e.g. ``"HuggingFace download"``).
    """

    _DEFAULT_INTERVAL = 30.0
    _MIN_INTERVAL = 1.0

    def __init__(
        self,
        dest: str,
        dest_logger: logging.Logger,
        *,
        total_size: int | None = None,
        interval: float | None = None,
        label: str = "Download",
    ):
        self._dest = dest
        self._logger = dest_logger
        self._total_size = total_size
        self._label = label
        self._interval = max(self._MIN_INTERVAL, interval) if interval is not None else self._interval_from_env()
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._start_time: float | None = None
        self._active = False

    # ------------------------------------------------------------------
    # Context manager
    # ------------------------------------------------------------------

    def __enter__(self) -> "ProgressReporter":
        if is_interactive():
            return self
        self._active = True
        self._start_time = time.time()
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):  # noqa: ANN001
        if not self._active:
            return
        self._stop.set()
        if self._thread is not None:
            self._thread.join(timeout=5.0)
        # Final summary
        elapsed = time.time() - self._start_time if self._start_time is not None else 0
        final_size = get_dir_size(self._dest)
        speed = final_size / elapsed if elapsed > 0 else 0
        if exc_type is None:
            self._logger.info(
                "%s completed: %s in %.0fs (%s/s)",
                self._label,
                format_size(final_size),
                elapsed,
                format_size(speed),
            )
        else:
            self._logger.warning(
                "%s aborted after %.0fs (%s transferred): %s",
                self._label,
                elapsed,
                format_size(final_size),
                exc_val,
            )

    # ------------------------------------------------------------------
    # Internal
    # ------------------------------------------------------------------

    def _run(self) -> None:
        prev_size = get_dir_size(self._dest)
        prev_time = time.time()
        while not self._stop.wait(self._interval):
            now = time.time()
            current_size = get_dir_size(self._dest)
            dt = now - prev_time
            speed = (current_size - prev_size) / dt if dt > 0 else 0
            if self._total_size and self._total_size > 0:
                pct = min(100.0, current_size / self._total_size * 100)
                self._logger.info(
                    "%s progress: %s / %s (%.1f%%) - %s/s",
                    self._label,
                    format_size(current_size),
                    format_size(self._total_size),
                    pct,
                    format_size(speed),
                )
            else:
                self._logger.info(
                    "%s progress: %s downloaded - %s/s",
                    self._label,
                    format_size(current_size),
                    format_size(speed),
                )
            prev_size = current_size
            prev_time = now

    @classmethod
    def _interval_from_env(cls) -> float:
        raw = os.environ.get("NEUTREE_DL_PROGRESS_INTERVAL")
        if raw:
            try:
                return max(cls._MIN_INTERVAL, float(raw))
            except (ValueError, TypeError):
                pass
        return cls._DEFAULT_INTERVAL
