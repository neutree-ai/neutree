from abc import ABC, abstractmethod
from typing import Optional, Dict, Any


class Downloader(ABC):
    """Abstract downloader interface.

    Implementations should provide a `download` method which accepts a
    resource identifier (string) and a destination path.
    """

    @abstractmethod
    def download(self, source: str, dest: str, *, credentials: Optional[Dict[str, str]] = None,
                 recursive: bool = True, overwrite: bool = False, retries: int = 3,
                 timeout: Optional[float] = None, metadata: Optional[Dict[str, Any]] = None) -> None:
        """Download the resource to dest.

        Args:
            source: identifier handled by the backend (e.g. hf repo id, s3:// URL, or local path)
            dest: local destination directory
            credentials: optional credentials map for the backend
            recursive: whether to download recursively (if applicable)
            overwrite: whether to overwrite existing files
            retries: number of retries for transient errors
            timeout: optional timeout in seconds
            metadata: optional high-level metadata (model_args) passed from orchestrator
        """

        raise NotImplementedError()
