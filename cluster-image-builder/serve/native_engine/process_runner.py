"""Run an engine server as a direct child of a Ray Serve actor."""

from __future__ import annotations

import logging
import subprocess
import sys
import time
from collections.abc import Callable, Sequence
from typing import Any

logger = logging.getLogger("ray.serve")


class EngineExitedBeforeReady(RuntimeError):
    """The server process exited before its health endpoint was available."""


class DirectEngineRunner:
    """Start one engine process without detaching it from the Actor group.

    This class intentionally has no stop method. On Actor exit Ray's process
    group cleanup owns termination of this process and every descendant.
    """

    def __init__(
        self,
        *,
        command: Sequence[str],
        health_url: str,
        startup_timeout_s: float = 600.0,
        poll_interval_s: float = 1.0,
        process_factory: Callable[..., Any] = subprocess.Popen,
        http_get: Callable[[str], Any] | None = None,
        sleeper: Callable[[float], None] = time.sleep,
    ) -> None:
        self._command = list(command)
        self._health_url = health_url
        self._startup_timeout_s = startup_timeout_s
        self._poll_interval_s = poll_interval_s
        self._process_factory = process_factory
        self._http_get = http_get or _http_get
        self._sleeper = sleeper
        self._process: Any | None = None

    def start(self) -> None:
        if self._process is not None:
            return
        logger.info("starting native engine process: %s", self._command)
        print(f"[native-engine] starting direct process: {self._command}", flush=True)
        self._process = self._process_factory(
            self._command,
            stdout=sys.stdout,
            stderr=sys.stderr,
            # A new session would detach vLLM from the Ray worker's process
            # group and prevent Ray cleanup from owning the full lifecycle.
            start_new_session=False,
        )
        self._wait_until_healthy()

    def _wait_until_healthy(self) -> None:
        deadline = time.monotonic() + self._startup_timeout_s
        last_error: Exception | None = None
        while time.monotonic() < deadline:
            if self._process is not None and self._process.poll() is not None:
                raise EngineExitedBeforeReady("native engine exited before becoming healthy")
            try:
                response = self._http_get(self._health_url)
                if 200 <= response.status_code < 400:
                    return
            except Exception as exc:  # Health checks are expected to race startup.
                last_error = exc
            self._sleeper(self._poll_interval_s)
        message = f"native engine did not become healthy at {self._health_url}"
        if last_error is not None:
            raise RuntimeError(message) from last_error
        raise RuntimeError(message)


def _http_get(url: str) -> Any:
    import httpx

    return httpx.get(url, timeout=3.0)
