import os
import sys
import time
from typing import Optional, Dict, Any

from .base import Downloader
from .utils import ensure_dir


class _LoggingProgressBar:
    """Custom progress tracker for non-TTY environments.

    Outputs progress logs at regular intervals instead of updating a single line.
    Compatible with tqdm interface for use with huggingface_hub.

    Note: Tracks bytes downloaded, not file count.
    """

    def __init__(self, *args, **kwargs):
        self.total = kwargs.get('total', 0)
        self.desc = kwargs.get('desc', 'Downloading')
        self.unit = kwargs.get('unit', 'B')
        self.unit_scale = kwargs.get('unit_scale', False)
        self.n = 0
        self.last_log_time = time.time()
        self.log_interval = 10.0  # Log every 10 seconds
        self.last_percent = 0

        # Format initial message
        if self.unit == 'B' and self.unit_scale:
            if self.total > 0:
                total_mb = self.total / (1024 * 1024)
                print(f"[Downloader] {self.desc}: Starting (total: {total_mb:.1f} MB)", file=sys.stderr)
            else:
                print(f"[Downloader] {self.desc}: Starting (size unknown)", file=sys.stderr)
        else:
            print(f"[Downloader] {self.desc}: Starting", file=sys.stderr)

    def update(self, n=1):
        """Update progress counter."""
        self.n += n
        current_time = time.time()

        # Calculate progress percentage
        if self.total > 0:
            percent = int((self.n / self.total) * 100)

            # Log if: 10 seconds passed OR percentage increased by 20% OR completed
            time_elapsed = current_time - self.last_log_time >= self.log_interval
            percent_changed = percent - self.last_percent >= 20
            completed = self.n >= self.total

            if time_elapsed or percent_changed or completed:
                if self.unit == 'B' and self.unit_scale:
                    # Format as MB for readability
                    current_mb = self.n / (1024 * 1024)
                    total_mb = self.total / (1024 * 1024)
                    print(
                        f"[Downloader] {self.desc}: {current_mb:.1f} / {total_mb:.1f} MB ({percent}%)",
                        file=sys.stderr
                    )
                else:
                    print(
                        f"[Downloader] {self.desc}: {self.n}/{self.total} ({percent}%)",
                        file=sys.stderr
                    )
                self.last_log_time = current_time
                self.last_percent = percent
        else:
            # Unknown total, just log every interval
            if current_time - self.last_log_time >= self.log_interval:
                if self.unit == 'B' and self.unit_scale:
                    current_mb = self.n / (1024 * 1024)
                    print(f"[Downloader] {self.desc}: {current_mb:.1f} MB downloaded", file=sys.stderr)
                else:
                    print(f"[Downloader] {self.desc}: {self.n} items", file=sys.stderr)
                self.last_log_time = current_time

    def close(self):
        """Called when download completes."""
        if self.n > 0:
            if self.unit == 'B' and self.unit_scale:
                final_mb = self.n / (1024 * 1024)
                print(f"[Downloader] {self.desc}: Completed {final_mb:.1f} MB", file=sys.stderr)
            else:
                print(f"[Downloader] {self.desc}: Completed", file=sys.stderr)

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()


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

        print(f"[Downloader] Starting download from HuggingFace", file=sys.stderr)
        print(f"[Downloader]   Repository: {repo_id}", file=sys.stderr)
        print(f"[Downloader]   Destination: {dest}", file=sys.stderr)
        if allow_pattern:
            print(f"[Downloader]   Pattern: {allow_pattern}", file=sys.stderr)
        if version:
            print(f"[Downloader]   Revision: {version}", file=sys.stderr)

        # Check if running in TTY environment
        is_tty = sys.stderr.isatty()

        # For non-TTY environments (kubectl logs, docker logs, CI/CD)
        # Use custom progress tracker that outputs incremental logs
        tqdm_class = None
        if not is_tty:
            # Disable default progress bars and use our custom logger
            if "HF_HUB_DISABLE_PROGRESS_BARS" not in os.environ:
                os.environ["HF_HUB_DISABLE_PROGRESS_BARS"] = "1"
            tqdm_class = _LoggingProgressBar
            print(f"[Downloader]   Using incremental progress logging (non-TTY)", file=sys.stderr)

        hf.snapshot_download(
            repo_id=repo_id,
            allow_patterns=allow_pattern,
            local_dir=dest,
            token=token,
            revision=version,
            tqdm_class=tqdm_class
        )

        print(f"[Downloader] Download completed: {dest}", file=sys.stderr)
