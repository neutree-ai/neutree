"""Launch and supervise an unmodified engine image from a Ray Serve actor."""

from __future__ import annotations

import logging
import shlex
import subprocess
import threading
import time
from collections.abc import Callable, Mapping, Sequence
from typing import Any

logger = logging.getLogger("ray.serve")


def build_docker_run_command(
    *,
    container_name: str,
    image: str,
    run_options: Sequence[str],
    accelerator_ids: Sequence[str],
    environment: Mapping[str, str],
    engine_args: Sequence[str],
) -> list[str]:
    """Build an argv-only Docker command for a native engine sibling.

    ``run_options`` comes from the accelerator plugin and can contain quoted
    Docker values (notably NFS ``--mount`` settings), so it is tokenized with
    :mod:`shlex` but never passed through a shell. The plugin's broad GPU
    setting is deliberately replaced with Ray's per-actor allocation.
    """
    command = ["docker", "run", "--name", container_name, "--network", "host"]
    command.extend(_without_conflicting_options(_tokenize_run_options(run_options)))
    if accelerator_ids:
        command.extend(["--gpus", f"device={','.join(accelerator_ids)}"])
    for name in sorted(environment):
        command.extend(["--env", f"{name}={environment[name]}"])
    command.extend([image, *engine_args])
    return command


def _tokenize_run_options(run_options: Sequence[str]) -> list[str]:
    tokens: list[str] = []
    for option in run_options:
        tokens.extend(shlex.split(option))
    return tokens


def _without_conflicting_options(tokens: Sequence[str]) -> list[str]:
    result: list[str] = []
    index = 0
    value_options = {"--gpus", "--name", "--network", "--net"}
    while index < len(tokens):
        token = tokens[index]
        if token in value_options:
            index += 2
            continue
        if token.startswith(("--gpus=", "--name=", "--network=", "--net=")):
            index += 1
            continue
        if token in {"--detach", "-d"}:
            index += 1
            continue
        result.append(token)
        index += 1
    return result


class DockerEngineRunner:
    """Own one native engine container for the lifetime of a Ray actor."""

    def __init__(
        self,
        *,
        container_name: str,
        image: str,
        run_options: Sequence[str],
        accelerator_ids: Sequence[str],
        environment: Mapping[str, str],
        engine_args: Sequence[str],
        health_url: str,
        startup_timeout_s: float = 600.0,
        poll_interval_s: float = 1.0,
        command_runner: Callable[..., Any] = subprocess.run,
        process_factory: Callable[..., subprocess.Popen[str]] = subprocess.Popen,
        http_get: Callable[[str], Any] | None = None,
        sleeper: Callable[[float], None] = time.sleep,
    ) -> None:
        self.container_name = container_name
        self.image = image
        self.run_options = tuple(run_options)
        self.accelerator_ids = tuple(accelerator_ids)
        self.environment = dict(environment)
        self.engine_args = tuple(engine_args)
        self.health_url = health_url
        self.startup_timeout_s = startup_timeout_s
        self.poll_interval_s = poll_interval_s
        self._command_runner = command_runner
        self._process_factory = process_factory
        self._http_get = http_get or _http_get
        self._sleeper = sleeper
        self._engine_process: subprocess.Popen[str] | None = None
        self._log_thread: threading.Thread | None = None

    def start(self) -> None:
        self.stop()
        command = build_docker_run_command(
            container_name=self.container_name,
            image=self.image,
            run_options=self.run_options,
            accelerator_ids=self.accelerator_ids,
            environment=self.environment,
            engine_args=self.engine_args,
        )
        logger.info("starting native engine container %s from %s", self.container_name, self.image)
        self._engine_process = self._process_factory(
            command,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        self._log_thread = threading.Thread(target=self._relay_logs, daemon=True)
        self._log_thread.start()
        self._wait_until_healthy()

    def stop(self) -> None:
        if self._engine_process is not None:
            self._engine_process.terminate()
            self._engine_process = None
        if self._log_thread is not None:
            self._log_thread.join(timeout=2.0)
            self._log_thread = None
        self._command_runner(
            ["docker", "rm", "--force", self.container_name],
            check=False,
            capture_output=True,
            text=True,
        )

    def _wait_until_healthy(self) -> None:
        deadline = time.monotonic() + self.startup_timeout_s
        last_error: Exception | None = None
        while time.monotonic() < deadline:
            if self._engine_process is not None and self._engine_process.poll() is not None:
                self.stop()
                raise RuntimeError(f"native engine {self.container_name} exited before becoming healthy")
            try:
                response = self._http_get(self.health_url)
                if 200 <= response.status_code < 400:
                    return
            except Exception as exc:  # Health checks are expected to race startup.
                last_error = exc
            self._sleeper(self.poll_interval_s)
        self.stop()
        message = f"native engine {self.container_name} did not become healthy at {self.health_url}"
        if last_error is not None:
            raise RuntimeError(message) from last_error
        raise RuntimeError(message)

    def _relay_logs(self) -> None:
        if self._engine_process is None or self._engine_process.stdout is None:
            return
        for line in self._engine_process.stdout:
            logger.info("[native-engine:%s] %s", self.container_name, line.rstrip())


def _http_get(url: str) -> Any:
    import httpx

    return httpx.get(url, timeout=3.0)
