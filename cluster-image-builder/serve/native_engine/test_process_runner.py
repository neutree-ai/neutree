from __future__ import annotations

import sys

import pytest

from serve.native_engine.process_runner import DirectEngineRunner, EngineExitedBeforeReady


class FakeProcess:
    def __init__(self) -> None:
        self.terminated = False

    def poll(self) -> None:
        return None

    def terminate(self) -> None:
        self.terminated = True

    def wait(self, timeout: float | None = None) -> None:
        del timeout
        return None


class FakeResponse:
    status_code = 200


def test_runner_starts_engine_as_actor_child_and_relays_logs_directly() -> None:
    calls: list[tuple[list[str], dict[str, object]]] = []
    process = FakeProcess()

    def process_factory(command: list[str], **kwargs: object) -> FakeProcess:
        calls.append((command, kwargs))
        return process

    runner = DirectEngineRunner(
        command=["vllm", "serve", "--model", "/models/demo", "--port", "30000"],
        health_url="http://127.0.0.1:30000/health",
        process_factory=process_factory,
        http_get=lambda _url: FakeResponse(),
    )

    runner.start()

    assert calls == [
        (
            ["vllm", "serve", "--model", "/models/demo", "--port", "30000"],
            {
                "stderr": sys.stderr,
                "stdout": sys.stdout,
                "start_new_session": False,
            },
        )
    ]
    assert process.terminated is False
    assert not hasattr(runner, "stop")


def test_runner_leaves_cleanup_to_ray_when_the_engine_exits_before_ready() -> None:
    class ExitedProcess(FakeProcess):
        def poll(self) -> int:
            return 1

    process = ExitedProcess()
    runner = DirectEngineRunner(
        command=["vllm", "serve", "--port", "30000"],
        health_url="http://127.0.0.1:30000/health",
        process_factory=lambda *_args, **_kwargs: process,
    )

    with pytest.raises(EngineExitedBeforeReady):
        runner.start()

    assert process.terminated is False
