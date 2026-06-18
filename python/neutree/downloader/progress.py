"""Log-based progress reporting for non-interactive model downloads."""

import math
import os
import sys
import threading
import time
from typing import Optional

DEFAULT_PROGRESS_INTERVAL = 30.0
MIN_PROGRESS_INTERVAL = 1.0
PROGRESS_INTERVAL_ENV = "NEUTREE_DL_PROGRESS_INTERVAL"


def is_interactive() -> bool:
    """Return true when either stdout or stderr is attached to a terminal."""
    for stream in (sys.stdout, sys.stderr):
        isatty = getattr(stream, "isatty", None)
        try:
            if callable(isatty) and isatty():
                return True
        except Exception:
            continue
    return False


def format_size(size: int) -> str:
    """Format a byte count using binary units."""
    size = max(0, int(size))
    if size < 1024:
        return f"{size} B"

    value = float(size)
    for unit in ("KiB", "MiB", "GiB", "TiB"):
        value /= 1024.0
        if value < 1024.0 or unit == "TiB":
            return f"{value:.1f} {unit}"

    return f"{value:.1f} TiB"


def get_dir_size(path: str) -> int:
    """Return total file size for a file or directory, ignoring transient races."""
    try:
        if os.path.isfile(path):
            return os.path.getsize(path)
    except OSError:
        return 0

    total = 0
    if not os.path.isdir(path):
        return total

    for root, _, files in os.walk(path):
        for name in files:
            file_path = os.path.join(root, name)
            try:
                total += os.path.getsize(file_path)
            except OSError:
                continue
    return total


def _valid_interval(value: object) -> Optional[float]:
    try:
        interval = float(value)
    except (TypeError, ValueError):
        return None

    if not math.isfinite(interval) or interval <= 0:
        return None
    return max(MIN_PROGRESS_INTERVAL, interval)


def _resolve_interval(interval: Optional[float]) -> float:
    if interval is not None:
        return _valid_interval(interval) or DEFAULT_PROGRESS_INTERVAL

    env_value = os.environ.get(PROGRESS_INTERVAL_ENV)
    if env_value is None:
        return DEFAULT_PROGRESS_INTERVAL
    return _valid_interval(env_value) or DEFAULT_PROGRESS_INTERVAL


class ProgressReporter:
    """Periodically log destination size for non-TTY downloads."""

    def __init__(
            self,
            path: str,
            logger,
            *,
            label: str = "Download",
            total_size: Optional[int] = None,
            interval: Optional[float] = None,
            interactive: Optional[bool] = None):
        self.path = path
        self.logger = logger
        self.label = label
        self.total_size = total_size if total_size and total_size > 0 else None
        self.interval = _resolve_interval(interval)
        self.interactive = is_interactive() if interactive is None else interactive
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._started_at: Optional[float] = None
        self._baseline_size = 0

    def __enter__(self):
        if self.interactive:
            return self

        self._started_at = time.time()
        self._baseline_size = get_dir_size(self.path)
        self._log_progress()
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        return self

    def __exit__(self, exc_type, exc, _tb):
        if self.interactive:
            return False

        self._stop.set()
        if self._thread:
            self._thread.join(timeout=1.0)

        elapsed = time.time() - self._started_at if self._started_at else 0.0
        size = self._downloaded_size()
        status = "aborted" if exc_type else "completed"
        self.logger.info(
            f"{self.label} {status}: {format_size(size)} downloaded in {elapsed:.1f}s")
        return False

    def _run(self) -> None:
        while not self._stop.wait(self.interval):
            self._log_progress()

    def _log_progress(self) -> None:
        try:
            size = self._downloaded_size()
        except Exception as exc:
            self.logger.debug(f"{self.label} progress unavailable: {exc}")
            return

        if self.total_size:
            pct = min(100.0, (size / self.total_size) * 100.0)
            self.logger.info(
                f"{self.label} progress: {format_size(size)} / "
                f"{format_size(self.total_size)} ({pct:.1f}%)")
            return

        self.logger.info(f"{self.label} progress: {format_size(size)} downloaded")

    def _downloaded_size(self) -> int:
        return max(0, get_dir_size(self.path) - self._baseline_size)
