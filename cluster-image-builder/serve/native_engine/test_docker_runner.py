from __future__ import annotations

from io import StringIO

from serve.native_engine.docker_runner import DockerEngineRunner, build_docker_run_command


class FakeProcess:
    def __init__(self) -> None:
        self.stdout = StringIO("vLLM server started\\n")
        self.terminated = False

    def poll(self) -> None:
        return None

    def terminate(self) -> None:
        self.terminated = True


class FakeResponse:
    status_code = 200


def test_build_docker_run_command_uses_exact_ray_gpu_ids_and_preserves_options() -> None:
    command = build_docker_run_command(
        container_name="neutree-native-default-chat-replica-1",
        image="vllm/vllm-openai:v0.24.0",
        run_options=[
            "--runtime=nvidia",
            "--gpus all",
            "-v /data/models:/mnt/neutree/models/default",
            "--rm",
        ],
        accelerator_ids=["2", "7"],
        environment={"HF_TOKEN": "test-token", "VLLM_LOGGING_LEVEL": "INFO"},
        engine_args=[
            "--model",
            "/mnt/neutree/models/default/Qwen/Qwen2.5-0.5B-Instruct/main",
            "--served-model-name",
            "chat-model",
            "--host",
            "127.0.0.1",
            "--port",
            "18000",
        ],
    )

    assert command == [
        "docker",
        "run",
        "--name",
        "neutree-native-default-chat-replica-1",
        "--network",
        "host",
        "--runtime=nvidia",
        "-v",
        "/data/models:/mnt/neutree/models/default",
        "--rm",
        "--gpus",
        "device=2,7",
        "--env",
        "HF_TOKEN=test-token",
        "--env",
        "VLLM_LOGGING_LEVEL=INFO",
        "vllm/vllm-openai:v0.24.0",
        "--model",
        "/mnt/neutree/models/default/Qwen/Qwen2.5-0.5B-Instruct/main",
        "--served-model-name",
        "chat-model",
        "--host",
        "127.0.0.1",
        "--port",
        "18000",
    ]


def test_build_docker_run_command_replaces_equals_form_of_gpu_option() -> None:
    command = build_docker_run_command(
        container_name="neutree-native-cpu",
        image="example/native:latest",
        run_options=["--gpus=all", "--mount 'type=volume,dst=/mnt/models'"],
        accelerator_ids=[],
        environment={},
        engine_args=["--help"],
    )

    assert "--gpus=all" not in command
    assert "--gpus" not in command
    assert command == [
        "docker",
        "run",
        "--name",
        "neutree-native-cpu",
        "--network",
        "host",
        "--mount",
        "type=volume,dst=/mnt/models",
        "example/native:latest",
        "--help",
    ]


def test_runner_keeps_native_container_attached_and_removes_it_on_stop() -> None:
    commands: list[list[str]] = []
    spawned: list[list[str]] = []
    process = FakeProcess()

    def command_runner(command: list[str], **_kwargs: object) -> None:
        commands.append(command)

    def process_factory(command: list[str], **_kwargs: object) -> FakeProcess:
        spawned.append(command)
        return process

    runner = DockerEngineRunner(
        container_name="neutree-native-demo",
        image="vllm/vllm-openai:v0.24.0",
        run_options=["--rm", "--gpus all"],
        accelerator_ids=["3"],
        environment={},
        engine_args=["/models/demo", "--port", "18000"],
        health_url="http://127.0.0.1:18000/health",
        command_runner=command_runner,
        process_factory=process_factory,
        http_get=lambda _url: FakeResponse(),
    )

    runner.start()
    runner.stop()

    assert spawned == [
        [
            "docker",
            "run",
            "--name",
            "neutree-native-demo",
            "--network",
            "host",
            "--rm",
            "--gpus",
            "device=3",
            "vllm/vllm-openai:v0.24.0",
            "/models/demo",
            "--port",
            "18000",
        ]
    ]
    assert process.terminated is True
    assert commands == [
        ["docker", "rm", "--force", "neutree-native-demo"],
        ["docker", "rm", "--force", "neutree-native-demo"],
    ]
