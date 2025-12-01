from dataclasses import dataclass
from typing import Optional, Dict, Any


@dataclass
class DownloadRequest:
    """Low-level download request used by backend implementations.

    This should contain only low-level parameters required to perform a download.
    High-level model arguments (modelArgs) should be parsed by main/dispatcher into
    this structure before calling the backend.
    """
    # low-level source identifier (repo id, http:// url, or local path)
    source: str
    # local destination path
    dest: str = "/models"
    # generic credentials map for extensible auth (e.g., {"token":"..."})
    credentials: Optional[Dict[str, str]] = None
    recursive: bool = True
    overwrite: bool = False
    retries: int = 3
    timeout: Optional[float] = None
    # optional metadata retained for logging or advanced policies
    metadata: Optional[Dict[str, Any]] = None
